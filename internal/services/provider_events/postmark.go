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
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// PostmarkAdapter adapts Postmark's webhook: a single JSON object per POST
// whose RecordType names the event kind.
//
// Signature scheme: Postmark has no built-in cryptographic signing — it
// authenticates webhooks out-of-band by appending a token to the URL or via
// HTTP Basic auth. Posta accepts the configured secret (POSTA_PROVIDER_
// POSTMARK_SECRET) from the X-Postmark-Webhook header, the X-Postmark-Token
// header, or the password of an HTTP Basic Authorization header, compared in
// constant time.
//
// Event mapping: Delivery → delivered; Bounce → bounced (Type HardBounce* →
// bounce:hard, SoftBounce* → bounce:soft); SpamComplaint → complained;
// Open/Click → opened/clicked; SubscriptionChange → skipped.
//
// Message resolution: Postmark echoes the sender-supplied Metadata map, so
// the adapter reads email_uuid / posta_email_uuid / X-Posta-ID from it;
// without metadata the Postmark MessageID does not name a Posta message and
// the event is skipped by the ingestion service.
type PostmarkAdapter struct{}

func (a *PostmarkAdapter) Name() string { return "postmark" }

func (a *PostmarkAdapter) VerifySignature(rawBody []byte, headers http.Header, config ProviderConfig) error {
	if config.Secret == "" {
		return errors.New("postmark: no webhook secret configured")
	}
	secret := config.Secret
	for _, header := range []string{"X-Postmark-Webhook", "X-Postmark-Token"} {
		if v := strings.TrimSpace(headers.Get(header)); v != "" {
			if subtle.ConstantTimeCompare([]byte(v), []byte(secret)) == 1 {
				return nil
			}
			return errors.New("postmark: webhook token mismatch")
		}
	}
	if auth := headers.Get("Authorization"); auth != "" {
		_, pass, ok := parseBasicAuth(auth)
		if !ok {
			return errors.New("postmark: invalid Authorization header")
		}
		if subtle.ConstantTimeCompare([]byte(pass), []byte(secret)) == 1 {
			return nil
		}
		return errors.New("postmark: webhook token mismatch")
	}
	return errors.New("postmark: no webhook token supplied (X-Postmark-Webhook header or Basic auth)")
}

// parseBasicAuth decodes an "Basic base64(user:pass)" header value.
func parseBasicAuth(header string) (user, pass string, ok bool) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Basic") {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
	if err != nil {
		return "", "", false
	}
	if i := strings.IndexByte(string(decoded), ':'); i >= 0 {
		return string(decoded[:i]), string(decoded[i+1:]), true
	}
	return "", "", false
}

type postmarkEvent struct {
	RecordType string            `json:"RecordType"`
	ID         int64             `json:"ID"` // Postmark's event id, used as the provider event id
	Email      string            `json:"Email"`
	MessageID  string            `json:"MessageID"`
	Metadata   map[string]string `json:"Metadata"`
	// Bounce / SpamComplaint
	Type        string `json:"Type"` // HardBounce, SoftBounce, …
	TypeCode    int    `json:"TypeCode"`
	Name        string `json:"Name"`
	Tag         string `json:"Tag"`
	BouncedAt   string `json:"BouncedAt"`
	Date        string `json:"Date"`
	Details     string `json:"Details"`
	Description string `json:"Description"`
	// Delivery / Open / Click
	Recipient    string `json:"Recipient"`
	DeliveredAt  string `json:"DeliveredAt"`
	OpenAt       string `json:"OpenAt"`
	ClickAt      string `json:"ClickAt"`
	OriginalLink string `json:"OriginalLink"`
	UserAgent    string `json:"UserAgent,omitempty"`
	// Open
	FirstOpen bool `json:"FirstOpen"`
}

