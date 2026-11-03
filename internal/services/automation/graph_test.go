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
	"testing"

	"github.com/goposta/posta/internal/models"
)

// ---------- BFSOrphans ----------

func TestBFSOrphans(t *testing.T) {
	doc := minimalWorkflow()
	if orphans := doc.BFSOrphans(); len(orphans) != 0 {
		t.Fatalf("connected workflow has orphans: %v", orphans)
	}

	// A chain plus two disconnected nodes; orphans are sorted.
	doc.Nodes = append(doc.Nodes,
		testNode("zz", NodeTypeAddToList, map[string]any{"list_id": 2}, nil),
		testNode("aa", NodeTypeAddToList, map[string]any{"list_id": 3}, nil),
	)
	orphans := doc.BFSOrphans()
	if len(orphans) != 2 || orphans[0] != "aa" || orphans[1] != "zz" {
		t.Fatalf("orphans = %v, want [aa zz] (sorted)", orphans)
	}

	// Nodes referenced by branch configs count as edges. The filter's exit
	// arm keeps the original email node reachable too.
	doc = minimalWorkflow()
	doc.Nodes = append(doc.Nodes, testNode("f", NodeTypeFilter, map[string]any{
		"conditions":       []any{map[string]any{"field": "email", "operator": "equals", "value": "x@y.z"}},
		"continue_node_id": "via-branch",
		"exit_node_id":     "e1",
	}, nil))
	doc.Nodes[0].NextNodeID = strPtr("f")
	doc.Nodes = append(doc.Nodes, testNode("via-branch", NodeTypeAddToList, map[string]any{"list_id": 4}, nil))
	if orphans := doc.BFSOrphans(); len(orphans) != 0 {
		t.Fatalf("branch targets must count as edges, got orphans %v", orphans)
	}
}

// ---------- RequiredListIDs ----------

func TestRequiredListIDsDedup(t *testing.T) {
	// A workflow with no list-scoped nodes and an unlisted trigger: empty.
	doc := &WorkflowDoc{RootNodeID: "t", Nodes: []*models.AutomationNode{
		testNode("t", NodeTypeTrigger, map[string]any{"event_kind": EventKindContactCreated, "frequency": FrequencyOnce}, strPtr("d")),
		testNode("d", NodeTypeDelay, map[string]any{"duration": 1, "unit": "hours"}, nil),
	}}
	if ids := doc.RequiredListIDs(); len(ids) != 0 {
		t.Fatalf("workflow without list nodes = %v, want empty", ids)
	}

	doc = &WorkflowDoc{RootNodeID: "t", Nodes: []*models.AutomationNode{
		triggerNode(strPtr("ls")),
		testNode("ls", NodeTypeListStatus, map[string]any{"list_id": 7}, strPtr("add")),
		testNode("add", NodeTypeAddToList, map[string]any{"list_id": 7}, strPtr("rem")),
		testNode("rem", NodeTypeRemoveFromList, map[string]any{"list_id": 9}, nil),
	}}

	// The trigger carries list_id 1; list_status and add_to_list both carry 7
	// (one entry after dedup); remove_from_list carries 9.
	ids := doc.RequiredListIDs()
	if len(ids) != 3 {
		t.Fatalf("RequiredListIDs = %v, want exactly [1 7 9] with 7 deduped", ids)
	}
	seen := map[uint]bool{}
	for _, id := range ids {
		if id != 1 && id != 7 && id != 9 {
			t.Errorf("unexpected list id %d in %v", id, ids)
		}
		if seen[id] {
			t.Errorf("list id %d appears twice: %v is not deduped", id, ids)
		}
		seen[id] = true
	}
}

func TestRequiredListIDsEmptyAndZeroSkipped(t *testing.T) {
	doc := &WorkflowDoc{RootNodeID: "t", Nodes: []*models.AutomationNode{
		testNode("t", NodeTypeTrigger, map[string]any{"event_kind": EventKindListSubscribed}, strPtr("add")),
		testNode("add", NodeTypeAddToList, map[string]any{"list_id": 0}, nil),
	}}
	if ids := doc.RequiredListIDs(); len(ids) != 0 {
		t.Fatalf("zero list ids must be skipped, got %v", ids)
	}
}

// ---------- DefaultNodeConfig ----------

