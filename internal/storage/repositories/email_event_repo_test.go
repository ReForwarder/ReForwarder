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

package repositories

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

	"github.com/goposta/posta/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ---------- statement capture on a DryRun session ----------

// capturedStmt is one SQL statement built by gorm with its bound variables.
type capturedStmt struct {
	sql  string
	vars []any
}

// sqlCapture collects the statements the repositories build on a DryRun
// session: queries and deletes are captured after gorm builds them, raw Execs
// before the (skipped) execution, so tests can assert the exact SQL a
// repository produces without a database.
type sqlCapture struct {
	mu      sync.Mutex
	queries []capturedStmt
	deletes []capturedStmt
	raws    []capturedStmt
}

func (c *sqlCapture) addQuery(sql string, vars []any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, capturedStmt{sql: sql, vars: append([]any(nil), vars...)})
}

func (c *sqlCapture) addDelete(sql string, vars []any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletes = append(c.deletes, capturedStmt{sql: sql, vars: append([]any(nil), vars...)})
}

func (c *sqlCapture) addRaw(sql string, vars []any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raws = append(c.raws, capturedStmt{sql: sql, vars: append([]any(nil), vars...)})
}

func (c *sqlCapture) lastQuery() capturedStmt {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queries) == 0 {
		return capturedStmt{}
	}
	return c.queries[len(c.queries)-1]
}

// dryCapture builds a DryRun postgres-dialect gorm whose query, delete and raw
// statements are captured.
func dryCapture(t *testing.T) (*gorm.DB, *sqlCapture) {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(nopConnector{}),
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open dry-run gorm: %v", err)
	}
	c := &sqlCapture{}
	if err := gdb.Callback().Query().After("gorm:query").Register("repo-test:capture-query", func(tx *gorm.DB) {
		c.addQuery(tx.Statement.SQL.String(), tx.Statement.Vars)
	}); err != nil {
		t.Fatalf("register query capture: %v", err)
	}
	if err := gdb.Callback().Delete().After("gorm:delete").Register("repo-test:capture-delete", func(tx *gorm.DB) {
		c.addDelete(tx.Statement.SQL.String(), tx.Statement.Vars)
	}); err != nil {
		t.Fatalf("register delete capture: %v", err)
	}
	if err := gdb.Callback().Raw().Before("gorm:raw").Register("repo-test:capture-raw", func(tx *gorm.DB) {
		c.addRaw(tx.Statement.SQL.String(), tx.Statement.Vars)
	}); err != nil {
		t.Fatalf("register raw capture: %v", err)
	}
	return gdb.Session(&gorm.Session{DryRun: true}), c
}

type nopConnector struct{}

func (nopConnector) Connect(context.Context) (driver.Conn, error) { return nopConn{}, nil }
func (nopConnector) Driver() driver.Driver                        { return nopDriver{} }

type nopDriver struct{}

func (nopDriver) Open(string) (driver.Conn, error) { return nopConn{}, nil }

type nopConn struct{}

func (nopConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("nop conn") }
func (nopConn) Close() error                        { return nil }
func (nopConn) Begin() (driver.Tx, error)           { return nil, errors.New("nop conn") }

// ---------- fake database for behavior that needs executed results ----------

// fakeResult reports the configured rows-affected count.
type fakeResult struct{ rows int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.rows, nil }

// fakeRows is a static result set.
type fakeRows struct {
	columns []string
	rows    [][]driver.Value
	i       int
}

func (r *fakeRows) Columns() []string { return r.columns }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// eventFakeState simulates the (email_uuid, type, provider_event_id) unique
// index of email_events so Create's conflict contract can be observed, and
// holds the rows-affected count the next DELETE reports.
type eventFakeState struct {
	mu           sync.Mutex
	inserted     map[string]bool
	inserts      []map[string]driver.Value // accepted inserts, parsed column → value
	nextID       int64
	deleteResult int64
}

func newEventFakeState() *eventFakeState {
	return &eventFakeState{inserted: make(map[string]bool), nextID: 100}
}

// fakeEventConnector serves one shared state across pooled connections.
type fakeEventConnector struct{ state *eventFakeState }

func (c fakeEventConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeEventConn{state: c.state}, nil
}
func (c fakeEventConnector) Driver() driver.Driver { return nopDriver{} }

type fakeEventConn struct{ state *eventFakeState }

func (c fakeEventConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake conn: prepare unsupported")
}
func (c fakeEventConn) Close() error { return nil }
func (c fakeEventConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fake conn: tx unsupported")
}

