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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/jkaninda/logger"
)

// BanList is the thread-safe, in-memory view of the current ban and allowlist
// state. It is the only state consulted on the hot path (counting listeners,
// HTTP middleware, SMTP session hooks), so lookups never touch Redis or the
// database.
//
// Concurrency model: copy-on-write. Readers (Blocked, Allowed) load an
// immutable snapshot through an atomic pointer — lock-free, no contention on
// the per-connection path. Writers (Replace, UpsertBan, RemoveBan) serialize
// on a mutex, clone the current snapshot, mutate the clone, and publish it
// with a single atomic store. Writes are rare (ban placements, admin
// actions, resyncs), so the clone cost is irrelevant next to the lock-free
// reads it buys.
//
// Addresses are canonicalized on the way in and on every lookup: an address
// written as a v4-mapped v6 literal (::ffff:10.0.0.1) is unmapped to its
// plain v4 form, so both spellings are the same key. CIDR prefixes are
// canonicalized and masked the same way before they are stored.
type BanList struct {
	// allowLoopback keeps loopback traffic (127.0.0.0/8, ::1) allowed
	// regardless of the dynamic allowlist — including across Replace calls,
	// which swap the dynamic entries wholesale.
	allowLoopback bool

	writeMu sync.Mutex
	current atomic.Pointer[banSnapshot]

	// now backs the lazy expiry checks. It is a field only so tests in this
	// package can install a fake clock; production always uses time.Now.
	now func() time.Time
}

// NewBanList returns an empty ban list. When allowLoopback is true, loopback
// addresses (127.0.0.0/8 and ::1) are always allowed: the flag is checked on
// every lookup, so the exemption survives Replace.
func NewBanList(allowLoopback bool) *BanList {
	l := &BanList{allowLoopback: allowLoopback, now: time.Now}
	snap := newBanSnapshot()
	if allowLoopback {
		snap.allowNets = append(snap.allowNets,
			canonicalPrefix(netip.MustParsePrefix("127.0.0.0/8")),
			canonicalPrefix(netip.MustParsePrefix("::1/128")),
		)
	}
	l.current.Store(snap)
	return l
}

// banSnapshot is an immutable view of the ban state. Pointers into it are
// shared with callers of Blocked; the returned bans must not be mutated.
type banSnapshot struct {
	// slots maps each ban's lookup slot to the ban that currently holds it.
	// When several rows cover the same slot (possible with CIDR overlaps),
	// the one blocking longest wins — permanent beats timed, later expiry
	// beats earlier.
	slots map[banSlotKey]*models.SecurityBan
	byID  map[uint]*models.SecurityBan

	allowIPs  map[netip.Addr]struct{}
	allowNets []netip.Prefix
}

// banSlotKey identifies where a ban lives in the lookup tables: the
// canonical address for single-IP bans, the canonical masked prefix for CIDR
// bans. It is comparable so it can key a map.
type banSlotKey struct {
	cidr   bool
	addr   netip.Addr   // valid when cidr is false
	prefix netip.Prefix // valid when cidr is true
}

func newBanSnapshot() *banSnapshot {
	return &banSnapshot{
		slots:    map[banSlotKey]*models.SecurityBan{},
		byID:     map[uint]*models.SecurityBan{},
		allowIPs: map[netip.Addr]struct{}{},
	}
}

func (s *banSnapshot) clone() *banSnapshot {
	c := newBanSnapshot()
	for k, v := range s.slots {
		c.slots[k] = v
	}
	for k, v := range s.byID {
		c.byID[k] = v
	}
	for k, v := range s.allowIPs {
		c.allowIPs[k] = v
	}
	c.allowNets = append(c.allowNets, s.allowNets...)
	return c
}

// canonicalIP normalizes an address so a v4-mapped v6 spelling
// (::ffff:10.0.0.1) and the plain v4 form (10.0.0.1) are the same key.
func canonicalIP(ip netip.Addr) netip.Addr {
	if !ip.IsValid() {
		return ip
	}
	return ip.Unmap()
}

