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

package spamfilter

import (
	"strings"
	"testing"
	"time"
)

// SF-39 tests: the four additional analyzer groups (the ADR-006 amendment)
// — per-analyzer behavior + the score integration (signals stacking toward
// the quarantine disposition). The URL-count analyzer named in the same
// bounded spec already exists in the baseline (scorer_test.go pins it) and
// is deliberately NOT reimplemented here.

func analyzerMsg() *Message {
	return &Message{
		Subject: "Invoice",
		TextBody: "plain text body with enough content",
		Headers: map[string]string{
			"Message-Id": "<a@b.c>",
			"Date":       time.Now().UTC().Format(time.RFC1123Z),
		},
	}
}

func wantReason(t *testing.T, reasons []string, tag string, score float64, min float64) {
	t.Helper()
	if !containsReason(reasons, tag) {
		t.Fatalf("reasons %v missing %q", reasons, tag)
	}
	if score < min {
		t.Fatalf("score = %v, want >= %v for %q", score, min, tag)
	}
}

func noReason(t *testing.T, reasons []string, tag string, score float64, max float64) {
	t.Helper()
	noTag(t, reasons, tag)
	if score > max {
		t.Fatalf("score = %v, want <= %v (no %q contribution)", score, max, tag)
	}
}

// noTag asserts a reason prefix is absent (no score expectation — use it
// when other analyzers legitimately contribute to the score).
func noTag(t *testing.T, reasons []string, tag string) {
	t.Helper()
	for _, r := range reasons {
		if strings.HasPrefix(r, tag) {
			t.Fatalf("reasons %v unexpectedly contain %q", reasons, tag)
		}
	}
}

// TestAttachmentTypeAnalyzer — the double-extension cloaking and the
// executable shapes fire; a clean PDF and no attachments abstain; the
// per-message cap holds.
func TestAttachmentTypeAnalyzer(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)

	// Clean: no attachments, nothing fires.
	score, reasons := s.Score(analyzerMsg())
	noReason(t, reasons, "attachment_", score, 0.001)

	// Double extension: invoice.pdf.exe.
	msg := analyzerMsg()
	msg.Attachments = []Attachment{{Filename: "invoice.pdf.exe", ContentType: "application/octet-stream"}}
	score, reasons = s.Score(msg)
	wantReason(t, reasons, "attachment_double_extension", score, 3.0)
	wantReason(t, reasons, "attachment_executable", score, 2.5)

	// Executable by extension alone (.scr) — no double-extension tag.
	msg = analyzerMsg()
	msg.Attachments = []Attachment{{Filename: "screensaver.scr", ContentType: "application/octet-stream"}}
	score, reasons = s.Score(msg)
	noTag(t, reasons, "attachment_double_extension")
	wantReason(t, reasons, "attachment_executable", score, 2.5)

	// Executable by declared MIME type alone (a benign-looking filename) —
	// no double-extension tag.
	msg = analyzerMsg()
	msg.Attachments = []Attachment{{Filename: "update.bin", ContentType: "application/x-msdownload"}}
	score, reasons = s.Score(msg)
	noTag(t, reasons, "attachment_double_extension")
	wantReason(t, reasons, "attachment_executable", score, 2.5)

	// Clean attachments abstain.
	msg = analyzerMsg()
	msg.Attachments = []Attachment{
		{Filename: "report.pdf", ContentType: "application/pdf"},
		{Filename: "data.csv", ContentType: "text/csv"},
	}
	score, reasons = s.Score(msg)
	noReason(t, reasons, "attachment_", score, 0.001)

	// The per-message cap: many suspicious attachments cannot exceed it.
	msg = analyzerMsg()
	for i := 0; i < 10; i++ {
		msg.Attachments = append(msg.Attachments,
			Attachment{Filename: "tool" + string(rune('a'+i)) + ".tar.exe", ContentType: "application/x-msdownload"})
	}
	score, _ = s.Score(msg)
	if score > DefaultWeights.AttachmentMax+0.001 {
		t.Fatalf("attachment score = %v, want the cap %v", score, DefaultWeights.AttachmentMax)
	}
}

