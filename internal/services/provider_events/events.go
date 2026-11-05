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

// Package provider_events ingests delivery-event webhooks from upstream email
// providers (R-04) and records them on the Posta message-history timeline,
// classifying bounce and failure causes on the way in (R-05).
//
// The pipeline has three layers:
//
//   - One adapter per provider implements ProviderAdapter: it verifies the
//     provider's webhook signature scheme and parses the provider payload into
//     []NormalizedEvent (events.go + one file per provider).
//   - The HTTP layer (internal/handlers/provider_event_handler.go) dispatches
//     on the {provider} path parameter, refuses unconfigured providers (501),
//     and hands the normalized events plus the raw payload to the ingestion
//     service.
//   - IngestionService (service.go) resolves the email UUID, delegates the
//     classification to the R-05 classifier, records timeline events through
//     emailhistory.Recorder and applies the bounce/suppression side effects
//     for permanent failures.
//
// Configuration choice (R-04, documented): per-provider signing secrets come
// from environment variables (POSTA_PROVIDER_SES_SECRET, …, see
// internal/config), not from the workspace_settings table. Provider event
// ingestion is an operator-level integration — the events concern the mail
// Posta itself relays — so one install-wide secret per provider is enough and
// signing secrets never need to be stored per workspace.
package provider_events

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Normalized event types, the provider-neutral vocabulary every adapter maps
// its provider-specific event names onto.
const (
	EventDelivered  = "delivered"
	EventBounced    = "bounced"
	EventDeferred   = "deferred"
	EventRejected   = "rejected"
	EventComplained = "complained"
	EventOpened     = "opened"
	EventClicked    = "clicked"
)

// isNormalizedEventType is the closed set ParseEvent implementations may
// produce; anything else would fall through the ingestion switch.
func isNormalizedEventType(t string) bool {
	switch t {
	case EventDelivered, EventBounced, EventDeferred, EventRejected,
		EventComplained, EventOpened, EventClicked:
		return true
	}
	return false
}

// NormalizedEvent is one provider-reported delivery event in provider-neutral
// shape. Adapters fill Provider..Metadata; the ingestion service fills
// Classification (R-05) and rewrites ProviderEventID into the dedup id.
type NormalizedEvent struct {
	// Provider is the adapter name ("ses", "sendgrid", "postmark", "mailgun").
	Provider string
	// EventType is one of the Event* constants above.
	EventType string
	// EmailUUID is the Posta message the event refers to, as best the adapter
	// can determine (echoed custom header, provider message id, metadata).
	// Empty, or a reference that does not resolve, makes the event skipped.
	EmailUUID string
	// Recipient is the recipient address the provider reported on.
	Recipient string
	// ProviderEventID is the provider's unique event identifier (SNS
	// MessageId, sg_event_id, Mailgun token, …). It is folded into the
	// dedup id by DedupEventID.
	ProviderEventID string
	// Timestamp is the provider-reported occurrence time; nil means unknown
	// and the ingestion service records the receipt time instead. Out-of-order
	// timestamps are stored as reported — the timeline is not re-ordered.
	Timestamp *time.Time
	// Classification is the R-05 classifier verdict, filled by the ingestion
	// service (empty in adapter output).
	Classification string
	// Diagnostic is the human-readable cause: the provider's reason string
	// and/or the bounced SMTP response. Adapters may prefix it with a
	// "bounce:permanent" / "bounce:transient" / "bounce:hard" / "bounce:soft"
	// marker the classifier recognizes.
	Diagnostic string
	// Metadata carries provider-specific detail (bounce types, enhanced codes,
	// user agents, …). Keys "smtp_code" and "enhanced_code", when present, feed
	// the classifier as structured input.
	Metadata map[string]any
}

// DedupEventID derives the stable provider event id the email_events unique
// index on (email_uuid, type, provider_event_id) dedups on:
// sha256(provider|event_type|provider_event_id).
//
// Events without a provider-assigned id dedup on the report's content
// (recipient|diagnostic) instead, so an identical replay collapses into the
// first row while a genuinely different report — other diagnostic, other
// recipient — still gets its own timeline row.
func DedupEventID(provider, eventType, providerEventID, recipient, diagnostic string) string {
	key := strings.TrimSpace(providerEventID)
	if key == "" {
		key = recipient + "|" + diagnostic
	}
	h := sha256.Sum256([]byte(provider + "|" + eventType + "|" + key))
	return hex.EncodeToString(h[:])
}
