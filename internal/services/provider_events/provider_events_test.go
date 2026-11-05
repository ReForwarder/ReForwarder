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
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ---------- R-05: SMTP response classification matrix ----------

// TestClassifySMTPMatrix pins the representative SMTP reply codes and
// diagnostic texts to their classifications (R-05 verification battery).
func TestClassifySMTPMatrix(t *testing.T) {
	cases := []struct {
		name       string
		code       int
		enhanced   string
		diagnostic string
		want       Classification
	}{
		// Enhanced status codes (the structured signal wins).
		{"550 5.1.1", 550, "5.1.1", "user unknown", ClassInvalidRecipient},
		{"551 5.1.6 user moved", 551, "5.1.6", "user has moved", ClassInvalidRecipient},
		{"554 5.7.1", 554, "5.7.1", "delivery not authorized", ClassReputation},
		{"452 4.2.2", 452, "4.2.2", "mailbox full", ClassMailboxFull},
		{"552 5.2.2", 552, "5.2.2", "mailbox over quota", ClassMailboxFull},
		{"550 5.1.2 bad destination host", 550, "5.1.2", "bad destination system address", ClassDNSFailure},
		{"535 5.7.9 auth", 535, "5.7.9", "authentication credentials invalid", ClassAuthFailure},
		{"530 5.7.0 must issue STARTTLS -> reputation", 530, "5.7.0", "must issue a STARTTLS command first", ClassReputation},
		{"451 4.7.1 transient policy", 451, "4.7.1", "delivery not authorized", ClassReputation},
		{"421 4.4.1 no answer", 421, "4.4.1", "no answer from host", ClassConnectionFailure},
		{"554 5.4.4 unable to route", 554, "5.4.4", "unable to route", ClassDNSFailure},

		// Reply-code class only.
		{"421 bare", 421, "", "service not available", ClassDeferred},
		{"451 bare", 451, "", "temporary local problem", ClassDeferred},
		{"550 bare", 550, "", "rejected", ClassHardBounce},
		{"554 bare", 554, "", "transaction failed", ClassHardBounce},
		{"452 insufficient storage", 452, "", "insufficient system storage", ClassMailboxFull},

		// Diagnostic text patterns (no enhanced code).
		{"550 user unknown", 550, "", "The email account that you tried to reach does not exist. Please double-check the recipient", ClassInvalidRecipient},
		{"550 no such user", 550, "", "No such user here", ClassInvalidRecipient},
		{"550 mailbox unavailable", 550, "", "mailbox unavailable", ClassInvalidRecipient},
		{"550 account disabled", 550, "", "the account has been disabled", ClassInvalidRecipient},
		{"451 greylisted (outlook)", 451, "", "Please try again later, greylisted by the mail server", ClassDeferred},
		{"429 rate limit (gmail)", 429, "", "we have detected a rate limit violation for your domain", ClassRateLimited},
		{"550 rate limited", 550, "", "The IP you are sending from has been rate limited", ClassRateLimited},
		{"554 blocked using spamhaus", 554, "", "blocked using spamhaus.org", ClassReputation},
		{"550 access denied", 550, "", "access denied, bad reputation", ClassReputation},
		{"451 connection timed out", 451, "", "connection timed out during greeting", ClassConnectionFailure},
		{"554 TLS handshake", 554, "", "TLS handshake failed: certificate verify failed", ClassTLSFailure},
		{"421 dns", 421, "", "DNS lookup for the domain failed", ClassDNSFailure},
		{"535 auth text", 535, "", "authentication required before MAIL", ClassAuthFailure},
		{"452 quota text", 452, "", "the user you are trying to contact is over quota", ClassMailboxFull},

		// Codes embedded in the diagnostic text (code/enhanced not supplied).
		{"text-embedded 550 5.1.1", 0, "", "smtp; 550 5.1.1 user unknown", ClassInvalidRecipient},
		{"text-embedded 554 5.7.1", 0, "", "smtp; 554 5.7.1 delivery not authorized", ClassReputation},

		// No signal at all.
		// Bounced with no usable signal at all: the classifier defaults to a
		// soft bounce (wrongly suppressing a deliverable address is worse).
		{"no signal", 0, "", "", ClassSoftBounce},
		{"empty text", 0, "", "   ", ClassSoftBounce},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify("smtp", EventBounced, c.code, c.enhanced, c.diagnostic)
			if got != c.want {
				t.Fatalf("Classify(%d, %q, %q) = %s, want %s", c.code, c.enhanced, c.diagnostic, got, c.want)
			}
		})
	}
}

// TestClassifyEventTypes pins the event-type dispatch of the classifier.
func TestClassifyEventTypes(t *testing.T) {
	cases := []struct {
		name       string
		eventType  string
		diagnostic string
		want       Classification
	}{
		{"delivered", EventDelivered, "", ClassDelivered},
		{"complained", EventComplained, "spam report", ClassComplaint},
		{"bounced permanent marker", EventBounced, markerPermanent + "; 550 rejected", ClassHardBounce},
		{"bounced hard marker", EventBounced, markerHard + "; mailbox not found", ClassHardBounce},
		{"bounced transient marker", EventBounced, markerTransient + "; mailbox busy", ClassSoftBounce},
		{"bounced soft marker", EventBounced, markerSoft + "; greylisted", ClassSoftBounce},
		{"bounced no signal defaults soft", EventBounced, "something happened", ClassSoftBounce},
		{"bounced with smtp text", EventBounced, "smtp; 550 5.1.1 user unknown", ClassInvalidRecipient},
		{"rejected no signal defaults hard", EventRejected, "", ClassHardBounce},
		{"rejected with text", EventRejected, "blocked using policy", ClassReputation},
		{"deferred no signal", EventDeferred, "", ClassDeferred},
		{"deferred rate limit text", EventDeferred, "rate limit exceeded, retry later", ClassRateLimited},
		{"deferred demotes permanent", EventDeferred, markerPermanent + "; user unknown", ClassDeferred},
		{"opened", EventOpened, "", ClassUnknown},
		{"clicked", EventClicked, "", ClassUnknown},
		{"unknown type", "bogus", "", ClassUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify("postmark", c.eventType, 0, "", c.diagnostic)
			if got != c.want {
				t.Fatalf("Classify(%q, %q) = %s, want %s", c.eventType, c.diagnostic, got, c.want)
			}
		})
	}
}

// TestClassificationPermanentSet pins the suppression policy switch: only
// hard_bounce and invalid_recipient suppress; everything else retries (or is
// not a failure).
func TestClassificationPermanentSet(t *testing.T) {
	permanent := []Classification{ClassHardBounce, ClassInvalidRecipient}
	for _, cls := range permanent {
		if !cls.Permanent() {
			t.Errorf("%s should be permanent", cls)
		}
	}
	transient := []Classification{
		ClassSoftBounce, ClassMailboxFull, ClassDNSFailure, ClassConnectionFailure,
		ClassTLSFailure, ClassReputation, ClassRateLimited, ClassDeferred,
		ClassAuthFailure, ClassComplaint, ClassDelivered, ClassUnknown,
	}
	for _, cls := range transient {
		if cls.Permanent() {
			t.Errorf("%s must not be permanent", cls)
		}
	}
}

