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

package security

import (
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
)

// Middleware enforces the security engine on every HTTP request: allowlist
// and ban snapshot checks, connection recording and suspicious-path
// detection. It must be wired after RequestID and before all route groups;
// enforcement therefore runs before authentication, so an unauthenticated
// prober is judged by the same signals as anyone else.
//
// Fail-open throughout: recording errors are ignored — the detector applies
// the configured fail-open policy itself and only ever reports errors back,
// never blocks. A nil engine (POSTA_SECURITY_ENABLED=false) yields a
// pass-through middleware.
//
// Keep-alive caveat (documented by contract): HTTP reuses one TCP connection
// for many requests, so new_connections counts requests rather than
// connections for HTTP sources. A browser dashboard that polls frequently
// contributes to the same window; loopback is exempt via the built-in
// allowlist, and the thresholds absorb ordinary interactive traffic.
func (e *Engine) Middleware() func(*okapi.Context) error {
	return func(c *okapi.Context) error {
		if e == nil || !e.Enabled() {
			return c.Next()
		}

		ip := ParseRemoteIP(c.RealIP())
		if !ip.IsValid() {
			// Unparseable peer: skip enforcement rather than guess.
			return c.Next()
		}

		if e.Bans != nil {
			if e.Bans.IsAllowed(ip) {
				return c.Next()
			}
			if blocked, _ := e.Bans.IsBlocked(ip); blocked {
				// Matched the middleware error style used elsewhere
				// (e.g. LoginRateLimitMiddleware): AbortForbidden routes
				// through okapi's configured error handler.
				return c.AbortForbidden("Your IP address has been blocked")
			}
		}

		if e.Detect == nil {
			return c.Next()
		}

		ctx := c.Request().Context()
		path := c.Request().URL.Path

		_ = e.Detect.RecordConnect(ctx, ip, requestPort(c), "http")

		// High-risk probe paths (/.env, /wp-login.php, …) are recorded
		// before routing so the instant-evaluate trigger fires even when a
		// route would answer them.
		if IsSuspiciousPath(path) {
			_ = e.Detect.RecordSuspiciousPath(ctx, ip, path)
		}

		err := c.Next()

		// Stale probing: a request that fell through the router to a 404 is
		// recorded as a suspicious path too (ordinary noise accumulates in
		// the window; only the high-risk globs evaluate instantly).
		if err == nil && c.Response().StatusCode() == http.StatusNotFound {
			_ = e.Detect.RecordSuspiciousPath(ctx, ip, path)
		}
		return err
	}
}

// requestPort extracts the numeric port of the request URL, 0 when absent or
// implicit (the common case: HTTP on 80/443 carries no port in the URL).
func requestPort(c *okapi.Context) int {
	p := c.Request().URL.Port()
	if p == "" {
		return 0
	}
	v, err := strconv.Atoi(p)
	if err != nil || v <= 0 || v > 65535 {
		return 0
	}
	return v
}
