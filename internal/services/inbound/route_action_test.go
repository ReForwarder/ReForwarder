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

package inbound

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/security"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OB-2 routing integration tests: RCPT-time validation, the explicit
// unknown-recipient reject, post-ingest action execution (forward queue row,
// webhook dispatch, quarantine seam), idempotency on re-ingest, workspace
// isolation and the audit events. Everything runs against the fake
// database/sql driver (the verify_hook_test.go pattern) — the only gorm
// statements issued here are the ones the real repos issue.

// ---------- fake driver state ----------

type raState struct {
	mu    sync.Mutex
	seq   int64
	table map[string][]map[string]driver.Value
	// failRoutes forces route-store reads to fail (temporary-failure path).
	failRoutes bool
}

func newRaState() *raState {
	return &raState{seq: 100, table: map[string][]map[string]driver.Value{}}
}

func (s *raState) addDomain(id uint, userID uint, wsID *uint, name string, verified bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := map[string]driver.Value{
		"id":                 int64(id),
		"user_id":            int64(userID),
		"domain":             name,
		"ownership_verified": verified,
		"created_at":         time.Now().UTC(),
	}
	if wsID != nil {
		row["workspace_id"] = int64(*wsID)
	}
	s.table["domains"] = append(s.table["domains"], row)
}

// addRoute seeds an inbound_routes row directly (the repo's Create is a
// trivial db.Create; the selects are what the engine depends on).
func (s *raState) addRoute(id, domainID, userID uint, wsID *uint, pattern string, action models.InboundRouteAction, priority int, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := map[string]driver.Value{
		"id":         int64(id),
		"user_id":    int64(userID),
		"domain_id":  int64(domainID),
		"pattern":    pattern,
		"action":     string(action),
		"priority":   int64(priority),
		"enabled":    enabled,
		"created_at": time.Now().UTC(),
		"updated_at": time.Now().UTC(),
	}
	if wsID != nil {
		row["workspace_id"] = int64(*wsID)
	}
	s.table["inbound_routes"] = append(s.table["inbound_routes"], row)
}

// setRouteTarget sets the target column of a seeded route (routes are seeded
// with empty targets).
func (s *raState) setRouteTarget(id uint, target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.table["inbound_routes"] {
		if row["id"] == int64(id) {
			row["target"] = target
		}
	}
}

func (s *raState) rows(t *testing.T, table string) []map[string]driver.Value {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, len(s.table[table]))
	copy(out, s.table[table])
	return out
}

func (s *raState) rowCount(t *testing.T, table string) int {
	t.Helper()
	return len(s.rows(t, table))
}

func (s *raState) failRouteReads() {
	s.mu.Lock()
	s.failRoutes = true
	s.mu.Unlock()
}

// raGorm opens gorm over the fake driver.
func raGorm(t *testing.T, state *raState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(raConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open routing fake gorm: %v", err)
	}
	return gdb
}

type raConnector struct{ state *raState }

func (c raConnector) Connect(context.Context) (driver.Conn, error) { return &raConn{s: c.state}, nil }
func (c raConnector) Driver() driver.Driver                        { return raDrv{} }

type raDrv struct{}

func (raDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type raConn struct{ s *raState }

func (c *raConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *raConn) Close() error                        { return nil }
func (c *raConn) Begin() (driver.Tx, error)           { return raTx{}, nil }

type raTx struct{}

func (raTx) Commit() error   { return nil }
func (raTx) Rollback() error { return nil }

func (c *raConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := raNormalize(query)
	switch {
	case strings.Contains(lower, `update "inbound_emails"`),
		strings.Contains(lower, `update "inbound_routes"`),
		strings.Contains(lower, `update "inbound_forward_tasks"`):
		return c.execUpdate(lower, args)
	case strings.Contains(lower, `delete from "inbound_routes"`):
		return c.execDelete(lower, args)
	default:
		if strings.Contains(lower, "insert into") {
			// Some gorm paths exec inserts; count returned rows.
			rows, err := c.QueryContext(ctx, query, args)
			if err != nil {
				return nil, err
			}
			var n int64
			for {
				if err := rows.Next([]driver.Value{make([]driver.Value, len(rows.Columns()))}); err != nil {
					break
				}
				n++
			}
			_ = rows.Close()
			return raResult{affected: n}, nil
		}
		return nil, fmt.Errorf("raState: unsupported exec: %s", query)
	}
}

func (c *raConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := raNormalize(query)
	switch {
	case strings.Contains(lower, "insert into"):
		return c.insert(query, args)
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "domains"`):
		return c.selectWhere(lower, args, "domains")
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "inbound_routes"`):
		if c.s.failRoutes {
			return nil, errors.New("route store unavailable")
		}
		return c.selectWhere(lower, args, "inbound_routes")
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "inbound_emails"`):
		return c.selectWhere(lower, args, "inbound_emails")
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "inbound_forward_tasks"`):
		return c.selectWhere(lower, args, "inbound_forward_tasks")
	default:
		return nil, fmt.Errorf("raState: cannot route query: %s", query)
	}
}

