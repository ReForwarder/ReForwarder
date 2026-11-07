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
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/blob"
)

// Retention-wave tests (OB-3, ADR-003 decision 7, SF-81): the retention
// sweep purges only in-policy messages (age from the server-side ingest
// timestamp, or oldest-beyond-count), folder policy overrides mailbox policy,
// policy-less folders/mailboxes and other users' mailboxes are never touched,
// counters stay in sync, and the blob-ref reclamation pass deletes rows and
// objects only once nothing references them.

const retentionUserID = 404

// attachRaw builds a multipart message carrying one attachment with the given
// content (the dedup identity for the blob-GC test).
func attachRaw(subject, attachmentContent string) []byte {
	return []byte("Message-ID: <" + subject + "@test>\r\n" +
		"From: s@example.test\r\nTo: ret@example.test\r\nSubject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n" +
		"\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"see attachment\r\n" +
		"--BOUND\r\n" +
		"Content-Type: application/octet-stream; name=\"blob.bin\"\r\n" +
		"Content-Disposition: attachment; filename=\"blob.bin\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte(attachmentContent)) + "\r\n" +
		"--BOUND--\r\n")
}

// backdateMessage rewrites a message's created_at (the ingest timestamp the
// retention age window reads) directly in the fake state.
func backdateMessage(t *testing.T, state *msState, messageID uint, at time.Time) {
	t.Helper()
	idxID := state.colIndex("mail_messages", "id")
	idxCreated := state.colIndex("mail_messages", "created_at")
	for _, row := range state.tables["mail_messages"].rows {
		if row[idxID] == int64(messageID) {
			row[idxCreated] = at
			return
		}
	}
	t.Fatalf("backdate: message %d not found", messageID)
}

// blobRefCount returns the mail_blob_refs ref_count for a hash (present=false
// when the row was reclaimed).
func blobRefCount(t *testing.T, state *msState, hash string) (int64, bool) {
	t.Helper()
	idxHash := state.colIndex("mail_blob_refs", "blob_hash")
	idxRef := state.colIndex("mail_blob_refs", "ref_count")
	for _, row := range state.tables["mail_blob_refs"].rows {
		if row[idxHash] == hash {
			return row[idxRef].(int64), true
		}
	}
	return 0, false
}

// bodyHash derives the content address of a raw message body (identity codec).
func bodyHash(t *testing.T, raw []byte) string {
	t.Helper()
	_, ref, err := IdentityBlobCodec{}.Encode(raw)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return ref.Hash
}

// mustSweeper builds a RetentionSweeper over the service's db and blob store.
func mustSweeper(t *testing.T, svc *Service, blobs blob.Store) *RetentionSweeper {
	t.Helper()
	sweeper, err := NewRetentionSweeper(svc.db, blobs)
	if err != nil {
		t.Fatalf("new retention sweeper: %v", err)
	}
	// OB-3 W2 added the reclaim grace period (GAP-45): these tests target the
	// reclamation logic itself, so they collapse the grace to ~zero; the grace
	// semantics are covered by TestPGMailBlobGCDropsOnlyAgedOrphans.
	sweeper.ReclaimGrace = time.Nanosecond
	return sweeper
}

// sweepStats runs a sweep and fails the test on error.
func sweepStats(t *testing.T, sweeper *RetentionSweeper) RetentionStats {
	t.Helper()
	stats, err := sweeper.SweepRetention(context.Background())
	if err != nil {
		t.Fatalf("SweepRetention: %v", err)
	}
	return stats
}

