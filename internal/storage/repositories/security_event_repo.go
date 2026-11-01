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

// SecurityEventRepository persists and queries security engine events.
type SecurityEventRepository struct {
	db *gorm.DB
}

func NewSecurityEventRepository(db *gorm.DB) *SecurityEventRepository {
	return &SecurityEventRepository{db: db}
}

func (r *SecurityEventRepository) Create(event *models.SecurityEvent) error {
	return r.db.Create(event).Error
}

// List returns a page of security events, newest first, optionally filtered by
// event type and severity (empty filters are ignored).
func (r *SecurityEventRepository) List(page, size int, eventType, severity string) ([]models.SecurityEvent, int64, error) {
	limit, offset := normalizeSecurityPage(page, size)

	where := func(q *gorm.DB) *gorm.DB {
		if eventType != "" {
			q = q.Where("event_type = ?", eventType)
		}
		if severity != "" {
			q = q.Where("severity = ?", severity)
		}
		return q
	}

	var total int64
	if err := where(r.db.Model(&models.SecurityEvent{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var events []models.SecurityEvent
	if err := where(r.db.Model(&models.SecurityEvent{})).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// FindSinceID returns events with an ID greater than id, oldest first — the
// polling query behind the live events stream.
func (r *SecurityEventRepository) FindSinceID(id uint, limit int) ([]models.SecurityEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	var events []models.SecurityEvent
	if err := r.db.Where("id > ?", id).
		Order("id ASC").
		Limit(limit).
		Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

// CountSince counts events created since the given instant.
func (r *SecurityEventRepository) CountSince(since time.Time) (int64, error) {
	var total int64
	err := r.db.Model(&models.SecurityEvent{}).
		Where("created_at >= ?", since).
		Count(&total).Error
	return total, err
}

// SecurityDailyCount is one day of security activity: detection events
// recorded and bans created. Days without activity carry zeroes so a trend
// chart gets a contiguous series.
type SecurityDailyCount struct {
	Date   string `json:"date"`
	Events int    `json:"events"`
	Bans   int    `json:"bans"`
}

// TrendCounts returns per-day counts of security events and ban creations for
// the last days days (UTC dates), merged into one date-ordered series.
func (r *SecurityEventRepository) TrendCounts(days int) ([]SecurityDailyCount, error) {
	if days <= 0 {
		days = 14
	}
	start := time.Now().UTC().Truncate(24 * time.Hour).AddDate(0, 0, -(days - 1))

	type datedCount struct {
		Date  string
		Count int64
	}
	var eventRows, banRows []datedCount
	if err := r.db.Table("security_events").
		Select("TO_CHAR(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') as date, COUNT(*) as count").
		Where("created_at >= ?", start).
		Group("date").
		Find(&eventRows).Error; err != nil {
		return nil, err
	}
	if err := r.db.Table("security_bans").
		Select("TO_CHAR(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') as date, COUNT(*) as count").
		Where("created_at >= ?", start).
		Group("date").
		Find(&banRows).Error; err != nil {
		return nil, err
	}

	out := make([]SecurityDailyCount, 0, days)
	for i := 0; i < days; i++ {
		out = append(out, SecurityDailyCount{Date: start.AddDate(0, 0, i).Format("2006-01-02")})
	}
	byDate := make(map[string]*SecurityDailyCount, days)
	for i := range out {
		byDate[out[i].Date] = &out[i]
	}
	for _, row := range eventRows {
		if dc, ok := byDate[row.Date]; ok {
			dc.Events = int(row.Count)
		}
	}
	for _, row := range banRows {
		if dc, ok := byDate[row.Date]; ok {
			dc.Bans = int(row.Count)
		}
	}
	return out, nil
}

// DeleteOlderThan removes events recorded before the given instant (retention)
// and returns the number of rows removed.
func (r *SecurityEventRepository) DeleteOlderThan(before time.Time) (int64, error) {
	result := r.db.Where("created_at < ?", before).Delete(&models.SecurityEvent{})
	return result.RowsAffected, result.Error
}
