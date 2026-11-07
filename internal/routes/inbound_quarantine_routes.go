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
	"github.com/jkaninda/okapi"
)

// inboundQuarantineRoutes registers the quarantine review surface (OB-2,
// ADR-002 decision 3): list, release (re-runs routing), delete (permanent).
// Same auth shape as the inbound workspace routes — a dashboard session or
// an API key; the handler gates mutations on workspace edit rights.
func (r *Router) inboundQuarantineRoutes() []okapi.RouteDefinition {
	group := r.v1.Group("/workspaces/current", r.mw.auth, r.mw.workspaceQuery).WithTagInfo(okapi.GroupTag{
		Name:        tagInbound,
		Description: "Inbound quarantine review: held messages, release (re-runs routing), permanent delete.",
	})
	group.WithBearerAuth()

	return []okapi.RouteDefinition{
		{
			Method:   http.MethodGet,
			Path:     "/inbound-quarantine",
			Handler:  okapi.H(r.h.inboundQuarantine.List),
			Group:    group,
			Summary:  "List quarantined inbound emails",
			Request:  &handlers.QuarantineListRequest{},
			Response: &dto.PageableResponse[handlers.QuarantineListItem]{},
		},
		{
			Method:   http.MethodPost,
			Path:     "/inbound-quarantine/{id}/release",
			Handler:  okapi.H(r.h.inboundQuarantine.Release),
			Group:    group,
			Summary:  "Release a quarantined email (re-runs routing)",
			Request:  &handlers.QuarantineActionRequest{},
			Response: &dto.Response[models.InboundQuarantine]{},
			Options:  []okapi.RouteOption{okapi.DocPathParam("id", "integer", "Quarantine record ID")},
		},
		{
			Method:   http.MethodDelete,
			Path:     "/inbound-quarantine/{id}",
			Handler:  okapi.H(r.h.inboundQuarantine.Delete),
			Group:    group,
			Summary:  "Permanently delete a quarantined email",
			Request:  &handlers.QuarantineActionRequest{},
			Response: &dto.Response[models.InboundQuarantine]{},
			Options:  []okapi.RouteOption{okapi.DocPathParam("id", "integer", "Quarantine record ID")},
		},
	}
}
