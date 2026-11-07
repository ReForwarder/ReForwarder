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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/queues"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The queue administration endpoints are exercised through the real okapi
// stack with the real admin JWT middleware. asynq.Inspector needs a live
// Redis (the CI has no Redis harness for unit tests), so the handler sits
// behind the QueueInspector interface and these tests drive a stub that
// records calls and can fail like the real inspector does. Audited actions
// are verified against a fake database/sql driver holding the events table
// (the segment_handler_test.go arrangement).

// ---------- the stub inspector ----------

type stubQueueInspector struct {
	mu     sync.Mutex
	queues map[string]*asynq.QueueInfo
	tasks  map[string][]*asynq.TaskInfo // state -> tasks (single queue granularity not needed by the stub)
	calls  []string
	errOn  map[string]error // "<verb>:<arg>" -> error
}

func newStubQueueInspector() *stubQueueInspector {
	return &stubQueueInspector{
		queues: map[string]*asynq.QueueInfo{},
		tasks:  map[string][]*asynq.TaskInfo{},
		errOn:  map[string]error{},
	}
}

func (s *stubQueueInspector) record(call string) {
	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()
}

func (s *stubQueueInspector) fail(verb, arg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.errOn[verb+":"+arg]; ok {
		return e
	}
	return nil
}

func (s *stubQueueInspector) setQueue(name string, info *asynq.QueueInfo) {
	s.mu.Lock()
	s.queues[name] = info
	s.mu.Unlock()
}

func (s *stubQueueInspector) setTasks(state string, tasks []*asynq.TaskInfo) {
	s.mu.Lock()
	s.tasks[state] = tasks
	s.mu.Unlock()
}

func (s *stubQueueInspector) callCount(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (s *stubQueueInspector) GetQueueInfo(queue string) (*asynq.QueueInfo, error) {
	s.record("GetQueueInfo:" + queue)
	if err := s.fail("GetQueueInfo", queue); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if q, ok := s.queues[queue]; ok {
		return q, nil
	}
	return &asynq.QueueInfo{Queue: queue}, nil
}

func (s *stubQueueInspector) list(state string, page, size int) ([]*asynq.TaskInfo, error) {
	s.record("List" + state)
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.tasks[state]
	// Honor paging so the handler's page/size contract is pinned.
	start := page * size
	if start >= len(all) {
		return nil, nil
	}
	end := start + size
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], nil
}

func (s *stubQueueInspector) ListPendingTasks(queue string, page, size int) ([]*asynq.TaskInfo, error) {
	if err := s.fail("List", queue); err != nil {
		return nil, err
	}
	return s.list("pending", page, size)
}
func (s *stubQueueInspector) ListActiveTasks(queue string, page, size int) ([]*asynq.TaskInfo, error) {
	if err := s.fail("List", queue); err != nil {
		return nil, err
	}
	return s.list("active", page, size)
}
func (s *stubQueueInspector) ListScheduledTasks(queue string, page, size int) ([]*asynq.TaskInfo, error) {
	if err := s.fail("List", queue); err != nil {
		return nil, err
	}
	return s.list("scheduled", page, size)
}
func (s *stubQueueInspector) ListRetryTasks(queue string, page, size int) ([]*asynq.TaskInfo, error) {
	if err := s.fail("List", queue); err != nil {
		return nil, err
	}
	return s.list("retry", page, size)
}
func (s *stubQueueInspector) ListArchivedTasks(queue string, page, size int) ([]*asynq.TaskInfo, error) {
	if err := s.fail("List", queue); err != nil {
		return nil, err
	}
	return s.list("archived", page, size)
}

