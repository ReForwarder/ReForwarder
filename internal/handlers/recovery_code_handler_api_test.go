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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// twofactorSecretFor mints a live TOTP secret the tests can compute codes for.
func twofactorSecretFor(email string) (string, string, error) {
	return twofactorSecret(email)
}

func twofactorSecret(email string) (string, string, error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Posta", AccountName: email})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

func bcryptHashFor(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// R-10 handler-level tests, on top of the store tests in
// recovery_code_handler_test.go: the GenerateRecoveryCodes endpoint's TOTP
// gate, and the login path that accepts a single-use recovery code in place
// of the TOTP code (and burns it exactly once). The fake database serves the
// users, user_recovery_codes and events queries the flow issues.

const rcApiTestSecret = "rc-api-handler-test-secret"

var rcApiUserColumns = []string{"id", "name", "email", "role", "active", "password_hash",
	"two_factor_enabled", "two_factor_secret", "created_at", "last_login_at"}

type rcApiRow struct {
	id        int64
	userID    int64
	codeHash  string
	usedAt    *time.Time
	createdAt time.Time
}

type rcApiState struct {
	mu     sync.Mutex
	seq    int64
	users  map[int64][]driver.Value
	codes  []*rcApiRow
	events []string
}

func newRcApiState() *rcApiState { return &rcApiState{seq: 200, users: map[int64][]driver.Value{}} }

func (s *rcApiState) seedUser(id int64, email, passwordHash, totpSecret string, twoFA bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := make([]driver.Value, len(rcApiUserColumns))
	for i, col := range rcApiUserColumns {
		switch col {
		case "id":
			row[i] = id
		case "email":
			row[i] = email
		case "role":
			row[i] = "user"
		case "active":
			row[i] = true
		case "password_hash":
			row[i] = passwordHash
		case "two_factor_enabled":
			row[i] = twoFA
		case "two_factor_secret":
			row[i] = totpSecret
		case "created_at":
			row[i] = time.Now().UTC()
		default:
			row[i] = nil
		}
	}
	s.users[id] = row
}

func (s *rcApiState) unusedCodes(userID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.codes {
		if r.userID == userID && r.usedAt == nil {
			n++
		}
	}
	return n
}

func rcApiGorm(t *testing.T, state *rcApiState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(rcApiConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open rc api fake gorm: %v", err)
	}
	return gdb
}

type rcApiConnector struct{ state *rcApiState }

func (c rcApiConnector) Connect(context.Context) (driver.Conn, error) {
	return &rcApiConn{s: c.state}, nil
}
func (c rcApiConnector) Driver() driver.Driver { return rcApiDrv{} }

type rcApiDrv struct{}

func (rcApiDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type rcApiConn struct{ s *rcApiState }

func (c *rcApiConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *rcApiConn) Close() error                        { return nil }
func (c *rcApiConn) Begin() (driver.Tx, error)           { return rcApiTx{}, nil }

type rcApiTx struct{}

func (rcApiTx) Commit() error   { return nil }
func (rcApiTx) Rollback() error { return nil }

type rcApiResult struct{ rows int64 }

func (r rcApiResult) LastInsertId() (int64, error) { return 0, nil }
func (r rcApiResult) RowsAffected() (int64, error) { return r.rows, nil }

type rcApiRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *rcApiRows) Columns() []string { return r.cols }
func (r *rcApiRows) Close() error      { return nil }
func (r *rcApiRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func rcApiArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("rcApiState: no arg with ordinal %d", n))
}

func rcApiColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func (c *rcApiConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	switch {
	case strings.Contains(lower, `insert into "user_recovery_codes"`):
		return c.insertCode(lower, args)
	case strings.Contains(lower, `insert into "events"`):
		return c.insertEvent(lower, query, args)
	case strings.Contains(lower, `from "users"`):
		return c.selectUser(lower, args)
	case strings.Contains(lower, `from "user_recovery_codes"`):
		if strings.Contains(lower, "count(") {
			userID := rcApiArg(args, 1).(int64)
			return &rcApiRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(s.unusedCodes(userID))}}}, nil
		}
		userID := rcApiArg(args, 1).(int64)
		s.mu.Lock()
		defer s.mu.Unlock()
		var rows [][]driver.Value
		for _, r := range s.codes {
			if r.userID == userID && r.usedAt == nil {
				rows = append(rows, []driver.Value{r.id, r.userID, r.codeHash, r.usedAt, r.createdAt})
			}
		}
		return &rcApiRows{cols: []string{"id", "user_id", "code_hash", "used_at", "created_at"}, rows: rows}, nil
	}
	return nil, fmt.Errorf("rcApiState: cannot route query: %s", query)
}

