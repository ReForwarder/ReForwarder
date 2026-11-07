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
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// SPF results (RFC 7208 §2.6).
const (
	SPFPass      = "pass"
	SPFFail      = "fail"
	SPFSoftfail  = "softfail"
	SPFNeutral   = "neutral"
	SPFNone      = "none"
	SPFTempError = "temperror"
	SPFPermError = "permerror"
)

// SPFVerdict is the outcome of one SPF evaluation.
type SPFVerdict struct {
	// Result is one of pass|fail|softfail|neutral|none|temperror|permerror.
	Result string
	// Reason is a short human-readable explanation for the verdict.
	Reason string
	// Domain is the policy domain the record was evaluated against.
	Domain string
}

// SPFEvaluator evaluates the connecting client IP against the envelope-from
// domain's SPF record (SF-22). It is a deliberately small RFC 7208 subset:
// include, redirect, a, mx, ip4, ip6, exists and ptr mechanisms with all four
// qualifiers. Macros are NOT supported — a record that needs macro expansion
// evaluates to permerror with an explicit reason rather than a guessed result.
type SPFEvaluator struct {
	Resolver Resolver
	// MaxDNSQueries caps the total number of DNS lookups for one evaluation
	// (RFC 7208 §4.6.4 requires a limit of 10). Zero means 10.
	MaxDNSQueries int
	// MaxRedirects caps include/redirect recursion depth (RFC 7208 §4.6.4).
	// Zero means 10.
	MaxRedirects int
}

// spfState carries the shared DNS budget and recursion depth across the
// recursive check_host evaluations of one message.
type spfState struct {
	ctx     context.Context
	res     Resolver
	queries int
	budget  int
	depth   int
	maxDept int
}

// CheckHost evaluates SPF for the connecting client IP and the envelope-from
// domain. An unknown (invalid) IP or an unusable domain yields none — the
// honest result when one of the two inputs the algorithm is defined over is
// missing.
func (e *SPFEvaluator) CheckHost(ctx context.Context, ip netip.Addr, domain, sender string) SPFVerdict {
	domain = normalizeDomain(domain)
	if !isValidDomain(domain) {
		return SPFVerdict{Result: SPFNone, Reason: "no usable envelope-from domain"}
	}
	if !ip.IsValid() {
		return SPFVerdict{Result: SPFNone, Reason: "no client IP available", Domain: domain}
	}
	if sender == "" {
		sender = "postmaster@" + domain
	}
	maxQ := e.MaxDNSQueries
	if maxQ <= 0 {
		maxQ = 10
	}
	maxD := e.MaxRedirects
	if maxD <= 0 {
		maxD = 10
	}
	st := &spfState{ctx: ctx, res: e.Resolver, budget: maxQ, maxDept: maxD}
	return st.checkHost(ip, domain, sender)
}

// tempError marks a DNS failure so include() can propagate it as temperror
// (RFC 7208 §5.2) while other errors stay permerror.
type tempError struct{ err error }

func (t *tempError) Error() string { return t.err.Error() }

