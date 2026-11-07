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

package worker

import (
	"context"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mta"
	"github.com/goposta/posta/internal/storage/repositories"
)

// The OB-1 worker-branch tests run the real EmailSendHandler against the
// claimState fake (atomic_claim_test.go patterns) plus an in-process MX
// server, pinning the delivery-mode branch: 'direct' routes to the MTA
// executor with durable attempt records; 'relay' (the default) takes exactly
// today's path; duplicate deliveries deliver exactly once (R-26 preserved).

// ---------- in-process MX server (happy + scripted failure) ----------

type branchMX struct {
	ln net.Listener

	mu           sync.Mutex
	sessions     int
	mailFroms    []string
	failMailWith string
	failRcptWith string
}

func startBranchMX(t *testing.T) *branchMX {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("branch MX listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	m := &branchMX{ln: ln}
	go m.accept()
	return m
}

func (m *branchMX) addr() string { return m.ln.Addr().String() }

func (m *branchMX) port() int {
	_, port, err := net.SplitHostPort(m.addr())
	if err != nil {
		return 0
	}
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	return p
}

func (m *branchMX) accept() {
	for {
		c, err := m.ln.Accept()
		if err != nil {
			return
		}
		go m.serve(c)
	}
}

func (m *branchMX) serve(c net.Conn) {
	m.mu.Lock()
	m.sessions++
	m.mu.Unlock()
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	text := textproto.NewConn(c)
	defer func() { _ = text.Close() }()
	_ = text.PrintfLine("220 branch-mx.test ESMTP")
	for {
		line, err := text.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			_ = text.PrintfLine("250-branch-mx.test")
			_ = text.PrintfLine("250 8BITMIME")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			m.mu.Lock()
			m.mailFroms = append(m.mailFroms, cmd)
			m.mu.Unlock()
			if m.failMailWith != "" {
				_ = text.PrintfLine("%s", m.failMailWith)
				return
			}
			_ = text.PrintfLine("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			if m.failRcptWith != "" {
				_ = text.PrintfLine("%s", m.failRcptWith)
				continue
			}
			_ = text.PrintfLine("250 ok")
		case cmd == "DATA":
			_ = text.PrintfLine("354 go ahead")
			for {
				l, err := text.ReadLine()
				if err != nil {
					return
				}
				if l == "." {
					break
				}
			}
			_ = text.PrintfLine("250 accepted")
		case cmd == "QUIT":
			_ = text.PrintfLine("221 bye")
			return
		default:
			_ = text.PrintfLine("250 ok")
		}
	}
}

func (m *branchMX) sessionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions
}

func (m *branchMX) mailFromCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.mailFroms)
}

// ---------- fixtures ----------

type recordingAttempts struct {
	mu   sync.Mutex
	rows []models.MTADeliveryAttempt
}

func (r *recordingAttempts) RecordAttempt(a models.MTADeliveryAttempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, a)
	return nil
}

func (r *recordingAttempts) all() []models.MTADeliveryAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]models.MTADeliveryAttempt(nil), r.rows...)
}

func (r *recordingAttempts) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

// seedDirectEmail seeds an email row addressed from the fixture sender domain.
func seedDirectEmail(t *testing.T, s *claimState, id int64, status models.EmailStatus) {
	t.Helper()
	s.insert("emails", map[string]any{
		"id": id, "status": status, "retry_count": int64(0),
		"user_id": int64(7), "sender": "sender@example.com",
		"recipients": "{rcpt@example.com}", "subject": "direct branch test",
		"created_at": time.Now(), "updated_at": time.Now(),
	})
}

func seedFixtureDomain(t *testing.T, s *claimState, mode string, verified bool) {
	t.Helper()
	s.insert("domains", map[string]any{
		"id": int64(1), "user_id": int64(7), "domain": "example.com",
		"ownership_verified": verified, "delivery_mode": mode,
	})
}

// directHandler wires the handler the way cmd wiring does: executor over a
// fixture resolver that resolves the MX to the in-process server, and the
// attempt recorder fake.
func directHandler(t *testing.T, state *claimState, mx *branchMX, rec *recordingAttempts, autoSuppress bool) *EmailSendHandler {
	t.Helper()
	gdb := claimGorm(t, state)
	res := mta.NewResolver()
	res.SetLookups(
		func(ctx context.Context, domain string) ([]*net.MX, error) {
			return []*net.MX{{Host: "127.0.0.1", Pref: 10}}, nil
		},
		nil,
	)
	res.SetPort(mx.port())
	exec := mta.NewExecutor(res, mta.NewBudgetKeeper(time.Hour), rec)

	handler := NewEmailSendHandler(
		repositories.NewEmailRepository(gdb),
		repositories.NewSMTPRepository(gdb),
		nil,
		repositories.NewDomainRepository(gdb), // the delivery-mode lookup reads this
		nil,
		nil, // nil dispatcher: the fake DB is not thread-safe under -race
	)
	handler.SetMTAExecutor(exec)
	handler.SetAutoSuppress(autoSuppress)
	return handler
}

