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

type WorkspaceSSORepository struct {
	db *gorm.DB
}

func NewWorkspaceSSORepository(db *gorm.DB) *WorkspaceSSORepository {
	return &WorkspaceSSORepository{db: db}
}

func (r *WorkspaceSSORepository) FindByWorkspaceID(wsID uint) (*models.WorkspaceSSOConfig, error) {
	var c models.WorkspaceSSOConfig
	if err := r.db.Preload("Provider").Where("workspace_id = ?", wsID).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *WorkspaceSSORepository) Upsert(c *models.WorkspaceSSOConfig) error {
	var existing models.WorkspaceSSOConfig
	if err := r.db.Where("workspace_id = ?", c.WorkspaceID).First(&existing).Error; err != nil {
		return r.db.Create(c).Error
	}
	existing.ProviderID = c.ProviderID
	existing.EnforceSSO = c.EnforceSSO
	existing.AutoProvision = c.AutoProvision
	existing.AllowedDomains = c.AllowedDomains
	return r.db.Save(&existing).Error
}

func (r *WorkspaceSSORepository) Delete(wsID uint) error {
	return r.db.Where("workspace_id = ?", wsID).Delete(&models.WorkspaceSSOConfig{}).Error
}
