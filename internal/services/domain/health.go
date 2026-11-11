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

package domain

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/goposta/posta/internal/services/reputation"
	"gorm.io/gorm"
)

// HealthService assembles the per-domain deliverability health report (R-07):
// the stored SPF/DKIM/DMARC check state, the managed DKIM signing status (R-02),
// recent bounce and complaint rates for mail sent from the domain, and the DNS
// records to set when something is off. One view answers "why is my mail
// landing in spam?" without hopping between the logs, the domain page, and the
// signing-key page.
type HealthService struct {
	db      *gorm.DB
	keyRepo *repositories.DomainSigningKeyRepository
	// verify re-runs the live DNS checks; a field so Diagnose is testable.
	verify func(*models.Domain) (*VerificationResult, error)
	now    func() time.Time
	// stats reads the sending numbers; a field so tests inject a fake instead
	// of a database.
	stats SendingStatsReader
	// lookupPTR resolves reverse-DNS names for the outbound bind IP
	// (OB-1 PTR guidance slice, ADR-001 §7); a field so tests inject a fake.
	lookupPTR func(ip string) ([]string, error)
	// ehloDomain is the process EHLO name (POSTA_MTA_EHLO_NAME); the PTR
	// match check targets it, falling back to the sending domain.
	ehloDomain string
	// reputation supplies the OB-6 W3 additions to the report (per-IP
	// reputation rollups, the domain's pool, the active warm-up ramp); a
	// field so tests inject a fake. nil = the sections are omitted.
	reputation ReputationReader
}

// ReputationReader supplies the OB-6 W3 (ADR-007 decision 7) extensions to
// the R-07 report: the per-source-IP reputation rollups for the domain, the
// pool the domain sends from, and its active warm-up ramp. Implemented by
// the reputation service; nil keeps the report shape unchanged.
type ReputationReader interface {
	DomainReputationReport(scope repositories.ResourceScope, domain string, window time.Duration) (*reputation.DomainReport, error)
	DomainPoolView(d *models.Domain) (*reputation.PoolView, error)
	DomainWarmupView(d *models.Domain, now time.Time) (*reputation.WarmupView, error)
}

// SetReputationReader wires the reputation/pool/warm-up sections (OB-6 W3).
func (s *HealthService) SetReputationReader(r ReputationReader) { s.reputation = r }

func NewHealthService(db *gorm.DB, keyRepo *repositories.DomainSigningKeyRepository) *HealthService {
	return &HealthService{
		db:        db,
		keyRepo:   keyRepo,
		verify:    Verify,
		now:       time.Now,
		stats:     &gormSendingStats{db: db},
		lookupPTR: net.LookupAddr,
	}
}

// SetEhloDomain sets the process EHLO name the PTR match check compares
// against (guidance only — never enforced at delivery time).
func (s *HealthService) SetEhloDomain(d string) { s.ehloDomain = d }

// SetPTRLookup overrides the reverse-DNS resolver (tests).
func (s *HealthService) SetPTRLookup(fn func(ip string) ([]string, error)) {
	if fn != nil {
		s.lookupPTR = fn
	}
}

// SetStats overrides the sending-stats source (tests).
func (s *HealthService) SetStats(r SendingStatsReader) { s.stats = r }

// DefaultHealthWindow is the lookback window for bounce/complaint rates.
const DefaultHealthWindow = 30 * 24 * time.Hour

// SendingStatsReader returns the sent/bounced/complained counts for one sender
// domain over a window. The gorm implementation queries emails by sender domain
// and joins bounces and email_events; a fake backs the unit tests.
type SendingStatsReader interface {
	SendingStats(scope repositories.ResourceScope, senderDomain string, since time.Time) (SendingStats, error)
}

// SendingStats is the raw count triple behind the rates.
type SendingStats struct {
	Sent       int64
	Bounced    int64
	Complained int64
}

// CheckResult is one auth check with the operator guidance when it fails.
type CheckResult struct {
	Verified bool   `json:"verified"`
	Record   string `json:"record,omitempty"`
	Guidance string `json:"guidance,omitempty"`
}

// SigningStatus reports the managed DKIM signing key (R-02) state for the
// domain: whether outbound mail from it is actually being signed.
type SigningStatus struct {
	Enabled   bool   `json:"enabled"`
	Selector  string `json:"selector,omitempty"`
	Algorithm string `json:"algorithm,omitempty"`
	DNSState  string `json:"dns_state,omitempty"`
	Guidance  string `json:"guidance,omitempty"`
}

// SendingHealth is the recent deliverability record of mail from this domain.
type SendingHealth struct {
	Sent          int64     `json:"sent"`
	Bounced       int64     `json:"bounced"`
	Complained    int64     `json:"complained"`
	BounceRate    float64   `json:"bounce_rate"`    // percent of sent
	ComplaintRate float64   `json:"complaint_rate"` // percent of sent
	Since         time.Time `json:"since"`
}

