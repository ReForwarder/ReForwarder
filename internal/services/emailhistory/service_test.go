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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package emailhistory

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
	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ---------- test doubles ----------

// fakeClock is the deterministic clock injected through WithNow.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// rawExec is one captured raw (Exec) statement with its bound variables.
type rawExec struct {
	sql  string
	vars []any
}

// capture collects the rows Record creates and the raw statements it issues,
// via gorm callbacks on a DryRun session — no SQL is ever executed.
type capture struct {
	mu     sync.Mutex
	events []models.EmailEvent
	raw    []rawExec
}

func (c *capture) addEvent(ev models.EmailEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *capture) addRaw(sql string, vars []any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raw = append(c.raw, rawExec{sql: sql, vars: append([]any(nil), vars...)})
}

func (c *capture) eventCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *capture) rawCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.raw)
}

func (c *capture) lastEvent() models.EmailEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return models.EmailEvent{}
	}
	return c.events[len(c.events)-1]
}

func (c *capture) allRaw() []rawExec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]rawExec(nil), c.raw...)
}

// nopConnector yields connections that refuse every operation; with DryRun
// gorm never touches them, it only builds statements.
type nopConnector struct{}

func (nopConnector) Connect(context.Context) (driver.Conn, error) { return nopConn{}, nil }
func (nopConnector) Driver() driver.Driver                        { return nopDriver{} }

type nopDriver struct{}

func (nopDriver) Open(string) (driver.Conn, error) { return nopConn{}, nil }

type nopConn struct{}

func (nopConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("nop conn") }
func (nopConn) Close() error                        { return nil }
func (nopConn) Begin() (driver.Tx, error)           { return nil, errors.New("nop conn") }

// failingConnector yields connections that error on every query — the
// "database is down" case the recorder must survive.
type failingConnector struct{}

func (failingConnector) Connect(context.Context) (driver.Conn, error) { return failingConn{}, nil }
func (failingConnector) Driver() driver.Driver                        { return nopDriver{} }

type failingConn struct{ nopConn }

func (failingConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("emailhistory test: database is down")
}

func (failingConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("emailhistory test: database is down")
}

// openTestGorm builds a postgres-dialect gorm over a database/sql DB backed by
// the given connector, mirroring the DryRun capture pattern used elsewhere in
// the codebase (see security detector_test).
func openTestGorm(t *testing.T, conn driver.Connector) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(conn)}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open test gorm: %v", err)
	}
	return gdb
}

// newCapturingRecorder returns a Recorder on a DryRun session whose Create and
// raw callbacks capture everything Record attempts, plus the capture itself.
func newCapturingRecorder(t *testing.T, clock *fakeClock) (*Recorder, *capture) {
	t.Helper()
	gdb := openTestGorm(t, nopConnector{})
	c := &capture{}
	if err := gdb.Callback().Create().Before("gorm:create").Register("emailhistory-test:capture-create", func(tx *gorm.DB) {
		if ev, ok := tx.Statement.Dest.(*models.EmailEvent); ok {
			c.addEvent(*ev)
		}
	}); err != nil {
		t.Fatalf("register create capture: %v", err)
	}
	if err := gdb.Callback().Raw().Before("gorm:raw").Register("emailhistory-test:capture-raw", func(tx *gorm.DB) {
		c.addRaw(tx.Statement.SQL.String(), tx.Statement.Vars)
	}); err != nil {
		t.Fatalf("register raw capture: %v", err)
	}

	rec := NewRecorder(gdb.Session(&gorm.Session{DryRun: true}))
	rec.WithNow(clock.Now)
	return rec, c
}

// ---------- Record: insert + write-once timestamp ----------

