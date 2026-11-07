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
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// The hourly budget: deliveries fit until the limit, then defer; the sliding
// window frees the budget as stamps age out.
func TestBudgetKeeperHourlyWindow(t *testing.T) {
	b := NewBudgetKeeper(time.Hour)
	p := Policy{HourlyBudget: 2}
	domain := "example.com"
	now := time.Now()

	for i := 0; i < 2; i++ {
		ok, delay := b.Allow(domain, p, now)
		if !ok {
			t.Fatalf("delivery %d must fit the budget (delay %s)", i+1, delay)
		}
		b.Record(domain, now)
	}
	ok, delay := b.Allow(domain, p, now.Add(time.Minute))
	if ok {
		t.Fatal("delivery beyond the hourly budget must be denied")
	}
	if delay <= 0 {
		t.Fatalf("defer delay = %s, want positive (time until the window frees)", delay)
	}

	// Fifty-nine minutes later the first stamps are still inside the window.
	if ok, _ := b.Allow(domain, p, now.Add(59*time.Minute)); ok {
		t.Fatal("budget must still be exhausted 59 minutes in")
	}
	// After the window slides past every stamp the budget frees.
	if ok, _ := b.Allow(domain, p, now.Add(2*time.Hour)); !ok {
		t.Fatal("budget must free after the window slides past the stamps")
	}
}

// Budget 0 (the unlimited default) never defers.
func TestBudgetKeeperUnlimitedWhenBudgetZero(t *testing.T) {
	b := NewBudgetKeeper(time.Hour)
	for i := 0; i < 1000; i++ {
		if ok, _ := b.Allow("example.com", Policy{}, time.Now()); !ok {
			t.Fatal("unlimited policy must always allow")
		}
	}
}

// Budgets are per domain: exhausting one domain must not throttle another.
func TestBudgetKeeperIsPerDomain(t *testing.T) {
	b := NewBudgetKeeper(time.Hour)
	p := Policy{HourlyBudget: 1}
	now := time.Now()
	b.Record("a.example.com", now)
	if ok, _ := b.Allow("a.example.com", p, now); ok {
		t.Fatal("exhausted domain must defer")
	}
	if ok, _ := b.Allow("b.example.com", p, now); !ok {
		t.Fatal("a different domain must not inherit the exhausted budget")
	}
}

// PolicyFor applies the built-in defaults for zero-valued domain rows and
// honors the row's overrides.
func TestPolicyForDefaultsAndOverrides(t *testing.T) {
	def := PolicyFor(nil)
	if def.MaxConnections != DefaultMaxConnections ||
		def.MaxMsgsPerConnection != DefaultMaxMsgsPerConn ||
		def.MaxHostAttempts != DefaultMaxHostAttempts {
		t.Fatalf("defaults not applied: %+v", def)
	}
	if def.HourlyBudget != 0 {
		t.Fatalf("default hourly budget must be unlimited, got %d", def.HourlyBudget)
	}

	row := &models.Domain{MaxMsgsPerConnection: 25, HourlyBudget: 500}
	p := PolicyFor(row)
	if p.MaxMsgsPerConnection != 25 {
		t.Fatalf("MaxMsgsPerConnection override ignored: %d", p.MaxMsgsPerConnection)
	}
	if p.HourlyBudget != 500 {
		t.Fatalf("HourlyBudget override ignored: %d", p.HourlyBudget)
	}
	if p.MaxHostAttempts != DefaultMaxHostAttempts {
		t.Fatalf("MaxHostAttempts default lost: %d", p.MaxHostAttempts)
	}
}

// DeferDelay stays within the base ± jitter band and never goes negative.
func TestDeferDelayJitterBounded(t *testing.T) {
	base := 5 * time.Minute
	jitter := time.Minute
	for i := 0; i < 200; i++ {
		d := DeferDelay(base, jitter)
		if d < base-jitter || d > base+jitter {
			t.Fatalf("defer delay %s outside [%s, %s]", d, base-jitter, base+jitter)
		}
	}
	if d := DeferDelay(time.Second, 10*time.Minute); d < 0 {
		t.Fatalf("defer delay must never be negative, got %s", d)
	}
	if d := DeferDelay(3*time.Second, 0); d != 3*time.Second {
		t.Fatalf("zero jitter must return the base delay, got %s", d)
	}
}
