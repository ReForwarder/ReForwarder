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

package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
)

// SF-02 tests: the relay-path REQUIRETLS guard (RFC 8689 §5.2) — a
// REQUIRETLS-marked message must never cross a plaintext hop, so a relay
// server configured with encryption "none" refuses the send through the
// existing failure bookkeeping, while plain messages keep the pre-SF-02
// behavior on the same configuration.

// seedRequireTLSEmail seeds a queued email row with the RFC 8689 requirement
// set (user_id 1, matching the relay-server fixtures below).
func seedRequireTLSEmail(t *testing.T, s *claimState, id int64, requireTLS bool) {
	t.Helper()
	s.insert("emails", map[string]any{
		"id": id, "status": models.EmailStatusQueued, "retry_count": int64(0),
		"user_id": int64(1),
		"sender":  "sender@example.com", "subject": "requiretls test",
		"require_tls": requireTLS,
		"created_at":  time.Now(), "updated_at": time.Now(),
	})
}

func newRequireTLSHandler(t *testing.T, state *claimState) *EmailSendHandler {
	t.Helper()
	gdb := claimGorm(t, state)
	return NewEmailSendHandler(
		repositories.NewEmailRepository(gdb),
		repositories.NewSMTPRepository(gdb),
		nil, nil, nil,
		nil, // nil dispatcher: the fake DB is not thread-safe under -race.
	)
}

// A REQUIRETLS-marked message aimed at a relay configured without TLS is
// refused — the row finalizes failed with the REQUIRETLS reason and the
// plaintext relay is never dialed.
func TestRelayPathRefusesPlaintextRelayForRequireTLSMessage(t *testing.T) {
	state := newClaimState()
	seedRequireTLSEmail(t, state, 91, true)
	state.insert("smtp_servers", map[string]any{
		"id": int64(910), "user_id": int64(1), "status": "enabled",
		"host": "relay.test", "port": int64(25), "encryption": "none",
	})

	handler := newRequireTLSHandler(t, state)
	payload, err := NewEmailSendTask(91)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	// The guard refuses the send: the returned error is the SendError the
	// queue layer sees (asynq retries it into the existing exhaustion path).
	if err := handler.ProcessTask(context.Background(), payload); err == nil {
		t.Fatal("ProcessTask must return the REQUIRETLS refusal error")
	} else if !strings.Contains(err.Error(), "REQUIRETLS") {
		t.Fatalf("refusal error = %v, want the REQUIRETLS reason", err)
	}

	if got := state.statusOf("emails", 91); got != string(models.EmailStatusFailed) {
		t.Fatalf("status = %v, want failed", got)
	}
	if got := state.stringOf("emails", 91, "error_message"); !strings.Contains(got, "REQUIRETLS") {
		t.Fatalf("error_message = %q, want the REQUIRETLS reason", got)
	}
}

// The control: a plain message keeps the pre-SF-02 behavior on the same
// configuration — the guard does not fire (the failure, if any, comes from
// the transport, not the REQUIRETLS reason).
func TestRelayPathPlainMessageKeepsLegacyBehavior(t *testing.T) {
	state := newClaimState()
	seedRequireTLSEmail(t, state, 92, false)
	state.insert("smtp_servers", map[string]any{
		"id": int64(920), "user_id": int64(1), "status": "enabled",
		"host": "relay.test", "port": int64(25), "encryption": "none",
	})

	handler := newRequireTLSHandler(t, state)
	payload, err := NewEmailSendTask(92)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	// Without the requirement the send is attempted: nothing listens on
	// relay.test:25 in the test, so the transport dials and fails with a
	// connection error — anything EXCEPT the REQUIRETLS refusal.
	if err := handler.ProcessTask(context.Background(), payload); err != nil {
		if strings.Contains(err.Error(), "REQUIRETLS") {
			t.Fatalf("plain message hit the REQUIRETLS guard: %v", err)
		}
	}
	if got := state.stringOf("emails", 92, "error_message"); strings.Contains(got, "REQUIRETLS") {
		t.Fatalf("error_message = %q, want a transport-level failure (guard must not fire)", got)
	}
}
