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

type InboundEmailStatus string

const (
	InboundStatusReceived    InboundEmailStatus = "received"
	InboundStatusForwarded   InboundEmailStatus = "forwarded"
	InboundStatusFailed      InboundEmailStatus = "failed"
	InboundStatusRejected    InboundEmailStatus = "rejected"
	InboundStatusQuarantined InboundEmailStatus = "quarantined"
)

type InboundSource string

const (
	InboundSourceSMTP    InboundSource = "smtp"
	InboundSourceWebhook InboundSource = "webhook"
	// InboundSourceLMTP — mail handed over by a local delivery agent over
	// the RFC 2033 LMTP listener (SF-01); same ingest pipeline as SMTP.
	InboundSourceLMTP InboundSource = "lmtp"
)

type InboundEmail struct {
	ID              uint           `json:"id" gorm:"primaryKey"`
	UUID            string         `json:"uuid" gorm:"type:uuid;default:gen_random_uuid();uniqueIndex;not null"`
	UserID          uint           `json:"user_id" gorm:"index;not null"`
	WorkspaceID     *uint          `json:"workspace_id,omitempty" gorm:"index"`
	DomainID        uint           `json:"domain_id" gorm:"index;not null"`
	MessageID       string         `json:"message_id" gorm:"index"`
	Sender          string         `json:"sender" gorm:"not null"`
	Recipients      pq.StringArray `json:"recipients" gorm:"type:text[];not null"`
	Subject         string         `json:"subject"`
	TextBody        string         `json:"text_body" gorm:"type:text"`
	HTMLBody        string         `json:"html_body" gorm:"type:text"`
	AttachmentsJSON string         `json:"attachments_json,omitempty" gorm:"type:text"`
	HeadersJSON     string         `json:"headers_json,omitempty" gorm:"type:text"`
	RawStorageKey   string         `json:"raw_storage_key,omitempty"`
	// RawContent holds the raw RFC 5322 message until the async
	RawContent []byte   `json:"-" gorm:"type:bytea"`
	DedupHash  string   `json:"-" gorm:"index"`
	Size       int64    `json:"size"`
	SpamScore  *float64 `json:"spam_score,omitempty"`
	// SpamReasons holds the comma-joined reason vocabulary the ADR-006 scorer
	// produced for this message (empty when the filter is disabled or the
	// message delivered without a flag). Additive column (OB-2 W5).
	SpamReasons string `json:"spam_reasons,omitempty" gorm:"type:text"`
	// ThreatSignature records the ClamAV signature of a malware positive
	// (GAP-47); empty unless the malware scanner flagged an attachment.
	// Additive column (OB-2 W5).
	ThreatSignature string             `json:"threat_signature,omitempty" gorm:"size:255"`
	Status          InboundEmailStatus `json:"status" gorm:"default:received;not null"`
	Source          InboundSource      `json:"source" gorm:"not null"`
	ErrorMessage    string             `json:"error_message,omitempty"`
	RetryCount      int                `json:"retry_count" gorm:"default:0;not null"`
	ReceivedAt      time.Time          `json:"received_at" gorm:"not null"`
	ForwardedAt     *time.Time         `json:"forwarded_at,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`

	// OB-2 routing provenance (ADR-002 decision 1): the route the RCPT/ingest
	// validation matched for this message, so the post-ingest executor knows
	// which action to run without re-evaluating rules. Both columns are
	// additive and empty/NULL when routing is disabled or nothing matched
	// (deliver-without-rule keeps today's behavior).
	RouteID     *uint              `json:"route_id,omitempty" gorm:"index"`
	RouteAction InboundRouteAction `json:"route_action,omitempty" gorm:"size:20;index;default:''"`

	User   User   `json:"-" gorm:"foreignKey:UserID"`
	Domain Domain `json:"-" gorm:"foreignKey:DomainID"`
}

// InboundAttachmentMeta describes one inbound attachment stored in blob storage.
// Serialized into InboundEmail.AttachmentsJSON.
type InboundAttachmentMeta struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	StorageKey  string `json:"storage_key,omitempty"`
	Content     string `json:"content,omitempty"`
}
