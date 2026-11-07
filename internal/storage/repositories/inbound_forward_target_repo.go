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

// InboundForwardTargetRepository stores the confirmed forwarding destinations
// (OB-2, ADR-002 decision 2). Tokens are persisted hashed only; confirmation
// is single-use (the hash is cleared on confirm).
type InboundForwardTargetRepository struct {
	db *gorm.DB
}

func NewInboundForwardTargetRepository(db *gorm.DB) *InboundForwardTargetRepository {
	return &InboundForwardTargetRepository{db: db}
}

// Create persists a new forward-target row.
func (r *InboundForwardTargetRepository) Create(t *models.InboundForwardTarget) error {
	return r.db.Create(t).Error
}

// Delete removes a forward-target row (the revoke operation). Returns the
// gorm result so callers can distinguish "not found".
func (r *InboundForwardTargetRepository) Delete(id uint) error {
	return r.db.Delete(&models.InboundForwardTarget{}, id).Error
}

// FindByID returns one target row, or gorm.ErrRecordNotFound.
func (r *InboundForwardTargetRepository) FindByID(id uint) (*models.InboundForwardTarget, error) {
	var t models.InboundForwardTarget
	if err := r.db.First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// FindByScope lists the scope's forward targets, newest first.
func (r *InboundForwardTargetRepository) FindByScope(scope ResourceScope, limit, offset int) ([]models.InboundForwardTarget, int64, error) {
	q := ApplyScope(r.db.Model(&models.InboundForwardTarget{}), scope)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	var items []models.InboundForwardTarget
	if err := q.Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// FindByTokenHash resolves a confirmation token's row by its hash, regardless
// of scope — the token itself is the credential on the public confirm link.
// Returns gorm.ErrRecordNotFound when no outstanding token matches (already
// consumed, revoked, or wrong).
func (r *InboundForwardTargetRepository) FindByTokenHash(tokenHash string) (*models.InboundForwardTarget, error) {
	var t models.InboundForwardTarget
	if err := r.db.Where("token_hash = ?", tokenHash).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// FindConfirmed reports whether the scope has a CONFIRMED row for the target
// address. The forwarding executor refuses a task whose target has none.
func (r *InboundForwardTargetRepository) FindConfirmed(scope ResourceScope, target string) (*models.InboundForwardTarget, error) {
	var t models.InboundForwardTarget
	q := ApplyScope(r.db.Model(&models.InboundForwardTarget{}), scope)
	if err := q.Where("target = ? AND confirmed_at IS NOT NULL", target).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// Confirm consumes the single-use token: sets confirmed_at and clears the
// token hash in ONE update keyed on the outstanding hash, so two clicks (or a
// click racing a revoke) can never both consume it — the loser matches no row.
func (r *InboundForwardTargetRepository) Confirm(id uint, tokenHash string, at time.Time) (bool, error) {
	res := r.db.Model(&models.InboundForwardTarget{}).
		Where("id = ? AND token_hash = ?", id, tokenHash).
		Updates(map[string]interface{}{
			"confirmed_at": at,
			"token_hash":   "",
		})
	return res.RowsAffected > 0, res.Error
}
