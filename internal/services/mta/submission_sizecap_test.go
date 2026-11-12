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

package mta

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	gosmtp "github.com/emersion/go-smtp"
	"github.com/emersion/go-sasl"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
)

// SF-17: the per-domain outbound size cap. At the MSA, a declared SIZE above
// the sender domain's cap is refused at MAIL — before any body crosses the
// wire. On the executor, a message over the cap is a permanent policy refusal
// recorded durably, before any dial.

type fakeDomainPolicy struct{ caps map[string]int64 }

func (f *fakeDomainPolicy) MaxMessageSizeFor(_ uint, _ *uint, domain string) int64 {
	return f.caps[domain]
}

// sizeCapFixture mirrors newSubmissionFixture with the SF-17 policy wired.
func sizeCapFixture(t *testing.T, caps map[string]int64) string {
	t.Helper()
	verifier := &fakeVerifier{creds: map[string]fakeCred{
		"smtp_alice": {
			password:   "correct-horse",
			cred:       &models.SMTPCredential{ID: 1, UserID: 7, WorkspaceID: 3, Username: "smtp_alice"},
			ownerEmail: "alice@example.org",
		},
	}}
	domains := &fakeDomains{verified: map[string]bool{"verified.example": true}}

	backend, err := NewSubmissionBackend(verifier, domains, &fakeSink{}, 1024*1024)
	if err != nil {
		t.Fatalf("NewSubmissionBackend: %v", err)
	}
	backend.SetDomainPolicy(&fakeDomainPolicy{caps: caps})
	srv, err := NewSubmissionServer(backend, SubmissionConfig{
		Host: "127.0.0.1", Port: 0, Hostname: "posta.test", TLSMode: "none",
	})
	if err != nil {
		t.Fatalf("NewSubmissionServer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String()
}

func authedMSA(t *testing.T, addr string) *gosmtp.Client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial MSA: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cl := gosmtp.NewClient(conn)
	if err := cl.Hello("client.example.org"); err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	if err := cl.Auth(sasl.NewPlainClient("", "smtp_alice", "correct-horse")); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	return cl
}

func TestMSAPerDomainSizeCapRejectsOversizeAtMail(t *testing.T) {
	addr := sizeCapFixture(t, map[string]int64{"verified.example": 1024})
	cl := authedMSA(t, addr)

	err := cl.Mail("sender@verified.example", &gosmtp.MailOptions{Size: 2048})
	if err == nil {
		t.Fatal("a SIZE above the domain cap must be refused at MAIL")
	}
	var smtpErr *gosmtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 552 {
		t.Fatalf("err = %v, want 552", err)
	}
	if !strings.Contains(smtpErr.Message, "1024-octet") {
		t.Fatalf("reply must name the cap: %q", smtpErr.Message)
	}
}

func TestMSAPerDomainSizeCapAllowsWithinCap(t *testing.T) {
	addr := sizeCapFixture(t, map[string]int64{"verified.example": 1024})
	cl := authedMSA(t, addr)

	if err := cl.Mail("sender@verified.example", &gosmtp.MailOptions{Size: 512}); err != nil {
		t.Fatalf("SIZE below the cap must be accepted: %v", err)
	}
	if err := cl.Rcpt("rcpt@example.net", nil); err != nil {
		t.Fatalf("RCPT: %v", err)
	}
}

func TestMSAPerDomainSizeCapUnsetAcceptsAnySize(t *testing.T) {
	// No cap for the domain: the per-domain check is inert (the listener
	// MaxMessageBytes still bounds DATA).
	addr := sizeCapFixture(t, map[string]int64{})
	cl := authedMSA(t, addr)

	if err := cl.Mail("sender@verified.example", &gosmtp.MailOptions{Size: 1 << 30}); err != nil {
		t.Fatalf("an uncapped domain must accept any declared SIZE: %v", err)
	}
}

func TestDirectDeliverySizeCapRejectsBeforeDial(t *testing.T) {
	f := newDeliverFixture(t)
	spec := testSpec()
	spec.Policy.MaxMessageSize = 10 // octets

	env := testEnvelope("rcpt@example.com") // body far exceeds 10 octets
	res, err := f.executor.Deliver(context.Background(), spec, env)
	if err == nil {
		t.Fatal("an over-cap message must be refused")
	}
	if res.Delivered {
		t.Fatal("a size-capped refusal is never delivered")
	}
	var se *email.SendError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *email.SendError", err)
	}
	if se.Code != 552 || se.Stage != "POLICY" {
		t.Fatalf("code/stage = %d/%s, want 552/POLICY", se.Code, se.Stage)
	}
	if !se.Permanent() {
		t.Fatal("a size-cap refusal is permanent")
	}
	if n := f.srv.sessionCount(); n != 0 {
		t.Fatalf("MX sessions = %d, want 0 (the refusal precedes any dial)", n)
	}
	// The refusal is durably recorded for the attempt log.
	rows := f.attempts.all()
	if len(rows) == 0 || rows[len(rows)-1].Outcome != models.MTAAttemptPermanentFailure {
		t.Fatalf("attempt rows = %v, want a permanent_failure record", rows)
	}
}
