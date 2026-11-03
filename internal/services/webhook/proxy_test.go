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

// Proxy-mode tests for the pinned proxy dialing layer (proxydial.go) and the
// dispatcher's ProxyAckPolicy gate.
//
// Network discipline: every test is fully offline. Proxies (HTTP CONNECT and
// SOCKS5) are hand-rolled IN THIS PROCESS on loopback listeners; the real
// targets are loopback httptest servers; resolution is always a stub or the
// in-package sequence resolver. The public-looking address literal
// (proxyTestPublicIP) is only ever used as the CONNECT / SOCKS5 / absolute-form
// authority that the pinned transport hands to the PROXY — the in-process
// proxies deliberately ignore that authority and tunnel to a loopback listener,
// so no packet can ever leave the machine. Destination hostnames use the
// reserved ".example" TLD, which cannot resolve publicly.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// proxyTestPublicIP is the public-looking destination address the stub
// resolvers answer with. The in-process proxies never actually connect to it:
// they record the authority and tunnel to a loopback listener instead.
const proxyTestPublicIP = "93.184.216.34"

// ---------- in-test PKI ----------

// proxyTestPKI is a minimal in-test certificate authority: one ECDSA P-256 CA
// plus one leaf certificate per requested hostname. Everything is generated in
// the test process; nothing touches the network or the filesystem.
type proxyTestPKI struct {
	roots *x509.CertPool
	leafs map[string]tls.Certificate
}

func newProxyTestPKI(t *testing.T, names ...string) *proxyTestPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "proxy test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	p := &proxyTestPKI{roots: x509.NewCertPool(), leafs: make(map[string]tls.Certificate, len(names))}
	p.roots.AddCert(caCert)
	for i, name := range names {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("leaf key %s: %v", name, err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(int64(i + 2)),
			Subject:      pkix.Name{CommonName: name},
			DNSNames:     []string{name},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("leaf cert %s: %v", name, err)
		}
		p.leafs[name] = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	return p
}

// startProxyTestTLSTarget starts a loopback HTTPS server serving the given
// leaf certificate, recording hits and (when non-nil) the TLS SNI value.
func startProxyTestTLSTarget(t *testing.T, leaf tls.Certificate, hits *atomic.Int32, sni *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if sni != nil && r.TLS != nil {
			sni.Store(r.TLS.ServerName)
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// portOnly returns the port of a host:port literal.
func portOnly(hostport string) string {
	_, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return ""
	}
	return port
}

// ---------- in-process HTTP proxy (CONNECT tunneling + plain forwarding) ----------

// testHTTPProxy is a hand-rolled in-process HTTP proxy:
//
//   - CONNECT requests: the authority is recorded, a 200 reply is written, the
//     connection hijacked and raw-piped to tunnel (the authority is IGNORED —
//     tests use it only to assert what the pinned transport sent).
//   - plain (absolute-form) requests: the request URI and Host header are
//     recorded, then the request is forwarded to tunnel.
type testHTTPProxy struct {
	tunnel string

	mu         sync.Mutex
	connects   []string // recorded CONNECT authorities
	plainURIs  []string // recorded absolute-form request URIs
	plainHosts []string // recorded Host headers
}

func (p *testHTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.mu.Lock()
		p.connects = append(p.connects, r.Host)
		p.mu.Unlock()
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		client, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = client.Close() }()
		if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			return
		}
		upstream, err := net.DialTimeout("tcp", p.tunnel, 5*time.Second)
		if err != nil {
			return
		}
		defer func() { _ = upstream.Close() }()
		pipeBoth(client, upstream)
		return
	}

	p.mu.Lock()
	p.plainURIs = append(p.plainURIs, r.RequestURI)
	p.plainHosts = append(p.plainHosts, r.Host)
	p.mu.Unlock()

	// Forward to the tunnel target, preserving the original Host header so the
	// origin sees exactly what the pinned transport kept.
	out, err := http.NewRequest(r.Method, "http://"+p.tunnel+r.URL.RequestURI(), r.Body)
	if err != nil {
		http.Error(w, "bad forward", http.StatusBadGateway)
		return
	}
	out.Host = r.Host
	for k, vv := range r.Header {
		switch strings.ToLower(k) {
		case "connection", "proxy-connection", "keep-alive", "te", "trailer", "upgrade":
			continue
		}
		out.Header[k] = append([]string(nil), vv...)
	}
	resp, err := (&http.Transport{}).RoundTrip(out)
	if err != nil {
		http.Error(w, "forward failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *testHTTPProxy) connectAuthorities() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.connects...)
}

func (p *testHTTPProxy) plainRequests() (uris, hosts []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.plainURIs...), append([]string(nil), p.plainHosts...)
}

