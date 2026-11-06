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

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// R-06 handler tests for POST /webhook-deliveries/{id}/replay: the endpoint's
// failed-only gate, the ownership/404 scoping, the 5-replays-per-delivery-per-
// hour cap, and the webhook.delivery.replayed audit event. The handler runs
// through the real okapi stack (JWT + workspace binding, the
// domain_signing_handler_test.go arrangement) against a fake database/sql
// driver; the dispatcher's own write path is exercised at the dispatcher level
// (dispatcher_replay_test.go) and is parked in DryRun here — its outbound send
// to a loopback httptest target is deterministically refused by the guarded
// URL policy, which is fine: the handler contract is independent of the
// delivery's final outcome.

const wrTestWorkspace = "3"

var wrColumns = map[string][]string{
	"webhook_deliveries": {"id", "webhook_id", "user_id", "workspace_id", "event", "status",
		"http_status_code", "error_message", "attempt", "payload", "created_at"},
	"webhooks":                  {"id", "user_id", "workspace_id", "url", "secret", "created_at"},
	"webhook_delivery_attempts": {"id", "delivery_id", "attempt", "status_code", "response_body", "error", "created_at"},
	"events":                    {"id", "category", "type", "workspace_id", "actor_id", "actor_name", "client_ip", "message", "metadata", "outcome", "target_type", "target_id", "created_at"},
}

var wrDefaults = map[string]map[string]func() driver.Value{
	"webhook_deliveries": {
		"workspace_id":     func() driver.Value { return nil },
		"event":            func() driver.Value { return "email.sent" },
		"http_status_code": func() driver.Value { return int64(0) },
		"error_message":    func() driver.Value { return "" },
		"attempt":          func() driver.Value { return int64(1) },
		"payload":          func() driver.Value { return "" },
	},
	"webhooks": {
		"workspace_id": func() driver.Value { return nil },
		"secret":       func() driver.Value { return "" },
	},
	"webhook_delivery_attempts": {
		"status_code":   func() driver.Value { return int64(0) },
		"response_body": func() driver.Value { return "" },
		"error":         func() driver.Value { return "" },
	},
	"events": {
		"workspace_id": func() driver.Value { return nil },
		"metadata":     func() driver.Value { return "{}" },
		"outcome":      func() driver.Value { return "" },
		"target_type":  func() driver.Value { return "" },
		"target_id":    func() driver.Value { return nil },
	},
}

type wrTable struct {
	columns []string
	rows    [][]driver.Value
}

type wrState struct {
	mu     sync.Mutex
	tables map[string]*wrTable
	seq    int64
}

func newWrState() *wrState {
	tables := make(map[string]*wrTable, len(wrColumns))
	for name, cols := range wrColumns {
		tables[name] = &wrTable{columns: cols}
	}
	return &wrState{tables: tables, seq: 900}
}

func (s *wrState) colIndex(table, col string) int {
	for i, c := range wrColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

func wrSeed(t *testing.T, s *wrState, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := wrColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := wrDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for col, v := range vals {
		idx := s.colIndex(table, col)
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, col)
		}
		row[idx] = wrVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func wrVal(v any) driver.Value {
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
	case models.WebhookDeliveryStatus:
		return string(t)
	default:
		panic(fmt.Sprintf("wrVal: unsupported type %T", v))
	}
}

func wrWsID(v uint) *uint { return &v }

