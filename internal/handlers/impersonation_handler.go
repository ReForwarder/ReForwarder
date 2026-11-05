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
	"fmt"
	"strings"

	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/session"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// Administrative impersonation (R-20): an admin may open a session that acts
// as another user to reproduce a support report. The flow mints a brand-new
// JWT carrying the target's identity plus two marker claims — impersonated_by
// (the admin's user ID) and impersonation_reason — and a session row stamped
// the same way; the middleware turns those claims into context markers so
// handlers and the dashboard can see the session is a support login. Exiting
// revokes the impersonation session; the admin's original session is left
// intact server-side, but the browser cookie has been replaced, so the admin
// re-authenticates on the next sign-in.
//
// Both events are audited: admin.impersonation.started and
// admin.impersonation.stopped, with the admin ID, target ID and reason.

// ImpersonationHandler implements the impersonation endpoints.
type ImpersonationHandler struct {
	repo        *repositories.UserRepository
	sessionRepo *repositories.SessionRepository
	store       *session.Store
	users       *UserHandler
	audit       *audit.Logger
}

// NewImpersonationHandler builds the handler. users is the shared login
// session factory (R-20 session creation rides R-19's IssueSession).
func NewImpersonationHandler(
	repo *repositories.UserRepository,
	sessionRepo *repositories.SessionRepository,
	store *session.Store,
	users *UserHandler,
	auditLogger *audit.Logger,
) *ImpersonationHandler {
	return &ImpersonationHandler{
		repo:        repo,
		sessionRepo: sessionRepo,
		store:       store,
		users:       users,
		audit:       auditLogger,
	}
}

// ImpersonateRequest carries the target user ID and the mandatory reason.
type ImpersonateRequest struct {
	ID   int `param:"id"`
	Body struct {
		Reason string `json:"reason" required:"true" minLength:"10" maxLength:"500"`
	} `json:"body"`
}

// ImpersonationResponse carries the impersonation token and the target profile
// the dashboard switches to.
type ImpersonationResponse struct {
	Token        string `json:"token"`
	Impersonated bool   `json:"impersonated"`
	Target       struct {
		ID    uint            `json:"id"`
		Name  string          `json:"name"`
		Email string          `json:"email"`
		Role  models.UserRole `json:"role"`
	} `json:"target"`
	// OriginalAdmin names the admin whose browser this was, for the exit UX.
	OriginalAdminID   uint   `json:"-"`
	OriginalAdminName string `json:"original_admin"`
}

// Impersonate starts an impersonation session for the target user.
func (h *ImpersonationHandler) Impersonate(c *okapi.Context, req *ImpersonateRequest) error {
	adminID := uint(c.GetInt("user_id"))
	admin, err := h.repo.FindByID(adminID)
	if err != nil || admin == nil {
		return c.AbortNotFound("user not found")
	}

	target, err := h.repo.FindByID(uint(req.ID))
	if err != nil || target == nil {
		return c.AbortNotFound("user not found")
	}
	if target.ID == adminID {
		return c.AbortBadRequest("you are already signed in as this user")
	}
	if target.Role == models.UserRoleAdmin {
		return c.AbortForbidden("administrators cannot impersonate other administrators")
	}
	if !target.Active {
		return c.AbortBadRequest("cannot impersonate a disabled account")
	}

	reason := strings.TrimSpace(req.Body.Reason)
	if len([]rune(reason)) < 10 {
		return c.AbortBadRequest("a reason of at least 10 characters is required")
	}

	// The admin's own session row stays untouched: only the browser's cookie
	// is replaced below, and exit requires re-authentication.
	token, _, err := h.users.IssueSession(c, target, map[string]any{
		"impersonated_by":      admin.ID,
		"impersonation_reason": reason,
	})
	if err != nil {
		return c.AbortInternalServerError("failed to create impersonation session", err)
	}
	session.SetCookie(c, token, jwtTokenTTL)

	if h.audit != nil {
		h.audit.LogCtx(c, "admin.impersonation.started",
			fmt.Sprintf("Impersonation started for %q", target.Email),
			map[string]any{
				"admin_id":     adminID,
				"admin_email":  admin.Email,
				"target_id":    target.ID,
				"target_email": target.Email,
				"reason":       reason,
			})
	}

	res := ImpersonationResponse{
		Token:             token,
		Impersonated:      true,
		OriginalAdminID:   adminID,
		OriginalAdminName: admin.Email,
	}
	res.Target.ID = target.ID
	res.Target.Name = target.Name
	res.Target.Email = target.Email
	res.Target.Role = target.Role
	return created(c, res)
}

// ExitImpersonation ends the current impersonation session: the impersonation
// JWT is revoked (Redis blacklist + session row) and the cookie expires, so
// the next dashboard action re-authenticates. Requires an impersonated
// session; a session that carries no impersonation marker is refused.
func (h *ImpersonationHandler) ExitImpersonation(c *okapi.Context) error {
	adminID := middlewares.ImpersonatedBy(c)
	if adminID == 0 {
		return c.AbortBadRequest("this session is not an impersonation session")
	}

	jti := c.GetString("jti")
	targetID := uint(c.GetInt("user_id"))
	targetEmail := c.GetString("email")

	if jti != "" && h.sessionRepo != nil {
		if sess, err := h.sessionRepo.FindByJTI(jti); err == nil && sess != nil && !sess.Revoked {
			_ = h.sessionRepo.Revoke(sess.ID)
			if h.store != nil {
				h.store.MarkRevoked(c.Request().Context(), sess.JTI, sess.ExpiresAt)
			}
		}
	}
	session.ClearCookie(c)

	// The stop event names the admin as the actor — the session acts as the
	// target, but the action being audited is the admin's.
	actorID := adminID
	actorEmail := targetEmail
	if admin, err := h.repo.FindByID(adminID); err == nil && admin != nil {
		actorEmail = admin.Email
	}
	if h.audit != nil {
		h.audit.Log(actorID, actorEmail, c.RealIP(), "admin.impersonation.stopped",
			fmt.Sprintf("Impersonation ended for %q", targetEmail),
			map[string]any{
				"admin_id":  adminID,
				"target_id": targetID,
			})
	}

	return ok(c, okapi.M{
		"message":           "impersonation ended; sign in again to return to your account",
		"original_admin_id": adminID,
	})
}
