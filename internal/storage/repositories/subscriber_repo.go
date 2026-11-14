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
	"strings"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/conditions"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SubscriberRepository struct {
	db *gorm.DB
}

func NewSubscriberRepository(db *gorm.DB) *SubscriberRepository {
	return &SubscriberRepository{db: db}
}

func (r *SubscriberRepository) Create(s *models.Subscriber) error {
	return r.db.Create(s).Error
}

func (r *SubscriberRepository) FindByID(id uint) (*models.Subscriber, error) {
	var s models.Subscriber
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SubscriberRepository) FindByEmail(scope ResourceScope, email string) (*models.Subscriber, error) {
	var s models.Subscriber
	q := ApplyScope(r.db, scope).Where("email = ?", strings.ToLower(strings.TrimSpace(email)))
	if err := q.First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// FindAllByEmail finds all subscribers with the given email address across all scopes.
func (r *SubscriberRepository) FindAllByEmail(email string, result *[]models.Subscriber) error {
	return r.db.Where("email = ?", strings.ToLower(strings.TrimSpace(email))).Find(result).Error
}

func (r *SubscriberRepository) FindByScope(scope ResourceScope, search, status string, limit, offset int) ([]models.Subscriber, int64, error) {
	var items []models.Subscriber
	var total int64

	q := ApplyScope(r.db.Model(&models.Subscriber{}), scope)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if search != "" {
		q = q.Where("email ILIKE ? OR name ILIKE ?", "%"+search+"%", "%"+search+"%")
	}
	q.Count(&total)

	qFind := ApplyScope(r.db, scope)
	if status != "" {
		qFind = qFind.Where("status = ?", status)
	}
	if search != "" {
		qFind = qFind.Where("email ILIKE ? OR name ILIKE ?", "%"+search+"%", "%"+search+"%")
	}
	if err := qFind.Order("created_at DESC").Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *SubscriberRepository) Update(s *models.Subscriber) error {
	return r.db.Save(s).Error
}

func (r *SubscriberRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Remove from all static lists first
		if err := tx.Where("subscriber_id = ?", id).Delete(&models.SubscriberListMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Subscriber{}, id).Error
	})
}

// BulkCreate inserts subscribers in batches, skipping duplicates.
func (r *SubscriberRepository) BulkCreate(subscribers []models.Subscriber) (created int, skipped int, err error) {
	if len(subscribers) == 0 {
		return 0, 0, nil
	}
	result := r.db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(subscribers, 100)
	if result.Error != nil {
		return 0, 0, result.Error
	}
	created = int(result.RowsAffected)
	skipped = len(subscribers) - created
	return created, skipped, nil
}

// FindByFilterRules evaluates dynamic segment filter rules against subscribers.
func (r *SubscriberRepository) FindByFilterRules(scope ResourceScope, rules models.FilterRules, limit, offset int) ([]models.Subscriber, int64, error) {
	var items []models.Subscriber
	var total int64

	// SF-103: the FilterRule currency rides the unified evaluator — the same
	// SQL translation the delegating applyFilterRules below carries out.
	ev := conditions.NewFilterRuleEvaluator(rules)
	q, err := ev.WhereSQL(ApplyScope(r.db.Model(&models.Subscriber{}), scope))
	if err != nil {
		return nil, 0, err
	}
	q.Count(&total)

	qFind, err := ev.WhereSQL(ApplyScope(r.db, scope))
	if err != nil {
		return nil, 0, err
	}
	if err := qFind.Order("created_at DESC").Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// CountByFilterRules returns the count of subscribers matching filter rules.
func (r *SubscriberRepository) CountByFilterRules(scope ResourceScope, rules models.FilterRules) (int64, error) {
	var count int64
	ev := conditions.NewFilterRuleEvaluator(rules)
	q, err := ev.WhereSQL(ApplyScope(r.db.Model(&models.Subscriber{}), scope))
	if err != nil {
		return 0, err
	}
	if err := q.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// applyFilterRules translates FilterRules into SQL WHERE clauses — the
// SF-103 delegate: the implementation moved verbatim to the conditions
// package (ApplyFilterRuleSQL); this method keeps the repository's own name
// (and the cross-engine tests that drive it) working unchanged.
func (r *SubscriberRepository) applyFilterRules(q *gorm.DB, rules models.FilterRules) *gorm.DB {
	return conditions.ApplyFilterRuleSQL(q, rules)
}