func (s *wrState) rowsOf(table string) []map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, 0, len(s.tables[table].rows))
	for _, row := range s.tables[table].rows {
		m := map[string]driver.Value{}
		for i, c := range wrColumns[table] {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

func (s *wrState) eventTypes() []string {
	var out []string
	for _, m := range s.rowsOf("events") {
		if t, ok := m["type"].(string); ok {
			out = append(out, t)
		}
	}
	return out
}

func wrGorm(t *testing.T, state *wrState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(wrConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open webhook replay fake gorm: %v", err)
	}
	return gdb
}

type wrConnector struct{ state *wrState }

func (c wrConnector) Connect(context.Context) (driver.Conn, error) { return &wrConn{s: c.state}, nil }
func (c wrConnector) Driver() driver.Driver                        { return wrDrv{} }

type wrDrv struct{}

func (wrDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type wrConn struct{ s *wrState }

func (c *wrConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *wrConn) Close() error                        { return nil }
func (c *wrConn) Begin() (driver.Tx, error)           { return wrTx{}, nil }

type wrTx struct{}

func (wrTx) Commit() error   { return nil }
func (wrTx) Rollback() error { return nil }

type wrRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *wrRows) Columns() []string { return r.cols }
func (r *wrRows) Close() error      { return nil }
func (r *wrRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func wrTableOf(lower string) string {
	for _, name := range []string{"webhook_delivery_attempts", "webhook_deliveries", "webhooks", "events"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`, `join ` + name} {
			if strings.Contains(lower, pat) {
				return name
			}
		}
	}
	return ""
}

func (c *wrConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := wrTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("wrState: cannot route query: %s", query)
	}
	if strings.Contains(lower, "insert into") {
		return c.handleInsert(lower, table, query, args)
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(lower, "count(*)") {
		return &wrRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	if m := strings.Index(lower, " limit $"); m >= 0 {
		if n, err := strconv.Atoi(lower[m+len(" limit "):][1:]); err == nil {
			if n < len(matched) {
				matched = matched[:n]
			}
		}
	}
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &wrRows{cols: wrColumns[table], rows: rows}, nil
}

// handleInsert answers the audit event INSERT ... RETURNING "id".
func (c *wrConn) handleInsert(lower, table, query string, args []driver.NamedValue) (driver.Rows, error) {
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("wrState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, fmt.Errorf("wrState: insert without a column list")
	}
	colParts := strings.Split(query[open+1:close], ",")
	valText := query[vi+len(" values "):]
	end := strings.LastIndex(valText, ")")
	valParts := strings.Split(valText[1:end], ",")
	if len(valParts) != len(colParts) {
		return nil, fmt.Errorf("wrState: %d columns but %d values", len(colParts), len(valParts))
	}
	vals := make([]driver.Value, len(valParts))
	for i, p := range valParts {
		n, err := strconv.Atoi(strings.TrimSpace(p)[1:])
		if err != nil {
			return nil, err
		}
		vals[i] = wrArg(args, n)
	}

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := wrColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := wrDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for j, col := range colParts {
		col = strings.Trim(strings.TrimSpace(col), `"`)
		idx := s.colIndex(table, col)
		if idx < 0 {
			return nil, fmt.Errorf("wrState: %s has no column %q", table, col)
		}
		row[idx] = vals[j]
	}
	s.seq++
	row[s.colIndex(table, "id")] = s.seq
	s.tables[table].rows = append(s.tables[table].rows, row)

	var returning []string
	if i := strings.Index(lower, " returning "); i >= 0 {
		for _, cn := range strings.Split(lower[i+len(" returning "):], ",") {
			returning = append(returning, strings.Trim(strings.TrimSpace(cn), `"`))
		}
	}
	out := make([]driver.Value, len(returning))
	for i, cn := range returning {
		out[i] = row[s.colIndex(table, cn)]
	}
	return &wrRows{cols: returning, rows: [][]driver.Value{out}}, nil
}

func (c *wrConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return nil, fmt.Errorf("wrState: unsupported exec: %s", query)
}

// evalWhere evaluates equality (=), comparison (>) and inclusive (>=)
// predicates — the WHERE shapes the delivery lookup, the webhook lookup and
// the replay-cap COUNT issue.
func (s *wrState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
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
		opIdx := -1
		op := ""
		for _, candidate := range []string{" >= ", " > ", " = "} {
			if i := strings.Index(cond, candidate); i >= 0 {
				opIdx = i
				op = candidate
				break
			}
		}
		if opIdx < 0 {
			return nil, fmt.Errorf("wrState: unsupported condition %q", cond)
		}
		idx := s.colIndex(table, wrColumnOf(cond[:opIdx]))
		n, err := strconv.Atoi(strings.TrimSpace(cond[opIdx+len(op):])[1:])
		if err != nil {
			return nil, fmt.Errorf("wrState: bad bindvar in %q", cond)
		}
		want := wrArg(args, n)
		switch op {
		case " = ":
			preds = append(preds, func(row []driver.Value) bool { return wrEqual(row[idx], want) })
		case " > ":
			preds = append(preds, func(row []driver.Value) bool { return wrCompare(row[idx], want) > 0 })
		case " >= ":
			preds = append(preds, func(row []driver.Value) bool { return wrCompare(row[idx], want) >= 0 })
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

func wrColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func wrArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("wrState: no arg with ordinal %d", n))
}

func wrEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if ta, ok := a.(time.Time); ok {
		tb, ok2 := b.(time.Time)
		return ok2 && ta.Equal(tb)
	}
	if sa, ok := a.(string); ok {
		sb, ok2 := b.(string)
		return ok2 && sa == sb
	}
	return a == b
}

// wrCompare orders two values of the same comparable kind (int64 or time.Time).
func wrCompare(a, b driver.Value) int {
	switch ta := a.(type) {
	case int64:
		tb, _ := b.(int64)
		switch {
		case ta > tb:
			return 1
		case ta < tb:
			return -1
		}
		return 0
	case time.Time:
		tb, _ := b.(time.Time)
		switch {
		case ta.After(tb):
			return 1
		case ta.Before(tb):
			return -1
		}
		return 0
	default:
		return -1
	}
}

// ---------- fixtures + server ----------

type wrFixture struct {
	state *wrState
}

// seedDelivery inserts a delivery; ownership defaults to the test scope.
func (f *wrFixture) seedDelivery(t *testing.T, id uint, vals map[string]any) {
	t.Helper()
	base := map[string]any{
		"id": id, "webhook_id": uint(5), "user_id": uint(7), "workspace_id": wrWsID(3),
		"event": "email.sent", "status": models.WebhookDeliveryFailed, "attempt": int64(2),
		"payload": `{"event":"email.sent"}`,
	}
	for k, v := range vals {
		base[k] = v
	}
	wrSeed(t, f.state, "webhook_deliveries", base)
}

func (f *wrFixture) seedWebhook(t *testing.T, id uint, vals map[string]any) {
	t.Helper()
	base := map[string]any{
		"id": id, "user_id": uint(7), "workspace_id": wrWsID(3), "url": "http://127.0.0.1:1/hook",
	}
	for k, v := range vals {
		base[k] = v
	}
	wrSeed(t, f.state, "webhooks", base)
}

// seedReplayAttempts appends n replay attempt rows for a delivery.
func (f *wrFixture) seedReplayAttempts(t *testing.T, deliveryID uint, afterAttempt, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		wrSeed(t, f.state, "webhook_delivery_attempts", map[string]any{
			"delivery_id": deliveryID, "attempt": int64(afterAttempt + 1 + i),
			"created_at": time.Now(),
		})
	}
}

func newReplayServer(t *testing.T, state *wrState) *okapi.TestServer {
	t.Helper()
	gdb := wrGorm(t, state)
	dispatcher := webhook.NewDispatcher(repositories.NewWebhookRepository(gdb.Session(&gorm.Session{DryRun: true})))
	dispatcher.SetDeliveryRepo(repositories.NewWebhookDeliveryRepository(gdb.Session(&gorm.Session{DryRun: true})))
	dispatcher.SetAttemptRepo(repositories.NewWebhookDeliveryAttemptRepository(gdb.Session(&gorm.Session{DryRun: true})))
	h := NewWebhookReplayHandler(
		repositories.NewWebhookDeliveryRepository(gdb),
		repositories.NewWebhookDeliveryAttemptRepository(gdb),
		repositories.NewWebhookRepository(gdb),
		dispatcher,
		audit.NewLogger(eventbus.New(repositories.NewEventRepository(gdb))),
	)
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		testWorkspaceBinding,
	)
	g.Post("/webhook-deliveries/{id:int}/replay", okapi.H(h.Replay))
	return o
}

