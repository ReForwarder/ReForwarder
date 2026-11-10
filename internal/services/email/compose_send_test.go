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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package email

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/storage/repositories"
)

// OB-5 W3 (ADR-005 decision 2): SendMailCompose — the webmail compose seam's
// outbound entry. These tests prove, over the same fake-driver harness as the
// system-forward tests, that the created row is stamped provenance=mail-compose,
// that the pipeline enqueue uses the standard email:send task, that the
// X-Posta-Email-Uuid linkage header is stored, and that the composed event
// opens the message's email_events timeline.

// TestComposeSendStampsProvenanceAndEnqueues is the compose happy path.
func TestComposeSendStampsProvenanceAndEnqueues(t *testing.T) {
	state := &sfState{returningID: 91}
	db := sfGorm(t, state)
	enq := &stubEnqueuer{}
	svc := NewService(repositories.NewEmailRepository(db), nil, nil, nil, nil, nil, false)
	svc.SetEnqueuer(enq)
	svc.SetRecorder(emailhistory.NewRecorder(db))

	resp, err := svc.SendMailCompose(context.Background(), &MailComposeRequest{
		UUID:        "compose-uuid-1",
		UserID:      7,
		From:        "ada@example.test",
		To:          []string{"bob@external.test", "carol@hidden.test"},
		Subject:     "Hello from webmail",
		Text:        "plain body",
		HTML:        "<p>html body</p>",
		MessageID:   "compose-uuid-1@example.test",
		MailboxID:   3,
	})
	if err != nil {
		t.Fatalf("SendMailCompose: %v", err)
	}
	// resp.Status is asserted; resp.ID is not — the fake driver's INSERT
	// RETURNING overwrites UUID-shaped columns with the row id (the same
	// harness limitation the system-forward tests work around). The
	// fixed-UUID behavior is proven through the headers_json bound value
	// below.
	if resp.Status != models.EmailStatusQueued {
		t.Errorf("resp = %+v, want queued status", resp)
	}
	if len(enq.enqueued) != 1 || enq.enqueued[0] != 91 {
		t.Errorf("enqueued = %v, want [91] (standard email:send task)", enq.enqueued)
	}

	// The row carries the mail-compose provenance stamp and the linkage
	// headers (bound values, not source-code shapes).
	inserts := state.insertsFor("emails")
	if len(inserts) != 1 {
		t.Fatalf("emails inserts = %d, want 1", len(inserts))
	}
	if got := fmt.Sprint(inserts[0].values["provenance"]); got != string(models.EmailProvenanceMailCompose) {
		t.Errorf("provenance = %v, want %q", got, models.EmailProvenanceMailCompose)
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(fmt.Sprint(inserts[0].values["headers_json"])), &headers); err != nil {
		t.Fatalf("decode headers_json: %v (raw %v)", err, inserts[0].values["headers_json"])
	}
	if headers["X-Posta-Email-Uuid"] != "compose-uuid-1" {
		t.Errorf("linkage header = %q", headers["X-Posta-Email-Uuid"])
	}
	if headers["Message-ID"] != "<compose-uuid-1@example.test>" {
		t.Errorf("message-id header = %q", headers["Message-ID"])
	}
	// The composed event opens the email_events timeline.
	events := state.insertsFor("email_events")
	if len(events) != 1 {
		t.Fatalf("email_events inserts = %d, want 1 (composed)", len(events))
	}
	if got := fmt.Sprint(events[0].values["type"]); got != models.EmailEventComposed {
		t.Errorf("event type = %v, want %q", got, models.EmailEventComposed)
	}
}

// TestComposeSendDevModeStoresWithoutSend mirrors the dev short-circuit of
// every send path: stored and marked sent, nothing enqueued.
func TestComposeSendDevModeStoresWithoutSend(t *testing.T) {
	state := &sfState{returningID: 5}
	db := sfGorm(t, state)
	enq := &stubEnqueuer{}
	svc := NewService(repositories.NewEmailRepository(db), nil, nil, nil, nil, nil, true)
	svc.SetEnqueuer(enq)
	svc.SetRecorder(emailhistory.NewRecorder(db))

	resp, err := svc.SendMailCompose(context.Background(), &MailComposeRequest{
		UserID: 7, From: "ada@example.test", To: []string{"bob@external.test"},
		Subject: "dev", Text: "x",
	})
	if err != nil {
		t.Fatalf("SendMailCompose: %v", err)
	}
	if resp.Status != models.EmailStatusSent {
		t.Errorf("status = %q, want sent (dev store-without-send)", resp.Status)
	}
	if len(enq.enqueued) != 0 {
		t.Errorf("dev mode enqueued %d sends", len(enq.enqueued))
	}
}

// TestComposeSendRefusesBrokenInput keeps the fail-fast contract: no
// recipients, header injection, and an empty request all refuse before any
// state is created.
func TestComposeSendRefusesBrokenInput(t *testing.T) {
	state := &sfState{returningID: 1}
	db := sfGorm(t, state)
	svc := NewService(repositories.NewEmailRepository(db), nil, nil, nil, nil, nil, false)

	cases := []struct {
		name string
		req  *MailComposeRequest
	}{
		{"no recipients", &MailComposeRequest{From: "a@b.test", Subject: "s", Text: "t"}},
		{"bad from", &MailComposeRequest{From: "not-an-address", To: []string{"x@y.test"}, Subject: "s"}},
		{"header injection", &MailComposeRequest{From: "a@b.test", To: []string{"x@y.test"}, Subject: "line1\r\nBcc: victim@y.test"}},
		{"nil request", nil},
	}
	for _, tc := range cases {
		if _, err := svc.SendMailCompose(context.Background(), tc.req); err == nil {
			t.Errorf("%s: accepted", tc.name)
		} else if !strings.Contains(err.Error(), "compose") && !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "header") && !strings.Contains(err.Error(), "recipient") {
			t.Errorf("%s: unexpected error shape %v", tc.name, err)
		}
	}
	if len(state.insertsFor("emails")) != 0 {
		t.Error("refused requests created email rows")
	}
}
