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

// Package mailstore is the Postgres-backed hosted-mailbox store (Option B
// Phase 3, ADR-003). Postgres holds metadata (mailboxes, mail_folders,
// mail_messages, mail_blob_refs); message bodies and attachments are
// content-addressed, compressed and encrypted blobs in blob.Store. It is
// explicitly NOT the transactional Email model — the two stores share no rows
// and meet only at forwarding (ADR-002) and webmail compose (ADR-005).
//
// Concurrency contract (mandatory, verified by tests):
//   - UID assignment is a single atomic statement per folder
//     (UPDATE mail_folders SET uid_next = uid_next + 1 ... RETURNING), chosen
//     over SELECT ... FOR UPDATE because one statement is atomic under
//     READ COMMITTED, holds the row lock to commit, and needs no separate
//     round-trip. Concurrent appends to a folder get distinct, monotonic UIDs.
//   - Flag stores are CAS on the message mod_seq (R-26 row-level CAS shape:
//     one UPDATE ... WHERE id = ? AND mod_seq = ?, success decided by
//     RowsAffected), giving OB-4 UNCHANGEDSINCE semantics.
package mailstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// Typed errors. Callers (OB-4 IMAP/POP3, OB-5 webmail, ingestion) map these to
// protocol responses; the service never returns ambiguous bare errors for
// expected outcomes.
var (
	ErrMailboxNotFound   = errors.New("mailstore: mailbox not found")
	ErrFolderNotFound    = errors.New("mailstore: folder not found")
	ErrMessageNotFound   = errors.New("mailstore: message not found")
	ErrMailboxInactive   = errors.New("mailstore: mailbox is not active")
	ErrFolderExists      = errors.New("mailstore: folder already exists")
	ErrFolderImmutable   = errors.New("mailstore: INBOX cannot be renamed or deleted")
	ErrNotMailboxOwner   = errors.New("mailstore: mailbox belongs to another user")
	ErrQuotaExceeded     = errors.New("mailstore: mailbox quota exceeded")
	ErrFlagConflict      = errors.New("mailstore: flag store conflict (message changed since last seen)")
	ErrSearchUnavailable = errors.New("mailstore: full-text search not available")
	ErrSearchQueryTooLong = errors.New("mailstore: search query too long")
	ErrBlobCorrupt       = errors.New("mailstore: stored blob failed integrity check")
	ErrEmptyMessage      = errors.New("mailstore: refusing to store an empty message")
)

// BlobRef is the content address of one stored content item. Hash is the
// SHA-256 hex of the PLAINTEXT bytes (the dedup identity); Size is the
// plaintext size. Encoded stores may differ byte-wise from the plaintext
// (compression + encryption), so the object key is derived from the hash and
// the stored form is self-describing.
type BlobRef struct {
	Hash string
	Size int64
}

// BlobCodec round-trips content through the stored form: compress (zstd for
// bodies > 4 KiB, ADR-003 decision 4) → encrypt (AES-256-GCM, per-workspace
// data keys) → content address.
//
// W1 defines the interface and ships the pass-through IdentityBlobCodec stub;
// the crypto wave replaces it with the real codec (its implementation lives in
// its own file). The codec must be safe for concurrent use.
type BlobCodec interface {
	// Encode returns the stored (compressed + encrypted) form of plaintext
	// together with its content address.
	Encode(plaintext []byte) (stored []byte, ref BlobRef, err error)
	// Decode reverses Encode. Implementations must verify the decoded
	// plaintext matches ref.Hash and report ErrBlobCorrupt otherwise.
	Decode(stored []byte, ref BlobRef) (plaintext []byte, err error)
}

// IdentityBlobCodec is the W1 stub codec: content-addressed pass-through, no
// compression, no encryption. It keeps the append/fetch path end-to-end
// testable and compilable before the crypto wave lands the real
// compress+encrypt codec; wiring chooses it only for development. It is
// intentionally NOT installed in production configuration by this wave.
type IdentityBlobCodec struct{}

// Encode stores plaintext as-is, addressed by its SHA-256.
func (IdentityBlobCodec) Encode(plaintext []byte) ([]byte, BlobRef, error) {
	sum := sha256.Sum256(plaintext)
	return plaintext, BlobRef{Hash: hex.EncodeToString(sum[:]), Size: int64(len(plaintext))}, nil
}

// Decode returns the stored bytes after verifying the content address.
func (IdentityBlobCodec) Decode(stored []byte, ref BlobRef) ([]byte, error) {
	sum := sha256.Sum256(stored)
	if hex.EncodeToString(sum[:]) != ref.Hash {
		return nil, ErrBlobCorrupt
	}
	return stored, nil
}

// QuotaEnforcer decides whether an append is admitted: per-mailbox byte
// quota, per-mailbox message-count quota, and the workspace aggregate byte
// ceiling (ADR-003 decision 5). Implemented by DefaultQuotaEnforcer (quota.go)
// — the quota wave replaced the W1 byte-only stub, as that stub's own note
// anticipated.
type QuotaEnforcer interface {
	// CheckAppend returns a *QuotaError (errors.Is → ErrQuotaExceeded) when
	// admitting a message of incomingBytes bytes would violate a quota. tx is
	// the caller's append transaction (nil = pure per-mailbox checks only);
	// implementations must decide under the mailbox row lock so concurrent
	// appends to one mailbox cannot overshoot a nearly-full quota.
	CheckAppend(tx *gorm.DB, mb *models.Mailbox, incomingBytes int64) error
}

// Searcher is the full-text search API over mail_messages.search_tsv
// (implemented by the search wave in its own file; the stub below keeps W1
// compiling). Queries are parameterized — never string-concatenated.
type Searcher interface {
	// Search runs an FTS query scoped to one mailbox's messages and returns
	// matching message IDs (best-effort relevance order) plus the total.
	Search(ctx context.Context, scope SearchScope, q SearchQuery) ([]SearchHit, int64, error)
}

// SearchScope limits a search to the caller's mailbox.
type SearchScope struct {
	MailboxID uint
	UserID    uint
}

// SearchQuery is a free-text websearch-syntax query (the search wave's final
// shape). Text is parsed by Postgres websearch_to_tsquery — quoted phrases,
// OR and -exclusions are honored there, never in Go. FolderID != 0 narrows the
// search to one folder inside the scoped mailbox (0 = whole mailbox). Limit
// and Offset page the result: Limit <= 0 means DefaultSearchPageSize, values
// above MaxSearchPageSize are clamped; Offset < 0 is treated as 0. Flag
// filters are deliberately absent — OB-4's IMAP SEARCH extends this struct
// when it needs them.
type SearchQuery struct {
	Text     string
	FolderID uint
	Limit    int
	Offset   int
}

// SearchHit is one FTS match.
type SearchHit struct {
	MessageID uint
	FolderID  uint
	Rank      float64
}

// SearcherStub reports ErrSearchUnavailable until the search wave installs the
// real Postgres FTS implementation.
type SearcherStub struct{}

// Search implements Searcher.
func (SearcherStub) Search(context.Context, SearchScope, SearchQuery) ([]SearchHit, int64, error) {
	return nil, 0, ErrSearchUnavailable
}
