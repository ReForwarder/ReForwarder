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

type EventRepository struct {
	db *gorm.DB
}

func NewEventRepository(db *gorm.DB) *EventRepository {
	return &EventRepository{db: db}
}

func (r *EventRepository) Create(event *models.Event) error {
	return r.db.Create(event).Error
}

// FindByID returns a single event by its primary key.
func (r *EventRepository) FindByID(id uint) (*models.Event, error) {
	var event models.Event
	if err := r.db.First(&event, id).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

func (r *EventRepository) FindAll(search string, limit, offset int) ([]models.Event, int64, error) {
	var events []models.Event
	var total int64

	countQ := r.db.Model(&models.Event{})
	findQ := r.db.Model(&models.Event{})
	if search != "" {
		like := "%" + search + "%"
		countQ = countQ.Where("type ILIKE ? OR message ILIKE ? OR actor_name ILIKE ?", like, like, like)
		findQ = findQ.Where("type ILIKE ? OR message ILIKE ? OR actor_name ILIKE ?", like, like, like)
	}

	countQ.Count(&total)

	if err := findQ.Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

func (r *EventRepository) FindPersonalByActorAndCategory(actorID uint, category models.EventCategory, limit, offset int) ([]models.Event, int64, error) {
	var events []models.Event
	var total int64

	r.db.Model(&models.Event{}).Where("actor_id = ? AND category = ? AND workspace_id IS NULL", actorID, category).Count(&total)

	if err := r.db.Where("actor_id = ? AND category = ? AND workspace_id IS NULL", actorID, category).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

func (r *EventRepository) FindByCategory(category models.EventCategory, search string, limit, offset int) ([]models.Event, int64, error) {
	var events []models.Event
	var total int64

	countQ := r.db.Model(&models.Event{}).Where("category = ?", category)
	findQ := r.db.Model(&models.Event{}).Where("category = ?", category)
	if search != "" {
		like := "%" + search + "%"
		countQ = countQ.Where("type ILIKE ? OR message ILIKE ? OR actor_name ILIKE ?", like, like, like)
		findQ = findQ.Where("type ILIKE ? OR message ILIKE ? OR actor_name ILIKE ?", like, like, like)
	}

	countQ.Count(&total)

	if err := findQ.Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// FindByWorkspaceID returns paginated events for a workspace.
func (r *EventRepository) FindByWorkspaceID(workspaceID uint, limit, offset int) ([]models.Event, int64, error) {
	var events []models.Event
	var total int64

	r.db.Model(&models.Event{}).Where("workspace_id = ?", workspaceID).Count(&total)

	if err := r.db.Where("workspace_id = ?", workspaceID).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// FindByWorkspaceAndCategory returns paginated events for a workspace filtered by category.
func (r *EventRepository) FindByWorkspaceAndCategory(workspaceID uint, category models.EventCategory, limit, offset int) ([]models.Event, int64, error) {
	var events []models.Event
	var total int64

	r.db.Model(&models.Event{}).Where("workspace_id = ? AND category = ?", workspaceID, category).Count(&total)

	if err := r.db.Where("workspace_id = ? AND category = ?", workspaceID, category).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

func (r *EventRepository) DeleteOlderThan(before time.Time) (int64, error) {
	result := r.db.Where("created_at < ?", before).Delete(&models.Event{})
	return result.RowsAffected, result.Error
}

// AuditEventFilter captures the audit-log list and export filters. Empty
// fields are ignored. A nil WorkspaceID spans the whole deployment and is
// reserved for the admin deployment scope; the workspace scope always sets it.
type AuditEventFilter struct {
	Action      string
	Outcome     string
	Actor       string
	TargetType  string
	TargetID    string
	IP          string
	WorkspaceID *uint
	From        *time.Time
	To          *time.Time
}

// applyAuditFilter narrows a query to the non-empty audit filters.
func applyAuditFilter(q *gorm.DB, f AuditEventFilter) *gorm.DB {
	if f.Action != "" {
		q = q.Where("type = ?", f.Action)
	}
	if f.Outcome != "" {
		q = q.Where("outcome = ?", f.Outcome)
	}
	if f.Actor != "" {
		q = q.Where("actor_name ILIKE ?", "%"+f.Actor+"%")
	}
	if f.TargetType != "" {
		q = q.Where("target_type = ?", f.TargetType)
	}
	if f.TargetID != "" {
		q = q.Where("target_id = ?", f.TargetID)
	}
	if f.IP != "" {
		q = q.Where("client_ip = ?", f.IP)
	}
	if f.WorkspaceID != nil {
		q = q.Where("workspace_id = ?", *f.WorkspaceID)
	}
	if f.From != nil {
		q = q.Where("created_at >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("created_at < ?", *f.To)
	}
	return q
}

// FindFiltered returns a page of audit events matching the filter, newest
// first, with a total count.
func (r *EventRepository) FindFiltered(page, size int, f AuditEventFilter) ([]models.Event, int64, error) {
	limit, offset := normalizeSecurityPage(page, size)

	var events []models.Event
	var total int64

	if err := applyAuditFilter(r.db.Model(&models.Event{}), f).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := applyAuditFilter(r.db, f).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&events).Error; err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// maxAuditExportRows caps the audit CSV export so one request cannot stream an
// unbounded result set.
const maxAuditExportRows = 100000

// ListForExport returns up to maxRows audit events matching the filter,
// newest first, for the CSV export stream.
func (r *EventRepository) ListForExport(f AuditEventFilter, maxRows int) ([]models.Event, error) {
	if maxRows <= 0 {
		maxRows = maxAuditExportRows
	}
	var events []models.Event
	if err := applyAuditFilter(r.db, f).
		Order("created_at DESC").
		Limit(maxRows).
		Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}
