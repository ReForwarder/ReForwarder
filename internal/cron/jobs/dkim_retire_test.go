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

package jobs

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
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The DKIM auto-retire job (SF-104) runs against the real gorm on a fake
// database/sql driver holding domains, domain_signing_keys, settings and
// events. Assertions pin the two preconditions — grace period elapsed AND the
// replacement key's DNS record verified published — the CAS-only transition
// through the existing repository method, and the audit event per transition.

var dkColumns = map[string][]string{
	// pool_id: OB-6 W3 additive column (domains.pool_id) — Save() on the model
	// emits it in every UPDATE's SET list, so the fake must know it.
	"domains": {"id", "user_id", "workspace_id", "domain", "ownership_verified", "spf_verified",
		"dkim_verified", "dmarc_verified", "verification_token", "delivery_mode", "outbound_bind_ip",
		"max_msgs_per_connection", "hourly_budget", "pool_id", "mta_sts_mode", "created_at"},
	"domain_signing_keys": {"id", "user_id", "workspace_id", "domain_id", "selector", "algorithm",
		"private_key_pem", "status", "dns_state", "dns_published_at", "created_at", "updated_at"},
	"settings": {"id", "key", "value", "type", "created_at", "updated_at"},
	"events": {"id", "category", "type", "workspace_id", "actor_id", "actor_name", "client_ip",
		"message", "metadata", "outcome", "target_type", "target_id", "created_at"},
}