func doReplayReq(t *testing.T, o *okapi.TestServer, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
	req.Header.Set("X-Test-Workspace", wrTestWorkspace)
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

// ---------- tests ----------

// A failed delivery with a stored payload is replayable: the endpoint records
// the webhook.delivery.replayed audit event and answers with the replay view.
func TestWebhookReplayRecordsAuditEvent(t *testing.T) {
	state := newWrState()
	f := &wrFixture{state: state}
	f.seedWebhook(t, 5, nil)
	f.seedDelivery(t, 42, nil)
	o := newReplayServer(t, state)

	rec := doReplayReq(t, o, "/workspaces/current/webhook-deliveries/42/replay")
	if rec.Code != http.StatusOK {
		t.Fatalf("replay = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Data WebhookReplayResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Data.ID != 42 || res.Data.WebhookID != 5 {
		t.Fatalf("replay response wrong: %+v", res.Data)
	}
	if res.Data.Replay == nil {
		t.Fatal("replay response must carry the replay result")
	}
	got := state.eventTypes()
	if len(got) != 1 || got[0] != "webhook.delivery.replayed" {
		t.Fatalf("audit events = %v, want one webhook.delivery.replayed", got)
	}
}

// Only failed deliveries are replayable.
func TestWebhookReplayRejectsNonFailedDelivery(t *testing.T) {
	state := newWrState()
	f := &wrFixture{state: state}
	f.seedWebhook(t, 5, nil)
	f.seedDelivery(t, 43, map[string]any{"status": models.WebhookDeliverySuccess})
	o := newReplayServer(t, state)

	rec := doReplayReq(t, o, "/workspaces/current/webhook-deliveries/43/replay")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("replay of successful delivery = %d, want 400", rec.Code)
	}
	if got := state.eventTypes(); len(got) != 0 {
		t.Fatalf("rejected replay must not write an audit event, got %v", got)
	}
}

// A delivery with no stored payload (pre-R-06 legacy row) cannot be replayed.
func TestWebhookReplayRejectsMissingPayload(t *testing.T) {
	state := newWrState()
	f := &wrFixture{state: state}
	f.seedWebhook(t, 5, nil)
	f.seedDelivery(t, 44, map[string]any{"payload": ""})
	o := newReplayServer(t, state)

	rec := doReplayReq(t, o, "/workspaces/current/webhook-deliveries/44/replay")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("replay without payload = %d, want 400", rec.Code)
	}
}

