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

package spamfilter

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jkaninda/logger"
)

// DNSBL (RBL) checking for the connecting IP — ADR-006 decision 1's first
// DNSBL capability. Strictly config-gated: no zone is queried unless the
// operator lists it in POSTA_SPAM_DNSBL_ZONES, and only zones on the
// allowlist below are honored — a configured-but-unlisted zone is dropped at
// construction (logged) rather than queried. This keeps an operator from
// pointing the checker at an attacker-controlled zone that could answer
// "listed" for everything.
//
// A lookup that errors or times out is treated as a miss (fail-open): a
// flaky DNSBL must not quarantine mail by itself. Hits are A records inside
// 127.0.0.0/8, the convention every listed zone follows.

// DNSBLAllowlist is the only set of zones the checker will ever query.
// Well-known, established public zones; each documents its return
// convention (127.0.0.x loopback answers).
var DNSBLAllowlist = map[string]bool{
	"zen.spamhaus.org":       true, // Spamhaus ZEN (SBL/XBL/PBL)
	"bl.spamcop.net":         true, // SpamCop
	"b.barracudacentral.org": true, // Barracuda Reputation Block List
	"dnsbl.sorbs.net":        true, // SORBS aggregate
	"psbl.surriel.com":       true, // Passive Spam Block List
}

// dnsblLookupTimeout bounds one zone's DNS query.
const dnsblLookupTimeout = 2 * time.Second

// DNSBLResolver is the DNS seam (tests inject a stub).
type DNSBLResolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

// NetDNSBLResolver adapts the Go default resolver.
type NetDNSBLResolver struct{}

func (NetDNSBLResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, network, host)
}

// DNSBLChecker queries the configured, allowlisted zones for one IP.
// Zero zones (the default configuration) means disabled.
type DNSBLChecker struct {
	zones    []string
	resolver DNSBLResolver
}

// NewDNSBLChecker builds a checker from a comma-separated zone list. Only
// allowlisted zones survive; the rest are logged and dropped. An empty list
// (or nil resolver resolver fallback) yields a disabled checker.
func NewDNSBLChecker(zonesCSV string, resolver DNSBLResolver) *DNSBLChecker {
	if resolver == nil {
		resolver = NetDNSBLResolver{}
	}
	c := &DNSBLChecker{resolver: resolver}
	for _, raw := range strings.Split(zonesCSV, ",") {
		z := strings.ToLower(strings.TrimSpace(raw))
		if z == "" {
			continue
		}
		if !DNSBLAllowlist[z] {
			logger.Warn("spamfilter: ignoring non-allowlisted DNSBL zone",
				"zone", z)
			continue
		}
		c.zones = append(c.zones, z)
	}
	return c
}

// Enabled reports whether any zone is configured.
func (c *DNSBLChecker) Enabled() bool { return c != nil && len(c.zones) > 0 }

// Check returns the zones that list ip. lookups run sequentially with a
// per-zone timeout; errors are misses. Loopback/private/unparseable IPs are
// never queried (a private peer address in a public DNSBL query is either a
// misconfiguration or an information leak).
func (c *DNSBLChecker) Check(ctx context.Context, ipStr string) []string {
	if !c.Enabled() || ipStr == "" {
		return nil
	}
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return nil
	}
	rev, ok := reverseOctets(ip)
	if !ok {
		return nil
	}

	var hits []string
	for _, zone := range c.zones {
		name := rev + "." + zone
		lctx, cancel := context.WithTimeout(ctx, dnsblLookupTimeout)
		addrs, err := c.resolver.LookupIP(lctx, "ip4", name)
		cancel()
		if err != nil {
			continue // fail-open: NXDOMAIN, timeout, tempfail → miss
		}
		for _, a := range addrs {
			if a.To4() != nil && a.IsLoopback() {
				hits = append(hits, zone)
				break
			}
		}
	}
	return hits
}

// reverseOctets renders an IPv4 (or IPv4-mapped) address in the DNSBL
// dotted-reverse form (a.b.c.d → d.c.b.a).
func reverseOctets(ip net.IP) (string, bool) {
	v4 := ip.To4()
	if v4 == nil {
		return "", false // IPv6 DNSBL zones are out of scope at Phase 2
	}
	return fmt.Sprintf("%d.%d.%d.%d", v4[3], v4[2], v4[1], v4[0]), true
}
