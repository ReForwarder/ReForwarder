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

// CronRunHistoryRepository persists the cron run history (SF-99): one row per
// completed cron job run, written by the cron manager's wrapper and read
// through the admin-gated history endpoint. Deployment-wide operational data
// — no tenant scoping applies (cron jobs are not tenant objects).
type CronRunHistoryRepository struct {
	db *gorm.DB
}

func NewCronRunHistoryRepository(db *gorm.DB) *CronRunHistoryRepository {
	return &CronRunHistoryRepository{db: db}
}

// RecordCronRun persists one completed-run row; implements the cron
// manager's RunHistoryRecorder seam (SF-99). Best-effort on the caller's
// side: a recording failure is logged by the manager, never blocks a run.
func (r *CronRunHistoryRepository) RecordCronRun(entry *models.CronRunHistory) error {
	return r.db.Create(entry).Error
}

// ListPaged returns one page of run history, newest first, optionally
// narrowed to one job name, with the total row count for the pager. page is
// 0-based; offset = page*size.
func (r *CronRunHistoryRepository) ListPaged(jobName string, page, size int) ([]models.CronRunHistory, int64, error) {
	return r.ListPagedOffset(jobName, page*size, size)
}

// ListPagedOffset is ListPaged with the offset computed by the caller (the
// handler's normalizePageParams owns the page/size normalization).
func (r *CronRunHistoryRepository) ListPagedOffset(jobName string, offset, size int) ([]models.CronRunHistory, int64, error) {
	q := r.db.Model(&models.CronRunHistory{})
	if jobName != "" {
		q = q.Where("job_name = ?", jobName)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []models.CronRunHistory
	err := q.
		Order("started_at DESC, id DESC").
		Offset(offset).
		Limit(size).
		Find(&out).Error
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
