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
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
)

// deliverFixture wires an executor over one in-process MX server with a fake
// attempt recorder, so executor tests run fully in-process.
type deliverFixture struct {
	srv      *mxServer
	resolver *Resolver
	attempts *fakeAttempts
	executor *Executor
}

func newDeliverFixture(t *testing.T, srvOpts ...func(*mxServer)) *deliverFixture {
	t.Helper()
	srv := newMXServer(t, srvOpts...)
	res := NewResolver()
	// The MX "host" is the IP literal of the in-process server: dialing needs
	// no DNS in the container. The TLS tests still pin the certificate name
	// through SetTLSConfigFor.
	res.SetLookups(
		func(ctx context.Context, domain string) ([]*net.MX, error) {
			return []*net.MX{{Host: "127.0.0.1", Pref: 10}}, nil
		},
		func(ctx context.Context, host string) ([]net.IPAddr, error) {
			hostIP, _, _ := net.SplitHostPort(srv.addr())
			return []net.IPAddr{{IP: net.ParseIP(hostIP)}}, nil
		},
	)
	res.SetPort(portOfAddr(t, srv.addr()))
	att := newFakeAttempts()
	exec := NewExecutor(res, NewBudgetKeeper(time.Hour), att)
	exec.SetHeloDomain("ehlo.posta.test")
	return &deliverFixture{srv: srv, resolver: res, attempts: att, executor: exec}
}

func portOfAddr(t *testing.T, addr string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	return p
}

func testSpec() DeliverySpec {
	return DeliverySpec{
		SenderDomain:   "example.com",
		DomainVerified: true,
		Policy:         PolicyFor(nil),
	}
}

func testEnvelope(rcpts ...string) Envelope {
	return Envelope{
		EmailID:    42,
		From:       "sender@example.com",
		Recipients: rcpts,
		Msg:        []byte("Subject: hi\r\n\r\nbody\r\n"),
	}
}

// Direct delivery happy path: MX → 250 at every step → delivered, with one
// attempt row recording outcome delivered and the 250 response.
func TestDirectDeliveryHappyPath(t *testing.T) {
	f := newDeliverFixture(t, withCaps("8BITMIME"))

	res, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@EXAMPLE.com"))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !res.Delivered {
		t.Fatal("Deliver must report delivered on MX 2xx")
	}
	if res.MXHost != "127.0.0.1" {
		t.Fatalf("MXHost = %q, want the fixture MX address", res.MXHost)
	}
	if res.Attempts != 1 {
		t.Fatalf("Attempts = %d, want 1", res.Attempts)
	}

	rows := f.attempts.all()
	if len(rows) != 1 {
		t.Fatalf("attempt rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Outcome != models.MTAAttemptDelivered {
		t.Fatalf("outcome = %q, want delivered", row.Outcome)
	}
	if row.ResponseCode != 250 {
		t.Fatalf("response code = %d, want 250", row.ResponseCode)
	}
	if row.EmailID != 42 {
		t.Fatalf("email_id = %d, want 42", row.EmailID)
	}
	if row.MXHost != "127.0.0.1" {
		t.Fatalf("mx_host = %q, want the fixture MX address", row.MXHost)
	}
	if row.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", row.Attempt)
	}
	// Durable trace: the attempt row carries host, timing, and the recipient
	// reached the MX (domain re-derived from the validated recipient).
	if row.StartedAt.IsZero() || row.FinishedAt.IsZero() {
		t.Fatal("attempt timestamps must be stamped")
	}
	joined := strings.Join(f.srv.commandsSeen(), "|")
	if !strings.Contains(joined, "RCPT TO:<RCPT@EXAMPLE.COM>") {
		t.Fatalf("recipient missing on the wire: %v", f.srv.commandsSeen())
	}
}

// STARTTLS enforced end to end: an MX advertising it gets the upgrade before
// MAIL FROM, with certificate verification against the test CA.
func TestDirectDeliverySTARTTLSWhenOffered(t *testing.T) {
	f := newDeliverFixture(t, withTLS(), withCaps("8BITMIME"))
	tlsCfg := clientTLSConfig(t, f.srv.cert)
	f.executor.SetTLSConfigFor(func(host string) *tls.Config {
		// The MX host dial target is the IP literal; the certificate name is
		// pinned by the returned config (mx.test), verification enabled.
		return tlsCfg
	})

	res, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com"))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !res.Delivered || !res.Caps.UpgradedTLS {
		t.Fatalf("delivered=%v upgraded=%v — direct delivery must upgrade when offered", res.Delivered, res.Caps.UpgradedTLS)
	}
	if !f.srv.sawTLS() {
		t.Fatal("MX server completed no TLS handshake")
	}
}

