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
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// stubResolver answers every lookup with a fixed set of addresses (or an
// error), so the guard's DNS behavior is fully controlled in tests.
type stubResolver struct {
	addrs []netip.Addr
	err   error
	calls atomic.Int32
}

func (s *stubResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	out := make([]net.IPAddr, 0, len(s.addrs))
	for _, a := range s.addrs {
		out = append(out, net.IPAddr{IP: a.AsSlice()})
	}
	return out, nil
}

func ip(t *testing.T, s string) netip.Addr {
	t.Helper()
	return netip.MustParseAddr(s)
}

// ---------- static URL validation (§12A) ----------

func TestValidateWebhookURL_RejectsUnsafeDestinations(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"loopback v4", "http://127.0.0.1/x"},
		{"loopback name", "http://localhost/x"},
		{"subdomain of localhost", "http://api.localhost/x"},
		{"private 10/8", "http://10.0.0.1/"},
		{"private 172.16/12", "http://172.16.0.1/"},
		{"private 192.168/16", "http://192.168.1.1/"},
		{"link-local metadata", "http://169.254.169.254/latest/meta-data/"},
		{"unspecified", "http://0.0.0.0/"},
		{"cgnat", "http://100.64.0.1/"},
		{"reserved 240/4", "http://240.0.0.1/"},
		{"broadcast", "http://255.255.255.255/"},
		{"test-net-1", "http://192.0.2.1/"},
		{"test-net-2", "http://198.51.100.1/"},
		{"test-net-3", "http://203.0.113.1/"},
		{"benchmarking", "http://198.18.0.1/"},
		{"loopback v6", "http://[::1]/x"},
		{"link-local v6", "http://[fe80::1]/x"},
		{"ula v6", "http://[fc00::1]/x"},
		{"ula fd00", "http://[fd12:3456::1]/x"},
		{"multicast v6", "http://[ff02::1]/x"},
		{"unspecified v6", "http://[::]/x"},
		{"v4-mapped loopback", "http://[::ffff:127.0.0.1]/x"},
		{"v4-mapped private", "http://[::ffff:10.0.0.1]/x"},
		{"v4-mapped metadata", "http://[::ffff:169.254.169.254]/x"},
		{"docs range v6", "http://[2001:db8::1]/x"},
		{"file scheme", "file:///etc/passwd"},
		{"ftp scheme", "ftp://example.com/file"},
		{"gopher scheme", "gopher://example.com/"},
		{"javascript scheme", "javascript:alert(1)"},
		{"data scheme", "data:text/html,hi"},
		{"credentials", "http://user:pass@example.com/"},
		{"credentials no password", "http://admin@example.com/"},
		{"no host", "http:///path"},
		{"invalid port", "http://example.com:99999/"},
		{"non-numeric port", "http://example.com:web/"},
		{"bad url", "http://[::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateWebhookURL(tc.raw); err == nil {
				t.Fatalf("ValidateWebhookURL(%q) = nil, want error", tc.raw)
			}
		})
	}
}

func TestValidateWebhookURL_AcceptsPublicDestinations(t *testing.T) {
	cases := []string{
		"https://example.com/webhook",
		"http://example.com/webhook",
		"https://hooks.example.internal-service.com:443/hook", // hostname, not an IP literal
		"https://example.com:8443/webhook",
		"http://93.184.216.34/hook",
		"https://[2606:4700::1111]/hook",
		"http://1.1.1.1:8080/hook",
	}
	for _, raw := range cases {
		if err := ValidateWebhookURL(raw); err != nil {
			t.Errorf("ValidateWebhookURL(%q) = %v, want nil", raw, err)
		}
	}
}

// ---------- address policy (§12A) ----------

func TestValidateIPAddr(t *testing.T) {
	blocked := []string{
		// v4
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "172.31.255.255",
		"192.168.1.1", "169.254.169.254", "0.0.0.0", "0.1.2.3",
		"100.64.0.1", "100.127.255.255", "224.0.0.1", "239.255.255.255",
		"240.0.0.1", "255.255.255.255", "192.0.2.1", "198.51.100.7",
		"203.0.113.9", "198.18.0.1", "192.0.0.5", "192.88.99.1",
		// v6
		"::1", "::", "fe80::1", "fc00::1", "fd12::1", "ff02::1", "ff01::1",
		"2001:db8::1",
		// v4-mapped v6 judged by v4 half
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:192.168.0.1",
		"::ffff:169.254.169.254",
	}
	for _, s := range blocked {
		if err := validateIPAddr(ip(t, s)); err == nil {
			t.Errorf("validateIPAddr(%s) = nil, want error", s)
		}
	}
	allowed := []string{
		"8.8.8.8", "1.1.1.1", "93.184.216.34", "172.32.0.1", "100.128.0.1",
		"9.9.9.9", "2606:4700::1111", "2620:fe::fe", "::ffff:8.8.8.8",
	}
	for _, s := range allowed {
		if err := validateIPAddr(ip(t, s)); err != nil {
			t.Errorf("validateIPAddr(%s) = %v, want nil", s, err)
		}
	}
}

