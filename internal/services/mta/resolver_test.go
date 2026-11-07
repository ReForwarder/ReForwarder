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

package mta

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeResolver builds a Resolver over injected lookup functions.
func fakeResolver(mx func(string) ([]*net.MX, error), ip func(string) ([]net.IPAddr, error)) *Resolver {
	r := NewResolver()
	r.SetLookups(
		func(ctx context.Context, domain string) ([]*net.MX, error) { return mx(domain) },
		func(ctx context.Context, host string) ([]net.IPAddr, error) { return ip(host) },
	)
	return r
}

// MX hosts are preference-ordered; equal-preference groups stay grouped.
func TestResolveOrdersByPreference(t *testing.T) {
	calls := 0
	r := fakeResolver(
		func(domain string) ([]*net.MX, error) {
			calls++
			return []*net.MX{
				{Host: "mx3.example.com.", Pref: 30},
				{Host: "mx1.example.com.", Pref: 10},
				{Host: "mx2.example.com.", Pref: 20},
			}, nil
		},
		func(host string) ([]net.IPAddr, error) { return nil, errors.New("no A lookup expected") },
	)
	r.SetRandom(func(n int) int { return 0 })

	hosts, err := r.Resolve(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(hosts) != 3 {
		t.Fatalf("hosts = %v, want 3", hosts)
	}
	// Distinct preferences: exact order, no shuffle reordering possible.
	wantOrder := []uint16{10, 20, 30}
	for i, h := range hosts {
		if h.Pref != wantOrder[i] {
			t.Fatalf("host[%d].Pref = %d, want %d (hosts %v)", i, h.Pref, wantOrder[i], hosts)
		}
		if h.Source != SourceMX {
			t.Fatalf("host[%d].Source = %q, want %q", i, h.Source, SourceMX)
		}
		if h.Port != DefaultMXPort {
			t.Fatalf("host[%d].Port = %d, want %d", i, h.Port, DefaultMXPort)
		}
		if strings.HasSuffix(h.Host, ".") {
			t.Fatalf("host %q must be stored without the trailing dot", h.Host)
		}
	}
	if calls != 1 {
		t.Fatalf("lookupMX calls = %d, want 1", calls)
	}
}

// Equal-preference hosts shuffle within the group (deterministic here via the
// injected random source) and the group stays bounded by its preference tier.
func TestResolveShufflesEqualPreference(t *testing.T) {
	r := fakeResolver(
		func(domain string) ([]*net.MX, error) {
			return []*net.MX{
				{Host: "a.example.com.", Pref: 10},
				{Host: "b.example.com.", Pref: 10},
				{Host: "c.example.com.", Pref: 50},
			}, nil
		},
		nil,
	)
	// randInt always picks index 0: the Fisher-Yates within the two-host
	// equal-preference group deterministically swaps the pair.
	r.SetRandom(func(n int) int { return 0 })

	hosts, err := r.Resolve(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(hosts) != 3 {
		t.Fatalf("hosts = %v, want 3", hosts)
	}
	if hosts[0].Pref != 10 || hosts[1].Pref != 10 || hosts[2].Pref != 50 {
		t.Fatalf("preference tiers broken: %v", hosts)
	}
	// The input order [a, b] is deterministically shuffled to [b, a].
	if hosts[0].Host != "b.example.com" || hosts[1].Host != "a.example.com" {
		t.Fatalf("equal-preference group was not shuffled: %v", hosts)
	}
}

// The 30s TTL cache: a second Resolve within the window performs no lookups;
// after expiry it does.
func TestResolveCachesWithTTL(t *testing.T) {
	calls := 0
	r := fakeResolver(
		func(domain string) ([]*net.MX, error) {
			calls++
			return []*net.MX{{Host: "mx1.example.com.", Pref: 10}}, nil
		},
		nil,
	)
	base := time.Now()
	clock := base
	r.now = func() time.Time { return clock }
	r.SetTTL(30 * time.Second)

	ctx := context.Background()
	if _, err := r.Resolve(ctx, "example.com"); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if _, err := r.Resolve(ctx, "example.com"); err != nil {
		t.Fatalf("cached Resolve: %v", err)
	}
	if calls != 1 {
		t.Fatalf("lookupMX calls within TTL = %d, want 1 (cache miss)", calls)
	}

	clock = base.Add(31 * time.Second)
	if _, err := r.Resolve(ctx, "example.com"); err != nil {
		t.Fatalf("expired Resolve: %v", err)
	}
	if calls != 2 {
		t.Fatalf("lookupMX calls after TTL = %d, want 2", calls)
	}
}

// The per-domain invalidation hook forces a fresh lookup regardless of TTL.
func TestInvalidateDomainForcesFreshLookup(t *testing.T) {
	calls := 0
	r := fakeResolver(
		func(domain string) ([]*net.MX, error) {
			calls++
			return []*net.MX{{Host: "mx1.example.com.", Pref: 10}}, nil
		},
		nil,
	)
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "example.com"); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	r.Invalidate("Example.COM.") // case/normalized like Resolve
	if _, err := r.Resolve(ctx, "example.com"); err != nil {
		t.Fatalf("post-invalidation Resolve: %v", err)
	}
	if calls != 2 {
		t.Fatalf("lookupMX calls = %d, want 2 after Invalidate", calls)
	}
}

// Null MX (RFC 7505, "0 .") falls back to the domain's A/AAAA records per
// ADR-001 §3 (see the recorded RFC tension in the ADR / report).
func TestNullMXFallsBackToA(t *testing.T) {
	ipCalls := 0
	r := fakeResolver(
		func(domain string) ([]*net.MX, error) {
			return []*net.MX{{Host: ".", Pref: 0}}, nil
		},
		func(host string) ([]net.IPAddr, error) {
			ipCalls++
			if host != "example.com" {
				t.Fatalf("A fallback looked up %q, want the recipient domain", host)
			}
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
		},
	)

	hosts, err := r.Resolve(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(hosts) != 1 || hosts[0].Host != "192.0.2.10" {
		t.Fatalf("hosts = %v, want the A-record fallback", hosts)
	}
	if hosts[0].Source != SourceNullMXFallback {
		t.Fatalf("Source = %q, want %q", hosts[0].Source, SourceNullMXFallback)
	}
	if ipCalls != 1 {
		t.Fatalf("lookupIP calls = %d, want 1", ipCalls)
	}
}

// No MX records at all → implicit MX: deliver to the domain's own addresses.
func TestImplicitMXWhenNoMXRecords(t *testing.T) {
	r := fakeResolver(
		func(domain string) ([]*net.MX, error) { return []*net.MX{}, nil },
		func(host string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.20")}}, nil
		},
	)

	hosts, err := r.Resolve(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(hosts) != 1 || hosts[0].Host != "192.0.2.20" {
		t.Fatalf("hosts = %v, want the implicit-MX address", hosts)
	}
	if hosts[0].Source != SourceImplicitMX {
		t.Fatalf("Source = %q, want %q", hosts[0].Source, SourceImplicitMX)
	}
}

// A DNS failure is surfaced, not cached — the next Resolve after a resolver
// blip retries the lookup instead of pinning the error for the TTL window.
func TestResolveErrorIsNotCached(t *testing.T) {
	calls := 0
	fail := true
	r := fakeResolver(
		func(domain string) ([]*net.MX, error) {
			calls++
			if fail {
				return nil, errors.New("temporary resolver failure")
			}
			return []*net.MX{{Host: "mx1.example.com.", Pref: 10}}, nil
		},
		nil,
	)
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "example.com"); err == nil {
		t.Fatal("Resolve must surface the lookup error")
	}
	fail = false
	if _, err := r.Resolve(ctx, "example.com"); err != nil {
		t.Fatalf("Resolve after recovery: %v", err)
	}
	if calls != 2 {
		t.Fatalf("lookupMX calls = %d, want 2 (errors must not be cached)", calls)
	}
}

// An empty recipient domain never reaches the resolver.
func TestResolveRejectsEmptyDomain(t *testing.T) {
	r := fakeResolver(nil, nil)
	if _, err := r.Resolve(context.Background(), "  "); err == nil {
		t.Fatal("Resolve of an empty domain must fail")
	}
}
