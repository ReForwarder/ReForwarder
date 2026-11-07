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

package spamfilter

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// ---------- stubs ----------

type stubReputation struct {
	rep Reputation
	err error
}

func (s stubReputation) SenderReputation(_ context.Context, _ uint, _ string) (Reputation, error) {
	return s.rep, s.err
}

type stubResolver struct {
	ips  map[string][]net.IP // zone-qualified name → answer
	fail map[string]bool
}

func (s stubResolver) LookupIP(_ context.Context, _, host string) ([]net.IP, error) {
	if s.fail[host] {
		return nil, fmt.Errorf("dns timeout for %s", host)
	}
	return s.ips[host], nil
}

// ---------- auth verdict signal ----------

func TestScoreAuthVerdicts(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)

	cases := []struct {
		name    string
		auth    AuthVerdict
		wantMin float64
		reason  string
	}{
		{"spf fail", AuthVerdict{SPF: "fail"}, 2.5, "spf_fail"},
		{"spf softfail", AuthVerdict{SPF: "softfail"}, 1.0, "spf_softfail"},
		{"spf temperror", AuthVerdict{SPF: "temperror"}, 0.5, "spf_temperror"},
		{"dkim fail", AuthVerdict{DKIM: "fail"}, 1.0, "dkim_fail"},
		{"dmarc fail", AuthVerdict{DMARC: "fail"}, 2.5, "dmarc_fail"},
		{"dmarc fail + reject policy", AuthVerdict{DMARC: "fail", DMARCDisposition: "reject"}, 3.5, "dmarc_policy_enforced"},
		{"all pass", AuthVerdict{SPF: "pass", DKIM: "pass", DMARC: "pass"}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score, reasons := s.Score(&Message{Auth: tc.auth})
			if score < tc.wantMin {
				t.Fatalf("score = %v, want >= %v", score, tc.wantMin)
			}
			if tc.reason != "" && !containsReason(reasons, tc.reason) {
				t.Fatalf("reasons %v missing %q", reasons, tc.reason)
			}
		})
	}
}

func TestScoreCleanMessageIsZero(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)
	score, reasons := s.Score(&Message{
		Auth:     AuthVerdict{SPF: "pass", DKIM: "pass", DMARC: "pass"},
		Headers:  map[string]string{"Message-Id": "<a@b>", "Date": time.Now().UTC().Format(time.RFC1123Z)},
		TextBody: "hello",
	})
	if score != 0 {
		t.Fatalf("score = %v, want 0", score)
	}
	if len(reasons) != 0 {
		t.Fatalf("reasons = %v, want none", reasons)
	}
}

// ---------- header heuristics ----------

// cleanHeaders is a complete, unremarkable header set so a test message only
// exercises the signal it names.
func cleanHeaders() map[string]string {
	return map[string]string{
		"Message-Id": "<x@y>",
		"Date":       time.Now().UTC().Format(time.RFC1123Z),
	}
}

func TestScoreMissingMessageID(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)
	score, reasons := s.Score(&Message{Headers: map[string]string{"Date": time.Now().UTC().Format(time.RFC1123Z)}})
	if score != DefaultWeights.MissingMessageID {
		t.Fatalf("score = %v, want %v", score, DefaultWeights.MissingMessageID)
	}
	if !containsReason(reasons, "missing_message_id") {
		t.Fatalf("reasons %v missing missing_message_id", reasons)
	}
}

func TestScoreDateSkew(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)

	future := time.Now().UTC().Add(-96 * time.Hour).Format(time.RFC1123Z) // 4 days stale
	score, reasons := s.Score(&Message{Headers: cleanHeadersWith("Date", future)})
	if score != DefaultWeights.DateSkew || !containsReasonPrefix(reasons, "date_skew:") {
		t.Fatalf("stale date: score=%v reasons=%v", score, reasons)
	}

	// Missing Date header: missing_date_header, not skew.
	score, reasons = s.Score(&Message{Headers: map[string]string{"Message-Id": "<x@y>"}})
	if score != DefaultWeights.MissingDate || !containsReason(reasons, "missing_date_header") {
		t.Fatalf("missing date: score=%v reasons=%v", score, reasons)
	}

	// Unparseable Date.
	score, reasons = s.Score(&Message{Headers: cleanHeadersWith("Date", "not a date")})
	if score != DefaultWeights.MissingDate || !containsReason(reasons, "unparseable_date_header") {
		t.Fatalf("bad date: score=%v reasons=%v", score, reasons)
	}

	// Fresh message: no date penalty.
	score, _ = s.Score(&Message{Headers: cleanHeaders()})
	if score != 0 {
		t.Fatalf("fresh date scored %v, want 0", score)
	}
}

