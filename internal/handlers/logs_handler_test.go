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
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/services/provider_events"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The logs handler is exercised over the real okapi routing stack with the
// real JWT middleware (nil API-key collaborators, exactly like the
// Authenticate middleware tests). The database is a fake database/sql driver:
// it answers the handful of SELECTs the endpoints issue from fixed rows, and
// simulates the email_events dedup index, so every endpoint is served
// end-to-end without a database.

const logsTestJWTSecret = "logs-handler-test-secret"

var logsTestTime = time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)

// logsTestJWT mints the dashboard-session token the middleware accepts.
func logsTestJWT(t *testing.T, role string) string {
	t.Helper()
	tok, err := okapi.GenerateJwtToken([]byte(logsTestJWTSecret), map[string]any{
		"sub":   7,
		"email": "user@example.com",
		"role":  role,
		"aud":   "posta",
		"jti":   "jti-logs-1",
	}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateJwtToken: %v", err)
	}
	return tok
}

// ---------- the fake database ----------

type fakeTable struct {
	columns []string
	rows    [][]driver.Value
}

type fakeState struct {
	mu       sync.Mutex
	tables   map[string]fakeTable
	nextID   int64
	inserted map[string]bool // email_events dedup keys
}

func newFakeState() *fakeState {
	return &fakeState{tables: make(map[string]fakeTable), nextID: 1000, inserted: make(map[string]bool)}
}

// fakeConnector serves one shared state across pooled connections.
type fakeConnector struct{ state *fakeState }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &fakeConn{state: c.state}, nil
}
func (c fakeConnector) Driver() driver.Driver { return fakeDriverSingleton{} }

type fakeDriverSingleton struct{}

func (fakeDriverSingleton) Open(string) (driver.Conn, error) {
	return nil, errors.New("use sql.OpenDB with fakeConnector")
}

type fakeConn struct{ state *fakeState }

func (c fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake conn: prepare unsupported")
}
func (c fakeConn) Close() error              { return nil }
func (c fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("fake conn: tx unsupported") }

type fakeResult struct{ rows int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.rows, nil }

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

// tableNames is ordered most-specific first so substring matches pick the
// right table (email_events before events, and so on).
var fakeTableNames = []string{
	"settings", "email_events", "incoming_webhook_events",
	"webhook_delivery_attempts", "webhook_deliveries",
	"campaign_messages", "campaigns", "emails", "events",
}

func (c *fakeConn) matchTable(lowerQuery string) (fakeTable, bool) {
	for _, name := range fakeTableNames {
		for _, pat := range []string{`from "` + name + `"`, `from ` + name + ` `, `into "` + name + `"`} {
			if strings.Contains(lowerQuery, pat) {
				c.state.mu.Lock()
				defer c.state.mu.Unlock()
				tbl, ok := c.state.tables[name]
				return tbl, ok
			}
		}
	}
	return fakeTable{}, false
}

// parseInsertColumns maps the bound variables of an INSERT to its columns.
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

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	if strings.Contains(lower, "insert into") {
		return c.handleInsert(query, args)
	}
	if tbl, ok := c.matchTable(lower); ok {
		// The classification aggregate groups by the (possibly quoted)
		// "classification" alias — match the alias, not the exact spelling.
		if strings.Contains(lower, "group by") && strings.Contains(lower, "classification") {
			return c.classificationAggregate(tbl, args)
		}
		if strings.Contains(lower, "count(") {
			return &fakeRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(len(tbl.rows))}}}, nil
		}
		cols, rows := selectedColumns(lower, tbl)
		return &fakeRows{columns: cols, rows: rows}, nil
	}
	// Unknown statements answer with an empty set.
	return &fakeRows{columns: []string{"x"}}, nil
}

