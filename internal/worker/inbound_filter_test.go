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

package worker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/goposta/posta/internal/services/spamfilter"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OB-2 W5 tests (ADR-006): the ingest-worker filter step — malware scan
// outcomes + fail modes, scoring → disposition, the external hook escalation
// and its fail-open contract, and the events each disposition writes. The
// database is the fake database/sql driver (wf fake); clamd is a real
// loopback listener speaking INSTREAM.

// ---------- fake driver ----------

type wfState struct {
	mu    sync.Mutex
	seq   int64
	table map[string][]map[string]driver.Value
}

func newWfState() *wfState { return &wfState{seq: 100, table: map[string][]map[string]driver.Value{}} }

func (s *wfState) rows(t *testing.T, table string) []map[string]driver.Value {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, 0, len(s.table[table]))
	out = append(out, s.table[table]...)
	return out
}

type wfConnector struct{ state *wfState }

func (c wfConnector) Connect(context.Context) (driver.Conn, error) { return &wfConn{s: c.state}, nil }
func (c wfConnector) Driver() driver.Driver                        { return wfDrv{} }

type wfDrv struct{}

func (wfDrv) Open(string) (driver.Conn, error) { return nil, fmt.Errorf("use sql.OpenDB") }

type wfConn struct{ s *wfState }

func (c *wfConn) Prepare(string) (driver.Stmt, error) { return nil, fmt.Errorf("no prepare") }
func (c *wfConn) Close() error                        { return nil }
func (c *wfConn) Begin() (driver.Tx, error)           { return wfTx{}, nil }

type wfTx struct{}

func (wfTx) Commit() error   { return nil }
func (wfTx) Rollback() error { return nil }

func (c *wfConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := wfNormalize(query)
	if strings.Contains(lower, `update "inbound_emails"`) {
		return c.execUpdate(lower, args)
	}
	if strings.Contains(lower, "insert into") {
		rows, err := c.QueryContext(ctx, query, args)
		if err != nil {
			return nil, err
		}
		n := int64(0)
		for {
			if err := rows.Next(make([]driver.Value, len(rows.Columns()))); err != nil {
				break
			}
			n++
		}
		_ = rows.Close()
		return driver.RowsAffected(n), nil
	}
	return nil, fmt.Errorf("wfState: unsupported exec: %s", query)
}

func (c *wfConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := wfNormalize(query)
	switch {
	case strings.Contains(lower, "insert into"):
		return c.insert(query, args)
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "inbound_auth_results"`):
		return c.selectAuthResults(lower, args)
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "inbound_emails"`):
		return c.selectWhere(lower, args, "inbound_emails")
	default:
		return nil, fmt.Errorf("wfState: cannot route query: %s", query)
	}
}

func wfNormalize(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

func (c *wfConn) insert(query string, args []driver.NamedValue) (driver.Rows, error) {
	table, cols, tuples, ret, err := wfParseInsert(query, args)
	if err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]driver.Value
	for _, vals := range tuples {
		row := map[string]driver.Value{}
		for j, col := range cols {
			row[col] = vals[j]
		}
		s.seq++
		row["id"] = s.seq
		for _, col := range ret {
			if _, ok := row[col]; !ok {
				if col == "id" {
					row[col] = s.seq
				} else {
					row[col] = ""
				}
			}
		}
		s.table[table] = append(s.table[table], row)
		out = append(out, wfProject(row, ret))
	}
	return &wfRows{cols: ret, rows: out}, nil
}

