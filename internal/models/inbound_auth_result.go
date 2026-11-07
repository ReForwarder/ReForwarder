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

// InboundAuthResult is the message-level SPF/DKIM/DMARC verdict for one
// inbound email (SF-22/23/24, ADR-002 Phase-3 slice): computed asynchronously
// after durable ingest, never blocking delivery. The unique inbound_id index
// makes re-verification idempotent — one row per message, first verdict wins.
// Disposition is a RECOMMENDATION (none|quarantine|reject) for the ADR-002
// routing engine; it is not executed at ingest time.
type InboundAuthResult struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// UserID is the tenant that received the message (the inbound record's
	// owner); WorkspaceID scopes it like every other inbound artifact.
	UserID      uint  `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint `json:"workspace_id,omitempty" gorm:"index"`
	// InboundID references models.InboundEmail.ID; unique so re-running the
	// verification is a no-op (INSERT ... ON CONFLICT DO NOTHING).
	InboundID uint   `json:"inbound_id" gorm:"uniqueIndex;not null"`
	MessageID string `json:"message_id" gorm:"size:255;index"`

	ClientIP     string `json:"client_ip,omitempty" gorm:"size:64"`
	EnvelopeFrom string `json:"envelope_from,omitempty" gorm:"size:320"`

	// SPF: pass|fail|softfail|neutral|none|temperror|permerror (RFC 7208).
	SPFResult string `json:"spf_result" gorm:"size:20;not null;default:'none'"`
	SPFDomain string `json:"spf_domain,omitempty" gorm:"size:255"`
	SPFReason string `json:"spf_reason,omitempty" gorm:"size:255"`

	// DKIM: pass|fail|none|temperror|permerror (RFC 8601), with the d=/s=
	// of the signature the verdict is about.
	DKIMResult   string `json:"dkim_result" gorm:"size:20;not null;default:'none'"`
	DKIMDomain   string `json:"dkim_domain,omitempty" gorm:"size:255"`
	DKIMSelector string `json:"dkim_selector,omitempty" gorm:"size:255"`

	// DMARC: pass|fail|none|temperror|permerror (RFC 7489), plus the policy
	// that governed and the recommended (never auto-executed) disposition.
	DMARCResult       string `json:"dmarc_result" gorm:"size:20;not null;default:'none'"`
	DMARCDisposition  string `json:"dmarc_disposition" gorm:"size:20;not null;default:'none'"`
	DMARCPolicyDomain string `json:"dmarc_policy_domain,omitempty" gorm:"size:255"`
	DMARCPolicy       string `json:"dmarc_policy,omitempty" gorm:"size:20"`

	// AuthResultsHeader is the composed RFC 8601 Authentication-Results
	// value under the instance authserv-id — the authoritative record of the
	// injected header.
	AuthResultsHeader string `json:"auth_results_header,omitempty" gorm:"type:text"`

	CreatedAt time.Time `json:"created_at"`
}

// TableName overrides GORM's default table naming.
func (InboundAuthResult) TableName() string { return "inbound_auth_results" }
