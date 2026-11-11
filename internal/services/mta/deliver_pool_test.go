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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
)

// Pool selection rotates round-robin across the pool's enabled members and
// skips blank entries.
func TestExecutorSelectsPoolIPRoundRobin(t *testing.T) {
	e := NewExecutor(nil, nil, nil)
	spec := DeliverySpec{BindIP: "192.0.2.9", PoolIPs: []string{"192.0.2.1", "", "192.0.2.2", "192.0.2.3"}}

	want := []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.1", "192.0.2.2"}
	for i, w := range want {
		if got := e.selectBindIP(spec); got != w {
			t.Fatalf("selection %d = %q, want %q (blank entries must be skipped)", i+1, got, w)
		}
	}
}

// A pool whose entries are all blank falls back to the domain's static bind
// IP; no pool at all uses it directly.
func TestExecutorFallsBackToStaticBindIP(t *testing.T) {
	e := NewExecutor(nil, nil, nil)
	spec := DeliverySpec{BindIP: "192.0.2.9", PoolIPs: []string{"", "   "}}
	if got := e.selectBindIP(spec); got != "192.0.2.9" {
		t.Fatalf("all-blank pool = %q, want the static bind IP", got)
	}
	if got := e.selectBindIP(DeliverySpec{BindIP: "192.0.2.9"}); got != "192.0.2.9" {
		t.Fatalf("no pool = %q, want the static bind IP", got)
	}
	if got := e.selectBindIP(DeliverySpec{}); got != "" {
		t.Fatalf("nothing configured = %q, want \"\" (OS default)", got)
	}
}

// Deliver actually dials from the selected pool member: two deliveries
// rotate across two pool IPs, and every attempt row records the source IP it
// went out from.
func TestDeliverDialsFromPoolMembersAndStampsSourceIP(t *testing.T) {
	// The pool members must be addresses the test host can actually bind:
	// the loopback range (the whole 127/8 is bindable on Linux) stands in for
	// the operator's sending IPs.
	f := newDeliverFixture(t, withCaps("PIPELINING"))
	spec := testSpec()
	spec.PoolIPs = []string{"127.0.0.2", "127.0.0.3"}

	for i := 0; i < 2; i++ {
		res, err := f.executor.Deliver(ctxOf(t), spec, testEnvelope("a@dest.example"))
		if err != nil {
			t.Fatalf("delivery %d failed: %v", i+1, err)
		}
		if !res.Delivered {
			t.Fatalf("delivery %d not delivered: %+v", i+1, res)
		}
	}
	rows := f.attempts.all()
	if len(rows) < 2 {
		t.Fatalf("attempt rows = %d, want >= 2", len(rows))
	}
	if rows[0].SourceIP != "127.0.0.2" || rows[1].SourceIP != "127.0.0.3" {
		t.Fatalf("source IPs = %q, %q; want the pool rotation 127.0.0.2, 127.0.0.3",
			rows[0].SourceIP, rows[1].SourceIP)
	}
	// The remote MX IP stays on IP — SourceIP is our outbound address.
	if rows[0].IP == rows[0].SourceIP {
		t.Fatalf("attempt IP must stay the remote MX address, got %q for both", rows[0].IP)
	}
}

// Pool selection is per delivery spec: two domains each carrying their own
// pool never borrow each other's source IPs (isolation).
func TestPoolSelectionIsolatesDomains(t *testing.T) {
	e := NewExecutor(nil, nil, nil)
	poolA := DeliverySpec{SenderDomain: "a.example", PoolIPs: []string{"192.0.2.1", "192.0.2.2"}}
	poolB := DeliverySpec{SenderDomain: "b.example", PoolIPs: []string{"198.51.100.1", "198.51.100.2"}}

	for i := 0; i < 4; i++ {
		a := e.selectBindIP(poolA)
		if !strings.HasPrefix(a, "192.0.2.") {
			t.Fatalf("domain a picked foreign source %q", a)
		}
		b := e.selectBindIP(poolB)
		if !strings.HasPrefix(b, "198.51.100.") {
			t.Fatalf("domain b picked foreign source %q", b)
		}
	}
}

// An active warm-up ramp defers deliveries once its hourly cap is exhausted —
// without contacting the MX — and the attempt row names the warm-up cap.
func TestDeliverWarmupCapDefersWithoutMX(t *testing.T) {
	f := newDeliverFixture(t)
	// A schedule capped at 1 message/day: the hourly cap is 1, so the second
	// delivery within the hour defers.
	s := &WarmupSchedule{StartVolume: 1, DailyMultiplier: 1, Days: 5, StartedAt: rampDay0()}

	spec := testSpec()
	spec.Warmup = s

	if _, err := f.executor.Deliver(ctxOf(t), spec, testEnvelope("first@dest.example")); err != nil {
		t.Fatalf("first delivery must fit the cap: %v", err)
	}
	res, err := f.executor.Deliver(ctxOf(t), spec, testEnvelope("second@dest.example"))
	if err == nil {
		t.Fatalf("second delivery must defer under the warm-up cap")
	}
	if res != nil && res.Delivered {
		t.Fatal("deferred delivery must not report delivered")
	}
	var se *email.SendError
	if !errors.As(err, &se) || se.Code != 450 {
		t.Fatalf("defer error = %v, want a 450 POLICY SendError", err)
	}

	rows := f.attempts.all()
	deferred := 0
	for _, r := range rows {
		if r.Outcome == models.MTAAttemptDeferredBudget {
			deferred++
			if !strings.Contains(r.ResponseText, "warm-up cap 1/day") {
				t.Fatalf("deferred row text must name the warm-up cap: %q", r.ResponseText)
			}
		}
	}
	if deferred != 1 {
		t.Fatalf("deferred attempt rows = %d, want 1", deferred)
	}
	// No MX was ever contacted for the deferred message: only two attempt
	// rows exist (one delivered + one deferred), no session failures.
	if f.attempts.count() != 2 {
		t.Fatalf("attempt rows = %d, want 2 (defer must not dial the MX)", f.attempts.count())
	}
}

// After the ramp completes the domain's own budget stands: the same spec
// pattern that deferred under the cap delivers through the MX again.
func TestDeliverWarmupCapExpiresAfterRampDays(t *testing.T) {
	f := newDeliverFixture(t)
	base := rampDay0()
	// Ramp over before any delivery: no cap applies at all.
	s := &WarmupSchedule{StartVolume: 1, DailyMultiplier: 1, Days: 2, StartedAt: base.Add(-3 * 24 * time.Hour)}

	spec := testSpec()
	spec.Warmup = s
	for i := 0; i < 3; i++ {
		if _, err := f.executor.Deliver(ctxOf(t), spec, testEnvelope("a@dest.example")); err != nil {
			t.Fatalf("delivery %d after ramp completion must not defer: %v", i+1, err)
		}
	}
}

func ctxOf(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}
