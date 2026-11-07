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

package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
)

// Synchronous external filter consult (ADR-006 decision 3): the milter-analog
// without the milter protocol. The spam pipeline POSTs a filter-request body
// (message reference + metadata — not raw content) to the workspace's
// registered webhooks subscribed to the filter event, and awaits the first
// disposition synchronously, under a hard timeout. This reuses the same
// machinery as every other delivery — the HMAC signature header, the SSRF
// URL policy and the guarded transport — through d.post's building blocks;
// no bypass path exists.

// FilterReply is the JSON body a filter endpoint answers with. Disposition is
// one of deliver|quarantine|reject (empty/unparsable means "no opinion");
// Score is optional and informational.
type FilterReply struct {
	Disposition string   `json:"disposition"`
	Score       *float64 `json:"score,omitempty"`
}

// FilterReplyTimeout is the default synchronous-await window when the caller
// passes no timeout.
const FilterReplyTimeout = 5 * time.Second

// ConsultFilter POSTs body to every webhook subscribed to event within scope
// (sender-filtered like all dispatches) and returns the first reply with a
// recognized disposition. It is fully synchronous: bounded by timeout, one
// attempt per endpoint, no retries — a filter consult must not delay the
// pipeline the way a delivery retry loop would. Callers own the fail-open
// decision: an error here means "no external opinion", never "block".
func (d *Dispatcher) ConsultFilter(ctx context.Context, userID uint, workspaceID *uint, event string, body []byte, fromAddr string, timeout time.Duration) (*FilterReply, error) {
	if timeout <= 0 {
		timeout = FilterReplyTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	scope := repositories.ResourceScope{UserID: userID, WorkspaceID: workspaceID}
	webhooks, err := d.repo.FindByScopeAndEvent(scope, event)
	if err != nil {
		return nil, fmt.Errorf("filter consult: find webhooks: %w", err)
	}

	var lastErr error = fmt.Errorf("filter consult: no webhook subscribed to %q", event)
	for _, wh := range webhooks {
		if !matchesFilters(wh.Filters, fromAddr) {
			continue
		}
		reply, err := d.consultOne(ctx, wh, body)
		if err != nil {
			lastErr = err
			continue
		}
		return reply, nil
	}
	return nil, lastErr
}

// consultOne performs one signed POST and parses the reply. Reuses the
// dispatcher's URL policy (SSRF gate), the guarded client, and the HMAC
// signature header format the delivery path uses.
func (d *Dispatcher) consultOne(ctx context.Context, wh models.Webhook, body []byte) (*FilterReply, error) {
	if err := d.urlPolicy(wh.URL); err != nil {
		return nil, fmt.Errorf("filter consult: policy: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("filter consult: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Posta-Webhook/1.0")
	if wh.Secret != "" {
		// Same signature scheme as the delivery path (signPayload, X-Posta-Signature).
		req.Header.Set("X-Posta-Signature", "sha256="+signPayload(wh.Secret, body))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("filter consult: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("filter consult: HTTP %d", resp.StatusCode)
	}

	var reply FilterReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, fmt.Errorf("filter consult: decode reply: %w", err)
	}
	reply.Disposition = strings.ToLower(strings.TrimSpace(reply.Disposition))
	switch reply.Disposition {
	case "deliver", "quarantine", "reject":
		return &reply, nil
	default:
		return nil, fmt.Errorf("filter consult: unknown disposition %q", reply.Disposition)
	}
}
