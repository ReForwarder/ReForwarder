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

package spamfilter

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

// Sender reputation from the message-history substrate (ADR-006 decision 1,
// R-07's data): how the receiving tenant's own OUTBOUND relationship with a
// domain has gone. For a domain that now sends inbound mail, the only honest
// history this codebase owns is email_events on messages the tenant sent TO
// that domain — sent volume (the denominator), bounces and complaints (the
// numerators). Rates feed the scorer; a domain with too little history is
// unscored rather than penalized.
//
// Scope: per tenant (emails.user_id). Reputation is never global — one
// tenant's bad experience must not quarantine another tenant's mail.

// Reputation is one domain's outbound-relationship counts for one tenant.
type Reputation struct {
	Sent       int64
	Bounced    int64
	Complained int64
}

// minReputationSample is the smallest sent volume the rates are trusted
// over. Below it the signal abstains (a 1/1 bounce history is not evidence).
const minReputationSample = 20

// BounceRate / ComplaintRate return the rates, or -1 when the sample is too
// small to be meaningful (the scorer treats -1 as "no signal").
func (r Reputation) BounceRate() float64 {
	if r.Sent < minReputationSample {
		return -1
	}
	return float64(r.Bounced) / float64(r.Sent)
}

func (r Reputation) ComplaintRate() float64 {
	if r.Sent < minReputationSample {
		return -1
	}
	return float64(r.Complained) / float64(r.Sent)
}

// ReputationSource looks up one tenant's reputation for one sender domain.
type ReputationSource interface {
	SenderReputation(ctx context.Context, userID uint, senderDomain string) (Reputation, error)
}

// reputationRow is one aggregated (event type, recipient) count.
type reputationRow struct {
	Type string
	N    int64
}

// EventReputationSource reads reputation from the email_events substrate
// joined to the outbound messages. The aggregation runs in SQL (grouped by
// event type and recipient address, bounded to the tenant and to recipient
// addresses at the looked-up domain via a bound LIKE parameter); the
// domain-level summation happens here so the query stays portable. The
// statement is fully parameterized — nothing user-controlled is ever
// concatenated into SQL.
//
// Postgres note: recipients is a text[] column and the unnest lateral is
// Postgres-specific — consistent with this codebase, whose storage layer is
// Postgres (internal/storage/postgres.go).
type EventReputationSource struct {
	db *gorm.DB
}

// NewEventReputationSource builds the source over a gorm handle.
func NewEventReputationSource(db *gorm.DB) *EventReputationSource {
	return &EventReputationSource{db: db}
}

// reputationQuery aggregates the tenant's sent/bounced/complained events for
// recipient addresses at one domain. Everything after SELECT is constant
// text; both user-influenced values are bound parameters.
const reputationQuery = `
SELECT ee.type AS type, r.address AS address, COUNT(*) AS n
FROM email_events ee
JOIN emails e ON e.uuid = ee.email_uuid
CROSS JOIN LATERAL unnest(e.recipients) AS r(address)
WHERE e.user_id = ?
  AND ee.type IN ('sent','bounced','complained')
  AND r.address LIKE ?
GROUP BY ee.type, r.address`

func (s *EventReputationSource) SenderReputation(ctx context.Context, userID uint, senderDomain string) (Reputation, error) {
	var out Reputation
	if s == nil || s.db == nil || senderDomain == "" {
		return out, nil
	}
	domain := strings.ToLower(strings.TrimSpace(senderDomain))
	var rows []reputationRow
	if err := s.db.WithContext(ctx).
		Raw(reputationQuery, userID, "%@"+domain).
		Scan(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		switch row.Type {
		case "sent":
			out.Sent += row.N
		case "bounced":
			out.Bounced += row.N
		case "complained":
			out.Complained += row.N
		}
	}
	return out, nil
}
