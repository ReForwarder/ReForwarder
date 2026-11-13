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

package certmanager

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/storage/blob"
)

// mustSelfSignedCert mints a self-signed ECDSA certificate covering the
// given DNS names — a deterministic stand-in for a real leaf in the SNI
// selection tests (real issuance is exercised only by the env-gated ACME
// test, which skips without a live ACME server).
func mustSelfSignedCert(t *testing.T, dnsNames ...string) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return &tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}
}

// testBlobStore builds a filesystem blob store under a temp dir — the same
// blob.Store contract the production FS/S3 providers implement.
func testBlobStore(t *testing.T) blob.Store {
	t.Helper()
	store, err := blob.New(blob.Config{Provider: "fs", FSBasePath: t.TempDir()})
	if err != nil {
		t.Fatalf("build blob store: %v", err)
	}
	return store
}

// mustManager builds a Manager with a real blob-store cache and the given
// explicit hosts.
func mustManager(t *testing.T, hosts ...string) *Manager {
	t.Helper()
	m, err := New(Options{Hosts: hosts, Cache: NewBlobCache(testBlobStore(t))})
	if err != nil {
		t.Fatalf("build manager: %v", err)
	}
	return m
}

// certName extracts a printable identity from a test certificate.
func certName(c *tls.Certificate) string {
	if c == nil || c.Leaf == nil || len(c.Leaf.DNSNames) == 0 {
		return ""
	}
	return c.Leaf.DNSNames[0]
}

// stubACMEGet installs a fake autocert GetCertificate returning a distinct
// certificate per requested name. The real issuance path is exercised by
// TestACMEIssuanceAgainstLocalServer; this stub isolates the SNI selection
// logic, which is the unit under test here.
func stubACMEGet(m *Manager, issued map[string]*tls.Certificate) {
	m.acmeGet = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		name := normalizeHost(hello.ServerName)
		if cert, ok := issued[name]; ok {
			return cert, nil
		}
		return nil, fmt.Errorf("stub: no issuance for %q", name)
	}
}

// TestSNISelectionMapsPerDomainCerts: the SNI map serves each requested
// domain its own certificate, keeps the listener's static certificate for
// the name the static cert covers, and falls back to the static cert for
// names the map does not know (SF-33 fallback posture preserved).
func TestSNISelectionMapsPerDomainCerts(t *testing.T) {
	base := mustSelfSignedCert(t, "mail.example.org")
	acmeA := mustSelfSignedCert(t, "mail.a.example")
	acmeB := mustSelfSignedCert(t, "mail.b.example")

	m := mustManager(t, "mail.a.example", "mail.b.example")
	stubACMEGet(m, map[string]*tls.Certificate{
		"mail.a.example": acmeA,
		"mail.b.example": acmeB,
	})

	cfg := m.MailListenerTLSConfig(base)
	if cfg.GetCertificate == nil {
		t.Fatal("MailListenerTLSConfig did not install GetCertificate with ACME enabled")
	}
	if len(cfg.NextProtos) != 0 {
		t.Fatalf("mail listener NextProtos = %v, want none (no HTTP ALPN on mail listeners)", cfg.NextProtos)
	}

	cases := []struct {
		name     string
		sni      string
		wantName string
	}{
		{"first mapped domain serves its own cert", "mail.a.example", "mail.a.example"},
		{"second mapped domain serves its own cert", "mail.b.example", "mail.b.example"},
		{"base-cert hostname serves the static cert", "mail.example.org", "mail.example.org"},
		{"case-insensitive SNI maps the same cert", "MAIL.A.EXAMPLE", "mail.a.example"},
		{"unknown name falls back to the static cert (SF-33)", "unlisted.example", "mail.example.org"},
		{"empty SNI falls back to the static cert", "", "mail.example.org"},
		{"trailing dot is normalized", "mail.a.example.", "mail.a.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hello := &tls.ClientHelloInfo{ServerName: tc.sni}
			got, err := cfg.GetCertificate(hello)
			if err != nil {
				t.Fatalf("GetCertificate(%q) error: %v", tc.sni, err)
			}
			if name := certName(got); name != tc.wantName {
				t.Fatalf("GetCertificate(%q) served %q, want %q", tc.sni, name, tc.wantName)
			}
		})
	}
}

