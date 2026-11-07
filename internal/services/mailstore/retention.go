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
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// Mailstore retention (ADR-003 decision 7, SF-81): per-mailbox and
// per-folder policies (age days + max message count) live as settings on the
// mailbox/folder rows; the unified retention cron (internal/cron/jobs) drives
// RetentionSweeper, which purges only in-policy messages, keeps the mailbox
// counters in sync inside the same transaction, and then reclaims blob refs
// whose count dropped to zero together with their stored objects.
//
// Age is measured from mail_messages.created_at — the server-controlled
// ingest timestamp — never from the sender-supplied Date header, which an
// attacker can forge to age a message out of (or dodge) a window. Size-based
// policies (the third axis in ADR-003 §7) are a documented residual: this
// wave implements the age + count axes the wave brief names.

// SetMailboxRetention sets the mailbox-level retention policy (0 = keep
// forever / no count cap). Negative values are rejected.
func (s *Service) SetMailboxRetention(mailboxID, userID, workspaceID uint, days, maxCount int) error {
	if days < 0 || maxCount < 0 {
		return errors.New("mailstore: retention values must not be negative")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.resolveMailbox(tx, mailboxID, userID, workspaceID); err != nil {
			return err
		}
		return tx.Model(&models.Mailbox{}).Where("id = ?", mailboxID).
			Updates(map[string]interface{}{"retention_days": days, "retention_max_count": maxCount}).Error
	})
}

// SetMailboxMessageQuota sets the per-mailbox message-count quota; 0 lifts
// the limit (ADR-003 decision 5). The byte quota sibling is SetMailboxQuota.
func (s *Service) SetMailboxMessageQuota(mailboxID, userID, workspaceID uint, maxMessages int64) error {
	if maxMessages < 0 {
		return errors.New("mailstore: message quota must not be negative")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.resolveMailbox(tx, mailboxID, userID, workspaceID); err != nil {
			return err
		}
		return tx.Model(&models.Mailbox{}).Where("id = ?", mailboxID).
			Update("quota_messages", maxMessages).Error
	})
}

// SetFolderRetention sets the per-folder retention override (0 = inherit the
// owning mailbox's policy for that axis).
func (s *Service) SetFolderRetention(folderID, userID, workspaceID uint, days, maxCount int) error {
	if days < 0 || maxCount < 0 {
		return errors.New("mailstore: retention values must not be negative")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, _, err := s.resolveFolder(tx, folderID, userID, workspaceID); err != nil {
			return err
		}
		return tx.Model(&models.MailFolder{}).Where("id = ?", folderID).
			Updates(map[string]interface{}{"retention_days": days, "retention_max_count": maxCount}).Error
	})
}

// effectiveRetention resolves the folder-over-mailbox precedence: a positive
// folder value overrides the mailbox value on its axis; 0 inherits.
func effectiveRetention(folder *models.MailFolder, mailbox *models.Mailbox) (days, maxCount int) {
	days = mailbox.RetentionDays
	if folder.RetentionDays > 0 {
		days = folder.RetentionDays
	}
	maxCount = mailbox.RetentionMaxCount
	if folder.RetentionMaxCount > 0 {
		maxCount = folder.RetentionMaxCount
	}
	return days, maxCount
}

