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

package provider_events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SendGridAdapter adapts SendGrid's Event Webhook: an RFC-style JSON *array*
// of event objects posted to the endpoint.
//
// Signature scheme (documented deviation): SendGrid's official Signed Event
// Webhook verifies an ECDSA signature whose public key travels inside the
// payload. Posta implements the simpler HMAC-SHA256 scheme instead —
// X-SendGrid-Signature carries base64(HMAC-SHA256(verificationKey,
// X-SendGrid-Signature-Timestamp + "." + rawBody)) — because the verification
// key is provisioned alongside the endpoint (POSTA_PROVIDER_SENDGRID_KEY), so
// the key-distribution problem the embedded-public-key design solves does not
// exist here. The ECDSA public-key path is intentionally unimplemented.
//
// Event mapping: delivered → delivered; bounce → bounced; blocked → rejected
// (policy, classifier lands on reputation); dropped → bounced with the
// bounce:permanent marker (SendGrid drops what it considers undeliverable);
// deferred → deferred; open/click → opened/clicked; spamreport → complained;
// processed → skipped (relay acceptance already recorded as "sent").
type SendGridAdapter struct{}

func (a *SendGridAdapter) Name() string { return "sendgrid" }

// sendGridSignatureMaxAge bounds signature freshness so a captured request
// cannot be replayed forever.
const sendGridSignatureMaxAge = 10 * time.Minute

