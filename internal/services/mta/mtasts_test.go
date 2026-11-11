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
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mta

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------- _smtp._tls TXT record parsing ----------

func TestSTSRecordIDParse(t *testing.T) {
	cases := []struct {
		name string
		txt  string
		id   string
		ok   bool
	}{
		{"valid", "v=STS1 id=20240101", "20240101", true},
		{"valid extra tags", "id=abc123; v=STS1; foo=bar", "abc123", true},
		{"case-insensitive keys", "V=STS1 ID=ze9", "ze9", true},
		{"wrong version", "v=STS2 id=x", "", false},
		{"no version", "id=x", "", false},
		{"no id", "v=STS1", "", false},
		{"garbage", "hello world", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := STSRecordID(tc.txt)
			if ok != tc.ok || id != tc.id {
				t.Fatalf("STSRecordID(%q) = (%q, %v), want (%q, %v)", tc.txt, id, ok, tc.id, tc.ok)
			}
		})
	}
}

// ---------- policy document parsing (RFC 8461 §4.2 fixtures) ----------

const enforcePolicy = "version: STS1\r\nmode: enforce\r\nmx: mx1.example.com\r\nmx: .example.com\r\nmax_age: 604800\r\n"

func TestParseSTSPolicyValidEnforce(t *testing.T) {
	p, err := ParseSTSPolicy([]byte(enforcePolicy), "20240101")
	if err != nil {
		t.Fatalf("parse enforce policy: %v", err)
	}
	if p.Mode != STSModeEnforce {
		t.Fatalf("mode = %q, want enforce", p.Mode)
	}
	if len(p.MX) != 2 || p.MX[0] != "mx1.example.com" || p.MX[1] != ".example.com" {
		t.Fatalf("mx = %v, want [mx1.example.com .example.com]", p.MX)
	}
	if p.MaxAgeSeconds != 604800 {
		t.Fatalf("max_age = %d, want 604800", p.MaxAgeSeconds)
	}
	if p.ID != "20240101" {
		t.Fatalf("id = %q, want 20240101", p.ID)
	}
	if !p.Enforced() {
		t.Fatalf("enforce policy must report Enforced()")
	}
}

func TestParseSTSPolicyValidNoneNoMX(t *testing.T) {
	// mode: none policies may omit mx (RFC 8461 §4.2).
	p, err := ParseSTSPolicy([]byte("version: STS1\nmode: none\nmax_age: 86400\n"), "id1")
	if err != nil {
		t.Fatalf("parse none policy: %v", err)
	}
	if p.Mode != STSModeNone || len(p.MX) != 0 {
		t.Fatalf("mode/mx = %q/%v, want none/[]", p.Mode, p.MX)
	}
	if p.Enforced() {
		t.Fatalf("none policy must not report Enforced()")
	}
}

func TestParseSTSPolicyRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":              "",
		"no version":         "mode: enforce\nmx: a.example\nmax_age: 60\n",
		"wrong version":      "version: STS2\nmode: enforce\nmx: a.example\nmax_age: 60\n",
		"duplicate version":  "version: STS1\nversion: STS1\nmode: none\nmax_age: 60\n",
		"no mode":            "version: STS1\nmax_age: 60\n",
		"unknown mode":       "version: STS1\nmode: strict\nmx: a.example\nmax_age: 60\n",
		"duplicate mode":     "version: STS1\nmode: none\nmode: none\nmax_age: 60\n",
		"no max_age":         "version: STS1\nmode: none\n",
		"bad max_age":        "version: STS1\nmode: none\nmax_age: soon\n",
		"negative max_age":   "version: STS1\nmode: none\nmax_age: -5\n",
		"duplicate max_age":  "version: STS1\nmode: none\nmax_age: 60\nmax_age: 60\n",
		"enforce without mx": "version: STS1\nmode: enforce\nmax_age: 60\n",
		"testing without mx": "version: STS1\nmode: testing\nmax_age: 60\n",
		"empty mx":           "version: STS1\nmode: enforce\nmx:\nmax_age: 60\n",
		"unknown field":      "version: STS1\nmode: none\nmax_age: 60\nextra: x\n",
		"malformed line":     "version STS1\nmode: none\nmax_age: 60\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSTSPolicy([]byte(doc), "id"); err == nil {
				t.Fatalf("ParseSTSPolicy(%q) accepted a malformed policy", name)
			}
		})
	}
}

func TestParseSTSPolicyRejectsOversize(t *testing.T) {
	doc := "version: STS1\r\nmode: none\r\nmax_age: 60\r\n" + strings.Repeat("x", stsMaxPolicyBytes)
	if _, err := ParseSTSPolicy([]byte(doc), "id"); err == nil {
		t.Fatalf("oversize policy accepted")
	}
}

// ---------- hosted policy rendering (the HTTPS document shape) ----------

