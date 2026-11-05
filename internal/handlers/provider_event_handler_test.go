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
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/provider_events"
	"github.com/jkaninda/okapi"
)

// The provider webhook handler is exercised through the real okapi stack (the
// admin_queue_test.go arrangement). Auth between the route and the handler is
// the API-key middleware (covered by the middleware's own tests and wired in
// tracking_routes.go); these tests pin the handler contract itself: provider
// dispatch, the 501-when-unconfigured rule, signature verification over the
// byte-exact body, and the 202 {accepted, skipped} response shape.

// stubProviderIngestor records what the handler handed over.
type stubProviderIngestor struct {
	events  []provider_events.NormalizedEvent
	raw     string
	ingests int
}

func (s *stubProviderIngestor) Ingest(_ context.Context, events []provider_events.NormalizedEvent, rawPayload string) (int, int) {
	s.ingests++
	s.events = events
	s.raw = rawPayload
	return len(events), 0
}

// newProviderEventServer builds the test server with the handler mounted the
// same way the route registration does (minus the API-key middleware, which
// needs live services).
func newProviderEventServer(t *testing.T, ingestor ProviderEventIngestor, secrets map[string]provider_events.ProviderConfig) *okapi.TestServer {
	t.Helper()
	o := okapi.NewTestServer(t)
	h := NewProviderEventHandler(ingestor)
	h.SetProviderSecrets(secrets)
	g := o.Group("/api/v1/webhooks/provider")
	g.Post("/{provider}", h.Handle)
	return o
}

