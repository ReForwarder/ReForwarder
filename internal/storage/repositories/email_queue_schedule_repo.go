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

// EmailQueueScheduleRepository persists the SF-08 per-recipient queue
// schedule rows. Every write that moves a row between lifecycle states is a
// CAS on the current status so concurrent claimants (asynq redelivery, the
// retry sweep, the expiry sweep) cannot double-fire a transition.
type EmailQueueScheduleRepository struct {
	db *gorm.DB
}

func NewEmailQueueScheduleRepository(db *gorm.DB) *EmailQueueScheduleRepository {
	return &EmailQueueScheduleRepository{db: db}
}

// Create inserts rows in one transaction (the per-recipient fan-out of one
// message). The unique (email_id, recipient) index makes a re-run of the same
// fan-out a no-op for already-present rows — GORM's ON CONFLICT DO NOTHING.
func (r *EmailQueueScheduleRepository) Create(rows []models.EmailQueueSchedule) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// FindByEmailID returns the schedule rows of one message.
func (r *EmailQueueScheduleRepository) FindByEmailID(emailID uint) ([]models.EmailQueueSchedule, error) {
	var rows []models.EmailQueueSchedule
	err := r.db.Where("email_id = ?", emailID).Order("id ASC").Find(&rows).Error
	return rows, err
}

// FindByIDs returns the schedule rows of several messages (expiry sweep).
func (r *EmailQueueScheduleRepository) FindByEmailIDs(emailIDs []uint) ([]models.EmailQueueSchedule, error) {
	var rows []models.EmailQueueSchedule
	if len(emailIDs) == 0 {
		return rows, nil
	}
	err := r.db.Where("email_id IN ?", emailIDs).Order("id ASC").Find(&rows).Error
	return rows, err
}

// AdvanceResult reports the outcome of a RecordFailure-style CAS advance.
type AdvanceResult struct {
	// Matched is true when the row was still active and advanced.
	Matched bool
	// Exhausted is true when the advance consumed the last interval entry.
	Exhausted bool
}

// AdvanceCAS moves one active row to its next attempt state in a single
// UPDATE: attempt_index += 1, next_retry_at = next, last_attempt_at/last_error
// stamped — or, when the interval list ran out, status flips to exhausted and
// next_retry_at clears. Only a still-active row matches; a row already terminal
// (or advanced by a racing claimant) reports Matched=false.
func (r *EmailQueueScheduleRepository) AdvanceCAS(id uint, next *time.Time, exhausted bool, errMsg string, now time.Time) (AdvanceResult, error) {
	updates := map[string]interface{}{
		"attempt_index":   gorm.Expr("attempt_index + 1"),
		"last_attempt_at": now,
		"last_error":      errMsg,
		"updated_at":      now,
	}
	status := models.ScheduleStatusActive
	if exhausted {
		status = models.ScheduleStatusExhausted
		updates["next_retry_at"] = nil
	} else {
		updates["next_retry_at"] = next
	}
	updates["status"] = status

	res := r.db.Model(&models.EmailQueueSchedule{}).
		Where("id = ? AND status = ?", id, models.ScheduleStatusActive).
		Updates(updates)
	if res.Error != nil {
		return AdvanceResult{}, res.Error
	}
	return AdvanceResult{Matched: res.RowsAffected > 0, Exhausted: exhausted}, nil
}

// TerminateCAS flips one active row to a terminal state in a single UPDATE.
// Returns true when this caller won the transition (the row was still active).
func (r *EmailQueueScheduleRepository) TerminateCAS(id uint, status models.EmailQueueScheduleStatus, errMsg string, now time.Time) (bool, error) {
	res := r.db.Model(&models.EmailQueueSchedule{}).
		Where("id = ? AND status = ?", id, models.ScheduleStatusActive).
		Updates(map[string]interface{}{
			"status":     status,
			"last_error": errMsg,
			"updated_at": now,
		})
	return res.RowsAffected > 0, res.Error
}

// TerminateAllForEmail flips every active row of a message to the given
// terminal state (message-level finalization). Returns the ids of the rows
// this caller transitioned — the caller emits one notify event per winner id,
// so a concurrent finalizer never double-fires.
func (r *EmailQueueScheduleRepository) TerminateAllForEmail(emailID uint, status models.EmailQueueScheduleStatus, errMsg string, now time.Time) ([]uint, error) {
	var winners []uint
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var active []models.EmailQueueSchedule
		if err := tx.Where("email_id = ? AND status = ?", emailID, models.ScheduleStatusActive).
			Find(&active).Error; err != nil {
			return err
		}
		if len(active) == 0 {
			return nil
		}
		res := tx.Model(&models.EmailQueueSchedule{}).
			Where("email_id = ? AND status = ?", emailID, models.ScheduleStatusActive).
			Updates(map[string]interface{}{
				"status":     status,
				"last_error": errMsg,
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			// The CAS won for the whole set (a single UPDATE either matches
			// all listed rows or none of them — rows can only have left the
			// active state between the read and the write for rows a racing
			// finalizer already terminated, which the UPDATE skips).
			for _, row := range active {
				winners = append(winners, row.ID)
			}
		}
		return nil
	})
	return winners, err
}

// MarkDeliveredForEmail closes every active row of a message as delivered.
func (r *EmailQueueScheduleRepository) MarkDeliveredForEmail(emailID uint, now time.Time) error {
	return r.db.Model(&models.EmailQueueSchedule{}).
		Where("email_id = ? AND status = ?", emailID, models.ScheduleStatusActive).
		Updates(map[string]interface{}{
			"status":     models.ScheduleStatusDelivered,
			"updated_at": now,
		}).Error
}

// ExpiredEmailIDs lists still-undelivered messages whose TTL deadline passed
// (SF-08 expiry sweep), oldest deadline first, bounded.
func (r *EmailQueueScheduleRepository) ExpiredEmailIDs(now time.Time, limit int) ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.Email{}).
		Where("expires_at IS NOT NULL AND expires_at < ? AND status IN ?",
			now,
			[]models.EmailStatus{models.EmailStatusPending, models.EmailStatusQueued, models.EmailStatusScheduled},
		).
		Order("expires_at ASC").
		Limit(limit).
		Pluck("id", &ids).Error
	return ids, err
}

// ExpireEmailCAS finalizes one expired message in a single UPDATE: only a row
// still in an undelivered, non-processing state and still past its deadline
// matches. Returns true when this caller won the finalization.
func (r *EmailQueueScheduleRepository) ExpireEmailCAS(id uint, now time.Time, reason string) (bool, error) {
	res := r.db.Model(&models.Email{}).
		Where("id = ? AND expires_at IS NOT NULL AND expires_at < ? AND status IN ?",
			id, now,
			[]models.EmailStatus{models.EmailStatusPending, models.EmailStatusQueued, models.EmailStatusScheduled},
		).
		Updates(map[string]interface{}{
			"status":        models.EmailStatusFailed,
			"error_message": reason,
		})
	return res.RowsAffected > 0, res.Error
}

// MarkCampaignMessageFailed moves the campaign message linked to an expired
// email to failed (best-effort; campaign mail only).
func (r *EmailQueueScheduleRepository) MarkCampaignMessageFailed(emailID uint, reason string) error {
	return r.db.Model(&models.CampaignMessage{}).
		Where("email_id = ? AND status IN ?", emailID,
			[]models.CampaignMessageStatus{models.CampaignMsgPending, models.CampaignMsgQueued}).
		Updates(map[string]interface{}{
			"status":        models.CampaignMsgFailed,
			"error_message": reason,
		}).Error
}
