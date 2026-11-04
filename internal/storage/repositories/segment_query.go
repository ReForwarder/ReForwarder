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

package repositories

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
)

// Engagement field names and the shared unsubscribed-at column, hoisted so
// the whitelist maps, the switch cases and the legacy engine all reference
// single identifiers.
const (
	engOpenedCampaign    = "opened_campaign"
	engClickedCampaign   = "clicked_campaign"
	engNotOpenedCampaign = "not_opened_campaign"
	engReceivedCampaign  = "received_campaign"
	engInactiveDays      = "inactive_days"
	engActiveDays        = "active_days"
	engTotalOpensGte     = "total_opens_gte"
	engTotalClicksGte    = "total_clicks_gte"
	colUnsubscribedAt    = "unsubscribed_at"
)

// Segment condition query builder.
//
// A SegmentCondition document is translated into a parameterized WHERE
// fragment. Placeholders are always `?` bindvars — values are returned
// separately and handed to gorm, so user input never lands in the SQL text.
// Field names are whitelisted, custom-field keys must be [A-Za-z0-9_] (and are
// the only string ever interpolated, after that strict check), and the
// engagement subqueries are the same shapes the dynamic subscriber-list engine
// (subscriber_repo.go applyFilterRules) has always produced.

// Operator names accepted in SegmentConditionRule.Operator.
const (
	opEquals      = "equals"
	opNotEquals   = "not_equals"
	opContains    = "contains"
	opNotContains = "not_contains"
	opStartsWith  = "starts_with"
	opEndsWith    = "ends_with"
	opIsEmpty     = "is_empty"
	opIsNotEmpty  = "is_not_empty"
	opGreaterThan = "greater_than"
	opLessThan    = "less_than"
	opOneOf       = "one_of"
	opNotOneOf    = "not_one_of"
	opExists      = "exists"
	opNotExists   = "not_exists"
)

var (
	textOperators = []string{
		opEquals, opNotEquals, opContains, opNotContains,
		opStartsWith, opEndsWith, opIsEmpty, opIsNotEmpty,
		opOneOf, opNotOneOf,
	}
	statusOperators = []string{opEquals, opNotEquals, opOneOf, opNotOneOf}
	dateOperators   = []string{opEquals, opNotEquals, opGreaterThan, opLessThan}
	// subscribed_at / unsubscribed_at are nullable; created_at is not.
	nullableDateOperators = []string{opEquals, opNotEquals, opGreaterThan, opLessThan, opIsEmpty, opIsNotEmpty}
	customFieldOperators  = []string{opEquals, opNotEquals, opContains, opNotContains, opStartsWith, opEndsWith, opIsEmpty, opIsNotEmpty, opGreaterThan, opLessThan, opOneOf, opNotOneOf, opExists, opNotExists}
	campaignEngageOps     = []string{opEquals, opNotEquals}
	scalarEngageOps       = []string{opGreaterThan, opLessThan}
)

// Whitelisted direct columns of the subscribers table mapped to their allowed
// operators. Column names are used verbatim in SQL, hence the closed set.
var segmentDirectFields = map[string][]string{
	"email":           textOperators,
	"name":            textOperators,
	"status":          statusOperators,
	"timezone":        textOperators,
	"language":        textOperators,
	"created_at":      dateOperators,
	"subscribed_at":   nullableDateOperators,
	colUnsubscribedAt: nullableDateOperators,
}

// engagement.opened_campaign / clicked_campaign / not_opened_campaign /
// received_campaign carry a campaign id (or a boolean for the campaign-agnostic
// "opened anything" reading); the *_days / *_gte fields carry a number.
var segmentCampaignEngagementFields = map[string]bool{
	engOpenedCampaign:    true,
	engClickedCampaign:   true,
	engNotOpenedCampaign: true,
	engReceivedCampaign:  true,
}

var segmentScalarEngagementFields = map[string][]string{
	engInactiveDays:   scalarEngageOps,
	engActiveDays:     scalarEngageOps,
	engTotalOpensGte:  scalarEngageOps,
	engTotalClicksGte: scalarEngageOps,
}

