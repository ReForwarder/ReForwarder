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

package mailauth

import (
	"context"
	"errors"
	"testing"

	"github.com/goposta/posta/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// fakeUserFinder / fakeAppPasswordFinder stand in for the repositories (the
// ratelimit fake-cmdable pattern: the seams are interfaces, so no SQL fake is
// needed to prove the verification contract).
type fakeUserFinder struct {
	users map[string]*models.User
}

func (f *fakeUserFinder) FindByEmail(email string) (*models.User, error) {
	u, ok := f.users[email]
	if !ok {
		return nil, errors.New("not found")
	}
	return u, nil
}

type fakeAppPasswordFinder struct {
	// active mirrors FindActiveByUserID: only still-active digests.
	active  map[uint][]models.UserAppPassword
	touched map[uint]bool
}

func (f *fakeAppPasswordFinder) FindActiveByUserID(userID uint) ([]models.UserAppPassword, error) {
	return f.active[userID], nil
}

func (f *fakeAppPasswordFinder) TouchLastUsed(id uint) error {
	f.touched[id] = true
	return nil
}

func hashFor(t *testing.T, secret string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(h)
}

// seedUser builds the fixtures: one user with a primary password hash (which
// must NEVER authenticate), two app passwords, one of which is revoked and
// therefore absent from the active set.
func seedUser(t *testing.T) (*fakeUserFinder, *fakeAppPasswordFinder, *Authenticator, *models.User) {
	t.Helper()
	uf := &fakeUserFinder{users: map[string]*models.User{
		"ada@example.test": {
			ID:           7,
			Email:        "ada@example.test",
			PasswordHash: hashFor(t, "primary-dashboard-secret"),
		},
	}}
	pf := &fakeAppPasswordFinder{
		active: map[uint][]models.UserAppPassword{
			7: {
				{ID: 101, UserID: 7, Name: "laptop", PasswordHash: hashFor(t, "apw_secret_laptop")},
				{ID: 102, UserID: 7, Name: "phone", PasswordHash: hashFor(t, "apw_secret_phone")},
			},
		},
		touched: map[uint]bool{},
	}
	auth := NewAuthenticator(uf, pf)
	if auth == nil {
		t.Fatalf("NewAuthenticator returned nil")
	}
	return uf, pf, auth, uf.users["ada@example.test"]
}

// TestAuthenticateWithAppPassword: the full verification matrix — a valid app
// password (with last_used_at stamped), wrong secret, revoked password,
// missing user, no app passwords, empty inputs, the primary password
// rejection, and the no-enumeration failure shape.
func TestAuthenticateWithAppPassword(t *testing.T) {
	uf, pf, auth, user := seedUser(t)
	ctx := context.Background()

	t.Run("valid app password authenticates and stamps last_used", func(t *testing.T) {
		u, err := auth.AuthenticateWithAppPassword(ctx, "ada@example.test", "apw_secret_laptop")
		if err != nil {
			t.Fatalf("valid app password rejected: %v", err)
		}
		if u.ID != user.ID || u.Email != "ada@example.test" {
			t.Fatalf("returned user = %+v", u)
		}
		if !pf.touched[101] {
			t.Fatalf("last_used_at not stamped for the matched credential")
		}
		if pf.touched[102] {
			t.Fatalf("last_used_at stamped for a non-matching credential")
		}
	})

	t.Run("second credential also verifies", func(t *testing.T) {
		if _, err := auth.AuthenticateWithAppPassword(ctx, "ada@example.test", "apw_secret_phone"); err != nil {
			t.Fatalf("second app password rejected: %v", err)
		}
	})

	t.Run("wrong secret rejected without stamping", func(t *testing.T) {
		before := len(pf.touched)
		if _, err := auth.AuthenticateWithAppPassword(ctx, "ada@example.test", "apw_secret_wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("wrong secret → %v, want ErrInvalidCredentials", err)
		}
		if len(pf.touched) != before {
			t.Fatalf("failed verification stamped last_used_at: %v", pf.touched)
		}
	})

	t.Run("revoked app password rejected", func(t *testing.T) {
		// Revocation removes the digest from the active set (exactly what
		// repositories.UserAppPasswordRepository.FindActiveByUserID does).
		pf.active[7] = pf.active[7][:1] // drop the "phone" row
		if _, err := auth.AuthenticateWithAppPassword(ctx, "ada@example.test", "apw_secret_phone"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("revoked secret → %v, want ErrInvalidCredentials", err)
		}
		pf.active[7] = append(pf.active[7], models.UserAppPassword{ID: 102, UserID: 7, Name: "phone", PasswordHash: hashFor(t, "apw_secret_phone")})
	})

	t.Run("unknown user rejected identically", func(t *testing.T) {
		if _, err := auth.AuthenticateWithAppPassword(ctx, "nobody@example.test", "apw_secret_laptop"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("unknown user → %v, want ErrInvalidCredentials", err)
		}
	})

	t.Run("user without app passwords rejected identically", func(t *testing.T) {
		uf.users["grace@example.test"] = &models.User{ID: 8, Email: "grace@example.test"}
		if _, err := auth.AuthenticateWithAppPassword(ctx, "grace@example.test", "anything"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("no app passwords → %v, want ErrInvalidCredentials", err)
		}
	})

	t.Run("primary account password rejected", func(t *testing.T) {
		// The structural guarantee: the presented secret is compared only
		// against app-password digests — the users table hash is never read.
		// A client reusing its dashboard password as the mail secret fails.
		if _, err := auth.AuthenticateWithAppPassword(ctx, "ada@example.test", "primary-dashboard-secret"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("primary password accepted: %v", err)
		}
	})

	t.Run("empty inputs rejected", func(t *testing.T) {
		if _, err := auth.AuthenticateWithAppPassword(ctx, "", "apw_secret_laptop"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("empty email accepted: %v", err)
		}
		if _, err := auth.AuthenticateWithAppPassword(ctx, "ada@example.test", ""); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("empty secret accepted: %v", err)
		}
		if _, err := auth.AuthenticateWithAppPassword(ctx, "  ada@example.test  ", "apw_secret_laptop"); err != nil {
			t.Fatalf("padded email should be trimmed, got: %v", err)
		}
	})

	t.Run("nil authenticator refuses everything", func(t *testing.T) {
		var nilAuth *Authenticator
		if _, err := nilAuth.AuthenticateWithAppPassword(ctx, "ada@example.test", "apw_secret_laptop"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("nil authenticator → %v, want ErrInvalidCredentials", err)
		}
	})
}

// TestAuthenticateWithAppPasswordNilDeps: a nil dependency disables the
// authenticator rather than leaving a half-wired one that panics on first
// login.
func TestAuthenticateWithAppPasswordNilDeps(t *testing.T) {
	if NewAuthenticator(nil, &fakeAppPasswordFinder{}) != nil {
		t.Fatalf("nil user finder must disable the authenticator")
	}
	if NewAuthenticator(&fakeUserFinder{}, nil) != nil {
		t.Fatalf("nil password finder must disable the authenticator")
	}
}
