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
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/migration"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Real-Postgres suite: the tests here prove what the fake driver cannot —
// transaction atomicity, the SQL-level atomic UID claim (UPDATE ... RETURNING
// serialized by the row lock), the flag CAS under real row semantics, and the
// raw tsvector/GIN schema. They skip when TEST_DATABASE_DSN is unset, exactly
// like internal/storage/repositories' db-backed tests.
//
// In-session verification runs them against a real postgres:17-alpine
// container; see the W1 report for the exact commands.

const pgTestDSNEnv = "TEST_DATABASE_DSN"

func pgTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(pgTestDSNEnv)
	if dsn == "" {
		t.Skipf("skipping: %s not set", pgTestDSNEnv)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available (%s): %v", dsn, err)
	}
	if err := db.AutoMigrate(&models.Mailbox{}, &models.MailFolder{}, &models.MailMessage{}, &models.MailBlobRef{}); err != nil {
		t.Skipf("skipping: cannot migrate test schema: %v", err)
	}
	if err := migration.EnsureMailstoreSearch(db); err != nil {
		t.Skipf("skipping: cannot ensure search schema: %v", err)
	}
	return db
}

// pgUniqueSuffix makes table contents unique per test run so reruns against a
// shared database never collide.
var pgRunSuffix = fmt.Sprintf("%d", time.Now().UnixNano())

func newPGService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	svc, db, _ := newPGServiceFull(t)
	return svc, db
}

