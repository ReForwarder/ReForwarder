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

package dnsmanager

import (
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

func planDomain(name string, verified bool) *models.Domain {
	d := &models.Domain{
		ID:                7,
		UserID:            3,
		Domain:            name,
		VerificationToken: "tok-123",
	}
	d.OwnershipVerified = verified
	return d
}

func planKinds(plan []PlannedRecord) map[string]DNSRecordSpec {
	out := map[string]DNSRecordSpec{}
	for _, p := range plan {
		out[p.Kind] = p.Record
	}
	return out
}

// Planner completeness: an ownership-verified domain with mail routing on
// plans ALL SIX record kinds the ask names — verification, SPF, DKIM, DMARC,
// MX and return-path — each with its expected identity.
func TestPlanProducesAllSixRecordKindsForVerifiedDomain(t *testing.T) {
	plan := Plan(planDomain("example.com", true), "", PlanOptions{
		MailHost:           "mail.example.net",
		IncludeMailRouting: true,
	})
	kinds := planKinds(plan)

	for _, kind := range []string{
		RecordKindVerification, RecordKindSPF, RecordKindDKIM,
		RecordKindDMARC, RecordKindMX, RecordKindReturnPath,
	} {
		if _, ok := kinds[kind]; !ok {
			t.Fatalf("plan is missing the %q record kind; got %v", kind, kindList(plan))
		}
	}

	// Identities: apex verification + SPF, _dmarc subdomain, MX at apex and
	// at the bounce subdomain.
	if kinds[RecordKindVerification].Name != "example.com" ||
		kinds[RecordKindVerification].Value != "posta-verification=tok-123" {
		t.Fatalf("verification record wrong: %+v", kinds[RecordKindVerification])
	}
	if kinds[RecordKindSPF].Name != "example.com" ||
		kinds[RecordKindSPF].Value != "v=spf1 include:_spf.posta ~all" {
		t.Fatalf("SPF record wrong: %+v", kinds[RecordKindSPF])
	}
	if kinds[RecordKindDMARC].Name != "_dmarc.example.com" ||
		kinds[RecordKindDMARC].Value != "v=DMARC1; p=none; rua=mailto:dmarc@example.com" {
		t.Fatalf("DMARC record wrong: %+v", kinds[RecordKindDMARC])
	}
	if kinds[RecordKindMX].Type != "MX" || kinds[RecordKindMX].Value != "mail.example.net" ||
		kinds[RecordKindMX].Priority != 10 {
		t.Fatalf("MX record wrong: %+v", kinds[RecordKindMX])
	}
	if kinds[RecordKindReturnPath].Name != "bounce.example.com" ||
		kinds[RecordKindReturnPath].Value != "mail.example.net" {
		t.Fatalf("return-path record wrong: %+v", kinds[RecordKindReturnPath])
	}
}

func kindList(plan []PlannedRecord) []string {
	out := make([]string, 0, len(plan))
	for _, p := range plan {
		out = append(out, p.Kind)
	}
	return out
}

// Mail routing is opt-in: with the flag off the plan stays at the four
// deliverability records and never redirects the domain's mail receipt.
func TestPlanOmitsMailRoutingUnlessOptedIn(t *testing.T) {
	plan := Plan(planDomain("example.com", true), "", PlanOptions{MailHost: "mail.example.net"})
	kinds := planKinds(plan)
	if _, ok := kinds[RecordKindMX]; ok {
		t.Fatal("MX must not be planned without the mail-routing opt-in")
	}
	if _, ok := kinds[RecordKindReturnPath]; ok {
		t.Fatal("return-path must not be planned without the mail-routing opt-in")
	}
	if len(plan) != 4 {
		t.Fatalf("plan with routing off = %v, want exactly the 4 deliverability records", kindList(plan))
	}
}

// Fail-closed (ADR-008 decision 2): an unverified domain never gets MX or
// return-path records planned, even with routing enabled.
func TestPlanOmitsMailRoutingForUnverifiedDomain(t *testing.T) {
	plan := Plan(planDomain("example.com", false), "", PlanOptions{
		MailHost:           "mail.example.net",
		IncludeMailRouting: true,
	})
	kinds := planKinds(plan)
	if _, ok := kinds[RecordKindMX]; ok {
		t.Fatal("MX must not be planned for an unverified domain (ADR-008 fail-closed)")
	}
	if _, ok := kinds[RecordKindReturnPath]; ok {
		t.Fatal("return-path must not be planned for an unverified domain (ADR-008 fail-closed)")
	}
	// The verification/SPF/DKIM/DMARC records ARE planned: they are what gets
	// the domain verified in the first place.
	if len(plan) != 4 {
		t.Fatalf("plan for unverified domain = %v, want the 4 deliverability records", kindList(plan))
	}
}

// Mail routing with no configured mail host produces no routing records (the
// publish path surfaces ErrMailHostMissing when the operator asked for them).
func TestPlanOmitsMailRoutingWithoutMailHost(t *testing.T) {
	plan := Plan(planDomain("example.com", true), "", PlanOptions{IncludeMailRouting: true})
	kinds := planKinds(plan)
	if _, ok := kinds[RecordKindMX]; ok {
		t.Fatal("MX must not be planned without a mail host")
	}
}

// The managed DKIM signing key (R-02) wins over the legacy upstream CNAME:
// when a key TXT value is supplied the plan publishes the selector record as
// TXT with the key's public material.
func TestPlanUsesManagedDKIMKeyWhenSupplied(t *testing.T) {
	plan := Plan(planDomain("example.com", true), "v=DKIM1; h=sha256; k=rsa; p=MIIB", PlanOptions{})
	kinds := planKinds(plan)
	dkim := kinds[RecordKindDKIM]
	if dkim.Type != "TXT" || dkim.Name != "posta._domainkey.example.com" {
		t.Fatalf("DKIM record wrong: %+v", dkim)
	}
	if dkim.Value != "v=DKIM1; h=sha256; k=rsa; p=MIIB" {
		t.Fatalf("DKIM value wrong: %q", dkim.Value)
	}
}

// Without a managed key the plan falls back to the legacy upstream CNAME the
// manual flow documents (domain.RequiredRecords).
func TestPlanFallsBackToLegacyDKIMCNAME(t *testing.T) {
	plan := Plan(planDomain("example.com", true), "", PlanOptions{})
	kinds := planKinds(plan)
	dkim := kinds[RecordKindDKIM]
	if dkim.Type != "CNAME" || dkim.Name != "posta._domainkey.example.com" ||
		dkim.Value != "posta._domainkey.posta" {
		t.Fatalf("legacy DKIM CNAME wrong: %+v", dkim)
	}
}

// Every planned record carries the recoverable default TTL.
func TestPlanStampsDefaultTTL(t *testing.T) {
	plan := Plan(planDomain("example.com", true), "", PlanOptions{
		MailHost:           "mail.example.net",
		IncludeMailRouting: true,
	})
	for _, p := range plan {
		if p.Record.TTL != DefaultTTL {
			t.Fatalf("%s record TTL = %d, want %d", p.Kind, p.Record.TTL, DefaultTTL)
		}
	}
}

// A custom bounce prefix is honored on the return-path record.
func TestPlanHonorsCustomBouncePrefix(t *testing.T) {
	plan := Plan(planDomain("example.com", true), "", PlanOptions{
		MailHost:           "mail.example.net",
		IncludeMailRouting: true,
		BouncePrefix:       "bnc",
	})
	kinds := planKinds(plan)
	if kinds[RecordKindReturnPath].Name != "bnc.example.com" {
		t.Fatalf("return-path host = %q, want bnc.example.com", kinds[RecordKindReturnPath].Name)
	}
}

// The wire summary of a publish carries the R-07 verification outcome.
func TestPublishResultCarriesVerification(t *testing.T) {
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	res := PublishResult{
		Records: []DNSRecordSpec{{Type: "TXT", Name: "example.com", Value: "x"}},
		Verification: &VerificationSummary{
			OwnershipVerified: true, SPFVerified: true, CheckedAt: now,
		},
	}
	if res.Verification == nil || !res.Verification.SPFVerified || !res.Verification.CheckedAt.Equal(now) {
		t.Fatalf("publish result lost the verification summary: %+v", res.Verification)
	}
}
