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

package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SF-08 tests: the schedule layer's interval arithmetic, per-recipient
// terminality, notify emission through the existing event taxonomy, and the
// TTL expiry sweep. The suite follows the stylesheet_repo_test.go convention:
// it runs against TEST_DATABASE_DSN (the queue tables carry Postgres-flavored
// jsonb/text[] columns, so the fake database/sql driver pattern of the worker
// package does not fit here) and skips cleanly without one.

func scheduleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "host=localhost user=posta password=posta dbname=posta port=5432 sslmode=disable"
	if env := os.Getenv("TEST_DATABASE_DSN"); env != "" {
		dsn = env
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Skipf("skipping: no test database available: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.Subscriber{}, &models.Email{}, &models.EmailQueueSchedule{}, &models.EmailEvent{}, &models.Campaign{}, &models.CampaignMessage{}); err != nil {
		t.Skipf("skipping: cannot migrate queue schedule schema: %v", err)
	}
	return db
}

// stubSettings is the SettingsReader stub: fixed values, nothing live.
type stubSettings struct {
	str map[string]string
	i   map[string]int
}

func (s *stubSettings) GetString(key, _ string) string { return s.str[key] }
func (s *stubSettings) GetInt(key string, _ int) int   { return s.i[key] }

// recDispatcher records the webhook emissions the manager fires (the notify
// seam stub). The sweep calls the seam synchronously, so plain mutex
// bookkeeping suffices.
type recDispatcher struct {
	mu     sync.Mutex
	events []recEvent
}

type recEvent struct {
	userID      uint
	workspaceID *uint
	event       string
	body        map[string]any
}

func (d *recDispatcher) DispatchJSON(userID uint, workspaceID *uint, event string, body []byte, _ string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	d.events = append(d.events, recEvent{userID: userID, workspaceID: workspaceID, event: event, body: m})
}

func (d *recDispatcher) captured() []recEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]recEvent(nil), d.events...)
}

func (d *recDispatcher) count(event string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, e := range d.events {
		if e.event == event {
			n++
		}
	}
	return n
}

type schedFixture struct {
	db         *gorm.DB
	mgr        *Manager
	dispatcher *recDispatcher
	settings   *stubSettings
	userID     uint
	emails     []uint
	campaigns  []uint
}

func newSchedFixture(t *testing.T) *schedFixture {
	t.Helper()
	db := scheduleTestDB(t)
	f := &schedFixture{
		db:         db,
		dispatcher: &recDispatcher{},
		settings:   &stubSettings{str: map[string]string{}, i: map[string]int{}},
	}
	// emails.user_id carries an FK to users: seed one owner row per fixture
	// (unique address so concurrent fixtures in this suite cannot collide).
	u := &models.User{Name: "sf08-fixture", Email: "sf08-" + uuid.NewString() + "@test.local", PasswordHash: "x"}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("seed fixture user: %v", err)
	}
	f.userID = u.ID
	t.Cleanup(func() { db.Delete(u) })
	f.mgr = NewManager(db, Deps{
		Settings:   f.settings,
		Dispatcher: f.dispatcher,
		Recorder:   emailhistory.NewRecorder(db),
	})
	t.Cleanup(func() {
		// Scope every delete to the rows this fixture seeded: the test
		// database may be shared, and a whole-table purge would be
		// destructive (the stylesheet_repo_test.go lesson, made explicit).
		if len(f.emails) > 0 {
			db.Where("email_id IN ?", f.emails).Delete(&models.EmailQueueSchedule{})
			db.Where("email_id IN ?", f.emails).Delete(&models.EmailEvent{})
			db.Where("email_id IN ?", f.emails).Delete(&models.CampaignMessage{})
			db.Where("id IN ?", f.emails).Delete(&models.Email{})
		}
		if len(f.campaigns) > 0 {
			db.Where("campaign_id IN ?", f.campaigns).Delete(&models.CampaignMessage{})
			db.Where("id IN ?", f.campaigns).Delete(&models.Campaign{})
		}
	})
	return f
}

// seedEmail creates one queued email row with the given recipients.
func (f *schedFixture) seedEmail(t *testing.T, id uint, recipients []string) *models.Email {
	t.Helper()
	em := &models.Email{
		ID:         id,
		UUID:       uuid.NewString(),
		UserID:     f.userID,
		Sender:     "sender@example.com",
		Recipients: recipients,
		Subject:    "schedule test",
		Status:     models.EmailStatusQueued,
	}
	if err := f.db.Create(em).Error; err != nil {
		t.Fatalf("seed email: %v", err)
	}
	f.emails = append(f.emails, id)
	return em
}

func (f *schedFixture) rows(t *testing.T, emailID uint) []models.EmailQueueSchedule {
	t.Helper()
	got, err := f.mgr.repo.FindByEmailID(emailID)
	if err != nil {
		t.Fatalf("load schedule rows: %v", err)
	}
	return got
}

