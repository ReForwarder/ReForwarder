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

package models

import "time"

// TLSRPTReport is one policy entry of an RFC 8460 SMTP TLS report (SF-31,
// ADR-007 §5): a remote sending MTA's aggregate of the TLS sessions it
// attempted into one of the tenant's hosted domains, delivered to the rua
// address the domain published in its _smtp._tls TXT record. Reports arrive on
// the inbound pipeline (dedicated RCPT allowlist, gzip/size caps); one stored
// row represents one policies[] entry of the JSON report, keyed by
// (user_id, report_id, policy_domain) so re-ingesting the same report is
// idempotent.
//
// Ingestion mirrors the R-17 DMARC pattern (internal/services/inbound/dmarc.go):
// the message is durably stored first, then parsed; the processor never fails
// the ingestion.
type TLSRPTReport struct {
	ID          uint  `json:"id" gorm:"primaryKey"`
	UserID      uint  `json:"user_id" gorm:"index;not null;uniqueIndex:idx_tls_rpt_report_dedup"`
	WorkspaceID *uint `json:"workspace_id,omitempty" gorm:"index"`
	// DomainID resolves the report to the tenant domain the report was
	// addressed to (the rua address's domain) — the ownership anchor the read
	// surface scopes by. The report's self-declared policy domain is carried
	// separately below and never trusted for scoping.
	DomainID  *uint `json:"domain_id,omitempty" gorm:"index"`
	InboundID uint  `json:"inbound_id" gorm:"index"`

	ReportID     string `json:"report_id" gorm:"size:255;not null;uniqueIndex:idx_tls_rpt_report_dedup"`
	PolicyDomain string `json:"policy_domain" gorm:"size:255;not null;uniqueIndex:idx_tls_rpt_report_dedup;index"`

	// Report metadata (RFC 8460 §7.2 top level).
	OrganizationName string `json:"organization_name" gorm:"size:255"`
	ContactInfo      string `json:"contact_info" gorm:"size:255"`

	// Policy facts as the reporter saw them.
	PolicyType string `json:"policy_type" gorm:"size:20"` // sts | no-policy-found
	// MXHosts is the policy's mx-host list, JSON-encoded (empty when the
	// reporter declared none).
	MXHosts string `json:"mx_hosts" gorm:"type:text"`

	SuccessfulSessions int64 `json:"successful_sessions" gorm:"not null;default:0"`
	FailedSessions     int64 `json:"failed_sessions" gorm:"not null;default:0"`

	// DetailsJSON carries the policy's failure-details array, re-encoded and
	// size-capped by the ingest gate (the raw document stays on the inbound
	// record; this is the read-surface projection).
	DetailsJSON string `json:"details_json" gorm:"type:text"`

	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`

	CreatedAt time.Time `json:"created_at"`
}

// TableName overrides GORM's default table naming.
func (TLSRPTReport) TableName() string { return "tls_rpt_reports" }
