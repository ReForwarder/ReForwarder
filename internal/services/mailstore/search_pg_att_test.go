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
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// SF-112 (the extraction-for-search slice): attachment-content search.

// mimeAttachment is one attachment of buildMIMEWithAttachments: name and
// content type matter for the extractor's dispatch (SF-112).
type mimeAttachment struct {
	Name string
	CT   string
	Body []byte
}

// buildMIMEWithAttachments builds one multipart/mixed message with the given
// base64-encoded attachments.
func buildMIMEWithAttachments(subject, body string, atts ...mimeAttachment) []byte {
	var b strings.Builder
	b.WriteString("Message-ID: <" + subject + "-att@test>\r\n")
	b.WriteString("From: s@example.test\r\nTo: r@example.test\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: Mon, 02 Nov 2026 10:00:00 +0000\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n\r\n")
	b.WriteString("--BOUND\r\nContent-Type: text/plain\r\n\r\n")
	b.WriteString(body + "\r\n")
	for i, a := range atts {
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("a%d.bin", i)
		}
		ct := a.CT
		if ct == "" {
			ct = "application/octet-stream"
		}
		b.WriteString(fmt.Sprintf("--BOUND\r\nContent-Type: %s; name=\"%s\"\r\n", ct, name))
		b.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"\r\n", name))
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(base64.StdEncoding.EncodeToString(a.Body) + "\r\n")
	}
	b.WriteString("--BOUND--\r\n")
	return []byte(b.String())
}

// TestPGSearchAttachmentHits — attachment content is extracted at ingest,
// indexed with the 'D' weight, and surfaced: a hit whose token lives in the
// attachment carries AttachmentHit=true; a body hit carries false; a PDF
// attachment's extracted text is searchable.
func TestPGSearchAttachmentHits(t *testing.T) {
	svc, db := newPGService(t)
	s := &PostgresSearcher{db: db}
	suffix := pgRunSuffix + "-att"

	tokenText := "attfindtext" + suffix
	tokenPDF := "attfindpdf" + suffix
	tokenBody := "attfindbody" + suffix

	mb, inbox := seedSearchMailbox(t, svc, 9201, "att-search-"+suffix,
		// A: a plain-text attachment carrying the token.
		[]byte("Message-ID: <a-att@test>\r\nFrom: s@example.test\r\nTo: r@example.test\r\n"+
			"Subject: "+suffix+"-a\r\nMIME-Version: 1.0\r\n"+
			"Content-Type: multipart/mixed; boundary=\"B\"\r\n\r\n"+
			"--B\r\nContent-Type: text/plain\r\n\r\nbody only\r\n"+
			"--B\r\nContent-Type: text/plain; name=\"a.txt\"\r\n"+
			"Content-Disposition: attachment; filename=\"a.txt\"\r\n\r\n"+
			tokenText+" invoice\r\n--B--\r\n"),
		// B: a body-only control.
		[]byte("Message-ID: <b-att@test>\r\nFrom: s@example.test\r\nTo: r@example.test\r\n"+
			"Subject: "+suffix+"-b\r\nMIME-Version: 1.0\r\n"+
			"Content-Type: text/plain\r\n\r\n"+tokenBody+" plain body\r\n"),
		// C: a PDF attachment whose extracted text carries the token.
		buildMIMEWithAttachments(suffix+"-c", "pdf body", mimeAttachment{
			Name: "statement.pdf", CT: "application/pdf",
			Body: buildMinimalPDF(t, tokenPDF),
		}),
	)
	scope := SearchScope{MailboxID: mb.ID, UserID: 9201}

	for _, tc := range []struct {
		name       string
		token      string
		wantHit    bool
		wantAttHit bool
	}{
		{"text attachment token", tokenText, true, true},
		{"pdf attachment token", tokenPDF, true, true},
		{"body-only token", tokenBody, true, false},
	} {
		hits, total, err := s.Search(context.Background(), scope, SearchQuery{Text: tc.token, Limit: 10})
		if err != nil {
			t.Fatalf("%s: search: %v", tc.name, err)
		}
		if total != 1 || len(hits) != 1 {
			t.Fatalf("%s: hits=%d total=%d, want exactly 1", tc.name, len(hits), total)
		}
		h := hits[0]
		if h.FolderID != inbox.ID {
			t.Fatalf("%s: hit folder = %d, want the INBOX", tc.name, h.FolderID)
		}
		if !tc.wantHit {
			t.Fatalf("%s: the case table is inconsistent", tc.name)
		}
		if h.AttachmentHit != tc.wantAttHit {
			t.Fatalf("%s: AttachmentHit = %v, want %v", tc.name, h.AttachmentHit, tc.wantAttHit)
		}
	}

	// A token that appears nowhere yields no hits.
	hits, total, err := s.Search(context.Background(), scope, SearchQuery{Text: "nowhere-token-" + suffix, Limit: 10})
	if err != nil || total != 0 || len(hits) != 0 {
		t.Fatalf("no-match search = %d/%d/%v, want empty", len(hits), total, err)
	}
}

// TestPGSearchScanOrderingPreservedWithAttachments — the scan-ordering
// contract (rank DESC, date DESC, id DESC) is unchanged when attachment text
// is present: three messages carrying the SAME token in their attachments
// (equal rank, equal date) come back in the stable id-DESC tiebreak order.
func TestPGSearchScanOrderingPreservedWithAttachments(t *testing.T) {
	svc, db := newPGService(t)
	s := &PostgresSearcher{db: db}
	suffix := pgRunSuffix + "-order"

	token := "orderatt" + suffix
	var raws [][]byte
	for i := 0; i < 3; i++ {
		raws = append(raws, buildMIMEWithAttachments(suffix+"-same", "same body", mimeAttachment{
			Name: fmt.Sprintf("doc%d.pdf", i), CT: "application/pdf",
			Body: buildMinimalPDF(t, token+" payload "+fmt.Sprint(i)),
		}))
	}
	mb, _ := seedSearchMailbox(t, svc, 9202, "att-order-"+suffix, raws...)
	scope := SearchScope{MailboxID: mb.ID, UserID: 9202}

	hits, total, err := s.Search(context.Background(), scope, SearchQuery{Text: token, Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 3 || len(hits) != 3 {
		t.Fatalf("hits=%d total=%d, want 3", len(hits), total)
	}
	// The stable tiebreak: equal ranks and equal dates → ids strictly
	// descending.
	for i := 1; i < len(hits); i++ {
		if hits[i].MessageID >= hits[i-1].MessageID {
			t.Fatalf("ordering not preserved: ids = %v (want strictly descending)", hits)
		}
		if !hits[i].AttachmentHit {
			t.Fatalf("hit %d lost the attachment flag", hits[i].MessageID)
		}
	}
}
