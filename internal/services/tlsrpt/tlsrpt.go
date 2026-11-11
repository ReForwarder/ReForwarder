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

// Package tlsrpt implements the SMTP TLS reporting halves of SF-31
// (OB-6 W4, ADR-007 §5, RFC 8460) that are not bound to a concrete transport:
//
//   - ParseTLSRPTRecord reads a domain's _smtp._tls TXT record and returns the
//     published rua mailto endpoints (inbound ingest guidance + outbound
//     report-address discovery);
//   - ParseTLSRPTReport parses an inbound report document (the JSON body the
//     inbound processor hands over, gzip already unwrapped);
//   - BuildOutboundReport renders our own daily aggregate for one remote
//     policy domain from the mta_delivery_attempts substrate (SF-104 slice).
//
// Transport concerns (RCPT allowlist, gzip/size caps, durable-first ingest,
// storage) live where they belong: the inbound processor in
// internal/services/inbound/tlsrpt.go and the scheduled task in
// internal/cron/jobs.
package tlsrpt

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"
)

// TLSRPTTXTLabel is the DNS label prefix of the TLS-RPT record:
// _smtp._tls.<domain> (RFC 8460 §3.1).
const TLSRPTTXTLabel = "_smtp._tls"

// LookupTXT resolves TXT records through the system resolver (the default
// seam the cron job uses; tests override it).
func LookupTXT(ctx context.Context, name string) ([]string, error) {
	return net.LookupTXT(name)
}

// ParseTLSRPTRecord parses a _smtp._tls TXT record ("v=TLSRPTv1;
// rua=mailto:reports@example.com,mailto:alert@example.net", RFC 8460 §3.3)
// and returns the mailto endpoints. Malformed ruas are skipped; a record that
// is not TLSRPTv1 or publishes no usable mailto yields nothing.
func ParseTLSRPTRecord(txt string) (ruas []string) {
	valid := false
	for _, tag := range strings.Split(txt, ";") {
		i := strings.Index(tag, "=")
		if i <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(tag[:i]))
		value := strings.TrimSpace(tag[i+1:])
		switch key {
		case "v":
			if value != "TLSRPTv1" {
				return nil
			}
			valid = true
		case "rua":
			for _, u := range strings.Split(value, ",") {
				u = strings.TrimSpace(u)
				if addr, ok := mailtoAddress(u); ok {
					ruas = append(ruas, addr)
				}
			}
		}
	}
	if !valid {
		return nil
	}
	return ruas
}

// mailtoAddress extracts the bare address of a mailto: URI (percent-decoded).
// Anything that is not a well-formed mailto with a usable address is refused.
func mailtoAddress(u string) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(u), "mailto:") {
		return "", false
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", false
	}
	addr, err := url.QueryUnescape(parsed.Opaque)
	if err != nil || addr == "" {
		return "", false
	}
	parsedMail, err := mail.ParseAddress(addr)
	if err != nil || parsedMail.Address == "" {
		return "", false
	}
	return parsedMail.Address, true
}

// ---------- inbound report document (RFC 8460 §7.2) ----------

// TLSFailureDetail is one failure-details entry of a policy in an inbound
// report.
type TLSFailureDetail struct {
	ResultType           string `json:"result-type"`
	SendingMTAIP         string `json:"sending-mta-ip,omitempty"`
	ReceivingMTAHostname string `json:"receiving-mta-hostname,omitempty"`
	FailedSessionCount   int64  `json:"failed-session-count"`
}

// TLSPolicyResult is one policies[] entry of an inbound report: the reporter's
// summary for one policy domain.
type TLSPolicyResult struct {
	PolicyType         string             `json:"policy-type"`
	PolicyDomain       string             `json:"policy-domain"`
	MXHosts            []string           `json:"mx-host,omitempty"`
	PolicyString       []string           `json:"policy-string,omitempty"`
	SuccessfulSessions int64              `json:"total-successful-session-count"`
	FailedSessions     int64              `json:"total-failure-session-count"`
	FailureDetails     []TLSFailureDetail `json:"failure-details,omitempty"`
}

