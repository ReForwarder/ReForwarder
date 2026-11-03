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
	"encoding/json"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// ---------- builders ----------

func strPtr(s string) *string { return &s }

func uintPtr(v uint) *uint { return &v }

func testNode(id, typ string, cfg map[string]any, next *string) *models.AutomationNode {
	n := &models.AutomationNode{ID: id, Type: typ, Config: cfg, NextNodeID: next}
	return n
}

// triggerNode builds a list.subscribed trigger scoped to list 1.
func triggerNode(id string, next *string) *models.AutomationNode {
	return testNode(id, NodeTypeTrigger, map[string]any{
		"event_kind": EventKindListSubscribed,
		"list_id":    1,
		"frequency":  FrequencyOnce,
	}, next)
}

// minimalWorkflow is a valid one-email workflow: trigger -> email -> done.
func minimalWorkflow() *WorkflowDoc {
	return &WorkflowDoc{
		RootNodeID: "t",
		Nodes: []*models.AutomationNode{
			triggerNode("t", strPtr("e1")),
			testNode("e1", NodeTypeEmail, map[string]any{"template_id": 7}, nil),
		},
	}
}

func containsError(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

func expectError(t *testing.T, errs []string, substr string) {
	t.Helper()
	if !containsError(errs, substr) {
		t.Fatalf("expected a validation error containing %q, got %v", substr, errs)
	}
}

// ---------- ParseWorkflow round-trip ----------

func TestParseWorkflowRoundTrip(t *testing.T) {
	nodes := []*models.AutomationNode{
		triggerNode("t", strPtr("e1")),
		testNode("e1", NodeTypeEmail, map[string]any{"template_id": 7, "subject_override": "Hi"}, nil),
	}
	raw, err := json.Marshal(nodes)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc, err := ParseWorkflow("t", string(raw))
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	if doc.RootNodeID != "t" {
		t.Errorf("RootNodeID = %q, want t", doc.RootNodeID)
	}
	if len(doc.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(doc.Nodes))
	}
	email := doc.Node("e1")
	if email == nil {
		t.Fatal("email node not found by id")
	}
	cfg, err := doc.EmailConfig(email)
	if err != nil {
		t.Fatalf("EmailConfig: %v", err)
	}
	if cfg.TemplateID != 7 || cfg.SubjectOverride != "Hi" {
		t.Errorf("config round-trip lost data: %+v", cfg)
	}
	trig := doc.Trigger()
	if trig == nil || trig.ID != "t" {
		t.Fatalf("Trigger() = %+v, want the t trigger node", trig)
	}

	// Malformed JSON is the only parse failure.
	if _, err := ParseWorkflow("t", "{not json"); err == nil {
		t.Error("malformed JSON must fail parsing")
	}
}

// ---------- Validate: the happy path ----------

func TestValidateMinimalWorkflowPasses(t *testing.T) {
	if errs := minimalWorkflow().Validate(uintPtr(3)); len(errs) != 0 {
		t.Fatalf("minimal workflow must validate clean, got %v", errs)
	}
	// The empty slice contract: never nil.
	if errs := minimalWorkflow().Validate(uintPtr(3)); errs == nil {
		t.Fatal("Validate must return an empty slice, not nil")
	}
	// A list.subscribed trigger without a config list_id is fine when the
	// automation carries the list.
	doc := &WorkflowDoc{RootNodeID: "t", Nodes: []*models.AutomationNode{
		testNode("t", NodeTypeTrigger, map[string]any{"event_kind": EventKindListSubscribed, "frequency": FrequencyOnce}, nil),
	}}
	if errs := doc.Validate(uintPtr(3)); len(errs) != 0 {
		t.Fatalf("automation-level list must satisfy the trigger, got %v", errs)
	}
}

// ---------- Validate: the rule matrix ----------

func TestValidateMissingTrigger(t *testing.T) {
	doc := &WorkflowDoc{RootNodeID: "e1", Nodes: []*models.AutomationNode{
		testNode("e1", NodeTypeEmail, map[string]any{"template_id": 7}, nil),
	}}
	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "no trigger node")
}

func TestValidateTriggerNotRoot(t *testing.T) {
	doc := &WorkflowDoc{RootNodeID: "e1", Nodes: []*models.AutomationNode{
		triggerNode("t", strPtr("e1")),
		testNode("e1", NodeTypeEmail, map[string]any{"template_id": 7}, nil),
	}}
	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "the root must be the trigger node")
}

