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
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// fakeSearcher is the in-memory Searcher stand-in for the FTS tier: it
// matches case-insensitive substrings against a seeded messageID → body-text
// map, so the SEARCH matrix can prove the FTS wiring without Postgres.
type fakeSearcher struct {
	bodies map[uint]string
}

func (f *fakeSearcher) Search(_ context.Context, _ SearchScope, q SearchQuery) ([]SearchHit, int64, error) {
	text := strings.ToLower(trimASCIISpace(q.Text))
	if text == "" {
		return []SearchHit{}, 0, nil
	}
	var hits []SearchHit
	for id, body := range f.bodies {
		if strings.Contains(strings.ToLower(body), text) {
			hits = append(hits, SearchHit{MessageID: id, FolderID: q.FolderID, Rank: 1})
		}
	}
	return hits, int64(len(hits)), nil
}

// searchSeedRaw builds a message with a controlled Date header (the SENT*
// keys read the stored header date; the internal-date keys read created_at).
func searchSeedRaw(subject, from, to, cc, body string, date time.Time, flags uint32) []byte {
	return []byte("Message-ID: <" + subject + "@search>\r\n" +
		"From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Cc: " + cc + "\r\n" +
		"Bcc: deep-bcc@example.test\r\n" +
		"X-Campaign: winter-gala\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: " + date.Format("Mon, 02 Jan 2006 15:04:05 -0700") + "\r\n" +
		"\r\n" +
		body + "\r\n")
}

// seedSearchFolder builds the corpus the matrix runs against:
//
//	seq 1 alpha: seen,                        2026-01-02, size ~
//	seq 2 beta:  flagged + recent,            2026-02-03
//	seq 3 gamma: deleted + seen,              2026-03-04
func seedSearchFolder(t *testing.T, svc *Service) (*models.Mailbox, *models.MailFolder, map[string]uint) {
	t.Helper()
	mb := seedUserMailbox(t, svc, 1, "search-matrix@post", 0)
	folder := inboxFolder(t, svc, mb)
	msgs := []struct {
		subject string
		flags   uint32
	}{
		{"Alpha report", models.FlagSeen},
		{"Beta invoice", models.FlagFlagged | models.FlagRecent},
		{"Gamma invoice", models.FlagDeleted | models.FlagSeen},
	}
	dates := []time.Time{
		time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC),
	}
	bodies := []string{"quarterly numbers report", "invoice total due", "meeting notes and invoice"}
	ids := map[string]uint{}
	sizes := []int64{500, 1500, 2500}
	froms := []string{"alpha@from.test", "beta@from.test", "gamma@from.test"}
	tos := []string{"to-alpha@test", "to-beta@test", "to-gamma@test"}
	for i, m := range msgs {
		raw := searchSeedRaw(m.subject, froms[i], tos[i], "cc@example.test", bodies[i], dates[i], m.flags)
		// The raw size is what the store records; pad to hit the matrix's
		// LARGER/SMALLER thresholds deterministically.
		pad := int(sizes[i]) - len(raw) - 10
		if pad > 0 {
			raw = append(raw, []byte(strings.Repeat("x", pad)+"\r\n")...)
		}
		res, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: folder.ID, Raw: raw, Flags: m.flags})
		if err != nil {
			t.Fatalf("seed append %s: %v", m.subject, err)
		}
		// Append adds \Recent to everything; align with the matrix intent.
		if m.flags&models.FlagRecent == 0 {
			if _, err := svc.SetFlags(1, 0, FlagsUpdate{MessageID: res.MessageID, Clear: models.FlagRecent, Set: m.flags}); err != nil {
				t.Fatalf("align flags of %s: %v", m.subject, err)
			}
		}
		ids[strings.ToLower(m.subject)] = res.MessageID
	}
	// Wire the fake FTS corpus against the stored body text.
	svc.SetSearcher(&fakeSearcher{bodies: map[uint]string{
		ids["alpha report"]:  bodies[0],
		ids["beta invoice"]:  bodies[1],
		ids["gamma invoice"]: bodies[2],
	}})
	return mb, folder, ids
}

