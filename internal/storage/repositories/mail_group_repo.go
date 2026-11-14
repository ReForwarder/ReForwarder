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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
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
)

// MailGroupRepository persists mail-account groups (SF-65): a group address
// whose delivery fans out to member mailboxes. Groups and memberships are
// owner-scoped (user_id via the group row); the delivery expansion resolves
// only through the owning user, so another user's group is invisible.
type MailGroupRepository struct {
	db *gorm.DB
}

func NewMailGroupRepository(db *gorm.DB) *MailGroupRepository {
	return &MailGroupRepository{db: db}
}

// Create persists the group row and its member list in one transaction.
// Member IDs are stored as given (the endpoint validates ownership and
// excludes the group's own anchor before calling).
func (r *MailGroupRepository) Create(group *models.MailGroup, memberMailboxIDs []uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(group).Error; err != nil {
			return err
		}
		for _, mbID := range memberMailboxIDs {
			member := &models.MailGroupMember{GroupID: group.ID, MailboxID: mbID}
			if err := tx.Create(member).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceMembers swaps the group's member list in one transaction (the PATCH
// semantics: the request carries the full new set).
func (r *MailGroupRepository) ReplaceMembers(groupID, userID uint, memberMailboxIDs []uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Scope the group to the user: a foreign group id reads as absent.
		var n int64
		if err := tx.Model(&models.MailGroup{}).
			Where("id = ? AND user_id = ?", groupID, userID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Where("group_id = ?", groupID).Delete(&models.MailGroupMember{}).Error; err != nil {
			return err
		}
		for _, mbID := range memberMailboxIDs {
			member := &models.MailGroupMember{GroupID: groupID, MailboxID: mbID}
			if err := tx.Create(member).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteForUser removes one of the user's groups with its memberships in one
// transaction (scoped; returns the affected-row count).
func (r *MailGroupRepository) DeleteForUser(id, userID uint) (int64, error) {
	var affected int64
	err := r.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ? AND user_id = ?", id, userID).Delete(&models.MailGroup{})
		affected = res.RowsAffected
		if res.Error != nil {
			return res.Error
		}
		if affected > 0 {
			if err := tx.Where("group_id = ?", id).Delete(&models.MailGroupMember{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return affected, err
}

// ListByUser returns the user's groups, oldest first.
func (r *MailGroupRepository) ListByUser(userID uint) ([]models.MailGroup, error) {
	var out []models.MailGroup
	if err := r.db.Where("user_id = ?", userID).Order("id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// MembersOf returns the member mailbox IDs of one group, in insertion order.
func (r *MailGroupRepository) MembersOf(groupID uint) ([]uint, error) {
	var out []uint
	if err := r.db.Model(&models.MailGroupMember{}).
		Where("group_id = ?", groupID).
		Order("id ASC").
		Pluck("mailbox_id", &out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// FindByIDForUser returns one of the user's groups, or gorm.ErrRecordNotFound.
func (r *MailGroupRepository) FindByIDForUser(id, userID uint) (*models.MailGroup, error) {
	var group models.MailGroup
	if err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&group).Error; err != nil {
		return nil, err
	}
	return &group, nil
}

// CountAddress counts how many groups carry the address (unscoped by user:
// the endpoint's uniqueness check must see another user's group too — one
// address, one mapping, globally).
func (r *MailGroupRepository) CountAddress(address string) (int64, error) {
	var n int64
	if err := r.db.Model(&models.MailGroup{}).Where("address = ?", address).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// ResolveGroupAddress implements the inbound.AliasGroupResolver seam (SF-65):
// the member mailbox IDs of the user's group at address, ok=false when the
// user owns no such group. The user scoping is the tenant boundary.
func (r *MailGroupRepository) ResolveGroupAddress(userID uint, address string) ([]uint, bool) {
	var group models.MailGroup
	if err := r.db.Where("user_id = ? AND address = ?", userID, address).First(&group).Error; err != nil {
		return nil, false
	}
	members, err := r.MembersOf(group.ID)
	if err != nil {
		return nil, false
	}
	return members, true
}

// ResolveGroupByMailbox implements the inbound.AliasGroupResolver seam
// (SF-65): the member mailbox IDs when the mailbox anchors one of the user's
// groups (list-of-lists expansion), ok=false otherwise.
func (r *MailGroupRepository) ResolveGroupByMailbox(userID, mailboxID uint) ([]uint, bool) {
	var group models.MailGroup
	if err := r.db.Where("user_id = ? AND mailbox_id = ?", userID, mailboxID).First(&group).Error; err != nil {
		return nil, false
	}
	members, err := r.MembersOf(group.ID)
	if err != nil {
		return nil, false
	}
	return members, true
}
