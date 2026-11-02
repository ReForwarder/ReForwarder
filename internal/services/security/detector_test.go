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
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// fakeClock is a controllable clock injected through the now func() time.Time
// seams, so window expiry and ban tiers are fully deterministic.
type fakeClock struct {
	cur time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{cur: time.Unix(1_700_000_000, 0).UTC()}
}

func (c *fakeClock) Now() time.Time { return c.cur }

func (c *fakeClock) Advance(d time.Duration) { c.cur = c.cur.Add(d) }

// windowMember is one sorted-set entry: the member plus its score (event time).
type windowMember struct {
	member string
	score  float64
}

// fakeWindowStore is an in-memory windowStore. It mirrors the semantics of the
// Redis implementation: ZAdd trims members strictly older than the window
// cutoff (score < cutoff), honors NX, caps the set at the newest `keep`
// members, and Members returns members oldest first.
type fakeWindowStore struct {
	sets       map[string][]windowMember
	zaddErr    error
	countErr   error
	membersErr error
}

func newFakeWindowStore() *fakeWindowStore {
	return &fakeWindowStore{sets: map[string][]windowMember{}}
}

func (f *fakeWindowStore) ZAdd(_ context.Context, key, member string, score float64, nx bool, window time.Duration, keep int64) (int64, error) {
	if f.zaddErr != nil {
		return 0, f.zaddErr
	}
	cutoff := score - float64(window.Nanoseconds())
	live := make([]windowMember, 0, len(f.sets[key])+1)
	exists := false
	for _, m := range f.sets[key] {
		if m.score < cutoff {
			continue // expired out of the window
		}
		if m.member == member {
			exists = true
		}
		live = append(live, m)
	}
	if !nx || !exists {
		live = append(live, windowMember{member: member, score: score})
	}
	// Oldest first; ties broken by member so the order is deterministic.
	for i := 1; i < len(live); i++ {
		for j := i; j > 0 && windowMemberLess(live[j], live[j-1]); j-- {
			live[j], live[j-1] = live[j-1], live[j]
		}
	}
	if int64(len(live)) > keep {
		live = live[len(live)-int(keep):]
	}
	f.sets[key] = live
	return int64(len(live)), nil
}

func windowMemberLess(a, b windowMember) bool {
	if a.score != b.score {
		return a.score < b.score
	}
	return a.member < b.member
}

func (f *fakeWindowStore) Count(_ context.Context, keys ...string) ([]int64, error) {
	if f.countErr != nil {
		return nil, f.countErr
	}
	out := make([]int64, len(keys))
	for i, k := range keys {
		out[i] = int64(len(f.sets[k]))
	}
	return out, nil
}

func (f *fakeWindowStore) Members(_ context.Context, key string) ([]string, error) {
	if f.membersErr != nil {
		return nil, f.membersErr
	}
	set := f.sets[key] // maintained sorted by ZAdd
	out := make([]string, len(set))
	for i, m := range set {
		out[i] = m.member
	}
	return out, nil
}

func (f *fakeWindowStore) count(key string) int { return len(f.sets[key]) }

func (f *fakeWindowStore) members(key string) []string {
	out, _ := f.Members(context.Background(), key)
	return out
}

// newTestDetector assembles a Detector over the fake store with the default
// rules; bans may be a stubbed BanService (see newStubBanService) or nil.
func newTestDetector(t *testing.T, clock *fakeClock, store *fakeWindowStore, bans *BanService, ev *EventRecorder, failOpen bool) *Detector {
	t.Helper()
	return &Detector{
		store:    store,
		rules:    NewRulesLoader(nil),
		bans:     bans,
		ev:       ev,
		now:      clock.Now,
		failOpen: failOpen,
		notified: newNotifyCache(notifyCacheCap),
		limiter:  newLogThrottle(),
	}
}

// eventCapture collects security_events rows.
type eventCapture struct {
	mu     sync.Mutex
	events []models.SecurityEvent
}

func (c *eventCapture) add(e models.SecurityEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *eventCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *eventCapture) all() []models.SecurityEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]models.SecurityEvent(nil), c.events...)
}

