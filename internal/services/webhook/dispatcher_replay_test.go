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

package webhook

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// R-06 tests: the exponential retry backoff (comment/code mismatch fix) and
// the delivery replay path. The persistence capture pattern follows
// dispatcher_attempts_test.go, extended to also capture delivery updates so
// the replay outcome rewrite can be pinned.

// TestRetryDelayExponentialCurve pins the backoff curve: min(base*2^(n-1),
// 60s) plus jitter(0, base/4). Attempts 1-5 walk the doubling curve; from
// attempt 7 on the delay is capped at 60s (+ jitter).
func TestRetryDelayExponentialCurve(t *testing.T) {
	base := defaultRetryBaseWait
	jitter := base / 4
	for _, tc := range []struct {
		attempt   int
		wantFloor time.Duration
		wantCeil  time.Duration
	}{
		{1, base, base + jitter},         // 2s .. 2.5s
		{2, 2 * base, 2*base + jitter},   // 4s .. 4.5s
		{3, 4 * base, 4*base + jitter},   // 8s .. 8.5s
		{4, 8 * base, 8*base + jitter},   // 16s .. 16.5s
		{5, 16 * base, 16*base + jitter}, // 32s .. 32.5s
		// 64s raw exceeds the 60s cap on attempt 6: the cap applies.
		{6, maxRetryWait, maxRetryWait + jitter},
		{7, maxRetryWait, maxRetryWait + jitter},
		{12, maxRetryWait, maxRetryWait + jitter},
	} {
		for i := 0; i < 25; i++ {
			got := retryDelay(tc.attempt)
			if got < tc.wantFloor || got > tc.wantCeil {
				t.Fatalf("retryDelay(%d) = %v, want within [%v, %v]", tc.attempt, got, tc.wantFloor, tc.wantCeil)
			}
		}
	}
}

// TestRetryDelayNeverExceedsCap sweeps a wide attempt range and asserts the
// hard 60s (+ jitter) ceiling and positivity — including the overflow guard
// for absurd attempt numbers.
func TestRetryDelayNeverExceedsCap(t *testing.T) {
	for attempt := 1; attempt <= 200; attempt++ {
		got := retryDelay(attempt)
		if got <= 0 {
			t.Fatalf("retryDelay(%d) = %v, must be positive", attempt, got)
		}
		if got > maxRetryWait+defaultRetryBaseWait/4 {
			t.Fatalf("retryDelay(%d) = %v, exceeds the %v cap (+jitter)", attempt, got, maxRetryWait)
		}
	}
}

// replayUpdate records one UPDATE the dispatcher issued (SQL + bound vars).
type replayUpdate struct {
	sql  string
	vars []any
}

// replayCapture extends the attempt capture with delivery updates.
type replayCapture struct {
	mu         sync.Mutex
	deliveries []models.WebhookDelivery
	attempts   []models.WebhookDeliveryAttempt
	updates    []replayUpdate
}

func (c *replayCapture) allAttempts() []models.WebhookDeliveryAttempt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]models.WebhookDeliveryAttempt(nil), c.attempts...)
}

func (c *replayCapture) allUpdates() []replayUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]replayUpdate(nil), c.updates...)
}

// newReplayGorm opens a DryRun gorm session with create/update capture
// registered and returns the capture alongside it.
func newReplayGorm(t *testing.T) (*gorm.DB, *replayCapture) {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(nopConnector{}),
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open dry-run gorm: %v", err)
	}
	cap := &replayCapture{}
	if err := gdb.Callback().Create().Before("gorm:create").Register("replay-test:capture-create", func(tx *gorm.DB) {
		cap.mu.Lock()
		defer cap.mu.Unlock()
		switch dest := tx.Statement.Dest.(type) {
		case *models.WebhookDelivery:
			dest.ID = capturedDeliveryID
			cap.deliveries = append(cap.deliveries, *dest)
		case *models.WebhookDeliveryAttempt:
			cap.attempts = append(cap.attempts, *dest)
		}
	}); err != nil {
		t.Fatalf("register create capture: %v", err)
	}
	if err := gdb.Callback().Update().After("gorm:update").Register("replay-test:capture-update", func(tx *gorm.DB) {
		cap.mu.Lock()
		defer cap.mu.Unlock()
		cap.updates = append(cap.updates, replayUpdate{
			sql:  tx.Statement.SQL.String(),
			vars: append([]any(nil), tx.Statement.Vars...),
		})
	}); err != nil {
		t.Fatalf("register update capture: %v", err)
	}
	return gdb, cap
}

