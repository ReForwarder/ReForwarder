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

package provider_events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// IngestionService turns normalized provider events into timeline rows and
// side effects. It never returns an error for per-event problems: an event
// that cannot be resolved or recorded is skipped and counted, because one
// malformed report must not fail the whole webhook batch.
type IngestionService struct {
	emailRepo       *repositories.EmailRepository
	subscriberRepo  *repositories.SubscriberRepository
	suppressionRepo *repositories.SuppressionRepository
	bounceRepo      *repositories.BounceRepository
	messageRepo     *repositories.CampaignMessageRepository
	incomingRepo    *repositories.IncomingWebhookEventRepository
	recorder        *emailhistory.Recorder
	autoSuppress    bool

	// db backs the classification analytics query; wired via SetDB.
	db *gorm.DB

	// lookupEmail resolves an email UUID; a test seam over
	// emailRepo.FindByUUID.
	lookupEmail func(uuid string) (*models.Email, error)
}

func NewIngestionService(emailRepo *repositories.EmailRepository, recorder *emailhistory.Recorder) *IngestionService {
	s := &IngestionService{
		emailRepo: emailRepo,
		recorder:  recorder,
	}
	if emailRepo != nil {
		s.lookupEmail = emailRepo.FindByUUID
	}
	return s
}

// SetSubscriberRepo lets permanent bounces mark subscribers as bounced and
// complaints as complained. Nil (the default) skips that.
func (s *IngestionService) SetSubscriberRepo(r *repositories.SubscriberRepository) {
	s.subscriberRepo = r
}

// SetSuppressionRepo enables auto-suppression of recipients reported as
// permanently bounced by the upstream provider. Nil (the default) skips that.
func (s *IngestionService) SetSuppressionRepo(r *repositories.SuppressionRepository) {
	s.suppressionRepo = r
}

// SetBounceRepo lets provider bounces and complaints be recorded in the
// bounces table. Nil (the default) skips that.
func (s *IngestionService) SetBounceRepo(r *repositories.BounceRepository) {
	s.bounceRepo = r
}

// SetCampaignMessageRepo lets bounces mark campaign messages bounced.
func (s *IngestionService) SetCampaignMessageRepo(r *repositories.CampaignMessageRepository) {
	s.messageRepo = r
}

// SetIncomingWebhookRepo wires the store for raw provider payloads, captured
// before processing (the bounce-webhook pattern). Nil (the default) skips the
// capture.
func (s *IngestionService) SetIncomingWebhookRepo(r *repositories.IncomingWebhookEventRepository) {
	s.incomingRepo = r
}

// SetAutoSuppress toggles suppression of recipients reported permanently
// bounced by the upstream provider (mirrors the worker's
// POSTA_AUTO_SUPPRESS_ON_REJECT switch).
func (s *IngestionService) SetAutoSuppress(enabled bool) {
	s.autoSuppress = enabled
}

// SetDB wires the database handle for the classification analytics query.
func (s *IngestionService) SetDB(db *gorm.DB) {
	s.db = db
}

// WithEmailLookup overrides the email resolution seam (tests).
func (s *IngestionService) WithEmailLookup(fn func(uuid string) (*models.Email, error)) {
	s.lookupEmail = fn
}

