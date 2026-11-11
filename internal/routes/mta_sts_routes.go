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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"github.com/jkaninda/okapi"
)

// mtaSTSRoutes registers the MTA-STS policy hosting path (SF-29 policy-host
// half, OB-6 W4; ADR-007 §5, RFC 8461): a fixed well-known path served at
// the app root, authenticated by the mta-sts.<domain> Host header rather
// than an API group. Gated by POSTA_MTA_STS_ENABLED (ADR-009 §1: listeners
// and paths default off; the reverse proxy fronts the mta-sts.<domain>
// vhost with TLS when the operator opts in).
func (r *Router) mtaSTSRoutes() []okapi.RouteDefinition {
	return []okapi.RouteDefinition{
		{
			Method:      "GET",
			Path:        "/.well-known/mta-sts.txt",
			Handler:     r.h.mtaStsPolicy.Get,
			Summary:     "MTA-STS policy document",
			Description: "Serves the MTA-STS policy of the domain named by the mta-sts.<domain> request host (RFC 8461). Policy text, not an API envelope; unknown or opted-out domains answer 404 (the 'no policy' posture senders already handle).",
			Options: []okapi.RouteOption{
				okapi.DocHide(),
			},
		},
	}
}
