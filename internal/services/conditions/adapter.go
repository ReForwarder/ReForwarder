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
	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// The three currency adapters (SF-103): each wraps its currency's own
// implementation — this package's moved, verbatim code — behind the unified
// Evaluator interface. The adapters add no logic of their own: every method
// delegates, so behavior is byte-for-byte the original's, and the mode each
// currency does not speak returns ErrModeNotSupported.

// automationEvaluator adapts the in-memory FilterRule evaluation (the
// automation currency): rule sets with in_list / in_segment membership
// checks that no SQL query can express.
type automationEvaluator struct {
	conditions     []models.FilterRule
	listChecker    func(uint) bool
	segmentChecker func(uint) bool
}

// NewAutomationEvaluator builds the automation currency's adapter. The
// checkers are the caller's membership seams (list ids / segment ids);
// nil segmentChecker degrades in_segment rules to closed, exactly as the
// original evaluateConditionSet always did.
func NewAutomationEvaluator(conditions []models.FilterRule, listChecker, segmentChecker func(uint) bool) Evaluator {
	return automationEvaluator{conditions: conditions, listChecker: listChecker, segmentChecker: segmentChecker}
}

func (a automationEvaluator) Currency() Currency { return CurrencyAutomation }

func (a automationEvaluator) MatchesSubscriber(sub *models.Subscriber) (bool, error) {
	return EvaluateFilterRuleSet(a.conditions, sub, a.listChecker, a.segmentChecker), nil
}

func (a automationEvaluator) WhereSQL(q *gorm.DB) (*gorm.DB, error) {
	return nil, ErrModeNotSupported
}

// filterRuleEvaluator adapts the dynamic subscriber-list FilterRule SQL
// engine.
type filterRuleEvaluator struct {
	rules models.FilterRules
}

// NewFilterRuleEvaluator builds the FilterRule currency's adapter.
func NewFilterRuleEvaluator(rules models.FilterRules) Evaluator {
	return filterRuleEvaluator{rules: rules}
}

func (f filterRuleEvaluator) Currency() Currency { return CurrencyFilterRule }

func (f filterRuleEvaluator) MatchesSubscriber(sub *models.Subscriber) (bool, error) {
	return false, ErrModeNotSupported
}

func (f filterRuleEvaluator) WhereSQL(q *gorm.DB) (*gorm.DB, error) {
	return ApplyFilterRuleSQL(q, f.rules), nil
}

// segmentEvaluator adapts the SegmentCondition SQL builder.
type segmentEvaluator struct {
	cond models.SegmentCondition
}

// NewSegmentEvaluator builds the Segment currency's adapter.
func NewSegmentEvaluator(cond models.SegmentCondition) Evaluator {
	return segmentEvaluator{cond: cond}
}

func (s segmentEvaluator) Currency() Currency { return CurrencySegment }

func (s segmentEvaluator) MatchesSubscriber(sub *models.Subscriber) (bool, error) {
	return false, ErrModeNotSupported
}

// WhereSQL appends the condition's compiled WHERE fragment exactly as every
// pre-SF-103 call site did (q.Where(expr, args...)) — gorm itself skips an
// empty fragment, so an empty condition adds no restriction, unchanged.
func (s segmentEvaluator) WhereSQL(q *gorm.DB) (*gorm.DB, error) {
	expr, args, err := BuildSegmentConditionSQL(s.cond)
	if err != nil {
		return nil, err
	}
	return q.Where(expr, args...), nil
}

// Compile-time checks: all three adapters satisfy the unified interface.
var (
	_ Evaluator = automationEvaluator{}
	_ Evaluator = filterRuleEvaluator{}
	_ Evaluator = segmentEvaluator{}
)