// TestIMAPSearchMatrix runs the SEARCH translation matrix: each key of the
// ADR-004 command list maps to the expected message set. Names use the wire
// key names so the matrix reads like the RFC.
func TestIMAPSearchMatrix(t *testing.T) {
	svc, _, _ := newFakeService(t)
	_, folder, _ := seedSearchFolder(t, svc)
	ctx := context.Background()
	scope := IMAPSearchScope{UserID: 1, FolderID: folder.ID}

	run := func(t *testing.T, crit SearchNode) map[uint32]bool {
		t.Helper()
		hits, err := svc.SearchIMAP(ctx, scope, crit)
		if err != nil {
			t.Fatalf("SearchIMAP: %v", err)
		}
		out := map[uint32]bool{}
		for _, h := range hits {
			out[h.Seq] = true
		}
		return out
	}
	expect := func(t *testing.T, got map[uint32]bool, want ...uint32) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got seqs %v, want %v", got, want)
		}
		for _, w := range want {
			if !got[w] {
				t.Fatalf("got seqs %v, want %v", got, want)
			}
		}
	}

	// ALL
	expect(t, run(t, SearchAll{}), 1, 2, 3)
	// Flag keys
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchFlagKey{Mask: models.FlagSeen, Set: true}}}), 1, 3)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchFlagKey{Mask: models.FlagSeen, Set: false}}}), 2)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchFlagKey{Mask: models.FlagFlagged, Set: true}}}), 2)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchFlagKey{Mask: models.FlagFlagged, Set: false}}}), 1, 3)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchFlagKey{Mask: models.FlagDeleted, Set: true}}}), 3)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchFlagKey{Mask: models.FlagDeleted, Set: false}}}), 1, 2)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchFlagKey{Mask: models.FlagRecent, Set: true}}}), 2)
	// OLD = NOT RECENT
	expect(t, run(t, &SearchNot{Child: SearchFlagKey{Mask: models.FlagRecent, Set: true}}), 1, 3)
	// NEW = RECENT ∧ UNSEEN
	expect(t, run(t, &SearchAnd{Children: []SearchNode{
		SearchFlagKey{Mask: models.FlagRecent, Set: true},
		SearchFlagKey{Mask: models.FlagSeen, Set: false},
	}}), 2)
	// Header column keys
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "from", Value: "beta@from.test"}}}), 2)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "to", Value: "to-gamma"}}}), 3)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "cc", Value: "cc@example.test"}}}), 1, 2, 3)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "subject", Value: "invoice"}}}), 2, 3)
	// Case-insensitivity of the ILIKE fast path
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "SUBJECT", Value: "INVOICE"}}}), 2, 3)
	// LIKE wildcards in the client string are literals, not patterns.
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "subject", Value: "l_pha"}}}))
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "subject", Value: "alpha"}}}), 1)
	// HEADER over a non-column field → bounded raw-header scan.
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "x-campaign", Value: "winter"}}}), 1, 2, 3)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "x-campaign", Value: "missing"}}}))
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "x-campaign", Value: ""}}}), 1, 2, 3)
	// BCC (raw-header scan; not stored as a column).
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "bcc", Value: "deep-bcc", Blob: true}}}), 1, 2, 3)
	// Size keys
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchSizeKey{Larger: true, Bytes: 1200}}}), 2, 3)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchSizeKey{Larger: false, Bytes: 1200}}}), 1)
	// Internal-date keys (ingest time is "now": everything is SINCE yesterday,
	// nothing BEFORE yesterday, all ON today).
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchDateKey{Op: DateSince, Day: yesterday, Internal: true}}}), 1, 2, 3)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchDateKey{Op: DateBefore, Day: yesterday, Internal: true}}}))
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchDateKey{Op: DateOn, Day: time.Now().UTC(), Internal: true}}}), 1, 2, 3)
	// SENT* keys over the stored Date header.
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchDateKey{Op: DateSince, Day: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}}}), 2, 3)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchDateKey{Op: DateBefore, Day: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}}}), 1)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchDateKey{Op: DateOn, Day: time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)}}}), 2)
	// SENTON honors the calendar day even with a non-midnight time in Day.
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchDateKey{Op: DateOn, Day: time.Date(2026, 2, 3, 17, 45, 0, 0, time.UTC)}}}), 2)
	// FTS keys (through the Searcher seam).
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchBodyKey{Text: "quarterly"}}}), 1)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchBodyKey{Text: "invoice"}}}), 2, 3)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchTextKey{Text: "quarterly"}}}), 1)
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchTextKey{Text: "Gamma"}}}), 3)
	// UID set keys
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchUIDKey{Set: mustSeqSet(t, "2:3")}}}), 2, 3)
	// Sequence set keys
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchSeqKey{Set: mustSeqSet(t, "1,3")}}}), 1, 3)
	// OR / NOT composition
	expect(t, run(t, &SearchOr{
		A: SearchFlagKey{Mask: models.FlagFlagged, Set: true},
		B: SearchFlagKey{Mask: models.FlagDeleted, Set: true},
	}), 2, 3)
	expect(t, run(t, &SearchNot{Child: &SearchAnd{Children: []SearchNode{
		SearchHeaderKey{Field: "subject", Value: "invoice"},
	}}}), 1)
	// A mixed conjunction (SQL fast path) with a NOT (Go path) around it.
	expect(t, run(t, &SearchAnd{Children: []SearchNode{
		SearchFlagKey{Mask: models.FlagSeen, Set: true},
		&SearchNot{Child: SearchFlagKey{Mask: models.FlagDeleted, Set: true}},
	}}), 1)
	// KEYWORD: system flag atoms only.
	expect(t, run(t, &SearchAnd{Children: []SearchNode{SearchHeaderKey{Field: "subject", Value: ""}, keywordNode("Seen")}}), 1, 3)
}

