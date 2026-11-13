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
	"github.com/goposta/posta/internal/queues"
	"github.com/hibiken/asynq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The reconcile job runs against the real gorm on a fake database/sql driver
// (the campaign_scheduler_test.go arrangement): the fake stores emails and
// campaign_messages rows, evaluates the = and < comparisons the job issues,
// and applies UPDATEs so the CAS claim semantics (RowsAffected) are pinned.

var qrcEmailsColumns = []string{"id", "uuid", "status", "updated_at", "sender", "recipients", "created_at"}

var qrcTableColumns = map[string][]string{
	"emails":                 qrcEmailsColumns,
	"campaign_messages":      {"id", "email_id"},
	"email_queue_schedules":  {"id", "email_id", "status", "next_retry_at", "attempt_index"},
}

type qrcState struct {
	mu    sync.Mutex
	seq   int64
	table map[string][][]driver.Value // table -> rows
	calls []string
}

func newQrcState() *qrcState {
	return &qrcState{seq: 10, table: map[string][][]driver.Value{}}
}

func (s *qrcState) colIndex(table, col string) int {
	spec, ok := qrcTableColumns[table]
	if !ok {
		return -1
	}
	for i, c := range spec {
		if c == col {
			return i
		}
	}
	return -1
}

func (s *qrcState) insert(table string, vals map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	spec := qrcTableColumns[table]
	row := make([]driver.Value, len(spec))
	for i := range spec {
		row[i] = nil
	}
	for col, v := range vals {
		row[s.colIndex(table, col)] = qrcVal(v)
	}
	if s.colIndex(table, "id") >= 0 && row[s.colIndex(table, "id")] == nil {
		row[s.colIndex(table, "id")] = s.seq
	}
	if s.colIndex(table, "uuid") >= 0 && row[s.colIndex(table, "uuid")] == nil {
		row[s.colIndex(table, "uuid")] = fmt.Sprintf("uuid-%d", s.seq)
	}
	if s.colIndex(table, "recipients") >= 0 && row[s.colIndex(table, "recipients")] == nil {
		row[s.colIndex(table, "recipients")] = `{rcpt@example.com}`
	}
	s.table[table] = append(s.table[table], row)
}

// updatedAtOf returns the current updated_at of the email with the given id.
func (s *qrcState) updatedAtOf(id int64) (driver.Value, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idIdx, updIdx := s.colIndex("emails", "id"), s.colIndex("emails", "updated_at")
	for _, row := range s.table["emails"] {
		if row[idIdx] == id {
			return row[updIdx], true
		}
	}
	return nil, false
}

func qrcVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case models.EmailStatus:
		return string(t)
	case models.EmailQueueScheduleStatus:
		return string(t)
	case time.Time:
		return t
	case int64, float64, bool, string, []byte:
		return t
	default:
		panic(fmt.Sprintf("qrcVal: unsupported type %T", v))
	}
}

type qrcConn struct{ s *qrcState }

func (c *qrcConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *qrcConn) Close() error                        { return nil }
func (c *qrcConn) Begin() (driver.Tx, error)           { return qrcTx{}, nil }

type qrcTx struct{}

func (qrcTx) Commit() error   { return nil }
func (qrcTx) Rollback() error { return nil }

type qrcResult struct{ rows int64 }

func (r qrcResult) LastInsertId() (int64, error) { return 0, nil }
func (r qrcResult) RowsAffected() (int64, error) { return r.rows, nil }

type qrcRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *qrcRows) Columns() []string { return r.cols }
func (r *qrcRows) Close() error      { return nil }
func (r *qrcRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type qrcConnector struct{ state *qrcState }

func (c qrcConnector) Connect(context.Context) (driver.Conn, error) { return &qrcConn{s: c.state}, nil }
func (c qrcConnector) Driver() driver.Driver                        { return qrcDrv{} }

type qrcDrv struct{}

func (qrcDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func qrcGorm(t *testing.T, state *qrcState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(qrcConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open reconcile fake gorm: %v", err)
	}
	return gdb
}

func (c *qrcConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.record(query, args)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	switch {
	case strings.Contains(lower, `from "campaign_messages"`):
		// The campaign-link existence count.
		matched, err := c.s.evalWhere("campaign_messages", lower, args)
		if err != nil {
			return nil, err
		}
		return &qrcRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	case strings.Contains(lower, `from "email_queue_schedules"`):
		// The SF-08 schedule consult (only reached when a scheduler is wired).
		matched, err := c.s.evalWhere("email_queue_schedules", lower, args)
		if err != nil {
			return nil, err
		}
		rows := make([][]driver.Value, len(matched))
		for i, r := range matched {
			rows[i] = append([]driver.Value(nil), r...)
		}
		return &qrcRows{cols: qrcTableColumns["email_queue_schedules"], rows: rows}, nil
	case strings.Contains(lower, `from "emails"`):
		matched, err := c.s.evalWhere("emails", lower, args)
		if err != nil {
			return nil, err
		}
		matched = qrcLimit(matched, lower, args)
		rows := make([][]driver.Value, len(matched))
		for i, r := range matched {
			rows[i] = append([]driver.Value(nil), r...)
		}
		return &qrcRows{cols: qrcEmailsColumns, rows: rows}, nil
	}
	return nil, fmt.Errorf("qrcState: cannot route query: %s", query)
}

func (c *qrcConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.record(query, args)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `update "emails"`) {
		return nil, fmt.Errorf("qrcState: unsupported exec: %s", query)
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("qrcState: malformed update: %s", lower)
	}
	type setter struct {
		idx int
		val driver.Value
	}
	var setters []setter
	for _, pair := range strings.Split(lower[setStart+len(" set "):whereStart], ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("qrcState: malformed SET pair %q", pair)
		}
		idx := s.colIndex("emails", qrcColumnOf(pair[:eq]))
		n, err := strconv.Atoi(strings.TrimSpace(pair[eq+1:])[1:])
		if err != nil {
			return nil, fmt.Errorf("qrcState: bad bindvar in SET %q", pair)
		}
		setters = append(setters, setter{idx: idx, val: qrcArg(args, n)})
	}
	// evalWhere needs the lock; inline a locked variant for the update path.
	where := lower[whereStart+len(" where "):]
	preds, err := s.predsFor("emails", where, args)
	if err != nil {
		return nil, err
	}
	matched := 0
	for _, row := range s.table["emails"] {
		ok := true
		for _, p := range preds {
			if !p(row) {
				ok = false
				break
			}
		}
		if ok {
			matched++
			for _, st := range setters {
				row[st.idx] = st.val
			}
		}
	}
	return qrcResult{rows: int64(matched)}, nil
}

func (c *qrcConn) record(query string, _ []driver.NamedValue) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.calls = append(c.s.calls, query)
}

