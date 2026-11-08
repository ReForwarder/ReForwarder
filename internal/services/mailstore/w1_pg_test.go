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
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// Real-Postgres siblings of the W1 fake-driver tests (OB-4 wave W1). They
// prove what the fake cannot: the FILTER (WHERE …) aggregate syntax, real FTS
// integration for the BODY/TEXT SEARCH keys, and the append+expunge race
// invariants under genuine row-lock serialization. Skipped when
// TEST_DATABASE_DSN is unset (pgTestDB), exactly like the other PG suites.
// itoa() is shared with mailstore_pg_test.go.

// TestPGStatusCounts: the single-round-trip FILTER aggregate against real
// Postgres, tracking appends → flags → expunge.
func TestPGStatusCounts(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "status-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := svc.Append(context.Background(), 1, 0, AppendIn{
			FolderID: inbox.ID,
			Raw:      simpleRaw("pg-status-"+itoa(i)+"-"+pgRunSuffix, "s@pg.test", "t@pg.test", "body"),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	st, err := svc.StatusCounts(inbox.ID, 1, 0)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Messages != 3 || st.Unseen != 3 || st.Recent != 3 {
		t.Fatalf("status = %+v, want 3/3/3", st)
	}
	page, err := svc.ListMessages(inbox.ID, 1, 0, ListFilter{}, 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := svc.SetFlags(1, 0, FlagsUpdate{MessageID: page.Messages[0].ID, Set: models.FlagSeen, Clear: models.FlagRecent}); err != nil {
		t.Fatalf("flags: %v", err)
	}
	st, err = svc.StatusCounts(inbox.ID, 1, 0)
	if err != nil {
		t.Fatalf("status after flags: %v", err)
	}
	if st.Unseen != 2 || st.Recent != 2 {
		t.Fatalf("status after flags = %+v, want unseen/recent 2/2", st)
	}
}

// TestPGFetchRangeSections: BODY[] byte-exactness and a nested part fetch
// against the real blob store.
func TestPGFetchRangeSections(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "fetch-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	raw := []byte(multipartRaw)
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); err != nil {
		t.Fatalf("append: %v", err)
	}
	res, err := svc.FetchRange(context.Background(), FetchRangeSpec{
		FolderID: inbox.ID, UserID: 1, Set: mustSeqSet(t, "1:*"),
		Items: FetchItems{Body: &BodySection{Spec: "2.1"}},
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("fetch → %d results", len(res))
	}
	if !res[0].SectionFound || !strings.Contains(string(res[0].Section), "alternative plain") {
		t.Fatalf("BODY[2.1] = %q", res[0].Section)
	}
	// \Seen side effect persisted.
	m, err := svc.GetMessage(res[0].MessageID, 1, 0)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if m.Flags&models.FlagSeen == 0 {
		t.Fatalf("\\Seen not persisted by the non-peek body fetch")
	}
}

// TestPGIMAPSearchFTS: flag keys through SQL and BODY/TEXT keys through the
// real PostgresSearcher (websearch_to_tsquery over search_tsv) — the
// integration the fake-driven matrix cannot reach.
func TestPGIMAPSearchFTS(t *testing.T) {
	svc, db, _ := newPGServiceFull(t)
	sr, err := NewPostgresSearcher(db)
	if err != nil {
		t.Fatalf("searcher: %v", err)
	}
	svc.SetSearcher(sr)
	mb, err := svc.CreateMailbox(1, nil, "search-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	raws := []string{"quarterly numbers report", "invoice total due"}
	var ids []uint
	for i, b := range raws {
		res, err := svc.Append(context.Background(), 1, 0, AppendIn{
			FolderID: inbox.ID,
			Raw:      simpleRaw("pg-search-"+itoa(i)+"-"+pgRunSuffix, "s@pg.test", "t@pg.test", b),
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		ids = append(ids, res.MessageID)
	}
	ctx := context.Background()
	scope := IMAPSearchScope{UserID: 1, FolderID: inbox.ID}

	// Flag key (SQL push-down).
	hits, err := svc.SearchIMAP(ctx, scope, &SearchAnd{Children: []SearchNode{
		SearchFlagKey{Mask: models.FlagSeen, Set: false},
	}})
	if err != nil {
		t.Fatalf("flag search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("flag search → %d hits, want 2", len(hits))
	}
	// FTS BODY key through the real searcher.
	hits, err = svc.SearchIMAP(ctx, scope, &SearchAnd{Children: []SearchNode{SearchBodyKey{Text: "quarterly"}}})
	if err != nil {
		t.Fatalf("fts search: %v", err)
	}
	if len(hits) != 1 || hits[0].MessageID != ids[0] {
		t.Fatalf("fts search → %+v, want message %d", hits, ids[0])
	}
	// Combined: SQL flag predicate AND FTS text key.
	hits, err = svc.SearchIMAP(ctx, scope, &SearchAnd{Children: []SearchNode{
		SearchFlagKey{Mask: models.FlagSeen, Set: false},
		SearchBodyKey{Text: "invoice"},
	}})
	if err != nil {
		t.Fatalf("combined search: %v", err)
	}
	if len(hits) != 1 || hits[0].MessageID != ids[1] {
		t.Fatalf("combined search → %+v, want message %d", hits, ids[1])
	}
}

// TestPGConcurrentAppendAndExpungeRace — the real-Postgres sibling of
// TestConcurrentAppendAndExpungeRace: concurrent appends, flag stores and
// guard-serialized expunges through genuine transactions.
func TestPGConcurrentAppendAndExpungeRacePG(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "racepg-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}

	const (
		appenders   = 8
		perAppender = 8
		expungers   = 2
	)
	seenUIDs := map[uint32]bool{}
	var mu sync.Mutex

	var wg sync.WaitGroup
	errCh := make(chan error, appenders+expungers)
	for a := 0; a < appenders; a++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perAppender; i++ {
				raw := simpleRaw("pg-race-"+itoa(id)+"-"+itoa(i)+"-"+pgRunSuffix, "s@pg.test", "race@pg.test", "body")
				res, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
				if err != nil {
					errCh <- fmt.Errorf("append: %w", err)
					return
				}
				mu.Lock()
				if seenUIDs[res.UID] {
					mu.Unlock()
					errCh <- fmt.Errorf("duplicate UID %d", res.UID)
					return
				}
				seenUIDs[res.UID] = true
				mu.Unlock()
			}
		}(a)
	}
	for e := 0; e < expungers; e++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 4; i++ {
				page, err := svc.ListMessages(inbox.ID, 1, 0, ListFilter{}, 1, 10)
				if err != nil {
					errCh <- fmt.Errorf("list: %w", err)
					return
				}
				for _, m := range page.Messages {
					if m.UID%3 == uint32(id) {
						_ = svc.MarkDeleted(1, 0, m.ID)
					}
				}
				if _, err := svc.Expunge(inbox.ID, 1, 0); err != nil {
					errCh <- fmt.Errorf("expunge: %w", err)
					return
				}
			}
		}(e)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	// Survivors' UIDs were all genuinely assigned, counts agree.
	st, err := svc.StatusCounts(inbox.ID, 1, 0)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	page, err := svc.ListMessages(inbox.ID, 1, 0, ListFilter{}, 1, 200)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if st.Messages != int64(len(page.Messages)) {
		t.Fatalf("StatusCounts(%d) disagrees with rows (%d)", st.Messages, len(page.Messages))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, m := range page.Messages {
		if !seenUIDs[m.UID] {
			t.Fatalf("row carries UID %d never assigned", m.UID)
		}
	}
}

// TestPGSessionWildcardOnEmptyFolder: the UIDNEXT-1 wildcard fallback on a
// real folder row (the RFC 9051 §2.3.1.2 empty-mailbox shape).
func TestPGSessionWildcardOnEmptyFolder(t *testing.T) {
	svc, _ := newPGService(t)
	mb, err := svc.CreateMailbox(1, nil, "wild-"+pgRunSuffix+"@pg.test", 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	inbox, err := svc.GetFolderByPath(mb.ID, 1, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	// Empty: UID "*" resolves to UIDNEXT-1 = 1 (no match, no error).
	hits, err := svc.SearchIMAP(context.Background(),
		IMAPSearchScope{UserID: 1, FolderID: inbox.ID},
		&SearchAnd{Children: []SearchNode{SearchUIDKey{Set: mustSeqSet(t, "*")}}})
	if err != nil {
		t.Fatalf("wildcard search on empty folder: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("empty folder wildcard → %d hits", len(hits))
	}
	// One append: the wildcard now matches it.
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{
		FolderID: inbox.ID,
		Raw:      simpleRaw("pg-wild-"+pgRunSuffix, "s@pg.test", "t@pg.test", "body"),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	hits, err = svc.SearchIMAP(context.Background(),
		IMAPSearchScope{UserID: 1, FolderID: inbox.ID},
		&SearchAnd{Children: []SearchNode{SearchUIDKey{Set: mustSeqSet(t, "*")}}})
	if err != nil || len(hits) != 1 {
		t.Fatalf("wildcard after append → %d hits (err %v)", len(hits), err)
	}
}
