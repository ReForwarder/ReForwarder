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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/goposta/posta/internal/models"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// maxSearchBodyBytes bounds the BodyText kept in Postgres for FTS (ADR-003:
// body-text in the tsvector; the full message stays in the blob store).
const maxSearchBodyBytes = 256 * 1024

// AppendIn is the caller-supplied ingest request.
type AppendIn struct {
	MailboxID uint // informational (FolderID is authoritative); validated on resolve
	FolderID  uint // target folder; resolve paths via GetFolderByPath first
	Raw       []byte
	// Flags to set at append (e.g. FlagSeen when ingestion pre-marks read).
	// FlagDeleted may not be preset here; expunge is deliberate, not ingest.
	Flags uint32
}

// AppendResult reports the assigned identifiers.
type AppendResult struct {
	MessageID uint
	FolderID  uint
	UID       uint32
	ModSeq    uint64
}

// Append ingests a raw RFC 5322 message for a user: parse → encode
// body/attachments into content-addressed blobs → RESERVE each content hash
// (ref_count++ under the per-hash advisory lock, with the blob Put inside the
// same locked transaction) → claim the folder's next UID atomically → insert
// the message row → bump the mailbox counters, inside one transaction. The
// folder must belong to userID.
//
// Durability order ("never lose a message"): blob bytes are written to
// blob.Store BEFORE the message transaction commits; if that transaction
// fails the reservations are compensated (ref counts dropped) and the store
// holds at most unreferenced blobs that the GC sweep reclaims — the inverse
// (a committed message whose blob is missing) cannot happen, because a
// message row is only inserted after every referenced hash is reserved with
// ref_count >= 1, and the GC only ever collects rows still at ref_count <= 0
// inside the same per-hash advisory lock (see lockMailBlob in service.go and
// RetentionSweeper.reclaimBlobs). The lock totally orders reservation against
// reclamation per content address:
//   - reservation first → ref_count >= 1 committed → the GC re-check (inside
//     the lock) matches 0 rows → a referenced blob is never collected;
//   - GC first → dead row + object gone → a racing reservation re-inserts the
//     ref and re-Puts the blob (idempotent for a content address).
//
// Dedup happens at reserve time: the upsert returns the post-increment
// ref_count; 1 means this append is the only reference (possibly fresh) so
// the blob must be Put; > 1 means other live references exist and the blob is
// probed — a successful read skips the write, a failed one self-heals by
// re-Putting. Both the probe and the Put run under the lock, so no GC sweep
// can delete the object between the check and the decision.
func (s *Service) Append(ctx context.Context, userID, workspaceID uint, in AppendIn) (*AppendResult, error) {
	if len(in.Raw) == 0 {
		return nil, ErrEmptyMessage
	}
	parsed, err := ParseMessage(in.Raw)
	if err != nil {
		return nil, err
	}
	bodyText, snippet := deriveSearchText(parsed)
	flags := in.Flags &^ models.FlagDeleted // ingest never pre-deletes

	// Phase A: encode every content item.
	type pendingBlob struct {
		ref    BlobRef
		stored []byte
	}
	pending := make([]pendingBlob, 0, 1+len(parsed.Attachments))
	{
		stored, ref, err := s.codec.Encode(in.Raw)
		if err != nil {
			return nil, fmt.Errorf("encode message body: %w", err)
		}
		pending = append(pending, pendingBlob{ref: ref, stored: stored})
	}
	attRefs := make([]models.MailAttachmentRef, 0, len(parsed.Attachments))
	for _, a := range parsed.Attachments {
		stored, ref, err := s.codec.Encode(a.Content)
		if err != nil {
			return nil, fmt.Errorf("encode attachment %q: %w", a.Filename, err)
		}
		pending = append(pending, pendingBlob{ref: ref, stored: stored})
		attRefs = append(attRefs, models.MailAttachmentRef{
			Name:        a.Filename,
			ContentType: a.ContentType,
			Size:        int64(len(a.Content)),
			BlobHash:    ref.Hash,
		})
	}
	attachmentsJSON, err := json.Marshal(attRefs)
	if err != nil {
		return nil, fmt.Errorf("marshal attachment refs: %w", err)
	}

	size := int64(len(in.Raw))
	bodyRef := pending[0].ref

	// Cheap pre-check so a doomed append (wrong owner, over quota, inactive
	// mailbox) never reserves blobs. Phase B re-checks authoritatively inside
	// its transaction (the enforcer re-reads the mailbox FOR UPDATE there, so
	// concurrent appends to one mailbox still serialize before deciding).
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		_, mb, err := s.resolveFolder(tx, in.FolderID, userID, workspaceID)
		if err != nil {
			return err
		}
		return s.quota.CheckAppend(tx, mb, size)
	}); err != nil {
		return nil, err
	}

	// Reserve phase: one transaction takes the per-hash advisory lock for
	// every pending content address, bumps its ref count, and Puts the blob
	// when this append is responsible for the bytes. Committing here makes
	// ref_count >= 1 durable BEFORE the message exists — exactly what makes
	// the GC sweep unable to collect these blobs mid-append.
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		for i, p := range pending {
			if i > 0 && p.ref.Hash == pending[i-1].ref.Hash {
				continue // duplicated content within one message: reserved once
			}
			if err := lockMailBlob(tx, p.ref.Hash); err != nil {
				return err
			}
			rc, err := upsertBlobRef(tx, p.ref)
			if err != nil {
				return err
			}
			if rc > 1 {
				// Other live references exist, so the blob should be present;
				// verify rather than trust (a crashed reservation may have
				// left a ref count without bytes — self-heal by re-Putting).
				body, err := s.store.Get(ctx, BlobObjectKey(p.ref.Hash))
				if err == nil {
					_ = body.Close()
					continue
				}
			}
			if err := s.store.Put(ctx, BlobObjectKey(p.ref.Hash), bytes.NewReader(p.stored), "application/octet-stream"); err != nil {
				return fmt.Errorf("store blob %s: %w", p.ref.Hash, err)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Phase B (one tx): ownership + quota + status re-check, UID claim, row
	// insert, counter bump. Ref counts were reserved above and are NOT
	// touched here. Any failure rolls the message back and compensates the
	// reservations; blobs stay unreferenced for the GC sweep.
	var result AppendResult
	err = s.db.Transaction(func(tx *gorm.DB) error {
		f, mb, err := s.resolveFolder(tx, in.FolderID, userID, workspaceID)
		if err != nil {
			return err
		}
		// The enforcer re-reads the mailbox row FOR UPDATE inside this
		// transaction, so concurrent appends to one mailbox serialize before
		// the decision and the counters below can never overshoot a quota.
		if err := s.quota.CheckAppend(tx, mb, size); err != nil {
			return err
		}
		if mb.Status != models.MailboxStatusActive {
			return ErrMailboxInactive
		}
		uid, modSeq, err := claimUIDAndModSeq(tx, f.ID)
		if err != nil {
			return err
		}
		msg := &models.MailMessage{
			FolderID:        f.ID,
			UID:             uid,
			MessageID:       parsed.MessageID,
			Subject:         parsed.Subject,
			FromAddr:        parsed.From,
			ToAddr:          strings.Join(parsed.To, ", "),
			Cc:              strings.Join(parsed.Cc, ", "),
			Date:            parsed.Date,
			Size:            size,
			Flags:           flags | models.FlagRecent,
			ModSeq:          modSeq,
			Snippet:         snippet,
			BodyBlobRef:     bodyRef.Hash,
			BodyText:        bodyText,
			BodySize:        bodyRef.Size,
			AttachmentsJSON: string(attachmentsJSON),
		}
		if err := tx.Create(msg).Error; err != nil {
			return fmt.Errorf("insert message: %w", err)
		}
		if err := bumpMailboxCounters(tx, mb.ID, size, 1); err != nil {
			return err
		}
		result = AppendResult{MessageID: msg.ID, FolderID: f.ID, UID: uid, ModSeq: modSeq}
		return nil
	})
	if err != nil {
		// Compensate the reservations: the message never landed, so the ref
		// counts it took must go back. Best-effort — a failure here leaves an
		// over-counted ref (a benign leak the GC will not collect, not a
		// correctness hole) and is logged loudly.
		if compErr := s.db.Transaction(func(tx *gorm.DB) error {
			hashes := make([]string, 0, len(pending))
			for i, p := range pending {
				if i > 0 && p.ref.Hash == pending[i-1].ref.Hash {
					continue
				}
				hashes = append(hashes, p.ref.Hash)
			}
			return decrementBlobRefs(tx, hashes)
		}); compErr != nil {
			logger.Error("mailstore: failed to compensate blob reservations",
				"folder_id", in.FolderID, "error", compErr.Error())
		}
		return nil, err
	}
	return &result, nil
}

// upsertBlobRef increments the ref count for a content address, inserting the
// row on first reference, and stamps last_referenced (the GC grace anchor) on
// both paths. It returns the post-increment ref count: 1 means the caller now
// holds the only reference. Raw + parameterized (no SQL concatenation); the
// ON CONFLICT makes racing appends of identical content idempotent.
func upsertBlobRef(tx *gorm.DB, ref BlobRef) (int64, error) {
	now := time.Now().UTC()
	var out struct {
		RefCount int64 `gorm:"column:ref_count"`
	}
	res := tx.Raw(
		`INSERT INTO mail_blob_refs (blob_hash, size, ref_count, created_at, last_referenced)
		 VALUES (?, ?, 1, ?, ?)
		 ON CONFLICT (blob_hash) DO UPDATE
		   SET ref_count = mail_blob_refs.ref_count + 1, last_referenced = ?
		 RETURNING ref_count`,
		ref.Hash, ref.Size, now, now, now,
	).Scan(&out)
	if res.Error != nil {
		return 0, fmt.Errorf("upsert blob ref %s: %w", ref.Hash, res.Error)
	}
	if res.RowsAffected == 0 {
		return 0, fmt.Errorf("upsert blob ref %s: no row returned", ref.Hash)
	}
	return out.RefCount, nil
}

// decrementBlobRefs drops one reference per hash and stamps last_referenced
// (within the caller's transaction): the release is the last reference event,
// so the GC grace window ("ref_count <= 0 older than the grace period") is
// measured from the moment the blob actually became unreferenced.
func decrementBlobRefs(tx *gorm.DB, hashes []string) error {
	for _, h := range hashes {
		if err := tx.Exec(
			`UPDATE mail_blob_refs SET ref_count = ref_count - 1, last_referenced = ? WHERE blob_hash = ?`,
			time.Now().UTC(), h,
		).Error; err != nil {
			return fmt.Errorf("decrement blob ref %s: %w", h, err)
		}
	}
	return nil
}

// bumpMailboxCounters adjusts the denormalized quota counters atomically.
func bumpMailboxCounters(tx *gorm.DB, mailboxID uint, bytesDelta, countDelta int64) error {
	err := tx.Exec(
		`UPDATE mailboxes SET used_bytes = used_bytes + ?, message_count = message_count + ? WHERE id = ?`,
		bytesDelta, countDelta, mailboxID,
	).Error
	if err != nil {
		return fmt.Errorf("bump counters of mailbox %d: %w", mailboxID, err)
	}
	return nil
}

// messageBlobHashes returns the body hash plus every attachment hash of a
// message (for ref-count decrements).
func messageBlobHashes(m *models.MailMessage) []string {
	hashes := []string{m.BodyBlobRef}
	var atts []models.MailAttachmentRef
	if m.AttachmentsJSON != "" {
		if err := json.Unmarshal([]byte(m.AttachmentsJSON), &atts); err == nil {
			for _, a := range atts {
				if a.BlobHash != "" {
					hashes = append(hashes, a.BlobHash)
				}
			}
		}
	}
	return hashes
}

// deriveSearchText produces the FTS body text (bounded) and the snippet from
// the parsed message: text/plain preferred, a tag-stripped HTML fallback
// otherwise.
func deriveSearchText(p *ParsedMessage) (bodyText, snippet string) {
	text := strings.TrimSpace(p.TextBody)
	if text == "" && p.HTMLBody != "" {
		text = stripHTMLTags(p.HTMLBody)
	}
	if utf8.RuneCountInString(text) > maxSearchBodyBytes {
		text = string([]rune(text)[:maxSearchBodyBytes])
	}
	return text, makeSnippet(text)
}

// makeSnippet collapses whitespace and truncates to the snippet column budget.
func makeSnippet(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	const maxSnippetRunes = 250
	if utf8.RuneCountInString(s) > maxSnippetRunes {
		s = string([]rune(s)[:maxSnippetRunes])
	}
	return s
}

// stripHTMLTags is a deliberately crude HTML→text reduction used only when a
// message has no text/plain part. It replaces tags with a single space (block
// boundaries become word boundaries), unescapes the handful of entities the
// snippet/search path cares about — FTS text, not rendering.
func stripHTMLTags(html string) string {
	var b strings.Builder
	depth := 0
	lastSpace := true // leading space is trimmed by the caller anyway
	for _, r := range html {
		switch {
		case r == '<':
			depth++
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
			lastSpace = r == ' ' || r == '\n' || r == '\t' || r == '\r'
		}
	}
	s := b.String()
	for _, pair := range [][2]string{
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}, {"&#39;", "'"}, {"&nbsp;", " "},
	} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	return s
}