// Engagement subquery shapes, copied verbatim from applyFilterRules so a
// segment and a dynamic list with the same rule resolve identically.
const (
	segEngageOpenedAny  = "EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at IS NOT NULL)"
	segEngageClickedAny = "EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.clicked_at IS NOT NULL)"
	// The campaign-scoped variants are the exact applyFilterRules strings.
	segEngageOpenedCampaign   = "EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.campaign_id = ? AND campaign_messages.opened_at IS NOT NULL)"
	segEngageClickedCampaign  = "EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.campaign_id = ? AND campaign_messages.clicked_at IS NOT NULL)"
	segEngageReceivedCampaign = "EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.campaign_id = ?)"
	segEngageOpensCount       = "(SELECT COUNT(*) FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at IS NOT NULL)"
	segEngageClicksCount      = "(SELECT COUNT(*) FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.clicked_at IS NOT NULL)"
)

// isValidSegmentCustomKey reports whether key may be interpolated into the
// JSONB extraction. Strictly [A-Za-z0-9_]+ — stricter than the legacy
// isAlphanumericUnderscore (which also allows '-'), matching the API contract.
func isValidSegmentCustomKey(key string) bool {
	if key == "" {
		return false
	}
	for _, c := range key {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

var segmentLikeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLikePattern neutralizes LIKE wildcards in user input so "100%" matches
// the literal string, not every value ending in "100".
func escapeLikePattern(s string) string {
	return segmentLikeEscaper.Replace(s)
}

// isSegmentDateField reports whether the field is a timestamp column, whose
// comparisons need parsed time values and whose emptiness check must not
// compare against ”.
func isSegmentDateField(field string) bool {
	switch field {
	case "created_at", "subscribed_at", colUnsubscribedAt:
		return true
	}
	return false
}

// parseSegmentDateValue accepts RFC3339 or a plain YYYY-MM-DD (UTC) string.
func parseSegmentDateValue(v any) (time.Time, error) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, errors.New("date value must be a string (RFC3339 or YYYY-MM-DD)")
	}
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("cannot parse date %q: use RFC3339 or YYYY-MM-DD", s)
}

// segmentScalarString renders a JSON scalar for comparisons against the text
// produced by the ->>'key' extraction, which always yields text.
func segmentScalarString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	default:
		return "", fmt.Errorf("value must be a string, number, or boolean")
	}
}

// segmentNumericValue coerces the rule value to a float for day/count/numeric
// comparisons. Numeric strings are accepted because API clients routinely
// serialize numbers as strings.
func segmentNumericValue(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, fmt.Errorf("value must be a number")
		}
		return f, nil
	default:
		return 0, fmt.Errorf("value must be a number")
	}
}

func allowedSegmentOperator(ops []string, op string) bool {
	for _, candidate := range ops {
		if candidate == op {
			return true
		}
	}
	return false
}

// ValidateSegmentCondition checks a condition document structurally and
// semantically. It returns address-free problems (no pointers), one per
// violated rule, each naming the offending group and rule with 1-based
// indices — e.g. `group 1 rule 2: unknown field "emailx"`.
func ValidateSegmentCondition(cond models.SegmentCondition) []string {
	var errs []string
	if cond.Logic != models.SegmentLogicAnd && cond.Logic != models.SegmentLogicOr {
		errs = append(errs, fmt.Sprintf(`logic must be "and" or "or", got %q`, string(cond.Logic)))
	}
	if len(cond.Groups) == 0 {
		errs = append(errs, "condition must contain at least one group")
		return errs
	}
	for gi, group := range cond.Groups {
		at := gi + 1
		if group.Logic != models.SegmentLogicAnd && group.Logic != models.SegmentLogicOr {
			errs = append(errs, fmt.Sprintf(`group %d: logic must be "and" or "or", got %q`, at, string(group.Logic)))
		}
		if len(group.Rules) == 0 {
			errs = append(errs, fmt.Sprintf("group %d: must contain at least one rule", at))
			continue
		}
		for ri, rule := range group.Rules {
			errs = append(errs, validateSegmentRule(at, ri+1, rule)...)
		}
	}
	return errs
}