// TLSReport is the parsed inbound report document.
type TLSReport struct {
	OrganizationName string            `json:"organization-name"`
	ContactInfo      string            `json:"contact-info,omitempty"`
	ReportID         string            `json:"report-id"`
	Start            time.Time         `json:"-"`
	End              time.Time         `json:"-"`
	Policies         []TLSPolicyResult `json:"policies"`
}

// tlsReportDocument mirrors the RFC 8460 §7.2 JSON shape — used both to
// decode inbound reports and to render our own outbound ones.
type tlsReportDocument struct {
	OrganizationName string                    `json:"organization-name"`
	ContactInfo      string                    `json:"contact-info,omitempty"`
	ReportID         string                    `json:"report-id"`
	DateRange        tlsReportDateRange        `json:"date-range"`
	Policies         []tlsReportDocumentPolicy `json:"policies"`
}

type tlsReportDateRange struct {
	Start string `json:"start-datetime"`
	End   string `json:"end-datetime"`
}

type tlsReportDocumentPolicy struct {
	Policy         tlsReportDocumentPolicySpec `json:"policy"`
	Summary        tlsReportDocumentSummary    `json:"summary"`
	FailureDetails []TLSFailureDetail          `json:"failure-details,omitempty"`
}

type tlsReportDocumentPolicySpec struct {
	PolicyType   string   `json:"policy-type"`
	PolicyDomain string   `json:"policy-domain"`
	MXHost       []string `json:"mx-host,omitempty"`
	PolicyString []string `json:"policy-string,omitempty"`
}

type tlsReportDocumentSummary struct {
	TotalSuccessfulSessionCount int64 `json:"total-successful-session-count"`
	TotalFailureSessionCount    int64 `json:"total-failure-session-count"`
}

// ParseTLSRPTReport parses an inbound RFC 8460 report document. Reports with
// zero policies are structurally valid (providers send empty reports for
// quiet intervals). Structural problems — non-JSON, a missing report id or
// organization name, an unparseable date range — return an error.
func ParseTLSRPTReport(raw []byte) (*TLSReport, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, fmt.Errorf("tlsrpt: empty report document")
	}
	var doc tlsReportDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("tlsrpt: not a TLS report document: %w", err)
	}
	if doc.ReportID == "" {
		// Without a report id re-ingestion dedup is meaningless; refuse
		// rather than double-count (same rule as the R-17 DMARC parser).
		return nil, fmt.Errorf("tlsrpt: report has no report-id")
	}
	if doc.OrganizationName == "" {
		return nil, fmt.Errorf("tlsrpt: report has no organization-name")
	}
	start, end, err := parseReportDateRange(doc.DateRange.Start, doc.DateRange.End)
	if err != nil {
		return nil, err
	}

	out := &TLSReport{
		OrganizationName: doc.OrganizationName,
		ContactInfo:      doc.ContactInfo,
		ReportID:         doc.ReportID,
		Start:            start,
		End:              end,
	}
	for _, p := range doc.Policies {
		if p.Policy.PolicyDomain == "" {
			return nil, fmt.Errorf("tlsrpt: policy entry has no policy-domain")
		}
		entry := TLSPolicyResult{
			PolicyType:         p.Policy.PolicyType,
			PolicyDomain:       strings.ToLower(p.Policy.PolicyDomain),
			MXHosts:            p.Policy.MXHost,
			PolicyString:       p.Policy.PolicyString,
			SuccessfulSessions: p.Summary.TotalSuccessfulSessionCount,
			FailedSessions:     p.Summary.TotalFailureSessionCount,
		}
		if entry.PolicyType == "" {
			entry.PolicyType = "sts"
		}
		entry.FailureDetails = append(entry.FailureDetails, p.FailureDetails...)
		out.Policies = append(out.Policies, entry)
	}
	return out, nil
}

// parseReportDateRange parses the report window's RFC 3339 instants.
func parseReportDateRange(startRaw, endRaw string) (time.Time, time.Time, error) {
	if startRaw == "" || endRaw == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("tlsrpt: report has an incomplete date-range")
	}
	start, err := time.Parse(time.RFC3339, startRaw)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("tlsrpt: bad date-range start %q: %w", startRaw, err)
	}
	end, err := time.Parse(time.RFC3339, endRaw)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("tlsrpt: bad date-range end %q: %w", endRaw, err)
	}
	return start.UTC(), end.UTC(), nil
}

