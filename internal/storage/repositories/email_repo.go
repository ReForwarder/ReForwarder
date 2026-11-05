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
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

type EmailRepository struct {
	db *gorm.DB
}

func NewEmailRepository(db *gorm.DB) *EmailRepository {
	return &EmailRepository{db: db}
}

func (r *EmailRepository) Create(email *models.Email) error {
	return r.db.Create(email).Error
}

func (r *EmailRepository) Update(email *models.Email) error {
	return r.db.Save(email).Error
}

// ClaimForProcessing atomically claims an email for delivery (R-26): the
// queued→processing flip happens in a single UPDATE, so two concurrent
// deliveries of duplicate tasks cannot both send. Only the claimant whose
// UPDATE matched (RowsAffected > 0) proceeds; the loser skips the task.
//
// processing rows are claimable too: asynq redelivers a task after a worker
// crash (graceful shutdown, lease expiry) while the row is still processing,
// and no second task can exist for a processing row — the retry worker and the
// queue reconcile only touch failed/queued rows, so a processing row's task is
// always the redelivery of the same task.
func (r *EmailRepository) ClaimForProcessing(id uint) (bool, error) {
	res := r.db.Model(&models.Email{}).
		Where("id = ? AND status IN ?", id,
			[]models.EmailStatus{models.EmailStatusQueued, models.EmailStatusProcessing}).
		Update("status", models.EmailStatusProcessing)
	return res.RowsAffected > 0, res.Error
}

// ClaimForRetry atomically claims a failed email for another delivery attempt
// (R-26): status flips failed→queued, the retry counter increments, and the
// error clears in one UPDATE. A user retry and the retry poller (or two
// concurrent retries) can therefore never both claim the same row and enqueue
// two tasks. Returns false when the row was not failed — another claimant won.
func (r *EmailRepository) ClaimForRetry(id uint) (bool, error) {
	res := r.db.Model(&models.Email{}).
		Where("id = ? AND status = ?", id, models.EmailStatusFailed).
		Updates(map[string]interface{}{
			"status":        models.EmailStatusQueued,
			"retry_count":   gorm.Expr("retry_count + 1"),
			"error_message": "",
		})
	return res.RowsAffected > 0, res.Error
}

// ClaimFailedForProcessing claims a failed email for an immediate synchronous
// send attempt (R-26): failed→processing in one UPDATE, so two pollers (or a
// poller and a user retry racing the send) cannot both deliver the same row.
func (r *EmailRepository) ClaimFailedForProcessing(id uint) (bool, error) {
	res := r.db.Model(&models.Email{}).
		Where("id = ? AND status = ?", id, models.EmailStatusFailed).
		Update("status", models.EmailStatusProcessing)
	return res.RowsAffected > 0, res.Error
}

func (r *EmailRepository) FindByID(id uint) (*models.Email, error) {
	var email models.Email
	if err := r.db.First(&email, id).Error; err != nil {
		return nil, err
	}
	return &email, nil
}

func (r *EmailRepository) FindByUUID(uuid string) (*models.Email, error) {
	var email models.Email
	if err := r.db.Where("uuid = ?", uuid).First(&email).Error; err != nil {
		return nil, err
	}
	return &email, nil
}

func (r *EmailRepository) FindByUUIDWithAPIKey(uuid string) (*models.Email, error) {
	var email models.Email
	if err := r.db.Preload("APIKey").Where("uuid = ?", uuid).First(&email).Error; err != nil {
		return nil, err
	}
	return &email, nil
}

// slimEmailColumns is the column list for message-history queries: every
// metadata column of an email except the bodies and the attachment/header
// blobs, so a page of rows never drags megabytes of HTML through the driver.
// Bodies and render data are served only by the dedicated content endpoint.
const slimEmailColumns = "id, uuid, external_id, workspace_id, user_id, api_key_id, " +
	"sender, recipients, subject, template_name, status, error_message, retry_count, " +
	"provider, smtp_hostname, unsubscribe_list_id, created_at, scheduled_at, sent_at, " +
	"delivered_at, opened_at, clicked_at, bounced_at, complained_at, unsubscribed_at"

