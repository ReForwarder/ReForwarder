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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MailgunAdapter adapts Mailgun's webhook, which posts either
// application/x-www-form-urlencoded (legacy webhooks) or JSON (API v5 events
// with a nested signature object).
//
// Signature scheme (v1): HMAC-SHA256(signingKey, timestamp + token), hex-encoded
// in the signature field, plus a freshness check on the timestamp (Mailgun's
// documented anti-replay guidance). The signing key is
// POSTA_PROVIDER_MAILGUN_KEY.
//
// Event mapping: delivered → delivered; bounced/failed → bounced (severity
// permanent → bounce:permanent, temporary → bounce:transient); complained →
// complained; opened/clicked → opened/clicked; accepted/unsubscribed and
// friends are skipped (relay acceptance is Posta's own "sent"; unsubscribes
// are outside the normalized event vocabulary).
//
// Message resolution: Mailgun echoes the outbound Message-ID header Posta
// stamps with the message UUID, either as message-id or inside the
// message-headers array (X-Posta-ID).
type MailgunAdapter struct{}

func (a *MailgunAdapter) Name() string { return "mailgun" }

// mailgunSignatureMaxAge bounds timestamp freshness — Mailgun recommends
// rejecting stale webhooks as a replay defense; 15 minutes tolerates modest
// clock skew.
const mailgunSignatureMaxAge = 15 * time.Minute

// mailgunPayload is the uniform view over both encodings.
type mailgunPayload struct {
	Event     string
	Recipient string
	MessageID string // mailgun message-id (no angle brackets)
	Timestamp string
	Token     string
	Signature string
	Severity  string // permanent|temporary on bounced/failed
	Reason    string
	Code      string // SMTP reply code as string, e.g. "550"
	URL       string
	UserAgent string
	IP        string
	EventID   string
	Headers   [][2]string // message-headers array when present
}

func decodeMailgunPayload(rawBody []byte) (*mailgunPayload, error) {
	trimmed := strings.TrimSpace(string(rawBody))
	if strings.HasPrefix(trimmed, "{") {
		var root map[string]any
		if err := json.Unmarshal(rawBody, &root); err != nil {
			return nil, fmt.Errorf("mailgun: invalid JSON payload: %w", err)
		}
		p := &mailgunPayload{}
		p.Event = mailgunStr(root, "event")
		p.Recipient = mailgunStr(root, "recipient")
		p.MessageID = mailgunStr(root, "message-id")
		p.Timestamp = mailgunStr(root, "timestamp")
		p.Token = mailgunStr(root, "token")
		p.Signature = mailgunStr(root, "signature")
		p.Severity = mailgunStr(root, "severity")
		p.Reason = mailgunStr(root, "reason")
		p.URL = mailgunStr(root, "url")
		p.EventID = mailgunStr(root, "event-id")
		if sig, ok := root["signature"].(map[string]any); ok {
			// v5 signature object overrides the flat fields.
			p.Timestamp = mailgunStr(sig, "timestamp")
			p.Token = mailgunStr(sig, "token")
			p.Signature = mailgunStr(sig, "signature")
		}
		if ds, ok := root["delivery-status"].(map[string]any); ok {
			p.Code = mailgunStr(ds, "code")
			if p.Reason == "" {
				p.Reason = mailgunStr(ds, "message")
			}
		}
		if ci, ok := root["client-info"].(map[string]any); ok {
			p.UserAgent = mailgunStr(ci, "user-agent")
		}
		if envelope, ok := root["envelope"].(map[string]any); ok {
			if p.Recipient == "" {
				p.Recipient = mailgunStr(envelope, "sender")
			}
		}
		if hs, ok := root["message-headers"].([]any); ok {
			for _, pair := range hs {
				if arr, ok := pair.([]any); ok && len(arr) == 2 {
					name, _ := arr[0].(string)
					value, _ := arr[1].(string)
					p.Headers = append(p.Headers, [2]string{name, value})
				}
			}
		}
		return p, nil
	}

	// Legacy form-encoded webhook.
	values, err := url.ParseQuery(trimmed)
	if err != nil {
		return nil, fmt.Errorf("mailgun: payload is neither JSON nor a form: %w", err)
	}
	get := func(key string) string {
		return strings.TrimSpace(values.Get(key))
	}
	p := &mailgunPayload{
		Event:     get("event"),
		Recipient: get("recipient"),
		MessageID: get("message-id"),
		Timestamp: get("timestamp"),
		Token:     get("token"),
		Signature: get("signature"),
		Severity:  get("severity"),
		Reason:    get("reason"),
		Code:      get("code"),
		URL:       get("url"),
		UserAgent: get("user-agent"),
		IP:        get("ip"),
		EventID:   get("event-id"),
	}
	if p.Reason == "" {
		p.Reason = get("description")
	}
	if headers, ok := values["message-headers"]; ok && len(headers) > 0 {
		var pairs [][2]string
		if err := json.Unmarshal([]byte(headers[0]), &pairs); err == nil {
			p.Headers = pairs
		}
	}
	return p, nil
}

