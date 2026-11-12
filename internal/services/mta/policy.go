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

import (
	"math/rand"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
)

// Defaults for the per-domain sending policy (ADR-001 §8). A domain row may
// override max-messages-per-connection and the hourly budget; zero on the
// domain row means the built-in default / unlimited respectively.
const (
	DefaultMaxConnections     = 5
	DefaultMaxMsgsPerConn     = 10
	DefaultMaxHostAttempts    = 5
	DefaultThrottleJitterMsec = 250
)

// Policy is the resolved per-domain sending policy for one delivery.
type Policy struct {
	// MaxConnections bounds simultaneous connections the MTA opens to the
	// domain. One worker task opens at most one connection in this phase, so
	// the field is carried for the queue-manager slice (ADR-007/009, Phase 6)
	// and recorded here for that consumer.
	MaxConnections int
	// MaxMsgsPerConnection bounds how many messages share one SMTP session
	// before the session is recycled. One task carries one message, so the
	// limit is enforced by the session (MessagesSent) and only binds once
	// batch sending lands.
	MaxMsgsPerConnection int
	// MaxHostAttempts caps how many MX hosts a single delivery tries before
	// giving up — the per-domain bound on attempts (ADR-001 §8, "bounded by
	// per-domain policy").
	MaxHostAttempts int
	// HourlyBudget caps deliveries to the domain per hour window; 0 means
	// unlimited. Exhaustion defers the message (421-style) with jitter.
	HourlyBudget int
	// MsgRatePerMin paces per-message sends for this domain to n messages per
	// minute (CA-04); 0 falls back to the process-wide
	// POSTA_MTA_MSG_RATE_PER_MIN default (which is itself 0 = disabled).
	MsgRatePerMin int
	// MaxMessageSize caps this sending domain's outbound message octets
	// (SF-17, domains.max_message_size); 0 keeps the listener default.
	MaxMessageSize int64
	// ThrottleJitter is the ±jitter applied to computed defer delays.
	ThrottleJitter time.Duration
}

// PolicyFor resolves the policy for a domain row, falling back to the
// built-in defaults where the row carries zero values.
func PolicyFor(d *models.Domain) Policy {
	p := Policy{
		MaxConnections:       DefaultMaxConnections,
		MaxMsgsPerConnection: DefaultMaxMsgsPerConn,
		MaxHostAttempts:      DefaultMaxHostAttempts,
		ThrottleJitter:       DefaultThrottleJitterMsec * time.Millisecond,
	}
	if d != nil {
		if d.MaxMsgsPerConnection > 0 {
			p.MaxMsgsPerConnection = d.MaxMsgsPerConnection
		}
		if d.HourlyBudget > 0 {
			p.HourlyBudget = d.HourlyBudget
		}
		if d.MaxMessageSize > 0 {
			p.MaxMessageSize = d.MaxMessageSize
		}	}
	return p
}

// BudgetKeeper counts deliveries per domain over a sliding hourly window.
// In-process by design in this phase: every worker process keeps its own
// counter, so an N-process deployment enforces roughly N× the budget. That is
// the documented limitation until the budget moves to shared storage with the
// queue-manager slice; exactly-once delivery is unaffected (claims, R-26).
type BudgetKeeper struct {
	window time.Duration

	mu     sync.Mutex
	counts map[string][]time.Time
}

// NewBudgetKeeper builds a budget keeper with the given sliding window (one
// hour in production).
func NewBudgetKeeper(window time.Duration) *BudgetKeeper {
	if window <= 0 {
		window = time.Hour
	}
	return &BudgetKeeper{window: window, counts: map[string][]time.Time{}}
}

// Allow reports whether a delivery to domain fits the policy's hourly budget,
// returning false plus the computed defer delay when exhausted. Unlimited
// policies (budget 0) always allow.
func (b *BudgetKeeper) Allow(domain string, p Policy, now time.Time) (ok bool, deferDelay time.Duration) {
	if p.HourlyBudget <= 0 {
		return true, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	cutoff := now.Add(-b.window)
	stamps := b.counts[domain][:0]
	for _, t := range b.counts[domain] {
		if t.After(cutoff) {
			stamps = append(stamps, t)
		}
	}
	b.counts[domain] = stamps

	if len(stamps) < p.HourlyBudget {
		return true, 0
	}
	// Defer until the oldest in-window delivery ages out of the window, plus
	// jitter so a fleet of workers does not re-thunder at the same instant.
	earliest := stamps[0]
	for _, t := range stamps {
		if t.Before(earliest) {
			earliest = t
		}
	}
	delay := earliest.Add(b.window).Sub(now)
	if delay < 0 {
		delay = 0
	}
	return false, deferWithJitter(delay, p.ThrottleJitter)
}

// Record stamps one delivery to the domain into the budget window. Called
// when a delivery to the domain is initiated (not only when it succeeds), so
// a failure storm cannot bypass the budget.
func (b *BudgetKeeper) Record(domain string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.counts[domain] = append(b.counts[domain], now)
}

// MessageRateKeeper paces per-message sends per SENDING domain (CA-04): a
// minimum-interval rate limiter (token bucket of depth one) consulted by the
// delivery executor immediately before each MAIL FROM, so a burst of queued
// mail to a freshly warmed domain goes out at the configured rate instead of
// all at once. The rate is configured process-wide via
// POSTA_MTA_MSG_RATE_PER_MIN (0 = disabled); per-domain overrides ride the
// Policy.MsgRatePerMin field when a domain-row column lands.
//
// Same in-process limitation as BudgetKeeper: each worker process keeps its
// own pacer, so an N-process deployment paces at roughly N× the rate.
type MessageRateKeeper struct {
	mu   sync.Mutex
	next map[string]time.Time // sending domain → earliest instant the next message may start
}

// NewMessageRateKeeper builds a per-sending-domain message pacer.
func NewMessageRateKeeper() *MessageRateKeeper {
	return &MessageRateKeeper{next: map[string]time.Time{}}
}

// Wait returns how long the next message for sendingDomain must wait to hold
// the rate (messages per minute; <=0 disables pacing) and reserves the slot.
// The reservation always advances by one interval from the reserved start, so
// callers that ignore the returned wait still pace correctly.
func (k *MessageRateKeeper) Wait(sendingDomain string, ratePerMin int, now time.Time) time.Duration {
	if ratePerMin <= 0 {
		return 0
	}
	interval := time.Minute / time.Duration(ratePerMin)
	if interval <= 0 {
		interval = time.Second
	}
	k.mu.Lock()
	defer k.mu.Unlock()

	var wait time.Duration
	if nxt, ok := k.next[sendingDomain]; ok && nxt.After(now) {
		wait = nxt.Sub(now)
	}
	start := now.Add(wait)
	k.next[sendingDomain] = start.Add(interval)
	return wait
}

// deferWithJitter adds ±jitter to a defer delay, never returning negative.
func deferWithJitter(delay, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return delay
	}
	span := 2 * jitter
	delta := time.Duration(rand.Int63n(int64(span)+1)) - jitter
	out := delay + delta
	if out < 0 {
		out = 0
	}
	return out
}

// DeferDelay computes a jittered defer delay for throttle responses
// (honor-421/450, ADR-001 §8). base is the minimum back-off before the next
// attempt; the result stays within [base-jitter, base+jitter].
func DeferDelay(base, jitter time.Duration) time.Duration {
	if base < 0 {
		base = 0
	}
	return deferWithJitter(base, jitter)
}
