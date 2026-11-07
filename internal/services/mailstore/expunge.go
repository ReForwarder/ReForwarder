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
	"fmt"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// ExpungeResult reports what a purge removed.
type ExpungeResult struct {
	Expunged  int64
	ByteDelta int64
}

// Expunge permanently removes the \Deleted-flagged messages of a folder
// (IMAP EXPUNGE/CLOSE semantics), decrementing blob ref counts and the
// mailbox counters in the same transaction.
//
// uid_validity policy (ADR-003 §6): expunge does NOT bump uid_validity and
// does NOT rewind uid_next — surviving messages keep their UIDs, removed ones
// leave permanent holes (RFC 9051 §2.3.1.2). uid_validity changes only when a
// folder's whole message set is destroyed: folder deletion is destructive and
// a later recreate mints a fresh random UIDValidity (see createFolderRow).
func (s *Service) Expunge(folderID, userID, workspaceID uint) (*ExpungeResult, error) {
	var out ExpungeResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		f, _, err := s.resolveFolder(tx, folderID, userID, workspaceID)
		if err != nil {
			return err
		}
		out, err = purgeDeletedMessages(tx, f.ID, f.MailboxID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// purgeDeletedMessages purges \Deleted messages of one folder inside the
// caller's transaction (mailboxID is known from the resolved folder).
func purgeDeletedMessages(tx *gorm.DB, folderID, mailboxID uint) (ExpungeResult, error) {
	var victims []models.MailMessage
	if err := tx.Where("folder_id = ? AND (flags & ?) > 0", folderID, models.FlagDeleted).
		Find(&victims).Error; err != nil {
		return ExpungeResult{}, fmt.Errorf("collect deleted messages of folder %d: %w", folderID, err)
	}
	purged, bytes, err := purgeMessageRows(tx, victims, mailboxID)
	if err != nil {
		return ExpungeResult{}, err
	}
	return ExpungeResult{Expunged: purged, ByteDelta: -bytes}, nil
}

// purgeFolderMessages purges ALL messages of a folder (folder/mailbox delete
// cascade) inside the caller's transaction, decrementing ref counts.
func purgeFolderMessages(tx *gorm.DB, folderID, mailboxID uint) error {
	var all []models.MailMessage
	if err := tx.Where("folder_id = ?", folderID).Find(&all).Error; err != nil {
		return fmt.Errorf("collect messages of folder %d: %w", folderID, err)
	}
	_, _, err := purgeMessageRows(tx, all, mailboxID)
	return err
}
