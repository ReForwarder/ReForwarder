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

package tlsrpt

import (
	"strings"
	"testing"
	"time"
)

// ---------- _smtp._tls TLS-RPT record parsing ----------

func TestParseTLSRPTRecord(t *testing.T) {
	ruas := ParseTLSRPTRecord("v=TLSRPTv1; rua=mailto:tlsrpt@example.com")
	if len(ruas) != 1 || ruas[0] != "tlsrpt@example.com" {
		t.Fatalf("single rua = %v, want [tlsrpt@example.com]", ruas)
	}

	ruas = ParseTLSRPTRecord("v=TLSRPTv1; rua=mailto:reports@example.com,mailto:alert@example.net")
	if len(ruas) != 2 || ruas[0] != "reports@example.com" || ruas[1] != "alert@example.net" {
		t.Fatalf("multi rua = %v", ruas)
	}

	if got := ParseTLSRPTRecord("v=spf1 -all"); got != nil {
		t.Fatalf("non-TLSRPT record must yield nothing, got %v", got)
	}
	if got := ParseTLSRPTRecord("v=TLSRPTv1"); got != nil {
		t.Fatalf("record without rua must yield nothing, got %v", got)
	}
	if got := ParseTLSRPTRecord("v=TLSRPTv1; rua=mailto:not an address"); got != nil {
		t.Fatalf("malformed mailto must be skipped, got %v", got)
	}
}

// ---------- inbound report document fixtures (RFC 8460 §7.2) ----------

const tlsReportFixture = `{
  "organization-name": "Example Receiver Inc.",
  "date-range": {
    "start-datetime": "2026-04-01T00:00:00Z",
    "end-datetime": "2026-04-02T00:00:00Z"
  },
  "contact-info": "sts-reporting@example.com",
  "report-id": "2026-04-01T00:00:00Z_example.com",
  "policies": [
    {
      "policy": {
        "policy-type": "sts",
        "policy-domain": "example.com",
        "mx-host": ["*.example.com"],
        "policy-string": ["version: STSv1", "mode: enforce", "mx: *.example.com", "max_age: 86400"]
      },
      "summary": {
        "total-successful-session-count": 1174,
        "total-failure-session-count": 3
      },
      "failure-details": [
        {
          "result-type": "validation-failure",
          "sending-mta-ip": "2001:db8::1",
          "receiving-mta-hostname": "mx1.example.com",
          "failed-session-count": 2
        },
        {
          "result-type": "sts-policy-fetch-error",
          "sending-mta-ip": "2001:db8::2",
          "failed-session-count": 1
        }
      ]
    }
  ]
}`

func TestParseTLSRPTReportValid(t *testing.T) {
	r, err := ParseTLSRPTReport([]byte(tlsReportFixture))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if r.OrganizationName != "Example Receiver Inc." || r.ReportID != "2026-04-01T00:00:00Z_example.com" {
		t.Fatalf("identity wrong: %+v", r)
	}
	if r.Start != time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) || r.End != time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("window wrong: %s..%s", r.Start, r.End)
	}
	if len(r.Policies) != 1 {
		t.Fatalf("policies = %d, want 1", len(r.Policies))
	}
	p := r.Policies[0]
	if p.PolicyDomain != "example.com" || p.PolicyType != "sts" {
		t.Fatalf("policy identity wrong: %+v", p)
	}
	if p.SuccessfulSessions != 1174 || p.FailedSessions != 3 {
		t.Fatalf("summary wrong: %+v", p)
	}
	if len(p.FailureDetails) != 2 || p.FailureDetails[0].FailedSessionCount != 2 {
		t.Fatalf("failure details wrong: %+v", p.FailureDetails)
	}
}

func TestParseTLSRPTReportEmptyPoliciesIsValid(t *testing.T) {
	doc := `{
		"organization-name": "Quiet Inc.",
		"date-range": {"start-datetime": "2026-04-01T00:00:00Z", "end-datetime": "2026-04-02T00:00:00Z"},
		"report-id": "quiet-1",
		"policies": []
	}`
	r, err := ParseTLSRPTReport([]byte(doc))
	if err != nil {
		t.Fatalf("empty report must parse (providers send quiet reports): %v", err)
	}
	if len(r.Policies) != 0 {
		t.Fatalf("expected zero policies, got %d", len(r.Policies))
	}
}

