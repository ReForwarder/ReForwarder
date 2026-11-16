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

package inbound

import (
	"strings"
	"testing"
)

// SF-12 tests: the sender-rewrite codec (the ADR-001 amendment) — the
// encode/decode round-trip, forgery refusal, and the gating.

const srsTestUUID = "a1b2c3d4-e5f6-4a1b-8c2d-9e0f1a2b3c4d"

func srsCodec() *Verp { return NewVerp(VerpConfig{Subdomain: "bounce", Secret: "srs-test-secret"}) }

// TestSRSRoundTrip pins the codec contract: the encoded address is on the
// forwarder domain, carries the srs0+ prefix, stays within the local-part
// limit, and decodes back to the original inbound UUID.
func TestSRSRoundTrip(t *testing.T) {
	v := srsCodec()
	addr, ok := v.EncodeForwarded(srsTestUUID, "fwd.example.test")
	if !ok {
		t.Fatal("encode must succeed on the fixture inputs")
	}
	if !strings.HasSuffix(addr, "@fwd.example.test") {
		t.Fatalf("addr = %q, want the forwarder domain", addr)
	}
	if !strings.HasPrefix(addr, "srs0+") {
		t.Fatalf("addr = %q, want the srs0+ prefix", addr)
	}
	local := strings.SplitN(addr, "@", 2)[0]
	if len(local) > 64 {
		t.Fatalf("local part = %d octets, want <= 64 (RFC 5321)", len(local))
	}
	decoded, ok := v.DecodeForwarded(addr)
	if !ok {
		t.Fatalf("decode must succeed on %q", addr)
	}
	if decoded != srsTestUUID {
		t.Fatalf("decoded = %q, want the original inbound UUID %q", decoded, srsTestUUID)
	}
	// Case-folding intermediaries cannot corrupt the address.
	decoded, ok = v.DecodeForwarded(strings.ToUpper(addr))
	if !ok || decoded != srsTestUUID {
		t.Fatalf("the uppercased address must still decode: %v/%q", ok, decoded)
	}
}

// TestSRSForgeryRefusal pins the fail-closed signature: a well-formed
// address with a bogus signature, a tampered payload, a tampered host, and a
// wrong secret ALL refuse to decode — no forgery path decodes.
func TestSRSForgeryRefusal(t *testing.T) {
	v := srsCodec()
	addr, _ := v.EncodeForwarded(srsTestUUID, "fwd.example.test")

	// Tampered payload: same sig, different inbound UUID.
	parts := strings.SplitN(addr, "@", 2)
	local := parts[0]
	tampered := strings.Replace(local, "a1b2c3d4", "ffffffff", 1) + "@" + parts[1]
	if _, ok := v.DecodeForwarded(tampered); ok {
		t.Fatal("a tampered payload must refuse to decode")
	}

	// Tampered host: the signature covers the host.
	swapped := local + "@other.example.test"
	if _, ok := v.DecodeForwarded(swapped); ok {
		t.Fatal("a host swap must refuse to decode")
	}

	// A well-formed shape with a bogus signature.
	bogus := "srs0+" + strings.Repeat("a", 32) + "=" + strings.Repeat("9", srsSigHexLen) + "@fwd.example.test"
	if _, ok := v.DecodeForwarded(bogus); ok {
		t.Fatal("a bogus signature must refuse to decode")
	}

	// A different secret (key rotation): the same shape, the wrong key.
	other := NewVerp(VerpConfig{Subdomain: "bounce", Secret: "another-secret"})
	if _, ok := other.DecodeForwarded(addr); ok {
		t.Fatal("a codec keyed by a different secret must refuse")
	}

	// Shape violations: wrong prefix lengths, the wrong local prefix.
	if _, ok := v.DecodeForwarded("srs0+short=f@" + "fwd.example.test"); ok {
		t.Fatal("a short payload must refuse")
	}
	if _, ok := v.DecodeForwarded("bounce+" + strings.Repeat("a", 49) + "@fwd.example.test"); ok {
		t.Fatal("a VERP-shaped address must not decode as SRS")
	}

	// The cheap pre-check never vouches for the signature.
	if !v.LooksLikeForwardedBounce(bogus) {
		t.Fatal("the shape pre-check matches the bogus signature (it is not the gate)")
	}

	// The feature off: both directions no-op.
	off := NewVerp(VerpConfig{})
	if _, ok := off.EncodeForwarded(srsTestUUID, "fwd.example.test"); ok {
		t.Fatal("a disabled codec must not encode")
	}
	if _, ok := off.DecodeForwarded(addr); ok {
		t.Fatal("a disabled codec must not decode")
	}
}

// TestSRSDistinctFromVERP pins the vocabulary separation: an srs0+ address
// and a bounce+ address never decode through each other's codec — a forged
// address cannot hop the fence between the two gates.
func TestSRSDistinctFromVERP(t *testing.T) {
	v := srsCodec()
	srsAddr, _ := v.EncodeForwarded(srsTestUUID, "fwd.example.test")
	verpAddr, _ := v.EnvelopeFrom(srsTestUUID, "noreply@fwd.example.test")

	if _, ok := v.DecodeForwarded(verpAddr); ok {
		t.Fatal("a VERP address must not decode as SRS")
	}
	if _, ok := v.Decode(verpAddr); !ok {
		t.Fatal("the VERP decode must still work (unchanged)")
	}
	if v.LooksLikeForwardedBounce(verpAddr) {
		t.Fatal("a VERP address must not carry the srs0+ shape")
	}
	if !strings.HasPrefix(srsAddr, "srs0+") || !strings.HasPrefix(verpAddr, "bounce+") {
		t.Fatal("the two vocabularies must stay distinct")
	}
}
