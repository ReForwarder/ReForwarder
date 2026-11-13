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
	"encoding/json"
	"strings"
	"time"

	"github.com/lib/pq"
)

// EmailQueueScheduleStatus is the lifecycle state of one per-recipient queue
// schedule row (SF-08).
type EmailQueueScheduleStatus string

const (
	// ScheduleStatusActive means the recipient's retry schedule is still
	// running: more attempts may happen, or the message is awaiting delivery.
	ScheduleStatusActive EmailQueueScheduleStatus = "active"
	// ScheduleStatusDelivered means the message carrying this recipient was
	// accepted for delivery; the schedule is closed as fulfilled.
	ScheduleStatusDelivered EmailQueueScheduleStatus = "delivered"
	// ScheduleStatusExhausted means the recipient's configurable retry
	// interval list ran out (expiry by attempt count), or the recipient was
	// permanently rejected: no further attempts are scheduled for it.
	ScheduleStatusExhausted EmailQueueScheduleStatus = "exhausted"
	// ScheduleStatusExpired means the message's TTL elapsed before delivery
	// (expiry by TTL): the schedule is closed by the expiry sweep.
	ScheduleStatusExpired EmailQueueScheduleStatus = "expired"
	// ScheduleStatusCancelled means the message was finalized terminal by a
	// non-retry path (configuration error, content refusal, …) while this
	// schedule was still running.
	ScheduleStatusCancelled EmailQueueScheduleStatus = "cancelled"
)

// EmailQueueSchedule is one per-recipient row of the persistent multi-queue
// manager (SF-08): the recipient's configurable retry interval list, the
// notify addresses to inform through the existing event taxonomy when the
// schedule reaches a terminal state, and the attempt ledger.
//
// Rows are keyed uniquely on (email_id, recipient). They are created additively
// — per-message schedule requests or the platform defaults — and consulted at
// claim time to schedule the retry (ProcessIn/ProcessAt). A message without
// rows keeps the pre-SF-08 queue behavior untouched.
//
// Envelope caveat (documented in the SF-08 ledger entry): SMTP delivery is
// envelope-granular — the relay/MTA paths deliver a message's surviving
// recipients in one envelope — so the schedule table gates the shared
// message-level retry clock (the earliest due row) and tracks each
// recipient's attempt ledger and terminality; it does not split the envelope
// per recipient.
type EmailQueueSchedule struct {
	ID uint `json:"id" gorm:"primaryKey"`

	EmailID uint `json:"email_id" gorm:"not null;uniqueIndex:idx_email_queue_schedules_email_recipient"`
	// Recipient is the envelope recipient address this row schedules.
	Recipient string `json:"recipient" gorm:"size:320;not null;uniqueIndex:idx_email_queue_schedules_email_recipient"`

	UserID      uint   `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint  `json:"workspace_id,omitempty" gorm:"index"`
	Provenance  string `json:"provenance,omitempty" gorm:"size:32;default:''"`

	// RetryIntervals is the recipient's configurable retry interval list in
	// minutes, stored as a JSON array (e.g. "[5,30,120,1440]"). Empty means
	// the row carries no interval schedule of its own: it stays active until
	// the message finalizes, and its notify addresses fire on terminality
	// without overriding the queue's retry timing.
	RetryIntervals string `json:"retry_intervals" gorm:"type:jsonb;not null;default:'[]'"`

	// NotifyAddresses are the per-recipient delivery-notification targets.
	// When the row reaches a terminal state (exhausted, expired, cancelled)
	// they ride the existing event taxonomy: an email.failed timeline event
	// with the schedule metadata, and — when addresses exist — an
	// email.failed webhook carrying the per-recipient notify payload.
	NotifyAddresses pq.StringArray `json:"notify_addresses" gorm:"type:text[];default:'{}'"`

	// AttemptIndex counts the consumed interval entries: after the k-th
	// recorded failure (0-based), AttemptIndex = k and the next retry lands at
	// now + RetryIntervals[k] minutes; reaching len(RetryIntervals) exhausts
	// the schedule (expiry by attempt count).
	AttemptIndex int `json:"attempt_index" gorm:"default:0;not null"`

	// NextRetryAt is the persisted due time of the next attempt for this
	// recipient. Nil = no retry has been scheduled yet (or none remains).
	NextRetryAt *time.Time `json:"next_retry_at,omitempty" gorm:"index"`

	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastError     string     `json:"last_error"`

	Status EmailQueueScheduleStatus `json:"status" gorm:"size:16;default:active;not null;index"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IntervalList decodes the stored JSON interval list into minutes.
func (s *EmailQueueSchedule) IntervalList() []int {
	return DecodeScheduleIntervals(s.RetryIntervals)
}

// ScheduleTerminal reports whether the row left the active state.
func (s *EmailQueueSchedule) ScheduleTerminal() bool {
	return s.Status != ScheduleStatusActive
}

// DecodeScheduleIntervals parses a stored interval list ("[5,30,120]") into
// minutes. Unparsable input yields nil — a nil list never schedules a retry
// and never exhausts a row, so corrupt state degrades to passive tracking.
func DecodeScheduleIntervals(raw string) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []int
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// EncodeScheduleIntervals serializes an interval list (minutes) for storage.
func EncodeScheduleIntervals(intervals []int) string {
	if len(intervals) == 0 {
		return "[]"
	}
	b, err := json.Marshal(intervals)
	if err != nil {
		return "[]"
	}
	return string(b)
}