// classificationAggregate answers the provider_events classification query
// (COUNT(*) … GROUP BY classification over email_events.metadata): each
// seeded row's metadata JSON is parsed for the classification the real query
// extracts with metadata->>'classification', and the bound workspace /
// occurred-at filters are applied — time args in bind order are the window's
// from (inclusive) and to (exclusive).
func (c *fakeConn) classificationAggregate(tbl fakeTable, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()

	var wsFilter *int64
	var timeBounds []*time.Time
	for _, a := range args {
		switch v := a.Value.(type) {
		case int64:
			x := v
			wsFilter = &x
		case uint64:
			x := int64(v)
			wsFilter = &x
		case float64:
			x := int64(v)
			wsFilter = &x
		case time.Time:
			t := v
			timeBounds = append(timeBounds, &t)
		}
	}
	var from, to *time.Time
	if len(timeBounds) > 0 {
		from = timeBounds[0]
	}
	if len(timeBounds) > 1 {
		to = timeBounds[1]
	}

	col := func(name string) int {
		for i, c := range tbl.columns {
			if c == name {
				return i
			}
		}
		return -1
	}
	wsCol, tsCol, metaCol := col("workspace_id"), col("occurred_at"), col("metadata")

	counts := map[string]int64{}
	for _, row := range tbl.rows {
		if wsCol >= 0 {
			if wsFilter == nil {
				continue // the query is always workspace-pinned
			}
			switch v := row[wsCol].(type) {
			case int64:
				if v != *wsFilter {
					continue
				}
			case uint64:
				if int64(v) != *wsFilter {
					continue
				}
			default:
				continue // NULL workspace never matches a workspace filter
			}
		}
		if tsCol >= 0 {
			ts, _ := row[tsCol].(time.Time)
			if from != nil && ts.Before(*from) {
				continue
			}
			if to != nil && !ts.Before(*to) {
				continue
			}
		}
		var meta map[string]any
		raw, _ := row[metaCol].(string)
		if raw == "" || json.Unmarshal([]byte(raw), &meta) != nil {
			continue
		}
		cls, _ := meta["classification"].(string)
		if cls == "" {
			continue // metadata->>'classification' IS NOT NULL
		}
		counts[cls]++
	}

	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([][]driver.Value, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, []driver.Value{k, counts[k]})
	}
	return &fakeRows{columns: []string{"classification", "count"}, rows: rows}, nil
}

// selectedColumns honors the query's SELECT list: like a real database, only
// the requested columns come back in the result set, so a slim SELECT can
// never surface columns it did not ask for.
func selectedColumns(lowerQuery string, tbl fakeTable) ([]string, [][]driver.Value) {
	if !strings.Contains(lowerQuery, "select ") {
		return tbl.columns, tbl.rows
	}
	rest := lowerQuery[strings.Index(lowerQuery, "select ")+len("select "):]
	end := strings.Index(rest, " from ")
	if end < 0 || strings.Contains(rest[:end], "*") {
		return tbl.columns, tbl.rows
	}
	want := make(map[string]bool)
	for _, p := range strings.Split(rest[:end], ",") {
		want[strings.TrimSpace(p)] = true
	}
	idxs := make([]int, 0, len(want))
	cols := make([]string, 0, len(want))
	for i, col := range tbl.columns {
		if want[col] {
			idxs = append(idxs, i)
			cols = append(cols, col)
		}
	}
	rows := make([][]driver.Value, len(tbl.rows))
	for r, row := range tbl.rows {
		narrowed := make([]driver.Value, len(idxs))
		for j, ix := range idxs {
			narrowed[j] = row[ix]
		}
		rows[r] = narrowed
	}
	return cols, rows
}

func (c *fakeConn) handleInsert(query string, args []driver.NamedValue) (driver.Rows, error) {
	values := parseInsertColumns(query, args)
	if values == nil {
		return &fakeRows{columns: []string{"id"}}, nil
	}
	key := fmt.Sprintf("%v|%v|%v", values["email_uuid"], values["type"], values["provider_event_id"])
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.inserted[key] {
		return &fakeRows{columns: []string{"id"}}, nil // conflict: nothing returned
	}
	c.state.inserted[key] = true
	c.state.nextID++
	return &fakeRows{columns: []string{"id"}, rows: [][]driver.Value{{c.state.nextID}}}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(strings.ToLower(query), "delete") {
		return fakeResult{rows: 1}, nil
	}
	return fakeResult{rows: 1}, nil
}

