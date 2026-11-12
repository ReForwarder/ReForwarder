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

package mta

import (
	"context"
	"testing"
	"time"
)

// CA-04: per-message send-rate pacing. The keeper is deterministic (no
// jitter — Policy.ThrottleJitter is zero in these specs), the executor
// consults it immediately before each MAIL FROM, and pacing at zero is a
// no-op. The deliver tests pin the package clock and advance it in the fake
// sleeper, so waits are asserted exactly without real time.

func TestMessageRateKeeperRefill(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// 60/min → one message per second.
	k := NewMessageRateKeeper()
	if wait := k.Wait("example.com", 60, now); wait != 0 {
		t.Fatalf("first wait = %s, want 0", wait)
	}
	// An immediate second message must wait exactly one interval.
	if wait := k.Wait("example.com", 60, now); wait != time.Second {
		t.Fatalf("second wait = %s, want 1s", wait)
	}
	// Domains pace independently.
	if wait := k.Wait("other.com", 60, now); wait != 0 {
		t.Fatalf("other-domain wait = %s, want 0", wait)
	}
	// Each Wait reserves the following slot: at t=1s the two slots already
	// taken (t=0 and t=1s) push the next start to t=2s → wait 1s.
	if wait := k.Wait("example.com", 60, now.Add(time.Second)); wait != time.Second {
		t.Fatalf("next-slot wait = %s, want 1s", wait)
	}

	// Partial refill on a fresh keeper: one reservation at t=0 (slot at
	// t=1s); a request at t=0.5s waits for the remaining 500ms.
	k = NewMessageRateKeeper()
	if wait := k.Wait("example.com", 60, now); wait != 0 {
		t.Fatalf("fresh first wait = %s, want 0", wait)
	}
	if wait := k.Wait("example.com", 60, now.Add(500*time.Millisecond)); wait != 500*time.Millisecond {
		t.Fatalf("partial refill wait = %s, want 500ms", wait)
	}
	// That request reserved the t=2s slot; once its interval has fully
	// elapsed the token is free again.
	if wait := k.Wait("example.com", 60, now.Add(2*time.Second)); wait != 0 {
		t.Fatalf("refilled wait = %s, want 0", wait)
	}
}

func TestThrottleDisabledAtZero(t *testing.T) {
	k := NewMessageRateKeeper()
	now := time.Now()
	for i := 0; i < 100; i++ {
		if wait := k.Wait("example.com", 0, now); wait != 0 {
			t.Fatalf("rate 0 wait = %s, want 0 (disabled)", wait)
		}
		if wait := k.Wait("example.com", -5, now); wait != 0 {
			t.Fatalf("negative rate wait = %s, want 0 (disabled)", wait)
		}
	}

	// End to end: an executor with no rate configured never paces.
	f := newDeliverFixture(t)
	var sleeps []time.Duration
	f.executor.SetSleeper(func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	})
	for i := 0; i < 3; i++ {
		if _, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com")); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	if len(sleeps) != 0 {
		t.Fatalf("pacing sleeps recorded with no rate configured: %v", sleeps)
	}
}

func TestDeliverHonorsPerMessageRate(t *testing.T) {
	f := newDeliverFixture(t)

	// Virtual pacer clock (executor-level; the SMTP session keeps the real
	// package clock so its deadlines stay live). The fake sleeper advances
	// it, so the pacing timeline behaves like real time without real delays.
	clock := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	f.executor.SetClock(func() time.Time { return clock })

	var sleeps []time.Duration
	f.executor.SetSleeper(func(ctx context.Context, d time.Duration) error {
		clock = clock.Add(d)
		sleeps = append(sleeps, d)
		return nil
	})
	// 30/min → one message every 2s. The executor's process-wide default
	// applies (Policy.MsgRatePerMin unset). Jitter is zeroed so the wait
	// assertions are exact; the ±jitter path itself is covered by the
	// budget-defer tests.
	f.executor.SetMessageRatePerMin(30)
	spec := testSpec()
	spec.Policy.ThrottleJitter = 0

	for i := 0; i < 3; i++ {
		if _, err := f.executor.Deliver(context.Background(), spec, testEnvelope("rcpt@example.com")); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	if len(sleeps) != 2 {
		t.Fatalf("paced sleeps = %v, want exactly the 2nd and 3rd message waits", sleeps)
	}
	for i, d := range sleeps {
		if d != 2*time.Second {
			t.Fatalf("sleep[%d] = %s, want 2s (30/min interval)", i, d)
		}
	}

	// A per-domain Policy override wins over the executor default: 600/min →
	// 100ms interval. A fresh keeper isolates the reservation timeline.
	f.executor.rateKeeper = NewMessageRateKeeper()
	f.executor.SetMessageRatePerMin(30)
	spec.Policy.MsgRatePerMin = 600
	sleeps = nil
	for i := 0; i < 2; i++ {
		if _, err := f.executor.Deliver(context.Background(), spec, testEnvelope("rcpt@example.com")); err != nil {
			t.Fatalf("override delivery %d: %v", i, err)
		}
	}
	if len(sleeps) != 1 || sleeps[0] != 100*time.Millisecond {
		t.Fatalf("override sleeps = %v, want one 100ms wait", sleeps)
	}
}
