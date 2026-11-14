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
	"path/filepath"
	"strings"
)

// Additional scorer analyzers (SF-39, the ADR-006 amendment): four analyzer
// groups beyond the OB-2 baseline, riding the same architecture — each
// signal contributes weight + reasons, every unavailable signal abstains,
// and all defaults stay below the reject threshold (GTUBE remains the only
// full-score signal). The URL-count analyzer named in the same bounded spec
// already exists in the baseline (scoreURLs) and is NOT reimplemented here.
//
// The SF-39 slice: attachment-type, Received-chain shape, subject
// heuristics, HTML-ratio (this file) — bringing the scorer to 12 analyzer
// groups / 32 bounded reason families (see scorer.go for the count). The reference suite's remaining
// surface (redirect chasing, homographs, FTRL, LLM) stays on SF-39/40/41.

// Subject-heuristics constants: a subject needs enough letters to judge the
// all-caps shape, and the uppercase ratio threshold.
const (
	subjectAllCapsMinLetters = 12
	subjectAllCapsRatio      = 0.6
)

// spamSubjectKeywords is the bounded keyword list for the subject-heuristics
// analyzer (classic mass-mail markers; case-insensitive word-boundary
// matches). Deliberately short: the analyzer adds pressure, it does not
// decide — the disposition thresholds stay the score's job.
var spamSubjectKeywords = []string{
	"free", "winner", "winning", "lottery", "urgent", "congratulations",
	"viagra", "casino", "crypto", "investment opportunity", "act now",
	"limited time", "risk-free", "double your",
}

// executableExtensions is the attachment-type analyzer's executable-shape
// extension set (lowercase, with the leading dot).
var executableExtensions = map[string]bool{
	".exe": true, ".scr": true, ".bat": true, ".cmd": true, ".com": true,
	".pif": true, ".vbs": true, ".vbe": true, ".js": true, ".jse": true,
	".wsf": true, ".wsh": true, ".ps1": true, ".jar": true, ".msi": true,
	".hta": true, ".cpl": true, ".gadget": true,
}

// executableContentTypes is the MIME-type side of the executable-shape check.
var executableContentTypes = map[string]bool{
	"application/x-msdownload":      true,
	"application/x-ms-installer":    true,
	"application/x-dosexec":         true,
	"application/x-executable":      true,
	"application/x-sh":              true,
	"application/x-java-archive":    true,
	"application/java-archive":      true,
	"application/vnd.ms-htmlhelp":   true,
	"application/x-msi":             true,
	"application/x-bat":             true,
	"text/x-script":                 true,
	"application/x-msdos-program":   true,
	"application/x-winexe":          true,
}

// scoreAttachments applies the attachment-type analyzer: filenames with
// double extensions (`.pdf.exe`-style cloaking) and executable shapes (the
// extension or the declared MIME type) add weight. Zero attachments abstain.
// It complements SF-42's exact-match blocklists, which hard-block on sight;
// this analyzer only pressures the score.
func (s *RulesScorer) scoreAttachments(msg *Message, acc *ReasonAccumulator) float64 {
	w := s.weights
	var score float64
	for i := range msg.Attachments {
		att := &msg.Attachments[i]
		name := strings.ToLower(strings.TrimSpace(att.Filename))
		ext := strings.ToLower(filepath.Ext(name))
		stem := strings.TrimSuffix(name, ext)

		// Double extension: the stem itself ends with a known document/media
		// extension — invoice.pdf.exe-style cloaking.
		if ext != "" && executableExtensions[ext] && filepath.Ext(stem) != "" {
			score += w.AttachmentDoubleExtension
			acc.Add("attachment_double_extension")
		}
		if executableExtensions[ext] || executableContentTypes[strings.ToLower(strings.TrimSpace(att.ContentType))] {
			score += w.AttachmentExecutable
			acc.Add("attachment_executable")
		}
	}
	if score > w.AttachmentMax {
		score = w.AttachmentMax
	}
	return score
}

