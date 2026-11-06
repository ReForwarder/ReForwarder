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

package email

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

	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// R-23 send-gate tests: strict domain verification must actually change send
// behavior. checkDomainVerification is the gate the Send paths call
// (service.go Send at :516 and :700); these tests run it against the real
// repositories over a fake database/sql driver and pin the full resolution
// order: an explicit workspace override wins, otherwise the platform
// require_domain_verification setting (the admin toggle, default true)
// decides, and in personal (no-workspace) sends the per-user flag decides.

type dvState struct {
	mu     sync.Mutex
	tables map[string][]map[string]driver.Value
}

func newDvState() *dvState {
	return &dvState{tables: map[string][]map[string]driver.Value{}}
}

func (s *dvState) insert(table string, vals map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := map[string]driver.Value{}
	for k, v := range vals {
		switch t := v.(type) {
		case uint:
			row[k] = int64(t)
		case int:
			row[k] = int64(t)
		default:
			row[k] = t
		}
	}
	s.tables[table] = append(s.tables[table], row)
}

func dvGorm(t *testing.T, state *dvState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(dvConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open domain-verification fake gorm: %v", err)
	}
	return gdb
}

type dvConnector struct{ state *dvState }

func (c dvConnector) Connect(context.Context) (driver.Conn, error) { return &dvConn{s: c.state}, nil }
func (c dvConnector) Driver() driver.Driver                        { return dvDrv{} }

type dvDrv struct{}

func (dvDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type dvConn struct{ s *dvState }

func (c *dvConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *dvConn) Close() error                        { return nil }
func (c *dvConn) Begin() (driver.Tx, error)           { return dvTx{}, nil }

type dvTx struct{}

func (dvTx) Commit() error   { return nil }
func (dvTx) Rollback() error { return nil }

type dvRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *dvRows) Columns() []string { return r.cols }
func (r *dvRows) Close() error      { return nil }
func (r *dvRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func dvTableOf(lower string) string {
	for _, name := range []string{"workspace_settings", "domains", "users", "settings"} {
		if strings.Contains(lower, `from "`+name+`"`) {
			return name
		}
	}
	return ""
}

func (c *dvConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := dvTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("dvState: cannot route query: %s", query)
	}
	// Equality-only WHERE: workspace_id, user_id, domain, ownership_verified,
	// id — every lookup this gate issues.
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = lower[i+len(" where "):]
		for _, cut := range []string{" order by ", " limit "} {
			if j := strings.Index(where, cut); j >= 0 {
				where = where[:j]
			}
		}
	}

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := append([]map[string]driver.Value(nil), s.tables[table]...)
	if strings.TrimSpace(where) != "" {
		var preds []func(map[string]driver.Value) bool
		for _, cond := range strings.Split(strings.NewReplacer("(", " ", ")", " ").Replace(where), " and ") {
			cond = strings.TrimSpace(cond)
			if cond == "" {
				continue
			}
			eq := strings.Index(cond, " = ")
			if eq < 0 || !strings.HasPrefix(strings.TrimSpace(cond[eq+3:]), "$") {
				return nil, fmt.Errorf("dvState: unsupported condition %q", cond)
			}
			col := dvColumnOf(cond[:eq])
			n := dvOrdinal(strings.TrimSpace(cond[eq+3:]))
			want := dvArg(args, n)
			preds = append(preds, func(row map[string]driver.Value) bool {
				return fmt.Sprint(row[col]) == fmt.Sprint(want)
			})
		}
		var out []map[string]driver.Value
		for _, row := range matched {
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
		matched = out
	}
	if strings.Contains(lower, "count(*)") {
		return &dvRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	if len(matched) == 0 {
		return &dvRows{cols: []string{"id"}}, nil
	}
	cols := make([]string, 0, len(matched[0]))
	for col := range matched[0] {
		cols = append(cols, col)
	}
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		for _, col := range cols {
			rows[i] = append(rows[i], r[col])
		}
	}
	return &dvRows{cols: cols, rows: rows}, nil
}

func (c *dvConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return nil, fmt.Errorf("dvState: unsupported exec: %s", query)
}

func dvColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func dvOrdinal(tok string) int {
	tok = strings.TrimSpace(tok)
	var n int
	if _, err := fmt.Sscanf(tok[1:], "%d", &n); err != nil {
		panic(fmt.Sprintf("dvState: bad bindvar %q", tok))
	}
	return n
}

func dvArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("dvState: no arg with ordinal %d", n))
}

