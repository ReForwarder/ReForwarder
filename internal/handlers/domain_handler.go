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
	"crypto/rand"
	"encoding/hex"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/domain"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

type DomainHandler struct {
	repo  *repositories.DomainRepository
	quota QuotaChecker
	db    *gorm.DB
	audit *audit.Logger
}
type CreateDomainRequest struct {
	Body struct {
		Domain string `json:"domain" required:"true"`
	} `json:"body"`
}
type DomainWithRecords struct {
	models.Domain
	DNSRecords *domain.DNSRecords `json:"dns_records"`
}
type GetDomainRequest struct {
	ID int `param:"id"`
}
type DeleteDomainRequest struct {
	ID int `param:"id"`
}
type VerifyDomainRequest struct {
	ID int `param:"id"`
}

func NewDomainHandler(repo *repositories.DomainRepository) *DomainHandler {
	return &DomainHandler{repo: repo}
}

// SetAuditLogger wires the optional audit logger (follows the domain-health
// handler's pattern; every setter method degrades gracefully when nil).
func (h *DomainHandler) SetAuditLogger(a *audit.Logger) { h.audit = a }

// SetDeliveryModeRequest selects the outbound executor for a domain (OB-1,
// ADR-001): 'relay' (default) or 'direct'.
type SetDeliveryModeRequest struct {
	ID   int `param:"id"`
	Body struct {
		DeliveryMode string `json:"delivery_mode" required:"true"`
	} `json:"body"`
}

// SetDeliveryMode switches a domain between the relay and direct-to-MX
// executors. Fail-closed per ADR-001: 'direct' requires ownership
// verification (the sender domain must be tenant-verified before the MTA
// will ever deliver for it).
func (h *DomainHandler) SetDeliveryMode(c *okapi.Context, req *SetDeliveryModeRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	mode := req.Body.DeliveryMode
	if mode != models.DeliveryModeRelay && mode != models.DeliveryModeDirect {
		return c.AbortBadRequest("delivery_mode must be 'relay' or 'direct'")
	}
	d, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		return c.AbortNotFound("domain not found")
	}
	if mode == models.DeliveryModeDirect && !d.OwnershipVerified {
		return c.AbortBadRequest("direct delivery requires a verified domain")
	}
	if d.DeliveryMode == mode {
		return ok(c, okapi.M{"domain": d, "delivery_mode": mode, "changed": false})
	}
	d.DeliveryMode = mode
	if err := h.repo.Update(d); err != nil {
		return c.AbortInternalServerError("failed to update domain")
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "domain.delivery_mode_changed",
			"Domain delivery mode changed", map[string]any{
				"domain_id": d.ID, "domain": d.Domain, "delivery_mode": mode,
			})
	}
	return ok(c, okapi.M{"domain": d, "delivery_mode": mode, "changed": true})
}

// SetMTASTSModeRequest selects the MTA-STS policy mode this deployment
// publishes for the domain (SF-29, OB-6 W4, ADR-007 §5): ” clears the
// policy (nothing is served), 'none' monitors, 'testing' announces enforce
// semantics without refusing, 'enforce' refuses.
type SetMTASTSModeRequest struct {
	ID   int `param:"id"`
	Body struct {
		Mode string `json:"mode" required:"true"`
	} `json:"body"`
}

// SetMTASTSMode sets the domain's published MTA-STS policy mode. Fail-closed
// per ADR-008 §2: only ownership-verified domains may publish a policy — the
// policy document and the _smtp._tls TXT guidance are only meaningful for a
// domain whose mail this deployment actually receives.
func (h *DomainHandler) SetMTASTSMode(c *okapi.Context, req *SetMTASTSModeRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	mode := req.Body.Mode
	switch mode {
	case "", models.MTASTSModeNone, models.MTASTSModeTesting, models.MTASTSModeEnforce:
	default:
		return c.AbortBadRequest("mode must be '', 'none', 'testing' or 'enforce'")
	}
	d, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		return c.AbortNotFound("domain not found")
	}
	if mode != "" && !d.OwnershipVerified {
		return c.AbortBadRequest("an MTA-STS policy requires a verified domain")
	}
	if d.MTASTSMode == mode {
		return ok(c, okapi.M{"domain": d, "mta_sts_mode": mode, "changed": false})
	}
	d.MTASTSMode = mode
	if err := h.repo.Update(d); err != nil {
		return c.AbortInternalServerError("failed to update domain")
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "domain.mta_sts_mode_changed",
			"Domain MTA-STS policy mode changed", map[string]any{
				"domain_id": d.ID, "domain": d.Domain, "mta_sts_mode": mode,
			})
	}
	return ok(c, okapi.M{"domain": d, "mta_sts_mode": mode, "changed": true})
}

// SetQuota sets the quota checker for plan-based resource limits.
func (h *DomainHandler) SetQuota(q QuotaChecker, db *gorm.DB) {
	h.quota = q
	h.db = db
}

func (h *DomainHandler) Create(c *okapi.Context, req *CreateDomainRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	scope := getScope(c)

	if h.quota != nil {
		if err := h.quota.CheckQuota(h.db, scope.UserID, scope.WorkspaceID, "domains"); err != nil {
			return c.AbortForbidden("Domain quota exceeded for this workspace", err)
		}
	}

	token, err := generateVerificationToken()
	if err != nil {
		return c.AbortInternalServerError("failed to generate verification token")
	}

	d := &models.Domain{
		UserID:            scope.UserID,
		WorkspaceID:       scope.WorkspaceID,
		Domain:            req.Body.Domain,
		VerificationToken: token,
	}

	if err := h.repo.Create(d); err != nil {
		return c.AbortConflict("domain already exists")
	}

	return created(c, DomainWithRecords{
		Domain:     *d,
		DNSRecords: domain.RequiredRecords(d),
	})
}

func (h *DomainHandler) List(c *okapi.Context, req *ListRequest) error {
	page, size, offset := normalizePageParams(req.Page, req.Size)

	domains, total, err := h.repo.FindByScope(getScope(c), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list domains")
	}

	return paginated(c, domains, total, page, size)
}

func (h *DomainHandler) Get(c *okapi.Context, req *GetDomainRequest) error {
	d, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		return c.AbortNotFound("domain not found")
	}

	return ok(c, DomainWithRecords{
		Domain:     *d,
		DNSRecords: domain.RequiredRecords(d),
	})
}

func (h *DomainHandler) Delete(c *okapi.Context, req *DeleteDomainRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		return c.AbortNotFound("domain not found")
	}

	if err := h.repo.Delete(d.ID); err != nil {
		return c.AbortInternalServerError("failed to delete domain")
	}

	return noContent(c)
}

func (h *DomainHandler) Verify(c *okapi.Context, req *VerifyDomainRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		return c.AbortNotFound("domain not found")
	}

	result, err := domain.Verify(d)
	if err != nil {
		return c.AbortInternalServerError("DNS verification failed")
	}

	d.OwnershipVerified = result.OwnershipVerified
	d.SPFVerified = result.SPFVerified
	d.DKIMVerified = result.DKIMVerified
	d.DMARCVerified = result.DMARCVerified

	if err := h.repo.Update(d); err != nil {
		return c.AbortInternalServerError("failed to update domain")
	}

	return ok(c, okapi.M{
		"domain":         d,
		"verification":   result,
		"fully_verified": d.IsFullyVerified(),
	})
}

func generateVerificationToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