// canonicalPrefix canonicalizes a CIDR the way canonicalIP canonicalizes
// addresses: a prefix written over a v4-mapped v6 address (e.g.
// ::ffff:10.0.0.0/120) becomes the equivalent plain v4 prefix
// (10.0.0.0/24). The result is masked (host bits cleared).
//
// A prefix shorter than 96 bits over a 4-in-6 address also covers non-mapped
// IPv6 space and has no v4 equivalent; it is kept as written and simply never
// matches, because every address looked up has been unmapped first.
func canonicalPrefix(p netip.Prefix) netip.Prefix {
	addr := p.Addr()
	unmapped := addr.Unmap()
	bits := p.Bits()
	if addr.Is4In6() && unmapped.Is4() && bits >= 96 {
		return netip.PrefixFrom(unmapped, bits-96).Masked()
	}
	return netip.PrefixFrom(unmapped, bits).Masked()
}

// parseBanIP turns a stored ban row into its lookup slot, canonicalizing the
// address or prefix. Rows that cannot be parsed (in either shape) yield
// ok=false and are ignored rather than silently matching nothing.
func parseBanIP(b models.SecurityBan) (slot banSlotKey, ok bool) {
	if b.IsCIDR {
		if p, err := netip.ParsePrefix(b.IPAddress); err == nil {
			return banSlotKey{cidr: true, prefix: canonicalPrefix(p)}, true
		}
		// Flag says CIDR but the value is a bare address: treat the /32 or
		// /128 as a prefix so the ban still applies.
		if a, err := netip.ParseAddr(b.IPAddress); err == nil {
			return banSlotKey{cidr: true, prefix: canonicalPrefix(netip.PrefixFrom(a, a.BitLen()))}, true
		}
		return banSlotKey{}, false
	}
	if a, err := netip.ParseAddr(b.IPAddress); err == nil {
		return banSlotKey{addr: canonicalIP(a)}, true
	}
	// Flag says address but the value carries a prefix length.
	if p, err := netip.ParsePrefix(b.IPAddress); err == nil {
		return banSlotKey{cidr: true, prefix: canonicalPrefix(p)}, true
	}
	return banSlotKey{}, false
}

// banActive reports whether a ban is currently in force: permanent rows
// (nil expiry) always are, timed rows until their expiry has passed.
func banActive(b *models.SecurityBan, now time.Time) bool {
	return b.ExpiresAt == nil || now.Before(*b.ExpiresAt)
}

// holdsLonger reports whether candidate should take over the slot currently
// held by incumbent: a ban that blocks longer wins — permanent beats timed,
// a later expiry beats an earlier one, an active ban beats an expired one.
// Ties keep the incumbent. A nil candidate never wins; a nil incumbent
// always loses.
func holdsLonger(incumbent, candidate *models.SecurityBan, now time.Time) bool {
	if candidate == nil {
		return false
	}
	if incumbent == nil {
		return true
	}
	incActive, candActive := banActive(incumbent, now), banActive(candidate, now)
	switch {
	case incActive && !candActive:
		return false
	case !incActive && candActive:
		return true
	case !incActive && !candActive:
		return false
	}
	if incumbent.ExpiresAt == nil {
		return false // permanent incumbent stays
	}
	if candidate.ExpiresAt == nil {
		return true // permanent candidate wins
	}
	return candidate.ExpiresAt.After(*incumbent.ExpiresAt)
}

func (s *banSnapshot) addBan(b models.SecurityBan, now time.Time) {
	slot, ok := parseBanIP(b)
	if !ok {
		logger.Warn("security: ignoring ban with unparseable address", "ban_id", b.ID, "ip", b.IPAddress)
		return
	}
	stored := b
	s.byID[b.ID] = &stored
	if holdsLonger(s.slots[slot], &stored, now) {
		s.slots[slot] = &stored
	}
}

// reclaimSlot recomputes the winner of the slot a removed ban held: the
// longest-blocking remaining ban with the same key takes over, or the slot
// is dropped.
func (s *banSnapshot) reclaimSlot(removed *models.SecurityBan, now time.Time) {
	slot, ok := parseBanIP(*removed)
	if !ok {
		return
	}
	held, ok := s.slots[slot]
	if !ok || held.ID != removed.ID {
		return // another ban holds the slot; nothing to reclaim
	}
	var best *models.SecurityBan
	for _, b := range s.byID {
		bs, ok := parseBanIP(*b)
		if !ok || bs != slot {
			continue
		}
		if holdsLonger(best, b, now) {
			best = b
		}
	}
	if best != nil {
		s.slots[slot] = best
	} else {
		delete(s.slots, slot)
	}
}

