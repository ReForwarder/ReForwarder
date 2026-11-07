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
	"net/http"
	"time"

	"strings"

	"github.com/google/uuid"
	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/auth"
	"github.com/goposta/posta/internal/services/clientinfo"
	"github.com/goposta/posta/internal/services/emailverify"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/notification"
	"github.com/goposta/posta/internal/services/passwordreset"
	"github.com/goposta/posta/internal/services/seeder"
	"github.com/goposta/posta/internal/services/session"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/services/twofactor"
	"github.com/goposta/posta/internal/services/workspaceprovision"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type UserHandler struct {
	repo          *repositories.UserRepository
	sessionRepo   *repositories.SessionRepository
	jwtSecret     []byte
	seeder        *seeder.Seeder
	bus           *eventbus.EventBus
	settings      *settings.Provider
	notifier      *notification.Service
	emailVerifier *emailverify.Service
	passwordReset *passwordreset.Service
	db            *gorm.DB
	migrator      *workspaceprovision.Service
	audit         *audit.Logger
	recovery      *RecoveryCodeStore
	// refresh issues the CA-03 rotating refresh tokens. nil when
	// POSTA_REFRESH_TOKENS_ENABLED=false — logins then behave exactly as
	// before this wave.
	refresh *auth.RefreshTokenService
	// appPasswords backs the SF-35/SF-67 app-password management surface.
	// nil in bare test constructions.
	appPasswords *repositories.UserAppPasswordRepository
}

func NewUserHandler(repo *repositories.UserRepository, jwtSecret string, seeder *seeder.Seeder, bus *eventbus.EventBus) *UserHandler {
	return &UserHandler{
		repo:      repo,
		jwtSecret: []byte(jwtSecret),
		seeder:    seeder,
		bus:       bus,
	}
}

func (h *UserHandler) SetMigrator(db *gorm.DB, m *workspaceprovision.Service) {
	h.db = db
	h.migrator = m
	h.recovery = NewRecoveryCodeStore(db)
}

// SetAuditLogger wires the audit trail so recovery-code logins are recorded.
func (h *UserHandler) SetAuditLogger(a *audit.Logger) {
	h.audit = a
}

// SetRefreshTokens wires the CA-03 refresh-token service. Pass nil (or don't
// call this) to run without refresh tokens: logins then return no
// refresh_token and POST /auth/refresh is not registered.
func (h *UserHandler) SetRefreshTokens(s *auth.RefreshTokenService) {
	h.refresh = s
}

// SetAppPasswordRepo wires the SF-35/SF-67 app-password store.
func (h *UserHandler) SetAppPasswordRepo(r *repositories.UserAppPasswordRepository) {
	h.appPasswords = r
}

// AppPasswordActiveHashes returns the still-active bcrypt digests of one
// user's app passwords — the verification seam the ADR-004 mail-client auth
// backends (IMAP/POP3/submission) call with credentials presented on the wire.
func (h *UserHandler) AppPasswordActiveHashes(userID uint) ([]string, error) {
	if h.appPasswords == nil {
		return nil, fmt.Errorf("app passwords are not wired")
	}
	rows, err := h.appPasswords.FindActiveByUserID(userID)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(rows))
	for _, r := range rows {
		hashes = append(hashes, r.PasswordHash)
	}
	return hashes, nil
}

// twoFactorEnrollmentRequired implements the GAP-12 policy check: the
// two_factor_required platform setting demands every account — admins
// included — to have 2FA configured before it can complete a sign-in. There is
// deliberately no admin exemption: the whole point of the policy is that an
// administrator account without a second factor is the highest-value target.
func (h *UserHandler) twoFactorEnrollmentRequired(user *models.User) bool {
	if h.settings == nil || !h.settings.TwoFactorRequired() {
		return false
	}
	return !user.TwoFactorEnabled
}

// recoveryCodes returns the recovery-code store, or nil when the handler was
// built without a database (some tests construct it bare).
func (h *UserHandler) recoveryCodes() *RecoveryCodeStore {
	return h.recovery
}

func (h *UserHandler) ensurePersonalWorkspace(userID uint) {
	if h.migrator == nil || h.db == nil {
		return
	}
	if _, err := h.migrator.EnsureWorkspace(h.db, userID); err != nil {
		logger.Error("failed to provision personal workspace", "user_id", userID, "err", err)
	}
}

func (h *UserHandler) SetSessionRepo(repo *repositories.SessionRepository) {
	h.sessionRepo = repo
}

const jwtTokenTTL = 24 * time.Hour

func (h *UserHandler) SetSettings(s *settings.Provider) {
	h.settings = s
}

// SetNotifier sets the notification service for sending welcome and password change emails.
func (h *UserHandler) SetNotifier(n *notification.Service) {
	h.notifier = n
}

// SetEmailVerifier wires the email verification service.
func (h *UserHandler) SetEmailVerifier(s *emailverify.Service) {
	h.emailVerifier = s
}

// SetPasswordReset wires the self-service password reset service.
func (h *UserHandler) SetPasswordReset(s *passwordreset.Service) {
	h.passwordReset = s
}