// CopyMessage duplicates a message into another folder of the SAME mailbox:
// a fresh UID claim, a new row sharing the existing blobs (ref counts
// incremented — dedup means the bytes are never copied), and a fresh mod_seq.
func (s *Service) CopyMessage(messageID, targetFolderID, userID, workspaceID uint) (*AppendResult, error) {
	var result AppendResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		m, _, mb, err := s.resolveMessage(tx, messageID, userID, workspaceID)
		if err != nil {
			return err
		}
		target, targetMB, err := s.resolveFolder(tx, targetFolderID, userID, workspaceID)
		if err != nil {
			return err
		}
		if target.MailboxID != mb.ID || targetMB.ID != mb.ID {
			return ErrNotMailboxOwner
		}
		if targetMB.Status != models.MailboxStatusActive {
			return ErrMailboxInactive
		}
		// Copies consume message-count quota but no new bytes (dedup shares
		// the stored blobs), so the enforcer sees 0 incoming bytes — it still
		// refuses a mailbox that is already over its byte quota.
		if err := s.quota.CheckAppend(tx, targetMB, 0); err != nil {
			return err
		}
		uid, modSeq, err := claimUIDAndModSeq(tx, target.ID)
		if err != nil {
			return err
		}
		for _, h := range messageBlobHashes(m) {
			var ref models.MailBlobRef
			if err := tx.Where("blob_hash = ?", h).First(&ref).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("blob ref %s missing for message %d: %w", h, m.ID, ErrBlobCorrupt)
				}
				return fmt.Errorf("load blob ref %s: %w", h, err)
			}
			if _, err := upsertBlobRef(tx, BlobRef{Hash: ref.BlobHash, Size: ref.Size}); err != nil {
				return err
			}
		}
		copied := *m
		copied.ID = 0
		copied.FolderID = target.ID
		copied.UID = uid
		copied.ModSeq = modSeq
		copied.Flags &^= models.FlagRecent
		copied.CreatedAt = time.Time{}
		copied.UpdatedAt = time.Time{}
		if err := tx.Create(&copied).Error; err != nil {
			return fmt.Errorf("copy message %d: %w", m.ID, err)
		}
		if err := bumpMailboxCounters(tx, mb.ID, 0, 1); err != nil {
			return err
		}
		result = AppendResult{MessageID: copied.ID, FolderID: target.ID, UID: uid, ModSeq: modSeq}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// MoveMessage relocates a message between folders of the same mailbox: one
