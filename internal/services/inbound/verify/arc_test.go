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
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// ARC chain validation tests (RFC 8617 §5.2). A test sealer builds valid
// chains from freshly generated keys (round-trip), the negative cases then
// corrupt one link at a time. The canonicalization tests are pinned to the
// RFC 6376 §3.4 behaviors and the published empty-body SHA-256 vectors, so
// a shared sealer/verifier bug cannot pass unnoticed.

// arcTestMessage is the unsigned base message every chain is built on.
const arcTestMessage = "From: Alice <alice@example.test>\r\n" +
	"To: bob@recipient.test\r\n" +
	"Subject: arc fixture\r\n" +
	"Date: Fri, 07 Nov 2025 10:00:00 +0000\r\n" +
	"Message-ID: <arc-1@example.test>\r\n" +
	"\r\n" +
	"Hello ARC chain.\r\n"

// ---------- test sealer ----------

type arcTestSigner struct {
	algo string // "ed25519-sha256" | "rsa-sha256"
	priv crypto.Signer
	p    string // base64 public key for the DNS record
	dom  string
	sel  string
}

func newArcTestSigner(t *testing.T, algo string) *arcTestSigner {
	t.Helper()
	s := &arcTestSigner{algo: algo, dom: "forwarder.test", sel: "arc-test"}
	switch algo {
	case "ed25519-sha256":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("ed25519 keygen: %v", err)
		}
		s.priv = priv
		s.p = base64.StdEncoding.EncodeToString(pub)
	case "rsa-sha256":
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("rsa keygen: %v", err)
		}
		s.priv = key
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatalf("marshal pubkey: %v", err)
		}
		s.p = base64.StdEncoding.EncodeToString(der)
	default:
		t.Fatalf("unknown test algorithm %q", algo)
	}
	return s
}

func (s *arcTestSigner) keyType() string {
	if s.algo == "ed25519-sha256" {
		return "ed25519"
	}
	return "rsa"
}

func (s *arcTestSigner) keyRecord() string {
	return fmt.Sprintf("v=DKIM1; k=%s; p=%s", s.keyType(), s.p)
}

func (s *arcTestSigner) keyName() string {
	return s.sel + "._domainkey." + s.dom
}

func (s *arcTestSigner) sign(t *testing.T, digest []byte) []byte {
	t.Helper()
	switch key := s.priv.(type) {
	case ed25519.PrivateKey:
		return ed25519.Sign(key, digest)
	case *rsa.PrivateKey:
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest)
		if err != nil {
			t.Fatalf("rsa sign: %v", err)
		}
		return sig
	default:
		t.Fatalf("unexpected signer type %T", s.priv)
		return nil
	}
}

// sealHop appends one ARC set to raw (instance = highest existing + 1, or
// forceInstance when non-zero) and returns the updated message. sealExtra
// injects additional tags into the ARC-Seal (e.g. the prohibited h=).
func sealHop(t *testing.T, raw []byte, sg *arcTestSigner, cv string, forceInstance int, sealExtra string) []byte {
	t.Helper()
	msg := parseRawMessage(raw)
	instance := forceInstance
	if instance == 0 {
		instance = arcHighestInstance(msg.headers) + 1
	}

	aar := fmt.Sprintf("ARC-Authentication-Results: i=%d; mx.forwarder.test; spf=pass dkim=pass", instance)
	withAAR := arcPrependHeader(t, raw, aar)
	ams := arcBuildAMS(t, withAAR, sg, instance, "")
	sealTags := fmt.Sprintf("a=%s; d=%s; s=%s; t=%d; i=%d; cv=%s; %sb=", sg.algo, sg.dom, sg.sel, arcTestTime, instance, cv, sealExtra)
	as := arcBuildSeal(t, msg, aar, ams, sg, sealTags)

	out := raw
	for _, field := range []string{aar, ams, as} {
		out = arcPrependHeader(t, out, field)
	}
	return out
}

// arcTestTime is the t= stamp used by every fixture seal.
const arcTestTime = 1730975000

// arcHighestInstance scans raw headers for the highest ARC-Seal instance.
func arcHighestInstance(headers []rawHeader) int {
	highest := 0
	for _, h := range headers {
		if h.name != "arc-seal" {
			continue
		}
		_, instance, err := arcSigParsed(h.raw)
		if err == nil && instance > highest {
			highest = instance
		}
	}
	return highest
}

// arcPrependHeader inserts one header field at the top of the header block.
func arcPrependHeader(t *testing.T, raw []byte, field string) []byte {
	t.Helper()
	s := string(raw)
	i := strings.Index(s, "\r\n\r\n")
	if i < 0 {
		t.Fatal("fixture has no header/body separator")
	}
	return []byte(field + "\r\n" + s[:i] + "\r\n\r\n" + s[i+4:])
}

