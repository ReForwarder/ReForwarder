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
	"github.com/goposta/posta/internal/cron"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SF-99 tests: the /admin/jobs/history + /admin/jobs/{name}/trigger endpoints
// through the real okapi stack with the real admin JWT middleware (the
// admin_queue_test.go arrangement). The run-history repo rides a fake
// database/sql driver over an in-memory cron_run_history table (the segment
// fake pattern); the handler drives a real cron.Manager over fake jobs, so
// the trigger tests exercise actual asynchronous runs.

const cronHistJWTSecret = "cron-history-test-secret"

func cronHistJWT(t *testing.T, role string) string {
	t.Helper()
	tok, err := okapi.GenerateJwtToken([]byte(cronHistJWTSecret), map[string]any{
		"sub":   7,
		"email": "user@example.com",
		"role":  role,
		"aud":   "posta",
		"jti":   fmt.Sprintf("jti-cron-hist-%s", role),
	}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateJwtToken: %v", err)
	}
	return tok
}

// ---------- the fake CronJob + manager ----------

type cronFakeJob struct {
	name  string
	runFn func() error
	ran   chan struct{}
	mu    sync.Mutex
	n     int
}

func newCronFakeJob(name string, runFn func() error) *cronFakeJob {
	return &cronFakeJob{name: name, runFn: runFn, ran: make(chan struct{}, 16)}
}

func (j *cronFakeJob) Name() string     { return j.name }
func (j *cronFakeJob) Schedule() string { return "0 3 * * *" }
func (j *cronFakeJob) Run(ctx context.Context, client *asynq.Client) error {
	j.mu.Lock()
	j.n++
	j.mu.Unlock()
	j.ran <- struct{}{}
	if j.runFn != nil {
		return j.runFn()
	}
	return nil
}

func (j *cronFakeJob) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.n
}

func (j *cronFakeJob) waitRan(t *testing.T) {
	t.Helper()
	select {
	case <-j.ran:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not start within 5s")
	}
}

func newCronManagerForTest(rec cron.RunHistoryRecorder, jobs ...cron.CronJob) *cron.Manager {
	m := cron.NewManager(nil)
	m.SetRunHistory(rec)
	for _, j := range jobs {
		m.Register(j)
	}
	return m
}

// recordingHistory (shared shape with the cron package's fake): an
// in-memory RunHistoryRecorder.
type recordingHistory struct {
	mu      sync.Mutex
	entries []*models.CronRunHistory
}

func (r *recordingHistory) RecordCronRun(entry *models.CronRunHistory) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *entry
	r.entries = append(r.entries, &cp)
	return nil
}

func (r *recordingHistory) all() []*models.CronRunHistory {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*models.CronRunHistory(nil), r.entries...)
}

// ---------- the fake cron_run_history store ----------

var crhCols = []string{"id", "job_name", "trigger", "started_at", "finished_at", "outcome", "detail", "created_at"}

type crhState struct {
	mu   sync.Mutex
	rows [][]driver.Value
	seq  int64
}

func newCRHState() *crhState { return &crhState{seq: 500} }

func crhVal(v any) driver.Value {
	switch t := v.(type) {
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case string:
		return t
	case time.Time:
		return t
	default:
		panic(fmt.Sprintf("crhVal: unsupported %T", v))
	}
}

func (s *crhState) seed(jobName string, startedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	row := []driver.Value{s.seq, jobName, "schedule", startedAt,
		startedAt.Add(time.Second), "success", "", startedAt}
	s.rows = append(s.rows, row)
}

func crhNormalize(q string) string {
	q = strings.ReplaceAll(q, `"`, "")
	return strings.Join(strings.Fields(strings.ToLower(q)), " ")
}

func crhArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return crhVal(a.Value)
		}
	}
	panic(fmt.Sprintf("crh fake: missing bind var $%d", n))
}

func (s *crhState) match(preds []crhPredicate, args []driver.NamedValue, argStart int) [][]driver.Value {
	out := make([][]driver.Value, 0, len(s.rows))
	for _, row := range s.rows {
		ok := true
		argN := argStart
		for _, p := range preds {
			idx := -1
			for i, c := range crhCols {
				if c == p.col {
					idx = i
					break
				}
			}
			if idx < 0 {
				ok = false
				break
			}
			av := crhArg(args, argN)
			argN++
			cell := row[idx]
			fv, ok2 := av.(string)
			cv, ok3 := cell.(string)
			if !ok2 || !ok3 || cv != fv {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, row)
		}
	}
	return out
}

func crhParseWhere(where string) []crhPredicate {
	var preds []crhPredicate
	for _, part := range strings.Split(where, " and ") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, "="); i >= 0 {
			preds = append(preds, crhPredicate{
				col: strings.TrimSpace(strings.ReplaceAll(part[:i], `"`, "")),
			})
		}
	}
	return preds
}

