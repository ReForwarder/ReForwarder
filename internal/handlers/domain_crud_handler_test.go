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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// SF-60 tests: the dedicated domain CRUD battery the verification audit
// flagged as missing (V-EF: "domain CRUD has no dedicated tests"). The
// surface runs through the real okapi stack (JWT + the workspace binding
// middleware, the domain_health_handler_test.go arrangement) against the mail
// fake-driver harness extended with the domains table — the same
// select/insert/count/delete machinery the other surfaces ride. The CRUD
// scope is the WORKSPACE scope (repositories.ApplyScope: workspace_id = ?),
// so the isolation assertions pin the workspace boundary: a domain of one
// workspace reads as 404 exactly like a missing one from another workspace.
// The re-word per the verification document §6: the reference platform's
// alias/catch-all/relay Domain columns were delivered by the OB-2 route model
// (superseded by design) — what this feature's CRUD must still pin is the
// create/list/get/delete round trip, the workspace isolation, the edit-role
// gate and the verification-token issuance.

type dcHarness struct {
	o     *okapi.TestServer
	h     *DomainHandler
	alice *mhUser
	bob   *mhUser
}

func newDCHarness(t *testing.T) *dcHarness {
	t.Helper()
	f := newMHFixture(t)
	o := okapi.NewTestServer(t)
	h := NewDomainHandler(repositories.NewDomainRepository(f.db))
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: mhTestSecret}), nil, nil, nil),
		dmrWorkspaceBinding,
	)
	g.Post("/domains", okapi.H(h.Create))
	g.Get("/domains", okapi.H(h.List))
	g.Get("/domains/{id:int}", okapi.H(h.Get))
	g.Delete("/domains/{id:int}", okapi.H(h.Delete))
	hh := &dcHarness{o: o, h: h}
	hh.alice = dcSeedUser(t, mhAliceID, "alice")
	hh.bob = dcSeedUser(t, mhBobID, "bob")
	return hh
}

