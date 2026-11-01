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
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// banStoreStub is an in-memory stand-in for the ban persistence hooks. The
// BanService carries its repositories as function fields (the tracking-service
// test convention), so tests stub every one of them and exercise the full ban
// lifecycle with no database.
type banStoreStub struct {
	now    func() time.Time
	bans   map[uint]*models.SecurityBan
	events []models.SecurityEvent
	nextID uint

	createCalls int
	updateCalls int
	saveCalls   int
}

func (st *banStoreStub) countByIPFor(ip string) int64 {
	var n int64
	for _, b := range st.bans {
		if b.IPAddress == ip && b.CreatedAt.Before(st.now()) {
			n++
		}
	}
	return n
}

func (st *banStoreStub) activeBanFor(ip string) *models.SecurityBan {
	var best *models.SecurityBan
	now := st.now()
	for _, b := range st.bans {
		if b.IPAddress != ip {
			continue
		}
		if b.ExpiresAt != nil && !now.Before(*b.ExpiresAt) {
			continue // expired
		}
		if best == nil || b.CreatedAt.After(best.CreatedAt) {
			best = b
		}
	}
	return best
}

// newStubBanService returns a BanService whose persistence is fully stubbed.
// The detector tests reuse it: BanService.Ban is the detector's ban sink.
func newStubBanService(t *testing.T, clock *fakeClock) (*BanService, *banStoreStub) {
	t.Helper()

	st := &banStoreStub{now: clock.Now, bans: map[uint]*models.SecurityBan{}, nextID: 1}
	s := &BanService{
		list: NewBanList(true),
		now:  clock.Now,
		// TierHours nil: the default 1/24/168/720 ladder applies.

		countByIP: func(ip string) (int64, error) {
			return st.countByIPFor(ip), nil
		},
		findActiveByIP: func(ip string) (*models.SecurityBan, error) {
			b := st.activeBanFor(ip)
			if b == nil {
				return nil, gorm.ErrRecordNotFound
			}
			cpy := *b
			return &cpy, nil
		},
		createBan: func(b *models.SecurityBan) error {
			st.createCalls++
			b.ID = st.nextID
			st.nextID++
			cpy := *b
			st.bans[b.ID] = &cpy
			return nil
		},
		updateBanExpiry: func(id uint, expiresAt time.Time) error {
			st.updateCalls++
			if b, ok := st.bans[id]; ok {
				b.ExpiresAt = &expiresAt
			}
			return nil
		},
		saveBan: func(b *models.SecurityBan) error {
			st.saveCalls++
			cpy := *b
			st.bans[b.ID] = &cpy
			return nil
		},
		findBanByID: func(id uint) (*models.SecurityBan, error) {
			b, ok := st.bans[id]
			if !ok {
				return nil, gorm.ErrRecordNotFound
			}
			cpy := *b
			return &cpy, nil
		},
		createEvent: func(event *models.SecurityEvent) error {
			st.events = append(st.events, *event)
			return nil
		},
	}
	s.list.now = clock.Now
	return s, st
}

func (st *banStoreStub) banCount() int { return len(st.bans) }

// eventByType returns the last recorded event of the given type.
func (st *banStoreStub) eventByType(t *testing.T, eventType string) models.SecurityEvent {
	t.Helper()
	for i := len(st.events) - 1; i >= 0; i-- {
		if st.events[i].EventType == eventType {
			return st.events[i]
		}
	}
	t.Fatalf("no security_event of type %q recorded; have %v", eventType, st.eventTypes())
	return models.SecurityEvent{}
}

func (st *banStoreStub) eventTypes() []string {
	out := make([]string, 0, len(st.events))
	for _, e := range st.events {
		out = append(out, e.EventType)
	}
	return out
}

