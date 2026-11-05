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
)

// This file adds the R-06 webhook-replay queries. They live in their own file
// (rather than in webhook_delivery_repo.go / webhook_delivery_attempt_repo.go)
// so the replay feature's surface stays reviewable as one unit; the methods
// extend the existing repositories.

// FindByID returns one webhook delivery by primary key. The caller is
// responsible for the scope/ownership check.
func (r *WebhookDeliveryRepository) FindByID(id uint) (*models.WebhookDelivery, error) {
	var d models.WebhookDelivery
	if err := r.db.First(&d, id).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// UpdateReplayOutcome rewrites the delivery's final outcome after an
// operator-initiated replay. Attempt is deliberately untouched: it keeps the
// ORIGINAL delivery's attempt count, and replay attempts are the attempt rows
// numbered above it — which is exactly what the replay rate limit counts.
func (r *WebhookDeliveryRepository) UpdateReplayOutcome(id uint, status models.WebhookDeliveryStatus, httpStatus int, errMsg string) error {
	return r.db.Model(&models.WebhookDelivery{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":           status,
		"http_status_code": httpStatus,
		"error_message":    errMsg,
	}).Error
}

// MaxAttemptNumber returns the highest attempt number recorded for a delivery
// (original retries plus any replays), so a replay can continue the numbering
// at prior max + 1. A delivery with no rows yields 0.
func (r *WebhookDeliveryAttemptRepository) MaxAttemptNumber(deliveryID uint) (int, error) {
	var max *int
	if err := r.db.Model(&models.WebhookDeliveryAttempt{}).
		Where("delivery_id = ?", deliveryID).
		Select("MAX(attempt) AS attempt").
		Scan(&max).Error; err != nil {
		return 0, err
	}
	if max == nil {
		return 0, nil
	}
	return *max, nil
}

// CountReplaysSince counts the replay attempt rows of one delivery created
// after `since`. Original attempts are always numbered <= the delivery's
// Attempt field, and every replay appends exactly one row numbered above it,
// so counting rows with attempt > afterAttempt counts replay calls precisely.
// The webhook-replay handler uses this as the per-delivery hourly cap.
func (r *WebhookDeliveryAttemptRepository) CountReplaysSince(deliveryID uint, afterAttempt int, since time.Time) (int64, error) {
	var count int64
	err := r.db.Model(&models.WebhookDeliveryAttempt{}).
		Where("delivery_id = ? AND attempt > ? AND created_at >= ?", deliveryID, afterAttempt, since).
		Count(&count).Error
	return count, err
}
