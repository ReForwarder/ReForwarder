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
	"strings"
	"testing"
)

// SF-12 tests: the forwarder stamps the SRS envelope on every forwarded
// copy (the ADR-001 amendment); the feature off leaves the request untouched,
// and an unusable inbound UUID degrades to no envelope rewrite — never to a
// broken send. The forwarder_test.go fixture (fwDeps/fwMailer/fwInbound)
// drives the executor.

// TestForwarderStampsSRSReturnPath — with the SRS codec wired, the copy's
// request carries the srs0+ return path on the forwarder domain, decodable
// back to the original inbound record's UUID; the visible From stays the
// forward-from address.
func TestForwarderStampsSRSReturnPath(t *testing.T) {
	rec := fwInbound()
	rec.UUID = srsTestUUID // the codec requires a 32-hex UUID payload
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true

	v := NewVerp(VerpConfig{Subdomain: "bounce", Secret: "srs-forwarder-secret"})
	d.fwd.SetSRS(v)

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if d.mailer.calls() != 1 {
		t.Fatalf("mailer called %d times, want 1", d.mailer.calls())
	}
	rp := d.mailer.requests[0].ReturnPath
	if !strings.HasPrefix(rp, "srs0+") || !strings.HasSuffix(rp, "@lists.example") {
		t.Fatalf("ReturnPath = %q, want the srs0+ address on the forwarder domain", rp)
	}
	decoded, ok := v.DecodeForwarded(rp)
	if !ok || decoded != rec.UUID {
		t.Fatalf("decode = %v/%q, want the original inbound record's UUID %q", ok, decoded, rec.UUID)
	}
	if d.mailer.requests[0].From != "inbox@lists.example" {
		t.Fatalf("From = %q, want the forward-from address (unchanged)", d.mailer.requests[0].From)
	}
}

// TestForwarderSRSOffLeavesRequestUntouched — the codec not wired (the
// feature off) leaves the request's ReturnPath empty; an unusable inbound
// UUID (the encode refuses) also degrades to no envelope rewrite — never to
// a broken send.
func TestForwarderSRSOffLeavesRequestUntouched(t *testing.T) {
	// Off: the codec unwired.
	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true
	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if d.mailer.requests[0].ReturnPath != "" {
		t.Fatalf("ReturnPath = %q, want empty with the codec unwired", d.mailer.requests[0].ReturnPath)
	}

	// Wired, but the inbound UUID is unusable: the encode refuses, the send
	// proceeds without the envelope rewrite.
	rec2 := fwInbound()
	rec2.UUID = "not-a-uuid"
	task2 := fwTask(rec2.ID, "friend@external.test")
	d2 := newFwDeps(t, rec2, task2)
	d2.targets.sheet["friend@external.test"] = true
	d2.fwd.SetSRS(NewVerp(VerpConfig{Subdomain: "bounce", Secret: "srs-forwarder-secret"}))
	if _, err := d2.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce with an unusable UUID: %v", err)
	}
	if d2.mailer.requests[0].ReturnPath != "" {
		t.Fatalf("ReturnPath = %q, want empty (the encode refused)", d2.mailer.requests[0].ReturnPath)
	}
}