func (c *rcApiConn) insertCode(lower string, args []driver.NamedValue) (driver.Rows, error) {
	start := strings.Index(lower, `"user_recovery_codes" (`)
	end := strings.Index(lower[start:], ") values")
	if start < 0 || end < 0 {
		return nil, fmt.Errorf("rcApiState: cannot parse code insert: %s", lower)
	}
	cols := strings.Split(strings.ReplaceAll(strings.TrimSpace(lower[start+len(`"user_recovery_codes" (`):start+end]), `"`, ""), ",")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	row := &rcApiRow{createdAt: time.Now()}
	for i, col := range cols {
		col = strings.TrimSpace(col)
		switch col {
		case "user_id":
			row.userID = rcApiArg(args, i+1).(int64)
		case "code_hash":
			row.codeHash = rcApiArg(args, i+1).(string)
		}
	}
	s.seq++
	row.id = s.seq
	s.codes = append(s.codes, row)
	return &rcApiRows{cols: []string{"id"}, rows: [][]driver.Value{{row.id}}}, nil
}

func (c *rcApiConn) insertEvent(lower, query string, args []driver.NamedValue) (driver.Rows, error) {
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("rcApiState: event insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	colParts := strings.Split(query[open+1:close], ",")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	eventType := ""
	for i, col := range colParts {
		col = strings.Trim(strings.TrimSpace(col), `"`)
		if col == "type" {
			eventType = fmt.Sprint(rcApiArg(args, i+1))
		}
	}
	s.events = append(s.events, eventType)
	return &rcApiRows{cols: []string{"id"}, rows: [][]driver.Value{{int64(len(s.events))}}}, nil
}

func (c *rcApiConn) selectUser(lower string, args []driver.NamedValue) (driver.Rows, error) {
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = lower[i+len(" where "):]
		for _, cut := range []string{" order by ", " limit "} {
			if j := strings.Index(where, cut); j >= 0 {
				where = where[:j]
			}
		}
	}
	eq := strings.Index(where, " = ")
	if eq < 0 {
		return nil, fmt.Errorf("rcApiState: unsupported user lookup %q", where)
	}
	col := rcApiColumnOf(where[:eq])
	want := rcApiArg(args, whereOrdinal(where[eq+3:]))
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.users {
		idx := -1
		for i, name := range rcApiUserColumns {
			if name == col {
				idx = i
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("rcApiState: unknown user column %q", col)
		}
		if fmt.Sprint(row[idx]) == fmt.Sprint(want) {
			return &rcApiRows{cols: rcApiUserColumns, rows: [][]driver.Value{append([]driver.Value(nil), row...)}}, nil
		}
	}
	return &rcApiRows{cols: rcApiUserColumns}, nil
}

func whereOrdinal(tok string) int {
	tok = strings.TrimSpace(tok)
	var n int
	if _, err := fmt.Sscanf(tok[1:], "%d", &n); err != nil {
		panic(fmt.Sprintf("rcApiState: bad bindvar %q", tok))
	}
	return n
}

func (c *rcApiConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.Contains(lower, `update "user_recovery_codes"`):
		// The single-use burn: UPDATE ... SET used_at=$1 WHERE id=$2 AND used_at IS NULL.
		whereStart := strings.Index(lower, " where ")
		where := lower[whereStart+len(" where "):]
		var id int64
		hasNullGuard := strings.Contains(strings.NewReplacer("(", " ", ")", " ").Replace(where), "used_at is null")
		for _, cond := range strings.Split(strings.NewReplacer("(", " ", ")", " ").Replace(where), " and ") {
			cond = strings.TrimSpace(cond)
			if eq := strings.Index(cond, " = "); eq >= 0 && rcApiColumnOf(cond[:eq]) == "id" {
				id = rcApiArg(args, whereOrdinal(cond[eq+3:])).(int64)
			}
		}
		for _, r := range s.codes {
			if r.id == id && r.usedAt == nil {
				if !hasNullGuard {
					return nil, fmt.Errorf("rcApiState: burn without the used_at IS NULL guard: %s", lower)
				}
				now := time.Now()
				r.usedAt = &now
				return rcApiResult{rows: 1}, nil
			}
		}
		return rcApiResult{rows: 0}, nil
	case strings.Contains(lower, `delete from "user_recovery_codes"`):
		kept := s.codes[:0]
		removed := int64(0)
		for _, r := range s.codes {
			if r.userID == rcApiArg(args, 1).(int64) && r.usedAt == nil {
				removed++
				continue
			}
			kept = append(kept, r)
		}
		s.codes = kept
		return rcApiResult{rows: removed}, nil
	case strings.Contains(lower, `update "users"`):
		whereStart := strings.Index(lower, " where ")
		where := lower[whereStart+len(" where "):]
		var id int64
		for _, cond := range strings.Split(strings.NewReplacer("(", " ", ")", " ").Replace(where), " and ") {
			cond = strings.TrimSpace(cond)
			if eq := strings.Index(cond, " = "); eq >= 0 && rcApiColumnOf(cond[:eq]) == "id" {
				id = rcApiArg(args, whereOrdinal(cond[eq+3:])).(int64)
			}
		}
		row, ok := s.users[id]
		if !ok {
			return rcApiResult{rows: 0}, nil
		}
		setStart := strings.Index(lower, " set ")
		setClause := lower[setStart+len(" set ") : whereStart]
		for _, pair := range strings.Split(setClause, ",") {
			eq := strings.Index(pair, "=")
			col := rcApiColumnOf(pair[:eq])
			val := rcApiArg(args, whereOrdinal(pair[eq+1:]))
			for i, name := range rcApiUserColumns {
				if name == col {
					row[i] = val
				}
			}
		}
		return rcApiResult{rows: 1}, nil
	}
	return nil, fmt.Errorf("rcApiState: unsupported exec: %s", query)
}

