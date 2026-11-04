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
	"errors"
	"fmt"
	"time"

	"github.com/goposta/posta/internal/queues"
	"github.com/hibiken/asynq"
)

// Producer enqueues email tasks into Redis via Asynq.
type Producer struct {
	client     *asynq.Client
	inspector  *asynq.Inspector
	maxRetries int
}

func NewProducer(redisOpt asynq.RedisClientOpt, maxRetries int) *Producer {
	client := asynq.NewClient(redisOpt)
	return &Producer{
		client:     client,
		inspector:  asynq.NewInspector(redisOpt),
		maxRetries: maxRetries,
	}
}

// EmailSendTaskID is the deterministic asynq task ID of an email:send task
// (queues.EmailSendTaskID). See the queues package for why the ID is fixed.
func EmailSendTaskID(emailID uint) string {
	return queues.EmailSendTaskID(emailID)
}

// EnqueueEmailSend enqueues an email for background delivery.
func (p *Producer) EnqueueEmailSend(emailID uint, queue string) error {
	if queue == "" {
		queue = QueueTransactional
	}
	task, err := NewEmailSendTask(emailID,
		asynq.Queue(queue),
		asynq.MaxRetry(p.maxRetries),
		asynq.TaskID(queues.EmailSendTaskID(emailID)),
	)
	if err != nil {
		return fmt.Errorf("failed to create task: %w", err)
	}
	if _, err := p.client.Enqueue(task); err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return p.resolveSendTaskConflict(emailID, queue)
		}
		return fmt.Errorf("failed to enqueue task: %w", err)
	}
	return nil
}

// resolveSendTaskConflict handles an enqueue refused because the email's
// deterministic task ID is already held. A task in a live state (pending,
// active, scheduled, retry) means the email is already queued — dedupe success.
// A dead lettered (archived) task holds the ID forever, which would strand a
// retry forever, so it is deleted and the enqueue retried.
func (p *Producer) resolveSendTaskConflict(emailID uint, queue string) error {
	if p.inspector == nil {
		return fmt.Errorf("task %s already exists and no inspector is available", EmailSendTaskID(emailID))
	}
	id := queues.EmailSendTaskID(emailID)
	info, err := p.inspector.GetTaskInfo(queue, id)
	if err != nil {
		// Cannot tell whether the holder is live; treat as already-queued.
		// The queue-reconcile cron re-enqueues if it is actually lost.
		return nil
	}
	if info.State != asynq.TaskStateArchived && info.State != asynq.TaskStateCompleted {
		return nil // a live task already covers this email
	}
	if err := p.inspector.DeleteTask(queue, id); err != nil {
		return fmt.Errorf("failed to clear dead-lettered task %s before re-enqueue: %w", id, err)
	}
	task, err := NewEmailSendTask(emailID, asynq.Queue(queue), asynq.MaxRetry(p.maxRetries))
	if err != nil {
		return fmt.Errorf("failed to create task: %w", err)
	}
	if _, err := p.client.Enqueue(task); err != nil && !errors.Is(err, asynq.ErrTaskIDConflict) {
		return fmt.Errorf("failed to enqueue task: %w", err)
	}
	return nil
}

// EnqueueEmailSendAt enqueues an email for delivery at the specified time.
func (p *Producer) EnqueueEmailSendAt(emailID uint, queue string, sendAt time.Time) error {
	if queue == "" {
		queue = QueueTransactional
	}
	delay := time.Until(sendAt)
	if delay < 0 {
		delay = 0
	}
	task, err := NewEmailSendTask(emailID,
		asynq.Queue(queue),
		asynq.MaxRetry(p.maxRetries),
		asynq.ProcessIn(delay),
		asynq.TaskID(queues.EmailSendTaskID(emailID)),
	)
	if err != nil {
		return fmt.Errorf("failed to create task: %w", err)
	}
	if _, err := p.client.Enqueue(task); err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return p.resolveSendTaskConflict(emailID, queue)
		}
		return fmt.Errorf("failed to enqueue task: %w", err)
	}
	return nil
}

// EnqueueCampaignStart enqueues a campaign start task (fan-out to subscribers).
// The TaskID is derived from the campaign ID so double-clicks and retries
// collapse into a single Redis entry instead of fanning out twice.
func (p *Producer) EnqueueCampaignStart(campaignID uint) error {
	task, err := NewCampaignStartTask(
		campaignID,
		asynq.Queue(QueueBulk),
		asynq.MaxRetry(3),
		asynq.TaskID(fmt.Sprintf("campaign:start:%d", campaignID)),
	)
	if err != nil {
		return fmt.Errorf("failed to create campaign start task: %w", err)
	}
	if _, err := p.client.Enqueue(task); err != nil {
		// asynq.ErrTaskIDConflict means the task is already pending: treat as success.
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("failed to enqueue campaign start: %w", err)
	}
	return nil
}