// raNormalize lower-cases and collapses whitespace for clause matching.
func raNormalize(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

// raReturningDefaults are the DB-side defaults gorm reads back via RETURNING
// for columns it omitted from the INSERT (zero-valued fields carrying a
// default tag).
var raReturningDefaults = map[string]driver.Value{
	"retry_count":  int64(0),
	"attempts":     int64(0),
	"priority":     int64(0),
	"route_action": "",
	"target":       "",
}

// insert handles gorm's single-tuple INSERT ... [ON CONFLICT DO NOTHING]
// RETURNING ...: stores the row and answers the RETURNING columns. The
// forwarding queue deduplicates on inbound_email_id (unique index).
func (c *raConn) insert(query string, args []driver.NamedValue) (driver.Rows, error) {
	table, cols, tuples, err := raParseInsert(query, args)
	if err != nil {
		return nil, err
	}
	ret := raReturningOf(query)
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	conflict := strings.Contains(raNormalize(query), "on conflict")
	var out [][]driver.Value
	for _, vals := range tuples {
		row := map[string]driver.Value{}
		for j, col := range cols {
			row[col] = raCanonical(vals[j])
		}
		if conflict {
			dup := false
			for _, existing := range s.table[table] {
				if fmt.Sprint(existing["inbound_email_id"]) == fmt.Sprint(row["inbound_email_id"]) {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
		}
		s.seq++
		row["id"] = s.seq
		for _, col := range ret {
			if _, ok := row[col]; ok {
				continue
			}
			switch col {
			case "id":
				row[col] = s.seq
			case "uuid":
				row[col] = fmt.Sprintf("inb-fake-%d", s.seq)
			default:
				row[col] = raReturningDefaults[col]
			}
		}
		s.table[table] = append(s.table[table], row)
		out = append(out, raProject(row, ret))
	}
	return &raRows{cols: ret, rows: out}, nil
}

func raReturningOf(query string) []string {
	lower := strings.ToLower(query)
	i := strings.Index(lower, "returning")
	if i < 0 {
		return nil
	}
	var cols []string
	for _, p := range strings.Split(query[i+len("returning"):], ",") {
		p = strings.Trim(strings.TrimSpace(p), `"`)
		if p != "" {
			cols = append(cols, p)
		}
	}
	return cols
}

func raProject(row map[string]driver.Value, cols []string) []driver.Value {
	out := make([]driver.Value, len(cols))
	for i, c := range cols {
		out[i] = row[c]
	}
	return out
}

// raParseInsert extracts the table, column list and bound tuple values of the
// single-row INSERT gorm issues (bindvars are $N tokens only).
func raParseInsert(query string, args []driver.NamedValue) (string, []string, [][]driver.Value, error) {
	lower := strings.ToLower(query)
	ni := strings.Index(lower, "insert into")
	if ni < 0 {
		return "", nil, nil, fmt.Errorf("raState: not an insert: %s", query)
	}
	rest := strings.TrimSpace(query[ni+len("insert into"):])
	end := strings.Index(rest, "(")
	if end < 0 {
		return "", nil, nil, fmt.Errorf("raState: insert without columns: %s", query)
	}
	table := strings.Trim(strings.TrimSpace(rest[:end]), `"`)
	vi := strings.Index(lower, "values (")
	if vi < 0 {
		return table, nil, nil, fmt.Errorf("raState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return table, nil, nil, fmt.Errorf("raState: insert without a column list: %s", query)
	}
	var cols []string
	for _, p := range strings.Split(query[open+1:close], ",") {
		cols = append(cols, strings.Trim(strings.TrimSpace(p), `"`))
	}

	valText := query[vi+len(" values "):]
	if i := strings.Index(strings.ToLower(valText), " on conflict"); i >= 0 {
		valText = valText[:i]
	}
	if i := strings.Index(strings.ToLower(valText), " returning"); i >= 0 {
		valText = valText[:i]
	}
	valText = strings.TrimSpace(valText)
	valText = strings.TrimSuffix(strings.TrimPrefix(valText, "("), ")")

	var tuples [][]driver.Value
	for _, tuple := range strings.Split(valText, "),(") {
		parts := strings.Split(tuple, ",")
		if len(parts) != len(cols) {
			return table, nil, nil, fmt.Errorf("raState: %d columns but %d values in %q", len(cols), len(parts), tuple)
		}
		vals := make([]driver.Value, len(parts))
		for i, p := range parts {
			p = strings.TrimSpace(p)
			if !strings.HasPrefix(p, "$") {
				return table, nil, nil, fmt.Errorf("raState: non-bindvar value %q", p)
			}
			n, err := strconv.Atoi(p[1:])
			if err != nil {
				return table, nil, nil, err
			}
			found := false
			for _, a := range args {
				if a.Ordinal == n {
					vals[i] = a.Value
					found = true
					break
				}
			}
			if !found {
				return table, nil, nil, fmt.Errorf("raState: no arg with ordinal %d", n)
			}
		}
		tuples = append(tuples, vals)
	}
	return table, cols, tuples, nil
}

// selectWhere answers the repo SELECTs: a WHERE of `col = $n` /
// `lower(col) = lower($n)` conjunctions (plus `<=` for due-task lookups) and
// an optional ORDER BY. LIMIT is ignored — every caller either uses First
// over a unique match or wants the full ordered scan.
func (c *raConn) selectWhere(lower string, args []driver.NamedValue, table string) (driver.Rows, error) {
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = lower[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit ", " returning "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}

	s := c.s
	s.mu.Lock()
	matched := make([]map[string]driver.Value, 0, len(s.table[table]))
	for _, row := range s.table[table] {
		ok := true
		if strings.TrimSpace(where) != "" {
			for _, term := range strings.Split(where, " and ") {
				if !raMatchTerm(row, strings.TrimSpace(term), args) {
					ok = false
					break
				}
			}
		}
		if ok {
			matched = append(matched, row)
		}
	}
	orderBy := ""
	if i := strings.Index(lower, " order by "); i >= 0 {
		rest := lower[i+len(" order by "):]
		if j := strings.Index(rest, " limit "); j >= 0 {
			rest = rest[:j]
		}
		orderBy = rest
	}
	s.mu.Unlock()

	// Aggregate counts answer a single "count" column.
	if strings.Contains(lower, "count(*)") {
		return &raRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}

	if orderBy != "" {
		keys := strings.Split(orderBy, ",")
		sort.SliceStable(matched, func(a, b int) bool {
			for _, k := range keys {
				f := strings.Fields(strings.TrimSpace(k))
				if len(f) == 0 {
					continue
				}
				col := strings.Trim(f[0], `"`)
				if i := strings.LastIndex(col, "."); i >= 0 {
					col = col[i+1:]
				}
				desc := len(f) > 1 && strings.HasPrefix(f[1], "desc")
				cmp := raCompareVal(matched[a][col], matched[b][col])
				if cmp == 0 {
					continue
				}
				if desc {
					return cmp > 0
				}
				return cmp < 0
			}
			return false
		})
	}

	cols := make([]string, 0)
	rows := make([][]driver.Value, 0, len(matched))
	if len(matched) > 0 {
		for c := range matched[0] {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		for _, row := range matched {
			rows = append(rows, raProject(row, cols))
		}
	}
	return &raRows{cols: cols, rows: rows}, nil
}

// raMatchTerm evaluates one WHERE term against a row. Supports
// `col = $n`, `lower(col) = lower($n)`, `col <= $n` and table-qualified
// columns (`"t"."col" = $n`).
func raMatchTerm(row map[string]driver.Value, term string, args []driver.NamedValue) bool {
	orig := term
	op := "="
	if i := strings.Index(term, "<="); i >= 0 {
		op = "<="
		term = term[:i] + "=" + term[i+2:]
	}
	i := strings.Index(term, "=")
	if i < 0 {
		return false
	}
	left := strings.TrimSpace(term[:i])
	right := strings.TrimSpace(term[i+1:])
	if strings.HasPrefix(left, "$") {
		left, right = right, left
	}

	// Column: strip the lower() wrapper, quoting and the table qualifier.
	col := strings.TrimSpace(left)
	col = strings.TrimSuffix(strings.TrimPrefix(col, "lower("), ")")
	col = strings.ReplaceAll(col, `"`, "")
	if j := strings.LastIndex(col, "."); j >= 0 {
		col = col[j+1:]
	}

	// Value: a $n bindvar (possibly wrapped in lower()) or an inline literal
	// (gorm inlines primary-key conditions like `"t"."id" = 101`).
	val := strings.TrimSpace(right)
	val = strings.TrimSuffix(strings.TrimPrefix(val, "lower("), ")")
	var rv driver.Value
	if strings.HasPrefix(val, "$") {
		n, err := strconv.Atoi(strings.TrimPrefix(val, "$"))
		if err != nil {
			return false
		}
		var arg driver.Value
		found := false
		for _, a := range args {
			if a.Ordinal == n {
				arg = a.Value
				found = true
				break
			}
		}
		if !found {
			return false
		}
		rv = raCanonical(arg)
	} else {
		// Inline literal: quoted string or bare number/bool. Comparisons go
		// through the Sprint-based equality, so the textual form matches.
		rv = strings.Trim(val, `'`)
	}
	lv := raCanonical(row[col])
	if strings.Contains(strings.ToLower(orig), "lower(") {
		ls, ok1 := lv.(string)
		rs, ok2 := rv.(string)
		if ok1 && ok2 {
			lv, rv = strings.ToLower(ls), strings.ToLower(rs)
		}
	}
	if op == "<=" {
		return raCompareVal(lv, rv) <= 0
	}
	return raEqualVal(lv, rv)
}

func raCanonical(v driver.Value) driver.Value {
	switch t := v.(type) {
	case uint:
		return int64(t)
	case uint64:
		return int64(t)
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case []byte:
		return string(t)
	default:
		return v
	}
}

func raEqualVal(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if ta, ok := a.(time.Time); ok {
		tb, ok2 := b.(time.Time)
		return ok2 && ta.Equal(tb)
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// raCompareVal orders two column values; non-comparables sort as equal.
func raCompareVal(a, b driver.Value) int {
	if a == nil || b == nil {
		return 0
	}
	ta, aok := a.(time.Time)
	tb, bok := b.(time.Time)
	if aok && bok {
		switch {
		case ta.Before(tb):
			return -1
		case ta.After(tb):
			return 1
		default:
			return 0
		}
	}
	ai, aok2 := raAsInt(a)
	bi, bok2 := raAsInt(b)
	if aok2 && bok2 {
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

func raAsInt(v driver.Value) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

// execUpdate handles `UPDATE "t" SET "col"=$n, ... WHERE "id" = $n` (gorm's
// Save) and returns the affected count.
func (c *raConn) execUpdate(lower string, args []driver.NamedValue) (driver.Result, error) {
	table := "inbound_emails"
	for _, t := range []string{"inbound_routes", "inbound_forward_tasks", "inbound_emails"} {
		if strings.Contains(lower, `update "`+t+`"`) {
			table = t
		}
	}
	si := strings.Index(lower, " set ")
	wi := strings.Index(lower, " where ")
	if si < 0 || wi < 0 {
		return nil, fmt.Errorf("raState: unsupported update shape: %s", lower)
	}
	setPart := lower[si+len(" set ") : wi]
	wherePart := lower[wi+len(" where "):]

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	affected := int64(0)
	for _, row := range s.table[table] {
		ok := true
		for _, term := range strings.Split(wherePart, " and ") {
			if !raMatchTerm(row, strings.TrimSpace(term), args) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, pair := range strings.Split(setPart, ",") {
			kv := strings.Split(pair, "=")
			if len(kv) != 2 {
				continue
			}
			col := strings.Trim(strings.TrimSpace(kv[0]), `"`)
			ord, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(kv[1]), "$"))
			if err != nil {
				return nil, err
			}
			for _, a := range args {
				if a.Ordinal == ord {
					row[col] = raCanonical(a.Value)
				}
			}
		}
		affected++
	}
	return raResult{affected: affected}, nil
}

// execDelete handles `DELETE FROM "inbound_routes" WHERE ...`.
func (c *raConn) execDelete(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	wherePart := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		wherePart = lower[i+len(" where "):]
	}
	kept := make([]map[string]driver.Value, 0, len(s.table["inbound_routes"]))
	affected := int64(0)
	for _, row := range s.table["inbound_routes"] {
		ok := true
		if strings.TrimSpace(wherePart) != "" {
			for _, term := range strings.Split(wherePart, " and ") {
				if !raMatchTerm(row, strings.TrimSpace(term), args) {
					ok = false
					break
				}
			}
		}
		if ok {
			affected++
			continue
		}
		kept = append(kept, row)
	}
	s.table["inbound_routes"] = kept
	return raResult{affected: affected}, nil
}

type raResult struct{ affected int64 }

func (r raResult) LastInsertId() (int64, error) { return 0, nil }
func (r raResult) RowsAffected() (int64, error) { return r.affected, nil }

type raRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *raRows) Columns() []string { return r.cols }
func (r *raRows) Close() error      { return nil }
func (r *raRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// ---------- fake seams ----------

type raEnqueuer struct {
	mu      sync.Mutex
	parses  []uint
	process []uint
}

func (e *raEnqueuer) EnqueueInboundProcess(id uint) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.process = append(e.process, id)
	return nil
}

func (e *raEnqueuer) EnqueueInboundParse(id uint) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.parses = append(e.parses, id)
	return nil
}

func (e *raEnqueuer) parseCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.parses)
}

type raDispatch struct {
	userID      uint
	workspaceID *uint
	event       string
	body        string
	from        string
}

type raWebhooks struct {
	mu    sync.Mutex
	calls []raDispatch
}

func (w *raWebhooks) DispatchJSON(userID uint, workspaceID *uint, event string, body []byte, fromAddr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, raDispatch{userID: userID, workspaceID: workspaceID, event: event, body: string(body), from: fromAddr})
}

func (w *raWebhooks) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.calls)
}

type raQuarantineCall struct {
	inboundID uint
	routeID   uint
	reason    string
}

type raQuarantine struct {
	mu    sync.Mutex
	calls []raQuarantineCall
}

func (q *raQuarantine) Quarantine(_ context.Context, rec *models.InboundEmail, route *models.InboundRoute, reason string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls = append(q.calls, raQuarantineCall{inboundID: rec.ID, routeID: route.ID, reason: reason})
	return nil
}

func (q *raQuarantine) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.calls)
}