func startTestHTTPProxy(t *testing.T, tunnel string) (*testHTTPProxy, string) {
	t.Helper()
	p := &testHTTPProxy{tunnel: tunnel}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return p, srv.URL
}

// pipeBoth copies both directions between the two connections until one side
// ends, then closes both, which unblocks the other direction.
func pipeBoth(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(a, b)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(b, a)
		done <- struct{}{}
	}()
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}

// ---------- in-process SOCKS5 proxy ----------

// testSOCKS5Proxy is a hand-rolled in-process SOCKS5 server (no-auth only).
// It records the connect request's address-type and target for every CONNECT
// command and tunnels the connection to tunnel (the recorded target is
// IGNORED — tests use it only to assert what the pinned dialer sent).
type testSOCKS5Proxy struct {
	tunnel string

	mu      sync.Mutex
	atyps   []byte
	targets []string
	conns   int

	ln net.Listener
}

func startTestSOCKS5Proxy(t *testing.T, tunnel string) *testSOCKS5Proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("socks5 proxy listen: %v", err)
	}
	s := &testSOCKS5Proxy{tunnel: tunnel, ln: ln}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *testSOCKS5Proxy) addr() string { return s.ln.Addr().String() }

// seen returns the recorded connect requests as (atyp, target) pairs.
func (s *testSOCKS5Proxy) seen() (atyps []byte, targets []string, conns int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.atyps...), append([]string(nil), s.targets...), s.conns
}

func (s *testSOCKS5Proxy) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *testSOCKS5Proxy) handle(c net.Conn) {
	defer func() { _ = c.Close() }()

	// Greeting: ver, nmethods, methods. Answer: no-auth required.
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	if head[0] != 5 {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}

	// Connect request: ver, cmd, rsv, atyp, addr, port.
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return
	}
	if hdr[0] != 5 || hdr[1] != 1 { // only CONNECT
		return
	}
	var host string
	switch hdr[3] {
	case 1: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3: // domain
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = string(b)
	case 4: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb))))

	s.mu.Lock()
	s.atyps = append(s.atyps, hdr[3])
	s.targets = append(s.targets, target)
	s.conns++
	s.mu.Unlock()

	// Success reply: ver, rep=0, rsv, atyp=1, BND.ADDR 0.0.0.0, BND.PORT 0.
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	upstream, err := net.DialTimeout("tcp", s.tunnel, 5*time.Second)
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()
	pipeBoth(c, upstream)
}

// ---------- pinned round tripper: HTTPS via CONNECT ----------