// HealthReport is the R-07 domain health page payload.
type HealthReport struct {
	DomainID          uint          `json:"domain_id"`
	Domain            string        `json:"domain"`
	OwnershipVerified bool          `json:"ownership_verified"`
	SPF               CheckResult   `json:"spf"`
	DKIM              CheckResult   `json:"dkim"`
	DMARC             CheckResult   `json:"dmarc"`
	Signing           SigningStatus `json:"signing"`
	Sending           SendingHealth `json:"sending"`
	Records           *DNSRecords   `json:"records,omitempty"`
	// PTR surfaces reverse-DNS guidance for the outbound bind IP (OB-1,
	// ADR-001 §7 — guidance only, never enforced at delivery). nil when the
	// domain is not in direct mode.
	PTR *PTRGuidance `json:"ptr,omitempty"`
	// Reputation/Pool/Warmup are the OB-6 W3 additions (ADR-007 decision 7):
	// the per-source-IP reputation rollups (bounce/deferred/complaint rates
	// + trend) for the domain, the pool it sends from, and its active
	// warm-up ramp. nil when the reputation reader is absent (or the domain
	// has no pool / no active ramp for those sections).
	Reputation *reputation.DomainReport `json:"reputation,omitempty"`
	Pool       *reputation.PoolView     `json:"pool,omitempty"`
	Warmup     *reputation.WarmupView   `json:"warmup,omitempty"`
	CheckedAt  time.Time                `json:"checked_at"`
}

// PTR match verdicts for PTRGuidance.Match.
const (
	PTRMatchUnbound  = "unbound"  // direct mode but no outbound bind IP configured
	PTRMatchMissing  = "missing"  // the bound IP resolves to no PTR name
	PTRMatchMatch    = "match"    // PTR name matches the EHLO/sending identity
	PTRMatchMismatch = "mismatch" // PTR name differs from the EHLO identity
)

// PTRGuidance surfaces reverse-DNS guidance for the domain's outbound bind
// IP (SF-32 partial slice): the PTR name the IP resolves to, whether it
// matches the EHLO/sending domain, and the operator instruction when it
// doesn't. Guidance text only in this phase.
type PTRGuidance struct {
	BoundIP  string `json:"bound_ip,omitempty"`
	PTRName  string `json:"ptr_name,omitempty"`
	Match    string `json:"match"`
	Guidance string `json:"guidance,omitempty"`
}

// Report assembles the health view from stored state — no DNS traffic. The
// auth booleans are whatever the last verification recorded.
func (s *HealthService) Report(d *models.Domain, window time.Duration) (*HealthReport, error) {
	return s.report(d, window, nil), nil
}

// Diagnose re-runs the live DNS checks first (the health page's "diagnose"
// button) and reports on the fresh results. Persisting them stays the domain
// verify endpoint's job.
func (s *HealthService) Diagnose(d *models.Domain, window time.Duration) (*HealthReport, error) {
	result, err := s.verify(d)
	if err != nil {
		return nil, fmt.Errorf("domain health: DNS check failed: %w", err)
	}
	return s.report(d, window, result), nil
}

func (s *HealthService) report(d *models.Domain, window time.Duration, live *VerificationResult) *HealthReport {
	if window <= 0 {
		window = DefaultHealthWindow
	}
	since := s.now().Add(-window)

	ownership := d.OwnershipVerified
	spfOK, spfRecord := d.SPFVerified, ""
	dkimOK, dkimRecord := d.DKIMVerified, ""
	dmarcOK, dmarcRecord := d.DMARCVerified, ""
	if live != nil {
		ownership, spfOK, spfRecord = live.OwnershipVerified, live.SPFVerified, live.SPFRecord
		dkimOK, dkimRecord = live.DKIMVerified, live.DKIMRecord
		dmarcOK, dmarcRecord = live.DMARCVerified, live.DMARCRecord
	}

	records := RequiredRecords(d)
	report := &HealthReport{
		DomainID:          d.ID,
		Domain:            d.Domain,
		OwnershipVerified: ownership,
		CheckedAt:         s.now(),
		Records:           records,
		// Guidance is attached only to a failing check: a passing one needs no
		// instruction.
		SPF:     checkResult(spfOK, spfRecord, spfGuidance(records.SPF)),
		DKIM:    checkResult(dkimOK, dkimRecord, dkimGuidance(d, records.DKIM, records.Verification)),
		DMARC:   checkResult(dmarcOK, dmarcRecord, dmarcGuidance(records.DMARC)),
		Signing: s.signingStatus(d),
		PTR:     s.ptrGuidance(d),
	}

	if s.stats != nil && ownership {
		scope := repositories.ResourceScope{UserID: d.UserID, WorkspaceID: d.WorkspaceID}
		if stats, err := s.stats.SendingStats(scope, strings.ToLower(d.Domain), since); err == nil {
			report.Sending = SendingHealth{
				Sent:          stats.Sent,
				Bounced:       stats.Bounced,
				Complained:    stats.Complained,
				BounceRate:    rateOf(stats.Bounced, stats.Sent),
				ComplaintRate: rateOf(stats.Complained, stats.Sent),
				Since:         since,
			}
		}
	}

	// OB-6 W3 (ADR-007 decision 7): reputation rollups, pool, warm-up. The
	// sections ride the same ownership gate as the sending stats; a failure
	// in one is non-fatal — the section is omitted, never a failed report.
	if s.reputation != nil && ownership {
		scope := repositories.ResourceScope{UserID: d.UserID, WorkspaceID: d.WorkspaceID}
		if rep, err := s.reputation.DomainReputationReport(scope, strings.ToLower(d.Domain), window); err == nil {
			report.Reputation = rep
		}
		if pool, err := s.reputation.DomainPoolView(d); err == nil {
			report.Pool = pool
		}
		if wu, err := s.reputation.DomainWarmupView(d, s.now()); err == nil {
			report.Warmup = wu
		}
	}
	return report
}