// ---------- harness ----------

func raWs(v uint) *uint { return &v }

// raFixture is the shared test fixture: one verified domain (routed.test,
// workspace 3, tenant 7), optional routes, and a fully wired service.
type raFixture struct {
	state      *raState
	svc        *Service
	gdb        *gorm.DB
	eq         *raEnqueuer
	webhooks   *raWebhooks
	quarantine *raQuarantine
	events     <-chan *models.Event
}

// raNewFixture seeds the domain plus routes and wires the service.
func raNewFixture(t *testing.T, routes ...models.InboundRoute) *raFixture {
	t.Helper()
	state := newRaState()
	state.addDomain(1, 7, raWs(3), "routed.test", true)
	for _, r := range routes {
		state.addRoute(r.ID, 1, 7, raWs(3), r.Pattern, r.Action, r.Priority, r.Enabled)
	}

	gdb := raGorm(t, state)
	svc := NewService(
		repositories.NewInboundEmailRepository(gdb),
		repositories.NewDomainRepository(gdb),
		nil, // suppression repo: not under test here
		Config{},
	)
	svc.SetRouter(NewRouter(repositories.NewInboundRouteRepository(gdb)))
	f := &raFixture{state: state, gdb: gdb, eq: &raEnqueuer{}, webhooks: &raWebhooks{}, quarantine: &raQuarantine{}}
	svc.SetForwardQueue(repositories.NewInboundForwardTaskRepository(gdb))
	svc.SetWebhookDispatcher(f.webhooks)
	svc.SetQuarantineStore(f.quarantine)
	svc.SetEnqueuer(f.eq)

	bus := eventbus.New(repositories.NewEventRepository(gdb))
	svc.SetEventBus(bus)
	events, unsub := bus.Subscribe()
	t.Cleanup(unsub)
	f.svc = svc
	f.events = events
	return f
}

