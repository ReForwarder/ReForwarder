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
	"io"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/inbound"
	"github.com/goposta/posta/internal/services/inbound/verify"
)

// SF-25 send-side tests: the RFC 7489 aggregate-report XML builder. The
// documents round-trip through the inbound receiver's own parser — the same
// grammar on both sides, one proof.

func testPolicy() *verify.DMARCPolicy {
	return &verify.DMARCPolicy{
		P:    "reject",
		SP:   "quarantine",
		PCT:  100,
		ADKIM: "r",
		ASPF:  "r",
		RUA:  []string{"mailto:reports@example.com"},
	}
}

func sampleRows() []RecordRow {
	return []RecordRow{
		{ClientIP: "192.0.2.10", Disposition: "none", SPFResult: "pass", SPFDomain: "mail.example.com",
			DKIMResult: "pass", DKIMDomain: "example.com", DKIMSelector: "s1"},
		{ClientIP: "192.0.2.10", Disposition: "none", SPFResult: "pass", SPFDomain: "mail.example.com",
			DKIMResult: "pass", DKIMDomain: "example.com", DKIMSelector: "s1"},
		{ClientIP: "198.51.100.7", Disposition: "quarantine", SPFResult: "fail", SPFDomain: "spoofer.example.net",
			DKIMResult: "fail", DKIMDomain: "", DKIMSelector: ""},
	}
}

func buildSample(t *testing.T, policy *verify.DMARCPolicy, rows []RecordRow) []byte {
	t.Helper()
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	id := OutboundAggregateReportID(start, end, "reporter.example", "example.com")
	records := BuildRecordAggregates(policy, "example.com", rows)
	doc, err := BuildAggregateReport("reporter.example", "dmarc-report@reporter.example", start, end, id, "example.com", policy, records)
	if err != nil {
		t.Fatalf("BuildAggregateReport: %v", err)
	}
	return doc
}

// The document round-trips through the inbound receiver's own parser
// losslessly: metadata, published policy, and the record tallies with their
// policy-evaluated verdicts and auth_results.
func TestBuildAggregateReportRoundTripsThroughReceiverParser(t *testing.T) {
	doc := buildSample(t, testPolicy(), sampleRows())

	parsed, err := inbound.ParseDMARCAggregateReport(doc)
	if err != nil {
		t.Fatalf("receiver-side parse: %v", err)
	}
	if parsed.Reporter != "reporter.example" || parsed.ReporterEmail != "dmarc-report@reporter.example" {
		t.Fatalf("metadata = %s/%s", parsed.Reporter, parsed.ReporterEmail)
	}
	if parsed.Domain != "example.com" || parsed.Policy.Domain != "example.com" {
		t.Fatalf("published domain = %s/%s", parsed.Domain, parsed.Policy.Domain)
	}
	if parsed.Policy.Policy != "reject" || parsed.Policy.ADKIM != "r" || parsed.Policy.ASP != "r" {
		t.Fatalf("published policy = %+v", parsed.Policy)
	}
	if len(parsed.Records) != 2 {
		t.Fatalf("records = %d, want 2 (two source IPs, aligned rows merged)", len(parsed.Records))
	}
	var pass, fail *inbound.DMARCRecord
	for i := range parsed.Records {
		switch parsed.Records[i].SourceIP {
		case "192.0.2.10":
			pass = &parsed.Records[i]
		case "198.51.100.7":
			fail = &parsed.Records[i]
		}
	}
	if pass == nil || fail == nil {
		t.Fatalf("records = %+v, want one per source IP", parsed.Records)
	}
	if pass.Count != 2 || pass.Disposition != "none" || !pass.DKIMAligned || !pass.SPFAligned {
		t.Fatalf("pass record = %+v, want count 2 / none / aligned pass", pass)
	}
	if fail.Count != 1 || fail.Disposition != "quarantine" || fail.DKIMAligned || fail.SPFAligned {
		t.Fatalf("fail record = %+v, want count 1 / quarantine / aligned fail", fail)
	}
	if pass.HeaderFrom != "example.com" {
		t.Fatalf("header_from = %q, want example.com", pass.HeaderFrom)
	}
}

