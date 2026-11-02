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

// EmailEventRepository persists the message-history event timeline.
type EmailEventRepository struct {
	db *gorm.DB
}

func NewEmailEventRepository(db *gorm.DB) *EmailEventRepository {
	return &EmailEventRepository{db: db}
}

// Create inserts an email event and reports whether a new row was stored. The
// (email_uuid, type, provider_event_id) unique index dedups provider replays
// and duplicate callbacks: on a conflict the insert is skipped and
// (false, nil) is returned, so callers can move on without treating the
// duplicate as an error.
func (r *EmailEventRepository) Create(event *models.EmailEvent) (bool, error) {
	if event.Metadata == "" {
		// jsonb rejects '' — persist the structural default instead.
		event.Metadata = "{}"
	}
	result := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(event)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ListByEmailUUID returns the full event timeline of one message, oldest first.
func (r *EmailEventRepository) ListByEmailUUID(uuid string) ([]models.EmailEvent, error) {
	var events []models.EmailEvent
	if err := r.db.Where("email_uuid = ?", uuid).
		Order("occurred_at ASC").
		Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

// ListByEmailUUIDs returns the events of several messages in one query — the
// batch lookup behind the message-history list, which decorates each slim row
// with its timeline without an N+1 round trip. Ordered oldest first within
// each message.
func (r *EmailEventRepository) ListByEmailUUIDs(uuids []string) ([]models.EmailEvent, error) {
	if len(uuids) == 0 {
		return nil, nil
	}
	var events []models.EmailEvent
	if err := r.db.Where("email_uuid IN ?", uuids).
		Order("occurred_at ASC").
		Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

// ListFiltered returns a page of events narrowed by workspace, type, message
// and an optional occurred-at range (nil bounds are ignored), newest first.
func (r *EmailEventRepository) ListFiltered(page, size int, workspaceID *uint, types []string, emailUUID string, from, to *time.Time) ([]models.EmailEvent, int64, error) {
	limit, offset := normalizeSecurityPage(page, size)

	where := func(q *gorm.DB) *gorm.DB {
		if workspaceID != nil {
			q = q.Where("workspace_id = ?", *workspaceID)
		}
		if len(types) > 0 {
			q = q.Where("type IN ?", types)
		}
		if emailUUID != "" {
			q = q.Where("email_uuid = ?", emailUUID)
		}
		if from != nil {
			q = q.Where("occurred_at >= ?", *from)
		}
		if to != nil {
			q = q.Where("occurred_at < ?", *to)
		}
		return q
	}

	var total int64
	if err := where(r.db.Model(&models.EmailEvent{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var events []models.EmailEvent
	if err := where(r.db.Model(&models.EmailEvent{})).
		Order("occurred_at DESC").
		Limit(limit).Offset(offset).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// DeleteOlderThan deletes events that occurred before the cutoff. Returns the
// number of rows deleted.
func (r *EmailEventRepository) DeleteOlderThan(before time.Time) (int64, error) {
	result := r.db.Where("occurred_at < ?", before).Delete(&models.EmailEvent{})
	return result.RowsAffected, result.Error
}
