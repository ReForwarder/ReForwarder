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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The scheduler is exercised against the real CampaignRepository on a fake
// database/sql driver (the segment_handler_test.go arrangement): it stores
// campaign rows and evaluates the simple WHERE clauses the job triggers, so
// the tests pin actual SQL semantics — the `status = 'scheduled' AND
// scheduled_at <= now` filter and the RowsAffected-based atomic claim —
// rather than re-implementing them in a stub.

// ---------- the fake database ----------

var schedColumns = []string{
	"id", "user_id", "workspace_id", "name", "subject", "from_email", "from_name", "template_id",
	"template_version_id", "language", "template_data", "status", "list_id", "segment_id", "send_rate",
	"send_at_local_time", "ab_test_enabled", "ab_test_variants", "ab_test_winner", "scheduled_at",
	"started_at", "completed_at", "created_at", "updated_at", "deleted_at", "snapshot",
}

var schedDefaults = map[string]func() driver.Value{
	"status":             func() driver.Value { return models.CampaignStatusDraft },
	"language":           func() driver.Value { return "en" },
	"template_data":      func() driver.Value { return "{}" },
	"ab_test_variants":   func() driver.Value { return "[]" },
	"snapshot":           func() driver.Value { return "{}" },
	"send_rate":          func() driver.Value { return int64(0) },
	"list_id":            func() driver.Value { return int64(0) },
	"send_at_local_time": func() driver.Value { return false },
	"ab_test_enabled":    func() driver.Value { return false },
	"deleted_at":         func() driver.Value { return nil },
}

type schedState struct {
	mu      sync.Mutex
	rows    [][]driver.Value
	seq     int64
	queries []string
	// conflict, when set, makes the next campaigns UPDATE match nothing and
	// report zero rows affected — a concurrent worker claiming the same row
	// between the SELECT and the UPDATE.
	conflict bool
}

func newSchedState() *schedState {
	return &schedState{seq: 100}
}

func (s *schedState) colIndex(col string) int {
	for i, c := range schedColumns {
		if c == col {
			return i
		}
	}
	return -1
}

// seedSchedCampaign inserts a campaign fixture row.
func seedSchedCampaign(t *testing.T, s *schedState, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	row := make([]driver.Value, len(schedColumns))
	for i, col := range schedColumns {
		row[i] = nil
		if def, ok := schedDefaults[col]; ok {
			row[i] = def()
		}
	}
	for col, v := range vals {
		idx := s.colIndex(col)
		if idx < 0 {
			t.Fatalf("campaigns has no column %s", col)
		}
		row[idx] = schedVal(v)
	}
	if row[s.colIndex("id")] == nil {
		s.seq++
		row[s.colIndex("id")] = s.seq
	}
	s.rows = append(s.rows, row)
}

func schedVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case models.CampaignStatus:
		return string(t)
	case int64, float64, bool, string, []byte, time.Time:
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
		panic(fmt.Sprintf("schedVal: unsupported type %T", v))
	}
}

// statusOf returns the stored status of the row with the given id.
func (s *schedState) statusOf(t *testing.T, id int64) models.CampaignStatus {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	idIdx, statusIdx := s.colIndex("id"), s.colIndex("status")
	for _, row := range s.rows {
		if row[idIdx] == id {
			return models.CampaignStatus(row[statusIdx].(string))
		}
	}
	t.Fatalf("no campaign row with id %d", id)
	return ""
}

func (s *schedState) recordedQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

type schedConn struct{ s *schedState }

func (c *schedConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *schedConn) Close() error                        { return nil }
func (c *schedConn) Begin() (driver.Tx, error)           { return schedTx{}, nil }

type schedTx struct{}

func (schedTx) Commit() error   { return nil }
func (schedTx) Rollback() error { return nil }

type schedResult struct{ rows int64 }

func (r schedResult) LastInsertId() (int64, error) { return 0, nil }
func (r schedResult) RowsAffected() (int64, error) { return r.rows, nil }

type schedRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *schedRows) Columns() []string { return r.cols }
func (r *schedRows) Close() error      { return nil }
func (r *schedRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type schedConnector struct{ state *schedState }

func (c schedConnector) Connect(context.Context) (driver.Conn, error) {
	return &schedConn{s: c.state}, nil
}
func (c schedConnector) Driver() driver.Driver { return schedDrv{} }

type schedDrv struct{}

func (schedDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func schedGorm(t *testing.T, state *schedState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(schedConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open scheduler fake gorm: %v", err)
	}
	return gdb
}

func (c *schedConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.record(query, args)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `from "campaigns"`) {
		return nil, fmt.Errorf("schedState: cannot route query: %s", query)
	}
	matched, err := c.s.evalWhere(lower, args)
	if err != nil {
		return nil, err
	}
	rows := make([][]driver.Value, len(matched))
	for i, r := range matched {
		rows[i] = append([]driver.Value(nil), r...)
	}
	return &schedRows{cols: schedColumns, rows: rows}, nil
}

func (c *schedConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.record(query, args)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `update "campaigns"`) {
		return nil, fmt.Errorf("schedState: unsupported exec: %s", query)
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.conflict {
		// Simulate a concurrent claim: the row changed between the SELECT and
		// this UPDATE, so no row matches the expected source status.
		c.s.conflict = false
		return schedResult{rows: 0}, nil
	}
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("schedState: malformed update: %s", lower)
	}
	type setter struct {
		idx int
		val driver.Value
	}
	var setters []setter
	for _, pair := range strings.Split(lower[setStart+len(" set "):whereStart], ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("schedState: malformed SET pair %q", pair)
		}
		idx := c.s.colIndex(schedColumnOf(pair[:eq]))
		if idx < 0 {
			return nil, fmt.Errorf("schedState: unknown column in SET %q", pair)
		}
		n, err := strconv.Atoi(strings.TrimSpace(pair[eq+1:])[1:])
		if err != nil {
			return nil, fmt.Errorf("schedState: bad bindvar in SET %q", pair)
		}
		setters = append(setters, setter{idx: idx, val: schedArg(args, n)})
	}
	matched, err := c.s.evalWhereLocked(lower, args)
	if err != nil {
		return nil, err
	}
	for _, row := range matched {
		for _, st := range setters {
			row[st.idx] = st.val
		}
	}
	return schedResult{rows: int64(len(matched))}, nil
}

func (c *schedConn) record(query string, _ []driver.NamedValue) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.queries = append(c.s.queries, query)
}

// evalWhere evaluates the WHERE clause against the stored rows.
func (s *schedState) evalWhere(compact string, args []driver.NamedValue) ([][]driver.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evalWhereLocked(compact, args)
}