func (a *PostmarkAdapter) ParseEvent(rawBody []byte) ([]NormalizedEvent, error) {
	var ev postmarkEvent
	if err := json.Unmarshal(rawBody, &ev); err != nil {
		return nil, fmt.Errorf("postmark: payload is not a JSON event object: %w", err)
	}

	e := NormalizedEvent{
		Provider:        a.Name(),
		EmailUUID:       postmarkUUID(ev.Metadata, ev.MessageID),
		Recipient:       firstNonEmptyStr(ev.Email, ev.Recipient),
		ProviderEventID: fmt.Sprintf("%d", ev.ID),
	}

	switch ev.RecordType {
	case "Delivery":
		e.EventType = EventDelivered
		e.Timestamp = parseRFC3339(firstNonEmptyStr(ev.DeliveredAt, ev.Date))
		e.Metadata = map[string]any{"postmark_message_id": ev.MessageID}
	case "Bounce":
		e.EventType = EventBounced
		e.Timestamp = parseRFC3339(firstNonEmptyStr(ev.BouncedAt, ev.Date))
		e.Diagnostic = postmarkBounceDiagnostic(ev.Type, ev.Details, ev.Description)
		e.Metadata = map[string]any{
			"bounce_type":         ev.Type,
			"bounce_type_code":    ev.TypeCode,
			"postmark_message_id": ev.MessageID,
		}
	case "SpamComplaint":
		e.EventType = EventComplained
		e.Timestamp = parseRFC3339(firstNonEmptyStr(ev.BouncedAt, ev.Date))
		e.Diagnostic = "spam complaint reported by Postmark"
		e.Metadata = map[string]any{
			"complaint_type":      ev.Type,
			"postmark_message_id": ev.MessageID,
		}
	case "Open":
		e.EventType = EventOpened
		e.Timestamp = parseRFC3339(firstNonEmptyStr(ev.OpenAt, ev.Date))
		e.Metadata = map[string]any{
			"user_agent":          ev.UserAgent,
			"first_open":          ev.FirstOpen,
			"postmark_message_id": ev.MessageID,
		}
	case "Click":
		e.EventType = EventClicked
		e.Timestamp = parseRFC3339(firstNonEmptyStr(ev.ClickAt, ev.Date))
		e.Metadata = map[string]any{
			"url":                 ev.OriginalLink,
			"user_agent":          ev.UserAgent,
			"postmark_message_id": ev.MessageID,
		}
	case "SubscriptionChange":
		// List-subscription preference change, not a delivery event; the
		// unsubscribe flow handles those.
		return nil, nil
	default:
		return nil, fmt.Errorf("postmark: unsupported RecordType %q", ev.RecordType)
	}

	if len(ev.Metadata) > 0 {
		for k, v := range ev.Metadata {
			if e.Metadata == nil {
				e.Metadata = map[string]any{}
			}
			e.Metadata["postmark_metadata_"+k] = v
		}
	}
	return []NormalizedEvent{e}, nil
}

// postmarkBounceDiagnostic prefixes the classifier marker carrying Postmark's
// own hard/soft judgement, then the bounce details.
func postmarkBounceDiagnostic(bounceType, details, description string) string {
	marker := ""
	switch {
	case strings.Contains(bounceType, "Hard"):
		marker = markerHard + "; "
	case strings.Contains(bounceType, "Soft"):
		marker = markerSoft + "; "
	}
	reason := firstNonEmptyStr(details, description)
	if reason == "" {
		reason = "bounce reported by Postmark (" + bounceType + ")"
	}
	return marker + reason
}

// postmarkUUID resolves the Posta message from the echoed Metadata map,
// falling back to the Postmark MessageID (which will simply not resolve when
// it is Postmark's own id).
func postmarkUUID(metadata map[string]string, messageID string) string {
	for _, key := range []string{"email_uuid", "posta_email_uuid", "X-Posta-ID"} {
		if v := strings.TrimSpace(metadata[key]); v != "" {
			return v
		}
	}
	return strings.TrimSpace(messageID)
}
