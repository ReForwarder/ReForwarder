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

package email

import (
	"strings"
	"testing"
)

// A CR or LF in the Subject or in a custom header name/value would inject an
// arbitrary header line into the outbound message (e.g. a smuggled Bcc). The
// send and validate endpoints must reject such payloads (R-24).
func TestValidateSendHeadersRejectsInjection(t *testing.T) {
	payloads := []string{
		"hello\r\nBcc: victim@evil.test",
		"hello\nBcc: victim@evil.test",
		"hello\rBcc: victim@evil.test",
		"hello\x00Bcc: victim@evil.test",
		"\r",
		"\n",
	}
	for _, subject := range payloads {
		if err := validateSendHeaders(subject, nil); err == nil {
			t.Errorf("validateSendHeaders(subject=%q) = nil, want error", subject)
		}
	}

	evilHeaders := []map[string]string{
		{"X-Innocent": "ok\r\nBcc: victim@evil.test"},
		{"X-Innocent": "ok\nBcc: victim@evil.test"},
		{"X-Innocent\x00": "ok"},
		{"X-Innocent\r\nX-Smuggled": "1"},
	}
	for _, headers := range evilHeaders {
		if err := validateSendHeaders("clean subject", headers); err == nil {
			t.Errorf("validateSendHeaders(headers=%v) = nil, want error", headers)
		}
	}

	clean := map[string]string{
		"X-Custom":   "fine",
		"X-Unicode":  "café — 東京",
		"X-Tab\tOrd": "tab in a name is not a line break",
	}
	if err := validateSendHeaders("clean subject", clean); err != nil {
		t.Errorf("validateSendHeaders(clean) = %v, want nil", err)
	}
	if err := validateSendHeaders("clean subject", nil); err != nil {
		t.Errorf("validateSendHeaders(nil headers) = %v, want nil", err)
	}
}

// filterCustomHeaders is the last guard in front of buildMessage: entries whose
// name or value carries CR/LF/NUL must be dropped even when the caller skipped
// validateSendHeaders.
func TestFilterCustomHeadersDropsInjection(t *testing.T) {
	got := filterCustomHeaders(map[string]string{
		"X-Keep":                   "kept",
		"X-Injected-Value":         "a\r\nBcc: victim@evil.test",
		"X-Injected\r\nX-Smuggled": "1",
		"X-Nul-Name\x00":           "v",
	})
	if _, ok := got["X-Keep"]; !ok {
		t.Error("clean header dropped")
	}
	for name := range got {
		if containsHeaderInjection(name) {
			t.Errorf("injected header name survived: %q", name)
		}
		if containsHeaderInjection(got[name]) {
			t.Errorf("injected header value survived under %q", name)
		}
	}
}

// buildMessage is the final boundary. Even for internal paths (campaigns) that
// bypass validateSendHeaders, an injected Subject or header must never produce
// an additional header line — it renders as literal text instead (R-24).
func TestBuildMessageRendersInjectedPayloadsAsText(t *testing.T) {
	injected := "Hello\r\nBcc: victim@evil.test"

	msg := string(buildMessage("sender@example.test",
		[]string{"rcpt@example.test"},
		injected,
		"<p>body</p>", "body", nil,
		map[string]string{"X-Note": "see\r\nBcc: victim@evil.test"},
		"", "", false))

	// Exactly one Subject header, on a single physical line, with the payload
	// rendered harmlessly (encoded or space-collapsed) — never a raw break.
	if got := strings.Count(msg, "Subject:"); got != 1 {
		t.Fatalf("Subject header count = %d, want 1; message:\n%s", got, msg)
	}
	if strings.Contains(msg, "\r\nBcc:") {
		t.Fatalf("injected Bcc header line leaked into message:\n%s", msg)
	}
	// The custom header value must not start a new header either.
	if strings.Contains(msg, "X-Note: see\r\nBcc:") {
		t.Fatalf("custom header value injected a header line:\n%s", msg)
	}
	// The message still parses as a header block: the first empty line must
	// come after all headers, i.e. the injected text never ends the block early.
	headerBlock := msg[:strings.Index(msg, "\r\n\r\n")]
	for _, line := range strings.Split(headerBlock, "\r\n") {
		if line != "" && !strings.Contains(line, ":") {
			t.Fatalf("malformed header line %q in:\n%s", line, msg)
		}
	}
}