// TestSNISelectionPrefersStaticCertOverACME: an operator-pinned static
// certificate that covers a name wins over the ACME map for that name.
func TestSNISelectionPrefersStaticCertOverACME(t *testing.T) {
	base := mustSelfSignedCert(t, "mail.a.example")
	acmeA := mustSelfSignedCert(t, "mail.a.example")

	m := mustManager(t, "mail.a.example")
	stubACMEGet(m, map[string]*tls.Certificate{"mail.a.example": acmeA})

	cfg := m.MailListenerTLSConfig(base)
	hello := &tls.ClientHelloInfo{ServerName: "mail.a.example"}
	got, err := cfg.GetCertificate(hello)
	if err != nil {
		t.Fatalf("GetCertificate error: %v", err)
	}
	if got != base {
		t.Fatal("static certificate covering the SNI name must win over the ACME map")
	}
}

// TestSNISelectionFailsClosedWithoutBaseCert: an ACME-only listener (no
// static certificate configured) refuses handshakes for names the map does
// not cover instead of inventing a certificate.
func TestSNISelectionFailsClosedWithoutBaseCert(t *testing.T) {
	acmeA := mustSelfSignedCert(t, "mail.a.example")
	m := mustManager(t, "mail.a.example")
	stubACMEGet(m, map[string]*tls.Certificate{"mail.a.example": acmeA})

	cfg := m.MailListenerTLSConfig(nil)
	hello := &tls.ClientHelloInfo{ServerName: "unknown.example"}
	if _, err := cfg.GetCertificate(hello); err == nil {
		t.Fatal("ACME-only listener must fail closed for an unlisted SNI name")
	}
	// Missing SNI entirely: same refusal.
	if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{}); err == nil {
		t.Fatal("ACME-only listener must fail closed for a missing SNI name")
	}
}

// TestWebListenerTLSConfigOffersACMEALPN: the webmail HTTPS listener
// negotiates HTTP/2 and plain HTTP/1.1, and marks the ACME TLS-ALPN
// protocol so the challenge can answer on this listener when it serves
// port 443.
func TestWebListenerTLSConfigOffersACMEALPN(t *testing.T) {
	m := mustManager(t)
	cfg := m.WebListenerTLSConfig(nil)
	found := false
	for _, p := range cfg.NextProtos {
		if p == "h2" || p == "http/1.1" || p == "acme-tls/1" {
			if p == "acme-tls/1" {
				found = true
			}
			continue
		}
		t.Fatalf("unexpected NextProto %q", p)
	}
	if !found {
		t.Fatalf("NextProtos %v missing the ACME ALPN protocol", cfg.NextProtos)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d, want TLS 1.2", cfg.MinVersion)
	}
}

// TestHostAllowlistHotReload: SyncHosts applies the new allowlist with a
// precise added/removed delta and the host policy follows it immediately —
// the SF-110 hot-reload path for the SNI map.
func TestHostAllowlistHotReload(t *testing.T) {
	m := mustManager(t, "mail.a.example")

	if !m.allowHost("mail.a.example") || m.allowHost("mail.b.example") {
		t.Fatal("initial allowlist does not match the configured hosts")
	}

	added, removed := m.SyncHosts([]string{"mail.b.example", " Mail.C.Example. "})
	// SyncHosts derives the delta by ranging over maps, so the returned
	// slices carry no ordering guarantee — compare as sets.
	sort.Strings(added)
	if len(added) != 2 || added[0] != "mail.b.example" || added[1] != "mail.c.example" {
		t.Fatalf("added = %v, want [mail.b.example mail.c.example]", added)
	}
	if len(removed) != 1 || removed[0] != "mail.a.example" {
		t.Fatalf("removed = %v, want [mail.a.example]", removed)
	}
	if m.allowHost("mail.a.example") || !m.allowHost("mail.b.example") || !m.allowHost("mail.c.example") {
		t.Fatal("allowlist did not follow the SyncHosts reload")
	}
	if !m.allowHost("MAIL.C.EXAMPLE") {
		t.Fatal("allowlist lookups must be case-insensitive")
	}
}

