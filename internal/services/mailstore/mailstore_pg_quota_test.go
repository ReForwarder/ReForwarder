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
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// Real-Postgres quota/retention suite (quota + retention waves, ADR-003
// §5/§7): the fake driver cannot prove transactional locking, so the
// concurrency guarantee (a nearly-full quota is never overshot by racing
// appends) and the real-SQL sweep (created_at window, ORDER BY + LIMIT,
// DELETE ... RETURNING) run here against a live Postgres. Skips when
// TEST_DATABASE_DSN is unset, exactly like mailstore_pg_test.go.

// TestPGConcurrentAppendQuotaNoOvershoot — 24 concurrent appends of
// equal-size messages into one mailbox whose byte quota admits exactly 10 of
// them: the locked quota check (SELECT ... FOR UPDATE inside each append
// transaction) must admit exactly 10, and used_bytes must end exactly at the
// quota — never above it.
func TestPGConcurrentAppendQuotaNoOvershoot(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "quota-race-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}

	// Same-length subjects (zero-padded index + run suffix) keep every
	// message — including the probe — byte-identical in size.
	probe := simpleRaw(fmt.Sprintf("pg-quota-race-%02d", 0)+"-"+pgRunSuffix, "s@pg.test", "quota@pg.test", "size probe")
	msgSize := int64(len(probe))
	const admit = 10
	const n = 24
	quota := msgSize * admit
	if err := svc.SetMailboxQuota(mb.ID, 1, 0, quota); err != nil {
		t.Fatalf("set quota: %v", err)
	}

	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw := simpleRaw(fmt.Sprintf("pg-quota-race-%02d", i)+"-"+pgRunSuffix, "s@pg.test", "quota@pg.test", "size probe")
			if int64(len(raw)) != msgSize {
				errs <- fmt.Errorf("message %d size %d != probe %d", i, len(raw), msgSize)
				return
			}
			_, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)

	admitted := 0
	for err := range errs {
		if err == nil {
			admitted++
			continue
		}
		if !errors.Is(err, ErrQuotaExceeded) {
			t.Fatalf("append failed with unexpected error: %v", err)
		}
	}
	if admitted != admit {
		t.Fatalf("admitted = %d, want exactly %d", admitted, admit)
	}
	mbNow, err := svc.GetMailbox(mb.ID, 1, 0)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if mbNow.UsedBytes != quota || mbNow.MessageCount != admit {
		t.Fatalf("counters = %d bytes/%d messages, want %d/%d (never over the quota)",
			mbNow.UsedBytes, mbNow.MessageCount, quota, admit)
	}
}

// TestPGRetentionSweepAgainstRealPostgres — the sweep's real-SQL surface:
// the created_at age window, ORDER BY created_at/uid + LIMIT, the counter
// sync, and the grace-guarded blob reclamation (W2 added a reclaim grace
// anchored on last_referenced; this test shortens it via ReclaimGrace), all
// against real Postgres with an fs blob store.
func TestPGRetentionSweepAgainstRealPostgres(t *testing.T) {
	svc, db := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "retention-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	if err := svc.SetMailboxRetention(mb.ID, 1, 0, 7, 0); err != nil {
		t.Fatalf("set retention: %v", err)
	}

	// Unique subjects per run → unique content hashes, so the assertions
	// below hold on a shared test database that accumulates earlier runs.
	oldRaw := simpleRaw("pg-ret-old-"+pgRunSuffix, "s@pg.test", "ret@pg.test", "old body")
	freshRaw := simpleRaw("pg-ret-fresh-"+pgRunSuffix, "s@pg.test", "ret@pg.test", "fresh body")
	rOld, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: oldRaw})
	if err != nil {
		t.Fatalf("append old: %v", err)
	}
	rFresh, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: freshRaw})
	if err != nil {
		t.Fatalf("append fresh: %v", err)
	}
	// Backdate through the real database — the sweep's age window is SQL-side.
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if err := db.Model(&models.MailMessage{}).Where("id = ?", rOld.MessageID).
		Update("created_at", cutoff).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}

	sweeper, err := NewRetentionSweeper(db, svc.store)
	if err != nil {
		t.Fatalf("new sweeper: %v", err)
	}
	sweeper.ReclaimGrace = time.Microsecond // test mode: reclaim immediately past the grace anchor
	stats, err := sweeper.SweepRetention(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if stats.PurgedMessages != 1 || stats.PurgedBytes != int64(len(oldRaw)) {
		t.Fatalf("stats = %+v, want 1 purged / %d bytes", stats, len(oldRaw))
	}
	if stats.BlobRefsReclaimed < 1 {
		t.Fatalf("reclaimed = %d, want >= 1 (the aged message's body blob)", stats.BlobRefsReclaimed)
	}
	if _, err := svc.GetMessage(rOld.MessageID, 1, 0); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("aged message err = %v, want ErrMessageNotFound", err)
	}
	if _, err := svc.GetMessage(rFresh.MessageID, 1, 0); err != nil {
		t.Fatalf("fresh message deleted: %v", err)
	}
	// The aged body's ref row is gone and its stored object was deleted; the
	// fresh message's blob is untouched.
	_, oldRef, err := IdentityBlobCodec{}.Encode(oldRaw)
	if err != nil {
		t.Fatalf("encode old: %v", err)
	}
	_, freshRef, err := IdentityBlobCodec{}.Encode(freshRaw)
	if err != nil {
		t.Fatalf("encode fresh: %v", err)
	}
	var n int64
	if err := db.Model(&models.MailBlobRef{}).Where("blob_hash = ?", oldRef.Hash).Count(&n).Error; err != nil {
		t.Fatalf("count reclaimed ref rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("reclaimed ref row for %s still present (%d rows)", oldRef.Hash[:12], n)
	}
	if _, err := svc.store.Get(context.Background(), BlobObjectKey(oldRef.Hash)); err == nil {
		t.Fatal("reclaimed blob object still present in the store")
	}
	if _, err := svc.store.Get(context.Background(), BlobObjectKey(freshRef.Hash)); err != nil {
		t.Fatalf("fresh blob object missing after sweep: %v", err)
	}
	mbNow, err := svc.GetMailbox(mb.ID, 1, 0)
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	if mbNow.MessageCount != 1 || mbNow.UsedBytes != int64(len(freshRaw)) {
		t.Fatalf("counters = %d/%d, want 1/%d", mbNow.MessageCount, mbNow.UsedBytes, len(freshRaw))
	}
}
