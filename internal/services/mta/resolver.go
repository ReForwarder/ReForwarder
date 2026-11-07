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

// Package mta is the outbound MTA foundation (Option B wave OB-1, ADR-001):
// MX resolution, the outbound ESMTP session runner, per-domain sending policy,
// and the direct-to-MX delivery executor. The relay path
// (internal/services/email) is untouched and remains the default delivery
// mode; the executor is only invoked for domains configured 'direct'.
//
// Binding rules enforced here (ADR-001, non-negotiables):
//   - the recipient domain is always re-derived from the validated recipient
//     address at delivery time, never taken from client input;
//   - `delivered` is recorded only from an MX 2xx response, never on enqueue;
//   - a direct attempt for an unverified sender domain fails closed.
package mta

import (
	"context"
	"math/rand"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultCacheTTL is how long one MX resolution stays cached. ADR-001 §3
// specifies a 30s TTL cache; short enough to follow DNS changes, long enough
// to keep a burst to one domain from hammering the resolver.
const DefaultCacheTTL = 30 * time.Second

// DefaultMXPort is the port every resolved MX/implicit-MX host is dialed on.
// Tests override it (SetPort) to point at an in-process server.
const DefaultMXPort = 25

// HostSource records why a host is in the resolution result.
const (
	SourceMX             = "mx"
	SourceNullMXFallback = "null_mx_fallback"
	SourceImplicitMX     = "implicit_mx"
)

// Host is one candidate delivery target for a recipient domain.
type Host struct {
	// Host is the hostname (MX target) or IP literal (implicit/null-MX
	// fallback address), without port.
	Host string
	Pref uint16
	Port int
	// Source is one of the Source* constants: "mx", "null_mx_fallback", or
	// "implicit_mx" — observability for which DNS path produced the host.
	Source string
}

// Addr returns the dialable host:port for this host.
func (h Host) Addr() string {
	port := h.Port
	if port == 0 {
		port = DefaultMXPort
	}
	return net.JoinHostPort(h.Host, itoa(port))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Resolver resolves recipient domains to delivery hosts with a TTL cache.
// Resolution rules (ADR-001 §3):
//   - MX records, preference-ordered, shuffled among equal preference;
//   - null MX (RFC 7505, single "0 ." record) falls back to A/AAAA of the
//     domain (see the deviation note in the package docs and ADR-001);
//   - no MX records at all → implicit MX: A/AAAA of the domain itself;
//   - results cached per domain for CacheTTL, with per-domain invalidation.
type Resolver struct {
	ttl      time.Duration
	now      func() time.Time
	lookupMX func(ctx context.Context, domain string) ([]*net.MX, error)
	lookupIP func(ctx context.Context, host string) ([]net.IPAddr, error)
	rand     func(n int) int // shuffle source; overridable in tests
	port     int

	mu    sync.Mutex
	cache map[string]resolverEntry
}

type resolverEntry struct {
	hosts   []Host
	expires time.Time
}

// NewResolver builds a resolver backed by the system DNS with a 30s cache.
func NewResolver() *Resolver {
	return &Resolver{
		ttl:      DefaultCacheTTL,
		now:      time.Now,
		lookupMX: net.DefaultResolver.LookupMX,
		lookupIP: net.DefaultResolver.LookupIPAddr,
		rand:     rand.Intn,
		port:     DefaultMXPort,
		cache:    map[string]resolverEntry{},
	}
}

// SetTTL overrides the cache TTL. Values <= 0 keep the default.
func (r *Resolver) SetTTL(d time.Duration) {
	if d > 0 {
		r.ttl = d
	}
}

// SetPort overrides the port every resolved host is dialed on (test hook; the
// production default is 25).
func (r *Resolver) SetPort(p int) {
	if p > 0 {
		r.port = p
	}
}

// SetLookups replaces the DNS lookup functions (tests inject fakes here;
// production never calls this).
func (r *Resolver) SetLookups(
	mx func(ctx context.Context, domain string) ([]*net.MX, error),
	ip func(ctx context.Context, host string) ([]net.IPAddr, error),
) {
	if mx != nil {
		r.lookupMX = mx
	}
	if ip != nil {
		r.lookupIP = ip
	}
}

// SetRandom replaces the shuffle source (tests) so ordering is deterministic.
func (r *Resolver) SetRandom(fn func(n int) int) {
	if fn != nil {
		r.rand = fn
	}
}

// Invalidate drops the cached resolution for one domain (per-domain cache
// invalidation hook). The next Resolve performs fresh lookups.
func (r *Resolver) Invalidate(domain string) {
	r.mu.Lock()
	delete(r.cache, normalizeDomain(domain))
	r.mu.Unlock()
}

// Resolve returns the candidate hosts for domain, preference-ordered with
// equal-preference groups shuffled, from cache when fresh.
func (r *Resolver) Resolve(ctx context.Context, domain string) ([]Host, error) {
	d := normalizeDomain(domain)
	if d == "" {
		return nil, &net.DNSError{Err: "empty recipient domain", Name: domain, IsTemporary: false}
	}

	r.mu.Lock()
	if e, ok := r.cache[d]; ok && r.now().Before(e.expires) {
		hosts := append([]Host(nil), e.hosts...)
		r.mu.Unlock()
		return hosts, nil
	}
	r.mu.Unlock()

	hosts, err := r.resolveFresh(ctx, d)
	if err != nil {
		// DNS failures are never cached: a transient resolver outage must not
		// pin an empty result for the TTL window.
		return nil, err
	}

	r.mu.Lock()
	r.cache[d] = resolverEntry{hosts: append([]Host(nil), hosts...), expires: r.now().Add(r.ttl)}
	r.mu.Unlock()
	return hosts, nil
}

func (r *Resolver) resolveFresh(ctx context.Context, d string) ([]Host, error) {
	mxs, err := r.lookupMX(ctx, d)
	if err != nil {
		return nil, err
	}

	// Null MX (RFC 7505): a single MX record "0 ." declares the domain does
	// not accept SMTP mail. ADR-001 §3 pins this wave's behavior to an
	// A/AAAA fallback of the domain instead of an immediate refusal — see the
	// recorded deviation in docs/architecture/ADR-001-outbound-mta.md.
	if len(mxs) == 1 && strings.TrimSpace(mxs[0].Host) == "." {
		return r.fallbackHosts(ctx, d, SourceNullMXFallback)
	}

	if len(mxs) == 0 {
		// Implicit MX (RFC 5321 §5.1): no MX records → deliver to the A/AAAA
		// addresses of the domain itself.
		return r.fallbackHosts(ctx, d, SourceImplicitMX)
	}

	hosts := make([]Host, 0, len(mxs))
	for _, mx := range mxs {
		h := strings.TrimSuffix(strings.TrimSpace(mx.Host), ".")
		if h == "" || h == "." {
			continue
		}
		hosts = append(hosts, Host{Host: h, Pref: mx.Pref, Port: r.port, Source: SourceMX})
	}
	if len(hosts) == 0 {
		return r.fallbackHosts(ctx, d, SourceImplicitMX)
	}
	return shuffleByPreference(hosts, r.rand), nil
}

// fallbackHosts resolves A/AAAA for a domain with no usable MX records.
func (r *Resolver) fallbackHosts(ctx context.Context, d, source string) ([]Host, error) {
	ips, err := r.lookupIP(ctx, d)
	if err != nil {
		return nil, err
	}
	hosts := make([]Host, 0, len(ips))
	for _, ip := range ips {
		if ip.IP == nil {
			continue
		}
		hosts = append(hosts, Host{Host: ip.IP.String(), Pref: 0, Port: r.port, Source: source})
	}
	if len(hosts) == 0 {
		return nil, &net.DNSError{Err: "no address records for domain", Name: d, IsNotFound: true}
	}
	return shuffleByPreference(hosts, r.rand), nil
}

// shuffleByPreference sorts hosts by preference ascending and, within each
// equal-preference group, shuffles so no single secondary is always first
// (RFC 5321 §5.1 guidance).
func shuffleByPreference(hosts []Host, randInt func(n int) int) []Host {
	if randInt == nil {
		randInt = rand.Intn
	}
	sort.SliceStable(hosts, func(i, j int) bool { return hosts[i].Pref < hosts[j].Pref })
	for start := 0; start < len(hosts); {
		end := start + 1
		for end < len(hosts) && hosts[end].Pref == hosts[start].Pref {
			end++
		}
		group := hosts[start:end]
		for i := len(group) - 1; i > 0; i-- {
			j := randInt(i + 1)
			group[i], group[j] = group[j], group[i]
		}
		start = end
	}
	return hosts
}

func normalizeDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}
