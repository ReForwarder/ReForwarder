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

package smtprelay

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The R-25 rehash-on-success migration writes through the repository, and
// these tests run the real SMTPCredentialRepository over a fake database/sql
// driver so the upgrade of a legacy SHA-256 row to bcrypt — and the
// conditional (CAS) write that keeps a concurrently rotated row from being
// clobbered — are pinned, not just the pure hash functions.

type rcState struct {
	mu   sync.Mutex
	rows map[int64]*rcRow
	// updates records one entry per issued UPDATE: the SET hash, the WHERE id
	// and the WHERE previous hash (the CAS guard).
	updates []rcUpdate
}

type rcRow struct {
	id           int64
	username     string
	passwordHash string
	revoked      bool
}

type rcUpdate struct {
	setHash   string
	whereID   int64
	wherePrev string
	applied   bool
}

func newRcState() *rcState { return &rcState{rows: map[int64]*rcRow{}} }

func (s *rcState) insert(id int64, username, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id] = &rcRow{id: id, username: username, passwordHash: hash}
}

func (s *rcState) hashOf(id int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[id]; ok {
		return r.passwordHash
	}
	return ""
}

func (s *rcState) appliedUpdates() []rcUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]rcUpdate, 0, len(s.updates))
	for _, u := range s.updates {
		if u.applied {
			out = append(out, u)
		}
	}
	return out
}

func rcGorm(t *testing.T, state *rcState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(rcConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open credential fake gorm: %v", err)
	}
	return gdb
}

type rcConnector struct{ state *rcState }

func (c rcConnector) Connect(context.Context) (driver.Conn, error) { return &rcConn{s: c.state}, nil }
func (c rcConnector) Driver() driver.Driver                        { return rcDrv{} }

type rcDrv struct{}

func (rcDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type rcConn struct{ s *rcState }

func (c *rcConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *rcConn) Close() error                        { return nil }
func (c *rcConn) Begin() (driver.Tx, error)           { return rcTx{}, nil }

type rcTx struct{}

func (rcTx) Commit() error   { return nil }
func (rcTx) Rollback() error { return nil }

type rcResult struct{ rows int64 }

func (r rcResult) LastInsertId() (int64, error) { return 0, nil }
func (r rcResult) RowsAffected() (int64, error) { return r.rows, nil }

type rcRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *rcRows) Columns() []string { return r.cols }
func (r *rcRows) Close() error      { return nil }
func (r *rcRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func (c *rcConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `from "smtp_credentials"`) {
		return nil, fmt.Errorf("rcState: cannot route query: %s", query)
	}
	// The lookups the credential flow issues: WHERE username = $1 (FindByUsername)
	// and WHERE id = $N (FindByID/First). Equality-only WHERE clauses.
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = lower[i+len(" where "):]
		for _, cut := range []string{" order by ", " limit "} {
			if j := strings.Index(where, cut); j >= 0 {
				where = where[:j]
			}
		}
	}
	cond := strings.TrimSpace(where)
	var col string
	var want driver.Value
	if cond != "" {
		eq := strings.Index(cond, " = ")
		if eq < 0 || !strings.HasPrefix(strings.TrimSpace(cond[eq+3:]), "$") {
			return nil, fmt.Errorf("rcState: unsupported condition %q", cond)
		}
		col = rcColumnOf(cond[:eq])
		v, err := argValue(args, strings.TrimSpace(cond[eq+3:]))
		if err != nil {
			return nil, err
		}
		want = v
	}

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows [][]driver.Value
	for _, r := range s.rows {
		if col != "" {
			var got driver.Value
			switch col {
			case "username":
				got = r.username
			case "id":
				got = r.id
			default:
				return nil, fmt.Errorf("rcState: unsupported lookup column %q", col)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				continue
			}
		}
		rows = append(rows, []driver.Value{
			r.id, r.username, r.passwordHash, r.revoked,
		})
	}
	return &rcRows{
		cols: []string{"id", "username", "password_hash", "revoked"},
		rows: rows,
	}, nil
}

