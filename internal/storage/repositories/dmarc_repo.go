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

package repositories

import (
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// DMARCSummary is the per-domain roll-up of the stored RFC 7489 aggregate
// reports (R-17). One dmarc_reports row is one <record> element — a source
// IP's authentication tally over a report window — so RecordCount counts
// report records while MessageCount sums the message volumes they carried.
type DMARCSummary struct {
	// RecordCount is the number of stored report records.
	RecordCount int64 `json:"record_count"`
	// MessageCount is the total message volume the records carried (sum of count).
	MessageCount int64 `json:"message_count"`
	// DKIMAlignedMessages / SPFAlignedMessages are the message volumes whose
	// DKIM / SPF alignment passed.
	DKIMAlignedMessages int64 `json:"dkim_aligned_messages"`
	SPFAlignedMessages  int64 `json:"spf_aligned_messages"`
	// DKIMAlignment / SPFAlignment are the message-weighted alignment ratios
	// (aligned messages / total messages, 0..1; 0 when nothing is stored).
	DKIMAlignment float64 `json:"dkim_alignment"`
	SPFAlignment  float64 `json:"spf_alignment"`
	// Disposition breakdown by record: the policy disposition receiving
	// providers reported applying (none | quarantine | reject).
	DispositionNone       int64 `json:"disposition_none"`
	DispositionQuarantine int64 `json:"disposition_quarantine"`
	DispositionReject     int64 `json:"disposition_reject"`
	// LatestWindowEnd is the end of the newest stored report window; nil when
	// the domain has no stored reports.
	LatestWindowEnd *time.Time `json:"latest_window_end"`
}

// DMARCReportRepository reads the DMARC aggregate reports the inbound
// pipeline ingested (R-17). Read-only: rows are written by the ingestion
// processor (internal/services/inbound/dmarc.go).
type DMARCReportRepository struct {
	db *gorm.DB
}

func NewDMARCReportRepository(db *gorm.DB) *DMARCReportRepository {
	return &DMARCReportRepository{db: db}
}

const (
	// dmarcRecentDefault applies when a caller passes no limit.
	dmarcRecentDefault = 20
	// dmarcRecentMax caps a single Recent call.
	dmarcRecentMax = 500
)

// scoped narrows a query to one domain's rows inside one workspace. Every
// query on dmarc_reports goes through it: rows carry the workspace they were
// ingested under, and dropping the workspace filter would leak one tenant's
// authentication record to another. A domain outside any workspace never
// reaches this repository (scope ownership rejects it first), so workspaceID
// is the resolved domain's workspace; 0 matches nothing.
func (r *DMARCReportRepository) scoped(domainID, workspaceID uint) *gorm.DB {
	return r.db.Where("domain_id = ? AND workspace_id = ?", domainID, workspaceID)
}

// DomainSummary aggregates every stored report record for the domain into the
// per-domain view: record and message counts, message-weighted DKIM/SPF
// alignment ratios, the disposition breakdown, and the newest report window.
// A domain with no stored reports yields the zero summary (latest window nil).
func (r *DMARCReportRepository) DomainSummary(domainID, workspaceID uint) (DMARCSummary, error) {
	var agg dmarcSummaryAggregate
	err := r.scoped(domainID, workspaceID).
		Model(&models.DMARCReport{}).
		Select(`COUNT(*) AS record_count,
COALESCE(SUM("count"), 0) AS message_count,
COALESCE(SUM(CASE WHEN dkim_aligned THEN "count" ELSE 0 END), 0) AS dkim_aligned,
COALESCE(SUM(CASE WHEN spf_aligned THEN "count" ELSE 0 END), 0) AS spf_aligned,
COALESCE(SUM(CASE WHEN disposition = 'none' THEN 1 ELSE 0 END), 0) AS disp_none,
COALESCE(SUM(CASE WHEN disposition = 'quarantine' THEN 1 ELSE 0 END), 0) AS disp_quarantine,
COALESCE(SUM(CASE WHEN disposition = 'reject' THEN 1 ELSE 0 END), 0) AS disp_reject,
MAX(window_end) AS latest_window_end`).
		Scan(&agg).Error
	if err != nil {
		return DMARCSummary{}, err
	}
	return DMARCSummary{
		RecordCount:           agg.RecordCount,
		MessageCount:          agg.MessageCount,
		DKIMAlignedMessages:   agg.DKIMAligned,
		SPFAlignedMessages:    agg.SPFAligned,
		DKIMAlignment:         dmarcRatioOf(agg.DKIMAligned, agg.MessageCount),
		SPFAlignment:          dmarcRatioOf(agg.SPFAligned, agg.MessageCount),
		DispositionNone:       agg.DispNone,
		DispositionQuarantine: agg.DispQuarantine,
		DispositionReject:     agg.DispReject,
		LatestWindowEnd:       agg.LatestWindowEnd,
	}, nil
}

// dmarcSummaryAggregate is the raw SQL aggregate row DomainSummary scans;
// the public DMARCSummary derives from it.
type dmarcSummaryAggregate struct {
	RecordCount     int64
	MessageCount    int64
	DKIMAligned     int64
	SPFAligned      int64
	DispNone        int64
	DispQuarantine  int64
	DispReject      int64
	LatestWindowEnd *time.Time
}

// dmarcRatioOf returns part/total as a 0..1 fraction; 0 for an empty total.
func dmarcRatioOf(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// Recent returns the domain's newest report records, latest window first.
// limit is clamped to [1, dmarcRecentMax]; non-positive means the default.
func (r *DMARCReportRepository) Recent(domainID, workspaceID uint, limit int) ([]models.DMARCReport, error) {
	switch {
	case limit <= 0:
		limit = dmarcRecentDefault
	case limit > dmarcRecentMax:
		limit = dmarcRecentMax
	}
	var reports []models.DMARCReport
	if err := r.scoped(domainID, workspaceID).
		Order("window_end DESC, id DESC").
		Limit(limit).
		Find(&reports).Error; err != nil {
		return nil, err
	}
	return reports, nil
}
