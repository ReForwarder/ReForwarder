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

package automation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The engine speaks to Postgres through concrete gorm repositories, and its
// correctness lives in the SQL semantics (compare-and-swap updates, the
// FOR UPDATE SKIP LOCKED claim, the jsonb stat increment, ON CONFLICT dedup).
// These tests run the real engine over a fake database/sql driver: a tiny
// in-memory store that evaluates the WHERE clauses the repositories emit, so
// CAS losses, claim windows and dedup conflicts behave like the database.
// No SQL is ever sent anywhere; everything is deterministic under the fake
// clock wired through SetClock.

// ---------- the fake store ----------

var engColumns = map[string][]string{
	"automations": {"id", "uuid", "user_id", "workspace_id", "name", "status", "list_id",
		"exit_on_reply", "trigger_config", "nodes", "root_node_id", "stats", "version",
		"created_at", "updated_at", "deleted_at"},
	"automation_journeys": {"id", "uuid", "automation_id", "workspace_id", "subscriber_id",
		"email", "current_node_id", "status", "exit_reason", "scheduled_at", "retry_count",
		"last_error", "context_json", "entered_at", "last_advanced_at", "completed_at"},
	"automation_node_runs": {"id", "journey_id", "automation_id", "node_id", "node_type",
		"action", "output", "error", "duration_ms", "entered_at", "completed_at"},
	"automation_trigger_log":       {"id", "automation_id", "subscriber_id", "triggered_at"},
	"subscribers":                  {"id", "user_id", "workspace_id", "email", "name", "status", "custom_fields", "timezone", "language", "subscribed_at", "unsubscribed_at", "created_at", "updated_at"},
	"subscriber_lists":             {"id", "user_id", "workspace_id", "name", "description", "type", "filter_rules", "created_at", "updated_at"},
	"subscriber_list_members":      {"id", "list_id", "subscriber_id", "created_at"},
	"subscriber_list_unsubscribes": {"id", "list_id", "subscriber_id", "reason", "unsubscribed_at"},
	"segments":                     {"id", "user_id", "workspace_id", "name", "description", "condition", "created_at", "updated_at"},
}

// Composite unique keys consulted for ON CONFLICT DO NOTHING inserts.
var engUniqueKeys = map[string][]string{
	"automation_trigger_log":       {"automation_id", "subscriber_id"},
	"subscriber_list_members":      {"list_id", "subscriber_id"},
	"subscriber_list_unsubscribes": {"list_id", "subscriber_id"},
}

// Column defaults applied to INSERTs that omit them (the database would fill
// them from the schema defaults).
var engDefaults = map[string]map[string]func() driver.Value{
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
	"automation_node_runs": {
		"action":      func() driver.Value { return models.AutomationRunActionEntered },
		"output":      func() driver.Value { return "{}" },
		"error":       func() driver.Value { return "" },
		"duration_ms": func() driver.Value { return int64(0) },
	},
}

type engTable struct {
	columns []string
	rows    [][]driver.Value
}

type engState struct {
	mu     sync.Mutex
	tables map[string]*engTable
	seq    int64
	// hook, when set, fires with every statement's lowercased SQL before it is
	// processed — the injection point for mid-flight state changes (the CAS
	// race test). Hook bodies may take s.mu.
	hook func(lowerQuery string)
}

func newEngState() *engState {
	tables := make(map[string]*engTable, len(engColumns))
	for name, cols := range engColumns {
		tables[name] = &engTable{columns: cols}
	}
	return &engState{tables: tables, seq: 1000}
}

func (s *engState) fireHook(lower string) {
	if s.hook != nil {
		s.hook(lower)
	}
}

func (s *engState) colIndex(table, col string) int {
	for i, c := range engColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

// engVal canonicalizes a Go value into a driver.Value the way database/sql's
// default converter does.
func engVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case int64, float64, bool, string, []byte, time.Time:
		return t
	case uint64:
		return int64(t)
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
		panic(fmt.Sprintf("engVal: unsupported type %T", v))
	}
}

// insertRow stores one row and returns it (with defaults and a fresh id).
func (s *engState) insertRow(table string, cols []string, vals []driver.Value) ([]driver.Value, error) {
	spec, ok := engColumns[table]
	if !ok {
		return nil, fmt.Errorf("engState: unknown table %q", table)
	}
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := engDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for j, col := range cols {
		idx := s.colIndex(table, col)
		if idx < 0 {
			return nil, fmt.Errorf("engState: table %q has no column %q", table, col)
		}
		row[idx] = vals[j]
	}
	// Structural columns.
	if idx := s.colIndex(table, "id"); row[idx] == nil {
		s.seq++
		row[idx] = s.seq
	}
	if idx := s.colIndex(table, "uuid"); idx >= 0 && row[idx] == nil {
		row[idx] = fmt.Sprintf("%s-%d-uuid", table, row[s.colIndex(table, "id")])
	}
	tbl := s.tables[table]
	tbl.rows = append(tbl.rows, row)
	return row, nil
}

// seed inserts a row from Go values (test fixture helper); it returns the id.
func engSeed(t *testing.T, s *engState, table string, vals map[string]any) int64 {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	cols := make([]string, 0, len(vals))
	vals2 := make([]driver.Value, 0, len(vals))
	for col, v := range vals {
		cols = append(cols, col)
		vals2 = append(vals2, engVal(v))
	}
	row, err := s.insertRow(table, cols, vals2)
	if err != nil {
		t.Fatalf("seed %s: %v", table, err)
	}
	return row[s.colIndex(table, "id")].(int64)
}

// rowMap returns the row with id as a column map (nil when absent).
func (s *engState) rowMap(table string, id int64) map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rowMapLocked(table, "id", id)
}

func (s *engState) rowMapLocked(table, col string, val driver.Value) map[string]driver.Value {
	idx := s.colIndex(table, col)
	for _, row := range s.tables[table].rows {
		if driverValueEqual(row[idx], val) {
			out := make(map[string]driver.Value, len(row))
			for i, c := range engColumns[table] {
				out[c] = row[i]
			}
			return out
		}
	}
	return nil
}

func (s *engState) setCells(table string, id int64, vals map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.colIndex(table, "id")
	for _, row := range s.tables[table].rows {
		if driverValueEqual(row[idx], id) {
			for col, v := range vals {
				ci := s.colIndex(table, col)
				if ci < 0 {
					panic("setCells: unknown column " + col)
				}
				row[ci] = engVal(v)
			}
			return
		}
	}
}

func (s *engState) rowCount(table string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tables[table].rows)
}

// ---------- the fake driver ----------

type engConnector struct{ state *engState }

func (c engConnector) Connect(context.Context) (driver.Conn, error) { return &engConn{s: c.state}, nil }
func (c engConnector) Driver() driver.Driver                        { return engDrv{} }

type engDrv struct{}

