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
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/services/auth"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/services/session"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"github.com/pquerna/otp/totp"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// GAP-12 (two_factor_required enforcement) and CA-03 (refresh-token rotation)
// handler tests, on the real okapi stack. The database is a fake database/sql
// driver serving the users / refresh_tokens / user_app_passwords / settings /
// sessions statements the flows issue — the established harness arrangement in
// this package. App-password endpoint tests live in app_password_test.go on
// the same harness.

const leTestSecret = "login-enforcement-test-secret"

const (
	lePlainUserID   = 11
	leAdminID       = 12
	leTwoFAUserID   = 13
	lePlainUserMail = "plain@example.com"
	leAdminMail     = "admin@example.com"
	leTwoFAMail     = "with2fa@example.com"
	leUserPassword  = "correct-horse-battery"
)

var leUserColumns = []string{
	"id", "name", "email", "password_hash", "role", "active",
	"two_factor_enabled", "two_factor_secret",
	"require_verified_domain", "auth_method", "avatar_url",
	"plan_id", "scheduled_deletion_at", "email_verified_at",
	"created_at", "last_login_at", "default_workspace_id",
	"migrated_at", "migration_error",
}

type leRefreshRow struct {
	id         int64
	userID     int64
	familyID   int64
	tokenHash  string
	rotatedAt  *time.Time
	revoked    bool
	revokedAt  *time.Time
	expiresAt  time.Time
	rotatedFrom int64
}

type leAppRow struct {
	id       int64
	userID   int64
	name     string
	pwHash   string
	revoked  bool
}

type leState struct {
	mu        sync.Mutex
	seq       int64
	users     map[int64][]driver.Value
	settings  map[string]string
	refreshes []*leRefreshRow
	appPws    []*leAppRow
}

func newLEState() *leState {
	return &leState{seq: 500, users: map[int64][]driver.Value{}, settings: map[string]string{}}
}

func (s *leState) seedUser(id int64, email, role string, twoFA bool, totpSecret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := bcrypt.GenerateFromPassword([]byte(leUserPassword), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	row := make([]driver.Value, len(leUserColumns))
	for i, col := range leUserColumns {
		switch col {
		case "id":
			row[i] = id
		case "email":
			row[i] = email
		case "role":
			row[i] = role
		case "active":
			row[i] = true
		case "password_hash":
			row[i] = string(hash)
		case "two_factor_enabled":
			row[i] = twoFA
		case "two_factor_secret":
			row[i] = totpSecret
		case "created_at":
			row[i] = time.Now().UTC()
		case "auth_method":
			row[i] = "password"
		default:
			row[i] = nil
		}
	}
	s.users[id] = row
}

func (s *leState) setSetting(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[key] = value
}

func (s *leState) liveRefreshes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.refreshes {
		if !r.revoked && time.Now().Before(r.expiresAt) {
			n++
		}
	}
	return n
}

// ---------- fake driver plumbing ----------

func leGorm(t *testing.T, state *leState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(leConnector{state: state}),
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open le fake gorm: %v", err)
	}
	return gdb
}

type leConnector struct{ state *leState }

func (c leConnector) Connect(context.Context) (driver.Conn, error) { return &leConn{s: c.state}, nil }
func (c leConnector) Driver() driver.Driver                        { return leDrv{} }

type leDrv struct{}

func (leDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type leConn struct{ s *leState }

func (c *leConn) Prepare(string) (driver.Stmt, error)     { return nil, errors.New("no prepare") }
func (c *leConn) Close() error                            { return nil }
func (c *leConn) Begin() (driver.Tx, error)               { return leTx{}, nil }

type leTx struct{}

func (leTx) Commit() error   { return nil }
func (leTx) Rollback() error { return nil }

type leResult struct{ rows int64 }

func (r leResult) LastInsertId() (int64, error) { return 0, nil }
func (r leResult) RowsAffected() (int64, error) { return r.rows, nil }

type leRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *leRows) Columns() []string { return r.cols }
func (r *leRows) Close() error      { return nil }
func (r *leRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func leArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("leState: no arg with ordinal %d", n))
}

func leColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func leOrdinal(tok string) int {
	tok = strings.TrimSpace(tok)
	var n int
	if _, err := fmt.Sscanf(tok[1:], "%d", &n); err != nil {
		panic(fmt.Sprintf("leState: bad bindvar %q", tok))
	}
	return n
}

// leInt64 / leStr coerce bound values without assuming gorm's exact Go type.
func leInt64(v driver.Value) int64 {
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

func leStr(v driver.Value) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// leConditions parses a WHERE clause into col → ordinal plus null guards.
func leConditions(where string) (map[string]int, map[string]bool) {
	conds := map[string]int{}
	nulls := map[string]bool{}
	where = strings.NewReplacer("(", " ", ")", " ").Replace(where)
	for _, part := range strings.Split(where, " and ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasSuffix(part, " is null") {
			nulls[leColumnOf(strings.TrimSuffix(part, " is null"))] = true
			continue
		}
		eq := strings.Index(part, " = ")
		if eq < 0 {
			continue
		}
		conds[leColumnOf(part[:eq])] = leOrdinal(part[eq+3:])
	}
	return conds, nulls
}

func leWhere(query string) string {
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

func (c *leConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	// gorm's postgres driver issues INSERT ... RETURNING "id" as a query.
	if strings.Contains(lower, "insert into") {
		switch {
		case strings.Contains(lower, `insert into "refresh_tokens"`):
			return c.insertRefresh(lower, query, args)
		case strings.Contains(lower, `insert into "user_app_passwords"`):
			return c.insertAppPassword(lower, query, args)
		default:
			return &leRows{cols: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
		}
	}
	switch {
	case strings.Contains(lower, `from "settings"`):
		s.mu.Lock()
		defer s.mu.Unlock()
		keys := make([]string, 0, len(s.settings))
		for k := range s.settings {
			keys = append(keys, k)
		}
		var rows [][]driver.Value
		for _, k := range keys {
			rows = append(rows, []driver.Value{k, s.settings[k], "bool", time.Now(), time.Now()})
		}
		return &leRows{cols: []string{"key", "value", "type", "created_at", "updated_at"}, rows: rows}, nil

	case strings.Contains(lower, `from "users"`):
		return c.selectUsers(lower, args)

	case strings.Contains(lower, `from "refresh_tokens"`):
		return c.selectRefreshes(lower, args)

	case strings.Contains(lower, `from "user_app_passwords"`):
		return c.selectAppPasswords(lower, args)

	case strings.Contains(lower, `from "sessions"`):
		// Logout looks the current JTI up; no session rows are served.
		return &leRows{cols: []string{"id"}}, nil
	}
	return nil, fmt.Errorf("leState: cannot route query: %s", query)
}

func (c *leConn) selectUsers(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	conds, _ := leConditions(leWhere(lower))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.users {
		matched := true
		for col, ord := range conds {
			idx := -1
			for i, name := range leUserColumns {
				if name == col {
					idx = i
				}
			}
			if idx < 0 {
				return nil, fmt.Errorf("leState: unknown user column %q", col)
			}
			if fmt.Sprint(row[idx]) != fmt.Sprint(leArg(args, ord)) {
				matched = false
				break
			}
		}
		if matched && len(conds) > 0 {
			return &leRows{cols: leUserColumns, rows: [][]driver.Value{append([]driver.Value(nil), row...)}}, nil
		}
	}
	return &leRows{cols: leUserColumns}, nil
}

func (c *leConn) selectRefreshes(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cols := []string{"id", "user_id", "family_id", "token_hash", "rotated_from", "rotated_at", "revoked", "revoked_at", "expires_at", "created_at", "last_used_at"}

	// NextFamilyID's aggregate.
	if strings.Contains(lower, "max(") {
		var max int64
		for _, r := range s.refreshes {
			if r.familyID > max {
				max = r.familyID
			}
		}
		return &leRows{cols: []string{"max"}, rows: [][]driver.Value{{max}}}, nil
	}

	conds, _ := leConditions(leWhere(lower))
	var rows [][]driver.Value
	for _, r := range s.refreshes {
		ok := true
		for col, ord := range conds {
			switch col {
			case "token_hash":
				ok = ok && fmt.Sprint(r.tokenHash) == fmt.Sprint(leArg(args, ord))
			case "id":
				ok = ok && fmt.Sprint(r.id) == fmt.Sprint(leArg(args, ord))
			case "user_id":
				ok = ok && fmt.Sprint(r.userID) == fmt.Sprint(leArg(args, ord))
			case "revoked":
				ok = ok && fmt.Sprint(r.revoked) == fmt.Sprint(leArg(args, ord))
			default:
				return nil, fmt.Errorf("leState: unknown refresh column %q", col)
			}
		}
		if ok {
			rows = append(rows, []driver.Value{r.id, r.userID, r.familyID, r.tokenHash, r.rotatedFrom, r.rotatedAt, r.revoked, r.revokedAt, r.expiresAt, time.Now(), nil})
		}
	}
	if leDebug {
		fmt.Printf("DEBUG selectRefreshes: query=%q conds=%v stored=%d matched=%d\n", lower, conds, len(s.refreshes), len(rows))
		for _, r := range s.refreshes {
			fmt.Printf("  row id=%d user=%d fam=%d hash=%s revoked=%v rotated=%v exp=%s\n", r.id, r.userID, r.familyID, r.tokenHash[:8], r.revoked, r.rotatedAt != nil, r.expiresAt.Format(time.RFC3339))
		}
	}
	return &leRows{cols: cols, rows: rows}, nil
}

// leDebug gates temporary diagnostics; flip with LE_DEBUG=1.
var leDebug = os.Getenv("LE_DEBUG") == "1"

func (c *leConn) selectAppPasswords(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	conds, nulls := leConditions(leWhere(lower))
	all := strings.Contains(lower, "select id, user_id, name, created_at, last_used_at, revoked_at")
	var cols []string
	if all {
		cols = []string{"id", "user_id", "name", "created_at", "last_used_at", "revoked_at"}
	} else {
		cols = []string{"id", "user_id", "name", "password_hash", "revoked_at", "created_at", "last_used_at"}
	}
	var rows [][]driver.Value
	for _, p := range s.appPws {
		// The active-only reads carry `revoked_at IS NULL`.
		if nulls["revoked_at"] && p.revoked {
			continue
		}
		ok := true
		for col, ord := range conds {
			switch col {
			case "user_id":
				ok = ok && fmt.Sprint(p.userID) == fmt.Sprint(leArg(args, ord))
			case "id":
				ok = ok && fmt.Sprint(p.id) == fmt.Sprint(leArg(args, ord))
			default:
				return nil, fmt.Errorf("leState: unknown app-password column %q", col)
			}
		}
		if !ok {
			continue
		}
		var revokedAt driver.Value
		if p.revoked {
			revokedAt = time.Now()
		}
		if all {
			rows = append(rows, []driver.Value{p.id, p.userID, p.name, time.Now(), nil, revokedAt})
		} else {
			rows = append(rows, []driver.Value{p.id, p.userID, p.name, p.pwHash, revokedAt, time.Now(), nil})
		}
	}
	return &leRows{cols: cols, rows: rows}, nil
}

func (c *leConn) insertRefresh(lower, query string, args []driver.NamedValue) (driver.Rows, error) {
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("leState: refresh insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	cols := strings.Split(strings.ReplaceAll(query[open+1:vi], `"`, ""), ",")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	row := &leRefreshRow{expiresAt: time.Now().Add(14 * 24 * time.Hour)}
	for i, col := range cols {
		col = strings.TrimSpace(col)
		switch col {
		case "user_id":
			row.userID = leInt64(leArg(args, i+1))
		case "family_id":
			row.familyID = leInt64(leArg(args, i+1))
		case "token_hash":
			row.tokenHash = leStr(leArg(args, i+1))
		case "rotated_from":
			row.rotatedFrom = leInt64(leArg(args, i+1))
		case "expires_at":
			if v, ok := leArg(args, i+1).(time.Time); ok {
				row.expiresAt = v
			}
		}
	}
	s.seq++
	row.id = s.seq
	s.refreshes = append(s.refreshes, row)
	return &leRows{cols: []string{"id"}, rows: [][]driver.Value{{row.id}}}, nil
}

func (c *leConn) insertAppPassword(lower, query string, args []driver.NamedValue) (driver.Rows, error) {
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("leState: app-password insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	cols := strings.Split(strings.ReplaceAll(query[open+1:vi], `"`, ""), ",")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	row := &leAppRow{}
	for i, col := range cols {
		col = strings.TrimSpace(col)
		switch col {
		case "user_id":
			row.userID = leInt64(leArg(args, i+1))
		case "name":
			row.name = leStr(leArg(args, i+1))
		case "password_hash":
			row.pwHash = leStr(leArg(args, i+1))
		}
	}
	s.seq++
	row.id = s.seq
	s.appPws = append(s.appPws, row)
	return &leRows{cols: []string{"id"}, rows: [][]driver.Value{{row.id}}}, nil
}

func (c *leConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	switch {
	case strings.Contains(lower, `update "users"`):
		return c.updateUsers(lower, args)
	case strings.Contains(lower, `update "refresh_tokens"`):
		return c.updateRefreshes(lower, args)
	case strings.Contains(lower, `update "user_app_passwords"`):
		return c.updateAppPasswords(lower, args)
	case strings.Contains(lower, `delete from "user_app_passwords"`):
		return leResult{rows: 0}, nil
	}
	return nil, fmt.Errorf("leState: unsupported exec: %s", query)
}

func (c *leConn) updateUsers(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("leState: user update without set/where: %s", lower)
	}
	conds, _ := leConditions(lower[whereStart+len(" where "):])
	idOrd, ok := conds["id"]
	if !ok {
		return nil, fmt.Errorf("leState: user update without id guard: %s", lower)
	}
	wantID := fmt.Sprint(leArg(args, idOrd))
	var row []driver.Value
	for k, v := range s.users {
		if fmt.Sprint(k) == wantID {
			row = v
			break
		}
	}
	if row == nil {
		return leResult{rows: 0}, nil
	}
	setClause := lower[setStart+len(" set "):whereStart]
	for _, pair := range strings.Split(setClause, ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			continue
		}
		col := leColumnOf(pair[:eq])
		for i, name := range leUserColumns {
			if name == col {
				row[i] = leArg(args, leOrdinal(pair[eq+1:]))
			}
		}
	}
	return leResult{rows: 1}, nil
}

// updateRefreshes models the guarded refresh-token UPDATEs: the rotation
// consume carries `rotated_at IS NULL` + `revoked = false` guards and only
// flips rows that are still live — the single-use guarantee under the fake.
func (c *leConn) updateRefreshes(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("leState: refresh update without set/where: %s", lower)
	}
	setClause := lower[setStart+len(" set "):whereStart]
	conds, nulls := leConditions(lower[whereStart+len(" where "):])
	if leDebug {
		fmt.Printf("DEBUG updateRefreshes: query=%q set=%q conds=%v nulls=%v\n", lower, setClause, conds, nulls)
	}

	// Rotation consume: WHERE id = ? AND revoked = ? AND rotated_at IS NULL.
	if _, guarded := nulls["rotated_at"]; guarded && setClauseContains(setClause, "rotated_at") {
		wantID := fmt.Sprint(leArg(args, conds["id"]))
		for _, r := range s.refreshes {
			if fmt.Sprint(r.id) != wantID {
				continue
			}
			if r.revoked || r.rotatedAt != nil {
				return leResult{rows: 0}, nil // lost the single-use race
			}
			now := time.Now().UTC()
			r.rotatedAt = &now
			r.revoked = true
			r.revokedAt = &now
			return leResult{rows: 1}, nil
		}
		return leResult{rows: 0}, nil
	}

	// Family revocation (reuse detection): WHERE family_id = ? AND revoked = ?.
	if famOrd, ok := conds["family_id"]; ok {
		family := fmt.Sprint(leArg(args, famOrd))
		n := int64(0)
		now := time.Now().UTC()
		for _, r := range s.refreshes {
			if fmt.Sprint(r.familyID) == family && !r.revoked {
				r.revoked = true
				r.revokedAt = &now
				n++
			}
		}
		return leResult{rows: n}, nil
	}

	// Logout / password-change revocations: token_hash or user_id, live rows only.
	now := time.Now().UTC()
	n := int64(0)
	if hashOrd, ok := conds["token_hash"]; ok {
		hash := fmt.Sprint(leArg(args, hashOrd))
		for _, r := range s.refreshes {
			if fmt.Sprint(r.tokenHash) == hash && !r.revoked {
				r.revoked = true
				r.revokedAt = &now
				n++
			}
		}
		return leResult{rows: n}, nil
	}
	if userOrd, ok := conds["user_id"]; ok {
		uid := fmt.Sprint(leArg(args, userOrd))
		for _, r := range s.refreshes {
			if fmt.Sprint(r.userID) == uid && !r.revoked {
				r.revoked = true
				r.revokedAt = &now
				n++
			}
		}
		return leResult{rows: n}, nil
	}
	return leResult{rows: 0}, nil
}

