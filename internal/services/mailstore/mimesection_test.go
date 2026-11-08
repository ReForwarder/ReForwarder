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
	"strings"
	"testing"
)

// multipartRaw builds the canonical test message:
//
//	multipart/mixed
//	  1  text/plain
//	  2  multipart/alternative
//	    2.1 text/plain
//	    2.2 text/html
//	  3  application/pdf attachment (base64)
const multipartRaw = "From: a@example.test\r\n" +
	"To: t@example.test\r\n" +
	"Subject: structure probe\r\n" +
	"Message-Id: <struct@probe>\r\n" +
	"Date: Mon, 02 Nov 2026 10:00:00 +0000\r\n" +
	"X-Custom-Token: alpha-bravo\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"OUTER\"\r\n" +
	"\r\n" +
	"preamble to ignore\r\n" +
	"--OUTER\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: 7bit\r\n" +
	"\r\n" +
	"plain body alpha\r\n" +
	"--OUTER\r\n" +
	"Content-Type: multipart/alternative; boundary=\"INNER\"\r\n" +
	"\r\n" +
	"--INNER\r\n" +
	"Content-Type: text/plain\r\n" +
	"\r\n" +
	"alternative plain\r\n" +
	"--INNER\r\n" +
	"Content-Type: text/html\r\n" +
	"\r\n" +
	"<p>alternative html</p>\r\n" +
	"--INNER--\r\n" +
	"--OUTER\r\n" +
	"Content-Type: application/pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"r.pdf\"\r\n" +
	"\r\n" +
	"JVBERi0xLjQ=\r\n" +
	"--OUTER--\r\n" +
	"epilogue to ignore\r\n"

// TestLocateSectionMatrix exercises the BODY[...] section locator against the
// canonical multipart message: whole message, header-only, header field
// filtering, TEXT, MIME part paths (top-level, nested, attachment), part
// HEADER/TEXT/MIME terminals, and missing sections.
func TestLocateSectionMatrix(t *testing.T) {
	cases := []struct {
		name        string
		spec        sectionSpec
		want        string // exact bytes ("" = not byte-checked)
		wantOK      bool
		contains    string
		notContains string
	}{
		{
			name:   "whole message",
			spec:   sectionSpec{},
			wantOK: true,
			want:   multipartRaw,
		},
		{
			name:   "HEADER includes separating blank line",
			spec:   sectionSpec{Terminal: "HEADER"},
			wantOK: true,
			// The full header block: everything up to and including the blank
			// line (X-Custom-Token is followed by more headers, so the blank
			// line check must look at the tail).
			want:        multipartRaw[:strings.Index(multipartRaw, "\r\n\r\n")+4],
			notContains: "preamble to ignore",
		},
		{
			name:     "HEADER excludes body",
			spec:     sectionSpec{Terminal: "HEADER"},
			wantOK:   true,
			contains: "Subject: structure probe",
		},
		{
			name:        "TEXT excludes headers, keeps body through epilogue",
			spec:        sectionSpec{Terminal: "TEXT"},
			wantOK:      true,
			contains:    "preamble to ignore\r\n--OUTER\r\n",
			notContains: "Subject: structure probe",
		},
		{
			name:   "HEADER.FIELDS keeps continuations and blank line",
			spec:   sectionSpec{Terminal: "HEADER.FIELDS", Fields: []string{"SUBJECT"}},
			wantOK: true,
			want:   "Subject: structure probe\r\n\r\n",
		},
		{
			name:     "HEADER.FIELDS multiple, case-insensitive",
			spec:     sectionSpec{Terminal: "HEADER.FIELDS", Fields: []string{"subject", "FROM"}},
			wantOK:   true,
			contains: "From: a@example.test\r\n",
		},
		{
			name:     "HEADER.FIELDS.NOT",
			spec:     sectionSpec{Terminal: "HEADER.FIELDS.NOT", Fields: []string{"SUBJECT"}},
			wantOK:   true,
			contains: "X-Custom-Token: alpha-bravo\r\n",
		},
		{
			name:   "part 1 = first mixed child with its MIME header",
			spec:   sectionSpec{Part: "1"},
			wantOK: true,
			want:   "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 7bit\r\n\r\nplain body alpha",
		},
		{
			name:     "part 2 = nested multipart raw",
			spec:     sectionSpec{Part: "2"},
			wantOK:   true,
			contains: "--INNER\r\n",
		},
		{
			name:   "part 2.1 = alternative plain",
			spec:   sectionSpec{Part: "2.1"},
			wantOK: true,
			want:   "Content-Type: text/plain\r\n\r\nalternative plain",
		},
		{
			name:   "part 2.2 = alternative html",
			spec:   sectionSpec{Part: "2.2"},
			wantOK: true,
			want:   "Content-Type: text/html\r\n\r\n<p>alternative html</p>",
		},
		{
			name:   "part 3 = attachment as transmitted (not decoded)",
			spec:   sectionSpec{Part: "3"},
			wantOK: true,
			want:   "Content-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"r.pdf\"\r\n\r\nJVBERi0xLjQ=",
		},
		{
			name:     "part 2.MIME = MIME entity header of part 2",
			spec:     sectionSpec{Part: "2", Terminal: "MIME"},
			wantOK:   true,
			contains: "Content-Type: multipart/alternative; boundary=\"INNER\"",
		},
		{
			name:   "part 2.1.MIME",
			spec:   sectionSpec{Part: "2.1", Terminal: "MIME"},
			wantOK: true,
			want:   "Content-Type: text/plain\r\n\r\n",
		},
		{
			name:   "missing part",
			spec:   sectionSpec{Part: "4"},
			wantOK: false,
		},
		{
			name:   "missing nested part",
			spec:   sectionSpec{Part: "2.3"},
			wantOK: false,
		},
		{
			name:   "part 0 invalid",
			spec:   sectionSpec{Part: "0"},
			wantOK: false,
		},
		{
			name:   "deep missing",
			spec:   sectionSpec{Part: "1.1"},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := locateSection([]byte(multipartRaw), tc.spec)
			if ok != tc.wantOK {
				t.Fatalf("locateSection ok = %v, want %v (got %q)", ok, tc.wantOK, truncateForTest(got))
			}
			if !tc.wantOK {
				return
			}
			if tc.want != "" && string(got) != tc.want {
				t.Fatalf("section mismatch:\n got: %q\nwant: %q", truncateForTest(got), tc.want)
			}
			if tc.contains != "" && !strings.Contains(string(got), tc.contains) {
				t.Fatalf("section %q missing expected %q", truncateForTest(got), tc.contains)
			}
			if tc.notContains != "" && strings.Contains(string(got), tc.notContains) {
				t.Fatalf("section %q must not contain %q", truncateForTest(got), tc.notContains)
			}
		})
	}
}

