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

	"github.com/goposta/posta/internal/services/spamfilter"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
)

// RuleFeedJob is the SF-42 refresh cron (the ADR-006 amendment): fetches the
// external rule feed on schedule, HMAC-verifies the document, and atomically
// swaps the scorer's in-memory snapshot. Fail-open by contract: a fetch,
// signature, or parse failure keeps the PREVIOUS snapshot (stale rules
// continue, never an empty gate) — the job just logs. Disabled when the feed
// URL is unset (the caller does not register it).
type RuleFeedJob struct {
	feed     *spamfilter.RuleFeed
	schedule string
}

// NewRuleFeedJob builds the refresh job over the loader seam.
func NewRuleFeedJob(feed *spamfilter.RuleFeed) *RuleFeedJob {
	return &RuleFeedJob{feed: feed, schedule: "7 * * * *"} // hourly at :07
}

func (j *RuleFeedJob) Name() string { return "rule_feed_refresh" }

func (j *RuleFeedJob) Schedule() string { return j.schedule }

func (j *RuleFeedJob) Run(ctx context.Context, _ *asynq.Client) error {
	n, err := j.feed.Refresh(ctx)
	if err != nil {
		// Fail-open: the previous snapshot stands (stale rules continue).
		logger.Warn("rule feed refresh failed; keeping the previous snapshot", "error", err)
		return nil
	}
	logger.Info("rule feed refreshed", "rules", n)
	return nil
}
