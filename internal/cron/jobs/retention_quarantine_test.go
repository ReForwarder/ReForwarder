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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OB-2 quarantine retention (ADR-002 §3, ADR-008 lifecycle): the retention
// job purges quarantine records past inbound_quarantine_retention_days
// (default 30, absent row uses the fallback, 0 keeps forever), and a message
// still held pending is purged together with its record — retention expiry on
// a hold is an automatic permanent deletion. Same fake-driver arrangement as
// retention_security_test.go, self-contained so the two files stay
// independent.

// ---------- fake driver ----------

var rqColumns = map[string][]string{
	"settings":           {"id", "key", "value", "type", "created_at", "updated_at"},
	"inbound_quarantine": {"id", "user_id", "workspace_id", "domain_id", "inbound_email_id", "route_id", "reason", "score", "status", "received_at", "reviewed_by", "reviewed_at", "created_at", "updated_at"},
	"inbound_emails":     {"id", "uuid", "user_id", "workspace_id", "domain_id", "sender", "recipients", "size", "status", "source", "received_at", "created_at"},
}

type rqTable struct {
	columns []string
	rows    [][]driver.Value
}

type rqState struct {
	mu     sync.Mutex
	tables map[string]*rqTable
	seq    int64
}

func newRqState() *rqState {
	tables := make(map[string]*rqTable, len(rqColumns))
	for name, cols := range rqColumns {
		tables[name] = &rqTable{columns: cols}
	}
	return &rqState{tables: tables, seq: 100}
}

func (s *rqState) colIndex(table, col string) int {
	for i, c := range rqColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

func (s *rqState) rowsOf(table string) []map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, 0, len(s.tables[table].rows))
	for _, row := range s.tables[table].rows {
		m := map[string]driver.Value{}
		for i, c := range rqColumns[table] {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

func rqSeed(t *testing.T, s *rqState, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := rqColumns[table]
	row := make([]driver.Value, len(spec))
	for i := range spec {
		row[i] = nil
	}
	for col, v := range vals {
		idx := s.colIndex(table, col)
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, col)
		}
		row[idx] = rqVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func rqVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case bool:
		return t
	case string:
		return t
	case time.Time:
		return t
	default:
		panic(fmt.Sprintf("rqVal: unsupported type %T", v))
	}
}

func rqGorm(t *testing.T, state *rqState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(rqConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open quarantine-retention fake gorm: %v", err)
	}
	return gdb
}

// newRetentionQuarantineJob builds the retention job with only the quarantine
// family wired; every other family's window is seeded to 0 so the fake only
// answers the settings, quarantine and inbound-email queries.
func newRetentionQuarantineJob(t *testing.T, state *rqState) *RetentionCleanupJob {
	t.Helper()
	gdb := rqGorm(t, state)
	sp := settings.NewProvider(repositories.NewSettingRepository(gdb))
	job := NewRetentionCleanupJob(nil, nil, nil, nil, sp)
	job.SetInboundEmailRepo(repositories.NewInboundEmailRepository(gdb))
	job.SetInboundQuarantineRepo(repositories.NewInboundQuarantineRepository(gdb))
	return job
}

