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

package cron

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/hibiken/asynq"
)

// SF-99 tests: the run-history wrapper + the manual trigger. The recording
// seam is a fake; no scheduler, no asynq client, no database — the manager's
// own bookkeeping is the assertion target.

// fakeJob is a CronJob whose Run signals ran and can block until released.
type fakeJob struct {
	name     string
	runFn    func() error
	ran      chan struct{}
	runCount int
	mu       sync.Mutex
}

func newFakeJob(name string, runFn func() error) *fakeJob {
	return &fakeJob{name: name, runFn: runFn, ran: make(chan struct{}, 16)}
}

func (j *fakeJob) Name() string    { return j.name }
func (j *fakeJob) Schedule() string { return "0 3 * * *" }
func (j *fakeJob) Run(ctx context.Context, client *asynq.Client) error {
	j.mu.Lock()
	j.runCount++
	j.mu.Unlock()
	j.ran <- struct{}{}
	if j.runFn != nil {
		return j.runFn()
	}
	return nil
}

func (j *fakeJob) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.runCount
}

func (j *fakeJob) waitRan(t *testing.T) {
	t.Helper()
	select {
	case <-j.ran:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not start within 5s")
	}
}

// waitFor pins a poll-until-deadline helper: the manual path is asynchronous,
// so a condition (the run finishing, the history row landing) must be waited
// for rather than asserted immediately.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// recordingHistory is a RunHistoryRecorder over an in-memory slice.
type recordingHistory struct {
	mu    sync.Mutex
	entries []*models.CronRunHistory
	errOn  int // the nth RecordCronRun call fails (0 = never)
	calls  int
}

func (r *recordingHistory) RecordCronRun(entry *models.CronRunHistory) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.errOn > 0 && r.calls == r.errOn {
		return errors.New("history store down")
	}
	cp := *entry
	r.entries = append(r.entries, &cp)
	return nil
}

func (r *recordingHistory) all() []*models.CronRunHistory {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*models.CronRunHistory(nil), r.entries...)
}

func (r *recordingHistory) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// TestRunHistoryCaptureOnScheduledTick pins the capture contract: every
// completed scheduled run writes exactly one row — success rows with empty
// detail, failure rows carrying the error text — with started/finished
// bracketing the run and outcome set accordingly. A nil recorder records
// nothing and never fails a run, and a recorder failure is swallowed (the
// run outcome stands).
func TestRunHistoryCaptureOnScheduledTick(t *testing.T) {
	okJob := newFakeJob("ok_job", nil)
	failJob := newFakeJob("fail_job", func() error { return errors.New("boom: reconcile exploded") })

	rec := &recordingHistory{}
	m := NewManager(nil)
	m.SetRunHistory(rec)
	m.Register(okJob)
	m.Register(failJob)

	m.RunOnce(context.Background(), okJob)
	okJob.waitRan(t)
	m.RunOnce(context.Background(), failJob)
	failJob.waitRan(t)

	entries := rec.all()
	if len(entries) != 2 {
		t.Fatalf("history rows = %d, want 2 (one per run)", len(entries))
	}
	first := entries[0]
	if first.JobName != "ok_job" || first.Trigger != models.CronTriggerSchedule ||
		first.Outcome != models.CronOutcomeSuccess || first.Detail != "" {
		t.Fatalf("success row = %+v, want ok_job/schedule/success with empty detail", first)
	}
	if first.StartedAt.IsZero() || first.FinishedAt.IsZero() || first.FinishedAt.Before(first.StartedAt) {
		t.Fatalf("success row timestamps = %v → %v, want finished >= started", first.StartedAt, first.FinishedAt)
	}
	second := entries[1]
	if second.JobName != "fail_job" || second.Outcome != models.CronOutcomeFailed {
		t.Fatalf("failure row = %+v, want fail_job/failed", second)
	}
	if second.Detail != "boom: reconcile exploded" {
		t.Fatalf("failure detail = %q, want the error text", second.Detail)
	}

	// A recorder failure is best-effort: the run completes, the in-memory
	// status still updates, nothing panics.
	failing := &recordingHistory{errOn: 1}
	m2 := NewManager(nil)
	m2.SetRunHistory(failing)
	j := newFakeJob("j", nil)
	m2.Register(j)
	m2.RunOnce(context.Background(), j)
	j.waitRan(t)
	if j.count() != 1 {
		t.Fatalf("job ran %d times with a failing recorder, want 1 (recording is best-effort)", j.count())
	}

	// No recorder wired: the pre-SF-99 behavior, zero rows, no panic.
	m3 := NewManager(nil)
	j3 := newFakeJob("j3", nil)
	m3.Register(j3)
	m3.RunOnce(context.Background(), j3)
	j3.waitRan(t)
	if rec.len() != 2 {
		// rec belongs to the first manager only; this guard just documents
		// that m3 (nil recorder) has nowhere to write.
		t.Fatalf("unrelated recorder grew: %d", rec.len())
	}
}

