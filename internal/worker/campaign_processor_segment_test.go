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

package worker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/hibiken/asynq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The campaign:start segment branch is exercised end to end against a fake
// database/sql driver (the automation_handler_test.go / segment_handler_test.go
// arrangement): it stores inserted rows, evaluates the simple equality WHERE
// clauses these tests use, and records every query so the tests can pin that
// the segment audience is resolved through the condition path (parameterized,
// workspace-scoped) rather than the list path.

var cwTestTime = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

// cwSegmentCondition is a condition the fake evaluator can execute: one AND
// group with a single email equality rule.
var cwSegmentCondition = `{"logic":"and","groups":[{"logic":"and","rules":[{"field":"email","operator":"equals","value":"alice@example.com"}]}]}`

// ---------- the fake database ----------

var cwColumns = map[string][]string{
	"campaigns": {"id", "user_id", "workspace_id", "name", "subject", "from_email", "from_name", "template_id",
		"template_version_id", "language", "template_data", "status", "list_id", "segment_id", "send_rate",
		"send_at_local_time", "ab_test_enabled", "ab_test_variants", "ab_test_winner", "scheduled_at",
		"started_at", "completed_at", "created_at", "updated_at", "deleted_at", "snapshot"},
	"segments":         {"id", "user_id", "workspace_id", "name", "description", "condition", "created_at", "updated_at"},
	"subscriber_lists": {"id", "user_id", "workspace_id", "name", "description", "type", "filter_rules", "created_at", "updated_at"},
	"subscribers":      {"id", "user_id", "workspace_id", "email", "name", "status", "custom_fields", "timezone", "language", "subscribed_at", "unsubscribed_at", "created_at", "updated_at"},
	"campaign_messages": {"id", "campaign_id", "subscriber_id", "email_id", "status", "error_message", "variant",
		"sent_at", "opened_at", "clicked_at", "bounced_at", "unsubscribed_at", "created_at"},
}

var cwDefaults = map[string]map[string]func() driver.Value{
	"campaigns": {
		"status":           func() driver.Value { return models.CampaignStatusDraft },
		"language":         func() driver.Value { return "en" },
		"template_data":    func() driver.Value { return "{}" },
		"ab_test_variants": func() driver.Value { return "[]" },
		"snapshot":         func() driver.Value { return "{}" },
		"list_id":          func() driver.Value { return int64(0) },
		"send_rate":        func() driver.Value { return int64(0) },
		"deleted_at":       func() driver.Value { return nil },
	},
	"segments": {"condition": func() driver.Value { return "{}" }},
	"subscribers": {
		"status":        func() driver.Value { return "subscribed" },
		"custom_fields": func() driver.Value { return "{}" },
		"name":          func() driver.Value { return "" },
	},
	"campaign_messages": {
		"status":        func() driver.Value { return models.CampaignMsgPending },
		"error_message": func() driver.Value { return "" },
		"variant":       func() driver.Value { return "" },
	},
}

type cwTable struct {
	rows [][]driver.Value
}

type cwState struct {
	mu      sync.Mutex
	tables  map[string]*cwTable
	seq     int64
	queries []string
}

func newCWState() *cwState {
	tables := make(map[string]*cwTable, len(cwColumns))
	for name := range cwColumns {
		tables[name] = &cwTable{}
	}
	return &cwState{tables: tables, seq: 100}
}

func (s *cwState) colIndex(table, col string) int {
	for i, c := range cwColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

const cwTableCampaignMessages = "campaign_messages"

func (s *cwState) rowCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tables[cwTableCampaignMessages].rows)
}

// rowsByColumn returns the rows whose column equals the value.
func (s *cwState) rowsByColumn(table, col string, val int64) [][]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.colIndex(table, col)
	var out [][]driver.Value
	for _, row := range s.tables[table].rows {
		if v, ok := row[idx].(int64); ok && v == val {
			out = append(out, append([]driver.Value(nil), row...))
		}
	}
	return out
}

// columnByID returns one cell of the row with the given id.
func (s *cwState) columnByID(table, column string, id int64) driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	idIdx := s.colIndex(table, "id")
	colIdx := s.colIndex(table, column)
	for _, row := range s.tables[table].rows {
		if v, ok := row[idIdx].(int64); ok && v == id {
			return row[colIdx]
		}
	}
	return nil
}