// arcBuildAMS produces a signed ARC-Message-Signature (relaxed/relaxed,
// fixed h= list) for the message, instance and signer.
func arcBuildAMS(t *testing.T, raw []byte, sg *arcTestSigner, instance int, extra string) string {
	t.Helper()
	msg := parseRawMessage(raw)
	bh := sha256.Sum256(canonBodyRelaxed(msg.body))
	field := fmt.Sprintf("ARC-Message-Signature: a=%s; d=%s; s=%s; t=%d; i=%d; c=relaxed/relaxed; h=from:to:subject:date:message-id; bh=%s; %sb=",
		sg.algo, sg.dom, sg.sel, arcTestTime, instance, base64.StdEncoding.EncodeToString(bh[:]), extra)

	p := newPicker(msg.headers)
	var input strings.Builder
	for _, n := range []string{"from", "to", "subject", "date", "message-id"} {
		if rawField := p.pick(n); rawField != "" {
			input.WriteString(canonHeaderRelaxed(rawField))
		}
	}
	input.WriteString(strings.TrimRight(canonHeaderRelaxed(field), "\r\n"))
	digest := sha256.Sum256([]byte(input.String()))
	return field + base64.StdEncoding.EncodeToString(sg.sign(t, digest[:]))
}

// arcBuildSeal produces a signed ARC-Seal over the prior sets (parsed from
// the message), the new AAR/AMS and the seal itself with b= empty.
func arcBuildSeal(t *testing.T, prior *rawMessage, aar, ams string, sg *arcTestSigner, sealTags string) string {
	t.Helper()
	sets, err := collectARCSets(prior.headers)
	if err != nil {
		t.Fatalf("collect prior ARC sets: %v", err)
	}
	var input strings.Builder
	for _, s := range sets {
		input.WriteString(canonHeaderRelaxed(s.aar))
		input.WriteString(canonHeaderRelaxed(s.ams))
		input.WriteString(canonHeaderRelaxed(s.seal))
	}
	input.WriteString(canonHeaderRelaxed(aar))
	input.WriteString(canonHeaderRelaxed(ams))
	input.WriteString(strings.TrimRight(canonHeaderRelaxed("ARC-Seal: "+sealTags), "\r\n"))
	digest := sha256.Sum256([]byte(input.String()))
	return "ARC-Seal: " + sealTags + base64.StdEncoding.EncodeToString(sg.sign(t, digest[:]))
}

// arcVerify is the one-line verifier over fake DNS.
func arcVerify(t *testing.T, dns *fakeDNS, raw []byte) ARCVerdict {
	t.Helper()
	return (&ARCEvaluator{Resolver: dns}).Verify(context.Background(), raw)
}

// ---------- positive paths ----------

func TestARCValidSingleEd25519Set(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")

	v := arcVerify(t, dns, raw)
	if v.Result != ARCPass {
		t.Fatalf("single-set chain = %s (%s), want pass", v.Result, v.Reason)
	}
	if v.Domain != sg.dom {
		t.Fatalf("verdict domain = %q, want %q", v.Domain, sg.dom)
	}
}

func TestARCValidTwoSetChain(t *testing.T) {
	first := newArcTestSigner(t, "ed25519-sha256")
	second := newArcTestSigner(t, "ed25519-sha256")
	second.dom = "relay.test"
	second.sel = "arc-relay"
	dns := newFakeDNS().
		withTXT(first.keyName(), first.keyRecord()).
		withTXT(second.keyName(), second.keyRecord())
	raw := sealHop(t, []byte(arcTestMessage), first, "none", 0, "")
	raw = sealHop(t, raw, second, "pass", 0, "")

	v := arcVerify(t, dns, raw)
	if v.Result != ARCPass {
		t.Fatalf("two-set chain = %s (%s), want pass", v.Result, v.Reason)
	}
	if v.Domain != second.dom {
		t.Fatalf("verdict domain = %q, want the most recent sealer %q", v.Domain, second.dom)
	}
}

func TestARCValidSingleRSASet(t *testing.T) {
	sg := newArcTestSigner(t, "rsa-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")

	if v := arcVerify(t, dns, raw); v.Result != ARCPass {
		t.Fatalf("rsa-sha256 chain = %s (%s), want pass", v.Result, v.Reason)
	}
}

// ---------- corruption matrix ----------

