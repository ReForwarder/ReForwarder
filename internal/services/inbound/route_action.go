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

package inbound

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/jkaninda/logger"
)

// Post-ingest route-action execution (OB-2, ADR-002 decision 1). Every action
// runs AFTER the message is durably persisted — the ADR's non-negotiable
// "never accept inbound without a durable path" — so a failed action can
// never lose a message that was accepted. The matched route itself is stamped
// onto the inbound row at persist time (RouteID/RouteAction), so the decision
// travels with the record.
//
// Inbound audit events (W8 audit trail starts here): route.matched on any
// match, inbound.rejected on the explicit no-route reject,
// inbound.forward_queued and inbound.quarantined per action — published into
// the events substrate (eventbus → events table).

// defaultRouteWebhookEvent is the event a webhook route dispatches when its
// target is empty — the established inbound webhook event the parse worker
// already uses (internal/worker/inbound_handler.go).
const defaultRouteWebhookEvent = "email.inbound"

// WebhookDispatcher is the seam to the existing webhook machinery for the
// route action=webhook. *webhook.Dispatcher satisfies it; the dispatcher
// owns HMAC signing, SSRF guarding, retries and delivery records.
type WebhookDispatcher interface {
	DispatchJSON(userID uint, workspaceID *uint, event string, body []byte, fromAddr string)
}

// QuarantineStore is the seam the quarantine wave (W3) fills: persist one
// quarantine row for a message routed to quarantine. A nil seam leaves the
// inbound row's quarantined status as the only record (documented gap until
// W3 wires the store).
type QuarantineStore interface {
	Quarantine(ctx context.Context, rec *models.InboundEmail, route *models.InboundRoute, reason string) error
}

// ForwardTaskQueue is the durable forwarding queue seam (ADR-002 decision 2).
// *repositories.InboundForwardTaskRepository satisfies it. The forwarding
// executor (wave 2) consumes due rows; this wave only queues.
type ForwardTaskQueue interface {
	Enqueue(task *models.InboundForwardTask) error
}

// InboundRouteWebhookPayload is the body POSTed for a route action=webhook.
// At ingest time only envelope facts are known on the SMTP path (parsing is
// asynchronous), so the payload carries the durable record's identity and
// envelope; consumers fetch the full message. Shape follows the parse
// worker's InboundWebhookPayload field naming.
type InboundRouteWebhookPayload struct {
	Event      string   `json:"event"`
	Timestamp  string   `json:"timestamp"`
	InboundID  string   `json:"inbound_id"`
	From       string   `json:"from"`
	To         []string `json:"to"`
	Size       int64    `json:"size"`
	MessageID  string   `json:"message_id,omitempty"`
	Subject    string   `json:"subject,omitempty"`
	Source     string   `json:"source"`
	RouteID    uint     `json:"route_id"`
	RouteMatch string   `json:"route_match,omitempty"`
	ReceivedAt string   `json:"received_at"`
}

// applyRouteActions executes the matched route's action for a durably
// persisted inbound record. A nil route means routing is disabled or the
// recipient was accepted without a rule (pre-ADR behavior) — nothing runs.
// It never fails the ingestion: action errors are logged, the durable record
// stands.
func (s *Service) applyRouteActions(ctx context.Context, rec *models.InboundEmail, route *models.InboundRoute, kind RouteMatchKind) {
	if s == nil || route == nil || rec == nil {
		return
	}
	s.publishInboundEvent("route.matched", rec, route, map[string]any{
		"route_match": string(kind),
	})

	switch route.Action {
	case models.InboundRouteActionDeliver:
		// Today's archive/thread behavior: the durable record + the regular
		// parse/notify pipeline that the caller already enqueues. No extra
		// side effect.
	case models.InboundRouteActionForward:
		s.queueForward(rec, route)
	case models.InboundRouteActionWebhook:
		s.dispatchRouteWebhook(rec, route, kind)
	case models.InboundRouteActionQuarantine:
		s.quarantineInbound(ctx, rec, route)
	default:
		logger.Warn("inbound route has unknown action; treating as deliver",
			"inbound_id", rec.UUID, "action", string(route.Action))
	}
}

