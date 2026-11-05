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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// DMARCReport is one record of an RFC 7489 aggregate report (R-17): a
// receiving mail provider's per-source-IP authentication tally for one of the
// tenant's sending domains, over the report's UTC interval. Reports arrive as
// XML attachments on the inbound pipeline; one stored row represents one
// <record> element, so per-domain auth-summary trends are plain aggregates.
// The (user_id, report_id, source_ip) unique index makes re-ingestion of the
// same report idempotent.
type DMARCReport struct {
	ID          uint  `json:"id" gorm:"primaryKey"`
	UserID      uint  `json:"user_id" gorm:"index;not null;uniqueIndex:idx_dmarc_report_dedup"`
	WorkspaceID *uint `json:"workspace_id,omitempty" gorm:"index"`
	DomainID    *uint `json:"domain_id,omitempty" gorm:"index"`
	InboundID   uint  `json:"inbound_id" gorm:"index"`
	// Domain is the published policy domain the report is about (the tenant's
	// sending domain).
	Domain string `json:"domain" gorm:"size:255;not null;index"`
	// Reporter is the reporting organization's name and contact.
	Reporter      string `json:"reporter" gorm:"size:255"`
	ReporterEmail string `json:"reporter_email" gorm:"size:255"`
	ReportID      string `json:"report_id" gorm:"size:255;not null;uniqueIndex:idx_dmarc_report_dedup"`
	SourceIP      string `json:"source_ip" gorm:"size:64;not null;uniqueIndex:idx_dmarc_report_dedup"`

	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`

	Count       int    `json:"count" gorm:"not null;default:0"`
	DKIMAligned bool   `json:"dkim_aligned" gorm:"not null;default:false"`
	SPFAligned  bool   `json:"spf_aligned" gorm:"not null;default:false"`
	Disposition string `json:"disposition" gorm:"size:20"` // none|quarantine|reject
	HeaderFrom  string `json:"header_from" gorm:"size:255"`

	CreatedAt time.Time `json:"created_at"`
}

// TableName overrides GORM's default table naming.
func (DMARCReport) TableName() string { return "dmarc_reports" }
