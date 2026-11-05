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
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SESAdapter adapts Amazon SES notifications delivered through SNS.
//
// Signature scheme: SNS SignatureVersion 1 — an HMAC-SHA1 over the canonical
// field list ("Message\n…\nMessageId\n…\nTimestamp\n…\nTopicArn\n…\nType\n"),
// base64-encoded in the Signature field. Posta verifies it with the operator-
// configured topic secret (POSTA_PROVIDER_SES_SECRET); SignatureVersion 2
// (RSA signatures verified against a downloaded certificate) is deliberately
// not implemented — the HMAC scheme keeps the trust anchor local. Requests
// carrying SignatureVersion 2 are rejected rather than trusted.
//
// Event mapping: Delivery → delivered; Bounce → bounced (bounceType Permanent
// → bounce:permanent marker, Transient → bounce:transient); Complaint →
// complained; Open/Click → opened/clicked; Send is skipped (Posta already
// recorded relay acceptance as "sent" when it handed the message upstream).
//
// Message resolution: SES does not echo Posta's message identity, so the
// adapter looks for the stamped X-Posta-ID header in mail.headers (present
// when the SES configuration set includes headers); without it the event
// cannot be attached to a message and the ingestion service skips it.
type SESAdapter struct {
	// confirmSubscribe, when set, replaces the default HTTP GET used to
	// confirm SNS subscriptions (test seam).
	confirmSubscribe func(url string) error
}

type snsEnvelope struct {
	Type             string `json:"Type"`
	MessageId        string `json:"MessageId"`
	TopicArn         string `json:"TopicArn"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	SubscribeURL     string `json:"SubscribeURL"`
	Token            string `json:"Token"`
	Subject          string `json:"Subject"`
	Message          string `json:"Message"`
}

type sesEvent struct {
	EventType string `json:"eventType"`
	Mail      struct {
		MessageId   string   `json:"messageId"`
		Destination []string `json:"destination"`
		Timestamp   string   `json:"timestamp"`
		Headers     []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
	} `json:"mail"`
	Bounce *struct {
		BounceType        string `json:"bounceType"`
		BounceSubType     string `json:"bounceSubType"`
		BouncedRecipients []struct {
			EmailAddress   string `json:"emailAddress"`
			DiagnosticCode string `json:"diagnosticCode"`
		} `json:"bouncedRecipients"`
		Timestamp  string `json:"timestamp"`
		FeedbackId string `json:"feedbackId"`
	} `json:"bounce"`
	Delivery *struct {
		Timestamp  string   `json:"timestamp"`
		Recipients []string `json:"recipients"`
	} `json:"delivery"`
	Complaint *struct {
		ComplaintSubType     string `json:"complaintSubType"`
		ComplainedRecipients []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"complainedRecipients"`
		Timestamp  string `json:"timestamp"`
		FeedbackId string `json:"feedbackId"`
	} `json:"complaint"`
	Open *struct {
		Timestamp string `json:"timestamp"`
		IPAddress string `json:"ipAddress"`
		UserAgent string `json:"userAgent"`
	} `json:"open"`
	Click *struct {
		Timestamp string `json:"timestamp"`
		IPAddress string `json:"ipAddress"`
		UserAgent string `json:"userAgent"`
		Link      string `json:"link"`
	} `json:"click"`
}

func (a *SESAdapter) Name() string { return "ses" }

