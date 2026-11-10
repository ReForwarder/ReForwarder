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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/handlers"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/jkaninda/okapi"
)

// The OB-5 W1 webmail route set (ADR-005 decision 1) is exercised with the
// real mailRoutes() definitions and the real middlewares, handler stubbed —
// the same shape as mailstore_search_routes_test.go. It pins the gate (an
// API-key-less 401 for an unauthenticated call, a dashboard session passes
// through to the handler) and that each path is registered exactly once.

const mailRoutesTestSecret = "mail-routes-test-secret"

func mailRouteDefs(t *testing.T, cfg *config.Config, o *okapi.TestServer) []okapi.RouteDefinition {
	t.Helper()
	jwtAuth := middlewares.JWTAuth(cfg)
	r := &Router{
		app: o.Okapi,
		cfg: cfg,
		v1:  o.Group("/api/v1"),
		mw: routerMiddleware{
			jwtOnly:           jwtAuth.Middleware,
			optionalWorkspace: middlewares.OptionalWorkspaceMiddleware(nil, nil),
		},
		h: routerHandlers{
			mail: handlers.NewMailHandler(nil),
		},
	}
	return r.mailRoutes()
}

func TestMailRoutes_RegisteredOncePerMethodAndGated(t *testing.T) {
	o := okapi.NewTestServer(t)
	cfg := &config.Config{JWTSecret: mailRoutesTestSecret}
	defs := mailRouteDefs(t, cfg, o)

	// Every W1 endpoint is present exactly once: method+path uniqueness.
	// RouteDefinition.Path is group-relative (the /mail group prefixes it);
	// okapi resolves the full path at registration time.
	seen := make(map[string]int)
	for i := range defs {
		key := defs[i].Method + " " + defs[i].Path
		seen[key]++
	}
	want := []string{
		http.MethodGet + " /mailboxes",
		http.MethodGet + " /folders",
		http.MethodPost + " /folders",
		http.MethodPatch + " /folders/{id:int}",
		http.MethodDelete + " /folders/{id:int}",
		http.MethodPost + " /folders/{id:int}/expunge",
		http.MethodGet + " /messages",
		http.MethodGet + " /messages/{uid:int}",
		http.MethodGet + " /messages/{uid:int}/attachments/{index:int}",
		http.MethodPost + " /messages/{uid:int}/flags",
		http.MethodPost + " /messages/{uid:int}/move",
		http.MethodPost + " /messages/{uid:int}/copy",
		http.MethodDelete + " /messages/{uid:int}",
		http.MethodPost + " /search",
	}
	if len(defs) != len(want) {
		t.Fatalf("mailRoutes registered %d routes, want %d", len(defs), len(want))
	}
	for _, w := range want {
		if seen[w] != 1 {
			t.Fatalf("route %q registered %d times, want exactly 1", w, seen[w])
		}
	}
	o.Register(defs...)

	tok, err := okapi.GenerateJwtToken([]byte(cfg.JWTSecret), map[string]any{
		"sub": 42, "email": "u@example.com", "role": "user", "aud": "posta", "jti": "mail-routes-42",
	}, time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	// Unauthenticated → 401 before the handler.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/mailboxes", nil)
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rec.Code)
	}

	// Authenticated → reaches the handler; the nil store answers 503 (the
	// disabled-deployment posture, not a routing failure).
	req = httptest.NewRequest(http.MethodGet, "/api/v1/mail/mailboxes", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec = httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("authenticated (nil store) status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}

	// A path param route exists and resolves: expunge on a foreign folder is
	// gated the same way (401 unauthenticated, 503 authenticated + nil store).
	expunge := func(bearer string) int {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/mail/folders/%d/expunge", 7), nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		o.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := expunge(""); got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated expunge status = %d, want 401", got)
	}
	if got := expunge(tok); got != http.StatusServiceUnavailable {
		t.Fatalf("authenticated expunge (nil store) status = %d, want 503", got)
	}
}
