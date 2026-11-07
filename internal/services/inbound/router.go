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

package inbound

import (
	"errors"
	"sort"
	"strings"

	"github.com/goposta/posta/internal/models"
)

// Recipient routing (OB-2, ADR-002 decision 1). Routing rules are data in
// inbound_routes; this engine evaluates them for one recipient of one
// tenant-verified domain. Binding constraints:
//
//   - Rules resolve ONLY through a verified domain — the caller hands over
//     the *models.Domain it has already verified (RCPT-time ownership check);
//     the engine never re-derives a domain from client input.
//   - First-match-wins by priority (lower priority value = evaluated first;
//     ties break by id). Exact and sub-address patterns always beat the
//     catch-all: the catch-all is the fallback for recipients that matched
//     no specific rule, and the explicit unknown-recipient policy is
//     default-reject (no enabled catch-all and no matching rule → ErrNoRoute,
//     which the SMTP surface answers with 550).
//   - Sub-address support (user+tag@host): when no pattern matches the full
//     address, the base address (user@host, tag stripped at the first '+')
//     is tried, so user+tag@host routes to the base route.

// ErrNoRoute reports that no enabled routing rule (and no enabled catch-all)
// matched the recipient on a verified domain. The SMTP surface maps it to a
// 550 "recipient not accepted"; the ingest paths persist the rejection.
var ErrNoRoute = errors.New("no inbound route matches recipient")

// RouteMatchKind tells the caller WHY a route matched — exact address,
// sub-address base, or the domain's catch-all.
type RouteMatchKind string

const (
	// RouteMatchExact: the full recipient address equals an exact pattern
	// (including the sub-address spec form "user+tag@host").
	RouteMatchExact RouteMatchKind = "exact"
	// RouteMatchSubAddress: the recipient's sub-address base (user+tag@host
	// → user@host) matched an exact pattern.
	RouteMatchSubAddress RouteMatchKind = "sub_address"
	// RouteMatchCatchAll: the domain's "@domain" pattern matched as fallback.
	RouteMatchCatchAll RouteMatchKind = "catch_all"
)

// routeSource is the engine's view of the inbound_routes store. Satisfied by
// *repositories.InboundRouteRepository; faked in tests.
type routeSource interface {
	ListEnabledByDomain(domainID uint) ([]models.InboundRoute, error)
}

// Router evaluates inbound routing rules for a verified domain.
type Router struct {
	source routeSource
}

// NewRouter builds a Router over a route store.
func NewRouter(source routeSource) *Router {
	return &Router{source: source}
}

// ResolveRecipient returns the route that handles address on the verified
// domain d, and how it matched. Errors:
//   - ErrNoRoute: the domain is verified but no enabled rule and no enabled
//     catch-all matches — the explicit default-reject policy;
//   - any other error: the route store is temporarily unavailable (callers
//     must NOT answer 550 for infra failures — a 451 temporary failure is
//     the honest SMTP answer).
func (r *Router) ResolveRecipient(domain *models.Domain, address string) (*models.InboundRoute, RouteMatchKind, error) {
	if r == nil || r.source == nil || domain == nil {
		return nil, "", ErrNoRoute
	}
	addr := strings.ToLower(strings.TrimSpace(address))
	local, host, ok := splitAddress(addr)
	if !ok {
		return nil, "", ErrNoRoute
	}
	// The recipient's host must be the verified domain the caller resolved —
	// never a client-supplied authority.
	if !strings.EqualFold(host, domain.Domain) {
		return nil, "", ErrNoRoute
	}

	routes, err := r.source.ListEnabledByDomain(domain.ID)
	if err != nil {
		return nil, "", err
	}
	// Defensive ordering (the repo already sorts, but the engine owns the
	// first-match-wins contract, not the storage): stable sort by priority
	// then id so evaluation order is deterministic.
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Priority != routes[j].Priority {
			return routes[i].Priority < routes[j].Priority
		}
		return routes[i].ID < routes[j].ID
	})

	// Pass 1 — exact match on the full address (priority order).
	for i := range routes {
		if matchesRoutePattern(routes[i].Pattern, addr) {
			return &routes[i], RouteMatchExact, nil
		}
	}
	// Pass 2 — sub-address base: user+tag@host falls back to the base
	// address's route (ADR-002 §1 sub-address support).
	if base, hasTag := subAddressBase(local); hasTag {
		baseAddr := base + "@" + strings.ToLower(host)
		for i := range routes {
			if matchesRoutePattern(routes[i].Pattern, baseAddr) {
				return &routes[i], RouteMatchSubAddress, nil
			}
		}
	}
	// Pass 3 — catch-all fallback for unmatched recipients.
	for i := range routes {
		if isCatchAllPattern(routes[i].Pattern, host) {
			return &routes[i], RouteMatchCatchAll, nil
		}
	}
	return nil, "", ErrNoRoute
}

// matchesRoutePattern reports whether an exact-form pattern equals addr.
func matchesRoutePattern(pattern, addr string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" || strings.HasPrefix(pattern, "@") {
		return false
	}
	return pattern == addr
}

// isCatchAllPattern reports whether pattern is the "@host" catch-all spec.
func isCatchAllPattern(pattern, host string) bool {
	return strings.EqualFold(strings.TrimSpace(pattern), "@"+host)
}

// splitAddress splits addr at the last '@' into local part and host.
func splitAddress(addr string) (local, host string, ok bool) {
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return "", "", false
	}
	return addr[:at], addr[at+1:], true
}

// subAddressBase strips the sub-address tag from local at the first '+'
// (RFC 5233 convention). Reports false when the local part carries no tag.
func subAddressBase(local string) (string, bool) {
	plus := strings.Index(local, "+")
	if plus <= 0 {
		return local, false
	}
	return local[:plus], true
}
