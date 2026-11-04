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

package models

import (
	"database/sql/driver"
	"encoding/json"
	"time"
)

// SegmentLogic is the combinator joining a condition's groups (and a group's
// rules): "and" or "or".
type SegmentLogic string

const (
	SegmentLogicAnd SegmentLogic = "and"
	SegmentLogicOr  SegmentLogic = "or"
)

// SegmentConditionRule is one leaf predicate: field/operator/value.
type SegmentConditionRule struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value,omitempty"`
}

// SegmentConditionGroup joins its rules with its own logic.
type SegmentConditionGroup struct {
	Logic SegmentLogic           `json:"logic"`
	Rules []SegmentConditionRule `json:"rules"`
}

// SegmentCondition is the saved audience filter document. Exactly one level of
// nesting: groups of rules, groups joined by the condition logic, rules joined
// by the group logic. It is stored as a JSONB column (see Segment.Condition).
type SegmentCondition struct {
	Logic  SegmentLogic            `json:"logic"`
	Groups []SegmentConditionGroup `json:"groups"`
}

// Value marshals the condition for the database (driver.Valuer, mirroring the
// FilterRules pattern in subscriber_list.go).
func (sc SegmentCondition) Value() (driver.Value, error) {
	if len(sc.Groups) == 0 && sc.Logic == "" {
		return "{}", nil
	}
	return json.Marshal(sc)
}

// Scan unmarshals a stored condition (driver.Scanner).
func (sc *SegmentCondition) Scan(value interface{}) error {
	if value == nil {
		*sc = SegmentCondition{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		s, ok := value.(string)
		if !ok {
			return nil
		}
		bytes = []byte(s)
	}
	return json.Unmarshal(bytes, sc)
}

// IsEmpty reports whether the condition carries no groups, so callers can
// distinguish "absent" from a saved (possibly trivially false) filter.
func (sc SegmentCondition) IsEmpty() bool {
	return len(sc.Groups) == 0
}

// Segment is a saved audience filter over the subscribers table. Campaigns can
// target a segment instead of a subscriber list.
type Segment struct {
	ID          uint             `json:"id" gorm:"primaryKey"`
	UserID      uint             `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint            `json:"workspace_id,omitempty"`
	Name        string           `json:"name" gorm:"not null"`
	Description string           `json:"description"`
	Condition   SegmentCondition `json:"condition" gorm:"type:jsonb"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   *time.Time       `json:"updated_at"`
	User        User             `json:"-" gorm:"foreignKey:UserID"`
}
