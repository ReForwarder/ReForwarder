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
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
)

// Pure-protocol unit tests: wire tokenizer, FETCH item parsing, SEARCH key
// compilation, LIST pattern matching, envelope/bodystructure rendering and
// the limiters. No database required — these run in every battery.

// TestTokenizerAtomsQuotedAndParens — the line scanner's token shapes.
func TestTokenizerAtomsQuotedAndParens(t *testing.T) {
	lr := &lineReader{maxLiteral: 1024}
	_, toks, litAtEnd, err := scanLine(`a1 LOGIN "user@x" "pass w\"d"`, lr)
	if err != nil || litAtEnd {
		t.Fatalf("scan: %v", err)
	}
	want := []token{
		{Kind: 'a', Val: "a1"},
		{Kind: 'a', Val: "LOGIN"},
		{Kind: 's', Val: "user@x"},
		{Kind: 's', Val: `pass w"d`},
	}
	if len(toks) != len(want) {
		t.Fatalf("tokens: %+v", toks)
	}
	for i := range want {
		if toks[i] != want[i] {
			t.Fatalf("token %d: got %+v want %+v", i, toks[i], want[i])
		}
	}
}

// TestTokenizerBodySectionFolding — BODY[HEADER.FIELDS (From To)]<0.10> folds
// into one atom token including the spaces and parens.
func TestTokenizerBodySectionFolding(t *testing.T) {
	lr := &lineReader{maxLiteral: 1024}
	_, toks, _, err := scanLine(`f1 1 BODY[HEADER.FIELDS (From To)]<0.10> UID`, lr)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var atoms []string
	for _, tk := range toks {
		if tk.Kind == 'a' {
			atoms = append(atoms, tk.Val)
		}
	}
	if len(atoms) != 4 || atoms[2] != "BODY[HEADER.FIELDS (From To)]<0.10>" || atoms[3] != "UID" {
		t.Fatalf("atoms: %#v", atoms)
	}
}

// TestTokenizerUnterminatedErrors — malformed inputs are protocol errors.
func TestTokenizerUnterminatedErrors(t *testing.T) {
	lr := &lineReader{maxLiteral: 1024}
	for _, bad := range []string{`a1 "unterminated`, `a1 BODY[HEADER (broken)`, `a1 {12`} {
		_, _, _, err := scanLine(bad, lr)
		if err == nil {
			t.Fatalf("input %q must error", bad)
		}
	}
}

// TestParseFetchItemsMacrosAndSections — the FETCH grammar.
func TestParseFetchItemsMacrosAndSections(t *testing.T) {
	items, err := parseFetchItems([]token{{Kind: 'a', Val: "ALL"}}, 0)
	if err != nil || len(items) != 4 {
		t.Fatalf("ALL: %v %v", items, err)
	}
	items, err = parseFetchItems([]token{{Kind: 'a', Val: "FAST"}}, 0)
	if err != nil || len(items) != 3 {
		t.Fatalf("FAST: %v %v", items, err)
	}
	items, err = parseFetchItems([]token{{Kind: 'a', Val: "FULL"}}, 0)
	if err != nil || len(items) != 5 {
		t.Fatalf("FULL: %v %v", items, err)
	}

	// BODY.PEEK section with field list and partial.
	items, err = parseFetchItems([]token{
		{Kind: 'a', Val: "FLAGS"},
		{Kind: 'a', Val: "BODY.PEEK[HEADER.FIELDS (From To)]<5.10>"},
	}, 0)
	if err != nil {
		t.Fatalf("section parse: %v", err)
	}
	if len(items) != 2 || items[1].Section == nil {
		t.Fatalf("items: %+v", items)
	}
	sec := items[1].Section
	if !sec.Peek || sec.Spec != "HEADER.FIELDS" || sec.Not || len(sec.Fields) != 2 {
		t.Fatalf("section: %+v", sec)
	}
	if !sec.Partial || sec.PartialOffset != 5 || sec.PartialLength != 10 {
		t.Fatalf("partial: %+v", sec)
	}
	if items[1].Name != "BODY[HEADER.FIELDS (From To)]<5>" {
		t.Fatalf("response name: %q", items[1].Name)
	}

	// HEADER.FIELDS.NOT.
	items, err = parseFetchItems([]token{{Kind: 'a', Val: "BODY[HEADER.FIELDS.NOT (Subject)]"}}, 0)
	if err != nil {
		t.Fatalf("fields.not: %v", err)
	}
	if !items[0].Section.Not || len(items[0].Section.Fields) != 1 {
		t.Fatalf("fields.not section: %+v", items[0].Section)
	}

	// RFC822 aliases map to section fetches.
	items, err = parseFetchItems([]token{{Kind: 'a', Val: "RFC822"}, {Kind: 'a', Val: "RFC822.HEADER"}, {Kind: 'a', Val: "RFC822.TEXT"}}, 0)
	if err != nil || len(items) != 3 {
		t.Fatalf("rfc822 aliases: %v %v", items, err)
	}
	if items[0].Section == nil || items[0].Section.Peek || items[0].Section.Spec != "" {
		t.Fatalf("RFC822 must be a full non-peek body: %+v", items[0].Section)
	}
	if items[1].Section == nil || !items[1].Section.Peek || items[1].Section.Spec != "HEADER" {
		t.Fatalf("RFC822.HEADER must be PEEK HEADER: %+v", items[1].Section)
	}
	if items[2].Section == nil || items[2].Section.Spec != "TEXT" {
		t.Fatalf("RFC822.TEXT must be TEXT: %+v", items[2].Section)
	}

	// Unknown items are protocol errors.
	if _, err := parseFetchItems([]token{{Kind: 'a', Val: "BOGUS"}}, 0); err == nil {
		t.Fatal("unknown item must error")
	}
}

