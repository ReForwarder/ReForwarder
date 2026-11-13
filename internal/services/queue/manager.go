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

// Package queue is the persistent multi-queue manager's schedule layer (SF-08):
// per-recipient retry schedules (configurable interval lists), per-recipient
// delivery-notification schedules, and message expiry.
//
// The durable queues themselves (asynq over Redis), the virtual queue names,
// the event-locked claims (R-26) and the bounded retry sweeps (R-27) predate
// this package and stay untouched. What SF-08 adds is the per-recipient
// schedule table consulted at claim time to schedule the retry
// (ProcessIn/ProcessAt), notify addresses emitted through the existing event
// taxonomy when a schedule reaches a terminal state, and the TTL expiry sweep
// that rides the unified retention cron (the mailstore-sweeper family pattern,
// ADR-003 §7).
//
// Everything here is additive and nil-safe: a message without schedule rows
// keeps the pre-SF-08 queue behavior exactly, and a nil *Manager degrades
// every method to a no-op so unwired callers never change behavior.
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// Platform settings keys (admin settings API; documented defaults).
const (
	// SettingRetryIntervals is the deployment-wide default retry interval list
	// in minutes ("5,30,120,1440"). Empty (the default) keeps the legacy queue
	// behavior: no schedule rows, asynq/R-27 retry semantics only.
	SettingRetryIntervals = "queue_retry_intervals"
	// SettingExpiryMinutes is the default message TTL in minutes applied to
	// messages enqueued without an explicit expires_at. 0 (the default)
	// disables TTL expiry.
	SettingExpiryMinutes = "queue_message_expiry_minutes"
)

// ScheduleRequest is the per-message schedule configuration. Zero fields fall
// back to the platform settings; everything unset with default settings means
// no schedule rows and no TTL — the legacy behavior.
type ScheduleRequest struct {
	// RetryIntervalsMinutes overrides the recipient retry interval list.
	RetryIntervalsMinutes []int
	// NotifyAddresses overrides/extends the per-recipient notify targets.
	NotifyAddresses []string
	// ExpiresAt sets the message TTL deadline; nil falls back to the
	// queue_message_expiry_minutes setting (0 = no expiry).
	ExpiresAt *time.Time
}

// Schedule decision constants returned by DecideRetry. Decided from the
// message's schedule rows:
//
//   - DecisionNone: no schedule governs the message (no rows, or no row
//     carries a next-retry time) — legacy queue behavior.
//   - DecisionDue: the earliest scheduled retry is due (or overdue) — the
//     retry may proceed.
//   - DecisionWait: the earliest scheduled retry is still in the future —
//     hold the line until then.
//   - DecisionExhausted: every row reached a terminal state — no further
//     attempts are scheduled for this message.
type ScheduleDecision int

const (
	DecisionNone ScheduleDecision = iota
	DecisionDue
	DecisionWait
	DecisionExhausted
)

// SettingsReader is the platform settings seam. *settings.Provider satisfies
// it; tests inject a stub.
type SettingsReader interface {
	GetString(key, fallback string) string
	GetInt(key string, fallback int) int
}

// NotifyDispatcher is the webhook seam (the existing event taxonomy's
// transport). *webhook.Dispatcher satisfies it; tests inject a recorder.
type NotifyDispatcher interface {
	DispatchJSON(userID uint, workspaceID *uint, event string, body []byte, fromAddr string)
}

// Deps wires the manager. Every field is optional; nil/zero keeps the
// corresponding behavior off.
type Deps struct {
	// Settings reads the platform defaults (queue_retry_intervals,
	// queue_message_expiry_minutes). Nil = defaults stay empty (legacy).
	Settings SettingsReader
	// Dispatcher emits the notify events. Nil = no webhook emission (the
	// timeline events still record).
	Dispatcher NotifyDispatcher
	// Recorder writes the message-history timeline events. Nil = no timeline
	// events.
	Recorder *emailhistory.Recorder
	// Now overrides the clock (tests). Nil = time.Now.
	Now func() time.Time
}