func (c *leConn) updateAppPasswords(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("leState: app-password update without set/where: %s", lower)
	}
	setClause := lower[setStart+len(" set "):whereStart]
	conds, nulls := leConditions(lower[whereStart+len(" where "):])

	if setClauseContains(setClause, "last_used_at") {
		return leResult{rows: 1}, nil
	}
	// Revoke: WHERE user_id = ? AND id = ? AND revoked_at IS NULL.
	if _, guarded := nulls["revoked_at"]; !guarded {
		return nil, fmt.Errorf("leState: revoke without revoked_at IS NULL guard: %s", lower)
	}
	wantID := fmt.Sprint(leArg(args, conds["id"]))
	wantUID := fmt.Sprint(leArg(args, conds["user_id"]))
	for _, p := range s.appPws {
		if fmt.Sprint(p.id) == wantID && fmt.Sprint(p.userID) == wantUID {
			if p.revoked {
				return leResult{rows: 0}, nil
			}
			p.revoked = true
			return leResult{rows: 1}, nil
		}
	}
	return leResult{rows: 0}, nil
}

func setClauseContains(setClause, col string) bool {
	for _, pair := range strings.Split(setClause, ",") {
		if eq := strings.Index(pair, "="); eq >= 0 {
			if leColumnOf(pair[:eq]) == col {
				return true
			}
		}
	}
	return false
}