func TestRenderSTSPolicyShape(t *testing.T) {
	got := RenderSTSPolicy("enforce", []string{"mx1.example.com", "mx2.example.com"}, 86400)
	want := "version: STS1\r\nmode: enforce\r\nmx: mx1.example.com\r\nmx: mx2.example.com\r\nmax_age: 86400\r\n"
	if got != want {
		t.Fatalf("RenderSTSPolicy =\n%q\nwant\n%q", got, want)
	}
	// The renderer's output must always parse back (hosting shape round-trip).
	if _, err := ParseSTSPolicy([]byte(got), "rendered"); err != nil {
		t.Fatalf("rendered policy does not parse: %v", err)
	}
}

func TestRenderSTSPolicyRefusesInvalid(t *testing.T) {
	if got := RenderSTSPolicy("", []string{"mx.example"}, 86400); got != "" {
		t.Fatalf("unknown mode must render nothing, got %q", got)
	}
	if got := RenderSTSPolicy("enforce", nil, 86400); got != "" {
		t.Fatalf("mx-less enforce policy must render nothing, got %q", got)
	}
	// Our hosting posture never renders an mx-less document: the handler 404s
	// when no MX resolves (a policy without mx lines is useless even for
	// mode: none), so the renderer refuses too.
	if got := RenderSTSPolicy("none", nil, 0); got != "" {
		t.Fatalf("mx-less policy must render nothing, got %q", got)
	}
}

// ---------- mx pattern matching (RFC 8461 §4.1) ----------

func TestMXAllowed(t *testing.T) {
	cases := []struct {
		patterns []string
		host     string
		want     bool
	}{
		{[]string{"mx1.example.com"}, "mx1.example.com", true},
		{[]string{"mx1.example.com"}, "MX1.EXAMPLE.COM.", true}, // case + trailing dot
		{[]string{"mx1.example.com"}, "mx2.example.com", false},
		{[]string{".example.com"}, "example.com", true},        // suffix form: apex included
		{[]string{".example.com"}, "mx1.example.com", true},    // suffix form: any subdomain
		{[]string{".example.com"}, "example.org", false},       // suffix form: unrelated domain
		{[]string{"*.example.com"}, "mx1.example.com", true},   // wildcard: at least one label
		{[]string{"*.example.com"}, "a.b.example.com", true},   // wildcard: multiple labels
		{[]string{"*.example.com"}, "example.com", false},      // wildcard needs ≥1 label
		{[]string{"*.example.com"}, "example.com.evil", false}, // no partial match
		{[]string{"*.example.com"}, "mx1.example.org", false},
		{nil, "mx1.example.com", false},
	}
	for i, tc := range cases {
		if got := MXAllowed(tc.patterns, tc.host); got != tc.want {
			t.Fatalf("case %d: MXAllowed(%v, %q) = %v, want %v", i, tc.patterns, tc.host, got, tc.want)
		}
	}
}

// ---------- policy client cache behavior ----------

type stsFakeClient struct {
	txtCalls  int
	fetches   int
	txt       []string
	txtErr    error
	body      []byte
	fetchErr  error
	statusCod int
}

func (f *stsFakeClient) lookupTXT(context.Context, string) ([]string, error) {
	f.txtCalls++
	if f.txtErr != nil {
		return nil, f.txtErr
	}
	return f.txt, nil
}

func (f *stsFakeClient) fetch(_ context.Context, _ string) ([]byte, error) {
	f.fetches++
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return f.body, nil
}

func newSTSClient(t *testing.T, fake *stsFakeClient, now func() time.Time) *PolicyClient {
	t.Helper()
	c := NewPolicyClient()
	c.now = now
	c.SetLookups(fake.lookupTXT, fake.fetch)
	return c
}

var stsNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func TestPolicyClientCachesPositive(t *testing.T) {
	fake := &stsFakeClient{
		txt:  []string{"v=STS1 id=abc"},
		body: []byte(enforcePolicy),
	}
	// Tiny TTL so expiry is reachable in-test; the point is the cache hit
	// window, not the absolute duration.
	c := newSTSClient(t, fake, func() time.Time { return stsNow })
	c.SetTTL(time.Minute)

	p, ok := c.Policy(context.Background(), "example.com")
	if !ok || !p.Enforced() {
		t.Fatalf("first Policy = (%v, ok=%v), want an enforce policy", p, ok)
	}
	if fake.txtCalls != 1 || fake.fetches != 1 {
		t.Fatalf("first call: txt=%d fetch=%d, want 1/1", fake.txtCalls, fake.fetches)
	}

	// Inside the TTL window: served from cache, no DNS/HTTP traffic.
	if _, ok := c.Policy(context.Background(), "example.com"); !ok {
		t.Fatalf("second Policy lost the cached policy")
	}
	if fake.txtCalls != 1 || fake.fetches != 1 {
		t.Fatalf("cached call hit the wire: txt=%d fetch=%d", fake.txtCalls, fake.fetches)
	}

	// After the TTL expires: fresh discovery.
	c.now = func() time.Time { return stsNow.Add(2 * time.Minute) }
	if _, ok := c.Policy(context.Background(), "example.com"); !ok {
		t.Fatalf("post-TTL Policy lost the policy")
	}
	if fake.txtCalls != 2 || fake.fetches != 2 {
		t.Fatalf("post-TTL call did not refresh: txt=%d fetch=%d", fake.txtCalls, fake.fetches)
	}
}

