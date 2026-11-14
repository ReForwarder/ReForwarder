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

package arf

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The aggregator runs against a self-contained fake database/sql driver that
// implements exactly the inbound_auth_results query the aggregation issues
// (window filter on created_at), the same arrangement as the DMARC aggregate
// tests.

const arfAggCols = "id,user_id,workspace_id,inbound_id,message_id,client_ip,envelope_from," +
	"spf_result,spf_domain,spf_reason,dkim_result,dkim_domain,dkim_selector," +
	"dmarc_result,dmarc_disposition,dmarc_policy_domain,dmarc_policy,arc_result,arc_domain," +
	"iprev_result,iprev_ptr,auth_results_header,created_at"

type arfAggState struct {
	mu   sync.Mutex
	rows [][]driver.Value
	seq  int64
}

func newARFAggState() *arfAggState { return &arfAggState{seq: 900} }

func arfAggVal(v any) driver.Value {
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
		panic(fmt.Sprintf("arfAggVal: unsupported %T", v))
	}
}

func (s *arfAggState) seed(vals map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	cols := strings.Split(arfAggCols, ",")
	row := make([]driver.Value, len(cols))
	for i, c := range cols {
		if v, ok := vals[c]; ok {
			row[i] = arfAggVal(v)
		}
	}
	row[0] = s.seq
	s.rows = append(s.rows, row)
}

func arfAggGorm(t *testing.T, state *arfAggState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(arfAggConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open arf aggregate fake gorm: %v", err)
	}
	return gdb
}

type arfAggConnector struct{ state *arfAggState }

func (c arfAggConnector) Connect(context.Context) (driver.Conn, error) {
	return &arfAggConn{s: c.state}, nil
}
func (c arfAggConnector) Driver() driver.Driver { return arfAggDrv{} }

type arfAggDrv struct{}

