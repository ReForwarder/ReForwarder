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
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

const (
	// webhookTimeout bounds one webhook node's HTTP round trip.
	webhookTimeout = 10 * time.Second
	// webhookBodyLimit is how much of the response body is captured for the
	// node-run output; everything past it is discarded.
	webhookBodyLimit = 2048
)

// WebhookClient performs the outbound HTTP call of a webhook node. It is
// SSRF-guarded: the target host is resolved up front and every address it
// resolves to must be a public unicast address, so an automation author cannot
// point a webhook at localhost, the cluster's internal network or cloud
// metadata endpoints. Only http and https are accepted; a non-default port must
// be written explicitly in the URL (the scheme defaults are 80/443).
type WebhookClient struct {
	client *http.Client
}

// NewWebhookClient returns a WebhookClient with the platform defaults
// (10s timeout).
func NewWebhookClient() *WebhookClient {
	return &WebhookClient{
		client: &http.Client{Timeout: webhookTimeout},
	}
}

// SetHTTPClient overrides the underlying HTTP client (tests use this to inject
// a transport; production code keeps the default).
func (w *WebhookClient) SetHTTPClient(c *http.Client) {
	if c != nil {
		w.client = c
	}
}

// Post sends one webhook request and reports the status code plus the (size
// capped) response body. A transport error or a failed SSRF check returns a
// non-nil error; a non-2xx status is NOT an error here — the caller decides how
// to treat it.
func (w *WebhookClient) Post(ctx context.Context, rawURL string, headers map[string]string, body []byte, secret string) (int, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return 0, "", fmt.Errorf("invalid webhook url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return 0, "", fmt.Errorf("webhook url scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return 0, "", fmt.Errorf("webhook url has no host")
	}
	// Port policy: an omitted port means the scheme default (80/443). A
	// non-default port must be written explicitly in the URL — there is no way
	// to reach one otherwise — so nothing further to enforce here; the address
	// checks below carry the SSRF protection.

	// SSRF guard: resolve once and validate every address. A URL that is
	// already an IP literal resolves to itself here, so it is covered too.
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil {
		return 0, "", fmt.Errorf("webhook host %q lookup failed: %w", parsed.Hostname(), err)
	}
	if len(addrs) == 0 {
		return 0, "", fmt.Errorf("webhook host %q did not resolve to any address", parsed.Hostname())
	}
	for _, addr := range addrs {
		if err := validateWebhookIP(addr.IP); err != nil {
			return 0, "", err
		}
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

// validateWebhookIP rejects every non-public address: loopback, RFC 1918 / RFC
// 4193 private ranges, link-local unicast and multicast, multicast generally,
// and the unspecified address. IPv4-mapped IPv6 addresses are unmapped first so
// the v4 checks apply to them.
func validateWebhookIP(ip net.IP) error {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok || !addr.IsValid() {
		return fmt.Errorf("webhook host resolved to an invalid address")
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() {
		return fmt.Errorf("webhook url host %s is not a public address", addr)
	}
	return nil
}
