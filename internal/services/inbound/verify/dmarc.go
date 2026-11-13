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
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// DMARC results (RFC 7489 §9.1 dmarc method values).
const (
	DMARCPass      = "pass"
	DMARCFail      = "fail"
	DMARCNone      = "none"
	DMARCTempError = "temperror"
	DMARCPermError = "permerror"
)

// DMARC dispositions. DispositionReject/Quarantine are RECOMMENDATIONS only:
// this engine records them for the ADR-002 routing engine and the ADR-002/006
// quarantine design; nothing here executes destructive rejection.
const (
	DispositionNone       = "none"
	DispositionQuarantine = "quarantine"
	DispositionReject     = "reject"
)

// DMARCVerdict is the outcome of one DMARC evaluation (SF-24).
type DMARCVerdict struct {
	// Result is one of pass|fail|none|temperror|permerror.
	Result string
	// Disposition is the recommended handling: none, quarantine or reject.
	// It is a recommendation — the ADR-002 routing engine decides.
	Disposition string
	// PolicyDomain is the domain whose DMARC record governed.
	PolicyDomain string
	// Policy is the effective p= (or sp=) value that produced Disposition.
	Policy string
	// PCT is the parsed pct= tag (100 when omitted).
	PCT int
	// ADKIM / ASPF are the parsed alignment modes ("r" or "s").
	ADKIM, ASPF string
	// AlignedBy records which mechanism produced a pass ("spf"/"dkim"/"").
	AlignedBy string
	// RUA / RI are the parsed reporting tags (recorded, not acted on).
	RUA []string
	RI  int
	// Reason explains a non-pass verdict or a pct downgrade.
	Reason string
}

// DMARCEvaluator evaluates the RFC 7489 policy for the RFC 5322.From domain
// given the SPF and DKIM verdicts (SF-24).
type DMARCEvaluator struct {
	Resolver Resolver
}

// Evaluate fetches the policy record and produces the verdict. sampleKey must
// be a stable per-message identifier (the inbound UUID); it drives the
// deterministic pct= sampling so the same message always gets the same
// disposition.
func (e *DMARCEvaluator) Evaluate(ctx context.Context, fromDomain string, spf SPFVerdict, dkim DKIMVerdict, sampleKey string) DMARCVerdict {
	fromDomain = normalizeDomain(fromDomain)
	if !isValidDomain(fromDomain) {
		return DMARCVerdict{Result: DMARCNone, Disposition: DispositionNone, Reason: "no usable RFC 5322.From domain"}
	}
	policyDomain, policy, result, reason := e.discoverPolicy(ctx, fromDomain)
	if policy == nil {
		res := DMARCNone
		if result != "" {
			res = result
		}
		return DMARCVerdict{Result: res, Disposition: DispositionNone, PolicyDomain: policyDomain, Reason: reason}
	}
	v := DMARCVerdict{
		PolicyDomain: policyDomain,
		Policy:       policy.P,
		PCT:          policy.PCT,
		ADKIM:        policy.ADKIM,
		ASPF:         policy.ASPF,
		RUA:          policy.RUA,
		RI:           policy.RI,
	}
	if result != "" && result != DMARCPass && result != DMARCFail {
		v.Result = result
		v.Disposition = DispositionNone
		v.Reason = reason
		return v
	}

	// RFC 7489 §6.6.1: DKIM aligned pass OR SPF aligned pass.
	if dkim.Result == DKIMPass && Aligns(dkim.Domain, fromDomain, policy.ADKIM) {
		v.Result = DMARCPass
		v.Disposition = DispositionNone
		v.AlignedBy = "dkim"
		return v
	}
	if spf.Result == SPFPass && Aligns(spf.Domain, fromDomain, policy.ASPF) {
		v.Result = DMARCPass
		v.Disposition = DispositionNone
		v.AlignedBy = "spf"
		return v
	}

	v.Result = DMARCFail
	v.Disposition = DispositionNone
	v.Reason = reason
	if reason == "" {
		v.Reason = fmt.Sprintf("neither SPF (%s) nor DKIM (%s) passed aligned", spf.Result, dkim.Result)
	}

	// RFC 7489 §6.6.3: subdomains of the policy domain use sp= when present.
	effective := policy.P
	if policyDomain != fromDomain && policy.SP != "" {
		effective = policy.SP
		v.Policy = effective
	}
	switch effective {
	case DispositionReject, DispositionQuarantine:
	default:
		return v // p=none or an invalid value → no handling recommended
	}

	// RFC 7489 §6.6.4: pct= samples the policy application. Deterministic:
	// the same message key always lands in the same bucket.
	if pctApplies(sampleKey, fromDomain, policy.PCT) {
		v.Disposition = effective
	} else {
		v.Disposition = downgrade(effective)
		v.Reason = fmt.Sprintf("%s pct=%d: sampled out, disposition downgraded to %s", effective, policy.PCT, v.Disposition)
	}
	return v
}

