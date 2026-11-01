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

package security

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/jkaninda/logger"
)

// loginFailedEvent is the event type the auth handler publishes on every
// failed login (see handlers.UserHandler.publishLoginFailed).
const loginFailedEvent = "user.login_failed"

// EventPublisher is the event bus surface the security engine consumes.
// *eventbus.EventBus satisfies it — the category parameter is
// models.EventCategory, matching the concrete PublishSimple signature
// exactly (Go interface satisfaction requires identical parameter types).
type EventPublisher interface {
	PublishSimple(category models.EventCategory, eventType string, actorID *uint, actorName, clientIP, message string, meta map[string]any)
	Subscribe() (<-chan *models.Event, func())
}

// Compile-time guarantee that the live event bus can back the Feed and the
// Engine's EventPublisher parameter.
var _ EventPublisher = (*eventbus.EventBus)(nil)

// Feed turns user login-failure events from the event bus into auth-failure
// signals on the detector, so brute force against the dashboard is scored by
// the same engine as SMTP abuse. It is a thin consumer: everything else is
// ignored, errors are logged rate-limited, and shutdown is ctx-driven.
type Feed struct {
	bus     EventPublisher
	det     *Detector
	limiter *logThrottle
}

// NewFeed returns a Feed consuming from bus and feeding det.
func NewFeed(bus EventPublisher, det *Detector) *Feed {
	return &Feed{bus: bus, det: det, limiter: newLogThrottle(30 * time.Second)}
}

// Start subscribes to the event bus and forwards failed logins to the
// detector until ctx is cancelled or the bus channel closes. It blocks, so
// the Engine runs it alongside its other background loops.
func (f *Feed) Start(ctx context.Context) error {
	if f == nil || f.bus == nil || f.det == nil {
		return nil
	}

	ch, unsub := f.bus.Subscribe()
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			return nil
		case evt, ok := <-ch:
			if !ok {
				return nil
			}
			f.handle(evt)
		}
	}
}

// handle forwards one user.login_failed event to the detector. The actor
// name is the attempted login (email); the reason and client IP come from
// the event's fields, mirroring handlers.UserHandler.publishLoginFailed.
func (f *Feed) handle(evt *models.Event) {
	if evt == nil || evt.Category != models.EventCategoryUser || evt.Type != loginFailedEvent {
		return
	}

	var meta map[string]any
	if evt.Metadata != "" {
		if err := json.Unmarshal([]byte(evt.Metadata), &meta); err != nil {
			meta = nil
		}
	}
	reason, _ := meta["reason"].(string)

	ip, err := netip.ParseAddr(strings.TrimSpace(evt.ClientIP))
	if err != nil || !ip.IsValid() {
		if f.limiter.allow("security.feed.client_ip", time.Now()) {
			logger.Warn("security feed: login_failed event without a parseable client IP",
				"client_ip", evt.ClientIP, "reason", reason)
		}
		return
	}

	// The detector normalizes again; feed lower-cased so the per-login
	// window key is stable across case variants of the same address.
	login := strings.ToLower(strings.TrimSpace(evt.ActorName))
	if err := f.det.RecordAuthFailure(context.Background(), ip, login); err != nil {
		if f.limiter.allow("security.feed.record", time.Now()) {
			logger.Warn("security feed: failed to record auth failure", "error", err)
		}
	}
}