// TestResponseNameRendering — PEEK sections answer as BODY[...]; partials
// carry the origin octet.
func TestResponseNameRendering(t *testing.T) {
	cases := []struct {
		sec  mailstore.BodySection
		want string
	}{
		{mailstore.BodySection{}, "BODY[]"},
		{mailstore.BodySection{Spec: "HEADER"}, "BODY[HEADER]"},
		{mailstore.BodySection{Spec: "TEXT"}, "BODY[TEXT]"},
		{mailstore.BodySection{Spec: "HEADER.FIELDS", Fields: []string{"From", "To"}}, "BODY[HEADER.FIELDS (From To)]"},
		{mailstore.BodySection{Spec: "HEADER.FIELDS.NOT", Fields: []string{"Subject"}}, "BODY[HEADER.FIELDS.NOT (Subject)]"},
		{mailstore.BodySection{Partial: true, PartialOffset: 12}, "BODY[]<12>"},
	}
	for _, tc := range cases {
		if got := responseName(&tc.sec); got != tc.want {
			t.Fatalf("responseName: got %q want %q", got, tc.want)
		}
	}
}

// TestParseSearchKeysCompilation — wire keys compile onto the mailstore tree.
func TestParseSearchKeysCompilation(t *testing.T) {
	node, err := parseSearchKeys([]token{{Kind: 'a', Val: "UNSEEN"}, {Kind: 'a', Val: "FROM"}, {Kind: 's', Val: "a@b"}})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	and, ok := node.(*mailstore.SearchAnd)
	if !ok || len(and.Children) != 2 {
		t.Fatalf("implicit AND expected: %#v", node)
	}
	if fl, ok := and.Children[0].(mailstore.SearchFlagKey); !ok || fl.Mask != models.FlagSeen || fl.Set {
		t.Fatalf("UNSEEN: %#v", and.Children[0])
	}
	hdr, ok := and.Children[1].(mailstore.SearchHeaderKey)
	if !ok || hdr.Field != "FROM" || hdr.Value != "a@b" {
		t.Fatalf("FROM: %#v", and.Children[1])
	}

	// OR takes two keys; NOT one.
	node, err = parseSearchKeys([]token{{Kind: 'a', Val: "OR"}, {Kind: 'a', Val: "SEEN"}, {Kind: 'a', Val: "FLAGGED"}})
	if err != nil {
		t.Fatalf("OR: %v", err)
	}
	if _, ok := node.(*mailstore.SearchOr); !ok {
		t.Fatalf("OR node: %#v", node)
	}
	node, err = parseSearchKeys([]token{{Kind: 'a', Val: "NOT"}, {Kind: 'a', Val: "SEEN"}})
	if err != nil {
		t.Fatalf("NOT: %v", err)
	}
	if _, ok := node.(*mailstore.SearchNot); !ok {
		t.Fatalf("NOT node: %#v", node)
	}

	// Parenthesized AND group.
	node, err = parseSearchKeys([]token{{Kind: '(', Val: "("}, {Kind: 'a', Val: "SEEN"}, {Kind: 'a', Val: "FLAGGED"}, {Kind: ')', Val: ")"}})
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if _, ok := node.(*mailstore.SearchAnd); !ok {
		t.Fatalf("group node: %#v", node)
	}

	// Dates.
	node, err = parseSearchKeys([]token{{Kind: 'a', Val: "BEFORE"}, {Kind: 's', Val: "1-Feb-1994"}})
	if err != nil {
		t.Fatalf("BEFORE: %v", err)
	}
	dk, ok := node.(mailstore.SearchDateKey)
	if !ok || !dk.Internal || dk.Op != mailstore.DateBefore {
		t.Fatalf("BEFORE node: %#v", node)
	}
	if dk.Day.UTC().Year() != 1994 || dk.Day.UTC().Month() != 2 || dk.Day.UTC().Day() != 1 {
		t.Fatalf("BEFORE day: %v", dk.Day)
	}

	// UID set + bare seq set.
	node, err = parseSearchKeys([]token{{Kind: 'a', Val: "UID"}, {Kind: 's', Val: "5:*"}})
	if err != nil {
		t.Fatalf("UID: %v", err)
	}
	if uk, ok := node.(mailstore.SearchUIDKey); !ok || len(uk.Set) != 1 {
		t.Fatalf("UID node: %#v", node)
	}
	node, err = parseSearchKeys([]token{{Kind: 's', Val: "1:3"}})
	if err != nil {
		t.Fatalf("bare set: %v", err)
	}
	if _, ok := node.(mailstore.SearchSeqKey); !ok {
		t.Fatalf("bare set node: %#v", node)
	}

	// Unsupported charset errors (mapped to NO [BADCHARSET]).
	_, err = parseSearchKeys([]token{{Kind: 'a', Val: "CHARSET"}, {Kind: 's', Val: "KOI8-R"}})
	if err != errSearchBadCharset {
		t.Fatalf("charset error: %v", err)
	}

	// Unknown key errors.
	_, err = parseSearchKeys([]token{{Kind: 'a', Val: "NOSUCHKEY"}})
	if err == nil {
		t.Fatal("unknown key must error")
	}
}

