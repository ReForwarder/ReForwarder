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
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// tlsRptRecentLimit bounds the entries list: the dashboard shows the newest
// report entries, not the full history (mirrors the DMARC read surface).
const tlsRptRecentLimit = 50

// TLSRPTHandler serves the per-domain SMTP TLS report read view (SF-31
// inbound half, OB-6 W4; ADR-007 §5): the roll-up of the RFC 8460 reports
// remote sending MTAs delivered to the domain's tlsrpt@ rua address, plus the
// newest stored entries. Reports are ingested by the inbound pipeline; this
// handler only reads stored state. Workspace-scoped like every domain
// endpoint: a foreign domain answers 404.
type TLSRPTHandler struct {
	reports    *repositories.TLSRPTRepository
	domainRepo *repositories.DomainRepository
	audit      *audit.Logger
}

func NewTLSRPTHandler(reports *repositories.TLSRPTRepository, domainRepo *repositories.DomainRepository, auditLogger *audit.Logger) *TLSRPTHandler {
	return &TLSRPTHandler{reports: reports, domainRepo: domainRepo, audit: auditLogger}
}

// DomainTLSRPTRequest names one domain.
type DomainTLSRPTRequest struct {
	ID uint `param:"id"`
}

// DomainTLSRPTResponse is the SF-31 read view: the domain's aggregate summary
// over every stored report and the newest entries.
type DomainTLSRPTResponse struct {
	Summary repositories.TLSRPTSummary `json:"summary"`
	Reports []models.TLSRPTReport      `json:"reports"`
}

// resolveDomain loads the domain and enforces scope ownership; a foreign or
// missing domain is indistinguishable (404). Pairs the abort with a sentinel
// error like the DMARC handler's helper, so a caller that ignores the error
// cannot fall through and write a second response.
func (h *TLSRPTHandler) resolveDomain(c *okapi.Context, domainID uint) (*models.Domain, error) {
	d, err := h.domainRepo.FindByID(domainID)
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		_ = c.AbortNotFound("domain not found")
		return nil, errDomainNotFound
	}
	return d, nil
}

// Get returns the TLS report view from stored state: the summary over every
// report ingested for the domain and the newest entries. No outbound
// traffic, safe for page loads. Every access is audit-logged, and the queries
// are workspace-scoped by the resolved domain's workspace.
func (h *TLSRPTHandler) Get(c *okapi.Context, req *DomainTLSRPTRequest) error {
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return nil // 404 already written; the sentinel keeps the abort visible
	}

	var workspaceID uint
	if d.WorkspaceID != nil {
		workspaceID = *d.WorkspaceID
	}
	summary, err := h.reports.Summary(d.ID, workspaceID)
	if err != nil {
		return c.AbortInternalServerError("failed to load TLS reports", err)
	}
	reports, err := h.reports.Recent(d.ID, workspaceID, tlsRptRecentLimit)
	if err != nil {
		return c.AbortInternalServerError("failed to load TLS reports", err)
	}
	if reports == nil {
		reports = []models.TLSRPTReport{}
	}

	if h.audit != nil {
		h.audit.LogCtx(c, "domain.tlsrpt.viewed",
			"Viewed SMTP TLS reports for \""+d.Domain+"\"", map[string]any{
				"domain_id": d.ID,
				"domain":    d.Domain,
				"reports":   summary.ReportCount,
			})
	}
	return ok(c, DomainTLSRPTResponse{Summary: summary, Reports: reports})
}