// cleanHeadersWith returns cleanHeaders with one header overridden.
func cleanHeadersWith(k, v string) map[string]string {
	h := cleanHeaders()
	h[k] = v
	return h
}

// ---------- URL count ----------

func TestScoreURLCounts(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)

	body := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "visit https://example.com/%d now\n", i)
		}
		return b.String()
	}

	score, _ := s.Score(&Message{TextBody: body(3), Headers: cleanHeaders()})
	if score != 0 {
		t.Fatalf("3 URLs scored %v, want 0", score)
	}

	score, reasons := s.Score(&Message{TextBody: body(15), Headers: cleanHeaders()})
	if score != DefaultWeights.URLSpeak || !containsReason(reasons, "url_count:15") {
		t.Fatalf("15 URLs: score=%v reasons=%v", score, reasons)
	}

	score, _ = s.Score(&Message{TextBody: body(30), Headers: cleanHeaders()})
	if score != DefaultWeights.URLHeavy {
		t.Fatalf("30 URLs scored %v, want %v", score, DefaultWeights.URLHeavy)
	}
}

// ---------- GTUBE (SF-42's testable slice) ----------

func TestScoreGTUBE(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)
	score, reasons := s.Score(&Message{
		TextBody: "Hello\n\n" + GTUBEString + "\n",
		Headers:  cleanHeaders(),
	})
	if score != DefaultWeights.GTUBE {
		t.Fatalf("GTUBE score = %v, want %v", score, DefaultWeights.GTUBE)
	}
	if !containsReason(reasons, gtubeReason) {
		t.Fatalf("reasons %v missing %q", reasons, gtubeReason)
	}

	// HTML body also matches.
	score, _ = s.Score(&Message{HTMLBody: "<p>" + GTUBEString + "</p>", Headers: cleanHeaders()})
	if score != DefaultWeights.GTUBE {
		t.Fatalf("GTUBE in HTML scored %v, want %v", score, DefaultWeights.GTUBE)
	}

	// A near-miss (different marker, quoted spec text without its own line)
	// must NOT fire.
	score, _ = s.Score(&Message{TextBody: "the GTUBE marker is " + strings.ToLower(GTUBEString), Headers: cleanHeaders()})
	if score != 0 {
		t.Fatalf("lowercase GTUBE scored %v, want 0", score)
	}

	// GTUBE alone crosses the reject threshold.
	if d := s.Disposition(DefaultWeights.GTUBE); d != DispositionReject {
		t.Fatalf("GTUBE disposition = %v, want reject", d)
	}
}

// ---------- disposition thresholds ----------

func TestDispositionThresholdMatrix(t *testing.T) {
	s := NewRulesScorer(StaticThresholds{Quarantine: 5, Reject: 10}, nil, nil)
	cases := []struct {
		score float64
		want  Disposition
	}{
		{0, DispositionDeliver},
		{4.99, DispositionDeliver},
		{5, DispositionQuarantine},
		{7.5, DispositionQuarantine},
		{9.99, DispositionQuarantine},
		{10, DispositionReject},
		{1000, DispositionReject},
	}
	for _, tc := range cases {
		if got := s.Disposition(tc.score); got != tc.want {
			t.Fatalf("Disposition(%v) = %v, want %v", tc.score, got, tc.want)
		}
	}
}

