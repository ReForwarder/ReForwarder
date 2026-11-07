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
	"context"
	"net"
	"strings"
)

// iprev (SF-32 partial, ADR-002 decision 6): forward-confirmed reverse DNS
// for the connecting client IP. The procedure (RFC 8601's iprev method
// semantics): look up the PTR names of the address, then forward-resolve
// each name — pass when any forward answer matches the connecting IP.
// ANNOTATION ONLY: the verdict is guidance for humans and the routing
// engine's audit trail; nothing here rejects or scores mail.

const (
	IPRevPass      = "pass"
	IPRevFail      = "fail"
	IPRevNone      = "none"
	IPRevTempError = "temperror"
	IPRevPermError = "permerror"
)

// IPRevVerdict is the forward-confirmed reverse-DNS result for one
// connection.
type IPRevVerdict struct {
	// Result is pass (PTR forward-confirms), fail (PTR exists but no
	// forward confirmation), none (no PTR record / no client IP),
	// temperror or permerror.
	Result string
	// PTR is the first PTR name that forward-confirmed ("" otherwise).
	PTR string
	// Reason explains a non-pass verdict.
	Reason string
}

// IPRevEvaluator checks forward-confirmed reverse DNS. Resolver serves the
// PTR and address lookups; nil uses the Go default resolver. MaxPTRs bounds
// how many PTR names are forward-checked (zero means 10).
type IPRevEvaluator struct {
	Resolver Resolver
	MaxPTRs  int
}

// Check evaluates the connecting IP.
func (e *IPRevEvaluator) Check(ctx context.Context, clientIP string) IPRevVerdict {
	ip := parseIP(clientIP)
	if !ip.IsValid() {
		if strings.TrimSpace(clientIP) == "" {
			return IPRevVerdict{Result: IPRevNone, Reason: "no client IP"}
		}
		return IPRevVerdict{Result: IPRevPermError, Reason: "invalid client IP"}
	}

	res := e.resolver()
	names, err := res.LookupAddr(ctx, ip.String())
	if err != nil {
		if ErrNotFound(err) {
			return IPRevVerdict{Result: IPRevNone, Reason: "no PTR record"}
		}
		return IPRevVerdict{Result: IPRevTempError, Reason: "PTR lookup failed: " + dnsErrText(err)}
	}
	if len(names) == 0 {
		return IPRevVerdict{Result: IPRevNone, Reason: "no PTR record"}
	}

	max := e.MaxPTRs
	if max <= 0 {
		max = 10
	}
	if len(names) > max {
		names = names[:max]
	}

	target := net.IP(ip.AsSlice())
	tempFailures := 0
	definitive := false
	for _, name := range names {
		addrs, err := res.LookupIP(ctx, name)
		if err != nil {
			if !ErrNotFound(err) {
				tempFailures++
			}
			continue // a name that cannot be resolved cannot confirm
		}
		definitive = true
		for _, a := range addrs {
			if a.Equal(target) {
				return IPRevVerdict{Result: IPRevPass, PTR: strings.TrimSuffix(name, ".")}
			}
		}
	}
	if !definitive && tempFailures > 0 {
		return IPRevVerdict{Result: IPRevTempError, Reason: "forward lookups failed"}
	}
	return IPRevVerdict{Result: IPRevFail, Reason: "PTR does not forward-confirm to the connecting IP"}
}

func (e *IPRevEvaluator) resolver() Resolver {
	if e.Resolver == nil {
		return NetResolver{}
	}
	return e.Resolver
}

// dnsErrText renders a DNS error briefly for reason strings.
func dnsErrText(err error) string {
	var dnsErr *net.DNSError
	if e, ok := err.(*net.DNSError); ok {
		dnsErr = e
	}
	if dnsErr != nil && dnsErr.Err != "" {
		return dnsErr.Err
	}
	return err.Error()
}
