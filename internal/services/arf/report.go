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
	"context"
	"fmt"
	"strings"
	"time"
)

// maxReturnedMessageBytes bounds the report's third part (the offending
// message): the DSN precedent's bounded returned part (SF-10).
const maxReturnedMessageBytes = 64 * 1024

// MessageSource resolves one offending message's stored body for the report's
// third part (RFC 5965 §6.3: the message part is a SHOULD). The production
// wiring fetches the stored inbound row; tests inject stubs. A nil or
// erroring source omits the part.
type MessageSource func(ctx context.Context, inboundID uint) ([]byte, error)

// FeedbackReport holds the RFC 5965 §6.1 machine-readable fields.
type FeedbackReport struct {
	FeedbackType   string // "auth-failure" (RFC 6591 §3.2)
	AuthVersion    string // "1"
	UserAgent      string // e.g. "Posta/<version>"
	ReportedDomain string // the domain whose authentication failed
	SourceIP       string
	ArrivalDate    time.Time
}

// render renders the machine-readable part (the RFC 5965 §6.1 field set).
func (f FeedbackReport) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Feedback-Type: %s\r\n", f.FeedbackType)
	fmt.Fprintf(&b, "Auth-Version: %s\r\n", f.AuthVersion)
	fmt.Fprintf(&b, "User-Agent: %s\r\n", f.UserAgent)
	fmt.Fprintf(&b, "Reported-Domain: %s\r\n", f.ReportedDomain)
	if f.SourceIP != "" {
		fmt.Fprintf(&b, "Source-IP: %s\r\n", f.SourceIP)
	}
	if !f.ArrivalDate.IsZero() {
		fmt.Fprintf(&b, "Arrival-Date: %s\r\n", f.ArrivalDate.UTC().Format(time.RFC1123Z))
	}
	return b.String()
}

// BuildAuthFailureReport renders the complete RFC 5965 §6 feedback report:
// a multipart/report (report-type=auth-failure) with the machine-readable
// part, the message/authentication-results part (the stored RFC 8601 header
// this deployment injected), and — when the message source provides it — the
// offending message (RFC 5965 §6.3's SHOULD, bounded). The document is sent
// verbatim as the message body by the system-notification relay.
func BuildAuthFailureReport(orgName, version string, row FailureRow, arHeader string, message []byte, generatedAt time.Time) string {
	feedback := FeedbackReport{
		FeedbackType:   "auth-failure",
		AuthVersion:    "1",
		UserAgent:      fmt.Sprintf("%s/%s", orgName, version),
		ReportedDomain: row.Domain,
		SourceIP:       row.ClientIP,
		ArrivalDate:    row.CreatedAt,
	}
	if arHeader == "" {
		// The stored Authentication-Results header is the authoritative
		// record; a minimal fallback keeps the report parseable when the
		// header was not persisted.
		arHeader = fmt.Sprintf("authserv-id: %s;\r\n\tdkim=%s;\r\n\tspf=%s",
			orgName, "fail", "fail")
	}
	arHeader = strings.TrimRight(arHeader, "\r\n")

	boundary := fmt.Sprintf("arf-%d-%d", generatedAt.Unix(), row.InboundID)
	var b strings.Builder
	fmt.Fprintf(&b, "This is an authentication-failure report (RFC 5965/6591).\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/report; report-type=auth-failure;\r\n\tboundary=\"%s\"\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=\"utf-8\"\r\n\r\n")
	fmt.Fprintf(&b, "This is an authentication-failure report for domain %s.\r\n", row.Domain)
	fmt.Fprintf(&b, "The message arrived at %s and failed %s authentication.\r\n", orgName, row.Family)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: message/feedback-report\r\n\r\n")
	b.WriteString(feedback.render())
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: message/authentication-results\r\n\r\n")
	b.WriteString(arHeader)
	b.WriteString("\r\n")
	if len(message) > 0 {
		fmt.Fprintf(&b, "--%s\r\n", boundary)
		fmt.Fprintf(&b, "Content-Type: message/rfc822\r\n\r\n")
		msg := message
		if len(msg) > maxReturnedMessageBytes {
			msg = msg[:maxReturnedMessageBytes]
		}
		b.Write(msg)
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.String()
}

// OutboundReportRequest is one generated report the sender must deliver.
type OutboundReportRequest struct {
	// ReportID is the deterministic per-message id (<inbound-id>-<family>@<org>).
	ReportID string
	// ReportedDomain is the domain the report is about.
	ReportedDomain string
	// Family is the failing mechanism ("dkim" | "spf").
	Family string
	// InboundID is the offending message's inbound row (attribution).
	InboundID uint
	// UserID / WorkspaceID attribute the received message to its tenant.
	UserID      uint
	WorkspaceID *uint
	// Document is the complete multipart/report body (sent verbatim).
	Document string
	// Subject is the report mail's subject line.
	Subject string
	// To holds the abuse-reporting addresses discovered from the reported
	// domain's live _arf DNS record.
	To []string
	// From is the generator address (auth-failure-reports@<org domain>).
	From string
}

// OutboundReportID is the deterministic per-message report id, so a re-run
// cannot double-send under a new identity.
func OutboundReportID(inboundID uint, family, orgName string) string {
	return fmt.Sprintf("%d-%s@%s", inboundID, family, orgName)
}

// MailtoAddresses extracts the mailto addresses from a domain's published
// _arf TXT record values (RFC 6591 §4): values carry mailto URIs separated
// by whitespace, commas or semicolons; '!'-extensions are dropped; non-mailto
// URIs are skipped (the dispatch is mail-only). Addresses are returned
// deduplicated, in first-seen order.
func MailtoAddresses(txts []string) []string {
	seen := map[string]bool{}
	var out []string
	split := func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ',', ';':
			return true
		}
		return false
	}
	for _, txt := range txts {
		for _, tok := range strings.FieldsFunc(txt, split) {
			tok = strings.TrimSpace(tok)
			if !strings.HasPrefix(strings.ToLower(tok), "mailto:") {
				continue
			}
			addr := tok[len("mailto:"):]
			if bang := strings.IndexByte(addr, '!'); bang >= 0 {
				addr = addr[:bang]
			}
			addr = strings.TrimSpace(addr)
			if addr == "" || seen[strings.ToLower(addr)] {
				continue
			}
			seen[strings.ToLower(addr)] = true
			out = append(out, addr)
		}
	}
	return out
}
