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
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package memorywatch

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// SF-43 bounded-spec item 1: the per-connection aggregate byte budget. The
// accounting charges every read across the whole connection (commands and
// payloads alike) and never server writes; the throttle ramps linearly past
// the soft threshold; the hard budget disconnects with ErrConnBudgetExceeded
// and counts one metric.

// stubConn serves a fixed payload through net.Conn semantics (no I/O, no
// goroutines): Read pulls from the embedded reader.
type stubConn struct {
	io.Reader
}

func (c *stubConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *stubConn) Close() error                { return nil }
func (c *stubConn) LocalAddr() net.Addr         { return addrStub{} }
func (c *stubConn) RemoteAddr() net.Addr        { return addrStub{} }
func (c *stubConn) SetDeadline(time.Time) error { return nil }
func (c *stubConn) SetReadDeadline(time.Time) error {
	return nil
}
func (c *stubConn) SetWriteDeadline(time.Time) error { return nil }

type addrStub struct{}

func (addrStub) Network() string { return "tcp" }
func (addrStub) String() string  { return "192.0.2.7:4441" }

// recordingBudget builds a budget with a delay-recording sleeper instead of
// a real sleep.
type recordingBudget struct {
	*ConnectionBudget
	delays []time.Duration
}

func newRecordingBudget(soft, hard int64, maxDelay time.Duration) *recordingBudget {
	b := &ConnectionBudget{
		softBytes: soft,
		hardBytes: hard,
		maxDelay:  maxDelay,
	}
	rb := &recordingBudget{ConnectionBudget: b}
	b.sleeper = func(d time.Duration) { rb.delays = append(rb.delays, d) }
	return rb
}

func counterValue(t *testing.T, name string) float64 {
	t.Helper()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var sum float64
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			sum += m.GetCounter().GetValue()
		}
	}
	return sum
}

// TestConnectionBudgetAccountingCountsAcrossCommands pins the accounting
// contract: the aggregate is charged per connection across every read —
// whatever the chunking — while server-side writes are never charged, and a
// disabled (nil) budget meters nothing and never errors.
func TestConnectionBudgetAccountingCountsAcrossCommands(t *testing.T) {
	payload := bytes.Repeat([]byte("A"), 100)

	// Ten reads of ten bytes and one of ninety must charge what flowed.
	charge := func(chunks []int) int64 {
		conn := &budgetConn{
			Conn:   &stubConn{Reader: bytes.NewReader(payload)},
			budget: newRecordingBudget(1000, 2000, time.Second).ConnectionBudget,
		}
		for _, n := range chunks {
			buf := make([]byte, n)
			if _, err := conn.Read(buf); err != nil {
				t.Fatalf("read %d bytes: %v", n, err)
			}
		}
		return conn.used.Load()
	}
	if got := charge([]int{10, 10, 10, 10, 10, 10, 10, 10, 10, 10}); got != 100 {
		t.Fatalf("ten 10-byte reads charged %d, want 100", got)
	}
	if got := charge([]int{90}); got != 90 {
		t.Fatalf("one 90-byte read charged %d, want 90", got)
	}

	// Writes are never charged: after a full read of the payload, writes do
	// not move the meter.
	conn := &budgetConn{
		Conn:   &stubConn{Reader: bytes.NewReader(payload)},
		budget: newRecordingBudget(1000, 2000, time.Second).ConnectionBudget,
	}
	if _, err := conn.Read(make([]byte, 100)); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := conn.Write(bytes.Repeat([]byte("2"), 5000)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := conn.used.Load(); got != 100 {
		t.Fatalf("after reads+writes charged %d, want reads only (100)", got)
	}

	// Disabled budget (nil): reads never error and meter nothing.
	free := &budgetConn{Conn: &stubConn{Reader: bytes.NewReader(payload)}}
	if _, err := free.Read(make([]byte, 100)); err != nil {
		t.Fatalf("nil-budget read must never error: %v", err)
	}
	if got := free.used.Load(); got != 0 {
		t.Fatalf("nil-budget conn must meter nothing, got %d", got)
	}

	// The config constructor: 0 disables, 1 MiB arms 80% soft.
	if b := NewConnectionBudget(0); b != nil {
		t.Fatal("budget 0 must disable (nil)")
	}
	b := NewConnectionBudget(1)
	if b == nil || b.hardBytes != 1<<20 || b.softBytes != (1<<20)*80/100 {
		t.Fatalf("NewConnectionBudget(1) = %+v, want hard 1MiB with soft at 80%%", b)
	}
}

// TestConnectionBudgetThrottleThenDisconnect pins the graduated response:
// below the soft threshold reads return unthrottled; past soft the per-read
// delay grows linearly toward maxDelay; at the hard budget the connection
// disconnects with ErrConnBudgetExceeded and the disconnect metric fires
// exactly once.
func TestConnectionBudgetThrottleThenDisconnect(t *testing.T) {
	// soft 30, hard 100: reads of 25 → 25 (free), 50 (ramp 20/70),
	// 75 (ramp 45/70), then 30 → 105 > 100 (disconnect).
	rb := newRecordingBudget(30, 100, time.Second)
	conn := &budgetConn{
		Conn:   &stubConn{Reader: bytes.NewReader(bytes.Repeat([]byte("B"), 130))},
		budget: rb.ConnectionBudget,
	}

	before := counterValue(t, "posta_mta_conn_budget_disconnects_total")

	buf := make([]byte, 25)
	for i := 0; i < 3; i++ {
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("read %d: %v", i+1, err)
		}
	}
	if len(rb.delays) != 2 {
		t.Fatalf("delays = %v, want two ramped delays after the two over-soft reads", rb.delays)
	}
	// Gradual: each successive delay must grow toward (but stay under) the
	// ceiling — (20/70)s then (45/70)s.
	if !(rb.delays[0] < rb.delays[1] && rb.delays[1] < time.Second) {
		t.Fatalf("throttle must ramp gradually toward maxDelay: %v", rb.delays)
	}
	wantFirst := time.Duration(float64(time.Second.Nanoseconds()) * 20.0 / 70.0)
	wantSecond := time.Duration(float64(time.Second.Nanoseconds()) * 45.0 / 70.0)
	if rb.delays[0] != wantFirst || rb.delays[1] != wantSecond {
		t.Fatalf("delays = %v, want [%s %s] (linear ramp)", rb.delays, wantFirst, wantSecond)
	}

	// The over-budget read disconnects: ErrConnBudgetExceeded surfaces and
	// the metric counts exactly one episode even if further reads error too.
	if _, err := conn.Read(make([]byte, 30)); err != ErrConnBudgetExceeded {
		t.Fatalf("over-budget read err = %v, want ErrConnBudgetExceeded", err)
	}
	if !conn.disconnected.Load() {
		t.Fatal("the over-budget connection must flag itself disconnected")
	}
	if _, err := conn.Read(make([]byte, 25)); err != ErrConnBudgetExceeded {
		t.Fatalf("post-disconnect read err = %v, want ErrConnBudgetExceeded", err)
	}
	if got := counterValue(t, "posta_mta_conn_budget_disconnects_total"); got != before+1 {
		t.Fatalf("disconnect metric = %f after two over-budget reads (before %f), want +1 (one-shot)", got, before)
	}
}

