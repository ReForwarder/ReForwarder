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
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// SF-42 tests: the spam-trap gate (the ADR-006 amendment) — a matching trap
// recipient quarantines instantly through the ordinary disposition path
// (explicit + event-backed); a lookup error fails open (the scoring pipeline
// continues); an unwired seam and system mail skip the gate entirely.

type stubTraps struct {
	mu     sync.Mutex
	called int
	lastD  string
	lastL  string
	hit    bool
	err    error
}

func (s *stubTraps) HasRecipient(workspaceID uint, domainName, localPart string) (bool, error) {
	s.mu.Lock()
	s.called++
	s.lastD = domainName
	s.lastL = localPart
	hit, err := s.hit, s.err
	s.mu.Unlock()
	return hit, err
}

func (s *stubTraps) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called
}

func TestSpamTrapHitQuarantines(t *testing.T) {
	h := newWfHarness(t)
	w := uint(5)
	rec := h.newRecord(t, func(r *models.InboundEmail) {
		r.Recipients = []string{"Trap@prio.example"}
		r.WorkspaceID = &w
	})

	stub := &stubTraps{hit: true}

	f := build(h, stubFilter{score: 0.5}, nil, nil, InboundFilterOptions{Enabled: true})
	f.SetTrapChecker(stub)

	if f.Run(context.Background(), rec) {
		t.Fatal("a trap hit must dispose the record")
	}
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %q, want quarantined", rec.Status)
	}
	if !strings.Contains(rec.SpamReasons, "spam_trap_hit:trap@prio.example") {
		t.Fatalf("reasons = %q, want the normalized trap-hit reason", rec.SpamReasons)
	}
	if stub.count() != 1 || stub.lastD != "prio.example" || stub.lastL != "trap" {
		t.Fatalf("lookup = %d/%q/%q, want the normalized parts", stub.count(), stub.lastD, stub.lastL)
	}
	if events := h.drain(t); len(events) == 0 {
		t.Fatal("a trap hit must publish events (explicit, never a silent drop)")
	}
}

func TestSpamTrapMissAndFailOpen(t *testing.T) {
	h := newWfHarness(t)
	w := uint(5)
	rec := h.newRecord(t, func(r *models.InboundEmail) {
		r.Recipients = []string{"real@prio.example"}
		r.WorkspaceID = &w
	})

	// A non-trap recipient: the gate passes and scoring proceeds.
	stub := &stubTraps{hit: false}
	f := build(h, stubFilter{score: 0.5}, nil, nil, InboundFilterOptions{Enabled: true})
	f.SetTrapChecker(stub)
	if !f.Run(context.Background(), rec) {
		t.Fatal("a non-trap recipient must proceed to scoring")
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received (the low score delivers)", rec.Status)
	}

	// Fail-open: a lookup error logs and continues to scoring — the score
	// pipeline still gates the message (the low-score stub delivers).
	errStub := &stubTraps{err: errors.New("trap store down")}
	f2 := build(h, stubFilter{score: 0.5}, nil, nil, InboundFilterOptions{Enabled: true})
	f2.SetTrapChecker(errStub)
	rec2 := h.newRecord(t, func(r *models.InboundEmail) {
		r.Recipients = []string{"real@prio.example"}
		r.WorkspaceID = &w
	})
	if !f2.Run(context.Background(), rec2) {
		t.Fatal("the pipeline must continue past a lookup error")
	}
	if rec2.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received (fail-open continued to scoring and delivered)", rec2.Status)
	}
}

func TestSpamTrapUnwiredAndSystemMail(t *testing.T) {
	h := newWfHarness(t)

	// An unwired seam: the gate is disabled, scoring proceeds.
	rec := h.newRecord(t, func(r *models.InboundEmail) {
		r.Recipients = []string{"trap@prio.example"}
	})
	f := build(h, stubFilter{score: 0.5}, nil, nil, InboundFilterOptions{Enabled: true})
	if !f.Run(context.Background(), rec) {
		t.Fatal("an unwired trap seam must proceed")
	}

	// System mail (nil workspace) skips the gate — traps are workspace-
	// scoped and system mail carries no workspace.
	f2 := build(h, stubFilter{score: 0.5}, nil, nil, InboundFilterOptions{Enabled: true})
	stub := &stubTraps{hit: true}
	f2.SetTrapChecker(stub)
	rec2 := h.newRecord(t, func(r *models.InboundEmail) {
		r.Recipients = []string{"trap@prio.example"}
		r.WorkspaceID = nil
	})
	if !f2.Run(context.Background(), rec2) {
		t.Fatal("system mail must skip the trap gate")
	}
	if stub.count() != 0 {
		t.Fatalf("the trap seam was consulted %d times for system mail, want 0", stub.count())
	}
}
