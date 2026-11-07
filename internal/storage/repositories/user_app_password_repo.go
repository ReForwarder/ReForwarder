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

// UserAppPasswordRepository stores the bcrypt digests of the per-user app
// passwords (SF-35/SF-67). The plaintext never reaches this layer: it is shown
// once by the handler at generation time and discarded.
type UserAppPasswordRepository struct {
	db *gorm.DB
}

func NewUserAppPasswordRepository(db *gorm.DB) *UserAppPasswordRepository {
	return &UserAppPasswordRepository{db: db}
}

func (r *UserAppPasswordRepository) Create(p *models.UserAppPassword) error {
	return r.db.Create(p).Error
}

// FindByUserAndID returns one of the user's own app passwords, or
// gorm.ErrRecordNotFound when the row is not theirs (scoping is the query).
func (r *UserAppPasswordRepository) FindByUserAndID(userID, id uint) (*models.UserAppPassword, error) {
	var p models.UserAppPassword
	if err := r.db.Where("user_id = ? AND id = ?", userID, id).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// ListByUser returns the user's app passwords, newest first. Hashes are
// excluded at the model level (`json:"-"`), and this query deliberately does
// not select the digest column at all.
func (r *UserAppPasswordRepository) ListByUser(userID uint) ([]models.UserAppPassword, error) {
	var list []models.UserAppPassword
	if err := r.db.Select("id", "user_id", "name", "created_at", "last_used_at", "revoked_at").
		Where("user_id = ?", userID).
		Order("created_at DESC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// Revoke sets revoked_at on the user's own password row. Rows that are already
// revoked are left alone; the affected count tells the handler whether the row
// existed and was live.
func (r *UserAppPasswordRepository) Revoke(userID, id uint) (int64, error) {
	res := r.db.Model(&models.UserAppPassword{}).
		Where("user_id = ? AND id = ? AND revoked_at IS NULL", userID, id).
		Update("revoked_at", time.Now().UTC())
	return res.RowsAffected, res.Error
}

// FindActiveHashByUser loads the still-active digests for one user so the
// credential can be verified without listing metadata. This is the seam the
// future IMAP / POP3 / submission auth backends (ADR-004) call with the
// username the client presented.
func (r *UserAppPasswordRepository) FindActiveByUserID(userID uint) ([]models.UserAppPassword, error) {
	var list []models.UserAppPassword
	if err := r.db.Where("user_id = ? AND revoked_at IS NULL", userID).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// TouchLastUsed stamps last_used_at after a successful verification
// (best-effort on the caller's side).
func (r *UserAppPasswordRepository) TouchLastUsed(id uint) error {
	return r.db.Model(&models.UserAppPassword{}).Where("id = ?", id).
		Update("last_used_at", time.Now().UTC()).Error
}
