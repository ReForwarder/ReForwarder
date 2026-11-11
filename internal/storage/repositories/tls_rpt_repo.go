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

package repositories

import (
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// TLSRPTSummary is the per-domain roll-up of the stored RFC 8460 TLS reports
// (SF-31 inbound half). One tls_rpt_reports row is one policies[] entry — a
// reporter's session tally for one policy domain over one report window.
type TLSRPTSummary struct {
	// ReportCount is the number of stored policy entries (dedup-keyed rows).
	ReportCount int64 `json:"report_count"`
	// SuccessfulSessions / FailedSessions are the session totals the
	// reporters sent about this domain.
	SuccessfulSessions int64 `json:"successful_sessions"`
	FailedSessions     int64 `json:"failed_sessions"`
	// LatestWindowEnd is the end of the newest stored report window; nil
	// when the domain has no stored reports.
	LatestWindowEnd *time.Time `json:"latest_window_end"`
	// LatestReportID is the report id of the newest stored report; empty
	// when nothing is stored.
	LatestReportID string `json:"latest_report_id"`
}

// TLSRPTRepository reads the SMTP TLS reports the inbound pipeline ingested
// (SF-31, OB-6 W4). Read-only: rows are written by the ingestion processor
// (internal/services/inbound/tlsrpt.go).
type TLSRPTRepository struct {
	db *gorm.DB
}

func NewTLSRPTRepository(db *gorm.DB) *TLSRPTRepository {
	return &TLSRPTRepository{db: db}
}

const (
	// tlsRptRecentDefault applies when a caller passes no limit.
	tlsRptRecentDefault = 20
	// tlsRptRecentMax caps a single Recent call.
	tlsRptRecentMax = 500
)

// scoped narrows a query to one domain's rows inside one workspace. Every
// query on tls_rpt_reports goes through it: rows carry the workspace they
// were ingested under, and dropping the workspace filter would leak one
// tenant's TLS report record to another (ADR-008 §1). A domain outside any
// workspace never reaches this repository (scope ownership rejects it
// first), so workspaceID is the resolved domain's workspace; 0 matches
// nothing.
func (r *TLSRPTRepository) scoped(domainID, workspaceID uint) *gorm.DB {
	return r.db.Where("domain_id = ? AND workspace_id = ?", domainID, workspaceID)
}

// Summary aggregates every stored report entry for the domain: report count,
// session totals, and the newest report window. A domain with no stored
// reports yields the zero summary.
func (r *TLSRPTRepository) Summary(domainID, workspaceID uint) (TLSRPTSummary, error) {
	var agg struct {
		ReportCount     int64
		Successful      int64
		Failed          int64
		LatestWindowEnd *time.Time
		LatestReportID  string
	}
	err := r.scoped(domainID, workspaceID).
		Model(&models.TLSRPTReport{}).
		Select(`COUNT(*) AS report_count,
COALESCE(SUM(successful_sessions), 0) AS successful,
COALESCE(SUM(failed_sessions), 0) AS failed,
MAX(window_end) AS latest_window_end`).
		Scan(&agg).Error
	if err != nil {
		return TLSRPTSummary{}, err
	}
	if agg.LatestWindowEnd != nil {
		// The newest report id rides the newest window end (same row).
		var latest []models.TLSRPTReport
		_ = r.scoped(domainID, workspaceID).
			Order("window_end DESC, id DESC").
			Limit(1).
			Find(&latest).Error
		if len(latest) > 0 {
			agg.LatestReportID = latest[0].ReportID
		}
	}
	return TLSRPTSummary{
		ReportCount:        agg.ReportCount,
		SuccessfulSessions: agg.Successful,
		FailedSessions:     agg.Failed,
		LatestWindowEnd:    agg.LatestWindowEnd,
		LatestReportID:     agg.LatestReportID,
	}, nil
}

// Recent returns the domain's newest report entries, latest window first.
// limit is clamped to [1, tlsRptRecentMax]; non-positive means the default.
func (r *TLSRPTRepository) Recent(domainID, workspaceID uint, limit int) ([]models.TLSRPTReport, error) {
	switch {
	case limit <= 0:
		limit = tlsRptRecentDefault
	case limit > tlsRptRecentMax:
		limit = tlsRptRecentMax
	}
	var reports []models.TLSRPTReport
	if err := r.scoped(domainID, workspaceID).
		Order("window_end DESC, id DESC").
		Limit(limit).
		Find(&reports).Error; err != nil {
		return nil, err
	}
	return reports, nil
}
