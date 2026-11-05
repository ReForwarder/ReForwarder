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
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	uuidpkg "github.com/google/uuid"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// fakeStats stands in for the sending-stats SQL: it records the scope and
// sender domain it was asked about and answers from a canned triple.
type fakeStats struct {
	stats  SendingStats
	err    error
	domain string
	since  time.Time
	scope  repositories.ResourceScope
}

func (f *fakeStats) SendingStats(scope repositories.ResourceScope, senderDomain string, since time.Time) (SendingStats, error) {
	f.scope, f.domain, f.since = scope, senderDomain, since
	return f.stats, f.err
}

func newHealthTestService(dumper *fakeStats) *HealthService {
	svc := NewHealthService(nil, nil)
	svc.now = func() time.Time { return time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC) }
	if dumper != nil {
		svc.stats = dumper
	}
	return svc
}

func healthTestDomain() *models.Domain {
	return &models.Domain{
		ID:                5,
		UserID:            1,
		Domain:            "example.com",
		OwnershipVerified: true,
		SPFVerified:       true,
		DKIMVerified:      false,
		DMARCVerified:     false,
		VerificationToken: "tok-123",
	}
}

// The report mirrors the stored check state, attaches guidance only to the
// failing checks, and asks the stats reader about the domain (lower-cased) over
// the expected window.
func TestHealthReportFromStoredState(t *testing.T) {
	probe := &fakeStats{stats: SendingStats{Sent: 1000, Bounced: 41, Complained: 3}}
	svc := newHealthTestService(probe)

	report, err := svc.Report(healthTestDomain(), 0) // 0 → default window
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	if !report.OwnershipVerified || !report.SPF.Verified || report.DKIM.Verified || report.DMARC.Verified {
		t.Fatalf("stored checks not mirrored: %+v", report)
	}
	if report.SPF.Guidance != "" {
		t.Errorf("passing check carries guidance: %q", report.SPF.Guidance)
	}
	if report.DKIM.Guidance == "" || !strings.Contains(report.DKIM.Guidance, "posta._domainkey.example.com") {
		t.Errorf("failing DKIM check lacks host guidance: %q", report.DKIM.Guidance)
	}
	if report.DMARC.Guidance == "" || !strings.Contains(report.DMARC.Guidance, "_dmarc.example.com") {
		t.Errorf("failing DMARC check lacks host guidance: %q", report.DMARC.Guidance)
	}
	if probe.domain != "example.com" {
		t.Errorf("stats queried for %q, want the lower-cased domain", probe.domain)
	}
	if want := svc.now().Add(-DefaultHealthWindow); !probe.since.Equal(want) {
		t.Errorf("stats window since = %v, want %v", probe.since, want)
	}
	if probe.scope.WorkspaceID != nil {
		t.Errorf("unscoped domain must query with nil workspace, got %v", *probe.scope.WorkspaceID)
	}
	if report.Sending.Sent != 1000 || report.Sending.Bounced != 41 {
		t.Fatalf("sending counts not carried: %+v", report.Sending)
	}
	if report.Sending.BounceRate < 4.1 || report.Sending.BounceRate > 4.10001 {
		t.Errorf("bounce rate = %v, want 4.1", report.Sending.BounceRate)
	}
	if report.Sending.ComplaintRate < 0.3 || report.Sending.ComplaintRate > 0.30001 {
		t.Errorf("complaint rate = %v, want 0.3", report.Sending.ComplaintRate)
	}
	if report.Records == nil || report.Records.SPF.Value == "" {
		t.Error("record-setting guidance missing")
	}
}

// A domain without verified ownership gets no sending numbers (nothing was
// legitimately sent from it) and DKIM guidance that points at ownership first.
func TestHealthReportUnverifiedDomain(t *testing.T) {
	probe := &fakeStats{stats: SendingStats{Sent: 5, Bounced: 5, Complained: 5}}
	svc := newHealthTestService(probe)

	d := healthTestDomain()
	d.OwnershipVerified = false
	report, err := svc.Report(d, 0)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	if report.Sending.Sent != 0 || report.Sending.BounceRate != 0 {
		t.Errorf("unverified domain must not report sending stats: %+v", report.Sending)
	}
	if !strings.Contains(report.DKIM.Guidance, "ownership") {
		t.Errorf("unverified domain DKIM guidance should lead with ownership: %q", report.DKIM.Guidance)
	}
	if !strings.Contains(report.DKIM.Guidance, "posta-verification=tok-123") {
		t.Errorf("guidance should carry the verification record: %q", report.DKIM.Guidance)
	}
}

