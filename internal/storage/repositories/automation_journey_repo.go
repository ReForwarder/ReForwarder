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

type AutomationJourneyRepository struct {
	db *gorm.DB
}

func NewAutomationJourneyRepository(db *gorm.DB) *AutomationJourneyRepository {
	return &AutomationJourneyRepository{db: db}
}

// Create inserts a journey, filling the structural jsonb default so a
// zero-value Create stays valid (jsonb rejects the empty string).
func (r *AutomationJourneyRepository) Create(journey *models.AutomationJourney) error {
	if journey.ContextJSON == "" {
		journey.ContextJSON = "{}"
	}
	return r.db.Create(journey).Error
}

// GetByID returns one journey by primary key.
func (r *AutomationJourneyRepository) GetByID(id uint) (*models.AutomationJourney, error) {
	var journey models.AutomationJourney
	if err := r.db.First(&journey, id).Error; err != nil {
		return nil, err
	}
	return &journey, nil
}

// GetByAutomationAndEmail returns the contact's most recent journey in an
// automation — the once-frequency dedup fallback and the execution lookup.
func (r *AutomationJourneyRepository) GetByAutomationAndEmail(automationID uint, email string) (*models.AutomationJourney, error) {
	var journey models.AutomationJourney
	if err := r.db.Where("automation_id = ? AND email = ?", automationID, email).
		Order("entered_at DESC").
		First(&journey).Error; err != nil {
		return nil, err
	}
	return &journey, nil
}

