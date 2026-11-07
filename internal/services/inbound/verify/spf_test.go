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
	"net/netip"
	"testing"
)

// mustAddr parses a client IP for CheckHost (netip) while mustIP builds the
// fake DNS tables (net.IP).
func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("test bug: %q is not an IP", s)
	}
	return addr
}

// fakeDNS is the table-driven fake resolver backing every engine test: a
// map of names to answers plus a map of names to injected errors (a
// *net.DNSError with IsNotFound simulates NXDOMAIN, anything else a
// temporary resolver failure).
type fakeDNS struct {
	txt map[string][]string
	ip  map[string][]net.IP
	mx  map[string][]*net.MX
	ptr map[string][]string

	errs map[string]error
}

func newFakeDNS() *fakeDNS {
	return &fakeDNS{
		txt:  map[string][]string{},
		ip:   map[string][]net.IP{},
		mx:   map[string][]*net.MX{},
		ptr:  map[string][]string{},
		errs: map[string]error{},
	}
}

func (f *fakeDNS) fail(name string, err error) *fakeDNS { f.errs[name] = err; return f }

func (f *fakeDNS) withTXT(name string, records ...string) *fakeDNS {
	f.txt[name] = records
	return f
}

func (f *fakeDNS) withIP(name string, addrs ...net.IP) *fakeDNS {
	f.ip[name] = addrs
	return f
}

func (f *fakeDNS) withMX(name string, hosts ...string) {
	for i, h := range hosts {
		f.mx[name] = append(f.mx[name], &net.MX{Host: h, Pref: uint16(i)})
	}
}

func (f *fakeDNS) withPTR(addr string, names ...string) *fakeDNS {
	f.ptr[addr] = names
	return f
}

func (f *fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	if recs, ok := f.txt[name]; ok {
		return recs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *fakeDNS) LookupIP(_ context.Context, name string) ([]net.IP, error) {
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	if addrs, ok := f.ip[name]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *fakeDNS) LookupAddr(_ context.Context, addr string) ([]string, error) {
	if err, ok := f.errs[addr]; ok {
		return nil, err
	}
	return f.ptr[addr], nil
}

func (f *fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	return f.mx[name], nil
}

// tempErr is a transient resolver failure (temperror); a name without a
// table entry answers a definitive NXDOMAIN (none / non-match).
func tempErr(name string) error {
	return &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
}

func mustIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("test bug: %q is not an IP", s)
	}
	return ip
}

const (
	spfClientIP   = "192.0.2.10"
	spfTestDomain = "example.test"
)

// evalSPF runs one SPF check against the fake table.
func evalSPF(t *testing.T, dns *fakeDNS, record string) SPFVerdict {
	t.Helper()
	dns.withTXT(spfTestDomain, record)
	e := &SPFEvaluator{Resolver: dns}
	return e.CheckHost(context.Background(), mustAddr(t, spfClientIP), spfTestDomain, "user@"+spfTestDomain)
}

// The full mechanism matrix (SF-22): each mechanism matches or not as RFC
// 7208 defines, producing the expected result through the default qualifier.
func TestSPFMechanismMatrix(t *testing.T) {
	cases := []struct {
		name   string
		record string
		setup  func(*fakeDNS)
		want   string
	}{
		{"ip4 exact match", "v=spf1 ip4:192.0.2.10 -all", nil, SPFPass},
		{"ip4 no match", "v=spf1 ip4:192.0.2.11 -all", nil, SPFFail},
		{"ip4 cidr match", "v=spf1 ip4:192.0.2.0/24 -all", nil, SPFPass},
		{"ip6 no match for v4 client", "v=spf1 ip6:2001:db8::1 -all", nil, SPFFail},
		{"a matches own address", "v=spf1 a -all", func(f *fakeDNS) {
			f.withIP("example.test", mustIP(t, spfClientIP))
		}, SPFPass},
		{"a does not match", "v=spf1 a -all", func(f *fakeDNS) {
			f.withIP("example.test", mustIP(t, "198.51.100.1"))
		}, SPFFail},
		{"a with target and cidr", "v=spf1 a:hosts.example.test/24 -all", func(f *fakeDNS) {
			f.withIP("hosts.example.test", mustIP(t, "192.0.2.77"))
		}, SPFPass},
		{"a v6-only answer for v4 client", "v=spf1 a -all", func(f *fakeDNS) {
			f.withIP("example.test", mustIP(t, "2001:db8::1"))
		}, SPFFail},
		{"mx host matches", "v=spf1 mx -all", func(f *fakeDNS) {
			f.withMX("example.test", "mx1.example.test")
			f.withIP("mx1.example.test", mustIP(t, spfClientIP))
		}, SPFPass},
		{"mx host does not match", "v=spf1 mx -all", func(f *fakeDNS) {
			f.withMX("example.test", "mx1.example.test")
			f.withIP("mx1.example.test", mustIP(t, "198.51.100.1"))
		}, SPFFail},
		{"exists matches", "v=spf1 exists:check.example.test -all", func(f *fakeDNS) {
			f.withIP("check.example.test", mustIP(t, "198.51.100.9"))
		}, SPFPass},
		{"exists no address", "v=spf1 exists:check.example.test -all", nil, SPFFail},
		{"ptr validated and in domain", "v=spf1 ptr -all", func(f *fakeDNS) {
			f.withPTR(spfClientIP, "client.example.test.")
			f.withIP("client.example.test", mustIP(t, spfClientIP))
		}, SPFPass},
		{"ptr without forward confirmation", "v=spf1 ptr -all", func(f *fakeDNS) {
			f.withPTR(spfClientIP, "client.example.test.")
			f.withIP("client.example.test", mustIP(t, "198.51.100.1"))
		}, SPFFail},
		{"include passes through", "v=spf1 include:spf.example.test -all", func(f *fakeDNS) {
			f.withTXT("spf.example.test", "v=spf1 ip4:192.0.2.0/24 -all")
		}, SPFPass},
		{"include miss falls to all", "v=spf1 include:spf.example.test -all", func(f *fakeDNS) {
			f.withTXT("spf.example.test", "v=spf1 ip4:198.51.100.0/24 -all")
		}, SPFFail},
		{"bare all", "v=spf1 all", nil, SPFPass},
		{"empty record defaults neutral", "v=spf1", nil, SPFNeutral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dns := newFakeDNS()
			if tc.setup != nil {
				tc.setup(dns)
			}
			got := evalSPF(t, dns, tc.record)
			if got.Result != tc.want {
				t.Fatalf("SPF(%q) = %s (%s), want %s", tc.record, got.Result, got.Reason, tc.want)
			}
		})
	}
}