func (s *stubQueueInspector) DeleteTask(queue, id string) error {
	s.record("DeleteTask:" + queue + "/" + id)
	return s.fail("DeleteTask", id)
}
func (s *stubQueueInspector) RunTask(queue, id string) error {
	s.record("RunTask:" + queue + "/" + id)
	return s.fail("RunTask", id)
}
func (s *stubQueueInspector) ArchiveTask(queue, id string) error {
	s.record("ArchiveTask:" + queue + "/" + id)
	return s.fail("ArchiveTask", id)
}
func (s *stubQueueInspector) PauseQueue(queue string) error {
	s.record("PauseQueue:" + queue)
	return s.fail("PauseQueue", queue)
}
func (s *stubQueueInspector) UnpauseQueue(queue string) error {
	s.record("UnpauseQueue:" + queue)
	return s.fail("UnpauseQueue", queue)
}
func (s *stubQueueInspector) GetTaskInfo(queue, id string) (*asynq.TaskInfo, error) {
	s.record("GetTaskInfo:" + queue + "/" + id)
	if err := s.fail("GetTaskInfo", id); err != nil {
		return nil, err
	}
	return &asynq.TaskInfo{ID: id, Queue: queue, Type: queues.EmailSendTaskType, State: asynq.TaskStatePending}, nil
}

// stubSendProducer is the jobs.SendEnqueuer stub behind requeue_stuck.
type stubSendProducer struct {
	mu       sync.Mutex
	enqueued []uint
}

func (p *stubSendProducer) EnqueueEmailSend(emailID uint, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enqueued = append(p.enqueued, emailID)
	return nil
}

func queueTaskFixture(id string, retried int, lastErr string) *asynq.TaskInfo {
	t := &asynq.TaskInfo{
		ID:            id,
		Queue:         queues.Transactional,
		Type:          "email:send",
		Payload:       []byte(`{"email_id":42}`),
		Retried:       retried,
		MaxRetry:      25,
		LastErr:       lastErr,
		State:         asynq.TaskStateRetry,
		NextProcessAt: time.Now().Add(2 * time.Minute).Truncate(time.Second),
	}
	if lastErr != "" {
		lastFailed := time.Now().Add(-time.Minute).Truncate(time.Second)
		t.LastFailedAt = lastFailed
	}
	return t
}

// ---------- the events fake database (audit trail) ----------

var qEvtColumns = []string{
	"id", "category", "type", "workspace_id", "actor_id", "actor_name",
	"client_ip", "message", "metadata", "outcome", "target_type", "target_id", "created_at",
}

type qEvtState struct {
	mu   sync.Mutex
	rows [][]driver.Value
	seq  int64
}

func newQEvtState() *qEvtState { return &qEvtState{seq: 500} }

func (s *qEvtState) events() []map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, 0, len(s.rows))
	for _, row := range s.rows {
		m := map[string]driver.Value{}
		for i, c := range qEvtColumns {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

type qEvtConn struct{ s *qEvtState }

func (c *qEvtConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *qEvtConn) Close() error                        { return nil }
func (c *qEvtConn) Begin() (driver.Tx, error)           { return qEvtTx{}, nil }

// QueryContext handles the events INSERT ... RETURNING "id" (gorm Create on
// Postgres is a query) and nothing else.
func (c *qEvtConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `into "events"`) {
		return nil, fmt.Errorf("qEvtState: cannot route query: %s", query)
	}
	if _, err := c.insertEvent(query, args); err != nil {
		return nil, err
	}
	id := c.s.seq
	return &qEvtRows{cols: []string{"id"}, rows: [][]driver.Value{{id}}}, nil
}

// ExecContext accepts a plain events INSERT and nothing else.
func (c *qEvtConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `into "events"`) {
		return nil, fmt.Errorf("qEvtState: unsupported exec: %s", query)
	}
	return c.insertEvent(query, args)
}