var registerNopDriverOnce sync.Once

type nopDriver struct{}

func (nopDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("security test: no SQL is executed (gorm DryRun)")
}

// newRecordingEventRecorder returns an EventRecorder whose persistence runs on
// a gorm DryRun session: inserts build SQL but never execute, and a
// before-create callback captures every row. This observes the real
// SignalExceeded path with no database.
func newRecordingEventRecorder(t *testing.T) (*EventRecorder, *eventCapture) {
	t.Helper()

	registerNopDriverOnce.Do(func() {
		sql.Register("security-test-nop", nopDriver{})
	})
	sqlDB, err := sql.Open("security-test-nop", "")
	if err != nil {
		t.Fatalf("open nop sql.DB: %v", err)
	}
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB,
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open dry-run gorm: %v", err)
	}

	capture := &eventCapture{}
	if err := gdb.Callback().Create().Before("gorm:create").Register("security-test:capture", func(tx *gorm.DB) {
		if ev, ok := tx.Statement.Dest.(*models.SecurityEvent); ok {
			capture.add(*ev)
		}
	}); err != nil {
		t.Fatalf("register capture callback: %v", err)
	}

	dry := gdb.Session(&gorm.Session{DryRun: true})
	return NewEventRecorder(repositories.NewSecurityEventRepository(dry)), capture
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestRecordConnectWindowKeysAndDistinctPortDedup pins the key layout
// (sec:sig:<signal>:<canonical-ip>) and the distinct_ports dedupe: the same
// port connected to repeatedly counts once, every connection counts for
// new_connections, and a v4-mapped v6 source lands in the plain v4 key.
func TestRecordConnectWindowKeysAndDistinctPortDedup(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	svc, st := newStubBanService(t, clock)
	d := newTestDetector(t, clock, store, svc, nil, true)
	ctx := context.Background()
	ip := mustAddr(t, "6.6.6.6")

	for i := 0; i < 40; i++ {
		clock.Advance(time.Millisecond)
		if err := d.RecordConnect(ctx, ip, 25, "smtp"); err != nil {
			t.Fatalf("RecordConnect #%d: %v", i+1, err)
		}
	}
	if got := store.count("sec:sig:distinct_ports:6.6.6.6"); got != 1 {
		t.Fatalf("distinct_ports count after 40 connects to one port = %d, want 1 (NX dedupe)", got)
	}
	if got := store.count("sec:sig:new_connections:6.6.6.6"); got != 40 {
		t.Fatalf("new_connections count = %d, want 40 (every event counted)", got)
	}
	if st.banCount() != 0 {
		t.Fatalf("40 connections must not ban (below instant 60 and score 20 < 40), got %d rows", st.banCount())
	}

	// Other ports join the distinct set; the v4-mapped spelling of the same
	// address lands in the canonical v4 key.
	if err := d.RecordConnect(ctx, netip.MustParseAddr("::ffff:6.6.6.6"), 22, "smtp"); err != nil {
		t.Fatalf("RecordConnect mapped: %v", err)
	}
	if err := d.RecordConnect(ctx, ip, 993, "smtp"); err != nil {
		t.Fatalf("RecordConnect 993: %v", err)
	}
	if got := store.count("sec:sig:distinct_ports:6.6.6.6"); got != 3 {
		t.Fatalf("distinct_ports count = %d, want 3", got)
	}
	for _, want := range []string{"22", "25", "993"} {
		found := false
		for _, m := range store.members("sec:sig:distinct_ports:6.6.6.6") {
			if m == want {
				found = true
			}
		}
		if !found {
			t.Errorf("distinct_ports members missing port %q: %v", want, store.members("sec:sig:distinct_ports:6.6.6.6"))
		}
	}
}

