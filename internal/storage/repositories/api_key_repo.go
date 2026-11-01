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

type APIKeyRepository struct {
	db *gorm.DB
}

func NewAPIKeyRepository(db *gorm.DB) *APIKeyRepository {
	return &APIKeyRepository{db: db}
}

func (r *APIKeyRepository) Create(key *models.APIKey) error {
	return r.db.Create(key).Error
}

func (r *APIKeyRepository) FindByPrefix(prefix string) ([]models.APIKey, error) {
	var keys []models.APIKey
	if err := r.db.Where("key_prefix = ?", prefix).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

func (r *APIKeyRepository) FindByUserID(userID uint, limit, offset int) ([]models.APIKey, int64, error) {
	var keys []models.APIKey
	var total int64

	r.db.Model(&models.APIKey{}).Where("user_id = ? AND workspace_id IS NULL", userID).Count(&total)

	if err := r.db.Where("user_id = ? AND workspace_id IS NULL", userID).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&keys).Error; err != nil {
		return nil, 0, err
	}
	return keys, total, nil
}

func (r *APIKeyRepository) FindByWorkspaceID(workspaceID uint, limit, offset int) ([]models.APIKey, int64, error) {
	var keys []models.APIKey
	var total int64

	r.db.Model(&models.APIKey{}).Where("workspace_id = ?", workspaceID).Count(&total)

	if err := r.db.Where("workspace_id = ?", workspaceID).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&keys).Error; err != nil {
		return nil, 0, err
	}
	return keys, total, nil
}

func (r *APIKeyRepository) FindByScope(scope ResourceScope, limit, offset int) ([]models.APIKey, int64, error) {
	var items []models.APIKey
	var total int64

	ApplyScope(r.db.Model(&models.APIKey{}), scope).Count(&total)

	if err := ApplyScope(r.db, scope).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *APIKeyRepository) FindByID(id uint) (*models.APIKey, error) {
	var key models.APIKey
	if err := r.db.First(&key, id).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

func (r *APIKeyRepository) FindByIDWithCreator(id uint) (*models.APIKey, error) {
	var key models.APIKey
	if err := r.db.Preload("CreatedBy").First(&key, id).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

func (r *APIKeyRepository) UpdateLastUsed(id uint) error {
	now := time.Now()
	return r.db.Model(&models.APIKey{}).Where("id = ?", id).Update("last_used_at", now).Error
}

func (r *APIKeyRepository) Revoke(id uint) error {
	return r.db.Model(&models.APIKey{}).Where("id = ?", id).Update("revoked", true).Error
}

func (r *APIKeyRepository) Delete(id uint) error {
	return r.db.Delete(&models.APIKey{}, id).Error
}

func (r *APIKeyRepository) FindAll(limit, offset int) ([]models.APIKey, int64, error) {
	var keys []models.APIKey
	var total int64

	r.db.Model(&models.APIKey{}).Count(&total)

	if err := r.db.Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&keys).Error; err != nil {
		return nil, 0, err
	}
	return keys, total, nil
}
