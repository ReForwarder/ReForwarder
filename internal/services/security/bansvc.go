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
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// Ban types stored in SecurityBan.BanType.
const (
	BanTypeAutomatic = "automatic"
	BanTypeManual    = "manual"
)

// CreatedBySystem marks bans placed by the detection engine rather than by
// an administrator.
const CreatedBySystem = "system"

// BanInput describes a ban placement. IP is the address (or, for CIDR bans,
// the network address); CIDR is set only for CIDR bans, which are placed
// through ManualBan.
type BanInput struct {
	IP          netip.Addr
	CIDR        *netip.Prefix
	Reason      Reason
	Description string
	Evidence    map[string]any
	RiskScore   int
	Rule        string // rule/signal that triggered the ban (recorded in evidence)
	BanType     string // automatic|manual
	CreatedBy   string // "system" or the admin email
}

// BanService places, lifts and extends bans. Every mutation is applied to the
// database, the in-memory BanList and the Redis channel, in that order: the
// snapshot is the hot-path view, the database the source of truth, and the
// channel keeps other replicas in step.
type BanService struct {
	db   *gorm.DB
	list *BanList
	prop *Propagator
	now  func() time.Time

	banRepo   *repositories.SecurityBanRepository
	allowRepo *repositories.SecurityAllowlistRepository
	eventRepo *repositories.SecurityEventRepository

	// TierHours resolves the automatic ban duration in hours for a 1-based
	// offense count. NewEngine injects an implementation backed by the
	// security_ban_tier_* settings; the default matches the seeded values
	// (1h, 24h, 168h, 720h). A value <= 0 means the ban is permanent.
	TierHours func(offense int) int

	// Persistence hooks, following the tracking service convention: they
	// default to the repository methods and exist only so tests can stub
	// persistence without a database.
	countByIP       func(ip string) (int64, error)
	findActiveByIP  func(ip string) (*models.SecurityBan, error)
	createBan       func(ban *models.SecurityBan) error
	updateBanExpiry func(id uint, expiresAt time.Time) error
	saveBan         func(ban *models.SecurityBan) error
	findBanByID     func(id uint) (*models.SecurityBan, error)
	createEvent     func(event *models.SecurityEvent) error
}

// NewBanService returns a BanService over the given ban list, propagator and
// database. A nil now defaults to time.Now; a nil propagator disables
// publishing (tests, single-process operation without Redis).
func NewBanService(db *gorm.DB, list *BanList, prop *Propagator, now func() time.Time) *BanService {
	if now == nil {
		now = time.Now
	}
	banRepo := repositories.NewSecurityBanRepository(db)
	eventRepo := repositories.NewSecurityEventRepository(db)
	return &BanService{
		db:        db,
		list:      list,
		prop:      prop,
		now:       now,
		banRepo:   banRepo,
		allowRepo: repositories.NewSecurityAllowlistRepository(db),
		eventRepo: eventRepo,
		TierHours: defaultTierHours,

		countByIP:       banRepo.CountByIP,
		findActiveByIP:  banRepo.FindActiveByIP,
		createBan:       banRepo.Create,
		updateBanExpiry: banRepo.UpdateExpiry,
		saveBan:         banRepo.Update,
		findBanByID:     banRepo.FindByID,
		createEvent:     eventRepo.Create,
	}
}

// defaultTierHours is the automatic escalation ladder: 1h, 24h, 1 week,
// 30 days, capped at the fourth tier.
func defaultTierHours(offense int) int {
	hours := []int{1, 24, 168, 720}
	if offense < 1 {
		offense = 1
	}
	if offense > len(hours) {
		offense = len(hours)
	}
	return hours[offense-1]
}

// Ban places an automatic ban with the escalation TTL for the IP's offense
// count: offense = prior bans + 1, tier = min(offense, 4), TTL = tier hours
// (a zero tier TTL means permanent). When an active ban already exists the
// expiry is extended to the longer of the two and the original offense count
// is kept.
//
// The allowlist wins first: an allowlisted address gets a "recorded" event
// and no ban. Errors are returned; a suppressed ban is reported as
// (nil, nil).
func (s *BanService) Ban(ctx context.Context, in BanInput) (*models.SecurityBan, error) {
	if in.BanType == "" {
		in.BanType = BanTypeAutomatic
	}
	return s.applyBan(ctx, in, s.autoExpiry)
}