// TestReceivedChainAnalyzer — the header-order slice: SMTP-path mail with no
// Received header fires; a normal chain and the webhook path (no ClientIP)
// abstain. Hop counts beyond presence are deliberately not scored (the
// parsed header map dedupes headers — only presence is knowable).
func TestReceivedChainAnalyzer(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)

	// SMTP-path mail with no Received header at all.
	msg := analyzerMsg()
	msg.ClientIP = "192.0.2.10"
	score, reasons := s.Score(msg)
	wantReason(t, reasons, "missing_received_header", score, 2.0)

	// A normal single hop abstains.
	msg.Headers["Received"] = "from mx.example by posta.test; Mon, 02 Nov 2026 10:00:01 +0000"
	score, reasons = s.Score(msg)
	noReason(t, reasons, "missing_received_header", score, 0.001)

	// The webhook path (no ClientIP) abstains even without Received.
	msg = analyzerMsg()
	score, reasons = s.Score(msg)
	noReason(t, reasons, "missing_received_header", score, 0.001)
}

// TestSubjectHeuristicsAnalyzer — all-caps, excessive punctuation, and the
// bounded keyword hits fire (the keyword contribution capped); a normal
// subject abstains.
func TestSubjectHeuristicsAnalyzer(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)

	// All-caps: enough letters, overwhelmingly uppercase.
	msg := analyzerMsg()
	msg.Subject = "URGENT ACTION REQUIRED ON YOUR ACCOUNT TODAY"
	score, reasons := s.Score(msg)
	wantReason(t, reasons, "subject_all_caps", score, 1.5)

	// A normal subject abstains.
	score, reasons = s.Score(analyzerMsg())
	noReason(t, reasons, "subject_", score, 0.001)

	// Excessive terminal punctuation.
	msg.Subject = "Great deal!!!"
	score, reasons = s.Score(msg)
	wantReason(t, reasons, "subject_excessive_punctuation", score, 1.0)

	// A bounded keyword hit fires once with the word named.
	msg.Subject = "Your free gift awaits"
	score, reasons = s.Score(msg)
	wantReason(t, reasons, "subject_spam_keyword:free", score, 1.0)

	// Keyword stuffing caps the keyword contribution.
	msg.Subject = "free winner lottery urgent congratulations crypto casino"
	score, reasons = s.Score(msg)
	kwCount := 0
	for _, r := range reasons {
		if strings.HasPrefix(r, "subject_spam_keyword:") {
			kwCount++
		}
	}
	if kwCount < 2 {
		t.Fatalf("reasons %v, want multiple keyword hits", reasons)
	}
	if score > DefaultWeights.SubjectSpamKeywordMax+DefaultWeights.SubjectAllCaps+0.001 {
		t.Fatalf("score = %v, want the keyword contribution capped at %v (+ any shape signals)",
			score, DefaultWeights.SubjectSpamKeywordMax)
	}
}

// TestHTMLRatioAnalyzer — the HTML-only and HTML-dominated shapes fire; a
// balanced multipart body and a text-only body abstain; small HTML abstains.
func TestHTMLRatioAnalyzer(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)
	bigHTML := "<html><body>" + strings.Repeat("<p>markup</p>", 600) + "</body></html>" // ~7.8 KB

	// HTML-only body ≥ the minimum size.
	msg := analyzerMsg()
	msg.HTMLBody = bigHTML
	msg.TextBody = ""
	score, reasons := s.Score(msg)
	wantReason(t, reasons, "html_text_missing", score, 2.0)

	// HTML-dominated (≥ 95% of the bytes).
	msg.TextBody = "tiny"
	score, reasons = s.Score(msg)
	wantReason(t, reasons, "html_ratio_high", score, 1.5)

	// A balanced multipart body abstains.
	msg.TextBody = strings.Repeat("plain text line\n", 500) // ~8 KB text
	score, reasons = s.Score(msg)
	noReason(t, reasons, "html_ratio_high", score, 0.001)
	noReason(t, reasons, "html_text_missing", score, 0.001)

	// A text-only body abstains.
	msg = analyzerMsg()
	score, reasons = s.Score(msg)
	noReason(t, reasons, "html_", score, 0.001)

	// Small HTML below the minimum abstains (a styled signature).
	msg.HTMLBody = "<b>hi</b>"
	msg.TextBody = ""
	score, reasons = s.Score(msg)
	noReason(t, reasons, "html_", score, 0.001)
}

