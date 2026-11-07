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

package jobs

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The GAP-45 GC audit suite (OB-3 W2): the retention job must record an
// audited summary event for the mail-blob reclamation (with the per-mailbox
// blob-byte usage from mail_blob_refs) and stay quiet on a no-op night. The
// sweeper itself is faked (the real one is verified against real Postgres in
// the mailstore package); the events table rides a minimal database/sql fake.

// TestMailBlobGCAuditEventWritten — a sweep that reclaimed blobs produces one
// audit event of type mail_blob_gc whose metadata carries the reclaim stats
// and the usage summary from the reporter.
func TestMailBlobGCAuditEventWritten(t *testing.T) {
	state := newMbgcState()
	job := &RetentionCleanupJob{
		eventRepo: repositories.NewEventRepository(mbgcGorm(t, state)),
	}
	job.SetMailstoreSweeper(fakeSweeper{stats: mailstore.RetentionStats{
		MailboxesScanned:   3,
		PurgedMessages:     2,
		PurgedBytes:        2048,
		BlobRefsReclaimed:  4,
		ReclaimedBlobBytes: 4096,
	}})
	job.SetMailBlobUsageReporter(fakeUsageReporter{reports: []mailstore.MailboxUsageReport{
		{MailboxID: 1, BlobBytes: 1024, DistinctBlobs: 3},
		{MailboxID: 2, BlobBytes: 0, DistinctBlobs: 0},
	}})

	job.auditMailBlobGC(mailstore.RetentionStats{
		MailboxesScanned:   3,
		PurgedMessages:     2,
		PurgedBytes:        2048,
		BlobRefsReclaimed:  4,
		ReclaimedBlobBytes: 4096,
	})

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(state.events))
	}
	ev := state.events[0]
	if ev.Type != "mail_blob_gc" {
		t.Fatalf("event type = %q, want mail_blob_gc", ev.Type)
	}
	if ev.Category != models.EventCategoryAudit {
		t.Fatalf("event category = %q, want audit", ev.Category)
	}
	if ev.Outcome != "success" {
		t.Fatalf("event outcome = %q, want success", ev.Outcome)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(ev.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v (%q)", err, ev.Metadata)
	}
	if meta["blob_refs_reclaimed"].(float64) != 4 {
		t.Fatalf("metadata blob_refs_reclaimed = %v, want 4", meta["blob_refs_reclaimed"])
	}
	if meta["reclaimed_blob_bytes"].(float64) != 4096 {
		t.Fatalf("metadata reclaimed_blob_bytes = %v, want 4096", meta["reclaimed_blob_bytes"])
	}
	if meta["total_blob_bytes"].(float64) != 1024 {
		t.Fatalf("metadata total_blob_bytes = %v, want 1024 (usage summary missing)", meta["total_blob_bytes"])
	}
	if _, ok := meta["per_mailbox"]; !ok {
		t.Fatal("metadata per_mailbox missing")
	}
}

// TestMailBlobGCAuditEventSkippedWhenQuiet — a sweep with nothing reclaimed
// and nothing purged writes no audit event (no nightly noise).
func TestMailBlobGCAuditEventSkippedWhenQuiet(t *testing.T) {
	state := newMbgcState()
	job := &RetentionCleanupJob{
		eventRepo: repositories.NewEventRepository(mbgcGorm(t, state)),
	}
	job.SetMailBlobUsageReporter(fakeUsageReporter{reports: []mailstore.MailboxUsageReport{}})

	job.auditMailBlobGC(mailstore.RetentionStats{}) // nothing happened

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.events) != 0 {
		t.Fatalf("audit events = %d, want 0 on a quiet sweep", len(state.events))
	}
}

// TestMailBlobGCAuditEventWithoutReporter — the usage reporter is optional:
// without it the event still lands, with the stats and a marked-unavailable
// usage summary.
func TestMailBlobGCAuditEventWithoutReporter(t *testing.T) {
	state := newMbgcState()
	job := &RetentionCleanupJob{
		eventRepo: repositories.NewEventRepository(mbgcGorm(t, state)),
	}
	job.auditMailBlobGC(mailstore.RetentionStats{BlobRefsReclaimed: 1, ReclaimedBlobBytes: 10})

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(state.events))
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(state.events[0].Metadata), &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta["blob_bytes_summary"] != "unavailable" {
		t.Fatalf("blob_bytes_summary = %v, want unavailable", meta["blob_bytes_summary"])
	}
	if _, ok := meta["total_blob_bytes"]; ok {
		t.Fatal("total_blob_bytes present without a reporter")
	}
}

// ─── Fakes ───

