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

package models

import "time"

// Compose-attachment staging (OB-5 wave W3, ADR-005 + ADR-006). One row per
// webmail attachment upload: the bytes live in the mailstore blob layer
// (content-addressed, reserved in mail_blob_refs by services/mailstore), the
// row is the ownership + malware-verdict record that send acceptance is gated
// on. A positively-scanned upload is refused outright and never reaches this
// table — only clean (or fail-open unscanned) content is staged, so a send can
// never attach content that was never scanned.

// MailAttachmentScanState is the ADR-006 verdict recorded at upload time.
type MailAttachmentScanState string

const (
	// MailAttachmentScanClean: the scanner returned a clean verdict.
	MailAttachmentScanClean MailAttachmentScanState = "clean"
	// MailAttachmentScanUnavailable: the scanner could not be reached and the
	// deployment's fail mode is open (ADR-006 decision 2), so the upload was
	// accepted unscanned. Under a closed fail mode the upload is refused
	// instead and no row exists.
	MailAttachmentScanUnavailable MailAttachmentScanState = "unavailable"
)

// MailAttachmentUpload stages one compose attachment. (user_id, blob_hash) is
// the identity: content is content-addressed, so identical content from one
// user stages once, and the row proves the OWNER scanned exactly these bytes.
// The blob hash alone never authorizes anything — a foreign hash (or a hash
// staged by another user) is refused at send acceptance.
type MailAttachmentUpload struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// UserID is the staging owner: only this user's sends and drafts may
	// reference the ref.
	UserID      uint      `json:"user_id" gorm:"index;not null;uniqueIndex:idx_mail_attachment_uploads_user_hash"`
	WorkspaceID *uint     `json:"workspace_id,omitempty" gorm:"index"`
	BlobHash    string    `json:"blob_hash" gorm:"size:64;not null;uniqueIndex:idx_mail_attachment_uploads_user_hash"`
	Name        string    `json:"name" gorm:"size:255;not null"`
	ContentType string    `json:"content_type" gorm:"size:255;not null;default:'application/octet-stream'"`
	Size        int64     `json:"size" gorm:"not null;default:0"`
	ScanState   string    `json:"scan_state" gorm:"size:32;not null"`
	CreatedAt   time.Time `json:"created_at"`
}

// TableName pins the plural table name, matching the repo convention.
func (MailAttachmentUpload) TableName() string { return "mail_attachment_uploads" }