// TestRetentionAgePurge — mailbox retention_days purges exactly the messages
// past the window (measured from the server-controlled ingest time, never the
// spoofable Date header), the rest survive, counters stay synced, and the
// purged message's blob ref drops to zero.
func TestRetentionAgePurge(t *testing.T) {
	svc, state, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, retentionUserID, "age@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	if err := svc.SetMailboxRetention(mb.ID, retentionUserID, 0, 7, 0); err != nil {
		t.Fatalf("set retention: %v", err)
	}

	old := simpleRaw("age-old", "s@example.test", "ret@example.test", "old body")
	fresh := simpleRaw("age-fresh", "s@example.test", "ret@example.test", "fresh body")
	rOld, err := svc.Append(context.Background(), retentionUserID, 0, AppendIn{FolderID: inbox.ID, Raw: old})
	if err != nil {
		t.Fatalf("append old: %v", err)
	}
	rFresh, err := svc.Append(context.Background(), retentionUserID, 0, AppendIn{FolderID: inbox.ID, Raw: fresh})
	if err != nil {
		t.Fatalf("append fresh: %v", err)
	}
	backdateMessage(t, state, rOld.MessageID, time.Now().UTC().Add(-30*24*time.Hour))
	backdateMessage(t, state, rFresh.MessageID, time.Now().UTC().Add(-1*time.Hour))

	stats := sweepStats(t, mustSweeper(t, svc, nil)) // nil store: object reclamation is TestRetentionBlobGCReclaimsUnreferenced
	if stats.PurgedMessages != 1 || stats.PurgedBytes != int64(len(old)) {
		t.Fatalf("stats = %+v, want 1 purged / %d bytes", stats, len(old))
	}
	if stats.MailboxesScanned != 1 || stats.FoldersScanned != 1 {
		t.Fatalf("scan stats = %+v, want 1 mailbox / 1 folder", stats)
	}
	if _, err := svc.GetMessage(rOld.MessageID, retentionUserID, 0); err == nil {
		t.Fatal("aged message survived the sweep")
	}
	if _, err := svc.GetMessage(rFresh.MessageID, retentionUserID, 0); err != nil {
		t.Fatalf("in-policy message was deleted: %v", err)
	}
	mbNow, err := svc.GetMailbox(mb.ID, retentionUserID, 0)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if mbNow.MessageCount != 1 || mbNow.UsedBytes != int64(len(fresh)) {
		t.Fatalf("counters after sweep = %d/%d, want 1/%d", mbNow.MessageCount, mbNow.UsedBytes, len(fresh))
	}
	if rc, ok := blobRefCount(t, state, bodyHash(t, old)); !ok || rc != 0 {
		t.Fatalf("purged message body ref count = %d (row present %v), want 0", rc, ok)
	}
}

// TestRetentionCountPurgeOldest — retention_max_count purges exactly the
// oldest messages beyond the cap, ordered by ingest time then UID; newer
// messages survive and the counters stay consistent.
func TestRetentionCountPurgeOldest(t *testing.T) {
	svc, state, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, retentionUserID, "count@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	if err := svc.SetMailboxRetention(mb.ID, retentionUserID, 0, 0, 2); err != nil {
		t.Fatalf("set retention: %v", err)
	}

	raws := [][]byte{
		simpleRaw("cnt-1", "s@example.test", "ret@example.test", "one"),
		simpleRaw("cnt-2", "s@example.test", "ret@example.test", "two"),
		simpleRaw("cnt-3", "s@example.test", "ret@example.test", "three"),
		simpleRaw("cnt-4", "s@example.test", "ret@example.test", "four"),
	}
	var ids []uint
	base := time.Now().UTC().Add(-48 * time.Hour)
	for i, raw := range raws {
		res, err := svc.Append(context.Background(), retentionUserID, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
		if err != nil {
			t.Fatalf("append %d: %v", i+1, err)
		}
		ids = append(ids, res.MessageID)
		backdateMessage(t, state, res.MessageID, base.Add(time.Duration(i)*time.Hour))
	}

	stats := sweepStats(t, mustSweeper(t, svc, nil))
	if stats.PurgedMessages != 2 || stats.PurgedBytes != int64(len(raws[0])+len(raws[1])) {
		t.Fatalf("stats = %+v, want the two oldest purged (%d bytes)", stats, len(raws[0])+len(raws[1]))
	}
	for _, id := range ids[:2] {
		if _, err := svc.GetMessage(id, retentionUserID, 0); err == nil {
			t.Fatalf("oldest message %d survived the count purge", id)
		}
	}
	for _, id := range ids[2:] {
		if _, err := svc.GetMessage(id, retentionUserID, 0); err != nil {
			t.Fatalf("recent message %d was deleted: %v", id, err)
		}
	}
	mbNow, err := svc.GetMailbox(mb.ID, retentionUserID, 0)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if mbNow.MessageCount != 2 || mbNow.UsedBytes != int64(len(raws[2])+len(raws[3])) {
		t.Fatalf("counters = %d/%d, want 2/%d", mbNow.MessageCount, mbNow.UsedBytes, len(raws[2])+len(raws[3]))
	}
}

