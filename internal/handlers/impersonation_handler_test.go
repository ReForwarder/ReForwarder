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

	"github.com/golang-jwt/jwt/v5"
	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The impersonation handler (R-20) is served through the real okapi stack with
// the real admin JWT middleware, because the flow's security properties live in
// exactly those layers: the impersonate endpoint must demand the admin role,
// the exit endpoint must accept the impersonated (non-admin) session, and the
// middleware must turn the token's impersonated_by claim into the context
// marker ExitImpersonation gates on. The database is a fake database/sql driver
// in the established arrangement: it stores users, sessions and events rows and
// evaluates the simple equality WHERE clauses these flows issue, so the
// session-row stamping, revocation and audit events are pinned without a live
// database or Redis (the session.Store is nil — MarkRevoked is skipped, the
// row-level revocation below is what the middleware blacklist rides on in
// production).

const impTestJWTSecret = "impersonation-handler-test-secret"

const impAdminID = 1
const impTargetID = 2

var impTestTime = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

// ---------- the fake database ----------

var impHColumns = map[string][]string{
	"users": {"id", "name", "email", "role", "active", "password_hash",
		"two_factor_enabled", "two_factor_secret", "created_at", "last_login_at"},
	"sessions": {"id", "user_id", "jti", "ip_address", "user_agent", "revoked",
		"created_at", "expires_at", "impersonated_by", "impersonation_reason"},
	"events": {"id", "category", "type", "workspace_id", "actor_id", "actor_name",
		"client_ip", "message", "metadata", "outcome", "target_type", "target_id", "created_at"},
}

var impHDefaults = map[string]map[string]func() driver.Value{
	"users": {
		"name":              func() driver.Value { return "" },
		"role":              func() driver.Value { return "user" },
		"active":            func() driver.Value { return true },
		"password_hash":     func() driver.Value { return "" },
		"two_factor_secret": func() driver.Value { return "" },
		"created_at":        func() driver.Value { return impTestTime },
	},
	"sessions": {
		"ip_address": func() driver.Value { return "" },
		"user_agent": func() driver.Value { return "" },
		"revoked":    func() driver.Value { return false },
		"created_at": func() driver.Value { return impTestTime },
	},
	"events": {
		"actor_name": func() driver.Value { return "" },
		"metadata":   func() driver.Value { return "{}" },
		"created_at": func() driver.Value { return impTestTime },
	},
}

type impHTable struct {
	rows [][]driver.Value
}

type impHState struct {
	mu     sync.Mutex
	tables map[string]*impHTable
	seq    int64
}

func newImpHState() *impHState {
	return &impHState{tables: map[string]*impHTable{
		"users":    {},
		"sessions": {},
		"events":   {},
	}, seq: 100}
}

func (s *impHState) colIndex(table, col string) int {
	for i, c := range impHColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

// rowMaps returns every row of the table as column maps.
func (s *impHState) rowMaps(table string) []map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := impHColumns[table]
	out := make([]map[string]driver.Value, 0, len(s.tables[table].rows))
	for _, row := range s.tables[table].rows {
		m := make(map[string]driver.Value, len(spec))
		for i, c := range spec {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

func driverValEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tbo := a, b
	switch va := a.(type) {
	case int64:
		if vb, ok := b.(int64); ok {
			return va == vb
		}
		return false
	case string:
		vb, ok := b.(string)
		return ok && va == vb
	case bool:
		vb, ok := b.(bool)
		return ok && va == vb
	case time.Time:
		vb, ok := b.(time.Time)
		return ok && va.Equal(vb)
	}
	return ta == tbo
}

func impHVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case int64, bool, string, time.Time:
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
		panic(fmt.Sprintf("impHVal: unsupported type %T", v))
	}
}

