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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobs

import (
	"context"
	"errors"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
)

// CampaignStartEnqueuer abstracts the producer so the scheduler can be tested
// without Redis (same arrangement as CampaignBatchEnqueuer).
type CampaignStartEnqueuer interface {
	EnqueueCampaignStart(campaignID uint) error
}

// CampaignSchedulerJob fires campaigns that reached their scheduled send time.
// Send puts a campaign with a future scheduled_at into status=scheduled; this
// sweep (every minute — scheduled sends should not wait longer than a tick)
// claims the due ones and enqueues the worker fan-out task.
//
// Claiming: each candidate is moved scheduled -> sending via the atomic
// TransitionStatus (UPDATE ... WHERE id = ? AND status IN (...), RowsAffected
// == 0 -> ErrStatusConflict). A lost race is skipped — another worker claimed
// the campaign. Pause/cancel/complete are excluded naturally: the claim only
// matches status=scheduled, and a campaign cancelled while still scheduled is
// no longer in that status.
//
// Pause/cancel during the sending phase is enforced downstream by the worker:
// HandleCampaignStart refuses to fan out unless the row still reads
// status=sending, so a cancel landing after the claim still stops the send.
//
// Restart-safety is inherent: the state lives in the database (status +
// scheduled_at) and the every-minute poll re-finds anything left in
// scheduled, so a crashed or restarted server loses nothing; an enqueue
// failure rolls the status back to scheduled so the next tick retries (the
// same rollback pattern as the Send handler). The producer's
// campaign:start:<id> TaskID collapses any double-fire.
type CampaignSchedulerJob struct {
	campaignRepo *repositories.CampaignRepository
	producer     CampaignStartEnqueuer
}

func NewCampaignSchedulerJob(
	campaignRepo *repositories.CampaignRepository,
	producer CampaignStartEnqueuer,
) *CampaignSchedulerJob {
	return &CampaignSchedulerJob{
		campaignRepo: campaignRepo,
		producer:     producer,
	}
}

func (j *CampaignSchedulerJob) Name() string     { return "campaign-scheduler" }
func (j *CampaignSchedulerJob) Schedule() string { return "* * * * *" }

func (j *CampaignSchedulerJob) Run(_ context.Context, _ *asynq.Client) error {
	if j.producer == nil {
		return nil
	}
	due, err := j.campaignRepo.FindScheduledReady()
	if err != nil {
		return err
	}
	for _, c := range due {
		// Atomically claim the scheduled -> sending transition. Losing the race
		// (another worker got it, or the campaign was cancelled meanwhile)
		// returns ErrStatusConflict and must not enqueue.
		if err := j.campaignRepo.TransitionStatus(
			c.ID,
			[]models.CampaignStatus{models.CampaignStatusScheduled},
			models.CampaignStatusSending,
		); err != nil {
			if !errors.Is(err, repositories.ErrStatusConflict) {
				logger.Warn("campaign-scheduler: failed to claim campaign", "campaign_id", c.ID, "error", err)
			}
			continue
		}

		if err := j.producer.EnqueueCampaignStart(c.ID); err != nil {
			// Roll back so the next tick retries instead of leaving the
			// campaign stuck in sending with nothing enqueued.
			if rbErr := j.campaignRepo.TransitionStatus(
				c.ID,
				[]models.CampaignStatus{models.CampaignStatusSending},
				models.CampaignStatusScheduled,
			); rbErr != nil {
				logger.Error("campaign-scheduler: failed to roll back claim", "campaign_id", c.ID, "error", rbErr)
			}
			logger.Warn("campaign-scheduler: failed to enqueue campaign start", "campaign_id", c.ID, "error", err)
			continue
		}
		logger.Info("campaign-scheduler: dispatched scheduled campaign", "campaign_id", c.ID)
	}
	return nil
}