// TestWindowTrimDropsExpiredMembers pins the sliding window: members older
// than the window are dropped from the key on the next write.
func TestWindowTrimDropsExpiredMembers(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	svc, _ := newStubBanService(t, clock)
	d := newTestDetector(t, clock, store, svc, nil, true)
	ctx := context.Background()
	key := "sec:sig:connect_aborts:6.6.6.6"

	for i := 0; i < 3; i++ {
		clock.Advance(time.Millisecond)
		if err := d.RecordAbort(ctx, mustAddr(t, "6.6.6.6"), 25, "smtp"); err != nil {
			t.Fatalf("RecordAbort #%d: %v", i+1, err)
		}
	}
	if got := store.count(key); got != 3 {
		t.Fatalf("connect_aborts count = %d, want 3 inside the window", got)
	}

	clock.Advance(61 * time.Second) // window is 60s for connect_aborts
	if err := d.RecordAbort(ctx, mustAddr(t, "6.6.6.6"), 25, "smtp"); err != nil {
		t.Fatalf("RecordAbort after window: %v", err)
	}
	if got := store.count(key); got != 1 {
		t.Fatalf("connect_aborts count after window passed = %d, want 1 (old members trimmed)", got)
	}
}

// TestScoreSumAcrossSignalsBansWithEvidence drives a combined ban: a port
// scan (weight 35) alone sits under the risk threshold of 40, and a
// suspicious-path burst (weight 25) on top crosses it. The ban row must carry
// the summed score, the HTTP recon reason and the port list as evidence.
func TestScoreSumAcrossSignalsBansWithEvidence(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	svc, st := newStubBanService(t, clock)
	d := newTestDetector(t, clock, store, svc, nil, true)
	ctx := context.Background()
	ip := mustAddr(t, "6.6.6.6")

	for _, port := range []int{21, 22, 23, 24, 25} {
		clock.Advance(time.Millisecond)
		if err := d.RecordConnect(ctx, ip, port, "smtp"); err != nil {
			t.Fatalf("RecordConnect port %d: %v", port, err)
		}
	}
	if st.banCount() != 0 {
		t.Fatalf("a 35-point signal must sit below the threshold 40, got %d bans", st.banCount())
	}

	for i := 0; i < 5; i++ {
		clock.Advance(time.Millisecond)
		if err := d.RecordSuspiciousPath(ctx, ip, "/.env"); err != nil {
			t.Fatalf("RecordSuspiciousPath #%d: %v", i+1, err)
		}
	}

	if st.banCount() != 1 {
		t.Fatalf("35 + 25 must reach the risk threshold and ban once, got %d bans", st.banCount())
	}
	var row *models.SecurityBan
	for _, b := range st.bans {
		row = b
	}
	if row.IPAddress != "6.6.6.6" || row.BanType != BanTypeAutomatic || row.CreatedBy != "system" {
		t.Fatalf("automatic ban metadata wrong: %+v", row)
	}
	if row.ReasonCode != string(ReasonHTTPRecon) {
		t.Fatalf("ban reason = %q, want %q (the trigger signal wins)", row.ReasonCode, ReasonHTTPRecon)
	}
	if row.RiskScore != 60 {
		t.Fatalf("ban risk score = %d, want the summed 60", row.RiskScore)
	}

	var evidence map[string]any
	if err := json.Unmarshal([]byte(row.Evidence), &evidence); err != nil {
		t.Fatalf("ban evidence is not JSON: %v (raw %q)", err, row.Evidence)
	}
	if evidence["trigger"] != string(SigSuspiciousPath) {
		t.Errorf("evidence trigger = %v, want %q", evidence["trigger"], SigSuspiciousPath)
	}
	counts, ok := evidence["counts"].(map[string]any)
	if !ok {
		t.Fatalf("evidence counts missing or wrong type: %v", evidence["counts"])
	}
	if counts[string(SigDistinctPorts)] != float64(5) || counts[string(SigSuspiciousPath)] != float64(5) {
		t.Errorf("evidence counts = %v, want distinct_ports 5 and suspicious_paths 5", counts)
	}
	ports, ok := evidence["ports"].([]any)
	if !ok || len(ports) != 5 {
		t.Fatalf("evidence ports = %v, want the 5 scanned ports", evidence["ports"])
	}
	for i, want := range []string{"21", "22", "23", "24", "25"} {
		if ports[i] != want {
			t.Errorf("evidence ports[%d] = %v, want %q", i, ports[i], want)
		}
	}
}