// ---------- tests ----------

// Direct-mode happy path through the worker: claim → deliver → MX 2xx →
// status sent, durable delivered attempt row, MX saw the envelope.
func TestDirectModeDeliveryHappyPathViaWorker(t *testing.T) {
	state := newClaimState()
	seedDirectEmail(t, state, 71, models.EmailStatusQueued)
	seedFixtureDomain(t, state, models.DeliveryModeDirect, true)
	mx := startBranchMX(t)
	rec := &recordingAttempts{}
	handler := directHandler(t, state, mx, rec, false)

	payload, err := NewEmailSendTask(71)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if err := handler.ProcessTask(context.Background(), payload); err != nil {
		t.Fatalf("ProcessTask: %v", err)
	}

	if got := state.statusOf("emails", 71); got != string(models.EmailStatusSent) {
		t.Fatalf("status = %v, want sent", got)
	}
	if mx.sessionCount() != 1 {
		t.Fatalf("MX sessions = %d, want 1", mx.sessionCount())
	}
	if mx.mailFromCount() != 1 {
		t.Fatalf("MAIL FROMs = %d, want 1", mx.mailFromCount())
	}
	rows := rec.all()
	if len(rows) != 1 {
		t.Fatalf("attempt rows = %d, want 1", len(rows))
	}
	if rows[0].Outcome != models.MTAAttemptDelivered || rows[0].ResponseCode != 250 {
		t.Fatalf("attempt row = %+v, want delivered/250", rows[0])
	}
	if rows[0].EmailID != 71 {
		t.Fatalf("attempt email_id = %d, want 71", rows[0].EmailID)
	}
}

// Exactly-once (R-26) is preserved in direct mode: a duplicate task delivery
// loses the claim and never re-enters the MX.
func TestDirectModeDuplicateDeliveryDeliversOnce(t *testing.T) {
	state := newClaimState()
	seedDirectEmail(t, state, 72, models.EmailStatusQueued)
	seedFixtureDomain(t, state, models.DeliveryModeDirect, true)
	mx := startBranchMX(t)
	rec := &recordingAttempts{}
	handler := directHandler(t, state, mx, rec, false)

	payload, err := NewEmailSendTask(72)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if err := handler.ProcessTask(context.Background(), payload); err != nil {
		t.Fatalf("first ProcessTask: %v", err)
	}
	sessionsAfterFirst := mx.sessionCount()

	// Duplicate delivery: the claim loses (row is sent) — no second send.
	if err := handler.ProcessTask(context.Background(), payload); err != nil {
		t.Fatalf("duplicate ProcessTask must succeed: %v", err)
	}
	if mx.sessionCount() != sessionsAfterFirst {
		t.Fatalf("MX sessions after duplicate = %d, want %d (delivered once)", mx.sessionCount(), sessionsAfterFirst)
	}
	if rec.count() != 1 {
		t.Fatalf("attempt rows = %d, want 1", rec.count())
	}
	if got := state.statusOf("emails", 72); got != string(models.EmailStatusSent) {
		t.Fatalf("status = %v, want sent", got)
	}
}

// 421/450 → transient: the handler returns an error so asynq re-enqueues with
// the existing backoff, the email lands failed with retry_count bumped, and
// the attempt row records the transient reply.
func TestDirectModeTransientThrottleReenqueues(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		wantCode    int
	}{
		{"421", "421 4.7.0 service not available", 421},
		{"450", "450 4.2.1 try again later", 450},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newClaimState()
			seedDirectEmail(t, state, 73, models.EmailStatusQueued)
			seedFixtureDomain(t, state, models.DeliveryModeDirect, true)
			mx := startBranchMX(t)
			mx.mu.Lock()
			mx.failMailWith = tc.reply
			mx.mu.Unlock()
			rec := &recordingAttempts{}
			handler := directHandler(t, state, mx, rec, false)

			payload, err := NewEmailSendTask(73)
			if err != nil {
				t.Fatalf("build payload: %v", err)
			}
			if perr := handler.ProcessTask(context.Background(), payload); perr == nil {
				t.Fatal("throttled delivery must return an error so asynq retries")
			}
			if got := state.statusOf("emails", 73); got != string(models.EmailStatusFailed) {
				t.Fatalf("status = %v, want failed", got)
			}
			if got := state.intOf("emails", 73, "retry_count"); got != 1 {
				t.Fatalf("retry_count = %d, want 1", got)
			}
			rows := rec.all()
			if len(rows) != 1 ||
				rows[0].Outcome != models.MTAAttemptTransientFailure ||
				rows[0].ResponseCode != tc.wantCode {
				t.Fatalf("attempt rows = %+v, want one transient %d row", rows, tc.wantCode)
			}
		})
	}
}

