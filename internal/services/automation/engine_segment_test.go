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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package automation

import (
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
)

// R-14 tests: the in_segment filter condition. evaluateConditionSet must
// resolve in_segment rules through the segment checker while AND-combining the
// remaining rules with the shared evaluator; subscriberInSegment must evaluate
// membership live over the saved segment's condition, scoped to the
// automation's workspace, and close on every unusable segment.

func wsScope() repositories.ResourceScope {
	ws := uint(3)
	return repositories.ResourceScope{UserID: 7, WorkspaceID: &ws}
}

// An in_segment rule resolves through the segment checker; the rest of the
// set is AND-combined as usual.
func TestEvaluateConditionSetInSegment(t *testing.T) {
	sub := &models.Subscriber{ID: 9, Email: "alice@example.com"}
	inSegment := func(id uint) bool { return id == 5 }
	noSegment := func(uint) bool { return false }

	cases := []struct {
		name       string
		conditions []models.FilterRule
		checker    func(uint) bool
		want       bool
	}{
		{
			name:       "in_segment matching checker",
			conditions: []models.FilterRule{{Field: FieldInSegment, Value: "5"}},
			checker:    inSegment,
			want:       true,
		},
		{
			name:       "in_segment non-matching checker",
			conditions: []models.FilterRule{{Field: FieldInSegment, Value: "5"}},
			checker:    noSegment,
			want:       false,
		},
		{
			name:       "in_segment with unparseable value closes",
			conditions: []models.FilterRule{{Field: FieldInSegment, Value: "banana"}},
			checker:    inSegment,
			want:       false,
		},
		{
			name:       "in_segment with nil checker closes",
			conditions: []models.FilterRule{{Field: FieldInSegment, Value: "5"}},
			checker:    nil,
			want:       false,
		},
		{
			name: "in_segment AND other rule",
			conditions: []models.FilterRule{
				{Field: FieldInSegment, Value: "5"},
				{Field: "email", Operator: "contains", Value: "bob"},
			},
			checker: inSegment,
			want:    false,
		},
		{
			name: "in_segment AND passing rule",
			conditions: []models.FilterRule{
				{Field: FieldInSegment, Value: "5"},
				{Field: "email", Operator: "contains", Value: "alice"},
			},
			checker: inSegment,
			want:    true,
		},
		{
			name: "non-segment rules ignore the checker",
			conditions: []models.FilterRule{
				{Field: "email", Operator: "contains", Value: "alice"},
			},
			checker: noSegment,
			want:    true,
		},
	}
	for _, tc := range cases {
		if got := evaluateConditionSet(tc.conditions, sub, func(uint) bool { return false }, tc.checker); got != tc.want {
			t.Errorf("%s: evaluateConditionSet = %v, want %v", tc.name, got, tc.want)
		}
	}
}

const aliceSegmentCondition = `{"logic":"and","groups":[{"logic":"and","rules":[{"field":"email","operator":"equals","value":"alice@example.com"}]}]}`

// subscriberInSegment evaluates the saved segment condition live, inside the
// caller's workspace scope.
func TestSubscriberInSegmentLiveEvaluation(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	engSeed(t, state, "segments", map[string]any{
		"id": int64(12), "user_id": uint(7), "workspace_id": uint(3),
		"name": "alice only", "condition": aliceSegmentCondition,
	})
	engSeed(t, state, "subscribers", map[string]any{
		"id": uint(9), "user_id": uint(7), "workspace_id": uint(3),
		"email": "alice@example.com", "status": "subscribed",
	})
	engSeed(t, state, "subscribers", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": uint(3),
		"email": "bob@example.com", "status": "subscribed",
	})

	scope := wsScope()
	if !e.subscriberInSegment(scope, 12, 9) {
		t.Fatal("subscriber 9 must be a member of segment 12")
	}
	if e.subscriberInSegment(scope, 12, 10) {
		t.Fatal("subscriber 10 (bob) must not be a member of the alice-only segment")
	}
}

// Every unusable segment closes: missing, foreign workspace, condition-less
// and invalid-condition segments match nobody.
func TestSubscriberInSegmentClosesOnUnusableSegment(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	engSeed(t, state, "segments", map[string]any{
		"id": int64(12), "user_id": uint(7), "workspace_id": uint(3),
		"name": "alice only", "condition": aliceSegmentCondition,
	})
	engSeed(t, state, "segments", map[string]any{
		"id": int64(13), "user_id": uint(7), "workspace_id": uint(3),
		"name": "no condition", "condition": "{}",
	})
	engSeed(t, state, "segments", map[string]any{
		"id": int64(14), "user_id": uint(7), "workspace_id": uint(3),
		"name": "invalid condition", "condition": `{"logic":"maybe"}`,
	})
	engSeed(t, state, "subscribers", map[string]any{
		"id": uint(9), "user_id": uint(7), "workspace_id": uint(3),
		"email": "alice@example.com", "status": "subscribed",
	})

	scope := wsScope()
	if e.subscriberInSegment(scope, 999, 9) {
		t.Fatal("missing segment must close")
	}
	foreign := uint(99)
	if e.subscriberInSegment(repositories.ResourceScope{UserID: 7, WorkspaceID: &foreign}, 12, 9) {
		t.Fatal("segment outside the scope must close")
	}
	if e.subscriberInSegment(scope, 13, 9) {
		t.Fatal("condition-less segment must close")
	}
	if e.subscriberInSegment(scope, 14, 9) {
		t.Fatal("invalid-condition segment must close")
	}
}

// The in_segment membership check is workspace-scoped on the subscriber side
// too: a subscriber outside the scope never matches, even with a matching
// email.
func TestSubscriberInSegmentScopeFiltersSubscriber(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	engSeed(t, state, "segments", map[string]any{
		"id": int64(12), "user_id": uint(7), "workspace_id": uint(3),
		"name": "alice only", "condition": aliceSegmentCondition,
	})
	engSeed(t, state, "subscribers", map[string]any{
		"id": uint(11), "user_id": uint(7), "workspace_id": uint(99),
		"email": "alice@example.com", "status": "subscribed",
	})

	if e.subscriberInSegment(wsScope(), 12, 11) {
		t.Fatal("a foreign-workspace subscriber must never match")
	}
}