func fakeGorm(t *testing.T, state *fakeState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(fakeConnector{state: state}),
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

// ---------- fixtures: seeded tables, settings provider, server ----------

// seedEmails loads the one message row the endpoints serve, with bodies that
// must never leak through the slim payloads.
func seedEmails(state *fakeState) {
	state.tables["emails"] = fakeTable{
		columns: []string{
			"id", "uuid", "external_id", "user_id", "workspace_id", "api_key_id",
			"sender", "recipients", "subject", "template_name", "status", "error_message",
			"retry_count", "provider", "smtp_hostname", "unsubscribe_list_id",
			"created_at", "scheduled_at", "sent_at", "delivered_at", "opened_at",
			"clicked_at", "bounced_at", "complained_at", "unsubscribed_at",
			"html_body", "text_body", "attachments_json", "headers_json", "template_data",
		},
		rows: [][]driver.Value{{
			int64(11), "em-1", "ext-1", int64(7), int64(3), nil,
			"news@posta.dev", []byte("{alice@example.com,bob@example.com}"), "Welcome", "welcome", "sent", "",
			int64(0), "postmark", "smtp1.posta.dev", nil,
			logsTestTime, nil, logsTestTime, nil, nil,
			nil, nil, nil, nil,
			"<p>secret-html</p>", "secret-text",
			`[{"filename":"a.pdf","content_type":"application/pdf","storage_key":"att/1"}]`,
			`{"X-Test":"1"}`, `{"name":"Alice"}`,
		}},
	}
	state.tables["email_events"] = fakeTable{
		columns: []string{
			"id", "email_uuid", "workspace_id", "type", "occurred_at", "provider",
			"provider_event_id", "recipient", "ip", "user_agent", "url", "error",
			"metadata", "created_at",
		},
		rows: [][]driver.Value{{
			int64(5), "em-1", nil, "sent", logsTestTime, "postmark",
			"", "alice@example.com", "", "", "", "",
			"{}", logsTestTime,
		}},
	}
}

// seedAudit loads two audit events: one workspace-bound, one deployment-wide.
func seedAudit(state *fakeState) {
	state.tables["events"] = fakeTable{
		columns: []string{
			"id", "category", "type", "workspace_id", "actor_id", "actor_name",
			"client_ip", "message", "metadata", "outcome", "target_type", "target_id", "created_at",
		},
		rows: [][]driver.Value{
			{int64(21), "email", "email.sent", int64(3), int64(7), "Alice",
				"10.0.0.1", "sent a message", "{}", "success", "email", "em-1", logsTestTime},
			{int64(22), "system", "admin.login", nil, int64(7), "Alice",
				"10.0.0.2", "admin signed in", "{}", "success", "", "", logsTestTime},
		},
	}
}

func seedIncomingWebhook(state *fakeState) {
	state.tables["incoming_webhook_events"] = fakeTable{
		columns: []string{"id", "source", "type", "email_uuid", "recipient", "message_id", "raw_payload", "process_error", "created_at"},
		rows: [][]driver.Value{{
			int64(9), "bounce", "bounced", "", "x@y.z", "mid-1", `{"bounce":true}`, "", logsTestTime,
		}},
	}
}

// settingsProvider builds a live settings provider over the fake database:
// whatever rows the settings table holds is what the accessors return.
func settingsProvider(t *testing.T, state *fakeState) *settings.Provider {
	t.Helper()
	return settings.NewProvider(repositories.NewSettingRepository(fakeGorm(t, state)))
}

// testWorkspaceBinding plays the role of the workspace middleware: it binds
// the request to the workspace named by the X-Test-Workspace header (mirroring
// the value the real middleware sets after the membership check).
var testWorkspaceBinding okapi.Middleware = func(c *okapi.Context) error {
	if ws := c.Request().Header.Get("X-Test-Workspace"); ws != "" {
		c.Set(middlewares.CtxWorkspaceID, ws)
		c.Set(middlewares.CtxWorkspaceRole, "admin")
	}
	return c.Next()
}

// newLogsServer registers the logs routes with the real authentication
// middleware (nil API-key collaborators are safe: only JWTs are presented).
// Optional opts decorate the handler before routing (used to wire test
// doubles for the newer endpoints).
func newLogsServer(t *testing.T, db *gorm.DB, sp *settings.Provider, opts ...func(*LogsHandler)) *okapi.TestServer {
	t.Helper()
	h := NewLogsHandler(db, emailhistory.NewRecorder(nil), sp)
	for _, opt := range opts {
		opt(h)
	}
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		testWorkspaceBinding,
	)
	g.Get("/logs/messages", okapi.H(h.ListMessages))
	g.Get("/logs/messages/{uuid}", okapi.H(h.GetMessage))
	g.Get("/logs/messages/{uuid}/content", okapi.H(h.GetMessageContent))
	g.Get("/logs/messages/{uuid}/events", okapi.H(h.GetMessageEvents))
	g.Get("/logs/incoming-webhooks/{id:int}", okapi.H(h.GetIncomingWebhook))
	g.Get("/logs/audit", okapi.H(h.ListAudit))
	g.Get("/logs/audit/export", h.ExportAudit)
	g.Get("/logs/classifications", okapi.H(h.ListClassifications))
	g.Get("/logs/{uuid}/events/stream", okapi.H(h.StreamMessageEvents))
	return o
}

