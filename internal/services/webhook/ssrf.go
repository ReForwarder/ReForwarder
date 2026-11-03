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

package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// The SSRF guard. Webhook destinations are user-supplied, so without it the
// delivery system can be used to probe localhost, the internal network and
// cloud metadata endpoints (upstream issue goposta/posta#282). The policy is
// "public, globally-routable unicast only" and it is enforced in layers:
//
//  1. ValidateWebhookURL — static, network-free checks; applied when a webhook
//     is created and again before every delivery (legacy stored webhooks are
//     re-checked, never trusted because they were accepted before).
//  2. ResolvePublicHost — resolves the hostname and rejects the whole answer
//     if any address is non-public; applied at creation and before proxied
//     deliveries.
//  3. The guarded transport's DialContext — resolves, validates and dials a
//     *validated IP literal* on every connection, so DNS rebinding between
//     validation and connection cannot slip through; retries resolve afresh.
//     A Control hook re-checks the raw socket address just before connect.
//
// All user-facing errors are sanitized: they never echo resolved addresses,
// ports or resolver internals. Raw transport errors stay in server logs only.

// ErrPolicyBlocked marks a destination rejected by the SSRF policy. The
// dispatcher treats it as deterministic: fail fast, no retry.
var ErrPolicyBlocked = errors.New("webhook destination blocked by security policy")

// Resolver resolves hostnames to IP addresses. *net.Resolver satisfies it;
// tests inject a stub so resolution is controlled without touching the
// network.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Dial timeout of the guarded dialer. A variable (not a constant) so tests can
// shorten it; production code never overrides it.
var guardDialTimeout = 10 * time.Second

// blockedV4 are the IPv4 ranges beyond netip's own classifications that are
// not globally routable (IANA special-purpose registry): "this network",
// CGNAT shared space, IETF protocol assignments, TEST-NETs, deprecated 6to4
// relays, benchmarking, and reserved space including broadcast.
var blockedV4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network" (0.0.0.0 and friends)
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT shared address space
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("192.88.99.0/24"),  // deprecated 6to4 relay anycast
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, incl. broadcast
}

// blockedV6 covers the documentation range; loopback, unspecified, ULA,
// link-local and multicast are caught by netip's classifications.
var blockedV6 = []netip.Prefix{
	netip.MustParsePrefix("2001:db8::/32"), // documentation
}

// ValidateWebhookURL is the static, network-free half of the policy: scheme,
// host presence, embedded credentials, port, obvious local names, and — when
// the host is an IP literal — the address policy. A hostname that passes here
// still needs ResolvePublicHost (hostname → private IP is the classic bypass),
// and every connection is re-checked by the guarded dialer regardless.
func ValidateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("webhook URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("webhook URL is malformed")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook URL scheme must be http or https")
	}
	if u.User != nil {
		return fmt.Errorf("webhook URL must not contain credentials")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("webhook URL has no host")
	}
	if p := u.Port(); p != "" && !isDecimalPort(p) {
		return fmt.Errorf("webhook URL has an invalid port")
	}

	// Hostnames that can never leave the machine; resolving them is pointless.
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("%w: destination is not a public address", ErrPolicyBlocked)
	}

	// An IP literal is judged here without any DNS round trip.
	if ip, perr := netip.ParseAddr(host); perr == nil {
		if verr := validateIPAddr(ip); verr != nil {
			return verr
		}
	}
	return nil
}

// isDecimalPort reports whether s is a decimal port number in range.
func isDecimalPort(s string) bool {
	if len(s) == 0 || len(s) > 5 {
		return false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
	}
	return n > 0 && n <= 65535
}

// validateIPAddr enforces the destination policy on one address: public,
// globally-routable unicast only. IPv4-mapped IPv6 is unmapped first so the
// v4 rules apply to it. The error never echoes the address, so it is safe to
// surface through the API.
func validateIPAddr(addr netip.Addr) error {
	if !addr.IsValid() {
		return fmt.Errorf("%w: destination is not a usable address", ErrPolicyBlocked)
	}
	a := addr.Unmap()
	if a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return fmt.Errorf("%w: destination is not a public address", ErrPolicyBlocked)
	}
	if a.Is4() {
		for _, rng := range blockedV4 {
			if rng.Contains(a) {
				return fmt.Errorf("%w: destination is not a public address", ErrPolicyBlocked)
			}
		}
	} else {
		for _, rng := range blockedV6 {
			if rng.Contains(a) {
				return fmt.Errorf("%w: destination is not a public address", ErrPolicyBlocked)
			}
		}
	}
	return nil
}

// ResolvePublicHost resolves host and enforces the policy on every returned
// address. A mixed answer — any non-public address among public ones — is
// rejected wholesale (fail closed; there is no per-address fallback for an
// attacker to hide behind). IP literals short-circuit to the address policy.
// Callers may dial the returned addresses directly.
func ResolvePublicHost(ctx context.Context, res Resolver, host string) ([]netip.Addr, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("%w: destination host is empty", ErrPolicyBlocked)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if verr := validateIPAddr(ip); verr != nil {
			return nil, verr
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	if res == nil {
		res = net.DefaultResolver
	}
	addrs, err := res.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		// The resolver's error text may carry infrastructure detail; drop it.
		// This is deliberately NOT ErrPolicyBlocked: DNS hiccups are
		// transient and retrying is safe — every retry re-resolves and
		// re-validates.
		return nil, fmt.Errorf("webhook destination could not be resolved")
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			return nil, fmt.Errorf("%w: destination is not a usable address", ErrPolicyBlocked)
		}
		if verr := validateIPAddr(ip); verr != nil {
			return nil, verr
		}
		out = append(out, ip.Unmap())
	}
	return out, nil
}

