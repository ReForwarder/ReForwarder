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

package inbound

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/spamfilter"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OB-2 W5 tests (ADR-006): filter disposition application on the durable
// record + the event taxonomy, and the auth-verdict wait. Runs against the
// fake database/sql driver (route_action_test.go pattern); flConn is raConn
// plus inbound_auth_results select routing.

// ---------- fake driver: raConn + inbound_auth_results ----------

type flConnector struct{ state *raState }

func (c flConnector) Connect(context.Context) (driver.Conn, error) {
	return &flConn{raConn: &raConn{s: c.state}}, nil
}
func (c flConnector) Driver() driver.Driver { return flDrv{} }

type flDrv struct{}

func (flDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type flConn struct{ *raConn }

func (c *flConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := raNormalize(query)
	if strings.Contains(lower, "select") && strings.Contains(lower, `from "inbound_auth_results"`) {
		return c.selectAuthResults(lower, args)
	}
	return c.raConn.QueryContext(ctx, query, args)
}

// selectAuthResults mirrors the ra select: conjunctions of `col = $n` terms
// via raMatchTerm; ORDER BY/LIMIT ignored (First() takes the first match).
func (c *flConn) selectAuthResults(lower string, args []driver.NamedValue) (driver.Rows, error) {
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
	matched := make([]map[string]driver.Value, 0)
	for _, row := range s.table["inbound_auth_results"] {
		ok := true
		if strings.TrimSpace(where) != "" {
			for _, term := range strings.Split(where, " and ") {
				if !raMatchTerm(row, strings.TrimSpace(term), args) {
					ok = false
					break
				}
			}
		}
		if ok {
			matched = append(matched, row)
		}
	}
	cols := make([]string, 0)
	rows := make([][]driver.Value, 0)
	if len(matched) > 0 {
		for col := range matched[0] {
			cols = append(cols, col)
		}
		for _, row := range matched {
			rows = append(rows, raProject(row, cols))
		}
	}
	return &raRows{cols: cols, rows: rows}, nil
}

func flGorm(t *testing.T, state *raState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(flConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open filter fake gorm: %v", err)
	}
	return gdb
}

// ---------- harness ----------

// recordingQuarantineStore captures the QuarantineStore seam invocation.
type recordingQuarantineStore struct {
	mu        sync.Mutex
	calls     int
	route     *models.InboundRoute
	reason    string
	inboundID uint
}

func (r *recordingQuarantineStore) Quarantine(_ context.Context, rec *models.InboundEmail, route *models.InboundRoute, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.route = route
	r.reason = reason
	r.inboundID = rec.ID
	return nil
}

type filterHarness struct {
	svc    *Service
	bus    *eventbus.EventBus
	events <-chan *models.Event
	done   func()
	state  *raState
	store  *recordingQuarantineStore
}

func newFilterHarness(t *testing.T) *filterHarness {
	t.Helper()
	state := newRaState()
	db := flGorm(t, state)
	repo := repositories.NewInboundEmailRepository(db)
	svc := NewService(repo, repositories.NewDomainRepository(db), nil, Config{})
	bus := eventbus.New(repositories.NewEventRepository(db))
	svc.SetEventBus(bus)
	ch, done := bus.Subscribe()
	store := &recordingQuarantineStore{}
	svc.SetQuarantineStore(store)
	return &filterHarness{svc: svc, bus: bus, events: ch, done: done, state: state, store: store}
}

func (h *filterHarness) newRecord(t *testing.T, id uint) *models.InboundEmail {
	t.Helper()
	rec := &models.InboundEmail{
		ID:         id,
		Sender:     "sender@example.net",
		Recipients: []string{"inbox@example.test"},
		Subject:    "hello",
		Status:     models.InboundStatusReceived,
		ReceivedAt: time.Now().UTC(),
	}
	if err := h.svc.repo.Create(rec); err != nil {
		t.Fatalf("seed inbound record: %v", err)
	}
	return rec
}

func (h *filterHarness) drain(t *testing.T) []*models.Event {
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

func findEvent(t *testing.T, events []*models.Event, typ string) *models.Event {
	t.Helper()
	for _, ev := range events {
		if ev.Type == typ {
			return ev
		}
	}
	t.Fatalf("event %q not found among %d events", typ, len(events))
	return nil
}

func eventAbsent(t *testing.T, events []*models.Event, typ string) {
	t.Helper()
	for _, ev := range events {
		if ev.Type == typ {
			t.Fatalf("event %q unexpectedly present", typ)
		}
	}
}

// ---------- disposition tests ----------

func TestApplyFilterDispositionQuarantineWritesEventsAndRow(t *testing.T) {
	h := newFilterHarness(t)
	rec := h.newRecord(t, 11)

	err := h.svc.ApplyFilterDisposition(context.Background(), rec, FilterDisposition{
		Disposition: spamfilter.DispositionQuarantine,
		Score:       7.5,
		Reasons:     []string{"spf_fail", "dmarc_fail"},
	})
	if err != nil {
		t.Fatalf("ApplyFilterDisposition: %v", err)
	}

	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %v, want quarantined", rec.Status)
	}
	if rec.SpamScore == nil || *rec.SpamScore != 7.5 {
		t.Fatalf("spam score = %v, want 7.5", rec.SpamScore)
	}
	if rec.SpamReasons != "spf_fail,dmarc_fail" {
		t.Fatalf("spam reasons = %q", rec.SpamReasons)
	}
	if rec.ThreatSignature != "" {
		t.Fatalf("threat signature = %q, want empty", rec.ThreatSignature)
	}

	events := h.drain(t)
	flagged := findEvent(t, events, "inbound.spam_flagged")
	if flagged.Metadata == "" || !strings.Contains(flagged.Metadata, "spf_fail") {
		t.Fatalf("spam_flagged metadata = %q, want reasons", flagged.Metadata)
	}
	findEvent(t, events, "inbound.quarantined")
	eventAbsent(t, events, "inbound.malware_detected")

	if h.store.calls != 1 {
		t.Fatalf("quarantine store calls = %d, want 1", h.store.calls)
	}
	if h.store.route != nil {
		t.Fatal("filter-driven hold must pass a nil route (no routing rule drove it)")
	}
	if h.store.inboundID != rec.ID || !strings.Contains(h.store.reason, "7.5") {
		t.Fatalf("store reason = %q inboundID = %d", h.store.reason, h.store.inboundID)
	}
}

func TestApplyFilterDispositionRejectMarksRow(t *testing.T) {
	h := newFilterHarness(t)
	rec := h.newRecord(t, 12)

	err := h.svc.ApplyFilterDisposition(context.Background(), rec, FilterDisposition{
		Disposition: spamfilter.DispositionReject,
		Score:       12,
		Reasons:     []string{"gtube_test_pattern"},
	})
	if err != nil {
		t.Fatalf("ApplyFilterDisposition: %v", err)
	}
	if rec.Status != models.InboundStatusRejected {
		t.Fatalf("status = %v, want rejected", rec.Status)
	}
	if !strings.Contains(rec.ErrorMessage, "spam filter") {
		t.Fatalf("error message = %q, want the filter reason", rec.ErrorMessage)
	}

	events := h.drain(t)
	findEvent(t, events, "inbound.spam_flagged")
	eventAbsent(t, events, "inbound.quarantined")
	eventAbsent(t, events, "inbound.malware_detected")
	if h.store.calls != 0 {
		t.Fatalf("reject must not create a quarantine hold; calls = %d", h.store.calls)
	}
}

func TestApplyFilterDispositionMalwareRecordsSignature(t *testing.T) {
	h := newFilterHarness(t)
	rec := h.newRecord(t, 13)

	err := h.svc.ApplyFilterDisposition(context.Background(), rec, FilterDisposition{
		Disposition:     spamfilter.DispositionQuarantine,
		Score:           100,
		Reasons:         []string{"malware_detected"},
		ThreatSignature: "Eicar-Test-Signature",
		Malware:         true,
	})
	if err != nil {
		t.Fatalf("ApplyFilterDisposition: %v", err)
	}
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %v, want quarantined", rec.Status)
	}
	if rec.ThreatSignature != "Eicar-Test-Signature" {
		t.Fatalf("threat signature = %q, want the clamd signature", rec.ThreatSignature)
	}

	events := h.drain(t)
	detected := findEvent(t, events, "inbound.malware_detected")
	if !strings.Contains(detected.Metadata, "Eicar-Test-Signature") {
		t.Fatalf("malware_detected metadata = %q, want the signature", detected.Metadata)
	}
	findEvent(t, events, "inbound.quarantined")
	eventAbsent(t, events, "inbound.spam_flagged")
}