// doLogsReq performs one GET request against the test server.
func doLogsReq(o *okapi.TestServer, path, token, workspace string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if workspace != "" {
		req.Header.Set("X-Test-Workspace", workspace)
	}
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

// decodeEnvelope parses the standard paginated envelope (data is an array)
// into generic maps.
func decodeEnvelope(t *testing.T, body []byte) (map[string]any, []any) {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	data, _ := envelope["data"].([]any)
	return envelope, data
}

// decodeSingle parses the standard single-object envelope (data is an object).
func decodeSingle(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	data, _ := envelope["data"].(map[string]any)
	if data == nil {
		t.Fatalf("data is not an object: %q", body)
	}
	return data
}

// ---------- authentication ----------

// TestLogsRoutesRequireAuthentication pins that the logs surface sits behind
// the real authentication middleware: no credential or a broken one is 401
// before any handler runs.
func TestLogsRoutesRequireAuthentication(t *testing.T) {
	state := newFakeState()
	seedEmails(state)
	o := newLogsServer(t, fakeGorm(t, state), settingsProvider(t, state))

	cases := []struct {
		name string
		path string
	}{
		{"message list", "/workspaces/current/logs/messages"},
		{"message detail", "/workspaces/current/logs/messages/em-1"},
		{"message content", "/workspaces/current/logs/messages/em-1/content"},
		{"audit", "/workspaces/current/logs/audit"},
		{"audit export", "/workspaces/current/logs/audit/export"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, token := range []string{"", "not-a-jwt"} {
				rec := doLogsReq(o, tc.path, token, "3")
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("token %q: status = %d, want 401 (body %s)", token, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// ---------- message list: slim payload ----------

// TestLogsMessageListIsSlim pins the payload contract of the list: every
// metadata column, the embedded timeline, and never the bodies.
func TestLogsMessageListIsSlim(t *testing.T) {
	state := newFakeState()
	seedEmails(state)
	o := newLogsServer(t, fakeGorm(t, state), settingsProvider(t, state))
	token := logsTestJWT(t, "user")

	rec := doLogsReq(o, "/workspaces/current/logs/messages", token, "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, banned := range []string{"secret-html", "secret-text"} {
		if strings.Contains(body, banned) {
			t.Errorf("list response leaks body content %q", banned)
		}
	}

	envelope, data := decodeEnvelope(t, rec.Body.Bytes())
	if success, _ := envelope["success"].(bool); !success {
		t.Errorf("success = %v, want true", envelope["success"])
	}
	if len(data) != 1 {
		t.Fatalf("data rows = %d, want 1", len(data))
	}
	row, _ := data[0].(map[string]any)
	for _, banned := range []string{"html_body", "text_body", "attachments_json", "headers_json", "template_data"} {
		if _, present := row[banned]; present {
			t.Errorf("slim row contains key %q", banned)
		}
	}
	if row["uuid"] != "em-1" {
		t.Errorf("uuid = %v, want em-1", row["uuid"])
	}
	if row["recipient"] != "alice@example.com" {
		t.Errorf("recipient = %v, want the first recipient", row["recipient"])
	}
	if row["status"] != "sent" || row["provider"] != "postmark" {
		t.Errorf("status/provider = %v/%v", row["status"], row["provider"])
	}
	if row["external_id"] != "ext-1" {
		t.Errorf("external_id = %v, want ext-1", row["external_id"])
	}
	events, _ := row["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("embedded events = %d, want 1", len(events))
	}
	ev, _ := events[0].(map[string]any)
	if ev["type"] != "sent" {
		t.Errorf("embedded event type = %v, want sent", ev["type"])
	}
	if _, present := ev["occurred_at"]; !present {
		t.Error("embedded event must carry occurred_at")
	}
	pageable, _ := envelope["pageable"].(map[string]any)
	if pageable == nil || pageable["total_elements"] != float64(1) {
		t.Errorf("pageable = %v, want total_elements 1", envelope["pageable"])
	}
}

// TestLogsMessageListRejectsBadFilters pins that malformed filters are client
// errors, not silently ignored values.
func TestLogsMessageListRejectsBadFilters(t *testing.T) {
	state := newFakeState()
	seedEmails(state)
	o := newLogsServer(t, fakeGorm(t, state), settingsProvider(t, state))
	token := logsTestJWT(t, "user")

	cases := map[string]string{
		"unsupported channel": "/workspaces/current/logs/messages?channel=sms",
		"malformed list_id":   "/workspaces/current/logs/messages?list_id=abc",
		"malformed block_id":  "/workspaces/current/logs/messages?block_id=0",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			rec := doLogsReq(o, path, token, "3")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// ---------- message detail / events: authz + no bodies ----------

// TestLogsMessageDetailAuthzAndSlim pins the house convention: a member sees
// the message without bodies; a non-member gets the same 404 an unknown uuid
// gets — no existence leak.
func TestLogsMessageDetailAuthzAndSlim(t *testing.T) {
	state := newFakeState()
	seedEmails(state)
	o := newLogsServer(t, fakeGorm(t, state), settingsProvider(t, state))
	token := logsTestJWT(t, "user")

	rec := doLogsReq(o, "/workspaces/current/logs/messages/em-1", token, "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("member status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret-html") || strings.Contains(rec.Body.String(), "secret-text") {
		t.Error("detail response leaks body content")
	}
	row := decodeSingle(t, rec.Body.Bytes())
	if row["uuid"] != "em-1" {
		t.Errorf("uuid = %v", row["uuid"])
	}
	// The detail shape reuses the email model: bodies are present as empty
	// strings (the columns are never selected), never as content.
	if body, _ := row["html_body"].(string); body != "" {
		t.Errorf("detail html_body = %q, want empty", body)
	}
	if body, _ := row["text_body"].(string); body != "" {
		t.Errorf("detail text_body = %q, want empty", body)
	}

	// A different workspace gets the not-found answer.
	rec = doLogsReq(o, "/workspaces/current/logs/messages/em-1", token, "9")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-member status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestLogsMessageEvents pins the timeline endpoint: member 200 with the
// events of the message, non-member 404.
func TestLogsMessageEvents(t *testing.T) {
	state := newFakeState()
	seedEmails(state)
	o := newLogsServer(t, fakeGorm(t, state), settingsProvider(t, state))
	token := logsTestJWT(t, "user")

	rec := doLogsReq(o, "/workspaces/current/logs/messages/em-1/events", token, "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("member status = %d, want 200", rec.Code)
	}
	_, data := decodeEnvelope(t, rec.Body.Bytes())
	if len(data) != 1 {
		t.Fatalf("events = %d, want 1", len(data))
	}
	ev := data[0].(map[string]any)
	if ev["type"] != "sent" || ev["email_uuid"] != "em-1" {
		t.Errorf("event = %v", ev)
	}

	rec = doLogsReq(o, "/workspaces/current/logs/messages/em-1/events", token, "9")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-member status = %d, want 404", rec.Code)
	}
}

// ---------- content endpoint: visibility gate ----------

// TestLogsMessageContentHiddenByDefault pins the privacy gate: with the
// visibility setting off — including when no settings provider is wired — the
// content endpoint answers 200 {redacted:true} and never the bodies.
func TestLogsMessageContentHiddenByDefault(t *testing.T) {
	state := newFakeState()
	seedEmails(state) // settings table stays empty: visibility falls back to hidden
	sp := settingsProvider(t, state)
	o := newLogsServer(t, fakeGorm(t, state), sp)
	token := logsTestJWT(t, "user")

	rec := doLogsReq(o, "/workspaces/current/logs/messages/em-1/content", token, "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (redaction is not an error)", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret-html") || strings.Contains(rec.Body.String(), "secret-text") {
		t.Error("redacted response leaks body content")
	}
	row := decodeSingle(t, rec.Body.Bytes())
	if row["redacted"] != true {
		t.Fatalf("redacted = %v, want true", row["redacted"])
	}
	if _, present := row["html_body"]; present {
		t.Error("redacted response must not carry html_body")
	}

	// No provider wired at all: the default stays hidden.
	h := NewLogsHandler(fakeGorm(t, state), nil, nil)
	h.SetSettings(nil)
	if h.contentVisible() {
		t.Fatal("missing settings provider must default to hidden content")
	}
}

// TestLogsMessageContentServedWhenVisible pins the other side of the gate:
// with email_content_visibility on, the member gets the bodies, the render
// data and the attachment manifest.
func TestLogsMessageContentServedWhenVisible(t *testing.T) {
	state := newFakeState()
	seedEmails(state)
	state.tables["settings"] = fakeTable{
		columns: []string{"key", "value"},
		rows:    [][]driver.Value{{"email_content_visibility", "true"}},
	}
	o := newLogsServer(t, fakeGorm(t, state), settingsProvider(t, state))
	token := logsTestJWT(t, "user")

	rec := doLogsReq(o, "/workspaces/current/logs/messages/em-1/content", token, "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	row := decodeSingle(t, rec.Body.Bytes())
	if row["redacted"] == true {
		t.Fatal("visible content must not be redacted")
	}
	if row["html_body"] != "<p>secret-html</p>" || row["text_body"] != "secret-text" {
		t.Errorf("bodies = %v / %v", row["html_body"], row["text_body"])
	}
	if row["template_name"] != "welcome" {
		t.Errorf("template_name = %v", row["template_name"])
	}
	td, _ := row["template_data"].(map[string]any)
	if td == nil || td["name"] != "Alice" {
		t.Errorf("template_data = %v, want the render data", row["template_data"])
	}
	hdrs, _ := row["headers"].(map[string]any)
	if hdrs == nil || hdrs["X-Test"] != "1" {
		t.Errorf("headers = %v, want the stored headers", row["headers"])
	}
	atts, _ := row["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments = %d, want 1", len(atts))
	}
	att := atts[0].(map[string]any)
	if att["filename"] != "a.pdf" || att["storage_key"] != "att/1" {
		t.Errorf("attachment = %v", att)
	}
	if _, present := att["content"]; present {
		t.Error("attachment manifest must not carry content bytes")
	}

	// The content endpoint obeys the same ownership rule as the rest.
	rec = doLogsReq(o, "/workspaces/current/logs/messages/em-1/content", token, "9")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-member status = %d, want 404", rec.Code)
	}
}

// ---------- incoming webhooks: unlinked payloads are admin diagnostics ----------

// TestLogsIncomingWebhookUnlinkedNeedsAdmin pins that stored payloads that
// never resolved to a message are served to dashboard admins only.
func TestLogsIncomingWebhookUnlinkedNeedsAdmin(t *testing.T) {
	state := newFakeState()
	seedIncomingWebhook(state)
	o := newLogsServer(t, fakeGorm(t, state), settingsProvider(t, state))

	rec := doLogsReq(o, "/workspaces/current/logs/incoming-webhooks/9", logsTestJWT(t, "user"), "3")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-admin status = %d, want 404", rec.Code)
	}

	rec = doLogsReq(o, "/workspaces/current/logs/incoming-webhooks/9", logsTestJWT(t, "admin"), "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	row := decodeSingle(t, rec.Body.Bytes())
	if row["raw_payload"] != `{"bounce":true}` {
		t.Errorf("raw_payload = %v", row["raw_payload"])
	}
}

// ---------- audit: scopes + export ----------

// TestLogsAuditScopes pins the audit authorization: workspace scope for
// everyone, deployment scope for admins only, malformed filters rejected.
func TestLogsAuditScopes(t *testing.T) {
	state := newFakeState()
	seedAudit(state)
	o := newLogsServer(t, fakeGorm(t, state), settingsProvider(t, state))
	userToken := logsTestJWT(t, "user")
	adminToken := logsTestJWT(t, "admin")

	// Workspace scope is the default and needs no role.
	rec := doLogsReq(o, "/workspaces/current/logs/audit", userToken, "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace scope status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	_, data := decodeEnvelope(t, rec.Body.Bytes())
	if len(data) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(data))
	}
	first := data[0].(map[string]any)
	if first["type"] != "email.sent" || first["outcome"] != "success" {
		t.Errorf("audit row = %v", first)
	}

	// Deployment scope: refused for a plain user, allowed for an admin.
	rec = doLogsReq(o, "/workspaces/current/logs/audit?scope=deployment", userToken, "3")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("deployment scope as user = %d, want 403", rec.Code)
	}
	rec = doLogsReq(o, "/workspaces/current/logs/audit?scope=deployment", adminToken, "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("deployment scope as admin = %d, want 200", rec.Code)
	}

	// Malformed filters are client errors.
	for name, path := range map[string]string{
		"bad outcome":  "/workspaces/current/logs/audit?outcome=maybe",
		"bad scope":    "/workspaces/current/logs/audit?scope=galaxy",
		"bad from":     "/workspaces/current/logs/audit?from=notatime",
		"bad to":       "/workspaces/current/logs/audit?to=2026-13-99",
		"bad date key": "/workspaces/current/logs/audit?from=2026-05",
	} {
		t.Run(name, func(t *testing.T) {
			rec := doLogsReq(o, path, userToken, "3")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestLogsAuditExport pins the CSV export: text/csv content type, the header
// row, one line per event, and the same deployment-scope authorization.
func TestLogsAuditExport(t *testing.T) {
	state := newFakeState()
	seedAudit(state)
	o := newLogsServer(t, fakeGorm(t, state), settingsProvider(t, state))
	userToken := logsTestJWT(t, "user")

	rec := doLogsReq(o, "/workspaces/current/logs/audit/export", userToken, "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content-type = %q, want text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "audit-logs-workspace") {
		t.Errorf("content-disposition = %q", cd)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("cache-control = %q, want no-store", cc)
	}

	lines := strings.Split(strings.TrimRight(rec.Body.String(), "\n"), "\n")
	const header = "id,occurred_at,workspace_id,action,category,outcome,actor_id,actor_name,target_type,target_id,ip_address,message,metadata"
	if len(lines) != 3 {
		t.Fatalf("csv lines = %d, want header + 2 events:\n%s", len(lines), rec.Body.String())
	}
	if lines[0] != header {
		t.Errorf("csv header = %q, want %q", lines[0], header)
	}
	if !strings.Contains(rec.Body.String(), "email.sent") || !strings.Contains(rec.Body.String(), "em-1") {
		t.Error("csv must contain the event data")
	}
	if !strings.Contains(lines[1], "email.sent") {
		t.Errorf("first data line = %q", lines[1])
	}

	// Same authorization as the list: deployment scope stays admin-only.
	rec = doLogsReq(o, "/workspaces/current/logs/audit/export?scope=deployment", userToken, "3")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("export deployment scope as user = %d, want 403", rec.Code)
	}
}

// ---------- Classifications (SF-53) ----------

// seedClassificationEvents loads a timeline with classified rows for two
// workspaces plus unclassified rows, so the breakdown's counting, window and
// workspace scoping are all observable.
func seedClassificationEvents(state *fakeState) {
	state.tables["email_events"] = fakeTable{
		columns: []string{
			"id", "email_uuid", "workspace_id", "type", "occurred_at", "provider",
			"provider_event_id", "recipient", "ip", "user_agent", "url", "error",
			"metadata", "created_at",
		},
		rows: [][]driver.Value{
			{int64(1), "em-1", int64(3), "bounced", logsTestTime, "postmark", "", "a@x.y", "", "", "", "", `{"classification":"bad_domain"}`, logsTestTime},
			{int64(2), "em-2", int64(3), "bounced", logsTestTime.Add(-time.Hour), "ses", "", "b@x.y", "", "", "", "", `{"classification":"mailbox_not_found"}`, logsTestTime},
			{int64(3), "em-3", int64(3), "failed", logsTestTime.Add(2 * time.Hour), "ses", "", "c@x.y", "", "", "", "", `{"classification":"bad_domain"}`, logsTestTime},
			// Another workspace's row must never leak into workspace 3's counts.
			{int64(4), "em-4", int64(5), "bounced", logsTestTime, "postmark", "", "d@x.y", "", "", "", "", `{"classification":"bad_domain"}`, logsTestTime},
			// Unclassified rows are excluded by the query's metadata predicate.
			{int64(5), "em-5", int64(3), "sent", logsTestTime, "", "", "e@x.y", "", "", "", "", `{}`, logsTestTime},
		},
	}
}

// The classifications endpoint serves the ingestion service's workspace-scoped
// aggregate over the fake database: happy path, time window, and 400s on
// malformed bounds.
func TestLogsClassifications(t *testing.T) {
	state := newFakeState()
	seedClassificationEvents(state)
	db := fakeGorm(t, state)
	// The real provider_events service runs its real query through the fake
	// driver — the handler is exercised against the genuine aggregate.
	svc := provider_events.NewIngestionService(nil, nil)
	svc.SetDB(db)
	o := newLogsServer(t, db, settingsProvider(t, state), func(h *LogsHandler) {
		h.SetClassificationCounter(svc)
	})

	rec := doLogsReq(o, "/workspaces/current/logs/classifications", logsTestJWT(t, "admin"), "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("classifications = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Data struct {
			Counts map[string]int64 `json:"counts"`
			Total  int64            `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Data.Counts["bad_domain"] != 2 || res.Data.Counts["mailbox_not_found"] != 1 {
		t.Fatalf("counts wrong: %+v", res.Data.Counts)
	}
	if res.Data.Total != 3 {
		t.Fatalf("total = %d, want 3", res.Data.Total)
	}

	// The time window bounds the aggregate: rows 2 (-1h) and 3 (+2h) fall
	// outside [-30m, +30m], leaving only row 1.
	rec = doLogsReq(o,
		"/workspaces/current/logs/classifications?from="+url.QueryEscape(logsTestTime.Add(-30*time.Minute).Format(time.RFC3339))+
			"&to="+url.QueryEscape(logsTestTime.Add(30*time.Minute).Format(time.RFC3339)),
		logsTestJWT(t, "admin"), "3")
	if rec.Code != http.StatusOK {
		t.Fatalf("windowed classifications = %d: %s", rec.Code, rec.Body.String())
	}
	var windowed struct {
		Data struct {
			Counts map[string]int64 `json:"counts"`
			Total  int64            `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &windowed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if windowed.Data.Counts["bad_domain"] != 1 || windowed.Data.Total != 1 {
		t.Fatalf("windowed counts wrong: %+v", windowed.Data)
	}

	// Malformed bounds are client errors.
	if rec := doLogsReq(o, "/workspaces/current/logs/classifications?from=bogus", logsTestJWT(t, "admin"), "3"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad from = %d, want 400", rec.Code)
	}
	if rec := doLogsReq(o, "/workspaces/current/logs/classifications?to=bogus", logsTestJWT(t, "admin"), "3"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad to = %d, want 400", rec.Code)
	}
}

// Without the counter wired (no ingestion service in this deployment) the
// endpoint degrades to 503 instead of reporting an empty breakdown.
func TestLogsClassificationsUnavailableWithoutCounter(t *testing.T) {
	state := newFakeState()
	seedClassificationEvents(state)
	db := fakeGorm(t, state)
	o := newLogsServer(t, db, settingsProvider(t, state))

	rec := doLogsReq(o, "/workspaces/current/logs/classifications", logsTestJWT(t, "admin"), "3")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-counter classifications = %d, want 503", rec.Code)
	}
}

// ---------- Message events live stream (SF-56) ----------

// The per-message SSE stream completes the handshake, opens at the newest
// existing event, and delivers rows recorded after the client connected as
// email.event frames within one poll tick.
func TestLogsMessageEventsStream(t *testing.T) {
	state := newFakeState()
	seedEmails(state) // em-1, workspace 3, newest event id=5
	db := fakeGorm(t, state)
	o := newLogsServer(t, db, settingsProvider(t, state))
	srv := httptest.NewServer(o)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/workspaces/current/logs/em-1/events/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "admin"))
	req.Header.Set("X-Test-Workspace", "3")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// Record a new timeline row once the stream is definitively past its
	// initial snapshot: the handshake flush makes the client's Do return
	// before the handler has necessarily computed its lastID watermark, so
	// appending immediately could race the row into the "history" side. Half
	// a second is far beyond that window and still well under one tick.
	time.Sleep(500 * time.Millisecond)
	state.mu.Lock()
	tbl := state.tables["email_events"]
	tbl.rows = append(tbl.rows, []driver.Value{
		int64(6), "em-1", int64(3), "delivered", logsTestTime.Add(time.Minute), "postmark",
		"", "a@x.y", "", "", "", "", `{"classification":"delivered"}`, logsTestTime,
	})
	state.tables["email_events"] = tbl
	state.mu.Unlock()

	scanner := bufio.NewScanner(resp.Body)
	foundEvent := false
	var payload string
	var seen []string
	for scanner.Scan() {
		line := scanner.Text()
		seen = append(seen, line)
		if line == "event: email.event" {
			foundEvent = true
			continue
		}
		if foundEvent && strings.HasPrefix(line, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			break
		}
	}
	cancel()
	if !foundEvent {
		t.Fatalf("no email.event frame within the deadline; lines read: %q (scan err: %v)", seen, scanner.Err())
	}
	var evt struct {
		ID    uint   `json:"id"`
		Type  string `json:"type"`
		Email string `json:"email_uuid"`
	}
	if err := json.Unmarshal([]byte(payload), &evt); err != nil {
		t.Fatalf("decode frame %q: %v", payload, err)
	}
	if evt.ID != 6 || evt.Type != "delivered" || evt.Email != "em-1" {
		t.Fatalf("streamed event wrong: %+v", evt)
	}
}

// The stream is scoped like every message endpoint: a message outside the
// caller's workspace answers 404 and an unauthenticated request is rejected by
// the middleware. (The fake driver answers every uuid lookup with the seeded
// row — it does not emulate WHERE — so the unknown-message 404 is not
// exercisable at this layer; the repository tests cover the lookup itself.)
func TestLogsMessageEventsStreamAuthz(t *testing.T) {
	state := newFakeState()
	seedEmails(state)
	db := fakeGorm(t, state)
	o := newLogsServer(t, db, settingsProvider(t, state))

	// Another workspace's message.
	rec := doLogsReq(o, "/workspaces/current/logs/em-1/events/stream", logsTestJWT(t, "admin"), "4")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign message stream = %d, want 404", rec.Code)
	}
	// No session.
	req := httptest.NewRequest(http.MethodGet, "/workspaces/current/logs/em-1/events/stream", nil)
	rec2 := httptest.NewRecorder()
	o.ServeHTTP(rec2, req)
	if rec2.Code == http.StatusOK {
		t.Fatalf("unauthenticated stream = %d, want 401/403", rec2.Code)
	}
}
