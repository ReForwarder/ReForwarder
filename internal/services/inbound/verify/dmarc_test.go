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
	"testing"
)

// dmarcPass is the SPF/DKIM state a passing DMARC evaluation needs.
func spfVerdict(result, domain string) SPFVerdict {
	return SPFVerdict{Result: result, Domain: domain}
}

func dkimVerdict(result, domain, selector string) DKIMVerdict {
	return DKIMVerdict{Result: result, Domain: domain, Selector: selector}
}

func evalDMARC(t *testing.T, dns *fakeDNS, fromDomain string, spf SPFVerdict, dkim DKIMVerdict, sampleKey string) DMARCVerdict {
	t.Helper()
	e := &DMARCEvaluator{Resolver: dns}
	return e.Evaluate(context.Background(), fromDomain, spf, dkim, sampleKey)
}

// DKIM aligned pass under the default relaxed mode (org-domain equality).
func TestDMARCPassDKIMRelaxedAlignment(t *testing.T) {
	dns := newFakeDNS().
		withTXT("_dmarc.example.test", "v=DMARC1; p=reject; rua=mailto:agg@example.test")
	got := evalDMARC(t, dns, "example.test",
		spfVerdict(SPFFail, "example.test"),
		dkimVerdict(DKIMPass, "mail.example.test", "s1"), "key-1")
	if got.Result != DMARCPass || got.AlignedBy != "dkim" {
		t.Fatalf("dmarc = %s alignedBy=%q (%s), want pass via dkim", got.Result, got.AlignedBy, got.Reason)
	}
	if got.Disposition != DispositionNone {
		t.Fatalf("pass disposition = %s, want none", got.Disposition)
	}
	if got.PolicyDomain != "example.test" || got.Policy != "reject" {
		t.Fatalf("policy = %s/%s, want example.test/reject", got.PolicyDomain, got.Policy)
	}
	if len(got.RUA) != 1 || got.RUA[0] != "mailto:agg@example.test" {
		t.Fatalf("rua = %v, want the mailto URI recorded", got.RUA)
	}
	if got.RI != 86400 {
		t.Fatalf("ri default = %d, want 86400", got.RI)
	}
}

// SPF aligned pass; strict aspf accepts only the exact policy domain.
func TestDMARCPassSPFStrictAlignment(t *testing.T) {
	dns := newFakeDNS().
		withTXT("_dmarc.example.test", "v=DMARC1; p=quarantine; aspf=s")
	got := evalDMARC(t, dns, "example.test",
		spfVerdict(SPFPass, "example.test"),
		dkimVerdict(DKIMNone, "", ""), "key-1")
	if got.Result != DMARCPass || got.AlignedBy != "spf" {
		t.Fatalf("strict same-domain spf = %s alignedBy=%q, want pass via spf", got.Result, got.AlignedBy)
	}

	// Strict mode rejects the organizational-domain match a relaxed mode
	// would accept: spf passed for a subdomain, aspf=s → fail.
	dns2 := newFakeDNS().
		withTXT("_dmarc.example.test", "v=DMARC1; p=quarantine; aspf=s")
	got2 := evalDMARC(t, dns2, "example.test",
		spfVerdict(SPFPass, "mail.example.test"),
		dkimVerdict(DKIMNone, "", ""), "key-1")
	if got2.Result != DMARCFail || got2.Disposition != DispositionQuarantine {
		t.Fatalf("strict subdomain spf = %s/%s, want fail/quarantine", got2.Result, got2.Disposition)
	}

	// The same setup under relaxed alignment passes.
	dns3 := newFakeDNS().
		withTXT("_dmarc.example.test", "v=DMARC1; p=quarantine; aspf=r")
	got3 := evalDMARC(t, dns3, "example.test",
		spfVerdict(SPFPass, "mail.example.test"),
		dkimVerdict(DKIMNone, "", ""), "key-1")
	if got3.Result != DMARCPass {
		t.Fatalf("relaxed subdomain spf = %s (%s), want pass", got3.Result, got3.Reason)
	}
}

// Strict adkim likewise requires the exact domain for the DKIM identity.
func TestDMARCFailStrictADKIMSubdomain(t *testing.T) {
	dns := newFakeDNS().
		withTXT("_dmarc.example.test", "v=DMARC1; p=reject; adkim=s")
	got := evalDMARC(t, dns, "example.test",
		spfVerdict(SPFFail, "example.test"),
		dkimVerdict(DKIMPass, "mail.example.test", "s1"), "key-1")
	if got.Result != DMARCFail || got.Disposition != DispositionReject {
		t.Fatalf("strict adkim subdomain = %s/%s, want fail/reject", got.Result, got.Disposition)
	}
}