// wfParseInsert extracts table, columns, bound values and RETURNING list.
func wfParseInsert(query string, args []driver.NamedValue) (string, []string, [][]driver.Value, []string, error) {
	lower := strings.ToLower(query)
	ni := strings.Index(lower, "insert into")
	if ni < 0 {
		return "", nil, nil, nil, fmt.Errorf("not an insert: %s", query)
	}
	rest := strings.TrimSpace(query[ni+len("insert into"):])
	end := strings.Index(rest, "(")
	if end < 0 {
		return "", nil, nil, nil, fmt.Errorf("insert without columns: %s", query)
	}
	table := strings.Trim(strings.TrimSpace(rest[:end]), `"`)

	open := strings.LastIndex(query[:strings.LastIndex(lower, " values (")], "(")
	close := strings.LastIndex(query[:strings.LastIndex(lower, " values (")], ")")
	if open < 0 || close < open {
		return "", nil, nil, nil, fmt.Errorf("insert without a column list: %s", query)
	}
	var cols []string
	for _, p := range strings.Split(query[open+1:close], ",") {
		cols = append(cols, strings.Trim(strings.TrimSpace(p), `"`))
	}

	vi := strings.Index(lower, "values (")
	valText := query[vi+len("values "):]
	if i := strings.Index(strings.ToLower(valText), " returning"); i >= 0 {
		valText = valText[:i]
	}
	valText = strings.TrimSpace(valText)
	valText = strings.TrimSuffix(strings.TrimPrefix(valText, "("), ")")

	var ret []string
	if i := strings.Index(strings.ToLower(query), " returning"); i >= 0 {
		for _, p := range strings.Split(query[i+len(" returning"):], ",") {
			p = strings.Trim(strings.TrimSpace(p), `"`)
			if p != "" && !strings.HasPrefix(p, ")") {
				ret = append(ret, p)
			}
		}
	}

	var tuples [][]driver.Value
	for _, tuple := range strings.Split(valText, "),(") {
		parts := strings.Split(tuple, ",")
		if len(parts) != len(cols) {
			return table, nil, nil, nil, fmt.Errorf("%d columns but %d values", len(cols), len(parts))
		}
		vals := make([]driver.Value, len(parts))
		for i, p := range parts {
			p = strings.TrimSpace(p)
			if !strings.HasPrefix(p, "$") {
				return table, nil, nil, nil, fmt.Errorf("non-bindvar value %q", p)
			}
			n, _ := strconv.Atoi(p[1:])
			for _, a := range args {
				if a.Ordinal == n {
					vals[i] = a.Value
					break
				}
			}
		}
		tuples = append(tuples, vals)
	}
	return table, cols, tuples, ret, nil
}

func wfProject(row map[string]driver.Value, cols []string) []driver.Value {
	out := make([]driver.Value, len(cols))
	for i, c := range cols {
		out[i] = row[c]
	}
	return out
}

func (c *wfConn) selectWhere(lower string, args []driver.NamedValue, table string) (driver.Rows, error) {
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = lower[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit ", " returning "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	var cols []string
	var rows [][]driver.Value
	for _, row := range s.table[table] {
		ok := true
		if strings.TrimSpace(where) != "" {
			for _, term := range strings.Split(where, " and ") {
				if !wfMatchTerm(row, strings.TrimSpace(term), args) {
					ok = false
					break
				}
			}
		}
		if !ok {
			continue
		}
		if len(cols) == 0 {
			for col := range row {
				cols = append(cols, col)
			}
		}
		rows = append(rows, wfProject(row, cols))
	}
	return &wfRows{cols: cols, rows: rows}, nil
}

func (c *wfConn) selectAuthResults(lower string, args []driver.NamedValue) (driver.Rows, error) {
	return c.selectWhere(lower, args, "inbound_auth_results")
}

func (c *wfConn) execUpdate(lower string, args []driver.NamedValue) (driver.Result, error) {
	si := strings.Index(lower, " set ")
	wi := strings.Index(lower, " where ")
	if si < 0 || wi < 0 {
		return nil, fmt.Errorf("wfState: unsupported update shape: %s", lower)
	}
	setPart := lower[si+len(" set ") : wi]
	wherePart := lower[wi+len(" where "):]
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	affected := int64(0)
	for _, row := range s.table["inbound_emails"] {
		ok := true
		for _, term := range strings.Split(wherePart, " and ") {
			if !wfMatchTerm(row, strings.TrimSpace(term), args) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, pair := range strings.Split(setPart, ",") {
			kv := strings.Split(pair, "=")
			if len(kv) != 2 {
				continue
			}
			col := strings.Trim(strings.TrimSpace(kv[0]), `"`)
			ord, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(kv[1]), "$"))
			if err != nil {
				return nil, err
			}
			for _, a := range args {
				if a.Ordinal == ord {
					row[col] = a.Value
				}
			}
		}
		affected++
	}
	return driver.RowsAffected(affected), nil
}

