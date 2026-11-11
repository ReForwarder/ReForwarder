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
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/reputation"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// IPPoolHandler serves the OB-6 W3 IP-pool surface (ADR-007 decision 7):
// workspace-side pool listing/creation, domain pool assignment, warm-up
// schedule management and the per-domain reputation detail — plus the
// deployment-wide admin view. Every workspace path scopes through the
// workspace context; admin paths are gated by the admin middleware group.
type IPPoolHandler struct {
	pools      *repositories.IPPoolRepository
	domainRepo *repositories.DomainRepository
	rep        *reputation.Service
	audit      *audit.Logger
}

func NewIPPoolHandler(
	pools *repositories.IPPoolRepository,
	domainRepo *repositories.DomainRepository,
	rep *reputation.Service,
	auditLogger *audit.Logger,
) *IPPoolHandler {
	return &IPPoolHandler{pools: pools, domainRepo: domainRepo, rep: rep, audit: auditLogger}
}

// ---------- wire shapes ----------

// IPoolWire is the wire shape of one pool with its members. IPs are
// configuration data the operator manages; no credentials are involved.
type IPoolWire struct {
	ID           uint             `json:"id"`
	WorkspaceID  *uint            `json:"workspace_id,omitempty"`
	Scope        string           `json:"scope"`
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	Strategy     string           `json:"strategy"`
	Members      []IPoolMemberWire `json:"members"`
	DomainsUsing int64            `json:"domains_using"`
}

// IPoolMemberWire is one member IP of a pool.
type IPoolMemberWire struct {
	ID      uint   `json:"id"`
	IP      string `json:"ip"`
	PTRName string `json:"ptr_name,omitempty"`
	Enabled bool   `json:"enabled"`
}

func poolWire(p *models.IPPool, members []models.IPPoolMember, domainsUsing int64) IPoolWire {
	out := IPoolWire{
		ID:           p.ID,
		WorkspaceID:  p.WorkspaceID,
		Scope:        p.Scope(),
		Name:         p.Name,
		Description:  p.Description,
		Strategy:     p.Strategy,
		Members:      []IPoolMemberWire{},
		DomainsUsing: domainsUsing,
	}
	for i := range members {
		out.Members = append(out.Members, IPoolMemberWire{
			ID:      members[i].ID,
			IP:      members[i].IP,
			PTRName: members[i].PTRName,
			Enabled: members[i].Enabled,
		})
	}
	return out
}

// ---------- requests ----------

// ListIPPoolsRequest is the (empty) request of the pool list endpoints.
type ListIPPoolsRequest struct{}

// CreateIPPoolRequest is the body of POST /workspaces/current/ip-pools.
type CreateIPPoolRequest struct {
	Name        string `json:"name" required:"true"`
	Description string `json:"description"`
	Strategy    string `json:"strategy"`
}

// AdminCreateIPPoolRequest is the body of POST /admin/ip-pools; workspace_id
// optionally scopes the pool (nil = deployment-wide shared pool).
type AdminCreateIPPoolRequest struct {
	Name        string `json:"name" required:"true"`
	Description string `json:"description"`
	Strategy    string `json:"strategy"`
	WorkspaceID *uint  `json:"workspace_id"`
}

// PoolMemberRequest names a pool; member bodies nest under Body.
type PoolMemberRequest struct {
	PoolID uint `param:"poolId"`
	Body   struct {
		IP      string `json:"ip" required:"true"`
		PTRName string `json:"ptr_name"`
		Enabled *bool  `json:"enabled"`
	} `json:"body"`
}

// PoolPathRequest names one pool on the path.
type PoolPathRequest struct {
	PoolID uint `param:"poolId"`
}

// MemberPathRequest names one member of one pool.
type MemberPathRequest struct {
	PoolID   uint `param:"poolId"`
	MemberID uint `param:"memberId"`
}

// AssignDomainPoolRequest is the body of PUT /domains/{id}/pool; a nil
// pool_id clears the assignment.
type AssignDomainPoolRequest struct {
	ID   uint `param:"id"`
	Body struct {
		PoolID *uint `json:"pool_id"`
	} `json:"body"`
}

// SetDomainWarmupRequest is the body of PUT /domains/{id}/warmup.
type SetDomainWarmupRequest struct {
	ID   uint `param:"id"`
	Body struct {
		StartVolume     int     `json:"start_volume"`
		DailyMultiplier float64 `json:"daily_multiplier"`
		Days            int     `json:"days"`
	} `json:"body"`
}

