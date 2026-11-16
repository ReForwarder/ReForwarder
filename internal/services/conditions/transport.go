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
	"fmt"
	"strconv"
	"strings"

	"github.com/goposta/posta/internal/models"
)

// The transport-policy currency (SF-50, the fourth currency of the unified
// evaluator — ADR-001 amendment): models.FilterRule rule sets evaluated
// against ENVELOPE FACTS (mail_from, rcpt_to, helo, authenticated_as,
// sender_domain, message_size) at the MTA's gate points.
//
// Strictly fail-closed, unlike the marketing currencies: an unknown field or
// operator, or a non-parsable value for a numeric comparison, is an ERROR —
// a typo in a transport policy must never silently pass mail through a gate
// that might have rejected it. The callers (the MTA's RCPT gate and the
// delivery executor) treat any error as the conservative outcome: the RCPT
// gate answers a temporary 451, the executor defers the message.

// Transport fact names (the field whitelist) and operators.
const (
	TransportFieldMailFrom        = "mail_from"
	TransportFieldRcptTo          = "rcpt_to"
	TransportFieldHelo            = "helo"
	TransportFieldAuthenticatedAs = "authenticated_as"
	TransportFieldSenderDomain    = "sender_domain"
	TransportFieldMessageSize     = "message_size"
)

// CurrencyTransportPolicy names the dialect.
const CurrencyTransportPolicy Currency = "transport_policy"

var transportStringFields = map[string]bool{
	TransportFieldMailFrom:        true,
	TransportFieldRcptTo:          true,
	TransportFieldHelo:            true,
	TransportFieldAuthenticatedAs: true,
	TransportFieldSenderDomain:    true,
	TransportFieldMessageSize:     true, // rendered as a decimal string; numeric ops also accepted
}

// EvaluateTransportRules AND-combines the rule set against the envelope
// facts. An empty set matches vacuously. Every deviation — unknown field,
// unknown operator, non-parsable numeric comparison — is an error (the
// fail-closed posture); the caller decides the conservative outcome.
// Address-like facts are compared case-insensitively; callers normalize
// before handing facts over (the resolver owns the normalization).
func EvaluateTransportRules(rules []models.FilterRule, facts map[string]string) (bool, error) {
	return EvaluateTransportRulesMapped(rules, facts, transportStringFields, "transport policy")
}

// transportRuleMatch evaluates one rule. Unknown operators and non-parsable
// numerics are errors (fail-closed).
func transportRuleMatch(field, fact, operator string, value any) (bool, error) {
	switch operator {
	case "equals":
		return strings.EqualFold(fact, CoerceString(value)), nil
	case "not_equals":
		return !strings.EqualFold(fact, CoerceString(value)), nil
	case "contains":
		return strings.Contains(strings.ToLower(fact), strings.ToLower(CoerceString(value))), nil
	case "starts_with":
		return strings.HasPrefix(strings.ToLower(fact), strings.ToLower(CoerceString(value))), nil
	case "ends_with":
		return strings.HasSuffix(strings.ToLower(fact), strings.ToLower(CoerceString(value))), nil
	case "greater_than", "less_than":
		factN, err := strconv.ParseFloat(strings.TrimSpace(fact), 64)
		if err != nil {
			return false, fmt.Errorf("transport policy: field %q does not carry a numeric value (%q)", field, fact)
		}
		valueN, err := strconv.ParseFloat(strings.TrimSpace(CoerceString(value)), 64)
		if err != nil {
			return false, fmt.Errorf("transport policy: %q requires a numeric value, got %v", operator, value)
		}
		if operator == "greater_than" {
			return factN > valueN, nil
		}
		return factN < valueN, nil
	default:
		return false, fmt.Errorf("transport policy: unknown operator %q for field %q", operator, field)
	}
}

// transportFactEvaluator is the transport currency's adapter: the fact-map
// subject does not fit the subscriber-shaped Evaluator mode, so it speaks
// the FactEvaluator mode only.
type transportFactEvaluator struct {
	rules []models.FilterRule
	facts map[string]string
}

// NewTransportEvaluator builds the transport currency's adapter over one
// evaluation subject (the facts snapshot).
func NewTransportEvaluator(rules []models.FilterRule, facts map[string]string) FactEvaluator {
	return transportFactEvaluator{rules: rules, facts: facts}
}

func (t transportFactEvaluator) Currency() Currency { return CurrencyTransportPolicy }

func (t transportFactEvaluator) MatchesFacts(facts map[string]string) (bool, error) {
	return EvaluateTransportRules(t.rules, facts)
}

// FactEvaluator is the fact-subject evaluation mode (the transport-policy
// currency, SF-50): conditions evaluated against envelope facts, strictly
// fail-closed on any deviation.
type FactEvaluator interface {
	Currency() Currency
	MatchesFacts(facts map[string]string) (bool, error)
}
