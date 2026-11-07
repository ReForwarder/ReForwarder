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

	"github.com/goposta/posta/internal/services/inbound"
	"github.com/hibiken/asynq"
)

// InboundForwardQueueJob drives the inbound forwarding executor (OB-2,
// ADR-002 decision 2): every tick it re-queues stale running rows (a crashed
// executor's orphans) and claims due inbound_forward_tasks, re-sending the
// stored originals as system_forward copies. The task-level retry/backoff
// story lives on the rows themselves (next_attempt_at + attempts), so a
// one-minute cadence bounds forwarding latency without extra task machinery.
type InboundForwardQueueJob struct {
	forwarder *inbound.Forwarder
}

func NewInboundForwardQueueJob(forwarder *inbound.Forwarder) *InboundForwardQueueJob {
	return &InboundForwardQueueJob{forwarder: forwarder}
}

func (j *InboundForwardQueueJob) Name() string     { return "inbound-forward-queue" }
func (j *InboundForwardQueueJob) Schedule() string { return "*/1 * * * *" }

func (j *InboundForwardQueueJob) Run(ctx context.Context, _ *asynq.Client) error {
	if j.forwarder == nil {
		return nil
	}
	_, err := j.forwarder.RunOnce(ctx)
	return err
}