func (engDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type engConn struct{ s *engState }

func (c *engConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("prepare unsupported") }
func (c *engConn) Close() error                        { return nil }
func (c *engConn) Begin() (driver.Tx, error)           { return engTx{}, nil }

type engTx struct{}

func (engTx) Commit() error   { return nil }
func (engTx) Rollback() error { return nil }

type engResult struct{ rows int64 }

func (r engResult) LastInsertId() (int64, error) { return 0, nil }
func (r engResult) RowsAffected() (int64, error) { return r.rows, nil }

type engRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *engRows) Columns() []string { return r.cols }
func (r *engRows) Close() error      { return nil }
func (r *engRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func (c *engConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	c.s.fireHook(lower)
	if strings.Contains(lower, "insert into") {
		return c.s.handleInsert(query, args)
	}
	compact := strings.Join(strings.Fields(lower), " ")
	if strings.Contains(compact, "from automation_journeys aj") {
		return c.s.handleClaimQuery(args)
	}
	// The list-membership join (SubscriberListRepository.ListMembers): the
	// schedule trigger's list audience resolves through it.
	if strings.Contains(compact, `from "subscribers" join subscriber_list_members`) {
		return c.s.handleListMembersQuery(args)
	}
	table := matchEngTable(compact)
	if table == "" {
		return nil, fmt.Errorf("engState: cannot route query: %s", query)
	}
	if strings.Contains(compact, "count(*)") {
		matched, err := c.s.evalWhere(table, compact, args)
		if err != nil {
			return nil, err
		}
		return &engRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	matched, err := c.s.evalWhere(table, compact, args)
	if err != nil {
		return nil, err
	}
	matched = sortEngRows(c.s, table, matched, compact)
	matched = applyEngLimitOffset(matched, compact, args)
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &engRows{cols: engColumns[table], rows: rows}, nil
}

func (c *engConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.ToLower(query)
	c.s.fireHook(lower)
	compact := strings.Join(strings.Fields(lower), " ")
	if strings.Contains(compact, "jsonb_set(") {
		return c.s.handleIncrementStat(compact, args)
	}
	switch {
	case strings.Contains(compact, "update "):
		return c.s.handleUpdate(compact, args)
	case strings.Contains(compact, "delete from"):
		return c.s.handleDelete(compact, args)
	default:
		return nil, fmt.Errorf("engState: unsupported exec: %s", query)
	}
}

// matchEngTable finds the table a SELECT/UPDATE/DELETE touches by its quoted
// name.
// handleListMembersQuery answers SubscriberListRepository.ListMembers: the
// subscriber rows joined through subscriber_list_members for one list,
// newest-subscriber first (ORDER BY subscribers.created_at DESC).
func (s *engState) handleListMembersQuery(args []driver.NamedValue) (driver.Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(args) < 1 {
		return nil, fmt.Errorf("engState: list members query without a list id")
	}
	listID := args[0].Value
	memIdx := s.colIndex("subscriber_list_members", "list_id")
	subIdx := s.colIndex("subscriber_list_members", "subscriber_id")

	var subs [][]driver.Value
	for _, m := range s.tables["subscriber_list_members"].rows {
		if !driverValueEqual(m[memIdx], listID) {
			continue
		}
		row := s.rowMapLocked("subscribers", "id", m[subIdx])
		if row == nil {
			continue
		}
		full := make([]driver.Value, len(engColumns["subscribers"]))
		for i, col := range engColumns["subscribers"] {
			full[i] = row[col]
		}
		subs = append(subs, full)
	}
	createdIdx := s.colIndex("subscribers", "created_at")
	idIdx := s.colIndex("subscribers", "id")
	sort.SliceStable(subs, func(a, b int) bool {
		ta, _ := subs[a][createdIdx].(time.Time)
		tb, _ := subs[b][createdIdx].(time.Time)
		if !ta.Equal(tb) {
			return ta.After(tb)
		}
		return subs[a][idIdx].(int64) > subs[b][idIdx].(int64)
	})
	return &engRows{cols: engColumns["subscribers"], rows: subs}, nil
}

func matchEngTable(lowerQuery string) string {
	for _, name := range []string{"automation_node_runs", "automation_trigger_log", "automation_journeys", "subscriber_list_members", "subscriber_list_unsubscribes", "subscriber_lists", "segments", "automations", "subscribers"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `update ` + name + ` `, `delete from "` + name + `"`, `delete from ` + name + ` `, `into "` + name + `"`, `into ` + name + ` `} {
			if strings.Contains(lowerQuery, pat) {
				return name
			}
		}
	}
	return ""
}

// evalWhere returns the rows of table satisfying the query's WHERE clause.
// Only the condition shapes the automation repositories emit are supported;
// anything else is an error so tests fail loudly instead of silently.
func (s *engState) evalWhere(table string, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
	where := ""
	if i := strings.Index(compact, " where "); i >= 0 {
		where = compact[i+len(" where "):]
	} else if strings.HasPrefix(compact, "where ") {
		where = compact[len("where "):]
	}
	// Cut the tail clauses the evaluator does not need.
	for _, cut := range []string{" order by ", " limit ", " for update", " returning "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}
	where = strings.NewReplacer("(", " ", ")", " ").Replace(where)

	conds := strings.Split(where, " and ")
	preds := make([]func([]driver.Value) (bool, error), 0, len(conds))
	for _, cond := range conds {
		cond = strings.TrimSpace(cond)
		if cond == "" {
			continue
		}
		pred, err := s.buildPredicate(table, cond, args)
		if err != nil {
			return nil, err
		}
		preds = append(preds, pred)
	}

	rows := s.tables[table].rows
	out := make([][]driver.Value, 0, len(rows))
	for _, row := range rows {
		ok := true
		for _, pred := range preds {
			match, err := pred(row)
			if err != nil {
				return nil, err
			}
			if !match {
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

func (s *engState) buildPredicate(table, cond string, args []driver.NamedValue) (func([]driver.Value) (bool, error), error) {
	switch {
	case strings.Contains(cond, " like "):
		parts := strings.SplitN(cond, " like ", 2)
		col := engColumnOf(parts[0])
		argIdx, err := engBindVar(parts[1])
		if err != nil {
			return nil, err
		}
		pattern := strings.Trim(argValue(args, argIdx).(string), "%")
		idx := s.colIndex(table, col)
		return func(row []driver.Value) (bool, error) {
			s, ok := row[idx].(string)
			if !ok {
				return false, nil
			}
			return strings.Contains(strings.ToLower(s), pattern), nil
		}, nil
	case strings.HasSuffix(cond, " is null"):
		col := engColumnOf(strings.TrimSuffix(cond, " is null"))
		idx := s.colIndex(table, col)
		return func(row []driver.Value) (bool, error) { return row[idx] == nil, nil }, nil
	case strings.HasSuffix(cond, " is not null"):
		col := engColumnOf(strings.TrimSuffix(cond, " is not null"))
		idx := s.colIndex(table, col)
		return func(row []driver.Value) (bool, error) { return row[idx] != nil, nil }, nil
	}

	eqIdx := strings.Index(cond, " = ")
	if eqIdx < 0 {
		return nil, fmt.Errorf("engState: unsupported where condition %q", cond)
	}
	col := engColumnOf(cond[:eqIdx])
	argIdx, err := engBindVar(cond[eqIdx+3:])
	if err != nil {
		return nil, err
	}
	want := argValue(args, argIdx)
	idx := s.colIndex(table, col)
	return func(row []driver.Value) (bool, error) { return driverValueEqual(row[idx], want), nil }, nil
}

// engColumnOf reduces `"tbl"."col"`, `tbl.col` or `lower(col)` to `col`.
func engColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	expr = strings.Trim(expr, `"`)
	if strings.HasPrefix(expr, "lower(") && strings.HasSuffix(expr, ")") {
		expr = strings.TrimSuffix(strings.TrimPrefix(expr, "lower("), ")")
	}
	return strings.TrimSpace(expr)
}

// engBindVar parses a leading `$N` token (the bindvar may be followed by more
// SQL, e.g. "LIMIT $2 OFFSET $3").
func engBindVar(token string) (int, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "$") {
		return 0, fmt.Errorf("engState: expected a bindvar, got %q", token)
	}
	token = token[1:]
	end := 0
	for end < len(token) && token[end] >= '0' && token[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("engState: bad bindvar %q", token)
	}
	n, err := strconv.Atoi(token[:end])
	if err != nil {
		return 0, fmt.Errorf("engState: bad bindvar %q", token)
	}
	return n, nil
}

func argValue(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("engState: no arg with ordinal %d", n))
}

// driverValueEqual compares two driver.Values with numeric string and time
// coercion.
func driverValueEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, aIsTime := a.(time.Time)
	tb, bIsTime := b.(time.Time)
	if aIsTime || bIsTime {
		return aIsTime && bIsTime && ta.Equal(tb)
	}
	sa, aIsStr := a.(string)
	sb, bIsStr := b.(string)
	if aIsStr || bIsStr {
		if !aIsStr {
			sa = fmt.Sprint(a)
		}
		if !bIsStr {
			sb = fmt.Sprint(b)
		}
		return sa == sb
	}
	ia, aIsInt := toInt64(a)
	ib, bIsInt := toInt64(b)
	if aIsInt && bIsInt {
		return ia == ib
	}
	return a == b
}

func toInt64(v driver.Value) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	}
	return 0, false
}