// Diagnose builds the report from the live DNS result, not the stored state.
func TestHealthDiagnoseUsesLiveChecks(t *testing.T) {
	probe := &fakeStats{stats: SendingStats{Sent: 10}}
	svc := newHealthTestService(probe)
	svc.verify = func(d *models.Domain) (*VerificationResult, error) {
		return &VerificationResult{
			OwnershipVerified: true,
			SPFVerified:       false,
			DKIMVerified:      true,
			DKIMRecord:        "v=DKIM1; k=rsa; p=MIIB",
			DMARCVerified:     true,
			DMARCRecord:       "v=DMARC1; p=none",
		}, nil
	}

	report, dErr := svc.Diagnose(healthTestDomain(), 24*time.Hour)
	if dErr != nil {
		t.Fatalf("Diagnose: %v", dErr)
	}

	if report == nil {
		t.Fatal("no report")
	}
	if report.SPF.Verified {
		t.Error("live SPF=false must win over stored true")
	}
	if !report.DKIM.Verified || report.DKIM.Record != "v=DKIM1; k=rsa; p=MIIB" {
		t.Errorf("live DKIM result not carried: %+v", report.DKIM)
	}
	if report.DKIM.Guidance != "" {
		t.Errorf("passing live check carries guidance: %q", report.DKIM.Guidance)
	}
	if want := svc.now().Add(-24 * time.Hour); !probe.since.Equal(want) {
		t.Errorf("custom window not applied: %v", probe.since)
	}
}

func TestHealthDiagnoseVerifyError(t *testing.T) {
	svc := newHealthTestService(nil)
	svc.verify = func(d *models.Domain) (*VerificationResult, error) {
		return nil, context.DeadlineExceeded
	}
	if _, err := svc.Diagnose(healthTestDomain(), 0); err == nil {
		t.Fatal("Diagnose must surface the DNS failure")
	}
}

// The signing view prefers the key that can actually sign.
func TestSigningStatusOf(t *testing.T) {
	d := healthTestDomain()

	if st := signingStatusOf(nil, d); st.Enabled || st.Guidance == "" {
		t.Errorf("no keys must yield disabled + guidance, got %+v", st)
	}

	retired := models.DomainSigningKey{Selector: "old", Algorithm: "rsa-sha256", Status: "retired", DNSState: "published"}
	st := signingStatusOf([]models.DomainSigningKey{retired}, d)
	if st.Enabled {
		t.Error("a retired key cannot sign")
	}
	if st.Selector != "old" {
		t.Errorf("existing-but-inactive key should be reported: %+v", st)
	}

	active := models.DomainSigningKey{Selector: "posta", Algorithm: "ed25519-sha256", Status: "active", DNSState: "published"}
	st = signingStatusOf([]models.DomainSigningKey{retired, active}, d)
	if !st.Enabled || st.Selector != "posta" || st.Algorithm != "ed25519-sha256" {
		t.Errorf("active key must be reported as signing: %+v", st)
	}
	if st.Guidance != "" {
		t.Errorf("signing key carries no guidance: %q", st.Guidance)
	}
}

func TestRateOf(t *testing.T) {
	if got := rateOf(41, 1000); got < 4.1 || got > 4.10001 {
		t.Errorf("rateOf(41,1000) = %v", got)
	}
	if got := rateOf(1, 0); got != 0 {
		t.Errorf("rateOf with zero total = %v, want 0", got)
	}
	if got := rateOf(0, 100); got != 0 {
		t.Errorf("rateOf(0,100) = %v, want 0", got)
	}
}

// healthProbeDB opens the live test database when one is configured; without
// it the caller skips.
func healthProbeDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=posta password=posta dbname=posta port=5432 sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Skipf("skipping: no test database available: %v", err)
	}
	return db
}

