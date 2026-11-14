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

// SF-57 tests: the sent-report admin-object read surfaces (the ARF
// auth-failure reports and the RFC 7489 aggregates this deployment
// dispatched). Handler battery + tenant isolation, on the dmarc_handler_test
// fake-driver arrangement.

const rohDomainColumns = "id,user_id,workspace_id,domain,ownership_verified,created_at"
const rohArfColumns = "id,user_id,workspace_id,report_id,reported_domain,family,inbound_id,recipients,document,status,error_message,created_at"
const rohDmarcColumns = "id,report_id,reported_domain,policy,window_start,window_end,rua_addresses,record_count,document,status,error_message,created_at"

type rohState struct {
	mu     sync.Mutex
	tables map[string]*rohTable
	seq    int64
}

type rohTable struct {
	cols []string
	rows [][]driver.Value
}

func newRohState() *rohState {
	return &rohState{
		tables: map[string]*rohTable{
			"domains":             {cols: strings.Split(rohDomainColumns, ",")},
			"arf_reports":         {cols: strings.Split(rohArfColumns, ",")},
			"dmarc_sent_reports":  {cols: strings.Split(rohDmarcColumns, ",")},
		},
		seq: 1300,
	}
}

func rohVal(v any) driver.Value {
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
		panic(fmt.Sprintf("rohVal: unsupported %T", v))
	}
}

func (s *rohState) seed(t *testing.T, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	cols := s.tables[table].cols
	row := make([]driver.Value, len(cols))
	for i, c := range cols {
		if v, ok := vals[c]; ok {
			row[i] = rohVal(v)
		} else {
			var zero driver.Value
			switch c {
			case "id":
				s.mu.Unlock()
				s.mu.Lock()
				s.seq++
				row[i] = s.seq
			default:
				zero = nil
				row[i] = zero
			}
		}
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func rohGorm(t *testing.T, state *rohState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(rohConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open roh fake gorm: %v", err)
	}
	return gdb
}

type rohConnector struct{ state *rohState }

func (c rohConnector) Connect(context.Context) (driver.Conn, error) { return &rohConn{s: c.state}, nil }
func (c rohConnector) Driver() driver.Driver                        { return rohDrv{} }

type rohDrv struct{}

func (rohDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type rohConn struct{ s *rohState }

func (c *rohConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *rohConn) Close() error                        { return nil }
func (c *rohConn) Begin() (driver.Tx, error)           { return rohTx{}, nil }

type rohTx struct{}

func (rohTx) Commit() error   { return nil }
func (rohTx) Rollback() error { return nil }

type rohRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *rohRows) Columns() []string { return r.cols }
func (r *rohRows) Close() error      { return nil }
func (r *rohRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type rohResult struct{ rows int64 }

func (r rohResult) LastInsertId() (int64, error) { return 0, nil }
func (r rohResult) RowsAffected() (int64, error) { return r.rows, nil }

func (c *rohConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	var table string
	for name := range s.tables {
		if strings.Contains(lower, `from "`+name+`"`) {
			table = name
			break
		}
	}
	if table == "" {
		return nil, fmt.Errorf("rohState: cannot route query: %s", query)
	}
	cols := s.tables[table].cols
	colIndex := func(name string) int {
		for i, c := range cols {
			if c == name {
				return i
			}
		}
		return -1
	}
	// The two read queries the repos issue: a windowless newest-first SELECT
	// with the predicates the repos add — the ARF scope list's
	// `workspace_id = $n` (ApplyScope) and the DMARC tenant list's
	// `reported_domain IN (...)`. Both predicates are evaluated here so
	// foreign rows never leak into the assertions.
	var out [][]driver.Value
	var domainNames map[string]bool
	if strings.Contains(lower, "reported_domain in") {
		domainNames = map[string]bool{}
		for _, a := range args {
			if v, ok := a.Value.(string); ok {
				domainNames[strings.ToLower(v)] = true
			}
		}
	}
	// The id = $n predicate (FindByID): without it the fake would return the
	// whole table for every single-row lookup. Detection is exclusive of the
	// scoped list queries, whose workspace_id = predicate contains "id = "
	// as a substring.
	var findID *int64
	if !strings.Contains(lower, "workspace_id =") && (strings.Contains(lower, `"id" =`) || strings.Contains(lower, "id = ")) {
		for _, a := range args {
			if v, ok := a.Value.(int64); ok {
				findID = &v
				break
			}
		}
	}
	var scopeWorkspace *int64
	if strings.Contains(lower, "workspace_id =") {
		for _, a := range args {
			if v, ok := a.Value.(int64); ok {
				scopeWorkspace = &v
				break
			}
		}
	}
	for _, row := range s.tables[table].rows {
		if findID != nil {
			idIdx := colIndex("id")
			id, _ := row[idIdx].(int64)
			if id != *findID {
				continue
			}
		}
		if scopeWorkspace != nil {
			wsIdx := colIndex("workspace_id")
			ws, _ := row[wsIdx].(int64)
			if ws != *scopeWorkspace {
				continue
			}
		}
		if domainNames != nil {
			idx := colIndex("reported_domain")
			dom, _ := row[idx].(string)
			if !domainNames[strings.ToLower(dom)] {
				continue
			}
		}
		out = append(out, row)
	}
	// Newest-first by created_at.
	if idx := colIndex("created_at"); idx >= 0 {
		for i := 1; i < len(out); i++ {
			for j := i; j > 0; j-- {
				a, _ := out[j][idx].(time.Time)
				b, _ := out[j-1][idx].(time.Time)
				if a.After(b) {
					out[j], out[j-1] = out[j-1], out[j]
				} else {
					break
				}
			}
		}
	}
	return &rohRows{cols: cols, rows: out}, nil
}

func (c *rohConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.QueryContext(ctx, query, args); err != nil {
		return nil, err
	}
	return rohResult{}, nil
}

// newReportObjectServer mounts the SF-57 read surfaces over the fake.
func newReportObjectServer(t *testing.T, state *rohState) (*okapi.TestServer, *ReportObjectHandler) {
	t.Helper()
	gdb := rohGorm(t, state)
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
	g.Get("/arf-reports", okapi.H(h.ListARFReports))
	g.Get("/dmarc-reports", okapi.H(h.ListDMARCSentReports))
	g.Get("/domains/{id:int}/sent-reports", okapi.H(h.ListDMARCSentReportsByDomain))
	return o, h
}

// TestARFReportsTenantScoped pins the workspace isolation: the list returns
// only the reports attributed to the current workspace; the foreign row
// never leaks.
func TestARFReportsTenantScoped(t *testing.T) {
	state := newRohState()
	state.seed(t, "arf_reports", map[string]any{
		"id": uint(1), "user_id": uint(7), "workspace_id": wsID(3),
		"report_id": "1-dkim@reporter.example", "reported_domain": "sender.example.com",
		"family": "dkim", "inbound_id": uint(11), "recipients": `["arf@sender.example.com"]`,
		"document": "report-doc-1", "status": "sent",
		"created_at": time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC),
	})
	state.seed(t, "arf_reports", map[string]any{
		"id": uint(2), "user_id": uint(42), "workspace_id": wsID(99),
		"report_id": "9-dkim@reporter.example", "reported_domain": "foreign.example.com",
		"family": "dkim", "inbound_id": uint(12), "recipients": `["arf@foreign.example.com"]`,
		"document": "foreign-doc", "status": "sent",
		"created_at": time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC),
	})
	o, _ := newReportObjectServer(t, state)

	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/arf-reports", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("arf list = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Success bool             `json:"success"`
		Data    ARFReportsResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Data.Reports) != 1 {
		t.Fatalf("reports = %d, want 1 (the foreign row excluded)", len(res.Data.Reports))
	}
	if res.Data.Reports[0].ReportID != "1-dkim@reporter.example" {
		t.Fatalf("report id = %q, want the workspace-owned row", res.Data.Reports[0].ReportID)
	}
}