// DomainPathRequest names one domain on the path.
type DomainPathRequest struct {
	ID uint `param:"id"`
}

// DomainReputationRequest is the request of GET /domains/{id}/reputation.
type DomainReputationRequest struct {
	ID   uint `param:"id"`
	Days int  `query:"days"`
}

// AdminIPReputationRequest is the request of GET /admin/ip-reputation.
type AdminIPReputationRequest struct {
	IP   string `query:"ip" required:"true"`
	Days int    `query:"days"`
}

// ---------- workspace surface ----------

// resolveDomain loads the domain and enforces scope ownership; a foreign or
// missing domain is indistinguishable (404).
func (h *IPPoolHandler) resolveDomain(c *okapi.Context, domainID uint) (*models.Domain, error) {
	d, err := h.domainRepo.FindByID(domainID)
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		return nil, gorm.ErrRecordNotFound
	}
	return d, nil
}

// ListPools returns the pools visible to the workspace: its own plus the
// deployment-wide shared ones, with their members and usage counts.
func (h *IPPoolHandler) ListPools(c *okapi.Context, _ *ListIPPoolsRequest) error {
	scope := getScope(c)
	if scope.WorkspaceID == nil {
		return c.AbortBadRequest("no workspace context")
	}
	out, err := h.poolsForWorkspace(*scope.WorkspaceID)
	if err != nil {
		return c.AbortInternalServerError("failed to load IP pools", err)
	}
	return ok(c, out)
}

func (h *IPPoolHandler) poolsForWorkspace(workspaceID uint) ([]IPoolWire, error) {
	items, _, err := h.pools.FindVisiblePools(workspaceID, 200, 0)
	if err != nil {
		return nil, err
	}
	out := make([]IPoolWire, 0, len(items))
	for i := range items {
		members, err := h.pools.PoolMembers(items[i].ID)
		if err != nil {
			return nil, err
		}
		out = append(out, poolWire(&items[i], members, h.pools.DomainCountForPool(items[i].ID)))
	}
	return out, nil
}

// CreatePool creates a workspace-private pool. Shared (deployment-wide)
// pools are the admin surface's job — a workspace can never mint one.
func (h *IPPoolHandler) CreatePool(c *okapi.Context, req *CreateIPPoolRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	scope := getScope(c)
	if scope.WorkspaceID == nil {
		return c.AbortBadRequest("no workspace context")
	}
	pool, err := buildPool(req.Name, req.Description, req.Strategy, scope.WorkspaceID, scope.UserID)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if err := h.pools.CreatePool(pool); err != nil {
		return c.AbortInternalServerError("failed to create IP pool", err)
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "domain.ip_pool.created",
			"Created IP pool \""+pool.Name+"\"", map[string]any{"pool_id": pool.ID, "name": pool.Name})
	}
	return created(c, poolWire(pool, nil, 0))
}

// DeletePool removes a workspace-owned pool that no domain references.
func (h *IPPoolHandler) DeletePool(c *okapi.Context, req *PoolPathRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	pool, err := h.ownedPool(c, req.PoolID)
	if err != nil {
		return c.AbortNotFound("pool not found")
	}
	if err := h.pools.DeletePool(pool.ID); err != nil {
		if errors.Is(err, repositories.ErrPoolHasDomains) {
			return c.AbortBadRequest("pool still has domains assigned — clear the assignments first")
		}
		return c.AbortInternalServerError("failed to delete IP pool", err)
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "domain.ip_pool.deleted",
			"Deleted IP pool \""+pool.Name+"\"", map[string]any{"pool_id": pool.ID, "name": pool.Name})
	}
	return noContent(c)
}

// ownedPool loads a pool and verifies the caller's workspace owns it —
// shared pools are usable but not editable from the workspace surface.
func (h *IPPoolHandler) ownedPool(c *okapi.Context, poolID uint) (*models.IPPool, error) {
	scope := getScope(c)
	pool, err := h.pools.FindPoolByID(poolID)
	if err != nil || pool.WorkspaceID == nil || scope.WorkspaceID == nil || *pool.WorkspaceID != *scope.WorkspaceID {
		return nil, gorm.ErrRecordNotFound
	}
	return pool, nil
}

