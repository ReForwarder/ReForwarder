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

package spamfilter

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// SF-42 tests: the external rule-feed loader (the ADR-006 amendment) — the
// HMAC verification against a fake feed server, the fail-open snapshot
// retention, the gating, and the scorer integration (per-rule weights capped).

const feedSecret = "rule-feed-test-secret"

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func feedServer(t *testing.T, body, signature string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Rule-Feed-Signature", signature)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRuleFeedRefreshVerifiesAndSwaps(t *testing.T) {
	body := `{"rules": [
		{"field": "subject", "operator": "contains", "value": "wire transfer", "weight": 2.0, "tag": "wire"},
		{"field": "from", "operator": "ends_with", "value": "@spammy.example", "weight": 1.5, "tag": "spammy-from"}
	]}`
	srv := feedServer(t, body, signBody(feedSecret, []byte(body)), http.StatusOK)

	f := NewRuleFeed(srv.URL, feedSecret)
	if !f.Enabled() {
		t.Fatal("a configured feed must be enabled")
	}
	n, err := f.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if n != 2 {
		t.Fatalf("refreshed rules = %d, want 2", n)
	}
	if len(f.Rules()) != 2 {
		t.Fatalf("snapshot = %d rules, want 2", len(f.Rules()))
	}
}

func TestRuleFeedFailOpen(t *testing.T) {
	good := `{"rules": [{"field": "subject", "operator": "contains", "value": "wire transfer", "weight": 2.0, "tag": "wire"}]}`
	srv := feedServer(t, good, signBody(feedSecret, []byte(good)), http.StatusOK)

	f := NewRuleFeed(srv.URL, feedSecret)
	if _, err := f.Refresh(context.Background()); err != nil {
		t.Fatalf("seed refresh: %v", err)
	}
	if len(f.Rules()) != 1 {
		t.Fatalf("seeded snapshot = %d, want 1", len(f.Rules()))
	}

	cases := []struct {
		name string
		body string
		sig  string
		code int
	}{
		{"bad signature", good, "sha256=" + hexEncode([]byte("wrong")), http.StatusOK},
		{"missing signature", good, "", http.StatusOK},
		{"broken json", `{"rules": [`, signBody(feedSecret, []byte(`{"rules": [`)), http.StatusOK},
		{"server error", good, signBody(feedSecret, []byte(good)), http.StatusInternalServerError},
		{"rule over cap", `{"rules": [{"field": "nope", "operator": "contains", "value": "x", "weight": 1, "tag": "t"}]}`,
			signBody(feedSecret, []byte(`{"rules": [{"field": "nope", "operator": "contains", "value": "x", "weight": 1, "tag": "t"}]}`)), http.StatusOK},
	}
	for _, tc := range cases {
		srv2 := feedServer(t, tc.body, tc.sig, tc.code)
		f2 := NewRuleFeed(srv2.URL, feedSecret)
		if _, err := f2.Refresh(context.Background()); err == nil {
			t.Fatalf("%s: refresh must fail", tc.name)
		}
		// Fail-open: the snapshot from BEFORE the failure stands.
		f2.SeedRules([]FeedRule{{Field: "subject", Operator: "contains", Value: "old", Weight: 1, Tag: "old"}})
		if _, err := f2.Refresh(context.Background()); err == nil {
			t.Fatalf("%s: second refresh must fail too", tc.name)
		}
		if len(f2.Rules()) != 1 || f2.Rules()[0].Tag != "old" {
			t.Fatalf("%s: the previous snapshot must stand: %+v", tc.name, f2.Rules())
		}
	}
}

func hexEncode(b []byte) string {
	return hex.EncodeToString(b)
}

func TestRuleFeedGating(t *testing.T) {
	f := NewRuleFeed("", "secret")
	if f.Enabled() {
		t.Fatal("an empty URL must disable the feed")
	}
	if n, err := f.Refresh(context.Background()); n != 0 || err != nil {
		t.Fatalf("a disabled feed refresh = %d/%v, want 0/nil (no-op)", n, err)
	}
	if f.Rules() != nil {
		t.Fatal("a disabled feed must hold no rules")
	}
}

// TestScoreRuleFeedIntegration — the scorer integration: matching feed rules
// add their own weights with the feed_rule:<tag> reasons; the per-message
// contribution is capped; a non-matching rule adds nothing; an empty
// snapshot scores nothing.
func TestScoreRuleFeedIntegration(t *testing.T) {
	f := NewRuleFeed("http://feed.example", feedSecret)
	f.SeedRules([]FeedRule{
		{Field: "subject", Operator: "contains", Value: "wire transfer", Weight: 2.0, Tag: "wire"},
		{Field: "from", Operator: "ends_with", Value: "@spammy.example", Weight: 1.5, Tag: "spammy-from"},
		{Field: "text_body", Operator: "contains", Value: "unclaimed funds", Weight: 99, Tag: "funds"}, // over the cap
	})
	s := NewRulesScorer(DefaultThresholds, nil, nil)
	s.SetRuleFeed(f)

	msg := &Message{
		Subject:  "wire transfer confirmation",
		Sender:   "crook@spammy.example",
		TextBody: "send the unclaimed funds today",
		Headers: map[string]string{
			"Message-Id": "<x@y>",
			"Date":       time.Now().UTC().Format(time.RFC1123Z),
			"Received":   "from mx.example by posta.test",
		},
	}
	score, reasons := s.Score(msg)
	wantReason(t, reasons, "feed_rule:wire", score, 2.0)
	wantReason(t, reasons, "feed_rule:spammy-from", score, 3.5)
	wantReason(t, reasons, "feed_rule:funds", score, 5.0)
	// The per-message cap: the feed contribution alone is capped at 5, and
	// with the message's headers clean the total is exactly the cap.
	if score > DefaultWeights.FeedRuleMax+0.001 {
		t.Fatalf("score = %v, want the per-message cap %v", score, DefaultWeights.FeedRuleMax)
	}

	// A non-matching message scores nothing from the feed.
	score, reasons = s.Score(&Message{Subject: "hello", Sender: "a@b.c", TextBody: "normal", Headers: map[string]string{"Message-Id": "<x@y>", "Date": time.Now().UTC().Format(time.RFC1123Z)}})
	for _, r := range reasons {
		if strings.HasPrefix(r, "feed_rule:") {
			t.Fatalf("a non-matching message must not fire feed rules: %v", reasons)
		}
	}
	if score > 0.001 {
		t.Fatalf("score = %v, want 0", score)
	}

	// An empty snapshot scores nothing.
	f.SeedRules(nil)
	score, reasons = s.Score(msg)
	for _, r := range reasons {
		if strings.HasPrefix(r, "feed_rule:") {
			t.Fatalf("an empty snapshot must not fire feed rules: %v", reasons)
		}
	}
	if score > 0.001 {
		t.Fatalf("score = %v, want 0", score)
	}

	// Gating: a nil feed contributes nothing.
	s2 := NewRulesScorer(DefaultThresholds, nil, nil)
	s2.SetRuleFeed(nil)
	score, _ = s2.Score(msg)
	if score > 0.001 {
		t.Fatalf("a nil feed must contribute nothing: %v", score)
	}
}