// TestRecordInsertsEventAndStampsWriteOnceTimestamp pins the core contract:
// one email_events row with the denormalized fields, and one guarded UPDATE of
// the matching timestamp column.
func TestRecordInsertsEventAndStampsWriteOnceTimestamp(t *testing.T) {
	clock := newFakeClock()
	rec, cap := newCapturingRecorder(t, clock)

	ws := uint(3)
	rec.Record(context.Background(), EventInput{
		EmailUUID:       "em-1",
		WorkspaceID:     &ws,
		Type:            models.EmailEventOpened,
		OccurredAt:      clock.Now(),
		Provider:        "postmark",
		ProviderEventID: "pev-42",
		Recipient:       "alice@example.com",
		IP:              "203.0.113.7",
		UserAgent:       "Mozilla/5.0",
		Metadata:        map[string]any{"source": "provider"},
	})

	if cap.eventCount() != 1 {
		t.Fatalf("event rows captured = %d, want 1", cap.eventCount())
	}
	ev := cap.lastEvent()
	if ev.EmailUUID != "em-1" || ev.Type != models.EmailEventOpened {
		t.Errorf("event = %s/%s, want em-1/opened", ev.EmailUUID, ev.Type)
	}
	if ev.WorkspaceID == nil || *ev.WorkspaceID != 3 {
		t.Errorf("event workspace_id = %v, want 3", ev.WorkspaceID)
	}
	if ev.Provider != "postmark" || ev.ProviderEventID != "pev-42" {
		t.Errorf("provider/event id = %q/%q, want postmark/pev-42", ev.Provider, ev.ProviderEventID)
	}
	if ev.Recipient != "alice@example.com" || ev.IP != "203.0.113.7" || ev.UserAgent != "Mozilla/5.0" {
		t.Errorf("denormalized fields lost: %+v", ev)
	}
	if !ev.OccurredAt.Equal(clock.Now()) {
		t.Errorf("occurred_at = %v, want %v", ev.OccurredAt, clock.Now())
	}
	if !ev.CreatedAt.Equal(clock.Now()) {
		t.Errorf("created_at = %v, want the injected clock time %v", ev.CreatedAt, clock.Now())
	}
	if ev.Metadata != `{"source":"provider"}` {
		t.Errorf("metadata = %q, want JSON of the map", ev.Metadata)
	}

	// The write-once timestamp: the exact guarded UPDATE, with the occurred-at
	// value and the uuid bound.
	if cap.rawCount() != 1 {
		t.Fatalf("raw statements captured = %d, want 1 (the timestamp update)", cap.rawCount())
	}
	stmt := cap.allRaw()[0]
	if !strings.Contains(stmt.sql, "UPDATE emails SET opened_at = ") ||
		!strings.Contains(stmt.sql, "WHERE uuid = ") ||
		!strings.Contains(stmt.sql, "AND opened_at IS NULL") {
		t.Errorf("timestamp update SQL = %q, want the write-once guarded form", stmt.sql)
	}
	if len(stmt.vars) != 2 {
		t.Fatalf("update vars = %v, want [occurred_at, uuid]", stmt.vars)
	}
	at, ok := stmt.vars[0].(time.Time)
	if !ok || !at.Equal(clock.Now()) {
		t.Errorf("update var[0] = %v, want occurred_at %v", stmt.vars[0], clock.Now())
	}
	if uuid, _ := stmt.vars[1].(string); uuid != "em-1" {
		t.Errorf("update var[1] = %v, want em-1", stmt.vars[1])
	}
}

// TestRecordDedupKeyIdenticalForIdenticalEvents pins the dedup contract: the
// unique (email_uuid, type, provider_event_id) key of a replayed event is
// identical to the first one, and the insert carries ON CONFLICT DO NOTHING —
// so the database, not the recorder, collapses replays. The conflict-aware
// return of EmailEventRepository.Create is pinned in the repositories tests.
func TestRecordDedupKeyIdenticalForIdenticalEvents(t *testing.T) {
	clock := newFakeClock()
	rec, cap := newCapturingRecorder(t, clock)

	in := EventInput{
		EmailUUID:       "em-2",
		Type:            models.EmailEventBounced,
		Provider:        "postmark",
		ProviderEventID: "bounce-1",
		Error:           "smtp 550",
	}
	rec.Record(context.Background(), in)
	rec.Record(context.Background(), in)

	if cap.eventCount() != 2 {
		t.Fatalf("event rows captured = %d, want 2 (both inserts attempted)", cap.eventCount())
	}
	first, second := cap.events[0], cap.events[1]
	if first.EmailUUID != second.EmailUUID || first.Type != second.Type || first.ProviderEventID != second.ProviderEventID {
		t.Errorf("identical inputs produced different dedup keys: (%s,%s,%s) vs (%s,%s,%s)",
			first.EmailUUID, first.Type, first.ProviderEventID,
			second.EmailUUID, second.Type, second.ProviderEventID)
	}

	in2 := in
	in2.ProviderEventID = "bounce-2"
	rec.Record(context.Background(), in2)
	third := cap.lastEvent()
	if third.ProviderEventID != "bounce-2" {
		t.Errorf("distinct provider id collapsed: %q, want bounce-2", third.ProviderEventID)
	}
}

