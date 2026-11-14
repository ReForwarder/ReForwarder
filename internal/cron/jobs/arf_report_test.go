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

	"github.com/goposta/posta/internal/services/arf"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The scheduled outbound auth-failure report pass runs against the same
// fake-driver arrangement as the DMARC job tests: seeded verification
// verdicts, a fake _arf TXT resolver, and a recording sender. Assertions pin
// the policy decisions: reports only to mailto abuse addresses read from the
// reported domain's live _arf record, bounded per domain, and the pass is
// inert without a generator identity.

const arfJobResults = "id,user_id,workspace_id,inbound_id,message_id,client_ip,envelope_from," +
	"spf_result,spf_domain,spf_reason,dkim_result,dkim_domain,dkim_selector," +
	"dmarc_result,dmarc_disposition,dmarc_policy_domain,dmarc_policy,arc_result,arc_domain," +
	"iprev_result,iprev_ptr,auth_results_header,created_at"

type arfJobState struct {
	mu   sync.Mutex
	rows [][]driver.Value
	seq  int64
}

func newARFJobState() *arfJobState { return &arfJobState{seq: 1100} }

func arfJobVal(v any) driver.Value {
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
		panic(fmt.Sprintf("arfJobVal: unsupported %T", v))
	}
}

func (s *arfJobState) seed(vals map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	cols := strings.Split(arfJobResults, ",")
	row := make([]driver.Value, len(cols))
	for i, c := range cols {
		if v, ok := vals[c]; ok {
			row[i] = arfJobVal(v)
		}
	}
	row[0] = s.seq
	s.rows = append(s.rows, row)
}

func arfJobGorm(t *testing.T, state *arfJobState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(arfJobConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open arf job fake gorm: %v", err)
	}
	return gdb
}

type arfJobConnector struct{ state *arfJobState }

func (c arfJobConnector) Connect(context.Context) (driver.Conn, error) {
	return &arfJobConn{s: c.state}, nil
}
func (c arfJobConnector) Driver() driver.Driver { return arfJobDrv{} }

type arfJobDrv struct{}

func (arfJobDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type arfJobConn struct{ s *arfJobState }

func (c *arfJobConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *arfJobConn) Close() error                        { return nil }
func (c *arfJobConn) Begin() (driver.Tx, error)           { return arfJobTx{}, nil }

type arfJobTx struct{}

func (arfJobTx) Commit() error   { return nil }
func (arfJobTx) Rollback() error { return nil }

type arfJobRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *arfJobRows) Columns() []string { return r.cols }
func (r *arfJobRows) Close() error      { return nil }
func (r *arfJobRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type arfJobResult struct{ rows int64 }

func (r arfJobResult) LastInsertId() (int64, error) { return 0, nil }
func (r arfJobResult) RowsAffected() (int64, error) { return r.rows, nil }

func (c *arfJobConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `from "inbound_auth_results"`) {
		return nil, fmt.Errorf("arfJobState: cannot route query: %s", query)
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
	return &arfJobRows{cols: strings.Split(arfJobResults, ","), rows: out}, nil
}

func (c *arfJobConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.QueryContext(ctx, query, args); err != nil {
		return nil, err
	}
	return arfJobResult{}, nil
}

// recordingARFSender captures every report handed to the sender seam.
type recordingARFSender struct {
	mu   sync.Mutex
	sent []*arf.OutboundReportRequest
}

func (r *recordingARFSender) SendReport(_ context.Context, req *arf.OutboundReportRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, req)
	return nil
}

func (r *recordingARFSender) captured() []*arf.OutboundReportRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*arf.OutboundReportRequest(nil), r.sent...)
}

// fakeARFTXT is the _arf DNS seam: per-domain published TXT records.
type fakeARFTXT struct {
	records map[string][]string
}

func (f *fakeARFTXT) lookup(_ context.Context, name string) ([]string, error) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if r, ok := f.records[name]; ok {
		return r, nil
	}
	return nil, errors.New("no such host")
}

// newARFJobFixture builds the job with two seeded DKIM failures for
// sender.example.com inside the window.
func newARFJobFixture(t *testing.T, txt map[string][]string, sender arf.Sender, from, org string) (*ARFReportJob, time.Time) {
	t.Helper()
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	state := newARFJobState()
	for i, id := range []int64{1, 2} {
		state.seed(map[string]any{
			"created_at": start.Add(time.Duration(i+1) * time.Hour),
			"inbound_id": id, "user_id": int64(1), "client_ip": "192.0.2.10",
			"dkim_result": "fail", "dkim_domain": "sender.example.com", "dkim_selector": "s1",
			"auth_results_header": "authserv-id: reporter.example;\r\n\tdkim=fail header.d=sender.example.com",
		})
	}
	job := NewARFReportJob(arfJobGorm(t, state), arf.NewAggregator(arfJobGorm(t, state)), sender, from, org, "1.0.0")
	job.SetTXTLookup((&fakeARFTXT{records: txt}).lookup)
	job.SetMessageSource(func(ctx context.Context, inboundID uint) ([]byte, error) {
		return []byte("Subject: spoofed\r\n\r\nbody"), nil
	})
	job.SetNow(func() time.Time { return start.Add(30 * time.Hour) })
	return job, start
}

