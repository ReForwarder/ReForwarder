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

package repositories

import (
	"fmt"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

type AutomationRepository struct {
	db *gorm.DB
}

func NewAutomationRepository(db *gorm.DB) *AutomationRepository {
	return &AutomationRepository{db: db}
}

// Create inserts an automation, filling the structural jsonb defaults so a
// zero-value Create stays valid (jsonb rejects the empty string).
func (r *AutomationRepository) Create(automation *models.Automation) error {
	if automation.TriggerConfig == "" {
		automation.TriggerConfig = "{}"
	}
	if automation.Nodes == "" {
		automation.Nodes = "[]"
	}
	if automation.Stats == "" {
		automation.Stats = models.DefaultWorkflowStatsJSON
	}
	return r.db.Create(automation).Error
}

// GetByID returns one automation by primary key; soft-deleted rows are excluded.
func (r *AutomationRepository) GetByID(id uint) (*models.Automation, error) {
	var automation models.Automation
	if err := r.db.First(&automation, id).Error; err != nil {
		return nil, err
	}
	return &automation, nil
}

// GetByUUID returns one automation by its public UUID.
func (r *AutomationRepository) GetByUUID(uuid string) (*models.Automation, error) {
	var automation models.Automation
	if err := r.db.Where("uuid = ?", uuid).First(&automation).Error; err != nil {
		return nil, err
	}
	return &automation, nil
}

// applyAutomationScope narrows a query to the caller's scope: workspace rows
// when a workspace is given, otherwise the user's personal rows (created
// outside any workspace) — the nil-workspace personal scope of
// EmailRepository.FindByUserID.
func applyAutomationScope(q *gorm.DB, userID uint, workspaceID *uint) *gorm.DB {
	if workspaceID != nil {
		return q.Where("workspace_id = ?", *workspaceID)
	}
	return q.Where("user_id = ? AND workspace_id IS NULL", userID)
}

// ListByScope returns a page of automations for the scope, newest first, with a
// total count. status filters draft|live|paused when non-empty.
func (r *AutomationRepository) ListByScope(page, size int, userID uint, workspaceID *uint, status string) ([]models.Automation, int64, error) {
	limit, offset := normalizeSecurityPage(page, size)

	where := func(q *gorm.DB) *gorm.DB {
		q = applyAutomationScope(q, userID, workspaceID)
		if status != "" {
			q = q.Where("status = ?", status)
		}
		return q
	}

	var total int64
	if err := where(r.db.Model(&models.Automation{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var automations []models.Automation
	if err := where(r.db.Model(&models.Automation{})).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&automations).Error; err != nil {
		return nil, 0, err
	}
	return automations, total, nil
}

// GetForScopeStats counts the scope's automations grouped by status. The
// dashboard totals; unknown statuses simply appear as their own key.
func (r *AutomationRepository) GetForScopeStats(userID uint, workspaceID *uint) (map[string]int64, error) {
	var rows []struct {
		Status string
		Count  int64
	}
	if err := applyAutomationScope(r.db.Model(&models.Automation{}), userID, workspaceID).
		Select("status, COUNT(*) AS count").
		Group("status").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.Status] = row.Count
	}
	return out, nil
}

// Update saves every field of the given automation (builder document edits).
func (r *AutomationRepository) Update(automation *models.Automation) error {
	return r.db.Save(automation).Error
}

// AutomationUpdate carries the optional field changes applied by
// UpdateIfStatus alongside the status transition. Nil fields are left alone.
type AutomationUpdate struct {
	Name    *string
	Version *int
}

// UpdateIfStatus performs a targeted compare-and-swap: the fields are applied
// only when the row is still in fromStatus, and updated_at is refreshed. Pass
// fromStatus "" to match any current status. Returns false when the row was
// not in fromStatus (or does not exist) — the caller turns that into a 409.
func (r *AutomationRepository) UpdateIfStatus(id uint, fromStatus, toStatus string, u AutomationUpdate) (bool, error) {
	updates := map[string]any{
		"status":     toStatus,
		"updated_at": time.Now(),
	}
	if u.Name != nil {
		updates["name"] = *u.Name
	}
	if u.Version != nil {
		updates["version"] = *u.Version
	}

	q := r.db.Model(&models.Automation{}).Where("id = ?", id)
	if fromStatus != "" {
		q = q.Where("status = ?", fromStatus)
	}
	result := q.Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// SoftDelete soft-deletes the automation and exits its active journeys in the
// same transaction, so paused or parked contacts never linger as zombies that
// the due-batch claim and the overdue sweep would keep revisiting.
func (r *AutomationRepository) SoftDelete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&models.Automation{}, id).Error; err != nil {
			return err
		}
		now := time.Now()
		return tx.Model(&models.AutomationJourney{}).
			Where("automation_id = ? AND status = ?", id, models.AutomationJourneyStatusActive).
			Updates(map[string]any{
				"status":           models.AutomationJourneyStatusExited,
				"exit_reason":      models.AutomationExitReasonAutomationDeleted,
				"completed_at":     now,
				"scheduled_at":     nil,
				"last_advanced_at": now,
			}).Error
	})
}

// automationStatColumns whitelists the countable stats keys — UPDATE statements
// are built only from these constants, user input never reaches the column
// position.
var automationStatColumns = map[string]bool{
	"enrolled":  true,
	"completed": true,
	"exited":    true,
	"failed":    true,
}

// IncrementStat bumps one counter of Automation.Stats atomically: a single
// UPDATE with jsonb arithmetic, so concurrent enrollments and completions
// cannot lose increments the way a Go-side read-modify-write would. stat must
// be one of enrolled|completed|exited|failed.
func (r *AutomationRepository) IncrementStat(id uint, stat string) error {
	if !automationStatColumns[stat] {
		return fmt.Errorf("automation stat %q is not countable", stat)
	}
	return r.db.Exec(
		fmt.Sprintf(
			`UPDATE automations SET stats = jsonb_set(COALESCE(stats, '{}'::jsonb), '{%s}', to_jsonb(COALESCE((stats->>'%s')::int, 0) + 1), true) WHERE id = ?`,
			stat, stat,
		),
		id,
	).Error
}
