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
	"strings"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// CreateMailbox provisions a mailbox for a user and creates its INBOX folder.
// Name is the mailbox identity (e.g. the hosted address); one name per user.
func (s *Service) CreateMailbox(userID uint, workspaceID *uint, name string, quotaBytes int64) (*models.Mailbox, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("mailstore: mailbox name required")
	}
	if len(name) > 255 {
		return nil, errors.New("mailstore: mailbox name too long")
	}
	mb := &models.Mailbox{
		UserID:      userID,
		WorkspaceID: workspaceID,
		Name:        name,
		QuotaBytes:  quotaBytes,
		Status:      models.MailboxStatusActive,
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(mb).Error; err != nil {
			return fmt.Errorf("create mailbox: %w", err)
		}
		_, err := createFolderRow(tx, mb.ID, nil, "INBOX")
		return err
	})
	if err != nil {
		return nil, err
	}
	return mb, nil
}

// FindMailboxByName resolves a mailbox by its (owner, name) identity — the
// ingestion seam's provisioning lookup (OB-3 W5, flow F13). Scoping is
// user-level only, deliberately: (user_id, name) is the unique mailbox
// identity (the unique index carries no workspace), a mailbox is its owner's
// property regardless of which workspace a delivery arrived through, and the
// caller is the server-internal ingest path, never a cross-user HTTP actor.
// Returns ErrMailboxNotFound when the user owns no such mailbox.
func (s *Service) FindMailboxByName(userID uint, name string) (*models.Mailbox, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrMailboxNotFound
	}
	var mb models.Mailbox
	err := s.db.Where("user_id = ? AND name = ?", userID, name).First(&mb).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMailboxNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find mailbox %q of user %d: %w", name, userID, err)
	}
	return &mb, nil
}

// GetMailbox returns one mailbox with ownership scoping.
func (s *Service) GetMailbox(mailboxID, userID, workspaceID uint) (*models.Mailbox, error) {
	var mb *models.Mailbox
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		mb, err = s.resolveMailbox(tx, mailboxID, userID, workspaceID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return mb, nil
}

// ListMailboxes returns a user's mailboxes (workspace-filtered when
// workspaceID is non-zero), oldest first.
func (s *Service) ListMailboxes(userID, workspaceID uint) ([]models.Mailbox, error) {
	q := s.db.Where("user_id = ?", userID)
	if workspaceID != 0 {
		q = q.Where("workspace_id = ?", workspaceID)
	}
	var out []models.Mailbox
	if err := q.Order("id ASC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list mailboxes for user %d: %w", userID, err)
	}
	return out, nil
}

// SetMailboxQuota updates the byte quota; 0 lifts the limit.
func (s *Service) SetMailboxQuota(mailboxID, userID, workspaceID uint, quotaBytes int64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.resolveMailbox(tx, mailboxID, userID, workspaceID); err != nil {
			return err
		}
		return tx.Model(&models.Mailbox{}).Where("id = ?", mailboxID).
			Update("quota_bytes", quotaBytes).Error
	})
}

// SetMailboxStatus moves a mailbox between active/suspended/closed. Closed is
// terminal on the write path: appends refuse it, reads keep working so the
// owner can still drain their mail.
func (s *Service) SetMailboxStatus(mailboxID, userID, workspaceID uint, status models.MailboxStatus) error {
	switch status {
	case models.MailboxStatusActive, models.MailboxStatusSuspended, models.MailboxStatusClosed:
	default:
		return fmt.Errorf("mailstore: unknown mailbox status %q", status)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.resolveMailbox(tx, mailboxID, userID, workspaceID); err != nil {
			return err
		}
		return tx.Model(&models.Mailbox{}).Where("id = ?", mailboxID).
			Update("status", status).Error
	})
}

// DeleteMailbox removes the mailbox with all folders and messages in one
// transaction, decrementing every referenced blob's ref count. The blob
// objects themselves are left for the GC sweep (a crash mid-sweep can only
// orphan blobs, never lose referenced content).
func (s *Service) DeleteMailbox(mailboxID, userID, workspaceID uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		mb, err := s.resolveMailbox(tx, mailboxID, userID, workspaceID)
		if err != nil {
			return err
		}
		var folderIDs []uint
		if err := tx.Model(&models.MailFolder{}).Where("mailbox_id = ?", mb.ID).
			Pluck("id", &folderIDs).Error; err != nil {
			return fmt.Errorf("list folders of mailbox %d: %w", mb.ID, err)
		}
		for _, fid := range folderIDs {
			if err := purgeFolderMessages(tx, fid, mb.ID); err != nil {
				return err
			}
		}
		if err := tx.Where("mailbox_id = ?", mb.ID).Delete(&models.MailFolder{}).Error; err != nil {
			return fmt.Errorf("delete folders of mailbox %d: %w", mb.ID, err)
		}
		if err := tx.Delete(&models.Mailbox{}, mb.ID).Error; err != nil {
			return fmt.Errorf("delete mailbox %d: %w", mb.ID, err)
		}
		return nil
	})
}
