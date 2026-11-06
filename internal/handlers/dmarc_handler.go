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
	"errors"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// errDomainNotFound is the sentinel resolveDomain pairs with its 404 abort:
// okapi's Abort helpers write the response and return a nil error, so callers
// must treat any error as "response already written" and stop (the
// errAutomationNotFound idiom in automation_handler.go).
var errDomainNotFound = errors.New("domain not found")

// dmarcRecentLimit bounds the records list: the dashboard shows the newest
// report records, not the full history.
const dmarcRecentLimit = 50

// DMARCReportHandler serves the per-domain DMARC aggregate-report read view
// (R-17): the roll-up of the RFC 7489 reports receiving providers sent about
// the tenant's sending domain, plus the newest stored records. Reports are
// ingested by the inbound pipeline; this handler only reads stored state.
// Workspace-scoped like every domain endpoint: a foreign domain answers 404.
type DMARCReportHandler struct {
	reports    *repositories.DMARCReportRepository
	domainRepo *repositories.DomainRepository
	audit      *audit.Logger
}

func NewDMARCReportHandler(reports *repositories.DMARCReportRepository, domainRepo *repositories.DomainRepository, auditLogger *audit.Logger) *DMARCReportHandler {
	return &DMARCReportHandler{reports: reports, domainRepo: domainRepo, audit: auditLogger}
}

// DomainDMARCRequest names one domain.
type DomainDMARCRequest struct {
	ID uint `param:"id"`
}

// DomainDMARCResponse is the R-17 read view: the domain's aggregate summary
// over every stored report and the newest records.
type DomainDMARCResponse struct {
	Summary repositories.DMARCSummary `json:"summary"`
	Records []models.DMARCReport      `json:"records"`
}

// resolveDomain loads the domain and enforces scope ownership; a foreign or
// missing domain is indistinguishable (404). Unlike the domain health
// handler's helper it pairs the abort with a real sentinel error, so a caller
// that ignores the error cannot fall through and write a second response.
func (h *DMARCReportHandler) resolveDomain(c *okapi.Context, domainID uint) (*models.Domain, error) {
	d, err := h.domainRepo.FindByID(domainID)
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		_ = c.AbortNotFound("domain not found")
		return nil, errDomainNotFound
	}
	return d, nil
}

// Get returns the DMARC aggregate-report view from stored state: the summary
// over every report ingested for the domain and the newest records. No
// outbound traffic, safe for page loads. Every access is audit-logged, and
// the queries are workspace-scoped by the resolved domain's workspace.
func (h *DMARCReportHandler) Get(c *okapi.Context, req *DomainDMARCRequest) error {
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return nil // 404 already written; the sentinel keeps the abort visible
	}

	// ownsResource already proved the domain belongs to the caller's
	// workspace, so WorkspaceID is set; the nil guard only keeps the
	// zero-value fallback (which matches nothing) safe.
	var workspaceID uint
	if d.WorkspaceID != nil {
		workspaceID = *d.WorkspaceID
	}
	summary, err := h.reports.DomainSummary(d.ID, workspaceID)
	if err != nil {
		return c.AbortInternalServerError("failed to load DMARC reports", err)
	}
	records, err := h.reports.Recent(d.ID, workspaceID, dmarcRecentLimit)
	if err != nil {
		return c.AbortInternalServerError("failed to load DMARC reports", err)
	}
	if records == nil {
		records = []models.DMARCReport{}
	}

	if h.audit != nil {
		h.audit.LogCtx(c, "domain.dmarc.viewed",
			"Viewed DMARC aggregate reports for \""+d.Domain+"\"", map[string]any{
				"domain_id": d.ID,
				"domain":    d.Domain,
				"records":   summary.RecordCount,
			})
	}
	return ok(c, DomainDMARCResponse{Summary: summary, Records: records})
}
