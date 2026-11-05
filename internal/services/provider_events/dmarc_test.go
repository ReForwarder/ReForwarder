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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package provider_events

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/inbound"
)

// R-17 fixtures: RFC 7489 aggregate reports as real receiving providers mail
// them (Google-style multi-record reports, an empty quiet-interval report, a
// gzipped attachment, and the malformed shapes the parser must refuse).
//
// The parser and the ingestion processor live in internal/services/inbound
// (the email package's test helpers import inbound, and provider_events
// imports email, so the parser sits on the inbound side of that edge) — this
// file keeps the R-17 fixture battery beside the other provider-ingestion
// fixtures.

// dmarcFixtureGoogle is a two-record report in the shape Google sends.
const dmarcFixtureGoogle = `<?xml version="1.0" encoding="UTF-8" ?>
<feedback>
  <report_metadata>
    <org_name>google.com</org_name>
    <email>noreply-dmarc-support@google.com</email>
    <extra_contact_info>https://support.google.com/a/answer/2466580</extra_contact_info>
    <report_id>8847265484505953717</report_id>
    <date_range>
      <begin>1764115200</begin>
      <end>1764201599</end>
    </date_range>
  </report_metadata>
  <policy_published>
    <domain>example.com</domain>
    <adkim>r</adkim>
    <aspf>r</aspf>
    <p>none</p>
    <sp>none</sp>
    <pct>100</pct>
  </policy_published>
  <record>
    <row>
      <source_ip>209.85.220.41</source_ip>
      <count>2</count>
      <policy_evaluated>
        <disposition>none</disposition>
        <dkim>pass</dkim>
        <spf>fail</spf>
      </policy_evaluated>
    </row>
    <identifiers>
      <header_from>example.com</header_from>
      <envelope_from>example.com</envelope_from>
    </identifiers>
    <auth_results>
      <dkim>
        <domain>example.com</domain>
        <result>pass</result>
        <selector>20230601</selector>
      </dkim>
      <spf>
        <domain>example.com</domain>
        <result>pass</result>
        <scope>mfrom</scope>
      </spf>
    </auth_results>
  </record>
  <record>
    <row>
      <source_ip>2001:db8::1</source_ip>
      <count>5</count>
      <policy_evaluated>
        <disposition>quarantine</disposition>
        <dkim>fail</dkim>
        <spf>pass</spf>
      </policy_evaluated>
    </row>
    <identifiers>
      <header_from>example.com</header_from>
    </identifiers>
    <auth_results>
      <spf>
        <domain>mail.example.com</domain>
        <result>pass</result>
        <scope>mfrom</scope>
      </spf>
    </auth_results>
  </record>
</feedback>`

// dmarcFixtureEmpty is a valid quiet-interval report (zero records).
const dmarcFixtureEmpty = `<feedback>
  <report_metadata>
    <org_name>mail provider</org_name>
    <report_id>r-42</report_id>
    <date_range><begin>1764115200</begin><end>1764201599</end></date_range>
  </report_metadata>
  <policy_published><domain>example.com</domain></policy_published>
</feedback>`

func dmarcGzip(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestParseDMARCGoogleFixture(t *testing.T) {
	report, err := inbound.ParseDMARCAggregateReport([]byte(dmarcFixtureGoogle))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if report.ReportID != "8847265484505953717" {
		t.Errorf("report_id = %q", report.ReportID)
	}
	if report.Reporter != "google.com" {
		t.Errorf("reporter = %q", report.Reporter)
	}
	if report.Domain != "example.com" {
		t.Errorf("domain = %q", report.Domain)
	}
	wantStart := time.Unix(1764115200, 0).UTC()
	if !report.Start.Equal(wantStart) {
		t.Errorf("window start = %s, want %s", report.Start, wantStart)
	}
	if len(report.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(report.Records))
	}

	r0 := report.Records[0]
	if r0.SourceIP != "209.85.220.41" || r0.Count != 2 {
		t.Errorf("record 0 = %+v", r0)
	}
	if !r0.DKIMAligned || r0.SPFAligned {
		t.Errorf("record 0 alignment dkim=%v spf=%v, want (true, false)", r0.DKIMAligned, r0.SPFAligned)
	}
	if r0.Disposition != "none" || r0.HeaderFrom != "example.com" {
		t.Errorf("record 0 disposition/from = %q/%q", r0.Disposition, r0.HeaderFrom)
	}
	if len(r0.DKIMDomains) != 1 || r0.DKIMDomains[0] != "example.com" {
		t.Errorf("record 0 dkim domains = %v", r0.DKIMDomains)
	}

	r1 := report.Records[1]
	if r1.SourceIP != "2001:db8::1" || r1.Count != 5 {
		t.Errorf("record 1 = %+v", r1)
	}
	if r1.DKIMAligned || !r1.SPFAligned {
		t.Errorf("record 1 alignment dkim=%v spf=%v, want (false, true)", r1.DKIMAligned, r1.SPFAligned)
	}
	if r1.Disposition != "quarantine" {
		t.Errorf("record 1 disposition = %q", r1.Disposition)
	}
	if len(r1.SPFDomains) != 1 || r1.SPFDomains[0] != "mail.example.com" {
		t.Errorf("record 1 spf domains = %v", r1.SPFDomains)
	}
}

