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

package inbound

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
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

// TLS-RPT ingestion tests: the allowlist grammar, the gzip/size caps on the
// report path, and the idempotent store (the R-17 DMARC pattern — the fake
// driver reuses the dmarc_test.go INSERT parser and dedup discipline over the
// (user_id, report_id, policy_domain) unique index).

// ---------- RCPT allowlist ----------

func TestTlsRptAllowlistGrammar(t *testing.T) {
	a := NewTlsRptAllowlist()
	if !a.LooksLikeReport("tlsrpt@example.com") {
		t.Fatalf("tlsrpt@example.com must be recognized")
	}
	if !a.LooksLikeReport("TLSRPT@Example.COM") {
		t.Fatalf("address comparison is case-insensitive")
	}
	if a.LooksLikeReport("dmarc@example.com") {
		t.Fatalf("a different local part must not match")
	}
	if a.LooksLikeReport("tlsrpt@") || a.LooksLikeReport("tlsrpt") {
		t.Fatalf("hostless addresses must not match")
	}
	if a.LooksLikeReport("") {
		t.Fatalf("empty address must not match")
	}
	d, ok := a.ReportDomain("tlsrpt@Sub.Example.COM")
	if !ok || d != "sub.example.com" {
		t.Fatalf("ReportDomain = (%q, %v), want (sub.example.com, true)", d, ok)
	}
	var nilList *TlsRptAllowlist
	if nilList.LooksLikeReport("tlsrpt@example.com") {
		t.Fatalf("a nil allowlist accepts nothing")
	}
}

// ---------- processor + store ----------

const tlsRptTestJSON = `{
  "organization-name": "Receiver Inc.",
  "date-range": {"start-datetime": "2026-04-01T00:00:00Z", "end-datetime": "2026-04-02T00:00:00Z"},
  "report-id": "r-2026-04-01",
  "policies": [
    {
      "policy": {"policy-type": "sts", "policy-domain": "example.com"},
      "summary": {"total-successful-session-count": 10, "total-failure-session-count": 2},
      "failure-details": [
        {"result-type": "validation-failure", "sending-mta-ip": "198.51.100.1", "receiving-mta-hostname": "mx.example.com", "failed-session-count": 2}
      ]
    }
  ]
}`

var trColumns = []string{"id", "user_id", "workspace_id", "domain_id", "inbound_id", "report_id",
	"policy_domain", "organization_name", "contact_info", "policy_type", "mx_hosts",
	"successful_sessions", "failed_sessions", "details_json", "window_start", "window_end", "created_at"}

// trUniqueKey mirrors uniqueIndex idx_tls_rpt_report_dedup.
var trUniqueKey = []string{"user_id", "report_id", "policy_domain"}

type trState struct {
	mu    sync.Mutex
	cols  []string
	rows  [][]driver.Value
	calls []string
	seq   int64
}

func newTrState() *trState { return &trState{cols: trColumns, seq: 500} }

func (s *trState) colIdx(col string) int {
	for i, c := range trColumns {
		if c == col {
			return i
		}
	}
	return -1
}

func trGorm(t *testing.T, state *trState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(trConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open tlsrpt fake gorm: %v", err)
	}
	return gdb
}

type trConnector struct{ state *trState }

func (c trConnector) Connect(context.Context) (driver.Conn, error) { return &trConn{s: c.state}, nil }
func (c trConnector) Driver() driver.Driver                        { return trDrv{} }

type trDrv struct{}

