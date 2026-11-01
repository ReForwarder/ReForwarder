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
	"errors"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NotificationRepository persists the per-user in-app inbox. It is unrelated to
// services/notification, which sends email.
type NotificationRepository struct {
	db *gorm.DB
}

func NewNotificationRepository(db *gorm.DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}

// NotificationFilter narrows the inbox listing.
type NotificationFilter struct {
	WorkspaceID *uint
	UnreadOnly  bool
	OpenOnly    bool
	Category    models.NotificationCategory
}

// Upsert writes one delivery, folding it into the existing unresolved row for the
// same (user, workspace, dedup key) when there is one.
//
// A dismissal survives only while the condition looks the same. When the
// fingerprint changes the row is re-armed — undismissed and marked unread — so a
// worsening condition comes back rather than staying silenced by a dismissal the
// user made when it was smaller.
func (r *NotificationRepository) Upsert(n *models.Notification) error {
	// Two dashboard loads racing would both find nothing and both insert. The
	// partial unique index would reject the loser, so let the database arbitrate
	// and fall through to the update path when it does.
	if err := r.upsertOnce(n); errors.Is(err, errRaceLost) {
		return r.upsertOnce(n)
	} else if err != nil {
		return err
	}
	return nil
}

var errRaceLost = errors.New("notification upsert lost an insert race")

func (r *NotificationRepository) upsertOnce(n *models.Notification) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var existing models.Notification
		q := tx.Where("user_id = ? AND dedup_key = ? AND resolved_at IS NULL", n.UserID, n.DedupKey)
		q = scopeWorkspace(q, n.WorkspaceID)

		err := q.First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(n)
			if created.Error != nil {
				return created.Error
			}
			if created.RowsAffected == 0 {
				return errRaceLost
			}
			return nil
		}
		if err != nil {
			return err
		}

		changed := existing.Fingerprint != n.Fingerprint
		existing.Title = n.Title
		existing.Body = n.Body
		existing.Link = n.Link
		existing.ActionText = n.ActionText
		existing.Severity = n.Severity
		existing.Category = n.Category
		existing.Fingerprint = n.Fingerprint
		if changed {
			existing.DismissedAt = nil
			existing.ReadAt = nil
		}
		if err := tx.Save(&existing).Error; err != nil {
			return err
		}
		*n = existing
		return nil
	})
}

// ResolveExcept closes every unresolved alert for the user and workspace whose
// dedup key is no longer in keep — the conditions that have cleared since the
// last scan. Resolved rows stay in the inbox as history but drop off the banner.
func (r *NotificationRepository) ResolveExcept(userID uint, workspaceID *uint, keep []string) error {
	q := r.db.Model(&models.Notification{}).
		Where("user_id = ? AND kind = ? AND resolved_at IS NULL", userID, models.NotificationKindAlert)
	q = scopeWorkspace(q, workspaceID)
	if len(keep) > 0 {
		q = q.Where("dedup_key NOT IN ?", keep)
	}
	return q.Update("resolved_at", time.Now().UTC()).Error
}

// List returns a page of the user's inbox, newest first. before is a keyset
// cursor: pass the id of the last row seen.
func (r *NotificationRepository) List(userID uint, f NotificationFilter, before uint, limit int) ([]models.Notification, error) {
	q := r.query(userID, f)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	var out []models.Notification
	err := q.Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}

// Counts returns the bell badge and the number of open items, in one round trip
// each rather than loading the rows.
func (r *NotificationRepository) Counts(userID uint, workspaceID *uint) (unread, open int64, err error) {
	unreadQ := r.db.Model(&models.Notification{}).
		Where("user_id = ? AND read_at IS NULL AND resolved_at IS NULL", userID)
	if err = unreadQ.Count(&unread).Error; err != nil {
		return 0, 0, err
	}
	openQ := r.db.Model(&models.Notification{}).
		Where("user_id = ? AND resolved_at IS NULL AND dismissed_at IS NULL", userID)
	openQ = scopeWorkspace(openQ, workspaceID)
	err = openQ.Count(&open).Error
	return unread, open, err
}

