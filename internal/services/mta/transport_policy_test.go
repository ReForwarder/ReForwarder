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
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/conditions"
	"github.com/goposta/posta/internal/storage/migration"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SF-50 tests: the transport-policy gates. The resolver (precedence,
// no-match, the strict fail-closed grammar through the real table) runs
// against REAL Postgres (TEST_DATABASE_DSN, the repositories suite's
// pattern — one rolled-back transaction per test). The two GATE wirings
// (delivery merge + RCPT reject/451) run through stub resolvers over the
// existing in-process harnesses, so the fail-closed and override behaviors
// are observable without a store.

var (
	tpMigrateOnce sync.Once
	tpMigrateErr  error
)

func tpDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skipf("skipping: TEST_DATABASE_DSN not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available (%s): %v", dsn, err)
	}
	// The full additive migration runs once (the mailbox-delivery suite's
	// pattern): the transport-policy table must exist for the resolver.
	tpMigrateOnce.Do(func() {
		tpMigrateErr = migration.Run(db)
	})
	if tpMigrateErr != nil {
		t.Fatalf("migrate: %v", tpMigrateErr)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func tpRule(field, operator string, value any) models.FilterRule {
	return models.FilterRule{Field: field, Operator: operator, Value: value}
}

// TestTransportPolicyResolverPrecedenceAndFailClosed — the resolver contract
// against the real table: evaluation order (priority ASC then id ASC), the
// FIRST matching policy wins (later policies never stack), disabled policies
// never match, and a policy carrying an unknown field (a strict-grammar
// violation) surfaces as an ERROR — the fail-closed contract the gates
// answer 451/defer to.
func TestTransportPolicyResolverPrecedenceAndFailClosed(t *testing.T) {
	tx := tpDB(t)
	const ws = 33
	const dom = "prio.example"

	repo := repositories.NewTransportPolicyRepository(tx)
	resolver := NewTransportPolicyResolver(repo)

	seed := func(p models.TransportPolicy) {
		t.Helper()
		if err := repo.Create(&p); err != nil {
			t.Fatalf("seed %q: %v", p.Name, err)
		}
	}

	// Three policies for the same domain: a non-matching one at priority 0,
	// the matching "tighten" at priority 1, and a matching rejector at
	// priority 2 — the first match must win and the rejector must never
	// stack.
	seed(tpPolicyFix(ws, dom, "never-matches", 0,
		models.FilterRules{tpRule(conditions.TransportFieldMailFrom, "equals", "nobody@nowhere.example")}, nil))
	seed(tpPolicyFix(ws, dom, "tighten", 1,
		models.FilterRules{tpRule(conditions.TransportFieldMailFrom, "ends_with", "prio.example")}, func(p *models.TransportPolicy) {
			p.HourlyBudget = 7
			p.MsgRatePerMin = 3
			p.MaxMessageSize = 4096
		}))
	seed(tpPolicyFix(ws, dom, "rejector", 2,
		models.FilterRules{tpRule(conditions.TransportFieldMailFrom, "ends_with", "prio.example")}, func(p *models.TransportPolicy) {
			p.Reject = true
			p.RejectReason = "blocked by precedence test"
		}))

	facts := TransportFacts{
		MailFrom:     "alice@prio.example",
		RcptTo:       "bob@remote.example",
		SenderDomain: "prio.example",
		MessageSize:  512,
	}
	o, matched, err := resolver.ResolveForDelivery(context.Background(), ws, dom, facts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !matched {
		t.Fatal("the tighten policy must match")
	}
	if o.PolicyName != "tighten" {
		t.Fatalf("precedence: the first matching policy = %q, want tighten", o.PolicyName)
	}
	if o.HourlyBudget != 7 || o.MsgRatePerMin != 3 || o.MaxMessageSize != 4096 {
		t.Fatalf("override fields = %+v, want the tighten policy's", o)
	}
	if o.Reject {
		t.Fatal("the non-first reject policy must never stack over the first match")
	}

	// A disabled policy never matches.
	// A disabled policy never matches — even at a higher precedence (its
	// priority 0 would have won over the tighten policy): the resolution must
	// return the tighten policy, proving the disabled row was excluded.
	disabled := tpPolicyFix(ws, dom, "disabled", 0,
		models.FilterRules{tpRule(conditions.TransportFieldMailFrom, "equals", "alice@prio.example")}, nil)
	disabled.Enabled = false
	seed(disabled)
	o, matched, err = resolver.ResolveForDelivery(context.Background(), ws, dom, facts)
	if err != nil || !matched {
		t.Fatalf("the enabled tighten policy must still match: %v/%v", matched, err)
	}
	if o.PolicyName != "tighten" {
		t.Fatalf("the disabled policy (priority 0) must be excluded: matched %q instead", o.PolicyName)
	}
}

// tpPolicyFix builds a policy row (the test's compact constructor).
func tpPolicyFix(workspaceID uint, domain, name string, priority int, conditions models.FilterRules, mutate func(*models.TransportPolicy)) models.TransportPolicy {
	p := models.TransportPolicy{
		WorkspaceID: workspaceID,
		DomainName:  domain,
		Name:        name,
		Priority:    priority,
		Enabled:     true,
		Conditions:  conditions,
	}
	if mutate != nil {
		mutate(&p)
	}
	return p
}

// TestTransportPolicyResolverStrictGrammar — a policy whose conditions use
// the grammar's strict mode (an unknown field) surfaces an evaluator ERROR
// from the resolver: the gates' fail-closed input.
func TestTransportPolicyResolverStrictGrammar(t *testing.T) {
	tx := tpDB(t)
	const ws = 34
	repo := repositories.NewTransportPolicyRepository(tx)
	resolver := NewTransportPolicyResolver(repo)

	if err := repo.Create(&models.TransportPolicy{
		WorkspaceID: ws, DomainName: "strict.example", Name: "typo",
		Priority: 1, Enabled: true,
		Conditions: models.FilterRules{tpRule("not_a_fact", "equals", "x")},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, _, err := resolver.ResolveForDelivery(context.Background(), ws, "strict.example", TransportFacts{
		MailFrom: "a@strict.example", RcptTo: "b@x.y", SenderDomain: "strict.example", MessageSize: 1,
	})
	if err == nil {
		t.Fatal("a strict-grammar violation must surface as an evaluator error (fail-closed)")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Fatalf("the error must name the offending policy: %v", err)
	}
}

// ---------- the stub resolver for the gate wirings ----------

type stubTransport struct {
	mu      sync.Mutex
	o       *TransportOverride
	matched bool
	err     error
	called  int
	facts   TransportFacts
	domain  string
	ws      uint
}

func (s *stubTransport) record(ws uint, domain string, facts TransportFacts) (*TransportOverride, bool, error) {
	s.mu.Lock()
	s.called++
	s.facts = facts
	s.domain = domain
	s.ws = ws
	o, matched, err := s.o, s.matched, s.err
	s.mu.Unlock()
	return o, matched, err
}

func (s *stubTransport) ResolveForDelivery(ctx context.Context, ws uint, domain string, facts TransportFacts) (*TransportOverride, bool, error) {
	return s.record(ws, domain, facts)
}

func (s *stubTransport) ResolveForRcpt(ctx context.Context, ws uint, domain string, facts TransportFacts) (*TransportOverride, bool, error) {
	return s.record(ws, domain, facts)
}

func (s *stubTransport) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called
}

// TestDeliveryGateOverridesAndFailsClosed — the delivery gate wiring: a
// matching policy's overrides merge into the per-domain policy (a tightened
// size cap refuses the send BEFORE any dial — the 552 gate), an evaluator
// error defers the send with a transient failure and dials nothing
// (fail-closed), and system mail (nil workspace) skips the gate entirely.
func TestDeliveryGateOverridesAndFailsClosed(t *testing.T) {
	ws := uint(42)

	// (a) The override merge: a transport policy tightening MaxMessageSize
	// below the message size fires the SF-17 size gate — a durable 552 row,
	// no MX dial.
	f := newDeliverFixture(t)
	stub := &stubTransport{matched: true, o: &TransportOverride{
		MaxMessageSize: 10, // the fixture message is larger
		PolicyName:     "tiny-cap",
	}}
	f.executor.SetTransportPolicy(stub)
	env := testEnvelope("rcpt@example.com")
	env.WorkspaceID = &ws

	res, err := f.executor.Deliver(context.Background(), testSpec(), env)
	if err == nil || res.Delivered {
		t.Fatalf("the tightened size cap must refuse the delivery: %v/%v", res, err)
	}
	rows := f.attempts.all()
	if len(rows) != 1 || rows[0].ResponseCode != 552 {
		t.Fatalf("attempt rows = %+v, want the 552 size refusal", rows)
	}
	if f.srv.sessionCount() != 0 {
		t.Fatal("no MX session may be dialed once the size gate refused")
	}
	if stub.count() != 1 {
		t.Fatalf("the resolver was consulted %d times, want 1", stub.count())
	}
	if stub.facts.MailFrom != "sender@example.com" || stub.facts.RcptTo != "rcpt@example.com" ||
		stub.facts.SenderDomain != "example.com" || stub.facts.MessageSize != int64(len(env.Msg)) {
		t.Fatalf("delivery facts = %+v, want the envelope facts", stub.facts)
	}
	if stub.ws != ws || stub.domain != "example.com" {
		t.Fatalf("resolver scope = %d/%q, want 42/example.com (the envelope workspace + sender domain)", stub.ws, stub.domain)
	}

	// (b) Fail-closed: an evaluator error defers with a transient failure —
	// no dial, the message retried with the existing backoff.
	f2 := newDeliverFixture(t)
	errStub := &stubTransport{err: errors.New("policy store down")}
	f2.executor.SetTransportPolicy(errStub)
	env2 := testEnvelope("rcpt@example.com")
	env2.WorkspaceID = &ws
	res2, err2 := f2.executor.Deliver(context.Background(), testSpec(), env2)
	if err2 == nil || res2.Delivered {
		t.Fatal("an evaluator error must defer the delivery (fail-closed)")
	}
	if !strings.Contains(err2.Error(), "transport policy evaluation failed") {
		t.Fatalf("defer error = %v, must name the transport-policy failure", err2)
	}
	if f2.srv.sessionCount() != 0 {
		t.Fatal("an evaluator error must dial no MX host (fail-closed)")
	}
	rows2 := f2.attempts.all()
	if len(rows2) != 1 || rows2[0].ResponseCode != 451 {
		t.Fatalf("attempt rows = %+v, want the 451 deferral row", rows2)
	}

	// (c) System mail (nil workspace) skips the gate entirely.
	f3 := newDeliverFixture(t)
	skipStub := &stubTransport{}
	f3.executor.SetTransportPolicy(skipStub)
	if _, err := f3.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com")); err != nil {
		t.Fatalf("system delivery: %v", err)
	}
	if skipStub.count() != 0 {
		t.Fatal("system mail must not consult the transport-policy gate")
	}
}

// TestRCPTGateRejectsAndFailsClosed — the RCPT gate wiring over the
// in-process MSA: a matching reject policy answers 550 with the policy's
// reason, an evaluator error answers the temporary 451 (fail-closed), and a
// non-reject match passes the recipient through.
func TestRCPTGateRejectsAndFailsClosed(t *testing.T) {
	newBackend := func(stub *stubTransport) string {
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
		backend.SetTransportPolicy(stub)
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

	// (a) A matching reject policy refuses the recipient with the policy's
	// reason.
	rejectStub := &stubTransport{matched: true, o: &TransportOverride{Reject: true, Reason: "blocked by the ops team", PolicyName: "blocker"}}
	addr := newBackend(rejectStub)
	cl := submissionClient(t, addr, true)
	if err := cl.Mail("alice@verified.example", nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	err := cl.Rcpt("victim@remote.example", nil)
	if smtpCode(t, err) != 550 {
		t.Fatalf("RCPT = %v, want 550 (the reject override)", err)
	}
	if !strings.Contains(err.Error(), "blocked by the ops team") {
		t.Fatalf("the 550 must carry the policy's reason: %v", err)
	}
	if rejectStub.count() != 1 {
		t.Fatalf("the resolver was consulted %d times, want 1", rejectStub.count())
	}
	if rejectStub.facts.Helo != "client.example.org" || rejectStub.facts.AuthenticatedAs != "smtp_alice" ||
		rejectStub.facts.MailFrom != "alice@verified.example" || rejectStub.facts.RcptTo != "victim@remote.example" {
		t.Fatalf("rcpt facts = %+v, want the envelope facts", rejectStub.facts)
	}
	if rejectStub.ws != 3 || rejectStub.domain != "verified.example" {
		t.Fatalf("resolver scope = %d/%q, want 3/verified.example", rejectStub.ws, rejectStub.domain)
	}

	// (b) An evaluator error answers the temporary 451 (fail-closed).
	errStub := &stubTransport{err: errors.New("policy store down")}
	addr2 := newBackend(errStub)
	cl2 := submissionClient(t, addr2, true)
	if err := cl2.Mail("alice@verified.example", nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if code := smtpCode(t, cl2.Rcpt("victim@remote.example", nil)); code != 451 {
		t.Fatalf("RCPT on evaluator error = %d, want the temporary 451 (fail-closed)", code)
	}

	// (c) A non-reject match passes the recipient through (the override
	// belongs to the delivery gate).
	passStub := &stubTransport{matched: true, o: &TransportOverride{MsgRatePerMin: 5}}
	addr3 := newBackend(passStub)
	cl3 := submissionClient(t, addr3, true)
	if err := cl3.Mail("alice@verified.example", nil); err != nil {
		t.Fatalf("MAIL: %v", err)
	}
	if err := cl3.Rcpt("bob@remote.example", nil); err != nil {
		t.Fatalf("a non-reject match must pass the recipient: %v", err)
	}
}

// TestNoTransportGateByDefault — an unwired seam keeps the pre-SF-50
// behavior byte-for-byte: the delivery proceeds and no resolver runs.
func TestNoTransportGateByDefault(t *testing.T) {
	f := newDeliverFixture(t)
	res, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt@example.com"))
	if err != nil || !res.Delivered {
		t.Fatalf("delivery without the gate: %v/%v", res, err)
	}
	if f.srv.sessionCount() != 1 {
		t.Fatalf("MX sessions = %d, want 1", f.srv.sessionCount())
	}
}
