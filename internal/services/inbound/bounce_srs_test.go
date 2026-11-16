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
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// emailBySRS returns the emails-table row carrying the given srs_return_path
// (the match-back's assertion key).
func (s *vbState) emailBySRS(path string) map[string]driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.table["emails"] {
		if v, ok := row["srs_return_path"]; ok && v == path {
			return row
		}
	}
	return nil
}

// SF-12 tests: the SRS return path on the bounce ingestion (the ADR-001
// amendment) — a validly signed srs0+ recipient resolves through the
// return-path allowlist to the forwarder domain, the DSN match-back resolves
// the forwarded copy through its persisted SRS return path, and a forged
// srs0+ address falls through to ordinary recipient handling (forgery
// refused). The bounce_test.go vb fixture drives everything.

// addForwardCopy seeds a system_forward copy email row carrying the SRS
// return path (the match-back's exact-address lookup key).
func (s *vbState) addForwardCopy(id uint, uuid string, wsID uint, sender, srsPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := map[string]driver.Value{
		"id":               int64(id),
		"uuid":             uuid,
		"user_id":          int64(11),
		"workspace_id":     int64(wsID),
		"sender":           sender,
		"recipients":       "{friend@elsewhere.example}",
		"status":           string(models.EmailStatusQueued),
		"provenance":       models.EmailProvenanceSystemForward,
		"srs_return_path":  srsPath,
		"created_at":       timeNowUTC(),
	}
	s.table["emails"] = append(s.table["emails"], row)
}

func timeNowUTC() time.Time { return time.Now().UTC() }

// TestSRSBounceRCPTAllowlistAndMatchBack — the full return path: a DSN
// arriving at the srs0+ address (a) is accepted at RCPT through the
// allowlist's SRS branch, (b) is durably ingested as a system bounce, and
// (c) matches back to the forwarded copy through its persisted SRS return
// path — the bounce lands on the copy's identity.
func TestSRSBounceRCPTAllowlistAndMatchBack(t *testing.T) {
	f := vbNewFixture(t)
	// The forwarder's verified domain for the SRS addresses.
	state := f.state
	state.addDomain(2, 11, vbWs(3), "fwd.example.test", true)

	inboundUUID := "d1c2b3a4-0000-4000-8000-00000000abc1"
	srsAddr, ok := f.verp.EncodeForwarded(inboundUUID, "fwd.example.test")
	if !ok {
		t.Fatal("encode the SRS envelope")
	}
	// The forwarded copy row carrying the address (the match-back key).
	state.addForwardCopy(77, "fwd-copy-uuid-1", 3, "forward@fwd.example.test", srsAddr)

	rec, err := f.svc.IngestRaw(context.Background(), vbBounceRaw(srsAddr, vbFailedStatusPart()),
		"MAILER-DAEMON@mx.remote.test", []string{srsAddr}, models.InboundSourceSMTP, "192.0.2.10")
	if err != nil {
		t.Fatalf("ingest the srs0+ bounce: %v", err)
	}
	if rec == nil || rec.ID == 0 {
		t.Fatal("the bounce must be durably persisted")
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received", rec.Status)
	}

	// The DSN truth landed on the forwarded copy: the copy's status flips to
	// bounced and a bounce row + email_events record carry its identity.
	copyRow := state.emailBySRS(srsAddr)
	if copyRow == nil {
		t.Fatal("the forwarded copy row vanished")
	}
	// The DSN truth matched back to the forwarded copy: the bounce row and
	// the email_events record carry the copy's identity (the same writes the
	// VERP match-back produces; the copy's own status flip rides the worker's
	// failure handling, not the matcher).
	if got := state.rowCount(t, "bounces"); got != 1 {
		t.Fatalf("bounce rows = %d, want 1 (matched back to the copy)", got)
	}
	var bounceEmailID int64
	for _, row := range state.rows(t, "bounces") {
		bounceEmailID, _ = row["email_id"].(int64)
	}
	if bounceEmailID != 77 {
		t.Fatalf("bounce.email_id = %v, want the forwarded copy's id 77", bounceEmailID)
	}
	if got := state.rowCount(t, "email_events"); got == 0 {
		t.Fatal("email_events must carry the DSN record")
	}
}

// TestSRSBounceForgedSignatureFallsThrough — a well-formed srs0+ address
// with a bogus signature falls through the allowlist to ordinary recipient
// handling: the forwarder domain is not a tenant mailbox domain, so the
// recipient is refused and nothing is persisted.
func TestSRSBounceForgedSignatureFallsThrough(t *testing.T) {
	f := vbNewFixture(t)
	// The forwarder domain is deliberately NOT seeded here: the forged
	// signature fails the SRS decode, falls through to ordinary recipient
	// handling, and the unverified host refuses the recipient (the VERP
	// forged fall-through posture, preserved for SRS).

	bogus := "srs0+" + strings.Repeat("a", 32) + "=" + strings.Repeat("9", srsSigHexLen) + "@fwd.example.test"
	_, err := f.svc.IngestRaw(context.Background(), []byte("Subject: x\r\n\r\nhi\r\n"),
		"MAILER-DAEMON@mx.remote.test", []string{bogus}, models.InboundSourceSMTP, "192.0.2.10")
	if !errors.Is(err, ErrUnverifiedDomain) {
		t.Fatalf("a forged srs0+ recipient = %v, want ErrUnverifiedDomain (forgery refused)", err)
	}
	if got := f.state.rowCount(t, "inbound_emails"); got != 0 {
		t.Fatalf("inbound_emails rows = %d, want 0 (nothing persisted)", got)
	}
}
