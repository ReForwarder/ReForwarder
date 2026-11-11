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

package tlsrpt

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

	"github.com/goposta/posta/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The aggregator runs through a fake database/sql driver holding the
// mta_delivery_attempts and emails tables (the dmarc_test.go arrangement in
// internal/services/inbound). Assertions pin the attribution rule: sessions
// count only for emails with exactly one recipient domain, delivered counts
// as a successful session, reached-and-failed as a failure, and never-
// connected outcomes count as no session at all.

const agColsAttempts = "id,email_id,workspace_id,attempt,mx_host,ip,response_code,response_text,outcome,started_at,finished_at,created_at"

type agState struct {
	mu     sync.Mutex
	tables map[string]*agTable
	seq    int64
}

type agTable struct {
	cols []string
	rows [][]driver.Value
}

func newAgState() *agState {
	return &agState{
		tables: map[string]*agTable{
			"mta_delivery_attempts": {cols: strings.Split(agColsAttempts, ",")},
			"emails":                {cols: []string{"id", "recipients"}},
		},
		seq: 100,
	}
}

func (s *agState) seedAttempt(t *testing.T, vals map[string]any) {
	t.Helper()
	s.seed(t, "mta_delivery_attempts", strings.Split(agColsAttempts, ","), vals)
}

func (s *agState) seedEmail(t *testing.T, id uint, recipients []string) {
	t.Helper()
	s.seed(t, "emails", []string{"id", "recipients"}, map[string]any{"id": id, "recipients": recipients})
}

