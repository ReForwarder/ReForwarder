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

// SystemARFRequest is one generated RFC 5965/6591 auth-failure feedback
// report (SF-26) to store and relay as a system notification. The Document
// is the complete multipart/report body — the outbound pipeline relays it
// verbatim (the system_dsn relay semantics: null reverse path, standard
// retry, DKIM-signed for the sender's domain at delivery time).
type SystemARFRequest struct {
	// UserID / WorkspaceID attribute the received message (the report's
	// subject) to its tenant.
	UserID      uint
	WorkspaceID *uint
	// From is the generator address on the reporting organization's domain.
	From string
	// To holds the abuse-reporting addresses discovered from the reported
	// domain's live _arf DNS record.
	To []string
	// Subject is the report mail's subject line.
	Subject string
	// Document is the complete multipart/report body (RFC 5965 §6), sent
	// verbatim.
	Document string
	// Metadata lands on the copy's "forwarded" email_events row.
	Metadata map[string]any
}

// SendSystemARF stores one RFC 5965/6591 auth-failure feedback report as a
// system notification (SF-26): the row carries Provenance system_arf and its
// TextBody IS the complete multipart/report message — the worker relays it
// verbatim with a null reverse path (the system_dsn relay semantics), and it
// is itself exempt from further notifications (the double-bounce stop).
// Deliberately NOT the tenant send path: no suppression, quota, unsubscribe
// or consent layers apply (the forwarding posture).
func (s *Service) SendSystemARF(ctx context.Context, req *SystemARFRequest) (*SendResponse, error) {
	if req == nil || req.From == "" || len(req.To) == 0 || req.Document == "" {
		return nil, fmt.Errorf("system arf: from, recipients and the report document are required")
	}
	em := &models.Email{
		UUID:        uuid.NewString(),
		UserID:      req.UserID,
		WorkspaceID: req.WorkspaceID,
		Sender:      req.From,
		Recipients:  req.To,
		Subject:     req.Subject,
		TextBody:    req.Document,
		Provenance:  models.EmailProvenanceSystemARF,
		Status:      models.EmailStatusPending,
		Provider:    ClassifyRecipients(req.To),
	}
	if err := s.emailRepo.Create(em); err != nil {
		return nil, fmt.Errorf("failed to store ARF report: %w", err)
	}

	if s.devMode {
		em.Status = models.EmailStatusSent
		now := time.Now()
		em.SentAt = &now
		_ = s.emailRepo.Update(em)
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}
	if s.enqueuer == nil {
		return nil, fmt.Errorf("system arf: no producer wired; report stays unsent")
	}
	em.Status = models.EmailStatusQueued
	_ = s.emailRepo.Update(em)
	if err := s.enqueuer.EnqueueEmailSend(em.ID, ""); err != nil {
		em.Status = models.EmailStatusFailed
		em.ErrorMessage = fmt.Sprintf("failed to enqueue ARF report: %v", err)
		_ = s.emailRepo.Update(em)
		return nil, fmt.Errorf("failed to enqueue ARF report: %w", err)
	}
	return &SendResponse{ID: em.UUID, Status: em.Status}, nil
}
