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

type TemplateVersionRepository struct {
	db *gorm.DB
}

func NewTemplateVersionRepository(db *gorm.DB) *TemplateVersionRepository {
	return &TemplateVersionRepository{db: db}
}

func (r *TemplateVersionRepository) Create(v *models.TemplateVersion) error {
	return r.db.Create(v).Error
}

func (r *TemplateVersionRepository) FindByID(id uint) (*models.TemplateVersion, error) {
	var v models.TemplateVersion
	if err := r.db.Preload("StyleSheet").First(&v, id).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

func (r *TemplateVersionRepository) FindByTemplateID(templateID uint) ([]models.TemplateVersion, error) {
	var versions []models.TemplateVersion
	if err := r.db.Preload("StyleSheet").Preload("Localizations").
		Where("template_id = ?", templateID).
		Order("version DESC").
		Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

func (r *TemplateVersionRepository) NextVersion(templateID uint) (int, error) {
	var max int
	err := r.db.Model(&models.TemplateVersion{}).
		Where("template_id = ?", templateID).
		Select("COALESCE(MAX(version), 0)").
		Scan(&max).Error
	return max + 1, err
}

func (r *TemplateVersionRepository) Update(v *models.TemplateVersion) error {
	return r.db.Save(v).Error
}

func (r *TemplateVersionRepository) Delete(id uint) error {
	return r.db.Delete(&models.TemplateVersion{}, id).Error
}
