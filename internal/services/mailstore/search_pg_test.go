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
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/migration"
)

// Real-Postgres FTS suite (OB-3 wave W3). tsvector ranking, websearch
// parsing, COUNT(*) OVER () and the generated-column rebuild cannot be
// expressed by the fake driver, so every behavior here is proven against a
// real Postgres via the TEST_DATABASE_DSN gate — skipped cleanly (never
// faked) when no database is configured, exactly like mailstore_pg_test.go,
// which also supplies newPGService and pgRunSuffix.

// seedSearchMailbox creates a mailbox with an INBOX for userID and appends
// the given raw messages, returning the folder.
func seedSearchMailbox(t *testing.T, svc *Service, userID uint, name string, raws ...[]byte) (*models.Mailbox, *models.MailFolder) {
	t.Helper()
	mb, err := svc.CreateMailbox(userID, nil, name, 0)
	if err != nil {
		t.Fatalf("create mailbox %s: %v", name, err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, userID, 0, "INBOX")
	if err != nil {
		t.Fatalf("get INBOX: %v", err)
	}
	for i, raw := range raws {
		if _, err := svc.Append(context.Background(), userID, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}
	return mb, inbox
}

// searchIDs runs one search and returns the matched message ids.
func searchIDs(t *testing.T, s *PostgresSearcher, scope SearchScope, q SearchQuery) ([]uint, int64) {
	t.Helper()
	hits, total, err := s.Search(context.Background(), scope, q)
	if err != nil {
		t.Fatalf("search %q: %v", q.Text, err)
	}
	ids := make([]uint, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.MessageID)
	}
	return ids, total
}

// TestPGSearchHitsSubjectFromAndBody — one token per message, placed in the
// subject, the sender address and the body respectively; each query matches
// exactly its own message through the weighted tsvector. The from query uses
// the full address because Postgres's default parser keeps an email address
// as one token.
func TestPGSearchHitsSubjectFromAndBody(t *testing.T) {
	svc, db := newPGService(t)
	s := &PostgresSearcher{db: db}
	subjectToken := "zephyrine" + pgRunSuffix
	fromToken := "postmaster" + pgRunSuffix
	bodyToken := "arctostaphylos" + pgRunSuffix

	mb, _ := seedSearchMailbox(t, svc, 1, "fts-fields-"+pgRunSuffix+"@pg.test",
		simpleRaw(subjectToken+" launch plan", "s@pg.test", "fields@pg.test", "nothing special here"),
		simpleRaw("quarterly notes", fromToken+"@pg.test", "fields@pg.test", "nothing special here"),
		simpleRaw("quarterly notes", "s@pg.test", "fields@pg.test", "the "+bodyToken+" shrub likes acid soil"),
	)
	scope := SearchScope{MailboxID: mb.ID, UserID: 1}

	t.Run("subject", func(t *testing.T) {
		ids, total := searchIDs(t, s, scope, SearchQuery{Text: subjectToken})
		if len(ids) != 1 || total != 1 {
			t.Fatalf("subject token: ids=%v total=%d, want exactly 1", ids, total)
		}
	})
	t.Run("from", func(t *testing.T) {
		ids, total := searchIDs(t, s, scope, SearchQuery{Text: fromToken + "@pg.test"})
		if len(ids) != 1 || total != 1 {
			t.Fatalf("from token: ids=%v total=%d, want exactly 1", ids, total)
		}
	})
	t.Run("body", func(t *testing.T) {
		ids, total := searchIDs(t, s, scope, SearchQuery{Text: bodyToken})
		if len(ids) != 1 || total != 1 {
			t.Fatalf("body token: ids=%v total=%d, want exactly 1", ids, total)
		}
	})
	t.Run("no_match_is_empty", func(t *testing.T) {
		ids, total := searchIDs(t, s, scope, SearchQuery{Text: "xylophone" + pgRunSuffix})
		if len(ids) != 0 || total != 0 {
			t.Fatalf("absent token: ids=%v total=%d, want empty", ids, total)
		}
	})
}

// TestPGSearchIsolationBetweenUsers — alice and bob each hold a message with
// the same token; each search sees only its own user's row, and a search
// against someone else's mailbox id returns an empty page (never an error,
// never a leak of the other user's data).
func TestPGSearchIsolationBetweenUsers(t *testing.T) {
	svc, db := newPGService(t)
	s := &PostgresSearcher{db: db}
	shared := "mutual" + pgRunSuffix

	aliceMB, _ := seedSearchMailbox(t, svc, 101, "alice-iso-"+pgRunSuffix+"@pg.test",
		simpleRaw("shared "+shared, "s@pg.test", "alice@pg.test", shared+" for alice"))
	bobMB, _ := seedSearchMailbox(t, svc, 202, "bob-iso-"+pgRunSuffix+"@pg.test",
		simpleRaw("shared "+shared, "s@pg.test", "bob@pg.test", shared+" for bob"))

	aliceScope := SearchScope{MailboxID: aliceMB.ID, UserID: 101}
	bobScope := SearchScope{MailboxID: bobMB.ID, UserID: 202}

	aliceIDs, aliceTotal := searchIDs(t, s, aliceScope, SearchQuery{Text: shared})
	if len(aliceIDs) != 1 || aliceTotal != 1 {
		t.Fatalf("alice search: ids=%v total=%d, want exactly her one message", aliceIDs, aliceTotal)
	}
	bobIDs, bobTotal := searchIDs(t, s, bobScope, SearchQuery{Text: shared})
	if len(bobIDs) != 1 || bobTotal != 1 {
		t.Fatalf("bob search: ids=%v total=%d, want exactly his one message", bobIDs, bobTotal)
	}
	if aliceIDs[0] == bobIDs[0] {
		t.Fatal("isolation broken: both users got the same message id")
	}

	crossScope := SearchScope{MailboxID: aliceMB.ID, UserID: 202} // bob asks for alice's mailbox
	ids, total := searchIDs(t, s, crossScope, SearchQuery{Text: shared})
	if len(ids) != 0 || total != 0 {
		t.Fatalf("cross-user search: ids=%v total=%d, want empty (no leak)", ids, total)
	}
}

// TestPGSearchPaginationAndTotals — 7 matches paged 3/3/1 with total 7 on
// every page, no duplicates or holes, and an offset past the end still
// reports the true total (the window-count fallback path).
func TestPGSearchPaginationAndTotals(t *testing.T) {
	svc, db := newPGService(t)
	s := &PostgresSearcher{db: db}
	token := "pagination" + pgRunSuffix

	raws := make([][]byte, 7)
	for i := range raws {
		raws[i] = simpleRaw("page msg "+itoa(i), "s@pg.test", "paging@pg.test", "body with "+token+" number "+itoa(i))
	}
	mb, _ := seedSearchMailbox(t, svc, 1, "fts-paging-"+pgRunSuffix+"@pg.test", raws...)
	scope := SearchScope{MailboxID: mb.ID, UserID: 1}

	seen := make(map[uint]bool)
	wantCounts := []int{3, 3, 1, 0}
	offset := 0
	for page, want := range wantCounts {
		ids, total := searchIDs(t, s, scope, SearchQuery{Text: token, Limit: 3, Offset: offset})
		if total != 7 {
			t.Fatalf("page %d: total = %d, want 7", page, total)
		}
		if len(ids) != want {
			t.Fatalf("page %d: hits = %d, want %d", page, len(ids), want)
		}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("page %d: duplicate message id %d across pages", page, id)
			}
			seen[id] = true
		}
		offset += 3
	}
	if len(seen) != 7 {
		t.Fatalf("paged through %d distinct ids, want 7", len(seen))
	}

	// Default paging: no Limit set means DefaultSearchPageSize, so all 7 fit
	// on one page.
	ids, total := searchIDs(t, s, scope, SearchQuery{Text: token})
	if total != 7 || len(ids) != 7 {
		t.Fatalf("default page: hits=%d total=%d, want 7/7", len(ids), total)
	}
}

