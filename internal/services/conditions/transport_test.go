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

package conditions

import (
	"testing"

	"github.com/goposta/posta/internal/models"
)

// SF-50 tests: the transport-policy currency — envelope-fact rules evaluated
// strictly (an unknown field/operator or a non-parsable numeric is an ERROR,
// the fail-closed posture the MTA gates rely on), through the direct
// evaluator and the adapter.

func transportFacts() map[string]string {
	return map[string]string{
		TransportFieldMailFrom:        "alice@verified.example",
		TransportFieldRcptTo:          "bob@remote.example",
		TransportFieldHelo:            "client.example.org",
		TransportFieldAuthenticatedAs: "smtp_alice",
		TransportFieldSenderDomain:    "verified.example",
		TransportFieldMessageSize:     "2048",
	}
}

func transportRule(field, operator string, value any) models.FilterRule {
	return models.FilterRule{Field: field, Operator: operator, Value: value}
}

// TestTransportMatchMismatch pins the operator matrix on the envelope facts.
func TestTransportMatchMismatch(t *testing.T) {
	cases := []struct {
		name       string
		field      string
		operator   string
		value      any
		want       bool
	}{
		{"mail_from equals", TransportFieldMailFrom, "equals", "alice@verified.example", true},
		{"mail_from equals case-insensitive", TransportFieldMailFrom, "equals", "ALICE@VERIFIED.example", true},
		{"mail_from not_equals", TransportFieldMailFrom, "not_equals", "mallory@evil.example", true},
		{"mail_from contains", TransportFieldMailFrom, "contains", "@verified.", true},
		{"mail_from contains miss", TransportFieldMailFrom, "contains", "@evil.", false},
		{"rcpt_to starts_with", TransportFieldRcptTo, "starts_with", "bob@", true},
		{"rcpt_to ends_with", TransportFieldRcptTo, "ends_with", "remote.example", true},
		{"helo equals", TransportFieldHelo, "equals", "client.example.org", true},
		{"authenticated_as equals", TransportFieldAuthenticatedAs, "equals", "smtp_alice", true},
		{"sender_domain equals", TransportFieldSenderDomain, "equals", "verified.example", true},
		{"message_size equals", TransportFieldMessageSize, "equals", "2048", true},
		{"message_size greater_than", TransportFieldMessageSize, "greater_than", "1024", true},
		{"message_size greater_than miss", TransportFieldMessageSize, "greater_than", "4096", false},
		{"message_size less_than", TransportFieldMessageSize, "less_than", "4096", true},
	}
	for _, tc := range cases {
		got, err := EvaluateTransportRules([]models.FilterRule{transportRule(tc.field, tc.operator, tc.value)}, transportFacts())
		if err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestTransportEmptySetMatches pins the vacuous match: a policy with no
// conditions always applies (the operator's priority ordering is the
// disambiguator).
func TestTransportEmptySetMatches(t *testing.T) {
	got, err := EvaluateTransportRules(nil, transportFacts())
	if err != nil || !got {
		t.Fatalf("empty set = %v/%v, want true/nil", got, err)
	}
}

// TestTransportStrictErrors pins the fail-closed grammar: everything that
// would silently pass mail in the marketing currencies is an ERROR here.
func TestTransportStrictErrors(t *testing.T) {
	cases := []struct {
		name  string
		rules []models.FilterRule
	}{
		{"unknown field", []models.FilterRule{transportRule("not_a_fact", "equals", "x")}},
		{"unknown operator", []models.FilterRule{transportRule(TransportFieldMailFrom, "regex", "x")}},
		{"empty field", []models.FilterRule{transportRule("", "equals", "x")}},
		{"non-numeric greater_than fact", []models.FilterRule{transportRule(TransportFieldMailFrom, "greater_than", "1")}},
		{"non-numeric greater_than value", []models.FilterRule{transportRule(TransportFieldMessageSize, "greater_than", "big")}},
	}
	for _, tc := range cases {
		got, err := EvaluateTransportRules(tc.rules, transportFacts())
		if err == nil {
			t.Errorf("%s: must error (fail-closed), got match=%v", tc.name, got)
		}
		if got {
			t.Errorf("%s: an errored evaluation must not report a match", tc.name)
		}
	}
}

// TestTransportMissingFactErrors pins the subject completeness: a rule naming
// a fact its gate point never supplies errors (fail-closed) — a policy
// referencing message_size at the RCPT gate (which cannot honestly evaluate
// it) must surface, not silently never-match.
func TestTransportMissingFactErrors(t *testing.T) {
	rcptFacts := map[string]string{
		TransportFieldMailFrom: "alice@verified.example",
		TransportFieldRcptTo:   "bob@remote.example",
		TransportFieldHelo:     "client.example.org",
		TransportFieldAuthenticatedAs: "smtp_alice",
		// sender_domain + message_size deliberately absent (the RCPT gate's
		// resolver omits them).
	}
	_, err := EvaluateTransportRules([]models.FilterRule{transportRule(TransportFieldMessageSize, "greater_than", "1")}, rcptFacts)
	if err == nil {
		t.Fatal("a rule naming a missing fact must error (fail-closed)")
	}
}

// TestTransportANDCombination pins the AND semantics: every rule must hold;
// the first failing rule fails the set.
func TestTransportANDCombination(t *testing.T) {
	both := []models.FilterRule{
		transportRule(TransportFieldMailFrom, "equals", "alice@verified.example"),
		transportRule(TransportFieldRcptTo, "contains", "@remote."),
	}
	if got, err := EvaluateTransportRules(both, transportFacts()); err != nil || !got {
		t.Fatalf("both matching = %v/%v, want true/nil", got, err)
	}
	both[1] = transportRule(TransportFieldRcptTo, "contains", "@elsewhere.")
	if got, err := EvaluateTransportRules(both, transportFacts()); err != nil || got {
		t.Fatalf("one failing rule = %v/%v, want false/nil", got, err)
	}
}

// TestTransportAdapter pins the adapter: the currency naming, the fact-mode
// evaluation matching the direct evaluator, and the strict error surfacing.
func TestTransportAdapter(t *testing.T) {
	rules := []models.FilterRule{transportRule(TransportFieldSenderDomain, "equals", "verified.example")}
	ev := NewTransportEvaluator(rules, transportFacts())
	if ev.Currency() != CurrencyTransportPolicy {
		t.Fatalf("Currency = %q, want transport_policy", ev.Currency())
	}
	got, err := ev.MatchesFacts(transportFacts())
	if err != nil || !got {
		t.Fatalf("MatchesFacts = %v/%v, want true/nil", got, err)
	}
	strict := NewTransportEvaluator([]models.FilterRule{transportRule("nope", "equals", "x")}, transportFacts())
	if _, err := strict.MatchesFacts(transportFacts()); err == nil {
		t.Fatal("the adapter must surface the strict error")
	}
}