// checkHost is the recursive RFC 7208 check_host() over the trimmed budget.
func (st *spfState) checkHost(ip netip.Addr, domain, sender string) SPFVerdict {
	txts, err := st.lookupTXT(domain)
	if err != nil {
		if ErrNotFound(err) {
			return SPFVerdict{Result: SPFNone, Reason: "no SPF record for " + domain, Domain: domain}
		}
		return SPFVerdict{Result: SPFTempError, Reason: fmt.Sprintf("spf txt lookup for %s: %v", domain, err), Domain: domain}
	}
	var records []string
	for _, t := range txts {
		if isSPFRecord(t) {
			records = append(records, t)
		}
	}
	switch {
	case len(records) == 0:
		// RFC 7208 §4.5: TXT exists but none is an SPF record — none. A
		// caller that reached here via include/redirect translates none into
		// permerror (§5.2/§6.1).
		return SPFVerdict{Result: SPFNone, Reason: "no SPF record for " + domain, Domain: domain}
	case len(records) > 1:
		return SPFVerdict{Result: SPFPermError, Reason: fmt.Sprintf("%d SPF records for %s", len(records), domain), Domain: domain}
	}

	matched, qualifier, reason, redirect, err := st.evalRecord(ip, domain, sender, records[0])
	if err != nil {
		if te := (*tempError)(nil); errors.As(err, &te) {
			return SPFVerdict{Result: SPFTempError, Reason: err.Error(), Domain: domain}
		}
		return SPFVerdict{Result: SPFPermError, Reason: err.Error(), Domain: domain}
	}
	if matched {
		return SPFVerdict{Result: qualifier, Reason: reason, Domain: domain}
	}

	// No mechanism matched: follow redirect= (RFC 7208 §6.1) or fall back to
	// neutral (RFC 7208 §4.7 default result).
	if redirect == "" {
		return SPFVerdict{Result: SPFNeutral, Reason: "default result (no mechanism matched)", Domain: domain}
	}
	if st.depth+1 >= st.maxDept {
		return SPFVerdict{Result: SPFPermError, Reason: "include/redirect recursion limit exceeded", Domain: domain}
	}
	st.depth++
	child := st.checkHost(ip, redirect, sender)
	st.depth--
	if child.Result == SPFNone {
		// RFC 7208 §6.1: a redirect target without an SPF record is permerror.
		child.Result = SPFPermError
		child.Reason = fmt.Sprintf("redirect target %s has no SPF record", redirect)
	}
	child.Domain = domain
	return child
}

// evalRecord walks one SPF record. It reports the matched qualifier (with a
// reason) when a mechanism matched, the redirect target when the record ends
// without a match, or an error for permerror/temperror conditions.
func (st *spfState) evalRecord(ip netip.Addr, domain, sender, record string) (bool, string, string, string, error) {
	var redirect string
	fields := strings.Fields(record)
	for _, tok := range fields[1:] {
		qualifier := SPFPass
		body := tok
		switch tok[0] {
		case '+', '-', '~', '?':
			qualifier = map[byte]string{'+': SPFPass, '-': SPFFail, '~': SPFSoftfail, '?': SPFNeutral}[tok[0]]
			body = tok[1:]
		}
		if body == "" {
			return false, "", "", "", fmt.Errorf("empty mechanism in %q", tok)
		}
		// Modifiers (RFC 7208 §5/§6): name=value. Unknown modifiers are
		// ignored; exp= is ignored outright (explanation-only, never affects
		// the result — so macros inside it do not surface either).
		if isModifier(body) {
			name := body[:strings.IndexByte(body, '=')]
			if strings.EqualFold(name, "redirect") {
				if redirect != "" {
					return false, "", "", "", fmt.Errorf("duplicate redirect modifier")
				}
				target := body[len("redirect="):]
				if strings.Contains(target, "%") {
					return false, "", "", "", fmt.Errorf("spf macros are not supported (redirect target %q)", target)
				}
				redirect = normalizeDomain(target)
				if !isValidDomain(redirect) {
					return false, "", "", "", fmt.Errorf("malformed redirect target %q", target)
				}
			}
			continue
		}
		if strings.Contains(body, "%") {
			return false, "", "", "", fmt.Errorf("spf macros are not supported (mechanism %q)", body)
		}

		ok, err := st.evalMechanism(ip, domain, sender, body)
		if err != nil {
			return false, "", "", "", err
		}
		if ok {
			return true, qualifier, fmt.Sprintf("matched %s", tok), "", nil
		}
	}
	return false, "", "", redirect, nil
}