// sortEngRows applies the ORDER BY the queries use (a single column).
func sortEngRows(s *engState, table string, rows [][]driver.Value, compact string) [][]driver.Value {
	i := strings.Index(compact, " order by ")
	if i < 0 {
		return rows
	}
	rest := compact[i+len(" order by "):]
	end := len(rest)
	for _, cut := range []string{" limit ", " offset ", " for update"} {
		if j := strings.Index(rest, cut); j >= 0 && j < end {
			end = j
		}
	}
	rest = rest[:end]
	fields := strings.Fields(rest)
	col := engColumnOf(fields[0])
	desc := len(fields) > 1 && fields[1] == "desc"
	idx := s.colIndex(table, col)
	if idx < 0 {
		return rows
	}
	out := append([][]driver.Value(nil), rows...)
	sort.SliceStable(out, func(a, b int) bool {
		less := engLess(out[a][idx], out[b][idx])
		if desc {
			return engLess(out[b][idx], out[a][idx])
		}
		return less
	})
	return out
}

func engLess(a, b driver.Value) bool {
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

func applyEngLimitOffset(rows [][]driver.Value, compact string, args []driver.NamedValue) [][]driver.Value {
	offset := 0
	if m := strings.Index(compact, " offset $"); m >= 0 {
		if n, err := engBindVar(compact[m+len(" offset "):]); err == nil {
			if v, ok := toInt64(argValue(args, n)); ok {
				offset = int(v)
			}
		}
	}
	if offset >= len(rows) {
		return nil
	}
	rows = rows[offset:]
	if m := strings.Index(compact, " limit $"); m >= 0 {
		if n, err := engBindVar(compact[m+len(" limit "):]); err == nil {
			if v, ok := toInt64(argValue(args, n)); ok && int(v) < len(rows) {
				rows = rows[:v]
			}
		}
	}
	return rows
}

// handleClaimQuery answers ClaimDueBatchAt and ListOverdue: the raw
// `SELECT aj.* FROM automation_journeys aj JOIN automations a ...` statements.
func (s *engState) handleClaimQuery(args []driver.NamedValue) (driver.Rows, error) {
	status := argValue(args, 1).(string)
	cutoff := argValue(args, 2).(time.Time)
	autoStatus := argValue(args, 3).(string)
	limitArg, _ := toInt64(argValue(args, 4))

	jIdx := func(col string) int { return s.colIndex("automation_journeys", col) }

	var matched [][]driver.Value
	for _, row := range s.tables["automation_journeys"].rows {
		if row[jIdx("status")].(string) != status {
			continue
		}
		sched, ok := row[jIdx("scheduled_at")].(time.Time)
		if !ok || sched.After(cutoff) {
			continue
		}
		autoID := row[jIdx("automation_id")].(int64)
		auto := s.rowMapLocked("automations", "id", autoID)
		if auto == nil {
			continue
		}
		if auto["status"].(string) != autoStatus || auto["deleted_at"] != nil {
			continue
		}
		matched = append(matched, row)
	}
	sort.SliceStable(matched, func(a, b int) bool {
		sa := matched[a][jIdx("scheduled_at")].(time.Time)
		sb := matched[b][jIdx("scheduled_at")].(time.Time)
		return sa.Before(sb)
	})
	if limitArg > 0 && int(limitArg) < len(matched) {
		matched = matched[:limitArg]
	}
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &engRows{cols: engColumns["automation_journeys"], rows: rows}, nil
}

// handleInsert stores an INSERT ... RETURNING row; ON CONFLICT DO NOTHING
// inserts that hit a unique key return an empty result (the conflict path).
func (s *engState) handleInsert(query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	table := matchEngTable(lower)
	if table == "" {
		return nil, fmt.Errorf("engState: insert without a known table: %s", query)
	}
	cols, vals, err := parseInsertEng(query, args)
	if err != nil {
		return nil, err
	}

	returning := []string{}
	if i := strings.Index(lower, " returning "); i >= 0 {
		for _, c := range strings.Split(lower[i+len(" returning "):], ",") {
			returning = append(returning, strings.Trim(strings.TrimSpace(c), `"`))
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.Contains(lower, "on conflict do nothing") {
		if keys, ok := engUniqueKeys[table]; ok {
			for _, row := range s.tables[table].rows {
				conflict := true
				for _, k := range keys {
					idx := s.colIndex(table, k)
					ins := vals[colsIndex(cols, k)]
					if !driverValueEqual(row[idx], ins) {
						conflict = false
						break
					}
				}
				if conflict {
					return &engRows{cols: returning}, nil // zero rows: the CAS-style dedup miss
				}
			}
		}
	}

	row, err := s.insertRow(table, cols, vals)
	if err != nil {
		return nil, err
	}
	rows := make([][]driver.Value, 1)
	out := make([]driver.Value, len(returning))
	for i, c := range returning {
		out[i] = row[s.colIndex(table, c)]
	}
	rows[0] = out
	return &engRows{cols: returning, rows: rows}, nil
}

func colsIndex(cols []string, col string) int {
	for i, c := range cols {
		if c == col {
			return i
		}
	}
	return -1
}

// parseInsertEng extracts the column list and bound values of an INSERT.
func parseInsertEng(query string, args []driver.NamedValue) ([]string, []driver.Value, error) {
	vi := strings.Index(strings.ToLower(query), " values (")
	if vi < 0 {
		return nil, nil, fmt.Errorf("engState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, nil, fmt.Errorf("engState: insert without a column list: %s", query)
	}
	colParts := strings.Split(query[open+1:close], ",")
	cols := make([]string, 0, len(colParts))
	for _, p := range colParts {
		cols = append(cols, strings.Trim(strings.TrimSpace(p), `"`))
	}
	valText := query[vi+len(" values "):]
	// Values run to the closing paren of the VALUES list.
	end := strings.LastIndex(valText, ")")
	if end < 0 {
		return nil, nil, fmt.Errorf("engState: unterminated VALUES: %s", query)
	}
	valParts := strings.Split(valText[1:end], ",")
	if len(valParts) != len(cols) {
		return nil, nil, fmt.Errorf("engState: %d columns but %d values in: %s", len(cols), len(valParts), query)
	}
	vals := make([]driver.Value, len(valParts))
	for i, p := range valParts {
		n, err := engBindVar(p)
		if err != nil {
			return nil, nil, err
		}
		vals[i] = argValue(args, n)
	}
	return cols, vals, nil
}

// handleUpdate applies a parsed UPDATE under its WHERE clause and reports the
// affected row count — the compare-and-swap contract of the journey repo.
func (s *engState) handleUpdate(compact string, args []driver.NamedValue) (driver.Result, error) {
	table := matchEngTable(compact)
	if table == "" {
		return nil, fmt.Errorf("engState: update without a known table: %s", compact)
	}
	setStart := strings.Index(compact, " set ")
	whereStart := strings.Index(compact, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("engState: malformed update: %s", compact)
	}
	setClause := compact[setStart+len(" set ") : whereStart]

	type setter struct {
		idx   int
		value driver.Value
		expr  string
	}
	var setters []setter
	for _, pair := range strings.Split(setClause, ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("engState: malformed SET pair %q in %s", pair, compact)
		}
		col := engColumnOf(pair[:eq])
		valExpr := strings.TrimSpace(pair[eq+1:])
		idx := s.colIndex(table, col)
		if idx < 0 {
			return nil, fmt.Errorf("engState: table %q has no column %q", table, col)
		}
		if strings.HasPrefix(valExpr, "$") {
			n, err := engBindVar(valExpr)
			if err != nil {
				return nil, err
			}
			setters = append(setters, setter{idx: idx, value: argValue(args, n)})
			continue
		}
		// A SQL expression; only retry_count + 1 is ever used.
		if col == "retry_count" && strings.Contains(valExpr, "retry_count + 1") {
			setters = append(setters, setter{idx: idx, expr: "inc"})
			continue
		}
		return nil, fmt.Errorf("engState: unsupported SET expression %q in %s", valExpr, compact)
	}

	matched, err := s.evalWhere(table, compact, args)
	if err != nil {
		return nil, err
	}
	for _, row := range matched {
		for _, st := range setters {
			if st.expr == "inc" {
				cur, _ := toInt64(row[st.idx])
				row[st.idx] = cur + 1
				continue
			}
			row[st.idx] = st.value
		}
	}
	return engResult{rows: int64(len(matched))}, nil
}

func (s *engState) handleDelete(compact string, args []driver.NamedValue) (driver.Result, error) {
	table := matchEngTable(compact)
	if table == "" {
		return nil, fmt.Errorf("engState: delete without a known table: %s", compact)
	}
	matched, err := s.evalWhere(table, compact, args)
	if err != nil {
		return nil, err
	}
	keep := make([][]driver.Value, 0, len(s.tables[table].rows))
	for _, row := range s.tables[table].rows {
		drop := false
		for _, m := range matched {
			if &row[0] == &m[0] {
				drop = true
				break
			}
		}
		if !drop {
			keep = append(keep, row)
		}
	}
	s.tables[table].rows = keep
	return engResult{rows: int64(len(matched))}, nil
}

// engStatRe extracts the stat key from the jsonb_set UPDATE; the plain '{}'
// default in COALESCE(stats, '{}'::jsonb) must not match ([a-z_]+ is
// non-empty). Underscores appear in the R-13 checkpoint path key
// (last_fired_at).
var engStatRe = regexp.MustCompile(`'\{([a-z_]+)\}'`)

// handleIncrementStat emulates the atomic jsonb_set stat UPDATE.
func (s *engState) handleIncrementStat(compact string, args []driver.NamedValue) (driver.Result, error) {
	m := engStatRe.FindStringSubmatch(compact)
	if m == nil {
		return nil, fmt.Errorf("engState: stat update without a stat key: %s", compact)
	}
	stat := m[1]
	matched, err := s.evalWhere("automations", compact, args)
	if err != nil {
		return nil, err
	}
	for _, row := range matched {
		idx := s.colIndex("automations", "stats")
		stats := models.ParseWorkflowStats(row[idx].(string))
		switch stat {
		case "enrolled":
			stats.Enrolled++
		case "completed":
			stats.Completed++
		case "exited":
			stats.Exited++
		case "failed":
			stats.Failed++
		case "last_fired_at":
			// The schedule checkpoint (R-13): to_jsonb($1::text) binds the
			// RFC3339 timestamp as the FIRST argument (the id is second).
			if len(args) < 1 {
				return nil, fmt.Errorf("engState: checkpoint update without a timestamp: %s", compact)
			}
			ts, err := time.Parse(time.RFC3339Nano, fmt.Sprint(args[0].Value))
			if err != nil {
				return nil, fmt.Errorf("engState: bad checkpoint timestamp %v: %w", args[0].Value, err)
			}
			stats.LastFiredAt = &ts
		default:
			return nil, fmt.Errorf("engState: unknown stat %q", stat)
		}
		b, _ := json.Marshal(stats)
		row[idx] = string(b)
	}
	return engResult{rows: int64(len(matched))}, nil
}

// ---------- the test harness ----------

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeProducer struct {
	mu       sync.Mutex
	journeys []uint
	delays   []time.Duration
}

func (p *fakeProducer) EnqueueAutomationRun(journeyID uint, delay time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.journeys = append(p.journeys, journeyID)
	p.delays = append(p.delays, delay)
	return nil
}

func (p *fakeProducer) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.journeys)
}

type emailCall struct {
	automationID    uint
	templateID      uint
	subjectOverride string
	fromOverride    string
	toEmail         string
	data            map[string]any
}

type stubEmailSender struct {
	mu    sync.Mutex
	calls []emailCall
	err   error
	uuid  string
}

func (s *stubEmailSender) SendAutomationEmail(_ context.Context, automationID uint, tmplID uint, subjectOverride, fromOverride, toEmail string, data map[string]any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	s.calls = append(s.calls, emailCall{
		automationID: automationID, templateID: tmplID,
		subjectOverride: subjectOverride, fromOverride: fromOverride,
		toEmail: toEmail, data: data,
	})
	return s.uuid, nil
}

func (s *stubEmailSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubEmailSender) lastCall() emailCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

// engGorm opens the gorm postgres dialect over the fake database/sql driver —
// no SQL ever reaches a server; the fake evaluates the statements.
func engGorm(t *testing.T, state *engState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(engConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open engine fake gorm: %v", err)
	}
	return gdb
}

// newTestEngine wires the real engine over the fake database with the fake
// clock, a recording producer and a stubbed email sender.
func newTestEngine(t *testing.T) (*Engine, *engState, *fakeProducer, *stubEmailSender, *testClock) {
	t.Helper()
	state := newEngState()
	gdb := engGorm(t, state)
	producer := &fakeProducer{}
	sender := &stubEmailSender{uuid: "em-uuid-1"}
	clock := newTestClock()
	e := NewEngine(gdb, producer, sender,
		repositories.NewSubscriberListRepository(gdb),
		repositories.NewSubscriberRepository(gdb))
	e.SetClock(clock.Now)
	return e, state, producer, sender, clock
}

// ---------- fixtures ----------

func marshalNodes(t *testing.T, nodes []*models.AutomationNode) string {
	t.Helper()
	b, err := json.Marshal(nodes)
	if err != nil {
		t.Fatalf("marshal nodes: %v", err)
	}
	return string(b)
}

// linearChain builds trigger -> n1 -> ... -> nN -> done with email nodes.
func linearChain(count int) []*models.AutomationNode {
	nodes := []*models.AutomationNode{triggerNode(strPtr("n1"))}
	for i := 1; i <= count; i++ {
		id := fmt.Sprintf("n%d", i)
		var next *string
		if i < count {
			next = strPtr(fmt.Sprintf("n%d", i+1))
		}
		nodes = append(nodes, testNode(id, NodeTypeEmail, map[string]any{"template_id": 1}, next))
	}
	return nodes
}

// seedAutomation inserts an automation row and returns its id.
func seedAutomation(t *testing.T, state *engState, name, status string, listID *uint, triggerCfg, nodes string) uint {
	vals := map[string]any{
		"user_id":        uint(7),
		"workspace_id":   uint(3),
		"name":           name,
		"status":         status,
		"trigger_config": triggerCfg,
		"nodes":          nodes,
		"root_node_id":   "t",
		"stats":          models.DefaultWorkflowStatsJSON,
		"version":        1,
	}
	if listID != nil {
		vals["list_id"] = *listID
	}
	return uint(engSeed(t, state, "automations", vals))
}

func seedSubscriber(t *testing.T, state *engState) {
	engSeed(t, state, "subscribers", map[string]any{
		"id":            uint(9),
		"user_id":       uint(7),
		"workspace_id":  uint(3),
		"email":         "alice@example.com",
		"name":          "Alice Anderson",
		"status":        "subscribed",
		"custom_fields": `{"plan":"pro"}`,
	})
}

func seedJourney(t *testing.T, state *engState, automationID uint, cursor string, scheduledAt *time.Time, extra map[string]any) uint {
	vals := map[string]any{
		"automation_id":   automationID,
		"workspace_id":    uint(3),
		"subscriber_id":   uint(9),
		"email":           "alice@example.com",
		"current_node_id": cursor,
		"status":          models.AutomationJourneyStatusActive,
		"context_json":    "{}",
		"entered_at":      newTestClock().Now(),
	}
	if scheduledAt != nil {
		vals["scheduled_at"] = *scheduledAt
	}
	for k, v := range extra {
		vals[k] = v
	}
	return uint(engSeed(t, state, "automation_journeys", vals))
}

func readStats(t *testing.T, state *engState, automationID uint) models.WorkflowStats {
	t.Helper()
	row := state.rowMap("automations", int64(automationID))
	if row == nil {
		t.Fatal("automation row disappeared")
	}
	return models.ParseWorkflowStats(row["stats"].(string))
}

func journeyRow(t *testing.T, state *engState, journeyID uint) map[string]driver.Value {
	t.Helper()
	row := state.rowMap("automation_journeys", int64(journeyID))
	if row == nil {
		t.Fatalf("journey %d disappeared", journeyID)
	}
	return row
}

func assertStatus(t *testing.T, state *engState, journeyID uint, want string) {
	t.Helper()
	if got := journeyRow(t, state, journeyID)["status"].(string); got != want {
		t.Fatalf("journey status = %q, want %q", got, want)
	}
}

// ---------- Enroll ----------

func TestEnrollCreatesJourneyStatsAndNudge(t *testing.T) {
	e, state, producer, _, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)

	ev := TriggerEvent{Kind: EventKindListSubscribed, ListID: &listID, Subscriber: &models.Subscriber{
		ID: 9, UserID: 7, WorkspaceID: uintPtr(3), Email: "alice@example.com",
	}}
	if err := e.Enroll(context.Background(), ev); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	if got := state.rowCount("automation_journeys"); got != 1 {
		t.Fatalf("journeys = %d, want 1", got)
	}
	row := journeyRow(t, state, producer.journeys[0])
	if row["automation_id"].(int64) != int64(auto) {
		t.Errorf("journey automation_id = %v, want %d", row["automation_id"], auto)
	}
	if row["current_node_id"].(string) != "t" {
		t.Errorf("journey cursor = %q, want the root t", row["current_node_id"])
	}
	if row["status"].(string) != models.AutomationJourneyStatusActive {
		t.Errorf("journey status = %q, want active", row["status"])
	}
	if row["email"].(string) != "alice@example.com" {
		t.Errorf("journey email = %q", row["email"])
	}
	if sched, ok := row["scheduled_at"].(time.Time); !ok || !sched.Equal(clock.Now()) {
		t.Errorf("scheduled_at = %v, want the enrollment instant %v", row["scheduled_at"], clock.Now())
	}
	if entered, ok := row["entered_at"].(time.Time); !ok || !entered.Equal(clock.Now()) {
		t.Errorf("entered_at = %v, want %v", row["entered_at"], clock.Now())
	}

	stats := readStats(t, state, auto)
	if stats.Enrolled != 1 {
		t.Errorf("stats.enrolled = %d, want 1", stats.Enrolled)
	}

	// The trigger node's run row is written and completed at enrollment.
	if got := state.rowCount("automation_node_runs"); got != 1 {
		t.Fatalf("node runs = %d, want 1 (the trigger row)", got)
	}

	// The execution nudge is queued right away.
	if producer.calls() != 1 {
		t.Fatalf("producer calls = %d, want 1", producer.calls())
	}
	if p := producer.delays[0]; p != enrollNudgeDelay {
		t.Errorf("nudge delay = %v, want %v", p, enrollNudgeDelay)
	}
}

