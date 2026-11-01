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

package models

import "time"

// NotificationKind separates the two things that reach a user's inbox: a health
// alert Posta derived on its own, and an announcement a platform administrator
// wrote and broadcast.
type NotificationKind string

const (
	NotificationKindAlert        NotificationKind = "alert"
	NotificationKindAnnouncement NotificationKind = "announcement"
)

// NotificationSeverity ranks urgency. Only alerts carry warning/critical;
// announcements are info unless the operator says otherwise.
type NotificationSeverity string

const (
	NotificationInfo     NotificationSeverity = "info"
	NotificationWarning  NotificationSeverity = "warning"
	NotificationCritical NotificationSeverity = "critical"
)

// NotificationCategory groups items for filtering in the inbox.
type NotificationCategory string

const (
	NotificationCategoryDomains        NotificationCategory = "domains"
	NotificationCategoryDeliverability NotificationCategory = "deliverability"
	NotificationCategorySecurity       NotificationCategory = "security"
	NotificationCategoryMessages       NotificationCategory = "messages"
	NotificationCategoryPlatform       NotificationCategory = "platform"
)

// Notification is one item in a user's in-app inbox: the bell, the notifications
// page, and the alert banner on the workspace dashboard all read this table.
//
// It is per-user rather than per-workspace because read and dismissed state
// belong to a person, not to a team. A condition affecting a workspace fans out
// to one row per member.
type Notification struct {
	ID     uint `json:"id" gorm:"primaryKey"`
	UserID uint `json:"user_id" gorm:"not null;index:idx_notification_user_open,priority:1"`
	// WorkspaceID is nil for platform-wide items, which follow the user rather
	// than any one workspace.
	WorkspaceID *uint                `json:"workspace_id,omitempty" gorm:"index"`
	Kind        NotificationKind     `json:"kind" gorm:"not null;default:alert"`
	Category    NotificationCategory `json:"category" gorm:"not null"`
	Severity    NotificationSeverity `json:"severity" gorm:"not null;default:info"`

	Title      string `json:"title" gorm:"not null"`
	Body       string `json:"body"`
	Link       string `json:"link,omitempty"`
	ActionText string `json:"action_text,omitempty"`

	// DedupKey names the condition, not the occurrence: "domains:unverified" for a
	// workspace's unverified domains. At most one unresolved row exists per
	// (user, workspace, dedup_key), so a condition that persists across scans
	// updates in place instead of filling the bell with duplicates.
	DedupKey string `json:"dedup_key" gorm:"not null;index:idx_notification_user_open,priority:2"`

	// Fingerprint captures the condition's current shape — the count, the bucketed
	// rate. It is what makes dismissal safe: a dismissal is bound to the
	// fingerprint it was made against, so dismissing "3 domains unverified" stays
	// dismissed at 3 and resurfaces at 4. Without it, dismissing once would
	// silence the condition forever, including as it gets worse.
	Fingerprint string `json:"-" gorm:"not null;default:''"`

	ReadAt      *time.Time `json:"read_at,omitempty"`
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
	// ResolvedAt is set when the underlying condition clears. Resolved items stay
	// in the inbox as history but leave the dashboard banner.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`

	CreatedAt time.Time `json:"created_at" gorm:"index"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Open reports whether the notification still describes a live condition the
// user has not dismissed — the set the dashboard banner renders.
func (n *Notification) Open() bool {
	return n.ResolvedAt == nil && n.DismissedAt == nil
}

// Announcement is a platform-wide notice written by an administrator. It is kept
// as its own row so the operator can see what was sent, and edit or retract it,
// independently of the per-user deliveries it produced.
type Announcement struct {
	ID    uint   `json:"id" gorm:"primaryKey"`
	Title string `json:"title" gorm:"not null"`
	// Message is the notice text. It is deliberately not called Body: okapi
	// resolves a request's body by looking for a field named Body or tagged
	// json:"body", and it applies that rule again to the body's own fields — so
	// a payload containing "body" binds as an empty struct rather than failing
	// loudly. See TestRequestBodiesHaveNoBodyField.
	Message    string               `json:"message"`
	Link       string               `json:"link,omitempty"`
	Severity   NotificationSeverity `json:"severity" gorm:"not null;default:info"`
	CreatedBy  uint                 `json:"created_by" gorm:"not null"`
	AuthorName string               `json:"author_name"`
	// Recipients is how many inbox rows the broadcast produced.
	Recipients int        `json:"recipients" gorm:"not null;default:0"`
	SentAt     *time.Time `json:"sent_at,omitempty"`

	CreatedAt time.Time `json:"created_at" gorm:"index"`
	UpdatedAt time.Time `json:"updated_at"`
}