// evalMechanism dispatches one mechanism body (qualifier stripped).
func (st *spfState) evalMechanism(ip netip.Addr, domain, sender, body string) (bool, error) {
	name := body
	rest := ""
	if i := strings.IndexAny(body, ":/"); i >= 0 {
		name, rest = body[:i], body[i:]
	}
	switch strings.ToLower(name) {
	case "all":
		if rest != "" {
			return false, fmt.Errorf("all mechanism takes no arguments: %q", body)
		}
		return true, nil
	case "include":
		if !strings.HasPrefix(rest, ":") || len(rest) < 2 {
			return false, fmt.Errorf("include without domain: %q", body)
		}
		target := normalizeDomain(rest[1:])
		if !isValidDomain(target) {
			return false, fmt.Errorf("malformed include target %q", body)
		}
		if st.depth+1 >= st.maxDept {
			return false, fmt.Errorf("include/redirect recursion limit exceeded")
		}
		st.depth++
		child := st.checkHost(ip, target, sender)
		st.depth--
		switch child.Result {
		case SPFPass:
			return true, nil
		case SPFFail, SPFSoftfail, SPFNeutral:
			return false, nil
		case SPFTempError:
			return false, &tempError{err: fmt.Errorf("include %s: %s", target, child.Reason)}
		default: // permerror and none (RFC 7208 §5.2: include target without a record is permerror)
			return false, fmt.Errorf("include %s: %s", target, child.Reason)
		}
	case "a", "mx":
		return st.evalAddrMechanism(ip, domain, name, rest)
	case "ip4":
		return matchLiteralIP(ip, rest, 4)
	case "ip6":
		return matchLiteralIP(ip, rest, 6)
	case "exists":
		if !strings.HasPrefix(rest, ":") || len(rest) < 2 {
			return false, fmt.Errorf("exists without domain: %q", body)
		}
		target := normalizeDomain(rest[1:])
		if !isValidDomain(target) {
			return false, fmt.Errorf("malformed exists target %q", body)
		}
		addrs, err := st.lookupIP(target)
		if err != nil {
			return false, dnsErr(err, target)
		}
		// RFC 7208 §5.7 defines exists over A records; we accept AAAA answers
		// too — a broadened but harmless reading for a match-only mechanism.
		return len(addrs) > 0, nil
	case "ptr":
		return st.evalPtr(ip, domain, rest)
	default:
		return false, fmt.Errorf("unknown mechanism %q", body)
	}
}

// evalAddrMechanism implements a and mx with optional :domain and dual
// cidr-length (RFC 7208 §5.3/§5.4).
func (st *spfState) evalAddrMechanism(ip netip.Addr, domain, name, rest string) (bool, error) {
	target := domain
	cidrPart := ""
	if strings.HasPrefix(rest, ":") {
		spec := rest[1:]
		if i := strings.IndexByte(spec, '/'); i >= 0 {
			target, cidrPart = spec[:i], spec[i:]
		} else {
			target = spec
		}
	} else if rest != "" {
		cidrPart = rest
	}
	target = normalizeDomain(target)
	if !isValidDomain(target) {
		return false, fmt.Errorf("malformed %s target %q", name, rest)
	}
	c4, c6, err := parseDualCIDR(cidrPart)
	if err != nil {
		return false, fmt.Errorf("malformed cidr in %q: %v", name+rest, err)
	}

	hosts := []string{target}
	if strings.EqualFold(name, "mx") {
		mxs, err := st.lookupMX(target)
		if err != nil {
			return false, dnsErr(err, target)
		}
		if len(mxs) > 10 {
			mxs = mxs[:10]
		}
		hosts = mxs
	}
	for _, h := range hosts {
		addrs, err := st.lookupIP(h)
		if err != nil {
			return false, dnsErr(err, h)
		}
		if addrMatches(ip, addrs, c4, c6) {
			return true, nil
		}
	}
	return false, nil
}