// parseInsertColumns extracts the quoted column list of an INSERT statement
// and maps each bound variable position to its column name.
func parseInsertColumns(sql string, vars []driver.NamedValue) map[string]driver.Value {
	i := strings.Index(sql, " VALUES (")
	if i < 0 {
		return nil
	}
	open := strings.LastIndex(sql[:i], "(")
	if open < 0 {
		return nil
	}
	parts := strings.Split(sql[open+1:i], ",")
	values := make(map[string]driver.Value, len(parts))
	for j, p := range parts {
		col := strings.Trim(strings.TrimSpace(p), `"`)
		if j < len(vars) {
			values[col] = vars[j].Value
		}
	}
	return values
}

// handleInsert applies the dedup semantics of the unique index: a repeated
// (email_uuid, type, provider_event_id) key is silently skipped. It reports
// whether the row was accepted and its assigned id.
func (c *fakeEventConn) handleInsert(sql string, vars []driver.NamedValue) (bool, int64) {
	values := parseInsertColumns(sql, vars)
	key := fmt.Sprintf("%v|%v|%v", values["email_uuid"], values["type"], values["provider_event_id"])
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.inserted[key] {
		return false, 0
	}
	c.state.inserted[key] = true
	c.state.nextID++
	values["id"] = c.state.nextID
	c.state.inserts = append(c.state.inserts, values)
	return true, c.state.nextID
}

func (c *fakeEventConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	if strings.Contains(lower, "insert into") {
		if accepted, id := c.handleInsert(query, args); accepted {
			return &fakeRows{columns: []string{"id"}, rows: [][]driver.Value{{id}}}, nil
		}
		return &fakeRows{columns: []string{"id"}}, nil // conflict: no row returned
	}
	// No SELECTs are exercised through the fake; answer with an empty set.
	return &fakeRows{columns: []string{"x"}}, nil
}

func (c *fakeEventConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.ToLower(query)
	switch {
	case strings.Contains(lower, "insert into"):
		if accepted, _ := c.handleInsert(query, args); accepted {
			return fakeResult{rows: 1}, nil
		}
		return fakeResult{rows: 0}, nil
	case strings.Contains(lower, "delete"):
		c.state.mu.Lock()
		defer c.state.mu.Unlock()
		return fakeResult{rows: c.state.deleteResult}, nil
	default:
		return fakeResult{rows: 1}, nil
	}
}

func fakeEventGorm(t *testing.T, state *eventFakeState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(fakeEventConnector{state: state}),
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open fake gorm: %v", err)
	}
	return gdb
}

// ---------- EmailEventRepository.Create: conflict-aware contract ----------

// TestEmailEventCreateConflictContract pins the dedup contract of Create: a
// fresh key reports (true, nil), a replayed key reports (false, nil) — a
// duplicate is not an error — and an empty metadata column is persisted as the
// structural default.
func TestEmailEventCreateConflictContract(t *testing.T) {
	state := newEventFakeState()
	repo := NewEmailEventRepository(fakeEventGorm(t, state))

	first := &models.EmailEvent{EmailUUID: "em-1", Type: "opened", ProviderEventID: "k1", Metadata: `{}`}
	created, err := repo.Create(first)
	if err != nil || !created {
		t.Fatalf("first Create = (%v, %v), want (true, nil)", created, err)
	}

	replay := &models.EmailEvent{EmailUUID: "em-1", Type: "opened", ProviderEventID: "k1"}
	created, err = repo.Create(replay)
	if err != nil {
		t.Fatalf("conflicting Create must not error, got %v", err)
	}
	if created {
		t.Fatal("conflicting Create must report created=false")
	}

	distinct := &models.EmailEvent{EmailUUID: "em-1", Type: "opened", ProviderEventID: "k2"}
	if created, err = repo.Create(distinct); err != nil || !created {
		t.Fatalf("distinct-key Create = (%v, %v), want (true, nil)", created, err)
	}

	emptyMeta := &models.EmailEvent{EmailUUID: "em-1", Type: "clicked", ProviderEventID: "k3"}
	if _, err = repo.Create(emptyMeta); err != nil {
		t.Fatalf("empty-metadata Create: %v", err)
	}
	if emptyMeta.Metadata != "{}" {
		t.Errorf("empty metadata = %q, want the structural default {}", emptyMeta.Metadata)
	}
	state.mu.Lock()
	last := state.inserts[len(state.inserts)-1]
	state.mu.Unlock()
	if got := last["metadata"]; got != "{}" {
		t.Errorf("persisted metadata = %v, want {}", got)
	}
}

