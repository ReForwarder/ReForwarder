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

// RefreshToken is a CA-03 rotation token: a long-lived, single-use bearer
// secret the client redeems at POST /auth/refresh for a fresh access JWT plus
// a successor refresh token. The plaintext lives only with the client; the row
// stores a SHA-256 digest (peppered with the JWT secret).
//
// Rotation model:
//   - every token carries the ID of the rotation family it belongs to;
//   - redeeming an active token atomically marks it rotated (RotatedAt set,
//     Revoked set) and issues the successor in the same family;
//   - presenting a token that was already rotated is a replay: the whole
//     family is revoked (standard reuse detection), so a stolen token chain
//     dies with the first replay.
type RefreshToken struct {
	ID       uint   `json:"id" gorm:"primaryKey"`
	UserID   uint   `json:"user_id" gorm:"index;not null"`
	FamilyID uint   `json:"family_id" gorm:"index;not null"`
	TokenHash string `json:"-" gorm:"uniqueIndex;not null"`
	// RotatedFrom is the ID of the predecessor token, 0 for the family root.
	RotatedFrom uint `json:"rotated_from" gorm:"default:0;not null"`
	// RotatedAt is set when the token has been redeemed (consumed) by a
	// rotation. Non-nil makes the token a replay if it is ever presented again.
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
	// Revoked marks the token as unusable, whether by rotation, reuse
	// detection, or logout.
	Revoked    bool       `json:"revoked" gorm:"default:false;not null"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at" gorm:"not null"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}