// TestDMARCSentReportsScopedByOwnDomains pins the DMARC sent-report scoping:
// the list resolves the workspace's own domain names and returns only the
// aggregates about them; aggregates about other domains never leak.
func TestDMARCSentReportsScopedByOwnDomains(t *testing.T) {
	state := newRohState()
	state.seed(t, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true,
	})
	state.seed(t, "dmarc_sent_reports", map[string]any{
		"report_id": "w1@reporter.example:example.com", "reported_domain": "example.com",
		"policy": "reject", "record_count": 3, "status": "sent",
		"rua_addresses": `["agg@example.com"]`,
		"document":      "<feedback>example.com aggregate</feedback>",
		"created_at":    time.Date(2026, 11, 14, 6, 50, 0, 0, time.UTC),
	})
	state.seed(t, "dmarc_sent_reports", map[string]any{
		"report_id": "w1@reporter.example:foreign.com", "reported_domain": "foreign.com",
		"policy": "none", "record_count": 9, "status": "sent",
		"document": "<feedback>foreign aggregate</feedback>",
		"created_at": time.Date(2026, 11, 14, 6, 51, 0, 0, time.UTC),
	})
	o, _ := newReportObjectServer(t, state)

	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/dmarc-reports", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("dmarc sent list = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Success bool                     `json:"success"`
		Data    DMARCSentReportsResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Data.Reports) != 1 {
		t.Fatalf("reports = %d, want 1 (the foreign-domain aggregate excluded)", len(res.Data.Reports))
	}
	if res.Data.Reports[0].ReportedDomain != "example.com" {
		t.Fatalf("reported domain = %q, want the tenant's own domain", res.Data.Reports[0].ReportedDomain)
	}
}

// TestDomainSentReportsForeignDomainIs404 pins the per-domain view's
// isolation: a foreign (not-owned) domain answers 404, indistinguishable
// from a missing one.
func TestDomainSentReportsForeignDomainIs404(t *testing.T) {
	state := newRohState()
	state.seed(t, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true,
	})
	state.seed(t, "domains", map[string]any{
		"id": uint(11), "user_id": uint(42), "workspace_id": wsID(99),
		"domain": "foreign.com", "ownership_verified": true,
	})
	state.seed(t, "dmarc_sent_reports", map[string]any{
		"report_id": "w1@reporter.example:foreign.com", "reported_domain": "foreign.com",
		"status": "sent", "record_count": 5,
		"created_at": time.Date(2026, 11, 14, 6, 50, 0, 0, time.UTC),
	})
	o, _ := newReportObjectServer(t, state)

	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/10/sent-reports", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("own-domain view = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Success bool                     `json:"success"`
		Data    DomainSentReportsResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Data.Domain != "example.com" {
		t.Fatalf("domain = %q", res.Data.Domain)
	}

	foreign := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/11/sent-reports", "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign domain view = %d, want 404 (indistinguishable from missing)", foreign.Code)
	}
}