// Plaintext only when the MX offers no STARTTLS.
func TestDirectDeliveryPlaintextWhenNoSTARTTLSOffered(t *testing.T) {
	f := newDeliverFixture(t, withCaps("8BITMIME"))
	res, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com"))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Caps.UpgradedTLS {
		t.Fatal("MX advertised no STARTTLS; session must stay plaintext")
	}
	if f.srv.sawTLS() {
		t.Fatal("server saw a TLS handshake although it advertised none")
	}
	if !res.Delivered {
		t.Fatal("plaintext delivery must complete when STARTTLS is not offered")
	}
}

// 421/450 at MAIL FROM are transient: a transient SendError the worker turns
// into a re-enqueue with the existing backoff, and transient attempt rows.
func TestDirectDeliveryTransient421And450(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		wantCode    int
	}{
		{"421 throttle", "421 4.7.0 service not available", 421},
		{"450 throttle", "450 4.2.1 try again later", 450},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDeliverFixture(t, withReply("MAIL FROM", tc.reply))
			res, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com"))
			if err == nil {
				t.Fatal("throttled delivery must fail")
			}
			if res.Delivered {
				t.Fatal("a throttle response must never report delivered")
			}
			var se *email.SendError
			if !errors.As(err, &se) || se.Code != tc.wantCode || se.Permanent() {
				t.Fatalf("%s must surface as transient SendError %d, got %v", tc.name, tc.wantCode, err)
			}
			rows := f.attempts.all()
			if len(rows) != 1 ||
				rows[0].Outcome != models.MTAAttemptTransientFailure ||
				rows[0].ResponseCode != tc.wantCode {
				t.Fatalf("attempt rows = %+v, want one transient %d row", rows, tc.wantCode)
			}
		})
	}
}

// 550 at RCPT TO is permanent: stage/recipient carry into the worker's R-05
// suppression chain, the attempt row is permanent_failure, and the executor
// does NOT retry the dead recipient on the next MX host.
func TestDirectDeliveryPermanent550AtRcpt(t *testing.T) {
	f := newDeliverFixture(t, withReply("RCPT TO", "550 5.1.1 user unknown"))
	res, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("dead@example.com"))
	if err == nil {
		t.Fatal("550 must fail the delivery")
	}
	if res.Delivered {
		t.Fatal("550 must never report delivered")
	}
	var se *email.SendError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *email.SendError", err)
	}
	if !se.Permanent() || se.Stage != "RCPT TO" || se.Recipient != "dead@example.com" {
		t.Fatalf("SendError = {stage %q, code %d, rcpt %q}", se.Stage, se.Code, se.Recipient)
	}
	rows := f.attempts.all()
	if len(rows) != 1 || rows[0].Outcome != models.MTAAttemptPermanentFailure || rows[0].ResponseCode != 550 {
		t.Fatalf("attempt rows = %+v, want one permanent 550 row", rows)
	}
}

// Fail closed (non-negotiable #1): an unverified sender domain is refused
// before any MX is contacted, with a durable refusal record.
func TestDirectDeliveryFailsClosedForUnverifiedSenderDomain(t *testing.T) {
	f := newDeliverFixture(t)
	spec := testSpec()
	spec.DomainVerified = false

	res, err := f.executor.Deliver(context.Background(), spec, testEnvelope("rcpt@example.com"))
	if err == nil {
		t.Fatal("unverified sender domain must be refused")
	}
	if res != nil && res.Delivered {
		t.Fatal("refusal must never report delivered")
	}
	var refusal *PolicyRefusal
	if !errors.As(err, &refusal) || !refusal.UnverifiedSenderDomain {
		t.Fatalf("err = %v, want *PolicyRefusal (unverified sender domain)", err)
	}
	if n := f.srv.sessionCount(); n != 0 {
		t.Fatalf("MX sessions = %d, want 0 (fail closed before dialing)", n)
	}
	rows := f.attempts.all()
	if len(rows) != 1 || rows[0].Outcome != models.MTAAttemptRefusedUnverifiedSender {
		t.Fatalf("attempt rows = %+v, want one refused_unverified_sender row", rows)
	}
}

