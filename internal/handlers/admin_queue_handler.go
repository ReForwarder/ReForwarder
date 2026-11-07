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

package handlers

import (
	"errors"
	"fmt"
	"strings"
	"time"

	cronjobs "github.com/goposta/posta/internal/cron/jobs"
	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/queues"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// QueueInspector is the narrow slice of *asynq.Inspector the queue
// administration API uses. It exists so the handlers below are testable with a
// stub: a real asynq.Inspector needs a live Redis, and the repository's CI has
// a database service but no Redis harness for handler tests.
type QueueInspector interface {
	GetQueueInfo(queue string) (*asynq.QueueInfo, error)
	GetTaskInfo(queue, id string) (*asynq.TaskInfo, error)
	ListPendingTasks(queue string, page, size int) ([]*asynq.TaskInfo, error)
	ListActiveTasks(queue string, page, size int) ([]*asynq.TaskInfo, error)
	ListScheduledTasks(queue string, page, size int) ([]*asynq.TaskInfo, error)
	ListRetryTasks(queue string, page, size int) ([]*asynq.TaskInfo, error)
	ListArchivedTasks(queue string, page, size int) ([]*asynq.TaskInfo, error)
	DeleteTask(queue, id string) error
	RunTask(queue, id string) error
	ArchiveTask(queue, id string) error
	PauseQueue(queue string) error
	UnpauseQueue(queue string) error
}

// AsynqQueueInspector adapts *asynq.Inspector to QueueInspector, translating
// the ListOption variadics into (page, size).
type AsynqQueueInspector struct {
	insp *asynq.Inspector
}

// NewAsynqQueueInspector wraps a real asynq.Inspector.
func NewAsynqQueueInspector(insp *asynq.Inspector) *AsynqQueueInspector {
	return &AsynqQueueInspector{insp: insp}
}

func (a *AsynqQueueInspector) GetQueueInfo(queue string) (*asynq.QueueInfo, error) {
	return a.insp.GetQueueInfo(queue)
}

func (a *AsynqQueueInspector) GetTaskInfo(queue, id string) (*asynq.TaskInfo, error) {
	return a.insp.GetTaskInfo(queue, id)
}

func (a *AsynqQueueInspector) ListPendingTasks(queue string, page, size int) ([]*asynq.TaskInfo, error) {
	return a.insp.ListPendingTasks(queue, asynq.PageSize(size), asynq.Page(page))
}

func (a *AsynqQueueInspector) ListActiveTasks(queue string, page, size int) ([]*asynq.TaskInfo, error) {
	return a.insp.ListActiveTasks(queue, asynq.PageSize(size), asynq.Page(page))
}

func (a *AsynqQueueInspector) ListScheduledTasks(queue string, page, size int) ([]*asynq.TaskInfo, error) {
	return a.insp.ListScheduledTasks(queue, asynq.PageSize(size), asynq.Page(page))
}

func (a *AsynqQueueInspector) ListRetryTasks(queue string, page, size int) ([]*asynq.TaskInfo, error) {
	return a.insp.ListRetryTasks(queue, asynq.PageSize(size), asynq.Page(page))
}

func (a *AsynqQueueInspector) ListArchivedTasks(queue string, page, size int) ([]*asynq.TaskInfo, error) {
	return a.insp.ListArchivedTasks(queue, asynq.PageSize(size), asynq.Page(page))
}

func (a *AsynqQueueInspector) DeleteTask(queue, id string) error {
	return a.insp.DeleteTask(queue, id)
}

func (a *AsynqQueueInspector) RunTask(queue, id string) error {
	return a.insp.RunTask(queue, id)
}

func (a *AsynqQueueInspector) ArchiveTask(queue, id string) error {
	return a.insp.ArchiveTask(queue, id)
}

func (a *AsynqQueueInspector) PauseQueue(queue string) error {
	return a.insp.PauseQueue(queue)
}

func (a *AsynqQueueInspector) UnpauseQueue(queue string) error {
	return a.insp.UnpauseQueue(queue)
}

// adminQueues are the queues the send pipeline consumes (mirrors the weights
// in the asynq server configs). Anything else is answered 404 rather than
// probed in Redis.
var adminQueues = []string{queues.Transactional, queues.Bulk, queues.Low}

func isManagedQueue(name string) bool {
	for _, q := range adminQueues {
		if q == name {
			return true
		}
	}
	return false
}

// QueueStats is the per-queue view of one asynq queue.
type QueueStats struct {
	Queue     string `json:"queue"`
	Size      int    `json:"size"`
	Pending   int    `json:"pending"`
	Active    int    `json:"active"`
	Scheduled int    `json:"scheduled"`
	Retry     int    `json:"retry"`
	Archived  int    `json:"archived"`
	Processed int    `json:"processed"`
	Failed    int    `json:"failed"`
	LatencyMS int64  `json:"latency_ms"`
	Paused    bool   `json:"paused"`
}

// QueuesOverview is the response of GET /admin/queues.
type QueuesOverview struct {
	Queues        []QueueStats `json:"queues"`
	ActiveWorkers int          `json:"active_workers"`
}

// queueStatsFor gathers stats for the managed queues, feeding the
// posta_queue_archived_tasks gauge so dead-letter growth is alertable even
// before a dedicated dashboard exists (feeds R-12). Inspector-nil safe.
func (h *AdminHandler) queueStatsFor() []QueueStats {
	out := make([]QueueStats, 0, len(adminQueues))
	if h.queueInspector == nil {
		for _, name := range adminQueues {
			out = append(out, QueueStats{Queue: name})
		}
		return out
	}
	for _, name := range adminQueues {
		info, err := h.queueInspector.GetQueueInfo(name)
		if err != nil {
			// A queue that was never created in Redis simply reports zeros —
			// absence is not an error for the operator.
			out = append(out, QueueStats{Queue: name})
			continue
		}
		metrics.SetQueueArchivedTasks(name, info.Archived)
		out = append(out, queueStatsOf(info))
	}
	return out
}

// ListQueues returns per-queue stats for the send pipeline queues plus the
// live worker count (admin only).
func (h *AdminHandler) ListQueues(c *okapi.Context) error {
	if h.queueInspector == nil {
		return c.AbortServiceUnavailable("queue inspection is not available (no worker configured)")
	}

	out := QueuesOverview{Queues: h.queueStatsFor()}
	if h.inspector != nil {
		if servers, err := h.inspector.Servers(); err == nil {
			out.ActiveWorkers = len(servers)
		}
	}
	return ok(c, out)
}

func queueStatsOf(info *asynq.QueueInfo) QueueStats {
	return QueueStats{
		Queue:     info.Queue,
		Size:      info.Size,
		Pending:   info.Pending,
		Active:    info.Active,
		Scheduled: info.Scheduled,
		Retry:     info.Retry,
		Archived:  info.Archived,
		Processed: info.Processed,
		Failed:    info.Failed,
		LatencyMS: info.Latency.Milliseconds(),
		Paused:    info.Paused,
	}
}

// ListQueueTasksRequest is the request for GET /admin/queues/{queue}/tasks.
type ListQueueTasksRequest struct {
	Queue string `param:"queue"`
	State string `query:"state"`
	Page  int    `query:"page"`
	Size  int    `query:"size"`
}

// QueueTask is the wire shape of one asynq task.
type QueueTask struct {
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	Queue          string     `json:"queue"`
	State          string     `json:"state"`
	PayloadSnippet string     `json:"payload_snippet"`
	MaxRetry       int        `json:"max_retry"`
	Retried        int        `json:"retried"`
	LastErr        string     `json:"last_error,omitempty"`
	LastFailedAt   *time.Time `json:"last_failed_at,omitempty"`
	NextProcessAt  *time.Time `json:"next_process_at,omitempty"`
}

// QueueTaskPage is the response of GET /admin/queues/{queue}/tasks.
type QueueTaskPage struct {
	Queue string      `json:"queue"`
	State string      `json:"state"`
	Tasks []QueueTask `json:"tasks"`
	Page  int         `json:"page"`
	Size  int         `json:"size"`
	Total int         `json:"total"`
}

// ListQueueTasks lists the tasks of one queue in the requested state, paged
// (admin only). Total is the page-local count: asynq's list API is page-based
// and a full count would require walking every page.
func (h *AdminHandler) ListQueueTasks(c *okapi.Context, req *ListQueueTasksRequest) error {
	if h.queueInspector == nil {
		return c.AbortServiceUnavailable("queue inspection is not available (no worker configured)")
	}
	if !isManagedQueue(req.Queue) {
		return c.AbortNotFound("queue not found")
	}
	state := req.State
	if state == "" {
		state = "pending"
	}
	page, size := req.Page, req.Size
	if page < 0 {
		page = 0
	}
	if size <= 0 || size > 100 {
		size = 20
	}

	var (
		tasks []*asynq.TaskInfo
		err   error
	)
	switch state {
	case "pending":
		tasks, err = h.queueInspector.ListPendingTasks(req.Queue, page, size)
	case "active":
		tasks, err = h.queueInspector.ListActiveTasks(req.Queue, page, size)
	case "scheduled":
		tasks, err = h.queueInspector.ListScheduledTasks(req.Queue, page, size)
	case "retry":
		tasks, err = h.queueInspector.ListRetryTasks(req.Queue, page, size)
	case "archived":
		tasks, err = h.queueInspector.ListArchivedTasks(req.Queue, page, size)
	default:
		return c.AbortBadRequest("invalid state: use pending, active, scheduled, retry or archived")
	}
	if err != nil {
		// asynq reports a queue that has never received a task as ErrQueueNotFound.
		// That is the empty state, not a server failure: the overview above already
		// reports such a queue as zeros, and the dashboard opens this tab on a fresh
		// install before anything has been enqueued. Answer with an empty page.
		if errors.Is(err, asynq.ErrQueueNotFound) {
			tasks = nil
		} else {
			return c.AbortInternalServerError(fmt.Sprintf("failed to list %s tasks: %v", state, err))
		}
	}

	out := make([]QueueTask, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, queueTaskOf(t))
	}
	return ok(c, QueueTaskPage{
		Queue: req.Queue, State: state, Tasks: out,
		Page: page, Size: size, Total: len(out),
	})
}

