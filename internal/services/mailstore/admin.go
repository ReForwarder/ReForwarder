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

package mailstore

import (
	"errors"
	"fmt"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// Admin-scoped mailbox operations (OB-5 wave W2, ADR-005 decision 3): the
// platform-administration surface over hosted mailboxes — provision for a
// user, paged cross-tenant listing, quota change, suspend/reactivate, cascade
// delete (ADR-008 ownership chain mailbox → user → workspace) and the
// GAP-45 usage report. The rest of the Service is owner-scoped (a user can
// never touch another user's mailboxes); these Admin* methods are meant for
// the admin HTTP layer only, whose route group is admin-gated (the /admin
// middleware in internal/routes), matching ADR-008's "admin scope is
// deployment-wide".
//
// Everything here reuses the owner-scoped machinery underneath: mutations
// load the target mailbox first and then call the owner-scoped paths with the
// mailbox's real owner, so the same purge/ref-count/counter code runs for
// admins and users and cannot drift. Queries are parameterized — no SQL
// concatenation anywhere.

// ErrMailboxNameTaken is returned by AdminProvisionMailbox when the owner
// already holds a mailbox of that name ((user_id, name) is unique —
// models.Mailbox).
var ErrMailboxNameTaken = errors.New("mailstore: mailbox name already in use for this user")

// AdminMailboxFilter narrows the platform-wide mailbox listing. Zero values
// mean "no constraint"; Status accepts the models.MailboxStatus values and
// Search is a case-insensitive substring match on the mailbox name (wildcards
// escaped via likePattern, so user input is always a literal).
type AdminMailboxFilter struct {
	UserID      uint
	WorkspaceID uint
	Status      models.MailboxStatus
	Search      string
}

// apply chains the filter onto q. Kept as a method so the COUNT and the page
// query cannot drift apart.
func (f AdminMailboxFilter) apply(q *gorm.DB) *gorm.DB {
	if f.UserID != 0 {
		q = q.Where("user_id = ?", f.UserID)
	}
	if f.WorkspaceID != 0 {
		q = q.Where("workspace_id = ?", f.WorkspaceID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Search != "" {
		q = q.Where("name ILIKE ? ESCAPE '\\'", likePattern(f.Search))
	}
	return q
}

// adminListPageBounds clamps the page window for the admin listing
// (mirrors the handlers' normalizePageParams contract: default 20, cap 100).
func adminListPageBounds(limit, offset int) (int, int) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// AdminListMailboxes returns one paged slice of the platform's mailboxes with
// the matching total, oldest first (same ordering as ListMailboxes).
func (s *Service) AdminListMailboxes(f AdminMailboxFilter, limit, offset int) ([]models.Mailbox, int64, error) {
	limit, offset = adminListPageBounds(limit, offset)
	var total int64
	if err := f.apply(s.db.Model(&models.Mailbox{})).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("admin: count mailboxes: %w", err)
	}
	var out []models.Mailbox
	if err := f.apply(s.db.Model(&models.Mailbox{})).
		Order("id ASC").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("admin: list mailboxes: %w", err)
	}
	return out, total, nil
}

// AdminGetMailbox loads one mailbox with no owner constraint. Returns
// ErrMailboxNotFound for an unknown id.
func (s *Service) AdminGetMailbox(id uint) (*models.Mailbox, error) {
	var mb models.Mailbox
	if err := s.db.First(&mb, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMailboxNotFound
		}
		return nil, fmt.Errorf("admin: load mailbox %d: %w", id, err)
	}
	return &mb, nil
}

// AdminProvisionMailbox provisions a mailbox for a user and creates its
// INBOX folder — the admin path over CreateMailbox, with a pre-check that
// turns the (user_id, name) unique-index violation into the typed
// ErrMailboxNameTaken (the unique index remains the backstop for concurrent
// provisions). quotaBytes/quotaMessages must be >= 0; 0 = unlimited.
func (s *Service) AdminProvisionMailbox(userID uint, workspaceID *uint, name string, quotaBytes, quotaMessages int64) (*models.Mailbox, error) {
	if quotaBytes < 0 {
		return nil, errors.New("mailstore: quota_bytes must be >= 0")
	}
	if quotaMessages < 0 {
		return nil, errors.New("mailstore: quota_messages must be >= 0")
	}
	if _, err := s.FindMailboxByName(userID, name); err == nil {
		return nil, ErrMailboxNameTaken
	} else if !errors.Is(err, ErrMailboxNotFound) {
		return nil, err
	}
	mb, err := s.CreateMailbox(userID, workspaceID, name, quotaBytes)
	if err != nil {
		// A concurrent provision can still win the unique index.
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrMailboxNameTaken
		}
		return nil, err
	}
	if quotaMessages > 0 {
		if err := s.db.Model(&models.Mailbox{}).Where("id = ?", mb.ID).
			Update("quota_messages", quotaMessages).Error; err != nil {
			return nil, fmt.Errorf("admin: set message quota on mailbox %d: %w", mb.ID, err)
		}
		mb.QuotaMessages = quotaMessages
	}
	return mb, nil
}

