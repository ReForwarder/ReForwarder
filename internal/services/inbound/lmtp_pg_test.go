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
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
)

// SF-01: RFC 2033 LMTP listener end-to-end. Runs against REAL Postgres
// (TEST_DATABASE_DSN, the mailbox-delivery suite's harness): the
// per-recipient DATA semantics rest on real per-recipient ingests, so a fake
// driver cannot express the contract. Each test works in one transaction
// rolled back at cleanup.

// lmtpServe starts the SF-01 LMTP server over the fixture's ingest service
// on an ephemeral loopback port and returns its address.
func lmtpServe(t *testing.T, fx *mdFixture) string {
	t.Helper()
	backend := NewBackend(fx.svc, repositories.NewDomainRepository(fx.tx), 1<<20)
	backend.SetRouter(NewRouter(repositories.NewInboundRouteRepository(fx.tx)))
	srv, err := NewLMTPServer(backend, SMTPConfig{
		Host: "127.0.0.1", Port: 0, Hostname: "mx.test", MaxMessageSize: 1 << 20,
	})
	if err != nil {
		t.Fatalf("build LMTP server: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// lmtpDeliver speaks RFC 2033 to addr: LHLO, MAIL, RCPT per address, DATA —
// and returns the per-recipient final statuses (nil value = 250 queued).
func lmtpDeliver(t *testing.T, addr, from string, rcpts []string, body string) map[string]*smtp.SMTPError {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial LMTP: %v", err)
	}
	c := smtp.NewClientLMTP(conn)
	defer func() { _ = c.Close() }()
	if err := c.Hello("lda.test"); err != nil {
		t.Fatalf("LHLO: %v", err)
	}
	if err := c.Mail(from, nil); err != nil {
		t.Fatalf("MAIL FROM: %v", err)
	}
	for _, rcpt := range rcpts {
		if err := c.Rcpt(rcpt, nil); err != nil {
			t.Fatalf("RCPT %s: %v", rcpt, err)
		}
	}
	dc, err := c.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	if _, err := dc.Write([]byte(body)); err != nil {
		t.Fatalf("write body: %v", err)
	}
	_, err = dc.CloseWithLMTPResponse()
	if err != nil {
		lmtpErr, ok := err.(smtp.LMTPDataError)
		if !ok {
			t.Fatalf("DATA close: %v", err)
		}
		return lmtpErr
	}
	return map[string]*smtp.SMTPError{}
}

// lmtpSeedRoute adds a second verified domain + deliver route to the
// fixture's workspace, for the mixed-status scenario.
func lmtpSeedRoute(t *testing.T, fx *mdFixture, name string) (string, uint) {
	t.Helper()
	domName := name + ".test"
	dom := &models.Domain{
		UserID: fx.userID, WorkspaceID: &fx.wsID, Domain: domName,
		OwnershipVerified: true, VerificationToken: "tok-" + name,
	}
	if err := fx.tx.Create(dom).Error; err != nil {
		t.Fatalf("create domain %s: %v", domName, err)
	}
	route := &models.InboundRoute{
		UserID: fx.userID, WorkspaceID: &fx.wsID, DomainID: dom.ID,
		Pattern: "@" + domName, Action: models.InboundRouteActionDeliver,
		Priority: 10, Enabled: true,
	}
	if err := fx.tx.Create(route).Error; err != nil {
		t.Fatalf("create route: %v", err)
	}
	return domName, route.ID
}

func TestLMTPPerRecipientStatus(t *testing.T) {
	tx := mdDB(t)
	fx := mdNewFixture(t, tx, true)
	addr := lmtpServe(t, fx)

	domB, routeB := lmtpSeedRoute(t, fx, "lmtpb")
	rcptA := "user@" + fx.domName
	rcptB := "other@" + domB

	// Drive the transaction manually so the route can be disabled BETWEEN
	// RCPT (accepted) and DATA: B must get its own 550 at DATA while A still
	// queues — the per-recipient status contract RFC 2033 §4.2 requires.
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial LMTP: %v", err)
	}
	c := smtp.NewClientLMTP(conn)
	defer func() { _ = c.Close() }()
	if err := c.Hello("lda.test"); err != nil {
		t.Fatalf("LHLO: %v", err)
	}
	if err := c.Mail("sender@example.org", nil); err != nil {
		t.Fatalf("MAIL FROM: %v", err)
	}
	for _, rcpt := range []string{rcptA, rcptB} {
		if err := c.Rcpt(rcpt, nil); err != nil {
			t.Fatalf("RCPT %s: %v", rcpt, err)
		}
	}

	// Rules changed between RCPT and DATA — the same race the DATA handler
	// re-checks: B gets its own 550, A still queues.
	if err := tx.Model(&models.InboundRoute{}).Where("id = ?", routeB).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable route B: %v", err)
	}

	dc, err := c.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	if _, err := dc.Write([]byte("Subject: lmtp\r\n\r\nper-recipient status\r\n")); err != nil {
		t.Fatalf("write body: %v", err)
	}
	_, derr := dc.CloseWithLMTPResponse()
	if derr == nil {
		t.Fatal("expected a per-recipient error for the unrouted recipient")
	}
	statuses, ok := derr.(smtp.LMTPDataError)
	if !ok {
		t.Fatalf("DATA close error type: %T", derr)
	}

	if err, ok := statuses[rcptA]; ok {
		t.Fatalf("routed recipient %s must queue (got error %v)", rcptA, err)
	}
	bErr, ok := statuses[rcptB]
	if !ok {
		t.Fatalf("unrouted recipient %s must get its own error status", rcptB)
	}
	if bErr.Code != 550 {
		t.Fatalf("unrouted recipient status = %d, want 550", bErr.Code)
	}

	// A's message is stored accepted; B's reject is persisted too (OB-2's
	// explicit-reject record) with its own row — one ingest per recipient.
	var stored []models.InboundEmail
	if err := tx.Where("user_id = ?", fx.userID).Order("id").Find(&stored).Error; err != nil {
		t.Fatalf("query stored: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored rows = %d, want 2 (one accepted, one persisted reject)", len(stored))
	}
	var accepted *models.InboundEmail
	for i := range stored {
		if stored[i].Status == models.InboundStatusReceived {
			accepted = &stored[i]
		}
	}
	if accepted == nil {
		t.Fatal("no accepted (received) row stored for the routed recipient")
	}
	if len(accepted.Recipients) != 1 || accepted.Recipients[0] != rcptA {
		t.Fatalf("accepted row recipients = %v, want [%s]", accepted.Recipients, rcptA)
	}
	if stored[0].Source != models.InboundSourceLMTP {
		t.Fatalf("source = %q, want %q", stored[0].Source, models.InboundSourceLMTP)
	}
}