func queueTaskOf(t *asynq.TaskInfo) QueueTask {
	qt := QueueTask{
		ID:             t.ID,
		Type:           t.Type,
		Queue:          t.Queue,
		State:          t.State.String(),
		PayloadSnippet: payloadSnippet(t.Payload, 120),
		MaxRetry:       t.MaxRetry,
		Retried:        t.Retried,
		LastErr:        t.LastErr,
	}
	if !t.LastFailedAt.IsZero() {
		at := t.LastFailedAt
		qt.LastFailedAt = &at
	}
	if t.NextProcessAt.After(time.Unix(0, 0)) {
		next := t.NextProcessAt
		qt.NextProcessAt = &next
	}
	return qt
}

// payloadSnippet truncates the task payload for display — the payload is a
// small JSON object ({"email_id":N}), but a malformed or hostile payload must
// not blow up the admin response.
func payloadSnippet(payload []byte, max int) string {
	if len(payload) == 0 {
		return ""
	}
	if len(payload) > max {
		return string(payload[:max]) + "…"
	}
	return string(payload)
}

// QueueTaskActionRequest names one task of one queue for cancel/run/archive.
type QueueTaskActionRequest struct {
	Queue  string `param:"queue"`
	TaskID string `param:"task_id"`
}

// CancelQueueTask removes a pending/scheduled/retry task from the queue so it
// never runs (admin only). Archived tasks are deleted by the same verb: in
// ReForwarder's REST style cancel means "make this task go away", and for a
// dead-lettered task the only way it goes away is deletion.
func (h *AdminHandler) CancelQueueTask(c *okapi.Context, req *QueueTaskActionRequest) error {
	return h.queueTaskAction(c, req, "cancel", func(q, id string) error {
		return h.queueInspector.DeleteTask(q, id)
	}, "queue.task.cancelled", "cancelled queue task")
}

