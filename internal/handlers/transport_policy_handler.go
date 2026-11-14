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
	"net"
	"strings"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/conditions"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// TransportPolicyHandler is the SF-50 transport-policy admin surface (the
// ADR-001 amendment): CRUD over a domain's per-domain transport policy rules
// — the tenant administrator's gate. It mirrors the SF-63 report-settings
// posture exactly: owner-scoped (requireEdit + the ownership 404, a foreign
// domain or policy reading as missing), the domain must be the workspace's
// own, condition sets validated through the SAME strict evaluator the MTA
// gates run (an unknown field/operator or a non-parsable numeric is a 400 —
// the fail-closed grammar never reaches the table), and changes audited.
type TransportPolicyHandler struct {
	policies *repositories.TransportPolicyRepository
	domains  *repositories.DomainRepository
	audit    *audit.Logger
}

func NewTransportPolicyHandler(policies *repositories.TransportPolicyRepository, domains *repositories.DomainRepository) *TransportPolicyHandler {
	return &TransportPolicyHandler{policies: policies, domains: domains}
}

// SetAuditLogger wires the audit trail (the policy-change events). Nil
// logger: changes succeed without an audit entry (the established posture).
func (h *TransportPolicyHandler) SetAuditLogger(a *audit.Logger) { h.audit = a }

// ─── Requests / responses ────────────────────────────────────────────────────

type TransportPolicyListRequest struct {
	ID int `param:"id" doc:"Domain ID; the workspace's own"`
}

type TransportPolicyCreateRequest struct {
	ID int `param:"id" doc:"Domain ID; the workspace's own"`
	Body struct {
		Name           string              `json:"name" required:"true" minLength:"1" maxLength:"255" doc:"Policy name"`
		Priority       int                 `json:"priority" doc:"Evaluation order (lower first); first match wins"`
		Enabled        *bool               `json:"enabled" doc:"Defaults to true"`
		Conditions     []models.FilterRule `json:"conditions" doc:"Envelope-fact rules (mail_from, rcpt_to, helo, authenticated_as, sender_domain, message_size)"`
		Reject         bool                `json:"reject" doc:"Refuse matching recipients at RCPT"`
		RejectReason   string              `json:"reject_reason" maxLength:"255" doc:"The 550 reply text"`
		BindIP         string              `json:"bind_ip" doc:"Pin the outbound source address"`
		MsgRatePerMin  int                 `json:"msg_rate_per_min" doc:"Override the per-message send-rate pacing"`
		MaxMessageSize int64               `json:"max_message_size" doc:"Override the outbound size cap (octets)"`
		HourlyBudget   int                 `json:"hourly_budget" doc:"Override the per-hour delivery budget"`
	} `json:"body"`
}

type TransportPolicyPatchRequest struct {
	ID   int `param:"id" doc:"Domain ID; the workspace's own"`
	PID  int `param:"pid" doc:"Policy ID"`
	Body struct {
		Name           *string             `json:"name" maxLength:"255"`
		Priority       *int                `json:"priority"`
		Enabled        *bool               `json:"enabled"`
		Conditions     *[]models.FilterRule `json:"conditions"`
		Reject         *bool               `json:"reject"`
		RejectReason   *string             `json:"reject_reason" maxLength:"255"`
		BindIP         *string             `json:"bind_ip"`
		MsgRatePerMin  *int                `json:"msg_rate_per_min"`
		MaxMessageSize *int64              `json:"max_message_size"`
		HourlyBudget   *int                `json:"hourly_budget"`
	} `json:"body"`
}

type TransportPolicyIDRequest struct {
	ID  int `param:"id" doc:"Domain ID; the workspace's own"`
	PID int `param:"pid" doc:"Policy ID"`
}

type TransportPolicyListResponse struct {
	Policies []models.TransportPolicy `json:"policies"`
}

// ─── Validation ──────────────────────────────────────────────────────────────

// validateTransportConditions runs the rule set through the SAME strict
// evaluator the MTA gates run, over a complete sample subject: any unknown
// field/operator or non-parsable numeric is a 400 — the fail-closed grammar
// never reaches the table.
func validateTransportConditions(rules []models.FilterRule) error {
	sample := map[string]string{
		conditions.TransportFieldMailFrom:        "",
		conditions.TransportFieldRcptTo:          "",
		conditions.TransportFieldHelo:            "",
		conditions.TransportFieldAuthenticatedAs: "",
		conditions.TransportFieldSenderDomain:    "",
		conditions.TransportFieldMessageSize:     "0",
	}
	if _, err := conditions.EvaluateTransportRules(rules, sample); err != nil {
		return err
	}
	return nil
}

