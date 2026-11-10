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

package models

import "time"

// Email event types recorded on the message-history timeline.
const (
	EmailEventSent         = "sent"
	EmailEventDelivered    = "delivered"
	EmailEventFailed       = "failed"
	EmailEventOpened       = "opened"
	EmailEventClicked      = "clicked"
	EmailEventBounced      = "bounced"
	EmailEventComplained   = "complained"
	EmailEventUnsubscribed = "unsubscribed"
	// EmailEventForwarded stamps the moment the inbound forwarding executor
	// enqueued a system_forward copy (OB-2, ADR-002 decision 2). It has no
	// write-once timestamp column — the copy's sent/failed events carry the
	// delivery truth; this event marks the forwarding provenance on the
	// message-history timeline.
	EmailEventForwarded = "forwarded"
	// EmailEventComposed stamps the moment the webmail compose seam accepted a
	// message into the outbound pipeline (OB-5, ADR-005 decision 2). Like
	// "forwarded" it has no write-once timestamp column — the delivery truth
	// arrives as sent/failed/delivered events; this event marks the compose
	// provenance (and the Sent-folder linkage) on the timeline.
	EmailEventComposed = "composed"
)

// EmailEvent records one lifecycle event on the message-history timeline of an
// outbound email. Provider replays and duplicate deliveries are deduped by the
// unique index on (email_uuid, type, provider_event_id): for opens and clicks
// without a provider event id, callers store a hash of (url|ip|user-agent|
// date bucket) in ProviderEventID so only genuinely distinct interactions are
// kept. Denormalized "first time" timestamps live on the Email row itself and
// are write-once there; repeated events may still add timeline rows.
type EmailEvent struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	EmailUUID   string `json:"email_uuid" gorm:"type:uuid;not null;index:idx_email_events_email;index:idx_email_events_dedup,unique"`
	WorkspaceID *uint  `json:"workspace_id,omitempty" gorm:"index"`
	Type        string `json:"type" gorm:"size:20;not null;index:idx_email_events_type_created,priority:1;index:idx_email_events_dedup,unique"`
	// sent|delivered|failed|forwarded|opened|clicked|bounced|complained|unsubscribed
	OccurredAt      time.Time `json:"occurred_at" gorm:"not null;index:idx_email_events_type_created,priority:2"`
	Provider        string    `json:"provider" gorm:"size:32;not null;default:''"`
	ProviderEventID string    `json:"provider_event_id,omitempty" gorm:"size:120;index:idx_email_events_dedup,unique"`
	Recipient       string    `json:"recipient" gorm:"size:255;not null;default:''"`
	IP              string    `json:"ip,omitempty" gorm:"size:45"`
	UserAgent       string    `json:"user_agent,omitempty" gorm:"type:text"`
	URL             string    `json:"url,omitempty" gorm:"type:text"` // clicked URL
	Error           string    `json:"error,omitempty" gorm:"type:text"`
	Metadata        string    `json:"metadata" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt       time.Time `json:"created_at" gorm:"not null"`
}

// TableName overrides GORM's default table naming.
func (EmailEvent) TableName() string { return "email_events" }
