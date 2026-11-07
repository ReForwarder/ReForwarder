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

// InboundForwardTarget is one confirmed forwarding destination (OB-2, ADR-002
// decision 2): an external or internal address a forward rule may re-send to.
// A rule's forward action only executes while the target row for that address
// is confirmed — the mailbox owner clicked the single-use confirmation link
// (no open forwarding to arbitrary addresses).
//
// The confirmation token is stored HASHED (SHA-256 hex, TokenHash); the raw
// token is shown exactly once when the target is created and travels only in
// the plain-text confirm URL the operator sends manually. Confirmation clears
// the hash — a confirmed target has no live token left, which makes the link
// single-use by construction.
type InboundForwardTarget struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// UserID is the owning tenant; WorkspaceID scopes the target like every
	// other resource. Confirmation is per scope: a target confirmed for one
	// workspace is not automatically confirmed for another.
	UserID      uint  `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint `json:"workspace_id,omitempty" gorm:"index"`
	// Target is the destination address (lowercased at creation).
	Target string `json:"target" gorm:"size:320;not null;index"`
	// TokenHash is the SHA-256 hex of the outstanding confirmation token, or
	// empty once confirmed (the single-use consumption) or never issued.
	TokenHash string `json:"-" gorm:"size:64;index"`
	// ConfirmedAt is set by the single-use confirmation; nil = not confirmed.
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName overrides GORM's default table naming.
func (InboundForwardTarget) TableName() string { return "inbound_forward_targets" }

// IsConfirmed reports whether this target has completed its confirmation.
func (t InboundForwardTarget) IsConfirmed() bool {
	return t.ConfirmedAt != nil
}
