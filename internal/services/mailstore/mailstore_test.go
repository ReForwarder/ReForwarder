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
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// Fake-driver suite: service logic, control flow and concurrency against the
// in-memory driver. Transaction atomicity and the SQL-level atomic claims are
// additionally verified against real Postgres in mailstore_pg_test.go.

const (
	aliceID = 101
	bobID   = 202
)

// TestAppendFetchRoundTrip — append raw MIME, then read the exact raw bytes
// back through the blob store and codec (fake store + pass-through codec).
func TestAppendFetchRoundTrip(t *testing.T) {
	svc, _, store := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "alice@example.test", 0)
	inbox := inboxFolder(t, svc, mb)

	raw := simpleRaw("roundtrip-1", "sender@example.test", "alice@example.test", "Hello roundtrip")
	res, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if res.UID != 1 {
		t.Fatalf("first UID = %d, want 1", res.UID)
	}

	got, err := svc.GetMessageBody(context.Background(), res.MessageID, aliceID, 0)
	if err != nil {
		t.Fatalf("fetch body: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, raw)
	}

	m, err := svc.GetMessage(res.MessageID, aliceID, 0)
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	if m.Subject != "roundtrip-1" || m.FromAddr != "sender@example.test" {
		t.Fatalf("metadata wrong: subject=%q from=%q", m.Subject, m.FromAddr)
	}
	if m.Snippet == "" || !strings.Contains(m.Snippet, "Hello roundtrip") {
		t.Fatalf("snippet missing body: %q", m.Snippet)
	}
	if m.Size != int64(len(raw)) {
		t.Fatalf("size = %d, want %d", m.Size, len(raw))
	}
	if m.BodyBlobRef == "" {
		t.Fatal("body blob ref empty")
	}
	if !store.has(BlobObjectKey(m.BodyBlobRef)) {
		t.Fatalf("body blob %s not in store", m.BodyBlobRef)
	}
}

// TestAppendParsesAttachments — multipart MIME extracts attachments into
// content-addressed blobs with refs recorded in the row.
func TestAppendParsesAttachments(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "att@example.test", 0)
	inbox := inboxFolder(t, svc, mb)

	raw := []byte("Message-ID: <att-1@test>\r\n" +
		"From: s@example.test\r\nTo: att@example.test\r\nSubject: with-att\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"see attachment\r\n" +
		"--BOUND\r\n" +
		"Content-Type: application/pdf; name=\"doc.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"doc.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"JVBERi0xLjQK\r\n" + // base64("%PDF-1.4\n")
		"--BOUND--\r\n")
	res, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	m, err := svc.GetMessage(res.MessageID, aliceID, 0)
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	var atts []models.MailAttachmentRef
	if err := json.Unmarshal([]byte(m.AttachmentsJSON), &atts); err != nil {
		t.Fatalf("unmarshal attachments: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("attachments = %d, want 1 (%s)", len(atts), m.AttachmentsJSON)
	}
	if atts[0].Name != "doc.pdf" || atts[0].ContentType != "application/pdf" {
		t.Fatalf("attachment metadata wrong: %+v", atts[0])
	}
	if atts[0].BlobHash == "" || atts[0].BlobHash == m.BodyBlobRef {
		t.Fatalf("attachment blob hash wrong: %q", atts[0].BlobHash)
	}
	_, data, err := svc.GetAttachment(context.Background(), res.MessageID, aliceID, 0, atts[0].BlobHash)
	if err != nil {
		t.Fatalf("get attachment: %v", err)
	}
	if string(data) != "%PDF-1.4\n" {
		t.Fatalf("attachment content = %q", data)
	}
}

// TestAppendDedupSkipsSecondBlobWrite — identical content appended twice
// stores one blob and ref-counts it to 2.
func TestAppendDedupSkipsSecondBlobWrite(t *testing.T) {
	svc, state, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "dedup@example.test", 0)
	inbox := inboxFolder(t, svc, mb)

	raw := simpleRaw("dedup-1", "s@example.test", "dedup@example.test", "same body")
	if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); err != nil {
		t.Fatalf("second append: %v", err)
	}
	state.mu.Lock()
	refs := state.tables["mail_blob_refs"].rows
	var refCount int64
	blobRows := 0
	for _, r := range refs {
		refCount += r[state.colIndex("mail_blob_refs", "ref_count")].(int64)
		blobRows++
	}
	state.mu.Unlock()
	if blobRows != 1 {
		t.Fatalf("blob rows = %d, want 1 (no dedup)", blobRows)
	}
	if refCount != 2 {
		t.Fatalf("ref count = %d, want 2", refCount)
	}
}