// mailgunStr reads a nested JSON field as a string, regardless of the numeric
// encoding Mailgun chose for it.
func mailgunStr(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func (a *MailgunAdapter) VerifySignature(rawBody []byte, _ http.Header, config ProviderConfig) error {
	if config.Secret == "" {
		return errors.New("mailgun: no signing key configured")
	}
	p, err := decodeMailgunPayload(rawBody)
	if err != nil {
		return err
	}
	if p.Signature == "" || p.Token == "" || p.Timestamp == "" {
		return errors.New("mailgun: missing signature fields (timestamp, token, signature)")
	}
	ts, err := strconv.ParseInt(p.Timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("mailgun: invalid signature timestamp: %w", err)
	}
	if age := time.Since(time.Unix(ts, 0)); age > mailgunSignatureMaxAge || age < -mailgunSignatureMaxAge {
		return fmt.Errorf("mailgun: webhook timestamp outside the accepted window (%v)", age)
	}
	mac := hmac.New(sha256.New, []byte(config.Secret))
	mac.Write([]byte(p.Timestamp))
	mac.Write([]byte(p.Token))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(p.Signature)) {
		return errors.New("mailgun: signature mismatch")
	}
	return nil
}

func (a *MailgunAdapter) ParseEvent(rawBody []byte) ([]NormalizedEvent, error) {
	p, err := decodeMailgunPayload(rawBody)
	if err != nil {
		return nil, err
	}

	e := NormalizedEvent{
		Provider:        a.Name(),
		EmailUUID:       mailgunUUID(p),
		Recipient:       p.Recipient,
		ProviderEventID: firstNonEmptyStr(p.EventID, p.Token),
	}
	if ts, err := strconv.ParseInt(p.Timestamp, 10, 64); err == nil && ts > 0 {
		t := time.Unix(ts, 0)
		e.Timestamp = &t
	}

	switch strings.ToLower(p.Event) {
	case string(ClassDelivered):
		e.EventType = EventDelivered
		e.Metadata = map[string]any{"mailgun_message_id": p.MessageID}
	case "bounced", "failed":
		e.EventType = EventBounced
		marker := ""
		switch strings.ToLower(p.Severity) {
		case "permanent":
			marker = markerPermanent + "; "
		case "temporary":
			marker = markerTransient + "; "
		}
		reason := firstNonEmptyStr(p.Reason, "bounce reported by Mailgun")
		e.Diagnostic = marker + reason
		metadata := map[string]any{"mailgun_message_id": p.MessageID, "severity": p.Severity}
		if code, err := strconv.Atoi(strings.TrimSpace(p.Code)); err == nil {
			metadata["smtp_code"] = code
		}
		e.Metadata = metadata
	case "complained":
		e.EventType = EventComplained
		e.Diagnostic = "spam complaint reported by Mailgun"
		e.Metadata = map[string]any{"mailgun_message_id": p.MessageID}
	case "opened":
		e.EventType = EventOpened
		e.Metadata = map[string]any{"user_agent": p.UserAgent, "ip": p.IP}
	case "clicked":
		e.EventType = EventClicked
		e.Metadata = map[string]any{"url": p.URL, "user_agent": p.UserAgent, "ip": p.IP}
	default:
		// accepted, unsubscribed, storage charges, temporary failures of the
		// polling API, … — outside the normalized vocabulary.
		return nil, nil
	}
	return []NormalizedEvent{e}, nil
}

// mailgunUUID resolves the Posta message: X-Posta-ID inside the echoed
// message-headers array, else the local part of the mailgun message-id
// (Posta stamps Message-ID as <uuid@domain>).
func mailgunUUID(p *mailgunPayload) string {
	for _, pair := range p.Headers {
		if strings.EqualFold(pair[0], "X-Posta-ID") && strings.TrimSpace(pair[1]) != "" {
			return strings.TrimSpace(pair[1])
		}
	}
	id := strings.Trim(p.MessageID, "<>")
	if i := strings.IndexByte(id, '@'); i > 0 {
		return id[:i]
	}
	return strings.TrimSpace(id)
}
