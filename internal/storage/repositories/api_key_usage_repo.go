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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// APIKeyUsageRepository keeps the per-day request/send counters of the API
// keys (R-18). The auth middleware Bumps one row per key per day (upsert);
// the API-key detail view reads the trailing window.
type APIKeyUsageRepository struct {
	db *gorm.DB
}

func NewAPIKeyUsageRepository(db *gorm.DB) *APIKeyUsageRepository {
	return &APIKeyUsageRepository{db: db}
}

// Bump increments a key's daily request counter by requests and its send
// counter by sends (R-18). The row is keyed on (api_key_id, UTC day); the
// upsert makes concurrent request goroutines additive rather than
// last-write-wins. Errors are returned; the middleware logs nothing and drops
// them — usage analytics must never fail a request.
func (r *APIKeyUsageRepository) Bump(apiKeyID uint, requests, sends int) error {
	if requests <= 0 && sends <= 0 {
		return nil
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "api_key_id"}, {Name: "day"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"requests": clause.Expr{SQL: "api_key_usage_daily.requests + ?", Vars: []interface{}{requests}},
			"sends":    clause.Expr{SQL: "api_key_usage_daily.sends + ?", Vars: []interface{}{sends}},
		}),
	}).Create(&models.APIKeyUsageDaily{
		APIKeyID: apiKeyID,
		Day:      day,
		Requests: int64(requests),
		Sends:    int64(sends),
	}).Error
}

// List returns a key's daily usage for the trailing `days` days (UTC days, in
// ascending chronological order: oldest first, today last), zero-padded: days
// with no row still appear with zero counts so the UI can draw a gap-free
// series.
func (r *APIKeyUsageRepository) List(apiKeyID uint, days int) ([]models.APIKeyUsageDaily, error) {
	if days <= 0 {
		days = 30
	}
	since := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	var rows []models.APIKeyUsageDaily
	if err := r.db.Where("api_key_id = ? AND day >= ?", apiKeyID, since).
		Order("day DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	byDay := make(map[string]models.APIKeyUsageDaily, len(rows))
	for _, row := range rows {
		byDay[row.Day.Format("2006-01-02")] = row
	}
	out := make([]models.APIKeyUsageDaily, 0, days)
	for i := 0; i < days; i++ {
		day := since.AddDate(0, 0, i)
		key := day.Format("2006-01-02")
		if row, ok := byDay[key]; ok {
			out = append(out, row)
			continue
		}
		out = append(out, models.APIKeyUsageDaily{APIKeyID: apiKeyID, Day: day})
	}
	return out, nil
}

// UsageTotals aggregates a key's requests and sends since `since` (inclusive).
type UsageTotals struct {
	Requests int64 `json:"requests"`
	Sends    int64 `json:"sends"`
}

// Totals sums a key's counters over the window.
func (r *APIKeyUsageRepository) Totals(apiKeyID uint, since time.Time) (UsageTotals, error) {
	var totals UsageTotals
	err := r.db.Model(&models.APIKeyUsageDaily{}).
		Select("COALESCE(SUM(requests), 0) AS requests, COALESCE(SUM(sends), 0) AS sends").
		Where("api_key_id = ? AND day >= ?", apiKeyID, since).
		Scan(&totals).Error
	return totals, err
}
