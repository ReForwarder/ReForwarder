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

// Cron run history (SF-99): one additive row per completed cron job run,
// written by the cron manager's wrapper (scheduled ticks and manual triggers
// alike). The table is deployment-wide operational data — cron jobs are not
// tenant-scoped — and is read only through the admin-gated history endpoint.

// Cron run triggers and outcomes. Kept as plain string columns with pinned
// constants (the code sets them, never the client).
const (
	// CronTriggerSchedule — the run came from the cron scheduler's tick.
	CronTriggerSchedule = "schedule"
	// CronTriggerManual — the run came from POST /admin/jobs/{name}/trigger.
	CronTriggerManual = "manual"
)

const (
	// CronOutcomeSuccess — the job's Run returned nil.
	CronOutcomeSuccess = "success"
	// CronOutcomeFailed — the job's Run returned an error; Detail carries it.
	CronOutcomeFailed = "failed"
)

// CronRunHistory is one completed run of one cron job. StartedAt/FinishedAt
// bracket the job's Run call (server clock, UTC); Detail carries the error
// text for failed runs and is empty for successes.
type CronRunHistory struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// JobName is the registered cron job's Name (e.g. "queue_reconcile").
	JobName string `json:"job_name" gorm:"size:255;not null;index"`
	// Trigger records what started the run: CronTriggerSchedule or
	// CronTriggerManual.
	Trigger string `json:"trigger" gorm:"size:16;not null;default:'schedule'"`
	// StartedAt is the instant the job's Run began (before it executed).
	StartedAt time.Time `json:"started_at" gorm:"not null"`
	// FinishedAt is the instant Run returned (success or failure).
	FinishedAt time.Time `json:"finished_at"`
	// Outcome is CronOutcomeSuccess or CronOutcomeFailed.
	Outcome string `json:"outcome" gorm:"size:16;not null"`
	// Detail carries the failure's error text; empty on success.
	Detail    string    `json:"detail" gorm:"type:text"`
	CreatedAt time.Time `json:"created_at"`
}

func (CronRunHistory) TableName() string { return "cron_run_history" }
