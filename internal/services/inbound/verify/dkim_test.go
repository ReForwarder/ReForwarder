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
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
)

// The DKIM fixture is signed in-test with dkim.Sign (go-msgauth, the same
// package the sender uses) against an ed25519 key, then verified through the
// engine with the public key served from the fake resolver — the fake-DNS
// pattern the inbound tests already use.

const (
	// dkimFixtureDomain is the signing domain of every DKIM fixture.
	dkimFixtureDomain = "example.test"
)

const dkimFixtureMessage = "From: Alice <alice@example.test>\r\n" +
	"To: bob@recipient.test\r\n" +
	"Subject: SF-23 fixture\r\n" +
	"\r\n" +
	"Hello, verification.\r\n"

// signFixture produces a message signed for domain/selector with key.
func signFixture(t *testing.T, selector string, key ed25519.PrivateKey) []byte {
	t.Helper()
	opts := &dkim.SignOptions{
		Domain:                 dkimFixtureDomain,
		Selector:               selector,
		Signer:                 key,
		Hash:                   0, // default (SHA-256)
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		HeaderKeys:             []string{"From", "To", "Subject"},
	}
	var out bytes.Buffer
	if err := dkim.Sign(&out, bytes.NewReader([]byte(dkimFixtureMessage)), opts); err != nil {
		t.Fatalf("dkim.Sign fixture: %v", err)
	}
	return out.Bytes()
}

// serveDKIMKey publishes the ed25519 public key at
// <selector>._domainkey.<domain> on the fake resolver.
func serveDKIMKey(dns *fakeDNS, selector string, key ed25519.PrivateKey) {
	pub, _ := key.Public().(ed25519.PublicKey)
	dns.withTXT(selector+"._domainkey."+dkimFixtureDomain,
		"v=DKIM1; k=ed25519; p="+base64.StdEncoding.EncodeToString(pub))
}

// A properly signed message verifies pass, with the d= and s= identity
// recovered for the Authentication-Results header (SF-23).
func TestDKIMVerifyPassSignedFixture(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)) // deterministic test key
	signed := signFixture(t, "s1", key)

	dns := newFakeDNS()
	serveDKIMKey(dns, "s1", key)

	e := &DKIMEvaluator{LookupTXT: dnsLookupAdapter(dns)}
	got := e.Verify(context.Background(), signed)
	if got.Result != DKIMPass {
		t.Fatalf("dkim = %s (%s), want pass", got.Result, got.Reason)
	}
	if got.Domain != "example.test" {
		t.Fatalf("dkim d= = %q, want example.test", got.Domain)
	}
	if got.Selector != "s1" {
		t.Fatalf("dkim s= = %q, want s1", got.Selector)
	}
}

// A tampered body fails verification as fail (signature mismatch class).
func TestDKIMVerifyFailTamperedBody(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	signed := signFixture(t, "s1", key)
	tampered := bytes.Replace(signed, []byte("Hello, verification."), []byte("Hello, tampering!.."), 1)
	if bytes.Equal(tampered, signed) {
		t.Fatal("test bug: tampering changed nothing")
	}

	dns := newFakeDNS()
	serveDKIMKey(dns, "s1", key)

	e := &DKIMEvaluator{LookupTXT: dnsLookupAdapter(dns)}
	got := e.Verify(context.Background(), tampered)
	if got.Result != DKIMFail {
		t.Fatalf("tampered body → %s (%s), want fail", got.Result, got.Reason)
	}
}

// A missing selector key record is a permanent failure.
func TestDKIMVerifyPermErrorWhenKeyMissing(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	signed := signFixture(t, "ghost", key)
	// No TXT served for ghost._domainkey.example.test → NXDOMAIN.

	e := &DKIMEvaluator{LookupTXT: dnsLookupAdapter(newFakeDNS())}
	got := e.Verify(context.Background(), signed)
	if got.Result != DKIMPermError {
		t.Fatalf("missing key → %s (%s), want permerror", got.Result, got.Reason)
	}
}

