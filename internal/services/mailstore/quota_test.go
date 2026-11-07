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
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// Quota-wave tests (OB-3, ADR-003 decision 5): the per-mailbox byte and
// message-count quotas, the workspace aggregate ceiling, and the counter
// accounting sync across append/expunge/copy/delete. The fake driver runs
// these deterministically (serialized); the real-transactional guarantees
// (no overshoot under concurrency) are proven in the pg suite
// (mailstore_pg_quota_test.go, TestPGConcurrentAppendQuotaNoOvershoot).

const quotaUserID = 303

// requireQuotaError asserts the typed rejection: errors.Is against the
// ErrQuotaExceeded sentinel and the *QuotaError detail reachable via errors.As.
func requireQuotaError(t *testing.T, err error) *QuotaError {
	t.Helper()
	if err == nil {
		t.Fatal("append unexpectedly admitted over quota")
	}
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("err = %v, want errors.Is(err, ErrQuotaExceeded)", err)
	}
	var qe *QuotaError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v (%T), want errors.As to *QuotaError", err, err)
	}
	return qe
}

// TestQuotaEnforcerCountLimit — the pure per-mailbox message-count boundary:
// exactly at the quota rejects the next message, 0 stays unlimited, and the
// rejection is a *QuotaError matching ErrQuotaExceeded.
func TestQuotaEnforcerCountLimit(t *testing.T) {
	q := DefaultQuotaEnforcer{}
	under := &models.Mailbox{ID: 1, QuotaMessages: 3, MessageCount: 2}
	if err := q.CheckAppend(nil, under, 0); err != nil {
		t.Fatalf("boundary append rejected: %v", err)
	}
	at := &models.Mailbox{ID: 1, QuotaMessages: 3, MessageCount: 3}
	qe := requireQuotaError(t, q.CheckAppend(nil, at, 0))
	if qe.Scope != QuotaScopeMailboxMessages || qe.Used != 3 || qe.Limit != 3 || qe.Incoming != 1 {
		t.Fatalf("quota error detail = %+v, want mailbox-messages scope used=3 limit=3 incoming=1", qe)
	}
	unlimited := &models.Mailbox{ID: 1, QuotaMessages: 0, MessageCount: 1 << 20}
	if err := q.CheckAppend(nil, unlimited, 0); err != nil {
		t.Fatalf("unlimited count quota rejected: %v", err)
	}
	if err := q.CheckAppend(nil, nil, 1); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatalf("nil mailbox err = %v, want ErrMailboxNotFound", err)
	}
}