// validateSegmentRule returns the problems of a single rule, prefixed with its
// 1-based group/rule address.
func validateSegmentRule(group, rule int, r models.SegmentConditionRule) []string {
	at := func(format string, a ...any) string {
		return fmt.Sprintf("group %d rule %d: %s", group, rule, fmt.Sprintf(format, a...))
	}
	field := r.Field

	if strings.HasPrefix(field, "engagement.") {
		name := strings.TrimPrefix(field, "engagement.")
		if segmentCampaignEngagementFields[name] {
			if !allowedSegmentOperator(campaignEngageOps, r.Operator) {
				return []string{at("operator %q is not allowed for field %q", r.Operator, field)}
			}
			switch r.Value.(type) {
			case bool, float64, int, int64, string:
			default:
				return []string{at("field %q requires a boolean or a campaign id", field)}
			}
			if s, isStr := r.Value.(string); isStr {
				if _, err := segmentNumericValue(s); err != nil {
					return []string{at("field %q requires a boolean or a campaign id", field)}
				}
			}
			return nil
		}
		if _, ok := segmentScalarEngagementFields[name]; ok {
			if !allowedSegmentOperator(segmentScalarEngagementFields[name], r.Operator) {
				return []string{at("operator %q is not allowed for field %q", r.Operator, field)}
			}
			if _, err := segmentNumericValue(r.Value); err != nil {
				return []string{at("field %q requires a numeric value", field)}
			}
			return nil
		}
		return []string{at("unknown field %q", field)}
	}

	if strings.HasPrefix(field, "custom_fields.") {
		key := strings.TrimPrefix(field, "custom_fields.")
		if !isValidSegmentCustomKey(key) {
			return []string{at("invalid custom field key %q: use letters, digits, and underscores only", key)}
		}
		if !allowedSegmentOperator(customFieldOperators, r.Operator) {
			return []string{at("operator %q is not allowed for field %q", r.Operator, field)}
		}
		return validateSegmentValue(at, r, segmentFieldKindCustom)
	}

	ops, ok := segmentDirectFields[field]
	if !ok {
		return []string{at("unknown field %q", field)}
	}
	if !allowedSegmentOperator(ops, r.Operator) {
		return []string{at("operator %q is not allowed for field %q", r.Operator, field)}
	}
	if isSegmentDateField(field) {
		return validateSegmentValue(at, r, segmentFieldKindDate)
	}
	if field == "status" {
		return validateSegmentValue(at, r, segmentFieldKindText)
	}
	return validateSegmentValue(at, r, segmentFieldKindText)
}

type segmentFieldKind int

const (
	segmentFieldKindText segmentFieldKind = iota
	segmentFieldKindDate
	segmentFieldKindCustom
)

// noValueOperators can stand without a value.
var noValueOperators = map[string]bool{
	opIsEmpty: true, opIsNotEmpty: true, opExists: true, opNotExists: true,
}

func validateSegmentValue(at func(string, ...any) string, r models.SegmentConditionRule, kind segmentFieldKind) []string {
	if noValueOperators[r.Operator] {
		return nil
	}
	if r.Value == nil {
		return []string{at("value is required for operator %q", r.Operator)}
	}
	switch r.Operator {
	case opOneOf, opNotOneOf:
		arr, ok := r.Value.([]any)
		if !ok || len(arr) == 0 {
			return []string{at("operator %q requires a non-empty array value", r.Operator)}
		}
		for _, el := range arr {
			if _, err := segmentScalarString(el); err != nil {
				return []string{at("operator %q requires string, number, or boolean elements", r.Operator)}
			}
		}
		return nil
	case opContains, opNotContains, opStartsWith, opEndsWith:
		if kind == segmentFieldKindDate {
			// Unreachable: dates never allow LIKE operators; kept for safety.
			return []string{at("operator %q is not allowed for date fields", r.Operator)}
		}
		if s, ok := r.Value.(string); !ok {
			return []string{at("operator %q requires a string value", r.Operator)}
		} else if kind != segmentFieldKindCustom && s == "" {
			return []string{at("operator %q requires a non-empty string value", r.Operator)}
		}
		return nil
	case opGreaterThan, opLessThan:
		if kind == segmentFieldKindDate {
			if _, err := parseSegmentDateValue(r.Value); err != nil {
				return []string{at("%s", err.Error())}
			}
			return nil
		}
		if _, err := segmentNumericValue(r.Value); err != nil {
			return []string{at("operator %q requires a numeric value", r.Operator)}
		}
		return nil
	case opEquals, opNotEquals:
		switch kind {
		case segmentFieldKindDate:
			if _, err := parseSegmentDateValue(r.Value); err != nil {
				return []string{at("%s", err.Error())}
			}
		case segmentFieldKindText:
			if _, ok := r.Value.(string); !ok {
				return []string{at("value must be a string")}
			}
		case segmentFieldKindCustom:
			if _, err := segmentScalarString(r.Value); err != nil {
				return []string{at("value must be a string, number, or boolean")}
			}
		}
		return nil
	case opIsEmpty, opIsNotEmpty:
		return nil
	}
	return nil
}

