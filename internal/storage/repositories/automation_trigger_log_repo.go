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
	"gorm.io/gorm/clause"
)

type AutomationTriggerLogRepository struct {
	db *gorm.DB
}

func NewAutomationTriggerLogRepository(db *gorm.DB) *AutomationTriggerLogRepository {
	return &AutomationTriggerLogRepository{db: db}
}

// Insert records one (automation, subscriber) firing and reports whether a new
// row was stored. The (automation_id, subscriber_id) unique index dedups
// once-frequency enrollments: on a conflict the insert is skipped and
// (false, nil) is returned, so the caller skips the enrollment without treating
// the duplicate as an error — and two racing trigger events still enroll the
// contact exactly once.
func (r *AutomationTriggerLogRepository) Insert(automationID, subscriberID uint) (bool, error) {
	row := models.AutomationTriggerLog{
		AutomationID: automationID,
		SubscriberID: subscriberID,
		TriggeredAt:  time.Now(),
	}
	result := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// Exists reports whether the (automation, subscriber) pair has already fired —
// the pre-check behind the insert dedup.
func (r *AutomationTriggerLogRepository) Exists(automationID, subscriberID uint) (bool, error) {
	var count int64
	if err := r.db.Model(&models.AutomationTriggerLog{}).
		Where("automation_id = ? AND subscriber_id = ?", automationID, subscriberID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