func TestValidateMissingRootNode(t *testing.T) {
	doc := minimalWorkflow()
	doc.RootNodeID = "ghost"
	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "does not reference an existing node")
}

func TestValidateInvalidEventKind(t *testing.T) {
	doc := &WorkflowDoc{RootNodeID: "t", Nodes: []*models.AutomationNode{
		testNode("t", NodeTypeTrigger, map[string]any{"event_kind": "banana", "frequency": FrequencyOnce}, nil),
	}}
	errs := doc.Validate(nil)
	expectError(t, errs, "unknown event_kind")
}

func TestValidateListSubscribedWithoutListID(t *testing.T) {
	doc := &WorkflowDoc{RootNodeID: "t", Nodes: []*models.AutomationNode{
		testNode("t", NodeTypeTrigger, map[string]any{"event_kind": EventKindListSubscribed, "frequency": FrequencyOnce}, nil),
	}}
	errs := doc.Validate(nil)
	expectError(t, errs, "requires a list_id")
}

func TestValidateCustomWithoutEventName(t *testing.T) {
	doc := &WorkflowDoc{RootNodeID: "t", Nodes: []*models.AutomationNode{
		testNode("t", NodeTypeTrigger, map[string]any{"event_kind": EventKindCustom, "frequency": FrequencyOnce}, nil),
	}}
	errs := doc.Validate(nil)
	expectError(t, errs, "requires a custom_event_name")

	// With the name set it validates clean.
	doc.Nodes[0].Config["custom_event_name"] = "order.placed"
	if errs := doc.Validate(nil); len(errs) != 0 {
		t.Fatalf("named custom trigger must validate clean, got %v", errs)
	}
}

func TestValidateUnknownFrequency(t *testing.T) {
	doc := minimalWorkflow()
	doc.Nodes[0].Config["frequency"] = "sometimes"
	expectError(t, doc.Validate(uintPtr(3)), "unknown frequency")
}

func TestValidateEmailNodeRequirements(t *testing.T) {
	// No automation list at all.
	doc := minimalWorkflow()
	errs := doc.Validate(nil)
	expectError(t, errs, "the automation must have a list")

	// List present but template missing.
	doc = minimalWorkflow()
	delete(doc.Nodes[1].Config, "template_id")
	errs = doc.Validate(uintPtr(3))
	expectError(t, errs, "template_id is required")
}

func TestValidateDelayRules(t *testing.T) {
	doc := minimalWorkflow()
	delay := testNode("d", NodeTypeDelay, map[string]any{"duration": 0, "unit": "hours"}, nil)
	doc.Nodes[1].NextNodeID = strPtr("d")
	doc.Nodes = append(doc.Nodes, delay)

	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "duration must be a number greater than 0")

	delay.Config["duration"] = 5
	delay.Config["unit"] = "fortnights"
	errs = doc.Validate(uintPtr(3))
	expectError(t, errs, "unknown unit")

	delay.Config["unit"] = models.AutomationDelayDays
	if errs := doc.Validate(uintPtr(3)); len(errs) != 0 {
		t.Fatalf("5 days delay must validate clean, got %v", errs)
	}
}

func TestValidateABTestRules(t *testing.T) {
	build := func(variants []any) *WorkflowDoc {
		doc := minimalWorkflow()
		ab := testNode("ab", NodeTypeABTest, map[string]any{"variants": variants}, nil)
		doc.Nodes[1].NextNodeID = strPtr("ab")
		doc.Nodes = append(doc.Nodes,
			ab,
			testNode("va", NodeTypeEmail, map[string]any{"template_id": 1}, nil),
			testNode("vb", NodeTypeEmail, map[string]any{"template_id": 2}, nil),
		)
		return doc
	}

	// One variant only.
	doc := build([]any{map[string]any{"id": "a", "weight": 100, "next_node_id": "va"}})
	expectError(t, doc.Validate(uintPtr(3)), "at least 2 variants")

	// Weights sum to 97.
	doc = build([]any{
		map[string]any{"id": "a", "weight": 50, "next_node_id": "va"},
		map[string]any{"id": "b", "weight": 47, "next_node_id": "vb"},
	})
	expectError(t, doc.Validate(uintPtr(3)), "sum to exactly 100")

	// Variants disconnected (no next_node_id anywhere).
	doc = build([]any{
		map[string]any{"id": "a", "weight": 50, "next_node_id": ""},
		map[string]any{"id": "b", "weight": 50, "next_node_id": ""},
	})
	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "no next_node_id")

	// Valid A/B passes.
	doc = build([]any{
		map[string]any{"id": "a", "weight": 50, "next_node_id": "va"},
		map[string]any{"id": "b", "weight": 50, "next_node_id": "vb"},
	})
	if errs := doc.Validate(uintPtr(3)); len(errs) != 0 {
		t.Fatalf("valid ab_test must validate clean, got %v", errs)
	}
}