func TestParseTLSRPTReportRejects(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"not json":         "this is not a report",
		"no report id":     `{"organization-name":"A","date-range":{"start-datetime":"2026-04-01T00:00:00Z","end-datetime":"2026-04-02T00:00:00Z"},"policies":[]}`,
		"no org name":      `{"report-id":"x","date-range":{"start-datetime":"2026-04-01T00:00:00Z","end-datetime":"2026-04-02T00:00:00Z"},"policies":[]}`,
		"incomplete range": `{"organization-name":"A","report-id":"x","date-range":{"start-datetime":"2026-04-01T00:00:00Z"},"policies":[]}`,
		"bad range format": `{"organization-name":"A","report-id":"x","date-range":{"start-datetime":"yesterday","end-datetime":"2026-04-02T00:00:00Z"},"policies":[]}`,
		"policy no domain": `{"organization-name":"A","report-id":"x","date-range":{"start-datetime":"2026-04-01T00:00:00Z","end-datetime":"2026-04-02T00:00:00Z"},"policies":[{"policy":{"policy-type":"sts"},"summary":{"total-successful-session-count":0,"total-failure-session-count":0}}]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseTLSRPTReport([]byte(doc)); err == nil {
				t.Fatalf("%s: malformed report accepted", name)
			}
		})
	}
}

// ---------- outbound report generation (SF-104 slice) ----------

func TestOutboundReportShapeAndDeterminism(t *testing.T) {
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	agg := []SessionAggregate{
		{PolicyDomain: "remote.example", MXHost: "mx-b.remote.example", Successful: 5},
		{PolicyDomain: "remote.example", MXHost: "mx-a.remote.example", Successful: 7, Failed: 2},
	}
	id := OutboundReportID(start, "remote.example")

	doc, err := OutboundReport("mta.example", start, end, id, agg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Round-trips through our own parser (the generator shares the RFC shape).
	r, err := ParseTLSRPTReport(doc)
	if err != nil {
		t.Fatalf("generated report does not parse: %v\n%s", err, doc)
	}
	if r.ReportID != id {
		t.Fatalf("report id = %q, want %q", r.ReportID, id)
	}
	if len(r.Policies) != 1 {
		t.Fatalf("policies = %d, want 1 (one policy domain)", len(r.Policies))
	}
	p := r.Policies[0]
	if p.PolicyDomain != "remote.example" || p.PolicyType != "sts" {
		t.Fatalf("policy identity wrong: %+v", p)
	}
	if p.SuccessfulSessions != 12 || p.FailedSessions != 2 {
		t.Fatalf("summary wrong: %+v", p)
	}
	// mx-host list is sorted regardless of aggregation order.
	if len(p.MXHosts) != 2 || p.MXHosts[0] != "mx-a.remote.example" {
		t.Fatalf("mx-host wrong: %v", p.MXHosts)
	}
	// contact-info derives from the generator identity.
	if r.ContactInfo != "postmaster@mta.example" {
		t.Fatalf("contact-info = %q", r.ContactInfo)
	}

	// Deterministic: the same window + aggregates serialize identically
	// (regenerating a report must not mint a new document).
	again, err := OutboundReport("mta.example", start, end, id, agg)
	if err != nil || string(again) != string(doc) {
		t.Fatalf("report generation is not deterministic")
	}
}

func TestOutboundReportRefusesEmptyWindow(t *testing.T) {
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	if _, err := OutboundReport("mta.example", start, start.Add(24*time.Hour), "id", nil); err == nil {
		t.Fatalf("empty window must refuse (quiet days are never reported)")
	}
	// Aggregates that all-zero out after session filtering also refuse.
	zeroed := []SessionAggregate{{PolicyDomain: "x.example", MXHost: "", Successful: 0, Failed: 0}}
	if _, err := OutboundReport("mta.example", start, start.Add(24*time.Hour), "id", zeroed); err == nil {
		t.Fatalf("all-zero aggregates must refuse")
	}
}

func TestOutboundReportIDDeterministic(t *testing.T) {
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	a := OutboundReportID(start, "remote.example")
	b := OutboundReportID(start, "remote.example")
	if a != b {
		t.Fatalf("report id must be deterministic per window + domain: %q vs %q", a, b)
	}
	other := OutboundReportID(start.Add(24*time.Hour), "remote.example")
	if other == a {
		t.Fatalf("a new window must mint a new report id")
	}
	if !strings.Contains(a, "remote.example") {
		t.Fatalf("report id must carry the policy domain: %q", a)
	}
	if !strings.HasPrefix(a, "2026-11-09T00:00:00Z") {
		t.Fatalf("report id must key on the UTC window start: %q", a)
	}
}

// ---------- aggregation over the attempt substrate ----------

func TestRecipientDomainsDerivation(t *testing.T) {
	got := recipientDomains([]string{"A@Example.com", "b@sub.example.com", "not-an-address", "c@example.com", "@", ""})
	if len(got) != 2 || got[0] != "example.com" || got[1] != "sub.example.com" {
		t.Fatalf("recipientDomains = %v, want [example.com sub.example.com]", got)
	}
}

func TestChunkIDs(t *testing.T) {
	var ids []uint
	for i := 0; i < 1001; i++ {
		ids = append(ids, uint(i))
	}
	chunks := chunkIDs(ids, 500)
	if len(chunks) != 3 || len(chunks[0]) != 500 || len(chunks[1]) != 500 || len(chunks[2]) != 1 {
		t.Fatalf("chunkIDs = %d chunks, want 3 (500/500/1)", len(chunks))
	}
	if chunkIDs(nil, 500) != nil {
		t.Fatalf("nil ids must yield no chunks")
	}
}
