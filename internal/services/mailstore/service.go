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
	"github.com/goposta/posta/internal/storage/blob"
	"gorm.io/gorm"
)

// Service is the hosted-mailbox store (ADR-003). All operations are scoped to
// a user (and, when non-zero, a workspace): user A can never read or mutate
// user B's mailboxes, folders or messages — the isolation is enforced here,
// below the future HTTP/admin layers, and covered by tests.
type Service struct {
	db    *gorm.DB
	codec BlobCodec
	store blob.Store
	quota QuotaEnforcer
}

// New wires the service. db is the primary Postgres handle; codec transforms
// content into the stored form; store persists the encoded bytes; quota
// guards appends (nil installs DefaultQuotaEnforcer).
func New(db *gorm.DB, codec BlobCodec, store blob.Store, quota QuotaEnforcer) (*Service, error) {
	if db == nil {
		return nil, errors.New("mailstore: nil db")
	}
	if codec == nil {
		return nil, errors.New("mailstore: nil blob codec")
	}
	if store == nil {
		return nil, errors.New("mailstore: nil blob store")
	}
	if quota == nil {
		quota = DefaultQuotaEnforcer{}
	}
	return &Service{db: db, codec: codec, store: store, quota: quota}, nil
}

// resolveMailbox loads the mailbox owned by userID (workspaceID, when
// non-zero, must also match). Single-table lookup plus in-Go comparison — the
// owner cannot be changed by a concurrent write, so a two-step check has no
// exploitable window here.
func (s *Service) resolveMailbox(tx *gorm.DB, mailboxID, userID, workspaceID uint) (*models.Mailbox, error) {
	var mb models.Mailbox
	if err := tx.First(&mb, mailboxID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMailboxNotFound
		}
		return nil, fmt.Errorf("load mailbox %d: %w", mailboxID, err)
	}
	if mb.UserID != userID || (workspaceID != 0 && mb.WorkspaceID != nil && *mb.WorkspaceID != workspaceID) {
		return nil, ErrNotMailboxOwner
	}
	return &mb, nil
}

// resolveFolder loads the folder and its owning mailbox for userID.
func (s *Service) resolveFolder(tx *gorm.DB, folderID, userID, workspaceID uint) (*models.MailFolder, *models.Mailbox, error) {
	var f models.MailFolder
	if err := tx.First(&f, folderID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrFolderNotFound
		}
		return nil, nil, fmt.Errorf("load folder %d: %w", folderID, err)
	}
	mb, err := s.resolveMailbox(tx, f.MailboxID, userID, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	return &f, mb, nil
}

// resolveMessage loads the message and its folder/mailbox for userID.
func (s *Service) resolveMessage(tx *gorm.DB, messageID, userID, workspaceID uint) (*models.MailMessage, *models.MailFolder, *models.Mailbox, error) {
	var m models.MailMessage
	if err := tx.First(&m, messageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, ErrMessageNotFound
		}
		return nil, nil, nil, fmt.Errorf("load message %d: %w", messageID, err)
	}
	f, mb, err := s.resolveFolder(tx, m.FolderID, userID, workspaceID)
	if err != nil {
		return nil, nil, nil, err
	}
	return &m, f, mb, nil
}

// BlobObjectKey derives the blob.Store object key for a content address. The
// two-character prefix shards the namespace; the mapping is pure so the GC
// sweep (retention wave) and this service always agree.
func BlobObjectKey(hash string) string {
	if len(hash) < 2 {
		return "mail/" + hash
	}
	return "mail/" + hash[:2] + "/" + hash
}

// Mail-blob GC interlock (OB-3 W2, ADR-003 decision 2). Appends RESERVE a
// content hash (ref_count++ under the per-hash advisory lock, with the blob
// Put inside the same locked transaction) before any message references it,
// and the GC sweep deletes a dead row + blob only inside the SAME lock, after
// re-checking ref_count <= 0. The lock therefore totally orders reservation
// against reclamation per hash:
//   - reservation first → ref_count >= 1 committed → GC re-check matches 0
//     rows → the blob is never collected while referenced;
//   - GC first → row + blob gone → a racing reservation re-inserts the ref and
//     re-Puts the blob (idempotent) → the store stays consistent.
//
// Without it, a GC sweep could delete the blob between an append's
// "is it stored?" check and its ref upsert, leaving a committed message whose
// body is gone — the one outcome the mailstore must never produce.
//
// mailBlobLockNamespace namespaces the lock family away from any other
// advisory-lock user; hashtext maps the hex hash to an int4 key.
const (
	mailBlobLockNamespace = 0x4D42 // 'MB' — mail-blob lock family
	mailBlobLockSQL       = `SELECT pg_advisory_xact_lock(?::int, hashtext(?)::int)`
)

// lockMailBlob takes the per-hash advisory lock inside the caller's
// transaction (released at commit/rollback).
func lockMailBlob(tx *gorm.DB, hash string) error {
	if err := tx.Exec(mailBlobLockSQL, mailBlobLockNamespace, hash).Error; err != nil {
		return fmt.Errorf("lock mail blob %s: %w", hash, err)
	}
	return nil
}

// claimUIDAndModSeq allocates the next UID and mod-sequence for a folder with
// one atomic statement. RETURNING yields the post-increment values, so the
// assigned UID is uid_next-1 (the value that was uid_next at claim time).
// Under READ COMMITTED two concurrent transactions serialize on the folder row
// lock; both get distinct UIDs. This is the documented UID-claim decision for
// ADR-003 §6 (see package doc).
func claimUIDAndModSeq(tx *gorm.DB, folderID uint) (uid uint32, modSeq uint64, err error) {
	var out struct {
		ClaimedUID    uint32 `gorm:"column:claimed_uid"`
		ClaimedModSeq uint64 `gorm:"column:claimed_mod_seq"`
	}
	res := tx.Raw(
		`UPDATE mail_folders SET uid_next = uid_next + 1, mod_seq_next = mod_seq_next + 1
		 WHERE id = ?
		 RETURNING uid_next - 1 AS claimed_uid, mod_seq_next - 1 AS claimed_mod_seq`,
		folderID,
	).Scan(&out)
	if res.Error != nil {
		return 0, 0, fmt.Errorf("claim uid for folder %d: %w", folderID, res.Error)
	}
	if res.RowsAffected == 0 {
		return 0, 0, ErrFolderNotFound
	}
	return out.ClaimedUID, out.ClaimedModSeq, nil
}

// claimModSeq allocates the next mod-sequence for a folder (flag stores).
func claimModSeq(tx *gorm.DB, folderID uint) (uint64, error) {
	var out struct {
		ClaimedModSeq uint64 `gorm:"column:claimed_mod_seq"`
	}
	res := tx.Raw(
		`UPDATE mail_folders SET mod_seq_next = mod_seq_next + 1
		 WHERE id = ?
		 RETURNING mod_seq_next - 1 AS claimed_mod_seq`,
		folderID,
	).Scan(&out)
	if res.Error != nil {
		return 0, fmt.Errorf("claim modseq for folder %d: %w", folderID, res.Error)
	}
	if res.RowsAffected == 0 {
		return 0, ErrFolderNotFound
	}
	return out.ClaimedModSeq, nil
}