// TestRefreshHostsPullsFromSource: RefreshHosts pulls the snapshot from the
// configured source (the server wiring reads the ownership-verified tenant
// domains there, per ADR-008 decision 2) and merges it over the explicit
// operator hosts.
func TestRefreshHostsPullsFromSource(t *testing.T) {
	m := mustManager(t, "explicit.example")
	m.SetHostSource(func(ctx context.Context) []string {
		return []string{"tenant1.example", "Tenant2.Example."}
	})

	added, removed, err := m.RefreshHosts(context.Background())
	if err != nil {
		t.Fatalf("RefreshHosts error: %v", err)
	}
	if len(added) != 2 || len(removed) != 0 {
		t.Fatalf("added/removed = %v/%v, want the two tenant hosts/none", added, removed)
	}
	if !m.allowHost("tenant1.example") || !m.allowHost("tenant2.example") || !m.allowHost("explicit.example") {
		t.Fatalf("allowlist = %v, want tenant + explicit hosts", m.Hosts())
	}

	// The source drops tenant1: the next refresh removes it.
	m.SetHostSource(func(ctx context.Context) []string {
		return []string{"tenant2.example"}
	})
	_, removed, err = m.RefreshHosts(context.Background())
	if err != nil {
		t.Fatalf("second RefreshHosts error: %v", err)
	}
	if len(removed) != 1 || removed[0] != "tenant1.example" {
		t.Fatalf("removed = %v, want [tenant1.example]", removed)
	}

	// No source configured: RefreshHosts is a no-op, never a wipe.
	m2, err := New(Options{Hosts: []string{"keep.example"}, Cache: NewBlobCache(testBlobStore(t))})
	if err != nil {
		t.Fatalf("build second manager: %v", err)
	}
	if _, _, err := m2.RefreshHosts(context.Background()); err != nil {
		t.Fatalf("RefreshHosts without source error: %v", err)
	}
	if !m2.allowHost("keep.example") {
		t.Fatal("RefreshHosts without a source must not touch the allowlist")
	}
}

// TestStartHostSyncStopsOnContextCancel: the periodic resync loop exits when
// its context is cancelled (the server wiring ties it to shutdown).
func TestStartHostSyncStopsOnContextCancel(t *testing.T) {
	m := mustManager(t)
	calls := make(chan struct{}, 8)
	m.SetHostSource(func(ctx context.Context) []string {
		select {
		case calls <- struct{}{}:
		default:
		}
		return []string{"sync.example"}
	})
	ctx, cancel := context.WithCancel(context.Background())
	m.StartHostSync(ctx, 20*time.Millisecond)
	defer cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-calls:
			cancel()
			// The loop must observe the cancellation promptly; give it a
			// grace period and confirm no further refreshes arrive.
			select {
			case c := <-calls:
				t.Fatalf("host sync kept running after context cancellation (extra refresh %v)", c)
			case <-time.After(150 * time.Millisecond):
				return
			}
		case <-deadline:
			t.Fatal("host sync never refreshed")
		}
	}
}

// TestHTTPChallengeMiddlewareRouting: the mounted HTTP-01 responder answers
// only /.well-known/acme-challenge/ paths, enforces the host policy, answers
// unknown tokens with 404, and falls through to the app for everything else.
func TestHTTPChallengeMiddlewareRouting(t *testing.T) {
	m := mustManager(t, "challenge.example.com")
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("app"))
	})
	handler := m.HTTPChallenge(fallback)

	t.Run("challenge path with disallowed host is refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://attacker.example/.well-known/acme-challenge/token123", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("challenge path with allowed host and unknown token is 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://challenge.example.com/.well-known/acme-challenge/token123", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("non-challenge paths fall through to the app", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://challenge.example.com/api/v1/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "app" {
			t.Fatalf("status/body = %d/%q, want 200/app", rec.Code, rec.Body.String())
		}
	})
}

// TestNewRequiresCache: Manager construction refuses a nil cache — the
// ADR-007 blob-store cache guarantee is structural, not documented-only.
func TestNewRequiresCache(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New without a cache must fail")
	}
}

