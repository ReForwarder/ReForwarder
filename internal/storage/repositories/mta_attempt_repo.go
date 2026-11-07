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

// MTADeliveryAttemptRepository persists the per-attempt transport records of
// direct-to-MX delivery (ADR-001 §5). Append-only: rows are created, never
// updated, so the attempt log stays an accurate trace even when a later status
// update races it.
type MTADeliveryAttemptRepository struct {
	db *gorm.DB
}

func NewMTADeliveryAttemptRepository(db *gorm.DB) *MTADeliveryAttemptRepository {
	return &MTADeliveryAttemptRepository{db: db}
}

// Create inserts one attempt row.
func (r *MTADeliveryAttemptRepository) Create(a *models.MTADeliveryAttempt) error {
	return r.db.Create(a).Error
}

// RecordAttempt persists one attempt record (the mta.AttemptRecorder
// interface the delivery executor consumes).
func (r *MTADeliveryAttemptRepository) RecordAttempt(a models.MTADeliveryAttempt) error {
	return r.db.Create(&a).Error
}

// FindByEmailID returns an email's attempt rows, oldest first.
func (r *MTADeliveryAttemptRepository) FindByEmailID(emailID uint, limit, offset int) ([]models.MTADeliveryAttempt, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []models.MTADeliveryAttempt
	if err := r.db.Where("email_id = ?", emailID).
		Order("attempt ASC, id ASC").
		Limit(limit).Offset(offset).
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// NextAttemptNumber returns the next attempt ordinal for an email: the count
// of recorded attempts plus one. The executor stamps it onto every row it
// writes so the (email_id, attempt) index stays gapless even across re-enqueues.
func (r *MTADeliveryAttemptRepository) NextAttemptNumber(emailID uint) int {
	var count int64
	r.db.Model(&models.MTADeliveryAttempt{}).Where("email_id = ?", emailID).Count(&count)
	return int(count) + 1
}