func (c *qEvtConn) insertEvent(query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.ToLower(query)
	vi := strings.Index(lower, " values (")
	if vi < 0 {
		return nil, fmt.Errorf("qEvtState: insert without VALUES")
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	colParts := strings.Split(query[open+1:close], ",")
	valText := query[vi+len(" values "):]
	end := strings.LastIndex(valText, ")")
	valParts := strings.Split(valText[1:end], ",")
	if len(valParts) != len(colParts) {
		return nil, fmt.Errorf("qEvtState: %d columns but %d values", len(colParts), len(valParts))
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.seq++
	row := make([]driver.Value, len(qEvtColumns))
	for i := range qEvtColumns {
		row[i] = nil
	}
	for j, p := range colParts {
		col := strings.Trim(strings.TrimSpace(p), `"`)
		n, err := strconv.Atoi(strings.TrimSpace(valParts[j])[1:])
		if err != nil {
			return nil, err
		}
		idx := -1
		for i, name := range qEvtColumns {
			if name == col {
				idx = i
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("qEvtState: events has no column %q", col)
		}
		row[idx] = evtArg(args, n)
	}
	if row[0] == nil {
		row[0] = c.s.seq
	}
	c.s.rows = append(c.s.rows, row)
	return qEvtResult{rows: 1}, nil
}

func evtArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("qEvtState: no arg with ordinal %d", n))
}

type qEvtTx struct{}

func (qEvtTx) Commit() error   { return nil }
func (qEvtTx) Rollback() error { return nil }

type qEvtResult struct{ rows int64 }

func (r qEvtResult) LastInsertId() (int64, error) { return 0, nil }
func (r qEvtResult) RowsAffected() (int64, error) { return r.rows, nil }

type qEvtRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *qEvtRows) Columns() []string { return r.cols }
func (r *qEvtRows) Close() error      { return nil }
func (r *qEvtRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type qEvtConnector struct{ state *qEvtState }

func (c qEvtConnector) Connect(context.Context) (driver.Conn, error) {
	return &qEvtConn{s: c.state}, nil
}
func (c qEvtConnector) Driver() driver.Driver { return qEvtDrv{} }

type qEvtDrv struct{}

func (qEvtDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func qEvtGorm(t *testing.T, state *qEvtState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(qEvtConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open events fake gorm: %v", err)
	}
	return gdb
}

// ---------- the server under test ----------

func newQueueAdminServer(t *testing.T) (*okapi.TestServer, *stubQueueInspector, *qEvtState) {
	t.Helper()
	stub := newStubQueueInspector()
	evtState := newQEvtState()

	h := &AdminHandler{inspector: nil}
	h.queueInspector = stub
	auditLogger := audit.NewLogger(eventbus.New(repositories.NewEventRepository(qEvtGorm(t, evtState))))
	h.audit = auditLogger

	// The SF-59 operational actions handler over the same stub, with a fake
	// database for the reconcile sweep (the logs fake answers any unknown
	// SELECT with an empty set, so the sweep finds no stuck email) and a
	// recording producer.
	ops := &QueueOpsHandler{
		db:             fakeGorm(t, newFakeState()),
		producer:       &stubSendProducer{},
		inspector:      stub,
		queueInspector: stub,
	}
	ops.SetAuditLogger(auditLogger)

	o := okapi.NewTestServer(t)
	adminAuth := middlewares.JWTAdminAuth(&config.Config{JWTSecret: logsTestJWTSecret})
	g := o.Group("/admin", adminAuth.Middleware)
	g.Get("/queues", h.ListQueues)
	g.Get("/queues/{queue}/tasks", okapi.H(h.ListQueueTasks))
	g.Post("/queues/{queue}/tasks/{task_id}/cancel", okapi.H(h.CancelQueueTask))
	g.Post("/queues/{queue}/tasks/{task_id}/run", okapi.H(h.RunQueueTask))
	g.Post("/queues/{queue}/tasks/{task_id}/archive", okapi.H(h.ArchiveQueueTask))
	g.Delete("/queues/{queue}/tasks/{task_id}", okapi.H(h.DestroyQueueTask))
	g.Post("/queue/pause", h.PauseQueues)
	g.Post("/queue/resume", h.ResumeQueues)
	g.Post("/queue/actions/{action}", okapi.H(ops.RunAction))
	return o, stub, evtState
}

func doQueueAdminReq(t *testing.T, o *okapi.TestServer, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "admin"))
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

// ---------- tests ----------

func TestAdminQueuesOverview(t *testing.T) {
	o, stub, _ := newQueueAdminServer(t)
	stub.setQueue(queues.Transactional, &asynq.QueueInfo{
		Queue: queues.Transactional, Size: 30, Pending: 10, Active: 2, Scheduled: 5,
		Retry: 3, Archived: 7, Processed: 1000, Failed: 20, Latency: 250 * time.Millisecond,
	})
	// bulk and low are left absent — absence reports zeros, not an error.

	rec := doQueueAdminReq(t, o, http.MethodGet, "/admin/queues")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/queues = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Success bool `json:"success"`
		Data    struct {
			Queues        []QueueStats `json:"queues"`
			ActiveWorkers int          `json:"active_workers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !res.Success || len(res.Data.Queues) != 3 {
		t.Fatalf("expected 3 queues, got %+v", res.Data)
	}
	byName := map[string]QueueStats{}
	for _, q := range res.Data.Queues {
		byName[q.Queue] = q
	}
	tx := byName[queues.Transactional]
	if tx.Pending != 10 || tx.Active != 2 || tx.Scheduled != 5 || tx.Retry != 3 || tx.Archived != 7 {
		t.Fatalf("transactional stats wrong: %+v", tx)
	}
	if tx.Processed != 1000 || tx.Failed != 20 {
		t.Fatalf("processed/failed stats wrong: %+v", tx)
	}
	if tx.LatencyMS != 250 {
		t.Fatalf("latency = %dms, want 250", tx.LatencyMS)
	}
	if got := byName[queues.Bulk]; got.Size != 0 || got.Archived != 0 {
		t.Fatalf("absent queue must report zeros: %+v", got)
	}
}

func TestAdminQueueTasksListByState(t *testing.T) {
	o, stub, _ := newQueueAdminServer(t)
	stub.setTasks("retry", []*asynq.TaskInfo{
		queueTaskFixture("task-a", 2, "dial tcp: i/o timeout"),
		queueTaskFixture("task-b", 0, ""),
	})
	// 3 more tasks so the page contract is visible.
	extra := []*asynq.TaskInfo{queueTaskFixture("task-c", 0, ""), queueTaskFixture("task-d", 0, ""), queueTaskFixture("task-e", 0, "")}
	stub.setTasks("pending", extra)

	rec := doQueueAdminReq(t, o, http.MethodGet, "/admin/queues/transactional/tasks?state=retry")
	if rec.Code != http.StatusOK {
		t.Fatalf("list retry tasks = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Data QueueTaskPage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Data.Tasks) != 2 {
		t.Fatalf("got %d retry tasks, want 2: %+v", len(res.Data.Tasks), res.Data)
	}
	first := res.Data.Tasks[0]
	if first.ID != "task-a" || first.Type != queues.EmailSendTaskType {
		t.Fatalf("unexpected first task: %+v", first)
	}
	if first.PayloadSnippet != `{"email_id":42}` {
		t.Fatalf("payload snippet = %q", first.PayloadSnippet)
	}
	if first.Retried != 2 || first.LastErr != "dial tcp: i/o timeout" {
		t.Fatalf("retries/error not mapped: %+v", first)
	}
	if first.NextProcessAt == nil {
		t.Fatal("next_process_at missing for a retry task")
	}
	if first.LastFailedAt == nil {
		t.Fatal("last_failed_at missing for a task that failed")
	}
	second := res.Data.Tasks[1]
	if second.LastFailedAt != nil || second.LastErr != "" {
		t.Fatalf("task without failure must not carry failure metadata: %+v", second)
	}

	// Default state is pending; paging applies (size=2 → first 2 of 3).
	rec = doQueueAdminReq(t, o, http.MethodGet, "/admin/queues/bulk/tasks?size=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("default-state list = %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Data QueueTaskPage `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Data.Tasks) != 2 || page.Data.State != "pending" {
		t.Fatalf("default state/paging wrong: %+v", page.Data)
	}

	// An invalid state is a 400, an unknown queue a 404.
	if rec := doQueueAdminReq(t, o, http.MethodGet, "/admin/queues/transactional/tasks?state=bogus"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid state = %d, want 400", rec.Code)
	}
	if rec := doQueueAdminReq(t, o, http.MethodGet, "/admin/queues/nope/tasks"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown queue = %d, want 404", rec.Code)
	}
}

func TestAdminQueueTasksOnQueueThatNeverExisted(t *testing.T) {
	o, stub, _ := newQueueAdminServer(t)
	// asynq's inspector answers ErrQueueNotFound for a managed queue that has
	// never received a task (a fresh install). That is the empty state, not a
	// server error: the overview reports the same queue as zeros.
	stub.errOn["List:"+queues.Transactional] = asynq.ErrQueueNotFound

	rec := doQueueAdminReq(t, o, http.MethodGet, "/admin/queues/transactional/tasks")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty queue tasks = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Data QueueTaskPage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Data.Tasks) != 0 || res.Data.Total != 0 {
		t.Fatalf("empty queue returned tasks: %+v", res.Data)
	}
	if res.Data.Queue != queues.Transactional || res.Data.State != "pending" {
		t.Fatalf("queue/state not echoed: %+v", res.Data)
	}
}

