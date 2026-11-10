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
	"fmt"
	"strings"
	"time"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// AdminMailboxHandler is the OB-5 mailbox-administration surface (ADR-005
// decision 3, SF-65/72/80 admin slices): provision a mailbox for a user, the
// platform-wide paged listing, quota changes, suspend/reactivate, cascade
// delete per the ADR-008 ownership chain, and the per-mailbox usage report.
// Every route lives in the /admin group (jwtAdminAuth) — ADR-008's "admin
// scope is deployment-wide"; the actor is the platform admin, not a tenant
// member, so the workspace-scoped ownsResource pattern does not bind here.
// Every mutating action is audited as admin.mailbox.* (SF-74); reads (list,
// usage) are not, matching the rest of the admin surface.
type AdminMailboxHandler struct {
	store AdminMailboxStore
	// db enriches rows with owner identity and validates provisioning
	// targets (user exists, workspace membership) — the same direct-handle
	// pattern AdminWorkspaceHandler uses for its joins.
	db    *gorm.DB
	audit *audit.Logger
}

// AdminMailboxStore is the seam this handler consumes: the admin-scoped
// mailstore operations (implemented by *mailstore.Service — see
// internal/services/mailstore/admin.go). Kept as an interface so the handler
// battery can run over an in-memory fake without a database.
type AdminMailboxStore interface {
	AdminListMailboxes(filter mailstore.AdminMailboxFilter, limit, offset int) ([]models.Mailbox, int64, error)
	AdminGetMailbox(id uint) (*models.Mailbox, error)
	AdminProvisionMailbox(userID uint, workspaceID *uint, name string, quotaBytes, quotaMessages int64) (*models.Mailbox, error)
	AdminSetMailboxQuota(id uint, quotaBytes, quotaMessages int64) (*models.Mailbox, error)
	AdminSetMailboxStatus(id uint, status models.MailboxStatus) (*models.Mailbox, error)
	AdminDeleteMailbox(id uint) error
	AdminMailboxUsage(id uint) (*mailstore.MailboxUsageReport, error)
	AdminUserExists(userID uint) (bool, error)
	AdminUserInWorkspace(userID, workspaceID uint) (bool, error)
}

// compile-time pin: the real service satisfies the seam.
var _ AdminMailboxStore = (*mailstore.Service)(nil)

// NewAdminMailboxHandler wires the handler onto the mailstore-backed store.
// A nil store is legal at construction (feature not configured) and every
// endpoint answers 503 — see the wiring note in internal/routes/routes.go.
func NewAdminMailboxHandler(store AdminMailboxStore, db *gorm.DB) *AdminMailboxHandler {
	return &AdminMailboxHandler{store: store, db: db}
}

// SetAuditLogger wires the platform audit logger. Nil (default) disables
// auditing; the endpoints still work.
func (h *AdminMailboxHandler) SetAuditLogger(a *audit.Logger) {
	h.audit = a
}

// AdminMailboxRow is one line of the platform mailbox table: the mailstore's
// counters plus the owner identity an administrator needs to act.
type AdminMailboxRow struct {
	ID            uint                 `json:"id"`
	UserID        uint                 `json:"user_id"`
	OwnerName     string               `json:"owner_name"`
	OwnerEmail    string               `json:"owner_email"`
	WorkspaceID   *uint                `json:"workspace_id,omitempty"`
	Name          string               `json:"name"`
	QuotaBytes    int64                `json:"quota_bytes"`
	QuotaMessages int64                `json:"quota_messages"`
	UsedBytes     int64                `json:"used_bytes"`
	MessageCount  int64                `json:"message_count"`
	Status        models.MailboxStatus `json:"status"`
	CreatedAt     time.Time            `json:"created_at"`
}

// AdminMailboxUsageResponse is the quota/usage report for one mailbox
// (mailstore.MailboxUsageReport's counters, verbatim shape).
type AdminMailboxUsageResponse = mailstore.MailboxUsageReport

type AdminListMailboxesRequest struct {
	Page      int    `query:"page" default:"0"`
	Size      int    `query:"size" default:"20"`
	Search    string `query:"search" doc:"Case-insensitive substring of the mailbox name (wildcards are matched literally)"`
	User      int    `query:"user" doc:"Restrict to mailboxes owned by this user id"`
	Workspace int    `query:"workspace" doc:"Restrict to mailboxes bound to this workspace id"`
	Status    string `query:"status" doc:"Filter by lifecycle status: active, suspended or closed"`
}

