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
	"time"

	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/queues"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/okapi"
)

// QueueInspector is the narrow slice of *asynq.Inspector the queue
// administration API uses. It exists so the handlers below are testable with a
// stub: a real asynq.Inspector needs a live Redis, and the repository's CI has
// a database service but no Redis harness for handler tests.
type QueueInspector interface {
	GetQueueInfo(queue string) (*asynq.QueueInfo, error)
	ListPendingTasks(queue string, page, size int) ([]*asynq.TaskInfo, error)
	ListActiveTasks(queue string, page, size int) ([]*asynq.TaskInfo, error)
	ListScheduledTasks(queue string, page, size int) ([]*asynq.TaskInfo, error)
	ListRetryTasks(queue string, page, size int) ([]*asynq.TaskInfo, error)
	ListArchivedTasks(queue string, page, size int) ([]*asynq.TaskInfo, error)
	DeleteTask(queue, id string) error
	RunTask(queue, id string) error
	ArchiveTask(queue, id string) error
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
