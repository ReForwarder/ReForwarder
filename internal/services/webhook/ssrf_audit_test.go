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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

// Independent adversarial security-audit tests for commit 5c2678a
// ("Block SSRF through webhook URLs"). Written by the auditor; production
// code and existing test files are untouched.
//
// Amended by the proxy-mode hardening (issue #6): the translation-prefix pin
// test flipped to hard assertions (prefixes now blocked), and the
// client-construction / proxy-mode tests were adapted to the pinned proxy
// transports and the ProxyAckPolicy gate. Assertions were strengthened, never
// weakened.
//
// Network discipline: every test uses httptest servers bound to loopback or
// stub resolvers. The tests that exercise a "public" address (8.8.8.8) do so
// ONLY through a net.Dialer Control hook that records the final socket
// address and returns an error BEFORE the connect(2) syscall — no packet
// leaves the process. The assertion is about the address the dialer was
// about to use, which is exactly the property under test. The TLS
// verification test uses an in-test PKI (ECDSA P-256 CA + leaf) served from a
// loopback listener with reserved ".example" hostnames, so it cannot touch
// real DNS or the Internet either.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// auditIPv4Re matches any dotted-quad IPv4 literal; sanitized user-facing
// text must never contain one.
var auditIPv4Re = regexp.MustCompile(`([0-9]{1,3}\.){3}[0-9]{1,3}`)

// auditAssertNoInfraDetail fails the test if s leaks an IPv4 literal or a
// known internal marker.
func auditAssertNoInfraDetail(t *testing.T, what, s string) {
	t.Helper()
	if m := auditIPv4Re.FindAllString(s, -1); len(m) > 0 {
		t.Errorf("%s leaks IPv4 literal(s) %v in %q", what, m, s)
	}
	for _, marker := range []string{"0x7f", "2130706433", "0177.", "meta-data", ":53", ":5432", ":3128"} {
		if strings.Contains(s, marker) {
			t.Errorf("%s leaks internal marker %q in %q", what, marker, s)
		}
	}
}

// ---------- 1. adversarial IP-literal encodings through the two-layer gate ----------

// TestAuditStaticGate_AdversarialIPEncodings pins the behavior of the static
// gate and the resolution layer for hosts a strict parser cannot judge: hex,
// decimal and octal IPv4 encodings, and trailing-dot forms. getaddrinfo-style
// resolvers accept all of them as loopback even though netip.ParseAddr
// rejects them, so the static gate may pass them as "hostnames" — but the
// resolution layer must then fail closed when the answer is non-public, and
// stay fail-closed (transient error, retryable) when the answer is unknown.
func TestAuditStaticGate_AdversarialIPEncodings(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		raw  string
	}{
		{"hex ipv4", "http://0x7f000001/hook"},
		{"hex ipv4 with port", "http://0x7f000001:8080/hook"},
		{"decimal ipv4", "http://2130706433/hook"},
		{"octal ipv4", "http://0177.0.0.1/hook"},
		{"mixed hex octet", "http://0x7f.0.0.1/hook"},
		{"trailing dot on ip", "http://127.0.0.1./hook"},
		{"trailing dot on localhost", "http://localhost./hook"},
		{"trailing dot on localhost with port", "http://localhost.:8080/hook"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Documented (logged, not asserted): the static gate cannot judge
			// these forms — netip.ParseAddr rejects them, so they pass as
			// hostnames. The defense is the resolution layer below.
			staticErr := ValidateWebhookURL(tc.raw)
			t.Logf("static gate verdict for %q: err=%v", tc.raw, staticErr)

			// (a) A resolver that answers with loopback (what getaddrinfo does
			// for these encodings) must be rejected as a policy block.
			res := &stubResolver{addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
			_, err := ResolvePublicURL(ctx, res, tc.raw)
			if !errors.Is(err, ErrPolicyBlocked) {
				t.Fatalf("ResolvePublicURL(%q) with loopback answer = %v, want ErrPolicyBlocked", tc.raw, err)
			}

			// (b) A resolver that cannot answer must fail closed with a
			// transient (non-policy) error — never an accidental allow.
			resEmpty := &stubResolver{addrs: []netip.Addr{}}
			_, err = ResolvePublicURL(ctx, resEmpty, tc.raw)
			if err == nil {
				t.Fatalf("ResolvePublicURL(%q) with empty answer = nil, want error", tc.raw)
			}
			if errors.Is(err, ErrPolicyBlocked) {
				t.Fatalf("empty answer treated as policy block for %q; must be transient", tc.raw)
			}
		})
	}
}