func TestValidateFilterNeedsCondition(t *testing.T) {
	doc := minimalWorkflow()
	filter := testNode("f", NodeTypeFilter, map[string]any{
		"conditions":       []any{},
		"continue_node_id": "e1",
		"exit_node_id":     "",
	}, nil)
	doc.Nodes[0].NextNodeID = strPtr("f")
	doc.Nodes = append(doc.Nodes, filter)

	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "at least one condition")

	filter.Config["conditions"] = []any{map[string]any{"field": "email", "operator": "equals", "value": "a@b.c"}}
	if errs := doc.Validate(uintPtr(3)); len(errs) != 0 {
		t.Fatalf("filter with a condition must validate clean, got %v", errs)
	}

	// A malformed rule is surfaced too.
	filter.Config["conditions"] = []any{map[string]any{"field": "banana", "operator": "equals", "value": "x"}}
	expectError(t, doc.Validate(uintPtr(3)), "unknown field")
}

func TestValidateListStatusNeedsTargets(t *testing.T) {
	doc := minimalWorkflow()
	ls := testNode("ls", NodeTypeListStatus, map[string]any{"list_id": 2}, nil)
	doc.Nodes[0].NextNodeID = strPtr("ls")
	doc.Nodes = append(doc.Nodes, ls)

	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "at least one branch target")

	ls.Config["active_node_id"] = "e1"
	if errs := doc.Validate(uintPtr(3)); len(errs) != 0 {
		t.Fatalf("list_status with a target must validate clean, got %v", errs)
	}

	// And a list_status without a list id fails.
	delete(ls.Config, "list_id")
	expectError(t, doc.Validate(uintPtr(3)), "list_id is required")
}

func TestValidateWebhookScheme(t *testing.T) {
	doc := minimalWorkflow()
	wh := testNode("wh", NodeTypeWebhook, map[string]any{"url": "ftp://example.com/hook"}, nil)
	doc.Nodes[1].NextNodeID = strPtr("wh")
	doc.Nodes = append(doc.Nodes, wh)

	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "must be a valid http or https URL")

	wh.Config["url"] = "https://example.com/hook"
	if errs := doc.Validate(uintPtr(3)); len(errs) != 0 {
		t.Fatalf("https webhook must validate clean, got %v", errs)
	}

	wh.Config["url"] = "not a url"
	expectError(t, doc.Validate(uintPtr(3)), "must be a valid http or https URL")
}

func TestValidateDanglingNextNodeID(t *testing.T) {
	doc := minimalWorkflow()
	doc.Nodes[0].NextNodeID = strPtr("ghost")
	expectError(t, doc.Validate(uintPtr(3)), `references missing node "ghost"`)
}

func TestValidateTargetToTrigger(t *testing.T) {
	doc := minimalWorkflow()
	doc.Nodes[1].NextNodeID = strPtr("t")
	expectError(t, doc.Validate(uintPtr(3)), "cannot target the trigger node")
}

func TestValidateOrphanNode(t *testing.T) {
	doc := minimalWorkflow()
	doc.Nodes = append(doc.Nodes,
		testNode("lonely", NodeTypeDelay, map[string]any{"duration": 1, "unit": "hours"}, nil))
	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "not reachable from the trigger")
}

func TestValidateOnPathCycleRejected(t *testing.T) {
	doc := minimalWorkflow()
	a := testNode("a", NodeTypeEmail, map[string]any{"template_id": 1}, strPtr("b"))
	b := testNode("b", NodeTypeEmail, map[string]any{"template_id": 2}, strPtr("a"))
	doc.Nodes[1].NextNodeID = strPtr("a")
	doc.Nodes = append(doc.Nodes, a, b)

	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "infinite loop")
}

func TestValidateDiamondNotRejected(t *testing.T) {
	// ab_test fans out to two arms that converge on the same join node —
	// legitimate branching, not a cycle.
	doc := minimalWorkflow()
	ab := testNode("ab", NodeTypeABTest, map[string]any{"variants": []any{
		map[string]any{"id": "a", "weight": 50, "next_node_id": "j"},
		map[string]any{"id": "b", "weight": 50, "next_node_id": "j"},
	}}, nil)
	join := testNode("j", NodeTypeAddToList, map[string]any{"list_id": 2}, nil)
	doc.Nodes[1].NextNodeID = strPtr("ab")
	doc.Nodes = append(doc.Nodes, ab, join)

	if errs := doc.Validate(uintPtr(3)); len(errs) != 0 {
		t.Fatalf("converging branches must validate clean, got %v", errs)
	}
}

