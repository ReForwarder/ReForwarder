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
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// IMAP SEARCH execution (OB-4 wave W1, ADR-004 decision 1). The protocol
// layer parses the wire SEARCH command into the criteria tree below; this
// file translates it onto the mailstore's two predicates tiers:
//
//   - SQL: flag bitmasks, internal-date/sent-date windows, size bounds and
//     header-substring matches over the stored columns. When the whole tree is
//     a conjunction of such leaves — the shape real clients overwhelmingly
//     send — every predicate is pushed into one parameterized WHERE and the
//     scan is bounded before any row is decoded. All values travel as bind
//     parameters; input is never concatenated into SQL.
//   - FTS: BODY/TEXT keys run through the Searcher interface
//     (PostgresSearcher over mail_messages.search_tsv, wired via
//     SetSearcher).
//
// Keys that need material the rows do not carry — arbitrary HEADER fields and
// BCC — fall back to a bounded scan of the stored raw messages
// (MaxSearchBlobScanMessages blob reads per search; beyond the cap the key
// stops matching rather than degrading the session). OR/NOT trees evaluate in
// Go over the folder's rows; folders are per-user, so the scan is bounded by
// one mailbox's folder, not by the store.
//
// Documented limitations (ADR-004 record): FROM/TO/CC/SUBJECT match the
// address/subject columns the ingest parser stored — a sender's display name
// is only reachable through the HEADER key; \Recent is ingest-time only
// (session-selection semantics are the servers'); KEYWORD recognizes only the
// system flag atoms (there is no custom-keyword storage).

// IMAPSearchScope scopes one search: the folder must resolve for
// (userID, workspaceID) exactly like every other Service entry point.
type IMAPSearchScope struct {
	UserID      uint
	WorkspaceID uint
	FolderID    uint
}

// IMAPSearchHit is one matching message, in UID order.
type IMAPSearchHit struct {
	MessageID uint
	UID       uint32
	Seq       uint32
}

// SearchNode is one node of the IMAP SEARCH criteria tree.
type SearchNode interface {
	isSearchNode()
}

// SearchAll matches every message (the ALL key; also the empty criteria).
type SearchAll struct{}

// SearchAnd matches when every child matches (IMAP's default concatenation).
type SearchAnd struct{ Children []SearchNode }

// SearchOr matches when either child matches (the OR key takes exactly two).
type SearchOr struct{ A, B SearchNode }

// SearchNot inverts its child.
type SearchNot struct{ Child SearchNode }

// SearchFlagKey is one system-flag presence/absence key: SEEN, UNSEEN,
// FLAGGED, UNFLAGGED, ANSWERED, UNANSWERED, DELETED, UNDELETED, DRAFT,
// UNDRAFT, RECENT, OLD, NEW (= RECENT ∧ UNSEEN), KEYWORD/UNKEYWORD (system
// flag atoms only — no custom-keyword storage).
type SearchFlagKey struct {
	Mask uint32
	Set  bool
}

// SearchDateOp selects BEFORE / ON / SINCE (and the SENT* variants).
type SearchDateOp int

const (
	DateBefore SearchDateOp = iota
	DateOn
	DateSince
)

// SearchDateKey is a date-window key. Internal = true: the INTERNALDATE axis
// (BEFORE/ON/SINCE — the mailstore's internal date is the server-controlled
// ingest timestamp, mail_messages.created_at, per the retention policy's
// forged-Date rationale). Internal = false: the SENT* keys over the Date
// header stored in mail_messages.date.
type SearchDateKey struct {
	Op       SearchDateOp
	Day      time.Time // UTC midnight of the client-supplied date
	Internal bool
}

// SearchSizeKey is LARGER (strictly greater) / SMALLER (strictly less).
type SearchSizeKey struct {
	Larger bool
	Bytes  int64
}

// SearchHeaderKey is the HEADER key: Field case-insensitive; Value is a
// case-insensitive substring; empty Value tests field existence. Fields with
// dedicated columns (Subject/From/To/Cc) match against the columns; all other
// fields and BCC match against the stored raw message's header block (bounded
// by MaxSearchBlobScanMessages).
type SearchHeaderKey struct {
	Field string
	Value string
	// Blob forces the raw-header path (BCC and friends).
	Blob bool
}

// SearchBodyKey is BODY <string>: full-text match through the FTS tier.
type SearchBodyKey struct{ Text string }

// SearchTextKey is TEXT <string>: headers + body — FTS hit or a substring of
// any stored header column.
type SearchTextKey struct{ Text string }

