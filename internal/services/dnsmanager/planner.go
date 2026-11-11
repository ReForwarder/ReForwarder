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
	"time"

	"github.com/goposta/posta/internal/models"
)

// Planned record kinds. The planner computes the full deliverability set:
// verification, SPF, DKIM, DMARC always; MX + return-path only for an
// ownership-verified domain with mail routing enabled (ADR-008 decision 2:
// MX/return-path/auto-publish actions are refused for unverified domains).
const (
	RecordKindVerification = "verification"
	RecordKindSPF          = "spf"
	RecordKindDKIM         = "dkim"
	RecordKindDMARC        = "dmarc"
	RecordKindMX           = "mx"
	RecordKindReturnPath   = "return-path"
)

// DefaultTTL is the TTL the planner stamps on records; 300 keeps a mis-publish
// recoverable within minutes.
const DefaultTTL = 300

// ReturnPathPrefix is the subdomain the bounce (VERP) mail rides.
const ReturnPathPrefix = "bounce"

// PlanOptions tunes the planner from deployment configuration.
type PlanOptions struct {
	// MailHost is the FQDN inbound mail lands at (POSTA_DNS_MAIL_HOST); it is
	// the MX target and the return-path MX target.
	MailHost string
	// IncludeMailRouting adds MX + return-path to the plan. Off by default:
	// publishing an MX would redirect the domain's mail receipt away from
	// its current provider, so the operator opts in explicitly.
	IncludeMailRouting bool
	// BouncePrefix overrides the return-path subdomain (default "bounce").
	BouncePrefix string
}

// PlannedRecord is one record of the plan with its role in the deliverability
// set — the UI renders the role, the publisher consumes the record.
type PlannedRecord struct {
	Kind   string        `json:"kind"`
	Record DNSRecordSpec `json:"record"`
}

// Plan computes the DNS records the domain needs, from the domain state.
// dkimTXT (optional) supplies the managed signing key's public TXT record
// (email.DnsTXTRecord of the Active key, R-02); when absent the plan falls
// back to the legacy upstream CNAME the manual flow shows.
func Plan(d *models.Domain, dkimTXT string, opts PlanOptions) []PlannedRecord {
	domain := normalizeName(d.Domain)

	plan := []PlannedRecord{
		{
			Kind: RecordKindVerification,
			Record: DNSRecordSpec{
				Type:  "TXT",
				Name:  domain,
				Value: "posta-verification=" + d.VerificationToken,
				TTL:   DefaultTTL,
			},
		},
		{
			// SPF mirrors domain.RequiredRecords — the record verification (R-07)
			// checks for a v=spf1 TXT at the apex.
			Kind: RecordKindSPF,
			Record: DNSRecordSpec{
				Type:  "TXT",
				Name:  domain,
				Value: "v=spf1 include:_spf.posta ~all",
				TTL:   DefaultTTL,
			},
		},
		dkimRecord(domain, dkimTXT),
		{
			// DMARC mirrors domain.RequiredRecords: v=DMARC1 at _dmarc.
			Kind: RecordKindDMARC,
			Record: DNSRecordSpec{
				Type:  "TXT",
				Name:  "_dmarc." + domain,
				Value: "v=DMARC1; p=none; rua=mailto:dmarc@" + domain,
				TTL:   DefaultTTL,
			},
		},
	}

	// Mail-routing records gate on ownership (fail-closed, ADR-008 decision 2)
	// and on the operator's explicit opt-in: an MX published at the apex
	// redirects the domain's mail RECEIPT, which is only wanted when this
	// deployment actually receives for the domain.
	if opts.IncludeMailRouting && d.IsOwnershipVerified() && opts.MailHost != "" {
		prefix := opts.BouncePrefix
		if prefix == "" {
			prefix = ReturnPathPrefix
		}
		plan = append(plan,
			PlannedRecord{
				Kind: RecordKindMX,
				Record: DNSRecordSpec{
					Type:     "MX",
					Name:     domain,
					Value:    normalizeName(opts.MailHost),
					Priority: 10,
					TTL:      DefaultTTL,
				},
			},
			// The return-path (VERP envelope sender) host: bounce.<domain>
			// routes to the same mail host so DSNs come back to the delivery
			// pipeline (ADR-002 §4's VERP return-path matching).
			PlannedRecord{
				Kind: RecordKindReturnPath,
				Record: DNSRecordSpec{
					Type:     "MX",
					Name:     prefix + "." + domain,
					Value:    normalizeName(opts.MailHost),
					Priority: 10,
					TTL:      DefaultTTL,
				},
			},
		)
	}
	return plan
}

// dkimRecord selects the DKIM publication: a managed signing key's TXT record
// when one is supplied (R-02 lifecycle), else the legacy upstream CNAME the
// manual flow documents (domain.RequiredRecords).
func dkimRecord(domain, dkimTXT string) PlannedRecord {
	if dkimTXT != "" {
		return PlannedRecord{
			Kind: RecordKindDKIM,
			Record: DNSRecordSpec{
				Type:  "TXT",
				Name:  "posta._domainkey." + domain,
				Value: dkimTXT,
				TTL:   DefaultTTL,
			},
		}
	}
	return PlannedRecord{
		Kind: RecordKindDKIM,
		Record: DNSRecordSpec{
			Type:  "CNAME",
			Name:  "posta._domainkey." + domain,
			Value: "posta._domainkey.posta",
			TTL:   DefaultTTL,
		},
	}
}

// plannedOf maps a plan onto the bare record specs a publisher consumes.
func plannedOf(plan []PlannedRecord) []DNSRecordSpec {
	out := make([]DNSRecordSpec, 0, len(plan))
	for _, p := range plan {
		out = append(out, p.Record)
	}
	return out
}

// PublishResult is the outcome of one publish + verify pass.
type PublishResult struct {
	// Records is the set that was upserted.
	Records []DNSRecordSpec `json:"records"`
	// Verification is the R-07 result recorded AFTER the publish — publish is
	// always followed by verification (ADR-007 decision 1), and DNS records
	// often need time to propagate, so verified booleans may still be false
	// here; the drift cron re-checks.
	Verification *VerificationSummary `json:"verification,omitempty"`
}

// VerificationSummary is the wire shape of the R-07 verification outcome.
type VerificationSummary struct {
	OwnershipVerified bool      `json:"ownership_verified"`
	SPFVerified       bool      `json:"spf_verified"`
	DKIMVerified      bool      `json:"dkim_verified"`
	DMARCVerified     bool      `json:"dmarc_verified"`
	CheckedAt         time.Time `json:"checked_at"`
}

// planSummary renders the plan for audit metadata without echoing secret
// material (record values are DNS-public by construction; only names+kinds).
func planSummary(plan []PlannedRecord) []map[string]any {
	out := make([]map[string]any, 0, len(plan))
	for _, p := range plan {
		out = append(out, map[string]any{
			"kind": p.Kind,
			"type": p.Record.Type,
			"name": p.Record.Name,
		})
	}
	return out
}