func TestEnrollOnceDedupSkipsSecondEvent(t *testing.T) {
	e, state, producer, _, _ := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)
	ev := TriggerEvent{Kind: EventKindListSubscribed, ListID: &listID, Subscriber: &models.Subscriber{
		ID: 9, UserID: 7, WorkspaceID: uintPtr(3), Email: "alice@example.com",
	}}

	if err := e.Enroll(context.Background(), ev); err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	if err := e.Enroll(context.Background(), ev); err != nil {
		t.Fatalf("second Enroll: %v", err)
	}

	if got := state.rowCount("automation_journeys"); got != 1 {
		t.Fatalf("journeys = %d, want 1 (once-frequency dedup)", got)
	}
	if got := state.rowCount("automation_trigger_log"); got != 1 {
		t.Fatalf("trigger log rows = %d, want 1", got)
	}
	if producer.calls() != 1 {
		t.Errorf("producer calls = %d, want 1", producer.calls())
	}
	if stats := readStats(t, state, auto); stats.Enrolled != 1 {
		t.Errorf("stats.enrolled = %d, want 1", stats.Enrolled)
	}
}

func TestEnrollEveryTimeCreatesSecondJourney(t *testing.T) {
	e, state, producer, _, _ := newTestEngine(t)
	listID := uint(3)
	seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"every_time"}`,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)
	ev := TriggerEvent{Kind: EventKindListSubscribed, ListID: &listID, Subscriber: &models.Subscriber{
		ID: 9, UserID: 7, WorkspaceID: uintPtr(3), Email: "alice@example.com",
	}}

	_ = e.Enroll(context.Background(), ev)
	_ = e.Enroll(context.Background(), ev)

	if got := state.rowCount("automation_journeys"); got != 2 {
		t.Fatalf("journeys = %d, want 2 (every_time)", got)
	}
	if got := state.rowCount("automation_trigger_log"); got != 0 {
		t.Errorf("every_time must not write trigger-log dedup rows, got %d", got)
	}
	if producer.calls() != 2 {
		t.Errorf("producer calls = %d, want 2", producer.calls())
	}
}

func TestEnrollConditionsFalseSkips(t *testing.T) {
	e, state, producer, _, _ := newTestEngine(t)
	listID := uint(3)
	seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once","conditions":[{"field":"email","operator":"equals","value":"bob@example.com"}]}`,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)
	ev := TriggerEvent{Kind: EventKindListSubscribed, ListID: &listID, Subscriber: &models.Subscriber{
		ID: 9, UserID: 7, WorkspaceID: uintPtr(3), Email: "alice@example.com",
	}}

	_ = e.Enroll(context.Background(), ev)

	if got := state.rowCount("automation_journeys"); got != 0 {
		t.Fatalf("conditions false must not enroll, got %d journeys", got)
	}
	if got := state.rowCount("automation_trigger_log"); got != 0 {
		t.Errorf("conditions false must not burn the dedup slot, got %d trigger-log rows", got)
	}
	if producer.calls() != 0 {
		t.Errorf("producer calls = %d, want 0", producer.calls())
	}
}

