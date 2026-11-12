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

package mta

import (
	"context"
	"strings"
	"testing"
)

// SF-13: the outbound hop prepends a Received: trace header to the message
// actually sent to the remote MX — MX host plus the attempt identity (email
// id + attempt ordinal) as the queue id — while the caller's payload stays
// untouched so a retry prepends its own fresh hop.
func TestDirectDeliveryPrependsReceivedTraceHeader(t *testing.T) {
	f := newDeliverFixture(t)

	env := testEnvelope("rcpt@example.com")
	original := string(env.Msg)
	if _, err := f.executor.Deliver(context.Background(), testSpec(), env); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	bodies := f.srv.bodiesSeen()
	if len(bodies) != 1 {
		t.Fatalf("bodies seen = %d, want 1", len(bodies))
	}
	sent := bodies[0]
	if !strings.HasPrefix(sent, "Received: by ") {
		t.Fatalf("sent message must start with the trace header: %q", sent[:min(len(sent), 80)])
	}
	// MX host and queue id (email 42, first attempt) both appear.
	if !strings.Contains(sent, "for delivery to 127.0.0.1") {
		t.Fatalf("trace header must name the MX host: %q", sent)
	}
	if !strings.Contains(sent, "(Posta, id 42.1)") {
		t.Fatalf("trace header must carry the attempt queue id: %q", sent)
	}
	// The original message follows below the inserted hop. The harness
	// records bodies newline-joined (CR stripped, no trailing newline), so
	// compare on the normalized, line-content form.
	normalizedOriginal := strings.TrimSuffix(strings.ReplaceAll(original, "\r\n", "\n"), "\n")
	if !strings.Contains(sent, normalizedOriginal) {
		t.Fatalf("original message must be preserved under the trace header")
	}
	// The caller's envelope must not be mutated (a retry must not stack hops
	// on shared bytes).
	if string(env.Msg) != original {
		t.Fatal("Deliver must not mutate the caller's message buffer")
	}
}

// A second delivery attempt (fresh envelope, same executor) prepends its own
// hop — headers accumulate per hop, newest first.
func TestDirectDeliveryEachAttemptGetsOwnHop(t *testing.T) {
	f := newDeliverFixture(t)

	// One attempt per Deliver: two emails → two independent hops.
	if _, err := f.executor.Deliver(context.Background(), testSpec(), testEnvelope("rcpt1@example.com")); err != nil {
		t.Fatalf("first deliver: %v", err)
	}
	// Second email: the worker stamps a new email id.
	env2 := testEnvelope("rcpt2@example.com")
	env2.EmailID = 43
	if _, err := f.executor.Deliver(context.Background(), testSpec(), env2); err != nil {
		t.Fatalf("second deliver: %v", err)
	}

	bodies := f.srv.bodiesSeen()
	if len(bodies) != 2 {
		t.Fatalf("bodies seen = %d, want 2", len(bodies))
	}
	if !strings.Contains(bodies[1], "(Posta, id 43.1)") {
		t.Fatalf("second email's hop must carry its own queue id: %q", bodies[1])
	}
}