// TestLocateSectionSimpleMessage covers the non-multipart shapes: BODY[1] is
// the whole content, HEADER/TEXT split at the blank line, and header-only
// messages (no blank line) fetch without dying.
func TestLocateSectionSimpleMessage(t *testing.T) {
	raw := []byte("From: a@example.test\r\nSubject: s\r\n\r\nline one\r\nline two\r\n")
	got, ok := locateSection(raw, sectionSpec{Part: "1"})
	if !ok || string(got) != "line one\r\nline two\r\n" {
		t.Fatalf("BODY[1] = %q (ok=%v), want the text body", got, ok)
	}
	got, _ = locateSection(raw, sectionSpec{Terminal: "HEADER"})
	if string(got) != "From: a@example.test\r\nSubject: s\r\n\r\n" {
		t.Fatalf("BODY[HEADER] = %q, want headers + blank line", got)
	}
	got, _ = locateSection(raw, sectionSpec{Terminal: "TEXT"})
	if string(got) != "line one\r\nline two\r\n" {
		t.Fatalf("BODY[TEXT] = %q, want the body", got)
	}
	headerOnly := []byte("From: a@example.test\r\nSubject: no blank line")
	if _, ok := locateSection(headerOnly, sectionSpec{Terminal: "TEXT"}); !ok {
		t.Fatalf("header-only TEXT fetch reported not-found")
	}
}

// TestHeaderFieldLinesContinuation keeps folded continuation lines with their
// field (a HEADER.FIELDS response must not orphan them).
func TestHeaderFieldLinesContinuation(t *testing.T) {
	header := []byte("Subject: first\r\nX-Long: one\r\n two\r\n\tthree\r\nSubject: folded\r\n second\r\nX-Other: keep-out\r\n")
	got := string(headerFieldLines(header, []string{"subject"}, false))
	want := "Subject: first\r\nSubject: folded\r\n second\r\n\r\n"
	if got != want {
		t.Fatalf("HEADER.FIELDS (subject) = %q, want %q", got, want)
	}
	notGot := string(headerFieldLines(header, []string{"subject"}, true))
	if !strings.Contains(notGot, "X-Long: one\r\n two\r\n\tthree\r\n") {
		t.Fatalf("HEADER.FIELDS.NOT lost the folded X-Long field: %q", notGot)
	}
	if strings.Contains(notGot, "Subject:") {
		t.Fatalf("HEADER.FIELDS.NOT must exclude Subject: %q", notGot)
	}
}