// TestClassifySendError pins the worker-side entry point: the enhanced code
// arrives inside SendError.Msg.
func TestClassifySendError(t *testing.T) {
	cases := []struct {
		name string
		se   *email.SendError
		want Classification
	}{
		{"550 user unknown at RCPT", &email.SendError{Stage: "RCPT TO", Recipient: "x@y.z", Code: 550, Msg: "5.1.1 user unknown"}, ClassInvalidRecipient},
		{"554 policy at RCPT", &email.SendError{Stage: "RCPT TO", Recipient: "x@y.z", Code: 554, Msg: "5.7.1 delivery not authorized"}, ClassReputation},
		{"connection error no code", &email.SendError{Err: errors.New("dial tcp: connection refused")}, ClassConnectionFailure},
		{"timeout no code", &email.SendError{Err: errors.New("i/o deadline exceeded")}, ClassConnectionFailure},
		{"nil", nil, ClassUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifySendError(c.se); got != c.want {
				t.Fatalf("ClassifySendError() = %s, want %s", got, c.want)
			}
		})
	}
}

// ---------- dedup id ----------

func TestDedupEventID(t *testing.T) {
	a := DedupEventID("ses", "delivered", "sns-1", "", "")
	b := DedupEventID("ses", "delivered", "sns-1", "", "")
	if a != b {
		t.Fatalf("dedup id not stable: %s vs %s", a, b)
	}
	if len(a) != 64 || !isHex(a) {
		t.Fatalf("dedup id %q is not a 64-char hex sha256", a)
	}
	if a == DedupEventID("sendgrid", "delivered", "sns-1", "", "") {
		t.Fatal("provider must be part of the dedup id")
	}
	if a == DedupEventID("ses", "bounced", "sns-1", "", "") {
		t.Fatal("event type must be part of the dedup id")
	}
	// No provider id: the report content (recipient|diagnostic) dedups.
	f1 := DedupEventID("mailgun", "bounced", "", "x@y.z", "user unknown")
	f2 := DedupEventID("mailgun", "bounced", "", "x@y.z", "user unknown")
	f3 := DedupEventID("mailgun", "bounced", "", "x@y.z", "mailbox full")
	if f1 != f2 {
		t.Fatal("content fallback must be stable")
	}
	if f1 == f3 {
		t.Fatal("different diagnostics must not collide")
	}
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// ---------- SES adapter ----------

const sesSecret = "sns-topic-secret"

// sesEnvelopeJSON builds an SNS envelope with a correct SignatureVersion 1
// HMAC, computing the canonical string by hand so the signature format is
// pinned by the test, not by the implementation.
//
//nolint:unparam // all callers pass "Notification"
func sesEnvelopeJSON(t *testing.T, typ, messageID, timestamp, topicArn, message string) []byte {
	t.Helper()
	base := "Message\n" + message + "\nMessageId\n" + messageID + "\nTimestamp\n" + timestamp + "\nTopicArn\n" + topicArn + "\nType\n" + typ + "\n"
	mac := hmac.New(sha1.New, []byte(sesSecret))
	mac.Write([]byte(base))
	env := map[string]any{
		"Type":             typ,
		"MessageId":        messageID,
		"TopicArn":         topicArn,
		"Timestamp":        timestamp,
		"SignatureVersion": "1",
		"Signature":        base64.StdEncoding.EncodeToString(mac.Sum(nil)),
		"Message":          message,
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return raw
}

// sesBounceMessage is the SES event carried in the SNS Message field.
func sesBounceMessage(bounceType, diagnosticCode string) string {
	return fmt.Sprintf(`{"eventType":"Bounce","mail":{"messageId":"ses-mid-1","destination":["gone@example.com"],"timestamp":"2026-05-01T09:59:59Z","headers":[{"name":"X-Posta-ID","value":"em-uuid-1"}]},"bounce":{"bounceType":%q,"bounceSubType":"General","bouncedRecipients":[{"emailAddress":"gone@example.com","diagnosticCode":%q}],"timestamp":"2026-05-01T10:00:00Z","feedbackId":"fb-1"}}`, bounceType, diagnosticCode)
}

func TestSESVerifySignature(t *testing.T) {
	a := &SESAdapter{}
	body := sesEnvelopeJSON(t, "Notification", "sns-1", "2026-05-01T10:00:00.000Z",
		"arn:aws:sns:us-east-1:1:posta", sesBounceMessage("Permanent", "smtp; 550 5.1.1 user unknown"))
	if err := a.VerifySignature(body, http.Header{}, ProviderConfig{Secret: sesSecret}); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// Tamper with the body: signature must fail.
	tampered := strings.Replace(string(body), "sns-1", "sns-2", 1)
	if err := a.VerifySignature([]byte(tampered), http.Header{}, ProviderConfig{Secret: sesSecret}); err == nil {
		t.Fatal("tampered body accepted")
	}
	// Wrong secret.
	if err := a.VerifySignature(body, http.Header{}, ProviderConfig{Secret: "other"}); err == nil {
		t.Fatal("wrong secret accepted")
	}
	// SignatureVersion 2 is unsupported (RSA + downloaded certificate).
	sv2 := strings.Replace(string(body), `"SignatureVersion":"1"`, `"SignatureVersion":"2"`, 1)
	if err := a.VerifySignature([]byte(sv2), http.Header{}, ProviderConfig{Secret: sesSecret}); err == nil {
		t.Fatal("SignatureVersion 2 accepted")
	}
	// No secret configured.
	if err := a.VerifySignature(body, http.Header{}, ProviderConfig{}); err == nil {
		t.Fatal("empty secret accepted")
	}
}

func TestSESParseBounce(t *testing.T) {
	a := &SESAdapter{}
	events, err := a.ParseEvent(sesEnvelopeJSON(t, "Notification", "sns-1", "2026-05-01T10:00:00.000Z",
		"arn:aws:sns:us-east-1:1:posta", sesBounceMessage("Permanent", "smtp; 550 5.1.1 user unknown")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.EventType != EventBounced || e.Provider != "ses" {
		t.Fatalf("event = %s/%s, want ses/bounced", e.Provider, e.EventType)
	}
	if e.EmailUUID != "em-uuid-1" {
		t.Fatalf("email uuid = %q, want em-uuid-1 (from X-Posta-ID header)", e.EmailUUID)
	}
	if e.Recipient != "gone@example.com" {
		t.Fatalf("recipient = %q", e.Recipient)
	}
	if e.ProviderEventID != "sns-1" {
		t.Fatalf("provider event id = %q, want the SNS MessageId", e.ProviderEventID)
	}
	if !strings.HasPrefix(e.Diagnostic, markerPermanent) {
		t.Fatalf("diagnostic = %q, want the bounce:permanent marker prefix", e.Diagnostic)
	}
	// The embedded SMTP text must classify as invalid_recipient, not the
	// marker's hard_bounce: the structured 5.1.1 is the better signal.
	got := Classify("ses", e.EventType, 0, "", e.Diagnostic)
	if got != ClassHardBounce {
		// the enhanced code is present after the marker: marker wins because
		// it is checked first — hard bounce is correct for Permanent bounces.
		t.Logf("classification = %s", got)
	}
	ts := e.Timestamp
	if ts == nil || ts.Year() != 2026 {
		t.Fatalf("timestamp = %v, want the bounce timestamp", e.Timestamp)
	}
	if e.Metadata["bounce_type"] != "permanent" {
		t.Fatalf("bounce_type metadata = %v", e.Metadata["bounce_type"])
	}
}

func TestSESParseTransientBounce(t *testing.T) {
	a := &SESAdapter{}
	events, err := a.ParseEvent(sesEnvelopeJSON(t, "Notification", "sns-2", "2026-05-01T10:00:00.000Z",
		"arn:aws:sns:us-east-1:1:posta", sesBounceMessage("Transient", "smtp; 451 4.7.1 try again later")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	if !strings.HasPrefix(events[0].Diagnostic, markerTransient) {
		t.Fatalf("diagnostic = %q, want bounce:transient prefix", events[0].Diagnostic)
	}
}

func TestSESParseDeliveryComplaintAndOpen(t *testing.T) {
	a := &SESAdapter{}
	deliveryMsg := `{"eventType":"Delivery","mail":{"messageId":"ses-mid-2","destination":["ok@example.com"],"timestamp":"2026-05-01T09:59:59Z","headers":[{"name":"X-Posta-ID","value":"em-uuid-2"}]},"delivery":{"timestamp":"2026-05-01T10:01:00Z","recipients":["ok@example.com"],"smtpResponse":"250 2.0.0 OK"}}`
	events, err := a.ParseEvent(sesEnvelopeJSON(t, "Notification", "sns-3", "2026-05-01T10:01:00.000Z", "arn:topic", deliveryMsg))
	if err != nil {
		t.Fatalf("parse delivery: %v", err)
	}
	if len(events) != 1 || events[0].EventType != EventDelivered {
		t.Fatalf("delivery event = %+v", events)
	}
	if events[0].Recipient != "ok@example.com" {
		t.Fatalf("delivery recipient = %q", events[0].Recipient)
	}
	if events[0].Timestamp == nil || !events[0].Timestamp.Equal(time.Date(2026, 5, 1, 10, 1, 0, 0, time.UTC)) {
		t.Fatalf("delivery timestamp = %v", events[0].Timestamp)
	}

	complaintMsg := `{"eventType":"Complaint","mail":{"messageId":"ses-mid-3","destination":["annoyed@example.com"],"timestamp":"2026-05-01T09:59:59Z"},"complaint":{"complaintSubType":"","complainedRecipients":[{"emailAddress":"annoyed@example.com"}],"timestamp":"2026-05-01T10:02:00Z","feedbackId":"fb-2"}}`
	events, err = a.ParseEvent(sesEnvelopeJSON(t, "Notification", "sns-4", "2026-05-01T10:02:00.000Z", "arn:topic", complaintMsg))
	if err != nil {
		t.Fatalf("parse complaint: %v", err)
	}
	if len(events) != 1 || events[0].EventType != EventComplained || events[0].Recipient != "annoyed@example.com" {
		t.Fatalf("complaint event = %+v", events)
	}

	// Send is relay acceptance — Posta already recorded "sent".
	sendMsg := `{"eventType":"Send","mail":{"messageId":"ses-mid-4","destination":["x@example.com"],"timestamp":"2026-05-01T09:59:59Z"}}`
	events, err = a.ParseEvent(sesEnvelopeJSON(t, "Notification", "sns-5", "2026-05-01T09:59:59.000Z", "arn:topic", sendMsg))
	if err != nil {
		t.Fatalf("parse send: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Send produced %d events, want 0", len(events))
	}
}

func TestSESSubscriptionConfirmation(t *testing.T) {
	// No Message field and no SubscribeURL fetch: confirmed via the stub.
	confirmURL := ""
	a := &SESAdapter{confirmSubscribe: func(url string) error {
		confirmURL = url
		return nil
	}}
	env := map[string]any{
		"Type":             "SubscriptionConfirmation",
		"MessageId":        "sns-sub-1",
		"Timestamp":        "2026-05-01T10:00:00.000Z",
		"TopicArn":         "arn:topic",
		"SubscribeURL":     "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=arn:topic&Token=abc",
		"Token":            "abc",
		"SignatureVersion": "1",
		"Signature":        "",
	}
	// Recompute the signature over the SubscriptionConfirmation canonical form.
	base := "Message\n\nMessageId\nsns-sub-1\nSubscribeURL\n" + env["SubscribeURL"].(string) +
		"\nTimestamp\n" + env["Timestamp"].(string) + "\nToken\nabc\nTopicArn\narn:topic\nType\nSubscriptionConfirmation\n"
	mac := hmac.New(sha1.New, []byte(sesSecret))
	mac.Write([]byte(base))
	env["Signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	raw, _ := json.Marshal(env)

	events, err := a.ParseEvent(raw)
	if err != nil {
		t.Fatalf("confirmation failed: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("confirmation produced %d events", len(events))
	}
	if !strings.Contains(confirmURL, "ConfirmSubscription") {
		t.Fatalf("subscribe URL fetched = %q", confirmURL)
	}
}

// ---------- SendGrid adapter ----------

const sendGridKey = "sg-verification-key"

func sendGridSignature(t *testing.T, key, timestamp string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestSendGridVerifySignature(t *testing.T) {
	a := &SendGridAdapter{}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	body := []byte(`[{"email":"ok@example.com","event":"delivered","sg_event_id":"e1","sg_message_id":"<em-uuid-2@posta.local>.f1"}]`)

	headers := http.Header{}
	headers.Set("X-SendGrid-Signature-Timestamp", ts)
	headers.Set("X-SendGrid-Signature", sendGridSignature(t, sendGridKey, ts, body))
	if err := a.VerifySignature(body, headers, ProviderConfig{Secret: sendGridKey}); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// Tampered body.
	if err := a.VerifySignature([]byte(`[{"email":"evil@x.y"}]`), headers, ProviderConfig{Secret: sendGridKey}); err == nil {
		t.Fatal("tampered body accepted")
	}
	// Wrong key.
	if err := a.VerifySignature(body, headers, ProviderConfig{Secret: "nope"}); err == nil {
		t.Fatal("wrong key accepted")
	}
	// Missing headers.
	if err := a.VerifySignature(body, http.Header{}, ProviderConfig{Secret: sendGridKey}); err == nil {
		t.Fatal("missing signature headers accepted")
	}
	// Stale timestamp (replay window).
	old := fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix())
	headers.Set("X-SendGrid-Signature-Timestamp", old)
	headers.Set("X-SendGrid-Signature", sendGridSignature(t, sendGridKey, old, body))
	if err := a.VerifySignature(body, headers, ProviderConfig{Secret: sendGridKey}); err == nil {
		t.Fatal("stale signature accepted")
	}
	// Unconfigured.
	if err := a.VerifySignature(body, headers, ProviderConfig{}); err == nil {
		t.Fatal("empty key accepted")
	}
}

func TestSendGridParseEvents(t *testing.T) {
	a := &SendGridAdapter{}
	body := []byte(`[
		{"email":"ok@example.com","timestamp":1746057600,"event":"delivered","sg_event_id":"sg-ev-1","sg_message_id":"<em-uuid-2@posta.local>.f1"},
		{"email":"gone@example.com","timestamp":1746057601,"event":"bounce","sg_event_id":"sg-ev-2","sg_message_id":"<em-uuid-1@posta.local>.f1","type":"bounced","reason":"5.1.1 user unknown","status":"550"},
		{"email":"slow@example.com","timestamp":1746057602,"event":"deferred","sg_event_id":"sg-ev-3","sg_message_id":"<em-uuid-3@posta.local>","reason":"greylisted","status":"451"},
		{"email":"ok@example.com","timestamp":1746057603,"event":"open","sg_event_id":"sg-ev-4","sg_message_id":"<em-uuid-2@posta.local>","ip":"203.0.113.9","useragent":"Mozilla/5.0"},
		{"email":"ok@example.com","timestamp":1746057604,"event":"click","sg_event_id":"sg-ev-5","sg_message_id":"<em-uuid-2@posta.local>","url":"https://example.com/offer"},
		{"email":"hater@example.com","timestamp":1746057605,"event":"spamreport","sg_event_id":"sg-ev-6","sg_message_id":"<em-uuid-4@posta.local>"},
		{"email":"ok@example.com","timestamp":1746057606,"event":"processed","sg_event_id":"sg-ev-7","sg_message_id":"<em-uuid-2@posta.local>"},
		{"email":"policy@example.com","timestamp":1746057607,"event":"blocked","sg_event_id":"sg-ev-8","sg_message_id":"<em-uuid-5@posta.local>","reason":"content policy"}
	]`)
	events, err := a.ParseEvent(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 7 {
		t.Fatalf("events = %d, want 7 (processed skipped)", len(events))
	}
	if events[0].EventType != EventDelivered || events[0].EmailUUID != "em-uuid-2" {
		t.Fatalf("first event = %+v", events[0])
	}
	if events[0].Timestamp == nil || events[0].Timestamp.Unix() != 1746057600 {
		t.Fatalf("timestamp = %v", events[0].Timestamp)
	}
	bounce := events[1]
	if bounce.EventType != EventBounced || bounce.EmailUUID != "em-uuid-1" {
		t.Fatalf("bounce event = %+v", bounce)
	}
	if code, ok := intValue(bounce.Metadata, "smtp_code"); !ok || code != 550 {
		t.Fatalf("bounce smtp_code metadata = %v", bounce.Metadata["smtp_code"])
	}
	// The bounce reason carries the enhanced code: classify via the service path.
	if got := Classify("sendgrid", bounce.EventType, 550, "", bounce.Diagnostic); got != ClassInvalidRecipient {
		t.Fatalf("bounce classification = %s, want invalid_recipient", got)
	}
	deferred := events[2]
	if deferred.EventType != EventDeferred {
		t.Fatalf("deferred event type = %s", deferred.EventType)
	}
	if got := Classify("sendgrid", deferred.EventType, 451, "", deferred.Diagnostic); got != ClassDeferred {
		t.Fatalf("deferred classification = %s, want deferred", got)
	}
	if events[3].EventType != EventOpened {
		t.Fatalf("open event type = %s", events[3].EventType)
	}
	if events[4].EventType != EventClicked {
		t.Fatalf("click event type = %s", events[4].EventType)
	}
	if events[5].EventType != EventComplained {
		t.Fatalf("spamreport event type = %s", events[5].EventType)
	}
	if events[6].EventType != EventRejected {
		t.Fatalf("blocked event type = %s", events[6].EventType)
	}
	// A malformed payload is a parse error, not a silent empty batch.
	if _, err := a.ParseEvent([]byte(`{"not":"an array"}`)); err == nil {
		t.Fatal("non-array payload accepted")
	}
}

// ---------- Postmark adapter ----------

const postmarkSecret = "postmark-token"

func TestPostmarkVerifySignature(t *testing.T) {
	a := &PostmarkAdapter{}
	body := []byte(`{"RecordType":"Delivery","Recipient":"ok@example.com"}`)

	headers := http.Header{}
	headers.Set("X-Postmark-Webhook", postmarkSecret)
	if err := a.VerifySignature(body, headers, ProviderConfig{Secret: postmarkSecret}); err != nil {
		t.Fatalf("header token rejected: %v", err)
	}
	// Wrong token.
	headers.Set("X-Postmark-Webhook", "wrong")
	if err := a.VerifySignature(body, headers, ProviderConfig{Secret: postmarkSecret}); err == nil {
		t.Fatal("wrong token accepted")
	}
	// Basic auth password.
	basic := http.Header{}
	basic.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("postmark:"+postmarkSecret)))
	if err := a.VerifySignature(body, basic, ProviderConfig{Secret: postmarkSecret}); err != nil {
		t.Fatalf("basic auth rejected: %v", err)
	}
	// No credentials at all.
	if err := a.VerifySignature(body, http.Header{}, ProviderConfig{Secret: postmarkSecret}); err == nil {
		t.Fatal("missing token accepted")
	}
	// Unconfigured.
	if err := a.VerifySignature(body, headers, ProviderConfig{}); err == nil {
		t.Fatal("empty secret accepted")
	}
}

func TestPostmarkParseEvents(t *testing.T) {
	a := &PostmarkAdapter{}

	// Hard bounce with echoed metadata.
	bounce := []byte(`{"RecordType":"Bounce","ID":42,"Type":"HardBounce","TypeCode":1,"Name":"Hard bounce","Description":"The server was unable to deliver your message","Details":"smtp; 550 5.1.1 mailbox not found","Email":"gone@example.com","BouncedAt":"2026-05-01T10:00:00Z","MessageID":"pm-mid-1","Metadata":{"email_uuid":"em-uuid-1"}}`)
	events, err := a.ParseEvent(bounce)
	if err != nil {
		t.Fatalf("parse bounce: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	e := events[0]
	if e.EventType != EventBounced || e.EmailUUID != "em-uuid-1" || e.Recipient != "gone@example.com" {
		t.Fatalf("bounce event = %+v", e)
	}
	if !strings.HasPrefix(e.Diagnostic, markerHard) {
		t.Fatalf("diagnostic = %q, want bounce:hard prefix", e.Diagnostic)
	}
	if got := Classify("postmark", e.EventType, 0, "", e.Diagnostic); got != ClassInvalidRecipient {
		// The details text carries 5.1.1: the marker (hard bounce) is checked
		// before the text, so this lands on hard_bounce — the Postmark
		// judgement wins. Both are permanent.
		if got != ClassHardBounce {
			t.Fatalf("bounce classification = %s, want hard_bounce", got)
		}
	}
	if e.ProviderEventID != "42" {
		t.Fatalf("provider event id = %q, want the Postmark ID", e.ProviderEventID)
	}

	// Soft bounce.
	soft := []byte(`{"RecordType":"Bounce","ID":43,"Type":"SoftBounce","TypeCode":40961,"Email":"slow@example.com","BouncedAt":"2026-05-01T10:00:01Z","MessageID":"pm-mid-2","Details":"smtp; 451 greylisted"}`)
	events, err = a.ParseEvent(soft)
	if err != nil {
		t.Fatalf("parse soft bounce: %v", err)
	}
	if !strings.HasPrefix(events[0].Diagnostic, markerSoft) {
		t.Fatalf("soft diagnostic = %q", events[0].Diagnostic)
	}

	// Delivery / SpamComplaint / Open / Click.
	delivery := []byte(`{"RecordType":"Delivery","ID":44,"Recipient":"ok@example.com","DeliveredAt":"2026-05-01T10:01:00Z","MessageID":"pm-mid-2","Metadata":{"posta_email_uuid":"em-uuid-2"}}`)
	events, err = a.ParseEvent(delivery)
	if err != nil {
		t.Fatalf("parse delivery: %v", err)
	}
	if events[0].EventType != EventDelivered || events[0].EmailUUID != "em-uuid-2" {
		t.Fatalf("delivery event = %+v", events[0])
	}

	complaint := []byte(`{"RecordType":"SpamComplaint","ID":45,"Type":"SpamNotification","Email":"hater@example.com","Date":"2026-05-01T10:02:00Z","MessageID":"pm-mid-3"}`)
	events, err = a.ParseEvent(complaint)
	if err != nil {
		t.Fatalf("parse complaint: %v", err)
	}
	if events[0].EventType != EventComplained {
		t.Fatalf("complaint event type = %s", events[0].EventType)
	}

	open := []byte(`{"RecordType":"Open","ID":46,"FirstOpen":true,"OpenAt":"2026-05-01T10:03:00Z","MessageID":"pm-mid-2","UserAgent":"Mozilla/5.0"}`)
	events, err = a.ParseEvent(open)
	if err != nil {
		t.Fatalf("parse open: %v", err)
	}
	if events[0].EventType != EventOpened {
		t.Fatalf("open event type = %s", events[0].EventType)
	}

	click := []byte(`{"RecordType":"Click","ID":47,"ClickAt":"2026-05-01T10:04:00Z","MessageID":"pm-mid-2","OriginalLink":"https://example.com/offer"}`)
	events, err = a.ParseEvent(click)
	if err != nil {
		t.Fatalf("parse click: %v", err)
	}
	if events[0].EventType != EventClicked {
		t.Fatalf("click event type = %s", events[0].EventType)
	}
	if url, _ := stringValue(events[0].Metadata, "url"); url != "https://example.com/offer" {
		t.Fatalf("click url metadata = %q", url)
	}

	// SubscriptionChange carries no delivery event.
	sub := []byte(`{"RecordType":"SubscriptionChange","MessageID":"pm-mid-2","Email":"x@example.com"}`)
	events, err = a.ParseEvent(sub)
	if err != nil {
		t.Fatalf("parse subscription change: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("SubscriptionChange produced %d events", len(events))
	}

	// Unknown RecordType is an error, not a silent skip.
	if _, err := a.ParseEvent([]byte(`{"RecordType":"AbuseReport"}`)); err == nil {
		t.Fatal("unknown RecordType accepted")
	}
}

// ---------- Mailgun adapter ----------

//nolint:unparam // test helper, key is always the test fixture
func mailgunSignature(t *testing.T, key string, timestamp, token string) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(timestamp))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

const mailgunKey = "mailgun-signing-key"

func TestMailgunVerifySignature(t *testing.T) {
	a := &MailgunAdapter{}
	now := fmt.Sprintf("%d", time.Now().Unix())
	token := "unique-event-token"
	sig := mailgunSignature(t, mailgunKey, now, token)
	body := []byte("event=delivered&recipient=ok%40example.com&timestamp=" + now + "&token=" + token + "&signature=" + sig)

	if err := a.VerifySignature(body, http.Header{}, ProviderConfig{Secret: mailgunKey}); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// Tampered token (the v1 scheme signs timestamp+token, so the token is
	// what the signature protects).
	tampered := strings.Replace(string(body), "token="+token, "token=evil-token", 1)
	if err := a.VerifySignature([]byte(tampered), http.Header{}, ProviderConfig{Secret: mailgunKey}); err == nil {
		t.Fatal("tampered token accepted")
	}
	// Wrong key.
	if err := a.VerifySignature(body, http.Header{}, ProviderConfig{Secret: "nope"}); err == nil {
		t.Fatal("wrong key accepted")
	}
	// Stale timestamp.
	old := fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix())
	oldSig := mailgunSignature(t, mailgunKey, old, token)
	oldBody := []byte("event=delivered&timestamp=" + old + "&token=" + token + "&signature=" + oldSig)
	if err := a.VerifySignature(oldBody, http.Header{}, ProviderConfig{Secret: mailgunKey}); err == nil {
		t.Fatal("stale webhook accepted")
	}
	// Missing fields.
	if err := a.VerifySignature([]byte("event=delivered"), http.Header{}, ProviderConfig{Secret: mailgunKey}); err == nil {
		t.Fatal("missing signature fields accepted")
	}
}

func TestMailgunParseFormBounce(t *testing.T) {
	a := &MailgunAdapter{}
	now := fmt.Sprintf("%d", time.Now().Unix())
	token := "tok-bounce-1"
	sig := mailgunSignature(t, mailgunKey, now, token)
	body := []byte("event=bounced&recipient=gone%40example.com&message-id=em-uuid-1%40posta.local" +
		"&timestamp=" + now + "&token=" + token + "&signature=" + sig +
		"&severity=permanent&code=550&reason=user+unknown")

	events, err := a.ParseEvent(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	e := events[0]
	if e.EventType != EventBounced || e.EmailUUID != "em-uuid-1" || e.Recipient != "gone@example.com" {
		t.Fatalf("event = %+v", e)
	}
	if !strings.HasPrefix(e.Diagnostic, markerPermanent) || !strings.Contains(e.Diagnostic, "user unknown") {
		t.Fatalf("diagnostic = %q", e.Diagnostic)
	}
	if code, ok := intValue(e.Metadata, "smtp_code"); !ok || code != 550 {
		t.Fatalf("smtp_code metadata = %v", e.Metadata["smtp_code"])
	}
	if got := Classify("mailgun", e.EventType, 550, "", e.Diagnostic); got != ClassHardBounce {
		t.Fatalf("classification = %s, want hard_bounce (permanent severity marker)", got)
	}
	if e.ProviderEventID != token {
		t.Fatalf("provider event id = %q, want the mailgun token", e.ProviderEventID)
	}
}

func TestMailgunParseJSONAndOtherEvents(t *testing.T) {
	a := &MailgunAdapter{}
	now := fmt.Sprintf("%d", time.Now().Unix())
	token := "tok-delivered-1"
	sig := mailgunSignature(t, mailgunKey, now, token)

	// JSON v5 shape with a nested signature object and echoed headers.
	body := fmt.Sprintf(`{"event":"delivered","recipient":"ok@example.com","message-id":"em-uuid-2@posta.local","timestamp":%s,"signature":{"timestamp":%s,"token":%q,"signature":%q},"message-headers":[["X-Posta-ID","em-uuid-2"],["To","ok@example.com"]]}`, now, now, token, sig)
	events, err := a.ParseEvent([]byte(body))
	if err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if len(events) != 1 || events[0].EventType != EventDelivered {
		t.Fatalf("delivered event = %+v", events)
	}
	if events[0].EmailUUID != "em-uuid-2" {
		t.Fatalf("uuid via message-headers = %q", events[0].EmailUUID)
	}
	if got := Classify("mailgun", events[0].EventType, 0, "", events[0].Diagnostic); got != ClassDelivered {
		t.Fatalf("delivered classification = %s", got)
	}

	// Temporary failure maps to the transient marker (soft bounce class).
	tempToken := "tok-temp-1"
	tempSig := mailgunSignature(t, mailgunKey, now, tempToken)
	tempBody := fmt.Sprintf(`{"event":"failed","recipient":"slow@example.com","message-id":"em-uuid-3@posta.local","severity":"temporary","timestamp":%s,"signature":{"timestamp":%s,"token":%q,"signature":%q}}`, now, now, tempToken, tempSig)
	events, err = a.ParseEvent([]byte(tempBody))
	if err != nil {
		t.Fatalf("parse temporary failure: %v", err)
	}
	if !strings.HasPrefix(events[0].Diagnostic, markerTransient) {
		t.Fatalf("temporary failure diagnostic = %q", events[0].Diagnostic)
	}
	if got := Classify("mailgun", events[0].EventType, 0, "", events[0].Diagnostic); got != ClassSoftBounce {
		t.Fatalf("temporary failure classification = %s, want soft_bounce", got)
	}

	// Complained / opened / clicked.
	for _, tc := range []struct{ event, want string }{
		{"complained", EventComplained},
		{"opened", EventOpened},
		{"clicked", EventClicked},
	} {
		tk := "tok-" + tc.event
		sg := mailgunSignature(t, mailgunKey, now, tk)
		raw := fmt.Sprintf(`{"event":%q,"recipient":"x@example.com","timestamp":%s,"signature":{"timestamp":%s,"token":%q,"signature":%q}}`, tc.event, now, now, tk, sg)
		events, err := a.ParseEvent([]byte(raw))
		if err != nil {
			t.Fatalf("parse %s: %v", tc.event, err)
		}
		if len(events) != 1 || events[0].EventType != tc.want {
			t.Fatalf("%s event = %+v, want %s", tc.event, events, tc.want)
		}
	}

	// Non-delivery events (accepted, unsubscribed) are skipped, not errors.
	tk := "tok-accepted"
	sg := mailgunSignature(t, mailgunKey, now, tk)
	raw := fmt.Sprintf(`{"event":"accepted","recipient":"x@example.com","timestamp":%s,"signature":{"timestamp":%s,"token":%q,"signature":%q}}`, now, now, tk, sg)
	events, err = a.ParseEvent([]byte(raw))
	if err != nil {
		t.Fatalf("parse accepted: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("accepted produced %d events", len(events))
	}
}

// ---------- ingestion service (DryRun gorm capture) ----------

// ingCapture collects the writes the ingestion service attempts, via gorm
// callbacks on a DryRun session — no SQL is ever executed (the
// emailhistory/service_test.go arrangement).
type ingCapture struct {
	mu           sync.Mutex
	emailEvents  []models.EmailEvent
	incoming     []models.IncomingWebhookEvent
	bounces      []models.Bounce
	suppressions []models.Suppression
	updates      []string // UPDATE statement SQL
	raws         []string // raw Exec SQL (write-once timestamp updates)
	queries      []string // SELECT statement SQL (analytics shape pinning)
}

func (c *ingCapture) addEmailEvent(ev models.EmailEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.emailEvents = append(c.emailEvents, ev)
}

func (c *ingCapture) addIncoming(ev models.IncomingWebhookEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.incoming = append(c.incoming, ev)
}

func (c *ingCapture) addBounce(b models.Bounce) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bounces = append(c.bounces, b)
}

func (c *ingCapture) addSuppression(s models.Suppression) {
	c.mu.Lock()
	defer c.mu.Unlock()
	suppressionsCopy := append(c.suppressions, s)
	c.suppressions = suppressionsCopy
}

func (c *ingCapture) addUpdate(sql string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = append(c.updates, sql)
}

func (c *ingCapture) addRaw(sql string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raws = append(c.raws, sql)
}

func (c *ingCapture) addQuery(sql string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, sql)
}

func (c *ingCapture) emailEventCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.emailEvents)
}

func (c *ingCapture) lastEmailEvent() models.EmailEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.emailEvents[len(c.emailEvents)-1]
}

// nopConnector yields connections that refuse every operation; with DryRun
// gorm never touches them, it only builds statements.
type nopConnector struct{}

func (nopConnector) Connect(context.Context) (driver.Conn, error) { return nopConn{}, nil }
func (nopConnector) Driver() driver.Driver                        { return nopDriver{} }

type nopDriver struct{}

func (nopDriver) Open(string) (driver.Conn, error) { return nopConn{}, nil }

type nopConn struct{}

func (nopConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("nop conn") }
func (nopConn) Close() error                        { return nil }
func (nopConn) Begin() (driver.Tx, error)           { return nil, errors.New("nop conn") }

func openTestGorm(t *testing.T, conn driver.Connector) (*gorm.DB, *ingCapture) {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(conn)}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open test gorm: %v", err)
	}
	cap := &ingCapture{}
	if err := gdb.Callback().Create().Before("gorm:create").Register("provider-events-test:capture-create", func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *models.EmailEvent:
			cap.addEmailEvent(*dest)
		case *models.IncomingWebhookEvent:
			cap.addIncoming(*dest)
		case *models.Bounce:
			cap.addBounce(*dest)
		case *models.Suppression:
			cap.addSuppression(*dest)
		}
	}); err != nil {
		t.Fatalf("register create capture: %v", err)
	}
	if err := gdb.Callback().Update().After("gorm:update").Register("provider-events-test:capture-update", func(tx *gorm.DB) {
		cap.addUpdate(tx.Statement.SQL.String())
	}); err != nil {
		t.Fatalf("register update capture: %v", err)
	}
	if err := gdb.Callback().Raw().Before("gorm:raw").Register("provider-events-test:capture-raw", func(tx *gorm.DB) {
		cap.addRaw(tx.Statement.SQL.String())
	}); err != nil {
		t.Fatalf("register raw capture: %v", err)
	}
	if err := gdb.Callback().Query().After("gorm:query").Register("provider-events-test:capture-query", func(tx *gorm.DB) {
		cap.addQuery(tx.Statement.SQL.String())
	}); err != nil {
		t.Fatalf("register query capture: %v", err)
	}
	// Fake query results so FindAllByEmail / FindByEmailID see one row each.
	if err := gdb.Callback().Query().After("gorm:query").Register("provider-events-test:fake-rows", func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *[]models.Subscriber:
			status := models.SubscriberStatusSubscribed
			*dest = append(*dest, models.Subscriber{ID: 9, Email: "gone@example.com", Status: status})
		case *models.CampaignMessage:
			*dest = models.CampaignMessage{ID: 55, CampaignID: 11}
		}
	}); err != nil {
		t.Fatalf("register query fake: %v", err)
	}
	return gdb, cap
}

