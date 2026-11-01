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
	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

type SettingRepository struct {
	db *gorm.DB
}

func NewSettingRepository(db *gorm.DB) *SettingRepository {
	return &SettingRepository{db: db}
}

func (r *SettingRepository) FindAll() ([]models.Setting, error) {
	var settings []models.Setting
	if err := r.db.Order("key ASC").Find(&settings).Error; err != nil {
		return nil, err
	}
	return settings, nil
}

func (r *SettingRepository) FindByKey(key string) (*models.Setting, error) {
	var setting models.Setting
	if err := r.db.Where("key = ?", key).First(&setting).Error; err != nil {
		return nil, err
	}
	return &setting, nil
}

func (r *SettingRepository) Upsert(setting *models.Setting) error {
	var existing models.Setting
	result := r.db.Where("key = ?", setting.Key).First(&existing)
	if result.Error == nil {
		existing.Value = setting.Value
		existing.Type = setting.Type
		return r.db.Save(&existing).Error
	}
	return r.db.Create(setting).Error
}

func (r *SettingRepository) BulkUpsert(settings []models.Setting) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		repo := &SettingRepository{db: tx}
		for i := range settings {
			if err := repo.Upsert(&settings[i]); err != nil {
				return err
			}
		}
		return nil
	})
}