// TestParseIMAPDateFormat — d-MMM-yyyy parsing to UTC midnight.
func TestParseIMAPDateFormat(t *testing.T) {
	got, err := parseIMAPDate("7-Nov-2026")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if _, err := parseIMAPDate("invalid"); err == nil {
		t.Fatal("invalid date must error")
	}
}

// TestListPatternMatching — '*' spans, '%' stops at the delimiter, INBOX is
// case-insensitive.
func TestListPatternMatching(t *testing.T) {
	cases := []struct {
		path, pattern string
		want          bool
	}{
		{"INBOX", "*", true},
		{"INBOX", "INBOX", true},
		{"INBOX", "inbox", true},
		{"Arch/2026", "Arch/%", true},
		{"Arch/2026/Nov", "Arch/%", false},
		{"Arch/2026/Nov", "Arch/*", true},
		{"Arch/2026/Nov", "*/Nov", true},
		{"INBOX", "%", true},
		{"Arch", "%", true},
		{"Arch/2026", "%", false},
		{"", "", true},
		{"Arch", "INBOX", false},
	}
	for _, tc := range cases {
		if got := matchListPattern(tc.path, tc.pattern); got != tc.want {
			t.Fatalf("matchListPattern(%q, %q) = %v, want %v", tc.path, tc.pattern, got, tc.want)
		}
	}
}

// TestEnvelopeBuilding — the ENVELOPE structure from raw bytes.
func TestEnvelopeBuilding(t *testing.T) {
	raw := []byte("From: Ada Lovelace <ada@example.org>\r\nSender: secretary@example.org\r\nTo: rcpt1@example.org, Bob <rcpt2@example.org>\r\nCc: cc@example.org\r\nSubject: Hello there\r\nDate: Sat, 07 Nov 2026 10:00:00 +0000\r\nMessage-ID: <id@x>\r\n\r\nbody\r\n")
	env := buildEnvelope(raw)
	r := env.render()
	for _, want := range []string{
		`"Hello there"`,
		`"Ada Lovelace" NIL "ada@example.org" NIL`,
		`"secretary@example.org" NIL`,
		`"rcpt2@example.org" NIL`,
		`<id@x>`,
	} {
		if !strings.Contains(r, want) {
			t.Fatalf("envelope missing %s: %s", want, r)
		}
	}
	// Sender defaults to From when absent.
	env2 := buildEnvelope([]byte("From: a@b\r\nSubject: s\r\n\r\n"))
	if !strings.Contains(env2.render(), `"a@b" NIL`) {
		t.Fatalf("sender default: %s", env2.render())
	}
}

