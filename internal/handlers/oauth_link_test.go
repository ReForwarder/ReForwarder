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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/auth"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// SF-68 tests: the OAuth link/unlink administration for the existing login
// identities — the LINK lifecycle (an authenticated session starts the
// authorize flow with link intent; the callback binds the provider identity to
// THAT account instead of running login semantics), the isolation (a provider
// identity already bound to another user is refused; the session cookie must
// agree with the link state), the unlink lifecycle (the last-auth-method
// guard, the auth-method downgrade), and the admin visibility of linked
// identities. The provider handshake runs against in-process httptest token/
// userinfo endpoints via the custom-OIDC explicit-endpoint configuration; the
// CSRF state rides an in-memory store over the OAuthHandler's seam (production
// keeps the constructor-wired Redis client).

const (
	olTestSecret = "oauth-link-test-secret"
	olAliceID    = 201
	olBobID      = 202
)

// olMemState is the in-memory oauthStateStore.
type olMemState struct {
	mu sync.Mutex
	m  map[string]string
}

func newOlMemState() *olMemState { return &olMemState{m: map[string]string{}} }

func (s *olMemState) SetState(_ context.Context, key, value string, _ time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
}

func (s *olMemState) GetState(_ context.Context, key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return v, ok
}

func (s *olMemState) DelState(_ context.Context, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
}

func (s *olMemState) peek(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[key]
}

// olUserBinding plays the JWT middleware's claim forwarding: the X-Test-User
// header names the session user for the authenticated endpoints (the
// cookie-based session verification is exercised by the callback's own link
// check, which reads the real session cookie).
var olUserBinding okapi.Middleware = func(c *okapi.Context) error {
	if u := c.Request().Header.Get("X-Test-User"); u != "" {
		c.Set("user_id", u)
	}
	return c.Next()
}

type olHarness struct {
	f     *mhFixture
	srv   *httptest.Server // the fake OAuth provider (token + userinfo)
	o     *okapi.TestServer
	state *olMemState

	// identity is what the fake userinfo endpoint serves for the next
	// handshake (the test rewrites it between runs).
	identityMu   sync.Mutex
	identitySub  string
	identityMail string
}

func (h *olHarness) setIdentity(sub, mail string) {
	h.identityMu.Lock()
	defer h.identityMu.Unlock()
	h.identitySub = sub
	h.identityMail = mail
}

func (h *olHarness) getIdentity() (string, string) {
	h.identityMu.Lock()
	defer h.identityMu.Unlock()
	return h.identitySub, h.identityMail
}