// raSMTPSession builds an SMTP session over the fixture's state.
func (f *raFixture) raSMTPSession() *session {
	backend := &Backend{
		svc:        f.svc,
		domainRepo: repositories.NewDomainRepository(f.gdb),
		router:     NewRouter(repositories.NewInboundRouteRepository(f.gdb)),
	}
	return &session{
		backend: backend,
		remote:  "192.0.2.10:4441",
		guard:   security.NewSessionGuard(nil, netip.MustParseAddr("192.0.2.10"), 2525, "smtp-routing-test"),
	}
}

// raIngestRaw ingests the fixture message for recipient.
func (f *raFixture) raIngestRaw(t *testing.T, recipient string) (*models.InboundEmail, error) {
	t.Helper()
	return f.svc.IngestRaw(context.Background(), []byte(raFixtureRaw), "alice@example.test",
		[]string{recipient}, models.InboundSourceSMTP, "192.0.2.10")
}

const raFixtureRaw = "From: Alice <alice@example.test>\r\n" +
	"To: user@routed.test\r\nSubject: routing fixture\r\n\r\nHello\r\n"

// raDrainEvents collects buffered events published during the call under
// test (publish is synchronous, so they are queued already).
func raDrainEvents(t *testing.T, ch <-chan *models.Event) []*models.Event {
	t.Helper()
	var out []*models.Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func raEventTypes(evs []*models.Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

// ---------- RCPT-time validation ----------

// A recipient on a verified domain with a matching route is accepted.
func TestSMTPRcptAcceptsRecipientWithMatchingRoute(t *testing.T) {
	f := raNewFixture(t, route(2, "user@routed.test", models.InboundRouteActionDeliver, 10, true))
	sess := f.raSMTPSession()

	if err := sess.Rcpt("User@Routed.test", nil); err != nil {
		t.Fatalf("routed recipient rejected: %v", err)
	}
	if len(sess.to) != 1 || sess.to[0] != "User@Routed.test" {
		t.Fatalf("recipient not recorded: %v", sess.to)
	}
}

// A verified-domain recipient with no matching route/catch-all gets the
// explicit 550 BEFORE the receiver accepts responsibility.
func TestSMTPRcptRejectsUnroutedRecipientWith550(t *testing.T) {
	f := raNewFixture(t, route(2, "user@routed.test", models.InboundRouteActionDeliver, 10, true))
	sess := f.raSMTPSession()

	err := sess.Rcpt("ghost@routed.test", nil)
	var smtpErr *smtp.SMTPError
	if !errors.As(err, &smtpErr) {
		t.Fatalf("err = %v, want SMTPError", err)
	}
	if smtpErr.Code != 550 {
		t.Fatalf("code = %d, want 550", smtpErr.Code)
	}
	if len(sess.to) != 0 {
		t.Fatalf("rejected recipient must not be recorded: %v", sess.to)
	}
}

// A catch-all accepts otherwise-unrouted recipients at RCPT.
func TestSMTPRcptCatchAllAcceptsRecipient(t *testing.T) {
	f := raNewFixture(t, route(2, "@routed.test", models.InboundRouteActionDeliver, 100, true))
	sess := f.raSMTPSession()

	if err := sess.Rcpt("anyone@routed.test", nil); err != nil {
		t.Fatalf("catch-all recipient rejected: %v", err)
	}
}

// Unknown (unverified) domains keep today's 550.
func TestSMTPRcptRejectsUnknownDomainWith550(t *testing.T) {
	f := raNewFixture(t)
	sess := f.raSMTPSession()

	err := sess.Rcpt("user@nowhere.test", nil)
	var smtpErr *smtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 550 {
		t.Fatalf("err = %v, want 550 SMTPError", err)
	}
}

// A route-store failure is temporary (451), never a policy reject.
func TestSMTPRcptRouteStoreFailureIsTemporary451(t *testing.T) {
	f := raNewFixture(t)
	f.state.failRouteReads()
	sess := f.raSMTPSession()

	err := sess.Rcpt("user@routed.test", nil)
	var smtpErr *smtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 451 {
		t.Fatalf("err = %v, want 451 SMTPError", err)
	}
}

// DATA-time: a recipient accepted at RCPT whose route disappeared gets the
// explicit 550, and the rejection is persisted (never accept then silently
// lose).
func TestSMTPDataMapsNoRouteTo550AndPersistsReject(t *testing.T) {
	f := raNewFixture(t, route(2, "user@routed.test", models.InboundRouteActionDeliver, 10, true))
	sess := f.raSMTPSession()
	if err := sess.Rcpt("user@routed.test", nil); err != nil {
		t.Fatalf("rcpt: %v", err)
	}
	f.state.mu.Lock()
	f.state.table["inbound_routes"] = nil // route removed between RCPT and DATA
	f.state.mu.Unlock()

	err := sess.Data(strings.NewReader(raFixtureRaw))
	var smtpErr *smtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 550 {
		t.Fatalf("err = %v, want 550 SMTPError", err)
	}
	rows := f.state.rows(t, "inbound_emails")
	if len(rows) != 1 {
		t.Fatalf("rejected message not persisted: %d rows", len(rows))
	}
	if rows[0]["status"] != string(models.InboundStatusRejected) {
		t.Fatalf("status = %v, want rejected", rows[0]["status"])
	}
	if msg, _ := rows[0]["error_message"].(string); !strings.Contains(msg, "no matching inbound route") {
		t.Fatalf("error_message = %q, want the no-route reason", msg)
	}
}

// ---------- ingest-path routing (SMTP raw path) ----------

// Unknown domain: today's behavior — rejected, nothing persisted.
func TestIngestRawUnknownDomainRejectsWithoutPersist(t *testing.T) {
	f := raNewFixture(t)

	_, err := f.raIngestRaw(t, "user@nowhere.test")
	if !errors.Is(err, ErrUnverifiedDomain) {
		t.Fatalf("err = %v, want ErrUnverifiedDomain", err)
	}
	if n := f.state.rowCount(t, "inbound_emails"); n != 0 {
		t.Fatalf("unverified-domain reject must not persist, got %d rows", n)
	}
}

// Explicit no-route reject: persisted as a rejected record.
func TestIngestRawNoRoutePersistsRejectedRecord(t *testing.T) {
	f := raNewFixture(t, route(2, "user@routed.test", models.InboundRouteActionDeliver, 10, true))

	_, err := f.raIngestRaw(t, "ghost@routed.test")
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("err = %v, want ErrNoRoute", err)
	}
	rows := f.state.rows(t, "inbound_emails")
	if len(rows) != 1 {
		t.Fatalf("want 1 persisted reject, got %d", len(rows))
	}
	if rows[0]["status"] != string(models.InboundStatusRejected) {
		t.Fatalf("status = %v, want rejected", rows[0]["status"])
	}
	if msg, _ := rows[0]["error_message"].(string); !strings.Contains(msg, "no matching inbound route") {
		t.Fatalf("error_message = %q", msg)
	}
	if rec, _ := rows[0]["recipients"].([]byte); rec != nil && !strings.Contains(string(rec), "ghost@routed.test") {
		t.Fatalf("recipients = %q, want the envelope recipient", rec)
	}
}

// A forward route queues the durable forwarding task row (target, attempts,
// next_attempt_at) and stamps the record — the executor itself is wave 2.
func TestIngestRawQueuesForwardTaskForForwardRoute(t *testing.T) {
	f := raNewFixture(t, route(5, "@routed.test", models.InboundRouteActionForward, 10, true))
	f.state.setRouteTarget(5, "dest@elsewhere.test")

	rec, err := f.raIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if rec.RouteAction != models.InboundRouteActionForward || rec.RouteID == nil || *rec.RouteID != 5 {
		t.Fatalf("route stamp missing: action=%q routeID=%v", rec.RouteAction, rec.RouteID)
	}

	tasks := f.state.rows(t, "inbound_forward_tasks")
	if len(tasks) != 1 {
		t.Fatalf("want 1 forward task, got %d", len(tasks))
	}
	task := tasks[0]
	if task["target"] != "dest@elsewhere.test" {
		t.Fatalf("task target = %v", task["target"])
	}
	if task["status"] != string(models.InboundForwardTaskPending) {
		t.Fatalf("task status = %v, want pending", task["status"])
	}
	if attempts, _ := task["attempts"].(int64); attempts != 0 {
		t.Fatalf("task attempts = %v, want 0", task["attempts"])
	}
	if na, _ := task["next_attempt_at"].(time.Time); na.IsZero() {
		t.Fatal("task next_attempt_at must be set")
	}
	if task["inbound_email_id"] != int64(rec.ID) {
		t.Fatalf("task inbound ref = %v, want %d", task["inbound_email_id"], rec.ID)
	}

	// The record keeps moving through the regular parse pipeline; no webhook
	// dispatch and no quarantine for a forward route.
	if f.eq.parseCount() != 1 {
		t.Fatalf("parse enqueue count = %d, want 1", f.eq.parseCount())
	}
	if f.webhooks.count() != 0 {
		t.Fatal("forward route must not dispatch webhooks")
	}
	if f.quarantine.count() != 0 {
		t.Fatal("forward route must not quarantine")
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received (forward happens later)", rec.Status)
	}

	types := raEventTypes(raDrainEvents(t, f.events))
	if len(types) != 2 || types[0] != "route.matched" || types[1] != "inbound.forward_queued" {
		t.Fatalf("events = %v, want [route.matched inbound.forward_queued]", types)
	}
}

// The forwarding queue is idempotent per inbound record: re-running the
// action step for the same record (a retried or racing ingest) must not
// queue a second task — the unique inbound_email_id + ON CONFLICT DO NOTHING
// makes first enqueue win. (A genuinely second SMTP delivery of the same
// message is a new record; its parse-time duplicate detection is the parse
// worker's job — the webhook-path dedup test covers the message-level story.)
func TestIngestRawForwardQueueIdempotentOnReingest(t *testing.T) {
	f := raNewFixture(t, route(5, "user@routed.test", models.InboundRouteActionForward, 10, true))
	f.state.setRouteTarget(5, "dest@elsewhere.test")

	rec, err := f.raIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if n := f.state.rowCount(t, "inbound_forward_tasks"); n != 1 {
		t.Fatalf("forward tasks after first ingest = %d, want 1", n)
	}

	// Simulate the action step running again for the same record.
	f.svc.applyRouteActions(context.Background(), rec, &models.InboundRoute{
		ID: 5, Action: models.InboundRouteActionForward, Target: "dest@elsewhere.test",
	}, RouteMatchCatchAll)
	if n := f.state.rowCount(t, "inbound_forward_tasks"); n != 1 {
		t.Fatalf("forward tasks after re-run = %d, want 1 (idempotent queue)", n)
	}
}

// A webhook route dispatches through the webhook seam at ingest — the HMAC
// signing and SSRF guard live inside the machinery, untouched.
func TestIngestRawWebhookActionDispatchesViaDispatcherSeam(t *testing.T) {
	f := raNewFixture(t, route(9, "user@routed.test", models.InboundRouteActionWebhook, 10, true))
	f.state.setRouteTarget(9, "email.inbound.route")

	rec, err := f.raIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if f.webhooks.count() != 1 {
		t.Fatalf("webhook dispatches = %d, want 1", f.webhooks.count())
	}
	call := f.webhooks.calls[0]
	if call.event != "email.inbound.route" {
		t.Fatalf("dispatch event = %q, want the route target", call.event)
	}
	if call.userID != rec.UserID || call.workspaceID != rec.WorkspaceID {
		t.Fatalf("dispatch scope = %d/%v, want %d/%v", call.userID, call.workspaceID, rec.UserID, rec.WorkspaceID)
	}
	var payload InboundRouteWebhookPayload
	if err := json.Unmarshal([]byte(call.body), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.InboundID != rec.UUID || payload.RouteID != 9 {
		t.Fatalf("payload = %+v, want inbound %s route 9", payload, rec.UUID)
	}
	// Route-owned notification: the generic email.inbound process dispatch
	// must not also run for a webhook route (no double POST).
	if f.eq.parseCount() != 1 {
		t.Fatalf("parse enqueue count = %d, want 1", f.eq.parseCount())
	}
	types := raEventTypes(raDrainEvents(t, f.events))
	if len(types) != 1 || types[0] != "route.matched" {
		t.Fatalf("events = %v, want [route.matched]", types)
	}
}

// An empty webhook target falls back to the established email.inbound event.
func TestIngestRawWebhookActionDefaultTargetEvent(t *testing.T) {
	f := raNewFixture(t, route(9, "user@routed.test", models.InboundRouteActionWebhook, 10, true))

	if _, err := f.raIngestRaw(t, "user@routed.test"); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if f.webhooks.count() != 1 || f.webhooks.calls[0].event != "email.inbound" {
		t.Fatalf("default dispatch = %+v, want event email.inbound", f.webhooks.calls)
	}
}

// A quarantine route flips the inbound row to quarantined and calls the
// quarantine seam; the message is held unparsed (no parse task).
func TestIngestRawQuarantineActionSetsStatusAndCallsSeam(t *testing.T) {
	f := raNewFixture(t, route(4, "user@routed.test", models.InboundRouteActionQuarantine, 10, true))

	rec, err := f.raIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %q, want quarantined", rec.Status)
	}
	if f.quarantine.count() != 1 {
		t.Fatalf("quarantine seam calls = %d, want 1", f.quarantine.count())
	}
	if f.quarantine.calls[0].inboundID != rec.ID || f.quarantine.calls[0].routeID != 4 {
		t.Fatalf("quarantine call = %+v, want inbound %d route 4", f.quarantine.calls[0], rec.ID)
	}
	if f.eq.parseCount() != 0 {
		t.Fatalf("quarantined message must not be parsed, got %d parse tasks", f.eq.parseCount())
	}
	if f.webhooks.count() != 0 {
		t.Fatal("quarantined message must not dispatch webhooks")
	}
	rows := f.state.rows(t, "inbound_emails")
	if len(rows) != 1 || rows[0]["status"] != string(models.InboundStatusQuarantined) {
		t.Fatalf("persisted status = %+v, want quarantined", rows)
	}
	types := raEventTypes(raDrainEvents(t, f.events))
	if len(types) != 2 || types[0] != "route.matched" || types[1] != "inbound.quarantined" {
		t.Fatalf("events = %v, want [route.matched inbound.quarantined]", types)
	}
}

// A deliver route keeps today's archive behavior: received status, parse
// pipeline, no side effects.
func TestIngestRawDeliverActionKeepsArchiveBehavior(t *testing.T) {
	f := raNewFixture(t, route(2, "user@routed.test", models.InboundRouteActionDeliver, 10, true))

	rec, err := f.raIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received", rec.Status)
	}
	if f.eq.parseCount() != 1 {
		t.Fatalf("parse enqueue count = %d, want 1", f.eq.parseCount())
	}
	if n := f.state.rowCount(t, "inbound_forward_tasks"); n != 0 {
		t.Fatalf("deliver route must not queue forwards, got %d", n)
	}
	if f.webhooks.count() != 0 || f.quarantine.count() != 0 {
		t.Fatal("deliver route must not dispatch webhooks or quarantine")
	}
	types := raEventTypes(raDrainEvents(t, f.events))
	if len(types) != 1 || types[0] != "route.matched" {
		t.Fatalf("events = %v, want [route.matched]", types)
	}
}

