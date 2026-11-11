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

package jobs

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
	"github.com/goposta/posta/internal/services/tlsrpt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The scheduled outbound TLS report pass runs against the same fake-driver
// arrangement as the aggregator tests: seeded attempts + recipient lists, a
// fake _smtp._tls TXT resolver, and a recording sender. Assertions pin the
// policy decisions: reports only to DNS-published rua endpoints, only for
// attributable sessions, and the pass is inert without a generator identity.

const tlsJobAttempts = "id,email_id,workspace_id,attempt,mx_host,ip,response_code,response_text,outcome,started_at,finished_at,created_at"

type tlsJobState struct {
	mu     sync.Mutex
	tables map[string]*tlsJobTable
	seq    int64
}

type tlsJobTable struct {
	cols []string
	rows [][]driver.Value
}

func newTLSJobState() *tlsJobState {
	return &tlsJobState{
		tables: map[string]*tlsJobTable{
			"mta_delivery_attempts": {cols: strings.Split(tlsJobAttempts, ",")},
			"emails":                {cols: []string{"id", "recipients"}},
		},
		seq: 300,
	}
}

func (s *tlsJobState) seed(t *testing.T, table string, cols []string, vals map[string]any) {
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
		row[idx] = tlsJobVal(v)
	}
	if table == "mta_delivery_attempts" && row[0] == nil {
		s.seq++
		row[0] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func tlsJobVal(v any) driver.Value {
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
	case []string:
		return "{" + strings.Join(t, ",") + "}"
	default:
		panic(fmt.Sprintf("tlsJobVal: unsupported %T", v))
	}
}

func tlsJobGorm(t *testing.T, state *tlsJobState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(tlsJobConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open tlsrpt job fake gorm: %v", err)
	}
	return gdb
}

type tlsJobConnector struct{ state *tlsJobState }

func (c tlsJobConnector) Connect(context.Context) (driver.Conn, error) {
	return &tlsJobConn{s: c.state}, nil
}
func (c tlsJobConnector) Driver() driver.Driver { return tlsJobDrv{} }

type tlsJobDrv struct{}

func (tlsJobDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type tlsJobConn struct{ s *tlsJobState }

func (c *tlsJobConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *tlsJobConn) Close() error                        { return nil }
func (c *tlsJobConn) Begin() (driver.Tx, error)           { return tlsJobTx{}, nil }

type tlsJobTx struct{}

func (tlsJobTx) Commit() error   { return nil }
func (tlsJobTx) Rollback() error { return nil }

type tlsJobRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *tlsJobRows) Columns() []string { return r.cols }
func (r *tlsJobRows) Close() error      { return nil }
func (r *tlsJobRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type tlsJobResult struct{ rows int64 }

func (r tlsJobResult) LastInsertId() (int64, error) { return 0, nil }
func (r tlsJobResult) RowsAffected() (int64, error) { return r.rows, nil }

func (c *tlsJobConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.Contains(lower, "from \"mta_delivery_attempts\""):
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
		for _, row := range s.tables["mta_delivery_attempts"].rows {
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
		return &tlsJobRows{cols: s.tables["mta_delivery_attempts"].cols, rows: out}, nil
	case strings.Contains(lower, "from \"emails\""):
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
		return &tlsJobRows{cols: []string{"id", "recipients"}, rows: out}, nil
	}
	return nil, fmt.Errorf("tlsJobState: cannot route query: %s", query)
}

func (c *tlsJobConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.QueryContext(ctx, query, args); err != nil {
		return nil, err
	}
	return tlsJobResult{}, nil
}

// recordingSender captures every report handed to the sender seam.
type recordingSender struct {
	mu     sync.Mutex
	sent   []*tlsrpt.OutboundReportRequest
	errFor map[string]error // policy domain → failure injection
}

func (r *recordingSender) SendReport(_ context.Context, req *tlsrpt.OutboundReportRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err, ok := r.errFor[req.PolicyDomain]; ok {
		return err
	}
	r.sent = append(r.sent, req)
	return nil
}

func fakeTxt(records map[string][]string) func(context.Context, string) ([]string, error) {
	return func(_ context.Context, name string) ([]string, error) {
		name = strings.TrimSuffix(strings.ToLower(name), ".")
		if r, ok := records[name]; ok {
			return r, nil
		}
		return nil, errors.New("no such host")
	}
}

func TestTLSRPTReportJobSendsToPublishedRuas(t *testing.T) {
	state := newTLSJobState()
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	state.seed(t, "emails", []string{"id", "recipients"}, map[string]any{"id": uint(1), "recipients": []string{"user@remote.example"}})
	state.seed(t, "emails", []string{"id", "recipients"}, map[string]any{"id": uint(2), "recipients": []string{"user@quiet.example"}})
	at := func(tbl string, emailID uint, mx, outcome string, min int) {
		state.seed(t, "mta_delivery_attempts", strings.Split(tlsJobAttempts, ","), map[string]any{
			"email_id": emailID, "mx_host": mx, "outcome": outcome,
			"started_at": start.Add(time.Duration(min) * time.Minute), "finished_at": start.Add(time.Duration(min) * time.Minute),
		})
	}
	at("a", 1, "mx1.remote.example", models.MTAAttemptDelivered, 10)
	at("a", 1, "mx1.remote.example", models.MTAAttemptTransientFailure, 11)
	at("b", 2, "mx.quiet.example", models.MTAAttemptDelivered, 12)

	sender := &recordingSender{}
	job := NewTLSRPTReportJob(tlsJobGorm(t, state), tlsrpt.NewAggregator(tlsJobGorm(t, state)), sender,
		"mta-sts-report@posta.example", "posta.example")
	job.SetTXTLookup(fakeTxt(map[string][]string{
		// Only remote.example publishes a TLS-RPT record.
		"_smtp._tls.remote.example": {"v=TLSRPTv1; rua=mailto:tlsrpt@remote.example"},
	}))
	job.SetNow(func() time.Time { return end.Add(time.Hour) })

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d reports (%v), want exactly 1 (quiet.example publishes no rua)", len(sender.sent), sender.sent)
	}
	req := sender.sent[0]
	if req.PolicyDomain != "remote.example" {
		t.Fatalf("policy domain = %q", req.PolicyDomain)
	}
	if len(req.To) != 1 || req.To[0] != "tlsrpt@remote.example" {
		t.Fatalf("recipients = %v, want the published rua only", req.To)
	}
	if req.From != "mta-sts-report@posta.example" {
		t.Fatalf("from = %q", req.From)
	}
	if len(req.JSON) == 0 {
		t.Fatalf("report document missing")
	}
	// The attached document must round-trip through our parser.
	doc, err := tlsrpt.ParseTLSRPTReport(req.JSON)
	if err != nil {
		t.Fatalf("generated document invalid: %v", err)
	}
	if len(doc.Policies) != 1 || doc.Policies[0].SuccessfulSessions != 1 || doc.Policies[0].FailedSessions != 1 {
		t.Fatalf("document summary wrong: %+v", doc.Policies)
	}
	// report-id keys on the window + policy domain.
	if req.ReportID != tlsrpt.OutboundReportID(start, "remote.example") {
		t.Fatalf("report id = %q", req.ReportID)
	}
}