func TestAdminQueueTaskActions(t *testing.T) {
	o, stub, evt := newQueueAdminServer(t)

	// Cancel deletes the task (pending/scheduled/retry cancel and archived
	// delete share the verb) and writes an audit event.
	rec := doQueueAdminReq(t, o, http.MethodPost, "/admin/queues/transactional/tasks/email%3Asend%3A42/cancel")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", rec.Code, rec.Body.String())
	}
	if stub.callCount("DeleteTask:transactional/") != 1 {
		t.Fatalf("cancel must call DeleteTask, calls: %v", stub.calls)
	}
	events := evt.events()
	if len(events) != 1 || events[0]["type"] != "queue.task.cancelled" {
		t.Fatalf("cancel must write one audit event, got %+v", events)
	}
	if got, _ := events[0]["message"].(string); !strings.Contains(got, "email:send:42") {
		t.Fatalf("audit event message should name the task: %v", got)
	}

	// Run re-enqueues dead-lettered tasks; archive parks them.
	if rec := doQueueAdminReq(t, o, http.MethodPost, "/admin/queues/bulk/tasks/task-9/run"); rec.Code != http.StatusOK {
		t.Fatalf("run = %d: %s", rec.Code, rec.Body.String())
	}
	if stub.callCount("RunTask:bulk/") != 1 {
		t.Fatal("run must call RunTask")
	}
	if rec := doQueueAdminReq(t, o, http.MethodPost, "/admin/queues/low/tasks/task-10/archive"); rec.Code != http.StatusOK {
		t.Fatalf("archive = %d: %s", rec.Code, rec.Body.String())
	}
	if stub.callCount("ArchiveTask:low/") != 1 {
		t.Fatal("archive must call ArchiveTask")
	}
	if len(evt.events()) != 3 {
		t.Fatalf("expected 3 audit events total, got %d", len(evt.events()))
	}

	// Not-found surfaces precisely.
	stub.errOn["DeleteTask:missing"] = asynq.ErrTaskNotFound
	if rec := doQueueAdminReq(t, o, http.MethodPost, "/admin/queues/transactional/tasks/missing/cancel"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing task = %d, want 404", rec.Code)
	}
	// Unknown queue 404s before Redis is touched.
	if rec := doQueueAdminReq(t, o, http.MethodPost, "/admin/queues/nope/tasks/x/cancel"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown queue cancel = %d, want 404", rec.Code)
	}
	// A conflicting state (e.g. RunTask on a pending task) is a 409.
	stub.errOn["RunTask:live"] = fmt.Errorf("task is not retriable: %w", errors.New("state pending"))
	if rec := doQueueAdminReq(t, o, http.MethodPost, "/admin/queues/bulk/tasks/live/run"); rec.Code != http.StatusConflict {
		t.Fatalf("conflicting action = %d, want 409", rec.Code)
	}
}