// ---------- outbound report building (SF-104 slice) ----------

// SessionAggregate is one (policy domain, MX host) tally of our own outbound
// delivery sessions over a report window. It is what the aggregation query
// produces and what BuildOutboundReport consumes.
type SessionAggregate struct {
	PolicyDomain string
	MXHost       string
	Successful   int64 // sessions that completed (MX 2xx at end-of-DATA)
	Failed       int64 // sessions that contacted a remote host and failed
}

// OutboundReport builds the RFC 8460 report document for one policy domain
// from our own session aggregates. orgName identifies this deployment as the
// report generator; reportID must be unique per (policy domain, window) — the
// scheduled task derives it deterministically ("<start>_<domain>").
//
// Count mapping (documented limitation): the mta_delivery_attempts substrate
// records the SMTP session outcome, not the per-attempt TLS handshake result,
// so successful sessions (delivered) count as successful and sessions that
// reached a remote host and failed count as failures — under STARTTLS-
// mandatory-when-offered outbound posture (ADR-001 §4) delivered sessions are
// TLS-secured, but a 5xx rejection after a successful TLS handshake is still
// tallied as a failure here. Per-attempt TLS columns land with the attempt
// substrate extension and are NOT silently hidden by this report.
func OutboundReport(orgName string, start, end time.Time, reportID string, agg []SessionAggregate) ([]byte, error) {
	if len(agg) == 0 {
		return nil, fmt.Errorf("tlsrpt: nothing to report for the window")
	}
	byDomain := map[string][]SessionAggregate{}
	for _, a := range agg {
		if a.PolicyDomain == "" {
			continue
		}
		if a.Successful == 0 && a.Failed == 0 {
			continue // a session tally of zero is not a session
		}
		byDomain[a.PolicyDomain] = append(byDomain[a.PolicyDomain], a)
	}
	if len(byDomain) == 0 {
		return nil, fmt.Errorf("tlsrpt: no attributable sessions in the window")
	}

	doc := tlsReportDocument{
		OrganizationName: orgName,
		ContactInfo:      contactInfoFor(orgName),
		ReportID:         reportID,
	}
	doc.DateRange.Start = start.UTC().Format(time.RFC3339)
	doc.DateRange.End = end.UTC().Format(time.RFC3339)

	// Deterministic order: sort policy domains; each domain's mx-host list is
	// sorted so the same window always serializes identically (idempotent
	// regeneration).
	domains := make([]string, 0, len(byDomain))
	for d := range byDomain {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	for _, d := range domains {
		rows := byDomain[d]
		mxSeen := map[string]bool{}
		var okc, failc int64
		for _, r := range rows {
			if r.MXHost != "" && !mxSeen[r.MXHost] {
				mxSeen[r.MXHost] = true
			}
			okc += r.Successful
			failc += r.Failed
		}
		entry := tlsReportDocumentPolicy{}
		entry.Policy.PolicyType = "sts"
		entry.Policy.PolicyDomain = d
		for h := range mxSeen {
			entry.Policy.MXHost = append(entry.Policy.MXHost, h)
		}
		sort.Strings(entry.Policy.MXHost)
		entry.Summary.TotalSuccessfulSessionCount = okc
		entry.Summary.TotalFailureSessionCount = failc
		doc.Policies = append(doc.Policies, entry)
	}
	out, err := json.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return out, nil
}

// contactInfoFor derives the report contact point from the generator
// identity: postmaster@<org host> (RFC 8460 §7.2 contact-info).
func contactInfoFor(orgName string) string {
	host := orgName
	if i := strings.Index(host, "//"); i >= 0 {
		host = host[i+2:]
	}
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimPrefix(host, "https://")
	if host == "" || strings.ContainsAny(host, " @") {
		return ""
	}
	return "postmaster@" + host
}

// OutboundReportID derives the deterministic report id for one window and
// policy domain: "<start-datetime>_<policy-domain>" (RFC 8460 §7.1 example
// shape; uniqueness per generator + window + domain).
func OutboundReportID(start time.Time, policyDomain string) string {
	return start.UTC().Format(time.RFC3339) + "_" + policyDomain
}
