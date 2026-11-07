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
	"errors"
	"fmt"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// OB-2 quarantine (ADR-002 decision 3, SF-51). The QuarantineService is the
// wave-3 implementation of the QuarantineStore seam (route_action.go): the
// routing engine hands a durably persisted message over here when a route
// says quarantine, and the dashboard's review surface releases or deletes
// through the same service.
//
// Binding rules:
//   - The message itself is never moved or copied — it stays in
//     inbound_emails (durable-first); the quarantine row records the hold.
//   - Release re-runs routing at the post-ingest point (resolveRoute +
//     applyRouteActions). A message whose recipient no longer resolves to an
//     enabled route (or whose domain lost verification) is NOT released —
//     fail closed, the hold stands.
//   - Delete is permanent: the inbound row and its blobs are destroyed; the
//     quarantine row flips to `deleted` as the surviving tombstone and an
//     audit event records what was destroyed and by whom.
//   - Every lookup is workspace-scoped (ownsResource pattern); a record from
//     another workspace is invisible, never a leak.

var (
	// ErrQuarantineNotPending — the record was already released or deleted;
	// a disposition is taken exactly once.
	ErrQuarantineNotPending = errors.New("quarantine record is not pending")
	// ErrQuarantineMessageMissing — the referenced inbound email row is gone
	// (never happens through the service paths; a defensive state).
	ErrQuarantineMessageMissing = errors.New("quarantined message no longer exists")
	// ErrQuarantineUnverifiedDomain — the message's domain lost verification
	// since ingest; routing rules resolve only through tenant-verified domains.
	ErrQuarantineUnverifiedDomain = errors.New("quarantined message domain is no longer verified")
)

// QuarantineService implements the QuarantineStore seam and the review
// surface (list / release / delete) behind the dashboard endpoints.
type QuarantineService struct {
	repo        *repositories.InboundQuarantineRepository
	inboundRepo *repositories.InboundEmailRepository
	domainRepo  *repositories.DomainRepository
	// svc is the ingest service this store is attached to: release re-runs
	// its routing resolution and post-ingest actions on the same message.
	svc *Service
}

// NewQuarantineService builds the quarantine store over its repositories and
// the ingest service whose routing it re-runs on release.
func NewQuarantineService(
	repo *repositories.InboundQuarantineRepository,
	inboundRepo *repositories.InboundEmailRepository,
	domainRepo *repositories.DomainRepository,
	svc *Service,
) *QuarantineService {
	return &QuarantineService{
		repo:        repo,
		inboundRepo: inboundRepo,
		domainRepo:  domainRepo,
		svc:         svc,
	}
}

// Quarantine records the hold for a durably persisted message (the
// QuarantineStore seam). Idempotent per message: an existing pending record
// is left alone, so a racing or repeated action cannot mint a second hold.
func (s *QuarantineService) Quarantine(_ context.Context, rec *models.InboundEmail, route *models.InboundRoute, reason string) error {
	if s == nil || s.repo == nil || rec == nil {
		return nil
	}
	if existing, err := s.repo.FindPendingByInboundEmailID(rec.ID); err == nil && existing != nil {
		return nil
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("check existing quarantine record: %w", err)
	}

	var routeID *uint
	if route != nil {
		id := route.ID
		routeID = &id
	}
	row := &models.InboundQuarantine{
		UserID:         rec.UserID,
		WorkspaceID:    rec.WorkspaceID,
		DomainID:       rec.DomainID,
		InboundEmailID: rec.ID,
		RouteID:        routeID,
		Reason:         reason,
		Score:          rec.SpamScore,
		Status:         models.InboundQuarantinePending,
		ReceivedAt:     rec.ReceivedAt,
	}
	if err := s.repo.Create(row); err != nil {
		return fmt.Errorf("create quarantine record: %w", err)
	}
	return nil
}

// List returns the workspace's quarantine records, paged, with the caller's
// filters applied. Scope comes from the request context (ownsResource
// pattern) — cross-workspace rows are unaddressable.
func (s *QuarantineService) List(scope repositories.ResourceScope, f repositories.InboundQuarantineFilter, limit, offset int) ([]models.InboundQuarantine, int64, error) {
	return s.repo.ListByScope(scope, f, limit, offset)
}