// newReplayCapturingDispatcher wires a permissive dispatcher to the capturing
// DryRun repos.
func newReplayCapturingDispatcher(t *testing.T) (*Dispatcher, *replayCapture) {
	t.Helper()
	gdb, cap := newReplayGorm(t)
	d := newPermissiveDispatcher()
	d.SetDeliveryRepo(repositories.NewWebhookDeliveryRepository(gdb.Session(&gorm.Session{DryRun: true})))
	d.SetAttemptRepo(repositories.NewWebhookDeliveryAttemptRepository(gdb.Session(&gorm.Session{DryRun: true})))
	return d, cap
}

// TestDeliverWithRetryBackoffSeam pins that the retry loop sleeps exactly the
// delays the backoff function computes, between attempts only (no trailing
// sleep after the final attempt).
func TestDeliverWithRetryBackoffSeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := newPermissiveDispatcher()
	d.maxRetries = 4
	d.backoffDelay = func(attempt int) time.Duration { return time.Duration(attempt) * time.Second }
	var mu sync.Mutex
	var sleeps []time.Duration
	d.sleep = func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		sleeps = append(sleeps, d)
	}

	d.deliverWithRetry(1, models.Webhook{ID: 11, UserID: 1, URL: srv.URL},
		deliverBody(t, "email.sent", "backoff"), "email.sent")

	mu.Lock()
	defer mu.Unlock()
	if len(sleeps) != 3 {
		t.Fatalf("sleeps = %v, want 3 (between 4 attempts, none after the last)", sleeps)
	}
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second} {
		if sleeps[i] != want {
			t.Errorf("sleep #%d = %v, want %v", i, sleeps[i], want)
		}
	}
}

// TestRecordDeliveryStoresPayload pins that the delivery row keeps the exact
// outbound payload — the data the replay endpoint re-sends.
func TestRecordDeliveryStoresPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d, cap := newReplayCapturingDispatcher(t)
	body := deliverBody(t, "email.sent", "payload-check")
	d.deliverWithRetry(1, models.Webhook{ID: 12, UserID: 1, URL: srv.URL}, body, "email.sent")

	if len(cap.deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(cap.deliveries))
	}
	if cap.deliveries[0].Payload != string(body) {
		t.Errorf("stored payload = %q, want %q", cap.deliveries[0].Payload, string(body))
	}
}