// EnqueueCampaignBatch enqueues a campaign batch processing task. A per-run
// random suffix keeps successive batches distinct while still letting us dedupe
// the *first* kick-off from Send (callers that want dedupe pass delay=0 after
// transitioning status atomically).
func (p *Producer) EnqueueCampaignBatch(campaignID uint, delay time.Duration) error {
	opts := []asynq.Option{asynq.Queue(QueueBulk), asynq.MaxRetry(3)}
	if delay > 0 {
		opts = append(opts, asynq.ProcessIn(delay))
	}
	task, err := NewCampaignBatchTask(campaignID, opts...)
	if err != nil {
		return fmt.Errorf("failed to create campaign batch task: %w", err)
	}
	_, err = p.client.Enqueue(task)
	return err
}

// EnqueueInboundProcess enqueues an inbound-process task that dispatches the
// email.inbound webhook for a received message.
func (p *Producer) EnqueueInboundProcess(inboundEmailID uint) error {
	task, err := NewInboundProcessTask(inboundEmailID,
		asynq.Queue(QueueTransactional),
		asynq.MaxRetry(p.maxRetries),
	)
	if err != nil {
		return fmt.Errorf("failed to create inbound task: %w", err)
	}
	if _, err := p.client.Enqueue(task); err != nil {
		return fmt.Errorf("failed to enqueue inbound task: %w", err)
	}
	return nil
}

// EnqueueInboundParse enqueues a parse task for a raw inbound message that
// was just persisted by the SMTP receiver. The parse step is intentionally
// separate from EnqueueInboundProcess so a permanent parse failure does not
// block the SMTP 250 OK reply.
func (p *Producer) EnqueueInboundParse(inboundEmailID uint) error {
	task, err := NewInboundParseTask(inboundEmailID,
		asynq.Queue(QueueTransactional),
		asynq.MaxRetry(p.maxRetries),
	)
	if err != nil {
		return fmt.Errorf("failed to create inbound parse task: %w", err)
	}
	if _, err := p.client.Enqueue(task); err != nil {
		return fmt.Errorf("failed to enqueue inbound parse task: %w", err)
	}
	return nil
}

func (p *Producer) EnqueueMessageProcess(messageID uint) error {
	task, err := NewMessageProcessTask(messageID,
		asynq.Queue(QueueTransactional),
		asynq.MaxRetry(p.maxRetries),
		asynq.TaskID(fmt.Sprintf("message:process:%d", messageID)),
	)
	if err != nil {
		return fmt.Errorf("failed to create message task: %w", err)
	}
	if _, err := p.client.Enqueue(task); err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("failed to enqueue message task: %w", err)
	}
	return nil
}

// EnqueueMessageReply schedules the follow-up for a thread reply. The task ID
// makes a second enqueue of the same reply a no-op.
func (p *Producer) EnqueueMessageReply(replyID uint) error {
	task, err := NewMessageReplyTask(replyID,
		asynq.Queue(QueueTransactional),
		asynq.MaxRetry(p.maxRetries),
		asynq.TaskID(fmt.Sprintf("message:reply:%d", replyID)),
	)
	if err != nil {
		return fmt.Errorf("failed to create message reply task: %w", err)
	}
	if _, err := p.client.Enqueue(task); err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("failed to enqueue message reply task: %w", err)
	}
	return nil
}

// EnqueueAutomationRun schedules one automation journey execution after the
// given delay (the enrollment nudge uses ~1s; the overdue sweep and the tick
// budget re-park use 0 = immediate). Automation ticks are background work, so
// they ride the low queue; the engine's 10s in-process scheduler is the
// backstop if an enqueue is lost. Deliberately NO TaskID dedup — a dedup id
// would swallow legitimate re-runs (crash between claim and execute, sweep
// after a lost nudge) — journey-level CAS makes duplicate deliveries safe.
func (p *Producer) EnqueueAutomationRun(journeyID uint, delay time.Duration) error {
	opts := []asynq.Option{asynq.Queue(QueueLow), asynq.MaxRetry(p.maxRetries)}
	if delay > 0 {
		opts = append(opts, asynq.ProcessIn(delay))
	}
	task, err := NewAutomationRunTask(journeyID, opts...)
	if err != nil {
		return fmt.Errorf("failed to create automation run task: %w", err)
	}
	if _, err := p.client.Enqueue(task); err != nil {
		return fmt.Errorf("failed to enqueue automation run: %w", err)
	}
	return nil
}

// Close closes the underlying Asynq client and inspector.
func (p *Producer) Close() error {
	if p.inspector != nil {
		_ = p.inspector.Close()
	}
	return p.client.Close()
}