// TestAuditResolvePublicHost_AdversarialAnswers sweeps hostile DNS answers:
// v4-mapped v6 encodings of loopback/private space, link-local, CGNAT,
// documentation, reserved and IETF-protocol ranges, plus a mixed answer whose
// private member is a mapped form. Every one must be rejected wholesale.
func TestAuditResolvePublicHost_AdversarialAnswers(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		addrs []netip.Addr
	}{
		{"mapped loopback", []netip.Addr{netip.MustParseAddr("::ffff:127.0.0.1")}},
		{"mapped metadata", []netip.Addr{netip.MustParseAddr("::ffff:169.254.169.254")}},
		{"mapped private in mixed answer", []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("::ffff:10.0.0.1")}},
		{"link-local v6", []netip.Addr{netip.MustParseAddr("fe80::1")}},
		{"cgnat", []netip.Addr{netip.MustParseAddr("100.64.0.1")}},
		{"docs v6", []netip.Addr{netip.MustParseAddr("2001:db8::1")}},
		{"reserved v4", []netip.Addr{netip.MustParseAddr("240.1.2.3")}},
		{"ietf protocol range", []netip.Addr{netip.MustParseAddr("192.0.0.9")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &stubResolver{addrs: tc.addrs}
			_, err := ResolvePublicHost(ctx, res, "host.example")
			if !errors.Is(err, ErrPolicyBlocked) {
				t.Fatalf("ResolvePublicHost with answer %v = %v, want ErrPolicyBlocked", tc.addrs, err)
			}
		})
	}
}

// ---------- 2. pinned dial + time-of-check/time-of-use atomicity ----------

// auditAddrRecorder is a net.Dialer Control hook: it records every FINAL
// socket address the dialer is about to connect(2) to, then aborts the dial.
// The Control hook runs after socket creation and strictly before connect, so
// returning an error here means no packet is ever sent.
type auditAddrRecorder struct {
	mu     sync.Mutex
	addrs  []string
	aborts int
	err    error
}

func (r *auditAddrRecorder) hook(_ string, address string, _ syscall.RawConn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addrs = append(r.addrs, address)
	r.aborts++
	return r.err
}

func (r *auditAddrRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.addrs...)
}

