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

// UserAppPassword is a per-user credential for the future IMAP / POP3 /
// submission surfaces (SF-35 / SF-67, ADR-004): a separate secret a mail client
// can use instead of the account password, so the dashboard password never has
// to be pasted into a mail app.
//
// The plaintext is shown to the user exactly once at generation (same contract
// as the R-10 recovery codes); only the bcrypt digest is stored. A revoked
// password (RevokedAt set) never authenticates again.
type UserAppPassword struct {
	ID          uint       `json:"id" gorm:"primaryKey"`
	UserID      uint       `json:"user_id" gorm:"index;not null"`
	Name        string     `json:"name" gorm:"not null;default:''"`
	PasswordHash string    `json:"-" gorm:"not null"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

// Active reports whether the app password may still authenticate.
func (p *UserAppPassword) Active() bool { return p.RevokedAt == nil }