// FindByUUIDSlim returns one email without its bodies or content blobs — the
// shape the message-history detail endpoint serves. Content lives behind the
// dedicated content endpoint so a plain Get can never leak HTML.
func (r *EmailRepository) FindByUUIDSlim(uuid string) (*models.Email, error) {
	var email models.Email
	if err := r.db.Select(slimEmailColumns).Where("uuid = ?", uuid).First(&email).Error; err != nil {
		return nil, err
	}
	return &email, nil
}

// emailStatusTimestampColumns whitelists the write-once lifecycle timestamp
// columns. UPDATE statements are built only from these constants — user input
// never reaches the column position.
var emailStatusTimestampColumns = map[string]string{
	"sent_at":         "sent_at",
	"delivered_at":    "delivered_at",
	"opened_at":       "opened_at",
	"clicked_at":      "clicked_at",
	"bounced_at":      "bounced_at",
	"complained_at":   "complained_at",
	colUnsubscribedAt: colUnsubscribedAt,
}

// UpdateStatusTimestamp writes a write-once lifecycle timestamp on an email
// row: an existing value is never overwritten, so "first opened" survives
// repeated open events even when they race. column is a timestamp name such as
// "opened_at"; anything outside the whitelist is rejected.
func (r *EmailRepository) UpdateStatusTimestamp(uuid, column string, t time.Time) error {
	col, ok := emailStatusTimestampColumns[column]
	if !ok {
		return fmt.Errorf("email status timestamp column %q is not writable", column)
	}
	return r.db.Exec(
		fmt.Sprintf("UPDATE emails SET %s = ? WHERE uuid = ? AND %s IS NULL", col, col),
		t, uuid,
	).Error
}

func (r *EmailRepository) FindByUserID(userID uint, limit, offset int) ([]models.Email, int64, error) {
	var emails []models.Email
	var total int64

	r.db.Model(&models.Email{}).Where("user_id = ? AND workspace_id IS NULL", userID).Count(&total)

	if err := r.db.Where("user_id = ? AND workspace_id IS NULL", userID).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&emails).Error; err != nil {
		return nil, 0, err
	}
	return emails, total, nil
}

func (r *EmailRepository) FindByWorkspaceID(workspaceID uint, limit, offset int) ([]models.Email, int64, error) {
	var emails []models.Email
	var total int64

	r.db.Model(&models.Email{}).Where("workspace_id = ?", workspaceID).Count(&total)

	if err := r.db.Where("workspace_id = ?", workspaceID).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&emails).Error; err != nil {
		return nil, 0, err
	}
	return emails, total, nil
}