// A non-admin session must not reach the queue admin API.
func TestAdminQueuesRequireAdminRole(t *testing.T) {
	o, _, _ := newQueueAdminServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/queues"},
		{http.MethodPost, "/admin/queue/pause"},
		{http.MethodPost, "/admin/queue/resume"},
		{http.MethodPost, "/admin/queue/actions/requeue_stuck"},
		{http.MethodDelete, "/admin/queues/transactional/tasks/x"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
		rec := httptest.NewRecorder()
		o.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("non-admin %s %s = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

// Without an inspector (no worker configured) the endpoints degrade to a clear
// 503 instead of panicking.
func TestAdminQueuesUnavailableWithoutInspector(t *testing.T) {
	stub := newStubQueueInspector()
	_ = stub
	h := &AdminHandler{} // no queueInspector
	o := okapi.NewTestServer(t)
	adminAuth := middlewares.JWTAdminAuth(&config.Config{JWTSecret: logsTestJWTSecret})
	g := o.Group("/admin", adminAuth.Middleware)
	g.Get("/queues", h.ListQueues)

	req := httptest.NewRequest(http.MethodGet, "/admin/queues", nil)
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "admin"))
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-inspector GET /admin/queues = %d, want 503", rec.Code)
	}
}

// ---------- SF-52: destroy ----------