// reload fetches one email row with a fresh dest variable (a reused dest
// carrying a previous primary key makes gorm's First misbehave — the
// SF-08 suite hit the same).
func (f *schedFixture) reload(t *testing.T, id uint) *models.Email {
	t.Helper()
	var row models.Email
	if err := f.db.Where("id = ?", id).First(&row).Error; err != nil {
		t.Fatalf("reload email %d: %v", id, err)
	}
	return &row
}

func (f *schedFixture) rowOf(t *testing.T, emailID uint, recipient string) *models.EmailQueueSchedule {
	t.Helper()
	for i := range f.rows(t, emailID) {
		if f.rows(t, emailID)[i].Recipient == recipient {
			return &f.rows(t, emailID)[i]
		}
	}
	return nil
}

// eventMetadata decodes a timeline event's jsonb metadata.
func eventMetadata(t *testing.T, ev models.EmailEvent) map[string]any {
	t.Helper()
	m := map[string]any{}
	if ev.Metadata == "" {
		return m
	}
	if err := json.Unmarshal([]byte(ev.Metadata), &m); err != nil {
		t.Fatalf("decode event metadata %q: %v", ev.Metadata, err)
	}
	return m
}

// TestEnsureForEmailCreatesPerRecipientRows pins the schedule fan-out: one
// row per recipient, the interval list and notify addresses persisted, and
// the creation is idempotent on re-run.
func TestEnsureForEmailCreatesPerRecipientRows(t *testing.T) {
	f := newSchedFixture(t)
	em := f.seedEmail(t, 101, []string{"a@example.com", "b@example.com"})

	err := f.mgr.EnsureForEmail(em, ScheduleRequest{
		RetryIntervalsMinutes: []int{5, 30, 120},
		NotifyAddresses:       []string{"ops@example.com", "OPS@example.com", "ops@example.com"},
	})
	if err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}

	got := f.rows(t, 101)
	if len(got) != 2 {
		t.Fatalf("schedule rows = %d, want 2 (one per recipient)", len(got))
	}
	for _, row := range got {
		if row.Status != models.ScheduleStatusActive {
			t.Fatalf("row %s status = %s, want active", row.Recipient, row.Status)
		}
		if iv := row.IntervalList(); len(iv) != 3 || iv[0] != 5 || iv[1] != 30 || iv[2] != 120 {
			t.Fatalf("row %s intervals = %v, want [5 30 120]", row.Recipient, iv)
		}
		if addrs := row.NotifyAddresses; len(addrs) != 1 || addrs[0] != "ops@example.com" {
			t.Fatalf("row %s notify = %v, want deduped [ops@example.com]", row.Recipient, addrs)
		}
		if row.NextRetryAt != nil {
			t.Fatalf("row %s next_retry_at = %v, want nil (no attempt yet)", row.Recipient, row.NextRetryAt)
		}
	}

	// Re-run (e.g. a duplicate send path): idempotent — no duplicate rows.
	if err := f.mgr.EnsureForEmail(em, ScheduleRequest{RetryIntervalsMinutes: []int{5, 30}}); err != nil {
		t.Fatalf("re-run EnsureForEmail: %v", err)
	}
	if got := f.rows(t, 101); len(got) != 2 {
		t.Fatalf("schedule rows after re-run = %d, want 2", len(got))
	}
}

// TestEnsureForEmailWithoutConfigKeepsLegacy pins the no-regression default:
// no request fields and empty settings → no rows, no expires_at stamp — the
// pre-SF-08 queue behavior byte-for-byte.
func TestEnsureForEmailWithoutConfigKeepsLegacy(t *testing.T) {
	f := newSchedFixture(t)
	em := f.seedEmail(t, 102, []string{"a@example.com"})

	if err := f.mgr.EnsureForEmail(em, ScheduleRequest{}); err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}
	if got := f.rows(t, 102); len(got) != 0 {
		t.Fatalf("schedule rows = %d, want 0 (legacy)", len(got))
	}
	var row models.Email
	if err := f.db.First(&row, 102).Error; err != nil {
		t.Fatalf("reload email: %v", err)
	}
	if row.ExpiresAt != nil {
		t.Fatalf("expires_at = %v, want nil (no TTL requested)", row.ExpiresAt)
	}
}

