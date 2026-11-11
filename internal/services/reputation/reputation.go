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

// Package reputation builds the IP-pool reputation surface (OB-6 W3, ADR-007
// decision 7): it aggregates mta_delivery_attempts (transport outcomes per
// source IP × recipient domain) and email_events (recipient-side sent/
// bounced/complained per sender domain) into per-day rollups
// (ip_reputation_rollups) and reads them back as rates + trend for the R-07
// domain health report and the admin deployment view.
//
// Event attribution: mta_delivery_attempts rows carry the outbound source IP
// (SourceIP, stamped by the executor) and, through the emails join, the
// sender domain — so transport counters are truly per (ip, domain).
// Recipient-side events have no source IP (the bounce/complaint arrives for
// the message, not for one IP), so they land on the domain-wide rollup row
// (IP = "") and the report joins them onto every IP row of the same domain by
// (domain, day). Rates never double-count: transport rates divide IP-attributed
// counters; bounce/complaint rates divide domain-wide counters once.
package reputation

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/goposta/posta/internal/services/warmup"
	"gorm.io/gorm"
)

// Reputation grades (obvious thresholds, documented in the report guidance).
const (
	GradeGood    = "good"
	GradeWarning = "warning"
	GradePoor    = "poor"
	GradeUnknown = "unknown" // no data in the window — nothing measured yet
)

// Rollup rate thresholds in percent. Bounce/complaint rates are
// recipient-side (events/sent); hard-fail and deferred rates are transport
// (attempts/attempts). The values follow the widely-published receiver
// guidance (~2% bounces healthy, ~0.1% complaints healthy; well above that
// is trouble).
const (
	poorBounceRatePct      = 8.0
	warnBounceRatePct      = 3.0
	poorComplaintRatePct   = 0.5
	warnComplaintRatePct   = 0.2
	poorHardFailRatePct    = 10.0
	warnHardFailRatePct    = 5.0
	warnDeferredRatePct    = 20.0
)

// DefaultReportWindow is the reputation lookback when a caller does not name
// one: two weeks of daily rollups is enough trend without dragging the table.
const DefaultReportWindow = 14 * 24 * time.Hour

// DomainFilter narrows a raw-source read. WorkspaceID nil = unrestricted
// (the cron rollup reads deployment-wide; handlers always pass the
// workspace); Domain "" = all domains. There is deliberately no fail-closed
// "no filter" mode — the report and handler callers always bind a scope.
type DomainFilter struct {
	WorkspaceID *uint
	Domain      string
}

// AttemptOutcomeRow is one raw transport outcome feeding the rollups.
type AttemptOutcomeRow struct {
	WorkspaceID *uint
	SourceIP    string
	Domain      string
	Outcome     string
	At          time.Time
}

// RecipientEventRow is one raw recipient-side event feeding the rollups
// (sent/bounced/complained only — the source query restricts the types).
type RecipientEventRow struct {
	WorkspaceID *uint
	Domain      string
	Type        string
	At          time.Time
}

// RawSource reads the raw rows that feed the rollups. The gorm implementation
// is gormRawSource (postgres SQL, shared sender-domain expression); tests
// inject fakes so the aggregation math is verified without a database.
type RawSource interface {
	AttemptOutcomes(from, to time.Time, f DomainFilter) ([]AttemptOutcomeRow, error)
	RecipientEvents(from, to time.Time, f DomainFilter) ([]RecipientEventRow, error)
}

// rollupStore persists and reads rollup rows (repositories.IPPoolRepository
// in production; a fake in tests).
type rollupStore interface {
	UpsertRollups(rows []models.IPReputationRollup) error
	RollupsForDomain(scope repositories.ResourceScope, domain string, from, to time.Time) ([]models.IPReputationRollup, error)
}

// Service assembles and reads the reputation surface.
type Service struct {
	db    *gorm.DB
	pools *repositories.IPPoolRepository
	store rollupStore
	// raw is the raw-row source; a field so tests inject a fake instead of a
	// database.
	raw RawSource
	// now is overridable in tests.
	now func() time.Time
}

