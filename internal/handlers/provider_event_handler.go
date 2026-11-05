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

package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/goposta/posta/internal/services/provider_events"
	"github.com/jkaninda/okapi"
)

// ProviderEventIngestor consumes normalized provider events. The ingestion
// service implements it; the interface keeps the handler testable without a
// database.
type ProviderEventIngestor interface {
	Ingest(ctx context.Context, events []provider_events.NormalizedEvent, rawPayload string) (accepted, skipped int)
}

// maxProviderEventPayload bounds one webhook body: SES notifications, SendGrid
// batches and Mailgun reports are small JSON; anything above this is refused
// rather than buffered.
const maxProviderEventPayload = 2 << 20 // 2 MiB

// ProviderEventHandler serves POST /api/v1/webhooks/provider/{provider}
// (R-04). The {provider} path parameter selects the adapter; the route group
// authenticates with an API key (the same auth as the bounce webhook — the
// provider posts with an API key in the Authorization header), and the
// adapter's signature verification runs on top of that.
type ProviderEventHandler struct {
	ingestor ProviderEventIngestor
	secrets  map[string]provider_events.ProviderConfig
}

func NewProviderEventHandler(ingestor ProviderEventIngestor) *ProviderEventHandler {
	return &ProviderEventHandler{ingestor: ingestor}
}

// SetProviderSecrets wires the per-provider signing secrets from configuration.
// A provider without a configured secret answers 501 — the endpoint exists but
// the integration is not set up.
func (h *ProviderEventHandler) SetProviderSecrets(secrets map[string]provider_events.ProviderConfig) {
	h.secrets = secrets
}

// ProviderEventResponse is the 202 body of a processed webhook batch.
type ProviderEventResponse struct {
	Accepted int `json:"accepted" doc:"Events resolved to a known message and recorded (duplicates included)"`
	Skipped  int `json:"skipped" doc:"Events with no resolvable message reference or malformed"`
}

// Handle processes one provider webhook POST. Registered as a raw
// func(*okapi.Context) error — no request binding — so the body reaches the
// signature verification byte-exact (SendGrid signs the raw bytes; Mailgun
// signs timestamp+token pairs inside them).
func (h *ProviderEventHandler) Handle(c *okapi.Context) error {
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	adapter := provider_events.Adapter(provider)
	if adapter == nil {
		return c.AbortNotFound("unknown provider " + provider + " (supported: ses, sendgrid, postmark, mailgun)")
	}

	config, configured := h.secrets[provider]
	if !configured || config.Secret == "" {
		return c.AbortNotImplemented("provider webhook is not configured (set the provider secret environment variable)")
	}

	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, maxProviderEventPayload+1))
	if err != nil {
		return c.AbortBadRequest("failed to read the payload", err)
	}
	if len(raw) > maxProviderEventPayload {
		return c.AbortRequestEntityTooLarge("payload exceeds the 2 MiB limit")
	}

	if err := adapter.VerifySignature(raw, c.Request().Header, config); err != nil {
		return c.AbortUnauthorized("provider signature verification failed", err)
	}

	events, err := adapter.ParseEvent(raw)
	if err != nil {
		return c.AbortBadRequest("failed to parse the provider payload", err)
	}

	var accepted, skipped int
	if h.ingestor != nil {
		accepted, skipped = h.ingestor.Ingest(c.Request().Context(), events, string(raw))
	}
	return c.JSON(http.StatusAccepted, ProviderEventResponse{Accepted: accepted, Skipped: skipped})
}