// TestEnsureForEmailStampsExpiry pins both TTL sources: the request value
// wins over the platform setting, and the setting applies when the request
// carries none.
func TestEnsureForEmailStampsExpiry(t *testing.T) {
	f := newSchedFixture(t)

	// Request value.
	em := f.seedEmail(t, 103, []string{"a@example.com"})
	want := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	if err := f.mgr.EnsureForEmail(em, ScheduleRequest{ExpiresAt: &want}); err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}
	var row models.Email
	if err := f.db.First(&row, 103).Error; err != nil {
		t.Fatalf("reload email: %v", err)
	}
	if row.ExpiresAt == nil || !row.ExpiresAt.Truncate(time.Second).Equal(want) {
		t.Fatalf("expires_at = %v, want request value %v", row.ExpiresAt, want)
	}

	// Setting value when the request carries none.
	f.settings.i[SettingExpiryMinutes] = 60
	em2 := f.seedEmail(t, 104, []string{"b@example.com"})
	if err := f.mgr.EnsureForEmail(em2, ScheduleRequest{}); err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}
	var row2 models.Email
	if err := f.db.First(&row2, 104).Error; err != nil {
		t.Fatalf("reload email: %v", err)
	}
	if row2.ExpiresAt == nil {
		t.Fatal("expires_at = nil, want the setting-derived TTL stamp")
	}
	if diff := time.Until(*row2.ExpiresAt); diff < 55*time.Minute || diff > 61*time.Minute {
		t.Fatalf("expires_at = %v (%s away), want ~60 minutes", row2.ExpiresAt, diff)
	}
}

// TestRecordFailureHonorsIntervalList pins the interval arithmetic end to
// end: attempt 1 → next in intervals[0]; attempt 2 → intervals[1]; attempt 3
// (past the list) exhausts the row and fires the notify emission.
func TestRecordFailureHonorsIntervalList(t *testing.T) {
	f := newSchedFixture(t)
	base := time.Now().Add(-time.Hour)
	f.mgr.deps.Now = func() time.Time { return base }
	em := f.seedEmail(t, 105, []string{"a@example.com"})
	if err := f.mgr.EnsureForEmail(em, ScheduleRequest{
		RetryIntervalsMinutes: []int{5, 30},
		NotifyAddresses:       []string{"ops@example.com"},
	}); err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}

	// Attempt 1 fails → next retry in 5 minutes, still active.
	out := f.mgr.RecordFailure(em, "", "451 temporary")
	if !out.AnyRows || out.AllExhausted {
		t.Fatalf("outcome = %+v, want AnyRows && !AllExhausted", out)
	}
	row := f.rowOf(t, 105, "a@example.com")
	if row.AttemptIndex != 1 || row.NextRetryAt == nil {
		t.Fatalf("after attempt 1: index=%d next=%v, want index 1 + scheduled next", row.AttemptIndex, row.NextRetryAt)
	}
	if want := base.Add(5 * time.Minute).Truncate(time.Microsecond); row.NextRetryAt == nil || !row.NextRetryAt.Equal(want) {
		t.Fatalf("next retry = %v, want %v (intervals[0])", row.NextRetryAt, want)
	}

	// Attempt 2 fails → next retry in 30 minutes, still active.
	out = f.mgr.RecordFailure(em, "", "451 temporary")
	if out.AllExhausted || out.NextRetry == nil {
		t.Fatalf("outcome = %+v, want a live next retry", out)
	}
	row = f.rowOf(t, 105, "a@example.com")
	if want := base.Add(30 * time.Minute).Truncate(time.Microsecond); row.NextRetryAt == nil || !row.NextRetryAt.Equal(want) {
		t.Fatalf("next retry = %v, want %v (intervals[1])", row.NextRetryAt, want)
	}

	// Attempt 3 fails past the list → exhausted, no next retry, notified.
	out = f.mgr.RecordFailure(em, "", "451 temporary")
	if !out.AllExhausted {
		t.Fatalf("outcome.AllExhausted = false after the interval list ran out, want true")
	}
	row = f.rowOf(t, 105, "a@example.com")
	if row.Status != models.ScheduleStatusExhausted || row.NextRetryAt != nil {
		t.Fatalf("row = %s next=%v, want exhausted with cleared next", row.Status, row.NextRetryAt)
	}
	if got := f.dispatcher.count("email.failed"); got != 1 {
		t.Fatalf("notify emissions = %d, want 1", got)
	}
	ev := f.dispatcher.captured()[0]
	if ev.body["recipient"] != "a@example.com" {
		t.Fatalf("notify payload recipient = %v, want a@example.com", ev.body["recipient"])
	}
	if addrs, ok := ev.body["notify_addresses"].([]any); !ok || len(addrs) != 1 || addrs[0] != "ops@example.com" {
		t.Fatalf("notify payload addresses = %v, want [ops@example.com]", ev.body["notify_addresses"])
	}
	if ev.body["schedule_event"] != "recipient_schedule_exhausted" {
		t.Fatalf("schedule_event = %v, want recipient_schedule_exhausted", ev.body["schedule_event"])
	}

	// The timeline event recorded through the existing taxonomy.
	var events []models.EmailEvent
	if err := f.db.Where("email_uuid = ?", em.UUID).Find(&events).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 1 || events[0].Type != models.EmailEventFailed {
		t.Fatalf("timeline events = %+v, want one failed event", events)
	}
	if meta := eventMetadata(t, events[0]); meta["schedule_event"] != "recipient_schedule_exhausted" {
		t.Fatalf("timeline metadata = %v, want schedule_event recipient_schedule_exhausted", meta)
	}
}

