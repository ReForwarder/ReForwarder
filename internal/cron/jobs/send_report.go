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

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// TypeSendReport is the cron trigger for the daily send-report pass (SF-104
// remainder). The job aggregates the previous UTC day's send outcomes already
// recorded on email_events (the R-12 families: sent, failed, bounced,
// opened, clicked) per workspace, then delivers the summary as (a) a platform
// event on the workspace timeline and (b) optionally an email to the
// workspace admins through the notification template seam.
//
// Policy decisions baked in (documented):
//   - the whole pass is gated by the platform setting send_reports_enabled
//     (admin settings API; default off) — an operator opts in once;
//   - workspaces whose window carried no events are skipped: quiet tenants
//     are never mailed about nothing;
//   - one daily pass aggregates; a delivery failure of the admin email is
//     logged and dropped (the platform event carries the numbers either way).
const TypeSendReport = "cron:send-report"

// SendReportJob is the daily per-workspace send-outcome report pass.
type SendReportJob struct {
	db *gorm.DB
	// enabled is the platform-setting gate (send_reports_enabled, default
	// off). Wired from the settings provider at construction.
	enabled func() bool
	// publish delivers the platform event (production: EventRepository).
	publish func(event *models.Event) error
	// notify emails the workspace admins (production:
	// notification.Service.SendToWorkspaceAdmins). Nil = event-only.
	notify func(workspaceID uint, templateName string, data map[string]any) error
	// now is overridable in tests.
	now func() time.Time
}

// NewSendReportJob builds the job; enabled comes from the settings provider
// (settings.GetBool("send_reports_enabled", false)).
func NewSendReportJob(db *gorm.DB, enabled func() bool) *SendReportJob {
	return &SendReportJob{
		db:      db,
		enabled: enabled,
		now:     time.Now,
	}
}

// SetPublisher attaches the platform-event sink (production: EventRepository.
// Create). Nil disables the event half.
func (j *SendReportJob) SetPublisher(fn func(event *models.Event) error) {
	j.publish = fn
}

// SetNotifier attaches the admin-email seam (production:
// notification.Service.SendToWorkspaceAdmins). Nil keeps the pass event-only.
func (j *SendReportJob) SetNotifier(fn func(workspaceID uint, templateName string, data map[string]any) error) {
	j.notify = fn
}

// SetNow overrides the clock (tests).
func (j *SendReportJob) SetNow(fn func() time.Time) {
	if fn != nil {
		j.now = fn
	}
}

func (j *SendReportJob) Name() string     { return "send-report" }
func (j *SendReportJob) Schedule() string { return "15 7 * * *" } // daily 07:15 UTC

// sendReportCounts holds one workspace's windowed outcome counts.
type sendReportCounts struct {
	WorkspaceID uint
	Sent        int64
	Failed      int64
	Bounced     int64
	Opened      int64
	Clicked     int64
}

func (c sendReportCounts) total() int64 {
	return c.Sent + c.Failed + c.Bounced + c.Opened + c.Clicked
}

func (c sendReportCounts) asMap() map[string]int64 {
	return map[string]int64{
		"sent":    c.Sent,
		"failed":  c.Failed,
		"bounced": c.Bounced,
		"opened":  c.Opened,
		"clicked": c.Clicked,
	}
}

// Run implements cron.Job.
func (j *SendReportJob) Run(_ context.Context, _ *asynq.Client) error {
	if j.enabled == nil || !j.enabled() {
		return nil // send_reports_enabled off: the pass is inert
	}
	if j.publish == nil {
		return nil // no event sink wired; nothing would reach the timeline
	}

	end := j.now().UTC().Truncate(24 * time.Hour)
	start := end.Add(-24 * time.Hour)

	counts, err := j.aggregate(start, end)
	if err != nil {
		return fmt.Errorf("send report: aggregate: %w", err)
	}

	for _, c := range counts {
		if c.total() == 0 {
			continue // quiet workspace: never mailed about nothing
		}
		if err := j.emitEvent(c, start); err != nil {
			logger.Error("send report: platform event failed", "workspace_id", c.WorkspaceID, "error", err)
			continue
		}
		if j.notify != nil {
			data := map[string]any{
				"WindowStart":  start.Format("2006-01-02"),
				"Sent":         c.Sent,
				"Failed":       c.Failed,
				"Bounced":      c.Bounced,
				"Opened":       c.Opened,
				"Clicked":      c.Clicked,
				"TotalEvents":  c.total(),
			}
			if err := j.notify(c.WorkspaceID, "send_report", data); err != nil {
				// The platform event already carries the numbers; the admin
				// email is best-effort.
				logger.Error("send report: admin email failed", "workspace_id", c.WorkspaceID, "error", err)
			}
		}
	}
	return nil
}

// aggregate groups the window's email_events per workspace into the five
// outcome families. Uses grouped SQL with SUM(CASE) so one query returns
// every workspace.
func (j *SendReportJob) aggregate(start, end time.Time) ([]sendReportCounts, error) {
	var rows []sendReportCounts
	err := j.db.Raw(`
		SELECT
			workspace_id AS workspace_id,
			SUM(CASE WHEN type = ? THEN 1 ELSE 0 END) AS sent,
			SUM(CASE WHEN type = ? THEN 1 ELSE 0 END) AS failed,
			SUM(CASE WHEN type = ? THEN 1 ELSE 0 END) AS bounced,
			SUM(CASE WHEN type = ? THEN 1 ELSE 0 END) AS opened,
			SUM(CASE WHEN type = ? THEN 1 ELSE 0 END) AS clicked
		FROM email_events
		WHERE occurred_at >= ? AND occurred_at < ? AND workspace_id IS NOT NULL
		GROUP BY workspace_id`,
		models.EmailEventSent, models.EmailEventFailed, models.EmailEventBounced,
		models.EmailEventOpened, models.EmailEventClicked,
		start, end,
	).Scan(&rows).Error
	return rows, err
}

// emitEvent writes the workspace's report onto its platform-event timeline.
func (j *SendReportJob) emitEvent(c sendReportCounts, start time.Time) error {
	countsJSON, err := json.Marshal(c.asMap())
	if err != nil {
		return err
	}
	return j.publish(&models.Event{
		Category: models.EventCategoryEmail,
		Type:     "email.send_report",
		Message: fmt.Sprintf("Daily send report for %s: %d sent, %d failed, %d bounced, %d opened, %d clicked",
			start.Format("2006-01-02"), c.Sent, c.Failed, c.Bounced, c.Opened, c.Clicked),
		WorkspaceID: &c.WorkspaceID,
		Metadata:    string(countsJSON),
		Outcome:     "success",
	})
}
