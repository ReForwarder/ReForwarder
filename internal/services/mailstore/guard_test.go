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

package mailstore

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestConcurrentSessionGuardBlocksSecondWriter: while one session holds the
// folder's write slot, a second Acquire blocks until release and a TryAcquire
// fails immediately.
func TestConcurrentSessionGuardBlocksSecondWriter(t *testing.T) {
	g := NewConcurrentSessionGuard()
	rel1, err := g.Acquire(context.Background(), 7)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	if _, ok := g.TryAcquire(7); ok {
		t.Fatalf("TryAcquire took the slot while it was held")
	}

	// Second writer blocks until the first releases.
	acquired := make(chan struct{})
	go func() {
		rel2, err := g.Acquire(context.Background(), 7)
		if err != nil {
			t.Errorf("second acquire: %v", err)
			return
		}
		rel2()
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatalf("second writer acquired while the slot was still held")
	case <-time.After(30 * time.Millisecond):
	}
	rel1()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatalf("second writer never acquired after release")
	}
}

// TestConcurrentSessionGuardContextCancel: a cancelled waiter never takes the
// slot — the holder keeps it and the waiter returns the ctx error.
func TestConcurrentSessionGuardContextCancel(t *testing.T) {
	g := NewConcurrentSessionGuard()
	rel, err := g.Acquire(context.Background(), 42)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer rel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx, 42); err == nil {
		t.Fatalf("cancelled acquire must fail")
	}
	// The slot is still exclusively held; TryAcquire from "another session"
	// must fail even after the cancelled waiter gave up.
	if _, ok := g.TryAcquire(42); ok {
		t.Fatalf("cancelled waiter stole the slot")
	}
}

// TestConcurrentSessionGuardPerFolder: different folders lock independently.
func TestConcurrentSessionGuardPerFolder(t *testing.T) {
	g := NewConcurrentSessionGuard()
	relA, err := g.Acquire(context.Background(), 1)
	if err != nil {
		t.Fatalf("acquire folder 1: %v", err)
	}
	defer relA()
	relB, ok := g.TryAcquire(2)
	if !ok {
		t.Fatalf("folder 2 must be independent of folder 1")
	}
	relB()
}

// TestConcurrentSessionGuardSerializesExpunges proves the service-level
// contract the ask names: two sessions cannot expunge the same folder
// concurrently — the second Expunge waits for the first to commit.
func TestConcurrentSessionGuardSerializesExpunges(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "expunge-guard@post", 0)
	folder := inboxFolder(t, svc, mb)

	// An outside holder simulates another session's expunge in progress.
	rel, err := svc.LockFolderForWrite(context.Background(), folder.ID)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	done := make(chan *ExpungeResult, 1)
	go func() {
		// No \Deleted messages — the point is the serialization, not the purge.
		res, err := svc.Expunge(folder.ID, 1, 0)
		if err != nil {
			t.Errorf("expunge: %v", err)
			done <- nil
			return
		}
		done <- res
	}()
	select {
	case <-done:
		t.Fatalf("Expunge ran while another session held the folder write lock")
	case <-time.After(50 * time.Millisecond):
	}
	rel()
	select {
	case res := <-done:
		if res == nil || res.Expunged != 0 {
			t.Fatalf("expunge result = %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Expunge never completed after release")
	}
}

// TestConcurrentSessionGuardRaces hammers Acquire/TryAcquire/release across
// many goroutines and folders — runs under -race in the battery.
func TestConcurrentSessionGuardRaces(t *testing.T) {
	g := NewConcurrentSessionGuard()
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			folder := uint(id%4 + 1)
			for i := 0; i < 100; i++ {
				rel, err := g.Acquire(context.Background(), folder)
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				if rel2, ok := g.TryAcquire(folder); ok {
					t.Errorf("TryAcquire succeeded while the slot was held")
					rel2()
				}
				rel()
			}
		}(w)
	}
	wg.Wait()
}
