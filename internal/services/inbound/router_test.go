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
	"testing"

	"github.com/goposta/posta/internal/models"
)

// The routing engine's matching matrix (ADR-002 decision 1): first-match-wins
// by priority, the catch-all is the fallback for unmatched recipients, sub-
// addressed recipients route to their base route, and the unknown-recipient
// policy is explicit reject. The engine is exercised through a fake route
// store; the repository itself is covered by the route_action_test.go
// fake-driver tests.

// fakeRouteSource is an in-memory routeSource; rows are returned in the
// stored order so tests can prove the engine's ordering contract. Enabled
// filtering mirrors the real store's WHERE clause.
type fakeRouteSource struct {
	routes []models.InboundRoute
	err    error
}

func (f *fakeRouteSource) ListEnabledByDomain(uint) ([]models.InboundRoute, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []models.InboundRoute
	for _, r := range f.routes {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out, nil
}

func route(id uint, pattern string, action models.InboundRouteAction, priority int, enabled bool) models.InboundRoute {
	return models.InboundRoute{ID: id, DomainID: 1, Pattern: pattern, Action: action, Priority: priority, Enabled: enabled}
}

func verifiedDomain() *models.Domain {
	ws := uint(3)
	return &models.Domain{ID: 1, UserID: 7, WorkspaceID: &ws, Domain: "routed.test", OwnershipVerified: true}
}

func TestRouterExactMatchFirstMatchWinsByPriority(t *testing.T) {
	// Two exact rules, the lower-priority-value one must win; an unrelated
	// exact rule is skipped. Source order is intentionally not priority
	// order — the engine owns the ordering contract.
	src := &fakeRouteSource{routes: []models.InboundRoute{
		route(9, "other@routed.test", models.InboundRouteActionDeliver, 1, true),
		route(4, "team@routed.test", models.InboundRouteActionForward, 50, true),
		route(2, "team@routed.test", models.InboundRouteActionDeliver, 10, true),
	}}
	rt := NewRouter(src)

	got, kind, err := rt.ResolveRecipient(verifiedDomain(), "Team@Routed.test")
	if err != nil {
		t.Fatalf("ResolveRecipient: %v", err)
	}
	if kind != RouteMatchExact {
		t.Fatalf("kind = %q, want %q", kind, RouteMatchExact)
	}
	if got == nil || got.ID != 2 {
		t.Fatalf("matched route %+v, want id 2 (priority 10 over 50)", got)
	}
}

func TestRouterExactBeatsHigherPriorityCatchAll(t *testing.T) {
	// The catch-all is the FALLBACK for unmatched recipients — a low priority
	// value (high precedence) on it must not shadow an exact rule.
	src := &fakeRouteSource{routes: []models.InboundRoute{
		route(1, "@routed.test", models.InboundRouteActionDeliver, 1, true),
		route(5, "team@routed.test", models.InboundRouteActionForward, 100, true),
	}}
	rt := NewRouter(src)

	got, kind, err := rt.ResolveRecipient(verifiedDomain(), "team@routed.test")
	if err != nil {
		t.Fatalf("ResolveRecipient: %v", err)
	}
	if kind != RouteMatchExact || got == nil || got.ID != 5 {
		t.Fatalf("exact rule must win over a higher-priority catch-all, got %+v (%s)", got, kind)
	}

	// Unmatched recipients fall back to the catch-all.
	got, kind, err = rt.ResolveRecipient(verifiedDomain(), "stray@routed.test")
	if err != nil {
		t.Fatalf("catch-all fallback: %v", err)
	}
	if kind != RouteMatchCatchAll || got == nil || got.ID != 1 {
		t.Fatalf("catch-all fallback got %+v (%s), want route 1", got, kind)
	}
}

func TestRouterCatchAllFallbackWhenNoRuleMatches(t *testing.T) {
	src := &fakeRouteSource{routes: []models.InboundRoute{
		route(3, "team@routed.test", models.InboundRouteActionDeliver, 10, true),
	}}
	rt := NewRouter(src)

	if _, _, err := rt.ResolveRecipient(verifiedDomain(), "nobody@routed.test"); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("no catch-all and no match: err = %v, want ErrNoRoute", err)
	}

	onlyCatchAll := &fakeRouteSource{routes: []models.InboundRoute{
		route(8, "@routed.test", models.InboundRouteActionWebhook, 100, true),
	}}
	got, kind, err := NewRouter(onlyCatchAll).ResolveRecipient(verifiedDomain(), "anyone@routed.test")
	if err != nil || kind != RouteMatchCatchAll || got == nil || got.ID != 8 {
		t.Fatalf("catch-all-only domain: got %+v (%s), err %v", got, kind, err)
	}
}