// TestReplayDeliveryAppendsAttemptAndUpdatesOutcome covers the replay happy
// path: the attempt row continues the original numbering (prior max + 1) on
// the SAME delivery, and the delivery row's outcome is rewritten to success.
func TestReplayDeliveryAppendsAttemptAndUpdatesOutcome(t *testing.T) {
	const responseBody = `{"ok":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	}))
	defer srv.Close()

	d, cap := newReplayCapturingDispatcher(t)
	secret := "replay-secret"
	delivery := &models.WebhookDelivery{
		ID:        42,
		WebhookID: 5,
		UserID:    1,
		Event:     "email.sent",
		Status:    models.WebhookDeliveryFailed,
		Attempt:   3,
		Payload:   `{"event":"email.sent"}`,
	}
	wh := models.Webhook{ID: 5, UserID: 1, URL: srv.URL, Secret: secret}

	result, err := d.ReplayDelivery(wh, delivery, []byte(delivery.Payload))
	if err != nil {
		t.Fatalf("ReplayDelivery error: %v", err)
	}
	if result.Status != models.WebhookDeliverySuccess || result.StatusCode != http.StatusOK {
		t.Errorf("replay result = %s/%d, want success/200", result.Status, result.StatusCode)
	}
	if result.Attempt != 4 {
		t.Errorf("replay attempt = %d, want 4 (prior max 3 + 1)", result.Attempt)
	}

	attempts := cap.allAttempts()
	if len(attempts) != 1 {
		t.Fatalf("attempt rows = %d, want 1", len(attempts))
	}
	a := attempts[0]
	if a.DeliveryID != 42 || a.Attempt != 4 || a.StatusCode != http.StatusOK || a.ResponseBody != responseBody {
		t.Errorf("attempt row = %+v", a)
	}

	updates := cap.allUpdates()
	if len(updates) != 1 {
		t.Fatalf("delivery updates = %d, want 1", len(updates))
	}
	u := updates[0]
	if !strings.Contains(u.sql, `"webhook_deliveries"`) {
		t.Errorf("update SQL = %q, want a webhook_deliveries update", u.sql)
	}
	if !strings.Contains(u.sql, `"status"`) || !strings.Contains(u.sql, `"http_status_code"`) {
		t.Errorf("update SQL = %q, want the outcome columns", u.sql)
	}
	// The delivery's own Attempt field must be untouched (the original attempt
	// count is the replay rate-limit's baseline).
	if delivery.Attempt != 3 {
		t.Errorf("delivery.Attempt = %d, want unchanged 3", delivery.Attempt)
	}
}

// TestReplayDeliveryFailurePath pins the failed replay: the outcome is
// recorded as failed with the sanitized error, and the attempt row carries it.
func TestReplayDeliveryFailurePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d, cap := newReplayCapturingDispatcher(t)
	delivery := &models.WebhookDelivery{ID: 43, WebhookID: 6, UserID: 1, Status: models.WebhookDeliveryFailed, Attempt: 2}
	wh := models.Webhook{ID: 6, UserID: 1, URL: srv.URL}

	result, err := d.ReplayDelivery(wh, delivery, []byte(`{"event":"email.failed"}`))
	if err != nil {
		t.Fatalf("ReplayDelivery error: %v", err)
	}
	if result.Status != models.WebhookDeliveryFailed || result.StatusCode != http.StatusInternalServerError {
		t.Errorf("replay result = %s/%d, want failed/500", result.Status, result.StatusCode)
	}
	if result.Error != "HTTP 500" {
		t.Errorf("replay error = %q, want HTTP 500", result.Error)
	}
	attempts := cap.allAttempts()
	if len(attempts) != 1 || attempts[0].Attempt != 3 || attempts[0].DeliveryID != 43 {
		t.Fatalf("attempt rows = %+v, want one row numbered 3 on delivery 43", attempts)
	}
	updates := cap.allUpdates()
	if len(updates) != 1 {
		t.Fatalf("delivery updates = %d, want 1", len(updates))
	}
	found := false
	for _, v := range updates[0].vars {
		// Statement.Vars keep the pre-driver Go value, so the typed
		// WebhookDeliveryStatus needs a string comparison, not a type assert.
		if fmt.Sprint(v) == string(models.WebhookDeliveryFailed) {
			found = true
		}
	}
	if !found {
		t.Errorf("update vars %v must carry the failed status", updates[0].vars)
	}
}

// TestReplayDeliveryPolicyBlockIsSanitized is the SSRF regression for the
// replay path: replaying a delivery whose webhook now points at a loopback
// destination must be rejected by the guarded transport (the SAME enforcement
// as live deliveries), recorded with the sanitized message, and must never
// reach the listener.
func TestReplayDeliveryPolicyBlockIsSanitized(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	gdb, cap := newReplayGorm(t)
	res := &stubResolver{addrs: []netip.Addr{ip(t, "127.0.0.1")}}
	d := newPermissiveDispatcher()
	d.urlPolicy = ValidateWebhookURL
	d.client = &http.Client{
		Timeout:   defaultTimeout,
		Transport: NewGuardedTransport(res),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	d.SetDeliveryRepo(repositories.NewWebhookDeliveryRepository(gdb.Session(&gorm.Session{DryRun: true})))
	d.SetAttemptRepo(repositories.NewWebhookDeliveryAttemptRepository(gdb.Session(&gorm.Session{DryRun: true})))

	delivery := &models.WebhookDelivery{ID: 44, WebhookID: 7, UserID: 1, Status: models.WebhookDeliveryFailed, Attempt: 1}
	// The stored URL resolves to loopback where the listener actually waits.
	wh := models.Webhook{ID: 7, UserID: 1, URL: "http://replay-blocked.test/hook"}

	result, err := d.ReplayDelivery(wh, delivery, []byte(`{"event":"email.sent"}`))
	if err != nil {
		t.Fatalf("ReplayDelivery error: %v", err)
	}
	if result.Status != models.WebhookDeliveryFailed {
		t.Errorf("replay status = %s, want failed", result.Status)
	}
	if result.Error != msgBlocked {
		t.Errorf("replay error = %q, want %q", result.Error, msgBlocked)
	}
	if strings.Contains(result.Error, "127.0.0.1") {
		t.Errorf("replay error %q must not leak the resolved address", result.Error)
	}
	if hits != 0 {
		t.Fatalf("listener received %d requests, want 0", hits)
	}
	if got := len(cap.allAttempts()); got != 1 {
		t.Fatalf("attempt rows = %d, want 1", got)
	}
}