// TestCertCoversHostWildcard: single-label wildcards match exactly one
// label (RFC 6125), exact names match themselves, and nothing matches an
// empty host.
func TestCertCoversHostWildcard(t *testing.T) {
	cert := mustSelfSignedCert(t, "mail.example.org", "*.a.example")
	cases := []struct {
		host string
		want bool
	}{{"mail.example.org", true}, {"MAIL.EXAMPLE.ORG.", true}, {"a.a.example", true},
		{"b.a.a.example", false}, {"a.example", false}, {"other.example", false}, {"", false}}
	for _, tc := range cases {
		if got := certCoversHost(cert, tc.host); got != tc.want {
			t.Fatalf("certCoversHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestStaticCertParsesLeafCoverage: StaticCertFromFiles produces a
// certificate whose leaf is parsed, so SNI coverage checks can read the
// SANs from a real key pair on disk.
func TestStaticCertParsesLeafCoverage(t *testing.T) {
	dir := t.TempDir()
	cert := mustSelfSignedCert(t, "files.example")
	certPEM := pemEncodeCert(t, cert)
	keyPEM := pemEncodeKey(t, cert)
	certPath, keyPath := dir+"/cert.pem", dir+"/key.pem"
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	loaded, err := StaticCertFromFiles(certPath, keyPath)
	if err != nil {
		t.Fatalf("StaticCertFromFiles: %v", err)
	}
	if loaded.Leaf == nil {
		t.Fatal("StaticCertFromFiles must parse the leaf (SNI coverage checks read it)")
	}
	if !certCoversHost(loaded, "files.example") {
		t.Fatal("loaded certificate does not cover its own SAN")
	}
	if certCoversHost(loaded, "files.invalid") {
		t.Fatal("loaded certificate must not cover an unlisted name")
	}
}

// TestACMEIssuanceAgainstLocalServer exercises the full issuance flow —
// autocert manager + blob cache + host policy — against a real local ACME
// server. It is strictly env-gated: without POSTA_TEST_ACME_DIRECTORY and
// POSTA_TEST_ACME_DOMAIN it skips cleanly and reports nothing. Run it
// against a local Pebble with the test domain mapped to this host (for
// example `pebble -dnb -config pebble.json` with POSTA_TEST_ACME_DIRECTORY
// = https://localhost:14000/dir and the domain resolving to 127.0.0.1 so
// the HTTP-01 challenge reaches the responder mounted on port 80).
func TestACMEIssuanceAgainstLocalServer(t *testing.T) {
	directory := strings.TrimSpace(os.Getenv("POSTA_TEST_ACME_DIRECTORY"))
	domain := strings.TrimSpace(os.Getenv("POSTA_TEST_ACME_DOMAIN"))
	if directory == "" || domain == "" {
		t.Skip("POSTA_TEST_ACME_DIRECTORY / POSTA_TEST_ACME_DOMAIN not set; skipping the live ACME issuance flow")
	}

	store := testBlobStore(t)
	m, err := New(Options{
		DirectoryURL: directory,
		Email:        os.Getenv("POSTA_TEST_ACME_EMAIL"),
		Hosts:        []string{domain},
		Cache:        NewBlobCache(store),
	})
	if err != nil {
		t.Fatalf("build manager: %v", err)
	}
	// The ACME server validates the HTTP-01 challenge by fetching
	// http://<domain>/.well-known/acme-challenge/<token> — mount the
	// responder locally the way the server wiring does (direct connection
	// on port 80; a reverse proxy deployment forwards the same path).
	challengeSrv := &http.Server{
		Addr:              ":80",
		Handler:           m.HTTPChallenge(http.NotFoundHandler()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = challengeSrv.ListenAndServe() }()
	defer func() { _ = challengeSrv.Close() }()

	cfg := m.MailListenerTLSConfig(nil)
	hello := &tls.ClientHelloInfo{
		ServerName:        domain,
		CipherSuites:      []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		SupportedVersions: []uint16{tls.VersionTLS13, tls.VersionTLS12},
	}
	cert, err := cfg.GetCertificate(hello)
	if err != nil {
		t.Fatalf("issue certificate for %q: %v", domain, err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("issuance returned no certificate")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}
	covers := false
	for _, dns := range leaf.DNSNames {
		if strings.EqualFold(strings.TrimSuffix(dns, "."), domain) {
			covers = true
		}
	}
	if !covers {
		t.Fatalf("issued certificate %v does not cover %q", leaf.DNSNames, domain)
	}
	// The certificate must be in the blob cache (the restart-survival path).
	bc := NewBlobCache(store)
	if _, err := bc.Get(context.Background(), domain+"+rsa-2048"); err == nil {
		return // RSA key shape found; either key shape proves the cache path.
	}
	if _, err := bc.Get(context.Background(), domain+"+ecdsa-p256"); err != nil {
		t.Fatalf("issued certificate not found in the blob cache: %v", err)
	}
}

// pemEncodeCert/pemEncodeKey serialize the generated test key pair into PEM
// for the StaticCertFromFiles round trip.
func pemEncodeCert(t *testing.T, cert *tls.Certificate) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
}

func pemEncodeKey(t *testing.T, cert *tls.Certificate) []byte {
	t.Helper()
	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("test certificate must carry an ECDSA key")
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}
