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

package spamfilter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// EventReputationSource tests: the aggregation query runs against a fake
// database/sql driver that answers the one SELECT the source issues (the
// route_action_test.go pattern). The query is fully parameterized; the fake
// asserts the bound domain filter arrives as an argument.

type repState struct {
	mu   sync.Mutex
	rows []repRow
	// lastArgs records the bound parameters of the most recent query.
	lastArgs []driver.Value
	fail     bool
}

type repRow struct {
	Type    string
	Address string
	N       int64
}

func (s *repState) answer(args []driver.NamedValue) ([]repRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range args {
		s.lastArgs = append(s.lastArgs, a.Value)
	}
	if s.fail {
		return nil, errors.New("db unavailable")
	}
	// Honor the bound LIKE filter the way Postgres would, so the fake
	// verifies the query's scoping rather than ignoring it.
	like := ""
	if len(args) >= 2 {
		if v, ok := args[1].Value.(string); ok {
			like = strings.TrimPrefix(v, "%@")
		}
	}
	out := make([]repRow, 0, len(s.rows))
	for _, r := range s.rows {
		if like != "" && !strings.HasSuffix(strings.ToLower(r.Address), like) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

type repConnector struct{ state *repState }

func (c repConnector) Connect(context.Context) (driver.Conn, error) { return &repConn{s: c.state}, nil }
func (c repConnector) Driver() driver.Driver                        { return repDrv{} }

type repDrv struct{}

func (repDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type repConn struct{ s *repState }

func (c *repConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *repConn) Close() error                        { return nil }
func (c *repConn) Begin() (driver.Tx, error)           { return repTx{}, nil }

type repTx struct{}

func (repTx) Commit() error   { return nil }
func (repTx) Rollback() error { return nil }

func (c *repConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (c *repConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if !strings.Contains(lower, "from email_events") || !strings.Contains(lower, "unnest") {
		return nil, errors.New("repState: unexpected query: " + query)
	}
	rows, err := c.s.answer(args)
	if err != nil {
		return nil, err
	}
	cols := []string{"type", "address", "n"}
	vals := make([][]driver.Value, 0, len(rows))
	for _, r := range rows {
		vals = append(vals, []driver.Value{r.Type, r.Address, r.N})
	}
	return &repRows{cols: cols, vals: vals}, nil
}

type repRows struct {
	cols []string
	vals [][]driver.Value
	i    int
}

func (r *repRows) Columns() []string { return r.cols }
func (r *repRows) Close() error      { return nil }
func (r *repRows) Next(dest []driver.Value) error {
	if r.i >= len(r.vals) {
		return io.EOF
	}
	copy(dest, r.vals[r.i])
	r.i++
	return nil
}

func repGorm(t *testing.T, state *repState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(repConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open reputation fake gorm: %v", err)
	}
	return gdb
}

func TestEventReputationSourceAggregatesByType(t *testing.T) {
	state := &repState{rows: []repRow{
		{Type: "sent", Address: "a@sender.example", N: 60},
		{Type: "sent", Address: "b@sender.example", N: 40},
		{Type: "sent", Address: "c@other.example", N: 500}, // different domain: filtered in SQL (fake returns it anyway)
		{Type: "bounced", Address: "a@sender.example", N: 25},
		{Type: "complained", Address: "b@sender.example", N: 1},
	}}
	src := NewEventReputationSource(repGorm(t, state))

	rep, err := src.SenderReputation(context.Background(), 7, "Sender.Example")
	if err != nil {
		t.Fatalf("SenderReputation: %v", err)
	}
	if rep.Sent != 100 || rep.Bounced != 25 || rep.Complained != 1 {
		t.Fatalf("reputation = %+v, want sent=100 bounced=25 complained=1", rep)
	}

	// The bound parameters must carry the tenant scope and the domain filter
	// (never string-concatenated SQL).
	if len(state.lastArgs) != 2 {
		t.Fatalf("query args = %v, want 2 bound parameters", state.lastArgs)
	}
	if userID, ok := state.lastArgs[0].(int64); !ok || userID != 7 {
		t.Fatalf("arg0 = %v (%T), want user id 7", state.lastArgs[0], state.lastArgs[0])
	}
	if like, ok := state.lastArgs[1].(string); !ok || like != "%@sender.example" {
		t.Fatalf("arg1 = %v (%T), want %%@sender.example", state.lastArgs[1], state.lastArgs[1])
	}
}

func TestEventReputationSourceErrorPropagates(t *testing.T) {
	state := &repState{fail: true}
	src := NewEventReputationSource(repGorm(t, state))
	if _, err := src.SenderReputation(context.Background(), 7, "sender.example"); err == nil {
		t.Fatal("source outage must surface as an error (the scorer abstains)")
	}
}

func TestEventReputationSourceEmptyDomain(t *testing.T) {
	src := NewEventReputationSource(nil)
	if rep, err := src.SenderReputation(context.Background(), 7, ""); err != nil || rep.Sent != 0 {
		t.Fatalf("empty domain = %+v, %v; want zero reputation, nil error", rep, err)
	}
}

func TestReputationRateFloor(t *testing.T) {
	small := Reputation{Sent: 10, Bounced: 10}
	if br := small.BounceRate(); br != -1 {
		t.Fatalf("BounceRate on a %d-message sample = %v, want -1 (abstain)", small.Sent, br)
	}
	if cr := small.ComplaintRate(); cr != -1 {
		t.Fatalf("ComplaintRate on a %d-message sample = %v, want -1 (abstain)", small.Sent, cr)
	}
	big := Reputation{Sent: 100, Bounced: 20, Complained: 1}
	if br := big.BounceRate(); br < 0.199 || br > 0.201 {
		t.Fatalf("BounceRate = %v, want 0.2", br)
	}
	if cr := big.ComplaintRate(); cr < 0.0099 || cr > 0.0101 {
		t.Fatalf("ComplaintRate = %v, want 0.01", cr)
	}
}