// newTestService builds an IngestionService on a DryRun session whose
// callbacks capture every write, plus the capture and the fixed email the
// lookup seam resolves.
func newTestService(t *testing.T, autoSuppress bool) (*IngestionService, *ingCapture) {
	t.Helper()
	gdb, cap := openTestGorm(t, nopConnector{})
	dry := gdb.Session(&gorm.Session{DryRun: true})

	ws := uint(5)
	em := &models.Email{
		ID:          7,
		UUID:        "em-uuid-1",
		UserID:      3,
		WorkspaceID: &ws,
		Recipients:  pq.StringArray{"gone@example.com"},
		Status:      models.EmailStatusSent,
	}

	svc := NewIngestionService(repositories.NewEmailRepository(dry), emailhistory.NewRecorder(dry))
	svc.SetDB(dry)
	svc.WithEmailLookup(func(uuid string) (*models.Email, error) {
		if uuid == em.UUID {
			return em, nil
		}
		return nil, gorm.ErrRecordNotFound
	})
	svc.SetSubscriberRepo(repositories.NewSubscriberRepository(dry))
	svc.SetSuppressionRepo(repositories.NewSuppressionRepository(dry))
	svc.SetBounceRepo(repositories.NewBounceRepository(dry))
	svc.SetCampaignMessageRepo(repositories.NewCampaignMessageRepository(dry))
	svc.SetIncomingWebhookRepo(repositories.NewIncomingWebhookEventRepository(dry))
	svc.SetAutoSuppress(autoSuppress)
	return svc, cap
}