// TestRecordInteractionDedupKeyForOpensAndClicks pins the derived dedup key of
// open/click events without a provider id: stable within a day for the same
// interaction signature, different across days and URLs.
func TestRecordInteractionDedupKeyForOpensAndClicks(t *testing.T) {
	clock := newFakeClock()
	rec, cap := newCapturingRecorder(t, clock)

	in := EventInput{
		EmailUUID: "em-3",
		Type:      models.EmailEventOpened,
		IP:        "198.51.100.9",
		UserAgent: "Thunderbird/1",
	}
	rec.Record(context.Background(), in)
	rec.Record(context.Background(), in)
	if got := cap.events[0].ProviderEventID; got == "" || got != cap.events[1].ProviderEventID {
		t.Fatalf("same-day same-signature opens must share the dedup key, got %q vs %q",
			cap.events[0].ProviderEventID, cap.events[1].ProviderEventID)
	}
	if want := interactionEventID(models.EmailEventOpened, "", "198.51.100.9", "Thunderbird/1", clock.Now()); cap.events[0].ProviderEventID != want {
		t.Errorf("derived key = %q, want interactionEventID %q", cap.events[0].ProviderEventID, want)
	}

	// A different day must produce a different key.
	clock.Advance(24 * time.Hour)
	rec.Record(context.Background(), in)
	if cap.lastEvent().ProviderEventID == cap.events[0].ProviderEventID {
		t.Error("next-day open must get its own dedup key")
	}

	// Clicks key on the destination URL.
	click := in
	click.Type = models.EmailEventClicked
	click.URL = "https://example.com/pricing"
	rec.Record(context.Background(), click)
	if got, want := cap.lastEvent().ProviderEventID,
		interactionEventID(models.EmailEventClicked, "https://example.com/pricing", "198.51.100.9", "Thunderbird/1", clock.Now()); got != want {
		t.Errorf("click key = %q, want %q", got, want)
	}
}

// TestRecordRepeatedEventsKeepWriteOnceGuard pins the write-once semantics:
// even when the timestamp is already set, the UPDATE the recorder issues still
// carries the IS NULL guard, so the database refuses the overwrite.
func TestRecordRepeatedEventsKeepWriteOnceGuard(t *testing.T) {
	clock := newFakeClock()
	rec, cap := newCapturingRecorder(t, clock)

	in := EventInput{EmailUUID: "em-4", Type: models.EmailEventClicked, URL: "https://example.com/a"}
	rec.Record(context.Background(), in)
	clock.Advance(time.Hour)
	rec.Record(context.Background(), in)

	raws := cap.allRaw()
	if len(raws) != 2 {
		t.Fatalf("raw statements = %d, want 2", len(raws))
	}
	for i, stmt := range raws {
		if !strings.Contains(stmt.sql, "clicked_at IS NULL") {
			t.Errorf("update #%d lost the write-once guard: %q", i+1, stmt.sql)
		}
		if !strings.Contains(stmt.sql, "SET clicked_at = ") {
			t.Errorf("update #%d targets the wrong column: %q", i+1, stmt.sql)
		}
	}
}

// TestRecordFailedHasNoTimestampColumn pins that failures write the timeline
// row but no timestamp update — "failed" has no write-once column.
func TestRecordFailedHasNoTimestampColumn(t *testing.T) {
	clock := newFakeClock()
	rec, cap := newCapturingRecorder(t, clock)

	rec.Record(context.Background(), EventInput{
		EmailUUID: "em-5",
		Type:      models.EmailEventFailed,
		Error:     "dial tcp: refused",
	})

	if cap.eventCount() != 1 || cap.lastEvent().Error != "dial tcp: refused" {
		t.Fatalf("failed event not recorded: %+v", cap.lastEvent())
	}
	if cap.rawCount() != 0 {
		t.Errorf("failed event must not touch timestamp columns, got %d raw statements", cap.rawCount())
	}
}

