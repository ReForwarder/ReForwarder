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
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// reportObjectRecentLimit bounds the list endpoints: the surfaces show the
// newest entries, not the full history (the tlsRptRecentLimit precedent).
const reportObjectRecentLimit = 50

// ReportObjectHandler serves the SF-57 read surfaces for the report objects
// this deployment SENT: the RFC 7489 aggregate reports dispatched to
// published rua endpoints (SF-25) and the RFC 5965/6591 auth-failure
// feedback reports dispatched to published _arf abuse addresses (SF-26).
//
// Scoping:
//   - the ARF reports attribute to the tenant whose inbound message failed
//     (the verdict's user_id + workspace_id) — the workspace list is
//     ApplyScope-filtered;
//   - the sent DMARC aggregates are deployment-wide (the verdicts of every
//     tenant's inbound traffic claiming the reported domain), so the tenant
//     list scopes by the reported domain: only the aggregates about the
//     tenant's own domains are the tenant's to see;
//   - the admin lists see everything (admin-gated routes).
type ReportObjectHandler struct {
	dmarcSent  *repositories.DMARCSentReportRepository
	arf        *repositories.ARFReportRepository
	domainRepo *repositories.DomainRepository
}

func NewReportObjectHandler(
	dmarcSent *repositories.DMARCSentReportRepository,
	arf *repositories.ARFReportRepository,
	domainRepo *repositories.DomainRepository,
) *ReportObjectHandler {
	return &ReportObjectHandler{dmarcSent: dmarcSent, arf: arf, domainRepo: domainRepo}
}

// domainNamesForScope resolves the scope's own domain names (verified or
// not — mail claiming an unverified tenant domain also draws verdicts and
// reports about it).
func (h *ReportObjectHandler) domainNamesForScope(c *okapi.Context) []string {
	scope := getScope(c)
	if scope.WorkspaceID == nil {
		return nil
	}
	domains, _, err := h.domainRepo.FindByScope(scope, 10000, 0)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(domains))
	for _, d := range domains {
		if d.Domain != "" {
			names = append(names, d.Domain)
		}
	}
	return names
}

// ARFReportsResponse is the tenant's newest sent auth-failure reports.
type ARFReportsResponse struct {
	Reports []models.ARFReport `json:"reports"`
}

// ListARFReportsRequest carries no parameters (the workspace scope resolves
// the actor).
type ListARFReportsRequest struct{}

// ListARFReports returns the newest ARF reports attributed to the current
// workspace scope (the received messages' tenant). Read-only, safe for page
// loads.
func (h *ReportObjectHandler) ListARFReports(c *okapi.Context, _ *ListARFReportsRequest) error {
	rows, err := h.arf.FindByScope(c.Context(), getScope(c), reportObjectRecentLimit)
	if err != nil {
		return c.AbortInternalServerError("failed to load ARF reports", err)
	}
	if rows == nil {
		rows = []models.ARFReport{}
	}
	return ok(c, ARFReportsResponse{Reports: rows})
}

// DomainSentReportsRequest names one domain.
type DomainSentReportsRequest struct {
	ID uint `param:"id"`
}

// DomainSentReportsResponse is the newest sent DMARC aggregates about one
// domain (the DMARC panel's per-domain view).
type DomainSentReportsResponse struct {
	Domain  string                   `json:"domain"`
	Reports []models.DMARCSentReport `json:"reports"`
}

// resolveDomain loads the domain and enforces scope ownership; a foreign or
// missing domain is indistinguishable (404), the TLS-RPT handler's shape.
func (h *ReportObjectHandler) resolveDomain(c *okapi.Context, domainID uint) (*models.Domain, error) {
	d, err := h.domainRepo.FindByID(domainID)
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		_ = c.AbortNotFound("domain not found")
		return nil, errDomainNotFound
	}
	return d, nil
}

// ListDMARCSentReportsByDomain returns the newest RFC 7489 aggregates this
// deployment dispatched whose reported domain is the named domain (the
// per-domain DMARC panel view). Workspace-scoped like every domain endpoint:
// a foreign domain answers 404.
func (h *ReportObjectHandler) ListDMARCSentReportsByDomain(c *okapi.Context, req *DomainSentReportsRequest) error {
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return nil // 404 already written; the sentinel keeps the abort visible
	}
	rows, err := h.dmarcSent.FindRecentByDomains(c.Context(), []string{d.Domain}, reportObjectRecentLimit)
	if err != nil {
		return c.AbortInternalServerError("failed to load DMARC sent reports", err)
	}
	if rows == nil {
		rows = []models.DMARCSentReport{}
	}
	return ok(c, DomainSentReportsResponse{Domain: d.Domain, Reports: rows})
}

// DMARCSentReportsResponse is the newest sent DMARC aggregates about the
// tenant's own domains.
type DMARCSentReportsResponse struct {
	Reports []models.DMARCSentReport `json:"reports"`
}

// ListDMARCSentReports returns the newest RFC 7489 aggregates this
// deployment sent whose reported domain is one of the scope's own domains.
// Read-only, safe for page loads.
// ListDMARCSentReportsRequest carries no parameters.
type ListDMARCSentReportsRequest struct{}

func (h *ReportObjectHandler) ListDMARCSentReports(c *okapi.Context, _ *ListDMARCSentReportsRequest) error {
	names := h.domainNamesForScope(c)
	rows, err := h.dmarcSent.FindRecentByDomains(c.Context(), names, reportObjectRecentLimit)
	if err != nil {
		return c.AbortInternalServerError("failed to load DMARC sent reports", err)
	}
	if rows == nil {
		rows = []models.DMARCSentReport{}
	}
	return ok(c, DMARCSentReportsResponse{Reports: rows})
}

// AdminARFReportsResponse is the deployment-wide ARF report list.
type AdminARFReportsResponse struct {
	Reports []models.ARFReport `json:"reports"`
}

// ListAllARFReports returns the newest ARF reports deployment-wide
// (admin-gated route).
// ListAllARFReportsRequest carries no parameters.
type ListAllARFReportsRequest struct{}

func (h *ReportObjectHandler) ListAllARFReports(c *okapi.Context, _ *ListAllARFReportsRequest) error {
	rows, err := h.arf.FindAll(c.Context(), reportObjectRecentLimit)
	if err != nil {
		return c.AbortInternalServerError("failed to load ARF reports", err)
	}
	if rows == nil {
		rows = []models.ARFReport{}
	}
	return ok(c, AdminARFReportsResponse{Reports: rows})
}

// AdminDMARCSentReportsResponse is the deployment-wide sent-aggregate list.
type AdminDMARCSentReportsResponse struct {
	Reports []models.DMARCSentReport `json:"reports"`
}

// ListAllDMARCSentReports returns the newest sent DMARC aggregates
// deployment-wide (admin-gated route).
// ListAllDMARCSentReportsRequest carries no parameters.
type ListAllDMARCSentReportsRequest struct{}

func (h *ReportObjectHandler) ListAllDMARCSentReports(c *okapi.Context, _ *ListAllDMARCSentReportsRequest) error {
	rows, err := h.dmarcSent.FindAll(c.Context(), reportObjectRecentLimit)
	if err != nil {
		return c.AbortInternalServerError("failed to load DMARC sent reports", err)
	}
	if rows == nil {
		rows = []models.DMARCSentReport{}
	}
	return ok(c, AdminDMARCSentReportsResponse{Reports: rows})
}