func TestTLSRPTReportJobInertWithoutIdentity(t *testing.T) {
	state := newTLSJobState()
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	state.seed(t, "emails", []string{"id", "recipients"}, map[string]any{"id": uint(1), "recipients": []string{"user@remote.example"}})
	state.seed(t, "mta_delivery_attempts", strings.Split(tlsJobAttempts, ","), map[string]any{
		"email_id": uint(1), "mx_host": "mx1.remote.example", "outcome": models.MTAAttemptDelivered,
		"started_at": start.Add(time.Minute), "finished_at": start.Add(time.Minute),
	})

	sender := &recordingSender{}
	// No org identity configured: the pass is inert even with sessions.
	job := NewTLSRPTReportJob(tlsJobGorm(t, state), tlsrpt.NewAggregator(tlsJobGorm(t, state)), sender, "", "")
	job.SetTXTLookup(fakeTxt(map[string][]string{"_smtp._tls.remote.example": {"v=TLSRPTv1; rua=mailto:tlsrpt@remote.example"}}))
	job.SetNow(func() time.Time { return end.Add(time.Hour) })

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("inert run must succeed silently: %v", err)
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != 0 {
		t.Fatalf("no generator identity: nothing may be sent, got %d", len(sender.sent))
	}
}

func TestTLSRPTReportJobQuietWindowSendsNothing(t *testing.T) {
	state := newTLSJobState()
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	sender := &recordingSender{}
	job := NewTLSRPTReportJob(tlsJobGorm(t, state), tlsrpt.NewAggregator(tlsJobGorm(t, state)), sender,
		"mta-sts-report@posta.example", "posta.example")
	job.SetTXTLookup(fakeTxt(map[string][]string{"_smtp._tls.remote.example": {"v=TLSRPTv1; rua=mailto:tlsrpt@remote.example"}}))
	job.SetNow(func() time.Time { return end.Add(time.Hour) })

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("quiet run: %v", err)
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != 0 {
		t.Fatalf("a quiet window must never mail external addresses, sent %d", len(sender.sent))
	}
}

func TestTLSRPTReportJobSendFailureIsLoggedNotFatal(t *testing.T) {
	state := newTLSJobState()
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	state.seed(t, "emails", []string{"id", "recipients"}, map[string]any{"id": uint(1), "recipients": []string{"user@remote.example"}})
	state.seed(t, "mta_delivery_attempts", strings.Split(tlsJobAttempts, ","), map[string]any{
		"email_id": uint(1), "mx_host": "mx1.remote.example", "outcome": models.MTAAttemptDelivered,
		"started_at": start.Add(time.Minute), "finished_at": start.Add(time.Minute),
	})

	sender := &recordingSender{errFor: map[string]error{"remote.example": errors.New("smtp unavailable")}}
	job := NewTLSRPTReportJob(tlsJobGorm(t, state), tlsrpt.NewAggregator(tlsJobGorm(t, state)), sender,
		"mta-sts-report@posta.example", "posta.example")
	job.SetTXTLookup(fakeTxt(map[string][]string{"_smtp._tls.remote.example": {"v=TLSRPTv1; rua=mailto:tlsrpt@remote.example"}}))
	job.SetNow(func() time.Time { return end.Add(time.Hour) })

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("a send failure is logged and the pass continues, not fatal: %v", err)
	}
}
