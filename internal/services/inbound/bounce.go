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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// DSN match-back (OB-2, ADR-002 decision 4; parity SF-10, CA-05).
//
// A bounce accepted through the VERP return-path allowlist carries the
// original message's UUID inside its signed recipient address. After the
// bounce is durably ingested (like every inbound message — ADR-002's
// non-negotiable), this processor parses the RFC 3464 report and writes the
// delivery truth onto the original message's timeline:
//
//   - Action: failed    → email_events "bounced" + a Bounce row + the
//     existing suppression chain (BounceRepository + SuppressionRepository,
//     exactly as the worker's permanent-rejection path feeds it) —
//     suppression/consent semantics unchanged;
//   - Action: delivered → email_events "delivered" (DSN-derived delivery
//     truth);
//   - Action: delayed   → transient by definition: no timeline event and no
//     suppression; the report remains on the durable inbound record.
//
// Every event is idempotent per DSN record: the timeline dedups on the
// (email_uuid, type, provider_event_id) unique index, and the side effects
// (bounce row, suppression) are guarded by the same key before they run —
// re-processing a bounce (task replay, re-ingest) is a no-op.

// providerDSN marks events recorded from RFC 3464 reports rather than by an
// ESP integration or the tracking handlers.
const providerDSN = "dsn"

// dsnMatcher runs the match-back. Built by EnableDSN; nil on services where
// the feature is not wired (then bounces ingest without match-back).
type dsnMatcher struct {
	db              *gorm.DB
	recorder        *emailhistory.Recorder
	emailRepo       *repositories.EmailRepository
	bounceRepo      *repositories.BounceRepository
	suppressionRepo *repositories.SuppressionRepository
}

// EnableDSN attaches RFC 3464 match-back to the service. db is the shared
// database handle; the processor owns its repositories and recorder. Safe to
// call twice; the last handle wins. Mirrors EnableDMARC/EnableVerification.
func (s *Service) EnableDSN(db *gorm.DB) {
	if db == nil {
		return
	}
	s.dsn = &dsnMatcher{
		db:              db,
		recorder:        emailhistory.NewRecorder(db),
		emailRepo:       repositories.NewEmailRepository(db),
		bounceRepo:      repositories.NewBounceRepository(db),
		suppressionRepo: repositories.NewSuppressionRepository(db),
	}
}

// matchBack parses the raw bounce message and writes the DSN truth to the
// original message. bounceAddress must already have been accepted through
// the VERP allowlist (the signature was verified there and is re-verified
// here). The bounce UUID must resolve to an existing sent message; anything
// else is logged and dropped — the inbound bounce record remains regardless.
func (m *dsnMatcher) matchBack(ctx context.Context, rec *models.InboundEmail, raw []byte, bounceAddress string, verp *Verp) error {
	if m == nil || m.db == nil {
		return nil
	}
	if verp == nil || !verp.Enabled() {
		return nil
	}
	emailUUID, ok := verp.Decode(bounceAddress)
	if !ok {
		// Defense in depth: the allowlist validated this address at RCPT and
		// at ingest; a decode failure here means configuration changed
		// between those points.
		return fmt.Errorf("bounce address no longer decodes: %s", bounceAddress)
	}
	em, err := m.emailRepo.FindByUUID(emailUUID)
	if err != nil || em == nil {
		// Unknown (or forged-but-valid) message UUID: the bounce is kept as
		// an ordinary inbound record, no match-back. Logged at debug — a
		// mismatch is an operator-visible curiosity, not an error.
		logger.Debug("dsn: no message for bounce UUID", "bounce_uuid", emailUUID)
		return nil
	}

	dsn, err := ParseDSN(raw)
	if err != nil {
		return err
	}

	for _, record := range dsn.Records {
		m.applyRecord(ctx, em, rec, record, dsn.ReportingMTA)
	}
	return nil
}

