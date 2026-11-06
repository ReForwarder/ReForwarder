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
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The recovery-code store (R-10) runs against the real gorm on a fake
// database/sql driver (the repo's established fake-connector arrangement): the
// fake stores user_recovery_codes rows and evaluates the queries the store
// issues, so generation, single-use consumption, and the used_at burn are
// pinned without a live database.

type rcState struct {
	mu   sync.Mutex
	seq  int64
	rows []*rcRow
}

type rcRow struct {
	id        int64
	userID    int64
	codeHash  string
	usedAt    *time.Time
	createdAt time.Time
}

func newRcState() *rcState { return &rcState{seq: 100} }

var rcColumns = []string{"id", "user_id", "code_hash", "used_at", "created_at"}

func (s *rcState) values(r *rcRow) []driver.Value {
	return []driver.Value{r.id, r.userID, r.codeHash, r.usedAt, r.createdAt}
}

func (s *rcState) unusedByUser(userID int64) []*rcRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*rcRow
	for _, r := range s.rows {
		if r.userID == userID && r.usedAt == nil {
			out = append(out, r)
		}
	}
	return out
}

func (s *rcState) count(userID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.rows {
		if r.userID == userID && r.usedAt == nil {
			n++
		}
	}
	return n
}

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

type rcConnector struct{ state *rcState }

func (c rcConnector) Connect(context.Context) (driver.Conn, error) { return &rcConn{s: c.state}, nil }
func (c rcConnector) Driver() driver.Driver                        { return rcDrv{} }

type rcDrv struct{}

func (rcDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func rcGorm(t *testing.T, state *rcState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(rcConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open recovery fake gorm: %v", err)
	}
	return gdb
}

func rcArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("rcState: no arg with ordinal %d", n))
}

func (c *rcConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")

	// gorm Create ... RETURNING "id": mint the row and return the generated id.
	if strings.Contains(lower, "returning") {
		if !strings.Contains(lower, `insert into "user_recovery_codes"`) {
			return nil, fmt.Errorf("rcState: unsupported returning query: %s", query)
		}
		return c.execInsert(lower, args)
	}
	switch {
	case strings.Contains(lower, "count("):
		s := c.s
		s.mu.Lock()
		defer s.mu.Unlock()
		n := int64(0)
		userID := rcArg(args, 1).(int64)
		for _, r := range s.rows {
			if r.userID == userID && r.usedAt == nil {
				n++
			}
		}
		return &rcRows{cols: []string{"count"}, rows: [][]driver.Value{{n}}}, nil
	case strings.Contains(lower, `from "user_recovery_codes"`):
		s := c.s
		s.mu.Lock()
		defer s.mu.Unlock()
		userID := rcArg(args, 1).(int64)
		var rows [][]driver.Value
		for _, r := range s.rows {
			if r.userID == userID && r.usedAt == nil {
				rows = append(rows, s.values(r))
			}
		}
		return &rcRows{cols: rcColumns, rows: rows}, nil
	}
	return nil, fmt.Errorf("rcState: cannot route query: %s", query)
}

// execInsert mints a recovery-code row from the INSERT's bind values.
func (c *rcConn) execInsert(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	row := &rcRow{createdAt: time.Now()}
	// Column list sits between INSERT INTO "user_recovery_codes" ( and ) VALUES.
	start := strings.Index(lower, `"user_recovery_codes" (`)
	end := strings.Index(lower[start:], ") values")
	if start < 0 || end < 0 {
		return nil, fmt.Errorf("rcState: cannot parse insert: %s", lower)
	}
	cols := strings.Split(strings.ReplaceAll(strings.TrimSpace(lower[start+len(`"user_recovery_codes" (`):start+end]), `"`, ""), ",")
	for i, col := range cols {
		col = strings.TrimSpace(col)
		switch col {
		case "user_id":
			row.userID = rcArg(args, i+1).(int64)
		case "code_hash":
			row.codeHash = rcArg(args, i+1).(string)
		case "used_at":
			if v := rcArg(args, i+1); v != nil {
				t := v.(time.Time)
				row.usedAt = &t
			}
		case "created_at":
			if v := rcArg(args, i+1); v != nil {
				row.createdAt = v.(time.Time)
			}
		}
	}
	s.seq++
	row.id = s.seq
	s.rows = append(s.rows, row)
	return &rcRows{cols: []string{"id"}, rows: [][]driver.Value{{row.id}}}, nil
}

func (c *rcConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.Contains(lower, `delete from "user_recovery_codes"`):
		// WHERE user_id = $1 [AND used_at IS NULL]
		userID := rcArg(args, 1).(int64)
		onlyUnused := strings.Contains(lower, "used_at is null")
		kept := s.rows[:0]
		removed := int64(0)
		for _, r := range s.rows {
			drop := r.userID == userID && (!onlyUnused || r.usedAt == nil)
			if drop {
				removed++
				continue
			}
			kept = append(kept, r)
		}
		s.rows = kept
		return rcResult{rows: removed}, nil
	case strings.Contains(lower, `update "user_recovery_codes"`):
		// SET used_at=$1 WHERE id=$2 AND used_at IS NULL
		if v, ok := rcArg(args, 1).(time.Time); ok {
			id := rcArg(args, 2).(int64)
			matched := int64(0)
			for _, r := range s.rows {
				if r.id == id && r.usedAt == nil {
					r.usedAt = &v
					matched++
				}
			}
			return rcResult{rows: matched}, nil
		}
		return nil, fmt.Errorf("rcState: unsupported update binds: %s", query)
	}
	return nil, fmt.Errorf("rcState: unsupported exec: %s", query)
}

