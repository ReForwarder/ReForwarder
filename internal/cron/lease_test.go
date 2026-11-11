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

package cron

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// fakeLeaseStore is an in-memory LeaseStore whose entries carry real expiry
// timestamps, so failover after a TTL can be exercised without sleeping out
// production-sized windows. err, when set, fails every call like a Redis
// outage would.
type fakeLeaseStore struct {
	mu     sync.Mutex
	holders map[string]string // key -> holder (entry removed on expiry)
	expires map[string]time.Time
	err     error
}

func newFakeLeaseStore() *fakeLeaseStore {
	return &fakeLeaseStore{holders: map[string]string{}, expires: map[string]time.Time{}}
}

func (f *fakeLeaseStore) AcquireOrRenew(_ context.Context, key, holder string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	now := time.Now()
	if cur, ok := f.holders[key]; ok {
		if f.expires[key].After(now) {
			if cur == holder {
				f.expires[key] = now.Add(ttl)
				return true, nil
			}
			return false, nil
		}
		// expired: free to take
	}
	f.holders[key] = holder
	f.expires[key] = now.Add(ttl)
	return true, nil
}

func (f *fakeLeaseStore) Release(_ context.Context, key, holder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.holders[key] == holder {
		delete(f.holders, key)
		delete(f.expires, key)
	}
	return nil
}

// fakeClaimStore is an in-memory ClaimStore: first claim wins until cleared,
// and err makes every claim fail like a Redis outage (which ClaimOnce must
// fail OPEN on).
type fakeClaimStore struct {
	mu     sync.Mutex
	claims map[string]bool
	err    error
}

func newFakeClaimStore() *fakeClaimStore { return &fakeClaimStore{claims: map[string]bool{}} }

func (f *fakeClaimStore) ClaimOnce(_ context.Context, key string, _ time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return true // fail-open, mirrors RedisClaimStore
	}
	if f.claims[key] {
		return false
	}
	f.claims[key] = true
	return true
}

func (f *fakeClaimStore) ClearClaim(_ context.Context, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.claims, key)
}

// stubJob is a CronJob that counts ticks.
type stubJob struct {
	name string
	runs *atomic.Int64
	err  error
}

func (s *stubJob) Name() string       { return s.name }
func (s *stubJob) Schedule() string   { return "0 0 1 1 *" } // yearly: never fires on its own in tests
func (s *stubJob) Run(_ context.Context, _ *asynq.Client) error {
	if s.runs != nil {
		s.runs.Add(1)
	}
	return s.err
}

// Compile-time guard that stubJob satisfies CronJob.
var _ CronJob = (*stubJob)(nil)

func TestLeaderLeaseExclusiveAcrossReplicas(t *testing.T) {
	store := newFakeLeaseStore()
	leader := NewLeaderLease(store, DefaultLeaseKey, time.Minute, "replica-a")
	follower := NewLeaderLease(store, DefaultLeaseKey, time.Minute, "replica-b")
	ctx := context.Background()

	if !leader.Acquire(ctx) {
		t.Fatal("first replica must take a free lease")
	}
	if follower.Acquire(ctx) {
		t.Fatal("second replica must not take a held lease")
	}
	if !leader.Acquire(ctx) {
		t.Fatal("holder must be able to renew its own lease")
	}
	if follower.Acquire(ctx) {
		t.Fatal("renewal must not hand leadership to another replica")
	}
}

func TestLeaderLeaseFailoverAfterTTL(t *testing.T) {
	store := newFakeLeaseStore()
	// TTL far below real schedules: expiry, not sleep-to-next-day, moves
	// leadership. Failover bound (GAP-03): once the holder stops renewing,
	// another replica takes over within one TTL.
	leader := NewLeaderLease(store, DefaultLeaseKey, 30*time.Millisecond, "replica-a")
	follower := NewLeaderLease(store, DefaultLeaseKey, 30*time.Millisecond, "replica-b")
	ctx := context.Background()

	if !leader.Acquire(ctx) {
		t.Fatal("leader must take the lease")
	}
	if follower.Acquire(ctx) {
		t.Fatal("lease must be held while inside the TTL")
	}
	time.Sleep(45 * time.Millisecond) // past the TTL, no renewal
	if !follower.Acquire(ctx) {
		t.Fatal("failover must complete within one TTL after the holder stops renewing")
	}
	if leader.Acquire(ctx) {
		t.Fatal("the displaced leader must not retake a held lease")
	}
}

