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

package webauthn

import (
	"io"
	"net/http"
)

// requestFrom wraps a ceremony body in the minimal *http.Request the library's
// Finish* entry points parse. The finish handlers run inside okapi, whose
// request binding does not fit the byte-exact WebAuthn credential payloads, so
// the handler hands the raw (or extracted assertion) body through here.
func requestFrom(body io.Reader) *http.Request {
	r, err := http.NewRequest(http.MethodPost, "/", body)
	if err != nil {
		// NewRequest only fails on a malformed method/URL; the constants here
		// are fixed and valid, so this cannot happen in practice.
		return nil
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}
