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
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The segment handler is served through the real okapi stack with the real JWT
// middleware (the automation_handler_test.go arrangement). The database is a
// second fake database/sql driver dedicated to this file: it stores inserted
// rows and evaluates the simple equality WHERE clauses these tests use,
// because the assertions pin scope isolation, validation, pagination and the
// route order of /segments/preview — behaviors that need real request
// semantics. It also records every query so tests can assert that condition
// values arrive as bindvars, never as interpolated SQL.

const segTestWorkspace = "3"

var segTestTime = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

// segValidConditionJSON is the canonical condition the frontend contract
// documents.
const segValidConditionJSON = `{"logic":"and","groups":[{"logic":"and","rules":[{"field":"email","operator":"contains","value":"example.com"}]}]}`

// ---------- the fake database ----------

var segHColumns = map[string][]string{
	"segments":         {"id", "user_id", "workspace_id", "name", "description", "condition", "created_at", "updated_at"},
	"subscribers":      {"id", "user_id", "workspace_id", "email", "name", "status", "custom_fields", "timezone", "language", "subscribed_at", "unsubscribed_at", "created_at", "updated_at"},
	"subscriber_lists": {"id", "user_id", "workspace_id", "name", "description", "type", "filter_rules", "created_at", "updated_at"},
	"campaigns": {"id", "user_id", "workspace_id", "name", "subject", "from_email", "from_name", "template_id",
		"template_version_id", "language", "template_data", "status", "list_id", "segment_id", "send_rate",
		"send_at_local_time", "ab_test_enabled", "ab_test_variants", "ab_test_winner", "scheduled_at",
		"started_at", "completed_at", "created_at", "updated_at", "deleted_at", "snapshot"},
}

var segHDefaults = map[string]map[string]func() driver.Value{
	"segments": {
		"condition": func() driver.Value { return "{}" },
		"name":      func() driver.Value { return "" },
	},
	"subscribers": {
		"name":          func() driver.Value { return "" },
		"status":        func() driver.Value { return "subscribed" },
		"custom_fields": func() driver.Value { return "{}" },
		"timezone":      func() driver.Value { return "" },
		"language":      func() driver.Value { return "" },
	},
	"subscriber_lists": {
		"type":         func() driver.Value { return "static" },
		"filter_rules": func() driver.Value { return "[]" },
	},
	"campaigns": {
		"status":             func() driver.Value { return models.CampaignStatusDraft },
		"language":           func() driver.Value { return "en" },
		"template_data":      func() driver.Value { return "{}" },
		"ab_test_variants":   func() driver.Value { return "[]" },
		"snapshot":           func() driver.Value { return "{}" },
		"send_rate":          func() driver.Value { return int64(0) },
		"list_id":            func() driver.Value { return int64(0) },
		"send_at_local_time": func() driver.Value { return false },
		"ab_test_enabled":    func() driver.Value { return false },
		"deleted_at":         func() driver.Value { return nil },
	},
}

type segHTable struct {
	columns []string
	rows    [][]driver.Value
}

type segHState struct {
	mu       sync.Mutex
	tables   map[string]*segHTable
	seq      int64
	queries  []string
	lastArgs []driver.Value
}

func newSegHState() *segHState {
	tables := make(map[string]*segHTable, len(segHColumns))
	for name, cols := range segHColumns {
		tables[name] = &segHTable{columns: cols}
	}
	return &segHState{tables: tables, seq: 300}
}

func (s *segHState) colIndex(table, col string) int {
	for i, c := range segHColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

const segHTableSegments = "segments"

func (s *segHState) rowCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tables[segHTableSegments].rows)
}

// rowMap returns the row with the given id as a column map (nil when absent).
func (s *segHState) rowMap(id int64) map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.colIndex(segHTableSegments, "id")
	for _, row := range s.tables[segHTableSegments].rows {
		if row[idx] == id {
			out := make(map[string]driver.Value, len(row))
			for i, c := range segHColumns[segHTableSegments] {
				out[c] = row[i]
			}
			return out
		}
	}
	return nil
}

