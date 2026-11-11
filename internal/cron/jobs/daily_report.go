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
	"encoding/json"
	"fmt"
	"time"

	"github.com/goposta/posta/internal/cron"
	"github.com/goposta/posta/internal/queues"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
)

const TypeDailyReport = "cron:daily-report"

// DailyReportPayload is the Asynq task payload for a per-workspace daily report.
type DailyReportPayload struct {
	WorkspaceID uint `json:"workspace_id"`
}

// DailyReportJob enqueues a report task for each workspace. The processed report
// is delivered to the workspace's owners and admins.
type DailyReportJob struct {
	workspaceRepo *repositories.WorkspaceRepository
	// claims, when set, guards one report enqueue per workspace per day
	// (GAP-03): the leader lease keeps one replica per tick, but a crash
	// between lease acquire and enqueue — or a failover edge — could
	// double-enqueue a customer-facing digest.
	claims cron.ClaimStore
}

// dailyReportTask implements cron.Job for enqueueing a single workspace's report.
type dailyReportTask struct {
	workspaceID uint
}

func (t *dailyReportTask) Type() string { return TypeDailyReport }
func (t *dailyReportTask) Payload() any { return DailyReportPayload{WorkspaceID: t.workspaceID} }

func NewDailyReportJob(workspaceRepo *repositories.WorkspaceRepository) *DailyReportJob {
	return &DailyReportJob{workspaceRepo: workspaceRepo}
}

func (j *DailyReportJob) Name() string     { return "daily-report" }
func (j *DailyReportJob) Schedule() string { return "0 7 * * *" } // daily at 07:00 UTC

// SetClaimStore installs the per-recipient dedup guard (GAP-03). Nil keeps
// single-replica behavior unchanged.
func (j *DailyReportJob) SetClaimStore(cs cron.ClaimStore) { j.claims = cs }

func (j *DailyReportJob) Run(ctx context.Context, client *asynq.Client) error {
	workspaces, err := j.workspaceRepo.FindAll()
	if err != nil {
		logger.Error("daily report: failed to find workspaces", "error", err)
		return err
	}

	today := time.Now().UTC().Format("20060102")
	enqueued := 0
	for _, ws := range workspaces {
		// One report enqueue per workspace per day (GAP-03); cleared when the
		// enqueue fails so the next leader can retry. Fails open on a Redis
		// outage — a possible duplicate beats a missing report.
		dedupKey := fmt.Sprintf("cron:dedup:daily-report:%d:%s", ws.ID, today)
		if j.claims != nil && !j.claims.ClaimOnce(ctx, dedupKey, 48*time.Hour) {
			continue
		}
		if err := cron.EnqueueJob(client, &dailyReportTask{workspaceID: ws.ID}, asynq.Queue(queues.Low)); err != nil {
			if j.claims != nil {
				j.claims.ClearClaim(ctx, dedupKey)
			}
			logger.Error("daily report: failed to enqueue", "workspace_id", ws.ID, "error", err)
			continue
		}
		enqueued++
	}

	logger.Info("daily report: enqueued tasks", "count", enqueued)
	return nil
}

// NewDailyReportTask creates an Asynq task for processing a daily report.
func NewDailyReportTask(workspaceID uint, opts ...asynq.Option) (*asynq.Task, error) {
	payload, err := json.Marshal(DailyReportPayload{WorkspaceID: workspaceID})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeDailyReport, payload, opts...), nil
}