func (s *cwState) recordedQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func cwVal(v any) driver.Value {
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
		panic(fmt.Sprintf("cwVal: unsupported type %T", v))
	}
}

func cwSeed(t *testing.T, s *cwState, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := cwColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := cwDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for col, v := range vals {
		idx := s.colIndex(table, col)
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, col)
		}
		row[idx] = cwVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

type cwConn struct{ s *cwState }

func (c *cwConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *cwConn) Close() error                        { return nil }
func (c *cwConn) Begin() (driver.Tx, error)           { return cwTx{}, nil }

type cwTx struct{}

func (cwTx) Commit() error   { return nil }
func (cwTx) Rollback() error { return nil }

type cwResult struct{ rows int64 }

func (r cwResult) LastInsertId() (int64, error) { return 0, nil }
func (r cwResult) RowsAffected() (int64, error) { return r.rows, nil }

type cwRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *cwRows) Columns() []string { return r.cols }
func (r *cwRows) Close() error      { return nil }
func (r *cwRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type cwConnector struct{ state *cwState }

func (c cwConnector) Connect(context.Context) (driver.Conn, error) {
	return &cwConn{s: c.state}, nil
}
func (c cwConnector) Driver() driver.Driver { return cwDrv{} }

type cwDrv struct{}

func (cwDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func cwGorm(t *testing.T, state *cwState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(cwConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open campaign worker fake gorm: %v", err)
	}
	return gdb
}

func (c *cwConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.record(query)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if strings.Contains(lower, "insert into") {
		return c.s.handleInsert(query, args)
	}
	table := cwTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("cwState: cannot route query: %s", query)
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(lower, "count(*)") {
		return &cwRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	matched = cwOrder(c.s, table, matched, lower)
	matched = cwLimitOffset(matched, lower, args)
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &cwRows{cols: cwColumns[table], rows: rows}, nil
}

func (c *cwConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.record(query)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := cwTableOf(lower)
	if table == "" || !strings.Contains(lower, "update ") {
		return nil, fmt.Errorf("cwState: unsupported exec: %s", query)
	}
	return c.s.handleUpdate(table, lower, args)
}

func (c *cwConn) record(query string) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.queries = append(c.s.queries, query)
}

func cwTableOf(lowerQuery string) string {
	for _, name := range []string{"campaign_messages", "subscriber_lists", "campaigns", "segments", "subscribers"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`} {
			if strings.Contains(lowerQuery, pat) {
				return name
			}
		}
	}
	return ""
}

func (s *cwState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
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
			idx := s.colIndex(table, cwColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
		case strings.Contains(cond, " in "):
			// GORM preloads render `id IN ($1,$2,...)` — the R-08/R-09 tests
			// exercise queries that preload subscribers.
			ci := s.colIndex(table, cwColumnOf(cond[:strings.Index(cond, " in ")]))
			rest := strings.TrimSpace(cond[strings.Index(cond, " in ")+len(" in "):])
			if strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")") {
				rest = strings.TrimSpace(rest[1 : len(rest)-1])
			}
			var vals []driver.Value
			for _, tok := range strings.Split(rest, ",") {
				n, err := cwBindToken(tok)
				if err != nil {
					return nil, fmt.Errorf("cwState: bad IN list in %q: %w", cond, err)
				}
				vals = append(vals, cwArg(args, n))
			}
			preds = append(preds, func(row []driver.Value) bool {
				for _, v := range vals {
					if cwEqual(row[ci], v) {
						return true
					}
				}
				return false
			})
		default:
			eq := strings.Index(cond, " = ")
			if eq < 0 {
				return nil, fmt.Errorf("cwState: unsupported condition %q", cond)
			}
			idx := s.colIndex(table, cwColumnOf(cond[:eq]))
			n, err := strconv.Atoi(strings.TrimSpace(cond[eq+3:])[1:])
			if err != nil {
				return nil, fmt.Errorf("cwState: bad bindvar in %q", cond)
			}
			want := cwArg(args, n)
			preds = append(preds, func(row []driver.Value) bool { return cwEqual(row[idx], want) })
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

func cwColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func cwArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("cwState: no arg with ordinal %d", n))
}

func cwEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
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

func cwOrder(s *cwState, table string, rows [][]driver.Value, compact string) [][]driver.Value {
	i := strings.Index(compact, " order by ")
	if i < 0 {
		return rows
	}
	rest := compact[i+len(" order by "):]
	if j := strings.Index(rest, " limit "); j >= 0 {
		rest = rest[:j]
	}
	fields := strings.Fields(rest)
	idx := s.colIndex(table, cwColumnOf(fields[0]))
	desc := len(fields) > 1 && fields[1] == "desc"
	out := append([][]driver.Value(nil), rows...)
	sort.SliceStable(out, func(a, b int) bool {
		if desc {
			return cwLess(out[b][idx], out[a][idx])
		}
		return cwLess(out[a][idx], out[b][idx])
	})
	return out
}

func cwLess(a, b driver.Value) bool {
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

func cwBindToken(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$") {
		return 0, fmt.Errorf("cwState: expected a bindvar, got %q", s)
	}
	s = s[1:]
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("cwState: bad bindvar %q", s)
	}
	return strconv.Atoi(s[:end])
}

func cwLimitOffset(rows [][]driver.Value, compact string, args []driver.NamedValue) [][]driver.Value {
	offset := 0
	if m := strings.Index(compact, " offset $"); m >= 0 {
		if n, err := cwBindToken(compact[m+len(" offset "):]); err == nil {
			if v, ok := cwArg(args, n).(int64); ok {
				offset = int(v)
			}
		}
	}
	if offset >= len(rows) {
		return nil
	}
	rows = rows[offset:]
	if m := strings.Index(compact, " limit $"); m >= 0 {
		if n, err := cwBindToken(compact[m+len(" limit "):]); err == nil {
			if v, ok := cwArg(args, n).(int64); ok && int(v) < len(rows) {
				rows = rows[:v]
			}
		}
	}
	return rows
}

func (s *cwState) handleInsert(query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	table := cwTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("cwState: insert without a known table: %s", query)
	}
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("cwState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, fmt.Errorf("cwState: insert without a column list: %s", query)
	}
	colParts := strings.Split(query[open+1:close], ",")
	cols := make([]string, 0, len(colParts))
	for _, p := range colParts {
		cols = append(cols, strings.Trim(strings.TrimSpace(p), `"`))
	}
	valText := query[vi+len(" values "):]
	end := strings.LastIndex(valText, ")")
	if end < 0 {
		return nil, fmt.Errorf("cwState: insert without a closing paren: %s", query)
	}
	// Multi-row inserts (CreateInBatches) render one tuple per row; split the
	// top-level tuples so batched campaign-message creation lands row by row.
	tuples := strings.Split(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(valText[:end+1]), "("), ")"), "),(")
	if len(tuples) == 0 {
		return nil, fmt.Errorf("cwState: insert without values: %s", query)
	}
	inserted := make([][]driver.Value, 0, len(tuples))
	for _, tuple := range tuples {
		valParts := strings.Split(tuple, ",")
		if len(valParts) != len(cols) {
			return nil, fmt.Errorf("cwState: %d columns but %d values", len(cols), len(valParts))
		}
		vals := make([]driver.Value, len(valParts))
		for i, p := range valParts {
			n, err := strconv.Atoi(strings.TrimSpace(p)[1:])
			if err != nil {
				return nil, err
			}
			vals[i] = cwArg(args, n)
		}
		inserted = append(inserted, vals)
	}

	var returning []string
	if i := strings.Index(lower, " returning "); i >= 0 {
		for _, c := range strings.Split(lower[i+len(" returning "):], ",") {
			returning = append(returning, strings.Trim(strings.TrimSpace(c), `"`))
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	spec := cwColumns[table]
	var lastRow []driver.Value
	for _, vals := range inserted {
		row := make([]driver.Value, len(spec))
		for i, col := range spec {
			row[i] = nil
			if def, ok := cwDefaults[table][col]; ok {
				row[i] = def()
			}
		}
		for j, col := range cols {
			row[s.colIndex(table, col)] = vals[j]
		}
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
		s.tables[table].rows = append(s.tables[table].rows, row)
		lastRow = row
	}

	out := make([]driver.Value, len(returning))
	for i, c := range returning {
		out[i] = lastRow[s.colIndex(table, c)]
	}
	return &cwRows{cols: returning, rows: [][]driver.Value{out}}, nil
}

func (s *cwState) handleUpdate(table, compact string, args []driver.NamedValue) (driver.Result, error) {
	setStart := strings.Index(compact, " set ")
	whereStart := strings.Index(compact, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("cwState: malformed update: %s", compact)
	}
	type setter struct {
		idx int
		val driver.Value
	}
	var setters []setter
	for _, pair := range strings.Split(compact[setStart+len(" set "):whereStart], ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("cwState: malformed SET pair %q", pair)
		}
		idx := s.colIndex(table, cwColumnOf(pair[:eq]))
		if idx < 0 {
			return nil, fmt.Errorf("cwState: unknown column in SET %q", pair)
		}
		tok := strings.TrimSpace(pair[eq+1:])
		if !strings.HasPrefix(tok, "$") {
			return nil, fmt.Errorf("cwState: unsupported SET expression %q", tok)
		}
		n, _ := strconv.Atoi(tok[1:])
		setters = append(setters, setter{idx: idx, val: cwArg(args, n)})
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
	return cwResult{rows: int64(len(matched))}, nil
}

// ---------- fixtures ----------

func cwSeedCampaign(t *testing.T, s *cwState, id uint, segmentID *uint) {
	vals := map[string]any{
		"id": id, "user_id": uint(7), "workspace_id": uint(3),
		"name": "Launch", "subject": "Hi", "from_email": "hello@example.com",
		"template_id": uint(5), "status": string(models.CampaignStatusSending),
		"created_at": cwTestTime,
	}
	if segmentID != nil {
		vals["segment_id"] = *segmentID
	}
	cwSeed(t, s, "campaigns", vals)
}

func newSegmentProcessor(t *testing.T, state *cwState) *CampaignProcessor {
	t.Helper()
	gdb := cwGorm(t, state)
	return NewCampaignProcessor(
		repositories.NewCampaignRepository(gdb),
		repositories.NewCampaignMessageRepository(gdb),
		repositories.NewSubscriberListRepository(gdb),
		repositories.NewSegmentRepository(gdb),
		repositories.NewSubscriberRepository(gdb),
		nil, nil, nil, nil, nil, nil,
		nil, // producer: the enqueue guard must fail the task after the messages exist
		nil, // dispatcher
	)
}

func cwStartTask(t *testing.T, campaignID uint) *asynq.Task {
	t.Helper()
	payload, err := json.Marshal(CampaignPayload{CampaignID: campaignID})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return asynq.NewTask(TypeCampaignStart, payload)
}

// ---------- the segment branch ----------

func TestCampaignStartResolvesSegmentAudience(t *testing.T) {
	state := newCWState()
	p := newSegmentProcessor(t, state)

	segID := uint(7)
	cwSeedCampaign(t, state, 5, &segID)
	cwSeed(t, state, "segments", map[string]any{
		"id": uint(7), "user_id": uint(7), "workspace_id": uint(3),
		"name": "Alice only", "condition": cwSegmentCondition, "created_at": cwTestTime,
	})
	cwSeed(t, state, "subscribers", map[string]any{
		"id": uint(21), "user_id": uint(7), "workspace_id": uint(3),
		"email": "alice@example.com", "status": "subscribed", "created_at": cwTestTime,
	})
	// A second, non-matching subscriber must not receive the campaign.
	cwSeed(t, state, "subscribers", map[string]any{
		"id": uint(22), "user_id": uint(7), "workspace_id": uint(3),
		"email": "bob@example.com", "status": "subscribed", "created_at": cwTestTime,
	})

	err := p.HandleCampaignStart(context.Background(), cwStartTask(t, 5))
	if err == nil || !strings.Contains(err.Error(), "producer not configured") {
		t.Fatalf("HandleCampaignStart error = %v, want the nil-producer enqueue guard after messages were created", err)
	}

	messages := state.rowsByColumn("campaign_messages", "campaign_id", 5)
	if len(messages) != 1 {
		t.Fatalf("campaign messages = %d, want 1 (only the condition match)", len(messages))
	}
	if got := messages[0][state.colIndex("campaign_messages", "subscriber_id")]; got != int64(21) {
		t.Errorf("message subscriber_id = %v, want 21", got)
	}
	if got := messages[0][state.colIndex("campaign_messages", "status")]; fmt.Sprint(got) != string(models.CampaignMsgPending) {
		t.Errorf("message status = %v, want pending", got)
	}
	// The campaign is not marked sent while a batch is still to be enqueued.
	if got := state.columnByID("campaigns", "status", 5); fmt.Sprint(got) != string(models.CampaignStatusSending) {
		t.Errorf("campaign status = %v, want still sending", got)
	}

	// The audience was resolved through the condition path: a parameterized,
	// workspace-scoped subscribers query carrying the condition, and no
	// interpolated value.
	var sawConditionQuery bool
	for _, q := range state.recordedQueries() {
		if !strings.Contains(q, `FROM "subscribers"`) || strings.Contains(strings.ToLower(q), "count(*)") {
			continue
		}
		sawConditionQuery = true
		if !strings.Contains(q, "workspace_id = $1") {
			t.Errorf("audience query is not workspace-scoped: %s", q)
		}
		if !strings.Contains(q, "email = $2") {
			t.Errorf("audience query does not carry the condition as a bindvar: %s", q)
		}
		if strings.Contains(q, "alice@example.com") {
			t.Errorf("audience query embeds the raw value: %s", q)
		}
	}
	if !sawConditionQuery {
		t.Fatalf("no condition query was captured; queries: %v", state.recordedQueries())
	}
}

func TestCampaignStartSegmentEligibilityMarksSent(t *testing.T) {
	state := newCWState()
	p := newSegmentProcessor(t, state)

	segID := uint(7)
	cwSeedCampaign(t, state, 5, &segID)
	cwSeed(t, state, "segments", map[string]any{
		"id": uint(7), "user_id": uint(7), "workspace_id": uint(3),
		"name": "Alice only", "condition": cwSegmentCondition, "created_at": cwTestTime,
	})
	// The condition matches, but the subscriber is not eligible: unsubscribed.
	cwSeed(t, state, "subscribers", map[string]any{
		"id": uint(21), "user_id": uint(7), "workspace_id": uint(3),
		"email": "alice@example.com", "status": "unsubscribed", "created_at": cwTestTime,
	})

	if err := p.HandleCampaignStart(context.Background(), cwStartTask(t, 5)); err != nil {
		t.Fatalf("HandleCampaignStart error = %v, want nil (no eligible subscribers marks the campaign sent)", err)
	}
	if state.rowCount() != 0 {
		t.Errorf("campaign messages = %d, want 0", state.rowCount())
	}
	if got := state.columnByID("campaigns", "status", 5); fmt.Sprint(got) != string(models.CampaignStatusSent) {
		t.Errorf("campaign status = %v, want sent", got)
	}
}

func TestCampaignStartSegmentScopeIsEnforced(t *testing.T) {
	state := newCWState()
	p := newSegmentProcessor(t, state)

	// Segment 8 belongs to workspace 9; the campaign to workspace 3.
	segID := uint(8)
	cwSeedCampaign(t, state, 5, &segID)
	cwSeed(t, state, "segments", map[string]any{
		"id": uint(8), "user_id": uint(8), "workspace_id": uint(9),
		"name": "Foreign", "condition": cwSegmentCondition, "created_at": cwTestTime,
	})

	err := p.HandleCampaignStart(context.Background(), cwStartTask(t, 5))
	if err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("HandleCampaignStart error = %v, want the cross-workspace refusal", err)
	}
	if state.rowCount() != 0 {
		t.Error("a refused segment audience must not create messages")
	}
}

// ---------- the list branch is untouched ----------

func TestCampaignStartListBranchStillUsedWithoutSegment(t *testing.T) {
	state := newCWState()
	p := newSegmentProcessor(t, state)

	// No segment and list 0 does not exist: the list branch runs and fails on
	// the missing list, proving the segment branch was not entered.
	cwSeedCampaign(t, state, 6, nil)

	err := p.HandleCampaignStart(context.Background(), cwStartTask(t, 6))
	if err == nil || !strings.Contains(err.Error(), "subscriber list not found") {
		t.Fatalf("HandleCampaignStart error = %v, want subscriber list not found from the list branch", err)
	}
	if state.rowCount() != 0 {
		t.Error("the failed list branch must not create messages")
	}
}