// ---------- harness ----------

type leHarness struct {
	state    *leState
	o        *okapi.TestServer
	users    *UserHandler
	provider *settings.Provider
}

func newLEHarness(t *testing.T, withRefresh bool) *leHarness {
	t.Helper()
	state := newLEState()
	state.setSetting("two_factor_required", "false")
	gdb := leGorm(t, state)

	users := NewUserHandler(repositories.NewUserRepository(gdb), leTestSecret, nil, nil)
	users.SetMigrator(gdb, nil) // nil migrator: no workspace provisioning in tests
	if withRefresh {
		users.SetRefreshTokens(auth.NewRefreshTokenService(repositories.NewRefreshTokenRepository(gdb), leTestSecret))
	}
	appPasswordRepo := repositories.NewUserAppPasswordRepository(gdb)
	users.SetAppPasswordRepo(appPasswordRepo)

	provider := settings.NewProvider(repositories.NewSettingRepository(gdb))
	users.SetSettings(provider)

	o := okapi.NewTestServer(t)
	jwtAuth := middlewares.JWTAuth(&config.Config{JWTSecret: leTestSecret})
	public := o.Group("/auth")
	public.Post("/login", okapi.H(users.Login))
	public.Post("/refresh", okapi.H(users.Refresh))
	appPasswords := NewAppPasswordHandler(appPasswordRepo)
	authed := o.Group("/users/me", jwtAuth.Middleware)
	authed.Post("/app-passwords", okapi.H(appPasswords.Create))
	authed.Get("/app-passwords", appPasswords.List)
	authed.Delete("/app-passwords/{id:int}", okapi.H(appPasswords.Revoke))
	authed.Get("/whoami-test", users.Me)
	return &leHarness{state: state, o: o, users: users, provider: provider}
}