// TestBudgetListenerMetersAcceptedConns pins the listener seam: every
// accepted connection is wrapped with the budget, and a nil budget (or nil
// listener) passes through unchanged — the disabled case.
func TestBudgetListenerMetersAcceptedConns(t *testing.T) {
	payload := []byte("EHLO example.test\r\n")
	raw := &stubListener{conns: []net.Conn{&stubConn{Reader: bytes.NewReader(payload)}}}

	bl := BudgetListener(raw, newRecordingBudget(1000, 2000, time.Second).ConnectionBudget)
	c, err := bl.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	bc, ok := c.(*budgetConn)
	if !ok {
		t.Fatalf("accepted conn = %T, want *budgetConn", c)
	}
	if _, err := c.Read(make([]byte, len(payload))); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := bc.used.Load(); got != int64(len(payload)) {
		t.Fatalf("metered %d bytes, want %d", got, len(payload))
	}
	if raw.leftover() != 0 {
		t.Fatalf("accept must drain the stub listener, %d conns left", raw.leftover())
	}

	// Disabled: nil budget and nil listener pass through as themselves.
	var nilBudget *ConnectionBudget
	if BudgetListener(raw, nilBudget) != raw {
		t.Fatal("nil budget must return the listener unchanged")
	}
	if BudgetListener(nil, NewConnectionBudget(1)) != nil {
		t.Fatal("nil listener must return nil")
	}
}

// stubListener hands out pre-built conns.
type stubListener struct {
	conns []net.Conn
}

func (l *stubListener) Accept() (net.Conn, error) {
	if len(l.conns) == 0 {
		return nil, net.ErrClosed
	}
	c := l.conns[0]
	l.conns = l.conns[1:]
	return c, nil
}
func (l *stubListener) Close() error   { return nil }
func (l *stubListener) Addr() net.Addr { return addrStub{} }
func (l *stubListener) leftover() int  { return len(l.conns) }
