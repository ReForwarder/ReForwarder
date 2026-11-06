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

package repositories

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// R-18 tests: the API-key usage aggregation. The fake database implements the
// upsert semantics the Bump path relies on — ON CONFLICT (api_key_id, day)
// DO UPDATE adding into the existing counters — so "concurrent bumps are
// additive, not last-write-wins" is pinned at the SQL level, alongside the
// zero-padded trailing window and the window totals.

type akuRow struct {
	id       int64
	apiKeyID int64
	day      time.Time
	requests int64
	sends    int64
}

type akuState struct {
	mu    sync.Mutex
	rows  []akuRow
	seq   int64
	calls []string
}

func newAkuState() *akuState { return &akuState{seq: 40} }

func akuGorm(t *testing.T, state *akuState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(akuConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open api key usage fake gorm: %v", err)
	}
	return gdb
}

func (s *akuState) rowCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

func akuDay(t time.Time) time.Time { return t.UTC().Truncate(24 * time.Hour) }

type akuConnector struct{ state *akuState }

func (c akuConnector) Connect(context.Context) (driver.Conn, error) { return &akuConn{s: c.state}, nil }
func (c akuConnector) Driver() driver.Driver                        { return akuDrv{} }

type akuDrv struct{}

func (akuDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type akuConn struct{ s *akuState }

func (c *akuConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *akuConn) Close() error                        { return nil }
func (c *akuConn) Begin() (driver.Tx, error)           { return akuTx{}, nil }

type akuTx struct{}

func (akuTx) Commit() error   { return nil }
func (akuTx) Rollback() error { return nil }

type akuResult struct{ rows int64 }

func (r akuResult) LastInsertId() (int64, error) { return 0, nil }
func (r akuResult) RowsAffected() (int64, error) { return r.rows, nil }

type akuRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *akuRows) Columns() []string { return r.cols }
func (r *akuRows) Close() error      { return nil }
func (r *akuRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

var (
	akuReRequests = regexp.MustCompile(`"requests"=api_key_usage_daily\.requests\s*\+\s*\$(\d+)`)
	akuReSends    = regexp.MustCompile(`"sends"=api_key_usage_daily\.sends\s*\+\s*\$(\d+)`)
)

func (c *akuConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	c.s.mu.Lock()
	c.s.calls = append(c.s.calls, lower)
	c.s.mu.Unlock()

	if !strings.Contains(lower, "api_key_usage_daily") {
		return nil, fmt.Errorf("akuState: cannot route query: %s", query)
	}
	if strings.Contains(lower, "insert into") {
		return c.handleUpsert(lower, query, args)
	}
	// Reads: predicates api_key_id = $n and day >= $m.
	preds, err := c.akuPreds(lower, args)
	if err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := make([]akuRow, 0, len(s.rows))
	for _, r := range s.rows {
		ok := true
		for _, p := range preds {
			if !p(r) {
				ok = false
				break
			}
		}
		if ok {
			matched = append(matched, r)
		}
	}
	if strings.Contains(lower, "coalesce(sum(requests)") {
		var requests, sends int64
		for _, r := range matched {
			requests += r.requests
			sends += r.sends
		}
		return &akuRows{cols: []string{"requests", "sends"},
			rows: [][]driver.Value{{requests, sends}}}, nil
	}
	// Plain row select: day DESC (the repo relies on it for the window).
	sort.SliceStable(matched, func(a, b int) bool { return matched[a].day.After(matched[b].day) })
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = []driver.Value{r.id, r.apiKeyID, r.day, r.requests, r.sends}
	}
	return &akuRows{cols: []string{"id", "api_key_id", "day", "requests", "sends"}, rows: rows}, nil
}

func (c *akuConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.QueryContext(ctx, query, args); err != nil {
		return nil, err
	}
	return akuResult{}, nil
}