func TestValidateEmptyWorkflow(t *testing.T) {
	doc := &WorkflowDoc{RootNodeID: "", Nodes: nil}
	errs := doc.Validate(nil)
	expectError(t, errs, "workflow has no nodes")
}

func TestValidateEmptyNodeID(t *testing.T) {
	doc := minimalWorkflow()
	doc.Nodes = append(doc.Nodes, testNode("", NodeTypeDelay, map[string]any{"duration": 1, "unit": "hours"}, nil))
	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "missing an id")
}

func TestValidateUnknownNodeType(t *testing.T) {
	doc := minimalWorkflow()
	doc.Nodes = append(doc.Nodes, testNode("x", "banana", nil, nil))
	errs := doc.Validate(uintPtr(3))
	expectError(t, errs, "unknown node type")
}

// ---------- NextNodeFrom ----------

func TestNextNodeFromLinear(t *testing.T) {
	doc := minimalWorkflow()
	if got := doc.NextNodeFrom(doc.Nodes[0], ""); got != "e1" {
		t.Errorf("trigger next = %q, want e1", got)
	}
	// Branch argument is ignored for linear nodes.
	if got := doc.NextNodeFrom(doc.Nodes[0], "anything"); got != "e1" {
		t.Errorf("linear node must ignore the branch, got %q", got)
	}
	// No successor: empty means the journey completes.
	if got := doc.NextNodeFrom(doc.Nodes[1], ""); got != "" {
		t.Errorf("terminal node next = %q, want empty", got)
	}
	// A nil node is safe.
	if got := doc.NextNodeFrom(nil, ""); got != "" {
		t.Errorf("nil node next = %q, want empty", got)
	}
}

func TestNextNodeFromFilterBranches(t *testing.T) {
	doc := minimalWorkflow()
	filter := testNode("f", NodeTypeFilter, map[string]any{
		"conditions":       []any{},
		"continue_node_id": "e1",
		"exit_node_id":     "x",
	}, nil)
	doc.Nodes = append(doc.Nodes, filter)

	if got := doc.NextNodeFrom(filter, BranchContinue); got != "e1" {
		t.Errorf("continue branch = %q, want e1", got)
	}
	if got := doc.NextNodeFrom(filter, BranchExit); got != "x" {
		t.Errorf("exit branch = %q, want x", got)
	}
	if got := doc.NextNodeFrom(filter, "sideways"); got != "" {
		t.Errorf("unknown branch = %q, want empty", got)
	}
}

func TestNextNodeFromABTestVariantID(t *testing.T) {
	doc := minimalWorkflow()
	ab := testNode("ab", NodeTypeABTest, map[string]any{"variants": []any{
		map[string]any{"id": "a", "weight": 50, "next_node_id": "na"},
		map[string]any{"id": "b", "weight": 50, "next_node_id": "nb"},
	}}, nil)
	doc.Nodes = append(doc.Nodes, ab)

	if got := doc.NextNodeFrom(ab, "a"); got != "na" {
		t.Errorf("variant a next = %q, want na", got)
	}
	if got := doc.NextNodeFrom(ab, "b"); got != "nb" {
		t.Errorf("variant b next = %q, want nb", got)
	}
	if got := doc.NextNodeFrom(ab, "nope"); got != "" {
		t.Errorf("unknown variant = %q, want empty", got)
	}
}

func TestNextNodeFromListStatusBranches(t *testing.T) {
	doc := minimalWorkflow()
	ls := testNode("ls", NodeTypeListStatus, map[string]any{
		"list_id":             2,
		"active_node_id":      "act",
		"non_active_node_id":  "non",
		"not_in_list_node_id": "out",
	}, nil)
	doc.Nodes = append(doc.Nodes, ls)

	cases := map[string]string{
		BranchActive:    "act",
		BranchNonActive: "non",
		BranchNotInList: "out",
		"mystery":       "",
	}
	for branch, want := range cases {
		if got := doc.NextNodeFrom(ls, branch); got != want {
			t.Errorf("branch %q = %q, want %q", branch, got, want)
		}
	}
}
