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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package automation

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// The node executors exercised one by one: the executors are called through
// the engine (so the real list membership repo and webhook client run), with
// the email sender stubbed and the database faked.

func testNodeContext(t *testing.T, state *engState, email string) *nodeContext {
	t.Helper()
	sub := &models.Subscriber{
		ID: 9, UserID: 7, WorkspaceID: uintPtr(3), Email: email,
		Name:   "Alice Anderson",
		Status: models.SubscriberStatusSubscribed,
		CustomFields: models.CustomFields{
			"plan": "pro",
		},
	}
	return &nodeContext{
		automation: &models.Automation{
			ID: 5, UserID: 7, WorkspaceID: uintPtr(3), Name: "Welcome Flow", ListID: uintPtr(3),
		},
		journey: &models.AutomationJourney{
			ID: 11, AutomationID: 5, SubscriberID: 9, Email: email,
			ContextJSON: `{"order_id":"A-1","source":"api"}`,
		},
		subscriber: sub,
		workflow:   &WorkflowDoc{},
		properties: map[string]any{"order_id": "A-1", "source": "api"},
	}
}

// ---------- email node ----------

func TestEmailNodeBuildsDataAndRecordsUUID(t *testing.T) {
	e, state, _, sender, _ := newTestEngine(t)
	nc := testNodeContext(t, state, "alice@example.com")
	node := testNode("e1", NodeTypeEmail, map[string]any{
		"template_id":      uint(42),
		"subject_override": "Your order A-1",
		"from_override":    "shop@example.com",
	}, nil)

	res, err := e.executeEmailNode(context.Background(), nc, node)
	if err != nil {
		t.Fatalf("executeEmailNode: %v", err)
	}
	if res.Output["email_uuid"] != "em-uuid-1" {
		t.Errorf("output email_uuid = %v, want em-uuid-1", res.Output["email_uuid"])
	}
	if res.Branch != "" {
		t.Errorf("email node branch = %q, want empty (linear)", res.Branch)
	}

	if sender.callCount() != 1 {
		t.Fatalf("sender calls = %d, want 1", sender.callCount())
	}
	call := sender.lastCall()
	if call.automationID != 5 || call.templateID != 42 {
		t.Errorf("automation/template = %d/%d, want 5/42", call.automationID, call.templateID)
	}
	if call.subjectOverride != "Your order A-1" || call.fromOverride != "shop@example.com" {
		t.Errorf("overrides = %q / %q", call.subjectOverride, call.fromOverride)
	}
	if call.toEmail != "alice@example.com" {
		t.Errorf("toEmail = %q", call.toEmail)
	}

	// The render data document: contact fields, automation, event properties
	// both flattened and nested, and the custom fields.
	if call.data["contact_email"] != "alice@example.com" {
		t.Errorf("contact_email = %v", call.data["contact_email"])
	}
	if call.data["contact_name"] != "Alice Anderson" {
		t.Errorf("contact_name = %v", call.data["contact_name"])
	}
	if call.data["contact_status"] != string(models.SubscriberStatusSubscribed) {
		t.Errorf("contact_status = %v", call.data["contact_status"])
	}
	if call.data["automation_name"] != "Welcome Flow" {
		t.Errorf("automation_name = %v", call.data["automation_name"])
	}
	if call.data["order_id"] != "A-1" || call.data["source"] != "api" {
		t.Errorf("event properties must be flattened top-level, got order_id=%v source=%v", call.data["order_id"], call.data["source"])
	}
	ev, ok := call.data["event"].(map[string]any)
	if !ok || ev["order_id"] != "A-1" {
		t.Errorf("data.event = %v, want the journey context", call.data["event"])
	}
	cf, ok := call.data["custom_fields"].(map[string]any)
	if !ok || cf["plan"] != "pro" {
		t.Errorf("custom_fields = %v, want the subscriber's custom fields", call.data["custom_fields"])
	}
	// automation_id is numeric and reserved (event properties must not
	// overwrite the contact identity).
	if id, ok := call.data["automation_id"].(uint); !ok || id != 5 {
		t.Errorf("automation_id = %v (%T), want uint 5", call.data["automation_id"], call.data["automation_id"])
	}
}

