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

package inbound

import (
	"strings"
	"testing"
)

const (
	verpTestUUID   = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	verpTestSecret = "verp-test-secret"
)

func verpTestCodec() *Verp {
	return NewVerp(VerpConfig{Subdomain: "bounce", Secret: verpTestSecret})
}

// Encode → decode is a lossless round trip for the original message UUID.
func TestVerpEnvelopeFromRoundTrip(t *testing.T) {
	v := verpTestCodec()

	addr, ok := v.EnvelopeFrom(verpTestUUID, "noreply@mail.example.test")
	if !ok {
		t.Fatalf("EnvelopeFrom reported failure for a valid message")
	}
	if !strings.HasPrefix(addr, "bounce+") {
		t.Fatalf("address %q does not use the bounce+ local part", addr)
	}
	if !strings.HasSuffix(addr, "@bounce.mail.example.test") {
		t.Fatalf("address %q is not on the return-path subdomain", addr)
	}
	got, ok := v.Decode(addr)
	if !ok {
		t.Fatalf("Decode refused an address the codec itself produced: %s", addr)
	}
	if got != verpTestUUID {
		t.Fatalf("round trip produced %q, want %q", got, verpTestUUID)
	}
}

// The bounce local part must stay within RFC 5321's 64-octet limit so no
// remote MTA refuses it.
func TestVerpLocalPartWithinRFC5321Limit(t *testing.T) {
	v := verpTestCodec()

	addr, ok := v.EnvelopeFrom(verpTestUUID, "noreply@mail.example.test")
	if !ok {
		t.Fatalf("EnvelopeFrom failed")
	}
	local := addr[:strings.Index(addr, "@")]
	if len(local) > 64 {
		t.Fatalf("local part is %d octets, RFC 5321 allows at most 64: %q", len(local), local)
	}
	if len(addr) > 254 {
		t.Fatalf("address is %d octets, RFC 5321 allows at most 254", len(addr))
	}
}

// Tampering with the payload (the encoded message UUID) breaks the HMAC.
func TestVerpDecodeForgedPayloadRefused(t *testing.T) {
	v := verpTestCodec()

	addr, ok := v.EnvelopeFrom(verpTestUUID, "noreply@mail.example.test")
	if !ok {
		t.Fatalf("EnvelopeFrom failed")
	}
	at := strings.LastIndex(addr, "@")
	forged := "bounce+" + strings.Repeat("a", 32) + addr[strings.Index(addr, "="):at] + addr[at:]

	if got, ok := v.Decode(forged); ok {
		t.Fatalf("forged payload decoded to %q, want refusal", got)
	}
}

// Tampering with the signature breaks the HMAC.
func TestVerpDecodeForgedSignatureRefused(t *testing.T) {
	v := verpTestCodec()

	addr, ok := v.EnvelopeFrom(verpTestUUID, "noreply@mail.example.test")
	if !ok {
		t.Fatalf("EnvelopeFrom failed")
	}
	at := strings.LastIndex(addr, "@")
	forged := addr[:at-verpSigHexLen] + strings.Repeat("f", verpSigHexLen) + addr[at:]

	if got, ok := v.Decode(forged); ok {
		t.Fatalf("forged signature decoded to %q, want refusal", got)
	}
}

// A bounce address minted under a different secret (secret rotation, or an
// attacker's guess) must not decode.
func TestVerpDecodeWrongSecretRefused(t *testing.T) {
	minter := NewVerp(VerpConfig{Subdomain: "bounce", Secret: "secret-one"})
	verifier := NewVerp(VerpConfig{Subdomain: "bounce", Secret: "secret-two"})

	addr, ok := minter.EnvelopeFrom(verpTestUUID, "noreply@mail.example.test")
	if !ok {
		t.Fatalf("EnvelopeFrom failed")
	}
	if got, ok := verifier.Decode(addr); ok {
		t.Fatalf("address minted under another secret decoded to %q, want refusal", got)
	}
}