// TestManualTriggerRunsAndRecords pins the manual path: the named job runs
// exactly once through the same recording path with trigger=manual, a
// second trigger while the job is mid-run is refused (alreadyRunning), an
// unknown name is not found, and other registered jobs stay untouched
// (isolation).
func TestManualTriggerRunsAndRecords(t *testing.T) {
	release := make(chan struct{})
	blocked := newFakeJob("blocked_job", func() error {
		<-release
		return nil
	})
	other := newFakeJob("other_job", nil)

	rec := &recordingHistory{}
	m := NewManager(nil)
	m.SetRunHistory(rec)
	m.Register(blocked)
	m.Register(other)

	// Unknown name: not found, nothing runs.
	found, running := m.TriggerJob("no_such_job")
	if found || running {
		t.Fatalf("TriggerJob(unknown) = %v/%v, want false/false", found, running)
	}
	if other.count() != 0 || blocked.count() != 0 {
		t.Fatal("no job may run for an unknown name")
	}

	// Trigger the blocked job: claimed immediately, runs asynchronously.
	found, running = m.TriggerJob("blocked_job")
	if !found || running {
		t.Fatalf("TriggerJob(blocked_job) = %v/%v, want true/false", found, running)
	}
	blocked.waitRan(t) // the run started

	// A second trigger while mid-run: refused, no second run.
	found, running = m.TriggerJob("blocked_job")
	if !found || !running {
		t.Fatalf("second TriggerJob = %v/%v, want true/true (already running)", found, running)
	}
	if blocked.count() != 1 {
		t.Fatalf("blocked job ran %d times, want exactly 1 (the double trigger is a no-op)", blocked.count())
	}

	// Other registered jobs stay untouched (isolation): nothing but the
	// triggered job ran.
	if other.count() != 0 {
		t.Fatalf("other job ran %d times, want 0 (the trigger isolates to the named job)", other.count())
	}

	close(release)
	// The manual run finishes asynchronously: wait for its history row.
	waitFor(t, "the manual run's history row", func() bool { return rec.len() == 1 })

	entries := rec.all()
	e := entries[0]
	if e.JobName != "blocked_job" || e.Trigger != models.CronTriggerManual || e.Outcome != models.CronOutcomeSuccess {
		t.Fatalf("manual row = %+v, want blocked_job/manual/success", e)
	}
}

// TestManualTriggerBypassesLeaderLease pins the lease semantics: a scheduled
// tick under a non-holder lease is skipped entirely (no run, no history
// row), while a manual trigger on the same manager still runs — the
// leadership gate exists to prevent duplicate scheduled runs across
// replicas, not to block operator intent on the node that received it. The
// lease is a real LeaderLease over a store that always refuses (the
// fail-closed Redis posture).
func TestManualTriggerBypassesLeaderLease(t *testing.T) {
	job := newFakeJob("leased_job", nil)
	rec := &recordingHistory{}
	m := NewManager(nil)
	m.SetRunHistory(rec)
	m.SetLeaderLease(NewLeaderLease(refusingLeaseStore{}, "cron-test-lease", time.Minute, "replica-a"))
	m.Register(job)

	// The scheduled tick: skipped, nothing recorded.
	m.RunOnce(context.Background(), job)
	if job.count() != 0 {
		t.Fatalf("scheduled run executed under a refusing lease, want skipped")
	}
	if rec.len() != 0 {
		t.Fatalf("history rows after a skipped tick = %d, want 0", rec.len())
	}

	// The manual trigger: runs despite the refusing lease.
	found, _ := m.TriggerJob("leased_job")
	if !found {
		t.Fatal("manual trigger must find the registered job")
	}
	job.waitRan(t)
	waitFor(t, "the manual run's history row", func() bool { return rec.len() == 1 })
	if job.count() != 1 {
		t.Fatalf("manual run count = %d, want 1 (the lease is bypassed for operator intent)", job.count())
	}
	entries := rec.all()
	if len(entries) != 1 || entries[0].Trigger != models.CronTriggerManual {
		t.Fatalf("history = %+v, want exactly one manual row", entries)
	}
}

// refusingLeaseStore never grants the lease (fail-closed without Redis).
type refusingLeaseStore struct{}

func (s refusingLeaseStore) AcquireOrRenew(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	return false, errors.New("redis unreachable")
}

func (s refusingLeaseStore) Release(ctx context.Context, key, holder string) error {
	return nil
}