func TestEmailNodeErrorsWithoutTemplateOrSender(t *testing.T) {
	e, state, _, sender, _ := newTestEngine(t)
	nc := testNodeContext(t, state, "alice@example.com")

	// Missing template_id.
	node := testNode("e1", NodeTypeEmail, map[string]any{}, nil)
	if _, err := e.executeEmailNode(context.Background(), nc, node); err == nil {
		t.Error("a missing template_id must fail the node")
	}

	// No sender wired.
	node = testNode("e1", NodeTypeEmail, map[string]any{"template_id": uint(1)}, nil)
	e.emailSender = nil
	if _, err := e.executeEmailNode(context.Background(), nc, node); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("a nil sender must fail with a clear error, got %v", err)
	}

	// Sender failure propagates as a node error.
	e.emailSender = sender
	sender.err = errors.New("provider down")
	if _, err := e.executeEmailNode(context.Background(), nc, node); err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Errorf("sender errors must surface as node errors, got %v", err)
	}
}

// ---------- filter node ----------

func TestFilterNodeBranchesOnConditions(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	nc := testNodeContext(t, state, "alice@example.com")

	matchNode := testNode("f", NodeTypeFilter, map[string]any{
		"conditions":       []any{map[string]any{"field": "email", "operator": "contains", "value": "ALICE"}},
		"continue_node_id": "keep",
		"exit_node_id":     "bye",
	}, nil)
	res, err := e.executeFilterNode(nc, matchNode)
	if err != nil {
		t.Fatalf("executeFilterNode: %v", err)
	}
	if res.Branch != BranchContinue {
		t.Errorf("branch = %q, want continue", res.Branch)
	}
	if res.Output["matched"] != true {
		t.Errorf("output matched = %v, want true", res.Output["matched"])
	}

	missNode := testNode("f", NodeTypeFilter, map[string]any{
		"conditions":       []any{map[string]any{"field": "email", "operator": "equals", "value": "bob@example.com"}},
		"continue_node_id": "keep",
		"exit_node_id":     "bye",
	}, nil)
	res, err = e.executeFilterNode(nc, missNode)
	if err != nil {
		t.Fatalf("executeFilterNode: %v", err)
	}
	if res.Branch != BranchExit {
		t.Errorf("branch = %q, want exit", res.Branch)
	}

	// in_list rides the real membership repo: a member continues.
	e2, state2, _, _, _ := newTestEngine(t)
	engSeed(t, state2, "subscriber_list_members", map[string]any{"list_id": uint(4), "subscriber_id": uint(9)})
	nc2 := testNodeContext(t, state2, "alice@example.com")
	listNode := testNode("f", NodeTypeFilter, map[string]any{
		"conditions":       []any{map[string]any{"field": "in_list", "operator": "equals", "value": 4}},
		"continue_node_id": "keep",
		"exit_node_id":     "bye",
	}, nil)
	res, err = e2.executeFilterNode(nc2, listNode)
	if err != nil {
		t.Fatalf("executeFilterNode with in_list: %v", err)
	}
	if res.Branch != BranchContinue {
		t.Errorf("in_list member branch = %q, want continue", res.Branch)
	}
}

// ---------- ab_test node ----------

// expectedABRoll mirrors the executor's FNV-1a roll for a contact+node pair.
func expectedABRoll(email, nodeID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(email))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(nodeID))
	return int(h.Sum32() % 100)
}

func TestABTestNodeDeterministicAssignment(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	nc := testNodeContext(t, state, "alice@example.com")
	node := testNode("ab", NodeTypeABTest, map[string]any{"variants": []any{
		map[string]any{"id": "a", "name": "A", "weight": 50, "next_node_id": "na"},
		map[string]any{"id": "b", "name": "B", "weight": 50, "next_node_id": "nb"},
	}}, nil)

	res, err := e.executeABTestNode(nc, node)
	if err != nil {
		t.Fatalf("executeABTestNode: %v", err)
	}

	// Deterministic: the same contact always lands in the same variant.
	again, err := e.executeABTestNode(nc, node)
	if err != nil {
		t.Fatalf("second executeABTestNode: %v", err)
	}
	if again.Branch != res.Branch {
		t.Fatalf("assignment must be deterministic: %q then %q", res.Branch, again.Branch)
	}

	// The branch matches the documented FNV roll against the weights.
	roll := expectedABRoll(nc.journey.Email, node.ID)
	want := "b"
	if roll < 50 {
		want = "a"
	}
	if res.Branch != want {
		t.Errorf("branch = %q (roll %d), want %q", res.Branch, roll, want)
	}
	if res.Output["roll"] != roll {
		t.Errorf("output roll = %v, want %d", res.Output["roll"], roll)
	}

	// A different node id may (and generally will) land elsewhere; the two
	// rolls must at least be computed independently.
	otherNode := testNode("ab2", NodeTypeABTest, map[string]any{"variants": []any{
		map[string]any{"id": "a", "name": "A", "weight": 50, "next_node_id": "na"},
		map[string]any{"id": "b", "name": "B", "weight": 50, "next_node_id": "nb"},
	}}, nil)
	if _, err := e.executeABTestNode(nc, otherNode); err != nil {
		t.Fatalf("other node execute: %v", err)
	}
}

