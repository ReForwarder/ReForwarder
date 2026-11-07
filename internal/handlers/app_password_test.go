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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// SF-35/SF-67 app-password endpoint tests over the le harness (shared with
// login_enforcement_test.go): generate returns the plaintext exactly once and
// stores only a bcrypt digest; list and revoke never touch credential
// material; scoping is strictly per user.

type leAppCreated struct {
	AppPassword struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	} `json:"app_password"`
	Password string `json:"password"`
	Notice   string `json:"notice"`
}

func leCreateAppPassword(t *testing.T, h *leHarness, token, name string) leAppCreated {
	t.Helper()
	rec := h.do(t, http.MethodPost, "/users/me/app-passwords", token, fmt.Sprintf(`{"name":%q}`, name))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var env struct {
		Data leAppCreated `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return env.Data
}

func TestAppPassword_CreateReturnsPlaintextOnce(t *testing.T) {
	h := newLEHarness(t, false)
	h.state.seedUser(lePlainUserID, lePlainUserMail, "user", false, "")
	token := leToken(t, lePlainUserID, "user")

	created := leCreateAppPassword(t, h, token, "Thunderbird")

	if !strings.HasPrefix(created.Password, AppPasswordPrefix) {
		t.Fatalf("plaintext %q must carry the %q prefix", created.Password, AppPasswordPrefix)
	}
	if len(created.Password) != len(AppPasswordPrefix)+64 {
		t.Fatalf("plaintext length = %d, want prefix + 64 hex chars", len(created.Password))
	}
	if created.Notice == "" {
		t.Fatal("the one-time-display notice must be present")
	}

	// The stored form is a bcrypt digest of the plaintext, never the plaintext.
	h.state.mu.Lock()
	var stored []string
	for _, p := range h.state.appPws {
		stored = append(stored, p.pwHash)
	}
	h.state.mu.Unlock()
	if len(stored) != 1 {
		t.Fatalf("stored rows = %d, want 1", len(stored))
	}
	if strings.Contains(stored[0], created.Password) || stored[0] == created.Password {
		t.Fatal("the plaintext must not be stored")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored[0]), []byte(created.Password)); err != nil {
		t.Fatalf("stored digest does not verify the returned plaintext: %v", err)
	}

	// VerifyAppPassword (the ADR-004 seam) accepts the plaintext.
	if !VerifyAppPassword(stored, created.Password) {
		t.Fatal("VerifyAppPassword must accept the generated password")
	}
	if VerifyAppPassword(stored, leUserPassword) {
		t.Fatal("VerifyAppPassword must reject the account password")
	}
	if VerifyAppPassword(stored, "") {
		t.Fatal("VerifyAppPassword must reject an empty secret")
	}
}

func TestAppPassword_ListHasNoCredentialMaterial(t *testing.T) {
	h := newLEHarness(t, false)
	h.state.seedUser(lePlainUserID, lePlainUserMail, "user", false, "")
	token := leToken(t, lePlainUserID, "user")

	created := leCreateAppPassword(t, h, token, "IMAP client")

	rec := h.do(t, http.MethodGet, "/users/me/app-passwords", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, created.Password) {
		t.Fatal("list must never contain the plaintext")
	}
	if strings.Contains(body, "password_hash") || strings.Contains(body, "$2a$") || strings.Contains(body, "$2b$") {
		t.Fatal("list must never contain a digest")
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(env.Data) != 1 {
		t.Fatalf("list rows = %d, want 1", len(env.Data))
	}
	if env.Data[0]["name"] != "IMAP client" {
		t.Fatalf("row name = %v", env.Data[0]["name"])
	}
}

func TestAppPassword_RevokeLifecycle(t *testing.T) {
	h := newLEHarness(t, false)
	h.state.seedUser(lePlainUserID, lePlainUserMail, "user", false, "")
	token := leToken(t, lePlainUserID, "user")

	created := leCreateAppPassword(t, h, token, "Phone")

	// Revoke works once.
	rec := h.do(t, http.MethodDelete, fmt.Sprintf("/users/me/app-passwords/%d", created.AppPassword.ID), token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d (body %s)", rec.Code, rec.Body.String())
	}

	// A revoked password no longer verifies (the ADR-004 seam reads only
	// active rows).
	active, err := h.users.AppPasswordActiveHashes(lePlainUserID)
	if err != nil {
		t.Fatalf("load active hashes: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("revoked password still active: %d hashes", len(active))
	}

	// Revoking again reads as a conflict.
	rec = h.do(t, http.MethodDelete, fmt.Sprintf("/users/me/app-passwords/%d", created.AppPassword.ID), token, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("second revoke status = %d, want 400", rec.Code)
	}

	// An unknown id reads as 404.
	rec = h.do(t, http.MethodDelete, "/users/me/app-passwords/99999", token, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown revoke status = %d, want 404", rec.Code)
	}
}

// A user cannot revoke (or otherwise touch) another user's app password.
func TestAppPassword_CrossUserIsolation(t *testing.T) {
	h := newLEHarness(t, false)
	h.state.seedUser(lePlainUserID, lePlainUserMail, "user", false, "")
	h.state.seedUser(leTwoFAUserID, leTwoFAMail, "user", false, "")

	ownerToken := leToken(t, lePlainUserID, "user")
	attackerToken := leToken(t, leTwoFAUserID, "user")

	created := leCreateAppPassword(t, h, ownerToken, "Mine")

	rec := h.do(t, http.MethodDelete, fmt.Sprintf("/users/me/app-passwords/%d", created.AppPassword.ID), attackerToken, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user revoke status = %d, want 404", rec.Code)
	}

	// The victim's password still verifies.
	active, err := h.users.AppPasswordActiveHashes(lePlainUserID)
	if err != nil {
		t.Fatalf("load active hashes: %v", err)
	}
	if len(active) != 1 || !VerifyAppPassword(active, created.Password) {
		t.Fatal("the owner's app password must be untouched")
	}
}

func TestAppPassword_CreateRequiresName(t *testing.T) {
	h := newLEHarness(t, false)
	h.state.seedUser(lePlainUserID, lePlainUserMail, "user", false, "")
	token := leToken(t, lePlainUserID, "user")

	rec := h.do(t, http.MethodPost, "/users/me/app-passwords", token, `{"name":"   "}`)
	if rec.Code == http.StatusCreated {
		t.Fatalf("blank name must be refused, got %d", rec.Code)
	}
}