// 550 at RCPT TO → permanent classification → the existing bounce/suppression
// chain (auto-suppress on) finalizes the email with no asynq retry.
func TestDirectModePermanent550SuppressesWithoutRetry(t *testing.T) {
	state := newClaimState()
	seedDirectEmail(t, state, 74, models.EmailStatusQueued)
	seedFixtureDomain(t, state, models.DeliveryModeDirect, true)
	mx := startBranchMX(t)
	mx.mu.Lock()
	mx.failRcptWith = "550 5.1.1 user unknown"
	mx.mu.Unlock()
	rec := &recordingAttempts{}
	handler := directHandler(t, state, mx, rec, true) // autoSuppress on

	payload, err := NewEmailSendTask(74)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if perr := handler.ProcessTask(context.Background(), payload); perr != nil {
		t.Fatalf("permanent rejection must finalize the task (no retry), got %v", perr)
	}
	if got := state.statusOf("emails", 74); got != string(models.EmailStatusFailed) {
		t.Fatalf("status = %v, want failed", got)
	}
	if got := state.intOf("emails", 74, "retry_count"); got != 0 {
		t.Fatalf("retry_count = %d, want 0 (permanent path does not retry)", got)
	}
	rows := rec.all()
	if len(rows) != 1 || rows[0].Outcome != models.MTAAttemptPermanentFailure || rows[0].ResponseCode != 550 {
		t.Fatalf("attempt rows = %+v, want one permanent 550 row", rows)
	}
}

// 'relay' (the default) is untouched: with a relay-mode domain row — or none
// at all — the handler consults the SMTP server selection and never dials an
// MX. With no server configured the email fails exactly as before.
func TestRelayModeUnchangedForRelayDomains(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seedDomain bool
		mode       string
	}{
		{"relay-mode domain row", true, models.DeliveryModeRelay},
		{"no domain row (default)", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newClaimState()
			seedDirectEmail(t, state, 75, models.EmailStatusQueued)
			if tc.seedDomain {
				seedFixtureDomain(t, state, tc.mode, false)
			}
			mx := startBranchMX(t)
			rec := &recordingAttempts{}
			handler := directHandler(t, state, mx, rec, false)

			payload, err := NewEmailSendTask(75)
			if err != nil {
				t.Fatalf("build payload: %v", err)
			}
			if perr := handler.ProcessTask(context.Background(), payload); perr != nil {
				t.Fatalf("relay-mode ProcessTask: %v", perr)
			}
			if got := state.statusOf("emails", 75); got != string(models.EmailStatusFailed) {
				t.Fatalf("status = %v, want failed (no SMTP server configured)", got)
			}
			if n := state.countSelects("smtp_servers"); n != 1 {
				t.Fatalf("smtp_servers selects = %d, want 1 (relay server selection ran)", n)
			}
			if mx.sessionCount() != 0 {
				t.Fatalf("MX sessions = %d, want 0 (relay mode never dials MX)", mx.sessionCount())
			}
			if rec.count() != 0 {
				t.Fatalf("attempt rows = %d, want 0 (no direct attempts in relay mode)", rec.count())
			}
		})
	}
}

// Fail closed: direct mode configured but no MTA executor wired (e.g. an
// older worker) must fail the email with a clear message — never fall back
// to the relay silently.
func TestDirectModeFailsClosedWithoutExecutor(t *testing.T) {
	state := newClaimState()
	seedDirectEmail(t, state, 76, models.EmailStatusQueued)
	seedFixtureDomain(t, state, models.DeliveryModeDirect, true)
	mx := startBranchMX(t)
	rec := &recordingAttempts{}

	gdb := claimGorm(t, state)
	handler := NewEmailSendHandler(
		repositories.NewEmailRepository(gdb),
		repositories.NewSMTPRepository(gdb),
		nil,
		repositories.NewDomainRepository(gdb),
		nil, nil,
	)
	// No SetMTAExecutor call — the executor is missing on this worker.

	payload, err := NewEmailSendTask(76)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if perr := handler.ProcessTask(context.Background(), payload); perr != nil {
		t.Fatalf("fail-closed ProcessTask: %v", perr)
	}
	if got := state.statusOf("emails", 76); got != string(models.EmailStatusFailed) {
		t.Fatalf("status = %v, want failed", got)
	}
	if mx.sessionCount() != 0 {
		t.Fatalf("MX sessions = %d, want 0 (fail closed)", mx.sessionCount())
	}
	if rec.count() != 0 {
		t.Fatalf("attempt rows = %d, want 0", rec.count())
	}
}
