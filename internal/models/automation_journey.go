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

// AutomationJourney is one contact's run through an automation. The journey
// advances by compare-and-swap on the status column: cursor updates, retries
// and terminal transitions only apply while the row is still active, so a
// concurrent worker (or a re-run after a crash) can never double-advance a
// journey that has already completed, exited or failed.
type AutomationJourney struct {
	ID           uint   `json:"id" gorm:"primaryKey"`
	UUID         string `json:"uuid" gorm:"type:uuid;default:gen_random_uuid();uniqueIndex"`
	AutomationID uint   `json:"automation_id" gorm:"index;not null"`
	WorkspaceID  *uint  `json:"workspace_id,omitempty" gorm:"index"`
	SubscriberID uint   `json:"subscriber_id" gorm:"index;not null"`
	Email        string `json:"email" gorm:"size:255;not null;index"`
	// CurrentNodeID is the node the journey is parked at or currently
	// executing; empty before the first advance.
	CurrentNodeID string `json:"current_node_id" gorm:"size:120"`
	Status        string `json:"status" gorm:"size:12;not null;default:active;index"` // active|completed|exited|failed
	ExitReason    string `json:"exit_reason,omitempty" gorm:"size:50"`
	// ScheduledAt parks the journey until a delay node elapses (or the
	// enrollment instant); nil means runnable now.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty" gorm:"index"`
	RetryCount  int        `json:"retry_count" gorm:"default:0"`
	LastError   string     `json:"last_error,omitempty" gorm:"type:text"`
	// ContextJSON carries the last custom-event properties and similar
	// per-journey render data.
	ContextJSON string    `json:"context_json,omitempty" gorm:"type:jsonb;not null;default:'{}'"`
	EnteredAt   time.Time `json:"entered_at" gorm:"index;not null"`
	// LastAdvancedAt is the last cursor, retry or terminal transition time.
	LastAdvancedAt *time.Time `json:"last_advanced_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// TableName overrides GORM's default table naming.
func (AutomationJourney) TableName() string { return "automation_journeys" }
