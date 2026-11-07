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

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/handlers"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/jkaninda/okapi"
)

// The mailstore search maintenance route (OB-3, ADR-003 decision 3) is
// exercised with the real adminRoutes() definitions and the real admin JWT
// middleware, with the handler stubbed — the same shape as
// security_routes_test.go. It pins the gate (unauthenticated 401, non-admin
// 403, admin 200) and the registered path.

// mailstoreRouteDefs builds the real admin route definitions on a bare
// Router with only the mailstore handler installed (the others stay nil and
// are never invoked; their conditional registrations are skipped).
func mailstoreRouteDefs(t *testing.T, cfg *config.Config, o *okapi.TestServer) []okapi.RouteDefinition {
	t.Helper()
	r := &Router{
		app: o.Okapi,
		cfg: cfg,
		v1:  o.Group("/api/v1"),
		mw:  routerMiddleware{jwtAdminAuth: middlewares.JWTAdminAuth(cfg)},
		h: routerHandlers{
			mailstoreSearch: handlers.NewMailstoreSearchAdminHandler(nil),
		},
	}
	return r.adminRoutes()
}

// TestMailstoreSearchRouteRequiresAdminAuth is the gating contract for POST
// /admin/mailstore/search/reindex: only an admin session reaches the handler.
func TestMailstoreSearchRouteRequiresAdminAuth(t *testing.T) {
	o := okapi.NewTestServer(t)
	cfg := &config.Config{JWTSecret: securityTestJWTSecret}
	defs := mailstoreRouteDefs(t, cfg, o)

	var registered int
	for i := range defs {
		if defs[i].Path == "/mailstore/search/reindex" && defs[i].Method == http.MethodPost {
			defs[i].Handler = stubHandler("POST /mailstore/search/reindex")
			registered++
		}
	}
	if registered != 1 {
		t.Fatalf("mailstore reindex route registered %d times, want exactly 1", registered)
	}
	o.Register(defs...)

	const path = "/api/v1/admin/mailstore/search/reindex"
	do := func(bearer string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		o.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := do(""); got != http.StatusUnauthorized {
		t.Errorf("unauthenticated: status = %d, want 401", got)
	}
	if got := do(signedToken(t, cfg, "user")); got != http.StatusForbidden {
		t.Errorf("non-admin: status = %d, want 403", got)
	}
	if got := do(signedToken(t, cfg, "admin")); got != http.StatusOK {
		t.Errorf("admin: status = %d, want 200", got)
	}
}
