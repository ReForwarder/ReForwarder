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

// TransportPolicyRepository persists the per-domain transport policy rules
// (SF-50, ADR-001 amendment). Workspace-scoped: every read and write is
// bound to the owning workspace, and the bound domain name is validated
// against the workspace's own domains by the admin surface at write time.
type TransportPolicyRepository struct {
	db *gorm.DB
}

func NewTransportPolicyRepository(db *gorm.DB) *TransportPolicyRepository {
	return &TransportPolicyRepository{db: db}
}

// Create persists one policy row.
func (r *TransportPolicyRepository) Create(policy *models.TransportPolicy) error {
	return r.db.Create(policy).Error
}

// Update saves policy state (the PATCH path).
func (r *TransportPolicyRepository) Update(policy *models.TransportPolicy) error {
	return r.db.Save(policy).Error
}

// FindByIDForWorkspace returns one of the workspace's policies, or
// gorm.ErrRecordNotFound (a foreign policy reads as missing — isolation).
func (r *TransportPolicyRepository) FindByIDForWorkspace(id, workspaceID uint) (*models.TransportPolicy, error) {
	var policy models.TransportPolicy
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&policy).Error; err != nil {
		return nil, err
	}
	return &policy, nil
}

// ListForWorkspaceDomain returns the workspace's policies bound to one
// domain name, evaluation order (priority ASC, id ASC).
func (r *TransportPolicyRepository) ListForWorkspaceDomain(workspaceID uint, domainName string) ([]models.TransportPolicy, error) {
	var out []models.TransportPolicy
	if err := r.db.Where("workspace_id = ? AND domain_name = ?", workspaceID, domainName).
		Order("priority ASC, id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteForWorkspace removes one of the workspace's policies (scoped;
// returns the affected-row count).
func (r *TransportPolicyRepository) DeleteForWorkspace(id, workspaceID uint) (int64, error) {
	res := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).Delete(&models.TransportPolicy{})
	return res.RowsAffected, res.Error
}

// ListEnabledForDomain returns the ENABLED policies bound to one domain
// name for one workspace, evaluation order — the resolver's read (the MTA
// gate points load exactly this set).
func (r *TransportPolicyRepository) ListEnabledForDomain(workspaceID uint, domainName string) ([]models.TransportPolicy, error) {
	var out []models.TransportPolicy
	if err := r.db.Where("workspace_id = ? AND domain_name = ? AND enabled = ?", workspaceID, domainName, true).
		Order("priority ASC, id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
