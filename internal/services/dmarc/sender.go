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

package dmarc

import (
	"context"
	"fmt"
	"strings"

	"github.com/goposta/posta/internal/services/inbound/verify"
)

// Sender delivers one generated outbound aggregate report. The production
// adapter (built in cmd wiring) enqueues the report through the
// system-forward pipeline with system provenance — queued, signed, retried
// with the standard backoff, never through the tenant send path; tests
// inject fakes.
type Sender interface {
	SendReport(ctx context.Context, req *OutboundReportRequest) error
}

// SenderFunc adapts a function onto Sender.
type SenderFunc func(ctx context.Context, req *OutboundReportRequest) error

// SendReport implements Sender.
func (f SenderFunc) SendReport(ctx context.Context, req *OutboundReportRequest) error {
	if f == nil {
		return fmt.Errorf("dmarc: no report sender wired")
	}
	return f(ctx, req)
}

// MailtoRuas extracts the mailto rua addresses from a published DMARC
// policy's rua= list (RFC 7489 §6.5): a "mailto:<address>" URI yields the
// address; size/interval extension directives after the '!' separator
// ("mailto:<address>!<size>!<interval>") are ignored (this deployment does
// not honor report-size or per-URI interval limits); non-mailto URIs
// (https:) are skipped — the system-forward dispatch is mail-only.
// Addresses are returned deduplicated, in first-seen order.
func MailtoRuas(policy *verify.DMARCPolicy) []string {
	if policy == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, uri := range policy.RUA {
		uri = strings.TrimSpace(uri)
		lower := strings.ToLower(uri)
		if !strings.HasPrefix(lower, "mailto:") {
			continue // https: and other schemes are not dispatchable by mail
		}
		addr := uri[len("mailto:"):]
		// RFC 7489 §6.5: extensions ride after the '!' separator
		// ("<addr>!<size>!<interval>"); everything from the first '!' on is
		// an extension directive, not part of the address.
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
	return out
}