// handleUpsert implements INSERT ... ON CONFLICT (api_key_id, day) DO UPDATE
// SET requests = requests + $n, sends = sends + $m: existing rows accumulate,
// new rows are inserted.
func (c *akuConn) handleUpsert(lower, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(lower, "on conflict") || !strings.Contains(lower, "do update set") {
		return nil, fmt.Errorf("akuState: bump must upsert, got: %s", query)
	}
	mr := akuReRequests.FindStringSubmatch(lower)
	ms := akuReSends.FindStringSubmatch(lower)
	if mr == nil || ms == nil {
		return nil, fmt.Errorf("akuState: DO UPDATE must add into requests and sends: %s", query)
	}
	reqArg, err := strconv.Atoi(mr[1])
	if err != nil {
		return nil, err
	}
	deltaReq, _ := akuArg(args, reqArg).(int64)
	sendArg, err := strconv.Atoi(ms[1])
	if err != nil {
		return nil, err
	}
	deltaSend, _ := akuArg(args, sendArg).(int64)

	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("akuState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, fmt.Errorf("akuState: insert without a column list")
	}
	var cols []string
	for _, p := range strings.Split(query[open+1:close], ",") {
		cols = append(cols, strings.Trim(strings.TrimSpace(p), `"`))
	}
	valText := query[vi+len(" values "):]
	if i := strings.Index(strings.ToLower(valText), " on conflict"); i >= 0 {
		valText = valText[:i]
	}
	valText = strings.TrimSpace(valText)
	valText = strings.TrimSuffix(strings.TrimPrefix(valText, "("), ")")
	parts := strings.Split(valText, ",")
	if len(parts) != len(cols) {
		return nil, fmt.Errorf("akuState: %d columns but %d values", len(cols), len(parts))
	}
	vals := map[string]driver.Value{}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if !strings.HasPrefix(p, "$") {
			return nil, fmt.Errorf("akuState: non-bindvar value %q", p)
		}
		n, err := strconv.Atoi(p[1:])
		if err != nil {
			return nil, err
		}
		vals[cols[i]] = akuArg(args, n)
	}
	keyID, ok1 := vals["api_key_id"].(int64)
	day, ok2 := vals["day"].(time.Time)
	req, _ := vals["requests"].(int64)
	send, _ := vals["sends"].(int64)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("akuState: upsert without api_key_id/day: %v", vals)
	}

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].apiKeyID == keyID && s.rows[i].day.Equal(day) {
			s.rows[i].requests += deltaReq
			s.rows[i].sends += deltaSend
			return &akuRows{cols: []string{"id"}, rows: [][]driver.Value{{s.rows[i].id}}}, nil
		}
	}
	s.seq++
	s.rows = append(s.rows, akuRow{id: s.seq, apiKeyID: keyID, day: day, requests: req, sends: send})
	return &akuRows{cols: []string{"id"}, rows: [][]driver.Value{{s.seq}}}, nil
}

// akuPreds parses the equality and >= predicates the usage reads issue.
func (c *akuConn) akuPreds(lower string, args []driver.NamedValue) ([]func(akuRow) bool, error) {
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = lower[i+len(" where "):]
		for _, cut := range []string{" order by ", " limit "} {
			if j := strings.Index(where, cut); j >= 0 {
				where = where[:j]
			}
		}
	}
	var preds []func(akuRow) bool
	for _, cond := range strings.Split(strings.NewReplacer("(", " ", ")", " ").Replace(where), " and ") {
		cond = strings.TrimSpace(cond)
		if cond == "" {
			continue
		}
		op := " = "
		if i := strings.Index(cond, " >= "); i >= 0 {
			op = " >= "
			cond = cond[:i] + op + strings.TrimSpace(cond[i+len(" >= "):])
		}
		eq := strings.Index(cond, op)
		if eq < 0 || !strings.HasPrefix(strings.TrimSpace(cond[eq+len(op):]), "$") {
			return nil, fmt.Errorf("akuState: unsupported condition %q", cond)
		}
		col := strings.Trim(strings.TrimSpace(cond[:eq]), `"`)
		n := akuOrdinal(strings.TrimSpace(cond[eq+len(op):]))
		val := akuArg(args, n)
		switch col {
		case "api_key_id":
			key, _ := val.(int64)
			preds = append(preds, func(r akuRow) bool { return r.apiKeyID == key })
		case "day":
			day, _ := val.(time.Time)
			if op == " >= " {
				preds = append(preds, func(r akuRow) bool { return !r.day.Before(day) })
			} else {
				preds = append(preds, func(r akuRow) bool { return r.day.Equal(day) })
			}
		default:
			return nil, fmt.Errorf("akuState: unsupported column %q", col)
		}
	}
	return preds, nil
}

func akuOrdinal(tok string) int {
	tok = strings.TrimSpace(tok)
	n, err := strconv.Atoi(tok[1:])
	if err != nil {
		panic(fmt.Sprintf("akuState: bad bindvar %q", tok))
	}
	return n
}

func akuArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("akuState: no arg with ordinal %d", n))
}

// ---------- tests ----------

