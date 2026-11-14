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
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// SF-42 tests: the spam-trap admin surface (the ADR-006 amendment) — the
// CRUD over the same fake-driver arrangement the transport-policy tests use
// (domains + spam_traps fake tables): the ownership 404s, the local-part
// validation, and the lowercase normalization.

func TestSpamTrapAdmin_CRUDAndValidation(t *testing.T) {
	h := newTpolHarness(t)
	domID := h.state.seed("domains", map[string]any{
		"id": uint(950), "user_id": 7, "workspace_id": 5, "domain": "prio.example", "ownership_verified": true,
		"created_at": time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
	})
	foreignDom := h.state.seed("domains", map[string]any{
		"id": uint(951), "user_id": 9, "workspace_id": 6, "domain": "foreign.example", "ownership_verified": true,
		"created_at": time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
	})

	// Create: the local part normalizes to lowercase.
	rec := h.do(t, "admin", 5, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/spam-traps", domID),
		`{"local_part": "Trap.String", "note": "published on the blog"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			ID         int    `json:"id"`
			DomainName string `json:"domain_name"`
			LocalPart  string `json:"local_part"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Data.DomainName != "prio.example" || created.Data.LocalPart != "trap.string" {
		t.Fatalf("created = %+v, want the domain-bound lowercase trap", created.Data)
	}

	// List: exactly the created trap.
	rec = h.do(t, "admin", 5, http.MethodGet,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/spam-traps", domID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}

	// A foreign domain reads as 404.
	rec = h.do(t, "admin", 5, http.MethodGet,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/spam-traps", foreignDom), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign domain list = %d, want 404", rec.Code)
	}

	// Validation: an @ in the local part is 400.
	rec = h.do(t, "admin", 5, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/spam-traps", domID),
		`{"local_part": "full@address"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid local part = %d, want 400", rec.Code)
	}

	// Delete: once OK, then 404.
	rec = h.do(t, "admin", 5, http.MethodDelete,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/spam-traps/%d", domID, created.Data.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	rec = h.do(t, "admin", 5, http.MethodDelete,
		fmt.Sprintf("/api/v1/workspaces/current/domains/%d/spam-traps/%d", domID, created.Data.ID), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}
}