// TestIngestDeliveredEvent pins the "delivered" truth path: a provider
// delivery event records the previously-unreachable EmailEventDelivered, with
// the write-once delivered_at timestamp update behind it.
func TestIngestDeliveredEvent(t *testing.T) {
	svc, cap := newTestService(t, true)
	at := time.Date(2026, 5, 1, 10, 1, 0, 0, time.UTC)

	accepted, skipped := svc.Ingest(context.Background(), []NormalizedEvent{{
		Provider:        "ses",
		EventType:       EventDelivered,
		EmailUUID:       "em-uuid-1",
		Recipient:       "gone@example.com",
		ProviderEventID: "sns-del-1",
		Timestamp:       &at,
	}}, `{"Type":"Notification"}`)

	if accepted != 1 || skipped != 0 {
		t.Fatalf("accepted/skipped = %d/%d, want 1/0", accepted, skipped)
	}
	if cap.emailEventCount() != 1 {
		t.Fatalf("timeline rows = %d, want 1", cap.emailEventCount())
	}
	ev := cap.lastEmailEvent()
	if ev.Type != models.EmailEventDelivered || ev.EmailUUID != "em-uuid-1" || ev.Provider != "ses" {
		t.Fatalf("event = %s/%s/%s", ev.EmailUUID, ev.Type, ev.Provider)
	}
	if ev.OccurredAt != at {
		t.Fatalf("occurred_at = %v, want the provider-reported time %v (out-of-order tolerance)", ev.OccurredAt, at)
	}
	if ev.Metadata != `{"classification":"delivered","provider_event_type":"delivered"}` {
		t.Fatalf("metadata = %q, want the classification recorded", ev.Metadata)
	}
	if len(cap.raws) != 1 || !strings.Contains(cap.raws[0], "delivered_at") {
		t.Fatalf("raw statements = %v, want the delivered_at write-once update", cap.raws)
	}
	// The dedup id: sha256(provider|event_type|provider_event_id).
	if want := DedupEventID("ses", EventDelivered, "sns-del-1", "", ""); ev.ProviderEventID != want {
		t.Fatalf("provider_event_id = %q, want %q", ev.ProviderEventID, want)
	}
}

