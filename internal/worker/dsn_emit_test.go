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
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
)

// SF-10: the emission rules for RFC 3464 notifications — terminal failures
// of locally-originated mail notify the sender; system rows and DSNs never
// do (double-bounce stop), and the feature is off unless enabled.

type fakeDSNMailer struct {
	reqs []*email.DSNRequest
	err  error
}

func (f *fakeDSNMailer) SendDSN(_ context.Context, req *email.DSNRequest) (*email.SendResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.reqs = append(f.reqs, req)
	return &email.SendResponse{ID: "dsn-1", Status: models.EmailStatusQueued}, nil
}

func wsRef(id uint) *uint { return &id }

func failedEmail() *models.Email {
	return &models.Email{
		ID:          7,
		UUID:        "uuid-7",
		UserID:      3,
		WorkspaceID: wsRef(9),
		Sender:      "alice@example.org",
		Recipients:  []string{"dead@example.net"},
		Subject:     "hello",
		SMTPHostname: "mx.example.net",
		HeadersJSON: `{"Message-ID":"<orig@example.org>"}`,
	}
}

func dsnRejection() *email.SendError {
	return &email.SendError{
		Stage: "RCPT TO", Code: 550,
		Msg: "550 5.1.1 user unknown",
	}
}

func TestEmitDSNForEligibleRow(t *testing.T) {
	m := &fakeDSNMailer{}
	em := failedEmail()
	emitDSNFor(m, true, "mx.posta.example", em, dsnRejection())

	if len(m.reqs) != 1 {
		t.Fatalf("DSN requests = %d, want 1", len(m.reqs))
	}
	req := m.reqs[0]
	if req.Sender != "alice@example.org" {
		t.Errorf("sender = %q", req.Sender)
	}
	if req.ReportingMTA != "mx.posta.example" {
		t.Errorf("reporting MTA = %q", req.ReportingMTA)
	}
	if req.UserID != 3 || req.WorkspaceID != 9 {
		t.Errorf("owner = user %d ws %d, want 3/9", req.UserID, req.WorkspaceID)
	}
	if len(req.Recipients) != 1 || req.Recipients[0].Recipient != "dead@example.net" {
		t.Fatalf("recipients = %+v", req.Recipients)
	}
	r := req.Recipients[0]
	if r.Action != "failed" || r.Status != "5.1.1" {
		t.Errorf("action/status = %s/%s, want failed/5.1.1", r.Action, r.Status)
	}
	if r.Diagnostic != "550 5.1.1 user unknown" {
		t.Errorf("diagnostic = %q", r.Diagnostic)
	}
	if r.RemoteMTA != "mx.example.net" {
		t.Errorf("remote MTA = %q", r.RemoteMTA)
	}
}

func TestEmitDSNGateDisabledByDefault(t *testing.T) {
	m := &fakeDSNMailer{}
	emitDSNFor(m, false, "mx.posta.example", failedEmail(), dsnRejection())
	emitDSNFor(nil, true, "mx.posta.example", failedEmail(), dsnRejection())
	if len(m.reqs) != 0 {
		t.Fatalf("DSN emitted while disabled/unwired: %d", len(m.reqs))
	}
}

func TestEmitDSNDoubleBounceStop(t *testing.T) {
	m := &fakeDSNMailer{}
	for _, provenance := range []string{models.EmailProvenanceSystemForward, models.EmailProvenanceSystemDSN} {
		em := failedEmail()
		em.Provenance = provenance
		emitDSNFor(m, true, "mx.posta.example", em, dsnRejection())
	}
	if len(m.reqs) != 0 {
		t.Fatalf("system rows must never notify; got %d DSNs", len(m.reqs))
	}
}

// The exhausted path (no live SMTP verdict) still carries the stored error
// text and any enhanced code the remote quoted into it.
func TestEmitDSNFromExhaustionCarriesStoredDiagnostic(t *testing.T) {
	m := &fakeDSNMailer{}
	em := failedEmail()
	em.ErrorMessage = "permanently failed after retries: 550 5.7.1 relay denied"
	emitDSNFor(m, true, "mx.posta.example", em, nil)

	if len(m.reqs) != 1 {
		t.Fatalf("DSN requests = %d, want 1", len(m.reqs))
	}
	r := m.reqs[0].Recipients[0]
	if r.Status != "5.7.1" {
		t.Errorf("status = %q, want 5.7.1 (parsed from the stored error)", r.Status)
	}
	if r.Diagnostic == "" {
		t.Error("stored error text must travel as the diagnostic")
	}
}

// The envelope for a DSN row is the NULL reverse path — an undeliverable DSN
// cannot bounce again.
func TestEnvelopeSenderNullPathForDSN(t *testing.T) {
	h := NewEmailSendHandler(nil, nil, nil, nil, nil, nil)
	em := failedEmail()
	em.Provenance = models.EmailProvenanceSystemDSN
	if got := h.envelopeSender(em); got != "" {
		t.Fatalf("DSN envelope = %q, want the null reverse path", got)
	}
	// Ordinary rows keep their envelope.
	if got := h.envelopeSender(failedEmail()); got != "alice@example.org" {
		t.Fatalf("ordinary envelope = %q, want the sender address", got)
	}
}
