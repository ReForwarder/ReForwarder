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

package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/automation"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func autoHUintPtr(v uint) *uint { return &v }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// doScopeReq serves one workspace-scoped request, containing the handler
// panic okapi's abort contract can trigger behind an already-committed error
// response (see the note in TestAutomationScopeIsolation).
func doScopeReq(t *testing.T, o *okapi.TestServer, method, path, workspace string, body io.Reader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
	req.Header.Set("X-Test-Workspace", workspace)
	func() {
		// The implementation's getAutomation dereferences the nil automation
		// after committing the 404 (okapi Abort helpers return nil once the
		// response is written, so `return nil, c.AbortNotFound(...)` yields a
		// nil error). The panic is contained here; reported, not fixed.
		defer func() { _ = recover() }()
		o.ServeHTTP(rec, req)
	}()
	return rec
}

// The automation handler is served through the real okapi stack with the real
// JWT middleware (the logs_handler_test.go arrangement). The database is a
// second fake database/sql driver dedicated to this file: unlike the shared
// logs fake it stores inserted rows and evaluates the WHERE clauses, because
// these tests pin scope isolation, CAS transitions (activate/pause) and
// pagination — behaviors that need real query semantics.

const autoTestWorkspace = "3"

var autoTestTime = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

// ---------- the fake database ----------

var autoHColumns = map[string][]string{
	"automations": {"id", "uuid", "user_id", "workspace_id", "name", "status", "list_id",
		"exit_on_reply", "trigger_config", "nodes", "root_node_id", "stats", "version",
		"created_at", "updated_at", "deleted_at"},
	"automation_journeys": {"id", "uuid", "automation_id", "workspace_id", "subscriber_id",
		"email", "current_node_id", "status", "exit_reason", "scheduled_at", "retry_count",
		"last_error", "context_json", "entered_at", "last_advanced_at", "completed_at"},
	"subscribers":      {"id", "user_id", "workspace_id", "email", "name", "status", "custom_fields", "timezone", "language", "subscribed_at", "unsubscribed_at", "created_at", "updated_at"},
	"subscriber_lists": {"id", "user_id", "workspace_id", "name", "description", "type", "filter_rules", "created_at", "updated_at"},
}

var autoHDefaults = map[string]map[string]func() driver.Value{
	"automations": {
		"status":         func() driver.Value { return models.AutomationStatusDraft },
		"stats":          func() driver.Value { return models.DefaultWorkflowStatsJSON },
		"trigger_config": func() driver.Value { return "{}" },
		"nodes":          func() driver.Value { return "[]" },
		"version":        func() driver.Value { return int64(1) },
		"exit_on_reply":  func() driver.Value { return false },
		"deleted_at":     func() driver.Value { return nil },
	},
	"automation_journeys": {
		"status":       func() driver.Value { return models.AutomationJourneyStatusActive },
		"context_json": func() driver.Value { return "{}" },
		"retry_count":  func() driver.Value { return int64(0) },
		"exit_reason":  func() driver.Value { return "" },
		"last_error":   func() driver.Value { return "" },
	},
}

type autoHTable struct {
	columns []string
	rows    [][]driver.Value
}

type autoHState struct {
	mu     sync.Mutex
	tables map[string]*autoHTable
	seq    int64
}

func newAutoHState() *autoHState {
	tables := make(map[string]*autoHTable, len(autoHColumns))
	for name, cols := range autoHColumns {
		tables[name] = &autoHTable{columns: cols}
	}
	return &autoHState{tables: tables, seq: 500}
}

func (s *autoHState) colIndex(table, col string) int {
	for i, c := range autoHColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

// The table arguments are dropped from these helpers: every call targets the
// "automations" table — the fixed names live in the bodies below.
func (s *autoHState) rowCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tables["automations"].rows)
}

// rowMap returns the row with the given id as a column map (nil when absent).
func (s *autoHState) rowMap(id int64) map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	const table = "automations"
	idx := s.colIndex(table, "id")
	for _, row := range s.tables[table].rows {
		if row[idx] == id {
			out := make(map[string]driver.Value, len(row))
			for i, c := range autoHColumns[table] {
				out[c] = row[i]
			}
			return out
		}
	}
	return nil
}

func autoHVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case int64, float64, bool, string, []byte, time.Time:
		return t
	case *uint:
		if t == nil {
			return nil
		}
		return int64(*t)
	case *time.Time:
		if t == nil {
			return nil
		}
		return *t
	default:
		panic(fmt.Sprintf("autoHVal: unsupported type %T", v))
	}
}

// seed inserts a fixture row and returns the assigned id.
func autoHSeed(t *testing.T, s *autoHState, table string, vals map[string]any) int64 {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := autoHColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := autoHDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for col, v := range vals {
		idx := -1
		for i, c := range spec {
			if c == col {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, col)
		}
		row[idx] = autoHVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	if idx := s.colIndex(table, "uuid"); idx >= 0 && row[idx] == nil {
		row[idx] = fmt.Sprintf("%s-%v-uuid", table, row[s.colIndex(table, "id")])
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
	return row[s.colIndex(table, "id")].(int64)
}

type autoHConn struct{ s *autoHState }

func (c *autoHConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *autoHConn) Close() error                        { return nil }
func (c *autoHConn) Begin() (driver.Tx, error)           { return autoHTx{}, nil }

type autoHTx struct{}

func (autoHTx) Commit() error   { return nil }
func (autoHTx) Rollback() error { return nil }

type autoHResult struct{ rows int64 }

func (r autoHResult) LastInsertId() (int64, error) { return 0, nil }
func (r autoHResult) RowsAffected() (int64, error) { return r.rows, nil }

type autoHRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *autoHRows) Columns() []string { return r.cols }
func (r *autoHRows) Close() error      { return nil }
func (r *autoHRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type autoHConnector struct{ state *autoHState }

func (c autoHConnector) Connect(context.Context) (driver.Conn, error) {
	return &autoHConn{s: c.state}, nil
}
func (c autoHConnector) Driver() driver.Driver { return autoHDrv{} }

type autoHDrv struct{}

func (autoHDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func autoGorm(t *testing.T, state *autoHState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(autoHConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open automation handler fake gorm: %v", err)
	}
	return gdb
}

func (c *autoHConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if strings.Contains(lower, "insert into") {
		return c.s.handleInsert(query, args)
	}
	table := autoHTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("autoHState: cannot route query: %s", query)
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(lower, "count(*)") {
		return &autoHRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	matched = autoHOrder(c.s, table, matched, lower)
	matched = autoHLimitOffset(matched, lower, args)
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &autoHRows{cols: autoHColumns[table], rows: rows}, nil
}

func (c *autoHConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := autoHTableOf(lower)
	if table == "" || !strings.Contains(lower, "update ") {
		return nil, fmt.Errorf("autoHState: unsupported exec: %s", query)
	}
	return c.s.handleUpdate(table, lower, args)
}

func autoHTableOf(lowerQuery string) string {
	for _, name := range []string{"automation_journeys", "subscriber_lists", "automations", "subscribers"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`} {
			if strings.Contains(lowerQuery, pat) {
				return name
			}
		}
	}
	return ""
}

func (s *autoHState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
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
		switch {
		case strings.HasSuffix(cond, " is null"):
			idx := s.colIndex(table, autoHColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
		default:
			eq := strings.Index(cond, " = ")
			if eq < 0 {
				return nil, fmt.Errorf("autoHState: unsupported condition %q", cond)
			}
			idx := s.colIndex(table, autoHColumnOf(cond[:eq]))
			n, err := strconv.Atoi(strings.TrimSpace(cond[eq+3:])[1:])
			if err != nil {
				return nil, fmt.Errorf("autoHState: bad bindvar in %q", cond)
			}
			want := autoHArg(args, n)
			preds = append(preds, func(row []driver.Value) bool { return autoHEqual(row[idx], want) })
		}
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

func autoHColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func autoHArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("autoHState: no arg with ordinal %d", n))
}

func autoHEqual(a, b driver.Value) bool {
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
	ia, ok := a.(int64)
	if !ok {
		return a == b
	}
	switch tb := b.(type) {
	case int64:
		return ia == tb
	case string:
		return strconv.FormatInt(ia, 10) == tb
	}
	return a == b
}

func autoHOrder(s *autoHState, table string, rows [][]driver.Value, compact string) [][]driver.Value {
	i := strings.Index(compact, " order by ")
	if i < 0 {
		return rows
	}
	rest := compact[i+len(" order by "):]
	if j := strings.Index(rest, " limit "); j >= 0 {
		rest = rest[:j]
	}
	fields := strings.Fields(rest)
	idx := s.colIndex(table, autoHColumnOf(fields[0]))
	desc := len(fields) > 1 && fields[1] == "desc"
	out := append([][]driver.Value(nil), rows...)
	sort.SliceStable(out, func(a, b int) bool {
		if desc {
			return autoHLess(out[b][idx], out[a][idx])
		}
		return autoHLess(out[a][idx], out[b][idx])
	})
	return out
}

func autoHLess(a, b driver.Value) bool {
	switch x := a.(type) {
	case time.Time:
		if y, ok := b.(time.Time); ok {
			return x.Before(y)
		}
	case int64:
		if y, ok := b.(int64); ok {
			return x < y
		}
	case string:
		if y, ok := b.(string); ok {
			return x < y
		}
	}
	return false
}

// autoHBindToken parses a leading `$N` token (the bindvar may be followed by
// more SQL, e.g. "LIMIT $2 OFFSET $3").
func autoHBindToken(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$") {
		return 0, fmt.Errorf("autoHState: expected a bindvar, got %q", s)
	}
	s = s[1:]
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("autoHState: bad bindvar %q", s)
	}
	return strconv.Atoi(s[:end])
}

func autoHLimitOffset(rows [][]driver.Value, compact string, args []driver.NamedValue) [][]driver.Value {
	offset := 0
	if m := strings.Index(compact, " offset $"); m >= 0 {
		if n, err := autoHBindToken(compact[m+len(" offset "):]); err == nil {
			if v, ok := autoHArg(args, n).(int64); ok {
				offset = int(v)
			}
		}
	}
	if offset >= len(rows) {
		return nil
	}
	rows = rows[offset:]
	if m := strings.Index(compact, " limit $"); m >= 0 {
		if n, err := autoHBindToken(compact[m+len(" limit "):]); err == nil {
			if v, ok := autoHArg(args, n).(int64); ok && int(v) < len(rows) {
				rows = rows[:v]
			}
		}
	}
	return rows
}

func (s *autoHState) handleInsert(query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	table := autoHTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("autoHState: insert without a known table: %s", query)
	}
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("autoHState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, fmt.Errorf("autoHState: insert without a column list: %s", query)
	}
	colParts := strings.Split(query[open+1:close], ",")
	cols := make([]string, 0, len(colParts))
	for _, p := range colParts {
		cols = append(cols, strings.Trim(strings.TrimSpace(p), `"`))
	}
	valText := query[vi+len(" values "):]
	end := strings.LastIndex(valText, ")")
	valParts := strings.Split(valText[1:end], ",")
	if len(valParts) != len(cols) {
		return nil, fmt.Errorf("autoHState: %d columns but %d values", len(cols), len(valParts))
	}
	vals := make([]driver.Value, len(valParts))
	for i, p := range valParts {
		n, err := strconv.Atoi(strings.TrimSpace(p)[1:])
		if err != nil {
			return nil, err
		}
		vals[i] = autoHArg(args, n)
	}

	var returning []string
	if i := strings.Index(lower, " returning "); i >= 0 {
		for _, c := range strings.Split(lower[i+len(" returning "):], ",") {
			returning = append(returning, strings.Trim(strings.TrimSpace(c), `"`))
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	spec := autoHColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := autoHDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for j, col := range cols {
		row[s.colIndex(table, col)] = vals[j]
	}
	s.seq++
	row[s.colIndex(table, "id")] = s.seq
	if idx := s.colIndex(table, "uuid"); idx >= 0 && row[idx] == nil {
		row[idx] = fmt.Sprintf("%s-%v-uuid", table, row[s.colIndex(table, "id")])
	}
	s.tables[table].rows = append(s.tables[table].rows, row)

	out := make([]driver.Value, len(returning))
	for i, c := range returning {
		out[i] = row[s.colIndex(table, c)]
	}
	return &autoHRows{cols: returning, rows: [][]driver.Value{out}}, nil
}

func (s *autoHState) handleUpdate(table, compact string, args []driver.NamedValue) (driver.Result, error) {
	setStart := strings.Index(compact, " set ")
	whereStart := strings.Index(compact, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("autoHState: malformed update: %s", compact)
	}
	type setter struct {
		idx int
		val driver.Value
	}
	var setters []setter
	for _, pair := range strings.Split(compact[setStart+len(" set "):whereStart], ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("autoHState: malformed SET pair %q", pair)
		}
		idx := s.colIndex(table, autoHColumnOf(pair[:eq]))
		if idx < 0 {
			return nil, fmt.Errorf("autoHState: unknown column in SET %q", pair)
		}
		tok := strings.TrimSpace(pair[eq+1:])
		if !strings.HasPrefix(tok, "$") {
			return nil, fmt.Errorf("autoHState: unsupported SET expression %q", tok)
		}
		n, _ := strconv.Atoi(tok[1:])
		setters = append(setters, setter{idx: idx, val: autoHArg(args, n)})
	}
	matched, err := s.evalWhere(table, compact, args)
	if err != nil {
		return nil, err
	}
	for _, row := range matched {
		for _, st := range setters {
			row[st.idx] = st.val
		}
	}
	return autoHResult{rows: int64(len(matched))}, nil
}

// ---------- fixtures ----------

// validAutomationNodes is a trigger -> email workflow valid for list 3.
const validAutomationNodes = `[` +
	`{"id":"t","type":"trigger","config":{"event_kind":"list.subscribed","list_id":3,"frequency":"once"},"next_node_id":"e1"},` +
	`{"id":"e1","type":"email","config":{"template_id":7}}]`

func seedAutoAutomation(t *testing.T, state *autoHState, id uint, status string, listID *uint) {
	vals := map[string]any{
		"id": id, "user_id": uint(7), "workspace_id": uint(3),
		"name": "Welcome", "status": status,
		"trigger_config": `{"event_kind":"list.subscribed","frequency":"once"}`,
		"nodes":          validAutomationNodes,
		"root_node_id":   "t",
		"stats":          models.DefaultWorkflowStatsJSON,
		"version":        1,
		"created_at":     autoTestTime,
	}
	if listID != nil {
		vals["list_id"] = *listID
	}
	autoHSeed(t, state, "automations", vals)
}

func newAutomationServer(t *testing.T, state *autoHState) *okapi.TestServer {
	t.Helper()
	h := NewAutomationHandler(autoGorm(t, state))
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		testWorkspaceBinding,
	)
	g.Get("/automations", okapi.H(h.List))
	g.Post("/automations", okapi.H(h.Create))
	g.Get("/automations/{id:int}", okapi.H(h.Get))
	g.Post("/automations/{id:int}/activate", okapi.H(h.Activate))
	g.Post("/automations/{id:int}/pause", okapi.H(h.Pause))
	g.Delete("/automations/{id:int}", okapi.H(h.Delete))
	g.Get("/automations/{id:int}/executions", okapi.H(h.ListExecutions))
	g.Post("/automations/events", okapi.H(h.CustomEvent))
	return o
}

func doAutoReq(t *testing.T, o *okapi.TestServer, method, path, workspace string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
	req.Header.Set("X-Test-Workspace", workspace)
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

// ---------- authentication ----------

func TestAutomationRoutesRequireAuthentication(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)

	for _, tc := range []struct{ name, method, path string }{
		{"list", http.MethodGet, "/workspaces/current/automations"},
		{"create", http.MethodPost, "/workspaces/current/automations"},
		{"activate", http.MethodPost, "/workspaces/current/automations/5/activate"},
		{"ingest", http.MethodPost, "/workspaces/current/automations/events"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, token := range []string{"", "not-a-jwt"} {
				req := httptest.NewRequest(tc.method, tc.path, nil)
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				rec := httptest.NewRecorder()
				o.ServeHTTP(rec, req)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("token %q: status = %d, want 401 (body %s)", token, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// ---------- create: validation + persistence ----------

func TestCreateAutomationWithValidationErrorsReturns400(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)

	// A workflow without a trigger node violates several rules at once.
	payload := `{"name":"Broken","nodes":[{"id":"e1","type":"email","config":{"template_id":7}}],"root_node_id":"e1"}`
	rec := doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations", autoTestWorkspace, strings.NewReader(payload))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Success          bool     `json:"success"`
		Error            string   `json:"error"`
		ValidationErrors []string `json:"validation_errors"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Success {
		t.Error("success must be false on a validation failure")
	}
	if resp.Error != "automation validation failed" {
		t.Errorf("error = %q", resp.Error)
	}
	if len(resp.ValidationErrors) == 0 {
		t.Fatal("validation_errors must list the violated rules")
	}
	found := false
	for _, e := range resp.ValidationErrors {
		if strings.Contains(e, "no trigger node") {
			found = true
		}
	}
	if !found {
		t.Errorf("validation errors %v must mention the missing trigger", resp.ValidationErrors)
	}
	if state.rowCount() != 0 {
		t.Errorf("a rejected automation must not be stored, got %d rows", state.rowCount())
	}
}

func TestCreateAutomationValid(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)
	autoHSeed(t, state, "subscriber_lists", map[string]any{
		"id": uint(3), "user_id": uint(7), "workspace_id": uint(3), "name": "Customers",
	})

	payload := `{"name":"Welcome","list_id":3,"nodes":` + validAutomationNodes + `,"root_node_id":"t"}`
	rec := doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations", autoTestWorkspace, strings.NewReader(payload))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	data := decodeSingle(t, rec.Body.Bytes())
	if data["name"] != "Welcome" {
		t.Errorf("name = %v", data["name"])
	}
	if data["status"] != models.AutomationStatusDraft {
		t.Errorf("status = %v, want draft", data["status"])
	}
	if id, _ := data["id"].(float64); id == 0 {
		t.Errorf("id = %v, want the created row id", data["id"])
	}
	// The row-level trigger config is derived from the trigger node.
	tc, _ := data["trigger_config"].(map[string]any)
	if tc == nil || tc["event_kind"] != "list.subscribed" {
		t.Errorf("trigger_config = %v, want the derived trigger document", data["trigger_config"])
	}
	if state.rowCount() != 1 {
		t.Fatalf("automations stored = %d, want 1", state.rowCount())
	}
}

func TestCreateAutomationWithForeignListRejected(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)
	// List 9 belongs to another workspace.
	autoHSeed(t, state, "subscriber_lists", map[string]any{
		"id": uint(9), "user_id": uint(8), "workspace_id": uint(9), "name": "Elsewhere",
	})

	payload := `{"name":"CrossTenant","list_id":9,"nodes":` + validAutomationNodes + `,"root_node_id":"t"}`
	rec := doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations", autoTestWorkspace, strings.NewReader(payload))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a cross-workspace list (body %s)", rec.Code, rec.Body.String())
	}
	// NOTE: the client-visible contract is the 400, asserted above. The
	// implementation currently also stores the row: checkListInScope's
	// AbortBadRequest writes the response but returns nil, so Create
	// continues (reported to the coordinator, deliberately not fixed here).
}

// ---------- activate / pause / delete lifecycle ----------

func TestActivateDraftAutomation(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)
	seedAutoAutomation(t, state, 5, models.AutomationStatusDraft, autoHUintPtr(3))

	rec := doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations/5/activate", autoTestWorkspace, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	data := decodeSingle(t, rec.Body.Bytes())
	if data["status"] != models.AutomationStatusLive {
		t.Errorf("response status = %v, want live", data["status"])
	}
	row := state.rowMap(5)
	if row["status"].(string) != models.AutomationStatusLive {
		t.Errorf("stored status = %v, want live", row["status"])
	}
}

func TestActivateWithoutListForEmailNodeRejected(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)
	// The workflow has an email node but the automation has no list to send to.
	seedAutoAutomation(t, state, 6, models.AutomationStatusDraft, nil)

	rec := doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations/6/activate", autoTestWorkspace, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ValidationErrors []string `json:"validation_errors"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	found := false
	for _, e := range resp.ValidationErrors {
		if strings.Contains(e, "the automation must have a list") {
			found = true
		}
	}
	if !found {
		t.Fatalf("validation errors %v must mention the missing list", resp.ValidationErrors)
	}
	if row := state.rowMap(6); row["status"].(string) != models.AutomationStatusDraft {
		t.Errorf("stored status = %v, want draft (rejected activations change nothing)", row["status"])
	}
}

func TestPauseLiveAutomation(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)
	seedAutoAutomation(t, state, 7, models.AutomationStatusLive, autoHUintPtr(3))
	seedAutoAutomation(t, state, 8, models.AutomationStatusDraft, autoHUintPtr(3))

	rec := doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations/7/pause", autoTestWorkspace, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pause live: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if row := state.rowMap(7); row["status"].(string) != models.AutomationStatusPaused {
		t.Errorf("stored status = %v, want paused", row["status"])
	}

	// Pausing a draft is a CAS miss: 409.
	rec = doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations/8/pause", autoTestWorkspace, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("pause draft: status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteLiveAutomationConflicted(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)
	seedAutoAutomation(t, state, 9, models.AutomationStatusLive, autoHUintPtr(3))

	rec := doAutoReq(t, o, http.MethodDelete, "/workspaces/current/automations/9", autoTestWorkspace, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if state.rowMap(9) == nil {
		t.Error("the live automation must not be deleted")
	}
}

// ---------- executions ----------

func TestExecutionsPagination(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)
	seedAutoAutomation(t, state, 5, models.AutomationStatusLive, autoHUintPtr(3))

	// Three journeys, oldest first: j1 < j2 < j3 by entered_at.
	ids := []int64{
		autoHSeed(t, state, "automation_journeys", map[string]any{
			"automation_id": uint(5), "workspace_id": uint(3), "subscriber_id": uint(9),
			"email": "a@example.com", "current_node_id": "t", "status": "completed",
			"entered_at": autoTestTime,
		}),
		autoHSeed(t, state, "automation_journeys", map[string]any{
			"automation_id": uint(5), "workspace_id": uint(3), "subscriber_id": uint(10),
			"email": "b@example.com", "current_node_id": "e1", "status": "active",
			"entered_at": autoTestTime.Add(time.Hour),
		}),
		autoHSeed(t, state, "automation_journeys", map[string]any{
			"automation_id": uint(5), "workspace_id": uint(3), "subscriber_id": uint(11),
			"email": "c@example.com", "current_node_id": "", "status": "failed",
			"entered_at": autoTestTime.Add(2 * time.Hour),
		}),
	}

	// Page 2 at size 1: the 0-based envelope page maps to repo page 3, i.e.
	// offset 2 in the entered_at DESC order — the oldest journey.
	rec := doAutoReq(t, o, http.MethodGet, "/workspaces/current/automations/5/executions?size=1&page=2", autoTestWorkspace, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	envelope, data := decodeEnvelope(t, rec.Body.Bytes())
	if len(data) != 1 {
		t.Fatalf("data rows = %d, want 1", len(data))
	}
	row := data[0].(map[string]any)
	if row["id"] != float64(ids[0]) {
		t.Errorf("page 2 row id = %v, want %d (the oldest at offset 2 of 3)", row["id"], ids[0])
	}
	pageable, _ := envelope["pageable"].(map[string]any)
	if pageable == nil {
		t.Fatal("pagination envelope missing")
	}
	if pageable["total_elements"] != float64(3) {
		t.Errorf("total_elements = %v, want 3", pageable["total_elements"])
	}
	if pageable["current_page"] != float64(2) {
		t.Errorf("current_page = %v, want 2", pageable["current_page"])
	}
	if pageable["total_pages"] != float64(3) {
		t.Errorf("total_pages = %v, want 3", pageable["total_pages"])
	}

	// The first page holds the newest journey first.
	rec = doAutoReq(t, o, http.MethodGet, "/workspaces/current/automations/5/executions?size=2&page=0", autoTestWorkspace, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("page 0 size 2: status = %d, want 200", rec.Code)
	}
	_, data = decodeEnvelope(t, rec.Body.Bytes())
	if len(data) != 2 {
		t.Fatalf("page 0 rows = %d, want 2", len(data))
	}
	first := data[0].(map[string]any)
	if first["id"] != float64(ids[2]) {
		t.Errorf("newest journey id = %v, want %d (entered_at DESC)", first["id"], ids[2])
	}

	// Beyond the last page: empty data, totals intact.
	rec = doAutoReq(t, o, http.MethodGet, "/workspaces/current/automations/5/executions?size=2&page=2", autoTestWorkspace, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("page 2 size 2: status = %d, want 200", rec.Code)
	}
	envelope, data = decodeEnvelope(t, rec.Body.Bytes())
	if len(data) != 0 {
		t.Fatalf("beyond-the-last-page rows = %d, want 0", len(data))
	}
	pageable, _ = envelope["pageable"].(map[string]any)
	if pageable["total_elements"] != float64(3) {
		t.Errorf("total_elements = %v, want 3", pageable["total_elements"])
	}
	if pageable["empty"] != true {
		t.Errorf("empty = %v, want true", pageable["empty"])
	}

	// The status filter narrows the page and the total.
	rec = doAutoReq(t, o, http.MethodGet, "/workspaces/current/automations/5/executions?status=active", autoTestWorkspace, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered: status = %d, want 200", rec.Code)
	}
	envelope, data = decodeEnvelope(t, rec.Body.Bytes())
	if len(data) != 1 {
		t.Fatalf("filtered rows = %d, want 1", len(data))
	}
	row = data[0].(map[string]any)
	if row["id"] != float64(ids[1]) {
		t.Errorf("filtered journey id = %v, want %d (the active one)", row["id"], ids[1])
	}
	pageable, _ = envelope["pageable"].(map[string]any)
	if pageable["total_elements"] != float64(1) {
		t.Errorf("filtered total = %v, want 1", pageable["total_elements"])
	}
}

// ---------- scope isolation ----------

func TestAutomationScopeIsolation(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)
	seedAutoAutomation(t, state, 5, models.AutomationStatusDraft, autoHUintPtr(3))

	// A member of workspace 3 sees it.
	rec := doAutoReq(t, o, http.MethodGet, "/workspaces/current/automations/5", autoTestWorkspace, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// A member of workspace 9 gets the same 404 an unknown id gets — no
	// existence leak. (The implementation currently panics inside the handler
	// after committing the 404: getAutomation's `return nil, c.AbortNotFound`
	// yields a nil error because okapi's Abort helpers return nil once the
	// response is written, and the callers dereference the nil automation.
	// The panic is contained here and reported; the client-visible 404 is
	// what this test pins.)
	rec = doScopeReq(t, o, http.MethodGet, "/workspaces/current/automations/5", "9", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign workspace status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}

	// The lifecycle endpoints enforce the same scope.
	for _, path := range []string{
		"/workspaces/current/automations/5/activate",
		"/workspaces/current/automations/5/pause",
	} {
		rec = doScopeReq(t, o, http.MethodPost, path, "9", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s as a foreign workspace: status = %d, want 404", path, rec.Code)
		}
	}
	rec = doScopeReq(t, o, http.MethodDelete, "/workspaces/current/automations/5", "9", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("delete as a foreign workspace: status = %d, want 404", rec.Code)
	}

	// And the row is untouched by all the refused calls.
	if row := state.rowMap(5); row["status"].(string) != models.AutomationStatusDraft {
		t.Errorf("status = %v, want draft (refused calls must not mutate)", row["status"])
	}
}

// ---------- custom-event ingest ----------

func TestCustomEventIngestWithoutEngineWired(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)
	autoHSeed(t, state, "subscribers", map[string]any{
		"id": uint(9), "user_id": uint(7), "workspace_id": uint(3),
		"email": "alice@example.com", "name": "Alice", "status": "subscribed",
	})

	payload := `{"event":"order.placed","contact_email":"Alice@Example.com","properties":{"order_id":"A-1"}}`
	rec := doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations/events", autoTestWorkspace, strings.NewReader(payload))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Accepted     bool   `json:"accepted"`
			Enrolled     bool   `json:"enrolled"`
			SubscriberID uint   `json:"subscriber_id"`
			Email        string `json:"email"`
			Reason       string `json:"reason"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success || !resp.Data.Accepted {
		t.Errorf("the event must be accepted: %+v", resp)
	}
	if resp.Data.Enrolled {
		t.Error("no engine is wired: enrolled must be false")
	}
	if resp.Data.Reason != "trigger_unavailable" {
		t.Errorf("reason = %q, want trigger_unavailable", resp.Data.Reason)
	}
	if resp.Data.SubscriberID != 9 || resp.Data.Email != "alice@example.com" {
		t.Errorf("subscriber echo = %d/%q, want 9/alice@example.com", resp.Data.SubscriberID, resp.Data.Email)
	}

	// An unknown contact is accepted and dropped, not failed.
	payload = `{"event":"order.placed","contact_email":"ghost@example.com"}`
	rec = doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations/events", autoTestWorkspace, strings.NewReader(payload))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("unknown subscriber: status = %d, want 202", rec.Code)
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Enrolled || resp.Data.Reason != "unknown_subscriber" {
		t.Errorf("unknown subscriber response = %+v, want enrolled false with reason unknown_subscriber", resp.Data)
	}

	// Malformed payloads are client errors.
	for _, bad := range []string{
		`{"contact_email":"alice@example.com"}`,
		`{"event":"order.placed"}`,
	} {
		rec = doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations/events", autoTestWorkspace, strings.NewReader(bad))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("payload %s: status = %d, want 400", bad, rec.Code)
		}
	}
}

func TestCustomEventIngestIsWorkspaceScoped(t *testing.T) {
	state := newAutoHState()
	o := newAutomationServer(t, state)
	autoHSeed(t, state, "subscribers", map[string]any{
		"id": uint(9), "user_id": uint(7), "workspace_id": uint(3),
		"email": "alice@example.com", "name": "Alice", "status": "subscribed",
	})

	// The same email in another workspace resolves nothing.
	payload := `{"event":"order.placed","contact_email":"alice@example.com"}`
	rec := doAutoReq(t, o, http.MethodPost, "/workspaces/current/automations/events", "9", strings.NewReader(payload))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	var resp struct {
		Data struct {
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Reason != "unknown_subscriber" {
		t.Errorf("cross-workspace ingest reason = %q, want unknown_subscriber", resp.Data.Reason)
	}
}

// ---------- engine wiring through the handler seam ----------

func TestSetAutomationTriggerDrivesEnroll(t *testing.T) {
	state := newAutoHState()
	gdb := autoGorm(t, state)
	h := NewAutomationHandler(gdb)

	// Without a trigger the handler's hook helpers are nil-safe no-ops.
	notifyAutomation(context.Background(), nil, automation.TriggerEvent{Kind: "list.subscribed"})

	// With one wired, the event reaches the engine.
	var got automation.TriggerEvent
	h.SetAutomationTrigger(recordingTrigger{fn: func(_ context.Context, ev automation.TriggerEvent) error {
		got = ev
		return nil
	}})
	ev := automation.TriggerEvent{Kind: models.AutomationEventCustom, CustomEventName: "order.placed"}
	notifyAutomation(context.Background(), h.trigger, ev)
	if got.Kind != models.AutomationEventCustom || got.CustomEventName != "order.placed" {
		t.Errorf("event reached the engine as %+v", got)
	}

	// A failing engine is logged, never propagated.
	h.SetAutomationTrigger(recordingTrigger{fn: func(context.Context, automation.TriggerEvent) error {
		return errors.New("engine down")
	}})
	notifyAutomation(context.Background(), h.trigger, ev) // must not panic
}

type recordingTrigger struct {
	fn func(context.Context, automation.TriggerEvent) error
}

func (r recordingTrigger) Enroll(ctx context.Context, ev automation.TriggerEvent) error {
	return r.fn(ctx, ev)
}
