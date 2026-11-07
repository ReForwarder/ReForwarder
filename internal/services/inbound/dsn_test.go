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

package inbound

import (
	"errors"
	"strings"
	"testing"
)

// dsnFixture builds a multipart/report;report-type=delivery-status message
// around one delivery-status part body. statusPart is the raw per-message +
// per-recipient field text (CRLF-joined by this helper).
func dsnFixture(statusPart string, extraParts ...string) []byte {
	var b strings.Builder
	b.WriteString("From: Mail Delivery System <MAILER-DAEMON@mx.remote.test>\r\n")
	b.WriteString("To: bounce+6ba7b8109dad11d180b400c04fd430c8=deadbeefdeadbeefdead@bounce.mail.example.test\r\n")
	b.WriteString("Subject: Undelivered Mail Returned to Sender\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/report; report-type=delivery-status; boundary=\"=_dsn\"\r\n")
	b.WriteString("\r\n")
	b.WriteString("--=_dsn\r\n")
	b.WriteString("Content-Type: text/plain\r\n")
	b.WriteString("\r\n")
	b.WriteString("This is the mail system at host mx.remote.test.\r\nI'm afraid the message was not delivered.\r\n")
	b.WriteString("--=_dsn\r\n")
	b.WriteString("Content-Type: message/delivery-status\r\n")
	b.WriteString("\r\n")
	b.WriteString(statusPart)
	if !strings.HasSuffix(statusPart, "\r\n") {
		b.WriteString("\r\n")
	}
	for _, p := range extraParts {
		b.WriteString("--=_dsn\r\n")
		b.WriteString(p)
		b.WriteString("--=_dsn\r\n")
	}
	b.WriteString("--=_dsn--\r\n")
	return []byte(b.String())
}

func dsnFailedStatusPart() string {
	return strings.Join([]string{
		"Reporting-MTA: dns; mx.remote.test",
		"",
		"Original-Recipient: rfc822;user@dest.example",
		"Final-Recipient: rfc822;User@Dest.Example",
		"Action: failed",
		"Status: 5.1.1",
		"Diagnostic-Code: smtp; 550 5.1.1 The email account that you tried to reach does not exist",
		"Remote-MTA: dns; mx.dest.example",
		"",
	}, "\r\n")
}

func dsnDelayedStatusPart() string {
	return strings.Join([]string{
		"Reporting-MTA: dns; mx.remote.test",
		"",
		"Final-Recipient: rfc822;slow@dest.example",
		"Original-Recipient: rfc822;slow@dest.example",
		"Action: delayed",
		"Status: 4.4.1",
		"Diagnostic-Code: smtp; 421 connection timed out",
		"",
	}, "\r\n")
}

// A canonical failed DSN parses to the per-recipient fields the match-back
// needs: action, enhanced status, original recipient (normalized), diagnostic.
func TestParseDSNFailedFixture(t *testing.T) {
	dsn, err := ParseDSN(dsnFixture(dsnFailedStatusPart()))
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if dsn.ReportingMTA != "mx.remote.test" {
		t.Fatalf("ReportingMTA = %q, want mx.remote.test", dsn.ReportingMTA)
	}
	if len(dsn.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(dsn.Records))
	}
	rec := dsn.Records[0]
	if rec.Action != DSNActionFailed {
		t.Fatalf("Action = %q, want failed", rec.Action)
	}
	if rec.Status != "5.1.1" {
		t.Fatalf("Status = %q, want 5.1.1", rec.Status)
	}
	if rec.OriginalRecipient != "user@dest.example" {
		t.Fatalf("OriginalRecipient = %q, want user@dest.example", rec.OriginalRecipient)
	}
	if rec.Recipient() != "user@dest.example" {
		t.Fatalf("Recipient = %q, want the original recipient", rec.Recipient())
	}
	if !strings.Contains(rec.DiagnosticCode, "550 5.1.1") {
		t.Fatalf("DiagnosticCode = %q, want the remote 550 diagnostic", rec.DiagnosticCode)
	}
	if rec.RemoteMTA != "mx.dest.example" {
		t.Fatalf("RemoteMTA = %q, want mx.dest.example", rec.RemoteMTA)
	}
}

