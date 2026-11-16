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

// Package filterengine is the bounded per-mailbox filter-script engine
// (SF-14, the ADR-006 amendment): ordered filter scripts evaluated at
// mailbox delivery — after the spam disposition, before final folder
// placement — supporting exactly four operations (fileinto, redirect,
// discard, keep) with conditions from the unified evaluator's envelope-fact
// grammar. NOT a full RFC 5228 interpreter: the bounded grammar covers the
// documented common operations only, is strictly validated at save time,
// and is strictly fail-closed at evaluation (an unknown field/operator is an
// error, mirroring the transport currency — a malformed script must never
// silently pass mail through a gate).
package filterengine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/conditions"
)

// Action types (the four bounded operations).
const (
	ActionFileInto = "fileinto"
	ActionRedirect = "redirect"
	ActionDiscard  = "discard"
	ActionKeep     = "keep"
)

// Fact names the condition grammar accepts (the message-fact whitelist the
// unified evaluator's envelope-fact mode already defines, minus the envelope
// facts that do not exist at mailbox delivery: helo/authenticated_as are
// SMTP-session facts; message_size is known and useful).
const (
	FactSubject    = "subject"
	FactFrom       = "from"
	FactTextBody   = "text_body"
	FactHTMLBody   = "html_body"
	FactRecipients = "recipients"
	FactMessageSize = "message_size"
)

var validFields = map[string]bool{
	FactSubject:     true,
	FactFrom:        true,
	FactTextBody:    true,
	FactHTMLBody:    true,
	FactRecipients:  true,
	FactMessageSize: true,
}

var validOperators = map[string]bool{
	"equals": true, "not_equals": true, "contains": true,
	"starts_with": true, "ends_with": true,
	"greater_than": true, "less_than": true,
}

var validActionTypes = map[string]bool{
	ActionFileInto: true, ActionRedirect: true,
	ActionDiscard: true, ActionKeep: true,
}

// decodeActions unmarshals the stored actions JSON.
func decodeActions(raw string) ([]ScriptAction, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("filterengine: empty action list")
	}
	var actions []ScriptAction
	if err := json.Unmarshal([]byte(raw), &actions); err != nil {
		return nil, fmt.Errorf("filterengine: decode actions: %w", err)
	}
	return actions, nil
}

// ScriptAction is one operation of a script's action list.
type ScriptAction struct {
	Type   string `json:"type"`   // fileinto | redirect | discard | keep
	Target string `json:"target"` // fileinto: the folder IMAP path; redirect: the address
}

// Decision is what the engine produces for one mailbox: the folder to file
// into (empty = INBOX default), whether to skip the mailbox append, and the
// redirect targets to hand to the forwarding pipeline.
type Decision struct {
	// Folder is the IMAP path to file into; empty = INBOX (keep).
	Folder string
	// SkipAppend is true when a discard action matched — the mailbox append
	// is skipped (the durable inbound archive remains).
	SkipAppend bool
	// Redirects lists the external addresses to forward the message to
	// (bounded; loop-checked by the caller against the forwarded-for chain).
	Redirects []string
	// Applied names the executed action tags (observability).
	Applied []string
}

// ScriptFacts is the evaluation subject for one mailbox: the message facts
// the conditions may reference.
type ScriptFacts struct {
	Subject     string
	From        string
	TextBody    string
	HTMLBody    string
	Recipients  string
	MessageSize int64
}

func (f ScriptFacts) toScriptFacts() conditions.ScriptFacts {
	return conditions.ScriptFacts{
		Subject:     strings.TrimSpace(f.Subject),
		From:        strings.ToLower(strings.TrimSpace(f.From)),
		TextBody:    f.TextBody,
		HTMLBody:    f.HTMLBody,
		Recipients:  strings.ToLower(strings.TrimSpace(f.Recipients)),
		MessageSize: f.MessageSize,
	}
}

// ValidateScript validates a script's condition set and action list. The
// conditions run through the unified evaluator's strict grammar (an unknown
// field/operator or a non-parsable numeric is an error — the fail-closed
// posture); the actions must be one of the four bounded operations with a
// target where required.
func ValidateScript(ruleSet []models.FilterRule, actions []ScriptAction) error {
	// The strict evaluator's save-time probe: an empty-subject pass over the
	// grammar rejects unknown fields/operators without needing real content.
	facts := conditions.ScriptFacts{}
	if _, err := conditions.EvaluateScriptRules(ruleSet, facts); err != nil {
		return err
	}
	if len(actions) == 0 {
		return fmt.Errorf("filterengine: a script requires at least one action")
	}
	for i, a := range actions {
		if !validActionTypes[a.Type] {
			return fmt.Errorf("filterengine: action %d has unknown type %q", i, a.Type)
		}
		switch a.Type {
		case ActionFileInto, ActionRedirect:
			target := strings.TrimSpace(a.Target)
			if target == "" || len(target) > 255 {
				return fmt.Errorf("filterengine: action %d (%s) requires a target of 1..255 characters", i, a.Type)
			}
			if a.Type == ActionRedirect && (strings.Contains(target, "@") == false ||
				strings.Count(target, "@") != 1) {
				return fmt.Errorf("filterengine: action %d (redirect) requires an address target", i)
			}
		}
	}
	return nil
}

// Evaluate runs the scripts in priority order (the caller supplies them
// ordered) against the facts and returns the FIRST matching script's
// decision: its actions execute in order — fileinto sets the folder (the
// last one wins), redirect collects targets, discard stops the mailbox
// append, keep is explicit. No matching script (or none at all) returns the
// empty decision = the default INBOX filing, unchanged. Evaluation is
// strictly fail-closed: a condition error skips the remaining scripts and
// returns the error for the caller's fail-open handling at the delivery
// point (the default filing proceeds — an engine outage must not hold mail).
func Evaluate(scripts []models.FilterScript, facts ScriptFacts) (*Decision, error) {
	var decision Decision
	for i := range scripts {
		s := &scripts[i]
		if !s.Enabled {
			continue
		}
		got, err := conditions.EvaluateScriptRules(s.Conditions, facts.toScriptFacts())
		if err != nil {
			return nil, fmt.Errorf("filter script %q (id %d): %w", s.Name, s.ID, err)
		}
		if !got {
			continue
		}
		actions, aerr := decodeActions(s.ActionsJSON)
		if aerr != nil {
			return nil, fmt.Errorf("filter script %q (id %d): %w", s.Name, s.ID, aerr)
		}
		for _, a := range actions {
			switch a.Type {
			case ActionFileInto:
				decision.Folder = strings.TrimSpace(a.Target)
				decision.Applied = append(decision.Applied, "fileinto")
			case ActionRedirect:
				if t := strings.TrimSpace(a.Target); t != "" {
					decision.Redirects = append(decision.Redirects, t)
				}
				decision.Applied = append(decision.Applied, "redirect")
			case ActionDiscard:
				decision.SkipAppend = true
				decision.Applied = append(decision.Applied, "discard")
			case ActionKeep:
				decision.Folder = "" // explicit INBOX
				decision.Applied = append(decision.Applied, "keep")
			}
		}
		break // first match wins: later scripts never stack
	}
	return &decision, nil
}