// TestInstantBanAtThresholdTimesRatio pins the instant-ban rule: a single
// signal reaching threshold × ratio bans on its own, before any score sum.
func TestInstantBanAtThresholdTimesRatio(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	svc, st := newStubBanService(t, clock)
	d := newTestDetector(t, clock, store, svc, nil, true)
	ctx := context.Background()
	ip := mustAddr(t, "6.6.6.6")

	// auth_failures: threshold 10, ratio 2 → instant at 20. Up to 19 the
	// weight (20) stays under the risk threshold (40).
	for i := 0; i < 19; i++ {
		clock.Advance(time.Millisecond)
		if err := d.RecordAuthFailure(ctx, ip, "victim@example.com"); err != nil {
			t.Fatalf("RecordAuthFailure #%d: %v", i+1, err)
		}
	}
	if st.banCount() != 0 {
		t.Fatalf("19 auth failures must not ban, got %d bans", st.banCount())
	}

	clock.Advance(time.Millisecond)
	if err := d.RecordAuthFailure(ctx, ip, "victim@example.com"); err != nil {
		t.Fatalf("RecordAuthFailure #20: %v", err)
	}
	if st.banCount() != 1 {
		t.Fatalf("20 auth failures (threshold×ratio) must instant-ban once, got %d bans", st.banCount())
	}
	var row *models.SecurityBan
	for _, b := range st.bans {
		row = b
	}
	if row.ReasonCode != string(ReasonAuthBruteForce) {
		t.Fatalf("ban reason = %q, want %q", row.ReasonCode, ReasonAuthBruteForce)
	}
	if row.RiskScore != 20 {
		t.Fatalf("instant ban risk score = %d, want 20 (only the exceeded weight)", row.RiskScore)
	}

	var evidence map[string]any
	if err := json.Unmarshal([]byte(row.Evidence), &evidence); err != nil {
		t.Fatalf("ban evidence is not JSON: %v", err)
	}
	if evidence["login"] != "victim@example.com" {
		t.Errorf("evidence login = %v, want victim@example.com", evidence["login"])
	}
	if evidence["trigger"] != string(SigAuthFailures) {
		t.Errorf("evidence trigger = %v, want %q", evidence["trigger"], SigAuthFailures)
	}

	// The ban.created event carries the instant-ban rule decision.
	ev := st.eventByType(t, EventBanCreated)
	var evEvidence map[string]any
	if err := json.Unmarshal([]byte(ev.Evidence), &evEvidence); err != nil {
		t.Fatalf("ban event evidence is not JSON: %v", err)
	}
	if evEvidence["rule"] != "instant:auth_failures" {
		t.Errorf("ban event rule = %v, want instant:auth_failures", evEvidence["rule"])
	}
}

// TestAuthFailureLoginScopeKeyAloneBans pins the dual-key brute-force check:
// one account hit from many IPs is scored off the per-login window, whose key
// hashes the lower-cased login, and the attacking IP is banned.
func TestAuthFailureLoginScopeKeyAloneBans(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	svc, st := newStubBanService(t, clock)
	d := newTestDetector(t, clock, store, svc, nil, true)
	ctx := context.Background()

	loginKey := "sec:sig:auth_failures:login:" + sha256Hex("shared@example.com")
	for i := 0; i < 20; i++ {
		clock.Advance(time.Millisecond)
		ip := mustAddr(t, fmt.Sprintf("10.0.0.%d", i+1))
		// Mixed case on purpose: the login scope key must hash the
		// normalized (lower-cased) login so case variants share one window.
		if err := d.RecordAuthFailure(ctx, ip, "Shared@Example.com"); err != nil {
			t.Fatalf("RecordAuthFailure #%d: %v", i+1, err)
		}
	}

	if got := store.count(loginKey); got != 20 {
		t.Fatalf("per-login window count = %d, want 20 (all IPs share the login key)", got)
	}
	if got := store.count("sec:sig:auth_failures:10.0.0.1"); got != 1 {
		t.Fatalf("per-IP window count = %d, want 1", got)
	}
	if st.banCount() != 1 {
		t.Fatalf("a login window reaching the instant ratio must ban once, got %d bans", st.banCount())
	}
	var row *models.SecurityBan
	for _, b := range st.bans {
		row = b
	}
	// The evaluation ran for the 20th IP, so that is the banned source.
	if row.IPAddress != "10.0.0.20" {
		t.Fatalf("banned IP = %q, want 10.0.0.20 (the IP whose evaluation crossed)", row.IPAddress)
	}
	if row.ReasonCode != string(ReasonAuthBruteForce) {
		t.Fatalf("ban reason = %q, want %q", row.ReasonCode, ReasonAuthBruteForce)
	}
	var evidence map[string]any
	if err := json.Unmarshal([]byte(row.Evidence), &evidence); err != nil {
		t.Fatalf("ban evidence is not JSON: %v", err)
	}
	if evidence["login"] != "shared@example.com" {
		t.Errorf("evidence login = %v, want the normalized login", evidence["login"])
	}
}

