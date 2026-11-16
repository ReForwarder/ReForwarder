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

// The script currency (SF-14, the ADR-006 amendment): the per-mailbox filter
// scripts' condition grammar. The same strict fail-closed posture as the
// transport currency — an unknown field/operator or a non-parsable numeric is
// an ERROR (a malformed script is rejected at save time and never stored) —
// with the script's own fact whitelist (the message facts a mailbox-delivery
// evaluation honestly knows).

// Script fact names (the field whitelist).
const (
	ScriptFieldSubject    = "subject"
	ScriptFieldFrom       = "from"
	ScriptFieldTextBody   = "text_body"
	ScriptFieldHTMLBody   = "html_body"
	ScriptFieldRecipients = "recipients"
	ScriptFieldMessageSize = "message_size"
)

var scriptStringFields = map[string]bool{
	ScriptFieldSubject:    true,
	ScriptFieldFrom:       true,
	ScriptFieldTextBody:   true,
	ScriptFieldHTMLBody:   true,
	ScriptFieldRecipients: true,
	ScriptFieldMessageSize: true,
}

// ScriptFacts is the evaluation subject for one mailbox: the message facts
// the script conditions may reference.
type ScriptFacts struct {
	Subject     string
	From        string
	TextBody    string
	HTMLBody    string
	Recipients  string
	MessageSize int64
}

func (f ScriptFacts) toMap() map[string]string {
	return map[string]string{
		ScriptFieldSubject:     strings.TrimSpace(f.Subject),
		ScriptFieldFrom:        strings.ToLower(strings.TrimSpace(f.From)),
		ScriptFieldTextBody:    f.TextBody,
		ScriptFieldHTMLBody:    f.HTMLBody,
		ScriptFieldRecipients:  strings.ToLower(strings.TrimSpace(f.Recipients)),
		ScriptFieldMessageSize: strconv.FormatInt(f.MessageSize, 10),
	}
}

// EvaluateScriptRules AND-combines the rule set against the message facts.
// An empty set matches vacuously. Every deviation — unknown field, unknown
// operator, non-parsable numeric — is an error (fail-closed).
func EvaluateScriptRules(rules []models.FilterRule, facts ScriptFacts) (bool, error) {
	return EvaluateTransportRulesMapped(rules, facts.toMap(), scriptStringFields, "filter script")
}

// EvaluateTransportRulesMapped is the shared strict evaluation over a caller
// supplied field whitelist: the same operator semantics as the transport
// currency, with the accepted field set owned by the caller's currency. A
// field outside the whitelist is an error (fail-closed).
func EvaluateTransportRulesMapped(rules []models.FilterRule, facts map[string]string, fields map[string]bool, label string) (bool, error) {
	for _, r := range rules {
		field := strings.TrimSpace(r.Field)
		if field == "" {
			return false, fmt.Errorf("%s: empty condition field", label)
		}
		if !fields[field] {
			return false, fmt.Errorf("%s: unknown condition field %q", label, field)
		}
		fact, ok := facts[field]
		if !ok {
			return false, fmt.Errorf("%s: fact %q missing from the evaluation subject", label, field)
		}
		match, err := transportRuleMatch(field, fact, r.Operator, r.Value)
		if err != nil {
			return false, err
		}
		if !match {
			return false, nil
		}
	}
	return true, nil
}