// Manager carries the SF-08 schedule state. Construct with NewManager; a nil
// *Manager is safe to call (every method no-ops) so callers can wire it
// unconditionally and keep the pre-SF-08 behavior when unset.
type Manager struct {
	db   *gorm.DB
	repo *repositories.EmailQueueScheduleRepository
	deps Deps
}

func NewManager(db *gorm.DB, deps Deps) *Manager {
	if db == nil {
		return nil
	}
	return &Manager{
		db:   db,
		repo: repositories.NewEmailQueueScheduleRepository(db),
		deps: deps,
	}
}

func (m *Manager) now() time.Time {
	if m.deps.Now != nil {
		return m.deps.Now()
	}
	return time.Now()
}

// interval bounds for request-level and settings-level lists.
const (
	MaxScheduleIntervals   = 24        // entries per list
	MaxIntervalMinutes     = 30 * 1440 // one entry may not exceed 30 days
	DefaultExpiryBatchSize = 500       // sweep page
)

// ParseIntervalList parses a comma/whitespace-separated minute list
// (settings surface). Out-of-range and unparsable entries are rejected — the
// caller surfaces the error rather than silently shaping retries wrong.
func ParseIntervalList(raw string) ([]int, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';' || r == '\t' || r == '\n'
	})
	var out []int
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		v, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("queue retry interval %q is not a number of minutes", f)
		}
		if v < 1 || v > MaxIntervalMinutes {
			return nil, fmt.Errorf("queue retry interval %d is outside 1..%d minutes", v, MaxIntervalMinutes)
		}
		out = append(out, v)
		if len(out) > MaxScheduleIntervals {
			return nil, fmt.Errorf("queue retry interval list exceeds %d entries", MaxScheduleIntervals)
		}
	}
	return out, nil
}

// SanitizeIntervals validates a request-level interval list.
func SanitizeIntervals(intervals []int) error {
	if len(intervals) > MaxScheduleIntervals {
		return fmt.Errorf("retry_intervals exceeds %d entries", MaxScheduleIntervals)
	}
	for _, v := range intervals {
		if v < 1 || v > MaxIntervalMinutes {
			return fmt.Errorf("retry interval %d is outside 1..%d minutes", v, MaxIntervalMinutes)
		}
	}
	return nil
}

// defaultIntervals resolves the platform default interval list; nil when
// unset (or the settings seam is absent).
func (m *Manager) defaultIntervals() []int {
	if m.deps.Settings == nil {
		return nil
	}
	raw := m.deps.Settings.GetString(SettingRetryIntervals, "")
	if raw == "" {
		return nil
	}
	intervals, err := ParseIntervalList(raw)
	if err != nil {
		// A broken settings value must not wedge every retry: log and treat
		// as unset (legacy behavior) until an operator fixes the list.
		logger.Error("queue manager: invalid "+SettingRetryIntervals+" setting, ignoring", "error", err)
		return nil
	}
	return intervals
}

// defaultExpiry resolves the platform default TTL; zero when disabled.
func (m *Manager) defaultExpiry() time.Duration {
	if m.deps.Settings == nil {
		return 0
	}
	mins := m.deps.Settings.GetInt(SettingExpiryMinutes, 0)
	if mins <= 0 {
		return 0
	}
	return time.Duration(mins) * time.Minute
}

