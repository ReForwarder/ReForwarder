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
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

type WebhookDeliveryHandler struct {
	repo *repositories.WebhookDeliveryRepository
}

func NewWebhookDeliveryHandler(repo *repositories.WebhookDeliveryRepository) *WebhookDeliveryHandler {
	return &WebhookDeliveryHandler{repo: repo}
}

func (h *WebhookDeliveryHandler) List(c *okapi.Context, req *ListRequest) error {
	page, size, offset := normalizePageParams(req.Page, req.Size)

	deliveries, total, err := h.repo.FindByScope(getScope(c), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list webhook deliveries")
	}
	sanitizeDeliveries(deliveries)

	return paginated(c, deliveries, total, page, size)
}

// sanitizeDeliveries rewrites every delivery's raw error message in place to
// the safe user-facing form. Rows written before the SSRF guard existed can
// carry internal IPs and ports in their error text; deliveries are sanitized
// on the way out so legacy history is cleaned without being destroyed.
func sanitizeDeliveries(deliveries []models.WebhookDelivery) {
	for i := range deliveries {
		deliveries[i].ErrorMessage = webhook.SanitizeDeliveryError(deliveries[i].ErrorMessage)
	}
}

// sanitizeAttempts rewrites every attempt's raw error in place and redacts
// the captured response body when the owning webhook's stored URL may have
// reached a non-public destination (rows written before the SSRF guard).
// urlLookup maps a delivery ID to its webhook's stored URL; an unknown URL
// fails safe and the body is redacted.
func sanitizeAttempts(attempts []models.WebhookDeliveryAttempt, urlLookup func(uint) string) {
	for i := range attempts {
		a := &attempts[i]
		a.Error = webhook.SanitizeDeliveryError(a.Error)
		if webhook.LegacyAttemptBodyUnsafe(urlLookup(a.DeliveryID)) {
			a.ResponseBody = ""
		}
	}
}