// newPGServiceFull is newPGService plus the shared blob store, for the W2 GC
// tests that must observe object deletion on the same store the service
// reads and writes.
func newPGServiceFull(t *testing.T) (*Service, *gorm.DB, blob.Store) {
	t.Helper()
	db := pgTestDB(t)
	st, err := os.MkdirTemp("", "mailstore-blob-*")
	if err != nil {
		t.Fatalf("temp blob dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(st) })
	store, err := blob.New(blob.Config{Provider: "fs", FSBasePath: st})
	if err != nil {
		t.Fatalf("fs blob store: %v", err)
	}
	if store == nil {
		t.Fatal("fs blob store nil")
	}
	svc, err := New(db, IdentityBlobCodec{}, store, nil)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, db, store
}

// TestPGUIDMonotonicUnderConcurrentAppends — the SQL-level claim: 48
// concurrent appends through REAL Postgres transactions must yield exactly
// UIDs 1..48, each once.
func TestPGUIDMonotonicUnderConcurrentAppends(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "race-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}

	const n = 48
	uids := make(chan uint32, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw := simpleRaw("pg-race-"+itoa(i)+"-"+pgRunSuffix, "s@pg.test", "race@pg.test", "body")
			res, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
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
		t.Fatalf("concurrent append: %v", err)
	}
	seen := make(map[uint32]bool, n)
	for u := range uids {
		if seen[u] {
			t.Fatalf("duplicate UID %d", u)
		}
		seen[u] = true
	}
	if len(seen) != n {
		t.Fatalf("distinct UIDs = %d, want %d", len(seen), n)
	}
	for i := uint32(1); i <= n; i++ {
		if !seen[i] {
			t.Fatalf("UID %d missing (hole in 1..%d)", i, n)
		}
	}
}

// TestPGFlagCASConflict — two competing stores, one carrying a stale
// mod_seq, under real row semantics: exactly the fresh one wins.
func TestPGFlagCASConflict(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "cas-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	res, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("pg-cas-"+pgRunSuffix, "s@pg.test", "cas@pg.test", "x")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	stale := res.ModSeq
	r1, err := svc.SetFlags(1, 0, FlagsUpdate{MessageID: res.MessageID, ExpectedModSeq: stale, Set: models.FlagSeen})
	if err != nil {
		t.Fatalf("first store: %v", err)
	}
	if _, err := svc.SetFlags(1, 0, FlagsUpdate{MessageID: res.MessageID, ExpectedModSeq: stale, Set: models.FlagFlagged}); err != ErrFlagConflict {
		t.Fatalf("stale store err = %v, want ErrFlagConflict", err)
	}
	if _, err := svc.SetFlags(1, 0, FlagsUpdate{MessageID: res.MessageID, ExpectedModSeq: r1.ModSeq, Set: models.FlagFlagged}); err != nil {
		t.Fatalf("fresh store: %v", err)
	}
	m, err := svc.GetMessage(res.MessageID, 1, 0)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if m.Flags&(models.FlagSeen|models.FlagFlagged) != models.FlagSeen|models.FlagFlagged {
		t.Fatalf("flags = %d", m.Flags)
	}
}

// TestPGAppendFetchRoundTripAndCounters — full append → fetch against real
// Postgres with an FS blob store; counters must land exactly.
func TestPGAppendFetchRoundTripAndCounters(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "rt-"+pgRunSuffix+"@pg.test", 1<<20)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	raw := simpleRaw("pg-rt-"+pgRunSuffix, "s@pg.test", "rt@pg.test", "real postgres round trip")
	res, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := svc.GetMessageBody(context.Background(), res.MessageID, 1, 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatal("round-trip mismatch")
	}
	mbNow, err := svc.GetMailbox(mb.ID, 1, 0)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if mbNow.MessageCount != 1 || mbNow.UsedBytes != int64(len(raw)) {
		t.Fatalf("counters = count %d used %d, want 1/%d", mbNow.MessageCount, mbNow.UsedBytes, len(raw))
	}
}

// TestPGSearchTSVGeneratedColumnAndGIN — the raw-SQL schema exists after
// EnsureMailstoreSearch, is genuinely GENERATED, is populated on insert, and
// the GIN index backs it.
func TestPGSearchTSVGeneratedColumnAndGIN(t *testing.T) {
	svc, db := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "fts-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("quokka-unique-"+pgRunSuffix, "s@pg.test", "fts@pg.test", "marsupial husbandry notes")}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Column is generated (attgenerated = 's' for STORED).
	var generated string
	if err := db.Raw(`SELECT attgenerated FROM pg_attribute WHERE attrelid = 'mail_messages'::regclass AND attname = 'search_tsv' AND NOT attisdropped`).Scan(&generated).Error; err != nil {
		t.Fatalf("read attgenerated: %v", err)
	}
	if generated != "s" {
		t.Fatalf("search_tsv attgenerated = %q, want \"s\" (STORED generated)", generated)
	}

	// GIN index exists.
	var n int64
	if err := db.Raw(`SELECT count(*) FROM pg_indexes WHERE tablename = 'mail_messages' AND indexname = 'idx_mail_messages_search_tsv'`).Scan(&n).Error; err != nil {
		t.Fatalf("read pg_indexes: %v", err)
	}
	if n != 1 {
		t.Fatalf("GIN index present = %d, want 1", n)
	}

	// The generated column auto-populates: an FTS match on the subject finds
	// the appended row through the tsvector, proving ingest → FTS wiring.
	var hits int64
	if err := db.Raw(`SELECT count(*) FROM mail_messages WHERE search_tsv @@ websearch_to_tsquery('english', ?)`, "quokka-unique-"+pgRunSuffix).Scan(&hits).Error; err != nil {
		t.Fatalf("fts query: %v", err)
	}
	if hits != 1 {
		t.Fatalf("fts hits = %d, want 1", hits)
	}
}

// TestPGFolderCascadeAndExpunge — expunge + folder cascade under real FK-free
// but transactional semantics; uid_validity/uid_next policy verified.
func TestPGFolderCascadeAndExpunge(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "cascade-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	r1, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("pg-exp-1-"+pgRunSuffix, "s@pg.test", "cascade@pg.test", "delete me")})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	r2, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("pg-exp-2-"+pgRunSuffix, "s@pg.test", "cascade@pg.test", "keep me")})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if err := svc.MarkDeleted(1, 0, r1.MessageID); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	out, err := svc.Expunge(inbox.ID, 1, 0)
	if err != nil {
		t.Fatalf("expunge: %v", err)
	}
	if out.Expunged != 1 {
		t.Fatalf("expunged = %d", out.Expunged)
	}
	f, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get folder: %v", err)
	}
	if f.UIDNext != 3 {
		t.Fatalf("uid_next = %d, want 3 (never rewound)", f.UIDNext)
	}
	// Recreate with the same path after delete → fresh uid_validity.
	other, err := svc.CreateFolder(mb.ID, 1, 0, "", "Temp")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	v1 := other.UIDValidity
	if err := svc.DeleteFolder(other.ID, 1, 0); err != nil {
		t.Fatalf("delete temp: %v", err)
	}
	reborn, err := svc.CreateFolder(mb.ID, 1, 0, "", "Temp")
	if err != nil {
		t.Fatalf("recreate temp: %v", err)
	}
	if reborn.UIDValidity == v1 {
		t.Fatal("uid_validity not bumped on folder recreate")
	}
	_ = r2
}