// TestBodystructureRendering — simple text, multipart, and message/rfc822.
func TestBodystructureRendering(t *testing.T) {
	// Simple text message.
	raw := []byte("From: a@b\r\nContent-Type: text/plain\r\n\r\nhello\r\nworld\r\n")
	part := mailstore.BuildMimeStructure(raw)
	bs := renderBodyStructure(part, true)
	if !strings.Contains(bs, `"TEXT" "PLAIN"`) || !strings.Contains(bs, `"7BIT"`) {
		t.Fatalf("bodystructure: %s", bs)
	}
	// Non-extensible BODY omits disposition/MD5.
	body := renderBodyStructure(part, false)
	if strings.Contains(body, `"ATTACHMENT"`) {
		t.Fatalf("non-extensible body must omit disposition: %s", body)
	}

	// Multipart message with an attachment.
	mp := []byte("From: a@b\r\nContent-Type: multipart/mixed; boundary=\"BB\"\r\n\r\n--BB\r\nContent-Type: text/plain\r\n\r\npart one\r\n--BB\r\nContent-Type: application/pdf; name=\"doc.pdf\"\r\nContent-Disposition: attachment; filename=\"doc.pdf\"\r\n\r\nPDFDATA\r\n--BB--\r\n")
	tree := mailstore.BuildMimeStructure(mp)
	bs = renderBodyStructure(tree, true)
	if !strings.Contains(bs, `"MIXED"`) || !strings.Contains(bs, `"APPLICATION" "PDF"`) || !strings.Contains(bs, `"ATTACHMENT"`) || !strings.Contains(bs, `"doc.pdf"`) {
		t.Fatalf("multipart bodystructure: %s", bs)
	}

	// A nil tree renders NIL (unreadable message — honest answer).
	if renderBodyStructure(nil, true) != "NIL" {
		t.Fatal("nil tree must render NIL")
	}
}

// TestFlagListRoundTrip — parseFlagList and flagNames agree on the system
// flags; \Recent is server-managed and ignored in client stores.
func TestFlagListRoundTrip(t *testing.T) {
	set := parseFlagList([]string{`\Seen`, `\Flagged`})
	if set == 0 {
		t.Fatal("flags not parsed")
	}
	names := flagNames(set)
	if !strings.Contains(names, "\\Seen") || !strings.Contains(names, "\\Flagged") {
		t.Fatalf("names: %q", names)
	}
	recentNames := flagNames(parseFlagList([]string{`\Recent`}))
	if strings.Contains(recentNames, "\\Recent") {
		t.Fatal("client \\Recent stores must be ignored")
	}
	if got := flagNames(0); got != "" {
		t.Fatalf("empty flags: %q", got)
	}
}

// TestCmdLimiterSlidingWindow — the per-session command budget.
func TestCmdLimiterSlidingWindow(t *testing.T) {
	l := newCmdLimiter(3, 60)
	for i := 0; i < 3; i++ {
		if !l.allow() {
			t.Fatalf("allow %d failed", i)
		}
	}
	if l.allow() {
		t.Fatal("4th command must be refused")
	}
	// Zero limit disables.
	l0 := newCmdLimiter(0, 60)
	for i := 0; i < 100; i++ {
		if !l0.allow() {
			t.Fatal("disabled limiter must always allow")
		}
	}
}

// TestConnTrackerConcurrentCap — acquire/release keeps the per-IP cap exact.
func TestConnTrackerConcurrentCap(t *testing.T) {
	tr := newConnTracker(2)
	if !tr.acquire("1.2.3.4") || !tr.acquire("1.2.3.4") {
		t.Fatal("first two acquires must pass")
	}
	if tr.acquire("1.2.3.4") {
		t.Fatal("third concurrent connection must be refused")
	}
	if !tr.acquire("5.6.7.8") {
		t.Fatal("other IPs are independent")
	}
	tr.release("1.2.3.4")
	if !tr.acquire("1.2.3.4") {
		t.Fatal("release must free a slot")
	}
	tr.release("1.2.3.4")
	tr.release("1.2.3.4")
	tr.release("1.2.3.4") // over-release is safe
	if !tr.acquire("1.2.3.4") || !tr.acquire("1.2.3.4") {
		t.Fatal("slots must be reusable after full release")
	}
}

// TestQuoteStringEscaping — the two mandatory IMAP quoted-string escapes.
func TestQuoteStringEscaping(t *testing.T) {
	if got := quoteString(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("quote: %q", got)
	}
}

// TestLiteralTooLargeErrorShape — the rejection error carries the tag.
func TestLiteralTooLargeErrorShape(t *testing.T) {
	err := &literalTooLargeError{size: 1000, tag: "a1"}
	if !strings.Contains(err.Error(), "1000") {
		t.Fatalf("error text: %q", err.Error())
	}
	if err.tag != "a1" {
		t.Fatalf("tag: %q", err.tag)
	}
}
