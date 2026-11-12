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

package inbound

import (
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/email"
)

// SF-10 round-trip contract: the RFC 3464 builder (email package) produces a
// message the receiver's match-back parser reads losslessly — the two sides
// of the bounce conversation agree on the vocabulary.
func TestBuildDSNParsesBackThroughMatchBack(t *testing.T) {
	original := "Received: from elsewhere\r\nMessage-ID: <orig@example.org>\r\nSubject: hi\r\n\r\noriginal body\r\n"
	raw := email.BuildDSN(&email.DSNRequest{
		ReportingMTA:      "mx.posta.example",
		Sender:            "alice@example.org",
		OriginalSubject:   "hi",
		OriginalMessageID: "<orig@example.org>",
		ArrivalDate:       time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
		Recipients: []email.DSNRecipient{
			{Recipient: "dead@example.net", Action: "failed", Status: "5.1.1",
				Diagnostic: "550 5.1.1 user unknown", RemoteMTA: "mx.example.net"},
		},
		OriginalRaw: []byte(original),
	})
	if raw == nil {
		t.Fatal("BuildDSN returned nil")
	}

	// Shape: multipart/report with the delivery-status part, the loop guards,
	// and the MAILER-DAEMON identity on the sender's own domain.
	s := string(raw)
	for _, want := range []string{
		"Content-Type: multipart/report; report-type=delivery-status;",
		"From: MAILER-DAEMON@example.org",
		"To: <alice@example.org>",
		"Auto-Submitted: auto-replied",
		"message/delivery-status",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("DSN missing %q", want)
		}
	}

	// The match-back parser reads it back.
	dsn, err := ParseDSN(raw)
	if err != nil {
		t.Fatalf("ParseDSN(BuildDSN(...)): %v", err)
	}
	if dsn.ReportingMTA != "mx.posta.example" {
		t.Errorf("ReportingMTA = %q, want mx.posta.example", dsn.ReportingMTA)
	}
	if len(dsn.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(dsn.Records))
	}
	rec := dsn.Records[0]
	if rec.Action != DSNActionFailed {
		t.Errorf("action = %q, want failed", rec.Action)
	}
	if rec.Status != "5.1.1" {
		t.Errorf("status = %q, want 5.1.1", rec.Status)
	}
	if rec.Recipient() != "dead@example.net" {
		t.Errorf("recipient = %q, want dead@example.net", rec.Recipient())
	}
	if rec.DiagnosticCode == "" || !strings.Contains(rec.DiagnosticCode, "user unknown") {
		t.Errorf("diagnostic = %q, want the remote text", rec.DiagnosticCode)
	}
	if rec.RemoteMTA != "mx.example.net" {
		t.Errorf("remote MTA = %q, want mx.example.net", rec.RemoteMTA)
	}
}

func TestBuildDSNSanitizesHeaderInjectionInAddresses(t *testing.T) {
	raw := email.BuildDSN(&email.DSNRequest{
		Sender: "victim@example.org",
		Recipients: []email.DSNRecipient{
			{Recipient: "a@example.net\r\nBcc: hacker@evil.example", Action: "failed"},
		},
	})
	s := string(raw)
	if strings.Contains(s, "\r\nBcc") {
		t.Fatalf("recipient address must not inject headers: %q", s)
	}
}

func TestBuildDSNBoundedReturnedMessage(t *testing.T) {
	huge := strings.Repeat("X", 2<<20)
	raw := email.BuildDSN(&email.DSNRequest{
		Sender:      "a@b.c",
		Recipients:  []email.DSNRecipient{{Recipient: "d@e.f", Action: "failed"}},
		OriginalRaw: []byte("Subject: x\r\n\r\n" + huge),
	})
	if len(raw) > (2<<20)+1<<16 {
		t.Fatalf("returned part must be bounded; got %d bytes", len(raw))
	}
}
