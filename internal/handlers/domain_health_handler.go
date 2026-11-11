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

package handlers

import (
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/domain"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// DomainHealthReport is the wire shape of the R-07 health report.
type DomainHealthReport = domain.HealthReport

// DomainHealthHandler serves the per-domain deliverability health view (R-07):
// auth check state, managed DKIM signing status, recent bounce/complaint rates,
// and the records to set when something is off. Workspace-scoped like every
// domain endpoint: a foreign domain answers 404.
type DomainHealthHandler struct {
	health     *domain.HealthService
	domainRepo *repositories.DomainRepository
	audit      *audit.Logger
}

func NewDomainHealthHandler(health *domain.HealthService, domainRepo *repositories.DomainRepository, auditLogger *audit.Logger) *DomainHealthHandler {
	return &DomainHealthHandler{health: health, domainRepo: domainRepo, audit: auditLogger}
}

// SetReputationReader wires the OB-6 W3 reputation/pool/warm-up sections
// (ADR-007 decision 7) onto the underlying health service; a nil reader
// keeps the report shape unchanged.
func (h *DomainHealthHandler) SetReputationReader(r domain.ReputationReader) {
	h.health.SetReputationReader(r)
}

// DomainHealthRequest names one domain.
type DomainHealthRequest struct {
	ID uint `param:"id"`
}

// resolveDomain loads the domain and enforces scope ownership; a foreign or
// missing domain is indistinguishable (404).
func (h *DomainHealthHandler) resolveDomain(c *okapi.Context, domainID uint) (*models.Domain, error) {
	d, err := h.domainRepo.FindByID(domainID)
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		return nil, gorm.ErrRecordNotFound
	}
	return d, nil
}

// Get returns the health report from stored state — no DNS traffic, safe for
// page loads.
func (h *DomainHealthHandler) Get(c *okapi.Context, req *DomainHealthRequest) error {
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	report, err := h.health.Report(d, domain.DefaultHealthWindow)
	if err != nil {
		return c.AbortInternalServerError("failed to assemble domain health", err)
	}
	return ok(c, report)
}

// Diagnose re-runs the live DNS checks on demand (the health page's diagnose
// button) and reports on the fresh results. The stored check booleans are
// updated too, so the domain list stays truthful after a manual diagnosis.
func (h *DomainHealthHandler) Diagnose(c *okapi.Context, req *DomainHealthRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	report, err := h.health.Diagnose(d, domain.DefaultHealthWindow)
	if err != nil {
		return c.AbortInternalServerError("DNS diagnosis failed", err)
	}

	// Persist the fresh check state (mirrors the verify endpoint) so stored
	// booleans never drift from what DNS actually says.
	d.OwnershipVerified = report.OwnershipVerified
	d.SPFVerified = report.SPF.Verified
	d.DKIMVerified = report.DKIM.Verified
	d.DMARCVerified = report.DMARC.Verified
	if err := h.domainRepo.Update(d); err != nil {
		return c.AbortInternalServerError("failed to update domain", err)
	}

	if h.audit != nil {
		h.audit.LogCtx(c, "domain.health.diagnosed",
			"Re-ran DNS checks for \""+d.Domain+"\"", map[string]any{
				"domain_id": d.ID,
				"domain":    d.Domain,
				"spf":       report.SPF.Verified,
				"dkim":      report.DKIM.Verified,
				"dmarc":     report.DMARC.Verified,
			})
	}
	return ok(c, report)
}
