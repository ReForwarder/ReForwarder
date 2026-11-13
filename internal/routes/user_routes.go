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

// userRoutes returns route definitions for all authenticated user endpoints.
func (r *Router) userRoutes() []okapi.RouteDefinition {
	userGroup := r.v1.Group("/users/me", r.mw.jwtOnly, r.mw.optionalWorkspace).WithTagInfo(okapi.GroupTag{
		Name:        tagUser,
		Description: "Manage the authenticated user: profile, password, API keys, notification preferences, and session. Requires a dashboard session — an API key cannot administer the account that issued it.",
	})
	userGroup.WithBearerAuth()

	routes := make([]okapi.RouteDefinition, 0, 35)
	routes = append(routes, []okapi.RouteDefinition{
		{
			Method:   http.MethodGet,
			Path:     "",
			Handler:  r.h.user.Me,
			Group:    userGroup,
			Summary:  "Get current user profile",
			Response: &dto.Response[handlers.UserProfile]{},
		},
		{
			Method:      http.MethodPut,
			Path:        "",
			Handler:     okapi.H(r.h.user.UpdateProfile),
			Group:       userGroup,
			Summary:     "Update profile",
			Description: "Update the current user's profile",
			Request:     &handlers.UpdateProfileRequest{},
			Response:    &dto.Response[handlers.UserProfile]{},
		},
		{
			Method:      http.MethodPut,
			Path:        "/password",
			Handler:     okapi.H(r.h.user.ChangePassword),
			Group:       userGroup,
			Summary:     "Change password",
			Description: "Change the current user's password",
			Request:     &handlers.ChangePasswordRequest{},
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/verify-email/resend",
			Handler:     r.h.user.ResendVerificationEmail,
			Group:       userGroup,
			Summary:     "Resend verification email",
			Description: "Issue a fresh verification email for the authenticated user",
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(429, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/plan",
			Handler:     r.h.plan.GetMyPlan,
			Group:       userGroup,
			Summary:     "Get my plan",
			Description: "Get the effective plan for the authenticated user",
			Response:    &dto.Response[models.Plan]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/2fa/setup",
			Handler:     r.h.user.Setup2FA,
			Group:       userGroup,
			Summary:     "Setup 2FA",
			Description: "Generate a TOTP secret for enabling 2FA. Returns secret and otpauth URL for QR code.",
			Response:    &dto.Response[handlers.Enable2FAResponse]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/2fa/verify",
			Handler:     okapi.H(r.h.user.Verify2FA),
			Group:       userGroup,
			Summary:     "Verify and enable 2FA",
			Description: "Verify a TOTP code to confirm 2FA setup",
			Request:     &handlers.Verify2FARequest{},
			Response:    &dto.Response[any]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/2fa/disable",
			Handler:     okapi.H(r.h.user.Disable2FA),
			Group:       userGroup,
			Summary:     "Disable 2FA",
			Description: "Disable 2FA after verifying a TOTP code",
			Request:     &handlers.Disable2FARequest{},
			Response:    &dto.Response[any]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/2fa/recovery-codes",
			Handler:     okapi.H(r.h.user.GenerateRecoveryCodes),
			Group:       userGroup,
			Summary:     "Regenerate 2FA recovery codes",
			Description: "Issue a fresh set of single-use 2FA recovery codes (R-10). Requires the current TOTP code; the plaintext codes are returned once and never again.",
			Request:     &handlers.GenerateRecoveryCodesRequest{},
			Response:    &dto.Response[handlers.RecoveryCodesResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/2fa/recovery-codes/count",
			Handler:     r.h.user.RecoveryCodeCount,
			Group:       userGroup,
			Summary:     "Count remaining 2FA recovery codes",
			Description: "How many single-use 2FA recovery codes the account has left.",
			Response:    &dto.Response[handlers.RecoveryCodeCountResponse]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/delete",
			Handler:     r.h.user.RequestAccountDeletion,
			Group:       userGroup,
			Summary:     "Request account deletion",
			Description: "Schedule account for deletion in 7 days. The account is deactivated immediately.",
			Response:    &dto.Response[any]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/cancel-deletion",
			Handler:     r.h.user.CancelAccountDeletion,
			Group:       userGroup,
			Summary:     "Cancel account deletion",
			Description: "Cancel a previously scheduled account deletion and reactivate the account.",
			Response:    &dto.Response[any]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/sessions",
			Handler:     r.h.session.List,
			Group:       userGroup,
			Summary:     "List active sessions",
			Description: "Returns all active (non-revoked, non-expired) sessions for the current user",
			Response:    &dto.Response[[]handlers.SessionResponse]{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/sessions/{id:int}",
			Handler:     okapi.H(r.h.session.Revoke),
			Group:       userGroup,
			Summary:     "Revoke session",
			Description: "Force logout a specific session by ID",
			Response:    &dto.Response[any]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Session ID"),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/sessions/revoke-others",
			Handler:     r.h.session.RevokeOthers,
			Group:       userGroup,
			Summary:     "Revoke all other sessions",
			Description: "Force logout all sessions except the current one",
			Response:    &dto.Response[any]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/sessions/logout",
			Handler:     okapi.H(r.h.session.Logout),
			Group:       userGroup,
			Summary:     "Logout current session",
			Description: "Revoke the current session's JWT token. A refresh token in the body is revoked with it (CA-03).",
			Request:     &handlers.LogoutRequest{},
			Response:    &dto.Response[any]{},
		},
		// SF-89 Web Push (user-scoped): the capability probe, then CRUD over
		// the calling user's own browser push subscriptions.
		{
			Method:      http.MethodGet,
			Path:        "/push/config",
			Handler:     r.h.webpush.GetConfig,
			Group:       userGroup,
			Summary:     "Get Web Push capability",
			Description: "Returns whether Web Push is enabled on this deployment and the VAPID public key to subscribe with (SF-89).",
			Response:    &dto.Response[handlers.PushConfigResponse]{},
		},
		{
			Method:      http.MethodPost,
			Path:        "/push/subscriptions",
			Handler:     okapi.H(r.h.webpush.Subscribe),
			Group:       userGroup,
			Summary:     "Register a browser push subscription",
			Description: "Stores the browser's PushSubscription (endpoint + RFC 8291 keys) for the current user (SF-89).",
			Request:     &handlers.SubscribeRequest{},
			Response:    &dto.Response[map[string]any]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/push/subscriptions",
			Handler:     r.h.webpush.List,
			Group:       userGroup,
			Summary:     "List push subscriptions",
			Description: "Returns the current user's own Web Push subscriptions (SF-89).",
			Response:    &dto.Response[[]models.UserPushSubscription]{},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/push/subscriptions/{id:int}",
			Handler:     okapi.H(r.h.webpush.Unsubscribe),
			Group:       userGroup,
			Summary:     "Remove a push subscription",
			Description: "Deletes one of the current user's own Web Push subscriptions by ID (SF-89).",
			Response:    &dto.Response[map[string]string]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Push subscription ID"),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPut,
			Path:        "/default-workspace",
			Handler:     okapi.H(r.h.user.SetDefaultWorkspace),
			Group:       userGroup,
			Summary:     "Set the default workspace",
			Description: "Chooses which workspace a request that sends no X-Posta-Workspace-Id header operates on. The caller must be a member.",
			Request:     &handlers.SetDefaultWorkspaceRequest{},
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/audit-log",
			Handler:     okapi.H(r.h.event.UserAuditLog),
			Group:       userGroup,
			Summary:     "List user audit log",
			Description: "Returns the authenticated user's security audit trail (login, password, 2FA). Workspace-operational audit lives at /workspaces/current/audit-log.",
			Request:     &handlers.ListEventsRequest{},
			Response:    &dto.PageableResponse[models.Event]{},
		},
		{
			Method:      http.MethodGet,
			Path:        pathSettings,
			Handler:     r.h.userSetting.GetSettings,
			Group:       userGroup,
			Summary:     "Get user settings",
			Description: "Personal notification preferences. Operational settings live at /workspaces/current/settings.",
			Response:    &dto.Response[models.UserSetting]{},
		},
		{
			Method:      http.MethodPut,
			Path:        pathSettings,
			Handler:     okapi.H(r.h.userSetting.UpdateSettings),
			Group:       userGroup,
			Summary:     "Update user settings",
			Description: "Personal notification preferences. Operational settings live at /workspaces/current/settings.",
			Request:     &handlers.UpdateUserSettingsRequest{},
			Response:    &dto.Response[models.UserSetting]{},
		},

		// In-app inbox. These sit under /users/me because a notification is
		// addressed to a person, not to a workspace: read and dismissed state
		// follow the user across every workspace they belong to.
		{
			Method:      http.MethodGet,
			Path:        pathNotifications,
			Handler:     okapi.H(r.h.notification.List),
			Group:       userGroup,
			Summary:     "List notifications",
			Description: "The authenticated user's in-app inbox, newest first. Paginate with the `before` keyset cursor.",
			Request:     &handlers.ListNotificationsRequest{},
			Response:    &dto.Response[[]models.Notification]{},
		},
		{
			Method:      http.MethodGet,
			Path:        pathNotifications + "/counts",
			Handler:     r.h.notification.Counts,
			Group:       userGroup,
			Summary:     "Get notification counts",
			Description: "Unread count for the header bell, and how many live items the active workspace has.",
			Response:    &dto.Response[handlers.NotificationCounts]{},
		},
		{
			Method:      http.MethodGet,
			Path:        pathNotifications + "/banner",
			Handler:     r.h.notification.Banner,
			Group:       userGroup,
			Summary:     "List dashboard banner notifications",
			Description: "Live, undismissed notifications for the active workspace, most severe first.",
			Response:    &dto.Response[[]models.Notification]{},
		},
		{
			Method:   http.MethodPost,
			Path:     pathNotifications + "/read",
			Handler:  okapi.H(r.h.notification.MarkRead),
			Group:    userGroup,
			Summary:  "Mark notifications read",
			Request:  &handlers.NotificationIDsRequest{},
			Response: &dto.Response[dto.MessageData]{},
		},
		{
			Method:   http.MethodPost,
			Path:     pathNotifications + "/read-all",
			Handler:  r.h.notification.MarkAllRead,
			Group:    userGroup,
			Summary:  "Mark all notifications read",
			Response: &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        pathNotifications + "/dismiss",
			Handler:     okapi.H(r.h.notification.Dismiss),
			Group:       userGroup,
			Summary:     "Dismiss notifications",
			Description: "Hides items from the dashboard banner. A dismissal is bound to the condition as it currently stands: if the condition materially changes the item resurfaces, so dismissing is not permanent silence.",
			Request:     &handlers.NotificationIDsRequest{},
			Response:    &dto.Response[dto.MessageData]{},
		},
		{
			Method:      http.MethodPost,
			Path:        pathNotifications + "/dismiss-all",
			Handler:     r.h.notification.DismissAll,
			Group:       userGroup,
			Summary:     "Dismiss all notifications",
			Description: "Clears the dashboard banner for the active workspace.",
			Response:    &dto.Response[dto.MessageData]{},
		},
	}...)

	// Passkeys (R-19). The ceremonies live under /auth/webauthn: registration
	// and credential management demand a dashboard session (an API key cannot
	// mint credentials for the account that issued it), while the login
	// ceremonies are public but rate-limited and keyed to the user's email.
	// Registered from the user route set because passkeys are account-level,
	// not workspace-scoped, state.
	webauthnAuthed := r.v1.Group("/auth/webauthn", r.mw.jwtOnly).WithTagInfo(okapi.GroupTag{
		Name:        tagUser,
		Description: "Passkey registration and credential management. Requires a dashboard session.",
	})
	webauthnAuthed.WithBearerAuth()
	webauthnPublic := r.v1.Group("/auth/webauthn", r.mw.loginLimiter).WithTagInfo(okapi.GroupTag{
		Name:        tagUser,
		Description: "Passkey login ceremonies. Public, rate-limited, keyed to the user's email.",
	})

	routes = append(routes,
		okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "/register/begin",
			Handler:     r.h.webauthn.BeginRegistration,
			Group:       webauthnAuthed,
			Summary:     "Begin passkey registration",
			Description: "Returns the PublicKeyCredentialCreationOptions for navigator.credentials.create().",
			Response:    &dto.Response[any]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "/register/finish",
			Handler:     r.h.webauthn.FinishRegistration,
			Group:       webauthnAuthed,
			Summary:     "Finish passkey registration",
			Description: "Validates the attestation response (raw WebAuthn credential JSON body) and stores the credential. Audited.",
			Response:    &dto.Response[models.WebAuthnCredential]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "/login/begin",
			Handler:     okapi.H(r.h.webauthn.BeginLogin),
			Group:       webauthnPublic,
			Summary:     "Begin passkey login",
			Description: "Returns the PublicKeyCredentialRequestOptions for navigator.credentials.get(). Requires the account's email; unknown or passkey-less accounts answer with one generic error.",
			Request:     &handlers.BeginWebAuthnLoginRequest{},
			Response:    &dto.Response[any]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(429, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "/login/finish",
			Handler:     r.h.webauthn.FinishLogin,
			Group:       webauthnPublic,
			Summary:     "Finish passkey login",
			Description: "Verifies the assertion and issues the JWT + session like a password login. With 2FA enabled the assertion alone is not enough: the response is a requires_2fa 401 carrying a one-shot pending token that this endpoint redeems together with the TOTP code. Body: {email, assertion} or {email, pending_token, two_factor_code}.",
			Response:    &dto.Response[handlers.AuthResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(401, &dto.ErrorResponseBody{}),
				okapi.DocResponse(429, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodGet,
			Path:        "/credentials",
			Handler:     r.h.webauthn.ListCredentials,
			Group:       webauthnAuthed,
			Summary:     "List registered passkeys",
			Description: "The authenticated user's registered passkeys, oldest first.",
			Response:    &dto.Response[[]models.WebAuthnCredential]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodPut,
			Path:        "/credentials/{id}",
			Handler:     okapi.H(r.h.webauthn.RenameCredential),
			Group:       webauthnAuthed,
			Summary:     "Rename a passkey",
			Description: "Renames one of the caller's own passkeys. The id is the base64url credential ID.",
			Request:     &handlers.RenameWebAuthnCredentialRequest{},
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "string", "Credential ID (base64url)"),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodDelete,
			Path:        "/credentials/{id}",
			Handler:     okapi.H(r.h.webauthn.DeleteCredential),
			Group:       webauthnAuthed,
			Summary:     "Delete a passkey",
			Description: "Removes one of the caller's own passkeys. The id is the base64url credential ID. Audited.",
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "string", "Credential ID (base64url)"),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
	)

	// R-20 impersonation exit. It sits on the /admin path because the dashboard
	// shows the exit affordance while impersonating, but it must NOT use the
	// admin middleware: the impersonated session carries the target's role, so
	// requiring the admin role here would lock the impersonator out of the
	// only route that restores them. Any authenticated session can call it;
	// non-impersonated sessions are refused by the handler.
	exitGroup := r.v1.Group("/admin/impersonate", r.mw.jwtOnly).WithTagInfo(okapi.GroupTag{
		Name:        tagUser,
		Description: "Impersonation session exit. Requires an impersonated session.",
	})

	routes = append(routes, okapi.RouteDefinition{
		Method:      http.MethodPost,
		Path:        "/exit",
		Handler:     r.h.impersonation.ExitImpersonation,
		Group:       exitGroup,
		Summary:     "Exit impersonation",
		Description: "Revokes the current impersonation session and expires the cookie; the admin re-authenticates. Audited.",
		Response:    &dto.Response[okapi.M]{},
		Options: []okapi.RouteOption{
			okapi.DocResponse(400, &dto.ErrorResponseBody{}),
		},
	})

	// SF-35/SF-67: app passwords. Per-user secrets for the future IMAP / POP3 /
	// submission surfaces (ADR-004). Generate returns the plaintext exactly
	// once (bcrypt stored); list and revoke never touch credential material.
	appPasswordGroup := r.v1.Group("/users/me/app-passwords", r.mw.jwtOnly).WithTagInfo(okapi.GroupTag{
		Name:        tagUser,
		Description: "App passwords for mail clients (IMAP/POP3/submission). Requires a dashboard session.",
	})

	routes = append(routes,
		okapi.RouteDefinition{
			Method:      http.MethodPost,
			Path:        "",
			Handler:     okapi.H(r.h.appPassword.Create),
			Group:       appPasswordGroup,
			Summary:     "Generate an app password",
			Description: "Creates a named app password for the current user. The plaintext is returned exactly once and stored only as a bcrypt digest.",
			Request:     &handlers.CreateAppPasswordRequest{},
			Response:    &dto.Response[handlers.AppPasswordCreatedResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
			},
		},
		okapi.RouteDefinition{
			Method:      http.MethodGet,
			Path:        "",
			Handler:     r.h.appPassword.List,
			Group:       appPasswordGroup,
			Summary:     "List app passwords",
			Description: "The current user's app passwords with metadata only — the plaintext is never recoverable.",
			Response:    &dto.Response[[]handlers.AppPasswordResponse]{},
		},
		okapi.RouteDefinition{
			Method:      http.MethodDelete,
			Path:        "/{id:int}",
			Handler:     okapi.H(r.h.appPassword.Revoke),
			Group:       appPasswordGroup,
			Summary:     "Revoke an app password",
			Description: "Permanently disables one of the current user's app passwords.",
			Request:     &handlers.RevokeAppPasswordRequest{},
			Response:    &dto.Response[any]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "App password ID"),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
			},
		},
	)

	// CA-03: single-use refresh-token rotation. Public like /auth/login and
	// under the same per-IP limiter. Registered from the user route set because
	// the rotation logic lives on the user handler alongside the login that
	// issues the tokens.
	refreshGroup := r.v1.Group("/auth", r.mw.loginLimiter).WithTagInfo(okapi.GroupTag{
		Name:        "Auth",
		Description: "Refresh-token rotation for issued sessions.",
	})
	routes = append(routes, okapi.RouteDefinition{
		Method:      http.MethodPost,
		Path:        "/refresh",
		Handler:     okapi.H(r.h.user.Refresh),
		Group:       refreshGroup,
		Summary:     "Rotate refresh token",
		Description: "Redeem a single-use refresh token for a fresh access JWT and successor refresh token. Replaying a rotated token revokes its whole rotation family.",
		Request:     &handlers.RefreshRequest{},
		Response:    &dto.Response[handlers.AuthResponse]{},
		Options: []okapi.RouteOption{
			okapi.DocResponse(401, &dto.ErrorResponseBody{}),
			okapi.DocResponse(429, &dto.ErrorResponseBody{}),
		},
	})

	return routes
}
