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

// Package verify implements message-level inbound authentication for the
// SMTP receiver: SPF (RFC 7208), DKIM (RFC 6376, via go-msgauth) and DMARC
// (RFC 7489) evaluation plus RFC 8601 Authentication-Results composition.
// This is the SF-22/SF-23/SF-24 engine set; the verdicts are annotations for
// the ADR-002 routing engine (ADR-002 Phase-3 slice) — nothing here rejects
// or bounces mail.
package verify

import (
	"context"
	"errors"
	"net"
)

// Resolver is the DNS surface the evaluators need. *NetResolver wraps the
// Go default resolver; tests substitute a table-driven fake.
type Resolver interface {
	// LookupTXT returns the TXT records for name.
	LookupTXT(ctx context.Context, name string) ([]string, error)
	// LookupIP returns the A and AAAA addresses for name.
	LookupIP(ctx context.Context, name string) ([]net.IP, error)
	// LookupAddr returns the PTR names for an IP address.
	LookupAddr(ctx context.Context, addr string) ([]string, error)
	// LookupMX returns the MX hosts for name, lowest preference first.
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// NetResolver resolves through the Go default resolver (the same pattern as
// internal/services/domain/verifier.go and internal/services/email/dkim_signer.go).
type NetResolver struct{}

// LookupTXT implements Resolver.
func (NetResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, name)
}

// LookupIP implements Resolver. The "ip" network asks for both address
// families in one query path.
func (NetResolver) LookupIP(ctx context.Context, name string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", name)
}

// LookupAddr implements Resolver.
func (NetResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	return net.DefaultResolver.LookupAddr(ctx, addr)
}

// LookupMX implements Resolver.
func (NetResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return net.DefaultResolver.LookupMX(ctx, name)
}

// ErrNotFound reports a definitive NXDOMAIN-style negative answer. The
// evaluators distinguish it from transient resolver trouble (temperror) and
// map it to the none / no-record result instead.
func ErrNotFound(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}
	return false
}
