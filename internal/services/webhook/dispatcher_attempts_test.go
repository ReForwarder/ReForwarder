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

package webhook

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The dispatcher's persistence runs on a gorm DryRun session: inserts build
// SQL but never execute, and a before-create callback captures the delivery
// and every attempt row. This observes the real deliverWithRetry path with no
// database, the same pattern the security detector tests use.

// attemptCapture collects what the dispatcher persists.
type attemptCapture struct {
	mu         sync.Mutex
	deliveries []models.WebhookDelivery
	attempts   []models.WebhookDeliveryAttempt
}

func (c *attemptCapture) addDelivery(d models.WebhookDelivery) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deliveries = append(c.deliveries, d)
}

func (c *attemptCapture) addAttempt(a models.WebhookDeliveryAttempt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts = append(c.attempts, a)
}

func (c *attemptCapture) deliveryCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.deliveries)
}

func (c *attemptCapture) attemptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.attempts)
}

func (c *attemptCapture) allAttempts() []models.WebhookDeliveryAttempt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]models.WebhookDeliveryAttempt(nil), c.attempts...)
}

func (c *attemptCapture) lastDelivery() models.WebhookDelivery {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.deliveries) == 0 {
		return models.WebhookDelivery{}
	}
	return c.deliveries[len(c.deliveries)-1]
}

type nopConnector struct{}

func (nopConnector) Connect(context.Context) (driver.Conn, error) { return nopConn{}, nil }
func (nopConnector) Driver() driver.Driver                        { return nopDriver{} }

type nopDriver struct{}

func (nopDriver) Open(string) (driver.Conn, error) { return nopConn{}, nil }

type nopConn struct{}

func (nopConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("nop conn") }
func (nopConn) Close() error                        { return nil }
func (nopConn) Begin() (driver.Tx, error)           { return nil, errors.New("nop conn") }

const capturedDeliveryID = 77

// newCapturingDispatcher returns a Dispatcher whose delivery and attempt
// writes run on DryRun gorm and are captured. The captured delivery is given
// the id the database would assign, so attempt rows can be checked against it.
func newCapturingDispatcher(t *testing.T) (*Dispatcher, *attemptCapture) {
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
	cap := &attemptCapture{}
	if err := gdb.Callback().Create().Before("gorm:create").Register("webhook-test:capture-create", func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *models.WebhookDelivery:
			dest.ID = capturedDeliveryID
			cap.addDelivery(*dest)
		case *models.WebhookDeliveryAttempt:
			cap.addAttempt(*dest)
		}
	}); err != nil {
		t.Fatalf("register create capture: %v", err)
	}

	d := newPermissiveDispatcher()
	d.SetDeliveryRepo(repositories.NewWebhookDeliveryRepository(gdb.Session(&gorm.Session{DryRun: true})))
	d.SetAttemptRepo(repositories.NewWebhookDeliveryAttemptRepository(gdb.Session(&gorm.Session{DryRun: true})))
	return d, cap
}

