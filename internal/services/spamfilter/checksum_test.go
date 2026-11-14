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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// SF-42 tests: the distributed-checksum client (the ADR-006 amendment) —
// the lookup against a fake server, the gating, and the fail-open posture.

func TestBodyDigestStableAcrossWhitespaceAndCase(t *testing.T) {
	a := BodyDigest("Free  VIAGRA   offer\n\tnow!")
	b := BodyDigest("free viagra offer now!")
	if a != b {
		t.Fatalf("digests must normalize whitespace + case: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("digest = %q, want a 64-hex SHA-256", a)
	}
	if a == BodyDigest("different body") {
		t.Fatal("different bodies must digest differently")
	}
}

func TestCheckClientGatingAndReplies(t *testing.T) {
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Digest string `json:"digest"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		received = req.Digest
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count": 42, "wl_count": 1}`))
	}))
	t.Cleanup(srv.Close)

	c := NewCheckClient(srv.URL, time.Second)
	if !c.Enabled() {
		t.Fatal("a configured client must be enabled")
	}
	reply, spam, err := c.Check(context.Background(), "free viagra offer now!")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !spam {
		t.Fatal("count 42 > wl 1 must count as spam")
	}
	if reply.Count != 42 || reply.WLCount != 1 {
		t.Fatalf("reply = %+v", reply)
	}
	if received != BodyDigest("free viagra offer now!") {
		t.Fatalf("the lookup must carry the normalized digest, got %q", received)
	}

	// Whitelisted digests do not count as spam.
	wlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"count": 1, "wl_count": 9}`))
	}))
	t.Cleanup(wlSrv.Close)
	wl := NewCheckClient(wlSrv.URL, time.Second)
	if _, spam, _ := wl.Check(context.Background(), "hello"); spam {
		t.Fatal("wl_count >= count must not count as spam")
	}
}

func TestCheckClientFailOpen(t *testing.T) {
	// Server error (500), connection refused (a closed server), and a
	// malformed reply all ERROR — the caller abstains (fail-open).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	for name, c := range map[string]*CheckClient{
		"500":          NewCheckClient(srv.URL, time.Second),
		"refused":      NewCheckClient("http://127.0.0.1:1/nope", time.Second),
		"bad json":     NewCheckClient(mustSrv(t, "not json"), time.Second),
		"nil client":   nil,
		"empty url":    NewCheckClient("  ", time.Second),
	} {
		reply, spam, err := c.Check(context.Background(), "body")
		if err == nil && spam {
			t.Fatalf("%s: must not report spam through failure: %+v", name, reply)
		}
		if name != "bad json" && name != "500" && name != "refused" && err != nil {
			t.Fatalf("%s: an unconfigured client must not error, got %v", name, err)
		}
	}
}

func mustSrv(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestScoreChecksumIntegration — the scorer integration: a spam-counting
// reply adds the checksum weight + reason; an erroring endpoint abstains
// (fail-open: the score reflects only the other signals); a disabled client
// contributes nothing.
func TestScoreChecksumIntegration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"count": 30, "wl_count": 2}`))
	}))
	t.Cleanup(srv.Close)

	s := NewRulesScorer(DefaultThresholds, nil, nil)
	s.SetChecksumChecker(NewCheckClient(srv.URL, time.Second))
	msg := &Message{TextBody: strings.Repeat("free stuff ", 3)}
	score, reasons := s.Score(msg)
	wantReason(t, reasons, "checksum_spam:30/2", score, DefaultWeights.ChecksumSpam)

	// An erroring endpoint: fail-open — the checksum abstains.
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(errSrv.Close)
	s2 := NewRulesScorer(DefaultThresholds, nil, nil)
	s2.SetChecksumChecker(NewCheckClient(errSrv.URL, time.Second))
	score, reasons = s2.Score(msg)
	noTag(t, reasons, "checksum_spam:")
	_ = score

	// Disabled: no contribution.
	s3 := NewRulesScorer(DefaultThresholds, nil, nil)
	score, reasons = s3.Score(msg)
	noTag(t, reasons, "checksum_spam:")
	_ = score
}
