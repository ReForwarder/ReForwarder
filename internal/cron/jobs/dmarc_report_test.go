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

	"github.com/goposta/posta/internal/services/dmarc"
	"github.com/goposta/posta/internal/services/inbound/verify"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The scheduled outbound DMARC aggregate report pass runs against the same
// fake-driver arrangement as the TLS-RPT job tests: seeded verification
// verdicts, a fake _dmarc policy resolver, and a recording sender.
// Assertions pin the policy decisions: reports only to mailto rua endpoints
// read from the reported domain's live DNS record, only for windows with
// DMARC-evaluated messages, and the pass is inert without a generator
// identity.

const dmJobResults = "id,user_id,workspace_id,inbound_id,message_id,client_ip,envelope_from," +
	"spf_result,spf_domain,spf_reason,dkim_result,dkim_domain,dkim_selector," +
	"dmarc_result,dmarc_disposition,dmarc_policy_domain,dmarc_policy,created_at"

type dmJobState struct {
	mu   sync.Mutex
	rows [][]driver.Value
	seq  int64
}

func newDMJobState() *dmJobState { return &dmJobState{seq: 700} }

func dmJobVal(v any) driver.Value {
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
		panic(fmt.Sprintf("dmJobVal: unsupported %T", v))
	}
}

func (s *dmJobState) seed(vals map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	cols := strings.Split(dmJobResults, ",")
	row := make([]driver.Value, len(cols))
	for i, c := range cols {
		if v, ok := vals[c]; ok {
			row[i] = dmJobVal(v)
		}
	}
	row[0] = s.seq
	s.rows = append(s.rows, row)
}

func dmJobGorm(t *testing.T, state *dmJobState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(dmJobConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open dmarc job fake gorm: %v", err)
	}
	return gdb
}

type dmJobConnector struct{ state *dmJobState }

func (c dmJobConnector) Connect(context.Context) (driver.Conn, error) { return &dmJobConn{s: c.state}, nil }
func (c dmJobConnector) Driver() driver.Driver                        { return dmJobDrv{} }

type dmJobDrv struct{}

func (dmJobDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type dmJobConn struct{ s *dmJobState }

func (c *dmJobConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *dmJobConn) Close() error                        { return nil }
func (c *dmJobConn) Begin() (driver.Tx, error)           { return dmJobTx{}, nil }

type dmJobTx struct{}

func (dmJobTx) Commit() error   { return nil }
func (dmJobTx) Rollback() error { return nil }

type dmJobRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *dmJobRows) Columns() []string { return r.cols }
func (r *dmJobRows) Close() error      { return nil }
func (r *dmJobRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type dmJobResult struct{ rows int64 }

func (r dmJobResult) LastInsertId() (int64, error) { return 0, nil }
func (r dmJobResult) RowsAffected() (int64, error) { return r.rows, nil }

func (c *dmJobConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `from "inbound_auth_results"`) {
		return nil, fmt.Errorf("dmJobState: cannot route query: %s", query)
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
		created, _ := row[17].(time.Time)
		if created.Before(windowStart) || !created.Before(windowEnd) {
			continue
		}
		out = append(out, row)
	}
	return &dmJobRows{cols: strings.Split(dmJobResults, ","), rows: out}, nil
}

func (c *dmJobConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.QueryContext(ctx, query, args); err != nil {
		return nil, err
	}
	return dmJobResult{}, nil
}

// recordingDMARCSender captures every report handed to the sender seam.
type recordingDMARCSender struct {
	mu   sync.Mutex
	sent []*dmarc.OutboundReportRequest
}

func (r *recordingDMARCSender) SendReport(_ context.Context, req *dmarc.OutboundReportRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, req)
	return nil
}

func (r *recordingDMARCSender) captured() []*dmarc.OutboundReportRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*dmarc.OutboundReportRequest(nil), r.sent...)
}

// fakeDMARCPolicy is the _dmarc DNS seam: per-domain published policies.
type fakeDMARCPolicy struct {
	records map[string]*verify.DMARCPolicy
}

func (f *fakeDMARCPolicy) lookup(_ context.Context, domain string) (string, *verify.DMARCPolicy, error) {
	if p, ok := f.records[domain]; ok {
		return domain, p, nil
	}
	return domain, nil, errors.New("no usable DMARC record for " + domain)
}

// newDMARCJobFixture builds the job with one seeded verdict for
// example.com (source 192.0.2.10) inside the window.
func newDMARCJobFixture(t *testing.T, records map[string]*verify.DMARCPolicy, sender dmarc.Sender, from, org string) (*DMARCReportJob, time.Time) {
	t.Helper()
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	state := newDMJobState()
	state.seed(map[string]any{
		"created_at":          start.Add(5 * time.Hour),
		"client_ip":           "192.0.2.10",
		"dmarc_result":        "pass",
		"dmarc_policy_domain": "example.com",
		"dmarc_disposition":   "none",
		"spf_result":          "pass",
		"spf_domain":          "mail.example.com",
		"dkim_result":         "pass",
		"dkim_domain":         "example.com",
		"dkim_selector":       "s1",
	})
	job := NewDMARCReportJob(dmJobGorm(t, state), dmarc.NewAggregator(dmJobGorm(t, state)), sender, from, org)
	job.SetPolicyLookup((&fakeDMARCPolicy{records: records}).lookup)
	job.SetNow(func() time.Time { return start.Add(30 * time.Hour) }) // any time inside the next day
	return job, start
}