// RunQueueTask schedules an archived or retrying task for immediate processing
// (admin only) — the requeue path for dead-lettered sends.
func (h *AdminHandler) RunQueueTask(c *okapi.Context, req *QueueTaskActionRequest) error {
	return h.queueTaskAction(c, req, "run", func(q, id string) error {
		return h.queueInspector.RunTask(q, id)
	}, "queue.task.run", "scheduled queue task for immediate run")
}

// ArchiveQueueTask parks a pending/scheduled/retry task in the archived set
// (the dead-letter view) without deleting it (admin only).
func (h *AdminHandler) ArchiveQueueTask(c *okapi.Context, req *QueueTaskActionRequest) error {
	return h.queueTaskAction(c, req, "archive", func(q, id string) error {
		return h.queueInspector.ArchiveTask(q, id)
	}, "queue.task.archived", "moved queue task to the archive")
}

func (h *AdminHandler) queueTaskAction(c *okapi.Context, req *QueueTaskActionRequest, verb string, act func(queue, id string) error, event, message string) error {
	if h.queueInspector == nil {
		return c.AbortServiceUnavailable("queue inspection is not available (no worker configured)")
	}
	if !isManagedQueue(req.Queue) {
		return c.AbortNotFound("queue not found")
	}
	if err := act(req.Queue, req.TaskID); err != nil {
		if errors.Is(err, asynq.ErrTaskNotFound) {
			return c.AbortNotFound("task not found")
		}
		if errors.Is(err, asynq.ErrQueueNotFound) {
			return c.AbortNotFound("queue not found")
		}
		return c.AbortConflict(fmt.Sprintf("cannot %s task: %v", verb, err))
	}
	if h.audit != nil {
		h.audit.Log(uint(c.GetInt("user_id")), c.GetString("email"), c.RealIP(),
			event, fmt.Sprintf("%s %s: %s", message, req.Queue, req.TaskID), nil)
	}
	return ok(c, okapi.M{"message": message, "queue": req.Queue, "task_id": req.TaskID})
}