// Ingest processes one webhook batch: classify each event (R-05), resolve its
// message, record the timeline event, and apply the bounce side effects.
// accepted counts events recorded (or deduped) against a known message;
// skipped counts events with no usable message reference. The raw payload is
// captured into incoming_webhook_events once per batch, with the email UUID
// attached when it resolved.
func (s *IngestionService) Ingest(ctx context.Context, events []NormalizedEvent, rawPayload string) (accepted, skipped int) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(events) == 0 {
		s.captureRaw(rawPayload, nil, "")
		return 0, 0
	}

	var firstResolved *models.Email
	var firstUUIDRef string

	for i := range events {
		ev := &events[i]
		if !isNormalizedEventType(ev.EventType) || ev.Provider == "" {
			logger.Warn("provider event: dropping malformed normalized event",
				"provider", ev.Provider, "event_type", ev.EventType)
			skipped++
			continue
		}

		if ev.EmailUUID == "" {
			logger.Debug("provider event: no message reference, skipping",
				"provider", ev.Provider, "event_type", ev.EventType, "recipient", ev.Recipient)
			skipped++
			continue
		}
		// Remember the first reference even before resolution, so the raw
		// capture keeps the caller-supplied reference when it fails to
		// resolve (the bounce-webhook pattern).
		if firstUUIDRef == "" {
			firstUUIDRef = ev.EmailUUID
		}
		em := s.resolveEmail(ev.EmailUUID)
		if em == nil {
			logger.Info("provider event: email uuid did not resolve, skipping",
				"provider", ev.Provider, "event_type", ev.EventType, "email_uuid", ev.EmailUUID)
			skipped++
			continue
		}
		if firstResolved == nil {
			firstResolved = em
		}

		// Out-of-order tolerance: the provider-reported timestamp is stored
		// as-is (nil falls back to the recorder's now).
		ev.ProviderEventID = DedupEventID(ev.Provider, ev.EventType, ev.ProviderEventID, ev.Recipient, ev.Diagnostic)
		ev.Classification = string(s.classify(ev))

		s.applyEvent(ctx, em, ev)
		accepted++
	}

	s.captureRaw(rawPayload, firstResolved, firstUUIDRef)
	return accepted, skipped
}

// resolveEmail looks up the message a provider event refers to, returning nil
// when it does not exist.
func (s *IngestionService) resolveEmail(uuid string) *models.Email {
	if s.lookupEmail == nil {
		return nil
	}
	em, err := s.lookupEmail(uuid)
	if err != nil || em == nil {
		return nil
	}
	return em
}

// classify runs the R-05 classifier over a normalized event, feeding it the
// structured SMTP fields the adapter may have placed in Metadata.
func (s *IngestionService) classify(ev *NormalizedEvent) Classification {
	var code int
	var enhanced string
	if v, ok := intValue(ev.Metadata, "smtp_code"); ok {
		code = v
	}
	if v, ok := stringValue(ev.Metadata, "enhanced_code"); ok {
		enhanced = v
	}
	return Classify(ev.Provider, ev.EventType, code, enhanced, ev.Diagnostic)
}

// applyEvent dispatches one classified event to its timeline + side effects.
func (s *IngestionService) applyEvent(ctx context.Context, em *models.Email, ev *NormalizedEvent) {
	metadata := mergedMetadata(ev)

	switch ev.EventType {
	case EventDelivered:
		// Provider-confirmed delivery — the previously-unreachable
		// EmailEventDelivered, distinct from the relay-acceptance "sent".
		s.record(ctx, em, ev, models.EmailEventDelivered, "", metadata)
	case EventOpened:
		s.record(ctx, em, ev, models.EmailEventOpened, "", metadata)
	case EventClicked:
		s.record(ctx, em, ev, models.EmailEventClicked, "", metadata)
	case EventComplained:
		s.recordComplaint(ctx, em, ev, metadata)
	case EventBounced, EventRejected:
		cls := Classification(ev.Classification)
		if cls.Permanent() {
			s.recordPermanentBounce(ctx, em, ev, cls, metadata)
			return
		}
		// Soft bounce / transient rejection: recorded on the timeline as a
		// bounce with its classification, without suppression or subscriber
		// marking — the message may still deliver.
		s.record(ctx, em, ev, models.EmailEventBounced, ev.Diagnostic, metadata)
	case EventDeferred:
		// Transient deferral: a failed timeline row with the classification,
		// so retry analytics can see the provider's own reports.
		s.record(ctx, em, ev, models.EmailEventFailed, ev.Diagnostic, metadata)
	}
}

// record writes one timeline row through the recorder. reason, when set,
// lands in the event Error field.
func (s *IngestionService) record(ctx context.Context, em *models.Email, ev *NormalizedEvent, eventType, reason string, metadata map[string]any) {
	in := emailhistory.EventInput{
		EmailUUID:       em.UUID,
		WorkspaceID:     em.WorkspaceID,
		Type:            eventType,
		OccurredAt:      providerTime(ev),
		Provider:        ev.Provider,
		ProviderEventID: ev.ProviderEventID,
		Recipient:       eventRecipient(ev, em),
		Error:           reason,
		Metadata:        metadata,
	}
	if ev.EventType == EventClicked {
		in.URL, _ = stringValue(ev.Metadata, "url")
	}
	s.recorder.Record(ctx, in)
}

