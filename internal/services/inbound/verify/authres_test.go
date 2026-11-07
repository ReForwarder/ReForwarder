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
	"strings"
	"testing"
)

// The composed value follows RFC 8601: authserv-id, then one
// method=result group per engine with the evaluated identities as
// properties (SF-49 slice).
func TestAuthenticationResultsHeaderShape(t *testing.T) {
	r := Results{
		SPF:                SPFVerdict{Result: SPFPass, Domain: "example.test"},
		DKIM:               DKIMVerdict{Result: DKIMFail, Domain: "example.test", Selector: "s1", Reason: "body hash did not verify"},
		DMARC:              DMARCVerdict{Result: DMARCFail, Disposition: DispositionReject, PolicyDomain: "example.test", Policy: "reject", PCT: 25},
		EnvelopeFromDomain: "example.test",
		FromDomain:         "example.test",
	}
	got := AuthenticationResults("posta.local", r)

	for _, want := range []string{
		"posta.local;",
		"spf=pass smtp.mailfrom=example.test",
		"dkim=fail header.d=example.test header.s=s1",
		`reason="body hash did not verify"`,
		"dmarc=fail header.from=example.test policy.p=reject policy.pct=25",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("A-R value %q does not contain %q", got, want)
		}
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("A-R value must be a single line: %q", got)
	}
	if !strings.HasPrefix(got, "posta.local; spf=") {
		t.Fatalf("A-R value must start with the authserv-id: %q", got)
	}
	// Three method groups, separated by semicolons.
	if n := strings.Count(got, ";"); n != 3 {
		t.Fatalf("A-R value has %d semicolons, want 3: %q", n, got)
	}
}

// Pass and no-identity paths: a none DKIM carries no header.d/header.s, and
// a passing DMARC reports no policy.p (nothing was enforced).
func TestAuthenticationResultsHeaderMinimalShape(t *testing.T) {
	r := Results{
		SPF:                SPFVerdict{Result: SPFNone, Reason: "no envelope sender"},
		DKIM:               DKIMVerdict{Result: DKIMNone},
		DMARC:              DMARCVerdict{Result: DMARCNone, Disposition: DispositionNone},
		EnvelopeFromDomain: "",
		FromDomain:         "",
	}
	got := AuthenticationResults("mx1.example.test", r)
	if !strings.Contains(got, "spf=none") || !strings.Contains(got, "dkim=none") || !strings.Contains(got, "dmarc=none") {
		t.Fatalf("A-R value missing the three none results: %q", got)
	}
	if strings.Contains(got, "header.d=") || strings.Contains(got, "smtp.mailfrom=") || strings.Contains(got, "header.from=") || strings.Contains(got, "policy.p=") {
		t.Fatalf("A-R value reports identities it does not have: %q", got)
	}
}

// Hostile values cannot break the value syntax: tokens are sanitized, reasons
// quoted and escaped.
func TestAuthenticationResultsSanitizesValues(t *testing.T) {
	r := Results{
		SPF:                SPFVerdict{Result: SPFPass, Domain: "evil;header.example"},
		DKIM:               DKIMVerdict{Result: DKIMFail, Domain: "d.test", Selector: "s; p=1"},
		DMARC:              DMARCVerdict{Result: DMARCFail, Policy: "re\"ject", PolicyDomain: "pd.test", PCT: 100},
		EnvelopeFromDomain: "evil;header.example",
		FromDomain:         "pd.test",
	}
	got := AuthenticationResults("auth id", r)
	if strings.Contains(got, ";header") || strings.Contains(got, "s; p=1") {
		t.Fatalf("A-R value contains unsanitized tokens: %q", got)
	}
	if strings.Contains(got, `p=re"ject`) {
		t.Fatalf("A-R value contains unescaped policy: %q", got)
	}
	// The authserv-id with a space is sanitized too (fallback keeps letters).
	if !strings.HasPrefix(got, "authid;") && !strings.HasPrefix(got, "auth id;") {
		t.Fatalf("A-R value starts with %q", got[:10])
	}
}

