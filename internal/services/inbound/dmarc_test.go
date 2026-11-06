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

package inbound

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The DMARC processor faces remote input twice: an attachment's decompressed
// bytes (a gzip bomb inside the compressed-attachment envelope) and the
// deduplication of re-delivered reports (the ON CONFLICT DO NOTHING store).
// These tests pin both: decompression is bounded at maxDecompressedReportBytes
// (R-17), and re-ingesting the same report inserts nothing new and reports
// zero new rows (idempotency over the unique (user_id, report_id, source_ip)
// key) — through the real gorm on a fake database/sql driver.

const dmarcTestXML = `<?xml version="1.0" encoding="UTF-8"?>
<feedback>
  <report_metadata>
    <org_name>google.com</org_name>
    <email>noreply-dmarc-support@google.com</email>
    <report_id>9391651994969114919</report_id>
    <date_range><begin>1770000000</begin><end>1770086399</end></date_range>
  </report_metadata>
  <policy_published>
    <domain>example.com</domain>
    <adkim>r</adkim>
    <aspf>r</aspf>
    <p>none</p>
  </policy_published>
  <record>
    <row><source_ip>192.0.2.10</source_ip><count>2</count>
      <policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>fail</spf></policy_evaluated>
    </row>
    <identifiers><header_from>example.com</header_from></identifiers>
    <auth_results><dkim><domain>example.com</domain><result>pass</result></dkim></auth_results>
  </record>
  <record>
    <row><source_ip>192.0.2.11</source_ip><count>1</count>
      <policy_evaluated><disposition>none</disposition><dkim>fail</dkim><spf>pass</spf></policy_evaluated>
    </row>
    <identifiers><header_from>example.com</header_from></identifiers>
    <auth_results><spf><domain>example.com</domain><result>pass</result></spf></auth_results>
  </record>
</feedback>`

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// A gzip bomb that decompresses past maxDecompressedReportBytes must be
// refused, not read to the end — the compressed attachment fits the ingest
// gate, but its expansion would exhaust worker memory (R-17).
func TestMaybeGunzipBoundsDecompressedSize(t *testing.T) {
	bomb := gzipBytes(t, bytes.Repeat([]byte{0}, maxDecompressedReportBytes+1<<20))
	if got := len(bomb); got > 10<<20 {
		t.Fatalf("fixture itself exceeds the compressed gate: %d bytes", got)
	}

	out, err := maybeGunzip(bomb)
	if err == nil {
		t.Fatalf("maybeGunzip(bomb) = %d bytes, want an error", len(out))
	}
	if !strings.Contains(err.Error(), "report cap") {
		t.Fatalf("maybeGunzip(bomb) error = %v, want the report-cap message", err)
	}
	if out != nil {
		t.Fatalf("maybeGunzip(bomb) returned content alongside the error")
	}
}

// A legitimate gzipped report — under the cap — still round-trips.
func TestMaybeGunzipRoundTrip(t *testing.T) {
	raw := gzipBytes(t, []byte(dmarcTestXML))
	out, err := maybeGunzip(raw)
	if err != nil {
		t.Fatalf("maybeGunzip: %v", err)
	}
	if string(out) != dmarcTestXML {
		t.Fatalf("maybeGunzip round-trip mismatch")
	}
}

// blobStub is a blobStore serving one reader — the R-17 blob-path bound test.
type blobStub struct{ r io.Reader }

func (b blobStub) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(b.r), nil
}

// An oversized blob-backed attachment is refused at the read, not streamed to
// the end into memory (R-17).
func TestAttachmentContentBoundsBlobRead(t *testing.T) {
	p := &dmarcProcessor{blob: blobStub{r: bytes.NewReader(make([]byte, maxDecompressedReportBytes+1024))}}
	_, err := p.attachmentContent(context.Background(), models.InboundAttachmentMeta{StorageKey: "k"})
	if err == nil || !strings.Contains(err.Error(), "report cap") {
		t.Fatalf("blob read = %v, want the report-cap error", err)
	}
}