// ---------- SF-52: REST destroy ----------

// DestroyQueueTask deletes a pending/scheduled/retrying/archived task outright
// (admin only) — the REST-shaped destroy verb beside POST /cancel. It is the
// same asynq.DeleteTask the cancel endpoint ends in, but it carries its own
// audit event (admin.queue.task_destroyed) so a deliberate destroy is
// distinguishable in the trail from a cancel. Active tasks cannot be deleted;
// asynq answers that with an error, surfaced as 409.
//
// PATCH of a task's options (next process time, max retry, queue) is
// deliberately not offered: asynq v0.26.0's Inspector has no API to modify a
// task's options in place — only UpdateTaskPayload (scheduled tasks only, and
// the payload here is the worker's internal {"email_id":N} reference, not an
// operator-tunable option). The supported adjustment path is cancel +
// re-enqueue (or run/archive on the existing task).
func (h *AdminHandler) DestroyQueueTask(c *okapi.Context, req *QueueTaskActionRequest) error {
	return h.queueTaskAction(c, req, "destroy", func(q, id string) error {
		return h.queueInspector.DeleteTask(q, id)
	}, "admin.queue.task_destroyed", "destroyed queue task")
}

// ---------- SF-54: global queue pause / resume ----------

// QueuePauseState is the per-queue outcome of a pause/resume operation.
type QueuePauseState struct {
	Queue   string `json:"queue"`
	Paused  bool   `json:"paused"`
	Changed bool   `json:"changed"`
}

// QueuePauseResult is the response of POST /admin/queue/pause and
// POST /admin/queue/resume.
type QueuePauseResult struct {
	Paused bool              `json:"paused"`
	Queues []QueuePauseState `json:"queues"`
}

// PauseQueues pauses task processing on every managed send pipeline queue
// (transactional, bulk, low) at once (admin only). A queue that is already
// paused is reported unchanged rather than failed — the operation is
// idempotent from the operator's point of view. Delivery workers keep their
// connections; asynq simply stops handing out tasks until ResumeQueues.
//
// Per-domain email suspension is explicitly out of scope (ADR-001): pausing is
// a deployment-wide delivery switch, not a tenancy control.
func (h *AdminHandler) PauseQueues(c *okapi.Context) error {
	return h.setQueuesPaused(c, true, "admin.queue.paused", "paused delivery queues")
}