func TestRouterSubAddressRoutesToBaseRoute(t *testing.T) {
	// user+tag@host routes to the base route (ADR-002 §1): no pattern matches
	// the tagged address, so the base address's exact rule applies.
	src := &fakeRouteSource{routes: []models.InboundRoute{
		route(6, "user@routed.test", models.InboundRouteActionForward, 10, true),
	}}
	rt := NewRouter(src)

	got, kind, err := rt.ResolveRecipient(verifiedDomain(), "user+newsletter@routed.test")
	if err != nil {
		t.Fatalf("sub-address base routing: %v", err)
	}
	if kind != RouteMatchSubAddress || got == nil || got.ID != 6 {
		t.Fatalf("sub-address must route to base route 6, got %+v (%s)", got, kind)
	}

	// A sub-address SPEC pattern (the tagged address itself) is an exact
	// match and wins over the base route.
	withSpec := &fakeRouteSource{routes: []models.InboundRoute{
		route(6, "user@routed.test", models.InboundRouteActionForward, 1, true),
		route(7, "user+newsletter@routed.test", models.InboundRouteActionQuarantine, 10, true),
	}}
	got, kind, err = NewRouter(withSpec).ResolveRecipient(verifiedDomain(), "user+newsletter@routed.test")
	if err != nil {
		t.Fatalf("sub-address spec: %v", err)
	}
	if kind != RouteMatchExact || got == nil || got.ID != 7 {
		t.Fatalf("tagged exact pattern must win, got %+v (%s)", got, kind)
	}

	// A different base user's tag does not inherit another rule.
	if _, _, err := rt.ResolveRecipient(verifiedDomain(), "stranger+newsletter@routed.test"); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("other user's sub-address: err = %v, want ErrNoRoute", err)
	}
}

func TestRouterUnknownRecipientExplicitReject(t *testing.T) {
	// The explicit unknown-recipient policy: no enabled rule, no catch-all
	// → ErrNoRoute (the SMTP surface answers 550).
	rt := NewRouter(&fakeRouteSource{})
	if _, _, err := rt.ResolveRecipient(verifiedDomain(), "ghost@routed.test"); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("unknown recipient: err = %v, want ErrNoRoute", err)
	}
}

func TestRouterRejectsRecipientOutsideVerifiedDomain(t *testing.T) {
	// Routing rules resolve ONLY through the verified domain handed in — a
	// recipient whose host differs is never matched, even when a pattern
	// textually covers the other host.
	src := &fakeRouteSource{routes: []models.InboundRoute{
		route(1, "@other.test", models.InboundRouteActionDeliver, 1, true),
		route(2, "user@routed.test", models.InboundRouteActionDeliver, 2, true),
	}}
	rt := NewRouter(src)

	if _, _, err := rt.ResolveRecipient(verifiedDomain(), "user@other.test"); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("cross-domain recipient: err = %v, want ErrNoRoute", err)
	}
	if _, _, err := rt.ResolveRecipient(verifiedDomain(), "user@attacker-routed.test"); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("lookalike host: err = %v, want ErrNoRoute", err)
	}
}

func TestRouterIgnoresDisabledRoutes(t *testing.T) {
	src := &fakeRouteSource{routes: []models.InboundRoute{
		route(1, "team@routed.test", models.InboundRouteActionForward, 1, false), // disabled
		route(2, "@routed.test", models.InboundRouteActionQuarantine, 2, true),
	}}
	rt := NewRouter(src)

	got, kind, err := rt.ResolveRecipient(verifiedDomain(), "team@routed.test")
	if err != nil {
		t.Fatalf("disabled rule must be skipped: %v", err)
	}
	if got == nil || got.ID != 2 || kind != RouteMatchCatchAll {
		t.Fatalf("disabled exact rule fell through to the wrong route: %+v (%s)", got, kind)
	}
}

func TestRouterSourceFailureIsNotAPolicyReject(t *testing.T) {
	// A route-store failure must surface as its own error (callers answer
	// 451), never as ErrNoRoute (which maps to a permanent 550).
	boom := errors.New("route store unavailable")
	rt := NewRouter(&fakeRouteSource{err: boom})
	_, _, err := rt.ResolveRecipient(verifiedDomain(), "team@routed.test")
	if !errors.Is(err, boom) {
		t.Fatalf("source failure err = %v, want the store error", err)
	}
	if errors.Is(err, ErrNoRoute) {
		t.Fatal("source failure must not be classified as ErrNoRoute")
	}
}

func TestRouterNilRouterFailsClosed(t *testing.T) {
	var rt *Router
	if _, _, err := rt.ResolveRecipient(verifiedDomain(), "team@routed.test"); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("nil router: err = %v, want ErrNoRoute (fail closed)", err)
	}
}