// passwordResetEnabled reports whether the forgot-password flow is both turned
// on in platform settings and actually deliverable (notifier configured).
func (h *UserHandler) passwordResetEnabled() bool {
	return h.settings != nil && h.settings.PasswordResetEnabled() &&
		h.passwordReset != nil && h.passwordReset.Deliverable()
}

type LoginRequest struct {
	Body struct {
		Email         string `json:"email" required:"true" format:"email"`
		Password      string `json:"password" required:"true" minLength:"4"`
		TwoFactorCode string `json:"two_factor_code"`
	} `json:"body"`
}

// AuthUser is the public shape of the authenticated account in login payloads.
type AuthUser struct {
	ID    uint            `json:"id"`
	Name  string          `json:"name"`
	Email string          `json:"email"`
	Role  models.UserRole `json:"role"`
}

type AuthResponse struct {
	Token string   `json:"token"`
	User  AuthUser `json:"user"`
	// RefreshToken is the CA-03 single-use rotation token (POST /auth/refresh
	// redeems it for a fresh pair). Empty when refresh tokens are disabled.
	RefreshToken string `json:"refresh_token,omitempty"`
	// TwoFactorEnrollmentRequired is the GAP-12 forced-enrollment marker: the
	// two_factor_required policy applies and this account has no second factor
	// yet. The dashboard must land the user on the 2FA setup card before
	// anything else; see the Login handler's documentation for the full flow.
	TwoFactorEnrollmentRequired bool `json:"two_factor_enrollment_required,omitempty"`
}

