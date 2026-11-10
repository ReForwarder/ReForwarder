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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package email

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
)

// Webmail compose submission (OB-5 wave W3, ADR-005 decision 2): the seam the
// compose service calls to push a composed mailbox message into the standard
// outbound pipeline. Delivery is per ADR-001 (direct/relay per sending-domain
// configuration), DKIM-signed at delivery time from the domain's signing keys,
// with every attempt recorded by the delivering worker — none of that is
// re-implemented here; the row enqueues through the same `email:send` task the
// transactional API uses.
//
// Deliberately narrower than Send (the same judgment as SendSystemForward,
// ADR-002 decision 2): personal mail is not a marketing-send path (ADR-005
// decision 4), so this path never consults or mutates suppression,
// unsubscribe, or consent layers, never applies open/click tracking rewriting,
// and never consumes the tenant's marketing rate limit. What it DOES inherit
// is the ADR-001 non-negotiable: the sender domain must be verified
// (checkDomainVerification — compose's From is user-supplied, unlike the
// system-forward path whose caller hands over a structurally verified
// address), maintenance mode is honored, and R-24 header validation applies.

// MailComposeRequest describes one composed message entering the outbound
// pipeline. Attachments carry base64 Content like every transactional API
// send (persistAttachments decodes and stores them).
type MailComposeRequest struct {
	// UUID optionally fixes the Email row's UUID. The compose service
	// pre-generates it so the mailstore Sent copy can carry the linkage
	// header before the row exists. Empty mints a fresh UUID.
	UUID        string              `json:"-"`
	UserID      uint                `json:"-"`
	WorkspaceID *uint               `json:"-"`
	From        string              `json:"-"`
	To          []string            `json:"-"`
	Subject     string              `json:"-"`
	Text        string              `json:"-"`
	HTML        string              `json:"-"`
	Attachments []models.Attachment `json:"-"`
	// MessageID is the RFC 5322 Message-ID minted for the compose. The
	// mailstore Sent copy carries the same value, so the stored message and
	// the transported message share identity for threading and traces.
	MessageID string `json:"-"`
	// MailboxID is metadata only: the hosted mailbox the Sent copy was filed
	// into, recorded on the composed event for traceability.
	MailboxID uint `json:"-"`
}

// SendMailCompose enqueues a composed mailbox message through the standard
// outbound pipeline. Returns the pipeline's SendResponse (ID = the Email
// UUID, Status = queued/sent(dev)/failed); delivery truth arrives later as
// email_events on that UUID — the compose surface reads them back through the
// delivery-status endpoint.
func (s *Service) SendMailCompose(ctx context.Context, req *MailComposeRequest) (*SendResponse, error) {
	if req == nil || len(req.To) == 0 {
		return nil, fmt.Errorf("compose: no recipient")
	}
	if err := validateAddresses(req.From, req.To); err != nil {
		return nil, err
	}
	// R-24: CR/LF/NUL never reach the message builder on the compose path.
	if err := validateSendHeaders(req.Subject, nil); err != nil {
		return nil, fmt.Errorf("header validation: %w", err)
	}
	if s.settings != nil && s.settings.MaintenanceMode() {
		return nil, fmt.Errorf("maintenance: email sending is temporarily disabled")
	}
	// ADR-001 non-negotiable 1: a composed message's sender domain must be
	// verified before anything is enqueued (strict mode is scoped to the
	// workspace, with the user-level flag in personal mode — R-23).
	if err := s.checkDomainVerification(req.UserID, req.WorkspaceID, req.From); err != nil {
		return nil, err
	}

	// System headers only: custom headers are not part of the compose surface
	// (ADR-005 decision 2 — to/cc/bcc + subject + body + attachments). The
	// Posta-UUID header is the durable link between the outbound Email row and
	// the mailstore Sent copy; it is set here, never accepted from callers.
	headers := map[string]string{}
	if req.MessageID != "" {
		headers["Message-ID"] = fmt.Sprintf("<%s>", req.MessageID)
	}
	if req.UUID != "" {
		headers["X-Posta-Email-Uuid"] = req.UUID
	}
	headersJSON := ""
	if len(headers) > 0 {
		b, err := json.Marshal(headers)
		if err != nil {
			return nil, fmt.Errorf("compose: marshal headers: %w", err)
		}
		headersJSON = string(b)
	}

	attachmentsJSON, err := s.persistAttachments(ctx, fmt.Sprintf("%d", time.Now().UnixNano()), req.Attachments)
	if err != nil {
		return nil, err
	}
	text := req.Text
	if text == "" && req.HTML != "" {
		text = HTMLToText(req.HTML)
	}

	id := req.UUID
	if id == "" {
		id = uuid.NewString()
	}
	em := &models.Email{
		UUID:            id,
		UserID:          req.UserID,
		WorkspaceID:     req.WorkspaceID,
		Sender:          req.From,
		Recipients:      req.To,
		Subject:         req.Subject,
		HTMLBody:        req.HTML,
		TextBody:        text,
		AttachmentsJSON: attachmentsJSON,
		HeadersJSON:     headersJSON,
		// Provenance stamp: webmail compose (OB-5, ADR-005 decision 2) — the
		// mark distinguishing mailbox mail from tenant/API-originated sends.
		Provenance: models.EmailProvenanceMailCompose,
		Status:     models.EmailStatusPending,
		Provider:   ClassifyRecipients(req.To),
	}
	if err := s.emailRepo.Create(em); err != nil {
		return nil, fmt.Errorf("failed to store email: %w", err)
	}

	// The composed event opens the message's timeline with the compose
	// provenance and the Sent-copy linkage, so the webmail delivery-status
	// view always has at least one event to show.
	emailhistory.RecordComposed(s.recorder, em, map[string]any{
		"provenance": models.EmailProvenanceMailCompose,
		"mailbox_id": req.MailboxID,
		"message_id": req.MessageID,
	})

	// CA-06 dev fallback: without a wired producer the compose is stored and
	// marked sent without delivery, exactly like the API send path's
	// brand-new-send dev short-circuit (store-without-send).
	if s.devMode {
		em.Status = models.EmailStatusSent
		now := time.Now()
		em.SentAt = &now
		_ = s.emailRepo.Update(em)
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}

	if s.enqueuer != nil {
		em.Status = models.EmailStatusQueued
		_ = s.emailRepo.Update(em)
		if err := s.enqueuer.EnqueueEmailSend(em.ID, ""); err != nil {
			em.Status = models.EmailStatusFailed
			em.ErrorMessage = fmt.Sprintf("failed to enqueue: %v", err)
			_ = s.emailRepo.Update(em)
			emailhistory.RecordFailed(s.recorder, em, em.ErrorMessage, nil)
			return nil, fmt.Errorf("failed to enqueue email: %w", err)
		}
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}

	// No producer wired (enqueuer-less flow): synchronous delivery through the
	// same pipeline Send's fallback uses.
	return s.sendSync(em, req.UserID, req.WorkspaceID, &SendRequest{
		From:        req.From,
		To:          req.To,
		Subject:     req.Subject,
		HTML:        req.HTML,
		Text:        text,
		Attachments: req.Attachments,
	})
}
