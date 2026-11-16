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

package repositories

import (
	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// FilterScriptRepository persists the per-mailbox filter scripts (SF-14, the
// ADR-006 amendment). User-scoped: every read and write is bound to the
// owning user (the mail surface's session user).
type FilterScriptRepository struct {
	db *gorm.DB
}

func NewFilterScriptRepository(db *gorm.DB) *FilterScriptRepository {
	return &FilterScriptRepository{db: db}
}

// Create persists one script row.
func (r *FilterScriptRepository) Create(script *models.FilterScript) error {
	return r.db.Create(script).Error
}

// Update saves script state (the PATCH path).
func (r *FilterScriptRepository) Update(script *models.FilterScript) error {
	return r.db.Save(script).Error
}

// FindByIDForUser returns one of the user's scripts, or gorm.ErrRecordNotFound
// (a foreign script reads as missing — isolation).
func (r *FilterScriptRepository) FindByIDForUser(id, userID uint) (*models.FilterScript, error) {
	var script models.FilterScript
	if err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&script).Error; err != nil {
		return nil, err
	}
	return &script, nil
}

// ListForUser returns the user's scripts for one mailbox, evaluation order
// (priority ASC, id ASC).
func (r *FilterScriptRepository) ListForUser(userID, mailboxID uint) ([]models.FilterScript, error) {
	var out []models.FilterScript
	if err := r.db.Where("user_id = ? AND mailbox_id = ?", userID, mailboxID).
		Order("priority ASC, id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListEnabledForMailbox returns the ENABLED scripts bound to one mailbox in
// evaluation order — the delivery evaluation's read.
func (r *FilterScriptRepository) ListEnabledForMailbox(mailboxID uint) ([]models.FilterScript, error) {
	var out []models.FilterScript
	if err := r.db.Where("mailbox_id = ? AND enabled = ?", mailboxID, true).
		Order("priority ASC, id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteForUser removes one of the user's scripts (scoped; returns the
// affected-row count).
func (r *FilterScriptRepository) DeleteForUser(id, userID uint) (int64, error) {
	res := r.db.Where("id = ? AND user_id = ?", id, userID).Delete(&models.FilterScript{})
	return res.RowsAffected, res.Error
}
