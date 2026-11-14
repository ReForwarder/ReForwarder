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
	"github.com/goposta/posta/internal/models"
	"github.com/jkaninda/okapi"
)

// SF-65 tests: the mail-account alias/group CRUD over the mail fake-driver
// harness (mail_fake_driver_test.go), riding the same jwtOnly gating as the
// production /api/v1/mail surface. The headline assertions mirror the mail
// battery's posture: strict per-session-user scoping (a foreign mailbox,
// alias or group reads as 404 like a missing one), the global one-address-
// one-shape rule, and the self-shadow/self-loop refusals.

// agHarness wires the SF-65 alias/group endpoints onto the okapi test server
// under the production gating (jwtOnly; personal scope) over the mail fake
// fixture.
type agHarness struct {
	f     *mhFixture
	o     *okapi.TestServer
	alice *mhUser
	bob   *mhUser
}

func newAGHarness(t *testing.T) *agHarness {
	t.Helper()
	f := newMHFixture(t)
	o := okapi.NewTestServer(t)
	jwtAuth := middlewares.JWTAuth(&config.Config{JWTSecret: mhTestSecret})
	g := o.Group("/api/v1/mail", jwtAuth.Middleware)
	g.Get("/aliases", okapi.H(f.h.ListAliases))
	g.Post("/aliases", okapi.H(f.h.CreateAlias))
	g.Delete("/aliases/{id:int}", okapi.H(f.h.DeleteAlias))
	g.Get("/groups", okapi.H(f.h.ListGroups))
	g.Post("/groups", okapi.H(f.h.CreateGroup))
	g.Patch("/groups/{id:int}", okapi.H(f.h.PatchGroup))
	g.Delete("/groups/{id:int}", okapi.H(f.h.DeleteGroup))
	h := &agHarness{f: f, o: o}
	h.alice = h.seedUser(t, mhAliceID, "alice@example.test")
	h.bob = h.seedUser(t, mhBobID, "bob@example.test")
	return h
}

// seedUser provisions a mailbox (INBOX included) and mints a session token.
func (h *agHarness) seedUser(t *testing.T, userID uint, name string) *mhUser {
	t.Helper()
	mb, err := h.f.svc.CreateMailbox(userID, nil, name, 0)
	if err != nil {
		t.Fatalf("seed mailbox %s: %v", name, err)
	}
	tok, err := okapi.GenerateJwtToken([]byte(mhTestSecret), map[string]any{
		"sub":   userID,
		"email": name + "@mail.example",
		"role":  "user",
		"aud":   "posta",
		"jti":   fmt.Sprintf("ag-%d-%d", userID, time.Now().UnixNano()),
	}, time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return &mhUser{id: userID, mb: mb, token: tok}
}

func (h *agHarness) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
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
	rec := httptest.NewRecorder()
	h.o.ServeHTTP(rec, req)
	return rec
}

// doData runs a request, asserts the status, and decodes the envelope. The
// success flag is asserted only for success statuses — the error envelope
// legitimately carries success=false.
func (h *agHarness) doData(t *testing.T, method, path, token, body string, wantStatus int) json.RawMessage {
	t.Helper()
	rec := h.do(t, method, path, token, body)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s → %d (want %d): %s", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	success, data := mhDecodeEnvelope(t, rec)
	if wantStatus < 400 && !success {
		t.Fatalf("%s %s answered success=false: %s", method, path, rec.Body.String())
	}
	return data
}

func agAliasID(t *testing.T, data json.RawMessage) int {
	t.Helper()
	var out struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode alias id: %v", err)
	}
	return out.ID
}