func newOLHarness(t *testing.T) *olHarness {
	t.Helper()
	f := newMHFixture(t)
	state := newOlMemState()

	// The fake OAuth provider: a token endpoint and a userinfo endpoint the
	// custom-OIDC explicit-URL configuration points at.
	hh := &olHarness{f: f, state: state}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-123","token_type":"bearer"}`))
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		sub, mail := hh.getIdentity()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"sub":%q,"email":%q,"name":"Linked User"}`, sub, mail)
	})
	srv := httptest.NewServer(mux)
	hh.srv = srv

	h := NewOAuthHandler(
		auth.NewOAuthService(
			repositories.NewOAuthProviderRepository(f.db),
			repositories.NewOAuthAccountRepository(f.db),
			repositories.NewUserRepository(f.db),
		),
		repositories.NewOAuthProviderRepository(f.db),
		repositories.NewOAuthAccountRepository(f.db),
		repositories.NewUserRepository(f.db),
		nil, olTestSecret, nil, nil, nil,
		"http://localhost:9000", "http://localhost:9000",
	)
	h.SetStateStore(state)

	o := okapi.NewTestServer(t)
	g := o.Group("/users/me", olUserBinding)
	g.Get("/oauth", h.ListLinkedAccounts)
	g.Post("/oauth/link", okapi.H(h.LinkProvider))
	g.Delete("/oauth/{provider_id:int}", okapi.H(h.UnlinkAccount))
	ga := o.Group("/admin/users", olUserBinding)
	ga.Get("/{id:int}/oauth", okapi.H(h.AdminListUserLinkedAccounts))
	o.Get("/auth/oauth/{provider}/callback", h.Callback)

	hh.o = o
	t.Cleanup(srv.Close)
	return hh
}

// olSeedRow writes one full row into a fake table (column order = mhColumns).
func olSeedRow(t *testing.T, h *olHarness, table string, vals map[string]any) {
	t.Helper()
	s := h.f.state
	s.mu.Lock()
	defer s.mu.Unlock()
	row := make([]driver.Value, len(mhColumns[table]))
	for i, col := range mhColumns[table] {
		if v, ok := vals[col]; ok {
			row[i] = v
		}
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func (h *olHarness) rowCell(table string, rowIndex int, col string) any {
	s := h.f.state
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.tables[table].rows
	if rowIndex >= len(rows) {
		return nil
	}
	for i, c := range mhColumns[table] {
		if c == col {
			return rows[rowIndex][i]
		}
	}
	return nil
}

func (h *olHarness) rowCount(table string) int {
	s := h.f.state
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tables[table].rows)
}

func (h *olHarness) do(t *testing.T, method, path, cookieToken, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if cookieToken != "" {
		// The session cookie (the callback's link mode reads exactly this).
		req.AddCookie(&http.Cookie{Name: "posta_session", Value: cookieToken})
	}
	if userID != "" {
		req.Header.Set("X-Test-User", userID)
	}
	rec := httptest.NewRecorder()
	h.o.ServeHTTP(rec, req)
	return rec
}

// olJWT mints a session JWT for one user (the same claims the login flow
// issues).
func olJWT(t *testing.T, userID uint) string {
	t.Helper()
	tok, err := okapi.GenerateJwtToken([]byte(olTestSecret), map[string]any{
		"sub":   userID,
		"email": fmt.Sprintf("u%d@example.test", userID),
		"role":  "user",
		"aud":   "posta",
		"jti":   fmt.Sprintf("ol-%d-%d", userID, time.Now().UnixNano()),
	}, time.Hour)
	if err != nil {
		t.Fatalf("mint jwt: %v", err)
	}
	return tok
}

// olStartLink runs LinkProvider as userID and returns the state parameter
// extracted from the answered authorize URL.
func (h *olHarness) olStartLink(t *testing.T, userID uint, slug string) string {
	t.Helper()
	rec := h.do(t, "POST", "/users/me/oauth/link", "", fmt.Sprint(userID),
		fmt.Sprintf(`{"provider": %q}`, slug))
	if rec.Code != http.StatusOK {
		t.Fatalf("link provider = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Data struct {
			AuthorizeURL string `json:"authorize_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode link provider: %v", err)
	}
	if !strings.Contains(got.Data.AuthorizeURL, "state=") {
		t.Fatalf("authorize URL %q carries no state", got.Data.AuthorizeURL)
	}
	idx := strings.Index(got.Data.AuthorizeURL, "state=")
	return got.Data.AuthorizeURL[idx+len("state="):]
}

func (h *olHarness) olRunLinkCallback(t *testing.T, cookieToken, state string) *httptest.ResponseRecorder {
	t.Helper()
	return h.do(t, "GET", fmt.Sprintf("/auth/oauth/custom/callback?code=c1&state=%s", state), cookieToken, "", "")
}

