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

// Package conditions is the unified condition-evaluator surface (SF-103):
// one Evaluator interface over the three condition currencies the platform
// already evaluated separately, with adapters that delegate to each
// currency's own implementation so every caller's behavior is preserved
// byte-for-byte.
//
// The three currencies (they are NOT merged into one grammar — their
// operator vocabularies and field sets are deliberately per-currency, and
// merging them would change behavior):
//
//   - CurrencyAutomation — models.FilterRule rule sets evaluated IN MEMORY
//     against one subscriber (automation triggers and filter nodes; R-14
//     in_segment rules resolve through an injected segment checker).
//   - CurrencyFilterRule — models.FilterRules compiled to a parameterized
//     SQL WHERE (the dynamic subscriber-list engine).
//   - CurrencySegment — models.SegmentCondition documents compiled to a
//     parameterized SQL WHERE (saved segments, campaign audience
//     resolution, in_segment membership).
//
// The invariants every adapter shares (pinned by the consistency tests):
// an empty condition set matches everything; rules combine with AND (with
// the currency's own group-logic layer on top where it has one); values are
// bound as parameters — the only string ever interpolated into SQL text is
// a custom-field key that survived that currency's strict key whitelist.
package conditions

import (
	"errors"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// Currency names the condition dialect an adapter speaks.
type Currency string

const (
	// CurrencyAutomation — in-memory models.FilterRule evaluation with
	// in_list / in_segment membership checks (the automation engine).
	CurrencyAutomation Currency = "automation"
	// CurrencyFilterRule — models.FilterRules compiled to SQL (the dynamic
	// subscriber-list engine).
	CurrencyFilterRule Currency = "filter_rule"
	// CurrencySegment — models.SegmentCondition compiled to SQL (saved
	// segments and their audience resolution).
	CurrencySegment Currency = "segment"
)

// ErrModeNotSupported is returned by an adapter for the evaluation mode its
// currency does not implement: the SQL currencies evaluate in the database
// (they cannot answer MatchesSubscriber), and the in-memory currency has no
// SQL representation. Callers switch on errors.Is.
var ErrModeNotSupported = errors.New("conditions: evaluation mode not supported by this currency")

// Evaluator is the unified condition-evaluation contract (SF-103). One
// interface over the three currencies; each adapter implements the mode its
// currency speaks and returns ErrModeNotSupported for the other.
type Evaluator interface {
	// Currency names the dialect this adapter speaks.
	Currency() Currency
	// MatchesSubscriber evaluates the condition set in memory against one
	// subscriber (AND-combined). The automation currency's mode.
	MatchesSubscriber(sub *models.Subscriber) (bool, error)
	// WhereSQL applies the condition set's WHERE restrictions onto the given
	// query (already scoped by the caller) and returns it. The two SQL
	// currencies' mode.
	WhereSQL(q *gorm.DB) (*gorm.DB, error)
}