func TestBanServiceAllowlistWins(t *testing.T) {
	clock := newFakeClock()
	s, st := newStubBanService(t, clock)

	s.list.Replace(nil, []models.SecurityAllowlist{{ID: 1, IPCidr: "3.3.3.3"}})

	ban, err := s.Ban(context.Background(), BanInput{
		IP:      mustAddr(t, "3.3.3.3"),
		Reason:  ReasonPortScan,
		BanType: BanTypeAutomatic,
	})
	if err != nil {
		t.Fatalf("Ban on an allowlisted IP returned an error: %v", err)
	}
	if ban != nil {
		t.Fatalf("allowlisted IP must not be banned, got %+v", ban)
	}
	if st.banCount() != 0 || st.createCalls != 0 {
		t.Fatalf("allowlisted IP must not reach persistence: %d rows, %d inserts", st.banCount(), st.createCalls)
	}

	// The attempt is recorded, not banned: action=recorded, severity=info,
	// evidence flags the suppression.
	ev := st.eventByType(t, string(ReasonPortScan))
	if ev.ActionTaken != EventActionRecorded || ev.Severity != SeverityInfo {
		t.Fatalf("suppression event = action %q severity %q, want recorded/info", ev.ActionTaken, ev.Severity)
	}
	if ev.SourceIP != "3.3.3.3" {
		t.Fatalf("suppression event source_ip = %q, want 3.3.3.3", ev.SourceIP)
	}
	var evidence map[string]any
	if err := json.Unmarshal([]byte(ev.Evidence), &evidence); err != nil {
		t.Fatalf("event evidence is not JSON: %v", err)
	}
	if evidence["allowlisted"] != true {
		t.Fatalf("suppression event evidence missing allowlisted=true: %v", evidence)
	}

	// A CIDR allowlist entry suppresses bans for every address inside it.
	s.list.Replace(nil, []models.SecurityAllowlist{{ID: 2, IPCidr: "10.0.0.0/8"}})
	ban, err = s.Ban(context.Background(), BanInput{
		IP:      mustAddr(t, "10.1.2.3"),
		Reason:  ReasonPortScan,
		BanType: BanTypeAutomatic,
	})
	if err != nil || ban != nil {
		t.Fatalf("CIDR allowlist must suppress the ban, got (%+v, %v)", ban, err)
	}
	if st.banCount() != 0 {
		t.Fatalf("CIDR allowlisted IP must not be banned, got %d rows", st.banCount())
	}
}

// TestBanServiceAutomaticTierEscalation pins the offense ladder: TTL comes
// from the tier indexed by the offense count (prior bans + 1), capped at the
// fourth tier — 1h, 24h, 168h, then 720h forever.
func TestBanServiceAutomaticTierEscalation(t *testing.T) {
	clock := newFakeClock()
	s, st := newStubBanService(t, clock)
	ctx := context.Background()
	ip := mustAddr(t, "6.6.6.6")

	place := func() *models.SecurityBan {
		t.Helper()
		ban, err := s.Ban(ctx, BanInput{IP: ip, Reason: ReasonPortScan, BanType: BanTypeAutomatic})
		if err != nil {
			t.Fatalf("Ban: %v", err)
		}
		return ban
	}

	wantHours := []int{1, 24, 168, 720, 720}
	wantOffense := []int{1, 2, 3, 4, 5}
	for i, want := range wantHours {
		ban := place()
		if ban.OffenseCount != wantOffense[i] {
			t.Errorf("ban #%d: offense_count = %d, want %d", i+1, ban.OffenseCount, wantOffense[i])
		}
		wantExpiry := clock.Now().Add(time.Duration(want) * time.Hour)
		if ban.ExpiresAt == nil || !ban.ExpiresAt.Equal(wantExpiry) {
			t.Errorf("ban #%d: expiry = %v, want %v (tier %dh)", i+1, ban.ExpiresAt, wantExpiry, want)
		}
		// The stored row matches what was returned.
		if row := st.bans[ban.ID]; row == nil || row.ExpiresAt == nil || !row.ExpiresAt.Equal(wantExpiry) {
			t.Errorf("ban #%d: stored expiry does not match", i+1)
		}
		// Let the ban lapse before the next placement so it counts as a new
		// offense rather than an extension of the running one.
		clock.Advance(time.Duration(want)*time.Hour + time.Minute)
	}

	// Every placement produced a ban.created security_event.
	if ev := st.eventByType(t, EventBanCreated); ev.ActionTaken != EventActionBanned || ev.Severity != SeverityCritical {
		t.Fatalf("ban.created event = action %q severity %q, want banned/critical", ev.ActionTaken, ev.Severity)
	}
}