// snsSignatureBaseString builds the canonical string SNS SignatureVersion 1
// signs, per the AWS SNS developer guide (field order fixed, each field
// followed by a newline).
func snsSignatureBaseString(env *snsEnvelope) string {
	var b strings.Builder
	b.WriteString("Message\n")
	b.WriteString(env.Message)
	b.WriteString("\nMessageId\n")
	b.WriteString(env.MessageId)
	b.WriteString("\n")
	switch env.Type {
	case "SubscriptionConfirmation", "UnsubscribeConfirmation":
		b.WriteString("SubscribeURL\n")
		b.WriteString(env.SubscribeURL)
		b.WriteString("\nTimestamp\n")
		b.WriteString(env.Timestamp)
		b.WriteString("\nToken\n")
		b.WriteString(env.Token)
		b.WriteString("\nTopicArn\n")
		b.WriteString(env.TopicArn)
		b.WriteString("\nType\n")
		b.WriteString(env.Type)
		b.WriteString("\n")
	default:
		if env.Subject != "" {
			b.WriteString("Subject\n")
			b.WriteString(env.Subject)
			b.WriteString("\n")
		}
		b.WriteString("Timestamp\n")
		b.WriteString(env.Timestamp)
		b.WriteString("\nTopicArn\n")
		b.WriteString(env.TopicArn)
		b.WriteString("\nType\n")
		b.WriteString(env.Type)
		b.WriteString("\n")
	}
	return b.String()
}

func (a *SESAdapter) VerifySignature(rawBody []byte, _ http.Header, config ProviderConfig) error {
	if config.Secret == "" {
		return errors.New("ses: no SNS signing secret configured")
	}
	var env snsEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		return fmt.Errorf("ses: invalid SNS envelope: %w", err)
	}
	if env.SignatureVersion != "1" {
		// SignatureVersion 2 is RSA over a downloaded certificate — outside
		// the local-trust design. Refuse rather than silently trust.
		return fmt.Errorf("ses: unsupported SNS SignatureVersion %q", env.SignatureVersion)
	}
	if env.Signature == "" {
		return errors.New("ses: missing SNS Signature")
	}
	mac := hmac.New(sha1.New, []byte(config.Secret))
	mac.Write([]byte(snsSignatureBaseString(&env)))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(env.Signature)) {
		return errors.New("ses: SNS signature mismatch")
	}
	return nil
}

