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

	"github.com/goposta/posta/internal/services/auth"
	"github.com/goposta/posta/internal/services/clientinfo"
	"github.com/goposta/posta/internal/services/session"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

type SessionHandler struct {
	repo  *repositories.SessionRepository
	store *session.Store
	// refresh is the CA-03 refresh-token service; nil when refresh tokens are
	// disabled, in which case logout revokes only the access session.
	refresh *auth.RefreshTokenService
}

func NewSessionHandler(repo *repositories.SessionRepository, store *session.Store) *SessionHandler {
	return &SessionHandler{repo: repo, store: store}
}

// SetRefreshTokens wires the CA-03 refresh-token service for logout revocation.
func (h *SessionHandler) SetRefreshTokens(s *auth.RefreshTokenService) {
	h.refresh = s
}

// LogoutRequest carries the optional refresh token the client wants dead with
// the session (CA-03). CLI/SDK clients hold one; browsers log out with the
// cookie alone.
type LogoutRequest struct {
	Body struct {
		RefreshToken string `json:"refresh_token"`
	} `json:"body"`
}

// SessionResponse is the API representation of a session.
type SessionResponse struct {
	ID        uint   `json:"id"`
	IPAddress string `json:"ip_address"`
	UserAgent string `json:"user_agent"`
	Browser   string `json:"browser"`
	OS        string `json:"os"`
	Device    string `json:"device"`
	Label     string `json:"label"`
	Current   bool   `json:"current"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

type RevokeSessionRequest struct {
	ID int `param:"id"`
}

// List returns all active sessions for the current user.
func (h *SessionHandler) List(c *okapi.Context) error {
	userID := uint(c.GetInt("user_id"))
	currentJTI := c.GetString("jti")

	sessions, err := h.repo.FindActiveByUserID(userID)
	if err != nil {
		return c.AbortInternalServerError("failed to load sessions")
	}

	result := make([]SessionResponse, 0, len(sessions))
	for _, s := range sessions {
		client := clientinfo.Parse(s.UserAgent)
		result = append(result, SessionResponse{
			ID:        s.ID,
			IPAddress: s.IPAddress,
			UserAgent: s.UserAgent,
			Browser:   client.Browser,
			OS:        client.OS,
			Device:    client.Device,
			Label:     client.Label(),
			Current:   s.JTI == currentJTI,
			CreatedAt: s.CreatedAt.Format("2006-01-02T15:04:05Z"),
			ExpiresAt: s.ExpiresAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	return ok(c, result)
}

// Revoke terminates a specific session by ID.
func (h *SessionHandler) Revoke(c *okapi.Context, req *RevokeSessionRequest) error {
	userID := uint(c.GetInt("user_id"))

	sess, err := h.repo.FindByID(uint(req.ID))
	if err != nil || sess.UserID != userID {
		return c.AbortNotFound("session not found")
	}

	if sess.Revoked {
		return c.AbortBadRequest("session already revoked")
	}

	if err := h.repo.Revoke(sess.ID); err != nil {
		return c.AbortInternalServerError("failed to revoke session")
	}

	// Add to Redis blacklist for immediate effect
	h.store.MarkRevoked(c.Request().Context(), sess.JTI, sess.ExpiresAt)

	return ok(c, okapi.M{"message": fmt.Sprintf("Session %d revoked", sess.ID)})
}

// RevokeOthers revokes all sessions except the current one.
func (h *SessionHandler) RevokeOthers(c *okapi.Context) error {
	userID := uint(c.GetInt("user_id"))
	currentJTI := c.GetString("jti")

	// Get all active sessions first (need JTIs for Redis blacklist)
	sessions, err := h.repo.FindActiveByUserID(userID)
	if err != nil {
		return c.AbortInternalServerError("failed to load sessions")
	}

	count, err := h.repo.RevokeOthersByUserID(userID, currentJTI)
	if err != nil {
		return c.AbortInternalServerError("failed to revoke sessions")
	}

	// Blacklist each revoked session in Redis
	for _, s := range sessions {
		if s.JTI != currentJTI {
			h.store.MarkRevoked(c.Request().Context(), s.JTI, s.ExpiresAt)
		}
	}

	return ok(c, okapi.M{
		"message": fmt.Sprintf("%d other session(s) revoked", count),
		"revoked": count,
	})
}

// Logout revokes the current session and clears the browser session cookie.
// Clearing the cookie unconditionally means a browser holding a token we can no
// longer resolve still ends up logged out.
//
// CA-03: when the body carries a refresh token it is revoked too, so a stolen
// long-lived credential does not survive the sign-out that kills its session.
func (h *SessionHandler) Logout(c *okapi.Context, req *LogoutRequest) error {
	session.ClearCookie(c)

	if req != nil && req.Body.RefreshToken != "" && h.refresh != nil {
		_ = h.refresh.RevokeByHash(req.Body.RefreshToken)
	}

	currentJTI := c.GetString("jti")
	if currentJTI == "" {
		return ok(c, okapi.M{"message": "logged out"})
	}

	sess, err := h.repo.FindByJTI(currentJTI)
	if err == nil && sess != nil && !sess.Revoked {
		_ = h.repo.Revoke(sess.ID)
		h.store.MarkRevoked(c.Request().Context(), sess.JTI, sess.ExpiresAt)
	}

	return ok(c, okapi.M{"message": "logged out"})
}