type AdminMailboxIDRequest struct {
	ID int `param:"id"`
}

type AdminProvisionMailboxRequest struct {
	Body struct {
		UserID uint `json:"user_id" required:"true" doc:"Owner of the new mailbox"`
		// Name is the mailbox identity (e.g. the hosted address); unique per owner.
		Name          string `json:"name" required:"true" minLength:"1" maxLength:"255"`
		QuotaBytes    int64  `json:"quota_bytes" doc:"Byte quota; 0 = unlimited"`
		QuotaMessages int64  `json:"quota_messages" doc:"Message-count quota; 0 = unlimited"`
		WorkspaceID   uint   `json:"workspace_id" doc:"Optional workspace binding; the user must be a member"`
	} `json:"body"`
}

type AdminUpdateMailboxRequest struct {
	ID   int `param:"id"`
	Body struct {
		// Status accepts only active and suspended: suspend is the admin
		// disable, reactivate the recovery. Closed is terminal-on-write and
		// never what an operator means by "disable" — deletion is the
		// removal path.
		Status        *string `json:"status,omitempty" enum:"active,suspended"`
		QuotaBytes    *int64  `json:"quota_bytes,omitempty" doc:"New byte quota; 0 = unlimited"`
		QuotaMessages *int64  `json:"quota_messages,omitempty" doc:"New message-count quota; 0 = unlimited"`
	} `json:"body"`
}

// List serves GET /admin/mailboxes — the paged platform-wide mailbox table.
func (h *AdminMailboxHandler) List(c *okapi.Context, req *AdminListMailboxesRequest) error {
	if h.store == nil {
		return c.AbortServiceUnavailable("mailbox administration is not available")
	}
	page, size, offset := normalizePageParams(req.Page, req.Size)

	filter := mailstore.AdminMailboxFilter{Search: req.Search}
	if req.User > 0 {
		filter.UserID = uint(req.User)
	}
	if req.Workspace > 0 {
		filter.WorkspaceID = uint(req.Workspace)
	}
	switch req.Status {
	case "":
	case string(models.MailboxStatusActive), string(models.MailboxStatusSuspended), string(models.MailboxStatusClosed):
		filter.Status = models.MailboxStatus(req.Status)
	default:
		return c.AbortBadRequest("status must be active, suspended or closed")
	}

	rows0, total, err := h.store.AdminListMailboxes(filter, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list mailboxes")
	}
	rows := h.rows(rows0)
	return paginated(c, rows, total, page, size)
}

// Create serves POST /admin/mailboxes — provision a mailbox for a user
// (name + quotas, optional workspace binding), audited.
func (h *AdminMailboxHandler) Create(c *okapi.Context, req *AdminProvisionMailboxRequest) error {
	if h.store == nil {
		return c.AbortServiceUnavailable("mailbox administration is not available")
	}
	body := req.Body
	if body.UserID == 0 {
		return c.AbortBadRequest("user_id is required")
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return c.AbortBadRequest("name is required")
	}

	exists, err := h.store.AdminUserExists(body.UserID)
	if err != nil {
		return c.AbortInternalServerError("failed to validate the mailbox owner")
	}
	if !exists {
		return c.AbortNotFound(fmt.Sprintf("user %d not found", body.UserID))
	}

	var workspaceID *uint
	if body.WorkspaceID > 0 {
		member, err := h.store.AdminUserInWorkspace(body.UserID, body.WorkspaceID)
		if err != nil {
			return c.AbortInternalServerError("failed to validate the workspace binding")
		}
		if !member {
			return c.AbortBadRequest(fmt.Sprintf("user %d is not a member of workspace %d", body.UserID, body.WorkspaceID))
		}
		wid := body.WorkspaceID
		workspaceID = &wid
	}

	mb, err := h.store.AdminProvisionMailbox(body.UserID, workspaceID, name, body.QuotaBytes, body.QuotaMessages)
	if err != nil {
		switch {
		case errors.Is(err, mailstore.ErrMailboxNameTaken):
			return c.AbortConflict(fmt.Sprintf("user %d already has a mailbox named %q", body.UserID, name))
		case errors.Is(err, mailstore.ErrQuotaExceeded): // not a provisioning error; defensive
			return c.AbortBadRequest("invalid quota")
		default:
			return c.AbortBadRequest(provisionError(err))
		}
	}

	if h.audit != nil {
		h.audit.Log(uint(c.GetInt("user_id")), c.GetString("email"), c.RealIP(),
			"admin.mailbox.provisioned", fmt.Sprintf("provisioned mailbox %s for user %d", mb.Name, mb.UserID),
			map[string]any{
				"mailbox_id": mb.ID, "user_id": mb.UserID, "name": mb.Name,
				"quota_bytes": mb.QuotaBytes, "quota_messages": mb.QuotaMessages,
				"workspace_id": workspaceID,
			})
	}
	rows := h.rows([]models.Mailbox{*mb})
	return created(c, rows[0])
}

