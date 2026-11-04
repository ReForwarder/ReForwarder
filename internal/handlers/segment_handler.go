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
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// SegmentHandler serves saved audience filters (the reference segmentation product-style segments) over the
// subscribers table, plus backend-evaluated previews of unsaved conditions.
type SegmentHandler struct {
	repo           *repositories.SegmentRepository
	subscriberRepo *repositories.SubscriberRepository
}

func NewSegmentHandler(repo *repositories.SegmentRepository, subscriberRepo *repositories.SubscriberRepository) *SegmentHandler {
	return &SegmentHandler{repo: repo, subscriberRepo: subscriberRepo}
}

type CreateSegmentRequest struct {
	Body struct {
		// Name is validated in the handler (not via the okapi required tag) so
		// every create rejection answers with the same validationFailed shape.
		Name        string                   `json:"name"`
		Description string                   `json:"description"`
		Condition   *models.SegmentCondition `json:"condition"`
	} `json:"body"`
}

type UpdateSegmentRequest struct {
	ID   int `param:"id"`
	Body struct {
		Name        string                   `json:"name"`
		Description string                   `json:"description"`
		Condition   *models.SegmentCondition `json:"condition"`
	} `json:"body"`
}

type ListSegmentsRequest struct {
	Page int `query:"page" default:"0"`
	Size int `query:"size" default:"20"`
}

type PreviewSegmentConditionRequest struct {
	Body struct {
		Condition *models.SegmentCondition `json:"condition"`
		Page      int                      `json:"page"`
		Size      int                      `json:"size"`
	} `json:"body"`
}

type GetSegmentRequest struct {
	ID int `param:"id"`
}

type DeleteSegmentRequest struct {
	ID int `param:"id"`
}

type SegmentContactsRequest struct {
	ID   int `param:"id"`
	Page int `query:"page" default:"0"`
	Size int `query:"size" default:"20"`
}

// SegmentWithCount is a segment row enriched with the live match count (same
// convention as dynamic subscriber lists).
type SegmentWithCount struct {
	models.Segment
	MatchingCount int64 `json:"matching_count"`
}