func TestARCTamperedSealSignature(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")

	// Flip the first character of the ARC-Seal b= value (only the seal's).
	s := string(raw)
	i := strings.Index(s, "ARC-Seal: ")
	if i < 0 {
		t.Fatal("no ARC-Seal in fixture")
	}
	j := strings.Index(s[i:], "b=")
	if j < 0 || i+j+2 >= len(s) {
		t.Fatal("no b= in seal")
	}
	pos := i + j + 2
	repl := "A"
	if s[pos] == 'A' {
		repl = "B"
	}
	s = s[:pos] + repl + s[pos+1:]

	if v := arcVerify(t, dns, []byte(s)); v.Result != ARCFail {
		t.Fatalf("tampered seal = %s, want fail", v.Result)
	}
}

func TestARCTamperedBodyFailsAMS(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")
	raw = append(raw, 'X') // body no longer matches bh=

	v := arcVerify(t, dns, raw)
	if v.Result != ARCFail || !strings.Contains(v.Reason, "body hash") {
		t.Fatalf("tampered body = %s (%s), want fail with body hash reason", v.Result, v.Reason)
	}
}

func TestARCTamperedSignedHeaderFailsAMS(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")
	raw = []byte(strings.Replace(string(raw), "Subject: arc fixture", "Subject: SPAM", 1))

	v := arcVerify(t, dns, raw)
	if v.Result != ARCFail {
		t.Fatalf("tampered Subject = %s (%s), want fail", v.Result, v.Reason)
	}
}

func TestARCInstanceGap(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")
	raw = sealHop(t, raw, sg, "pass", 3, "") // i=1 then i=3

	v := arcVerify(t, dns, raw)
	if v.Result != ARCFail || !strings.Contains(v.Reason, "gap") {
		t.Fatalf("gapped chain = %s (%s), want fail mentioning the gap", v.Result, v.Reason)
	}
}

func TestARCDuplicateSealHeader(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")
	raw = arcPrependHeader(t, raw, "ARC-Seal: i=1; a=ed25519-sha256; b=AAAA; cv=none; d=evil.test; s=s")

	if v := arcVerify(t, dns, raw); v.Result != ARCFail {
		t.Fatalf("duplicated seal = %s, want fail", v.Result)
	}
}

func TestARCSealWithHTagIsFail(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "h=from;")

	v := arcVerify(t, dns, raw)
	if v.Result != ARCFail || !strings.Contains(v.Reason, "h=") {
		t.Fatalf("seal with h= = %s (%s), want fail", v.Result, v.Reason)
	}
}

func TestARCCVMatrix(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())

	// i=1 with cv=pass is invalid (only i=1 → none).
	raw := sealHop(t, []byte(arcTestMessage), sg, "pass", 0, "")
	if v := arcVerify(t, dns, raw); v.Result != ARCFail {
		t.Fatalf("i=1 cv=pass = %s, want fail", v.Result)
	}

	// i=2 with cv=none is invalid.
	first := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")
	second := sealHop(t, first, sg, "none", 0, "")
	if v := arcVerify(t, dns, second); v.Result != ARCFail {
		t.Fatalf("i=2 cv=none = %s, want fail", v.Result)
	}

	// A failed chain cannot recover: i=2 with cv=fail.
	third := sealHop(t, first, sg, "fail", 0, "")
	if v := arcVerify(t, dns, third); v.Result != ARCFail {
		t.Fatalf("i=2 cv=fail = %s, want fail", v.Result)
	}

	// Uppercase cv= values are the same ABNF strings (case-insensitive).
	okChain := sealHop(t, []byte(arcTestMessage), sg, "NONE", 0, "")
	if v := arcVerify(t, dns, okChain); v.Result != ARCPass {
		t.Fatalf("i=1 cv=NONE = %s (%s), want pass", v.Result, v.Reason)
	}
}

func TestARCMoreThanFiftySets(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := []byte(arcTestMessage)
	for i := 0; i < 51; i++ {
		raw = sealHop(t, raw, sg, "", 0, "")
	}
	v := arcVerify(t, dns, raw)
	if v.Result != ARCFail || !strings.Contains(v.Reason, "51") {
		t.Fatalf("51 sets = %s (%s), want fail", v.Result, v.Reason)
	}
}

func TestARCMissingKeyIsPermanentFail(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS() // no key record at all
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")

	v := arcVerify(t, dns, raw)
	if v.Result != ARCFail || !strings.Contains(v.Reason, "no key") {
		t.Fatalf("missing key = %s (%s), want fail (RFC 8617 §5.2.1: permanent)", v.Result, v.Reason)
	}
}