// TestAuditGuardedDialer_DialsValidatedLiteralOnly proves, at the socket
// level, that the guarded dialer:
//  1. resolves exactly once per connection (no second lookup between check
//     and use — the TOCTOU window is closed),
//  2. dials the validated IP literal, not the hostname,
//  3. re-resolves fresh on the NEXT connection, and a rebinding answer is
//     then rejected by policy before any socket is opened.
func TestAuditGuardedDialer_DialsValidatedLiteralOnly(t *testing.T) {
	oldTimeout := guardDialTimeout
	guardDialTimeout = 250 * time.Millisecond
	t.Cleanup(func() { guardDialTimeout = oldTimeout })

	sentinel := errors.New("audit control hook: connect intentionally aborted before connect(2)")

	// Case A: one connection, one resolution, dial goes to the validated literal.
	res := &stubResolver{addrs: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	rec := &auditAddrRecorder{err: sentinel}
	dial := guardedDialContext(&net.Dialer{Control: rec.hook}, res)
	conn, err := dial(context.Background(), "tcp", "pin.test:443")
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("dial err = %v, want the Control-hook sentinel (the hook must fire pre-connect)", err)
	}
	if seen := rec.seen(); len(seen) != 1 || seen[0] != "8.8.8.8:443" {
		t.Fatalf("Control hook saw %v, want [8.8.8.8:443] (no hostname, no second resolution)", seen)
	}
	if res.calls.Load() != 1 {
		t.Fatalf("resolver called %d times for one connection, want exactly 1 (resolve→validate→dial is atomic w.r.t. DNS)", res.calls.Load())
	}

	// Case B: the attacker rebinds between connections. The first connection
	// is answered with a public address; the NEXT connection's answer flips
	// to loopback. The new connection must re-resolve (fresh answer) and be
	// rejected by policy BEFORE any socket exists for the loopback address.
	seq := &auditSeqResolver{plan: [][]netip.Addr{
		{netip.MustParseAddr("8.8.8.8")},
		{netip.MustParseAddr("127.0.0.1")},
	}}
	rec2 := &auditAddrRecorder{err: sentinel}
	dial2 := guardedDialContext(&net.Dialer{Control: rec2.hook}, seq)

	_, err = dial2(context.Background(), "tcp", "rebind.test:443")
	if !errors.Is(err, sentinel) {
		t.Fatalf("first connection err = %v, want sentinel", err)
	}
	if seen := rec2.seen(); len(seen) != 1 || seen[0] != "8.8.8.8:443" {
		t.Fatalf("first connection dialed %v, want [8.8.8.8:443]", seen)
	}

	_, err = dial2(context.Background(), "tcp", "rebind.test:443")
	if !errors.Is(err, ErrPolicyBlocked) {
		t.Fatalf("rebound second connection err = %v, want ErrPolicyBlocked", err)
	}
	// The loopback answer must never have reached a socket: only the public
	// literal was ever passed to Control.
	if seen := rec2.seen(); len(seen) != 1 || seen[0] != "8.8.8.8:443" {
		t.Fatalf("after rebinding, Control saw %v; loopback must never be dialed", seen)
	}
	if seq.callCount() != 2 {
		t.Fatalf("resolver called %d times across two connections, want 2 (fresh resolution per connection)", seq.callCount())
	}
}

// auditSeqResolver returns planned answers in order, holding the last one.
type auditSeqResolver struct {
	mu    sync.Mutex
	plan  [][]netip.Addr
	calls int
}

func (r *auditSeqResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.calls
	if i >= len(r.plan) {
		i = len(r.plan) - 1
	}
	r.calls++
	out := make([]net.IPAddr, 0, len(r.plan[i]))
	for _, a := range r.plan[i] {
		out = append(out, net.IPAddr{IP: a.AsSlice()})
	}
	return out, nil
}

func (r *auditSeqResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// ---------- 3. redirects: every 3xx code terminates delivery ----------

// TestAuditAllRedirectCodesTerminateDelivery is the mandated table test over
// 301/302/303/307/308: the dispatcher must record the 3xx itself as the final
// response and never follow the Location — regardless of method-preservation
// semantics (307/308) — and the hop-2 target must see zero requests. It runs
// against the production dispatcher, preserving NewDispatcher's
// CheckRedirect; only the transport is swapped so loopback httptest servers
// are reachable (the address policy is covered by the guarded-transport
// tests; redirect behavior lives on the client, not the transport).
func TestAuditAllRedirectCodesTerminateDelivery(t *testing.T) {
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

			// Production dispatcher: production client (CheckRedirect, timeout)
			// with only the transport swapped for loopback reachability.
			d := NewDispatcher(nil)
			d.urlPolicy = func(string) error { return nil }
			d.client.Transport = &http.Transport{}

			status, _, err := d.post(source.URL+"/start", "secret", []byte(`{"event":"audit"}`))
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
		})
	}
}

// TestAuditDispatcherClientCheckRedirectIsProduction asserts the client-level
// redirect policy of the production dispatcher directly, so the table test
// above cannot drift from what production actually ships.
func TestAuditDispatcherClientCheckRedirectIsProduction(t *testing.T) {
	d := NewDispatcher(nil)
	err := d.client.CheckRedirect(nil, nil)
	if !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("production CheckRedirect = %v, want http.ErrUseLastResponse", err)
	}
}

// ---------- 4. client/transport construction: guarded, TLS intact ----------

