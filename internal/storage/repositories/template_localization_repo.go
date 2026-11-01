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

type TemplateLocalizationRepository struct {
	db *gorm.DB
}

func NewTemplateLocalizationRepository(db *gorm.DB) *TemplateLocalizationRepository {
	return &TemplateLocalizationRepository{db: db}
}

func (r *TemplateLocalizationRepository) Create(l *models.TemplateLocalization) error {
	return r.db.Create(l).Error
}

func (r *TemplateLocalizationRepository) Update(l *models.TemplateLocalization) error {
	return r.db.Save(l).Error
}

func (r *TemplateLocalizationRepository) FindByID(id uint) (*models.TemplateLocalization, error) {
	var l models.TemplateLocalization
	if err := r.db.First(&l, id).Error; err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *TemplateLocalizationRepository) FindByVersionID(versionID uint) ([]models.TemplateLocalization, error) {
	var localizations []models.TemplateLocalization
	if err := r.db.Where("version_id = ?", versionID).
		Order("language ASC").
		Find(&localizations).Error; err != nil {
		return nil, err
	}
	return localizations, nil
}

func (r *TemplateLocalizationRepository) FindByVersionAndLanguage(versionID uint, language string) (*models.TemplateLocalization, error) {
	var l models.TemplateLocalization
	if err := r.db.Where("version_id = ? AND language = ?", versionID, language).First(&l).Error; err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *TemplateLocalizationRepository) Delete(id uint) error {
	return r.db.Delete(&models.TemplateLocalization{}, id).Error
}
