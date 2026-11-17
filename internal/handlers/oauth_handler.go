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
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/auth"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/seeder"
	sessionpkg "github.com/goposta/posta/internal/services/session"
	"github.com/goposta/posta/internal/services/workspaceprovision"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type OAuthHandler struct {
	oauthService *auth.OAuthService
	providerRepo *repositories.OAuthProviderRepository
	accountRepo  *repositories.OAuthAccountRepository
	userRepo     *repositories.UserRepository
	sessionRepo  *repositories.SessionRepository
	jwtSecret    []byte
	bus          *eventbus.EventBus
	seeder       *seeder.Seeder
	redisClient  *redis.Client
	callbackBase string // e.g. "http://localhost:9000"
	appWebURL    string // e.g. "http://localhost:9000"
	db           *gorm.DB
	migrator     *workspaceprovision.Service

	// state is the optional CSRF-state seam override (tests); nil keeps the
	// constructor-wired Redis client as the store.
	state oauthStateStore
}

// SetMigrator wires the personal-workspace migrator used to provision (and seed)
// a personal workspace after OAuth user upsert. See §4.
func (h *OAuthHandler) SetMigrator(db *gorm.DB, m *workspaceprovision.Service) {
	h.db = db
	h.migrator = m
}

// oauthStateStore is the CSRF-state seam behind the authorize/callback pair:
// Redis in production (the constructor's client), an in-memory store in tests.
// The state's lifecycle is set-on-authorize, get-once-and-delete-on-callback,
// TTL-bounded.
type oauthStateStore interface {
	SetState(ctx context.Context, key, value string, ttl time.Duration)
	GetState(ctx context.Context, key string) (string, bool)
	DelState(ctx context.Context, key string)
}

// redisOAuthState adapts the constructor-wired Redis client to the seam.
type redisOAuthState struct{ client *redis.Client }

func (s redisOAuthState) SetState(ctx context.Context, key, value string, ttl time.Duration) {
	_ = s.client.Set(ctx, key, value, ttl)
}

func (s redisOAuthState) GetState(ctx context.Context, key string) (string, bool) {
	val, err := s.client.Get(ctx, key).Result()
	if err != nil {
		return "", false
	}
	return val, true
}

func (s redisOAuthState) DelState(ctx context.Context, key string) {
	_ = s.client.Del(ctx, key)
}

// SetStateStore overrides the state store (tests inject an in-memory store;
// production never calls this — the constructor's Redis client stays the
// default).
func (h *OAuthHandler) SetStateStore(s oauthStateStore) { h.state = s }

// stateStore resolves the effective store: the injected seam, else the Redis
// client (nil client + nil seam = a nil store, which the handlers refuse —
// the pre-seam behavior where a Redis-less deployment cannot run OAuth flows).
func (h *OAuthHandler) stateStore() oauthStateStore {
	if h.state != nil {
		return h.state
	}
	if h.redisClient == nil {
		return nil
	}
	return redisOAuthState{client: h.redisClient}
}

func NewOAuthHandler(
	oauthService *auth.OAuthService,
	providerRepo *repositories.OAuthProviderRepository,
	accountRepo *repositories.OAuthAccountRepository,
	userRepo *repositories.UserRepository,
	sessionRepo *repositories.SessionRepository,
	jwtSecret string,
	seeder *seeder.Seeder,
	bus *eventbus.EventBus,
	redisClient *redis.Client,
	callbackBase string,
	appWebURL string,
) *OAuthHandler {
	return &OAuthHandler{
		oauthService: oauthService,
		providerRepo: providerRepo,
		accountRepo:  accountRepo,
		userRepo:     userRepo,
		sessionRepo:  sessionRepo,
		jwtSecret:    []byte(jwtSecret),
		seeder:       seeder,
		bus:          bus,
		redisClient:  redisClient,
		callbackBase: callbackBase,
		appWebURL:    appWebURL,
	}
}

type OAuthProviderInfo struct {
	ID   uint   `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type OAuthLinkedAccount struct {
	ID           uint   `json:"id"`
	ProviderID   uint   `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	ProviderType string `json:"provider_type"`
	Email        string `json:"email"`
	CreatedAt    string `json:"created_at"`
}

