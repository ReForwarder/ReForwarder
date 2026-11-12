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

package mailtrace

import (
	"crypto/tls"
	"strings"
	"testing"
	"time"
)

var traceAt = time.Date(2026, 11, 12, 17, 30, 0, 0, time.UTC)

func TestPrependNewestHopFirst(t *testing.T) {
	msg := []byte("Received: from outer hop\r\nSubject: hi\r\n\r\nbody\r\n")
	out := Prepend(msg, "Received: from inner hop\r\n")
	got := string(out)
	if !strings.HasPrefix(got, "Received: from inner hop\r\n") {
		t.Fatalf("prepended header must come first: %q", got[:min(len(got), 60)])
	}
	// Deterministic prepend, not append: the original first hop follows.
	if !strings.Contains(got, "\r\nReceived: from outer hop\r\n") {
		t.Fatalf("original hop must survive below the new one: %q", got)
	}
	if !strings.HasSuffix(got, "\r\n\r\nbody\r\n") {
		t.Fatalf("body must be untouched: %q", got)
	}
}

func TestPrependHeaderlessMessageStaysValid(t *testing.T) {
	// A body-only message (starts with a blank line): the trace header
	// becomes the header block and the blank line still separates the body.
	out := string(Prepend([]byte("\r\nraw body\r\n"), "Received: by posta;\r\n\tdate\r\n"))
	if !strings.HasPrefix(out, "Received: by posta;\r\n\tdate\r\n\r\nraw body\r\n") {
		t.Fatalf("headerless message mangled: %q", out)
	}
}

func TestPrependEmptyHeaderNoOp(t *testing.T) {
	msg := []byte("Subject: x\r\n\r\n")
	if got := Prepend(msg, ""); string(got) != string(msg) {
		t.Fatalf("empty header must not modify the message")
	}
}

func TestIngestReceivedShape(t *testing.T) {
	h := IngestReceived("client.example.net", "192.0.2.10", "mx.posta.example", 0,
		[]string{"user@posta.example"}, traceAt)
	for _, want := range []string{
		"Received: from client.example.net ([192.0.2.10])",
		"by mx.posta.example (Posta) with ESMTP for <user@posta.example>",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("ingest header missing %q; got %q", want, h)
		}
	}
	if !strings.Contains(h, "12 Nov 2026") {
		t.Errorf("header must carry an RFC 5322 date: %q", h)
	}
	if !strings.HasSuffix(h, "\r\n") {
		t.Errorf("header block must end with CRLF: %q", h)
	}
}

func TestIngestReceivedTLSStateNamesProtocol(t *testing.T) {
	h := IngestReceived("client.example.net", "192.0.2.10", "mx.posta.example", tls.VersionTLS13,
		[]string{"a@x.example", "b@x.example"}, traceAt)
	if !strings.Contains(h, "with ESMTPS (TLSv1.3)") {
		t.Fatalf("TLS sessions must be marked ESMTPS with the version: %q", h)
	}
	if strings.Contains(h, " for <") {
		t.Fatalf("`for` clause must be omitted with multiple recipients: %q", h)
	}
}

func TestIngestReceivedSanitizesInjection(t *testing.T) {
	evil := "evil.example\r\nBcc: victim@example.com\r\nX-Injected: yes"
	h := IngestReceived(evil, "192.0.2.10", "mx.posta.example", 0, []string{"a@b.c"}, traceAt)
	// The control bytes are stripped, so no NEW header can start: the
	// injected names may survive as inline text but never as line heads.
	if strings.Contains(h, "\r\nBcc") || strings.Contains(h, "\r\nX-Injected") {
		t.Fatalf("client-controlled HELO must not inject headers: %q", h)
	}
	// One trace header = exactly two fold lines + the terminator CRLF.
	if got := strings.Count(h, "\r\n"); got != 3 {
		t.Fatalf("trace header must fold exactly two lines: %d CRLFs in %q", got, h)
	}
}

func TestOutboundReceivedShape(t *testing.T) {
	h := OutboundReceived("ehlo.posta.example", "mx1.remote.example", "42.3", traceAt)
	for _, want := range []string{
		"Received: by ehlo.posta.example (Posta, id 42.3)",
		"for delivery to mx1.remote.example",
		"12 Nov 2026",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("outbound header missing %q; got %q", want, h)
		}
	}
}

func TestOutboundReceivedSanitizesHost(t *testing.T) {
	h := OutboundReceived("posta", "mx1.example\r\nX-Bad: 1", "42.3", traceAt)
	if strings.Contains(h, "\r\nX-Bad") {
		t.Fatalf("remote host token must be sanitized: %q", h)
	}
}