var dkDefaults = map[string]map[string]func() driver.Value{
	"domains": {
		"workspace_id":  func() driver.Value { return nil },
		"delivery_mode": func() driver.Value { return models.DeliveryModeRelay },
		"created_at":    func() driver.Value { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	},
	"domain_signing_keys": {
		"workspace_id":     func() driver.Value { return nil },
		"algorithm":        func() driver.Value { return models.SigningAlgorithmRSA },
		"dns_state":        func() driver.Value { return models.DomainSigningDNSPending },
		"dns_published_at": func() driver.Value { return nil },
		"status":           func() driver.Value { return models.DomainSigningStatusActive },
	},
	"settings": {
		"type": func() driver.Value { return models.SettingTypeInt },
	},
}

type dkTable struct {
	columns []string
	rows    [][]driver.Value
}

type dkState struct {
	mu     sync.Mutex
	tables map[string]*dkTable
	seq    int64
}

func newDkState() *dkState {
	tables := make(map[string]*dkTable, len(dkColumns))
	for name, cols := range dkColumns {
		tables[name] = &dkTable{columns: cols}
	}
	return &dkState{tables: tables, seq: 500}
}

func (s *dkState) colIndex(table, col string) int {
	for i, c := range dkColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

// seed inserts a fixture row; unspecified columns take their defaults.
func dkSeed(t *testing.T, s *dkState, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	spec := dkColumns[table]
	row := make([]driver.Value, len(spec))
	for i, col := range spec {
		row[i] = nil
		if def, ok := dkDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for col, v := range vals {
		idx := s.colIndex(table, col)
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, col)
		}
		row[idx] = dkVal(v)
	}
	if row[s.colIndex(table, "id")] == nil {
		s.seq++
		row[s.colIndex(table, "id")] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func dkVal(v any) driver.Value {
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
		panic(fmt.Sprintf("dkVal: unsupported type %T", v))
	}
}

func (s *dkState) rowsOf(table string) []map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, 0, len(s.tables[table].rows))
	for _, row := range s.tables[table].rows {
		m := map[string]driver.Value{}
		for i, c := range dkColumns[table] {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

// statusesOf maps signing-key id → status.
func (s *dkState) statusesOf() map[int64]string {
	out := map[int64]string{}
	for _, m := range s.rowsOf("domain_signing_keys") {
		out[m["id"].(int64)] = m["status"].(string)
	}
	return out
}

// eventTypes lists the audit event types persisted by the run.
func (s *dkState) eventTypes() []string {
	var out []string
	for _, m := range s.rowsOf("events") {
		out = append(out, m["type"].(string))
	}
	return out
}

func dkGorm(t *testing.T, state *dkState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(dkConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open dkim-retire fake gorm: %v", err)
	}
	return gdb
}

type dkConnector struct{ state *dkState }

func (c dkConnector) Connect(context.Context) (driver.Conn, error) { return &dkConn{s: c.state}, nil }
func (c dkConnector) Driver() driver.Driver                        { return dkDrv{} }

type dkDrv struct{}

func (dkDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type dkConn struct{ s *dkState }

func (c *dkConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *dkConn) Close() error                        { return nil }
func (c *dkConn) Begin() (driver.Tx, error)           { return dkTx{}, nil }

type dkTx struct{}

func (dkTx) Commit() error   { return nil }
func (dkTx) Rollback() error { return nil }

type dkResult struct{ rows int64 }

func (r dkResult) LastInsertId() (int64, error) { return 0, nil }
func (r dkResult) RowsAffected() (int64, error) { return r.rows, nil }

type dkRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *dkRows) Columns() []string { return r.cols }
func (r *dkRows) Close() error      { return nil }
func (r *dkRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func dkTableOf(lower string) string {
	for _, name := range []string{"domain_signing_keys", "domains", "settings", "events"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`} {
			if strings.Contains(lower, pat) {
				return name
			}
		}
	}
	return ""
}

func (c *dkConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := dkTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("dkState: cannot route query: %s", query)
	}
	// gorm issues INSERT ... RETURNING (audit events) as a query.
	if strings.Contains(lower, "insert into") {
		return c.handleInsert(lower, table, query, args)
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(lower, "count(*)") {
		return &dkRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}
	matched = dkOrderBy(table, lower, matched)
	matched = dkLimitOffset(lower, args, matched)
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &dkRows{cols: dkColumns[table], rows: rows}, nil
}

func (c *dkConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := dkTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("dkState: unsupported exec: %s", query)
	}
	if strings.Contains(lower, "insert into") {
		return nil, fmt.Errorf("dkState: inserts with RETURNING must arrive via QueryContext: %s", query)
	}
	if !strings.Contains(lower, "update ") {
		return nil, fmt.Errorf("dkState: unsupported exec: %s", query)
	}
	return c.handleUpdate(table, lower, args)
}

func (c *dkConn) handleInsert(lower, table, query string, args []driver.NamedValue) (driver.Rows, error) {
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("dkState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, fmt.Errorf("dkState: insert without a column list")
	}
	colParts := strings.Split(query[open+1:close], ",")
	valText := query[vi+len(" values "):]
	end := strings.LastIndex(valText, ")")
	valParts := strings.Split(valText[1:end], ",")
	if len(valParts) != len(colParts) {
		return nil, fmt.Errorf("dkState: %d columns but %d values", len(colParts), len(valParts))
	}
	vals := make([]driver.Value, len(valParts))
	for i, p := range valParts {
		n, err := strconv.Atoi(strings.TrimSpace(p)[1:])
		if err != nil {
			return nil, err
		}
		vals[i] = dkArg(args, n)
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
	row := make([]driver.Value, len(dkColumns[table]))
	for i, col := range dkColumns[table] {
		row[i] = nil
		if def, ok := dkDefaults[table][col]; ok {
			row[i] = def()
		}
	}
	for j, col := range colParts {
		col = strings.Trim(strings.TrimSpace(col), `"`)
		idx := s.colIndex(table, col)
		if idx < 0 {
			return nil, fmt.Errorf("dkState: %s has no column %q", table, col)
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
	return &dkRows{cols: returning, rows: [][]driver.Value{out}}, nil
}

func (c *dkConn) handleUpdate(table, lower string, args []driver.NamedValue) (driver.Result, error) {
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("dkState: malformed update: %s", lower)
	}
	type setter struct {
		idx int
		val driver.Value
	}
	var setters []setter
	for _, pair := range strings.Split(lower[setStart+len(" set "):whereStart], ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("dkState: malformed SET pair %q", pair)
		}
		idx := c.s.colIndex(table, dkColumnOf(pair[:eq]))
		if idx < 0 {
			return nil, fmt.Errorf("dkState: unknown column in SET %q", pair)
		}
		tok := strings.TrimSpace(pair[eq+1:])
		if !strings.HasPrefix(tok, "$") {
			return nil, fmt.Errorf("dkState: unsupported SET expression %q", tok)
		}
		n, _ := strconv.Atoi(tok[1:])
		setters = append(setters, setter{idx: idx, val: dkArg(args, n)})
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
	return dkResult{rows: int64(len(matched))}, nil
}

func (s *dkState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
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
		if strings.HasSuffix(cond, " is null") {
			idx := s.colIndex(table, dkColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
			continue
		}
		eq := strings.Index(cond, " = ")
		if eq < 0 {
			return nil, fmt.Errorf("dkState: unsupported condition %q", cond)
		}
		idx := s.colIndex(table, dkColumnOf(cond[:eq]))
		n, err := strconv.Atoi(strings.TrimSpace(cond[eq+3:])[1:])
		if err != nil {
			return nil, fmt.Errorf("dkState: bad bindvar in %q", cond)
		}
		want := dkArg(args, n)
		preds = append(preds, func(row []driver.Value) bool { return dkEqual(row[idx], want) })
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

func dkOrderBy(table, lower string, rows [][]driver.Value) [][]driver.Value {
	i := strings.Index(lower, " order by ")
	if i < 0 {
		return rows
	}
	rest := lower[i+len(" order by "):]
	end := len(rest)
	if j := strings.Index(rest, " limit "); j >= 0 {
		end = j
	}
	type term struct {
		idx  int
		desc bool
	}
	var terms []term
	for _, part := range strings.Split(strings.TrimSpace(rest[:end]), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		desc := strings.HasSuffix(part, " desc")
		col := dkColumnOf(strings.TrimSuffix(strings.TrimSuffix(part, " desc"), " asc"))
		idx := -1
		for k, c := range dkColumns[table] {
			if c == col {
				idx = k
			}
		}
		if idx < 0 {
			continue
		}
		terms = append(terms, term{idx: idx, desc: desc})
	}
	if len(terms) == 0 {
		return rows
	}
	out := append([][]driver.Value(nil), rows...)
	for t := len(terms) - 1; t >= 0; t-- {
		idx, desc := terms[t].idx, terms[t].desc
		for a := 1; a < len(out); a++ {
			for b := a; b > 0 && dkCompare(out[b][idx], out[b-1][idx], desc); b-- {
				out[b], out[b-1] = out[b-1], out[b]
			}
		}
	}
	return out
}

func dkCompare(a, b driver.Value, before bool) bool {
	if av, ok := a.(int64); ok {
		if bv, ok2 := b.(int64); ok2 {
			if before {
				return av > bv
			}
			return av < bv
		}
	}
	as, _ := a.(string)
	bs, _ := b.(string)
	if before {
		return as > bs
	}
	return as < bs
}

func dkLimitOffset(lower string, args []driver.NamedValue, rows [][]driver.Value) [][]driver.Value {
	if m := strings.Index(lower, " limit "); m >= 0 {
		if n, err := dkBindToken(lower[m+len(" limit "):]); err == nil {
			if v, ok := dkArg(args, n).(int64); ok && int(v) < len(rows) {
				rows = rows[:int(v)]
			}
		}
	}
	return rows
}

func dkColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func dkArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("dkState: no arg with ordinal %d", n))
}

func dkEqual(a, b driver.Value) bool {
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

func dkBindToken(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$") {
		return 0, fmt.Errorf("dkState: expected a bindvar, got %q", s)
	}
	end := 1
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return strconv.Atoi(s[1:end])
}

// ---------- fixtures ----------

const dkGraceDefault = 14

// newDKIMJob builds the job over a fresh fake DB and seeds one verified
// domain. Returns the state so tests can pin statuses and events.
func newDKIMJob(t *testing.T, graceDays int) (*dkState, *DKIMRetireJob) {
	t.Helper()
	state := newDkState()
	gdb := dkGorm(t, state)

	if graceDays != dkGraceDefault {
		dkSeed(t, state, "settings", map[string]any{
			"id": uint(1), "key": "dkim_retire_grace_days",
			"value": strconv.Itoa(graceDays), "type": models.SettingTypeInt,
			"created_at": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		})
	}

	sp := settings.NewProvider(repositories.NewSettingRepository(gdb))
	return state, NewDKIMRetireJob(gdb, sp)
}

// seedRotation seeds a verified domain, a retiring key demoted ageAgo ago and
// its Active replacement in the given DNS state. Returns the retiring key id.
func seedRotation(t *testing.T, s *dkState, ageAgo time.Duration, replacementDNS string) int64 {
	t.Helper()
	dkSeed(t, s, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "domain": "example.com",
		"ownership_verified": true, "verification_token": "tok",
	})
	dkSeed(t, s, "domain_signing_keys", map[string]any{
		"id": uint(11), "user_id": uint(7), "domain_id": uint(10),
		"selector": "posta-2025", "status": models.DomainSigningStatusRetiring,
		"dns_state":  models.DomainSigningDNSPublished,
		"created_at": time.Now().Add(-60 * 24 * time.Hour).UTC(),
		"updated_at": time.Now().Add(-ageAgo).UTC(),
	})
	dkSeed(t, s, "domain_signing_keys", map[string]any{
		"id": uint(12), "user_id": uint(7), "domain_id": uint(10),
		"selector": "posta-2026", "status": models.DomainSigningStatusActive,
		"dns_state":  replacementDNS,
		"created_at": time.Now().Add(-2 * 24 * time.Hour).UTC(),
		"updated_at": time.Now().Add(-2 * 24 * time.Hour).UTC(),
	})
	return 11
}

func runDKIMRetire(t *testing.T, job *DKIMRetireJob) {
	t.Helper()
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("dkim retire run: %v", err)
	}
}

// ---------- tests ----------

// TestDKIMAutoRetireRetiresExpiredVerifiedKey pins the happy path: a retiring
// key past the grace period whose replacement is DNS-verified flips to
// retired via the existing repository method, and the transition is audited.
func TestDKIMAutoRetireRetiresExpiredVerifiedKey(t *testing.T) {
	state, job := newDKIMJob(t, dkGraceDefault)
	retiringID := seedRotation(t, state, 20*24*time.Hour, models.DomainSigningDNSPublished)

	runDKIMRetire(t, job)

	statuses := state.statusesOf()
	if statuses[retiringID] != models.DomainSigningStatusRetired {
		t.Errorf("retiring key id %d status = %q, want retired", retiringID, statuses[retiringID])
	}
	if statuses[12] != models.DomainSigningStatusActive {
		t.Errorf("replacement must stay active, got %q", statuses[12])
	}
	found := false
	for _, ev := range state.eventTypes() {
		if ev == "domain.dkim_key_auto_retired" {
			found = true
		}
	}
	if !found {
		t.Errorf("no domain.dkim_key_auto_retired audit event: %v", state.eventTypes())
	}
}

// TestDKIMAutoRetireKeepsKeysWithinGrace pins the grace precondition: a key
// demoted recently stays retiring.
func TestDKIMAutoRetireKeepsKeysWithinGrace(t *testing.T) {
	state, job := newDKIMJob(t, dkGraceDefault)
	retiringID := seedRotation(t, state, 2*24*time.Hour, models.DomainSigningDNSPublished)

	runDKIMRetire(t, job)

	if statuses := state.statusesOf(); statuses[retiringID] != models.DomainSigningStatusRetiring {
		t.Errorf("key within grace must stay retiring, got %q", statuses[retiringID])
	}
	if len(state.eventTypes()) != 0 {
		t.Errorf("no audit event expected, got %v", state.eventTypes())
	}
}

// TestDKIMAutoRetireRefusesUnverifiedReplacement pins the DNS precondition:
// the replacement's TXT record not yet verified blocks the transition.
func TestDKIMAutoRetireRefusesUnverifiedReplacement(t *testing.T) {
	state, job := newDKIMJob(t, dkGraceDefault)
	retiringID := seedRotation(t, state, 20*24*time.Hour, models.DomainSigningDNSPending)

	runDKIMRetire(t, job)

	if statuses := state.statusesOf(); statuses[retiringID] != models.DomainSigningStatusRetiring {
		t.Errorf("unverified replacement must keep the key retiring, got %q", statuses[retiringID])
	}
	if len(state.eventTypes()) != 0 {
		t.Errorf("no audit event expected, got %v", state.eventTypes())
	}
}

// TestDKIMAutoRetireRequiresActiveReplacement: with no Active key at all the
// retiring key is kept — a domain whose replacement vanished must not lose
// its last non-deletable record.
func TestDKIMAutoRetireRequiresActiveReplacement(t *testing.T) {
	state, job := newDKIMJob(t, dkGraceDefault)
	dkSeed(t, state, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "domain": "example.com",
		"ownership_verified": true, "verification_token": "tok",
	})
	dkSeed(t, state, "domain_signing_keys", map[string]any{
		"id": uint(11), "user_id": uint(7), "domain_id": uint(10),
		"selector": "posta-2025", "status": models.DomainSigningStatusRetiring,
		"dns_state":  models.DomainSigningDNSPublished,
		"updated_at": time.Now().Add(-20 * 24 * time.Hour).UTC(),
	})

	runDKIMRetire(t, job)

	if statuses := state.statusesOf(); statuses[11] != models.DomainSigningStatusRetiring {
		t.Errorf("key without replacement must stay retiring, got %q", statuses[11])
	}
}

// TestDKIMAutoRetireSkipsUnverifiedDomain: an unverified (non-enabled) domain
// is out of scope for the automated pass.
func TestDKIMAutoRetireSkipsUnverifiedDomain(t *testing.T) {
	state, job := newDKIMJob(t, dkGraceDefault)
	dkSeed(t, state, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "domain": "pending.example.com",
		"ownership_verified": false, "verification_token": "tok",
	})
	dkSeed(t, state, "domain_signing_keys", map[string]any{
		"id": uint(11), "user_id": uint(7), "domain_id": uint(10),
		"selector": "posta-2025", "status": models.DomainSigningStatusRetiring,
		"dns_state":  models.DomainSigningDNSPublished,
		"updated_at": time.Now().Add(-20 * 24 * time.Hour).UTC(),
	})

	runDKIMRetire(t, job)

	if statuses := state.statusesOf(); statuses[11] != models.DomainSigningStatusRetiring {
		t.Errorf("unverified domain's key must stay retiring, got %q", statuses[11])
	}
}

// TestDKIMAutoRetireHonorsConfiguredGrace pins the settings key:
// dkim_retire_grace_days = 0 retires an eligible key on the next run, and a
// negative value disables the pass entirely.
func TestDKIMAutoRetireHonorsConfiguredGrace(t *testing.T) {
	state, job := newDKIMJob(t, 0)
	retiringID := seedRotation(t, state, 2*time.Hour, models.DomainSigningDNSPublished)
	runDKIMRetire(t, job)
	if statuses := state.statusesOf(); statuses[retiringID] != models.DomainSigningStatusRetired {
		t.Errorf("grace=0 must retire on the next run, got %q", statuses[retiringID])
	}

	state2, job2 := newDKIMJob(t, -1)
	retiringID2 := seedRotation(t, state2, 30*24*time.Hour, models.DomainSigningDNSPublished)
	runDKIMRetire(t, job2)
	if statuses := state2.statusesOf(); statuses[retiringID2] != models.DomainSigningStatusRetiring {
		t.Errorf("negative grace must disable the pass, got %q", statuses[retiringID2])
	}
	if len(state2.eventTypes()) != 0 {
		t.Errorf("disabled pass must not audit events, got %v", state2.eventTypes())
	}
}

// TestDKIMAutoRetireIsIdempotent: a retired key is never touched again (the
// repository's CAS only matches status='retiring').
func TestDKIMAutoRetireIsIdempotent(t *testing.T) {
	state, job := newDKIMJob(t, dkGraceDefault)
	seedRotation(t, state, 20*24*time.Hour, models.DomainSigningDNSPublished)
	runDKIMRetire(t, job)
	events := len(state.eventTypes())
	runDKIMRetire(t, job)
	if got := len(state.eventTypes()); got != events {
		t.Errorf("second run must be a no-op: %d events before, %d after", events, got)
	}
}
