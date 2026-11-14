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
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SF-50 tests: the transport-policy admin surface (the ADR-001 amendment's
// workspace-scoped domain-policy CRUD) over the repo's established fake
// database/sql driver arrangement (the report-settings handler test's
// pattern): real handler + real admin JWT middleware + fake tables, so the
// validation gates, the ownership isolation and the CRUD round trip run in
// every battery without a live database.

const tpolJWTSecret = "transport-policy-test-secret"

func tpolJWT(t *testing.T, role string, ws int) string {
	t.Helper()
	tok, err := okapi.GenerateJwtToken([]byte(tpolJWTSecret), map[string]any{
		"sub":   7,
		"email": "owner@example.org",
		"role":  role,
		"aud":   "posta",
		"jti":   fmt.Sprintf("jti-tpol-%s-%d", role, ws),
	}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateJwtToken: %v", err)
	}
	return tok
}

// ---------- the fake tables ----------

var tpolColumns = map[string][]string{
	"domains": {"id", "user_id", "workspace_id", "domain", "ownership_verified", "created_at"},
	"transport_policies": {"id", "workspace_id", "domain_name", "name", "priority", "enabled",
		"conditions", "reject", "reject_reason", "bind_ip", "msg_rate_per_min", "max_message_size",
		"hourly_budget", "created_at", "updated_at"},
}

type tpolState struct {
	mu   sync.Mutex
	rows map[string][][]driver.Value
	seq  int64
}

func newTpolState() *tpolState {
	return &tpolState{rows: map[string][][]driver.Value{}, seq: 900}
}

func tpolVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
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
	case bool:
		return t
	case []uint8:
		return string(t)
	default:
		panic(fmt.Sprintf("tpolVal: unsupported %T", v))
	}
}

func (s *tpolState) colIndex(table, col string) int {
	for i, c := range tpolColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

func (s *tpolState) seed(table string, vals map[string]any) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	row := make([]driver.Value, len(tpolColumns[table]))
	for i, c := range tpolColumns[table] {
		if v, ok := vals[c]; ok {
			row[i] = tpolVal(v)
		}
	}
	row[s.colIndex(table, "id")] = s.seq
	s.rows[table] = append(s.rows[table], row)
	return s.seq
}

func tpolNormalize(q string) string {
	q = strings.ReplaceAll(q, `"`, "")
	return strings.Join(strings.Fields(strings.ToLower(q)), " ")
}

func tpolArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return tpolVal(a.Value)
		}
	}
	panic(fmt.Sprintf("tpol fake: missing bind var $%d", n))
}

type tpolPred struct {
	col  string
	op   string
	argN int
}

func tpolParseWhere(where string, argStart int) ([]tpolPred, int) {
	var preds []tpolPred
	argN := argStart
	for _, part := range strings.Split(where, " and ") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, "="); i >= 0 {
			col := strings.TrimSpace(strings.ReplaceAll(part[:i], `"`, ""))
			// gorm table-qualifies columns ("domains"."id") — strip the
			// qualifier (the established fake-driver convention).
			if j := strings.LastIndex(col, "."); j >= 0 {
				col = col[j+1:]
			}
			preds = append(preds, tpolPred{col: col, op: "=", argN: argN})
			argN++
		}
	}
	return preds, argN
}