// validateTransportOverride checks the override fields (sane ranges, a
// parseable bind IP, a reason length bound).
func validateTransportOverride(reject bool, rejectReason, bindIP string, msgRatePerMin int, maxMessageSize int64, hourlyBudget int) error {
	if len(rejectReason) > 255 {
		return errors.New("reject_reason must be at most 255 characters")
	}
	if bindIP != "" && net.ParseIP(strings.TrimSpace(bindIP)) == nil {
		return errors.New("bind_ip is not a valid IP address")
	}
	if msgRatePerMin < 0 || msgRatePerMin > 100000 {
		return errors.New("msg_rate_per_min must be between 0 and 100000")
	}
	if maxMessageSize < 0 {
		return errors.New("max_message_size must be non-negative")
	}
	if hourlyBudget < 0 || hourlyBudget > 1000000 {
		return errors.New("hourly_budget must be between 0 and 1000000")
	}
	if !reject && strings.TrimSpace(rejectReason) != "" {
		return errors.New("reject_reason requires reject")
	}
	return nil
}

// resolvePolicyDomain loads the domain, enforcing the ownership 404 (a
// foreign domain reads as missing — isolation never leaks existence).
func (h *TransportPolicyHandler) resolveDomain(c *okapi.Context, domainID uint) (*models.Domain, error) {
	d, err := h.domains.FindByID(domainID)
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		_ = c.AbortNotFound("domain not found")
		return nil, errDomainNotFound
	}
	return d, nil
}

// resolvePolicy loads one of the workspace's policies bound to this domain
// (both scoping keys must hold; a foreign policy reads as missing).
func (h *TransportPolicyHandler) resolvePolicy(c *okapi.Context, d *models.Domain, policyID uint) (*models.TransportPolicy, error) {
	p, err := h.policies.FindByIDForWorkspace(policyID, domainWorkspace(d))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = c.AbortNotFound("transport policy not found")
			return nil, errDomainNotFound
		}
		_ = c.AbortInternalServerError("failed to load the transport policy", err)
		return nil, errDomainNotFound
	}
	if !strings.EqualFold(p.DomainName, d.Domain) {
		// The policy must belong to the addressed domain.
		_ = c.AbortNotFound("transport policy not found")
		return nil, errDomainNotFound
	}
	return p, nil
}

// domainWorkspace renders the domain's workspace ownership as the policy
// row's non-null workspace id; a personal (workspace-less) domain has no
// transport-policy surface — 0 matches nothing in the repository.
func domainWorkspace(d *models.Domain) uint {
	if d.WorkspaceID == nil {
		return 0
	}
	return *d.WorkspaceID
}

// ─── Endpoints ───────────────────────────────────────────────────────────────

// ListTransportPolicies returns the domain's policies in evaluation order.
func (h *TransportPolicyHandler) ListTransportPolicies(c *okapi.Context, req *TransportPolicyListRequest) error {
	if h.policies == nil || h.domains == nil {
		return c.AbortServiceUnavailable("transport policies are not available on this deployment")
	}
	d, err := h.resolveDomain(c, uint(req.ID))
	if err != nil {
		return nil // 404 already written
	}
	policies, err := h.policies.ListForWorkspaceDomain(domainWorkspace(d), d.Domain)
	if err != nil {
		return c.AbortInternalServerError("failed to load transport policies", err)
	}
	if policies == nil {
		policies = []models.TransportPolicy{}
	}
	return ok(c, TransportPolicyListResponse{Policies: policies})
}

// CreateTransportPolicy adds a policy rule to the domain.
func (h *TransportPolicyHandler) CreateTransportPolicy(c *okapi.Context, req *TransportPolicyCreateRequest) error {
	if h.policies == nil || h.domains == nil {
		return c.AbortServiceUnavailable("transport policies are not available on this deployment")
	}
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.resolveDomain(c, uint(req.ID))
	if err != nil {
		return nil // 404 already written
	}
	if err := validateTransportConditions(req.Body.Conditions); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	wsID := domainWorkspace(d)
	if wsID == 0 {
		return c.AbortBadRequest("transport policies require a workspace-bound domain")
	}
	enabled := true
	if req.Body.Enabled != nil {
		enabled = *req.Body.Enabled
	}
	if err := validateTransportOverride(req.Body.Reject, req.Body.RejectReason, req.Body.BindIP,
		req.Body.MsgRatePerMin, req.Body.MaxMessageSize, req.Body.HourlyBudget); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	p := &models.TransportPolicy{
		WorkspaceID:    wsID,
		DomainName:     strings.ToLower(strings.TrimSpace(d.Domain)),
		Name:           strings.TrimSpace(req.Body.Name),
		Priority:       req.Body.Priority,
		Enabled:        enabled,
		Conditions:     models.FilterRules(req.Body.Conditions),
		Reject:         req.Body.Reject,
		RejectReason:   req.Body.RejectReason,
		BindIP:         strings.TrimSpace(req.Body.BindIP),
		MsgRatePerMin:  req.Body.MsgRatePerMin,
		MaxMessageSize: req.Body.MaxMessageSize,
		HourlyBudget:   req.Body.HourlyBudget,
	}
	if err := h.policies.Create(p); err != nil {
		return c.AbortInternalServerError("failed to create the transport policy", err)
	}
	h.auditTransportPolicyChange(c, "domain.transport_policy_created", d.Domain, p.ID)
	return created(c, p)
}