// TestRecordFailurePerRecipientAdvance pins the recipient-scoped advance: a
// RCPT TO-stage failure advances only the named recipient's row; the other
// rows keep their state and count toward the active set.
func TestRecordFailurePerRecipientAdvance(t *testing.T) {
	f := newSchedFixture(t)
	em := f.seedEmail(t, 106, []string{"a@example.com", "b@example.com"})
	if err := f.mgr.EnsureForEmail(em, ScheduleRequest{RetryIntervalsMinutes: []int{10}}); err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}

	out := f.mgr.RecordFailure(em, "a@example.com", "452 4.2.2 box full")
	if !out.AnyRows || out.AllExhausted {
		t.Fatalf("outcome = %+v, want b still active", out)
	}
	if row := f.rowOf(t, 106, "a@example.com"); row.AttemptIndex != 1 {
		t.Fatalf("a's attempt_index = %d, want 1", row.AttemptIndex)
	}
	if row := f.rowOf(t, 106, "b@example.com"); row.AttemptIndex != 0 || row.NextRetryAt != nil {
		t.Fatalf("b untouched: index=%d next=%v, want 0/nil", row.AttemptIndex, row.NextRetryAt)
	}
}

// TestRecordFailureNoRowsKeepsLegacy pins the no-regression path: a message
// without schedule rows (and no platform default) yields a zero outcome so
// callers fall back to their pre-SF-08 retry behavior.
func TestRecordFailureNoRowsKeepsLegacy(t *testing.T) {
	f := newSchedFixture(t)
	em := f.seedEmail(t, 107, []string{"a@example.com"})

	out := f.mgr.RecordFailure(em, "", "boom")
	if out.AnyRows || out.AllExhausted || out.NextRetry != nil {
		t.Fatalf("outcome = %+v, want the zero outcome (legacy)", out)
	}
	if got := f.rows(t, 107); len(got) != 0 {
		t.Fatalf("rows = %d, want 0", len(got))
	}
}

// TestRecordFailureLazilyCreatesFromSettings pins the platform-default path:
// with queue_retry_intervals configured, the first failure of a message that
// bypassed the request surface (campaign, automation, system mail) creates
// the fan-out and advances it.
func TestRecordFailureLazilyCreatesFromSettings(t *testing.T) {
	f := newSchedFixture(t)
	f.settings.str[SettingRetryIntervals] = "15,60"
	em := f.seedEmail(t, 108, []string{"a@example.com", "b@example.com"})

	out := f.mgr.RecordFailure(em, "", "451 try later")
	if !out.AnyRows || out.NextRetry == nil {
		t.Fatalf("outcome = %+v, want rows lazily created", out)
	}
	if got := f.rows(t, 108); len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	for _, row := range f.rows(t, 108) {
		if iv := row.IntervalList(); len(iv) != 2 || iv[0] != 15 || iv[1] != 60 {
			t.Fatalf("row %s intervals = %v, want [15 60] (setting)", row.Recipient, iv)
		}
	}

	// A broken setting value degrades to legacy instead of wedging retries.
	f.settings.str[SettingRetryIntervals] = "not-a-number,xyz"
	em2 := f.seedEmail(t, 109, []string{"c@example.com"})
	if out := f.mgr.RecordFailure(em2, "", "boom"); out.AnyRows {
		t.Fatalf("broken setting produced rows, want legacy zero outcome")
	}
}

// TestDecideRetryStates pins the four claim-time decisions end to end.
func TestDecideRetryStates(t *testing.T) {
	f := newSchedFixture(t)
	now := time.Now()

	// No rows → none (legacy).
	em0 := f.seedEmail(t, 110, []string{"a@example.com"})
	if d, _ := f.mgr.DecideRetry(em0.ID, now); d != DecisionNone {
		t.Fatalf("no rows → %v, want DecisionNone", d)
	}

	// Active rows without a scheduled retry → none (first attempt).
	em1 := f.seedEmail(t, 111, []string{"a@example.com"})
	if err := f.mgr.EnsureForEmail(em1, ScheduleRequest{RetryIntervalsMinutes: []int{5}}); err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}
	if d, _ := f.mgr.DecideRetry(em1.ID, now); d != DecisionNone {
		t.Fatalf("rows without next retry → %v, want DecisionNone", d)
	}

	// Future next retry → wait.
	f.mgr.RecordFailure(em1, "", "451")
	if d, due := f.mgr.DecideRetry(em1.ID, now); d != DecisionWait || due.Before(now) {
		t.Fatalf("future next retry → (%v, %v), want DecisionWait in the future", d, due)
	}

	// Overdue next retry → due. (Timestamps round-trip through Postgres at
	// microsecond precision, so the expectation is truncated to match.)
	past := now.Add(-2 * time.Minute).Truncate(time.Microsecond)
	if err := f.db.Model(&models.EmailQueueSchedule{}).Where("email_id = ?", em1.ID).
		Update("next_retry_at", past).Error; err != nil {
		t.Fatalf("set past next retry: %v", err)
	}
	if d, due := f.mgr.DecideRetry(em1.ID, now); d != DecisionDue || !due.Equal(past) {
		t.Fatalf("overdue next retry → (%v, %v), want DecisionDue at %v", d, due, past)
	}

	// All rows terminal → exhausted.
	for _, row := range f.rows(t, em1.ID) {
		if _, err := f.mgr.repo.TerminateCAS(row.ID, models.ScheduleStatusExhausted, "done", now); err != nil {
			t.Fatalf("terminate row: %v", err)
		}
	}
	if d, _ := f.mgr.DecideRetry(em1.ID, now); d != DecisionExhausted {
		t.Fatalf("all terminal → %v, want DecisionExhausted", d)
	}
}

