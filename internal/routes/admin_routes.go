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

	cronpkg "github.com/goposta/posta/internal/cron"
	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/handlers"
	"github.com/goposta/posta/internal/models"
	"github.com/jkaninda/okapi"
)

// adminRoutes returns route definitions for admin endpoints.
func (r *Router) adminRoutes() []okapi.RouteDefinition {
	adminGroup := r.v1.Group("/admin", r.mw.jwtAdminAuth.Middleware).WithTagInfo(okapi.GroupTag{
		Name:        tagAdmin,
		Description: descAdmin,
	})
	adminGroup.WithBearerAuth()

	routes := []okapi.RouteDefinition{
		// ==================== Users ====================
		{
			Method:      http.MethodPost,
			Path:        "/users",
			Handler:     okapi.H(r.h.admin.CreateUser),
			Group:       adminGroup,
			Summary:     "Create a new user",
			Description: "Create a new user account (admin only)",
			Request:     &handlers.AdminCreateUserRequest{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(201, &dto.Response[models.User]{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:   http.MethodGet,
			Path:     "/users",
			Handler:  okapi.H(r.h.admin.ListUsers),
			Group:    adminGroup,
			Summary:  "List all users",
			Request:  &handlers.ListUsersRequest{},
			Response: &dto.PageableResponse[models.User]{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/users/{id:int}",
			Handler:     okapi.H(r.h.admin.UpdateUser),
			Group:       adminGroup,
			Summary:     "Update user",
			Description: "Update a user's name, email, and/or role",
			Request:     &handlers.AdminUpdateUserRequest{},
			Response:    &dto.Response[models.User]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "User ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/users/{id:int}/metrics",
			Handler:     okapi.H(r.h.admin.UserMetrics),
			Group:       adminGroup,
			Summary:     "Get user metrics",
			Description: "Returns detailed metrics for a specific user",
			Response:    &dto.Response[handlers.UserDetailMetrics]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "User ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:  http.MethodDelete,
			Path:    "/users/{id:int}",
			Handler: okapi.H(r.h.admin.DeleteUser),
			Group:   adminGroup,
			Tags:    []string{tagAdmin},
			Summary: "Delete user",
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "User ID"),
				okapi.DocResponse(204, nil),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/users/{id:int}/force",
			Handler:     okapi.H(r.h.admin.ForceDeleteUser),
			Group:       adminGroup,
			Tags:        []string{tagAdmin},
			Summary:     "Force delete user",
			Description: "Permanently delete a disabled user and all their data. The user must be disabled before force deletion.",
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "User ID"),
				okapi.DocResponse(204, nil),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/users/{id:int}/cancel-deletion",
			Handler:     okapi.H(r.h.admin.CancelUserDeletion),
			Group:       adminGroup,
			Summary:     "Cancel user deletion",
			Description: "Cancel a scheduled account deletion and re-enable the user (admin only)",
			Response:    &dto.Response[okapi.M]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "User ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/users/{id:int}/2fa",
			Handler:     okapi.H(r.h.admin.Disable2FA),
			Group:       adminGroup,
			Summary:     "Disable 2FA for user",
			Description: "Disable two-factor authentication for a user (admin only)",
			Response:    &dto.Response[okapi.M]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "User ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/users/{id}/revoke-sessions",
			Handler:     okapi.H(r.h.admin.RevokeUserSessions),
			Group:       adminGroup,
			Summary:     "Revoke all user sessions",
			Description: "Revoke all active sessions for a user (admin only)",
			Response:    &dto.Response[okapi.M]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "User ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/users/{id:int}/impersonate",
			Handler:     okapi.H(r.h.impersonation.Impersonate),
			Group:       adminGroup,
			Summary:     "Impersonate a user",
			Description: "Open a session acting as the target user (admin only, R-20). Requires a reason of at least 10 characters, is audited, and issues a new JWT + session carrying impersonated_by / impersonation_reason. Administrators and disabled accounts cannot be impersonated; the browser's session cookie is replaced, so exiting requires re-authentication.",
			Request:     &handlers.ImpersonateRequest{},
			Response:    &dto.Response[handlers.ImpersonationResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Target user ID"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(403, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/users/{id:int}/workspaces",
			Handler:     okapi.H(r.h.admin.UserWorkspaces),
			Group:       adminGroup,
			Summary:     "List user workspaces",
			Description: "Returns all workspaces a user belongs to with plan info",
			Response:    &dto.Response[[]handlers.AdminWorkspace]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "User ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},

		// ==================== User Plans ====================
		{
			Method:      http.MethodPost,
			Path:        "/users/{id:int}/plan",
			Handler:     okapi.H(r.h.plan.AssignToUser),
			Group:       adminGroup,
			Summary:     "Assign plan to user",
			Description: "Assign a usage plan to a user account",
			Request:     &handlers.AssignUserPlanRequest{},
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "User ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/users/{id:int}/plan",
			Handler:     okapi.H(r.h.plan.GetUserPlan),
			Group:       adminGroup,
			Summary:     "Get user plan",
			Description: "Get the effective plan for a user",
			Response:    &dto.Response[models.Plan]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "User ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},

		// ==================== Events ====================
		{
			Method:      http.MethodGet,
			Path:        "/events",
			Handler:     okapi.H(r.h.event.List),
			Group:       adminGroup,
			Summary:     "List events",
			Description: "List platform activity and system events with optional category filter",
			Request:     &handlers.ListEventsRequest{},
			Response:    &dto.PageableResponse[models.Event]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/events/{id:int}",
			Handler:     okapi.H(r.h.event.Get),
			Group:       adminGroup,
			Summary:     "Get event",
			Description: "Returns a single platform event by ID",
			Request:     &handlers.EventRequest{},
			Response:    &dto.Response[models.Event]{},
		},

		// ==================== Metrics & Analytics ====================
		{
			Method:      http.MethodGet,
			Path:        "/metrics",
			Handler:     r.h.admin.Metrics,
			Group:       adminGroup,
			Summary:     "Platform metrics",
			Description: "Returns platform-wide metrics: users, emails, bounces, suppressions, API keys",
			Response:    &dto.Response[handlers.PlatformMetrics]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/analytics",
			Handler:     okapi.H(r.h.analytics.AdminAnalytics),
			Group:       adminGroup,
			Summary:     "Platform analytics",
			Description: "Returns platform-wide daily email counts and status breakdown",
			Request:     &handlers.AnalyticsRequest{},
			Response:    &dto.Response[handlers.AnalyticsResponse]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/analytics/dashboard",
			Handler:     okapi.H(r.h.analytics.AdminDashboardAnalytics),
			Group:       adminGroup,
			Summary:     "Platform dashboard analytics",
			Description: "Returns platform-wide delivery rate trends, bounce rate graphs, and latency percentiles",
			Request:     &handlers.DashboardAnalyticsRequest{},
			Response:    &dto.Response[handlers.DashboardAnalyticsResponse]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/analytics/providers",
			Handler:     okapi.H(r.h.analytics.AdminProviderBreakdown),
			Group:       adminGroup,
			Summary:     "Platform deliverability by provider",
			Description: "Returns platform-wide sent/failed counts and delivery rate grouped by recipient mailbox provider",
			Request:     &handlers.ProviderBreakdownRequest{},
			Response:    &dto.Response[handlers.ProviderBreakdownResponse]{},
		},

		// ==================== Platform Settings ====================
		{
			Method:   http.MethodGet,
			Path:     pathSettings,
			Handler:  r.h.setting.GetSettings,
			Group:    adminGroup,
			Summary:  "Get platform settings",
			Response: &dto.Response[[]models.Setting]{},
		},
		{
			Method:   http.MethodPut,
			Path:     pathSettings,
			Handler:  okapi.H(r.h.setting.UpdateSettings),
			Group:    adminGroup,
			Summary:  "Update platform settings",
			Request:  &handlers.UpdateSettingsRequest{},
			Response: &dto.Response[[]models.Setting]{},
		},

		// Settings change history (SF-100): the per-key audit trail over every
		// platform settings write, and rollback of one key to a recorded prior
		// value. Rollback writes the old value back through the normal setter,
		// so it appends its own history entry and invalidates the provider's
		// TTL cache. Platform keys are admin-only, so both endpoints are
		// admin-gated; workspace-scoped keys do not exist yet (the history
		// table carries a nullable workspace id for them).
		{
			Method:      http.MethodGet,
			Path:        "/settings/history",
			Handler:     okapi.H(r.h.setting.GetSettingHistory),
			Group:       adminGroup,
			Summary:     "Get the change history of one platform setting",
			Description: "Returns one page of change-history entries for the ?key= query parameter, newest first: old value → new value, who changed it, when. Reserved internal keys (app.*) are refused.",
			Request:     &handlers.SettingHistoryRequest{},
			Response:    &dto.PageableResponse[models.SettingHistory]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/settings/history/rollback",
			Handler:     okapi.H(r.h.setting.RollbackSetting),
			Group:       adminGroup,
			Summary:     "Roll back one platform setting to a recorded prior value",
			Description: "Restores the setting named in the body to the old value of the selected history entry (history_id, or the newest entry when omitted). Refuses the key's creation entry (no prior value to restore) and reserved internal keys. The rollback is itself a settings write: it records a new history entry and takes effect immediately.",
			Request:     &handlers.RollbackSettingRequest{},
			Response:    &dto.Response[[]models.Setting]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},

		// ==================== Updates ====================
		{
			Method:      http.MethodGet,
			Path:        "/update",
			Handler:     r.h.update.GetUpdate,
			Group:       adminGroup,
			Summary:     "Get update status",
			Description: "Returns the cached result of the daily release check for the running build",
			Response:    &dto.Response[handlers.UpdateInfo]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/update/dismiss",
			Handler:     okapi.H(r.h.update.DismissUpdate),
			Group:       adminGroup,
			Summary:     "Dismiss the update notice for a specific version",
			Description: "Silences the notice until a newer version is released",
			Request:     &handlers.DismissUpdateRequest{},
			Response:    &dto.Response[dto.MessageData]{},
		},

		// ==================== Announcements ====================
		{
			Method:      http.MethodGet,
			Path:        pathAnnouncements,
			Handler:     okapi.H(r.h.announcement.List),
			Group:       adminGroup,
			Summary:     "List announcements",
			Description: "Platform notices that have been broadcast, newest first, with how many inboxes each reached.",
			Request:     &handlers.ListAnnouncementsRequest{},
			Response:    &dto.PageableResponse[models.Announcement]{},
		},
		{
			Method:      http.MethodPost,
			Path:        pathAnnouncements,
			Handler:     okapi.H(r.h.announcement.Create),
			Group:       adminGroup,
			Summary:     "Broadcast an announcement",
			Description: "Delivers a notice to every active user's in-app inbox immediately. There is no draft state; use retract if it went out wrong.",
			Request:     &handlers.CreateAnnouncementRequest{},
			Response:    &dto.Response[models.Announcement]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        pathAnnouncementByID,
			Handler:     okapi.H(r.h.announcement.Retract),
			Group:       adminGroup,
			Summary:     "Retract an announcement",
			Description: "Deletes the announcement and every delivery it made, removing it from users' inboxes.",
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Announcement ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},

		// ==================== Domains ====================
		{
			Method:      http.MethodGet,
			Path:        pathDomains,
			Handler:     okapi.H(r.h.adminDomain.List),
			Group:       adminGroup,
			Summary:     "List domains across every workspace",
			Description: "Platform-wide domain list, filterable by name, ownership verification, and workspace.",
			Request:     &handlers.AdminListDomainsRequest{},
			Response:    &dto.PageableResponse[handlers.AdminDomainRow]{},
		},
		{
			Method:      http.MethodGet,
			Path:        pathDomainByID,
			Handler:     okapi.H(r.h.adminDomain.Get),
			Group:       adminGroup,
			Summary:     "Get a domain",
			Description: "Returns the domain with its owner, the DNS records still required, and any workspace already holding the name verified.",
			Response:    &dto.Response[handlers.AdminDomainDetail]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Domain ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        pathDomainByID + "/verify",
			Handler:     okapi.H(r.h.adminDomain.Verify),
			Group:       adminGroup,
			Summary:     "Run DNS verification for a domain",
			Description: "Performs the same DNS checks the owning workspace would, on their behalf.",
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Domain ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:  http.MethodPut,
			Path:    pathDomainByID + "/verification",
			Handler: okapi.H(r.h.adminDomain.SetVerification),
			Group:   adminGroup,
			Summary: "Set domain ownership manually",
			Description: "Marks ownership verified, or revokes it, without a DNS check. Requires a reason when granting, " +
				"and is refused when another workspace already holds the name verified.",
			Request:  &handlers.AdminSetDomainVerificationRequest{},
			Response: &dto.Response[models.Domain]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Domain ID"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},

		// ==================== Workspaces ====================
		{
			Method:      http.MethodGet,
			Path:        pathWorkspaces,
			Handler:     okapi.H(r.h.adminWorkspace.List),
			Group:       adminGroup,
			Summary:     "List all workspaces",
			Description: "Platform-wide workspace list with owner, plan and member count, filterable by name/slug, owner, and plan.",
			Request:     &handlers.AdminListWorkspacesRequest{},
			Response:    &dto.PageableResponse[handlers.AdminWorkspaceRow]{},
		},
		{
			Method:      http.MethodGet,
			Path:        pathWorkspaceByID,
			Handler:     okapi.H(r.h.adminWorkspace.Get),
			Group:       adminGroup,
			Summary:     "Get a workspace",
			Description: "Returns the workspace with its members, pending invitation count, and resource usage.",
			Response:    &dto.Response[handlers.AdminWorkspaceDetail]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Workspace ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},

		// ==================== Plans ====================
		{
			Method:      http.MethodPost,
			Path:        "/plans",
			Handler:     okapi.H(r.h.plan.Create),
			Group:       adminGroup,
			Summary:     "Create plan",
			Description: "Create a new usage plan/package",
			Request:     &handlers.CreatePlanRequest{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(201, &dto.Response[models.Plan]{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:   http.MethodGet,
			Path:     "/plans",
			Handler:  okapi.H(r.h.plan.List),
			Group:    adminGroup,
			Summary:  "List plans",
			Request:  &handlers.ListPlansRequest{},
			Response: &dto.PageableResponse[models.Plan]{},
		},
		{
			Method:   http.MethodGet,
			Path:     pathPlanByID,
			Handler:  okapi.H(r.h.plan.Get),
			Group:    adminGroup,
			Summary:  "Get plan",
			Response: &dto.Response[models.Plan]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Plan ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPut,
			Path:        pathPlanByID,
			Handler:     okapi.H(r.h.plan.Update),
			Group:       adminGroup,
			Summary:     "Update plan",
			Description: "Update a plan's configuration and limits",
			Request:     &handlers.UpdatePlanRequest{},
			Response:    &dto.Response[models.Plan]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Plan ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        pathPlanByID,
			Handler:     okapi.H(r.h.plan.Delete),
			Group:       adminGroup,
			Summary:     "Delete plan",
			Description: "Delete a plan. Use ?force=true to delete a plan assigned to workspaces.",
			Request:     &handlers.DeletePlanRequest{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Plan ID"),
				okapi.DocResponse(204, nil),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPatch,
			Path:        "/plans/{id:int}/default",
			Handler:     okapi.H(r.h.plan.SetDefault),
			Group:       adminGroup,
			Summary:     "Set plan as default",
			Description: "Set this plan as the default plan, unsetting any previous default",
			Response:    &dto.Response[models.Plan]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Plan ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/workspaces/{id:int}/plan",
			Handler:     okapi.H(r.h.plan.AssignToWorkspace),
			Group:       adminGroup,
			Summary:     "Assign plan to workspace",
			Description: "Assign a usage plan to a workspace",
			Request:     &handlers.AssignWorkspacePlanRequest{},
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Workspace ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/workspaces/{id:int}/plan",
			Handler:     okapi.H(r.h.plan.GetWorkspacePlan),
			Group:       adminGroup,
			Summary:     "Get workspace plan",
			Description: "Get the effective plan for a workspace",
			Response:    &dto.Response[models.Plan]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Workspace ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},

		// ==================== Shared SMTP Servers ====================
		{
			Method:      http.MethodPost,
			Path:        "/servers",
			Handler:     okapi.H(r.h.server.Create),
			Group:       adminGroup,
			Summary:     "Create shared SMTP server",
			Description: "Add a new shared SMTP server to the pool",
			Request:     &handlers.CreateServerRequest{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(201, &dto.Response[models.Server]{}),
			},
		},
		{
			Method:   http.MethodGet,
			Path:     "/servers",
			Handler:  okapi.H(r.h.server.List),
			Group:    adminGroup,
			Summary:  "List shared SMTP servers",
			Request:  &handlers.ListServersRequest{},
			Response: &dto.PageableResponse[models.Server]{},
		},
		{
			Method:   http.MethodGet,
			Path:     pathServerByID,
			Handler:  okapi.H(r.h.server.Get),
			Group:    adminGroup,
			Summary:  "Get shared SMTP server",
			Response: &dto.Response[models.Server]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Server ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:   http.MethodPut,
			Path:     pathServerByID,
			Handler:  okapi.H(r.h.server.Update),
			Group:    adminGroup,
			Summary:  "Update shared SMTP server",
			Request:  &handlers.UpdateServerRequest{},
			Response: &dto.Response[models.Server]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Server ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:  http.MethodDelete,
			Path:    pathServerByID,
			Handler: okapi.H(r.h.server.Delete),
			Group:   adminGroup,
			Tags:    []string{tagAdmin},
			Summary: "Delete shared SMTP server",
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Server ID"),
				okapi.DocResponse(204, nil),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:   http.MethodPost,
			Path:     "/servers/{id:int}/enable",
			Handler:  okapi.H(r.h.server.Enable),
			Group:    adminGroup,
			Summary:  "Enable shared SMTP server",
			Response: &dto.Response[models.Server]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Server ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:   http.MethodPost,
			Path:     "/servers/{id:int}/disable",
			Handler:  okapi.H(r.h.server.Disable),
			Group:    adminGroup,
			Summary:  "Disable shared SMTP server",
			Response: &dto.Response[models.Server]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Server ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:   http.MethodPost,
			Path:     "/servers/{id:int}/test",
			Handler:  okapi.H(r.h.server.Test),
			Group:    adminGroup,
			Summary:  "Test shared SMTP server connection",
			Response: &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Server ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
	}

	// Cron jobs (only if cron manager is configured)
	if r.h.cron != nil {
		routes = append(routes, okapi.RouteDefinition{
			Method:      http.MethodGet,
			Path:        "/jobs",
			Handler:     r.h.cron.List,
			Group:       adminGroup,
			Summary:     "List scheduled jobs",
			Description: "Returns all registered cron jobs with their schedule and last execution status.",
			Response:    &dto.Response[[]cronpkg.JobStatus]{},
		})
	}

	// ==================== Delivery Queues (asynq inspection) ====================
	routes = append(routes,
		okapi.RouteDefinition{
			Method:      http.MethodGet,
			Path:        "/queues",
			Handler:     r.h.admin.ListQueues,
			Group:       adminGroup,
			Summary:     "List delivery queues",
			Description: "Returns per-queue stats (pending, active, scheduled, retry, archived, latency) for the send pipeline queues plus the live worker count.",
			Response:    &dto.Response[handlers.QueuesOverview]{},
		},
		okapi.RouteDefinition{
			Method:      http.MethodGet,
			Path:        "/queues/{queue}/tasks",
			Handler:     okapi.H(r.h.admin.ListQueueTasks),
			Group:       adminGroup,
			Summary:     "List tasks in a delivery queue",
			Description: "Returns a paged task list for one queue in the requested state (pending, active, scheduled, retry or archived — the archived set is the dead-letter view).",
			Request:     &handlers.ListQueueTasksRequest{},
			Response:    &dto.Response[handlers.QueueTaskPage]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("queue", "string", "Queue name (transactional, bulk or low)"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "/queues/{queue}/tasks/{task_id}/cancel",
			Handler:     okapi.H(r.h.admin.CancelQueueTask),
			Group:       adminGroup,
			Tags:        []string{tagAdmin},
			Summary:     "Cancel a queued task",
			Description: "Removes a pending/scheduled/retrying task from the queue so it never runs; for a dead-lettered (archived) task it deletes it. Audited.",
			Response:    &dto.Response[okapi.M]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("queue", "string", "Queue name"),
				okapi.DocPathParam("task_id", "string", "Asynq task ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "/queues/{queue}/tasks/{task_id}/run",
			Handler:     okapi.H(r.h.admin.RunQueueTask),
			Group:       adminGroup,
			Tags:        []string{tagAdmin},
			Summary:     "Run a task now",
			Description: "Schedules a retrying or dead-lettered (archived) task for immediate processing. Audited.",
			Response:    &dto.Response[okapi.M]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("queue", "string", "Queue name"),
				okapi.DocPathParam("task_id", "string", "Asynq task ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "/queues/{queue}/tasks/{task_id}/archive",
			Handler:     okapi.H(r.h.admin.ArchiveQueueTask),
			Group:       adminGroup,
			Tags:        []string{tagAdmin},
			Summary:     "Archive a task",
			Description: "Parks a pending/scheduled/retrying task in the archived set (the dead-letter view) without deleting it. Audited.",
			Response:    &dto.Response[okapi.M]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("queue", "string", "Queue name"),
				okapi.DocPathParam("task_id", "string", "Asynq task ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodDelete,
			Path:        "/queues/{queue}/tasks/{task_id}",
			Handler:     okapi.H(r.h.admin.DestroyQueueTask),
			Group:       adminGroup,
			Tags:        []string{tagAdmin},
			Summary:     "Destroy a task",
			Description: "Deletes a pending/scheduled/retrying/archived task outright (the REST destroy verb beside POST /cancel; active tasks cannot be deleted). Audited as admin.queue.task_destroyed. PATCH of a task's options is not offered: asynq's Inspector has no API to modify a task's options in place — adjust by cancel + re-enqueue instead.",
			Response:    &dto.Response[okapi.M]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("queue", "string", "Queue name"),
				okapi.DocPathParam("task_id", "string", "Asynq task ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
	)

	// Queue pause/resume (SF-54): a deployment-wide delivery switch over all
	// managed queues. Per-domain email suspension is explicitly out of scope
	// (ADR-001).
	routes = append(routes,
		okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "/queue/pause",
			Handler:     r.h.admin.PauseQueues,
			Group:       adminGroup,
			Tags:        []string{tagAdmin},
			Summary:     "Pause all delivery queues",
			Description: "Stops task processing on the transactional, bulk and low queues (already-paused queues are reported unchanged). Audited. The per-queue paused flags appear in GET /admin/queues.",
			Response:    &dto.Response[handlers.QueuePauseResult]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "/queue/resume",
			Handler:     r.h.admin.ResumeQueues,
			Group:       adminGroup,
			Tags:        []string{tagAdmin},
			Summary:     "Resume all delivery queues",
			Description: "Resumes task processing on the transactional, bulk and low queues (already-running queues are reported unchanged). Audited.",
			Response:    &dto.Response[handlers.QueuePauseResult]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
	)

	// Operational queue actions (SF-59) exist only when a worker is wired —
	// requeue_stuck re-enqueues through the send producer.
	if r.h.queueOps != nil {
		routes = append(routes, okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "/queue/actions/{action}",
			Handler:     okapi.H(r.h.queueOps.RunAction),
			Group:       adminGroup,
			Tags:        []string{tagAdmin},
			Summary:     "Run a queue operation",
			Description: "Operational actions over the delivery queues: requeue_stuck (run the queue-reconcile sweep to re-enqueue emails stuck in queued whose task was lost) and purge_archived_before (delete dead-lettered tasks that reached the archive at or before body.before; absent means now). Audited.",
			Request:     &handlers.QueueActionRequest{},
			Response:    &dto.Response[handlers.QueueActionResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("action", "string", "requeue_stuck or purge_archived_before"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		})
	}

	// ==================== Mailstore maintenance (OB-3, ADR-003) ====================
	// The search index rebuild is the only mailstore HTTP surface this phase
	// adds: a single admin-gated maintenance action. User-facing search comes
	// with OB-4 (IMAP SEARCH) and OB-5 (webmail).
	if r.h.mailstoreSearch != nil {
		routes = append(routes, okapi.RouteDefinition{
			Method:  http.MethodPost,
			Path:    "/mailstore/search/reindex",
			Handler: r.h.mailstoreSearch.Reindex,
			Group:   adminGroup,
			Tags:    []string{tagAdmin},
			Summary: "Rebuild the mailstore full-text search index",
			Description: "Drops and re-creates the mail_messages.search_tsv generated tsvector column and its GIN index from the canonical definition (ADR-003 decision 3). The column regenerates automatically on every write, so this is an operator recovery path for schema or index drift, not a routine task; it takes an exclusive lock on mail_messages while the DDL runs. Idempotent. Audited.",
			Response: &dto.Response[handlers.ReindexMailstoreSearchResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(500, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(503, &dto.ErrorResponseBody{}),
			},
		})
	}

	return routes
}

// adminSSERoutes returns route definitions for admin SSE (Server-Sent Events) endpoints.
func (r *Router) adminSSERoutes() []okapi.RouteDefinition {
	adminSSE := r.v1.Group("/admin", r.mw.jwtAdminAuth.Middleware).WithTagInfo(okapi.GroupTag{
		Name:        tagAdmin,
		Description: descAdmin,
	})

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/events/stream",
			Handler:     r.h.event.Stream,
			Group:       adminSSE,
			Summary:     "Stream events (SSE)",
			Description: "Real-time event stream via Server-Sent Events. Authenticated by the session cookie the browser sends on the EventSource handshake.",
			Options:     []okapi.RouteOption{okapi.DocHide()},
		},
		{
			Method:      http.MethodGet,
			Path:        "/metrics/stream",
			Handler:     r.h.admin.MetricsStream,
			Group:       adminSSE,
			Summary:     "Stream platform metrics (SSE)",
			Description: "Real-time worker status and system runtime stats via Server-Sent Events. Emits worker.status and system.status events. Authenticated by the session cookie.",
			Options:     []okapi.RouteOption{okapi.DocHide()},
		},
	}
}

// securityRoutes returns route definitions for the admin security endpoints:
// the ban list, detection events, tunable rules and the allowlist.
func (r *Router) securityRoutes() []okapi.RouteDefinition {
	securityGroup := r.v1.Group("/admin", r.mw.jwtAdminAuth.Middleware).WithTagInfo(okapi.GroupTag{
		Name:        tagAdmin,
		Description: descAdmin,
	})
	securityGroup.WithBearerAuth()

	return []okapi.RouteDefinition{
		// ==================== Security Overview ====================
		{
			Method:      http.MethodGet,
			Path:        "/security/overview",
			Handler:     r.h.security.Overview,
			Group:       securityGroup,
			Summary:     "Security overview",
			Description: "Returns aggregate ban and detection counts plus the 14-day activity trend",
			Response:    &dto.Response[handlers.SecurityOverview]{},
		},

		// ==================== Bans ====================
		{
			Method:      http.MethodGet,
			Path:        "/security/bans",
			Handler:     okapi.H(r.h.security.ListBans),
			Group:       securityGroup,
			Summary:     "List IP bans",
			Description: "Paginated ban list, searchable by IP address; filter accepts active (default), expired or all",
			Request:     &handlers.ListSecurityBansRequest{},
			Response:    &dto.PageableResponse[models.SecurityBan]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/security/bans",
			Handler:     okapi.H(r.h.security.CreateBan),
			Group:       securityGroup,
			Summary:     "Ban an IP address manually",
			Description: "Places an administrative ban on an IP address or CIDR block, permanent or for a number of hours",
			Request:     &handlers.CreateSecurityBanRequest{},
			Response:    &dto.Response[models.SecurityBan]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(201, &dto.Response[models.SecurityBan]{}),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/security/bans/{id:int}",
			Handler:     okapi.H(r.h.security.Unban),
			Group:       securityGroup,
			Summary:     "Lift a ban",
			Description: "Expires the ban in place; the row is kept as offense history",
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Ban ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/security/bans/{id:int}/extend",
			Handler:     okapi.H(r.h.security.ExtendBan),
			Group:       securityGroup,
			Summary:     "Extend a ban",
			Description: "Pushes a timed ban's expiry out by the given number of hours",
			Request:     &handlers.ExtendSecurityBanRequest{},
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Ban ID"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},

		// ==================== Detection Events ====================
		{
			Method:      http.MethodGet,
			Path:        "/security/events",
			Handler:     okapi.H(r.h.security.ListEvents),
			Group:       securityGroup,
			Summary:     "List detection events",
			Description: "Paginated security events, filterable by event type and severity",
			Request:     &handlers.ListSecurityEventsRequest{},
			Response:    &dto.PageableResponse[models.SecurityEvent]{},
		},

		// ==================== Rules ====================
		{
			Method:      http.MethodGet,
			Path:        "/security/rules",
			Handler:     r.h.security.GetRules,
			Group:       securityGroup,
			Summary:     "List security rules",
			Description: "Returns the tunable security_* settings with labels, current values and bounds",
			Response:    &dto.Response[[]handlers.SecurityRule]{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/security/rules",
			Handler:     okapi.H(r.h.security.UpdateRules),
			Group:       securityGroup,
			Summary:     "Update security rules",
			Description: "Upserts a batch of security_* settings; unknown keys are ignored and values are validated against the rule bounds",
			Request:     &handlers.UpdateSecurityRulesRequest{},
			Response:    &dto.Response[[]handlers.SecurityRule]{},
		},

		// ==================== Allowlist ====================
		{
			Method:      http.MethodGet,
			Path:        "/security/allowlist",
			Handler:     okapi.H(r.h.security.ListAllowlist),
			Group:       securityGroup,
			Summary:     "List the IP allowlist",
			Description: "Addresses and CIDR blocks exempt from detection and bans",
			Request:     &handlers.ListSecurityAllowlistRequest{},
			Response:    &dto.PageableResponse[models.SecurityAllowlist]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/security/allowlist",
			Handler:     okapi.H(r.h.security.CreateAllowlist),
			Group:       securityGroup,
			Summary:     "Add an allowlist entry",
			Description: "Exempts an IP address or CIDR block from detection and bans; the change propagates to every replica",
			Request:     &handlers.CreateSecurityAllowlistRequest{},
			Response:    &dto.Response[models.SecurityAllowlist]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(201, &dto.Response[models.SecurityAllowlist]{}),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/security/allowlist/{id:int}",
			Handler:     okapi.H(r.h.security.DeleteAllowlist),
			Group:       securityGroup,
			Summary:     "Remove an allowlist entry",
			Description: "Deletes the exemption; the address becomes subject to detection and existing bans again",
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Allowlist entry ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
	}
}

// securitySSERoutes returns the security SSE endpoints. They skip WithBearerAuth
// like the other SSE groups: the browser's EventSource handshake authenticates
// with the session cookie and cannot set an Authorization header. Registered
// before the plain security routes so no param route can shadow the stream
// path (see admin_sse_route_order_test.go for the convention).
func (r *Router) securitySSERoutes() []okapi.RouteDefinition {
	securitySSE := r.v1.Group("/admin", r.mw.jwtAdminAuth.Middleware).WithTagInfo(okapi.GroupTag{
		Name:        tagAdmin,
		Description: descAdmin,
	})

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/security/events/stream",
			Handler:     r.h.security.EventsStream,
			Group:       securitySSE,
			Summary:     "Stream security events (SSE)",
			Description: "Real-time security events via Server-Sent Events. Emits security.event, ban.created and ban.lifted. Authenticated by the session cookie the browser sends on the EventSource handshake.",
			Options:     []okapi.RouteOption{okapi.DocHide()},
		},
	}
}