func wfMatchTerm(row map[string]driver.Value, term string, args []driver.NamedValue) bool {
	i := strings.Index(term, "=")
	if i < 0 {
		return false
	}
	left := strings.TrimSpace(term[:i])
	right := strings.TrimSpace(term[i+1:])
	if strings.HasPrefix(left, "$") {
		left, right = right, left
	}
	col := strings.ReplaceAll(strings.TrimSpace(left), `"`, "")
	if j := strings.LastIndex(col, "."); j >= 0 {
		col = col[j+1:]
	}
	val := strings.TrimSpace(right)
	var rv driver.Value
	if strings.HasPrefix(val, "$") {
		n, _ := strconv.Atoi(strings.TrimPrefix(val, "$"))
		for _, a := range args {
			if a.Ordinal == n {
				rv = a.Value
			}
		}
	} else {
		rv = strings.Trim(val, "'")
	}
	got, ok := row[col]
	if !ok {
		return false
	}
	return fmt.Sprint(got) == fmt.Sprint(rv)
}

type wfRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *wfRows) Columns() []string { return r.cols }
func (r *wfRows) Close() error      { return nil }
func (r *wfRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func wfGorm(t *testing.T, state *wfState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(wfConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open worker filter fake gorm: %v", err)
	}
	return gdb
}

// ---------- stubs ----------

type stubFilter struct {
	score   float64
	reasons []string
}

func (s stubFilter) Score(_ *spamfilter.Message) (float64, []string) { return s.score, s.reasons }
func (s stubFilter) Disposition(score float64) spamfilter.Disposition {
	switch {
	case score >= 10:
		return spamfilter.DispositionReject
	case score >= 5:
		return spamfilter.DispositionQuarantine
	default:
		return spamfilter.DispositionDeliver
	}
}

type stubConsultant struct {
	reply    *webhook.FilterReply
	err      error
	lastBody []byte
	gotEvent string
}

func (c *stubConsultant) ConsultFilter(_ context.Context, _ uint, _ *uint, event string, body []byte, _ string, _ time.Duration) (*webhook.FilterReply, error) {
	c.lastBody = body
	c.gotEvent = event
	return c.reply, c.err
}

// fakeClamd answers every INSTREAM with the configured response.
type fakeClamd struct {
	listener net.Listener
	response string
	mu       sync.Mutex
	scans    int
}

func startFakeClamd(t *testing.T, response string) *fakeClamd {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeClamd{listener: l, response: response}
	go f.serve()
	t.Cleanup(func() { _ = l.Close() })
	return f
}

func (f *fakeClamd) host() string { return f.listener.Addr().(*net.TCPAddr).IP.String() }
func (f *fakeClamd) port() int    { return f.listener.Addr().(*net.TCPAddr).Port }

func (f *fakeClamd) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			// Read command up to NUL.
			var cmd []byte
			one := make([]byte, 1)
			for {
				if _, err := io.ReadFull(c, one); err != nil {
					return
				}
				if one[0] == 0 {
					break
				}
				cmd = append(cmd, one[0])
			}
			if !strings.Contains(string(cmd), "INSTREAM") {
				_, _ = c.Write([]byte("UNKNOWN\x00"))
				return
			}
			var lenBuf [4]byte
			for {
				if _, err := io.ReadFull(c, lenBuf[:]); err != nil {
					return
				}
				n := int(lenBuf[0])<<24 | int(lenBuf[1])<<16 | int(lenBuf[2])<<8 | int(lenBuf[3])
				if n == 0 {
					break
				}
				if _, err := io.CopyN(io.Discard, c, int64(n)); err != nil {
					return
				}
			}
			f.mu.Lock()
			f.scans++
			f.mu.Unlock()
			_, _ = c.Write([]byte(f.response + "\x00"))
		}(conn)
	}
}

func (f *fakeClamd) scanCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scans
}

// ---------- harness ----------

type wfHarness struct {
	state  *wfState
	db     *gorm.DB
	svc    *inbound.Service
	repo   *repositories.InboundEmailRepository
	bus    *eventbus.EventBus
	events <-chan *models.Event
	done   func()
}

