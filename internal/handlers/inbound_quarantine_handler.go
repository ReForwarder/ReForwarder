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
	"errors"
	"net/http"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// InboundQuarantineHandler serves the quarantine review surface (OB-2,
// ADR-002 decision 3, SF-51): list the held messages for the current
// workspace, release one (re-running routing), or permanently delete one.
// Every operation is workspace-scoped through the ownsResource pattern;
// release and delete additionally require workspace edit rights.
type InboundQuarantineHandler struct {
	qsvc *inbound.QuarantineService
}

func NewInboundQuarantineHandler(qsvc *inbound.QuarantineService) *InboundQuarantineHandler {
	return &InboundQuarantineHandler{qsvc: qsvc}
}

// QuarantineListRequest is the list query: paged, optionally filtered by
// review state and domain.
type QuarantineListRequest struct {
	Page     int    `query:"page" default:"0"`
	Size     int    `query:"size" default:"20"`
	Status   string `query:"status" doc:"Filter by review state: pending|released|deleted"`
	DomainID *uint  `query:"domain_id" doc:"Filter by domain"`
}

// QuarantineActionRequest identifies one quarantine record by ID.
type QuarantineActionRequest struct {
	ID uint `param:"id" doc:"Quarantine record ID"`
}

// QuarantineMessageSummary is the referenced inbound message's review view —
// absent when the message was permanently deleted (the tombstone row keeps
// the audit trail).
type QuarantineMessageSummary struct {
	UUID       string   `json:"uuid"`
	Sender     string   `json:"sender"`
	Recipients []string `json:"recipients"`
	Subject    string   `json:"subject"`
	Size       int64    `json:"size"`
	Status     string   `json:"status"`
}

// QuarantineListItem is one row of the review list: the quarantine record
// plus, when it still exists, the held message's summary.
type QuarantineListItem struct {
	models.InboundQuarantine
	Inbound *QuarantineMessageSummary `json:"inbound,omitempty"`
}

// List returns the workspace's quarantine records, paged, newest arrival
// first. Scope comes from the request context — cross-workspace rows are
// never returned.
func (h *InboundQuarantineHandler) List(c *okapi.Context, req *QuarantineListRequest) error {
	page, size, offset := normalizePageParams(req.Page, req.Size)

	items, total, err := h.qsvc.List(getScope(c), repositories.InboundQuarantineFilter{
		Status:   req.Status,
		DomainID: req.DomainID,
	}, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list quarantine records", err)
	}

	// Enrich with the held messages' summaries (one lookup per row; a missing
	// reference — permanently deleted — simply carries no summary).
	summaries, err := h.qsvc.Summaries(getScope(c), items)
	if err != nil {
		return c.AbortInternalServerError("failed to load quarantined messages", err)
	}
	list := make([]QuarantineListItem, 0, len(items))
	for _, q := range items {
		item := QuarantineListItem{InboundQuarantine: q}
		if rec, ok := summaries[q.InboundEmailID]; ok {
			item.Inbound = &QuarantineMessageSummary{
				UUID:       rec.UUID,
				Sender:     rec.Sender,
				Recipients: []string(rec.Recipients),
				Subject:    rec.Subject,
				Size:       rec.Size,
				Status:     string(rec.Status),
			}
		}
		list = append(list, item)
	}
	return paginated(c, list, total, page, size)
}

// Release puts a held message back through routing (ADR-002 §3). Workspace-
// edit gated; the disposition is attributed to the acting user.
func (h *InboundQuarantineHandler) Release(c *okapi.Context, req *QuarantineActionRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	row, err := h.qsvc.Release(c.Request().Context(), getScope(c), req.ID, uint(c.GetInt("user_id")))
	if err != nil {
		return h.mapActionError(c, "release", err)
	}
	return ok(c, row)
}

// Delete permanently discards a held message (message + blobs destroyed;
// the tombstone record and an audit event remain). Workspace-edit gated.
func (h *InboundQuarantineHandler) Delete(c *okapi.Context, req *QuarantineActionRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	row, err := h.qsvc.Delete(c.Request().Context(), getScope(c), req.ID, uint(c.GetInt("user_id")))
	if err != nil {
		return h.mapActionError(c, "delete", err)
	}
	return ok(c, row)
}

// mapActionError translates quarantine-service errors into honest HTTP
// answers: not-found stays a 404 (never confirming foreign rows), state
// conflicts are 409, policy failures are 422.
func (h *InboundQuarantineHandler) mapActionError(c *okapi.Context, action string, err error) error {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return c.AbortNotFound("quarantine record not found")
	case errors.Is(err, inbound.ErrQuarantineMessageMissing):
		return c.AbortNotFound("quarantined message no longer exists")
	case errors.Is(err, inbound.ErrQuarantineNotPending):
		return c.JSON(http.StatusConflict, dto.ErrorResponseBody{
			Success: false,
			Error: &dto.ErrorInfo{
				Code:    "quarantine_not_pending",
				Message: "quarantine record was already released or deleted",
			},
		})
	case errors.Is(err, inbound.ErrQuarantineUnverifiedDomain),
		errors.Is(err, inbound.ErrNoRoute):
		return c.JSON(http.StatusUnprocessableEntity, dto.ErrorResponseBody{
			Success: false,
			Error: &dto.ErrorInfo{
				Code:    "quarantine_release_policy",
				Message: "cannot release: the recipient no longer matches an enabled route on a verified domain",
			},
		})
	default:
		return c.AbortInternalServerError("failed to "+action+" quarantined message", err)
	}
}
