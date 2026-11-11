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

	"github.com/goposta/posta/internal/services/meticalert"
	"github.com/hibiken/asynq"
)

// MetricAlertJob (SF-96, OB-6 W5) evaluates the platform metric-alert rules
// every minute. The cadence is the alert granularity: a rule with
// duration_seconds=300 fires at most one minute after its breach has been
// held that long. Without rules configured the evaluation is a cheap no-op
// (one settings read, one gather), so the job is registered unconditionally —
// the same posture as the bounce-alert job.
type MetricAlertJob struct {
	engine *meticalert.Engine
}

func NewMetricAlertJob(engine *meticalert.Engine) *MetricAlertJob {
	return &MetricAlertJob{engine: engine}
}

func (j *MetricAlertJob) Name() string     { return "metric-alert" }
func (j *MetricAlertJob) Schedule() string { return "* * * * *" } // every minute

func (j *MetricAlertJob) Run(ctx context.Context, _ *asynq.Client) error {
	if j.engine == nil {
		return nil
	}
	return j.engine.Evaluate(ctx)
}