type fakeSweeper struct {
	stats mailstore.RetentionStats
}

func (f fakeSweeper) SweepRetention(context.Context) (mailstore.RetentionStats, error) {
	return f.stats, nil
}

type fakeUsageReporter struct {
	reports []mailstore.MailboxUsageReport
}

func (f fakeUsageReporter) MailboxBlobUsageAll() ([]mailstore.MailboxUsageReport, error) {
	return f.reports, nil
}

// Minimal events-table fake driver (the campaign_scheduler_test.go
// arrangement) — it records INSERTs into "events".

type mbgcState struct {
	mu     sync.Mutex
	seq    int64
	events []models.Event
}

func newMbgcState() *mbgcState { return &mbgcState{seq: 100} }

type mbgcConn struct{ s *mbgcState }

func (c *mbgcConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *mbgcConn) Close() error                        { return nil }
func (c *mbgcConn) Begin() (driver.Tx, error)           { return mbgcTx{}, nil }

type mbgcTx struct{}

func (mbgcTx) Commit() error   { return nil }
func (mbgcTx) Rollback() error { return nil }

type mbgcResult struct{ rows int64 }

func (r mbgcResult) LastInsertId() (int64, error) { return 0, nil }
func (r mbgcResult) RowsAffected() (int64, error) { return r.rows, nil }

type mbgcRows struct {
	cols []string
	i    int
}

func (r *mbgcRows) Columns() []string { return r.cols }
func (r *mbgcRows) Close() error      { return nil }
func (r *mbgcRows) Next([]driver.Value) error {
	if r.i == 0 {
		r.i++
		return nil
	}
	return io.EOF
}

type mbgcConnector struct{ state *mbgcState }

func (c mbgcConnector) Connect(context.Context) (driver.Conn, error) {
	return &mbgcConn{s: c.state}, nil
}
func (c mbgcConnector) Driver() driver.Driver { return mbgcDrv{} }

type mbgcDrv struct{}

func (mbgcDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

var mbgcDriverOnce sync.Once

func mbgcGorm(t *testing.T, state *mbgcState) *gorm.DB {
	t.Helper()
	mbgcDriverOnce.Do(func() {
		sql.Register("mailblob-gc-fake", mbgcDrv{})
	})
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(mbgcConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open gc-fake gorm: %v", err)
	}
	return gdb
}

func (c *mbgcConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if strings.HasPrefix(lower, "insert into \"events\"") || strings.HasPrefix(lower, "insert into events") {
		c.recordEventInsert(lower, args)
		return &mbgcRows{cols: []string{"id"}}, nil
	}
	return nil, errors.New("mbgc fake: unsupported query: " + query)
}

// ExecContext records event INSERTs issued through Exec (defensive: gorm's
// postgres driver usually routes Create through QueryContext with RETURNING).
func (c *mbgcConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if strings.HasPrefix(lower, "insert into \"events\"") || strings.HasPrefix(lower, "insert into events") {
		c.recordEventInsert(lower, args)
		return mbgcResult{rows: 1}, nil
	}
	return nil, errors.New("mbgc fake: unsupported exec: " + query)
}

// recordEventInsert captures the audit event from an INSERT column list +
// bind args (column order taken from the statement, not assumed).
func (c *mbgcConn) recordEventInsert(lower string, args []driver.NamedValue) {
	open := strings.Index(lower, "(")
	closeIdx := strings.Index(lower, ")")
	if open < 0 || closeIdx < 0 {
		return
	}
	cols := strings.Split(lower[open+1:closeIdx], ",")
	value := func(name string) any {
		for i, col := range cols {
			if strings.Trim(col, " \"") != name {
				continue
			}
			// The i-th column binds to the i-th values placeholder; gorm uses
			// positional ordinals 1..N.
			if i < len(args) {
				return args[i].Value
			}
		}
		return nil
	}
	ev := models.Event{}
	if s, ok := value("category").(string); ok {
		ev.Category = models.EventCategory(s)
	}
	if s, ok := value("type").(string); ok {
		ev.Type = s
	}
	if s, ok := value("message").(string); ok {
		ev.Message = s
	}
	if s, ok := value("metadata").(string); ok {
		ev.Metadata = s
	}
	if s, ok := value("outcome").(string); ok {
		ev.Outcome = s
	}
	if s, ok := value("target_type").(string); ok {
		ev.TargetType = s
	}
	if s, ok := value("target_id").(string); ok {
		ev.TargetID = s
	}
	c.s.mu.Lock()
	c.s.seq++
	c.s.events = append(c.s.events, ev)
	c.s.mu.Unlock()
}