func (a *SendGridAdapter) VerifySignature(rawBody []byte, headers http.Header, config ProviderConfig) error {
	if config.Secret == "" {
		return errors.New("sendgrid: no verification key configured")
	}
	sig := strings.TrimSpace(headers.Get("X-Sendgrid-Signature"))
	ts := strings.TrimSpace(headers.Get("X-Sendgrid-Signature-Timestamp"))
	// net/textproto canonicalizes names case-insensitively; the Get above
	// already matches any casing the provider used.
	if sig == "" || ts == "" {
		return errors.New("sendgrid: missing X-SendGrid-Signature / X-SendGrid-Signature-Timestamp headers")
	}
	if n, err := strconv.ParseInt(ts, 10, 64); err != nil {
		return fmt.Errorf("sendgrid: invalid signature timestamp: %w", err)
	} else if age := time.Since(time.Unix(n, 0)); age > sendGridSignatureMaxAge || age < -sendGridSignatureMaxAge {
		return fmt.Errorf("sendgrid: signature timestamp outside the accepted window (%v)", age)
	}
	mac := hmac.New(sha256.New, []byte(config.Secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(rawBody)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return errors.New("sendgrid: signature mismatch")
	}
	return nil
}

type sendGridEvent struct {
	Email       string `json:"email"`
	Timestamp   int64  `json:"timestamp"` // unix seconds
	Event       string `json:"event"`
	SgMessageID string `json:"sg_message_id"`
	SgEventID   string `json:"sg_event_id"`
	Type        string `json:"type"`   // bounced|blocked on bounce events
	Reason      string `json:"reason"` // bounce/block/deferred cause
	Status      string `json:"status"` // SMTP reply code as a string, e.g. "550"
	URL         string `json:"url"`
	IP          string `json:"ip"`
	UserAgent   string `json:"useragent"`
}

func (a *SendGridAdapter) ParseEvent(rawBody []byte) ([]NormalizedEvent, error) {
	// Custom args are flattened into each event object under their own keys,
	// so a second pass over a generic map recovers the stamped message UUID
	// when the sender configured custom_args.
	var rawEvents []map[string]any
	if err := json.Unmarshal(rawBody, &rawEvents); err != nil {
		return nil, fmt.Errorf("sendgrid: payload is not a JSON event array: %w", err)
	}

	out := make([]NormalizedEvent, 0, len(rawEvents))
	for _, raw := range rawEvents {
		var ev sendGridEvent
		if err := json.Unmarshal(mustJSON(raw), &ev); err != nil {
			continue
		}
		if ev.Event == "" {
			continue
		}
		switch ev.Event {
		case "processed":
			// Relay acceptance: already recorded as "sent".
			continue
		case "delivered", "bounce", string(ClassBlocked), "dropped", "deferred",
			"open", "click", "spamreport":
			// handled below
		default:
			continue
		}

		e := NormalizedEvent{
			Provider:        a.Name(),
			EmailUUID:       sendGridUUID(ev.SgMessageID, raw),
			Recipient:       ev.Email,
			ProviderEventID: ev.SgEventID,
		}
		if ev.Timestamp > 0 {
			t := time.Unix(ev.Timestamp, 0)
			e.Timestamp = &t
		}

		metadata := map[string]any{}
		if ip, ok := raw["ip"].(string); ok {
			metadata["ip"] = ip
		}
		switch ev.Event {
		case "delivered":
			e.EventType = EventDelivered
		case "bounce":
			e.EventType = EventBounced
			// SendGrid bounce events carry the SMTP reply: feed it to the
			// classifier as structured input when parseable.
			if code, err := strconv.Atoi(strings.TrimSpace(ev.Status)); err == nil {
				metadata["smtp_code"] = code
			}
			if strings.TrimSpace(ev.Reason) != "" {
				e.Diagnostic = ev.Reason
			} else {
				e.Diagnostic = "bounce reported by SendGrid"
			}
			if ev.Type != "" {
				metadata["bounce_type"] = ev.Type
			}
		case string(ClassBlocked):
			e.EventType = EventRejected
			e.Diagnostic = firstNonEmptyStr(ev.Reason, "message blocked by the receiving server")
			metadata["block_reason"] = ev.Reason
		case "dropped":
			e.EventType = EventBounced
			// SendGrid drops what its validation considers undeliverable
			// (invalid address, unsubscribed, spam-filtered content); the
			// marker tells the classifier this is permanent.
			e.Diagnostic = markerPermanent + "; " + firstNonEmptyStr(ev.Reason, "message dropped by SendGrid")
		case "deferred":
			e.EventType = EventDeferred
			if code, err := strconv.Atoi(strings.TrimSpace(ev.Status)); err == nil {
				metadata["smtp_code"] = code
			}
			e.Diagnostic = firstNonEmptyStr(ev.Reason, "delivery deferred by the receiving server")
		case "open":
			e.EventType = EventOpened
			metadata["ip"] = ev.IP
			metadata["user_agent"] = ev.UserAgent
		case "click":
			e.EventType = EventClicked
			metadata["ip"] = ev.IP
			metadata["user_agent"] = ev.UserAgent
			metadata["url"] = ev.URL
		case "spamreport":
			e.EventType = EventComplained
			e.Diagnostic = "spam complaint reported by SendGrid"
		}
		if len(metadata) > 0 {
			e.Metadata = metadata
		}
		out = append(out, e)
	}
	return out, nil
}

// sendGridUUID extracts the Posta message UUID for an event: SendGrid echoes
// custom_args under their own keys, and truncates the outbound Message-ID
// header (X-Posta-ID's sibling, the RFC Message-ID carrying the UUID) into
// sg_message_id as "<uuid@domain>.<filter>" — both are recognized.
func sendGridUUID(sgMessageID string, raw map[string]any) string {
	for _, key := range []string{"email_uuid", "posta_email_uuid", "X-Posta-ID"} {
		if v, ok := raw[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	// sg_message_id: "<uuid@domain>" possibly followed by ".<filter>".
	id := strings.Trim(sgMessageID, "<>")
	if i := strings.IndexByte(id, '.'); i >= 0 {
		id = id[:i]
	}
	if i := strings.IndexByte(id, '@'); i > 0 {
		return id[:i]
	}
	return ""
}

// mustJSON marshals a decoded map back to bytes. Errors are impossible for
// values json.Unmarshal produced, so the result is returned directly.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
