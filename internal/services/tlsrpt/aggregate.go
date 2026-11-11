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

package tlsrpt

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// Aggregator derives the per-policy-domain session tallies for the outbound
// report (SF-104 slice) from the durable mta_delivery_attempts substrate
// (ADR-001 §5).
//
// Attribution rule (documented limitation): one attempt row records one SMTP
// session, and deliver.go runs one session per recipient-domain group — but
// the row does not carry the recipient domain. The aggregator loads the
// attempt's email row and attributes the session to the email's
// recipient-domain set only when that set resolves to exactly ONE domain (the
// overwhelmingly common transactional shape); multi-domain emails are counted
// as skipped and logged, never mis-attributed.
type Aggregator struct {
	db *gorm.DB
	// maxAttempts bounds one aggregation pass (worker memory backpressure,
	// SF-108 posture). 0 = the default.
	MaxAttempts int
	// Log receives the skip reasons (test assertions; production logger).
	Log func(reason string, emailID uint)
}

const defaultMaxAttempts = 10000

// Aggregate scans the attempts finished inside [start, end) and returns the
// per (policy domain, MX host) tallies plus how many attempt rows could not
// be attributed. Every query is parameterized through gorm — no SQL
// concatenation anywhere.
func (a *Aggregator) Aggregate(ctx context.Context, start, end time.Time) (aggs []SessionAggregate, skipped int, err error) {
	if a.db == nil {
		return nil, 0, fmt.Errorf("tlsrpt: aggregator has no database handle")
	}
	limit := a.MaxAttempts
	if limit <= 0 {
		limit = defaultMaxAttempts
	}

	var attempts []models.MTADeliveryAttempt
	if err := a.db.WithContext(ctx).
		Where("finished_at >= ? AND finished_at < ?", start, end).
		Order("id ASC").
		Limit(limit + 1).
		Find(&attempts).Error; err != nil {
		return nil, 0, fmt.Errorf("tlsrpt: attempt query: %w", err)
	}
	truncated := len(attempts) > limit
	if truncated {
		attempts = attempts[:limit]
	}

	emailIDs := make([]uint, 0, len(attempts))
	seen := map[uint]bool{}
	for _, at := range attempts {
		if !seen[at.EmailID] {
			seen[at.EmailID] = true
			emailIDs = append(emailIDs, at.EmailID)
		}
	}
	emailDomains := make(map[uint][]string, len(emailIDs))
	for _, chunk := range chunkIDs(emailIDs, 500) {
		var emails []models.Email
		if err := a.db.WithContext(ctx).
			Select("id", "recipients").
			Where("id IN ?", chunk).
			Find(&emails).Error; err != nil {
			return nil, 0, fmt.Errorf("tlsrpt: email query: %w", err)
		}
		for _, em := range emails {
			emailDomains[em.ID] = recipientDomains(em.Recipients)
		}
	}

	tallies := map[string]*SessionAggregate{}
	order := map[string]int{}
	for _, at := range attempts {
		domains := emailDomains[at.EmailID]
		if len(domains) != 1 {
			skipped++
			a.logSkip(fmt.Sprintf("tlsrpt: attempt skipped (%d recipient domains on the email; unattributable)", len(domains)), at.EmailID)
			continue
		}
		key := domains[0] + "|" + at.MXHost
		t := tallies[key]
		if t == nil {
			t = &SessionAggregate{PolicyDomain: domains[0], MXHost: at.MXHost}
			tallies[key] = t
			order[key] = len(order)
		}
		// Only sessions that reached a remote host belong in a TLS report:
		// DNS failures, budget deferrals and policy refusals never opened a
		// connection, so there is no session to count (RFC 8460 §2).
		switch at.Outcome {
		case models.MTAAttemptDelivered:
			t.Successful++
		case models.MTAAttemptTransientFailure, models.MTAAttemptPermanentFailure:
			if at.MXHost != "" {
				t.Failed++
			}
		}
	}
	if truncated {
		skipped++
		a.logSkip("tlsrpt: attempt window truncated at the aggregation cap; report covers the earliest attempts only", 0)
	}

	aggs = make([]SessionAggregate, 0, len(tallies))
	keys := make([]string, 0, len(tallies))
	for k := range tallies {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return order[keys[i]] < order[keys[j]] })
	for _, k := range keys {
		t := tallies[k]
		if t.Successful > 0 || t.Failed > 0 {
			aggs = append(aggs, *t)
		}
	}
	return aggs, skipped, nil
}

func (a *Aggregator) logSkip(reason string, emailID uint) {
	if a.Log != nil {
		a.Log(reason, emailID)
	}
}

// chunkIDs splits ids into query-sized chunks (bounded IN lists).
func chunkIDs(ids []uint, size int) [][]uint {
	if len(ids) == 0 {
		return nil
	}
	var out [][]uint
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[start:end])
	}
	return out
}

// recipientDomains re-derives the distinct recipient domains from the stored
// recipient list (lowercased, sorted, deduplicated) — the same derivation the
// delivery path applies (ADR-001 non-negotiable #2).
func recipientDomains(recipients []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range recipients {
		addr := strings.TrimSpace(r)
		i := strings.LastIndex(addr, "@")
		if i < 0 || i == len(addr)-1 {
			continue
		}
		d := strings.ToLower(addr[i+1:])
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// NewAggregator builds the gorm-backed aggregator.
func NewAggregator(db *gorm.DB) *Aggregator { return &Aggregator{db: db} }