// signingStatus derives the DKIM signing view from the domain's managed keys:
// a published Active key means mail from this domain is being signed.
func (s *HealthService) signingStatus(d *models.Domain) SigningStatus {
	if s.keyRepo == nil {
		return SigningStatus{Guidance: dkimSigningGuidance(d)}
	}
	keys, err := s.keyRepo.FindByDomainID(d.ID)
	if err != nil {
		keys = nil
	}
	return signingStatusOf(keys, d)
}

// signingStatusOf maps the domain's keys onto the signing view.
func signingStatusOf(keys []models.DomainSigningKey, d *models.Domain) SigningStatus {
	if len(keys) == 0 {
		return SigningStatus{Guidance: dkimSigningGuidance(d)}
	}
	for i := range keys {
		k := &keys[i]
		if k.CanSign() {
			return SigningStatus{Enabled: true, Selector: k.Selector, Algorithm: k.Algorithm, DNSState: k.DNSState}
		}
	}
	// A key exists but cannot sign yet: report the newest with its state.
	k := &keys[0]
	return SigningStatus{
		Enabled:   false,
		Selector:  k.Selector,
		Algorithm: k.Algorithm,
		DNSState:  k.DNSState,
		Guidance:  "A signing key exists but is not active yet — publish its TXT record and re-check DNS, or rotate to a new key.",
	}
}

func checkResult(verified bool, record, guidance string) CheckResult {
	if verified {
		return CheckResult{Verified: true, Record: record}
	}
	return CheckResult{Verified: false, Record: record, Guidance: guidance}
}

func spfGuidance(rec DNSRecord) string {
	return fmt.Sprintf("Publish the TXT record %s = %q so receiving servers can authorize Posta as a sender.", rec.Host, rec.Value)
}

func dkimGuidance(d *models.Domain, rec DNSRecord, verification DNSRecord) string {
	if !d.OwnershipVerified {
		return "Verify domain ownership first: " + fmt.Sprintf("publish the TXT record %s = %q.", verification.Host, verification.Value)
	}
	return fmt.Sprintf("Publish %s CNAME %s (or a TXT key at that host) so Posta can sign mail and receivers can check it.", rec.Host, rec.Value)
}

func dmarcGuidance(rec DNSRecord) string {
	return fmt.Sprintf("Publish the TXT record %s = %q so receivers know where to send failure reports.", rec.Host, rec.Value)
}

func dkimSigningGuidance(d *models.Domain) string {
	return fmt.Sprintf("No managed signing key for %s yet — create one to sign outbound mail, or keep the CNAME at posta._domainkey.%s pointing at an upstream signer.", d.Domain, d.Domain)
}

