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

import "time"

// AutomationNodeRun is one node execution on a journey's timeline. A row is
// created with Action=entered before the node executes and is flipped in place
// to completed or failed afterwards, so the (node_id, node_type, action)
// aggregate behind the stats canvas counts each execution exactly once.
type AutomationNodeRun struct {
	ID           uint   `json:"id" gorm:"primaryKey"`
	JourneyID    uint   `json:"journey_id" gorm:"index;not null"`
	AutomationID uint   `json:"automation_id" gorm:"index;not null"`
	NodeID       string `json:"node_id" gorm:"size:120;not null"`
	NodeType     string `json:"node_type" gorm:"size:20;not null"`
	Action       string `json:"action" gorm:"size:12;not null"` // entered|processing|completed|failed
	// Output is the node result as a JSON object string (email_uuid,
	// status_code, chosen branch, ...).
	Output string `json:"output" gorm:"type:jsonb;not null;default:'{}'"`
	Error  string `json:"error,omitempty" gorm:"type:text"`
	// DurationMs is the node execution time; set on completion.
	DurationMs  int        `json:"duration_ms"`
	EnteredAt   time.Time  `json:"entered_at" gorm:"not null"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// TableName overrides GORM's default table naming.
func (AutomationNodeRun) TableName() string { return "automation_node_runs" }