func (s *agState) seed(t *testing.T, table string, cols []string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	row := make([]driver.Value, len(cols))
	for i := range cols {
		row[i] = nil
	}
	for c, v := range vals {
		idx := -1
		for i, col := range cols {
			if col == c {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, c)
		}
		row[idx] = agVal(v)
	}
	if table == "mta_delivery_attempts" {
		if row[0] == nil {
			s.seq++
			row[0] = s.seq
		}
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func agVal(v any) driver.Value {
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
	case []string:
		// text[] renders as the Postgres array literal the fake parses back.
		return "{" + strings.Join(t, ",") + "}"
	default:
		panic(fmt.Sprintf("agVal: unsupported type %T", v))
	}
}

func agGorm(t *testing.T, state *agState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(agConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open aggregator fake gorm: %v", err)
	}
	return gdb
}

type agConnector struct{ state *agState }

func (c agConnector) Connect(context.Context) (driver.Conn, error) { return &agConn{s: c.state}, nil }
func (c agConnector) Driver() driver.Driver                        { return agDrv{} }

type agDrv struct{}

func (agDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type agConn struct{ s *agState }

func (c *agConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *agConn) Close() error                        { return nil }
func (c *agConn) Begin() (driver.Tx, error)           { return agTx{}, nil }

type agTx struct{}

func (agTx) Commit() error   { return nil }
func (agTx) Rollback() error { return nil }

type agRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *agRows) Columns() []string { return r.cols }
func (r *agRows) Close() error      { return nil }
func (r *agRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type agResult struct{ rows int64 }

func (r agResult) LastInsertId() (int64, error) { return 0, nil }
func (r agResult) RowsAffected() (int64, error) { return r.rows, nil }

// parsePgArray parses the trivial {a,b,c} literal the fake stores.
func parsePgArray(v driver.Value) []string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	s = strings.TrimPrefix(strings.TrimSuffix(s, "}"), "{")
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func (c *agConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case strings.Contains(lower, "from \"mta_delivery_attempts\""):
		rows := append([][]driver.Value(nil), s.tables["mta_delivery_attempts"].rows...)
		// The finished_at window filter + order by id.
		var windowStart, windowEnd time.Time
		for _, a := range args {
			if t, ok := a.Value.(time.Time); ok {
				if windowStart.IsZero() {
					windowStart = t
				} else {
					windowEnd = t
				}
			}
		}
		var out [][]driver.Value
		for _, row := range rows {
			fin, _ := row[10].(time.Time)
			if fin.Before(windowStart) || !fin.Before(windowEnd) {
				continue
			}
			out = append(out, row)
		}
		for i := 1; i < len(out); i++ {
			for j := i; j > 0; j-- {
				a, _ := out[j][0].(int64)
				b, _ := out[j-1][0].(int64)
				if a < b {
					out[j], out[j-1] = out[j-1], out[j]
				} else {
					break
				}
			}
		}
		if m := strings.Index(lower, " limit "); m >= 0 {
			var n int
			if _, err := fmt.Sscanf(lower[m+7:], "%d", &n); err == nil && n < len(out) {
				out = out[:n]
			}
		}
		return &agRows{cols: s.tables["mta_delivery_attempts"].cols, rows: out}, nil

	case strings.Contains(lower, "from \"emails\""):
		// WHERE id IN (...) — collect the bound ids.
		want := map[int64]bool{}
		for _, a := range args {
			if v, ok := a.Value.(int64); ok {
				want[v] = true
			}
		}
		var out [][]driver.Value
		for _, row := range s.tables["emails"].rows {
			id, _ := row[0].(int64)
			if want[id] {
				out = append(out, row)
			}
		}
		return &agRows{cols: []string{"id", "recipients"}, rows: out}, nil
	}
	return nil, fmt.Errorf("agState: cannot route query: %s", query)
}

func (c *agConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.QueryContext(ctx, query, args); err != nil {
		return nil, err
	}
	return agResult{}, nil
}

func agWindow() (time.Time, time.Time) {
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	return start, start.Add(24 * time.Hour)
}

func agAttempt(t *testing.T, s *agState, emailID uint, mxHost, outcome string, finishedAt time.Time) {
	t.Helper()
	s.seedAttempt(t, map[string]any{
		"email_id": emailID, "mx_host": mxHost, "outcome": outcome,
		"started_at": finishedAt.Add(-time.Second), "finished_at": finishedAt,
		"attempt": int64(1), "response_code": int64(250),
	})
}

func TestAggregatorAttributionAndClassification(t *testing.T) {
	s := newAgState()
	start, end := agWindow()

	// Email 1 → exactly one recipient domain (attributable).
	s.seedEmail(t, 1, []string{"user@remote.example"})
	// Email 2 → two recipient domains (unattributable by design).
	s.seedEmail(t, 2, []string{"a@one.example", "b@two.example"})
	// Email 3 → one domain, but its attempts are outside the window.
	s.seedEmail(t, 3, []string{"user@outside.example"})

	at := func(min int) time.Time { return start.Add(time.Duration(min) * time.Minute) }
	// Delivered → successful.
	agAttempt(t, s, 1, "mx1.remote.example", models.MTAAttemptDelivered, at(10))
	agAttempt(t, s, 1, "mx1.remote.example", models.MTAAttemptDelivered, at(11))
	// Reached-and-failed (5xx at RCPT) → failed session.
	agAttempt(t, s, 1, "mx2.remote.example", models.MTAAttemptPermanentFailure, at(12))
	// Transient with an MX host → failed session.
	agAttempt(t, s, 1, "mx2.remote.example", models.MTAAttemptTransientFailure, at(13))
	// DNS failure: no session (no connection opened).
	agAttempt(t, s, 1, "", models.MTAAttemptDNSFailure, at(14))
	// Budget deferral: no session.
	agAttempt(t, s, 1, "", models.MTAAttemptDeferredBudget, at(15))
	// Unverified refusal: no session.
	agAttempt(t, s, 1, "", models.MTAAttemptRefusedUnverifiedSender, at(16))
	// Multi-domain email: logged and skipped, never mis-attributed.
	agAttempt(t, s, 2, "mx.one.example", models.MTAAttemptDelivered, at(17))
	// Outside the window: excluded by the query filter.
	agAttempt(t, s, 3, "mx.outside.example", models.MTAAttemptDelivered, end.Add(time.Minute))

	skips := 0
	agg := &Aggregator{db: agGorm(t, s), Log: func(string, uint) { skips++ }}
	aggs, skipped, err := agg.Aggregate(context.Background(), start, end)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (the two-domain email)", skipped)
	}
	if skips == 0 {
		t.Fatalf("skip reasons must be logged")
	}
	if len(aggs) != 2 {
		t.Fatalf("aggregates = %d (%v), want 2", len(aggs), aggs)
	}
	byHost := map[string]SessionAggregate{}
	for _, a := range aggs {
		if a.PolicyDomain != "remote.example" {
			t.Fatalf("aggregate leaked a foreign policy domain: %+v", a)
		}
		byHost[a.MXHost] = a
	}
	if byHost["mx1.remote.example"].Successful != 2 || byHost["mx1.remote.example"].Failed != 0 {
		t.Fatalf("mx1 tally wrong: %+v", byHost["mx1.remote.example"])
	}
	if byHost["mx2.remote.example"].Failed != 2 || byHost["mx2.remote.example"].Successful != 0 {
		t.Fatalf("mx2 tally wrong: %+v", byHost["mx2.remote.example"])
	}
}

func TestAggregatorTruncatesAtCap(t *testing.T) {
	s := newAgState()
	start, end := agWindow()
	s.seedEmail(t, 1, []string{"user@remote.example"})
	for i := 0; i < 5; i++ {
		agAttempt(t, s, 1, "mx1.remote.example", models.MTAAttemptDelivered, start.Add(time.Duration(i)*time.Minute))
	}
	agg := &Aggregator{db: agGorm(t, s), MaxAttempts: 3}
	aggs, skipped, err := agg.Aggregate(context.Background(), start, end)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("truncation must count as skipped, got %d", skipped)
	}
	total := int64(0)
	for _, a := range aggs {
		total += a.Successful + a.Failed
	}
	if total != 3 {
		t.Fatalf("cap not applied: %d sessions, want 3", total)
	}
}

func TestAggregatorNeedsDB(t *testing.T) {
	if _, _, err := (&Aggregator{}).Aggregate(context.Background(), time.Now(), time.Now()); err == nil {
		t.Fatalf("a db-less aggregator must refuse")
	}
}
