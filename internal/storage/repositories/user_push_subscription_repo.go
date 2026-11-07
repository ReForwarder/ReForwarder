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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserPushSubscriptionRepository stores the browser Web Push subscriptions
// (SF-89). Every method is user-scoped except FindForWorkspace, which joins
// workspace membership so the realtime fan-out only reaches users who are
// members of the workspace an event was published for.
type UserPushSubscriptionRepository struct {
	db *gorm.DB
}

func NewUserPushSubscriptionRepository(db *gorm.DB) *UserPushSubscriptionRepository {
	return &UserPushSubscriptionRepository{db: db}
}

// Upsert saves a subscription keyed on its endpoint URL: a browser that
// re-registers the same endpoint refreshes its keys instead of failing the
// unique index.
func (r *UserPushSubscriptionRepository) Upsert(sub *models.UserPushSubscription) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "endpoint"}},
		DoUpdates: clause.AssignmentColumns([]string{"p256dh", "auth", "user_agent", "updated_at"}),
	}).Create(sub).Error
}

// FindByUser lists the calling user's own subscriptions.
func (r *UserPushSubscriptionRepository) FindByUser(userID uint) ([]models.UserPushSubscription, error) {
	var subs []models.UserPushSubscription
	if err := r.db.Where("user_id = ?", userID).Order("id ASC").Find(&subs).Error; err != nil {
		return nil, err
	}
	return subs, nil
}

// Delete removes one of the calling user's subscriptions. The user_id filter
// makes it impossible to delete someone else's row by guessing an ID.
func (r *UserPushSubscriptionRepository) Delete(userID, id uint) (bool, error) {
	res := r.db.Where("user_id = ? AND id = ?", userID, id).Delete(&models.UserPushSubscription{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteByEndpoint removes a subscription whose push service reported it gone
// (404/410), regardless of owner — the endpoint is cryptographically bound to
// its keys and is dead.
func (r *UserPushSubscriptionRepository) DeleteByEndpoint(endpoint string) error {
	return r.db.Where("endpoint = ?", endpoint).Delete(&models.UserPushSubscription{}).Error
}

// CountByUser returns how many subscriptions the user holds.
func (r *UserPushSubscriptionRepository) CountByUser(userID uint) (int64, error) {
	var n int64
	if err := r.db.Model(&models.UserPushSubscription{}).Where("user_id = ?", userID).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// FindForWorkspace returns every push subscription whose owner is a member of
// the given workspace — the delivery set for that workspace's realtime
// events. A user with no membership receives nothing.
func (r *UserPushSubscriptionRepository) FindForWorkspace(workspaceID uint) ([]models.UserPushSubscription, error) {
	var subs []models.UserPushSubscription
	err := r.db.
		Joins("JOIN workspace_members ON workspace_members.user_id = user_push_subscriptions.user_id").
		Where("workspace_members.workspace_id = ?", workspaceID).
		Find(&subs).Error
	if err != nil {
		return nil, err
	}
	return subs, nil
}
