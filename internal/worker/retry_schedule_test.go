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

package worker

import (
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/queue"
)

// SF-08: the retry sweep's schedule consult (the seam over
// services/queue.Manager). A failed email whose recipients' interval lists
// hold the next attempt in the future stays failed until due; an exhausted
// schedule never re-enters; due and schedule-less emails keep the R-27
// bounded-batch behavior. The consult is a stub seam, so these tests pin the
// sweep's branch logic (the manager's decisions are pinned in the queue
// package's own tests).

type stubScheduler struct {
	decision queue.ScheduleDecision
	due      time.Time
	asked    []uint
}

func (s *stubScheduler) DecideRetry(emailID uint, _ time.Time) (queue.ScheduleDecision, time.Time) {
	s.asked = append(s.asked, emailID)
	return s.decision, s.due
}

// TestRetrySweepHonorsScheduleWait pins the wait branch: a failed email whose
// schedule holds the next attempt in the future is skipped — not claimed, not
// re-enqueued — and stays failed for the next sweep tick.
func TestRetrySweepHonorsScheduleWait(t *testing.T) {
	state := newRBState()
	var wsID uint = 17
	state.seedServer(&wsID, 3)
	state.seedFailedEmail(1, &wsID, 1)

	eq := &rbEnqueuer{first: make(chan struct{})}
	w := newRetryWorker(t, state, eq, 10)
	sched := &stubScheduler{decision: queue.DecisionWait, due: time.Now().Add(30 * time.Minute)}
	w.SetScheduler(sched)

	w.ProcessRetries()
	if got := eq.enqueued(); len(got) != 0 {
		t.Fatalf("sweep enqueued %v, want nothing (schedule holds)", got)
	}
	if left := state.failedIDs(); len(left) != 1 {
		t.Fatalf("failed rows = %v, want the email left failed", left)
	}
	if len(sched.asked) != 1 || sched.asked[0] != 1 {
		t.Fatalf("consulted emails = %v, want [1]", sched.asked)
	}
}

// TestRetrySweepSkipsExhaustedSchedule pins the exhausted branch: the email
// never re-enters the queue.
func TestRetrySweepSkipsExhaustedSchedule(t *testing.T) {
	state := newRBState()
	var wsID uint = 17
	state.seedServer(&wsID, 3)
	state.seedFailedEmail(1, &wsID, 1)

	eq := &rbEnqueuer{first: make(chan struct{})}
	w := newRetryWorker(t, state, eq, 10)
	w.SetScheduler(&stubScheduler{decision: queue.DecisionExhausted})

	w.ProcessRetries()
	if got := eq.enqueued(); len(got) != 0 {
		t.Fatalf("sweep enqueued %v, want nothing (schedule exhausted)", got)
	}
	if left := state.failedIDs(); len(left) != 1 {
		t.Fatalf("failed rows = %v, want the email left failed", left)
	}
}

// TestRetrySweepDueScheduleProceeds pins the due branch: a due (or
// schedule-less) email re-enqueues exactly as the pre-SF-08 sweep does.
func TestRetrySweepDueScheduleProceeds(t *testing.T) {
	for _, decision := range []queue.ScheduleDecision{queue.DecisionDue, queue.DecisionNone} {
		state := newRBState()
		var wsID uint = 17
		state.seedServer(&wsID, 3)
		state.seedFailedEmail(1, &wsID, 1)

		eq := &rbEnqueuer{first: make(chan struct{})}
		w := newRetryWorker(t, state, eq, 10)
		w.SetScheduler(&stubScheduler{decision: decision})

		w.ProcessRetries()
		got := eq.enqueued()
		if len(got) != 1 || got[0] != 1 {
			t.Fatalf("decision %v: sweep enqueued %v, want [1]", decision, got)
		}
		if left := state.failedIDs(); len(left) != 0 {
			t.Fatalf("decision %v: failed rows = %v, want claimed", decision, left)
		}
	}
}