func (c *rcConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `update "smtp_credentials"`) {
		return nil, fmt.Errorf("rcState: unsupported exec: %s", query)
	}
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("rcState: malformed update: %s", query)
	}
	setClause := lower[setStart+len(" set ") : whereStart]
	pair := strings.TrimSpace(setClause)
	eq := strings.Index(pair, "=")
	if eq < 0 || !strings.HasPrefix(strings.TrimSpace(pair[eq+1:]), "$") {
		return nil, fmt.Errorf("rcState: unsupported SET %q", pair)
	}
	setCol := rcColumnOf(pair[:eq])
	if setCol != "password_hash" {
		return nil, fmt.Errorf("rcState: unsupported SET column %q", setCol)
	}
	setRaw, err := argValue(args, strings.TrimSpace(pair[eq+1:]))
	if err != nil {
		return nil, err
	}
	setHash := fmt.Sprint(setRaw)

	// WHERE id = $n AND password_hash = $m (order-agnostic equality pairs).
	where := lower[whereStart+len(" where "):]
	var whereID int64
	var wherePrev string
	for _, cond := range strings.Split(strings.NewReplacer("(", " ", ")", " ").Replace(where), " and ") {
		cond = strings.TrimSpace(cond)
		eq := strings.Index(cond, " = ")
		if eq < 0 {
			return nil, fmt.Errorf("rcState: unsupported condition %q", cond)
		}
		col := rcColumnOf(cond[:eq])
		raw, err := argValue(args, strings.TrimSpace(cond[eq+3:]))
		if err != nil {
			return nil, err
		}
		val := fmt.Sprint(raw)
		switch col {
		case "id":
			whereID, err = strconv.ParseInt(val, 10, 64)
			if err != nil {
				return nil, err
			}
		case "password_hash":
			wherePrev = val
		default:
			return nil, fmt.Errorf("rcState: unsupported WHERE column %q", col)
		}
	}

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	u := rcUpdate{setHash: setHash, whereID: whereID, wherePrev: wherePrev}
	row, ok := s.rows[whereID]
	if ok && row.passwordHash == wherePrev {
		row.passwordHash = setHash
		u.applied = true
	}
	s.updates = append(s.updates, u)
	if u.applied {
		return rcResult{rows: 1}, nil
	}
	return rcResult{rows: 0}, nil
}

// rcColumnOf normalizes a (possibly table-qualified) column reference to its
// bare name.
func rcColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

// argValue resolves a `$N` bindvar token against the statement's arguments.
func argValue(args []driver.NamedValue, tok string) (driver.Value, error) {
	tok = strings.TrimSpace(tok)
	if !strings.HasPrefix(tok, "$") {
		return nil, fmt.Errorf("rcState: expected bindvar, got %q", tok)
	}
	var ord int
	if _, err := fmt.Sscanf(tok[1:], "%d", &ord); err != nil {
		return nil, err
	}
	for _, a := range args {
		if a.Ordinal == ord {
			return a.Value, nil
		}
	}
	return nil, fmt.Errorf("rcState: no arg with ordinal %d", ord)
}

// ---------- the migration tests ----------