// TestAppendQuotaRejected — the quota enforcer rejects the append before any
// write; counters and rows stay untouched.
func TestAppendQuotaRejected(t *testing.T) {
	svc, state, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "quota@example.test", 10) // 10 bytes
	inbox := inboxFolder(t, svc, mb)

	raw := simpleRaw("quota-1", "s@example.test", "quota@example.test", "this body is much longer than ten bytes")
	if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("append err = %v, want ErrQuotaExceeded", err)
	}
	state.mu.Lock()
	msgs := len(state.tables["mail_messages"].rows)
	var used, count int64
	for _, r := range state.tables["mailboxes"].rows {
		used = r[state.colIndex("mailboxes", "used_bytes")].(int64)
		count = r[state.colIndex("mailboxes", "message_count")].(int64)
	}
	state.mu.Unlock()
	if msgs != 0 {
		t.Fatalf("messages = %d, want 0 after rejected append", msgs)
	}
	if used != 0 || count != 0 {
		t.Fatalf("counters moved on rejected append: used=%d count=%d", used, count)
	}
}

// TestAppendInactiveMailboxRejected — suspended/closed mailboxes refuse appends.
func TestAppendInactiveMailboxRejected(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "closed@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	if err := svc.SetMailboxStatus(mb.ID, aliceID, 0, models.MailboxStatusSuspended); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	raw := simpleRaw("inactive-1", "s@example.test", "closed@example.test", "x")
	if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); !errors.Is(err, ErrMailboxInactive) {
		t.Fatalf("append err = %v, want ErrMailboxInactive", err)
	}
}

// TestUIDMonotonicUnderConcurrentAppends — N goroutines append concurrently;
// UIDs must be unique and form exactly the set 1..N. The fake serializes
// statements under its mutex; -race additionally checks the service code for
// data races. Real-Postgres verification of the SQL-level claim:
// TestPGUIDMonotonicUnderConcurrentAppends.
func TestUIDMonotonicUnderConcurrentAppends(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "race@example.test", 0)
	inbox := inboxFolder(t, svc, mb)

	const n = 32
	uids := make(chan uint32, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw := simpleRaw("race-"+itoa(i), "s@example.test", "race@example.test", "body")
			res, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
			if err != nil {
				errs <- err
				return
			}
			uids <- res.UID
		}(i)
	}
	wg.Wait()
	close(uids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append failed: %v", err)
	}
	seen := make(map[uint32]bool, n)
	for u := range uids {
		if seen[u] {
			t.Fatalf("duplicate UID %d assigned", u)
		}
		seen[u] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct UIDs, want %d", len(seen), n)
	}
	for i := uint32(1); i <= n; i++ {
		if !seen[i] {
			t.Fatalf("UID %d missing from assignment set (hole in 1..%d)", i, n)
		}
	}
	// uid_next advanced to n+1 and the row count matches.
	f, err := svc.GetFolderByPath(mb.ID, aliceID, 0, "INBOX")
	if err != nil {
		t.Fatalf("get folder: %v", err)
	}
	if f.UIDNext != n+1 {
		t.Fatalf("uid_next = %d, want %d", f.UIDNext, n+1)
	}
}

// TestFlagCASConflict — a store carrying a stale ExpectedModSeq is rejected;
// the winner's read-back shows the new state; a fresh expected value then
// succeeds.
func TestFlagCASConflict(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "cas@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	res, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("cas-1", "s@example.test", "cas@example.test", "x")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	stale := res.ModSeq

	// First store succeeds and moves the mod_seq.
	r1, err := svc.SetFlags(aliceID, 0, FlagsUpdate{MessageID: res.MessageID, ExpectedModSeq: stale, Set: models.FlagSeen})
	if err != nil {
		t.Fatalf("first store: %v", err)
	}
	if r1.ModSeq <= stale {
		t.Fatalf("mod_seq did not advance: %d -> %d", stale, r1.ModSeq)
	}
	// Second store reusing the SAME stale expected value must conflict.
	if _, err := svc.SetFlags(aliceID, 0, FlagsUpdate{MessageID: res.MessageID, ExpectedModSeq: stale, Set: models.FlagFlagged}); !errors.Is(err, ErrFlagConflict) {
		t.Fatalf("stale store err = %v, want ErrFlagConflict", err)
	}
	// Store with the CURRENT mod_seq succeeds.
	r2, err := svc.SetFlags(aliceID, 0, FlagsUpdate{MessageID: res.MessageID, ExpectedModSeq: r1.ModSeq, Set: models.FlagFlagged})
	if err != nil {
		t.Fatalf("current store: %v", err)
	}
	if r2.Flags&(models.FlagSeen|models.FlagFlagged) != models.FlagSeen|models.FlagFlagged {
		t.Fatalf("flags = %d, want seen|flagged", r2.Flags)
	}
}