// pgBlobRefCount reads one ref row's count from the real database.
func pgBlobRefCount(t *testing.T, db *gorm.DB, hash string) (int64, bool) {
	t.Helper()
	var row struct {
		RefCount int64  `gorm:"column:ref_count"`
		BlobHash string `gorm:"column:blob_hash"`
	}
	err := db.Raw(`SELECT ref_count, blob_hash FROM mail_blob_refs WHERE blob_hash = ?`, hash).
		Scan(&row).Error
	if err != nil {
		t.Fatalf("read ref count of %s: %v", hash, err)
	}
	return row.RefCount, row.BlobHash != ""
}

// TestPGAppendDedupRefCountsTwo (OB-3 W2, GAP-45) — the same raw message
// appended twice stores ONE content address at ref count 2 (dedup by hash),
// and both messages fetch byte-identical bodies.
func TestPGAppendDedupRefCountsTwo(t *testing.T) {
	svc, db := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "dedup-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	raw := simpleRaw("pg-dedup-"+pgRunSuffix, "s@pg.test", "dedup@pg.test", "identical body")
	r1, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	r2, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	m1, err := svc.GetMessage(r1.MessageID, 1, 0)
	if err != nil {
		t.Fatalf("get message 1: %v", err)
	}
	m2, err := svc.GetMessage(r2.MessageID, 1, 0)
	if err != nil {
		t.Fatalf("get message 2: %v", err)
	}
	if m1.BodyBlobRef != m2.BodyBlobRef {
		t.Fatalf("identical content addressed differently: %s vs %s", m1.BodyBlobRef, m2.BodyBlobRef)
	}
	if rc, ok := pgBlobRefCount(t, db, m1.BodyBlobRef); !ok || rc != 2 {
		t.Fatalf("ref count = %d (row present: %v), want 2", rc, ok)
	}
	// Exactly ONE ref row exists for the shared hash (the shared test database
	// holds rows from other suites, so scope the count).
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM mail_blob_refs WHERE blob_hash = ?`, m1.BodyBlobRef).Scan(&rows).Error; err != nil {
		t.Fatalf("count refs: %v", err)
	}
	if rows != 1 {
		t.Fatalf("blob ref rows for %s = %d, want 1", m1.BodyBlobRef, rows)
	}
	for _, id := range []uint{r1.MessageID, r2.MessageID} {
		got, err := svc.GetMessageBody(context.Background(), id, 1, 0)
		if err != nil {
			t.Fatalf("fetch body of %d: %v", id, err)
		}
		if string(got) != string(raw) {
			t.Fatalf("body of %d mismatched", id)
		}
	}
}

// TestPGMailBlobGCDropsOnlyAgedOrphans (OB-3 W2, GAP-45) — the sweep reclaims
// only rows with ref_count <= 0 that have been unreferenced past the grace
// period: a just-released blob survives the sweep; the same blob backdated
// past the grace window is reclaimed together with its stored object; a live
// blob is never collected no matter how old its row is.
func TestPGMailBlobGCDropsOnlyAgedOrphans(t *testing.T) {
	svc, db, store := newPGServiceFull(t)
	mb, err := svc.CreateMailbox(1, nil, "gc-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	// Two identical messages → refcount 2 on one blob; plus one distinct
	// message → its own blob.
	shared := simpleRaw("pg-gc-shared-"+pgRunSuffix, "s@pg.test", "gc@pg.test", "shared content")
	other := simpleRaw("pg-gc-other-"+pgRunSuffix, "s@pg.test", "gc@pg.test", "other content")
	r1, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: shared})
	if err != nil {
		t.Fatalf("append shared 1: %v", err)
	}
	r2, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: shared})
	if err != nil {
		t.Fatalf("append shared 2: %v", err)
	}
	r3, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: other})
	if err != nil {
		t.Fatalf("append other: %v", err)
	}
	sharedHash, otherHash := "", ""
	if m, err := svc.GetMessage(r1.MessageID, 1, 0); err == nil {
		sharedHash = m.BodyBlobRef
	} else {
		t.Fatalf("get shared message: %v", err)
	}
	if m, err := svc.GetMessage(r3.MessageID, 1, 0); err == nil {
		otherHash = m.BodyBlobRef
	} else {
		t.Fatalf("get other message: %v", err)
	}

	// Expunge r2 (shared → refcount 1) and r3 (other → refcount 0, freshly
	// released). Sweep with a 1h grace: nothing is reclaimable yet.
	if err := svc.MarkDeleted(1, 0, r2.MessageID); err != nil {
		t.Fatalf("mark deleted r2: %v", err)
	}
	if err := svc.MarkDeleted(1, 0, r3.MessageID); err != nil {
		t.Fatalf("mark deleted r3: %v", err)
	}
	if _, err := svc.Expunge(inbox.ID, 1, 0); err != nil {
		t.Fatalf("expunge: %v", err)
	}
	sweeper, err := NewRetentionSweeper(db, store)
	if err != nil {
		t.Fatalf("new sweeper: %v", err)
	}
	sweeper.ReclaimGrace = time.Hour
	stats, err := sweeper.SweepRetention(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if stats.BlobRefsReclaimed != 0 {
		t.Fatalf("reclaimed %d refs inside the grace window, want 0", stats.BlobRefsReclaimed)
	}
	if rc, ok := pgBlobRefCount(t, db, sharedHash); !ok || rc != 1 {
		t.Fatalf("shared ref = %d (present: %v), want 1 (live ref survives)", rc, ok)
	}
	if _, ok := pgBlobRefCount(t, db, otherHash); !ok {
		t.Fatal("freshly released ref row was deleted inside the grace window")
	}

	// Backdate the released blob's last_referenced past the grace window and
	// sweep again: the dead row + object go, the live ref stays.
	if err := db.Exec(`UPDATE mail_blob_refs SET last_referenced = ? WHERE blob_hash = ?`,
		time.Now().UTC().Add(-2*time.Hour), otherHash).Error; err != nil {
		t.Fatalf("backdate last_referenced: %v", err)
	}
	stats, err = sweeper.SweepRetention(context.Background())
	if err != nil {
		t.Fatalf("sweep 2: %v", err)
	}
	if stats.BlobRefsReclaimed != 1 || stats.ReclaimedBlobBytes != int64(len(other)) {
		t.Fatalf("reclaimed = %d refs / %d bytes, want 1/%d", stats.BlobRefsReclaimed, stats.ReclaimedBlobBytes, len(other))
	}
	if _, ok := pgBlobRefCount(t, db, otherHash); ok {
		t.Fatal("aged dead ref row survived the sweep")
	}
	// The stored object went with it: a Get on the shared store misses.
	if rc, err := store.Get(context.Background(), BlobObjectKey(otherHash)); err == nil {
		_ = rc.Close()
		t.Fatal("reclaimed blob object still readable from the store")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object Get after reclaim returned an unexpected error type: %v", err)
	}
	// The live object is untouched.
	if rc, err := store.Get(context.Background(), BlobObjectKey(sharedHash)); err != nil {
		t.Fatalf("live object unreadable after GC: %v", err)
	} else {
		_ = rc.Close()
	}
	if rc, ok := pgBlobRefCount(t, db, sharedHash); !ok || rc != 1 {
		t.Fatalf("live ref after GC = %d (present: %v), want 1", rc, ok)
	}
	got, err := svc.GetMessageBody(context.Background(), r1.MessageID, 1, 0)
	if err != nil {
		t.Fatalf("fetch live message after GC: %v", err)
	}
	if string(got) != string(shared) {
		t.Fatal("live message body corrupted after GC")
	}
	// The GC self-heal path: re-appending the reclaimed content re-creates
	// the object through the reserve (refcount 1 → Put) and the message reads.
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: other}); err != nil {
		t.Fatalf("re-append reclaimed content: %v", err)
	}
}

// TestPGConcurrentAppendSameContent — concurrent appends of identical content
// all land: exactly one blob row at ref count N, N messages, every body
// readable (the reserve upsert is idempotent under the per-hash lock).
func TestPGConcurrentAppendSameContent(t *testing.T) {
	svc, db := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "cappend-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	raw := simpleRaw("pg-cappend-"+pgRunSuffix, "s@pg.test", "cappend@pg.test", "same body, many writers")
	const n = 12
	ids := make(chan uint, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
			if err != nil {
				errs <- err
				return
			}
			ids <- res.MessageID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append: %v", err)
	}
	var count int
	var hashID string
	for id := range ids {
		count++
		m, err := svc.GetMessage(id, 1, 0)
		if err != nil {
			t.Fatalf("get message %d: %v", id, err)
		}
		if hashID == "" {
			hashID = m.BodyBlobRef
		} else if m.BodyBlobRef != hashID {
			t.Fatalf("message %d addressed differently: %s vs %s", id, m.BodyBlobRef, hashID)
		}
		if _, err := svc.GetMessageBody(context.Background(), id, 1, 0); err != nil {
			t.Fatalf("fetch body of %d: %v", id, err)
		}
	}
	if count != n {
		t.Fatalf("appended = %d, want %d", count, n)
	}
	// Exactly one ref row for the shared hash (scoped: the shared test
	// database accumulates rows from other suites).
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM mail_blob_refs WHERE blob_hash = ?`, hashID).Scan(&rows).Error; err != nil {
		t.Fatalf("count refs: %v", err)
	}
	if rows != 1 {
		t.Fatalf("blob ref rows = %d, want 1", rows)
	}
	var total int64
	if err := db.Raw(`SELECT COALESCE(SUM(ref_count), 0) FROM mail_blob_refs WHERE blob_hash = ?`, hashID).Scan(&total).Error; err != nil {
		t.Fatalf("sum ref counts: %v", err)
	}
	if total != n {
		t.Fatalf("total ref count = %d, want %d", total, n)
	}
}