// ResolvePublicURL validates raw and resolves its host in one step.
func ResolvePublicURL(ctx context.Context, res Resolver, raw string) ([]netip.Addr, error) {
	if err := ValidateWebhookURL(raw); err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("webhook URL is malformed")
	}
	return ResolvePublicHost(ctx, res, u.Hostname())
}

// NewGuardedTransport builds the hardened transport used for direct webhook
// deliveries: every connection resolves through res, every resolved address
// is policy-checked, and the connection is pinned to a validated IP literal —
// http.Transport keeps the URL's hostname for TLS SNI and certificate
// verification, so certificate validation is untouched. Environment proxies
// are intentionally not honored (Proxy: nil): webhook egress is direct or via
// the explicitly configured proxy URL, never via ambient HTTP_PROXY.
func NewGuardedTransport(res Resolver) *http.Transport {
	d := &net.Dialer{
		Timeout:   guardDialTimeout,
		KeepAlive: 30 * time.Second,
		Control:   controlPublicAddr,
	}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           guardedDialContext(d, res),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
}

// guardedDialContext wraps a dialer with resolution and policy enforcement.
// The hostname is resolved and validated here; the connection is made to a
// validated IP literal, so no second, unvalidated resolution can happen
// between check and use. Every new connection re-resolves, which is what
// makes retries immune to DNS rebinding.
func guardedDialContext(d *net.Dialer, res Resolver) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed dial address", ErrPolicyBlocked)
		}
		addrs, err := ResolvePublicHost(ctx, res, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, a := range addrs {
			conn, dialErr := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no validated address could be dialed")
		}
		return nil, lastErr
	}
}

// controlPublicAddr is the net.Dialer Control hook: it re-checks the raw
// socket address immediately before connect, so even a bug in the resolution
// path cannot open a connection to a non-public destination.
func controlPublicAddr(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: malformed socket address", ErrPolicyBlocked)
	}
	ip, perr := netip.ParseAddr(host)
	if perr != nil {
		return fmt.Errorf("%w: socket address is not an IP", ErrPolicyBlocked)
	}
	return validateIPAddr(ip)
}

// canonical delivery error texts. These are the ONLY free-text delivery
// errors the API may return; everything else collapses into
// genericDeliveryFailure. HTTP status codes are recorded separately and are
// kept as "HTTP <code>".
const (
	msgBlocked = "webhook destination blocked by security policy"
	msgResolve = "webhook destination could not be resolved"
	msgConn    = "connection failed"
	msgTimeout = "delivery timed out"
	msgTLS     = "secure connection failed"
	msgGeneric = "webhook delivery failed"
)

// SanitizeDeliveryError maps any raw error text — including rows written
// before the guard existed, which can contain internal IPs, ports and
// resolver detail — to a message that is safe to expose through the API.
// Raw detail stays in server logs only.
func SanitizeDeliveryError(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)
	// The dispatcher's own "HTTP <code>" status notation is safe and useful;
	// keep it verbatim.
	if strings.HasPrefix(low, "http ") && isHTTPStatusSuffix(s[len("http "):]) {
		return s
	}
	switch {
	case strings.Contains(low, "blocked by security policy"):
		return msgBlocked
	case strings.Contains(low, "could not be resolved"),
		strings.Contains(low, "no such host"),
		strings.Contains(low, "lookup "),
		strings.Contains(low, "did not resolve"):
		return msgResolve
	case strings.Contains(low, "timeout"),
		strings.Contains(low, "deadline exceeded"):
		return msgTimeout
	case strings.Contains(low, "tls"),
		strings.Contains(low, "x509"),
		strings.Contains(low, "certificate"):
		return msgTLS
	case strings.Contains(low, "dial"),
		strings.Contains(low, "connect"),
		strings.Contains(low, "no route"),
		strings.Contains(low, "unreachable"),
		strings.Contains(low, "refused"),
		strings.Contains(low, "reset"),
		strings.Contains(low, "broken pipe"),
		strings.Contains(low, "proxy"),
		strings.Contains(low, "socks"):
		return msgConn
	default:
		return msgGeneric
	}
}

// isHTTPStatusSuffix reports whether s is exactly a three-digit number (the
// tail of the dispatcher's "HTTP <code>" status notation).
func isHTTPStatusSuffix(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// LegacyAttemptBodyUnsafe reports whether a response body captured for a
// webhook with the given stored URL may have come from a non-public
// destination and should be redacted when delivery history is served.
// Hostname URLs that pass static validation are assumed safe: going forward
// every connection is pinned to a validated public address, so bodies can
// only be captured from destinations the policy allowed.
func LegacyAttemptBodyUnsafe(raw string) bool {
	if err := ValidateWebhookURL(raw); err != nil {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return true
	}
	if ip, perr := netip.ParseAddr(u.Hostname()); perr == nil {
		return validateIPAddr(ip) != nil
	}
	return false
}