// TestParseSectionSpecValidation rejects malformed specifiers (the wire
// parser leans on this before handing a section spec to the locator).
func TestParseSectionSpecValidation(t *testing.T) {
	bad := []string{"HEADER.TEXT", "1.HEADER.TEXT", "1..2", "MIME", "1.TEXT.HEADER", "1HEADER"}
	for _, spec := range bad {
		if _, ok := parseSectionSpec(spec); ok {
			t.Errorf("parseSectionSpec(%q) accepted a malformed spec", spec)
		}
	}
	good := []string{"", "HEADER", "TEXT", "HEADER.FIELDS", "HEADER.FIELDS.NOT", "1", "2.HEADER", "2.MIME", "2.1.HEADER", "1.2.3"}
	for _, spec := range good {
		if _, ok := parseSectionSpec(spec); !ok {
			t.Errorf("parseSectionSpec(%q) rejected a valid spec", spec)
		}
	}
}

// TestSplitMultipartMissingCloseBoundary: a stored message with a missing
// close boundary still yields every part (lenient parsing, documented).
func TestSplitMultipartMissingCloseBoundary(t *testing.T) {
	body := []byte("--B\r\nContent-Type: text/plain\r\n\r\none\r\n--B\r\nContent-Type: text/plain\r\n\r\ntwo")
	parts := splitMultipart(body, "B")
	if len(parts) != 2 {
		t.Fatalf("splitMultipart = %d parts, want 2", len(parts))
	}
	if !strings.Contains(string(parts[1]), "two") {
		t.Fatalf("second part lost: %q", parts[1])
	}
}

// TestSplitMultipartExcludesDelimiters: the CRLF before a boundary belongs to
// the delimiter (RFC 2046 §5.1.1), never to the part content.
func TestSplitMultipartExcludesDelimiters(t *testing.T) {
	body := []byte("--B\r\nA\r\n\r\nB\r\n--B--\r\n")
	parts := splitMultipart(body, "B")
	if len(parts) != 1 {
		t.Fatalf("splitMultipart = %d parts, want 1", len(parts))
	}
	if bytes.Contains(parts[0], []byte("--B")) {
		t.Fatalf("part content leaks a delimiter: %q", parts[0])
	}
}

// TestBuildMimeStructureTree checks the structure tree the BODY/BODYSTRUCTURE
// responses render from: part ids, content types, boundaries, encodings.
func TestBuildMimeStructureTree(t *testing.T) {
	tree := BuildMimeStructure([]byte(multipartRaw))
	if tree.Type != "multipart" || tree.Subtype != "mixed" {
		t.Fatalf("root = %s/%s, want multipart/mixed", tree.Type, tree.Subtype)
	}
	if len(tree.Children) != 3 {
		t.Fatalf("root has %d children, want 3", len(tree.Children))
	}
	if tree.Children[0].PartID != "1" || tree.Children[0].Type != "text" {
		t.Fatalf("child 1 = %s (%s)", tree.Children[0].PartID, tree.Children[0].Type)
	}
	if tree.Children[1].Subtype != "alternative" || len(tree.Children[1].Children) != 2 {
		t.Fatalf("child 2 = %s with %d children", tree.Children[1].Subtype, len(tree.Children[1].Children))
	}
	if tree.Children[1].Children[1].PartID != "2.2" || tree.Children[1].Children[1].Subtype != "html" {
		t.Fatalf("child 2.2 = %s (%s)", tree.Children[1].Children[1].PartID, tree.Children[1].Children[1].Subtype)
	}
	if tree.Children[2].Filename != "r.pdf" || tree.Children[2].Encoding != "base64" {
		t.Fatalf("child 3 filename/encoding = %q/%q", tree.Children[2].Filename, tree.Children[2].Encoding)
	}
	if tree.Children[2].Disposition != "attachment" {
		t.Fatalf("child 3 disposition = %q", tree.Children[2].Disposition)
	}
}

func truncateForTest(b []byte) string {
	if len(b) > 120 {
		return string(b[:120]) + "...<truncated>"
	}
	return string(b)
}
