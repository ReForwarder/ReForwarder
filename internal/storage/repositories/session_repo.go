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

type SessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

func (r *SessionRepository) Create(session *models.Session) error {
	return r.db.Create(session).Error
}

// FindActiveByUserID returns non-revoked, non-expired sessions for a user.
func (r *SessionRepository) FindActiveByUserID(userID uint) ([]models.Session, error) {
	var sessions []models.Session
	if err := r.db.Where("user_id = ? AND revoked = false AND expires_at > ?", userID, time.Now()).
		Order("created_at DESC").
		Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

func (r *SessionRepository) FindByID(id uint) (*models.Session, error) {
	var session models.Session
	if err := r.db.First(&session, id).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *SessionRepository) FindByJTI(jti string) (*models.Session, error) {
	var session models.Session
	if err := r.db.Where("jti = ?", jti).First(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// Revoke marks a session as revoked.
func (r *SessionRepository) Revoke(id uint) error {
	return r.db.Model(&models.Session{}).Where("id = ?", id).Update("revoked", true).Error
}

// RevokeByJTI marks a session as revoked by JTI.
func (r *SessionRepository) RevokeByJTI(jti string) error {
	return r.db.Model(&models.Session{}).Where("jti = ?", jti).Update("revoked", true).Error
}

// RevokeAllByUserID revokes all active sessions for a user.
func (r *SessionRepository) RevokeAllByUserID(userID uint) (int64, error) {
	result := r.db.Model(&models.Session{}).
		Where("user_id = ? AND revoked = false AND expires_at > ?", userID, time.Now()).
		Update("revoked", true)
	return result.RowsAffected, result.Error
}

// RevokeOthersByUserID revokes all active sessions except the given JTI.
func (r *SessionRepository) RevokeOthersByUserID(userID uint, exceptJTI string) (int64, error) {
	result := r.db.Model(&models.Session{}).
		Where("user_id = ? AND jti != ? AND revoked = false AND expires_at > ?", userID, exceptJTI, time.Now()).
		Update("revoked", true)
	return result.RowsAffected, result.Error
}

// CleanExpired removes expired sessions from the database.
func (r *SessionRepository) CleanExpired() (int64, error) {
	result := r.db.Where("expires_at < ?", time.Now()).Delete(&models.Session{})
	return result.RowsAffected, result.Error
}

// IsRevoked checks if a session with the given JTI is revoked.
func (r *SessionRepository) IsRevoked(jti string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Session{}).
		Where("jti = ? AND revoked = true", jti).
		Count(&count).Error
	return count > 0, err
}