// TestRetentionInheritAndOverrideMatrix — the folder-over-mailbox precedence
// in one sweep: a folder without its own policy inherits the mailbox policy;
// a positive folder value overrides it; a mailbox without any policy keeps
// everything; and the age window reads the ingest timestamp.
func TestRetentionInheritAndOverrideMatrix(t *testing.T) {
	svc, state, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, retentionUserID, "matrix@example.test", 0)
	inbox := inboxFolder(t, svc, mb) // no folder policy → inherits mailbox 7d
	long, err := svc.CreateFolder(mb.ID, retentionUserID, 0, "", "Long")
	if err != nil {
		t.Fatalf("create Long: %v", err)
	}
	if err := svc.SetFolderRetention(long.ID, retentionUserID, 0, 365, 0); err != nil {
		t.Fatalf("set Long retention: %v", err)
	}
	if err := svc.SetMailboxRetention(mb.ID, retentionUserID, 0, 7, 0); err != nil {
		t.Fatalf("set mailbox retention: %v", err)
	}
	other := seedUserMailbox(t, svc, retentionUserID+1, "matrix-b@example.test", 0) // no policy anywhere
	otherInbox := inboxFolder(t, svc, other)

	old10 := time.Now().UTC().Add(-10 * 24 * time.Hour)

	rawInbox := simpleRaw("mx-inbox", "s@example.test", "ret@example.test", "inbox")
	rInbox, err := svc.Append(context.Background(), retentionUserID, 0, AppendIn{FolderID: inbox.ID, Raw: rawInbox})
	if err != nil {
		t.Fatalf("append inbox: %v", err)
	}
	backdateMessage(t, state, rInbox.MessageID, old10) // inherited 7d → purged

	rawLong := simpleRaw("mx-long", "s@example.test", "ret@example.test", "long")
	rLong, err := svc.Append(context.Background(), retentionUserID, 0, AppendIn{FolderID: long.ID, Raw: rawLong})
	if err != nil {
		t.Fatalf("append long: %v", err)
	}
	backdateMessage(t, state, rLong.MessageID, old10) // 365d override → survives

	rawOther := simpleRaw("mx-other", "s@example.test", "ret@example.test", "other")
	rOther, err := svc.Append(context.Background(), retentionUserID+1, 0, AppendIn{FolderID: otherInbox.ID, Raw: rawOther})
	if err != nil {
		t.Fatalf("append other: %v", err)
	}
	backdateMessage(t, state, rOther.MessageID, old10) // no policy → survives

	stats := sweepStats(t, mustSweeper(t, svc, nil))
	if stats.PurgedMessages != 1 || stats.PurgedBytes != int64(len(rawInbox)) {
		t.Fatalf("stats = %+v, want exactly the inherited-policy message (%d bytes) purged", stats, len(rawInbox))
	}
	if _, err := svc.GetMessage(rLong.MessageID, retentionUserID, 0); err != nil {
		t.Fatalf("override-protected message deleted: %v", err)
	}
	if _, err := svc.GetMessage(rOther.MessageID, retentionUserID+1, 0); err != nil {
		t.Fatalf("policy-less mailbox message deleted: %v", err)
	}
}