// TestFlagCASUnconditionalAndClear — ExpectedModSeq == 0 stores
// unconditionally; Clear drops flags.
func TestFlagCASUnconditionalAndClear(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "cas2@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	res, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("cas2-1", "s@example.test", "cas2@example.test", "x")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	r, err := svc.SetFlags(aliceID, 0, FlagsUpdate{MessageID: res.MessageID, Set: models.FlagSeen | models.FlagFlagged})
	if err != nil {
		t.Fatalf("unconditional store: %v", err)
	}
	r, err = svc.SetFlags(aliceID, 0, FlagsUpdate{MessageID: res.MessageID, Clear: models.FlagFlagged})
	if err != nil {
		t.Fatalf("clear store: %v", err)
	}
	if r.Flags&models.FlagFlagged != 0 || r.Flags&models.FlagSeen == 0 {
		t.Fatalf("flags after clear = %d, want seen kept, flagged dropped", r.Flags)
	}
}

// TestMoveCopyBetweenFolders — copy keeps the original and mints a new UID;
// move relocates the row and the UID changes.
func TestMoveCopyBetweenFolders(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "mv@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	archive, err := svc.CreateFolder(mb.ID, aliceID, 0, "", "Archive")
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	res, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("mv-1", "s@example.test", "mv@example.test", "x")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	copied, err := svc.CopyMessage(res.MessageID, archive.ID, aliceID, 0)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.MessageID == res.MessageID {
		t.Fatal("copy reused the message id")
	}
	if copied.FolderID != archive.ID || copied.UID != 1 {
		t.Fatalf("copy landed wrong: folder=%d uid=%d", copied.FolderID, copied.UID)
	}
	if _, err := svc.GetMessage(res.MessageID, aliceID, 0); err != nil {
		t.Fatalf("original vanished after copy: %v", err)
	}

	moved, err := svc.MoveMessage(res.MessageID, archive.ID, aliceID, 0)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if moved.FolderID != archive.ID || moved.UID != 2 {
		t.Fatalf("move landed wrong: folder=%d uid=%d", moved.FolderID, moved.UID)
	}
	page, err := svc.ListMessages(inbox.ID, aliceID, 0, ListFilter{}, 1, 50)
	if err != nil {
		t.Fatalf("list inbox after move: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("inbox total = %d after move, want 0", page.Total)
	}
}

// TestCopyIncrementsBlobRefs — a copied message shares the body blob, so the
// ref count grows; deleting one copy decrements, not deletes.
func TestCopyIncrementsBlobRefs(t *testing.T) {
	svc, state, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "cref@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	archive, err := svc.CreateFolder(mb.ID, aliceID, 0, "", "Archive")
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	res, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("cref-1", "s@example.test", "cref@example.test", "shared body")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := svc.CopyMessage(res.MessageID, archive.ID, aliceID, 0); err != nil {
		t.Fatalf("copy: %v", err)
	}
	state.mu.Lock()
	refCounts := map[string]int64{}
	for _, r := range state.tables["mail_blob_refs"].rows {
		refCounts[r[state.colIndex("mail_blob_refs", "blob_hash")].(string)] = r[state.colIndex("mail_blob_refs", "ref_count")].(int64)
	}
	state.mu.Unlock()
	for h, c := range refCounts {
		if c != 2 {
			t.Fatalf("blob %s ref count = %d, want 2 after copy", h, c)
		}
	}
}

// TestExpungePurgesDeletedOnly — only \Deleted rows vanish; counters and
// uid_validity stay put; uid_next is not rewound.
func TestExpungePurgesDeletedOnly(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "exp@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	raw := simpleRaw("exp-1", "s@example.test", "exp@example.test", "expunge me")
	r1, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	r2, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("exp-2", "s@example.test", "exp@example.test", "keep me")})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if err := svc.MarkDeleted(aliceID, 0, r1.MessageID); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	out, err := svc.Expunge(inbox.ID, aliceID, 0)
	if err != nil {
		t.Fatalf("expunge: %v", err)
	}
	if out.Expunged != 1 {
		t.Fatalf("expunged = %d, want 1", out.Expunged)
	}
	if _, err := svc.GetMessage(r1.MessageID, aliceID, 0); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("deleted message err = %v, want ErrMessageNotFound", err)
	}
	if _, err := svc.GetMessage(r2.MessageID, aliceID, 0); err != nil {
		t.Fatalf("survivor gone: %v", err)
	}
	f, err := svc.GetFolderByPath(mb.ID, aliceID, 0, "INBOX")
	if err != nil {
		t.Fatalf("get folder: %v", err)
	}
	if f.UIDNext != 3 {
		t.Fatalf("uid_next rewound: %d, want 3", f.UIDNext)
	}
	mbNow, err := svc.GetMailbox(mb.ID, aliceID, 0)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	keepRaw := simpleRaw("exp-2", "s@example.test", "exp@example.test", "keep me")
	if mbNow.MessageCount != 1 || mbNow.UsedBytes != int64(len(keepRaw)) {
		t.Fatalf("counters wrong after expunge: count=%d used=%d, want 1/%d", mbNow.MessageCount, mbNow.UsedBytes, len(keepRaw))
	}
	_ = r2
}

