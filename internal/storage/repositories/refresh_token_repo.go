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

package repositories

import (
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// RefreshTokenRepository persists the CA-03 refresh-token rows. The plaintext
// token never lands here: callers store the SHA-256 digest and look rows up by
// it, so a database leak does not yield usable credentials.
type RefreshTokenRepository struct {
	db *gorm.DB
}

func NewRefreshTokenRepository(db *gorm.DB) *RefreshTokenRepository {
	return &RefreshTokenRepository{db: db}
}

func (r *RefreshTokenRepository) Create(t *models.RefreshToken) error {
	return r.db.Create(t).Error
}

// FindByHash resolves a presented token by its digest.
func (r *RefreshTokenRepository) FindByHash(hash string) (*models.RefreshToken, error) {
	var t models.RefreshToken
	if err := r.db.Where("token_hash = ?", hash).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// ConsumeForRotation atomically flips an ACTIVE token into the rotated state:
// the UPDATE carries the `revoked = false AND rotated_at IS NULL` guard, so
// under concurrent redemption exactly one caller wins (RowsAffected 1) and the
// loser observes the row already consumed — which is the replay signal.
func (r *RefreshTokenRepository) ConsumeForRotation(id uint) (bool, error) {
	now := time.Now().UTC()
	res := r.db.Model(&models.RefreshToken{}).
		Where("id = ? AND revoked = ? AND rotated_at IS NULL", id, false).
		Updates(map[string]any{"rotated_at": now, "revoked": true, "revoked_at": now})
	return res.RowsAffected == 1, res.Error
}

// RevokeFamily revokes every live token in a rotation family (CA-03 reuse
// detection: a replayed token kills the whole chain). Returns the number of
// rows that were still live.
func (r *RefreshTokenRepository) RevokeFamily(familyID uint) (int64, error) {
	now := time.Now().UTC()
	res := r.db.Model(&models.RefreshToken{}).
		Where("family_id = ? AND revoked = ?", familyID, false).
		Updates(map[string]any{"revoked": true, "revoked_at": now})
	return res.RowsAffected, res.Error
}

// RevokeByHash revokes one token if it is still live (logout).
func (r *RefreshTokenRepository) RevokeByHash(hash string) (int64, error) {
	now := time.Now().UTC()
	res := r.db.Model(&models.RefreshToken{}).
		Where("token_hash = ? AND revoked = ?", hash, false).
		Updates(map[string]any{"revoked": true, "revoked_at": now})
	return res.RowsAffected, res.Error
}

// RevokeAllForUser revokes every live refresh token for a user (used when a
// password changes or the account is disabled).
func (r *RefreshTokenRepository) RevokeAllForUser(userID uint) (int64, error) {
	now := time.Now().UTC()
	res := r.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked = ?", userID, false).
		Updates(map[string]any{"revoked": true, "revoked_at": now})
	return res.RowsAffected, res.Error
}

// TouchLastUsed stamps last_used_at on a freshly redeemed token (best-effort).
func (r *RefreshTokenRepository) TouchLastUsed(id uint) error {
	return r.db.Model(&models.RefreshToken{}).Where("id = ?", id).
		Update("last_used_at", time.Now().UTC()).Error
}

// NextFamilyID returns a fresh family ID. Families are numbered from the same
// sequence as the rows themselves, which keeps the column a plain integer
// index without a separate sequence table.
func (r *RefreshTokenRepository) NextFamilyID() (uint, error) {
	var maxID int64
	if err := r.db.Model(&models.RefreshToken{}).
		Select("COALESCE(MAX(family_id), 0)").Scan(&maxID).Error; err != nil {
		return 0, err
	}
	return uint(maxID) + 1, nil
}