// recordPermanentBounce applies the full hard-bounce side-effect chain for a
// provider-reported permanent bounce — the ingestion-side mirror of the
// worker's handlePermanentRejection: bounce record, suppression upsert,
// subscriber marking, campaign message update, and the bounced timeline event.
func (s *IngestionService) recordPermanentBounce(ctx context.Context, em *models.Email, ev *NormalizedEvent, cls Classification, metadata map[string]any) {
	recipient := eventRecipient(ev, em)
	reason := firstNonEmptyStr(ev.Diagnostic, "bounce reported by "+ev.Provider)
	classifiedReason := string(cls) + ": " + reason

	// Bounce record for audit/metrics.
	if s.bounceRepo != nil {
		bounce := &models.Bounce{
			UserID:      em.UserID,
			WorkspaceID: em.WorkspaceID,
			EmailID:     em.ID,
			Recipient:   recipient,
			Type:        models.BounceTypeHard,
			Reason:      classifiedReason,
		}
		if err := s.bounceRepo.Create(bounce); err == nil {
			metrics.IncrementBounce(string(models.BounceTypeHard))
		}
	}

	// Suppression: retrying a permanently bounced address is pointless.
	if s.autoSuppress && s.suppressionRepo != nil {
		suppression := &models.Suppression{
			UserID:      em.UserID,
			WorkspaceID: em.WorkspaceID,
			Email:       recipient,
			Reason:      "auto-suppressed: provider " + string(cls) + " bounce (" + reason + ")",
		}
		if err := s.suppressionRepo.Upsert(suppression); err == nil {
			metrics.IncrementSuppression()
		}
	}

	// Mark subscribers as bounced (the bounce-webhook pattern: every scope).
	if s.subscriberRepo != nil {
		var subscribers []models.Subscriber
		if err := s.subscriberRepo.FindAllByEmail(recipient, &subscribers); err == nil {
			now := time.Now()
			for i := range subscribers {
				if subscribers[i].Status != models.SubscriberStatusBounced {
					subscribers[i].Status = models.SubscriberStatusBounced
					subscribers[i].UpdatedAt = &now
					_ = s.subscriberRepo.Update(&subscribers[i])
				}
			}
		}
	}

	// Campaign message bookkeeping.
	if s.messageRepo != nil {
		if msg, err := s.messageRepo.FindByEmailID(em.ID); err == nil {
			_ = s.messageRepo.UpdateBouncedAt(msg.ID)
		}
	}

	// Timeline: a bounced row carrying the classification and dedup id.
	s.record(ctx, em, ev, models.EmailEventBounced, classifiedReason, metadata)
}

// recordComplaint records a spam complaint: a Bounce row of type complaint,
// the complained timeline event, and subscriber marking.
func (s *IngestionService) recordComplaint(ctx context.Context, em *models.Email, ev *NormalizedEvent, metadata map[string]any) {
	recipient := eventRecipient(ev, em)
	reason := firstNonEmptyStr(ev.Diagnostic, "spam complaint reported by "+ev.Provider)

	if s.bounceRepo != nil {
		bounce := &models.Bounce{
			UserID:      em.UserID,
			WorkspaceID: em.WorkspaceID,
			EmailID:     em.ID,
			Recipient:   recipient,
			Type:        models.BounceTypeComplaint,
			Reason:      string(ClassComplaint) + ": " + reason,
		}
		if err := s.bounceRepo.Create(bounce); err == nil {
			metrics.IncrementBounce(string(models.BounceTypeComplaint))
		}
	}

	if s.subscriberRepo != nil {
		var subscribers []models.Subscriber
		if err := s.subscriberRepo.FindAllByEmail(recipient, &subscribers); err == nil {
			now := time.Now()
			for i := range subscribers {
				if subscribers[i].Status != models.SubscriberStatusComplained {
					subscribers[i].Status = models.SubscriberStatusComplained
					subscribers[i].UpdatedAt = &now
					_ = s.subscriberRepo.Update(&subscribers[i])
				}
			}
		}
	}

	s.record(ctx, em, ev, models.EmailEventComplained, reason, metadata)
}

// providerTime returns the event's timestamp, or zero (→ recorder's now).
func providerTime(ev *NormalizedEvent) time.Time {
	if ev.Timestamp != nil {
		return *ev.Timestamp
	}
	return time.Time{}
}

