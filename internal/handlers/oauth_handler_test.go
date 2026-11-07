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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SF-37 residual: the OAuth surface had zero tests. These cover the paths
// exercisable offline through the real okapi stack: the login-page provider
// list, email-based SSO discovery (config validation), the admin provider
// CRUD, and the workspace SSO binding incl. per-workspace isolation. The
// redirect ceremonies (Authorize/Callback) need a live Redis + upstream and
// are out of the fake's reach.

// ---------- fake state ----------

var oatProviderColumns = []string{
	"id", "workspace_id", "name", "slug", "type", "client_id", "client_secret",
	"issuer", "auth_url", "token_url", "userinfo_url", "scopes",
	"enabled", "hidden", "auto_register", "allowed_domains",
	"created_at", "updated_at",
}

var oatSSOColumns = []string{
	"id", "workspace_id", "provider_id", "enforce_sso", "auto_provision",
	"allowed_domains", "created_at", "updated_at",
}

type oatState struct {
	mu        sync.Mutex
	seq       int64
	providers map[int64]map[string]driver.Value
	sso       map[int64]map[string]driver.Value // keyed by workspace id
}

func newOatState() *oatState {
	return &oatState{seq: 300, providers: map[int64]map[string]driver.Value{}, sso: map[int64]map[string]driver.Value{}}
}

func (s *oatState) seedProvider(id int64, slug, ptype string, enabled, hidden bool, wsID any, domains string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := make(map[string]driver.Value, len(oatProviderColumns))
	row["id"] = id
	row["name"] = "Provider " + slug
	row["slug"] = slug
	row["type"] = ptype
	row["client_id"] = "client-" + slug
	row["client_secret"] = "secret-" + slug
	row["scopes"] = "openid email profile"
	row["enabled"] = enabled
	row["hidden"] = hidden
	row["auto_register"] = true
	row["allowed_domains"] = domains
	row["created_at"] = time.Now().UTC()
	row["updated_at"] = time.Now().UTC()
	if wsID == nil {
		row["workspace_id"] = nil
	} else {
		row["workspace_id"] = wsID
	}
	s.providers[id] = row
}

func oatGorm(t *testing.T, state *oatState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(oatConnector{state: state}),
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open oat fake gorm: %v", err)
	}
	return gdb
}

type oatConnector struct{ state *oatState }

func (c oatConnector) Connect(context.Context) (driver.Conn, error) { return &oatConn{s: c.state}, nil }
func (c oatConnector) Driver() driver.Driver                        { return oatDrv{} }

type oatDrv struct{}

