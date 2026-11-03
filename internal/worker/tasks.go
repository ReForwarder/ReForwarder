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
	"encoding/json"

	"github.com/goposta/posta/internal/queues"
	"github.com/hibiken/asynq"
)

const (
	TypeEmailSend      = "email:send"
	TypeCampaignStart  = "campaign:start"
	TypeCampaignBatch  = "campaign:batch"
	TypeInboundParse   = "inbound:parse"
	TypeInboundProcess = "inbound:process"
	TypeMessageProcess = "message:process"
	TypeMessageReply   = "message:reply"
	TypeAutomationRun  = "automation:run"

	QueueTransactional = queues.Transactional
	QueueBulk          = queues.Bulk
	QueueLow           = queues.Low
)

type EmailSendPayload struct {
	EmailID uint `json:"email_id"`
}

// NewEmailSendTask creates an Asynq task to send an email.
func NewEmailSendTask(emailID uint, opts ...asynq.Option) (*asynq.Task, error) {
	payload, err := json.Marshal(EmailSendPayload{EmailID: emailID})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeEmailSend, payload, opts...), nil
}

type CampaignPayload struct {
	CampaignID uint `json:"campaign_id"`
}

func NewCampaignStartTask(campaignID uint, opts ...asynq.Option) (*asynq.Task, error) {
	payload, err := json.Marshal(CampaignPayload{CampaignID: campaignID})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeCampaignStart, payload, opts...), nil
}

func NewCampaignBatchTask(campaignID uint, opts ...asynq.Option) (*asynq.Task, error) {
	payload, err := json.Marshal(CampaignPayload{CampaignID: campaignID})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeCampaignBatch, payload, opts...), nil
}

type InboundProcessPayload struct {
	InboundEmailID uint `json:"inbound_email_id"`
}

// NewInboundProcessTask creates an Asynq task to dispatch an inbound email's
// email.inbound webhook to subscribers.
func NewInboundProcessTask(id uint, opts ...asynq.Option) (*asynq.Task, error) {
	payload, err := json.Marshal(InboundProcessPayload{InboundEmailID: id})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeInboundProcess, payload, opts...), nil
}

type InboundParsePayload struct {
	InboundEmailID uint `json:"inbound_email_id"`
}

// NewInboundParseTask creates an Asynq task that parses a previously-received
// raw inbound message into its structured form.
func NewInboundParseTask(id uint, opts ...asynq.Option) (*asynq.Task, error) {
	payload, err := json.Marshal(InboundParsePayload{InboundEmailID: id})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeInboundParse, payload, opts...), nil
}

type MessageProcessPayload struct {
	MessageID uint `json:"message_id"`
}

func NewMessageProcessTask(id uint, opts ...asynq.Option) (*asynq.Task, error) {
	payload, err := json.Marshal(MessageProcessPayload{MessageID: id})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeMessageProcess, payload, opts...), nil
}

// MessageReplyPayload names one thread reply, outbound or inbound, whose
// webhook (and, for inbound replies, notification) is still to be sent.
type MessageReplyPayload struct {
	ReplyID uint `json:"reply_id"`
}

func NewMessageReplyTask(id uint, opts ...asynq.Option) (*asynq.Task, error) {
	payload, err := json.Marshal(MessageReplyPayload{ReplyID: id})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeMessageReply, payload, opts...), nil
}

// AutomationRunPayload names one automation journey to advance.
type AutomationRunPayload struct {
	JourneyID uint `json:"journey_id"`
}

// NewAutomationRunTask creates an Asynq task that executes one automation
// journey (advancing it by up to ten nodes, parking it on delays, retrying
// failures). No TaskID dedup: every nudge must run, because a dedup id would
// swallow legitimate re-runs after a crash between claim and execution —
// idempotence is enforced by the journey status CAS inside the engine.
func NewAutomationRunTask(journeyID uint, opts ...asynq.Option) (*asynq.Task, error) {
	payload, err := json.Marshal(AutomationRunPayload{JourneyID: journeyID})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeAutomationRun, payload, opts...), nil
}
