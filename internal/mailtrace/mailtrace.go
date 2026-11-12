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

// Package mailtrace builds RFC 5321 §4.4 Received: trace headers and
// prepends them to raw messages (SF-13). Both hop types — receiver ingest
// (client IP, HELO, TLS state) and outbound direct-to-MX delivery (MX host,
// attempt ordinal as queue id) — use the same deterministic prepend so the
// newest hop is always the first header.
package mailtrace

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"
)

// Prepend inserts header at the very front of raw and returns the new
// message. header must be a complete header block ending with CRLF. Inserting
// at the front keeps hop ordering deterministic (newest first) and never
// invalidates signatures over the original content: DKIM and S/MIME sign the
// original bytes, which are untouched below the inserted header. A message
// that begins with a blank line (no headers) still stays valid — the trace
// header becomes the header block, followed by the original blank line and
// body.
func Prepend(raw []byte, header string) []byte {
	if header == "" {
		return raw
	}
	out := make([]byte, 0, len(raw)+len(header))
	out = append(out, header...)
	out = append(out, raw...)
	return out
}

// tlsName maps the negotiated TLS version onto the trace name; 0 means no TLS.
func tlsName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLSv1"
	case tls.VersionTLS11:
		return "TLSv1.1"
	case tls.VersionTLS12:
		return "TLSv1.2"
	case tls.VersionTLS13:
		return "TLSv1.3"
	default:
		return ""
	}
}

// sanitizeToken strips control characters (CR, LF, NUL and other C0 bytes)
// from client-controlled tokens so a hostile HELO name or recipient cannot
// inject additional headers, and caps the length.
func sanitizeToken(s string, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// IngestReceived builds the trace header for a message accepted by the
// inbound receiver (SF-13). helo is the client's EHLO argument, clientIP the
// session peer address, byHost our receiving hostname, tlsVersion the
// negotiated version (0 = plaintext). Exactly one recipient yields a `for`
// clause; the field is omitted otherwise (RFC 5321: at most one).
func IngestReceived(helo, clientIP, byHost string, tlsVersion uint16, recipients []string, at time.Time) string {
	helo = sanitizeToken(helo, 200)
	if helo == "" {
		helo = "unknown"
	}
	byHost = sanitizeToken(byHost, 200)
	if byHost == "" {
		byHost = "posta"
	}
	proto := "ESMTP"
	if tlsVersion != 0 {
		proto = "ESMTPS (" + tlsName(tlsVersion) + ")"
	}
	line := fmt.Sprintf("Received: from %s ([%s])\r\n\tby %s (Posta) with %s",
		helo, sanitizeToken(clientIP, 64), byHost, proto)
	if len(recipients) == 1 {
		line += " for <" + sanitizeToken(recipients[0], 320) + ">"
	}
	line += ";\r\n\t" + at.Format(time.RFC1123Z) + "\r\n"
	return line
}

// OutboundReceived builds the trace header prepended when the MTA hands a
// locally-queued message to a remote MX host (SF-13). queueID identifies the
// delivery attempt (email id + attempt ordinal); mxHost is the host the
// session dialed.
func OutboundReceived(fromHost, mxHost, queueID string, at time.Time) string {
	fromHost = sanitizeToken(fromHost, 200)
	if fromHost == "" {
		fromHost = "posta"
	}
	mxHost = sanitizeToken(mxHost, 255)
	queueID = sanitizeToken(queueID, 64)
	line := fmt.Sprintf("Received: by %s (Posta, id %s) with outbound ESMTP\r\n\tfor delivery to %s;\r\n\t%s\r\n",
		fromHost, queueID, mxHost, at.Format(time.RFC1123Z))
	return line
}
