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

package inbound

import (
	"context"
	"fmt"
	"time"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/migration"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SF-32: the iprev operator modes (annotate | tag | reject). The verdict
// always evaluates and persists when the engine is on; the mode decides what
// a DEFINITIVE fail does. Tests run against REAL Postgres (TEST_DATABASE_DSN,
// the mailbox-delivery suite's gate): tag/reject stamp and flip the durable
// inbound row, which the fake driver cannot express.

const iprevFixtureRaw = "From: sender@outside.test\r\nSubject: iprev mode\r\n\r\nbody\r\n"

func iprevDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available: %v", err)
	}
	if err := migration.Run(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// notFound is the seams' "record does not exist" answer.
func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

// iprevService wires a Service whose verifier has the DNS seams set so the
// connecting IP resolves to a PTR that does NOT forward-confirm (fail), or
// (noPtr=true) has no PTR at all (none).
func iprevService(t *testing.T, gdb *gorm.DB, mode string, noPtr bool) *Service {
	t.Helper()
	t.Setenv("POSTA_INBOUND_IPREV_ENABLED", "true")
	t.Setenv("POSTA_INBOUND_IPREV_MODE", mode)
	svc := NewService(
		repositories.NewInboundEmailRepository(gdb),
		repositories.NewDomainRepository(gdb),
		repositories.NewSuppressionRepository(gdb),
		Config{MaxMessageSize: 1 << 20},
	)
	svc.EnableVerification(gdb, "posta.test")
	svc.verifier.lookupAddr = func(ctx context.Context, addr string) ([]string, error) {
		if noPtr {
			return nil, notFound(addr)
		}
		return []string{"unconfirmed.outside.test."}, nil
	}
	svc.verifier.lookupIP = func(ctx context.Context, name string) ([]net.IP, error) {
		if noPtr {
			return nil, notFound(name)
		}
		// A PTR that resolves to a different address: fail.
		return []net.IP{net.ParseIP("203.0.113.99")}, nil
	}
	svc.verifier.lookupTXT = func(ctx context.Context, name string) ([]string, error) {
		return nil, notFound(name)
	}
	svc.verifier.lookupMX = func(ctx context.Context, name string) ([]*net.MX, error) {
		return nil, notFound(name)
	}
	return svc
}

// iprevSeed stores the fixture message row inside the test transaction.
func iprevSeed(t *testing.T, gdb *gorm.DB) *models.InboundEmail {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	user := &models.User{Name: "iprev-" + suffix, Email: "iprev-" + suffix + "@test.local", PasswordHash: "x"}
	if err := gdb.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	ws := &models.Workspace{Name: "iprev-" + suffix, Slug: "iprev-" + suffix, OwnerID: user.ID}
	if err := gdb.Create(ws).Error; err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	dom := &models.Domain{UserID: user.ID, WorkspaceID: &ws.ID, Domain: "iprev-" + suffix + ".test", OwnershipVerified: true, VerificationToken: "tok-" + suffix}
	if err := gdb.Create(dom).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	rec := &models.InboundEmail{
		UserID:      user.ID,
		WorkspaceID: &ws.ID,
		DomainID:    dom.ID,
		Sender:      "sender@outside.test",
		Recipients:  pq.StringArray{"user@posta.test"},
		Status:      models.InboundStatusReceived,
		Source:      models.InboundSourceSMTP,
	}
	if err := gdb.Create(rec).Error; err != nil {
		t.Fatalf("seed inbound row: %v", err)
	}
	return rec
}

func TestIPRevModeAnnotateDefault(t *testing.T) {
	gdb := iprevDB(t)
	svc := iprevService(t, gdb, "", false) // mode unset → annotate
	rec := iprevSeed(t, gdb)

	svc.verifier.run(rec, []byte(iprevFixtureRaw), "sender@outside.test", "198.51.100.7")

	var got models.InboundEmail
	if err := gdb.First(&got, rec.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != models.InboundStatusReceived {
		t.Fatalf("annotate mode must not change status; got %q", got.Status)
	}
	if got.SpamReasons != "" {
		t.Fatalf("annotate mode must not stamp spam reasons; got %q", got.SpamReasons)
	}
	// The verdict itself is persisted (annotate's whole effect).
	var ar models.InboundAuthResult
	if err := gdb.Where("inbound_id = ?", rec.ID).First(&ar).Error; err != nil {
		t.Fatalf("verdict row: %v", err)
	}
	if ar.IPRevResult != "fail" {
		t.Fatalf("iprev verdict = %q, want fail", ar.IPRevResult)
	}
}

func TestIPRevModeTagStampsReason(t *testing.T) {
	gdb := iprevDB(t)
	svc := iprevService(t, gdb, IPRevModeTag, false)
	rec := iprevSeed(t, gdb)

	svc.verifier.run(rec, []byte(iprevFixtureRaw), "sender@outside.test", "198.51.100.7")

	var got models.InboundEmail
	if err := gdb.First(&got, rec.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != models.InboundStatusReceived {
		t.Fatalf("tag mode must not change status; got %q", got.Status)
	}
	if !strings.Contains(got.SpamReasons, "iprev:") {
		t.Fatalf("tag mode must stamp the iprev reason; got %q", got.SpamReasons)
	}
}

func TestIPRevModeRejectFlipsStatus(t *testing.T) {
	gdb := iprevDB(t)
	svc := iprevService(t, gdb, IPRevModeReject, false)
	rec := iprevSeed(t, gdb)

	svc.verifier.run(rec, []byte(iprevFixtureRaw), "sender@outside.test", "198.51.100.7")

	var got models.InboundEmail
	if err := gdb.First(&got, rec.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != models.InboundStatusRejected {
		t.Fatalf("reject mode must flip the stored record to rejected; got %q", got.Status)
	}
	if !strings.Contains(got.ErrorMessage, "iprev:") {
		t.Fatalf("reject must record the iprev reason; got %q", got.ErrorMessage)
	}
}

func TestIPRevModeNoneVerdictNeverEnforced(t *testing.T) {
	gdb := iprevDB(t)
	// No PTR at all: a "none" verdict must not enforce in ANY mode.
	svc := iprevService(t, gdb, IPRevModeReject, true)
	rec := iprevSeed(t, gdb)

	svc.verifier.run(rec, []byte(iprevFixtureRaw), "sender@outside.test", "198.51.100.7")

	var got models.InboundEmail
	if err := gdb.First(&got, rec.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != models.InboundStatusReceived {
		t.Fatalf("a none verdict must never enforce; got %q", got.Status)
	}
	var ar models.InboundAuthResult
	if err := gdb.Where("inbound_id = ?", rec.ID).First(&ar).Error; err != nil {
		t.Fatalf("verdict row: %v", err)
	}
	if ar.IPRevResult != "none" {
		t.Fatalf("iprev verdict = %q, want none", ar.IPRevResult)
	}
}

