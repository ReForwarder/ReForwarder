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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/filterengine"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// FilterScriptHandler is the SF-14 per-mailbox filter-script management
// surface (the ADR-006 amendment): CRUD over the session user's own scripts
// — the webmail-side per-user gate. It mirrors the mail handler's posture:
// strictly per session user (a foreign script reads as 404 exactly like a
// missing one), scripts validated through the SAME strict evaluator the
// engine runs (an unknown field/operator is a 400 — the fail-closed grammar
// never reaches the table), and the mailbox binding validated against the
// session user's own mailboxes.
type FilterScriptHandler struct {
	scripts *repositories.FilterScriptRepository
	store   *mailstore.Service
}

func NewFilterScriptHandler(scripts *repositories.FilterScriptRepository, store *mailstore.Service) *FilterScriptHandler {
	return &FilterScriptHandler{scripts: scripts, store: store}
}

// ─── Requests / responses ────────────────────────────────────────────────────

type FilterScriptListRequest struct {
	Mailbox uint `query:"mailbox" doc:"Mailbox ID; the session user's own"`
}

type FilterScriptCreateRequest struct {
	Body struct {
		Mailbox    uint                 `json:"mailbox" required:"true" doc:"Mailbox ID; the session user's own"`
		Name       string               `json:"name" required:"true" minLength:"1" maxLength:"255"`
		Priority   int                  `json:"priority" doc:"Evaluation order (lower first); first match wins"`
		Enabled    *bool                `json:"enabled" doc:"Defaults to true"`
		Conditions []models.FilterRule  `json:"conditions" doc:"Envelope-fact rules (subject/from/text_body/html_body/recipients/message_size)"`
		Actions    []filterengine.ScriptAction `json:"actions" required:"true" doc:"The ordered operations (fileinto/redirect/discard/keep)"`
	} `json:"body"`
}

type FilterScriptPatchRequest struct {
	ID  int `param:"id"`
	Body struct {
		Name       *string              `json:"name" maxLength:"255"`
		Priority   *int                 `json:"priority"`
		Enabled    *bool                `json:"enabled"`
		Conditions *[]models.FilterRule `json:"conditions"`
		Actions    *[]filterengine.ScriptAction `json:"actions"`
	} `json:"body"`
}

type FilterScriptIDRequest struct {
	ID int `param:"id"`
}

type FilterScriptListResponse struct {
	Scripts []models.FilterScript `json:"scripts"`
}

// ─── Validation ──────────────────────────────────────────────────────────────

// validateFilterScript runs the condition set through the SAME strict
// evaluator the engine runs and validates the action list.
func validateFilterScript(conditions []models.FilterRule, actions []filterengine.ScriptAction) error {
	if err := filterengine.ValidateScript(conditions, actions); err != nil {
		return err
	}
	return nil
}

// ─── Endpoints ───────────────────────────────────────────────────────────────

// ListFilterScripts returns the session user's scripts for one mailbox, in
// evaluation order.
func (h *FilterScriptHandler) ListFilterScripts(c *okapi.Context, req *FilterScriptListRequest) error {
	if h.scripts == nil {
		return mailUnavailable(c)
	}
	userID, _ := mailScope(c)
	scripts, err := h.scripts.ListForUser(userID, req.Mailbox)
	if err != nil {
		return mailInternal(c, "failed to load filter scripts", err)
	}
	if scripts == nil {
		scripts = []models.FilterScript{}
	}
	return ok(c, FilterScriptListResponse{Scripts: scripts})
}

// CreateFilterScript adds a script bound to one of the session user's
// mailboxes.
func (h *FilterScriptHandler) CreateFilterScript(c *okapi.Context, req *FilterScriptCreateRequest) error {
	if h.scripts == nil || h.store == nil {
		return mailUnavailable(c)
	}
	userID, _ := mailScope(c)
	if req.Body.Mailbox == 0 {
		return c.AbortBadRequest("mailbox is required")
	}
	if _, err := h.store.GetMailbox(req.Body.Mailbox, userID, 0); err != nil {
		return mailAbort(c, err)
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" || len(name) > 255 {
		return c.AbortBadRequest("name must be 1..255 characters")
	}
	if err := validateFilterScript(req.Body.Conditions, req.Body.Actions); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	acts, _ := json.Marshal(req.Body.Actions)
	enabled := true
	if req.Body.Enabled != nil {
		enabled = *req.Body.Enabled
	}
	script := &models.FilterScript{
		UserID:      userID,
		MailboxID:   req.Body.Mailbox,
		Name:        name,
		Priority:    req.Body.Priority,
		Enabled:     enabled,
		Conditions:  models.FilterRules(req.Body.Conditions),
		ActionsJSON: string(acts),
	}
	if err := h.scripts.Create(script); err != nil {
		return mailInternal(c, "failed to create the filter script", err)
	}
	return created(c, script)
}

// PatchFilterScript updates one of the session user's scripts; a foreign
// script reads as 404 exactly like a missing one.
func (h *FilterScriptHandler) PatchFilterScript(c *okapi.Context, req *FilterScriptPatchRequest) error {
	if h.scripts == nil {
		return mailUnavailable(c)
	}
	userID, _ := mailScope(c)
	script, err := h.scripts.FindByIDForUser(uint(req.ID), userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.AbortNotFound("filter script not found")
		}
		return mailInternal(c, "failed to load the filter script", err)
	}
	if req.Body.Name != nil {
		name := strings.TrimSpace(*req.Body.Name)
		if name == "" || len(name) > 255 {
			return c.AbortBadRequest("name must be 1..255 characters")
		}
		script.Name = name
	}
	if req.Body.Priority != nil {
		script.Priority = *req.Body.Priority
	}
	if req.Body.Enabled != nil {
		script.Enabled = *req.Body.Enabled
	}
	if req.Body.Conditions != nil {
		if err := validateFilterScript(*req.Body.Conditions, []filterengine.ScriptAction{{Type: filterengine.ActionKeep}}); err != nil {
			return c.AbortBadRequest(err.Error())
		}
		script.Conditions = models.FilterRules(*req.Body.Conditions)
	}
	if req.Body.Actions != nil {
		if err := validateFilterScript(script.Conditions, *req.Body.Actions); err != nil {
			return c.AbortBadRequest(err.Error())
		}
		acts, _ := json.Marshal(*req.Body.Actions)
		script.ActionsJSON = string(acts)
	}
	if err := h.scripts.Update(script); err != nil {
		return mailInternal(c, "failed to update the filter script", err)
	}
	return ok(c, script)
}

// DeleteFilterScript removes one of the session user's scripts.
func (h *FilterScriptHandler) DeleteFilterScript(c *okapi.Context, req *FilterScriptIDRequest) error {
	if h.scripts == nil {
		return mailUnavailable(c)
	}
	userID, _ := mailScope(c)
	n, err := h.scripts.DeleteForUser(uint(req.ID), userID)
	if err != nil {
		return mailInternal(c, "failed to delete the filter script", err)
	}
	if n == 0 {
		return c.AbortNotFound("filter script not found")
	}
	return ok(c, okapi.M{"message": "filter script deleted"})
}
