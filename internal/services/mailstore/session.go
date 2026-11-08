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

	"github.com/goposta/posta/internal/models"
)

// MailSession (OB-4 wave W1, ADR-004 decisions 1/3): the per-connection state
// both mail-protocol servers share — the authenticated identity, the mailbox
// it operates on, and the currently selected folder with its read-write vs
// read-only mode (SELECT vs EXAMINE). The type is a plain value holder: one
// connection owns one session and drives it from its own goroutine, so there
// is no internal locking; a server that mutates a session from several
// goroutines adds its own synchronization.
//
// Mutating mailstore calls are refused for read-only sessions at the session
// boundary via CanWrite — the store itself stays mode-agnostic.
type MailSession struct {
	// UserID is the authenticated ReForwarder user; UserEmail the identity the
	// client presented (the app-password username). App-password-only auth is
	// enforced by mailauth.Authenticator — the session records the outcome.
	UserID    uint
	UserEmail string

	// Mailbox identifies the hosted mailbox the session opened. Zero until
	// the server resolves the user's mailbox.
	MailboxID   uint
	MailboxName string

	// Folder carries the SELECTed folder snapshot. FolderID == 0 means no
	// folder selected (authenticated-but-unselected state).
	FolderID    uint
	FolderPath  string
	UIDValidity uint32
	// UIDNextSnapshot is the folder's UIDNEXT at selection time (clients read
	// it from the SELECT/EXAMINE banner).
	UIDNextSnapshot uint32

	// ReadOnly marks an EXAMINE session: no flag stores, no expunge, no
	// appends through this session.
	ReadOnly bool
}

// ErrSessionReadOnly is returned by CanWrite on EXAMINE-selected sessions.
var ErrSessionReadOnly = errors.New("mailstore: session is selected read-only (EXAMINE)")

// NewMailSession starts a session in the authenticated, unselected state.
func NewMailSession(userID uint, userEmail string) *MailSession {
	return &MailSession{UserID: userID, UserEmail: userEmail}
}

// Select attaches a folder in read-write mode (IMAP SELECT). It replaces any
// previous selection.
func (ms *MailSession) Select(f *models.MailFolder) {
	ms.selectFolder(f, false)
}

// Examine attaches a folder in read-only mode (IMAP EXAMINE).
func (ms *MailSession) Examine(f *models.MailFolder) {
	ms.selectFolder(f, true)
}

func (ms *MailSession) selectFolder(f *models.MailFolder, readOnly bool) {
	if f == nil {
		// Programming error at the call site: leave the previous state alone
		// rather than half-selecting (TestMailSessionNilFolder pins this).
		return
	}
	ms.FolderID = f.ID
	ms.FolderPath = f.IMAPPath
	ms.UIDValidity = f.UIDValidity
	ms.UIDNextSnapshot = f.UIDNext
	ms.ReadOnly = readOnly
}

// Deselect drops the folder selection (IMAP CLOSE semantics are the
// servers': whether CLOSE expunges is decided there; this only clears state).
func (ms *MailSession) Deselect() {
	ms.FolderID = 0
	ms.FolderPath = ""
	ms.UIDValidity = 0
	ms.UIDNextSnapshot = 0
	ms.ReadOnly = false
}

// Selected reports whether a folder is attached.
func (ms *MailSession) Selected() bool { return ms.FolderID != 0 }

// CanWrite returns ErrSessionReadOnly when the session is EXAMINE-selected
// (or when no folder is selected, which servers also treat as non-writable —
// call Selected separately for that distinction).
func (ms *MailSession) CanWrite() error {
	if ms.ReadOnly {
		return ErrSessionReadOnly
	}
	return nil
}