func TestARCKeyRecordHashRestriction(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), "v=DKIM1; k=ed25519; h=sha1; p="+sg.p)
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")

	if v := arcVerify(t, dns, raw); v.Result != ARCFail {
		t.Fatalf("key with h=sha1 = %s, want fail", v.Result)
	}
}

func TestARCOldestSealAlsoVerified(t *testing.T) {
	// The newest seal is genuine (its key resolves), but the OLDEST seal was
	// made by a key whose record does not exist. RFC 8617 §5.2 step 6 checks
	// every seal newest→oldest, so the chain must fail even though the i=2
	// verification alone would succeed.
	forger := newArcTestSigner(t, "ed25519-sha256")
	second := newArcTestSigner(t, "ed25519-sha256")
	second.dom = "relay.test"
	second.sel = "arc-relay"
	dns := newFakeDNS().withTXT(second.keyName(), second.keyRecord()) // forger's key missing
	raw := sealHop(t, []byte(arcTestMessage), forger, "none", 0, "")
	raw = sealHop(t, raw, second, "pass", 0, "")

	v := arcVerify(t, dns, raw)
	if v.Result != ARCFail || !strings.Contains(v.Reason, "i=1") {
		t.Fatalf("chain with unverifiable oldest seal = %s (%s), want fail at i=1", v.Result, v.Reason)
	}
}

func TestARCAARWithoutInstance(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := arcPrependHeader(t, []byte(arcTestMessage), "ARC-Authentication-Results: mx.forwarder.test; spf=pass")

	if v := arcVerify(t, dns, raw); v.Result != ARCFail {
		t.Fatalf("AAR without instance = %s, want fail", v.Result)
	}
}

func TestARCIncompleteSet(t *testing.T) {
	sg := newArcTestSigner(t, "ed25519-sha256")
	dns := newFakeDNS().withTXT(sg.keyName(), sg.keyRecord())
	raw := sealHop(t, []byte(arcTestMessage), sg, "none", 0, "")
	raw = arcPrependHeader(t, raw, "ARC-Authentication-Results: i=2; mx.forwarder.test; spf=pass")

	if v := arcVerify(t, dns, raw); v.Result != ARCFail || !strings.Contains(v.Reason, "incomplete") {
		t.Fatalf("incomplete set = %s (%s), want fail", v.Result, v.Reason)
	}
}

// ---------- none paths ----------

func TestARCNoHeadersIsNone(t *testing.T) {
	v := arcVerify(t, newFakeDNS(), []byte(arcTestMessage))
	if v.Result != ARCNone {
		t.Fatalf("unsigned message = %s, want none", v.Result)
	}
	if v.Domain != "" {
		t.Fatalf("none verdict carries domain %q", v.Domain)
	}
}

func TestARCEmptyRawIsNone(t *testing.T) {
	v := arcVerify(t, newFakeDNS(), nil)
	if v.Result != ARCNone {
		t.Fatalf("empty raw = %s, want none", v.Result)
	}
}

// ---------- RFC 6376 canonicalization pins ----------

// The empty-body vectors are published in RFC 6376 §3.4.3/§3.4.4.
func TestCanonBodyVectors(t *testing.T) {
	simpleEmpty := sha256.Sum256(canonBodySimple(nil))
	if got := base64.StdEncoding.EncodeToString(simpleEmpty[:]); got != "frcCV1k9oG9oKj3dpUqdJg1PxRT2RSN/XKdLCPjaYaY=" {
		t.Fatalf("simple empty body sha256 = %s, want the RFC 6376 §3.4.3 vector", got)
	}
	relaxedEmpty := sha256.Sum256(canonBodyRelaxed(nil))
	if got := base64.StdEncoding.EncodeToString(relaxedEmpty[:]); got != "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=" {
		t.Fatalf("relaxed empty body sha256 = %s, want the RFC 6376 §3.4.4 vector", got)
	}
}

