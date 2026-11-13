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

package dmarc

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
// (window filter on created_at + the reportability predicates) — independent
// of any live database, the same arrangement as the TLS-RPT aggregator tests.

const dmArcAggCols = "id,user_id,workspace_id,inbound_id,message_id,client_ip,envelope_from," +
	"spf_result,spf_domain,spf_reason,dkim_result,dkim_domain,dkim_selector," +
	"dmarc_result,dmarc_disposition,dmarc_policy_domain,dmarc_policy,created_at"

type aggState struct {
	mu   sync.Mutex
	rows [][]driver.Value
	seq  int64
}

func newAggState() *aggState { return &aggState{seq: 500} }

func aggVal(v any) driver.Value {
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
		panic(fmt.Sprintf("aggVal: unsupported %T", v))
	}
}

func (s *aggState) seed(vals map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	cols := strings.Split(dmArcAggCols, ",")
	row := make([]driver.Value, len(cols))
	for i, c := range cols {
		if v, ok := vals[c]; ok {
			row[i] = aggVal(v)
		}
	}
	row[0] = s.seq
	s.rows = append(s.rows, row)
}

func aggGorm(t *testing.T, state *aggState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(aggConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open aggregate fake gorm: %v", err)
	}
	return gdb
}

type aggConnector struct{ state *aggState }

func (c aggConnector) Connect(context.Context) (driver.Conn, error) { return &aggConn{s: c.state}, nil }
func (c aggConnector) Driver() driver.Driver                        { return aggDrv{} }

type aggDrv struct{}

func (aggDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type aggConn struct{ s *aggState }

func (c *aggConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *aggConn) Close() error                        { return nil }
func (c *aggConn) Begin() (driver.Tx, error)           { return aggTx{}, nil }

type aggTx struct{}

func (aggTx) Commit() error   { return nil }
func (aggTx) Rollback() error { return nil }

type aggRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *aggRows) Columns() []string { return r.cols }
func (r *aggRows) Close() error      { return nil }
func (r *aggRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type aggResult struct{ rows int64 }

func (r aggResult) LastInsertId() (int64, error) { return 0, nil }
func (r aggResult) RowsAffected() (int64, error) { return r.rows, nil }

func (c *aggConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `from "inbound_auth_results"`) {
		return nil, fmt.Errorf("aggState: cannot route query: %s", query)
	}
	// The window predicate: created_at >= start AND created_at < end.
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
		created, _ := row[17].(time.Time)
		if created.Before(windowStart) || !created.Before(windowEnd) {
			continue
		}
		out = append(out, row)
	}
	return &aggRows{cols: strings.Split(dmArcAggCols, ","), rows: out}, nil
}

func (c *aggConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.QueryContext(ctx, query, args); err != nil {
		return nil, err
	}
	return aggResult{}, nil
}

func aggFixture(t *testing.T, seed func(*aggState)) (*Aggregator, time.Time) {
	t.Helper()
	start := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	state := newAggState()
	seed(state)
	agg := NewAggregator(aggGorm(t, state))
	return agg, start
}

func TestAggregateGroupsByPolicyDomainInWindow(t *testing.T) {
	start := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	agg, _ := aggFixture(t, func(s *aggState) {
		inWindow := start.Add(6 * time.Hour)
		s.seed(map[string]any{
			"created_at": inWindow, "client_ip": "192.0.2.10", "dmarc_result": "pass",
			"dmarc_policy_domain": "example.com", "dmarc_disposition": "none",
			"spf_result": "pass", "spf_domain": "mail.example.com",
			"dkim_result": "pass", "dkim_domain": "example.com", "dkim_selector": "s1",
		})
		s.seed(map[string]any{
			"created_at": inWindow, "client_ip": "198.51.100.7", "dmarc_result": "fail",
			"dmarc_policy_domain": "example.com", "dmarc_disposition": "quarantine",
			"spf_result": "fail", "spf_domain": "spoofer.example.net",
			"dkim_result": "fail",
		})
		// A different reported domain.
		s.seed(map[string]any{
			"created_at": inWindow, "client_ip": "192.0.2.99", "dmarc_result": "pass",
			"dmarc_policy_domain": "other.example", "dmarc_disposition": "none",
			"spf_result": "pass", "spf_domain": "other.example",
		})
		// Outside the window: not aggregated.
		s.seed(map[string]any{
			"created_at": start.Add(-time.Hour), "client_ip": "192.0.2.50", "dmarc_result": "fail",
			"dmarc_policy_domain": "example.com", "dmarc_disposition": "none",
		})
	})
	end := start.Add(24 * time.Hour)

	out, skipped, err := agg.Aggregate(context.Background(), start, end)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if skipped != 0 || len(out) != 2 {
		t.Fatalf("aggs = %d skipped = %d, want 2 domains / 0 skipped", len(out), skipped)
	}
	if out[0].PolicyDomain != "example.com" || out[1].PolicyDomain != "other.example" {
		t.Fatalf("domains = %s, %s", out[0].PolicyDomain, out[1].PolicyDomain)
	}
	if len(out[0].Rows) != 2 {
		t.Fatalf("example.com rows = %d, want 2 (in-window only)", len(out[0].Rows))
	}
	if out[0].Rows[0].ClientIP != "192.0.2.10" || out[0].Rows[0].DKIMSelector != "s1" {
		t.Fatalf("first row = %+v", out[0].Rows[0])
	}
	if out[1].Rows[0].ClientIP != "192.0.2.99" {
		t.Fatalf("other.example row = %+v", out[1].Rows[0])
	}
}

func TestAggregateSkipsUnreportableRows(t *testing.T) {
	var reasons []string
	start := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	agg, _ := aggFixture(t, func(s *aggState) {
		inWindow := start.Add(2 * time.Hour)
		s.seed(map[string]any{ // no DMARC evaluation at all (no policy)
			"created_at": inWindow, "client_ip": "192.0.2.1", "dmarc_result": "none",
			"dmarc_policy_domain": "", "dmarc_disposition": "none",
		})
		s.seed(map[string]any{ // resolver trouble, not an evaluation
			"created_at": inWindow, "client_ip": "192.0.2.2", "dmarc_result": "temperror",
			"dmarc_policy_domain": "temp.example", "dmarc_disposition": "none",
		})
		s.seed(map[string]any{ // a real evaluation
			"created_at": inWindow, "client_ip": "192.0.2.3", "dmarc_result": "fail",
			"dmarc_policy_domain": "example.com", "dmarc_disposition": "reject",
		})
	})
	agg.Log = func(reason string) { reasons = append(reasons, reason) }

	out, skipped, err := agg.Aggregate(context.Background(), start, start.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2 (none + temperror are not reportable evaluations)", skipped)
	}
	if len(out) != 1 || out[0].PolicyDomain != "example.com" || len(out[0].Rows) != 1 {
		t.Fatalf("aggs = %+v, want exactly the example.com evaluation", out)
	}
	if len(reasons) != 2 {
		t.Fatalf("skip reasons = %v, want 2", reasons)
	}
}

func TestAggregateNilDBErrors(t *testing.T) {
	agg := &Aggregator{}
	if _, _, err := agg.Aggregate(context.Background(), time.Now(), time.Now().Add(time.Hour)); err == nil {
		t.Fatal("nil db handle accepted, want an error")
	}
}