type UserProfile struct {
	ID                        uint            `json:"id"`
	Name                      string          `json:"name"`
	Email                     string          `json:"email"`
	Role                      models.UserRole `json:"role"`
	TwoFactorEnabled          bool            `json:"two_factor_enabled"`
	TwoFactorEnrollmentRequired bool          `json:"two_factor_enrollment_required"`
	RequireVerifiedDomain     bool            `json:"require_verified_domain"`
	ScheduledDeletionAt       *time.Time      `json:"scheduled_deletion_at"`
	EmailVerifiedAt           *time.Time      `json:"email_verified_at"`
	EmailVerificationRequired bool            `json:"email_verification_required"`
	DefaultWorkspaceID        *uint           `json:"default_workspace_id"`
	// Deprecated: renamed to default_workspace_id. Mirrors it for one minor
	// release so an older dashboard build keeps working.
	PersonalWorkspaceID *uint `json:"personal_workspace_id"`
	// ImpersonatedBy carries the admin's user ID while the caller's session is
	// an R-20 impersonation session, so the dashboard can show the
	// "viewing as" banner. Absent on ordinary sessions.
	ImpersonatedBy      *uint     `json:"impersonated_by,omitempty"`
	ImpersonationReason string    `json:"impersonation_reason,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type Enable2FAResponse struct {
	Secret string `json:"secret"`
	URL    string `json:"url"` // otpauth:// URL for QR code
}

type Verify2FARequest struct {
	Body struct {
		Code string `json:"code" required:"true" minLength:"6" maxLength:"6"`
	} `json:"body"`
}

type Disable2FARequest struct {
	Body struct {
		Code string `json:"code" required:"true" minLength:"6" maxLength:"6"`
	} `json:"body"`
}

type UpdateProfileRequest struct {
	Body struct {
		Name                  string `json:"name" required:"true" minLength:"1"`
		RequireVerifiedDomain *bool  `json:"require_verified_domain"`
	} `json:"body"`
}

type ChangePasswordRequest struct {
	Body struct {
		CurrentPassword string `json:"current_password" required:"true"`
		NewPassword     string `json:"new_password" required:"true" minLength:"8"`
	} `json:"body"`
}

type RegisterRequest struct {
	Body struct {
		Name     string `json:"name" required:"true" minLength:"1"`
		Email    string `json:"email" required:"true" format:"email"`
		Password string `json:"password" required:"true" minLength:"8"`
	} `json:"body"`
}

// Register allows new users to self-register when registration is enabled.
func (h *UserHandler) Register(c *okapi.Context, req *RegisterRequest) error {
	if h.settings == nil || !h.settings.RegistrationEnabled() {
		return c.AbortForbidden("registration is disabled")
	}

	email := strings.TrimSpace(strings.ToLower(req.Body.Email))
	if email == "" {
		return c.AbortBadRequest("email is required")
	}

	// Check allowed signup domains
	allowedDomains := h.settings.GetString("allowed_signup_domains", "")
	if allowedDomains != "" {
		parts := strings.SplitN(email, "@", 2)
		if len(parts) != 2 {
			return c.AbortBadRequest("invalid email address")
		}
		domain := parts[1]
		allowed := false
		for _, d := range strings.Split(allowedDomains, ",") {
			if strings.TrimSpace(d) == domain {
				allowed = true
				break
			}
		}
		if !allowed {
			return c.AbortForbidden("registration is not allowed for this email domain")
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Body.Password), bcrypt.DefaultCost)
	if err != nil {
		return c.AbortInternalServerError("failed to hash password", err)
	}

	user := &models.User{
		Name:         strings.TrimSpace(req.Body.Name),
		Email:        email,
		PasswordHash: string(hash),
		Role:         models.UserRoleUser,
	}

	if err := h.repo.Create(user); err != nil {
		return c.AbortConflict("email already registered")
	}

	// Provision the user's personal workspace (and seed its default content)
	// before issuing the JWT, so the first authenticated request lands in it.
	h.ensurePersonalWorkspace(user.ID)

	if h.bus != nil {
		h.bus.PublishSimple(models.EventCategoryUser, "user.registered", &user.ID, user.Email, c.RealIP(),
			fmt.Sprintf("User %q registered", user.Email), nil)
	}

	// Email verification: if the notifier is configured, send a verification email.
	// Otherwise (self-hosted without SMTP) auto-verify so the account isn't locked out.
	// The welcome email is deferred until after verification (see VerifyEmail).
	if h.emailVerifier != nil {
		if h.emailVerifier.Required() {
			go func() {
				if err := h.emailVerifier.IssueAndSend(user); err != nil {
					logger.Error("failed to send verification email", "user_id", user.ID, "err", err)
				}
			}()
		} else {
			_ = h.emailVerifier.MarkVerifiedNow(user)
		}
	}

	// Auto-login: generate JWT token (and the CA-03 refresh token). A fresh
	// account has no second factor, so with the GAP-12 policy on the response
	// carries the enrollment marker just like a password login would.
	resp, err := h.authResponse(c, user, h.twoFactorEnrollmentRequired(user))
	if err != nil {
		return c.AbortInternalServerError("failed to generate token", err)
	}

	return created(c, resp)
}

func (h *UserHandler) RegistrationStatus(c *okapi.Context) error {
	enabled := h.settings != nil && h.settings.RegistrationEnabled()
	return ok(c, okapi.M{
		"registration_enabled":   enabled,
		"password_reset_enabled": h.passwordResetEnabled(),
	})
}

type ForgotPasswordRequest struct {
	Body struct {
		Email string `json:"email" required:"true" format:"email"`
	} `json:"body"`
}

type ResetPasswordRequest struct {
	Body struct {
		Token       string `json:"token" required:"true"`
		NewPassword string `json:"new_password" required:"true" minLength:"8"`
	} `json:"body"`
}

func (h *UserHandler) ForgotPassword(c *okapi.Context, req *ForgotPasswordRequest) error {
	if !h.passwordResetEnabled() {
		return c.AbortBadRequest("password reset is not enabled")
	}

	const genericMsg = "If an account exists for that email, a password reset link is on its way."
	email := strings.ToLower(strings.TrimSpace(req.Body.Email))

	user, err := h.repo.FindByEmail(email)
	if err != nil || user == nil {
		return ok(c, okapi.M{"message": genericMsg})
	}
	// Rate-limit per account to curb token flooding; stay generic on the wire.
	if err := h.passwordReset.CanIssue(user.ID); err != nil {
		return ok(c, okapi.M{"message": genericMsg})
	}
	if err := h.passwordReset.IssueAndSend(user); err != nil {
		logger.Error("failed to send password reset email", "user_id", user.ID, "err", err)
		return ok(c, okapi.M{"message": genericMsg})
	}
	if h.bus != nil {
		h.bus.PublishSimple(models.EventCategoryUser, "user.password_reset_requested", &user.ID, user.Email, c.RealIP(),
			fmt.Sprintf("Password reset requested for %q", user.Email), nil)
	}
	return ok(c, okapi.M{"message": genericMsg})
}

// ResetPassword redeems a reset token and sets the new password.
func (h *UserHandler) ResetPassword(c *okapi.Context, req *ResetPasswordRequest) error {
	if !h.passwordResetEnabled() {
		return c.AbortBadRequest("password reset is not enabled")
	}

	user, err := h.passwordReset.Redeem(req.Body.Token, req.Body.NewPassword)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}

	if h.bus != nil {
		h.bus.PublishSimple(models.EventCategoryUser, "user.password_reset", &user.ID, user.Email, c.RealIP(),
			fmt.Sprintf("Password reset completed for %q", user.Email), nil)
	}
	// Notify the account owner that their password changed (best-effort).
	if h.notifier != nil {
		go func(uid uint) {
			_ = h.notifier.SendSecurityToUser(uid, "Your password has been changed", notification.TemplatePasswordChanged, map[string]any{
				"ChangedAt": time.Now().UTC().Format("January 2, 2006 at 15:04 UTC"),
			})
		}(user.ID)
	}
	return ok(c, okapi.M{"message": "password updated successfully"})
}

type VerifyEmailRequest struct {
	Token string `query:"token" required:"true"`
}

// VerifyEmail redeems a verification token and marks the user's email as verified.
func (h *UserHandler) VerifyEmail(c *okapi.Context, req *VerifyEmailRequest) error {
	if h.emailVerifier == nil {
		return c.AbortBadRequest("email verification is not enabled")
	}
	user, newlyVerified, err := h.emailVerifier.Redeem(req.Token)
	if err != nil {
		return c.AbortBadRequest(err.Error())
	}
	if newlyVerified {
		if h.bus != nil {
			h.bus.PublishSimple(models.EventCategoryUser, "user.email_verified", &user.ID, user.Email, c.RealIP(),
				fmt.Sprintf("User %q verified email", user.Email), nil)
		}
		// Fire the welcome email now that we know the address works.
		if h.notifier != nil {
			go func(uid uint) {
				_ = h.notifier.SendToUser(uid, "Welcome to Posta!", notification.TemplateWelcome, nil)
			}(user.ID)
		}
	}
	return ok(c, okapi.M{"message": "email verified"})
}

// ResendVerificationEmail issues a fresh token for the authenticated user.
func (h *UserHandler) ResendVerificationEmail(c *okapi.Context) error {
	if h.emailVerifier == nil || !h.emailVerifier.Required() {
		return c.AbortBadRequest("email verification is not enabled")
	}
	userID := c.GetInt("user_id")
	user, err := h.repo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if user.EmailVerifiedAt != nil {
		return ok(c, okapi.M{"message": "email already verified"})
	}
	if err := h.emailVerifier.CanResend(user.ID); err != nil {
		return c.AbortTooManyRequests(err.Error())
	}
	if err := h.emailVerifier.IssueAndSend(user); err != nil {
		logger.Error("failed to resend verification email", "user_id", user.ID, "err", err)
		return c.AbortInternalServerError("failed to send verification email")
	}
	return ok(c, okapi.M{"message": "verification email sent"})
}

// UpdateProfile allows authenticated users to update their profile (name).
func (h *UserHandler) UpdateProfile(c *okapi.Context, req *UpdateProfileRequest) error {
	userID := c.GetInt("user_id")
	user, err := h.repo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}

	user.Name = req.Body.Name
	if req.Body.RequireVerifiedDomain != nil {
		user.RequireVerifiedDomain = *req.Body.RequireVerifiedDomain
	}
	if err := h.repo.Update(user); err != nil {
		return c.AbortInternalServerError("failed to update profile")
	}

	return ok(c, h.buildProfile(c, user))
}

// ChangePassword allows authenticated users to change their own password.
func (h *UserHandler) ChangePassword(c *okapi.Context, req *ChangePasswordRequest) error {
	userID := c.GetInt("user_id")
	user, err := h.repo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Body.CurrentPassword)); err != nil {
		return c.AbortBadRequest("current password is incorrect")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Body.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return c.AbortInternalServerError("failed to hash password", err)
	}

	user.PasswordHash = string(hash)
	if err := h.repo.Update(user); err != nil {
		return c.AbortInternalServerError("failed to update password")
	}

	// CA-03 hygiene: a password change revokes every live refresh token, so a
	// stolen long-lived credential cannot outlive the new password.
	if h.refresh != nil {
		if err := h.refresh.RevokeAllForUser(user.ID); err != nil {
			logger.Error("failed to revoke refresh tokens after password change", "user_id", user.ID, "err", err)
		}
	}

	// Send password change notification (best-effort)
	if h.notifier != nil {
		go func() {
			_ = h.notifier.SendSecurityToUser(user.ID, "Your password has been changed", notification.TemplatePasswordChanged, map[string]any{
				"ChangedAt": time.Now().UTC().Format("January 2, 2006 at 15:04 UTC"),
			})
		}()
	}

	return ok(c, okapi.M{"message": "password updated successfully"})
}

func (h *UserHandler) publishLoginFailed(actorID *uint, email, ip, reason string) {
	if h.bus == nil {
		return
	}
	h.bus.PublishSimple(models.EventCategoryUser, "user.login_failed", actorID, email, ip,
		fmt.Sprintf("Failed login attempt for %q (%s)", email, reason),
		map[string]any{"reason": reason})
}

// Login authenticates with email + password (+ TOTP when enrolled).
//
// GAP-12 forced-enrollment flow: when the two_factor_required policy is on and
// the account has no second factor configured, the login DOES complete — the
// account holder has proven their password — but the response carries the
// forced-enrollment marker `two_factor_enrollment_required` and the dashboard
// is required to land the user on the 2FA setup card (Login.vue redirects to
// /profile?setup=2fa) before any other screen. The policy has no admin
// exemption: an administrator without a second factor is exactly the account
// the setting exists to protect.
//
// Users WITH 2FA keep the unchanged flow: a missing code answers the
// requires_2fa pending marker, and the code (or a single-use R-10 recovery
// code) completes the sign-in.
//
// Documented limitation (residual of this wave): the enforcement point is the
// login response and the dashboard redirect; the middleware layer does not yet
// refuse API calls from a session whose account owes enrollment (the
// middleware package was out of scope here), and the profile endpoint exposes
// `two_factor_enrollment_required` so the dashboard can keep the banner up
// until 2FA is enabled.
func (h *UserHandler) Login(c *okapi.Context, req *LoginRequest) error {
	user, err := h.repo.FindByEmail(req.Body.Email)
	if err != nil {
		h.publishLoginFailed(nil, req.Body.Email, c.RealIP(), "unknown_email")
		return c.AbortUnauthorized("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Body.Password)); err != nil {
		h.publishLoginFailed(&user.ID, user.Email, c.RealIP(), "bad_password")
		return c.AbortUnauthorized("invalid credentials")
	}

	if !user.Active {
		h.publishLoginFailed(&user.ID, user.Email, c.RealIP(), "inactive")
		return c.AbortForbidden("account is disabled")
	}

	// GAP-12: the enrollment policy is evaluated on the verified account —
	// admins included — before the second factor is consulted. An un-enrolled
	// account skips the TOTP step entirely (there is nothing to type yet) and
	// the completion below stamps the marker on the response.
	enrollmentRequired := h.twoFactorEnrollmentRequired(user)

	// Check 2FA
	if user.TwoFactorEnabled {
		if req.Body.TwoFactorCode == "" {
			return c.JSON(http.StatusUnauthorized, dto.Response[any]{
				Success: false,
				Data: okapi.M{
					"requires_2fa": true,
					"message":      "2FA code required",
				},
			})
		}
		if !twofactor.ValidateCode(user.TwoFactorSecret, req.Body.TwoFactorCode) {
			// R-10: a single-use recovery code is accepted in place of the TOTP
			// code, so a lost authenticator no longer requires admin help.
			if !h.consumeRecoveryCode(user.ID, req.Body.TwoFactorCode) {
				h.publishLoginFailed(&user.ID, user.Email, c.RealIP(), "bad_2fa")
				return c.AbortUnauthorized("invalid 2FA code")
			}
			if h.audit != nil {
				h.audit.Log(user.ID, user.Email, c.RealIP(), "user.recovery_code_login",
					fmt.Sprintf("User %q signed in with a 2FA recovery code", user.Email),
					map[string]any{"remaining_codes_hint": "see /users/me/2fa/recovery-codes/count"})
			}
		}
	}

	h.ensurePersonalWorkspace(user.ID)

	// Record last login time
	now := time.Now()
	user.LastLoginAt = &now
	_ = h.repo.Update(user)

	if h.bus != nil {
		h.bus.PublishSimple(models.EventCategoryUser, "user.login", &user.ID, user.Email, c.RealIP(),
			fmt.Sprintf("User %q logged in", user.Email), nil)
	}

	// Detect a sign-in from a new device before the session is recorded, so the
	// freshly created session doesn't count as a prior known device.
	ua := c.Header("User-Agent")
	newDevice := h.isNewDevice(user.ID, ua)

	resp, err := h.authResponse(c, user, enrollmentRequired)
	if err != nil {
		return c.AbortInternalServerError("failed to generate token", err)
	}

	if newDevice {
		h.sendLoginAlert(user.ID, ua, c.RealIP())
	}

	return ok(c, resp)
}

// authResponse mints the access JWT (unchanged semantics: same claims, session
// row, and cookie), issues the CA-03 refresh token when the service is wired,
// and stamps the GAP-12 enrollment marker. Shared by the password login, the
// registration auto-login, and the passkey login.
func (h *UserHandler) authResponse(c *okapi.Context, user *models.User, enrollmentRequired bool) (AuthResponse, error) {
	token, _, err := h.generateTokenWithSession(c, user)
	if err != nil {
		return AuthResponse{}, err
	}

	// Browser clients ride the HttpOnly cookie; the body's token remains for
	// CLI/SDK clients, which send it back as a Bearer header.
	session.SetCookie(c, token, jwtTokenTTL)

	refreshToken := ""
	if h.refresh != nil {
		rt, rerr := h.refresh.Issue(user.ID)
		if rerr != nil {
			// The session is valid without the refresh token; failing the whole
			// login over the long-lived credential would be the wrong trade.
			logger.Error("failed to issue refresh token", "user_id", user.ID, "err", rerr)
		} else {
			refreshToken = rt
		}
	}

	return AuthResponse{
		Token:                       token,
		RefreshToken:                refreshToken,
		TwoFactorEnrollmentRequired: enrollmentRequired,
		User: AuthUser{
			ID:    user.ID,
			Name:  user.Name,
			Email: user.Email,
			Role:  user.Role,
		},
	}, nil
}

// consumeRecoveryCode burns a single-use 2FA recovery code (R-10). A handler
// built without a database (some tests) never consumes.
func (h *UserHandler) consumeRecoveryCode(userID uint, code string) bool {
	if h.recovery == nil {
		return false
	}
	return h.recovery.Consume(userID, code)
}

// Me returns the current user's profile.
func (h *UserHandler) Me(c *okapi.Context) error {
	userID := c.GetInt("user_id")
	user, err := h.repo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}

	return ok(c, h.buildProfile(c, user))
}

func (h *UserHandler) buildProfile(c *okapi.Context, user *models.User) UserProfile {
	required := h.emailVerifier != nil && h.emailVerifier.Required()
	profile := UserProfile{
		ID:                          user.ID,
		Name:                        user.Name,
		Email:                       user.Email,
		Role:                        user.Role,
		TwoFactorEnabled:            user.TwoFactorEnabled,
		TwoFactorEnrollmentRequired: h.twoFactorEnrollmentRequired(user),
		RequireVerifiedDomain:       user.RequireVerifiedDomain,
		ScheduledDeletionAt:         user.ScheduledDeletionAt,
		EmailVerifiedAt:             user.EmailVerifiedAt,
		EmailVerificationRequired:   required,
		DefaultWorkspaceID:          user.DefaultWorkspaceID,
		PersonalWorkspaceID:         user.DefaultWorkspaceID,
		CreatedAt:                   user.CreatedAt,
	}
	// R-20: an impersonation session surfaces who is acting, so the dashboard
	// can banner "Viewing as …" instead of silently operating as someone else.
	if adminID := c.GetInt("impersonated_by"); adminID > 0 {
		id := uint(adminID)
		profile.ImpersonatedBy = &id
		profile.ImpersonationReason = c.GetString("impersonation_reason")
	}
	return profile
}

// Setup2FA generates a TOTP secret for the user (doesn't enable yet).
func (h *UserHandler) Setup2FA(c *okapi.Context) error {
	userID := c.GetInt("user_id")
	user, err := h.repo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if user.TwoFactorEnabled {
		return c.AbortBadRequest("2FA is already enabled")
	}

	secret, url, err := twofactor.GenerateSecret(user.Email)
	if err != nil {
		return c.AbortInternalServerError("failed to generate 2FA secret")
	}

	// Store secret temporarily (not enabled yet)
	user.TwoFactorSecret = secret
	if err := h.repo.Update(user); err != nil {
		return c.AbortInternalServerError("failed to save 2FA secret")
	}

	return ok(c, Enable2FAResponse{
		Secret: secret,
		URL:    url,
	})
}

// Verify2FA verifies a TOTP code and enables 2FA.
func (h *UserHandler) Verify2FA(c *okapi.Context, req *Verify2FARequest) error {
	userID := c.GetInt("user_id")
	user, err := h.repo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if user.TwoFactorEnabled {
		return c.AbortBadRequest("2FA is already enabled")
	}
	if user.TwoFactorSecret == "" {
		return c.AbortBadRequest("2FA setup not initiated, call setup first")
	}

	if !twofactor.ValidateCode(user.TwoFactorSecret, req.Body.Code) {
		return c.AbortBadRequest("invalid 2FA code")
	}

	user.TwoFactorEnabled = true
	if err := h.repo.Update(user); err != nil {
		return c.AbortInternalServerError("failed to enable 2FA")
	}

	// R-10: issue the single-use recovery codes at enable time. The plaintext
	// is returned exactly once — only bcrypt digests are stored.
	recoveryCodes := []string{}
	if h.recovery != nil {
		if codes, err := h.recovery.Generate(user.ID); err == nil {
			recoveryCodes = codes
		} else {
			logger.Error("failed to generate recovery codes", "user_id", user.ID, "err", err)
		}
	}

	h.sendTwoFactorAlert(user.ID, "enabled")

	return ok(c, okapi.M{
		"message":        "2FA enabled successfully",
		"recovery_codes": recoveryCodes,
	})
}

// Disable2FA disables 2FA after verifying a code.
func (h *UserHandler) Disable2FA(c *okapi.Context, req *Disable2FARequest) error {
	userID := c.GetInt("user_id")
	user, err := h.repo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if !user.TwoFactorEnabled {
		return c.AbortBadRequest("2FA is not enabled")
	}

	if !twofactor.ValidateCode(user.TwoFactorSecret, req.Body.Code) {
		return c.AbortBadRequest("invalid 2FA code")
	}

	user.TwoFactorEnabled = false
	user.TwoFactorSecret = ""
	if err := h.repo.Update(user); err != nil {
		return c.AbortInternalServerError("failed to disable 2FA")
	}

	// R-10: codes belong to the disabled factor — drop them so none survive.
	if h.recovery != nil {
		if err := h.recovery.DeleteAll(user.ID); err != nil {
			logger.Error("failed to delete recovery codes", "user_id", user.ID, "err", err)
		}
	}

	h.sendTwoFactorAlert(user.ID, "disabled")

	return ok(c, okapi.M{"message": "2FA disabled successfully"})
}

func (h *UserHandler) isNewDevice(userID uint, ua string) bool {
	if h.sessionRepo == nil {
		return false
	}
	sessions, err := h.sessionRepo.FindActiveByUserID(userID)
	if err != nil {
		return false
	}
	sig := clientinfo.Parse(ua).Signature()
	for _, s := range sessions {
		if clientinfo.Parse(s.UserAgent).Signature() == sig {
			return false
		}
	}
	return true
}

// sendLoginAlert emails the user about a sign-in from a new device (best-effort).
func (h *UserHandler) sendLoginAlert(userID uint, ua, ip string) {
	if h.notifier == nil {
		return
	}
	client := clientinfo.Parse(ua)
	data := map[string]any{
		"Browser":   client.Browser,
		"OS":        client.OS,
		"Device":    client.Device,
		"IPAddress": ip,
		"LoginAt":   time.Now().UTC().Format("January 2, 2006 at 15:04 UTC"),
	}
	go func() {
		_ = h.notifier.SendSecurityToUser(userID, "New sign-in to your account", notification.TemplateLoginAlert, data)
	}()
}

// sendTwoFactorAlert emails the user when 2FA is enabled or disabled (best-effort).
// action must be "enabled" or "disabled".
func (h *UserHandler) sendTwoFactorAlert(userID uint, action string) {
	if h.notifier == nil {
		return
	}
	subject := "Two-factor authentication " + action
	data := map[string]any{
		"Action":    action,
		"ChangedAt": time.Now().UTC().Format("January 2, 2006 at 15:04 UTC"),
	}
	go func() {
		_ = h.notifier.SendSecurityToUser(userID, subject, notification.TemplateTwoFactorChange, data)
	}()
}

// generateTokenWithSession creates a JWT with a jti claim and records the session.
func (h *UserHandler) generateTokenWithSession(c *okapi.Context, user *models.User) (string, string, error) { //nolint:unparam
	return h.IssueSession(c, user, nil)
}

// IssueSession mints a JWT for user with the standard claims (sub, email,
// role, aud, jti) plus any extra claims the caller adds, records the matching
// session row, and returns the token and its JTI. R-19's passkey login and
// R-20's impersonation both issue sessions here so the JWT, the session row,
// and the cookie never drift apart.
func (h *UserHandler) IssueSession(c *okapi.Context, user *models.User, extraClaims map[string]any) (string, string, error) {
	jti := uuid.NewString()

	claims := map[string]any{
		"sub":   user.ID,
		"email": user.Email,
		"role":  string(user.Role),
		"aud":   "posta",
		"jti":   jti,
	}
	for k, v := range extraClaims {
		claims[k] = v
	}

	token, err := okapi.GenerateJwtToken(h.jwtSecret, claims, jwtTokenTTL)
	if err != nil {
		return "", "", err
	}

	// Track session in database
	if h.sessionRepo != nil {
		ua := c.Header("User-Agent")
		if len(ua) > 512 {
			ua = ua[:512]
		}
		sess := &models.Session{
			UserID:    user.ID,
			JTI:       jti,
			IPAddress: c.RealIP(),
			UserAgent: ua,
			ExpiresAt: time.Now().Add(jwtTokenTTL),
		}
		// R-20: an impersonation session records who is acting on the row, so
		// the sessions list and audits can tell support logins apart.
		if raw, ok := extraClaims["impersonated_by"]; ok {
			if id := claimToUint(raw); id > 0 {
				sess.ImpersonatedBy = &id
				if reason, ok := extraClaims["impersonation_reason"].(string); ok {
					sess.ImpersonationReason = reason
				}
			}
		}
		_ = h.sessionRepo.Create(sess)
	}

	return token, jti, nil
}

// CompletePasskeyLogin finishes a successful passkey authentication exactly
// like the password login completes: workspace provisioning, last-login
// bookkeeping, the login event, new-device alerting, and the JWT + session
// issue (R-19) — plus the GAP-12 enrollment marker and the CA-03 refresh
// token. Called by the WebAuthn handler once the assertion has verified.
func (h *UserHandler) CompletePasskeyLogin(c *okapi.Context, user *models.User) (AuthResponse, error) {
	h.ensurePersonalWorkspace(user.ID)

	// Record last login time
	now := time.Now()
	user.LastLoginAt = &now
	_ = h.repo.Update(user)

	if h.bus != nil {
		h.bus.PublishSimple(models.EventCategoryUser, "user.passkey_login", &user.ID, user.Email, c.RealIP(),
			fmt.Sprintf("User %q logged in with a passkey", user.Email), nil)
	}

	// Detect a sign-in from a new device before the session is recorded, so the
	// freshly created session doesn't count as a prior known device.
	ua := c.Header("User-Agent")
	newDevice := h.isNewDevice(user.ID, ua)

	resp, err := h.authResponse(c, user, h.twoFactorEnrollmentRequired(user))
	if err != nil {
		return AuthResponse{}, c.AbortInternalServerError("failed to generate token", err)
	}

	if newDevice {
		h.sendLoginAlert(user.ID, ua, c.RealIP())
	}

	return resp, nil
}

// validateSecondFactor checks a second-factor code for a passkey login: the
// TOTP code first, then a single-use recovery code, mirroring the password
// login's acceptance rules (R-19).
func (h *UserHandler) validateSecondFactor(user *models.User, code string) bool {
	if twofactor.ValidateCode(user.TwoFactorSecret, code) {
		return true
	}
	return h.consumeRecoveryCode(user.ID, code)
}

// RefreshRequest is the CA-03 redemption body for POST /auth/refresh.
type RefreshRequest struct {
	Body struct {
		RefreshToken string `json:"refresh_token" required:"true"`
	} `json:"body"`
}

// Refresh rotates a refresh token (CA-03): the presented single-use token is
// consumed atomically and replaced by a successor in the same rotation family,
// and a fresh access JWT — same claims, session row, and JTI semantics as a
// login-issued one — is minted for the token's owner.
//
// Reuse detection: presenting a token that was already rotated revokes the
// entire family before the 401 is returned, so a stolen token chain cannot be
// ridden in parallel with its legitimate owner. All failures read as one
// generic 401 — no oracle for which tokens exist.
func (h *UserHandler) Refresh(c *okapi.Context, req *RefreshRequest) error {
	if h.refresh == nil {
		return c.AbortBadRequest("refresh tokens are not enabled")
	}

	newToken, userID, err := h.refresh.Rotate(req.Body.RefreshToken)
	if err != nil {
		_ = err // reuse vs invalid reads identically on the wire
		return c.AbortUnauthorized("invalid or expired refresh token")
	}

	user, err := h.repo.FindByID(userID)
	if err != nil || user == nil {
		return c.AbortUnauthorized("invalid or expired refresh token")
	}
	if !user.Active {
		// A disabled account cannot keep refreshing; kill the family too.
		_ = h.refresh.RevokeAllForUser(userID)
		return c.AbortForbidden("account is disabled")
	}

	token, _, err := h.IssueSession(c, user, nil)
	if err != nil {
		return c.AbortInternalServerError("failed to generate token", err)
	}

	return ok(c, AuthResponse{
		Token:        token,
		RefreshToken: newToken,
		User: AuthUser{
			ID:    user.ID,
			Name:  user.Name,
			Email: user.Email,
			Role:  user.Role,
		},
	})
}

// claimToUint normalizes a JWT claim value (a float64 once decoded from JSON)
// into a uint.
func claimToUint(v any) uint {
	switch n := v.(type) {
	case uint:
		return n
	case uint64:
		return uint(n)
	case int:
		return uint(n)
	case int64:
		return uint(n)
	case float64:
		if n < 0 {
			return 0
		}
		return uint(n)
	}
	return 0
}

// RequestAccountDeletion schedules the current user's account for deletion in 7 days.
func (h *UserHandler) RequestAccountDeletion(c *okapi.Context) error {
	userID := c.GetInt("user_id")
	user, err := h.repo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}

	if user.Role == models.UserRoleAdmin {
		return c.AbortBadRequest("admin accounts cannot be self-deleted")
	}

	if user.ScheduledDeletionAt != nil {
		return c.AbortBadRequest("account deletion is already scheduled")
	}

	deletionDate := time.Now().Add(7 * 24 * time.Hour)
	user.ScheduledDeletionAt = &deletionDate
	user.Active = false

	if err := h.repo.Update(user); err != nil {
		return c.AbortInternalServerError("failed to schedule account deletion")
	}

	if h.bus != nil {
		uid := user.ID
		h.bus.PublishSimple(models.EventCategoryUser, "user.deletion_requested", &uid, user.Email, c.RealIP(),
			fmt.Sprintf("Account deletion scheduled for %s", deletionDate.Format("2006-01-02")), nil)
	}

	// Confirm the scheduled deletion by email (best-effort).
	if h.notifier != nil {
		uid := user.ID
		data := map[string]any{
			"ScheduledFor": deletionDate.UTC().Format("January 2, 2006"),
		}
		go func() {
			_ = h.notifier.SendSecurityToUser(uid, "Your account is scheduled for deletion", notification.TemplateAccountDeletion, data)
		}()
	}

	return ok(c, map[string]any{
		"message":               "Account scheduled for deletion",
		"scheduled_deletion_at": deletionDate,
	})
}

// CancelAccountDeletion cancels a previously scheduled account deletion.
func (h *UserHandler) CancelAccountDeletion(c *okapi.Context) error {
	userID := c.GetInt("user_id")
	user, err := h.repo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}

	if user.ScheduledDeletionAt == nil {
		return c.AbortBadRequest("no deletion is scheduled")
	}

	user.ScheduledDeletionAt = nil
	user.Active = true

	if err := h.repo.Update(user); err != nil {
		return c.AbortInternalServerError("failed to cancel account deletion")
	}

	if h.bus != nil {
		uid := user.ID
		h.bus.PublishSimple(models.EventCategoryUser, "user.deletion_cancelled", &uid, user.Email, c.RealIP(),
			"Account deletion cancelled", nil)
	}

	return ok(c, map[string]any{"message": "Account deletion cancelled"})
}

type SetDefaultWorkspaceRequest struct {
	Body struct {
		WorkspaceID uint `json:"workspace_id" required:"true"`
	} `json:"body"`
}

// SetDefaultWorkspace records which workspace a request that names none lands
// in. The caller must be a member, and the system workspace is not a valid
// choice: landing there by default would hide the user's real work.
func (h *UserHandler) SetDefaultWorkspace(c *okapi.Context, req *SetDefaultWorkspaceRequest) error {
	userID := uint(c.GetInt("user_id"))

	ws, _, err := h.repo.WorkspaceForMember(userID, req.Body.WorkspaceID)
	if err != nil {
		return c.AbortNotFound("workspace not found, or you are not a member of it")
	}
	if ws.System {
		return c.AbortBadRequest("the system workspace cannot be your default")
	}

	if err := h.repo.SetDefaultWorkspace(userID, &ws.ID); err != nil {
		return c.AbortInternalServerError("failed to update the default workspace")
	}
	return ok(c, dto.MessageData{Message: "Default workspace updated"})
}
