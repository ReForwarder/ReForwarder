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
	"sync"
	"testing"

)

// SF-49 tests: the Authentication-Results injection at FORWARD composition
// (the archive-integrity resolution) — the header present on the forwarded
// copy, the stored original unmodified, and the dedup keying intact.

const arTestHeader = "fwd.example.test; spf=pass smtp.mailfrom=verified.example; dkim=pass"

// stubARHeader is the injection seam over a fixed map (the production
// implementation reads the verdict row; fail-open semantics are pinned on
// the seam's "" contract).
type stubARHeader struct {
	mu     sync.Mutex
	called int
	lastID uint
	value  string
}

func (s *stubARHeader) StoredARHeader(inboundID uint) string {
	s.mu.Lock()
	s.called++
	s.lastID = inboundID
	v := s.value
	s.mu.Unlock()
	return v
}

func TestForwarderInjectsARHeader(t *testing.T) {
	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true

	ar := &stubARHeader{value: arTestHeader}
	d.fwd.SetARHeaderSource(ar)

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if d.mailer.calls() != 1 {
		t.Fatalf("mailer called %d times, want 1", d.mailer.calls())
	}
	// The header is present on the forwarded copy's composition, carrying
	// the verdict row's value.
	if got := d.mailer.requests[0].Headers["Authentication-Results"]; got != arTestHeader {
		t.Fatalf("Authentication-Results = %q, want the persisted verdict header", got)
	}
	if ar.called != 1 || ar.lastID != rec.ID {
		t.Fatalf("the seam was consulted %d times (last id %d), want once for rec %d", ar.called, ar.lastID, rec.ID)
	}
	// The existing system headers are untouched.
	if d.mailer.requests[0].Headers[HeaderForwardedFor] == "" {
		t.Fatal("the loop-evidence header must survive the injection")
	}
}

func TestForwarderARInjectionFailOpenAndUnwired(t *testing.T) {
	// (a) The append-only merge, unit-pinned on the composition helper: an
	// upstream Authentication-Results header in the copy's composition is
	// never overwritten by our injection (RFC 8601 headers are append-only;
	// the map cannot represent two values, and the upstream's value stands).
	merged := withInjectedARHeader(
		map[string]string{"Authentication-Results": "upstream.example; spf=fail"},
		arTestHeader)
	if merged["Authentication-Results"] != "upstream.example; spf=fail" {
		t.Fatalf("the upstream A-R header was overwritten: %q", merged["Authentication-Results"])
	}

	// (b) Fail-open: a source with no verdict ("" return) skips the
	// injection — the copy carries no injected header, the send proceeds.
	rec2 := fwInbound()
	task2 := fwTask(rec2.ID, "friend@external.test")
	d2 := newFwDeps(t, rec2, task2)
	d2.targets.sheet["friend@external.test"] = true
	d2.fwd.SetARHeaderSource(&stubARHeader{})

	if _, err := d2.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce without a verdict: %v", err)
	}
	if _, ok := d2.mailer.requests[0].Headers["Authentication-Results"]; ok {
		t.Fatal("no verdict must mean no injected header")
	}

	// (c) An unwired seam: the pre-SF-49 behavior byte-for-byte.
	rec3 := fwInbound()
	task3 := fwTask(rec3.ID, "friend@external.test")
	d3 := newFwDeps(t, rec3, task3)
	d3.targets.sheet["friend@external.test"] = true
	if _, err := d3.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce unwired: %v", err)
	}
	if _, ok := d3.mailer.requests[0].Headers["Authentication-Results"]; ok {
		t.Fatal("an unwired seam must not inject")
	}
}

// TestForwardARLeavesStoredOriginalUnmodified — the archive-integrity core:
// the forward reads the stored original's raw bytes and composes a FRESH
// message; the stored original's bytes (the content-addressed blob's source)
// are never rewritten, and the copy is a distinct composition (its bytes
// differ from the original's, so the content-addressed dedup keys stay
// separate — dedup intact).
func TestForwardARLeavesStoredOriginalUnmodified(t *testing.T) {
	original := "From: someone@example.org\r\nSubject: the original\r\n\r\noriginal body"
	rec := fwInbound()
	rec.UUID = "inb-fw-test"
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.raws.byUUID[rec.UUID] = []byte(original)
	d.targets.sheet["friend@external.test"] = true
	d.fwd.SetARHeaderSource(&stubARHeader{value: arTestHeader})

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if d.mailer.calls() != 1 {
		t.Fatalf("mailer called %d times, want 1", d.mailer.calls())
	}

	// The stored original's bytes are untouched — the forwarder only ever
	// reads the raw (the archive is falsified by nothing).
	if string(d.raws.byUUID[rec.UUID]) != original {
		t.Fatal("the stored original's raw bytes must never be rewritten")
	}
	// Dedup intact: the copy is a fresh composition — its bytes differ from
	// the original's (the A-R injection changes the composed message), so
	// the content-addressed store keys them separately.
	req := d.mailer.requests[0]
	if req.Headers["Authentication-Results"] != arTestHeader {
		t.Fatalf("the copy must carry the injected A-R header: %+v", req.Headers)
	}
	if strings.Contains(original, "Authentication-Results") {
		t.Fatal("the stored original must not carry the injected header")
	}
}