// Summaries loads the referenced inbound messages for a page of quarantine
// records (sender, subject, size, recipients) so the review list can show
// what was held. References whose message is gone (permanently deleted) are
// simply absent from the map. Only messages owned by the caller's scope are
// returned — a summary never leaks across workspaces.
func (s *QuarantineService) Summaries(scope repositories.ResourceScope, items []models.InboundQuarantine) (map[uint]models.InboundEmail, error) {
	out := make(map[uint]models.InboundEmail, len(items))
	for _, q := range items {
		if q.InboundEmailID == 0 {
			continue
		}
		rec, err := s.ownedMessage(scope, q.InboundEmailID)
		if err != nil {
			continue // deleted or foreign: no summary, the row itself still shows
		}
		out[q.InboundEmailID] = *rec
	}
	return out, nil
}

// Release puts a held message back through routing (ADR-002 §3: "release
// re-runs routing"). reviewerID is the acting user, recorded on the record.
//
// The hold must be pending; the message's domain must still be tenant-
// verified; and the recipient must still resolve to an enabled route —
// otherwise the release fails and the hold stands. On success the record
// settles to released (with the reviewer stamped), the matched action
// executes (deliver → parse pipeline, forward → task queue, webhook →
// dispatch, quarantine → a new hold), and an audit event records the
// disposition.
func (s *QuarantineService) Release(ctx context.Context, scope repositories.ResourceScope, id uint, reviewerID uint) (*models.InboundQuarantine, error) {
	row, err := s.repo.FindByID(scope, id)
	if err != nil {
		return nil, err // includes the cross-workspace "not found" case
	}
	if row.Status != models.InboundQuarantinePending {
		return nil, ErrQuarantineNotPending
	}
	rec, err := s.ownedMessage(scope, row.InboundEmailID)
	if err != nil {
		return nil, err
	}
	domain, err := s.domainRepo.FindByID(rec.DomainID)
	if err != nil {
		return nil, fmt.Errorf("load quarantined message domain: %w", err)
	}
	if !domain.OwnershipVerified {
		// The non-negotiable: routing rules resolve only through tenant-
		// verified domains. A domain that lost verification cannot re-route.
		return nil, ErrQuarantineUnverifiedDomain
	}

	route, kind, rerr := s.svc.resolveRoute(domain, rec.Recipients)
	if errors.Is(rerr, ErrNoRoute) {
		// Fail closed: no enabled route (and no catch-all) matches anymore —
		// the operator must add one or delete the message.
		return nil, fmt.Errorf("%w: recipient no longer matches an enabled route", ErrNoRoute)
	} else if rerr != nil {
		return nil, rerr
	}

	// Back through the pipeline: the record re-stamps the freshly resolved
	// route. The review record settles BEFORE the action runs — the action
	// may legitimately quarantine the message again (re-hold), and the
	// store's per-message idempotency must not collide with the still-pending
	// record.
	rec.Status = models.InboundStatusReceived
	rec.ErrorMessage = ""
	rec.RouteID = inboundRouteID(route)
	rec.RouteAction = inboundRouteAction(route)
	if err := s.inboundRepo.Update(rec); err != nil {
		return nil, fmt.Errorf("update quarantined message for release: %w", err)
	}
	now := time.Now().UTC()
	row.Status = models.InboundQuarantineReleased
	row.ReviewedBy = &reviewerID
	row.ReviewedAt = &now
	if err := s.repo.Update(row); err != nil {
		return nil, fmt.Errorf("mark quarantine record released: %w", err)
	}

	// Re-run the post-ingest action executor exactly as at ingest. The raw
	// bytes ride along for the OB-3 mailbox delivery seam (flow F13): a
	// released over-quota hold re-attempts the append. LoadRaw reads the
	// inline column or the blob-stored .eml; on failure the actions still run
	// (forward/webhook/quarantine need only the record) and only the mailbox
	// delivery degrades — logged, never silent.
	raw, rawErr := s.svc.LoadRaw(ctx, rec)
	if rawErr != nil {
		logger.Error("release could not load the message raw bytes; mailbox delivery degraded",
			"inbound_id", rec.ID, "error", rawErr)
		raw = nil
	}
	s.svc.applyRouteActions(ctx, rec, route, kind, raw)

	// Deliver (and an empty action) leaves the record received — the parse
	// worker was never enqueued for a held message, so the release queues it
	// now. Route-owned actions (forward/webhook/quarantine) ran above and own
	// their own notification semantics.
	if rec.Status == models.InboundStatusReceived && s.svc.producer != nil {
		if err := s.svc.producer.EnqueueInboundParse(rec.ID); err != nil {
			logger.Error("failed to enqueue inbound parse after quarantine release",
				"inbound_id", rec.ID, "error", err)
		}
	}

	s.publishReviewEvent("inbound.quarantine_released", scope, row, rec, reviewerID)
	return row, nil
}

