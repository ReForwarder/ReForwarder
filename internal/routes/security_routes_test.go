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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/jkaninda/okapi"
)

// The security admin routes are exercised with the real route definitions and
// the real admin JWT middleware, but with every handler replaced by a stub
// that answers with the route's registered path: the handlers themselves need
// a live database, while these tests pin routing, route ordering and the
// auth gate (workspace_rbac_test.go builds its bare Router the same way).

const securityTestJWTSecret = "security-routes-test-secret"

// securityRouteDefs builds the real security route definitions on a bare
// Router: only v1 and the admin middleware are needed to produce them; the
// handler set stays zero because the handlers are never invoked here.
func securityRouteDefs(t *testing.T, cfg *config.Config, o *okapi.TestServer) (sse []okapi.RouteDefinition, plain []okapi.RouteDefinition) {
	t.Helper()
	r := &Router{
		app: o.Okapi,
		cfg: cfg,
		v1:  o.Group("/api/v1"),
		mw:  routerMiddleware{jwtAdminAuth: middlewares.JWTAdminAuth(cfg)},
	}
	return r.securitySSERoutes(), r.securityRoutes()
}

// newSecurityRouteServer registers the security routes the way registerRoutes
// does — SSE stream first, then the plain routes — with stub handlers.
func newSecurityRouteServer(t *testing.T) (*okapi.TestServer, string, string) {
	t.Helper()

	o := okapi.NewTestServer(t)
	cfg := &config.Config{JWTSecret: securityTestJWTSecret}
	sse, plain := securityRouteDefs(t, cfg, o)

	for i := range sse {
		sse[i].Handler = stubHandler(sse[i].Method + " " + sse[i].Path)
	}
	for i := range plain {
		plain[i].Handler = stubHandler(plain[i].Method + " " + plain[i].Path)
	}

	o.Register(sse...)
	o.Register(plain...)
	return o, signedToken(t, cfg, "admin"), signedToken(t, cfg, "user")
}

func stubHandler(answer string) okapi.HandlerFunc {
	return func(c *okapi.Context) error {
		return c.String(http.StatusOK, answer)
	}
}

// signedToken mints a JWT the way the login handler does: HS256 over the app
// secret, with the posta audience and the user's role in the claims.
func signedToken(t *testing.T, cfg *config.Config, role string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "1",
		"jti":   "security-route-test-jti",
		"role":  role,
		"email": role + "@example.com",
		"aud":   "posta",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		t.Fatalf("sign test JWT: %v", err)
	}
	return signed
}

func doSecurityRequest(o *okapi.TestServer, method, path, bearer string, body string) int {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec.Code
}

// securityRouteTable lists every security endpoint by its dispatched path.
var securityRouteTable = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/api/v1/admin/security/overview"},
	{http.MethodGet, "/api/v1/admin/security/bans"},
	{http.MethodPost, "/api/v1/admin/security/bans"},
	{http.MethodDelete, "/api/v1/admin/security/bans/7"},
	{http.MethodPost, "/api/v1/admin/security/bans/7/extend"},
	{http.MethodGet, "/api/v1/admin/security/events"},
	{http.MethodGet, "/api/v1/admin/security/events/stream"},
	{http.MethodGet, "/api/v1/admin/security/rules"},
	{http.MethodPut, "/api/v1/admin/security/rules"},
	{http.MethodGet, "/api/v1/admin/security/allowlist"},
	{http.MethodPost, "/api/v1/admin/security/allowlist"},
	{http.MethodDelete, "/api/v1/admin/security/allowlist/3"},
}

// TestSecurityRoutesRequireAdminAuth is the gating contract: unauthenticated
// callers get 401, an authenticated non-admin gets 403, and only an admin
// session reaches the handler.
func TestSecurityRoutesRequireAdminAuth(t *testing.T) {
	o, adminToken, userToken := newSecurityRouteServer(t)

	for _, rt := range securityRouteTable {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			if got := doSecurityRequest(o, rt.method, rt.path, "", "{}"); got != http.StatusUnauthorized {
				t.Errorf("unauthenticated: status = %d, want 401", got)
			}
			if got := doSecurityRequest(o, rt.method, rt.path, userToken, "{}"); got != http.StatusForbidden {
				t.Errorf("non-admin: status = %d, want 403", got)
			}
			if got := doSecurityRequest(o, rt.method, rt.path, adminToken, "{}"); got != http.StatusOK {
				t.Errorf("admin: status = %d, want 200", got)
			}
		})
	}
}

// TestSecurityEventsStreamNotShadowed pins the registration order the route
// table relies on: the SSE stream is registered before the plain security
// routes, so /admin/security/events/stream can never be swallowed by a later
// param route and stays a distinct route from GET /admin/security/events.
func TestSecurityEventsStreamNotShadowed(t *testing.T) {
	o, adminToken, _ := newSecurityRouteServer(t)

	stream := "GET /security/events/stream"
	events := "GET /security/events"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/security/events/stream", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stream: status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != stream {
		t.Fatalf("stream: handler = %q, want %q (SSE route shadowed or missing)", got, stream)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/security/events", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	o.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("events: status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != events {
		t.Fatalf("events: handler = %q, want %q", got, events)
	}
}

// TestSecurityRouteSurfaceMatchesContract pins the full route surface — one
// SSE route plus the eleven plain routes, all inside the /api/v1/admin group
// guarded by the admin middleware.
func TestSecurityRouteSurfaceMatchesContract(t *testing.T) {
	o := okapi.NewTestServer(t)
	cfg := &config.Config{JWTSecret: securityTestJWTSecret}
	sse, plain := securityRouteDefs(t, cfg, o)

	if len(sse) != 1 {
		t.Fatalf("security SSE surface = %d routes, want 1", len(sse))
	}
	if len(plain) != 11 {
		t.Fatalf("security surface = %d routes, want 11", len(plain))
	}

	want := map[string]bool{
		"GET /security/overview":              false,
		"GET /security/bans":                  false,
		"POST /security/bans":                 false,
		"DELETE /security/bans/{id:int}":      false,
		"POST /security/bans/{id:int}/extend": false,
		"GET /security/events":                false,
		"GET /security/events/stream":         false,
		"GET /security/rules":                 false,
		"PUT /security/rules":                 false,
		"GET /security/allowlist":             false,
		"POST /security/allowlist":            false,
		"DELETE /security/allowlist/{id:int}": false,
	}
	for _, defs := range [][]okapi.RouteDefinition{sse, plain} {
		for _, d := range defs {
			key := d.Method + " " + d.Path
			seen, known := want[key]
			if !known {
				t.Errorf("unexpected security route %q", key)
				continue
			}
			if seen {
				t.Errorf("duplicate security route %q", key)
			}
			want[key] = true
			if d.Group == nil || d.Group.Prefix != "/api/v1/admin" {
				t.Errorf("route %q is not inside the /api/v1/admin group (group %v)", key, d.Group)
			}
		}
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("missing security route %q", key)
		}
	}
}
