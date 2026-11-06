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

// R-18 handler tests: GET /api-keys/{id}/usage answers the trailing series and
// totals only inside the caller's scope (a foreign or missing key is 404), and
// clamps the requested window to 1..90 days. The fake database serves the
// api_keys lookup and the usage reads (equality + day >= predicates and the
// COALESCE totals aggregate).

type akuHState struct {
	mu      sync.Mutex
	apiKeys map[int64]map[string]driver.Value
	usage   []map[string]driver.Value
	calls   []string
}

func newAkuHState() *akuHState {
	return &akuHState{apiKeys: map[int64]map[string]driver.Value{}}
}

func (s *akuHState) seedKey(id int64, userID int64, workspaceID any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiKeys[id] = map[string]driver.Value{"id": id, "user_id": userID, "workspace_id": workspaceID}
}

func (s *akuHState) seedUsage(keyID int64, day time.Time, requests, sends int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = append(s.usage, map[string]driver.Value{
		"api_key_id": keyID, "day": day, "requests": requests, "sends": sends,
	})
}

func akuHGorm(t *testing.T, state *akuHState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(akuHConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open api key usage handler fake gorm: %v", err)
	}
	return gdb
}

type akuHConnector struct{ state *akuHState }

func (c akuHConnector) Connect(context.Context) (driver.Conn, error) {
	return &akuHConn{s: c.state}, nil
}
func (c akuHConnector) Driver() driver.Driver { return akuHDrv{} }

type akuHDrv struct{}

func (akuHDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type akuHConn struct{ s *akuHState }

func (c *akuHConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *akuHConn) Close() error                        { return nil }
func (c *akuHConn) Begin() (driver.Tx, error)           { return akuHTx{}, nil }

type akuHTx struct{}

func (akuHTx) Commit() error   { return nil }
func (akuHTx) Rollback() error { return nil }

type akuHRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *akuHRows) Columns() []string { return r.cols }
func (r *akuHRows) Close() error      { return nil }
func (r *akuHRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func (c *akuHConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.calls = append(c.s.calls, lower)

	switch {
	case strings.Contains(lower, `from "api_keys"`):
		where := akuHWhere(lower)
		col, val := akuHPred(where, args)
		for id, row := range c.s.apiKeys {
			if col == "id" && fmt.Sprint(id) == fmt.Sprint(val) {
				return &akuHRows{cols: []string{"id", "user_id", "workspace_id"},
					rows: [][]driver.Value{{row["id"], row["user_id"], row["workspace_id"]}}}, nil
			}
		}
		return &akuHRows{cols: []string{"id"}, rows: nil}, nil
	case strings.Contains(lower, "api_key_usage_daily"):
		where := akuHWhere(lower)
		preds := akuHPreds(where, args)
		var matched []map[string]driver.Value
		for _, row := range c.s.usage {
			ok := true
			for _, p := range preds {
				if !p(row) {
					ok = false
					break
				}
			}
			if ok {
				matched = append(matched, row)
			}
		}
		if strings.Contains(lower, "coalesce(sum(requests)") {
			var requests, sends int64
			for _, row := range matched {
				requests += row["requests"].(int64)
				sends += row["sends"].(int64)
			}
			return &akuHRows{cols: []string{"requests", "sends"},
				rows: [][]driver.Value{{requests, sends}}}, nil
		}
		rows := make([][]driver.Value, 0, len(matched))
		for _, row := range matched {
			rows = append(rows, []driver.Value{row["api_key_id"], row["day"], row["requests"], row["sends"]})
		}
		return &akuHRows{cols: []string{"api_key_id", "day", "requests", "sends"}, rows: rows}, nil
	}
	return nil, fmt.Errorf("akuHState: cannot route query: %s", query)
}

func (c *akuHConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return nil, fmt.Errorf("akuHState: unsupported exec: %s", query)
}

func akuHWhere(lower string) string {
	if i := strings.Index(lower, " where "); i >= 0 {
		where := lower[i+len(" where "):]
		for _, cut := range []string{" order by ", " limit "} {
			if j := strings.Index(where, cut); j >= 0 {
				where = where[:j]
			}
		}
		return where
	}
	return ""
}

// akuHColumnOf normalizes a (possibly table-qualified) column reference to
// its bare name.
func akuHColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

// akuHPred returns the single equality predicate of a key lookup.
func akuHPred(where string, args []driver.NamedValue) (string, driver.Value) {
	eq := strings.Index(where, " = ")
	if eq < 0 {
		return "", nil
	}
	return akuHColumnOf(where[:eq]), akuHArg(args, where[eq+3:])
}

// akuHPreds parses the equality and >= predicates of the usage reads.
func akuHPreds(where string, args []driver.NamedValue) []func(map[string]driver.Value) bool {
	var preds []func(map[string]driver.Value) bool
	for _, cond := range strings.Split(strings.NewReplacer("(", " ", ")", " ").Replace(where), " and ") {
		cond = strings.TrimSpace(cond)
		if cond == "" {
			continue
		}
		op := " = "
		idx := strings.Index(cond, " >= ")
		if idx >= 0 {
			op = " >= "
		} else {
			idx = strings.Index(cond, " = ")
			if idx < 0 {
				continue
			}
		}
		col := strings.Trim(strings.TrimSpace(cond[:idx]), `"`)
		val := akuHArg(args, cond[idx+len(op):])
		switch col {
		case "api_key_id", "id":
			preds = append(preds, func(row map[string]driver.Value) bool {
				return fmt.Sprint(row[col]) == fmt.Sprint(val)
			})
		case "day":
			day := val.(time.Time)
			if op == " >= " {
				preds = append(preds, func(row map[string]driver.Value) bool {
					return !row["day"].(time.Time).Before(day)
				})
			} else {
				preds = append(preds, func(row map[string]driver.Value) bool {
					return row["day"].(time.Time).Equal(day)
				})
			}
		}
	}
	return preds
}

func akuHArg(args []driver.NamedValue, tok string) driver.Value {
	tok = strings.TrimSpace(tok)
	var n int
	if _, err := fmt.Sscanf(tok[1:], "%d", &n); err != nil {
		panic(fmt.Sprintf("akuHState: bad bindvar %q", tok))
	}
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("akuHState: no arg with ordinal %d", n))
}

