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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	wa "github.com/goposta/posta/internal/services/webauthn"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
)

// Passkeys (R-19). Registration ceremonies run for the authenticated user and
// bind to the browser session's JTI; login ceremonies are public but keyed to
// the user's email, which the client must supply at both begin and finish. A
// successful assertion replaces the password step and issues the regular JWT +
// session through the shared login path — and when the account has 2FA, the
// assertion alone is not enough: the client owes the TOTP code through a
// one-shot pending marker, so the second factor always follows the passkey.
type WebAuthnHandler struct {
	svc   *wa.Service
	repo  *repositories.UserRepository
	users *UserHandler
	audit *audit.Logger
}

// NewWebAuthnHandler builds the handler. svc may be nil — passkeys are then
// disabled and every endpoint answers 503 (the dashboard hides the flow).
func NewWebAuthnHandler(svc *wa.Service, repo *repositories.UserRepository, users *UserHandler) *WebAuthnHandler {
	return &WebAuthnHandler{svc: svc, repo: repo, users: users}
}

// SetAuditLogger wires the audit trail for passkey lifecycle events.
func (h *WebAuthnHandler) SetAuditLogger(a *audit.Logger) {
	h.audit = a
}

// BeginWebAuthnLoginRequest carries the email the login ceremony is keyed to.
type BeginWebAuthnLoginRequest struct {
	Body struct {
		Email string `json:"email" required:"true" format:"email"`
	} `json:"body"`
}

// RenameWebAuthnCredentialRequest carries the credential ID and new label.
type RenameWebAuthnCredentialRequest struct {
	ID   string `param:"id"`
	Body struct {
		Name string `json:"name" required:"true" minLength:"1" maxLength:"64"`
	} `json:"body"`
}

// DeleteWebAuthnCredentialRequest carries the credential ID to remove.
type DeleteWebAuthnCredentialRequest struct {
	ID string `param:"id"`
}

// enabled answers the disabled-feature case once, with the 503 the dashboard
// treats as "hide the passkey flow".
func (h *WebAuthnHandler) enabled(c *okapi.Context) bool {
	if h.svc == nil {
		_ = c.AbortServiceUnavailable("passkeys are not configured on this instance")
		return false
	}
	return true
}

// errWebAuthnUserNotFound marks the currentUser abort path. okapi's Abort
// helpers write the 404 and return nil, so the sentinel — not the abort's
// return value — is what stops callers from falling through with a nil user.
var errWebAuthnUserNotFound = errors.New("webauthn: user lookup aborted")

func (h *WebAuthnHandler) currentUser(c *okapi.Context) (*models.User, error) {
	user, err := h.repo.FindByID(uint(c.GetInt("user_id")))
	if err != nil || user == nil {
		_ = c.AbortNotFound("user not found")
		return nil, errWebAuthnUserNotFound
	}
	return user, nil
}

// BeginRegistration returns the credential-creation options for
// navigator.credentials.create(). Requires an authenticated session: the
// ceremony is keyed to the session's JTI.
func (h *WebAuthnHandler) BeginRegistration(c *okapi.Context) error {
	if !h.enabled(c) {
		return nil
	}
	user, err := h.currentUser(c)
	if err != nil {
		// currentUser already wrote the 404; returning its sentinel would
		// make okapi answer the request a second time.
		return nil
	}

	creation, err := h.svc.BeginRegistration(c.Request().Context(), user, c.GetString("jti"))
	if err != nil {
		logger.Error("webauthn registration begin failed", "user_id", user.ID, "err", err)
		return c.AbortInternalServerError("failed to start passkey registration")
	}

	// The Response member marshals to exactly the PublicKeyCredentialCreationOptions
	// dictionary the browser's create() call expects.
	return ok(c, creation.Response)
}

