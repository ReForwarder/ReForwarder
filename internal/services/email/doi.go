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

package email

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/goposta/posta/internal/models"
)

// SystemDOIConfirmationRequest is one double-opt-in confirmation message
// (RF-F075) to store and relay as a system notification.
type SystemDOIConfirmationRequest struct {
	// UserID / WorkspaceID attribute the confirmation to the tenant whose
	// list the recipient asked to join.
	UserID      uint
	WorkspaceID *uint
	// From is the platform transactional sender identity.
	From string
	// To holds the recipient that asked to subscribe.
	To []string
	// Subject / HTML / Text carry the confirmation copy with the redeemable
	// /t/confirm/{token} link.
	Subject string
	HTML    string
	Text    string
}

// DOIConfirmationSender is the narrow seam handlers use to send
// double-opt-in confirmation emails (RF-F075); *Service implements it.
type DOIConfirmationSender interface {
	SendSystemDOIConfirmation(ctx context.Context, req *SystemDOIConfirmationRequest) (*SendResponse, error)
}

// SendSystemDOIConfirmation stores one double-opt-in confirmation as a
// system notification (RF-F075): the row carries Provenance system_doi and
// travels the standard outbound pipeline (queued, DKIM-signed for the
// sender's domain at delivery time, standard retry). Deliberately NOT the
// tenant send path: a confirmation is the recipient's own requested action,
// so no suppression, unsubscribe or consent layer applies and no marketing
// rate limit is consumed — the forwarding posture.
func (s *Service) SendSystemDOIConfirmation(ctx context.Context, req *SystemDOIConfirmationRequest) (*SendResponse, error) {
	if req == nil || req.From == "" || len(req.To) == 0 || req.Subject == "" {
		return nil, fmt.Errorf("system doi: from, recipients and subject are required")
	}
	if req.HTML == "" && req.Text == "" {
		return nil, fmt.Errorf("system doi: html or text body is required")
	}
	if err := validateAddresses(req.From, req.To); err != nil {
		return nil, err
	}
	// R-24: CR/LF/NUL never reach the message builder, on the system path too.
	if err := validateSendHeaders(req.Subject, nil); err != nil {
		return nil, fmt.Errorf("header validation: %w", err)
	}
	if s.settings != nil && s.settings.MaintenanceMode() {
		return nil, fmt.Errorf("maintenance: email sending is temporarily disabled")
	}
	text := req.Text
	if text == "" {
		text = HTMLToText(req.HTML)
	}

	em := &models.Email{
		UUID:        uuid.NewString(),
		UserID:      req.UserID,
		WorkspaceID: req.WorkspaceID,
		Sender:      req.From,
		Recipients:  req.To,
		Subject:     req.Subject,
		HTMLBody:    req.HTML,
		TextBody:    text,
		Provenance:  models.EmailProvenanceSystemDOI,
		Status:      models.EmailStatusPending,
		Provider:    ClassifyRecipients(req.To),
	}
	if err := s.emailRepo.Create(em); err != nil {
		return nil, fmt.Errorf("failed to store DOI confirmation: %w", err)
	}

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
			em.ErrorMessage = fmt.Sprintf("failed to enqueue DOI confirmation: %v", err)
			_ = s.emailRepo.Update(em)
			return nil, fmt.Errorf("failed to enqueue DOI confirmation: %w", err)
		}
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}
	// No producer wired: synchronous delivery through the same pipeline
	// Send's fallback uses, so a confirmation is never silently dropped.
	return s.sendSync(em, req.UserID, req.WorkspaceID, &SendRequest{
		From:    req.From,
		To:      req.To,
		Subject: req.Subject,
		HTML:    req.HTML,
		Text:    text,
	})
}
