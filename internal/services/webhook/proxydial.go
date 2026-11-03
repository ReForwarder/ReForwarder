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
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Proxy-mode dialing.
//
// Direct deliveries are pinned at dial level by the guarded transport: the
// dial target is a validated IP literal. Through a proxy that is not enough —
// the dial target is the proxy itself (which may legitimately be internal) and
// a naive proxy transport forwards the HOSTNAME to the proxy, letting the
// proxy resolve DNS on its side. The resolution validated in the pre-flight
// and the address the proxy actually connects to are then two different
// lookups: the classic time-of-check/time-of-use window (rebinding) is open.
//
// The constructors here close it by pinning the destination. Every proxied
// connection — SOCKS5 connect request or HTTP CONNECT tunnel / absolute-form
// request — carries the VALIDATED PUBLIC IP, never the hostname:
//
//   - newPinnedSOCKS5Dialer resolves, policy-checks every address and dials
//     a validated IP literal through the proxy per connection.
//   - newPinnedProxyRoundTripper re-resolves per request (rebinding between
//     requests buys nothing) and rewrites the outbound request to target the
//     validated public IP, while the original hostname is preserved as the
//     Host header and as the TLS ServerName, so certificate verification
//     stays hostname-based and untouched.
//
// What is pinned: the proxy is asked to connect to the pre-validated public
// IP. The pre-flight-to-proxy TOCTOU is closed — a rebinding DNS answer can
// no longer change where the connection lands, because no name is ever
// handed to the proxy or resolved a second time.
//
// Split-horizon caveat: the pinned IP must be one the PROXY can reach. In a
// DNS-isolated (split-horizon) setup where only the operator's resolver maps
// the hostname to an internal address, the proxy cannot connect to the pinned
// public IP and delivery fails closed — such destinations are outside the
// public-destination policy anyway.
//
// Compatibility caveats:
//   - rare proxies enforce hostname-based ACL policies on their targets and
//     may refuse IP-form CONNECT/absolute-form targets (or classify them
//     differently). Deployments behind such proxies need an ACL entry for the
//     pinned IP form or must stay in direct mode.
//   - for plain-HTTP destinations the stdlib derives the proxied request-line
//     authority and the Host header from one field, so pinning the authority
//     necessarily pins the Host header too: plain-HTTP origins receive an
//     IP-form Host header. HTTPS destinations keep their hostname-based Host
//     header inside the CONNECT tunnel.
//
// Proxies themselves are exempt from the destination policy: the proxy is
// operator-chosen infrastructure and may legitimately sit on the internal
// network, so the dialer that reaches the PROXY is a plain hardened dialer,
// not the guarded one. The public-destination policy is applied to the
// webhook destination the proxy is asked to connect to.

// newPinnedSOCKS5Dialer builds a DialContext for SOCKS5 proxy mode. It
// resolves the destination host, rejects any non-public answer (ErrPolicyBlocked
// flows out — fail closed), and dials a validated IP literal through the
// proxy, so the proxy never resolves the name on its side. A nil res selects
// net.DefaultResolver.
func newPinnedSOCKS5Dialer(proxyURL *url.URL, res Resolver) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	auth := &proxy.Auth{}
	if proxyURL.User != nil {
		auth.User = proxyURL.User.Username()
		auth.Password, _ = proxyURL.User.Password()
	} else {
		auth = nil
	}
	dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, auth, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("failed to create SOCKS5 dialer: %w", err)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("SOCKS5 dialer does not support DialContext")
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed dial address", ErrPolicyBlocked)
		}
		addrs, err := ResolvePublicHost(ctx, res, host)
		if err != nil {
			return nil, err
		}
		// Dial each validated address in turn; x/net/proxy encodes an IP
		// literal as the SOCKS5 ATYP=1/ATYP=4 address form, so the proxy
		// connects to exactly the validated address without a lookup of its
		// own. The first successful connection wins; the last dial error (if
		// any) is returned.
		var lastErr error
		for _, a := range addrs {
			conn, dialErr := contextDialer.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no validated address could be dialed")
		}
		return nil, lastErr
	}, nil
}