// The signature binds the full address, so a payload+signature pair cannot be
// replayed on another host.
func TestVerpSignatureBindsHost(t *testing.T) {
	v := verpTestCodec()

	addr, ok := v.EnvelopeFrom(verpTestUUID, "noreply@mail.example.test")
	if !ok {
		t.Fatalf("EnvelopeFrom failed")
	}
	local := addr[:strings.Index(addr, "@")]
	relayAddr := local + "@bounce.other.example.test"

	if got, ok := v.Decode(relayAddr); ok {
		t.Fatalf("valid local part replayed on another host decoded to %q, want refusal", got)
	}
}

// Feature off (nil codec or missing config): every operation is inert.
func TestVerpDisabledIsInert(t *testing.T) {
	cases := map[string]*Verp{
		"nil codec":        nil,
		"no subdomain":     NewVerp(VerpConfig{Secret: verpTestSecret}),
		"no secret":        NewVerp(VerpConfig{Subdomain: "bounce"}),
		"empty subdomain":  NewVerp(VerpConfig{Subdomain: "  ", Secret: verpTestSecret}),
	}
	for name, v := range cases {
		if v.Enabled() {
			t.Fatalf("%s: Enabled() = true, want false", name)
		}
		if addr, ok := v.EnvelopeFrom(verpTestUUID, "noreply@mail.example.test"); ok || addr != "" {
			t.Fatalf("%s: EnvelopeFrom = (%q, true), want (\"\", false)", name, addr)
		}
		if got, ok := v.Decode("bounce+" + strings.Repeat("0", 32) + "=" + strings.Repeat("0", 20) + "@bounce.mail.example.test"); ok || got != "" {
			t.Fatalf("%s: Decode = (%q, true), want refusal", name, got)
		}
		if v.LooksLikeBounce("bounce+" + strings.Repeat("0", 32) + "=x@bounce.mail.example.test") {
			t.Fatalf("%s: LooksLikeBounce = true, want false", name)
		}
		if d := v.BounceDomain("mail.example.test"); d != "" {
			t.Fatalf("%s: BounceDomain = %q, want empty", name, d)
		}
	}
}

// The parent-domain derivation strips exactly one subdomain label.
func TestVerpBounceParentDomain(t *testing.T) {
	v := verpTestCodec()

	parent, ok := v.BounceParentDomain("bounce.mail.example.test")
	if !ok || parent != "mail.example.test" {
		t.Fatalf("BounceParentDomain = (%q, %v), want (mail.example.test, true)", parent, ok)
	}
	if _, ok := v.BounceParentDomain("mail.example.test"); ok {
		t.Fatalf("a non-bounce host was accepted as a bounce host")
	}
	if _, ok := v.BounceParentDomain("mail.example.bounce"); ok {
		t.Fatalf("a host merely SUFFIXED with the label was accepted (the label is the first label, not the last)")
	}
	if _, ok := v.BounceParentDomain("bounce"); ok {
		t.Fatalf("a bare subdomain label without a parent was accepted")
	}
	// Uppercase hosts fold.
	if parent, ok := v.BounceParentDomain("Bounce.Mail.Example.Test"); !ok || parent != "mail.example.test" {
		t.Fatalf("case-folded host = (%q, %v)", parent, ok)
	}
}

// Display-name senders yield the bounce address of their bare domain.
func TestVerpEnvelopeFromDisplaySender(t *testing.T) {
	v := verpTestCodec()

	addr, ok := v.EnvelopeFrom(verpTestUUID, "Acme Notifications <noreply@mail.example.test>")
	if !ok {
		t.Fatalf("EnvelopeFrom failed for a display-name sender")
	}
	if !strings.HasSuffix(addr, "@bounce.mail.example.test") {
		t.Fatalf("address %q derived from the wrong domain", addr)
	}
}

// A sender without a usable domain has no bounce address.
func TestVerpEnvelopeFromInvalidSenderRefused(t *testing.T) {
	v := verpTestCodec()

	if addr, ok := v.EnvelopeFrom(verpTestUUID, "not-an-address"); ok || addr != "" {
		t.Fatalf("EnvelopeFrom = (%q, true) for a sender without a domain, want refusal", addr)
	}
	if addr, ok := v.EnvelopeFrom("not-a-uuid", "noreply@mail.example.test"); ok || addr != "" {
		t.Fatalf("EnvelopeFrom = (%q, true) for a non-UUID message id, want refusal", addr)
	}
}
