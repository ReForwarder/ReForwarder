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

package imap

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
)

// IMAP SEARCH (RFC 9051 §6.4.4) — the ADR-004 key set: ALL ANSWERED BCC
// BEFORE BODY CC DELETED DRAFT FLAGGED FROM HEADER KEYWORD LARGER NEW NOT OLD
// ON OR RECENT SEEN SENTBEFORE SENTON SENTSINCE SINCE SMALLER SUBJECT TEXT TO
// UID UNANSWERED UNDELETED UNFLAGGED UNSEEN, plus bare sequence sets and
// CHARSET. Keys compile onto the W1 SearchNode tree (mailstore.SearchIMAP
// executes it with SQL push-down for pure conjunctions). Custom KEYWORD
// atoms recognize only the system flags (no custom-keyword storage — the
// documented mailstore limitation).

// searchCharsets are the accepted SEARCH CHARSET values (the RFC 9051
// default set).
var searchCharsets = map[string]bool{"UTF-8": true, "US-ASCII": true}

// cmdSearch implements SEARCH and UID SEARCH. Results come back in UID order
// (the mailstore's), rendered as sequence numbers (SEARCH) or UIDs (UID
// SEARCH) per RFC 9051.
func (st *session) cmdSearch(cmd *commandTokens, uidMode bool) error {
	f, ok := st.requireSelection(cmd.Tag)
	if !ok {
		return nil
	}
	crit, err := parseSearchKeys(cmd.Args)
	if err != nil {
		if errors.Is(err, errSearchBadCharset) {
			st.tagged(cmd.Tag, "NO", "[BADCHARSET (US-ASCII UTF-8)] Unsupported charset")
			return nil
		}
		st.tagged(cmd.Tag, "BAD", err.Error())
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := st.refreshView(ctx); err != nil {
		st.tagged(cmd.Tag, "NO", "SEARCH failed: view refresh")
		return nil
	}
	hits, err := st.srv.store.SearchIMAP(ctx, mailstore.IMAPSearchScope{
		UserID:   st.user.ID,
		FolderID: f.ID,
	}, crit)
	if err != nil {
		if errors.Is(err, errSearchBadCharset) {
			st.tagged(cmd.Tag, "NO", "[BADCHARSET (US-ASCII UTF-8)] Unsupported charset")
			return nil
		}
		st.tagged(cmd.Tag, "NO", "SEARCH failed: "+err.Error())
		return nil
	}
	var b strings.Builder
	b.WriteString("* SEARCH")
	for _, h := range hits {
		b.WriteByte(' ')
		if uidMode {
			b.WriteString(strconv.FormatUint(uint64(h.UID), 10))
		} else {
			b.WriteString(strconv.FormatUint(uint64(h.Seq), 10))
		}
	}
	b.WriteString("\r\n")
	st.write(b.String())
	st.srv.auditf(AuditSearch, st.user.ID, st.user.Email, st.remote,
		map[string]any{"folder": f.IMAPPath, "hits": len(hits)})
	st.tagged(cmd.Tag, "OK", "SEARCH completed")
	return nil
}

// parseSearchKeys compiles the wire keys into the mailstore criteria tree.
// The key list is an implicit AND.
func parseSearchKeys(toks []token) (mailstore.SearchNode, error) {
	p := &searchParser{toks: toks}
	nodes, err := p.parseList()
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return mailstore.SearchAll{}, nil
	}
	if len(nodes) == 1 {
		return nodes[0], nil
	}
	return &mailstore.SearchAnd{Children: nodes}, nil
}

type searchParser struct {
	toks []token
	i    int
}

func (p *searchParser) next() (token, bool) {
	if p.i >= len(p.toks) {
		return token{}, false
	}
	t := p.toks[p.i]
	p.i++
	return t, true
}

func (p *searchParser) peek() (token, bool) {
	if p.i >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.i], true
}