func (oatDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type oatConn struct{ s *oatState }

func (c *oatConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *oatConn) Close() error                        { return nil }
func (c *oatConn) Begin() (driver.Tx, error)           { return oatTx{}, nil }

type oatTx struct{}

func (oatTx) Commit() error   { return nil }
func (oatTx) Rollback() error { return nil }

type oatResult struct{ rows int64 }

func (r oatResult) LastInsertId() (int64, error) { return 0, nil }
func (r oatResult) RowsAffected() (int64, error) { return r.rows, nil }

type oatRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *oatRows) Columns() []string { return r.cols }
func (r *oatRows) Close() error      { return nil }
func (r *oatRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func (c *oatConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.Contains(lower, "insert into") {
		if strings.Contains(lower, `insert into "o_auth_providers"`) {
			return c.insertProvider(lower, query, args)
		}
		if strings.Contains(lower, `insert into "workspace_sso_configs"`) {
			return c.insertSSO(lower, query, args)
		}
	}
	if strings.Contains(lower, `from "o_auth_providers"`) {
		return c.selectProviders(lower, args)
	}
	if strings.Contains(lower, `from "workspace_sso_configs"`) {
		return c.selectSSO(lower, args)
	}
	if strings.Contains(lower, `from "o_auth_accounts"`) {
		return &oatRows{cols: []string{"id"}}, nil
	}
	fmt.Println("OAT-DEBUG unrouted query:", query)
	return nil, fmt.Errorf("oatState: cannot route query: %s", query)
}

// oatRowValues projects a stored row onto the requested column order.
func oatRowValues(row map[string]driver.Value, cols []string) []driver.Value {
	vals := make([]driver.Value, len(cols))
	for i, col := range cols {
		vals[i] = row[col]
	}
	return vals
}

// oatBool compares a stored truthy/falsy value with a wanted bool.
func oatBool(v driver.Value, want bool) bool {
	switch b := v.(type) {
	case bool:
		return b == want
	case int64:
		return (b != 0) == want
	}
	return false
}

func (c *oatConn) selectProviders(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s

	// HasEnabledHidden's aggregate.
	if strings.Contains(lower, "count(") {
		n := int64(0)
		for _, row := range s.providers {
			if oatBool(row["enabled"], true) && oatBool(row["hidden"], true) {
				n++
			}
		}
		return &oatRows{cols: []string{"count"}, rows: [][]driver.Value{{n}}}, nil
	}

	// Preload("Provider"): WHERE id IN ($1, …).
	if strings.Contains(lower, " in (") {
		if i := strings.Index(lower, " in ("); i >= 0 {
			end := strings.Index(lower[i:], ")")
			inside := lower[i+len(" in (") : i+end]
			var ids []int64
			for _, tok := range strings.Split(inside, ",") {
				tok = strings.TrimSpace(tok)
				if strings.HasPrefix(tok, "$") {
					ids = append(ids, oatInt64(oatArg(args, oatOrdinal(tok))))
				}
			}
			var rows [][]driver.Value
			for _, id := range ids {
				if row, ok := s.providers[id]; ok {
					rows = append(rows, oatRowValues(row, oatProviderColumns))
				}
			}
			return &oatRows{cols: oatProviderColumns, rows: rows}, nil
		}
	}

	conds, _ := oatConditions(oatWhere(lower))
	var rows [][]driver.Value
	for _, row := range s.providers {
		matched := true
		for col, ord := range conds {
			if fmt.Sprint(row[col]) != fmt.Sprint(oatArg(args, ord)) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		// The static filters the repos bake into their WHERE strings.
		if strings.Contains(lower, "enabled = true") && !oatBool(row["enabled"], true) {
			continue
		}
		if strings.Contains(lower, "enabled = false") && !oatBool(row["enabled"], false) {
			continue
		}
		if strings.Contains(lower, "hidden = false") && oatBool(row["hidden"], true) {
			continue
		}
		if strings.Contains(lower, "workspace_id is null") && row["workspace_id"] != nil {
			continue
		}
		if strings.Contains(lower, "allowed_domains <> ''") {
			if d, _ := row["allowed_domains"].(string); strings.TrimSpace(d) == "" {
				continue
			}
		}
		rows = append(rows, oatRowValues(row, oatProviderColumns))
	}
	return &oatRows{cols: oatProviderColumns, rows: rows}, nil
}

func (c *oatConn) selectSSO(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	conds, _ := oatConditions(oatWhere(lower))
	wsOrd, ok := conds["workspace_id"]
	if !ok {
		return &oatRows{cols: oatSSOColumns}, nil
	}
	want := fmt.Sprint(oatArg(args, wsOrd))
	var rows [][]driver.Value
	for wsID, row := range s.sso {
		if fmt.Sprint(wsID) != want {
			continue
		}
		rows = append(rows, oatRowValues(row, oatSSOColumns))
	}
	return &oatRows{cols: oatSSOColumns, rows: rows}, nil
}


// insertProvider stores a new provider row; the fake enforces the slug unique
// index so the handler's conflict path is reachable.
func (c *oatConn) insertProvider(lower, query string, args []driver.NamedValue) (driver.Rows, error) {
	values := oatInsertColumns(lower, query, args)
	if values == nil {
		return nil, fmt.Errorf("oatState: provider insert without VALUES: %s", query)
	}
	s := c.s // the caller (QueryContext) already holds s.mu
	slug := fmt.Sprint(values["slug"])
	for _, row := range s.providers {
		if fmt.Sprint(row["slug"]) == slug {
			return nil, fmt.Errorf("oatState: duplicate key on oauth_providers.slug (unique index)")
		}
	}
	s.seq++
	values["id"] = s.seq
	s.providers[s.seq] = values
	return &oatRows{cols: []string{"id"}, rows: [][]driver.Value{{s.seq}}}, nil
}

func (c *oatConn) insertSSO(lower, query string, args []driver.NamedValue) (driver.Rows, error) {
	values := oatInsertColumns(lower, query, args)
	if values == nil {
		return nil, fmt.Errorf("oatState: sso insert without VALUES: %s", query)
	}
	s := c.s // the caller (QueryContext) already holds s.mu
	wsID := oatInt64(values["workspace_id"])
	s.seq++
	values["id"] = s.seq
	s.sso[wsID] = values
	return &oatRows{cols: []string{"id"}, rows: [][]driver.Value{{s.seq}}}, nil
}

func (c *oatConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case strings.Contains(lower, `update "o_auth_providers"`):
		setStart := strings.Index(lower, " set ")
		whereStart := strings.Index(lower, " where ")
		conds, _ := oatConditions(lower[whereStart+len(" where "):])
		id := oatInt64(oatArg(args, conds["id"]))
		row, ok := s.providers[id]
		if !ok {
			return oatResult{rows: 0}, nil
		}
		setClause := lower[setStart+len(" set "):whereStart]
		for _, pair := range strings.Split(setClause, ",") {
			eq := strings.Index(pair, "=")
			if eq < 0 {
				continue
			}
			col := oatColumnOf(pair[:eq])
			if _, known := row[col]; known {
				row[col] = oatArg(args, oatOrdinal(pair[eq+1:]))
			}
		}
		return oatResult{rows: 1}, nil

	case strings.Contains(lower, `update "workspace_sso_configs"`):
		setStart := strings.Index(lower, " set ")
		whereStart := strings.Index(lower, " where ")
		conds, _ := oatConditions(lower[whereStart+len(" where "):])
		id := oatInt64(oatArg(args, conds["id"]))
		for wsID, row := range s.sso {
			if fmt.Sprint(row["id"]) == fmt.Sprint(id) {
				setClause := lower[setStart+len(" set "):whereStart]
				for _, pair := range strings.Split(setClause, ",") {
					eq := strings.Index(pair, "=")
					if eq < 0 {
						continue
					}
					col := oatColumnOf(pair[:eq])
					if _, known := row[col]; known {
						row[col] = oatArg(args, oatOrdinal(pair[eq+1:]))
					}
				}
				s.sso[wsID] = row
				return oatResult{rows: 1}, nil
			}
		}
		return oatResult{rows: 0}, nil

	case strings.Contains(lower, `delete from "o_auth_providers"`):
		conds, _ := oatConditions(oatWhere(lower))
		id := oatInt64(oatArg(args, conds["id"]))
		if _, ok := s.providers[id]; ok {
			delete(s.providers, id)
			return oatResult{rows: 1}, nil
		}
		return oatResult{rows: 0}, nil

	case strings.Contains(lower, `delete from "workspace_sso_configs"`):
		conds, _ := oatConditions(oatWhere(lower))
		wsID := oatInt64(oatArg(args, conds["workspace_id"]))
		if _, ok := s.sso[wsID]; ok {
			delete(s.sso, wsID)
			return oatResult{rows: 1}, nil
		}
		return oatResult{rows: 0}, nil
	}
	fmt.Println("OAT-DEBUG unsupported exec:", query)
	return nil, fmt.Errorf("oatState: unsupported exec: %s", query)
}

// ---------- small SQL parsing helpers (package-local, oat-prefixed) ----------

func oatArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("oatState: no arg with ordinal %d", n))
}

