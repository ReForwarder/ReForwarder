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

package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/queue"
	"github.com/hibiken/asynq"
)

// SF-08: the queue-reconcile's schedule-aware behavior, on the same fake
// driver as queue_reconcile_test.go. The consult is the real queue.Manager
// over the fake, so the SQL shapes the reconcile and the manager issue are
// both exercised: a schedule-held lost task re-enqueues at exactly the due
// time (ProcessAt), an exhausted schedule stays lost, and schedule-less rows
// keep the immediate re-enqueue.

func seedQrcScheduleRow(t *testing.T, s *qrcState, id uint, emailID uint, status models.EmailQueueScheduleStatus, nextRetry *time.Time) {
	t.Helper()
	vals := map[string]any{
		"id": id, "email_id": emailID, "status": status, "attempt_index": 1,
	}
	if nextRetry != nil {
		vals["next_retry_at"] = *nextRetry
	}
	s.insert("email_queue_schedules", vals)
}

func TestQueueReconcileSchedulesHeldEmailAtDueTime(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	inspector := &qrcInspector{tasks: map[string]asynq.TaskState{}}
	seedQrcEmail(t, state, 21, models.EmailStatusQueued, qrcNow.Add(-time.Hour))
	due := qrcNow.Add(20 * time.Minute)
	seedQrcScheduleRow(t, state, 211, 21, models.ScheduleStatusActive, &due)

	j := qrcJob(t, state, producer, inspector)
	j.SetScheduler(queue.NewManager(qrcGorm(t, state), queue.Deps{}))
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(producer.enqueued) != 1 || producer.enqueued[0] != 21 {
		t.Fatalf("enqueued = %v, want exactly email 21", producer.enqueued)
	}
	if len(producer.scheduledAt) != 1 || !producer.scheduledAt[0].Equal(due) {
		t.Fatalf("scheduledAt = %v, want exactly the due time %v", producer.scheduledAt, due)
	}
}

func TestQueueReconcileSkipsExhaustedSchedule(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	inspector := &qrcInspector{tasks: map[string]asynq.TaskState{}}
	seedQrcEmail(t, state, 22, models.EmailStatusQueued, qrcNow.Add(-time.Hour))
	seedQrcScheduleRow(t, state, 221, 22, models.ScheduleStatusExhausted, nil)

	j := qrcJob(t, state, producer, inspector)
	j.SetScheduler(queue.NewManager(qrcGorm(t, state), queue.Deps{}))
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := producer.enqueued; len(got) != 0 {
		t.Fatalf("enqueued = %v, want nothing (exhausted schedule stays lost)", got)
	}
}

func TestQueueReconcileDueScheduleEnqueuesImmediately(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	inspector := &qrcInspector{tasks: map[string]asynq.TaskState{}}
	seedQrcEmail(t, state, 23, models.EmailStatusQueued, qrcNow.Add(-time.Hour))
	past := qrcNow.Add(-5 * time.Minute)
	seedQrcScheduleRow(t, state, 231, 23, models.ScheduleStatusActive, &past)

	j := qrcJob(t, state, producer, inspector)
	j.SetScheduler(queue.NewManager(qrcGorm(t, state), queue.Deps{}))
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(producer.enqueued) != 1 || producer.enqueued[0] != 23 {
		t.Fatalf("enqueued = %v, want exactly email 23", producer.enqueued)
	}
	if len(producer.scheduledAt) != 0 {
		t.Fatalf("scheduledAt = %v, want empty (due rows re-enqueue immediately)", producer.scheduledAt)
	}
}

func TestQueueReconcileNoScheduleKeepsLegacyEnqueue(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	inspector := &qrcInspector{tasks: map[string]asynq.TaskState{}}
	seedQrcEmail(t, state, 24, models.EmailStatusQueued, qrcNow.Add(-time.Hour))

	j := qrcJob(t, state, producer, inspector)
	j.SetScheduler(queue.NewManager(qrcGorm(t, state), queue.Deps{}))
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(producer.enqueued) != 1 || producer.enqueued[0] != 24 {
		t.Fatalf("enqueued = %v, want exactly email 24", producer.enqueued)
	}
	if len(producer.scheduledAt) != 0 {
		t.Fatalf("scheduledAt = %v, want empty (schedule-less rows keep the legacy behavior)", producer.scheduledAt)
	}
}