// recordedQueries snapshots the captured SQL text.
func (s *segHState) recordedQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func segHVal(v any) driver.Value {
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
		panic(fmt.Sprintf("segHVal: unsupported type %T", v))
	}
}

// seed inserts a fixture row into the given table.
func segHSeed(t *testing.T, s *segHState, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := segHColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := segHDefaults[table][col]; ok {
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
		row[idx] = segHVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

type segHConn struct{ s *segHState }

func (c *segHConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *segHConn) Close() error                        { return nil }
func (c *segHConn) Begin() (driver.Tx, error)           { return segHTx{}, nil }

type segHTx struct{}

func (segHTx) Commit() error   { return nil }
func (segHTx) Rollback() error { return nil }

type segHResult struct{ rows int64 }

func (r segHResult) LastInsertId() (int64, error) { return 0, nil }
func (r segHResult) RowsAffected() (int64, error) { return r.rows, nil }

type segHRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *segHRows) Columns() []string { return r.cols }
func (r *segHRows) Close() error      { return nil }
func (r *segHRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type segHConnector struct{ state *segHState }

func (c segHConnector) Connect(context.Context) (driver.Conn, error) {
	return &segHConn{s: c.state}, nil
}
func (c segHConnector) Driver() driver.Driver { return segHDrv{} }

type segHDrv struct{}

func (segHDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func segGorm(t *testing.T, state *segHState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(segHConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open segment handler fake gorm: %v", err)
	}
	return gdb
}

func (c *segHConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.record(query, args)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if strings.Contains(lower, "insert into") {
		return c.s.handleInsert(query, args)
	}
	table := segHTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("segHState: cannot route query: %s", query)
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(lower, "count(*)") {
		return &segHRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	matched = segHOrder(c.s, table, matched, lower)
	matched = segHLimitOffset(matched, lower, args)
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &segHRows{cols: segHColumns[table], rows: rows}, nil
}

func (c *segHConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.record(query, args)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := segHTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("segHState: unsupported exec: %s", query)
	}
	if strings.Contains(lower, "delete from") {
		matched, err := c.s.evalWhere(table, lower, args)
		if err != nil {
			return nil, err
		}
		c.s.mu.Lock()
		defer c.s.mu.Unlock()
		kept := c.s.tables[table].rows[:0]
		for _, row := range c.s.tables[table].rows {
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
		c.s.tables[table].rows = kept
		return segHResult{rows: int64(len(matched))}, nil
	}
	if !strings.Contains(lower, "update ") {
		return nil, fmt.Errorf("segHState: unsupported exec: %s", query)
	}
	return c.s.handleUpdate(table, lower, args)
}

func (c *segHConn) record(query string, args []driver.NamedValue) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.queries = append(c.s.queries, query)
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	c.s.lastArgs = vals
}

func segHTableOf(lowerQuery string) string {
	for _, name := range []string{"subscriber_lists", "campaigns", "segments", "subscribers"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`} {
			if strings.Contains(lowerQuery, pat) {
				return name
			}
		}
	}
	return ""
}

func (s *segHState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
	where := ""
	if i := strings.Index(compact, " where "); i >= 0 {
		where = compact[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit ", " returning ", " on conflict"} {
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
			idx := s.colIndex(table, segHColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
		default:
			eq := strings.Index(cond, " = ")
			if eq < 0 {
				return nil, fmt.Errorf("segHState: unsupported condition %q", cond)
			}
			idx := s.colIndex(table, segHColumnOf(cond[:eq]))
			n, err := strconv.Atoi(strings.TrimSpace(cond[eq+3:])[1:])
			if err != nil {
				return nil, fmt.Errorf("segHState: bad bindvar in %q", cond)
			}
			want := segHArg(args, n)
			preds = append(preds, func(row []driver.Value) bool { return segHEqual(row[idx], want) })
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

func segHColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func segHArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("segHState: no arg with ordinal %d", n))
}

func segHEqual(a, b driver.Value) bool {
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

func segHOrder(s *segHState, table string, rows [][]driver.Value, compact string) [][]driver.Value {
	i := strings.Index(compact, " order by ")
	if i < 0 {
		return rows
	}
	rest := compact[i+len(" order by "):]
	if j := strings.Index(rest, " limit "); j >= 0 {
		rest = rest[:j]
	}
	fields := strings.Fields(rest)
	idx := s.colIndex(table, segHColumnOf(fields[0]))
	desc := len(fields) > 1 && fields[1] == "desc"
	out := append([][]driver.Value(nil), rows...)
	sort.SliceStable(out, func(a, b int) bool {
		if desc {
			return segHLess(out[b][idx], out[a][idx])
		}
		return segHLess(out[a][idx], out[b][idx])
	})
	return out
}

func segHLess(a, b driver.Value) bool {
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

// segHBindToken parses a leading `$N` token (the bindvar may be followed by
// more SQL, e.g. "LIMIT $2 OFFSET $3").
func segHBindToken(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$") {
		return 0, fmt.Errorf("segHState: expected a bindvar, got %q", s)
	}
	s = s[1:]
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("segHState: bad bindvar %q", s)
	}
	return strconv.Atoi(s[:end])
}

func segHLimitOffset(rows [][]driver.Value, compact string, args []driver.NamedValue) [][]driver.Value {
	offset := 0
	if m := strings.Index(compact, " offset $"); m >= 0 {
		if n, err := segHBindToken(compact[m+len(" offset "):]); err == nil {
			if v, ok := segHArg(args, n).(int64); ok {
				offset = int(v)
			}
		}
	}
	if offset >= len(rows) {
		return nil
	}
	rows = rows[offset:]
	if m := strings.Index(compact, " limit $"); m >= 0 {
		if n, err := segHBindToken(compact[m+len(" limit "):]); err == nil {
			if v, ok := segHArg(args, n).(int64); ok && int(v) < len(rows) {
				rows = rows[:v]
			}
		}
	}
	return rows
}

func (s *segHState) handleInsert(query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	table := segHTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("segHState: insert without a known table: %s", query)
	}
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("segHState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, fmt.Errorf("segHState: insert without a column list: %s", query)
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
		return nil, fmt.Errorf("segHState: %d columns but %d values", len(cols), len(valParts))
	}
	vals := make([]driver.Value, len(valParts))
	for i, p := range valParts {
		n, err := strconv.Atoi(strings.TrimSpace(p)[1:])
		if err != nil {
			return nil, err
		}
		vals[i] = segHArg(args, n)
	}

	var returning []string
	if i := strings.Index(lower, " returning "); i >= 0 {
		for _, c := range strings.Split(lower[i+len(" returning "):], ",") {
			returning = append(returning, strings.Trim(strings.TrimSpace(c), `"`))
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	spec := segHColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := segHDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for j, col := range cols {
		row[s.colIndex(table, col)] = vals[j]
	}
	s.seq++
	row[s.colIndex(table, "id")] = s.seq
	s.tables[table].rows = append(s.tables[table].rows, row)

	out := make([]driver.Value, len(returning))
	for i, c := range returning {
		out[i] = row[s.colIndex(table, c)]
	}
	return &segHRows{cols: returning, rows: [][]driver.Value{out}}, nil
}

func (s *segHState) handleUpdate(table, compact string, args []driver.NamedValue) (driver.Result, error) {
	setStart := strings.Index(compact, " set ")
	whereStart := strings.Index(compact, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("segHState: malformed update: %s", compact)
	}
	type setter struct {
		idx int
		val driver.Value
	}
	var setters []setter
	for _, pair := range strings.Split(compact[setStart+len(" set "):whereStart], ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("segHState: malformed SET pair %q", pair)
		}
		idx := s.colIndex(table, segHColumnOf(pair[:eq]))
		if idx < 0 {
			return nil, fmt.Errorf("segHState: unknown column in SET %q", pair)
		}
		tok := strings.TrimSpace(pair[eq+1:])
		if !strings.HasPrefix(tok, "$") {
			return nil, fmt.Errorf("segHState: unsupported SET expression %q", tok)
		}
		n, _ := strconv.Atoi(tok[1:])
		setters = append(setters, setter{idx: idx, val: segHArg(args, n)})
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
	return segHResult{rows: int64(len(matched))}, nil
}

// ---------- fixtures ----------

func seedSegSegment(t *testing.T, state *segHState, id uint, conditionJSON string) {
	segHSeed(t, state, "segments", map[string]any{
		"id": id, "user_id": uint(7), "workspace_id": uint(3),
		"name": "Newsletter fans", "condition": conditionJSON,
		"created_at": segTestTime,
	})
}

func newSegmentServer(t *testing.T, state *segHState) *okapi.TestServer {
	t.Helper()
	gdb := segGorm(t, state)
	segmentRepo := repositories.NewSegmentRepository(gdb)
	subscriberRepo := repositories.NewSubscriberRepository(gdb)
	h := NewSegmentHandler(segmentRepo, subscriberRepo)
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		testWorkspaceBinding,
	)
	// The literal preview route is registered before any {id:int} route, and a
	// decoy POST /segments/{id:int} (never registered in production) makes the
	// precedence assertion meaningful: if the param route captured "preview",
	// this handler would answer 418.
	g.Post("/segments/{id:int}", func(c *okapi.Context) error {
		return c.JSON(http.StatusTeapot, okapi.M{"decoy": true})
	})
	g.Post("/segments/preview", okapi.H(h.Preview))
	g.Post("/segments", okapi.H(h.Create))
	g.Get("/segments", okapi.H(h.List))
	g.Get("/segments/{id:int}", okapi.H(h.Get))
	g.Put("/segments/{id:int}", okapi.H(h.Update))
	g.Delete("/segments/{id:int}", okapi.H(h.Delete))
	g.Get("/segments/{id:int}/contacts", okapi.H(h.Contacts))
	return o
}

func doSegReq(t *testing.T, o *okapi.TestServer, method, path, workspace string, body io.Reader) *httptest.ResponseRecorder {
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

// ---------- create: validation + persistence ----------

func TestCreateSegmentRejectsInvalidCondition(t *testing.T) {
	state := newSegHState()
	o := newSegmentServer(t, state)

	for _, tc := range []struct {
		name    string
		payload string
		wantSub string
	}{
		{
			name:    "unknown field names the rule",
			payload: `{"name":"S","condition":{"logic":"and","groups":[{"logic":"and","rules":[{"field":"emailx","operator":"equals","value":"x"}]}]}}`,
			wantSub: `group 1 rule 1: unknown field "emailx"`,
		},
		{
			name:    "injection attempt in the custom key",
			payload: `{"name":"S","condition":{"logic":"and","groups":[{"logic":"and","rules":[{"field":"custom_fields.x\"; DROP TABLE subscribers;--","operator":"equals","value":"v"}]}]}}`,
			wantSub: "invalid custom field key",
		},
		{
			name:    "missing value for a value operator",
			payload: `{"name":"S","condition":{"logic":"and","groups":[{"logic":"and","rules":[{"field":"email","operator":"equals"}]}]}}`,
			wantSub: `value is required for operator "equals"`,
		},
		{
			name:    "unparseable date",
			payload: `{"name":"S","condition":{"logic":"and","groups":[{"logic":"and","rules":[{"field":"created_at","operator":"greater_than","value":"yesterday"}]}]}}`,
			wantSub: "use RFC3339 or YYYY-MM-DD",
		},
		{
			name:    "condition missing entirely",
			payload: `{"name":"S"}`,
			wantSub: "condition is required",
		},
		{
			name:    "name missing",
			payload: `{"condition":{"logic":"and","groups":[{"logic":"and","rules":[{"field":"email","operator":"equals","value":"x"}]}]}}`,
			wantSub: "name is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doSegReq(t, o, http.MethodPost, "/workspaces/current/segments", segTestWorkspace, strings.NewReader(tc.payload))
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
			if len(resp.ValidationErrors) == 0 {
				t.Fatal("validation_errors must be present")
			}
			joined := strings.Join(resp.ValidationErrors, "; ")
			if !strings.Contains(joined, tc.wantSub) {
				t.Errorf("validation errors %q do not mention %q", joined, tc.wantSub)
			}
			for _, e := range resp.ValidationErrors {
				if strings.Contains(e, "0x") {
					t.Errorf("validation error %q must be address-free", e)
				}
			}
			if state.rowCount() != 0 {
				t.Errorf("a rejected segment must not be stored, got %d rows", state.rowCount())
			}
		})
	}
}

func TestCreateSegmentPersists(t *testing.T) {
	state := newSegHState()
	o := newSegmentServer(t, state)

	payload := `{"name":"Example.com fans","description":"Contains example.com","condition":` + segValidConditionJSON + `}`
	rec := doSegReq(t, o, http.MethodPost, "/workspaces/current/segments", segTestWorkspace, strings.NewReader(payload))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	data := decodeSingle(t, rec.Body.Bytes())
	if data["name"] != "Example.com fans" {
		t.Errorf("name = %v", data["name"])
	}
	if id, _ := data["id"].(float64); id == 0 {
		t.Errorf("id = %v, want the created row id", data["id"])
	}
	if _, ok := data["condition"].(map[string]any); !ok {
		t.Errorf("condition = %v, want the condition document", data["condition"])
	}
	if data["workspace_id"] != float64(3) {
		t.Errorf("workspace_id = %v, want 3", data["workspace_id"])
	}
	if state.rowCount() != 1 {
		t.Fatalf("segments stored = %d, want 1", state.rowCount())
	}
	row := state.rowMap(301)
	if row == nil {
		t.Fatal("stored row not found")
	}
	var stored models.SegmentCondition
	if err := json.Unmarshal(row["condition"].([]byte), &stored); err != nil {
		t.Fatalf("stored condition is not valid JSON: %v", err)
	}
	if len(stored.Groups) != 1 || len(stored.Groups[0].Rules) != 1 {
		t.Errorf("stored condition = %+v", stored)
	}
}

// ---------- list: matching counts + pagination ----------

func TestListSegmentsHasLiveMatchingCounts(t *testing.T) {
	state := newSegHState()
	o := newSegmentServer(t, state)

	seedSegSegment(t, state, 11, `{"logic":"and","groups":[{"logic":"and","rules":[{"field":"email","operator":"equals","value":"alice@example.com"}]}]}`)
	seedSegSegment(t, state, 12, `{"logic":"and","groups":[{"logic":"and","rules":[{"field":"email","operator":"equals","value":"nobody@nowhere.io"}]}]}`)
	segHSeed(t, state, "segments", map[string]any{
		"id": uint(13), "user_id": uint(7), "workspace_id": uint(9),
		"name": "Foreign", "condition": segValidConditionJSON, "created_at": segTestTime,
	})
	segHSeed(t, state, "subscribers", map[string]any{
		"id": uint(21), "user_id": uint(7), "workspace_id": uint(3), "email": "alice@example.com", "status": "subscribed",
	})
	segHSeed(t, state, "subscribers", map[string]any{
		"id": uint(22), "user_id": uint(7), "workspace_id": uint(3), "email": "bob@example.com", "status": "subscribed",
	})

	rec := doSegReq(t, o, http.MethodGet, "/workspaces/current/segments?size=10", segTestWorkspace, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	envelope, data := decodeEnvelope(t, rec.Body.Bytes())
	if len(data) != 2 {
		t.Fatalf("rows = %d, want 2 (the workspace-9 segment must not leak)", len(data))
	}
	counts := map[float64]float64{}
	for _, row := range data {
		m := row.(map[string]any)
		counts[m["id"].(float64)] = m["matching_count"].(float64)
	}
	if counts[11] != 1 || counts[12] != 0 {
		t.Errorf("matching counts = %v, want 11 -> 1 and 12 -> 0", counts)
	}
	pageable, _ := envelope["pageable"].(map[string]any)
	if pageable["total_elements"] != float64(2) {
		t.Errorf("total_elements = %v, want 2", pageable["total_elements"])
	}
}

// ---------- get / update / delete: scope isolation ----------

func TestSegmentScopeIsolation(t *testing.T) {
	state := newSegHState()
	o := newSegmentServer(t, state)
	seedSegSegment(t, state, 11, segValidConditionJSON)

	// A member of workspace 3 sees it.
	if rec := doSegReq(t, o, http.MethodGet, "/workspaces/current/segments/11", segTestWorkspace, nil); rec.Code != http.StatusOK {
		t.Fatalf("owner status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// A member of workspace 9 gets the same 404 an unknown id gets.
	if rec := doSegReq(t, o, http.MethodGet, "/workspaces/current/segments/11", "9", nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign workspace status = %d, want 404", rec.Code)
	}
	if rec := doSegReq(t, o, http.MethodDelete, "/workspaces/current/segments/11", "9", nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign delete status = %d, want 404", rec.Code)
	}
	if rec := doSegReq(t, o, http.MethodPut, "/workspaces/current/segments/11", "9", strings.NewReader(`{"name":"Hacked"}`)); rec.Code != http.StatusNotFound {
		t.Errorf("foreign update status = %d, want 404", rec.Code)
	}
	if state.rowMap(11) == nil {
		t.Error("the refused calls must not delete the segment")
	}
}

func TestUpdateAndDeleteSegment(t *testing.T) {
	state := newSegHState()
	o := newSegmentServer(t, state)
	seedSegSegment(t, state, 11, segValidConditionJSON)

	// A broken condition must not reach the row.
	rec := doSegReq(t, o, http.MethodPut, "/workspaces/current/segments/11", segTestWorkspace,
		strings.NewReader(`{"condition":{"logic":"and","groups":[{"logic":"and","rules":[{"field":"nope","operator":"equals","value":"x"}]}]}}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad condition update status = %d, want 400", rec.Code)
	}
	row := state.rowMap(11)
	if row["name"].(string) != "Newsletter fans" {
		t.Errorf("rejected update changed the name to %v", row["name"])
	}

	rec = doSegReq(t, o, http.MethodPut, "/workspaces/current/segments/11", segTestWorkspace,
		strings.NewReader(`{"name":"Renamed","description":"d"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if data := decodeSingle(t, rec.Body.Bytes()); data["name"] != "Renamed" {
		t.Errorf("updated name = %v", data["name"])
	}

	rec = doSegReq(t, o, http.MethodDelete, "/workspaces/current/segments/11", segTestWorkspace, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", rec.Code)
	}
	if state.rowMap(11) != nil {
		t.Error("the segment must be gone after DELETE")
	}
}

// ---------- preview: route order, response shape, parameterization ----------

func TestPreviewSegmentRouteOrderAndShape(t *testing.T) {
	state := newSegHState()
	o := newSegmentServer(t, state)
	segHSeed(t, state, "subscribers", map[string]any{
		"id": uint(21), "user_id": uint(7), "workspace_id": uint(3),
		"email": "alice@example.com", "name": "Alice", "status": "subscribed",
		"language": "en", "created_at": segTestTime,
	})

	// The fake driver evaluates simple equality conditions; the operator
	// surface itself is pinned in segment_query_test.go.
	payload := `{"condition":{"logic":"and","groups":[{"logic":"and","rules":[{"field":"email","operator":"equals","value":"alice@example.com"}]}]},"page":0,"size":1}`
	rec := doSegReq(t, o, http.MethodPost, "/workspaces/current/segments/preview", segTestWorkspace, strings.NewReader(payload))
	if rec.Code == http.StatusTeapot {
		t.Fatal("POST /segments/preview was captured by the {id:int} param route — literal route must win")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Count      int64            `json:"count"`
			Page       int              `json:"page"`
			Size       int              `json:"size"`
			TotalPages int              `json:"total_pages"`
			Contacts   []map[string]any `json:"contacts"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success {
		t.Fatal("preview response must use the standard success envelope")
	}
	if resp.Data.Count != 1 || resp.Data.Page != 0 || resp.Data.Size != 1 || resp.Data.TotalPages != 1 {
		t.Errorf("envelope = count %d page %d size %d total_pages %d, want 1/0/1/1", resp.Data.Count, resp.Data.Page, resp.Data.Size, resp.Data.TotalPages)
	}
	if len(resp.Data.Contacts) != 1 {
		t.Fatalf("contacts = %d, want 1", len(resp.Data.Contacts))
	}
	first := resp.Data.Contacts[0]
	if first["id"] != float64(21) || first["email"] != "alice@example.com" {
		t.Errorf("first contact = %v, want subscriber 21", first)
	}
	for _, key := range []string{"id", "email", "name", "status", "timezone", "language", "created_at", "subscribed_at"} {
		if _, ok := first[key]; !ok {
			t.Errorf("contact is missing %q", key)
		}
	}
	if _, ok := first["custom_fields"]; ok {
		t.Error("the contact projection must not carry custom_fields")
	}

	// The preview SELECT must be workspace-scoped and parameterized.
	found := false
	for _, q := range state.recordedQueries() {
		lower := strings.ToLower(q)
		if strings.Contains(lower, "from \"subscribers\"") {
			found = true
			if !strings.Contains(lower, "workspace_id = $") {
				t.Errorf("preview query is not workspace-scoped: %s", q)
			}
			if strings.Contains(q, "ali") {
				t.Errorf("preview query embeds the raw value: %s", q)
			}
		}
	}
	if !found {
		t.Error("no subscribers query was captured for the preview")
	}
}

func TestPreviewSegmentRejectsInvalidCondition(t *testing.T) {
	state := newSegHState()
	o := newSegmentServer(t, state)

	payload := `{"condition":{"logic":"and","groups":[{"logic":"and","rules":[{"field":"custom_fields.x; DROP TABLE subscribers;--","operator":"equals","value":"v"}]}]}}`
	rec := doSegReq(t, o, http.MethodPost, "/workspaces/current/segments/preview", segTestWorkspace, strings.NewReader(payload))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ValidationErrors []string `json:"validation_errors"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.ValidationErrors) == 0 {
		t.Fatal("validation_errors must be present")
	}
	if state.rowCount() != 0 {
		t.Error("a preview must not store anything")
	}
}

// ---------- contacts of a saved segment ----------

func TestSegmentContactsPaged(t *testing.T) {
	state := newSegHState()
	o := newSegmentServer(t, state)
	seedSegSegment(t, state, 11, `{"logic":"and","groups":[{"logic":"and","rules":[{"field":"email","operator":"equals","value":"alice@example.com"}]}]}`)
	segHSeed(t, state, "subscribers", map[string]any{
		"id": uint(21), "user_id": uint(7), "workspace_id": uint(3), "email": "alice@example.com",
		"status": "subscribed", "created_at": segTestTime,
	})

	rec := doSegReq(t, o, http.MethodGet, "/workspaces/current/segments/11/contacts?page=0&size=10", segTestWorkspace, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Count    int64            `json:"count"`
			Page     int              `json:"page"`
			Size     int              `json:"size"`
			Contacts []map[string]any `json:"contacts"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Count != 1 || resp.Data.Page != 0 || resp.Data.Size != 10 || len(resp.Data.Contacts) != 1 {
		t.Errorf("response = %+v", resp.Data)
	}
	if resp.Data.Contacts[0]["email"] != "alice@example.com" {
		t.Errorf("contact email = %v", resp.Data.Contacts[0]["email"])
	}

	// Foreign workspace: 404 like an unknown id.
	if rec := doSegReq(t, o, http.MethodGet, "/workspaces/current/segments/11/contacts", "9", nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign contacts status = %d, want 404", rec.Code)
	}
}

// ---------- campaign audience XOR validation ----------

func newCampaignServer(t *testing.T, state *segHState) *okapi.TestServer {
	t.Helper()
	gdb := segGorm(t, state)
	campaignRepo := repositories.NewCampaignRepository(gdb)
	messageRepo := repositories.NewCampaignMessageRepository(gdb)
	listRepo := repositories.NewSubscriberListRepository(gdb)
	segmentRepo := repositories.NewSegmentRepository(gdb)
	subscriberRepo := repositories.NewSubscriberRepository(gdb)
	h := NewCampaignHandler(campaignRepo, messageRepo, listRepo, segmentRepo, subscriberRepo, nil, nil, nil)
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		testWorkspaceBinding,
	)
	g.Post("/campaigns", okapi.H(h.Create))
	return o
}

func TestCampaignCreateAudienceXOR(t *testing.T) {
	state := newSegHState()
	o := newCampaignServer(t, state)

	seedSegSegment(t, state, 11, segValidConditionJSON)
	segHSeed(t, state, "segments", map[string]any{
		"id": uint(12), "user_id": uint(8), "workspace_id": uint(9),
		"name": "Foreign", "condition": segValidConditionJSON, "created_at": segTestTime,
	})
	segHSeed(t, state, "subscriber_lists", map[string]any{
		"id": uint(31), "user_id": uint(7), "workspace_id": uint(3), "name": "Customers",
	})
	segHSeed(t, state, "subscriber_lists", map[string]any{
		"id": uint(32), "user_id": uint(8), "workspace_id": uint(9), "name": "Foreign list",
	})

	base := `{"name":"Launch","subject":"Hi","from_email":"hello@example.com","template_id":5`
	cases := []struct {
		name    string
		payload string
		want    int
	}{
		{
			name:    "both audiences rejected",
			payload: base + `,"list_id":31,"segment_id":11}`,
			want:    http.StatusBadRequest,
		},
		{
			name:    "no audience rejected",
			payload: base + `}`,
			want:    http.StatusBadRequest,
		},
		{
			name:    "foreign segment rejected without existence leak",
			payload: base + `,"segment_id":12}`,
			want:    http.StatusNotFound,
		},
		{
			name:    "foreign list rejected (ownership now enforced)",
			payload: base + `,"list_id":32}`,
			want:    http.StatusNotFound,
		},
		{
			name:    "segment audience accepted",
			payload: base + `,"segment_id":11}`,
			want:    http.StatusCreated,
		},
		{
			name:    "list audience accepted",
			payload: base + `,"list_id":31}`,
			want:    http.StatusCreated,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doSegReq(t, o, http.MethodPost, "/workspaces/current/campaigns", segTestWorkspace, strings.NewReader(tc.payload))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}

	// The stored rows reflect the audience that was accepted.
	segmentCampaigns, listCampaigns := 0, 0
	state.mu.Lock()
	segIdx := state.colIndex("campaigns", "segment_id")
	for _, row := range state.tables["campaigns"].rows {
		segVal, _ := row[segIdx].(int64)
		switch segVal {
		case 11:
			segmentCampaigns++
		case 0:
			listCampaigns++
		}
	}
	state.mu.Unlock()
	if segmentCampaigns != 1 || listCampaigns != 1 {
		t.Errorf("stored campaigns: segment %d, list %d, want 1 and 1", segmentCampaigns, listCampaigns)
	}
}
