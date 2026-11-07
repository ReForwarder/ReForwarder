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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// SF-90 batch delivery + per-endpoint pending cap tests. The per-event
// default behavior is covered by the existing dispatcher tests; these pin the
// opt-in batch mode and the cap's drop-oldest accounting.

// batchServer collects every POST body it receives.
type batchServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	bodys [][]byte
}

func newBatchServer(t *testing.T) *batchServer {
	t.Helper()
	bs := &batchServer{}
	bs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		bs.mu.Lock()
		bs.bodys = append(bs.bodys, buf)
		bs.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(bs.srv.Close)
	return bs
}

func (bs *batchServer) count() int {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return len(bs.bodys)
}

func (bs *batchServer) lastBody() []byte {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.bodys[len(bs.bodys)-1]
}

// waitFor polls cond until it holds or the deadline passes; the dispatcher
// flushes asynchronously.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

func TestBatchingDisabledByDefaultDeliversPerEvent(t *testing.T) {
	bs := newBatchServer(t)
	wh := models.Webhook{ID: 1, UserID: 1, URL: bs.srv.URL, Events: []string{"email.sent"}}

	d := newPermissiveDispatcher()
	// No SetConfig call: the zero-value dispatcher must stay per-event. The
	// two calls are exactly what DispatchJSON's default branch issues for two
	// events — one HTTP delivery each, no coalescing.
	d.deliverWithRetry(1, wh, []byte(`{"event":"email.sent","email_id":"a"}`), "email.sent")
	d.deliverWithRetry(1, wh, []byte(`{"event":"email.sent","email_id":"b"}`), "email.sent")

	if bs.count() != 2 {
		t.Fatalf("per-event mode delivered %d requests, want 2", bs.count())
	}
}