func (s *tpolState) match(table string, preds []tpolPred, args []driver.NamedValue) [][]driver.Value {
	out := make([][]driver.Value, 0, len(s.rows[table]))
	for _, row := range s.rows[table] {
		ok := true
		for _, p := range preds {
			idx := s.colIndex(table, p.col)
			if idx < 0 {
				ok = false
				break
			}
			av := tpolArg(args, p.argN)
			cell := row[idx]
			eq := false
			switch cv := cell.(type) {
			case int64:
				fv, ok2 := av.(int64)
				eq = ok2 && cv == fv
			case string:
				fv, ok2 := av.(string)
				eq = ok2 && cv == fv
			case bool:
				fv, ok2 := av.(bool)
				eq = ok2 && cv == fv
			}
			if !eq {
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

type tpolConn struct{ s *tpolState }

func (c *tpolConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("tpol fake: no prepare") }
func (c *tpolConn) Close() error                        { return nil }
func (c *tpolConn) Begin() (driver.Tx, error)           { return tpolTx{}, nil }

type tpolTx struct{}

func (tpolTx) Commit() error   { return nil }
func (tpolTx) Rollback() error { return nil }

type tpolResult struct {
	rows int64
	id   int64
}

func (r tpolResult) LastInsertId() (int64, error) { return r.id, nil }
func (r tpolResult) RowsAffected() (int64, error) { return r.rows, nil }

type tpolRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *tpolRows) Columns() []string { return r.cols }
func (r *tpolRows) Close() error      { return nil }
func (r *tpolRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type tpolConnector struct{ state *tpolState }

func (c tpolConnector) Connect(context.Context) (driver.Conn, error) { return &tpolConn{s: c.state}, nil }
func (c tpolConnector) Driver() driver.Driver                        { return tpolDrv{} }

type tpolDrv struct{}

func (tpolDrv) Open(string) (driver.Conn, error) { return nil, errors.New("tpol fake: use OpenDB") }

var tpolDriverOnce sync.Once

func tpolGorm(t *testing.T, state *tpolState) *gorm.DB {
	t.Helper()
	tpolDriverOnce.Do(func() {
		sql.Register("transport-policy-fake", tpolDrv{})
	})
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(tpolConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open tpol fake gorm: %v", err)
	}
	return gdb
}

func (c *tpolConn) whereOf(lower string) (string, int) {
	i := strings.Index(lower, " where ")
	if i < 0 {
		return "", 1
	}
	rest := lower[i+len(" where "):]
	argStart := 1
	// Count the bindvars BEFORE the where clause (gorm orders them by
	// appearance; our queries bind where-clauses first, so this is 1 — the
	// offset/limit handling below adjusts when needed).
	for _, kw := range []string{" order by ", " limit ", " offset "} {
		if j := strings.Index(rest, kw); j >= 0 {
			rest = rest[:j]
		}
	}
	return strings.TrimSpace(rest), argStart
}

func (c *tpolConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	lower := tpolNormalize(query)
	switch {
	case strings.HasPrefix(lower, "insert into transport_policies"):
		// gorm's Create uses a query when RETURNING id is requested: run the
		// insert and answer the new id as the returned row.
		res, err := c.execInsert(lower, args)
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		return &tpolRows{cols: []string{"id"}, rows: [][]driver.Value{{id}}}, nil
	case strings.HasPrefix(lower, "select count(*)"):
		table, where := tpolSelectTail(lower)
		preds, _ := tpolParseWhere(where, 1)
		return &tpolRows{cols: []string{"count"},
			rows: [][]driver.Value{{int64(len(s.match(table, preds, args)))}}}, nil
	case strings.Contains(lower, "from transport_policies"):
		return c.querySelect("transport_policies", lower, args)
	case strings.Contains(lower, "from domains"):
		return c.querySelect("domains", lower, args)
	}
	return nil, fmt.Errorf("tpol fake: cannot route query: %s", query)
}

func tpolSelectTail(lower string) (string, string) {
	from := strings.Index(lower, " from ")
	rest := lower[from+len(" from "):]
	end := len(rest)
	for _, kw := range []string{" where ", " order by ", " limit ", " offset "} {
		if i := strings.Index(rest, kw); i >= 0 && i < end {
			end = i
		}
	}
	table := strings.TrimSpace(rest[:end])
	where := ""
	if i := strings.Index(rest, " where "); i >= 0 {
		where = rest[i+len(" where "):]
		for _, kw := range []string{" order by ", " limit ", " offset "} {
			if j := strings.Index(where, kw); j >= 0 {
				where = where[:j]
			}
		}
	}
	return table, strings.TrimSpace(where)
}

func (c *tpolConn) querySelect(table, lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	table, where := tpolSelectTail(lower)
	preds, _ := tpolParseWhere(where, 1)
	matched := s.match(table, preds, args)
	// order by priority asc, id asc → sort by the priority column then id.
	if strings.Contains(lower, " order by priority") {
		pi, ii := s.colIndex(table, "priority"), s.colIndex(table, "id")
		for i := 1; i < len(matched); i++ {
			for j := i; j > 0; j-- {
				a, b := matched[j-1], matched[j]
				pa, pb := a[pi].(int64), b[pi].(int64)
				if pa > pb || (pa == pb && a[ii].(int64) > b[ii].(int64)) {
					matched[j-1], matched[j] = b, a
					continue
				}
				break
			}
		}
	}
	rows := make([][]driver.Value, 0, len(matched))
	for _, r := range matched {
		rows = append(rows, append([]driver.Value(nil), r...))
	}
	return &tpolRows{cols: tpolColumns[table], rows: rows}, nil
}

func (c *tpolConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	lower := tpolNormalize(query)
	switch {
	case strings.HasPrefix(lower, "insert into transport_policies"):
		return c.execInsert(lower, args)
	case strings.HasPrefix(lower, "update transport_policies"):
		return c.execUpdate(lower, args)
	case strings.HasPrefix(lower, "delete from transport_policies"):
		return c.execDelete(lower, args)
	}
	return nil, fmt.Errorf("tpol fake: unsupported exec: %s", query)
}

func (c *tpolConn) execInsert(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	open := strings.Index(lower, "(")
	closeIdx := strings.Index(lower[open:], ")")
	cols := strings.Split(lower[open+1:open+closeIdx], ",")
	valsStart := strings.Index(lower, " values (")
	inner := lower[valsStart+len(" values ("):]
	inner = inner[:strings.Index(inner, ")")]
	vals := strings.Split(inner, ",")
	if len(vals) != len(cols) {
		return nil, fmt.Errorf("tpol fake: insert arity mismatch")
	}
	row := make([]driver.Value, len(tpolColumns["transport_policies"]))
	for i, col := range cols {
		col = strings.TrimSpace(strings.ReplaceAll(col, `"`, ""))
		idx := s.colIndex("transport_policies", col)
		if idx < 0 {
			return nil, fmt.Errorf("tpol fake: unknown insert column %q", col)
		}
		tok := strings.TrimSpace(vals[i])
		if strings.HasPrefix(tok, "$") {
			n, _ := strconv.Atoi(tok[1:])
			row[idx] = tpolArg(args, n)
		}
	}
	s.seq++
	row[s.colIndex("transport_policies", "id")] = s.seq
	s.rows["transport_policies"] = append(s.rows["transport_policies"], row)
	return tpolResult{rows: 1, id: s.seq}, nil
}

func (c *tpolConn) execUpdate(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	setStart := strings.Index(lower, " set ") + len(" set ")
	whereStart := strings.Index(lower, " where ")
	if whereStart < 0 {
		return nil, fmt.Errorf("tpol fake: update without where")
	}
	var setters []struct {
		idx int
		val driver.Value
	}
	for _, pair := range strings.Split(lower[setStart:whereStart], ",") {
		pieces := strings.SplitN(pair, "=", 2)
		col := strings.TrimSpace(strings.ReplaceAll(pieces[0], `"`, ""))
		rhs := strings.TrimSpace(pieces[1])
		idx := s.colIndex("transport_policies", col)
		if idx < 0 || !strings.HasPrefix(rhs, "$") {
			return nil, fmt.Errorf("tpol fake: bad SET pair %q", pair)
		}
		n, _ := strconv.Atoi(rhs[1:])
		setters = append(setters, struct {
			idx int
			val driver.Value
		}{idx, tpolArg(args, n)})
	}
	where := strings.TrimSpace(lower[whereStart+len(" where "):])
	preds, _ := tpolParseWhere(where, len(setters)+1)
	matched := 0
	for _, row := range s.rows["transport_policies"] {
		ok := true
		argN := len(setters) + 1
		for _, p := range preds {
			idx := s.colIndex("transport_policies", p.col)
			av := tpolArg(args, argN)
			argN++
			cv, okc := row[idx].(int64)
			fv, okv := av.(int64)
			if !okc || !okv || cv != fv {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		matched++
		for _, st := range setters {
			row[st.idx] = st.val
		}
	}
	return tpolResult{rows: int64(matched)}, nil
}

func (c *tpolConn) execDelete(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	where := strings.TrimSpace(lower[strings.Index(lower, " where ")+len(" where "):])
	preds, _ := tpolParseWhere(where, 1)
	kept := make([][]driver.Value, 0, len(s.rows["transport_policies"]))
	removed := int64(0)
	for _, row := range s.rows["transport_policies"] {
		ok := true
		for _, p := range preds {
			idx := s.colIndex("transport_policies", p.col)
			cv, okc := row[idx].(int64)
			fv, okv := tpolArg(args, p.argN).(int64)
			if !okc || !okv || cv != fv {
				ok = false
				break
			}
		}
		if ok {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	s.rows["transport_policies"] = kept
	return tpolResult{rows: removed}, nil
}

// ---------- the harness ----------

type tpolHarness struct {
	o     *okapi.TestServer
	state *tpolState
	db    *gorm.DB
}

func newTpolHarness(t *testing.T) *tpolHarness {
	t.Helper()
	state := newTpolState()
	db := tpolGorm(t, state)
	h := NewTransportPolicyHandler(repositories.NewTransportPolicyRepository(db), repositories.NewDomainRepository(db))
	o := okapi.NewTestServer(t)
	adminAuth := middlewares.JWTAdminAuth(&config.Config{JWTSecret: tpolJWTSecret})
	g := o.Group("/api/v1/workspaces/current", adminAuth.Middleware, dmrWorkspaceBinding)
	g.Get("/domains/{id:int}/transport-policies", okapi.H(h.ListTransportPolicies))
	g.Post("/domains/{id:int}/transport-policies", okapi.H(h.CreateTransportPolicy))
	g.Patch("/domains/{id:int}/transport-policies/{pid:int}", okapi.H(h.PatchTransportPolicy))
	g.Delete("/domains/{id:int}/transport-policies/{pid:int}", okapi.H(h.DeleteTransportPolicy))
	return &tpolHarness{o: o, state: state, db: db}
}

func (h *tpolHarness) do(t *testing.T, role string, ws int, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+tpolJWT(t, role, ws))
	// The test workspace binding (dmrWorkspaceBinding) scopes the ownership
	// checks from this header.
	req.Header.Set("X-Test-Workspace", strconv.Itoa(ws))
	rec := httptest.NewRecorder()
	h.o.ServeHTTP(rec, req)
	return rec
}

// ---------- tests ----------

// TestTransportPolicyAdmin_CRUDRoundTrip — the full CRUD: create persists a
// normalized row bound to the domain, the list returns it in evaluation
// order, the patch updates it, the delete removes it, and repeating the
// delete reads as 404 like a missing one.
func TestTransportPolicyAdmin_CRUDRoundTrip(t *testing.T) {
	h := newTpolHarness(t)
	// The workspace's own verified domain (user 7 / workspace 5).
	domID := h.state.seed("domains", map[string]any{
		"id": uint(901), "user_id": 7, "workspace_id": 5, "domain": "prio.example", "ownership_verified": true,
		"created_at": time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
	})


	// Create: the conditions validated through the strict evaluator, the row
	// bound to the domain name + workspace.
	rec := h.do(t, "admin", 5, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/transport-policies", domID),
		`{"name": "tighten", "priority": 1, "conditions": [{"field": "mail_from", "operator": "ends_with", "value": "prio.example"}],
		  "max_message_size": 4096, "msg_rate_per_min": 3}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			ID         int    `json:"id"`
			DomainName string `json:"domain_name"`
			Name       string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Data.DomainName != "prio.example" || created.Data.Name != "tighten" {
		t.Fatalf("created = %+v, want the domain-bound tighten policy", created.Data)
	}

	// List: exactly the created policy.
	rec = h.do(t, "admin", 5, http.MethodGet,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/transport-policies", domID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}

	// Patch: flip the reject override.
	rec = h.do(t, "admin", 5, http.MethodPatch,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/transport-policies/%d", domID, created.Data.ID),
		`{"reject": true, "reject_reason": "ops blocked"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body.String())
	}

	// Delete: once OK, then 404 (missing reads as missing).
	rec = h.do(t, "admin", 5, http.MethodDelete,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/transport-policies/%d", domID, created.Data.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	rec = h.do(t, "admin", 5, http.MethodDelete,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/transport-policies/%d", domID, created.Data.ID), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}
}

// TestTransportPolicyAdmin_ValidationAndIsolation — the gates: invalid
// conditions (the strict grammar's unknown field/operator) are 400 before
// the table is touched, a foreign domain and a foreign policy read as 404,
// and a malformed bind IP or an orphan reject_reason is refused.
func TestTransportPolicyAdmin_ValidationAndIsolation(t *testing.T) {
	h := newTpolHarness(t)
	domID := h.state.seed("domains", map[string]any{
		"user_id": 7, "workspace_id": 5, "domain": "prio.example", "ownership_verified": true,
	})
	foreignDom := h.state.seed("domains", map[string]any{
		"user_id": 9, "workspace_id": 6, "domain": "foreign.example", "ownership_verified": true,
		"created_at": time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
	})

	// The strict grammar: unknown field + unknown operator + bad numeric are
	// 400 (the same checks the MTA gates fail closed on).
	for _, body := range []string{
		`{"name": "x", "conditions": [{"field": "not_a_fact", "operator": "equals", "value": "x"}]}`,
		`{"name": "x", "conditions": [{"field": "mail_from", "operator": "regex", "value": "x"}]}`,
		`{"name": "x", "conditions": [{"field": "message_size", "operator": "greater_than", "value": "big"}]}`,
		`{"name": "x", "bind_ip": "not-an-ip"}`,
		`{"name": "x", "reject_reason": "orphan reason"}`,
		`{"name": "x", "msg_rate_per_min": -5}`,
	} {
		rec := h.do(t, "admin", 5, http.MethodPost,
			fmt.Sprintf("/api/v1/workspaces/current/domains/%d/transport-policies", domID), body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid create %s → %d: %s, want 400", body, rec.Code, rec.Body.String())
		}
	}
	if len(h.state.rows["transport_policies"]) != 0 {
		t.Fatal("invalid creates must not touch the table")
	}

	// A foreign domain reads as 404 (isolation never leaks existence).
	rec := h.do(t, "admin", 5, http.MethodGet,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/transport-policies", foreignDom), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign domain list = %d, want 404", rec.Code)
	}

	// A foreign WORKSPACE's policy id reads as 404 on patch and delete.
	polID := h.state.seed("transport_policies", map[string]any{
		"workspace_id": 6, "domain_name": "foreign.example", "name": "theirs",
		"priority": 1, "enabled": true, "conditions": "[]",
	})
	rec = h.do(t, "admin", 5, http.MethodPatch,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/transport-policies/%d", domID, polID),
		`{"enabled": false}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign policy patch = %d, want 404", rec.Code)
	}
	rec = h.do(t, "admin", 5, http.MethodDelete,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/transport-policies/%d", domID, polID), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign policy delete = %d, want 404", rec.Code)
	}
	if len(h.state.rows["transport_policies"]) != 1 {
		t.Fatal("the foreign policy must survive the isolation probes")
	}
}