// dcSeedUser mints a session token for one user (the harness's workspace
// binding comes from the per-request X-Test-Workspace header, exactly like
// the dmarc/health tests).
func dcSeedUser(t *testing.T, userID uint, name string) *mhUser {
	t.Helper()
	tok, err := okapi.GenerateJwtToken([]byte(mhTestSecret), map[string]any{
		"sub":   userID,
		"email": name + "@mail.example",
		"role":  "user",
		"aud":   "posta",
		"jti":   fmt.Sprintf("dc-%d-%d", userID, time.Now().UnixNano()),
	}, time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return &mhUser{id: userID, token: tok}
}

func (h *dcHarness) do(t *testing.T, method, path, token, workspace, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if workspace != "" {
		req.Header.Set("X-Test-Workspace", workspace)
	}
	if role != "" {
		req.Header.Set("X-Test-Workspace-Role", role)
	}
	rec := httptest.NewRecorder()
	h.o.ServeHTTP(rec, req)
	return rec
}

// TestDomainAdmin_CRUDAndOwnership — the create/list/get/delete round trip,
// the workspace isolation, the edit-role gate, and the verification-token
// issuance.
func TestDomainAdmin_CRUDAndOwnership(t *testing.T) {
	hh := newDCHarness(t)

	// Create: the domain bound to alice's workspace with a verification
	// token and the DNS records to publish.
	rec := hh.do(t, "POST", "/workspaces/current/domains", hh.alice.token, "3", "", `{"domain": "alice-mail.example.test"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			ID                uint           `json:"id"`
			UserID            uint           `json:"user_id"`
			WorkspaceID       *uint          `json:"workspace_id"`
			Domain            string         `json:"domain"`
			VerificationToken string         `json:"verification_token"`
			DNSRecords        map[string]any `json:"dns_records"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Data.Domain != "alice-mail.example.test" || created.Data.UserID != mhAliceID {
		t.Fatalf("created = %+v, want alice's domain", created.Data)
	}
	if created.Data.WorkspaceID == nil || *created.Data.WorkspaceID != 3 {
		t.Fatalf("created workspace = %v, want 3", created.Data.WorkspaceID)
	}
	if created.Data.VerificationToken == "" {
		t.Fatal("created domain must carry a verification token")
	}
	if len(created.Data.DNSRecords) == 0 {
		t.Fatal("created domain must carry the DNS records to publish")
	}

	// List: exactly the created domain, newest-first pageable envelope.
	rec = hh.do(t, "GET", "/workspaces/current/domains", hh.alice.token, "3", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Data []struct {
			ID     uint   `json:"id"`
			Domain string `json:"domain"`
		} `json:"data"`
		Pageable struct {
			TotalElements int64 `json:"total_elements"`
			Empty         bool  `json:"empty"`
		} `json:"pageable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].Domain != "alice-mail.example.test" || list.Pageable.TotalElements != 1 {
		t.Fatalf("list = %+v pageable %+v, want exactly the created domain", list.Data, list.Pageable)
	}

	// Workspace isolation: bob's workspace sees nothing, and a foreign
	// domain reads as 404 exactly like a missing one.
	rec = hh.do(t, "GET", "/workspaces/current/domains", hh.bob.token, "4", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("bob list = %d", rec.Code)
	}
	var bobList struct {
		Pageable struct {
			TotalElements int64 `json:"total_elements"`
		} `json:"pageable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bobList); err != nil {
		t.Fatalf("decode bob list: %v", err)
	}
	if bobList.Pageable.TotalElements != 0 {
		t.Fatalf("bob's domains total = %d, want 0 (workspace isolation)", bobList.Pageable.TotalElements)
	}
	rec = hh.do(t, "GET", fmt.Sprintf("/workspaces/current/domains/%d", created.Data.ID), hh.bob.token, "4", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign get = %d, want 404", rec.Code)
	}
	rec = hh.do(t, "DELETE", fmt.Sprintf("/workspaces/current/domains/%d", created.Data.ID), hh.bob.token, "4", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign delete = %d, want 404", rec.Code)
	}

	// The edit-role gate: a viewer cannot create (requireEdit).
	rec = hh.do(t, "POST", "/workspaces/current/domains", hh.alice.token, "3", "viewer", `{"domain": "nope.example.test"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer create = %d, want 403", rec.Code)
	}

	// Get (owner): the same record round-trips.
	rec = hh.do(t, "GET", fmt.Sprintf("/workspaces/current/domains/%d", created.Data.ID), hh.alice.token, "3", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Data struct {
			ID     uint   `json:"id"`
			Domain string `json:"domain"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.Data.ID != created.Data.ID || got.Data.Domain != "alice-mail.example.test" {
		t.Fatalf("got = %+v, want the created domain", got.Data)
	}

	// Delete: once 204, then the record is gone (404 on get, 404 on the
	// second delete, the list empties).
	rec = hh.do(t, "DELETE", fmt.Sprintf("/workspaces/current/domains/%d", created.Data.ID), hh.alice.token, "3", "", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	rec = hh.do(t, "GET", fmt.Sprintf("/workspaces/current/domains/%d", created.Data.ID), hh.alice.token, "3", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", rec.Code)
	}
	rec = hh.do(t, "DELETE", fmt.Sprintf("/workspaces/current/domains/%d", created.Data.ID), hh.alice.token, "3", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}
	rec = hh.do(t, "GET", "/workspaces/current/domains", hh.alice.token, "3", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list after delete = %d", rec.Code)
	}
	var after struct {
		Pageable struct {
			TotalElements int64 `json:"total_elements"`
		} `json:"pageable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode list after delete: %v", err)
	}
	if after.Pageable.TotalElements != 0 {
		t.Fatalf("list after delete total = %d, want 0", after.Pageable.TotalElements)
	}
}

// TestDomainAdmin_ListIsWorkspaceScopedNotUserScoped — two users of the SAME
// workspace both see the workspace's domain (the scope is the workspace, per
// repositories.ApplyScope), while the third workspace stays blind. This pins
// the re-worded scope semantics: the CRUD's tenancy boundary is the workspace.
func TestDomainAdmin_ListIsWorkspaceScopedNotUserScoped(t *testing.T) {
	hh := newDCHarness(t)

	// Alice creates in workspace 3.
	rec := hh.do(t, "POST", "/workspaces/current/domains", hh.alice.token, "3", "", `{"domain": "shared.example.test"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	// Bob (a different user id) bound to the SAME workspace sees it.
	rec = hh.do(t, "GET", "/workspaces/current/domains", hh.bob.token, "3", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("same-workspace list = %d", rec.Code)
	}
	var list struct {
		Data []struct {
			Domain string `json:"domain"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode same-workspace list: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].Domain != "shared.example.test" {
		t.Fatalf("same-workspace list = %+v, want the shared domain", list.Data)
	}

	// A workspace without the domain stays blind.
	rec = hh.do(t, "GET", "/workspaces/current/domains", hh.bob.token, "4", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("other-workspace list = %d", rec.Code)
	}
	var blind struct {
		Data []struct {
			Domain string `json:"domain"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &blind); err != nil {
		t.Fatalf("decode other-workspace list: %v", err)
	}
	if len(blind.Data) != 0 {
		t.Fatalf("other-workspace list = %+v, want empty (workspace isolation)", blind.Data)
	}
}
