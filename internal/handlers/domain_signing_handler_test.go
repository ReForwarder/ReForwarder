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
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	postacrypto "github.com/goposta/posta/internal/services/crypto"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The signing-key handler runs through the real okapi stack (JWT middleware +
// workspace binding, the segment_handler_test.go arrangement) against a fake
// database/sql driver holding the domains, domain_signing_keys and events
// tables. Assertions pin the wire contract — no private key material in any
// response, lifecycle transitions, scope isolation — and the DNS checks are
// stubbed at the email package's lookup seam.

const dskTestWorkspace = "3"

var dskColumns = map[string][]string{
	"domains": {"id", "user_id", "workspace_id", "domain", "ownership_verified", "spf_verified",
		"dkim_verified", "dmarc_verified", "verification_token", "created_at"},
	"domain_signing_keys": {"id", "user_id", "workspace_id", "domain_id", "selector", "algorithm",
		"private_key_pem", "status", "dns_state", "dns_published_at", "created_at", "updated_at"},
	"events": {"id", "category", "type", "workspace_id", "actor_id", "actor_name", "client_ip",
		"message", "metadata", "outcome", "target_type", "target_id", "created_at"},
}

var dskDefaults = map[string]map[string]func() driver.Value{
	"domains": {
		"ownership_verified": func() driver.Value { return false },
		"spf_verified":       func() driver.Value { return false },
		"dkim_verified":      func() driver.Value { return false },
		"dmarc_verified":     func() driver.Value { return false },
		"workspace_id":       func() driver.Value { return nil },
	},
	"domain_signing_keys": {
		"workspace_id":     func() driver.Value { return nil },
		"dns_state":        func() driver.Value { return models.DomainSigningDNSPending },
		"dns_published_at": func() driver.Value { return nil },
		"algorithm":        func() driver.Value { return models.SigningAlgorithmRSA },
		"status":           func() driver.Value { return models.DomainSigningStatusActive },
	},
}

type dskTable struct {
	columns []string
	rows    [][]driver.Value
}

type dskState struct {
	mu     sync.Mutex
	tables map[string]*dskTable
	seq    int64
}

func newDskState() *dskState {
	tables := make(map[string]*dskTable, len(dskColumns))
	for name, cols := range dskColumns {
		tables[name] = &dskTable{columns: cols}
	}
	return &dskState{tables: tables, seq: 400}
}

func (s *dskState) colIndex(table, col string) int {
	for i, c := range dskColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

// seed inserts a fixture row; unspecified columns take their defaults.
func dskSeed(t *testing.T, s *dskState, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := dskColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := dskDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for col, v := range vals {
		idx := s.colIndex(table, col)
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, col)
		}
		row[idx] = dskVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func dskVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
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
	default:
		panic(fmt.Sprintf("dskVal: unsupported type %T", v))
	}
}