func TestDefaultNodeConfigShapes(t *testing.T) {
	trig := DefaultNodeConfig(NodeTypeTrigger)
	if trig["event_kind"] != EventKindListSubscribed || trig["frequency"] != FrequencyOnce {
		t.Errorf("trigger defaults = %v, want list.subscribed/once", trig)
	}
	if _, ok := trig["conditions"].([]any); !ok {
		t.Errorf("trigger conditions default must be an empty array, got %T", trig["conditions"])
	}

	delay := DefaultNodeConfig(NodeTypeDelay)
	if delay["duration"] != 1 || delay["unit"] != "hours" {
		t.Errorf("delay defaults = %v, want 1/hours", delay)
	}

	if cfg := DefaultNodeConfig(NodeTypeEmail); len(cfg) != 0 {
		t.Errorf("email default config must be empty (template chosen explicitly), got %v", cfg)
	}

	filter := DefaultNodeConfig(NodeTypeFilter)
	for _, key := range []string{"conditions", "continue_node_id", "exit_node_id"} {
		if _, ok := filter[key]; !ok {
			t.Errorf("filter default missing key %q: %v", key, filter)
		}
	}

	ab := DefaultNodeConfig(NodeTypeABTest)
	variants, ok := ab["variants"].([]any)
	if !ok || len(variants) != 2 {
		t.Fatalf("ab_test default variants = %v, want two variants", ab["variants"])
	}
	a := variants[0].(map[string]any)
	b := variants[1].(map[string]any)
	if a["weight"] != 50 || b["weight"] != 50 || a["id"] != "a" || b["id"] != "b" {
		t.Errorf("ab_test default variant weights/ids = %v / %v", a, b)
	}

	ls := DefaultNodeConfig(NodeTypeListStatus)
	for _, key := range []string{"list_id", "active_node_id", "non_active_node_id", "not_in_list_node_id"} {
		if _, ok := ls[key]; !ok {
			t.Errorf("list_status default missing key %q", key)
		}
	}

	wh := DefaultNodeConfig(NodeTypeWebhook)
	if wh["method"] != "POST" || wh["url"] != "" {
		t.Errorf("webhook defaults = %v, want empty url and POST", wh)
	}

	for _, typ := range []string{NodeTypeAddToList, NodeTypeRemoveFromList} {
		if cfg := DefaultNodeConfig(typ); cfg["list_id"] != nil {
			t.Errorf("%s default = %v, want nil list_id", typ, cfg)
		}
	}

	if cfg := DefaultNodeConfig("banana"); cfg != nil {
		t.Errorf("unknown type default = %v, want nil", cfg)
	}
}

// ---------- EvaluateConditions ----------

func testSubscriber() *models.Subscriber {
	return &models.Subscriber{
		ID:     9,
		Email:  "alice@example.com",
		Name:   "Alice Anderson",
		Status: models.SubscriberStatusSubscribed,
		CustomFields: models.CustomFields{
			"plan":  "pro",
			"tier":  nil, // present but NULL
			"level": "gold",
		},
	}
}

func rule(field, operator string, value any) models.FilterRule {
	return models.FilterRule{Field: field, Operator: operator, Value: value}
}

func TestEvaluateConditionsEmptyMatchesVacuously(t *testing.T) {
	if !EvaluateConditions(nil, nil, nil) {
		t.Fatal("no conditions must match (vacuous truth), even with a nil subscriber")
	}
}

func TestEvaluateConditionsNilSubscriberNeverMatches(t *testing.T) {
	if EvaluateConditions([]models.FilterRule{rule("email", "equals", "alice@example.com")}, nil, nil) {
		t.Fatal("nil subscriber must never match")
	}
}