type crhPredicate struct {
	col string
}

type crhConn struct{ s *crhState }

func (c *crhConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("crh fake: no prepare") }
func (c *crhConn) Close() error                        { return nil }
func (c *crhConn) Begin() (driver.Tx, error)           { return crhTx{}, nil }

type crhTx struct{}

func (crhTx) Commit() error   { return nil }
func (crhTx) Rollback() error { return nil }

type crhResult struct{ rows int64 }

func (r crhResult) LastInsertId() (int64, error) { return 0, nil }
func (r crhResult) RowsAffected() (int64, error) { return r.rows, nil }

type crhRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *crhRows) Columns() []string { return r.cols }
func (r *crhRows) Close() error      { return nil }
func (r *crhRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type crhConnector struct{ state *crhState }

func (c crhConnector) Connect(context.Context) (driver.Conn, error) { return &crhConn{s: c.state}, nil }
func (c crhConnector) Driver() driver.Driver                        { return crhDrv{} }

type crhDrv struct{}

func (crhDrv) Open(string) (driver.Conn, error) { return nil, errors.New("crh fake: use sql.OpenDB") }

var crhDriverOnce sync.Once

func crhGorm(t *testing.T, state *crhState) *gorm.DB {
	t.Helper()
	crhDriverOnce.Do(func() {
		sql.Register("cron-history-fake", crhDrv{})
	})
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(crhConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open crh fake gorm: %v", err)
	}
	return gdb
}

func (c *crhConn) whereOf(lower string) string {
	i := strings.Index(lower, " where ")
	if i < 0 {
		return ""
	}
	rest := lower[i+len(" where "):]
	for _, kw := range []string{" order by ", " limit ", " offset "} {
		if j := strings.Index(rest, kw); j >= 0 {
			rest = rest[:j]
		}
	}
	return strings.TrimSpace(rest)
}

func (c *crhConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	lower := crhNormalize(query)
	switch {
	case strings.HasPrefix(lower, "select count(*)"):
		preds := crhParseWhere(c.whereOf(lower))
		return &crhRows{cols: []string{"count"},
			rows: [][]driver.Value{{int64(len(s.match(preds, args, 1)))}}}, nil
	case strings.Contains(lower, "from cron_run_history"):
		preds := crhParseWhere(c.whereOf(lower))
		matched := s.match(preds, args, 1)
		// Newest first: seeds ascend in time, so reverse; tie-break by id
		// (seed order) is the same reversal for equal timestamps.
		for i := 0; i < len(matched)/2; i++ {
			j := len(matched) - 1 - i
			matched[i], matched[j] = matched[j], matched[i]
		}
		offset, limit := 0, len(matched)
		if i := strings.Index(lower, " offset "); i >= 0 {
			tok := strings.Fields(lower[i+len(" offset "):])[0]
			n, err := strconv.Atoi(strings.TrimPrefix(tok, "$"))
			if err != nil {
				return nil, fmt.Errorf("crh fake: bad offset %q", tok)
			}
			offset = int(crhArg(args, n).(int64))
		}
		if i := strings.Index(lower, " limit "); i >= 0 {
			tok := strings.Fields(lower[i+len(" limit "):])[0]
			n, err := strconv.Atoi(strings.TrimPrefix(tok, "$"))
			if err != nil {
				return nil, fmt.Errorf("crh fake: bad limit %q", tok)
			}
			limit = int(crhArg(args, n).(int64))
		}
		if offset > len(matched) {
			matched = nil
		} else {
			matched = matched[offset:]
			if limit < len(matched) {
				matched = matched[:limit]
			}
		}
		rows := make([][]driver.Value, 0, len(matched))
		for _, r := range matched {
			rows = append(rows, append([]driver.Value(nil), r...))
		}
		return &crhRows{cols: crhCols, rows: rows}, nil
	}
	return nil, fmt.Errorf("crh fake: cannot route query: %s", query)
}

func (c *crhConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	lower := crhNormalize(query)
	if !strings.HasPrefix(lower, "insert into cron_run_history") {
		return nil, fmt.Errorf("crh fake: unsupported exec: %s", query)
	}
	open := strings.Index(lower, "(")
	closeIdx := strings.Index(lower[open:], ")")
	cols := strings.Split(lower[open+1:open+closeIdx], ",")
	valsStart := strings.Index(lower, " values (")
	inner := lower[valsStart+len(" values ("):]
	inner = inner[:strings.Index(inner, ")")]
	vals := strings.Split(inner, ",")
	if len(vals) != len(cols) {
		return nil, fmt.Errorf("crh fake: insert arity mismatch")
	}
	row := make([]driver.Value, len(crhCols))
	for i, col := range cols {
		col = strings.TrimSpace(strings.ReplaceAll(col, `"`, ""))
		idx := -1
		for j, cc := range crhCols {
			if cc == col {
				idx = j
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("crh fake: unknown insert column %q", col)
		}
		tok := strings.TrimSpace(vals[i])
		if strings.HasPrefix(tok, "$") {
			n, _ := strconv.Atoi(tok[1:])
			row[idx] = crhArg(args, n)
		}
	}
	s.seq++
	row[0] = s.seq
	s.rows = append(s.rows, row)
	return crhResult{rows: 1}, nil
}

// ---------- the harness ----------

type crhHarness struct {
	o     *okapi.TestServer
	state *crhState
	rec   *recordingHistory
	okJob *cronFakeJob
}

func newCRHHarness(t *testing.T) *crhHarness {
	t.Helper()
	state := newCRHState()
	db := crhGorm(t, state)
	rec := &recordingHistory{}

	okJob := newCronFakeJob("ok_job", nil)
	failJob := newCronFakeJob("fail_job", func() error { return errors.New("boom: job exploded") })

	// A real manager over fake jobs; the recording seam is the in-memory
	// fake (the history READ rides the fake-driver-backed repo).
	cr := NewCronHandler(newCronManagerForTest(rec, okJob, failJob))
	cr.SetRunHistoryRepo(repositories.NewCronRunHistoryRepository(db))

	o := okapi.NewTestServer(t)
	adminAuth := middlewares.JWTAdminAuth(&config.Config{JWTSecret: cronHistJWTSecret})
	g := o.Group("/admin", adminAuth.Middleware)
	g.Get("/jobs/history", okapi.H(cr.ListHistory))
	g.Post("/jobs/{name}/trigger", okapi.H(cr.TriggerJob))
	return &crhHarness{o: o, state: state, rec: rec, okJob: okJob}
}

func (h *crhHarness) do(t *testing.T, role, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+cronHistJWT(t, role))
	rec := httptest.NewRecorder()
	h.o.ServeHTTP(rec, req)
	return rec
}

// ---------- tests ----------

// TestCronHistory_RoleGating pins the gate: the whole /admin/jobs surface is
// admin-only — a plain user's session answers 403 on both the history read
// and the trigger, before any handler logic runs.
func TestCronHistory_RoleGating(t *testing.T) {
	h := newCRHHarness(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/jobs/history"},
		{http.MethodPost, "/admin/jobs/ok_job/trigger"},
	} {
		rec := h.do(t, "user", tc.method, tc.path)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s as user = %d: %s, want 403", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}

	// The admin session passes the same gate.
	rec := h.do(t, "admin", http.MethodGet, "/admin/jobs/history")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/jobs/history as admin = %d: %s, want 200", rec.Code, rec.Body.String())
	}
}

// TestCronHistory_PagedAndFiltered pins the history read: rows come back
// newest first with the pageable envelope, the job_name filter narrows to
// one job, and the pager carries the filtered total.
func TestCronHistory_PagedAndFiltered(t *testing.T) {
	h := newCRHHarness(t)

	base := time.Now().UTC().Add(-time.Hour)
	// Three ok_job rows and two fail_job rows, ascending in time.
	for i := 0; i < 3; i++ {
		h.state.seed("ok_job", base.Add(time.Duration(i)*time.Minute))
	}
	for i := 0; i < 2; i++ {
		h.state.seed("fail_job", base.Add(time.Duration(10+i)*time.Minute))
	}

	rec := h.do(t, "admin", http.MethodGet, "/admin/jobs/history?page=0&size=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("history GET = %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Success  bool `json:"success"`
		Data     []struct {
			ID      int    `json:"id"`
			JobName string `json:"job_name"`
		} `json:"data"`
		Pageable struct {
			CurrentPage   int   `json:"current_page"`
			Size          int   `json:"size"`
			TotalPages    int   `json:"total_pages"`
			TotalElements int64 `json:"total_elements"`
			Empty         bool  `json:"empty"`
		} `json:"pageable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode history page: %v", err)
	}
	if !page.Success || len(page.Data) != 2 {
		t.Fatalf("page = %+v, want success with 2 rows", page)
	}
	if page.Data[0].JobName != "fail_job" || page.Data[1].JobName != "fail_job" {
		t.Fatalf("rows = %v/%v, want the newest (fail_job) first", page.Data[0].JobName, page.Data[1].JobName)
	}
	if page.Pageable.TotalElements != 5 || page.Pageable.TotalPages != 3 {
		t.Fatalf("pageable = %+v, want total 5 over 3 pages", page.Pageable)
	}

	// The job_name filter narrows the window and the total.
	rec = h.do(t, "admin", http.MethodGet, "/admin/jobs/history?job_name=ok_job&page=0&size=25")
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered history GET = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode filtered page: %v", err)
	}
	if len(page.Data) != 3 || page.Pageable.TotalElements != 3 {
		t.Fatalf("filtered page = %+v, want exactly the 3 ok_job rows", page)
	}
	for _, row := range page.Data {
		if row.JobName != "ok_job" {
			t.Fatalf("filtered row job = %q, want ok_job", row.JobName)
		}
	}
}

// TestCronTrigger_Lifecycle pins the trigger contract end to end: 202 on a
// claimed manual run (the run executes asynchronously and records a
// trigger=manual history row), 404 for an unknown job name (isolation:
// missing reads as missing), 409 for a mid-run job (a second trigger is a
// no-op — the job runs exactly once), and the job status clears when the
// run finishes.
func TestCronTrigger_Lifecycle(t *testing.T) {
	release := make(chan struct{})
	var blocked struct {
		mu  sync.Mutex
		fn  func() error
	}
	blocked.fn = func() error {
		<-release
		return nil
	}
	blockingJob := newCronFakeJob("blocking_job", blocked.fn)
	failJob := newCronFakeJob("fail_job", func() error { return errors.New("boom: job exploded") })

	state := newCRHState()
	db := crhGorm(t, state)
	rec := &recordingHistory{}
	cr := NewCronHandler(newCronManagerForTest(rec, blockingJob, failJob))
	cr.SetRunHistoryRepo(repositories.NewCronRunHistoryRepository(db))
	o := okapi.NewTestServer(t)
	adminAuth := middlewares.JWTAdminAuth(&config.Config{JWTSecret: cronHistJWTSecret})
	g := o.Group("/admin", adminAuth.Middleware)
	g.Post("/jobs/{name}/trigger", okapi.H(cr.TriggerJob))
	g.Get("/jobs", cr.List)

	// Unknown job: 404, nothing runs.
	rec1 := h_doTrigger(o, t, "no_such_job")
	if rec1.Code != http.StatusNotFound {
		t.Fatalf("trigger unknown = %d: %s, want 404", rec1.Code, rec1.Body.String())
	}
	if blockingJob.count() != 0 || failJob.count() != 0 {
		t.Fatal("no job may run for an unknown name")
	}

	// Trigger the blocking job: 202, the run starts asynchronously.
	rec2 := h_doTrigger(o, t, "blocking_job")
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("trigger = %d: %s, want 202", rec2.Code, rec2.Body.String())
	}
	blockingJob.waitRan(t) // the run started

	// The status surface shows Running while mid-run; the trigger answers
	// 409 and the job still runs exactly once.
	var status struct {
		Data []struct {
			Name    string `json:"name"`
			Running bool   `json:"running"`
		} `json:"data"`
	}
	statusRes := h_doList(o, t)
	if err := json.Unmarshal(statusRes.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode jobs: %v", err)
	}
	found := false
	for _, j := range status.Data {
		if j.Name == "blocking_job" {
			found = true
			if !j.Running {
				t.Fatal("mid-run job must report running=true")
			}
		}
	}
	if !found {
		t.Fatal("the triggered job must appear in the status surface")
	}
	rec3 := h_doTrigger(o, t, "blocking_job")
	if rec3.Code != http.StatusConflict {
		t.Fatalf("second trigger = %d: %s, want 409", rec3.Code, rec3.Body.String())
	}
	if blockingJob.count() != 1 {
		t.Fatalf("blocking job ran %d times, want exactly 1", blockingJob.count())
	}

	// A manual run that FAILS records the failure with the error detail.
	releaseFail := make(chan struct{})
	failJob.runFn = func() error {
		<-releaseFail
		return errors.New("boom: job exploded")
	}
	rec4 := h_doTrigger(o, t, "fail_job")
	if rec4.Code != http.StatusAccepted {
		t.Fatalf("trigger fail_job = %d: %s, want 202", rec4.Code, rec4.Body.String())
	}
	failJob.waitRan(t)
	close(releaseFail)

	// The failed run finishes asynchronously: wait for its history row.
	var failed *models.CronRunHistory
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, e := range rec.all() {
			if e.JobName == "fail_job" {
				failed = e
			}
		}
		if failed != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if failed == nil {
		t.Fatalf("history = %+v, want the fail_job row", rec.all())
	}
	if failed.Trigger != "manual" || failed.Outcome != "failed" || failed.Detail != "boom: job exploded" {
		t.Fatalf("failed row = %+v, want manual/failed with the error detail", failed)
	}

	close(release)
}

func h_doTrigger(o *okapi.TestServer, t *testing.T, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/jobs/"+name+"/trigger", nil)
	req.Header.Set("Authorization", "Bearer "+cronHistJWT(t, "admin"))
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

func h_doList(o *okapi.TestServer, t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/jobs", nil)
	req.Header.Set("Authorization", "Bearer "+cronHistJWT(t, "admin"))
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}