// Open returns the items the workspace dashboard banner renders: live, not
// dismissed, and belonging to this workspace or to no workspace at all.
func (r *NotificationRepository) Open(userID uint, workspaceID *uint) ([]models.Notification, error) {
	q := r.db.Where("user_id = ? AND resolved_at IS NULL AND dismissed_at IS NULL", userID)
	if workspaceID != nil {
		q = q.Where("workspace_id IS NULL OR workspace_id = ?", *workspaceID)
	}
	var out []models.Notification
	err := q.Order("CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, id DESC").
		Limit(10).Find(&out).Error
	return out, err
}

func (r *NotificationRepository) MarkRead(userID uint, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.Model(&models.Notification{}).
		Where("user_id = ? AND id IN ? AND read_at IS NULL", userID, ids).
		Update("read_at", time.Now().UTC()).Error
}

func (r *NotificationRepository) MarkAllRead(userID uint, workspaceID *uint) error {
	q := r.db.Model(&models.Notification{}).Where("user_id = ? AND read_at IS NULL", userID)
	q = scopeWorkspace(q, workspaceID)
	return q.Update("read_at", time.Now().UTC()).Error
}

// Dismiss hides one item from the banner. It also marks it read: a user who
// dismissed something has seen it, and leaving the bell badge lit would be noise.
func (r *NotificationRepository) Dismiss(userID uint, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return r.db.Model(&models.Notification{}).
		Where("user_id = ? AND id IN ? AND dismissed_at IS NULL", userID, ids).
		Updates(map[string]any{"dismissed_at": now, "read_at": gorm.Expr("COALESCE(read_at, ?)", now)}).Error
}

// DismissAll clears the banner in one action.
func (r *NotificationRepository) DismissAll(userID uint, workspaceID *uint) error {
	now := time.Now().UTC()
	q := r.db.Model(&models.Notification{}).
		Where("user_id = ? AND dismissed_at IS NULL AND resolved_at IS NULL", userID)
	q = scopeWorkspace(q, workspaceID)
	return q.Updates(map[string]any{"dismissed_at": now, "read_at": gorm.Expr("COALESCE(read_at, ?)", now)}).Error
}

// Prune drops notifications older than the cutoff. Open items are kept whatever
// their age: an unresolved condition is still true, however long it has been.
func (r *NotificationRepository) Prune(before time.Time) (int64, error) {
	res := r.db.Where("created_at < ? AND (resolved_at IS NOT NULL OR dismissed_at IS NOT NULL)", before).
		Delete(&models.Notification{})
	return res.RowsAffected, res.Error
}

// DeleteForAnnouncement removes the deliveries a retracted announcement produced.
func (r *NotificationRepository) DeleteForAnnouncement(dedupKey string) (int64, error) {
	res := r.db.Where("dedup_key = ? AND kind = ?", dedupKey, models.NotificationKindAnnouncement).
		Delete(&models.Notification{})
	return res.RowsAffected, res.Error
}

// CreateBatch inserts many deliveries at once — the announcement fan-out.
func (r *NotificationRepository) CreateBatch(rows []models.Notification) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.CreateInBatches(rows, 200).Error
}

func (r *NotificationRepository) query(userID uint, f NotificationFilter) *gorm.DB {
	q := r.db.Model(&models.Notification{}).Where("user_id = ?", userID)
	q = scopeWorkspace(q, f.WorkspaceID)
	if f.UnreadOnly {
		q = q.Where("read_at IS NULL")
	}
	if f.OpenOnly {
		q = q.Where("resolved_at IS NULL AND dismissed_at IS NULL")
	}
	if f.Category != "" {
		q = q.Where("category = ?", f.Category)
	}
	return q
}

// scopeWorkspace restricts to one workspace while keeping platform-wide items,
// which belong to the user in every workspace they open.
func scopeWorkspace(q *gorm.DB, workspaceID *uint) *gorm.DB {
	if workspaceID == nil {
		return q
	}
	return q.Where("workspace_id IS NULL OR workspace_id = ?", *workspaceID)
}
