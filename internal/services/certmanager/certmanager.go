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

// Package certmanager implements the ACME/SNI certificate lifecycle
// (SF-34, ADR-007 decision 3): per-domain SNI certificate selection for the
// TLS listeners that serve mail (IMAP 993, POP3 995, submission 587
// STARTTLS) and the webmail HTTPS path, with certificates obtained through
// golang.org/x/crypto/acme/autocert (already an x/crypto subpackage — no new
// module) and cached in the blob store (ADR-007: "Certificates cache to the
// blob store (FS/S3), reload hot (SF-110 pattern)").
//
// The whole feature is config-gated (POSTA_ACME_ENABLED, default false);
// nothing in this package activates unless the server wiring constructs a
// Manager, and a nil Manager anywhere keeps the pre-OB-6 static-certificate
// behavior byte-for-byte.
//
// # Challenges
//
// HTTP-01 is served through the existing HTTP server: the server wiring
// mounts Manager.HTTPChallenge as an okapi middleware, so
// /.well-known/acme-challenge/<token> answers on the plain-HTTP listener
// while every other path falls through unchanged. TLS-ALPN-01 additionally
// works on the webmail HTTPS listener when that listener serves port 443
// (autocert's TLSConfig NextProtos include acme.ALPNProto).
//
// DNS-01 is NOT wired: autocert exposes no custom-challenge-solver hook. The
// CertSolver interface below is the seam reserved for the W1 dnsmanager
// adapter (ADR-007 decision 1); when that adapter lands it will either drive
// a custom acme.Client issuance loop or motivate the ADR-007-documented Lego
// upgrade path ("Lego is the documented upgrade path if DNS-01-per-provider
// breadth demands it"). The interface exists so the adapter is additive.
//
// # Hosts (the SNI map)
//
// The certificate-target allowlist is the union of the operator's explicit
// POSTA_ACME_DOMAINS list and the ownership-verified tenant domains (ADR-008
// decision 2: mail-domain features are gated on verified ownership — a
// domain the deployment does not own is refused as a certificate target).
// The set hot-reloads (SF-110 pattern): RefreshHosts pulls from the source
// the server wiring supplies, StartHostSync repeats it on an interval, and
// autocert re-reads the blob cache on every handshake miss.
//
// # Listener posture notes
//
//   - Submission (587) is STARTTLS in this tree (RFC 8314); the SNI map
//     serves its STARTTLS handshake. No implicit-TLS submission port is
//     added (RFC 8314 keeps implicit TLS on 465, which this tree does not
//     listen on).
//   - Mail listeners do not offer HTTP ALPN protocols; h2/http-1.1 belong to
//     the webmail HTTPS listener only.
//   - Unknown SNI falls back to the listener's static certificate (SF-33's
//     SNI fallback posture) and fails the handshake only when no static
//     certificate exists — fail-closed for an ACME-only listener.
package certmanager

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// CertSolver is the DNS-01 challenge seam (W1 coordination, ADR-007
// decisions 1 and 3). A production implementation adapts the dnsmanager
// DNSProvider interface (publish/delete the TXT record under
// _acme-challenge.<domain>) once W1 lands; the adapter note is part of the
// ADR-007 certificate-lifecycle scope.
//
// Deliberately un-wired: autocert (the issuance engine this package uses)
// has no custom-solver hook, so nothing here claims DNS-01 works. See the
// package documentation for the two activation routes (custom acme.Client
// loop or the Lego upgrade path).
type CertSolver interface {
	// Present publishes the DNS-01 TXT challenge record for domain.
	Present(domain, token, keyAuth string) error
	// CleanUp removes the DNS-01 TXT challenge record after validation.
	CleanUp(domain, token, keyAuth string) error
}

// Options carries the Manager construction settings the server wiring derives
// from config (POSTA_ACME_*).
type Options struct {
	// Email is the ACME account contact (POSTA_ACME_EMAIL); optional.
	Email string
	// DirectoryURL overrides the ACME directory endpoint
	// (POSTA_ACME_DIRECTORY_URL, e.g. a local Pebble); empty keeps
	// autocert's default (Let's Encrypt production).
	DirectoryURL string
	// Hosts is the initial explicit certificate-target allowlist
	// (POSTA_ACME_DOMAINS); the tenant verified domains join it through the
	// HostSource.
	Hosts []string
	// Cache must be the blob-store-backed cache (BlobCache). autocert needs
	// it to persist certificates across restarts — refusing nil keeps the
	// ADR-007 "cache to the blob store" guarantee structural rather than
	// documented-only.
	Cache autocert.Cache
	// HostSource, when set, supplies the allowlist snapshot for
	// RefreshHosts/StartHostSync (the server wiring reads the verified
	// domains through the domain repository).
	HostSource func(ctx context.Context) []string
}

