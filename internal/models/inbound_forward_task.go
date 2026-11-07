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

// InboundForwardTaskStatus is the lifecycle of one queued forwarding task.
type InboundForwardTaskStatus string

const (
	// InboundForwardTaskPending: queued, not yet picked up by the executor.
	InboundForwardTaskPending InboundForwardTaskStatus = "pending"
	// InboundForwardTaskRunning: an executor holds the task.
	InboundForwardTaskRunning InboundForwardTaskStatus = "running"
	// InboundForwardTaskDone: the forwarded copy was accepted for delivery.
	InboundForwardTaskDone InboundForwardTaskStatus = "done"
	// InboundForwardTaskFailed: retries exhausted (or a permanent failure).
	InboundForwardTaskFailed InboundForwardTaskStatus = "failed"
)

// InboundForwardTask is the durable forwarding queue row written by the
// routing engine when a matched route's action is forward (ADR-002 decision
// 2). The forwarding executor (Option B Phase 2, wave 2) consumes due rows,
// re-sends the stored original through the outbound pipeline with
// system_forward provenance, and retries with the standard backoff. The
// original inbound message stays durably stored regardless of the task's
// outcome.
type InboundForwardTask struct {
	ID          uint  `json:"id" gorm:"primaryKey"`
	UserID      uint  `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint `json:"workspace_id,omitempty" gorm:"index"`
	DomainID    uint  `json:"domain_id" gorm:"index;not null"`
	// InboundEmailID references the stored original; unique so a retried or
	// racing ingest cannot enqueue a second forward for the same message —
	// the enqueue uses INSERT ... ON CONFLICT DO NOTHING.
	InboundEmailID uint `json:"inbound_email_id" gorm:"uniqueIndex;not null"`
	RouteID        uint `json:"route_id" gorm:"index;not null"`
	// Target is the destination address the executor re-sends to.
	Target string                   `json:"target" gorm:"size:320;not null"`
	Status InboundForwardTaskStatus `json:"status" gorm:"size:20;not null;default:'pending'"`
	// Attempts counts executor attempts; NextAttemptAt schedules the next one
	// (standard backoff, standard queue ops).
	Attempts      int       `json:"attempts" gorm:"not null;default:0"`
	NextAttemptAt time.Time `json:"next_attempt_at" gorm:"index;not null"`
	LastError     string    `json:"last_error,omitempty" gorm:"type:text"`
	// Hops is the X-Forwarded-For-style forward depth of the forward this task
	// performs: 1 for a first forward, 2 when the copy already carries one
	// forwarding hop, and so on. The forwarding executor derives it from the
	// stored original's X-Posta-Forwarded-For header and rejects a task whose
	// hop count would exceed the loop limit (> 3). Additive executor column.
	Hops int `json:"hops" gorm:"not null;default:0"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	InboundEmail InboundEmail `json:"-" gorm:"foreignKey:InboundEmailID;constraint:OnDelete:CASCADE"`
}

// TableName overrides GORM's default table naming.
func (InboundForwardTask) TableName() string { return "inbound_forward_tasks" }