// ptrGuidance builds the reverse-DNS view for the domain's outbound bind IP
// (OB-1, ADR-001 §7). Present only for direct-mode domains; guidance text
// only — the delivery path never enforces a PTR match in this phase.
func (s *HealthService) ptrGuidance(d *models.Domain) *PTRGuidance {
	if d.DeliveryMode != models.DeliveryModeDirect {
		return nil
	}
	bindIP := strings.TrimSpace(d.OutboundBindIP)
	target := s.ehloTarget(d)
	if bindIP == "" {
		return &PTRGuidance{
			Match: PTRMatchUnbound,
			Guidance: fmt.Sprintf("Direct delivery for %s has no outbound bind IP set. Configure one and publish a PTR (reverse DNS) record pointing that IP at your EHLO hostname (%s); major receivers weigh the PTR/forward-confirmation heavily for direct senders.", d.Domain, target),
		}
	}
	names, err := s.lookupPTR(bindIP)
	if err != nil || len(names) == 0 {
		return &PTRGuidance{
			BoundIP: bindIP,
			Match:   PTRMatchMissing,
			Guidance: fmt.Sprintf("The outbound bind IP %s resolves to no PTR (reverse DNS) record. Publish one pointing at your EHLO hostname (%s); without it, direct mail from %s is likely to be deferred or rejected by major receivers.", bindIP, target, d.Domain),
		}
	}
	ptr := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(names[0])), ".")
	g := &PTRGuidance{BoundIP: bindIP, PTRName: ptr}
	if s.ptrMatches(ptr, d) {
		g.Match = PTRMatchMatch
		g.Guidance = fmt.Sprintf("PTR %s matches the EHLO identity — direct mail from %s presents a consistent sending source.", ptr, d.Domain)
		return g
	}
	g.Match = PTRMatchMismatch
	g.Guidance = fmt.Sprintf("PTR %s does not match the EHLO identity (%s). Point the PTR record at the EHLO hostname, or align POSTA_MTA_EHLO_NAME with it — a mismatch is a common reason direct deliveries land in spam.", ptr, target)
	return g
}

// ehloTarget is the identity the PTR check compares against: the configured
// process EHLO name when set, otherwise the sending domain itself.
func (s *HealthService) ehloTarget(d *models.Domain) string {
	if name := strings.ToLower(strings.TrimSpace(s.ehloDomain)); name != "" && name != DefaultHealthEhloDomain {
		return name
	}
	return strings.ToLower(d.Domain)
}

// ptrMatches reports whether the PTR name equals the sending domain or the
// configured EHLO name (case-insensitive, trailing dot stripped).
func (s *HealthService) ptrMatches(ptr string, d *models.Domain) bool {
	if ptr == strings.ToLower(d.Domain) {
		return true
	}
	if name := strings.ToLower(strings.TrimSpace(s.ehloDomain)); name != "" && ptr == name {
		return true
	}
	return false
}

// DefaultHealthEhloDomain mirrors mta.DefaultHeloDomain without importing the
// MTA package (health stays a leaf service).
const DefaultHealthEhloDomain = "posta.local"

// rateOf returns percent with one decimal; a zero denominator yields 0.
func rateOf(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// senderDomainExpr mirrors the shared expression in
// repositories.SenderDomainExpr (one source of truth lives there; this alias
// keeps the queries below readable).
const senderDomainExpr = repositories.SenderDomainExpr

// gormSendingStats reads the sending numbers with plain SQL: per-domain bounce
// rates need a join from bounces to their email rows and the sender's domain,
// which the analytics aggregates do not carry.
type gormSendingStats struct {
	db *gorm.DB
}

func (g *gormSendingStats) SendingStats(scope repositories.ResourceScope, senderDomain string, since time.Time) (SendingStats, error) {
	var stats SendingStats

	sentQ := g.db.Model(&models.Email{}).
		Where(senderDomainExpr+" = ?", senderDomain).
		Where("sent_at IS NOT NULL AND created_at >= ?", since)
	repositories.ApplyScope(sentQ, scope).Count(&stats.Sent)

	bouncedQ := g.db.Model(&models.Bounce{}).
		Joins("JOIN emails e ON e.id = bounces.email_id").
		Where(senderDomainExpr+" = ?", senderDomain).
		Where("e.created_at >= ?", since)
	applyBounceScope(bouncedQ, scope).Count(&stats.Bounced)

	complaintQ := g.db.Model(&models.EmailEvent{}).
		Joins("JOIN emails e ON e.uuid = email_events.email_uuid").
		Where("email_events.type = ?", models.EmailEventComplained).
		Where(senderDomainExpr+" = ?", senderDomain).
		Where("email_events.occurred_at >= ?", since)
	applyEventScope(complaintQ, scope).Count(&stats.Complained)

	return stats, nil
}

// applyBounceScope / applyEventScope narrow a joined query to the workspace via
// the emails side (e), the table that anchors the sender-domain match.
func applyBounceScope(q *gorm.DB, scope repositories.ResourceScope) *gorm.DB {
	if scope.WorkspaceID == nil {
		return q.Where("1 = 0")
	}
	return q.Where("e.workspace_id = ?", *scope.WorkspaceID)
}

func applyEventScope(q *gorm.DB, scope repositories.ResourceScope) *gorm.DB {
	if scope.WorkspaceID == nil {
		return q.Where("1 = 0")
	}
	return q.Where("e.workspace_id = ?", *scope.WorkspaceID)
}
