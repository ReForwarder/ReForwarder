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

package session

import (
	"net/http"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
)

// CookieName holds the user JWT for browser clients. It is HttpOnly (so XSS
// cannot read it) and SameSite=Strict (so it is never sent cross-site, which
// blocks CSRF). Non-browser clients (CLI, SDKs, n8n) keep using the
// Authorization header, so this cookie is additive, not a breaking change.
const CookieName = "posta_session"

// SetCookie writes the session cookie carrying token, expiring after ttl.
//
// okapi's Context.SetCookie cannot express SameSite, so the cookie is written
// directly. The JWT alphabet (base64url plus '.') needs no escaping.
func SetCookie(c *okapi.Context, token string, ttl time.Duration) {
	http.SetCookie(c.Response(), &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		Expires:  time.Now().Add(ttl),
		Secure:   isHTTPS(c),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearCookie expires the session cookie. The attributes other than MaxAge must
// match SetCookie's or the browser will keep the original cookie.
func ClearCookie(c *okapi.Context) {
	http.SetCookie(c.Response(), &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		Secure:   isHTTPS(c),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// isHTTPS reports whether the client reached us over TLS, either directly or
// through a terminating proxy. A Secure cookie is dropped by the browser on a
// plain-HTTP origin, so this must stay false for local http:// development.
func isHTTPS(c *okapi.Context) bool {
	r := c.Request()
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(c.Header("X-Forwarded-Proto"), "https")
}