func TestEvaluateConditionsDirectFields(t *testing.T) {
	sub := testSubscriber()

	cases := []struct {
		name string
		rule models.FilterRule
		want bool
	}{
		{"email equals hit", rule("email", "equals", "alice@example.com"), true},
		{"email equals miss", rule("email", "equals", "bob@example.com"), false},
		{"email not_equals miss", rule("email", "not_equals", "alice@example.com"), false},
		{"email not_equals hit", rule("email", "not_equals", "bob@example.com"), true},
		{"email contains hit", rule("email", "contains", "EXAMPLE"), true},
		{"email contains miss", rule("email", "contains", "bob"), false},
		{"name equals", rule("name", "equals", "Alice Anderson"), true},
		{"name contains", rule("name", "contains", "ander"), true},
		{"status equals", rule("status", "equals", "subscribed"), true},
		{"status not_equals", rule("status", "not_equals", "unsubscribed"), true},
		{"unsupported operator on direct field", rule("email", "exists", nil), false},
		{"unknown field", rule("banana", "equals", "x"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateConditions([]models.FilterRule{tc.rule}, sub, nil); got != tc.want {
				t.Fatalf("rule %+v = %v, want %v", tc.rule, got, tc.want)
			}
		})
	}
}

func TestEvaluateConditionsCustomFields(t *testing.T) {
	sub := testSubscriber()

	cases := []struct {
		name string
		rule models.FilterRule
		want bool
	}{
		{"exists hit", rule("custom_fields.plan", "exists", nil), true},
		{"exists on NULL value", rule("custom_fields.tier", "exists", nil), false},
		{"exists on missing key", rule("custom_fields.absent", "exists", nil), false},
		{"not_exists on missing key", rule("custom_fields.absent", "not_exists", nil), true},
		{"not_exists on NULL value", rule("custom_fields.tier", "not_exists", nil), true},
		{"not_exists hit", rule("custom_fields.plan", "not_exists", nil), false},
		{"equals hit", rule("custom_fields.plan", "equals", "pro"), true},
		{"equals miss", rule("custom_fields.plan", "equals", "free"), false},
		{"equals on missing key (SQL NULL semantics)", rule("custom_fields.absent", "not_equals", "pro"), false},
		{"not_equals hit", rule("custom_fields.plan", "not_equals", "free"), true},
		{"contains hit", rule("custom_fields.level", "contains", "OLD"), true},
		{"contains miss", rule("custom_fields.level", "contains", "silver"), false},
		{"unsupported operator", rule("custom_fields.plan", "in", "pro"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateConditions([]models.FilterRule{tc.rule}, sub, nil); got != tc.want {
				t.Fatalf("rule %+v = %v, want %v", tc.rule, got, tc.want)
			}
		})
	}
}

func TestEvaluateConditionsInList(t *testing.T) {
	sub := testSubscriber()
	member := func(listID uint) bool { return listID == 3 }

	if !EvaluateConditions([]models.FilterRule{rule("in_list", "equals", 3)}, sub, member) {
		t.Error("in_list 3 must match for a member of list 3")
	}
	if EvaluateConditions([]models.FilterRule{rule("in_list", "equals", 4)}, sub, member) {
		t.Error("in_list 4 must not match for a member of list 3 only")
	}
	// The operator is irrelevant; the value is the list id.
	if !EvaluateConditions([]models.FilterRule{rule("in_list", "whatever", "3")}, sub, member) {
		t.Error("in_list with a numeric string value must coerce and match")
	}
	// No checker, no match.
	if EvaluateConditions([]models.FilterRule{rule("in_list", "equals", 3)}, sub, nil) {
		t.Error("in_list without a list checker must never match")
	}
	// Non-numeric value never matches.
	if EvaluateConditions([]models.FilterRule{rule("in_list", "equals", "banana")}, sub, member) {
		t.Error("in_list with a non-numeric value must never match")
	}
}

func TestEvaluateConditionsANDSemantics(t *testing.T) {
	sub := testSubscriber()
	member := func(uint) bool { return true }

	// Both true -> true.
	both := []models.FilterRule{
		rule("email", "equals", "alice@example.com"),
		rule("custom_fields.plan", "equals", "pro"),
	}
	if !EvaluateConditions(both, sub, member) {
		t.Error("AND of two matching rules must match")
	}

	// One false -> false.
	mixed := []models.FilterRule{
		rule("email", "equals", "alice@example.com"),
		rule("custom_fields.plan", "equals", "free"),
	}
	if EvaluateConditions(mixed, sub, member) {
		t.Error("AND with one failing rule must not match")
	}

	// The checker joins the conjunction too.
	mixedList := []models.FilterRule{
		rule("email", "equals", "alice@example.com"),
		rule("in_list", "equals", 3),
	}
	if !EvaluateConditions(mixedList, sub, member) {
		t.Error("email + in_list both matching must match")
	}
}