// scoreHeaderOrder applies the Received-chain shape analyzer (the header-
// order slice): the parsed header map carries no ordering, so the analyzer
// works on the chain's PRESENCE — SMTP-path mail (a connecting IP was
// recorded) with no Received header at all was injected without a real MTA
// hop. The webhook path (no ClientIP) legitimately lacks Received and
// abstains; hop counts beyond presence are unknowable from the durable
// record and are deliberately not invented (the parsed header map dedupes
// headers, so only presence is knowable).
func (s *RulesScorer) scoreHeaderOrder(msg *Message, acc *ReasonAccumulator) float64 {
	w := s.weights
	if msg.ClientIP == "" {
		return 0 // webhook ingest: no Received is the normal shape
	}
	if countHeaders(msg.Headers, "Received") == 0 {
		acc.Add("missing_received_header")
		return w.ReceivedMissing
	}
	return 0
}

// scoreSubject applies the subject heuristics: all-caps subjects, excessive
// terminal punctuation, and bounded spam-keyword hits (the keyword
// contribution capped so a keyword-stuffed subject cannot dominate).
func (s *RulesScorer) scoreSubject(msg *Message, acc *ReasonAccumulator) float64 {
	w := s.weights
	subject := strings.TrimSpace(msg.Subject)
	if subject == "" {
		return 0
	}
	var score float64

	// All-caps: enough letters to judge, overwhelmingly uppercase.
	letters := 0
	upper := 0
	for _, r := range subject {
		if r >= 'A' && r <= 'Z' {
			letters++
			upper++
		} else if r >= 'a' && r <= 'z' {
			letters++
		}
	}
	if letters >= subjectAllCapsMinLetters && float64(upper)/float64(letters) >= subjectAllCapsRatio {
		score += w.SubjectAllCaps
		acc.Add("subject_all_caps")
	}

	// Excessive terminal punctuation: !!!/???-style runs at the end.
	trimmed := strings.TrimRight(subject, " \t")
	if strings.HasSuffix(trimmed, "!!!") || strings.HasSuffix(trimmed, "???") ||
		strings.HasSuffix(trimmed, "!!!?") || strings.HasSuffix(trimmed, "???!") {
		score += w.SubjectExcessivePunctuation
		acc.Add("subject_excessive_punctuation")
	}

	// Bounded keyword hits, word-boundary matched, case-insensitive; the
	// keyword contribution is capped so a keyword-stuffed subject cannot
	// dominate.
	kw := 0
	lower := " " + strings.ToLower(subject) + " "
	for _, k := range spamSubjectKeywords {
		if strings.Contains(lower, " "+k+" ") || strings.Contains(lower, " "+k+",") ||
			strings.Contains(lower, " "+k+"!") || strings.Contains(lower, " "+k+".") {
			kw++
			acc.Add("subject_spam_keyword:" + k)
		}
	}
	if kw == 1 {
		score += w.SubjectSpamKeyword
	} else if kw > 1 {
		score += w.SubjectSpamKeywordMax
	}
	return score
}

// scoreHTMLRatio applies the HTML-shape analyzer: an HTML-only body (no text
// part at all) or an HTML-dominated body (the ratio ≥ threshold with a
// minimum size so small signatures abstain) adds weight — the classic
// image-only spam shape.
func (s *RulesScorer) scoreHTMLRatio(msg *Message, acc *ReasonAccumulator) float64 {
	w := s.weights
	htmlLen := len(msg.HTMLBody)
	if htmlLen == 0 {
		return 0
	}
	if strings.TrimSpace(msg.TextBody) == "" && htmlLen >= w.HTMLRatioMinBytes {
		acc.Add("html_text_missing")
		return w.HTMLTextMissing
	}
	total := htmlLen + len(msg.TextBody)
	if total > 0 && float64(htmlLen)/float64(total) >= w.HTMLRatioThreshold && htmlLen >= w.HTMLRatioMinBytes {
		acc.Add("html_ratio_high")
		return w.HTMLRatioHigh
	}
	return 0
}

// countHeaders counts how many of the canonical header name (case variants)
// are present. The parser normalizes to the Go-canonical spelling; the
// counted Received chain uses the common spellings — presence only (the
// parsed header map dedupes by name; hop counts are not invented).
func countHeaders(headers map[string]string, name string) int {
	for k := range headers {
		if strings.EqualFold(k, name) {
			return 1
		}
	}
	return 0
}