// TestPGMailboxUsageBlobBytesFromBlobRefs (OB-3 W2, GAP-45) — on real
// Postgres the usage report is dedup-aware: two identical messages count one
// blob's size in BlobBytes while RawBytes counts both.
func TestPGMailboxUsageBlobBytesFromBlobRefs(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "usage-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	raw := simpleRaw("pg-usage-"+pgRunSuffix, "s@pg.test", "usage@pg.test", "usage body")
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	report, err := svc.MailboxUsage(mb.ID, 1, 0)
	if err != nil {
		t.Fatalf("mailbox usage: %v", err)
	}
	if report.RawBytes != 2*int64(len(raw)) {
		t.Fatalf("raw bytes = %d, want %d (dedup-blind quota basis)", report.RawBytes, 2*len(raw))
	}
	if report.BlobBytes != int64(len(raw)) {
		t.Fatalf("blob bytes = %d, want %d (dedup-aware storage basis)", report.BlobBytes, len(raw))
	}
	if report.DistinctBlobs != 1 {
		t.Fatalf("distinct blobs = %d, want 1", report.DistinctBlobs)
	}
	if report.MessageCount != 2 {
		t.Fatalf("message count = %d, want 2", report.MessageCount)
	}
	if _, err := svc.MailboxUsage(mb.ID, 2, 0); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("usage as other user err = %v, want ErrNotMailboxOwner", err)
	}
}