func TestABTestNodeWeightFallback(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	nc := testNodeContext(t, state, "alice@example.com")

	// All weight on a: every roll lands in a.
	allA := testNode("ab", NodeTypeABTest, map[string]any{"variants": []any{
		map[string]any{"id": "a", "name": "A", "weight": 100, "next_node_id": "na"},
		map[string]any{"id": "b", "name": "B", "weight": 0, "next_node_id": "nb"},
	}}, nil)
	res, err := e.executeABTestNode(nc, allA)
	if err != nil {
		t.Fatalf("executeABTestNode: %v", err)
	}
	if res.Branch != "a" {
		t.Errorf("branch = %q, want a (100%% weight)", res.Branch)
	}

	// Malformed weights (sum 0, under 100): the last variant catches the rest
	// instead of panicking or returning an empty branch.
	underSum := testNode("ab", NodeTypeABTest, map[string]any{"variants": []any{
		map[string]any{"id": "a", "name": "A", "weight": 0, "next_node_id": "na"},
		map[string]any{"id": "b", "name": "B", "weight": 0, "next_node_id": "nb"},
	}}, nil)
	res, err = e.executeABTestNode(nc, underSum)
	if err != nil {
		t.Fatalf("executeABTestNode with malformed weights: %v", err)
	}
	if res.Branch != "b" {
		t.Errorf("branch = %q, want b (the fallback catches the remainder)", res.Branch)
	}
}

func TestABTestNodeWithoutVariantsErrors(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	nc := testNodeContext(t, state, "alice@example.com")
	node := testNode("ab", NodeTypeABTest, map[string]any{"variants": []any{}}, nil)
	if _, err := e.executeABTestNode(nc, node); err == nil {
		t.Error("an ab_test without variants must fail the node")
	}
}

// ---------- list_status node ----------

func TestListStatusNodeBranches(t *testing.T) {
	node := testNode("ls", NodeTypeListStatus, map[string]any{
		"list_id":             uint(4),
		"active_node_id":      "act",
		"non_active_node_id":  "non",
		"not_in_list_node_id": "out",
	}, nil)

	// Member: active.
	e, state, _, _, _ := newTestEngine(t)
	engSeed(t, state, "subscriber_list_members", map[string]any{"list_id": uint(4), "subscriber_id": uint(9)})
	nc := testNodeContext(t, state, "alice@example.com")
	res, err := e.executeListStatusNode(nc, node)
	if err != nil {
		t.Fatalf("executeListStatusNode: %v", err)
	}
	if res.Branch != BranchActive {
		t.Errorf("member branch = %q, want active", res.Branch)
	}

	// List-scoped opt-out: non_active.
	e, state, _, _, _ = newTestEngine(t)
	engSeed(t, state, "subscriber_list_unsubscribes", map[string]any{"list_id": uint(4), "subscriber_id": uint(9)})
	nc = testNodeContext(t, state, "alice@example.com")
	res, err = e.executeListStatusNode(nc, node)
	if err != nil {
		t.Fatalf("executeListStatusNode: %v", err)
	}
	if res.Branch != BranchNonActive {
		t.Errorf("suppressed branch = %q, want non_active", res.Branch)
	}

	// Neither: not_in_list.
	e, state, _, _, _ = newTestEngine(t)
	nc = testNodeContext(t, state, "alice@example.com")
	res, err = e.executeListStatusNode(nc, node)
	if err != nil {
		t.Fatalf("executeListStatusNode: %v", err)
	}
	if res.Branch != BranchNotInList {
		t.Errorf("outsider branch = %q, want not_in_list", res.Branch)
	}
}