func TestBatchingCoalescesEventsPerEndpoint(t *testing.T) {
	bs := newBatchServer(t)
	wh := models.Webhook{ID: 1, UserID: 1, URL: bs.srv.URL, Events: []string{"email.sent"}}

	d := newPermissiveDispatcher()
	d.SetConfig(Config{BatchEnabled: true, BatchFlushInterval: time.Hour}) // nothing flushes on the interval here
	for _, id := range []string{"a", "b", "c"} {
		d.dispatchBatched(1, wh, "email.sent", []byte(`{"event":"email.sent","email_id":"`+id+`"}`))
	}
	// Close flushes the buffer synchronously — below BatchMaxEvents and
	// before the interval, proving the events were held, not delivered.
	d.Close()

	time.Sleep(100 * time.Millisecond) // let any stray second request show up
	if got := bs.count(); got != 1 {
		t.Fatalf("batch mode delivered %d requests, want 1 (coalesced)", got)
	}

	var batch []json.RawMessage
	if err := json.Unmarshal(bs.lastBody(), &batch); err != nil {
		t.Fatalf("batch body is not a JSON array: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("batch carried %d events, want 3", len(batch))
	}
}

func TestBatchingFlushesOnInterval(t *testing.T) {
	bs := newBatchServer(t)
	wh := models.Webhook{ID: 1, UserID: 1, URL: bs.srv.URL, Events: []string{"email.sent"}}

	d := newPermissiveDispatcher()
	d.SetConfig(Config{BatchEnabled: true, BatchMaxEvents: 25, BatchFlushInterval: 50 * time.Millisecond})
	t.Cleanup(d.Close)

	d.dispatchBatched(1, wh, "email.sent", []byte(`{"event":"email.sent","email_id":"solo"}`))

	waitFor(t, func() bool { return bs.count() >= 1 })
	var batch []json.RawMessage
	if err := json.Unmarshal(bs.lastBody(), &batch); err != nil {
		t.Fatalf("batch body is not a JSON array: %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("interval flush carried %d events, want 1", len(batch))
	}
}

func TestBatchingSignsBatchBody(t *testing.T) {
	var gotSig string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Posta-Signature")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = buf
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := models.Webhook{ID: 1, UserID: 1, URL: srv.URL, Events: []string{"email.sent"}, Secret: "batch-secret"}

	d := newPermissiveDispatcher()
	d.SetConfig(Config{BatchEnabled: true, BatchFlushInterval: time.Hour})
	d.dispatchBatched(1, wh, "email.sent", []byte(`{"event":"email.sent","email_id":"s"}`))
	d.Close() // synchronous flush for the assertion below
	waitFor(t, func() bool { return gotBody != nil })

	want := "sha256=" + signPayload("batch-secret", gotBody)
	if gotSig != want {
		t.Fatalf("signature = %q, want signature over the batch bytes (%q)", gotSig, want)
	}
}

func TestPendingCapDropsOldestAndFiresCallback(t *testing.T) {
	bs := newBatchServer(t)
	wh := models.Webhook{ID: 1, UserID: 1, URL: bs.srv.URL, Events: []string{"email.sent"}}

	var dropped atomic.Int32
	d := newPermissiveDispatcher()
	// Cap 3, batch max 10 (never size-flushes), interval long enough that the
	// four events sit in the buffer together.
	d.SetConfig(Config{BatchEnabled: true, BatchMaxEvents: 10, BatchFlushInterval: time.Hour, MaxPendingEvents: 3})
	d.OnEventsDropped(func() { dropped.Add(1) })
	t.Cleanup(d.Close)

	for _, id := range []string{"keep-1", "keep-2", "keep-3", "drops"} {
		d.dispatchBatched(1, wh, "email.sent", []byte(`{"event":"email.sent","email_id":"`+id+`"}`))
	}

	if dropped.Load() != 1 {
		t.Fatalf("drop callback fired %d times, want 1", dropped.Load())
	}

	// The surviving buffer must hold the three NEWEST events, in order — the
	// cap keeps the newest MaxPendingEvents and drops the oldest.
	d.batchMu.Lock()
	buf := d.batches[1]
	if len(buf.events) != 3 {
		d.batchMu.Unlock()
		t.Fatalf("buffer holds %d events, want 3", len(buf.events))
	}
	first := string(buf.events[0].body)
	last := string(buf.events[2].body)
	d.batchMu.Unlock()
	if first != `{"event":"email.sent","email_id":"keep-2"}` || last != `{"event":"email.sent","email_id":"drops"}` {
		t.Fatalf("buffer does not hold the newest events: first=%s last=%s", first, last)
	}

	// Closing the dispatcher flushes what remains.
	d.Close()
	waitFor(t, func() bool { return bs.count() >= 1 })
	var batch []json.RawMessage
	if err := json.Unmarshal(bs.lastBody(), &batch); err != nil {
		t.Fatalf("flushed body is not a JSON array: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("Close flushed %d events, want the 3 survivors", len(batch))
	}
}

func TestPendingCapDropsPerEndpointIndependently(t *testing.T) {
	srvA := newBatchServer(t)
	srvB := newBatchServer(t)
	whA := models.Webhook{ID: 1, UserID: 1, URL: srvA.srv.URL, Events: []string{"email.sent"}}
	whB := models.Webhook{ID: 2, UserID: 1, URL: srvB.srv.URL, Events: []string{"email.sent"}}

	d := newPermissiveDispatcher()
	d.SetConfig(Config{BatchEnabled: true, BatchMaxEvents: 10, BatchFlushInterval: time.Hour, MaxPendingEvents: 1})
	t.Cleanup(d.Close)

	// Two events per endpoint: the second push on each endpoint drops that
	// endpoint's oldest; the endpoints' buffers are independent.
	d.dispatchBatched(1, whA, "email.sent", []byte(`{"email_id":"a1"}`))
	d.dispatchBatched(1, whA, "email.sent", []byte(`{"email_id":"a2"}`))
	d.dispatchBatched(1, whB, "email.sent", []byte(`{"email_id":"b1"}`))
	d.dispatchBatched(1, whB, "email.sent", []byte(`{"email_id":"b2"}`))

	d.batchMu.Lock()
	lenA := len(d.batches[1].events)
	lenB := len(d.batches[2].events)
	d.batchMu.Unlock()
	if lenA != 1 || lenB != 1 {
		t.Fatalf("buffer sizes after cap: A=%d B=%d, want 1 and 1", lenA, lenB)
	}
}
