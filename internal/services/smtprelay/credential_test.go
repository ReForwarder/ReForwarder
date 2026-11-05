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

package smtprelay

import (
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// New credentials must carry salted bcrypt digests: two hashes of the same
// password differ (distinct salts), and the digest is not the bare SHA-256 the
// pre-R-25 code stored.
func TestHashPasswordBcrypt(t *testing.T) {
	h1, err := hashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	h2, err := hashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	if h1 == h2 {
		t.Errorf("hashPassword() produced identical digests: %q", h1)
	}
	if !isBcryptHash(h1) {
		t.Errorf("hashPassword() = %q, want a bcrypt digest", h1)
	}
	if !strings.Contains(h1, "$2") || len(h1) != 60 {
		t.Errorf("hashPassword() = %q, want a 60-char bcrypt digest", h1)
	}
}

func TestHashPasswordDiffers(t *testing.T) {
	h1, _ := hashPassword("password-one")
	h2, _ := hashPassword("password-two")
	if h1 == h2 {
		t.Errorf("hashPassword() produced same hash for different passwords: %q", h1)
	}
}

func TestVerifyPassword(t *testing.T) {
	hash, err := hashPassword("s3cret-pass")
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	cred := &models.SMTPCredential{PasswordHash: hash}

	if !VerifyPassword(nil, cred, "s3cret-pass") {
		t.Error("VerifyPassword() = false, want true for correct password")
	}
	if VerifyPassword(nil, cred, "wrong-pass") {
		t.Error("VerifyPassword() = true, want false for incorrect password")
	}
	if VerifyPassword(nil, cred, "") {
		t.Error("VerifyPassword() = true, want false for empty password")
	}
}

// Pre-R-25 rows stored hex(sha256(password)). They must still verify — with a
// constant-time comparison — so existing relay credentials keep working.
func TestVerifyPasswordLegacySHA256(t *testing.T) {
	cred := &models.SMTPCredential{PasswordHash: legacyHashPassword("legacy-pass")}

	if !VerifyPassword(nil, cred, "legacy-pass") {
		t.Error("legacy verify = false, want true for correct password")
	}
	if VerifyPassword(nil, cred, "other-pass") {
		t.Error("legacy verify = true, want false for incorrect password")
	}

	ok, legacy := verifyPasswordHash(cred.PasswordHash, "legacy-pass")
	if !ok || !legacy {
		t.Errorf("verifyPasswordHash() = (%v, %v), want (true, true)", ok, legacy)
	}
}

// A garbage hash must not verify, and must not panic.
func TestVerifyPasswordMalformedHash(t *testing.T) {
	for _, hash := range []string{"", "not-a-hash", "$2", "deadbeef"} {
		cred := &models.SMTPCredential{PasswordHash: hash}
		if VerifyPassword(nil, cred, "anything") {
			t.Errorf("VerifyPassword(hash=%q) = true, want false", hash)
		}
	}
}

// The generated credential must store the bcrypt digest, not the plaintext or
// the legacy SHA-256 form.
func TestGenerateCredentialStoresBcrypt(t *testing.T) {
	password := "plaintext-never-stored"
	cred := &models.SMTPCredential{PasswordHash: mustHash(t, password)}

	if cred.PasswordHash == password || cred.PasswordHash == legacyHashPassword(password) {
		t.Fatalf("credential stores an unsalted/plaintext form: %q", cred.PasswordHash)
	}
	if !VerifyPassword(nil, cred, password) {
		t.Error("generated credential does not verify")
	}
}

func mustHash(t *testing.T, password string) string {
	t.Helper()
	h, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	return h
}
