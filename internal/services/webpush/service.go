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

package webpush

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jkaninda/logger"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/realtime"
	"github.com/goposta/posta/internal/storage/repositories"
)

// PushTTL tells the push service how long an undelivered message stays queued
// for a disconnected browser (RFC 8030 TTL header). Posta's events are
// refetch hints, not records — a short window keeps stale hints from piling up.
const PushTTL = 5 * time.Minute

// ErrGone marks a subscription the push service no longer knows (404/410).
var ErrGone = errors.New("webpush: subscription gone")

// Service sends RFC 8030 Web Push messages for the configured VAPID identity.
type Service struct {
	key     *vapidKeyPair
	subject string
	client  *http.Client
}

// Config carries the configured VAPID identity.
type Config struct {
	PublicKey  string // POSTA_WEB_PUSH_VAPID_PUBLIC_KEY (base64url, 65 bytes)
	PrivateKey string // POSTA_WEB_PUSH_VAPID_PRIVATE_KEY (base64url, 32 bytes)
	Subject    string // POSTA_WEB_PUSH_SUBJECT (mailto: or https: contact)
}

// New builds a Service; returns an error when the VAPID keys are missing or
// malformed — callers enable push only when this succeeds.
func New(cfg Config) (*Service, error) {
	if cfg.PrivateKey == "" {
		return nil, fmt.Errorf("webpush: POSTA_WEB_PUSH_VAPID_PRIVATE_KEY is required when web push is enabled")
	}
	if cfg.Subject == "" {
		cfg.Subject = "mailto:admin@example.com"
	}
	key, err := loadVAPIDKey(cfg.PublicKey, cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &Service{
		key:     key,
		subject: cfg.Subject,
		client:  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// PublicKey returns the base64url VAPID public key the frontend passes to
// PushManager.subscribe.
func (s *Service) PublicKey() string {
	return base64.RawURLEncoding.EncodeToString(s.key.publicRaw)
}

// Notification is the JSON payload pushed to the browser (the service worker
// turns it into a notification; the dashboard refetches on click).
type Notification struct {
	Type        string `json:"type"`
	WorkspaceID uint   `json:"workspace_id,omitempty"`
	Message     string `json:"message"`
}

// Send delivers one notification to one stored subscription (RFC 8030): the
// body is RFC 8291 aes128gcm-encrypted, the request is RFC 8292 VAPID-signed.
// A 404/410 from the push service reports the subscription dead (ErrGone) so
// the caller can delete it.
func (s *Service) Send(sub *models.UserPushSubscription, n Notification) error {
	if sub == nil {
		return fmt.Errorf("webpush: nil subscription")
	}
	uaPub, err := decodeP256DH(sub.P256DH)
	if err != nil {
		return err
	}
	auth, err := decodeSecret(sub.Auth)
	if err != nil {
		return err
	}
	body, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("webpush: marshal notification: %w", err)
	}
	payload, err := encodeAES128GCM(uaPub, auth, body)
	if err != nil {
		return err
	}
	authz, err := s.key.vapidAuthorization(sub.Endpoint, s.subject)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("webpush: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", fmt.Sprintf("%d", int(PushTTL.Seconds())))
	req.Header.Set("Authorization", authz)
	req.Header.Set("User-Agent", "Posta-WebPush/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("webpush: POST: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == 404 || resp.StatusCode == 410:
		return fmt.Errorf("webpush: push service answered HTTP %d: %w", resp.StatusCode, ErrGone)
	default:
		return fmt.Errorf("webpush: push service returned HTTP %d", resp.StatusCode)
	}
}

// decodeP256DH decodes the subscription's p256dh key (base64url, with or
// without padding — browsers differ).
func decodeP256DH(p256dh string) (*ecdh.PublicKey, error) {
	raw, err := decodeSecret(p256dh)
	if err != nil {
		return nil, fmt.Errorf("webpush: subscription p256dh key: %w", err)
	}
	pub, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("webpush: subscription p256dh is not a valid P-256 point: %w", err)
	}
	return pub, nil
}

// decodeSecret decodes a base64url subscription secret, accepting padded and
// unpadded forms.
func decodeSecret(s string) ([]byte, error) {
	for len(s) > 0 && s[len(s)-1] == '=' {
		s = s[:len(s)-1]
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("webpush: not base64url: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("webpush: empty secret")
	}
	return raw, nil
}

// Fanout wires the push service into the realtime bus: it is a decorator Bus
// that delegates everything to the wrapped bus and, additionally, fans every
// published `message.*` event out to the Web Push subscriptions of that
// workspace's members. Delivery is best effort and asynchronous — exactly the
// realtime package's contract ("an event is only a hint to refetch").
type Fanout struct {
	realtime.Bus
	svc  *Service
	repo *repositories.UserPushSubscriptionRepository
}

// NewFanout wraps bus so published dashboard events also go out as Web Push.
func NewFanout(bus realtime.Bus, svc *Service, repo *repositories.UserPushSubscriptionRepository) *Fanout {
	return &Fanout{Bus: bus, svc: svc, repo: repo}
}

// Publish delegates to the wrapped bus and asynchronously pushes the event to
// workspace members' browsers.
func (f *Fanout) Publish(ctx context.Context, ev realtime.Event) error {
	err := f.Bus.Publish(ctx, ev)
	f.pushAsync(ev)
	return err
}

// pushAsync delivers one event to every push subscriber of the event's
// workspace. Failures are logged and a gone subscription is removed.
func (f *Fanout) pushAsync(ev realtime.Event) {
	// SF-89 minimal scope: only the dashboard message stream is pushed;
	// other topics stay SSE-only.
	if !isPushableEvent(ev) || ev.WorkspaceID == 0 {
		return
	}
	go func() {
		subs, err := f.repo.FindForWorkspace(ev.WorkspaceID)
		if err != nil {
			logger.Warn("webpush: cannot list workspace subscriptions", "error", err)
			return
		}
		notification := Notification{Type: ev.Type, WorkspaceID: ev.WorkspaceID, Message: ev.Topic}
		for i := range subs {
			sub := &subs[i]
			pushErr := f.svc.Send(sub, notification)
			switch {
			case pushErr == nil:
				// delivered
			case errors.Is(pushErr, ErrGone):
				if delErr := f.repo.DeleteByEndpoint(sub.Endpoint); delErr != nil {
					logger.Warn("webpush: cannot remove gone subscription", "error", delErr)
				}
			default:
				logger.Warn("webpush: delivery failed", "type", ev.Type, "error", pushErr)
			}
		}
	}()
}

// isPushableEvent reports whether a realtime event should also surface as a
// browser push notification.
func isPushableEvent(ev realtime.Event) bool {
	prefix := "message."
	return len(ev.Type) >= len(prefix) && ev.Type[:len(prefix)] == prefix
}
