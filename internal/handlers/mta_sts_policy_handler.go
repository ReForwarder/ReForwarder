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

package handlers

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mta"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// MTASTSPolicyHandler serves the MTA-STS policy document of the tenant's
// hosted domains (SF-29 policy-hosting half, OB-6 W4; ADR-007 §5, RFC 8461):
//
//	https://mta-sts.<domain>/.well-known/mta-sts.txt
//
// The deployment fronts this endpoint with the standard reverse proxy
// (ADR-009 §2), which terminates TLS for the mta-sts.<domain> vhost; this
// handler authenticates the request by the Host header instead of serving a
// fixed vhost, so one process serves every tenant domain's policy.
//
// A domain's policy exists only when the operator opted in (Domain.
// MTASTSMode set to none/testing/enforce — the empty column default serves
// nothing). The document renders the live MX host list, cache-bounded, so
// MX changes propagate without a redeploy; senders re-fetch per the policy's
// max_age.
type MTASTSPolicyHandler struct {
	domains *repositories.DomainRepository
	// lookupMX resolves the domain's live MX hosts; a field so tests inject
	// a fake (production: net.LookupMX).
	lookupMX func(ctx context.Context, domain string) ([]*net.MX, error)
	// policyMaxAge is the published max_age (RFC 8461 §4.2).
	policyMaxAge int

	mu      sync.Mutex
	mxCache map[string]mxCacheEntry
}

type mxCacheEntry struct {
	hosts   []string
	expires time.Time
}

// mtaSTSPolicyCacheTTL bounds the live MX resolution behind the policy
// document: short enough that MX changes propagate, long enough that a
// polling sender cannot drive DNS traffic.
const mtaSTSPolicyCacheTTL = 60 * time.Second

// NewMTASTSPolicyHandler builds the policy handler.
func NewMTASTSPolicyHandler(domains *repositories.DomainRepository) *MTASTSPolicyHandler {
	return &MTASTSPolicyHandler{
		domains: domains,
		lookupMX: func(ctx context.Context, domain string) ([]*net.MX, error) {
			return net.LookupMX(domain)
		},
		policyMaxAge: 86400,
		mxCache:      map[string]mxCacheEntry{},
	}
}

// SetLookupMX overrides the MX resolver (tests).
func (h *MTASTSPolicyHandler) SetLookupMX(fn func(ctx context.Context, domain string) ([]*net.MX, error)) {
	if fn != nil {
		h.lookupMX = fn
	}
}

// Get serves the policy document for the mta-sts.<domain> vhost this request
// arrived on. Responses are protocol artifacts, not API envelope payloads:
// text/plain per RFC 8461 §4.2 with a Cache-Control aligned to max_age. Any
// mismatch — wrong host shape, unverified domain, no opt-in, no resolvable
// MX — is a 404, the "no policy" answer senders already handle.
func (h *MTASTSPolicyHandler) Get(c *okapi.Context) error {
	domain := mtastsDomainFromHost(c.Request().Host)
	if domain == "" {
		return c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte("not found\n"))
	}
	d, err := h.domains.FindVerifiedByName(domain)
	if err != nil {
		return c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte("not found\n"))
	}
	mode := d.MTASTSMode
	switch mode {
	case models.MTASTSModeNone, models.MTASTSModeTesting, models.MTASTSModeEnforce:
	default:
		return c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte("not found\n"))
	}

	hosts := h.mxHosts(c.Request().Context(), d.Domain)
	if len(hosts) == 0 {
		// A policy without mx lines is invalid for testing/enforce (RFC 8461
		// §4.2) and useless for none — never serve a broken document; a
		// sender treats the 404 as "no policy" (the safe fallback posture).
		return c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte("not found\n"))
	}

	body := mta.RenderSTSPolicy(mode, hosts, h.policyMaxAge)
	c.ResponseWriter().Header().Set("Cache-Control", "max-age=86400")
	return c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(body))
}

// mtastsDomainFromHost extracts the policy domain from a request Host: the
// RFC 8461 §3.3 fixed prefix "mta-sts." followed by the domain. Anything else
// (the dashboard origin, a bare IP, a foreign vhost) yields "".
func mtastsDomainFromHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i] // strip the port, keep IPv6 literals whole
	}
	if !strings.HasPrefix(host, "mta-sts.") {
		return ""
	}
	domain := strings.TrimPrefix(host, "mta-sts.")
	domain = strings.TrimSuffix(domain, ".")
	return domain
}

// mxHosts resolves the domain's live MX hostnames through the bounded cache.
// A resolution failure or empty result yields nil (the caller answers 404
// rather than serve a policy without mx lines).
func (h *MTASTSPolicyHandler) mxHosts(ctx context.Context, domain string) []string {
	h.mu.Lock()
	if e, ok := h.mxCache[domain]; ok && time.Now().Before(e.expires) {
		h.mu.Unlock()
		return e.hosts
	}
	h.mu.Unlock()

	lookup := h.lookupMX
	if lookup == nil {
		lookup = func(ctx context.Context, domain string) ([]*net.MX, error) {
			return net.LookupMX(domain)
		}
	}
	var hosts []string
	if mxs, err := lookup(ctx, domain); err == nil {
		for _, mx := range mxs {
			host := strings.TrimSuffix(strings.TrimSpace(mx.Host), ".")
			if host == "" || host == "." {
				continue // RFC 7505 null MX: the domain accepts no mail
			}
			hosts = append(hosts, host)
		}
	}

	h.mu.Lock()
	h.mxCache[domain] = mxCacheEntry{hosts: hosts, expires: time.Now().Add(mtaSTSPolicyCacheTTL)}
	h.mu.Unlock()
	return hosts
}