// dvService builds a Service wired to the gate's three dependencies: the
// domain repo, the user repo and the workspace-settings repo, over one fake
// database, plus the platform settings provider.
func dvService(t *testing.T, state *dvState) *Service {
	t.Helper()
	gdb := dvGorm(t, state)
	s := &Service{}
	s.SetDomainVerification(
		repositories.NewDomainRepository(gdb),
		repositories.NewUserRepository(gdb),
		repositories.NewWorkspaceSettingRepository(gdb),
	)
	sp := settings.NewProvider(repositories.NewSettingRepository(gdb))
	s.SetSettings(sp)
	return s
}

func wsIDPtr(v uint) *uint { return &v }

// The workspace override wins over the platform default: an explicit
// require_verified_domain=false settings row allows sending from an
// unverified sender domain even though the platform toggle is on (default).
func TestCheckDomainVerificationWorkspaceOverrideWins(t *testing.T) {
	state := newDvState()
	state.insert("workspace_settings", map[string]any{"workspace_id": uint(3), "require_verified_domain": false})
	s := dvService(t, state)

	if err := s.checkDomainVerification(7, wsIDPtr(3), "sender@unverified.test"); err != nil {
		t.Fatalf("workspace override false must allow the send: %v", err)
	}
}

// The inverse override: an explicit workspace row with true blocks the send
// even when the platform toggle is off.
func TestCheckDomainVerificationWorkspaceOverrideBlocks(t *testing.T) {
	state := newDvState()
	state.insert("workspace_settings", map[string]any{"workspace_id": uint(3), "require_verified_domain": true})
	state.insert("settings", map[string]any{"key": "require_domain_verification", "value": "false"})
	s := dvService(t, state)

	err := s.checkDomainVerification(7, wsIDPtr(3), "sender@unverified.test")
	if err == nil {
		t.Fatal("workspace override true must block an unverified sender domain")
	}
	if !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("error = %v, want the not-verified message", err)
	}
}

// No workspace row: the platform require_domain_verification default (true)
// blocks the send — out-of-the-box shared-server sending requires ownership.
func TestCheckDomainVerificationPlatformDefaultBlocks(t *testing.T) {
	state := newDvState()
	s := dvService(t, state)

	if err := s.checkDomainVerification(7, wsIDPtr(3), "sender@unverified.test"); err == nil {
		t.Fatal("platform default (true) must block an unverified sender domain")
	}
}

// The admin toggle: require_domain_verification=false at platform level
// allows the send when the workspace carries no explicit override.
func TestCheckDomainVerificationAdminToggleOffAllows(t *testing.T) {
	state := newDvState()
	state.insert("settings", map[string]any{"key": "require_domain_verification", "value": "false"})
	s := dvService(t, state)

	if err := s.checkDomainVerification(7, wsIDPtr(3), "sender@unverified.test"); err != nil {
		t.Fatalf("admin toggle off must allow the send: %v", err)
	}
}

func TestCheckDomainVerificationAdminToggleOnBlocks(t *testing.T) {
	state := newDvState()
	state.insert("settings", map[string]any{"key": "require_domain_verification", "value": "true"})
	s := dvService(t, state)

	if err := s.checkDomainVerification(7, wsIDPtr(3), "sender@unverified.test"); err == nil {
		t.Fatal("admin toggle on must block an unverified sender domain")
	}
}

// A workspace-verified sender domain passes even in strict mode.
func TestCheckDomainVerificationVerifiedDomainPasses(t *testing.T) {
	state := newDvState()
	state.insert("domains", map[string]any{
		"workspace_id": uint(3), "domain": "verified.test", "ownership_verified": true,
	})
	s := dvService(t, state)

	if err := s.checkDomainVerification(7, wsIDPtr(3), "sender@verified.test"); err != nil {
		t.Fatalf("verified domain must pass strict mode: %v", err)
	}
	// Case-insensitive match on the sender domain.
	if err := s.checkDomainVerification(7, wsIDPtr(3), "sender@VERIFIED.test"); err != nil {
		t.Fatalf("domain match must be case-insensitive: %v", err)
	}
}

// Personal-mode sends (no workspace) decide on the user's stored flag.
func TestCheckDomainVerificationUserFlagDecidesPersonalMode(t *testing.T) {
	state := newDvState()
	state.insert("users", map[string]any{"id": uint(7), "require_verified_domain": true})
	s := dvService(t, state)

	if err := s.checkDomainVerification(7, nil, "sender@unverified.test"); err == nil {
		t.Fatal("user flag true must block an unverified sender domain")
	}

	state.insert("users", map[string]any{"id": uint(8), "require_verified_domain": false})
	if err := s.checkDomainVerification(8, nil, "sender@unverified.test"); err != nil {
		t.Fatalf("user flag false must allow the send: %v", err)
	}
}
