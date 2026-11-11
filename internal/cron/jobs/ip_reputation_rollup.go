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
	"time"

	"github.com/goposta/posta/internal/services/reputation"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// IPReputationRollupJob maintains ip_reputation_rollups (OB-6 W3, ADR-007
// decision 7): each run aggregates the previous UTC day and the day before it
// — re-running a day is an idempotent upsert, so a missed tick self-heals on
// the next one without double-counting. The R-07 report additionally
// refreshes the current day on read, so the surface is live for today while
// this job owns the history.
type IPReputationRollupJob struct {
	db  *gorm.DB
	svc *reputation.Service
}

func NewIPReputationRollupJob(db *gorm.DB) *IPReputationRollupJob {
	return &IPReputationRollupJob{db: db, svc: reputation.NewService(db)}
}

func (j *IPReputationRollupJob) Name() string { return "ip_reputation_rollup" }

func (j *IPReputationRollupJob) Schedule() string { return "15 2 * * *" } // daily at 02:15 UTC

func (j *IPReputationRollupJob) Run(_ context.Context, _ *asynq.Client) error {
	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1)
	dayBefore := now.AddDate(0, 0, -2)

	n1, err := j.svc.RollupDay(yesterday)
	if err != nil {
		logger.Error("ip-reputation-rollup: failed to roll up yesterday", "error", err)
		return err
	}
	n2, err := j.svc.RollupDay(dayBefore)
	if err != nil {
		logger.Error("ip-reputation-rollup: failed to roll up the day before", "error", err)
		return err
	}
	logger.Info("ip-reputation-rollup: refreshed rollups",
		"yesterday_rows", n1, "day_before_rows", n2)
	return nil
}
