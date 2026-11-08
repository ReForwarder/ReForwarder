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
	"testing"

	"github.com/goposta/posta/internal/models"
)

// TestStatusCounts: messages/unseen/recent track the folder's flag state and
// react to expunge; a foreign folder is refused.
func TestStatusCounts(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "status@post", 0)
	folder := inboxFolder(t, svc, mb)

	// Empty folder: all zeroes.
	st, err := svc.StatusCounts(folder.ID, 1, 0)
	if err != nil {
		t.Fatalf("empty status: %v", err)
	}
	if st.Messages != 0 || st.Unseen != 0 || st.Recent != 0 || st.FolderID != folder.ID {
		t.Fatalf("empty folder status = %+v", st)
	}

	for i := 0; i < 4; i++ {
		if _, err := svc.Append(context.Background(), 1, 0, AppendIn{
			FolderID: folder.ID,
			Raw:      simpleRaw("status-msg", "s@t.test", "t@t.test", "body"),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	st, err = svc.StatusCounts(folder.ID, 1, 0)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	// Appends set \Recent, not \Seen: 4 messages, 4 unseen, 4 recent.
	if st.Messages != 4 || st.Unseen != 4 || st.Recent != 4 {
		t.Fatalf("after appends status = %+v, want 4/4/4", st)
	}

	// Mark one seen, clear one recent: 4 messages, 3 unseen, 3 recent.
	page, err := svc.ListMessages(folder.ID, 1, 0, ListFilter{}, 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	first := page.Messages[0]
	second := page.Messages[1]
	if _, err := svc.SetFlags(1, 0, FlagsUpdate{MessageID: first.ID, Set: models.FlagSeen, Clear: models.FlagRecent}); err != nil {
		t.Fatalf("flags 1: %v", err)
	}
	if _, err := svc.SetFlags(1, 0, FlagsUpdate{MessageID: second.ID, Clear: models.FlagRecent}); err != nil {
		t.Fatalf("flags 2: %v", err)
	}
	st, err = svc.StatusCounts(folder.ID, 1, 0)
	if err != nil {
		t.Fatalf("status after flags: %v", err)
	}
	if st.Messages != 4 || st.Unseen != 3 || st.Recent != 2 {
		t.Fatalf("after flags status = %+v, want 4/3/2", st)
	}

	// Expunge the deleted-flagged third message → 3 messages.
	if err := svc.MarkDeleted(1, 0, page.Messages[2].ID); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	if _, err := svc.Expunge(folder.ID, 1, 0); err != nil {
		t.Fatalf("expunge: %v", err)
	}
	st, err = svc.StatusCounts(folder.ID, 1, 0)
	if err != nil {
		t.Fatalf("status after expunge: %v", err)
	}
	if st.Messages != 3 {
		t.Fatalf("after expunge messages = %d, want 3", st.Messages)
	}

	// Foreign user refused.
	if _, err := svc.StatusCounts(folder.ID, 2, 0); err == nil {
		t.Fatalf("foreign status must be refused")
	}
}