// TestPinnedProxyRT_HTTPSConnectAuthorityIsPinnedIP proves, over a real
// in-process CONNECT tunnel, that the pinned transport asks the proxy for the
// VALIDATED PUBLIC IP (never the hostname) while TLS keeps hostname-based
// SNI and certificate verification: a cert matching the URL host succeeds and
// reports the original SNI, a wrong-name certificate fails with an x509
// error — with verification on, the IP-form rewrite does not touch it.
func TestPinnedProxyRT_HTTPSConnectAuthorityIsPinnedIP(t *testing.T) {
	pki := newProxyTestPKI(t, "trusted.example", "other.example")

	cases := []struct {
		name    string
		leaf    string // leaf served by the tunneled target
		wantErr bool
	}{
		{"trusted name", "trusted.example", false},
		{"wrong name certificate", "other.example", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			var sni atomic.Value
			srv := startProxyTestTLSTarget(t, pki.leafs[tc.leaf], &hits, &sni)
			dstPort := portOnly(srv.Listener.Addr().String())

			p, proxyURL := startTestHTTPProxy(t, srv.Listener.Addr().String())
			pu, err := url.Parse(proxyURL)
			if err != nil {
				t.Fatalf("parse proxy URL: %v", err)
			}
			res := &stubResolver{addrs: []netip.Addr{ip(t, proxyTestPublicIP)}}
			rt := newPinnedProxyRoundTripper(pu, res)
			pinned := rt.(*pinnedProxyRoundTripper)
			pinned.tlsConfig = &tls.Config{RootCAs: pki.roots, MinVersion: tls.VersionTLS12}
			client := &http.Client{Timeout: 10 * time.Second, Transport: rt}

			raw := "https://trusted.example:" + dstPort + "/hook"
			resp, err := client.Get(raw)
			if tc.wantErr {
				if err == nil {
					_ = resp.Body.Close()
					t.Fatal("wrong-name certificate was accepted; hostname verification is broken")
				}
				low := strings.ToLower(err.Error())
				if !strings.Contains(low, "certificate") && !strings.Contains(low, "x509") && !strings.Contains(low, "tls") {
					t.Fatalf("error %v is not a TLS/certificate failure", err)
				}
				if got := hits.Load(); got != 0 {
					t.Fatalf("target saw %d requests; the handshake must fail before any HTTP request", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("GET through pinned CONNECT proxy: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if got := sni.Load(); got != "trusted.example" {
				t.Fatalf("SNI = %v, want trusted.example (original hostname preserved)", got)
			}
			want := proxyTestPublicIP + ":" + dstPort
			if seen := p.connectAuthorities(); len(seen) != 1 || seen[0] != want {
				t.Fatalf("CONNECT authorities = %v, want [%s] (pinned IP form, never the hostname)", seen, want)
			}
		})
	}
}

// ---------- pinned round tripper: rebind closed before any CONNECT ----------

// TestPinnedProxyRT_RebindRejectedBeforeConnect proves the proxy-mode TOCTOU
// is closed: the first request (public answer) tunnels through CONNECT to the
// public IP, and the second request — whose resolution flipped to loopback —
// is rejected by policy BEFORE any CONNECT is issued and before any bytes
// reach the proxy or the target.
func TestPinnedProxyRT_RebindRejectedBeforeConnect(t *testing.T) {
	pki := newProxyTestPKI(t, "rebind.example")
	var hits atomic.Int32
	srv := startProxyTestTLSTarget(t, pki.leafs["rebind.example"], &hits, nil)
	dstPort := portOnly(srv.Listener.Addr().String())

	p, proxyURL := startTestHTTPProxy(t, srv.Listener.Addr().String())
	pu, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}

	// First resolution answers a public address, the next one flips to
	// loopback (attacker's rebinding DNS). auditSeqResolver holds the last
	// plan entry, so every subsequent lookup keeps answering loopback.
	seq := &auditSeqResolver{plan: [][]netip.Addr{
		{ip(t, proxyTestPublicIP)},
		{ip(t, "127.0.0.1")},
	}}
	rt := newPinnedProxyRoundTripper(pu, seq)
	pinned := rt.(*pinnedProxyRoundTripper)
	pinned.tlsConfig = &tls.Config{RootCAs: pki.roots, MinVersion: tls.VersionTLS12}
	client := &http.Client{Timeout: 10 * time.Second, Transport: rt}

	raw := "https://rebind.example:" + dstPort + "/hook"
	resp, err := client.Get(raw)
	if err != nil {
		t.Fatalf("first request through pinned proxy: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", resp.StatusCode)
	}
	want := proxyTestPublicIP + ":" + dstPort
	if seen := p.connectAuthorities(); len(seen) != 1 || seen[0] != want {
		t.Fatalf("first request CONNECT authorities = %v, want [%s]", seen, want)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("target hits after first request = %d, want 1", got)
	}

	// Rebound request: refused by policy before any CONNECT.
	resp2, err2 := client.Get(raw)
	if err2 == nil {
		_ = resp2.Body.Close()
		t.Fatal("rebound request succeeded, want ErrPolicyBlocked")
	}
	if !errors.Is(err2, ErrPolicyBlocked) {
		t.Fatalf("rebound request err = %v, want ErrPolicyBlocked", err2)
	}
	if seen := p.connectAuthorities(); len(seen) != 1 || seen[0] != want {
		t.Fatalf("after rebinding CONNECT authorities = %v; no second CONNECT may be issued", seen)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("target hits after rebinding = %d, want 1 (rebound request must not connect)", got)
	}
	if got := seq.callCount(); got != 2 {
		t.Fatalf("resolver called %d times across two requests, want 2 (fresh resolution per request)", got)
	}
}

// ---------- dispatcher in proxy mode: redirects ----------

// TestProxyModeRedirectsTerminateDelivery reuses the audit redirect table with
// the production dispatcher running in proxy mode (ack accepted, pinned
// transport active): each 3xx must be the FINAL recorded response and the hop
// must never be followed.
func TestProxyModeRedirectsTerminateDelivery(t *testing.T) {
	codes := []int{
		http.StatusMovedPermanently,  // 301
		http.StatusFound,             // 302
		http.StatusSeeOther,          // 303
		http.StatusTemporaryRedirect, // 307
		http.StatusPermanentRedirect, // 308
	}

	for _, code := range codes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var hop2Hits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hop2Hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()

			var sourceHits atomic.Int32
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceHits.Add(1)
				http.Redirect(w, r, target.URL+"/hop2", code)
			}))
			defer source.Close()

			// The proxy forwards plain HTTP to the source; the dispatcher
			// talks to the source through it, pinned to the public IP form.
			p, proxyURL := startTestHTTPProxy(t, source.Listener.Addr().String())

			d := NewDispatcher(nil)
			d.urlPolicy = func(string) error { return nil }
			d.resolver = &stubResolver{addrs: []netip.Addr{ip(t, proxyTestPublicIP)}}
			d.SetConfig(Config{ProxyURL: proxyURL, ProxyAckPolicy: proxyAckEnforced})
			if !d.proxyDial {
				t.Fatal("test setup: proxy mode not enabled")
			}
			if _, ok := d.client.Transport.(*pinnedProxyRoundTripper); !ok {
				t.Fatalf("proxy-mode transport is %T, want *pinnedProxyRoundTripper", d.client.Transport)
			}

			dstPort := portOnly(source.Listener.Addr().String())
			status, _, err := d.post("http://trusted.example:"+dstPort+"/start", "secret", []byte(`{"event":"proxy-redirect"}`))
			if err != nil {
				t.Fatalf("post error: %v", err)
			}
			if status != code {
				t.Fatalf("final status = %d, want %d (the 3xx itself must be the final response)", status, code)
			}
			if got := sourceHits.Load(); got != 1 {
				t.Fatalf("source hits = %d, want 1", got)
			}
			if got := hop2Hits.Load(); got != 0 {
				t.Fatalf("hop-2 target received %d requests, want 0 — redirect must not be followed", got)
			}
			want := "http://" + proxyTestPublicIP + ":" + dstPort + "/start"
			if uris, _ := p.plainRequests(); len(uris) != 1 || uris[0] != want {
				t.Fatalf("proxied request URIs = %v, want [%s] (pinned IP form)", uris, want)
			}
		})
	}
}