// The strict-alignment modes bite: under adkim=s a DKIM signature from the
// organizational domain of the header-from does NOT align, while under r it
// does (the same predicate the evaluator applied at verdict time).
func TestBuildRecordAggregatesAlignmentMatrix(t *testing.T) {
	rows := []RecordRow{
		{ClientIP: "192.0.2.1", Disposition: "none", SPFResult: "none", SPFDomain: "",
			DKIMResult: "pass", DKIMDomain: "sub.example.com", DKIMSelector: "s1"},
	}
	relaxed := &verify.DMARCPolicy{P: "none", ADKIM: "r", ASPF: "r"}
	strict := &verify.DMARCPolicy{P: "none", ADKIM: "s", ASPF: "s"}

	relaxedRecs := BuildRecordAggregates(relaxed, "example.com", rows)
	if len(relaxedRecs) != 1 || relaxedRecs[0].DKIMEval != "pass" {
		t.Fatalf("relaxed alignment = %+v, want dkim pass (same org domain)", relaxedRecs)
	}
	strictRecs := BuildRecordAggregates(strict, "example.com", rows)
	if len(strictRecs) != 1 || strictRecs[0].DKIMEval != "fail" {
		t.Fatalf("strict alignment = %+v, want dkim fail (subdomain under adkim=s)", strictRecs)
	}
}

// The published policy block carries the live record's data: adkim/aspf, p,
// sp and pct land on the document.
func TestBuildAggregateReportCarriesPublishedPolicy(t *testing.T) {
	doc := buildSample(t, testPolicy(), sampleRows())
	s := string(doc)
	for _, want := range []string{"<domain>example.com</domain>", "<adkim>r</adkim>", "<aspf>r</aspf>",
		"<p>reject</p>", "<sp>quarantine</sp>", "<pct>100</pct>"} {
		if !strings.Contains(s, want) {
			t.Fatalf("document missing %s:\n%s", want, s)
		}
	}
}

// The report id is deterministic per window + domain + org — a re-run cannot
// double-send under a new identity.
func TestOutboundAggregateReportIDDeterministic(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	a := OutboundAggregateReportID(start, end, "org.example", "example.com")
	b := OutboundAggregateReportID(start, end, "org.example", "example.com")
	c := OutboundAggregateReportID(start, end.Add(time.Hour), "org.example", "example.com")
	if a != b {
		t.Fatalf("ids differ across identical inputs: %q vs %q", a, b)
	}
	if a == c {
		t.Fatal("different windows produced the same id")
	}
	if !strings.Contains(a, "example.com") {
		t.Fatalf("id %q does not name the reported domain", a)
	}
}

// The attachment gzips under the RFC 7489 §7.2.3 naming and decompresses to
// the exact document.
func TestGzipAttachmentRoundTrip(t *testing.T) {
	req := &OutboundReportRequest{
		ReportID:     "1765411200.1765497600@org.example:example.com",
		PolicyDomain: "example.com",
		XML:          buildSample(t, testPolicy(), sampleRows()),
	}
	filename, body, err := GzipAttachment(req)
	if err != nil {
		t.Fatalf("GzipAttachment: %v", err)
	}
	if !strings.HasSuffix(filename, ".xml.gz") || !strings.Contains(filename, "example.com") {
		t.Fatalf("filename = %q, want <report-id>.xml.gz", filename)
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	back, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	if !bytes.Equal(back, req.XML) {
		t.Fatal("decompressed document differs from the built XML")
	}
	if _, err := inbound.ParseDMARCAggregateReport(back); err != nil {
		t.Fatalf("decompressed document does not parse: %v", err)
	}
}

// The text body names the domain, submitter, id and window.
func TestReportTextShape(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	req := &OutboundReportRequest{
		ReportID:     "id-1",
		PolicyDomain: "example.com",
		WindowStart:  start,
		WindowEnd:    start.Add(24 * time.Hour),
		From:         "dmarc-report@reporter.example",
	}
	text := ReportText(req)
	for _, want := range []string{"example.com", "reporter.example", "id-1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}
}

// The rua extraction: mailto URIs only, extensions after the '!' separator
// dropped, https: skipped, deduplicated case-insensitively.
func TestMailtoRuas(t *testing.T) {
	policy := &verify.DMARCPolicy{
		RUA: []string{
			"mailto:reports@example.com!50m!10m",
			"MAILTO:other@example.com",
			"https://reports.example.com/callback",
			"mailto:reports@example.com",
			"mailto:",
		},
	}
	got := MailtoRuas(policy)
	if len(got) != 2 || got[0] != "reports@example.com" || got[1] != "other@example.com" {
		t.Fatalf("ruas = %v, want [reports@example.com other@example.com] (extensions dropped, https skipped, deduped)", got)
	}
	if got := MailtoRuas(nil); got != nil {
		t.Fatalf("nil policy ruas = %v, want nil", got)
	}
}
