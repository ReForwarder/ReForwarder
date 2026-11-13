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

package smtprelay

import (
	"errors"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/goposta/posta/internal/models"
)

func TestMapSendError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantNil  bool
		wantCode int
	}{
		{name: "nil error accepted", err: nil, wantNil: true},
		{name: "rate limit", err: errors.New("rate_limit: too many requests"), wantCode: 452},
		{name: "domain verification", err: errors.New("domain_verification: sender domain not verified"), wantCode: 550},
		{name: "other error", err: errors.New("boom"), wantCode: 451},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapSendError(tt.err, "127.0.0.1:1234")
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil error, got %v", got)
				}
				return
			}
			smtpErr, ok := got.(*smtp.SMTPError)
			if !ok {
				t.Fatalf("expected *smtp.SMTPError, got %T", got)
			}
			if smtpErr.Code != tt.wantCode {
				t.Fatalf("expected code %d, got %d", tt.wantCode, smtpErr.Code)
			}
		})
	}
}

// ---------- SF-09: DELIVERBY capture on the relay session ----------

// newRelaySession builds a relay session with an authenticated credential and
// no guard (MarkCommand is nil-safe): the unit under test is the BY state
// machine, not the security guard.
func newRelaySession() *session {
	return &session{credential: &models.SMTPCredential{UserID: 7, WorkspaceID: 3}}
}

// The by-time rides RCPT TO: accepted values are captured, multiple
// recipients with different by-times resolve to the strictest (shortest).
func TestRelaySessionDeliverByCapturedStrictest(t *testing.T) {
	s := newRelaySession()
	if err := s.Rcpt("a@example.net", &smtp.RcptOptions{
		DeliverBy: &smtp.DeliverByOptions{Time: 120 * time.Second, Mode: smtp.DeliverByReturn},
	}); err != nil {
		t.Fatalf("RCPT with BY=120: %v", err)
	}
	if err := s.Rcpt("b@example.net", &smtp.RcptOptions{
		DeliverBy: &smtp.DeliverByOptions{Time: 60 * time.Second, Mode: smtp.DeliverByNotify},
	}); err != nil {
		t.Fatalf("RCPT with BY=60: %v", err)
	}
	if s.deliverBy != 60*time.Second {
		t.Fatalf("deliverBy = %s, want 60s (the strictest by-time)", s.deliverBy)
	}
	if len(s.to) != 2 {
		t.Fatalf("recipients = %v, want both captured", s.to)
	}

	// A plain recipient after a BY one leaves the captured by-time alone.
	if err := s.Rcpt("c@example.net", nil); err != nil {
		t.Fatalf("plain RCPT: %v", err)
	}
	if s.deliverBy != 60*time.Second {
		t.Fatalf("deliverBy after plain RCPT = %s, want unchanged 60s", s.deliverBy)
	}
}

// A by-time the server cannot honor — non-positive or above the 30-day cap —
// is refused with a 501 before DATA (RFC 2852 §4.2 posture) and never
// captured.
func TestRelaySessionDeliverByRefusesNonHonorableByTime(t *testing.T) {
	s := newRelaySession()

	err := s.Rcpt("dest@example.net", &smtp.RcptOptions{
		DeliverBy: &smtp.DeliverByOptions{Time: 0, Mode: smtp.DeliverByNotify},
	})
	smtpErr, ok := err.(*smtp.SMTPError)
	if !ok || smtpErr.Code != 501 {
		t.Fatalf("BY=0 error = %v, want 501 SMTPError", err)
	}
	if s.deliverBy != 0 {
		t.Fatalf("deliverBy after refused BY=0 = %s, want 0 (never captured)", s.deliverBy)
	}

	err = s.Rcpt("dest@example.net", &smtp.RcptOptions{
		DeliverBy: &smtp.DeliverByOptions{Time: 31 * 24 * time.Hour, Mode: smtp.DeliverByNotify},
	})
	smtpErr, ok = err.(*smtp.SMTPError)
	if !ok || smtpErr.Code != 501 {
		t.Fatalf("oversize BY error = %v, want 501 SMTPError", err)
	}
	if s.deliverBy != 0 {
		t.Fatalf("deliverBy after refused oversize BY = %s, want 0", s.deliverBy)
	}

	// A transaction reset clears the captured state.
	if err := s.Rcpt("ok@example.net", &smtp.RcptOptions{
		DeliverBy: &smtp.DeliverByOptions{Time: 90 * time.Second, Mode: smtp.DeliverByReturn},
	}); err != nil {
		t.Fatalf("RCPT with BY=90: %v", err)
	}
	s.Reset()
	if s.deliverBy != 0 {
		t.Fatalf("deliverBy after Reset = %s, want 0", s.deliverBy)
	}
}
