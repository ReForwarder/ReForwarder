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
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/conditions"
	"gorm.io/gorm"
)

// SegmentRepository stores saved audience filters and resolves their
// subscribers through the SegmentCondition builder (segment_query.go).
type SegmentRepository struct {
	db *gorm.DB
}

func NewSegmentRepository(db *gorm.DB) *SegmentRepository {
	return &SegmentRepository{db: db}
}

func (r *SegmentRepository) Create(s *models.Segment) error {
	return r.db.Create(s).Error
}

func (r *SegmentRepository) FindByID(id uint) (*models.Segment, error) {
	var s models.Segment
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// FindByScope returns the workspace's segments, newest first, with the total.
func (r *SegmentRepository) FindByScope(scope ResourceScope, limit, offset int) ([]models.Segment, int64, error) {
	var items []models.Segment
	var total int64

	ApplyScope(r.db.Model(&models.Segment{}), scope).Count(&total)

	if err := ApplyScope(r.db, scope).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *SegmentRepository) Update(s *models.Segment) error {
	return r.db.Save(s).Error
}

func (r *SegmentRepository) Delete(id uint) error {
	return r.db.Delete(&models.Segment{}, id).Error
}

// CountByCondition counts the subscribers in scope matching the condition.
// Evaluated live (same convention as dynamic subscriber lists). SF-103: the
// Segment currency rides the unified evaluator.
func (r *SegmentRepository) CountByCondition(scope ResourceScope, cond models.SegmentCondition) (int64, error) {
	var count int64
	q, err := conditions.NewSegmentEvaluator(cond).WhereSQL(ApplyScope(r.db.Model(&models.Subscriber{}), scope))
	if err != nil {
		return 0, err
	}
	if err := q.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// FindSubscribersByCondition resolves the audience of a condition: count plus
// a paged sample, newest subscribers first. A negative limit means all rows
// (campaign send-time resolution passes -1, 0). SF-103: the Segment currency
// rides the unified evaluator.
func (r *SegmentRepository) FindSubscribersByCondition(scope ResourceScope, cond models.SegmentCondition, limit, offset int) ([]models.Subscriber, int64, error) {
	ev := conditions.NewSegmentEvaluator(cond)
	var total int64
	countQ, err := ev.WhereSQL(ApplyScope(r.db.Model(&models.Subscriber{}), scope))
	if err != nil {
		return nil, 0, err
	}
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	findQ, err := ev.WhereSQL(ApplyScope(r.db, scope))
	if err != nil {
		return nil, 0, err
	}
	var items []models.Subscriber
	if err := findQ.
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