func TestPolicyClientCachesNegative(t *testing.T) {
	fake := &stsFakeClient{txtErr: errors.New("no such host")}
	c := newSTSClient(t, fake, func() time.Time { return stsNow })

	if p, ok := c.Policy(context.Background(), "example.com"); ok || p != nil {
		t.Fatalf("TXT-less domain must report no policy, got (%v, %v)", p, ok)
	}
	if _, ok := c.Policy(context.Background(), "example.com"); ok {
		t.Fatalf("negative cache miss")
	}
	if fake.txtCalls != 1 {
		t.Fatalf("negative verdict not cached: txt=%d calls", fake.txtCalls)
	}
	// Explicit invalidation forces re-discovery.
	c.Invalidate("example.com")
	_, _ = c.Policy(context.Background(), "example.com")
	if fake.txtCalls != 2 {
		t.Fatalf("Invalidate did not force re-discovery: txt=%d", fake.txtCalls)
	}
}

func TestPolicyClientFallsOpenOnFetchFailure(t *testing.T) {
	// RFC 8461 §6.1: a policy that cannot be fetched is "no policy" —
	// enforcement never blocks on a broken discovery path.
	fake := &stsFakeClient{
		txt:      []string{"v=STS1 id=abc"},
		fetchErr: errors.New("connection refused"),
	}
	c := newSTSClient(t, fake, func() time.Time { return stsNow })
	p, ok := c.Policy(context.Background(), "example.com")
	if ok || p != nil {
		t.Fatalf("fetch failure must fall open, got (%v, %v)", p, ok)
	}
}

func TestPolicyClientIgnoresForeignTXT(t *testing.T) {
	// A TXT record at _smtp._tls that is not an STS1 record is not a policy.
	fake := &stsFakeClient{txt: []string{"v=spf1 -all"}}
	c := newSTSClient(t, fake, func() time.Time { return stsNow })
	if _, ok := c.Policy(context.Background(), "example.com"); ok {
		t.Fatalf("non-STS TXT must not resolve to a policy")
	}
	if fake.fetches != 0 {
		t.Fatalf("fetch attempted without a valid STS1 id: %d", fake.fetches)
	}
}

// ---------- enforcement hook + process default ----------

func TestEnforcerSessionRule(t *testing.T) {
	fake := &stsFakeClient{txt: []string{"v=STS1 id=abc"}, body: []byte(enforcePolicy)}
	c := newSTSClient(t, fake, func() time.Time { return stsNow })
	e := NewEnforcer(c)

	rule := e.SessionRule(context.Background(), "example.com")
	if rule == nil || !rule.Enforced() {
		t.Fatalf("SessionRule = %v, want an enforce rule", rule)
	}
	if !rule.HostAllowed("mx1.example.com") || rule.HostAllowed("evil.example.net") {
		t.Fatalf("rule host matching wrong: allowed=%v evil=%v",
			rule.HostAllowed("mx1.example.com"), rule.HostAllowed("evil.example.net"))
	}

	// A domain without a policy resolves to a nil rule (no enforcement).
	SetDefaultSTS(nil)
	if r := NewEnforcer(NewPolicyClient()).SessionRule(context.Background(), "nopolicy.example"); r != nil {
		t.Fatalf("policy-less domain must yield a nil rule, got %v", r)
	}
}

func TestSetDefaultSTSWiring(t *testing.T) {
	t.Cleanup(func() { SetDefaultSTS(nil) })
	if DefaultSTS() != nil {
		t.Fatalf("default STS must be nil until wired")
	}
	e := NewEnforcer(NewPolicyClient())
	SetDefaultSTS(e)
	if DefaultSTS() != e {
		t.Fatalf("SetDefaultSTS did not arm the process hook")
	}
}

// ---------- fetch hardening (HTTPS policy endpoint) ----------

func TestFetchSTSPolicyRejectsNonText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: rewriteTransport{target: srv.Listener.Addr().String()}}
	if _, err := fetchSTSPolicy(context.Background(), client, "example.com"); err == nil {
		t.Fatalf("non-text policy document must be refused")
	}
}

func TestFetchSTSPolicyHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(enforcePolicy))
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: rewriteTransport{target: srv.Listener.Addr().String()}}
	body, err := fetchSTSPolicy(context.Background(), client, "example.com")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(string(body), "mode: enforce") {
		t.Fatalf("fetched body wrong: %q", body)
	}
}

// rewriteTransport sends every request to the test server regardless of the
// requested mta-sts host (the transport hook replaces DNS resolution; TLS
// verification behavior itself runs unchanged in production).
type rewriteTransport struct{ target string }

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = r.target
	return http.DefaultTransport.RoundTrip(clone)
}

func TestMXMatchFromSessionRuleNilSafety(t *testing.T) {
	var nilRule *STSRule
	if !nilRule.HostAllowed("anything.example") {
		t.Fatalf("a nil rule must allow every host (no policy applies)")
	}
	if nilRule.Enforced() {
		t.Fatalf("a nil rule is never enforced")
	}
}
