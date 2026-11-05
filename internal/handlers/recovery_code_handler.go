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
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/twofactor"
	"github.com/jkaninda/okapi"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Recovery codes (R-10): a set of single-use backup codes generated when 2FA is
// enabled. At login a recovery code is accepted in place of the TOTP code, so a
// lost or out-of-sync authenticator no longer requires admin intervention.
//
// Codes are eight characters from an unambiguous alphabet, displayed in two
// groups; only a bcrypt digest is stored, and a consumed code is stamped with
// used_at so it can never verify again.

const (
	// recoveryCodeCount is how many codes one generation issues.
	recoveryCodeCount = 10
	// recoveryCodeLength is the number of characters in one code.
	recoveryCodeLength = 8
	// recoveryCodeAlphabet skips 0/O and 1/I/L so a code read off paper is not
	// mistyped; 31 symbols keep modulo bias negligible for 8 draws.
	recoveryCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	// recoveryCodeBcryptCost is below the password cost deliberately: codes are
	// high-entropy random values, and login compares the candidate against
	// every unused hash, so a heavy cost would make each attempt sluggish
	// without adding meaningful security.
	recoveryCodeBcryptCost = 8
)

// RecoveryCodeStore manages a user's single-use 2FA recovery codes.
type RecoveryCodeStore struct {
	db *gorm.DB
}

func NewRecoveryCodeStore(db *gorm.DB) *RecoveryCodeStore {
	return &RecoveryCodeStore{db: db}
}

// Generate replaces the user's outstanding codes with a fresh single-use set
// and returns the plaintext codes, which are shown exactly once.
func (s *RecoveryCodeStore) Generate(userID uint) ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	rows := make([]models.UserRecoveryCode, 0, recoveryCodeCount)
	for i := range codes {
		code, err := newRecoveryCode()
		if err != nil {
			return nil, fmt.Errorf("failed to generate recovery code: %w", err)
		}
		codes[i] = formatRecoveryCode(code)
		hash, err := bcrypt.GenerateFromPassword([]byte(code), recoveryCodeBcryptCost)
		if err != nil {
			return nil, fmt.Errorf("failed to hash recovery code: %w", err)
		}
		rows = append(rows, models.UserRecoveryCode{UserID: userID, CodeHash: string(hash)})
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// Outstanding unused codes are invalidated: a regenerated set is the
		// only valid one (a lost printout cannot be "reused by accident").
		if err := tx.Where("user_id = ? AND used_at IS NULL", userID).
			Delete(&models.UserRecoveryCode{}).Error; err != nil {
			return err
		}
		for i := range rows {
			if err := tx.Create(&rows[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// Remaining reports how many unused codes the user has left.
func (s *RecoveryCodeStore) Remaining(userID uint) (int64, error) {
	var n int64
	err := s.db.Model(&models.UserRecoveryCode{}).
		Where("user_id = ? AND used_at IS NULL", userID).
		Count(&n).Error
	return n, err
}

// DeleteAll drops every code for the user — used when 2FA is disabled.
func (s *RecoveryCodeStore) DeleteAll(userID uint) error {
	return s.db.Where("user_id = ?", userID).Delete(&models.UserRecoveryCode{}).Error
}

// Consume verifies code against the user's unused recovery codes and burns the
// match. Reports whether the code was valid and is now spent.
func (s *RecoveryCodeStore) Consume(userID uint, code string) bool {
	normalized := normalizeRecoveryCode(code)
	if len(normalized) != recoveryCodeLength {
		return false
	}
	var rows []models.UserRecoveryCode
	if err := s.db.Where("user_id = ? AND used_at IS NULL", userID).
		Find(&rows).Error; err != nil {
		return false
	}
	for i := range rows {
		if bcrypt.CompareHashAndPassword([]byte(rows[i].CodeHash), []byte(normalized)) != nil {
			continue
		}
		// Burn atomically: a concurrent login guessing the same code wins only
		// when it flips used_at first (single-use guarantee, R-10).
		res := s.db.Model(&models.UserRecoveryCode{}).
			Where("id = ? AND used_at IS NULL", rows[i].ID).
			Update("used_at", time.Now())
		if res.Error == nil && res.RowsAffected > 0 {
			return true
		}
	}
	return false
}

// newRecoveryCode draws recoveryCodeLength characters from the unambiguous
// alphabet with crypto/rand.
func newRecoveryCode() (string, error) {
	out := make([]byte, recoveryCodeLength)
	max := big.NewInt(int64(len(recoveryCodeAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("failed to draw recovery code character: %w", err)
		}
		out[i] = recoveryCodeAlphabet[n.Int64()]
	}
	return string(out), nil
}

// formatRecoveryCode renders the code in two groups for legibility.
func formatRecoveryCode(code string) string {
	if len(code) < 5 {
		return code
	}
	half := len(code) / 2
	return code[:half] + "-" + code[half:]
}

// normalizeRecoveryCode strips separators and upper-cases so the code as
// printed and as typed compare equal. Upper-casing is injective letter by
// letter on the alphabet (no two letters share an upper form), so hashing the
// normalized form is safe.
func normalizeRecoveryCode(code string) string {
	var b strings.Builder
	b.Grow(len(code))
	for _, r := range code {
		if r == '-' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// GenerateRecoveryCodes issues a fresh single-use set for the authenticated
// user. 2FA must be enabled and the current TOTP code supplied, so a hijacked
// dashboard session cannot quietly replace the user's backup codes.
func (h *UserHandler) GenerateRecoveryCodes(c *okapi.Context, req *GenerateRecoveryCodesRequest) error {
	userID := c.GetInt("user_id")
	user, err := h.repo.FindByID(uint(userID))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if !user.TwoFactorEnabled || user.TwoFactorSecret == "" {
		return c.AbortBadRequest("2FA is not enabled")
	}
	if !twofactor.ValidateCode(user.TwoFactorSecret, req.Body.Code) {
		return c.AbortBadRequest("invalid 2FA code")
	}

	codes, err := h.recoveryCodes().Generate(user.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to generate recovery codes", err)
	}

	if h.audit != nil {
		h.audit.LogCtx(c, "user.recovery_codes.generated",
			fmt.Sprintf("Recovery codes regenerated for %q", user.Email),
			map[string]any{"count": len(codes)})
	}
	h.sendTwoFactorAlert(user.ID, "recovery codes regenerated")

	return ok(c, RecoveryCodesResponse{Codes: codes})
}

// RecoveryCodeCount reports how many single-use codes the user has left, so the
// dashboard can prompt for regeneration before the last one is spent.
func (h *UserHandler) RecoveryCodeCount(c *okapi.Context) error {
	userID := c.GetInt("user_id")
	remaining, err := h.recoveryCodes().Remaining(uint(userID))
	if err != nil {
		return c.AbortInternalServerError("failed to count recovery codes", err)
	}
	return ok(c, RecoveryCodeCountResponse{Remaining: remaining})
}

// RecoveryCodeCountResponse is the remaining-code count.
type RecoveryCodeCountResponse struct {
	Remaining int64 `json:"remaining"`
}

// GenerateRecoveryCodesRequest carries the TOTP confirmation code.
type GenerateRecoveryCodesRequest struct {
	Body struct {
		Code string `json:"code" required:"true" doc:"Current TOTP code from the authenticator."`
	} `json:"body"`
}

// RecoveryCodesResponse returns the freshly generated plaintext codes. They are
// shown once and never retrievable again.
type RecoveryCodesResponse struct {
	Codes []string `json:"codes"`
}