// TestBanServiceZeroTierMeansPermanent pins the TierHours escape hatch: a
// tier of 0 (or less) places a permanent ban instead of a zero-length one.
func TestBanServiceZeroTierMeansPermanent(t *testing.T) {
	clock := newFakeClock()
	s, st := newStubBanService(t, clock)
	s.TierHours = func(int) int { return 0 }

	ban, err := s.Ban(context.Background(), BanInput{IP: mustAddr(t, "6.6.6.7"), Reason: ReasonPortScan})
	if err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if ban.ExpiresAt != nil {
		t.Fatalf("zero tier must place a permanent ban, got expiry %v", ban.ExpiresAt)
	}
	if row := st.bans[ban.ID]; row.ExpiresAt != nil {
		t.Fatalf("stored row must be permanent, got expiry %v", row.ExpiresAt)
	}
}

// TestBanServiceExtendsActiveBanKeepsLongerExpiry pins the GREATEST semantics:
// a second trigger against an actively banned IP only extends when the new
// expiry is longer, and the original offense count survives.
func TestBanServiceExtendsActiveBanKeepsLongerExpiry(t *testing.T) {
	clock := newFakeClock()
	s, st := newStubBanService(t, clock)
	ctx := context.Background()
	ip := mustAddr(t, "7.7.7.7")

	first, err := s.Ban(ctx, BanInput{IP: ip, Reason: ReasonPortScan})
	if err != nil {
		t.Fatalf("first Ban: %v", err)
	}
	if first.ExpiresAt == nil || !first.ExpiresAt.Equal(clock.Now().Add(time.Hour)) {
		t.Fatalf("first ban expiry = %v, want now+1h", first.ExpiresAt)
	}

	clock.Advance(30 * time.Minute)

	// A much longer manual ban extends the running one.
	extended, err := s.ManualBan(ctx, BanInput{IP: ip, Reason: ReasonManual, CreatedBy: "admin@example.com"}, false, 48)
	if err != nil {
		t.Fatalf("ManualBan: %v", err)
	}
	if extended.ID != first.ID {
		t.Fatalf("extension must update the existing ban (id %d), got a new ban id %d", first.ID, extended.ID)
	}
	wantExpiry := clock.Now().Add(48 * time.Hour)
	if extended.ExpiresAt == nil || !extended.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("extended expiry = %v, want %v", extended.ExpiresAt, wantExpiry)
	}
	if extended.OffenseCount != 1 {
		t.Fatalf("extension must keep the original offense count, got %d", extended.OffenseCount)
	}
	if st.updateCalls == 0 {
		t.Fatal("extension must be persisted via the expiry update")
	}

	// A shorter duration loses against the running expiry.
	before := st.updateCalls
	if _, err := s.ManualBan(ctx, BanInput{IP: ip, Reason: ReasonManual}, false, 2); err != nil {
		t.Fatalf("shorter ManualBan: %v", err)
	}
	if row := st.bans[first.ID]; row.ExpiresAt == nil || !row.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("shorter ban must not shorten the expiry, got %v", row.ExpiresAt)
	}
	if st.updateCalls != before {
		t.Fatal("a shorter expiry must not write a new expiry")
	}

	// A permanent manual ban promotes the timed ban to permanent.
	if _, err := s.ManualBan(ctx, BanInput{IP: ip, Reason: ReasonManual}, true, 0); err != nil {
		t.Fatalf("permanent ManualBan: %v", err)
	}
	if row := st.bans[first.ID]; row.ExpiresAt != nil {
		t.Fatalf("permanent ban must make the ban permanent, got expiry %v", row.ExpiresAt)
	}
	if st.saveCalls == 0 {
		t.Fatal("promotion to permanent must be persisted")
	}
}