// ManualBan places an administrative ban: permanent, or for durationHours.
// It supports CIDR input via BanInput.CIDR.
func (s *BanService) ManualBan(ctx context.Context, in BanInput, permanent bool, durationHours int) (*models.SecurityBan, error) {
	in.BanType = BanTypeManual
	if in.CreatedBy == "" {
		in.CreatedBy = CreatedBySystem
	}

	var expiry *time.Time
	switch {
	case permanent:
		// nil expiry = permanent
	case durationHours > 0:
		t := s.now().Add(time.Duration(durationHours) * time.Hour)
		expiry = &t
	default:
		return nil, errors.New("security: manual ban requires permanent=true or duration_hours > 0")
	}
	return s.applyBan(ctx, in, func(int) *time.Time { return expiry })
}

// autoExpiry maps an offense count to the automatic ban duration:
// tier = min(offense, 4), TTL = tier hours. A zero or negative TTL means the
// ban never expires; otherwise the expiry is now + tier hours.
func (s *BanService) autoExpiry(offense int) *time.Time {
	hours := defaultTierHours(offense)
	if s.TierHours != nil {
		hours = s.TierHours(offense)
	}
	if hours <= 0 {
		return nil
	}
	t := s.now().Add(time.Duration(hours) * time.Hour)
	return &t
}

// applyBan is the shared ban placement path. resolveExpiry receives the
// offense count (prior bans + 1) and returns the expiry the caller wants.
func (s *BanService) applyBan(ctx context.Context, in BanInput, resolveExpiry func(offense int) *time.Time) (*models.SecurityBan, error) {
	if in.Reason == "" {
		in.Reason = ReasonManual
	}
	slot, err := newBanSlot(in)
	if err != nil {
		return nil, err
	}

	// Allowlist wins: record the attempt, place no ban.
	if s.IsAllowed(slot.addr) {
		logger.Info("security: ban suppressed, address is allowlisted", "ip", slot.key)
		s.recordEvent(string(in.Reason), slot.key, SeverityInfo, EventActionRecorded,
			banEventEvidence(nil, in, map[string]any{"allowlisted": true}), in.RiskScore)
		return nil, nil
	}

	prior, err := s.countByIP(slot.key)
	if err != nil {
		return nil, fmt.Errorf("security: count prior bans for %s: %w", slot.key, err)
	}
	offense := int(prior) + 1 // number of bans including the one being placed

	existing, err := s.findActiveByIP(slot.key)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("security: look up active ban for %s: %w", slot.key, err)
	}
	if existing != nil {
		return s.extendActive(ctx, in, existing, resolveExpiry(offense))
	}

	now := s.now()
	ban := models.SecurityBan{
		IPAddress:         slot.key,
		IsCIDR:            slot.isCIDR,
		ReasonCode:        string(in.Reason),
		ReasonDescription: in.Description,
		Evidence:          evidenceJSON(in.Evidence),
		RiskScore:         in.RiskScore,
		BanType:           in.BanType,
		OffenseCount:      offense,
		CreatedBy:         in.CreatedBy,
		CreatedAt:         now,
		UpdatedAt:         now,
		ExpiresAt:         resolveExpiry(offense),
	}
	if ban.CreatedBy == "" {
		ban.CreatedBy = CreatedBySystem
	}

	if err := s.createBan(&ban); err != nil {
		if isDuplicate(err) {
			// Lost a race with another replica placing the same ban (the
			// partial unique index on active bans rejected the insert):
			// fall back to the extend-keeps-longer path.
			return s.extendAfterRace(ctx, in, slot, ban.ExpiresAt)
		}
		return nil, fmt.Errorf("security: insert ban for %s: %w", slot.key, err)
	}

	s.list.UpsertBan(ban)
	s.publish(ctx, ActionBan, ban)
	s.recordEvent(EventBanCreated, ban.IPAddress, SeverityCritical, EventActionBanned,
		banEventEvidence(&ban, in, nil), ban.RiskScore)
	return &ban, nil
}

// extendActive extends an existing active ban: the new expiry only takes
// hold when it is longer (GREATEST), and the original offense count is kept.
// A ban.created event is recorded only when the ban state actually changed —
// a repeat trigger against an equal-or-shorter active ban is a no-op.
func (s *BanService) extendActive(ctx context.Context, in BanInput, existing *models.SecurityBan, newExpiry *time.Time) (*models.SecurityBan, error) {
	final := longerExpiry(existing.ExpiresAt, newExpiry)
	changed := false
	switch {
	case final == nil:
		// The new ban is permanent; the existing one was timed. Promote it.
		existing.ExpiresAt = nil
		if err := s.saveBan(existing); err != nil {
			return nil, fmt.Errorf("security: make ban %d permanent: %w", existing.ID, err)
		}
		changed = true
	case !sameExpiry(final, existing.ExpiresAt):
		existing.ExpiresAt = final
		if err := s.updateBanExpiry(existing.ID, *final); err != nil {
			return nil, fmt.Errorf("security: extend ban %d: %w", existing.ID, err)
		}
		changed = true
	}

	s.list.UpsertBan(*existing)
	if changed {
		s.publish(ctx, ActionBan, *existing)
		s.recordEvent(EventBanCreated, existing.IPAddress, SeverityCritical, EventActionBanned,
			banEventEvidence(existing, in, nil), existing.RiskScore)
	}
	return existing, nil
}