// EnsureForEmail prepares the SF-08 queue state for a message about to enter
// the queue:
//
//  1. Stamps em.ExpiresAt when the request carries one, else when the
//     platform TTL default applies, and persists the stamp. Nil expires_at
//     means no expiry — untouched row.
//  2. Creates the per-recipient schedule rows when an interval list (request
//     override or platform default) or notify addresses exist. Rows are
//     idempotent: the (email_id, recipient) unique index makes a re-run a
//     no-op for existing recipients.
//
// nil Manager, dev-mode rows and rows without either input are no-ops — the
// legacy behavior.
func (m *Manager) EnsureForEmail(em *models.Email, req ScheduleRequest) error {
	if m == nil || em == nil || em.ID == 0 {
		return nil
	}
	now := m.now()

	// TTL: request value wins; else the platform default.
	if em.ExpiresAt == nil {
		if req.ExpiresAt != nil {
			em.ExpiresAt = req.ExpiresAt
		} else if ttl := m.defaultExpiry(); ttl > 0 {
			d := now.Add(ttl)
			em.ExpiresAt = &d
		}
		if em.ExpiresAt != nil {
			if err := m.db.Model(&models.Email{}).Where("id = ?", em.ID).
				Update("expires_at", em.ExpiresAt).Error; err != nil {
				return fmt.Errorf("queue manager: failed to stamp expires_at on email %d: %w", em.ID, err)
			}
		}
	}

	// Schedule rows: an interval list (request override, else platform
	// default) or explicit notify addresses create the per-recipient fan-out.
	intervals := req.RetryIntervalsMinutes
	if len(intervals) == 0 {
		intervals = m.defaultIntervals()
	}
	if err := SanitizeIntervals(intervals); err != nil {
		return fmt.Errorf("queue manager: %w", err)
	}
	if len(intervals) == 0 && len(req.NotifyAddresses) == 0 {
		return nil
	}

	rows := make([]models.EmailQueueSchedule, 0, len(em.Recipients))
	for _, rcpt := range em.Recipients {
		rows = append(rows, models.EmailQueueSchedule{
			EmailID:         em.ID,
			Recipient:       rcpt,
			UserID:          em.UserID,
			WorkspaceID:     em.WorkspaceID,
			Provenance:      em.Provenance,
			RetryIntervals:  models.EncodeScheduleIntervals(intervals),
			NotifyAddresses: pqStringArray(req.NotifyAddresses),
			Status:          models.ScheduleStatusActive,
		})
	}
	if err := m.repo.Create(rows); err != nil {
		return fmt.Errorf("queue manager: failed to create schedule rows for email %d: %w", em.ID, err)
	}
	return nil
}

// pqStringArray builds a lib/pq text array; empty input stores an empty array.
func pqStringArray(addrs []string) pq.StringArray {
	if len(addrs) == 0 {
		return pq.StringArray{}
	}
	out := make(pq.StringArray, 0, len(addrs))
	seen := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" || seen[strings.ToLower(a)] {
			continue
		}
		seen[strings.ToLower(a)] = true
		out = append(out, a)
	}
	return out
}

// FailureOutcome reports what RecordFailure decided for the message.
type FailureOutcome struct {
	// AnyRows is true when the message carries schedule rows at all. False =
	// legacy behavior everywhere (callers fall back to their existing paths).
	AnyRows bool
	// NextRetry is the earliest persisted next-retry time across the rows
	// that still have one; nil when none.
	NextRetry *time.Time
	// AllExhausted is true when rows exist and every one of them is terminal
	// after this failure — the message will not be retried again.
	AllExhausted bool
	// NextRetryIn is the delay until NextRetry (0 when nil).
	NextRetryIn time.Duration
}

