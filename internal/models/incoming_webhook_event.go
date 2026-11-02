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

// IncomingWebhookEvent stores the raw payload of an HTTP callback the platform
// received (bounce posts, tracking pings, provider webhooks) before it is
// processed, so nothing is lost when parsing or resolution fails. The email is
// resolved from the payload when possible; EmailUUID stays NULL otherwise.
type IncomingWebhookEvent struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	Source       string    `json:"source" gorm:"size:32;not null;index"` // bounce|tracking|smtp|<provider>
	Type         string    `json:"type" gorm:"size:40;not null;index"`   // bounced|opened|clicked|…
	EmailUUID    string    `json:"email_uuid,omitempty" gorm:"type:uuid;index;default:null"`
	Recipient    string    `json:"recipient" gorm:"size:255;not null;default:'';index"`
	MessageID    string    `json:"message_id,omitempty" gorm:"size:120;index"`
	RawPayload   string    `json:"raw_payload" gorm:"type:text;not null"`
	ProcessError string    `json:"process_error,omitempty" gorm:"type:text"`
	CreatedAt    time.Time `json:"created_at" gorm:"index;not null"`
}

// TableName overrides GORM's default table naming.
func (IncomingWebhookEvent) TableName() string { return "incoming_webhook_events" }
