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

// Package dmarc is the DMARC aggregate-report send side (SF-25), mirroring
// the TLS-RPT send precedent (internal/services/tlsrpt): an aggregator over
// the inbound verification substrate (SF-22/23/24's inbound_auth_results),
// the RFC 7489 aggregate-report XML builder, and the delivery seam the cron
// job dispatches through (the production adapter rides the system-forward
// pipeline — queued, signed, standard retry, system provenance).
//
// Policy decisions baked in (mirroring the TLS-RPT send precedent,
// documented on the cron job):
//   - one daily pass, only when the window carried DMARC-evaluated messages —
//     the deployment never mails external addresses on a quiet day;
//   - reports are addressed ONLY to rua endpoints read from the reported
//     domain's live _dmarc DNS record at send time (never to stored or
//     user-supplied addresses);
//   - a send failure is logged and dropped: the next window's report
//     supersedes the lost one (RFC 7489 reports are best-effort aggregates;
//     no retry queue is kept for them).
package dmarc

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// RecordRow is one DMARC-evaluated message's verdict, as stored on the
// inbound verification substrate (SF-22/23/24). Alignment is applied at
// build time against the live published policy (adkim/aspf), exactly as the
// evaluator applied it when the verdict was computed.
type RecordRow struct {
	ClientIP    string
	Disposition string // none|quarantine|reject (the stored recommendation)
	SPFResult   string // pass|fail|softfail|neutral|none|temperror|permerror
	SPFDomain   string
	DKIMResult  string // pass|fail|none|temperror|permerror
	DKIMDomain  string
	DKIMSelector string
}

// DomainAggregate is one reported domain's evaluated messages inside the
// window, in stable (id) order.
type DomainAggregate struct {
	PolicyDomain string
	Rows         []RecordRow
}

// Aggregator collects the window's DMARC-evaluated inbound messages per
// policy domain for the outbound report (SF-25 send side).
//
// Reporting rule (documented): only rows carrying a real DMARC evaluation
// aggregate — dmarc_result pass|fail. Rows with none (no published policy —
// nothing to report against) and temperror/permerror (resolver trouble —
// not an evaluation of the policy) are skipped and logged, never guessed.
type Aggregator struct {
	db *gorm.DB
	// MaxResults bounds one aggregation pass (worker memory backpressure,
	// SF-108 posture). 0 = the default.
	MaxResults int
	// Log receives the skip reasons (test assertions; production logger).
	Log func(reason string)
}

const defaultMaxResults = 10000

// NewAggregator builds the aggregator over the inbound_auth_results
// substrate.
func NewAggregator(db *gorm.DB) *Aggregator { return &Aggregator{db: db} }

// Aggregate scans the verification verdicts created inside [start, end) and
// returns the per-policy-domain message rows plus how many rows were skipped
// (not reportable DMARC evaluations). Every query is parameterized through
// gorm — no SQL concatenation anywhere.
func (a *Aggregator) Aggregate(ctx context.Context, start, end time.Time) (aggs []DomainAggregate, skipped int, err error) {
	if a.db == nil {
		return nil, 0, fmt.Errorf("dmarc: aggregator has no database handle")
	}
	limit := a.MaxResults
	if limit <= 0 {
		limit = defaultMaxResults
	}

	var results []models.InboundAuthResult
	if err := a.db.WithContext(ctx).
		Where("created_at >= ? AND created_at < ?", start, end).
		Order("id ASC").
		Limit(limit + 1).
		Find(&results).Error; err != nil {
		return nil, 0, fmt.Errorf("dmarc: auth-result query: %w", err)
	}
	truncated := len(results) > limit
	if truncated {
		results = results[:limit]
	}
	if a.Log != nil && truncated {
		a.Log(fmt.Sprintf("dmarc: aggregation truncated at %d rows; the window's remainder is not in the report", limit))
	}

	tallies := map[string]*DomainAggregate{}
	for _, r := range results {
		domain := r.DMARCPolicyDomain
		// Reportability: only a real DMARC evaluation aggregates — pass|fail.
		// Rows with none (no published policy — nothing to report against)
		// and temperror/permerror (resolver trouble — not an evaluation of
		// the policy) are skipped and logged, never guessed.
		if domain == "" || (r.DMARCResult != "pass" && r.DMARCResult != "fail") {
			skipped++
			a.logSkip(fmt.Sprintf("dmarc: row skipped (dmarc_result %q on %q is not a reportable evaluation)", r.DMARCResult, domain))
			continue
		}
		agg := tallies[domain]
		if agg == nil {
			agg = &DomainAggregate{PolicyDomain: domain}
			tallies[domain] = agg
		}
		agg.Rows = append(agg.Rows, RecordRow{
			ClientIP:     r.ClientIP,
			Disposition:  r.DMARCDisposition,
			SPFResult:    r.SPFResult,
			SPFDomain:    r.SPFDomain,
			DKIMResult:   r.DKIMResult,
			DKIMDomain:   r.DKIMDomain,
			DKIMSelector: r.DKIMSelector,
		})
	}

	// Deterministic emission order (first-seen, mirroring the query's id
	// ordering).
	domains := make([]string, 0, len(tallies))
	for domain := range tallies {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	for _, domain := range domains {
		aggs = append(aggs, *tallies[domain])
	}
	return aggs, skipped, nil
}

func (a *Aggregator) logSkip(reason string) {
	if a.Log != nil {
		a.Log(reason)
	}
}
