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

package routes

import (
	"net/http"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/handlers"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/jkaninda/okapi"
)

// inboundForwardTargetRoutes returns the workspace-scoped forward-target
// management endpoints (OB-2, ADR-002 decision 2): create/list/revoke the
// confirmed forwarding destinations. Create and revoke are workspace-edit
// gated inside the handler (requireEdit) on top of the auth middleware.
func (r *Router) inboundForwardTargetRoutes() []okapi.RouteDefinition {
	targetGroup := r.v1.Group("/workspaces/current", r.mw.auth, r.mw.workspaceQuery).WithTagInfo(okapi.GroupTag{
		Name:        tagInbound,
		Description: "Inbound email confirmed forward destinations: register a target, receive a one-time plain-text confirmation URL, revoke it. Create/revoke require workspace edit rights.",
	})
	targetGroup.WithBearerAuth()

	return []okapi.RouteDefinition{
		{
			Method:   http.MethodPost,
			Path:     "/inbound/forward-targets",
			Handler:  okapi.H(r.h.inboundForwardTarget.Create),
			Group:    targetGroup,
			Summary:  "Register a forward target (issues the one-time confirmation URL)",
			Request:  &handlers.ForwardTargetCreateRequest{},
			Response: &dto.Response[inbound.CreatedTarget]{},
		},
		{
			Method:   http.MethodGet,
			Path:     "/inbound/forward-targets",
			Handler:  okapi.H(r.h.inboundForwardTarget.List),
			Group:    targetGroup,
			Summary:  "List forward targets",
			Request:  &handlers.ForwardTargetListRequest{},
			Response: &dto.PageableResponse[models.InboundForwardTarget]{},
		},
		{
			Method:  http.MethodDelete,
			Path:    "/inbound/forward-targets/{id:int}",
			Handler: okapi.H(r.h.inboundForwardTarget.Revoke),
			Group:   targetGroup,
			Summary: "Revoke a forward target (forwarding to it stops)",
			Request: &handlers.ForwardTargetActionRequest{},
			Options: []okapi.RouteOption{okapi.DocPathParam("id", "integer", "Forward target ID")},
		},
	}
}

// inboundForwardConfirmRoutes returns the PUBLIC confirmation endpoint: the
// operator sends the plain-text URL (base + /api/v1/inbound/forward-confirm/
// <token>) to the target mailbox manually; the token itself is the
// credential, so no session or API key is required.
func (r *Router) inboundForwardConfirmRoutes() []okapi.RouteDefinition {
	confirmGroup := r.v1.Group("/inbound").WithTagInfo(okapi.GroupTag{
		Name:        tagInbound,
		Description: "Public inbound endpoints. The forward-target confirmation consumes the single-use token carried by the plain-text URL the operator sent.",
	})
	return []okapi.RouteDefinition{
		{
			Method:   http.MethodGet,
			Path:     "/forward-confirm/{token}",
			Handler:  okapi.H(r.h.inboundForwardTarget.Confirm),
			Group:    confirmGroup,
			Summary:  "Confirm a forward target (single-use token)",
			Request:  &handlers.ForwardTargetConfirmRequest{},
			Response: &dto.Response[handlers.ForwardTargetConfirmResponse]{},
			Options:  []okapi.RouteOption{okapi.DocHide()},
		},
	}
}
