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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// SettingHistoryRepository appends to and reads the per-key settings change
// history (SF-100). History rows are append-only: nothing in this repository
// updates or deletes them.
type SettingHistoryRepository struct {
	db *gorm.DB
}

func NewSettingHistoryRepository(db *gorm.DB) *SettingHistoryRepository {
	return &SettingHistoryRepository{db: db}
}

// Create appends one history entry.
func (r *SettingHistoryRepository) Create(entry *models.SettingHistory) error {
	return r.db.Create(entry).Error
}

// CreateBatch appends several history entries in one transaction — the
// per-key diff of a single settings write lands atomically or not at all.
func (r *SettingHistoryRepository) CreateBatch(entries []models.SettingHistory) error {
	if len(entries) == 0 {
		return nil
	}
	return r.db.Create(&entries).Error
}

// FindByID returns one history entry.
func (r *SettingHistoryRepository) FindByID(id uint) (*models.SettingHistory, error) {
	var entry models.SettingHistory
	if err := r.db.First(&entry, id).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

// ListByKey returns a page of history entries for one key, newest first,
// using the shared limit/offset paging convention (limit is clamped to 100).
// workspaceID scopes the lookup: nil targets platform keys (the only keys the
// platform settings setter writes today), a non-nil id filters to that
// workspace's rows.
func (r *SettingHistoryRepository) ListByKey(key string, workspaceID *uint, limit, offset int) ([]models.SettingHistory, int64, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	where := func(q *gorm.DB) *gorm.DB {
		q = q.Where("key = ?", key)
		if workspaceID == nil {
			q = q.Where("workspace_id IS NULL")
		} else {
			q = q.Where("workspace_id = ?", *workspaceID)
		}
		return q
	}

	var total int64
	if err := where(r.db.Model(&models.SettingHistory{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var entries []models.SettingHistory
	if err := where(r.db).
		Order("created_at DESC").
		Order("id DESC").
		Limit(limit).Offset(offset).
		Find(&entries).Error; err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// LatestByKey returns the most recent history entry for one key, or
// gorm.ErrRecordNotFound when the key has never been written.
func (r *SettingHistoryRepository) LatestByKey(key string, workspaceID *uint) (*models.SettingHistory, error) {
	entries, _, err := r.ListByKey(key, workspaceID, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &entries[0], nil
}
