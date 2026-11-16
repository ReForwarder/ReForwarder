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

// FilterScript is one per-mailbox filter script (SF-14, the ADR-006
// amendment): ordered conditions (the unified evaluator's envelope-fact
// grammar) + the bounded operations (fileinto / redirect / discard / keep)
// evaluated at mailbox delivery — after the spam disposition, before final
// folder placement. User-owned, bound to one mailbox; the FIRST matching
// enabled script wins and later scripts never stack.
type FilterScript struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// UserID — the mailbox owner (the scripts belong to the session user's
	// own mailbox; the mail surface's scoping posture).
	UserID uint `json:"user_id" gorm:"not null;index"`
	// MailboxID is the mailbox the script files into (the owner's mailbox).
	MailboxID uint `json:"mailbox_id" gorm:"not null;index"`
	Name      string `json:"name" gorm:"size:255;not null"`
	// Priority orders evaluation (lower evaluated first; first match wins).
	Priority int  `json:"priority" gorm:"not null;default:100"`
	Enabled  bool `json:"enabled" gorm:"not null;default:true"`
	// Conditions is the envelope-fact rule set (models.FilterRules JSON —
	// the FilterRule shape reused; the filterengine owns the strict grammar).
	Conditions FilterRules `json:"conditions" gorm:"type:text"`
	// ActionsJSON is the ordered action list ([{"type","target"}] — the
	// filterengine.ScriptAction shape), stored as text per the repo's
	// JSON-in-text convention.
	ActionsJSON string    `json:"actions_json" gorm:"type:text"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (FilterScript) TableName() string { return "filter_scripts" }