// An invalid recipient (no re-derivable domain) is a permanent recipient
// failure at RCPT TO — 550 5.1.3 — and never dials the MX.
func TestDirectDeliveryInvalidRecipientFailsPermanent(t *testing.T) {
	f := newDeliverFixture(t)
	res, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("not-an-address"))
	if err == nil {
		t.Fatal("recipient without a domain must fail")
	}
	if res != nil && res.Delivered {
		t.Fatal("invalid recipient must never report delivered")
	}
	var se *email.SendError
	if !errors.As(err, &se) || !se.Permanent() || se.Stage != "RCPT TO" {
		t.Fatalf("err = %v, want permanent RCPT TO SendError", err)
	}
	if n := f.srv.sessionCount(); n != 0 {
		t.Fatalf("MX sessions = %d, want 0 (refused before dialing)", n)
	}
}

// Per-domain hourly budget: exhaustion defers the message without contacting
// the MX, and the defer decision is recorded durably.
func TestDirectDeliveryHourlyBudgetExhaustedDefers(t *testing.T) {
	f := newDeliverFixture(t)
	budget := NewBudgetKeeper(time.Hour)
	f.executor = NewExecutor(f.resolver, budget, f.attempts)
	f.executor.SetHeloDomain("ehlo.posta.test")

	spec := testSpec()
	spec.Policy = Policy{HourlyBudget: 1}

	if _, err := f.executor.Deliver(context.Background(), spec, testEnvelope("rcpt@example.com")); err != nil {
		t.Fatalf("first delivery within budget: %v", err)
	}
	res, err := f.executor.Deliver(context.Background(), spec, testEnvelope("rcpt2@example.com"))
	if err == nil {
		t.Fatal("budget-exhausted delivery must defer")
	}
	if res.Delivered {
		t.Fatal("deferred delivery must never report delivered")
	}
	var se *email.SendError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want SendError", err)
	}
	if se.Permanent() {
		t.Fatal("a budget defer is transient — it must be retried later")
	}
	if !strings.Contains(se.Msg, "hourly budget exhausted") {
		t.Fatalf("defer reply text must name the budget: %q", se.Msg)
	}
	if !strings.Contains(err.Error(), "deferred (budget)") {
		t.Fatalf("defer error must carry the deferral: %v", err)
	}
	if n := f.srv.sessionCount(); n != 1 {
		t.Fatalf("MX sessions = %d, want 1 (deferred delivery must not dial)", n)
	}
	rows := f.attempts.all()
	if len(rows) != 2 {
		t.Fatalf("attempt rows = %d, want 2", len(rows))
	}
	last := rows[len(rows)-1]
	if last.Outcome != models.MTAAttemptDeferredBudget {
		t.Fatalf("last outcome = %q, want deferred_budget", last.Outcome)
	}
}

// Recipients are re-derived and grouped per domain: both recipients of the
// same domain (whatever their stored case) reach the MX once each.
func TestDirectDeliveryGroupsRecipientsByDerivedDomain(t *testing.T) {
	f := newDeliverFixture(t, withCaps("8BITMIME"))
	res, err := f.executor.Deliver(context.Background(), testSpec(),
		testEnvelope("a@EXAMPLE.com", "b@example.com"))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !res.Delivered {
		t.Fatal("all recipients must deliver")
	}
	joined := strings.Join(f.srv.commandsSeen(), "|")
	if strings.Count(joined, "RCPT TO:<A@EXAMPLE.COM>") != 1 ||
		strings.Count(joined, "RCPT TO:<B@EXAMPLE.COM>") != 1 {
		t.Fatalf("each recipient must reach the MX exactly once: %v", f.srv.commandsSeen())
	}
	if res.Attempts != 1 {
		t.Fatalf("Attempts = %d, want 1 (one session for the whole domain)", res.Attempts)
	}
}

// DNS failure surfaces as a transient (retryable) error with a durable
// dns_failure attempt row.
func TestDirectDeliveryDNSFailureIsTransientAndRecorded(t *testing.T) {
	f := newDeliverFixture(t)
	f.resolver.SetLookups(
		func(ctx context.Context, domain string) ([]*net.MX, error) {
			return nil, errors.New("lookup example.com: temporary failure in name resolution")
		},
		nil,
	)
	res, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com"))
	if err == nil {
		t.Fatal("DNS failure must fail the delivery")
	}
	if res.Delivered {
		t.Fatal("DNS failure must never report delivered")
	}
	var se *email.SendError
	if !errors.As(err, &se) || se.Permanent() {
		t.Fatalf("DNS failure must stay transient (retry), got %v", err)
	}
	rows := f.attempts.all()
	if len(rows) != 1 || rows[0].Outcome != models.MTAAttemptDNSFailure {
		t.Fatalf("attempt rows = %+v, want one dns_failure row", rows)
	}
}