// ---------- EmailEventRepository.ListFiltered: filter composition ----------

// TestEmailEventListFilteredComposesFilters pins the WHERE composition of
// workspace, type-IN, message and occurred-at range filters, and the
// newest-first page ordering.
func TestEmailEventListFilteredComposesFilters(t *testing.T) {
	dry, cap := dryCapture(t)
	repo := NewEmailEventRepository(dry)

	ws := uint(3)
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	_, _, err := repo.ListFiltered(2, 50, &ws, []string{"opened", "clicked"}, "em-1", &from, &to)
	if err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}

	// Two statements: the count, then the page.
	if len(cap.queries) != 2 {
		t.Fatalf("statements captured = %d, want 2 (count + page)", len(cap.queries))
	}
	count, page := cap.queries[0], cap.queries[1]

	for _, stmt := range []capturedStmt{count, page} {
		for _, want := range []string{"workspace_id = ", "type IN (", "occurred_at >= ", "occurred_at < "} {
			if !strings.Contains(stmt.sql, want) {
				t.Errorf("statement missing %q:\n%s", want, stmt.sql)
			}
		}
		joined := fmt.Sprint(stmt.vars)
		for _, want := range []string{"3", "opened", "clicked", "em-1", "2026-05-01", "2026-05-02"} {
			if !strings.Contains(joined, want) {
				t.Errorf("statement vars %v missing %q", stmt.vars, want)
			}
		}
	}
	if strings.Contains(count.sql, "LIMIT") {
		t.Errorf("count must not be paginated:\n%s", count.sql)
	}
	if !strings.Contains(page.sql, "ORDER BY occurred_at DESC") {
		t.Errorf("page statement missing the newest-first order:\n%s", page.sql)
	}
	// gorm binds LIMIT/OFFSET: the trailing vars are (limit, offset).
	if lim, off := trailingPageVars(t, page); lim != 50 || off != 50 {
		t.Errorf("page limit/offset = %d/%d, want 50/50 (page 2 of size 50)", lim, off)
	}
}

// trailingPageVars extracts the (limit, offset) bound at the end of a page
// statement. An offset of zero omits the OFFSET clause entirely.
func trailingPageVars(t *testing.T, stmt capturedStmt) (int64, int64) {
	t.Helper()
	switch n := len(stmt.vars); {
	case n == 1:
		lim, ok := stmt.vars[0].(int)
		if !ok {
			t.Fatalf("page var %v is not the limit", stmt.vars[0])
		}
		return int64(lim), 0
	case n >= 2:
		lim, limOK := stmt.vars[n-2].(int)
		off, offOK := stmt.vars[n-1].(int)
		if !limOK || !offOK {
			t.Fatalf("trailing vars %v are not (limit, offset)", stmt.vars[n-2:])
		}
		return int64(lim), int64(off)
	default:
		t.Fatalf("page statement has %d vars, want limit (and offset):\n%s", n, stmt.sql)
		return 0, 0
	}
}

// TestEmailEventListFilteredPagination pins the page normalization: the size
// cap and the page→offset mapping.
func TestEmailEventListFilteredPagination(t *testing.T) {
	dry, cap := dryCapture(t)
	repo := NewEmailEventRepository(dry)

	// Size over the cap and a 1-based page of 3: cap 5000 → 100, offset 2*100.
	if _, _, err := repo.ListFiltered(3, 5000, nil, nil, "", nil, nil); err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}
	if lim, off := trailingPageVars(t, cap.lastQuery()); lim != 100 || off != 200 {
		t.Errorf("page limit/offset = %d/%d, want 100/200", lim, off)
	}

	// Degenerate values fall back to the default page.
	cap.mu.Lock()
	cap.queries = nil
	cap.mu.Unlock()
	if _, _, err := repo.ListFiltered(0, 0, nil, nil, "", nil, nil); err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}
	if lim, off := trailingPageVars(t, cap.lastQuery()); lim != 20 || off != 0 {
		t.Errorf("default page limit/offset = %d/%d, want 20/0", lim, off)
	}
}

// TestEmailEventListFilteredDropsEmptyFilters pins that absent filters produce
// no WHERE clause at all — no workspace pin, no type restriction.
func TestEmailEventListFilteredDropsEmptyFilters(t *testing.T) {
	dry, cap := dryCapture(t)
	repo := NewEmailEventRepository(dry)

	_, _, err := repo.ListFiltered(1, 20, nil, nil, "", nil, nil)
	if err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}
	page := cap.lastQuery()
	if strings.Contains(page.sql, "WHERE") {
		t.Errorf("unfiltered statement must have no WHERE:\n%s", page.sql)
	}
	if !strings.Contains(page.sql, "ORDER BY occurred_at DESC") {
		t.Errorf("page must stay newest-first:\n%s", page.sql)
	}
}

