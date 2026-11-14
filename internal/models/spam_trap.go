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

// SpamTrap is a seeded trap address (SF-42, the ADR-006 amendment): an
// address on one of the tenant's hosted domains that is never a real
// recipient — harvested from public sources, it receives nothing but
// unsolicited mail. Mail whose recipient matches a seeded trap is by
// definition unsolicited: the ingest worker quarantines it INSTANTLY (the
// SF-42 gate, step 0.5) with the spam_trap_hit reason. Workspace-scoped
// (the traps belong to the domain's tenant; the admin surface validates the
// domain at write time).
type SpamTrap struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// WorkspaceID — the tenant the trap belongs to (NOT NULL; matches the
	// bound domain's workspace).
	WorkspaceID uint `json:"workspace_id" gorm:"not null;index"`
	// DomainName is the receiving hosted domain the trap address sits on.
	DomainName string `json:"domain_name" gorm:"size:255;not null;uniqueIndex:idx_spam_traps_domain_local"`
	// LocalPart is the trap's local part (lowercased at write time).
	LocalPart  string    `json:"local_part" gorm:"size:255;not null;uniqueIndex:idx_spam_traps_domain_local"`
	Note       string    `json:"note" gorm:"size:255"`
	CreatedAt  time.Time `json:"created_at"`
}