func (trDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type trConn struct{ s *trState }

func (c *trConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *trConn) Close() error                        { return nil }
func (c *trConn) Begin() (driver.Tx, error)           { return trTx{}, nil }

type trTx struct{}

func (trTx) Commit() error   { return nil }
func (trTx) Rollback() error { return nil }

type trRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *trRows) Columns() []string { return r.cols }
func (r *trRows) Close() error      { return nil }
func (r *trRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// QueryContext answers the INSERT ... ON CONFLICT DO NOTHING RETURNING "id"
// the store() path issues — dedup semantics identical to the dmarc fake.
func (c *trConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	c.s.calls = append(c.s.calls, lower)
	if !strings.Contains(lower, "insert into") || !strings.Contains(lower, `"tls_rpt_reports"`) {
		return nil, fmt.Errorf("trState: cannot route query: %s", query)
	}
	cols, tuples, err := dmParseInsert(query, args)
	if err != nil {
		return nil, err
	}

	var returning []string
	if i := strings.Index(lower, " returning "); i >= 0 {
		for _, cn := range strings.Split(lower[i+len(" returning "):], ",") {
			returning = append(returning, strings.Trim(strings.TrimSpace(cn), `"`))
		}
	}

	var out [][]driver.Value
	for _, vals := range tuples {
		row := make([]driver.Value, len(trColumns))
		for i := range row {
			row[i] = nil
		}
		for j, col := range cols {
			idx := c.s.colIdx(col)
			if idx < 0 {
				return nil, fmt.Errorf("trState: tls_rpt_reports has no column %q", col)
			}
			row[idx] = vals[j]
		}
		dup := false
		for _, existing := range c.s.rows {
			match := true
			for _, k := range trUniqueKey {
				if !dmEqual(existing[c.s.colIdx(k)], row[c.s.colIdx(k)]) {
					match = false
					break
				}
			}
			if match {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		c.s.seq++
		row[c.s.colIdx("id")] = c.s.seq
		c.s.rows = append(c.s.rows, row)
		outRow := make([]driver.Value, len(returning))
		for i, cn := range returning {
			outRow[i] = row[c.s.colIdx(cn)]
		}
		out = append(out, outRow)
	}
	return &trRows{cols: returning, rows: out}, nil
}

func (c *trConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.QueryContext(ctx, query, args); err != nil {
		return nil, err
	}
	return dmResult{}, nil
}

func tlsRptTestReport(t *testing.T) *tlsrpt.TLSReport {
	t.Helper()
	r, err := tlsrpt.ParseTLSRPTReport([]byte(tlsRptTestJSON))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return r
}

func tlsRptTestInbound() *models.InboundEmail {
	uid := uint(7)
	ws := uint(3)
	domain := uint(11)
	return &models.InboundEmail{ID: 44, UUID: "inb-tr", UserID: uid, WorkspaceID: &ws, DomainID: domain, AttachmentsJSON: "[]"}
}

// Re-ingesting the same report id must be a no-op (the (user_id, report_id,
// policy_domain) dedup), mirroring the R-17 idempotency contract.
func TestTLSRPTStoreIsIdempotent(t *testing.T) {
	state := newTrState()
	p := &tlsRptProcessor{db: trGorm(t, state)}
	rec := tlsRptTestInbound()
	report := tlsRptTestReport(t)

	n, err := p.store(rec, report)
	if err != nil {
		t.Fatalf("first store: %v", err)
	}
	if n != 1 {
		t.Fatalf("first store = %d rows, want 1 (one policy entry)", n)
	}

	n, err = p.store(rec, report)
	if err != nil {
		t.Fatalf("second store: %v", err)
	}
	if n != 0 {
		t.Fatalf("second store = %d rows, want 0 (idempotent re-ingestion)", n)
	}
	state.mu.Lock()
	total := len(state.rows)
	state.mu.Unlock()
	if total != 1 {
		t.Fatalf("table has %d rows after two stores, want 1", total)
	}
}

func TestTLSRPTStoreCarriesPolicyFacts(t *testing.T) {
	state := newTrState()
	p := &tlsRptProcessor{db: trGorm(t, state)}
	rec := tlsRptTestInbound()
	if _, err := p.store(rec, tlsRptTestReport(t)); err != nil {
		t.Fatalf("store: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(state.rows))
	}
	row := state.rows[0]
	get := func(col string) driver.Value { return row[state.colIdx(col)] }
	if get("policy_domain") != "example.com" {
		t.Fatalf("policy_domain = %v", get("policy_domain"))
	}
	if get("report_id") != "r-2026-04-01" {
		t.Fatalf("report_id = %v", get("report_id"))
	}
	if get("organization_name") != "Receiver Inc." {
		t.Fatalf("organization_name = %v", get("organization_name"))
	}
	if get("successful_sessions") != int64(10) || get("failed_sessions") != int64(2) {
		t.Fatalf("session counts = %v/%v", get("successful_sessions"), get("failed_sessions"))
	}
	if !strings.Contains(get("details_json").(string), "validation-failure") {
		t.Fatalf("details_json missing the failure detail: %v", get("details_json"))
	}
	ws, _ := get("workspace_id").(int64)
	if ws != 3 {
		t.Fatalf("workspace_id = %v, want 3 (tenant scoping rides the inbound record)", get("workspace_id"))
	}
	did, _ := get("domain_id").(int64)
	if did != 11 {
		t.Fatalf("domain_id = %v, want 11", get("domain_id"))
	}
}

// End to end: a gzipped report attachment is unwrapped and stored; re-
// processing adds nothing; an oversized gzip bomb is skipped without failing
// the ingestion (never fails the inbound record).
func TestTLSRPTProcessGzipIdempotentAndBounded(t *testing.T) {
	state := newTrState()
	p := &tlsRptProcessor{db: trGorm(t, state)}

	gz := gzipBytes(t, []byte(tlsRptTestJSON))
	rec := tlsRptTestInbound()
	rec.AttachmentsJSON = fmt.Sprintf(
		`[{"filename":"report.json.gz","content_type":"application/tlsrpt+json","content":"%s"}]`,
		base64.StdEncoding.EncodeToString(gz))

	if err := p.process(context.Background(), rec); err != nil {
		t.Fatalf("process gzip report: %v", err)
	}
	if err := p.process(context.Background(), rec); err != nil {
		t.Fatalf("re-process: %v", err)
	}
	state.mu.Lock()
	total := len(state.rows)
	state.mu.Unlock()
	if total != 1 {
		t.Fatalf("rows after double process = %d, want 1", total)
	}

	// A gzip bomb inside the cap is skipped, not fatal.
	bomb := gzipBytes(t, make([]byte, maxDecompressedReportBytes+1<<20))
	rec2 := tlsRptTestInbound()
	rec2.AttachmentsJSON = fmt.Sprintf(
		`[{"filename":"bomb.json.gz","content_type":"application/tlsrpt+json","content":"%s"}]`,
		base64.StdEncoding.EncodeToString(bomb))
	if err := p.process(context.Background(), rec2); err != nil {
		t.Fatalf("oversized report must be skipped, not fatal: %v", err)
	}
	state.mu.Lock()
	total = len(state.rows)
	state.mu.Unlock()
	if total != 1 {
		t.Fatalf("bomb must not store rows, table has %d", total)
	}

	// Non-report attachments parse as misses, never errors.
	rec3 := tlsRptTestInbound()
	rec3.AttachmentsJSON = `[{"filename":"a.pdf","content_type":"application/pdf","content":"bm90IGEgcmVwb3J0"}]`
	if err := p.process(context.Background(), rec3); err != nil {
		t.Fatalf("non-report attachment must be a silent miss: %v", err)
	}
}

// The service-level hooks: EnableTLSRPT + ResolveTLSRPTRecipient resolve the
// report address to the owning verified domain (the RCPT allowlist path).
func TestTLSRPTResolveRecipient(t *testing.T) {
	state := newTrState()
	svc := &Service{tlsrpt: &tlsRptProcessor{db: trGorm(t, state)}}
	// No allowlist wired: nothing resolves.
	if _, _, ok := svc.ResolveTLSRPTRecipient([]string{"tlsrpt@example.com"}); ok {
		t.Fatalf("unwired service must not resolve report addresses")
	}
	svc.SetTLSRPTAddress(NewTlsRptAllowlist())
	if _, _, ok := svc.ResolveTLSRPTRecipient([]string{"tlsrpt@example.com"}); ok {
		t.Fatalf("no domain repo wired: nothing may resolve (nil-safe)")
	}
}

func TestTLSRPTWindowFromReport(t *testing.T) {
	r := tlsRptTestReport(t)
	if r.Start != time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) || r.End != time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("window = %s..%s", r.Start, r.End)
	}
}