// eventRecipient picks the event's recipient, falling back to the message's
// first recipient so the timeline row is never anonymous.
func eventRecipient(ev *NormalizedEvent, em *models.Email) string {
	if ev.Recipient != "" {
		return ev.Recipient
	}
	if len(em.Recipients) > 0 {
		return em.Recipients[0]
	}
	return ""
}

// mergedMetadata copies the event metadata and adds the classification.
func mergedMetadata(ev *NormalizedEvent) map[string]any {
	metadata := make(map[string]any, len(ev.Metadata)+1)
	for k, v := range ev.Metadata {
		metadata[k] = v
	}
	metadata["classification"] = ev.Classification
	metadata["provider_event_type"] = ev.EventType
	return metadata
}

// captureRaw stores the raw payload once per batch (the bounce-webhook
// pattern: capture before processing, annotate what failed to resolve).
func (s *IngestionService) captureRaw(rawPayload string, em *models.Email, uuidRef string) {
	if s.incomingRepo == nil || rawPayload == "" {
		return
	}
	stored := &models.IncomingWebhookEvent{
		Source: "provider_webhook",
		Type:   "provider_event",
	}
	if em != nil {
		stored.EmailUUID = em.UUID
		if len(em.Recipients) > 0 {
			stored.Recipient = em.Recipients[0]
		}
	} else if uuidRef != "" {
		// Keep the unresolved reference searchable, as the bounce webhook
		// does; message_id is a plain string column and accepts anything.
		stored.MessageID = uuidRef
	}
	if err := s.incomingRepo.Create(stored); err != nil {
		logger.Warn("provider event: failed to store raw payload", "error", err)
		return
	}
	if em == nil && uuidRef != "" {
		_ = s.incomingRepo.SetProcessError(stored.ID, "email uuid did not resolve to a known message")
	}
}

// ClassificationCounts aggregates R-05 classifications over the recorded
// timeline: classification counts live in the email_events Metadata JSONB
// (documented choice: no schema migration, and the timeline row already
// carries everything about the event; a dedicated column can be added later
// without losing data). Returns class → event count.
func (s *IngestionService) ClassificationCounts(ctx context.Context, from, to *time.Time) (map[string]int64, error) {
	return s.classificationCounts(ctx, nil, from, to)
}

// WorkspaceClassificationCounts is ClassificationCounts scoped to one
// workspace's timeline rows — the shape the workspace-scoped logs endpoint
// (GET /workspaces/current/logs/classifications) serves. Deployment-wide
// counts are deliberately not reachable through it.
func (s *IngestionService) WorkspaceClassificationCounts(ctx context.Context, workspaceID uint, from, to *time.Time) (map[string]int64, error) {
	return s.classificationCounts(ctx, &workspaceID, from, to)
}

// classificationCounts runs the shared aggregate; a nil workspaceID spans the
// deployment, a non-nil one pins email_events.workspace_id.
func (s *IngestionService) classificationCounts(ctx context.Context, workspaceID *uint, from, to *time.Time) (map[string]int64, error) {
	if s.db == nil {
		return nil, nil
	}
	q := s.db.WithContext(ctx).Model(&models.EmailEvent{}).
		Select("COALESCE(metadata->>'classification', '(none)') AS classification, COUNT(*) AS count").
		Where("metadata <> '{}' AND metadata->>'classification' IS NOT NULL").
		Group("classification")
	if workspaceID != nil {
		q = q.Where("workspace_id = ?", *workspaceID)
	}
	if from != nil {
		q = q.Where("occurred_at >= ?", *from)
	}
	if to != nil {
		q = q.Where("occurred_at < ?", *to)
	}
	var rows []struct {
		Classification string `json:"classification"`
		Count          int64  `json:"count"`
	}
	// Find (not Scan): with Model set it runs the same aggregate select, but
	// unlike Scan it builds its SQL through the normal query callbacks, which
	// keeps it testable on a DryRun session.
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.Classification] = row.Count
	}
	return out, nil
}

// intValue / stringValue read typed fields out of the metadata map.
func intValue(m map[string]any, key string) (int, bool) {
	switch v := m[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func stringValue(m map[string]any, key string) (string, bool) {
	if v, ok := m[key].(string); ok {
		return v, true
	}
	return "", false
}