func TestDispositionMisconfiguredClamp(t *testing.T) {
	// reject below quarantine is an admin mistake; the scorer clamps so a
	// score at/above quarantine can never deliver.
	s := NewRulesScorer(StaticThresholds{Quarantine: 5, Reject: 1}, nil, nil)
	if got := s.Disposition(5); got != DispositionReject {
		t.Fatalf("Disposition(5) = %v, want reject (clamped)", got)
	}
	if got := s.Disposition(4.9); got != DispositionDeliver {
		t.Fatalf("Disposition(4.9) = %v, want deliver", got)
	}
}

func TestDispositionNilScorerDefaults(t *testing.T) {
	// A scorer without a threshold source falls back to the defaults.
	s := NewRulesScorer(nil, nil, nil)
	if got := s.Disposition(4); got != DispositionDeliver {
		t.Fatalf("Disposition(4) = %v, want deliver", got)
	}
	if got := s.Disposition(6); got != DispositionQuarantine {
		t.Fatalf("Disposition(6) = %v, want quarantine", got)
	}
	if got := s.Disposition(11); got != DispositionReject {
		t.Fatalf("Disposition(11) = %v, want reject", got)
	}
}

// ---------- DNSBL ----------

func TestDNSBLDisabledWithoutZones(t *testing.T) {
	if NewDNSBLChecker("", nil).Enabled() {
		t.Fatal("empty zone list must be disabled")
	}
	c := NewDNSBLChecker("zen.spamhaus.org", stubResolver{
		ips: map[string][]net.IP{"1.2.0.192.zen.spamhaus.org": {net.IPv4(127, 0, 0, 4)}},
	})
	if !c.Enabled() {
		t.Fatal("allowlisted zone must enable the checker")
	}
}

func TestDNSBLNonAllowlistedZoneDropped(t *testing.T) {
	c := NewDNSBLChecker("evil.example.org, zen.spamhaus.org", stubResolver{})
	// Only the allowlisted zone survives; the configured one is dropped.
	if len(c.zones) != 1 || c.zones[0] != "zen.spamhaus.org" {
		t.Fatalf("zones = %v, want [zen.spamhaus.org]", c.zones)
	}
}

func TestDNSBLHitAndMiss(t *testing.T) {
	res := stubResolver{
		ips: map[string][]net.IP{
			"1.2.0.192.zen.spamhaus.org": {net.IPv4(127, 0, 0, 4)}, // listed
			// 3.4.0.192... absent → NXDOMAIN (miss)
		},
		fail: map[string]bool{
			"5.6.0.192.bl.spamcop.net": true, // resolver failure → miss (fail-open)
		},
	}
	c := NewDNSBLChecker("zen.spamhaus.org,bl.spamcop.net", res)

	hits := c.Check(context.Background(), "192.0.2.1")
	if len(hits) != 1 || hits[0] != "zen.spamhaus.org" {
		t.Fatalf("hits = %v, want [zen.spamhaus.org]", hits)
	}
	if hits := c.Check(context.Background(), "192.0.2.3"); len(hits) != 0 {
		t.Fatalf("unlisted IP hits = %v, want none", hits)
	}
	if hits := c.Check(context.Background(), "192.0.2.5"); len(hits) != 0 {
		t.Fatalf("resolver-failure hits = %v, want none (fail-open)", hits)
	}
}

func TestDNSBLPrivateIPLookupSkipped(t *testing.T) {
	c := NewDNSBLChecker("zen.spamhaus.org", stubResolver{
		ips: map[string][]net.IP{"1.0.0.127.zen.spamhaus.org": {net.IPv4(127, 0, 0, 2)}},
	})
	if hits := c.Check(context.Background(), "127.0.0.1"); len(hits) != 0 {
		t.Fatalf("loopback hits = %v, want none", hits)
	}
	if hits := c.Check(context.Background(), "10.1.2.3"); len(hits) != 0 {
		t.Fatalf("private hits = %v, want none", hits)
	}
	if hits := c.Check(context.Background(), "not-an-ip"); len(hits) != 0 {
		t.Fatalf("garbage hits = %v, want none", hits)
	}
	if hits := c.Check(context.Background(), ""); len(hits) != 0 {
		t.Fatalf("empty IP hits = %v, want none", hits)
	}
}

