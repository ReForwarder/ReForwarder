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

// IncomingWebhookEventRepository persists the raw payloads of HTTP callbacks
// the platform received, captured before processing.
type IncomingWebhookEventRepository struct {
	db *gorm.DB
}

func NewIncomingWebhookEventRepository(db *gorm.DB) *IncomingWebhookEventRepository {
	return &IncomingWebhookEventRepository{db: db}
}

func (r *IncomingWebhookEventRepository) Create(event *models.IncomingWebhookEvent) error {
	return r.db.Create(event).Error
}

// Get returns a single stored webhook payload by its primary key.
func (r *IncomingWebhookEventRepository) Get(id uint) (*models.IncomingWebhookEvent, error) {
	var event models.IncomingWebhookEvent
	if err := r.db.First(&event, id).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

// ListFiltered returns a page of stored payloads narrowed by source, type,
// recipient and message uuid (empty filters are ignored), newest first.
func (r *IncomingWebhookEventRepository) ListFiltered(page, size int, source, eventType, recipient, emailUUID string) ([]models.IncomingWebhookEvent, int64, error) {
	limit, offset := normalizeSecurityPage(page, size)

	where := func(q *gorm.DB) *gorm.DB {
		if source != "" {
			q = q.Where("source = ?", source)
		}
		if eventType != "" {
			q = q.Where("type = ?", eventType)
		}
		if recipient != "" {
			q = q.Where("recipient ILIKE ?", "%"+recipient+"%")
		}
		if emailUUID != "" {
			q = q.Where("email_uuid = ?", emailUUID)
		}
		return q
	}

	var total int64
	if err := where(r.db.Model(&models.IncomingWebhookEvent{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var events []models.IncomingWebhookEvent
	if err := where(r.db.Model(&models.IncomingWebhookEvent{})).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// SetProcessError records why a stored payload could not be processed.
func (r *IncomingWebhookEventRepository) SetProcessError(id uint, processErr string) error {
	return r.db.Model(&models.IncomingWebhookEvent{}).
		Where("id = ?", id).
		Update("process_error", processErr).Error
}

// DeleteOlderThan deletes stored payloads older than the given time. Returns
// the number of rows deleted.
func (r *IncomingWebhookEventRepository) DeleteOlderThan(before time.Time) (int64, error) {
	result := r.db.Where("created_at < ?", before).Delete(&models.IncomingWebhookEvent{})
	return result.RowsAffected, result.Error
}
