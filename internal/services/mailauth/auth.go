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

// Package mailauth is the mail-protocol authentication seam (OB-4 wave W1,
// ADR-004 decision 3): the IMAP and POP3 servers both authenticate clients
// with the user's email address plus an APP PASSWORD (SF-35/SF-67) — never
// the primary account password, which keeps the WebAuthn/2FA posture intact
// (app passwords are the deliberate, audited mail-protocol exception).
//
// Failure shape: every rejection returns ErrInvalidCredentials. A missing
// user, a user without active app passwords, a revoked password and a wrong
// secret are indistinguishable on the wire, so the mail ports cannot be used
// to enumerate identities; the servers log the audit event (ADR-004 decision
// 6) with the reason they got back.
package mailauth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/goposta/posta/internal/models"
	"github.com/jkaninda/logger"
	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials is the only failure surfaced to the wire: wrong user,
// wrong secret, revoked secret, no app passwords — all identical to a client.
var ErrInvalidCredentials = errors.New("mailauth: invalid credentials")

// UserFinder resolves the account an email addresses (implemented by
// repositories.UserRepository).
type UserFinder interface {
	FindByEmail(email string) (*models.User, error)
}

// AppPasswordFinder loads a user's still-active app-password digests and
// stamps last_used_at after a success (implemented by
// repositories.UserAppPasswordRepository).
type AppPasswordFinder interface {
	FindActiveByUserID(userID uint) ([]models.UserAppPassword, error)
	TouchLastUsed(id uint) error
}

// Authenticator verifies mail-protocol credentials. Safe for concurrent use
// (the repos are).
type Authenticator struct {
	users     UserFinder
	passwords AppPasswordFinder
}

// NewAuthenticator wires the seam. Both arguments are required; a nil
// dependency disables the authenticator (NewAuthenticator returns nil) so a
// mis-wired server refuses every login instead of panicking.
func NewAuthenticator(users UserFinder, passwords AppPasswordFinder) *Authenticator {
	if users == nil || passwords == nil {
		return nil
	}
	return &Authenticator{users: users, passwords: passwords}
}

// AuthenticateWithAppPassword verifies one mail-client login: bcrypt-compare
// the presented secret against every still-active app-password digest of the
// addressed user. On success the matched credential's last_used_at is stamped
// (best-effort — a failed stamp never fails a successful login).
//
// Primary-password rejection is structural: the presented secret is ONLY ever
// compared against app-password digests — the users table's password hash is
// never loaded, so even a client that reuses its dashboard password as the
// app-password secret is rejected (verified by TestAuthenticateWithAppPassword
// / primary-password case).
//
// Timing: the comparison loop always burns through every active digest, so
// the call's duration does not reveal which row matched (same shape as
// handlers.VerifyAppPassword; duplicated rather than imported because the
// handler package sits above services in the layering).
func (a *Authenticator) AuthenticateWithAppPassword(ctx context.Context, userEmail, secret string) (*models.User, error) {
	if a == nil {
		return nil, ErrInvalidCredentials
	}
	email := strings.TrimSpace(userEmail)
	if email == "" || secret == "" {
		return nil, ErrInvalidCredentials
	}
	user, err := a.users.FindByEmail(email)
	if err != nil || user == nil {
		// Not found (or the lookup failed) reads identically to a bad secret.
		return nil, ErrInvalidCredentials
	}
	digests, err := a.passwords.FindActiveByUserID(user.ID)
	if err != nil {
		return nil, fmt.Errorf("mailauth: load app passwords of user %d: %w", user.ID, err)
	}
	if len(digests) == 0 {
		return nil, ErrInvalidCredentials
	}
	matched := -1
	for i := range digests {
		if bcrypt.CompareHashAndPassword([]byte(digests[i].PasswordHash), []byte(secret)) == nil {
			matched = i // keep burning the loop to flatten timing
		}
	}
	if matched < 0 {
		// Includes revoked credentials: FindActiveByUserID already excludes
		// them, so a revoked secret simply fails the comparison like any
		// wrong one.
		return nil, ErrInvalidCredentials
	}
	if err := a.passwords.TouchLastUsed(digests[matched].ID); err != nil {
		logger.Warn("mailauth: last_used_at stamp failed",
			"app_password_id", digests[matched].ID, "error", err.Error())
	}
	return user, nil
}
