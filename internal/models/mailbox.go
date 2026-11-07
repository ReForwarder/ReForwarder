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

package models

import "time"

// Mailstore models (Option B Phase 3, ADR-003). These are the hosted-mailbox
// storage tables and are deliberately separate from the transactional Email
// model: mailboxes hold ordinary per-user mail (IMAP/POP3/webmail surfaces),
// while Email holds campaign/transactional sends. The two stores meet only at
// ADR-002 forwarding (system messages) and ADR-005 webmail compose.
//
// Postgres holds metadata only; message bodies and attachments are
// content-addressed encrypted blobs in blob.Store (SHA-256 refs), keyed by
// services/mailstore.

// MailboxStatus is the lifecycle state of a hosted mailbox.
type MailboxStatus string

const (
	MailboxStatusActive    MailboxStatus = "active"
	MailboxStatusSuspended MailboxStatus = "suspended"
	MailboxStatusClosed    MailboxStatus = "closed"
)

// Mailbox is a hosted mailbox owned by one user (ADR-003 decision 1).
// QuotaBytes == 0 means unlimited (a deployment-level default may still apply
// via the quota enforcer); UsedBytes and MessageCount are denormalized
// counters maintained transactionally by the mailstore service.
type Mailbox struct {
	ID     uint `json:"id" gorm:"primaryKey"`
	UserID uint `json:"user_id" gorm:"index;not null;uniqueIndex:idx_mailboxes_user_name"`
	// (user_id, name) is unique — one mailbox name per owner.
	WorkspaceID  *uint         `json:"workspace_id,omitempty" gorm:"index"`
	Name         string        `json:"name" gorm:"size:255;not null;uniqueIndex:idx_mailboxes_user_name"`
	QuotaBytes   int64         `json:"quota_bytes" gorm:"not null;default:0"`
	// QuotaMessages caps the mailbox's message count (ADR-003 decision 5);
	// 0 = unlimited. Enforced by the mailstore quota enforcer at append/copy.
	QuotaMessages int64        `json:"quota_messages" gorm:"not null;default:0"`
	UsedBytes    int64         `json:"used_bytes" gorm:"not null;default:0"`
	MessageCount int64         `json:"message_count" gorm:"not null;default:0"`
	Status       MailboxStatus `json:"status" gorm:"size:32;not null;default:'active';index"`
	// Retention policy (ADR-003 decision 7, SF-81): messages older than
	// RetentionDays (counted from the server-controlled ingest time, never
	// the sender-supplied Date header) or beyond RetentionMaxCount (oldest
	// first) are purged by the unified retention cron sweep. 0 = keep
	// forever / no count cap; a folder value overrides the mailbox value.
	RetentionDays     int       `json:"retention_days" gorm:"not null;default:0"`
	RetentionMaxCount int       `json:"retention_max_count" gorm:"not null;default:0"`
	CreatedAt         time.Time `json:"created_at"`
}

// TableName pins the plural table name (GORM would derive it from the struct
// name; pinned for clarity and to keep the raw-SQL FTS step in sync).
func (Mailbox) TableName() string { return "mailboxes" }

// MailFolder is a folder inside a mailbox — the IMAP mailbox entity. The
// hierarchy is expressed with ParentID plus a denormalized IMAPPath
// ("INBOX/Archive/2026", delimiter "/") that is unique per mailbox; the path
// is rewritten on ancestor rename. UIDValidity is minted once per folder and
// changes only when the folder's message set is destroyed (delete + recreate
// with the same path), telling IMAP clients to resync.
type MailFolder struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	MailboxID   uint   `json:"mailbox_id" gorm:"not null;uniqueIndex:idx_mail_folders_mailbox_path"`
	ParentID    *uint  `json:"parent_id,omitempty" gorm:"index"`
	Name        string `json:"name" gorm:"size:255;not null"`
	IMAPPath    string `json:"imap_path" gorm:"size:1024;not null;uniqueIndex:idx_mail_folders_mailbox_path"`
	UIDValidity uint32 `json:"uid_validity" gorm:"not null"`
	UIDNext     uint32 `json:"uid_next" gorm:"not null;default:1"`
	// ModSeqNext allocates per-message mod-sequences (CONDSTORE-style tokens
	// for the flag CAS); it shares the folder-row atomic claim with UIDNext.
	ModSeqNext uint64 `json:"mod_seq_next" gorm:"not null;default:1"`
	Subscribed bool   `json:"subscribed" gorm:"not null;default:true"`
	// Per-folder retention overrides (ADR-003 decision 7): a positive value
	// overrides the owning mailbox's policy for this folder; 0 inherits.
	RetentionDays     int       `json:"retention_days" gorm:"not null;default:0"`
	RetentionMaxCount int       `json:"retention_max_count" gorm:"not null;default:0"`
	CreatedAt         time.Time `json:"created_at"`
}

