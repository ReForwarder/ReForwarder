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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/jkaninda/logger"
	"github.com/redis/go-redis/v9"
)

// DefaultLeaseKey is the Redis key the cron leader lease lives under. It is
// shared by every replica of a deployment — that is the point: holding it is
// what makes a replica the leader (GAP-03, ADR-009 §2).
const DefaultLeaseKey = "posta:cron:leader"

// DefaultLeaseTTL is the lease window when config leaves it unset. A holder
// that stops renewing (crashed replica, lost Redis) is displaced after at
// most this long — the failover bound of GAP-03 is "within one cron
// interval", and the default TTL sits at the manager's smallest schedule
// (the every-minute campaign scheduler).
const DefaultLeaseTTL = time.Minute

// LeaseStore is the Redis surface the leader lease needs. It is narrow on
// purpose so the leadership gate is unit-testable against a fake without a
// Redis dependency — the same discipline as ratelimit's Counter interface.
type LeaseStore interface {
	// AcquireOrRenew atomically sets key=holder with NX and a TTL; when the
	// key already holds `holder` it refreshes the TTL instead. Returns true
	// when `holder` owns the lease after the call.
	AcquireOrRenew(ctx context.Context, key, holder string, ttl time.Duration) (bool, error)
	// Release deletes key only when it still holds `holder`.
	Release(ctx context.Context, key, holder string) error
}

// LeaderLease gates the cron manager's ticks so exactly one replica executes
// each scheduled job in a multi-replica deployment (GAP-03; without it every
// replica runs every schedule and bounce alerts / daily reports double-send).
//
// Failure semantics: **fail-closed**. If Redis errors on acquire, the tick is
// skipped — mutual exclusion is the feature, so "run anyway" would reintroduce
// the duplicate sends GAP-03 exists to fix. During a Redis outage the
// leader-elected cron work pauses; queued delivery keeps flowing on the
// Postgres/asynq substrate per the Redis HA runbook
// (docs/docs/operations/redis-ha.md).
type LeaderLease struct {
	store  LeaseStore
	key    string
	holder string
	ttl    time.Duration
}

// NewLeaderLease builds a lease under key with the given TTL (clamped to
// DefaultLeaseTTL when non-positive). holder identifies this instance; use
// NewHolderID.
func NewLeaderLease(store LeaseStore, key string, ttl time.Duration, holder string) *LeaderLease {
	if key == "" {
		key = DefaultLeaseKey
	}
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	return &LeaderLease{store: store, key: key, holder: holder, ttl: ttl}
}

// Acquire takes the lease, or renews it when this instance already holds it.
// It returns false — the tick must be skipped — when another replica holds
// the lease or Redis is unreachable (fail-closed).
func (l *LeaderLease) Acquire(ctx context.Context) bool {
	ok, err := l.store.AcquireOrRenew(ctx, l.key, l.holder, l.ttl)
	if err != nil {
		logger.Error("cron leader lease: redis unavailable; failing closed (tick skipped)",
			"key", l.key, "holder", l.holder, "error", err)
		return false
	}
	return ok
}

// Release hands leadership back immediately instead of waiting out the TTL
// (graceful shutdown). Best-effort: losing Redis here is harmless — the lease
// expires on its own.
func (l *LeaderLease) Release(ctx context.Context) {
	if err := l.store.Release(ctx, l.key, l.holder); err != nil {
		logger.Debug("cron leader lease: release skipped", "key", l.key, "error", err)
	}
}

// Holder returns this lease instance's identity.
func (l *LeaderLease) Holder() string { return l.holder }

// NewHolderID builds a lease-owner identity: hostname-pid-random. The random
// suffix keeps two managers inside one process (tests, embedded worker)
// distinct.
func NewHolderID() string {
	host, _ := os.Hostname()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(suffix))
}

// RedisLeaseStore is the production LeaseStore over go-redis. Both operations
// are Lua scripts so the check-and-act stays atomic: a renewal can never
// resurrect a lease another replica already took, and a release can never
// delete a successor's lease.
type RedisLeaseStore struct {
	client redis.Cmdable
}

// NewRedisLeaseStore builds the store over any go-redis command surface.
func NewRedisLeaseStore(client redis.Cmdable) *RedisLeaseStore {
	return &RedisLeaseStore{client: client}
}

// acquireOrRenewScript: SET NX PX wins when free; otherwise only the current
// holder may extend the TTL (value comparison), so leadership can change but
// never fork.
var acquireOrRenewScript = redis.NewScript(`
if redis.call('set', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2]) then
	return 1
end
if redis.call('get', KEYS[1]) == ARGV[1] then
	redis.call('pexpire', KEYS[1], ARGV[2])
	return 1
end
return 0
`)

// releaseIfHolderScript: delete only when the caller still owns the key.
var releaseIfHolderScript = redis.NewScript(`
if redis.call('get', KEYS[1]) == ARGV[1] then
	return redis.call('del', KEYS[1])
end
return 0
`)

// AcquireOrRenew implements LeaseStore.
func (s *RedisLeaseStore) AcquireOrRenew(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	v, err := acquireOrRenewScript.Run(ctx, s.client, []string{key}, holder, ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return v == 1, nil
}

// Release implements LeaseStore.
func (s *RedisLeaseStore) Release(ctx context.Context, key, holder string) error {
	return releaseIfHolderScript.Run(ctx, s.client, []string{key}, holder).Err()
}

// ClaimStore is the once-per-window guard for customer-facing cron sends
// (GAP-03 acceptance: the lease alone cannot cover a crash between lease
// acquire and send). bounce-alert and daily-report claim one key per
// recipient per day before acting.
type ClaimStore interface {
	// ClaimOnce marks key for ttl. true = this caller claimed it first,
	// proceed; false = the key was already claimed, skip.
	ClaimOnce(ctx context.Context, key string, ttl time.Duration) bool
	// ClearClaim drops a claim after a failed send so a later tick (or the
	// next leader) can retry.
	ClearClaim(ctx context.Context, key string)
}

// RedisClaimStore is the production ClaimStore.
type RedisClaimStore struct {
	client redis.Cmdable
}

// NewRedisClaimStore builds the store over any go-redis command surface.
func NewRedisClaimStore(client redis.Cmdable) *RedisClaimStore {
	return &RedisClaimStore{client: client}
}

// ClaimOnce implements ClaimStore. Failure semantics differ from the leader
// lease on purpose: **fail-open**. The claim is an edge-guard against
// duplicates, not a gate on the alert itself — when Redis is unavailable,
// suppressing a customer's bounce alert would trade a possible duplicate for
// a certain miss. (With the leader lease enabled a Redis outage pauses the
// ticks anyway, so the exposure window is small; see the HA runbook.)
func (s *RedisClaimStore) ClaimOnce(ctx context.Context, key string, ttl time.Duration) bool {
	ok, err := s.client.SetNX(ctx, key, 1, ttl).Result()
	if err != nil {
		logger.Error("cron claim: redis unavailable; failing open (proceeding)", "key", key, "error", err)
		return true
	}
	return ok
}

// ClearClaim implements ClaimStore.
func (s *RedisClaimStore) ClearClaim(ctx context.Context, key string) {
	if err := s.client.Del(ctx, key).Err(); err != nil {
		logger.Warn("cron claim: clear failed", "key", key, "error", err)
	}
}