// TestRetentionBlobGCReclaimsUnreferenced — the reclamation pass deletes a
// blob's row and stored object only when its ref count reaches zero; a blob
// still referenced by a surviving message is never collected (dedup: two
// messages share one attachment blob).
func TestRetentionBlobGCReclaimsUnreferenced(t *testing.T) {
	svc, state, st := newFakeService(t)
	mb := seedUserMailbox(t, svc, retentionUserID, "gc@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	if err := svc.SetMailboxRetention(mb.ID, retentionUserID, 0, 7, 0); err != nil {
		t.Fatalf("set retention: %v", err)
	}

	// Two messages share the identical attachment content → one blob, refs=2.
	raw1 := attachRaw("gc-1", "shared-attachment-bytes")
	raw2 := attachRaw("gc-2", "shared-attachment-bytes")
	r1, err := svc.Append(context.Background(), retentionUserID, 0, AppendIn{FolderID: inbox.ID, Raw: raw1})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	r2, err := svc.Append(context.Background(), retentionUserID, 0, AppendIn{FolderID: inbox.ID, Raw: raw2})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	m1, err := svc.GetMessage(r1.MessageID, retentionUserID, 0)
	if err != nil {
		t.Fatalf("get message 1: %v", err)
	}
	var atts []models.MailAttachmentRef
	if err := json.Unmarshal([]byte(m1.AttachmentsJSON), &atts); err != nil || len(atts) != 1 {
		t.Fatalf("attachments of message 1: err=%v n=%d", err, len(atts))
	}
	sharedHash := atts[0].BlobHash
	if rc, ok := blobRefCount(t, state, sharedHash); !ok || rc != 2 {
		t.Fatalf("shared attachment refs = %d (row present %v), want 2", rc, ok)
	}
	sharedKey := BlobObjectKey(sharedHash)
	if !st.has(sharedKey) {
		t.Fatalf("shared blob object %s missing", sharedKey)
	}

	// Purge message 1 (aged): the shared blob must survive with refs=1; only
	// message 1's BODY blob hits zero and is reclaimed.
	backdateMessage(t, state, r1.MessageID, time.Now().UTC().Add(-30*24*time.Hour))
	sweeper := mustSweeper(t, svc, st)
	stats := sweepStats(t, sweeper)
	if stats.BlobRefsReclaimed != 1 {
		t.Fatalf("reclaimed = %d, want 1 (only the body blob of message 1)", stats.BlobRefsReclaimed)
	}
	if !st.has(sharedKey) {
		t.Fatal("live shared blob object was collected by the GC pass")
	}
	if rc, ok := blobRefCount(t, state, sharedHash); !ok || rc != 1 {
		t.Fatalf("shared attachment refs after first sweep = %d (row present %v), want 1", rc, ok)
	}
	if _, err := svc.GetMessage(r2.MessageID, retentionUserID, 0); err != nil {
		t.Fatalf("surviving message lost: %v", err)
	}
	if _, ok := blobRefCount(t, state, bodyHash(t, raw1)); ok {
		t.Fatal("purged body ref row still present, want reclaimed")
	}

	// Purge message 2 as well: the shared blob hits zero AND message 2's own
	// body blob becomes unreferenced — both rows and objects are reclaimed.
	backdateMessage(t, state, r2.MessageID, time.Now().UTC().Add(-30*24*time.Hour))
	stats = sweepStats(t, sweeper)
	if stats.BlobRefsReclaimed != 2 {
		t.Fatalf("second sweep reclaimed = %d, want 2 (message 2's body + the shared attachment)", stats.BlobRefsReclaimed)
	}
	if st.has(sharedKey) {
		t.Fatal("unreferenced blob object survived the GC pass")
	}
	if _, ok := blobRefCount(t, state, sharedHash); ok {
		t.Fatal("unreferenced blob ref row survived the GC pass")
	}
}

