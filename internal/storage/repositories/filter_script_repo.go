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
	"errors"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// FilterScriptRepository persists the per-mailbox filter scripts (SF-14, the
// ADR-006 amendment). User-scoped: every read and write is bound to the
// owning user (the mail surface's session user).
type FilterScriptRepository struct {
	db *gorm.DB
}

func NewFilterScriptRepository(db *gorm.DB) *FilterScriptRepository {
	return &FilterScriptRepository{db: db}
}

// Create persists one script row.
func (r *FilterScriptRepository) Create(script *models.FilterScript) error {
	return r.db.Create(script).Error
}

// Update saves script state (the PATCH path).
func (r *FilterScriptRepository) Update(script *models.FilterScript) error {
	return r.db.Save(script).Error
}

// FindByIDForUser returns one of the user's scripts, or gorm.ErrRecordNotFound
// (a foreign script reads as missing — isolation).
func (r *FilterScriptRepository) FindByIDForUser(id, userID uint) (*models.FilterScript, error) {
	var script models.FilterScript
	if err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&script).Error; err != nil {
		return nil, err
	}
	return &script, nil
}

// ListForUser returns the user's scripts for one mailbox, evaluation order
// (priority ASC, id ASC).
func (r *FilterScriptRepository) ListForUser(userID, mailboxID uint) ([]models.FilterScript, error) {
	var out []models.FilterScript
	if err := r.db.Where("user_id = ? AND mailbox_id = ?", userID, mailboxID).
		Order("priority ASC, id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListEnabledForMailbox returns the ENABLED scripts bound to one mailbox in
// evaluation order — the delivery evaluation's read.
func (r *FilterScriptRepository) ListEnabledForMailbox(mailboxID uint) ([]models.FilterScript, error) {
	var out []models.FilterScript
	if err := r.db.Where("mailbox_id = ? AND enabled = ?", mailboxID, true).
		Order("priority ASC, id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteForUser removes one of the user's scripts (scoped; returns the
// affected-row count).
func (r *FilterScriptRepository) DeleteForUser(id, userID uint) (int64, error) {
	res := r.db.Where("id = ? AND user_id = ?", id, userID).Delete(&models.FilterScript{})
	return res.RowsAffected, res.Error
}

// FindByNameForUser returns one of the user's scripts by name within one
// mailbox (the ManageSieve name-addressed surface), or
// gorm.ErrRecordNotFound (a missing name reads as missing — isolation).
func (r *FilterScriptRepository) FindByNameForUser(userID, mailboxID uint, name string) (*models.FilterScript, error) {
	var script models.FilterScript
	if err := r.db.Where("user_id = ? AND mailbox_id = ? AND name = ?", userID, mailboxID, name).
		First(&script).Error; err != nil {
		return nil, err
	}
	return &script, nil
}

// DeleteByNameForUser removes the named script within one mailbox (scoped;
// returns the affected-row count).
func (r *FilterScriptRepository) DeleteByNameForUser(userID, mailboxID uint, name string) (int64, error) {
	res := r.db.Where("user_id = ? AND mailbox_id = ? AND name = ?", userID, mailboxID, name).
		Delete(&models.FilterScript{})
	return res.RowsAffected, res.Error
}

// UpsertForUser stores (or replaces by name within the mailbox) one script
// document — the ManageSieve PUTSCRIPT path. A re-put name replaces the
// stored document and keeps the existing enabled flag (PUTSCRIPT never
// changes activation); the find-then-write runs in one transaction to narrow
// the concurrent-put race.
func (r *FilterScriptRepository) UpsertForUser(script *models.FilterScript) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var existing models.FilterScript
		err := tx.Where("user_id = ? AND mailbox_id = ? AND name = ?", script.UserID, script.MailboxID, script.Name).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Map-based create: the Enabled column carries a server default
			// (`default:true` on the model), and gorm substitutes that
			// default for a zero-valued field on struct inserts — which
			// would silently flip PUTSCRIPT's INACTIVE to active. A map
			// insert writes every key as-is, Enabled false included.
			return tx.Model(&models.FilterScript{}).Create(map[string]any{
				"user_id":      script.UserID,
				"mailbox_id":   script.MailboxID,
				"name":         script.Name,
				"priority":     script.Priority,
				"enabled":      script.Enabled,
				"conditions":   script.Conditions, // driver.Valuer → JSON text
				"actions_json": script.ActionsJSON,
				"created_at":   time.Now(),
				"updated_at":   time.Now(),
			}).Error
		}
		if err != nil {
			return err
		}
		existing.Conditions = script.Conditions
		existing.ActionsJSON = script.ActionsJSON
		return tx.Save(&existing).Error
	})
}

// DeactivateAllForMailbox disables every enabled script of the mailbox —
// the SETACTIVE "" deactivation.
func (r *FilterScriptRepository) DeactivateAllForMailbox(userID, mailboxID uint) error {
	return r.db.Model(&models.FilterScript{}).
		Where("user_id = ? AND mailbox_id = ? AND enabled = ?", userID, mailboxID, true).
		Update("enabled", false).Error
}

// SetActiveForMailbox enables the named script and disables every other
// script of the mailbox in one transaction — exactly one active script per
// mailbox (the ManageSieve activation semantics; the filter engine evaluates
// enabled scripts, first match wins). An unknown name yields
// gorm.ErrRecordNotFound.
func (r *FilterScriptRepository) SetActiveForMailbox(userID, mailboxID uint, name string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.FilterScript{}).
			Where("user_id = ? AND mailbox_id = ? AND enabled = ?", userID, mailboxID, true).
			Update("enabled", false).Error; err != nil {
			return err
		}
		res := tx.Model(&models.FilterScript{}).
			Where("user_id = ? AND mailbox_id = ? AND name = ?", userID, mailboxID, name).
			Update("enabled", true)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}