func (s *banSnapshot) addAllow(a models.SecurityAllowlist) {
	if strings.Contains(a.IPCidr, "/") {
		p, err := netip.ParsePrefix(a.IPCidr)
		if err != nil {
			logger.Warn("security: ignoring allowlist entry with unparseable CIDR", "id", a.ID, "ip_cidr", a.IPCidr)
			return
		}
		s.allowNets = append(s.allowNets, canonicalPrefix(p))
		return
	}
	addr, err := netip.ParseAddr(a.IPCidr)
	if err != nil {
		logger.Warn("security: ignoring allowlist entry with unparseable address", "id", a.ID, "ip_cidr", a.IPCidr)
		return
	}
	s.allowIPs[canonicalIP(addr)] = struct{}{}
}

// Replace swaps the whole ban state for the given active bans and allowlist
// entries. It backs startup and post-reconnect resyncs; entries that cannot
// be parsed are skipped with a warning.
func (l *BanList) Replace(bans []models.SecurityBan, allows []models.SecurityAllowlist) {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()

	snap := newBanSnapshot()
	now := l.now()
	for _, a := range allows {
		snap.addAllow(a)
	}
	for _, b := range bans {
		snap.addBan(b, now)
	}
	l.current.Store(snap)
}

// UpsertBan adds or refreshes a single ban, keyed by its canonical address or
// prefix. When another active ban already holds the slot, the one blocking
// longer keeps it (permanent beats timed, later expiry beats earlier); the
// row is registered by ID either way so RemoveBan can account for it.
func (l *BanList) UpsertBan(b models.SecurityBan) {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()

	snap := l.current.Load().clone()
	snap.addBan(b, l.now())
	l.current.Store(snap)
}

// RemoveBan drops a ban by ID. If it held its lookup slot, the slot falls
// back to the longest-blocking remaining ban with the same key. Removing an
// unknown ID is a no-op (a Replace may already have dropped it).
func (l *BanList) RemoveBan(id uint) {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()

	old := l.current.Load()
	removed, ok := old.byID[id]
	if !ok {
		return
	}
	snap := old.clone()
	delete(snap.byID, id)
	snap.reclaimSlot(removed, l.now())
	l.current.Store(snap)
}

// Blocked reports whether the address is currently banned, returning the
// matching ban. Exact-address bans are checked first (most specific), then
// CIDR prefixes, preferring the most specific active match. Expired bans are
// lazily ignored — no expiry sweeper runs. The returned ban is shared with
// the snapshot and must not be mutated by the caller.
func (l *BanList) Blocked(ip netip.Addr) (bool, *models.SecurityBan) {
	canon := canonicalIP(ip)
	if !canon.IsValid() {
		return false, nil
	}
	snap := l.current.Load()
	if snap == nil {
		return false, nil
	}
	now := l.now()

	if b, ok := snap.slots[banSlotKey{addr: canon}]; ok && banActive(b, now) {
		return true, b
	}
	var best *models.SecurityBan
	bestBits := -1
	for k, b := range snap.slots {
		if !k.cidr || k.prefix.Bits() <= bestBits {
			continue
		}
		if !k.prefix.Contains(canon) || !banActive(b, now) {
			continue
		}
		best, bestBits = b, k.prefix.Bits()
	}
	if best != nil {
		return true, best
	}
	return false, nil
}

// Allowed reports whether the address is exempt from detection and bans:
// loopback (when the list was built with allowLoopback), an exact allowlist
// entry, or a CIDR allowlist entry containing the address. Callers check
// this before Blocked — the allowlist always wins.
func (l *BanList) Allowed(ip netip.Addr) bool {
	canon := canonicalIP(ip)
	if !canon.IsValid() {
		return false
	}
	if l.allowLoopback && canon.IsLoopback() {
		return true
	}
	snap := l.current.Load()
	if snap == nil {
		return false
	}
	if _, ok := snap.allowIPs[canon]; ok {
		return true
	}
	for _, p := range snap.allowNets {
		if p.Contains(canon) {
			return true
		}
	}
	return false
}