// evalWhere evaluates the WHERE clause of a SELECT.
func (s *qrcState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := compact
	if i := strings.Index(where, " where "); i >= 0 {
		where = where[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}
	preds, err := s.predsFor(table, where, args)
	if err != nil {
		return nil, err
	}
	var out [][]driver.Value
	for _, row := range s.table[table] {
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

// predsFor compiles a flattened WHERE clause (already stripped of ORDER BY /
// LIMIT by the caller for SELECTs) into row predicates.
func (s *qrcState) predsFor(table, where string, args []driver.NamedValue) ([]func([]driver.Value) bool, error) {
	where = strings.NewReplacer("(", " ", ")", " ").Replace(where)
	var preds []func([]driver.Value) bool
	for _, cond := range strings.Split(where, " and ") {
		cond = strings.TrimSpace(cond)
		if cond == "" {
			continue
		}
		switch {
		case strings.HasSuffix(cond, " is null"):
			idx := s.colIndex(table, qrcColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
		default:
			if i := strings.Index(cond, " < "); i >= 0 {
				idx := s.colIndex(table, qrcColumnOf(cond[:i]))
				n, err := strconv.Atoi(strings.TrimSpace(cond[i+len(" < "):])[1:])
				if err != nil {
					return nil, fmt.Errorf("qrcState: bad bindvar in %q", cond)
				}
				want := qrcArg(args, n)
				preds = append(preds, func(row []driver.Value) bool { return qrcLess(row[idx], want) })
				continue
			}
			eq := strings.Index(cond, " = ")
			if eq < 0 {
				return nil, fmt.Errorf("qrcState: unsupported condition %q", cond)
			}
			idx := s.colIndex(table, qrcColumnOf(cond[:eq]))
			n, err := strconv.Atoi(strings.TrimSpace(cond[eq+len(" = "):])[1:])
			if err != nil {
				return nil, fmt.Errorf("qrcState: bad bindvar in %q", cond)
			}
			want := qrcArg(args, n)
			preds = append(preds, func(row []driver.Value) bool { return qrcEqual(row[idx], want) })
		}
	}
	return preds, nil
}

func qrcColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func qrcArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("qrcState: no arg with ordinal %d", n))
}

func qrcEqual(a, b driver.Value) bool {
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

func qrcLess(a, b driver.Value) bool {
	ta, ok1 := a.(time.Time)
	tb, ok2 := b.(time.Time)
	if ok1 && ok2 {
		return ta.Before(tb)
	}
	return false
}

// qrcLimit applies LIMIT $n to a SELECT's matched rows (ORDER BY is ignored:
// insertion order suffices for these tests).
func qrcLimit(rows [][]driver.Value, compact string, _ []driver.NamedValue) [][]driver.Value {
	i := strings.Index(compact, " limit ")
	if i < 0 {
		return rows
	}
	n, err := strconv.Atoi(strings.TrimSpace(compact[i+len(" limit "):])[1:])
	if err != nil || n >= len(rows) {
		return rows
	}
	return rows[:n]
}

// ---------- stubs ----------

type qrcProducer struct {
	mu          sync.Mutex
	enqueued    []uint
	scheduledAt []time.Time
	failOn      map[uint]error
}

func newQrcProducer() *qrcProducer { return &qrcProducer{failOn: map[uint]error{}} }

func (p *qrcProducer) EnqueueEmailSend(emailID uint, queue string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err, ok := p.failOn[emailID]; ok {
		return err
	}
	p.enqueued = append(p.enqueued, emailID)
	return nil
}

// EnqueueEmailSendAt makes the stub a ScheduledSendEnqueuer (the SF-08
// ProcessAt carrier); the scheduled time is recorded for assertions.
func (p *qrcProducer) EnqueueEmailSendAt(emailID uint, queue string, sendAt time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err, ok := p.failOn[emailID]; ok {
		return err
	}
	p.enqueued = append(p.enqueued, emailID)
	p.scheduledAt = append(p.scheduledAt, sendAt)
	return nil
}

type qrcInspector struct {
	tasks map[string]asynq.TaskState // task id -> state
	err   error                      // when set, every lookup fails with it
}

func (i *qrcInspector) GetTaskInfo(queue, id string) (*asynq.TaskInfo, error) {
	if i.err != nil {
		return nil, i.err
	}
	state, ok := i.tasks[id]
	if !ok {
		return nil, asynq.ErrTaskNotFound
	}
	return &asynq.TaskInfo{ID: id, Queue: queue, State: state}, nil
}

// ---------- fixtures + tests ----------

func qrcJob(t *testing.T, state *qrcState, producer *qrcProducer, inspector *qrcInspector) *QueueReconcileJob {
	t.Helper()
	return NewQueueReconcileJob(qrcGorm(t, state), producer, inspector)
}

func seedQrcEmail(t *testing.T, s *qrcState, id uint, status models.EmailStatus, updatedAt time.Time) {
	t.Helper()
	s.insert("emails", map[string]any{
		"id": id, "status": status, "updated_at": updatedAt,
		"sender": "sender@example.com", "created_at": updatedAt.Add(-time.Hour),
	})
}

// qrcNow anchors every fixture; the real clock keeps the 15-minute window
// meaningful (a fixed date in the future would make "fresh" rows look stale).
var qrcNow = time.Now().Truncate(time.Second)

func TestQueueReconcileReenqueuesLostTask(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	inspector := &qrcInspector{tasks: map[string]asynq.TaskState{}}
	seedQrcEmail(t, state, 11, models.EmailStatusQueued, qrcNow.Add(-30*time.Minute))

	j := qrcJob(t, state, producer, inspector)
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(producer.enqueued) != 1 || producer.enqueued[0] != 11 {
		t.Fatalf("expected exactly one re-enqueue of email 11, got %v", producer.enqueued)
	}
	// The claim re-armed the window: the row is no longer older than 15 min.
	upd, ok := state.updatedAtOf(11)
	if !ok {
		t.Fatal("email 11 disappeared")
	}
	if upd.(time.Time).Before(qrcNow.Add(-14 * time.Minute)) {
		t.Fatalf("claim did not refresh updated_at: %v", upd)
	}
}

func TestQueueReconcileSkipsLiveTask(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	inspector := &qrcInspector{tasks: map[string]asynq.TaskState{
		queues.EmailSendTaskID(12): asynq.TaskStatePending,
		queues.EmailSendTaskID(13): asynq.TaskStateRetry,
		queues.EmailSendTaskID(14): asynq.TaskStateScheduled,
	}}
	seedQrcEmail(t, state, 12, models.EmailStatusQueued, qrcNow.Add(-time.Hour))
	seedQrcEmail(t, state, 13, models.EmailStatusQueued, qrcNow.Add(-time.Hour))
	seedQrcEmail(t, state, 14, models.EmailStatusQueued, qrcNow.Add(-time.Hour))

	j := qrcJob(t, state, producer, inspector)
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(producer.enqueued) != 0 {
		t.Fatalf("live tasks must not be re-enqueued: %v", producer.enqueued)
	}
	if upd, _ := state.updatedAtOf(12); !upd.(time.Time).Equal(qrcNow.Add(-time.Hour)) {
		t.Fatalf("skipped row must keep its updated_at: %v", upd)
	}
}

// A dead-lettered task holding the deterministic ID strands a queued email
// forever (the producer clears it during re-enqueue), so the reconcile treats
// archived like missing.
func TestQueueReconcileReenqueuesArchivedTask(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	inspector := &qrcInspector{tasks: map[string]asynq.TaskState{
		queues.EmailSendTaskID(15): asynq.TaskStateArchived,
	}}
	seedQrcEmail(t, state, 15, models.EmailStatusQueued, qrcNow.Add(-2*time.Hour))

	j := qrcJob(t, state, producer, inspector)
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(producer.enqueued) != 1 || producer.enqueued[0] != 15 {
		t.Fatalf("archived holder must be re-enqueued, got %v", producer.enqueued)
	}
}

func TestQueueReconcileSkipsCampaignEmails(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	inspector := &qrcInspector{tasks: map[string]asynq.TaskState{}}
	seedQrcEmail(t, state, 16, models.EmailStatusQueued, qrcNow.Add(-2*time.Hour))
	// A campaign_messages row for the email — the campaign machinery owns it.
	state.insert("campaign_messages", map[string]any{"id": uint(77), "email_id": uint(16)})

	j := qrcJob(t, state, producer, inspector)
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(producer.enqueued) != 0 {
		t.Fatalf("campaign email must be skipped: %v", producer.enqueued)
	}
}

func TestQueueReconcileOnlyStaleQueuedRows(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	inspector := &qrcInspector{tasks: map[string]asynq.TaskState{}}
	// Recently queued: outside the 15-minute window.
	seedQrcEmail(t, state, 17, models.EmailStatusQueued, qrcNow.Add(-2*time.Minute))
	// Stale but not queued: other statuses are not the reconcile's business.
	seedQrcEmail(t, state, 18, models.EmailStatusProcessing, qrcNow.Add(-2*time.Hour))
	seedQrcEmail(t, state, 19, models.EmailStatusFailed, qrcNow.Add(-2*time.Hour))

	j := qrcJob(t, state, producer, inspector)
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(producer.enqueued) != 0 {
		t.Fatalf("fresh or non-queued emails must not be touched: %v", producer.enqueued)
	}
}

// A failed re-enqueue leaves the row queued with a refreshed updated_at, so
// the next tick retries it instead of hot-looping every 5 minutes.
func TestQueueReconcileEnqueueFailureReArms(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	producer.failOn[20] = errors.New("redis unavailable")
	inspector := &qrcInspector{tasks: map[string]asynq.TaskState{}}
	seedQrcEmail(t, state, 20, models.EmailStatusQueued, qrcNow.Add(-2*time.Hour))

	j := qrcJob(t, state, producer, inspector)
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run must swallow the enqueue error: %v", err)
	}
	if upd, _ := state.updatedAtOf(20); upd.(time.Time).Before(qrcNow.Add(-14 * time.Minute)) {
		t.Fatalf("failed enqueue must still re-arm the window: %v", upd)
	}
}

// Redis being down must not mass re-enqueue: lookups failing with anything
// other than ErrTaskNotFound skip the tick.
func TestQueueReconcileLookupErrorSkipsTick(t *testing.T) {
	state := newQrcState()
	producer := newQrcProducer()
	inspector := &qrcInspector{err: errors.New("redis connection refused")}
	seedQrcEmail(t, state, 21, models.EmailStatusQueued, qrcNow.Add(-2*time.Hour))

	j := qrcJob(t, state, producer, inspector)
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(producer.enqueued) != 0 {
		t.Fatalf("lookup failure must skip the tick: %v", producer.enqueued)
	}
}

// Nil producer/inspector (dev mode, no Redis) is a no-op, not a panic.
func TestQueueReconcileNilDeps(t *testing.T) {
	state := newQrcState()
	seedQrcEmail(t, state, 22, models.EmailStatusQueued, qrcNow.Add(-2*time.Hour))
	j := NewQueueReconcileJob(qrcGorm(t, state), nil, nil)
	if err := j.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run with nil deps: %v", err)
	}
}
