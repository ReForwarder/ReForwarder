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

import (
	"time"

	"github.com/lib/pq"
)

type EmailStatus string

const (
	EmailStatusPending    EmailStatus = "pending"
	EmailStatusQueued     EmailStatus = "queued"
	EmailStatusProcessing EmailStatus = "processing"
	EmailStatusSent       EmailStatus = "sent"
	EmailStatusFailed     EmailStatus = "failed"
	EmailStatusSuppressed EmailStatus = "suppressed"
	EmailStatusScheduled  EmailStatus = "scheduled"
)

// EmailProvenanceSystemForward stamps a transactional Email row that the
// inbound forwarding executor enqueued as a system message (OB-2, ADR-002
// decision 2). Forwarding is not a marketing-send path: consent and
// suppression layers are never consulted for system forwards.
const EmailProvenanceSystemForward = "system_forward"

// EmailProvenanceMailCompose stamps a transactional Email row that the webmail
// compose seam enqueued (OB-5, ADR-005 decision 2): the outbound twin of a
// message stored in the composer's Sent folder. Compose is not a
// marketing-send path either — suppression/unsubscribe layers are not
// consulted — but the sender domain must be verified (ADR-001 non-negotiable).
const EmailProvenanceMailCompose = "mail-compose"

type Email struct {
	ID              uint           `json:"id" gorm:"primaryKey"`
	UUID            string         `json:"uuid" gorm:"type:uuid;default:gen_random_uuid();uniqueIndex;not null"`
	ExternalID      string         `json:"external_id,omitempty" gorm:"size:120;index"`
	UserID          uint           `json:"user_id" gorm:"index;not null"`
	WorkspaceID     *uint          `json:"workspace_id,omitempty" gorm:"index"`
	APIKeyID        *uint          `json:"api_key_id" gorm:"index"`
	Sender          string         `json:"sender" gorm:"not null"`
	Recipients      pq.StringArray `json:"recipients" gorm:"type:text[];not null"`
	Subject         string         `json:"subject" gorm:"not null"`
	TemplateName    string         `json:"template_name,omitempty"`
	HTMLBody        string         `json:"html_body"`
	TextBody        string         `json:"text_body"`
	AttachmentsJSON string         `json:"attachments_json,omitempty" gorm:"type:text"`
	HeadersJSON     string         `json:"headers_json,omitempty" gorm:"type:text"`
	// TemplateData holds the render data of the template as a JSON object
	// string. The default keeps Create-safe behavior: gorm omits the zero
	// value, and jsonb rejects ''.
	TemplateData          string `json:"template_data,omitempty" gorm:"type:jsonb;not null;default:'{}'"`
	ListUnsubscribeURL    string `json:"list_unsubscribe_url,omitempty" gorm:"type:text"`
	ListUnsubscribeMailto string `json:"list_unsubscribe_mailto,omitempty" gorm:"type:text"`
	ListUnsubscribePost   bool   `json:"list_unsubscribe_post,omitempty"`
	UnsubscribeListID     *uint  `json:"unsubscribe_list_id,omitempty" gorm:"index"`
	AutomationID          *uint  `json:"automation_id,omitempty" gorm:"index"`
	// Provenance stamps system-generated messages (OB-2, ADR-002 decision 2):
	// "system_forward" marks the re-send copy a forwarding executor enqueued
	// from a routed inbound message. Empty = a tenant/API-originated message.
	// Additive column; the constant lives below.
	Provenance   string      `json:"provenance,omitempty" gorm:"size:32;index;default:''"`
	Status       EmailStatus `json:"status" gorm:"default:pending;not null"`
	ErrorMessage string      `json:"error_message"`
	RetryCount   int         `json:"retry_count" gorm:"default:0;not null"`
	CreatedAt    time.Time   `json:"created_at"`
	SentAt       *time.Time  `json:"sent_at"`
	ScheduledAt  *time.Time  `json:"scheduled_at"`
	Provider     string      `json:"provider,omitempty" gorm:"index;size:32"`

	// Write-once lifecycle timestamps: denormalized from the message-history
	// event timeline and set only the first time the state is reached, so the
	// logs list can filter and sort without joining email_events.
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	OpenedAt       *time.Time `json:"opened_at,omitempty"`
	ClickedAt      *time.Time `json:"clicked_at,omitempty"`
	BouncedAt      *time.Time `json:"bounced_at,omitempty"`
	ComplainedAt   *time.Time `json:"complained_at,omitempty"`
	UnsubscribedAt *time.Time `json:"unsubscribed_at,omitempty"`

	SMTPHostname string `json:"smtp_hostname,omitempty"`

	User   User   `json:"-" gorm:"foreignKey:UserID"`
	APIKey APIKey `json:"-" gorm:"foreignKey:APIKeyID;constraint:OnDelete:SET NULL"`

	APIKeyName string `json:"api_key_name,omitempty" gorm:"-"`
}

// Attachment represents a file attachment in an email.
type Attachment struct {
	Filename    string `json:"filename"`
	Content     string `json:"content,omitempty"`
	ContentType string `json:"content_type"`
	// StorageKey is set when the attachment content is stored in blob storage
	// (S3 or filesystem). When present, Content may be empty and the actual
	// bytes should be fetched from the blob store using this key.
	StorageKey string `json:"storage_key,omitempty"`
}
