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

package spamfilter

import (
	"context"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// Weights holds the baseline scorer's per-signal contributions. Defaults are
// deliberately conservative — Phase 2 is a baseline, not a verdict machine:
// no single signal except GTUBE can reach the default reject threshold (10),
// and reaching quarantine (5) takes either a DNSBL hit, GTUBE, or several
// weaker signals agreeing.
type Weights struct {
	// SPF/DKIM/DMARC (RFC 8601 verdicts from the persisted auth-results row).
	SPFFail      float64
	SPFSoftFail  float64
	SPFTempError float64
	SPFPermError float64
	DKIMFail     float64
	DKIMError    float64 // temperror | permerror
	DMARCFail    float64
	// DMARCFailedPolicy adds when the author domain's own policy recommended
	// an enforcement action AND evaluation failed (a stronger fail signal).
	DMARCFailedPolicy float64
	AuthTempError     float64 // temperror on any mechanism (transient DNS)

	// DNSBL: per listing zone, capped at DNSBLMax.
	DNSBLHit float64
	DNSBLMax float64
	// Header heuristics.
	MissingMessageID float64
	MissingDate      float64
	DateSkew         float64 // |now − Date| beyond the skew window

	// URL count over text+HTML bodies.
	URLHeavy             float64 // count > URLHeavyThreshold
	URLSpeak             float64 // count > URLModerateThreshold (below heavy)
	URLModerateThreshold int
	URLHeavyThreshold    int

	// GTUBE self-test marker (SF-42's testable slice).
	GTUBE float64

	// Sender reputation (email_events bounce/complaint rates).
	HighBounceRate        float64 // ≥ 20%
	ModerateBounceRate    float64 // ≥ 10%
	HighComplaintRate     float64 // ≥ 2%
	ModerateComplaintRate float64 // ≥ 0.5%
}

// DefaultWeights is the shipped baseline configuration (documented in
// ADR-006's tests; every value is below reject except GTUBE).
var DefaultWeights = Weights{
	SPFFail:               2.5,
	SPFSoftFail:           1.0,
	SPFTempError:          0.5,
	SPFPermError:          0.5,
	DKIMFail:              1.0,
	DKIMError:             0.5,
	DMARCFail:             2.5,
	DMARCFailedPolicy:     1.0,
	AuthTempError:         0.5,
	DNSBLHit:              4.0,
	DNSBLMax:              8.0,
	MissingMessageID:      2.0,
	MissingDate:           1.0,
	DateSkew:              1.5,
	URLModerateThreshold:  10,
	URLHeavyThreshold:     25,
	URLSpeak:              1.0,
	URLHeavy:              2.0,
	GTUBE:                 1000.0,
	HighBounceRate:        2.0,
	ModerateBounceRate:    1.0,
	HighComplaintRate:     2.0,
	ModerateComplaintRate: 1.0,
}

// dateSkewWindow is how far a message's Date header may sit from the receive
// time before the skew signal fires (mail in transit across time zones is
// normal; days are not).
const dateSkewWindow = 24 * time.Hour

// urlPattern counts http(s) links across the text and HTML bodies.
var urlPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']{4,}`)

// RulesScorer is the ADR-006 rules-based baseline SpamFilter. Signals:
// SPF/DKIM/DMARC verdicts (from the persisted auth-results row), config-gated
// allowlisted DNSBL zones for the connecting IP, header heuristics, URL
// counts, the GTUBE marker, and the sender's bounce/complaint reputation.
type RulesScorer struct {
	weights    Weights
	thresholds ThresholdSource
	dnsbl      *DNSBLChecker
	reputation ReputationSource
}

// NewRulesScorer assembles the baseline. nil dnsbl (or an empty-zone
// checker) and nil reputation simply switch those signals off.
func NewRulesScorer(thresholds ThresholdSource, dnsbl *DNSBLChecker, reputation ReputationSource) *RulesScorer {
	if thresholds == nil {
		thresholds = DefaultThresholds
	}
	return &RulesScorer{
		weights:    DefaultWeights,
		thresholds: thresholds,
		dnsbl:      dnsbl,
		reputation: reputation,
	}
}

// Score implements SpamFilter. It never returns an error: every signal that
// cannot be evaluated (no verdict row yet, reputation unavailable, DNSBL
// disabled) abstains rather than penalizes — an unavailable signal is not
// evidence of spam.
func (s *RulesScorer) Score(msg *Message) (float64, []string) {
	if s == nil || msg == nil {
		return 0, nil
	}
	var score float64
	acc := &ReasonAccumulator{}

	score += s.scoreAuth(msg, acc)
	score += s.scoreDNSBL(msg, acc)
	score += s.scoreHeaders(msg, acc)
	score += s.scoreURLs(msg, acc)
	score += s.scoreReputation(msg, acc)
	score += s.scoreGTUBE(msg, acc)

	if score < 0 {
		score = 0
	}
	return score, acc.List()
}

// Disposition implements SpamFilter: score ≥ reject → reject, ≥ quarantine
// → quarantine, else deliver. A misconfigured pair (reject below quarantine)
// is clamped so nothing above the quarantine threshold can ever deliver —
// the safe direction of the error.
func (s *RulesScorer) Disposition(score float64) Disposition {
	q, r := s.thresholds.SpamScoreQuarantine(), s.thresholds.SpamScoreReject()
	if r < q {
		r = q
	}
	switch {
	case score >= r:
		return DispositionReject
	case score >= q:
		return DispositionQuarantine
	default:
		return DispositionDeliver
	}
}

// scoreAuth applies the persisted SPF/DKIM/DMARC verdicts. "none"/"" abstain.
func (s *RulesScorer) scoreAuth(msg *Message, acc *ReasonAccumulator) float64 {
	var score float64
	w := s.weights
	auth := msg.Auth

	switch strings.ToLower(auth.SPF) {
	case "fail":
		score += w.SPFFail
		acc.Add("spf_fail")
	case "softfail":
		score += w.SPFSoftFail
		acc.Add("spf_softfail")
	case "temperror":
		score += w.SPFTempError
		acc.Add("spf_temperror")
	case "permerror":
		score += w.SPFPermError
		acc.Add("spf_permerror")
	}

	switch strings.ToLower(auth.DKIM) {
	case "fail":
		score += w.DKIMFail
		acc.Add("dkim_fail")
	case "temperror", "permerror":
		score += w.DKIMError
		acc.Add("dkim_" + strings.ToLower(auth.DKIM))
	}

	switch strings.ToLower(auth.DMARC) {
	case "fail":
		score += w.DMARCFail
		acc.Add("dmarc_fail")
		switch strings.ToLower(auth.DMARCDisposition) {
		case "quarantine", "reject":
			score += w.DMARCFailedPolicy
			acc.Add("dmarc_policy_enforced")
		}
	case "temperror", "permerror":
		score += w.AuthTempError
		acc.Add("dmarc_" + strings.ToLower(auth.DMARC))
	}
	return score
}

// scoreDNSBL queries the configured, allowlisted zones for the connecting IP.
// Every listing is recorded as a reason (observability); the score
// contribution is capped at DNSBLMax so one bad afternoon on many zones
// cannot outweigh everything else.
func (s *RulesScorer) scoreDNSBL(msg *Message, acc *ReasonAccumulator) float64 {
	if !s.dnsbl.Enabled() || msg.ClientIP == "" {
		return 0
	}
	hits := s.dnsbl.Check(context.Background(), msg.ClientIP)
	if len(hits) == 0 {
		return 0
	}
	for _, zone := range hits {
		acc.Add("dnsbl_hit:" + zone)
	}
	score := float64(len(hits)) * s.weights.DNSBLHit
	if score > s.weights.DNSBLMax {
		score = s.weights.DNSBLMax
	}
	return score
}

// scoreHeaders applies the header heuristics: missing Message-ID, missing or
// unparseable Date, and Date skew beyond the window. Both Go-canonicalized
// header spellings are honored (parser.go normalizes to "Message-Id").
func (s *RulesScorer) scoreHeaders(msg *Message, acc *ReasonAccumulator) float64 {
	var score float64
	w := s.weights

	messageID := headerValue(msg.Headers, "Message-Id", "Message-ID")
	if strings.TrimSpace(messageID) == "" {
		score += w.MissingMessageID
		acc.Add("missing_message_id")
	}

	dateHeader := headerValue(msg.Headers, "Date")
	if strings.TrimSpace(dateHeader) == "" {
		score += w.MissingDate
		acc.Add("missing_date_header")
	} else if when, err := mail.ParseDate(dateHeader); err != nil {
		score += w.MissingDate
		acc.Add("unparseable_date_header")
	} else {
		skew := time.Since(when)
		if skew < 0 {
			skew = -skew
		}
		if skew > dateSkewWindow {
			score += w.DateSkew
			sign := "+"
			if time.Since(when) < 0 {
				sign = "-"
			}
			acc.Add("date_skew:" + sign + skew.Truncate(time.Hour).String())
		}
	}
	return score
}

// scoreURLs counts http(s) links across the text and HTML bodies.
func (s *RulesScorer) scoreURLs(msg *Message, acc *ReasonAccumulator) float64 {
	count := len(urlPattern.FindAllString(msg.TextBody+"\n"+msg.HTMLBody, -1))
	switch {
	case count > s.weights.URLHeavyThreshold:
		acc.Add(fmt.Sprintf("url_count:%d", count))
		return s.weights.URLHeavy
	case count > s.weights.URLModerateThreshold:
		acc.Add(fmt.Sprintf("url_count:%d", count))
		return s.weights.URLSpeak
	default:
		return 0
	}
}

// scoreReputation applies the tenant's outbound bounce/complaint rates for
// the sender's domain. An unavailable source or a too-small sample abstains.
func (s *RulesScorer) scoreReputation(msg *Message, acc *ReasonAccumulator) float64 {
	if s.reputation == nil || msg.SenderDomain == "" || msg.UserID == 0 {
		return 0
	}
	rep, err := s.reputation.SenderReputation(context.Background(), msg.UserID, msg.SenderDomain)
	if err != nil {
		// Reputation is an enrichment, never a gate: a source outage abstains.
		return 0
	}
	var score float64
	if br := rep.BounceRate(); br >= 0 {
		switch {
		case br >= 0.20:
			score += s.weights.HighBounceRate
			acc.Add("sender_bounce_rate:high")
		case br >= 0.10:
			score += s.weights.ModerateBounceRate
			acc.Add("sender_bounce_rate:moderate")
		}
	}
	if cr := rep.ComplaintRate(); cr >= 0 {
		switch {
		case cr >= 0.02:
			score += s.weights.HighComplaintRate
			acc.Add("sender_complaint_rate:high")
		case cr >= 0.005:
			score += s.weights.ModerateComplaintRate
			acc.Add("sender_complaint_rate:moderate")
		}
	}
	return score
}

// scoreGTUBE fires on the standard self-test marker.
func (s *RulesScorer) scoreGTUBE(msg *Message, acc *ReasonAccumulator) float64 {
	if containsGTUBE(msg.TextBody, msg.HTMLBody) {
		acc.Add(gtubeReason)
		return s.weights.GTUBE
	}
	return 0
}

// headerValue returns the first present header among the given canonical
// names ("").
func headerValue(headers map[string]string, names ...string) string {
	for _, n := range names {
		if v, ok := headers[n]; ok {
			return v
		}
	}
	return ""
}
