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
	"gorm.io/gorm/clause"
)

// InboundForwardTaskRepository is the durable forwarding queue (OB-2, ADR-002
// decision 2). The routing engine Enqueues rows after durable ingest; the
// forwarding executor (wave 2) claims due rows and re-sends the stored
// original through the outbound pipeline.
type InboundForwardTaskRepository struct {
	db *gorm.DB
}

func NewInboundForwardTaskRepository(db *gorm.DB) *InboundForwardTaskRepository {
	return &InboundForwardTaskRepository{db: db}
}

// Enqueue persists a forwarding task row. The unique inbound_email_id index
// plus ON CONFLICT DO NOTHING makes re-ingest (or a racing ingest) idempotent:
// one task per inbound message, first enqueue wins.
func (r *InboundForwardTaskRepository) Enqueue(task *models.InboundForwardTask) error {
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(task).Error
}

// ListDue returns pending tasks whose NextAttemptAt has arrived, oldest first,
// for the executor to claim.
func (r *InboundForwardTaskRepository) ListDue(now time.Time, limit int) ([]models.InboundForwardTask, error) {
	var items []models.InboundForwardTask
	if err := r.db.
		Where("status = ? AND next_attempt_at <= ?", models.InboundForwardTaskPending, now.UTC()).
		Order("next_attempt_at ASC, id ASC").
		Limit(limit).
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// Update saves task state (status transitions, attempts, last error).
func (r *InboundForwardTaskRepository) Update(task *models.InboundForwardTask) error {
	return r.db.Save(task).Error
}

// FindByInboundEmailID returns the task enqueued for an inbound message, or
// gorm.ErrRecordNotFound.
func (r *InboundForwardTaskRepository) FindByInboundEmailID(inboundEmailID uint) (*models.InboundForwardTask, error) {
	var task models.InboundForwardTask
	if err := r.db.Where("inbound_email_id = ?", inboundEmailID).First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// FindByID returns one task by primary key, or gorm.ErrRecordNotFound.
func (r *InboundForwardTaskRepository) FindByID(id uint) (*models.InboundForwardTask, error) {
	var task models.InboundForwardTask
	if err := r.db.First(&task, id).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// Claim atomically claims a pending task for execution (R-26 CAS, same shape
// as EmailRepository.ClaimForRetry): pending→running plus the attempt
// increment happen in one UPDATE, so two executor sweeps (or two processes)
// can never both claim the same row and double-forward. Returns false when
// the row was not pending — another claimant won.
func (r *InboundForwardTaskRepository) Claim(id uint) (bool, error) {
	res := r.db.Model(&models.InboundForwardTask{}).
		Where("id = ? AND status = ?", models.InboundForwardTaskPending, models.InboundForwardTaskPending).
		Updates(map[string]interface{}{
			"status":   models.InboundForwardTaskRunning,
			"attempts": gorm.Expr("attempts + 1"),
		})
	return res.RowsAffected > 0, res.Error
}

// MarkDone finalizes a claimed task as delivered to the outbound pipeline
// (running→done), persisting the derived hop count and clearing the error.
func (r *InboundForwardTaskRepository) MarkDone(task *models.InboundForwardTask) error {
	return r.db.Model(&models.InboundForwardTask{}).
		Where("id = ? AND status = ?", task.ID, models.InboundForwardTaskRunning).
		Updates(map[string]interface{}{
			"status":     models.InboundForwardTaskDone,
			"hops":       task.Hops,
			"last_error": "",
		}).Error
}

// MarkRetry returns a claimed task to the queue with the standard backoff:
// running→pending, next_attempt_at set to the caller-computed slot, the
// derived hop count and the failure recorded.
func (r *InboundForwardTaskRepository) MarkRetry(task *models.InboundForwardTask, next time.Time, lastErr string) error {
	return r.db.Model(&models.InboundForwardTask{}).
		Where("id = ? AND status = ?", task.ID, models.InboundForwardTaskRunning).
		Updates(map[string]interface{}{
			"status":          models.InboundForwardTaskPending,
			"hops":            task.Hops,
			"next_attempt_at": next.UTC(),
			"last_error":      lastErr,
		}).Error
}

// MarkFailed finalizes a claimed task after a permanent failure or an
// exhausted retry budget (running→failed), persisting the derived hop count
// and the terminal error.
func (r *InboundForwardTaskRepository) MarkFailed(task *models.InboundForwardTask, lastErr string) error {
	return r.db.Model(&models.InboundForwardTask{}).
		Where("id = ? AND status = ?", task.ID, models.InboundForwardTaskRunning).
		Updates(map[string]interface{}{
			"status":     models.InboundForwardTaskFailed,
			"hops":       task.Hops,
			"last_error": lastErr,
		}).Error
}

// RequeueStaleRunning re-arms tasks an executor died holding (R-03 lost-task
// reconcile): running rows untouched for staleFor go back to pending in one
// CAS UPDATE, so a crashed process cannot strand a forward forever. The row's
// updated_at guard makes concurrent re-arms idempotent. Returns the count.
func (r *InboundForwardTaskRepository) RequeueStaleRunning(staleFor time.Duration, now time.Time) (int64, error) {
	res := r.db.Model(&models.InboundForwardTask{}).
		Where("status = ? AND updated_at < ?", models.InboundForwardTaskRunning, now.UTC().Add(-staleFor)).
		Updates(map[string]interface{}{
			"status":          models.InboundForwardTaskPending,
			"next_attempt_at": now.UTC(),
		})
	return res.RowsAffected, res.Error
}