// SearchUIDKey is UID <sequence-set> (wildcards resolve against the folder).
type SearchUIDKey struct{ Set SeqSet }

// SearchSeqKey is a bare sequence-set (positions in UID order).
type SearchSeqKey struct{ Set SeqSet }

func (SearchAll) isSearchNode()       {}
func (*SearchAnd) isSearchNode()      {}
func (*SearchOr) isSearchNode()       {}
func (*SearchNot) isSearchNode()      {}
func (SearchFlagKey) isSearchNode()   {}
func (SearchDateKey) isSearchNode()   {}
func (SearchSizeKey) isSearchNode()   {}
func (SearchHeaderKey) isSearchNode() {}
func (SearchBodyKey) isSearchNode()   {}
func (SearchTextKey) isSearchNode()   {}
func (SearchUIDKey) isSearchNode()    {}
func (SearchSeqKey) isSearchNode()    {}

// MaxSearchBlobScanMessages bounds the raw-message header scans one search
// may perform (HEADER/BCC keys over non-column fields). Beyond the cap the
// key reports no-match for the remaining candidates — a documented bound, not
// a silent unbounded read.
const MaxSearchBlobScanMessages = 200

// searchEval is the per-search evaluation state.
type searchEval struct {
	svc       *Service
	ctx       context.Context
	mailboxID uint
	userID    uint
	folderID  uint
	seqOf     map[uint]uint32
	uidMax    uint32
	// ftsHits memoizes one BODY/TEXT query's matching message IDs.
	ftsHits map[string]map[uint]bool
	// blobBudget counts the remaining bounded header scans.
	blobBudget int
}

// ErrSearchUnavailable is surfaced when BODY/TEXT keys are used while no
// Searcher is wired (SetSearcher) — the servers refuse the command rather
// than silently under-match.
var ErrSearchFTSRequired = ErrSearchUnavailable

// SearchIMAP executes an IMAP SEARCH criteria tree against one folder.
// Results are UID-ascending; sequence numbers are computed from the same
// order FETCH uses.
func (s *Service) SearchIMAP(ctx context.Context, scope IMAPSearchScope, crit SearchNode) ([]IMAPSearchHit, error) {
	if crit == nil {
		crit = SearchAll{}
	}
	var (
		folder    *models.MailFolder
		mailboxID uint
		positions []fetchPosition
	)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		f, mb, err := s.resolveFolder(tx, scope.FolderID, scope.UserID, scope.WorkspaceID)
		if err != nil {
			return err
		}
		folder, mailboxID = f, mb.ID
		if err := tx.Model(&models.MailMessage{}).
			Select("id", "uid").
			Where("folder_id = ?", f.ID).
			Order("uid ASC").
			Scan(&positions).Error; err != nil {
			return fmt.Errorf("search scan of folder %d: %w", f.ID, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	seqOf := make(map[uint]uint32, len(positions))
	for i, p := range positions {
		seqOf[p.ID] = uint32(i + 1)
	}
	ev := &searchEval{
		svc:        s,
		ctx:        ctx,
		mailboxID:  mailboxID,
		userID:     scope.UserID,
		folderID:   folder.ID,
		seqOf:      seqOf,
		uidMax:     resolveUIDWildcardMax(lastUID(positions), folder.UIDNext),
		ftsHits:    make(map[string]map[uint]bool),
		blobBudget: MaxSearchBlobScanMessages,
	}

	// FTS reachability: refuse BODY/TEXT up front when no searcher is wired,
	// before loading rows.
	if needsFTS(crit) && s.searcher == nil {
		return nil, ErrSearchFTSRequired
	}

	var rows []models.MailMessage
	conds, args, pure := sqlConjunction(crit)
	if pure {
		// Pure conjunction: push every predicate into one parameterized WHERE.
		q := s.db.Model(&models.MailMessage{}).Where("folder_id = ?", folder.ID)
		for i := range conds {
			q = q.Where(conds[i], args[i]...)
		}
		if err := q.Order("uid ASC").Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("search folder %d: %w", folder.ID, err)
		}
	} else {
		// General tree: evaluate in Go over the folder's rows.
		if err := s.db.Model(&models.MailMessage{}).
			Where("folder_id = ?", folder.ID).
			Order("uid ASC").Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("search folder %d: %w", folder.ID, err)
		}
	}

	hits := make([]IMAPSearchHit, 0, len(rows))
	if pure {
		for i := range rows {
			m := &rows[i]
			hits = append(hits, IMAPSearchHit{MessageID: m.ID, UID: m.UID, Seq: seqOf[m.ID]})
		}
		return hits, nil
	}
	for i := range rows {
		m := &rows[i]
		ok, err := ev.eval(m, crit)
		if err != nil {
			return nil, err
		}
		if ok {
			hits = append(hits, IMAPSearchHit{MessageID: m.ID, UID: m.UID, Seq: seqOf[m.ID]})
		}
	}
	return hits, nil
}