// An IPv6 client matches through ip6 and a/AAAA answers symmetrically.
func TestSPFIPv6ClientMatches(t *testing.T) {
	client := mustAddr(t, "2001:db8::1")
	cases := []struct {
		name   string
		record string
		setup  func(*fakeDNS)
	}{
		{"ip6 cidr", "v=spf1 ip6:2001:db8::/32 ~all", nil},
		{"a via AAAA", "v=spf1 a ~all", func(f *fakeDNS) {
			f.withIP("example.test", mustIP(t, "2001:db8::1"))
		}},
		{"a with v6 cidr", "v=spf1 a//64 ~all", func(f *fakeDNS) {
			f.withIP("example.test", mustIP(t, "2001:db8::ffff"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dns := newFakeDNS()
			if tc.setup != nil {
				tc.setup(dns)
			}
			dns.withTXT("example.test", tc.record)
			e := &SPFEvaluator{Resolver: dns}
			got := e.CheckHost(context.Background(), client, "example.test", "user@example.test")
			if got.Result != SPFPass {
				t.Fatalf("SPF(%q) = %s (%s), want pass", tc.record, got.Result, got.Reason)
			}
		})
	}
}

// Qualifiers map onto the four positive/negative results (RFC 7208 §4.6.1).
func TestSPFQualifierMatrix(t *testing.T) {
	cases := []struct{ qualifier, want string }{
		{"+", SPFPass},
		{"-", SPFFail},
		{"~", SPFSoftfail},
		{"?", SPFNeutral},
	}
	for _, tc := range cases {
		t.Run("qualifier_"+tc.want, func(t *testing.T) {
			dns := newFakeDNS()
			got := evalSPF(t, dns, "v=spf1 "+tc.qualifier+"ip4:192.0.2.10 -all")
			if got.Result != tc.want {
				t.Fatalf("qualifier %q → %s, want %s", tc.qualifier, got.Result, tc.want)
			}
		})
	}
}

// The none / temperror / permerror / unsupported-syntax column of the matrix.
func TestSPFErrorMatrix(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeDNS)
		want  string
	}{
		{"no TXT record", func(f *fakeDNS) {}, SPFNone},
		{"no SPF record among TXT", func(f *fakeDNS) {
			f.withTXT("example.test", "google-site-verification=abc")
		}, SPFNone},
		{"dns failure", func(f *fakeDNS) {
			f.fail("example.test", tempErr("example.test"))
		}, SPFTempError},
		{"two SPF records", func(f *fakeDNS) {
			f.withTXT("example.test", "v=spf1 -all", "v=spf1 +all")
		}, SPFPermError},
		{"unknown mechanism", func(f *fakeDNS) {
			f.withTXT("example.test", "v=spf1 frobnicate -all")
		}, SPFPermError},
		{"macro expansion unsupported", func(f *fakeDNS) {
			f.withTXT("example.test", "v=spf1 include:%{ir}.spd.example.test -all")
		}, SPFPermError},
		{"include target without record", func(f *fakeDNS) {
			f.withTXT("example.test", "v=spf1 include:missing.example.test -all")
		}, SPFPermError},
		{"include target dns failure", func(f *fakeDNS) {
			f.withTXT("example.test", "v=spf1 include:down.example.test -all")
			f.fail("down.example.test", tempErr("down.example.test"))
		}, SPFTempError},
		{"redirect target without record", func(f *fakeDNS) {
			f.withTXT("example.test", "v=spf1 redirect=absent.example.test")
		}, SPFPermError},
		{"duplicate redirect", func(f *fakeDNS) {
			f.withTXT("example.test", "v=spf1 redirect=a.example.test redirect=b.example.test")
		}, SPFPermError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dns := newFakeDNS()
			tc.setup(dns)
			e := &SPFEvaluator{Resolver: dns}
			got := e.CheckHost(context.Background(), mustAddr(t, spfClientIP), "example.test", "user@example.test")
			if got.Result != tc.want {
				t.Fatalf("SPF = %s (%s), want %s", got.Result, got.Reason, tc.want)
			}
		})
	}
}