// extendAfterRace handles the unique-constraint fallback: another replica
// inserted the ban between our lookup and our insert. Re-read the active ban
// and extend it.
func (s *BanService) extendAfterRace(ctx context.Context, in BanInput, slot banSlot, newExpiry *time.Time) (*models.SecurityBan, error) {
	existing, err := s.findActiveByIP(slot.key)
	if err != nil {
		return nil, fmt.Errorf("security: reload active ban for %s after insert conflict: %w", slot.key, err)
	}
	return s.extendActive(ctx, in, existing, newExpiry)
}

// Unban lifts a ban: the row is expired in place (expires_at = now, kept as
// offense history), removed from the snapshot, published and logged as a
// ban.lifted event.
func (s *BanService) Unban(ctx context.Context, id uint, by string) error {
	ban, err := s.findBanByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("security: ban %d not found", id)
		}
		return fmt.Errorf("security: load ban %d: %w", id, err)
	}

	now := s.now()
	if err := s.updateBanExpiry(id, now); err != nil {
		return fmt.Errorf("security: lift ban %d: %w", id, err)
	}
	ban.ExpiresAt = &now

	s.list.RemoveBan(id)
	s.publish(ctx, ActionUnban, *ban)
	s.recordEvent(EventBanLifted, ban.IPAddress, SeverityInfo, EventActionRecorded,
		map[string]any{"ban_id": ban.ID, "reason_code": ban.ReasonCode, "lifted_by": by}, ban.RiskScore)
	return nil
}

// ExtendBan pushes an existing ban's expiry out by hours (counted from the
// later of now and the current expiry). Extending a permanent ban is a
// no-op. No security_event is written: the contract records events for ban
// creation and lifts only.
func (s *BanService) ExtendBan(ctx context.Context, id uint, hours int, by string) error {
	if hours <= 0 {
		return errors.New("security: extend requires a positive number of hours")
	}
	ban, err := s.findBanByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("security: ban %d not found", id)
		}
		return fmt.Errorf("security: load ban %d: %w", id, err)
	}
	if ban.ExpiresAt == nil {
		return nil // already permanent; nothing to extend
	}

	now := s.now()
	base := *ban.ExpiresAt
	if base.Before(now) {
		base = now
	}
	newExpiry := base.Add(time.Duration(hours) * time.Hour)

	if err := s.updateBanExpiry(id, newExpiry); err != nil {
		return fmt.Errorf("security: extend ban %d: %w", id, err)
	}
	ban.ExpiresAt = &newExpiry

	s.list.UpsertBan(*ban) // keeps-longer: the pushed-out expiry wins the slot
	s.publish(ctx, ActionBan, *ban)
	return nil
}

// IsBlocked reports whether the address is currently banned, with the
// matching ban. Snapshot only — no Redis or database on the hot path.
func (s *BanService) IsBlocked(ip netip.Addr) (bool, *models.SecurityBan) {
	return s.list.Blocked(ip)
}

// IsAllowed reports whether the address is exempt from bans (allowlist or
// loopback). Snapshot only.
func (s *BanService) IsAllowed(ip netip.Addr) bool {
	return s.list.Allowed(ip)
}

// Allow adds an allowlist entry and propagates the change: local resync plus
// a published "allow" message so other replicas resync too. The admin
// handler calls this after validating the IP/CIDR; validation is repeated
// here so an unparseable value never reaches the snapshot.
func (s *BanService) Allow(ctx context.Context, entry models.SecurityAllowlist) error {
	if err := validateAllowValue(entry.IPCidr); err != nil {
		return err
	}
	if err := s.allowRepo.Create(&entry); err != nil {
		return fmt.Errorf("security: save allowlist entry: %w", err)
	}
	if err := s.prop.ResyncFromDB(ctx); err != nil {
		logger.Warn("security: local allowlist refresh failed", "error", err)
	}
	s.publish(ctx, ActionAllow, entry)
	return nil
}

// Disallow removes an allowlist entry and propagates the change the same way
// Allow does.
func (s *BanService) Disallow(ctx context.Context, id uint) error {
	if err := s.allowRepo.Delete(id); err != nil {
		return fmt.Errorf("security: delete allowlist entry %d: %w", id, err)
	}
	if err := s.prop.ResyncFromDB(ctx); err != nil {
		logger.Warn("security: local allowlist refresh failed", "error", err)
	}
	s.publish(ctx, ActionDisallow, map[string]any{"id": id})
	return nil
}