// AdminSetMailboxQuota changes a mailbox's byte and message-count quotas in
// one write (ADR-003 decision 5; ADR-008 decision 4 — quota mutations are
// audited, which the calling HTTP layer owns). 0 = unlimited. Returns the
// reloaded row.
func (s *Service) AdminSetMailboxQuota(id uint, quotaBytes, quotaMessages int64) (*models.Mailbox, error) {
	if quotaBytes < 0 {
		return nil, errors.New("mailstore: quota_bytes must be >= 0")
	}
	if quotaMessages < 0 {
		return nil, errors.New("mailstore: quota_messages must be >= 0")
	}
	mb, err := s.AdminGetMailbox(id)
	if err != nil {
		return nil, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.resolveMailbox(tx, mb.ID, mb.UserID, 0); err != nil {
			return err
		}
		return tx.Model(&models.Mailbox{}).Where("id = ?", mb.ID).Updates(map[string]any{
			"quota_bytes":    quotaBytes,
			"quota_messages": quotaMessages,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return s.AdminGetMailbox(id)
}

// AdminSetMailboxStatus suspends or reactivates a mailbox. The terminal
// `closed` state is deliberately NOT admin-settable through this path — the
// admin removal flow is DeleteMailbox (a closed mailbox still occupies its
// rows and counters, which is never what an operator means by "disable").
func (s *Service) AdminSetMailboxStatus(id uint, status models.MailboxStatus) (*models.Mailbox, error) {
	switch status {
	case models.MailboxStatusActive, models.MailboxStatusSuspended:
	default:
		return nil, fmt.Errorf("mailstore: admin status change must be active or suspended, got %q", status)
	}
	mb, err := s.AdminGetMailbox(id)
	if err != nil {
		return nil, err
	}
	if err := s.SetMailboxStatus(mb.ID, mb.UserID, 0, status); err != nil {
		return nil, err
	}
	return s.AdminGetMailbox(id)
}

// AdminDeleteMailbox removes the mailbox with all folders and messages in one
// transaction — the same cascade as DeleteMailbox (decrementing every
// referenced blob's ref count; the objects themselves are left for the GC
// sweep), reached with no owner constraint.
func (s *Service) AdminDeleteMailbox(id uint) error {
	mb, err := s.AdminGetMailbox(id)
	if err != nil {
		return err
	}
	return s.DeleteMailbox(mb.ID, mb.UserID, 0)
}

// AdminMailboxUsage reports one mailbox's GAP-45 usage (quota basis
// RawBytes/MessageCount from the mailbox counters, storage basis BlobBytes
// dedup-aware from mail_blob_refs) with no owner constraint.
func (s *Service) AdminMailboxUsage(id uint) (*MailboxUsageReport, error) {
	mb, err := s.AdminGetMailbox(id)
	if err != nil {
		return nil, err
	}
	hashes, err := mailboxBlobHashes(s.db, mb.ID)
	if err != nil {
		return nil, err
	}
	blobBytes, distinct, err := sumBlobRefSizes(s.db, hashes)
	if err != nil {
		return nil, err
	}
	return &MailboxUsageReport{
		MailboxID:     mb.ID,
		UserID:        mb.UserID,
		Name:          mb.Name,
		QuotaBytes:    mb.QuotaBytes,
		RawBytes:      mb.UsedBytes,
		MessageCount:  mb.MessageCount,
		BlobBytes:     blobBytes,
		DistinctBlobs: distinct,
	}, nil
}

// AdminUserExists reports whether the user id exists — the provisioning
// target validation (fail closed: an unknown user gets no mailbox).
func (s *Service) AdminUserExists(userID uint) (bool, error) {
	var n int64
	if err := s.db.Model(&models.User{}).Where("id = ?", userID).Count(&n).Error; err != nil {
		return false, fmt.Errorf("admin: check user %d: %w", userID, err)
	}
	return n > 0, nil
}

// AdminUserInWorkspace reports whether the user is a member of the workspace
// — provisioning may only bind a mailbox into a workspace the owner belongs
// to (ADR-008 decision 1: the mailbox → user → workspace parent chain).
func (s *Service) AdminUserInWorkspace(userID, workspaceID uint) (bool, error) {
	var n int64
	if err := s.db.Model(&models.WorkspaceMember{}).
		Where("workspace_id = ? AND user_id = ?", workspaceID, userID).
		Count(&n).Error; err != nil {
		return false, fmt.Errorf("admin: check membership of user %d in workspace %d: %w", userID, workspaceID, err)
	}
	return n > 0, nil
}