// transaction claims the destination UID and rewrites the row (fresh mod_seq,
// \Recent cleared). Byte counters are unchanged — the message stays in its
// mailbox.
func (s *Service) MoveMessage(messageID, targetFolderID, userID, workspaceID uint) (*AppendResult, error) {
	var result AppendResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		m, _, mb, err := s.resolveMessage(tx, messageID, userID, workspaceID)
		if err != nil {
			return err
		}
		target, targetMB, err := s.resolveFolder(tx, targetFolderID, userID, workspaceID)
		if err != nil {
			return err
		}
		if target.MailboxID != mb.ID || targetMB.ID != mb.ID {
			return ErrNotMailboxOwner
		}
		if targetMB.Status != models.MailboxStatusActive {
			return ErrMailboxInactive
		}
		uid, modSeq, err := claimUIDAndModSeq(tx, target.ID)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.MailMessage{}).Where("id = ?", m.ID).
			Updates(map[string]interface{}{
				"folder_id": target.ID,
				"uid":       uid,
				"mod_seq":   modSeq,
				"flags":     m.Flags &^ models.FlagRecent,
			}).Error; err != nil {
			return fmt.Errorf("move message %d: %w", m.ID, err)
		}
		result = AppendResult{MessageID: m.ID, FolderID: target.ID, UID: uid, ModSeq: modSeq}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
