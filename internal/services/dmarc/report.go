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

package dmarc

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/goposta/posta/internal/services/inbound/verify"
)

// RecordAggregate is one <record> block: a source IP's tally over the report
// window, with the policy-evaluated verdicts and the auth_results
// identifiers. The alignment verdicts are computed at build time against the
// LIVE published policy (adkim/aspf), exactly as the evaluator applied them
// when the stored verdicts were computed.
type RecordAggregate struct {
	SourceIP    string
	Count       int
	Disposition string // none|quarantine|reject
	// DKIMEval / SPFEval are the policy_evaluated verdicts ("pass"|"fail").
	DKIMEval string
	SPFEval  string
	// Auth-results identifiers (diagnostics, RFC 7489 §7.2.2).
	DKIMDomain   string
	DKIMSelector string
	SPFDomain    string
	HeaderFrom   string
}

// OutboundAggregateReportID is the RFC 7489 §7.2.1.4 report-id,
// deterministic per window + reported domain ("<begin>.<end>@<org>"), so a
// re-run cannot double-send under a new identity.
func OutboundAggregateReportID(start, end time.Time, orgName, domain string) string {
	return fmt.Sprintf("%d.%d@%s:%s", start.UTC().Unix(), end.UTC().Unix(), orgName, domain)
}

