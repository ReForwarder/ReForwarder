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
	"fmt"
	"sync"
)

// ConcurrentSessionGuard (OB-4 wave W1, ADR-004 decisions 1/5): per-folder
// exclusive write locking so two mail sessions cannot run destructive
// operations (EXPUNGE, and the servers' own append+notify critical sections)
// interleaved. The guard is in-process, like the ban snapshot: cross-replica
// serialization rides the database (folder-row locks on the atomic UID claim,
// the flag CAS on mod_seq, and Expunge's row locks), while this guard gives
// one process's sessions a clean ordering and the IDLE layer a single writer
// to observe.
//
// Implementation: one buffered one-capacity token channel per folder — send
// acquires, receive releases. Context-aware Acquire therefore needs no
// goroutine-leaking mutex gymnastics: a cancelled waiter simply never sends.
type ConcurrentSessionGuard struct {
	mu      sync.Mutex
	entries map[uint]*guardEntry
}

type guardEntry struct {
	token chan struct{}
}

// NewConcurrentSessionGuard builds an empty guard.
func NewConcurrentSessionGuard() *ConcurrentSessionGuard {
	return &ConcurrentSessionGuard{entries: make(map[uint]*guardEntry)}
}

func (g *ConcurrentSessionGuard) entry(folderID uint) *guardEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.entries[folderID]
	if !ok {
		e = &guardEntry{token: make(chan struct{}, 1)}
		g.entries[folderID] = e
	}
	return e
}

// Acquire blocks until the folder's write slot is free or ctx is done. The
// returned release func must be called exactly once (defer it at the call
// site) and never before Acquire returns.
func (g *ConcurrentSessionGuard) Acquire(ctx context.Context, folderID uint) (release func(), err error) {
	e := g.entry(folderID)
	select {
	case e.token <- struct{}{}:
		return func() { <-e.token }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("mailstore: acquire folder %d write lock: %w", folderID, ctx.Err())
	}
}

// TryAcquire takes the folder's write slot without waiting.
func (g *ConcurrentSessionGuard) TryAcquire(folderID uint) (release func(), ok bool) {
	e := g.entry(folderID)
	select {
	case e.token <- struct{}{}:
		return func() { <-e.token }, true
	default:
		return nil, false
	}
}

// acquireBlocking is the no-context variant for entry points whose callers
// predate ctx threading (Expunge); it waits indefinitely — the release path
// is deferred by the holder, so the wait is bounded by the holder's body.
func (g *ConcurrentSessionGuard) acquireBlocking(folderID uint) func() {
	e := g.entry(folderID)
	e.token <- struct{}{}
	return func() { <-e.token }
}

// LockFolderForWrite exposes the guard to the mail-protocol servers (OB-4):
// they wrap their EXPUNGE and append+notify critical sections with it.
func (s *Service) LockFolderForWrite(ctx context.Context, folderID uint) (release func(), err error) {
	return s.guard.Acquire(ctx, folderID)
}

// TryLockFolderForWrite is the non-blocking sibling of LockFolderForWrite.
func (s *Service) TryLockFolderForWrite(folderID uint) (release func(), ok bool) {
	return s.guard.TryAcquire(folderID)
}
