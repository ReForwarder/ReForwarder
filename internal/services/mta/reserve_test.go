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
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
)

// SF-43 bounded-spec item 3: MTA session reservations. The ConnSlotKeeper
// holds one exclusive outbound-connection slot per recipient domain before
// the heavy SMTP session runs; the delivery executor defers (without
// dialing) when a domain's slots are fully reserved.

// TestConnSlotReservationExclusivity pins the exclusivity contract: slots up
// to the policy's MaxConnections are issued exactly once each — a held slot
// is never double-issued — release frees it for the next reservation, and a
// double release cannot overshoot the freed count.
func TestConnSlotReservationExclusivity(t *testing.T) {
	k := NewConnSlotKeeper()
	p := Policy{MaxConnections: 2}

	r1, ok1 := k.Reserve("example.com", p)
	r2, ok2 := k.Reserve("example.com", p)
	if !ok1 || !ok2 {
		t.Fatalf("reservations 1/2 ok = %v/%v, want both granted", ok1, ok2)
	}
	if k.Held("example.com") != 2 {
		t.Fatalf("Held = %d, want 2", k.Held("example.com"))
	}
	if r3, ok := k.Reserve("example.com", p); ok {
		t.Fatal("the third reservation must be refused while both slots are held")
	} else if r3 != nil {
		t.Fatal("a refused reservation must return no release func")
	}

	// Release frees the slot for the next reservation.
	r1()
	if k.Held("example.com") != 1 {
		t.Fatalf("Held after one release = %d, want 1", k.Held("example.com"))
	}
	r3, ok3 := k.Reserve("example.com", p)
	if !ok3 {
		t.Fatal("the released slot must be reservable again")
	}
	// A double release must not overshoot: Held stays correct.
	r1()
	r1() // idempotent
	if k.Held("example.com") != 2 {
		t.Fatalf("Held after a double release = %d, want 2 (no phantom slot)", k.Held("example.com"))
	}
	r2()
	r3()
	if k.Held("example.com") != 0 {
		t.Fatalf("Held after full release = %d, want 0", k.Held("example.com"))
	}
}

// TestConnSlotReservationDomainIndependentAndDefaultLimit pins scoping:
// another destination's budget is untouched by one domain's held slots, and
// a zero-value policy falls back to DefaultMaxConnections.
func TestConnSlotReservationDomainIndependentAndDefaultLimit(t *testing.T) {
	k := NewConnSlotKeeper()

	if _, ok := k.Reserve("example.com", Policy{}); !ok {
		t.Fatal("zero-value policy must reserve against the default limit")
	}
	if k.Held("other.test") != 0 {
		t.Fatalf("Held(other.test) = %d, want 0 — reservations are per domain", k.Held("other.test"))
	}

	// The default limit (5) grants five and refuses the sixth.
	var releases []func()
	for i := 0; i < DefaultMaxConnections; i++ {
		rel, ok := k.Reserve("other.test", Policy{})
		if !ok {
			t.Fatalf("reservation %d refused under the default limit", i+1)
		}
		releases = append(releases, rel)
	}
	if _, ok := k.Reserve("other.test", Policy{}); ok {
		t.Fatal("the sixth reservation must be refused at the default limit")
	}
	for _, rel := range releases {
		rel()
	}
	if k.Held("other.test") != 0 {
		t.Fatalf("Held after releasing all = %d, want 0", k.Held("other.test"))
	}
}

// TestConnSlotReservationNilKeeperAllows pins the nil guard: a nil keeper
// (Executor built without one in tests) always allows with a no-op release.
func TestConnSlotReservationNilKeeperAllows(t *testing.T) {
	var k *ConnSlotKeeper
	release, ok := k.Reserve("example.com", Policy{})
	if !ok {
		t.Fatal("nil keeper must allow")
	}
	release() // no-op, must not panic
	if k.Held("example.com") != 0 {
		t.Fatal("nil keeper must report zero held slots")
	}
}

// TestDirectDeliverySlotExhaustionDefers pins the executor wiring: with the
// recipient domain's connection slots fully reserved, Deliver defers with a
// transient 450 — no MX session is dialed — and records a durable
// deferred_slots attempt row.
func TestDirectDeliverySlotExhaustionDefers(t *testing.T) {
	f := newDeliverFixture(t)

	// Hold every connection slot for the recipient domain before delivering.
	for i := 0; i < DefaultMaxConnections; i++ {
		if _, ok := f.executor.slots.Reserve("example.com", Policy{}); !ok {
			t.Fatalf("holding slot %d failed", i+1)
		}
	}

	res, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com"))
	if err == nil {
		t.Fatal("a fully reserved domain must defer the delivery")
	}
	if res.Delivered {
		t.Fatal("deferred delivery must never report delivered")
	}
	var se *email.SendError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want SendError", err)
	}
	if se.Permanent() {
		t.Fatal("a slot defer is transient — it must be retried later")
	}
	if se.Code != 450 || se.Stage != "POLICY" {
		t.Fatalf("defer reply = %d/%s, want 450/POLICY", se.Code, se.Stage)
	}
	if se.Msg != "connection budget exhausted for domain example.com" {
		t.Fatalf("defer reply text = %q, must name the connection budget and domain", se.Msg)
	}
	if n := f.srv.sessionCount(); n != 0 {
		t.Fatalf("MX sessions = %d, want 0 (no dial while fully reserved)", n)
	}
	if held := f.executor.slots.Held("example.com"); held != DefaultMaxConnections {
		t.Fatalf("Held after deferral = %d, want %d (the delivery must not steal a slot)", held, DefaultMaxConnections)
	}
	rows := f.attempts.all()
	if len(rows) != 1 {
		t.Fatalf("attempt rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Outcome != models.MTAAttemptDeferredSlots {
		t.Fatalf("outcome = %q, want deferred_slots", row.Outcome)
	}
	if row.ResponseCode != 450 {
		t.Fatalf("response code = %d, want 450", row.ResponseCode)
	}
}

// TestDirectDeliveryReleasesSlotAfterSession pins the release side of the
// executor wiring: a delivered session returns its slot — Held drops to zero
// and the next delivery for the same domain proceeds.
func TestDirectDeliveryReleasesSlotAfterSession(t *testing.T) {
	f := newDeliverFixture(t)

	if _, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com")); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if held := f.executor.slots.Held("example.com"); held != 0 {
		t.Fatalf("Held after delivery = %d, want 0 (the session released its slot)", held)
	}
	if _, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt2@example.com")); err != nil {
		t.Fatalf("second delivery must proceed after the slot released: %v", err)
	}
	if n := f.srv.sessionCount(); n != 2 {
		t.Fatalf("MX sessions = %d, want 2", n)
	}
}