// rowsOf returns matching rows of a table as column maps (for assertions).
func (s *dskState) rowsOf(table string) []map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, 0, len(s.tables[table].rows))
	for _, row := range s.tables[table].rows {
		m := map[string]driver.Value{}
		for i, c := range dskColumns[table] {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

// eventTypes lists the persisted audit event types.
func (s *dskState) eventTypes() []string {
	var out []string
	for _, m := range s.rowsOf("events") {
		if t, ok := m["type"].(string); ok {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func dskGorm(t *testing.T, state *dskState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(dskConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open signing fake gorm: %v", err)
	}
	return gdb
}

type dskConnector struct{ state *dskState }

func (c dskConnector) Connect(context.Context) (driver.Conn, error) { return &dskConn{s: c.state}, nil }
func (c dskConnector) Driver() driver.Driver                        { return dskDrv{} }

type dskDrv struct{}

func (dskDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type dskConn struct{ s *dskState }

func (c *dskConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *dskConn) Close() error                        { return nil }
func (c *dskConn) Begin() (driver.Tx, error)           { return dskTx{}, nil }

type dskTx struct{}

func (dskTx) Commit() error   { return nil }
func (dskTx) Rollback() error { return nil }

type dskResult struct{ rows int64 }

func (r dskResult) LastInsertId() (int64, error) { return 0, nil }
func (r dskResult) RowsAffected() (int64, error) { return r.rows, nil }

type dskRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *dskRows) Columns() []string { return r.cols }
func (r *dskRows) Close() error      { return nil }
func (r *dskRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func dskTableOf(lower string) string {
	for _, name := range []string{"domain_signing_keys", "domains", "events"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`, `join ` + name} {
			if strings.Contains(lower, pat) {
				return name
			}
		}
	}
	return ""
}

func (c *dskConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := dskTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("dskState: cannot route query: %s", query)
	}
	if strings.Contains(lower, "insert into") {
		return c.handleInsert(lower, table, query, args)
	}
	// The signing repo joins domains to resolve the sending domain by name.
	if strings.Contains(lower, "join domains on domains.id = domain_signing_keys.domain_id") {
		return c.handleActiveByDomain(lower, args)
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(lower, "count(*)") {
		return &dskRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	if m := strings.Index(lower, " limit "); m >= 0 {
		if n, err := dskBindToken(lower[m+len(" limit "):]); err == nil {
			if v, ok := dskArg(args, n).(int64); ok && int(v) < len(matched) {
				matched = matched[:int(v)]
			}
		}
	}
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &dskRows{cols: dskColumns[table], rows: rows}, nil
}

// handleActiveByDomain answers the join query: WHERE domains.domain = $1 AND
// domain_signing_keys.status = $2 AND domain_signing_keys.dns_state = $3.
func (c *dskConn) handleActiveByDomain(_ string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	domIdx := s.colIndex("domains", "domain")
	idIdx := s.colIndex("domains", "id")
	keyDomIdx := s.colIndex("domain_signing_keys", "domain_id")
	statusIdx := s.colIndex("domain_signing_keys", "status")
	dnsIdx := s.colIndex("domain_signing_keys", "dns_state")

	domain := dskArg(args, 1)
	status := dskArg(args, 2)
	dnsState := dskArg(args, 3)

	var best []driver.Value
	for _, k := range s.tables["domain_signing_keys"].rows {
		if k[statusIdx] != status || k[dnsIdx] != dnsState {
			continue
		}
		for _, d := range s.tables["domains"].rows {
			if d[idIdx] == k[keyDomIdx] && dskEqual(d[domIdx], domain) {
				if best == nil || k[s.colIndex("domain_signing_keys", "id")].(int64) > best[s.colIndex("domain_signing_keys", "id")].(int64) {
					best = k
				}
			}
		}
	}
	if best == nil {
		return nil, sql.ErrNoRows // gorm maps this to ErrRecordNotFound
	}
	return &dskRows{cols: dskColumns["domain_signing_keys"], rows: [][]driver.Value{best}}, nil
}

func (c *dskConn) handleInsert(lower, table, query string, args []driver.NamedValue) (driver.Rows, error) {
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("dskState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, fmt.Errorf("dskState: insert without a column list")
	}
	colParts := strings.Split(query[open+1:close], ",")
	valText := query[vi+len(" values "):]
	end := strings.LastIndex(valText, ")")
	valParts := strings.Split(valText[1:end], ",")
	if len(valParts) != len(colParts) {
		return nil, fmt.Errorf("dskState: %d columns but %d values", len(colParts), len(valParts))
	}
	vals := make([]driver.Value, len(valParts))
	for i, p := range valParts {
		n, err := strconv.Atoi(strings.TrimSpace(p)[1:])
		if err != nil {
			return nil, err
		}
		vals[i] = dskArg(args, n)
	}

	var returning []string
	if i := strings.Index(lower, " returning "); i >= 0 {
		for _, cn := range strings.Split(lower[i+len(" returning "):], ",") {
			returning = append(returning, strings.Trim(strings.TrimSpace(cn), `"`))
		}
	}

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := dskColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := dskDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for j, col := range colParts {
		col = strings.Trim(strings.TrimSpace(col), `"`)
		idx := s.colIndex(table, col)
		if idx < 0 {
			return nil, fmt.Errorf("dskState: %s has no column %q", table, col)
		}
		row[idx] = vals[j]
	}
	s.seq++
	row[s.colIndex(table, "id")] = s.seq
	s.tables[table].rows = append(s.tables[table].rows, row)

	out := make([]driver.Value, len(returning))
	for i, cn := range returning {
		out[i] = row[s.colIndex(table, cn)]
	}
	return &dskRows{cols: returning, rows: [][]driver.Value{out}}, nil
}

func (c *dskConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := dskTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("dskState: unsupported exec: %s", query)
	}
	if strings.Contains(lower, "delete from") {
		matched, err := c.s.evalWhere(table, lower, args)
		if err != nil {
			return nil, err
		}
		s := c.s
		s.mu.Lock()
		defer s.mu.Unlock()
		kept := s.tables[table].rows[:0]
		for _, row := range s.tables[table].rows {
			removed := false
			for _, m := range matched {
				if &row[0] == &m[0] {
					removed = true
					break
				}
			}
			if !removed {
				kept = append(kept, row)
			}
		}
		s.tables[table].rows = kept
		return dskResult{rows: int64(len(matched))}, nil
	}
	if !strings.Contains(lower, "update ") {
		return nil, fmt.Errorf("dskState: unsupported exec: %s", query)
	}
	return c.handleUpdate(table, lower, args)
}

func (c *dskConn) handleUpdate(table, lower string, args []driver.NamedValue) (driver.Result, error) {
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("dskState: malformed update: %s", lower)
	}
	type setter struct {
		idx int
		val driver.Value
	}
	var setters []setter
	for _, pair := range strings.Split(lower[setStart+len(" set "):whereStart], ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("dskState: malformed SET pair %q", pair)
		}
		idx := c.s.colIndex(table, dskColumnOf(pair[:eq]))
		if idx < 0 {
			return nil, fmt.Errorf("dskState: unknown column in SET %q", pair)
		}
		tok := strings.TrimSpace(pair[eq+1:])
		if !strings.HasPrefix(tok, "$") {
			return nil, fmt.Errorf("dskState: unsupported SET expression %q", tok)
		}
		n, _ := strconv.Atoi(tok[1:])
		setters = append(setters, setter{idx: idx, val: dskArg(args, n)})
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	for _, row := range matched {
		for _, st := range setters {
			row[st.idx] = st.val
		}
	}
	return dskResult{rows: int64(len(matched))}, nil
}

func (s *dskState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
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
		switch {
		case strings.HasSuffix(cond, " is null"):
			idx := s.colIndex(table, dskColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
		default:
			eq := strings.Index(cond, " = ")
			if eq < 0 {
				return nil, fmt.Errorf("dskState: unsupported condition %q", cond)
			}
			idx := s.colIndex(table, dskColumnOf(cond[:eq]))
			n, err := strconv.Atoi(strings.TrimSpace(cond[eq+3:])[1:])
			if err != nil {
				return nil, fmt.Errorf("dskState: bad bindvar in %q", cond)
			}
			want := dskArg(args, n)
			preds = append(preds, func(row []driver.Value) bool { return dskEqual(row[idx], want) })
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

func dskColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func dskArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("dskState: no arg with ordinal %d", n))
}

func dskEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if ta, ok := a.(time.Time); ok {
		tb, ok2 := b.(time.Time)
		return ok2 && ta.Equal(tb)
	}
	if sa, ok := a.(string); ok {
		sb, ok2 := b.(string)
		if !ok2 {
			sb = fmt.Sprint(b)
		}
		return sa == sb
	}
	return a == b
}

// dskBindToken parses a leading `$N` bindvar token.
func dskBindToken(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$") {
		return 0, fmt.Errorf("dskState: expected a bindvar, got %q", s)
	}
	end := 1
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return strconv.Atoi(s[1:end])
}

// ---------- fixtures + server ----------

const dskTestRSAKey = `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEAnvqA5Xb5LyYGhnSDZaXgWXD+HHAtoPaJa3FIqgQT0wiv8yAq
EHiHvlaklDPuFuhKoFX4oRSmwTJFgiBikAQDrE8YVyBK/bB7oR5Mtn/6o9I/0zqu
JgzWVgqtHwDBJT8KftjsUOBG7AV3OUMe+DFJfz1XMbe+wrhVwaiyDlJ8EeKwOWhw
1fS0CYzrGH21/GD5LgweNk7Xi+WUZTmy6xXX9Stk9R4P9BVKgEDgMatU/4u5Dsdq
KICOH7IbyqyvD/gz+b369GswXxdMiterEuvMOTp5/Ke4hXkFOHvyC5sZkBvFD7i4
i4deCobm1492E/5PR55CCib31hvJ568Wi1wM8QIDAQABAoIBAB6YDp4aGzIbmz3c
xjyVg2sy93P5ko5bY26tylINntMJ5PJgyjeJOnZCrkFnDPaTlWjton89PioHxqCY
It+cJijOv9xkdooDoIcNEa49SrqkB2xa6pCf/89nFpHUz1afnyFyFWgrAPsCwkLe
y+LveTlnUJRcy/UlDDvMfxeKgB3guE0+VLB12fA94B2iOa06YX0V7nhAYZ9biE2a
fjCKM66NVR6ysh2ElZcCU59hLf/yyBDH+6xcZ1so/1kCF7/J5dpz5nkEPr6rEd3s
MNpMtX4aOw1MdZ1FXOcRPZJnUDaDuvmXFSgtdHM3yj95daPlDLj0EkjLLgBpZ2L0
+ga/+5ECgYEA1S7lnjmfRrORyl9L9kJxmyR1HwXaUEWNmoEn9k07DRFAO/ToroeB
BGk5t5jZv84H1hi+r2WUO9bkmM1U34UL82pZv46B2u/ZjeNcNqgTowK9pCGlO9Cl
QFyxrUrhQfTvOKqu6zNWbD8j2bHw5GG6apu0Z38bInbhTZwmi34URScCgYEAvuiZ
rgRzvpEskRqT7LfSprcw8mXSbhxDMp8UgGQzNOmU2/LQxDsoywKmfG8mkDfASyPT
YHfmUaTJZrGkcOpKZvUCS4lrKlfm8Ge0UHrOvzMVSp6xj208dpSKcDKzx+YdqPKY
pStR5Vu+CdU+qHvkj4qG+gc9tcvgSOSS6nVo3CcCgYEApww6jYOllWc8hJGsGmny
T5l/1TFm4qO91CYExHG8Jf8wkErW0XwI0wYJXh598lhlt5RoR1SXlVRMiJfNO+Er
fjAF556pjn+HwDE6nvGRgZiyNZzPt1FFTGvWJm6YmHBvy72jl1YBEI1hcEKI+szB
oKwl7zugbZ8t6PzFD2VaSN0CgYAmOuYrlvCmxOOr2yqsSNOCV0USBZnyOu6qAHlW
jLpEalJJCYMNCNvOa6VzXb7XOpLjeTUnKFuTFfDPA9vuULy9iWsAPXCcdiXlFp9h
TnaAi3ZEDyt3kWR+Spi+hyjBay+dWZd/3AD2cTzJM07s5oKKzC7jeve8pCsx7bQO
B50MGQKBgQCsNOeRQ3nZyTRcEt9Gq2sdSQgvStCrdaS7zY6KcZzhcqOxcgTb7GpK
pZsgk1fSG8HwuVd6FR73iaWPFBCGPSIbpofUR+tz1szw4o4xYQEwepxRbkkE4vjL
xNoKUSDg9uZ7jvVqi5vj0RMx+Pz9Z2MnMaPrTr3Bce8qo3EJMPDY4g==
-----END RSA PRIVATE KEY-----
`

func newSigningServer(t *testing.T) (*okapi.TestServer, *dskState) {
	t.Helper()
	state := newDskState()
	gdb := dskGorm(t, state)
	h := NewDomainSigningHandler(
		repositories.NewDomainSigningKeyRepository(gdb),
		repositories.NewDomainRepository(gdb),
		audit.NewLogger(eventbus.New(repositories.NewEventRepository(gdb))),
	)
	o := okapi.NewTestServer(t)
	auth := middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil)
	g := o.Group("/workspaces/current", auth, testWorkspaceBinding)
	g.Post("/domains/{id:int}/signing-keys", okapi.H(h.Create))
	g.Get("/domains/{id:int}/signing-keys", okapi.H(h.List))
	g.Post("/domains/{id:int}/signing-keys/rotate", okapi.H(h.Rotate))
	g.Post("/domains/{id:int}/signing-keys/{keyId:int}/retire", okapi.H(h.Retire))
	g.Post("/domains/{id:int}/signing-keys/{keyId:int}/verify-dns", okapi.H(h.VerifyDNS))
	g.Delete("/domains/{id:int}/signing-keys/{keyId:int}", okapi.H(h.Delete))
	return o, state
}

func doSigningReq(t *testing.T, o *okapi.TestServer, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
	req.Header.Set("X-Test-Workspace", dskTestWorkspace)
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

// seedDomain adds a verified domain owned by the test scope.
func seedDomain(t *testing.T, s *dskState, _ string, workspaceID *uint, userID uint) {
	t.Helper()
	vals := map[string]any{
		"id": uint(10), "user_id": userID, "domain": "example.com",
		"ownership_verified": true, "verification_token": "tok",
		"created_at": time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
	}
	if workspaceID != nil {
		vals["workspace_id"] = workspaceID
	}
	dskSeed(t, s, "domains", vals)
}

func wsID(v uint) *uint { return &v }

func decodeSigningResponse(t *testing.T, rec *httptest.ResponseRecorder) (SigningKeyView, []byte) {
	t.Helper()
	var res struct {
		Success bool           `json:"success"`
		Data    SigningKeyView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response %s: %v", rec.Body.String(), err)
	}
	return res.Data, rec.Body.Bytes()
}

// ---------- tests ----------

func TestSigningKeyGenerate(t *testing.T) {
	o, state := newSigningServer(t)
	seedDomain(t, state, "example.com", wsID(3), 7)

	rec := doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys",
		`{"selector":"posta","algorithm":"rsa-sha256"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("generate = %d: %s", rec.Code, rec.Body.String())
	}
	view, raw := decodeSigningResponse(t, rec)
	if view.Status != models.DomainSigningStatusActive || view.DNSState != models.DomainSigningDNSPending {
		t.Fatalf("new key lifecycle wrong: %+v", view)
	}
	if view.Selector != "posta" || view.Algorithm != models.SigningAlgorithmRSA {
		t.Fatalf("selector/algorithm wrong: %+v", view)
	}
	if view.DNSRecord == nil || view.DNSRecord.Host != "posta._domainkey.example.com" {
		t.Fatalf("dns record wrong: %+v", view.DNSRecord)
	}
	if !strings.Contains(view.DNSRecord.Value, "v=DKIM1") || !strings.Contains(view.DNSRecord.Value, "k=rsa; p=") {
		t.Fatalf("dns record value not a DKIM RSA record: %q", view.DNSRecord.Value)
	}
	// The private key must never appear in any response.
	if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "MIIEpAIBAAKCAQEAnvqA5Xb5") {
		t.Fatal("response leaks private key material")
	}
	// The stored row keeps an encrypted private key that is not the plaintext.
	rows := state.rowsOf("domain_signing_keys")
	if len(rows) != 1 {
		t.Fatalf("expected 1 key row, got %d", len(rows))
	}
	stored, _ := rows[0]["private_key_pem"].(string)
	if stored == "" || strings.Contains(stored, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("stored private key must not be plaintext PEM: %q", stored)
	}
	// Ownership mirrors the domain.
	if rows[0]["user_id"] != int64(7) || rows[0]["workspace_id"] != int64(3) {
		t.Fatalf("key ownership wrong: %+v", rows[0])
	}
	if got := state.eventTypes(); len(got) != 1 || got[0] != "domain.signing_key.created" {
		t.Fatalf("generate must write one audit event, got %v", got)
	}
}

func TestSigningKeyGenerateConflictWhileActiveExists(t *testing.T) {
	o, state := newSigningServer(t)
	seedDomain(t, state, "example.com", wsID(3), 7)
	dskSeed(t, state, "domain_signing_keys", map[string]any{
		"user_id": uint(7), "workspace_id": wsID(3), "domain_id": uint(10),
		"selector": "posta", "algorithm": models.SigningAlgorithmRSA,
		"private_key_pem": "enc:xxx", "status": models.DomainSigningStatusActive,
		"dns_state": models.DomainSigningDNSPublished,
	})

	rec := doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys",
		`{"selector":"s2","algorithm":"rsa-sha256"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("generate with an active key = %d, want 409", rec.Code)
	}
}

func TestSigningKeyListHasNoPrivateMaterial(t *testing.T) {
	o, state := newSigningServer(t)
	seedDomain(t, state, "example.com", wsID(3), 7)
	enc, err := postacrypto.Encrypt(dskTestRSAKey)
	if err != nil {
		t.Fatalf("encrypt fixture: %v", err)
	}
	dskSeed(t, state, "domain_signing_keys", map[string]any{
		"user_id": uint(7), "workspace_id": wsID(3), "domain_id": uint(10),
		"selector": "posta", "algorithm": models.SigningAlgorithmRSA,
		"private_key_pem": enc, "status": models.DomainSigningStatusActive,
		"dns_state": models.DomainSigningDNSPublished,
	})

	rec := doSigningReq(t, o, http.MethodGet, "/workspaces/current/domains/10/signing-keys", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "BEGIN RSA") || strings.Contains(rec.Body.String(), enc) {
		t.Fatal("list response leaks private key material (plaintext or ciphertext)")
	}
	var res struct {
		Data []SigningKeyView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Data) != 1 || res.Data[0].Selector != "posta" {
		t.Fatalf("list wrong: %+v", res.Data)
	}
	if res.Data[0].DNSRecord == nil || res.Data[0].DNSRecord.Value == "" {
		t.Fatal("list must include the public DNS record for display")
	}
}

func TestSigningKeyRotation(t *testing.T) {
	o, state := newSigningServer(t)
	seedDomain(t, state, "example.com", wsID(3), 7)
	enc, _ := postacrypto.Encrypt(dskTestRSAKey)
	dskSeed(t, state, "domain_signing_keys", map[string]any{
		"id": uint(501), "user_id": uint(7), "workspace_id": wsID(3), "domain_id": uint(10),
		"selector": "posta", "algorithm": models.SigningAlgorithmRSA,
		"private_key_pem": enc, "status": models.DomainSigningStatusActive,
		"dns_state": models.DomainSigningDNSPublished,
	})

	rec := doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys/rotate",
		`{"selector":"mail2026","algorithm":"ed25519-sha256"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("rotate = %d: %s", rec.Code, rec.Body.String())
	}
	view, _ := decodeSigningResponse(t, rec)
	if view.Selector != "mail2026" || view.Status != models.DomainSigningStatusActive {
		t.Fatalf("replacement key wrong: %+v", view)
	}

	rows := state.rowsOf("domain_signing_keys")
	if len(rows) != 2 {
		t.Fatalf("rotation must produce 2 keys, got %d", len(rows))
	}
	statuses := map[string]string{}
	for _, r := range rows {
		statuses[r["selector"].(string)] = r["status"].(string)
	}
	if statuses["posta"] != models.DomainSigningStatusRetiring {
		t.Fatalf("old key must be retiring: %v", statuses)
	}
	if statuses["mail2026"] != models.DomainSigningStatusActive {
		t.Fatalf("replacement must be active: %v", statuses)
	}
	if got := state.eventTypes(); len(got) != 1 || got[0] != "domain.signing_key.rotated" {
		t.Fatalf("rotate must write one audit event, got %v", got)
	}

	// Rotating to the same selector is refused.
	rec = doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys/rotate",
		`{"selector":"mail2026"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same-selector rotate = %d, want 400", rec.Code)
	}
}

func TestSigningKeyVerifyDNS(t *testing.T) {
	o, state := newSigningServer(t)
	seedDomain(t, state, "example.com", wsID(3), 7)
	enc, _ := postacrypto.Encrypt(dskTestRSAKey)
	dskSeed(t, state, "domain_signing_keys", map[string]any{
		"id": uint(502), "user_id": uint(7), "workspace_id": wsID(3), "domain_id": uint(10),
		"selector": "posta", "algorithm": models.SigningAlgorithmRSA,
		"private_key_pem": enc, "status": models.DomainSigningStatusActive,
		"dns_state": models.DomainSigningDNSPending,
	})

	prevLookup := email.TestLookupTXT
	email.TestLookupTXT = func(host string) ([]string, error) {
		if host == "posta._domainkey.example.com" {
			return []string{"v=DKIM1; h=sha256; k=rsa; p=MIIBIjANBg"}, nil
		}
		return nil, errors.New("NXDOMAIN")
	}
	t.Cleanup(func() { email.TestLookupTXT = prevLookup })

	rec := doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys/502/verify-dns", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("verify-dns = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Data struct {
			Published bool           `json:"published"`
			Key       SigningKeyView `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Data.Published || res.Data.Key.DNSState != models.DomainSigningDNSPublished {
		t.Fatalf("published verification wrong: %+v", res.Data)
	}
	rows := state.rowsOf("domain_signing_keys")
	if rows[0]["dns_state"] != models.DomainSigningDNSPublished {
		t.Fatalf("dns_state not persisted: %+v", rows[0])
	}
	if rows[0]["dns_published_at"] == nil {
		t.Fatal("dns_published_at must be stamped")
	}

	// A failed check records pending-retry, not an error status.
	email.TestLookupTXT = func(string) ([]string, error) { return nil, errors.New("NXDOMAIN") }
	rec = doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys/502/verify-dns", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("failed verify-dns = %d: %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Data.Published || res.Data.Key.DNSState != models.DomainSigningDNSPending {
		t.Fatalf("failed verification must report pending-retry: %+v", res.Data)
	}
	rows = state.rowsOf("domain_signing_keys")
	if rows[0]["dns_state"] != models.DomainSigningDNSPending {
		t.Fatalf("failed check must persist pending-retry: %+v", rows[0])
	}
}

// The full manual rotation tail (R-02): a retiring key is retired through the
// retire endpoint once receivers no longer see its signatures, and a retired
// key is then deletable — no step of the documented lifecycle requires
// hand-editing a database row.
func TestSigningKeyRetireLifecycle(t *testing.T) {
	o, state := newSigningServer(t)
	seedDomain(t, state, "example.com", wsID(3), 7)
	enc, _ := postacrypto.Encrypt(dskTestRSAKey)
	dskSeed(t, state, "domain_signing_keys", map[string]any{
		"id": uint(505), "user_id": uint(7), "workspace_id": wsID(3), "domain_id": uint(10),
		"selector": "posta", "algorithm": models.SigningAlgorithmRSA,
		"private_key_pem": enc, "status": models.DomainSigningStatusRetiring,
		"dns_state": models.DomainSigningDNSPublished,
	})

	rec := doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys/505/retire", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retire retiring key = %d: %s", rec.Code, rec.Body.String())
	}
	view, _ := decodeSigningResponse(t, rec)
	if view.ID != 505 || view.Status != models.DomainSigningStatusRetired {
		t.Fatalf("retire response wrong: %+v", view)
	}
	rows := state.rowsOf("domain_signing_keys")
	if len(rows) != 1 || rows[0]["status"] != models.DomainSigningStatusRetired {
		t.Fatalf("retire must persist status=retired: %+v", rows)
	}

	// A retired key can no longer retire (it is already done)…
	if rec := doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys/505/retire", ""); rec.Code != http.StatusConflict {
		t.Fatalf("re-retire = %d, want 409", rec.Code)
	}
	// …but it is now deletable.
	if rec := doSigningReq(t, o, http.MethodDelete, "/workspaces/current/domains/10/signing-keys/505", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete retired key = %d, want 204", rec.Code)
	}
	if rows := state.rowsOf("domain_signing_keys"); len(rows) != 0 {
		t.Fatalf("retired key must be gone: %+v", rows)
	}
	if got := state.eventTypes(); len(got) != 2 || got[0] != "domain.signing_key.deleted" || got[1] != "domain.signing_key.retired" {
		t.Fatalf("retire+delete must write one audit event each, got %v", got)
	}
}

// An Active key cannot be retired directly — the rotation endpoint is the only
// path that demotes it, so a signing key can never be retired out from under
// live mail.
func TestSigningKeyRetireRejectsActiveKey(t *testing.T) {
	o, state := newSigningServer(t)
	seedDomain(t, state, "example.com", wsID(3), 7)
	enc, _ := postacrypto.Encrypt(dskTestRSAKey)
	dskSeed(t, state, "domain_signing_keys", map[string]any{
		"id": uint(506), "user_id": uint(7), "workspace_id": wsID(3), "domain_id": uint(10),
		"selector": "posta", "algorithm": models.SigningAlgorithmRSA,
		"private_key_pem": enc, "status": models.DomainSigningStatusActive,
		"dns_state": models.DomainSigningDNSPublished,
	})

	rec := doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys/506/retire", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("retire active key = %d, want 409", rec.Code)
	}
	rows := state.rowsOf("domain_signing_keys")
	if len(rows) != 1 || rows[0]["status"] != models.DomainSigningStatusActive {
		t.Fatalf("active key must be untouched: %+v", rows)
	}
	if got := state.eventTypes(); len(got) != 0 {
		t.Fatalf("refused retire must not write an audit event, got %v", got)
	}
}

// A key of a foreign domain is not reachable through the retire endpoint.
func TestSigningKeyRetireForeignDomainNotFound(t *testing.T) {
	o, state := newSigningServer(t)
	seedDomain(t, state, "example.com", wsID(99), 42)
	enc, _ := postacrypto.Encrypt(dskTestRSAKey)
	dskSeed(t, state, "domain_signing_keys", map[string]any{
		"id": uint(507), "user_id": uint(42), "workspace_id": wsID(99), "domain_id": uint(10),
		"selector": "posta", "algorithm": models.SigningAlgorithmRSA,
		"private_key_pem": enc, "status": models.DomainSigningStatusRetiring,
		"dns_state": models.DomainSigningDNSPublished,
	})

	rec := doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys/507/retire", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign retire = %d, want 404", rec.Code)
	}
	if got := state.eventTypes(); len(got) != 0 {
		t.Fatalf("404 retire must not write an audit event, got %v", got)
	}
}

func TestSigningKeyDeleteLifecycle(t *testing.T) {
	o, state := newSigningServer(t)
	seedDomain(t, state, "example.com", wsID(3), 7)
	enc, _ := postacrypto.Encrypt(dskTestRSAKey)
	dskSeed(t, state, "domain_signing_keys", map[string]any{
		"id": uint(503), "user_id": uint(7), "workspace_id": wsID(3), "domain_id": uint(10),
		"selector": "posta", "algorithm": models.SigningAlgorithmRSA,
		"private_key_pem": enc, "status": models.DomainSigningStatusRetiring,
		"dns_state": models.DomainSigningDNSPublished,
	})

	// Retiring keys still verify in-transit mail: not deletable.
	if rec := doSigningReq(t, o, http.MethodDelete, "/workspaces/current/domains/10/signing-keys/503", ""); rec.Code != http.StatusConflict {
		t.Fatalf("delete retiring key = %d, want 409", rec.Code)
	}

	// Retire the key through the endpoint (what the operator does from the UI
	// once receivers no longer see the old signature) — now deletion succeeds.
	if rec := doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys/503/retire", ""); rec.Code != http.StatusOK {
		t.Fatalf("retire key = %d: %s", rec.Code, rec.Body.String())
	}

	if rec := doSigningReq(t, o, http.MethodDelete, "/workspaces/current/domains/10/signing-keys/503", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete retired key = %d, want 204", rec.Code)
	}
	if rows := state.rowsOf("domain_signing_keys"); len(rows) != 0 {
		t.Fatalf("retired key must be gone: %+v", rows)
	}
}

// A domain outside the caller's scope is indistinguishable from a missing one.
func TestSigningKeyForeignDomainNotFound(t *testing.T) {
	o, state := newSigningServer(t)
	// Same workspace id but a different owner is still in-scope (workspace
	// semantics); use a foreign workspace instead.
	seedDomain(t, state, "example.com", wsID(99), 42)

	rec := doSigningReq(t, o, http.MethodGet, "/workspaces/current/domains/10/signing-keys", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign domain = %d, want 404", rec.Code)
	}
	rec = doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys/rotate", `{"selector":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign domain rotate = %d, want 404", rec.Code)
	}
}

func TestSigningKeyValidation(t *testing.T) {
	o, state := newSigningServer(t)
	seedDomain(t, state, "example.com", wsID(3), 7)

	cases := []struct {
		name string
		body string
		code int
	}{
		{"bad selector", `{"selector":"has space"}`, http.StatusBadRequest},
		{"bad algorithm", `{"selector":"posta","algorithm":"dsa-sha1"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if rec := doSigningReq(t, o, http.MethodPost, "/workspaces/current/domains/10/signing-keys", tc.body); rec.Code != tc.code {
			t.Fatalf("%s: status = %d, want %d", tc.name, rec.Code, tc.code)
		}
	}
	if rows := state.rowsOf("domain_signing_keys"); len(rows) != 0 {
		t.Fatalf("rejected requests must not store keys: %+v", rows)
	}
}