func doProviderPost(t *testing.T, o *okapi.TestServer, provider string, headers map[string]string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/provider/"+provider, strings.NewReader(string(body)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

// sesCanonicalSignature computes the SNS SignatureVersion 1 HMAC by hand so
// the canonical string is pinned by the test, not the implementation.
func sesCanonicalSignature(secret, message, messageID, timestamp, topicArn string) string {
	base := "Message\n" + message + "\nMessageId\n" + messageID + "\nTimestamp\n" + timestamp + "\nTopicArn\n" + topicArn + "\nType\nNotification\n"
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(base))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestProviderEventHandlerUnknownProvider(t *testing.T) {
	o := newProviderEventServer(t, &stubProviderIngestor{}, map[string]provider_events.ProviderConfig{})
	rec := doProviderPost(t, o, "sparkpost", nil, []byte(`{}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d, want 404", rec.Code)
	}
}

func TestProviderEventHandlerUnconfiguredReturns501(t *testing.T) {
	// Only SES is configured; sendgrid exists as an adapter but has no secret.
	o := newProviderEventServer(t, &stubProviderIngestor{}, map[string]provider_events.ProviderConfig{
		"ses": {Secret: "secret"},
	})
	rec := doProviderPost(t, o, "sendgrid", nil, []byte(`[]`))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured provider status = %d, want 501", rec.Code)
	}
}

func TestProviderEventHandlerBadSignature401(t *testing.T) {
	ingestor := &stubProviderIngestor{}
	o := newProviderEventServer(t, ingestor, map[string]provider_events.ProviderConfig{
		"ses": {Secret: "secret"},
	})
	rec := doProviderPost(t, o, "ses", map[string]string{"Content-Type": "application/json"}, []byte(`{"Type":"Notification"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature status = %d, want 401", rec.Code)
	}
	if ingestor.ingests != 0 {
		t.Fatal("handler ingested despite failed signature")
	}
}

func TestProviderEventHandlerSESNotification(t *testing.T) {
	ingestor := &stubProviderIngestor{}
	o := newProviderEventServer(t, ingestor, map[string]provider_events.ProviderConfig{
		"ses": {Secret: "sns-secret"},
	})

	message := `{"eventType":"Bounce","mail":{"messageId":"ses-mid-1","destination":["gone@example.com"],"timestamp":"2026-05-01T09:59:59Z","headers":[{"name":"X-Posta-ID","value":"em-uuid-1"}]},"bounce":{"bounceType":"Permanent","bounceSubType":"General","bouncedRecipients":[{"emailAddress":"gone@example.com","diagnosticCode":"smtp; 550 5.1.1 user unknown"}],"timestamp":"2026-05-01T10:00:00Z"}}`
	env := map[string]any{
		"Type":             "Notification",
		"MessageId":        "sns-1",
		"TopicArn":         "arn:aws:sns:us-east-1:1:posta",
		"Timestamp":        "2026-05-01T10:00:00.000Z",
		"SignatureVersion": "1",
		"Signature":        sesCanonicalSignature("sns-secret", message, "sns-1", "2026-05-01T10:00:00.000Z", "arn:aws:sns:us-east-1:1:posta"),
		"Message":          message,
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	rec := doProviderPost(t, o, "ses", map[string]string{"Content-Type": "application/json"}, raw)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	if ingestor.ingests != 1 {
		t.Fatalf("ingests = %d, want 1", ingestor.ingests)
	}
	if len(ingestor.events) != 1 || ingestor.events[0].EventType != provider_events.EventBounced {
		t.Fatalf("normalized events = %+v", ingestor.events)
	}
	if !strings.Contains(ingestor.raw, "sns-1") {
		t.Fatal("raw payload not passed through to the ingestor")
	}
	var body struct {
		Accepted int `json:"accepted"`
		Skipped  int `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body = %q is not the {accepted, skipped} shape", rec.Body.String())
	}
	if body.Accepted != 1 || body.Skipped != 0 {
		t.Fatalf("response = %+v, want accepted=1 skipped=0", body)
	}
}

func TestProviderEventHandlerSendGridMalformedBody400(t *testing.T) {
	ingestor := &stubProviderIngestor{}
	o := newProviderEventServer(t, ingestor, map[string]provider_events.ProviderConfig{
		"sendgrid": {Secret: "sg-key"},
	})
	body := []byte(`{"not":"an array"}`)
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, []byte("sg-key"))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)

	headers := map[string]string{
		"X-SendGrid-Signature-Timestamp": ts,
		"X-SendGrid-Signature":           base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	}
	rec := doProviderPost(t, o, "sendgrid", headers, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed payload status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if ingestor.ingests != 0 {
		t.Fatal("handler ingested a malformed payload")
	}
}

func TestProviderEventHandlerPostmarkAndMailgun202(t *testing.T) {
	ingestor := &stubProviderIngestor{}
	o := newProviderEventServer(t, ingestor, map[string]provider_events.ProviderConfig{
		"postmark": {Secret: "pm-token"},
		"mailgun":  {Secret: "mg-key"},
	})

	// Postmark: token header.
	rec := doProviderPost(t, o, "postmark", map[string]string{
		"X-Postmark-Webhook": "pm-token",
		"Content-Type":       "application/json",
	}, []byte(`{"RecordType":"Delivery","ID":7,"Recipient":"ok@example.com","DeliveredAt":"2026-05-01T10:01:00Z","Metadata":{"email_uuid":"em-uuid-2"}}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("postmark status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	if len(ingestor.events) != 1 || ingestor.events[0].EventType != provider_events.EventDelivered || ingestor.events[0].Provider != "postmark" {
		t.Fatalf("postmark events = %+v", ingestor.events)
	}

	// Mailgun: HMAC of timestamp+token.
	now := fmt.Sprintf("%d", time.Now().Unix())
	token := "tok-1"
	mac := hmac.New(sha256.New, []byte("mg-key"))
	mac.Write([]byte(now))
	mac.Write([]byte(token))
	form := "event=delivered&recipient=ok%40example.com&message-id=em-uuid-2%40posta.local&timestamp=" + now + "&token=" + token + "&signature=" + fmt.Sprintf("%x", mac.Sum(nil))
	rec = doProviderPost(t, o, "mailgun", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, []byte(form))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("mailgun status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	if len(ingestor.events) != 1 || ingestor.events[0].EventType != provider_events.EventDelivered || ingestor.events[0].Provider != "mailgun" {
		t.Fatalf("mailgun events = %+v", ingestor.events)
	}
	if ingestor.events[0].EmailUUID != "em-uuid-2" {
		t.Fatalf("mailgun email uuid = %q, want em-uuid-2 from the message-id", ingestor.events[0].EmailUUID)
	}
}

func TestProviderEventHandlerEmptyBatchStill202(t *testing.T) {
	// A payload the adapter accepts but that carries no events (e.g. a Send
	// notification) is a 202 with zero counts, not an error.
	ingestor := &stubProviderIngestor{}
	o := newProviderEventServer(t, ingestor, map[string]provider_events.ProviderConfig{
		"postmark": {Secret: "pm-token"},
	})
	rec := doProviderPost(t, o, "postmark", map[string]string{
		"X-Postmark-Webhook": "pm-token",
		"Content-Type":       "application/json",
	}, []byte(`{"RecordType":"SubscriptionChange","Email":"x@example.com"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("empty batch status = %d, want 202", rec.Code)
	}
	var body struct {
		Accepted int `json:"accepted"`
		Skipped  int `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body = %q", rec.Body.String())
	}
	if body.Accepted != 0 || body.Skipped != 0 {
		t.Fatalf("response = %+v, want zeros", body)
	}
	if len(ingestor.events) != 0 {
		t.Fatalf("events = %+v", ingestor.events)
	}
}