// FinishRegistration validates the client's attestation response and stores
// the credential. Raw handler: the WebAuthn ceremony body must reach the
// library byte-exact, so no request binding happens here.
func (h *WebAuthnHandler) FinishRegistration(c *okapi.Context) error {
	if !h.enabled(c) {
		return nil
	}
	user, err := h.currentUser(c)
	if err != nil {
		// currentUser already wrote the 404; returning its sentinel would
		// make okapi answer the request a second time.
		return nil
	}

	cred, err := h.svc.FinishRegistration(c.Request().Context(), user, c.GetString("jti"), c.Request().Body)
	if err != nil {
		logger.Error("webauthn registration finish failed", "user_id", user.ID, "err", err)
		return c.AbortBadRequest(err.Error())
	}

	if h.audit != nil {
		h.audit.LogCtx(c, "user.passkey_registered",
			"Passkey registered", map[string]any{"credential_id": cred.ID})
	}

	return created(c, cred)
}

// BeginLogin returns the assertion options for navigator.credentials.get().
// Public, but the email is required and the ceremony is keyed to it.
func (h *WebAuthnHandler) BeginLogin(c *okapi.Context, req *BeginWebAuthnLoginRequest) error {
	if !h.enabled(c) {
		return nil
	}
	email := strings.ToLower(strings.TrimSpace(req.Body.Email))
	user, err := h.repo.FindByEmail(email)
	if err != nil || user == nil || !user.Active {
		// One message for unknown accounts, disabled accounts and accounts
		// without any passkey, so the endpoint cannot enumerate them apart.
		return c.AbortBadRequest("passkey sign-in is not available for this account")
	}

	assertion, err := h.svc.BeginLogin(c.Request().Context(), user)
	if err != nil {
		logger.Error("webauthn login begin failed", "email", email, "err", err)
		return c.AbortBadRequest("passkey sign-in is not available for this account")
	}

	return ok(c, assertion.Response)
}

// webauthnLoginFinishBody is the login finish envelope: the assertion produced
// by navigator.credentials.get(), or — on the second-factor step — the pending
// marker from the earlier passkey-verified 401 plus the TOTP code. The email
// rides along because a public finish has no session to identify the caller.
type webauthnLoginFinishBody struct {
	Email         string          `json:"email"`
	Assertion     json.RawMessage `json:"assertion"`
	PendingToken  string          `json:"pending_token"`
	TwoFactorCode string          `json:"two_factor_code"`
}

// FinishLogin verifies the assertion and issues the JWT + session (through the
// shared login path), or, when the account has 2FA, answers the requires_2fa
// 401 with a one-shot pending marker that the TOTP completion step redeems.
// Raw handler: the assertion is extracted from the wrapper and handed to the
// library byte-exact.
func (h *WebAuthnHandler) FinishLogin(c *okapi.Context) error {
	if !h.enabled(c) {
		return nil
	}

	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.AbortBadRequest("invalid request body")
	}
	var body webauthnLoginFinishBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return c.AbortBadRequest("invalid request body")
	}

	// Second-factor completion: redeem the pending marker from a verified passkey.
	if body.PendingToken != "" {
		return h.finishSecondFactor(c, body)
	}

	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || len(body.Assertion) == 0 {
		return c.AbortBadRequest("email and passkey assertion are required")
	}
	user, err := h.repo.FindByEmail(email)
	if err != nil || user == nil {
		return c.AbortUnauthorized("invalid credentials")
	}

	if _, err := h.svc.FinishLogin(c.Request().Context(), user, assertionBody(body.Assertion)); err != nil {
		if h.users != nil {
			h.users.publishLoginFailed(&user.ID, user.Email, c.RealIP(), "bad_passkey")
		}
		logger.Error("webauthn login finish failed", "email", email, "err", err)
		return c.AbortUnauthorized("passkey verification failed")
	}

	// The passkey replaced the password; 2FA still applies on top of it.
	if user.TwoFactorEnabled {
		if body.TwoFactorCode != "" && h.secondFactorVerified(user, body.TwoFactorCode) {
			return h.issueLogin(c, user)
		}
		token, err := h.svc.MarkTwoFactorPending(c.Request().Context(), user.ID)
		if err != nil {
			logger.Error("webauthn pending 2FA marker failed", "user_id", user.ID, "err", err)
			return c.AbortInternalServerError("failed to start the second-factor step")
		}
		// Same shape as the password login's requires_2fa 401, plus the marker
		// that proves the passkey step already succeeded. The response
		// interceptor lets requires_2fa 401s through without logging out.
		return c.JSON(http.StatusUnauthorized, dto.Response[any]{
			Success: false,
			Data: okapi.M{
				"requires_2fa":     true,
				"message":          "2FA code required",
				"passkey_verified": true,
				"pending_token":    token,
			},
		})
	}

	return h.issueLogin(c, user)
}