// DELETE deletes the task outright and writes the admin.queue.task_destroyed
// audit event; not-found and unknown-queue surface precisely.
func TestAdminQueueDestroyTask(t *testing.T) {
	o, stub, evt := newQueueAdminServer(t)

	rec := doQueueAdminReq(t, o, http.MethodDelete, "/admin/queues/transactional/tasks/email%3Asend%3A42")
	if rec.Code != http.StatusOK {
		t.Fatalf("destroy = %d: %s", rec.Code, rec.Body.String())
	}
	if stub.callCount("DeleteTask:transactional/") != 1 {
		t.Fatalf("destroy must call DeleteTask, calls: %v", stub.calls)
	}
	events := evt.events()
	if len(events) != 1 {
		t.Fatalf("destroy must write one audit event, got %d", len(events))
	}
	if events[0]["type"] != "admin.queue.task_destroyed" {
		t.Fatalf("audit event type = %v, want admin.queue.task_destroyed", events[0]["type"])
	}
	if got, _ := events[0]["message"].(string); !strings.Contains(got, "email:send:42") {
		t.Fatalf("audit event message should name the task: %v", got)
	}

	// A task asynq cannot delete (active) is a conflict; a missing task or
	// queue is a 404.
	stub.errOn["DeleteTask:live"] = fmt.Errorf("task is active: %w", errors.New("state active"))
	stub.errOn["DeleteTask:missing"] = asynq.ErrTaskNotFound
	if rec := doQueueAdminReq(t, o, http.MethodDelete, "/admin/queues/bulk/tasks/live"); rec.Code != http.StatusConflict {
		t.Fatalf("active task destroy = %d, want 409", rec.Code)
	}
	if rec := doQueueAdminReq(t, o, http.MethodDelete, "/admin/queues/transactional/tasks/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing task destroy = %d, want 404", rec.Code)
	}
	if rec := doQueueAdminReq(t, o, http.MethodDelete, "/admin/queues/nope/tasks/x"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown queue destroy = %d, want 404", rec.Code)
	}
}

// ---------- SF-54: global pause / resume ----------

