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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middlewares

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/config"
	"github.com/jkaninda/okapi"
)

// R-28 tests: the `?token=` query credential is scoped to the SSE stream
// endpoints. R-15 tests: the per-key rate limit decision (429 on a exceeded
// cap, fail-open on limiter infrastructure errors, untouched keys never
// consult the limiter). R-18: the send-path classifier.

// ---------- R-28: query-token scoping ----------

func TestQueryTokenAllowed(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api/v1/message-stream", true},
		{"/api/v1/workspaces/current/inbound-stream", true},
		{"/api/v1/admin/events/stream", true},
		{"/api/v1/admin/metrics/stream", true},
		{"/api/v1/admin/security/events/stream", true},
		{"/api/v1/workspaces/current/emails", false},
		{"/api/v1/emails/send", false},
		{"/api/v1/workspaces/current/api-keys", false},
		{"/api/v1/admin/users", false},
		{"/streaming", false}, // suffix must be the whole last segment
		{"/api/v1/stream", true},
	}
	for _, tc := range cases {
		if got := queryTokenAllowed(tc.path); got != tc.want {
			t.Errorf("queryTokenAllowed(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// A psk_ credential in `?token=` must NOT authenticate a plain JSON route:
// it must never even reach the API-key path (with nil collaborators that path
// panics, so a panic here would fail the test via the test runner's crash —
// instead we assert the clean 401 fallthrough to JWT handling).
func TestAuthenticate_QueryTokenRefusedOnPlainRoute(t *testing.T) {
	cfg := &config.Config{JWTSecret: testSecret}
	o := okapi.NewTestServer(t)
	o.Get("/protected", func(c *okapi.Context) error {
		return c.String(http.StatusOK, c.GetString(CtxAuthMethod))
	}).Use(Authenticate(JWTAuth(cfg), nil, nil, nil))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.URL.RawQuery = "token=psk_deadbeef"
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
}

// On an SSE path the same query credential DOES dispatch to the API-key path —
// proven the same way authenticate_test.go proves dispatch: nil collaborators
// panic when the API-key branch runs.
func TestAuthenticate_QueryTokenDispatchesOnSSEPath(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("psk_ query credential on an SSE path did not reach the API-key path")
		}
	}()

	cfg := &config.Config{JWTSecret: testSecret}
	mw := Authenticate(JWTAuth(cfg), nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/message-stream", nil)
	req.URL.RawQuery = "token=psk_deadbeef"

	o := okapi.NewTestServer(t)
	c := okapi.NewContext(o.Okapi, httptest.NewRecorder(), req)
	_ = mw(c)
}

// The Authorization header path is untouched by the scoping: keys still
// authenticate plain JSON routes from the header.
func TestAuthenticate_HeaderTokenStillDispatchesOnPlainRoute(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("psk_ header credential did not reach the API-key path")
		}
	}()

	cfg := &config.Config{JWTSecret: testSecret}
	mw := Authenticate(JWTAuth(cfg), nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer psk_deadbeef")

	o := okapi.NewTestServer(t)
	c := okapi.NewContext(o.Okapi, httptest.NewRecorder(), req)
	_ = mw(c)
}

// ---------- R-15: per-key rate limit decision ----------

type stubLimiter struct {
	err     error
	subject string
	hourly  int
	daily   int
	calls   int
}

func (s *stubLimiter) AllowWithLimits(_ context.Context, subject string, hourlyLimit, dailyLimit int) error {
	s.calls++
	s.subject = subject
	s.hourly = hourlyLimit
	s.daily = dailyLimit
	return s.err
}

func TestAPIKeyRateLimitCheck(t *testing.T) {
	t.Run("under the cap allows", func(t *testing.T) {
		lim := &stubLimiter{}
		limited, err := apiKeyRateLimitCheck(lim, context.Background(), 42, 100, 1000)
		if err != nil || limited {
			t.Fatalf("limited=%v err=%v, want allowed", limited, err)
		}
		if lim.calls != 1 {
			t.Fatalf("limiter calls = %d, want 1", lim.calls)
		}
		if lim.subject != "apikey:42" {
			t.Fatalf("subject = %q, want apikey:42", lim.subject)
		}
		if lim.hourly != 100 || lim.daily != 1000 {
			t.Fatalf("limits passed = (%d, %d), want (100, 1000)", lim.hourly, lim.daily)
		}
	})

	t.Run("exceeded cap answers 429-eligible", func(t *testing.T) {
		for _, msg := range []string{"hourly rate limit exceeded (11/10)", "daily rate limit exceeded (501/500)"} {
			lim := &stubLimiter{err: errors.New(msg)}
			limited, err := apiKeyRateLimitCheck(lim, context.Background(), 42, 10, 500)
			if err != nil || !limited {
				t.Fatalf("msg %q: limited=%v err=%v, want limited", msg, limited, err)
			}
		}
	})

	// Infrastructure failures fail OPEN: a Redis outage degrades enforcement
	// instead of locking out every limited key (same choice as AllowLogin).
	t.Run("infrastructure error fails open", func(t *testing.T) {
		lim := &stubLimiter{err: errors.New("rate limit check failed: connection refused")}
		limited, err := apiKeyRateLimitCheck(lim, context.Background(), 42, 10, 500)
		if limited {
			t.Fatal("infrastructure error reported limited=true; must fail open")
		}
		if err == nil {
			t.Fatal("infrastructure error swallowed; caller should be able to log it")
		}
	})
}

// ---------- R-18: send-path classifier ----------

func TestIsAPIKeySendPath(t *testing.T) {
	sends := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/emails/send"},
		{http.MethodPost, "/api/v1/emails/send-template"},
		{http.MethodPost, "/api/v1/emails/batch"},
	}
	for _, s := range sends {
		if !isAPIKeySendPath(s.method, s.path) {
			t.Errorf("isAPIKeySendPath(%s %s) = false, want true", s.method, s.path)
		}
	}
	notSends := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/emails/send"},
		{http.MethodPost, "/api/v1/emails/verify"},
		{http.MethodPost, "/api/v1/emails/preview"},
		{http.MethodPost, "/api/v1/workspaces/current/api-keys"},
		{http.MethodGet, "/api/v1/emails"},
		{http.MethodPost, "/api/v1/emails"},
	}
	for _, s := range notSends {
		if isAPIKeySendPath(s.method, s.path) {
			t.Errorf("isAPIKeySendPath(%s %s) = true, want false", s.method, s.path)
		}
	}
}

// The rate-limit subject must be stable per key so a key's counters cannot be
// crossed with another's.
func TestAPIKeyRateLimitSubjectDistinctPerKey(t *testing.T) {
	a := apiKeyRateLimitSubject(1)
	b := apiKeyRateLimitSubject(2)
	if a == b || strings.TrimSpace(a) == "" {
		t.Fatalf("subjects not distinct/empty: %q vs %q", a, b)
	}
}