// Workspace isolation: a route belonging to another workspace's domain never
// matches — routing resolves only through the recipient's own verified domain.
func TestIngestWorkspaceIsolation(t *testing.T) {
	state := newRaState()
	state.addDomain(1, 7, raWs(3), "routed.test", true) // tenant A (ws 3)
	state.addDomain(2, 8, raWs(4), "other.test", true)  // tenant B (ws 4)
	// Tenant B's rule names tenant A's address shape — it must never match.
	state.addRoute(5, 2, 8, raWs(4), "user@routed.test", models.InboundRouteActionForward, 1, true)

	gdb := raGorm(t, state)
	svc := NewService(
		repositories.NewInboundEmailRepository(gdb),
		repositories.NewDomainRepository(gdb),
		nil,
		Config{},
	)
	svc.SetRouter(NewRouter(repositories.NewInboundRouteRepository(gdb)))
	svc.SetForwardQueue(repositories.NewInboundForwardTaskRepository(gdb))

	_, err := svc.IngestRaw(context.Background(), []byte(raFixtureRaw), "alice@example.test",
		[]string{"user@routed.test"}, models.InboundSourceSMTP, "192.0.2.10")
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("err = %v, want ErrNoRoute (other workspace's route must not match)", err)
	}
	if n := state.rowCount(t, "inbound_forward_tasks"); n != 0 {
		t.Fatalf("no task may be queued for a rejected message, got %d", n)
	}
	if n := state.rowCount(t, "inbound_emails"); n != 1 {
		t.Fatalf("the reject must be persisted, got %d rows", n)
	}
}

