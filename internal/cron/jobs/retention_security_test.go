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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.
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

// GAP-13: SecurityEventRepository.DeleteOlderThan had no caller; the
// retention job is now that caller. These tests run the real job over a fake
// database/sql driver (same arrangement as queue_reconcile_test.go) and pin:
// security events are purged past the security_events_retention_days window
// (default 90, absent row uses the fallback), 0 keeps them forever, and the
// pre-existing retention families (audit events) still run.

var rseColumns = map[string][]string{
	"settings":        {"id", "key", "value", "type", "created_at", "updated_at"},
	"security_events": {"id", "source_ip", "event_type", "severity", "destination_port", "protocol", "evidence", "risk_score", "action_taken", "created_at"},
	"events":          {"id", "category", "type", "workspace_id", "actor_id", "actor_name", "client_ip", "message", "metadata", "outcome", "target_type", "target_id", "created_at"},
}

type rseTable struct {
	columns []string
	rows    [][]driver.Value
}

type rseState struct {
	mu     sync.Mutex
	tables map[string]*rseTable
	seq    int64
	calls  []string
}

func newRseState() *rseState {
	tables := make(map[string]*rseTable, len(rseColumns))
	for name, cols := range rseColumns {
		tables[name] = &rseTable{columns: cols}
	}
	return &rseState{tables: tables, seq: 100}
}

func (s *rseState) colIndex(table, col string) int {
	for i, c := range rseColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

func rseSeed(t *testing.T, s *rseState, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := rseColumns[table]
	row := make([]driver.Value, len(spec))
	for i := range spec {
		row[i] = nil
	}
	for col, v := range vals {
		idx := s.colIndex(table, col)
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, col)
		}
		row[idx] = rseVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func rseVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case bool:
		return t
	case string:
		return t
	case time.Time:
		return t
	case *int:
		if t == nil {
			return nil
		}
		return int64(*t)
	default:
		panic(fmt.Sprintf("rseVal: unsupported type %T", v))
	}
}