// evalPtr implements the deprecated ptr mechanism (RFC 7208 §5.5) with the
// RFC's 10-name cap. The optional ":domain" restricts the match to names at
// or under that domain.
func (st *spfState) evalPtr(ip netip.Addr, domain, rest string) (bool, error) {
	target := domain
	if strings.HasPrefix(rest, ":") {
		target = normalizeDomain(rest[1:])
		if !isValidDomain(target) {
			return false, fmt.Errorf("malformed ptr target %q", rest)
		}
	}
	names, err := st.res.LookupAddr(st.ctx, ip.String())
	if err != nil {
		// A missing or failed PTR record is a non-match, not an error
		// (RFC 7208 §5.5: ptr itself never yields temperror/permerror).
		return false, nil
	}
	if len(names) > 10 {
		names = names[:10]
	}
	for _, n := range names {
		host := normalizeDomain(n)
		if host == "" || !isValidDomain(host) {
			continue
		}
		addrs, err := st.lookupIP(host)
		if err != nil {
			continue // unvalidated name: skip
		}
		if !containsAddr(addrs, ip) {
			continue // forward confirmation failed
		}
		if host == target || strings.HasSuffix(host, "."+target) {
			return true, nil
		}
	}
	return false, nil
}

// ---------- helpers ----------

// dnsErr classifies a DNS failure: a definitive negative answer (NXDOMAIN)
// is a non-match, a transient resolver failure propagates as temperror.
func dnsErr(err error, name string) error {
	if ErrNotFound(err) {
		return nil
	}
	return &tempError{err: fmt.Errorf("dns lookup for %s: %v", name, err)}
}

func (st *spfState) lookupTXT(name string) ([]string, error) {
	st.queries++
	if st.queries > st.budget {
		// RFC 7208 §4.6.4: exceeding the lookup limit is a permerror.
		return nil, fmt.Errorf("spf dns query budget (%d) exceeded", st.budget)
	}
	return st.res.LookupTXT(st.ctx, name)
}

func (st *spfState) lookupIP(name string) ([]net.IP, error) {
	st.queries++
	if st.queries > st.budget {
		return nil, fmt.Errorf("spf dns query budget (%d) exceeded", st.budget)
	}
	return st.res.LookupIP(st.ctx, name)
}

func (st *spfState) lookupMX(name string) ([]string, error) {
	st.queries++
	if st.queries > st.budget {
		return nil, fmt.Errorf("spf dns query budget (%d) exceeded", st.budget)
	}
	mxs, err := st.res.LookupMX(st.ctx, name)
	if err != nil {
		return nil, err
	}
	hosts := make([]string, 0, len(mxs))
	for _, m := range mxs {
		hosts = append(hosts, m.Host)
	}
	return hosts, nil
}

// isSPFRecord reports whether one TXT record is an SPF record (RFC 7208 §4.5).
func isSPFRecord(record string) bool {
	f := strings.Fields(record)
	return len(f) > 0 && strings.EqualFold(f[0], "v=spf1")
}

// isWordRune reports whether c is allowed in a modifier name.
func isWordRune(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '_' || c == '.'
}

// isModifier reports whether a token has the modifier name=value shape
// (RFC 7208 §4.6.1 grammar). Mechanism names never contain '=' before ':'.
func isModifier(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	if strings.ContainsAny(tok[:eq], ":/") {
		return false
	}
	for i := 0; i < eq; i++ {
		if !isWordRune(tok[i]) {
			return false
		}
	}
	return true
}

// parseDualCIDR parses "[/n4][//n6]" into the two prefix lengths (0 = unset).
func parseDualCIDR(part string) (int, int, error) {
	if part == "" {
		return 0, 0, nil
	}
	c4, c6 := 0, 0
	v6 := ""
	if i := strings.Index(part, "//"); i >= 0 {
		c4part, v6part := part[:i], part[i+2:]
		if v6part != "" {
			v6 = strings.TrimPrefix(v6part, "/")
		}
		part = c4part
	}
	if part != "" {
		n, err := parseCIDRNumber(strings.TrimPrefix(part, "/"))
		if err != nil {
			return 0, 0, err
		}
		c4 = n
	}
	if v6 != "" {
		n, err := parseCIDRNumber(v6)
		if err != nil {
			return 0, 0, err
		}
		c6 = n
	}
	return c4, c6, nil
}

