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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/services/emailhistory"
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
		if strings.Contains(lower, "count(") {
			return &fakeRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(len(tbl.rows))}}}, nil
		}
		cols, rows := selectedColumns(lower, tbl)
		return &fakeRows{columns: cols, rows: rows}, nil
	}
	// Unknown statements answer with an empty set.
	return &fakeRows{columns: []string{"x"}}, nil
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
func newLogsServer(t *testing.T, db *gorm.DB, sp *settings.Provider) *okapi.TestServer {
	t.Helper()
	h := NewLogsHandler(db, emailhistory.NewRecorder(nil), sp)
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
	return o
}

// doLogsReq performs one request against the test server.
func doLogsReq(o *okapi.TestServer, method, path, token, workspace string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
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
				rec := doLogsReq(o, http.MethodGet, tc.path, token, "3")
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

	rec := doLogsReq(o, http.MethodGet, "/workspaces/current/logs/messages", token, "3")
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
			rec := doLogsReq(o, http.MethodGet, path, token, "3")
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

	rec := doLogsReq(o, http.MethodGet, "/workspaces/current/logs/messages/em-1", token, "3")
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
	rec = doLogsReq(o, http.MethodGet, "/workspaces/current/logs/messages/em-1", token, "9")
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

	rec := doLogsReq(o, http.MethodGet, "/workspaces/current/logs/messages/em-1/events", token, "3")
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

	rec = doLogsReq(o, http.MethodGet, "/workspaces/current/logs/messages/em-1/events", token, "9")
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

	rec := doLogsReq(o, http.MethodGet, "/workspaces/current/logs/messages/em-1/content", token, "3")
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

	rec := doLogsReq(o, http.MethodGet, "/workspaces/current/logs/messages/em-1/content", token, "3")
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
	rec = doLogsReq(o, http.MethodGet, "/workspaces/current/logs/messages/em-1/content", token, "9")
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

	rec := doLogsReq(o, http.MethodGet, "/workspaces/current/logs/incoming-webhooks/9", logsTestJWT(t, "user"), "3")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-admin status = %d, want 404", rec.Code)
	}

	rec = doLogsReq(o, http.MethodGet, "/workspaces/current/logs/incoming-webhooks/9", logsTestJWT(t, "admin"), "3")
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
	rec := doLogsReq(o, http.MethodGet, "/workspaces/current/logs/audit", userToken, "3")
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
	rec = doLogsReq(o, http.MethodGet, "/workspaces/current/logs/audit?scope=deployment", userToken, "3")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("deployment scope as user = %d, want 403", rec.Code)
	}
	rec = doLogsReq(o, http.MethodGet, "/workspaces/current/logs/audit?scope=deployment", adminToken, "3")
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
			rec := doLogsReq(o, http.MethodGet, path, userToken, "3")
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

	rec := doLogsReq(o, http.MethodGet, "/workspaces/current/logs/audit/export", userToken, "3")
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
	rec = doLogsReq(o, http.MethodGet, "/workspaces/current/logs/audit/export?scope=deployment", userToken, "3")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("export deployment scope as user = %d, want 403", rec.Code)
	}
}
