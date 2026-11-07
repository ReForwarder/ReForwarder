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

package models

import "time"

// MTA delivery attempt outcomes (ADR-001 §5). One row per SMTP delivery
// attempt, written before the email row's status changes so the attempt log is
// durable even when the status update races a crash.
const (
	// MTAAttemptDelivered — the remote MX answered 2xx at the end of DATA.
	// This is the only outcome that may produce a delivered email status.
	MTAAttemptDelivered = "delivered"
	// MTAAttemptTransientFailure — 4xx (or connection-level) failure; the
	// message is re-enqueued with the existing backoff.
	MTAAttemptTransientFailure = "transient_failure"
	// MTAAttemptPermanentFailure — 5xx rejection by the remote MX; feeds the
	// existing bounce/suppression chain.
	MTAAttemptPermanentFailure = "permanent_failure"
	// MTAAttemptDeferredBudget — the per-domain hourly budget was exhausted;
	// the message is deferred (retry later) without contacting the MX.
	MTAAttemptDeferredBudget = "deferred_budget"
	// MTAAttemptRefusedUnverifiedSender — fail-closed: direct delivery was
	// requested for a sender domain that is not ownership-verified.
	MTAAttemptRefusedUnverifiedSender = "refused_unverified_sender"
	// MTAAttemptDNSFailure — MX resolution failed; no connection was opened.
	MTAAttemptDNSFailure = "dns_failure"
)

// MTADeliveryAttempt is the durable transport-event record for direct-to-MX
// delivery (ADR-001 §5, SF-13/56/52 substrate). Attempts are append-only:
// every dial to every MX host writes exactly one row, successful or not.
type MTADeliveryAttempt struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	WorkspaceID  *uint     `json:"workspace_id,omitempty" gorm:"index"`
	EmailID      uint      `json:"email_id" gorm:"not null;index:idx_mta_delivery_attempts_email_attempt,priority:1"`
	Attempt      int       `json:"attempt" gorm:"not null;default:0;index:idx_mta_delivery_attempts_email_attempt,priority:2"`
	MXHost       string    `json:"mx_host" gorm:"size:255"`
	IP           string    `json:"ip" gorm:"size:64"`
	ResponseCode int       `json:"response_code"`
	ResponseText string    `json:"response_text" gorm:"type:text"`
	Outcome      string    `json:"outcome" gorm:"size:40;not null"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	CreatedAt    time.Time `json:"created_at"`
}
