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

// Package memorywatch is the process memory-budget watchdog (SF-108/SF-43).
// A goroutine samples the Go heap against POSTA_MEMORY_BUDGET_MB; while the
// budget is exceeded the watchdog's pressure flag is up, and the SMTP
// listeners consult it to refuse NEW sessions with a temporary 451 —
// backpressure at the front door, never an abort of in-flight work. The
// breach transitions are counted on posta_mta_memory_pressure_total.
package memorywatch

import (
	"context"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/goposta/posta/internal/metrics"
	"github.com/jkaninda/logger"
)

// Watchdog samples process memory against the configured budget. A nil
// *Watchdog (budget unset) reports no pressure — the disabled case must be
// indistinguishable from "healthy" at every call site.
type Watchdog struct {
	budgetBytes int64
	pressure    atomic.Bool
	// stop releases the sampling loop (tests).
	stop chan struct{}
	// stopped closes after the loop exits (tests).
	stopped chan struct{}
	// heapBytes reads the process heap (tests inject a fake).
	heapBytes func() uint64
}

// Start launches the watchdog. budgetMB <= 0 returns nil — the budget is
// unset and nothing samples. sampleEvery defaults to 5s.
func Start(ctx context.Context, budgetMB int, sampleEvery time.Duration) *Watchdog {
	if budgetMB <= 0 {
		return nil
	}
	if sampleEvery <= 0 {
		sampleEvery = 5 * time.Second
	}
	w := &Watchdog{
		budgetBytes: int64(budgetMB) << 20,
		stop:        make(chan struct{}),
		stopped:     make(chan struct{}),
		// HeapAlloc without a forced GC: allocated-but-not-yet-collected
		// garbage counts, which is exactly the transient burst the
		// backpressure exists for.
		heapBytes:   func() uint64 { var m runtime.MemStats; runtime.ReadMemStats(&m); return m.HeapAlloc },
	}
	go w.loop(ctx, sampleEvery)
	return w
}

// UnderPressure reports whether the process is over its memory budget.
func (w *Watchdog) UnderPressure() bool {
	return w != nil && w.pressure.Load()
}

// Stop releases the sampling loop (tests).
func (w *Watchdog) Stop() {
	if w == nil || w.stop == nil {
		return
	}
	select {
	case <-w.stop:
	default:
		close(w.stop)
		<-w.stopped
	}
}

func (w *Watchdog) loop(ctx context.Context, every time.Duration) {
	defer close(w.stopped)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-t.C:
		}
		w.sample()
	}
}

// sample reads the heap once and updates the pressure flag on transition.
// Split from the loop so tests can drive transitions synchronously.
func (w *Watchdog) sample() {
	over := int64(w.heapBytes()) > w.budgetBytes
	if over != w.pressure.Swap(over) {
		// Transition: count one pressure episode.
		if over {
			metrics.IncrementMTAMemoryPressure()
			logger.Error("memory watchdog: heap over budget; new SMTP sessions are refused until it drains",
				"budget_bytes", w.budgetBytes)
		} else {
			logger.Info("memory watchdog: heap back under budget; sessions accepted again")
		}
	}
}
