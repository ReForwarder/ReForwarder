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
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// InboundForwardTargetHandler serves the confirmed forward-destination surface
// (OB-2, ADR-002 decision 2): workspace-scoped management (create with the
// one-time confirmation token, list, revoke) plus the PUBLIC single-use
// confirmation link the operator sends to the target mailbox manually.
type InboundForwardTargetHandler struct {
	svc  *inbound.ForwardTargetService
	base string // public API base URL for the plain-text confirm URL
}

func NewInboundForwardTargetHandler(svc *inbound.ForwardTargetService, apiBaseURL string) *InboundForwardTargetHandler {
	return &InboundForwardTargetHandler{svc: svc, base: apiBaseURL}
}

// ForwardTargetCreateRequest names one destination address to confirm.
type ForwardTargetCreateRequest struct {
	Target string `json:"target" required:"true" doc:"Destination email address. The address receives a plain-text confirmation URL the operator sends manually; forwarding to it only runs after the single-use link is used."`
}

// ForwardTargetListRequest is the list query.
type ForwardTargetListRequest struct {
	Page int `query:"page" default:"0"`
	Size int `query:"size" default:"50"`
}

// ForwardTargetActionRequest identifies one target row by ID.
type ForwardTargetActionRequest struct {
	ID uint `param:"id" doc:"Forward target ID"`
}

// ForwardTargetConfirmRequest carries the token from the confirmation link.
type ForwardTargetConfirmRequest struct {
	Token string `param:"token" doc:"Single-use confirmation token from the plain-text confirm URL"`
}

// ForwardTargetConfirmResponse is the public confirmation result.
type ForwardTargetConfirmResponse struct {
	Confirmed bool   `json:"confirmed"`
	Target    string `json:"target,omitempty"`
}

// Create registers a forward target and returns it with the one-time
// plain-text confirmation URL. Workspace-edit gated; the raw token is shown
// exactly once (only its hash is stored).
func (h *InboundForwardTargetHandler) Create(c *okapi.Context, req *ForwardTargetCreateRequest) error {
	if err := requireEdit(c); err != nil {
		return err
	}
	res, err := h.svc.Create(getScope(c), req.Target, h.confirmBaseURL(c))
	if err != nil {
		switch {
		case errors.Is(err, inbound.ErrForwardTargetNeedsWorkspace):
			return c.JSON(http.StatusUnprocessableEntity, dto.ErrorResponseBody{
				Success: false,
				Error: &dto.ErrorInfo{
					Code:    "forward_target_workspace_required",
					Message: "forward targets require an active workspace",
				},
			})
		case errors.Is(err, inbound.ErrInvalidForwardTarget):
			return c.JSON(http.StatusUnprocessableEntity, dto.ErrorResponseBody{
				Success: false,
				Error: &dto.ErrorInfo{
					Code:    "invalid_forward_target",
					Message: "forward target is not a valid email address",
				},
			})
		}
		return c.AbortInternalServerError("failed to create forward target", err)
	}
	return created(c, res)
}

// List returns the scope's forward targets (confirmed state included; never
// the token hash — the json:"-" tag keeps it out of every response).
func (h *InboundForwardTargetHandler) List(c *okapi.Context, req *ForwardTargetListRequest) error {
	page, size, offset := normalizePageParams(req.Page, req.Size)
	items, total, err := h.svc.List(getScope(c), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list forward targets", err)
	}
	return paginated(c, items, total, page, size)
}

// Revoke deletes a forward target. Workspace-edit gated; a foreign or missing
// ID is a 404 (never confirming existence across scopes). After a revoke the
// forwarding executor refuses the target until a fresh confirmation exists.
func (h *InboundForwardTargetHandler) Revoke(c *okapi.Context, req *ForwardTargetActionRequest) error {
	if err := requireEdit(c); err != nil {
		return err
	}
	row, err := h.svc.RevokeOwned(getScope(c), req.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.AbortNotFound("forward target not found")
		}
		return c.AbortInternalServerError("failed to revoke forward target", err)
	}
	return ok(c, row)
}

// Confirm consumes a single-use confirmation token (public: the operator
// sends the plain-text URL to the target mailbox, whose owner may not be a
// dashboard user — the token itself is the credential). A consumed, revoked,
// or unknown token answers 404 with confirmed=false; the envelope is kept
// because every REST response in this API uses it.
func (h *InboundForwardTargetHandler) Confirm(c *okapi.Context, req *ForwardTargetConfirmRequest) error {
	confirmed, target, err := h.svc.Confirm(req.Token)
	if err != nil {
		return c.AbortInternalServerError("failed to confirm forward target", err)
	}
	if !confirmed {
		return c.JSON(http.StatusNotFound, dto.ErrorResponseBody{
			Success: false,
			Error: &dto.ErrorInfo{
				Code:    "forward_confirm_token_invalid",
				Message: "confirmation link is invalid or was already used",
			},
		})
	}
	return ok(c, ForwardTargetConfirmResponse{Confirmed: true, Target: target})
}

// confirmBaseURL resolves the public API base for the confirm link.
func (h *InboundForwardTargetHandler) confirmBaseURL(c *okapi.Context) string {
	if h.base != "" {
		return h.base
	}
	scheme := "https"
	if c.Request().TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + c.Request().Host
}
