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
	"errors"
	"strings"
	"testing"
)

func TestEncryptDecryptWithKey(t *testing.T) {
	Init("test-secret-key")
	defer Init("")

	plaintext := "my-smtp-password-123"

	encrypted, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if !strings.HasPrefix(encrypted, "enc:") {
		t.Fatalf("encrypted value should start with 'enc:', got: %s", encrypted[:10])
	}

	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptWithoutKey(t *testing.T) {
	Init("")

	// SF-78: without a configured key Encrypt must REFUSE, not fall back to
	// base64-obfuscated plaintext.
	encoded, err := Encrypt("my-password")
	if err == nil {
		t.Fatal("Encrypt without a key must fail, got success")
	}
	if !errors.Is(err, ErrEncryptionDisabled) {
		t.Fatalf("error = %v, want ErrEncryptionDisabled", err)
	}
	if encoded != "" {
		t.Fatalf("refused encryption returned data: %q", encoded)
	}
}

// The migration path: rows written by the old base64 fallback must keep
// decrypting, both with and without a key configured.
func TestDecryptLegacyBase64WithoutKey(t *testing.T) {
	Init("")

	decoded, err := Decrypt("bXktcGFzc3dvcmQ=")
	if err != nil {
		t.Fatalf("Decrypt legacy without key failed: %v", err)
	}
	if decoded != "my-password" {
		t.Fatalf("got %q, want %q", decoded, "my-password")
	}
}

// An "enc:" row cannot be opened without the key — that stays a hard error.
func TestDecryptEncryptedWithoutKeyFails(t *testing.T) {
	Init("some-key")
	enc, err := Encrypt("secret-material")
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	Init("") // "lost" the key
	if _, err := Decrypt(enc); err == nil {
		t.Fatal("Decrypt of an enc: row without a key must fail")
	}
}

func TestDecryptLegacyBase64(t *testing.T) {
	Init("some-key")
	defer Init("")

	// "my-password" in base64
	legacy := "bXktcGFzc3dvcmQ="
	decrypted, err := Decrypt(legacy)
	if err != nil {
		t.Fatalf("Decrypt legacy failed: %v", err)
	}
	if decrypted != "my-password" {
		t.Fatalf("got %q, want %q", decrypted, "my-password")
	}
}

func TestIsEncrypted(t *testing.T) {
	if IsEncrypted("bXktcGFzc3dvcmQ=") {
		t.Fatal("base64 should not be detected as encrypted")
	}
	if !IsEncrypted("enc:abcdef") {
		t.Fatal("enc: prefixed should be detected as encrypted")
	}
	if IsEncrypted("") {
		t.Fatal("empty string should not be encrypted")
	}
}

func TestEncryptProducesDifferentCiphertexts(t *testing.T) {
	Init("test-key")
	defer Init("")

	enc1, _ := Encrypt("same")
	enc2, _ := Encrypt("same")
	if enc1 == enc2 {
		t.Fatal("same plaintext should produce different ciphertexts (random nonce)")
	}

	dec1, _ := Decrypt(enc1)
	dec2, _ := Decrypt(enc2)
	if dec1 != "same" || dec2 != "same" {
		t.Fatal("both should decrypt to 'same'")
	}
}

func TestEnabled(t *testing.T) {
	Init("")
	if Enabled() {
		t.Fatal("should not be enabled without key")
	}
	Init("key")
	if !Enabled() {
		t.Fatal("should be enabled with key")
	}
	Init("")
}