func newUsageServer(t *testing.T, state *akuHState) *okapi.TestServer {
	t.Helper()
	gdb := akuHGorm(t, state)
	h := NewAPIKeyUsageHandler(
		repositories.NewAPIKeyUsageRepository(gdb),
		repositories.NewAPIKeyRepository(gdb),
	)
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		testWorkspaceBinding,
	)
	g.Get("/api-keys/{id:int}/usage", okapi.H(h.Get))
	return o
}

func doUsageReq(t *testing.T, o *okapi.TestServer, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
	req.Header.Set("X-Test-Workspace", wrTestWorkspace)
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

// The usage endpoint serves the zero-padded series and totals for an in-scope
// key and clamps the window to 90 days.
func TestAPIKeyUsageHandlerSeriesAndClamp(t *testing.T) {
	state := newAkuHState()
	state.seedKey(31, 7, int64(3))
	state.seedUsage(31, akuDayUTC(time.Now()), 4, 2)
	o := newUsageServer(t, state)

	rec := doUsageReq(t, o, "/workspaces/current/api-keys/31/usage?days=500")
	if rec.Code != http.StatusOK {
		t.Fatalf("usage = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Data struct {
			Days []struct {
				Day      string `json:"day"`
				Requests int64  `json:"requests"`
			} `json:"days"`
			DaysTrailing int `json:"days_trailing"`
			Totals       struct {
				Requests int64 `json:"requests"`
				Sends    int64 `json:"sends"`
			} `json:"totals"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Data.DaysTrailing != 90 {
		t.Fatalf("days_trailing = %d, want 90 (clamped)", res.Data.DaysTrailing)
	}
	if len(res.Data.Days) != 90 {
		t.Fatalf("series length = %d, want 90", len(res.Data.Days))
	}
	last := res.Data.Days[len(res.Data.Days)-1]
	if last.Requests != 4 {
		t.Fatalf("today's row = %+v, want 4 requests", last)
	}
	if res.Data.Totals.Requests != 4 || res.Data.Totals.Sends != 2 {
		t.Fatalf("totals = %+v, want 4/2", res.Data.Totals)
	}
}

// A key outside the caller's scope — and a missing key — answer 404.
func TestAPIKeyUsageHandlerScope(t *testing.T) {
	state := newAkuHState()
	state.seedKey(32, 7, int64(99)) // foreign workspace
	state.seedKey(33, 7, int64(3))
	o := newUsageServer(t, state)

	if rec := doUsageReq(t, o, "/workspaces/current/api-keys/32/usage"); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign key usage = %d, want 404", rec.Code)
	}
	if rec := doUsageReq(t, o, "/workspaces/current/api-keys/999/usage"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing key usage = %d, want 404", rec.Code)
	}
}

func akuDayUTC(t time.Time) time.Time { return t.UTC().Truncate(24 * time.Hour) }