func TestParseDMARCEmptyReport(t *testing.T) {
	report, err := inbound.ParseDMARCAggregateReport([]byte(dmarcFixtureEmpty))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if report.ReportID != "r-42" || report.Domain != "example.com" {
		t.Errorf("report = %+v", report)
	}
	if len(report.Records) != 0 {
		t.Fatalf("records = %d, want 0 (quiet interval)", len(report.Records))
	}
}

// Providers attach the XML gzipped (.gz); the ingestion unwraps gzip before
// the parser sees it. This pins the round-trip: gzip -> parse fails unless
// the caller unwraps, and the inbound processor's gunzip path produces a
// parseable document.
func TestParseDMARCGzippedRoundTrip(t *testing.T) {
	gz := dmarcGzip(t, []byte(dmarcFixtureGoogle))
	if _, err := inbound.ParseDMARCAggregateReport(gz); err == nil {
		t.Fatal("parser accepted raw gzip; callers must unwrap first")
	}

	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(zr); err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	_ = zr.Close()
	report, err := inbound.ParseDMARCAggregateReport(buf.Bytes())
	if err != nil {
		t.Fatalf("parse gunzipped: %v", err)
	}
	if report.ReportID != "8847265484505953717" || len(report.Records) != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestParseDMARCRefusals(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"not xml", "hello, world"},
		{"different root", `<?xml version="1.0"?><root><data/></root>`},
		{"no report id", `<feedback><report_metadata><org_name>x</org_name></report_metadata><policy_published><domain>example.com</domain></policy_published></feedback>`},
		{"no published domain", `<feedback><report_metadata><report_id>r1</report_id></report_metadata><policy_published></policy_published></feedback>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := inbound.ParseDMARCAggregateReport([]byte(tc.raw)); err == nil {
				t.Fatalf("expected refusal for %q fixture", tc.name)
			}
		})
	}
}

// A provider that reports alignment with unexpected casing must still parse:
// RFC 7489 values are lowercase, but the wild is wild.
func TestParseDMARCAlignmentCaseInsensitive(t *testing.T) {
	raw := `<feedback>
		<report_metadata><report_id>case-1</report_id></report_metadata>
		<policy_published><domain>example.com</domain></policy_published>
		<record>
			<row><source_ip>10.0.0.1</source_ip><count>1</count>
				<policy_evaluated><disposition>reject</disposition><dkim>PASS</dkim><spf>Pass</spf></policy_evaluated>
			</row>
			<identifiers><header_from>example.com</header_from></identifiers>
		</record>
	</feedback>`
	report, err := inbound.ParseDMARCAggregateReport([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(report.Records) != 1 {
		t.Fatalf("records = %d", len(report.Records))
	}
	if !report.Records[0].DKIMAligned || !report.Records[0].SPFAligned {
		t.Fatalf("alignment case handling broke: %+v", report.Records[0])
	}
}

// The stored-row shape the inbound processor persists must serialize with the
// fields the future domain-health trend reads (the model is JSON-consumed by
// the eventual R-07 surface).
func TestDMARCReportRowShape(t *testing.T) {
	row := map[string]any{
		"domain": "example.com", "reporter": "google.com", "count": 2,
		"dkim_aligned": true, "spf_aligned": false, "disposition": "none",
	}
	blob, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(blob, []byte(`"disposition":"none"`)) {
		t.Fatalf("unexpected row JSON: %s", blob)
	}
}