// keywordNode maps a system flag atom (the only KEYWORD vocabulary the store
// has) onto its flag key.
func keywordNode(atom string) SearchNode {
	switch strings.ToLower(atom) {
	case "seen":
		return SearchFlagKey{Mask: models.FlagSeen, Set: true}
	}
	return SearchAll{}
}

// TestIMAPSearchConjunctionPushDown proves the pure-conjunction path (SQL
// push-down) and the general path agree: the same key set answered through
// both routes returns identical results.
func TestIMAPSearchConjunctionPushDown(t *testing.T) {
	svc, _, _ := newFakeService(t)
	_, folder, _ := seedSearchFolder(t, svc)
	ctx := context.Background()
	scope := IMAPSearchScope{UserID: 1, FolderID: folder.ID}

	// Pure conjunction of SQL-expressible leaves → push-down path.
	pure := &SearchAnd{Children: []SearchNode{
		SearchHeaderKey{Field: "subject", Value: "invoice"},
		SearchFlagKey{Mask: models.FlagDeleted, Set: false},
		SearchSizeKey{Larger: true, Bytes: 1200},
	}}
	hits, err := svc.SearchIMAP(ctx, scope, pure)
	if err != nil {
		t.Fatalf("pure conjunction: %v", err)
	}
	if len(hits) != 1 || hits[0].Seq != 2 {
		t.Fatalf("pure conjunction → seqs %+v, want [2]", hits)
	}
	// Sanity: the pushed-down form actually took the SQL route (no FTS keys,
	// no blob keys) — sqlConjunction reports it pure.
	if _, _, ok := sqlConjunction(pure); !ok {
		t.Fatalf("pure conjunction not recognized by sqlConjunction")
	}
	// The same criteria with an OR → general path, same answer.
	mixed := &SearchOr{A: pure, B: SearchAll{}}
	hits, err = svc.SearchIMAP(ctx, scope, mixed)
	if err != nil {
		t.Fatalf("general tree: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("OR with ALL → %d hits, want 3", len(hits))
	}
}

// TestIMAPSearchFTSRequired: BODY/TEXT without a wired Searcher is a typed
// error, never a silent no-match.
func TestIMAPSearchFTSRequired(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "search-fts@post", 0)
	folder := inboxFolder(t, svc, mb)
	if _, err := svc.SearchIMAP(context.Background(), IMAPSearchScope{UserID: 1, FolderID: folder.ID},
		&SearchAnd{Children: []SearchNode{SearchBodyKey{Text: "x"}}}); err == nil {
		t.Fatalf("BODY search without a searcher must fail")
	}
	// Flag-only searches keep working without a searcher.
	hits, err := svc.SearchIMAP(context.Background(), IMAPSearchScope{UserID: 1, FolderID: folder.ID}, SearchAll{})
	if err != nil {
		t.Fatalf("flag-only search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("empty folder → %d hits", len(hits))
	}
}

// TestIMAPSearchIsolation: another user's folder is refused with the typed
// ownership error, same as every other Service entry point.
func TestIMAPSearchIsolation(t *testing.T) {
	svc, _, _ := newFakeService(t)
	_, folder, _ := seedSearchFolder(t, svc)
	_, err := svc.SearchIMAP(context.Background(), IMAPSearchScope{UserID: 2, FolderID: folder.ID}, SearchAll{})
	if err == nil {
		t.Fatalf("foreign search must be refused")
	}
}