func TestScoreDNSBLCapped(t *testing.T) {
	res := stubResolver{ips: map[string][]net.IP{
		"1.2.0.192.zen.spamhaus.org":       {net.IPv4(127, 0, 0, 4)},
		"1.2.0.192.bl.spamcop.net":         {net.IPv4(127, 0, 0, 2)},
		"1.2.0.192.b.barracudacentral.org": {net.IPv4(127, 0, 0, 2)},
	}}
	c := NewDNSBLChecker("zen.spamhaus.org,bl.spamcop.net,b.barracudacentral.org", res)
	s := NewRulesScorer(DefaultThresholds, c, nil)
	score, reasons := s.Score(&Message{ClientIP: "192.0.2.1", Headers: cleanHeaders()})
	if score != DefaultWeights.DNSBLMax {
		t.Fatalf("3 hits scored %v, want capped %v", score, DefaultWeights.DNSBLMax)
	}
	if len(reasons) != 3 {
		t.Fatalf("reasons = %v, want one per zone", reasons)
	}
}

// ---------- sender reputation ----------

func TestScoreReputationRates(t *testing.T) {
	cases := []struct {
		name   string
		rep    Reputation
		score  float64
		reason string
	}{
		{"high bounce", Reputation{Sent: 100, Bounced: 30}, DefaultWeights.HighBounceRate, "sender_bounce_rate:high"},
		{"moderate bounce", Reputation{Sent: 100, Bounced: 15}, DefaultWeights.ModerateBounceRate, "sender_bounce_rate:moderate"},
		{"high complaint", Reputation{Sent: 1000, Complained: 30}, DefaultWeights.HighComplaintRate, "sender_complaint_rate:high"},
		{"moderate complaint", Reputation{Sent: 1000, Complained: 8}, DefaultWeights.ModerateComplaintRate, "sender_complaint_rate:moderate"},
		{"small sample abstains", Reputation{Sent: 10, Bounced: 10}, 0, ""},
		{"healthy domain", Reputation{Sent: 500, Bounced: 5, Complained: 1}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewRulesScorer(DefaultThresholds, nil, stubReputation{rep: tc.rep})
			score, reasons := s.Score(&Message{UserID: 7, SenderDomain: "sender.example", Headers: cleanHeaders()})
			if score != tc.score {
				t.Fatalf("score = %v, want %v", score, tc.score)
			}
			if tc.reason != "" && !containsReason(reasons, tc.reason) {
				t.Fatalf("reasons %v missing %q", reasons, tc.reason)
			}
		})
	}
}

func TestScoreReputationSourceErrorAbstains(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, stubReputation{err: fmt.Errorf("db down")})
	score, _ := s.Score(&Message{UserID: 7, SenderDomain: "sender.example", Headers: cleanHeaders()})
	if score != 0 {
		t.Fatalf("source outage scored %v, want 0", score)
	}
}

// ---------- scorer robustness ----------

func TestScoreNilMessageAndNilScorer(t *testing.T) {
	var s *RulesScorer
	if score, reasons := s.Score(nil); score != 0 || reasons != nil {
		t.Fatalf("nil scorer scored %v %v, want 0 nil", score, reasons)
	}
	if score, _ := NewRulesScorer(DefaultThresholds, nil, nil).Score(nil); score != 0 {
		t.Fatalf("nil message scored %v, want 0", score)
	}
}

func TestReasonAccumulatorDedup(t *testing.T) {
	acc := &ReasonAccumulator{}
	acc.Add("x")
	acc.Add("x")
	acc.Add("")
	if got := acc.List(); len(got) != 1 || got[0] != "x" {
		t.Fatalf("List() = %v, want [x]", got)
	}
	empty := &ReasonAccumulator{}
	if empty.List() != nil {
		t.Fatal("empty accumulator should return nil")
	}
}

// ---------- helpers ----------

func containsReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

// containsReasonPrefix matches parameterized reasons like "date_skew:+96h0m0s".
func containsReasonPrefix(reasons []string, prefix string) bool {
	for _, r := range reasons {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}