func TestLeaderLeaseFailClosedOnRedisError(t *testing.T) {
	store := newFakeLeaseStore()
	store.err = errors.New("redis down")
	lease := NewLeaderLease(store, DefaultLeaseKey, time.Minute, "replica-a")

	if lease.Acquire(context.Background()) {
		t.Fatal("acquire must fail closed (skip the tick) while Redis errors")
	}
}

func TestLeaderLeaseReleaseOnlyByHolder(t *testing.T) {
	store := newFakeLeaseStore()
	leader := NewLeaderLease(store, DefaultLeaseKey, time.Minute, "replica-a")
	successor := NewLeaderLease(store, DefaultLeaseKey, time.Minute, "replica-b")
	ctx := context.Background()

	if !leader.Acquire(ctx) {
		t.Fatal("leader must take the lease")
	}
	_ = store.Release(ctx, DefaultLeaseKey, "replica-b") // wrong holder: must be a no-op
	if successor.Acquire(ctx) {
		t.Fatal("a non-holder release must not free the lease")
	}
	leader.Release(ctx)
	if !successor.Acquire(ctx) {
		t.Fatal("holder release must free the lease for the next replica")
	}
}

func TestManagerLeaseGateExactlyOneRunner(t *testing.T) {
	store := newFakeLeaseStore()

	// Two managers stand in for two replicas, sharing one Redis (here: one
	// fake store). Each installs the same lease identity scheme the server
	// uses (NewHolderID per process; here fixed ids for assertion clarity).
	leaseA := NewLeaderLease(store, DefaultLeaseKey, time.Minute, "replica-a")
	leaseB := NewLeaderLease(store, DefaultLeaseKey, time.Minute, "replica-b")

	mgrA, mgrB := NewManager(nil), NewManager(nil)
	mgrA.SetLeaderLease(leaseA)
	mgrB.SetLeaderLease(leaseB)

	var schedRuns, alertRuns, reportRuns atomic.Int64
	sched := &stubJob{name: "campaign_scheduler", runs: &schedRuns}
	alert := &stubJob{name: "bounce_alert", runs: &alertRuns}
	report := &stubJob{name: "daily_report", runs: &reportRuns}

	// One tick of each job on each replica.
	for _, job := range []CronJob{sched, alert, report} {
		mgrA.RunOnce(context.Background(), job)
		mgrB.RunOnce(context.Background(), job)
	}

	for _, tc := range []struct {
		name string
		runs *atomic.Int64
	}{{"campaign_scheduler", &schedRuns}, {"bounce_alert", &alertRuns}, {"daily_report", &reportRuns}} {
		if got := tc.runs.Load(); got != 1 {
			t.Fatalf("%s ran %d times across two replicas, want exactly 1", tc.name, got)
		}
	}
}

func TestManagerWithoutLeaseKeepsSingleReplicaBehavior(t *testing.T) {
	// Default (POSTA_CRON_LEADER_LEASE_ENABLED off): both replicas run every
	// job — today's behavior, unchanged.
	mgrA, mgrB := NewManager(nil), NewManager(nil)
	var runs atomic.Int64
	job := &stubJob{name: "retention", runs: &runs}
	mgrA.RunOnce(context.Background(), job)
	mgrB.RunOnce(context.Background(), job)
	if got := runs.Load(); got != 2 {
		t.Fatalf("job ran %d times without a lease, want 2 (one per replica)", got)
	}
}