// A transient resolver failure maps to temperror.
func TestDKIMVerifyTempErrorOnDNSFailure(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	signed := signFixture(t, "s1", key)

	dns := newFakeDNS()
	dns.fail("s1._domainkey.example.test", tempErr("s1._domainkey.example.test"))

	e := &DKIMEvaluator{LookupTXT: dnsLookupAdapter(dns)}
	got := e.Verify(context.Background(), signed)
	if got.Result != DKIMTempError {
		t.Fatalf("dns failure → %s (%s), want temperror", got.Result, got.Reason)
	}
}

// A message without a DKIM-Signature is none, not an error.
func TestDKIMNoSignatureYieldsNone(t *testing.T) {
	e := &DKIMEvaluator{LookupTXT: dnsLookupAdapter(newFakeDNS())}
	got := e.Verify(context.Background(), []byte(dkimFixtureMessage))
	if got.Result != DKIMNone {
		t.Fatalf("unsigned → %s, want none", got.Result)
	}
	if got.Domain != "" || got.Selector != "" {
		t.Fatalf("unsigned verdict carries an identity: %+v", got)
	}
}

// A tampered From header (the identity DMARC relies on) must fail even when
// the body is untouched — relaxed canonicalization still binds the header.
func TestDKIMVerifyFailTamperedHeader(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	signed := signFixture(t, "s1", key)
	tampered := bytes.Replace(signed, []byte("From: Alice <alice@example.test>"), []byte("From: Eve <eve@example.test>"), 1)
	if bytes.Equal(tampered, signed) {
		t.Fatal("test bug: header tampering changed nothing")
	}

	dns := newFakeDNS()
	serveDKIMKey(dns, "s1", key)

	e := &DKIMEvaluator{LookupTXT: dnsLookupAdapter(dns)}
	got := e.Verify(context.Background(), tampered)
	if got.Result != DKIMFail {
		t.Fatalf("tampered From → %s (%s), want fail", got.Result, got.Reason)
	}
}

// The selector is recovered even when the signature header is folded across
// lines (long base64 b= tags fold in real mail).
func TestDKIMSelectorFromFoldedSignature(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	signed := signFixture(t, "folded", key)
	// Force a fold by checking the fixture actually contains one; if not,
	// inject a harmless fold into the signature value (continuation lines
	// are part of the field value and go-msgauth unfolds them itself).
	if !bytes.Contains(signed, []byte("\r\n\t")) && !bytes.Contains(signed, []byte("\r\n ")) {
		signed = bytes.Replace(signed, []byte("; bh="), []byte(";\r\n\tbh="), 1)
	}

	dns := newFakeDNS()
	serveDKIMKey(dns, "folded", key)

	e := &DKIMEvaluator{LookupTXT: dnsLookupAdapter(dns)}
	got := e.Verify(context.Background(), signed)
	if got.Result != DKIMPass || got.Selector != "folded" {
		t.Fatalf("folded signature → %s s=%q, want pass s=folded (%s)", got.Result, got.Selector, got.Reason)
	}
}

// dnsLookupAdapter adapts the fake resolver to go-msgauth's no-context hook.
func dnsLookupAdapter(dns *fakeDNS) func(string) ([]string, error) {
	return func(domain string) ([]string, error) {
		return dns.LookupTXT(context.Background(), domain)
	}
}

// The signatureIdentity scanner itself: extracts d= and s= from a header
// block, tolerating folding and case differences.
func TestSignatureIdentityScanner(t *testing.T) {
	raw := []byte("DKIM-Signature: v=1; a=ed25519-sha256; d=Example.Test;\r\n\ts=sel-two; h=from:to;\r\n\tb=abc\r\n" +
		"From: a@example.test\r\n\r\nbody")
	dom, sel := signatureIdentity(raw, "example.test")
	if dom != "Example.Test" || sel != "sel-two" {
		t.Fatalf("signatureIdentity = (%q, %q), want (Example.Test, sel-two)", dom, sel)
	}
	if d, s := signatureIdentity(raw, "other.test"); d != "other.test" || s != "" {
		t.Fatalf("signatureIdentity for unknown domain = (%q, %q)", d, s)
	}
	if !strings.EqualFold(headerTagValue("DKIM-Signature: v=1; D=x; s=y", "d"), "x") {
		t.Fatal("headerTagValue d= extraction broken")
	}
}