func parseCIDRNumber(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty cidr length")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid cidr length %q", s)
		}
		n = n*10 + int(c-'0')
		if n > 128 {
			return 0, fmt.Errorf("cidr length out of range %q", s)
		}
	}
	return n, nil
}

// matchLiteralIP implements ip4:/ip6: with an optional prefix length. spec is
// the mechanism remainder including the leading colon (":1.2.3.4/24").
func matchLiteralIP(ip netip.Addr, spec string, family int) (bool, error) {
	if !strings.HasPrefix(spec, ":") || len(spec) < 2 {
		return false, fmt.Errorf("ip%d without address", family)
	}
	body := spec[1:]
	addr := body
	bits := 32
	if family == 6 {
		bits = 128
	}
	if i := strings.IndexByte(body, '/'); i >= 0 {
		addr = body[:i]
		n, err := parseCIDRNumber(body[i+1:])
		if err != nil {
			return false, err
		}
		if n > bits {
			return false, fmt.Errorf("cidr length out of range %q", spec)
		}
		bits = n
	}
	want, err := netip.ParseAddr(addr)
	if err != nil {
		return false, fmt.Errorf("malformed ip%d address %q", family, spec)
	}
	if want.Zone() != "" {
		return false, fmt.Errorf("ip%d address with zone %q", family, spec)
	}
	return ipInPrefix(ip, want, bits), nil
}

// addrMatches reports whether ip falls inside one of addrs, honoring the
// dual-cidr lengths (0 = full address). Addresses are compared unmapped, so
// an IPv4 client never matches through an IPv4-mapped AAAA answer.
func addrMatches(ip netip.Addr, addrs []net.IP, c4, c6 int) bool {
	cand := ip.Unmap()
	for _, a := range addrs {
		ra, ok := netip.AddrFromSlice(a)
		if !ok {
			continue
		}
		ra = ra.Unmap()
		if ra.Is4() != cand.Is4() {
			continue
		}
		bits := 128
		if cand.Is4() {
			bits = 32
			if c4 > 0 {
				bits = c4
			}
		} else if c6 > 0 {
			bits = c6
		}
		if ipInPrefix(cand, ra, bits) {
			return true
		}
	}
	return false
}

func containsAddr(addrs []net.IP, ip netip.Addr) bool {
	for _, a := range addrs {
		if ra, ok := netip.AddrFromSlice(a); ok && ra.Unmap() == ip.Unmap() {
			return true
		}
	}
	return false
}

// ipInPrefix reports whether ip falls inside base/bits.
func ipInPrefix(ip, base netip.Addr, bits int) bool {
	p := netip.PrefixFrom(base.Unmap(), bits)
	if !p.IsValid() {
		return false
	}
	return p.Contains(ip.Unmap())
}

// normalizeDomain lowercases, trims spaces and the trailing dot.
func normalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	return strings.TrimSuffix(d, ".")
}

// isHostRune reports whether c is allowed in a host name label.
func isHostRune(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// isValidDomain applies a light RFC 1035 shape check: non-empty, no empty
// labels, no spaces or control characters, bounded length. IDN A-labels pass;
// raw UTF-8 domains do not (senders must present punycode).
func isValidDomain(d string) bool {
	if d == "" || len(d) > 253 {
		return false
	}
	if strings.ContainsAny(d, " \t\r\n") {
		return false
	}
	labels := strings.Split(d, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return false
		}
		for _, c := range l {
			if !isHostRune(c) {
				return false
			}
		}
	}
	return true
}

// domainOf extracts the domain from an address like "user@example.com" or
// "<user@example.com>", or "" when there is none.
func domainOf(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.Trim(addr, "<>")
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		return normalizeDomain(addr[i+1:])
	}
	return ""
}