// The sender-domain expression is hand-rolled SQL: pin it against a real
// postgres with both sender spellings — bare address and RFC 5322 display form.
func TestGormSendingStatsSenderDomainSQL(t *testing.T) {
	db := healthProbeDB(t)
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS pgcrypto`).Error; err != nil {
		t.Skipf("skipping: cannot enable pgcrypto: %v", err)
	}
	if err := db.AutoMigrate(&models.Email{}, &models.Bounce{}, &models.EmailEvent{}); err != nil {
		t.Skipf("skipping: cannot migrate test schema: %v", err)
	}

	const probeSubject = "posta-health-probe"
	marker := fmt.Sprintf("health-probe-%d.test", time.Now().UnixNano())
	ws := uint(time.Now().UnixNano()%1_000_000) + 1_000_000
	defer func() {
		// Probe rows are keyed by subject so cleanup never touches real data.
		db.Exec(`DELETE FROM bounces WHERE email_id IN (SELECT id FROM emails WHERE subject = ?)`, probeSubject)
		db.Exec(`DELETE FROM email_events WHERE email_uuid IN (SELECT uuid FROM emails WHERE subject = ?)`, probeSubject)
		db.Exec(`DELETE FROM emails WHERE subject = ?`, probeSubject)
	}()

	sent1 := &models.Email{
		UUID: uuidpkg.NewString(), UserID: 1, WorkspaceID: &ws,
		Sender: "Acme Books <bounce@" + marker + ">", Recipients: pq.StringArray{"r@example.net"},
		Subject: probeSubject, Status: models.EmailStatusSent,
		SentAt: ptrHealth(time.Now()), CreatedAt: time.Now(),
	}
	sent2 := &models.Email{
		UUID: uuidpkg.NewString(), UserID: 1, WorkspaceID: &ws,
		Sender: "bare@" + marker, Recipients: pq.StringArray{"r@example.net"},
		Subject: probeSubject, Status: models.EmailStatusSent,
		SentAt: ptrHealth(time.Now()), CreatedAt: time.Now(),
	}
	other := &models.Email{
		UUID: uuidpkg.NewString(), UserID: 1, WorkspaceID: &ws,
		Sender: "someone@else.test", Recipients: pq.StringArray{"r@example.net"},
		Subject: probeSubject, Status: models.EmailStatusSent,
		SentAt: ptrHealth(time.Now()), CreatedAt: time.Now(),
	}
	for _, em := range []*models.Email{sent1, sent2, other} {
		if err := db.Create(em).Error; err != nil {
			t.Skipf("skipping: cannot seed probe rows: %v", err)
		}
	}
	if err := db.Create(&models.Bounce{
		UserID: 1, WorkspaceID: &ws, EmailID: sent1.ID,
		Recipient: "r@example.net", Type: models.BounceTypeHard, CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Skipf("skipping: cannot seed bounce: %v", err)
	}
	if err := db.Create(&models.EmailEvent{
		EmailUUID: sent2.UUID, WorkspaceID: &ws, Type: models.EmailEventComplained,
		OccurredAt: time.Now(), CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Skipf("skipping: cannot seed complaint: %v", err)
	}

	stats, err := (&gormSendingStats{db: db}).SendingStats(
		repositories.ResourceScope{UserID: 1, WorkspaceID: &ws}, marker, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("SendingStats: %v", err)
	}
	if stats.Sent != 2 {
		t.Errorf("Sent = %d, want 2 (display-name and bare senders both match the domain)", stats.Sent)
	}
	if stats.Bounced != 1 {
		t.Errorf("Bounced = %d, want 1", stats.Bounced)
	}
	if stats.Complained != 1 {
		t.Errorf("Complained = %d, want 1", stats.Complained)
	}

	// A foreign workspace sees nothing.
	empty := uint(ws + 1)
	stats, err = (&gormSendingStats{db: db}).SendingStats(
		repositories.ResourceScope{UserID: 1, WorkspaceID: &empty}, marker, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("SendingStats(other scope): %v", err)
	}
	if stats.Sent != 0 || stats.Bounced != 0 || stats.Complained != 0 {
		t.Errorf("foreign workspace must see nothing, got %+v", stats)
	}
}

func ptrHealth(t time.Time) *time.Time { return &t }
