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
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/queue"
	"github.com/goposta/posta/internal/storage/repositories"
)

// SF-08 claim-time tests: the send handler's consult of the per-recipient
// schedule ledger. They run the real handler and the real queue.Manager
// against the atomic-claim fake driver (extended with the schedule table),
// pinning that a schedule-held task is released WITHOUT claiming or sending,
// an exhausted schedule finalizes the message through the existing terminal
// path, and campaign mail bypasses the consult (the campaign machinery owns
// its lifecycle — the schedule-aware reconcile skips it too).

func seedScheduleRow(t *testing.T, s *claimState, id int64, emailID int64, status models.EmailQueueScheduleStatus, nextRetry *time.Time, attemptIndex int64) {
	t.Helper()
	vals := map[string]driver.Value{
		"id": id, "email_id": emailID,
		"recipient": "rcpt@example.com", "user_id": int64(1),
		"retry_intervals": "[5,30,120]", "notify_addresses": "{notify@example.com}",
		"attempt_index": attemptIndex, "status": status,
		"last_error": "", "created_at": time.Now(), "updated_at": time.Now(),
	}
	if nextRetry != nil {
		vals["next_retry_at"] = *nextRetry
	}
	s.tables["email_queue_schedules"] = append(s.tables["email_queue_schedules"], &claimRow{cols: vals})
}

// TestQueueScheduleHoldReleasesTaskAtClaim pins the ProcessIn/ProcessAt
// consult: a task arriving ahead of the recipient schedule is released with
// success — nothing is claimed, nothing is sent, and the row stays queued for
// the schedule-aware reconcile to re-enqueue at the due time.
func TestQueueScheduleHoldReleasesTaskAtClaim(t *testing.T) {
	state := newClaimState()
	seedClaimEmail(t, state, 71, models.EmailStatusQueued, 0)
	future := time.Now().Add(30 * time.Minute)
	seedScheduleRow(t, state, 711, 71, models.ScheduleStatusActive, &future, 1)

	gdb := claimGorm(t, state)
	mgr := queue.NewManager(gdb, queue.Deps{})
	handler := NewEmailSendHandler(
		repositories.NewEmailRepository(gdb),
		repositories.NewSMTPRepository(gdb),
		nil, nil, nil,
		nil, // nil dispatcher: fake DB is not thread-safe under -race.
	)
	handler.SetQueueManager(mgr)

	payload, err := NewEmailSendTask(71)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if err := handler.ProcessTask(context.Background(), payload); err != nil {
		t.Fatalf("ProcessTask must succeed while the schedule holds: %v", err)
	}

	if got := state.statusOf("emails", 71); got != string(models.EmailStatusQueued) {
		t.Fatalf("status = %v, want queued (claim never ran)", got)
	}
	if n := state.countSelects("smtp_servers"); n != 0 {
		t.Fatalf("smtp_servers selects = %d, want 0 (send path never entered)", n)
	}
}

// TestQueueScheduleExhaustedFinalizesAtClaim pins the exhausted branch: a
// message whose recipients' interval lists all ran out is finalized through
// markFailed — failed with the schedule reason, no send attempt.
func TestQueueScheduleExhaustedFinalizesAtClaim(t *testing.T) {
	state := newClaimState()
	seedClaimEmail(t, state, 72, models.EmailStatusQueued, 0)
	seedScheduleRow(t, state, 721, 72, models.ScheduleStatusExhausted, nil, 3)

	gdb := claimGorm(t, state)
	mgr := queue.NewManager(gdb, queue.Deps{})
	handler := NewEmailSendHandler(
		repositories.NewEmailRepository(gdb),
		repositories.NewSMTPRepository(gdb),
		nil, nil, nil,
		nil,
	)
	handler.SetQueueManager(mgr)

	payload, err := NewEmailSendTask(72)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if err := handler.ProcessTask(context.Background(), payload); err != nil {
		t.Fatalf("ProcessTask must succeed on an exhausted schedule: %v", err)
	}

	if got := state.statusOf("emails", 72); got != string(models.EmailStatusFailed) {
		t.Fatalf("status = %v, want failed", got)
	}
	if got := state.stringOf("emails", 72, "error_message"); got != "per-recipient retry schedules exhausted" {
		t.Fatalf("error_message = %q, want the schedule-exhausted reason", got)
	}
	if n := state.countSelects("smtp_servers"); n != 0 {
		t.Fatalf("smtp_servers selects = %d, want 0 (send path never entered)", n)
	}
}

