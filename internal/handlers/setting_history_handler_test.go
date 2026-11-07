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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The settings history surface (SF-100) runs through the real gorm on a fake
// database/sql driver (the domain_signing_handler_test.go arrangement): the
// fake holds the settings, setting_history and events tables, evaluates the
// equality / IS NULL predicates the handler issues, and applies UPDATEs so
// capture-on-write and rollback semantics are pinned end to end — including
// that a rollback re-invalidates the settings provider's TTL cache.

var shColumns = map[string][]string{
	"settings": {"id", "key", "value", "type", "created_at", "updated_at"},
	"setting_history": {"id", "workspace_id", "key", "old_value", "new_value",
		"changed_by", "changed_by_email", "created_at"},
	"events": {"id", "category", "type", "workspace_id", "actor_id", "actor_name", "client_ip",
		"message", "metadata", "outcome", "target_type", "target_id", "created_at"},
}

type shTable struct {
	columns []string
	rows    [][]driver.Value
}

type shState struct {
	mu     sync.Mutex
	tables map[string]*shTable
	seq    int64
}

func newShState() *shState {
	tables := make(map[string]*shTable, len(shColumns))
	for name, cols := range shColumns {
		tables[name] = &shTable{columns: cols}
	}
	return &shState{tables: tables, seq: 900}
}

func (s *shState) colIndex(table, col string) int {
	for i, c := range shColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

// seed inserts a fixture row; unspecified columns are nil.
func shSeed(t *testing.T, s *shState, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := shColumns[table]
	row := make([]driver.Value, len(spec))
	for i := range spec {
		row[i] = nil
	}
	for col, v := range vals {
		idx := s.colIndex(table, col)
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, col)
		}
		row[idx] = shVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func shVal(v any) driver.Value {
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
	case *uint:
		if t == nil {
			return nil
		}
		return int64(*t)
	case *string:
		if t == nil {
			return nil
		}
		return *t
	default:
		panic(fmt.Sprintf("shVal: unsupported type %T", v))
	}
}

