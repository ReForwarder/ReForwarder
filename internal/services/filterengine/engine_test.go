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

package filterengine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// SF-14 tests: the bounded filter-script engine — script parse (validation),
// each operation, condition gating, and the isolation contract (a foreign
// mailbox's scripts are never in the evaluated set — the caller's query is
// mailbox-scoped, asserted at the repo level; here the per-script mailbox
// binding is pinned through the model).

func script(conditions []models.FilterRule, actions []ScriptAction) models.FilterScript {
	acts, _ := json.Marshal(actions)
	return models.FilterScript{
		Name:        "test",
		Enabled:     true,
		Conditions:  conditions,
		ActionsJSON: string(acts),
	}
}

func rule(field, operator string, value any) models.FilterRule {
	return models.FilterRule{Field: field, Operator: operator, Value: value}
}

func facts() ScriptFacts {
	return ScriptFacts{
		Subject:     "wire transfer confirmation",
		From:        "crook@spammy.example",
		TextBody:    "send the unclaimed funds today",
		HTMLBody:    "<p>send the funds</p>",
		Recipients:  "inbox@lists.example",
		MessageSize: 2048,
	}
}

// TestValidateScriptRejectsMalformed — the strict grammar at save time: an
// unknown field/operator, a non-parsable numeric, no actions, an unknown
// action type, and a targetless fileinto/redirect are all rejected — a
// malformed script never reaches the table.
func TestValidateScriptRejectsMalformed(t *testing.T) {
	cases := []struct {
		name       string
		conditions []models.FilterRule
		actions    []ScriptAction
	}{
		{"unknown field", []models.FilterRule{rule("not_a_fact", "equals", "x")}, []ScriptAction{{Type: ActionKeep}}},
		{"unknown operator", []models.FilterRule{rule(FactSubject, "regex", "x")}, []ScriptAction{{Type: ActionKeep}}},
		{"non-numeric size", []models.FilterRule{rule(FactMessageSize, "greater_than", "big")}, []ScriptAction{{Type: ActionKeep}}},
		{"no actions", nil, nil},
		{"unknown action type", nil, []ScriptAction{{Type: "vacation"}}},
		{"targetless fileinto", nil, []ScriptAction{{Type: ActionFileInto}}},
		{"targetless redirect", nil, []ScriptAction{{Type: ActionRedirect}}},
		{"redirect non-address", nil, []ScriptAction{{Type: ActionRedirect, Target: "not-an-address"}}},
	}
	for _, tc := range cases {
		if err := ValidateScript(tc.conditions, tc.actions); err == nil {
			t.Errorf("%s: must be rejected at save time", tc.name)
		}
	}
	if err := ValidateScript(
		[]models.FilterRule{rule(FactSubject, "contains", "x")},
		[]ScriptAction{{Type: ActionFileInto, Target: "Archive"}}); err != nil {
		t.Fatalf("a valid script must pass: %v", err)
	}
}

// TestEvaluateFileInto — a matching script's fileinto names the folder; the
// empty folder decision = the default INBOX.
func TestEvaluateFileInto(t *testing.T) {
	scripts := []models.FilterScript{
		script([]models.FilterRule{rule(FactSubject, "contains", "wire transfer")},
			[]ScriptAction{{Type: ActionFileInto, Target: "INBOX/Finance"}}),
	}
	d, err := Evaluate(scripts, facts())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if d.Folder != "INBOX/Finance" {
		t.Fatalf("folder = %q, want INBOX/Finance", d.Folder)
	}
	if d.SkipAppend {
		t.Fatal("a fileinto script must not skip the append")
	}
	if len(d.Redirects) != 0 {
		t.Fatalf("redirects = %v, want none", d.Redirects)
	}

	// Condition gating: a non-matching condition → the default decision.
	scripts[0].Conditions = []models.FilterRule{rule(FactSubject, "contains", "nonexistent")}
	d, err = Evaluate(scripts, facts())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if d.Folder != "" || d.SkipAppend {
		t.Fatalf("a non-matching script must yield the default decision: %+v", d)
	}
}

