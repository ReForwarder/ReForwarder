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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/jkaninda/logger"
)

// GAP-51 error-monitoring hook: POSTA_ERROR_WEBHOOK_URL ships runtime
// panics and fatal exits as events — one JSON POST to the configured URL and
// one best-effort row in the platform `events` table so the admin activity
// timeline records the incident even when the webhook target is down.
// Disabled by default: an unset POSTA_ERROR_WEBHOOK_URL means the hook never
// sends anything.
//
// Transport trust class: the URL is operator infrastructure configured
// through the environment (POSTA_ERROR_WEBHOOK_URL + optional
// POSTA_ERROR_WEBHOOK_SECRET), the same trust class as POSTA_DB_HOST or the
// OTLP endpoint — so it is NOT routed through the tenant-webhook SSRF guard
// (internal/services/webhook.ValidateWebhookURL), which exists for URLs
// less-privileged actors can store in the database and which would block the
// common self-hosted on-private-network error collector. The guard itself is
// untouched. When a secret is configured the body is signed with the SAME
// HMAC-SHA256 scheme the webhook dispatcher uses (X-Posta-Signature:
// sha256=<hex>, via webhook.SignBody), so receivers verify both event
// streams with one code path.
//
// Coverage (wired at): the HTTP API panic path (CapturePanicsMiddleware,
// re-panics so request-handling behavior is unchanged) and the logger.Fatal
// call sites in cmd/posta (CaptureGlobalFatal before the process exits —
// synchronous, because a fatal is about to leave the building).
// SMTP/IMAP/POP3 session-level recover() sites keep their local logging;
// hooking them is a documented residual, not silently claimed as covered.

const (
	// fatalPostTimeout bounds the synchronous fatal POST: the process is
	// dying, but it must not hang forever on a dead collector.
	fatalPostTimeout = 3 * time.Second
	// panicPostTimeout bounds the asynchronous panic POST.
	panicPostTimeout = 5 * time.Second
	// maxStackBytes caps the embedded stack trace so one pathological panic
	// cannot produce a multi-megabyte payload.
	maxStackBytes = 16 * 1024
)

// ErrorEvent is the JSON body POSTed for one panic or fatal.
type ErrorEvent struct {
	Event     string `json:"event"` // always "runtime.error"
	Kind      string `json:"kind"`  // "panic" | "fatal"
	Message   string `json:"message"`
	Stack     string `json:"stack,omitempty"`
	Timestamp string `json:"timestamp"`
	Service   string `json:"service"`
	Version   string `json:"version,omitempty"`
}

// ErrorEventRecorder persists the platform-events copy of the incident
// (models.Event, category system). *repositories.EventRepository satisfies
// it; tests stub it. An interface keeps the storage layer out of the hook's
// signature and the tests hermetic.
type ErrorEventRecorder interface {
	Create(event *models.Event) error
}

// ErrorHook ships panics and fatals to POSTA_ERROR_WEBHOOK_URL.
type ErrorHook struct {
	url      string
	secret   string
	service  string
	version  string
	client   *http.Client
	recorder ErrorEventRecorder
	now      func() time.Time // test seam
}

// NewErrorHook builds a hook. url empty → disabled hook (every capture is a
// no-op); callers wire it unconditionally.
func NewErrorHook(url, secret, service, version string, recorder ErrorEventRecorder) *ErrorHook {
	return &ErrorHook{
		url:      url,
		secret:   secret,
		service:  service,
		version:  version,
		client:   &http.Client{Timeout: panicPostTimeout},
		recorder: recorder,
		now:      time.Now,
	}
}

// Active reports whether the hook has somewhere to send to.
func (h *ErrorHook) Active() bool { return h != nil && h.url != "" }

// CapturePanic records a recovered panic. Non-blocking: the POST runs on its
// own goroutine — recovering from a request panic must not add latency. Never
// re-panics; the caller keeps responsibility for its own recover flow.
func (h *ErrorHook) CapturePanic(recovered any) {
	h.capture("panic", fmt.Sprintf("%v", recovered), false)
}

// CaptureFatal records a fatal exit. SYNCHRONOUS with a short timeout: after
// this returns the process is expected to exit, so the send must have landed
// (or failed) first.
func (h *ErrorHook) CaptureFatal(message string) {
	h.capture("fatal", message, true)
}

func (h *ErrorHook) capture(kind, message string, synchronous bool) {
	if !h.Active() {
		return
	}
	stack := debug.Stack()
	if len(stack) > maxStackBytes {
		stack = stack[:maxStackBytes]
	}
	event := ErrorEvent{
		Event:     "runtime.error",
		Kind:      kind,
		Message:   message,
		Stack:     string(stack),
		Timestamp: h.now().UTC().Format(time.RFC3339),
		Service:   h.service,
		Version:   h.version,
	}

	if synchronous {
		h.send(event, fatalPostTimeout)
	} else {
		go h.send(event, panicPostTimeout)
	}
}

// send POSTs the event and writes the best-effort platform event row. A
// webhook failure is logged, never propagated: the monitor must not become a
// new failure mode.
func (h *ErrorHook) send(event ErrorEvent, timeout time.Duration) {
	body, err := json.Marshal(event)
	if err != nil {
		logger.Error("error hook: failed to marshal error event", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		logger.Error("error hook: invalid webhook URL", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Posta-ErrorHook/1.0")
	if h.secret != "" {
		req.Header.Set("X-Posta-Signature", "sha256="+webhook.SignBody(h.secret, body))
	}

	resp, err := h.client.Do(req)
	if err != nil {
		logger.Warn("error hook: delivery failed", "kind", event.Kind, "error", err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			logger.Warn("error hook: webhook answered with error status", "kind", event.Kind, "status", resp.StatusCode)
		}
	}

	h.recordEvent(event)
}

// recordEvent writes the platform-events copy (best effort: if the database
// is what crashed the process, this fails and is logged).
func (h *ErrorHook) recordEvent(event ErrorEvent) {
	if h.recorder == nil {
		return
	}
	meta, _ := json.Marshal(map[string]string{"kind": event.Kind, "service": event.Service, "version": event.Version})
	rec := &models.Event{
		Category:   models.EventCategorySystem,
		Type:       "runtime." + event.Kind,
		Message:    event.Message,
		Metadata:   string(meta),
		Outcome:    "failure",
		TargetType: "process",
		CreatedAt:  h.now().UTC(),
	}
	if err := h.recorder.Create(rec); err != nil {
		logger.Warn("error hook: failed to record platform event", "error", err)
	}
}

// ---- process-wide wiring ------------------------------------------------

var (
	globalHookMu sync.RWMutex
	globalHook   *ErrorHook
)

// SetErrorHook installs the process-wide hook. Called once from cmd/posta
// before any code that can panic or exit fatally; the routes middleware and
// the cmd/posta fatal sites read it back through CapturePanicsMiddleware and
// CaptureGlobalFatal.
func SetErrorHook(h *ErrorHook) {
	globalHookMu.Lock()
	globalHook = h
	globalHookMu.Unlock()
}

// GlobalErrorHook returns the installed hook (nil when none).
func GlobalErrorHook() *ErrorHook {
	globalHookMu.RLock()
	defer globalHookMu.RUnlock()
	return globalHook
}

// CaptureGlobalFatal forwards a fatal to the installed hook, if any. Safe to
// call with no hook installed (no-op). This is what cmd/posta calls right
// before logger.Fatal.
func CaptureGlobalFatal(message string) {
	if h := GlobalErrorHook(); h != nil {
		h.CaptureFatal(message)
	}
}
