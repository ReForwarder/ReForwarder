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

package conditions

import (
	"fmt"
	"strings"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// The dynamic subscriber-list FilterRule SQL engine (SF-103): moved verbatim
// from SubscriberRepository.applyFilterRules so the unified surface owns the
// implementation and the repository delegates. The generated SQL is
// byte-for-byte the original's — the repositories package's cross-engine
// tests (segment_query_test.go) pin it unchanged through the delegate.

// Engagement field names and the shared unsubscribed-at column, hoisted so
// the whitelist maps, the switch cases and the segment builder all reference
// single identifiers. Exported here because both SQL currencies
// (FilterRule and Segment) accept these fields.
const (
	EngOpenedCampaign    = "opened_campaign"
	EngClickedCampaign   = "clicked_campaign"
	EngNotOpenedCampaign = "not_opened_campaign"
	EngReceivedCampaign  = "received_campaign"
	EngInactiveDays      = "inactive_days"
	EngActiveDays        = "active_days"
	EngTotalOpensGte     = "total_opens_gte"
	EngTotalClicksGte    = "total_clicks_gte"
	ColUnsubscribedAt    = "unsubscribed_at"
)

// ApplyFilterRuleSQL translates FilterRules into SQL WHERE clauses appended
// onto the given (already scoped) query. Values are bound as parameters;
// the only string ever interpolated is a custom-field key that survived
// isAlphanumericUnderscore (an invalid key skips the rule, as always).
func ApplyFilterRuleSQL(q *gorm.DB, rules models.FilterRules) *gorm.DB {
	for _, rule := range rules {
		field := rule.Field
		op := rule.Operator
		val := rule.Value

		// Engagement-based filters — use subqueries on campaign_messages
		if strings.HasPrefix(field, "engagement.") {
			engagementField := strings.TrimPrefix(field, "engagement.")
			switch engagementField {
			case EngOpenedCampaign:
				// Subscribers who opened a specific campaign
				q = q.Where("EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.campaign_id = ? AND campaign_messages.opened_at IS NOT NULL)", val)
			case EngClickedCampaign:
				// Subscribers who clicked in a specific campaign
				q = q.Where("EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.campaign_id = ? AND campaign_messages.clicked_at IS NOT NULL)", val)
			case EngNotOpenedCampaign:
				// Subscribers who did NOT open a specific campaign
				q = q.Where("NOT EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.campaign_id = ? AND campaign_messages.opened_at IS NOT NULL)", val)
			case EngReceivedCampaign:
				// Subscribers who received a specific campaign (regardless of open/click)
				q = q.Where("EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.campaign_id = ?)", val)
			case EngInactiveDays:
				// Subscribers with no opens in the last N days
				q = q.Where("NOT EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at > NOW() - INTERVAL '1 day' * ?)", val)
			case EngActiveDays:
				// Subscribers with opens in the last N days
				q = q.Where("EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at > NOW() - INTERVAL '1 day' * ?)", val)
			case EngTotalOpensGte:
				// Subscribers with at least N total opens
				q = q.Where("(SELECT COUNT(*) FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at IS NOT NULL) >= ?", val)
			case EngTotalClicksGte:
				// Subscribers with at least N total clicks
				q = q.Where("(SELECT COUNT(*) FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.clicked_at IS NOT NULL) >= ?", val)
			}
			continue
		}

		// Determine the SQL column expression
		var column string
		if strings.HasPrefix(field, "custom_fields.") {
			// JSON field extraction (PostgreSQL) - sanitize key to prevent SQL injection
			jsonKey := strings.TrimPrefix(field, "custom_fields.")
			if !IsAlphanumericUnderscore(jsonKey) || jsonKey == "" {
				continue
			}
			column = fmt.Sprintf("custom_fields::jsonb->>'%s'", jsonKey)
		} else {
			// Direct column (sanitize: only allow known fields)
			switch field {
			case "email", "name", "status":
				column = field
			default:
				continue // skip unknown fields
			}
		}

		switch op {
		case "eq":
			q = q.Where(fmt.Sprintf("%s = ?", column), val)
		case "neq":
			q = q.Where(fmt.Sprintf("%s != ?", column), val)
		case "contains":
			q = q.Where(fmt.Sprintf("%s ILIKE ?", column), fmt.Sprintf("%%%v%%", val))
		case "starts_with":
			q = q.Where(fmt.Sprintf("%s ILIKE ?", column), fmt.Sprintf("%v%%", val))
		case "ends_with":
			q = q.Where(fmt.Sprintf("%s ILIKE ?", column), fmt.Sprintf("%%%v", val))
		case "gt":
			q = q.Where(fmt.Sprintf("%s > ?", column), val)
		case "lt":
			q = q.Where(fmt.Sprintf("%s < ?", column), val)
		case "in":
			q = q.Where(fmt.Sprintf("%s IN ?", column), val)
		}
	}
	return q
}

// IsAlphanumericUnderscore returns true if s contains only [a-zA-Z0-9_-].
func IsAlphanumericUnderscore(s string) bool {
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