type UnlinkOAuthRequest struct {
	ProviderID int `param:"provider_id"`
}

type DiscoverSSORequest struct {
	Body struct {
		Email string `json:"email" required:"true"`
	} `json:"body"`
}

type DiscoverSSOResponse struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// ListProviders returns enabled OAuth providers for the login page
func (h *OAuthHandler) ListProviders(c *okapi.Context) error {
	providers, err := h.providerRepo.FindEnabled()
	if err != nil {
		return ok(c, okapi.M{"providers": []OAuthProviderInfo{}, "sso_available": false})
	}

	var result []OAuthProviderInfo
	for _, p := range providers {
		result = append(result, OAuthProviderInfo{
			ID:   p.ID,
			Slug: p.Slug,
			Name: p.Name,
			Type: string(p.Type),
		})
	}

	ssoAvailable, _ := h.providerRepo.HasEnabledHidden()

	return ok(c, okapi.M{"providers": result, "sso_available": ssoAvailable})
}

// DiscoverSSO looks up an enabled OAuth provider whose AllowedDomains matches the
// email's domain.
func (h *OAuthHandler) DiscoverSSO(c *okapi.Context, req *DiscoverSSORequest) error {
	email := strings.TrimSpace(req.Body.Email)
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return c.AbortBadRequest("invalid email")
	}
	domain := email[at+1:]

	provider, err := h.providerRepo.FindEnabledByDomain(domain)
	if err != nil {
		return c.AbortNotFound("no SSO provider for this email")
	}

	return ok(c, DiscoverSSOResponse{
		Slug: provider.Slug,
		Name: provider.Name,
		Type: string(provider.Type),
	})
}

// Authorize redirects the user to the OAuth provider's authorization page.
func (h *OAuthHandler) Authorize(c *okapi.Context) error {
	providerSlug := c.Param("provider")
	provider, err := h.providerRepo.FindBySlug(providerSlug)
	if err != nil {
		return c.AbortNotFound("OAuth provider not found")
	}

	// Generate state for CSRF protection
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return c.AbortInternalServerError("failed to generate state")
	}
	state := hex.EncodeToString(stateBytes)

	store := h.stateStore()
	if store == nil {
		return c.AbortInternalServerError("OAuth state store unavailable")
	}

	// Store the state with a 10-minute TTL
	stateData, _ := json.Marshal(map[string]string{
		"provider_slug": provider.Slug,
		"created_at":    time.Now().UTC().Format(time.RFC3339),
	})
	store.SetState(c.Request().Context(), "oauth:state:"+state, string(stateData), 10*time.Minute)

	redirectURI := fmt.Sprintf("%s/api/v1/auth/oauth/%s/callback", h.callbackBase, provider.Slug)
	oauthCfg, err := h.oauthService.GetOAuthConfig(provider, redirectURI)
	if err != nil {
		return c.AbortBadRequest("OAuth provider misconfigured: " + err.Error())
	}
	authURL := oauthCfg.AuthCodeURL(state)

	c.Redirect(http.StatusFound, authURL)
	return nil
}

// LinkProviderRequest names the provider whose identity the authenticated
// session user wants to link (SF-68).
type LinkProviderRequest struct {
	Body struct {
		Provider string `json:"provider" required:"true"`
	} `json:"body"`
}

