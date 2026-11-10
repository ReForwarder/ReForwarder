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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// Session-level helpers for the webmail compose seam (OB-5 wave W3, ADR-005:
// "mailstore gains a small set of session-level helpers"). These are the only
// primitives the compose service needs that Append/Expunge do not already
// expose: a UID-scoped message lookup (drafts are addressed by folder UID on
// the REST surface), and ref-counted blob reservation/release for attachment
// uploads that are staged before any message references them.

// GetMessageByUID resolves one message by its folder UID with the same
// ownership scoping as every other read: the folder must belong to userID.
// Webmail addresses drafts and messages by (folder, uid) on the wire — UIDs
// are per-folder and never reused (ADR-003 §6).
func (s *Service) GetMessageByUID(folderID uint, uid uint32, userID, workspaceID uint) (*models.MailMessage, error) {
	var out *models.MailMessage
	err := s.db.Transaction(func(tx *gorm.DB) error {
		f, _, err := s.resolveFolder(tx, folderID, userID, workspaceID)
		if err != nil {
			return err
		}
		var m models.MailMessage
		if err := tx.Where("folder_id = ? AND uid = ?", f.ID, uid).First(&m).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrMessageNotFound
			}
			return fmt.Errorf("load message uid %d of folder %d: %w", uid, folderID, err)
		}
		out = &m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReserveBlob stores content in the blob layer and reserves one reference for
// the caller, before any message row references it. This is the same
// reservation the Append path performs (per-hash advisory lock → ref_count
// upsert → Put when this caller holds the only reference), so a staged
// upload's bytes are exactly as durable and GC-safe as a stored message's:
// the GC sweep can never collect a blob whose ref_count is >= 1, and a
// reservation is released only through ReleaseBlob.
//
// The content address is the SHA-256 of the PLAINTEXT bytes (the codec's
// identity), so reserving content that is already stored dedups to a
// ref-count bump.
func (s *Service) ReserveBlob(ctx context.Context, content []byte) (BlobRef, error) {
	stored, ref, err := s.codec.Encode(content)
	if err != nil {
		return BlobRef{}, fmt.Errorf("mailstore: encode staged attachment: %w", err)
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockMailBlob(tx, ref.Hash); err != nil {
			return err
		}
		rc, err := upsertBlobRef(tx, ref)
		if err != nil {
			return err
		}
		if rc > 1 {
			// Other live references exist, so the object should be present;
			// verify rather than trust and self-heal by re-Putting when the
			// read fails (same policy as Append's reserve phase).
			body, getErr := s.store.Get(ctx, BlobObjectKey(ref.Hash))
			if getErr == nil {
				_ = body.Close()
				return nil
			}
		}
		if err := s.store.Put(ctx, BlobObjectKey(ref.Hash), bytes.NewReader(stored), "application/octet-stream"); err != nil {
			return fmt.Errorf("mailstore: store staged blob %s: %w", ref.Hash, err)
		}
		return nil
	}); err != nil {
		return BlobRef{}, err
	}
	return ref, nil
}

// ReleaseBlob drops one reference of a staged content address. The blob object
// itself, when the reference was the last, is reclaimed by the GC sweep after
// the grace period — never inline, so a concurrent reader of the same content
// address cannot observe a disappearing object. A release for a hash that is
// not referenced is a no-op error (ref under-counted by callers is a bug).
func (s *Service) ReleaseBlob(_ context.Context, hash string) error {
	if hash == "" {
		return errors.New("mailstore: release blob: empty hash")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&models.MailBlobRef{}).Where("blob_hash = ?", hash).Count(&n).Error; err != nil {
			return fmt.Errorf("mailstore: load blob ref %s: %w", hash, err)
		}
		if n == 0 {
			return fmt.Errorf("mailstore: release blob %s: %w", hash, ErrBlobCorrupt)
		}
		return decrementBlobRefs(tx, []string{hash})
	})
}

// ReadBlob returns the decoded plaintext of one content-addressed blob. The
// ref must exist (readBlob fails with ErrBlobCorrupt otherwise); caller-side
// ownership comes from the compose staging row that named the hash.
func (s *Service) ReadBlob(ctx context.Context, hash string) ([]byte, error) {
	return s.readBlob(ctx, hash)
}

// SweepStaleAttachmentUploads drops the staging rows (and their blob
// reservations) of one user that are older than maxAge and are still
// unreferenced by any send: an upload whose bytes never made it into a stored
// message would otherwise pin its blob forever (ref_count >= 1 blocks the GC).
// The compose service calls this opportunistically on upload and accepted
// send; it is scoped to the acting user, so one user's housekeeping never
// touches another's rows.
func (s *Service) SweepStaleAttachmentUploads(_ context.Context, userID uint, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	var stale []models.MailAttachmentUpload
	if err := s.db.Where("user_id = ? AND created_at < ?", userID, cutoff).Find(&stale).Error; err != nil {
		return 0, fmt.Errorf("mailstore: list stale attachment uploads of user %d: %w", userID, err)
	}
	if len(stale) == 0 {
		return 0, nil
	}
	var swept int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		hashes := make([]string, 0, len(stale))
		for _, u := range stale {
			hashes = append(hashes, u.BlobHash)
		}
		if err := tx.Where("user_id = ? AND created_at < ?", userID, cutoff).
			Delete(&models.MailAttachmentUpload{}).Error; err != nil {
			return fmt.Errorf("mailstore: delete stale attachment uploads of user %d: %w", userID, err)
		}
		swept = int64(len(stale))
		return decrementBlobRefs(tx, hashes)
	})
	if err != nil {
		return 0, err
	}
	return swept, nil
}