func newWfHarness(t *testing.T) *wfHarness {
	t.Helper()
	state := newWfState()
	db := wfGorm(t, state)
	repo := repositories.NewInboundEmailRepository(db)
	svc := inbound.NewService(repo, nil, nil, inbound.Config{})
	bus := eventbus.New(repositories.NewEventRepository(db))
	svc.SetEventBus(bus)
	ch, done := bus.Subscribe()
	return &wfHarness{state: state, db: db, svc: svc, repo: repo, bus: bus, events: ch, done: done}
}

func (h *wfHarness) newRecord(t *testing.T, mutators ...func(*models.InboundEmail)) *models.InboundEmail {
	t.Helper()
	rec := &models.InboundEmail{
		ID:          51,
		Sender:      "outside@example.net",
		Recipients:  []string{"inbox@example.test"},
		Status:      models.InboundStatusReceived,
		ReceivedAt:  time.Now().UTC(),
		HeadersJSON: `{"Message-Id":"<x@y>","Date":"` + time.Now().UTC().Format(time.RFC1123Z) + `"}`,
	}
	for _, m := range mutators {
		m(rec)
	}
	if err := h.repo.Create(rec); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	return rec
}

func (h *wfHarness) drain(t *testing.T) []*models.Event {
	t.Helper()
	var out []*models.Event
	for {
		select {
		case ev := <-h.events:
			out = append(out, ev)
		default:
			h.done()
			return out
		}
	}
}

func build(h *wfHarness, f spamfilter.SpamFilter, scanner *spamfilter.ClamdClient, hook FilterConsultant, opts InboundFilterOptions) *InboundFilter {
	return NewInboundFilter(h.repo, h.svc, nil, f, scanner, hook, opts)
}

// ---------- Run: gating ----------

func TestInboundFilterNilAndDisabledProceed(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t)

	var nilFilter *InboundFilter
	if !nilFilter.Run(context.Background(), rec) {
		t.Fatal("nil filter must proceed")
	}
	disabled := build(h, stubFilter{score: 100}, nil, nil, InboundFilterOptions{Enabled: false})
	if !disabled.Run(context.Background(), rec) {
		t.Fatal("disabled filter must proceed")
	}
	if rec.Status != models.InboundStatusReceived || rec.SpamScore != nil {
		t.Fatalf("disabled filter must not touch the record: %+v", rec.Status)
	}
}

// ---------- Run: scoring → disposition ----------

func TestInboundFilterRunDeliverStampsScore(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t)

	f := build(h, stubFilter{score: 1.5, reasons: []string{"missing_message_id"}}, nil, nil, InboundFilterOptions{Enabled: true})
	if !f.Run(context.Background(), rec) {
		t.Fatal("low score must proceed")
	}
	if rec.SpamScore == nil || *rec.SpamScore != 1.5 {
		t.Fatalf("score stamp = %v, want 1.5", rec.SpamScore)
	}
	if rec.SpamReasons != "missing_message_id" {
		t.Fatalf("reasons = %q", rec.SpamReasons)
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %v, want received", rec.Status)
	}
	if events := h.drain(t); len(events) != 0 {
		t.Fatalf("deliver wrote %d events, want 0", len(events))
	}
}

func TestInboundFilterRunGTUBEQuarantinedByThreshold(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t, func(r *models.InboundEmail) {
		r.TextBody = "hi\n\n" + spamfilter.GTUBEString + "\n"
	})

	// Real baseline scorer, real thresholds: GTUBE = 1000 → reject.
	scorer := spamfilter.NewRulesScorer(spamfilter.StaticThresholds{Quarantine: 5, Reject: 10}, nil, nil)
	f := build(h, scorer, nil, nil, InboundFilterOptions{Enabled: true})
	if f.Run(context.Background(), rec) {
		t.Fatal("GTUBE message must not proceed")
	}
	if rec.Status != models.InboundStatusRejected {
		t.Fatalf("status = %v, want rejected", rec.Status)
	}
	if rec.SpamScore == nil || *rec.SpamScore != 1000 {
		t.Fatalf("score = %v, want 1000", rec.SpamScore)
	}
	events := h.drain(t)
	found := false
	for _, ev := range events {
		if ev.Type == "inbound.spam_flagged" && strings.Contains(ev.Metadata, "gtube_test_pattern") {
			found = true
		}
	}
	if !found {
		t.Fatalf("spam_flagged event with gtube reason missing among %d events", len(events))
	}
}