// needsFTS reports whether the tree contains a BODY/TEXT key.
func needsFTS(n SearchNode) bool {
	switch t := n.(type) {
	case SearchBodyKey, SearchTextKey:
		return true
	case *SearchAnd:
		for _, c := range t.Children {
			if needsFTS(c) {
				return true
			}
		}
	case *SearchOr:
		return needsFTS(t.A) || needsFTS(t.B)
	case *SearchNot:
		return needsFTS(t.Child)
	}
	return false
}

// ─── SQL conjunction push-down ───────────────────────────────────────────────

// headerColumns maps SEARCH header fields onto stored columns for the SQL
// fast path.
var headerColumns = map[string]string{
	"subject": "subject",
	"from":    "from_addr",
	"to":      "to_addr",
	"cc":      "cc",
}

// sqlConjunction flattens a criteria tree into ANDed WHERE fragments when the
// tree is a pure conjunction of SQL-expressible leaves. Anything else (OR,
// NOT, FTS keys, blob-header keys, multi-range sets, sequence sets) returns
// pure=false and the caller falls back to in-Go evaluation.
func sqlConjunction(crit SearchNode) (conds []string, args [][]any, pure bool) {
	var walk func(n SearchNode) bool
	walk = func(n SearchNode) bool {
		switch t := n.(type) {
		case SearchAll:
			return true
		case *SearchAnd:
			for _, c := range t.Children {
				if !walk(c) {
					return false
				}
			}
			return true
		case SearchFlagKey:
			if t.Set {
				conds = append(conds, "(flags & ?) > 0")
				args = append(args, []any{t.Mask})
			} else {
				conds = append(conds, "(flags & ?) = 0")
				args = append(args, []any{t.Mask})
			}
			return true
		case SearchDateKey:
			col := "date"
			if t.Internal {
				col = "created_at"
			}
			day := t.Day.Truncate(24 * time.Hour).UTC()
			switch t.Op {
			case DateBefore:
				conds = append(conds, col+" < ?")
				args = append(args, []any{day})
			case DateSince:
				conds = append(conds, col+" >= ?")
				args = append(args, []any{day})
			case DateOn:
				// One args entry per condition — the caller pairs them by
				// index.
				conds = append(conds, col+" >= ?", col+" < ?")
				args = append(args, []any{day}, []any{day.AddDate(0, 0, 1)})
			}
			return true
		case SearchSizeKey:
			if t.Larger {
				conds = append(conds, "size > ?")
				args = append(args, []any{t.Bytes})
			} else {
				conds = append(conds, "size < ?")
				args = append(args, []any{t.Bytes})
			}
			return true
		case SearchHeaderKey:
			if t.Blob || t.Value == "" {
				return false // field-existence and raw-header keys stay in Go
			}
			col, ok := headerColumns[strings.ToLower(t.Field)]
			if !ok {
				return false
			}
			conds = append(conds, col+" ILIKE ? ESCAPE '\\'")
			args = append(args, []any{likePattern(t.Value)})
			return true
		}
		return false
	}
	if !walk(crit) {
		return nil, nil, false
	}
	return conds, args, true
}