// The dispatch wiring: a window with addressable failures produces one
// bounded per-message report per reported domain, addressed ONLY to the
// mailto abuse addresses read from the domain's live _arf record, with the
// complete multipart/report document.
func TestARFReportJobDispatchesToLiveDNSAddresses(t *testing.T) {
	sender := &recordingARFSender{}
	job, _ := newARFJobFixture(t, map[string][]string{
		"_arf.sender.example.com": {"mailto:arf@sender.example.com!10m"},
	}, sender, "auth-failure-reports@reporter.example", "reporter.example")

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sent := sender.captured()
	if len(sent) != 2 {
		t.Fatalf("reports sent = %d, want 2 (one per failing message, bounded)", len(sent))
	}
	req := sent[0]
	if req.ReportedDomain != "sender.example.com" || req.Family != "dkim" {
		t.Fatalf("report = %+v", req)
	}
	if len(req.To) != 1 || req.To[0] != "arf@sender.example.com" {
		t.Fatalf("recipients = %v, want only the DNS-published mailto address (extension dropped)", req.To)
	}
	if req.From != "auth-failure-reports@reporter.example" {
		t.Fatalf("from = %s", req.From)
	}
	if req.ReportID == "" || !strings.Contains(req.ReportID, "dkim") {
		t.Fatalf("report id = %q", req.ReportID)
	}
	if !strings.Contains(req.Document, "Reported-Domain: sender.example.com") ||
		!strings.Contains(req.Document, "Feedback-Type: auth-failure") {
		t.Fatalf("document missing the feedback-report fields:\n%s", req.Document)
	}
	if req.UserID != 1 {
		t.Fatalf("attribution user = %d, want the receiving tenant", req.UserID)
	}
}

// A reported domain whose DNS publishes no _arf record is skipped this pass —
// reports are never sent to guessed or stored addresses.
func TestARFReportJobSkipsDomainsWithoutDNSAddress(t *testing.T) {
	sender := &recordingARFSender{}
	job, _ := newARFJobFixture(t, map[string][]string{}, sender, "auth-failure-reports@reporter.example", "reporter.example")

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := sender.captured(); len(got) != 0 {
		t.Fatalf("reports sent = %d, want 0 (no DNS-published _arf address)", len(got))
	}
}

// Quiet day: no addressable failures in the window — the pass is inert and
// never mails external addresses.
func TestARFReportJobInertOnQuietDay(t *testing.T) {
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	state := newARFJobState() // no rows
	sender := &recordingARFSender{}
	job := NewARFReportJob(arfJobGorm(t, state), arf.NewAggregator(arfJobGorm(t, state)), sender, "auth-failure-reports@reporter.example", "reporter.example", "1.0.0")
	job.SetTXTLookup((&fakeARFTXT{records: map[string][]string{
		"_arf.sender.example.com": {"mailto:arf@sender.example.com"},
	}}).lookup)
	job.SetNow(func() time.Time { return start.Add(30 * time.Hour) })

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := sender.captured(); len(got) != 0 {
		t.Fatalf("reports sent = %d, want 0 (quiet day)", len(got))
	}
}

// The pass is inert without a generator identity — nothing is ever sent
// without an explicit from address or organization name.
func TestARFReportJobInertWithoutIdentity(t *testing.T) {
	for _, cfg := range []struct{ from, org string }{
		{from: "", org: "reporter.example"},
		{from: "auth-failure-reports@reporter.example", org: ""},
	} {
		sender := &recordingARFSender{}
		job, _ := newARFJobFixture(t, map[string][]string{
			"_arf.sender.example.com": {"mailto:arf@sender.example.com"},
		}, sender, cfg.from, cfg.org)
		_ = job
		if err := job.Run(context.Background(), nil); err != nil {
			t.Fatalf("Run (from=%q org=%q): %v", cfg.from, cfg.org, err)
		}
		if got := sender.captured(); len(got) != 0 {
			t.Fatalf("reports sent = %d, want 0 (identity not configured)", len(got))
		}
	}
}

// A send failure is logged and dropped — the pass completes without error
// (best-effort reports, no retry queue).
func TestARFReportJobSendFailureIsLoggedNotFatal(t *testing.T) {
	sender := arf.SenderFunc(func(ctx context.Context, req *arf.OutboundReportRequest) error {
		return errors.New("relay down")
	})
	job, _ := newARFJobFixture(t, map[string][]string{
		"_arf.sender.example.com": {"mailto:arf@sender.example.com"},
	}, sender, "auth-failure-reports@reporter.example", "reporter.example")

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("a send failure must not fail the pass: %v", err)
	}
}
