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
	"net/netip"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// mustAddr parses an IP literal, failing the test on a typo.
func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return a
}

// banRow builds a SecurityBan model for the snapshot tests; a nil expires
// means a permanent ban. CIDR shape is derived from the address string.
func banRow(id uint, ip string, isCIDR bool, expires *time.Time) models.SecurityBan {
	return models.SecurityBan{
		ID:                id,
		IPAddress:         ip,
		IsCIDR:            isCIDR,
		ReasonCode:        string(ReasonPortScan),
		ReasonDescription: "test ban",
		BanType:           BanTypeAutomatic,
		OffenseCount:      1,
		CreatedBy:         "system",
		CreatedAt:         time.Unix(1_690_000_000, 0).UTC(),
		UpdatedAt:         time.Unix(1_690_000_000, 0).UTC(),
		ExpiresAt:         expires,
	}
}

// newTestBanList returns a BanList with the fake clock installed, so lazy
// expiry checks are fully deterministic.
func newTestBanList(t *testing.T, allowLoopback bool, clock *fakeClock) *BanList {
	t.Helper()
	l := NewBanList(allowLoopback)
	l.now = clock.Now
	return l
}

func TestBanListBlocksExactIPUntilExpiry(t *testing.T) {
	clock := newFakeClock()
	l := newTestBanList(t, false, clock)

	expires := clock.Now().Add(time.Hour)
	l.Replace([]models.SecurityBan{banRow(1, "9.9.9.9", false, &expires)}, nil)

	blocked, ban := l.Blocked(mustAddr(t, "9.9.9.9"))
	if !blocked || ban == nil || ban.ID != 1 {
		t.Fatalf("active ban not matched: blocked=%v ban=%+v", blocked, ban)
	}

	// Lazy expiry: exactly at the boundary the ban is no longer in force
	// (active means strictly before the expiry instant).
	clock.Advance(time.Hour)
	if blocked, _ := l.Blocked(mustAddr(t, "9.9.9.9")); blocked {
		t.Fatal("ban still blocks at its exact expiry instant")
	}
	clock.Advance(time.Minute)
	if blocked, _ := l.Blocked(mustAddr(t, "9.9.9.9")); blocked {
		t.Fatal("expired ban still blocks")
	}

	// An unrelated address was never covered.
	if blocked, _ := l.Blocked(mustAddr(t, "8.8.8.8")); blocked {
		t.Fatal("unrelated IP reported as blocked")
	}
}

func TestBanListCIDRBlockAndMostSpecificMatch(t *testing.T) {
	clock := newFakeClock()
	l := newTestBanList(t, false, clock)

	l.Replace([]models.SecurityBan{
		banRow(1, "10.0.0.0/8", true, nil),
		banRow(2, "10.1.0.0/16", true, nil),
	}, nil)

	if ban, want := l.Blocked(mustAddr(t, "10.1.2.3")); !ban || (*want).ID != 2 {
		t.Fatalf("10.1.2.3: blocked=%v ban=%+v, want the most specific /16 ban (id 2)", ban, want)
	}
	if ban, got := l.Blocked(mustAddr(t, "10.2.3.4")); !ban || (*got).ID != 1 {
		t.Fatalf("10.2.3.4: blocked=%v ban=%+v, want the /8 ban (id 1)", ban, got)
	}
	if blocked, _ := l.Blocked(mustAddr(t, "11.1.1.1")); blocked {
		t.Fatal("11.1.1.1 is outside every banned prefix")
	}

	// An exact-address ban is checked first and beats any covering CIDR.
	l.UpsertBan(banRow(3, "10.1.2.3", false, nil))
	if ban, got := l.Blocked(mustAddr(t, "10.1.2.3")); !ban || (*got).ID != 3 {
		t.Fatalf("10.1.2.3: blocked=%v ban=%+v, want the exact-address ban (id 3)", ban, got)
	}
}

func TestBanListCanonicalizesV4MappedV6(t *testing.T) {
	clock := newFakeClock()

	// A plain-IPv4 ban must also catch the v4-mapped v6 spelling of the same
	// address (and vice versa): both collapse to one canonical key.
	l := newTestBanList(t, false, clock)
	l.Replace([]models.SecurityBan{banRow(1, "1.2.3.4", false, nil)}, nil)
	if blocked, _ := l.Blocked(mustAddr(t, "::ffff:1.2.3.4")); !blocked {
		t.Fatal("::ffff:1.2.3.4 must hit the 1.2.3.4 ban")
	}

	l2 := newTestBanList(t, false, clock)
	l2.Replace([]models.SecurityBan{banRow(1, "::ffff:5.6.7.8", false, nil)}, nil)
	if blocked, _ := l2.Blocked(mustAddr(t, "5.6.7.8")); !blocked {
		t.Fatal("5.6.7.8 must hit the ::ffff:5.6.7.8 ban")
	}
	if blocked, _ := l2.Blocked(mustAddr(t, "::ffff:5.6.7.8")); !blocked {
		t.Fatal("::ffff:5.6.7.8 must hit its own ban")
	}

	// Allowlist entries canonicalize the same way, including CIDR prefixes
	// written over a v4-mapped v6 address.
	l3 := newTestBanList(t, false, clock)
	l3.Replace(nil, []models.SecurityAllowlist{
		{ID: 1, IPCidr: "::ffff:9.9.9.9"},
		{ID: 2, IPCidr: "::ffff:10.0.0.0/120"},
	})
	if !l3.Allowed(mustAddr(t, "9.9.9.9")) {
		t.Fatal("9.9.9.9 must be allowed via the v4-mapped entry")
	}
	if !l3.Allowed(mustAddr(t, "10.0.0.5")) {
		t.Fatal("10.0.0.5 must be allowed via the v4-mapped /120 (= 10.0.0.0/24)")
	}
	if l3.Allowed(mustAddr(t, "10.0.1.5")) {
		t.Fatal("10.0.1.5 is outside the v4-mapped /120")
	}
}