// TestListMessagesPaginationSortedByUID — ordered ascending by UID with
// stable pages; total reflects the unpaginated count.
func TestListMessagesPaginationSortedByUID(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "page@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	const total = 7
	for i := 0; i < total; i++ {
		if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("page-"+itoa(i), "s@example.test", "page@example.test", "x")}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	page1, err := svc.ListMessages(inbox.ID, aliceID, 0, ListFilter{}, 1, 3)
	if err != nil {
		t.Fatalf("list page 1: %v", err)
	}
	if page1.Total != total {
		t.Fatalf("total = %d, want %d", page1.Total, total)
	}
	if len(page1.Messages) != 3 {
		t.Fatalf("page 1 len = %d, want 3", len(page1.Messages))
	}
	if page1.Messages[0].UID != 1 || page1.Messages[2].UID != 3 {
		t.Fatalf("page 1 uids = %d,%d,%d", page1.Messages[0].UID, page1.Messages[1].UID, page1.Messages[2].UID)
	}
	page3, err := svc.ListMessages(inbox.ID, aliceID, 0, ListFilter{}, 3, 3)
	if err != nil {
		t.Fatalf("list page 3: %v", err)
	}
	if len(page3.Messages) != 1 || page3.Messages[0].UID != 7 {
		t.Fatalf("page 3 wrong: len=%d uid=%d", len(page3.Messages), page3.Messages[0].UID)
	}
}

// TestListMessagesFilters — unread and flagged filters narrow the page.
func TestListMessagesFilters(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "filter@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	r1, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("f-1", "s@example.test", "filter@example.test", "x")})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	r2, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("f-2", "s@example.test", "filter@example.test", "x")})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if _, err := svc.SetFlags(aliceID, 0, FlagsUpdate{MessageID: r1.MessageID, Set: models.FlagSeen | models.FlagFlagged}); err != nil {
		t.Fatalf("flag r1: %v", err)
	}
	unread, err := svc.ListMessages(inbox.ID, aliceID, 0, ListFilter{Unread: true}, 1, 50)
	if err != nil {
		t.Fatalf("list unread: %v", err)
	}
	if unread.Total != 1 || unread.Messages[0].ID != r2.MessageID {
		t.Fatalf("unread filter wrong: total=%d", unread.Total)
	}
	flagged, err := svc.ListMessages(inbox.ID, aliceID, 0, ListFilter{Flagged: true}, 1, 50)
	if err != nil {
		t.Fatalf("list flagged: %v", err)
	}
	if flagged.Total != 1 || flagged.Messages[0].ID != r1.MessageID {
		t.Fatalf("flagged filter wrong: total=%d", flagged.Total)
	}
}

// TestMailboxIsolation — user B can never read, append to, flag, move, or
// expunge anything of user A; mailboxes never cross-appear in listings.
func TestMailboxIsolation(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mbA := seedUserMailbox(t, svc, aliceID, "alice@iso.test", 0)
	inboxA := inboxFolder(t, svc, mbA)
	res, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inboxA.ID, Raw: simpleRaw("iso-1", "s@example.test", "alice@iso.test", "private")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	mbB := seedUserMailbox(t, svc, bobID, "bob@iso.test", 0)
	inboxB := inboxFolder(t, svc, mbB)

	if _, err := svc.GetMessage(res.MessageID, bobID, 0); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("B read A message: err = %v, want ErrNotMailboxOwner", err)
	}
	if _, err := svc.GetMessageBody(context.Background(), res.MessageID, bobID, 0); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("B read A body: err = %v, want ErrNotMailboxOwner", err)
	}
	if _, err := svc.Append(context.Background(), bobID, 0, AppendIn{FolderID: inboxA.ID, Raw: simpleRaw("intrude", "b@example.test", "x", "y")}); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("B appended to A folder: err = %v, want ErrNotMailboxOwner", err)
	}
	if _, err := svc.SetFlags(bobID, 0, FlagsUpdate{MessageID: res.MessageID, Set: models.FlagSeen}); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("B flagged A message: err = %v, want ErrNotMailboxOwner", err)
	}
	if _, err := svc.CopyMessage(res.MessageID, inboxB.ID, bobID, 0); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("B copied A message: err = %v, want ErrNotMailboxOwner", err)
	}
	if _, err := svc.Expunge(inboxA.ID, bobID, 0); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("B expunged A folder: err = %v, want ErrNotMailboxOwner", err)
	}
	if err := svc.DeleteFolder(inboxA.ID, bobID, 0); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("B deleted A folder: err = %v, want ErrNotMailboxOwner", err)
	}
	// Cross-user append attempt must not have landed anything in A's folder.
	page, err := svc.ListMessages(inboxA.ID, aliceID, 0, ListFilter{}, 1, 50)
	if err != nil {
		t.Fatalf("list A inbox: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("A inbox total = %d after B's attempts, want 1", page.Total)
	}
	// Listings never cross.
	mbsA, err := svc.ListMailboxes(aliceID, 0)
	if err != nil {
		t.Fatalf("list A mailboxes: %v", err)
	}
	for _, m := range mbsA {
		if m.UserID != aliceID {
			t.Fatalf("A's listing leaked mailbox of user %d", m.UserID)
		}
	}
}

