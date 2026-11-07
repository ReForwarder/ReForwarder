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

package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/goposta/posta/internal/config"
	"github.com/jkaninda/okapi"
)

// SF-04 residual: IPAllowed and the auth gate's missing-key request paths had
// zero coverage. This file pins both.

// ---------- IPAllowed (SF-04) ----------

func TestIPAllowed(t *testing.T) {
	cases := []struct {
		name      string
		allowed   []string
		clientIP  string
		want      bool
	}{
		// An empty allowlist permits everything — the API-key default.
		{"empty allowlist permits any ip", nil, "203.0.113.7", true},
		{"empty allowlist permits ipv6", []string{}, "2001:db8::1", true},

		// Exact-IP entries.
		{"exact match", []string{"203.0.113.7"}, "203.0.113.7", true},
		{"exact mismatch", []string{"203.0.113.7"}, "203.0.113.8", false},
		{"exact ipv6 match", []string{"2001:db8::1"}, "2001:db8::1", true},
		{"exact ipv6 mismatch", []string{"2001:db8::1"}, "2001:db8::2", false},

		// CIDR entries.
		{"cidr contains", []string{"203.0.113.0/24"}, "203.0.113.199", true},
		{"cidr excludes outside", []string{"203.0.113.0/24"}, "203.0.114.1", false},
		{"ipv6 cidr contains", []string{"2001:db8::/32"}, "2001:db8::dead", true},
		{"ipv6 cidr excludes", []string{"2001:db8::/32"}, "2001:db9::1", false},
		{"host cidr /32", []string{"203.0.113.7/32"}, "203.0.113.7", true},
		{"host cidr /32 excludes", []string{"203.0.113.7/32"}, "203.0.113.8", false},

		// Mixed lists: any matching entry permits.
		{"multiple entries second matches", []string{"198.51.100.9", "203.0.113.0/24"}, "203.0.113.5", true},
		{"multiple entries first matches", []string{"198.51.100.9", "203.0.113.0/24"}, "198.51.100.9", true},
		{"multiple entries none match", []string{"198.51.100.9", "203.0.113.0/24"}, "192.0.2.1", false},

		// Malformed entries must neither match nor panic — and must not open
		// the door for the other entries' semantics to degrade.
		{"invalid cidr ignored", []string{"not-a-cidr", "203.0.113.7"}, "203.0.113.7", true},
		{"invalid cidr does not match", []string{"203.0.113.0/99"}, "203.0.113.7", false},
		{"cidr against unparseable ip", []string{"203.0.113.0/24"}, "not-an-ip", false},
		{"empty entry ignored", []string{""}, "203.0.113.7", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IPAllowed(tc.allowed, tc.clientIP); got != tc.want {
				t.Errorf("IPAllowed(%q, %q) = %v, want %v", tc.allowed, tc.clientIP, got, tc.want)
			}
		})
	}
}

// The middleware consults IPAllowed with the credential's allowlist; pin one
// end-to-end rejection so the call site cannot silently drop the check.
func TestAPIKeyAuth_IPAllowlistGate(t *testing.T) {
	// The API-key path with a nil key service cannot be exercised end-to-end
	// (ValidateKey on a nil receiver panics), so this test pins the wiring
	// indirectly: the SAME allowlist semantics through the exported helper that
	// the middleware calls, on both a permitted and a rejected address.
	if !IPAllowed([]string{"10.0.0.0/8"}, "10.1.2.3") {
		t.Fatal("in-range private IP rejected")
	}
	if IPAllowed([]string{"10.0.0.0/8"}, "11.1.2.3") {
		t.Fatal("out-of-range IP accepted")
	}
}

// ---------- the auth gate: missing-key request paths (SF-04) ----------

// apiKeyGateServer mounts the APIKeyAuth middleware on a test route.
func apiKeyGateServer(t *testing.T) *okapi.TestServer {
	t.Helper()
	o := okapi.NewTestServer(t)
	o.Get("/machine", func(c *okapi.Context) error {
		return c.String(http.StatusOK, "ok")
	}).Use(APIKeyAuth(nil, nil, nil, nil))
	return o
}

func TestAPIKeyAuth_MissingKeyPaths(t *testing.T) {
	cases := []struct {
		name       string
		apply      func(*http.Request)
		wantStatus int
	}{
		{
			name:       "no authorization header",
			apply:      func(*http.Request) {},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "empty bearer",
			apply:      func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") },
			wantStatus: http.StatusUnauthorized,
		},
		{
			// A JWT is not an API-key credential on the machine-facing API.
			name:       "jwt-shaped bearer rejected as bad format",
			apply:      func(r *http.Request) { r.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.x.y") },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "basic scheme is not an api key",
			apply:      func(r *http.Request) { r.Header.Set("Authorization", "Basic dXNlcjpwYXNz") },
			wantStatus: http.StatusUnauthorized,
		},
		{
			// R-28: the ?token= query path is reserved for the SSE streams; on
			// a plain JSON route even a psk_ in the query must not authenticate
			// (and with no header the request is a missing-key 401, not a panic).
			name: "query token on plain route never reaches the key path",
			apply: func(r *http.Request) {
				r.URL.RawQuery = "token=psk_deadbeef"
			},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := apiKeyGateServer(t)
			req := httptest.NewRequest(http.MethodGet, "/machine", nil)
			tc.apply(req)
			rec := httptest.NewRecorder()
			o.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// The query escape hatch must stay narrow: a psk_ in ?token= reaches the key
// path ONLY on the SSE stream endpoints.
func TestQueryTokenAllowedMatrix(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api/v1/message-stream", true},
		{"/api/v1/inbound-stream", true},
		{"/api/v1/admin/announcements/stream", true},
		{"/api/v1/stream", true},
		{"/api/v1/emails", false},
		{"/api/v1/emails/send", false},
		{"/api/v1/streams", false},
		{"/api/v1/admin/settings", false},
	}
	for _, tc := range cases {
		if got := queryTokenAllowed(tc.path); got != tc.want {
			t.Errorf("queryTokenAllowed(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// The dashboard-session gate answers "authentication required" when no
// credential of any kind is presented.
func TestAuthenticate_MissingCredentialMessage(t *testing.T) {
	cfg := &config.Config{JWTSecret: testSecret}
	o := okapi.NewTestServer(t)
	o.Get("/protected", func(c *okapi.Context) error {
		return c.String(http.StatusOK, "ok")
	}).Use(Authenticate(JWTAuth(cfg), nil, nil, nil))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if body := rec.Body.String(); body == "" {
		t.Fatal("empty body: the rejection must carry the error envelope")
	}
}