// RecordFailure advances the schedule state after a failed delivery attempt.
//
// A recipient named by the error (an RCPT TO-stage rejection) advances only
// that recipient's row; a session-level failure advances every still-active
// row. Interval rows consume the next list entry and persist their next due
// time; reaching the end of the list (or a permanent recipient rejection)
// terminates the row as exhausted and fires its notify emission. Rows without
// an interval list (notify-only) are never exhausted here — they stay active
// until the message finalizes.
//
// nil Manager or no rows = zero outcome (legacy).
func (m *Manager) RecordFailure(em *models.Email, recipient string, errMsg string) FailureOutcome {
	if m == nil || em == nil || em.ID == 0 {
		return FailureOutcome{}
	}
	now := m.now()

	rows, err := m.repo.FindByEmailID(em.ID)
	if err != nil {
		logger.Error("queue manager: failed to load schedule rows", "email_id", em.ID, "error", err)
		return FailureOutcome{}
	}
	if len(rows) == 0 {
		// No rows yet: when the platform default intervals are configured,
		// lazily create the fan-out so schedule governance applies to
		// messages that bypass the request surface (campaign, automation,
		// system mail). Otherwise stay legacy.
		intervals := m.defaultIntervals()
		if len(intervals) == 0 {
			return FailureOutcome{}
		}
		newRows := make([]models.EmailQueueSchedule, 0, len(em.Recipients))
		for _, rcpt := range em.Recipients {
			newRows = append(newRows, models.EmailQueueSchedule{
				EmailID:         em.ID,
				Recipient:       rcpt,
				UserID:          em.UserID,
				WorkspaceID:     em.WorkspaceID,
				Provenance:      em.Provenance,
				RetryIntervals:  models.EncodeScheduleIntervals(intervals),
				NotifyAddresses: pq.StringArray{},
				Status:          models.ScheduleStatusActive,
			})
		}
		if err := m.repo.Create(newRows); err != nil {
			logger.Error("queue manager: failed to lazily create schedule rows", "email_id", em.ID, "error", err)
			return FailureOutcome{}
		}
		// Reload with the database-assigned ids: the conflict-clause batch
		// insert does not reliably populate the in-memory ids, and the CAS
		// advance below keys on them.
		rows, err = m.repo.FindByEmailID(em.ID)
		if err != nil {
			logger.Error("queue manager: failed to reload lazily created schedule rows", "email_id", em.ID, "error", err)
			return FailureOutcome{}
		}
	}

	out := FailureOutcome{AnyRows: true}
	activeLeft := 0
	for i := range rows {
		row := &rows[i]
		if row.ScheduleTerminal() {
			continue
		}
		if recipient != "" && !strings.EqualFold(row.Recipient, recipient) {
			// A per-recipient failure: the other rows' schedules do not
			// advance on it, but they still count toward the active set.
			activeLeft++
			m.noteEarliest(&out, row.NextRetryAt)
			continue
		}

		intervals := row.IntervalList()
		if len(intervals) == 0 {
			// Notify-only row: no interval schedule; stays active.
			activeLeft++
			m.noteEarliest(&out, row.NextRetryAt)
			continue
		}

		exhausted := row.AttemptIndex >= len(intervals)
		var next *time.Time
		if !exhausted {
			d := now.Add(time.Duration(intervals[row.AttemptIndex]) * time.Minute)
			next = &d
		}
		res, err := m.repo.AdvanceCAS(row.ID, next, exhausted, errMsg, now)
		if err != nil {
			logger.Error("queue manager: failed to advance schedule row", "email_id", em.ID, "recipient", row.Recipient, "error", err)
			continue
		}
		if !res.Matched {
			// A racing claimant already advanced it; honor its stored state.
			if row.NextRetryAt != nil && row.NextRetryAt.After(now) {
				activeLeft++
				m.noteEarliest(&out, row.NextRetryAt)
			}
			continue
		}
		row.AttemptIndex++
		row.NextRetryAt = next
		if exhausted {
			row.Status = models.ScheduleStatusExhausted
			m.emitScheduleTerminal(em, row, "recipient retry schedule exhausted", now)
		} else {
			activeLeft++
			m.noteEarliest(&out, next)
		}
	}

	out.AllExhausted = activeLeft == 0
	return out
}

// noteEarliest keeps the earliest non-nil next-retry on the outcome.
func (m *Manager) noteEarliest(out *FailureOutcome, t *time.Time) {
	if t == nil {
		return
	}
	if out.NextRetry == nil || t.Before(*out.NextRetry) {
		out.NextRetry = t
	}
}

// DecideRetry consults the schedule at claim time (the ProcessTask claim, the
// retry sweep's candidate check, the queue-reconcile pass): it returns the
// decision and — for DecisionDue/DecisionWait — the earliest scheduled retry
// time.
func (m *Manager) DecideRetry(emailID uint, now time.Time) (ScheduleDecision, time.Time) {
	if m == nil {
		return DecisionNone, time.Time{}
	}
	rows, err := m.repo.FindByEmailID(emailID)
	if err != nil || len(rows) == 0 {
		return DecisionNone, time.Time{}
	}
	earliest := time.Time{}
	sawActive := false
	sawScheduled := false
	for i := range rows {
		row := &rows[i]
		if row.ScheduleTerminal() {
			continue
		}
		sawActive = true
		if row.NextRetryAt == nil {
			continue // notify-only or first attempt: no timing to honor
		}
		sawScheduled = true
		if earliest.IsZero() || row.NextRetryAt.Before(earliest) {
			earliest = *row.NextRetryAt
		}
	}
	if !sawActive {
		return DecisionExhausted, time.Time{}
	}
	if !sawScheduled || earliest.IsZero() {
		return DecisionNone, time.Time{}
	}
	if !earliest.After(now) {
		return DecisionDue, earliest
	}
	return DecisionWait, earliest
}