// Ownership scoping: a delivery outside the caller's scope — and a delivery
// whose webhook moved out of scope — answer 404, indistinguishable from a
// missing delivery. A delivery whose webhook row is gone is a 400.
func TestWebhookReplayScope(t *testing.T) {
	state := newWrState()
	f := &wrFixture{state: state}
	o := newReplayServer(t, state)

	f.seedDelivery(t, 45, map[string]any{"workspace_id": wrWsID(99), "user_id": uint(42)})
	if rec := doReplayReq(t, o, "/workspaces/current/webhook-deliveries/45/replay"); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign delivery = %d, want 404", rec.Code)
	}

	if rec := doReplayReq(t, o, "/workspaces/current/webhook-deliveries/999/replay"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing delivery = %d, want 404", rec.Code)
	}

	f.seedWebhook(t, 5, nil)
	f.seedDelivery(t, 46, map[string]any{"webhook_id": uint(6)})
	// Delivery 46 points at webhook 6, which does not exist.
	if rec := doReplayReq(t, o, "/workspaces/current/webhook-deliveries/46/replay"); rec.Code != http.StatusBadRequest {
		t.Fatalf("delivery with missing webhook = %d, want 400", rec.Code)
	}

	f.seedWebhook(t, 7, map[string]any{"workspace_id": wrWsID(99), "user_id": uint(42)})
	f.seedDelivery(t, 47, map[string]any{"webhook_id": uint(7)})
	if rec := doReplayReq(t, o, "/workspaces/current/webhook-deliveries/47/replay"); rec.Code != http.StatusNotFound {
		t.Fatalf("delivery with foreign webhook = %d, want 404", rec.Code)
	}
	if got := state.eventTypes(); len(got) != 0 {
		t.Fatalf("no scoped replay may write an audit event, got %v", got)
	}
}

// The per-delivery hourly cap: five replays already recorded → 429 and no new
// audit event; four → still allowed.
func TestWebhookReplayEnforcesHourlyCap(t *testing.T) {
	state := newWrState()
	f := &wrFixture{state: state}
	f.seedWebhook(t, 5, nil)
	f.seedDelivery(t, 48, nil)
	o := newReplayServer(t, state)

	f.seedReplayAttempts(t, 48, 2, 5)
	rec := doReplayReq(t, o, "/workspaces/current/webhook-deliveries/48/replay")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("replay past cap = %d, want 429", rec.Code)
	}
	if got := state.eventTypes(); len(got) != 0 {
		t.Fatalf("capped replay must not write an audit event, got %v", got)
	}

	// One slot free: the replay goes through.
	state.mu.Lock()
	attempts := state.tables["webhook_delivery_attempts"].rows
	state.tables["webhook_delivery_attempts"].rows = attempts[:len(attempts)-1]
	state.mu.Unlock()
	rec = doReplayReq(t, o, "/workspaces/current/webhook-deliveries/48/replay")
	if rec.Code != http.StatusOK {
		t.Fatalf("replay with one slot free = %d: %s", rec.Code, rec.Body.String())
	}
	if got := state.eventTypes(); len(got) != 1 || got[0] != "webhook.delivery.replayed" {
		t.Fatalf("audit events = %v, want one webhook.delivery.replayed", got)
	}
}

// Original attempt rows (numbered at or below the delivery's attempt count)
// never count toward the replay cap.
func TestWebhookReplayCapIgnoresOriginalAttempts(t *testing.T) {
	state := newWrState()
	f := &wrFixture{state: state}
	f.seedWebhook(t, 5, nil)
	f.seedDelivery(t, 49, nil)
	o := newReplayServer(t, state)

	for i := 0; i < 5; i++ {
		wrSeed(t, state, "webhook_delivery_attempts", map[string]any{
			"delivery_id": uint(49), "attempt": int64(1 + i%2), "created_at": time.Now(),
		})
	}
	rec := doReplayReq(t, o, "/workspaces/current/webhook-deliveries/49/replay")
	if rec.Code != http.StatusOK {
		t.Fatalf("original attempts must not consume the replay cap: %d: %s", rec.Code, rec.Body.String())
	}
}
