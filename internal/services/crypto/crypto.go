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

package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jkaninda/logger"
)

const encryptedPrefix = "enc:"

// ErrEncryptionDisabled is returned by Encrypt when no POSTA_ENCRYPTION_KEY is
// configured (SF-78). Callers surface it as a configuration problem — the
// operation cannot complete safely — rather than silently writing
// base64-obfuscated plaintext to disk.
var ErrEncryptionDisabled = errors.New("crypto: no encryption key configured (set POSTA_ENCRYPTION_KEY); refusing to store a secret unprotected")

// key holds the derived 32-byte AES key. nil means encryption is disabled.
//
// Historical note (SF-78): before the hardening, a nil key silently fell back
// to base64 — encoding, not encryption — for every new secret. That fallback
// still exists for DECRYPT so installations that predate POSTA_ENCRYPTION_KEY
// keep reading their legacy rows (the migration path), but new secrets are
// refused instead of being written unprotected.
var (
	key   []byte
	keyMu sync.RWMutex
)

// Init derives a 32-byte AES-256 key from the provided secret via SHA-256.
// Call this once at startup. An empty secret disables encryption: Encrypt
// then fails with ErrEncryptionDisabled, and a loud warning is logged so the
// misconfiguration is visible at boot rather than at the first failed save.
func Init(secret string) {
	keyMu.Lock()
	if secret == "" {
		key = nil
		keyMu.Unlock()
		// SF-78: base64 was never encryption. Say so, loudly, once at boot.
		logger.Warn("SECURITY: POSTA_ENCRYPTION_KEY is not configured — secrets " +
			"(SMTP credentials, DKIM private keys) cannot be encrypted at rest and " +
			"storing new ones will fail. Legacy base64 rows remain readable for migration.")
		return
	}
	h := sha256.Sum256([]byte(secret))
	key = h[:]
	keyMu.Unlock()
}

// Enabled returns true if encryption is configured.
func Enabled() bool {
	keyMu.RLock()
	defer keyMu.RUnlock()
	return key != nil
}

// Encrypt encrypts plaintext with AES-256-GCM.
// Returns "enc:" + base64(nonce||ciphertext).
// Without a configured key it refuses with ErrEncryptionDisabled instead of
// writing base64-encoded plaintext (SF-78). Decrypt keeps accepting legacy
// base64 rows so existing installations migrate forward.
func Encrypt(plaintext string) (string, error) {
	keyMu.RLock()
	k := key
	keyMu.RUnlock()

	if k == nil {
		return "", ErrEncryptionDisabled
	}

	ct, err := aesGCMEncrypt(k, []byte(plaintext))
	if err != nil {
		return "", fmt.Errorf("crypto: failed to encrypt: %w", err)
	}
	return encryptedPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt decrypts a stored value. If it has the "enc:" prefix, AES-256-GCM
// decryption is used. Otherwise it is treated as base64.
func Decrypt(stored string) (string, error) {
	if IsEncrypted(stored) {
		keyMu.RLock()
		k := key
		keyMu.RUnlock()

		if k == nil {
			return "", fmt.Errorf("crypto: encrypted value found but no encryption key configured")
		}

		encoded := strings.TrimPrefix(stored, encryptedPrefix)
		ct, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", fmt.Errorf("crypto: failed to decode ciphertext: %w", err)
		}
		pt, err := aesGCMDecrypt(k, ct)
		if err != nil {
			return "", fmt.Errorf("crypto: failed to decrypt: %w", err)
		}
		return string(pt), nil
	}

	// base64 fallback (SF-78 legacy read path): rows written before
	// POSTA_ENCRYPTION_KEY existed hold base64-encoded plaintext. They stay
	// readable so an installation can migrate to encryption; new writes are
	// refused by Encrypt.
	decoded, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return stored, nil
	}
	return string(decoded), nil
}

// IsEncrypted returns true if the stored value uses the encrypted format.
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, encryptedPrefix)
}

// aesGCMEncrypt performs AES-256-GCM encryption, returning nonce||ciphertext.
func aesGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// aesGCMDecrypt performs AES-256-GCM decryption on nonce||ciphertext.
func aesGCMDecrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
}