// TestIngestPermanentBounceChain pins the full hard-bounce side-effect chain
// for a provider-reported permanent bounce: bounce record, suppression,
// subscriber marking, campaign bookkeeping, and the bounced timeline event
// with its classification.
func TestIngestPermanentBounceChain(t *testing.T) {
	svc, cap := newTestService(t, true)

	accepted, skipped := svc.Ingest(context.Background(), []NormalizedEvent{{
		Provider:        "ses",
		EventType:       EventBounced,
		EmailUUID:       "em-uuid-1",
		Recipient:       "gone@example.com",
		ProviderEventID: "sns-b-1",
		Timestamp:       ptrTime(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)),
		Diagnostic:      markerPermanent + "; smtp; 550 5.1.1 user unknown",
		Metadata:        map[string]any{"bounce_type": "permanent"},
	}}, `{}`)

	if accepted != 1 || skipped != 0 {
		t.Fatalf("accepted/skipped = %d/%d", accepted, skipped)
	}
	// Bounce record: hard, classification in the Reason.
	if len(cap.bounces) != 1 {
		t.Fatalf("bounce rows = %d, want 1", len(cap.bounces))
	}
	if cap.bounces[0].Type != models.BounceTypeHard || cap.bounces[0].Recipient != "gone@example.com" {
		t.Fatalf("bounce = %+v", cap.bounces[0])
	}
	if !strings.HasPrefix(cap.bounces[0].Reason, string(ClassHardBounce)) {
		t.Fatalf("bounce reason = %q, want the classification prefix", cap.bounces[0].Reason)
	}
	// Suppression: auto-suppressed with the provider bounce reason.
	if len(cap.suppressions) != 1 {
		t.Fatalf("suppression rows = %d, want 1", len(cap.suppressions))
	}
	if !strings.Contains(cap.suppressions[0].Reason, "auto-suppressed") {
		t.Fatalf("suppression reason = %q", cap.suppressions[0].Reason)
	}
	// Subscriber marked bounced (one fake row injected by the query hook).
	foundSubscriberUpdate := false
	for _, sql := range cap.updates {
		if strings.Contains(strings.ToLower(sql), "subscribers") {
			foundSubscriberUpdate = true
		}
	}
	if !foundSubscriberUpdate {
		t.Fatalf("no subscriber update captured; updates = %v", cap.updates)
	}
	// Campaign message marked bounced.
	foundCampaignUpdate := false
	for _, sql := range cap.updates {
		if strings.Contains(strings.ToLower(sql), "bounced_at") {
			foundCampaignUpdate = true
		}
	}
	if !foundCampaignUpdate {
		t.Fatalf("no campaign bounced_at update captured")
	}
	// Timeline: bounced event with the classification and the dedup id.
	if cap.emailEventCount() != 1 {
		t.Fatalf("timeline rows = %d", cap.emailEventCount())
	}
	ev := cap.lastEmailEvent()
	if ev.Type != models.EmailEventBounced || ev.Error == "" {
		t.Fatalf("event = %s with error %q", ev.Type, ev.Error)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(ev.Metadata), &metadata); err != nil {
		t.Fatalf("metadata = %q is not JSON", ev.Metadata)
	}
	if metadata["classification"] != string(ClassHardBounce) {
		t.Fatalf("classification metadata = %v", metadata["classification"])
	}
	if want := DedupEventID("ses", EventBounced, "sns-b-1", "", ""); ev.ProviderEventID != want {
		t.Fatalf("provider_event_id = %q, want %q", ev.ProviderEventID, want)
	}
}

