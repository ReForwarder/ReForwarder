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

package worker

import (
	"encoding/json"
	"time"

	"github.com/goposta/posta/internal/services/queue"
	"github.com/hibiken/asynq"
)

// ScheduleRetryDelay returns the asynq RetryDelayFunc for the email:send task
// type that consults the SF-08 per-recipient schedule ledger: a failing send
// task's next attempt is scheduled (ProcessAt semantics) at the earliest
// retry time the recipients' interval lists produced, instead of the default
// backoff. Tasks without schedule rows keep the default policy.
//
// Note on the interplay with the R-26 claim semantics: after a transient
// failure the email row is status=failed and a redelivered task no-ops at the
// claim (ClaimForProcessing only takes queued/processing rows) — the retry
// sweep (R-27, schedule-aware since SF-08) is the delivery carrier. Shaping
// this delay keeps the task-level timing coherent with the schedule ledger
// regardless.
func ScheduleRetryDelayFunc(m *queue.Manager) asynq.RetryDelayFunc {
	return func(n int, e error, t *asynq.Task) time.Duration {
		if m == nil || t == nil || t.Type() != TypeEmailSend {
			return asynq.DefaultRetryDelayFunc(n, e, t)
		}
		var payload EmailSendPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return asynq.DefaultRetryDelayFunc(n, e, t)
		}
		if d, ok := m.RetryDelay(payload.EmailID, time.Now()); ok {
			if d < 0 {
				return 0
			}
			return d
		}
		return asynq.DefaultRetryDelayFunc(n, e, t)
	}
}
