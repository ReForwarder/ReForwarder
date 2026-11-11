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

package observability

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// recordingCollector is an httptest webhook target plus a stub platform-event
// recorder, so hook emission is asserted on both channels (GAP-51: webhook +
// event row).
type recordingCollector struct {
	mu        sync.Mutex
	bodies    []ErrorEvent
	sigs      []string
	status    int
	events    []*models.Event
	failStore bool
}

func (r *recordingCollector) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var ev ErrorEvent
		_ = json.Unmarshal(body, &ev)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.bodies = append(r.bodies, ev)
		r.sigs = append(r.sigs, req.Header.Get("X-Posta-Signature"))
		if r.status != 0 {
			w.WriteHeader(r.status)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (r *recordingCollector) Create(event *models.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failStore {
		return errStoreDown
	}
	r.events = append(r.events, event)
	return nil
}

type storeDownError struct{}

func (storeDownError) Error() string { return "store down" }

var errStoreDown = storeDownError{}

func (r *recordingCollector) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *recordingCollector) eventCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// waitFor polls cond for up to 2s (panic posts are asynchronous).
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached within 2s")
}

func TestErrorHookDisabledWithoutURL(t *testing.T) {
	h := NewErrorHook("", "", "posta", "test", nil)
	if h.Active() {
		t.Fatal("hook with empty URL must be inactive (default-off guarantee)")
	}
	// Captures are no-ops, not panics.
	h.CapturePanic("boom")
	h.CaptureFatal("bye")
}

// TestErrorHookEmitsPanicEvent (GAP-51): a recovered panic is shipped as a
// runtime.error JSON event with kind=panic, a stack, and — when a secret is
// configured — a valid X-Posta-Signature HMAC. The platform-events copy is
// recorded with type runtime.panic, category system.
func TestErrorHookEmitsPanicEvent(t *testing.T) {
	collector := &recordingCollector{}
	srv := httptest.NewServer(collector.handler())
	defer srv.Close()

	const secret = "hook-secret"
	h := NewErrorHook(srv.URL, secret, "posta", "9.9.9-test", collector)
	if !h.Active() {
		t.Fatal("hook with URL must be active")
	}

	h.CapturePanic("runtime error: index out of range [5]")
	waitFor(t, func() bool { return collector.count() > 0 })

	// Copy what we need out under the lock, then RELEASE it before the next
	// waitFor — the recorder mutex guards both channels, and waitFor would
	// deadlock against a lock this test still holds.
	collector.mu.Lock()
	ev := collector.bodies[0]
	sig := collector.sigs[0]
	collector.mu.Unlock()

	if ev.Event != "runtime.error" {
		t.Fatalf("event = %q, want runtime.error", ev.Event)
	}
	if ev.Kind != "panic" {
		t.Fatalf("kind = %q, want panic", ev.Kind)
	}
	if ev.Message != "runtime error: index out of range [5]" {
		t.Fatalf("message = %q", ev.Message)
	}
	if ev.Stack == "" {
		t.Fatal("panic event must carry a stack")
	}
	if ev.Service != "posta" || ev.Version != "9.9.9-test" {
		t.Fatalf("service/version = %q/%q", ev.Service, ev.Version)
	}
	if _, err := time.Parse(time.RFC3339, ev.Timestamp); err != nil {
		t.Fatalf("timestamp not RFC3339: %v", err)
	}

	// HMAC: re-sign the exact body the receiver saw. We re-marshal the decoded
	// event — json.Marshal is deterministic for this struct, so the signature
	// must match byte-for-byte.
	body, _ := json.Marshal(ev)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != want {
		t.Fatalf("signature = %q, want %q (same scheme as delivery webhooks)", sig, want)
	}

	// Platform-events copy.
	waitFor(t, func() bool { return collector.eventCount() > 0 })
	collector.mu.Lock()
	pe := collector.events[0]
	collector.mu.Unlock()
	if pe.Type != "runtime.panic" {
		t.Fatalf("platform event type = %q, want runtime.panic", pe.Type)
	}
	if pe.Category != models.EventCategorySystem {
		t.Fatalf("platform event category = %q, want system", pe.Category)
	}
	if pe.Outcome != "failure" {
		t.Fatalf("platform event outcome = %q, want failure", pe.Outcome)
	}
}

// TestErrorHookEmitsFatalEventSynchronously: CaptureFatal must have DELIVERED
// (or failed) by the time it returns — the process is about to exit.
func TestErrorHookEmitsFatalEventSynchronously(t *testing.T) {
	collector := &recordingCollector{}
	srv := httptest.NewServer(collector.handler())
	defer srv.Close()

	h := NewErrorHook(srv.URL, "", "posta", "9.9.9-test", collector)
	h.CaptureFatal("failed to run migrations: connection refused")

	// No wait loop: synchronous means already there.
	if collector.count() != 1 {
		t.Fatalf("webhook calls after CaptureFatal = %d, want 1 (synchronous delivery)", collector.count())
	}
	if collector.eventCount() != 1 {
		t.Fatalf("platform events after CaptureFatal = %d, want 1", collector.eventCount())
	}
	if collector.bodies[0].Kind != "fatal" {
		t.Fatalf("kind = %q, want fatal", collector.bodies[0].Kind)
	}
	if collector.events[0].Type != "runtime.fatal" {
		t.Fatalf("platform event type = %q, want runtime.fatal", collector.events[0].Type)
	}
}

// TestErrorHookStoreFailureDoesNotBlockDelivery: the events-table copy is
// best-effort — if the store fails (possibly the cause of the crash), the
// webhook must still have gone out.
func TestErrorHookStoreFailureDoesNotBlockDelivery(t *testing.T) {
	collector := &recordingCollector{failStore: true}
	srv := httptest.NewServer(collector.handler())
	defer srv.Close()

	h := NewErrorHook(srv.URL, "", "posta", "9.9.9-test", collector)
	h.CaptureFatal("store down fatal")
	if collector.count() != 1 {
		t.Fatalf("webhook calls = %d, want 1 despite the event-store failure", collector.count())
	}
}

// TestCaptureGlobalFatalWithoutHookIsNoOp: wiring sites run before/without
// SetErrorHook in tests and dev; they must be safe.
func TestCaptureGlobalFatalWithoutHookIsNoOp(t *testing.T) {
	prev := GlobalErrorHook()
	defer SetErrorHook(prev)
	SetErrorHook(nil)
	CaptureGlobalFatal("nothing installed — must not panic")
}