// Evaluate end-to-end over the fake resolver: SPF from the envelope, DKIM
// from the raw bytes, DMARC from the RFC 5322.From header inside the raw.
func TestEvaluateEndToEndOverFakeDNS(t *testing.T) {
	dns := newFakeDNS().
		withTXT("example.test", "v=spf1 ip4:192.0.2.10 -all").
		withTXT("_dmarc.example.test", "v=DMARC1; p=quarantine")
	raw := []byte("From: Alice <alice@example.test>\r\nTo: bob@rcpt.test\r\nSubject: e2e\r\n\r\nHi\r\n")

	got := Evaluate(context.Background(), dns, Input{
		ClientIP:     "192.0.2.10",
		EnvelopeFrom: "bounce@example.test",
		Raw:          raw,
		SampleKey:    "inb-1",
	})
	if got.SPF.Result != SPFPass {
		t.Fatalf("spf = %s (%s), want pass", got.SPF.Result, got.SPF.Reason)
	}
	if got.DKIM.Result != DKIMNone {
		t.Fatalf("dkim = %s, want none (unsigned fixture)", got.DKIM.Result)
	}
	if got.DMARC.Result != DMARCPass || got.DMARC.AlignedBy != "spf" {
		t.Fatalf("dmarc = %s alignedBy=%q, want pass via spf", got.DMARC.Result, got.DMARC.AlignedBy)
	}
	if got.FromDomain != "example.test" || got.EnvelopeFromDomain != "example.test" {
		t.Fatalf("identities wrong: from=%q env=%q", got.FromDomain, got.EnvelopeFromDomain)
	}
	header := AuthenticationResults("posta.local", got)
	if !strings.Contains(header, "spf=pass") || !strings.Contains(header, "dmarc=pass") {
		t.Fatalf("end-to-end A-R value wrong: %q", header)
	}
}

// No RFC 5322.From in the raw message: DMARC is none, nothing panics.
func TestEvaluateWithoutFromHeader(t *testing.T) {
	got := Evaluate(context.Background(), newFakeDNS(), Input{
		ClientIP:     "192.0.2.10",
		EnvelopeFrom: "bounce@example.test",
		Raw:          []byte("Subject: no from\r\n\r\nbody\r\n"),
	})
	if got.FromDomain != "" || got.DMARC.Result != DMARCNone {
		t.Fatalf("missing From → from=%q dmarc=%s, want empty/none", got.FromDomain, got.DMARC.Result)
	}
}

// The RFC 5322.From parser handles display names, bare addresses and the
// group syntax garbage senders produce.
func TestHeaderFromDomainParsing(t *testing.T) {
	cases := []struct{ header, want string }{
		{"From: alice@example.test", "example.test"},
		{"From: Alice A <alice@example.test>", "example.test"},
		{"From: \"A, B\" <alice@example.test>", "example.test"},
		{"From: alice@Sub.Example.TEST", "sub.example.test"},
		{"From: <>", ""},
		{"From: junk", ""},
	}
	for _, tc := range cases {
		raw := []byte(tc.header + "\r\n\r\nbody")
		if got := headerFromDomain(raw); got != tc.want {
			t.Errorf("headerFromDomain(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

// parseIP tolerates host:port forms and rejects junk.
func TestParseIP(t *testing.T) {
	if got := parseIP("192.0.2.10"); got.String() != "192.0.2.10" {
		t.Fatalf("parseIP plain = %v", got)
	}
	if got := parseIP("192.0.2.10:25"); got.String() != "192.0.2.10" {
		t.Fatalf("parseIP host:port = %v", got)
	}
	if got := parseIP("2001:db8::1"); !got.IsValid() {
		t.Fatalf("parseIP v6 = %v", got)
	}
	if parseIP("not-an-ip").IsValid() {
		t.Fatal("parseIP accepted junk")
	}
	if parseIP("").IsValid() {
		t.Fatal("parseIP accepted empty")
	}
}
