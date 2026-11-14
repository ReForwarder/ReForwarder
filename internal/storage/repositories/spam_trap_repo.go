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
	"strings"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// SpamTrapRepository persists the seeded trap addresses (SF-42, the ADR-006
// amendment). Workspace-scoped: every read and write is bound to the owning
// workspace; the bound domain name is validated against the workspace's own
// domains by the admin surface at write time.
type SpamTrapRepository struct {
	db *gorm.DB
}

func NewSpamTrapRepository(db *gorm.DB) *SpamTrapRepository {
	return &SpamTrapRepository{db: db}
}

// Create persists one trap row.
func (r *SpamTrapRepository) Create(trap *models.SpamTrap) error {
	return r.db.Create(trap).Error
}

// FindByIDForWorkspace returns one of the workspace's traps, or
// gorm.ErrRecordNotFound (a foreign trap reads as missing — isolation).
func (r *SpamTrapRepository) FindByIDForWorkspace(id, workspaceID uint) (*models.SpamTrap, error) {
	var trap models.SpamTrap
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&trap).Error; err != nil {
		return nil, err
	}
	return &trap, nil
}

// ListForWorkspaceDomain returns the workspace's traps bound to one domain
// name, oldest first.
func (r *SpamTrapRepository) ListForWorkspaceDomain(workspaceID uint, domainName string) ([]models.SpamTrap, error) {
	var out []models.SpamTrap
	if err := r.db.Where("workspace_id = ? AND domain_name = ?", workspaceID, domainName).
		Order("id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteForWorkspace removes one of the workspace's traps (scoped; returns
// the affected-row count).
func (r *SpamTrapRepository) DeleteForWorkspace(id, workspaceID uint) (int64, error) {
	res := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).Delete(&models.SpamTrap{})
	return res.RowsAffected, res.Error
}

// HasRecipient reports whether a seeded trap exists for the given recipient
// address parts (lowercased here — the caller may pass raw envelope values).
// This is the ingest gate's read: one EXISTS-shaped query per record.
func (r *SpamTrapRepository) HasRecipient(workspaceID uint, domainName, localPart string) (bool, error) {
	var n int64
	if err := r.db.Model(&models.SpamTrap{}).
		Where("workspace_id = ? AND domain_name = ? AND local_part = ?",
			workspaceID, strings.ToLower(domainName), strings.ToLower(localPart)).
		Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
