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

// SecurityAllowlistRepository persists and queries IP/CIDR allowlist entries.
type SecurityAllowlistRepository struct {
	db *gorm.DB
}

func NewSecurityAllowlistRepository(db *gorm.DB) *SecurityAllowlistRepository {
	return &SecurityAllowlistRepository{db: db}
}

func (r *SecurityAllowlistRepository) Create(entry *models.SecurityAllowlist) error {
	return r.db.Create(entry).Error
}

// List returns a page of allowlist entries, newest first.
func (r *SecurityAllowlistRepository) List(page, size int) ([]models.SecurityAllowlist, int64, error) {
	limit, offset := normalizeSecurityPage(page, size)

	var total int64
	if err := r.db.Model(&models.SecurityAllowlist{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var entries []models.SecurityAllowlist
	if err := r.db.Model(&models.SecurityAllowlist{}).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&entries).Error; err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// FindByID returns a single allowlist entry by its primary key.
func (r *SecurityAllowlistRepository) FindByID(id uint) (*models.SecurityAllowlist, error) {
	var entry models.SecurityAllowlist
	if err := r.db.First(&entry, id).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

// FindByCIDR looks an entry up by its exact IP or CIDR string.
func (r *SecurityAllowlistRepository) FindByCIDR(value string) (*models.SecurityAllowlist, error) {
	var entry models.SecurityAllowlist
	if err := r.db.Where("ip_cidr = ?", value).First(&entry).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

// FindAll returns every allowlist entry. It backs the in-memory ban list
// resync performed on startup and after a Redis reconnection.
func (r *SecurityAllowlistRepository) FindAll() ([]models.SecurityAllowlist, error) {
	var entries []models.SecurityAllowlist
	if err := r.db.Order("created_at DESC").Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *SecurityAllowlistRepository) Delete(id uint) error {
	return r.db.Delete(&models.SecurityAllowlist{}, id).Error
}