// ResumeQueues unpauses every managed queue (admin only); the mirror of
// PauseQueues. An already-running queue is reported unchanged.
func (h *AdminHandler) ResumeQueues(c *okapi.Context) error {
	return h.setQueuesPaused(c, false, "admin.queue.resumed", "resumed delivery queues")
}

func (h *AdminHandler) setQueuesPaused(c *okapi.Context, pause bool, event, message string) error {
	if h.queueInspector == nil {
		return c.AbortServiceUnavailable("queue inspection is not available (no worker configured)")
	}

	states := make([]QueuePauseState, 0, len(adminQueues))
	var failures []string
	for _, name := range adminQueues {
		// Read the current state first: pause/resume of an already-there queue
		// is reported as unchanged instead of surfacing asynq's "already
		// paused/not paused" error. A queue Redis has never seen is simply not
		// paused.
		currentlyPaused := false
		if info, err := h.queueInspector.GetQueueInfo(name); err == nil {
			currentlyPaused = info.Paused
		}
		state := QueuePauseState{Queue: name, Paused: currentlyPaused}
		if currentlyPaused != pause {
			var err error
			if pause {
				err = h.queueInspector.PauseQueue(name)
			} else {
				err = h.queueInspector.UnpauseQueue(name)
			}
			if err != nil {
				// One queue refusing to flip must not leave the operator
				// believing the platform is paused: collect and report 409.
				failures = append(failures, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			state.Paused = pause
			state.Changed = true
		}
		states = append(states, state)
	}
	if len(failures) > 0 {
		return c.AbortConflict(fmt.Sprintf("failed to change queue state: %s", strings.Join(failures, "; ")))
	}

	if h.audit != nil {
		h.audit.Log(uint(c.GetInt("user_id")), c.GetString("email"), c.RealIP(), event, message, nil)
	}
	return ok(c, QueuePauseResult{Paused: pause, Queues: states})
}

// ---------- SF-59: operational actions ----------

// QueueOpsHandler serves the operational queue actions (SF-59,
// POST /admin/queue/actions/{action}). It sits beside AdminHandler rather than
// on it because the requeue_stuck action needs the send producer, which only
// exists when the deployment runs a worker — the handler is wired in
// routes.InitRoutes under that same condition and the routes are only
// registered when it is present.
type QueueOpsHandler struct {
	db             *gorm.DB
	producer       cronjobs.SendEnqueuer
	inspector      cronjobs.SendTaskInspector
	queueInspector QueueInspector
	audit          *audit.Logger
}

// NewQueueOpsHandler wires the operational actions. queueInspector backs the
// archive purge; producer and inspector back requeue_stuck.
func NewQueueOpsHandler(db *gorm.DB, producer cronjobs.SendEnqueuer, inspector cronjobs.SendTaskInspector, queueInspector QueueInspector) *QueueOpsHandler {
	return &QueueOpsHandler{
		db:             db,
		producer:       producer,
		inspector:      inspector,
		queueInspector: queueInspector,
	}
}

// SetAuditLogger wires the platform audit logger. Nil (default) disables
// auditing; the endpoints still work.
func (h *QueueOpsHandler) SetAuditLogger(a *audit.Logger) {
	h.audit = a
}

// QueueActionRequest addresses one operational action. For
// purge_archived_before, body.before is the cutoff (RFC3339 or YYYY-MM-DD):
// dead-lettered tasks that failed (or were parked) at or before it are
// deleted. An absent cutoff means now — i.e. the whole dead-letter set.
type QueueActionRequest struct {
	Action string `param:"action"`
	Body   struct {
		Before string `json:"before"`
	} `json:"body"`
}

// QueueActionResponse reports what one operational action did.
type QueueActionResponse struct {
	Action   string   `json:"action"`
	Requeued int      `json:"requeued"`
	Purged   int      `json:"purged"`
	Queues   []string `json:"queues"`
}

// queuePurgeWalkCap bounds one purge sweep: 50 pages of 100 archived tasks per
// queue. A backlog larger than that simply needs another sweep.
const queuePurgeWalkCap = 50

// queuePurgePageSize is the asynq list page size used while walking the
// archived set.
const queuePurgePageSize = 100

// RunAction executes one operational action (admin only, audited):
//
//	requeue_stuck          — runs the queue-reconcile sweep (the same
//	                       guard-railed logic the cron tick runs, see
//	                       cron/jobs.QueueReconcileJob) to re-enqueue emails
//	                       stuck in queued whose asynq task was lost.
//	purge_archived_before  — deletes dead-lettered (archived) tasks that
//	                       reached the archive at or before body.before.
//	                       asynq v0.26.0 has no ArchiveCleanup-style API, so
//	                       the sweep lists the archived set per queue and
//	                       deletes matching tasks individually.
func (h *QueueOpsHandler) RunAction(c *okapi.Context, req *QueueActionRequest) error {
	switch req.Action {
	case "requeue_stuck":
		return h.runRequeueStuck(c)
	case "purge_archived_before":
		return h.runPurgeArchived(c, req)
	default:
		return c.AbortBadRequest("unknown action: use requeue_stuck or purge_archived_before")
	}
}

func (h *QueueOpsHandler) runRequeueStuck(c *okapi.Context) error {
	if h.db == nil || h.producer == nil || h.inspector == nil {
		return c.AbortServiceUnavailable("queue actions are not available (no worker configured)")
	}
	job := cronjobs.NewQueueReconcileJob(h.db, h.producer, h.inspector)
	requeued, err := job.ReconcileOnce(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError(fmt.Sprintf("requeue sweep failed: %v", err))
	}
	h.auditAction(c, "requeue_stuck", requeued, 0)
	return ok(c, QueueActionResponse{
		Action:   "requeue_stuck",
		Requeued: requeued,
		Queues:   []string{queues.Transactional},
	})
}

func (h *QueueOpsHandler) runPurgeArchived(c *okapi.Context, req *QueueActionRequest) error {
	if h.queueInspector == nil {
		return c.AbortServiceUnavailable("queue actions are not available (no worker configured)")
	}
	cutoff, okTime := parseQueueActionCutoff(req.Body.Before)
	if !okTime {
		return c.AbortBadRequest("invalid before: expected RFC3339 or YYYY-MM-DD")
	}

	purged := 0
	for _, name := range adminQueues {
		for page := 0; page < queuePurgeWalkCap; page++ {
			tasks, err := h.queueInspector.ListArchivedTasks(name, page, queuePurgePageSize)
			if err != nil {
				if errors.Is(err, asynq.ErrQueueNotFound) {
					break // the queue has no archived set at all
				}
				return c.AbortInternalServerError(fmt.Sprintf("failed to list archived tasks of %s: %v", name, err))
			}
			for _, t := range tasks {
				// A task with a zero last-failed timestamp (manually archived
				// without a failure) counts as older than any cutoff — it has
				// no newer timestamp to offer.
				if t.LastFailedAt.After(cutoff) {
					continue
				}
				if err := h.queueInspector.DeleteTask(name, t.ID); err != nil {
					if errors.Is(err, asynq.ErrTaskNotFound) {
						continue // gone since the listing: not purged by us
					}
					return c.AbortConflict(fmt.Sprintf("failed to delete archived task %s of %s: %v", t.ID, name, err))
				}
				purged++
			}
			if len(tasks) < queuePurgePageSize {
				break
			}
		}
	}

	h.auditAction(c, "purge_archived_before", 0, purged)
	return ok(c, QueueActionResponse{
		Action: "purge_archived_before",
		Purged: purged,
		Queues: append([]string(nil), adminQueues...),
	})
}

// parseQueueActionCutoff accepts RFC3339 or a date-only value (whole day
// included); an empty value means now.
func parseQueueActionCutoff(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now(), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.Add(24 * time.Hour), true
	}
	return time.Time{}, false
}

func (h *QueueOpsHandler) auditAction(c *okapi.Context, action string, requeued, purged int) {
	if h.audit == nil {
		return
	}
	h.audit.Log(uint(c.GetInt("user_id")), c.GetString("email"), c.RealIP(),
		"admin.queue.action_run",
		fmt.Sprintf("ran queue action %s (requeued=%d, purged=%d)", action, requeued, purged),
		map[string]any{"action": action, "requeued": requeued, "purged": purged})
}