// A successful login against a legacy SHA-256 row must transparently upgrade
// it to bcrypt (R-25): the row's stored hash changes to a bcrypt digest of the
// same password, the CAS guard carries the digest that was just verified, and
// the upgraded row verifies again through the bcrypt path.
func TestVerifyPasswordMigratesLegacyHash(t *testing.T) {
	state := newRcState()
	state.insert(1, "smtp_abc123", legacyHashPassword("legacy-pass"))
	repo := repositories.NewSMTPCredentialRepository(rcGorm(t, state))

	cred, err := repo.FindByUsername("smtp_abc123")
	if err != nil {
		t.Fatalf("FindByUsername: %v", err)
	}
	if !VerifyPassword(repo, cred, "legacy-pass") {
		t.Fatal("legacy credential did not verify")
	}

	ups := state.appliedUpdates()
	if len(ups) != 1 {
		t.Fatalf("expected exactly one applied UPDATE, got %+v", state.updates)
	}
	if ups[0].wherePrev != legacyHashPassword("legacy-pass") {
		t.Fatalf("rehash CAS must carry the verified legacy digest, got %q", ups[0].wherePrev)
	}
	if !isBcryptHash(ups[0].setHash) {
		t.Fatalf("upgraded hash is not bcrypt: %q", ups[0].setHash)
	}

	// The stored row now carries the bcrypt digest and verifies through it.
	if got := state.hashOf(1); !isBcryptHash(got) {
		t.Fatalf("stored hash after login = %q, want bcrypt", got)
	}
	reloaded, err := repo.FindByID(1)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if !VerifyPassword(repo, reloaded, "legacy-pass") {
		t.Fatal("upgraded credential does not verify")
	}
	if VerifyPassword(repo, reloaded, "wrong-pass") {
		t.Fatal("upgraded credential accepts the wrong password")
	}
	if got := state.hashOf(1); got != ups[0].setHash {
		t.Fatal("a verifying bcrypt row must not be rewritten")
	}
}

// The rehash is conditional on the digest that was just verified: if the row
// was rotated concurrently (its stored hash is no longer what the caller
// verified), the migration write must match nothing and the new hash must
// survive.
func TestVerifyPasswordDoesNotClobberConcurrentRotation(t *testing.T) {
	state := newRcState()
	legacy := legacyHashPassword("legacy-pass")
	state.insert(2, "smtp_rotated", legacy)
	repo := repositories.NewSMTPCredentialRepository(rcGorm(t, state))

	// The caller reads a stale credential (pre-rotation snapshot).
	cred, err := repo.FindByID(2)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	rotated, err := hashPassword("brand-new-pass")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	// ...then another process rotates the row before the migration write.
	state.mu.Lock()
	state.rows[2].passwordHash = rotated
	state.mu.Unlock()

	if !VerifyPassword(repo, cred, "legacy-pass") {
		t.Fatal("the stale legacy credential still verifies (verification is hash-local)")
	}
	if got := state.hashOf(2); got != rotated {
		t.Fatalf("row hash = %q, want the concurrently rotated bcrypt digest untouched", got)
	}
	if got := state.hashOf(2); !VerifyPassword(repo, &models.SMTPCredential{PasswordHash: got}, "brand-new-pass") {
		t.Fatal("rotated credential no longer verifies after the racing migration")
	}
}

// A nil repository must leave the row alone: the pure verification path (used
// by callers that cannot write) verifies legacy rows without upgrading them.
func TestVerifyPasswordNilRepoSkipsMigration(t *testing.T) {
	state := newRcState()
	state.insert(3, "smtp_norepo", legacyHashPassword("legacy-pass"))

	cred := &models.SMTPCredential{ID: 3, Username: "smtp_norepo", PasswordHash: legacyHashPassword("legacy-pass")}
	if !VerifyPassword(nil, cred, "legacy-pass") {
		t.Fatal("legacy credential did not verify with a nil repo")
	}
	if len(state.updates) != 0 {
		t.Fatalf("nil repo issued %d updates, want none", len(state.updates))
	}
	if got := state.hashOf(3); got != legacyHashPassword("legacy-pass") {
		t.Fatalf("nil repo rewrote the row: %q", got)
	}
}

// A failed login against a legacy row must not trigger the rehash either.
func TestVerifyPasswordWrongPasswordNoMigration(t *testing.T) {
	state := newRcState()
	state.insert(4, "smtp_wrong", legacyHashPassword("legacy-pass"))
	repo := repositories.NewSMTPCredentialRepository(rcGorm(t, state))

	cred, err := repo.FindByID(4)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if VerifyPassword(repo, cred, "not-the-password") {
		t.Fatal("wrong password verified")
	}
	if ups := state.appliedUpdates(); len(ups) != 0 {
		t.Fatalf("failed login must not rehash, got %+v", ups)
	}
	if got := state.hashOf(4); got != legacyHashPassword("legacy-pass") {
		t.Fatalf("failed login rewrote the row: %q", got)
	}
}