// queueForward enqueues the durable forwarding task row for the forwarding
// executor (wave 2). Idempotent per inbound message (unique inbound_email_id,
// INSERT ... ON CONFLICT DO NOTHING).
func (s *Service) queueForward(rec *models.InboundEmail, route *models.InboundRoute) {
	if s.forwardTasks == nil {
		// Wiring always attaches the queue when routing is enabled; reaching
		// this means a deployment skipped the wiring — the message stays
		// durably stored and the gap is logged, never silent.
		logger.Error("inbound forward route matched but no forward queue is wired",
			"inbound_id", rec.UUID, "route_id", route.ID, "target", route.Target)
		return
	}
	task := &models.InboundForwardTask{
		UserID:         rec.UserID,
		WorkspaceID:    rec.WorkspaceID,
		DomainID:       rec.DomainID,
		InboundEmailID: rec.ID,
		RouteID:        route.ID,
		Target:         route.Target,
		Status:         models.InboundForwardTaskPending,
		Attempts:       0,
		NextAttemptAt:  time.Now().UTC(),
	}
	if err := s.forwardTasks.Enqueue(task); err != nil {
		// The original is durable; the forward retry story belongs to the
		// executor wave. Log loudly — this is an operator-visible gap.
		logger.Error("failed to enqueue inbound forward task",
			"inbound_id", rec.UUID, "route_id", route.ID, "error", err)
		return
	}
	s.publishInboundEvent("inbound.forward_queued", rec, route, map[string]any{
		"target":  route.Target,
		"task_id": task.ID,
	})
}

// dispatchRouteWebhook POSTs the message to the workspace's registered
// webhooks subscribed to the route's target event, through the existing
// webhook Dispatcher (HMAC signing + SSRF guard + retries inside it).
func (s *Service) dispatchRouteWebhook(rec *models.InboundEmail, route *models.InboundRoute, kind RouteMatchKind) {
	if s.webhooks == nil {
		logger.Error("inbound webhook route matched but no webhook dispatcher is wired",
			"inbound_id", rec.UUID, "route_id", route.ID)
		return
	}
	event := strings.TrimSpace(route.Target)
	if event == "" {
		event = defaultRouteWebhookEvent
	}
	payload := InboundRouteWebhookPayload{
		Event:      event,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		InboundID:  rec.UUID,
		From:       rec.Sender,
		To:         []string(rec.Recipients),
		Size:       rec.Size,
		MessageID:  rec.MessageID,
		Subject:    rec.Subject,
		Source:     string(rec.Source),
		RouteID:    route.ID,
		RouteMatch: string(kind),
		ReceivedAt: rec.ReceivedAt.UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("failed to marshal inbound route webhook payload",
			"inbound_id", rec.UUID, "route_id", route.ID, "error", err)
		return
	}
	s.webhooks.DispatchJSON(rec.UserID, rec.WorkspaceID, event, body, rec.Sender)
}

// quarantineInbound moves the durably stored message into review: the inbound
// row flips to the quarantined status and the quarantine row is created via
// the QuarantineStore seam (wired by the quarantine wave).
func (s *Service) quarantineInbound(ctx context.Context, rec *models.InboundEmail, route *models.InboundRoute) {
	rec.Status = models.InboundStatusQuarantined
	if err := s.repo.Update(rec); err != nil {
		logger.Error("failed to mark inbound email quarantined",
			"inbound_id", rec.UUID, "route_id", route.ID, "error", err)
		return
	}
	if s.quarantine != nil {
		if err := s.quarantine.Quarantine(ctx, rec, route, "matched quarantine route"); err != nil {
			logger.Error("failed to create quarantine row",
				"inbound_id", rec.UUID, "route_id", route.ID, "error", err)
		}
	}
	s.publishInboundEvent("inbound.quarantined", rec, route, nil)
}

// publishInboundEvent publishes an inbound routing event into the events
// substrate. A nil route (the explicit reject path) publishes without route
// fields; a nil bus is a no-op.
func (s *Service) publishInboundEvent(eventType string, rec *models.InboundEmail, route *models.InboundRoute, extra map[string]any) {
	if s == nil || s.bus == nil || rec == nil {
		return
	}
	meta := map[string]any{
		"inbound_id":   rec.UUID,
		"workspace_id": rec.WorkspaceID,
		"sender":       rec.Sender,
		"recipients":   []string(rec.Recipients),
	}
	if route != nil {
		meta["route_id"] = route.ID
		meta["route_action"] = string(route.Action)
		meta["route_pattern"] = route.Pattern
	}
	for k, v := range extra {
		meta[k] = v
	}
	actor := rec.UserID
	s.bus.PublishScoped(
		rec.WorkspaceID,
		models.EventCategoryEmail,
		eventType,
		&actor,
		"",
		"",
		"Inbound "+eventType+" for "+rec.Sender,
		meta,
	)
}