// ClaimDueBatch claims the next batch of runnable journeys: active journeys
// parked on a due scheduled_at whose automation is still live. The SELECT runs
// in a short transaction with FOR UPDATE OF aj SKIP LOCKED, so concurrent
// workers take disjoint batches and a claimed journey is never handed to two
// workers at the same instant; cursor-advance compare-and-swaps cover the rest.
// now is passed in (usually the engine clock) so tests can drive the schedule.
func (r *AutomationJourneyRepository) ClaimDueBatchAt(limit int, now time.Time) ([]models.AutomationJourney, error) {
	if limit <= 0 {
		limit = 50
	}
	var journeys []models.AutomationJourney
	err := r.db.Transaction(func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT aj.* FROM automation_journeys aj
			JOIN automations a ON a.id = aj.automation_id
			WHERE aj.status = ?
			  AND aj.scheduled_at IS NOT NULL AND aj.scheduled_at <= ?
			  AND a.status = ? AND a.deleted_at IS NULL
			ORDER BY aj.scheduled_at
			LIMIT ?
			FOR UPDATE OF aj SKIP LOCKED`,
			models.AutomationJourneyStatusActive, now, models.AutomationStatusLive, limit,
		).Scan(&journeys).Error
	})
	if err != nil {
		return nil, err
	}
	return journeys, nil
}

// ClaimDueBatch claims the due batch as of the current wall clock.
func (r *AutomationJourneyRepository) ClaimDueBatch(limit int) ([]models.AutomationJourney, error) {
	return r.ClaimDueBatchAt(limit, time.Now())
}

// UpdateCursorIfActive advances a journey's node cursor under compare-and-swap:
// the update applies only while the journey is still active, so a concurrent
// (or delayed re-run) executor cannot move a journey that already finished. A
// nil scheduledAt clears the park time — the journey is runnable now. Returns
// false when the CAS lost (0 rows affected).
func (r *AutomationJourneyRepository) UpdateCursorIfActive(id uint, currentNodeID string, scheduledAt *time.Time) (bool, error) {
	result := r.db.Model(&models.AutomationJourney{}).
		Where("id = ? AND status = ?", id, models.AutomationJourneyStatusActive).
		Updates(map[string]any{
			"current_node_id":  currentNodeID,
			"scheduled_at":     scheduledAt,
			"last_advanced_at": time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// MarkCompletedIfActive finishes a journey under compare-and-swap. Returns
// false when the journey already left the active state.
func (r *AutomationJourneyRepository) MarkCompletedIfActive(id uint, completedAt time.Time) (bool, error) {
	result := r.db.Model(&models.AutomationJourney{}).
		Where("id = ? AND status = ?", id, models.AutomationJourneyStatusActive).
		Updates(map[string]any{
			"status":           models.AutomationJourneyStatusCompleted,
			"completed_at":     completedAt,
			"scheduled_at":     nil,
			"last_advanced_at": completedAt,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// MarkExitedIfActive exits a journey with a reason (contact missing, node
// deleted, ...) under compare-and-swap. Returns false when the CAS lost.
func (r *AutomationJourneyRepository) MarkExitedIfActive(id uint, reason string) (bool, error) {
	now := time.Now()
	result := r.db.Model(&models.AutomationJourney{}).
		Where("id = ? AND status = ?", id, models.AutomationJourneyStatusActive).
		Updates(map[string]any{
			"status":           models.AutomationJourneyStatusExited,
			"exit_reason":      reason,
			"completed_at":     now,
			"scheduled_at":     nil,
			"last_advanced_at": now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// MarkFailedIfActive fails a journey permanently (retry budget exhausted) under
// compare-and-swap. Returns false when the CAS lost.
func (r *AutomationJourneyRepository) MarkFailedIfActive(id uint, lastErr string) (bool, error) {
	now := time.Now()
	result := r.db.Model(&models.AutomationJourney{}).
		Where("id = ? AND status = ?", id, models.AutomationJourneyStatusActive).
		Updates(map[string]any{
			"status":           models.AutomationJourneyStatusFailed,
			"last_error":       lastErr,
			"completed_at":     now,
			"scheduled_at":     nil,
			"last_advanced_at": now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// BumpRetry records one failed node attempt and re-parks the journey for the
// backoff retry: retry_count grows by one, last_error is stored and
// scheduled_at moves to nextRetryAt, under compare-and-swap so a concurrently
// completed journey is never re-parked.
func (r *AutomationJourneyRepository) BumpRetry(id uint, nextRetryAt time.Time, lastErr string) (bool, error) {
	result := r.db.Model(&models.AutomationJourney{}).
		Where("id = ? AND status = ?", id, models.AutomationJourneyStatusActive).
		Updates(map[string]any{
			"retry_count":      gorm.Expr("retry_count + 1"),
			"last_error":       lastErr,
			"scheduled_at":     nextRetryAt,
			"last_advanced_at": time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// ListByAutomation returns a page of an automation's journeys, newest first,
// with a total count. status filters active|completed|exited|failed when
// non-empty; contactSubstring narrows by contact email substring.
func (r *AutomationJourneyRepository) ListByAutomation(automationID uint, page, size int, status, contactSubstring string) ([]models.AutomationJourney, int64, error) {
	limit, offset := normalizeSecurityPage(page, size)

	where := func(q *gorm.DB) *gorm.DB {
		q = q.Where("automation_id = ?", automationID)
		if status != "" {
			q = q.Where("status = ?", status)
		}
		if contactSubstring != "" {
			q = q.Where("LOWER(email) LIKE ?", likePattern(contactSubstring))
		}
		return q
	}

	var total int64
	if err := where(r.db.Model(&models.AutomationJourney{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var journeys []models.AutomationJourney
	if err := where(r.db.Model(&models.AutomationJourney{})).
		Order("entered_at DESC").
		Limit(limit).Offset(offset).
		Find(&journeys).Error; err != nil {
		return nil, 0, err
	}
	return journeys, total, nil
}

// CountByStatus counts an automation's journeys grouped by status. Missing
// statuses are simply absent from the map.
func (r *AutomationJourneyRepository) CountByStatus(automationID uint) (map[string]int64, error) {
	var rows []struct {
		Status string
		Count  int64
	}
	if err := r.db.Model(&models.AutomationJourney{}).
		Select("status, COUNT(*) AS count").
		Where("automation_id = ?", automationID).
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

// ListOverdue returns active, due journeys the claimer missed (a lost enqueue
// or a worker crash between claim and execution), oldest schedule first, for
// the periodic sweep to re-enqueue. The automation join keeps paused and
// deleted automations from being re-enqueued forever. Journeys whose
// scheduled_at is NULL are excluded: they are the ones a live executor already
// holds or just advanced.
func (r *AutomationJourneyRepository) ListOverdue(cutoff time.Time, limit int) ([]models.AutomationJourney, error) {
	if limit <= 0 {
		limit = 100
	}
	var journeys []models.AutomationJourney
	err := r.db.Raw(`
		SELECT aj.* FROM automation_journeys aj
		JOIN automations a ON a.id = aj.automation_id
		WHERE aj.status = ?
		  AND aj.scheduled_at IS NOT NULL AND aj.scheduled_at <= ?
		  AND a.status = ? AND a.deleted_at IS NULL
		ORDER BY aj.scheduled_at
		LIMIT ?`,
		models.AutomationJourneyStatusActive, cutoff, models.AutomationStatusLive, limit,
	).Scan(&journeys).Error
	if err != nil {
		return nil, err
	}
	return journeys, nil
}
