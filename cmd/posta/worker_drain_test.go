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

package main

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// SF-109 regression coverage: the embedded asynq worker handle was discarded
// before the fix, so shutdownServer could never drain it. The two tests below
// cover the two halves of the fix:
//
//   - TestShutdownServerNilEmbeddedWorker: the shutdown path's nil guard —
//     every resource unset, shutdownServer must complete without panicking.
//   - TestAsynqShutdownDrainsInFlightTask (Redis-gated, POSTA_TEST_REDIS_URL,
//     same gate as internal/cron/lease_test.go): the drain mechanism
//     shutdownServer now relies on — Start -> Shutdown must let the in-flight
//     handler finish and must not re-deliver the task afterwards.

func TestShutdownServerNilEmbeddedWorker(t *testing.T) {
	res := &serverResources{}
	// The embedded worker is nil (not started / failed to start): the SF-109
	// drain step must be skipped cleanly instead of dereferencing the handle.
	shutdownServer(res)
}

// drainTestTaskType names the task used by the drain test; it is local to the
// test and never collides with the production mux.
const drainTestTaskType = "test:sf109-drain"

func TestAsynqShutdownDrainsInFlightTask(t *testing.T) {
	url := os.Getenv("POSTA_TEST_REDIS_URL")
	if url == "" {
		t.Skip("POSTA_TEST_REDIS_URL not set")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	// asynq takes its own conn-opt wrapper around the same Redis instance.
	connOpt := asynq.RedisClientOpt{Addr: opt.Addr, Username: opt.Username, Password: opt.Password, DB: opt.DB}

	var entered, completed atomic.Int32
	var runs atomic.Int32

	enteredCh := make(chan struct{}) // closed when the handler holds the slot
	releaseCh := make(chan struct{}) // closed when the handler may finish

	mux := asynq.NewServeMux()
	mux.HandleFunc(drainTestTaskType, func(ctx context.Context, t *asynq.Task) error {
		runs.Add(1)
		if entered.Add(1) == 1 {
			close(enteredCh)
		}
		<-releaseCh // hold the slot until the shutdown race begins
		completed.Add(1)
		return nil
	})

	srv := asynq.NewServer(connOpt, asynq.Config{
		Concurrency:     1,
		Queues:          map[string]int{"default": 1},
		ShutdownTimeout: 5 * time.Second,
	})
	client := asynq.NewClient(connOpt)
	defer func() { _ = client.Close() }()

	if _, err := client.Enqueue(asynq.NewTask(drainTestTaskType, nil), asynq.Queue("default")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := srv.Start(mux); err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-enteredCh:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never started")
	}

	// The exact sequence shutdownServer performs on the embedded worker:
	// Shutdown with a bounded wait. The handler is mid-flight, so a
	// non-draining shutdown would kill it and re-deliver the task later.
	done := make(chan struct{})
	go func() {
		srv.Shutdown()
		close(done)
	}()

	select {
	case <-time.After(0):
		// give Shutdown a moment to begin before releasing the handler
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseCh)

	deadline := time.Now().Add(5 * time.Second)
	for completed.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("in-flight handler did not finish during drain")
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return after handler finished")
	}

	// No re-delivery: the completed task must not run again.
	time.Sleep(1500 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("task ran %d times, want exactly 1", got)
	}
}
