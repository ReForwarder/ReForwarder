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

package models

import "time"

// Mail-account aliases and groups (SF-65): additive mailstore tables that
// extend the (user_id, name) mailbox identity with alternate addresses. An
// alias delivers into ONE existing mailbox; a group fans a delivery out to
// ALL its member mailboxes. Both shapes are owner-scoped (user_id) and are
// consumed only by the inbound deliver action's expansion (ADR-002 route
// action, SF-65) — the transactional Email model and every marketing path
// are untouched, exactly like the OB-3 mailbox-delivery seam.

// MailAlias maps one alias address onto an existing mailbox: mail delivered
// to Address lands in the target mailbox's INBOX. Address is globally unique
// across aliases (one address, one target). One hop only: the target is a
// mailbox, never another alias, so alias chains cannot loop by construction.
type MailAlias struct {
	ID     uint `json:"id" gorm:"primaryKey"`
	UserID uint `json:"user_id" gorm:"index;not null"`
	// WorkspaceID carries the ownership convention every mailstore table
	// uses (ADR-008 §1); nullable for personal (unbound) mailboxes.
	WorkspaceID *uint     `json:"workspace_id,omitempty" gorm:"index"`
	MailboxID   uint      `json:"mailbox_id" gorm:"not null;index"`
	Address     string    `json:"address" gorm:"size:255;not null;uniqueIndex"`
	CreatedAt   time.Time `json:"created_at"`
}

func (MailAlias) TableName() string { return "mail_aliases" }

// MailGroup is a group address backed by its own anchor mailbox (the group's
// mailbox identity, one per group) and a member list. Delivery to the group
// address fans out to every member mailbox idempotently — a mailbox reached
// through several paths (direct address, alias target, membership) receives
// exactly one copy per message. Members may include other groups' anchor
// mailboxes (list-of-lists); the delivery expansion cuts cycles with its
// visited set and a depth bound, so a cyclic configuration terminates.
type MailGroup struct {
	ID     uint `json:"id" gorm:"primaryKey"`
	UserID uint `json:"user_id" gorm:"index;not null"`
	// WorkspaceID carries the ownership convention (ADR-008 §1).
	WorkspaceID *uint `json:"workspace_id,omitempty" gorm:"index"`
	// MailboxID is the group's own mailbox — the anchor that gives the group
	// address a mailbox identity. It receives nothing by itself; only
	// members do (unless the anchor is itself a member).
	MailboxID uint      `json:"mailbox_id" gorm:"not null;uniqueIndex"`
	Address   string    `json:"address" gorm:"size:255;not null;uniqueIndex"`
	CreatedAt time.Time `json:"created_at"`
}

func (MailGroup) TableName() string { return "mail_groups" }

// MailGroupMember is one membership: a member mailbox inside a group.
// (group_id, mailbox_id) is unique — a mailbox appears at most once per
// group.
type MailGroupMember struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// GroupID references mail_groups.id (no FK constraint — the repo's
	// transactional delete removes members with the group, matching the
	// mailstore tables' convention).
	GroupID   uint      `json:"group_id" gorm:"not null;uniqueIndex:idx_mail_group_members_group_mb"`
	MailboxID uint      `json:"mailbox_id" gorm:"not null;uniqueIndex:idx_mail_group_members_group_mb;index"`
	CreatedAt time.Time `json:"created_at"`
}

func (MailGroupMember) TableName() string { return "mail_group_members" }