// TestRetentionSweepWithoutBlobStoreKeepsRefRows — with no blob store wired,
// the sweep still purges messages and syncs counters, but ref rows at <= 0
// stay put (deleting them would orphan their objects permanently) and no
// object is deleted.
func TestRetentionSweepWithoutBlobStoreKeepsRefRows(t *testing.T) {
	svc, state, st := newFakeService(t)
	mb := seedUserMailbox(t, svc, retentionUserID, "nostore@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	if err := svc.SetMailboxRetention(mb.ID, retentionUserID, 0, 7, 0); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	raw := simpleRaw("nostore-1", "s@example.test", "ret@example.test", "body")
	r1, err := svc.Append(context.Background(), retentionUserID, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	backdateMessage(t, state, r1.MessageID, time.Now().UTC().Add(-30*24*time.Hour))

	stats := sweepStats(t, mustSweeper(t, svc, nil))
	if stats.PurgedMessages != 1 || stats.BlobRefsReclaimed != 0 {
		t.Fatalf("stats = %+v, want 1 purged / 0 reclaimed (no store wired)", stats)
	}
	if rc, ok := blobRefCount(t, state, bodyHash(t, raw)); !ok || rc != 0 {
		t.Fatalf("body ref row = %d (present %v), want present at 0", rc, ok)
	}
	if !st.has(BlobObjectKey(bodyHash(t, raw))) {
		t.Fatal("blob object deleted without a store wired")
	}
}

// TestRetentionSweepIsolationBetweenMailboxes — one mailbox's policies never
// touch another user's mailbox: identical aged messages in a policy-less
// mailbox of a different user survive, and the sweep's counters only reflect
// the policied mailbox.
func TestRetentionSweepIsolationBetweenMailboxes(t *testing.T) {
	svc, state, _ := newFakeService(t)
	mbA := seedUserMailbox(t, svc, retentionUserID, "iso-a@example.test", 0)
	inboxA := inboxFolder(t, svc, mbA)
	mbB := seedUserMailbox(t, svc, retentionUserID+1, "iso-b@example.test", 0)
	inboxB := inboxFolder(t, svc, mbB)
	if err := svc.SetMailboxRetention(mbA.ID, retentionUserID, 0, 7, 0); err != nil {
		t.Fatalf("set retention: %v", err)
	}

	rawA := simpleRaw("iso-a", "s@example.test", "iso@example.test", "A aged")
	rawB := simpleRaw("iso-b", "s@example.test", "iso@example.test", "B aged")
	rA, err := svc.Append(context.Background(), retentionUserID, 0, AppendIn{FolderID: inboxA.ID, Raw: rawA})
	if err != nil {
		t.Fatalf("append A: %v", err)
	}
	rB, err := svc.Append(context.Background(), retentionUserID+1, 0, AppendIn{FolderID: inboxB.ID, Raw: rawB})
	if err != nil {
		t.Fatalf("append B: %v", err)
	}
	old := time.Now().UTC().Add(-90 * 24 * time.Hour)
	backdateMessage(t, state, rA.MessageID, old)
	backdateMessage(t, state, rB.MessageID, old)

	stats := sweepStats(t, mustSweeper(t, svc, nil))
	if stats.PurgedMessages != 1 || stats.PurgedBytes != int64(len(rawA)) {
		t.Fatalf("stats = %+v, want only mailbox A's message purged", stats)
	}
	if _, err := svc.GetMessage(rA.MessageID, retentionUserID, 0); err == nil {
		t.Fatal("A's aged message survived its own policy")
	}
	if _, err := svc.GetMessage(rB.MessageID, retentionUserID+1, 0); err != nil {
		t.Fatalf("B's message was touched by A's policy: %v", err)
	}
	mbBNow, err := svc.GetMailbox(mbB.ID, retentionUserID+1, 0)
	if err != nil {
		t.Fatalf("get mailbox B: %v", err)
	}
	if mbBNow.MessageCount != 1 || mbBNow.UsedBytes != int64(len(rawB)) {
		t.Fatalf("B counters = %d/%d, want 1/%d (untouched)", mbBNow.MessageCount, mbBNow.UsedBytes, len(rawB))
	}
}