// rowsOf returns table rows as column maps (for assertions).
func (s *shState) rowsOf(table string) []map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, 0, len(s.tables[table].rows))
	for _, row := range s.tables[table].rows {
		m := map[string]driver.Value{}
		for i, c := range shColumns[table] {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

// historyEntries decodes the setting_history rows ordered oldest first.
func (s *shState) historyEntries() []map[string]driver.Value {
	return s.rowsOf("setting_history")
}

// eventPayloads returns the persisted audit events as (type, metadata) pairs.
func (s *shState) eventPayloads() []struct {
	Type     string
	Metadata string
} {
	var out []struct {
		Type     string
		Metadata string
	}
	for _, m := range s.rowsOf("events") {
		md, _ := m["metadata"].(string)
		out = append(out, struct {
			Type     string
			Metadata string
		}{Type: m["type"].(string), Metadata: md})
	}
	return out
}

func shGorm(t *testing.T, state *shState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(shConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open settings-history fake gorm: %v", err)
	}
	return gdb
}

type shConnector struct{ state *shState }

func (c shConnector) Connect(context.Context) (driver.Conn, error) { return &shConn{s: c.state}, nil }
func (c shConnector) Driver() driver.Driver                        { return shDrv{} }

type shDrv struct{}

func (shDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type shConn struct{ s *shState }

func (c *shConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *shConn) Close() error                        { return nil }
func (c *shConn) Begin() (driver.Tx, error)           { return shTx{}, nil }

type shTx struct{}

func (shTx) Commit() error   { return nil }
func (shTx) Rollback() error { return nil }

type shResult struct{ rows int64 }

func (r shResult) LastInsertId() (int64, error) { return 0, nil }
func (r shResult) RowsAffected() (int64, error) { return r.rows, nil }

type shRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *shRows) Columns() []string { return r.cols }
func (r *shRows) Close() error      { return nil }
func (r *shRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func shTableOf(lower string) string {
	for _, name := range []string{"setting_history", "settings", "events"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`} {
			if strings.Contains(lower, pat) {
				return name
			}
		}
	}
	return ""
}

func (c *shConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := shTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("shState: cannot route query: %s", query)
	}
	// gorm issues INSERT ... RETURNING as a query (pgx routes it through
	// QueryContext), so inserts arrive here, not in ExecContext.
	if strings.Contains(lower, "insert into") {
		return c.handleInsert(lower, table, query, args)
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(lower, "count(*)") {
		return &shRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	// ORDER BY and LIMIT/OFFSET are applied by the fake in the order gorm
	// emits them; the handler sorts in SQL, so the fake must too.
	matched = shOrderBy(table, lower, matched)
	matched = shLimitOffset(lower, args, matched)
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &shRows{cols: shColumns[table], rows: rows}, nil
}

func (c *shConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := shTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("shState: unsupported exec: %s", query)
	}
	if strings.Contains(lower, "insert into") {
		return nil, fmt.Errorf("shState: inserts with RETURNING must arrive via QueryContext: %s", query)
	}
	if !strings.Contains(lower, "update ") {
		return nil, fmt.Errorf("shState: unsupported exec: %s", query)
	}
	return c.handleUpdate(table, lower, args)
}

func (c *shConn) handleInsert(lower, table, query string, args []driver.NamedValue) (driver.Rows, error) {
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("shState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, fmt.Errorf("shState: insert without a column list")
	}
	colParts := strings.Split(query[open+1:close], ",")

	valText := query[vi+len(" values "):]
	end := strings.LastIndex(valText, ")")
	valText = valText[:end] // strip the trailing ")" (and any RETURNING)
	// Split multi-row VALUES on the "),(" tuple separator.
	tuples := strings.Split(strings.TrimPrefix(strings.TrimSpace(valText), "("), "),(")

	var returning []string
	if i := strings.Index(lower, " returning "); i >= 0 {
		for _, cn := range strings.Split(lower[i+len(" returning "):], ",") {
			returning = append(returning, strings.Trim(strings.TrimSpace(cn), `"`))
		}
	}

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	var lastRow []driver.Value
	for _, tuple := range tuples {
		valParts := strings.Split(tuple, ",")
		if len(valParts) != len(colParts) {
			return nil, fmt.Errorf("shState: %d columns but %d values", len(colParts), len(valParts))
		}
		vals := make([]driver.Value, len(valParts))
		for i, p := range valParts {
			n, err := strconv.Atoi(strings.TrimSpace(p)[1:])
			if err != nil {
				return nil, err
			}
			vals[i] = shArg(args, n)
		}
		row := make([]driver.Value, len(shColumns[table]))
		for i := range row {
			row[i] = nil
		}
		for j, col := range colParts {
			col = strings.Trim(strings.TrimSpace(col), `"`)
			idx := s.colIndex(table, col)
			if idx < 0 {
				return nil, fmt.Errorf("shState: %s has no column %q", table, col)
			}
			row[idx] = vals[j]
		}
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
		s.tables[table].rows = append(s.tables[table].rows, row)
		lastRow = row
	}

	out := make([]driver.Value, len(returning))
	for i, cn := range returning {
		out[i] = lastRow[s.colIndex(table, cn)]
	}
	return &shRows{cols: returning, rows: [][]driver.Value{out}}, nil
}

func (c *shConn) handleUpdate(table, lower string, args []driver.NamedValue) (driver.Result, error) {
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("shState: malformed update: %s", lower)
	}
	type setter struct {
		idx int
		val driver.Value
	}
	var setters []setter
	for _, pair := range strings.Split(lower[setStart+len(" set "):whereStart], ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("shState: malformed SET pair %q", pair)
		}
		idx := c.s.colIndex(table, shColumnOf(pair[:eq]))
		if idx < 0 {
			return nil, fmt.Errorf("shState: unknown column in SET %q", pair)
		}
		tok := strings.TrimSpace(pair[eq+1:])
		if !strings.HasPrefix(tok, "$") {
			return nil, fmt.Errorf("shState: unsupported SET expression %q", tok)
		}
		n, _ := strconv.Atoi(tok[1:])
		setters = append(setters, setter{idx: idx, val: shArg(args, n)})
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	for _, row := range matched {
		for _, st := range setters {
			row[st.idx] = st.val
		}
	}
	return shResult{rows: int64(len(matched))}, nil
}

// evalWhere evaluates a flattened WHERE clause of = and IS NULL predicates —
// the only forms the settings/history queries emit.
func (s *shState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ""
	if i := strings.Index(compact, " where "); i >= 0 {
		where = compact[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit ", " offset ", " returning "} {
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
			idx := s.colIndex(table, shColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
			continue
		}
		eq := strings.Index(cond, " = ")
		if eq < 0 {
			return nil, fmt.Errorf("shState: unsupported condition %q", cond)
		}
		idx := s.colIndex(table, shColumnOf(cond[:eq]))
		n, err := strconv.Atoi(strings.TrimSpace(cond[eq+3:])[1:])
		if err != nil {
			return nil, fmt.Errorf("shState: bad bindvar in %q", cond)
		}
		want := shArg(args, n)
		preds = append(preds, func(row []driver.Value) bool { return shEqual(row[idx], want) })
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

// shOrderBy applies the query's ORDER BY clause(s). gorm merges consecutive
// Order() calls into one multi-column clause, so all comma-separated terms
// are parsed and applied from last to first (stable), making the first term
// dominate — matching SQL semantics.
func shOrderBy(table, lower string, rows [][]driver.Value) [][]driver.Value {
	i := strings.Index(lower, " order by ")
	if i < 0 {
		return rows
	}
	rest := lower[i+len(" order by "):]
	end := len(rest)
	for _, cut := range []string{" limit ", " offset "} {
		if j := strings.Index(rest, cut); j >= 0 && j < end {
			end = j
		}
	}
	type term struct {
		idx  int
		desc bool
	}
	var terms []term
	for _, part := range strings.Split(rest[:end], ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		desc := strings.HasSuffix(part, " desc")
		col := shColumnOf(strings.TrimSuffix(strings.TrimSuffix(part, " desc"), " asc"))
		idx := -1
		for k, c := range shColumns[table] {
			if c == col {
				idx = k
			}
		}
		if idx < 0 {
			continue
		}
		terms = append(terms, term{idx: idx, desc: desc})
	}
	if len(terms) == 0 {
		return rows
	}
	out := append([][]driver.Value(nil), rows...)
	// Apply the LAST term first; a stable insertion sort per term lets
	// earlier terms dominate, mirroring SQL's multi-key sort.
	for t := len(terms) - 1; t >= 0; t-- {
		idx, desc := terms[t].idx, terms[t].desc
		for a := 1; a < len(out); a++ {
			for b := a; b > 0 && shCompare(out[b][idx], out[b-1][idx], desc); b-- {
				out[b], out[b-1] = out[b-1], out[b]
			}
		}
	}
	return out
}

func shCompare(a, b driver.Value, before bool) bool {
	av, aok := a.(int64)
	bv, bok := b.(int64)
	if aok && bok {
		if before {
			return av > bv
		}
		return av < bv
	}
	as, _ := a.(string)
	bs, _ := b.(string)
	if before {
		return as > bs
	}
	return as < bs
}

func shLimitOffset(lower string, args []driver.NamedValue, rows [][]driver.Value) [][]driver.Value {
	// SQL applies OFFSET before LIMIT.
	if m := strings.Index(lower, " offset "); m >= 0 {
		if n, err := shBindToken(lower[m+len(" offset "):]); err == nil {
			if v, ok := shArg(args, n).(int64); ok && int(v) > 0 {
				if int(v) >= len(rows) {
					return nil
				}
				rows = rows[int(v):]
			}
		}
	}
	if m := strings.Index(lower, " limit "); m >= 0 {
		if n, err := shBindToken(lower[m+len(" limit "):]); err == nil {
			if v, ok := shArg(args, n).(int64); ok && int(v) < len(rows) {
				rows = rows[:int(v)]
			}
		}
	}
	return rows
}

func shColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func shArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("shState: no arg with ordinal %d", n))
}

func shEqual(a, b driver.Value) bool {
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

func shBindToken(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$") {
		return 0, fmt.Errorf("shState: expected a bindvar, got %q", s)
	}
	end := 1
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return strconv.Atoi(s[1:end])
}

// ---------- fixtures + server ----------

func newSettingsServer(t *testing.T) (*okapi.TestServer, *shState, *settings.Provider) {
	t.Helper()
	state := newShState()
	gdb := shGorm(t, state)
	h := NewSettingHandler(
		repositories.NewSettingRepository(gdb),
		audit.NewLogger(eventbus.New(repositories.NewEventRepository(gdb))),
	)
	h.SetHistoryRepo(repositories.NewSettingHistoryRepository(gdb))
	// A real provider over the same fake DB pins the invalidate-on-write
	// contract: without Invalidate the TTL cache would serve stale values.
	provider := settings.NewProvider(repositories.NewSettingRepository(gdb))
	h.SetSettingsProvider(provider)
	o := okapi.NewTestServer(t)
	o.Get("/admin/settings/history", okapi.H(h.GetSettingHistory))
	o.Post("/admin/settings/history/rollback", okapi.H(h.RollbackSetting))
	o.Put("/admin/settings", okapi.H(h.UpdateSettings))
	return o, state, provider
}

func doSettingsReq(t *testing.T, o *okapi.TestServer, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

// shActorBinding is the middleware stand-in: the real admin routes bind
// user_id and email from the JWT; the test server binds them directly.
var shActorBinding okapi.Middleware = func(c *okapi.Context) error {
	c.Set("user_id", 7)
	c.Set("email", "admin@example.com")
	return c.Next()
}

func newSettingsServerWithActor(t *testing.T) (*okapi.TestServer, *shState, *settings.Provider) {
	t.Helper()
	return newSettingsServerFromState(t, newShState())
}

// newSettingsServerFromState builds the server over a caller-prepared state
// (a test may seed settings before construction so the provider cache reads
// them at startup, as it would in production).
func newSettingsServerFromState(t *testing.T, state *shState) (*okapi.TestServer, *shState, *settings.Provider) {
	t.Helper()
	gdb := shGorm(t, state)
	h := NewSettingHandler(
		repositories.NewSettingRepository(gdb),
		audit.NewLogger(eventbus.New(repositories.NewEventRepository(gdb))),
	)
	h.SetHistoryRepo(repositories.NewSettingHistoryRepository(gdb))
	provider := settings.NewProvider(repositories.NewSettingRepository(gdb))
	h.SetSettingsProvider(provider)
	o := okapi.NewTestServer(t)
	g := o.Group("/admin", shActorBinding)
	g.Get("/settings/history", okapi.H(h.GetSettingHistory))
	g.Post("/settings/history/rollback", okapi.H(h.RollbackSetting))
	g.Put("/settings", okapi.H(h.UpdateSettings))
	return o, state, provider
}

func updateSettingViaAPI(t *testing.T, o *okapi.TestServer, key, value string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"settings":[{"key":%q,"value":%q,"type":"string"}]}`, key, value)
	return doSettingsReq(t, o, http.MethodPut, "/admin/settings", body)
}

func decodeSettingsResponse(t *testing.T, rec *httptest.ResponseRecorder) []models.Setting {
	t.Helper()
	var res struct {
		Success bool             `json:"success"`
		Data    []models.Setting `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode settings response %s: %v", rec.Body.String(), err)
	}
	return res.Data
}

type shHistoryRow struct {
	ID        uint
	Key       string
	OldValue  *string
	NewValue  string
	ChangedBy int64
}

func decodeHistoryResponse(t *testing.T, rec *httptest.ResponseRecorder) ([]shHistoryRow, int64) {
	t.Helper()
	var res struct {
		Success bool `json:"success"`
		Data    []struct {
			ID        uint    `json:"id"`
			Key       string  `json:"key"`
			OldValue  *string `json:"old_value"`
			NewValue  string  `json:"new_value"`
			ChangedBy int     `json:"changed_by"`
		} `json:"data"`
		Pageable struct {
			TotalElements int64 `json:"total_elements"`
		} `json:"pageable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode history response %s: %v", rec.Body.String(), err)
	}
	rows := make([]shHistoryRow, 0, len(res.Data))
	for _, d := range res.Data {
		rows = append(rows, shHistoryRow{
			ID:        d.ID,
			Key:       d.Key,
			OldValue:  d.OldValue,
			NewValue:  d.NewValue,
			ChangedBy: int64(d.ChangedBy),
		})
	}
	return rows, res.Pageable.TotalElements
}

// ---------- tests ----------

// TestSettingsUpdateCapturesPerKeyHistory pins the SF-100 diff contract: one
// history row per changed key with old → new and the actor; a key's first
// write records a nil old value; unchanged keys record nothing.
func TestSettingsUpdateCapturesPerKeyHistory(t *testing.T) {
	o, state, _ := newSettingsServerWithActor(t)

	shSeed(t, state, "settings", map[string]any{
		"id": uint(1), "key": "keep_me", "value": "same", "type": "string",
		"created_at": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	shSeed(t, state, "settings", map[string]any{
		"id": uint(2), "key": "changed_key", "value": "old-value", "type": "string",
		"created_at": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	body := `{"settings":[
		{"key":"keep_me","value":"same","type":"string"},
		{"key":"changed_key","value":"new-value","type":"string"},
		{"key":"brand_new_key","value":"first","type":"string"}
	]}`
	rec := doSettingsReq(t, o, http.MethodPut, "/admin/settings", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}

	entries := state.historyEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 history rows (changed + created), got %d: %+v", len(entries), entries)
	}
	byKey := map[string]map[string]driver.Value{}
	for _, e := range entries {
		byKey[e["key"].(string)] = e
	}
	ck, ok := byKey["changed_key"]
	if !ok {
		t.Fatalf("no history row for changed_key: %+v", entries)
	}
	if v, ok := ck["old_value"].(string); !ok || v != "old-value" {
		t.Errorf("changed_key old_value = %v, want old-value", ck["old_value"])
	}
	if ck["new_value"].(string) != "new-value" {
		t.Errorf("changed_key new_value = %v, want new-value", ck["new_value"])
	}
	if ck["changed_by"].(int64) != 7 {
		t.Errorf("changed_by = %v, want 7", ck["changed_by"])
	}
	if ck["changed_by_email"].(string) != "admin@example.com" {
		t.Errorf("changed_by_email = %v, want admin@example.com", ck["changed_by_email"])
	}

	nk := byKey["brand_new_key"]
	if nk == nil {
		t.Fatalf("no history row for brand_new_key: %+v", entries)
	}
	if nk["old_value"] != nil {
		t.Errorf("first write must record nil old_value, got %v", nk["old_value"])
	}
	if nk["new_value"].(string) != "first" {
		t.Errorf("brand_new_key new_value = %v, want first", nk["new_value"])
	}

	for _, e := range entries {
		if e["key"] == "keep_me" {
			t.Errorf("unchanged key must not produce a history row")
		}
	}
}

// TestSettingsUpdateInvalidatesProviderCache pins the cache half of SF-100:
// after a write the 30s-TTL provider serves the new value immediately, not a
// cycle later.
func TestSettingsUpdateInvalidatesProviderCache(t *testing.T) {
	// Seed BEFORE constructing the server so the provider's initial refresh
	// reads the row, exactly as a running server would.
	state := newShState()
	shSeed(t, state, "settings", map[string]any{
		"id": uint(1), "key": "smtp_timeout_seconds", "value": "30", "type": "int",
		"created_at": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	o, _, provider := newSettingsServerFromState(t, state)

	if got := provider.GetInt("smtp_timeout_seconds", 0); got != 30 {
		t.Fatalf("provider pre-read = %d, want 30", got)
	}

	rec := updateSettingViaAPI(t, o, "smtp_timeout_seconds", "45")
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	if got := provider.GetInt("smtp_timeout_seconds", 0); got != 45 {
		t.Errorf("provider after write = %d, want 45 (TTL cache must be invalidated on write)", got)
	}
}

// TestSettingsHistoryPagedGET pins the paged, newest-first GET contract.
func TestSettingsHistoryPagedGET(t *testing.T) {
	o, _, _ := newSettingsServerWithActor(t)

	// Three writes produce three history rows for the same key.
	for i, v := range []string{"a", "b", "c"} {
		if rec := updateSettingViaAPI(t, o, "retention_days", v); rec.Code != http.StatusOK {
			t.Fatalf("write %d = %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	rec := doSettingsReq(t, o, http.MethodGet, "/admin/settings/history?key=retention_days&page=0&size=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history = %d: %s", rec.Code, rec.Body.String())
	}
	page1, total := decodeHistoryResponse(t, rec)
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(page1) != 2 {
		t.Fatalf("page 1 size = %d, want 2", len(page1))
	}
	if page1[0].NewValue != "c" || page1[1].NewValue != "b" {
		t.Errorf("page 1 newest-first wrong: %+v", page1)
	}
	// The first write (oldest) has a nil old value.
	if page1[0].OldValue == nil {
		t.Errorf("newest entry (c) must carry old value b")
	}

	rec = doSettingsReq(t, o, http.MethodGet, "/admin/settings/history?key=retention_days&page=1&size=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history page 2 = %d: %s", rec.Code, rec.Body.String())
	}
	page2, _ := decodeHistoryResponse(t, rec)
	if len(page2) != 1 {
		t.Fatalf("page 2 size = %d, want 1: %+v", len(page2), page2)
	}
	if page2[0].NewValue != "a" {
		t.Errorf("page 2 wrong: %+v", page2)
	}
	if page2[0].OldValue != nil {
		t.Errorf("oldest entry must have nil old_value, got %v", *page2[0].OldValue)
	}
}

// TestSettingsRollbackRestoresAndReRecords pins the rollback contract: the
// prior value is restored through the normal setter, which appends a NEW
// history entry and invalidates the provider cache.
func TestSettingsRollbackRestoresAndReRecords(t *testing.T) {
	o, state, provider := newSettingsServerWithActor(t)

	if rec := updateSettingViaAPI(t, o, "max_batch_size", "100"); rec.Code != http.StatusOK {
		t.Fatalf("seed write = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := updateSettingViaAPI(t, o, "max_batch_size", "250"); rec.Code != http.StatusOK {
		t.Fatalf("change write = %d: %s", rec.Code, rec.Body.String())
	}
	if got := provider.GetInt("max_batch_size", 0); got != 250 {
		t.Fatalf("provider pre-rollback = %d, want 250", got)
	}

	rec := doSettingsReq(t, o, http.MethodPost, "/admin/settings/history/rollback", `{"key":"max_batch_size"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback = %d: %s", rec.Code, rec.Body.String())
	}
	settings := decodeSettingsResponse(t, rec)
	var val string
	for _, s := range settings {
		if s.Key == "max_batch_size" {
			val = s.Value
		}
	}
	if val != "100" {
		t.Errorf("rolled-back value = %q, want 100", val)
	}
	if got := provider.GetInt("max_batch_size", 0); got != 100 {
		t.Errorf("provider after rollback = %d, want 100 (rollback must invalidate the cache)", got)
	}

	entries := state.historyEntries()
	if len(entries) != 3 {
		t.Fatalf("rollback must append a new history entry: got %d rows, want 3: %+v", len(entries), entries)
	}
	last := entries[len(entries)-1]
	if last["old_value"].(string) != "250" || last["new_value"].(string) != "100" {
		t.Errorf("rollback history entry = %v → %v, want 250 → 100", last["old_value"], last["new_value"])
	}

	// The rollback is audited under its own action.
	found := false
	for _, ev := range state.eventPayloads() {
		if ev.Type == "settings.rolled_back" {
			found = true
			if !strings.Contains(ev.Metadata, `"history_id"`) {
				t.Errorf("rollback audit metadata missing history_id: %s", ev.Metadata)
			}
		}
	}
	if !found {
		t.Errorf("no settings.rolled_back audit event recorded: %+v", state.eventPayloads())
	}
}

// TestSettingsRollbackToSpecificHistoryEntry pins history_id selection.
func TestSettingsRollbackToSpecificHistoryEntry(t *testing.T) {
	o, state, _ := newSettingsServerWithActor(t)

	for _, v := range []string{"v1", "v2", "v3"} {
		if rec := updateSettingViaAPI(t, o, "allowed_signup_domains", v); rec.Code != http.StatusOK {
			t.Fatalf("write %s = %d: %s", v, rec.Code, rec.Body.String())
		}
	}
	entries := state.historyEntries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 history rows, got %d", len(entries))
	}
	// The first entry is the creation (nil → v1); rolling back to it must be
	// refused.
	targetID := entries[0]["id"].(int64)
	rec := doSettingsReq(t, o, http.MethodPost, "/admin/settings/history/rollback",
		fmt.Sprintf(`{"key":"allowed_signup_domains","history_id":%d}`, targetID))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("rollback to creation entry = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// The second entry (v1 → v2) restores v1 even though v3 is current.
	targetID = entries[1]["id"].(int64)
	rec = doSettingsReq(t, o, http.MethodPost, "/admin/settings/history/rollback",
		fmt.Sprintf(`{"key":"allowed_signup_domains","history_id":%d}`, targetID))
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback to entry 2 = %d: %s", rec.Code, rec.Body.String())
	}
	settings := decodeSettingsResponse(t, rec)
	var val string
	for _, s := range settings {
		if s.Key == "allowed_signup_domains" {
			val = s.Value
		}
	}
	if val != "v1" {
		t.Errorf("rolled-back value = %q, want v1", val)
	}
}

// TestSettingsRollbackRefusesCreationEntry pins the nil-old guard: the
// creation entry has no prior value, so rolling back to it is refused rather
// than silently deleting the key.
func TestSettingsRollbackRefusesCreationEntry(t *testing.T) {
	o, state, _ := newSettingsServerWithActor(t)

	if rec := updateSettingViaAPI(t, o, "brand_new_key", "only"); rec.Code != http.StatusOK {
		t.Fatalf("write = %d: %s", rec.Code, rec.Body.String())
	}
	if entries := state.historyEntries(); len(entries) != 1 {
		t.Fatalf("expected 1 creation entry, got %d", len(entries))
	}

	rec := doSettingsReq(t, o, http.MethodPost, "/admin/settings/history/rollback", `{"key":"brand_new_key"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("rollback of creation entry = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestSettingsRollbackOfUnknownKeyIs404 pins the missing-history guard.
func TestSettingsRollbackOfUnknownKeyIs404(t *testing.T) {
	o, _, _ := newSettingsServerWithActor(t)
	rec := doSettingsReq(t, o, http.MethodPost, "/admin/settings/history/rollback", `{"key":"never_written"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("rollback of unknown key = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestSettingsHistoryReservedKeyRejected keeps the app.* internals out of the
// history surface.
func TestSettingsHistoryReservedKeyRejected(t *testing.T) {
	o, _, _ := newSettingsServerWithActor(t)

	rec := doSettingsReq(t, o, http.MethodGet, "/admin/settings/history?key=app.upgrade_step", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("reserved key history = %d, want 400", rec.Code)
	}
	rec = doSettingsReq(t, o, http.MethodPost, "/admin/settings/history/rollback", `{"key":"app.upgrade_step"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("reserved key rollback = %d, want 400", rec.Code)
	}
}