func (MailFolder) TableName() string { return "mail_folders" }

// Message flags bitmask (IMAP system flags). Keep the bitwise values stable —
// they are persisted. \Recent is best-effort: set at append, never cleared by
// the store itself (a real session-selection semantic, OB-4).
const (
	FlagSeen     uint32 = 1 << 0
	FlagAnswered uint32 = 1 << 1
	FlagFlagged  uint32 = 1 << 2
	FlagDraft    uint32 = 1 << 3
	FlagDeleted  uint32 = 1 << 4
	FlagRecent   uint32 = 1 << 5
)

// MailAttachmentRef is one entry of MailMessage.AttachmentsJSON: a
// content-addressed blob reference for an extracted attachment (SF-112 —
// webmail and outbound compose reuse the same store).
type MailAttachmentRef struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	BlobHash    string `json:"blob_hash"` // SHA-256 hex of the attachment content
}

// MailMessage is one message in a folder. (FolderID, UID) is unique — UIDs are
// assigned per folder by a transactional claim and never reused; ModSeq is the
// per-message CAS token for flag stores (bumped on every metadata change) so
// OB-4 can implement UNCHANGEDSINCE semantics.
//
// BodyBlobRef points at the raw RFC 5322 message stored as a single
// content-addressed blob — the source of truth, never reassembled from parts,
// so FETCH BODY[] is byte-exact. Attachments are additionally extracted at
// ingest into their own blobs (deduplicated across messages via blob_refs).
// BodyText is the bounded plain-text body kept in Postgres for full-text
// search (ADR-003 decision 3: tsvector over subject/from/to/body-text); it is
// derived data — the blob remains authoritative.
type MailMessage struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	FolderID    uint      `json:"folder_id" gorm:"not null;uniqueIndex:idx_mail_messages_folder_uid"`
	UID         uint32    `json:"uid" gorm:"not null;uniqueIndex:idx_mail_messages_folder_uid"`
	MessageID   string    `json:"message_id" gorm:"size:998;index:idx_mail_messages_message_id"`
	Subject     string    `json:"subject" gorm:"type:text"`
	FromAddr    string    `json:"from_addr" gorm:"size:998;index"`
	ToAddr      string    `json:"to_addr" gorm:"type:text"`
	Cc          string    `json:"cc" gorm:"type:text"`
	Date        time.Time `json:"date" gorm:"index"`
	Size        int64     `json:"size" gorm:"not null;default:0"`
	Flags       uint32    `json:"flags" gorm:"not null;default:0"`
	ModSeq      uint64    `json:"mod_seq" gorm:"not null;default:0;index"`
	Snippet     string    `json:"snippet" gorm:"size:255"`
	BodyBlobRef string    `json:"body_blob_ref" gorm:"size:64;index"`
	BodyText    string    `json:"-" gorm:"type:text"`
	BodySize    int64     `json:"body_size" gorm:"not null;default:0"`
	// AttachmentsJSON holds a JSON array of MailAttachmentRef (the repo-wide
	// JSON-in-text convention, as InboundEmail.AttachmentsJSON); the mailstore
	// service marshals/unmarshals it.
	AttachmentsJSON string `json:"attachments_json,omitempty" gorm:"type:text"`
	// SearchTSV is the tsvector generated column created by raw SQL
	// (internal/storage/migration + the upgrade step); GORM must never
	// reference it in writes. See mailstore.Searcher (OB-3 search wave).
	SearchTSV any       `json:"-" gorm:"-:all"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (MailMessage) TableName() string { return "mail_messages" }

// MailBlobRef ref-counts content-addressed blobs (ADR-003 decision 2, GAP-45
// storage half). One row per unique content hash; the mailstore increments
// RefCount when a message starts referencing the blob and decrements on
// delete. A periodic GC sweep (retention job) reclaims rows at RefCount <= 0
// together with the stored object, so a live blob is never collected.
// BlobHash is the SHA-256 hex of the PLAINTEXT content — the content address —
// so identical content dedups to one stored object.
//
// LastReferenced is the grace-period anchor for the GAP-45 orphan GC (OB-3
// wave W2): the mailstore stamps it on every reference event (reserve,
// increment, release), so "RefCount <= 0 older than the grace period" means
// the blob has been unreferenced AND untouched for the whole window. Nullable:
// rows written before the column existed carry NULL and the GC falls back to
// CreatedAt.
type MailBlobRef struct {
	ID             uint       `json:"id" gorm:"primaryKey"`
	BlobHash       string     `json:"blob_hash" gorm:"size:64;uniqueIndex;not null"`
	Size           int64      `json:"size" gorm:"not null;default:0"`
	RefCount       int64      `json:"ref_count" gorm:"not null;default:0"`
	LastReferenced *time.Time `json:"last_referenced,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

func (MailBlobRef) TableName() string { return "mail_blob_refs" }
