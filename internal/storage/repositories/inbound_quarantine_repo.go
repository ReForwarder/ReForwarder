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

// InboundQuarantineFilter narrows a quarantine list query.
type InboundQuarantineFilter struct {
	// Status filters by review state (pending|released|deleted); empty = all.
	Status string
	// DomainID restricts to one of the workspace's verified domains; nil = all.
	DomainID *uint
}

// InboundQuarantineRepository persists quarantine review records (OB-2,
// ADR-002 decision 3). Management methods are workspace-scoped through
// ResourceScope; the engine-facing lookup keys on the inbound message ID.
type InboundQuarantineRepository struct {
	db *gorm.DB
}

func NewInboundQuarantineRepository(db *gorm.DB) *InboundQuarantineRepository {
	return &InboundQuarantineRepository{db: db}
}

// Create persists a new quarantine record.
func (r *InboundQuarantineRepository) Create(q *models.InboundQuarantine) error {
	return r.db.Create(q).Error
}

// Update saves a quarantine record (status transitions, review stamp).
func (r *InboundQuarantineRepository) Update(q *models.InboundQuarantine) error {
	return r.db.Save(q).Error
}

// FindByID returns one quarantine record within the caller's workspace scope.
func (r *InboundQuarantineRepository) FindByID(scope ResourceScope, id uint) (*models.InboundQuarantine, error) {
	var q models.InboundQuarantine
	if err := ApplyScope(r.db, scope).First(&q, id).Error; err != nil {
		return nil, err
	}
	return &q, nil
}

// FindPendingByInboundEmailID returns the open (pending) record for a message,
// or gorm.ErrRecordNotFound. The store uses it to keep the hold idempotent —
// a message is quarantined once per hold, never duplicated by a racing action.
func (r *InboundQuarantineRepository) FindPendingByInboundEmailID(inboundEmailID uint) (*models.InboundQuarantine, error) {
	var q models.InboundQuarantine
	if err := r.db.
		Where("inbound_email_id = ? AND status = ?", inboundEmailID, models.InboundQuarantinePending).
		Order("id ASC").
		First(&q).Error; err != nil {
		return nil, err
	}
	return &q, nil
}

// ListByScope returns the workspace's quarantine records with a total count,
// newest arrival first, then id (deterministic order for paging).
func (r *InboundQuarantineRepository) ListByScope(scope ResourceScope, f InboundQuarantineFilter, limit, offset int) ([]models.InboundQuarantine, int64, error) {
	var items []models.InboundQuarantine
	var total int64

	count := ApplyScope(r.db.Model(&models.InboundQuarantine{}), scope)
	list := ApplyScope(r.db, scope)
	if f.Status != "" {
		count = count.Where("status = ?", f.Status)
		list = list.Where("status = ?", f.Status)
	}
	if f.DomainID != nil {
		count = count.Where("domain_id = ?", *f.DomainID)
		list = list.Where("domain_id = ?", *f.DomainID)
	}

	if err := count.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := list.
		Order("received_at DESC, id DESC").
		Limit(limit).Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ExpiredPendingInboundEmailIDs returns the inbound message IDs still held
// pending on records past the retention cutoff — the messages whose content
// the retention pass must purge together with their record.
func (r *InboundQuarantineRepository) ExpiredPendingInboundEmailIDs(before time.Time, limit int) ([]uint, error) {
	var ids []uint
	if err := r.db.Model(&models.InboundQuarantine{}).
		Where("status = ? AND received_at < ?", models.InboundQuarantinePending, before).
		Limit(limit).
		Pluck("inbound_email_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// DeleteOlderThan permanently removes quarantine records (any status) past
// the retention cutoff. Called only by the retention job, which purges the
// held messages' content first.
func (r *InboundQuarantineRepository) DeleteOlderThan(before time.Time) (int64, error) {
	res := r.db.Where("received_at < ?", before).Delete(&models.InboundQuarantine{})
	return res.RowsAffected, res.Error
}
