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
	"net/netip"
	"strings"
)

// Results is the combined verdict of all three engines for one message.
type Results struct {
	SPF   SPFVerdict
	DKIM  DKIMVerdict
	DMARC DMARCVerdict
	// EnvelopeFromDomain is the RFC 5321 MAIL FROM domain (SPF identity).
	EnvelopeFromDomain string
	// FromDomain is the RFC 5322.From domain (DMARC identity).
	FromDomain string
	// ClientIP / EnvelopeFrom echo the evaluated inputs for the verdict row.
	ClientIP     string
	EnvelopeFrom string
}

// Input is everything the engines need for one inbound message.
type Input struct {
	// ClientIP is the connecting client's address; empty means unknown (the
	// SPF evaluation then honestly yields none).
	ClientIP string
	// EnvelopeFrom is the RFC 5321 MAIL FROM address (may be empty).
	EnvelopeFrom string
	// Raw is the full RFC 5322 message (DKIM needs the raw bytes).
	Raw []byte
	// SampleKey is the stable per-message identifier driving the DMARC pct=
	// sampling (the inbound UUID).
	SampleKey string
}

// Evaluate runs SPF, DKIM and DMARC for one inbound message. DMARC needs the
// RFC 5322.From domain, which is parsed from Raw; when Raw carries no usable
// From header, DMARC is none.
func Evaluate(ctx context.Context, res Resolver, in Input) Results {
	out := Results{
		SPF: SPFVerdict{Result: SPFNone, Reason: "not evaluated"},
	}
	out.ClientIP = in.ClientIP
	out.EnvelopeFrom = in.EnvelopeFrom
	spfe := &SPFEvaluator{Resolver: res}
	ip := parseIP(in.ClientIP)
	envDomain := domainOf(in.EnvelopeFrom)
	out.EnvelopeFromDomain = envDomain
	if envDomain == "" {
		out.SPF = SPFVerdict{Result: SPFNone, Reason: "no envelope sender"}
	} else {
		out.SPF = spfe.CheckHost(ctx, ip, envDomain, in.EnvelopeFrom)
	}

	dkime := &DKIMEvaluator{LookupTXT: dkimTXTLookup(ctx, res)}
	out.DKIM = dkime.Verify(ctx, in.Raw)

	out.FromDomain = headerFromDomain(in.Raw)
	dmarce := &DMARCEvaluator{Resolver: res}
	out.DMARC = dmarce.Evaluate(ctx, out.FromDomain, out.SPF, out.DKIM, in.SampleKey)
	return out
}

// parseIP parses a client address, returning an invalid Addr on failure.
func parseIP(s string) netip.Addr {
	s = strings.TrimSpace(s)
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host // tolerate "ip:port" style inputs
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

// dkimTXTLookup adapts the package Resolver to go-msgauth's no-context hook;
// the evaluation context still cancels outstanding lookups.
func dkimTXTLookup(ctx context.Context, res Resolver) func(string) ([]string, error) {
	return func(domain string) ([]string, error) {
		return res.LookupTXT(ctx, domain)
	}
}

// headerFromDomain extracts the RFC 5322.From domain from the raw message
// (first address of the From header). Empty when unusable.
func headerFromDomain(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	head := raw
	if i := indexHeaderEnd(head); i >= 0 {
		head = head[:i]
	}
	for _, line := range splitHeaderFields(head) {
		if strings.HasPrefix(strings.ToLower(line), "from:") {
			return domainOf(extractFirstAddress(line[len("from:"):]))
		}
	}
	return ""
}

// indexHeaderEnd finds the blank line separating header block and body.
func indexHeaderEnd(raw []byte) int {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return i
	}
	return bytes.Index(raw, []byte("\n\n"))
}

// extractFirstAddress pulls the addr-spec out of the first address in a
// From-style header value, tolerating display names and groups.
func extractFirstAddress(v string) string {
	// Prefer the address inside <>, else the bare token after the colon.
	if i := strings.IndexByte(v, '<'); i >= 0 {
		if j := strings.IndexByte(v[i:], '>'); j > 0 {
			return v[i+1 : i+j]
		}
	}
	v = strings.TrimSpace(v)
	if i := strings.IndexAny(v, ",;"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// AuthenticationResults composes an RFC 8601 Authentication-Results header
// VALUE (without the "Authentication-Results:" field name) reporting the
// three engines under authserv-id (SF-49 slice):
//
//	<authserv-id>; spf=<r> smtp.mailfrom=<domain>; dkim=<r> header.d=<d>
//	header.s=<s>; dmarc=<r> header.from=<domain> policy.p=<p> policy.pct=<n>
func AuthenticationResults(authservID string, r Results) string {
	var b strings.Builder
	b.WriteString(sanitizeToken(authservID, "localhost"))
	b.WriteString("; spf=")
	b.WriteString(r.SPF.Result)
	if r.EnvelopeFromDomain != "" {
		b.WriteString(" smtp.mailfrom=" + sanitizeToken(r.EnvelopeFromDomain, "unknown"))
	}
	if r.SPF.Reason != "" {
		b.WriteString(" reason=" + quoteARValue(r.SPF.Reason))
	}
	b.WriteString("; dkim=")
	b.WriteString(r.DKIM.Result)
	if r.DKIM.Domain != "" {
		b.WriteString(" header.d=" + sanitizeToken(r.DKIM.Domain, "unknown"))
		if r.DKIM.Selector != "" {
			b.WriteString(" header.s=" + sanitizeToken(r.DKIM.Selector, "unknown"))
		}
	}
	if r.DKIM.Reason != "" {
		b.WriteString(" reason=" + quoteARValue(r.DKIM.Reason))
	}
	b.WriteString("; dmarc=")
	b.WriteString(r.DMARC.Result)
	if r.FromDomain != "" {
		b.WriteString(" header.from=" + sanitizeToken(r.FromDomain, "unknown"))
	}
	if r.DMARC.Policy != "" {
		b.WriteString(" policy.p=" + sanitizeToken(r.DMARC.Policy, "none"))
	}
	if r.DMARC.PolicyDomain != "" && r.DMARC.PCT > 0 && r.DMARC.PCT != 100 {
		fmt.Fprintf(&b, " policy.pct=%d", r.DMARC.PCT)
	}
	if r.DMARC.Reason != "" {
		b.WriteString(" reason=" + quoteARValue(r.DMARC.Reason))
	}
	return b.String()
}

// sanitizeToken strips characters that would break the RFC 8601 value syntax
// from a token interpolated into the header (domains, selectors, policies).
func sanitizeToken(v, fallback string) string {
	var b strings.Builder
	for _, c := range v {
		switch {
		case c > ' ' && c < 0x7f && c != ';' && c != '=' && c != '(' && c != ')' && c != '"' && c != ',':
			b.WriteRune(c)
		}
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}

// quoteARValue renders a reason as a quoted RFC 8601 string.
func quoteARValue(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range v {
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(c)
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		default:
			if c >= ' ' && c < 0x7f {
				b.WriteRune(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