// Neither mechanism passes aligned → fail with the policy disposition.
func TestDMARCFailPolicyReject(t *testing.T) {
	dns := newFakeDNS().
		withTXT("_dmarc.example.test", "v=DMARC1; p=reject")
	got := evalDMARC(t, dns, "example.test",
		spfVerdict(SPFFail, "example.test"),
		dkimVerdict(DKIMFail, "example.test", "s1"), "key-1")
	if got.Result != DMARCFail {
		t.Fatalf("dmarc = %s (%s), want fail", got.Result, got.Reason)
	}
	if got.Disposition != DispositionReject {
		t.Fatalf("disposition = %s, want reject (recommendation only)", got.Disposition)
	}
}

// sp= governs subdomains of the policy domain (RFC 7489 §6.3).
func TestDMARCSubdomainPolicySP(t *testing.T) {
	dns := newFakeDNS().
		withTXT("_dmarc.example.test", "v=DMARC1; p=none; sp=quarantine")
	got := evalDMARC(t, dns, "sub.example.test",
		spfVerdict(SPFFail, "sub.example.test"),
		dkimVerdict(DKIMNone, "", ""), "key-1")
	if got.Result != DMARCFail || got.Disposition != DispositionQuarantine {
		t.Fatalf("sp= subdomain = %s/%s, want fail/quarantine", got.Result, got.Disposition)
	}
	if got.Policy != "quarantine" {
		t.Fatalf("effective policy = %q, want sp=quarantine", got.Policy)
	}
}

// The policy record is found one level up at the organizational domain
// (RFC 7489 §6.6.3) when the From domain has none.
func TestDMARCPolicyWalkUpToOrgDomain(t *testing.T) {
	dns := newFakeDNS().
		withTXT("_dmarc.example.test", "v=DMARC1; p=quarantine")
	got := evalDMARC(t, dns, "mail.example.test",
		spfVerdict(SPFPass, "example.test"),
		dkimVerdict(DKIMNone, "", ""), "key-1")
	if got.Result != DMARCPass || got.PolicyDomain != "example.test" {
		t.Fatalf("walk-up = %s @%s, want pass @example.test", got.Result, got.PolicyDomain)
	}
}

// No DMARC record anywhere → result none, disposition none.
func TestDMARCNoRecordYieldsNone(t *testing.T) {
	got := evalDMARC(t, newFakeDNS(), "example.test",
		spfVerdict(SPFFail, "example.test"), dkimVerdict(DKIMNone, "", ""), "key-1")
	if got.Result != DMARCNone || got.Disposition != DispositionNone {
		t.Fatalf("no record = %s/%s, want none/none", got.Result, got.Disposition)
	}
}

// DNS trouble discovering the policy is temperror, not a silent pass.
func TestDMARCTempErrorOnDNSFailure(t *testing.T) {
	dns := newFakeDNS()
	dns.fail("_dmarc.example.test", tempErr("_dmarc.example.test"))
	got := evalDMARC(t, dns, "example.test",
		spfVerdict(SPFFail, "example.test"), dkimVerdict(DKIMNone, "", ""), "key-1")
	if got.Result != DMARCTempError {
		t.Fatalf("dns failure = %s (%s), want temperror", got.Result, got.Reason)
	}
}

// Two published policy records are a permerror.
func TestDMARCMultipleRecordsPermError(t *testing.T) {
	dns := newFakeDNS().
		withTXT("_dmarc.example.test", "v=DMARC1; p=none", "v=DMARC1; p=reject")
	got := evalDMARC(t, dns, "example.test",
		spfVerdict(SPFFail, "example.test"), dkimVerdict(DKIMNone, "", ""), "key-1")
	if got.Result != DMARCPermError {
		t.Fatalf("two records = %s, want permerror", got.Result)
	}
}

// A record without the required p= tag is a permerror.
func TestDMARCMissingPTagPermError(t *testing.T) {
	dns := newFakeDNS().
		withTXT("_dmarc.example.test", "v=DMARC1; rua=mailto:agg@example.test")
	got := evalDMARC(t, dns, "example.test",
		spfVerdict(SPFFail, "example.test"), dkimVerdict(DKIMNone, "", ""), "key-1")
	if got.Result != DMARCPermError {
		t.Fatalf("p missing = %s, want permerror", got.Result)
	}
}