// banSlot is the normalized ban placement target: the canonical key stored in
// the database plus the address used for allowlist checks.
type banSlot struct {
	key    string
	addr   netip.Addr
	prefix netip.Prefix
	isCIDR bool
}

// newBanSlot normalizes a BanInput into its database/snapshot key: the unmapped
// address for single-IP bans, the canonical masked prefix for CIDR bans.
func newBanSlot(in BanInput) (banSlot, error) {
	if in.CIDR != nil {
		p := canonicalPrefix(*in.CIDR)
		return banSlot{key: p.String(), addr: p.Addr(), prefix: p, isCIDR: true}, nil
	}
	addr := canonicalIP(in.IP)
	if !addr.IsValid() {
		return banSlot{}, errors.New("security: ban requires a valid IP address or CIDR")
	}
	return banSlot{key: addr.String(), addr: addr}, nil
}

// validateAllowValue reports whether an allowlist value is a parseable IP
// address or CIDR.
func validateAllowValue(value string) error {
	if value == "" {
		return errors.New("security: allowlist value is empty")
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return nil
	}
	if _, err := netip.ParsePrefix(value); err == nil {
		return nil
	}
	return fmt.Errorf("security: %q is not a valid IP address or CIDR", value)
}

// longerExpiry returns whichever expiry blocks longer: nil (permanent) beats
// a timed expiry, a later timed expiry beats an earlier one, ties keep a.
func longerExpiry(a, b *time.Time) *time.Time {
	switch {
	case a == nil || b == nil:
		return nil // either permanent → permanent
	case a.After(*b):
		return a
	default:
		return b
	}
}

// sameExpiry reports whether two expiries denote the same instant (both nil
// counts as the same).
func sameExpiry(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// isDuplicate reports a unique-constraint violation — the partial unique
// index on active bans rejecting a second active ban for the same IP. It
// reads the driver error directly rather than gorm.ErrDuplicatedKey, which
// only exists when the connection is opened with TranslateError (same
// approach as the template repository).
func isDuplicate(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// banEventEvidence builds the evidence for a ban lifecycle event: the
// triggering input's evidence plus the ban's placement details. extra
// overrides the details when present (used for suppressed bans).
func banEventEvidence(ban *models.SecurityBan, in BanInput, extra map[string]any) map[string]any {
	ev := map[string]any{}
	for k, v := range in.Evidence {
		ev[k] = v
	}
	if in.Rule != "" {
		ev["rule"] = in.Rule
	}
	if extra != nil {
		for k, v := range extra {
			ev[k] = v
		}
		return ev
	}
	ev["reason_code"] = string(in.Reason)
	ev["ban_type"] = in.BanType
	if ban != nil {
		ev["offense_count"] = ban.OffenseCount
		ev["ban_id"] = ban.ID
		if ban.ExpiresAt == nil {
			ev["permanent"] = true
		} else {
			ev["expires_at"] = ban.ExpiresAt.UTC().Format(time.RFC3339)
		}
	}
	return ev
}

// evidenceJSON marshals an evidence map for the jsonb columns; a nil map or a
// marshaling failure yields "{}" (the column default).
func evidenceJSON(ev map[string]any) string {
	if len(ev) == 0 {
		return "{}"
	}
	b, err := json.Marshal(ev)
	if err != nil {
		logger.Warn("security: failed to marshal evidence; storing empty object", "error", err)
		return "{}"
	}
	return string(b)
}

// recordEvent writes a security_events row; failures are logged, never
// returned — event loss must not fail a ban placement.
func (s *BanService) recordEvent(eventType, sourceIP, severity, action string, evidence map[string]any, riskScore int) {
	event := models.SecurityEvent{
		SourceIP:    sourceIP,
		EventType:   eventType,
		Severity:    severity,
		Evidence:    evidenceJSON(evidence),
		RiskScore:   riskScore,
		ActionTaken: action,
		CreatedAt:   s.now(),
	}
	if err := s.createEvent(&event); err != nil {
		logger.Error("security: failed to record security event", "event_type", eventType, "ip", sourceIP, "error", err)
	}
}

// publish fans a ban change out to other replicas; failures are logged, never
// returned — the change is already durable in the database and live in the
// local snapshot.
func (s *BanService) publish(ctx context.Context, action string, payload any) {
	if s.prop == nil {
		return
	}
	if err := s.prop.Publish(ctx, action, payload); err != nil {
		logger.Warn("security: failed to publish ban change", "action", action, "error", err)
	}
}