// purgeMessageRows deletes the given messages of one mailbox inside the
// caller's transaction and returns the purged count and byte size. Order
// matters: the mailbox counters are bumped FIRST (taking the mailbox row
// lock), then blob refs are decremented, then the rows are deleted. Every
// purge path (expunge, folder/mailbox delete, retention sweep) therefore
// acquires the mailbox row before any blob-ref row — the same order as the
// append path's quota check (mailbox FOR UPDATE → blob-ref upsert). The
// reverse order would allow an append holding the mailbox row and waiting on
// a blob-ref row to AB-BA deadlock against a purge holding that blob-ref row
// and waiting on the mailbox row.
func purgeMessageRows(tx *gorm.DB, victims []models.MailMessage, mailboxID uint) (int64, int64, error) {
	if len(victims) == 0 {
		return 0, 0, nil
	}
	ids := make([]uint, 0, len(victims))
	var bytes int64
	for _, m := range victims {
		ids = append(ids, m.ID)
		bytes += m.Size
	}
	if err := bumpMailboxCounters(tx, mailboxID, -bytes, -int64(len(ids))); err != nil {
		return 0, 0, err
	}
	for i := range victims {
		if err := decrementBlobRefs(tx, messageBlobHashes(&victims[i])); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Where("id IN ?", ids).Delete(&models.MailMessage{}).Error; err != nil {
		return 0, 0, fmt.Errorf("purge %d messages of mailbox %d: %w", len(ids), mailboxID, err)
	}
	return int64(len(ids)), bytes, nil
}

// RetentionStats reports one sweep's outcome.
type RetentionStats struct {
	MailboxesScanned   int64 `json:"mailboxes_scanned"`
	FoldersScanned     int64 `json:"folders_scanned"`
	PurgedMessages     int64 `json:"purged_messages"`
	PurgedBytes        int64 `json:"purged_bytes"`
	BlobRefsReclaimed  int64 `json:"blob_refs_reclaimed"`
	ReclaimedBlobBytes int64 `json:"reclaimed_blob_bytes"`
}

// mailBlobReclaimGrace is the default orphan grace period (OB-3 W2, GAP-45):
// a blob whose ref count reached zero is only reclaimable after it has been
// unreferenced AND untouched for this long (last_referenced is stamped on
// every reference event). It keeps a blob that a slow client or a restore is
// about to re-reference from being collected under a racing sweep.
const mailBlobReclaimGrace = 24 * time.Hour

// RetentionSweeper executes the mailstore retention family for the unified
// retention cron (internal/cron/jobs/retention.go). It is deliberately
// independent of the blob codec — it only deletes rows and unreferenced blob
// objects — so it can be wired before the compress/encrypt codec lands.
type RetentionSweeper struct {
	db    *gorm.DB
	blobs blob.Store // nil disables blob reclamation (see reclaimBlobs)
	// ReclaimGrace bounds how long a ref_count <= 0 blob must have been
	// unreferenced before reclamation. <= 0 selects the default
	// (mailBlobReclaimGrace); tests shorten it.
	ReclaimGrace time.Duration
}

// NewRetentionSweeper wires the sweeper over the primary Postgres handle and
// the blob store (nil store is accepted: messages are still purged, ref rows
// at <= 0 stay for a sweep with a store wired — deleting the rows without the
// objects would orphan the objects permanently).
func NewRetentionSweeper(db *gorm.DB, blobs blob.Store) (*RetentionSweeper, error) {
	if db == nil {
		return nil, errors.New("mailstore: nil db")
	}
	return &RetentionSweeper{db: db, blobs: blobs}, nil
}

// SweepRetention purges in-policy messages mailbox by mailbox and then
// reclaims unreferenced blobs. Workspace scoping: every mutation is keyed
// through the folder → mailbox chain loaded from that mailbox's own rows, so
// one workspace's policies can never touch another workspace's rows. Each
// mailbox purges in its own transaction — a failure bounds the damage to that
// mailbox and never loses an out-of-policy message.
func (s *RetentionSweeper) SweepRetention(ctx context.Context) (RetentionStats, error) {
	var stats RetentionStats
	var mailboxes []models.Mailbox
	if err := s.db.Find(&mailboxes).Error; err != nil {
		return stats, fmt.Errorf("mailstore retention: list mailboxes: %w", err)
	}
	for i := range mailboxes {
		mb := &mailboxes[i]
		purged, purgedBytes, folders, err := s.sweepMailbox(mb)
		if err != nil {
			return stats, fmt.Errorf("mailstore retention: mailbox %d (%s): %w", mb.ID, mb.Name, err)
		}
		stats.MailboxesScanned++
		stats.FoldersScanned += folders
		stats.PurgedMessages += purged
		stats.PurgedBytes += purgedBytes
	}
	if s.blobs != nil {
		reclaimed, reclaimedBytes, err := s.reclaimBlobs(ctx)
		if err != nil {
			return stats, err
		}
		stats.BlobRefsReclaimed = reclaimed
		stats.ReclaimedBlobBytes = reclaimedBytes
	}
	return stats, nil
}

// MailboxBlobUsageAll reports GAP-45 per-mailbox blob bytes from
// mail_blob_refs (dedup-aware). It lets the unified retention cron audit the
// reclamation outcome against what the store actually holds, through the same
// wiring that drives the sweep — the mailstore Service (whose ownership-
// scoped MailboxUsage is the user-facing surface) shares this implementation.
func (s *RetentionSweeper) MailboxBlobUsageAll() ([]MailboxUsageReport, error) {
	return MailboxUsageAll(s.db)
}

// sweepMailbox applies the effective per-folder policies of one mailbox.
func (s *RetentionSweeper) sweepMailbox(mb *models.Mailbox) (purged, purgedBytes, folders int64, err error) {
	var folderRows []models.MailFolder
	if err := s.db.Where("mailbox_id = ?", mb.ID).Find(&folderRows).Error; err != nil {
		return 0, 0, 0, fmt.Errorf("list folders: %w", err)
	}
	for i := range folderRows {
		f := &folderRows[i]
		folders++
		days, maxCount := effectiveRetention(f, mb)
		if days <= 0 && maxCount <= 0 {
			continue // no policy on this folder
		}
		victims, err := retentionVictims(s.db, f.ID, days, maxCount)
		if err != nil {
			return 0, 0, 0, err
		}
		if len(victims) == 0 {
			continue
		}
		var n, b int64
		txErr := s.db.Transaction(func(tx *gorm.DB) error {
			var innerErr error
			n, b, innerErr = purgeMessageRows(tx, victims, mb.ID)
			return innerErr
		})
		if txErr != nil {
			return 0, 0, 0, fmt.Errorf("purge folder %d: %w", f.ID, txErr)
		}
		purged += n
		purgedBytes += b
	}
	return purged, purgedBytes, folders, nil
}

// retentionVictims collects the messages of one folder that the effective
// policy removes: everything past the age window (created_at < cutoff) plus,
// when a count cap applies, the oldest messages beyond the cap (ordered by
// ingest time, then UID). The union is deduplicated by ID, so a message that
// is both old and over the count is purged exactly once.
func retentionVictims(db *gorm.DB, folderID uint, days, maxCount int) ([]models.MailMessage, error) {
	var victims []models.MailMessage
	seen := map[uint]bool{}
	add := func(rows []models.MailMessage) {
		for _, m := range rows {
			if !seen[m.ID] {
				seen[m.ID] = true
				victims = append(victims, m)
			}
		}
	}
	if days > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -days)
		var aged []models.MailMessage
		if err := db.Where("folder_id = ? AND created_at < ?", folderID, cutoff).
			Find(&aged).Error; err != nil {
			return nil, fmt.Errorf("collect aged messages of folder %d: %w", folderID, err)
		}
		add(aged)
	}
	if maxCount > 0 {
		var total int64
		if err := db.Model(&models.MailMessage{}).Where("folder_id = ?", folderID).
			Count(&total).Error; err != nil {
			return nil, fmt.Errorf("count messages of folder %d: %w", folderID, err)
		}
		if excess := total - int64(maxCount); excess > 0 {
			var oldest []models.MailMessage
			if err := db.Where("folder_id = ?", folderID).
				Order("created_at ASC, uid ASC").Limit(int(excess)).
				Find(&oldest).Error; err != nil {
				return nil, fmt.Errorf("collect oldest messages of folder %d: %w", folderID, err)
			}
			add(oldest)
		}
	}
	return victims, nil
}

