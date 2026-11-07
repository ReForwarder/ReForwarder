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
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OB-3 (ADR-003 §7): the unified retention job gains the hosted-mailbox
// family. The sweep itself is covered in internal/services/mailstore; these
// tests pin the job-level contract: the family runs when a sweeper is wired,
// is skipped (without affecting other families) when it is not, and a sweep
// error is logged, not propagated — the job's other families must still be
// considered run.

// fakeMailstoreSweeper records SweepRetention calls.
type fakeMailstoreSweeper struct {
	mu    sync.Mutex
	calls int
	err   error
	stats mailstore.RetentionStats
}

func (f *fakeMailstoreSweeper) SweepRetention(context.Context) (mailstore.RetentionStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.stats, f.err
}

// rmsState is a minimal fake database answering exactly one query: the
// settings provider's `select * from settings order by key asc`. Every
// retention window is seeded to 0 so all other families are skipped and the
// fake needs to answer nothing else (their repos are nil).
var rmsColumns = []string{"id", "key", "value", "type", "created_at", "updated_at"}

type rmsState struct {
	mu     sync.Mutex
	rows   [][]driver.Value
	nextID int64
}

func newRMSState(t *testing.T, windows [][2]string) *rmsState {
	t.Helper()
	s := &rmsState{nextID: 1}
	now := time.Now().UTC()
	for _, kv := range windows {
		row := make([]driver.Value, len(rmsColumns))
		row[0] = s.nextID
		row[1] = kv[0]
		row[2] = kv[1]
		row[3] = "int"
		row[4] = now
		row[5] = now
		s.nextID++
		s.rows = append(s.rows, row)
	}
	return s
}

func rmsAllOff(t *testing.T) *rmsState {
	return newRMSState(t, [][2]string{
		{"retention_days", "0"},
		{"email_body_retention_days", "0"},
		{"email_attachment_retention_days", "0"},
		{"audit_log_retention_days", "0"},
		{"webhook_delivery_retention_days", "0"},
		{"tracking_event_retention_days", "0"},
		{"email_events_retention_days", "0"},
		{"message_retention_days", "0"},
		{"message_spam_retention_days", "0"},
		{"security_events_retention_days", "0"},
	})
}

type rmsConnector struct{ state *rmsState }

func (c rmsConnector) Connect(context.Context) (driver.Conn, error) { return &rmsConn{s: c.state}, nil }
func (c rmsConnector) Driver() driver.Driver                        { return rmsDrv{} }

type rmsDrv struct{}

func (rmsDrv) Open(string) (driver.Conn, error) { return nil, errors.New("rms: use sql.OpenDB") }

type rmsConn struct{ s *rmsState }

func (c *rmsConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("rms: no prepare") }
func (c *rmsConn) Close() error                        { return nil }
func (c *rmsConn) Begin() (driver.Tx, error)           { return rmsTx{}, nil }

type rmsTx struct{}

func (rmsTx) Commit() error   { return nil }
func (rmsTx) Rollback() error { return nil }

func (c *rmsConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(strings.Join(strings.Fields(query), " "))
	lower = strings.ReplaceAll(lower, `"`, "")
	if strings.Contains(lower, "from settings") {
		c.s.mu.Lock()
		defer c.s.mu.Unlock()
		rows := make([][]driver.Value, len(c.s.rows))
		copy(rows, c.s.rows)
		return &rmsRows{cols: rmsColumns, rows: rows}, nil
	}
	return nil, fmt.Errorf("rms: cannot route query: %s", query)
}

func (c *rmsConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	return nil, fmt.Errorf("rms: unexpected exec: %s", query)
}

type rmsRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *rmsRows) Columns() []string { return r.cols }
func (r *rmsRows) Close() error      { return nil }
func (r *rmsRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// rmsGorm opens the fake settings-only database.
func rmsGorm(t *testing.T, state *rmsState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(rmsConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open rms fake gorm: %v", err)
	}
	return gdb
}

// newRetentionMailstoreJob builds the retention job with every repo family
// nil (all windows seeded to 0) and only the mailstore sweeper wired.
func newRetentionMailstoreJob(t *testing.T, state *rmsState) *RetentionCleanupJob {
	t.Helper()
	sp := settings.NewProvider(repositories.NewSettingRepository(rmsGorm(t, state)))
	return NewRetentionCleanupJob(nil, nil, nil, nil, sp)
}

// TestRetentionJobRunsMailstoreFamily — with a sweeper wired, Run executes
// the mailstore family once and swallows nothing about its stats.
func TestRetentionJobRunsMailstoreFamily(t *testing.T) {
	job := newRetentionMailstoreJob(t, rmsAllOff(t))
	fake := &fakeMailstoreSweeper{stats: mailstore.RetentionStats{
		MailboxesScanned: 2, FoldersScanned: 5, PurgedMessages: 7, PurgedBytes: 9001, BlobRefsReclaimed: 3,
	}}
	job.SetMailstoreSweeper(fake)
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("run with mailstore sweeper: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("sweep calls = %d, want 1", fake.calls)
	}
}

// TestRetentionJobNilMailstoreSweeperSkips — without a sweeper (mailstore
// not wired) the family is skipped and Run still completes: the other
// families are unaffected by the additive family.
func TestRetentionJobNilMailstoreSweeperSkips(t *testing.T) {
	job := newRetentionMailstoreJob(t, rmsAllOff(t))
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("run without mailstore sweeper: %v", err)
	}
}

// TestRetentionJobMailstoreErrorLoggedNotPropagated — a failing sweep is
// logged (log-and-continue, matching every other family) and Run still
// returns nil.
func TestRetentionJobMailstoreErrorLoggedNotPropagated(t *testing.T) {
	job := newRetentionMailstoreJob(t, rmsAllOff(t))
	fake := &fakeMailstoreSweeper{err: errors.New("simulated sweep failure")}
	job.SetMailstoreSweeper(fake)
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("sweep error must not propagate, got: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("sweep calls = %d, want 1", fake.calls)
	}
}