// TestMail_Alias_CreateListDelete — the alias CRUD round trip: create
// normalizes the address and names the target mailbox, the list shows only
// the session user's aliases, delete removes it, and deleting it again (or
// another user's alias) reads as 404 like a missing one.
func TestMail_Alias_CreateListDelete(t *testing.T) {
	h := newAGHarness(t)

	data := h.doData(t, "POST", "/api/v1/mail/aliases", h.alice.token,
		fmt.Sprintf(`{"mailbox": %d, "address": "Team@Example.test"}`, h.alice.mb.ID), http.StatusCreated)
	var alias struct {
		ID          int    `json:"id"`
		Address     string `json:"address"`
		MailboxID   int    `json:"mailbox_id"`
		MailboxName string `json:"mailbox_name"`
	}
	if err := json.Unmarshal(data, &alias); err != nil {
		t.Fatalf("decode alias: %v", err)
	}
	if alias.Address != "team@example.test" {
		t.Fatalf("alias address = %q, want the normalized lowercase", alias.Address)
	}
	if alias.MailboxID != int(h.alice.mb.ID) || alias.MailboxName != h.alice.mb.Name {
		t.Fatalf("alias target = %v/%q, want %d/%q", alias.MailboxID, alias.MailboxName, h.alice.mb.ID, h.alice.mb.Name)
	}

	// List: exactly alice's alias, with the target name.
	listData := h.doData(t, "GET", "/api/v1/mail/aliases", h.alice.token, "", http.StatusOK)
	var aliases []MailAliasResponse
	if err := json.Unmarshal(listData, &aliases); err != nil {
		t.Fatalf("decode aliases: %v", err)
	}
	if len(aliases) != 1 || aliases[0].Address != "team@example.test" || aliases[0].MailboxName != h.alice.mb.Name {
		t.Fatalf("aliases = %+v, want exactly the created one", aliases)
	}

	// Bob's list is empty (user scoping) and Bob's delete of alice's alias
	// reads as 404 like a missing one — never as forbidden.
	bobList := h.doData(t, "GET", "/api/v1/mail/aliases", h.bob.token, "", http.StatusOK)
	var bobAliases []MailAliasResponse
	if err := json.Unmarshal(bobList, &bobAliases); err != nil {
		t.Fatalf("decode bob aliases: %v", err)
	}
	if len(bobAliases) != 0 {
		t.Fatalf("bob's aliases = %+v, want empty (user scoping)", bobAliases)
	}
	h.doData(t, "DELETE", fmt.Sprintf("/api/v1/mail/aliases/%d", alias.ID), h.bob.token, "", http.StatusNotFound)

	// The owner's delete works; a second delete reads as 404.
	h.doData(t, "DELETE", fmt.Sprintf("/api/v1/mail/aliases/%d", alias.ID), h.alice.token, "", http.StatusOK)
	h.doData(t, "DELETE", fmt.Sprintf("/api/v1/mail/aliases/%d", alias.ID), h.alice.token, "", http.StatusNotFound)
}

// TestMail_Alias_ValidationAndIsolation — the create-time gates: the
// local@domain shape, the self-shadow refusal, the foreign-mailbox 404, and
// the GLOBAL one-address-one-shape rule across both shapes and both users.
func TestMail_Alias_ValidationAndIsolation(t *testing.T) {
	h := newAGHarness(t)

	// Malformed addresses: 400, nothing created.
	for _, bad := range []string{"no-at-sign", "@nowhere", "user@", "double@@at", "sp ace@example"} {
		h.doData(t, "POST", "/api/v1/mail/aliases", h.alice.token,
			fmt.Sprintf(`{"mailbox": %d, "address": %q}`, h.alice.mb.ID, bad), http.StatusBadRequest)
	}

	// An alias cannot shadow its own mailbox's address.
	h.doData(t, "POST", "/api/v1/mail/aliases", h.alice.token,
		fmt.Sprintf(`{"mailbox": %d, "address": "alice@example.test"}`, h.alice.mb.ID), http.StatusBadRequest)

	// A foreign mailbox reads as 404 (isolation never leaks existence).
	h.doData(t, "POST", "/api/v1/mail/aliases", h.alice.token,
		fmt.Sprintf(`{"mailbox": %d, "address": "steal@example.test"}`, h.bob.mb.ID), http.StatusNotFound)

	// One address, one shape, globally: alice's alias blocks bob's alias AND
	// bob's group at the same address.
	data := h.doData(t, "POST", "/api/v1/mail/aliases", h.alice.token,
		fmt.Sprintf(`{"mailbox": %d, "address": "shared@example.test"}`, h.alice.mb.ID), http.StatusCreated)
	aliasID := agAliasID(t, data)
	h.doData(t, "POST", "/api/v1/mail/aliases", h.bob.token,
		fmt.Sprintf(`{"mailbox": %d, "address": "shared@example.test"}`, h.bob.mb.ID), http.StatusConflict)
	h.doData(t, "POST", "/api/v1/mail/groups", h.bob.token,
		`{"address": "shared@example.test", "member_mailboxes": []}`, http.StatusConflict)

	// A group at another address makes that address unavailable to alice's
	// alias too (the uniqueness check is global, not per user).
	h.doData(t, "POST", "/api/v1/mail/groups", h.bob.token,
		`{"address": "crew@example.test", "member_mailboxes": []}`, http.StatusCreated)
	h.doData(t, "POST", "/api/v1/mail/aliases", h.alice.token,
		fmt.Sprintf(`{"mailbox": %d, "address": "crew@example.test"}`, h.alice.mb.ID), http.StatusConflict)

	// Cleanup for a clean state assertion.
	h.doData(t, "DELETE", fmt.Sprintf("/api/v1/mail/aliases/%d", aliasID), h.alice.token, "", http.StatusOK)
}

