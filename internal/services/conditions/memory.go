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
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/goposta/posta/internal/models"
)

// The in-memory FilterRule evaluator (SF-103): moved verbatim from the
// automation package (graph.go EvaluateConditions + engine.go
// evaluateConditionSet) so the unified surface owns the implementation and
// the original sites delegate. The behavior is byte-for-byte the original's
// — the automation package's own tests (graph_test.go, engine_segment_test.go)
// pin it unchanged through the delegates.

// EvaluateFilterRules reports whether a subscriber satisfies all conditions
// (AND-combined). An empty condition set matches vacuously (a trigger with no
// conditions fires for every matching event).
//
// Supported rules (anything else — unknown field or operator — never matches):
//
//	email / name / status   operators equals, not_equals, contains
//	custom_fields.<key>     operators equals, not_equals, contains, exists, not_exists
//	in_list                 value is the list id; the operator is ignored and
//	                        listChecker decides membership
//
// Comparisons are string-based (custom field values and rule values are
// coerced to strings); contains is case-insensitive, matching the ILIKE
// behavior of the segment filters. There is no expression or code evaluation.
// A nil subscriber, a missing custom field, or a nil listChecker never matches.
func EvaluateFilterRules(conditions []models.FilterRule, sub *models.Subscriber, listChecker func(listID uint) bool) bool {
	if len(conditions) == 0 {
		return true
	}
	if sub == nil {
		return false
	}
	for i := range conditions {
		if !evaluateFilterRule(conditions[i], sub, listChecker) {
			return false
		}
	}
	return true
}

// FieldInSegment is the condition field referencing a saved segment (R-14).
// It lives here (not only in the automation package) because the in-memory
// evaluator owns the field's evaluation: segmentChecker decides membership
// (nil checker ⇒ in_segment never matches, so a store without segment
// support degrades to closed), and an unparsable id is a non-match.
const FieldInSegment = "in_segment"

// EvaluateFilterRuleSet AND-combines a condition set with the R-14 in_segment
// handling (moved verbatim from automation's evaluateConditionSet): in_segment
// rules resolve through segmentChecker; every other rule goes through
// EvaluateFilterRules. An empty set matches.
func EvaluateFilterRuleSet(conditions []models.FilterRule, sub *models.Subscriber, listChecker func(uint) bool, segmentChecker func(uint) bool) bool {
	pass := make([]models.FilterRule, 0, len(conditions))
	for _, rule := range conditions {
		if strings.TrimSpace(rule.Field) != FieldInSegment {
			pass = append(pass, rule)
			continue
		}
		id, ok := CoerceUint(rule.Value)
		if !ok || segmentChecker == nil {
			return false
		}
		if !segmentChecker(id) {
			return false
		}
	}
	return EvaluateFilterRules(pass, sub, listChecker)
}

// evaluateFilterRule evaluates a single rule against the subscriber.
func evaluateFilterRule(rule models.FilterRule, sub *models.Subscriber, listChecker func(uint) bool) bool {
	switch rule.Field {
	case "email":
		return compareStringField(sub.Email, rule.Operator, rule.Value)
	case "name":
		return compareStringField(sub.Name, rule.Operator, rule.Value)
	case "status":
		return compareStringField(string(sub.Status), rule.Operator, rule.Value)
	case "in_list":
		id, ok := CoerceUint(rule.Value)
		if !ok || listChecker == nil {
			return false
		}
		return listChecker(id)
	}
	if strings.HasPrefix(rule.Field, "custom_fields.") {
		return matchCustomField(rule, sub)
	}
	return false // unknown field never matches
}

// compareStringField applies the direct-field operators to a string value.
func compareStringField(fieldValue, operator string, value any) bool {
	switch operator {
	case "equals":
		return fieldValue == CoerceString(value)
	case "not_equals":
		return fieldValue != CoerceString(value)
	case "contains":
		return strings.Contains(strings.ToLower(fieldValue), strings.ToLower(CoerceString(value)))
	default:
		return false
	}
}

// matchCustomField applies the custom-field operators to sub.CustomFields[key].
// A missing key never matches (SQL NULL semantics: not_equals on a missing
// field is also false).
func matchCustomField(rule models.FilterRule, sub *models.Subscriber) bool {
	key := strings.TrimPrefix(rule.Field, "custom_fields.")
	raw, present := sub.CustomFields[key]
	switch rule.Operator {
	case "exists":
		return present && raw != nil
	case "not_exists":
		return !present || raw == nil
	case "equals":
		return present && CoerceString(raw) == CoerceString(rule.Value)
	case "not_equals":
		return present && CoerceString(raw) != CoerceString(rule.Value)
	case "contains":
		return present && strings.Contains(strings.ToLower(CoerceString(raw)), strings.ToLower(CoerceString(rule.Value)))
	default:
		return false
	}
}

// CoerceString renders a JSON scalar as a string for comparisons.
func CoerceString(value any) string {
	switch t := value.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

// CoerceUint converts a JSON scalar to a uint (list ids). Accepts numbers
// (integral only) and numeric strings.
func CoerceUint(value any) (uint, bool) {
	switch t := value.(type) {
	case uint:
		return t, true
	case int:
		if t >= 0 {
			return uint(t), true
		}
	case int64:
		if t >= 0 {
			return uint(t), true
		}
	case float64:
		if t >= 0 && t <= 1e15 && t == math.Trunc(t) {
			return uint(t), true
		}
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return CoerceUint(f)
		}
	case string:
		if n, err := strconv.ParseUint(strings.TrimSpace(t), 10, 64); err == nil {
			return uint(n), true
		}
	}
	return 0, false
}