func (s *schedState) evalWhereLocked(compact string, args []driver.NamedValue) ([][]driver.Value, error) {
	where := ""
	if i := strings.Index(compact, " where "); i >= 0 {
		where = compact[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit ", " returning "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}

	// gorm wraps each raw Where string in parens, e.g.
	// "(status = $1 and scheduled_at <= $2) and \"campaigns\".\"deleted_at\" is null".
	// Extract the IN (...) groups first (their parens are bindvar syntax, not
	// nesting), then flatten the remaining parens like the segment harness does.
	var preds []func([]driver.Value) bool
	for _, m := range schedInRe.FindAllStringSubmatch(where, -1) {
		idx := s.colIndex(schedColumnOf(m[1]))
		var want []driver.Value
		for _, tok := range strings.Split(m[2], ",") {
			n, err := strconv.Atoi(strings.TrimSpace(tok)[1:])
			if err != nil {
				return nil, fmt.Errorf("schedState: bad bindvar in %q", m[0])
			}
			want = append(want, schedArg(args, n))
		}
		preds = append(preds, func(row []driver.Value) bool {
			for _, w := range want {
				if schedEqual(row[idx], w) {
					return true
				}
			}
			return false
		})
	}
	where = schedInRe.ReplaceAllString(where, "")
	where = strings.NewReplacer("(", " ", ")", " ").Replace(where)

	for _, cond := range strings.Split(where, " and ") {
		cond = strings.TrimSpace(cond)
		if cond == "" {
			continue
		}
		switch {
		case strings.HasSuffix(cond, " is null"):
			idx := s.colIndex(schedColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
		default:
			if i := strings.Index(cond, " <= "); i >= 0 {
				idx := s.colIndex(schedColumnOf(cond[:i]))
				n, err := strconv.Atoi(strings.TrimSpace(cond[i+len(" <= "):])[1:])
				if err != nil {
					return nil, fmt.Errorf("schedState: bad bindvar in %q", cond)
				}
				want := schedArg(args, n)
				preds = append(preds, func(row []driver.Value) bool { return schedLessEqual(row[idx], want) })
				continue
			}
			eq := strings.Index(cond, " = ")
			if eq < 0 {
				return nil, fmt.Errorf("schedState: unsupported condition %q", cond)
			}
			idx := s.colIndex(schedColumnOf(cond[:eq]))
			n, err := strconv.Atoi(strings.TrimSpace(cond[eq+len(" = "):])[1:])
			if err != nil {
				return nil, fmt.Errorf("schedState: bad bindvar in %q", cond)
			}
			want := schedArg(args, n)
			preds = append(preds, func(row []driver.Value) bool { return schedEqual(row[idx], want) })
		}
	}

	var out [][]driver.Value
	for _, row := range s.rows {
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

// schedInRe matches `col in ($1,$2)` (quoted or bare column, bindvar list).
var schedInRe = regexp.MustCompile(`("[^"]+"|[a-z_.]+) in \(([^)]*)\)`)

func schedColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func schedArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("schedState: no arg with ordinal %d", n))
}

func schedEqual(a, b driver.Value) bool {
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
	ia, ok := a.(int64)
	if !ok {
		return a == b
	}
	switch tb := b.(type) {
	case int64:
		return ia == tb
	case string:
		return strconv.FormatInt(ia, 10) == tb
	}
	return a == b
}

func schedLessEqual(a, b driver.Value) bool {
	ta, ok1 := a.(time.Time)
	tb, ok2 := b.(time.Time)
	if ok1 && ok2 {
		return !ta.After(tb)
	}
	ia, ok1 := a.(int64)
	ib, ok2 := b.(int64)
	if ok1 && ok2 {
		return ia <= ib
	}
	return false
}

// ---------- the producer fake ----------

type schedProducer struct {
	mu    sync.Mutex
	ids   []uint
	err   error
	calls int
}

func (p *schedProducer) EnqueueCampaignStart(id uint) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return p.err
	}
	p.ids = append(p.ids, id)
	return nil
}

func (p *schedProducer) enqueued() []uint {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uint(nil), p.ids...)
}

// ---------- fixtures ----------

func newSchedulerJob(t *testing.T, state *schedState, producer *schedProducer) *CampaignSchedulerJob {
	t.Helper()
	repo := repositories.NewCampaignRepository(schedGorm(t, state))
	return NewCampaignSchedulerJob(repo, producer)
}

// ---------- the scheduler scenarios ----------

// A due campaign (status=scheduled, scheduled_at in the past) is claimed and
// enqueued; a future one and one already in sending are left alone — the
// FindScheduledReady filter and the atomic claim carry the whole behavior.
func TestSchedulerClaimsOnlyDueScheduledCampaigns(t *testing.T) {
	state := newSchedState()
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	seedSchedCampaign(t, state, map[string]any{"id": uint(1), "status": models.CampaignStatusScheduled, "scheduled_at": past})
	seedSchedCampaign(t, state, map[string]any{"id": uint(2), "status": models.CampaignStatusScheduled, "scheduled_at": future})
	seedSchedCampaign(t, state, map[string]any{"id": uint(3), "status": models.CampaignStatusSending, "scheduled_at": past})
	producer := &schedProducer{}
	job := newSchedulerJob(t, state, producer)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("run scheduler: %v", err)
	}

	if got := producer.enqueued(); len(got) != 1 || got[0] != 1 {
		t.Errorf("enqueued = %v, want [1] (only the due scheduled campaign)", got)
	}
	if got := state.statusOf(t, 1); got != models.CampaignStatusSending {
		t.Errorf("campaign 1 status = %q, want sending", got)
	}
	if got := state.statusOf(t, 2); got != models.CampaignStatusScheduled {
		t.Errorf("future campaign 2 status = %q, want scheduled (untouched)", got)
	}
	if got := state.statusOf(t, 3); got != models.CampaignStatusSending {
		t.Errorf("campaign 3 status = %q, want sending (untouched)", got)
	}
}