// TestMail_Group_CreatePatchDelete — the group CRUD: the anchor mailbox is
// provisioned under the group address, members dedupe and must be the
// session user's own, the self-anchor refusal holds on PATCH, and delete
// removes the memberships (the anchor mailbox stays).
func TestMail_Group_CreatePatchDelete(t *testing.T) {
	h := newAGHarness(t)

	// Second mailbox for alice (the second member).
	mbB, err := h.f.svc.CreateMailbox(mhAliceID, nil, "alice2@example.test", 0)
	if err != nil {
		t.Fatalf("seed second mailbox: %v", err)
	}

	// Create with a duplicated member: the set dedupes to [alice, alice2].
	data := h.doData(t, "POST", "/api/v1/mail/groups", h.alice.token,
		fmt.Sprintf(`{"address": "crew@example.test", "member_mailboxes": [%d, %d, %d]}`,
			h.alice.mb.ID, mbB.ID, h.alice.mb.ID), http.StatusCreated)
	var group struct {
		ID        int    `json:"id"`
		MailboxID int    `json:"mailbox_id"`
		Address   string `json:"address"`
		Members   []struct {
			MailboxID   int    `json:"mailbox_id"`
			MailboxName string `json:"mailbox_name"`
		} `json:"members"`
	}
	if err := json.Unmarshal(data, &group); err != nil {
		t.Fatalf("decode group: %v", err)
	}
	if group.Address != "crew@example.test" || len(group.Members) != 2 {
		t.Fatalf("group = %+v, want two deduplicated members", group)
	}
	// The anchor mailbox exists under the group address, owned by alice.
	anchor, err := h.f.svc.FindMailboxByName(mhAliceID, "crew@example.test")
	if err != nil {
		t.Fatalf("anchor mailbox not provisioned: %v", err)
	}
	if group.MailboxID != int(anchor.ID) {
		t.Fatalf("group anchor = %d, want the provisioned mailbox %d", group.MailboxID, anchor.ID)
	}

	// A foreign member mailbox reads as 404.
	h.doData(t, "POST", "/api/v1/mail/groups", h.alice.token,
		fmt.Sprintf(`{"address": "other@example.test", "member_mailboxes": [%d]}`, h.bob.mb.ID), http.StatusNotFound)

	// The self-anchor refusal on PATCH (the anchor exists only after create).
	h.doData(t, "PATCH", fmt.Sprintf("/api/v1/mail/groups/%d", group.ID), h.alice.token,
		fmt.Sprintf(`{"member_mailboxes": [%d]}`, anchor.ID), http.StatusBadRequest)

	// PATCH replaces the member set.
	patchData := h.doData(t, "PATCH", fmt.Sprintf("/api/v1/mail/groups/%d", group.ID), h.alice.token,
		fmt.Sprintf(`{"member_mailboxes": [%d]}`, mbB.ID), http.StatusOK)
	var patched MailGroupResponse
	if err := json.Unmarshal(patchData, &patched); err != nil {
		t.Fatalf("decode patched group: %v", err)
	}
	if len(patched.Members) != 1 || patched.Members[0].MailboxID != mbB.ID {
		t.Fatalf("patched members = %+v, want exactly alice2", patched.Members)
	}

	// Bob's list stays empty; bob's delete of alice's group reads as 404.
	bobList := h.doData(t, "GET", "/api/v1/mail/groups", h.bob.token, "", http.StatusOK)
	var bobGroups []MailGroupResponse
	if err := json.Unmarshal(bobList, &bobGroups); err != nil {
		t.Fatalf("decode bob groups: %v", err)
	}
	if len(bobGroups) != 0 {
		t.Fatalf("bob's groups = %+v, want empty (user scoping)", bobGroups)
	}
	h.doData(t, "DELETE", fmt.Sprintf("/api/v1/mail/groups/%d", group.ID), h.bob.token, "", http.StatusNotFound)

	// The owner's delete works and removes the memberships; the anchor
	// mailbox stays (an ordinary mailbox the owner may keep using).
	h.doData(t, "DELETE", fmt.Sprintf("/api/v1/mail/groups/%d", group.ID), h.alice.token, "", http.StatusOK)
	h.doData(t, "DELETE", fmt.Sprintf("/api/v1/mail/groups/%d", group.ID), h.alice.token, "", http.StatusNotFound)
	var members []models.MailGroupMember
	if err := h.f.db.Where("group_id = ?", group.ID).Find(&members).Error; err != nil {
		t.Fatalf("load leftover members: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("members after group delete = %d, want 0", len(members))
	}
	if _, err := h.f.svc.FindMailboxByName(mhAliceID, "crew@example.test"); err != nil {
		t.Fatalf("anchor mailbox must survive the group delete: %v", err)
	}
}