// likePattern escapes LIKE wildcards and wraps the value for a case-tolerant
// substring match (Postgres ILIKE is already case-insensitive).
func likePattern(v string) string {
	var b strings.Builder
	b.WriteByte('%')
	for _, r := range v {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	return b.String()
}

// ─── In-Go tree evaluation ───────────────────────────────────────────────────

// eval evaluates the criteria tree for one message row.
func (ev *searchEval) eval(m *models.MailMessage, n SearchNode) (bool, error) {
	switch t := n.(type) {
	case SearchAll:
		return true, nil
	case *SearchAnd:
		for _, c := range t.Children {
			ok, err := ev.eval(m, c)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	case *SearchOr:
		a, err := ev.eval(m, t.A)
		if err != nil || a {
			return a, err
		}
		return ev.eval(m, t.B)
	case *SearchNot:
		ok, err := ev.eval(m, t.Child)
		return !ok, err
	case SearchFlagKey:
		set := m.Flags&t.Mask != 0
		return set == t.Set, nil
	case SearchDateKey:
		ts := m.Date
		if t.Internal {
			ts = m.CreatedAt
		}
		return matchDateWindow(ts, t.Day, t.Op), nil
	case SearchSizeKey:
		if t.Larger {
			return m.Size > t.Bytes, nil
		}
		return m.Size < t.Bytes, nil
	case SearchHeaderKey:
		return ev.matchHeader(m, t)
	case SearchBodyKey:
		hits, err := ev.ftsMatch(t.Text)
		if err != nil {
			return false, err
		}
		return hits[m.ID], nil
	case SearchTextKey:
		hits, err := ev.ftsMatch(t.Text)
		if err != nil {
			return false, err
		}
		if hits[m.ID] {
			return true, nil
		}
		// TEXT spans headers too: fall back to the stored header columns.
		needle := strings.ToLower(t.Text)
		return strings.Contains(strings.ToLower(m.Subject), needle) ||
			strings.Contains(strings.ToLower(m.FromAddr), needle) ||
			strings.Contains(strings.ToLower(m.ToAddr), needle) ||
			strings.Contains(strings.ToLower(m.Cc), needle), nil
	case SearchUIDKey:
		return t.Set.Resolve(ev.uidMax).Contains(m.UID), nil
	case SearchSeqKey:
		return t.Set.Resolve(ev.uidMax).Contains(ev.seqOf[m.ID]), nil
	}
	return false, nil
}

// matchDateWindow applies BEFORE/ON/SINCE against one UTC calendar day.
func matchDateWindow(ts time.Time, day time.Time, op SearchDateOp) bool {
	t := ts.UTC()
	start := day.Truncate(24 * time.Hour).UTC()
	end := start.AddDate(0, 0, 1)
	switch op {
	case DateBefore:
		return t.Before(start)
	case DateOn:
		return !t.Before(start) && t.Before(end)
	case DateSince:
		return !t.Before(start)
	}
	return false
}

// matchHeader evaluates one HEADER key against a row: dedicated columns first,
// bounded raw-header scans otherwise.
func (ev *searchEval) matchHeader(m *models.MailMessage, k SearchHeaderKey) (bool, error) {
	field := strings.ToLower(k.Field)
	needle := strings.ToLower(k.Value)
	if col, ok := headerColumns[field]; ok && !k.Blob {
		v := map[string]string{
			"subject":   m.Subject,
			"from_addr": m.FromAddr,
			"to_addr":   m.ToAddr,
			"cc":        m.Cc,
		}[col]
		if k.Value == "" {
			return strings.TrimSpace(v) != "", nil
		}
		return strings.Contains(strings.ToLower(v), needle), nil
	}
	// Raw-header path (bounded): read the stored message and match the field.
	if ev.blobBudget <= 0 {
		return false, nil
	}
	ev.blobBudget--
	raw, err := ev.svc.readBlob(ev.ctx, m.BodyBlobRef)
	if err != nil {
		// A missing blob must not abort the whole search: report no-match and
		// let the session surface corruption separately.
		return false, nil
	}
	hdr := parseEntity(raw, true).header
	for _, f := range parseHeaderFields(hdr) {
		if f.name != field {
			continue
		}
		if k.Value == "" {
			return true, nil
		}
		if strings.Contains(strings.ToLower(string(bytes.Join(f.lines, []byte(" ")))), needle) {
			return true, nil
		}
	}
	return false, nil
}

// ftsMatch runs one BODY/TEXT query through the wired Searcher (memoized per
// query within the search) and returns the matching message-ID set.
func (ev *searchEval) ftsMatch(text string) (map[uint]bool, error) {
	if hits, ok := ev.ftsHits[text]; ok {
		return hits, nil
	}
	hits := make(map[uint]bool)
	scope := SearchScope{MailboxID: ev.mailboxID, UserID: ev.userID}
	q := SearchQuery{Text: text, FolderID: ev.folderID, Limit: MaxSearchPageSize}
	res, _, err := ev.svc.searcher.Search(ev.ctx, scope, q)
	if err != nil {
		if errors.Is(err, ErrSearchQueryTooLong) {
			return nil, err
		}
		return nil, fmt.Errorf("mailstore: IMAP search FTS: %w", err)
	}
	for _, h := range res {
		hits[h.MessageID] = true
	}
	ev.ftsHits[text] = hits
	return hits, nil
}
