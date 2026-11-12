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
	"strings"
	"testing"

	"github.com/goposta/posta/internal/mailtrace"
)

// SF-13: the receiver stamps an ingest Received: trace header from the live
// session state (HELO, peer IP, TLS) and the receiving hostname; the header
// is prepended to the stored message.
func TestSessionReceivedTraceHeaderShape(t *testing.T) {
	b := &Backend{hostname: "mx.posta.example"}
	s := &session{
		backend: b,
		remote:  "192.0.2.10:49981",
		helo:    "client.example.net",
		// conn nil ⇒ plaintext session in this unit test; the TLS branch is
		// exercised through mailtrace.IngestReceived's own tests.
	}
	h := s.withReceivedHeader([]string{"user@posta.example"})

	for _, want := range []string{
		"Received: from client.example.net ([192.0.2.10])",
		"by mx.posta.example (Posta) with ESMTP for <user@posta.example>",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("ingest trace header missing %q; got %q", want, h)
		}
	}
	if strings.Contains(h, "ESMTPS") {
		t.Errorf("a session without a TLS connection must not claim ESMTPS: %q", h)
	}
}

// The bytes handed to durable ingest start with the trace header, and the
// original message survives verbatim below it (golden composition).
func TestSessionReceivedHeaderPrependGolden(t *testing.T) {
	b := &Backend{hostname: "mx.posta.example"}
	s := &session{backend: b, remote: "198.51.100.7:52000", helo: "sender.example.org"}

	raw := []byte("Received: from upstream.example (hop zero)\r\n" +
		"Message-ID: <orig@sender.example>\r\n" +
		"Subject: hello\r\n" +
		"\r\n" +
		"original body\r\n")

	stored := string(mailtracePrepend(t, s, raw))
	if !strings.HasPrefix(stored, "Received: from sender.example.org ([198.51.100.7])") {
		t.Fatalf("stored message must start with our hop: %q", stored[:min(len(stored), 100)])
	}
	if !strings.Contains(stored, "\r\nReceived: from upstream.example (hop zero)") {
		t.Fatalf("the upstream hop must survive below ours: %q", stored)
	}
	if !strings.Contains(stored, "Subject: hello\r\n\r\noriginal body\r\n") {
		t.Fatalf("original headers and body must be untouched: %q", stored)
	}
	if !strings.Contains(stored, "by mx.posta.example (Posta) with ESMTP") {
		t.Fatalf("our by-clause must be present: %q", stored)
	}
}

func mailtracePrepend(t *testing.T, s *session, raw []byte) []byte {
	t.Helper()
	return mailtrace.Prepend(raw, s.withReceivedHeader([]string{"user@posta.example"}))
}

