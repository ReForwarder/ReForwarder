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

package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// SF-91: direct unit tests for RedisLimiter. miniredis is not a dependency of
// this module, so the limiter runs against a fake redis.Cmdable implementing
// the three commands it uses (INCR, EXPIRE, GET).

type limiterFakeRedis struct {
	redis.Cmdable

	mu     sync.Mutex
	counts map[string]int64
	// expireCalls records the TTLs seen on EXPIRE, for the window assertion.
	expireCalls map[string]time.Duration
	incrErr     error
	getValue    map[string]int64
}

func newLimiterFakeRedis() *limiterFakeRedis {
	return &limiterFakeRedis{
		counts:      map[string]int64{},
		expireCalls: map[string]time.Duration{},
		getValue:    map[string]int64{},
	}
}

func (f *limiterFakeRedis) Incr(_ context.Context, key string) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.incrErr != nil {
		cmd.SetErr(f.incrErr)
		return cmd
	}
	f.counts[key]++
	cmd.SetVal(f.counts[key])
	return cmd
}

func (f *limiterFakeRedis) Expire(_ context.Context, key string, ttl time.Duration) *redis.BoolCmd {
	cmd := redis.NewBoolCmd(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expireCalls[key] = ttl
	cmd.SetVal(true)
	return cmd
}

func (f *limiterFakeRedis) Get(_ context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.getValue[key]
	if !ok {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	cmd.SetVal(fmt.Sprintf("%d", v))
	return cmd
}

func (f *limiterFakeRedis) count(key string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[key]
}

func TestRedisLimiter_AllowWithinLimits(t *testing.T) {
	fake := newLimiterFakeRedis()
	l := NewRedisLimiter(fake, 3, 5)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := l.Allow(ctx, "user@example.com"); err != nil {
			t.Fatalf("request %d rejected: %v", i+1, err)
		}
	}
	if err := l.Allow(ctx, "user@example.com"); err == nil {
		t.Fatal("4th request must exceed the hourly limit (3)")
	}
}

func TestRedisLimiter_DailyLimitHits(t *testing.T) {
	fake := newLimiterFakeRedis()
	// Hourly is generous; daily is the binding constraint.
	l := NewRedisLimiter(fake, 100, 2)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := l.Allow(ctx, "user@example.com"); err != nil {
			t.Fatalf("request %d rejected: %v", i+1, err)
		}
	}
	if err := l.Allow(ctx, "user@example.com"); err == nil {
		t.Fatal("3rd request must exceed the daily limit (2)")
	}
}

// Independent subjects do not share counters.
func TestRedisLimiter_PerSubjectCounters(t *testing.T) {
	fake := newLimiterFakeRedis()
	l := NewRedisLimiter(fake, 1, 1000)
	ctx := context.Background()

	if err := l.Allow(ctx, "a@example.com"); err != nil {
		t.Fatalf("first subject rejected: %v", err)
	}
	if err := l.Allow(ctx, "b@example.com"); err != nil {
		t.Fatalf("second subject wrongly shared the first subject's counter: %v", err)
	}
	if err := l.Allow(ctx, "a@example.com"); err == nil {
		t.Fatal("second request of subject a must be limited")
	}
}

// A settings provider overrides the static limits without a rebuild.
func TestRedisLimiter_SettingsOverride(t *testing.T) {
	fake := newLimiterFakeRedis()
	l := NewRedisLimiter(fake, 100, 100)
	l.SetSettings(staticSettings{hourly: 1, daily: 1000})
	ctx := context.Background()

	if err := l.Allow(ctx, "user@example.com"); err != nil {
		t.Fatalf("first request rejected: %v", err)
	}
	if err := l.Allow(ctx, "user@example.com"); err == nil {
		t.Fatal("second request must be limited by the provider's hourly cap")
	}
}