func TestCanonBodySimple(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "\r\n"},
		{"\r\n", "\r\n"},
		{"\r\n\r\n\r\n", "\r\n"},
		{"Hey\r\n\r\n", "Hey\r\n"},
		{"Hey\r\nHow r u?\r\n\r\n\r\n", "Hey\r\nHow r u?\r\n"},
		{"Hey\r\n\r\nHow r u?", "Hey\r\n\r\nHow r u?\r\n"},
		{"What about\nLF endings?\n\n", "What about\r\nLF endings?\r\n"},
	}
	for _, tc := range cases {
		if got := string(canonBodySimple([]byte(tc.in))); got != tc.want {
			t.Errorf("canonBodySimple(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonBodyRelaxed(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"\r\n", ""},
		{"\r\n\r\n\r\n", ""},
		{"Hey\r\n\r\n", "Hey\r\n"},
		{"Hey\r\nHow r u?\r\n\r\n\r\n", "Hey\r\nHow r u?\r\n"},
		{"Hey\r\n\r\nHow r u?", "Hey\r\n\r\nHow r u?\r\n"},
		{"Hey \t you!", "Hey you!\r\n"},
		{"Hey \t \r\nyou!", "Hey\r\nyou!\r\n"},
		{"Hey\r\n \t you!\r\n", "Hey\r\n you!\r\n"},
		{"Hey\r\n \t \r\n \r\n", "Hey\r\n"},
	}
	for _, tc := range cases {
		if got := string(canonBodyRelaxed([]byte(tc.in))); got != tc.want {
			t.Errorf("canonBodyRelaxed(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonHeaderRelaxed(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SubjeCT: Your Name\r\n", "subject:Your Name\r\n"},
		{"Subject \t:\t Your Name\t \r\n", "subject:Your Name\r\n"},
		{"Subject \t:\t Kimi \t \r\n No \t\r\n Na Wa\r\n", "subject:Kimi No Na Wa\r\n"},
		{"Subject \t:\t Ki \tmi \t \r\n No \t\r\n Na Wa\r\n", "subject:Ki mi No Na Wa\r\n"},
	}
	for _, tc := range cases {
		if got := canonHeaderRelaxed(tc.in); got != tc.want {
			t.Errorf("canonHeaderRelaxed(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonHeaderSimple(t *testing.T) {
	if got := canonHeaderSimple("A:  X \r\n\tZ"); got != "A:  X \r\n\tZ\r\n" {
		t.Fatalf("canonHeaderSimple changed the field: %q", got)
	}
}

func TestParseSigTags(t *testing.T) {
	tags, err := parseSigTags(" a = rsa-sha256 ; d=example.test;  s=sel1; b=AbC\t  ;")
	if err != nil {
		t.Fatalf("valid tag list rejected: %v", err)
	}
	if tags.get("a") != "rsa-sha256" || tags.get("D") != "example.test" || tags.get("s") != "sel1" || tags.get("b") != "AbC" {
		t.Fatalf("parsed tags wrong: %#v", tags)
	}
	if _, err := parseSigTags("a=1; a=2"); err == nil {
		t.Fatal("duplicate tag accepted")
	}
	if _, err := parseSigTags("noequals"); err == nil {
		t.Fatal("tag without '=' accepted")
	}
	if _, err := parseSigTags("a=1;"); err != nil {
		t.Fatalf("trailing semicolon rejected: %v", err)
	}
}

func TestRemoveSigValue(t *testing.T) {
	got := removeSigValue("ARC-Seal: a=ed25519-sha256; b=abc def ; cv=none")
	if !strings.Contains(got, "b=;") || strings.Contains(got, "abc") {
		t.Fatalf("b= value not emptied: %q", got)
	}
	if got := removeSigValue("ARC-Message-Signature: bh=xyz; b=123;"); !strings.Contains(got, "bh=xyz") {
		t.Fatalf("bh= damaged by b= removal: %q", got)
	}
}

func TestAARInstanceParsing(t *testing.T) {
	cases := []struct {
		field string
		want  int
		ok    bool
	}{
		{"ARC-Authentication-Results: i=2; mx.example; spf=pass", 2, true},
		{"ARC-Authentication-Results:i=12; mx.example", 12, true},
		{"ARC-Authentication-Results: i = 3 ; mx.example", 3, true},
		{"ARC-Authentication-Results: mx.example; spf=pass", 0, false},
		{"ARC-Authentication-Results: i=123; mx.example", 0, false},
		{"ARC-Authentication-Results: i=0; mx.example", 0, false},
	}
	for _, tc := range cases {
		got, err := aarInstance(tc.field)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("aarInstance(%q) = %d, %v; want %d", tc.field, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("aarInstance(%q) accepted an invalid field", tc.field)
		}
	}
}

func TestRawMessageParsingFolding(t *testing.T) {
	raw := []byte("Subject: folded\r\n\tcontinuation\r\nFrom: a@b.test\r\n\r\nbody\r\n")
	msg := parseRawMessage(raw)
	if len(msg.headers) != 2 {
		t.Fatalf("parsed %d headers, want 2", len(msg.headers))
	}
	if msg.headers[0].name != "subject" || !strings.Contains(msg.headers[0].raw, "\r\n\tcontinuation") {
		t.Fatalf("folded header not preserved: %#v", msg.headers[0])
	}
	if string(msg.body) != "body\r\n" {
		t.Fatalf("body = %q", msg.body)
	}
}