// ---------- pinned round tripper: plain HTTP ----------

// TestPinnedProxyRT_PlainHTTPTargetsPinnedIP proves the request-rewrite form
// for plain HTTP: the absolute-form request URI the proxy receives carries the
// validated public IP as its authority — the proxy must never be handed the
// hostname, or it would resolve DNS on its side and reopen the rebinding
// window. The Host header carries the pinned IP as well: the stdlib derives
// the proxied request-line authority and the Host header from the same field
// (Request.write prefers req.Host), so keeping the original hostname in the
// Host header would unpin the forwarding target. HTTPS destinations keep
// their hostname-based Host header inside the CONNECT tunnel.
func TestPinnedProxyRT_PlainHTTPTargetsPinnedIP(t *testing.T) {
	var originHost atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHost.Store(r.Host)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	p, proxyURL := startTestHTTPProxy(t, target.Listener.Addr().String())
	pu, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	rt := newPinnedProxyRoundTripper(pu, &stubResolver{addrs: []netip.Addr{ip(t, proxyTestPublicIP)}})
	client := &http.Client{Timeout: 10 * time.Second, Transport: rt}

	dstPort := portOnly(target.Listener.Addr().String())
	resp, err := client.Get("http://trusted.example:" + dstPort + "/hook")
	if err != nil {
		t.Fatalf("GET through pinned proxy: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	uris, hosts := p.plainRequests()
	want := proxyTestPublicIP + ":" + dstPort
	if len(uris) != 1 || uris[0] != "http://"+want+"/hook" {
		t.Fatalf("proxied request URIs = %v, want [http://%s/hook] (authority must be the validated IP)", uris, want)
	}
	if len(hosts) != 1 || hosts[0] != want {
		t.Fatalf("proxied Host headers = %v, want [%s] (stdlib couples the Host header to the pinned authority)", hosts, want)
	}
	if got := originHost.Load(); got != want {
		t.Fatalf("origin saw Host %v, want %q", got, want)
	}
}

// ---------- pinned SOCKS5 dialer ----------

// TestPinnedSOCKS5_ConnectsToValidatedIPAndRebindClosed drives the production
// dispatcher through an in-process SOCKS5 proxy: the connect request must
// carry the validated public IP in ATYP=1 form (the proxy never sees a
// hostname), and a rebound resolution must be rejected before a second
// connect request is sent.
func TestPinnedSOCKS5_ConnectsToValidatedIPAndRebindClosed(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	s5 := startTestSOCKS5Proxy(t, target.Listener.Addr().String())

	res := &stubResolver{addrs: []netip.Addr{ip(t, proxyTestPublicIP)}}
	d := NewDispatcher(nil)
	d.urlPolicy = func(string) error { return nil }
	d.resolver = res
	dstPort := portOnly(target.Listener.Addr().String())
	d.SetConfig(Config{
		ProxyURL:       "socks5://" + s5.addr(),
		ProxyAckPolicy: proxyAckEnforced,
	})
	if !d.proxyDial {
		t.Fatal("test setup: proxy mode not enabled")
	}
	if _, ok := d.client.Transport.(*http.Transport); !ok {
		t.Fatalf("socks5 proxy transport is %T, want *http.Transport", d.client.Transport)
	}

	status, _, err := d.post("http://trusted.example:"+dstPort+"/hook", "secret", []byte(`{"event":"proxy-socks5"}`))
	if err != nil {
		t.Fatalf("post through pinned SOCKS5 proxy: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	atyps, targets, conns := s5.seen()
	if conns != 1 {
		t.Fatalf("socks5 proxy handled %d connect requests, want 1", conns)
	}
	if len(atyps) != 1 || atyps[0] != 1 {
		t.Fatalf("recorded ATYPs = %v, want [1] (IP-literal form, never a domain name)", atyps)
	}
	want := proxyTestPublicIP + ":" + dstPort
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("recorded SOCKS5 targets = %v, want [%s]", targets, want)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("target hits = %d, want 1", got)
	}

	// Rebind: the answer flips to loopback; the pinned dialer must refuse it
	// during validation, before any second connect request reaches the proxy.
	res.addrs = []netip.Addr{ip(t, "127.0.0.1")}
	_, _, err = d.post("http://trusted.example:"+dstPort+"/hook", "secret", []byte(`{"event":"proxy-socks5"}`))
	if !errors.Is(err, ErrPolicyBlocked) {
		t.Fatalf("rebound post err = %v, want ErrPolicyBlocked", err)
	}
	_, targets, conns = s5.seen()
	if conns != 1 || len(targets) != 1 {
		t.Fatalf("socks5 proxy saw %d connect requests after rebinding, want 1 (no second connect)", conns)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("target hits after rebinding = %d, want 1", got)
	}
}

// ---------- ProxyAckPolicy gate ----------

// TestProxyAckPolicyGatesProxyMode pins the acknowledgement contract: a
// configured proxy is IGNORED unless ProxyAckPolicy is "enforced", the
// guarded direct-mode transport stays intact when ignored, "enforced" swaps
// the transport for the pinned one, and malformed inputs fail safe.
func TestProxyAckPolicyGatesProxyMode(t *testing.T) {
	t.Run("proxy URL without ack stays guarded direct", func(t *testing.T) {
		d := NewDispatcher(nil)
		d.SetConfig(Config{ProxyURL: "http://127.0.0.1:3128"})
		if d.proxyDial {
			t.Fatal("proxy mode must not activate without ProxyAckPolicy=enforced")
		}
		tr, ok := d.client.Transport.(*http.Transport)
		if !ok || tr.DialContext == nil || tr.Proxy != nil {
			t.Fatalf("guarded transport must stay intact, got %#v", d.client.Transport)
		}
	})

	t.Run("ack enforced activates http proxy mode", func(t *testing.T) {
		d := NewDispatcher(nil)
		d.SetConfig(Config{ProxyURL: "http://127.0.0.1:3128", ProxyAckPolicy: proxyAckEnforced})
		if !d.proxyDial {
			t.Fatal("proxy mode must activate with ProxyAckPolicy=enforced")
		}
		pinned, ok := d.client.Transport.(*pinnedProxyRoundTripper)
		if !ok {
			t.Fatalf("proxy-mode transport is %T, want *pinnedProxyRoundTripper", d.client.Transport)
		}
		if pinned.proxyURL.Host != "127.0.0.1:3128" {
			t.Fatalf("pinned transport proxy = %s, want 127.0.0.1:3128", pinned.proxyURL.Host)
		}
	})

	t.Run("ack enforced activates socks5 proxy mode", func(t *testing.T) {
		d := NewDispatcher(nil)
		d.SetConfig(Config{ProxyURL: "socks5://127.0.0.1:1080", ProxyAckPolicy: proxyAckEnforced})
		if !d.proxyDial {
			t.Fatal("proxy mode must activate with ProxyAckPolicy=enforced")
		}
		tr, ok := d.client.Transport.(*http.Transport)
		if !ok || tr.DialContext == nil {
			t.Fatalf("socks5 proxy transport must carry the pinned dialer, got %#v", d.client.Transport)
		}
	})

	t.Run("other ack values stay guarded direct", func(t *testing.T) {
		for _, ack := range []string{"", "Enforced", "enforced ", "true", "1"} {
			d := NewDispatcher(nil)
			d.SetConfig(Config{ProxyURL: "http://127.0.0.1:3128", ProxyAckPolicy: ack})
			if d.proxyDial {
				t.Fatalf("ack value %q must not enable proxy mode", ack)
			}
		}
	})

	t.Run("invalid proxy URL fails safe even with ack", func(t *testing.T) {
		d := NewDispatcher(nil)
		d.SetConfig(Config{ProxyURL: "http://[::bad", ProxyAckPolicy: proxyAckEnforced})
		if d.proxyDial {
			t.Fatal("invalid proxy URL must not switch the dispatcher into proxy mode")
		}
		tr, ok := d.client.Transport.(*http.Transport)
		if !ok || tr.DialContext == nil || tr.Proxy != nil {
			t.Fatalf("guarded transport must stay intact, got %#v", d.client.Transport)
		}
	})

	t.Run("unsupported scheme stays guarded direct", func(t *testing.T) {
		d := NewDispatcher(nil)
		d.SetConfig(Config{ProxyURL: "ftp://127.0.0.1:21", ProxyAckPolicy: proxyAckEnforced})
		if d.proxyDial {
			t.Fatal("unsupported proxy scheme must not switch the dispatcher into proxy mode")
		}
	})
}