// reclaimBlobs deletes stored objects for blob refs that have been
// unreferenced past the grace period (OB-3 W2, GAP-45), then the rows
// themselves. Per candidate hash, inside ONE transaction that holds the
// per-hash advisory lock (lockMailBlob, service.go):
//
//	DELETE ... WHERE blob_hash = ? AND ref_count <= 0   (re-check)
//	blob store Delete
//
// The lock totally orders reclamation against the append path's reservation
// (Append's reserve transaction takes the same lock around its ref upsert and
// blob Put): a reservation that commits first makes the re-check match 0 rows,
// and a reclamation that commits first is followed by the racing reservation
// re-Putting the object. Without the lock, a sweep could delete an object
// between a racing reserve's row insert and its Put, leaving a live
// ref_count >= 1 row whose blob is gone — a lost message. A failed object
// delete rolls the row delete back, so the candidate is retried by the next
// sweep instead of becoming an untracked orphan.
//
// Grace: candidates are rows with ref_count <= 0 whose last reference event
// (last_referenced, falling back to created_at for pre-column rows) is older
// than ReclaimGrace. This pass never touches a blob that was referenced or
// released within the grace window.
//
// Blobs orphaned before any ref row existed (a failed reserve transaction
// whose blob Put still landed) have no row to reclaim and are out of scope
// here — enumerating the blob store for orphans is the remaining GAP-45 half,
// not this ref-count-driven pass (blob.Store has no listing API).
func (s *RetentionSweeper) reclaimBlobs(ctx context.Context) (int64, int64, error) {
	grace := s.ReclaimGrace
	if grace <= 0 {
		grace = mailBlobReclaimGrace
	}
	cutoff := time.Now().UTC().Add(-grace)
	var candidates []struct {
		BlobHash string `gorm:"column:blob_hash"`
		Size     int64  `gorm:"column:size"`
	}
	if err := s.db.Raw(
		`SELECT blob_hash, size FROM mail_blob_refs
		 WHERE ref_count <= 0 AND COALESCE(last_referenced, created_at) < ?`,
		cutoff,
	).Scan(&candidates).Error; err != nil {
		return 0, 0, fmt.Errorf("mailstore retention: list reclaimable blob refs: %w", err)
	}
	var reclaimed, bytes int64
	for _, c := range candidates {
		if strings.TrimSpace(c.BlobHash) == "" {
			continue
		}
		err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := lockMailBlob(tx, c.BlobHash); err != nil {
				return err
			}
			res := tx.Exec(
				`DELETE FROM mail_blob_refs WHERE blob_hash = ? AND ref_count <= 0`,
				c.BlobHash,
			)
			if res.Error != nil {
				return fmt.Errorf("delete dead blob ref %s: %w", c.BlobHash, res.Error)
			}
			if res.RowsAffected == 0 {
				// Re-referenced between the candidate SELECT and the lock —
				// the blob is live again; skip silently.
				return nil
			}
			if err := s.blobs.Delete(ctx, BlobObjectKey(c.BlobHash)); err != nil {
				// Roll the row delete back: the candidate stays a candidate
				// (ref_count <= 0, aged) and is retried by the next sweep —
				// never an untracked orphan.
				return fmt.Errorf("delete reclaimed blob object %s: %w", c.BlobHash, err)
			}
			return nil
		})
		if err != nil {
			// Log and continue — never abort the sweep over one blob.
			logger.Warn("mailstore retention: blob reclamation skipped for hash",
				"hash", c.BlobHash, "error", err.Error())
			continue
		}
		reclaimed++
		bytes += c.Size
	}
	return reclaimed, bytes, nil
}