// TestAuthFailureIPScopeKeyAloneBans is the mirror case: one IP guessing many
// accounts is scored off its own window.
func TestAuthFailureIPScopeKeyAloneBans(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	svc, st := newStubBanService(t, clock)
	d := newTestDetector(t, clock, store, svc, nil, true)
	ctx := context.Background()
	ip := mustAddr(t, "10.9.9.9")

	for i := 0; i < 20; i++ {
		clock.Advance(time.Millisecond)
		if err := d.RecordAuthFailure(ctx, ip, fmt.Sprintf("user%d@example.com", i)); err != nil {
			t.Fatalf("RecordAuthFailure #%d: %v", i+1, err)
		}
	}

	if st.banCount() != 1 {
		t.Fatalf("an IP window reaching the instant ratio must ban once, got %d bans", st.banCount())
	}
	var row *models.SecurityBan
	for _, b := range st.bans {
		row = b
	}
	if row.IPAddress != "10.9.9.9" {
		t.Fatalf("banned IP = %q, want 10.9.9.9", row.IPAddress)
	}
}

// TestFailOpenSwallowsStoreErrors pins the fail-open default: Redis failures
// are swallowed (no error to the caller, no ban, no panic).
func TestFailOpenSwallowsStoreErrors(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	store.zaddErr = errors.New("redis down")
	svc, st := newStubBanService(t, clock)
	d := newTestDetector(t, clock, store, svc, nil, true)
	ctx := context.Background()
	ip := mustAddr(t, "6.6.6.6")

	if err := d.RecordConnect(ctx, ip, 25, "smtp"); err != nil {
		t.Fatalf("fail-open RecordConnect must return nil, got %v", err)
	}
	if err := d.RecordAuthFailure(ctx, ip, "a@example.com"); err != nil {
		t.Fatalf("fail-open RecordAuthFailure must return nil, got %v", err)
	}
	if st.banCount() != 0 {
		t.Fatalf("fail-open must never ban on store errors, got %d bans", st.banCount())
	}

	// Count failures during evaluation are swallowed the same way.
	store.zaddErr = nil
	store.countErr = errors.New("count failed")
	if err := d.RecordAbort(ctx, ip, 25, "smtp"); err != nil {
		t.Fatalf("fail-open evaluation error must return nil, got %v", err)
	}
	if st.banCount() != 0 {
		t.Fatalf("fail-open must never ban on evaluation errors, got %d bans", st.banCount())
	}
}

// TestFailClosedReturnsErrors pins the other mode: when failOpen is off, Redis
// failures surface to the caller (HTTP paths reject instead of letting
// traffic through).
func TestFailClosedReturnsErrors(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	store.zaddErr = errors.New("redis down")
	svc, st := newStubBanService(t, clock)
	d := newTestDetector(t, clock, store, svc, nil, false)
	ctx := context.Background()
	ip := mustAddr(t, "6.6.6.6")

	err := d.RecordConnect(ctx, ip, 25, "smtp")
	if err == nil || !strings.Contains(err.Error(), "redis down") {
		t.Fatalf("fail-closed RecordConnect = %v, want the redis error back", err)
	}

	store.zaddErr = nil
	store.countErr = errors.New("count failed")
	err = d.RecordAbort(ctx, ip, 25, "smtp")
	if err == nil || !strings.Contains(err.Error(), "count failed") {
		t.Fatalf("fail-closed evaluation = %v, want the count error back", err)
	}
	if st.banCount() != 0 {
		t.Fatalf("failed evaluations must not ban, got %d bans", st.banCount())
	}
}