// TestBanListAllowlistBeatsBlocklist pins the lookup half of the
// allowlist-wins rule: an allowlisted address reports Allowed even while a ban
// for it exists, and callers (BanService, Detector, enforcement) consult
// Allowed first.
func TestBanListAllowlistBeatsBlocklist(t *testing.T) {
	clock := newFakeClock()
	l := newTestBanList(t, false, clock)

	l.Replace([]models.SecurityBan{banRow(1, "2.2.2.2", false, nil)},
		[]models.SecurityAllowlist{{ID: 1, IPCidr: "2.2.2.2"}})

	if !l.Allowed(mustAddr(t, "2.2.2.2")) {
		t.Fatal("allowlisted IP must report Allowed even though a ban row exists")
	}
	// Blocked still reports the row — the win happens because every caller
	// checks Allowed before Blocked.
	if blocked, _ := l.Blocked(mustAddr(t, "2.2.2.2")); !blocked {
		t.Fatal("the ban row must still be visible via Blocked")
	}

	// A CIDR allowlist covers addresses inside the block.
	l.Replace([]models.SecurityBan{banRow(2, "10.1.2.3", false, nil)},
		[]models.SecurityAllowlist{{ID: 2, IPCidr: "10.0.0.0/8"}})
	if !l.Allowed(mustAddr(t, "10.1.2.3")) {
		t.Fatal("10.1.2.3 must be allowed by the 10.0.0.0/8 allowlist entry")
	}
	if !l.Allowed(mustAddr(t, "10.200.0.1")) {
		t.Fatal("10.200.0.1 must be allowed by the 10.0.0.0/8 allowlist entry")
	}
	if l.Allowed(mustAddr(t, "192.168.1.1")) {
		t.Fatal("192.168.1.1 is outside the allowlist")
	}
}

func TestBanListLoopbackAlwaysAllowed(t *testing.T) {
	clock := newFakeClock()
	l := newTestBanList(t, true, clock)

	l.Replace([]models.SecurityBan{
		banRow(1, "127.0.0.1", false, nil),
		banRow(2, "::1", false, nil),
	}, nil)

	if !l.Allowed(mustAddr(t, "127.0.0.1")) || !l.Allowed(mustAddr(t, "::1")) {
		t.Fatal("loopback must always be allowed when allowLoopback is set")
	}
	// The exemption survives a wholesale Replace that drops every entry.
	l.Replace(nil, nil)
	if !l.Allowed(mustAddr(t, "127.0.0.1")) || !l.Allowed(mustAddr(t, "::1")) {
		t.Fatal("loopback exemption must survive Replace")
	}
	if !l.Allowed(mustAddr(t, "127.8.9.10")) {
		t.Fatal("the whole 127.0.0.0/8 loopback range must be allowed")
	}

	// Without the flag, loopback is blockable like any other address.
	strict := newTestBanList(t, false, clock)
	strict.Replace([]models.SecurityBan{banRow(3, "127.0.0.1", false, nil)}, nil)
	if blocked, _ := strict.Blocked(mustAddr(t, "127.0.0.1")); !blocked {
		t.Fatal("loopback must be blockable when allowLoopback is false")
	}
}

func TestBanListUpsertBanKeepsLongerExpiry(t *testing.T) {
	clock := newFakeClock()
	l := newTestBanList(t, false, clock)

	oneHour := clock.Now().Add(time.Hour)
	l.Replace([]models.SecurityBan{banRow(1, "4.4.4.4", false, &oneHour)}, nil)

	// A later expiry takes over the slot.
	twoHours := clock.Now().Add(2 * time.Hour)
	l.UpsertBan(banRow(2, "4.4.4.4", false, &twoHours))
	if _, got := l.Blocked(mustAddr(t, "4.4.4.4")); got.ID != 2 {
		t.Fatalf("slot holder = ban %d, want 2 (longer expiry wins)", got.ID)
	}

	// A shorter expiry does not displace the current holder.
	halfHour := clock.Now().Add(30 * time.Minute)
	l.UpsertBan(banRow(3, "4.4.4.4", false, &halfHour))
	if _, got := l.Blocked(mustAddr(t, "4.4.4.4")); got.ID != 2 {
		t.Fatalf("slot holder = ban %d, want 2 (shorter ban must not displace)", got.ID)
	}

	// Permanent beats timed.
	l.UpsertBan(banRow(4, "4.4.4.4", false, nil))
	if _, got := l.Blocked(mustAddr(t, "4.4.4.4")); got.ID != 4 {
		t.Fatalf("slot holder = ban %d, want 4 (permanent wins)", got.ID)
	}

	// An expired candidate never displaces an active holder.
	alreadyGone := clock.Now().Add(-time.Hour)
	l.UpsertBan(banRow(5, "4.4.4.4", false, &alreadyGone))
	if _, got := l.Blocked(mustAddr(t, "4.4.4.4")); got.ID != 4 {
		t.Fatalf("slot holder = ban %d, want 4 (expired candidate must not win)", got.ID)
	}
}