// TestRetryDelay pins the asynq RetryDelayFunc consult: wait → the remaining
// hold time, due → 0, no schedule → ok=false (the caller falls back to its
// default policy).
func TestRetryDelay(t *testing.T) {
	f := newSchedFixture(t)
	now := time.Now()

	em := f.seedEmail(t, 112, []string{"a@example.com"})
	if _, ok := f.mgr.RetryDelay(em.ID, now); ok {
		t.Fatal("no rows → ok=true, want false")
	}
	if err := f.mgr.EnsureForEmail(em, ScheduleRequest{RetryIntervalsMinutes: []int{10}}); err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}
	if _, ok := f.mgr.RetryDelay(em.ID, now); ok {
		t.Fatal("no scheduled retry → ok=true, want false")
	}
	f.mgr.RecordFailure(em, "", "451")
	d, ok := f.mgr.RetryDelay(em.ID, now)
	if !ok || d <= 0 || d > 11*time.Minute {
		t.Fatalf("delay = (%v, %v), want (0,10m]", d, ok)
	}
	if err := f.db.Model(&models.EmailQueueSchedule{}).Where("email_id = ?", em.ID).
		Update("next_retry_at", now.Add(-time.Minute)).Error; err != nil {
		t.Fatalf("set past next retry: %v", err)
	}
	if d, ok := f.mgr.RetryDelay(em.ID, now); !ok || d != 0 {
		t.Fatalf("overdue delay = (%v, %v), want (0, true)", d, ok)
	}
}

// TestFinalizeEmailNotifiesActiveRows pins the message-level finalization:
// active rows close as cancelled and rows with notify addresses emit.
func TestFinalizeEmailNotifiesActiveRows(t *testing.T) {
	f := newSchedFixture(t)
	em := f.seedEmail(t, 113, []string{"a@example.com", "b@example.com"})
	if err := f.mgr.EnsureForEmail(em, ScheduleRequest{
		RetryIntervalsMinutes: []int{5},
		NotifyAddresses:       []string{"ops@example.com"},
	}); err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}

	f.mgr.FinalizeEmail(em, "no SMTP server configured")
	for _, row := range f.rows(t, 113) {
		if row.Status != models.ScheduleStatusCancelled {
			t.Fatalf("row %s status = %s, want cancelled", row.Recipient, row.Status)
		}
	}
	if got := f.dispatcher.count("email.failed"); got != 2 {
		t.Fatalf("notify emissions = %d, want 2 (one per row)", got)
	}
}

// TestMarkDeliveredClosesRows pins the success-side bookkeeping: delivered
// rows close silently (no notify emission — the sent event is the delivery
// notification).
func TestMarkDeliveredClosesRows(t *testing.T) {
	f := newSchedFixture(t)
	em := f.seedEmail(t, 114, []string{"a@example.com"})
	if err := f.mgr.EnsureForEmail(em, ScheduleRequest{
		RetryIntervalsMinutes: []int{5},
		NotifyAddresses:       []string{"ops@example.com"},
	}); err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}

	f.mgr.MarkDelivered(em.ID)
	row := f.rowOf(t, 114, "a@example.com")
	if row.Status != models.ScheduleStatusDelivered {
		t.Fatalf("row status = %s, want delivered", row.Status)
	}
	if got := f.dispatcher.count("email.failed"); got != 0 {
		t.Fatalf("notify emissions = %d, want 0", got)
	}
	if d, _ := f.mgr.DecideRetry(em.ID, time.Now()); d != DecisionExhausted {
		t.Fatalf("decision after delivery = %v, want DecisionExhausted", d)
	}
}