// ---------- webhook (parsed) ingest path ----------

// The webhook ingest path resolves routes the same way, and a duplicate
// Message-ID re-ingest is fully idempotent: no second row, no second action.
func TestIngestDuplicateMessageSkipsActions(t *testing.T) {
	f := raNewFixture(t, route(5, "user@routed.test", models.InboundRouteActionForward, 10, true))
	f.state.setRouteTarget(5, "dest@elsewhere.test")

	parsed := &ParsedEmail{
		Raw:       []byte(raFixtureRaw),
		MessageID: "<dup-1@example.test>",
		From:      "alice@example.test",
		To:        []string{"user@routed.test"},
		Subject:   "dup",
	}
	if _, err := f.svc.Ingest(context.Background(), parsed, models.InboundSourceWebhook); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	_, err := f.svc.Ingest(context.Background(), parsed, models.InboundSourceWebhook)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second ingest err = %v, want ErrDuplicate", err)
	}
	if n := f.state.rowCount(t, "inbound_emails"); n != 1 {
		t.Fatalf("email rows = %d, want 1", n)
	}
	if n := f.state.rowCount(t, "inbound_forward_tasks"); n != 1 {
		t.Fatalf("forward tasks = %d, want 1", n)
	}
	if f.webhooks.count() != 0 || f.quarantine.count() != 0 {
		t.Fatal("duplicate ingest must not run any action")
	}
}