// ---------- DNS resolution (§12B) ----------

func TestResolvePublicHost(t *testing.T) {
	ctx := context.Background()

	t.Run("public v4", func(t *testing.T) {
		res := &stubResolver{addrs: []netip.Addr{ip(t, "93.184.216.34")}}
		got, err := ResolvePublicHost(ctx, res, "example.com")
		if err != nil || len(got) != 1 {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("public v6", func(t *testing.T) {
		res := &stubResolver{addrs: []netip.Addr{ip(t, "2606:4700::1111")}}
		if _, err := ResolvePublicHost(ctx, res, "example.com"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("private answer rejected", func(t *testing.T) {
		res := &stubResolver{addrs: []netip.Addr{ip(t, "192.168.1.10")}}
		_, err := ResolvePublicHost(ctx, res, "rebind.example")
		if !errors.Is(err, ErrPolicyBlocked) {
			t.Fatalf("err = %v, want ErrPolicyBlocked", err)
		}
	})

	t.Run("loopback answer rejected", func(t *testing.T) {
		res := &stubResolver{addrs: []netip.Addr{ip(t, "127.0.0.1")}}
		if _, err := ResolvePublicHost(ctx, res, "rebind.example"); !errors.Is(err, ErrPolicyBlocked) {
			t.Fatalf("err = %v, want ErrPolicyBlocked", err)
		}
	})

	t.Run("link-local answer rejected", func(t *testing.T) {
		res := &stubResolver{addrs: []netip.Addr{ip(t, "169.254.169.254")}}
		if _, err := ResolvePublicHost(ctx, res, "metadata.example"); !errors.Is(err, ErrPolicyBlocked) {
			t.Fatalf("err = %v, want ErrPolicyBlocked", err)
		}
	})

	t.Run("mixed public and private rejected wholesale", func(t *testing.T) {
		res := &stubResolver{addrs: []netip.Addr{ip(t, "8.8.8.8"), ip(t, "192.168.1.10")}}
		if _, err := ResolvePublicHost(ctx, res, "mixed.example"); !errors.Is(err, ErrPolicyBlocked) {
			t.Fatalf("err = %v, want ErrPolicyBlocked (mixed answers fail closed)", err)
		}
	})

	t.Run("resolution failure", func(t *testing.T) {
		res := &stubResolver{err: errors.New("no such host")}
		_, err := ResolvePublicHost(ctx, res, "gone.example")
		if err == nil {
			t.Fatal("want error")
		}
		if errors.Is(err, ErrPolicyBlocked) {
			t.Fatal("resolution failure is transient, must not be a policy block")
		}
		// The resolver's own text must not leak into the user-facing error.
		if got := SanitizeDeliveryError(err.Error()); got != msgResolve {
			t.Fatalf("sanitized = %q, want %q", got, msgResolve)
		}
	})

	t.Run("empty answer", func(t *testing.T) {
		res := &stubResolver{addrs: []netip.Addr{}}
		if _, err := ResolvePublicHost(ctx, res, "empty.example"); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("ip literal short-circuits resolver", func(t *testing.T) {
		res := &stubResolver{}
		got, err := ResolvePublicHost(ctx, res, "8.8.8.8")
		if err != nil || len(got) != 1 || got[0].String() != "8.8.8.8" {
			t.Fatalf("got %v, %v", got, err)
		}
		if res.calls.Load() != 0 {
			t.Fatalf("resolver called %d times for an IP literal", res.calls.Load())
		}
	})

	t.Run("private ip literal rejected without lookup", func(t *testing.T) {
		res := &stubResolver{}
		if _, err := ResolvePublicHost(ctx, res, "10.9.9.9"); !errors.Is(err, ErrPolicyBlocked) {
			t.Fatalf("err = %v, want ErrPolicyBlocked", err)
		}
		if res.calls.Load() != 0 {
			t.Fatalf("resolver called %d times", res.calls.Load())
		}
	})
}

// ---------- connection-time enforcement (§12B/§12C) ----------

// TestGuardedDialerBlocksLoopback is the core SSRF regression: even when a
// resolver lies and maps a hostname to loopback — and a real server IS
// listening there — the guarded dialer must refuse to connect.
func TestGuardedDialerBlocksLoopback(t *testing.T) {
	oldTimeout := guardDialTimeout
	guardDialTimeout = 200 * time.Millisecond
	t.Cleanup(func() { guardDialTimeout = oldTimeout })

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := &stubResolver{addrs: []netip.Addr{ip(t, "127.0.0.1")}}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: NewGuardedTransport(res),
	}
	resp, err := client.Post("http://evil.example/hook", "application/json", nil)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("request to loopback via lying resolver succeeded, want error")
	}
	if !errors.Is(err, ErrPolicyBlocked) {
		t.Fatalf("err = %v, want ErrPolicyBlocked", err)
	}
	if hits != 0 {
		t.Fatalf("test server received %d requests, want 0", hits)
	}
}

// TestGuardedDialerRejectsMixedAnswers proves there is no per-address
// fallback: a DNS answer mixing a public and a private address must fail
// closed instead of dialing the public one.
func TestGuardedDialerRejectsMixedAnswers(t *testing.T) {
	oldTimeout := guardDialTimeout
	guardDialTimeout = 200 * time.Millisecond
	t.Cleanup(func() { guardDialTimeout = oldTimeout })

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// The stub returns a public address alongside loopback. If the dialer
	// fell back per-address instead of failing closed, it would dial the
	// public one — the ErrPolicyBlocked assertion below catches that.
	res := &stubResolver{addrs: []netip.Addr{ip(t, "93.184.216.34"), ip(t, "127.0.0.1")}}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: NewGuardedTransport(res),
	}
	_, err := client.Post("http://mixed.example/hook", "application/json", nil)
	if !errors.Is(err, ErrPolicyBlocked) {
		t.Fatalf("err = %v, want ErrPolicyBlocked", err)
	}
	if hits != 0 {
		t.Fatalf("test server received %d requests, want 0", hits)
	}
}

// TestGuardedDialerRebindBetweenAttempts simulates DNS rebinding during
// retries: the first resolution answers a public address, the second flips to
// loopback. Fully offline: the dialer's Control hook records the final socket
// address and aborts BEFORE connect(2), so attempt 1 proves the validated
// public literal is exactly what gets dialed without a single packet leaving
// the machine, and attempt 2 proves the rebound loopback answer is refused
// by policy before any socket exists.
func TestGuardedDialerRebindBetweenAttempts(t *testing.T) {
	sentinel := errors.New("aborted-by-test: connect intentionally aborted before connect(2)")

	// Recording/aborting dialer: the Control hook runs after socket creation
	// and strictly before connect, so returning an error here means no packet
	// is ever sent.
	var dialed atomic.Value // last pre-connect socket address, as string
	d := &net.Dialer{
		Timeout: 100 * time.Millisecond,
		Control: func(_ string, address string, _ syscall.RawConn) error {
			dialed.Store(address)
			return sentinel
		},
	}

	// Loopback canary: if any request ever reaches the local listener, the
	// guard has failed AND the test has leaked traffic.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := &stubResolver{addrs: []netip.Addr{ip(t, "93.184.216.34")}}
	tr := &http.Transport{DialContext: guardedDialContext(d, res)}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: tr,
	}

	// Attempt 1: the resolution is public; the dial must carry exactly the
	// validated IP literal and fail with the pre-connect abort sentinel.
	_, firstErr := client.Post("http://rebind.example/hook", "application/json", nil)
	if !errors.Is(firstErr, sentinel) {
		t.Fatalf("first attempt err = %v, want the Control-hook sentinel (the hook must fire pre-connect)", firstErr)
	}
	if got, ok := dialed.Load().(string); !ok || got != "93.184.216.34:80" {
		t.Fatalf("attempt 1 dialed %v, want [93.184.216.34:80] (no hostname, the validated IP literal)", dialed.Load())
	}
	if got := res.calls.Load(); got != 1 {
		t.Fatalf("resolver called %d times across one connection, want 1 (resolve→validate→dial is atomic w.r.t. DNS)", got)
	}

	// Attempt 2: the attacker's DNS flips to loopback; the guarded dialer must
	// refuse it during validation, before any socket exists.
	res.addrs = []netip.Addr{ip(t, "127.0.0.1")}
	_, secondErr := client.Post("http://rebind.example/hook", "application/json", nil)
	if !errors.Is(secondErr, ErrPolicyBlocked) {
		t.Fatalf("second attempt err = %v, want ErrPolicyBlocked (resolver-validated refusal, pre-dial)", secondErr)
	}
	if got, ok := dialed.Load().(string); !ok || got != "93.184.216.34:80" {
		t.Fatalf("recorded dial address is now %v; the loopback answer must never reach a socket", dialed.Load())
	}
	if got := res.calls.Load(); got != 2 {
		t.Fatalf("resolver called %d times across two connections, want 2 (fresh resolution per connection)", got)
	}
	if hits != 0 {
		t.Fatalf("test server received %d requests, want 0", hits)
	}
}

// TestControlHookRechecksSocketAddress verifies the dialer's Control hook
// rejects a non-public raw socket address even when handed a literal.
func TestControlHookRechecksSocketAddress(t *testing.T) {
	cases := []struct {
		addr    string
		wantErr bool
	}{
		{"127.0.0.1:8080", true},
		{"10.0.0.5:5432", true},
		{"169.254.169.254:80", true},
		{"[::1]:9090", true},
		{"8.8.8.8:443", false},
		{"[2606:4700::1111]:443", false},
	}
	for _, tc := range cases {
		err := controlPublicAddr("tcp", tc.addr, nil)
		if tc.wantErr && err == nil {
			t.Errorf("controlPublicAddr(%q) = nil, want error", tc.addr)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("controlPublicAddr(%q) = %v, want nil", tc.addr, err)
		}
	}
}

// ---------- redirects (§12C) ----------

func TestRedirectsNeverFollowed(t *testing.T) {
	targetHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirects := 0
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects++
		http.Redirect(w, r, target.URL+"/stolen", http.StatusFound)
	}))
	defer source.Close()

	// The dispatcher's client shape: no redirect following. The transport is
	// permissive here because the point under test is the redirect policy,
	// not the address policy (loopback httptest servers are unreachable
	// through the guarded transport by design).
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(source.URL + "/start")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d (3xx must be the final response)", resp.StatusCode, http.StatusFound)
	}
	if redirects != 1 || targetHits != 0 {
		t.Fatalf("source hits = %d, target hits = %d; redirect must not be followed", redirects, targetHits)
	}
}