// TestPGSearchRankingPrefersSubjectOverBody — the same single token in one
// message's subject and another's body must rank the subject hit first
// (setweight A = 1.0 against C = 0.2 under ts_rank).
func TestPGSearchRankingPrefersSubjectOverBody(t *testing.T) {
	svc, db := newPGService(t)
	s := &PostgresSearcher{db: db}
	token := "stellar" + pgRunSuffix

	mb, _ := seedSearchMailbox(t, svc, 1, "fts-rank-"+pgRunSuffix+"@pg.test",
		simpleRaw(token+" observatory report", "s@pg.test", "rank@pg.test", "nothing to see here"),
		simpleRaw("routine circular", "s@pg.test", "rank@pg.test", "the "+token+" was logged incidentally"),
	)
	scope := SearchScope{MailboxID: mb.ID, UserID: 1}

	hits, total, err := s.Search(context.Background(), scope, SearchQuery{Text: token})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 2 || len(hits) != 2 {
		t.Fatalf("hits=%d total=%d, want both messages", len(hits), total)
	}
	if hits[0].Rank <= hits[1].Rank {
		t.Fatalf("ranks not descending: %.4f then %.4f", hits[0].Rank, hits[1].Rank)
	}
	// The subject-bearing message is whichever id Append assigned first;
	// recover it through a direct metadata check instead of guessing.
	var subjectMsgID uint
	if err := db.Raw(`SELECT id FROM mail_messages WHERE subject LIKE ? ORDER BY id LIMIT 1`, token+"%").Scan(&subjectMsgID).Error; err != nil {
		t.Fatalf("find subject message: %v", err)
	}
	if hits[0].MessageID != subjectMsgID {
		t.Fatalf("top hit = message %d, want the subject hit %d", hits[0].MessageID, subjectMsgID)
	}
}