func TestApplyFilterDispositionDeliverStampsScoreOnly(t *testing.T) {
	h := newFilterHarness(t)
	rec := h.newRecord(t, 14)

	err := h.svc.ApplyFilterDisposition(context.Background(), rec, FilterDisposition{
		Disposition: spamfilter.DispositionDeliver,
		Score:       1.5,
		Reasons:     []string{"missing_message_id"},
	})
	if err != nil {
		t.Fatalf("ApplyFilterDisposition: %v", err)
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %v, want unchanged received", rec.Status)
	}
	if rec.SpamScore == nil || *rec.SpamScore != 1.5 {
		t.Fatalf("spam score = %v, want 1.5", rec.SpamScore)
	}
	events := h.drain(t)
	if len(events) != 0 {
		t.Fatalf("deliver must not write filter events, got %d", len(events))
	}
	if h.store.calls != 0 {
		t.Fatalf("deliver must not create a hold; calls = %d", h.store.calls)
	}
}

func TestApplyFilterDispositionInvalid(t *testing.T) {
	h := newFilterHarness(t)
	rec := h.newRecord(t, 15)
	if err := h.svc.ApplyFilterDisposition(context.Background(), rec, FilterDisposition{
		Disposition: spamfilter.Disposition("bogus"),
	}); err == nil {
		t.Fatal("unknown disposition must error")
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %v, want unchanged", rec.Status)
	}
}

