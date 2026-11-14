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

// DMARCSentReport records one RFC 7489 aggregate report this deployment SENT
// to a reported domain's published rua endpoints (SF-25 send side; admin
// object, SF-57). The report covers a deployment-wide window: the verdicts
// come from every tenant's inbound traffic claiming the reported domain, so
// the row carries NO tenant attribution — the read surface scopes by the
// reported domain (a tenant sees the reports about their own domains; the
// admin surface sees all).
//
// The unique report_id index makes a re-run of the same window idempotent —
// the deterministic id cannot double-send or double-record.
type DMARCSentReport struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// ReportID is the RFC 7489 §7.2.1.4 id (deterministic per window + org +
	// reported domain).
	ReportID string `json:"report_id" gorm:"size:255;not null;uniqueIndex"`
	// ReportedDomain is the domain the aggregate is about (the From-domain
	// the evaluated messages claimed).
	ReportedDomain string `json:"reported_domain" gorm:"size:255;not null;index"`

	// Policy is the live published policy's p= at send time (the
	// policy-published projection).
	Policy string `json:"policy,omitempty" gorm:"size:20"`

	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`

	// RuaAddresses is the dispatched mailto endpoint list, JSON-encoded.
	RuaAddresses string `json:"rua_addresses" gorm:"type:text"`
	// RecordCount is the number of <record> blocks in the document.
	RecordCount int `json:"record_count" gorm:"not null;default:0"`
	// Document is the full aggregate XML (bounded by the aggregation caps).
	Document string `json:"document,omitempty" gorm:"type:text"`

	// Status is sent|failed; a failed row carries ErrorMessage and was NOT
	// delivered (best-effort reporting: failures are logged and dropped, the
	// next window supersedes them).
	Status       string `json:"status" gorm:"size:20;not null;default:sent"`
	ErrorMessage string `json:"error_message,omitempty" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`
}

// TableName overrides GORM's default table naming.
func (DMARCSentReport) TableName() string { return "dmarc_sent_reports" }

// ARFReport records one RFC 5965/6591 auth-failure feedback report this
// deployment SENT to a failing sender domain's published _arf abuse
// addresses (SF-26 send side; admin object, SF-57). Per-message by
// definition: one report per failing message, bounded per domain per window.
//
// The row attributes to the tenant whose inbound message failed (user_id +
// workspace_id — the read surface scopes by it), and the unique report_id
// index makes a re-run idempotent.
type ARFReport struct {
	ID uint `json:"id" gorm:"primaryKey"`
	UserID      uint  `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint `json:"workspace_id,omitempty" gorm:"index"`

	// ReportID is the deterministic per-message id
	// (<inbound-id>-<family>@<org>).
	ReportID string `json:"report_id" gorm:"size:255;not null;uniqueIndex"`
	// ReportedDomain is the failing sender domain the report is about.
	ReportedDomain string `json:"reported_domain" gorm:"size:255;not null;index"`
	// Family is the failing mechanism: dkim | spf.
	Family string `json:"family" gorm:"size:10;not null"`
	// InboundID references the offending message's inbound row.
	InboundID uint `json:"inbound_id" gorm:"index"`

	// Recipients is the dispatched abuse-address list, JSON-encoded.
	Recipients string `json:"recipients" gorm:"type:text"`
	// Document is the complete multipart/report body (bounded: the returned
	// message part carries a 64 KiB cap).
	Document string `json:"document,omitempty" gorm:"type:text"`

	// Status is sent|failed; a failed row carries ErrorMessage.
	Status       string `json:"status" gorm:"size:20;not null;default:sent"`
	ErrorMessage string `json:"error_message,omitempty" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`
}

// TableName overrides GORM's default table naming.
func (ARFReport) TableName() string { return "arf_reports" }
