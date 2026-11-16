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

package inbound

import (
	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// SF-49 (the archive-integrity resolution): the Authentication-Results
// header is stored on the verdict row (Wave P3-1) and injected at
// FORWARD-composition time only — the forward pipeline composes a fresh
// message, so no content-addressed stored blob is ever rewritten (rewriting
// the stored .eml would break dedup and falsify the archive). The forwarder
// reads the persisted header through this seam and merges it into the copy's
// headers with the established append-only semantics: an upstream MTA's
// existing A-R header is never touched.

// ARHeaderSource returns the persisted RFC 8601 Authentication-Results
// header value for one inbound message ("" when no verdict exists —
// verification is asynchronous). The forwarder's injection seam; nil
// disables the injection (the pre-SF-49-forward behavior byte-for-byte).
type ARHeaderSource interface {
	StoredARHeader(inboundID uint) string
}

// AuthResultHeaderSource is the production implementation over the verdict
// row (inbound_auth_results.auth_results_header). Fail-open: a read error
// returns "" — a missing header is the pre-injection behavior, never a
// blocked forward.
type AuthResultHeaderSource struct {
	db *gorm.DB
}

func NewAuthResultHeaderSource(db *gorm.DB) *AuthResultHeaderSource {
	return &AuthResultHeaderSource{db: db}
}

// StoredARHeader implements ARHeaderSource.
func (s *AuthResultHeaderSource) StoredARHeader(inboundID uint) string {
	if s == nil || s.db == nil {
		return ""
	}
	var row models.InboundAuthResult
	if err := s.db.Select("auth_results_header").Where("inbound_id = ?", inboundID).First(&row).Error; err != nil {
		return ""
	}
	return row.AuthResultsHeader
}

// withInjectedARHeader merges the persisted A-R header into the copy's
// headers. An upstream Authentication-Results header on the original is
// never overwritten (RFC 8601 headers are append-only and our verdict stays
// authoritative on the verdict row); the injected value is skipped when the
// upstream already provided one — the copy stays a faithful re-send either
// way, and the stored original is untouched either way.
func withInjectedARHeader(headers map[string]string, ar string) map[string]string {
	if ar == "" {
		return headers
	}
	if _, ok := headers["Authentication-Results"]; ok {
		return headers
	}
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	out["Authentication-Results"] = ar
	return out
}