// parseList parses keys until the closing paren (or end of input).
func (p *searchParser) parseList() ([]mailstore.SearchNode, error) {
	var nodes []mailstore.SearchNode
	for {
		t, ok := p.peek()
		if !ok {
			return nodes, nil
		}
		if t.Kind == ')' {
			p.i++
			return nodes, nil
		}
		if t.Kind == '(' {
			p.i++
			children, err := p.parseList()
			if err != nil {
				return nil, err
			}
			if len(children) == 0 {
				return nil, fmt.Errorf("%w: empty search key list", errProtocol)
			}
			if len(children) == 1 {
				nodes = append(nodes, children[0])
			} else {
				nodes = append(nodes, &mailstore.SearchAnd{Children: children})
			}
			continue
		}
		node, err := p.parseKey()
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
}

// keyWord reads the next atom uppercased.
func (p *searchParser) keyWord() (string, error) {
	t, ok := p.next()
	if !ok || (t.Kind != 'a' && t.Kind != 's') {
		return "", fmt.Errorf("%w: search key expected", errProtocol)
	}
	return strings.ToUpper(t.Val), nil
}

// keyString reads the next string argument.
func (p *searchParser) keyString(what string) (string, error) {
	t, ok := p.next()
	if !ok || (t.Kind != 'a' && t.Kind != 's') {
		return "", fmt.Errorf("%w: %s expected", errProtocol, what)
	}
	return t.Val, nil
}

// keyNumber reads the next numeric argument.
func (p *searchParser) keyNumber(what string) (int64, error) {
	v, err := p.keyString(what)
	if err != nil {
		return 0, err
	}
	n, cerr := strconv.ParseInt(v, 10, 64)
	if cerr != nil {
		return 0, fmt.Errorf("%w: %s must be numeric", errProtocol, what)
	}
	return n, nil
}

// keySet reads a sequence-set argument.
func (p *searchParser) keySet(what string) (mailstore.SeqSet, error) {
	v, err := p.keyString(what)
	if err != nil {
		return nil, err
	}
	return mailstore.ParseSeqSet(v)
}

// keyDate parses an IMAP date ("1-Feb-1994") into UTC midnight.
func (p *searchParser) keyDate(what string) (time.Time, error) {
	v, err := p.keyString(what)
	if err != nil {
		return time.Time{}, err
	}
	return parseIMAPDate(v)
}

// parseIMAPDate parses the IMAP date format d-MMM-yyyy (also dMMMy shorthand
// tolerance).
func parseIMAPDate(s string) (time.Time, error) {
	s = strings.TrimSpace(strings.Trim(s, `"`))
	t, err := time.Parse("2-Jan-2006", s)
	if err != nil {
		t, err = time.Parse("2-Jan-06", s)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid date %q (use d-MMM-yyyy)", errProtocol, s)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
}

// flagNode builds a flag presence/absence key.
func flagNode(mask uint32, set bool) mailstore.SearchNode {
	return mailstore.SearchFlagKey{Mask: mask, Set: set}
}

// parseKey compiles one key.
func (p *searchParser) parseKey() (mailstore.SearchNode, error) {
	kw, err := p.keyWord()
	if err != nil {
		return nil, err
	}
	switch kw {
	case "ALL":
		return mailstore.SearchAll{}, nil
	case "ANSWERED":
		return flagNode(models.FlagAnswered, true), nil
	case "UNANSWERED":
		return flagNode(models.FlagAnswered, false), nil
	case "SEEN":
		return flagNode(models.FlagSeen, true), nil
	case "UNSEEN":
		return flagNode(models.FlagSeen, false), nil
	case "FLAGGED":
		return flagNode(models.FlagFlagged, true), nil
	case "UNFLAGGED":
		return flagNode(models.FlagFlagged, false), nil
	case "DELETED":
		return flagNode(models.FlagDeleted, true), nil
	case "UNDELETED":
		return flagNode(models.FlagDeleted, false), nil
	case "DRAFT":
		return flagNode(models.FlagDraft, true), nil
	case "UNDRAFT":
		return flagNode(models.FlagDraft, false), nil
	case "RECENT":
		return flagNode(models.FlagRecent, true), nil
	case "OLD":
		return flagNode(models.FlagRecent, false), nil
	case "NEW":
		// NEW = RECENT ∧ UNSEEN (RFC 9051 §9.6.4).
		return &mailstore.SearchAnd{Children: []mailstore.SearchNode{
			flagNode(models.FlagRecent, true), flagNode(models.FlagSeen, false),
		}}, nil
	case "KEYWORD", "UNKEYWORD":
		v, kerr := p.keyString(kw)
		if kerr != nil {
			return nil, kerr
		}
		set := kw == "KEYWORD"
		switch strings.ToUpper(strings.TrimPrefix(v, "\\")) {
		case "SEEN":
			return flagNode(models.FlagSeen, set), nil
		case "ANSWERED":
			return flagNode(models.FlagAnswered, set), nil
		case "FLAGGED":
			return flagNode(models.FlagFlagged, set), nil
		case "DRAFT":
			return flagNode(models.FlagDraft, set), nil
		case "DELETED":
			return flagNode(models.FlagDeleted, set), nil
		case "RECENT":
			return flagNode(models.FlagRecent, set), nil
		}
		// Custom keywords have no storage: KEYWORD never matches, UNKEYWORD
		// always matches (the documented mailstore limitation).
		return mailstore.SearchFlagKey{Mask: models.FlagRecent, Set: !set}, nil

	case "BEFORE", "ON", "SINCE":
		day, derr := p.keyDate(kw)
		if derr != nil {
			return nil, derr
		}
		op := opFor(kw)
		return mailstore.SearchDateKey{Op: op, Day: day, Internal: true}, nil
	case "SENTBEFORE", "SENTON", "SENTSINCE":
		day, derr := p.keyDate(kw)
		if derr != nil {
			return nil, derr
		}
		op := opFor(strings.TrimPrefix(kw, "SENT"))
		return mailstore.SearchDateKey{Op: op, Day: day, Internal: false}, nil

	case "LARGER", "SMALLER":
		n, nerr := p.keyNumber(kw)
		if nerr != nil {
			return nil, nerr
		}
		return mailstore.SearchSizeKey{Larger: kw == "LARGER", Bytes: n}, nil

	case "FROM", "TO", "CC", "SUBJECT":
		v, serr := p.keyString(kw)
		if serr != nil {
			return nil, serr
		}
		return mailstore.SearchHeaderKey{Field: kw, Value: v}, nil
	case "BCC":
		v, serr := p.keyString(kw)
		if serr != nil {
			return nil, serr
		}
		return mailstore.SearchHeaderKey{Field: "Bcc", Value: v, Blob: true}, nil
	case "HEADER":
		field, ferr := p.keyString("header field")
		if ferr != nil {
			return nil, ferr
		}
		value, verr := p.keyString("header value")
		if verr != nil {
			return nil, verr
		}
		return mailstore.SearchHeaderKey{Field: field, Value: value}, nil

	case "BODY":
		v, serr := p.keyString("BODY text")
		if serr != nil {
			return nil, serr
		}
		return mailstore.SearchBodyKey{Text: v}, nil
	case "TEXT":
		v, serr := p.keyString("TEXT text")
		if serr != nil {
			return nil, serr
		}
		return mailstore.SearchTextKey{Text: v}, nil

	case "UID":
		set, serr := p.keySet("UID sequence set")
		if serr != nil {
			return nil, serr
		}
		return mailstore.SearchUIDKey{Set: set}, nil

	case "OR":
		a, aerr := p.parseKey()
		if aerr != nil {
			return nil, aerr
		}
		b, berr := p.parseKey()
		if berr != nil {
			return nil, berr
		}
		return &mailstore.SearchOr{A: a, B: b}, nil
	case "NOT":
		child, cerr := p.parseKey()
		if cerr != nil {
			return nil, cerr
		}
		return &mailstore.SearchNot{Child: child}, nil

	case "CHARSET":
		v, serr := p.keyString("charset")
		if serr != nil {
			return nil, serr
		}
		if !searchCharsets[strings.ToUpper(v)] {
			// The caller maps this to NO [BADCHARSET (US-ASCII UTF-8)].
			return nil, errSearchBadCharset
		}
		// CHARSET is transparent for the remaining keys (all matching is
		// byte/UTF-8 based).
		return p.parseKey()

	default:
		// A bare sequence set is a valid key.
		set, serr := mailstore.ParseSeqSet(kw)
		if serr != nil {
			return nil, fmt.Errorf("%w: unknown search key %q", errProtocol, kw)
		}
		return mailstore.SearchSeqKey{Set: set}, nil
	}
}

func opFor(kw string) mailstore.SearchDateOp {
	switch kw {
	case "BEFORE":
		return mailstore.DateBefore
	case "ON":
		return mailstore.DateOn
	default:
		return mailstore.DateSince
	}
}

// errSearchBadCharset maps to NO [BADCHARSET (US-ASCII UTF-8)].
var errSearchBadCharset = fmt.Errorf("imap: unsupported charset")