// AddPoolMember adds a member IP to a workspace-owned pool.
func (h *IPPoolHandler) AddPoolMember(c *okapi.Context, req *PoolMemberRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	pool, err := h.ownedPool(c, req.PoolID)
	if err != nil {
		return c.AbortNotFound("pool not found")
	}
	member, err := buildMember(pool, req.Body.IP, req.Body.PTRName, req.Body.Enabled)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if err := h.pools.AddMember(member); err != nil {
		return c.AbortInternalServerError("failed to add pool member", err)
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "domain.ip_pool.member_added",
			"Added "+member.IP+" to IP pool \""+pool.Name+"\"", map[string]any{
				"pool_id": pool.ID, "member_id": member.ID, "ip": member.IP,
			})
	}
	return created(c, IPoolMemberWire{ID: member.ID, IP: member.IP, PTRName: member.PTRName, Enabled: member.Enabled})
}

// RemovePoolMember deletes a member IP from a workspace-owned pool.
func (h *IPPoolHandler) RemovePoolMember(c *okapi.Context, req *MemberPathRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	pool, err := h.ownedPool(c, req.PoolID)
	if err != nil {
		return c.AbortNotFound("pool not found")
	}
	member, err := h.pools.FindMemberByID(req.MemberID)
	if err != nil || member.PoolID != pool.ID {
		return c.AbortNotFound("pool member not found")
	}
	if err := h.pools.RemoveMember(member.ID); err != nil {
		return c.AbortInternalServerError("failed to remove pool member", err)
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "domain.ip_pool.member_removed",
			"Removed "+member.IP+" from IP pool \""+pool.Name+"\"", map[string]any{
				"pool_id": pool.ID, "member_id": member.ID, "ip": member.IP,
			})
	}
	return noContent(c)
}

// AssignDomainPool points (or clears) a domain's IP pool assignment. The
// pool must be visible to the domain's workspace — a workspace can never
// point its domains at another tenant's private pool (404, indistinguishable
// from a missing pool, like every cross-tenant read here).
func (h *IPPoolHandler) AssignDomainPool(c *okapi.Context, req *AssignDomainPoolRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	scope := getScope(c)
	if scope.WorkspaceID == nil {
		return c.AbortBadRequest("no workspace context")
	}
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	if req.Body.PoolID != nil {
		pool, err := h.pools.FindPoolByID(*req.Body.PoolID)
		if err != nil || (pool.WorkspaceID != nil && *pool.WorkspaceID != *scope.WorkspaceID) {
			return c.AbortNotFound("pool not found")
		}
	}
	if err := h.pools.SetDomainPool(d.ID, req.Body.PoolID); err != nil {
		return c.AbortInternalServerError("failed to assign pool", err)
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "domain.pool.assigned",
			"Updated IP pool assignment for \""+d.Domain+"\"", map[string]any{
				"domain_id": d.ID, "pool_id": req.Body.PoolID,
			})
	}
	return ok(c, map[string]any{"domain_id": d.ID, "pool_id": req.Body.PoolID})
}

// SetDomainWarmup replaces the domain's active warm-up schedule (the previous
// one is deactivated, kept for the record). Validation: a positive ramp —
// warm-up only means something for a domain that sends.
func (h *IPPoolHandler) SetDomainWarmup(c *okapi.Context, req *SetDomainWarmupRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	scope := getScope(c)
	if scope.WorkspaceID == nil {
		return c.AbortBadRequest("no workspace context")
	}
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	volume, mult, days := req.Body.StartVolume, req.Body.DailyMultiplier, req.Body.Days
	if mult == 0 {
		mult = 1
	}
	if volume < 0 || mult < 0 || days < 0 || (days > 0 && volume <= 0) {
		return c.AbortBadRequest("warm-up schedule needs start_volume > 0, days > 0 and a non-negative multiplier")
	}
	schedule := &models.IPWarmupSchedule{
		WorkspaceID:     d.WorkspaceID,
		UserID:          d.UserID,
		DomainID:        d.ID,
		Domain:          d.Domain,
		StartVolume:     volume,
		DailyMultiplier: mult,
		Days:            days,
		StartedAt:       time.Now().UTC(),
		Active:          true,
	}
	if err := h.pools.UpsertWarmup(schedule); err != nil {
		return c.AbortInternalServerError("failed to set warm-up schedule", err)
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "domain.warmup.set",
			"Set warm-up schedule for \""+d.Domain+"\"", map[string]any{
				"domain_id": d.ID, "start_volume": volume,
				"daily_multiplier": mult, "days": days,
			})
	}
	return ok(c, map[string]any{
		"domain_id": d.ID, "active": true,
		"start_volume": volume, "daily_multiplier": mult, "days": days,
	})
}