// TestRecordDropsBadInput pins the input gate: unknown event types and empty
// uuids write nothing at all.
func TestRecordDropsBadInput(t *testing.T) {
	clock := newFakeClock()
	rec, cap := newCapturingRecorder(t, clock)

	rec.Record(context.Background(), EventInput{EmailUUID: "", Type: models.EmailEventSent})
	rec.Record(context.Background(), EventInput{EmailUUID: "em-6", Type: "banana"})
	rec.Record(context.Background(), EventInput{EmailUUID: "em-6", Type: ""})

	if cap.eventCount() != 0 || cap.rawCount() != 0 {
		t.Errorf("bad input must be dropped, got %d events and %d raw statements", cap.eventCount(), cap.rawCount())
	}
}

// TestRecordZeroOccurredAtFallsBackToClock pins the default: a zero OccurredAt
// is stamped with the injected clock.
func TestRecordZeroOccurredAtFallsBackToClock(t *testing.T) {
	clock := newFakeClock()
	rec, cap := newCapturingRecorder(t, clock)

	rec.Record(context.Background(), EventInput{EmailUUID: "em-7", Type: models.EmailEventDelivered})

	if got := cap.lastEvent(); !got.OccurredAt.Equal(clock.Now()) {
		t.Errorf("occurred_at = %v, want the clock %v", got.OccurredAt, clock.Now())
	}
}

// ---------- no-op and containment ----------

// TestNilRecorderAndNilDBAreNoOps pins the nil-safety contract: a nil
// recorder and a recorder without a database are valid no-ops.
func TestNilRecorderAndNilDBAreNoOps(t *testing.T) {
	var nilRec *Recorder
	nilRec.Record(context.Background(), EventInput{EmailUUID: "em-8", Type: models.EmailEventSent})

	rec := NewRecorder(nil)
	rec.Record(context.Background(), EventInput{EmailUUID: "em-8", Type: models.EmailEventSent})

	// Hooks must be equally safe.
	nilRec.recordFromEmail(&models.Email{UUID: "em-8"}, EventInput{Type: models.EmailEventSent})
	rec.recordFromEmail(&models.Email{UUID: "em-8"}, EventInput{Type: models.EmailEventSent})
}

// TestRecordRecoversFromPanic pins the panic containment: the recorder runs
// inside the sending pipeline, so a panic behind it must never escape Record.
func TestRecordRecoversFromPanic(t *testing.T) {
	gdb := openTestGorm(t, nopConnector{})
	if err := gdb.Callback().Create().Before("gorm:create").Register("emailhistory-test:panic", func(tx *gorm.DB) {
		panic("boom from the create callback")
	}); err != nil {
		t.Fatalf("register panicking callback: %v", err)
	}
	rec := NewRecorder(gdb.Session(&gorm.Session{DryRun: true}))

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Record must contain panics, got %v", r)
		}
	}()
	rec.Record(context.Background(), EventInput{EmailUUID: "em-9", Type: models.EmailEventSent})
}

// TestRecordSwallowsDatabaseErrors pins the failure containment: when the
// insert fails the recorder logs and returns — no panic, and no follow-up
// timestamp update is attempted.
func TestRecordSwallowsDatabaseErrors(t *testing.T) {
	gdb := openTestGorm(t, failingConnector{})
	cap := &capture{}
	if err := gdb.Callback().Create().Before("gorm:create").Register("emailhistory-test:capture-create", func(tx *gorm.DB) {
		if ev, ok := tx.Statement.Dest.(*models.EmailEvent); ok {
			cap.addEvent(*ev)
		}
	}); err != nil {
		t.Fatalf("register create capture: %v", err)
	}
	if err := gdb.Callback().Raw().Before("gorm:raw").Register("emailhistory-test:capture-raw", func(tx *gorm.DB) {
		cap.addRaw(tx.Statement.SQL.String(), tx.Statement.Vars)
	}); err != nil {
		t.Fatalf("register raw capture: %v", err)
	}
	rec := NewRecorder(gdb) // not DryRun: the failing conn is really reached

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Record must swallow database errors, got panic %v", r)
		}
	}()
	rec.Record(context.Background(), EventInput{EmailUUID: "em-10", Type: models.EmailEventOpened})

	if cap.eventCount() != 1 {
		t.Fatalf("insert must have been attempted once, got %d", cap.eventCount())
	}
	if cap.rawCount() != 0 {
		t.Errorf("a failed insert must skip the timestamp update, got %d raw statements", cap.rawCount())
	}
}

// ---------- capture hooks (hooks.go) ----------