// Multi-host attempts are bounded by the per-domain policy: with
// MaxHostAttempts=2 and three MX hosts, exactly two dials and two attempt
// rows happen before the delivery fails.
func TestDirectDeliveryAttemptBoundByPolicy(t *testing.T) {
	sessions := 0
	var mu sync.Mutex
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			mu.Lock()
			sessions++
			mu.Unlock()
			_ = c.Close() // accept then die: connection-level failure per attempt
		}
	}()

	res := NewResolver()
	res.SetLookups(
		func(ctx context.Context, domain string) ([]*net.MX, error) {
			return []*net.MX{
				{Host: "127.0.0.1", Pref: 10},
				{Host: "127.0.0.1", Pref: 20},
				{Host: "127.0.0.1", Pref: 30},
			}, nil
		},
		nil,
	)
	res.SetPort(portOfAddr(t, ln.Addr().String()))
	att := newFakeAttempts()
	exec := NewExecutor(res, NewBudgetKeeper(time.Hour), att)

	spec := testSpec()
	spec.Policy = Policy{MaxHostAttempts: 2}
	_, derr := exec.Deliver(context.Background(), spec, testEnvelope("rcpt@example.com"))
	if derr == nil {
		t.Fatal("all-hosts-failing delivery must fail")
	}
	if rows := att.all(); len(rows) != 2 {
		t.Fatalf("attempt rows = %d, want 2 (MaxHostAttempts bound)", len(rows))
	}
	mu.Lock()
	n := sessions
	mu.Unlock()
	if n != 2 {
		t.Fatalf("dial sessions = %d, want 2 (bounded by policy)", n)
	}
}

// Attempt rows are written for every attempt including failures: with two MX
// records for the domain, the first host throttles (451) and the second
// delivers — both attempts land as durable rows (one transient, one delivered).
func TestDirectDeliveryAttemptRowsForEveryAttemptIncludingFailures(t *testing.T) {
	shared := newMXServer(t, withCaps("8BITMIME"), firstMailFailsThenOK("451 4.3.0 try again"))

	att := newFakeAttempts()
	res := NewResolver()
	res.SetLookups(
		func(ctx context.Context, domain string) ([]*net.MX, error) {
			return []*net.MX{
				{Host: "127.0.0.1", Pref: 10},
				{Host: "127.0.0.1", Pref: 20},
			}, nil
		},
		nil,
	)
	res.SetPort(portOfAddr(t, shared.addr()))
	exec := NewExecutor(res, NewBudgetKeeper(time.Hour), att)
	exec.SetHeloDomain("ehlo.posta.test")

	res2, err := exec.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com"))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !res2.Delivered || res2.Attempts != 2 {
		t.Fatalf("delivered=%v attempts=%d — the second attempt must deliver", res2.Delivered, res2.Attempts)
	}
	rows := att.all()
	if len(rows) != 2 {
		t.Fatalf("attempt rows = %d, want 2 (failure row + success row)", len(rows))
	}
	if rows[0].Outcome != models.MTAAttemptTransientFailure || rows[0].ResponseCode != 451 {
		t.Fatalf("first row = %+v, want transient 451", rows[0])
	}
	if rows[1].Outcome != models.MTAAttemptDelivered || rows[1].ResponseCode != 250 {
		t.Fatalf("second row = %+v, want delivered 250", rows[1])
	}
	if rows[0].Attempt != 1 || rows[1].Attempt != 2 {
		t.Fatalf("attempt ordinals = %d,%d want 1,2", rows[0].Attempt, rows[1].Attempt)
	}
}

// firstMailFailsThenOK makes the first MAIL FROM of the server's lifetime
// fail with the given reply; later sessions succeed.
func firstMailFailsThenOK(reply string) func(*mxServer) {
	return func(s *mxServer) { s.firstMailReply = reply }
}

// "delivered" is never reported for a non-DATA-2xx outcome: a 554 at the end
// of DATA is a permanent failure even though banner/EHLO/MAIL/RCPT were 2xx.
func TestDirectDeliveryNeverDeliveredOnNonDATA2xx(t *testing.T) {
	f := newDeliverFixture(t, withReply("DATA-END", "554 5.7.1 message rejected"))
	res, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com"))
	if err == nil {
		t.Fatal("DATA 554 must fail the delivery")
	}
	if res.Delivered {
		t.Fatal("delivered may only follow a 2xx at end-of-DATA")
	}
	rows := f.attempts.all()
	if len(rows) != 1 || rows[0].Outcome != models.MTAAttemptPermanentFailure {
		t.Fatalf("attempt rows = %+v, want one permanent_failure row", rows)
	}
}