// TestFolderCreateRenameDeleteCascade — nested create, rename rewrites the
// subtree paths, delete removes subtree + messages + decrements refs.
func TestFolderCreateRenameDeleteCascade(t *testing.T) {
	svc, state, store := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "folders@example.test", 0)
	inbox := inboxFolder(t, svc, mb)

	// INBOX is protected.
	if err := svc.DeleteFolder(inbox.ID, aliceID, 0); !errors.Is(err, ErrFolderImmutable) {
		t.Fatalf("delete INBOX err = %v, want ErrFolderImmutable", err)
	}

	work, err := svc.CreateFolder(mb.ID, aliceID, 0, "", "Work")
	if err != nil {
		t.Fatalf("create Work: %v", err)
	}
	deep, err := svc.CreateFolder(mb.ID, aliceID, 0, "Work", "2026")
	if err != nil {
		t.Fatalf("create Work/2026: %v", err)
	}
	if deep.IMAPPath != "Work/2026" {
		t.Fatalf("deep path = %q, want Work/2026", deep.IMAPPath)
	}
	if deep.ParentID == nil || *deep.ParentID != work.ID {
		t.Fatalf("deep parent = %v, want %d", deep.ParentID, work.ID)
	}
	// Duplicate path rejected.
	if _, err := svc.CreateFolder(mb.ID, aliceID, 0, "", "Work"); !errors.Is(err, ErrFolderExists) {
		t.Fatalf("duplicate create err = %v, want ErrFolderExists", err)
	}

	// A message lives in the subtree; refs must survive rename and drop on delete.
	res, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: deep.ID, Raw: simpleRaw("fc-1", "s@example.test", "folders@example.test", "in subtree")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	msg, err := svc.GetMessage(res.MessageID, aliceID, 0)
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	bodyKey := BlobObjectKey(msg.BodyBlobRef)

	renamed, err := svc.RenameFolder(work.ID, aliceID, 0, "Business")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := svc.GetFolderByPath(mb.ID, aliceID, 0, "Business/2026")
	if err != nil {
		t.Fatalf("descendant path not rewritten: %v", err)
	}
	if got.ID != deep.ID {
		t.Fatal("descendant row changed id on rename")
	}
	if renamed.IMAPPath != "Business" {
		t.Fatalf("renamed path = %q", renamed.IMAPPath)
	}
	if _, err := svc.GetMessage(res.MessageID, aliceID, 0); err != nil {
		t.Fatalf("message lost across rename: %v", err)
	}

	// Delete the subtree; message + folder rows vanish, ref count hits 0.
	if err := svc.DeleteFolder(work.ID, aliceID, 0); err != nil {
		t.Fatalf("delete subtree: %v", err)
	}
	if _, err := svc.GetMessage(res.MessageID, aliceID, 0); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("message survived folder delete: %v", err)
	}
	state.mu.Lock()
	refs := 0
	for _, r := range state.tables["mail_blob_refs"].rows {
		refs++
		if r[state.colIndex("mail_blob_refs", "ref_count")].(int64) > 0 {
			t.Fatalf("blob ref still held after cascade delete: %s", r[state.colIndex("mail_blob_refs", "blob_hash")])
		}
	}
	folderRows := len(state.tables["mail_folders"].rows)
	state.mu.Unlock()
	if refs == 0 {
		t.Fatal("blob refs vanished instead of being decremented")
	}
	if folderRows != 1 { // INBOX remains
		t.Fatalf("folder rows = %d after subtree delete, want 1", folderRows)
	}
	// The blob object itself remains for the GC sweep (store-side, later wave).
	if !store.has(bodyKey) {
		t.Fatalf("blob object %s missing after cascade (GC may only collect ref_count<=0 rows)", bodyKey)
	}
}

// TestMailboxDeleteCascade — deleting the mailbox drops everything, including
// ref counts, and the mailbox disappears from listings.
func TestMailboxDeleteCascade(t *testing.T) {
	svc, state, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "del@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("del-1", "s@example.test", "del@example.test", "x")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := svc.DeleteMailbox(mb.ID, aliceID, 0); err != nil {
		t.Fatalf("delete mailbox: %v", err)
	}
	if _, err := svc.GetMailbox(mb.ID, aliceID, 0); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatalf("mailbox err = %v, want ErrMailboxNotFound", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if n := len(state.tables["mail_folders"].rows); n != 0 {
		t.Fatalf("folders = %d after mailbox delete, want 0", n)
	}
	if n := len(state.tables["mail_messages"].rows); n != 0 {
		t.Fatalf("messages = %d after mailbox delete, want 0", n)
	}
	for _, r := range state.tables["mail_blob_refs"].rows {
		if c := r[state.colIndex("mail_blob_refs", "ref_count")].(int64); c > 0 {
			t.Fatalf("blob ref %d still held after mailbox delete", c)
		}
	}
}

