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
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"golang.org/x/crypto/bcrypt"
)

// AppPasswordHandler is the SF-35 / SF-67 management surface for per-user app
// passwords: separate secrets a mail client uses against the future IMAP /
// POP3 / submission surfaces (ADR-004) instead of the account password.
//
// Generation contract: the plaintext is returned exactly once in the create
// response ("one-time display"), only the bcrypt digest is stored, and neither
// the list nor any later response can ever return it. This mirrors the R-10
// recovery-code contract.
//
// ADR-004 seam: the SMTP submission / IMAP / POP3 authentication backends call
// repositories.UserAppPasswordRepository.FindActiveByUserID +
// VerifyAppPassword with the credential the mail client presented. This wave
// ships the management surface and the verification primitive; wiring them
// into the SMTP listener lives with the MTA wave's owned files.
type AppPasswordHandler struct {
	repo *repositories.UserAppPasswordRepository
}

func NewAppPasswordHandler(repo *repositories.UserAppPasswordRepository) *AppPasswordHandler {
	return &AppPasswordHandler{repo: repo}
}

// AppPasswordPrefix marks a raw app password on the wire, the way psk_ marks
// an API key, so a backend that accepts both can tell them apart by prefix.
const AppPasswordPrefix = "apw_"

type CreateAppPasswordRequest struct {
	Body struct {
		Name string `json:"name" required:"true" minLength:"1" maxLength:"100"`
	} `json:"body"`
}

type AppPasswordResponse struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// AppPasswordCreatedResponse is the one-time view: the plaintext rides this
// response and nothing else, ever.
type AppPasswordCreatedResponse struct {
	AppPassword AppPasswordResponse `json:"app_password"`
	// Password is the only copy of the secret. Stored as a bcrypt digest.
	Password string `json:"password"`
	// Notice tells the client this value is unrecoverable.
	Notice string `json:"notice"`
}

// Create generates a named app password for the current user.
func (h *AppPasswordHandler) Create(c *okapi.Context, req *CreateAppPasswordRequest) error {
	if h.repo == nil {
		return c.AbortInternalServerError("app passwords are not available")
	}
	userID := uint(c.GetInt("user_id"))

	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return c.AbortBadRequest("name is required")
	}

	raw, err := GenerateAppPassword()
	if err != nil {
		return c.AbortInternalServerError("failed to generate app password", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	if err != nil {
		return c.AbortInternalServerError("failed to store app password", err)
	}

	row := &models.UserAppPassword{
		UserID:       userID,
		Name:         name,
		PasswordHash: string(hash),
	}
	if err := h.repo.Create(row); err != nil {
		return c.AbortInternalServerError("failed to store app password", err)
	}

	return created(c, AppPasswordCreatedResponse{
		AppPassword: AppPasswordResponse{
			ID:        row.ID,
			Name:      row.Name,
			CreatedAt: row.CreatedAt,
		},
		Password: raw,
		Notice:   "Store this password now — it is shown only once and cannot be recovered.",
	})
}

// List returns the user's app passwords without any credential material.
func (h *AppPasswordHandler) List(c *okapi.Context) error {
	if h.repo == nil {
		return c.AbortInternalServerError("app passwords are not available")
	}
	userID := uint(c.GetInt("user_id"))

	list, err := h.repo.ListByUser(userID)
	if err != nil {
		return c.AbortInternalServerError("failed to load app passwords")
	}
	out := make([]AppPasswordResponse, 0, len(list))
	for _, p := range list {
		out = append(out, AppPasswordResponse{
			ID:         p.ID,
			Name:       p.Name,
			CreatedAt:  p.CreatedAt,
			LastUsedAt: p.LastUsedAt,
			RevokedAt:  p.RevokedAt,
		})
	}
	return ok(c, out)
}

type RevokeAppPasswordRequest struct {
	ID int `param:"id"`
}

// Revoke permanently disables one of the user's app passwords.
func (h *AppPasswordHandler) Revoke(c *okapi.Context, req *RevokeAppPasswordRequest) error {
	if h.repo == nil {
		return c.AbortInternalServerError("app passwords are not available")
	}
	userID := uint(c.GetInt("user_id"))

	affected, err := h.repo.Revoke(userID, uint(req.ID))
	if err != nil {
		return c.AbortInternalServerError("failed to revoke app password")
	}
	if affected == 0 {
		// Either not the user's row or already revoked — both read as 404 so a
		// foreign ID cannot be probed.
		if _, ferr := h.repo.FindByUserAndID(userID, uint(req.ID)); ferr != nil {
			return c.AbortNotFound("app password not found")
		}
		return c.AbortBadRequest("app password already revoked")
	}
	return ok(c, okapi.M{"message": "app password revoked"})
}

// VerifyAppPassword checks a presented app-password plaintext against the
// active bcrypt digests of one user's app passwords (the ADR-004 entry point
// for the mail-client authentication backends). bcrypt's digest comparison is
// constant-time; every digest is compared even after a match so the call's
// cost does not leak which row matched.
func VerifyAppPassword(hashes []string, presented string) bool {
	if presented == "" {
		return false
	}
	matched := false
	for _, hash := range hashes {
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(presented)) == nil {
			matched = true // keep burning the loop to flatten timing
		}
	}
	return matched
}

// GenerateAppPassword mints "apw_" + 32 random bytes (64 hex chars). Exported
// for the ADR-004 backend tests.
func GenerateAppPassword() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return AppPasswordPrefix + hex.EncodeToString(raw), nil
}