func TestBanListRemoveBanFallsBackToRemainingBans(t *testing.T) {
	clock := newFakeClock()
	l := newTestBanList(t, false, clock)

	oneHour := clock.Now().Add(time.Hour)
	twoHours := clock.Now().Add(2 * time.Hour)
	l.Replace([]models.SecurityBan{
		banRow(1, "4.4.4.4", false, &oneHour),
		banRow(2, "4.4.4.4", false, &twoHours),
	}, nil)

	if _, got := l.Blocked(mustAddr(t, "4.4.4.4")); got.ID != 2 {
		t.Fatalf("slot holder = ban %d, want 2 before removal", got.ID)
	}

	l.RemoveBan(2)
	if _, got := l.Blocked(mustAddr(t, "4.4.4.4")); got.ID != 1 {
		t.Fatalf("after removing ban 2, slot holder = ban %d, want the remaining ban 1", got.ID)
	}

	l.RemoveBan(1)
	if blocked, _ := l.Blocked(mustAddr(t, "4.4.4.4")); blocked {
		t.Fatal("IP must be unblocked after every ban for it was removed")
	}

	// Removing an unknown ID is a no-op, not a panic.
	l.RemoveBan(42)
	if blocked, _ := l.Blocked(mustAddr(t, "4.4.4.4")); blocked {
		t.Fatal("no-op RemoveBan must not resurrect a ban")
	}
}

func TestBanListReplaceSwapsState(t *testing.T) {
	clock := newFakeClock()
	l := newTestBanList(t, false, clock)

	l.Replace([]models.SecurityBan{banRow(1, "8.8.8.8", false, nil)}, nil)
	if blocked, _ := l.Blocked(mustAddr(t, "8.8.8.8")); !blocked {
		t.Fatal("ban must block after Replace")
	}

	// Replace resets: prior state does not leak into the new snapshot.
	l.Replace(nil, nil)
	if blocked, _ := l.Blocked(mustAddr(t, "8.8.8.8")); blocked {
		t.Fatal("Replace must reset the ban state")
	}

	// Already-expired rows are skipped by the lazy expiry check.
	stale := clock.Now().Add(-time.Second)
	l.Replace([]models.SecurityBan{banRow(2, "8.8.8.8", false, &stale)}, nil)
	if blocked, _ := l.Blocked(mustAddr(t, "8.8.8.8")); blocked {
		t.Fatal("an already-expired ban must not block after Replace")
	}

	// Rows with an unparseable address are ignored rather than matching all.
	l.Replace([]models.SecurityBan{banRow(3, "not-an-ip", false, nil)}, nil)
	if blocked, _ := l.Blocked(mustAddr(t, "8.8.8.8")); blocked {
		t.Fatal("unparseable ban rows must be ignored")
	}

	// A CIDR-shaped value in an address-flagged row still applies (and the
	// reverse shape works too, via the same tolerant parse).
	l.Replace([]models.SecurityBan{banRow(4, "10.0.0.0/8", false, nil)}, nil)
	if blocked, _ := l.Blocked(mustAddr(t, "10.9.9.9")); !blocked {
		t.Fatal("address-flagged row holding a prefix must still block")
	}

	// Unmap canonicalization also normalizes stored CIDR rows over
	// v4-mapped v6 networks.
	l.Replace([]models.SecurityBan{banRow(5, "::ffff:10.0.0.0/120", true, nil)}, nil)
	if blocked, _ := l.Blocked(mustAddr(t, "10.0.0.9")); !blocked {
		t.Fatal("v4-mapped v6 CIDR ban must canonicalize to the v4 prefix")
	}
}

func TestBanListEmptyAndInvalidLookups(t *testing.T) {
	clock := newFakeClock()
	l := newTestBanList(t, true, clock)

	// Invalid addresses report nothing rather than panicking.
	if blocked, ban := l.Blocked(netip.Addr{}); blocked || ban != nil {
		t.Fatal("zero Addr must not match any ban")
	}
	if l.Allowed(netip.Addr{}) {
		t.Fatal("zero Addr must not be allowed")
	}

	// Fresh list blocks nothing but keeps the loopback exemption.
	if blocked, _ := l.Blocked(mustAddr(t, "1.1.1.1")); blocked {
		t.Fatal("fresh list must not block")
	}
	if !l.Allowed(mustAddr(t, "::1")) {
		t.Fatal("loopback must be allowed on a fresh list")
	}
}
