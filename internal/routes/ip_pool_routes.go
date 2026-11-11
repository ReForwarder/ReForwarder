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

package routes

import (
	"net/http"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/handlers"
	"github.com/goposta/posta/internal/services/reputation"
	"github.com/jkaninda/okapi"
)

// ipPoolRoutes returns the workspace-side OB-6 W3 IP-pool surface (ADR-007
// decision 7): pool list/create/delete, domain pool assignment, warm-up
// schedule management, and the per-domain reputation detail. Its own group
// keeps the diff additive — workspace_resource_routes.go is not touched.
func (r *Router) ipPoolRoutes() []okapi.RouteDefinition {
	opsGroup := r.v1.Group("/workspaces/current", r.mw.auth, r.mw.workspace).WithTagInfo(okapi.GroupTag{
		Name:        tagWorkspaceResources,
		Description: "Operational resources scoped to the active workspace: sending, templates, stylesheets, languages, API keys, contacts, subscribers, campaigns, domains, SMTP servers, webhooks, suppressions, and workspace analytics. Authenticated with a dashboard session or an API key bound to the workspace.",
	})
	opsGroup.WithBearerAuth()

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/ip-pools",
			Handler:     okapi.H(r.h.ipPool.ListPools),
			Group:       opsGroup,
			Summary:     "List IP pools",
			Description: "The IP pools this workspace may send from: its own pools plus the deployment-wide shared ones, each with its member IPs, enabled state, and how many domains currently use it.",
			Response:    &dto.Response[[]handlers.IPoolWire]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/ip-pools",
			Handler:     okapi.H(r.h.ipPool.CreatePool),
			Group:       opsGroup,
			Summary:     "Create an IP pool",
			Description: "Creates a workspace-private pool of outbound source IPs. Deployment-wide (shared) pools are the admin surface's job.",
			Request:     &handlers.CreateIPPoolRequest{},
			Response:    &dto.Response[handlers.IPoolWire]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(403, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/ip-pools/{poolId:int}",
			Handler:     okapi.H(r.h.ipPool.DeletePool),
			Group:       opsGroup,
			Summary:     "Delete an IP pool",
			Description: "Deletes a workspace-owned pool. Refused while any domain still references the pool — clear the assignments first.",
			Options: []okapi.RouteOption{
				okapi.DocPathParam("poolId", "integer", "IP pool ID"),
				okapi.DocResponse(204, nil),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/ip-pools/{poolId:int}/members",
			Handler:     okapi.H(r.h.ipPool.AddPoolMember),
			Group:       opsGroup,
			Summary:     "Add an IP pool member",
			Description: "Adds a member IP to a workspace-owned pool. Only pool members with enabled=true are selected for deliveries.",
			Request:     &handlers.PoolMemberRequest{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("poolId", "integer", "IP pool ID"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/ip-pools/{poolId:int}/members/{memberId:int}",
			Handler:     okapi.H(r.h.ipPool.RemovePoolMember),
			Group:       opsGroup,
			Summary:     "Remove an IP pool member",
			Description: "Removes a member IP from a workspace-owned pool. Historical reputation attribution for the IP is kept.",
			Options: []okapi.RouteOption{
				okapi.DocPathParam("poolId", "integer", "IP pool ID"),
				okapi.DocPathParam("memberId", "integer", "Pool member ID"),
				okapi.DocResponse(204, nil),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPut,
			Path:        pathDomainByID + "/pool",
			Handler:     okapi.H(r.h.ipPool.AssignDomainPool),
			Group:       opsGroup,
			Summary:     "Assign the domain's IP pool",
			Description: "Points the domain at an IP pool (its own or a deployment-wide shared one); a null pool_id clears the assignment. Direct deliveries then rotate across the pool's enabled members instead of the single outbound bind IP.",
			Request:     &handlers.AssignDomainPoolRequest{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Domain ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPut,
			Path:        pathDomainByID + "/warmup",
			Handler:     okapi.H(r.h.ipPool.SetDomainWarmup),
			Group:       opsGroup,
			Summary:     "Set the domain's warm-up schedule",
			Description: "Replaces the domain's active warm-up ramp: day N allows round(start_volume × daily_multiplier^N) messages for the given number of days; the ramp's hourly cap is enforced on the domain's hourly budget. The previous schedule is deactivated (kept for the record).",
			Request:     &handlers.SetDomainWarmupRequest{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Domain ID"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        pathDomainByID + "/reputation",
			Handler:     okapi.H(r.h.ipPool.DomainReputation),
			Group:       opsGroup,
			Summary:     "Get domain IP reputation",
			Description: "The per-source-IP reputation surface for this domain: transport outcomes per pool/bind IP, recipient-side bounce/complaint rates, grades, and the daily trend. Transport counters cover direct-to-MX deliveries only; bounced/complained events cover every delivery mode.",
			Response:    &dto.Response[reputation.DomainReport]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Domain ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
	}
}

// adminIPPoolRoutes returns the deployment-wide admin view (ADR-007 decision
// 7): every pool with members and usage, pool creation (optionally
// workspace-scoped), member management for shared pools, and the per-IP
// reputation read across all workspaces. Admin-only via the admin middleware.
func (r *Router) adminIPPoolRoutes() []okapi.RouteDefinition {
	adminGroup := r.v1.Group("/admin", r.mw.jwtAdminAuth.Middleware).WithTagInfo(okapi.GroupTag{
		Name:        tagAdmin,
		Description: descAdmin,
	})
	adminGroup.WithBearerAuth()

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/ip-pools",
			Handler:     okapi.H(r.h.ipPool.AdminListPools),
			Group:       adminGroup,
			Summary:     "List all IP pools",
			Description: "Every IP pool in the deployment — shared and workspace-private — with members and domain usage counts.",
			Response:    &dto.Response[[]handlers.IPoolWire]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/ip-pools",
			Handler:     okapi.H(r.h.ipPool.AdminCreatePool),
			Group:       adminGroup,
			Summary:     "Create an IP pool",
			Description: "Creates a pool. A null workspace_id makes it a deployment-wide shared pool every workspace may send from; a workspace_id scopes it to that workspace.",
			Request:     &handlers.AdminCreateIPPoolRequest{},
			Response:    &dto.Response[handlers.IPoolWire]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/ip-pools/{poolId:int}/members",
			Handler:     okapi.H(r.h.ipPool.AdminAddPoolMember),
			Group:       adminGroup,
			Summary:     "Add an IP pool member",
			Description: "Adds a member IP to any pool, including deployment-wide shared pools.",
			Request:     &handlers.PoolMemberRequest{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("poolId", "integer", "IP pool ID"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/ip-pools/{poolId:int}/members/{memberId:int}",
			Handler:     okapi.H(r.h.ipPool.AdminRemovePoolMember),
			Group:       adminGroup,
			Summary:     "Remove an IP pool member",
			Description: "Removes a member IP from any pool. Historical reputation attribution for the IP is kept.",
			Options: []okapi.RouteOption{
				okapi.DocPathParam("poolId", "integer", "IP pool ID"),
				okapi.DocPathParam("memberId", "integer", "Pool member ID"),
				okapi.DocResponse(204, nil),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/ip-reputation",
			Handler:     okapi.H(r.h.ipPool.AdminIPReputation),
			Group:       adminGroup,
			Summary:     "Get source IP reputation",
			Description: "The deployment-wide reputation view for one source IP: per (workspace, domain) transport outcomes, recipient-side bounce/complaint rates, and grades over the recent window. Reads the stored rollups the daily cron maintains (the current day is refreshed on the workspace-facing health read).",
			Response:    &dto.Response[[]handlers.IPReputationAdminView]{},
			Options: []okapi.RouteOption{
				okapi.DocQueryParam("ip", "string", "The source IP to look up", true),
				okapi.DocQueryParam("days", "integer", "Lookback window in days (default 14, max 90)", false),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
			},
		},
	}
}