// Bumps accumulate: two bumps on the same key and day land in ONE row whose
// counters are the sum (the ON CONFLICT DO UPDATE is additive, never
// last-write-wins), while a different key gets its own row.
func TestAPIKeyUsageBumpIsAdditiveUpsert(t *testing.T) {
	state := newAkuState()
	repo := NewAPIKeyUsageRepository(akuGorm(t, state))

	if err := repo.Bump(11, 3, 1); err != nil {
		t.Fatalf("first bump: %v", err)
	}
	if err := repo.Bump(11, 2, 0); err != nil {
		t.Fatalf("second bump: %v", err)
	}
	if err := repo.Bump(12, 1, 1); err != nil {
		t.Fatalf("other key bump: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.rows) != 2 {
		t.Fatalf("rows after three bumps = %d, want 2 (one per key)", len(state.rows))
	}
	for _, r := range state.rows {
		switch r.apiKeyID {
		case 11:
			if r.requests != 5 || r.sends != 1 {
				t.Errorf("key 11 row = requests %d, sends %d; want 5, 1 (additive)", r.requests, r.sends)
			}
		case 12:
			if r.requests != 1 || r.sends != 1 {
				t.Errorf("key 12 row = requests %d, sends %d; want 1, 1", r.requests, r.sends)
			}
		}
		if !r.day.Equal(akuDay(time.Now())) {
			t.Errorf("row day = %v, want today's UTC day %v", r.day, akuDay(time.Now()))
		}
	}
}

// A no-op bump (zero requests and sends) must not touch the database at all.
func TestAPIKeyUsageBumpZeroIsNoop(t *testing.T) {
	state := newAkuState()
	repo := NewAPIKeyUsageRepository(akuGorm(t, state))

	if err := repo.Bump(11, 0, 0); err != nil {
		t.Fatalf("zero bump: %v", err)
	}
	if got := state.rowCount(); got != 0 {
		t.Fatalf("zero bump created %d rows, want 0", got)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.calls) != 0 {
		t.Fatalf("zero bump issued %d statements, want none", len(state.calls))
	}
}

// List returns the trailing window zero-padded: days with no row appear with
// zero counts and the caller's key id, in ascending chronological order
// (oldest first, today last — the order the UI renders), and rows of other
// keys never leak in.
func TestAPIKeyUsageListZeroPadsTrailingWindow(t *testing.T) {
	state := newAkuState()
	repo := NewAPIKeyUsageRepository(akuGorm(t, state))
	today := akuDay(time.Now())

	seedAku := func(keyID uint, day time.Time, requests, sends int64) {
		state.mu.Lock()
		state.seq++
		state.rows = append(state.rows, akuRow{id: state.seq, apiKeyID: int64(keyID), day: day, requests: requests, sends: sends})
		state.mu.Unlock()
	}
	seedAku(11, today, 4, 2)
	seedAku(11, today.AddDate(0, 0, -3), 1, 0)
	seedAku(12, today.AddDate(0, 0, -1), 9, 9) // another key: must not leak

	rows, err := repo.List(11, 7)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("List = %d rows, want 7 (zero-padded window)", len(rows))
	}
	// Ascending chronological order: the oldest window day comes first.
	if !rows[0].Day.Equal(today.AddDate(0, 0, -6)) {
		t.Errorf("first row day = %v, want the window start %v", rows[0].Day, today.AddDate(0, 0, -6))
	}
	if !rows[6].Day.Equal(today) || rows[6].Requests != 4 || rows[6].Sends != 2 {
		t.Errorf("last row = %+v, want today with 4/2", rows[6])
	}
	third := rows[3]
	if !third.Day.Equal(today.AddDate(0, 0, -3)) || third.Requests != 1 {
		t.Errorf("row 3 = %+v, want today-3 with 1 request", third)
	}
	// Gap days are zero-filled with the requested key id.
	gap := rows[5] // today-1: only key 12 has a row there
	if gap.Requests != 0 || gap.Sends != 0 || gap.APIKeyID != 11 {
		t.Errorf("gap row = %+v, want zero counts for key 11", gap)
	}
	for i := 1; i < len(rows); i++ {
		if !rows[i].Day.After(rows[i-1].Day) {
			t.Fatalf("window must be ascending by day: %v then %v", rows[i-1].Day, rows[i].Day)
		}
	}

	// The default window is 30 days.
	rows30, err := repo.List(11, 0)
	if err != nil {
		t.Fatalf("List default: %v", err)
	}
	if len(rows30) != 30 {
		t.Fatalf("default List = %d rows, want 30", len(rows30))
	}
}

// Totals sums the counters over the window, other keys excluded, and yields
// zeros (COALESCE) for an empty window.
func TestAPIKeyUsageTotals(t *testing.T) {
	state := newAkuState()
	repo := NewAPIKeyUsageRepository(akuGorm(t, state))
	today := akuDay(time.Now())

	seedAku := func(keyID uint, day time.Time, requests, sends int64) {
		state.mu.Lock()
		state.seq++
		state.rows = append(state.rows, akuRow{id: state.seq, apiKeyID: int64(keyID), day: day, requests: requests, sends: sends})
		state.mu.Unlock()
	}
	seedAku(11, today, 4, 2)
	seedAku(11, today.AddDate(0, 0, -1), 1, 1)
	seedAku(11, today.AddDate(0, 0, -5), 100, 100) // outside the 3-day window
	seedAku(12, today, 9, 9)                       // another key

	totals, err := repo.Totals(11, today.AddDate(0, 0, -1))
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if totals.Requests != 5 || totals.Sends != 3 {
		t.Fatalf("totals = %+v, want requests 5, sends 3", totals)
	}

	empty, err := repo.Totals(99, today)
	if err != nil {
		t.Fatalf("Totals empty: %v", err)
	}
	if empty.Requests != 0 || empty.Sends != 0 {
		t.Fatalf("empty-window totals = %+v, want 0/0 (COALESCE)", empty)
	}
}
