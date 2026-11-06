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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/retry"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// R-27 tests: the retry sweep must load failed emails in bounded batches
// (LIMIT) and drain a large backlog across ticks, and the sweep loop must be
// jitter-spread so processes started together do not stampede. These tests run
// against a self-contained fake database/sql driver that implements exactly
// the queries the retry worker issues (smtp_servers SELECT, emails SELECT with
// ORDER/LIMIT, emails UPDATE claim) — independent of the cwState harness, whose
// table router only knows the campaign tables.

type rbRow map[string]driver.Value

// debugRB dumps each fake query's match count to stderr when set.
var debugRB = os.Getenv("RB_DEBUG") != ""

type rbState struct {
	mu      sync.Mutex
	servers []rbRow
	emails  []rbRow
	seq     int64
	claims  []uint // email ids successfully claimed (failed -> queued)
}

func newRBState() *rbState { return &rbState{seq: 1000} }

func (s *rbState) seedServer(workspaceID *uint, maxRetries int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	row := rbRow{"id": s.seq, "user_id": int64(1), "workspace_id": rbUintPtr(workspaceID), "max_retries": int64(maxRetries), "status": models.SMTPStatusEnabled}
	s.servers = append(s.servers, row)
}

func (s *rbState) seedFailedEmail(id uint, workspaceID *uint, ageHours int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emails = append(s.emails, rbRow{
		"id":            int64(id),
		"user_id":       int64(1),
		"workspace_id":  rbUintPtr(workspaceID),
		"status":        models.EmailStatusFailed,
		"retry_count":   int64(0),
		"error_message": "boom",
		"created_at":    time.Now().Add(-time.Duration(ageHours) * time.Hour).UTC(),
	})
}

func rbUintPtr(p *uint) driver.Value {
	if p == nil {
		return nil
	}
	return int64(*p)
}

func (s *rbState) failedIDs() []uint {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []uint
	for _, row := range s.emails {
		if row["status"] == models.EmailStatusFailed {
			out = append(out, uint(row["id"].(int64)))
		}
	}
	return out
}

func (s *rbState) retryCount(id uint) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.emails {
		if uint(row["id"].(int64)) == id {
			return row["retry_count"].(int64)
		}
	}
	return -1
}

// rbEval compares one WHERE conjunct against a row. Supports `col = $n`,
// `col < $n`, `col > <literal>` and `col = <literal>`.
func rbEval(row rbRow, cond string, args []driver.NamedValue) (bool, error) {
	cond = strings.TrimSpace(cond)
	for _, op := range []string{" = ", " < ", " > "} {
		i := strings.Index(cond, op)
		if i < 0 {
			continue
		}
		col := strings.Trim(strings.TrimSpace(cond[:i]), `"`)
		raw := strings.TrimSpace(cond[i+len(op):])
		var want driver.Value
		if strings.HasPrefix(raw, "$") {
			n, err := strconv.Atoi(raw[1:])
			if err != nil {
				return false, fmt.Errorf("rbState: bad bindvar %q", raw)
			}
			found := false
			for _, a := range args {
				if a.Ordinal == n {
					want, found = a.Value, true
					break
				}
			}
			if !found {
				return false, fmt.Errorf("rbState: no arg %d", n)
			}
		} else {
			if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
				want = n
			} else {
				want = strings.Trim(raw, "'\"")
			}
		}
		have, ok := row[col]
		if !ok {
			return false, fmt.Errorf("rbState: unknown column %q", col)
		}
		switch op {
		case " = ":
			return rbEqual(have, want), nil
		case " < ":
			hf, hfOK := rbNum(have)
			wf, wfOK := rbNum(want)
			return hfOK && wfOK && hf < wf, nil
		case " > ":
			hf, hfOK := rbNum(have)
			wf, wfOK := rbNum(want)
			return hfOK && wfOK && hf > wf, nil
		}
	}
	return false, fmt.Errorf("rbState: unsupported condition %q", cond)
}