// TestINBOXCaseCanonicalization — "inbox", "Inbox" resolve to the canonical
// INBOX row.
func TestINBOXCaseCanonicalization(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "inboxcase@example.test", 0)
	for _, variant := range []string{"inbox", "Inbox", "INBOX"} {
		f, err := svc.GetFolderByPath(mb.ID, aliceID, 0, variant)
		if err != nil {
			t.Fatalf("resolve %q: %v", variant, err)
		}
		if f.IMAPPath != "INBOX" {
			t.Fatalf("resolve %q gave %q", variant, f.IMAPPath)
		}
	}
	// Creating "INBOX" again is a duplicate.
	if _, err := svc.CreateFolder(mb.ID, aliceID, 0, "", "INBOX"); !errors.Is(err, ErrFolderExists) {
		t.Fatalf("duplicate INBOX err = %v, want ErrFolderExists", err)
	}
}

// TestRenameFolderRejectsBadNames — validation and INBOX protection.
func TestRenameFolderRejectsBadNames(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "badname@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	if _, err := svc.RenameFolder(inbox.ID, aliceID, 0, "NotInbox"); !errors.Is(err, ErrFolderImmutable) {
		t.Fatalf("rename INBOX err = %v, want ErrFolderImmutable", err)
	}
	f, err := svc.CreateFolder(mb.ID, aliceID, 0, "", "Ok")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.RenameFolder(f.ID, aliceID, 0, "has/slash"); err == nil {
		t.Fatal("rename accepted delimiter")
	}
	if _, err := svc.RenameFolder(f.ID, aliceID, 0, "  "); err == nil {
		t.Fatal("rename accepted empty name")
	}
}

// TestSearcherStubUnavailable — the W1 searcher stub reports
// ErrSearchUnavailable (the search wave replaces it).
func TestSearcherStubUnavailable(t *testing.T) {
	var s Searcher = SearcherStub{}
	if _, _, err := s.Search(context.Background(), SearchScope{}, SearchQuery{Text: "x"}); !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("stub err = %v, want ErrSearchUnavailable", err)
	}
}

// TestQuotaEnforcerByteLimit — the default enforcer's exact boundary behavior.
// (Signature updated by the quota wave: CheckAppend now takes the caller's
// transaction; nil runs the pure per-mailbox checks these assertions cover.)
func TestQuotaEnforcerByteLimit(t *testing.T) {
	q := DefaultQuotaEnforcer{}
	mb := &models.Mailbox{QuotaBytes: 100, UsedBytes: 90}
	if err := q.CheckAppend(nil, mb, 10); err != nil {
		t.Fatalf("boundary append rejected: %v", err)
	}
	if err := q.CheckAppend(nil, mb, 11); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("over-quota err = %v, want ErrQuotaExceeded", err)
	}
	unlimited := &models.Mailbox{QuotaBytes: 0, UsedBytes: 1 << 40}
	if err := q.CheckAppend(nil, unlimited, 1<<40); err != nil {
		t.Fatalf("unlimited rejected: %v", err)
	}
}

// TestBlobObjectKeyShardsStably — the key derivation is pure and sharded.
func TestBlobObjectKeyShardsStably(t *testing.T) {
	h := strings.Repeat("ab", 32)
	if got := BlobObjectKey(h); got != "mail/ab/"+h {
		t.Fatalf("key = %q", got)
	}
	if got := BlobObjectKey("x"); got != "mail/x" {
		t.Fatalf("short hash key = %q", got)
	}
}

// TestIdentityCodecRoundTrip — the stub codec's content-address integrity.
func TestIdentityCodecRoundTrip(t *testing.T) {
	var c BlobCodec = IdentityBlobCodec{}
	plain := []byte("payload")
	stored, ref, err := c.Encode(plain)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if string(stored) != string(plain) {
		t.Fatal("stub codec must pass through bytes")
	}
	got, err := c.Decode(stored, ref)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatal("decode mismatch")
	}
	if _, err := c.Decode([]byte("tampered"), ref); !errors.Is(err, ErrBlobCorrupt) {
		t.Fatalf("tampered decode err = %v, want ErrBlobCorrupt", err)
	}
}

