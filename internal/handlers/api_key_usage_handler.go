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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"time"

	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// APIKeyUsageHandler serves the per-key usage analytics (R-18): the trailing
// daily request/send series and window totals for one API key.
type APIKeyUsageHandler struct {
	repo    *repositories.APIKeyUsageRepository
	keyRepo *repositories.APIKeyRepository
}

func NewAPIKeyUsageHandler(repo *repositories.APIKeyUsageRepository, keyRepo *repositories.APIKeyRepository) *APIKeyUsageHandler {
	return &APIKeyUsageHandler{repo: repo, keyRepo: keyRepo}
}

// GetAPIKeyUsageRequest is the usage endpoint's parameter shape.
type GetAPIKeyUsageRequest struct {
	ID   int `param:"id" doc:"API key ID"`
	Days int `query:"days" doc:"Trailing window in days (1-90, default 30)"`
}

// APIKeyUsageRow is one day of a key's usage. Days without recorded traffic
// appear with zero counts so the client can draw a gap-free series.
type APIKeyUsageRow struct {
	Day      string `json:"day" doc:"UTC day (YYYY-MM-DD)"`
	Requests int64  `json:"requests"`
	Sends    int64  `json:"sends"`
}

func (h *APIKeyUsageHandler) Get(c *okapi.Context, req *GetAPIKeyUsageRequest) error {
	key, err := h.keyRepo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, key.UserID, key.WorkspaceID) {
		return c.AbortNotFound("API key not found")
	}
	if h.repo == nil {
		return c.AbortInternalServerError("usage analytics unavailable")
	}

	days := req.Days
	if days <= 0 {
		days = 30
	}
	if days > 90 {
		days = 90
	}

	rows, err := h.repo.List(uint(req.ID), days)
	if err != nil {
		return c.AbortInternalServerError("failed to load usage")
	}
	since := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	totals, err := h.repo.Totals(uint(req.ID), since)
	if err != nil {
		return c.AbortInternalServerError("failed to load usage totals")
	}

	out := make([]APIKeyUsageRow, len(rows))
	for i, row := range rows {
		out[i] = APIKeyUsageRow{Day: row.Day.Format("2006-01-02"), Requests: row.Requests, Sends: row.Sends}
	}

	return ok(c, okapi.M{
		"days":          out,
		"days_trailing": days,
		"totals":        totals,
	})
}
