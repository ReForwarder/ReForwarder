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

// Package spamfilter implements ADR-006 (Option B Phase 2): the SpamFilter
// interface plus the rules-based baseline scorer, the ClamAV (clamd) malware
// client, the GTUBE self-test marker and the sender-reputation signal.
//
// The package is deliberately dependency-light (models + gorm only): it is a
// scoring library, not a pipeline. The ingest worker (internal/worker)
// orchestrates scan → score → disposition, and the inbound service applies
// the disposition to the durable record — always AFTER the message is
// persisted (the ADR-002 non-negotiable). ML/AI classifiers (SF-40/41/113)
// are intentionally absent at Phase 2 (ADR-006 decision 4 — Phase 6 item,
// adopted additively behind this interface later).
package spamfilter

import "time"

// Disposition is what the filter pipeline does with a message (ADR-006 §1):
// deliver, quarantine (hold for review) or reject. Reject is a POST-INGEST
// disposition in this codebase — the message is already durably stored — and
// never a silent drop: every non-deliver disposition writes an event.
type Disposition string

const (
	DispositionDeliver    Disposition = "deliver"
	DispositionQuarantine Disposition = "quarantine"
	DispositionReject     Disposition = "reject"
)

// Valid reports whether d is one of the three known dispositions.
func Valid(d Disposition) bool {
	switch d {
	case DispositionDeliver, DispositionQuarantine, DispositionReject:
		return true
	}
	return false
}

// SpamFilter is the ADR-006 interface every scoring backend implements —
// the rules baseline today, ML/LLM classifiers at Phase 6 behind the same
// seam. Score returns the spam score and the human-readable reasons that
// contributed to it; Disposition maps a score onto the disposition pipeline
// using the admin-configurable thresholds.
type SpamFilter interface {
	Score(msg *Message) (float64, []string)
	Disposition(score float64) Disposition
}

// ThresholdSource supplies the admin-configurable score thresholds
// (ADR-006: "Score → disposition thresholds are admin-configurable").
// *settings.Provider satisfies it once its SpamScoreQuarantine/SpamScoreReject
// accessors exist; tests use StaticThresholds.
type ThresholdSource interface {
	SpamScoreQuarantine() float64
	SpamScoreReject() float64
}

// StaticThresholds is a fixed ThresholdSource: the programmatic default
// (quarantine ≥ 5.0, reject ≥ 10.0) used when no settings provider is wired.
type StaticThresholds struct {
	Quarantine float64
	Reject     float64
}

func (s StaticThresholds) SpamScoreQuarantine() float64 { return s.Quarantine }
func (s StaticThresholds) SpamScoreReject() float64     { return s.Reject }

// DefaultThresholds matches the seeded platform settings keys
// spam_score_quarantine / spam_score_reject.
var DefaultThresholds = StaticThresholds{Quarantine: 5.0, Reject: 10.0}

// AuthVerdict is the message-level SPF/DKIM/DMARC slice the scorer consumes.
// It mirrors the persisted verdict row (models.InboundAuthResult, the P3-1
// verification engines' output) without coupling the package to gorm.
// Values follow RFC 8601 result vocabularies (pass|fail|softfail|neutral|
// none|temperror|permerror); "none"/"" mean no result.
type AuthVerdict struct {
	SPF              string
	DKIM             string
	DMARC            string
	DMARCDisposition string // author-domain policy recommendation: none|quarantine|reject
}

// Attachment is one message attachment offered to the malware scanner.
type Attachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// Message is the scoring input. It is assembled by the ingest worker from the
// durable inbound record (never from client input), so scoring cannot be
// biased by unverified fields.
type Message struct {
	// InboundID/UUID identify the durable record (for logs and hook payloads);
	// UserID is the record's owner — the scope for the reputation lookup.
	InboundID uint
	UUID      string
	UserID    uint

	Sender     string
	Recipients []string
	Subject    string
	TextBody   string
	HTMLBody   string
	Headers    map[string]string

	Size       int64
	ReceivedAt time.Time

	// ClientIP is the connecting peer's address, taken from the persisted
	// authentication verdict (the SMTP path's only durable record of it).
	// Empty (webhook ingest) skips the DNSBL signal.
	ClientIP string

	// Auth carries the P3-1 verification verdicts; zero value (no row yet)
	// scores as "none" — no penalty, no bonus.
	Auth AuthVerdict

	// SenderDomain is the reputation lookup key (the sender address's
	// domain, lowercased). Empty skips the reputation signal.
	SenderDomain string
}

// ReasonAccumulator builds the reason list with dedup, preserving order.
type ReasonAccumulator struct {
	seen map[string]bool
	list []string
}

// Add appends a reason unless it is empty or already present.
func (a *ReasonAccumulator) Add(reason string) {
	if reason == "" {
		return
	}
	if a.seen == nil {
		a.seen = map[string]bool{}
	}
	if a.seen[reason] {
		return
	}
	a.seen[reason] = true
	a.list = append(a.list, reason)
}

// List returns the accumulated reasons (nil when empty).
func (a *ReasonAccumulator) List() []string {
	if len(a.list) == 0 {
		return nil
	}
	return a.list
}
