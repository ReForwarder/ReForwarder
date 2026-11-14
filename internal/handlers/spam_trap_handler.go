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
	"strings"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// SpamTrapHandler is the SF-42 spam-trap admin surface (the ADR-006
// amendment): CRUD over a domain's seeded trap addresses — the tenant
// administrator's gate. It mirrors the transport-policy posture exactly:
// owner-scoped (requireEdit + the ownership 404), the domain must be the
// workspace's own, the trap bound to the domain's workspace, changes
// audited. The local part is lowercased (the ingest gate matches
// lowercased recipients).
type SpamTrapHandler struct {
	traps   *repositories.SpamTrapRepository
	domains *repositories.DomainRepository
	audit   *audit.Logger
}

func NewSpamTrapHandler(traps *repositories.SpamTrapRepository, domains *repositories.DomainRepository) *SpamTrapHandler {
	return &SpamTrapHandler{traps: traps, domains: domains}
}

// SetAuditLogger wires the audit trail (the trap-change events). Nil
// logger: changes succeed without an audit entry (the established posture).
func (h *SpamTrapHandler) SetAuditLogger(a *audit.Logger) { h.audit = a }

// ─── Requests / responses ────────────────────────────────────────────────────

type SpamTrapListRequest struct {
	ID int `param:"id" doc:"Domain ID; the workspace's own"`
}

type SpamTrapCreateRequest struct {
	ID int `param:"id" doc:"Domain ID; the workspace's own"`
	Body struct {
		LocalPart string `json:"local_part" required:"true" minLength:"1" maxLength:"255" doc:"The trap's local part (lowercased; the address is local_part@domain)"`
		Note      string `json:"note" maxLength:"255" doc:"Optional operator note (e.g. where the address was published)"`
	} `json:"body"`
}

type SpamTrapIDRequest struct {
	ID  int `param:"id" doc:"Domain ID; the workspace's own"`
	TID int `param:"tid" doc:"Trap ID"`
}

type SpamTrapListResponse struct {
	Traps []models.SpamTrap `json:"traps"`
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// resolveTrapDomain loads the domain, enforcing the ownership 404.
func (h *SpamTrapHandler) resolveTrapDomain(c *okapi.Context, domainID uint) (*models.Domain, error) {
	d, err := h.domains.FindByID(domainID)
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		_ = c.AbortNotFound("domain not found")
		return nil, errDomainNotFound
	}
	return d, nil
}

// resolveTrap loads one of the workspace's traps bound to this domain.
func (h *SpamTrapHandler) resolveTrap(c *okapi.Context, d *models.Domain, trapID uint) (*models.SpamTrap, error) {
	trap, err := h.traps.FindByIDForWorkspace(trapID, domainWorkspace(d))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = c.AbortNotFound("spam trap not found")
			return nil, errDomainNotFound
		}
		_ = c.AbortInternalServerError("failed to load the spam trap", err)
		return nil, errDomainNotFound
	}
	if !strings.EqualFold(trap.DomainName, d.Domain) {
		_ = c.AbortNotFound("spam trap not found")
		return nil, errDomainNotFound
	}
	return trap, nil
}

// auditSpamTrapChange writes the audit event (best-effort; wired seams only).
func (h *SpamTrapHandler) auditSpamTrapChange(c *okapi.Context, action, domain string, trapID uint) {
	if h.audit != nil {
		h.audit.LogCtx(c, action, "Spam trap changed", map[string]any{
			"domain": domain, "trap_id": trapID,
		})
	}
}

// ─── Endpoints ───────────────────────────────────────────────────────────────

// ListSpamTraps returns the domain's seeded traps, oldest first.
func (h *SpamTrapHandler) ListSpamTraps(c *okapi.Context, req *SpamTrapListRequest) error {
	if h.traps == nil || h.domains == nil {
		return c.AbortServiceUnavailable("spam traps are not available on this deployment")
	}
	d, err := h.resolveTrapDomain(c, uint(req.ID))
	if err != nil {
		return nil // 404 already written
	}
	traps, err := h.traps.ListForWorkspaceDomain(domainWorkspace(d), d.Domain)
	if err != nil {
		return c.AbortInternalServerError("failed to load spam traps", err)
	}
	if traps == nil {
		traps = []models.SpamTrap{}
	}
	return ok(c, SpamTrapListResponse{Traps: traps})
}

// CreateSpamTrap seeds one trap address on the domain.
func (h *SpamTrapHandler) CreateSpamTrap(c *okapi.Context, req *SpamTrapCreateRequest) error {
	if h.traps == nil || h.domains == nil {
		return c.AbortServiceUnavailable("spam traps are not available on this deployment")
	}
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.resolveTrapDomain(c, uint(req.ID))
	if err != nil {
		return nil // 404 already written
	}
	local := strings.ToLower(strings.TrimSpace(req.Body.LocalPart))
	if local == "" || len(local) > 255 || strings.Contains(local, "@") || strings.ContainsAny(local, " \t\r\n") {
		return c.AbortBadRequest("local_part must be 1..255 characters without @ or whitespace")
	}
	trap := &models.SpamTrap{
		WorkspaceID: domainWorkspace(d),
		DomainName:  strings.ToLower(strings.TrimSpace(d.Domain)),
		LocalPart:   local,
		Note:        strings.TrimSpace(req.Body.Note),
	}
	if err := h.traps.Create(trap); err != nil {
		return c.AbortInternalServerError("failed to create the spam trap", err)
	}
	h.auditSpamTrapChange(c, "domain.spam_trap_created", d.Domain, trap.ID)
	return created(c, trap)
}

// DeleteSpamTrap removes one of the domain's seeded traps; a foreign or
// missing trap reads as 404 like a missing one.
func (h *SpamTrapHandler) DeleteSpamTrap(c *okapi.Context, req *SpamTrapIDRequest) error {
	if h.traps == nil || h.domains == nil {
		return c.AbortServiceUnavailable("spam traps are not available on this deployment")
	}
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, derr := h.resolveTrapDomain(c, uint(req.ID))
	if derr != nil {
		return nil // 404 already written
	}
	if _, terr := h.resolveTrap(c, d, uint(req.TID)); terr != nil {
		return nil // 404 already written
	}
	n, err := h.traps.DeleteForWorkspace(uint(req.TID), domainWorkspace(d))
	if err != nil {
		return c.AbortInternalServerError("failed to delete the spam trap", err)
	}
	if n == 0 {
		return c.AbortNotFound("spam trap not found")
	}
	h.auditSpamTrapChange(c, "domain.spam_trap_deleted", d.Domain, uint(req.TID))
	return ok(c, okapi.M{"message": "spam trap deleted"})
}