func NewService(db *gorm.DB) *Service {
	pools := repositories.NewIPPoolRepository(db)
	return &Service{
		db:    db,
		pools: pools,
		store: pools,
		raw:   &gormRawSource{db: db},
		now:   time.Now,
	}
}

// SetRawSource overrides the raw-row source (tests).
func (s *Service) SetRawSource(r RawSource) {
	if r != nil {
		s.raw = r
	}
}

// SetNow overrides the clock (tests).
func (s *Service) SetNow(fn func() time.Time) {
	if fn != nil {
		s.now = fn
	}
}

// DayStart truncates to the UTC day bucket the rollups use.
func DayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// AggregateDay folds one day's raw rows into rollup rows, grouped by
// (workspace, ip, domain) for transport outcomes and (workspace, domain) for
// recipient events. Pure so the math is unit-testable; rows outside the day
// are ignored, and the output is sorted for deterministic upserts.
func AggregateDay(day time.Time, attempts []AttemptOutcomeRow, events []RecipientEventRow) []models.IPReputationRollup {
	day = DayStart(day)
	next := day.Add(24 * time.Hour)

	type key struct {
		ws     string
		ip     string
		domain string
	}
	transport := map[key]*models.IPReputationRollup{}
	eventsByKey := map[key]*models.IPReputationRollup{}

	rollup := func(k key, ip string) *models.IPReputationRollup {
		var wsID *uint
		if k.ws != "" {
			id := mustParseUint(k.ws)
			wsID = &id
		}
		return &models.IPReputationRollup{
			WorkspaceID: wsID,
			IP:          ip,
			Domain:      k.domain,
			Day:         day,
		}
	}

	for _, a := range attempts {
		at := DayStart(a.At)
		if at.Before(day) || !at.Before(next) {
			continue
		}
		k := key{ws: wsKey(a.WorkspaceID), ip: strings.TrimSpace(a.SourceIP), domain: strings.ToLower(strings.TrimSpace(a.Domain))}
		r, ok := transport[k]
		if !ok {
			r = rollup(k, k.ip)
			transport[k] = r
		}
		r.AttemptsTotal++
		switch a.Outcome {
		case models.MTAAttemptDelivered:
			r.AttemptsDelivered++
		case models.MTAAttemptTransientFailure:
			r.AttemptsTransient++
		case models.MTAAttemptPermanentFailure:
			r.AttemptsPermanent++
		case models.MTAAttemptDeferredBudget:
			r.AttemptsDeferred++
		case models.MTAAttemptDNSFailure:
			r.AttemptsDNSFail++
		case models.MTAAttemptRefusedUnverifiedSender:
			r.AttemptsRefused++
		}
	}

	for _, e := range events {
		at := DayStart(e.At)
		if at.Before(day) || !at.Before(next) {
			continue
		}
		// Domain-wide row: IP stays "".
		k := key{ws: wsKey(e.WorkspaceID), ip: "", domain: strings.ToLower(strings.TrimSpace(e.Domain))}
		r, ok := eventsByKey[k]
		if !ok {
			r = rollup(k, "")
			eventsByKey[k] = r
		}
		switch e.Type {
		case models.EmailEventSent:
			r.EventsSent++
		case models.EmailEventBounced:
			r.EventsBounced++
		case models.EmailEventComplained:
			r.EventsComplained++
		}
	}

	out := make([]models.IPReputationRollup, 0, len(transport)+len(eventsByKey))
	for _, r := range transport {
		out = append(out, *r)
	}
	for _, r := range eventsByKey {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if wsLess(out[i].WorkspaceID, out[j].WorkspaceID) {
			return true
		}
		if wsLess(out[j].WorkspaceID, out[i].WorkspaceID) {
			return false
		}
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].IP < out[j].IP
	})
	return out
}

// wsKey renders the workspace part of the grouping key ("g" for nil).
func wsKey(id *uint) string {
	if id == nil {
		return "g"
	}
	return fmt.Sprint(*id)
}

func wsLess(a, b *uint) bool {
	if a == nil && b == nil {
		return false
	}
	if a == nil {
		return true
	}
	if b == nil {
		return false
	}
	return *a < *b
}

func mustParseUint(s string) uint {
	var n uint
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + uint(s[i]-'0')
	}
	return n
}

