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

// WebhookDeliveryAttemptRepository persists the per-attempt history of
// outgoing webhook deliveries.
type WebhookDeliveryAttemptRepository struct {
	db *gorm.DB
}

func NewWebhookDeliveryAttemptRepository(db *gorm.DB) *WebhookDeliveryAttemptRepository {
	return &WebhookDeliveryAttemptRepository{db: db}
}

func (r *WebhookDeliveryAttemptRepository) Create(attempt *models.WebhookDeliveryAttempt) error {
	return r.db.Create(attempt).Error
}

// ListByDeliveryID returns every attempt of one delivery, in attempt order.
func (r *WebhookDeliveryAttemptRepository) ListByDeliveryID(deliveryID uint) ([]models.WebhookDeliveryAttempt, error) {
	var attempts []models.WebhookDeliveryAttempt
	if err := r.db.Where("delivery_id = ?", deliveryID).
		Order("attempt ASC, created_at ASC").
		Find(&attempts).Error; err != nil {
		return nil, err
	}
	return attempts, nil
}

// CountByDeliveryIDs returns the number of recorded attempts per delivery for
// the given delivery ids — the per-row summary the outgoing-webhook list
// attaches to each delivery. Deliveries absent from the result have no
// recorded attempts.
func (r *WebhookDeliveryAttemptRepository) CountByDeliveryIDs(deliveryIDs []uint) (map[uint]int64, error) {
	counts := make(map[uint]int64, len(deliveryIDs))
	if len(deliveryIDs) == 0 {
		return counts, nil
	}
	var rows []struct {
		DeliveryID uint  `gorm:"column:delivery_id"`
		Total      int64 `gorm:"column:total"`
	}
	if err := r.db.Model(&models.WebhookDeliveryAttempt{}).
		Select("delivery_id, COUNT(*) AS total").
		Where("delivery_id IN ?", deliveryIDs).
		Group("delivery_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[row.DeliveryID] = row.Total
	}
	return counts, nil
}

// DeleteOlderThan deletes attempt rows created before the given time. Returns
// the number of rows deleted. Attempts share the webhook-delivery retention
// window: their parent rows are deleted on the same schedule, so a plain age
// cutoff keeps both tables shrinking in lockstep.
func (r *WebhookDeliveryAttemptRepository) DeleteOlderThan(before time.Time) (int64, error) {
	result := r.db.Where("created_at < ?", before).Delete(&models.WebhookDeliveryAttempt{})
	return result.RowsAffected, result.Error
}