// TestAppendMessageCountQuotaRejected — the third append into a
// quota_messages=2 mailbox is refused with the typed error, and the counters
// stay at 2 (the refused append must not leak count or bytes).
func TestAppendMessageCountQuotaRejected(t *testing.T) {
	svc, state, st := newFakeService(t)
	mb := seedUserMailbox(t, svc, quotaUserID, "count-quota@example.test", 0)
	if err := svc.SetMailboxMessageQuota(mb.ID, quotaUserID, 0, 2); err != nil {
		t.Fatalf("set message quota: %v", err)
	}
	inbox := inboxFolder(t, svc, mb)

	for i := 1; i <= 2; i++ {
		raw := simpleRaw("count-"+string(rune('a'+i)), "s@example.test", "quota@example.test", "body")
		if _, err := svc.Append(context.Background(), quotaUserID, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	third := simpleRaw("count-over", "s@example.test", "quota@example.test", "one message too many")
	_, appendErr := svc.Append(context.Background(), quotaUserID, 0, AppendIn{FolderID: inbox.ID, Raw: third})
	qe := requireQuotaError(t, appendErr)
	if qe.Scope != QuotaScopeMailboxMessages || qe.MailboxID != mb.ID {
		t.Fatalf("quota error = %+v, want mailbox-messages scope on mailbox %d", qe, mb.ID)
	}

	mbNow, err := svc.GetMailbox(mb.ID, quotaUserID, 0)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if mbNow.MessageCount != 2 {
		t.Fatalf("message_count = %d, want 2 (rejection must not leak count)", mbNow.MessageCount)
	}
	if got := len(state.tables["mail_messages"].rows); got != 2 {
		t.Fatalf("mail_messages rows = %d, want 2", got)
	}
	// OB-3 W2 reserve protocol: the quota pre-check runs BEFORE the blob
	// reserve, so a refused append writes nothing at all — no orphan blob,
	// no ref row (a pre-check failure can no longer leak storage; the
	// never-lose-a-message invariant is unchanged: message rows only ever
	// follow their blobs).
	_, ref, err := IdentityBlobCodec{}.Encode(third)
	if err != nil {
		t.Fatalf("encode refused message: %v", err)
	}
	if st.has(BlobObjectKey(ref.Hash)) {
		t.Fatalf("orphan blob written for refused message (W2 reserve protocol: refused appends write nothing)")
	}
	if _, ok := blobRefCount(t, state, ref.Hash); ok {
		t.Fatalf("ref row written for refused message (W2 reserve protocol: refused appends write nothing)")
	}
}

// TestQuotaBoundaryExactFitAdmitted — the byte-quota boundary: a message
// exactly reaching the quota is admitted (used == quota), the next one is
// refused. This is the off-by-one edge of used+incoming > quota.
func TestQuotaBoundaryExactFitAdmitted(t *testing.T) {
	svc, _, _ := newFakeService(t)
	inboxSeed := seedUserMailbox(t, svc, quotaUserID, "exact-fit@example.test", 0)
	inbox := inboxFolder(t, svc, inboxSeed)

	raw := simpleRaw("exact-fit", "s@example.test", "fit@example.test", "payload")
	if err := svc.SetMailboxQuota(inboxSeed.ID, quotaUserID, 0, int64(len(raw))); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if _, err := svc.Append(context.Background(), quotaUserID, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); err != nil {
		t.Fatalf("exact-fit append rejected: %v", err)
	}
	mb, err := svc.GetMailbox(inboxSeed.ID, quotaUserID, 0)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if mb.UsedBytes != int64(len(raw)) || mb.MessageCount != 1 {
		t.Fatalf("counters = %d/%d, want %d/1", mb.UsedBytes, mb.MessageCount, len(raw))
	}
	_, appendErr := svc.Append(context.Background(), quotaUserID, 0,
		AppendIn{FolderID: inbox.ID, Raw: simpleRaw("exact-fit-2", "s@example.test", "fit@example.test", "over")})
	qe := requireQuotaError(t, appendErr)
	if qe.Scope != QuotaScopeMailboxBytes || qe.Used != int64(len(raw)) || qe.Limit != int64(len(raw)) {
		t.Fatalf("quota error = %+v, want mailbox-bytes scope at the boundary", qe)
	}
}

// TestWorkspaceCeilingRejected — the workspace aggregate ceiling: two
// mailboxes of one workspace, neither byte-limited itself, together exceed
// Workspace.MailQuotaBytes → the second append is refused with the
// workspace-bytes scope and leaves the second mailbox untouched. A zero
// ceiling stays unlimited, and mailboxes without a workspace are never
// covered by one.
func TestWorkspaceCeilingRejected(t *testing.T) {
	svc, state, _ := newFakeService(t)
	const wsID = uint(7)
	seedWorkspaceRow(t, state, wsID, 600)

	ws := wsID
	mbA, err := svc.CreateMailbox(quotaUserID, &ws, "ws-a@example.test", 0)
	if err != nil {
		t.Fatalf("create workspace mailbox A: %v", err)
	}
	mbB, err := svc.CreateMailbox(quotaUserID+1, &ws, "ws-b@example.test", 0)
	if err != nil {
		t.Fatalf("create workspace mailbox B: %v", err)
	}
	inboxA := inboxFolder(t, svc, mbA)
	inboxB := inboxFolder(t, svc, mbB)

	big := simpleRaw("ws-big", "s@example.test", "ws@example.test", strings.Repeat("x", 400))
	if _, err := svc.Append(context.Background(), quotaUserID, 0, AppendIn{FolderID: inboxA.ID, Raw: big}); err != nil {
		t.Fatalf("append into A: %v", err)
	}
	_, appendErr := svc.Append(context.Background(), quotaUserID+1, 0,
		AppendIn{FolderID: inboxB.ID, Raw: simpleRaw("ws-b", "s@example.test", "ws@example.test", "enough to cross")})
	qe := requireQuotaError(t, appendErr)
	if qe.Scope != QuotaScopeWorkspaceBytes || qe.WorkspaceID != wsID {
		t.Fatalf("quota error = %+v, want workspace-bytes scope on workspace %d", qe, wsID)
	}
	if qe.Used != int64(len(big)) || qe.Limit != 600 {
		t.Fatalf("ceiling detail = %+v, want used=%d limit=600", qe, len(big))
	}
	mbBNow, err := svc.GetMailbox(mbB.ID, quotaUserID+1, 0)
	if err != nil {
		t.Fatalf("get mailbox B: %v", err)
	}
	if mbBNow.MessageCount != 0 || mbBNow.UsedBytes != 0 {
		t.Fatalf("mailbox B counters = %d/%d, want 0/0 after ceiling rejection", mbBNow.MessageCount, mbBNow.UsedBytes)
	}

	// Zero ceiling = unlimited.
	seedWorkspaceRow(t, state, 8, 0)
	ws8 := uint(8)
	mbC, err := svc.CreateMailbox(quotaUserID+2, &ws8, "ws-c@example.test", 0)
	if err != nil {
		t.Fatalf("create mailbox C: %v", err)
	}
	inboxC := inboxFolder(t, svc, mbC)
	if _, err := svc.Append(context.Background(), quotaUserID+2, 0,
		AppendIn{FolderID: inboxC.ID, Raw: simpleRaw("ws-c", "s@example.test", "ws@example.test", strings.Repeat("y", 700))}); err != nil {
		t.Fatalf("zero-ceiling workspace rejected an append: %v", err)
	}

	// No workspace at all → no ceiling applies.
	mbSolo := seedUserMailbox(t, svc, quotaUserID+3, "solo@example.test", 0)
	inboxSolo := inboxFolder(t, svc, mbSolo)
	if _, err := svc.Append(context.Background(), quotaUserID+3, 0,
		AppendIn{FolderID: inboxSolo.ID, Raw: simpleRaw("solo", "s@example.test", "ws@example.test", strings.Repeat("z", 900))}); err != nil {
		t.Fatalf("workspace-less mailbox rejected: %v", err)
	}
}

// TestCopyMessageCountQuotaRejected — a copy adds a message, so it consumes
// message-count quota (but no new bytes, dedup shares the blobs); a copy into
// a full mailbox is refused and counters stay put.
func TestCopyMessageCountQuotaRejected(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, quotaUserID, "copy-quota@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	raw := simpleRaw("copy-quota", "s@example.test", "copy@example.test", "original")
	res, err := svc.Append(context.Background(), quotaUserID, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	archive, err := svc.CreateFolder(mb.ID, quotaUserID, 0, "", "Archive")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if err := svc.SetMailboxMessageQuota(mb.ID, quotaUserID, 0, 1); err != nil {
		t.Fatalf("set message quota: %v", err)
	}
	_, copyErr := svc.CopyMessage(res.MessageID, archive.ID, quotaUserID, 0)
	qe := requireQuotaError(t, copyErr)
	if qe.Scope != QuotaScopeMailboxMessages {
		t.Fatalf("copy quota error = %+v, want mailbox-messages scope", qe)
	}
	mbNow, err := svc.GetMailbox(mb.ID, quotaUserID, 0)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if mbNow.MessageCount != 1 || mbNow.UsedBytes != int64(len(raw)) {
		t.Fatalf("counters after refused copy = %d/%d, want 1/%d (copy must not consume bytes)",
			mbNow.MessageCount, mbNow.UsedBytes, len(raw))
	}
}

// TestQuotaAccountingSyncAppendExpungeCopyDelete — the mailbox-model usage
// counters stay in sync with every mutation, each within its own transaction:
// append (+bytes,+count), copy (+0,+1 — dedup shares bytes), expunge
// (−bytes,−count) and mailbox delete (row gone).
func TestQuotaAccountingSyncAppendExpungeCopyDelete(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, quotaUserID, "sync@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	archive, err := svc.CreateFolder(mb.ID, quotaUserID, 0, "", "Archive")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}

	raw1 := simpleRaw("sync-1", "s@example.test", "sync@example.test", "first message")
	raw2 := simpleRaw("sync-2", "s@example.test", "sync@example.test", "second message body")
	r1, err := svc.Append(context.Background(), quotaUserID, 0, AppendIn{FolderID: inbox.ID, Raw: raw1})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if _, err := svc.Append(context.Background(), quotaUserID, 0, AppendIn{FolderID: inbox.ID, Raw: raw2}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	assertCounters := func(wantCount int64, wantBytes int64, when string) {
		t.Helper()
		got, err := svc.GetMailbox(mb.ID, quotaUserID, 0)
		if err != nil {
			t.Fatalf("get mailbox (%s): %v", when, err)
		}
		if got.MessageCount != wantCount || got.UsedBytes != wantBytes {
			t.Fatalf("counters after %s = %d/%d, want %d/%d", when, got.MessageCount, got.UsedBytes, wantCount, wantBytes)
		}
	}
	assertCounters(2, int64(len(raw1)+len(raw2)), "appends")

	// Copy adds a message without adding bytes.
	if _, err := svc.CopyMessage(r1.MessageID, archive.ID, quotaUserID, 0); err != nil {
		t.Fatalf("copy: %v", err)
	}
	assertCounters(3, int64(len(raw1)+len(raw2)), "copy")

	// Expunge removes the \Deleted original: bytes of exactly that message.
	if err := svc.MarkDeleted(quotaUserID, 0, r1.MessageID); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	if _, err := svc.Expunge(inbox.ID, quotaUserID, 0); err != nil {
		t.Fatalf("expunge: %v", err)
	}
	assertCounters(2, int64(len(raw2)), "expunge")

	// Mailbox delete removes the counters with the row.
	if err := svc.DeleteMailbox(mb.ID, quotaUserID, 0); err != nil {
		t.Fatalf("delete mailbox: %v", err)
	}
	if _, err := svc.GetMailbox(mb.ID, quotaUserID, 0); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatalf("get deleted mailbox err = %v, want ErrMailboxNotFound", err)
	}
}

// TestRetentionSettersScoping — the retention/quota setters are
// ownership-scoped: another user's call is ErrNotMailboxOwner, negative
// values are refused, and a legitimate update persists.
func TestRetentionSettersScoping(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, quotaUserID, "setter@example.test", 0)
	inbox := inboxFolder(t, svc, mb)

	if err := svc.SetMailboxRetention(mb.ID, quotaUserID+1, 0, 7, 10); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("foreign SetMailboxRetention err = %v, want ErrNotMailboxOwner", err)
	}
	if err := svc.SetFolderRetention(inbox.ID, quotaUserID+1, 0, 7, 0); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("foreign SetFolderRetention err = %v, want ErrNotMailboxOwner", err)
	}
	if err := svc.SetMailboxRetention(mb.ID, quotaUserID, 0, -1, 0); err == nil {
		t.Fatal("negative retention days accepted")
	}
	if err := svc.SetMailboxRetention(mb.ID, quotaUserID, 0, 30, 500); err != nil {
		t.Fatalf("set mailbox retention: %v", err)
	}
	got, err := svc.GetMailbox(mb.ID, quotaUserID, 0)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if got.RetentionDays != 30 || got.RetentionMaxCount != 500 {
		t.Fatalf("mailbox retention = %d/%d, want 30/500", got.RetentionDays, got.RetentionMaxCount)
	}
	if err := svc.SetFolderRetention(inbox.ID, quotaUserID, 0, 7, 0); err != nil {
		t.Fatalf("set folder retention: %v", err)
	}
	f, err := svc.GetFolderByPath(mb.ID, quotaUserID, 0, "INBOX")
	if err != nil {
		t.Fatalf("get folder: %v", err)
	}
	if f.RetentionDays != 7 {
		t.Fatalf("folder retention days = %d, want 7", f.RetentionDays)
	}
}