// Manager owns the per-domain SNI certificate map: an autocert manager for
// issuance + the blob cache, the hot-reloading host allowlist, and the
// per-listener tls.Config builders. All methods are safe for concurrent use
// (TLS handshakes run on server goroutines).
type Manager struct {
	acme    *autocert.Manager
	acmeGet func(*tls.ClientHelloInfo) (*tls.Certificate, error)

	hostMu    sync.RWMutex
	hosts     map[string]struct{}
	baseHosts map[string]struct{} // explicit operator hosts (never dropped by a refresh)

	sourceMu sync.RWMutex
	source   func(ctx context.Context) []string

	solverMu sync.RWMutex
	solver   CertSolver
}

// New builds the certificate manager. An error means the wiring refuses to
// start ACME features (the server logs it and keeps the static-cert
// behavior) rather than serving handshakes from a half-configured issuer.
func New(opts Options) (*Manager, error) {
	if opts.Cache == nil {
		return nil, errors.New("certmanager: a certificate cache is required (the blob-store BlobCache)")
	}
	hosts := make(map[string]struct{}, len(opts.Hosts))
	for _, h := range opts.Hosts {
		if n := normalizeHost(h); n != "" {
			hosts[n] = struct{}{}
		}
	}
	m := &Manager{
		hosts:     hosts,
		baseHosts: cloneHosts(hosts),
	}
	am := &autocert.Manager{
		Cache:  opts.Cache,
		Email:  opts.Email,
		Prompt: autocert.AcceptTOS,
		// The dynamic policy below replaces HostWhitelist so the allowlist
		// can hot-reload (SF-110) without rebuilding the autocert manager.
		HostPolicy: func(ctx context.Context, host string) error {
			if m.allowHost(host) {
				return nil
			}
			return fmt.Errorf("certmanager: host %q is not an approved certificate target", host)
		},
	}
	if d := strings.TrimSpace(opts.DirectoryURL); d != "" {
		am.Client = &acme.Client{DirectoryURL: d}
	}
	m.acme = am
	m.acmeGet = am.GetCertificate
	return m, nil
}

// SetSolver attaches the DNS-01 seam implementation (W1 adapter). Reserved:
// the current issuance engine (autocert) does not call it.
func (m *Manager) SetSolver(s CertSolver) {
	m.solverMu.Lock()
	defer m.solverMu.Unlock()
	m.solver = s
}

// Solver returns the attached DNS-01 seam implementation (nil today).
func (m *Manager) Solver() CertSolver {
	m.solverMu.RLock()
	defer m.solverMu.RUnlock()
	return m.solver
}

// SetHostSource installs the allowlist snapshot provider (the verified
// tenant domains, per ADR-008 decision 2).
func (m *Manager) SetHostSource(source func(ctx context.Context) []string) {
	m.sourceMu.Lock()
	defer m.sourceMu.Unlock()
	m.source = source
}

// allowHost reports whether host is on the current certificate-target
// allowlist. Normalization mirrors autocert's (case, trailing dot); an
// explicit operator list always wins over nothing.
func (m *Manager) allowHost(host string) bool {
	n := normalizeHost(host)
	if n == "" {
		return false
	}
	m.hostMu.RLock()
	defer m.hostMu.RUnlock()
	_, ok := m.hosts[n]
	return ok
}

// Hosts returns the current allowlist snapshot (sorted implicitly by the
// caller's use; order is not meaningful).
func (m *Manager) Hosts() []string {
	m.hostMu.RLock()
	defer m.hostMu.RUnlock()
	out := make([]string, 0, len(m.hosts))
	for h := range m.hosts {
		out = append(out, h)
	}
	return out
}

