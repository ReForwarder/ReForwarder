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

// Package dnsmanager is the OB-6 DNS automation layer (ADR-007 decision 1):
// a provider interface with Cloudflare / Route53 / DigitalOcean adapters, a
// publication planner, and the drift re-verification pass that closes GAP-08's
// residual alongside R-07.
//
// The ADR's invariants bind every adapter:
//   - publish is always followed by the existing verification flow (R-07) —
//     auto-publish never skips verification (service.PublishDomainRecords);
//   - a mis-scoped token fails with a typed error BEFORE any zone write —
//     adapters validate access with a read before publishing, so a rejected
//     credential produces no partial-zone writes;
//   - provider credentials are encrypted at rest (crypto.Encrypt) and never
//     returned by an API response (masked fingerprints only, service.Status).
package dnsmanager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// DNSRecordSpec is one DNS record the automation layer manages, provider
// neutral. Name is the absolute FQDN WITHOUT a trailing dot (the apex is the
// zone name itself); adapters translate to their provider's naming convention.
type DNSRecordSpec struct {
	Type  string `json:"type"` // TXT | CNAME | MX
	Name  string `json:"name"` // FQDN, no trailing dot; apex == zone name
	Value string `json:"value"`
	TTL   int    `json:"ttl,omitempty"`
	// Priority carries the MX preference; ignored for other types.
	Priority int `json:"priority,omitempty"`
}

// key returns the identity drift comparison and provider upserts match on.
func (r DNSRecordSpec) key() recordKey {
	return recordKey{typ: r.Type, name: normalizeName(r.Name)}
}

type recordKey struct {
	typ  string
	name string
}

// normalizeName lower-cases and strips one trailing dot so comparisons are
// stable across providers (Route53 echoes FQDNs with a trailing dot).
func normalizeName(name string) string {
	n := lowerTrim(name)
	for len(n) > 1 && n[len(n)-1] == '.' {
		n = n[:len(n)-1]
	}
	return n
}

func lowerTrim(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

// recordMatches reports whether a live provider record equals the spec's
// identity, and whether its value matches (TXT compared quote-tolerantly:
// providers and resolvers disagree about surrounding quotes).
func recordMatches(live DNSRecordSpec, spec DNSRecordSpec) bool {
	if live.Type != spec.Type || normalizeName(live.Name) != normalizeName(spec.Name) {
		return false
	}
	return valueMatches(live.Value, spec.Value)
}

// valueMatches compares record values; TXT values are compared with optional
// surrounding quotes stripped and runs of whitespace collapsed, because the
// three providers store and echo TXT content differently.
func valueMatches(a, b string) bool {
	return normalizeValue(a) == normalizeValue(b)
}

func normalizeValue(v string) string {
	v = lowerTrim(v)
	// Strip surrounding quotes.
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	// Collapse whitespace runs (TXT strings are often chunked/joined).
	out := make([]byte, 0, len(v))
	prevSpace := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if prevSpace {
				continue
			}
			c = ' '
			prevSpace = true
		} else {
			prevSpace = false
		}
		out = append(out, c)
	}
	return string(out)
}

// DNSProvider is the provider contract (ADR-007 decision 1). Adapters are
// additive; the three methods are the whole surface.
type DNSProvider interface {
	// Kind returns the provider identifier (models.DNSProvider* constant).
	Kind() string
	// PublishRecordSet upserts every record in the set under zone. Implementations
	// must validate access with a read BEFORE any write so a mis-scoped
	// credential fails with ErrUnauthorized and produces no partial-zone
	// writes. On failure it returns a *PublishError describing what was
	// applied before the failure.
	PublishRecordSet(ctx context.Context, zone string, records []DNSRecordSpec) error
	// ReadRecords returns every record currently live in the zone.
	ReadRecords(ctx context.Context, zone string) ([]DNSRecordSpec, error)
	// DeleteRecord removes one record identified by (type, name, value).
	DeleteRecord(ctx context.Context, zone string, rec DNSRecordSpec) error
}

// ---- Typed publish failures (ADR-007 decision 2) ----