// pct=0 never applies the policy; pct=100 always does (deterministic bounds).
func TestDMARCPCTBounds(t *testing.T) {
	dns := newFakeDNS().withTXT("_dmarc.example.test", "v=DMARC1; p=quarantine; pct=0")
	got := evalDMARC(t, dns, "example.test",
		spfVerdict(SPFFail, "example.test"), dkimVerdict(DKIMNone, "", ""), "key-1")
	if got.Disposition != DispositionNone {
		t.Fatalf("pct=0 → disposition %s, want none (sampled out)", got.Disposition)
	}

	dns2 := newFakeDNS().withTXT("_dmarc.example.test", "v=DMARC1; p=quarantine; pct=100")
	got2 := evalDMARC(t, dns2, "example.test",
		spfVerdict(SPFFail, "example.test"), dkimVerdict(DKIMNone, "", ""), "key-1")
	if got2.Disposition != DispositionQuarantine {
		t.Fatalf("pct=100 → disposition %s, want quarantine", got2.Disposition)
	}
	if got2.PCT != 100 {
		t.Fatalf("parsed pct = %d, want 100", got2.PCT)
	}
}

// pct=reject downgrade: sampled-out reject messages step down to quarantine
// (RFC 7489 §6.6.4), and the same sample key always lands the same way —
// no per-delivery coin flips.
func TestDMARCPCTSamplingDeterministic(t *testing.T) {
	dns := newFakeDNS().withTXT("_dmarc.example.test", "v=DMARC1; p=reject; pct=50")
	eval := func(key string) (string, string) {
		got := evalDMARC(t, dns, "example.test",
			spfVerdict(SPFFail, "example.test"), dkimVerdict(DKIMNone, "", ""), key)
		return got.Result, got.Disposition
	}

	// Determinism: identical keys → identical verdicts across re-evaluation.
	for _, key := range []string{"inb-1", "inb-2", "inb-3", "inb-4"} {
		res1, disp1 := eval(key)
		res2, disp2 := eval(key)
		if res1 != res2 || disp1 != disp2 {
			t.Fatalf("sample key %q is non-deterministic: %s/%s vs %s/%s", key, res1, disp1, res2, disp2)
		}
	}

	// With pct=50 over 32 keys both buckets must occur (odds of a single
	// bucket: 2^-31) and sampled-out rejects step down to quarantine.
	sawApplied, sawDowngraded := false, false
	for i := 0; i < 32; i++ {
		key := "inb-" + string(rune('a'+i))
		_, disp := eval(key)
		switch disp {
		case DispositionReject:
			sawApplied = true
		case DispositionQuarantine:
			sawDowngraded = true
		default:
			t.Fatalf("unexpected disposition %q under p=reject", disp)
		}
	}
	if !sawApplied || !sawDowngraded {
		t.Fatalf("pct=50 sampling did not split: applied=%v downgraded=%v", sawApplied, sawDowngraded)
	}
}

// pct sampling never affects a DMARC pass.
func TestDMARCPCTNeverTouchesPass(t *testing.T) {
	dns := newFakeDNS().withTXT("_dmarc.example.test", "v=DMARC1; p=reject; pct=0")
	got := evalDMARC(t, dns, "example.test",
		spfVerdict(SPFPass, "example.test"), dkimVerdict(DKIMNone, "", ""), "key-1")
	if got.Result != DMARCPass || got.Disposition != DispositionNone {
		t.Fatalf("pass under pct=0 = %s/%s, want pass/none", got.Result, got.Disposition)
	}
}

// The organizational-domain heuristic (PSL-free, documented): two labels by
// default, three under a known two-level suffix, and never fewer labels than
// the input has.
func TestOrgDomainHeuristic(t *testing.T) {
	cases := []struct{ in, want string }{
		{"example.test", "example.test"},
		{"EXAMPLE.test", "example.test"},
		{"mail.example.test", "example.test"},
		{"deep.sub.mail.example.test", "example.test"},
		{"example.co.uk", "example.co.uk"},
		{"example.co.uk.", "example.co.uk"},
		{"a.example.co.uk", "example.co.uk"},
		{"a.b.example.co.uk", "example.co.uk"},
		{"localhost", "localhost"},
	}
	for _, tc := range cases {
		if got := OrgDomain(tc.in); got != tc.want {
			t.Errorf("OrgDomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Non-DMARC1 v= tags are not policy records.
func TestDMARCRecordVersionValidation(t *testing.T) {
	dns := newFakeDNS().withTXT("_dmarc.example.test", "v=DMARC2; p=reject")
	got := evalDMARC(t, dns, "example.test",
		spfVerdict(SPFFail, "example.test"), dkimVerdict(DKIMNone, "", ""), "key-1")
	if got.Result != DMARCNone {
		t.Fatalf("v=DMARC2 → %s, want none (no valid policy)", got.Result)
	}
	if !IsDMARCRecord("V=DMARC1; p=none") {
		t.Fatal("case-insensitive v=DMARC1 not accepted")
	}
	if IsDMARCRecord("v=spf1 -all") {
		t.Fatal("an SPF record must not parse as DMARC")
	}
}