// setSetting flips a platform setting and drops the provider's cache, the way
// an admin write does in production (SF-100).
func (h *leHarness) setSetting(key, value string) {
	h.state.setSetting(key, value)
	h.provider.Invalidate()
}

func (h *leHarness) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
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

func (h *leHarness) login(t *testing.T, email, code string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"email":%q,"password":%q`, email, leUserPassword)
	if code != "" {
		body += fmt.Sprintf(`,"two_factor_code":%q`, code)
	}
	body += "}"
	return h.do(t, http.MethodPost, "/auth/login", "", body)
}

func leToken(t *testing.T, userID int64, role string) string {
	t.Helper()
	tok, err := okapi.GenerateJwtToken([]byte(leTestSecret), map[string]any{
		"sub":   userID,
		"email": "x@example.com",
		"role":  role,
		"aud":   "posta",
		"jti":   fmt.Sprintf("jti-le-%d", userID),
	}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateJwtToken: %v", err)
	}
	return tok
}

// decodeAuth pulls the AuthResponse payload out of a login/refresh response.
func decodeAuth(t *testing.T, rec *httptest.ResponseRecorder) AuthResponse {
	t.Helper()
	var env struct {
		Success bool       `json:"success"`
		Data    AuthResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode auth response: %v (body %s)", err, rec.Body.String())
	}
	return env.Data
}

func leBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v (body %s)", err, rec.Body.String())
	}
	return env.Data
}

// ---------- GAP-12: the forced-enrollment matrix ----------

// The matrix is setting(on/off) × user(with/without 2FA) × role(admin/user).
func TestLogin_TwoFactorRequired_Matrix(t *testing.T) {
	twofaSecret, _, err := twofactorSecretFor(leTwoFAMail)
	if err != nil {
		t.Fatalf("generate TOTP secret: %v", err)
	}

	cases := []struct {
		name string
		policyOn bool
		seedEmail string
		seedID  int64
		seedRole string
		seed2FA bool
		code    string
		wantStatus     int
		wantMarker     bool // two_factor_enrollment_required
		wantRequires2FA bool
	}{
		{
			name: "policy off, plain user: normal login, no marker",
			seedEmail: lePlainUserMail, seedID: lePlainUserID, seedRole: "user",
			wantStatus: http.StatusOK,
		},
		{
			name: "policy on, plain user: login completes with enrollment marker",
			policyOn: true,
			seedEmail: lePlainUserMail, seedID: lePlainUserID, seedRole: "user",
			wantStatus: http.StatusOK, wantMarker: true,
		},
		{
			name: "policy on, admin without 2FA: no exemption, marker present",
			policyOn: true,
			seedEmail: leAdminMail, seedID: leAdminID, seedRole: "admin",
			wantStatus: http.StatusOK, wantMarker: true,
		},
		{
			name: "policy off, admin without 2FA: normal login",
			seedEmail: leAdminMail, seedID: leAdminID, seedRole: "admin",
			wantStatus: http.StatusOK,
		},
		{
			name: "policy on, 2fa user without code: unchanged requires_2fa 401",
			policyOn: true,
			seedEmail: leTwoFAMail, seedID: leTwoFAUserID, seedRole: "user", seed2FA: true,
			wantStatus: http.StatusUnauthorized, wantRequires2FA: true,
		},
		{
			name: "policy off, 2fa user without code: unchanged requires_2fa 401",
			seedEmail: leTwoFAMail, seedID: leTwoFAUserID, seedRole: "user", seed2FA: true,
			wantStatus: http.StatusUnauthorized, wantRequires2FA: true,
		},
		{
			name: "policy on, 2fa user with valid code: normal login, no marker",
			policyOn: true,
			seedEmail: leTwoFAMail, seedID: leTwoFAUserID, seedRole: "user", seed2FA: true,
			code: "current",
			wantStatus: http.StatusOK,
		},
		{
			name: "policy off, 2fa user with valid code: normal login, no marker",
			seedEmail: leTwoFAMail, seedID: leTwoFAUserID, seedRole: "user", seed2FA: true,
			code: "current",
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newLEHarness(t, false)
			h.state.seedUser(tc.seedID, tc.seedEmail, tc.seedRole, tc.seed2FA, twofaSecret)
			h.setSetting("two_factor_required", map[bool]string{true: "true", false: "false"}[tc.policyOn])

			code := tc.code
			if code == "current" {
				code = generateTOTP(t, twofaSecret)
			}
			rec := h.login(t, tc.seedEmail, code)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantRequires2FA {
				data := leBody(t, rec)
				if data["requires_2fa"] != true {
					t.Fatalf("expected requires_2fa marker, got %v", data["requires_2fa"])
				}
				return
			}

			resp := decodeAuth(t, rec)
			if resp.Token == "" {
				t.Fatal("expected an access token in the response")
			}
			if resp.TwoFactorEnrollmentRequired != tc.wantMarker {
				t.Fatalf("two_factor_enrollment_required = %v, want %v", resp.TwoFactorEnrollmentRequired, tc.wantMarker)
			}
		})
	}
}

// The profile endpoint keeps the marker live so the dashboard can hold the
// user on the setup card until 2FA is configured.
func TestLogin_EnrollmentMarkerInProfile(t *testing.T) {
	h := newLEHarness(t, false)
	h.state.seedUser(lePlainUserID, lePlainUserMail, "user", false, "")
	h.setSetting("two_factor_required", "true")

	rec := h.do(t, http.MethodGet, "/users/me/whoami-test", leToken(t, lePlainUserID, "user"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("Me status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	data := raw["data"].(map[string]any)
	if data["two_factor_enrollment_required"] != true {
		t.Fatalf("profile two_factor_enrollment_required = %v, want true", data["two_factor_enrollment_required"])
	}
	if data["two_factor_enabled"] != false {
		t.Fatalf("profile two_factor_enabled = %v", data["two_factor_enabled"])
	}
}

// generateTOTP mints a currently-valid code for the secret.
func generateTOTP(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}
	return code
}

// redisDeadClient is a real go-redis client aimed at a port that refuses
// connections: MarkRevoked failures are ignored by the logout handler, so the
// database-side assertions still hold.
func redisDeadClient(t *testing.T) *redis.Client {
	t.Helper()
	return redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1", // nothing listens here
		DialTimeout: 100 * time.Millisecond,
		ReadTimeout: 100 * time.Millisecond,
		MaxRetries:  -1,
	})
}

// ---------- CA-03: refresh-token rotation ----------

func TestRefresh_RotateAndSingleUse(t *testing.T) {
	h := newLEHarness(t, true)
	h.state.seedUser(lePlainUserID, lePlainUserMail, "user", false, "")

	rec := h.login(t, lePlainUserMail, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d (body %s)", rec.Code, rec.Body.String())
	}
	first := decodeAuth(t, rec)
	if first.RefreshToken == "" {
		t.Fatal("login must issue a refresh token")
	}

	// Rotate: a fresh access token and a successor refresh token.
	rec = h.do(t, http.MethodPost, "/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, first.RefreshToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d (body %s)", rec.Code, rec.Body.String())
	}
	second := decodeAuth(t, rec)
	if second.Token == "" || second.RefreshToken == "" {
		t.Fatal("rotation must return both tokens")
	}
	if second.Token == first.Token {
		t.Fatal("rotation must mint a NEW access token")
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("rotation must mint a NEW refresh token")
	}
	if second.User.ID != uint(lePlainUserID) {
		t.Fatalf("refresh user id = %d, want %d", second.User.ID, lePlainUserID)
	}

	// Replay of the rotated token: the family dies.
	rec = h.do(t, http.MethodPost, "/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, first.RefreshToken))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed refresh status = %d, want 401", rec.Code)
	}

	// The successor (same family) is dead too.
	rec = h.do(t, http.MethodPost, "/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, second.RefreshToken))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("successor after family revocation status = %d, want 401", rec.Code)
	}
	if h.state.liveRefreshes() != 0 {
		t.Fatalf("family revocation left %d live tokens", h.state.liveRefreshes())
	}
}

// The successor chain keeps working while tokens are used in order.
func TestRefresh_ChainRotation(t *testing.T) {
	h := newLEHarness(t, true)
	h.state.seedUser(lePlainUserID, lePlainUserMail, "user", false, "")

	rec := h.login(t, lePlainUserMail, "")
	first := decodeAuth(t, rec)

	current := first.RefreshToken
	for i := 0; i < 3; i++ {
		rec := h.do(t, http.MethodPost, "/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, current))
		if rec.Code != http.StatusOK {
			t.Fatalf("rotation %d status = %d (body %s)", i+1, rec.Code, rec.Body.String())
		}
		current = decodeAuth(t, rec).RefreshToken
	}
	if current == "" || current == first.RefreshToken {
		t.Fatal("chain must keep producing fresh tokens")
	}
}

func TestRefresh_UnknownOrGarbageToken(t *testing.T) {
	h := newLEHarness(t, true)
	// A missing token fails request validation (400); a malformed one reads as
	// an invalid credential (401) — never an oracle for what exists.
	rec := h.do(t, http.MethodPost, "/auth/refresh", "", `{"refresh_token":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty refresh token status = %d, want 400", rec.Code)
	}
	for _, bad := range []string{"garbage", "deadbeef", "0000000000000000000000000000000000000000000000000000000000000000"} {
		rec := h.do(t, http.MethodPost, "/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, bad))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("refresh with %q: status = %d, want 401", bad, rec.Code)
		}
	}
}

