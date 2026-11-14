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

// Package arf is the auth-failure feedback-report send side (SF-26, RFC
// 5965/6591), mirroring the DMARC aggregate send precedent
// (internal/services/dmarc, which itself mirrors the TLS-RPT send): an
// aggregator over the inbound verification substrate (SF-22/23/24's
// inbound_auth_results), the RFC 5965 §6 feedback-report MIME builder
// (multipart/report; report-type=auth-failure), and the delivery seam the
// cron job dispatches through (the production adapter stores the report as a
// system notification that the outbound pipeline relays verbatim).
//
// Policy decisions baked in (documented; the TLS-RPT/DMARC send precedents'
// decisions apply):
//   - one daily pass, bounded per reported domain per window — the deployment
//     never mails external addresses on a quiet day or unboundedly;
//   - reports are addressed ONLY to the abuse-reporting addresses read from
//     the reported domain's live _arf DNS record at send time (RFC 6591 §4;
//     never to stored or user-supplied addresses);
//   - a send failure is logged and dropped: best-effort reports, no retry
//     queue.
package arf

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// FailureRow is one auth-failure verdict from the inbound verification
// substrate (SF-22/23/24): one candidate feedback report (RFC 5965's ARF is
// inherently per-message).
type FailureRow struct {
	// InboundID identifies the offending message (the report's third part).
	InboundID uint
	// UserID / WorkspaceID attribute the received message to its tenant.
	UserID      uint
	WorkspaceID *uint
	Family      string // "dkim" | "spf"
	// Domain is the reported domain: the DKIM d= of the failed signature, or
	// the SPF (MAIL FROM) domain of the failed check.
	Domain string
	// ClientIP is the source IP the message arrived from.
	ClientIP string
	// AuthResultsHeader is the stored RFC 8601 Authentication-Results value
	// under this deployment's authserv-id — the report's second part.
	AuthResultsHeader string
	CreatedAt         time.Time
}

// DomainBatch is one reported domain's failure candidates inside the window,
// in stable (id) order.
type DomainBatch struct {
	ReportedDomain string
	Family         string
	Rows           []FailureRow
}

// Aggregator collects the window's auth-failure verdicts per reported domain
// for the outbound feedback reports (SF-26 send side).
type Aggregator struct {
	db *gorm.DB
	// MaxRowsPerDomain bounds one domain's candidate rows per pass (the
	// deployment never mails external addresses unboundedly; the TLS-RPT /
	// DMARC precedents' quiet-day-and-bounded posture). 0 = the default.
	MaxRowsPerDomain int
	// Log receives the skip reasons (test assertions; production logger).
	Log func(reason string)
}

const defaultMaxRowsPerDomain = 50

// NewAggregator builds the aggregator over the inbound_auth_results
// substrate.
func NewAggregator(db *gorm.DB) *Aggregator { return &Aggregator{db: db} }

// Aggregate scans the verification verdicts created inside [start, end) and
// returns the per-domain failure candidates plus how many rows were skipped
// (no reporting domain on the failure). Every query is parameterized through
// gorm — no SQL concatenation anywhere.
func (a *Aggregator) Aggregate(ctx context.Context, start, end time.Time) (batches []DomainBatch, skipped int, err error) {
	if a.db == nil {
		return nil, 0, fmt.Errorf("arf: aggregator has no database handle")
	}
	bound := a.MaxRowsPerDomain
	if bound <= 0 {
		bound = defaultMaxRowsPerDomain
	}

	var results []models.InboundAuthResult
	if err := a.db.WithContext(ctx).
		Where("created_at >= ? AND created_at < ?", start, end).
		Order("id ASC").
		Find(&results).Error; err != nil {
		return nil, 0, fmt.Errorf("arf: auth-result query: %w", err)
	}

	tallies := map[string]*DomainBatch{}
	for _, r := range results {
		family := ""
		domain := ""
		switch {
		case r.DKIMResult == "fail" && r.DKIMDomain != "":
			family, domain = "dkim", r.DKIMDomain
		case r.SPFResult == "fail" && r.SPFDomain != "":
			family, domain = "spf", r.SPFDomain
		default:
			// Not an addressable auth failure: no failure, or no domain to
			// report against. Logged, never guessed.
			skipped++
			a.logSkip(fmt.Sprintf("arf: row skipped (no addressable auth failure: dkim=%q spf=%q)", r.DKIMResult, r.SPFResult))
			continue
		}
		key := family + "|" + domain
		batch := tallies[key]
		if batch == nil {
			batch = &DomainBatch{ReportedDomain: domain, Family: family}
			tallies[key] = batch
		}
		if len(batch.Rows) >= bound {
			skipped++
			a.logSkip(fmt.Sprintf("arf: row skipped (per-domain bound %d reached for %s)", bound, domain))
			continue
		}
		batch.Rows = append(batch.Rows, FailureRow{
			InboundID:         r.InboundID,
			UserID:            r.UserID,
			WorkspaceID:       r.WorkspaceID,
			Family:            family,
			Domain:            domain,
			ClientIP:          r.ClientIP,
			AuthResultsHeader: r.AuthResultsHeader,
			CreatedAt:         r.CreatedAt,
		})
	}

	// Deterministic emission order (family, then domain).
	keys := make([]string, 0, len(tallies))
	for key := range tallies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		batches = append(batches, *tallies[key])
	}
	return batches, skipped, nil
}

func (a *Aggregator) logSkip(reason string) {
	if a.Log != nil {
		a.Log(reason)
	}
}