// ---------- auth verdict wait ----------

func TestWaitAuthResultsReturnsRow(t *testing.T) {
	h := newFilterHarness(t)
	h.svc.EnableVerification(nil, "") // no-op guard: nil db must not crash
	if h.svc.verifier != nil {
		t.Fatal("EnableVerification(nil) must leave the verifier unwired")
	}
	if got := h.svc.WaitAuthResults(context.Background(), 99, time.Second); got != nil {
		t.Fatalf("service without verifier returned %v, want nil", got)
	}

	// Wire a verifier over the fake and seed a verdict row.
	db := flGorm(t, h.state)
	h.svc.EnableVerification(db, "authserv-test")
	h.state.mu.Lock()
	h.state.table["inbound_auth_results"] = append(h.state.table["inbound_auth_results"], map[string]driver.Value{
		"id":                int64(1),
		"inbound_id":        int64(21),
		"spf_result":        "fail",
		"dkim_result":       "pass",
		"dmarc_result":      "fail",
		"dmarc_disposition": "quarantine",
		"client_ip":         "192.0.2.9",
	})
	h.state.mu.Unlock()

	row := h.svc.WaitAuthResults(context.Background(), 21, time.Second)
	if row == nil {
		t.Fatal("WaitAuthResults returned nil for a seeded verdict")
	}
	if row.SPFResult != "fail" || row.DMARCDisposition != "quarantine" || row.ClientIP != "192.0.2.9" {
		t.Fatalf("row = %+v", row)
	}

	// A missing verdict must return nil after the wait, not block forever.
	start := time.Now()
	if got := h.svc.WaitAuthResults(context.Background(), 999, 300*time.Millisecond); got != nil {
		t.Fatalf("missing verdict returned %+v, want nil", got)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("missing-verdict wait exceeded its bound")
	}
}

func TestAuthVerdictFromRow(t *testing.T) {
	if v := AuthVerdictFromRow(nil); v != (spamfilter.AuthVerdict{}) {
		t.Fatalf("nil row mapped to %+v, want zero verdict", v)
	}
	v := AuthVerdictFromRow(&models.InboundAuthResult{
		SPFResult: "softfail", DKIMResult: "none", DMARCResult: "fail", DMARCDisposition: "reject",
	})
	if v.SPF != "softfail" || v.DKIM != "none" || v.DMARC != "fail" || v.DMARCDisposition != "reject" {
		t.Fatalf("mapping = %+v", v)
	}
}