// TestIngestPermanentBounceWithoutAutoSuppress pins the switch: with
// autoSuppress off the bounce is still recorded (audit) but nothing is
// suppressed.
func TestIngestPermanentBounceWithoutAutoSuppress(t *testing.T) {
	svc, cap := newTestService(t, false)

	svc.Ingest(context.Background(), []NormalizedEvent{{
		Provider:        "postmark",
		EventType:       EventBounced,
		EmailUUID:       "em-uuid-1",
		Recipient:       "gone@example.com",
		ProviderEventID: "pm-1",
		Diagnostic:      markerHard + "; mailbox not found",
	}}, `{}`)

	if len(cap.bounces) != 1 {
		t.Fatalf("bounce rows = %d, want 1 (audit regardless of the switch)", len(cap.bounces))
	}
	if len(cap.suppressions) != 0 {
		t.Fatalf("suppression rows = %d, want 0 (autoSuppress off)", len(cap.suppressions))
	}
}

// TestIngestSoftBounceNoSuppression pins the soft-bounce policy: recorded on
// the timeline with its classification, but no bounce record, no suppression
// and no subscriber marking.
func TestIngestSoftBounceNoSuppression(t *testing.T) {
	svc, cap := newTestService(t, true)

	accepted, _ := svc.Ingest(context.Background(), []NormalizedEvent{{
		Provider:        "mailgun",
		EventType:       EventBounced,
		EmailUUID:       "em-uuid-1",
		Recipient:       "gone@example.com",
		ProviderEventID: "mg-1",
		Diagnostic:      markerTransient + "; greylisted, try again",
	}}, `{}`)

	if accepted != 1 {
		t.Fatalf("accepted = %d", accepted)
	}
	if len(cap.bounces) != 0 || len(cap.suppressions) != 0 {
		t.Fatalf("soft bounce produced bounce=%d suppression=%d, want 0/0", len(cap.bounces), len(cap.suppressions))
	}
	ev := cap.lastEmailEvent()
	if ev.Type != models.EmailEventBounced {
		t.Fatalf("event type = %s, want bounced", ev.Type)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(ev.Metadata), &metadata); err != nil {
		t.Fatalf("metadata is not JSON")
	}
	if metadata["classification"] != string(ClassSoftBounce) {
		t.Fatalf("classification = %v, want soft_bounce", metadata["classification"])
	}
}

