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

package storage

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// SF-106 tests: the health-driven primary/replica DSN selector's failover
// logic, probed with a FAKE connection source (a map of DSN → error) — the
// ask's testable application-side preparation. The real-probe wiring is
// pinned separately against live Postgres (TestPGConnectWithFailover).

// fakeProbe builds a DSNProbe from a reachability map (nil = reachable).
func fakeProbe(state map[string]error) DSNProbe {
	return func(dsn string) error { return state[dsn] }
}

const (
	fpPrimary  = "host=primary.internal user=posta password=h dbname=porta port=5432"
	fpStandby  = "host=standby.internal user=posta password=h dbname=porta port=5432"
	fpStandby2 = "host=standby-2.internal user=posta password=h dbname=porta port=5432"
)

func TestFailoverSelectorPicksPrimaryWhenHealthy(t *testing.T) {
	s := NewFailoverSelector([]string{fpPrimary, fpStandby}, fakeProbe(map[string]error{}))
	got, err := s.Pick()
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got != fpPrimary {
		t.Fatalf("pick = %q, want the primary", got)
	}
	if s.Active() != fpPrimary {
		t.Fatalf("active = %q, want the primary", s.Active())
	}
}

// TestFailoverSelectorFailsOverOnPrimaryFailure — a primary probe failure
// fails over to the next reachable standby and the active position advances.
func TestFailoverSelectorFailsOverOnPrimaryFailure(t *testing.T) {
	state := map[string]error{fpPrimary: errors.New("connection refused")}
	s := NewFailoverSelector([]string{fpPrimary, fpStandby}, fakeProbe(state))

	got, err := s.Pick()
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got != fpStandby {
		t.Fatalf("pick = %q, want the standby after the primary failure", got)
	}
	if s.Active() != fpStandby {
		t.Fatalf("active = %q, want the standby", s.Active())
	}
}

// TestFailoverSelectorSkipsIntermediateFailures — multiple down entries are
// skipped until a reachable one (the probe trail carries every failure).
func TestFailoverSelectorSkipsIntermediateFailures(t *testing.T) {
	state := map[string]error{
		fpPrimary: errors.New("connection refused"),
		fpStandby: errors.New("connection refused"),
	}
	s := NewFailoverSelector([]string{fpPrimary, fpStandby, fpStandby2}, fakeProbe(state))

	got, err := s.Pick()
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got != fpStandby2 {
		t.Fatalf("pick = %q, want the second standby", got)
	}
}

// TestFailoverSelectorNoReachableEndpoint — every entry down is an error
// carrying the probe trail (the caller fails loudly, never silently
// mis-routes).
func TestFailoverSelectorNoReachableEndpoint(t *testing.T) {
	state := map[string]error{
		fpPrimary: errors.New("connection refused"),
		fpStandby: errors.New("connection refused"),
	}
	s := NewFailoverSelector([]string{fpPrimary, fpStandby}, fakeProbe(state))
	if _, err := s.Pick(); err == nil {
		t.Fatal("all-down pick must fail")
	} else if !strings.Contains(err.Error(), "no reachable endpoint") {
		t.Fatalf("err = %v, want the no-reachable-endpoint trail", err)
	}
}

// TestFailoverSelectorRePromotesPrimaryOnRecovery — the health-driven part:
// after a failover, a RECOVERED primary is re-promoted on the next pick
// (the selector prefers the head of the list when it is reachable again).
func TestFailoverSelectorRePromotesPrimaryOnRecovery(t *testing.T) {
	state := map[string]error{fpPrimary: errors.New("connection refused")}
	s := NewFailoverSelector([]string{fpPrimary, fpStandby}, fakeProbe(state))

	// Fail over.
	got, err := s.Pick()
	if err != nil || got != fpStandby {
		t.Fatalf("first pick = %q, %v; want the standby", got, err)
	}

	// The primary recovers; the next pick re-promotes it.
	delete(state, fpPrimary)
	got, err = s.Pick()
	if err != nil {
		t.Fatalf("pick after recovery: %v", err)
	}
	if got != fpPrimary {
		t.Fatalf("pick = %q, want the re-promoted primary", got)
	}
	if s.Active() != fpPrimary {
		t.Fatalf("active = %q, want the primary", s.Active())
	}
}

// TestConnectWithFailoverRequiresTwoEntries — failover semantics with one
// entry is a lie; the constructor refuses it.
func TestConnectWithFailoverRequiresTwoEntries(t *testing.T) {
	if _, _, err := ConnectWithFailover([]string{fpPrimary}); err == nil {
		t.Fatal("ConnectWithFailover with one entry must fail")
	}
}

// TestPGConnectWithFailoverChoosesReachable — the REAL probe wiring: with the
// live Postgres as the primary and an unreachable standby, the selector opens
// the primary; with the live Postgres as the SECOND entry behind an
// unreachable primary, it fails over to it. TEST_DATABASE_DSN-gated.
func TestPGConnectWithFailoverChoosesReachable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skipf("skipping: TEST_DATABASE_DSN not set")
	}
	dead := "host=unreachable.invalid user=posta password=x dbname=porta port=5432 sslmode=disable connect_timeout=1"

	// Primary reachable.
	db, chosen, err := ConnectWithFailover([]string{dsn, dead})
	if err != nil {
		t.Fatalf("failover connect (primary healthy): %v", err)
	}
	if chosen != dsn {
		t.Fatalf("chosen = %q, want the primary", chosen)
	}
	var one int64
	if err := db.Raw("SELECT 1").Scan(&one).Error; err != nil || one != 1 {
		t.Fatalf("query on the chosen endpoint: %v (%d)", err, one)
	}

	// Primary dead: the selector fails over to the live standby.
	db2, chosen2, err := ConnectWithFailover([]string{dead, dsn})
	if err != nil {
		t.Fatalf("failover connect (primary dead): %v", err)
	}
	if chosen2 != dsn {
		t.Fatalf("chosen = %q, want the standby after the primary failure", chosen2)
	}
	var two int64
	if err := db2.Raw("SELECT 2").Scan(&two).Error; err != nil || two != 2 {
		t.Fatalf("query on the failed-over endpoint: %v (%d)", err, two)
	}
}
