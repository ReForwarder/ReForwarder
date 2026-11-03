// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Posta.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package automation

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/webhook"
)

// The automation webhook guard, end to end (issue #7): the reject matrix
// through WebhookClient.Post, redirect no-follow, DNS-rebinding resistance,
// and the engine's write-boundary sanitization. Every test stays inside the
// process: resolvers are stubbed, transports rewrite or abort dials, and no
// real external IP is ever contacted.

const (
	// webhookFixtureHost is the hostname the webhook fixtures point at: a
	// plain name that passes the guard's static checks (not localhost, not an
	// IP literal — any IP literal, TEST-NET-1 included, is rejected inside
	// ResolvePublicURL, which no test seam relaxes). The stub resolver is what
	// answers it, making resolution explicit and deterministic.
	webhookFixtureHost = "webhook.example.test"
)

// webhookPublicAnswer is the address the stub resolver hands out for the
// fixture hostname: a public unicast address, so it passes the guard's address
// policy. It is never dialed — the test transports rewrite or abort every
// dial — it only has to be policy-valid.
var webhookPublicAnswer = net.IPAddr{IP: net.ParseIP("93.184.216.34")}

// fakeResolver is a deterministic, network-free Resolver stub. Every lookup is
// counted and answered by the scripted answer function.
type fakeResolver struct {
	mu     sync.Mutex
	calls  int
	answer func(nth int, host string) ([]net.IPAddr, error)
}

func (r *fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.answer(r.calls, host)
}

func (r *fakeResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// alwaysPublicAnswer answers every lookup with the fixture's public address.
func alwaysPublicAnswer(int, string) ([]net.IPAddr, error) {
	return []net.IPAddr{webhookPublicAnswer}, nil
}

// failingTransport is an http.RoundTripper that fails every request with a
// fixed error — the deterministic stand-in for a raw dial failure.
type failingTransport struct{ err error }

func (t failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// ---------- reject matrix ----------

// TestWebhookClientRejectsPolicyDestinations runs the issue #7 reject matrix
// through WebhookClient.Post with the production guard armed: every URL must
// die in the static checks — before resolution, before any network activity.
func TestWebhookClientRejectsPolicyDestinations(t *testing.T) {
	w := NewWebhookClient()
	// A resolver that fails the test if it is ever reached: the whole matrix
	// is static, so a lookup (let alone a connection) means a hole.
	w.resolver = &fakeResolver{answer: func(int, string) ([]net.IPAddr, error) {
		t.Error("a policy-blocked URL must be rejected before resolution")
		return nil, errors.New("resolver must not be reached")
	}}

	for _, tc := range []struct {
		name string
		url  string
	}{
		{"CGNAT shared space", "http://100.64.0.1/"},
		{"this-network", "http://0.0.0.1/"},
		{"TEST-NET-1", "http://192.0.2.55/"},
		{"reserved", "http://240.0.0.1/"},
		{"documentation v6", "http://[2001:db8::1]/"},
		{"embedded credentials", "http://u:p@example.com/"},
		{"localhost name", "http://localhost/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body, err := w.Post(context.Background(), tc.url, nil, []byte("{}"), "")
			if err == nil {
				t.Fatalf("Post(%q) = (%d, %q), want a static rejection", tc.url, status, body)
			}
			if status != 0 || body != "" {
				t.Errorf("a rejected delivery must not report a response, got (%d, %q)", status, body)
			}
			if strings.Contains(tc.url, "u:p@") {
				// Credentials are a plain static error, not the policy sentinel.
				if !strings.Contains(err.Error(), "credentials") {
					t.Errorf("error = %v, want the credentials rejection", err)
				}
				return
			}
			if !errors.Is(err, webhook.ErrPolicyBlocked) {
				t.Errorf("error = %v, want ErrPolicyBlocked", err)
			}
		})
	}
}

// ---------- redirect no-follow ----------

// TestWebhookNodeDoesNotFollowRedirects proves a 3xx is the final response:
// the redirect target (an internal absolute URL here — exactly where an
// attacker would send it) must never be contacted, and the node layer fails
// with the bare "HTTP 302" notation the engine stores verbatim.
func TestWebhookNodeDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/target", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/target", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e, _ := webhookTestEngine(t, srv)
	nc := testNodeContext(t)
	node := testNode("wh", NodeTypeWebhook, map[string]any{
		"url": "http://" + webhookFixtureHost + "/redirect",
	}, nil)

	_, err := e.executeWebhookNode(context.Background(), nc, node)
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("a 302 must be the final response and fail the node with HTTP 302, got %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("the redirect target must never be contacted, got %d hits", got)
	}
}

// ---------- DNS rebinding ----------