// newPinnedProxyRoundTripper builds the HTTP/HTTPS proxy-mode transport. Per
// request it resolves req.URL's hostname with the REQUEST's context (a fresh,
// policy-checked resolution — rebinding between requests buys nothing), picks
// the first validated address, rewrites the outbound request's target to that
// IP literal, and dispatches through an inner *http.Transport configured to
// use the proxy. A failed dial to the rewritten target surfaces as an
// ordinary transport error; no fallback iteration is needed for the
// request-rewrite form.
func newPinnedProxyRoundTripper(proxyURL *url.URL, res Resolver) http.RoundTripper {
	return &pinnedProxyRoundTripper{
		proxyURL: proxyURL,
		res:      res,
		pool:     make(map[string]*http.Transport),
	}
}

// pinnedProxyRoundTripper pins proxied requests to the pre-validated public
// destination IP. See newPinnedProxyRoundTripper.
type pinnedProxyRoundTripper struct {
	proxyURL *url.URL
	res      Resolver
	// tlsConfig, when set (tests only in practice), is cloned into every
	// inner transport with ServerName replaced by the destination hostname.
	// Production leaves it nil (system roots, verification on).
	tlsConfig *tls.Config
	// pool holds one inner *http.Transport per destination hostname. The
	// split is required because the pinned rewrite makes the URL host an IP
	// literal: the inner transport must be told the ORIGINAL hostname (via
	// TLSClientConfig.ServerName) or TLS would use the IP for SNI and
	// certificate verification. Inner transports dial the PROXY with a plain
	// hardened dialer — proxies are operator infrastructure, may legitimately
	// be internal, and are therefore exempt from the destination policy; the
	// destination itself is pinned in the rewritten request.
	mu   sync.Mutex
	pool map[string]*http.Transport
}

// RoundTrip resolves, validates and pins the destination, then dispatches the
// rewritten request through the per-hostname inner transport.
func (p *pinnedProxyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: destination host is empty", ErrPolicyBlocked)
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == schemeHTTPS {
			port = "443"
		} else {
			port = "80"
		}
	}
	addrs, err := ResolvePublicHost(req.Context(), p.res, host)
	if err != nil {
		return nil, err
	}
	pinned := addrs[0]

	// Clone the request and pin the destination: the URL host becomes the
	// validated IP literal (scheme and path unchanged). Body and GetBody
	// references are kept intact by Clone.
	out := req.Clone(req.Context())
	out.URL.Host = net.JoinHostPort(pinned.String(), port)
	if u.Scheme == schemeHTTPS {
		// HTTPS goes through a CONNECT tunnel: the tunnel authority is the
		// rewritten (pinned) URL host, and the original host — including any
		// non-default port — is preserved as the Host header the origin sees.
		out.Host = u.Host
	} else {
		// Plain HTTP is forwarded by the proxy using the absolute-form request
		// URI, and the stdlib derives BOTH the request-line authority and the
		// Host header from the same field, preferring req.Host over req.URL.Host
		// (net/http Request.write). Leaving the original hostname in req.Host
		// would therefore hand the hostname back to the proxy and silently
		// reopen the rebinding window, so Host is cleared and the pinned IP
		// becomes the forwarding target. The cost: plain-HTTP origins see an
		// IP-form Host header. HTTPS destinations are unaffected.
		out.Host = ""
	}

	return p.transportFor(host).RoundTrip(out)
}

// transportFor returns the inner transport for hostname, creating it on first
// use. It connects to the proxy (plain hardened dialer) and keeps TLS
// hostname-based via ServerName — verification is never disabled.
func (p *pinnedProxyRoundTripper) transportFor(hostname string) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.pool[hostname]; ok {
		return t
	}
	tlsCfg := p.tlsConfig.Clone()
	if tlsCfg == nil {
		tlsCfg = &tls.Config{}
	}
	tlsCfg.ServerName = hostname
	t := &http.Transport{
		Proxy: http.ProxyURL(p.proxyURL),
		// The proxy is operator-chosen infrastructure and may legitimately be
		// internal, so this dialer is deliberately NOT the guarded one: the
		// public-destination policy applies to the webhook destination (pinned
		// in the rewritten request), never to the proxy itself.
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	p.pool[hostname] = t
	return t
}
