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

// SecurityBanRepository persists and queries IP bans.
type SecurityBanRepository struct {
	db *gorm.DB
}

func NewSecurityBanRepository(db *gorm.DB) *SecurityBanRepository {
	return &SecurityBanRepository{db: db}
}

func (r *SecurityBanRepository) Create(ban *models.SecurityBan) error {
	return r.db.Create(ban).Error
}

// FindActiveByIP returns the ban currently in force for an IP address, if any.
// A ban is active while it is permanent (expires_at IS NULL) or its expiry is
// still in the future.
func (r *SecurityBanRepository) FindActiveByIP(ip string) (*models.SecurityBan, error) {
	var ban models.SecurityBan
	if err := r.db.Where("ip_address = ? AND (expires_at IS NULL OR expires_at > NOW())", ip).
		Order("created_at DESC").
		First(&ban).Error; err != nil {
		return nil, err
	}
	return &ban, nil
}

// FindAllActive returns every ban currently in force. It backs the in-memory
// ban list resync performed on startup and after a Redis reconnection.
func (r *SecurityBanRepository) FindAllActive() ([]models.SecurityBan, error) {
	var bans []models.SecurityBan
	if err := r.db.Where("expires_at IS NULL OR expires_at > NOW()").
		Order("created_at DESC").
		Find(&bans).Error; err != nil {
		return nil, err
	}
	return bans, nil
}

// CountByIP counts the bans previously recorded for an IP address — the
// offense history used to escalate ban durations. Rows created after this
// instant (i.e. the ban currently being placed) are not counted.
func (r *SecurityBanRepository) CountByIP(ip string) (int64, error) {
	var total int64
	err := r.db.Model(&models.SecurityBan{}).
		Where("ip_address = ? AND created_at < NOW()", ip).
		Count(&total).Error
	return total, err
}

// FindByID returns a single ban by its primary key.
func (r *SecurityBanRepository) FindByID(id uint) (*models.SecurityBan, error) {
	var ban models.SecurityBan
	if err := r.db.First(&ban, id).Error; err != nil {
		return nil, err
	}
	return &ban, nil
}

// List returns a page of bans, newest first. search matches the IP address;
// filter selects "active" (the default), "expired" or "all".
func (r *SecurityBanRepository) List(page, size int, search, filter string) ([]models.SecurityBan, int64, error) {
	limit, offset := normalizeSecurityPage(page, size)

	where := func(q *gorm.DB) *gorm.DB {
		switch filter {
		case "expired":
			q = q.Where("expires_at IS NOT NULL AND expires_at <= NOW()")
		case "all":
		default: // "active"
			q = q.Where("expires_at IS NULL OR expires_at > NOW()")
		}
		if search != "" {
			q = q.Where("ip_address ILIKE ?", "%"+search+"%")
		}
		return q
	}

	var total int64
	if err := where(r.db.Model(&models.SecurityBan{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var bans []models.SecurityBan
	if err := where(r.db.Model(&models.SecurityBan{})).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&bans).Error; err != nil {
		return nil, 0, err
	}
	return bans, total, nil
}

// UpdateExpiry sets a new expiry for a ban. Lifting a ban passes the current
// time: the row immediately expires but stays as offense history.
func (r *SecurityBanRepository) UpdateExpiry(id uint, expiresAt time.Time) error {
	return r.db.Model(&models.SecurityBan{}).
		Where("id = ?", id).
		Update("expires_at", expiresAt).Error
}

// IncrementOffense adds n to a ban's offense count.
func (r *SecurityBanRepository) IncrementOffense(id uint, n int) error {
	return r.db.Model(&models.SecurityBan{}).
		Where("id = ?", id).
		Update("offense_count", gorm.Expr("offense_count + ?", n)).Error
}

// Update saves every field of the given ban. Whether a removed ban is
// hard-deleted or merely expired in place is a BanService decision; this
// covers the "expire in place" path.
func (r *SecurityBanRepository) Update(ban *models.SecurityBan) error {
	return r.db.Save(ban).Error
}

// CountActive counts the bans currently in force.
func (r *SecurityBanRepository) CountActive() (int64, error) {
	var total int64
	err := r.db.Model(&models.SecurityBan{}).
		Where("expires_at IS NULL OR expires_at > NOW()").
		Count(&total).Error
	return total, err
}

// CountByType counts bans of the given type (automatic|manual) over all time.
func (r *SecurityBanRepository) CountByType(banType string) (int64, error) {
	var total int64
	err := r.db.Model(&models.SecurityBan{}).
		Where("ban_type = ?", banType).
		Count(&total).Error
	return total, err
}

// CountSince counts bans created since the given instant.
func (r *SecurityBanRepository) CountSince(since time.Time) (int64, error) {
	var total int64
	err := r.db.Model(&models.SecurityBan{}).
		Where("created_at >= ?", since).
		Count(&total).Error
	return total, err
}

// normalizeSecurityPage clamps list pagination: 1-based page, a bounded page
// size, and the derived offset.
func normalizeSecurityPage(page, size int) (limit, offset int) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return size, (page - 1) * size
}