// SegmentContact is the slim subscriber projection returned by the preview and
// contacts endpoints.
type SegmentContact struct {
	ID           uint       `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	Timezone     string     `json:"timezone"`
	Language     string     `json:"language"`
	CreatedAt    time.Time  `json:"created_at"`
	SubscribedAt *time.Time `json:"subscribed_at"`
}

// SegmentPreviewResponse is the response shape of POST /segments/preview and
// GET /segments/{id}/contacts: a total count plus one page of contacts.
type SegmentPreviewResponse struct {
	Count      int64            `json:"count"`
	Page       int              `json:"page"`
	Size       int              `json:"size"`
	TotalPages int              `json:"total_pages"`
	Contacts   []SegmentContact `json:"contacts"`
}

// segmentContacts projects subscribers onto the slim contact shape.
func segmentContacts(subscribers []models.Subscriber) []SegmentContact {
	contacts := make([]SegmentContact, 0, len(subscribers))
	for _, s := range subscribers {
		contacts = append(contacts, SegmentContact{
			ID:           s.ID,
			Email:        s.Email,
			Name:         s.Name,
			Status:       string(s.Status),
			Timezone:     s.Timezone,
			Language:     s.Language,
			CreatedAt:    s.CreatedAt,
			SubscribedAt: s.SubscribedAt,
		})
	}
	return contacts
}

// segmentPreview builds the shared preview/contacts response from a count and
// a page of subscribers.
func segmentPreview(count int64, page, size int, subscribers []models.Subscriber) SegmentPreviewResponse {
	totalPages := 0
	if size > 0 {
		totalPages = int((count + int64(size) - 1) / int64(size))
	}
	return SegmentPreviewResponse{
		Count:      count,
		Page:       page,
		Size:       size,
		TotalPages: totalPages,
		Contacts:   segmentContacts(subscribers),
	}
}

// Create validates and stores a segment. Condition problems are returned as a
// 400 with a `validation_errors` list naming the offending group/rule.
func (h *SegmentHandler) Create(c *okapi.Context, req *CreateSegmentRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	if req.Body.Name == "" {
		return validationFailed(c, []string{"name is required"})
	}
	if req.Body.Condition == nil {
		return validationFailed(c, []string{"condition is required"})
	}
	if errs := repositories.ValidateSegmentCondition(*req.Body.Condition); len(errs) > 0 {
		return validationFailed(c, errs)
	}
	scope := getScope(c)

	segment := &models.Segment{
		UserID:      scope.UserID,
		WorkspaceID: scope.WorkspaceID,
		Name:        req.Body.Name,
		Description: req.Body.Description,
		Condition:   *req.Body.Condition,
	}
	if err := h.repo.Create(segment); err != nil {
		return c.AbortInternalServerError("failed to create segment")
	}
	return created(c, SegmentWithCount{Segment: *segment, MatchingCount: h.matchingCount(scope, segment.Condition)})
}

// List returns the workspace's segments with live matching counts.
func (h *SegmentHandler) List(c *okapi.Context, req *ListSegmentsRequest) error {
	page, size, offset := normalizePageParams(req.Page, req.Size)
	scope := getScope(c)
	items, total, err := h.repo.FindByScope(scope, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list segments")
	}

	result := make([]SegmentWithCount, len(items))
	for i, item := range items {
		result[i] = SegmentWithCount{Segment: item, MatchingCount: h.matchingCount(scope, item.Condition)}
	}
	return paginated(c, result, total, page, size)
}

// matchingCount evaluates a condition against the current subscribers; the
// count is best-effort (a broken condition yields 0, matching the dynamic-list
// list-handler convention) because the stored condition was validated at
// write time.
func (h *SegmentHandler) matchingCount(scope repositories.ResourceScope, cond models.SegmentCondition) int64 {
	count, err := h.repo.CountByCondition(scope, cond)
	if err != nil {
		return 0
	}
	return count
}

func (h *SegmentHandler) Get(c *okapi.Context, req *GetSegmentRequest) error {
	segment, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, segment.UserID, segment.WorkspaceID) {
		return c.AbortNotFound("segment not found")
	}
	return ok(c, SegmentWithCount{Segment: *segment, MatchingCount: h.matchingCount(getScope(c), segment.Condition)})
}

func (h *SegmentHandler) Update(c *okapi.Context, req *UpdateSegmentRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	segment, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, segment.UserID, segment.WorkspaceID) {
		return c.AbortNotFound("segment not found")
	}

	if req.Body.Condition != nil {
		if errs := repositories.ValidateSegmentCondition(*req.Body.Condition); len(errs) > 0 {
			return validationFailed(c, errs)
		}
		segment.Condition = *req.Body.Condition
	}
	if req.Body.Name != "" {
		segment.Name = req.Body.Name
	}
	if req.Body.Description != "" {
		segment.Description = req.Body.Description
	}
	now := time.Now()
	segment.UpdatedAt = &now

	if err := h.repo.Update(segment); err != nil {
		return c.AbortInternalServerError("failed to update segment")
	}
	return ok(c, SegmentWithCount{Segment: *segment, MatchingCount: h.matchingCount(getScope(c), segment.Condition)})
}

func (h *SegmentHandler) Delete(c *okapi.Context, req *DeleteSegmentRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	segment, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, segment.UserID, segment.WorkspaceID) {
		return c.AbortNotFound("segment not found")
	}
	if err := h.repo.Delete(segment.ID); err != nil {
		return c.AbortInternalServerError("failed to delete segment")
	}
	return noContent(c)
}

// Preview evaluates an unsaved condition server-side: a COUNT over the whole
// audience plus one paged page of contacts.
func (h *SegmentHandler) Preview(c *okapi.Context, req *PreviewSegmentConditionRequest) error {
	if req.Body.Condition == nil {
		return validationFailed(c, []string{"condition is required"})
	}
	if errs := repositories.ValidateSegmentCondition(*req.Body.Condition); len(errs) > 0 {
		return validationFailed(c, errs)
	}
	page, size := req.Body.Page, req.Body.Size
	if size <= 0 || size > 100 {
		size = 20
	}
	if page < 0 {
		page = 0
	}
	scope := getScope(c)

	subscribers, count, err := h.repo.FindSubscribersByCondition(scope, *req.Body.Condition, size, page*size)
	if err != nil {
		return c.AbortInternalServerError("failed to evaluate segment")
	}
	return ok(c, segmentPreview(count, page, size, subscribers))
}

// Contacts returns the same shape as Preview for a saved segment.
func (h *SegmentHandler) Contacts(c *okapi.Context, req *SegmentContactsRequest) error {
	segment, err := h.repo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, segment.UserID, segment.WorkspaceID) {
		return c.AbortNotFound("segment not found")
	}
	page, size, offset := normalizePageParams(req.Page, req.Size)
	scope := getScope(c)

	subscribers, count, err := h.repo.FindSubscribersByCondition(scope, segment.Condition, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to evaluate segment")
	}
	return ok(c, segmentPreview(count, page, size, subscribers))
}