// DomainReputation returns the per-source-IP reputation surface for one
// domain (OB-6 W3): transport outcomes per pool/bind IP, recipient-side
// bounce/complaint rates, and the daily trend.
func (h *IPPoolHandler) DomainReputation(c *okapi.Context, req *DomainReputationRequest) error {
	scope := getScope(c)
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	window := reputation.DefaultReportWindow
	if req.Days > 0 && req.Days <= 90 {
		window = time.Duration(req.Days) * 24 * time.Hour
	}
	report, err := h.rep.DomainReputationReport(scope, d.Domain, window)
	if err != nil {
		return c.AbortInternalServerError("failed to build reputation report", err)
	}
	return ok(c, report)
}

// ---------- admin surface (gated by the admin middleware group) ----------

// AdminListPools returns every pool in the deployment with members and
// domain counts.
func (h *IPPoolHandler) AdminListPools(c *okapi.Context, _ *ListIPPoolsRequest) error {
	items, _, err := h.pools.FindAllPools(500, 0)
	if err != nil {
		return c.AbortInternalServerError("failed to load IP pools", err)
	}
	out := make([]IPoolWire, 0, len(items))
	for i := range items {
		members, err := h.pools.PoolMembers(items[i].ID)
		if err != nil {
			return c.AbortInternalServerError("failed to load IP pool members", err)
		}
		out = append(out, poolWire(&items[i], members, h.pools.DomainCountForPool(items[i].ID)))
	}
	return ok(c, out)
}

// AdminCreatePool creates a pool. A nil workspace_id makes it a
// deployment-wide shared pool; a workspace_id scopes it to that workspace.
func (h *IPPoolHandler) AdminCreatePool(c *okapi.Context, req *AdminCreateIPPoolRequest) error {
	pool, err := buildPool(req.Name, req.Description, req.Strategy, req.WorkspaceID, 0)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if err := h.pools.CreatePool(pool); err != nil {
		return c.AbortInternalServerError("failed to create IP pool", err)
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "admin.ip_pool.created",
			"Created IP pool \""+pool.Name+"\"", map[string]any{
				"pool_id": pool.ID, "name": pool.Name, "workspace_id": req.WorkspaceID,
			})
	}
	return created(c, poolWire(pool, nil, 0))
}

// AdminAddPoolMember adds a member IP to any pool (admin-managed shared
// pools included).
func (h *IPPoolHandler) AdminAddPoolMember(c *okapi.Context, req *PoolMemberRequest) error {
	pool, err := h.pools.FindPoolByID(req.PoolID)
	if err != nil {
		return c.AbortNotFound("pool not found")
	}
	member, err := buildMember(pool, req.Body.IP, req.Body.PTRName, req.Body.Enabled)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if err := h.pools.AddMember(member); err != nil {
		return c.AbortInternalServerError("failed to add pool member", err)
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "admin.ip_pool.member_added",
			"Added "+member.IP+" to IP pool \""+pool.Name+"\"", map[string]any{
				"pool_id": pool.ID, "member_id": member.ID, "ip": member.IP,
			})
	}
	return created(c, IPoolMemberWire{ID: member.ID, IP: member.IP, PTRName: member.PTRName, Enabled: member.Enabled})
}

// AdminRemovePoolMember deletes a member IP from any pool.
func (h *IPPoolHandler) AdminRemovePoolMember(c *okapi.Context, req *MemberPathRequest) error {
	pool, err := h.pools.FindPoolByID(req.PoolID)
	if err != nil {
		return c.AbortNotFound("pool not found")
	}
	member, err := h.pools.FindMemberByID(req.MemberID)
	if err != nil || member.PoolID != pool.ID {
		return c.AbortNotFound("pool member not found")
	}
	if err := h.pools.RemoveMember(member.ID); err != nil {
		return c.AbortInternalServerError("failed to remove pool member", err)
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "admin.ip_pool.member_removed",
			"Removed "+member.IP+" from IP pool \""+pool.Name+"\"", map[string]any{
				"pool_id": pool.ID, "member_id": member.ID, "ip": member.IP,
			})
	}
	return noContent(c)
}

// AdminIPReputation returns the deployment-wide reputation view for one
// source IP across every workspace (the operator's per-IP health read).
func (h *IPPoolHandler) AdminIPReputation(c *okapi.Context, req *AdminIPReputationRequest) error {
	ip := strings.TrimSpace(req.IP)
	if net.ParseIP(ip) == nil {
		return c.AbortBadRequest("ip query parameter must be a valid IP address")
	}
	days := req.Days
	if days <= 0 || days > 90 {
		days = 14
	}
	now := time.Now().UTC()
	to := reputation.DayStart(now).Add(24 * time.Hour)
	from := reputation.DayStart(now).Add(-time.Duration(days) * 24 * time.Hour)
	rows, err := h.pools.RollupsForIP(ip, from, to, 2000)
	if err != nil {
		return c.AbortInternalServerError("failed to load reputation rollups", err)
	}
	return ok(c, aggregateIPRollups(ip, rows))
}