// TestSweepExpired pins the TTL expiry sweep: expired queued/scheduled
// messages are CAS-marked failed with the event, their schedule rows expire
// with the notify emission, linked campaign messages fail with the message,
// and everything else (delivered, not-yet-expired) is untouched.
func TestSweepExpired(t *testing.T) {
	f := newSchedFixture(t)
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	// Expired + queued, with schedule rows and notify addresses.
	em1 := f.seedEmail(t, 201, []string{"a@example.com"})
	f.db.Model(&models.Email{}).Where("id = ?", 201).Update("expires_at", past)
	if err := f.mgr.EnsureForEmail(em1, ScheduleRequest{
		RetryIntervalsMinutes: []int{5},
		NotifyAddresses:       []string{"ops@example.com"},
	}); err != nil {
		t.Fatalf("EnsureForEmail: %v", err)
	}
	// Expired + scheduled.
	f.seedEmail(t, 202, []string{"b@example.com"})
	f.db.Model(&models.Email{}).Where("id = ?", 202).Updates(map[string]interface{}{
		"expires_at": past,
		"status":     models.EmailStatusScheduled,
	})
	// Future deadline — untouched.
	f.seedEmail(t, 203, []string{"c@example.com"})
	f.db.Model(&models.Email{}).Where("id = ?", 203).Update("expires_at", future)
	// Expired but already delivered — untouched.
	f.seedEmail(t, 204, []string{"d@example.com"})
	f.db.Model(&models.Email{}).Where("id = ?", 204).Updates(map[string]interface{}{
		"expires_at": past,
		"status":     models.EmailStatusSent,
	})

	// Campaign message linked to the expired one (campaign_messages carries
	// an FK to subscribers, so one subscriber row rides the fixture).
	sub := &models.Subscriber{UserID: f.userID, Email: "sub-" + uuid.NewString() + "@test.local", Status: models.SubscriberStatusSubscribed}
	if err := f.db.Create(sub).Error; err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}
	camp := &models.Campaign{Name: "camp", Status: models.CampaignStatusSending, UserID: f.userID}
	if err := f.db.Create(camp).Error; err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	f.campaigns = append(f.campaigns, camp.ID)
	emID := em1.ID
	cm := &models.CampaignMessage{CampaignID: camp.ID, SubscriberID: sub.ID, EmailID: &emID, Status: models.CampaignMsgQueued}
	if err := f.db.Create(cm).Error; err != nil {
		t.Fatalf("seed campaign message: %v", err)
	}

	stats, err := f.mgr.SweepExpired(context.Background(), now, 100)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if stats.Expired != 2 {
		t.Fatalf("stats.Expired = %d, want 2", stats.Expired)
	}
	if stats.Skipped != 0 {
		t.Fatalf("stats.Skipped = %d, want 0", stats.Skipped)
	}

	reload := func(id uint) *models.Email {
		var row models.Email
		if err := f.db.Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatalf("reload email %d: %v", id, err)
		}
		return &row
	}
	if got := reload(201); got.Status != models.EmailStatusFailed || got.ErrorMessage == "" {
		t.Fatalf("expired email = %s/%q, want failed with a reason", got.Status, got.ErrorMessage)
	}
	if got := reload(202); got.Status != models.EmailStatusFailed {
		t.Fatalf("expired scheduled email status = %s, want failed", got.Status)
	}
	if got := reload(203); got.Status != models.EmailStatusQueued {
		t.Fatalf("future email status = %s, want queued (untouched)", got.Status)
	}
	if got := reload(204); got.Status != models.EmailStatusSent {
		t.Fatalf("delivered email status = %s, want sent (untouched)", got.Status)
	}

	// The expired message's schedule rows are expired with their notify.
	row := f.rowOf(t, 201, "a@example.com")
	if row.Status != models.ScheduleStatusExpired {
		t.Fatalf("schedule row status = %s, want expired", row.Status)
	}
	if stats.Notified != 1 {
		t.Fatalf("stats.Notified = %d, want 1", stats.Notified)
	}
	if got := f.dispatcher.count("email.failed"); got < 1 {
		t.Fatalf("webhook email.failed emissions = %d, want at least the message-level one", got)
	}

	// The campaign message failed with the message.
	var cmRow models.CampaignMessage
	if err := f.db.First(&cmRow, cm.ID).Error; err != nil {
		t.Fatalf("reload campaign message: %v", err)
	}
	if cmRow.Status != models.CampaignMsgFailed {
		t.Fatalf("campaign message status = %s, want failed", cmRow.Status)
	}

	// The timeline carries the message-level expiry event.
	var events []models.EmailEvent
	if err := f.db.Where("email_uuid = ?", em1.UUID).Find(&events).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.Type == models.EmailEventFailed && eventMetadata(t, ev)["schedule_event"] == "message_expired" {
			found = true
		}
	}
	if !found {
		t.Fatalf("timeline = %+v, want a failed event with schedule_event=message_expired", events)
	}
}