// The dispatch wiring: a window with DMARC evaluations produces one report
// per reported domain, addressed ONLY to the mailto rua endpoints read from
// the live DNS policy, with the document attached and the published policy
// on board.
func TestDMARCReportJobDispatchesToLiveDNSRuas(t *testing.T) {
	policies := &fakeDMARCPolicy{records: map[string]*verify.DMARCPolicy{
		"example.com": {
			P: "reject", ADKIM: "r", ASPF: "r", PCT: 100,
			RUA: []string{"mailto:agg@example.com!50m"},
		},
	}}
	sender := &recordingDMARCSender{}
	job, _ := newDMARCJobFixture(t, policies.records, sender, "dmarc-report@reporter.example", "reporter.example")

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sent := sender.captured()
	if len(sent) != 1 {
		t.Fatalf("reports sent = %d, want 1", len(sent))
	}
	req := sent[0]
	if req.PolicyDomain != "example.com" {
		t.Fatalf("policy domain = %s, want example.com", req.PolicyDomain)
	}
	if len(req.To) != 1 || req.To[0] != "agg@example.com" {
		t.Fatalf("recipients = %v, want only the DNS-published mailto rua (extension dropped)", req.To)
	}
	if req.From != "dmarc-report@reporter.example" {
		t.Fatalf("from = %s", req.From)
	}
	if len(req.XML) == 0 || !strings.Contains(string(req.XML), "example.com") {
		t.Fatal("the report document is missing")
	}
	if req.ReportID == "" {
		t.Fatal("report id empty")
	}
}

// A reported domain whose DNS publishes no usable DMARC record is skipped
// this pass — reports are never sent to guessed or stored addresses.
func TestDMARCReportJobSkipsDomainsWithoutDNSPolicy(t *testing.T) {
	policies := &fakeDMARCPolicy{records: map[string]*verify.DMARCPolicy{}} // nothing published
	sender := &recordingDMARCSender{}
	job, _ := newDMARCJobFixture(t, policies.records, sender, "dmarc-report@reporter.example", "reporter.example")

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := sender.captured(); len(got) != 0 {
		t.Fatalf("reports sent = %d, want 0 (no DNS-published rua)", len(got))
	}
}

// A domain whose DNS publishes the policy but only non-mailto rua endpoints
// (https:) is skipped — the system-forward dispatch is mail-only.
func TestDMARCReportJobSkipsDomainsWithoutMailtoRuas(t *testing.T) {
	policies := &fakeDMARCPolicy{records: map[string]*verify.DMARCPolicy{
		"example.com": {P: "reject", ADKIM: "r", ASPF: "r", PCT: 100,
			RUA: []string{"https://reports.example.com/callback"}},
	}}
	sender := &recordingDMARCSender{}
	job, _ := newDMARCJobFixture(t, policies.records, sender, "dmarc-report@reporter.example", "reporter.example")

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := sender.captured(); len(got) != 0 {
		t.Fatalf("reports sent = %d, want 0 (https rua is not mail-dispatchable)", len(got))
	}
}

// Quiet day: no DMARC evaluations in the window — the pass is inert and
// never mails external addresses.
func TestDMARCReportJobInertOnQuietDay(t *testing.T) {
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	state := newDMJobState() // no rows
	sender := &recordingDMARCSender{}
	job := NewDMARCReportJob(dmJobGorm(t, state), dmarc.NewAggregator(dmJobGorm(t, state)), sender, "dmarc-report@reporter.example", "reporter.example")
	job.SetPolicyLookup((&fakeDMARCPolicy{records: map[string]*verify.DMARCPolicy{
		"example.com": {P: "reject", ADKIM: "r", ASPF: "r", PCT: 100,
			RUA: []string{"mailto:agg@example.com"}},
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
func TestDMARCReportJobInertWithoutIdentity(t *testing.T) {
	for _, cfg := range []struct{ from, org string }{
		{from: "", org: "reporter.example"},
		{from: "dmarc-report@reporter.example", org: ""},
	} {
		sender := &recordingDMARCSender{}
		job, _ := newDMARCJobFixture(t, map[string]*verify.DMARCPolicy{
			"example.com": {P: "reject", ADKIM: "r", ASPF: "r", PCT: 100,
				RUA: []string{"mailto:agg@example.com"}},
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
// (best-effort aggregates, no retry queue).
func TestDMARCReportJobSendFailureIsLoggedNotFatal(t *testing.T) {
	policies := &fakeDMARCPolicy{records: map[string]*verify.DMARCPolicy{
		"example.com": {P: "reject", ADKIM: "r", ASPF: "r", PCT: 100,
			RUA: []string{"mailto:agg@example.com"}},
	}}
	sender := dmarc.SenderFunc(func(ctx context.Context, req *dmarc.OutboundReportRequest) error {
		return errors.New("relay down")
	})
	job, _ := newDMARCJobFixture(t, policies.records, sender, "dmarc-report@reporter.example", "reporter.example")

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("a send failure must not fail the pass: %v", err)
	}
}
