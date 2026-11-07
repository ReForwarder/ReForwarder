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

package verify

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/emersion/go-msgauth/dkim"
)

// DKIM results (RFC 8601 §2.7.1 method values).
const (
	DKIMPass      = "pass"
	DKIMFail      = "fail"
	DKIMNone      = "none"
	DKIMTempError = "temperror"
	DKIMPermError = "permerror"
)

// DKIMVerdict aggregates the verification of every DKIM-Signature on a
// message into one verdict (SF-23).
type DKIMVerdict struct {
	// Result is one of pass|fail|none|temperror|permerror. pass wins over
	// everything (one valid signature is enough for DMARC); otherwise the
	// most informative failure class is kept.
	Result string
	// Domain is the d= of the signature this verdict is about — the best
	// (passing) one when any passed, else the first.
	Domain string
	// Selector is the s= of that same signature.
	Selector string
	// Reason explains a non-pass verdict.
	Reason string
}

// DKIMEvaluator verifies the raw message's DKIM signatures with the
// already-present go-msgauth verifier (SF-23). The public key lookups go
// through LookupTXT (nil uses net.LookupTXT), so tests can serve keys from a
// table.
type DKIMEvaluator struct {
	// LookupTXT resolves selector._domainkey.<domain> TXT records. When nil,
	// net.LookupTXT is used (no per-call context — go-msgauth's hook has no
	// context parameter).
	LookupTXT func(domain string) ([]string, error)
	// MaxVerifications caps how many signatures are checked (go-msgauth
	// verifies signatures in parallel; a message carrying hundreds of
	// signatures must not turn into a DoS). Zero means 5.
	MaxVerifications int
}

// Verify checks every DKIM-Signature on raw and aggregates the results.
func (e *DKIMEvaluator) Verify(ctx context.Context, raw []byte) DKIMVerdict {
	if len(raw) == 0 {
		return DKIMVerdict{Result: DKIMNone, Reason: "no message content"}
	}
	maxV := e.MaxVerifications
	if maxV <= 0 {
		maxV = 5
	}
	lookup := e.LookupTXT
	if lookup == nil {
		lookup = netLookupTXT
	}
	verifs, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{
		LookupTXT:        lookup,
		MaxVerifications: maxV,
	})
	if err != nil && len(verifs) == 0 {
		// The message could not even be scanned for signatures.
		return DKIMVerdict{Result: DKIMPermError, Reason: fmt.Sprintf("dkim scan failed: %v", err)}
	}
	if len(verifs) == 0 {
		return DKIMVerdict{Result: DKIMNone, Reason: "no DKIM-Signature"}
	}

	best := -1
	for i, v := range verifs {
		if v.Err == nil {
			best = i
			break
		}
		if best < 0 {
			best = i
		}
	}
	if best < 0 { // unreachable, but keeps the indexing honest
		return DKIMVerdict{Result: DKIMNone, Reason: "no DKIM-Signature"}
	}
	v := verifs[best]
	dom, sel := signatureIdentity(raw, v.Domain)
	if v.Err == nil {
		return DKIMVerdict{Result: DKIMPass, Domain: dom, Selector: sel}
	}
	return DKIMVerdict{Result: classifyDKIMError(v.Err), Domain: dom, Selector: sel, Reason: v.Err.Error()}
}

// classifyDKIMError maps go-msgauth's error classes onto RFC 8601 results.
func classifyDKIMError(err error) string {
	switch {
	case dkim.IsTempFail(err):
		return DKIMTempError
	case dkim.IsPermFail(err):
		return DKIMPermError
	default:
		return DKIMFail
	}
}

// netLookupTXT adapts net.LookupTXT to go-msgauth's hook signature.
func netLookupTXT(domain string) ([]string, error) {
	return net.LookupTXT(domain)
}

// signatureIdentity recovers the s= selector for a signature with the given
// d= domain by scanning the message's DKIM-Signature headers. go-msgauth's
// Verification carries the domain but not the selector, and the A-R header
// conventionally reports both.
func signatureIdentity(raw []byte, domain string) (string, string) {
	head := raw
	if i := bytes.Index(head, []byte("\r\n\r\n")); i >= 0 {
		head = head[:i]
	} else if i := bytes.Index(head, []byte("\n\n")); i >= 0 {
		head = head[:i]
	}
	for _, line := range splitHeaderFields(head) {
		if !strings.HasPrefix(strings.ToLower(line), "dkim-signature:") {
			continue
		}
		d := headerTagValue(line, "d")
		if strings.EqualFold(d, domain) {
			return d, headerTagValue(line, "s")
		}
	}
	return domain, ""
}

// splitHeaderFields splits a header block into unfolded logical lines.
func splitHeaderFields(head []byte) []string {
	var out []string
	var cur strings.Builder
	for _, rawLine := range strings.Split(string(head), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			cur.WriteString(" " + strings.TrimSpace(line))
			continue
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// headerTagValue extracts one tag=value from a DKIM-Signature style field.
func headerTagValue(field, tag string) string {
	i := strings.Index(field, ":")
	if i < 0 {
		return ""
	}
	for _, part := range strings.Split(field[i+1:], ";") {
		part = strings.TrimSpace(part)
		if eq := strings.IndexByte(part, '='); eq > 0 {
			if strings.EqualFold(strings.TrimSpace(part[:eq]), tag) {
				return strings.TrimSpace(part[eq+1:])
			}
		}
	}
	return ""
}
