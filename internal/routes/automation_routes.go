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

// automationRoutes returns the automation workflow endpoints: the builder CRUD,
// the activate/pause/duplicate lifecycle, the execution inspector and the
// custom-event ingest — all scoped to the active workspace.
func (r *Router) automationRoutes() []okapi.RouteDefinition {
	automationGroup := r.v1.Group("/workspaces/current", r.mw.auth, r.mw.workspace).WithTagInfo(okapi.GroupTag{
		Name:        tagAutomations,
		Description: "Automation workflows scoped to the active workspace: build, validate, activate and pause multi-step workflows (trigger, delay, email, filter, A/B test, list actions, webhook), inspect per-contact executions, and ingest custom events. Authenticated with a dashboard session or an API key bound to the workspace.",
	})
	automationGroup.WithBearerAuth()

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/automations",
			Handler:     okapi.H(r.h.automation.List),
			Group:       automationGroup,
			Summary:     "List automations",
			Description: "Returns a page of automations, newest first, with parsed trigger config, nodes and stats. status filters draft, live or paused.",
			Request:     &handlers.ListAutomationsRequest{},
			Response:    &dto.PageableResponse[handlers.AutomationResponse]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/automations",
			Handler:     okapi.H(r.h.automation.Create),
			Group:       automationGroup,
			Summary:     "Create automation",
			Description: "Validates the workflow graph and stores the automation as a draft. Returns 400 with validation_errors listing every violated rule.",
			Request:     &handlers.SaveAutomationRequest{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(201, &dto.Response[handlers.AutomationResponse]{}),
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:   http.MethodGet,
			Path:     pathAutomationByID,
			Handler:  okapi.H(r.h.automation.Get),
			Group:    automationGroup,
			Summary:  "Get automation",
			Response: &dto.Response[handlers.AutomationResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Automation ID"),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPut,
			Path:        pathAutomationByID,
			Handler:     okapi.H(r.h.automation.Update),
			Group:       automationGroup,
			Summary:     "Update automation",
			Description: "Replaces the builder document and bumps the version. A live automation picks the new workflow up on its next node tick.",
			Request:     &handlers.SaveAutomationRequest{},
			Response:    &dto.Response[handlers.AutomationResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Automation ID"),
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        pathAutomationByID,
			Handler:     okapi.H(r.h.automation.Delete),
			Group:       automationGroup,
			Summary:     "Delete automation",
			Description: "Refused with 409 while the automation is live — pause it first. Otherwise soft-deletes the automation and exits its active journeys.",
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Automation ID"),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        pathAutomationByID + "/activate",
			Handler:     okapi.H(r.h.automation.Activate),
			Group:       automationGroup,
			Summary:     "Activate automation",
			Description: "Re-validates the stored workflow and flips the automation live under compare-and-swap. Returns 400 with validation_errors or 409 on a concurrent status change.",
			Response:    &dto.Response[handlers.AutomationResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Automation ID"),
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        pathAutomationByID + "/pause",
			Handler:     okapi.H(r.h.automation.Pause),
			Group:       automationGroup,
			Summary:     "Pause automation",
			Description: "Flips a live automation back to paused under compare-and-swap; active journeys stay parked until it is activated again.",
			Response:    &dto.Response[handlers.AutomationResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Automation ID"),
				okapi.DocResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        pathAutomationByID + "/duplicate",
			Handler:     okapi.H(r.h.automation.Duplicate),
			Group:       automationGroup,
			Summary:     "Duplicate automation",
			Description: "Copies the workflow as a fresh draft named \"<name> (copy)\" with its counters reset.",
			Response:    &dto.Response[handlers.AutomationResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Automation ID"),
				okapi.DocResponse(201, &dto.Response[handlers.AutomationResponse]{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        pathAutomationByID + "/executions",
			Handler:     okapi.H(r.h.automation.ListExecutions),
			Group:       automationGroup,
			Summary:     "List executions",
			Description: "Returns a page of the automation's journeys — one per contact enrollment — filterable by status and contact email substring.",
			Request:     &handlers.ListAutomationExecutionsRequest{},
			Response:    &dto.PageableResponse[models.AutomationJourney]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Automation ID"),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        pathAutomationByID + "/executions/{journeyId:int}",
			Handler:     okapi.H(r.h.automation.GetExecution),
			Group:       automationGroup,
			Summary:     "Get execution",
			Description: "One journey with its node-run timeline, oldest run first.",
			Response:    &dto.Response[handlers.AutomationExecutionDetail]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Automation ID"),
				okapi.DocPathParam("journeyId", "integer", "Journey ID"),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        pathAutomationByID + "/stats",
			Handler:     okapi.H(r.h.automation.Stats),
			Group:       automationGroup,
			Summary:     "Get automation stats",
			Description: "Aggregate counters (enrolled, completed, exited, failed) plus the per-node entered/completed/failed breakdown.",
			Response:    &dto.Response[handlers.AutomationStatsResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Automation ID"),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/automations/events",
			Handler:     okapi.H(r.h.automation.CustomEvent),
			Group:       automationGroup,
			Summary:     "Send custom automation event",
			Description: "Ingests a custom event for a contact and enrolls them into every live automation with a matching custom trigger. Always answers 202 once the payload is well-formed; enrolled says whether an automation actually matched.",
			Request:     &handlers.AutomationEventRequest{},
			Response:    &dto.Response[handlers.AutomationEventResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(202, &dto.Response[handlers.AutomationEventResponse]{}),
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
			},
		},
	}
}