// Delete permanently discards a held message (ADR-002 §3: "deletion is
// permanent"). The inbound row and its blob-stored content are destroyed;
// the quarantine record flips to `deleted` as the surviving tombstone and an
// audit event preserves what was destroyed and who destroyed it.
func (s *QuarantineService) Delete(ctx context.Context, scope repositories.ResourceScope, id uint, reviewerID uint) (*models.InboundQuarantine, error) {
	row, err := s.repo.FindByID(scope, id)
	if err != nil {
		return nil, err
	}
	if row.Status != models.InboundQuarantinePending {
		return nil, ErrQuarantineNotPending
	}
	rec, err := s.ownedMessage(scope, row.InboundEmailID)
	if err != nil {
		return nil, err
	}

	// Best-effort blob cleanup, then the row: the message is gone for good.
	if s.svc.blobStore != nil {
		if rec.AttachmentsJSON != "" {
			var atts []models.InboundAttachmentMeta
			if err := json.Unmarshal([]byte(rec.AttachmentsJSON), &atts); err == nil {
				for _, a := range atts {
					if a.StorageKey != "" {
						_ = s.svc.blobStore.Delete(ctx, a.StorageKey)
					}
				}
			}
		}
		if rec.RawStorageKey != "" {
			_ = s.svc.blobStore.Delete(ctx, rec.RawStorageKey)
		}
	}
	if err := s.inboundRepo.Delete(rec.ID); err != nil {
		return nil, fmt.Errorf("delete quarantined message: %w", err)
	}

	now := time.Now().UTC()
	row.Status = models.InboundQuarantineDeleted
	row.ReviewedBy = &reviewerID
	row.ReviewedAt = &now
	if err := s.repo.Update(row); err != nil {
		return nil, fmt.Errorf("mark quarantine record deleted: %w", err)
	}
	s.publishReviewEvent("inbound.quarantine_deleted", scope, row, rec, reviewerID)
	return row, nil
}

// ownedMessage loads an inbound message and asserts it belongs to the
// caller's workspace (defense in depth on top of the scope-checked
// quarantine record — the reference itself is never trusted across scopes).
func (s *QuarantineService) ownedMessage(scope repositories.ResourceScope, inboundEmailID uint) (*models.InboundEmail, error) {
	rec, err := s.inboundRepo.FindByID(inboundEmailID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrQuarantineMessageMissing
		}
		return nil, err
	}
	if !repositories.OwnsResource(scope, rec.UserID, rec.WorkspaceID) {
		return nil, ErrQuarantineMessageMissing
	}
	return rec, nil
}

// publishReviewEvent emits the audit event for a review disposition into the
// events substrate, attributed to the acting reviewer (not the tenant).
func (s *QuarantineService) publishReviewEvent(eventType string, scope repositories.ResourceScope, row *models.InboundQuarantine, rec *models.InboundEmail, reviewerID uint) {
	if s.svc == nil || s.svc.bus == nil {
		return
	}
	meta := map[string]any{
		"quarantine_id":   row.ID,
		"inbound_id":      rec.UUID,
		"sender":          rec.Sender,
		"recipients":      []string(rec.Recipients),
		"subject":         rec.Subject,
		"quarantine_reason": row.Reason,
	}
	if row.RouteID != nil {
		meta["route_id"] = *row.RouteID
	}
	actor := reviewerID
	s.svc.bus.PublishScoped(
		scope.WorkspaceID,
		models.EventCategoryEmail,
		eventType,
		&actor,
		"",
		"",
		"Quarantine review: "+eventType+" for "+rec.Sender,
		meta,
	)
}