func TestEnrollSkipsDraftPausedAndNonMatchingList(t *testing.T) {
	e, state, producer, _, _ := newTestEngine(t)
	listID := uint(3)
	draftCfg := `{"event_kind":"list.subscribed","frequency":"once"}`
	seedAutomation(t, state, "Draft", models.AutomationStatusDraft, &listID, draftCfg, marshalNodes(t, linearChain(1)))
	seedAutomation(t, state, "Paused", models.AutomationStatusPaused, &listID, draftCfg, marshalNodes(t, linearChain(1)))
	seedAutomation(t, state, "OtherList", models.AutomationStatusLive, uintPtr(9), draftCfg, marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)

	ev := TriggerEvent{Kind: EventKindListSubscribed, ListID: &listID, Subscriber: &models.Subscriber{
		ID: 9, UserID: 7, WorkspaceID: uintPtr(3), Email: "alice@example.com",
	}}
	_ = e.Enroll(context.Background(), ev)

	if got := state.rowCount("automation_journeys"); got != 0 {
		t.Fatalf("no automation may match, got %d journeys", got)
	}
	if producer.calls() != 0 {
		t.Errorf("producer calls = %d, want 0", producer.calls())
	}
}

func TestEnrollWithoutSubscriberAndEmptyKind(t *testing.T) {
	e, state, producer, _, _ := newTestEngine(t)
	listID := uint(3)
	seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`, marshalNodes(t, linearChain(1)))

	// No subscriber: nothing to enroll, no error.
	if err := e.Enroll(context.Background(), TriggerEvent{Kind: EventKindListSubscribed, ListID: &listID}); err != nil {
		t.Fatalf("Enroll without subscriber must be a no-op, got %v", err)
	}
	// Empty kind: same.
	if err := e.Enroll(context.Background(), TriggerEvent{Subscriber: &models.Subscriber{ID: 9}}); err != nil {
		t.Fatalf("Enroll without kind must be a no-op, got %v", err)
	}
	if got := state.rowCount("automation_journeys"); got != 0 {
		t.Fatalf("journeys = %d, want 0", got)
	}
	if producer.calls() != 0 {
		t.Errorf("producer calls = %d, want 0", producer.calls())
	}
}

// ---------- ExecuteJourney / ProcessBatch ----------

func TestProcessBatchExecutesTriggerEmailWorkflow(t *testing.T) {
	e, state, _, sender, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)
	now := clock.Now()
	journeyID := seedJourney(t, state, auto, "t", &now, map[string]any{"context_json": `{"order_id":"A-1"}`})

	if err := e.ProcessBatch(context.Background(), 10); err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}

	assertStatus(t, state, journeyID, models.AutomationJourneyStatusCompleted)
	row := journeyRow(t, state, journeyID)
	if row["current_node_id"].(string) != "" {
		t.Errorf("terminal cursor = %q, want empty", row["current_node_id"])
	}
	if row["scheduled_at"] != nil {
		t.Errorf("completed journey must have a nil scheduled_at, got %v", row["scheduled_at"])
	}
	if completed, ok := row["completed_at"].(time.Time); !ok || !completed.Equal(now) {
		t.Errorf("completed_at = %v, want the clock %v", row["completed_at"], now)
	}

	stats := readStats(t, state, auto)
	if stats.Completed != 1 || stats.Enrolled != 0 {
		t.Errorf("stats = %+v, want completed=1 enrolled=0", stats)
	}

	// The email node executed exactly once with the journey's data document.
	if sender.callCount() != 1 {
		t.Fatalf("email sender calls = %d, want 1", sender.callCount())
	}
	call := sender.lastCall()
	if call.automationID != auto || call.templateID != 1 || call.toEmail != "alice@example.com" {
		t.Errorf("sender args = automation %d template %d to %q", call.automationID, call.templateID, call.toEmail)
	}
	if call.data["automation_name"] != "Welcome" {
		t.Errorf("data.automation_name = %v, want Welcome", call.data["automation_name"])
	}
	if call.data["contact_email"] != "alice@example.com" {
		t.Errorf("data.contact_email = %v", call.data["contact_email"])
	}
	if call.data["contact_name"] != "Alice Anderson" {
		t.Errorf("data.contact_name = %v", call.data["contact_name"])
	}
	if call.data["order_id"] != "A-1" {
		t.Errorf("event properties must be flattened into the data map, got %v", call.data["order_id"])
	}
	if ev, ok := call.data["event"].(map[string]any); !ok || ev["order_id"] != "A-1" {
		t.Errorf("data.event = %v, want the journey context", call.data["event"])
	}
	if cf, ok := call.data["custom_fields"].(map[string]any); !ok || cf["plan"] != "pro" {
		t.Errorf("data.custom_fields = %v, want the subscriber custom fields", call.data["custom_fields"])
	}

	// The email node's run row: entered, then completed with its output.
	if got := state.rowCount("automation_node_runs"); got != 1 {
		t.Fatalf("node runs = %d, want 1", got)
	}
	state.mu.Lock()
	runs := state.tables["automation_node_runs"].rows
	runAction := runs[0][state.colIndex("automation_node_runs", "action")].(string)
	runOutput := runs[0][state.colIndex("automation_node_runs", "output")].(string)
	state.mu.Unlock()
	if runAction != models.AutomationRunActionCompleted {
		t.Errorf("run action = %q, want completed", runAction)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(runOutput), &output); err != nil {
		t.Fatalf("run output not JSON: %q", runOutput)
	}
	if output["email_uuid"] != "em-uuid-1" {
		t.Errorf("run output = %v, want email_uuid em-uuid-1", output)
	}
}

func TestExecuteJourneyParksOnDelayThenResumes(t *testing.T) {
	e, state, _, sender, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, []*models.AutomationNode{
			triggerNode(strPtr("d")),
			testNode("d", NodeTypeDelay, map[string]any{"duration": 30, "unit": models.AutomationDelayMinutes}, strPtr("e1")),
			testNode("e1", NodeTypeEmail, map[string]any{"template_id": 2}, nil),
		}))
	seedSubscriber(t, state)
	now := clock.Now()
	journeyID := seedJourney(t, state, auto, "t", &now, nil)

	if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
		t.Fatalf("ExecuteJourney: %v", err)
	}

	// Parked: active, cursor past the delay, scheduled_at 30 minutes out.
	assertStatus(t, state, journeyID, models.AutomationJourneyStatusActive)
	row := journeyRow(t, state, journeyID)
	if row["current_node_id"].(string) != "e1" {
		t.Errorf("parked cursor = %q, want e1", row["current_node_id"])
	}
	want := now.Add(30 * time.Minute)
	if sched, ok := row["scheduled_at"].(time.Time); !ok || !sched.Equal(want) {
		t.Errorf("scheduled_at = %v, want %v", row["scheduled_at"], want)
	}
	if sender.callCount() != 0 {
		t.Errorf("parked journey must not execute the email node yet, got %d calls", sender.callCount())
	}

	// After the delay the scheduler resumes and completes the journey.
	clock.Advance(31 * time.Minute)
	if err := e.ProcessBatch(context.Background(), 10); err != nil {
		t.Fatalf("ProcessBatch after delay: %v", err)
	}
	assertStatus(t, state, journeyID, models.AutomationJourneyStatusCompleted)
	if sender.callCount() != 1 {
		t.Fatalf("email sender calls = %d, want 1 after the delay elapsed", sender.callCount())
	}
	if stats := readStats(t, state, auto); stats.Completed != 1 {
		t.Errorf("stats.completed = %d, want 1", stats.Completed)
	}
}

func TestExecuteJourneyTickBudgetReParks(t *testing.T) {
	e, state, _, _, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Long", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(12)))
	seedSubscriber(t, state)
	now := clock.Now()
	journeyID := seedJourney(t, state, auto, "t", &now, nil)

	if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
		t.Fatalf("ExecuteJourney: %v", err)
	}

	// maxNodesPerTick (10) nodes processed: trigger + n1..n9. The journey is
	// re-parked due-now at n10, still active.
	assertStatus(t, state, journeyID, models.AutomationJourneyStatusActive)
	row := journeyRow(t, state, journeyID)
	if row["current_node_id"].(string) != "n10" {
		t.Errorf("cursor after the tick budget = %q, want n10", row["current_node_id"])
	}
	if sched, ok := row["scheduled_at"].(time.Time); !ok || !sched.Equal(now) {
		t.Errorf("re-park scheduled_at = %v, want %v (due now)", row["scheduled_at"], now)
	}
	if got := state.rowCount("automation_node_runs"); got != maxNodesPerTick-1 {
		t.Errorf("node runs = %d, want %d (n1..n9; the trigger row belongs to enrollment)", got, maxNodesPerTick-1)
	}

	// The next execution runs the remaining nodes and completes.
	if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
		t.Fatalf("second ExecuteJourney: %v", err)
	}
	assertStatus(t, state, journeyID, models.AutomationJourneyStatusCompleted)
	if got := state.rowCount("automation_node_runs"); got != 12 {
		t.Errorf("node runs after completion = %d, want 12", got)
	}
}

func TestNodeErrorSchedulesRetryWithBackoff(t *testing.T) {
	e, state, _, sender, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)
	sender.err = errors.New("smtp: mailbox unavailable")
	now := clock.Now()
	journeyID := seedJourney(t, state, auto, "t", &now, nil)

	if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
		t.Fatalf("a node error must be contained, got %v", err)
	}

	assertStatus(t, state, journeyID, models.AutomationJourneyStatusActive)
	row := journeyRow(t, state, journeyID)
	if got, _ := row["retry_count"].(int64); got != 1 {
		t.Errorf("retry_count = %v, want 1", row["retry_count"])
	}
	want := now.Add(2 * time.Minute) // 2^retryCount minutes backoff
	if sched, ok := row["scheduled_at"].(time.Time); !ok || !sched.Equal(want) {
		t.Errorf("scheduled_at = %v, want the backoff slot %v", row["scheduled_at"], want)
	}
	// Node errors are sanitized at the write boundary before they are stored
	// (issue #7): the raw SMTP detail stays in the server log, the stored
	// copy is the guard's canonical text.
	if lastErr, _ := row["last_error"].(string); lastErr != webhook.SanitizeDeliveryError(`email node "n1": smtp: mailbox unavailable`) {
		t.Errorf("last_error = %q, want the sanitized node error", lastErr)
	}
	if stats := readStats(t, state, auto); stats.Failed != 0 {
		t.Errorf("a retrying journey must not count as failed yet, stats = %+v", stats)
	}
}

func TestNodeErrorExhaustsRetriesAndFailsJourney(t *testing.T) {
	e, state, _, sender, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)
	sender.err = errors.New("smtp: mailbox unavailable")
	now := clock.Now()
	journeyID := seedJourney(t, state, auto, "t", &now, map[string]any{"retry_count": maxRetries - 1})

	if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
		t.Fatalf("a node error must be contained, got %v", err)
	}

	assertStatus(t, state, journeyID, models.AutomationJourneyStatusFailed)
	row := journeyRow(t, state, journeyID)
	// The failure path does not bump retry_count again — the budget was spent.
	if got, _ := row["retry_count"].(int64); got != int64(maxRetries-1) {
		t.Errorf("retry_count = %v, want %d (unchanged by the permanent failure)", row["retry_count"], maxRetries-1)
	}
	// Same write boundary: the permanently failed journey stores the sanitized
	// text, never the raw SMTP detail.
	if lastErr, _ := row["last_error"].(string); lastErr != webhook.SanitizeDeliveryError(`email node "n1": smtp: mailbox unavailable`) {
		t.Errorf("last_error = %q, want the sanitized node error", lastErr)
	}
	if row["scheduled_at"] != nil {
		t.Errorf("a failed journey must not stay scheduled, got %v", row["scheduled_at"])
	}
	if stats := readStats(t, state, auto); stats.Failed != 1 {
		t.Errorf("stats.failed = %d, want 1", stats.Failed)
	}
}

func TestExecuteJourneyFrozenWhenAutomationNotLive(t *testing.T) {
	for _, status := range []string{models.AutomationStatusDraft, models.AutomationStatusPaused} {
		t.Run(status, func(t *testing.T) {
			e, state, _, sender, clock := newTestEngine(t)
			listID := uint(3)
			auto := seedAutomation(t, state, "Welcome", status, &listID,
				`{"event_kind":"list.subscribed","frequency":"once"}`,
				marshalNodes(t, linearChain(1)))
			seedSubscriber(t, state)
			now := clock.Now()
			journeyID := seedJourney(t, state, auto, "t", &now, nil)

			if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
				t.Fatalf("ExecuteJourney on a %s automation: %v", status, err)
			}

			// Untouched: still parked at the root, due, active, no run rows.
			assertStatus(t, state, journeyID, models.AutomationJourneyStatusActive)
			row := journeyRow(t, state, journeyID)
			if row["current_node_id"].(string) != "t" {
				t.Errorf("cursor moved to %q on a frozen automation", row["current_node_id"])
			}
			if sched, ok := row["scheduled_at"].(time.Time); !ok || !sched.Equal(now) {
				t.Errorf("scheduled_at = %v, want %v", row["scheduled_at"], now)
			}
			if got := state.rowCount("automation_node_runs"); got != 0 {
				t.Errorf("run rows = %d, want 0", got)
			}
			if sender.callCount() != 0 {
				t.Errorf("email sender calls = %d, want 0", sender.callCount())
			}
		})
	}
}

func TestExecuteJourneyExitsWhenSubscriberMissing(t *testing.T) {
	e, state, _, sender, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	now := clock.Now()
	// Subscriber 999 was never seeded: deleted after enrollment.
	journeyID := seedJourney(t, state, auto, "t", &now, map[string]any{"subscriber_id": uint(999)})

	if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
		t.Fatalf("ExecuteJourney: %v", err)
	}

	assertStatus(t, state, journeyID, models.AutomationJourneyStatusExited)
	row := journeyRow(t, state, journeyID)
	if row["exit_reason"].(string) != models.AutomationExitReasonContactMissing {
		t.Errorf("exit_reason = %q, want contact_missing", row["exit_reason"])
	}
	if sender.callCount() != 0 {
		t.Errorf("email sender calls = %d, want 0", sender.callCount())
	}
	if stats := readStats(t, state, auto); stats.Exited != 1 {
		t.Errorf("stats.exited = %d, want 1", stats.Exited)
	}
}

func TestExecuteJourneyExitsOnMissingNode(t *testing.T) {
	e, state, _, _, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)
	now := clock.Now()
	// The cursor points at a node the (edited) workflow no longer has.
	journeyID := seedJourney(t, state, auto, "ghost", &now, nil)

	if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
		t.Fatalf("ExecuteJourney: %v", err)
	}

	assertStatus(t, state, journeyID, models.AutomationJourneyStatusExited)
	row := journeyRow(t, state, journeyID)
	if row["exit_reason"].(string) != models.AutomationExitReasonNodeDeleted {
		t.Errorf("exit_reason = %q, want automation_node_deleted", row["exit_reason"])
	}
	if stats := readStats(t, state, auto); stats.Exited != 1 {
		t.Errorf("stats.exited = %d, want 1", stats.Exited)
	}
}

func TestExecuteJourneyAbortsWhenCASLost(t *testing.T) {
	e, state, _, sender, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)
	now := clock.Now()
	// The journey starts at the email node so the entered run row is the
	// race marker.
	journeyID := seedJourney(t, state, auto, "n1", &now, nil)

	fired := false
	state.hook = func(lowerQuery string) {
		if fired || !strings.Contains(lowerQuery, `insert into "automation_node_runs"`) {
			return
		}
		fired = true
		// A concurrent executor (or manual exit) finishes the journey while
		// the node is mid-flight.
		state.setCells("automation_journeys", int64(journeyID), map[string]any{
			"status":      models.AutomationJourneyStatusExited,
			"exit_reason": "concurrent_exit",
		})
	}

	if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
		t.Fatalf("ExecuteJourney must absorb the CAS loss, got %v", err)
	}

	// The journey stays exited with its concurrent reason; the cursor was
	// never advanced and the run row never flipped to completed.
	assertStatus(t, state, journeyID, models.AutomationJourneyStatusExited)
	row := journeyRow(t, state, journeyID)
	if row["exit_reason"].(string) != "concurrent_exit" {
		t.Errorf("exit_reason = %q, want concurrent_exit (the CAS loser must not rewrite it)", row["exit_reason"])
	}
	if row["current_node_id"].(string) != "n1" {
		t.Errorf("cursor = %q, want n1 (no advance after losing the CAS)", row["current_node_id"])
	}
	state.mu.Lock()
	runs := state.tables["automation_node_runs"].rows
	runActions := make([]string, 0, len(runs))
	for _, r := range runs {
		runActions = append(runActions, r[state.colIndex("automation_node_runs", "action")].(string))
	}
	state.mu.Unlock()
	if len(runActions) != 1 {
		t.Fatalf("run rows = %d, want 1", len(runActions))
	}
	if runActions[0] != models.AutomationRunActionEntered {
		t.Errorf("run action = %q, want entered (completion must be skipped after a CAS loss)", runActions[0])
	}
	if sender.callCount() != 1 {
		t.Errorf("email sender calls = %d, want 1 (the node did execute)", sender.callCount())
	}
}

func TestExecuteJourneyIgnoresFinishedJourney(t *testing.T) {
	e, state, _, sender, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)
	now := clock.Now()
	journeyID := seedJourney(t, state, auto, "t", &now, map[string]any{
		"status": models.AutomationJourneyStatusCompleted,
	})

	if err := e.ExecuteJourney(context.Background(), journeyID); err != nil {
		t.Fatalf("ExecuteJourney: %v", err)
	}

	assertStatus(t, state, journeyID, models.AutomationJourneyStatusCompleted)
	if sender.callCount() != 0 {
		t.Errorf("email sender calls = %d, want 0", sender.callCount())
	}
	if stats := readStats(t, state, auto); stats.Completed != 0 {
		t.Errorf("stats.completed = %d, want 0", stats.Completed)
	}
}

// ---------- Sweep ----------

func TestSweepReenqueuesOverdueJourneys(t *testing.T) {
	e, state, producer, _, clock := newTestEngine(t)
	listID := uint(3)
	auto := seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	now := clock.Now()
	// Overdue beyond the sweep grace on a live automation: re-enqueued.
	overdue := seedJourney(t, state, auto, "t", timePtr(now.Add(-10*time.Minute)), nil)
	// Within the sweep grace: not re-enqueued.
	seedJourney(t, state, auto, "t", timePtr(now.Add(-1*time.Minute)), nil)
	// Overdue, but its automation is paused: never re-enqueued.
	paused := seedAutomation(t, state, "Paused", models.AutomationStatusPaused, &listID,
		`{"event_kind":"list.subscribed","frequency":"once"}`,
		marshalNodes(t, linearChain(1)))
	seedJourney(t, state, paused, "t", timePtr(now.Add(-10*time.Minute)), nil)

	if err := e.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if producer.calls() != 1 {
		t.Fatalf("sweep enqueues = %d, want 1", producer.calls())
	}
	if producer.journeys[0] != overdue {
		t.Errorf("sweep enqueued journey %d, want the overdue %d", producer.journeys[0], overdue)
	}
}