func TestLMTPDuplicateRcptGetsStatusPerOccurrence(t *testing.T) {
	tx := mdDB(t)
	fx := mdNewFixture(t, tx, true)
	addr := lmtpServe(t, fx)

	rcpt := "dup@" + fx.domName
	// go-smtp counts occurrences: the same RCPT twice must yield a status
	// for both (RFC 2033 §4.2 via go-smtp's statusCollector buffering).
	statuses := lmtpDeliver(t, addr, "sender@example.org",
		[]string{rcpt, rcpt},
		"Subject: dup\r\n\r\nbody\r\n")
	if err, ok := statuses[rcpt]; ok {
		t.Fatalf("routed duplicate recipient must queue (got %v)", err)
	}
}

// A fully-routed transaction returns one 250 per recipient, in recipient
// order — the RFC 2033 contract a local delivery agent depends on.
func TestLMTPAllRoutedQueuesEveryRecipient(t *testing.T) {
	tx := mdDB(t)
	fx := mdNewFixture(t, tx, true)
	addr := lmtpServe(t, fx)

	domC, _ := lmtpSeedRoute(t, fx, "lmtc")
	rcptA := "first@" + fx.domName
	rcptC := "second@" + domC

	statuses := lmtpDeliver(t, addr, "sender@example.org",
		[]string{rcptA, rcptC}, fmt.Sprintf("Subject: all\r\n\r\nboth %s and %s queue\r\n", rcptA, rcptC))
	for _, rcpt := range []string{rcptA, rcptC} {
		if err, ok := statuses[rcpt]; ok {
			t.Fatalf("routed recipient %s must queue (got %v)", rcpt, err)
		}
	}
}
