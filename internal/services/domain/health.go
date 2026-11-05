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
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
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
}

func NewHealthService(db *gorm.DB, keyRepo *repositories.DomainSigningKeyRepository) *HealthService {
	return &HealthService{
		db:      db,
		keyRepo: keyRepo,
		verify:  Verify,
		now:     time.Now,
		stats:   &gormSendingStats{db: db},
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
	CheckedAt         time.Time     `json:"checked_at"`
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

// rateOf returns percent with one decimal; a zero denominator yields 0.
func rateOf(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// senderDomainExpr extracts the bare sending domain from emails.sender. Sender
// carries either a bare address ("hello@example.com") or RFC 5322 display form
// ("Acme <hello@example.com>"): prefer the <addr-spec> when present, then take
// everything after the @. Lower-cased so the comparison matches how domains
// are stored.
const senderDomainExpr = `LOWER(SPLIT_PART(BTRIM(COALESCE(NULLIF(SUBSTRING(sender FROM '<([^>]+)>'), ''), sender)), '@', 2))`

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