// The blob-backed happy path still parses: a stored gzipped report is read,
// bounded, unwrapped and parsed.
func TestAttachmentContentBlobGzipHappyPath(t *testing.T) {
	p := &dmarcProcessor{blob: blobStub{r: bytes.NewReader(gzipBytes(t, []byte(dmarcTestXML)))}}
	raw, err := p.attachmentContent(context.Background(), models.InboundAttachmentMeta{StorageKey: "k"})
	if err != nil {
		t.Fatalf("attachmentContent: %v", err)
	}
	report, err := ParseDMARCAggregateReport(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if report.ReportID != "9391651994969114919" || len(report.Records) != 2 {
		t.Fatalf("parsed report wrong: %+v", report)
	}
}

// ---------- the fake store for the ingestion tests ----------

var dmColumns = []string{"id", "user_id", "workspace_id", "domain_id", "inbound_id", "domain",
	"reporter", "reporter_email", "report_id", "source_ip", "window_start", "window_end",
	"count", "dkim_aligned", "spf_aligned", "disposition", "header_from", "created_at"}

// dmUniqueKey mirrors uniqueIndex idx_dmarc_report_dedup.
var dmUniqueKey = []string{"user_id", "report_id", "source_ip"}

type dmState struct {
	mu    sync.Mutex
	rows  [][]driver.Value
	seq   int64
	calls []string
}

func newDmState() *dmState { return &dmState{seq: 500} }

func (s *dmState) colIdx(col string) int {
	for i, c := range dmColumns {
		if c == col {
			return i
		}
	}
	return -1
}

func dmGorm(t *testing.T, state *dmState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(dmConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open dmarc fake gorm: %v", err)
	}
	return gdb
}

type dmConnector struct{ state *dmState }

func (c dmConnector) Connect(context.Context) (driver.Conn, error) { return &dmConn{s: c.state}, nil }
func (c dmConnector) Driver() driver.Driver                        { return dmDrv{} }

type dmDrv struct{}

func (dmDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type dmConn struct{ s *dmState }

func (c *dmConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *dmConn) Close() error                        { return nil }
func (c *dmConn) Begin() (driver.Tx, error)           { return dmTx{}, nil }

type dmTx struct{}

func (dmTx) Commit() error   { return nil }
func (dmTx) Rollback() error { return nil }

type dmRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *dmRows) Columns() []string { return r.cols }
func (r *dmRows) Close() error      { return nil }
func (r *dmRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func (c *dmConn) record(query string) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.calls = append(c.s.calls, strings.Join(strings.Fields(strings.ToLower(query)), " "))
}

// QueryContext answers the INSERT ... ON CONFLICT DO NOTHING RETURNING "id"
// the store() path issues: every VALUES tuple is checked against the dedup
// key, conflicting tuples are dropped (DoNothing), and the RETURNING rows are
// the tuples actually inserted — which is what gorm reports as RowsAffected.
func (c *dmConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.record(query)
	lower := c.s.calls[len(c.s.calls)-1]
	if !strings.Contains(lower, "insert into") || !strings.Contains(lower, `"dmarc_reports"`) {
		return nil, fmt.Errorf("dmState: cannot route query: %s", query)
	}
	cols, tuples, err := dmParseInsert(query, args)
	if err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	var returning []string
	if i := strings.Index(lower, " returning "); i >= 0 {
		for _, cn := range strings.Split(lower[i+len(" returning "):], ",") {
			returning = append(returning, strings.Trim(strings.TrimSpace(cn), `"`))
		}
	}

	var out [][]driver.Value
	for _, vals := range tuples {
		row := make([]driver.Value, len(dmColumns))
		for i := range row {
			row[i] = nil
		}
		for j, col := range cols {
			idx := s.colIdx(col)
			if idx < 0 {
				return nil, fmt.Errorf("dmState: dmarc_reports has no column %q", col)
			}
			row[idx] = vals[j]
		}
		// ON CONFLICT DO NOTHING over the dedup key.
		dup := false
		for _, existing := range s.rows {
			match := true
			for _, k := range dmUniqueKey {
				if !dmEqual(existing[s.colIdx(k)], row[s.colIdx(k)]) {
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
			continue // dropped: the conflict path returns no RETURNING row
		}
		s.seq++
		row[s.colIdx("id")] = s.seq
		s.rows = append(s.rows, row)
		outRow := make([]driver.Value, len(returning))
		for i, cn := range returning {
			outRow[i] = row[s.colIdx(cn)]
		}
		out = append(out, outRow)
	}
	return &dmRows{cols: returning, rows: out}, nil
}

func (c *dmConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	_, err := c.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return dmResult{}, nil
}

type dmResult struct{}

func (dmResult) LastInsertId() (int64, error) { return 0, nil }
func (dmResult) RowsAffected() (int64, error) { return 0, nil }

// dmParseInsert extracts the column list and the per-tuple bound values of a
// multi-row INSERT (bindvars are $N tokens only, so commas split cleanly).
func dmParseInsert(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
	lower := strings.ToLower(query)
	vi := strings.Index(lower, "values (")
	if vi < 0 {
		return nil, nil, fmt.Errorf("dmState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, nil, fmt.Errorf("dmState: insert without a column list: %s", query)
	}
	var cols []string
	for _, p := range strings.Split(query[open+1:close], ",") {
		cols = append(cols, strings.Trim(strings.TrimSpace(p), `"`))
	}

	valText := query[vi+len(" values "):]
	if i := strings.Index(strings.ToLower(valText), " on conflict"); i >= 0 {
		valText = valText[:i]
	}
	valText = strings.TrimSpace(valText)
	valText = strings.TrimSuffix(strings.TrimPrefix(valText, "("), ")")

	var tuples [][]driver.Value
	for _, tuple := range strings.Split(valText, "),(") {
		parts := strings.Split(tuple, ",")
		if len(parts) != len(cols) {
			return nil, nil, fmt.Errorf("dmState: %d columns but %d values in %q", len(cols), len(parts), tuple)
		}
		vals := make([]driver.Value, len(parts))
		for i, p := range parts {
			p = strings.TrimSpace(p)
			if !strings.HasPrefix(p, "$") {
				return nil, nil, fmt.Errorf("dmState: non-bindvar value %q", p)
			}
			n, err := strconv.Atoi(p[1:])
			if err != nil {
				return nil, nil, err
			}
			found := false
			for _, a := range args {
				if a.Ordinal == n {
					vals[i] = dmCanonical(a.Value)
					found = true
					break
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("dmState: no arg with ordinal %d", n)
			}
		}
		tuples = append(tuples, vals)
	}
	return cols, tuples, nil
}

func dmCanonical(v driver.Value) driver.Value {
	switch t := v.(type) {
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case time.Time:
		return t
	default:
		return v
	}
}

func dmEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if ta, ok := a.(time.Time); ok {
		tb, ok2 := b.(time.Time)
		return ok2 && ta.Equal(tb)
	}
	return a == b
}

// ---------- ingestion tests ----------

func dmarcTestReport(t *testing.T) *DMARCAggregateReport {
	t.Helper()
	report, err := ParseDMARCAggregateReport([]byte(dmarcTestXML))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return report
}

func dmarcTestInbound() *models.InboundEmail {
	uid := uint(7)
	return &models.InboundEmail{ID: 33, UUID: "inb-1", UserID: uid, DomainID: 11, AttachmentsJSON: "[]"}
}

// Re-ingesting the same report must be a no-op: the ON CONFLICT DO NOTHING
// dedup over (user_id, report_id, source_ip) drops every row on the second
// pass and store reports zero NEW rows (R-17).
func TestDMARCStoreIsIdempotent(t *testing.T) {
	state := newDmState()
	p := &dmarcProcessor{db: dmGorm(t, state)}
	rec := dmarcTestInbound()
	report := dmarcTestReport(t)

	n, err := p.store(rec, report)
	if err != nil {
		t.Fatalf("first store: %v", err)
	}
	if n != 2 {
		t.Fatalf("first store = %d rows, want 2", n)
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
	var conflictSeen bool
	for _, q := range state.calls {
		if strings.Contains(q, "insert into") && strings.Contains(q, "on conflict do nothing") {
			conflictSeen = true
		}
	}
	state.mu.Unlock()
	if total != 2 {
		t.Fatalf("table has %d rows after two stores, want 2", total)
	}
	if !conflictSeen {
		t.Fatal("store must issue INSERT ... ON CONFLICT DO NOTHING")
	}
}

// A distinct source IP in the same report id is a NEW row — the dedup key is
// (user_id, report_id, source_ip), not the report id alone.
func TestDMARCStoreKeepsDistinctSourceIPs(t *testing.T) {
	state := newDmState()
	p := &dmarcProcessor{db: dmGorm(t, state)}
	rec := dmarcTestInbound()
	report := dmarcTestReport(t)

	if n, err := p.store(rec, report); err != nil || n != 2 {
		t.Fatalf("first store = (%d, %v), want (2, nil)", n, err)
	}
	// Same report, one record's source IP changed: only that row is new.
	report.Records[0].SourceIP = "198.51.100.9"
	n, err := p.store(rec, report)
	if err != nil {
		t.Fatalf("second store: %v", err)
	}
	if n != 1 {
		t.Fatalf("store with one new source IP = %d rows, want 1", n)
	}
	state.mu.Lock()
	total := len(state.rows)
	state.mu.Unlock()
	if total != 3 {
		t.Fatalf("table has %d rows, want 3", total)
	}
}

// The end-to-end ingestion path: an inbound record carrying a base64 gzipped
// aggregate report is parsed and stored; re-processing the same message adds
// nothing (the dedup key covers the re-delivery), and an oversized bomb
// attachment is skipped without failing the ingestion.
func TestDMARCProcessIngestsGzipAndSkipsOversize(t *testing.T) {
	state := newDmState()
	p := &dmarcProcessor{db: dmGorm(t, state)}

	rec := dmarcTestInbound()
	gz := gzipBytes(t, []byte(dmarcTestXML))
	rec.AttachmentsJSON = fmt.Sprintf(
		`[{"filename":"report.gz","content_type":"application/gzip","content":%q}]`,
		base64.StdEncoding.EncodeToString(gz))

	if err := p.process(context.Background(), rec); err != nil {
		t.Fatalf("first process: %v", err)
	}
	if err := p.process(context.Background(), rec); err != nil {
		t.Fatalf("re-process: %v", err)
	}
	state.mu.Lock()
	total := len(state.rows)
	state.mu.Unlock()
	if total != 2 {
		t.Fatalf("two passes over one report stored %d rows, want 2", total)
	}

	// A gzip bomb decompressing past the cap is skipped: process still returns
	// nil (ingestion of the message is never failed), and the table is
	// untouched.
	bomb := gzipBytes(t, bytes.Repeat([]byte{0}, maxDecompressedReportBytes+1<<20))
	recBomb := dmarcTestInbound()
	recBomb.AttachmentsJSON = fmt.Sprintf(
		`[{"filename":"bomb.gz","content_type":"application/gzip","content":%q}]`,
		base64.StdEncoding.EncodeToString(bomb))
	if err := p.process(context.Background(), recBomb); err != nil {
		t.Fatalf("process with bomb attachment must not fail: %v", err)
	}
	state.mu.Lock()
	total = len(state.rows)
	state.mu.Unlock()
	if total != 2 {
		t.Fatalf("bomb attachment stored rows: %d, want 2", total)
	}
}