func (r *EmailRepository) FindByScope(scope ResourceScope, limit, offset int) ([]models.Email, int64, error) {
	var items []models.Email
	var total int64

	ApplyScope(r.db.Model(&models.Email{}), scope).Count(&total)

	if err := ApplyScope(r.db, scope).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// statusFlagColumns maps the message-history status flags to write-once
// timestamp columns: the flag matches any email whose timestamp is set. "sent"
// is resolved through the timestamp, not the status column, so queued-then-
// sent mail keeps matching once it left the queue.
var statusFlagColumns = map[string]string{
	"sent":         "sent_at",
	"delivered":    "delivered_at",
	"opened":       "opened_at",
	"clicked":      "clicked_at",
	"bounced":      "bounced_at",
	"complained":   "complained_at",
	"unsubscribed": colUnsubscribedAt,
}

// statusFlagValues maps the remaining message-history status flags to
// Email.Status values.
var statusFlagValues = map[string]models.EmailStatus{
	"pending":    models.EmailStatusPending,
	"queued":     models.EmailStatusQueued,
	"processing": models.EmailStatusProcessing,
	"failed":     models.EmailStatusFailed,
	"suppressed": models.EmailStatusSuppressed,
	"scheduled":  models.EmailStatusScheduled,
}

// statusFlagConds builds the OR'd predicate for the message-history status
// flags: status=sent&status=opened matches mail that is either. Unknown flags
// are ignored — the UI can only offer the known set.
func statusFlagConds(flags []string) (string, []any) {
	var conds []string
	var args []any
	seen := make(map[string]bool, len(flags))
	for _, flag := range flags {
		flag = strings.ToLower(strings.TrimSpace(flag))
		if flag == "" || seen[flag] {
			continue
		}
		seen[flag] = true
		if col, ok := statusFlagColumns[flag]; ok {
			conds = append(conds, col+" IS NOT NULL")
			continue
		}
		if status, ok := statusFlagValues[flag]; ok {
			conds = append(conds, "status = ?")
			args = append(args, status)
		}
	}
	return strings.Join(conds, " OR "), args
}

// applyEmailFilter narrows a query to the caller's scope and the non-empty
// fields of an EmailFilter. Shared by the scoped list (full rows) and the
// message-history list (slim rows) so both stay in lockstep.
func applyEmailFilter(q *gorm.DB, scope ResourceScope, f EmailFilter) *gorm.DB {
	q = ApplyScope(q, scope)
	if f.Sender != "" {
		q = q.Where("LOWER(sender) LIKE ?", likePattern(f.Sender))
	}
	if f.Recipient != "" {
		q = q.Where("EXISTS (SELECT 1 FROM unnest(recipients) AS r WHERE LOWER(r) LIKE ?)",
			likePattern(f.Recipient))
	}
	if f.Subject != "" {
		q = q.Where("LOWER(subject) LIKE ?", likePattern(f.Subject))
	}
	if f.Text != "" {
		pat := likePattern(f.Text)
		q = q.Where("(LOWER(subject) LIKE ? OR EXISTS (SELECT 1 FROM unnest(recipients) AS r WHERE LOWER(r) LIKE ?))",
			pat, pat)
	}
	if f.Template != "" {
		q = q.Where("LOWER(template_name) LIKE ?", likePattern(f.Template))
	}
	if len(f.Providers) > 0 {
		cond, args := likeAny("LOWER(provider)", f.Providers)
		q = q.Where(cond, args...)
	}
	if len(f.Servers) > 0 {
		cond, args := likeAny("LOWER(smtp_hostname)", f.Servers)
		q = q.Where(cond, args...)
	}
	if len(f.APIKeys) > 0 {
		cond, args := likeAny("LOWER(k.name)", f.APIKeys)
		q = q.Where("EXISTS (SELECT 1 FROM api_keys k WHERE k.id = emails.api_key_id AND "+cond+")", args...)
	}
	if len(f.Statuses) > 0 {
		q = q.Where("status IN ?", f.Statuses)
	}
	if len(f.StatusFlags) > 0 {
		if cond, args := statusFlagConds(f.StatusFlags); cond != "" {
			q = q.Where("("+cond+")", args...)
		}
	}
	if f.ExternalID != "" {
		q = q.Where("external_id = ?", f.ExternalID)
	}
	if f.MessageUUID != "" {
		q = q.Where("uuid = ?", f.MessageUUID)
	}
	if f.ListID != nil {
		q = q.Where("unsubscribe_list_id = ?", *f.ListID)
	}
	if f.BlockID != nil {
		// Suppressions link to emails by recipient address: the message
		// matches when any of its recipients is on that block list.
		q = q.Where(
			"EXISTS (SELECT 1 FROM unnest(recipients) AS r WHERE LOWER(r) IN (SELECT LOWER(email) FROM suppressions s WHERE s.list_id = ? AND s.workspace_id = emails.workspace_id))",
			*f.BlockID,
		)
	}
	if f.BroadcastID != nil {
		// Campaign mail is linked to its campaign through campaign_messages.
		q = q.Where("EXISTS (SELECT 1 FROM campaign_messages cm WHERE cm.campaign_id = ? AND cm.email_id = emails.id)", *f.BroadcastID)
	}
	if f.HasAttachment {
		q = q.Where("attachments_json <> '' AND attachments_json <> '[]'")
	}
	if f.After != nil {
		q = q.Where("created_at >= ?", *f.After)
	}
	if f.Before != nil {
		q = q.Where("created_at < ?", *f.Before)
	}
	return q
}

// FindByScopeFiltered returns scoped emails narrowed by the given search filter.
// sort is an optional sort key (e.g. "subject", "-sent_at"); empty/unknown keeps
// the default newest-first order.
func (r *EmailRepository) FindByScopeFiltered(scope ResourceScope, f EmailFilter, sort string, limit, offset int) ([]models.Email, int64, error) {
	var items []models.Email
	var total int64

	applyEmailFilter(r.db.Model(&models.Email{}), scope, f).Count(&total)

	if err := applyEmailFilter(r.db, scope, f).
		Order(orderClause(sort)).
		Limit(limit).Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListFilteredMessages returns a page of the message history: the same rows as
// FindByScopeFiltered, narrowed by the message-history filters (status flags,
// external id), but with bodies and content blobs excluded — see
// slimEmailColumns. Events and broadcast context are enriched by the caller.
func (r *EmailRepository) ListFilteredMessages(page, size int, scope ResourceScope, f EmailFilter) ([]models.Email, int64, error) {
	limit, offset := normalizeSecurityPage(page, size)

	var items []models.Email
	var total int64

	if err := applyEmailFilter(r.db.Model(&models.Email{}), scope, f).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := applyEmailFilter(r.db, scope, f).
		Select(slimEmailColumns).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// FindFailedForRetry returns failed emails with retry_count < maxRetries for a given user.
func (r *EmailRepository) FindFailedForRetry(userID uint, maxRetries int) ([]models.Email, error) {
	var emails []models.Email
	if err := r.db.Where("user_id = ? AND status = ? AND retry_count < ?", userID, models.EmailStatusFailed, maxRetries).
		Order("created_at ASC").
		Find(&emails).Error; err != nil {
		return nil, err
	}
	return emails, nil
}

// FindFailedForRetryByWorkspace returns failed emails for a workspace that haven't exceeded max retries.
func (r *EmailRepository) FindFailedForRetryByWorkspace(workspaceID uint, maxRetries int) ([]models.Email, error) {
	var emails []models.Email
	if err := r.db.Where("workspace_id = ? AND status = ? AND retry_count < ?", workspaceID, models.EmailStatusFailed, maxRetries).
		Order("created_at ASC").
		Find(&emails).Error; err != nil {
		return nil, err
	}
	return emails, nil
}

func (r *EmailRepository) FindAll(limit, offset int) ([]models.Email, int64, error) {
	var emails []models.Email
	var total int64

	r.db.Model(&models.Email{}).Count(&total)

	if err := r.db.Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&emails).Error; err != nil {
		return nil, 0, err
	}
	return emails, total, nil
}

// DeleteOlderThan deletes email records older than the given time.
// Returns the number of rows deleted.
func (r *EmailRepository) DeleteOlderThan(before time.Time) (int64, error) {
	result := r.db.Where("created_at < ?", before).Delete(&models.Email{})
	return result.RowsAffected, result.Error
}

// AttachmentsJSONOlderThan returns the attachments_json blob for every email
// row older than the cutoff. The caller decodes each to enumerate blob-storage
// keys so they can be deleted before the rows are dropped.
func (r *EmailRepository) AttachmentsJSONOlderThan(before time.Time) ([]string, error) {
	var out []string
	err := r.db.Model(&models.Email{}).
		Where("created_at < ? AND attachments_json <> ''", before).
		Pluck("attachments_json", &out).Error
	return out, err
}

// ScrubBodiesOlderThan clears HTML/text bodies of emails older than the cutoff,
// keeping the record. Returns the number of rows scrubbed.
func (r *EmailRepository) ScrubBodiesOlderThan(before time.Time) (int64, error) {
	result := r.db.Model(&models.Email{}).
		Where("created_at < ? AND (html_body <> '' OR text_body <> '')", before).
		Updates(map[string]interface{}{"html_body": "", "text_body": ""})
	return result.RowsAffected, result.Error
}

// ScrubAttachmentsOlderThan clears attachment metadata of emails older than the
// cutoff, keeping the record. Blobs are deleted separately by the caller.
func (r *EmailRepository) ScrubAttachmentsOlderThan(before time.Time) (int64, error) {
	result := r.db.Model(&models.Email{}).
		Where("created_at < ? AND attachments_json <> ''", before).
		Update("attachments_json", "")
	return result.RowsAffected, result.Error
}