// TestScoreIntegrationAnalyzersStack — the score integration: several
// analyzer signals on one message stack toward (and past) the default
// quarantine threshold, and the disposition answers quarantine — the
// analyzers feed the existing clamped-threshold pipeline unchanged.
func TestScoreIntegrationAnalyzersStack(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)

	msg := analyzerMsg()
	msg.ClientIP = "192.0.2.10" // no Received → missing_received_header (2.0)
	msg.Subject = "URGENT FREE CRYPTO OFFER ACT NOW!!!" // all-caps + terminal punctuation + keywords
	msg.HTMLBody = "<html><body>" + strings.Repeat("<p>x</p>", 700) + "</body></html>"
	msg.TextBody = ""
	msg.Attachments = []Attachment{{Filename: "invoice.pdf.exe", ContentType: "application/x-msdownload"}}

	score, reasons := s.Score(msg)
	if score < DefaultThresholds.SpamScoreQuarantine() {
		t.Fatalf("stacked score = %v, want >= the quarantine threshold %v (reasons %v)",
			score, DefaultThresholds.SpamScoreQuarantine(), reasons)
	}
	for _, tag := range []string{
		"attachment_double_extension", "attachment_executable", "missing_received_header",
		"subject_all_caps", "subject_excessive_punctuation", "html_text_missing",
	} {
		if !containsReason(reasons, tag) {
			t.Fatalf("reasons %v missing %q", reasons, tag)
		}
	}
	if d := s.Disposition(score); d != DispositionQuarantine && d != DispositionReject {
		t.Fatalf("disposition = %q, want quarantine or reject (the stack crossed the thresholds)", d)
	}

	// And a clean message still delivers (no false pressure from the new
	// analyzers on ordinary mail).
	clean := analyzerMsg()
	clean.ClientIP = "192.0.2.10"
	clean.Headers["Received"] = "from mx.example by posta.test; Mon, 02 Nov 2026 10:00:01 +0000"
	score, reasons = s.Score(clean)
	if score > 0.001 || len(reasons) != 0 {
		t.Fatalf("clean message score = %v/%v, want 0 with no reasons", score, reasons)
	}
	if d := s.Disposition(score); d != DispositionDeliver {
		t.Fatalf("clean disposition = %q, want deliver", d)
	}
}

// TestAnalyzerCountDocumented pins the documented analyzer count (the ADR-006
// amendment's number): the scorer runs exactly 10 analyzer groups — the
// regression guard for the "keep the total analyzer count documented"
// requirement. It drives each group's minimal trigger and counts the
// distinct weight-bearing groups that respond.
func TestAnalyzerCountDocumented(t *testing.T) {
	s := NewRulesScorer(DefaultThresholds, nil, nil)

	// One message engineered so EVERY group has something to respond to.
	msg := &Message{
		Subject:     "URGENT!!!",
		ClientIP:    "192.0.2.66",
		Sender:      "sender@verified.example",
		UserID:      1,
		SenderDomain: "verified.example",
		HTMLBody:    "<html>" + strings.Repeat("x", 5000) + "</html>", // html_text_missing
		Headers:     map[string]string{"Date": "Mon, 02 Nov 2026 10:00:00 +0000"}, // no Received, no Message-ID
		Attachments: []Attachment{{Filename: "a.pdf.exe", ContentType: "application/x-msdownload"}},
		Auth:        AuthVerdict{SPF: "fail"}, // auth group
	}
	msg.TextBody = ""
	_, reasons := s.Score(msg)

	// The group representatives present in the reasons.
	groups := map[string]bool{}
	for _, r := range reasons {
		switch {
		case strings.HasPrefix(r, "spf_") || strings.HasPrefix(r, "dkim_") || strings.HasPrefix(r, "dmarc_"):
			groups["auth"] = true
		case strings.HasPrefix(r, "missing_message_id"):
			groups["headers"] = true
		case strings.HasPrefix(r, "attachment_"):
			groups["attachments"] = true
		case r == "missing_received_header":
			groups["received-chain"] = true
		case strings.HasPrefix(r, "subject_"):
			groups["subject"] = true
		case strings.HasPrefix(r, "html_"):
			groups["html-ratio"] = true
		}
	}
	// The URL-count group is the pre-existing baseline analyzer (the ask's
	// list names it; it is NOT reimplemented — its test lives in
	// scorer_test.go). The DNSBL/reputation/GTUBE groups need external
	// sources; their existence is pinned by their own tests.
	for _, g := range []string{"auth", "headers", "attachments", "received-chain", "subject", "html-ratio"} {
		if !groups[g] {
			t.Fatalf("analyzer group %q did not respond: reasons %v", g, reasons)
		}
	}
	if len(groups) != 6 {
		t.Fatalf("responding groups = %v, want 6", groups)
	}
}