// TestSignalExceededEmittedOncePerWindowCrossing pins the event semantics:
// one signal_exceeded row per window crossing — repeats inside the window are
// suppressed, a later crossing after the window closes emits again.
func TestSignalExceededEmittedOncePerWindowCrossing(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	svc, _ := newStubBanService(t, clock)
	ev, capture := newRecordingEventRecorder(t)
	d := newTestDetector(t, clock, store, svc, ev, true)
	ctx := context.Background()
	ip := mustAddr(t, "6.6.6.6")

	for _, port := range []int{21, 22, 23, 24, 25} {
		clock.Advance(time.Millisecond)
		if err := d.RecordConnect(ctx, ip, port, "smtp"); err != nil {
			t.Fatalf("RecordConnect port %d: %v", port, err)
		}
	}
	events := capture.all()
	if len(events) != 1 {
		t.Fatalf("first crossing must emit exactly one signal_exceeded event, got %d", len(events))
	}
	first := events[0]
	if first.EventType != string(SigDistinctPorts) || first.Severity != SeverityWarning || first.ActionTaken != EventActionRecorded {
		t.Fatalf("signal_exceeded row = %q/%q/%q, want distinct_ports/warning/recorded",
			first.EventType, first.Severity, first.ActionTaken)
	}
	if first.SourceIP != "6.6.6.6" || first.RiskScore != 35 {
		t.Fatalf("signal_exceeded row = ip %q score %d, want 6.6.6.6/35", first.SourceIP, first.RiskScore)
	}

	// Repeat crossings inside the same window stay silent.
	for i := 0; i < 5; i++ {
		clock.Advance(time.Millisecond)
		if err := d.RecordConnect(ctx, ip, 21, "smtp"); err != nil {
			t.Fatalf("repeat RecordConnect #%d: %v", i+1, err)
		}
	}
	if got := capture.count(); got != 1 {
		t.Fatalf("repeats inside the window must not emit, got %d events", got)
	}

	// After the window closes, a fresh crossing emits again.
	clock.Advance(61 * time.Second)
	for _, port := range []int{31, 32, 33, 34, 35} {
		clock.Advance(time.Millisecond)
		if err := d.RecordConnect(ctx, ip, port, "smtp"); err != nil {
			t.Fatalf("RecordConnect after window port %d: %v", port, err)
		}
	}
	if got := capture.count(); got != 2 {
		t.Fatalf("a fresh crossing after the window must emit again, got %d events total", got)
	}
}

// TestAllowlistedAndAlreadyBlockedIPsShortCircuit pins the evaluation
// short-circuits: allowlisted IPs are never banned, and an already-blocked IP
// does not spawn a second ban row.
func TestAllowlistedAndAlreadyBlockedIPsShortCircuit(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	svc, st := newStubBanService(t, clock)
	d := newTestDetector(t, clock, store, svc, nil, true)
	ctx := context.Background()

	svc.list.Replace(nil, []models.SecurityAllowlist{{ID: 1, IPCidr: "7.7.7.7"}})
	for i := 0; i < 25; i++ {
		clock.Advance(time.Millisecond)
		if err := d.RecordAuthFailure(ctx, mustAddr(t, "7.7.7.7"), "a@example.com"); err != nil {
			t.Fatalf("RecordAuthFailure #%d: %v", i+1, err)
		}
	}
	if st.banCount() != 0 {
		t.Fatalf("allowlisted IP must never be banned, got %d bans", st.banCount())
	}

	svc.list.UpsertBan(banRow(1, "8.8.8.8", false, nil))
	for i := 0; i < 25; i++ {
		clock.Advance(time.Millisecond)
		if err := d.RecordAuthFailure(ctx, mustAddr(t, "8.8.8.8"), "b@example.com"); err != nil {
			t.Fatalf("RecordAuthFailure #%d: %v", i+1, err)
		}
	}
	if st.banCount() != 0 {
		t.Fatalf("already-blocked IP must not be re-banned, got %d bans", st.banCount())
	}
}