// ---------- error sanitization (§12D) ----------

func TestSanitizeDeliveryError(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{"dial refused with private ip", `Post "http://127.0.0.1:8080/x": dial tcp 127.0.0.1:8080: connect: connection refused`, msgConn},
		{"dial refused internal network", "dial tcp 10.0.0.5:5432: connect: connection refused", msgConn},
		{"no route", "dial tcp 192.168.1.1:443: connect: no route to host", msgConn},
		{"client timeout", `Post "http://10.0.0.1:9/x": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`, msgTimeout},
		{"dial i/o timeout", "dial tcp 93.184.216.34:443: i/o timeout", msgTimeout},
		{"dns failure with details", "lookup internal.corp on 10.0.0.2:53: no such host", msgResolve},
		{"tls error", "tls: failed to verify certificate: x509: certificate signed by unknown authority", msgTLS},
		{"proxy error", "proxyconnect tcp: dial tcp 10.0.0.9:3128: connect: connection refused", msgConn},
		{"policy block", msgBlocked, msgBlocked},
		{"http status kept", "HTTP 500", "HTTP 500"},
		{"http status kept lowercase", "http 404", "http 404"},
		{"garbage falls back", "something inexplicable happened", msgGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeDeliveryError(tc.raw); got != tc.want {
				t.Fatalf("SanitizeDeliveryError(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestSanitizeNeverLeaksAddresses sweeps representative raw errors and
// asserts no IP address or port survives sanitization.
func TestSanitizeNeverLeaksAddresses(t *testing.T) {
	raws := []string{
		`Post "http://127.0.0.1:8080/x": dial tcp 127.0.0.1:8080: connect: connection refused`,
		"dial tcp 10.0.0.5:5432: connect: connection refused",
		"lookup db.internal on 10.0.0.2:53: no such host",
		"proxyconnect tcp: dial tcp 172.16.0.9:3128: connect: connection refused",
	}
	for _, raw := range raws {
		got := SanitizeDeliveryError(raw)
		for _, leak := range []string{"127.0.0.1", "10.0.0", "172.16", ":8080", ":5432", ":3128", ":53"} {
			if contains(got, leak) {
				t.Errorf("SanitizeDeliveryError leaked %q in %q", leak, got)
			}
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// ---------- legacy response-body redaction (§12D) ----------

func TestLegacyAttemptBodyUnsafe(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"http://127.0.0.1:8080/admin", true},
		{"http://169.254.169.254/latest", true},
		{"http://10.0.0.5/x", true},
		{"http://localhost/x", true},
		{"file:///etc/passwd", true},
		{"", true},
		{"garbage", true},
		{"https://hooks.example.com/x", false},
		{"http://93.184.216.34/x", false},
	}
	for _, tc := range cases {
		if got := LegacyAttemptBodyUnsafe(tc.raw); got != tc.want {
			t.Errorf("LegacyAttemptBodyUnsafe(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// Compile-time assertion: *net.Resolver must satisfy the Resolver seam.
var (
	_ Resolver = (*net.Resolver)(nil)
	_ Resolver = (*stubResolver)(nil)
)