func oatOrdinal(tok string) int {
	tok = strings.TrimSpace(tok)
	var n int
	if _, err := fmt.Sscanf(tok[1:], "%d", &n); err != nil {
		panic(fmt.Sprintf("oatState: bad bindvar %q", tok))
	}
	return n
}

func oatInt64(v driver.Value) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case uint64:
		return int64(x)
	case float64:
		return int64(x)
	}
	var n int64
	_, _ = fmt.Sscanf(fmt.Sprint(v), "%d", &n)
	return n
}

func oatColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func oatWhere(query string) string {
	i := strings.Index(query, " where ")
	if i < 0 {
		return ""
	}
	where := query[i+len(" where "):]
	for _, cut := range []string{" order by ", " returning ", " limit "} {
		if j := strings.Index(where, cut); j >= 0 {
			where = where[:j]
		}
	}
	return where
}

func oatConditions(where string) (map[string]int, map[string]bool) {
	conds := map[string]int{}
	nulls := map[string]bool{}
	where = strings.NewReplacer("(", " ", ")", " ").Replace(where)
	for _, part := range strings.Split(where, " and ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasSuffix(part, " is null") {
			nulls[oatColumnOf(strings.TrimSuffix(part, " is null"))] = true
			continue
		}
		eq := strings.Index(part, " = ")
		if eq < 0 {
			continue
		}
		rhs := strings.TrimSpace(part[eq+3:])
		// Only $N right-hand sides are bindvars; literal true/false filters
		// are applied by the substring checks in the select functions.
		if strings.HasPrefix(rhs, "$") {
			conds[oatColumnOf(part[:eq])] = oatOrdinal(rhs)
		}
	}
	return conds, nulls
}

