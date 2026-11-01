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
	"testing"

	"github.com/goposta/posta/internal/models"
)

func TestHashPassword(t *testing.T) {
	h1 := hashPassword("correct-horse-battery-staple")
	h2 := hashPassword("correct-horse-battery-staple")
	if h1 != h2 {
		t.Errorf("hashPassword() is not deterministic: %q != %q", h1, h2)
	}
}

func TestHashPasswordDiffers(t *testing.T) {
	h1 := hashPassword("password-one")
	h2 := hashPassword("password-two")
	if h1 == h2 {
		t.Errorf("hashPassword() produced same hash for different passwords: %q", h1)
	}
}

func TestVerifyPassword(t *testing.T) {
	cred := &models.SMTPCredential{PasswordHash: hashPassword("s3cret-pass")}

	if !VerifyPassword(cred, "s3cret-pass") {
		t.Error("VerifyPassword() = false, want true for correct password")
	}
	if VerifyPassword(cred, "wrong-pass") {
		t.Error("VerifyPassword() = true, want false for incorrect password")
	}
}