func rbNum(v driver.Value) (float64, bool) {
	switch t := v.(type) {
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case uint64:
		return float64(t), true
	case time.Time:
		return float64(t.UnixNano()), true
	case string:
		if n, err := strconv.ParseFloat(t, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func rbEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	// String comparison is type-flexible: seeded rows may carry typed string
	// constants (models.EmailStatus) where the driver hands args as string.
	_, aStr := a.(string)
	_, bStr := b.(string)
	if aStr || bStr {
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
	// Numeric comparison is type-flexible: the driver may hand args through
	// as int where the fake stores int64.
	ia, aOK := rbNum(a)
	ib, bOK := rbNum(b)
	if aOK && bOK {
		return ia == ib
	}
	return a == b
}

func (s *rbState) selectRows(table, query string, args []driver.NamedValue) ([][]driver.Value, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var src []rbRow
	var cols []string
	switch table {
	case "smtp_servers":
		src, cols = s.servers, []string{"id", "user_id", "workspace_id", "max_retries", "status"}
	case "emails":
		src, cols = s.emails, []string{"id", "user_id", "workspace_id", "status", "retry_count", "error_message", "created_at"}
	default:
		return nil, nil, fmt.Errorf("rbState: unknown table %q", table)
	}

	where := ""
	if i := strings.Index(query, " where "); i >= 0 {
		where = query[i+len(" where "):]
	}
	// Trailing clauses are handled below; never let them leak into the WHERE
	// conjunctions.
	for _, cut := range []string{" order by ", " limit ", " offset "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}
	orderAsc := strings.Contains(query, " order by created_at asc")

	var preds []func(rbRow) bool
	if where != "" {
		for _, cond := range strings.Split(where, " and ") {
			c := cond
			// Compile the predicate once so an unparseable condition surfaces
			// as an error instead of silently matching nothing.
			probe := rbRow{"id": int64(0), "user_id": int64(0), "workspace_id": nil, "max_retries": int64(0), "status": "", "retry_count": int64(0), "error_message": "", "created_at": time.Unix(0, 0).UTC()}
			if _, err := rbEval(probe, c, args); err != nil {
				return nil, nil, err
			}
			preds = append(preds, func(row rbRow) bool {
				ok, err := rbEval(row, c, args)
				return err == nil && ok
			})
		}
	}

	var matched []rbRow
	for _, row := range src {
		ok := true
		for _, p := range preds {
			if !p(row) {
				ok = false
				break
			}
		}
		if ok {
			matched = append(matched, row)
		}
	}
	if debugRB {
		fmt.Fprintf(os.Stderr, "rbState: table=%s query=%q matched=%d of %d\n", table, query, len(matched), len(src))
	}
	if orderAsc {
		for i := 1; i < len(matched); i++ {
			for j := i; j > 0; j-- {
				a := matched[j]["created_at"].(time.Time)
				b := matched[j-1]["created_at"].(time.Time)
				if a.Before(b) {
					matched[j], matched[j-1] = matched[j-1], matched[j]
				} else {
					break
				}
			}
		}
	}
	// LIMIT $n (the R-27 bound under test).
	if i := strings.Index(query, " limit "); i >= 0 {
		raw := strings.TrimSpace(query[i+len(" limit "):])
		if strings.HasPrefix(raw, "$") {
			if n, err := strconv.Atoi(raw[1:]); err == nil {
				for _, a := range args {
					if a.Ordinal == n {
						if lim, ok := rbNum(a.Value); ok && int(lim) < len(matched) {
							matched = matched[:int(lim)]
						}
					}
				}
			}
		}
	}
	rows := make([][]driver.Value, len(matched))
	for i, row := range matched {
		vals := make([]driver.Value, len(cols))
		for j, c := range cols {
			vals[j] = row[c]
		}
		rows[i] = vals
	}
	return rows, cols, nil
}

func (s *rbState) execUpdate(query string, args []driver.NamedValue) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !strings.Contains(query, `"emails"`) {
		return 0, fmt.Errorf("rbState: unsupported exec: %s", query)
	}
	// Only the ClaimForRetry shape reaches this path: SET status/retry_count/
	// error_message WHERE id = $n AND status = 'failed'.
	var setID uint
	if i := strings.Index(query, " where "); i >= 0 {
		for _, cond := range strings.Split(query[i+len(" where "):], " and ") {
			if !strings.Contains(cond, " = ") {
				continue
			}
			parts := strings.SplitN(cond, " = ", 2)
			if strings.EqualFold(strings.Trim(strings.TrimSpace(parts[0]), `"`), "id") {
				raw := strings.TrimSpace(parts[1])
				if n, err := strconv.Atoi(strings.TrimPrefix(raw, "$")); err == nil {
					for _, a := range args {
						if a.Ordinal == n {
							setID = uint(a.Value.(int64))
						}
					}
				}
			}
		}
	}
	var affected int64
	for _, row := range s.emails {
		if uint(row["id"].(int64)) != setID {
			continue
		}
		if row["status"] != models.EmailStatusFailed {
			continue
		}
		row["status"] = models.EmailStatusQueued
		row["retry_count"] = row["retry_count"].(int64) + 1
		row["error_message"] = ""
		affected++
		s.claims = append(s.claims, setID)
	}
	return affected, nil
}

type rbConn struct{ s *rbState }

func (c *rbConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *rbConn) Close() error                        { return nil }
func (c *rbConn) Begin() (driver.Tx, error)           { return rbTx{}, nil }

func (c *rbConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	table := rbTableOf(query)
	if table == "" {
		return nil, fmt.Errorf("rbState: cannot route query: %s", query)
	}
	rows, cols, err := c.s.selectRows(table, strings.ToLower(strings.Join(strings.Fields(query), " ")), args)
	if err != nil {
		return nil, err
	}
	return &rbRows{cols: cols, rows: rows}, nil
}

func (c *rbConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	n, err := c.s.execUpdate(strings.ToLower(strings.Join(strings.Fields(query), " ")), args)
	return rbResult{rows: n}, err
}

func rbTableOf(query string) string {
	lower := strings.ToLower(query)
	for _, name := range []string{"smtp_servers", "emails"} {
		if strings.Contains(lower, `from "`+name+`"`) || strings.Contains(lower, `update "`+name+`"`) {
			return name
		}
	}
	return ""
}

type rbTx struct{}

func (rbTx) Commit() error   { return nil }
func (rbTx) Rollback() error { return nil }

type rbResult struct{ rows int64 }

func (r rbResult) LastInsertId() (int64, error) { return 0, nil }
func (r rbResult) RowsAffected() (int64, error) { return r.rows, nil }

type rbRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *rbRows) Columns() []string { return r.cols }
func (r *rbRows) Close() error      { return nil }
func (r *rbRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type rbConnector struct{ state *rbState }

func (c rbConnector) Connect(context.Context) (driver.Conn, error) { return &rbConn{s: c.state}, nil }
func (c rbConnector) Driver() driver.Driver                        { return rbDrv{} }

type rbDrv struct{}

func (rbDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func rbGorm(t *testing.T, state *rbState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(rbConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open retry fake gorm: %v", err)
	}
	return gdb
}

// rbEnqueuer records re-enqueued email ids.
type rbEnqueuer struct {
	mu    sync.Mutex
	ids   []uint
	first chan struct{}
	once  sync.Once
}

func (e *rbEnqueuer) EnqueueEmailSend(emailID uint, _ string) error {
	e.mu.Lock()
	e.ids = append(e.ids, emailID)
	e.mu.Unlock()
	e.once.Do(func() { close(e.first) })
	return nil
}

func (e *rbEnqueuer) EnqueueEmailSendAt(uint, string, time.Time) error { return nil }

func (e *rbEnqueuer) enqueued() []uint {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]uint(nil), e.ids...)
}

func newRetryWorker(t *testing.T, state *rbState, eq *rbEnqueuer, batchLimit int) *retry.Worker {
	t.Helper()
	w := retry.NewWorker(
		repositories.NewEmailRepository(rbGorm(t, state)),
		repositories.NewSMTPRepository(rbGorm(t, state)),
		nil,
		time.Minute,
	)
	w.SetEnqueuer(eq)
	w.SetBatchLimit(batchLimit)
	w.SetJitter(func() time.Duration { return 0 })
	return w
}

// TestRetrySweepBoundedBatch pins the R-27 acceptance: a backlog larger than
// the batch cap re-enqueues in bounded, oldest-first batches across sweeps
// instead of all at once.
func TestRetrySweepBoundedBatch(t *testing.T) {
	state := newRBState()
	var wsID uint = 7
	state.seedServer(&wsID, 3)
	for i := 1; i <= 5; i++ {
		state.seedFailedEmail(uint(i), &wsID, 6-i) // id 1 is oldest
	}
	eq := &rbEnqueuer{first: make(chan struct{})}
	w := newRetryWorker(t, state, eq, 2)

	w.ProcessRetries()
	got := eq.enqueued()
	if len(got) != 2 {
		t.Fatalf("first sweep enqueued %d emails (%v), want exactly 2", len(got), got)
	}
	if got[0] != 1 || got[1] != 2 {
		t.Fatalf("first sweep enqueued %v, want the two oldest [1 2]", got)
	}

	w.ProcessRetries()
	got = eq.enqueued()
	if len(got) != 4 || got[2] != 3 || got[3] != 4 {
		t.Fatalf("second sweep enqueued %v, want [1 2 3 4]", got)
	}

	w.ProcessRetries()
	got = eq.enqueued()
	if len(got) != 5 || got[4] != 5 {
		t.Fatalf("third sweep enqueued %v, want [1 2 3 4 5]", got)
	}

	w.ProcessRetries()
	if got := eq.enqueued(); len(got) != 5 {
		t.Fatalf("fourth sweep enqueued %v, want no new claims (backlog drained)", got)
	}
	if left := state.failedIDs(); len(left) != 0 {
		t.Fatalf("failed backlog = %v, want empty", left)
	}
	for id := uint(1); id <= 5; id++ {
		if n := state.retryCount(id); n != 1 {
			t.Fatalf("email %d retry_count = %d, want 1", id, n)
		}
	}
}

// TestRetrySweepZeroLimitUnbounded pins the limit<=0 escape hatch: without an
// explicit bound the sweep loads the whole backlog (legacy behavior, kept for
// direct repo callers).
func TestRetrySweepZeroLimitUnbounded(t *testing.T) {
	state := newRBState()
	var wsID uint = 7
	state.seedServer(&wsID, 3)
	for i := 1; i <= 6; i++ {
		state.seedFailedEmail(uint(i), &wsID, 7-i)
	}
	eq := &rbEnqueuer{first: make(chan struct{})}
	w := retry.NewWorker(
		repositories.NewEmailRepository(rbGorm(t, state)),
		repositories.NewSMTPRepository(rbGorm(t, state)),
		nil,
		time.Minute,
	)
	w.SetEnqueuer(eq)
	w.SetJitter(func() time.Duration { return 0 })
	w.SetBatchLimit(0) // the documented unbounded escape hatch

	w.ProcessRetries()
	if got := eq.enqueued(); len(got) != 6 {
		t.Fatalf("unbounded sweep enqueued %d emails, want all 6", len(got))
	}
}

// TestRetrySweepExhaustedRetriesSkipped pins the max_retries gate: emails whose
// retry_count has reached the server's max never re-enter the queue.
func TestRetrySweepExhaustedRetriesSkipped(t *testing.T) {
	state := newRBState()
	var wsID uint = 7
	state.seedServer(&wsID, 1) // maxRetries = 1
	state.seedFailedEmail(1, &wsID, 2)
	state.seedFailedEmail(2, &wsID, 3)
	// email 1 already used its one retry.
	state.mu.Lock()
	for _, row := range state.emails {
		if row["id"].(int64) == 1 {
			row["retry_count"] = int64(1)
		}
	}
	state.mu.Unlock()

	eq := &rbEnqueuer{first: make(chan struct{})}
	w := newRetryWorker(t, state, eq, 10)
	w.ProcessRetries()

	got := eq.enqueued()
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("sweep enqueued %v, want only email 2 (email 1 exhausted its retries)", got)
	}
}