// oatInsertColumns maps an INSERT's bound values to its columns.
func oatInsertColumns(lower, query string, args []driver.NamedValue) map[string]driver.Value {
	i := strings.Index(lower, " values (")
	if i < 0 {
		return nil
	}
	open := strings.LastIndex(query[:i], "(")
	parts := strings.Split(strings.ReplaceAll(query[open+1:i], `"`, ""), ",")
	values := make(map[string]driver.Value, len(parts))
	for j, p := range parts {
		if j < len(args) {
			values[strings.TrimSpace(p)] = args[j].Value
		}
	}
	return values
}

// ---------- harness ----------

// oatWorkspaceBinding plays the workspace middleware for the SSO tests: the
// X-Test-Workspace header names the caller's workspace.
var oatWorkspaceBinding okapi.Middleware = func(c *okapi.Context) error {
	if ws := c.Request().Header.Get("X-Test-Workspace"); ws != "" {
		c.Set(middlewares.CtxWorkspaceID, ws)
		c.Set(middlewares.CtxWorkspaceRole, "admin")
	}
	return c.Next()
}

type oatHarness struct {
	state *oatState
	o     *okapi.TestServer
}

func newOATHarness(t *testing.T) *oatHarness {
	t.Helper()
	state := newOatState()
	gdb := oatGorm(t, state)

	admin := NewOAuthAdminHandler(
		repositories.NewOAuthProviderRepository(gdb),
		repositories.NewWorkspaceSSORepository(gdb),
	)
	public := NewOAuthHandler(nil,
		repositories.NewOAuthProviderRepository(gdb), repositories.NewOAuthAccountRepository(gdb),
		nil, nil, oatTestSecret, nil, nil, nil, "http://localhost:9000", "http://localhost:9000")

	o := okapi.NewTestServer(t)
	o.Post("/auth/oauth/providers", okapi.H(admin.CreateProvider))
	o.Get("/auth/oauth/providers", admin.ListProviders)
	o.Put("/auth/oauth/providers/{id:int}", okapi.H(admin.UpdateProvider))
	o.Delete("/auth/oauth/providers/{id:int}", okapi.H(admin.DeleteProvider))
	wsGroup := o.Group("/workspaces/current", oatWorkspaceBinding)
	wsGroup.Get("/sso", admin.GetWorkspaceSSO)
	wsGroup.Put("/sso", okapi.H(admin.SetWorkspaceSSO))
	wsGroup.Delete("/sso", admin.DeleteWorkspaceSSO)
	o.Post("/auth/oauth/discover", okapi.H(public.DiscoverSSO))
	o.Get("/auth/oauth/login-providers", public.ListProviders)
	return &oatHarness{state: state, o: o}
}

const oatTestSecret = "oauth-test-secret"

func (h *oatHarness) do(t *testing.T, method, path, body string, ws string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if ws != "" {
		req.Header.Set("X-Test-Workspace", ws)
	}
	req.Header.Set("Authorization", "Bearer "+oatJWT(t))
	rec := httptest.NewRecorder()
	h.o.ServeHTTP(rec, req)
	return rec
}

func oatJWT(t *testing.T) string {
	t.Helper()
	tok, err := okapi.GenerateJwtToken([]byte(oatTestSecret), map[string]any{
		"sub": 9, "email": "admin@example.com", "role": "admin", "aud": "posta", "jti": "jti-oat",
	}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateJwtToken: %v", err)
	}
	return tok
}

func oatJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	return env
}

// ---------- tests: admin provider CRUD ----------

