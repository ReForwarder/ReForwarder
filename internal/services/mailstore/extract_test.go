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

package mailstore

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// SF-112 unit tests: the extraction-for-search slice's per-type behavior, the
// marker format, and the bounds. The PDF fixture is a hand-built minimal
// document (the offsets are computed, not guessed — see buildMinimalPDF).

// buildMinimalPDF renders one-page PDF with one text run. The xref table is
// computed from the emitted bytes, so the document is a legal target for the
// pure-Go parser.
func buildMinimalPDF(t *testing.T, text string) []byte {
	t.Helper()
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	objects := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n",
		fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(stream), stream),
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects))
	for _, o := range objects {
		offsets = append(offsets, buf.Len())
		buf.WriteString(o)
	}
	xrefAt := buf.Len()
	buf.WriteString(fmt.Sprintf("xref\n0 %d\n", len(objects)+1))
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		buf.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
	}
	buf.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefAt))
	return buf.Bytes()
}

func TestExtractTextPlain(t *testing.T) {
	got := extractAttachmentText(ParsedAttachment{
		Filename: "notes.txt", ContentType: "text/plain",
		Content: []byte("quarterly budget figures"),
	})
	if got != "quarterly budget figures" {
		t.Fatalf("text/plain extraction = %q", got)
	}
}

func TestExtractHTMLStripped(t *testing.T) {
	got := extractAttachmentText(ParsedAttachment{
		Filename: "report.html", ContentType: "text/html",
		Content: []byte("<html><body><p>invoice <b>total</b> 1,200</p></body></html>"),
	})
	joined := strings.Join(strings.Fields(got), " ")
	if !strings.Contains(joined, "invoice") || !strings.Contains(joined, "total") {
		t.Fatalf("html extraction lost the text: %q", got)
	}
	if strings.Contains(got, "<p>") || strings.Contains(got, "<b>") {
		t.Fatalf("html extraction kept tags: %q", got)
	}
}

func TestExtractPDF(t *testing.T) {
	got := extractAttachmentText(ParsedAttachment{
		Filename: "statement.pdf", ContentType: "application/pdf",
		Content: buildMinimalPDF(t, "devpdf-extract-token"),
	})
	if !strings.Contains(got, "devpdf-extract-token") {
		t.Fatalf("pdf extraction lost the token: %q", got)
	}
}

func TestExtractPDFGarbageDegradesToEmpty(t *testing.T) {
	got := extractAttachmentText(ParsedAttachment{
		Filename: "broken.pdf", ContentType: "application/pdf",
		Content: []byte("%PDF-1.4 not actually a pdf"),
	})
	if strings.TrimSpace(got) != "" {
		t.Fatalf("a broken pdf must degrade to no text, got %q", got)
	}
}

func TestExtractUnknownTypeYieldsNothing(t *testing.T) {
	got := extractAttachmentText(ParsedAttachment{
		Filename: "archive.zip", ContentType: "application/zip",
		Content: []byte("PK\x03\x04 whatever"),
	})
	if got != "" {
		t.Fatalf("a binary archive must yield no text, got %q", got)
	}
}

func TestExtractFilenameFallback(t *testing.T) {
	// A mislabeled content type falls back to the filename extension.
	got := extractAttachmentText(ParsedAttachment{
		Filename: "readme.txt", ContentType: "application/octet-stream",
		Content: []byte("fallback text"),
	})
	if got != "fallback text" {
		t.Fatalf("filename fallback = %q", got)
	}
}

func TestExtractAttachmentsTextMarkersAndBounds(t *testing.T) {
	atts := []ParsedAttachment{
		{Filename: "a.txt", ContentType: "text/plain", Content: []byte("alpha token")},
		{Filename: "b.pdf", ContentType: "application/pdf", Content: buildMinimalPDF(t, "beta token")},
		{Filename: "broken.bin", ContentType: "application/octet-stream", Content: []byte("\x00\x01")},
	}
	got := extractAttachmentsText(atts)
	if !strings.Contains(got, "[attachment: a.txt]") || !strings.Contains(got, "alpha token") {
		t.Fatalf("marker/section missing for a.txt: %q", got)
	}
	if !strings.Contains(got, "[attachment: b.pdf]") || !strings.Contains(got, "beta token") {
		t.Fatalf("marker/section missing for b.pdf: %q", got)
	}
	if strings.Contains(got, "broken.bin") {
		t.Fatalf("the no-text attachment must not produce a section: %q", got)
	}
	// A filename cannot forge a section boundary.
	forge := extractAttachmentsText([]ParsedAttachment{
		{Filename: "x]\n[attachment: forged", ContentType: "text/plain", Content: []byte("data")},
	})
	if strings.Contains(forge, "[attachment: forged]") {
		t.Fatalf("a filename forged a marker: %q", forge)
	}
}

func TestExtractBoundsPerAttachmentAndTotal(t *testing.T) {
	big := strings.Repeat("word ", maxExtractAttachmentBytes/5+10)
	got := extractAttachmentText(ParsedAttachment{
		Filename: "big.txt", ContentType: "text/plain", Content: []byte(big),
	})
	if utf8RuneCount(got) > maxExtractTextRunes {
		t.Fatalf("per-attachment extraction exceeded the bound: %d runes", utf8RuneCount(got))
	}

	// The total budget: many attachments cannot exceed it.
	many := make([]ParsedAttachment, 0, 40)
	for i := 0; i < 40; i++ {
		many = append(many, ParsedAttachment{
			Filename: fmt.Sprintf("f%d.txt", i), ContentType: "text/plain",
			Content: []byte(strings.Repeat("payload ", maxExtractTextRunes/8)),
		})
	}
	total := extractAttachmentsText(many)
	if utf8RuneCount(total) > maxAttachmentsTextTotal {
		t.Fatalf("attachments_text exceeded the total bound: %d runes", utf8RuneCount(total))
	}
}

func utf8RuneCount(s string) int { return len([]rune(s)) }