func TestListStatusNodeRequiresListID(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	nc := testNodeContext(t, state, "alice@example.com")
	node := testNode("ls", NodeTypeListStatus, map[string]any{}, nil)
	if _, err := e.executeListStatusNode(nc, node); err == nil || !strings.Contains(err.Error(), "list_id") {
		t.Errorf("a missing list_id must fail the node, got %v", err)
	}
}

// ---------- delay node ----------

func TestDelayNodeParksForConfiguredDuration(t *testing.T) {
	e, state, _, _, clock := newTestEngine(t)
	nc := testNodeContext(t, state, "alice@example.com")

	node := testNode("d", NodeTypeDelay, map[string]any{
		"duration": 2, "unit": models.AutomationDelayDays, "description": "Cool off",
	}, nil)
	res, err := e.executeDelayNode(nc, node)
	if err != nil {
		t.Fatalf("executeDelayNode: %v", err)
	}
	want := clock.Now().Add(48 * time.Hour)
	if res.ScheduledAt == nil || !res.ScheduledAt.Equal(want) {
		t.Errorf("scheduled_at = %v, want %v", res.ScheduledAt, want)
	}
	if res.Output["scheduled_at"] == "" || res.Output["description"] != "Cool off" {
		t.Errorf("delay output = %v", res.Output)
	}

	// Bad units and non-positive durations fail the node.
	for _, cfg := range []map[string]any{
		{"duration": 0, "unit": "hours"},
		{"duration": -5, "unit": "hours"},
		{"duration": 5, "unit": "fortnights"},
	} {
		bad := testNode("d", NodeTypeDelay, cfg, nil)
		if _, err := e.executeDelayNode(nc, bad); err == nil {
			t.Errorf("delay config %v must fail", cfg)
		}
	}
}

// ---------- list add/remove nodes ----------

func TestAddAndRemoveListNodes(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	nc := testNodeContext(t, state, "alice@example.com")

	add := testNode("add", NodeTypeAddToList, map[string]any{"list_id": uint(4)}, nil)
	res, err := e.executeAddToListNode(nc, add)
	if err != nil {
		t.Fatalf("executeAddToListNode: %v", err)
	}
	if res.Output["list_id"] != uint(4) {
		t.Errorf("output list_id = %v, want 4", res.Output["list_id"])
	}
	if got := state.rowCount("subscriber_list_members"); got != 1 {
		t.Fatalf("member rows = %d, want 1", got)
	}

	// Re-adding is a no-op (ON CONFLICT DO NOTHING), not an error.
	if _, err := e.executeAddToListNode(nc, add); err != nil {
		t.Fatalf("re-add must be idempotent, got %v", err)
	}
	if got := state.rowCount("subscriber_list_members"); got != 1 {
		t.Fatalf("member rows after re-add = %d, want 1", got)
	}

	remove := testNode("rem", NodeTypeRemoveFromList, map[string]any{"list_id": uint(4)}, nil)
	res, err = e.executeRemoveFromListNode(nc, remove)
	if err != nil {
		t.Fatalf("executeRemoveFromListNode: %v", err)
	}
	if got := state.rowCount("subscriber_list_members"); got != 0 {
		t.Fatalf("member rows after remove = %d, want 0", got)
	}

	// Removing a non-member is a no-op, not an error.
	if _, err := e.executeRemoveFromListNode(nc, remove); err != nil {
		t.Fatalf("removing a non-member must be a no-op, got %v", err)
	}

	// A missing list_id fails.
	bare := testNode("add", NodeTypeAddToList, map[string]any{}, nil)
	if _, err := e.executeAddToListNode(nc, bare); err == nil {
		t.Error("add_to_list without a list_id must fail")
	}
}

// ---------- webhook node ----------

// webhookTestEngine returns an engine whose webhook client dials the given
// httptest server directly. The webhook URL uses a public documentation IP
// (TEST-NET-1) so the SSRF guard passes — loopback, where httptest servers
// actually listen, is exactly what the guard exists to reject — while the
// transport routes the bytes to the local test server.
func webhookTestEngine(t *testing.T, srv *httptest.Server) (*Engine, *engState) {
	t.Helper()
	e, state, _, _, _ := newTestEngine(t)
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	client := NewWebhookClient()
	client.SetHTTPClient(&http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, "127.0.0.1:"+port)
		},
	}})
	e.SetWebhookClient(client)
	return e, state
}