func TestOAuthAdmin_CreateProvider(t *testing.T) {
	h := newOATHarness(t)

	rec := h.do(t, http.MethodPost, "/auth/oauth/providers",
		`{"name":"Acme OIDC","slug":"  Acme ","type":"oidc","client_id":"cid","client_secret":"cs"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	env := oatJSON(t, rec)
	data := env["data"].(map[string]any)
	if data["slug"] != "acme" {
		t.Fatalf("slug = %v, want lowercased/trimmed 'acme'", data["slug"])
	}
	if data["scopes"] != "openid email profile" {
		t.Fatalf("default scopes = %v", data["scopes"])
	}
	if data["enabled"] != true || data["auto_register"] != true {
		t.Fatalf("defaults wrong: enabled=%v auto_register=%v", data["enabled"], data["auto_register"])
	}
	if _, hasSecret := data["client_secret"]; hasSecret {
		t.Fatal("client_secret must not be echoed back")
	}

	// A duplicate slug conflicts.
	rec = h.do(t, http.MethodPost, "/auth/oauth/providers",
		`{"name":"Acme again","slug":"acme","type":"oidc","client_id":"cid","client_secret":"cs"}`, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate slug = %d, want 409", rec.Code)
	}
}

func TestOAuthAdmin_CreateProviderRejectsUnknownType(t *testing.T) {
	h := newOATHarness(t)
	rec := h.do(t, http.MethodPost, "/auth/oauth/providers",
		`{"name":"Evil","slug":"evil","type":"facebook","client_id":"c","client_secret":"s"}`, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown type = %d, want 400", rec.Code)
	}
}

func TestOAuthAdmin_UpdateProvider(t *testing.T) {
	h := newOATHarness(t)
	h.state.seedProvider(21, "corp", "oidc", true, false, nil, "")
	h.state.seedProvider(22, "other", "google", true, false, nil, "")

	// Rename + disable; fields left empty in the body stay untouched.
	rec := h.do(t, http.MethodPut, "/auth/oauth/providers/21",
		`{"name":"Corp SSO","enabled":false}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	h.state.mu.Lock()
	p := h.state.providers[21]
	h.state.mu.Unlock()
	if p["name"] != "Corp SSO" {
		t.Fatalf("name = %v", p["name"])
	}
	if !oatBool(p["enabled"], false) {
		t.Fatal("enabled must now be false")
	}
	if p["client_id"] != "client-corp" {
		t.Fatalf("empty body field overwrote client_id: %v", p["client_id"])
	}

	// Unknown id → 404.
	rec = h.do(t, http.MethodPut, "/auth/oauth/providers/999", `{"name":"X"}`, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("update unknown = %d, want 404", rec.Code)
	}
}

func TestOAuthAdmin_DeleteProvider(t *testing.T) {
	h := newOATHarness(t)
	h.state.seedProvider(31, "gone", "google", true, false, nil, "")

	rec := h.do(t, http.MethodDelete, "/auth/oauth/providers/31", "", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rec.Code)
	}
	// Deleting an already-deleted provider is idempotent: gorm reports zero
	// rows without an error, and the handler answers 204 again (pinned as the
	// current contract).
	rec = h.do(t, http.MethodDelete, "/auth/oauth/providers/31", "", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete again = %d, want 204", rec.Code)
	}
	// The row is really gone from the list.
	rec = h.do(t, http.MethodGet, "/auth/oauth/providers", "", "")
	env := oatJSON(t, rec)
	if rows, ok := env["data"].([]any); ok && len(rows) != 0 {
		t.Fatalf("deleted provider still listed: %v", env["data"])
	}
}