// include passes when the child domain passes and does not match on
// fail/softfail/neutral children (RFC 7208 §5.2).
func TestSPFIncludeChildResults(t *testing.T) {
	for _, tc := range []struct {
		child string
		want  string
	}{
		{"v=spf1 ip4:192.0.2.10 all", SPFPass},
		{"v=spf1 ip4:198.51.100.1 -all", SPFFail},
		{"v=spf1 ip4:198.51.100.1 ~all", SPFFail},
		{"v=spf1 ip4:198.51.100.1 ?all", SPFFail},
	} {
		t.Run(tc.child, func(t *testing.T) {
			dns := newFakeDNS().
				withTXT("example.test", "v=spf1 include:spf.example.test -all").
				withTXT("spf.example.test", tc.child)
			got := evalSPF(t, dns, "v=spf1 include:spf.example.test -all")
			if got.Result != tc.want {
				t.Fatalf("include child %q → %s, want %s", tc.child, got.Result, tc.want)
			}
		})
	}
}

// redirect re-evaluates against the target's record.
func TestSPFRedirectFollowsTarget(t *testing.T) {
	dns := newFakeDNS().
		withTXT("example.test", "v=spf1 redirect=other.example.test").
		withTXT("other.example.test", "v=spf1 ip4:192.0.2.10 -all")
	got := evalSPF(t, dns, "v=spf1 redirect=other.example.test")
	if got.Result != SPFPass {
		t.Fatalf("redirect → %s (%s), want pass", got.Result, got.Reason)
	}
	if got.Domain != "example.test" {
		t.Fatalf("verdict domain = %s, want the evaluated domain example.test", got.Domain)
	}
}

// include/redirect recursion is capped (RFC 7208 §4.6.4) — a loop is a
// permerror, not a hang.
func TestSPFIncludeLoopTerminates(t *testing.T) {
	dns := newFakeDNS().
		withTXT("a.example.test", "v=spf1 include:b.example.test -all").
		withTXT("b.example.test", "v=spf1 include:a.example.test -all")
	e := &SPFEvaluator{Resolver: dns}
	got := e.CheckHost(context.Background(), mustAddr(t, spfClientIP), "a.example.test", "user@a.example.test")
	if got.Result != SPFPermError {
		t.Fatalf("include loop → %s (%s), want permerror", got.Result, got.Reason)
	}
}

// The 10-lookup DNS budget is enforced: a chain of many includes terminates
// with permerror (RFC 7208 §4.6.4).
func TestSPFDNSBudgetExceeded(t *testing.T) {
	dns := newFakeDNS()
	for i := 0; i < 12; i++ {
		dns.withTXT(
			string(rune('a'+i))+".chain.test",
			"v=spf1 include:"+string(rune('a'+i+1))+".chain.test -all")
	}
	e := &SPFEvaluator{Resolver: dns}
	got := e.CheckHost(context.Background(), mustAddr(t, spfClientIP), "a.chain.test", "user@a.chain.test")
	if got.Result != SPFPermError {
		t.Fatalf("budget overflow → %s (%s), want permerror", got.Result, got.Reason)
	}
}

// Unknown client IP: SPF cannot be evaluated and honestly reports none.
func TestSPFNoClientIPYieldsNone(t *testing.T) {
	e := &SPFEvaluator{Resolver: newFakeDNS().withTXT("example.test", "v=spf1 -all")}
	got := e.CheckHost(context.Background(), netip.Addr{}, "example.test", "user@example.test")
	if got.Result != SPFNone {
		t.Fatalf("no client IP → %s, want none", got.Result)
	}
}

// Malformed envelope domain: none, without any DNS traffic.
func TestSPFInvalidDomainYieldsNone(t *testing.T) {
	dns := newFakeDNS().fail("bad domain", tempErr("bad domain"))
	e := &SPFEvaluator{Resolver: dns}
	got := e.CheckHost(context.Background(), mustAddr(t, spfClientIP), "bad domain", "x")
	if got.Result != SPFNone {
		t.Fatalf("invalid domain → %s, want none", got.Result)
	}
}
