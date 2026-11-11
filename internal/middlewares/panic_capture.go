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
	"github.com/goposta/posta/internal/observability"
	"github.com/jkaninda/okapi"
)

// CapturePanicsMiddleware (GAP-51) reports a panic escaping any later
// middleware or handler to the error-monitoring hook, then RE-PANICS so the
// process behavior is exactly what it was before this middleware existed:
// net/http recovers at the connection level, logs "http: panic serving", and
// closes the connection. The middleware only adds the observability side
// effect; it must never swallow or transform the panic.
//
// It is registered first in the chain (routes.go) and degrades to a pass-
// through when no hook is installed (hook unset or POSTA_ERROR_WEBHOOK_URL
// empty), so dev environments without the URL pay one nil-check per request.
func CapturePanicsMiddleware() okapi.Middleware {
	return func(c *okapi.Context) error {
		defer func() {
			if r := recover(); r != nil {
				if hook := observability.GlobalErrorHook(); hook != nil && hook.Active() {
					hook.CapturePanic(r)
				}
				panic(r)
			}
		}()
		return c.Next()
	}
}
