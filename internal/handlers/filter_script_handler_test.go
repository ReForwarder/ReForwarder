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
	"strings"
	"testing"
)

// SF-14 tests: the per-mailbox filter-script management surface over the
// mail fake-driver harness (the webmail battery's arrangement): the CRUD
// round trip, the strict-grammar validation, the mailbox-ownership gate, and
// the user isolation.

func TestFilterScriptAdmin_CRUDAndValidation(t *testing.T) {
	h := newAGHarness(t)
	// Alice's seeded mailbox (alice@example.test) from the harness.
	mbID := int(h.alice.mb.ID)

	// Create: the conditions validated through the strict evaluator.
	rec := h.do(t, "POST", "/api/v1/mail/filter-scripts", h.alice.token,
		fmt.Sprintf(`{"mailbox": %d, "name": "finance", "priority": 10, "conditions": [{"field": "subject", "operator": "contains", "value": "invoice"}], "actions": [{"type": "fileinto", "target": "INBOX/Finance"}]}`, mbID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			UserID uint   `json:"user_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Data.Name != "finance" || created.Data.UserID != mhAliceID {
		t.Fatalf("created = %+v, want alice's finance script", created.Data)
	}

	// List: exactly the created script.
	rec = h.do(t, "GET", "/api/v1/mail/filter-scripts?mailbox="+fmt.Sprint(mbID), h.alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Data struct {
			Scripts []struct {
				Name string `json:"name"`
			} `json:"scripts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Data.Scripts) != 1 || list.Data.Scripts[0].Name != "finance" {
		t.Fatalf("list = %+v, want exactly the created script", list.Data)
	}

	// Validation: the strict grammar's unknown field is 400 before the table.
	rec = h.do(t, "POST", "/api/v1/mail/filter-scripts", h.alice.token,
		fmt.Sprintf(`{"mailbox": %d, "name": "bad", "conditions": [{"field": "not_a_fact", "operator": "equals", "value": "x"}], "actions": [{"type": "keep"}]}`, mbID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid conditions = %d, want 400", rec.Code)
	}

	// The mailbox-ownership gate: a foreign mailbox reads as 404.
	rec = h.do(t, "POST", "/api/v1/mail/filter-scripts", h.alice.token,
		fmt.Sprintf(`{"mailbox": %d, "name": "steal", "conditions": [], "actions": [{"type": "keep"}]}`, int(h.bob.mb.ID)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign mailbox create = %d, want 404", rec.Code)
	}

	// User isolation: bob's list is empty; bob's delete of alice's script
	// reads as 404 like a missing one.
	rec = h.do(t, "GET", "/api/v1/mail/filter-scripts?mailbox="+fmt.Sprint(int(h.bob.mb.ID)), h.bob.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("bob list = %d", rec.Code)
	}
	var bobList struct {
		Data struct {
			Scripts []struct {
				ID int `json:"id"`
			} `json:"scripts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bobList); err != nil {
		t.Fatalf("decode bob list: %v", err)
	}
	if len(bobList.Data.Scripts) != 0 {
		t.Fatalf("bob's scripts = %+v, want empty (user isolation)", bobList.Data.Scripts)
	}
	rec = h.do(t, "DELETE", fmt.Sprintf("/api/v1/mail/filter-scripts/%d", created.Data.ID), h.bob.token, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign delete = %d, want 404", rec.Code)
	}

	// Delete: once OK, then 404.
	rec = h.do(t, "DELETE", fmt.Sprintf("/api/v1/mail/filter-scripts/%d", created.Data.ID), h.alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	rec = h.do(t, "DELETE", fmt.Sprintf("/api/v1/mail/filter-scripts/%d", created.Data.ID), h.alice.token, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "finance") {
		t.Fatalf("the deleted script must be gone: %s", rec.Body.String())
	}
}