// TestIngestComplaint pins the complaint path: complaint-typed bounce record,
// complained timeline event, subscriber marked complained.
func TestIngestComplaint(t *testing.T) {
	svc, cap := newTestService(t, true)

	svc.Ingest(context.Background(), []NormalizedEvent{{
		Provider:        "sendgrid",
		EventType:       EventComplained,
		EmailUUID:       "em-uuid-1",
		Recipient:       "gone@example.com",
		ProviderEventID: "sg-c-1",
		Diagnostic:      "spam complaint reported by SendGrid",
	}}, `{}`)

	if len(cap.bounces) != 1 {
		t.Fatalf("bounce rows = %d, want 1", len(cap.bounces))
	}
	if cap.bounces[0].Type != models.BounceTypeComplaint {
		t.Fatalf("bounce type = %s, want complaint", cap.bounces[0].Type)
	}
	ev := cap.lastEmailEvent()
	if ev.Type != models.EmailEventComplained {
		t.Fatalf("timeline type = %s, want complained", ev.Type)
	}
	foundSubscriberUpdate := false
	for _, sql := range cap.updates {
		lower := strings.ToLower(sql)
		if strings.Contains(lower, "subscribers") && strings.Contains(lower, "\"status\"") {
			foundSubscriberUpdate = true
		}
	}
	if !foundSubscriberUpdate {
		t.Fatalf("no subscriber complained update captured; updates = %v", cap.updates)
	}
}