func (arfAggDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type arfAggConn struct{ s *arfAggState }

func (c *arfAggConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *arfAggConn) Close() error                        { return nil }
func (c *arfAggConn) Begin() (driver.Tx, error)           { return arfAggTx{}, nil }

type arfAggTx struct{}

func (arfAggTx) Commit() error   { return nil }
func (arfAggTx) Rollback() error { return nil }

type arfAggRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *arfAggRows) Columns() []string { return r.cols }
func (r *arfAggRows) Close() error      { return nil }
func (r *arfAggRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type arfAggResult struct{ rows int64 }

func (r arfAggResult) LastInsertId() (int64, error) { return 0, nil }
func (r arfAggResult) RowsAffected() (int64, error) { return r.rows, nil }

func (c *arfAggConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `from "inbound_auth_results"`) {
		return nil, fmt.Errorf("arfAggState: cannot route query: %s", query)
	}
	var windowStart, windowEnd time.Time
	for _, a := range args {
		if t, ok := a.Value.(time.Time); ok {
			if windowStart.IsZero() {
				windowStart = t
			} else if windowEnd.IsZero() {
				windowEnd = t
			}
		}
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	var out [][]driver.Value
	for _, row := range c.s.rows {
		created, _ := row[len(row)-1].(time.Time)
		if created.Before(windowStart) || !created.Before(windowEnd) {
			continue
		}
		out = append(out, row)
	}
	return &arfAggRows{cols: strings.Split(arfAggCols, ","), rows: out}, nil
}

func (c *arfAggConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.QueryContext(ctx, query, args); err != nil {
		return nil, err
	}
	return arfAggResult{}, nil
}

func arfAggFixture(t *testing.T, seed func(*arfAggState)) (*Aggregator, time.Time) {
	t.Helper()
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	state := newARFAggState()
	seed(state)
	agg := NewAggregator(arfAggGorm(t, state))
	return agg, start
}

// The failure families group per reported domain: DKIM failures under the
// signature's d= domain, SPF failures under the MAIL FROM domain, in-window
// only.
func TestAggregateGroupsFailuresPerDomainAndFamily(t *testing.T) {
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	agg, _ := arfAggFixture(t, func(s *arfAggState) {
		inWindow := start.Add(6 * time.Hour)
		s.seed(map[string]any{
			"created_at": inWindow, "inbound_id": int64(1), "client_ip": "192.0.2.10",
			"dkim_result": "fail", "dkim_domain": "sender.example.com", "dkim_selector": "s1",
			"spf_result": "none", "spf_domain": "",
		})
		s.seed(map[string]any{
			"created_at": inWindow, "inbound_id": int64(2), "client_ip": "192.0.2.11",
			"dkim_result": "fail", "dkim_domain": "sender.example.com", "dkim_selector": "s2",
			"spf_result": "none", "spf_domain": "",
		})
		s.seed(map[string]any{
			"created_at": inWindow, "inbound_id": int64(3), "client_ip": "192.0.2.12",
			"spf_result": "fail", "spf_domain": "mailer.example.net",
			"dkim_result": "none", "dkim_domain": "",
		})
		// Out of the window.
		s.seed(map[string]any{
			"created_at": start.Add(-time.Hour), "inbound_id": int64(4), "client_ip": "192.0.2.13",
			"dkim_result": "fail", "dkim_domain": "sender.example.com",
		})
	})
	end := start.Add(24 * time.Hour)

	out, skipped, err := agg.Aggregate(context.Background(), start, end)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if skipped != 0 || len(out) != 2 {
		t.Fatalf("batches = %d skipped = %d, want 2 / 0", len(out), skipped)
	}
	if out[0].ReportedDomain != "sender.example.com" || out[0].Family != "dkim" || len(out[0].Rows) != 2 {
		t.Fatalf("dkim batch = %+v", out[0])
	}
	if out[1].ReportedDomain != "mailer.example.net" || out[1].Family != "spf" || len(out[1].Rows) != 1 {
		t.Fatalf("spf batch = %+v", out[1])
	}
	if out[0].Rows[0].AuthResultsHeader != "" || out[0].Rows[0].ClientIP != "192.0.2.10" {
		t.Fatalf("row = %+v", out[0].Rows[0])
	}
}

// Rows that are not addressable failures (no failure, or a failure without a
// reporting domain) are skipped and logged, never guessed.
func TestAggregateSkipsUnaddressableRows(t *testing.T) {
	var reasons []string
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	agg, _ := arfAggFixture(t, func(s *arfAggState) {
		inWindow := start.Add(2 * time.Hour)
		s.seed(map[string]any{ // DKIM passed — no failure
			"created_at": inWindow, "inbound_id": int64(1), "client_ip": "192.0.2.1",
			"dkim_result": "pass", "dkim_domain": "ok.example.com",
		})
		s.seed(map[string]any{ // DKIM failed but no d= domain to report against
			"created_at": inWindow, "inbound_id": int64(2), "client_ip": "192.0.2.2",
			"dkim_result": "fail", "dkim_domain": "",
		})
		s.seed(map[string]any{ // SPF passed
			"created_at": inWindow, "inbound_id": int64(3), "client_ip": "192.0.2.3",
			"spf_result": "pass", "spf_domain": "ok.example.net",
		})
		s.seed(map[string]any{ // an addressable failure
			"created_at": inWindow, "inbound_id": int64(4), "client_ip": "192.0.2.4",
			"dkim_result": "fail", "dkim_domain": "sender.example.com",
		})
	})
	agg.Log = func(reason string) { reasons = append(reasons, reason) }

	out, skipped, err := agg.Aggregate(context.Background(), start, start.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if skipped != 3 {
		t.Fatalf("skipped = %d, want 3", skipped)
	}
	if len(out) != 1 || out[0].ReportedDomain != "sender.example.com" || len(out[0].Rows) != 1 {
		t.Fatalf("batches = %+v, want only the addressable failure", out)
	}
	if len(reasons) != 3 {
		t.Fatalf("skip reasons = %v, want 3", reasons)
	}
}

// The per-domain bound: candidates beyond the bound are skipped, so the
// deployment never mails a domain unboundedly.
func TestAggregateBoundsPerDomain(t *testing.T) {
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	agg, _ := arfAggFixture(t, func(s *arfAggState) {
		for i := 1; i <= 6; i++ {
			s.seed(map[string]any{
				"created_at": start.Add(time.Duration(i) * time.Minute), "inbound_id": int64(i),
				"client_ip":   "192.0.2.10",
				"dkim_result": "fail", "dkim_domain": "sender.example.com", "dkim_selector": "s1",
			})
		}
	})
	agg.MaxRowsPerDomain = 4

	out, skipped, err := agg.Aggregate(context.Background(), start, start.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(out) != 1 || len(out[0].Rows) != 4 {
		t.Fatalf("rows = %d, want 4 (the per-domain bound)", len(out[0].Rows))
	}
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2 (beyond the bound)", skipped)
	}
}

func TestAggregateNilDBErrors(t *testing.T) {
	agg := &Aggregator{}
	if _, _, err := agg.Aggregate(context.Background(), time.Now(), time.Now().Add(time.Hour)); err == nil {
		t.Fatal("nil db handle accepted, want an error")
	}
}