// AllowLogin: N attempts pass, the N+1th is refused.
func TestRedisLimiter_AllowLoginWindow(t *testing.T) {
	fake := newLimiterFakeRedis()
	l := NewRedisLimiter(fake, 100, 100)
	l.SetSettings(staticSettings{loginCount: 3, loginWindow: 15})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := l.AllowLogin(ctx, "198.51.100.9"); err != nil {
			t.Fatalf("attempt %d rejected: %v", i+1, err)
		}
	}
	if err := l.AllowLogin(ctx, "198.51.100.9"); err == nil {
		t.Fatal("4th attempt from the same IP must be limited")
	}
	// A different IP is its own bucket.
	if err := l.AllowLogin(ctx, "203.0.113.1"); err != nil {
		t.Fatalf("fresh IP wrongly limited: %v", err)
	}
}

// AllowLogin fails OPEN on a Redis outage: a limiter outage must not lock
// every user out of the dashboard.
func TestRedisLimiter_AllowLoginFailsOpen(t *testing.T) {
	fake := newLimiterFakeRedis()
	fake.incrErr = context.DeadlineExceeded
	l := NewRedisLimiter(fake, 100, 100)
	if err := l.AllowLogin(context.Background(), "198.51.100.9"); err != nil {
		t.Fatalf("AllowLogin must fail open on a Redis error, got: %v", err)
	}
}

// AllowWithLimits: 0 means "skip this window".
func TestRedisLimiter_AllowWithLimitsZeroMeansUnlimited(t *testing.T) {
	fake := newLimiterFakeRedis()
	l := NewRedisLimiter(fake, 1, 1)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		if err := l.AllowWithLimits(ctx, "subject", 0, 0); err != nil {
			t.Fatalf("zero limits must be unlimited, request %d failed: %v", i+1, err)
		}
	}
	if len(fake.counts) != 0 {
		t.Fatalf("zero limits must not touch Redis, counters = %v", fake.counts)
	}
}

func TestRedisLimiter_AllowWithLimitsEnforces(t *testing.T) {
	fake := newLimiterFakeRedis()
	l := NewRedisLimiter(fake, 1000, 1000)
	ctx := context.Background()

	if err := l.AllowWithLimits(ctx, "subject", 2, 0); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := l.AllowWithLimits(ctx, "subject", 2, 0); err != nil {
		t.Fatalf("second: %v", err)
	}
	if err := l.AllowWithLimits(ctx, "subject", 2, 0); err == nil {
		t.Fatal("third request must exceed the hourly limit of 2")
	}
}

// Check reads counters without incrementing.
func TestRedisLimiter_CheckDoesNotIncrement(t *testing.T) {
	fake := newLimiterFakeRedis()
	l := NewRedisLimiter(fake, 2, 1000)
	ctx := context.Background()

	// Nothing recorded yet: Check passes.
	if err := l.Check(ctx, "user@example.com"); err != nil {
		t.Fatalf("Check with no counters must pass: %v", err)
	}
	// Seed an over-limit hourly counter directly.
	fake.mu.Lock()
	fake.getValue["ratelimit:hour:user@example.com:"+time.Now().Format("2006010215")] = 5
	fake.mu.Unlock()
	if err := l.Check(ctx, "user@example.com"); err == nil {
		t.Fatal("Check must refuse when the hourly counter is over the limit")
	}
	// The GET path must not have incremented anything.
	if len(fake.counts) != 0 {
		t.Fatalf("Check must not increment, counters = %v", fake.counts)
	}
}

// staticSettings is the SettingsProvider double.
type staticSettings struct {
	hourly      int
	daily       int
	loginCount  int
	loginWindow int
}

func (s staticSettings) DefaultRateLimitHourly() int      { return s.hourly }
func (s staticSettings) DefaultRateLimitDaily() int       { return s.daily }
func (s staticSettings) LoginRateLimitCount() int         { return s.loginCount }
func (s staticSettings) LoginRateLimitWindowMinutes() int { return s.loginWindow }