// TestQueueScheduleCampaignExemptProceeds pins the campaign exemption: a
// campaign-linked message whose schedule would hold is delivered through the
// ordinary path — the campaign machinery owns its lifecycle, and the
// schedule-aware queue-reconcile skips campaign mail (so a released task
// there could strand forever).
func TestQueueScheduleCampaignExemptProceeds(t *testing.T) {
	state := newClaimState()
	seedClaimEmail(t, state, 73, models.EmailStatusQueued, 0)
	future := time.Now().Add(30 * time.Minute)
	seedScheduleRow(t, state, 731, 73, models.ScheduleStatusActive, &future, 1)
	state.insert("campaign_messages", map[string]any{
		"id": int64(7301), "campaign_id": int64(73), "email_id": int64(73),
		"status": models.CampaignMsgQueued,
	})
	state.insert("campaigns", map[string]any{
		"id": int64(73), "status": "sending", "name": "exempt test",
	})

	gdb := claimGorm(t, state)
	mgr := queue.NewManager(gdb, queue.Deps{})
	handler := NewEmailSendHandler(
		repositories.NewEmailRepository(gdb),
		repositories.NewSMTPRepository(gdb),
		nil, nil, nil,
		nil,
	)
	handler.SetQueueManager(mgr)
	handler.SetCampaignMessageRepo(repositories.NewCampaignMessageRepository(gdb))
	handler.SetCampaignRepo(repositories.NewCampaignRepository(gdb))

	payload, err := NewEmailSendTask(73)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if err := handler.ProcessTask(context.Background(), payload); err != nil {
		t.Fatalf("ProcessTask must succeed for campaign mail: %v", err)
	}

	// The consult was bypassed: the send path ran its server lookup and the
	// ordinary "no SMTP server" terminal failure is recorded.
	if n := state.countSelects("smtp_servers"); n != 1 {
		t.Fatalf("smtp_servers selects = %d, want 1 (send path entered)", n)
	}
	if got := state.stringOf("emails", 73, "error_message"); got == "per-recipient retry schedules exhausted" {
		t.Fatal("campaign mail was finalized by the schedule consult, want the ordinary path")
	}
}

// TestScheduleRetryDelayFunc pins the asynq retry-delay seam: schedule-held
// messages wait until the due time; messages without schedule rows keep the
// default backoff policy.
func TestScheduleRetryDelayFunc(t *testing.T) {
	state := newClaimState()
	seedClaimEmail(t, state, 81, models.EmailStatusQueued, 0)
	future := time.Now().Add(45 * time.Minute)
	seedScheduleRow(t, state, 811, 81, models.ScheduleStatusActive, &future, 1)

	gdb := claimGorm(t, state)
	mgr := queue.NewManager(gdb, queue.Deps{})
	delayFunc := ScheduleRetryDelayFunc(mgr)

	task, err := NewEmailSendTask(81)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	d := delayFunc(1, context.DeadlineExceeded, task)
	if d <= 40*time.Minute || d > 45*time.Minute {
		t.Fatalf("delay = %s, want the remaining hold time (~45m)", d)
	}

	// A task whose email has no schedule rows keeps the default policy. The
	// default backoff carries a random component (15s + rand^4*... for n=1:
	// 15s..73s), so the assertion is a range, not an equality.
	bare, err := NewEmailSendTask(82)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	d = delayFunc(1, context.DeadlineExceeded, bare)
	if d < 15*time.Second || d > 73*time.Second {
		t.Fatalf("delay = %s, want the default backoff range [15s, 73s] for n=1", d)
	}

	// A nil manager degrades to the default policy entirely.
	nilDelay := ScheduleRetryDelayFunc(nil)
	if d := nilDelay(1, context.DeadlineExceeded, task); d < 15*time.Second || d > 73*time.Second {
		t.Fatalf("nil-manager delay = %s, want the default backoff range [15s, 73s] for n=1", d)
	}
}