// TestParseMIMETable — parser table test: plain text, RFC 2047 subject,
// quoted-printable and HTML fallback.
func TestParseMIMETable(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		subject string
		text    string
		html    string
		atts    int
	}{
		{
			name:    "plain",
			raw:     "From: a@b.test\r\nSubject: hi\r\n\r\nplain body\r\n",
			subject: "hi", text: "plain body\r\n",
		},
		{
			name:    "rfc2047 subject",
			raw:     "From: a@b.test\r\nSubject: =?utf-8?B?w6k=?=\r\n\r\nbody\r\n",
			subject: "é", text: "body\r\n",
		},
		{
			name:    "quoted-printable",
			raw:     "From: a@b.test\r\nSubject: qp\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=C3=A9\r\n",
			subject: "qp", text: "café\r\n",
		},
		{
			name: "html only",
			raw: "From: a@b.test\r\nSubject: html\r\nContent-Type: text/html\r\n\r\n" +
				"<p>Hello &amp; welcome</p>\r\n",
			subject: "html", html: "<p>Hello &amp; welcome</p>\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseMessage([]byte(tc.raw))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if p.Subject != tc.subject {
				t.Fatalf("subject = %q, want %q", p.Subject, tc.subject)
			}
			if p.TextBody != tc.text {
				t.Fatalf("text = %q, want %q", p.TextBody, tc.text)
			}
			if p.HTMLBody != tc.html {
				t.Fatalf("html = %q, want %q", p.HTMLBody, tc.html)
			}
			if len(p.Attachments) != tc.atts {
				t.Fatalf("attachments = %d, want %d", len(p.Attachments), tc.atts)
			}
		})
	}
}

// TestDeriveSearchTextHTMLFallback — no text/plain part: the search text comes
// from the tag-stripped HTML, entities decoded.
func TestDeriveSearchTextHTMLFallback(t *testing.T) {
	p := &ParsedMessage{HTMLBody: "<p>Invoice &amp; receipt</p><br><b>Total</b>"}
	text, snippet := deriveSearchText(p)
	if !strings.Contains(text, "Invoice & receipt") {
		t.Fatalf("text = %q", text)
	}
	if snippet != "Invoice & receipt Total" {
		t.Fatalf("snippet = %q", snippet)
	}
}

// TestSnippetTruncation — snippets collapse whitespace and cap at 250 runes.
func TestSnippetTruncation(t *testing.T) {
	long := strings.Repeat("word ", 200)
	s := makeSnippet(long)
	if n := len([]rune(s)); n != 250 {
		t.Fatalf("snippet runes = %d, want 250", n)
	}
	ws := makeSnippet("a\n\n  b\t c")
	if ws != "a b c" {
		t.Fatalf("whitespace snippet = %q", ws)
	}
}

// ─── helpers ───

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// refCountOf reads one blob ref's ref_count straight from the fake state.
func refCountOf(t *testing.T, state *msState, hash string) (int64, bool) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	idxHash := state.colIndex("mail_blob_refs", "blob_hash")
	idxRef := state.colIndex("mail_blob_refs", "ref_count")
	for _, row := range state.tables["mail_blob_refs"].rows {
		if row[idxHash] == hash {
			return row[idxRef].(int64), true
		}
	}
	return 0, false
}

// blobRefCount returns the ref count of a message's body blob, failing the
// test when the ref row is missing entirely.
func bodyBlobRefCount(t *testing.T, svc *Service, state *msState, messageID, userID uint) int64 {
	t.Helper()
	m, err := svc.GetMessage(messageID, userID, 0)
	if err != nil {
		t.Fatalf("get message %d: %v", messageID, err)
	}
	rc, ok := refCountOf(t, state, m.BodyBlobRef)
	if !ok {
		t.Fatalf("blob ref %s of message %d missing", m.BodyBlobRef, messageID)
	}
	return rc
}

// TestExpungeDecrementsBlobRefs (OB-3 W2) — releasing a message by expunge
// decrements exactly the refs it held: the purged message's body ref drops to
// 0 (GC-eligible), the survivor's stays at 1.
func TestExpungeDecrementsBlobRefs(t *testing.T) {
	svc, state, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "refs@example.test", 0)
	inbox := inboxFolder(t, svc, mb)

	victimRaw := simpleRaw("refs-1", "s@example.test", "refs@example.test", "purge me")
	keepRaw := simpleRaw("refs-2", "s@example.test", "refs@example.test", "keep me")
	r1, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: victimRaw})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	r2, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: keepRaw})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	victimMsg, err := svc.GetMessage(r1.MessageID, aliceID, 0)
	if err != nil {
		t.Fatalf("get victim: %v", err)
	}
	victimHash := victimMsg.BodyBlobRef
	if rc := bodyBlobRefCount(t, svc, state, r1.MessageID, aliceID); rc != 1 {
		t.Fatalf("victim ref count = %d, want 1", rc)
	}
	if rc := bodyBlobRefCount(t, svc, state, r2.MessageID, aliceID); rc != 1 {
		t.Fatalf("survivor ref count = %d, want 1", rc)
	}
	if err := svc.MarkDeleted(aliceID, 0, r1.MessageID); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	if _, err := svc.Expunge(inbox.ID, aliceID, 0); err != nil {
		t.Fatalf("expunge: %v", err)
	}
	survivor, err := svc.GetMessage(r2.MessageID, aliceID, 0)
	if err != nil {
		t.Fatalf("get survivor: %v", err)
	}
	if rc, ok := refCountOf(t, state, survivor.BodyBlobRef); !ok || rc != 1 {
		t.Fatalf("survivor ref after expunge = %d (row present: %v), want 1", rc, ok)
	}
	if rc, ok := refCountOf(t, state, victimHash); !ok || rc != 0 {
		t.Fatalf("purged message ref = %d (row present: %v), want 0", rc, ok)
	}
}

