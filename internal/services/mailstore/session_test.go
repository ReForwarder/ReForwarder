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
	"testing"

	"github.com/goposta/posta/internal/models"
)

// TestMailSessionSelectExamine: the session state both servers share —
// authenticated → SELECT (read-write) → EXAMINE (read-only) → Deselect — with
// the folder snapshot (path, UIDVALIDITY, UIDNEXT) captured at selection.
func TestMailSessionSelectExamine(t *testing.T) {
	ms := NewMailSession(1, "user@example.test")
	if ms.Selected() || ms.CanWrite() != nil {
		t.Fatalf("fresh session must be unselected and writable-capable")
	}
	f := &models.MailFolder{ID: 5, IMAPPath: "INBOX", UIDValidity: 1234567890, UIDNext: 42}
	ms.Select(f)
	if !ms.Selected() || ms.FolderID != 5 || ms.FolderPath != "INBOX" ||
		ms.UIDValidity != 1234567890 || ms.UIDNextSnapshot != 42 {
		t.Fatalf("select snapshot wrong: %+v", ms)
	}
	if err := ms.CanWrite(); err != nil {
		t.Fatalf("SELECT session must be writable: %v", err)
	}

	// EXAMINE flips to read-only.
	ms.Examine(f)
	if !ms.ReadOnly {
		t.Fatalf("Examine must mark the session read-only")
	}
	if err := ms.CanWrite(); err == nil {
		t.Fatalf("EXAMINE session must refuse writes")
	}

	// Re-selecting another folder replaces the snapshot and restores
	// read-write.
	other := &models.MailFolder{ID: 6, IMAPPath: "INBOX/Archive", UIDValidity: 987, UIDNext: 3}
	ms.Select(other)
	if ms.FolderID != 6 || ms.ReadOnly {
		t.Fatalf("re-select did not replace the snapshot: %+v", ms)
	}

	ms.Deselect()
	if ms.Selected() || ms.CanWrite() != nil {
		t.Fatalf("deselect must return to the unselected, writable-capable state")
	}
}

// TestMailSessionNilFolder: Select(nil)/Examine(nil) are programming errors;
// the session must not silently enter a half-selected state.
func TestMailSessionNilFolder(t *testing.T) {
	ms := NewMailSession(1, "user@example.test")
	ms.Select(nil)
	if ms.Selected() {
		t.Fatalf("Select(nil) must not mark the session selected")
	}
	if ms.CanWrite() != nil {
		t.Fatalf("Select(nil) must not flip read-only state")
	}
}
