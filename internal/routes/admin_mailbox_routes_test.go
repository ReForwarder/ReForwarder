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

// The OB-5 mailbox-administration routes (ADR-005 decision 3) are exercised
// with the real adminRoutes() definitions and the real admin JWT middleware,
// with the handler stubbed — the same shape as
// mailstore_search_routes_test.go. This pins the exact paths and methods
// registered and the gate (unauthenticated 401, non-admin 403, admin passes
// the middleware).

func adminMailboxRouteDefs(t *testing.T, cfg *config.Config, o *okapi.TestServer) []okapi.RouteDefinition {
	t.Helper()
	r := &Router{
		app: o.Okapi,
		cfg: cfg,
		v1:  o.Group("/api/v1"),
		mw:  routerMiddleware{jwtAdminAuth: middlewares.JWTAdminAuth(cfg)},
		h: routerHandlers{
			adminMailbox: handlers.NewAdminMailboxHandler(nil, nil),
		},
	}
	return r.adminRoutes()
}

func TestAdminMailboxRoutesRequireAdminAuth(t *testing.T) {
	o := okapi.NewTestServer(t)
	cfg := &config.Config{JWTSecret: securityTestJWTSecret}
	defs := adminMailboxRouteDefs(t, cfg, o)

	want := []struct{ method, path string }{
		{http.MethodGet, "/mailboxes"},
		{http.MethodPost, "/mailboxes"},
		{http.MethodPatch, "/mailboxes/{id:int}"},
		{http.MethodDelete, "/mailboxes/{id:int}"},
		{http.MethodGet, "/mailboxes/{id:int}/usage"},
	}
	wantKeys := make(map[string]struct{}, len(want))
	for _, w := range want {
		wantKeys[w.method+" "+w.path] = struct{}{}
	}
	seen := make(map[string]int) // "METHOD PATH" → count
	for i := range defs {
		key := defs[i].Method + " " + defs[i].Path
		if _, isMailbox := wantKeys[key]; isMailbox {
			defs[i].Handler = stubHandler(key)
			seen[key]++
		}
	}
	for key := range wantKeys {
		if seen[key] != 1 {
			t.Errorf("mailbox admin route %q registered %d times, want exactly 1", key, seen[key])
		}
	}
	if len(seen) != 5 {
		t.Fatalf("registered %d distinct mailbox routes, want 5", len(seen))
	}
	o.Register(defs...)

	const base = "/api/v1/admin"
	cases := []struct{ method, path string }{
		{http.MethodGet, base + "/mailboxes"},
		{http.MethodPost, base + "/mailboxes"},
		{http.MethodPatch, base + "/mailboxes/1"},
		{http.MethodDelete, base + "/mailboxes/1"},
		{http.MethodGet, base + "/mailboxes/1/usage"},
	}
	for _, tc := range cases {
		do := func(bearer string) int {
			var body *strings.Reader
			if tc.method == http.MethodGet || tc.method == http.MethodDelete {
				body = strings.NewReader("")
			} else {
				body = strings.NewReader("{}")
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			rec := httptest.NewRecorder()
			o.ServeHTTP(rec, req)
			return rec.Code
		}
		if got := do(""); got != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated: status = %d, want 401", tc.method, tc.path, got)
		}
		if got := do(signedToken(t, cfg, "user")); got != http.StatusForbidden {
			t.Errorf("%s %s non-admin: status = %d, want 403", tc.method, tc.path, got)
		}
		if got := do(signedToken(t, cfg, "admin")); got == http.StatusUnauthorized || got == http.StatusForbidden {
			t.Errorf("%s %s admin: status = %d, gate rejected an admin token", tc.method, tc.path, got)
		}
	}
}