// RollupDay aggregates and upserts one day's rollups across all workspaces
// (the cron's daily pass; also usable for manual backfills). Returns the
// number of rollup rows written.
func (s *Service) RollupDay(day time.Time) (int, error) {
	return s.rollupWindow(DayStart(day), DayStart(day).Add(24*time.Hour), DomainFilter{})
}

// RefreshDomainDay aggregates and upserts one workspace+domain's rollups for
// one day (the on-read freshness pass behind the R-07 report; cheap because
// the source query is filtered to the one domain). Returns rows written.
func (s *Service) RefreshDomainDay(scope repositories.ResourceScope, domain string, day time.Time) (int, error) {
	f := DomainFilter{Domain: strings.ToLower(strings.TrimSpace(domain))}
	if scope.WorkspaceID != nil {
		f.WorkspaceID = scope.WorkspaceID
	}
	return s.rollupWindow(DayStart(day), DayStart(day).Add(24*time.Hour), f)
}

func (s *Service) rollupWindow(from, to time.Time, f DomainFilter) (int, error) {
	attempts, err := s.raw.AttemptOutcomes(from, to, f)
	if err != nil {
		return 0, fmt.Errorf("reputation: read attempts: %w", err)
	}
	events, err := s.raw.RecipientEvents(from, to, f)
	if err != nil {
		return 0, fmt.Errorf("reputation: read events: %w", err)
	}
	rows := aggregateWindow(from, to, attempts, events)
	if err := s.store.UpsertRollups(rows); err != nil {
		return 0, fmt.Errorf("reputation: upsert rollups: %w", err)
	}
	return len(rows), nil
}

