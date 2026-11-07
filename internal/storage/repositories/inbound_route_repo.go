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

// InboundRouteRepository persists inbound routing rules (OB-2, ADR-002
// decision 1). Management methods are workspace-scoped through ResourceScope;
// the engine-facing lookups key on the already-verified domain ID (routing
// rules resolve only through tenant-verified domains).
type InboundRouteRepository struct {
	db *gorm.DB
}

func NewInboundRouteRepository(db *gorm.DB) *InboundRouteRepository {
	return &InboundRouteRepository{db: db}
}

// Create persists a new route.
func (r *InboundRouteRepository) Create(route *models.InboundRoute) error {
	return r.db.Create(route).Error
}

// Update saves a route.
func (r *InboundRouteRepository) Update(route *models.InboundRoute) error {
	return r.db.Save(route).Error
}

// FindByID returns one route within the caller's workspace scope.
func (r *InboundRouteRepository) FindByID(scope ResourceScope, id uint) (*models.InboundRoute, error) {
	var route models.InboundRoute
	if err := ApplyScope(r.db, scope).First(&route, id).Error; err != nil {
		return nil, err
	}
	return &route, nil
}

// ListByScope returns the workspace's routes with a total count, ordered by
// priority (lower first), then id.
func (r *InboundRouteRepository) ListByScope(scope ResourceScope, limit, offset int) ([]models.InboundRoute, int64, error) {
	var items []models.InboundRoute
	var total int64

	if err := ApplyScope(r.db.Model(&models.InboundRoute{}), scope).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := ApplyScope(r.db, scope).
		Order("priority ASC, id ASC").
		Limit(limit).Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Delete removes a route within the caller's workspace scope.
func (r *InboundRouteRepository) Delete(scope ResourceScope, id uint) error {
	return ApplyScope(r.db, scope).Delete(&models.InboundRoute{}, id).Error
}

// ListByDomain returns every route of a domain ordered by priority (lower
// first), then id. Engine-facing: the caller owns the domain row and has
// already established it is tenant-verified.
func (r *InboundRouteRepository) ListByDomain(domainID uint) ([]models.InboundRoute, error) {
	var items []models.InboundRoute
	if err := r.db.
		Where("domain_id = ?", domainID).
		Order("priority ASC, id ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListEnabledByDomain returns the domain's enabled routes, ordered by
// priority (lower first), then id — the engine's evaluation list.
func (r *InboundRouteRepository) ListEnabledByDomain(domainID uint) ([]models.InboundRoute, error) {
	var items []models.InboundRoute
	if err := r.db.
		Where("domain_id = ? AND enabled = ?", domainID, true).
		Order("priority ASC, id ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}