// ---------- parse interaction ----------

// ApplyParsed must not silently release a quarantined record back to
// received — the parse worker can race the quarantine action.
func TestApplyParsedPreservesQuarantinedStatus(t *testing.T) {
	f := raNewFixture(t)

	rec := &models.InboundEmail{
		ID: 33, UUID: "inb-q-1", UserID: 7, WorkspaceID: raWs(3), DomainID: 1,
		Sender:     "alice@example.test",
		Recipients: []string{"user@routed.test"},
		Status:     models.InboundStatusQuarantined,
		ReceivedAt: time.Now().UTC(),
	}
	f.state.mu.Lock()
	f.state.table["inbound_emails"] = append(f.state.table["inbound_emails"], map[string]driver.Value{
		"id": int64(33), "uuid": "inb-q-1", "user_id": int64(7), "workspace_id": int64(3),
		"domain_id": int64(1), "sender": "alice@example.test", "status": string(models.InboundStatusQuarantined),
		"recipients": []byte(`{user@routed.test}`), "created_at": time.Now().UTC(),
	})
	f.state.mu.Unlock()

	parsed := &ParsedEmail{Raw: []byte(raFixtureRaw), From: "alice@example.test", To: []string{"user@routed.test"}, Subject: "held"}
	if err := f.svc.ApplyParsed(context.Background(), rec, parsed); err != nil {
		t.Fatalf("apply parsed: %v", err)
	}
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status after parse = %q, want quarantined", rec.Status)
	}
	rows := f.state.rows(t, "inbound_emails")
	if len(rows) != 1 || rows[0]["status"] != string(models.InboundStatusQuarantined) {
		t.Fatalf("persisted status = %+v, want quarantined", rows)
	}
}

// ---------- route-owned notification contract ----------