// TestPGSearchFolderScoping — the same token in two folders of one mailbox;
// FolderID narrows the result to that folder, 0 searches the whole mailbox.
func TestPGSearchFolderScoping(t *testing.T) {
	svc, db := newPGService(t)
	s := &PostgresSearcher{db: db}
	token := "waypoint" + pgRunSuffix

	mb, inbox := seedSearchMailbox(t, svc, 1, "fts-scope-"+pgRunSuffix+"@pg.test",
		simpleRaw("inbox copy "+token, "s@pg.test", "scope@pg.test", "x"))
	archive, err := svc.CreateFolder(mb.ID, 1, 0, "", "Archive")
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: archive.ID, Raw: simpleRaw("archive copy "+token, "s@pg.test", "scope@pg.test", "x")}); err != nil {
		t.Fatalf("append to archive: %v", err)
	}
	scope := SearchScope{MailboxID: mb.ID, UserID: 1}

	both, total := searchIDs(t, s, scope, SearchQuery{Text: token})
	if len(both) != 2 || total != 2 {
		t.Fatalf("whole-mailbox search: ids=%v total=%d, want both folders", both, total)
	}
	onlyInbox, total := searchIDs(t, s, scope, SearchQuery{Text: token, FolderID: inbox.ID})
	if len(onlyInbox) != 1 || total != 1 {
		t.Fatalf("inbox-scoped search: ids=%v total=%d, want 1", onlyInbox, total)
	}
	onlyArchive, total := searchIDs(t, s, scope, SearchQuery{Text: token, FolderID: archive.ID})
	if len(onlyArchive) != 1 || total != 1 {
		t.Fatalf("archive-scoped search: ids=%v total=%d, want 1", onlyArchive, total)
	}
	if onlyInbox[0] == onlyArchive[0] {
		t.Fatal("folder scoping broken: both folders returned the same message id")
	}
}