// TestSuspiciousPathOnlyHighRiskTriggersEvaluation pins the path semantics:
// ordinary paths accumulate in the window but never evaluate, so they cannot
// ban or emit events on their own; a high-risk probe path evaluates
// immediately.
func TestSuspiciousPathOnlyHighRiskTriggersEvaluation(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	svc, st := newStubBanService(t, clock)
	ev, capture := newRecordingEventRecorder(t)
	d := newTestDetector(t, clock, store, svc, ev, true)
	ctx := context.Background()
	ip := mustAddr(t, "6.6.6.6")
	key := "sec:sig:suspicious_paths:6.6.6.6"

	// 6 ordinary hits: above the threshold of 5, below the instant ratio of
	// 10 — yet no event, because ordinary paths never evaluate.
	for i := 0; i < 6; i++ {
		clock.Advance(time.Millisecond)
		if err := d.RecordSuspiciousPath(ctx, ip, "/products/42"); err != nil {
			t.Fatalf("RecordSuspiciousPath #%d: %v", i+1, err)
		}
	}
	if got := store.count(key); got != 6 {
		t.Fatalf("suspicious_paths count = %d, want 6", got)
	}
	if got := capture.count(); got != 0 {
		t.Fatalf("ordinary paths must not emit signal_exceeded events, got %d", got)
	}
	if st.banCount() != 0 {
		t.Fatalf("ordinary paths must not ban, got %d bans", st.banCount())
	}

	// A high-risk probe path triggers the evaluation: the window (7) is over
	// the threshold, so one warning event is emitted — still no ban.
	clock.Advance(time.Millisecond)
	if err := d.RecordSuspiciousPath(ctx, ip, "/.env"); err != nil {
		t.Fatalf("RecordSuspiciousPath high-risk: %v", err)
	}
	if got := capture.count(); got != 1 {
		t.Fatalf("high-risk path must evaluate and emit once, got %d events", got)
	}
	if st.banCount() != 0 {
		t.Fatalf("7 suspicious paths must not ban (below instant 10 and score 25 < 40), got %d bans", st.banCount())
	}
}

// TestTLSProbingReasonOnHandshakeFails pins the TLS mapping: incomplete
// handshakes with protocol "tls" instant-ban as TLS_PROBING at threshold×ratio.
func TestTLSProbingReasonOnHandshakeFails(t *testing.T) {
	clock := newFakeClock()
	store := newFakeWindowStore()
	svc, st := newStubBanService(t, clock)
	d := newTestDetector(t, clock, store, svc, nil, true)
	ctx := context.Background()
	ip := mustAddr(t, "6.6.6.6")

	for i := 0; i < 19; i++ {
		clock.Advance(time.Millisecond)
		if err := d.RecordHandshakeFail(ctx, ip, 993, "tls"); err != nil {
			t.Fatalf("RecordHandshakeFail #%d: %v", i+1, err)
		}
	}
	if st.banCount() != 0 {
		t.Fatalf("19 TLS handshake failures must not ban, got %d bans", st.banCount())
	}

	clock.Advance(time.Millisecond)
	if err := d.RecordHandshakeFail(ctx, ip, 993, "tls"); err != nil {
		t.Fatalf("RecordHandshakeFail #20: %v", err)
	}
	if st.banCount() != 1 {
		t.Fatalf("20 TLS handshake failures must instant-ban once, got %d bans", st.banCount())
	}
	var row *models.SecurityBan
	for _, b := range st.bans {
		row = b
	}
	if row.ReasonCode != string(ReasonTLSProbing) {
		t.Fatalf("ban reason = %q, want %q", row.ReasonCode, ReasonTLSProbing)
	}
	if !strings.Contains(row.ReasonDescription, "TLS handshakes") {
		t.Fatalf("ban description %q should mention TLS handshakes", row.ReasonDescription)
	}
}
