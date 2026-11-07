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

// SF-90: optional batch delivery and a per-endpoint pending cap.
//
// By default every event is delivered immediately as its own HTTP POST
// (per-event behavior — the historical and still-default mode). When batching
// is enabled (POSTA_WEBHOOK_BATCH_ENABLED=true), events for the same webhook
// endpoint are grouped: the buffer flushes as ONE delivery when it holds
// BatchMaxEvents events (default 25) or when BatchFlushInterval elapses,
// whichever comes first. The batch body is a JSON array of the per-event
// payloads, HMAC-signed over the batch bytes, so the existing
// X-Posta-Signature verification applies unchanged to the raw request body.
// One webhook_deliveries row is recorded per batch flush, with the event
// column set to "batch".
//
// Independent of batching mode, a per-endpoint pending cap bounds how many
// events may sit queued for one webhook. When the cap is reached the OLDEST
// queued event is dropped, a warning is logged, and the
// posta_webhook_events_dropped_total metric is incremented (see the
// OnEventsDropped callback). With batching off the cap is inert — per-event
// dispatch has no pending backlog.

import (
	"encoding/json"
	"time"

	"github.com/jkaninda/logger"

	"github.com/goposta/posta/internal/models"
)

const (
	defaultBatchMaxEvents   = 25
	defaultBatchFlush       = 5 * time.Second
	defaultMaxPendingEvents = 1000

	// batchEventName is the delivery-record event label for a batch flush.
	batchEventName = "batch"
)

// batchEvent is one payload waiting in an endpoint's batch buffer.
type batchEvent struct {
	body  []byte
	event string
}

// batchBuffer accumulates events for a single webhook endpoint between
// flushes.
type batchBuffer struct {
	wh     models.Webhook
	userID uint
	events []batchEvent
	timer  *time.Timer
}

// dispatchBatched queues one event for batch delivery to wh (SF-90). The
// buffer flushes at BatchMaxEvents or on the flush-interval timer, whichever
// first; the oldest event is dropped when the pending cap is reached.
func (d *Dispatcher) dispatchBatched(userID uint, wh models.Webhook, event string, body []byte) {
	d.batchMu.Lock()
	if d.batches == nil {
		d.batches = make(map[uint]*batchBuffer)
	}
	buf, ok := d.batches[wh.ID]
	if !ok {
		buf = &batchBuffer{wh: wh, userID: userID}
		d.batches[wh.ID] = buf
		buf.timer = time.AfterFunc(d.batchFlushInterval, func() { d.flushBatch(wh.ID) })
	}
	if len(buf.events) >= d.maxPendingEvents {
		// Per-endpoint pending cap (SF-90): drop the oldest queued event so a
		// stalled endpoint cannot grow the buffer without bound.
		dropped := buf.events[0]
		buf.events = buf.events[1:]
		logger.Warn("webhook event dropped: pending cap reached", "webhook_id", wh.ID, "event", dropped.event, "cap", d.maxPendingEvents)
		if d.onEventsDropped != nil {
			// Callbacks run under the lock like the timer path; they must be
			// cheap (the metric increment is).
			d.onEventsDropped()
		}
	}
	buf.events = append(buf.events, batchEvent{body: body, event: event})
	flushNow := len(buf.events) >= d.batchMaxEvents
	d.batchMu.Unlock()

	if flushNow {
		d.flushBatch(wh.ID)
	}
}

// flushBatch takes ownership of an endpoint's pending events and delivers them
// as one batch. Buffer removal (and its timer) is atomic with the snapshot, so
// an event enqueued after the snapshot starts a fresh buffer with a fresh
// timer and can never be dropped with the flush.
func (d *Dispatcher) flushBatch(webhookID uint) {
	d.batchMu.Lock()
	buf, ok := d.batches[webhookID]
	if !ok {
		d.batchMu.Unlock()
		return
	}
	events := buf.events
	if buf.timer != nil {
		buf.timer.Stop()
	}
	delete(d.batches, webhookID)
	d.batchMu.Unlock()

	if len(events) == 0 {
		return
	}
	d.deliverBatch(buf.userID, buf.wh, events)
}

// deliverBatch sends one batch: a JSON array of the per-event payload
// bodies, delivered through the same guarded post() path (SSRF policy,
// HMAC signature over the batch bytes, retries) as single-event deliveries.
func (d *Dispatcher) deliverBatch(userID uint, wh models.Webhook, events []batchEvent) {
	bodies := make([]json.RawMessage, 0, len(events))
	for _, ev := range events {
		bodies = append(bodies, json.RawMessage(ev.body))
	}
	batchBody, err := json.Marshal(bodies)
	if err != nil {
		logger.Error("failed to marshal webhook batch payload", "error", err)
		return
	}
	logger.Info("webhook batch flush", "webhook_id", wh.ID, "events", len(events))
	d.deliverWithRetry(userID, wh, batchBody, batchEventName)
}

// Close stops every pending batch timer and synchronously delivers what is
// still buffered — a graceful-shutdown helper so batching does not silently
// swallow in-flight events on exit. Safe to call more than once.
func (d *Dispatcher) Close() {
	d.batchMu.Lock()
	ids := make([]uint, 0, len(d.batches))
	for id := range d.batches {
		ids = append(ids, id)
	}
	d.batchMu.Unlock()
	for _, id := range ids {
		d.flushBatch(id)
	}
}