// Pausing flips every managed queue, is audited, and is idempotent: a second
// pause reports the queues unchanged instead of failing. Resume mirrors it.
func TestAdminQueuePauseResume(t *testing.T) {
	o, stub, evt := newQueueAdminServer(t)

	rec := doQueueAdminReq(t, o, http.MethodPost, "/admin/queue/pause")
	if rec.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rec.Code, rec.Body.String())
	}
	for _, q := range adminQueues {
		if stub.callCount("PauseQueue:"+q) != 1 {
			t.Fatalf("pause must call PauseQueue for %s, calls: %v", q, stub.calls)
		}
	}
	var res struct {
		Data QueuePauseResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Data.Paused || len(res.Data.Queues) != len(adminQueues) {
		t.Fatalf("pause result wrong: %+v", res.Data)
	}
	for _, qs := range res.Data.Queues {
		if !qs.Paused || !qs.Changed {
			t.Fatalf("queue %s should be paused+changed: %+v", qs.Queue, qs)
		}
	}
	events := evt.events()
	if len(events) != 1 || events[0]["type"] != "admin.queue.paused" {
		t.Fatalf("pause must write one admin.queue.paused audit event, got %+v", events)
	}

	// Now the stub's queue info reflects the paused state: a second pause is a
	// no-op with changed=false, and asynq is never called again.
	for _, q := range adminQueues {
		stub.setQueue(q, &asynq.QueueInfo{Queue: q, Paused: true})
	}
	rec = doQueueAdminReq(t, o, http.MethodPost, "/admin/queue/pause")
	if rec.Code != http.StatusOK {
		t.Fatalf("second pause = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, qs := range res.Data.Queues {
		if qs.Changed {
			t.Fatalf("already-paused queue %s must report changed=false", qs.Queue)
		}
	}
	if stub.callCount("PauseQueue:") != len(adminQueues) {
		t.Fatal("second pause must not touch asynq again")
	}

	// Resume unpauses everything and audits the mirror event.
	rec = doQueueAdminReq(t, o, http.MethodPost, "/admin/queue/resume")
	if rec.Code != http.StatusOK {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Data.Paused {
		t.Fatal("resume must report paused=false")
	}
	for _, qs := range res.Data.Queues {
		if qs.Paused || !qs.Changed {
			t.Fatalf("queue %s should be resumed+changed: %+v", qs.Queue, qs)
		}
	}
	// The no-op pause was still an operator action and is audited too — three
	// events so far: paused, paused (idempotent), resumed.
	events = evt.events()
	if len(events) != 3 || events[len(events)-1]["type"] != "admin.queue.resumed" {
		t.Fatalf("resume must append an admin.queue.resumed audit event, got %+v", events)
	}

	// A queue refusing to flip is a 409 naming it, not a silent partial.
	stub.errOn["UnpauseQueue:"+queues.Bulk] = errors.New("redis: bulk locked")
	if rec := doQueueAdminReq(t, o, http.MethodPost, "/admin/queue/resume"); rec.Code != http.StatusConflict {
		t.Fatalf("failing resume = %d, want 409", rec.Code)
	}
}

// The overview carries the paused flag so the dashboard can badge it.
func TestAdminQueuesOverviewReportsPaused(t *testing.T) {
	o, stub, _ := newQueueAdminServer(t)
	stub.setQueue(queues.Transactional, &asynq.QueueInfo{
		Queue: queues.Transactional, Pending: 3, Paused: true,
	})

	rec := doQueueAdminReq(t, o, http.MethodGet, "/admin/queues")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/queues = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Data struct {
			Queues []QueueStats `json:"queues"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]QueueStats{}
	for _, q := range res.Data.Queues {
		byName[q.Queue] = q
	}
	if !byName[queues.Transactional].Paused {
		t.Fatal("transactional should report paused=true")
	}
	if byName[queues.Bulk].Paused {
		t.Fatal("bulk should report paused=false")
	}
}

// ---------- SF-59: operational actions ----------

// purge_archived_before deletes dead-lettered tasks whose archive timestamp is
// at or before the cutoff, reports the count, and is audited. Tasks that
// failed after the cutoff and unknown actions are refused.
func TestAdminQueueActionPurgeArchivedBefore(t *testing.T) {
	o, stub, evt := newQueueAdminServer(t)

	oldTask := queueTaskFixture("task-old", 3, "dial tcp: i/o timeout") // failed a minute ago
	manualTask := queueTaskFixture("task-manual", 0, "")                // parked without a failure: zero timestamp
	freshTask := queueTaskFixture("task-fresh", 1, "conn refused")      // failed a minute ago too...
	freshTask.LastFailedAt = time.Now().Add(time.Hour).Truncate(time.Second) // ...but push it after the cutoff
	stub.setTasks("archived", []*asynq.TaskInfo{oldTask, manualTask, freshTask})

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/queue/actions/purge_archived_before", body)
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "admin"))
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("purge = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Data QueueActionResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The stub serves the same archived set for every queue, so each of the
	// three managed queues purges its two eligible tasks.
	if res.Data.Action != "purge_archived_before" || res.Data.Purged != 6 {
		t.Fatalf("purge result wrong: %+v", res.Data)
	}
	if stub.callCount("DeleteTask:") < 6 {
		t.Fatalf("purge must delete the eligible tasks, calls: %v", stub.calls)
	}
	events := evt.events()
	if len(events) != 1 || events[0]["type"] != "admin.queue.action_run" {
		t.Fatalf("purge must write one admin.queue.action_run audit event, got %+v", events)
	}

	// A malformed cutoff is a 400, and so is an unknown action.
	resp := doQueueAdminJSON(t, o, http.MethodPost, "/admin/queue/actions/purge_archived_before", `{"before":"not-a-date"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("malformed before = %d, want 400", resp.Code)
	}
	resp = doQueueAdminJSON(t, o, http.MethodPost, "/admin/queue/actions/bogus", `{}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("unknown action = %d, want 400", resp.Code)
	}
}

// requeue_stuck runs the reconcile sweep. With the fake database the sweep
// finds no stuck email and reports zero; an unconfigured handler (no worker)
// answers 503.
func TestAdminQueueActionRequeueStuck(t *testing.T) {
	o, _, evt := newQueueAdminServer(t)

	resp := doQueueAdminJSON(t, o, http.MethodPost, "/admin/queue/actions/requeue_stuck", `{}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("requeue_stuck = %d: %s", resp.Code, resp.Body.String())
	}
	var res struct {
		Data QueueActionResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Data.Action != "requeue_stuck" || res.Data.Requeued != 0 {
		t.Fatalf("requeue result wrong: %+v", res.Data)
	}
	events := evt.events()
	if len(events) != 1 || events[0]["type"] != "admin.queue.action_run" {
		t.Fatalf("requeue must write one admin.queue.action_run audit event, got %+v", events)
	}

	// Without a producer (no worker configured) the action is unavailable.
	h := &QueueOpsHandler{db: fakeGorm(t, newFakeState())}
	o2 := okapi.NewTestServer(t)
	adminAuth := middlewares.JWTAdminAuth(&config.Config{JWTSecret: logsTestJWTSecret})
	g := o2.Group("/admin", adminAuth.Middleware)
	g.Post("/queue/actions/{action}", okapi.H(h.RunAction))
	req := httptest.NewRequest(http.MethodPost, "/admin/queue/actions/requeue_stuck", nil)
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "admin"))
	rec := httptest.NewRecorder()
	o2.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured requeue_stuck = %d, want 503", rec.Code)
	}
}

// doQueueAdminJSON performs one request with a JSON body against the queue
// admin test server.
func doQueueAdminJSON(t *testing.T, o *okapi.TestServer, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "admin"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}