// A delayed DSN parses with its transient status code.
func TestParseDSNDelayedFixture(t *testing.T) {
	dsn, err := ParseDSN(dsnFixture(dsnDelayedStatusPart()))
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if len(dsn.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(dsn.Records))
	}
	rec := dsn.Records[0]
	if rec.Action != DSNActionDelayed {
		t.Fatalf("Action = %q, want delayed", rec.Action)
	}
	if rec.Status != "4.4.1" {
		t.Fatalf("Status = %q, want 4.4.1", rec.Status)
	}
	if rec.Recipient() != "slow@dest.example" {
		t.Fatalf("Recipient = %q, want slow@dest.example", rec.Recipient())
	}
}

// A delivered DSN (remote acknowledged delivery) parses the same way.
func TestParseDSNDeliveredFixture(t *testing.T) {
	part := strings.Join([]string{
		"Reporting-MTA: dns; mx.remote.test",
		"",
		"Final-Recipient: rfc822;gone@dest.example",
		"Action: delivered",
		"Status: 2.0.0",
		"",
	}, "\r\n")
	dsn, err := ParseDSN(dsnFixture(part))
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	rec := dsn.Records[0]
	if rec.Action != DSNActionDelivered {
		t.Fatalf("Action = %q, want delivered", rec.Action)
	}
	if rec.Status != "2.0.0" {
		t.Fatalf("Status = %q, want 2.0.0", rec.Status)
	}
	// No Original-Recipient: the final recipient is the fallback.
	if rec.Recipient() != "gone@dest.example" {
		t.Fatalf("Recipient = %q, want gone@dest.example", rec.Recipient())
	}
}

// Consolidated reports carry one per-recipient block per address.
func TestParseDSNMultipleRecipientRecords(t *testing.T) {
	part := strings.Join([]string{
		"Reporting-MTA: dns; mx.remote.test",
		"",
		"Final-Recipient: rfc822;a@dest.example",
		"Action: failed",
		"Status: 5.1.1",
		"",
		"Final-Recipient: rfc822;b@dest.example",
		"Action: delayed",
		"Status: 4.4.1",
		"",
	}, "\r\n")
	dsn, err := ParseDSN(dsnFixture(part))
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if len(dsn.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(dsn.Records))
	}
	if dsn.Records[0].Recipient() != "a@dest.example" || dsn.Records[1].Recipient() != "b@dest.example" {
		t.Fatalf("recipients = %q, %q", dsn.Records[0].Recipient(), dsn.Records[1].Recipient())
	}
	if dsn.Records[0].Action != DSNActionFailed || dsn.Records[1].Action != DSNActionDelayed {
		t.Fatalf("actions = %q, %q", dsn.Records[0].Action, dsn.Records[1].Action)
	}
}

// Anything that is not a multipart/report with a delivery-status part is
// refused with ErrNotDSN — the bounce stays stored, just unmatched.
func TestParseDSNRefusesNonReport(t *testing.T) {
	plain := []byte("From: postmaster@remote.test\r\nSubject: delivery failure (legacy)\r\n\r\nUser unknown.\r\n")
	if _, err := ParseDSN(plain); !errors.Is(err, ErrNotDSN) {
		t.Fatalf("plain message err = %v, want ErrNotDSN", err)
	}
	multipartNonDSN := []byte("From: a@b.test\r\nContent-Type: multipart/mixed; boundary=\"x\"\r\n\r\n--x\r\n\r\nhello\r\n--x--\r\n")
	if _, err := ParseDSN(multipartNonDSN); !errors.Is(err, ErrNotDSN) {
		t.Fatalf("multipart without status part err = %v, want ErrNotDSN", err)
	}
}

// One delivery-status part may not exceed the parser's size cap: the oversized
// part is refused instead of read into memory unboundedly.
func TestParseDSNSizeCapRefused(t *testing.T) {
	huge := "Reporting-MTA: dns; mx.remote.test\r\n\r\nX-Pad: " + strings.Repeat("p", maxDSNPartBytes+4096) + "\r\n"
	msg := dsnFixture(huge)
	// The fixture itself is far beyond the cap; ParseDSN must refuse the part
	// (ErrNotDSN — no usable delivery-status part) rather than read it all.
	if _, err := ParseDSN(msg); err == nil {
		t.Fatalf("oversized delivery-status part parsed, want refusal")
	} else if !errors.Is(err, ErrNotDSN) && !strings.Contains(err.Error(), "cap") {
		t.Fatalf("err = %v, want ErrNotDSN or a cap error", err)
	}
}

// Empty input is refused, not panicking.
func TestParseDSNEmptyRefused(t *testing.T) {
	if _, err := ParseDSN(nil); err == nil {
		t.Fatalf("ParseDSN(nil) = nil error, want an error")
	}
}