// SyncHosts hot-reloads the allowlist (SF-110 pattern) and returns the
// added/removed names so the wiring can log the delta. Re-adding a host that
// is already present is a no-op for it.
func (m *Manager) SyncHosts(hosts []string) (added, removed []string) {
	next := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		if n := normalizeHost(h); n != "" {
			next[n] = struct{}{}
		}
	}
	m.hostMu.Lock()
	for h := range next {
		if _, ok := m.hosts[h]; !ok {
			added = append(added, h)
		}
	}
	for h := range m.hosts {
		if _, ok := next[h]; !ok {
			removed = append(removed, h)
		}
	}
	m.hosts = next
	m.hostMu.Unlock()
	return added, removed
}

// RefreshHosts pulls a fresh snapshot from the host source and applies the
// union of it with the explicit operator hosts (Options.Hosts): the source
// (the verified tenant domains) is the authority for its portion — a
// domain the source drops is removed, per ADR-007's drift re-check — but a
// nil snapshot (a transient source failure the wiring logs) keeps the
// previous allowlist untouched, and the operator list can never be dropped
// by a source read. It is the hot-reload entry point the periodic loop and
// the tests drive.
func (m *Manager) RefreshHosts(ctx context.Context) (added, removed []string, err error) {
	m.sourceMu.RLock()
	src := m.source
	m.sourceMu.RUnlock()
	if src == nil {
		return nil, nil, nil
	}
	fetched := src(ctx)
	if fetched == nil {
		return nil, nil, nil
	}
	m.hostMu.RLock()
	merged := make([]string, 0, len(fetched)+len(m.baseHosts))
	seen := make(map[string]struct{}, len(fetched)+len(m.baseHosts))
	for h := range m.baseHosts {
		merged = append(merged, h)
		seen[h] = struct{}{}
	}
	m.hostMu.RUnlock()
	for _, h := range fetched {
		n := normalizeHost(h)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; !dup {
			seen[n] = struct{}{}
			merged = append(merged, n)
		}
	}
	added, removed = m.SyncHosts(merged)
	return added, removed, nil
}

// StartHostSync runs the periodic allowlist resync until ctx is cancelled
// (SF-110 pattern: hot reload without restart). The first refresh happens
// immediately so a restart picks up verified-domain changes without waiting
// a full interval.
func (m *Manager) StartHostSync(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			if added, removed, err := m.RefreshHosts(ctx); err != nil {
				logger.Warn("certificate host resync failed", "error", err)
			} else if len(added) > 0 || len(removed) > 0 {
				logger.Info("certificate host allowlist reloaded",
					"added", added, "removed", removed)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// HTTPChallenge mounts the ACME HTTP-01 responder in front of the existing
// HTTP server: only /.well-known/acme-challenge/ paths are intercepted
// (host policy enforced, unknown tokens answer 404); everything else falls
// through to the app unchanged. Mounting enables autocert's HTTP-01
// challenge attempts (Manager.HTTPHandler sets tryHTTP01).
func (m *Manager) HTTPChallenge(fallback http.Handler) http.Handler {
	return m.acme.HTTPHandler(fallback)
}

// SubmissionTLSGetCert returns the GetCertificate callback for the
// submission listener's STARTTLS handshake (587 — RFC 8314 STARTTLS, not an
// implicit-TLS port), wired through mta.SubmissionConfig.TLSGetCertificate.
// base is the optional static fallback certificate; nil base fails closed
// for names the SNI map does not cover. A Manager without ACME (never built
// by New, but a zero-value guard) returns nil so the wiring falls back to
// the static-certificate-only configuration.
func (m *Manager) SubmissionTLSGetCert(base *tls.Certificate) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if m == nil || m.acme == nil {
		return nil
	}
	return m.selectCertificate(base)
}

// MailListenerTLSConfig builds the tls.Config for one implicit-TLS mail
// listener (IMAP 993 / POP3 995) or a STARTTLS receiver (submission 587).
// base is the listener's static certificate (nil for an ACME-only
// listener). Mail listeners offer no HTTP ALPN protocols.
func (m *Manager) MailListenerTLSConfig(base *tls.Certificate) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if base != nil {
		cfg.Certificates = []tls.Certificate{*base}
	}
	if m.acme != nil {
		cfg.GetCertificate = m.selectCertificate(base)
	}
	return cfg
}