// Sentinel typed failures. Every adapter maps its provider's errors onto
// these; the handler surfaces them as distinct HTTP outcomes and the publish
// state stores the message.
var (
	// ErrUnauthorized: the provider rejected the credential (invalid token,
	// expired token, or a token not scoped to the zone). No writes happened.
	ErrUnauthorized = errors.New("dnsmanager: provider rejected the credentials (invalid or mis-scoped token)")
	// ErrZoneNotFound: the zone does not exist at the provider (or the token
	// cannot see it).
	ErrZoneNotFound = errors.New("dnsmanager: zone not found at the DNS provider")
	// ErrRateLimited: the provider throttled the request.
	ErrRateLimited = errors.New("dnsmanager: DNS provider rate limited the request")
	// ErrProviderUnavailable: network failure or unexpected provider error.
	ErrProviderUnavailable = errors.New("dnsmanager: DNS provider unavailable")
	// ErrNotConnected: the domain has no provider credential stored.
	ErrNotConnected = errors.New("dnsmanager: domain is not connected to a DNS provider")
	// ErrDomainNotVerified: publish refused for an ownership-unverified domain
	// (fail-closed, ADR-008 decision 2).
	ErrDomainNotVerified = errors.New("dnsmanager: domain ownership is not verified — publish refused")
	// ErrMailHostMissing: mail-routing publication requested but the
	// deployment has no mail host configured.
	ErrMailHostMissing = errors.New("dnsmanager: mail-routing publication requested but POSTA_DNS_MAIL_HOST is not configured")
)

// PublishError is the typed failure of a publish attempt. Cause is one of the
// sentinel errors above (ErrUnauthorized, ErrZoneNotFound, ...); Applied lists
// the records that were upserted before the failing one, so the operator and
// the UI see exactly what landed and what did not. Adapters validate access
// before writing, so Applied is empty for mis-scoped credentials.
type PublishError struct {
	Zone    string
	Applied []DNSRecordSpec
	Failed  DNSRecordSpec
	Cause   error
}

func (e *PublishError) Error() string {
	msg := "dnsmanager: publish failed for zone " + e.Zone
	if len(e.Applied) > 0 {
		msg = fmt.Sprintf("%s (%d record(s) applied before the failure)", msg, len(e.Applied))
	}
	if e.Cause != nil {
		msg = msg + ": " + e.Cause.Error()
	}
	return msg
}

func (e *PublishError) Unwrap() error { return e.Cause }

// ErrorOf classifies a provider HTTP exchange onto the sentinel errors.
// status is the HTTP status code (0 for transport failures); code is the
// provider's machine error code when it carries one.
func ErrorOf(status int, code string) error {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ErrUnauthorized
	case code == "AccessDenied", code == "UnauthorizedOperation",
		code == "InvalidClientTokenId", code == "SignatureDoesNotMatch",
		code == "unauthorized", code == "forbidden":
		return ErrUnauthorized
	case status == http.StatusNotFound, code == "NoSuchHostedZone":
		return ErrZoneNotFound
	case status == http.StatusTooManyRequests, code == "Throttling", code == "throttled":
		return ErrRateLimited
	default:
		return ErrProviderUnavailable
	}
}

// ConnectRequest is the credential payload the API accepts at connect time.
// Provider selects the shape: bearer providers read Token; Route53 reads the
// access key pair. The raw material is accepted once, encrypted, and never
// echoed back.
type ConnectRequest struct {
	Provider        string `json:"provider"`
	Token           string `json:"token,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
	Region          string `json:"region,omitempty"`
}

// Credential is the decrypted provider secret material (the JSON envelope
// stored encrypted in models.DNSProviderCredential.Credential). It never
// appears in an API response — Status returns a CredentialView instead.
type Credential struct {
	Token           string `json:"token,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
	Region          string `json:"region,omitempty"`
}

// NewProvider builds the adapter for one credential. baseOverrides (test seam)
// lets tests point every adapter at a fake API server; production wiring
// passes nil and the adapters use their documented endpoints.
func NewProvider(kind string, cred Credential, baseOverrides map[string]string) (DNSProvider, error) {
	switch kind {
	case modelsProviderCloudflare:
		return NewCloudflareProvider(cred.Token, overrideBase(baseOverrides, "cloudflare", cloudflareDefaultBase)), nil
	case modelsProviderRoute53:
		return NewRoute53Provider(cred, overrideBase(baseOverrides, "route53", route53DefaultBase)), nil
	case modelsProviderDigitalOcean:
		return NewDigitalOceanProvider(cred.Token, overrideBase(baseOverrides, "digitalocean", digitalOceanDefaultBase)), nil
	default:
		return nil, fmt.Errorf("dnsmanager: unknown DNS provider %q (supported: cloudflare, route53, digitalocean)", kind)
	}
}

func overrideBase(overrides map[string]string, kind, def string) string {
	if overrides == nil {
		return def
	}
	if v, ok := overrides[kind]; ok && v != "" {
		return v
	}
	return def
}

// The provider kind constants re-declared locally to avoid importing models
// into the interface file (models imports nothing here; this keeps the
// adapter files independent of the storage layer).
const (
	modelsProviderCloudflare   = "cloudflare"
	modelsProviderRoute53      = "route53"
	modelsProviderDigitalOcean = "digitalocean"
)

// httpClient is the shared transport: a bounded timeout so a wedged provider
// API cannot hold a request (or the drift cron) open indefinitely.
var httpClient = &http.Client{Timeout: 20 * time.Second}