// seedQuarantineScenario seeds the settings windows plus quarantine records:
// an expired pending hold (with its live message) and a recent pending hold.
// window is the inbound_quarantine_retention_days value; "" leaves the row
// absent so the provider default (30) applies.
func seedQuarantineScenario(t *testing.T, s *rqState, window string, extraQuarantine ...map[string]any) {
	t.Helper()
	now := time.Now().UTC()
	for _, kv := range [][2]string{
		{"retention_days", "0"},
		{"audit_log_retention_days", "0"},
		{"webhook_delivery_retention_days", "0"},
		{"tracking_event_retention_days", "0"},
		{"email_events_retention_days", "0"},
		{"message_retention_days", "0"},
		{"message_spam_retention_days", "0"},
		{"security_events_retention_days", "0"},
		{"inbound_quarantine_retention_days", window},
	} {
		if kv[1] == "" {
			continue // row absent → provider default applies
		}
		rqSeed(t, s, "settings", map[string]any{
			"id": uint(len(s.rowsOf("settings")) + 1), "key": kv[0], "value": kv[1],
			"type": "int", "created_at": now, "updated_at": now,
		})
	}

	// Inbound messages: id 1 lives under the expired hold, id 2 under the
	// recent hold.
	rqSeed(t, s, "inbound_emails", map[string]any{
		"id": uint(1), "uuid": "inb-old", "user_id": uint(7), "workspace_id": uint(3),
		"domain_id": uint(1), "sender": "alice@example.test", "size": int64(120),
		"status": "quarantined", "source": "smtp", "received_at": now.Add(-40 * 24 * time.Hour),
		"created_at": now,
	})
	rqSeed(t, s, "inbound_emails", map[string]any{
		"id": uint(2), "uuid": "inb-recent", "user_id": uint(7), "workspace_id": uint(3),
		"domain_id": uint(1), "sender": "bob@example.test", "size": int64(80),
		"status": "quarantined", "source": "smtp", "received_at": now.Add(-2 * 24 * time.Hour),
		"created_at": now,
	})

	rqSeed(t, s, "inbound_quarantine", map[string]any{
		"id": uint(11), "user_id": uint(7), "workspace_id": uint(3), "domain_id": uint(1),
		"inbound_email_id": uint(1), "reason": "matched quarantine route",
		"status": "pending", "received_at": now.Add(-40 * 24 * time.Hour), "created_at": now, "updated_at": now,
	})
	rqSeed(t, s, "inbound_quarantine", map[string]any{
		"id": uint(12), "user_id": uint(7), "workspace_id": uint(3), "domain_id": uint(1),
		"inbound_email_id": uint(2), "reason": "matched quarantine route",
		"status": "pending", "received_at": now.Add(-2 * 24 * time.Hour), "created_at": now, "updated_at": now,
	})
	for _, extra := range extraQuarantine {
		rqSeed(t, s, "inbound_quarantine", extra)
	}
}

// ---------- tests ----------

// TestRetentionPurgesQuarantinePastDefaultWindow: with the settings row
// absent the provider's 30-day default applies — the expired hold's message
// and record are purged, the recent hold survives intact.
func TestRetentionPurgesQuarantinePastDefaultWindow(t *testing.T) {
	state := newRqState()
	seedQuarantineScenario(t, state, "")
	job := newRetentionQuarantineJob(t, state)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("retention run: %v", err)
	}

	records := state.rowsOf("inbound_quarantine")
	if len(records) != 1 || records[0]["id"].(int64) != 12 {
		t.Fatalf("surviving records = %+v, want only id 12 (the recent hold)", records)
	}
	messages := state.rowsOf("inbound_emails")
	if len(messages) != 1 || messages[0]["id"].(int64) != 2 {
		t.Fatalf("surviving messages = %+v, want only id 2 (the recent hold's message)", messages)
	}
}

// TestRetentionQuarantineZeroKeepsForever pins the 0 semantics: nothing is
// deleted from either table.
func TestRetentionQuarantineZeroKeepsForever(t *testing.T) {
	state := newRqState()
	seedQuarantineScenario(t, state, "0")
	job := newRetentionQuarantineJob(t, state)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("retention run: %v", err)
	}

	if rows := state.rowsOf("inbound_quarantine"); len(rows) != 2 {
		t.Errorf("0 = keep forever: both records must survive, got %d", len(rows))
	}
	if rows := state.rowsOf("inbound_emails"); len(rows) != 2 {
		t.Errorf("0 = keep forever: both messages must survive, got %d", len(rows))
	}
}