// LinkProvider starts the OAuth flow with LINK intent for the authenticated
// session user (SF-68): the state carries the intent and the session user's
// id, and the response carries the provider's authorize URL for the browser to
// navigate to — the callback then binds the provider identity to THAT account
// instead of running the login semantics. The user id comes from the
// server-side session (never from the client) and the callback additionally
// requires the browser's session cookie to agree with it — a state alone is
// replayable within its TTL, the cookie proves the session is still live in
// THIS browser.
func (h *OAuthHandler) LinkProvider(c *okapi.Context, req *LinkProviderRequest) error {
	userID := c.GetInt("user_id")
	if userID <= 0 {
		return c.AbortUnauthorized("authentication required")
	}
	slug := strings.TrimSpace(req.Body.Provider)
	provider, err := h.providerRepo.FindBySlug(slug)
	if err != nil {
		return c.AbortNotFound("OAuth provider not found")
	}

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return c.AbortInternalServerError("failed to generate state")
	}
	state := hex.EncodeToString(stateBytes)

	store := h.stateStore()
	if store == nil {
		return c.AbortInternalServerError("OAuth state store unavailable")
	}
	stateData, _ := json.Marshal(map[string]string{
		"provider_slug": provider.Slug,
		"created_at":    time.Now().UTC().Format(time.RFC3339),
		"link":          "1",
		"user_id":       strconv.Itoa(userID),
	})
	store.SetState(c.Request().Context(), "oauth:state:"+state, string(stateData), 10*time.Minute)

	redirectURI := fmt.Sprintf("%s/api/v1/auth/oauth/%s/callback", h.callbackBase, provider.Slug)
	oauthCfg, err := h.oauthService.GetOAuthConfig(provider, redirectURI)
	if err != nil {
		return c.AbortBadRequest("OAuth provider misconfigured: " + err.Error())
	}
	return ok(c, okapi.M{"authorize_url": oauthCfg.AuthCodeURL(state)})
}

// linkIdentity binds a provider identity to the session user (the SF-68 link
// lifecycle's resolution step). The provider identity stays unique across
// users: an identity already bound to ANOTHER user is refused; re-linking the
// session user's own identity is a no-op reported as already_linked.
func (h *OAuthHandler) linkIdentity(userID uint, provider *models.OAuthProvider, info *auth.OAuthUserInfo) (string, error) {
	account, err := h.accountRepo.FindByProviderAndExternalID(provider.ID, info.Sub)
	if err == nil && account != nil {
		if account.UserID == userID {
			return "already_linked", nil
		}
		return "", fmt.Errorf("provider identity already linked to another user")
	}
	user, err := h.userRepo.FindByID(userID)
	if err != nil {
		return "", fmt.Errorf("user not found")
	}
	account = &models.OAuthAccount{
		UserID:         userID,
		ProviderID:     provider.ID,
		ProviderUserID: info.Sub,
		Email:          info.Email,
		Name:           info.Name,
		AvatarURL:      info.AvatarURL,
	}
	if err := h.accountRepo.Create(account); err != nil {
		return "", fmt.Errorf("failed to link account: %w", err)
	}
	if user.AuthMethod == "password" {
		user.AuthMethod = "both"
		_ = h.userRepo.Update(user)
	}
	return "linked", nil
}

// sessionToken reads the browser session cookie (the JWT the login/callback
// flows set) — empty when absent.
func (h *OAuthHandler) sessionToken(c *okapi.Context) string {
	ck, err := c.Request().Cookie(sessionpkg.CookieName)
	if err != nil || ck == nil {
		return ""
	}
	return ck.Value
}

