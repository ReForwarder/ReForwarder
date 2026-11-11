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
	"errors"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/dnsmanager"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// DNSProviderHandler is the OB-6 DNS automation admin surface (ADR-007
// decision 1): per-domain provider connection (credential stored encrypted,
// responses masked), publish (plan → publish → verify), and the drift status
// health surface. Workspace-scoped like every domain endpoint: a foreign
// domain answers 404, and every mutation requires editor permissions.
//
// The routes themselves are registered only when POSTA_DNS_AUTOMATION_ENABLED
// is set (ADR-009 §8's off-by-default posture).
type DNSProviderHandler struct {
	svc        *dnsmanager.Service
	domainRepo *repositories.DomainRepository
	audit      *audit.Logger
}

func NewDNSProviderHandler(svc *dnsmanager.Service, domainRepo *repositories.DomainRepository, auditLogger *audit.Logger) *DNSProviderHandler {
	return &DNSProviderHandler{svc: svc, domainRepo: domainRepo, audit: auditLogger}
}

// Wire shapes (aliases like DomainHealthReport) for the DNS automation views.
type (
	// DNSStatusView is GET .../dns-provider's payload.
	DNSStatusView = dnsmanager.StatusView
	// DNSPublishResult is POST .../dns-records/publish's payload.
	DNSPublishResult = dnsmanager.PublishResult
	// DNSDriftStatusView is GET .../dns-drift's payload.
	DNSDriftStatusView = dnsmanager.DriftStatusView
	// DNSCredentialView is the masked credential view.
	DNSCredentialView = dnsmanager.CredentialView
	// DNSPlannedRecord is one record of the publication plan.
	DNSPlannedRecord = dnsmanager.PlannedRecord
)

// resolveDomain loads the domain and enforces scope ownership; a foreign or
// missing domain is indistinguishable (404).
func (h *DNSProviderHandler) resolveDomain(c *okapi.Context, domainID uint) (*models.Domain, error) {
	d, err := h.domainRepo.FindByID(domainID)
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		return nil, gorm.ErrRecordNotFound
	}
	return d, nil
}

// ---- Connect ----

// ConnectRequest names the provider and carries the credential material once.
// The raw material is never echoed back — responses carry the masked view.
type DNSConnectRequest struct {
	ID uint `param:"id"`
	Body dnsmanager.ConnectRequest `json:"body"`
}

// Connect validates the credential against the provider (read-only), stores
// it encrypted, and returns the masked view.
func (h *DNSProviderHandler) Connect(c *okapi.Context, req *DNSConnectRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	if req.Body.Provider == "" {
		return c.AbortBadRequest("provider is required (cloudflare, route53 or digitalocean)")
	}
	view, err := h.svc.Connect(c.Context(), d, req.Body)
	if err != nil {
		return h.fail(c, "connecting the DNS provider failed", err)
	}
	h.logAudit(c, "domain.dns_provider.connected",
		"Connected \""+d.Domain+"\" to the "+req.Body.Provider+" DNS provider", map[string]any{
			"domain_id": d.ID,
			"domain":    d.Domain,
			"provider":  req.Body.Provider,
		})
	return created(c, view)
}

// ---- Status ----

// DNSStatusRequest names one domain.
type DNSStatusRequest struct {
	ID uint `param:"id"`
}

// Status returns the masked connection, the planned record set and the
// publication/drift state — no provider traffic.
func (h *DNSProviderHandler) Status(c *okapi.Context, req *DNSStatusRequest) error {
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	view, err := h.svc.Status(d)
	if err != nil {
		return c.AbortInternalServerError("failed to assemble DNS automation status", err)
	}
	return ok(c, view)
}

// ---- Publish ----

// DNSPublishRequest names one domain.
type DNSPublishRequest struct {
	ID uint `param:"id"`
}

// Publish runs the plan → publish → verify flow. Refused (typed error → 4xx)
// for unverified domains and unconnected domains; provider failures surface
// the typed cause (401/403 → 401, zone missing → 404, rate limit → 429).
func (h *DNSProviderHandler) Publish(c *okapi.Context, req *DNSPublishRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	result, err := h.svc.PublishDomainRecords(c.Context(), d)
	if err != nil {
		h.logAudit(c, "domain.dns_records.publish_failed",
			"DNS publication for \""+d.Domain+"\" failed: "+err.Error(), map[string]any{
				"domain_id": d.ID,
				"domain":    d.Domain,
				"error":     err.Error(),
			})
		return h.fail(c, "DNS publication failed", err)
	}
	h.logAudit(c, "domain.dns_records.published",
		"Published the DNS record set for \""+d.Domain+"\" and re-ran verification", map[string]any{
			"domain_id": d.ID,
			"domain":    d.Domain,
			"records":   len(result.Records),
			"spf":       result.Verification.SPFVerified,
			"dkim":      result.Verification.DKIMVerified,
			"dmarc":     result.Verification.DMARCVerified,
		})
	return created(c, result)
}

// ---- Disconnect (per-domain revocation) ----

// Disconnect removes the domain's provider credential. Publication/drift
// history is kept; drift checks stop without a connection.
func (h *DNSProviderHandler) Disconnect(c *okapi.Context, req *DNSStatusRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	removed, err := h.svc.Disconnect(d)
	if err != nil {
		return c.AbortInternalServerError("failed to revoke the DNS provider credential", err)
	}
	if removed {
		h.logAudit(c, "domain.dns_provider.revoked",
			"Revoked the DNS provider credential for \""+d.Domain+"\"", map[string]any{
				"domain_id": d.ID,
				"domain":    d.Domain,
			})
	}
	return ok(c, map[string]any{"revoked": removed})
}

// ---- Drift status ----

// Drift returns the stored drift verdict — the health surface the drift cron
// writes and the dashboard panel reads. No provider traffic.
func (h *DNSProviderHandler) Drift(c *okapi.Context, req *DNSStatusRequest) error {
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	view, err := h.svc.DriftStatus(d)
	if err != nil {
		return c.AbortInternalServerError("failed to read DNS drift status", err)
	}
	return ok(c, view)
}

// ---- error mapping ----

// fail maps the service's typed errors onto HTTP outcomes: unauthorized
// (mis-scoped token) → 401, missing zone → 404, rate limit → 429, refused
// publish (unverified domain) → 409, everything else → 502.
func (h *DNSProviderHandler) fail(c *okapi.Context, message string, err error) error {
	switch {
	case errors.Is(err, dnsmanager.ErrUnauthorized):
		return c.AbortUnauthorized("the DNS provider rejected the credentials (invalid or mis-scoped token)", err)
	case errors.Is(err, dnsmanager.ErrZoneNotFound):
		return c.AbortNotFound("the zone was not found at the DNS provider")
	case errors.Is(err, dnsmanager.ErrRateLimited):
		return c.AbortTooManyRequests("the DNS provider rate limited the request", err)
	case errors.Is(err, dnsmanager.ErrDomainNotVerified):
		return c.AbortConflict("DNS publication is refused until domain ownership is verified", err)
	case errors.Is(err, dnsmanager.ErrNotConnected):
		return c.AbortBadRequest("this domain is not connected to a DNS provider yet")
	case errors.Is(err, dnsmanager.ErrMailHostMissing):
		return c.AbortBadRequest("mail-routing publication is requested but POSTA_DNS_MAIL_HOST is not configured")
	default:
		return c.AbortBadGateway(message, err)
	}
}

func (h *DNSProviderHandler) logAudit(c *okapi.Context, action, message string, metadata map[string]any) {
	if h.audit != nil {
		h.audit.LogCtx(c, action, message, metadata)
	}
}