// ---------- EmailEventRepository.ListByEmailUUID*: timeline order ----------

// TestEmailEventListByEmailUUIDOrdersOldestFirst pins the timeline contract:
// one message's events, oldest first, and the batch variant filters on the
// message uuid set with the same order.
func TestEmailEventListByEmailUUIDOrdersOldestFirst(t *testing.T) {
	dry, cap := dryCapture(t)
	repo := NewEmailEventRepository(dry)

	if _, err := repo.ListByEmailUUID("em-9"); err != nil {
		t.Fatalf("ListByEmailUUID: %v", err)
	}
	stmt := cap.lastQuery()
	for _, want := range []string{"email_uuid = ", "ORDER BY occurred_at ASC"} {
		if !strings.Contains(stmt.sql, want) {
			t.Errorf("timeline statement missing %q:\n%s", want, stmt.sql)
		}
	}
	if !strings.Contains(fmt.Sprint(stmt.vars), "em-9") {
		t.Errorf("timeline vars %v missing the uuid", stmt.vars)
	}

	cap.mu.Lock()
	cap.queries = nil
	cap.mu.Unlock()
	if events, err := repo.ListByEmailUUIDs(nil); err != nil || events != nil {
		t.Fatalf("ListByEmailUUIDs(nil) = (%v, %v), want (nil, nil)", events, err)
	}
	if len(cap.queries) != 0 {
		t.Fatalf("empty uuid list must not query, got %d statements", len(cap.queries))
	}

	if _, err := repo.ListByEmailUUIDs([]string{"a", "b"}); err != nil {
		t.Fatalf("ListByEmailUUIDs: %v", err)
	}
	stmt = cap.lastQuery()
	for _, want := range []string{"email_uuid IN (", "ORDER BY occurred_at ASC"} {
		if !strings.Contains(stmt.sql, want) {
			t.Errorf("batch statement missing %q:\n%s", want, stmt.sql)
		}
	}
}

// ---------- EmailEventRepository.DeleteOlderThan ----------

// TestEmailEventDeleteOlderThan pins the retention cutoff statement and that
// the rows-affected count is reported.
func TestEmailEventDeleteOlderThan(t *testing.T) {
	dry, cap := dryCapture(t)
	repo := NewEmailEventRepository(dry)

	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := repo.DeleteOlderThan(cutoff); err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}
	if len(cap.deletes) != 1 {
		t.Fatalf("delete statements = %d, want 1", len(cap.deletes))
	}
	stmt := cap.deletes[0]
	if !strings.Contains(stmt.sql, `DELETE FROM "email_events"`) || !strings.Contains(stmt.sql, "occurred_at < ") {
		t.Errorf("delete SQL = %q, want a cutoff on occurred_at", stmt.sql)
	}
	if len(stmt.vars) != 1 || !stmt.vars[0].(time.Time).Equal(cutoff) {
		t.Errorf("delete vars = %v, want the cutoff", stmt.vars)
	}

	// Executed behavior: the driver's rows-affected passes through.
	state := newEventFakeState()
	state.deleteResult = 7
	got, err := NewEmailEventRepository(fakeEventGorm(t, state)).DeleteOlderThan(cutoff)
	if err != nil || got != 7 {
		t.Fatalf("executed DeleteOlderThan = (%d, %v), want (7, nil)", got, err)
	}
}

// ---------- EmailRepository.UpdateStatusTimestamp: write-once SQL ----------