// aggregateWindow is AggregateDay generalized to a [from, to) window: rows
// are bucketed by their UTC day. Used by the rollup paths; the day functions
// stay separate so the pure per-day math keeps its simple signature.
func aggregateWindow(from, to time.Time, attempts []AttemptOutcomeRow, events []RecipientEventRow) []models.IPReputationRollup {
	buckets := map[time.Time]struct {
		attempts []AttemptOutcomeRow
		events   []RecipientEventRow
	}{}
	for _, a := range attempts {
		d := DayStart(a.At)
		if d.Before(from) || !d.Before(to) {
			continue
		}
		b := buckets[d]
		b.attempts = append(b.attempts, a)
		buckets[d] = b
	}
	for _, e := range events {
		d := DayStart(e.At)
		if d.Before(from) || !d.Before(to) {
			continue
		}
		b := buckets[d]
		b.events = append(b.events, e)
		buckets[d] = b
	}
	days := make([]time.Time, 0, len(buckets))
	for d := range buckets {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	var out []models.IPReputationRollup
	for _, d := range days {
		b := buckets[d]
		out = append(out, AggregateDay(d, b.attempts, b.events)...)
	}
	return out
}

// ---------- read surface ----------

// DayPoint is one day of the trend series: transport volume/outcomes for the
// IP plus the domain-wide recipient events.
type DayPoint struct {
	Day        time.Time `json:"day"`
	Attempts   int64     `json:"attempts"`
	Delivered  int64     `json:"delivered"`
	Permanent  int64     `json:"permanent"`
	Deferred   int64     `json:"deferred"`
	Bounced    int64     `json:"bounced"`
	Complained int64     `json:"complained"`
}

// IPReputation is the per-source-IP reputation view for one recipient domain
// over the report window.
type IPReputation struct {
	IP          string     `json:"ip"`
	Domain      string     `json:"domain"`
	Attempts    int64      `json:"attempts"`
	Delivered   int64      `json:"delivered"`
	Transient   int64      `json:"transient"`
	Permanent   int64      `json:"permanent"`
	Deferred    int64      `json:"deferred"`
	DNSFailure  int64      `json:"dns_failure"`
	Refused     int64      `json:"refused"`
	Sent        int64      `json:"sent"`
	Bounced     int64      `json:"bounced"`
	Complained  int64      `json:"complained"`
	BounceRate  float64    `json:"bounce_rate"`     // percent of sent (recipient events)
	ComplaintRate float64  `json:"complaint_rate"`  // percent of sent
	HardFailRate  float64  `json:"hard_fail_rate"`  // percent of attempts (transport 5xx)
	DeferredRate  float64  `json:"deferred_rate"`   // percent of attempts
	Grade       string     `json:"grade"`
	Trend       []DayPoint `json:"trend"`
}

// DomainReport is the reputation surface for one sender domain.
type DomainReport struct {
	Domain      string         `json:"domain"`
	WindowDays  int            `json:"window_days"`
	Since       time.Time      `json:"since"`
	IPs         []IPReputation `json:"ips"`
	GeneratedAt time.Time      `json:"generated_at"`
	// LimitationNote states honestly what the surface does not see: relay-mode
	// mail never touches mta_delivery_attempts, so its transport side is
	// invisible here; recipient events cover every mode.
	LimitationNote string `json:"limitation_note"`
}

// LimitationNoteText is the shared honesty note on every report.
const LimitationNoteText = "Transport counters cover direct-to-MX deliveries only (relay-mode mail does not write delivery attempts); bounced/complained events cover every delivery mode."

// DomainReputationReport builds the report from stored rollups. today is
// refreshed first (single-domain, idempotent) so the surface is live for the
// current day; earlier days come from the daily cron pass.
func (s *Service) DomainReputationReport(scope repositories.ResourceScope, domain string, window time.Duration) (*DomainReport, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if window <= 0 {
		window = DefaultReportWindow
	}
	now := s.now()
	since := DayStart(now.Add(-window))
	to := DayStart(now).Add(24 * time.Hour) // include today's partial bucket

	// Live freshness for today only — cheap (one-domain filter) and
	// idempotent (unique-key upsert). Failure is non-fatal: a stale report
	// beats a failed one.
	_, _ = s.RefreshDomainDay(scope, domain, now)

	rows, err := s.store.RollupsForDomain(scope, domain, since, to)
	if err != nil {
		return nil, fmt.Errorf("reputation: read rollups: %w", err)
	}
	return BuildDomainReport(domain, window, since, now, rows), nil
}

// BuildDomainReport assembles the read surface from stored rollup rows.
// Pure so the shaping is unit-testable against synthetic rows.
func BuildDomainReport(domain string, window time.Duration, since, generatedAt time.Time, rows []models.IPReputationRollup) *DomainReport {
	rep := &DomainReport{
		Domain:         domain,
		WindowDays:     int(window / (24 * time.Hour)),
		Since:          since,
		IPs:            []IPReputation{},
		GeneratedAt:    generatedAt,
		LimitationNote: LimitationNoteText,
	}

	// Domain-wide event row (IP "") supplies sent/bounced/complained per day.
	eventDays := map[time.Time]*models.IPReputationRollup{}
	for i := range rows {
		if rows[i].IP == "" {
			eventDays[DayStart(rows[i].Day)] = &rows[i]
		}
	}

	// Transport rows group per source IP.
	type ipAgg struct {
		rep   IPReputation
		days  map[time.Time]*IPReputation
	}
	byIP := map[string]*ipAgg{}
	for i := range rows {
		r := &rows[i]
		if r.IP == "" {
			continue
		}
		agg, ok := byIP[r.IP]
		if !ok {
			agg = &ipAgg{rep: IPReputation{
				IP:     r.IP,
				Domain: domain,
				Grade:  GradeUnknown,
			}, days: map[time.Time]*IPReputation{}}
			byIP[r.IP] = agg
		}
		agg.rep.Attempts += r.AttemptsTotal
		agg.rep.Delivered += r.AttemptsDelivered
		agg.rep.Transient += r.AttemptsTransient
		agg.rep.Permanent += r.AttemptsPermanent
		agg.rep.Deferred += r.AttemptsDeferred
		agg.rep.DNSFailure += r.AttemptsDNSFail
		agg.rep.Refused += r.AttemptsRefused
		d := DayStart(r.Day)
		dr, ok := agg.days[d]
		if !ok {
			dr = &IPReputation{}
			agg.days[d] = dr
		}
		dr.Attempts += r.AttemptsTotal
		dr.Delivered += r.AttemptsDelivered
		dr.Permanent += r.AttemptsPermanent
		dr.Deferred += r.AttemptsDeferred
	}

	for ip, agg := range byIP {
		// Join the domain-wide event row onto every IP row of the domain —
		// the events are not per-IP, so every IP row shows the domain's
		// recipient-side truth (never double-counted: it is read once here).
		for d, ev := range eventDays {
			agg.rep.Sent += ev.EventsSent
			agg.rep.Bounced += ev.EventsBounced
			agg.rep.Complained += ev.EventsComplained
			dp, ok := agg.days[d]
			if !ok {
				dp = &IPReputation{}
				agg.days[d] = dp
			}
			dp.Bounced += ev.EventsBounced
			dp.Complained += ev.EventsComplained
		}
		_ = ip

		agg.rep.BounceRate = ratePct(agg.rep.Bounced, agg.rep.Sent)
		agg.rep.ComplaintRate = ratePct(agg.rep.Complained, agg.rep.Sent)
		agg.rep.HardFailRate = ratePct(agg.rep.Permanent, agg.rep.Attempts)
		agg.rep.DeferredRate = ratePct(agg.rep.Deferred, agg.rep.Attempts)
		agg.rep.Grade = GradeOf(agg.rep)

		// Trend: one point per day that has any data, sorted by day.
		days := make([]time.Time, 0, len(agg.days))
		for d := range agg.days {
			days = append(days, d)
		}
		sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
		agg.rep.Trend = make([]DayPoint, 0, len(days))
		for _, d := range days {
			dr := agg.days[d]
			agg.rep.Trend = append(agg.rep.Trend, DayPoint{
				Day:        d,
				Attempts:   dr.Attempts,
				Delivered:  dr.Delivered,
				Permanent:  dr.Permanent,
				Deferred:   dr.Deferred,
				Bounced:    dr.Bounced,
				Complained: dr.Complained,
			})
		}
		rep.IPs = append(rep.IPs, agg.rep)
	}

	sort.Slice(rep.IPs, func(i, j int) bool { return rep.IPs[i].IP < rep.IPs[j].IP })
	return rep
}

// GradeOf classifies an aggregate into good/warning/poor/unknown from the
// documented thresholds. No data → unknown.
func GradeOf(r IPReputation) string {
	if r.Attempts == 0 && r.Sent == 0 {
		return GradeUnknown
	}
	if r.BounceRate >= poorBounceRatePct ||
		r.ComplaintRate >= poorComplaintRatePct ||
		r.HardFailRate >= poorHardFailRatePct {
		return GradePoor
	}
	if r.BounceRate >= warnBounceRatePct ||
		r.ComplaintRate >= warnComplaintRatePct ||
		r.HardFailRate >= warnHardFailRatePct ||
		r.DeferredRate >= warnDeferredRatePct {
		return GradeWarning
	}
	return GradeGood
}

func ratePct(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// ---------- pool / warm-up views (the domain-page surface) ----------

// PoolView is the domain-page summary of the domain's assigned pool.
type PoolView struct {
	PoolID      uint     `json:"pool_id"`
	Name        string   `json:"name"`
	Scope       string   `json:"scope"`        // shared | workspace
	Strategy    string   `json:"strategy"`
	MemberIPs   []string `json:"member_ips"`   // enabled members (the selection candidates)
	MemberCount int      `json:"member_count"` // all members, enabled or not
}

// WarmupView is the domain-page summary of the domain's active warm-up ramp,
// resolved with today's cap so the UI shows where in the ramp the domain is.
type WarmupView struct {
	Active          bool      `json:"active"`
	StartVolume     int       `json:"start_volume"`
	DailyMultiplier float64   `json:"daily_multiplier"`
	Days            int       `json:"days"`
	StartedAt       time.Time `json:"started_at"`
	DayIndex        int       `json:"day_index"`
	DailyCap        int       `json:"daily_cap"`  // today's allowed volume; 0 = ramp complete
	HourlyCap       int       `json:"hourly_cap"` // the enforced hourly allowance
}

// DomainPoolView resolves the pool summary for a sending domain.
func (s *Service) DomainPoolView(d *models.Domain) (*PoolView, error) {
	if d == nil || d.PoolID == nil {
		return nil, nil
	}
	pool, err := s.pools.FindPoolByID(*d.PoolID)
	if err != nil {
		return nil, fmt.Errorf("reputation: read pool: %w", err)
	}
	members, err := s.pools.PoolMembers(*d.PoolID)
	if err != nil {
		return nil, fmt.Errorf("reputation: read pool members: %w", err)
	}
	ips := make([]string, 0, len(members))
	enabled := 0
	for _, m := range members {
		if m.Enabled {
			ips = append(ips, m.IP)
			enabled++
		}
	}
	return &PoolView{
		PoolID:      pool.ID,
		Name:        pool.Name,
		Scope:       pool.Scope(),
		Strategy:    pool.Strategy,
		MemberIPs:   ips,
		MemberCount: len(members),
	}, nil
}

// DomainWarmupView resolves the warm-up summary for a sending domain as of
// now (nil when no schedule is active). The ramp math comes from the shared
// internal/services/warmup leaf package — the same functions the delivery
// path enforces.
func (s *Service) DomainWarmupView(d *models.Domain, now time.Time) (*WarmupView, error) {
	if d == nil {
		return nil, nil
	}
	row, err := s.pools.ActiveWarmupForDomain(d.ID)
	if err != nil {
		return nil, fmt.Errorf("reputation: read warmup: %w", err)
	}
	if row == nil {
		return nil, nil
	}
	ws := &warmup.Schedule{
		StartVolume:     row.StartVolume,
		DailyMultiplier: row.DailyMultiplier,
		Days:            row.Days,
		StartedAt:       row.StartedAt,
	}
	return &WarmupView{
		Active:          true,
		StartVolume:     row.StartVolume,
		DailyMultiplier: row.DailyMultiplier,
		Days:            row.Days,
		StartedAt:       row.StartedAt,
		DayIndex:        warmup.DayIndex(ws, now),
		DailyCap:        warmup.DailyCap(ws, now),
		HourlyCap:       warmup.HourlyCap(ws, now),
	}, nil
}

// ---------- gorm raw source ----------

// gormRawSource reads the raw rows with the same postgres SQL shapes the
// domain health stats use (emails join for the sender domain, shared
// sender-domain expression).
type gormRawSource struct {
	db *gorm.DB
}

func (g *gormRawSource) AttemptOutcomes(from, to time.Time, f DomainFilter) ([]AttemptOutcomeRow, error) {
	var rows []AttemptOutcomeRow
	q := g.db.Table("mta_delivery_attempts").
		Select("mta_delivery_attempts.workspace_id, mta_delivery_attempts.source_ip, "+
			repositories.SenderDomainExpr+" AS domain, mta_delivery_attempts.outcome, mta_delivery_attempts.created_at AS at").
		Joins("JOIN emails ON emails.id = mta_delivery_attempts.email_id").
		Where("mta_delivery_attempts.created_at >= ? AND mta_delivery_attempts.created_at < ?", from, to)
	if f.WorkspaceID != nil {
		q = q.Where("mta_delivery_attempts.workspace_id = ?", *f.WorkspaceID)
	}
	if f.Domain != "" {
		q = q.Where(repositories.SenderDomainExpr+" = ?", f.Domain)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (g *gormRawSource) RecipientEvents(from, to time.Time, f DomainFilter) ([]RecipientEventRow, error) {
	var rows []RecipientEventRow
	q := g.db.Table("email_events").
		Select("email_events.workspace_id, "+repositories.SenderDomainExpr+" AS domain, email_events.type, email_events.occurred_at AS at").
		Joins("JOIN emails ON emails.uuid = email_events.email_uuid").
		Where("email_events.type IN ?", []string{models.EmailEventSent, models.EmailEventBounced, models.EmailEventComplained}).
		Where("email_events.occurred_at >= ? AND email_events.occurred_at < ?", from, to)
	if f.WorkspaceID != nil {
		q = q.Where("email_events.workspace_id = ?", *f.WorkspaceID)
	}
	if f.Domain != "" {
		q = q.Where(repositories.SenderDomainExpr+" = ?", f.Domain)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