// BuildRecordAggregates groups the window's message rows into per-record
// tallies keyed on (source IP, disposition, aligned verdicts, auth-results
// identifiers) and computes the policy-evaluated alignment against the live
// published policy.
func BuildRecordAggregates(policy *verify.DMARCPolicy, headerFrom string, rows []RecordRow) []RecordAggregate {
	if policy == nil {
		return nil
	}
	// Record ordering: source IP, then the evaluated verdicts and
	// identifiers — deterministic for a given input set.
	byKey := map[string]*RecordAggregate{}
	var order []string
	for _, r := range rows {
		dkimEval := "fail"
		if r.DKIMResult == "pass" && verify.Aligns(r.DKIMDomain, headerFrom, policy.ADKIM) {
			dkimEval = "pass"
		}
		spfEval := "fail"
		if r.SPFResult == "pass" && verify.Aligns(r.SPFDomain, headerFrom, policy.ASPF) {
			spfEval = "pass"
		}
		key := strings.Join([]string{r.ClientIP, r.Disposition, dkimEval, spfEval,
			r.DKIMDomain, r.DKIMSelector, r.SPFDomain}, "|")
		rec := byKey[key]
		if rec == nil {
			rec = &RecordAggregate{
				SourceIP:     r.ClientIP,
				Disposition:  r.Disposition,
				DKIMEval:     dkimEval,
				SPFEval:      spfEval,
				DKIMDomain:   r.DKIMDomain,
				DKIMSelector: r.DKIMSelector,
				SPFDomain:    r.SPFDomain,
				HeaderFrom:   headerFrom,
			}
			byKey[key] = rec
			order = append(order, key)
		}
		rec.Count++
	}
	out := make([]RecordAggregate, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	return out
}

// feedback mirrors the RFC 7489 aggregate-report XML shape (§7.2). sp= and
// pct= are emitted when the published policy carries them; the receiver-side
// parser tolerates both their presence and absence.
type feedback struct {
	XMLName xml.Name `xml:"feedback"`
	Meta    struct {
		OrgName  string `xml:"org_name"`
		Email    string `xml:"email"`
		ReportID string `xml:"report_id"`
		DateRng  struct {
			Begin int64 `xml:"begin"`
			End   int64 `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	Policy struct {
		Domain string `xml:"domain"`
		ADKIM  string `xml:"adkim,omitempty"`
		ASP    string `xml:"aspf,omitempty"`
		P      string `xml:"p"`
		SP     string `xml:"sp,omitempty"`
		PCT    int    `xml:"pct"`
	} `xml:"policy_published"`
	Records []struct {
		Row struct {
			SourceIP string `xml:"source_ip"`
			Count    int    `xml:"count"`
			Eval     struct {
				Disposition string `xml:"disposition"`
				DKIM        string `xml:"dkim"`
				SPF         string `xml:"spf"`
			} `xml:"policy_evaluated"`
		} `xml:"row"`
		IDs struct {
			HeaderFrom string `xml:"header_from"`
		} `xml:"identifiers"`
		Auth struct {
			DKIMResults []struct {
				Domain   string `xml:"domain"`
				Selector string `xml:"selector,omitempty"`
				Result   string `xml:"result"`
			} `xml:"dkim"`
			SPFResults []struct {
				Domain string `xml:"domain"`
				Scope  string `xml:"scope"`
				Result string `xml:"result"`
			} `xml:"spf"`
		} `xml:"auth_results"`
	} `xml:"record"`
}

// BuildAggregateReport renders the RFC 7489 aggregate-report XML document
// for one reported domain. The document round-trips through the inbound
// receiver's own ParseDMARCAggregateReport (the same shape, one grammar).
func BuildAggregateReport(orgName, email string, start, end time.Time, reportID, policyDomain string, policy *verify.DMARCPolicy, records []RecordAggregate) ([]byte, error) {
	if orgName == "" || email == "" || reportID == "" || policyDomain == "" {
		return nil, fmt.Errorf("dmarc: report build needs org name, email, report id and policy domain")
	}
	if policy == nil {
		return nil, fmt.Errorf("dmarc: report build needs the published policy")
	}
	var doc feedback
	doc.Meta.OrgName = orgName
	doc.Meta.Email = email
	doc.Meta.ReportID = reportID
	doc.Meta.DateRng.Begin = start.UTC().Unix()
	doc.Meta.DateRng.End = end.UTC().Unix()
	doc.Policy.Domain = policyDomain
	doc.Policy.ADKIM = policy.ADKIM
	doc.Policy.ASP = policy.ASPF
	doc.Policy.P = policy.P
	doc.Policy.SP = policy.SP
	doc.Policy.PCT = policy.PCT

	for _, r := range records {
		var rec struct {
			Row struct {
				SourceIP string `xml:"source_ip"`
				Count    int    `xml:"count"`
				Eval     struct {
					Disposition string `xml:"disposition"`
					DKIM        string `xml:"dkim"`
					SPF         string `xml:"spf"`
				} `xml:"policy_evaluated"`
			} `xml:"row"`
			IDs struct {
				HeaderFrom string `xml:"header_from"`
			} `xml:"identifiers"`
			Auth struct {
				DKIMResults []struct {
					Domain   string `xml:"domain"`
					Selector string `xml:"selector,omitempty"`
					Result   string `xml:"result"`
				} `xml:"dkim"`
				SPFResults []struct {
					Domain string `xml:"domain"`
					Scope  string `xml:"scope"`
					Result string `xml:"result"`
				} `xml:"spf"`
			} `xml:"auth_results"`
		}
		rec.Row.SourceIP = r.SourceIP
		rec.Row.Count = r.Count
		rec.Row.Eval.Disposition = r.Disposition
		rec.Row.Eval.DKIM = r.DKIMEval
		rec.Row.Eval.SPF = r.SPFEval
		rec.IDs.HeaderFrom = r.HeaderFrom
		if r.DKIMDomain != "" {
			rec.Auth.DKIMResults = append(rec.Auth.DKIMResults, struct {
				Domain   string `xml:"domain"`
				Selector string `xml:"selector,omitempty"`
				Result   string `xml:"result"`
			}{Domain: r.DKIMDomain, Selector: r.DKIMSelector, Result: r.DKIMEval})
		}
		if r.SPFDomain != "" {
			rec.Auth.SPFResults = append(rec.Auth.SPFResults, struct {
				Domain string `xml:"domain"`
				Scope  string `xml:"scope"`
				Result string `xml:"result"`
			}{Domain: r.SPFDomain, Scope: "mfrom", Result: r.SPFEval})
		}
		doc.Records = append(doc.Records, rec)
	}

	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("dmarc: xml marshal: %w", err)
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// OutboundReportRequest is one generated report the sender must deliver.
type OutboundReportRequest struct {
	// ReportID is the RFC 7489 report-id (deterministic per window + reported
	// domain, so a re-run cannot double-send under a new identity).
	ReportID string
	// PolicyDomain is the domain the report is about.
	PolicyDomain string
	// Window is the covered interval.
	WindowStart, WindowEnd time.Time
	// XML is the report document (RFC 7489 §7.2).
	XML []byte
	// To holds the rua mailto endpoints discovered from the reported domain's
	// live _dmarc DNS record.
	To []string
	// From is the generator address (dmarc-report@<org domain>).
	From string
}

// ReportText is the plain-text body of the report mail (the human-readable
// companion to the attachment).
func ReportText(req *OutboundReportRequest) string {
	var b strings.Builder
	b.WriteString("This is a DMARC aggregate report for ")
	b.WriteString(req.PolicyDomain)
	b.WriteString(".\n\n")
	fmt.Fprintf(&b, "Report domain: %s\n", req.PolicyDomain)
	fmt.Fprintf(&b, "Submitter: %s\n", orgOf(req.From))
	fmt.Fprintf(&b, "Report ID: %s\n", req.ReportID)
	fmt.Fprintf(&b, "Window: %s .. %s UTC\n",
		req.WindowStart.UTC().Format(time.RFC3339), req.WindowEnd.UTC().Format(time.RFC3339))
	b.WriteString("\nThe aggregate document (RFC 7489 §7.2) is attached, gzipped.\n")
	return b.String()
}

// orgOf extracts the domain of the generator address for the subject line.
func orgOf(from string) string {
	at := strings.LastIndex(from, "@")
	if at < 0 || at+1 >= len(from) {
		return from
	}
	return from[at+1:]
}

// GzipAttachment gzips the report document for transport (RFC 7489 §7.2.3:
// the report rides as a gzipped attachment named after the report id). The
// result carries the attachment filename "<report-id>.xml.gz".
func GzipAttachment(req *OutboundReportRequest) (filename string, body []byte, err error) {
	if req == nil || len(req.XML) == 0 {
		return "", nil, fmt.Errorf("dmarc: report request carries no document")
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(req.XML); err != nil {
		return "", nil, fmt.Errorf("dmarc: gzip: %w", err)
	}
	if err := zw.Close(); err != nil {
		return "", nil, fmt.Errorf("dmarc: gzip close: %w", err)
	}
	safeID := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(req.ReportID)
	return safeID + ".xml.gz", buf.Bytes(), nil
}

// Base64Attachment encodes the gzipped document for the outbound pipeline's
// attachment content field.
func Base64Attachment(body []byte) string {
	return base64.StdEncoding.EncodeToString(body)
}