// seed inserts a fixture row.
func impHSeed(t *testing.T, s *impHState, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := impHColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := impHDefaults[table][col]; ok {
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
		row[idx] = impHVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

type impHConn struct{ s *impHState }

func (c *impHConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *impHConn) Close() error                        { return nil }
func (c *impHConn) Begin() (driver.Tx, error)           { return impHTx{}, nil }

type impHTx struct{}

func (impHTx) Commit() error   { return nil }
func (impHTx) Rollback() error { return nil }

type impHResult struct{ rows int64 }

func (r impHResult) LastInsertId() (int64, error) { return 0, nil }
func (r impHResult) RowsAffected() (int64, error) { return r.rows, nil }

type impHRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *impHRows) Columns() []string { return r.cols }
func (r *impHRows) Close() error      { return nil }
func (r *impHRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type impHConnector struct{ state *impHState }

func (c impHConnector) Connect(context.Context) (driver.Conn, error) {
	return &impHConn{s: c.state}, nil
}
func (c impHConnector) Driver() driver.Driver { return impHDrv{} }

type impHDrv struct{}

func (impHDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func impHGorm(t *testing.T, state *impHState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(impHConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open impersonation fake gorm: %v", err)
	}
	return gdb
}

func impHArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("impHState: no arg with ordinal %d", n))
}

// impHWhere parses the WHERE clause of a compacted query into (column, value)
// equality pairs. Only the shapes the impersonation flow issues are handled:
// `col = $n` bind conditions; anything else is a test-authoring error.
func impHWhere(compact, query string, args []driver.NamedValue) ([][2]driver.Value, error) {
	i := strings.Index(compact, " where ")
	if i < 0 {
		return nil, nil
	}
	where := compact[i+len(" where "):]
	// Truncate at ORDER BY / LIMIT / FOR — they carry no conditions here.
	for _, cut := range []string{" order by ", " limit ", " for "} {
		if j := strings.Index(where, cut); j >= 0 {
			where = where[:j]
		}
	}
	var out [][2]driver.Value
	for _, cond := range strings.Split(where, " and ") {
		cond = strings.TrimSpace(cond)
		at := strings.Index(cond, " = $")
		if at < 0 {
			return nil, fmt.Errorf("impHState: unsupported condition %q in: %s", cond, query)
		}
		ordinal := 0
		if _, err := fmt.Sscanf(cond[at+4:], "%d", &ordinal); err != nil {
			return nil, fmt.Errorf("impHState: bad bind ordinal in %q", cond)
		}
		col := strings.ReplaceAll(cond[:at], `"`, "")
		col = strings.TrimPrefix(col, "users.")
		col = strings.TrimPrefix(col, "sessions.")
		col = strings.TrimPrefix(col, "events.")
		out = append(out, [2]driver.Value{col, impHArg(args, ordinal)})
	}
	return out, nil
}

func (c *impHConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	compact := strings.Join(strings.Fields(strings.ToLower(query)), " ")

	// gorm runs INSERT ... RETURNING as a query (sessions, audit events):
	// persist the row and hand back the minted id.
	if strings.Contains(compact, "returning") && strings.Contains(compact, "insert into") {
		table := ""
		for name := range c.s.tables {
			if strings.Contains(compact, `insert into "`+name+`" (`) {
				table = name
			}
		}
		if _, err := c.execInsert(compact, args); err != nil {
			return nil, err
		}
		c.s.mu.Lock()
		rows := c.s.tables[table].rows
		last := rows[len(rows)-1]
		spec := impHColumns[table]
		cols := make([]string, len(spec))
		copy(cols, spec)
		c.s.mu.Unlock()
		return &impHRows{cols: cols, rows: [][]driver.Value{last}}, nil
	}
	table := ""
	switch {
	case strings.Contains(compact, `from "users"`):
		table = "users"
	case strings.Contains(compact, `from "sessions"`):
		table = "sessions"
	case strings.Contains(compact, `from "events"`):
		table = "events"
	default:
		return nil, fmt.Errorf("impHState: cannot route query: %s", query)
	}
	where, err := impHWhere(compact, query, args)
	if err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := impHColumns[table]
	var rows [][]driver.Value
	for _, row := range s.tables[table].rows {
		matched := true
		for _, cond := range where {
			idx := -1
			for i, col := range spec {
				if col == cond[0].(string) {
					idx = i
				}
			}
			if idx < 0 || !driverValEqual(row[idx], cond[1]) {
				matched = false
				break
			}
		}
		if matched {
			rows = append(rows, row)
		}
	}
	return &impHRows{cols: spec, rows: rows}, nil
}

// execInsert mints a row from an INSERT ... RETURNING "id". The minted row is
// stored in the table state; the Exec caller only sees the affected-row count.
func (c *impHConn) execInsert(lower string, args []driver.NamedValue) (driver.Result, error) {
	table := ""
	for name := range c.s.tables {
		if strings.Contains(lower, `insert into "`+name+`" (`) {
			table = name
		}
	}
	if table == "" {
		return nil, fmt.Errorf("impHState: unsupported insert: %s", lower)
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := impHColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := impHDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	start := strings.Index(lower, `"`+table+`" (`)
	end := strings.Index(lower[start:], ") values")
	if start < 0 || end < 0 {
		return nil, fmt.Errorf("impHState: cannot parse insert: %s", lower)
	}
	cols := strings.Split(strings.ReplaceAll(strings.TrimSpace(lower[start+len(`"`+table+`" (`):start+end]), `"`, ""), ",")
	for i, col := range cols {
		col = strings.TrimSpace(col)
		idx := -1
		for j, c2 := range spec {
			if c2 == col {
				idx = j
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("impHState: insert names unknown column %q of %s", col, table)
		}
		row[idx] = impHArg(args, i+1)
	}
	s.seq++
	row[s.colIndex(table, "id")] = s.seq
	s.tables[table].rows = append(s.tables[table].rows, row)
	return impHResult{rows: 1}, nil
}

func (c *impHConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if strings.Contains(lower, "returning") {
		return c.execInsert(lower, args)
	}
	table := ""
	switch {
	case strings.Contains(lower, `update "users"`):
		table = "users"
	case strings.Contains(lower, `update "sessions"`):
		table = "sessions"
	default:
		return nil, fmt.Errorf("impHState: unsupported exec: %s", query)
	}
	where, err := impHWhere(lower, query, args)
	if err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := impHColumns[table]

	// Parse the SET clause: `"col"=$n, ...`.
	set := lower[strings.Index(lower, " set ")+len(" set "):]
	set = set[:strings.Index(set, " where ")]
	var setters [][2]driver.Value
	for _, assignment := range strings.Split(set, ",") {
		assignment = strings.TrimSpace(assignment)
		at := strings.Index(assignment, "=$")
		if at < 0 {
			return nil, fmt.Errorf("impHState: unsupported set %q in: %s", assignment, query)
		}
		ordinal := 0
		if _, err := fmt.Sscanf(assignment[at+2:], "%d", &ordinal); err != nil {
			return nil, fmt.Errorf("impHState: bad set bind in %q", assignment)
		}
		col := strings.ReplaceAll(assignment[:at], `"`, "")
		setters = append(setters, [2]driver.Value{col, impHArg(args, ordinal)})
	}

	matched := int64(0)
	for _, row := range s.tables[table].rows {
		ok := true
		for _, cond := range where {
			idx := -1
			for i, col := range spec {
				if col == cond[0].(string) {
					idx = i
				}
			}
			if idx < 0 || !driverValEqual(row[idx], cond[1]) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		matched++
		for _, st := range setters {
			idx := -1
			for i, col := range spec {
				if col == st[0].(string) {
					idx = i
				}
			}
			if idx < 0 {
				return nil, fmt.Errorf("impHState: update names unknown column %q of %s", st[0], table)
			}
			row[idx] = st[1]
		}
	}
	return impHResult{rows: matched}, nil
}

// ---------- fixtures + harness ----------

func impSeedUser(t *testing.T, s *impHState, id uint, role string, active bool) {
	t.Helper()
	impHSeed(t, s, "users", map[string]any{
		"id": id, "name": fmt.Sprintf("User %d", id),
		"email":         fmt.Sprintf("user%d@example.com", id),
		"role":          role,
		"active":        active,
		"password_hash": "x", "created_at": impTestTime,
	})
}

const (
	impRoleAdmin = "admin"
	impRoleUser  = "user"
)

func impMintJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	if _, ok := claims["aud"]; !ok {
		claims["aud"] = "posta"
	}
	tok, err := okapi.GenerateJwtToken([]byte(impTestJWTSecret), claims, time.Hour)
	if err != nil {
		t.Fatalf("GenerateJwtToken: %v", err)
	}
	return tok
}

func impAdminJWT(t *testing.T) string {
	t.Helper()
	return impMintJWT(t, map[string]any{
		"sub": impAdminID, "email": "user1@example.com", "role": impRoleAdmin, "jti": "jti-admin-1",
	})
}

type impHarness struct {
	state *impHState
	o     *okapi.TestServer
}

func newImpHarness(t *testing.T) *impHarness {
	t.Helper()
	state := newImpHState()
	gdb := impHGorm(t, state)

	userRepo := repositories.NewUserRepository(gdb)
	sessionRepo := repositories.NewSessionRepository(gdb)

	users := NewUserHandler(userRepo, impTestJWTSecret, nil, nil)
	users.SetSessionRepo(sessionRepo)

	// Audit events land in the fake events table, so the tests can assert the
	// started/stopped audit trail directly.
	bus := eventbus.New(repositories.NewEventRepository(gdb))
	auditLogger := audit.NewLogger(bus)

	h := NewImpersonationHandler(userRepo, sessionRepo, nil, users, auditLogger)

	o := okapi.NewTestServer(t)
	adminAuth := middlewares.JWTAdminAuth(&config.Config{JWTSecret: impTestJWTSecret})
	admin := o.Group("/admin", adminAuth.Middleware)
	admin.Post("/users/{id:int}/impersonate", okapi.H(h.Impersonate))
	// The exit route is reachable by the impersonated (non-admin) session, so it
	// rides plain JWT auth — the production route table's arrangement.
	jwtAuth := middlewares.JWTAuth(&config.Config{JWTSecret: impTestJWTSecret})
	exit := o.Group("/admin/impersonate", jwtAuth.Middleware)
	exit.Post("/exit", h.ExitImpersonation)

	return &impHarness{state: state, o: o}
}

func (h *impHarness) do(t *testing.T, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.o.ServeHTTP(rec, req)
	return rec
}

func impDecode(t *testing.T, rec *httptest.ResponseRecorder) (bool, map[string]any) {
	t.Helper()
	var resp struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.Error != nil {
		return resp.Success, map[string]any{"_error": resp.Error.Message}
	}
	return resp.Success, resp.Data
}

// impStartImpersonation drives the happy-path start and returns the new token.
func (h *impHarness) impStartImpersonation(t *testing.T, adminToken string, targetID int, reason string) string {
	t.Helper()
	rec := h.do(t, fmt.Sprintf("/admin/users/%d/impersonate", targetID), adminToken,
		fmt.Sprintf(`{"reason":%q}`, reason))
	if rec.Code != http.StatusCreated {
		t.Fatalf("impersonate status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	_, data := impDecode(t, rec)
	tok, _ := data["token"].(string)
	if tok == "" {
		t.Fatalf("impersonate response carries no token: %v", data)
	}
	return tok
}

func impSessionRows(s *impHState) []map[string]driver.Value {
	return s.rowMaps("sessions")
}

// ---------- impersonate ----------

func TestImpersonateIssuesMarkedSession(t *testing.T) {
	h := newImpHarness(t)
	impSeedUser(t, h.state, impAdminID, impRoleAdmin, true)
	impSeedUser(t, h.state, impTargetID, impRoleUser, true)

	adminToken := impAdminJWT(t)
	reason := "Reproducing the reported forwarding loop"
	rec := h.do(t, fmt.Sprintf("/admin/users/%d/impersonate", impTargetID), adminToken,
		fmt.Sprintf(`{"reason":%q}`, reason))
	if rec.Code != http.StatusCreated {
		t.Fatalf("impersonate status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	_, data := impDecode(t, rec)
	tok, _ := data["token"].(string)
	if tok == "" {
		t.Fatalf("impersonate response carries no token: %v", data)
	}

	// The minted JWT carries the impersonation claims alongside the target's identity.
	parsed, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return []byte(impTestJWTSecret), nil })
	if err != nil {
		t.Fatalf("parse impersonation token: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if got := uint(claims["sub"].(float64)); got != impTargetID {
		t.Fatalf("token sub = %d, want target %d", got, impTargetID)
	}
	if got := uint(claims["impersonated_by"].(float64)); got != impAdminID {
		t.Fatalf("token impersonated_by = %d, want admin %d", got, impAdminID)
	}
	if claims["impersonation_reason"] != reason {
		t.Fatalf("token impersonation_reason = %v, want %q", claims["impersonation_reason"], reason)
	}

	// The browser cookie is replaced with the impersonation session: the very
	// token the response body issued.
	cookies := rec.Result().Cookies()
	found := false
	for _, ck := range cookies {
		if ck.Name == middlewares.SessionCookieName && ck.Value == tok {
			found = true
		}
	}
	if !found {
		t.Fatalf("impersonate did not set the %s cookie to the new token (cookies %+v)", middlewares.SessionCookieName, cookies)
	}

	// The session row is stamped with the admin and the reason.
	rows := impSessionRows(h.state)
	if len(rows) != 1 {
		t.Fatalf("sessions rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row["user_id"].(int64) != impTargetID {
		t.Fatalf("session user_id = %v, want target", row["user_id"])
	}
	if row["impersonated_by"] == nil || row["impersonated_by"].(int64) != impAdminID {
		t.Fatalf("session impersonated_by = %v, want admin %d", row["impersonated_by"], impAdminID)
	}
	if row["impersonation_reason"] != reason {
		t.Fatalf("session impersonation_reason = %v, want %q", row["impersonation_reason"], reason)
	}

	// The start is audited with both parties and the reason.
	events := h.state.rowMaps("events")
	if len(events) != 1 {
		t.Fatalf("events rows = %d, want 1", len(events))
	}
	if events[0]["type"] != "admin.impersonation.started" {
		t.Fatalf("audit event type = %v, want admin.impersonation.started", events[0]["type"])
	}
	if events[0]["actor_id"].(int64) != impAdminID {
		t.Fatalf("audit actor_id = %v, want admin", events[0]["actor_id"])
	}
	meta := events[0]["metadata"].(string)
	for _, want := range []string{`"admin_id":1`, `"target_id":2`, "Reproducing the reported forwarding loop"} {
		if !strings.Contains(meta, want) {
			t.Fatalf("audit metadata %q missing %q", meta, want)
		}
	}
}

func TestImpersonateResponseCarriesTargetProfile(t *testing.T) {
	h := newImpHarness(t)
	impSeedUser(t, h.state, impAdminID, impRoleAdmin, true)
	impSeedUser(t, h.state, impTargetID, impRoleUser, true)

	rec := h.do(t, fmt.Sprintf("/admin/users/%d/impersonate", impTargetID),
		impAdminJWT(t), `{"reason":"Checking the user's mailbox settings"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	success, data := impDecode(t, rec)
	if !success {
		t.Fatalf("success = false: %v", data)
	}
	if data["impersonated"] != true {
		t.Fatalf("impersonated = %v, want true", data["impersonated"])
	}
	if data["original_admin"] != "user1@example.com" {
		t.Fatalf("original_admin = %v, want the admin's email", data["original_admin"])
	}
	target, _ := data["target"].(map[string]any)
	if target == nil {
		t.Fatalf("response carries no target: %v", data)
	}
	if target["email"] != "user2@example.com" || target["role"] != impRoleUser {
		t.Fatalf("target profile wrong: %v", target)
	}
}

func TestImpersonateGuards(t *testing.T) {
	h := newImpHarness(t)
	impSeedUser(t, h.state, impAdminID, impRoleAdmin, true)
	impSeedUser(t, h.state, impTargetID, impRoleUser, true)
	// A second admin and a disabled user as impersonation targets.
	impSeedUser(t, h.state, 3, impRoleAdmin, true)
	impSeedUser(t, h.state, 4, impRoleUser, false)

	adminToken := impAdminJWT(t)
	reason := "A reason of sufficient length"

	cases := []struct {
		name     string
		path     string
		body     string
		wantCode int
		wantSub  string
	}{
		{
			name:     "self-impersonation refused",
			path:     fmt.Sprintf("/admin/users/%d/impersonate", impAdminID),
			body:     fmt.Sprintf(`{"reason":%q}`, reason),
			wantCode: http.StatusBadRequest,
			wantSub:  "already signed in",
		},
		{
			name:     "admin targets refused",
			path:     "/admin/users/3/impersonate",
			body:     fmt.Sprintf(`{"reason":%q}`, reason),
			wantCode: http.StatusForbidden,
			wantSub:  "cannot impersonate other administrators",
		},
		{
			name:     "disabled accounts refused",
			path:     "/admin/users/4/impersonate",
			body:     fmt.Sprintf(`{"reason":%q}`, reason),
			wantCode: http.StatusBadRequest,
			wantSub:  "disabled",
		},
		{
			name:     "missing reason refused",
			path:     fmt.Sprintf("/admin/users/%d/impersonate", impTargetID),
			body:     `{}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "short reason refused",
			path:     fmt.Sprintf("/admin/users/%d/impersonate", impTargetID),
			body:     `{"reason":"too short"}`,
			wantCode: http.StatusBadRequest,
			wantSub:  "10 characters",
		},
		{
			name:     "unknown target refused",
			path:     "/admin/users/999/impersonate",
			body:     fmt.Sprintf(`{"reason":%q}`, reason),
			wantCode: http.StatusNotFound,
		},
		{
			name:     "non-admin caller refused by the middleware",
			path:     fmt.Sprintf("/admin/users/%d/impersonate", impTargetID),
			body:     fmt.Sprintf(`{"reason":%q}`, reason),
			wantCode: http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := adminToken
			if tc.name == "non-admin caller refused by the middleware" {
				token = impMintJWT(t, map[string]any{
					"sub": impTargetID, "email": "user2@example.com", "role": impRoleUser, "jti": "jti-user-1",
				})
			}
			rec := h.do(t, tc.path, token, tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantSub != "" && !strings.Contains(rec.Body.String(), tc.wantSub) {
				t.Fatalf("body %q missing %q", rec.Body.String(), tc.wantSub)
			}
		})
	}
	// No session and no audit event may exist after the refusals.
	if n := len(impSessionRows(h.state)); n != 0 {
		t.Fatalf("refused impersonations created %d session rows, want 0", n)
	}
	if n := len(h.state.rowMaps("events")); n != 0 {
		t.Fatalf("refused impersonations produced %d audit events, want 0", n)
	}
}

// ---------- exit ----------

func TestExitImpersonationRevokesAndAudits(t *testing.T) {
	h := newImpHarness(t)
	impSeedUser(t, h.state, impAdminID, impRoleAdmin, true)
	impSeedUser(t, h.state, impTargetID, impRoleUser, true)

	adminToken := impAdminJWT(t)
	tok := h.impStartImpersonation(t, adminToken, impTargetID, "Reproducing the reported forwarding loop")
	if n := len(impSessionRows(h.state)); n != 1 {
		t.Fatalf("setup created %d session rows, want 1", n)
	}

	rec := h.do(t, "/admin/impersonate/exit", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("exit status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	success, data := impDecode(t, rec)
	if !success {
		t.Fatalf("exit success = false: %v", data)
	}
	if id, _ := data["original_admin_id"].(float64); uint(id) != impAdminID {
		t.Fatalf("original_admin_id = %v, want %d", data["original_admin_id"], impAdminID)
	}

	// The impersonation session row is revoked; the admin's own session is absent
	// (the admin's browser cookie was replaced, not their session revoked).
	rows := impSessionRows(h.state)
	if len(rows) != 1 {
		t.Fatalf("session rows after exit = %d, want 1", len(rows))
	}
	if rows[0]["revoked"] != true {
		t.Fatalf("impersonation session not revoked: %v", rows[0])
	}

	// The cookie is expired in place — the same exit response that revokes the
	// session also clears the browser cookie (a repeated exit would audit a
	// second stop event, so it is not exercised here).
	cleared := false
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == middlewares.SessionCookieName && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("exit did not clear the %s cookie (cookies %+v)", middlewares.SessionCookieName, rec.Result().Cookies())
	}

	// A replayed exit with the same token — reachable only when the first
	// exit's revocation itself failed — must not mint a second stop event.
	replay := h.do(t, "/admin/impersonate/exit", tok, "")
	if replay.Code != http.StatusOK && replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed exit status = %d, want 200 or 401 (body %s)", replay.Code, replay.Body.String())
	}
	if n := len(h.state.rowMaps("events")); n != 2 {
		t.Fatalf("events after replayed exit = %d, want 2 (no duplicate stop event)", n)
	}

	// The stop is audited naming the admin as the actor and the target as subject.
	events := h.state.rowMaps("events")
	if len(events) != 2 {
		t.Fatalf("events rows = %d, want 2 (start + stop)", len(events))
	}
	stop := events[1]
	if stop["type"] != "admin.impersonation.stopped" {
		t.Fatalf("audit event type = %v, want admin.impersonation.stopped", stop["type"])
	}
	if stop["actor_id"].(int64) != impAdminID {
		t.Fatalf("stop audit actor = %v, want the admin", stop["actor_id"])
	}
	if stop["actor_name"] != "user1@example.com" {
		t.Fatalf("stop audit actor_name = %v, want the admin's email", stop["actor_name"])
	}
	if !strings.Contains(stop["metadata"].(string), `"target_id":2`) {
		t.Fatalf("stop audit metadata missing target: %v", stop["metadata"])
	}
}

func TestExitImpersonationRefusesNonImpersonatedSession(t *testing.T) {
	h := newImpHarness(t)
	impSeedUser(t, h.state, impAdminID, impRoleAdmin, true)

	// An ordinary admin session carries no impersonated_by claim.
	adminToken := impAdminJWT(t)
	rec := h.do(t, "/admin/impersonate/exit", adminToken, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("exit status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not an impersonation session") {
		t.Fatalf("body %q missing the refusal reason", rec.Body.String())
	}
	if n := len(h.state.rowMaps("events")); n != 0 {
		t.Fatalf("refused exit produced %d audit events, want 0", n)
	}
}