func TestBanServiceUnban(t *testing.T) {
	clock := newFakeClock()
	s, st := newStubBanService(t, clock)
	ctx := context.Background()
	ip := mustAddr(t, "8.8.8.8")

	ban, err := s.Ban(ctx, BanInput{IP: ip, Reason: ReasonPortScan})
	if err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if blocked, _ := s.IsBlocked(ip); !blocked {
		t.Fatal("IP must be blocked right after Ban")
	}

	clock.Advance(10 * time.Minute)
	if err := s.Unban(ctx, ban.ID, "admin@example.com"); err != nil {
		t.Fatalf("Unban: %v", err)
	}

	// The row is expired in place and kept as offense history.
	row := st.bans[ban.ID]
	if row.ExpiresAt == nil || !row.ExpiresAt.Equal(clock.Now()) {
		t.Fatalf("unbanned row expiry = %v, want the unban instant %v", row.ExpiresAt, clock.Now())
	}
	// The snapshot entry is lifted.
	if blocked, _ := s.IsBlocked(ip); blocked {
		t.Fatal("unbanned IP must no longer be blocked in the snapshot")
	}
	// A ban.lifted event records who lifted it.
	ev := st.eventByType(t, EventBanLifted)
	if ev.SourceIP != "8.8.8.8" || ev.Severity != SeverityInfo {
		t.Fatalf("ban.lifted event = %q/%q, want 8.8.8.8/info", ev.SourceIP, ev.Severity)
	}
	var evidence map[string]any
	if err := json.Unmarshal([]byte(ev.Evidence), &evidence); err != nil {
		t.Fatalf("ban.lifted evidence is not JSON: %v", err)
	}
	if evidence["lifted_by"] != "admin@example.com" {
		t.Fatalf("ban.lifted evidence lifted_by = %v, want admin@example.com", evidence["lifted_by"])
	}

	// Unbanning an unknown id reports an error, not a silent success.
	if err := s.Unban(ctx, 999, "admin@example.com"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Unban(999) = %v, want a not-found error", err)
	}
}

func TestBanServiceManualBanShapes(t *testing.T) {
	clock := newFakeClock()
	s, st := newStubBanService(t, clock)
	ctx := context.Background()

	// Permanent.
	ban, err := s.ManualBan(ctx, BanInput{IP: mustAddr(t, "5.5.5.5"), Reason: ReasonManual, CreatedBy: "ops@example.com"}, true, 0)
	if err != nil {
		t.Fatalf("permanent ManualBan: %v", err)
	}
	if ban.ExpiresAt != nil {
		t.Fatalf("permanent ban must have no expiry, got %v", ban.ExpiresAt)
	}
	if ban.BanType != BanTypeManual || ban.CreatedBy != "ops@example.com" {
		t.Fatalf("manual ban metadata = type %q by %q, want manual/ops@example.com", ban.BanType, ban.CreatedBy)
	}

	// Timed.
	timed, err := s.ManualBan(ctx, BanInput{IP: mustAddr(t, "5.5.5.6"), Reason: ReasonManual}, false, 5)
	if err != nil {
		t.Fatalf("timed ManualBan: %v", err)
	}
	if timed.ExpiresAt == nil || !timed.ExpiresAt.Equal(clock.Now().Add(5*time.Hour)) {
		t.Fatalf("timed ban expiry = %v, want now+5h", timed.ExpiresAt)
	}

	// Neither permanent nor a duration is rejected.
	if _, err := s.ManualBan(ctx, BanInput{IP: mustAddr(t, "5.5.5.7"), Reason: ReasonManual}, false, 0); err == nil {
		t.Fatal("ManualBan without permanent or duration must fail")
	}

	// CIDR: the row stores the canonical masked prefix and blocks the range.
	cidr := mustCIDR(t, "10.0.0.0/24")
	cBan, err := s.ManualBan(ctx, BanInput{CIDR: &cidr, Reason: ReasonManual}, true, 0)
	if err != nil {
		t.Fatalf("CIDR ManualBan: %v", err)
	}
	if !cBan.IsCIDR || cBan.IPAddress != "10.0.0.0/24" {
		t.Fatalf("CIDR ban row = %q is_cidr=%v, want 10.0.0.0/24 true", cBan.IPAddress, cBan.IsCIDR)
	}
	if blocked, _ := s.IsBlocked(mustAddr(t, "10.0.0.77")); !blocked {
		t.Fatal("10.0.0.77 must be blocked by the /24 ban")
	}
	if blocked, _ := s.IsBlocked(mustAddr(t, "10.0.1.77")); blocked {
		t.Fatal("10.0.1.77 is outside the /24 ban")
	}

	// An invalid target is rejected before any persistence.
	if _, err := s.Ban(ctx, BanInput{Reason: ReasonManual}); err == nil {
		t.Fatal("Ban with no IP and no CIDR must fail")
	}
	if st.banCount() != 3 {
		t.Fatalf("unexpected number of stored rows: %d", st.banCount())
	}
}

