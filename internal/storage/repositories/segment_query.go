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

package repositories

import (
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/conditions"
)

// Segment condition query builder — the SF-103 delegate shim.
//
// The implementation moved verbatim to the conditions package
// (internal/services/conditions/segment_sql.go) so the unified evaluator
// surface owns it; these wrappers keep the repositories package's own names
// (and its cross-engine tests, which drive the legacy applyFilterRules
// against the segment builder) working unchanged. The behavior is
// byte-for-byte the original's.

// Operator names accepted in SegmentConditionRule.Operator — kept here (the
// same literals the moved implementation defines privately) because this
// package's builder tests name them.
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

// colUnsubscribedAt is the shared unsubscribed-at column the email_repo's
// export whitelist references (the segment builder carries its own private
// copy of the same literal).
const colUnsubscribedAt = "unsubscribed_at"

// segmentScalarEngagementFields mirrors the moved implementation's
// ScalarEngagementFields (single source of truth: the conditions package's
// map) for this package's cross-engine test, which checks the field kind to
// pick the segment operator.
var segmentScalarEngagementFields = conditions.ScalarEngagementFields

// ValidateSegmentCondition validates one saved-segment condition document
// and returns one problem string per violation (empty = valid). The
// implementation lives in the conditions package.
func ValidateSegmentCondition(cond models.SegmentCondition) []string {
	return conditions.ValidateSegmentCondition(cond)
}

// BuildSegmentConditionSQL translates a validated SegmentCondition document
// into a parameterized WHERE fragment (placeholders are `?` bindvars; the
// values are returned separately and handed to gorm). The implementation
// lives in the conditions package.
func BuildSegmentConditionSQL(cond models.SegmentCondition) (string, []any, error) {
	return conditions.BuildSegmentConditionSQL(cond)
}