// ---------- shared helpers ----------

// buildPool validates and builds a pool row.
func buildPool(name, description, strategy string, wsID *uint, userID uint) (*models.IPPool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("pool name is required")
	}
	if strategy == "" {
		strategy = models.PoolStrategyRoundRobin
	}
	if strategy != models.PoolStrategyRoundRobin {
		return nil, errors.New("unknown pool strategy")
	}
	return &models.IPPool{
		WorkspaceID: wsID,
		UserID:      userID,
		Name:        name,
		Description: strings.TrimSpace(description),
		Strategy:    strategy,
	}, nil
}

// buildMember validates and builds a pool member row.
func buildMember(pool *models.IPPool, ip, ptrName string, enabled *bool) (*models.IPPoolMember, error) {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return nil, errors.New("member ip must be a valid IP address")
	}
	on := true
	if enabled != nil {
		on = *enabled
	}
	return &models.IPPoolMember{
		WorkspaceID: pool.WorkspaceID,
		PoolID:      pool.ID,
		IP:          ip,
		PTRName:     strings.TrimSpace(ptrName),
		Enabled:     on,
	}, nil
}

// IPReputationAdminView is the admin per-(workspace, domain) row for one
// source IP.
type IPReputationAdminView struct {
	WorkspaceID   *uint   `json:"workspace_id,omitempty"`
	Domain        string  `json:"domain"`
	Attempts      int64   `json:"attempts"`
	Delivered     int64   `json:"delivered"`
	Permanent     int64   `json:"permanent"`
	Deferred      int64   `json:"deferred"`
	Sent          int64   `json:"sent"`
	Bounced       int64   `json:"bounced"`
	Complained    int64   `json:"complained"`
	BounceRate    float64 `json:"bounce_rate"`
	ComplaintRate float64 `json:"complaint_rate"`
	HardFailRate  float64 `json:"hard_fail_rate"`
	DeferredRate  float64 `json:"deferred_rate"`
	Grade         string  `json:"grade"`
}

// aggregateIPRollups folds one IP's stored rollup rows into per-(workspace,
// domain) aggregates with rates + grade (mirrors the domain report math):
// transport counters come from the IP's own rows; recipient events (incl.
// sent) come from the domain-wide row (IP "") of the same workspace+domain.
func aggregateIPRollups(ip string, rows []models.IPReputationRollup) []IPReputationAdminView {
	type key struct {
		ws     string
		domain string
	}
	agg := map[key]*IPReputationAdminView{}
	order := []key{}
	for i := range rows {
		r := &rows[i]
		k := key{ws: wsKeyOf(r.WorkspaceID), domain: r.Domain}
		a, ok := agg[k]
		if !ok {
			a = &IPReputationAdminView{WorkspaceID: r.WorkspaceID, Domain: r.Domain}
			agg[k] = a
			order = append(order, k)
		}
		if r.IP == "" {
			a.Sent += r.EventsSent
			a.Bounced += r.EventsBounced
			a.Complained += r.EventsComplained
		} else {
			a.Attempts += r.AttemptsTotal
			a.Delivered += r.AttemptsDelivered
			a.Permanent += r.AttemptsPermanent
			a.Deferred += r.AttemptsDeferred
		}
	}
	out := make([]IPReputationAdminView, 0, len(order))
	for _, k := range order {
		a := agg[k]
		a.BounceRate = pctOf(a.Bounced, a.Sent)
		a.ComplaintRate = pctOf(a.Complained, a.Sent)
		a.HardFailRate = pctOf(a.Permanent, a.Attempts)
		a.DeferredRate = pctOf(a.Deferred, a.Attempts)
		a.Grade = reputation.GradeOf(reputation.IPReputation{
			Attempts:      a.Attempts,
			Sent:          a.Sent,
			BounceRate:    a.BounceRate,
			ComplaintRate: a.ComplaintRate,
			HardFailRate:  a.HardFailRate,
			DeferredRate:  a.DeferredRate,
		})
		out = append(out, *a)
	}
	return out
}

func pctOf(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

func wsKeyOf(id *uint) string {
	if id == nil {
		return "g"
	}
	return fmt.Sprint(*id)
}