// discoverPolicy finds the DMARC record: _dmarc.<from-domain> first, then the
// organizational domain (RFC 7489 §6.6.3). Returns (domain, policy, "",
// "") on success or (domain, nil, result, reason) when no usable policy was
// found.
func (e *DMARCEvaluator) discoverPolicy(ctx context.Context, fromDomain string) (string, *DMARCPolicy, string, string) {
	domain, policy, result, reason := e.lookupPolicy(ctx, fromDomain)
	if policy != nil {
		return domain, policy, result, reason
	}
	if result == DMARCTempError || result == DMARCPermError {
		return fromDomain, nil, result, reason
	}
	org := OrgDomain(fromDomain)
	if org == fromDomain {
		return fromDomain, nil, DMARCNone, "no DMARC record for " + fromDomain
	}
	domain, policy, result, reason = e.lookupPolicy(ctx, org)
	if policy != nil {
		return domain, policy, result, reason
	}
	res := DMARCNone
	if result == DMARCTempError || result == DMARCPermError {
		res = result
	}
	return org, nil, res, reason
}

func (e *DMARCEvaluator) lookupPolicy(ctx context.Context, domain string) (string, *DMARCPolicy, string, string) {
	name := "_dmarc." + domain
	txts, err := e.Resolver.LookupTXT(ctx, name)
	if err != nil {
		if ErrNotFound(err) {
			return domain, nil, DMARCNone, "no DMARC record for " + domain
		}
		return domain, nil, DMARCTempError, fmt.Sprintf("dmarc txt lookup for %s: %v", name, err)
	}
	var records []string
	for _, t := range txts {
		if IsDMARCRecord(t) {
			records = append(records, t)
		}
	}
	switch {
	case len(records) == 0:
		return domain, nil, DMARCNone, "no DMARC record for " + domain
	case len(records) > 1:
		return domain, nil, DMARCPermError, fmt.Sprintf("%d DMARC records for %s", len(records), domain)
	}
	policy, err := ParseDMARCPolicy(records[0])
	if err != nil {
		return domain, nil, DMARCPermError, err.Error()
	}
	return domain, policy, "", ""
}

// DMARCPolicyForReporting resolves the published DMARC policy for the given
// From-domain using the RFC 7489 §6.6.3 discovery (_dmarc.<from-domain>
// first, then the organizational domain) and returns the domain whose record
// was found together with the parsed policy. SF-25's aggregate-report send
// side reads the live published policy for the policy-published block and
// the rua endpoints — never stored addresses. A domain without a usable
// policy record yields an error (nothing to report against); transient and
// permanent resolver/record trouble do too, so the reporting pass skips the
// domain rather than guessing.
func DMARCPolicyForReporting(ctx context.Context, res Resolver, fromDomain string) (string, *DMARCPolicy, error) {
	if res == nil {
		return "", nil, fmt.Errorf("dmarc reporting: no resolver wired")
	}
	fromDomain = normalizeDomain(fromDomain)
	if fromDomain == "" {
		return "", nil, fmt.Errorf("dmarc reporting: empty domain")
	}
	eval := DMARCEvaluator{Resolver: res}
	recordDomain, policy, _, _ := eval.discoverPolicy(ctx, fromDomain)
	if policy == nil {
		return recordDomain, nil, fmt.Errorf("dmarc reporting: no usable DMARC record for %s", fromDomain)
	}
	return recordDomain, policy, nil
}

// DMARCPolicy is the parsed DMARC record (RFC 7489 §6.4). Exported for the
// aggregate-report send side (SF-25), which reads the live published policy
// for the policy-published block and the rua endpoints.
type DMARCPolicy struct {
	P   string // p= (required)
	SP  string // sp= (optional)
	PCT int    // pct= (default 100)
	// ADKIM / ASPF are "r" (relaxed, default) or "s" (strict).
	ADKIM, ASPF string
	RUA         []string
	RI          int // ri= report interval seconds (default 86400)
}

// IsDMARCRecord reports whether one TXT record is a DMARC policy record
// (RFC 7489 §6.4: v=DMARC1, case-insensitive). Exported for the aggregate
// report send side (SF-25).
func IsDMARCRecord(record string) bool {
	for _, part := range strings.Split(record, ";") {
		part = strings.TrimSpace(part)
		if eq := strings.IndexByte(part, '='); eq > 0 && strings.EqualFold(strings.TrimSpace(part[:eq]), "v") {
			return strings.EqualFold(strings.TrimSpace(part[eq+1:]), "DMARC1")
		}
	}
	return false
}