// finishSecondFactor completes a passkey login by redeeming the pending
// marker with the TOTP (or recovery) code.
func (h *WebAuthnHandler) finishSecondFactor(c *okapi.Context, body webauthnLoginFinishBody) error {
	userID, err := h.svc.TakeTwoFactorPending(c.Request().Context(), body.PendingToken)
	if err != nil {
		return c.AbortUnauthorized(err.Error())
	}
	user, err := h.repo.FindByID(userID)
	if err != nil || user == nil {
		return c.AbortNotFound("user not found")
	}
	if !user.TwoFactorEnabled {
		return c.AbortBadRequest("2FA is not enabled")
	}
	if !h.secondFactorVerified(user, body.TwoFactorCode) {
		if h.users != nil {
			h.users.publishLoginFailed(&user.ID, user.Email, c.RealIP(), "bad_2fa")
		}
		return c.AbortUnauthorized("invalid 2FA code")
	}
	return h.issueLogin(c, user)
}

// secondFactorVerified checks the TOTP code, falling back to a single-use
// recovery code exactly like the password login does.
func (h *WebAuthnHandler) secondFactorVerified(user *models.User, code string) bool {
	if h.users == nil {
		return false
	}
	return h.users.validateSecondFactor(user, code)
}

// issueLogin delegates the successful passkey login to the shared session
// issuance path (R-19 login flow integration).
func (h *WebAuthnHandler) issueLogin(c *okapi.Context, user *models.User) error {
	if h.users == nil {
		return c.AbortInternalServerError("login service unavailable")
	}
	res, err := h.users.CompletePasskeyLogin(c, user)
	if err != nil {
		return err
	}
	return ok(c, res)
}

// ListCredentials returns the authenticated user's registered passkeys.
func (h *WebAuthnHandler) ListCredentials(c *okapi.Context) error {
	if !h.enabled(c) {
		return nil
	}
	creds, err := h.svc.ListCredentials(uint(c.GetInt("user_id")))
	if err != nil {
		return c.AbortInternalServerError("failed to load passkeys")
	}
	return ok(c, creds)
}

// RenameCredential renames one of the caller's own passkeys.
func (h *WebAuthnHandler) RenameCredential(c *okapi.Context, req *RenameWebAuthnCredentialRequest) error {
	if !h.enabled(c) {
		return nil
	}
	name := strings.TrimSpace(req.Body.Name)
	if err := h.svc.RenameCredential(uint(c.GetInt("user_id")), req.ID, name); err != nil {
		if err == wa.ErrCredentialNotFound {
			return c.AbortNotFound("passkey not found")
		}
		return c.AbortInternalServerError("failed to rename passkey")
	}
	return ok(c, okapi.M{"message": "passkey renamed"})
}

// DeleteCredential removes one of the caller's own passkeys.
func (h *WebAuthnHandler) DeleteCredential(c *okapi.Context, req *DeleteWebAuthnCredentialRequest) error {
	if !h.enabled(c) {
		return nil
	}
	if err := h.svc.DeleteCredential(uint(c.GetInt("user_id")), req.ID); err != nil {
		if err == wa.ErrCredentialNotFound {
			return c.AbortNotFound("passkey not found")
		}
		return c.AbortInternalServerError("failed to delete passkey")
	}
	if h.audit != nil {
		h.audit.LogCtx(c, "user.passkey_removed",
			"Passkey removed", map[string]any{"credential_id": req.ID})
	}
	return noContent(c)
}

// assertionBody rebuilds the raw credential JSON the library parses. The
// client sends the assertion nested under "assertion" next to the email; the
// library expects the bare PublicKeyCredentialJSON, so the extracted member is
// handed over unchanged.
func assertionBody(assertion json.RawMessage) io.Reader {
	return strings.NewReader(string(assertion))
}