// RowsAffected == 0 on the claim (another worker won the race between the
// SELECT and the UPDATE) must skip the campaign entirely: no enqueue, and the
// winner's state is not clobbered.
func TestSchedulerClaimConflictSkipsWithoutEnqueue(t *testing.T) {
	state := newSchedState()
	seedSchedCampaign(t, state, map[string]any{
		"id": uint(7), "status": models.CampaignStatusScheduled,
		"scheduled_at": time.Now().Add(-time.Minute),
	})
	producer := &schedProducer{}
	job := newSchedulerJob(t, state, producer)

	state.mu.Lock()
	state.conflict = true
	state.mu.Unlock()

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("run scheduler: %v", err)
	}

	if producer.calls != 0 {
		t.Errorf("enqueue calls = %d, want 0 after a lost claim", producer.calls)
	}
	if got := state.statusOf(t, 7); got != models.CampaignStatusScheduled {
		t.Errorf("campaign 7 status = %q, want scheduled (claim left to the winner)", got)
	}
}

// An enqueue failure rolls the claim back to scheduled so the next tick
// retries — the same rollback pattern as the Send handler.
func TestSchedulerEnqueueFailureRollsBackToScheduled(t *testing.T) {
	state := newSchedState()
	seedSchedCampaign(t, state, map[string]any{
		"id": uint(9), "status": models.CampaignStatusScheduled,
		"scheduled_at": time.Now().Add(-time.Minute),
	})
	producer := &schedProducer{err: errors.New("redis down")}
	job := newSchedulerJob(t, state, producer)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("run scheduler: %v", err)
	}

	if producer.calls != 1 {
		t.Errorf("enqueue calls = %d, want 1", producer.calls)
	}
	if got := state.statusOf(t, 9); got != models.CampaignStatusScheduled {
		t.Errorf("campaign 9 status = %q, want scheduled (rolled back for retry)", got)
	}
	// The rollback must target the claimed row, i.e. the second UPDATE moves
	// sending -> scheduled.
	found := 0
	for _, q := range state.recordedQueries() {
		lower := strings.ToLower(q)
		if strings.Contains(lower, `update "campaigns"`) {
			found++
		}
	}
	if found != 2 {
		t.Errorf("campaigns UPDATEs = %d, want 2 (claim + rollback)", found)
	}
}

// A paused or cancelled campaign is not in status=scheduled, so the claim
// filter excludes it without special-casing (states documented in the job).
func TestSchedulerIgnoresPausedAndCancelledCampaigns(t *testing.T) {
	state := newSchedState()
	seedSchedCampaign(t, state, map[string]any{
		"id": uint(11), "status": models.CampaignStatusPaused,
		"scheduled_at": time.Now().Add(-time.Minute),
	})
	seedSchedCampaign(t, state, map[string]any{
		"id": uint(12), "status": models.CampaignStatusCancelled,
		"scheduled_at": time.Now().Add(-time.Minute),
	})
	producer := &schedProducer{}
	job := newSchedulerJob(t, state, producer)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("run scheduler: %v", err)
	}

	if producer.calls != 0 {
		t.Errorf("enqueue calls = %d, want 0", producer.calls)
	}
	if got := state.statusOf(t, 11); got != models.CampaignStatusPaused {
		t.Errorf("paused campaign status = %q, want paused", got)
	}
	if got := state.statusOf(t, 12); got != models.CampaignStatusCancelled {
		t.Errorf("cancelled campaign status = %q, want cancelled", got)
	}
}
