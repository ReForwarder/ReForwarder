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

package mta

import "sync"

// ConnSlotKeeper is the MTA session-reservation registry (SF-43 bounded-spec
// item 3, the BudgetKeeper pattern): one outbound-connection slot per
// recipient domain must be reserved before the heavy SMTP session (dial,
// STARTTLS, transfer) runs, so Policy.MaxConnections is actually enforced
// instead of only carried. Reservations are exclusive — a held slot is never
// issued twice — and freed on release; a fully reserved domain defers new
// deliveries without dialing (honor-throttle, ADR-001 §8).
//
// Same in-process limitation as BudgetKeeper: every worker process keeps its
// own keeper, so an N-process deployment enforces roughly N× the connection
// bound. That is the documented limitation until reservations move to shared
// storage with the queue-manager slice.
type ConnSlotKeeper struct {
	mu   sync.Mutex
	used map[string]int
}

// NewConnSlotKeeper builds an empty reservation registry.
func NewConnSlotKeeper() *ConnSlotKeeper {
	return &ConnSlotKeeper{used: map[string]int{}}
}

// Reserve takes one connection slot for the recipient domain under the
// policy's MaxConnections (DefaultMaxConnections when the policy carries
// zero, mirroring boundAttempts). ok=false means every slot is held and the
// caller must defer. The returned release returns the slot exactly once —
// it is idempotent afterwards, so a defer-based caller cannot overshoot.
// A nil keeper (tests) always allows with a no-op release.
func (k *ConnSlotKeeper) Reserve(domain string, p Policy) (release func(), ok bool) {
	if k == nil {
		return func() {}, true
	}
	limit := p.MaxConnections
	if limit <= 0 {
		limit = DefaultMaxConnections
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.used == nil {
		k.used = map[string]int{}
	}
	if k.used[domain] >= limit {
		return nil, false
	}
	k.used[domain]++
	released := false
	return func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		if released {
			return
		}
		released = true
		if k.used[domain] > 0 {
			k.used[domain]--
		}
	}, true
}

// Held reports how many slots are currently reserved for the domain
// (operators/tests: the reservation pressure on a destination).
func (k *ConnSlotKeeper) Held(domain string) int {
	if k == nil {
		return 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.used[domain]
}
