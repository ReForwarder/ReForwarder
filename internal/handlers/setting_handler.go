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
	"strings"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

func isReservedSettingKey(key string) bool {
	return strings.HasPrefix(key, "app.")
}

// visibleSettings drops reserved internal rows from a settings list.
func visibleSettings(settings []models.Setting) []models.Setting {
	out := make([]models.Setting, 0, len(settings))
	for _, s := range settings {
		if isReservedSettingKey(s.Key) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// SettingHandler handles admin management of platform settings.
type SettingHandler struct {
	repo  *repositories.SettingRepository
	audit *audit.Logger
	// history records the per-key change log (SF-100). Optional until wired:
	// set via SetHistoryRepo; when nil, writes succeed but leave no history.
	history *repositories.SettingHistoryRepository
	// provider is the shared TTL-cached settings provider. Optional until
	// wired: set via SetSettingsProvider; when nil, writes cannot burst the
	// cache and consumers keep their value up to the cache TTL.
	provider *settings.Provider
}

func NewSettingHandler(repo *repositories.SettingRepository, audit *audit.Logger) *SettingHandler {
	return &SettingHandler{repo: repo, audit: audit}
}

// SetHistoryRepo attaches the settings change history repository (SF-100).
// Route wiring calls this with the history repository built on the same DB.
func (h *SettingHandler) SetHistoryRepo(r *repositories.SettingHistoryRepository) {
	h.history = r
}

// SetSettingsProvider attaches the shared settings provider so a write can
// invalidate its 30-second TTL cache immediately instead of letting the
// change lag behind for one TTL cycle.
func (h *SettingHandler) SetSettingsProvider(p *settings.Provider) {
	h.provider = p
}

// settingWrite is one resolved key write: what the caller asked for plus the
// type hint to persist.

type UpdateSettingsRequest struct {
	Body struct {
		Settings []SettingInput `json:"settings" required:"true"`
	} `json:"body"`
}

type SettingInput struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

func (h *SettingHandler) GetSettings(c *okapi.Context) error {
	settings, err := h.repo.FindAll()
	if err != nil {
		return c.AbortInternalServerError("failed to load settings", err)
	}
	return ok(c, visibleSettings(settings))
}

func (h *SettingHandler) UpdateSettings(c *okapi.Context, req *UpdateSettingsRequest) error {
	writes := make([]models.Setting, 0, len(req.Body.Settings))
	for _, s := range req.Body.Settings {

		if isReservedSettingKey(s.Key) {
			continue
		}
		typ := s.Type
		if typ == "" {
			typ = "string"
		}
		writes = append(writes, models.Setting{
			Key:   s.Key,
			Value: s.Value,
			Type:  typ,
		})
	}

	changed, err := h.applySettingWrites(c, writes)
	if err != nil {
		return err
	}

	h.audit.Log(uint(c.GetInt("user_id")), c.GetString("email"), c.RealIP(),
		"settings.updated", "Platform settings updated", map[string]any{"changed_keys": changed})

	updated, err := h.repo.FindAll()
	if err != nil {
		return c.AbortInternalServerError("failed to load settings", err)
	}
	return ok(c, visibleSettings(updated))
}

// applySettingWrites is the single write path for platform settings (SF-100):
// update, and rollback all funnel through it so diff capture, cache
// invalidation and persistence can never drift apart.
//
// It records one setting_history row per key whose value actually changed —
// old value → new value, with the acting user — invalidates the provider's
// TTL cache, and returns the keys that changed (in request order) for the
// audit metadata. A key's first write records a nil old value; such an entry
// cannot be rolled back to (there is no prior value) and the rollback
// endpoint refuses it.
//
// Platform (non-workspace) keys only: callers are the admin-gated settings
// routes; every row is written with a nil workspace id.
func (h *SettingHandler) applySettingWrites(c *okapi.Context, writes []models.Setting) ([]string, error) {
	if len(writes) == 0 {
		return []string{}, nil
	}

	// Snapshot current values so the diff can be captured after the upsert.
	current := make(map[string]models.Setting)
	if existing, err := h.repo.FindAll(); err == nil {
		for _, s := range existing {
			current[s.Key] = s
		}
	}

	if err := h.repo.BulkUpsert(writes); err != nil {
		return nil, c.AbortInternalServerError("failed to update settings", err)
	}

	changed := make([]string, 0, len(writes))
	for _, w := range writes {
		old, existed := current[w.Key]
		if !existed || old.Value != w.Value {
			changed = append(changed, w.Key)
		}
	}

	if h.history != nil {
		userID := uint(c.GetInt("user_id"))
		entries := make([]models.SettingHistory, 0, len(writes))
		for _, w := range writes {
			old, existed := current[w.Key]
			if existed && old.Value == w.Value {
				continue // unchanged key: no diff to record
			}
			entry := models.SettingHistory{
				Key:            w.Key,
				NewValue:       w.Value,
				ChangedBy:      userID,
				ChangedByEmail: c.GetString("email"),
			}
			if existed {
				// The key had a prior value; a first write leaves OldValue nil.
				v := old.Value
				entry.OldValue = &v
			}
			entries = append(entries, entry)
		}
		if len(entries) > 0 {
			if err := h.history.CreateBatch(entries); err != nil {
				return nil, c.AbortInternalServerError("failed to record settings history", err)
			}
		}
	}

	if h.provider != nil {
		h.provider.Invalidate()
	}

	return changed, nil
}
