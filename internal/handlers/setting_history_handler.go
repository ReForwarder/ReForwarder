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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"

	"github.com/goposta/posta/internal/models"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// SettingHistoryRequest queries the change history of one platform setting
// (SF-100). The key is a query parameter so the endpoint needs no path
// parameter; page/size follow the shared paging normalization.
type SettingHistoryRequest struct {
	Page int `query:"page" default:"0"`
	Size int `query:"size" default:"20"`
}

// RollbackSettingRequest restores a platform setting to a recorded prior
// value (SF-100). HistoryID selects a specific entry; when omitted the most
// recent entry for the key is used.
type RollbackSettingRequest struct {
	Body struct {
		Key       string `json:"key" required:"true"`
		HistoryID uint   `json:"history_id"`
	} `json:"body"`
}

// historyScope is the workspace filter for platform keys: the platform
// settings setter only writes non-workspace keys, so history lookups target
// the NULL-workspace partition of the table.
func historyScope() *uint { return nil }

// GetSettingHistory returns a page of change-history entries for one key,
// newest first. Admin-gated via the route group (platform keys are
// admin-only); workspace-scoped keys would additionally require edit rights,
// but no workspace-scoped key/value settings exist yet — the table carries a
// nullable workspace id for them.
func (h *SettingHandler) GetSettingHistory(c *okapi.Context, req *SettingHistoryRequest) error {
	if h.history == nil {
		return c.AbortInternalServerError("settings history is not wired", errors.New("nil history repository"))
	}
	key := c.Query("key")
	if key == "" {
		return c.AbortBadRequest("key query parameter is required")
	}
	if isReservedSettingKey(key) {
		return c.AbortBadRequest("reserved internal setting keys have no history")
	}

	page, size, offset := normalizePageParams(req.Page, req.Size)
	entries, total, err := h.history.ListByKey(key, historyScope(), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to load settings history", err)
	}
	if entries == nil {
		entries = []models.SettingHistory{}
	}
	return paginated(c, entries, total, page, size)
}

// RollbackSetting restores one platform setting to a recorded prior value by
// writing it back through the normal setter (applySettingWrites), so the
// rollback itself appends a new history entry and invalidates the provider's
// TTL cache exactly like any other settings write. Admin-gated via the route
// group.
func (h *SettingHandler) RollbackSetting(c *okapi.Context, req *RollbackSettingRequest) error {
	if h.history == nil {
		return c.AbortInternalServerError("settings history is not wired", errors.New("nil history repository"))
	}
	key := req.Body.Key
	if key == "" {
		return c.AbortBadRequest("key is required")
	}
	if isReservedSettingKey(key) {
		return c.AbortBadRequest("reserved internal setting keys cannot be rolled back")
	}

	var entry *models.SettingHistory
	if req.Body.HistoryID > 0 {
		var err error
		entry, err = h.history.FindByID(req.Body.HistoryID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return c.AbortNotFound("history entry not found")
			}
			return c.AbortInternalServerError("failed to load history entry", err)
		}
		if entry.Key != key {
			return c.AbortBadRequest("history entry belongs to a different key")
		}
	} else {
		var err error
		entry, err = h.history.LatestByKey(key, historyScope())
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return c.AbortNotFound("no recorded history for key " + key)
			}
			return c.AbortInternalServerError("failed to load settings history", err)
		}
	}

	// A nil old value marks the key's creation: there is no prior value to
	// restore, so a rollback to this entry would be a delete, which the
	// normal setter cannot express. Refuse instead of guessing.
	if entry.OldValue == nil {
		return c.AbortBadRequest("cannot roll back: the selected entry is the key's creation (no prior value recorded)")
	}

	// Preserve the stored type hint of the current row (the rolled-back value
	// predates it only in content, not in shape).
	typ := models.SettingTypeString
	if current, err := h.repo.FindByKey(key); err == nil && current.Type != "" {
		typ = current.Type
	}

	restored := []models.Setting{{
		Key:   key,
		Value: *entry.OldValue,
		Type:  typ,
	}}

	if _, err := h.applySettingWrites(c, restored); err != nil {
		return err
	}

	h.audit.Log(uint(c.GetInt("user_id")), c.GetString("email"), c.RealIP(),
		"settings.rolled_back", "Platform setting rolled back", map[string]any{
			"key":        key,
			"restored":   *entry.OldValue,
			"history_id": entry.ID,
		})

	updated, err := h.repo.FindAll()
	if err != nil {
		return c.AbortInternalServerError("failed to load settings", err)
	}
	return ok(c, visibleSettings(updated))
}