// TestWebhookClientRebindingBlocked pins the rebinding defense: the pre-flight
// resolution answers public, but the guarded transport re-resolves on every
// connection and the attacker has rebinded to loopback — the dial must be
// blocked before any connection is opened. A second call where the rebind has
// moved to lookup time itself must fail the pre-flight. The pre-flight
// resolver (WebhookClient.resolver) and the transport's resolver are stubbed
// separately — the transport captures its own resolver at construction — so
// the two resolution points can be scripted exactly and stay off the network.
func TestWebhookClientRebindingBlocked(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	preflight := &fakeResolver{answer: func(nth int, _ string) ([]net.IPAddr, error) {
		if nth == 1 {
			return []net.IPAddr{webhookPublicAnswer}, nil // first pre-flight: public
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil // second: rebinded
	}}
	transportResolver := &fakeResolver{answer: func(int, string) ([]net.IPAddr, error) {
		// Every dial-time re-resolution sees the attacker's loopback answer.
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}}

	w := NewWebhookClient()
	w.resolver = preflight
	w.client.Transport = webhook.NewGuardedTransport(transportResolver)

	status, _, err := w.Post(context.Background(), "http://rebind.test/hook", nil, []byte("{}"), "")
	if err == nil || !errors.Is(err, webhook.ErrPolicyBlocked) {
		t.Fatalf("the rebound dial must be blocked by policy, got status %d, err %v", status, err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("no connection may reach the loopback listener, got %d hits", got)
	}
	if got := preflight.callCount(); got != 1 {
		t.Errorf("pre-flight lookups = %d, want 1", got)
	}
	if got := transportResolver.callCount(); got != 1 {
		t.Errorf("dial-time lookups = %d, want 1 (the blocked attempt)", got)
	}

	// Second call: the rebind now shows up at resolution time, so the
	// pre-flight itself must refuse the destination before the request is
	// even built — the transport is never consulted again.
	status, _, err = w.Post(context.Background(), "http://rebind.test/hook", nil, []byte("{}"), "")
	if err == nil || !errors.Is(err, webhook.ErrPolicyBlocked) {
		t.Fatalf("a resolver answering loopback must fail the pre-flight, got status %d, err %v", status, err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("still no connection may reach the loopback listener, got %d hits", got)
	}
	if got := preflight.callCount(); got != 2 {
		t.Errorf("pre-flight lookups = %d, want 2", got)
	}
	if got := transportResolver.callCount(); got != 1 {
		t.Errorf("dial-time lookups = %d, want 1 (still no second attempt)", got)
	}
}

// ---------- write-boundary sanitization ----------

// TestWebhookNodeErrorSanitizedAtWriteBoundary drives a webhook node failure
// with a raw dial error carrying an internal address through the engine and
// asserts that neither persisted copy — the run row's error nor the journey's
// last_error — leaks it: both must carry the guard's canonical text. The
// server log keeps the raw error by design (not asserted here).
func TestWebhookNodeErrorSanitizedAtWriteBoundary(t *testing.T) {
	e, state, _, _, clock := newTestEngine(t)
	listID := uint(3)
	nodes := []*models.AutomationNode{
		triggerNode(strPtr("wh")),
		testNode("wh", NodeTypeWebhook, map[string]any{
			"url": "http://" + webhookFixtureHost + "/hook",
		}, nil),
	}
	auto := seedAutomation(t, state, "Hook", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, nodes))
	seedSubscriber(t, state)

	client := NewWebhookClient()
	// Production guard settings; the fixture hostname passes the static gate
	// and the stub resolver answers it with a public address. The swapped-in
	// transport then fails the dial with the raw error shape a legacy
	// delivery produced — internal address and port included.
	client.resolver = &fakeResolver{answer: alwaysPublicAnswer}
	client.client.Transport = failingTransport{
		err: errors.New("dial tcp 127.0.0.1:8080: connect: connection refused"),
	}
	e.SetWebhookClient(client)

	now := clock.Now()
	journeyID := seedJourney(t, state, auto, "t", &now, nil)
	if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
		t.Fatalf("a node error must be contained, got %v", err)
	}

	want := webhook.SanitizeDeliveryError("dial tcp 127.0.0.1:8080: connect: connection refused")

	lastErr, _ := journeyRow(t, state, journeyID)["last_error"].(string)
	if lastErr == "" {
		t.Fatal("the journey must record last_error")
	}
	if strings.Contains(lastErr, "127.0.0.1") || strings.Contains(lastErr, "8080") {
		t.Errorf("journey last_error = %q, must not carry the raw dial detail", lastErr)
	}
	if lastErr != want {
		t.Errorf("journey last_error = %q, want the canonical text %q", lastErr, want)
	}

	var runErr string
	for _, r := range state.tables["automation_node_runs"].rows {
		if r[state.colIndex("automation_node_runs", "node_id")].(string) == "wh" &&
			r[state.colIndex("automation_node_runs", "action")].(string) == models.AutomationRunActionFailed {
			runErr = r[state.colIndex("automation_node_runs", "error")].(string)
		}
	}
	if runErr == "" {
		t.Fatal("the failed run row must record an error")
	}
	if strings.Contains(runErr, "127.0.0.1") || strings.Contains(runErr, "8080") {
		t.Errorf("run error = %q, must not carry the raw dial detail", runErr)
	}
	if runErr != want {
		t.Errorf("run error = %q, want the canonical text %q", runErr, want)
	}
}

// ---------- workflow shape ----------

// TestWorkflowHasWebhookNode covers the pure half of the handler's scope gate:
// a workflow containing a webhook node is detected, a workflow without one is
// not, and a nil document is safe. (The handler-level gate composes this with
// middlewares.IsAPIKey and the scope list, which have no automation-package
// test seam.)
func TestWorkflowHasWebhookNode(t *testing.T) {
	doc, err := ParseWorkflow("t", `[{"id":"t","type":"trigger"},{"id":"wh","type":"webhook","config":{"url":"https://example.com/hook"}}]`)
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	if !doc.HasWebhookNode() {
		t.Error("a workflow with a webhook node must be detected")
	}

	plain, err := ParseWorkflow("t", `[{"id":"t","type":"trigger"},{"id":"e1","type":"email","config":{"template_id":1}}]`)
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	if plain.HasWebhookNode() {
		t.Error("a workflow without a webhook node must not be flagged")
	}

	var nilDoc *WorkflowDoc
	if nilDoc.HasWebhookNode() {
		t.Error("a nil document must not be flagged")
	}
}
