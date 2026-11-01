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
	"time"

	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
)

type CampaignBatchEnqueuer interface {
	EnqueueCampaignBatch(campaignID uint, delay time.Duration) error
}

type CampaignRestartJob struct {
	campaignRepo *repositories.CampaignRepository
	messageRepo  *repositories.CampaignMessageRepository
	producer     CampaignBatchEnqueuer
}

func NewCampaignRestartJob(
	campaignRepo *repositories.CampaignRepository,
	messageRepo *repositories.CampaignMessageRepository,
	producer CampaignBatchEnqueuer,
) *CampaignRestartJob {
	return &CampaignRestartJob{
		campaignRepo: campaignRepo,
		messageRepo:  messageRepo,
		producer:     producer,
	}
}

func (j *CampaignRestartJob) Name() string     { return "campaign-restart" }
func (j *CampaignRestartJob) Schedule() string { return "*/5 * * * *" }

const stuckFor = 10 * time.Minute

func (j *CampaignRestartJob) Run(_ context.Context, _ *asynq.Client) error {
	if j.producer == nil {
		return nil
	}
	stuck, err := j.campaignRepo.FindStuckSending(stuckFor)
	if err != nil {
		return err
	}
	for _, c := range stuck {
		pending, err := j.messageRepo.CountPending(c.ID)
		if err != nil {
			logger.Warn("campaign-restart: failed to count pending", "campaign_id", c.ID, "error", err)
			continue
		}
		if pending == 0 {
			continue
		}
		if err := j.producer.EnqueueCampaignBatch(c.ID, 0); err != nil {
			logger.Warn("campaign-restart: failed to re-enqueue batch", "campaign_id", c.ID, "error", err)
			continue
		}
		logger.Info("campaign-restart: re-enqueued stuck campaign", "campaign_id", c.ID, "pending", pending)
	}
	return nil
}