func TestOAuthAdmin_ListProviders(t *testing.T) {
	h := newOATHarness(t)
	h.state.seedProvider(41, "one", "google", true, false, nil, "")
	h.state.seedProvider(42, "two", "oidc", true, false, nil, "")

	rec := h.do(t, http.MethodGet, "/auth/oauth/providers", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	env := oatJSON(t, rec)
	data := env["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("list rows = %d, want 2", len(data))
	}
	for _, item := range data {
		if _, has := item.(map[string]any)["client_secret"]; has {
			t.Fatal("client_secret must not appear in the admin list")
		}
	}
}

// ---------- tests: the login-page surface ----------

func TestOAuthPublic_ListProvidersLogin(t *testing.T) {
	h := newOATHarness(t)
	h.state.seedProvider(51, "google", "google", true, false, nil, "")
	h.state.seedProvider(52, "hidden-sso", "oidc", true, true, nil, "")

	rec := h.do(t, http.MethodGet, "/auth/oauth/login-providers", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	env := oatJSON(t, rec)
	data := env["data"].(map[string]any)
	providers := data["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("visible providers = %d, want 1 (hidden excluded)", len(providers))
	}
	if data["sso_available"] != true {
		t.Fatal("a hidden enabled provider must advertise SSO availability")
	}
}

func TestOAuthPublic_DiscoverSSO(t *testing.T) {
	h := newOATHarness(t)
	h.state.seedProvider(61, "corp", "oidc", true, true, nil, "acme.com, corp.example")

	// A matching domain resolves to its provider slug.
	rec := h.do(t, http.MethodPost, "/auth/oauth/discover", `{"email":"someone@ACME.com"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("discover = %d: %s", rec.Code, rec.Body.String())
	}
	env := oatJSON(t, rec)
	data := env["data"].(map[string]any)
	if data["slug"] != "corp" {
		t.Fatalf("slug = %v, want corp", data["slug"])
	}

	// No provider for the domain → 404.
	rec = h.do(t, http.MethodPost, "/auth/oauth/discover", `{"email":"someone@stranger.example"}`, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("discover unknown domain = %d, want 404", rec.Code)
	}

	// Malformed addresses → 400, never a lookup.
	for _, bad := range []string{"no-at-sign", "trailing@", ""} {
		rec = h.do(t, http.MethodPost, "/auth/oauth/discover", fmt.Sprintf(`{"email":%q}`, bad), "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("discover %q = %d, want 400", bad, rec.Code)
		}
	}
	// "@leading" parses as domain "leading" (the handler only rejects a
	// missing or trailing @) — no provider for it, so 404.
	rec = h.do(t, http.MethodPost, "/auth/oauth/discover", `{"email":"@leading"}`, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("discover @leading = %d, want 404", rec.Code)
	}
}

// ---------- tests: workspace SSO binding + isolation ----------

func TestOAuthWorkspace_SSOBindingIsolation(t *testing.T) {
	h := newOATHarness(t)
	h.state.seedProvider(71, "corp", "oidc", true, true, nil, "")

	// Workspace 1 binds the provider.
	rec := h.do(t, http.MethodPut, "/workspaces/current/sso",
		`{"provider_id":71,"enforce_sso":true,"auto_provision":true,"allowed_domains":"acme.com"}`, "1")
	if rec.Code != http.StatusOK {
		t.Fatalf("set sso = %d: %s", rec.Code, rec.Body.String())
	}

	// Workspace 2 sees NO config: bindings do not leak across workspaces.
	rec = h.do(t, http.MethodGet, "/workspaces/current/sso", "", "2")
	if rec.Code != http.StatusOK {
		t.Fatalf("get sso ws2 = %d", rec.Code)
	}
	env := oatJSON(t, rec)
	if env["data"] != nil {
		t.Fatalf("workspace 2 must see no SSO config, got %v", env["data"])
	}

	// Workspace 1 sees its own binding.
	rec = h.do(t, http.MethodGet, "/workspaces/current/sso", "", "1")
	env = oatJSON(t, rec)
	data := env["data"].(map[string]any)
	if data["provider_id"] != float64(71) || data["enforce_sso"] != true {
		t.Fatalf("workspace 1 sso = %v", data)
	}

	// Updating from workspace 2 creates its own row, not a mutation of ws1's.
	rec = h.do(t, http.MethodPut, "/workspaces/current/sso",
		`{"provider_id":71,"enforce_sso":false,"auto_provision":false}`, "2")
	if rec.Code != http.StatusOK {
		t.Fatalf("set sso ws2 = %d", rec.Code)
	}
	h.state.mu.Lock()
	ws1 := h.state.sso[1]
	ws2 := h.state.sso[2]
	h.state.mu.Unlock()
	if !oatBool(ws1["enforce_sso"], true) {
		t.Fatal("workspace 1's binding was mutated by workspace 2")
	}
	if oatBool(ws2["enforce_sso"], true) {
		t.Fatal("workspace 2's binding should be unenforced")
	}

	// Deleting workspace 2's binding leaves workspace 1's intact.
	rec = h.do(t, http.MethodDelete, "/workspaces/current/sso", "", "2")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete sso ws2 = %d", rec.Code)
	}
	rec = h.do(t, http.MethodGet, "/workspaces/current/sso", "", "1")
	env = oatJSON(t, rec)
	if env["data"] == nil {
		t.Fatal("workspace 1's binding must survive workspace 2's delete")
	}
}

func TestOAuthWorkspace_SSORequiresWorkspaceContext(t *testing.T) {
	h := newOATHarness(t)
	rec := h.do(t, http.MethodPut, "/workspaces/current/sso", `{"provider_id":1}`, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("sso without workspace context = %d, want 400", rec.Code)
	}
}