func (a *SESAdapter) confirm(url string) error {
	if a.confirmSubscribe != nil {
		return a.confirmSubscribe(url)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ses: subscription confirmation returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *SESAdapter) ParseEvent(rawBody []byte) ([]NormalizedEvent, error) {
	var env snsEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		return nil, fmt.Errorf("ses: invalid SNS envelope: %w", err)
	}

	switch env.Type {
	case "SubscriptionConfirmation", "UnsubscribeConfirmation":
		// SNS requires the endpoint to fetch the SubscribeURL to complete the
		// handshake; no delivery event is carried in this notification.
		if env.SubscribeURL == "" {
			return nil, errors.New("ses: SNS confirmation without SubscribeURL")
		}
		if err := a.confirm(env.SubscribeURL); err != nil {
			return nil, fmt.Errorf("ses: subscription confirmation failed: %w", err)
		}
		return nil, nil
	case "Notification":
		// fall through
	default:
		return nil, fmt.Errorf("ses: unsupported SNS notification type %q", env.Type)
	}

	var ev sesEvent
	if err := json.Unmarshal([]byte(env.Message), &ev); err != nil {
		return nil, fmt.Errorf("ses: invalid SES event message: %w", err)
	}

	providerEventID := env.MessageId
	if providerEventID == "" {
		providerEventID = ev.Mail.MessageId
	}

	var out []NormalizedEvent
	base := NormalizedEvent{
		Provider:        a.Name(),
		EmailUUID:       sesPostaUUID(ev.Mail.Headers),
		ProviderEventID: providerEventID,
	}

	switch ev.EventType {
	case "Delivery":
		e := base
		e.EventType = EventDelivered
		e.Recipient = sesFirstRecipient(ev.Delivery.Recipients, ev.Mail.Destination)
		e.Timestamp = parseRFC3339(firstNonEmptyStr(ev.Delivery.Timestamp, ev.Mail.Timestamp))
		e.Metadata = map[string]any{"ses_message_id": ev.Mail.MessageId}
		out = append(out, e)
	case "Bounce":
		if ev.Bounce == nil {
			return nil, errors.New("ses: Bounce event without bounce object")
		}
		e := base
		e.EventType = EventBounced
		if n := len(ev.Bounce.BouncedRecipients); n > 0 {
			e.Recipient = ev.Bounce.BouncedRecipients[0].EmailAddress
		}
		e.Timestamp = parseRFC3339(firstNonEmptyStr(ev.Bounce.Timestamp, ev.Mail.Timestamp))
		marker := markerPermanent
		switch strings.ToLower(ev.Bounce.BounceType) {
		case "permanent":
			marker = markerPermanent
		case "transient":
			marker = markerTransient
		}
		var parts []string
		if n := len(ev.Bounce.BouncedRecipients); n > 0 {
			if code := strings.TrimSpace(ev.Bounce.BouncedRecipients[0].DiagnosticCode); code != "" {
				parts = append(parts, code)
			}
		}
		if len(parts) == 0 {
			parts = append(parts, "bounce reported by SES ("+ev.Bounce.BounceType+")")
		}
		e.Diagnostic = marker + "; " + strings.Join(parts, " — ")
		e.Metadata = map[string]any{
			"bounce_type":     strings.ToLower(ev.Bounce.BounceType),
			"bounce_sub_type": ev.Bounce.BounceSubType,
			"ses_message_id":  ev.Mail.MessageId,
		}
		out = append(out, e)
	case "Complaint":
		if ev.Complaint == nil {
			return nil, errors.New("ses: Complaint event without complaint object")
		}
		e := base
		e.EventType = EventComplained
		if n := len(ev.Complaint.ComplainedRecipients); n > 0 {
			e.Recipient = ev.Complaint.ComplainedRecipients[0].EmailAddress
		}
		e.Timestamp = parseRFC3339(firstNonEmptyStr(ev.Complaint.Timestamp, ev.Mail.Timestamp))
		e.Diagnostic = "spam complaint reported by SES"
		e.Metadata = map[string]any{
			"complaint_sub_type": ev.Complaint.ComplaintSubType,
			"ses_message_id":     ev.Mail.MessageId,
		}
		out = append(out, e)
	case "Open":
		if ev.Open == nil {
			return nil, errors.New("ses: Open event without open object")
		}
		e := base
		e.EventType = EventOpened
		e.Timestamp = parseRFC3339(firstNonEmptyStr(ev.Open.Timestamp, ev.Mail.Timestamp))
		e.Metadata = map[string]any{
			"ip":             ev.Open.IPAddress,
			"user_agent":     ev.Open.UserAgent,
			"ses_message_id": ev.Mail.MessageId,
		}
		out = append(out, e)
	case "Click":
		if ev.Click == nil {
			return nil, errors.New("ses: Click event without click object")
		}
		e := base
		e.EventType = EventClicked
		e.Timestamp = parseRFC3339(firstNonEmptyStr(ev.Click.Timestamp, ev.Mail.Timestamp))
		e.Metadata = map[string]any{
			"ip":             ev.Click.IPAddress,
			"user_agent":     ev.Click.UserAgent,
			"url":            ev.Click.Link,
			"ses_message_id": ev.Mail.MessageId,
		}
		out = append(out, e)
	case "Send":
		// Relay acceptance: Posta recorded "sent" when it handed the message
		// upstream; nothing new to record.
		return nil, nil
	default:
		return nil, fmt.Errorf("ses: unsupported SES eventType %q", ev.EventType)
	}
	return out, nil
}

// sesPostaUUID extracts the stamped X-Posta-ID header (the Posta message
// UUID) from the SES headers block, if the configuration set delivered it.
func sesPostaUUID(headers []struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, "X-Posta-ID") {
			return strings.TrimSpace(h.Value)
		}
	}
	return ""
}

// sesFirstRecipient picks the first explicit recipient, falling back to the
// envelope destination.
func sesFirstRecipient(explicit, destination []string) string {
	if n := len(explicit); n > 0 && explicit[0] != "" {
		return explicit[0]
	}
	if n := len(destination); n > 0 {
		return destination[0]
	}
	return ""
}

// parseRFC3339 parses a provider timestamp, returning nil when absent or
// unparsable. Out-of-order timestamps are carried as reported.
func parseRFC3339(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &t
}

// firstNonEmptyStr returns the first non-empty argument.
func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