// TestMailboxUsageBlobBytesFromBlobRefs (OB-3 W2, GAP-45) — the usage report
// is counted from mail_blob_refs and is dedup-aware: two messages sharing one
// content blob report BlobBytes once, while RawBytes (the quota basis) counts
// each appended message size.
func TestMailboxUsageBlobBytesFromBlobRefs(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "usage@example.test", 0)
	inbox := inboxFolder(t, svc, mb)

	shared := strings.Repeat("shared attachment payload ", 40) // 1050 B, one blob
	withAtt := []byte("Message-ID: <usage-1@test>\r\n" +
		"From: s@example.test\r\nTo: usage@example.test\r\nSubject: usage-1\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"BOUND\"\r\n\r\n" +
		"--BOUND\r\nContent-Type: text/plain\r\n\r\nbody one\r\n" +
		"--BOUND\r\nContent-Type: application/octet-stream; name=\"a.bin\"\r\n" +
		"Content-Disposition: attachment; filename=\"a.bin\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + base64Of(shared) + "\r\n" +
		"--BOUND--\r\n")
	sameAtt := []byte("Message-ID: <usage-2@test>\r\n" +
		"From: s@example.test\r\nTo: usage@example.test\r\nSubject: usage-2\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"BOUND\"\r\n\r\n" +
		"--BOUND\r\nContent-Type: text/plain\r\n\r\nbody two\r\n" +
		"--BOUND\r\nContent-Type: application/octet-stream; name=\"a.bin\"\r\n" +
		"Content-Disposition: attachment; filename=\"a.bin\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + base64Of(shared) + "\r\n" +
		"--BOUND--\r\n")
	for _, raw := range [][]byte{withAtt, sameAtt} {
		if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	report, err := svc.MailboxUsage(mb.ID, aliceID, 0)
	if err != nil {
		t.Fatalf("mailbox usage: %v", err)
	}
	// Raw bytes: both messages counted by size (quota basis, dedup-blind).
	if report.RawBytes != int64(len(withAtt)+len(sameAtt)) {
		t.Fatalf("raw bytes = %d, want %d", report.RawBytes, len(withAtt)+len(sameAtt))
	}
	// Blob bytes: the two body blobs are distinct (different subjects) and
	// the shared attachment content is ONE blob counted once. Each body blob
	// holds the full raw message (attachment bytes inline), so the dedup-aware
	// storage total is exactly RawBytes + one attachment size — without dedup
	// the shared attachment would be counted twice (2*attSize more).
	var attSize = int64(len(shared))
	if report.BlobBytes != report.RawBytes+attSize {
		t.Fatalf("blob bytes = %d, want %d (raw %d + shared attachment %d counted once)", report.BlobBytes, report.RawBytes+attSize, report.RawBytes, attSize)
	}
	if report.DistinctBlobs != 3 { // body1, body2, shared attachment
		t.Fatalf("distinct blobs = %d, want 3", report.DistinctBlobs)
	}
	// The dedup itself: the two attachment blobs share one hash.
	msgs, err := svc.ListMessages(inbox.ID, aliceID, 0, ListFilter{}, 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var attHashes []string
	for i := range msgs.Messages {
		var atts []models.MailAttachmentRef
		if err := json.Unmarshal([]byte(msgs.Messages[i].AttachmentsJSON), &atts); err != nil {
			t.Fatalf("unmarshal attachments: %v", err)
		}
		if len(atts) == 1 {
			attHashes = append(attHashes, atts[0].BlobHash)
		}
	}
	if len(attHashes) != 2 || attHashes[0] != attHashes[1] {
		t.Fatalf("shared attachment not deduped: %v", attHashes)
	}
}

// TestMailboxUsageOwnershipScoped — another user's mailbox usage is not
// readable; workspace scoping behaves like every other mailstore call.
func TestMailboxUsageOwnershipScoped(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "scoped@example.test", 0)
	if _, err := svc.MailboxUsage(mb.ID, bobID, 0); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("usage as other user err = %v, want ErrNotMailboxOwner", err)
	}
	if _, err := svc.MailboxUsage(mb.ID, aliceID, 0); err != nil {
		t.Fatalf("usage as owner: %v", err)
	}
}

// base64Of encodes content for the fake MIME fixtures above.
func base64Of(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
