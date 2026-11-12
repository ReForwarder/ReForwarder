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
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The DMARC report handler runs through the real okapi stack (JWT middleware +
// workspace binding, the domain_signing_handler_test.go arrangement) against a
// fake database/sql driver holding the domains, dmarc_reports, events and
// domain_signing_keys tables. Assertions pin the wire contract — the summary
// aggregate, newest-first records, workspace isolation (a row for the same
// domain in another workspace must never leak) — with the audit trail read
// straight out of the fake events table. The fake is shared with
// domain_health_handler_test.go.

const dmrTestWorkspace = "3"

var dmrColumns = map[string][]string{
	// pool_id: OB-6 W3 additive column (domains.pool_id) — Save() on the model
	// emits it in every UPDATE's SET list, so the fake must know it.
	// max_message_size: SF-17 additive column (domains.max_message_size) —
	// Save() on the model emits it in every UPDATE's SET list, like pool_id.
	"domains": {"id", "user_id", "workspace_id", "domain", "ownership_verified", "spf_verified",
		"dkim_verified", "dmarc_verified", "verification_token", "created_at",
		"delivery_mode", "outbound_bind_ip", "max_msgs_per_connection", "hourly_budget", "pool_id",
		"mta_sts_mode", "max_message_size"},
	"dmarc_reports": {"id", "user_id", "workspace_id", "domain_id", "inbound_id", "domain",
		"reporter", "reporter_email", "report_id", "source_ip", "window_start", "window_end",
		"count", "dkim_aligned", "spf_aligned", "disposition", "header_from", "created_at"},
	"domain_signing_keys": {"id", "user_id", "workspace_id", "domain_id", "selector", "algorithm",
		"private_key_pem", "status", "dns_state", "dns_published_at", "created_at", "updated_at"},
	"events": {"id", "category", "type", "workspace_id", "actor_id", "actor_name", "client_ip",
		"message", "metadata", "outcome", "target_type", "target_id", "created_at"},
}

var dmrDefaults = map[string]map[string]func() driver.Value{
	"domains": {
		"ownership_verified": func() driver.Value { return false },
		"spf_verified":       func() driver.Value { return false },
		"dkim_verified":      func() driver.Value { return false },
		"dmarc_verified":     func() driver.Value { return false },
		"workspace_id":       func() driver.Value { return nil },
		"verification_token": func() driver.Value { return "tok" },
		"delivery_mode":      func() driver.Value { return "relay" },
		"outbound_bind_ip":   func() driver.Value { return "" },
		"max_msgs_per_connection": func() driver.Value { return int64(0) },
		"hourly_budget":      func() driver.Value { return int64(0) },
		"max_message_size":   func() driver.Value { return int64(0) },
	},
	"dmarc_reports": {
		"workspace_id":   func() driver.Value { return nil },
		"domain_id":      func() driver.Value { return nil },
		"inbound_id":     func() driver.Value { return int64(0) },
		"reporter":       func() driver.Value { return "" },
		"reporter_email": func() driver.Value { return "" },
		"window_start":   func() driver.Value { return time.Time{} },
		"window_end":     func() driver.Value { return time.Time{} },
		"count":          func() driver.Value { return int64(0) },
		"dkim_aligned":   func() driver.Value { return false },
		"spf_aligned":    func() driver.Value { return false },
		"disposition":    func() driver.Value { return "none" },
		"header_from":    func() driver.Value { return "" },
		"created_at":     func() driver.Value { return time.Time{} },
	},
	"domain_signing_keys": {
		"workspace_id":     func() driver.Value { return nil },
		"dns_state":        func() driver.Value { return models.DomainSigningDNSPending },
		"dns_published_at": func() driver.Value { return nil },
		"algorithm":        func() driver.Value { return models.SigningAlgorithmRSA },
		"status":           func() driver.Value { return models.DomainSigningStatusActive },
	},
}

type dmrTable struct {
	columns []string
	rows    [][]driver.Value
}

type dmrState struct {
	mu     sync.Mutex
	tables map[string]*dmrTable
	seq    int64
}

func newDmrState() *dmrState {
	tables := make(map[string]*dmrTable, len(dmrColumns))
	for name, cols := range dmrColumns {
		tables[name] = &dmrTable{columns: cols}
	}
	return &dmrState{tables: tables, seq: 700}
}

func (s *dmrState) colIndex(table, col string) int {
	for i, c := range dmrColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

// dmrSeed inserts a fixture row; unspecified columns take their defaults.
func dmrSeed(t *testing.T, s *dmrState, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := dmrColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := dmrDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for col, v := range vals {
		idx := s.colIndex(table, col)
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, col)
		}
		row[idx] = dmrVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func dmrVal(v any) driver.Value {
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
	case *uint:
		if t == nil {
			return nil
		}
		return int64(*t)
	default:
		panic(fmt.Sprintf("dmrVal: unsupported type %T", v))
	}
}

// rowsOf returns the rows of a table as column maps (for assertions).
func (s *dmrState) rowsOf(table string) []map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, 0, len(s.tables[table].rows))
	for _, row := range s.tables[table].rows {
		m := map[string]driver.Value{}
		for i, c := range dmrColumns[table] {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

// eventTypes lists the persisted audit event types.
func (s *dmrState) eventTypes() []string {
	var out []string
	for _, m := range s.rowsOf("events") {
		if t, ok := m["type"].(string); ok {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func dmrGorm(t *testing.T, state *dmrState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(dmrConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open dmarc fake gorm: %v", err)
	}
	return gdb
}

type dmrConnector struct{ state *dmrState }

func (c dmrConnector) Connect(context.Context) (driver.Conn, error) { return &dmrConn{s: c.state}, nil }
func (c dmrConnector) Driver() driver.Driver                        { return dmrDrv{} }

type dmrDrv struct{}

func (dmrDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type dmrConn struct{ s *dmrState }

func (c *dmrConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *dmrConn) Close() error                        { return nil }
func (c *dmrConn) Begin() (driver.Tx, error)           { return dmrTx{}, nil }

type dmrTx struct{}

func (dmrTx) Commit() error   { return nil }
func (dmrTx) Rollback() error { return nil }

type dmrResult struct{ rows int64 }

func (r dmrResult) LastInsertId() (int64, error) { return 0, nil }
func (r dmrResult) RowsAffected() (int64, error) { return r.rows, nil }

type dmrRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *dmrRows) Columns() []string { return r.cols }
func (r *dmrRows) Close() error      { return nil }
func (r *dmrRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func dmrTableOf(lower string) string {
	for _, name := range []string{"domain_signing_keys", "dmarc_reports", "domains", "events"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`, `join ` + name} {
			if strings.Contains(lower, pat) {
				return name
			}
		}
	}
	return ""
}

func (c *dmrConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := dmrTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("dmrState: cannot route query: %s", query)
	}
	if strings.Contains(lower, "insert into") {
		return c.handleInsert(lower, table, query, args)
	}
	// The domain DMARC summary is a single SQL aggregate over the workspace-
	// filtered rows; compute what Postgres would return for it.
	if table == "dmarc_reports" && strings.Contains(lower, "record_count") {
		return c.handleSummary(table, lower, args)
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	// Emulate the ORDER BY the repository relies on: the Recent query must
	// return newest-window-first (id as the tiebreaker).
	if table == "dmarc_reports" && strings.Contains(lower, "order by window_end desc, id desc") {
		weIdx := c.s.colIndex(table, "window_end")
		idIdx := c.s.colIndex(table, "id")
		sort.SliceStable(matched, func(i, j int) bool {
			wa, _ := matched[i][weIdx].(time.Time)
			wb, _ := matched[j][weIdx].(time.Time)
			if !wa.Equal(wb) {
				return wa.After(wb)
			}
			ia, _ := matched[i][idIdx].(int64)
			ib, _ := matched[j][idIdx].(int64)
			return ia > ib
		})
	}
	if m := strings.Index(lower, " limit "); m >= 0 {
		if n, err := dmrBindToken(lower[m+len(" limit "):]); err == nil {
			if v, ok := dmrArg(args, n).(int64); ok && int(v) < len(matched) {
				matched = matched[:int(v)]
			}
		}
	}
	cols, err := dmrSelectColumns(table, lower)
	if err != nil {
		return nil, err
	}
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		if cols == nil {
			rows[i] = append([]driver.Value(nil), r...)
			continue
		}
		projected := make([]driver.Value, 0, len(cols))
		for _, col := range cols {
			projected = append(projected, r[c.s.colIndex(table, col)])
		}
		rows[i] = projected
	}
	if cols == nil {
		cols = dmrColumns[table]
	}
	return &dmrRows{cols: cols, rows: rows}, nil
}

// dmrSelectColumns extracts the projected column list of a plain SELECT;
// nil means "SELECT *" (full width). Aggregates are routed elsewhere before
// this is consulted, so a "(" in the list is rejected outright.
func dmrSelectColumns(table, lower string) ([]string, error) {
	s := strings.Index(lower, "select ")
	f := strings.Index(lower, " from ")
	if s < 0 || f < 0 || f <= s {
		return nil, nil
	}
	list := strings.TrimSpace(lower[s+len("select ") : f])
	if list == "*" || list == "" || strings.Contains(list, "(") {
		return nil, nil
	}
	parts := strings.Split(list, ",")
	cols := make([]string, 0, len(parts))
	for _, p := range parts {
		col := dmrColumnOf(p)
		if dmrColIndex(table, col) < 0 {
			return nil, fmt.Errorf("dmrState: select list names unknown column %q of %s", col, table)
		}
		cols = append(cols, col)
	}
	return cols, nil
}

func dmrColIndex(table, col string) int {
	for i, c := range dmrColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

// handleSummary answers the DMARC aggregate: one row with the aliased
// aggregates over the rows the WHERE clause matches, mirroring the SELECT in
// DMARCReportRepository.DomainSummary.
func (c *dmrConn) handleSummary(table, lower string, args []driver.NamedValue) (driver.Rows, error) {
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	cntIdx := c.s.colIndex(table, "count")
	dkimIdx := c.s.colIndex(table, "dkim_aligned")
	spfIdx := c.s.colIndex(table, "spf_aligned")
	dispIdx := c.s.colIndex(table, "disposition")
	weIdx := c.s.colIndex(table, "window_end")

	var messages, dkim, spf, none, quarantine, reject int64
	var latest *time.Time
	for _, row := range matched {
		n, _ := row[cntIdx].(int64)
		messages += n
		if b, _ := row[dkimIdx].(bool); b {
			dkim += n
		}
		if b, _ := row[spfIdx].(bool); b {
			spf += n
		}
		switch d, _ := row[dispIdx].(string); d {
		case "none":
			none++
		case "quarantine":
			quarantine++
		case "reject":
			reject++
		}
		if we, ok := row[weIdx].(time.Time); ok {
			t := we
			if latest == nil || t.After(*latest) {
				latest = &t
			}
		}
	}
	cols := []string{"record_count", "message_count", "dkim_aligned", "spf_aligned",
		"disp_none", "disp_quarantine", "disp_reject", "latest_window_end"}
	return &dmrRows{cols: cols, rows: [][]driver.Value{{
		int64(len(matched)), messages, dkim, spf, none, quarantine, reject, latest,
	}}}, nil
}

func (c *dmrConn) handleInsert(lower, table, query string, args []driver.NamedValue) (driver.Rows, error) {
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("dmrState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, fmt.Errorf("dmrState: insert without a column list")
	}
	colParts := strings.Split(query[open+1:close], ",")
	valText := query[vi+len(" values "):]
	end := strings.LastIndex(valText, ")")
	valParts := strings.Split(valText[1:end], ",")
	if len(valParts) != len(colParts) {
		return nil, fmt.Errorf("dmrState: %d columns but %d values", len(colParts), len(valParts))
	}
	vals := make([]driver.Value, len(valParts))
	for i, p := range valParts {
		n, err := strconv.Atoi(strings.TrimSpace(p)[1:])
		if err != nil {
			return nil, err
		}
		vals[i] = dmrArg(args, n)
	}

	var returning []string
	if i := strings.Index(lower, " returning "); i >= 0 {
		for _, cn := range strings.Split(lower[i+len(" returning "):], ",") {
			returning = append(returning, strings.Trim(strings.TrimSpace(cn), `"`))
		}
	}

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := dmrColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := dmrDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for j, col := range colParts {
		col = strings.Trim(strings.TrimSpace(col), `"`)
		idx := s.colIndex(table, col)
		if idx < 0 {
			return nil, fmt.Errorf("dmrState: %s has no column %q", table, col)
		}
		row[idx] = vals[j]
	}
	s.seq++
	row[s.colIndex(table, "id")] = s.seq
	s.tables[table].rows = append(s.tables[table].rows, row)

	out := make([]driver.Value, len(returning))
	for i, cn := range returning {
		out[i] = row[s.colIndex(table, cn)]
	}
	return &dmrRows{cols: returning, rows: [][]driver.Value{out}}, nil
}

func (c *dmrConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := dmrTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("dmrState: unsupported exec: %s", query)
	}
	if !strings.Contains(lower, "update ") {
		return nil, fmt.Errorf("dmrState: unsupported exec: %s", query)
	}
	return c.handleUpdate(table, lower, args)
}

func (c *dmrConn) handleUpdate(table, lower string, args []driver.NamedValue) (driver.Result, error) {
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("dmrState: malformed update: %s", lower)
	}
	type setter struct {
		idx int
		val driver.Value
	}
	var setters []setter
	for _, pair := range strings.Split(lower[setStart+len(" set "):whereStart], ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("dmrState: malformed SET pair %q", pair)
		}
		idx := c.s.colIndex(table, dmrColumnOf(pair[:eq]))
		if idx < 0 {
			return nil, fmt.Errorf("dmrState: unknown column in SET %q", pair)
		}
		tok := strings.TrimSpace(pair[eq+1:])
		if !strings.HasPrefix(tok, "$") {
			return nil, fmt.Errorf("dmrState: unsupported SET expression %q", tok)
		}
		n, _ := strconv.Atoi(tok[1:])
		setters = append(setters, setter{idx: idx, val: dmrArg(args, n)})
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
	return dmrResult{rows: int64(len(matched))}, nil
}

func (s *dmrState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
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
		switch {
		case strings.HasSuffix(cond, " is null"):
			idx := s.colIndex(table, dmrColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
		default:
			eq := strings.Index(cond, " = ")
			if eq < 0 {
				return nil, fmt.Errorf("dmrState: unsupported condition %q", cond)
			}
			idx := s.colIndex(table, dmrColumnOf(cond[:eq]))
			n, err := strconv.Atoi(strings.TrimSpace(cond[eq+3:])[1:])
			if err != nil {
				return nil, fmt.Errorf("dmrState: bad bindvar in %q", cond)
			}
			want := dmrArg(args, n)
			preds = append(preds, func(row []driver.Value) bool { return dmrEqual(row[idx], want) })
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

func dmrColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func dmrArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("dmrState: no arg with ordinal %d", n))
}

func dmrEqual(a, b driver.Value) bool {
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

// dmrBindToken parses a leading `$N` bindvar token.
func dmrBindToken(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$") {
		return 0, fmt.Errorf("dmrState: expected a bindvar, got %q", s)
	}
	end := 1
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return strconv.Atoi(s[1:end])
}

// ---------- fixtures + server ----------

// dmrWorkspaceBinding plays the workspace middleware: it binds the request to
// the workspace named by X-Test-Workspace and to the role named by
// X-Test-Workspace-Role (admin unless stated). The separate role header lets
// the health tests exercise the edit gate that the shared binding above
// always satisfies.
var dmrWorkspaceBinding okapi.Middleware = func(c *okapi.Context) error {
	if ws := c.Request().Header.Get("X-Test-Workspace"); ws != "" {
		c.Set(middlewares.CtxWorkspaceID, ws)
		role := c.Request().Header.Get("X-Test-Workspace-Role")
		if role == "" {
			role = string(models.WorkspaceRoleAdmin)
		}
		c.Set(middlewares.CtxWorkspaceRole, role)
	}
	return c.Next()
}

// dmrSeedDomain seeds a domain owned by the given scope.
func dmrSeedDomain(t *testing.T, s *dmrState, id uint, workspaceID *uint, userID uint, domain string) {
	t.Helper()
	vals := map[string]any{
		"id": id, "user_id": userID, "domain": domain,
		"verification_token": "posta-verification-token",
		"created_at":         time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
	}
	if workspaceID != nil {
		vals["workspace_id"] = workspaceID
	}
	dmrSeed(t, s, "domains", vals)
}

// dmrSeedReport seeds one DMARC report record row.
func dmrSeedReport(t *testing.T, s *dmrState, vals map[string]any) {
	t.Helper()
	dmrSeed(t, s, "dmarc_reports", vals)
}

func newDMARCServer(t *testing.T) (*okapi.TestServer, *dmrState) {
	t.Helper()
	state := newDmrState()
	gdb := dmrGorm(t, state)
	h := NewDMARCReportHandler(
		repositories.NewDMARCReportRepository(gdb),
		repositories.NewDomainRepository(gdb),
		audit.NewLogger(eventbus.New(repositories.NewEventRepository(gdb))),
	)
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		dmrWorkspaceBinding,
	)
	g.Get("/domains/{id:int}/dmarc", okapi.H(h.Get))
	return o, state
}

func doDMARCReq(t *testing.T, o *okapi.TestServer, method, path, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
	req.Header.Set("X-Test-Workspace", dmrTestWorkspace)
	if role != "" {
		req.Header.Set("X-Test-Workspace-Role", role)
	}
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

func decodeDMARCResponse(t *testing.T, rec *httptest.ResponseRecorder) DomainDMARCResponse {
	t.Helper()
	var res struct {
		Success bool                `json:"success"`
		Data    DomainDMARCResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response %s: %v", rec.Body.String(), err)
	}
	return res.Data
}

// ---------- tests ----------

// The summary aggregates only the caller's workspace: a row for the SAME
// domain in a foreign workspace must be excluded from every number, and the
// records list is newest-window-first.
func TestDMARCGetSummary(t *testing.T) {
	o, state := newDMARCServer(t)
	dmrSeedDomain(t, state, 10, wsID(3), 7, "example.com")

	base := map[string]any{
		"user_id": uint(7), "workspace_id": wsID(3), "domain_id": uint(10),
		"domain": "example.com", "reporter": "google.com", "reporter_email": "noreply-dmarc@google.com",
		"header_from": "example.com",
	}
	dmrSeedReport(t, state, mergeMap(base, map[string]any{
		"report_id": "r1", "source_ip": "198.51.100.1",
		"window_start": time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		"window_end":   time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		"count":        int64(60), "dkim_aligned": true, "spf_aligned": true, "disposition": "none",
	}))
	dmrSeedReport(t, state, mergeMap(base, map[string]any{
		"report_id": "r2", "source_ip": "198.51.100.2", "reporter": "outlook.com",
		"window_start": time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		"window_end":   time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
		"count":        int64(20), "dkim_aligned": true, "spf_aligned": false, "disposition": "quarantine",
	}))
	dmrSeedReport(t, state, mergeMap(base, map[string]any{
		"report_id": "r3", "source_ip": "198.51.100.3", "reporter": "google.com",
		"window_start": time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
		"window_end":   time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC),
		"count":        int64(20), "dkim_aligned": false, "spf_aligned": false, "disposition": "reject",
	}))
	// Same domain_id, foreign workspace: the workspace filter must exclude it
	// from the summary AND the records, or one tenant would read another's
	// authentication record.
	dmrSeedReport(t, state, mergeMap(base, map[string]any{
		"user_id": uint(42), "workspace_id": wsID(99),
		"report_id": "r4", "source_ip": "198.51.100.4",
		"window_start": time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC),
		"window_end":   time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
		"count":        int64(999), "dkim_aligned": true, "spf_aligned": true, "disposition": "none",
	}))

	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/10/dmarc", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("dmarc get = %d: %s", rec.Code, rec.Body.String())
	}
	data := decodeDMARCResponse(t, rec)
	s := data.Summary
	if s.RecordCount != 3 {
		t.Fatalf("record_count = %d, want 3", s.RecordCount)
	}
	if s.MessageCount != 100 {
		t.Fatalf("message_count = %d, want 100 (60+20+20, foreign 999 excluded)", s.MessageCount)
	}
	if s.DKIMAlignedMessages != 80 || s.SPFAlignedMessages != 60 {
		t.Fatalf("aligned messages = %d/%d, want 80/60", s.DKIMAlignedMessages, s.SPFAlignedMessages)
	}
	if s.DKIMAlignment != 0.8 || s.SPFAlignment != 0.6 {
		t.Fatalf("alignment ratios = %f/%f, want 0.8/0.6", s.DKIMAlignment, s.SPFAlignment)
	}
	if s.DispositionNone != 1 || s.DispositionQuarantine != 1 || s.DispositionReject != 1 {
		t.Fatalf("disposition breakdown = %d/%d/%d, want 1/1/1",
			s.DispositionNone, s.DispositionQuarantine, s.DispositionReject)
	}
	wantLatest := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	if s.LatestWindowEnd == nil || !s.LatestWindowEnd.Equal(wantLatest) {
		t.Fatalf("latest_window_end = %v, want %v (the foreign row's newer window must not leak)",
			s.LatestWindowEnd, wantLatest)
	}
	if len(data.Records) != 3 {
		t.Fatalf("records = %d, want 3 (foreign row excluded)", len(data.Records))
	}
	// Newest window first, newest record id breaking ties.
	wantOrder := []string{"r3", "r2", "r1"}
	for i, want := range wantOrder {
		if data.Records[i].ReportID != want {
			t.Fatalf("records[%d].report_id = %q, want %q (newest first)", i, data.Records[i].ReportID, want)
		}
	}
	if data.Records[0].SourceIP != "198.51.100.3" || data.Records[0].Count != 20 {
		t.Fatalf("records[0] payload wrong: %+v", data.Records[0])
	}
	if got := state.eventTypes(); len(got) != 1 || got[0] != "domain.dmarc.viewed" {
		t.Fatalf("access must write one audit event, got %v", got)
	}
}

// A domain outside the caller's scope is indistinguishable from a missing one,
// and a denied read leaves no audit event behind.
func TestDMARCGetForeignDomainNotFound(t *testing.T) {
	o, state := newDMARCServer(t)
	dmrSeedDomain(t, state, 10, wsID(99), 42, "example.com")

	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/10/dmarc", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign domain = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Success {
		t.Fatal("error envelope must carry success=false")
	}

	rec = doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/999/dmarc", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing domain = %d, want 404", rec.Code)
	}
	if got := state.eventTypes(); len(got) != 0 {
		t.Fatalf("denied reads must not write audit events, got %v", got)
	}
	if rows := state.rowsOf("dmarc_reports"); len(rows) != 0 {
		t.Fatalf("a denied read must not create report rows: %+v", rows)
	}
}