// Update serves PATCH /admin/mailboxes/{id} — quota change and/or
// suspend/reactivate, audited per change.
func (h *AdminMailboxHandler) Update(c *okapi.Context, req *AdminUpdateMailboxRequest) error {
	if h.store == nil {
		return c.AbortServiceUnavailable("mailbox administration is not available")
	}
	body := req.Body
	if body.Status == nil && body.QuotaBytes == nil && body.QuotaMessages == nil {
		return c.AbortBadRequest("nothing to update: provide status, quota_bytes or quota_messages")
	}

	before, err := h.store.AdminGetMailbox(uint(req.ID))
	if err != nil {
		return mailboxStoreError(c, err)
	}

	quotaChanged := body.QuotaBytes != nil || body.QuotaMessages != nil
	if quotaChanged {
		qb := before.QuotaBytes
		if body.QuotaBytes != nil {
			qb = *body.QuotaBytes
		}
		qm := before.QuotaMessages
		if body.QuotaMessages != nil {
			qm = *body.QuotaMessages
		}
		if qb < 0 || qm < 0 {
			return c.AbortBadRequest("quotas must be >= 0 (0 = unlimited)")
		}
		if _, err := h.store.AdminSetMailboxQuota(before.ID, qb, qm); err != nil {
			return mailboxStoreError(c, err)
		}
		if h.audit != nil {
			h.audit.Log(uint(c.GetInt("user_id")), c.GetString("email"), c.RealIP(),
				"admin.mailbox.updated", fmt.Sprintf("changed quotas of mailbox %s (user %d)", before.Name, before.UserID),
				map[string]any{
					"mailbox_id": before.ID, "user_id": before.UserID,
					"quota_bytes": qb, "quota_messages": qm,
				})
		}
	}

	updated := before
	if body.Status != nil {
		status := models.MailboxStatus(*body.Status)
		if _, err := h.store.AdminSetMailboxStatus(before.ID, status); err != nil {
			return mailboxStoreError(c, err)
		}
		if h.audit != nil {
			action, verb := "admin.mailbox.suspended", "suspended"
			if status == models.MailboxStatusActive {
				action, verb = "admin.mailbox.reactivated", "reactivated"
			}
			h.audit.Log(uint(c.GetInt("user_id")), c.GetString("email"), c.RealIP(),
				action, fmt.Sprintf("%s mailbox %s (user %d)", verb, before.Name, before.UserID),
				map[string]any{"mailbox_id": before.ID, "user_id": before.UserID, "name": before.Name})
		}
		if updated, err = h.store.AdminGetMailbox(before.ID); err != nil {
			return mailboxStoreError(c, err)
		}
	} else if quotaChanged {
		if updated, err = h.store.AdminGetMailbox(before.ID); err != nil {
			return mailboxStoreError(c, err)
		}
	}

	rows := h.rows([]models.Mailbox{*updated})
	return ok(c, rows[0])
}

