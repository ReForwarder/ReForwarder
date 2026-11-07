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

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
)

// Refresh-token lifetime: the whole point of CA-03 is that the access JWT
// stays short-lived while the refresh token lives longer. Two weeks is the
// "remember this device" window; the access JWT keeps its own TTL.
const RefreshTokenTTL = 14 * 24 * time.Hour

// Typed outcomes the refresh endpoint maps to HTTP semantics.
var (
	// ErrRefreshInvalid covers unknown, expired, revoked (logout) and
	// malformed tokens — always one generic rejection on the wire.
	ErrRefreshInvalid = errors.New("invalid or expired refresh token")
	// ErrRefreshReuse is returned when a token that was already rotated is
	// presented again. The family has been revoked by the time this returns;
	// the client must start over with a fresh login.
	ErrRefreshReuse = errors.New("refresh token reuse detected; token family revoked")
)

// RefreshTokenService issues and rotates the CA-03 refresh tokens. The
// pepper (the JWT secret) means a stolen database alone cannot verify a
// guessed token, and the stored form is a plain SHA-256 digest — cheap to
// look up by index, unlike bcrypt, because the token itself is high-entropy.
type RefreshTokenService struct {
	repo   *repositories.RefreshTokenRepository
	pepper []byte
}

func NewRefreshTokenService(repo *repositories.RefreshTokenRepository, jwtSecret string) *RefreshTokenService {
	return &RefreshTokenService{repo: repo, pepper: []byte(jwtSecret)}
}

// hashToken derives the stored digest for a presented token.
func (s *RefreshTokenService) hashToken(token string) string {
	h := sha256.New()
	h.Write(s.pepper)
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

// newToken mints a 256-bit opaque token.
func newRefreshSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// Issue mints a brand-new rotation family for the user and returns the
// plaintext token (shown/sent once; only the digest is stored).
func (s *RefreshTokenService) Issue(userID uint) (string, error) {
	secret, err := newRefreshSecret()
	if err != nil {
		return "", err
	}
	familyID, err := s.repo.NextFamilyID()
	if err != nil {
		return "", err
	}
	row := &models.RefreshToken{
		UserID:    userID,
		FamilyID:  familyID,
		TokenHash: s.hashToken(secret),
		ExpiresAt: time.Now().Add(RefreshTokenTTL),
	}
	if err := s.repo.Create(row); err != nil {
		return "", err
	}
	return secret, nil
}

// Rotate redeems a presented token: single-use, atomically. A live token is
// consumed and its successor (same family) returned. A replayed token — one
// that was already rotated — revokes the entire family before failing, so a
// stolen chain cannot be ridden in parallel with its legitimate owner.
func (s *RefreshTokenService) Rotate(presented string) (newToken string, userID uint, err error) {
	if presented == "" {
		return "", 0, ErrRefreshInvalid
	}
	row, err := s.repo.FindByHash(s.hashToken(presented))
	if err != nil || row == nil {
		return "", 0, ErrRefreshInvalid
	}
	if row.Revoked && row.RotatedAt != nil {
		// Replay of a rotated token: the classic reuse signal. Kill the family.
		if _, ferr := s.repo.RevokeFamily(row.FamilyID); ferr != nil {
			return "", 0, ferr
		}
		return "", 0, ErrRefreshReuse
	}
	if row.Revoked || time.Now().After(row.ExpiresAt) {
		return "", 0, ErrRefreshInvalid
	}

	// Single-use grant: the guarded UPDATE wins exactly once even under
	// concurrent redemptions of the same token.
	won, err := s.repo.ConsumeForRotation(row.ID)
	if err != nil {
		return "", 0, err
	}
	if !won {
		// Lost the race — the token was consumed between our read and write,
		// i.e. someone else just rotated it. Treat as reuse.
		if _, ferr := s.repo.RevokeFamily(row.FamilyID); ferr != nil {
			return "", 0, ferr
		}
		return "", 0, ErrRefreshReuse
	}

	secret, err := newRefreshSecret()
	if err != nil {
		return "", 0, err
	}
	successor := &models.RefreshToken{
		UserID:      row.UserID,
		FamilyID:    row.FamilyID,
		TokenHash:   s.hashToken(secret),
		RotatedFrom: row.ID,
		ExpiresAt:   time.Now().Add(RefreshTokenTTL),
	}
	if err := s.repo.Create(successor); err != nil {
		return "", 0, err
	}
	_ = s.repo.TouchLastUsed(row.ID)
	return secret, row.UserID, nil
}

// RevokeByHash implements the logout half of CA-03: the presented token (if
// any) dies with the session.
func (s *RefreshTokenService) RevokeByHash(presented string) error {
	if presented == "" {
		return nil
	}
	_, err := s.repo.RevokeByHash(s.hashToken(presented))
	return err
}

// RevokeAllForUser drops every live refresh token for a user (password
// change, account disable).
func (s *RefreshTokenService) RevokeAllForUser(userID uint) error {
	_, err := s.repo.RevokeAllForUser(userID)
	return err
}