// BuildSegmentConditionSQL translates a validated condition document into a
// WHERE fragment with `?` placeholders plus the argument list, in order. Groups
// are wrapped in parentheses and joined by the condition logic; rules inside a
// group are joined by the group logic. An invalid condition yields an error
// carrying every problem, semicolon-joined.
func BuildSegmentConditionSQL(cond models.SegmentCondition) (string, []any, error) {
	if errs := ValidateSegmentCondition(cond); len(errs) > 0 {
		return "", nil, errors.New(strings.Join(errs, "; "))
	}

	args := make([]any, 0, 8)
	groupExprs := make([]string, 0, len(cond.Groups))
	for _, group := range cond.Groups {
		ruleExprs := make([]string, 0, len(group.Rules))
		for _, rule := range group.Rules {
			expr, ruleArgs, err := buildSegmentRule(rule)
			if err != nil {
				return "", nil, err
			}
			ruleExprs = append(ruleExprs, expr)
			args = append(args, ruleArgs...)
		}
		joiner := " AND "
		if group.Logic == models.SegmentLogicOr {
			joiner = " OR "
		}
		groupExprs = append(groupExprs, "("+strings.Join(ruleExprs, joiner)+")")
	}
	joiner := " AND "
	if cond.Logic == models.SegmentLogicOr {
		joiner = " OR "
	}
	return strings.Join(groupExprs, joiner), args, nil
}

// buildSegmentRule emits the SQL for one rule. Validation has already passed;
// the error returns here are defensive.
func buildSegmentRule(r models.SegmentConditionRule) (string, []any, error) {
	field := r.Field

	if strings.HasPrefix(field, "engagement.") {
		return buildSegmentEngagementRule(field, r.Operator, r.Value)
	}
	if strings.HasPrefix(field, "custom_fields.") {
		key := strings.TrimPrefix(field, "custom_fields.")
		if !isValidSegmentCustomKey(key) {
			return "", nil, fmt.Errorf("invalid custom field key %q", key)
		}
		return buildSegmentCustomFieldRule(key, r.Operator, r.Value)
	}
	if _, ok := segmentDirectFields[field]; !ok {
		return "", nil, fmt.Errorf("unknown field %q", field)
	}

	if isSegmentDateField(field) {
		return buildSegmentDateRule(field, r.Operator, r.Value)
	}
	return buildSegmentTextRule(field, r.Operator, r.Value)
}