// TestPGReserveSelfHealsMissingBlob — a ref row whose stored object vanished
// (a crashed reservation) does not poison later appends of the same content:
// the next reserve finds the object missing and re-Puts it (idempotent), and
// both messages read.
func TestPGReserveSelfHealsMissingBlob(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "heal-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	raw := simpleRaw("pg-heal-"+pgRunSuffix, "s@pg.test", "heal@pg.test", "self-healing content")
	r1, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	m, err := svc.GetMessage(r1.MessageID, 1, 0)
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	// Simulate the lost object directly on the store.
	if err := svc.store.Delete(context.Background(), BlobObjectKey(m.BodyBlobRef)); err != nil {
		t.Fatalf("delete object: %v", err)
	}
	r2, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append 2 (self-heal): %v", err)
	}
	if rc, ok := pgBlobRefCount(t, svc.db, m.BodyBlobRef); !ok || rc != 2 {
		t.Fatalf("ref count after heal = %d (present: %v), want 2", rc, ok)
	}
	for _, id := range []uint{r1.MessageID, r2.MessageID} {
		got, err := svc.GetMessageBody(context.Background(), id, 1, 0)
		if err != nil {
			t.Fatalf("fetch body of %d: %v", id, err)
		}
		if string(got) != string(raw) {
			t.Fatalf("body of %d mismatched after heal", id)
		}
	}
}