// ---------- tests ----------

func newRecoveryStore(t *testing.T) (*RecoveryCodeStore, *rcState) {
	t.Helper()
	state := newRcState()
	return NewRecoveryCodeStore(rcGorm(t, state)), state
}

func TestRecoveryCodeGenerateAndConsume(t *testing.T) {
	store, state := newRecoveryStore(t)

	codes, err := store.Generate(7)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("Generate issued %d codes, want %d", len(codes), recoveryCodeCount)
	}
	for _, code := range codes {
		normalized := normalizeRecoveryCode(code)
		if len(normalized) != recoveryCodeLength {
			t.Fatalf("code %q normalizes to %d chars, want %d", code, len(normalized), recoveryCodeLength)
		}
		for _, r := range normalized {
			if !strings.ContainsRune(recoveryCodeAlphabet, r) {
				t.Fatalf("code %q carries character %q outside the unambiguous alphabet", code, r)
			}
		}
	}
	if n := state.count(7); n != recoveryCodeCount {
		t.Fatalf("stored %d unused codes, want %d", n, recoveryCodeCount)
	}
	// Only bcrypt digests are stored: no row may carry the plaintext.
	for _, r := range state.unusedByUser(7) {
		if !strings.HasPrefix(r.codeHash, "$2") {
			t.Fatalf("stored digest %q is not a bcrypt hash", r.codeHash)
		}
		for _, code := range codes {
			if r.codeHash == code || r.codeHash == normalizeRecoveryCode(code) {
				t.Fatal("a code was stored in plaintext form")
			}
		}
	}

	remaining, err := store.Remaining(7)
	if err != nil || remaining != recoveryCodeCount {
		t.Fatalf("Remaining = (%d, %v), want (%d, nil)", remaining, err, recoveryCodeCount)
	}

	// A valid code is consumed exactly once.
	if !store.Consume(7, codes[0]) {
		t.Fatal("Consume(valid) = false, want true")
	}
	if store.Consume(7, codes[0]) {
		t.Fatal("a consumed code verified again — single-use violated")
	}
	if n := state.count(7); n != recoveryCodeCount-1 {
		t.Fatalf("unused codes after burn = %d, want %d", n, recoveryCodeCount-1)
	}

	// Separators and case do not matter.
	if !store.Consume(7, strings.ToLower(strings.ReplaceAll(codes[1], "-", " "))) {
		t.Fatal("Consume(lowercase, space-separated) = false, want true")
	}

	// Wrong codes never verify.
	if store.Consume(7, "AAAA-AAAA") || store.Consume(7, codes[1]+"X") {
		t.Fatal("invalid code verified")
	}
	if store.Consume(999, codes[2]) {
		t.Fatal("another user's code verified")
	}
}

// Regenerating invalidates every outstanding code: only the newest set works.
func TestRecoveryCodeGenerateInvalidatesOutstanding(t *testing.T) {
	store, state := newRecoveryStore(t)

	first, err := store.Generate(9)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	second, err := store.Generate(9)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if n := state.count(9); n != recoveryCodeCount {
		t.Fatalf("unused codes after regeneration = %d, want %d", n, recoveryCodeCount)
	}
	for _, code := range first {
		if store.Consume(9, code) {
			t.Fatalf("stale code %q from the replaced set verified", code)
		}
	}
	if !store.Consume(9, second[0]) {
		t.Fatal("fresh code did not verify")
	}
}

func TestRecoveryCodeDeleteAll(t *testing.T) {
	store, state := newRecoveryStore(t)
	if _, err := store.Generate(11); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := store.DeleteAll(11); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if n := state.count(11); n != 0 {
		t.Fatalf("codes after DeleteAll = %d, want 0", n)
	}
	if remaining, _ := store.Remaining(11); remaining != 0 {
		t.Fatalf("Remaining after DeleteAll = %d, want 0", remaining)
	}
}

func TestNormalizeRecoveryCode(t *testing.T) {
	cases := map[string]string{
		"ABCD-EFGH":   "ABCDEFGH",
		"abcd-efgh":   "ABCDEFGH",
		" ab cd efgh": "ABCDEFGH",
		"ABCDEFGH":    "ABCDEFGH",
		"":            "",
		"---":         "",
	}
	for in, want := range cases {
		if got := normalizeRecoveryCode(in); got != want {
			t.Errorf("normalizeRecoveryCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatRecoveryCode(t *testing.T) {
	if got := formatRecoveryCode("ABCDEFGH"); got != "ABCD-EFGH" {
		t.Errorf("formatRecoveryCode = %q, want %q", got, "ABCD-EFGH")
	}
	if got := formatRecoveryCode("AB"); got != "AB" {
		t.Errorf("formatRecoveryCode(short) = %q, want unchanged", got)
	}
}
