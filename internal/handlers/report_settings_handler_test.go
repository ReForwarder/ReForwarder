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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SF-63 tests: the per-domain report-settings CRUD (GET + PATCH on
// /domains/{id}/report-settings) with owner-scoped isolation, on the
// dmarc_handler_test fake-driver arrangement. The fake evaluates the WHERE
// clauses the repos issue (id = $n for FindByID, the ownership-verified +
// name predicates for the settings lookup) and applies the settings UPDATE
// to the matching row, so the persistence assertions read back the writes.

const rshDomainColumns = "id,user_id,workspace_id,domain,ownership_verified,report_rua,report_interval_hours,report_max_bytes,created_at"

type rshState struct {
	mu   sync.Mutex
	rows [][]driver.Value
	seq  int64
}

func newRshState() *rshState { return &rshState{seq: 1500} }

func rshVal(v any) driver.Value {
	switch t := v.(type) {
	case uint:
		return int64(t)
	case *uint:
		if t == nil {
			return nil
		}
		return int64(*t)
	case int:
		return int64(t)
	case int64:
		return t
	case string:
		return t
	case time.Time:
		return t
	case bool:
		return t
	default:
		panic(fmt.Sprintf("rshVal: unsupported %T", v))
	}
}

var rshCols = strings.Split(rshDomainColumns, ",")

func (s *rshState) colIndex(name string) int {
	for i, c := range rshCols {
		if c == name {
			return i
		}
	}
	return -1
}

func (s *rshState) seed(t *testing.T, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	row := make([]driver.Value, len(rshCols))
	for i, c := range rshCols {
		if v, ok := vals[c]; ok {
			row[i] = rshVal(v)
		} else {
			row[i] = nil
		}
	}
	if row[s.colIndex("id")] == nil {
		s.seq++
		row[s.colIndex("id")] = s.seq
	}
	s.rows = append(s.rows, row)
}

func rshGorm(t *testing.T, state *rshState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(rshConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open rsh fake gorm: %v", err)
	}
	return gdb
}

type rshConnector struct{ state *rshState }

func (c rshConnector) Connect(context.Context) (driver.Conn, error) { return &rshConn{s: c.state}, nil }
func (c rshConnector) Driver() driver.Driver                        { return rshDrv{} }

type rshDrv struct{}

func (rshDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type rshConn struct{ s *rshState }

func (c *rshConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *rshConn) Close() error                        { return nil }
func (c *rshConn) Begin() (driver.Tx, error)           { return rshTx{}, nil }

type rshTx struct{}

func (rshTx) Commit() error   { return nil }
func (rshTx) Rollback() error { return nil }

type rshRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *rshRows) Columns() []string { return r.cols }
func (r *rshRows) Close() error      { return nil }
func (r *rshRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type rshResult struct{ rows int64 }

func (r rshResult) LastInsertId() (int64, error) { return 0, nil }
func (r rshResult) RowsAffected() (int64, error) { return r.rows, nil }

func (c *rshConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if !strings.Contains(lower, `from "domains"`) {
		return nil, fmt.Errorf("rshState: cannot route query: %s", query)
	}

	// WHERE evaluation: `col = $n` and `col = $n and ...` predicates, plus the
	// ownership_verified gate. The FindByID lookup carries id = $n; the
	// settings lookup carries domain = $n AND ownership_verified = true.
	type pred func(row []driver.Value) bool
	var preds []pred
	predFor := func(cond string) (pred, error) {
		eq := strings.Index(cond, " = ")
		if eq < 0 {
			return nil, fmt.Errorf("rshState: unsupported condition %q", cond)
		}
		col := strings.Trim(strings.TrimSpace(cond[:eq]), `"`)
		// gorm qualifies columns with the table name and quotes the parts
		// (`"domains"."id"`) — strip every quote and the qualifier so the
		// fixture's column index matches.
		col = strings.ReplaceAll(col, `"`, "")
		if dot := strings.LastIndex(col, "."); dot >= 0 {
			col = col[dot+1:]
		}
		idx := s.colIndex(col)
		if idx < 0 {
			return nil, fmt.Errorf("rshState: unknown column %q", col)
		}
		rhs := strings.TrimSpace(cond[eq+3:])
		n, err := strconv.Atoi(strings.TrimPrefix(rhs, "$"))
		if err != nil {
			return nil, fmt.Errorf("rshState: bad bindvar in %q", cond)
		}
		want := rshArg(args, n)
		return func(row []driver.Value) bool { return rshEqual(row[idx], want) }, nil
	}
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = lower[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit "} {
		if j := strings.Index(where, cut); j >= 0 {
			where = where[:j]
		}
	}
	if where != "" {
		for _, cond := range strings.Split(where, " and ") {
			cond = strings.TrimSpace(cond)
			if cond == "" {
				continue
			}
			p, err := predFor(cond)
			if err != nil {
				return nil, err
			}
			preds = append(preds, p)
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
	return &rshRows{cols: rshCols, rows: out}, nil
}

func (c *rshConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if !strings.Contains(lower, `update "domains"`) {
		return nil, fmt.Errorf("rshState: cannot route exec: %s", query)
	}
	// Apply the settings columns from the SET bindvars: the three SF-63
	// columns ride fixed bind positions after gorm's Save rewrites the full
	// column set — locate them by the SET clause's column names instead.
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("rshState: malformed update: %s", lower)
	}
	setClause := lower[setStart+5 : whereStart]
	// gorm quotes the WHERE column (`"id" = $n`) — strip the quotes so the
	// id-predicate extraction matches.
	where := strings.ReplaceAll(lower[whereStart+7:], `"`, "")
	// Collect the SET column → bindvar ordinals.
	type setter struct{ col, ord string }
	var setters []setter
	for _, pair := range strings.Split(setClause, ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("rshState: malformed SET pair %q", pair)
		}
		col := strings.Trim(strings.TrimSpace(pair[:eq]), `"`)
		ord := strings.TrimSpace(pair[eq+1:])
		setters = append(setters, setter{col: strings.Trim(col, `"`), ord: strings.TrimPrefix(ord, "$")})
	}
	// WHERE: id = $n.
	idOrd := ""
	if i := strings.Index(where, "id = "); i >= 0 {
		idOrd = strings.TrimSpace(where[i+5:])
		if strings.HasPrefix(idOrd, "$") {
			idOrd = idOrd[1:]
		} else {
			idOrd = ""
		}
	}
	if idOrd == "" {
		fmt.Fprintln(os.Stderr, "rshState: settings update without an id predicate:", lower)
		return nil, fmt.Errorf("rshState: settings update without an id predicate")
	}
	wantID := rshArg(args, atoi(idOrd))
	fmt.Fprintf(os.Stderr, "rshState: settings exec id=%v setters=%d args=%v", wantID, len(setters), args)
	matched := 0
	for _, row := range s.rows {
		id, _ := row[s.colIndex("id")].(int64)
		if id != wantID {
			continue
		}
		matched++
		for _, st := range setters {
			idx := s.colIndex(st.col)
			if idx < 0 {
				continue
			}
			row[idx] = rshArg(args, atoi(st.ord))
		}
	}
	return rshResult{rows: int64(matched)}, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func rshArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("rshState: no arg with ordinal %d", n))
}

func rshEqual(a, b driver.Value) bool {
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

func newRshServer(t *testing.T, state *rshState) (*okapi.TestServer, *rshState) {
	t.Helper()
	gdb := rshGorm(t, state)
	h := NewReportObjectHandler(
		repositories.NewDMARCSentReportRepository(gdb),
		repositories.NewARFReportRepository(gdb),
		repositories.NewDomainRepository(gdb),
	)
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		dmrWorkspaceBinding,
	)
	g.Get("/domains/{id:int}/report-settings", okapi.H(h.GetReportSettings))
	probe := okapi.Middleware(func(c *okapi.Context) error {
		b, _ := io.ReadAll(c.Request().Body)
		t.Logf("PROBE ct=%q body_len=%d", c.ContentType(), len(b))
		// Restore the consumed body so the downstream binder can read it.
		c.Request().Body = io.NopCloser(strings.NewReader(string(b)))
		return c.Next()
	})
	g.Use(probe)
	g.Patch("/domains/{id:int}/report-settings", okapi.H(h.SetReportSettings))
	return o, state
}

func rshPatchBody(rua string, hours, maxBytes int) string {
	// The payload is FLAT: okapi's Body-field special case re-binds the
	// request body into the handler's Body struct (the SetMTASTSMode
	// precedent — the client never wraps the payload in a "body" key).
	return `{"rua":"` + rua + `","report_interval_hours":` + strconv.Itoa(hours) + `,"report_max_bytes":` + strconv.Itoa(maxBytes) + `}`
}

// rshDo sends an authenticated request with an optional JSON body and an
// optional workspace-role override (the binding defaults to admin).
func rshDo(t *testing.T, o *okapi.TestServer, method, path, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
	req.Header.Set("X-Test-Workspace", dmrTestWorkspace)
	if role != "" {
		req.Header.Set("X-Test-Workspace-Role", role)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.ContentLength = int64(len(body))
	t.Logf("rshDo %s %s body_len=%d", method, path, len(body))
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

func rshGetSettings(t *testing.T, o *okapi.TestServer, domainID int) (int, struct {
	Success bool `json:"success"`
	Data    struct {
		Domain   string `json:"domain"`
		Settings struct {
			Rua           string `json:"rua"`
			IntervalHours int    `json:"report_interval_hours"`
			MaxBytes      int    `json:"report_max_bytes"`
		} `json:"settings"`
	} `json:"data"`
}) {
	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/"+strconv.Itoa(domainID)+"/report-settings", "")
	var res struct {
		Success bool `json:"success"`
		Data    struct {
			Domain   string `json:"domain"`
			Settings struct {
				Rua           string `json:"rua"`
				IntervalHours int    `json:"report_interval_hours"`
				MaxBytes      int    `json:"report_max_bytes"`
			} `json:"settings"`
		} `json:"data"`
	}
	err := json.Unmarshal(rec.Body.Bytes(), &res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rec.Code, res
}

// The GET returns the domain's stored settings; a foreign domain answers 404
// (isolation).
func TestReportSettingsGetAndIsolation(t *testing.T) {
	state := newRshState()
	state.seed(t, map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true,
		"report_rua": "reports@example.com", "report_interval_hours": 48, "report_max_bytes": 1048576,
	})
	state.seed(t, map[string]any{
		"id": uint(11), "user_id": uint(42), "workspace_id": wsID(99),
		"domain": "foreign.com", "ownership_verified": true,
	})
	o, _ := newRshServer(t, state)

	code, res := rshGetSettings(t, o, 10)
	if code != http.StatusOK {
		t.Fatalf("settings get = %d", code)
	}
	if res.Data.Domain != "example.com" || res.Data.Settings.Rua != "reports@example.com" ||
		res.Data.Settings.IntervalHours != 48 || res.Data.Settings.MaxBytes != 1048576 {
		t.Fatalf("settings = %+v", res.Data.Settings)
	}

	foreign := rshDo(t, o, http.MethodGet, "/workspaces/current/domains/11/report-settings", "", "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign domain settings = %d, want 404 (isolation)", foreign.Code)
	}
}

// The PATCH persists the settings (a re-GET reflects the written columns).
func TestReportSettingsPatchPersists(t *testing.T) {
	state := newRshState()
	state.seed(t, map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true,
	})
	o, _ := newRshServer(t, state)

	rec := rshDo(t, o, http.MethodPatch, "/workspaces/current/domains/10/report-settings", "",
		rshPatchBody("reports@example.com, dmarc@other.example", 72, 2097152))
	if rec.Code != http.StatusOK {
		t.Fatalf("settings patch = %d: %s", rec.Code, rec.Body.String())
	}

	code, res := rshGetSettings(t, o, 10)
	if code != http.StatusOK {
		t.Fatalf("settings re-get = %d", code)
	}
	if res.Data.Settings.IntervalHours != 72 || res.Data.Settings.MaxBytes != 2097152 {
		t.Fatalf("persisted settings = %+v, want the patched values", res.Data.Settings)
	}
	if !strings.Contains(res.Data.Settings.Rua, "reports@example.com") || !strings.Contains(res.Data.Settings.Rua, "dmarc@other.example") {
		t.Fatalf("persisted rua = %q, want both addresses", res.Data.Settings.Rua)
	}
}

// Invalid settings answer 400 without touching the row: a malformed rua
// entry, an out-of-range interval, an out-of-range size policy.
func TestReportSettingsPatchValidation(t *testing.T) {
	state := newRshState()
	state.seed(t, map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true,
	})
	o, _ := newRshServer(t, state)

	cases := map[string]string{
		"bad rua":         rshPatchBody("not-an-address", 24, 0),
		"bad interval":    rshPatchBody("reports@example.com", 9000, 0),
		"negative bytes":  rshPatchBody("reports@example.com", 24, -5),
		"too-small bytes": rshPatchBody("reports@example.com", 24, 5),
		"oversize bytes":  rshPatchBody("reports@example.com", 24, 32*1024*1024),
	}
	for name, body := range cases {
		rec := rshDo(t, o, http.MethodPatch, "/workspaces/current/domains/10/report-settings", "", body)
		t.Logf("%s: patch response = %d %s", name, rec.Code, rec.Body.String())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: patch = %d, want 400", name, rec.Code)
		}
	}
	code, res := rshGetSettings(t, o, 10)
	_ = code
	if res.Data.Settings.Rua != "" || res.Data.Settings.IntervalHours != 0 || res.Data.Settings.MaxBytes != 0 {
		t.Fatalf("settings mutated by rejected patches: %+v", res.Data.Settings)
	}
}

// An unverified domain cannot set report settings (the DNS-control proof
// makes the tenant the authority over reports about its own domain).
func TestReportSettingsPatchRequiresVerifiedDomain(t *testing.T) {
	state := newRshState()
	state.seed(t, map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "unverified.example.com", "ownership_verified": false,
	})
	o, _ := newRshServer(t, state)

	rec := rshDo(t, o, http.MethodPatch, "/workspaces/current/domains/10/report-settings", "",
		rshPatchBody("reports@example.com", 24, 0))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unverified-domain patch = %d, want 400 (verified-domain gate)", rec.Code)
	}
}
