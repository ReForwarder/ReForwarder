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

package arf

import (
	"strings"
	"testing"
	"time"
)

func sampleRow() FailureRow {
	return FailureRow{
		InboundID:         42,
		UserID:            1,
		Family:            "dkim",
		Domain:            "sender.example.com",
		ClientIP:          "192.0.2.10",
		AuthResultsHeader: "authserv-id: reporter.example;\r\n\tdkim=fail (signature did not verify) header.d=sender.example.com",
		CreatedAt:         time.Date(2026, 11, 13, 10, 30, 0, 0, time.UTC),
	}
}

func buildSample(t *testing.T, message []byte) string {
	t.Helper()
	row := sampleRow()
	row.AuthResultsHeader = strings.ReplaceAll(row.AuthResultsHeader, "\r\n", "\n")
	return BuildAuthFailureReport("reporter.example", "1.0.0", row, row.AuthResultsHeader, message, time.Date(2026, 11, 14, 7, 5, 0, 0, time.UTC))
}

// The ARF MIME shape: a multipart/report with report-type=auth-failure and
// the RFC 5965 §6 part order — text/plain, message/feedback-report (the
// machine-readable field set), message/authentication-results — plus the
// bounded message/rfc822 part when the message source provides the body.
func TestBuildAuthFailureReportMimeShape(t *testing.T) {
	doc := buildSample(t, []byte("Subject: spoofed\r\n\r\nbody"))
	if !strings.Contains(doc, `report-type=auth-failure`) {
		t.Fatalf("document is not an auth-failure multipart/report:\n%s", doc)
	}
	if !strings.Contains(doc, "Content-Type: message/feedback-report") {
		t.Fatalf("machine-readable part missing:\n%s", doc)
	}
	if !strings.Contains(doc, "Content-Type: message/authentication-results") {
		t.Fatalf("authentication-results part missing:\n%s", doc)
	}
	if !strings.Contains(doc, "Content-Type: message/rfc822") {
		t.Fatalf("offending-message part missing:\n%s", doc)
	}
	// The machine-readable field set (RFC 5965 §6.1).
	for _, want := range []string{
		"Feedback-Type: auth-failure",
		"Auth-Version: 1",
		"User-Agent: reporter.example/1.0.0",
		"Reported-Domain: sender.example.com",
		"Source-IP: 192.0.2.10",
		"Arrival-Date:",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("document missing %q:\n%s", want, doc)
		}
	}
	// The stored RFC 8601 header rides the second part verbatim.
	if !strings.Contains(doc, "authserv-id: reporter.example") {
		t.Fatalf("the stored authentication-results header did not ride the report:\n%s", doc)
	}
	// The offending message rides the third part.
	if !strings.Contains(doc, "Subject: spoofed") {
		t.Fatalf("the offending message did not ride the report:\n%s", doc)
	}
	// The boundary opens the four parts (human preamble, feedback-report,
	// authentication-results, the offending message) and terminates once
	// (well-formed multipart).
	if got := strings.Count(doc, "--arf-"); got != 5 {
		t.Fatalf("boundary occurrences = %d, want 5 (4 part openers + 1 terminator) in:\n%s", got, doc)
	}
}

// Without a message source the third part is omitted (RFC 5965 §6.3's SHOULD)
// and the report stays well-formed.
func TestBuildAuthFailureReportOmitsMessagePartWhenUnavailable(t *testing.T) {
	doc := buildSample(t, nil)
	if strings.Contains(doc, "message/rfc822") {
		t.Fatalf("message part present without a message source:\n%s", doc)
	}
	if !strings.Contains(doc, "Feedback-Type: auth-failure") {
		t.Fatalf("machine-readable part missing:\n%s", doc)
	}
}

// The returned message part is bounded (the SF-10 bounded-returned-part
// posture): an oversized body is truncated, never sent whole.
func TestBuildAuthFailureReportBoundsReturnedMessage(t *testing.T) {
	big := strings.Repeat("A", 200*1024)
	doc := buildSample(t, []byte(big))
	if !strings.Contains(doc, "message/rfc822") {
		t.Fatal("message part missing")
	}
	// The report as a whole stays well under the oversized body's size.
	if len(doc) > maxReturnedMessageBytes+16*1024 {
		t.Fatalf("report size = %d, want the returned part bounded at %d", len(doc), maxReturnedMessageBytes)
	}
}

// The per-message report id is deterministic per inbound id + family + org.
func TestOutboundReportIDDeterministic(t *testing.T) {
	a := OutboundReportID(42, "dkim", "org.example")
	b := OutboundReportID(42, "dkim", "org.example")
	c := OutboundReportID(43, "dkim", "org.example")
	if a != b || a == c {
		t.Fatalf("ids = %q %q %q, want a==b and a!=c", a, b, c)
	}
	if !strings.Contains(a, "dkim") {
		t.Fatalf("id %q does not name the failure family", a)
	}
}

// The _arf address extraction: mailto URIs only, whitespace/semicolon/comma
// separated, '!'-extensions dropped, deduplicated case-insensitively.
func TestMailtoAddresses(t *testing.T) {
	got := MailtoAddresses([]string{
		"mailto:abuse@sender.example.com!10m",
		"mailto:arf@sender.example.com, mailto:ABUSE@sender.example.com; https://web.sender.example.com/arf",
		"mailto:",
		"v=spf1 -all",
	})
	if len(got) != 2 || got[0] != "abuse@sender.example.com" || got[1] != "arf@sender.example.com" {
		t.Fatalf("addresses = %v, want [abuse arf] (extensions dropped, https skipped, deduped)", got)
	}
	if got := MailtoAddresses(nil); got != nil {
		t.Fatalf("nil txts = %v, want nil", got)
	}
}