// TestSweepExpiredEmitsDSNStyleDeliverByEvent pins the SF-09 deliver-by
// handling: an expired message whose deadline came from the RFC 2852
// DELIVERBY parameter is expired with a DSN-style event — the RFC 3463
// X.4.7 enhanced status and diagnostic for a delivery time that ran out
// (permanent class, matching the terminal status flip), the action, and the
// by-time provenance — on both the timeline event and the webhook payload.
func TestSweepExpiredEmitsDSNStyleDeliverByEvent(t *testing.T) {
	f := newSchedFixture(t)
	now := time.Now()
	past := now.Add(-time.Hour)

	em := f.seedEmail(t, 206, []string{"a@example.com"})
	f.db.Model(&models.Email{}).Where("id = ?", 206).Updates(map[string]interface{}{
		"expires_at":         past,
		"deliver_by_seconds": 90,
	})

	stats, err := f.mgr.SweepExpired(context.Background(), now, 100)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if stats.Expired != 1 {
		t.Fatalf("stats.Expired = %d, want 1", stats.Expired)
	}

	var events []models.EmailEvent
	if err := f.db.Where("email_uuid = ?", em.UUID).Find(&events).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 1 || events[0].Type != models.EmailEventFailed {
		t.Fatalf("timeline events = %+v, want one failed event", events)
	}
	meta := eventMetadata(t, events[0])
	for k, want := range map[string]any{
		"schedule_event":     "message_expired",
		"action":             "failed",
		"enhanced_status":    "5.4.7",
		"diagnostic":         "delivery time expired",
		"deliver_by_seconds": float64(90),
		"deadline_source":    "deliverby",
	} {
		if meta[k] != want {
			t.Fatalf("timeline metadata[%s] = %v, want %v", k, meta[k], want)
		}
	}

	got := f.dispatcher.captured()
	if len(got) != 1 || got[0].event != "email.failed" {
		t.Fatalf("webhook events = %+v, want one email.failed", got)
	}
	for k, want := range map[string]any{
		"enhanced_status":    "5.4.7",
		"deliver_by_seconds": float64(90),
		"deadline_source":    "deliverby",
	} {
		if got[0].body[k] != want {
			t.Fatalf("webhook payload[%s] = %v, want %v", k, got[0].body[k], want)
		}
	}
}

// TestExpireByDeadlineAtClaim pins the SF-09 claim-time finalization: a
// message past its deadline whose task reaches the claim is CAS-failed (the
// processing state included, so a redelivered task cannot deliver past the
// deadline either), and a message without a deadline is a no-op.
func TestExpireByDeadlineAtClaim(t *testing.T) {
	f := newSchedFixture(t)
	now := time.Now()
	past := now.Add(-time.Hour)

	// Queued, expired: the CAS wins and the message finalizes.
	f.seedEmail(t, 207, []string{"a@example.com"})
	f.db.Model(&models.Email{}).Where("id = ?", 207).Update("expires_at", past)
	var em models.Email
	if err := f.db.Where("id = ?", 207).First(&em).Error; err != nil {
		t.Fatalf("reload email: %v", err)
	}
	if err := f.mgr.ExpireByDeadline(&em, now); err != nil {
		t.Fatalf("ExpireByDeadline: %v", err)
	}
	if got := f.reload(t, 207); got.Status != models.EmailStatusFailed || got.ErrorMessage == "" {
		t.Fatalf("expired email = %s/%q, want failed with a reason", got.Status, got.ErrorMessage)
	}

	// Processing, expired: the claim-time CAS covers mid-send redeliveries.
	f.seedEmail(t, 208, []string{"b@example.com"})
	f.db.Model(&models.Email{}).Where("id = ?", 208).Updates(map[string]interface{}{
		"expires_at": past,
		"status":     models.EmailStatusProcessing,
	})
	var em2 models.Email
	if err := f.db.Where("id = ?", 208).First(&em2).Error; err != nil {
		t.Fatalf("reload email: %v", err)
	}
	if err := f.mgr.ExpireByDeadline(&em2, now); err != nil {
		t.Fatalf("ExpireByDeadline (processing): %v", err)
	}
	if got := f.reload(t, 208); got.Status != models.EmailStatusFailed {
		t.Fatalf("processing expired email status = %s, want failed", got.Status)
	}

	// Delivered, expired: the claim-time CAS must not overwrite delivery.
	f.seedEmail(t, 209, []string{"c@example.com"})
	f.db.Model(&models.Email{}).Where("id = ?", 209).Updates(map[string]interface{}{
		"expires_at": past,
		"status":     models.EmailStatusSent,
	})
	var em3 models.Email
	if err := f.db.Where("id = ?", 209).First(&em3).Error; err != nil {
		t.Fatalf("reload email: %v", err)
	}
	if err := f.mgr.ExpireByDeadline(&em3, now); err != nil {
		t.Fatalf("ExpireByDeadline (delivered): %v", err)
	}
	if got := f.reload(t, 209); got.Status != models.EmailStatusSent {
		t.Fatalf("delivered email status = %s, want sent (never overwritten)", got.Status)
	}

	// Not yet expired: a no-op.
	f.seedEmail(t, 210, []string{"d@example.com"})
	f.db.Model(&models.Email{}).Where("id = ?", 210).Update("expires_at", now.Add(time.Hour))
	var em4 models.Email
	if err := f.db.Where("id = ?", 210).First(&em4).Error; err != nil {
		t.Fatalf("reload email: %v", err)
	}
	if err := f.mgr.ExpireByDeadline(&em4, now); err != nil {
		t.Fatalf("ExpireByDeadline (future): %v", err)
	}
	if got := f.reload(t, 210); got.Status != models.EmailStatusQueued {
		t.Fatalf("future-deadline email status = %s, want queued (untouched)", got.Status)
	}

	// No deadline at all: a no-op.
	em5 := f.seedEmail(t, 211, []string{"e@example.com"})
	if err := f.mgr.ExpireByDeadline(em5, now); err != nil {
		t.Fatalf("ExpireByDeadline (no deadline): %v", err)
	}
	if got := f.reload(t, 211); got.Status != models.EmailStatusQueued {
		t.Fatalf("no-deadline email status = %s, want queued (untouched)", got.Status)
	}
}