// TestPGRebuildSearchIndexIdempotent — the raw-SQL rebuild converges any
// state to the canonical generated column + GIN index, twice in a row, with
// search results identical across the rebuilds, and the auto-update path
// alive afterwards (a message appended post-rebuild is immediately
// searchable).
func TestPGRebuildSearchIndexIdempotent(t *testing.T) {
	svc, db := newPGService(t)
	s := &PostgresSearcher{db: db}
	tok1 := "rebuilt" + pgRunSuffix
	tok2 := "drifted" + pgRunSuffix
	tok3 := "postrebuild" + pgRunSuffix

	mb, inbox := seedSearchMailbox(t, svc, 1, "fts-rebuild-"+pgRunSuffix+"@pg.test",
		simpleRaw("first "+tok1, "s@pg.test", "rebuild@pg.test", "x"),
		simpleRaw("second "+tok2, "s@pg.test", "rebuild@pg.test", "x"),
	)
	scope := SearchScope{MailboxID: mb.ID, UserID: 1}

	before1, total1 := searchIDs(t, s, scope, SearchQuery{Text: tok1})
	before2, total2 := searchIDs(t, s, scope, SearchQuery{Text: tok2})
	if len(before1) != 1 || total1 != 1 || len(before2) != 1 || total2 != 1 {
		t.Fatalf("pre-rebuild searches wrong: %v/%d, %v/%d", before1, total1, before2, total2)
	}

	for round := 1; round <= 2; round++ {
		if err := migration.RebuildMailstoreSearch(db); err != nil {
			t.Fatalf("rebuild round %d: %v", round, err)
		}
		after1, got1 := searchIDs(t, s, scope, SearchQuery{Text: tok1})
		after2, got2 := searchIDs(t, s, scope, SearchQuery{Text: tok2})
		if len(after1) != 1 || got1 != 1 || len(after2) != 1 || got2 != 1 {
			t.Fatalf("post-rebuild round %d searches wrong: %v/%d, %v/%d", round, after1, got1, after2, got2)
		}
		if after1[0] != before1[0] || after2[0] != before2[0] {
			t.Fatalf("rebuild round %d changed results: %v→%v, %v→%v", round, before1, after1, before2, after2)
		}

		// The column is still genuinely GENERATED STORED and the GIN index
		// still backs it after each rebuild.
		var generated string
		if err := db.Raw(`SELECT attgenerated FROM pg_attribute WHERE attrelid = 'mail_messages'::regclass AND attname = 'search_tsv' AND NOT attisdropped`).Scan(&generated).Error; err != nil {
			t.Fatalf("round %d: read attgenerated: %v", round, err)
		}
		if generated != "s" {
			t.Fatalf("round %d: search_tsv attgenerated = %q, want \"s\"", round, generated)
		}
		var n int64
		if err := db.Raw(`SELECT count(*) FROM pg_indexes WHERE tablename = 'mail_messages' AND indexname = 'idx_mail_messages_search_tsv'`).Scan(&n).Error; err != nil {
			t.Fatalf("round %d: read pg_indexes: %v", round, err)
		}
		if n != 1 {
			t.Fatalf("round %d: GIN index present = %d, want 1", round, n)
		}
	}

	// The regenerated column keeps auto-updating: a message appended after the
	// rebuilds is immediately searchable through it.
	res, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("third "+tok3, "s@pg.test", "rebuild@pg.test", "x")})
	if err != nil {
		t.Fatalf("append after rebuilds: %v", err)
	}
	ids, total := searchIDs(t, s, scope, SearchQuery{Text: tok3})
	if len(ids) != 1 || total != 1 || ids[0] != res.MessageID {
		t.Fatalf("post-rebuild append not searchable: ids=%v total=%d want [%d]/1", ids, total, res.MessageID)
	}
}

// TestPGRebuildSearchIndexFailsClosedWithoutDB — nil handle is a constructor-
// style refusal, not a panic.
func TestPGRebuildSearchIndexFailsClosedWithoutDB(t *testing.T) {
	if err := migration.RebuildMailstoreSearch(nil); err == nil {
		t.Fatal("RebuildMailstoreSearch(nil) = nil, want error")
	}
}

// TestPGSearchUnavailableWithoutDB documents the nil-db guard: a zero-value
// searcher reports ErrSearchUnavailable rather than panicking when it
// actually has to run SQL (blank/overlong inputs are handled before this).
func TestPGSearchUnavailableWithoutDB(t *testing.T) {
	s := &PostgresSearcher{}
	_, _, err := s.Search(context.Background(), SearchScope{MailboxID: 1, UserID: 1}, SearchQuery{Text: "nonblank"})
	if !errors.Is(err, ErrSearchUnavailable) {
		t.Fatalf("nil-db search: err = %v, want ErrSearchUnavailable", err)
	}
}
