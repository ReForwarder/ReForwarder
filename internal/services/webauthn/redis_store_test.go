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

package webauthn

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// SF-91: direct unit tests for RedisChallengeStore. miniredis is not a
// dependency of this module, so the store is exercised through a fake
// redis.Cmdable (SF-91's interface-fake alternative): the store only uses SET
// and GETDEL, which the fake implements with an in-process map that honors
// TTLs.

// fakeCmdable is a redis.Cmdable whose promoted interface is nil — any command
// the fake does not implement panics loudly, which pins the store to the two
// commands these tests cover.
type fakeCmdable struct {
	redis.Cmdable

	mu      sync.Mutex
	values  map[string]string
	expires map[string]time.Time
	setErr  error
	getErr  error
	// recorded TTLs of the SET calls, keyed by the raw command key.
	setTTLs map[string]time.Duration
}

func newFakeCmdable() *fakeCmdable {
	return &fakeCmdable{
		values:  map[string]string{},
		expires: map[string]time.Time{},
		setTTLs: map[string]time.Duration{},
	}
}

func (f *fakeCmdable) Set(_ context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		cmd.SetErr(f.setErr)
		return cmd
	}
	switch v := value.(type) {
	case string:
		f.values[key] = v
	case []byte:
		f.values[key] = string(v)
	default:
		panic(fmt.Sprintf("fakeCmdable: unsupported SET value type %T", value))
	}
	f.setTTLs[key] = ttl
	if ttl > 0 {
		f.expires[key] = time.Now().Add(ttl)
	} else {
		delete(f.expires, key)
	}
	return cmd
}

func (f *fakeCmdable) GetDel(_ context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		cmd.SetErr(f.getErr)
		return cmd
	}
	if exp, ok := f.expires[key]; ok && time.Now().After(exp) {
		delete(f.values, key)
		delete(f.expires, key)
	}
	v, ok := f.values[key]
	if !ok {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	delete(f.values, key)
	delete(f.expires, key)
	cmd.SetVal(v)
	return cmd
}

func TestRedisChallengeStore_SaveAndTake(t *testing.T) {
	fake := newFakeCmdable()
	store := NewRedisChallengeStore(fake)
	ctx := context.Background()

	if err := store.Save(ctx, "ceremony-1", []byte(`{"challenge":"abc"}`), 5*time.Minute); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The key is namespaced so ceremony state cannot collide with other Redis
	// users of this deployment.
	fake.mu.Lock()
	if _, ok := fake.values["webauthn:challenge:ceremony-1"]; !ok {
		t.Fatal("Save did not write under the webauthn:challenge: prefix")
	}
	if got := fake.setTTLs["webauthn:challenge:ceremony-1"]; got != 5*time.Minute {
		t.Fatalf("Save TTL = %v, want 5m", got)
	}
	fake.mu.Unlock()

	payload, err := store.Take(ctx, "ceremony-1")
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if string(payload) != `{"challenge":"abc"}` {
		t.Fatalf("payload = %q", payload)
	}
}

// Take is one-shot: a challenge can never be replayed.
func TestRedisChallengeStore_TakeIsOneShot(t *testing.T) {
	fake := newFakeCmdable()
	store := NewRedisChallengeStore(fake)
	ctx := context.Background()

	if err := store.Save(ctx, "ceremony-2", []byte("payload"), time.Minute); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.Take(ctx, "ceremony-2"); err != nil {
		t.Fatalf("first Take: %v", err)
	}
	if _, err := store.Take(ctx, "ceremony-2"); err == nil {
		t.Fatal("second Take must fail: the challenge was consumed")
	}
}

// An unknown or expired challenge reads as an error, not as empty bytes.
func TestRedisChallengeStore_TakeUnknownKey(t *testing.T) {
	store := NewRedisChallengeStore(newFakeCmdable())
	if _, err := store.Take(context.Background(), "never-saved"); err == nil {
		t.Fatal("Take of an unknown challenge must fail")
	}
}

// The Redis error path propagates: Save failing must reach the caller so the
// ceremony aborts instead of silently continuing without stored state.
func TestRedisChallengeStore_SetErrorPropagates(t *testing.T) {
	fake := newFakeCmdable()
	fake.setErr = assertAnError
	store := NewRedisChallengeStore(fake)
	if err := store.Save(context.Background(), "k", []byte("v"), time.Minute); err == nil {
		t.Fatal("Save must surface the Redis error")
	}
}

// An expired entry must not be returned by Take.
func TestRedisChallengeStore_TakeHonoursTTL(t *testing.T) {
	fake := newFakeCmdable()
	store := NewRedisChallengeStore(fake)
	ctx := context.Background()

	if err := store.Save(ctx, "ceremony-3", []byte("payload"), time.Nanosecond); err != nil {
		t.Fatalf("Save with a tiny TTL: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // let the entry expire
	if _, err := store.Take(ctx, "ceremony-3"); err == nil {
		t.Fatal("Take of an expired challenge must fail")
	}
}

// assertAnError is a stand-in Redis failure.
var assertAnError = context.DeadlineExceeded