// TestOAuthLink_LinkLifecycleAndIsolation — the full link lifecycle through
// the real callback: link intent → provider handshake → binding, the
// already-linked no-op, the identity-in-use refusal (isolation), the
// session-required guard, and the session/state agreement.
func TestOAuthLink_LinkLifecycleAndIsolation(t *testing.T) {
	hh := newOLHarness(t)
	olSeedRow(t, hh, "users", map[string]any{
		"id": int64(olAliceID), "name": "alice", "email": "alice@example.test",
		"password_hash": "bcrypt-hashed", "role": "user", "active": true,
		"auth_method": "password", "created_at": time.Now().UTC(),
	})
	olSeedRow(t, hh, "users", map[string]any{
		"id": int64(olBobID), "name": "bob", "email": "bob@example.test",
		"password_hash": "bcrypt-hashed", "role": "user", "active": true,
		"auth_method": "password", "created_at": time.Now().UTC(),
	})
	olSeedRow(t, hh, "o_auth_providers", map[string]any{
		"id": int64(9), "name": "Provider custom", "slug": "custom", "type": "oidc",
		"client_id": "client-custom", "client_secret": "secret-custom",
		"auth_url":      hh.srv.URL + "/auth",
		"token_url":     hh.srv.URL + "/token",
		"user_info_url": hh.srv.URL + "/userinfo",
		"scopes":        "openid email profile", "enabled": true,
		"auto_register": true, "allowed_domains": "",
		"created_at": time.Now().UTC(), "updated_at": time.Now().UTC(),
	})
	hh.setIdentity("ext-100", "alice@example.test")

	// 1. Start the link flow as alice.
	state := hh.olStartLink(t, olAliceID, "custom")

	// The state must carry the link intent + the session user.
	raw := hh.state.peek("oauth:state:" + state)
	if raw == "" {
		t.Fatal("link state missing from the store")
	}
	var stateData map[string]string
	if err := json.Unmarshal([]byte(raw), &stateData); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if stateData["link"] != "1" || stateData["user_id"] != fmt.Sprint(olAliceID) {
		t.Fatalf("state = %+v, want link intent for user %d", stateData, olAliceID)
	}

	// 2. The callback with alice's session cookie binds the identity.
	rec := hh.olRunLinkCallback(t, olJWT(t, olAliceID), state)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Location"), "oauth=linked") {
		t.Fatalf("callback Location = %q, want oauth=linked", rec.Header().Get("Location"))
	}

	// 3. The linked identity shows in alice's list.
	rec = hh.do(t, "GET", "/users/me/oauth", "", fmt.Sprint(olAliceID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var list struct {
		Data []struct {
			ProviderName string `json:"provider_name"`
			Email        string `json:"email"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ProviderName != "Provider custom" || list.Data[0].Email != "alice@example.test" {
		t.Fatalf("list = %+v, want exactly the linked identity", list.Data)
	}

	// 4. Isolation: bob re-links the SAME provider identity → refused.
	hh.setIdentity("ext-100", "bob@example.test")
	state2 := hh.olStartLink(t, olBobID, "custom")
	rec = hh.olRunLinkCallback(t, olJWT(t, olBobID), state2)
	if rec.Code != http.StatusFound {
		t.Fatalf("bob callback = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "oauth_error=identity_in_use") {
		t.Fatalf("bob callback Location = %q, want oauth_error=identity_in_use", rec.Header().Get("Location"))
	}
	// Bob's list stays empty and the stored link still belongs to alice.
	rec = hh.do(t, "GET", "/users/me/oauth", "", fmt.Sprint(olBobID), "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode bob list: %v", err)
	}
	if len(list.Data) != 0 {
		t.Fatalf("bob's linked accounts = %+v, want empty (identity isolation)", list.Data)
	}
	owner := hh.rowCell("o_auth_accounts", 0, "user_id")
	if owner != int64(olAliceID) {
		t.Fatalf("the linked identity's owner = %v, want alice %d (isolation)", owner, olAliceID)
	}

	// 5. Re-linking alice's own identity: the already_linked no-op, no
	// duplicate row.
	hh.setIdentity("ext-100", "alice@example.test")
	state3 := hh.olStartLink(t, olAliceID, "custom")
	rec = hh.olRunLinkCallback(t, olJWT(t, olAliceID), state3)
	if !strings.Contains(rec.Header().Get("Location"), "oauth=already_linked") {
		t.Fatalf("re-link Location = %q, want oauth=already_linked", rec.Header().Get("Location"))
	}
	if rows := hh.rowCount("o_auth_accounts"); rows != 1 {
		t.Fatalf("linked rows = %d, want 1 (no duplicate)", rows)
	}

	// 6. Link without a session cookie → refused.
	state4 := hh.olStartLink(t, olAliceID, "custom")
	rec = hh.olRunLinkCallback(t, "", state4)
	if !strings.Contains(rec.Header().Get("Location"), "oauth_error=link_requires_session") {
		t.Fatalf("cookie-less callback Location = %q, want link_requires_session", rec.Header().Get("Location"))
	}

	// 7. Session/state disagreement (bob's cookie, alice's state) → refused.
	state5 := hh.olStartLink(t, olAliceID, "custom")
	rec = hh.olRunLinkCallback(t, olJWT(t, olBobID), state5)
	if !strings.Contains(rec.Header().Get("Location"), "oauth_error=link_requires_session") {
		t.Fatalf("mismatched-session callback Location = %q, want link_requires_session", rec.Header().Get("Location"))
	}
}

// TestOAuthLink_UnlinkLifecycle — the unlink half: the last-auth-method guard,
// the auth-method downgrade, and the cross-user isolation.
func TestOAuthLink_UnlinkLifecycle(t *testing.T) {
	hh := newOLHarness(t)
	now := time.Now().UTC()
	olSeedRow(t, hh, "users", map[string]any{
		"id": int64(olAliceID), "name": "alice", "email": "alice@example.test",
		"password_hash": "bcrypt-hashed", "role": "user", "active": true,
		"auth_method": "both", "created_at": now,
	})
	olSeedRow(t, hh, "users", map[string]any{
		"id": int64(olBobID), "name": "bob", "email": "bob@example.test",
		"password_hash": "bcrypt-hashed", "role": "user", "active": true,
		"auth_method": "oauth", "created_at": now,
	})
	olSeedRow(t, hh, "o_auth_providers", map[string]any{
		"id": int64(1), "name": "Provider prov-a", "slug": "prov-a", "type": "oidc",
		"enabled": true, "auto_register": true, "created_at": now, "updated_at": now,
	})
	olSeedRow(t, hh, "o_auth_providers", map[string]any{
		"id": int64(2), "name": "Provider prov-b", "slug": "prov-b", "type": "oidc",
		"enabled": true, "auto_register": true, "created_at": now, "updated_at": now,
	})
	olSeedRow(t, hh, "o_auth_accounts", map[string]any{
		"id": int64(900), "user_id": int64(olAliceID), "provider_id": int64(1),
		"provider_user_id": "ext-a", "email": "a@prov.test",
		"created_at": now, "updated_at": now,
	})
	olSeedRow(t, hh, "o_auth_accounts", map[string]any{
		"id": int64(901), "user_id": int64(olAliceID), "provider_id": int64(2),
		"provider_user_id": "ext-b", "email": "b@prov.test",
		"created_at": now, "updated_at": now,
	})
	olSeedRow(t, hh, "o_auth_accounts", map[string]any{
		"id": int64(902), "user_id": int64(olBobID), "provider_id": int64(3),
		"provider_user_id": "ext-bob", "email": "bob@prov.test",
		"created_at": now, "updated_at": now,
	})

	// Isolation: bob unlinks ALICE's provider id → reads as 404 like a
	// missing link (FindByUserAndProvider is user-scoped).
	rec := hh.do(t, "DELETE", "/users/me/oauth/1", "", fmt.Sprint(olBobID), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign unlink = %d, want 404", rec.Code)
	}

	// Bob is oauth-only with one link (provider 3): unlinking his last auth
	// method is refused.
	rec = hh.do(t, "DELETE", "/users/me/oauth/3", "", fmt.Sprint(olBobID), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oauth-only unlink = %d, want 400", rec.Code)
	}

	// Alice has a password ("both") + two links: unlinking one is fine.
	rec = hh.do(t, "DELETE", "/users/me/oauth/1", "", fmt.Sprint(olAliceID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unlink = %d: %s", rec.Code, rec.Body.String())
	}

	// Unlinking the LAST link is still fine for a password-carrying account,
	// and the auth method downgrades to "password".
	rec = hh.do(t, "DELETE", "/users/me/oauth/2", "", fmt.Sprint(olAliceID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("last unlink = %d: %s", rec.Code, rec.Body.String())
	}
	authMethod := hh.rowCell("users", 0, "auth_method")
	if authMethod != "password" {
		t.Fatalf("auth_method after the last unlink = %v, want password", authMethod)
	}

	// Alice's list is empty now.
	rec = hh.do(t, "GET", "/users/me/oauth", "", fmt.Sprint(olAliceID), "")
	var list struct {
		Data []struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Data) != 0 {
		t.Fatalf("alice's links = %+v, want empty", list.Data)
	}

	// A missing provider id reads as 404.
	rec = hh.do(t, "DELETE", "/users/me/oauth/999", "", fmt.Sprint(olAliceID), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing unlink = %d, want 404", rec.Code)
	}
}

// TestOAuthAdmin_LinkedIdentityVisibility — the admin surface reads one
// user's linked identities (the account email + the links, no secrets) and a
// missing user reads as 404.
func TestOAuthAdmin_LinkedIdentityVisibility(t *testing.T) {
	hh := newOLHarness(t)
	now := time.Now().UTC()
	olSeedRow(t, hh, "users", map[string]any{
		"id": int64(olAliceID), "name": "alice", "email": "alice@example.test",
		"password_hash": "bcrypt-hashed", "role": "user", "active": true,
		"auth_method": "both", "created_at": now,
	})
	olSeedRow(t, hh, "o_auth_providers", map[string]any{
		"id": int64(1), "name": "Provider prov-a", "slug": "prov-a", "type": "oidc",
		"enabled": true, "auto_register": true, "created_at": now, "updated_at": now,
	})
	olSeedRow(t, hh, "o_auth_providers", map[string]any{
		"id": int64(2), "name": "Provider prov-b", "slug": "prov-b", "type": "oidc",
		"enabled": true, "auto_register": true, "created_at": now, "updated_at": now,
	})
	olSeedRow(t, hh, "o_auth_accounts", map[string]any{
		"id": int64(900), "user_id": int64(olAliceID), "provider_id": int64(1),
		"provider_user_id": "ext-a", "email": "a@prov.test",
		"created_at": now, "updated_at": now,
	})
	olSeedRow(t, hh, "o_auth_accounts", map[string]any{
		"id": int64(901), "user_id": int64(olAliceID), "provider_id": int64(2),
		"provider_user_id": "ext-b", "email": "b@prov.test",
		"created_at": now, "updated_at": now,
	})

	rec := hh.do(t, "GET", fmt.Sprintf("/admin/users/%d/oauth", olAliceID), "", fmt.Sprint(olAliceID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin visibility = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Data struct {
			UserEmail string `json:"user_email"`
			Accounts  []struct {
				ID           uint   `json:"id"`
				ProviderName string `json:"provider_name"`
				Email        string `json:"email"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Data.UserEmail != "alice@example.test" {
		t.Fatalf("user_email = %q", got.Data.UserEmail)
	}
	if len(got.Data.Accounts) != 2 {
		t.Fatalf("accounts = %+v, want both linked identities", got.Data.Accounts)
	}

	// A missing user reads as 404.
	rec = hh.do(t, "GET", "/admin/users/999/oauth", "", fmt.Sprint(olAliceID), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing user = %d, want 404", rec.Code)
	}
}