func (s *rseState) rowsOf(table string) []map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, 0, len(s.tables[table].rows))
	for _, row := range s.tables[table].rows {
		m := map[string]driver.Value{}
		for i, c := range rseColumns[table] {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

// deleteCalls lists the DELETE statements the fake has seen.
func (s *rseState) deleteCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, call := range s.calls {
		if strings.Contains(strings.ToLower(call), "delete from") {
			out = append(out, call)
		}
	}
	return out
}

func rseGorm(t *testing.T, state *rseState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(rseConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open retention-security fake gorm: %v", err)
	}
	return gdb
}

// newRetentionSecurityJob builds the retention job over the fake with only
// the security-event and audit-event families wired; every other family is
// left nil and (with its retention window seeded to 0) skipped, so the fake
// only has to answer the queries those two families issue.
func newRetentionSecurityJob(t *testing.T, state *rseState) *RetentionCleanupJob {
	t.Helper()
	gdb := rseGorm(t, state)
	sp := settings.NewProvider(repositories.NewSettingRepository(gdb))
	job := NewRetentionCleanupJob(
		nil,
		repositories.NewEventRepository(gdb),
		nil,
		nil,
		sp,
	)
	job.SetSecurityEventRepo(repositories.NewSecurityEventRepository(gdb))
	return job
}

// seedSecurityScenario seeds the settings windows and two security events:
// one 100 days old, one 10 days old.
func seedSecurityScenario(t *testing.T, s *rseState, securityWindow string) {
	t.Helper()
	now := time.Now().UTC()
	// All other families keep forever so the fake is only exercised on the
	// security + audit paths under test.
	for _, kv := range [][2]string{
		{"retention_days", "0"},
		{"audit_log_retention_days", "90"},
		{"webhook_delivery_retention_days", "0"},
		{"tracking_event_retention_days", "0"},
		{"email_events_retention_days", "0"},
		{"message_retention_days", "0"},
		{"message_spam_retention_days", "0"},
		{"security_events_retention_days", securityWindow},
	} {
		if kv[1] == "" {
			continue // leave the row absent (provider default applies)
		}
		rseSeed(t, s, "settings", map[string]any{
			"id": uint(len(s.rowsOf("settings")) + 1), "key": kv[0], "value": kv[1],
			"type": "int", "created_at": now, "updated_at": now,
		})
	}
	rseSeed(t, s, "security_events", map[string]any{
		"id": uint(1), "source_ip": "203.0.113.9", "event_type": "sig_auth_failures",
		"severity": "warning", "protocol": "smtp", "evidence": "{}",
		"risk_score": 40, "action_taken": "recorded",
		"created_at": now.Add(-100 * 24 * time.Hour),
	})
	rseSeed(t, s, "security_events", map[string]any{
		"id": uint(2), "source_ip": "203.0.113.10", "event_type": "sig_auth_failures",
		"severity": "info", "protocol": "smtp", "evidence": "{}",
		"risk_score": 5, "action_taken": "recorded",
		"created_at": now.Add(-10 * 24 * time.Hour),
	})
	// One audit event past the audit window (90d) — pins that the pre-existing
	// families still run alongside the new one.
	rseSeed(t, s, "events", map[string]any{
		"id": uint(3), "category": "audit", "type": "settings.updated", "actor_id": uint(1),
		"actor_name": "admin@example.com", "message": "old audit row",
		"created_at": now.Add(-120 * 24 * time.Hour),
	})
}

// ---------- tests ----------

// TestRetentionPurgesSecurityEventsPastDefaultWindow: with the settings row
// absent the provider's 90-day default applies and only events older than it
// are removed — the recent one survives.
func TestRetentionPurgesSecurityEventsPastDefaultWindow(t *testing.T) {
	state := newRseState()
	seedSecurityScenario(t, state, "") // row absent → default 90
	job := newRetentionSecurityJob(t, state)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("retention run: %v", err)
	}

	rows := state.rowsOf("security_events")
	if len(rows) != 1 {
		t.Fatalf("expected 1 surviving security event, got %d", len(rows))
	}
	if rows[0]["id"].(int64) != 2 {
		t.Errorf("survivor = id %v, want 2 (the recent event)", rows[0]["id"])
	}

	// The audit-event family must still run (no regression of existing
	// retention behavior alongside the new family).
	events := state.rowsOf("events")
	if len(events) != 0 {
		t.Errorf("old audit event should have been purged, %d rows remain", len(events))
	}
}

// TestRetentionSecurityEventsZeroKeepsForever pins the 0 semantics: no DELETE
// is issued against security_events at all.
func TestRetentionSecurityEventsZeroKeepsForever(t *testing.T) {
	state := newRseState()
	seedSecurityScenario(t, state, "0")
	job := newRetentionSecurityJob(t, state)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("retention run: %v", err)
	}

	if rows := state.rowsOf("security_events"); len(rows) != 2 {
		t.Errorf("0 = keep forever: expected both events to survive, got %d", len(rows))
	}
	for _, call := range state.deleteCalls() {
		if strings.Contains(strings.ToLower(call), `"security_events"`) {
			t.Errorf("no DELETE may run for security_events when the window is 0, saw: %s", call)
		}
	}
}

// TestRetentionSecurityEventsCustomWindow: an explicit window (30) removes
// exactly the events older than it, leaving newer ones.
func TestRetentionSecurityEventsCustomWindow(t *testing.T) {
	state := newRseState()
	seedSecurityScenario(t, state, "30")
	job := newRetentionSecurityJob(t, state)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("retention run: %v", err)
	}

	rows := state.rowsOf("security_events")
	if len(rows) != 1 || rows[0]["id"].(int64) != 2 {
		t.Errorf("30-day window must keep only the recent event, got %+v", rows)
	}
}

// TestRetentionSecurityEventsDeleteIsParameterized guards the SQL-injection
// contract: the DELETE carries the cutoff as a bindvar, never concatenation.
func TestRetentionSecurityEventsDeleteIsParameterized(t *testing.T) {
	state := newRseState()
	seedSecurityScenario(t, state, "90")
	job := newRetentionSecurityJob(t, state)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("retention run: %v", err)
	}

	found := false
	for _, call := range state.deleteCalls() {
		lower := strings.ToLower(call)
		if strings.Contains(lower, `"security_events"`) {
			found = true
			if !strings.Contains(lower, "created_at <") || !strings.Contains(lower, "$") {
				t.Errorf("security events DELETE must parameterize the cutoff: %s", call)
			}
		}
	}
	if !found {
		t.Errorf("expected a security_events DELETE, calls: %v", state.deleteCalls())
	}
}