// ---------- Run: malware scan ----------

func withInlineAttachment(content []byte) func(*models.InboundEmail) {
	return func(r *models.InboundEmail) {
		meta := models.InboundAttachmentMeta{
			Filename:    "invoice.pdf",
			ContentType: "application/pdf",
			Size:        int64(len(content)),
			Content:     base64.StdEncoding.EncodeToString(content),
		}
		data, _ := json.Marshal([]models.InboundAttachmentMeta{meta})
		r.AttachmentsJSON = string(data)
	}
}

func TestInboundFilterMalwarePositiveQuarantines(t *testing.T) {
	h := newWfHarness(t)
	fake := startFakeClamd(t, "stream: Eicar-Test-Signature FOUND")
	rec := h.newRecord(t, withInlineAttachment([]byte("infected payload")))

	scanner := spamfilter.NewClamdClient(spamfilter.ClamdConfig{
		Host: fake.host(), Port: fake.port(), Timeout: 3 * time.Second, FailMode: spamfilter.ClamFailOpen,
	})
	f := build(h, stubFilter{score: 0}, scanner, nil, InboundFilterOptions{Enabled: true})
	if f.Run(context.Background(), rec) {
		t.Fatal("malware positive must not proceed")
	}
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %v, want quarantined", rec.Status)
	}
	if rec.ThreatSignature != "Eicar-Test-Signature" {
		t.Fatalf("signature = %q", rec.ThreatSignature)
	}
	if fake.scanCount() != 1 {
		t.Fatalf("scans = %d, want 1", fake.scanCount())
	}
	events := h.drain(t)
	detected, held := false, false
	for _, ev := range events {
		switch ev.Type {
		case "inbound.malware_detected":
			detected = strings.Contains(ev.Metadata, "Eicar-Test-Signature")
		case "inbound.quarantined":
			held = true
		}
	}
	if !detected || !held {
		t.Fatalf("malware_detected=%v quarantined=%v among %d events", detected, held, len(events))
	}
}

func TestInboundFilterCleanAttachmentProceeds(t *testing.T) {
	h := newWfHarness(t)
	fake := startFakeClamd(t, "stream: OK")
	rec := h.newRecord(t, withInlineAttachment([]byte("clean body")))

	scanner := spamfilter.NewClamdClient(spamfilter.ClamdConfig{
		Host: fake.host(), Port: fake.port(), Timeout: 3 * time.Second,
	})
	f := build(h, stubFilter{score: 0}, scanner, nil, InboundFilterOptions{Enabled: true})
	if !f.Run(context.Background(), rec) {
		t.Fatal("clean scan must proceed")
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %v, want received", rec.Status)
	}
	if fake.scanCount() != 1 {
		t.Fatalf("scans = %d, want 1", fake.scanCount())
	}
	h.drain(t)
}

func TestInboundFilterFailClosedQuarantinesOnOutage(t *testing.T) {
	h := newWfHarness(t)
	// Bind then close: guaranteed connection refusal.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	rec := h.newRecord(t, withInlineAttachment([]byte("unscannable")))
	scanner := spamfilter.NewClamdClient(spamfilter.ClamdConfig{
		Host: "127.0.0.1", Port: closedPort, Timeout: time.Second, FailMode: spamfilter.ClamFailClosed,
	})
	f := build(h, stubFilter{score: 0}, scanner, nil, InboundFilterOptions{Enabled: true})
	if f.Run(context.Background(), rec) {
		t.Fatal("fail-closed outage must quarantine")
	}
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %v, want quarantined", rec.Status)
	}
	if rec.SpamReasons != "clamd_unavailable" {
		t.Fatalf("reasons = %q, want clamd_unavailable", rec.SpamReasons)
	}
	h.drain(t)
}

