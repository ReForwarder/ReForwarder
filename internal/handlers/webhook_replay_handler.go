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
	"fmt"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// maxReplaysPerHour caps the per-delivery replay abuse (R-06): at most five
// operator-initiated replays of one delivery within a rolling hour, counted
// from the delivery's attempt rows (rows numbered above the original
// delivery's attempt count).
const maxReplaysPerHour = 5

// WebhookReplayHandler serves the R-06 webhook delivery replay endpoint.
type WebhookReplayHandler struct {
	deliveryRepo *repositories.WebhookDeliveryRepository
	attemptRepo  *repositories.WebhookDeliveryAttemptRepository
	webhookRepo  *repositories.WebhookRepository
	dispatcher   *webhook.Dispatcher
	audit        *audit.Logger
}

func NewWebhookReplayHandler(
	deliveryRepo *repositories.WebhookDeliveryRepository,
	attemptRepo *repositories.WebhookDeliveryAttemptRepository,
	webhookRepo *repositories.WebhookRepository,
	dispatcher *webhook.Dispatcher,
	auditLogger *audit.Logger,
) *WebhookReplayHandler {
	return &WebhookReplayHandler{
		deliveryRepo: deliveryRepo,
		attemptRepo:  attemptRepo,
		webhookRepo:  webhookRepo,
		dispatcher:   dispatcher,
		audit:        auditLogger,
	}
}

type ReplayWebhookDeliveryRequest struct {
	ID int `param:"id"`
}

// WebhookReplayResponse mirrors the fields the delivery list renders, so the
// UI can patch the row in place after a replay.
type WebhookReplayResponse struct {
	ID             uint                         `json:"id"`
	WebhookID      uint                         `json:"webhook_id"`
	Status         models.WebhookDeliveryStatus `json:"status"`
	HTTPStatusCode int                          `json:"http_status_code"`
	Attempt        int                          `json:"attempt"`
	ErrorMessage   string                       `json:"error_message,omitempty"`
	Replay         *webhook.ReplayResult        `json:"replay"`
}

// Replay re-delivers a failed webhook delivery with its original payload.
//
// The re-send goes through the dispatcher's guarded post() path — the same
// static SSRF gate and guarded/proxied transport as live deliveries, with no
// bypass — and records one attempt row referencing the SAME delivery with
// attempt = prior max + 1. The delivery's outcome fields are updated in place.
func (h *WebhookReplayHandler) Replay(c *okapi.Context, req *ReplayWebhookDeliveryRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}

	delivery, err := h.deliveryRepo.FindByID(uint(req.ID))
	if err != nil {
		return c.AbortNotFound("webhook delivery not found")
	}
	// Ownership: the delivery must belong to the caller's scope.
	if !ownsResource(c, delivery.UserID, delivery.WorkspaceID) {
		return c.AbortNotFound("webhook delivery not found")
	}

	if delivery.Status != models.WebhookDeliveryFailed {
		return c.AbortBadRequest("only failed deliveries can be replayed")
	}
	if delivery.Payload == "" {
		// Payload persistence shipped with replay; legacy deliveries have no
		// stored body, and reconstructing one would deliver a fabricated
		// payload — refuse instead.
		return c.AbortBadRequest("the original payload was not stored for this delivery; it cannot be replayed")
	}

	wh, err := h.webhookRepo.FindByID(delivery.WebhookID)
	if err != nil || wh == nil {
		return c.AbortBadRequest("the webhook for this delivery no longer exists")
	}
	// The webhook must still belong to the same scope as the delivery.
	if !ownsResource(c, wh.UserID, wh.WorkspaceID) {
		return c.AbortNotFound("webhook delivery not found")
	}

	// Per-delivery hourly cap: every replay appends exactly one attempt row
	// numbered above the original delivery's attempt count, so counting those
	// rows counts replay calls.
	replays, err := h.attemptRepo.CountReplaysSince(delivery.ID, delivery.Attempt, time.Now().Add(-time.Hour))
	if err != nil {
		return c.AbortInternalServerError("failed to check the replay limit")
	}
	if replays >= maxReplaysPerHour {
		return c.AbortTooManyRequests("replay limit reached (5 per hour for this delivery)")
	}

	result, err := h.dispatcher.ReplayDelivery(*wh, delivery, []byte(delivery.Payload))
	if err != nil {
		return c.AbortInternalServerError("failed to replay webhook delivery")
	}

	if h.audit != nil {
		h.audit.LogCtx(c, "webhook.delivery.replayed",
			fmt.Sprintf("replayed webhook delivery #%d (event %s) — outcome %s (HTTP %d)", delivery.ID, delivery.Event, result.Status, result.StatusCode),
			map[string]any{
				"delivery_id":      delivery.ID,
				"webhook_id":       delivery.WebhookID,
				"event":            delivery.Event,
				"replay_attempt":   result.Attempt,
				"outcome":          string(result.Status),
				"http_status_code": result.StatusCode,
			})
	}

	return ok(c, WebhookReplayResponse{
		ID:             delivery.ID,
		WebhookID:      delivery.WebhookID,
		Status:         result.Status,
		HTTPStatusCode: result.StatusCode,
		Attempt:        delivery.Attempt,
		Replay:         result,
	})
}
