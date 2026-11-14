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

// MailAliasRepository persists mail-account aliases (SF-65): one alias
// address delivering into an existing mailbox. Every read and write is
// owner-scoped (user_id) — a session or a delivery expansion can never see
// or resolve another user's aliases.
type MailAliasRepository struct {
	db *gorm.DB
}

func NewMailAliasRepository(db *gorm.DB) *MailAliasRepository {
	return &MailAliasRepository{db: db}
}

// Create persists one alias row.
func (r *MailAliasRepository) Create(alias *models.MailAlias) error {
	return r.db.Create(alias).Error
}

// FindByIDForUser returns one of the user's aliases, or gorm.ErrRecordNotFound.
func (r *MailAliasRepository) FindByIDForUser(id, userID uint) (*models.MailAlias, error) {
	var alias models.MailAlias
	if err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&alias).Error; err != nil {
		return nil, err
	}
	return &alias, nil
}

// ListByUser returns the user's aliases, oldest first.
func (r *MailAliasRepository) ListByUser(userID uint) ([]models.MailAlias, error) {
	var out []models.MailAlias
	if err := r.db.Where("user_id = ?", userID).Order("id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteForUser removes one of the user's aliases (scoped; returns the
// affected-row count).
func (r *MailAliasRepository) DeleteForUser(id, userID uint) (int64, error) {
	res := r.db.Where("id = ? AND user_id = ?", id, userID).Delete(&models.MailAlias{})
	return res.RowsAffected, res.Error
}

// CountAddress counts how many aliases carry the address (unscoped by user:
// the endpoint's uniqueness check must see another user's alias too — one
// address, one mapping, globally).
func (r *MailAliasRepository) CountAddress(address string) (int64, error) {
	var n int64
	if err := r.db.Model(&models.MailAlias{}).Where("address = ?", address).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// ResolveAliasAddress implements the inbound.AliasGroupResolver seam (SF-65):
// the target mailbox ID for the user's alias at address, ok=false when the
// user owns no such alias. The user scoping is the tenant boundary: another
// user's alias for the same address never resolves here.
func (r *MailAliasRepository) ResolveAliasAddress(userID uint, address string) (uint, bool) {
	var alias models.MailAlias
	if err := r.db.Where("user_id = ? AND address = ?", userID, address).First(&alias).Error; err != nil {
		return 0, false
	}
	return alias.MailboxID, true
}
