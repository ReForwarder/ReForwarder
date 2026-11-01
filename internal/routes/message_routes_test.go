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
	"testing"

	"github.com/jkaninda/okapi"
)

// The global CORS preflight only answers for POSTA_CORS_ORIGINS. A customer
// site embedding a form is never on that list, so its preflight gets no
// Access-Control-Allow-Origin and ingest must stay reachable with simple
// requests (see the test below). Since okapi v1.0.0 an explicit OPTIONS route
// can be registered alongside CORS, so per-form preflight is possible if JSON
// submissions from customer origins are ever needed.
func TestGlobalPreflightIgnoresUnlistedOrigins(t *testing.T) {
	app := okapi.New()
	app.WithCORS(okapi.Cors{AllowedOrigins: []string{"https://dashboard.test"}})

	group := app.Group("/api/v1/f")
	app.Register(okapi.RouteDefinition{
		Method:  http.MethodPost,
		Path:    "/{key}",
		Handler: func(c *okapi.Context) error { return c.JSON(http.StatusAccepted, okapi.M{"ok": true}) },
		Group:   group,
	})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/f/abc", nil)
	req.Header.Set("Origin", "https://customer.test")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("preflight from an unlisted origin was allowed: %q", got)
	}
}

func TestFormIngestPostIsNotAPreflightedRequest(t *testing.T) {
	app := okapi.New()
	app.WithCORS(okapi.Cors{AllowedOrigins: []string{"https://dashboard.test"}})

	group := app.Group("/api/v1/f")
	app.Register(okapi.RouteDefinition{
		Method: http.MethodPost,
		Path:   "/{key}",
		Handler: func(c *okapi.Context) error {
			c.ResponseWriter().Header().Set("Access-Control-Allow-Origin", c.Request().Header.Get("Origin"))
			return c.JSON(http.StatusAccepted, okapi.M{"ok": true})
		},
		Group: group,
	})

	for _, contentType := range []string{
		"text/plain;charset=UTF-8",
		"application/x-www-form-urlencoded",
		"multipart/form-data; boundary=x",
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/f/abc", nil)
		req.Header.Set("Origin", "https://customer.test")
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("content type %q: status = %d, want 202", contentType, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://customer.test" {
			t.Fatalf("content type %q: allow-origin = %q", contentType, got)
		}
	}
}
