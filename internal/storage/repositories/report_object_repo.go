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
	"context"
	"errors"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DMARCSentReportRepository records and reads the RFC 7489 aggregate reports
// this deployment dispatched (SF-25 send side; read surface SF-57). The
// unique report_id makes a re-run of the same window idempotent.
type DMARCSentReportRepository struct {
	db *gorm.DB
}

func NewDMARCSentReportRepository(db *gorm.DB) *DMARCSentReportRepository {
	return &DMARCSentReportRepository{db: db}
}

// Record inserts one sent-report row; a re-run of the same window + domain
// (same deterministic report id) is a no-op.
func (r *DMARCSentReportRepository) Record(row *models.DMARCSentReport) error {
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

// FindRecentByDomains returns the newest sent reports whose reported domain
// is one of names (the tenant's own domain set — the reports about the
// tenant's domains are the tenant's to see), newest first, bounded.
func (r *DMARCSentReportRepository) FindRecentByDomains(ctx context.Context, names []string, limit int) ([]models.DMARCSentReport, error) {
	var rows []models.DMARCSentReport
	if len(names) == 0 {
		return rows, nil
	}
	if limit <= 0 {
		limit = 50
	}
	err := r.db.WithContext(ctx).
		Where("reported_domain IN ?", names).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// LatestSentAt returns the newest sent (or attempted) report timestamp for
// the reported domain — the SF-63 interval gate's reader. nil = nothing sent
// for the domain yet.
func (r *DMARCSentReportRepository) LatestSentAt(ctx context.Context, domain string) (*time.Time, error) {
	var row models.DMARCSentReport
	err := r.db.WithContext(ctx).
		Where("reported_domain = ?", domain).
		Order("created_at DESC").
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row.CreatedAt, nil
}

// FindAll returns the newest sent reports deployment-wide (the admin
// surface), newest first, bounded.
func (r *DMARCSentReportRepository) FindAll(ctx context.Context, limit int) ([]models.DMARCSentReport, error) {
	var rows []models.DMARCSentReport
	if limit <= 0 {
		limit = 50
	}
	err := r.db.WithContext(ctx).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// ARFReportRepository records and reads the RFC 5965/6591 auth-failure
// feedback reports this deployment dispatched (SF-26 send side; read surface
// SF-57). The unique report_id makes a re-run idempotent.
type ARFReportRepository struct {
	db *gorm.DB
}

func NewARFReportRepository(db *gorm.DB) *ARFReportRepository {
	return &ARFReportRepository{db: db}
}

// Record inserts one sent-report row; a re-run of the same message + family
// (same deterministic report id) is a no-op.
func (r *ARFReportRepository) Record(row *models.ARFReport) error {
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

// FindByScope returns the newest ARF reports attributed to the tenant's
// scope (the received message's owner), newest first, bounded.
func (r *ARFReportRepository) FindByScope(ctx context.Context, scope ResourceScope, limit int) ([]models.ARFReport, error) {
	var rows []models.ARFReport
	if limit <= 0 {
		limit = 50
	}
	err := ApplyScope(r.db.WithContext(ctx), scope).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// FindAll returns the newest ARF reports deployment-wide (the admin
// surface), newest first, bounded.
func (r *ARFReportRepository) FindAll(ctx context.Context, limit int) ([]models.ARFReport, error) {
	var rows []models.ARFReport
	if limit <= 0 {
		limit = 50
	}
	err := r.db.WithContext(ctx).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}