// TestUpdateStatusTimestampBuildsWriteOnceSQL pins the exact guarded UPDATE
// the recorder relies on, and that nothing outside the timestamp whitelist is
// ever written into the column position.
func TestUpdateStatusTimestampBuildsWriteOnceSQL(t *testing.T) {
	dry, cap := dryCapture(t)
	repo := NewEmailRepository(dry)

	at := time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC)
	if err := repo.UpdateStatusTimestamp("em-1", "opened_at", at); err != nil {
		t.Fatalf("UpdateStatusTimestamp: %v", err)
	}
	if len(cap.raws) != 1 {
		t.Fatalf("raw statements = %d, want 1", len(cap.raws))
	}
	want := "UPDATE emails SET opened_at = $1 WHERE uuid = $2 AND opened_at IS NULL"
	if got := cap.raws[0].sql; got != want {
		t.Errorf("update SQL =\n%q\nwant\n%q", got, want)
	}
	if len(cap.raws[0].vars) != 2 || !cap.raws[0].vars[0].(time.Time).Equal(at) || cap.raws[0].vars[1] != "em-1" {
		t.Errorf("update vars = %v, want [at, em-1]", cap.raws[0].vars)
	}

	// Every whitelisted column builds the same guarded shape.
	for _, col := range []string{"sent_at", "delivered_at", "opened_at", "clicked_at", "bounced_at", "complained_at", "unsubscribed_at"} {
		cap.mu.Lock()
		cap.raws = nil
		cap.mu.Unlock()
		if err := repo.UpdateStatusTimestamp("em-1", col, at); err != nil {
			t.Fatalf("UpdateStatusTimestamp(%s): %v", col, err)
		}
		want := fmt.Sprintf("UPDATE emails SET %s = $1 WHERE uuid = $2 AND %s IS NULL", col, col)
		if got := cap.raws[0].sql; got != want {
			t.Errorf("column %s: update SQL =\n%q\nwant\n%q", col, got, want)
		}
	}

	// The whitelist is the injection guard: anything else is refused before a
	// statement is built.
	for _, bad := range []string{"html_body", "status", "sent_at; DROP TABLE emails", ""} {
		cap.mu.Lock()
		cap.raws = nil
		cap.mu.Unlock()
		if err := repo.UpdateStatusTimestamp("em-1", bad, at); err == nil {
			t.Errorf("column %q must be refused", bad)
		}
		if len(cap.raws) != 0 {
			t.Errorf("refused column %q must not build SQL", bad)
		}
	}
}

// ---------- EmailRepository: slim message list + status flags ----------

// TestListFilteredMessagesSelectsSlimColumns pins the payload contract of the
// message-history list: bodies and content blobs are never selected, the
// metadata columns and write-once timestamps are.
func TestListFilteredMessagesSelectsSlimColumns(t *testing.T) {
	dry, cap := dryCapture(t)
	repo := NewEmailRepository(dry)

	if _, _, err := repo.ListFilteredMessages(1, 20, ResourceScope{UserID: 5}, EmailFilter{}); err != nil {
		t.Fatalf("ListFilteredMessages: %v", err)
	}
	if len(cap.queries) != 2 {
		t.Fatalf("statements = %d, want 2 (count + page)", len(cap.queries))
	}
	page := cap.lastQuery()

	for _, banned := range []string{"html_body", "text_body", "attachments_json", "headers_json", "template_data"} {
		if strings.Contains(page.sql, banned) {
			t.Errorf("slim selection must not fetch %q:\n%s", banned, page.sql)
		}
	}
	for _, want := range []string{"SELECT id, uuid, external_id", "sent_at", "opened_at", "error_message", "provider", "smtp_hostname"} {
		if !strings.Contains(page.sql, want) {
			t.Errorf("slim selection missing %q:\n%s", want, page.sql)
		}
	}
}

// TestStatusFlagCondsBuildsAnyOfPredicate pins the repeatable status filter:
// timestamp flags become IS NOT NULL conditions, status flags bind the enum,
// duplicates and unknown flags are ignored.
func TestStatusFlagCondsBuildsAnyOfPredicate(t *testing.T) {
	cases := []struct {
		name  string
		flags []string
		cond  string
		args  []any
	}{
		{"timestamp flags", []string{"sent", "opened"},
			"sent_at IS NOT NULL OR opened_at IS NOT NULL", nil},
		{"status flags", []string{"failed", "pending"},
			"status = ? OR status = ?", []any{models.EmailStatusFailed, models.EmailStatusPending}},
		{"mixed", []string{"bounced", "suppressed"},
			"bounced_at IS NOT NULL OR status = ?", []any{models.EmailStatusSuppressed}},
		{"dedup case-insensitive, unknown dropped", []string{"sent", "SENT", "", "bogus"},
			"sent_at IS NOT NULL", nil},
		{"empty", []string{"", " "}, "", nil},
		{"nil", nil, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cond, args := statusFlagConds(tc.flags)
			if cond != tc.cond {
				t.Errorf("cond = %q, want %q", cond, tc.cond)
			}
			if len(args) != len(tc.args) {
				t.Fatalf("args = %v, want %v", args, tc.args)
			}
			for i := range args {
				if args[i] != tc.args[i] {
					t.Errorf("args[%d] = %v, want %v", i, args[i], tc.args[i])
				}
			}
		})
	}
}