// RetryDelay returns the delay asynq must hold the send task before its next
// attempt (the RetryDelayFunc consult): the time until the earliest scheduled
// retry. ok=false → no schedule governs; the caller falls back to its default
// delay policy.
func (m *Manager) RetryDelay(emailID uint, now time.Time) (time.Duration, bool) {
	decision, due := m.DecideRetry(emailID, now)
	if decision != DecisionDue && decision != DecisionWait {
		return 0, false
	}
	if decision == DecisionDue {
		return 0, true // overdue: retry immediately
	}
	return due.Sub(now), true
}

// MarkDelivered closes every active schedule row of a delivered message.
func (m *Manager) MarkDelivered(emailID uint) {
	if m == nil || emailID == 0 {
		return
	}
	if err := m.repo.MarkDeliveredForEmail(emailID, m.now()); err != nil {
		logger.Error("queue manager: failed to mark schedule rows delivered", "email_id", emailID, "error", err)
	}
}

// FinalizeEmail closes every active schedule row of a message finalized
// terminal by a non-retry path (configuration refusal, content problem,
// recipient suppression, …). Each won transition emits its notify event.
func (m *Manager) FinalizeEmail(em *models.Email, reason string) {
	if m == nil || em == nil || em.ID == 0 {
		return
	}
	now := m.now()
	winners, err := m.repo.TerminateAllForEmail(em.ID, models.ScheduleStatusCancelled, reason, now)
	if err != nil {
		logger.Error("queue manager: failed to finalize schedule rows", "email_id", em.ID, "error", err)
		return
	}
	if len(winners) == 0 {
		return
	}
	rows, err := m.repo.FindByEmailID(em.ID)
	if err != nil {
		return
	}
	byID := make(map[uint]*models.EmailQueueSchedule, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	for _, id := range winners {
		if row, ok := byID[id]; ok {
			row.Status = models.ScheduleStatusCancelled
			m.emitScheduleTerminal(em, row, reason, now)
		}
	}
}

// MarkRecipientRejected closes one recipient's schedule row as exhausted
// after a permanent rejection suppressed it. Silent on purpose: the
// suppression path already emits the message-level taxonomy events (failure,
// bounce, webhook, optional SF-10 DSN); a row-level emission would
// double-fire them.
func (m *Manager) MarkRecipientRejected(em *models.Email, recipient string, errMsg string) {
	if m == nil || em == nil || em.ID == 0 || recipient == "" {
		return
	}
	rows, err := m.repo.FindByEmailID(em.ID)
	if err != nil {
		return
	}
	for i := range rows {
		if !strings.EqualFold(rows[i].Recipient, recipient) || rows[i].ScheduleTerminal() {
			continue
		}
		if _, err := m.repo.TerminateCAS(rows[i].ID, models.ScheduleStatusExhausted, errMsg, m.now()); err != nil {
			logger.Error("queue manager: failed to terminate rejected recipient's schedule row",
				"email_id", em.ID, "recipient", recipient, "error", err)
		}
	}
}

// QueueExpiryStats is the unified retention cron's per-run summary.
type QueueExpiryStats struct {
	// Expired counts messages marked failed by this sweep.
	Expired int
	// Notified counts per-recipient notify emissions fired by this sweep.
	Notified int
	// Skipped counts candidates that were finalized elsewhere between the
	// listing and the CAS (delivered meanwhile, another replica's sweep, …).
	Skipped int
}

// SweepExpired is the SF-08 expiry sweep, riding the unified retention cron
// (the mailstore-sweeper family pattern): every still-undelivered message
// (pending/queued/scheduled) whose expires_at deadline passed is marked
// failed with an event, its schedule rows expire with their notify emission,
// and linked campaign messages are moved to failed. The claim is a CAS per
// message, so concurrent sweeps and in-flight deliveries cannot double-fire.
func (m *Manager) SweepExpired(_ context.Context, now time.Time, batch int) (QueueExpiryStats, error) {
	var stats QueueExpiryStats
	if m == nil {
		return stats, nil
	}
	if batch <= 0 {
		batch = DefaultExpiryBatchSize
	}

	ids, err := m.repo.ExpiredEmailIDs(now, batch)
	if err != nil {
		return stats, fmt.Errorf("queue expiry sweep: failed to list expired emails: %w", err)
	}
	if len(ids) == 0 {
		return stats, nil
	}

	var emails []models.Email
	if err := m.db.Where("id IN ?", ids).Find(&emails).Error; err != nil {
		return stats, fmt.Errorf("queue expiry sweep: failed to load expired emails: %w", err)
	}

	for i := range emails {
		em := &emails[i]
		won, err := m.repo.ExpireEmailCAS(em.ID, now, ExpiryReason)
		if err != nil {
			logger.Error("queue expiry sweep: claim failed", "email_id", em.ID, "error", err)
			continue
		}
		if !won {
			stats.Skipped++
			continue
		}
		stats.Notified += m.finalizeExpired(em, now, ExpiryReason)
		stats.Expired++
		logger.Info("queue expiry sweep: marked expired email failed", "email_id", em.ID)
	}
	return stats, nil
}

// ExpiryReason is the terminal error text for both expiry paths (the
// retention sweep and the SF-09 claim-time deadline check); the deadline's
// provenance rides the event metadata, not the reason text.
const ExpiryReason = "message expired in the queue (TTL elapsed before delivery)"

// expiryEventMetadata builds the DSN-style metadata for a message-level
// expiry event (SF-09): the RFC 3463 enhanced status and diagnostic for a
// delivery time that ran out — the permanent class, matching the terminal
// status flip — plus the deadline provenance when it came from the RFC 2852
// DELIVERBY parameter.
func expiryEventMetadata(em *models.Email, scheduleEvent string) map[string]any {
	metadata := map[string]any{
		"schedule_event":  scheduleEvent,
		"expires_at":      em.ExpiresAt,
		"action":          "failed",
		"enhanced_status": "5.4.7",
		"diagnostic":      "delivery time expired",
	}
	if em.DeliverBySeconds > 0 {
		metadata["deliver_by_seconds"] = em.DeliverBySeconds
		metadata["deadline_source"] = "deliverby"
	}
	return metadata
}

// finalizeExpired completes one expiry whose CAS claim was won: schedule rows
// expire with their notify emission, the DSN-style message event records
// through the existing taxonomy, and linked campaign messages fail with the
// message. Returns the number of webhook emissions fired.
func (m *Manager) finalizeExpired(em *models.Email, now time.Time, reason string) int {
	em.Status = models.EmailStatusFailed
	em.ErrorMessage = reason

	// Schedule rows: expire + per-recipient notify.
	notified := 0
	rows, rerr := m.repo.FindByEmailID(em.ID)
	if rerr == nil {
		for j := range rows {
			if rows[j].ScheduleTerminal() {
				continue
			}
			ok, terr := m.repo.TerminateCAS(rows[j].ID, models.ScheduleStatusExpired, reason, now)
			if terr != nil {
				logger.Error("queue expiry: failed to expire schedule row", "email_id", em.ID, "error", terr)
				continue
			}
			if ok {
				rows[j].Status = models.ScheduleStatusExpired
				if m.emitScheduleTerminal(em, &rows[j], reason, now) {
					notified++
				}
			}
		}
	}

	// Message-level expiry event through the existing taxonomy.
	emailhistory.RecordFailed(m.deps.Recorder, em, reason, expiryEventMetadata(em, "message_expired"))
	if m.deps.Dispatcher != nil {
		m.dispatchSchedulePayload(em, nil, reason, "message_expired")
	}

	// Campaign bookkeeping: linked, still-pending campaign messages fail
	// with the message (the campaign UI shows the terminal cause).
	if err := m.repo.MarkCampaignMessageFailed(em.ID, reason); err != nil {
		logger.Warn("queue expiry: failed to update campaign message", "email_id", em.ID, "error", err)
	}
	return notified
}

// ExpireByDeadline is the SF-09 claim-time deadline check: called from the
// send handler before the R-26 claim, it refuses delivery of any message
// whose expires_at deadline has passed — however the task reached the claim
// (a fired retry, a manual re-enqueue ahead of the retention sweep's daily
// pass). The CAS includes processing rows: a redelivered task on a row
// mid-send must never deliver past the deadline either. A nil manager or a
// row without a deadline is a no-op (nil error).
func (m *Manager) ExpireByDeadline(em *models.Email, now time.Time) error {
	if m == nil || em == nil || em.ID == 0 || em.ExpiresAt == nil || em.ExpiresAt.After(now) {
		return nil
	}
	won, err := m.repo.ExpireEmailCASInStatuses(em.ID, now, ExpiryReason, repositories.ClaimTimeExpiryStatuses)
	if err != nil {
		return fmt.Errorf("queue deadline check: claim failed for email %d: %w", em.ID, err)
	}
	if !won {
		// Already finalized elsewhere (delivered raced and lost its claim, or
		// another claimant's deadline check won) — nothing to do.
		return nil
	}
	m.finalizeExpired(em, now, ExpiryReason)
	logger.Info("queue deadline check: expired message at claim time", "email_id", em.ID)
	return nil
}

// emitScheduleTerminal records the per-recipient terminal event: the timeline
// event always (queue bookkeeping in the message history), the webhook notify
// only when the row carries notify addresses. Returns true when a webhook
// emission fired.
func (m *Manager) emitScheduleTerminal(em *models.Email, row *models.EmailQueueSchedule, reason string, now time.Time) bool {
	metadata := map[string]any{
		"schedule_event":   "recipient_schedule_" + string(row.Status),
		"recipient":        row.Recipient,
		"notify_addresses": []string(row.NotifyAddresses),
		"attempt_index":    row.AttemptIndex,
		"retry_intervals":  row.IntervalList(),
	}
	// The row-level event carries its own provider-event id: without it the
	// (email_uuid, type, provider_event_id) dedup index would collapse it
	// into (or be collapsed by) the message-level failure event on the same
	// timeline. Replays of the same terminal transition dedup on this id.
	emailhistory.RecordFailedDeduped(m.deps.Recorder, em, reason,
		fmt.Sprintf("queue-schedule:%d:%s:%s:%d", em.ID, strings.ToLower(row.Recipient), row.Status, row.AttemptIndex),
		metadata)
	if len(row.NotifyAddresses) == 0 || m.deps.Dispatcher == nil {
		return false
	}
	m.dispatchSchedulePayload(em, row, reason, "recipient_schedule_"+string(row.Status))
	return true
}

// dispatchSchedulePayload sends the email.failed webhook carrying the
// schedule trace (the existing event name; the richer payload shape rides
// DispatchJSON, the documented escape hatch the inbound pipeline also uses).
func (m *Manager) dispatchSchedulePayload(em *models.Email, row *models.EmailQueueSchedule, reason, scheduleEvent string) {
	payload := map[string]any{
		"event":          "email.failed",
		"email_id":       em.UUID,
		"timestamp":      m.now().UTC().Format(time.RFC3339),
		"sender":         em.Sender,
		"reason":         reason,
		"schedule_event": scheduleEvent,
	}
	// The DSN-style expiry fields (RFC 3463 X.4.7, permanent class — the
	// terminal status flip) ride only the message-expired event; a row-level
	// exhaustion/cancellation is a different diagnostic.
	if scheduleEvent == "message_expired" {
		payload["action"] = "failed"
		payload["enhanced_status"] = "5.4.7"
		payload["diagnostic"] = "delivery time expired"
		if em.DeliverBySeconds > 0 {
			payload["deliver_by_seconds"] = em.DeliverBySeconds
			payload["deadline_source"] = "deliverby"
		}
	}
	if row != nil {
		payload["recipient"] = row.Recipient
		payload["notify_addresses"] = []string(row.NotifyAddresses)
		payload["attempt_index"] = row.AttemptIndex
	}
	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("queue manager: failed to marshal notify payload", "email_id", em.ID, "error", err)
		return
	}
	m.deps.Dispatcher.DispatchJSON(em.UserID, em.WorkspaceID, "email.failed", body, em.Sender)
}
