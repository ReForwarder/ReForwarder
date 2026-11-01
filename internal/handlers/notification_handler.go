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
	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/inbox"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// NotificationHandler serves a user's in-app inbox: the header bell, the
// notifications page, and the alert banner on the workspace dashboard.
//
// Everything is scoped to the authenticated user. There is no cross-user read
// path here at all, so a notification cannot leak between accounts.
type NotificationHandler struct {
	repo  *repositories.NotificationRepository
	inbox *inbox.Service
}

func NewNotificationHandler(repo *repositories.NotificationRepository, svc *inbox.Service) *NotificationHandler {
	return &NotificationHandler{repo: repo, inbox: svc}
}

type ListNotificationsRequest struct {
	Unread   bool   `query:"unread" doc:"Only notifications the user has not read"`
	Open     bool   `query:"open" doc:"Only live conditions the user has not dismissed"`
	Category string `query:"category" enum:"domains,deliverability,security,messages,platform"`
	Before   int    `query:"before" doc:"Keyset cursor: the id of the last row already seen"`
	Limit    int    `query:"limit" default:"30"`
	// Scoped restricts the listing to the active workspace. Platform
	// announcements have no workspace and are always included.
	Scoped bool `query:"scoped" doc:"Restrict to the active workspace"`
}

type NotificationIDsRequest struct {
	Body struct {
		IDs []uint `json:"ids"`
	} `json:"body"`
}

// NotificationCounts drives the bell badge and tells the dashboard whether the
// banner has anything to show without fetching it.
type NotificationCounts struct {
	Unread int64 `json:"unread"`
	Open   int64 `json:"open"`
}

func (h *NotificationHandler) List(c *okapi.Context, req *ListNotificationsRequest) error {
	scope := getScope(c)

	filter := repositories.NotificationFilter{
		UnreadOnly: req.Unread,
		OpenOnly:   req.Open,
		Category:   models.NotificationCategory(req.Category),
	}
	if req.Scoped {
		filter.WorkspaceID = scope.WorkspaceID
	}

	items, err := h.repo.List(scope.UserID, filter, uint(req.Before), req.Limit)
	if err != nil {
		return c.AbortInternalServerError("failed to list notifications")
	}
	return ok(c, items)
}

// Counts returns the unread badge and how many live items the current workspace
// has. Unread is deliberately not workspace-scoped: a badge that hid news from
// another workspace would train people to ignore it.
func (h *NotificationHandler) Counts(c *okapi.Context) error {
	scope := getScope(c)
	unread, open, err := h.repo.Counts(scope.UserID, scope.WorkspaceID)
	if err != nil {
		return c.AbortInternalServerError("failed to count notifications")
	}
	return ok(c, NotificationCounts{Unread: unread, Open: open})
}

// Banner returns what the workspace dashboard shows above the fold: live,
// undismissed items for this workspace, worst first.
func (h *NotificationHandler) Banner(c *okapi.Context) error {
	scope := getScope(c)
	items, err := h.repo.Open(scope.UserID, scope.WorkspaceID)
	if err != nil {
		return c.AbortInternalServerError("failed to load notifications")
	}
	return ok(c, items)
}

func (h *NotificationHandler) MarkRead(c *okapi.Context, req *NotificationIDsRequest) error {
	if err := h.repo.MarkRead(getScope(c).UserID, req.Body.IDs); err != nil {
		return c.AbortInternalServerError("failed to mark notifications read")
	}
	return ok(c, dto.MessageData{Message: "notifications marked read"})
}

func (h *NotificationHandler) MarkAllRead(c *okapi.Context) error {
	if err := h.repo.MarkAllRead(getScope(c).UserID, nil); err != nil {
		return c.AbortInternalServerError("failed to mark notifications read")
	}
	return ok(c, dto.MessageData{Message: "all notifications marked read"})
}

// Dismiss hides items from the dashboard banner.
//
// A dismissal is not permanent silence: it is bound to the condition as it
// stands now, and the next scan re-arms the item if the condition materially
// changes. Dismissing "3 domains not verified" stays dismissed at three and
// comes back at four.
func (h *NotificationHandler) Dismiss(c *okapi.Context, req *NotificationIDsRequest) error {
	if err := h.repo.Dismiss(getScope(c).UserID, req.Body.IDs); err != nil {
		return c.AbortInternalServerError("failed to dismiss notifications")
	}
	return ok(c, dto.MessageData{Message: "notifications dismissed"})
}

// DismissAll clears the whole banner for the active workspace.
func (h *NotificationHandler) DismissAll(c *okapi.Context) error {
	scope := getScope(c)
	if err := h.repo.DismissAll(scope.UserID, scope.WorkspaceID); err != nil {
		return c.AbortInternalServerError("failed to dismiss notifications")
	}
	return ok(c, dto.MessageData{Message: "notifications dismissed"})
}