// TestBanServiceIsBlockedIsSnapshotOnly pins the hot-path contract: IsBlocked
// reads the in-memory snapshot only, so it keeps answering after the backing
// persistence disappears.
func TestBanServiceIsBlockedIsSnapshotOnly(t *testing.T) {
	clock := newFakeClock()
	s, st := newStubBanService(t, clock)

	ban, err := s.Ban(context.Background(), BanInput{IP: mustAddr(t, "9.9.9.9"), Reason: ReasonPortScan})
	if err != nil {
		t.Fatalf("Ban: %v", err)
	}
	blocked, got := s.IsBlocked(mustAddr(t, "9.9.9.9"))
	if !blocked || got == nil || got.ID != ban.ID {
		t.Fatalf("IsBlocked = (%v, %+v), want the placed ban", blocked, got)
	}

	// Wipe the "database": the snapshot answer must not change and no stub
	// must be consulted.
	st.bans = map[uint]*models.SecurityBan{}
	before := st.createCalls
	if blocked, _ := s.IsBlocked(mustAddr(t, "9.9.9.9")); !blocked {
		t.Fatal("IsBlocked must keep answering from the snapshot alone")
	}
	if st.createCalls != before {
		t.Fatal("IsBlocked must not touch persistence")
	}

	// Loopback is allowed by the snapshot (NewBanList(true)), never blocked.
	if !s.IsAllowed(mustAddr(t, "127.0.0.1")) {
		t.Fatal("loopback must be allowed")
	}
	if blocked, _ := s.IsBlocked(mustAddr(t, "127.0.0.1")); blocked {
		t.Fatal("loopback must not be blocked")
	}
}

func TestBanServiceEvidenceJSONRoundTrip(t *testing.T) {
	clock := newFakeClock()
	s, _ := newStubBanService(t, clock)

	in := BanInput{
		IP:        mustAddr(t, "11.11.11.11"),
		Reason:    ReasonAuthBruteForce,
		Rule:      "instant:auth_failures",
		RiskScore: 60,
		Evidence: map[string]any{
			"score":  60,
			"counts": map[string]int{"auth_failures": 20},
			"login":  "victim@example.com",
		},
	}
	ban, err := s.Ban(context.Background(), in)
	if err != nil {
		t.Fatalf("Ban: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(ban.Evidence), &out); err != nil {
		t.Fatalf("stored evidence is not JSON: %v (raw %q)", err, ban.Evidence)
	}
	if out["score"] != float64(60) || out["login"] != "victim@example.com" {
		t.Fatalf("evidence round-trip lost values: %v", out)
	}
	counts, ok := out["counts"].(map[string]any)
	if !ok || counts["auth_failures"] != float64(20) {
		t.Fatalf("evidence counts round-trip failed: %v", out["counts"])
	}

	// Empty evidence stores the column default, not the empty string.
	empty, err := s.Ban(context.Background(), BanInput{IP: mustAddr(t, "11.11.11.12"), Reason: ReasonPortScan})
	if err != nil {
		t.Fatalf("Ban without evidence: %v", err)
	}
	if empty.Evidence != "{}" {
		t.Fatalf("nil evidence must store %q, got %q", "{}", empty.Evidence)
	}
}

func mustCIDR(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("parse prefix %q: %v", s, err)
	}
	return p
}
