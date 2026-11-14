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
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The external rule-feed loader (SF-42, the ADR-006 amendment): a config-
// gated HTTP feed of score rules, HMAC-SHA256-verified over the raw body,
// atomically swapped into the scorer's in-memory snapshot by the refresh
// cron. Fail-open by design: a fetch, signature, or parse failure keeps the
// PREVIOUS snapshot (stale rules continue, never an empty gate), and an
// empty snapshot scores nothing.

// FeedRule is one externally-supplied score rule.
type FeedRule struct {
	Field    string  `json:"field"`    // subject | from | text_body | html_body | header:<name>
	Operator string  `json:"operator"` // contains | equals | starts_with | ends_with
	Value    string  `json:"value"`
	Weight   float64 `json:"weight"`
	Tag      string  `json:"tag"` // the reason tag; prefixed feed_rule:<tag>
}

// feedDocument is the feed contract: HMAC-verified JSON over this shape.
type feedDocument struct {
	Rules []FeedRule `json:"rules"`
}

// RuleFeed holds the atomically-swapped rule snapshot.
type RuleFeed struct {
	url     string
	secret  string
	client  *http.Client
	mu      sync.RWMutex
	current []FeedRule
}

// NewRuleFeed builds the loader. An empty url disables it (Enabled() false).
func NewRuleFeed(url, secret string) *RuleFeed {
	return &RuleFeed{url: url, secret: secret, client: &http.Client{Timeout: 15 * time.Second}}
}

// Enabled reports whether the feed is configured.
func (f *RuleFeed) Enabled() bool { return f != nil && f.url != "" }

// Rules returns the current snapshot (nil-safe; possibly nil = none loaded).
func (f *RuleFeed) Rules() []FeedRule {
	if f == nil {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.current
}

// Refresh fetches, verifies, and swaps the snapshot. Errors are returned for
// the cron's logging — the snapshot is untouched on any failure (fail-open).
func (f *RuleFeed) Refresh(ctx context.Context) (int, error) {
	if f == nil || !f.Enabled() {
		return 0, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return 0, fmt.Errorf("rule feed: build request: %w", err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("rule feed: fetch failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("rule feed: fetch status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, fmt.Errorf("rule feed: read body: %w", err)
	}

	// HMAC-SHA256 over the raw body bytes — the signature the feed operator
	// computes with the shared secret. A mismatched or missing signature
	// rejects the document WITHOUT touching the snapshot.
	signature := strings.TrimPrefix(resp.Header.Get("X-Rule-Feed-Signature"), "sha256=")
	mac := hmac.New(sha256.New, []byte(f.secret))
	mac.Write(raw)
	if !hmac.Equal(mac.Sum(nil), mustHex(signature)) {
		return 0, fmt.Errorf("rule feed: signature mismatch")
	}

	var doc feedDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return 0, fmt.Errorf("rule feed: decode document: %w", err)
	}
	if len(doc.Rules) > maxFeedRules {
		return 0, fmt.Errorf("rule feed: %d rules exceed the cap %d", len(doc.Rules), maxFeedRules)
	}
	for i := range doc.Rules {
		if err := validateFeedRule(&doc.Rules[i]); err != nil {
			return 0, fmt.Errorf("rule feed: rule %d: %w", i, err)
		}
	}

	f.mu.Lock()
	f.current = doc.Rules
	f.mu.Unlock()
	return len(doc.Rules), nil
}

// maxFeedRules bounds one feed document (a hostile feed cannot balloon the
// scorer).
const maxFeedRules = 500

// validateFeedRule enforces the feed's field/operator whitelist and a sane
// weight bound — externally-supplied rules get the strictest validation in
// the scorer.
func validateFeedRule(r *FeedRule) error {
	field := strings.TrimSpace(r.Field)
	if field == "" {
		return fmt.Errorf("empty field")
	}
	if !(field == "subject" || field == "from" || field == "text_body" || field == "html_body" ||
		strings.HasPrefix(field, "header:")) {
		return fmt.Errorf("unknown field %q", field)
	}
	if strings.HasPrefix(field, "header:") && len(field) > 200 {
		return fmt.Errorf("header name too long")
	}
	switch r.Operator {
	case "contains", "equals", "starts_with", "ends_with":
	default:
		return fmt.Errorf("unknown operator %q", r.Operator)
	}
	if r.Value == "" {
		return fmt.Errorf("empty value")
	}
	if r.Weight < 0 || r.Weight > 10 {
		return fmt.Errorf("weight %v out of range [0,10]", r.Weight)
	}
	if strings.TrimSpace(r.Tag) == "" {
		return fmt.Errorf("empty tag")
	}
	return nil
}

// mustHex decodes a hex string; a malformed signature decodes to a mismatching
// slice (never a panic).
func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		return []byte("\x00invalid")
	}
	return b
}

// feedRuleSubject renders one rule's subject string for evaluation: the
// named message field (a header:<name> rule reads the header map, "" when
// absent).
func feedRuleSubject(msg *Message, field string) string {
	switch field {
	case "subject":
		return msg.Subject
	case "from":
		return msg.Sender
	case "text_body":
		return msg.TextBody
	case "html_body":
		return msg.HTMLBody
	}
	if name, ok := strings.CutPrefix(field, "header:"); ok {
		return headerValue(msg.Headers, name)
	}
	return ""
}

// feedRuleMatch evaluates one feed rule's operator (case-insensitive for
// contains; the others compare the coerced strings).
func feedRuleMatch(subject, operator, value string) bool {
	switch operator {
	case "contains":
		return strings.Contains(strings.ToLower(subject), strings.ToLower(value))
	case "equals":
		return strings.EqualFold(subject, value)
	case "starts_with":
		return strings.HasPrefix(strings.ToLower(subject), strings.ToLower(value))
	case "ends_with":
		return strings.HasSuffix(strings.ToLower(subject), strings.ToLower(value))
	}
	return false
}

// scoreRuleFeed applies the loaded feed rules (the analyzer group): every
// matching rule adds its own weight with the feed_rule:<tag> reason. The
// per-message contribution is capped at FeedRuleMax so a hostile feed cannot
// dominate the score.
func (s *RulesScorer) scoreRuleFeed(msg *Message, acc *ReasonAccumulator) float64 {
	rules := s.feed.Rules()
	if len(rules) == 0 {
		return 0
	}
	var score float64
	for i := range rules {
		r := &rules[i]
		subject := feedRuleSubject(msg, r.Field)
		if subject == "" && r.Field != "subject" && r.Field != "from" &&
			r.Field != "text_body" && r.Field != "html_body" && !strings.HasPrefix(r.Field, "header:") {
			continue
		}
		if feedRuleMatch(subject, r.Operator, r.Value) {
			acc.Add("feed_rule:" + strings.TrimSpace(r.Tag))
			score += r.Weight
			if score > s.weights.FeedRuleMax {
				score = s.weights.FeedRuleMax
				break
			}
		}
	}
	return score
}

// SeedRules installs an initial snapshot (tests; the cron is the production
// path).
func (f *RuleFeed) SeedRules(rules []FeedRule) {
	f.mu.Lock()
	f.current = rules
	f.mu.Unlock()
}