// TestRetentionQuarantineBoundary: at day granularity the window splits
// exactly — 29 days in survives, 31 days out is purged together with the
// message it still holds. (The seed/run clock skew is nanoseconds; a whole
// day of margin keeps the comparison deterministic.)
func TestRetentionQuarantineBoundary(t *testing.T) {
	state := newRqState()
	seedQuarantineScenario(t, state, "30",
		map[string]any{
			// 29 days old: inside the window.
			"id": uint(13), "user_id": uint(7), "workspace_id": uint(3), "domain_id": uint(1),
			"inbound_email_id": uint(3), "reason": "matched quarantine route",
			"status": "pending", "received_at": time.Now().UTC().Add(-29 * 24 * time.Hour),
			"created_at": time.Now().UTC(), "updated_at": time.Now().UTC(),
		},
		map[string]any{
			// 31 days old: one day past the window.
			"id": uint(14), "user_id": uint(7), "workspace_id": uint(3), "domain_id": uint(1),
			"inbound_email_id": uint(4), "reason": "matched quarantine route",
			"status": "pending", "received_at": time.Now().UTC().Add(-31 * 24 * time.Hour),
			"created_at": time.Now().UTC(), "updated_at": time.Now().UTC(),
		},
	)
	rqSeed(t, state, "inbound_emails", map[string]any{
		"id": uint(3), "uuid": "inb-edge", "user_id": uint(7), "workspace_id": uint(3),
		"domain_id": uint(1), "sender": "edge@example.test", "size": int64(10),
		"status": "quarantined", "source": "smtp", "received_at": time.Now().UTC().Add(-29 * 24 * time.Hour),
		"created_at": time.Now().UTC(),
	})
	rqSeed(t, state, "inbound_emails", map[string]any{
		"id": uint(4), "uuid": "inb-past", "user_id": uint(7), "workspace_id": uint(3),
		"domain_id": uint(1), "sender": "past@example.test", "size": int64(10),
		"status": "quarantined", "source": "smtp", "received_at": time.Now().UTC().Add(-31 * 24 * time.Hour),
		"created_at": time.Now().UTC(),
	})
	job := newRetentionQuarantineJob(t, state)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("retention run: %v", err)
	}

	records := state.rowsOf("inbound_quarantine")
	survivors := map[int64]bool{}
	for _, r := range records {
		survivors[r["id"].(int64)] = true
	}
	if !survivors[13] || !survivors[12] {
		t.Errorf("records inside the window (12, 13) must survive, survivors = %v", survivors)
	}
	if survivors[14] || survivors[11] {
		t.Errorf("records past the window (11, 14) must be purged, survivors = %v", survivors)
	}
	messages := state.rowsOf("inbound_emails")
	msgSurvivors := map[int64]bool{}
	for _, m := range messages {
		msgSurvivors[m["id"].(int64)] = true
	}
	if !msgSurvivors[2] || !msgSurvivors[3] {
		t.Errorf("messages inside the window (2, 3) must survive, got %v", msgSurvivors)
	}
	if msgSurvivors[1] || msgSurvivors[4] {
		t.Errorf("expired holds' messages (1, 4) must be purged with their records, got %v", msgSurvivors)
	}
}

// TestRetentionQuarantineCutoffIsExclusive pins the cutoff semantics at the
// exact boundary: a record received exactly AT the cutoff is not "older than
// it" — neither the held-message lookup nor the record delete may match it.
func TestRetentionQuarantineCutoffIsExclusive(t *testing.T) {
	state := newRqState()
	gdb := rqGorm(t, state)
	received := time.Now().UTC().Add(-30 * 24 * time.Hour)
	rqSeed(t, state, "inbound_quarantine", map[string]any{
		"id": uint(21), "user_id": uint(7), "workspace_id": uint(3), "domain_id": uint(1),
		"inbound_email_id": uint(9), "reason": "matched quarantine route",
		"status": "pending", "received_at": received, "created_at": received, "updated_at": received,
	})
	repo := repositories.NewInboundQuarantineRepository(gdb)

	ids, err := repo.ExpiredPendingInboundEmailIDs(received, 100)
	if err != nil {
		t.Fatalf("ExpiredPendingInboundEmailIDs (at boundary): %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("a record received exactly at the cutoff is not expired, got ids %v", ids)
	}
	affected, err := repo.DeleteOlderThan(received)
	if err != nil || affected != 0 {
		t.Fatalf("DeleteOlderThan (at boundary) = %d affected, err %v, want 0", affected, err)
	}
	if rows := state.rowsOf("inbound_quarantine"); len(rows) != 1 {
		t.Fatalf("boundary record must survive, got %d rows", len(rows))
	}

	// One nanosecond past the boundary: the same record is expired.
	ids, err = repo.ExpiredPendingInboundEmailIDs(received.Add(time.Nanosecond), 100)
	if err != nil || len(ids) != 1 || ids[0] != 9 {
		t.Fatalf("ExpiredPendingInboundEmailIDs (boundary+1ns) = %v, err %v, want [9]", ids, err)
	}
	affected, err = repo.DeleteOlderThan(received.Add(time.Nanosecond))
	if err != nil || affected != 1 {
		t.Fatalf("DeleteOlderThan (boundary+1ns) = %d affected, err %v, want 1", affected, err)
	}
	if rows := state.rowsOf("inbound_quarantine"); len(rows) != 0 {
		t.Fatalf("record past the boundary must be gone, got %d rows", len(rows))
	}
}

// TestRetentionQuarantineReleasedRecordDoesNotDeleteLiveMessage: only a
// still-pending hold drags its message into the purge — a released record
// past the window loses its review tombstone, but the message it released
// stays in the archive.
func TestRetentionQuarantineReleasedRecordDoesNotDeleteLiveMessage(t *testing.T) {
	state := newRqState()
	seedQuarantineScenario(t, state, "30")
	// Make record 11 a released tombstone (its message 1 was already released
	// back into the archive) — the message must survive the purge.
	for _, row := range state.tables["inbound_quarantine"].rows {
		if row[state.colIndex("inbound_quarantine", "id")] == int64(11) {
			row[state.colIndex("inbound_quarantine", "status")] = "released"
			row[state.colIndex("inbound_quarantine", "reviewed_by")] = int64(9)
		}
	}
	// Message 1 was released: it is a regular received record now.
	for _, row := range state.tables["inbound_emails"].rows {
		if row[state.colIndex("inbound_emails", "id")] == int64(1) {
			row[state.colIndex("inbound_emails", "status")] = "received"
		}
	}
	job := newRetentionQuarantineJob(t, state)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("retention run: %v", err)
	}

	if rows := state.rowsOf("inbound_quarantine"); len(rows) != 1 || rows[0]["id"].(int64) != 12 {
		t.Fatalf("surviving records = %+v, want only 12 (the recent hold)", state.rowsOf("inbound_quarantine"))
	}
	messages := state.rowsOf("inbound_emails")
	if len(messages) != 2 {
		t.Fatalf("released record's message must survive, got %d messages: %+v", len(messages), messages)
	}
}