// No stored reports: the summary is the zero value with a null latest window,
// and records serializes as [] rather than null.
func TestDMARCGetEmptySummary(t *testing.T) {
	o, state := newDMARCServer(t)
	dmrSeedDomain(t, state, 10, wsID(3), 7, "example.com")

	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/10/dmarc", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty dmarc get = %d: %s", rec.Code, rec.Body.String())
	}
	data := decodeDMARCResponse(t, rec)
	if data.Summary.RecordCount != 0 || data.Summary.MessageCount != 0 ||
		data.Summary.DKIMAlignment != 0 || data.Summary.SPFAlignment != 0 ||
		data.Summary.DispositionNone != 0 || data.Summary.DispositionQuarantine != 0 ||
		data.Summary.DispositionReject != 0 {
		t.Fatalf("empty summary must be zeroed: %+v", data.Summary)
	}
	if data.Summary.LatestWindowEnd != nil {
		t.Fatalf("latest_window_end = %v, want nil", data.Summary.LatestWindowEnd)
	}
	if !strings.Contains(rec.Body.String(), `"records":[]`) {
		t.Fatalf("records must serialize as [], got: %s", rec.Body.String())
	}
	// The access itself is still audit-logged.
	if got := state.eventTypes(); len(got) != 1 || got[0] != "domain.dmarc.viewed" {
		t.Fatalf("empty access must write one audit event, got %v", got)
	}
}

func mergeMap(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