// ParseDMARCPolicy parses the tag=value list of one DMARC record. Exported
// for the aggregate report send side (SF-25), which reads the live published
// policy for the policy-published block and the rua endpoints.
func ParseDMARCPolicy(record string) (*DMARCPolicy, error) {
	p := &DMARCPolicy{PCT: 100, ADKIM: "r", ASPF: "r", RI: 86400}
	for _, part := range strings.Split(record, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("malformed DMARC tag %q", part)
		}
		tag := strings.ToLower(strings.TrimSpace(part[:eq]))
		val := strings.TrimSpace(part[eq+1:])
		switch tag {
		case "v":
			// already validated by isDMARCRecord
		case "p":
			p.P = strings.ToLower(val)
		case "sp":
			p.SP = strings.ToLower(val)
		case "pct":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 || n > 100 {
				// RFC 7489 §6.6.4: a malformed pct= is treated as 100.
				p.PCT = 100
			} else {
				p.PCT = n
			}
		case "adkim":
			if val == "s" {
				p.ADKIM = "s"
			}
		case "aspf":
			if val == "s" {
				p.ASPF = "s"
			}
		case "rua":
			for _, uri := range strings.Split(val, ",") {
				if uri = strings.TrimSpace(uri); uri != "" {
					p.RUA = append(p.RUA, uri)
				}
			}
		case "ri":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				p.RI = n
			}
		default:
			// Unknown tags are ignored (RFC 7489 §6.4).
		}
	}
	if p.P == "" {
		return nil, fmt.Errorf("DMARC record has no p= tag")
	}
	return p, nil
}

// Aligns reports whether authDomain is aligned with fromDomain under the
// given mode ("r" relaxed = same organizational domain, "s" strict = exact).
// Exported for the aggregate report send side (SF-25), whose
// policy-evaluated blocks need the same alignment the evaluator applied.
func Aligns(authDomain, fromDomain, mode string) bool {
	if authDomain == "" || fromDomain == "" {
		return false
	}
	if strings.EqualFold(authDomain, fromDomain) {
		return true
	}
	if mode == "s" {
		return false
	}
	return strings.EqualFold(OrgDomain(authDomain), OrgDomain(fromDomain))
}

// pctApplies implements the deterministic pct= sampling: a stable FNV-1a
// hash of the message key maps onto the 0–99 range once, so a message either
// always or never falls under the policy — no per-delivery coin flips.
func pctApplies(sampleKey, fromDomain string, pct int) bool {
	if pct >= 100 {
		return true
	}
	if pct <= 0 {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(sampleKey))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(fromDomain))
	return h.Sum32()%100 < uint32(pct)
}

// downgrade returns the next-lower disposition for pct= sampled-out messages
// (RFC 7489 §6.6.4).
func downgrade(d string) string {
	switch d {
	case DispositionReject:
		return DispositionQuarantine
	case DispositionQuarantine:
		return DispositionNone
	default:
		return DispositionNone
	}
}

// twoLevelSuffixes is the PSL-free organizational-domain heuristic: a small
// static list of common two-level public suffixes. THIS IS NOT THE PUBLIC
// SUFFIX LIST — a registrant domain under a suffix absent from this list
// (or a listed suffix used below its registrant level) resolves its
// organizational domain one label too deep or shallow. The consequence is
// bounded: alignment and policy walk-up can only mis-evaluate for such
// corner-case TLDs; exact-match alignment and same-domain policies are
// unaffected.
var twoLevelSuffixes = map[string]bool{
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true,
	"me.uk": true, "net.uk": true, "sch.uk": true, "ltd.uk": true, "plc.uk": true,
	"com.au": true, "net.au": true, "org.au": true, "edu.au": true, "gov.au": true,
	"co.jp": true, "ne.jp": true, "or.jp": true, "go.jp": true, "ac.jp": true,
	"co.nz": true, "net.nz": true, "org.nz": true,
	"com.br": true, "net.br": true, "org.br": true, "gov.br": true,
	"com.ar": true, "com.mx": true, "com.co": true, "com.pe": true, "com.ve": true,
	"com.cn": true, "net.cn": true, "org.cn": true, "gov.cn": true,
	"com.tw": true, "org.tw": true, "com.hk": true, "org.hk": true,
	"com.sg": true, "com.my": true, "co.in": true, "net.in": true, "org.in": true,
	"co.za": true, "org.za": true, "com.tr": true, "com.eg": true, "com.sa": true,
	"com.ua": true, "com.pl": true, "com.es": true, "com.pt": true, "com.gr": true,
	"co.il": true, "org.il": true, "net.il": true, "co.kr": true, "or.kr": true,
	"co.th": true, "or.th": true, "com.vn": true, "com.ph": true, "com.pk": true,
	"com.bd": true, "com.np": true, "co.id": true,
}

// OrgDomain returns the best-effort organizational domain of d using the
// documented heuristic above: the registrant domain is the last two labels,
// or the last three when the last two labels are a known two-level suffix.
func OrgDomain(d string) string {
	d = normalizeDomain(d)
	labels := strings.Split(d, ".")
	if len(labels) <= 2 {
		return d
	}
	last2 := labels[len(labels)-2] + "." + labels[len(labels)-1]
	if twoLevelSuffixes[last2] {
		if len(labels) == 3 {
			return d
		}
		return labels[len(labels)-3] + "." + last2
	}
	return last2
}
