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

package inbox

import (
	"fmt"

	"github.com/goposta/posta/internal/models"
)

// Thresholds for the derived health rules. They match what the dashboard used to
// compute in the browser, so moving the rules to the server did not change when
// a workspace is told something is wrong.
const (
	minVolumeForRates   = 20
	bounceRateAlert     = 5.0
	failureRateAlert    = 10.0
	rateFingerprintBand = 5.0
)

// Snapshot is the workspace health the rules read. The dashboard already
// computes every field for its own stats, so evaluating the rules costs nothing
// beyond the rule evaluation itself.
type Snapshot struct {
	UnverifiedDomains int64
	ExpiringAPIKeys   int64
	UnreadMessages    int64
	TotalEmails       int64
	BounceRate        float64
	FailureRate       float64
	MessagesEnabled   bool
}

// rule is one condition Posta watches on a workspace's behalf.
type rule struct {
	dedupKey string
	category models.NotificationCategory
	severity models.NotificationSeverity
	minRole  models.WorkspaceRole
	title    string
	body     string
	link     string
	action   string
	// fingerprint captures the condition's magnitude, so a dismissal made at one
	// magnitude does not silence a worse one. See models.Notification.Fingerprint.
	fingerprint string
}

// evaluate returns the rules currently firing for a workspace.
func evaluate(s Snapshot) []rule {
	var out []rule

	if s.UnverifiedDomains > 0 {
		out = append(out, rule{
			dedupKey:    "domains:unverified",
			category:    models.NotificationCategoryDomains,
			severity:    models.NotificationWarning,
			minRole:     models.WorkspaceRoleEditor,
			title:       fmt.Sprintf("%d domain%s not verified", s.UnverifiedDomains, plural(s.UnverifiedDomains)),
			body:        "Mail from an unverified domain is far more likely to be filtered, and some routes refuse to send from one at all.",
			link:        "/domains",
			action:      "Verify domains",
			fingerprint: fmt.Sprintf("n=%d", s.UnverifiedDomains),
		})
	}

	// Rates are only meaningful once enough mail has gone out; below that a
	// single bounce reads as a catastrophe.
	if s.TotalEmails >= minVolumeForRates && s.BounceRate >= bounceRateAlert {
		out = append(out, rule{
			dedupKey:    "deliverability:bounce-rate",
			category:    models.NotificationCategoryDeliverability,
			severity:    models.NotificationCritical,
			minRole:     models.WorkspaceRoleEditor,
			title:       fmt.Sprintf("Bounce rate is %.1f%%", s.BounceRate),
			body:        "Sustained bounces above 5% damage sender reputation. Clean the recipient list before sending more.",
			link:        "/bounces",
			action:      "Review bounces",
			fingerprint: band(s.BounceRate),
		})
	}

	if s.TotalEmails >= minVolumeForRates && s.FailureRate >= failureRateAlert {
		out = append(out, rule{
			dedupKey:    "deliverability:failure-rate",
			category:    models.NotificationCategoryDeliverability,
			severity:    models.NotificationCritical,
			minRole:     models.WorkspaceRoleEditor,
			title:       fmt.Sprintf("%.1f%% of sends are failing", s.FailureRate),
			body:        "Check the SMTP server configuration and the most recent failures for a common cause.",
			link:        "/emails?status=failed",
			action:      "View failed emails",
			fingerprint: band(s.FailureRate),
		})
	}

	if s.ExpiringAPIKeys > 0 {
		out = append(out, rule{
			dedupKey:    "security:expiring-api-keys",
			category:    models.NotificationCategorySecurity,
			severity:    models.NotificationWarning,
			minRole:     models.WorkspaceRoleAdmin,
			title:       fmt.Sprintf("%d API key%s expiring within 7 days", s.ExpiringAPIKeys, plural(s.ExpiringAPIKeys)),
			body:        "Rotate them before they lapse, or the integrations using them will start failing.",
			link:        "/api-keys",
			action:      "Manage API keys",
			fingerprint: fmt.Sprintf("n=%d", s.ExpiringAPIKeys),
		})
	}

	if s.MessagesEnabled && s.UnreadMessages > 0 {
		out = append(out, rule{
			dedupKey:    "messages:unread",
			category:    models.NotificationCategoryMessages,
			severity:    models.NotificationInfo,
			minRole:     models.WorkspaceRoleViewer,
			title:       fmt.Sprintf("%d unread message%s", s.UnreadMessages, plural(s.UnreadMessages)),
			body:        "Someone filled in one of your web forms and is waiting for a reply.",
			link:        "/messages",
			action:      "Open inbox",
			fingerprint: fmt.Sprintf("n=%d", s.UnreadMessages),
		})
	}

	return out
}

// band buckets a rate so a dismissal is not undone by noise. A rate drifting
// from 5.1% to 5.4% is the same problem; crossing into the next band is not.
func band(rate float64) string {
	return fmt.Sprintf("band=%d", int(rate/rateFingerprintBand))
}

func plural(n int64) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// roleRank orders workspace roles so a rule's minimum role can be compared
// against a member's.
func roleRank(r models.WorkspaceRole) int {
	switch r {
	case models.WorkspaceRoleOwner:
		return 3
	case models.WorkspaceRoleAdmin:
		return 2
	case models.WorkspaceRoleEditor:
		return 1
	default:
		return 0
	}
}