func TestInboundFilterFailOpenProceedsOnOutage(t *testing.T) {
	h := newWfHarness(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	rec := h.newRecord(t, withInlineAttachment([]byte("unscannable")))
	scanner := spamfilter.NewClamdClient(spamfilter.ClamdConfig{
		Host: "127.0.0.1", Port: closedPort, Timeout: time.Second, FailMode: spamfilter.ClamFailOpen,
	})
	f := build(h, stubFilter{score: 0}, scanner, nil, InboundFilterOptions{Enabled: true})
	if !f.Run(context.Background(), rec) {
		t.Fatal("fail-open outage must proceed")
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %v, want received", rec.Status)
	}
	h.drain(t)
}

// ---------- Run: external hook ----------

func TestInboundFilterHookEscalatesDisposition(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t)
	consultant := &stubConsultant{reply: &webhook.FilterReply{Disposition: "quarantine"}}

	// Local deliver + external quarantine → quarantine.
	f := build(h, stubFilter{score: 0}, nil, consultant, InboundFilterOptions{
		Enabled:           true,
		ExternalHook:      true,
		ExternalHookEvent: "email.inbound.filter",
	})
	if f.Run(context.Background(), rec) {
		t.Fatal("external quarantine must stop delivery")
	}
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %v, want quarantined", rec.Status)
	}
	if !strings.Contains(rec.SpamReasons, "external_filter:quarantine") {
		t.Fatalf("reasons = %q, want the external marker", rec.SpamReasons)
	}
	if consultant.gotEvent != "email.inbound.filter" {
		t.Fatalf("consult event = %q", consultant.gotEvent)
	}
	var payload filterConsultPayload
	if err := json.Unmarshal(consultant.lastBody, &payload); err != nil {
		t.Fatalf("consult payload: %v", err)
	}
	if payload.InboundID != rec.UUID || payload.LocalScore != 0 {
		t.Fatalf("payload = %+v", payload)
	}
	if strings.Contains(string(consultant.lastBody), "text_body") {
		t.Fatal("consult payload must not carry raw content")
	}
	h.drain(t)
}

func TestInboundFilterHookRejectBeatsLocalQuarantine(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t)
	consultant := &stubConsultant{reply: &webhook.FilterReply{Disposition: "reject"}}

	f := build(h, stubFilter{score: 7}, nil, consultant, InboundFilterOptions{
		Enabled:      true,
		ExternalHook: true,
	})
	if f.Run(context.Background(), rec) {
		t.Fatal("external reject must stop delivery")
	}
	if rec.Status != models.InboundStatusRejected {
		t.Fatalf("status = %v, want rejected", rec.Status)
	}
	h.drain(t)
}

func TestInboundFilterHookCannotRescueLocalFlag(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t)
	consultant := &stubConsultant{reply: &webhook.FilterReply{Disposition: "deliver"}}

	// Local quarantine + external deliver → still quarantined.
	f := build(h, stubFilter{score: 7}, nil, consultant, InboundFilterOptions{
		Enabled:      true,
		ExternalHook: true,
	})
	if f.Run(context.Background(), rec) {
		t.Fatal("local quarantine must stand")
	}
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %v, want quarantined", rec.Status)
	}
	h.drain(t)
}

func TestInboundFilterHookFailOpen(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t)
	consultant := &stubConsultant{err: fmt.Errorf("endpoint down")}

	f := build(h, stubFilter{score: 0}, nil, consultant, InboundFilterOptions{
		Enabled:      true,
		ExternalHook: true,
	})
	if !f.Run(context.Background(), rec) {
		t.Fatal("hook outage must fail open and proceed")
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %v, want received", rec.Status)
	}
	h.drain(t)
}

func TestInboundFilterHookDisabled(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t)
	consultant := &stubConsultant{}

	f := build(h, stubFilter{score: 0}, nil, consultant, InboundFilterOptions{Enabled: true})
	if !f.Run(context.Background(), rec) {
		t.Fatal("filter must proceed")
	}
	if consultant.lastBody != nil {
		t.Fatal("hook must not be consulted when disabled")
	}
	h.drain(t)
}

// ---------- helpers ----------

func TestSenderDomain(t *testing.T) {
	// senderDomain is the shared handler.go helper: it lowercases the domain
	// after the LAST @ but does not trim — the ingest pipeline supplies
	// already-trimmed addresses (service.go normalizes the envelope).
	cases := map[string]string{
		"User@Example.COM": "example.com",
		"a@b.c":            "b.c",
		"no-at-sign":       "",
		"trailing@":        "",
		"":                 "",
	}
	for in, want := range cases {
		if got := senderDomain(in); got != want {
			t.Fatalf("senderDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
