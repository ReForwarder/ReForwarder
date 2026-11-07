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
	"encoding/json"
	"fmt"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// GAP-45 usage reporting (OB-3 W2, ADR-003 decision 2): per-mailbox blob
// bytes counted from mail_blob_refs. Two distinct measures exist on purpose:
//
//   - RawBytes (mailboxes.used_bytes) is the quota basis: the sum of message
//     sizes as appended, dedup-blind — two messages sharing one 10 MiB
//     attachment count 20 MiB, which is what the user sees in their client.
//   - BlobBytes is the storage basis: the sum of mail_blob_refs.size over the
//     DISTINCT content hashes the mailbox's messages reference, dedup-aware —
//     the same two messages count the shared blob once. This is what the
//     store actually holds on behalf of the mailbox, and what the GC sweep
//     reclaims (its reclamation shrinks exactly this number).
//
// Attachment hashes come from mail_messages.attachments_json (the repo-wide
// JSON-in-text convention); the hash set is accumulated per mailbox in Go with
// keyed batches so the queries stay portable and memory stays bounded. Sizes
// are read from mail_blob_refs only — blob_refs is the single source of truth
// for content sizes. The aggregation is package-level (used by the Service for
// the ownership-scoped report AND by RetentionSweeper for the GC cron's
// audited summary) so there is exactly one implementation.

// MailboxUsageReport is the GAP-45 usage surface for one mailbox.
type MailboxUsageReport struct {
	MailboxID uint   `json:"mailbox_id"`
	UserID    uint   `json:"user_id"`
	Name      string `json:"name"`
	// QuotaBytes / RawBytes / MessageCount mirror the mailbox row.
	QuotaBytes   int64 `json:"quota_bytes"`
	RawBytes     int64 `json:"raw_bytes"`
	MessageCount int64 `json:"message_count"`
	// BlobBytes is the dedup-aware stored-content total from mail_blob_refs
	// (sum of Size over distinct hashes referenced by this mailbox's
	// messages, body + attachments).
	BlobBytes int64 `json:"blob_bytes"`
	// DistinctBlobs is how many unique content hashes the mailbox references.
	DistinctBlobs int64 `json:"distinct_blobs"`
}

// MailboxUsageAll aggregates blob usage for every mailbox over the given
// handle (the Service's primary DB, or the sweeper's).
func MailboxUsageAll(db *gorm.DB) ([]MailboxUsageReport, error) {
	var mailboxes []models.Mailbox
	if err := db.Find(&mailboxes).Error; err != nil {
		return nil, fmt.Errorf("mailstore usage: list mailboxes: %w", err)
	}
	out := make([]MailboxUsageReport, 0, len(mailboxes))
	for i := range mailboxes {
		mb := &mailboxes[i]
		hashes, err := mailboxBlobHashes(db, mb.ID)
		if err != nil {
			return nil, err
		}
		blobBytes, distinct, err := sumBlobRefSizes(db, hashes)
		if err != nil {
			return nil, err
		}
		out = append(out, MailboxUsageReport{
			MailboxID:     mb.ID,
			UserID:        mb.UserID,
			Name:          mb.Name,
			QuotaBytes:    mb.QuotaBytes,
			RawBytes:      mb.UsedBytes,
			MessageCount:  mb.MessageCount,
			BlobBytes:     blobBytes,
			DistinctBlobs: distinct,
		})
	}
	return out, nil
}

// MailboxBlobUsageAll reports dedup-aware blob bytes for every mailbox —
// the GAP-45 summary the retention cron's GC step logs and audits.
func (s *Service) MailboxBlobUsageAll() ([]MailboxUsageReport, error) {
	return MailboxUsageAll(s.db)
}

// MailboxUsage reports one mailbox's usage with ownership scoping: the
// mailbox must belong to userID (and, when non-zero, the workspace must
// match). No new HTTP route this phase — the dashboard surfaces it in OB-5;
// until then this is the ingestion-seam/admin surface.
func (s *Service) MailboxUsage(mailboxID, userID, workspaceID uint) (*MailboxUsageReport, error) {
	var mb *models.Mailbox
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		loaded, err := s.resolveMailbox(tx, mailboxID, userID, workspaceID)
		mb = loaded
		return err
	}); err != nil {
		return nil, err
	}
	hashes, err := mailboxBlobHashes(s.db, mailboxID)
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

// mailboxBlobHashes returns the distinct content hashes referenced by the
// mailbox's messages (body ref + every attachment ref), batched by message ID
// so memory stays bounded on large mailboxes. Folder IDs are resolved first
// so the message query stays a flat keyset page (parameterized, no
// concatenation, no subquery).
func mailboxBlobHashes(db *gorm.DB, mailboxID uint) (map[string]struct{}, error) {
	var folderIDs []uint
	if err := db.Model(&models.MailFolder{}).Where("mailbox_id = ?", mailboxID).
		Pluck("id", &folderIDs).Error; err != nil {
		return nil, fmt.Errorf("mailstore usage: list folders of mailbox %d: %w", mailboxID, err)
	}
	if len(folderIDs) == 0 {
		return map[string]struct{}{}, nil
	}
	hashes := make(map[string]struct{})
	batch := 500
	var lastID uint
	for {
		var rows []models.MailMessage
		q := db.Where("id > ?", lastID)
		if len(folderIDs) == 1 {
			q = q.Where("folder_id = ?", folderIDs[0])
		} else {
			q = q.Where("folder_id IN ?", folderIDs)
		}
		if err := q.Order("id ASC").Limit(batch).
			Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("mailstore usage: list messages of mailbox %d: %w", mailboxID, err)
		}
		for i := range rows {
			m := &rows[i]
			if m.BodyBlobRef != "" {
				hashes[m.BodyBlobRef] = struct{}{}
			}
			if m.AttachmentsJSON != "" {
				var atts []models.MailAttachmentRef
				if err := json.Unmarshal([]byte(m.AttachmentsJSON), &atts); err == nil {
					for _, a := range atts {
						if a.BlobHash != "" {
							hashes[a.BlobHash] = struct{}{}
						}
					}
				}
			}
			if m.ID > lastID {
				lastID = m.ID
			}
		}
		if len(rows) < batch {
			return hashes, nil
		}
	}
}

// sumBlobRefSizes adds up mail_blob_refs.size over the given distinct hashes.
// The hash set is the dedup: one shared blob is counted once per mailbox.
func sumBlobRefSizes(db *gorm.DB, hashes map[string]struct{}) (blobBytes, distinct int64, err error) {
	if len(hashes) == 0 {
		return 0, 0, nil
	}
	keys := make([]string, 0, len(hashes))
	for h := range hashes {
		keys = append(keys, h)
	}
	var rows []models.MailBlobRef
	if err := db.Where("blob_hash IN ?", keys).Find(&rows).Error; err != nil {
		return 0, 0, fmt.Errorf("mailstore usage: load blob ref sizes: %w", err)
	}
	for i := range rows {
		blobBytes += rows[i].Size
		distinct++
	}
	return blobBytes, distinct, nil
}
