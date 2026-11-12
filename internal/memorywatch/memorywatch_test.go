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
	"context"
	"runtime"
	"testing"
	"time"
)

// SF-108/SF-43: the memory watchdog. A nil watchdog (no budget) always
// reports healthy; a configured one flips pressure on heap-over-budget
// transitions and back on recovery.

func TestDisabledAtZeroBudget(t *testing.T) {
	w := Start(context.Background(), 0, time.Millisecond)
	if w != nil {
		t.Fatal("budget 0 must disable the watchdog (nil)")
	}
	// A nil watchdog must be safely consultable everywhere.
	var nilWatch *Watchdog
	if nilWatch.UnderPressure() {
		t.Fatal("nil watchdog must report healthy")
	}
}

func TestPressureTransitions(t *testing.T) {
	heap := uint64(100)
	// A manually driven watchdog (no sampling loop): sample() is called
	// synchronously; no stop channels, so Stop() below is never needed.
	w := &Watchdog{
		budgetBytes: 200,
		heapBytes:   func() uint64 { return heap },
	}

	w.sample()
	if w.UnderPressure() {
		t.Fatal("healthy heap must not raise pressure")
	}

	heap = 300 // over the 200-byte budget
	w.sample()
	if !w.UnderPressure() {
		t.Fatal("over-budget heap must raise pressure")
	}

	// Staying over must not toggle (the flag is transition-based).
	w.sample()
	if !w.UnderPressure() {
		t.Fatal("still-over heap must keep pressure up")
	}

	heap = 100 // recovered
	w.sample()
	if w.UnderPressure() {
		t.Fatal("recovered heap must clear pressure")
	}
}

func TestStartSamplesAgainstBudget(t *testing.T) {
	// End-to-end: a real watchdog over a real (tiny) heap budget. The test
	// allocates above the budget and waits for the sampler to notice.
	w := Start(context.Background(), 1, 10*time.Millisecond) // 1 MiB budget
	if w == nil {
		t.Fatal("budget 1 must arm the watchdog")
	}
	defer w.Stop()

	ballast := make([]byte, 8<<20) // 8 MiB > 1 MiB budget
	// Consume the ballast: an unused allocation is dead-code eliminated, and
	// unwritten pages do not count toward HeapAlloc. The checksum forces both
	// the allocation and the pages to materialize.
	var checksum byte
	for i := range ballast {
		ballast[i] = byte(i)
		checksum ^= ballast[i]
	}
	t.Logf("ballast checksum=%d", checksum)

	deadline := time.Now().Add(3 * time.Second)
	for !w.UnderPressure() {
		if time.Now().After(deadline) {
			t.Fatal("watchdog must raise pressure while the heap exceeds the budget")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The ballast must stay live through the polling loop, or the sampler's
	// view of the heap drops it.
	runtime.KeepAlive(ballast)
}