// TestExpireEmailCASClaimsOnce pins the sweep's per-message CAS: only one
// claimant finalizes an expired message; once it left the undelivered set
// (delivered meanwhile, another replica's sweep), the claim matches nothing.
func TestExpireEmailCASClaimsOnce(t *testing.T) {
	f := newSchedFixture(t)
	now := time.Now()
	past := now.Add(-time.Hour)

	em := f.seedEmail(t, 205, []string{"a@example.com"})
	f.db.Model(&models.Email{}).Where("id = ?", 205).Update("expires_at", past)

	won, err := f.mgr.repo.ExpireEmailCAS(em.ID, now, "sweep")
	if err != nil || !won {
		t.Fatalf("first sweep claim = (%v, %v), want won", won, err)
	}
	// The row is failed now: a racing second sweep (or the same sweep's
	// duplicate) matches nothing — and a delivered row never matches either.
	f.db.Model(&models.Email{}).Where("id = ?", 205).Update("status", models.EmailStatusSent)
	won, err = f.mgr.repo.ExpireEmailCAS(em.ID, now, "sweep-again")
	if err != nil || won {
		t.Fatalf("second sweep claim = (%v, %v), want lost", won, err)
	}
	var got models.Email
	if err := f.db.First(&got, 205).Error; err != nil {
		t.Fatalf("reload email: %v", err)
	}
	if got.Status != models.EmailStatusSent {
		t.Fatalf("delivered email status = %s, want sent (sweep must not overwrite)", got.Status)
	}
}

// TestParseIntervalList pins the settings-surface parser.
func TestParseIntervalList(t *testing.T) {
	cases := []struct {
		raw   string
		want  []int
		fails bool
	}{
		{raw: "5,30,120", want: []int{5, 30, 120}},
		{raw: " 5 , 30 ", want: []int{5, 30}},
		{raw: "1440", want: []int{1440}},
		{raw: "", want: nil},
		{raw: "abc", fails: true},
		{raw: "0", fails: true},
		{raw: "-5", fails: true},
		{raw: "43201", fails: true},
	}
	for _, c := range cases {
		got, err := ParseIntervalList(c.raw)
		if c.fails {
			if err == nil {
				t.Errorf("ParseIntervalList(%q) = %v, want error", c.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseIntervalList(%q) error: %v", c.raw, err)
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("ParseIntervalList(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
	if err := SanitizeIntervals(make([]int, MaxScheduleIntervals+1)); err == nil {
		t.Error("SanitizeIntervals accepted an oversized list, want error")
	}
}

// TestNilManagerNoOps pins the nil-safety contract: an unwired manager
// degrades every entry point to a no-op, so callers can wire it
// unconditionally without changing the pre-SF-08 behavior.
func TestNilManagerNoOps(t *testing.T) {
	var m *Manager
	if err := m.EnsureForEmail(&models.Email{ID: 1}, ScheduleRequest{}); err != nil {
		t.Fatalf("nil EnsureForEmail: %v", err)
	}
	if out := m.RecordFailure(&models.Email{ID: 1}, "", "boom"); out.AnyRows || out.AllExhausted {
		t.Fatalf("nil RecordFailure = %+v, want zero outcome", out)
	}
	if d, _ := m.DecideRetry(1, time.Now()); d != DecisionNone {
		t.Fatalf("nil DecideRetry = %v, want DecisionNone", d)
	}
	if _, ok := m.RetryDelay(1, time.Now()); ok {
		t.Fatal("nil RetryDelay ok=true, want false")
	}
	m.MarkDelivered(1)
	m.FinalizeEmail(&models.Email{ID: 1}, "reason")
	m.MarkRecipientRejected(&models.Email{ID: 1}, "a@example.com", "550")
	stats, err := m.SweepExpired(context.Background(), time.Now(), 100)
	if err != nil || stats.Expired != 0 {
		t.Fatalf("nil SweepExpired = (%+v, %v), want zero stats", stats, err)
	}
}