// ---------- harness ----------

type rcApiHarness struct {
	state *rcApiState
	o     *okapi.TestServer
}

func newRcApiHarness(t *testing.T) *rcApiHarness {
	t.Helper()
	state := newRcApiState()
	gdb := rcApiGorm(t, state)
	users := NewUserHandler(repositories.NewUserRepository(gdb), rcApiTestSecret, nil, nil)
	// SetMigrator wires the recovery-code store; a nil migrator skips the
	// personal-workspace provisioning the login flow would otherwise run.
	users.SetMigrator(gdb, nil)
	users.SetAuditLogger(audit.NewLogger(eventbus.New(repositories.NewEventRepository(gdb))))

	o := okapi.NewTestServer(t)
	jwtAuth := middlewares.JWTAuth(&config.Config{JWTSecret: rcApiTestSecret})
	authed := o.Group("/users/me", jwtAuth.Middleware)
	authed.Post("/2fa/recovery-codes", okapi.H(users.GenerateRecoveryCodes))
	public := o.Group("/auth")
	public.Post("/login", okapi.H(users.Login))
	return &rcApiHarness{state: state, o: o}
}

func (h *rcApiHarness) do(t *testing.T, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(http.MethodPost, path, rdr)
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

func rcApiToken(t *testing.T, userID int64, email string) string {
	t.Helper()
	tok, err := okapi.GenerateJwtToken([]byte(rcApiTestSecret), map[string]any{
		"sub": userID, "email": email, "role": "user", "aud": "posta", "jti": "jti-rc-1",
	}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateJwtToken: %v", err)
	}
	return tok
}

// ---------- tests ----------

// GenerateRecoveryCodes is TOTP-gated: a wrong code, and a user without 2FA,
// are refused without touching the stored codes; the right code issues a fresh
// ten-code set exactly once and audits it.
func TestGenerateRecoveryCodesTOTPGate(t *testing.T) {
	h := newRcApiHarness(t)
	secret, _, err := twofactorSecretFor("alice@example.dev")
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	h.state.seedUser(7, "alice@example.dev", "hash", secret, true)
	token := rcApiToken(t, 7, "alice@example.dev")

	// 2FA not enabled: refused even with a syntactically valid code.
	h.state.seedUser(8, "bob@example.dev", "hash", "", false)
	if rec := h.do(t, "/users/me/2fa/recovery-codes", rcApiToken(t, 8, "bob@example.dev"), `{"code":"000000"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("generate without 2FA = %d, want 400", rec.Code)
	}

	// Invalid code: refused, nothing stored.
	if rec := h.do(t, "/users/me/2fa/recovery-codes", token, `{"code":"000000"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("generate with wrong TOTP = %d, want 400", rec.Code)
	}
	if got := h.state.unusedCodes(7); got != 0 {
		t.Fatalf("wrong TOTP stored %d codes, want 0", got)
	}

	// Current TOTP code: ten single-use codes come back once.
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP: %v", err)
	}
	rec := h.do(t, "/users/me/2fa/recovery-codes", token, fmt.Sprintf(`{"code":%q}`, code))
	if rec.Code != http.StatusOK {
		t.Fatalf("generate with valid TOTP = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Data struct {
			Codes []string `json:"codes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Data.Codes) != 10 {
		t.Fatalf("generate returned %d codes, want 10", len(res.Data.Codes))
	}
	if got := h.state.unusedCodes(7); got != 10 {
		t.Fatalf("store holds %d unused codes, want 10", got)
	}
	if len(h.state.events) != 1 || h.state.events[0] != "user.recovery_codes.generated" {
		t.Fatalf("audit events = %v, want one user.recovery_codes.generated", h.state.events)
	}
}

// The login flow accepts a recovery code in place of the TOTP code, burns it
// (single-use), audits the fallback, and a second use of the same code fails.
func TestLoginRecoveryCodeFallback(t *testing.T) {
	h := newRcApiHarness(t)
	secret, _, err := twofactorSecretFor("alice@example.dev")
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	hash, err := bcryptHashFor("passw0rd-secret")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	h.state.seedUser(7, "alice@example.dev", hash, secret, true)

	// Seed codes through the same store the handler uses.
	store := NewRecoveryCodeStore(rcApiGorm(t, h.state))
	codes, err := store.Generate(7)
	if err != nil {
		t.Fatalf("seed codes: %v", err)
	}
	if len(codes) == 0 {
		t.Fatal("no seeded codes")
	}

	body := fmt.Sprintf(`{"email":"alice@example.dev","password":"passw0rd-secret","two_factor_code":%q}`, codes[0])
	rec := h.do(t, "/auth/login", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery-code login = %d: %s", rec.Code, rec.Body.String())
	}
	if got := h.state.unusedCodes(7); got != 9 {
		t.Fatalf("unused codes after login = %d, want 9 (the used one is burned)", got)
	}
	found := false
	for _, ev := range h.state.events {
		if ev == "user.recovery_code_login" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit events = %v, want user.recovery_code_login", h.state.events)
	}

	// The burned code can never log in again.
	rec = h.do(t, "/auth/login", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("second use of a burned code = %d, want 401", rec.Code)
	}

	// An unused code still works (and burns).
	body2 := fmt.Sprintf(`{"email":"alice@example.dev","password":"passw0rd-secret","two_factor_code":%q}`, codes[1])
	if rec := h.do(t, "/auth/login", "", body2); rec.Code != http.StatusOK {
		t.Fatalf("second unused code login = %d: %s", rec.Code, rec.Body.String())
	}
	if got := h.state.unusedCodes(7); got != 8 {
		t.Fatalf("unused codes after second login = %d, want 8", got)
	}
}

// A wrong TOTP code AND no recovery code match: the login is refused.
func TestLoginRejectsBadTwoFactorCode(t *testing.T) {
	h := newRcApiHarness(t)
	secret, _, err := twofactorSecretFor("alice@example.dev")
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	hash, err := bcryptHashFor("passw0rd-secret")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	h.state.seedUser(7, "alice@example.dev", hash, secret, true)

	rec := h.do(t, "/auth/login", "",
		`{"email":"alice@example.dev","password":"passw0rd-secret","two_factor_code":"000000"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad 2FA login = %d, want 401", rec.Code)
	}
	if got := h.state.unusedCodes(7); got != 0 {
		t.Fatalf("failed login burned %d codes, want 0", got)
	}
}