func TestManagerRunOnceRecordsStatus(t *testing.T) {
	mgr := NewManager(nil)
	var runs atomic.Int64
	failing := &stubJob{name: "failing", runs: &runs, err: errors.New("boom")}
	mgr.Register(failing)
	mgr.RunOnce(context.Background(), failing)

	statuses := mgr.Jobs()
	if len(statuses) != 1 || statuses[0].LastError != "boom" {
		t.Fatalf("status after failed tick = %+v, want last_error=boom", statuses)
	}
	if runs.Load() != 1 {
		t.Fatalf("job ran %d times, want 1", runs.Load())
	}
}

// TestLeaderLeaseTwoManagersRedis is the GAP-03 acceptance integration test:
// two managers sharing a real Redis, exactly one replica executes per tick,
// and leadership fails over within one TTL. Runs only against a real Redis,
// set via POSTA_TEST_REDIS_URL (for example redis://localhost:6379/15) —
// same pattern as realtime's TestRedisBusAcrossClients.
func TestLeaderLeaseTwoManagersRedis(t *testing.T) {
	url := os.Getenv("POSTA_TEST_REDIS_URL")
	if url == "" {
		t.Skip("POSTA_TEST_REDIS_URL not set")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	// Two clients stand in for two processes.
	clientA, clientB := redis.NewClient(opt), redis.NewClient(opt)
	defer func() { _ = clientA.Close(); _ = clientB.Close() }()

	key := "posta:cron:leader:test-" + NewHolderID()
	storeA, storeB := NewRedisLeaseStore(clientA), NewRedisLeaseStore(clientB)
	ttl := 200 * time.Millisecond

	leaseA := NewLeaderLease(storeA, key, ttl, "replica-a")
	leaseB := NewLeaderLease(storeB, key, ttl, "replica-b")
	mgrA, mgrB := NewManager(nil), NewManager(nil)
	mgrA.SetLeaderLease(leaseA)
	mgrB.SetLeaderLease(leaseB)

	var schedRuns, alertRuns atomic.Int64
	sched := &stubJob{name: "campaign_scheduler", runs: &schedRuns}
	alert := &stubJob{name: "bounce_alert", runs: &alertRuns}

	ctx := context.Background()
	// Tick both replicas on both jobs: exactly one execution per job.
	mgrA.RunOnce(ctx, sched)
	mgrB.RunOnce(ctx, sched)
	mgrA.RunOnce(ctx, alert)
	mgrB.RunOnce(ctx, alert)
	if n := schedRuns.Load(); n != 1 {
		t.Fatalf("campaign_scheduler ran %d times, want exactly 1", n)
	}
	if n := alertRuns.Load(); n != 1 {
		t.Fatalf("bounce_alert ran %d times, want exactly 1", n)
	}

	// Leader loss: the holder stops renewing; after one TTL the other
	// replica takes over — failover within one interval, no double-run of
	// the same tick.
	time.Sleep(2 * ttl)
	mgrB.RunOnce(ctx, alert)
	if n := alertRuns.Load(); n != 2 {
		t.Fatalf("bounce_alert ran %d times after failover, want 2 (once per interval, one replica each)", n)
	}
}

func TestRedisClaimStoreDedupAndFailOpen(t *testing.T) {
	claims := newFakeClaimStore()
	ctx := context.Background()
	key := "cron:dedup:bounce-alert:7:20261110"

	if !claims.ClaimOnce(ctx, key, 48*time.Hour) {
		t.Fatal("first claim must win")
	}
	if claims.ClaimOnce(ctx, key, 48*time.Hour) {
		t.Fatal("second claim same window must be refused")
	}
	claims.ClearClaim(ctx, key)
	if !claims.ClaimOnce(ctx, key, 48*time.Hour) {
		t.Fatal("cleared claim must be claimable again (retry after failed send)")
	}

	// Redis outage: fail OPEN (send proceeds) — the guard prevents
	// duplicates but must not suppress the alert.
	claims.err = errors.New("redis down")
	if !claims.ClaimOnce(ctx, "other-key", 48*time.Hour) {
		t.Fatal("claim must fail open while Redis errors")
	}
}