// newGuardedCapturingDispatcher returns the capturing dispatcher in its
// production posture: the real static URL gate and the guarded transport, with
// res injected so the guarded dialer's resolution is fully controlled. Used by
// the end-to-end SSRF policy tests.
func newGuardedCapturingDispatcher(t *testing.T, res Resolver) (*Dispatcher, *attemptCapture) {
	t.Helper()
	d, cap := newCapturingDispatcher(t)
	d.urlPolicy = ValidateWebhookURL
	d.client = &http.Client{
		Timeout:   defaultTimeout,
		Transport: NewGuardedTransport(res),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return d, cap
}

func deliverBody(t *testing.T, event, emailID string) []byte {
	t.Helper()
	body, err := json.Marshal(Payload{Event: event, EmailID: emailID, Timestamp: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return body
}

// TestDeliverWithRetryRecordsAttemptsOnSuccess pins the success path: the
// first accepted delivery is recorded AND its attempt row is kept — success is
// part of the retry story, not an exception to it.
func TestDeliverWithRetryRecordsAttemptsOnSuccess(t *testing.T) {
	const responseBody = `{"received":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	}))
	defer srv.Close()

	d, cap := newCapturingDispatcher(t)
	d.maxRetries = 3

	wh := models.Webhook{ID: 5, UserID: 1, URL: srv.URL, Secret: "s"}
	body := deliverBody(t, "email.sent", "abc")
	d.deliverWithRetry(1, wh, body, "email.sent")

	if cap.deliveryCount() != 1 {
		t.Fatalf("deliveries recorded = %d, want 1", cap.deliveryCount())
	}
	delivery := cap.lastDelivery()
	if delivery.Status != models.WebhookDeliverySuccess {
		t.Errorf("delivery status = %q, want success", delivery.Status)
	}
	if delivery.HTTPStatusCode != http.StatusOK || delivery.Attempt != 1 {
		t.Errorf("delivery http status/attempt = %d/%d, want 200/1", delivery.HTTPStatusCode, delivery.Attempt)
	}
	if delivery.WebhookID != 5 || delivery.Event != "email.sent" || delivery.UserID != 1 {
		t.Errorf("delivery = %+v", delivery)
	}

	if cap.attemptCount() != 1 {
		t.Fatalf("attempt rows = %d, want 1", cap.attemptCount())
	}
	attempt := cap.allAttempts()[0]
	if attempt.Attempt != 1 {
		t.Errorf("attempt number = %d, want 1", attempt.Attempt)
	}
	if attempt.StatusCode != http.StatusOK {
		t.Errorf("attempt status code = %d, want 200", attempt.StatusCode)
	}
	if attempt.ResponseBody != responseBody {
		t.Errorf("attempt body = %q, want %q", attempt.ResponseBody, responseBody)
	}
	if attempt.Error != "" {
		t.Errorf("successful attempt must carry no error, got %q", attempt.Error)
	}
	if attempt.DeliveryID != capturedDeliveryID {
		t.Errorf("attempt delivery_id = %d, want %d (linked to the recorded delivery)", attempt.DeliveryID, capturedDeliveryID)
	}
}

// TestDeliverWithRetryRecordsAttemptsOnFailure pins the retry path: every
// HTTP attempt gets its own row with the status code and the body the endpoint
// answered, and the final delivery summarizes the exhaustion.
func TestDeliverWithRetryRecordsAttemptsOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	d, cap := newCapturingDispatcher(t)
	d.maxRetries = 2 // one 2s backoff between the two attempts

	wh := models.Webhook{ID: 6, UserID: 1, URL: srv.URL}
	// A non-default event type keeps the payload helper's parameter honest
	// and proves attempt recording is event-agnostic.
	d.deliverWithRetry(1, wh, deliverBody(t, "email.bounced", "bounce-e2e"), "email.bounced")

	if cap.deliveryCount() != 1 {
		t.Fatalf("deliveries recorded = %d, want 1", cap.deliveryCount())
	}
	delivery := cap.lastDelivery()
	if delivery.Status != models.WebhookDeliveryFailed {
		t.Errorf("delivery status = %q, want failed", delivery.Status)
	}
	if delivery.HTTPStatusCode != http.StatusInternalServerError {
		t.Errorf("delivery http status = %d, want 500", delivery.HTTPStatusCode)
	}
	if delivery.Attempt != 2 || delivery.ErrorMessage != "HTTP 500" {
		t.Errorf("delivery attempt/error = %d/%q, want 2/HTTP 500", delivery.Attempt, delivery.ErrorMessage)
	}

	attempts := cap.allAttempts()
	if len(attempts) != 2 {
		t.Fatalf("attempt rows = %d, want 2", len(attempts))
	}
	for i, attempt := range attempts {
		if attempt.Attempt != i+1 {
			t.Errorf("attempt #%d numbered %d", i+1, attempt.Attempt)
		}
		if attempt.StatusCode != http.StatusInternalServerError {
			t.Errorf("attempt #%d status = %d, want 500", i+1, attempt.StatusCode)
		}
		if attempt.ResponseBody != "boom" {
			t.Errorf("attempt #%d body = %q, want boom", i+1, attempt.ResponseBody)
		}
		if attempt.DeliveryID != capturedDeliveryID {
			t.Errorf("attempt #%d delivery_id = %d, want %d", i+1, attempt.DeliveryID, capturedDeliveryID)
		}
	}
}

// TestDeliverWithRetryTruncatesAttemptBodies pins the 2KB cap on the response
// body kept per attempt.
func TestDeliverWithRetryTruncatesAttemptBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 3000))
	}))
	defer srv.Close()

	d, cap := newCapturingDispatcher(t)
	d.maxRetries = 1

	d.deliverWithRetry(1, models.Webhook{ID: 7, UserID: 1, URL: srv.URL},
		deliverBody(t, "email.sent", "abc"), "email.sent")

	attempts := cap.allAttempts()
	if len(attempts) != 1 {
		t.Fatalf("attempt rows = %d, want 1", len(attempts))
	}
	if got := len(attempts[0].ResponseBody); got != maxAttemptBodyBytes {
		t.Errorf("attempt body length = %d, want %d (2KB cap)", got, maxAttemptBodyBytes)
	}
	if !strings.HasPrefix(attempts[0].ResponseBody, "xxx") {
		t.Error("truncated body must keep the response prefix")
	}
}

// TestDeliverWithRetryRecordsConnectionErrors pins the transport-failure path:
// no HTTP status exists, the attempt row keeps the error text, and the
// delivery is recorded as failed.
func TestDeliverWithRetryRecordsConnectionErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := srv.URL
	srv.Close() // nothing listens anymore: every attempt fails at transport level

	d, cap := newCapturingDispatcher(t)
	d.maxRetries = 1

	d.deliverWithRetry(1, models.Webhook{ID: 8, UserID: 1, URL: endpoint},
		deliverBody(t, "email.sent", "abc"), "email.sent")

	if cap.deliveryCount() != 1 {
		t.Fatalf("deliveries recorded = %d, want 1", cap.deliveryCount())
	}
	delivery := cap.lastDelivery()
	if delivery.Status != models.WebhookDeliveryFailed || delivery.HTTPStatusCode != 0 {
		t.Errorf("delivery = status %q http %d, want failed/0", delivery.Status, delivery.HTTPStatusCode)
	}
	if delivery.ErrorMessage == "" {
		t.Error("delivery must carry the transport error")
	}

	attempts := cap.allAttempts()
	if len(attempts) != 1 {
		t.Fatalf("attempt rows = %d, want 1", len(attempts))
	}
	if attempts[0].StatusCode != 0 {
		t.Errorf("attempt status = %d, want 0 (no HTTP response)", attempts[0].StatusCode)
	}
	if attempts[0].Error == "" {
		t.Error("attempt row must keep the transport error")
	}
	if attempts[0].ResponseBody != "" {
		t.Errorf("attempt body = %q, want empty", attempts[0].ResponseBody)
	}
}

// TestDeliverWithRetryWithoutReposStillDelivers pins the additive-safety
// contract: recording is optional — without the repos delivery still works,
// callbacks still fire, nothing panics.
func TestDeliverWithRetryWithoutReposStillDelivers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := newPermissiveDispatcher()
	d.maxRetries = 1

	var succeeded, done int
	d.OnDeliverySuccess(func() { succeeded++ })
	d.OnDeliveryDone(func(float64) { done++ })

	d.deliverWithRetry(1, models.Webhook{ID: 9, UserID: 1, URL: srv.URL},
		deliverBody(t, "email.sent", "abc"), "email.sent")

	if succeeded != 1 || done != 1 {
		t.Errorf("callbacks = success %d, done %d, want 1/1", succeeded, done)
	}
}

// TestRecordAttemptsSkipsWithoutDelivery pins that attempt rows are never
// orphaned: when the delivery could not be recorded they are skipped.
func TestRecordAttemptsSkipsWithoutDelivery(t *testing.T) {
	d, cap := newCapturingDispatcher(t)

	d.recordAttempts(nil, []models.WebhookDeliveryAttempt{{Attempt: 1, StatusCode: 200}})

	if cap.attemptCount() != 0 {
		t.Errorf("no delivery recorded: attempt rows must be skipped, got %d", cap.attemptCount())
	}

	// Without an attempt repo there is nothing to write either.
	d2 := NewDispatcher(nil)
	d2.recordAttempts(&models.WebhookDelivery{ID: 1}, []models.WebhookDeliveryAttempt{{Attempt: 1}})
}

// TestDeliverPolicyBlockFailsFast is the end-to-end SSRF regression for the
// dispatcher: a webhook whose hostname resolves (via a lying stub resolver) to
// loopback — where a real server is listening — must be rejected by the
// guarded transport, recorded exactly once with the sanitized policy message,
// and NOT retried (policy rejections are deterministic; retrying would only
// repeat them). The listening server must see nothing.
func TestDeliverPolicyBlockFailsFast(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := &stubResolver{addrs: []netip.Addr{ip(t, "127.0.0.1")}}
	d, cap := newGuardedCapturingDispatcher(t, res)
	d.maxRetries = 3 // a retried block would sleep 2s + 4s in backoff

	wh := models.Webhook{ID: 10, UserID: 1, URL: "http://blocked.test/hook", Secret: "s"}
	start := time.Now()
	d.deliverWithRetry(1, wh, deliverBody(t, "email.sent", "blocked"), "email.sent")
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("delivery took %v; a policy block must fail fast without retry backoff", elapsed)
	}

	if cap.deliveryCount() != 1 {
		t.Fatalf("deliveries recorded = %d, want 1", cap.deliveryCount())
	}
	delivery := cap.lastDelivery()
	if delivery.Status != models.WebhookDeliveryFailed {
		t.Errorf("delivery status = %q, want failed", delivery.Status)
	}
	if delivery.ErrorMessage != msgBlocked {
		t.Errorf("delivery error = %q, want %q", delivery.ErrorMessage, msgBlocked)
	}
	if strings.Contains(delivery.ErrorMessage, "127.0.0.1") {
		t.Errorf("recorded error %q must not leak the resolved address", delivery.ErrorMessage)
	}

	attempts := cap.allAttempts()
	if len(attempts) != 1 {
		t.Fatalf("attempt rows = %d, want 1 (fail fast: no retries after a policy block)", len(attempts))
	}
	if attempts[0].Attempt != 1 {
		t.Errorf("attempt number = %d, want 1", attempts[0].Attempt)
	}
	if attempts[0].StatusCode != 0 {
		t.Errorf("attempt status = %d, want 0 (no HTTP response)", attempts[0].StatusCode)
	}
	if attempts[0].Error != msgBlocked {
		t.Errorf("attempt error = %q, want %q", attempts[0].Error, msgBlocked)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("test server received %d requests, want 0", got)
	}
}
