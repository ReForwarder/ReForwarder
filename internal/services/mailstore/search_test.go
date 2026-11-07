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

package mailstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Unit tests for the input guards of PostgresSearcher that must hold without
// a database: a blank query short-circuits before any SQL runs, an over-long
// query is refused with the typed error, and the constructor demands a db.
// The tsvector semantics themselves are proven against real Postgres in
// search_pg_test.go (the fake driver cannot express tsvector).

// TestSearcherImplementsInterface pins the W1 contract: PostgresSearcher is a
// Searcher.
func TestSearcherImplementsInterface(t *testing.T) {
	var s Searcher = (*PostgresSearcher)(nil)
	_ = s
}

// TestSearcherBlankQueryReturnsEmptyWithoutDB — a blank query must never
// reach the database (it would return zero matches anyway): empty page, zero
// total, no error. The searcher is constructed with a nil db on purpose: if
// the guard ever regresses this panics instead of passing.
func TestSearcherBlankQueryReturnsEmptyWithoutDB(t *testing.T) {
	s := &PostgresSearcher{}
	for _, text := range []string{"", "   ", " \t\n\r "} {
		hits, total, err := s.Search(context.Background(), SearchScope{MailboxID: 1, UserID: 1}, SearchQuery{Text: text})
		if err != nil {
			t.Fatalf("blank query %q: err = %v, want nil", text, err)
		}
		if total != 0 {
			t.Fatalf("blank query %q: total = %d, want 0", text, total)
		}
		if len(hits) != 0 {
			t.Fatalf("blank query %q: hits = %d, want 0", text, len(hits))
		}
	}
}

// TestSearcherRejectsOverlongQuery — a query past MaxSearchQueryRunes is
// refused with ErrSearchQueryTooLong (errors.Is-mappable), before any SQL.
func TestSearcherRejectsOverlongQuery(t *testing.T) {
	s := &PostgresSearcher{}
	long := strings.Repeat("ö", MaxSearchQueryRunes+1) // 513 runes, 1026 bytes
	hits, total, err := s.Search(context.Background(), SearchScope{MailboxID: 1, UserID: 1}, SearchQuery{Text: long})
	if !errors.Is(err, ErrSearchQueryTooLong) {
		t.Fatalf("overlong query: err = %v, want ErrSearchQueryTooLong", err)
	}
	if hits != nil || total != 0 {
		t.Fatalf("overlong query: hits = %v total = %d, want nil/0", hits, total)
	}
}

// TestSearcherBoundaryQueryLengthAccepted — exactly MaxSearchQueryRunes runes
// passes the guard (the failure mode must be the limit, not an off-by-one
// rejecting legal queries). Still no db touched: the text is blank-safe? No —
// it is non-blank, so the searcher would hit the db; the guard check itself
// is what we pin by expecting ErrSearchUnavailable (nil db) rather than
// ErrSearchQueryTooLong.
func TestSearcherBoundaryQueryLengthAccepted(t *testing.T) {
	s := &PostgresSearcher{}
	text := strings.Repeat("a", MaxSearchQueryRunes)
	_, _, err := s.Search(context.Background(), SearchScope{MailboxID: 1, UserID: 1}, SearchQuery{Text: text})
	if !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("boundary-length query: err = %v, want ErrSearchUnavailable (guard passed, nil db reached)", err)
	}
}

// TestNewPostgresSearcherRequiresDB — the constructor refuses a nil handle so
// wiring mistakes surface at startup.
func TestNewPostgresSearcherRequiresDB(t *testing.T) {
	if s, err := NewPostgresSearcher(nil); err == nil || s != nil {
		t.Fatalf("NewPostgresSearcher(nil) = %v, %v; want nil, error", s, err)
	}
}