// ---------- fake driver ----------

type rseConnector struct{ state *rseState }

func (c rseConnector) Connect(context.Context) (driver.Conn, error) { return &rseConn{s: c.state}, nil }
func (c rseConnector) Driver() driver.Driver                        { return rseDrv{} }

type rseDrv struct{}

func (rseDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type rseConn struct{ s *rseState }

func (c *rseConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *rseConn) Close() error                        { return nil }
func (c *rseConn) Begin() (driver.Tx, error)           { return rseTx{}, nil }

type rseTx struct{}

func (rseTx) Commit() error   { return nil }
func (rseTx) Rollback() error { return nil }

type rseResult struct{ rows int64 }

func (r rseResult) LastInsertId() (int64, error) { return 0, nil }
func (r rseResult) RowsAffected() (int64, error) { return r.rows, nil }

type rseRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *rseRows) Columns() []string { return r.cols }
func (r *rseRows) Close() error      { return nil }
func (r *rseRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func (c *rseConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.record(query)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := rseTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("rseState: cannot route query: %s", query)
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(lower, "count(*)") {
		return &rseRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	matched = rseLimitOffset(lower, args, matched)
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &rseRows{cols: rseColumns[table], rows: rows}, nil
}

func (c *rseConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.record(query)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := rseTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("rseState: unsupported exec: %s", query)
	}
	if !strings.Contains(lower, "delete from") {
		return nil, fmt.Errorf("rseState: unsupported exec: %s", query)
	}
	matched, err := c.s.evalWhere(table, lower, args)
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
	return rseResult{rows: int64(len(matched))}, nil
}

func (c *rseConn) record(query string) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.calls = append(c.s.calls, query)
}

func rseTableOf(lower string) string {
	for _, name := range []string{"security_events", "settings", "events"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`} {
			if strings.Contains(lower, pat) {
				return name
			}
		}
	}
	return ""
}

func (s *rseState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
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
		if strings.HasSuffix(cond, " is null") {
			idx := s.colIndex(table, rseColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
			continue
		}
		op := " = "
		if strings.Contains(cond, " < ") {
			op = " < "
		}
		eq := strings.Index(cond, op)
		if eq < 0 {
			return nil, fmt.Errorf("rseState: unsupported condition %q", cond)
		}
		idx := s.colIndex(table, rseColumnOf(cond[:eq]))
		n, err := strconv.Atoi(strings.TrimSpace(cond[eq+len(op):])[1:])
		if err != nil {
			return nil, fmt.Errorf("rseState: bad bindvar in %q", cond)
		}
		want := rseArg(args, n)
		if op == " < " {
			wantT, ok := want.(time.Time)
			if !ok {
				return nil, fmt.Errorf("rseState: < comparison needs a time arg")
			}
			preds = append(preds, func(row []driver.Value) bool {
				rowT, ok := row[idx].(time.Time)
				return ok && rowT.Before(wantT)
			})
			continue
		}
		preds = append(preds, func(row []driver.Value) bool { return rseEqual(row[idx], want) })
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

func rseLimitOffset(lower string, args []driver.NamedValue, rows [][]driver.Value) [][]driver.Value {
	if m := strings.Index(lower, " limit "); m >= 0 {
		if n, err := rseBindToken(lower[m+len(" limit "):]); err == nil {
			if v, ok := rseArg(args, n).(int64); ok && int(v) < len(rows) {
				rows = rows[:int(v)]
			}
		}
	}
	return rows
}

func rseColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func rseArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("rseState: no arg with ordinal %d", n))
}

func rseEqual(a, b driver.Value) bool {
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

func rseBindToken(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$") {
		return 0, fmt.Errorf("rseState: expected a bindvar, got %q", s)
	}
	end := 1
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return strconv.Atoi(s[1:end])
}