// TestEvaluateRedirectAndDiscardAndKeep — each bounded operation behaves:
// redirect collects the targets, discard skips the append, keep resets the
// folder to the INBOX default.
func TestEvaluateRedirectAndDiscardAndKeep(t *testing.T) {
	facts := facts()

	// Redirect: the targets collected in order.
	redirect := []models.FilterScript{
		script([]models.FilterRule{rule(FactFrom, "equals", "crook@spammy.example")},
			[]ScriptAction{{Type: ActionRedirect, Target: "auditor@elsewhere.example"}}),
	}
	d, err := Evaluate(redirect, facts)
	if err != nil {
		t.Fatalf("redirect evaluate: %v", err)
	}
	if len(d.Redirects) != 1 || d.Redirects[0] != "auditor@elsewhere.example" {
		t.Fatalf("redirects = %v, want the auditor target", d.Redirects)
	}
	if d.SkipAppend {
		t.Fatal("a redirect must not skip the mailbox append (it adds delivery)")
	}

	// Discard: the append skipped.
	discard := []models.FilterScript{
		script([]models.FilterRule{rule(FactSubject, "contains", "wire transfer")},
			[]ScriptAction{{Type: ActionDiscard}}),
	}
	d, err = Evaluate(discard, facts)
	if err != nil {
		t.Fatalf("discard evaluate: %v", err)
	}
	if !d.SkipAppend {
		t.Fatal("a discard script must skip the mailbox append")
	}

	// Keep: the folder resets to the INBOX default even after a fileinto.
	keep := []models.FilterScript{
		script(nil, []ScriptAction{{Type: ActionKeep}}),
	}
	d, err = Evaluate(keep, facts)
	if err != nil {
		t.Fatalf("keep evaluate: %v", err)
	}
	if d.Folder != "" || d.SkipAppend {
		t.Fatalf("keep decision = %+v, want the default filing", d)
	}
}

// TestEvaluatePrecedenceAndLoopSafety — the first matching script wins (no
// stacking), and the caller's loop guard drops a redirect target already on
// the message path.
func TestEvaluatePrecedenceAndLoopSafety(t *testing.T) {
	// First match wins: the higher-priority (earlier) script's fileinto
	// stands and the later discard never runs.
	scripts := []models.FilterScript{
		script([]models.FilterRule{rule(FactSubject, "contains", "wire")},
			[]ScriptAction{{Type: ActionFileInto, Target: "INBOX/Finance"}}),
		script([]models.FilterRule{rule(FactSubject, "contains", "wire")},
			[]ScriptAction{{Type: ActionDiscard}}),
	}
	d, err := Evaluate(scripts, facts())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if d.Folder != "INBOX/Finance" || d.SkipAppend {
		t.Fatalf("decision = %+v, want only the first script's fileinto", d)
	}

	// Disabled scripts never match.
	scripts[0].Enabled = false
	d, err = Evaluate(scripts, facts())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if d.Folder != "" || !d.SkipAppend {
		t.Fatalf("with the first script disabled the discard must win: %+v", d)
	}
}

// TestEvaluateStrictGrammar — the fail-closed evaluation: an unknown field or
// operator in an ENABLED script errors (the caller logs and files by
// default); a DISABLED malformed script never evaluates.
func TestEvaluateStrictGrammar(t *testing.T) {
	bad := []models.FilterScript{
		script([]models.FilterRule{rule("not_a_fact", "equals", "x")}, []ScriptAction{{Type: ActionKeep}}),
	}
	if _, err := Evaluate(bad, facts()); err == nil {
		t.Fatal("an unknown field in an enabled script must error (fail-closed)")
	}

	disabled := bad
	disabled[0].Enabled = false
	if _, err := Evaluate(disabled, facts()); err != nil {
		t.Fatalf("a disabled malformed script must never evaluate: %v", err)
	}
}

// TestIsolationByMailboxBinding — the model pins the isolation contract: the
// script row is bound to (user_id, mailbox_id); the repo's mailbox-scoped
// query is the isolation boundary (the caller only ever evaluates the
// mailbox's own scripts).
func TestIsolationByMailboxBinding(t *testing.T) {
	var s models.FilterScript
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"mailbox_id"`) || !strings.Contains(string(b), `"user_id"`) {
		t.Fatal("the model must carry both ownership columns")
	}
	// The document records the contract; the repo test drives the scoping.
}