// Logout revokes the presented refresh token (CA-03).
func TestRefresh_LogoutRevokes(t *testing.T) {
	h := newLEHarness(t, true)
	h.state.seedUser(lePlainUserID, lePlainUserMail, "user", false, "")

	rec := h.login(t, lePlainUserMail, "")
	first := decodeAuth(t, rec)

	deadRedis := session.NewStore(redisDeadClient(t))
	// A session-aware logout route on its own prefix.
	o := h.o
	sh := NewSessionHandler(repositories.NewSessionRepository(leGorm(t, h.state)), deadRedis)
	sh.SetRefreshTokens(auth.NewRefreshTokenService(repositories.NewRefreshTokenRepository(leGorm(t, h.state)), leTestSecret))
	logoutAuth := middlewares.JWTAuth(&config.Config{JWTSecret: leTestSecret})
	authed := o.Group("/users/me-logout", logoutAuth.Middleware)
	authed.Post("/logout", okapi.H(sh.Logout))

	rec = h.do(t, http.MethodPost, "/users/me-logout/logout", leToken(t, lePlainUserID, "user"),
		fmt.Sprintf(`{"refresh_token":%q}`, first.RefreshToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("logout status = %d (body %s)", rec.Code, rec.Body.String())
	}

	rec = h.do(t, http.MethodPost, "/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, first.RefreshToken))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout status = %d, want 401", rec.Code)
	}
}

func TestRefresh_DisabledServiceAnswers400(t *testing.T) {
	h := newLEHarness(t, false) // refresh not wired
	rec := h.do(t, http.MethodPost, "/auth/refresh", "", `{"refresh_token":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("disabled refresh status = %d, want 400", rec.Code)
	}
}
