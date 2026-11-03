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
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

type AutomationRunRepository struct {
	db *gorm.DB
}

func NewAutomationRunRepository(db *gorm.DB) *AutomationRunRepository {
	return &AutomationRunRepository{db: db}
}

// Create inserts a node run. Rows are created in the entered state and flipped
// to completed or failed afterwards, so each node execution is one row.
func (r *AutomationRunRepository) Create(run *models.AutomationNodeRun) error {
	if run.Action == "" {
		run.Action = models.AutomationRunActionEntered
	}
	if run.Output == "" {
		// jsonb rejects '' — persist the structural default instead.
		run.Output = "{}"
	}
	return r.db.Create(run).Error
}

// Complete flips a run to completed with its output document and duration.
func (r *AutomationRunRepository) Complete(id uint, output string, durationMs int, completedAt time.Time) error {
	return r.db.Model(&models.AutomationNodeRun{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"action":       models.AutomationRunActionCompleted,
			"output":       output,
			"duration_ms":  durationMs,
			"completed_at": completedAt,
		}).Error
}

// Fail flips a run to failed with the node error; the completion time closes
// the run's timeline entry.
func (r *AutomationRunRepository) Fail(id uint, errMsg string) error {
	return r.db.Model(&models.AutomationNodeRun{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"action":       models.AutomationRunActionFailed,
			"error":        errMsg,
			"completed_at": time.Now(),
		}).Error
}

// ListByJourney returns a journey's node runs, oldest first — the execution
// drawer timeline.
func (r *AutomationRunRepository) ListByJourney(journeyID uint) ([]models.AutomationNodeRun, error) {
	var runs []models.AutomationNodeRun
	if err := r.db.Where("journey_id = ?", journeyID).
		Order("entered_at ASC, id ASC").
		Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}

// NodeStatRow is one node's aggregate over all journeys of an automation.
type NodeStatRow struct {
	NodeID    string `json:"node_id"`
	NodeType  string `json:"node_type"`
	Entered   int64  `json:"entered"`
	Completed int64  `json:"completed"`
	Failed    int64  `json:"failed"`
}

// NodeStats aggregates node runs per node with conditional counts in a single
// grouped query. Every run row is created with action='entered' and flipped in
// place, so Entered is the row count per node (executions started), while
// Completed and Failed count the terminal outcomes via COUNT(*) FILTER — rows
// still mid-execution count as entered only.
func (r *AutomationRunRepository) NodeStats(automationID uint) ([]NodeStatRow, error) {
	var rows []NodeStatRow
	err := r.db.Raw(`
		SELECT node_id, node_type,
			COUNT(*) AS entered,
			COUNT(*) FILTER (WHERE action = ?) AS completed,
			COUNT(*) FILTER (WHERE action = ?) AS failed
		FROM automation_node_runs
		WHERE automation_id = ?
		GROUP BY node_id, node_type
		ORDER BY node_id`,
		models.AutomationRunActionCompleted, models.AutomationRunActionFailed, automationID,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
