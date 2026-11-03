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

package automation

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/goposta/posta/internal/services/webhook"
)

const (
	// webhookTimeout bounds one webhook node's HTTP round trip.
	webhookTimeout = 10 * time.Second
	// webhookBodyLimit is how much of the response body is captured for the
	// node-run output; everything past it is discarded.
	webhookBodyLimit = 2048
)

// WebhookClient performs the outbound HTTP call of a webhook node. It is
// SSRF-guarded through the shared webhook guard, in layers:
//
//   - the static URL policy (scheme, embedded credentials, port, local names,
//     IP literals) runs before the request is built and again inside the
//     pre-flight resolution;
//   - a pre-flight resolution requires every address the host resolves to be
//     public — defense in depth that also protects the SetHTTPClient seam,
//     where a test or embedder may have swapped in a plain transport;
//   - the guarded transport resolves, validates and dials a *validated IP
//     literal* on every connection, so the destination is pinned at dial time
//     and DNS rebinding between check and connect cannot slip through.
//
// Redirects are never followed (a 3xx becomes the final response, so a
// redirect cannot hop the policy to another host) and ambient proxies are
// never honored (the transport's Proxy is nil). Only http and https are
// accepted; a non-default port must be written explicitly in the URL (the
// scheme defaults are 80/443).
type WebhookClient struct {
	client *http.Client
	// resolver resolves webhook hostnames for the pre-flight; nil means
	// net.DefaultResolver inside webhook.ResolvePublicURL. Tests stub it so
	// resolution stays deterministic and off the network.
	resolver webhook.Resolver
	// urlPolicy is the static SSRF gate every webhook URL must pass before a
	// request is built. Production always uses webhook.ValidateWebhookURL;
	// tests that exercise delivery semantics over loopback httptest servers
	// relax it, because loopback destinations are blocked by policy — the
	// same seam the Dispatcher carries.
	urlPolicy func(string) error
}

// NewWebhookClient returns a WebhookClient with the platform defaults: a 10s
// timeout over the shared guard's hardened transport — redirects blocked, no
// ambient proxy, destination pinned at dial time.
func NewWebhookClient() *WebhookClient {
	return &WebhookClient{
		client: &http.Client{
			Timeout:   webhookTimeout,
			Transport: webhook.NewGuardedTransport(nil),
			// Redirects are not followed: a 3xx becomes the final response
			// (http.ErrUseLastResponse), so a redirect can never hop the
			// SSRF policy to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		urlPolicy: webhook.ValidateWebhookURL,
	}
}

// SetHTTPClient overrides the underlying HTTP client (tests use this to inject
// a transport; production code keeps the default). The static gate and the
// pre-flight resolution still run in front of it, so a swapped-in client never
// weakens the URL policy.
func (w *WebhookClient) SetHTTPClient(c *http.Client) {
	if c != nil {
		w.client = c
	}
}

// Post sends one webhook request and reports the status code plus the (size
// capped) response body. A policy rejection, a failed pre-flight resolution or
// a transport error returns a non-nil error; a non-2xx status is NOT an error
// here — the caller decides how to treat it. The guard's own errors are
// address-free by construction; whatever else comes back is sanitized at the
// engine's write boundary before it is stored.
func (w *WebhookClient) Post(ctx context.Context, rawURL string, headers map[string]string, body []byte, secret string) (int, string, error) {
	if err := w.urlPolicy(rawURL); err != nil {
		return 0, "", err
	}
	// Pre-flight defense in depth: resolve and require every address public.
	// The guarded transport re-resolves and re-validates on every connection;
	// this pass exists so a transport swapped in through SetHTTPClient is
	// still preceded by a full check.
	if _, err := webhook.ResolvePublicURL(ctx, w.resolver, rawURL); err != nil {
		return 0, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, "", fmt.Errorf("invalid webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// The node secret authenticates Posta to the receiver; it wins over any
	// user-supplied Authorization header.
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("webhook request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, webhookBodyLimit+1))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("webhook response read failed: %w", err)
	}
	bodyStr := string(raw)
	if len(bodyStr) > webhookBodyLimit {
		bodyStr = bodyStr[:webhookBodyLimit]
	}
	return resp.StatusCode, bodyStr, nil
}