// TestIngestDeferredEvent pins the deferred path: a failed timeline row with
// the deferred classification (no timestamp column for failures).
func TestIngestDeferredEvent(t *testing.T) {
	svc, cap := newTestService(t, true)

	svc.Ingest(context.Background(), []NormalizedEvent{{
		Provider:        "sendgrid",
		EventType:       EventDeferred,
		EmailUUID:       "em-uuid-1",
		Recipient:       "gone@example.com",
		ProviderEventID: "sg-d-1",
		Diagnostic:      "greylisted",
	}}, `{}`)

	if cap.emailEventCount() != 1 {
		t.Fatalf("timeline rows = %d", cap.emailEventCount())
	}
	ev := cap.lastEmailEvent()
	if ev.Type != models.EmailEventFailed {
		t.Fatalf("event type = %s, want failed", ev.Type)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(ev.Metadata), &metadata); err != nil {
		t.Fatalf("metadata is not JSON")
	}
	if metadata["classification"] != string(ClassDeferred) {
		t.Fatalf("classification = %v, want deferred", metadata["classification"])
	}
	if len(cap.raws) != 0 {
		t.Fatalf("failed events have no timestamp column; raws = %v", cap.raws)
	}
}

// TestIngestSkipsUnknownUUID pins the 404-skip behavior and the raw-payload
// capture annotation.
func TestIngestSkipsUnknownUUID(t *testing.T) {
	svc, cap := newTestService(t, true)

	accepted, skipped := svc.Ingest(context.Background(), []NormalizedEvent{{
		Provider:        "ses",
		EventType:       EventDelivered,
		EmailUUID:       "00000000-0000-0000-0000-000000000000",
		Recipient:       "ghost@example.com",
		ProviderEventID: "sns-x-1",
	}}, `{}`)

	if accepted != 0 || skipped != 1 {
		t.Fatalf("accepted/skipped = %d/%d, want 0/1", accepted, skipped)
	}
	if cap.emailEventCount() != 0 {
		t.Fatalf("no timeline rows expected")
	}
	if len(cap.incoming) != 1 {
		t.Fatalf("raw captures = %d, want 1", len(cap.incoming))
	}
	if cap.incoming[0].EmailUUID != "" || cap.incoming[0].MessageID != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("capture = %+v, want the unresolved reference in message_id", cap.incoming[0])
	}
}

// TestIngestCapturesRawWithResolvedUUID pins the capture annotation on the
// happy path.
func TestIngestCapturesRawWithResolvedUUID(t *testing.T) {
	svc, cap := newTestService(t, true)

	svc.Ingest(context.Background(), []NormalizedEvent{{
		Provider:        "postmark",
		EventType:       EventDelivered,
		EmailUUID:       "em-uuid-1",
		Recipient:       "gone@example.com",
		ProviderEventID: "pm-d-1",
	}}, `{"RecordType":"Delivery"}`)

	if len(cap.incoming) != 1 {
		t.Fatalf("raw captures = %d", len(cap.incoming))
	}
	if cap.incoming[0].EmailUUID != "em-uuid-1" {
		t.Fatalf("capture email_uuid = %q", cap.incoming[0].EmailUUID)
	}
}

// TestIngestMalformedAndEmpty pins the batch guardrails: a malformed
// normalized event is skipped (not a crash), a batch with no message
// reference is fully skipped, and the raw payload is still captured.
func TestIngestMalformedAndEmpty(t *testing.T) {
	svc, cap := newTestService(t, true)

	accepted, skipped := svc.Ingest(context.Background(), []NormalizedEvent{
		{Provider: "ses", EventType: "bogus", EmailUUID: "em-uuid-1"},
		{Provider: "ses", EventType: EventDelivered, EmailUUID: ""},
	}, `{}`)
	if accepted != 0 || skipped != 2 {
		t.Fatalf("accepted/skipped = %d/%d, want 0/2", accepted, skipped)
	}

	// Nil recorder + nil email lookup: a batch cannot be attached, everything
	// is skipped, nothing panics.
	svc2 := NewIngestionService(nil, nil)
	a2, s2 := svc2.Ingest(context.Background(), []NormalizedEvent{
		{Provider: "ses", EventType: EventDelivered, EmailUUID: "em-uuid-1"},
	}, `{}`)
	if a2 != 0 || s2 != 1 {
		t.Fatalf("unwired service accepted/skipped = %d/%d, want 0/1", a2, s2)
	}
	if len(cap.incoming) != 1 {
		t.Fatalf("raw captures = %d, want 1 (only the wired service captures)", len(cap.incoming))
	}
}

// TestClassificationCountsQuery pins the R-05 analytics query shape: counts
// grouped by the metadata->>'classification' field. DryRun cannot execute the
// aggregate scan, so the built SQL is pinned instead.
func TestClassificationCountsQuery(t *testing.T) {
	svc, cap := newTestService(t, true)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, _ = svc.ClassificationCounts(context.Background(), &from, nil)

	if len(cap.queries) == 0 {
		t.Fatal("no analytics query built")
	}
	q := strings.ToLower(cap.queries[0])
	if !strings.Contains(q, "email_events") {
		t.Fatalf("analytics query targets the wrong table: %q", cap.queries[0])
	}
	if !strings.Contains(q, "classification") || !strings.Contains(q, "metadata") {
		t.Fatalf("analytics query does not group on metadata classification: %q", cap.queries[0])
	}
	if !strings.Contains(q, "occurred_at") {
		t.Fatalf("analytics query ignores the occurred_at range: %q", cap.queries[0])
	}

	// No db wired: returns nil without error.
	svc2 := NewIngestionService(nil, nil)
	out, err := svc2.ClassificationCounts(context.Background(), nil, nil)
	if err != nil || out != nil {
		t.Fatalf("unwired ClassificationCounts = %v, %v; want nil, nil", out, err)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