// PatchTransportPolicy updates one of the domain's policies.
func (h *TransportPolicyHandler) PatchTransportPolicy(c *okapi.Context, req *TransportPolicyPatchRequest) error {
	if h.policies == nil || h.domains == nil {
		return c.AbortServiceUnavailable("transport policies are not available on this deployment")
	}
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, derr := h.resolveDomain(c, uint(req.ID))
	if derr != nil {
		return nil // 404 already written
	}
	p, perr := h.resolvePolicy(c, d, uint(req.PID))
	if perr != nil {
		return nil // 404 already written
	}
	if req.Body.Name != nil {
		name := strings.TrimSpace(*req.Body.Name)
		if name == "" || len(name) > 255 {
			return c.AbortBadRequest("name must be 1..255 characters")
		}
		p.Name = name
	}
	if req.Body.Priority != nil {
		p.Priority = *req.Body.Priority
	}
	if req.Body.Enabled != nil {
		p.Enabled = *req.Body.Enabled
	}
	if req.Body.Conditions != nil {
		if err := validateTransportConditions(*req.Body.Conditions); err != nil {
			return c.AbortBadRequest(err.Error())
		}
		p.Conditions = models.FilterRules(*req.Body.Conditions)
	}
	if req.Body.Reject != nil {
		p.Reject = *req.Body.Reject
	}
	if req.Body.RejectReason != nil {
		p.RejectReason = *req.Body.RejectReason
	}
	if req.Body.BindIP != nil {
		p.BindIP = strings.TrimSpace(*req.Body.BindIP)
	}
	if req.Body.MsgRatePerMin != nil {
		p.MsgRatePerMin = *req.Body.MsgRatePerMin
	}
	if req.Body.MaxMessageSize != nil {
		p.MaxMessageSize = *req.Body.MaxMessageSize
	}
	if req.Body.HourlyBudget != nil {
		p.HourlyBudget = *req.Body.HourlyBudget
	}
	if err := validateTransportOverride(p.Reject, p.RejectReason, p.BindIP, p.MsgRatePerMin, p.MaxMessageSize, p.HourlyBudget); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if err := h.policies.Update(p); err != nil {
		return c.AbortInternalServerError("failed to update the transport policy", err)
	}
	h.auditTransportPolicyChange(c, "domain.transport_policy_changed", d.Domain, p.ID)
	return ok(c, p)
}

// DeleteTransportPolicy removes one of the domain's policies.
func (h *TransportPolicyHandler) DeleteTransportPolicy(c *okapi.Context, req *TransportPolicyIDRequest) error {
	if h.policies == nil || h.domains == nil {
		return c.AbortServiceUnavailable("transport policies are not available on this deployment")
	}
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, derr := h.resolveDomain(c, uint(req.ID))
	if derr != nil {
		return nil // 404 already written
	}
	if _, err := h.resolvePolicy(c, d, uint(req.PID)); err != nil {
		return nil // 404 already written
	}
	n, err := h.policies.DeleteForWorkspace(uint(req.PID), domainWorkspace(d))
	if err != nil {
		return c.AbortInternalServerError("failed to delete the transport policy", err)
	}
	if n == 0 {
		return c.AbortNotFound("transport policy not found")
	}
	h.auditTransportPolicyChange(c, "domain.transport_policy_deleted", d.Domain, uint(req.PID))
	return ok(c, okapi.M{"message": "transport policy deleted"})
}

// auditTransportPolicyChange writes the audit event (best-effort; wired
// seams only).
func (h *TransportPolicyHandler) auditTransportPolicyChange(c *okapi.Context, action, domain string, policyID uint) {
	if h.audit != nil {
		h.audit.LogCtx(c, action, "Transport policy changed", map[string]any{
			"domain": domain, "policy_id": policyID,
		})
	}
}