func joinSegmentInList(col, op string, value any) (string, []any, error) {
	arr, ok := value.([]any)
	if !ok || len(arr) == 0 {
		return "", nil, fmt.Errorf("operator %q requires a non-empty array value", op)
	}
	vals := make([]any, 0, len(arr))
	for _, el := range arr {
		s, err := segmentScalarString(el)
		if err != nil {
			return "", nil, fmt.Errorf("operator %q requires string, number, or boolean elements", op)
		}
		vals = append(vals, s)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")
	keyword := "IN"
	if op == opNotOneOf {
		keyword = "NOT IN"
	}
	return fmt.Sprintf("%s %s (%s)", col, keyword, placeholders), vals, nil
}

// buildSegmentTextRule handles the plain-text subscriber columns (email, name,
// status, timezone, language). LIKE values are wildcard-escaped.
func buildSegmentTextRule(field, op string, value any) (string, []any, error) {
	switch op {
	case opEquals, opNotEquals:
		s, ok := value.(string)
		if !ok {
			return "", nil, fmt.Errorf("field %q requires a string value", field)
		}
		if op == opEquals {
			return field + " = ?", []any{s}, nil
		}
		return field + " <> ?", []any{s}, nil
	case opContains, opNotContains, opStartsWith, opEndsWith:
		s, ok := value.(string)
		if !ok {
			return "", nil, fmt.Errorf("field %q requires a string value", field)
		}
		var pattern string
		switch op {
		case opContains:
			pattern = "%" + escapeLikePattern(s) + "%"
		case opNotContains:
			pattern = "%" + escapeLikePattern(s) + "%"
		case opStartsWith:
			pattern = escapeLikePattern(s) + "%"
		case opEndsWith:
			pattern = "%" + escapeLikePattern(s)
		}
		kw := "ILIKE"
		if op == opNotContains {
			kw = "NOT ILIKE"
		}
		return fmt.Sprintf("%s %s ?", field, kw), []any{pattern}, nil
	case opIsEmpty:
		return "(" + field + " IS NULL OR " + field + " = '')", nil, nil
	case opIsNotEmpty:
		return "(" + field + " IS NOT NULL AND " + field + " <> '')", nil, nil
	case opOneOf, opNotOneOf:
		return joinSegmentInList(field, op, value)
	}
	return "", nil, fmt.Errorf("operator %q is not allowed for field %q", op, field)
}

// buildSegmentDateRule handles created_at / subscribed_at / unsubscribed_at.
// Values are parsed (RFC3339 or YYYY-MM-DD) and bound as timestamps.
func buildSegmentDateRule(field, op string, value any) (string, []any, error) {
	switch op {
	case opEquals, opNotEquals, opGreaterThan, opLessThan:
		t, err := parseSegmentDateValue(value)
		if err != nil {
			return "", nil, err
		}
		var comparator string
		switch op {
		case opEquals:
			comparator = "="
		case opNotEquals:
			comparator = "<>"
		case opGreaterThan:
			comparator = ">"
		case opLessThan:
			comparator = "<"
		}
		return fmt.Sprintf("%s %s ?", field, comparator), []any{t}, nil
	case opIsEmpty:
		return field + " IS NULL", nil, nil
	case opIsNotEmpty:
		return field + " IS NOT NULL", nil, nil
	}
	return "", nil, fmt.Errorf("operator %q is not allowed for field %q", op, field)
}

// buildSegmentCustomFieldRule handles custom_fields.<key>. The key is the only
// interpolated string and has passed the strict [A-Za-z0-9_] check. The ->>”
// extraction returns text, so scalar values are compared as strings; numeric
// comparisons cast the extracted text (NULL-only when the row's value is empty
// or missing) to numeric — a stored non-numeric value makes Postgres reject
// that row's comparison, which the caller surfaces as a query error.
func buildSegmentCustomFieldRule(key, op string, value any) (string, []any, error) {
	col := fmt.Sprintf("custom_fields::jsonb->>'%s'", key)
	switch op {
	case opEquals, opNotEquals:
		s, err := segmentScalarString(value)
		if err != nil {
			return "", nil, fmt.Errorf("custom field comparison requires a string, number, or boolean value")
		}
		if op == opEquals {
			return col + " = ?", []any{s}, nil
		}
		return col + " <> ?", []any{s}, nil
	case opContains, opNotContains, opStartsWith, opEndsWith:
		s, ok := value.(string)
		if !ok {
			return "", nil, fmt.Errorf("operator %q requires a string value", op)
		}
		var pattern string
		switch op {
		case opContains, opNotContains:
			pattern = "%" + escapeLikePattern(s) + "%"
		case opStartsWith:
			pattern = escapeLikePattern(s) + "%"
		case opEndsWith:
			pattern = "%" + escapeLikePattern(s)
		}
		kw := "ILIKE"
		if op == opNotContains {
			kw = "NOT ILIKE"
		}
		return fmt.Sprintf("%s %s ?", col, kw), []any{pattern}, nil
	case opIsEmpty:
		return "(" + col + " IS NULL OR " + col + " = '')", nil, nil
	case opIsNotEmpty:
		return "(" + col + " IS NOT NULL AND " + col + " <> '')", nil, nil
	case opGreaterThan, opLessThan:
		n, err := segmentNumericValue(value)
		if err != nil {
			return "", nil, fmt.Errorf("operator %q requires a numeric value", op)
		}
		comparator := ">"
		if op == opLessThan {
			comparator = "<"
		}
		return fmt.Sprintf("(NULLIF(%s, '')::numeric %s ?)", col, comparator), []any{n}, nil
	case opOneOf, opNotOneOf:
		return joinSegmentInList(col, op, value)
	case opExists:
		return col + " IS NOT NULL", nil, nil
	case opNotExists:
		return col + " IS NULL", nil, nil
	}
	return "", nil, fmt.Errorf("operator %q is not allowed for field %q", op, "custom_fields."+key)
}

// buildSegmentEngagementRule reproduces the applyFilterRules engagement
// subqueries. Campaign-scoped fields accept a campaign id (the verbatim
// subquery) or a boolean (the campaign-agnostic "opened/clicked anything"
// reading, the same subquery with the campaign_id conjunct dropped);
// greater_than/less_than on the *_days and *_gte fields select the polarity.
func buildSegmentEngagementRule(field, op string, value any) (string, []any, error) {
	name := strings.TrimPrefix(field, "engagement.")

	switch name {
	case engOpenedCampaign, engClickedCampaign, engNotOpenedCampaign, engReceivedCampaign:
		var base string
		var arg any
		switch v := value.(type) {
		case bool:
			switch name {
			case engOpenedCampaign:
				base = segEngageOpenedAny
			case engClickedCampaign:
				base = segEngageClickedAny
			case engNotOpenedCampaign:
				base = segEngageOpenedAny
			case engReceivedCampaign:
				base = segEngageReceivedCampaign
			}
			want := v
			if name == engNotOpenedCampaign {
				want = !v
			}
			if op == opNotEquals {
				want = !want
			}
			if !want {
				base = "NOT " + base
			}
			return base, nil, nil
		default:
			id, err := segmentNumericValue(value)
			if err != nil {
				return "", nil, fmt.Errorf("field %q requires a boolean or a campaign id", field)
			}
			arg = id
			switch name {
			case engOpenedCampaign:
				base = segEngageOpenedCampaign
			case engClickedCampaign:
				base = segEngageClickedCampaign
			case engNotOpenedCampaign:
				base = "NOT " + segEngageOpenedCampaign
			case engReceivedCampaign:
				base = segEngageReceivedCampaign
			}
			if op == opNotEquals {
				if strings.HasPrefix(base, "NOT ") {
					base = strings.TrimPrefix(base, "NOT ")
				} else {
					base = "NOT " + base
				}
			}
			return base, []any{arg}, nil
		}

	case engInactiveDays, engActiveDays:
		n, err := segmentNumericValue(value)
		if err != nil {
			return "", nil, fmt.Errorf("field %q requires a numeric value", field)
		}
		window := "EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at > NOW() - INTERVAL '1 day' * ?)"
		// inactive_days > N  ≡  no opens within the last N days.
		// active_days   > N  ≡  an open within the last N days.
		pos := name == engActiveDays
		if op == opLessThan {
			pos = !pos
		}
		if pos {
			return window, []any{n}, nil
		}
		return "NOT " + window, []any{n}, nil

	case engTotalOpensGte, engTotalClicksGte:
		n, err := segmentNumericValue(value)
		if err != nil {
			return "", nil, fmt.Errorf("field %q requires a numeric value", field)
		}
		count := segEngageOpensCount
		if name == engTotalClicksGte {
			count = segEngageClicksCount
		}
		comparator := ">="
		if op == opLessThan {
			comparator = "<"
		}
		return fmt.Sprintf("%s %s ?", count, comparator), []any{n}, nil
	}
	return "", nil, fmt.Errorf("unknown field %q", field)
}