// applyRecord writes one DSN record's truth. See the package comment for the
// action semantics.
func (m *dsnMatcher) applyRecord(ctx context.Context, em *models.Email, bounceRec *models.InboundEmail, record DSNRecord, reportingMTA string) {
	recipient := record.Recipient()
	if recipient == "" {
		return
	}
	metadata := map[string]any{
		"source":        providerDSN,
		"inbound_id":    bounceRec.UUID,
		"action":        string(record.Action),
		"status":        record.Status,
		"reporting_mta": reportingMTA,
		"remote_mta":    record.RemoteMTA,
	}

	switch record.Action {
	case DSNActionFailed:
		eventID := dsnEventID(models.EmailEventBounced, recipient)
		if m.alreadyRecorded(em.UUID, models.EmailEventBounced, eventID) {
			return
		}
		reason := dsnFailureReason(record)
		m.recorder.Record(ctx, emailhistory.EventInput{
			EmailUUID:       em.UUID,
			Type:            models.EmailEventBounced,
			Provider:        providerDSN,
			ProviderEventID: eventID,
			Recipient:       recipient,
			Error:           reason,
			Metadata:        metadata,
		})
		// The existing suppression chain, unchanged: a hard-bounce row and
		// the same idempotent suppression upsert the worker's
		// permanent-rejection path performs.
		if m.bounceRepo != nil {
			if err := m.bounceRepo.Create(&models.Bounce{
				UserID:      em.UserID,
				WorkspaceID: em.WorkspaceID,
				EmailID:     em.ID,
				Recipient:   recipient,
				Type:        models.BounceTypeHard,
				Reason:      reason,
			}); err == nil {
				metrics.IncrementBounce(string(models.BounceTypeHard))
			} else {
				logger.Warn("dsn: failed to record bounce row", "error", err)
			}
		}
		if m.suppressionRepo != nil {
			suppression := &models.Suppression{
				UserID:      em.UserID,
				WorkspaceID: em.WorkspaceID,
				Email:       recipient,
				Reason:      "auto-suppressed: DSN " + reason,
			}
			if err := m.suppressionRepo.Upsert(suppression); err == nil {
				metrics.IncrementSuppression()
			} else {
				logger.Warn("dsn: failed to suppress recipient", "error", err)
			}
		}
	case DSNActionDelivered:
		eventID := dsnEventID(models.EmailEventDelivered, recipient)
		if m.alreadyRecorded(em.UUID, models.EmailEventDelivered, eventID) {
			return
		}
		m.recorder.Record(ctx, emailhistory.EventInput{
			EmailUUID:       em.UUID,
			Type:            models.EmailEventDelivered,
			Provider:        providerDSN,
			ProviderEventID: eventID,
			Recipient:       recipient,
			Metadata:        metadata,
		})
	case DSNActionDelayed:
		// Transient: the retry path owns delayed mail; a delay is not a
		// bounce and must not suppress. The record stays on the inbound row.
		logger.Debug("dsn: delayed disposition recorded on the inbound record only",
			"recipient", recipient, "status", record.Status)
	default:
		// relayed/expanded/unknown: no delivery truth to write.
	}
}

// alreadyRecorded reports whether this DSN-derived event was already written
// (idempotency per DSN record — replays must not double-count bounces or
// re-trigger suppression).
func (m *dsnMatcher) alreadyRecorded(emailUUID, eventType, providerEventID string) bool {
	var count int64
	if err := m.db.Model(&models.EmailEvent{}).
		Where("email_uuid = ? AND type = ? AND provider_event_id = ?", emailUUID, eventType, providerEventID).
		Count(&count).Error; err != nil {
		// The check failed (transient DB trouble): proceed — the timeline
		// insert dedups on the same key anyway, and a missed dedup is
		// preferable to dropping a genuine first report.
		return false
	}
	return count > 0
}

// dsnEventID builds the stable per-record provider event id: one DSN report
// for one recipient and action is one timeline row, no matter how often the
// message is re-ingested. Hashed so the bounded provider_event_id column
// cannot overflow on long addresses.
func dsnEventID(eventType, recipient string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(recipient)))
	return "dsn:" + eventType + ":" + hex.EncodeToString(sum[:])[:24]
}

// dsnFailureReason renders the human-readable failure reason carried on the
// bounce row, the suppression reason and the timeline event.
func dsnFailureReason(record DSNRecord) string {
	parts := make([]string, 0, 3)
	if record.Status != "" {
		parts = append(parts, "status "+record.Status)
	}
	if record.DiagnosticCode != "" {
		parts = append(parts, record.DiagnosticCode)
	}
	if len(parts) == 0 {
		return "failed (RFC 3464 DSN)"
	}
	return strings.Join(parts, ": ")
}

// processBounce offers a durably ingested bounce message to the DSN matcher.
// Never fails the ingest: every error is logged; the record stays.
func (s *Service) processBounce(ctx context.Context, rec *models.InboundEmail, raw []byte, bounceAddress string) {
	if s.dsn == nil || rec == nil || len(raw) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.dsn.matchBack(ctx, rec, raw, bounceAddress, s.verp); err != nil {
		logger.Warn("dsn match-back skipped", "inbound_id", rec.UUID, "error", err)
	}
}