// The parse worker's chained email.inbound dispatch is skipped for
// route-owned actions — this is the contract worker.inbound_handler.go
// consults via inbound.RouteOwnsNotification.
func TestRouteOwnsNotificationMatrix(t *testing.T) {
	if RouteOwnsNotification("") {
		t.Fatal("no routing (empty action) must keep the generic dispatch")
	}
	if RouteOwnsNotification(models.InboundRouteActionDeliver) {
		t.Fatal("deliver must keep the generic dispatch")
	}
	for _, a := range []models.InboundRouteAction{models.InboundRouteActionForward, models.InboundRouteActionWebhook, models.InboundRouteActionQuarantine} {
		if !RouteOwnsNotification(a) {
			t.Fatalf("action %q must own its notification", a)
		}
	}
}

// ---------- repositories over the fake driver ----------

// The engine-facing lookup returns only enabled routes of the domain,
// ordered by priority then id.
func TestInboundRouteRepoListEnabledByDomainOrdersByPriority(t *testing.T) {
	state := newRaState()
	state.addDomain(1, 7, raWs(3), "routed.test", true)
	state.addRoute(5, 1, 7, raWs(3), "a@routed.test", models.InboundRouteActionDeliver, 20, true)
	state.addRoute(2, 1, 7, raWs(3), "b@routed.test", models.InboundRouteActionDeliver, 5, true)
	state.addRoute(3, 1, 7, raWs(3), "c@routed.test", models.InboundRouteActionDeliver, 1, false) // disabled
	gdb := raGorm(t, state)
	repo := repositories.NewInboundRouteRepository(gdb)

	got, err := repo.ListEnabledByDomain(1)
	if err != nil {
		t.Fatalf("ListEnabledByDomain: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d routes, want 2 (disabled filtered)", len(got))
	}
	if got[0].ID != 2 || got[1].ID != 5 {
		t.Fatalf("order = [%d %d], want [2 5] (priority asc)", got[0].ID, got[1].ID)
	}
}

// The management surface is workspace-scoped: rows of another workspace are
// invisible and un-mutable (the ownsResource pattern at the repo layer).
func TestInboundRouteRepoWorkspaceScopedCRUD(t *testing.T) {
	state := newRaState()
	state.addRoute(5, 1, 7, raWs(3), "team@routed.test", models.InboundRouteActionDeliver, 10, true)
	gdb := raGorm(t, state)
	repo := repositories.NewInboundRouteRepository(gdb)

	own := repositories.ResourceScope{UserID: 7, WorkspaceID: raWs(3)}
	other := repositories.ResourceScope{UserID: 8, WorkspaceID: raWs(4)}

	// Create + read within scope.
	created := &models.InboundRoute{
		UserID: 7, WorkspaceID: raWs(3), DomainID: 1,
		Pattern: "new@routed.test", Action: models.InboundRouteActionWebhook,
		Priority: 7, Enabled: true,
	}
	if err := repo.Create(created); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.FindByID(own, created.ID)
	if err != nil {
		t.Fatalf("FindByID (own scope): %v", err)
	}
	if got.Pattern != "new@routed.test" {
		t.Fatalf("FindByID pattern = %q", got.Pattern)
	}
	if _, err := repo.FindByID(other, created.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("FindByID (other scope) err = %v, want ErrRecordNotFound", err)
	}

	// ListByScope is scoped.
	items, total, err := repo.ListByScope(own, 10, 0)
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("ListByScope (own) = %d items, total %d, err %v", len(items), total, err)
	}
	if _, total2, err := repo.ListByScope(other, 10, 0); err != nil || total2 != 0 {
		t.Fatalf("ListByScope (other) total = %d, err %v, want 0", total2, err)
	}

	// Delete is scoped.
	if err := repo.Delete(other, created.ID); err != nil {
		t.Fatalf("Delete (other scope): %v", err)
	}
	if _, err := repo.FindByID(own, created.ID); err != nil {
		t.Fatalf("other-scope delete must not remove the row: %v", err)
	}
	if err := repo.Delete(own, created.ID); err != nil {
		t.Fatalf("Delete (own scope): %v", err)
	}
	if _, err := repo.FindByID(own, created.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("own-scope delete must remove the row: %v", err)
	}
}

// The forwarding queue is idempotent per inbound message and ListDue returns
// only pending tasks whose next attempt has arrived.
func TestInboundForwardTaskRepoEnqueueIdempotentAndListDue(t *testing.T) {
	state := newRaState()
	gdb := raGorm(t, state)
	repo := repositories.NewInboundForwardTaskRepository(gdb)

	task := &models.InboundForwardTask{
		UserID: 7, WorkspaceID: raWs(3), DomainID: 1,
		InboundEmailID: 42, RouteID: 5, Target: "dest@elsewhere.test",
		Status:        models.InboundForwardTaskPending,
		NextAttemptAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := repo.Enqueue(task); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	dup := &models.InboundForwardTask{
		UserID: 7, WorkspaceID: raWs(3), DomainID: 1,
		InboundEmailID: 42, RouteID: 5, Target: "dest@elsewhere.test",
		Status:        models.InboundForwardTaskPending,
		NextAttemptAt: time.Now().UTC(),
	}
	if err := repo.Enqueue(dup); err != nil {
		t.Fatalf("Enqueue (duplicate): %v", err)
	}
	rows := state.rows(t, "inbound_forward_tasks")
	if len(rows) != 1 {
		t.Fatalf("queue rows = %d, want 1 (ON CONFLICT DO NOTHING)", len(rows))
	}

	// A future task is not due yet.
	future := &models.InboundForwardTask{
		UserID: 7, WorkspaceID: raWs(3), DomainID: 1,
		InboundEmailID: 43, RouteID: 5, Target: "dest2@elsewhere.test",
		Status:        models.InboundForwardTaskPending,
		NextAttemptAt: time.Now().UTC().Add(time.Hour),
	}
	if err := repo.Enqueue(future); err != nil {
		t.Fatalf("Enqueue (future): %v", err)
	}
	due, err := repo.ListDue(time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	if len(due) != 1 || due[0].InboundEmailID != 42 {
		t.Fatalf("ListDue = %+v, want only the overdue task 42", due)
	}
}
