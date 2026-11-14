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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The distributed-checksum check client (SF-42, the ADR-006 amendment): the
// message's normalized body digest is looked up against a configured HTTP
// endpoint reporting how many times the community has seen (and whitelisted)
// it — the Pyzor semantics over HTTP JSON. Config-gated (no URL = disabled),
// and strictly fail-open: every error abstains, because an unavailable
// source is not evidence of spam.

// CheckReply is the endpoint's response contract: spam reports vs whitelist
// reports for the digest.
type CheckReply struct {
	Count   int `json:"count"`
	WLCount int `json:"wl_count"`
}

// CheckClient looks up body digests against the configured endpoint.
type CheckClient struct {
	url     string
	client  *http.Client
	timeout time.Duration
}

// NewCheckClient builds the client. An empty url disables it (Enabled()
// false); timeout <= 0 falls back to 3s.
func NewCheckClient(url string, timeout time.Duration) *CheckClient {
	if strings.TrimSpace(url) == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &CheckClient{
		url:     url,
		client:  &http.Client{Timeout: timeout},
		timeout: timeout,
	}
}

// Enabled reports whether the client is configured.
func (c *CheckClient) Enabled() bool { return c != nil && c.url != "" }

// BodyDigest computes the distributed checksum: SHA-256 over the lowercased,
// whitespace-collapsed text body (HTML is not included — the digest must be
// stable across the cosmetic HTML churn that surrounds real spam bodies).
func BodyDigest(textBody string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(textBody)), " ")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// Check looks up the digest. Returns the reply, whether the digest counts as
// spam (count > wl_count), or an error for the caller's fail-open handling
// (the scorer abstains on any error).
func (c *CheckClient) Check(ctx context.Context, textBody string) (*CheckReply, bool, error) {
	if c == nil || !c.Enabled() {
		return nil, false, nil
	}
	digest := BodyDigest(textBody)
	body, err := json.Marshal(map[string]string{"digest": digest})
	if err != nil {
		return nil, false, fmt.Errorf("checksum: marshal digest: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("checksum: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("checksum: lookup failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("checksum: lookup status %d", resp.StatusCode)
	}
	var reply CheckReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, false, fmt.Errorf("checksum: decode reply: %w", err)
	}
	return &reply, reply.Count > reply.WLCount, nil
}

// scoreChecksum applies the distributed-checksum analyzer: a digest the
// community reports as spam more than whitelisted adds weight. Every error
// abstains (fail-open — an unavailable source is not evidence of spam).
func (s *RulesScorer) scoreChecksum(msg *Message, acc *ReasonAccumulator) float64 {
	if s.check == nil || !s.check.Enabled() {
		return 0
	}
	// The lookup carries its own client-side timeout; a background context
	// keeps the SpamFilter interface (which takes no ctx) intact.
	reply, spam, err := s.check.Check(context.Background(), msg.TextBody)
	if err != nil {
		return 0 // fail-open
	}
	if !spam {
		return 0
	}
	acc.Add(fmt.Sprintf("checksum_spam:%d/%d", reply.Count, reply.WLCount))
	return s.weights.ChecksumSpam
}
