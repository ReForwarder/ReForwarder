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

package verify

import (
	"context"
	"net"
	"testing"
)

// iprev forward-confirmation matrix (SF-32 partial, ADR-002 decision 6):
// pass when a PTR name forward-resolves to the connecting IP, fail when PTR
// records exist but none confirms, none when there is no PTR record (or no
// client IP), temperror on transient resolver failures.

func iprevCheck(t *testing.T, dns *fakeDNS, clientIP string) IPRevVerdict {
	t.Helper()
	return (&IPRevEvaluator{Resolver: dns}).Check(context.Background(), clientIP)
}

func TestIPRevForwardConfirmed(t *testing.T) {
	dns := newFakeDNS().
		withPTR("192.0.2.10", "mail.sender.test.").
		withIP("mail.sender.test.", net.ParseIP("192.0.2.10"))

	v := iprevCheck(t, dns, "192.0.2.10")
	if v.Result != IPRevPass || v.PTR != "mail.sender.test" {
		t.Fatalf("forward-confirmed iprev = %+v, want pass with PTR", v)
	}
}

// The defining negative: a PTR record exists, but its name resolves
// somewhere else (or nowhere) — no forward confirmation.
func TestIPRevPTRNotForwardConfirmed(t *testing.T) {
	dns := newFakeDNS().
		withPTR("198.51.100.9", "generic.shared-host.test.").
		withIP("generic.shared-host.test.", net.ParseIP("203.0.113.77"))

	v := iprevCheck(t, dns, "198.51.100.9")
	if v.Result != IPRevFail {
		t.Fatalf("unconfirmed PTR = %s (%s), want fail", v.Result, v.Reason)
	}
	if v.PTR != "" {
		t.Fatalf("fail verdict carries PTR %q", v.PTR)
	}
}

// PTR name that does not resolve at all (definitive NXDOMAIN): the record
// exists but cannot confirm — fail, not temperror.
func TestIPRevPTRNameDoesNotResolve(t *testing.T) {
	dns := newFakeDNS().withPTR("198.51.100.9", "stale.example.test.")

	v := iprevCheck(t, dns, "198.51.100.9")
	if v.Result != IPRevFail {
		t.Fatalf("unresolvable PTR name = %s (%s), want fail", v.Result, v.Reason)
	}
}

// Multiple PTR records: one confirming name is enough for pass.
func TestIPRevMultiplePTRsOneMatches(t *testing.T) {
	dns := newFakeDNS().
		withPTR("192.0.2.20", "one.example.test.", "two.example.test.").
		withIP("one.example.test.", net.ParseIP("203.0.113.1")).
		withIP("two.example.test.", net.ParseIP("192.0.2.20"))

	v := iprevCheck(t, dns, "192.0.2.20")
	if v.Result != IPRevPass || v.PTR != "two.example.test" {
		t.Fatalf("second PTR confirms = %+v, want pass via two.example.test", v)
	}
}

func TestIPRevNoPTRRecord(t *testing.T) {
	// fakeDNS answers PTR lookups for unknown addresses with an empty,
	// error-free list — the "no PTR" case.
	v := iprevCheck(t, newFakeDNS(), "192.0.2.99")
	if v.Result != IPRevNone {
		t.Fatalf("no PTR = %s (%s), want none", v.Result, v.Reason)
	}
}

func TestIPRevEmptyClientIP(t *testing.T) {
	v := iprevCheck(t, newFakeDNS(), "")
	if v.Result != IPRevNone || v.Reason != "no client IP" {
		t.Fatalf("empty IP = %+v, want none/no client IP", v)
	}
}

func TestIPRevInvalidClientIP(t *testing.T) {
	v := iprevCheck(t, newFakeDNS(), "not-an-ip")
	if v.Result != IPRevPermError {
		t.Fatalf("invalid IP = %s, want permerror", v.Result)
	}
}

func TestIPRevIPv6ForwardConfirmed(t *testing.T) {
	dns := newFakeDNS().
		withPTR("2001:db8::25", "v6.sender.test.").
		withIP("v6.sender.test.", net.ParseIP("2001:db8::25"))

	v := iprevCheck(t, dns, "2001:db8::25")
	if v.Result != IPRevPass {
		t.Fatalf("IPv6 FCrDNS = %s (%s), want pass", v.Result, v.Reason)
	}
}

func TestIPRevTempErrorOnPTRLookup(t *testing.T) {
	dns := newFakeDNS().fail("192.0.2.30", tempErr("192.0.2.30"))

	v := iprevCheck(t, dns, "192.0.2.30")
	if v.Result != IPRevTempError {
		t.Fatalf("PTR temp failure = %s (%s), want temperror", v.Result, v.Reason)
	}
}

// All forward lookups fail transiently (PTR itself resolved): temperror, not
// fail — no name got a definitive answer.
func TestIPRevTempErrorOnForwardLookups(t *testing.T) {
	dns := newFakeDNS().
		withPTR("192.0.2.40", "a.example.test.", "b.example.test.").
		fail("a.example.test.", tempErr("a.example.test.")).
		fail("b.example.test.", tempErr("b.example.test."))

	v := iprevCheck(t, dns, "192.0.2.40")
	if v.Result != IPRevTempError {
		t.Fatalf("all forward lookups temp-fail = %s (%s), want temperror", v.Result, v.Reason)
	}
}

// One name fails transiently, the other resolves but does not confirm:
// the definitive answer decides — fail.
func TestIPRevDefinitiveAnswerBeatsTemp(t *testing.T) {
	dns := newFakeDNS().
		withPTR("192.0.2.50", "flaky.example.test.", "solid.example.test.").
		fail("flaky.example.test.", tempErr("flaky.example.test.")).
		withIP("solid.example.test.", net.ParseIP("203.0.113.9"))

	v := iprevCheck(t, dns, "192.0.2.50")
	if v.Result != IPRevFail {
		t.Fatalf("mixed outcomes = %s (%s), want fail", v.Result, v.Reason)
	}
}

// host:port client addresses (as the SMTP layer sometimes reports them)
// are tolerated.
func TestIPRevHostPortClientIP(t *testing.T) {
	dns := newFakeDNS().
		withPTR("192.0.2.60", "port.sender.test.").
		withIP("port.sender.test.", net.ParseIP("192.0.2.60"))

	v := iprevCheck(t, dns, "192.0.2.60:25")
	if v.Result != IPRevPass {
		t.Fatalf("host:port input = %s (%s), want pass", v.Result, v.Reason)
	}
}
