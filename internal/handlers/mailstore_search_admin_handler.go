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

package handlers

import (
	"fmt"
	"time"

	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/storage/migration"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// MailstoreSearchAdminHandler serves the mailstore full-text-search
// maintenance endpoint (OB-3 wave W3, ADR-003 decision 3): POST
// /admin/mailstore/search/reindex, admin-gated via the /admin route group.
// The tsvector column regenerates itself on every write — this is purely the
// operator recovery path for schema/index drift, not a routine task.
type MailstoreSearchAdminHandler struct {
	db    *gorm.DB
	audit *audit.Logger
}

// NewMailstoreSearchAdminHandler wires the handler onto the primary Postgres
// handle (the same database the mailstore serves from).
func NewMailstoreSearchAdminHandler(db *gorm.DB) *MailstoreSearchAdminHandler {
	return &MailstoreSearchAdminHandler{db: db}
}

// SetAuditLogger wires the platform audit logger. Nil (default) disables
// auditing; the endpoint still works.
func (h *MailstoreSearchAdminHandler) SetAuditLogger(a *audit.Logger) {
	h.audit = a
}

// ReindexMailstoreSearchResponse reports what one rebuild did: reindexed is
// the number of mail_messages rows whose search_tsv the rebuild regenerated
// (the table's row count at rebuild time), duration_ms the wall-clock cost.
type ReindexMailstoreSearchResponse struct {
	Reindexed  int64 `json:"reindexed"`
	DurationMs int64 `json:"duration_ms"`
}

// Reindex rebuilds the mail_messages.search_tsv generated column and its GIN
// index (admin only, audited). It is idempotent and converges any prior
// schema state; see migration.RebuildMailstoreSearch for the exact SQL and
// its locking note.
func (h *MailstoreSearchAdminHandler) Reindex(c *okapi.Context) error {
	if h.db == nil {
		return c.AbortServiceUnavailable("mailstore search maintenance is not available")
	}

	start := time.Now()
	if err := migration.RebuildMailstoreSearch(h.db); err != nil {
		return c.AbortInternalServerError(fmt.Sprintf("rebuild mailstore search index: %v", err))
	}
	var reindexed int64
	if err := h.db.Raw(`SELECT count(*) FROM mail_messages`).Scan(&reindexed).Error; err != nil {
		return c.AbortInternalServerError(fmt.Sprintf("count mail_messages after reindex: %v", err))
	}
	elapsed := time.Since(start).Milliseconds()

	if h.audit != nil {
		h.audit.Log(uint(c.GetInt("user_id")), c.GetString("email"), c.RealIP(),
			"admin.mailstore.search_reindexed", "rebuilt the mailstore full-text search index",
			map[string]any{"reindexed": reindexed, "duration_ms": elapsed})
	}
	return ok(c, ReindexMailstoreSearchResponse{Reindexed: reindexed, DurationMs: elapsed})
}
