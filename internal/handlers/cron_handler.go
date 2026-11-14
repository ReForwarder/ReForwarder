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
	"net/http"

	"github.com/goposta/posta/internal/cron"
	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// CronHandler exposes cron job status to admin users.
type CronHandler struct {
	manager *cron.Manager
	// history is the SF-99 run-history read seam; nil (unwired) makes the
	// history endpoint answer 503 while the pre-SF-99 status surface is
	// untouched.
	history *repositories.CronRunHistoryRepository
}

func NewCronHandler(manager *cron.Manager) *CronHandler {
	return &CronHandler{manager: manager}
}

// SetRunHistoryRepo attaches the run-history read repo (SF-99).
func (h *CronHandler) SetRunHistoryRepo(r *repositories.CronRunHistoryRepository) {
	h.history = r
}

func (h *CronHandler) List(c *okapi.Context) error {
	jobs := h.manager.Jobs()
	return ok(c, jobs)
}

// ─── SF-99: run history + manual trigger ─────────────────────────────────────

type CronRunHistoryRequest struct {
	JobName string `query:"job_name" doc:"Optional filter: one job's runs only"`
	Page    int    `query:"page" default:"0"`
	Size    int    `query:"size" default:"25"`
}

type CronRunHistoryResponse struct {
	ID         int    `json:"id"`
	JobName    string `json:"job_name"`
	Trigger    string `json:"trigger"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	Outcome    string `json:"outcome"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type CronJobTriggerRequest struct {
	Name string `param:"name"`
}

// ListHistory returns one page of cron run history, newest first, optionally
// narrowed to one job (SF-99). Deployment-wide operational data — the admin
// group's middleware is the gate, and no tenant scoping applies (cron jobs
// are not tenant objects).
func (h *CronHandler) ListHistory(c *okapi.Context, req *CronRunHistoryRequest) error {
	if h.history == nil {
		return c.AbortServiceUnavailable("run history is not available on this deployment")
	}
	page, size, offset := normalizePageParams(req.Page, req.Size)
	entries, total, err := h.history.ListPagedOffset(req.JobName, offset, size)
	if err != nil {
		return c.AbortInternalServerError("failed to load run history")
	}
	out := make([]CronRunHistoryResponse, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		out = append(out, cronRunResponse(e))
	}
	return paginated(c, out, total, page, size)
}

func cronRunResponse(e *models.CronRunHistory) CronRunHistoryResponse {
	return CronRunHistoryResponse{
		ID:         int(e.ID),
		JobName:    e.JobName,
		Trigger:    e.Trigger,
		StartedAt:  e.StartedAt.UTC().Format("2006-01-02T15:04:05Z"),
		FinishedAt: e.FinishedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Outcome:    e.Outcome,
		Detail:     e.Detail,
		DurationMS: e.FinishedAt.Sub(e.StartedAt).Milliseconds(),
	}
}

// TriggerJob starts a MANUAL run of the named job on this node (SF-99). The
// run is asynchronous — the handler answers as soon as the run is claimed
// (202); the outcome lands in the run history and the job's status flips to
// Running until it completes. The admin group's middleware IS the
// scheduler-role gate: this tree's RBAC has admin and user roles only, and
// admin is the scheduler-capable role (a finer-grained scheduler role is
// SF-66's deferred registry territory).
func (h *CronHandler) TriggerJob(c *okapi.Context, req *CronJobTriggerRequest) error {
	if h.manager == nil {
		return c.AbortServiceUnavailable("the cron manager is not running on this deployment")
	}
	if req.Name == "" || len(req.Name) > 255 {
		return c.AbortBadRequest("job name is required")
	}
	found, alreadyRunning := h.manager.TriggerJob(req.Name)
	if !found {
		// Isolation: an unknown job name reads as 404 like a missing one.
		return c.AbortNotFound("job not found")
	}
	if alreadyRunning {
		return c.AbortConflict("job is already running")
	}
	return c.JSON(http.StatusAccepted, dto.Response[dto.MessageData]{
		Success: true,
		Data:    dto.MessageData{Message: "job triggered; its run lands in the history"},
	})
}
