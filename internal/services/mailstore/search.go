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
	"fmt"
	"unicode/utf8"

	"gorm.io/gorm"
)

// Postgres FTS search (OB-3 wave W3, ADR-003 decision 3). The searchable
// material is the mail_messages.search_tsv STORED generated column —
// weighted A = subject, B = from + to/cc, C = body text — kept in sync by
// Postgres itself on every insert/update (internal/storage/migration). This
// file implements the Searcher interface W1 defined (interfaces.go): it only
// reads that column, so it can never disagree with what is stored.
//
// Query semantics: Text is parsed by websearch_to_tsquery('english', ?) —
// quoted phrases, OR and -exclusion work exactly as Postgres defines them and
// that parser never raises syntax errors. The query text travels as a bound
// parameter; user input is never concatenated into SQL.
//
// Isolation: every statement joins mail_folders and mailboxes and requires
// mailboxes.id = scope.MailboxID AND mailboxes.user_id = scope.UserID in the
// same WHERE. A mailbox owned by someone else therefore yields an empty page
// rather than an error, so a wrong mailbox id is indistinguishable from an
// empty mailbox and existence never leaks across users (matches the
// service-level isolation contract in service.go).

const (
	// DefaultSearchPageSize is the page size used when SearchQuery.Limit is
	// not set (<= 0).
	DefaultSearchPageSize = 50
	// MaxSearchPageSize clamps SearchQuery.Limit to bound a single page's
	// work; callers page through with Offset.
	MaxSearchPageSize = 200
	// MaxSearchQueryRunes bounds the query text handed to
	// websearch_to_tsquery. It is a work guard, not a security one (the value
	// is always a bound parameter); real search boxes are far smaller.
	MaxSearchQueryRunes = 512
)

// searchFromWhere is the shared FROM/JOIN/WHERE core of the search
// statements. The mailbox + user predicates carry the isolation contract; the
// tsvector filter is the only match condition. Args (in order): mailbox id,
// user id, query text.
const searchFromWhere = `FROM mail_messages m
JOIN mail_folders f ON f.id = m.folder_id
JOIN mailboxes mb ON mb.id = f.mailbox_id
WHERE mb.id = ? AND mb.user_id = ?
	AND m.search_tsv @@ websearch_to_tsquery('english', ?)`

// searchFolderClause narrows the core to one folder; appended (with its bound
// argument) only when SearchQuery.FolderID is set.
const searchFolderClause = ` AND m.folder_id = ?`

// searchPageSQL selects one ranked page. The same tsquery expression drives
// both the @@ filter and ts_rank; websearch_to_tsquery is evaluated by the
// planner once per execution for the constant parameter. COUNT(*) OVER ()
// carries the total match count alongside the page so a full page costs one
// round-trip. Ordering: relevance first (ts_rank reads the setweight weights
// from search_tsv, so a subject hit outranks the same token in from/to and in
// body), then newest-first, then id for a stable tiebreak.
const searchPageSQL = `SELECT m.id AS message_id, m.folder_id AS folder_id,
	ts_rank(m.search_tsv, websearch_to_tsquery('english', ?)) AS rank_score,
	COUNT(*) OVER () AS total_count `

// searchPageOrder is the fixed page suffix; separate only so the folder
// clause can be spliced before it.
const searchPageOrder = ` ORDER BY rank_score DESC, m.date DESC, m.id DESC LIMIT ? OFFSET ?`

// searchCountSQL counts the whole match set — used when the page comes back
// empty (an offset past the end returns no rows and therefore no window
// count, but the caller still gets the true total).
const searchCountSQL = `SELECT count(*) AS total_count `

// PostgresSearcher is the real Searcher over mail_messages.search_tsv. It is
// safe for concurrent use (a read-only view over one *gorm.DB).
type PostgresSearcher struct {
	db *gorm.DB
}

// compile-time proof that the search wave implements W1's interface.
var _ Searcher = (*PostgresSearcher)(nil)

// NewPostgresSearcher wires the searcher onto the primary Postgres handle.
func NewPostgresSearcher(db *gorm.DB) (*PostgresSearcher, error) {
	if db == nil {
		return nil, errors.New("mailstore: nil db")
	}
	return &PostgresSearcher{db: db}, nil
}

// searchRow mirrors one result row of searchPageSQL.
type searchRow struct {
	MessageID uint    `gorm:"column:message_id"`
	FolderID  uint    `gorm:"column:folder_id"`
	RankScore float64 `gorm:"column:rank_score"`
	Total     int64   `gorm:"column:total_count"`
}

// normalizeSearchQuery applies the guards shared by every entry point: blank
// text means "no query" (empty page, not an error), an over-long query is
// refused with ErrSearchQueryTooLong, and paging is defaulted/clamped.
func normalizeSearchQuery(q SearchQuery) (string, int, int, error) {
	text := trimASCIISpace(q.Text)
	if text == "" {
		return "", 0, 0, nil
	}
	if n := utf8.RuneCountInString(text); n > MaxSearchQueryRunes {
		return "", 0, 0, fmt.Errorf("%w: %d runes > %d", ErrSearchQueryTooLong, n, MaxSearchQueryRunes)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultSearchPageSize
	}
	if limit > MaxSearchPageSize {
		limit = MaxSearchPageSize
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	return text, limit, offset, nil
}

// trimASCIISpace strips the ASCII whitespace Postgres's websearch parser
// ignores at the edges.
func trimASCIISpace(s string) string {
	start, end := 0, len(s)
	for start < end && isASCIISpaceByte(s[start]) {
		start++
	}
	for end > start && isASCIISpaceByte(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isASCIISpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

// Search implements Searcher. A blank query returns an empty page without
// touching the database; a mailbox owned by another user returns an empty
// page (isolation, see the package-level note above).
func (s *PostgresSearcher) Search(ctx context.Context, scope SearchScope, q SearchQuery) ([]SearchHit, int64, error) {
	text, limit, offset, err := normalizeSearchQuery(q)
	if err != nil {
		return nil, 0, err
	}
	if text == "" {
		return []SearchHit{}, 0, nil
	}
	if s == nil || s.db == nil {
		return nil, 0, ErrSearchUnavailable
	}

	// Build the two statements with bound parameters only — every ? below is
	// fed from this arg list, never from string interpolation of input.
	core := searchFromWhere
	coreArgs := []any{scope.MailboxID, scope.UserID, text}
	if q.FolderID != 0 {
		core += searchFolderClause
		coreArgs = append(coreArgs, q.FolderID)
	}

	pageArgs := append([]any{text}, coreArgs...)
	pageArgs = append(pageArgs, limit, offset)
	var rows []searchRow
	if err := s.db.WithContext(ctx).Raw(searchPageSQL+core+searchPageOrder, pageArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("mailstore search page: %w", err)
	}

	hits := make([]SearchHit, 0, len(rows))
	var total int64
	for _, r := range rows {
		total = r.Total
		hits = append(hits, SearchHit{MessageID: r.MessageID, FolderID: r.FolderID, Rank: r.RankScore})
	}
	if total == 0 {
		// Empty page: either genuinely no matches, or the offset lies past the
		// end of the match set. The window count cannot tell us — ask directly
		// so paging always reports the true total.
		if err := s.db.WithContext(ctx).Raw(searchCountSQL+core, coreArgs...).Scan(&total).Error; err != nil {
			return nil, 0, fmt.Errorf("mailstore search count: %w", err)
		}
	}
	return hits, total, nil
}
