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
	"strings"
	"unicode/utf8"

	pdf "github.com/ledongthuc/pdf"
)

// Attachment text extraction for search (SF-112, the EVALUATIONS.md §2
// narrow trigger: attachment-content search). At ingest, the text extractable
// from the stored attachments is concatenated with per-attachment markers and
// kept in mail_messages.attachments_text, which the mail_messages.search_tsv
// generated column weights with the 'D' class — so an attachment hit is
// distinguishable from a body hit and the search surface can flag it
// (search.go's attachment_hit).
//
// The slice's boundary (the ADR-006/ClamAV interplay, per the evaluation):
// extraction parses the DECRYPTED bytes of already-stored attachments at the
// mailstore seam; the parsers are pure Go (no CGO, no shell-outs), the input
// is bounded (per-attachment and total budgets below), and a parse failure
// degrades to "no text for this attachment" — a broken document must never
// fail ingest or break a search. Malware scanning composes where configured;
// extraction does not weaken any gate.

const (
	// maxExtractAttachmentBytes bounds the raw bytes fed to a parser per
	// attachment (parsers run on untrusted input; bigger documents are
	// truncated out of the search index, not out of storage).
	maxExtractAttachmentBytes = 256 * 1024
	// maxAttachmentsTextTotal bounds the whole attachments_text column (the
	// FTS budget mirrors maxSearchBodyBytes' posture).
	maxAttachmentsTextTotal = 1024 * 1024
	// maxExtractAttachments bounds how many attachments of one message are
	// extracted (the rest keep their blob + metadata, no text).
	maxExtractAttachments = 20
	// maxExtractTextRunes bounds the per-attachment extracted text after
	// parsing (a pathological PDF can expand hugely; the column budget is the
	// hard stop).
	maxExtractTextRunes = 64 * 1024
)

// extractAttachmentsText builds mail_messages.attachments_text for one
// message: per-attachment marker lines ([attachment: <name>]) followed by the
// extracted text, concatenated and bounded by maxAttachmentsTextTotal.
func extractAttachmentsText(atts []ParsedAttachment) string {
	var b strings.Builder
	used := 0
	count := 0
	for _, a := range atts {
		if count >= maxExtractAttachments || used >= maxAttachmentsTextTotal {
			break
		}
		count++
		text := extractAttachmentText(a)
		if strings.TrimSpace(text) == "" {
			continue
		}
		section := "[attachment: " + sanitizeMarker(a.Filename) + "]\n" + text + "\n"
		if r := utf8.RuneCountInString(section); used+r > maxAttachmentsTextTotal {
			remaining := maxAttachmentsTextTotal - used
			if remaining <= 0 {
				break
			}
			section = truncateRunes(section, remaining)
			used += remaining
			b.WriteString(section)
			break
		}
		used += utf8.RuneCountInString(section)
		b.WriteString(section)
	}
	return b.String()
}

// extractAttachmentText dispatches per attachment type: textual types pass
// through bounded; HTML is tag-stripped (the same reducer the snippet path
// uses); PDF runs through the pure-Go extractor (github.com/ledongthuc/pdf —
// BSD-3-Clause, no CGO, no shell-outs; the dependency choice is recorded in
// the SF-112 completion record per the evaluation's license-review note);
// anything else (binary formats, archives) yields no text in this slice.
func extractAttachmentText(a ParsedAttachment) string {
	content := a.Content
	if len(content) > maxExtractAttachmentBytes {
		content = content[:maxExtractAttachmentBytes]
	}
	mt := strings.ToLower(strings.TrimSpace(a.ContentType))
	switch {
	case strings.HasPrefix(mt, "application/pdf"):
		return extractPDFText(content)
	case strings.HasPrefix(mt, "text/html"):
		return stripHTMLTags(string(content))
	case strings.HasPrefix(mt, "text/"),
		strings.Contains(mt, "json"),
		strings.Contains(mt, "xml"),
		strings.Contains(mt, "csv"):
		return truncateRunes(string(content), maxExtractTextRunes)
	default:
		// By filename extension as a last resort — some senders mislabel.
		name := strings.ToLower(a.Filename)
		switch {
		case strings.HasSuffix(name, ".pdf"):
			return extractPDFText(content)
		case strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".htm"):
			return stripHTMLTags(string(content))
		case strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".md") ||
			strings.HasSuffix(name, ".csv") || strings.HasSuffix(name, ".json") ||
			strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".log"):
			return truncateRunes(string(content), maxExtractTextRunes)
		default:
			return ""
		}
	}
}

// extractPDFText extracts the text of a PDF document with the pure-Go parser.
// A parse failure degrades to "" (the attachment keeps its blob; extraction
// must never fail ingest).
func extractPDFText(raw []byte) string {
	defer func() {
		// The parser runs on untrusted bytes; a panic degrades to no text
		// exactly like a parse error.
		_ = recover()
	}()
	doc, err := pdf.NewReader(strings.NewReader(string(raw)), int64(len(raw)))
	if err != nil {
		return ""
	}
	var b strings.Builder
	for i := 1; i <= doc.NumPage(); i++ {
		page := doc.Page(i)
		if page.V.IsNull() {
			continue
		}
		lines, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		b.WriteString(lines)
		b.WriteString("\n")
		if utf8.RuneCountInString(b.String()) > maxExtractTextRunes {
			break
		}
	}
	return truncateRunes(b.String(), maxExtractTextRunes)
}

// sanitizeMarker strips the marker delimiters out of a filename so an
// attachment name cannot forge a section boundary in attachments_text.
func sanitizeMarker(name string) string {
	name = strings.ReplaceAll(name, "[", "(")
	name = strings.ReplaceAll(name, "]", ")")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.ReplaceAll(name, "\r", " ")
	return name
}

// truncateRunes bounds s to max runes (bounded parsing + bounded columns).
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}
