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
	"encoding/json"
	"errors"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// SF-103 tests, part 1: the automation currency's behavior pinned through
// the unified path. The same matrix the automation package's own tests pin
// (they run unchanged against the delegates) — here it runs through the
// conditions package's evaluator and the adapter.

func sub() *models.Subscriber {
	return &models.Subscriber{
		Email:        "Alice@Example.com",
		Name:         "Alice",
		Status:       models.SubscriberStatusSubscribed,
		CustomFields: map[string]any{"plan": "pro", "seats": float64(3)},
	}
}

func rule(field, operator string, value any) models.FilterRule {
	return models.FilterRule{Field: field, Operator: operator, Value: value}
}

func TestMemoryEmptySetMatchesVacuously(t *testing.T) {
	if !EvaluateFilterRules(nil, nil, nil) {
		t.Fatal("an empty condition set must match vacuously (even a nil subscriber)")
	}
}

func TestMemoryNilSubscriberNeverMatches(t *testing.T) {
	if EvaluateFilterRules([]models.FilterRule{rule("email", "equals", "a@b.c")}, nil, nil) {
		t.Fatal("a nil subscriber must never match")
	}
}

func TestMemoryDirectFieldOperators(t *testing.T) {
	cases := []struct {
		field, operator string
		value           any
		want            bool
	}{
		{"email", "equals", "Alice@Example.com", true},
		{"email", "equals", "alice@example.com", false}, // equals is case-sensitive
		{"email", "contains", "alice@EXAMPLE", true},    // contains is case-insensitive
		{"email", "not_equals", "other@x.y", true},
		{"email", "unknown_op", "x", false},
		{"name", "equals", "Alice", true},
		{"name", "contains", "lic", true},
		{"status", "equals", "subscribed", true},
		{"status", "equals", "unsubscribed", false},
		{"unknown_field", "equals", "x", false}, // unknown field never matches
	}
	for _, tc := range cases {
		got := EvaluateFilterRules([]models.FilterRule{rule(tc.field, tc.operator, tc.value)}, sub(), nil)
		if got != tc.want {
			t.Errorf("%s %s %v = %v, want %v", tc.field, tc.operator, tc.value, got, tc.want)
		}
	}
}

func TestMemoryCustomFieldOperators(t *testing.T) {
	cases := []struct {
		field, operator string
		value           any
		want            bool
	}{
		{"custom_fields.plan", "equals", "pro", true},
		{"custom_fields.plan", "equals", "PRO", false},
		{"custom_fields.plan", "contains", "r", true},
		{"custom_fields.plan", "not_equals", "free", true},
		{"custom_fields.missing", "not_equals", "free", false}, // NULL semantics: false too
		{"custom_fields.missing", "exists", nil, false},
		{"custom_fields.missing", "not_exists", nil, true},
		{"custom_fields.plan", "exists", nil, true},
		{"custom_fields.plan", "not_exists", nil, false},
		{"custom_fields.seats", "equals", float64(3), true}, // numeric values coerce to strings
		{"custom_fields.plan", "unknown_op", "pro", false},
	}
	for _, tc := range cases {
		got := EvaluateFilterRules([]models.FilterRule{rule(tc.field, tc.operator, tc.value)}, sub(), nil)
		if got != tc.want {
			t.Errorf("%s %s %v = %v, want %v", tc.field, tc.operator, tc.value, got, tc.want)
		}
	}
}

func TestMemoryInListAndUnknownValueShapes(t *testing.T) {
	calls := 0
	checker := func(uint) bool { calls++; return true }

	if !EvaluateFilterRules([]models.FilterRule{rule("in_list", "equals", float64(7))}, sub(), checker) || calls != 1 {
		t.Fatal("in_list must resolve through the list checker")
	}
	if EvaluateFilterRules([]models.FilterRule{rule("in_list", "equals", "not-a-number")}, sub(), checker) {
		t.Fatal("an unparsable in_list value must never match")
	}
	if EvaluateFilterRules([]models.FilterRule{rule("in_list", "equals", float64(7))}, sub(), nil) {
		t.Fatal("a nil list checker must never match")
	}
}

func TestMemoryInSegmentSemantics(t *testing.T) {
	member := func(uint) bool { return true }
	stranger := func(uint) bool { return false }

	set := []models.FilterRule{rule("in_segment", "equals", float64(3))}
	if !EvaluateFilterRuleSet(set, sub(), nil, member) {
		t.Fatal("an in_segment rule must match when the checker says member")
	}
	if EvaluateFilterRuleSet(set, sub(), nil, stranger) {
		t.Fatal("an in_segment rule must not match when the checker says stranger")
	}
	if EvaluateFilterRuleSet(set, sub(), nil, nil) {
		t.Fatal("a nil segment checker must degrade in_segment to closed")
	}
	if EvaluateFilterRuleSet([]models.FilterRule{rule("in_segment", "equals", "NaN")}, sub(), nil, member) {
		t.Fatal("an unparsable segment id must never match")
	}

	// AND-combination: the in_segment rule gates the whole set.
	both := []models.FilterRule{rule("in_segment", "equals", float64(3)), rule("email", "equals", "Alice@Example.com")}
	if EvaluateFilterRuleSet(both, sub(), nil, stranger) {
		t.Fatal("a failing in_segment rule must fail the set even when the rest match")
	}
	if !EvaluateFilterRuleSet(both, sub(), nil, member) {
		t.Fatal("all rules holding must match the set")
	}
}

func TestMemoryCoercion(t *testing.T) {
	if CoerceString(nil) != "" || CoerceString(true) != "true" || CoerceString(float64(3)) != "3" {
		t.Fatal("CoerceString scalar rendering drifted")
	}
	var n json.Number = "42"
	if CoerceString(n) != "42" {
		t.Fatal("CoerceString json.Number drifted")
	}
	if v, ok := CoerceUint(float64(7)); !ok || v != 7 {
		t.Fatal("CoerceUint float64 drifted")
	}
	if _, ok := CoerceUint(float64(-1)); ok {
		t.Fatal("CoerceUint must refuse negatives")
	}
	if _, ok := CoerceUint(1.5); ok {
		t.Fatal("CoerceUint must refuse non-integral floats")
	}
	if v, ok := CoerceUint(" 12 "); !ok || v != 12 {
		t.Fatal("CoerceUint numeric strings drifted")
	}
}

// TestAutomationAdapterPinsTheUnifiedPath — the adapter must answer exactly
// what the direct evaluator answers, name its currency, and refuse the SQL
// mode.
func TestAutomationAdapterPinsTheUnifiedPath(t *testing.T) {
	rules := []models.FilterRule{
		rule("in_segment", "equals", float64(9)),
		rule("email", "contains", "alice"),
	}
	member := func(uint) bool { return true }
	stranger := func(uint) bool { return false }

	ev := NewAutomationEvaluator(rules, nil, member)
	if ev.Currency() != CurrencyAutomation {
		t.Fatalf("Currency() = %q, want automation", ev.Currency())
	}
	got, err := ev.MatchesSubscriber(sub())
	if err != nil || !got {
		t.Fatalf("adapter MatchesSubscriber = %v/%v, want true/nil (direct path)", got, err)
	}
	ev2 := NewAutomationEvaluator(rules, nil, stranger)
	if got, _ := ev2.MatchesSubscriber(sub()); got {
		t.Fatal("the adapter must not match when the segment checker refuses")
	}
	if _, err := ev.WhereSQL(nil); !errors.Is(err, ErrModeNotSupported) {
		t.Fatalf("the automation adapter's SQL mode = %v, want ErrModeNotSupported", err)
	}
}