// TestRetentionQuarantineDeleteIsParameterized guards the SQL-injection
// contract: the cutoff rides as a bindvar, never as SQL concatenation.
func TestRetentionQuarantineDeleteIsParameterized(t *testing.T) {
	state := newRqState()
	seedQuarantineScenario(t, state, "30")
	job := newRetentionQuarantineJob(t, state)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("retention run: %v", err)
	}
	// The fake driver only accepts bindvar-parameterized predicates (see
	// rqEvalWhere: a non-bindvar value fails the query), so reaching this
	// point without an error proves the DELETEs carried `received_at < $n`.
	if rows := state.rowsOf("inbound_quarantine"); len(rows) != 1 {
		t.Fatalf("parameterized delete did not run correctly, survivors = %+v", rows)
	}
}

// ---------- fake driver plumbing ----------

type rqConnector struct{ state *rqState }

func (c rqConnector) Connect(context.Context) (driver.Conn, error) { return &rqConn{s: c.state}, nil }
func (c rqConnector) Driver() driver.Driver                        { return rqDrv{} }

type rqDrv struct{}

func (rqDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type rqConn struct{ s *rqState }

func (c *rqConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *rqConn) Close() error                        { return nil }
func (c *rqConn) Begin() (driver.Tx, error)           { return rqTx{}, nil }

type rqTx struct{}

func (rqTx) Commit() error   { return nil }
func (rqTx) Rollback() error { return nil }

type rqResult struct{ rows int64 }

func (r rqResult) LastInsertId() (int64, error) { return 0, nil }
func (r rqResult) RowsAffected() (int64, error) { return r.rows, nil }

type rqRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *rqRows) Columns() []string { return r.cols }
func (r *rqRows) Close() error      { return nil }
func (r *rqRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func (c *rqConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := rqTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("rqState: cannot route query: %s", query)
	}
	matched, err := rqEvalWhere(c.s, table, lower, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(lower, "count(*)") {
		return &rqRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	matched = rqLimit(lower, args, matched)
	// Honor the SELECT list: gorm's Pluck into a basic slice requires the
	// result set to carry exactly the named column.
	cols := rqSelectCols(lower, table)
	idxs := make([]int, 0, len(cols))
	for _, col := range cols {
		idxs = append(idxs, c.s.colIndex(table, col))
	}
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		out := make([]driver.Value, len(idxs))
		for j, idx := range idxs {
			out[j] = r[idx]
		}
		rows[i] = out
	}
	return &rqRows{cols: cols, rows: rows}, nil
}

// rqSelectCols extracts the SELECT list's columns ("*" → the table's full
// column set).
func rqSelectCols(lower, table string) []string {
	i := strings.Index(lower, "select ")
	j := strings.Index(lower, " from ")
	if i < 0 || j < 0 || j < i {
		return rqColumns[table]
	}
	list := strings.TrimSpace(lower[i+len("select ") : j])
	if list == "*" {
		return rqColumns[table]
	}
	var cols []string
	for _, c := range strings.Split(list, ",") {
		c = strings.Trim(strings.TrimSpace(c), `"`)
		if c == "" {
			continue
		}
		found := false
		for _, known := range rqColumns[table] {
			if known == c {
				found = true
				break
			}
		}
		if found {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return rqColumns[table]
	}
	return cols
}

func (c *rqConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := rqTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("rqState: unsupported exec: %s", query)
	}
	if !strings.Contains(lower, "delete from") {
		return nil, fmt.Errorf("rqState: unsupported exec: %s", query)
	}
	matched, err := rqEvalWhere(c.s, table, lower, args)
	if err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept [][]driver.Value
	for _, row := range s.tables[table].rows {
		removed := false
		for _, m := range matched {
			if &row[0] == &m[0] {
				removed = true
				break
			}
		}
		if !removed {
			kept = append(kept, row)
		}
	}
	s.tables[table].rows = kept
	return rqResult{rows: int64(len(matched))}, nil
}

func rqTableOf(lower string) string {
	for _, name := range []string{"inbound_quarantine", "inbound_emails", "settings"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`} {
			if strings.Contains(lower, pat) {
				return name
			}
		}
	}
	return ""
}

// rqEvalWhere evaluates `col = $n` / `col < $n` conjunctions (bindvar values
// only — a literal is a query bug here, which is what the parameterization
// test leans on).
func rqEvalWhere(s *rqState, table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ""
	if i := strings.Index(compact, " where "); i >= 0 {
		where = compact[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit ", " returning "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}
	where = strings.NewReplacer("(", " ", ")", " ").Replace(where)

	var preds []func([]driver.Value) bool
	for _, cond := range strings.Split(where, " and ") {
		cond = strings.TrimSpace(cond)
		if cond == "" {
			continue
		}
		op := " = "
		if strings.Contains(cond, " < ") {
			op = " < "
		}
		eq := strings.Index(cond, op)
		if eq < 0 {
			return nil, fmt.Errorf("rqState: unsupported condition %q", cond)
		}
		idx := s.colIndex(table, rqColumnOf(cond[:eq]))
		if idx < 0 {
			return nil, fmt.Errorf("rqState: unknown column in %q", cond)
		}
		val := strings.TrimSpace(cond[eq+len(op):])
		if !strings.HasPrefix(val, "$") {
			return nil, fmt.Errorf("rqState: condition %q must carry a bindvar", cond)
		}
		n, err := strconv.Atoi(strings.TrimPrefix(val, "$"))
		if err != nil {
			return nil, fmt.Errorf("rqState: bad bindvar in %q", cond)
		}
		want := rqArg(args, n)
		if op == " < " {
			wantT, ok := want.(time.Time)
			if !ok {
				return nil, fmt.Errorf("rqState: < comparison needs a time arg")
			}
			preds = append(preds, func(row []driver.Value) bool {
				rowT, ok := row[idx].(time.Time)
				return ok && rowT.Before(wantT)
			})
			continue
		}
		preds = append(preds, func(row []driver.Value) bool { return rqEqual(row[idx], want) })
	}

	var out [][]driver.Value
	for _, row := range s.tables[table].rows {
		ok := true
		for _, p := range preds {
			if !p(row) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func rqLimit(lower string, args []driver.NamedValue, rows [][]driver.Value) [][]driver.Value {
	m := strings.Index(lower, " limit ")
	if m < 0 {
		return rows
	}
	token := strings.TrimSpace(lower[m+len(" limit "):])
	if strings.HasPrefix(token, "$") {
		if n, err := rqBindToken(token); err == nil {
			if v, ok := rqArg(args, n).(int64); ok && int(v) < len(rows) {
				rows = rows[:int(v)]
			}
		}
		return rows
	}
	if n, err := strconv.Atoi(token); err == nil && n < len(rows) {
		rows = rows[:n]
	}
	return rows
}

func rqColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func rqArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("rqState: no arg with ordinal %d", n))
}

func rqEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if ta, ok := a.(time.Time); ok {
		tb, ok2 := b.(time.Time)
		return ok2 && ta.Equal(tb)
	}
	if sa, ok := a.(string); ok {
		sb, ok2 := b.(string)
		if !ok2 {
			sb = fmt.Sprint(b)
		}
		return sa == sb
	}
	return a == b
}

func rqBindToken(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$") {
		return 0, fmt.Errorf("rqState: expected a bindvar, got %q", s)
	}
	end := 1
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return strconv.Atoi(s[1:end])
}