// TestHooksFillDenormalizedFieldsFromEmailRow pins the hook mapping: the
// denormalized fields come from the email row, the provider/recipient
// fallbacks apply, and each hook targets its own timestamp column.
func TestHooksFillDenormalizedFieldsFromEmailRow(t *testing.T) {
	clock := newFakeClock()
	rec, cap := newCapturingRecorder(t, clock)
	ws := uint(7)
	em := &models.Email{
		UUID:        "em-11",
		WorkspaceID: &ws,
		Provider:    "postmark",
		Recipients:  pq.StringArray{"alice@example.com", "bob@example.com"},
	}

	RecordSent(rec, em, map[string]any{"source": "worker"})
	ev := cap.lastEvent()
	if ev.Type != models.EmailEventSent || ev.EmailUUID != "em-11" {
		t.Fatalf("sent hook: %+v", ev)
	}
	if ev.Provider != "postmark" {
		t.Errorf("provider fallback = %q, want postmark", ev.Provider)
	}
	if ev.Recipient != "alice@example.com" {
		t.Errorf("recipient fallback = %q, want first recipient", ev.Recipient)
	}
	if ev.Metadata != `{"source":"worker"}` {
		t.Errorf("metadata = %q", ev.Metadata)
	}
	if raws := cap.allRaw(); len(raws) != 1 || !strings.Contains(raws[0].sql, "sent_at IS NULL") {
		t.Errorf("sent hook must stamp sent_at write-once, got %v", raws)
	}

	RecordFailed(rec, em, "smtp 554", nil)
	ev = cap.lastEvent()
	if ev.Type != models.EmailEventFailed || ev.Error != "smtp 554" || ev.Metadata != "{}" {
		t.Errorf("failed hook: %+v", ev)
	}
	if n := cap.rawCount(); n != 1 {
		t.Errorf("failed hook must not stamp a timestamp, raw statements = %d", n)
	}

	RecordBounced(rec, em, "mailbox unavailable", "dsn-77", nil)
	ev = cap.lastEvent()
	if ev.Type != models.EmailEventBounced || ev.Error != "mailbox unavailable" || ev.ProviderEventID != "dsn-77" {
		t.Errorf("bounced hook: %+v", ev)
	}

	RecordOpened(rec, em, "198.51.100.1", "UA/1")
	ev = cap.lastEvent()
	if ev.Type != models.EmailEventOpened || ev.IP != "198.51.100.1" || ev.UserAgent != "UA/1" {
		t.Errorf("opened hook: %+v", ev)
	}
	if ev.Provider != "tracking" {
		t.Errorf("opened provider = %q, want tracking", ev.Provider)
	}
	if want := interactionEventID(models.EmailEventOpened, "", "198.51.100.1", "UA/1", clock.Now()); ev.ProviderEventID != want {
		t.Errorf("opened dedup key = %q, want %q", ev.ProviderEventID, want)
	}

	RecordClicked(rec, em, "https://example.com/offer", "198.51.100.1", "UA/1")
	ev = cap.lastEvent()
	if ev.Type != models.EmailEventClicked || ev.URL != "https://example.com/offer" {
		t.Errorf("clicked hook: %+v", ev)
	}

	RecordUnsubscribed(rec, em, "")
	ev = cap.lastEvent()
	if ev.Type != models.EmailEventUnsubscribed || ev.Recipient != "alice@example.com" {
		t.Errorf("unsubscribed hook: %+v", ev)
	}
	if raws := cap.allRaw(); len(raws) != 5 {
		t.Fatalf("timestamp updates = %d, want 5 (sent, bounced, opened, clicked, unsubscribed)", len(raws))
	}
	last := cap.allRaw()[len(cap.allRaw())-1]
	if !strings.Contains(last.sql, "unsubscribed_at IS NULL") {
		t.Errorf("unsubscribed hook must stamp unsubscribed_at, got %q", last.sql)
	}
}

// TestHooksDropNilEmail pins that events that could not be resolved to a
// message are dropped rather than written without a message key.
func TestHooksDropNilEmail(t *testing.T) {
	rec, cap := newCapturingRecorder(t, newFakeClock())

	RecordSent(rec, nil, nil)
	RecordFailed(rec, nil, "boom", nil)
	RecordClicked(rec, nil, "https://example.com", "", "")

	if cap.eventCount() != 0 || cap.rawCount() != 0 {
		t.Errorf("nil email must be dropped, got %d events / %d raw statements", cap.eventCount(), cap.rawCount())
	}
}