// Delete serves DELETE /admin/mailboxes/{id} — the confirmed cascade delete
// (the dashboard confirms in a modal before calling; the server-side cascade
// removes folders, messages and blob references in one transaction per
// ADR-008's mailbox ownership chain). Audited.
func (h *AdminMailboxHandler) Delete(c *okapi.Context, req *AdminMailboxIDRequest) error {
	if h.store == nil {
		return c.AbortServiceUnavailable("mailbox administration is not available")
	}
	before, err := h.store.AdminGetMailbox(uint(req.ID))
	if err != nil {
		return mailboxStoreError(c, err)
	}
	if err := h.store.AdminDeleteMailbox(before.ID); err != nil {
		return mailboxStoreError(c, err)
	}

	if h.audit != nil {
		h.audit.Log(uint(c.GetInt("user_id")), c.GetString("email"), c.RealIP(),
			"admin.mailbox.deleted", fmt.Sprintf("deleted mailbox %s (user %d) with %d messages", before.Name, before.UserID, before.MessageCount),
			map[string]any{
				"mailbox_id": before.ID, "user_id": before.UserID, "name": before.Name,
				"message_count": before.MessageCount, "used_bytes": before.UsedBytes,
			})
	}
	return ok(c, dto.MessageData{
		Message: fmt.Sprintf("mailbox %s deleted with its folders and messages; unreferenced blobs are reclaimed by the GC sweep", before.Name),
	})
}

// Usage serves GET /admin/mailboxes/{id}/usage — the mailstore's counters
// (quota basis raw bytes / message count) plus the dedup-aware stored-blob
// totals.
func (h *AdminMailboxHandler) Usage(c *okapi.Context, req *AdminMailboxIDRequest) error {
	if h.store == nil {
		return c.AbortServiceUnavailable("mailbox administration is not available")
	}
	report, err := h.store.AdminMailboxUsage(uint(req.ID))
	if err != nil {
		return mailboxStoreError(c, err)
	}
	return ok(c, AdminMailboxUsageResponse(*report))
}

// rows maps mailstore rows onto the admin table shape, enriching each with
// its owner identity in one query (the AdminWorkspaceHandler pattern).
func (h *AdminMailboxHandler) rows(mailboxes []models.Mailbox) []AdminMailboxRow {
	owners := h.owners(mailboxes)
	out := make([]AdminMailboxRow, 0, len(mailboxes))
	for i := range mailboxes {
		mb := &mailboxes[i]
		out = append(out, AdminMailboxRow{
			ID:            mb.ID,
			UserID:        mb.UserID,
			OwnerName:     owners[mb.UserID].Name,
			OwnerEmail:    owners[mb.UserID].Email,
			WorkspaceID:   mb.WorkspaceID,
			Name:          mb.Name,
			QuotaBytes:    mb.QuotaBytes,
			QuotaMessages: mb.QuotaMessages,
			UsedBytes:     mb.UsedBytes,
			MessageCount:  mb.MessageCount,
			Status:        mb.Status,
			CreatedAt:     mb.CreatedAt,
		})
	}
	return out
}

// owners resolves the owning accounts for a page of mailboxes in one query.
func (h *AdminMailboxHandler) owners(mailboxes []models.Mailbox) map[uint]models.User {
	out := map[uint]models.User{}
	if h.db == nil || len(mailboxes) == 0 {
		return out
	}
	ids := make([]uint, 0, len(mailboxes))
	seen := map[uint]struct{}{}
	for i := range mailboxes {
		id := mailboxes[i].UserID
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	var rows []models.User
	if err := h.db.Select("id", "name", "email").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return out
	}
	for i := range rows {
		out[rows[i].ID] = rows[i]
	}
	return out
}

// mailboxStoreError maps the store's typed errors onto HTTP statuses.
func mailboxStoreError(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, mailstore.ErrMailboxNotFound):
		return c.AbortNotFound("mailbox not found")
	case errors.Is(err, mailstore.ErrNotMailboxOwner):
		// Unreachable from the admin surface by construction; kept typed.
		return c.AbortForbidden("mailbox belongs to another user")
	default:
		return c.AbortInternalServerError("mailbox operation failed")
	}
}

// provisionError extracts a presentable message from a provisioning failure
// (validation errors carry their own text).
func provisionError(err error) string {
	const prefix = "mailstore: "
	msg := err.Error()
	if len(msg) > len(prefix) && msg[:len(prefix)] == prefix {
		return msg[len(prefix):]
	}
	return "could not provision mailbox"
}