// TestRetrySweepJitteredLoop runs the real loop briefly: the ticker fires the
// sweep (possibly after the configured jitter), and Stop ends it cleanly.
func TestRetrySweepJitteredLoop(t *testing.T) {
	state := newRBState()
	var wsID uint = 7
	state.seedServer(&wsID, 3)
	state.seedFailedEmail(1, &wsID, 1)
	state.seedFailedEmail(2, &wsID, 2)

	eq := &rbEnqueuer{first: make(chan struct{})}
	// Short interval so the ticker (not the test) drives the sweep.
	w := retry.NewWorker(
		repositories.NewEmailRepository(rbGorm(t, state)),
		repositories.NewSMTPRepository(rbGorm(t, state)),
		nil,
		20*time.Millisecond,
	)
	w.SetEnqueuer(eq)
	w.SetBatchLimit(5)
	jittered := false
	w.SetJitter(func() time.Duration {
		jittered = true
		return time.Millisecond
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	select {
	case <-eq.first:
	case <-time.After(2 * time.Second):
		t.Fatal("retry loop did not sweep within 2s")
	}

	w.Stop()
	cancel()
	if !jittered {
		t.Fatal("sweep ran without invoking the jitter function")
	}
}

// TestRetryDefaultJitterBounds pins the default jitter to [0, interval/10).
func TestRetryDefaultJitterBounds(t *testing.T) {
	interval := time.Minute
	w := retry.NewWorker(nil, nil, nil, interval)
	for i := 0; i < 200; i++ {
		d := w.Jitter()
		if d < 0 || d >= interval/10 {
			t.Fatalf("jitter %s outside [0, %s)", d, interval/10)
		}
	}
}
