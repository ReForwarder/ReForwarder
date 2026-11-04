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

package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/queues"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// SendTaskInspector is the slice of *asynq.Inspector the reconcile needs: look
// up one task by its deterministic ID. *asynq.Inspector satisfies it directly.
type SendTaskInspector interface {
	GetTaskInfo(queue, id string) (*asynq.TaskInfo, error)
}

// SendEnqueuer re-enqueues an email send task. *worker.Producer satisfies it.
type SendEnqueuer interface {
	EnqueueEmailSend(emailID uint, queue string) error
}

// QueueReconcileJob re-enqueues emails stuck in status=queued whose asynq task
// was lost (Redis flush, crash between DB update and enqueue, manually deleted
// task). The retry worker only covers status=failed and the user retry endpoint
// only accepts failed, so a queued email with no task would sit forever.
//
// Detection leans on the deterministic task ID (worker.EmailSendTaskID): a task
// in any live state (pending, active, scheduled, retry) means the email is
// merely waiting on a backlog and is left alone. Only ErrTaskNotFound (or a
// dead-lettered task holding the ID) triggers a re-enqueue.
//
// Guard rails:
//   - only rows with status='queued' AND updated_at older than stuckFor are
//     considered, so a message being processed right now never matches;
//   - the claim is a CAS (UPDATE ... WHERE status='queued' AND updated_at <
//     cutoff), which both re-arms the window and makes two concurrent
//     reconcilers (server + replica) enqueue at most once;
//   - emails that belong to a campaign are skipped: paused campaigns release
//     messages to queued with no task by design (Resume re-dispatches them),
//     and the campaign-restart sweep owns stuck campaigns.
type QueueReconcileJob struct {
	db        *gorm.DB
	producer  SendEnqueuer
	inspector SendTaskInspector
	stuckFor  time.Duration
	batchSize int
}

func NewQueueReconcileJob(db *gorm.DB, producer SendEnqueuer, inspector SendTaskInspector) *QueueReconcileJob {
	return &QueueReconcileJob{
		db:        db,
		producer:  producer,
		inspector: inspector,
		stuckFor:  15 * time.Minute,
		batchSize: 100,
	}
}

func (j *QueueReconcileJob) Name() string     { return "queue-reconcile" }
func (j *QueueReconcileJob) Schedule() string { return "*/5 * * * *" }

func (j *QueueReconcileJob) Run(_ context.Context, _ *asynq.Client) error {
	if j.producer == nil || j.inspector == nil {
		return nil
	}
	cutoff := time.Now().Add(-j.stuckFor)

	var stuck []models.Email
	if err := j.db.
		Where("status = ? AND updated_at < ?", models.EmailStatusQueued, cutoff).
		Order("updated_at ASC").
		Limit(j.batchSize).
		Find(&stuck).Error; err != nil {
		return err
	}

	for i := range stuck {
		em := &stuck[i]
		// Campaign mail: paused campaigns release messages to queued without a
		// task on purpose; the campaign machinery owns this lifecycle.
		var campaignLinks int64
		if err := j.db.Model(&models.CampaignMessage{}).
			Where("email_id = ?", em.ID).Count(&campaignLinks).Error; err != nil {
			logger.Warn("queue-reconcile: failed to check campaign link", "email_id", em.ID, "error", err)
			continue
		}
		if campaignLinks > 0 {
			continue
		}

		if info, err := j.inspector.GetTaskInfo(queues.Transactional, queues.EmailSendTaskID(em.ID)); err == nil {
			// A dead-lettered task holding the ID would strand the queued email
			// forever (the producer clears it on re-enqueue); live states wait.
			if info.State != asynq.TaskStateArchived && info.State != asynq.TaskStateCompleted {
				continue
			}
		} else if !errors.Is(err, asynq.ErrTaskNotFound) {
			// Redis unavailable: skip this tick rather than mass re-enqueue.
			logger.Warn("queue-reconcile: task lookup failed", "email_id", em.ID, "error", err)
			continue
		}

		// CAS the claim: only a row still queued and still stale wins. The
		// updated_at bump re-arms the window so a failed enqueue retries on a
		// later tick instead of hot-looping.
		claim := j.db.Model(&models.Email{}).
			Where("id = ? AND status = ? AND updated_at < ?", em.ID, models.EmailStatusQueued, cutoff).
			Update("updated_at", time.Now())
		if claim.Error != nil {
			logger.Warn("queue-reconcile: claim failed", "email_id", em.ID, "error", claim.Error)
			continue
		}
		if claim.RowsAffected == 0 {
			continue // another reconciler (or the worker) got there first
		}
		if err := j.producer.EnqueueEmailSend(em.ID, ""); err != nil {
			logger.Error("queue-reconcile: failed to re-enqueue lost email", "email_id", em.ID, "error", err)
			continue
		}
		logger.Info("queue-reconcile: re-enqueued lost email", "email_id", em.ID)
	}
	return nil
}
