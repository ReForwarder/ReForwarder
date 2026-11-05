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
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
)

// SMTPUsernamePrefix identifies a generated SMTP Relay username.
const SMTPUsernamePrefix = "smtp_"

type CredentialService struct {
	repo *repositories.SMTPCredentialRepository
}

func NewCredentialService(repo *repositories.SMTPCredentialRepository) *CredentialService {
	return &CredentialService{repo: repo}
}

func (s *CredentialService) GenerateCredential(userID, workspaceID uint, name string, allowedIPs []string) (username, password string, cred *models.SMTPCredential, err error) {
	userBytes := make([]byte, 8)
	if _, err = rand.Read(userBytes); err != nil {
		return "", "", nil, fmt.Errorf("failed to generate username: %w", err)
	}
	username = SMTPUsernamePrefix + hex.EncodeToString(userBytes)

	passBytes := make([]byte, 32)
	if _, err = rand.Read(passBytes); err != nil {
		return "", "", nil, fmt.Errorf("failed to generate password: %w", err)
	}
	password = hex.EncodeToString(passBytes)

	hash, err := hashPassword(password)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to hash password: %w", err)
	}

	cred = &models.SMTPCredential{
		WorkspaceID:  workspaceID,
		UserID:       userID,
		Name:         name,
		Username:     username,
		PasswordHash: hash,
		AllowedIPs:   allowedIPs,
	}

	if err = s.repo.Create(cred); err != nil {
		return "", "", nil, err
	}

	return username, password, cred, nil
}

// VerifyPassword reports whether password matches cred's stored hash, and
// transparently migrates pre-R-25 rows: a successful login against a legacy
// unsalted SHA-256 digest re-hashes the credential with bcrypt (R-25). repo may
// be nil, in which case a legacy row verifies without being upgraded.
func VerifyPassword(repo *repositories.SMTPCredentialRepository, cred *models.SMTPCredential, password string) bool {
	ok, legacy := verifyPasswordHash(cred.PasswordHash, password)
	if !ok || !legacy || repo == nil {
		return ok
	}
	// Re-hash only when the row still carries the digest that was just
	// verified, so a concurrent upgrade (or revoke/reset) is never clobbered.
	hash, err := hashPassword(password)
	if err != nil {
		return true
	}
	_ = repo.UpdatePasswordHashIfUnchanged(cred.ID, cred.PasswordHash, hash)
	return true
}

// verifyPasswordHash reports whether password matches hash and whether hash is
// a legacy digest that should be upgraded. Bcrypt digests go through
// bcrypt.CompareHashAndPassword (constant-time by construction); legacy
// hex(sha256(password)) rows from before R-25 are compared with
// subtle.ConstantTimeCompare so a wrong password costs the same as a right one.
func verifyPasswordHash(hash, password string) (ok, legacy bool) {
	if isBcryptHash(hash) {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil, false
	}
	expected := legacyHashPassword(password)
	return subtle.ConstantTimeCompare([]byte(hash), []byte(expected)) == 1, true
}

// hashPassword returns the bcrypt digest used for new credentials (R-25):
// salted, with a real KDF, unlike the pre-R-25 bare SHA-256.
func hashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// legacyHashPassword is the pre-R-25 storage form, kept only to verify and
// migrate existing rows. Do not use for new credentials.
func legacyHashPassword(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:])
}

// isBcryptHash reports whether hash looks like a bcrypt digest ($2a/$2b/$2y).
func isBcryptHash(hash string) bool {
	return len(hash) > 4 && hash[0] == '$' && strings.HasPrefix(hash, "$2")
}