// TestAuditDispatcherClientIsGuardedByConstruction pins the production client
// wiring: direct mode uses only the guarded transport, no ambient proxy is
// honored, and certificate verification is nowhere disabled — in direct mode
// AND in proxy mode, where the pinned proxy transports keep hostname-based
// TLS (ServerName from the original hostname, verification on).
func TestAuditDispatcherClientIsGuardedByConstruction(t *testing.T) {
	d := NewDispatcher(nil)
	tr, ok := d.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("dispatcher transport is %T, want *http.Transport", d.client.Transport)
	}
	if tr.DialContext == nil {
		t.Fatal("guarded transport must carry a DialContext (the guarded dialer)")
	}
	if tr.Proxy != nil {
		t.Fatal("guarded transport must not use an ambient proxy (Proxy must be nil)")
	}
	auditAssertTLSVerificationOn(t, "dispatcher transport", tr.TLSClientConfig)

	gt := NewGuardedTransport(nil)
	if gt.Proxy != nil {
		t.Fatal("NewGuardedTransport must not use an ambient proxy")
	}
	auditAssertTLSVerificationOn(t, "NewGuardedTransport", gt.TLSClientConfig)

	pHTTP, err := pinnedProxyTransport(&url.URL{Scheme: "http", Host: "127.0.0.1:3128"}, nil)
	if err != nil {
		t.Fatalf("pinnedProxyTransport(http): %v", err)
	}
	pinned, ok := pHTTP.(*pinnedProxyRoundTripper)
	if !ok {
		t.Fatalf("http proxy transport is %T, want *pinnedProxyRoundTripper", pHTTP)
	}
	inner := pinned.transportFor("hooks.example")
	auditAssertTLSVerificationOn(t, "http proxy pinned transport", inner.TLSClientConfig)
	if inner.Proxy == nil {
		t.Fatal("http proxy transport must set Proxy on the inner transport")
	}
	if inner.TLSClientConfig.ServerName != "hooks.example" {
		t.Fatalf("inner transport ServerName = %q, want the original hostname", inner.TLSClientConfig.ServerName)
	}

	pSocks, err := pinnedProxyTransport(&url.URL{Scheme: "socks5", Host: "127.0.0.1:1080"}, nil)
	if err != nil {
		t.Fatalf("pinnedProxyTransport(socks5): %v", err)
	}
	sTr, ok := pSocks.(*http.Transport)
	if !ok {
		t.Fatalf("socks5 proxy transport is %T, want *http.Transport", pSocks)
	}
	if sTr.DialContext == nil {
		t.Fatal("socks5 proxy transport must carry a DialContext (the pinned SOCKS5 dialer)")
	}
	auditAssertTLSVerificationOn(t, "socks5 proxy transport", sTr.TLSClientConfig)

	// An invalid proxy URL must fail safe: stay in guarded direct mode.
	d2 := NewDispatcher(nil)
	d2.SetConfig(Config{ProxyURL: "http://[::bad"})
	if d2.proxyDial {
		t.Fatal("invalid proxy URL must not switch the dispatcher into proxy mode")
	}
	tr2, ok := d2.client.Transport.(*http.Transport)
	if !ok || tr2.DialContext == nil || tr2.Proxy != nil {
		t.Fatalf("after failed SetConfig the client must keep the guarded transport, got %#v", d2.client.Transport)
	}
}

func auditAssertTLSVerificationOn(t *testing.T, what string, cfg *tls.Config) {
	t.Helper()
	if cfg == nil {
		return // nil TLSClientConfig: stdlib defaults, verification ON
	}
	if cfg.InsecureSkipVerify {
		t.Fatalf("%s sets InsecureSkipVerify — certificate verification must stay on", what)
	}
}

// ---------- 5. TLS verification under literal dialing ----------

