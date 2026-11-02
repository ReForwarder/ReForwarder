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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
)

// BounceWebhookHandler processes inbound bounce notifications.
type BounceWebhookHandler struct {
	subscriberRepo *repositories.SubscriberRepository
	emailRepo      *repositories.EmailRepository
	messageRepo    *repositories.CampaignMessageRepository
	incomingRepo   *repositories.IncomingWebhookEventRepository
	recorder       *emailhistory.Recorder
}

func NewBounceWebhookHandler(
	subscriberRepo *repositories.SubscriberRepository,
	emailRepo *repositories.EmailRepository,
	messageRepo *repositories.CampaignMessageRepository,
) *BounceWebhookHandler {
	return &BounceWebhookHandler{
		subscriberRepo: subscriberRepo,
		emailRepo:      emailRepo,
		messageRepo:    messageRepo,
	}
}

// SetIncomingWebhookRepo wires the store for raw bounce payloads, captured
// before processing. Nil (the default) skips the capture.
func (h *BounceWebhookHandler) SetIncomingWebhookRepo(r *repositories.IncomingWebhookEventRepository) {
	h.incomingRepo = r
}

// SetRecorder wires the message-history event recorder so bounces of known
// messages land on the timeline. Nil (the default) disables that recording.
func (h *BounceWebhookHandler) SetRecorder(r *emailhistory.Recorder) {
	h.recorder = r
}

type BounceNotification struct {
	Body struct {
		Email     string `json:"email" required:"true" doc:"Bounced email address"`
		Type      string `json:"type" doc:"Bounce type: hard or soft" enum:"hard,soft"`
		EmailUUID string `json:"email_id" doc:"UUID of the original email"`
		Reason    string `json:"reason" doc:"Bounce reason"`
	} `json:"body"`
}

type BounceResponse struct {
	Processed bool   `json:"processed"`
	Action    string `json:"action"`
}

func (h *BounceWebhookHandler) HandleBounce(c *okapi.Context, req *BounceNotification) error {
	email := strings.ToLower(strings.TrimSpace(req.Body.Email))
	if email == "" {
		return c.AbortBadRequest("email is required")
	}

	bounceType := req.Body.Type
	if bounceType == "" {
		bounceType = "hard"
	}

	action := "recorded"

	// Resolve the message the bounce refers to before processing, so the raw
	// payload can be stored with its message already attached.
	em := h.resolveEmail(req.Body.EmailUUID)

	// Capture the payload before processing so nothing is lost when
	// resolution or the downstream updates fail. EmailUUID is only set when it
	// actually resolved — the column is a real uuid and a caller-supplied
	// string may not be one; the raw reference stays in message_id.
	var stored *models.IncomingWebhookEvent
	if h.incomingRepo != nil {
		raw, _ := json.Marshal(req)
		stored = &models.IncomingWebhookEvent{
			Source:     "bounce",
			Type:       "bounced",
			Recipient:  email,
			MessageID:  req.Body.EmailUUID,
			RawPayload: string(raw),
		}
		if em != nil {
			stored.EmailUUID = em.UUID
		}
		if err := h.incomingRepo.Create(stored); err != nil {
			logger.Warn("bounce webhook: failed to store raw payload", "error", err)
			stored = nil
		} else if req.Body.EmailUUID != "" && em == nil {
			// The payload named a message we could not resolve: keep the
			// capture searchable with the reason it went nowhere.
			_ = h.incomingRepo.SetProcessError(stored.ID, "email_id did not resolve to a known message")
		}
	}

	// Mark subscriber as bounced for hard bounces
	if bounceType == "hard" {
		// Find all subscribers with this email (across all scopes)
		var subscribers []models.Subscriber
		if err := h.subscriberRepo.FindAllByEmail(email, &subscribers); err == nil {
			now := time.Now()
			for i := range subscribers {
				if subscribers[i].Status != models.SubscriberStatusBounced {
					subscribers[i].Status = models.SubscriberStatusBounced
					subscribers[i].UpdatedAt = &now
					_ = h.subscriberRepo.Update(&subscribers[i])
				}
			}
			action = "subscriber_bounced"
		}
	}

	// Update campaign message if email UUID is provided
	if em != nil {
		if msg, err := h.messageRepo.FindByEmailID(em.ID); err == nil {
			_ = h.messageRepo.UpdateBouncedAt(msg.ID)
			action = "message_bounced"
		}
	}

	// Record the bounce on the message-history timeline. The payload hash
	// dedups provider retries of the same report.
	if em != nil {
		reason := strings.TrimSpace(req.Body.Reason)
		if reason == "" {
			reason = "bounce reported by provider (" + bounceType + ")"
		}
		emailhistory.RecordBounced(h.recorder, em, reason, payloadFingerprint(req),
			map[string]any{"bounce_type": bounceType, "source": "bounce_webhook"})
	}

	logger.Info("bounce processed", "email", email, "type", bounceType, "action", action)
	return ok(c, BounceResponse{Processed: true, Action: action})
}

// resolveEmail looks up the message a bounce payload refers to. Returns nil
// when the payload carries no reference or the reference does not resolve.
func (h *BounceWebhookHandler) resolveEmail(emailUUID string) *models.Email {
	if emailUUID == "" || h.emailRepo == nil {
		return nil
	}
	em, err := h.emailRepo.FindByUUID(emailUUID)
	if err != nil {
		return nil
	}
	return em
}

// payloadFingerprint hashes the payload so retries of an identical bounce
// report dedup on the timeline instead of piling up rows.
func payloadFingerprint(req *BounceNotification) string {
	raw, err := json.Marshal(req)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