// sessionUserID verifies a session JWT and returns its subject. ok=false when
// the token is absent, unsigned by this deployment, expired, or carries no
// usable subject.
func (h *OAuthHandler) sessionUserID(token string) (int64, bool) {
	if token == "" {
		return 0, false
	}
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		return h.jwtSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !parsed.Valid {
		return 0, false
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return 0, false
	}
	switch v := mc["sub"].(type) {
	case float64:
		if v <= 0 {
			return 0, false
		}
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n <= 0 {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// Callback handles the OAuth provider's redirect with authorization code.
func (h *OAuthHandler) Callback(c *okapi.Context) error {
	providerSlug := c.Param("provider")
	code := c.Query("code")
	state := c.Query("state")
	oauthError := c.Query("error")

	redirect := func(errMsg string) error {
		c.Redirect(http.StatusFound, h.appWebURL+"/login?error="+errMsg)
		return nil
	}

	if oauthError != "" {
		return redirect(oauthError)
	}
	if code == "" || state == "" {
		return redirect("missing_params")
	}

	// Validate state
	ctx := c.Request().Context()
	store := h.stateStore()
	if store == nil {
		return redirect("invalid_state")
	}
	stateKey := "oauth:state:" + state
	stateVal, ok := store.GetState(ctx, stateKey)
	if !ok {
		return redirect("invalid_state")
	}
	store.DelState(ctx, stateKey)

	// Verify state matches provider
	var stateData map[string]string
	if err := json.Unmarshal([]byte(stateVal), &stateData); err != nil || stateData["provider_slug"] != providerSlug {
		return redirect("state_mismatch")
	}

	provider, err := h.providerRepo.FindBySlug(providerSlug)
	if err != nil {
		return redirect("provider_not_found")
	}

	redirectURI := fmt.Sprintf("%s/api/v1/auth/oauth/%s/callback", h.callbackBase, provider.Slug)

	// Exchange code for tokens + user info
	userInfo, token, err := h.oauthService.ExchangeCode(ctx, provider, code, redirectURI)
	if err != nil {
		logger.Error("OAuth exchange failed for provider %s: %v", provider.Slug, err)
		return redirect("exchange_failed")
	}

	// LINK intent (SF-68): bind the provider identity to the state's session
	// user instead of running the login semantics. The browser's session
	// cookie must agree with the state's user id.
	if stateData["link"] == "1" {
		linkRedirect := func(errMsg string) error {
			c.Redirect(http.StatusFound, h.appWebURL+"/settings/profile?oauth_error="+errMsg)
			return nil
		}
		wantUserID, convErr := strconv.Atoi(stateData["user_id"])
		if convErr != nil || wantUserID <= 0 {
			return linkRedirect("invalid_link_state")
		}
		sessUserID, valid := h.sessionUserID(h.sessionToken(c))
		if !valid || sessUserID != int64(wantUserID) {
			return linkRedirect("link_requires_session")
		}
		status, linkErr := h.linkIdentity(uint(wantUserID), provider, userInfo)
		if linkErr != nil {
			logger.Error("OAuth link failed for provider %s: %v", provider.Slug, linkErr)
			return linkRedirect("identity_in_use")
		}
		if h.bus != nil {
			linkedUserID := uint(wantUserID)
			h.bus.PublishSimple(models.EventCategoryUser, "user.oauth_linked", &linkedUserID,
				"", c.RealIP(),
				fmt.Sprintf("OAuth identity from %s linked to the account", provider.Name), nil)
		}
		c.Redirect(http.StatusFound, h.appWebURL+"/settings/profile?oauth="+status)
		return nil
	}

	// Find or create user
	user, isNew, err := h.oauthService.FindOrCreateUser(provider, userInfo, token)
	if err != nil {
		logger.Error("OAuth account resolution failed for provider %s: %v", provider.Slug, err)
		return redirect("account_error")
	}

	if !user.Active {
		return redirect("account_disabled")
	}

	// Provision the personal workspace (and seed defaults for new users) before
	// issuing the JWT. Idempotent: a no-op for already-migrated returning users.
	if h.migrator != nil && h.db != nil {
		if _, err := h.migrator.EnsureWorkspace(h.db, user.ID); err != nil {
			logger.Error("failed to provision personal workspace", "user_id", user.ID, "err", err)
		}
	}

	// Generate JWT
	jwtToken, err := h.generateToken(c, user)
	if err != nil {
		return redirect("token_generation_failed")
	}

	// Publish event
	if h.bus != nil {
		action := "user.oauth_login"
		if isNew {
			action = "user.oauth_registered"
		}
		h.bus.PublishSimple(models.EventCategoryUser, action, &user.ID, user.Email, c.RealIP(),
			fmt.Sprintf("User %q authenticated via %s", user.Email, provider.Name), nil)
	}

	// Update last login
	now := time.Now()
	user.LastLoginAt = &now
	_ = h.userRepo.Update(user)

	// Hand the session to the browser as an HttpOnly cookie rather than a query
	// parameter, so the JWT never lands in browser history or a Referer header.
	// The SPA callback route just fetches /users/me to learn who it is.
	sessionpkg.SetCookie(c, jwtToken, jwtTokenTTL)
	c.Redirect(http.StatusFound, h.appWebURL+"/auth/oauth/callback")
	return nil
}

// ListLinkedAccounts returns OAuth accounts linked to the current user.
func (h *OAuthHandler) ListLinkedAccounts(c *okapi.Context) error {
	userID := c.GetInt("user_id")

	accounts, err := h.accountRepo.FindByUserID(uint(userID))
	if err != nil {
		return c.AbortInternalServerError("failed to list linked accounts")
	}

	var result []OAuthLinkedAccount
	for _, a := range accounts {
		result = append(result, OAuthLinkedAccount{
			ID:           a.ID,
			ProviderID:   a.ProviderID,
			ProviderName: a.Provider.Name,
			ProviderType: string(a.Provider.Type),
			Email:        a.Email,
			CreatedAt:    a.CreatedAt.Format(time.RFC3339),
		})
	}

	return ok(c, result)
}

// AdminUserOAuthRequest names the user whose linked identities an admin reads.
type AdminUserOAuthRequest struct {
	ID int `param:"id"`
}

// AdminUserLinkedAccountsResponse is the admin-visibility view of one user's
// linked OAuth identities (SF-68): the links + the owning account's email.
type AdminUserLinkedAccountsResponse struct {
	UserEmail string               `json:"user_email"`
	Accounts  []OAuthLinkedAccount `json:"accounts"`
}

// AdminListUserLinkedAccounts returns one user's linked OAuth identities for
// the admin surface (SF-68). Admin-gated by the route group (jwtAdminAuth);
// the response exposes the same non-secret shape as the self-service list —
// access tokens/refresh tokens are json:"-" on the model and never leave.
func (h *OAuthHandler) AdminListUserLinkedAccounts(c *okapi.Context, req *AdminUserOAuthRequest) error {
	user, err := h.userRepo.FindByID(uint(req.ID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	accounts, err := h.accountRepo.FindByUserID(user.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list linked accounts")
	}
	result := []OAuthLinkedAccount{}
	for _, a := range accounts {
		result = append(result, OAuthLinkedAccount{
			ID:           a.ID,
			ProviderID:   a.ProviderID,
			ProviderName: a.Provider.Name,
			ProviderType: string(a.Provider.Type),
			Email:        a.Email,
			CreatedAt:    a.CreatedAt.Format(time.RFC3339),
		})
	}
	return ok(c, AdminUserLinkedAccountsResponse{UserEmail: user.Email, Accounts: result})
}

// UnlinkAccount removes an OAuth provider link from the current user.
func (h *OAuthHandler) UnlinkAccount(c *okapi.Context, req *UnlinkOAuthRequest) error {
	userID := c.GetInt("user_id")

	// Check the user has another auth method before unlinking
	user, err := h.userRepo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}

	account, err := h.accountRepo.FindByUserAndProvider(uint(userID), uint(req.ProviderID))
	if err != nil {
		return c.AbortNotFound("linked account not found")
	}

	// Count remaining OAuth links
	count, _ := h.accountRepo.CountByUserID(uint(userID))
	hasPassword := user.PasswordHash != "" && user.AuthMethod != "oauth"

	if count <= 1 && !hasPassword {
		return c.AbortBadRequest("cannot unlink the last authentication method — set a password first")
	}

	if err := h.accountRepo.Delete(account.ID); err != nil {
		return c.AbortInternalServerError("failed to unlink account")
	}

	// Update auth method if no more OAuth links
	if count <= 1 && hasPassword {
		user.AuthMethod = "password"
		_ = h.userRepo.Update(user)
	}

	return ok(c, okapi.M{"message": "account unlinked"})
}

// generateToken creates a JWT token for the given user.
func (h *OAuthHandler) generateToken(c *okapi.Context, user *models.User) (string, error) {
	jti := uuid.NewString()

	token, err := okapi.GenerateJwtToken(h.jwtSecret, map[string]any{
		"sub":   user.ID,
		"email": user.Email,
		"role":  string(user.Role),
		"aud":   "posta",
		"jti":   jti,
	}, jwtTokenTTL)
	if err != nil {
		return "", err
	}

	// Track session
	if h.sessionRepo != nil {
		session := &models.Session{
			UserID:    user.ID,
			JTI:       jti,
			IPAddress: c.RealIP(),
			UserAgent: c.Request().UserAgent(),
			ExpiresAt: time.Now().Add(jwtTokenTTL),
		}
		_ = h.sessionRepo.Create(session)
	}

	return token, nil
}