// WebListenerTLSConfig builds the tls.Config for the webmail HTTPS listener
// (okapi WithTLSServer): the SNI map plus HTTP/2 and the TLS-ALPN-01
// protocol marker (the challenge answers itself here when the listener
// serves port 443).
func (m *Manager) WebListenerTLSConfig(base *tls.Certificate) *tls.Config {
	cfg := m.MailListenerTLSConfig(base)
	cfg.NextProtos = []string{"h2", "http/1.1", acme.ALPNProto}
	return cfg
}

// selectCertificate is the SNI map selection, in order:
//
//  1. the listener's static certificate when it covers the requested name
//     (an operator-pinned cert wins over issuance);
//  2. the ACME map (autocert, blob-cached) for allowlisted hosts;
//  3. the static certificate as the fallback for every other name —
//     today's single-cert behavior (SF-33 SNI fallback) — and a typed
//     failure only when the listener has no static certificate at all.
func (m *Manager) selectCertificate(base *tls.Certificate) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		name := normalizeHost(hello.ServerName)
		if base != nil && certCoversHost(base, name) {
			return base, nil
		}
		var acmeErr error
		if m.acme != nil {
			cert, err := m.acmeGet(hello)
			if err == nil {
				return cert, nil
			}
			acmeErr = err
		}
		if base != nil {
			// SF-33 posture: the listener keeps serving its static
			// certificate for names the SNI map does not cover.
			return base, nil
		}
		if acmeErr != nil {
			return nil, fmt.Errorf("certmanager: no certificate for %q: %w", name, acmeErr)
		}
		return nil, fmt.Errorf("certmanager: no certificate for %q", name)
	}
}

// StaticCertFromFiles loads and parses a static certificate pair so SNI
// coverage checks can read the leaf's DNS names (crypto/tls leaves Leaf
// unparsed after LoadX509KeyPair).
func StaticCertFromFiles(certFile, keyFile string) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	if len(cert.Certificate) == 0 {
		return nil, errors.New("certmanager: certificate file contains no certificate")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("certmanager: parse leaf certificate: %w", err)
	}
	cert.Leaf = leaf
	return &cert, nil
}

// certCoversHost reports whether cert's leaf is valid for host (exact DNS
// name or a single-label wildcard match). An unparsable leaf never matches —
// callers fall through to the ACME map instead of guessing.
func certCoversHost(cert *tls.Certificate, host string) bool {
	if cert == nil || len(cert.Certificate) == 0 {
		return false
	}
	// Normalize like the SNI selection does (case, trailing dot); an empty
	// result or an IP literal never matches a DNS identity.
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	leaf := cert.Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return false
		}
		cert.Leaf = parsed
		leaf = parsed
	}
	for _, dns := range leaf.DNSNames {
		if matchDNSName(dns, host) {
			return true
		}
	}
	// A bare-IP or CN-only certificate still covers its CN when no SAN DNS
	// name matched and the CN equals the host (legacy self-hosted certs).
	if len(leaf.DNSNames) == 0 && leaf.Subject.CommonName != "" {
		return matchDNSName(leaf.Subject.CommonName, host)
	}
	return false
}

// matchDNSName matches one certificate DNS name against a host, honoring
// the RFC 6125 single-label wildcard ("*.mail.example.com" covers
// "a.mail.example.com" but not "a.b.mail.example.com").
func matchDNSName(dnsName, host string) bool {
	dnsName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(dnsName), "."))
	if dnsName == host {
		return true
	}
	if strings.HasPrefix(dnsName, "*.") {
		suffix := strings.TrimPrefix(dnsName, "*.")
		if !strings.HasSuffix(host, "."+suffix) {
			return false
		}
		label := strings.TrimSuffix(host, "."+suffix)
		return label != "" && !strings.Contains(label, ".")
	}
	return false
}

// normalizeHost lowercases, strips the trailing dot and the client-supplied
// port, and rejects IP literals (a certificate-target allowlist is made of
// DNS names — SNI for an IP connection has no ACME identity anyway).
func normalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return ""
	}
	if hst, _, err := net.SplitHostPort(h); err == nil {
		h = strings.ToLower(hst)
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" || net.ParseIP(h) != nil {
		return ""
	}
	return h
}

func cloneHosts(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for k := range in {
		out[k] = struct{}{}
	}
	return out
}