func TestWebhookNodeRecordsSuccess(t *testing.T) {
	var gotBody, gotAuth, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotAuth = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	e, state := webhookTestEngine(t, srv)
	nc := testNodeContext(t, state, "alice@example.com")

	node := testNode("wh", NodeTypeWebhook, map[string]any{
		"url":     "http://192.0.2.1/hook",
		"headers": map[string]any{"X-Custom": "1"},
		"secret":  "s3cr3t",
		"body":    `{"email":"{{contact_email}}","order":"{{order_id}}"}`,
	}, nil)

	res, err := e.executeWebhookNode(context.Background(), nc, node)
	if err != nil {
		t.Fatalf("executeWebhookNode: %v", err)
	}
	if res.Output["status_code"] != http.StatusOK {
		t.Errorf("output status_code = %v, want 200", res.Output["status_code"])
	}
	if res.Output["body"] != `{"ok":true}` {
		t.Errorf("output body = %v", res.Output["body"])
	}
	if !strings.Contains(gotBody, "alice@example.com") || !strings.Contains(gotBody, "A-1") {
		t.Errorf("webhook body %q must render the contact and event properties", gotBody)
	}
	if gotAuth != "Bearer s3cr3t" {
		t.Errorf("Authorization = %q, want the node secret bearer token", gotAuth)
	}
	if gotHeader != "1" {
		t.Errorf("X-Custom = %q, want the configured header", gotHeader)
	}
}

func TestWebhookNodeErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	e, state := webhookTestEngine(t, srv)
	nc := testNodeContext(t, state, "alice@example.com")

	node := testNode("wh", NodeTypeWebhook, map[string]any{"url": "http://192.0.2.1/hook"}, nil)
	_, err := e.executeWebhookNode(context.Background(), nc, node)
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("a 500 must fail the node with the status in the error, got %v", err)
	}
}

func TestWebhookNodeRejectsSSRFBeforeAnyRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"loopback", "http://127.0.0.1/hook"},
		{"private range", "http://192.168.1.1/hook"},
		{"link-local", "http://169.254.169.254/latest/meta-data/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, state, _, _, _ := newTestEngine(t)
			nc := testNodeContext(t, state, "alice@example.com")

			// A server the author would love to hit; if any request ever
			// arrives the test fails, proving the guard fires pre-flight.
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			// For the loopback case aim at the live test server's own port:
			// the SSRF guard must refuse it before a single byte is sent.
			target := tc.url
			if tc.name == "loopback" {
				target = "http://127.0.0.1" + strings.TrimPrefix(srv.URL, "http://127.0.0.1")
			}

			node := testNode("wh", NodeTypeWebhook, map[string]any{"url": target}, nil)
			_, err := e.executeWebhookNode(context.Background(), nc, node)
			if err == nil {
				t.Fatalf("webhook to %q must be rejected", target)
			}
			if !strings.Contains(err.Error(), "not a public address") {
				t.Errorf("error = %v, want the SSRF rejection", err)
			}
			if got := hits.Load(); got != 0 {
				t.Fatalf("the SSRF guard must fire before any request, got %d hits", got)
			}
		})
	}
}

func TestWebhookNodeRequiresURL(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	nc := testNodeContext(t, state, "alice@example.com")
	node := testNode("wh", NodeTypeWebhook, map[string]any{}, nil)
	if _, err := e.executeWebhookNode(context.Background(), nc, node); err == nil || !strings.Contains(err.Error(), "url is missing") {
		t.Errorf("a missing url must fail the node, got %v", err)
	}
}

// ---------- WebhookClient SSRF guard (direct) ----------

func TestWebhookClientRejectsPrivateHostsDirectly(t *testing.T) {
	w := NewWebhookClient()
	for _, url := range []string{
		"http://127.0.0.1/x",
		"http://10.0.0.5/x",
		"http://192.168.1.1/x",
		"http://172.16.0.9/x",
		"http://[::1]/x",
		"http://0.0.0.0/x",
		"ftp://example.com/x",
	} {
		if _, _, err := w.Post(context.Background(), url, nil, []byte("{}"), ""); err == nil {
			t.Errorf("Post(%q) must be rejected", url)
		}
	}
}