// TestAuditTLSVerificationSurvivesLiteralDial proves the property the pinned
// dialer depends on: dialing an IP literal while the URL carries a hostname
// keeps hostname-based SNI and certificate verification.
//
// Fully offline: the whole PKI is generated in the test (ECDSA P-256 CA +
// leaf for trusted.example), the TLS listener is loopback, and the dial target
// is pinned to the listener's literal. The hostnames use the reserved
// ".example" TLD, which cannot resolve publicly, so no DNS query and no
// packet can ever leave the machine.
//   - URL host covered by the leaf certificate (trusted.example) + local
//     literal dial → the request succeeds: ServerName came from the URL host.
//   - URL host NOT covered (other.example) + same literal dial → the
//     handshake is rejected with an x509 certificate error.
//
// The dial target is independent of verification, exactly as
// guardedDialContext produces (literal dial, URL-host verification).
func TestAuditTLSVerificationSurvivesLiteralDial(t *testing.T) {
	// ---- in-test PKI: CA + leaf for trusted.example ----
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "webhook audit test CA"},
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
	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "trusted.example"},
		DNSNames:     []string{"trusted.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	leaf := tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}

	// ---- loopback TLS listener serving ONLY the trusted.example leaf ----
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	literal := srv.Listener.Addr().String() // the local IP literal under test

	// Client wiring keeps the guarded transport's hardened shape (no ambient
	// proxy, handshake timeout, verification ON) with the in-test CA as root.
	// The dial is pinned to the local literal while the URL keeps its
	// hostname, exactly as guardedDialContext produces (literal dial,
	// URL-host verification). The guarded dialer itself refuses loopback by
	// design — TestAuditGuardedTransportBlocksLiveLocalTLSServer covers that
	// refusal — so the final-mile literal dial is made directly to the local
	// listener; SNI and certificate verification are transport-level behavior
	// and are what this test exercises.
	tr := NewGuardedTransport(nil)
	tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, literal)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: tr}

	// Hostname covered by the certificate: verification must pass even though
	// the connection was made to a different (literal) address.
	resp, err := client.Get("https://trusted.example/hook")
	if err != nil {
		t.Fatalf("GET https://trusted.example through literal dial failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Hostname NOT covered by the certificate: verification must fail — the
	// failure text must be a certificate/TLS error, not a connection error.
	resp2, err := client.Get("https://other.example/hook")
	if err == nil {
		_ = resp2.Body.Close()
		t.Fatal("wrong-name certificate was accepted; hostname verification is broken")
	}
	low := strings.ToLower(err.Error())
	if !strings.Contains(low, "certificate") && !strings.Contains(low, "x509") && !strings.Contains(low, "tls") {
		t.Fatalf("error %v is not a TLS/certificate failure", err)
	}
}

// TestAuditGuardedTransportBlocksLiveLocalTLSServer is the end-to-end
// fail-closed check for TLS destinations: a live local HTTPS endpoint
// (httptest TLS on loopback) reachable through a lying resolver must be
// refused by the address policy BEFORE any TLS handshake, with the server
// seeing nothing.
func TestAuditGuardedTransportBlocksLiveLocalTLSServer(t *testing.T) {
	oldTimeout := guardDialTimeout
	guardDialTimeout = 250 * time.Millisecond
	t.Cleanup(func() { guardDialTimeout = oldTimeout })

	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := &stubResolver{addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	client := &http.Client{Timeout: 5 * time.Second, Transport: NewGuardedTransport(res)}
	resp, err := client.Post("https://tls-target.test/hook", "application/json", nil)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("request to live local TLS server via lying resolver succeeded, want error")
	}
	if !errors.Is(err, ErrPolicyBlocked) {
		t.Fatalf("err = %v, want ErrPolicyBlocked (refused pre-handshake)", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("TLS server saw %d hits, want 0", got)
	}
}

// ---------- 6. end-to-end adversarial encoding at delivery time ----------

// TestAuditDeliveryHexEncodedLoopbackBlocked is the end-to-end adversarial
// regression: a stored webhook whose host is the hex-encoded loopback literal
// (0x7f000001 — accepted by getaddrinfo-style resolvers, invisible to
// netip.ParseAddr) must be blocked at delivery with exactly one attempt, the
// sanitized policy message, and a live local server seeing nothing.
func TestAuditDeliveryHexEncodedLoopbackBlocked(t *testing.T) {
	oldTimeout := guardDialTimeout
	guardDialTimeout = 250 * time.Millisecond
	t.Cleanup(func() { guardDialTimeout = oldTimeout })

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := &stubResolver{addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	d, cap := newGuardedCapturingDispatcher(t, res)
	d.maxRetries = 3

	d.deliverWithRetry(1, models.Webhook{ID: 42, UserID: 1, URL: "http://0x7f000001/hook"},
		deliverBody(t, "email.sent", "hex"), "email.sent")

	if cap.deliveryCount() != 1 {
		t.Fatalf("deliveries recorded = %d, want 1", cap.deliveryCount())
	}
	delivery := cap.lastDelivery()
	if delivery.ErrorMessage != msgBlocked {
		t.Fatalf("delivery error = %q, want %q", delivery.ErrorMessage, msgBlocked)
	}
	auditAssertNoInfraDetail(t, "recorded delivery error", delivery.ErrorMessage)
	if attempts := cap.allAttempts(); len(attempts) != 1 {
		t.Fatalf("attempt rows = %d, want 1 (policy block must fail fast)", len(attempts))
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("test server received %d requests, want 0", got)
	}
}

// ---------- 7. write-time sanitization: recorded rows are detail-free ----------

// TestAuditWriteTimeSanitization_NoInfraDetailInRecordedRows drives a real
// transport failure (connection refused to a dead loopback endpoint whose raw
// Go error contains 127.0.0.1:<port>) through the real dispatcher and asserts
// the PERSISTED delivery and attempt rows carry no infrastructure detail —
// the adversarial strengthening of the committed "records connection errors"
// test, which only checks that an error exists.
func TestAuditWriteTimeSanitization_NoInfraDetailInRecordedRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	endpoint := srv.URL
	srv.Close() // nothing listens: the raw dial error will contain 127.0.0.1:<port>

	d, cap := newCapturingDispatcher(t)
	d.maxRetries = 1

	d.deliverWithRetry(1, models.Webhook{ID: 8, UserID: 1, URL: endpoint},
		deliverBody(t, "email.sent", "dead"), "email.sent")

	if cap.deliveryCount() != 1 {
		t.Fatalf("deliveries recorded = %d, want 1", cap.deliveryCount())
	}
	delivery := cap.lastDelivery()
	auditAssertNoInfraDetail(t, "recorded delivery error", delivery.ErrorMessage)
	if delivery.ErrorMessage != msgConn {
		t.Errorf("recorded delivery error = %q, want %q", delivery.ErrorMessage, msgConn)
	}

	attempts := cap.allAttempts()
	if len(attempts) != 1 {
		t.Fatalf("attempt rows = %d, want 1", len(attempts))
	}
	auditAssertNoInfraDetail(t, "recorded attempt error", attempts[0].Error)
	if attempts[0].Error != msgConn {
		t.Errorf("recorded attempt error = %q, want %q", attempts[0].Error, msgConn)
	}
	if attempts[0].ResponseBody != "" {
		t.Errorf("failed attempt recorded body %q, want empty", attempts[0].ResponseBody)
	}
}

// ---------- 8. user-facing validation errors never contain addresses ----------

// TestAuditValidationErrorsNeverContainAddresses sweeps hostile URLs through
// both halves of the creation-time gate and asserts the returned error text
// (which the API echoes as "invalid webhook URL: <text>") never carries an
// IPv4 literal, an internal marker, or resolver detail.
func TestAuditValidationErrorsNeverContainAddresses(t *testing.T) {
	ctx := context.Background()
	raws := []string{
		"http://127.0.0.1:8080/x",
		"http://10.1.2.3/",
		"http://169.254.169.254/latest/meta-data/",
		"http://192.168.1.1/",
		"http://[::1]/",
		"http://[fe80::1%25eth0]/",
		"http://[::ffff:10.0.0.1]/",
		"http://localhost/x",
		"http://api.localhost/x",
		"http://0x7f000001/hook",
		"http://2130706433/hook",
		"http://0177.0.0.1/hook",
		"http://127.0.0.1./x",
		"http://user:pass@example.com/",
		"http://example.com:99999/",
		"ftp://example.com/",
	}
	for _, raw := range raws {
		t.Run(raw, func(t *testing.T) {
			if err := ValidateWebhookURL(raw); err != nil {
				auditAssertNoInfraDetail(t, "static validation error", err.Error())
			}
			// The resolution half: stub answers loopback (worst case).
			res := &stubResolver{addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
			if _, err := ResolvePublicURL(ctx, res, raw); err != nil {
				auditAssertNoInfraDetail(t, "resolution validation error", err.Error())
			}
			// And the resolver-failure case: the stub's raw error carries an
			// internal DNS server address; it must never survive.
			resErr := &stubResolver{err: errors.New("lookup failed on 10.0.0.2:53: server misbehaving")}
			if _, err := ResolvePublicURL(ctx, resErr, "rebind.test/hook"); err != nil {
				auditAssertNoInfraDetail(t, "resolver-failure error", err.Error())
			}
		})
	}
}

// ---------- 9. proxy mode: the gate runs before any connection ----------

// TestAuditProxyModeBlocksUnsafeDestinationsBeforeConnecting configures the
// dispatcher for proxy mode (the one mode where the guarded dialer does not
// see the destination) and asserts the static gate rejects every statically
// judgeable unsafe destination BEFORE the request reaches the proxy
// transport: a policy error proves no connection attempt was made (a real
// proxyconnect attempt would produce a transport error instead). Hostname
// pre-flight cases are not exercised here: the proxy-mode pre-flight
// resolves through the real resolver (non-injectable), and this suite never
// touches real DNS.
func TestAuditProxyModeBlocksUnsafeDestinationsBeforeConnecting(t *testing.T) {
	d := NewDispatcher(nil)
	d.SetConfig(Config{ProxyURL: "http://127.0.0.1:3128", ProxyAckPolicy: proxyAckEnforced})
	if !d.proxyDial {
		t.Fatal("test setup: proxy mode not enabled")
	}
	for _, raw := range []string{
		"http://192.168.5.5/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:9/x",
		"http://localhost/hook",
	} {
		_, _, err := d.post(raw, "secret", []byte(`{}`))
		if !errors.Is(err, ErrPolicyBlocked) {
			t.Errorf("proxy-mode post(%q) err = %v, want ErrPolicyBlocked before any proxy connection", raw, err)
		}
	}
}

// ---------- 10. documented hardening pins (current behavior, consciously accepted) ----------

// TestAuditLegacyHostnameBodiesAssumedSafe_HardeningPin documents a limitation
// of the legacy redaction heuristic: a stored HOSTNAME URL passes static
// validation, so captured bodies are considered safe even if, in legacy rows,
// that hostname once resolved to an internal address. Going forward the
// guarded dialer prevents such captures, so this is a legacy-data-only gap.
// Current behavior is pinned deliberately; a future fix should flip this
// assertion consciously.
func TestAuditLegacyHostnameBodiesAssumedSafe_HardeningPin(t *testing.T) {
	if LegacyAttemptBodyUnsafe("http://rebind.test/hook") {
		t.Log("hostname URLs are now treated as unsafe for legacy bodies — behavior improved; update this pin")
	}
	if !LegacyAttemptBodyUnsafe("http://10.0.0.5/hook") {
		t.Error("private IP literal URL must be treated as unsafe")
	}
	if !LegacyAttemptBodyUnsafe("http://127.0.0.1/hook") {
		t.Error("loopback literal URL must be treated as unsafe")
	}
}

// TestAuditTranslationPrefixes_Blocked pins that the address policy blocks
// the IPv4-embedding translation prefixes (6to4 2002::/16, NAT64
// 64:ff9b::/96): on a host with a working translation path these addresses
// route to the embedded IPv4 destination, so the policy rejects them like any
// other non-public destination. Formerly pinned as "not yet blocked"; the
// hardening landed in blockedV6 and this test now asserts it.
func TestAuditTranslationPrefixes_Blocked(t *testing.T) {
	for _, s := range []string{"2002:7f00:1::", "64:ff9b::127.0.0.1"} {
		if err := validateIPAddr(netip.MustParseAddr(s)); err == nil {
			t.Errorf("%s must be blocked: translation prefix embedding an IPv4 destination", s)
		}
	}
}
