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

// Package webpush implements the client side of the Web Push protocol for
// Posta's own events (SF-89): RFC 8030 delivery to a push service, RFC 8291
// aes128gcm message encryption, and RFC 8292 VAPID authentication. Only Go
// standard-library crypto is used — ECDH P-256, HKDF (built on
// crypto/hmac + crypto/sha256), AES-128-GCM, and ECDSA P-256 for the VAPID
// JWT.
//
// A push "subscription" here is the browser-issued triple: the push-service
// endpoint URL plus the p256dh/auth key pair. Encrypting a payload burns a
// fresh ephemeral key every time; the application server's key pair (VAPID)
// is long-lived and comes from configuration.
package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Record size of the encrypted message: the aes128gcm default (4096) is ample
// for Posta's small notification payloads and fits a single record.
const recordSize = 4096

// aes128gcmHeaderLen is the RFC 8291 aes128gcm header size: salt (16) + record
// size (4) + key id length (1) + ephemeral public key (65 for P-256).
const aes128gcmHeaderLen = 16 + 4 + 1 + 65

// encodeAES128GCM encrypts plaintext for one subscription per RFC 8291 §4 and
// returns the aes128gcm body: header || ciphertext||tag. uaPub is the
// subscription's p256dh key; auth is its auth secret.
func encodeAES128GCM(uaPub *ecdh.PublicKey, auth, plaintext []byte) ([]byte, error) {
	// A fresh ephemeral server key per message: the key derivation mixes both
	// public keys, so reuse would be unnecessary and harmful.
	asKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("webpush: ephemeral key: %w", err)
	}
	shared, err := asKey.ECDH(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: ECDH with subscription key: %w", err)
	}
	asPub := asKey.PublicKey().Bytes() // 65-byte uncompressed P-256 point
	uaRaw := uaPub.Bytes()

	// ikm = HKDF(salt=auth_secret, ikm=e2e_secret || ua_pub || as_pub,
	//            info="WebPush: info" || 0x00 || ua_pub || as_pub, 32)
	ikm := hkdfKey(auth,
		concat(shared, uaRaw, asPub),
		concat([]byte("WebPush: info\x00"), uaRaw, asPub),
		32)

	// CEK and NONCE derive from the ikm with all-zero salts (16 and 12 bytes)
	// and the aes128gcm content-coding info strings (NUL-terminated).
	cek := hkdfKey(make([]byte, 16), ikm, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfKey(make([]byte, 12), ikm, []byte("Content-Encoding: nonce\x00"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("webpush: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("webpush: GCM: %w", err)
	}

	// The aes128gcm coding appends a 0x02 padding delimiter to the plaintext
	// (no padding bytes); the additional authenticated data is empty.
	sealed := gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 0x02), nil)

	// Header: salt(16) | rs(4, BE) | idlen(1) | as_pub(65). The key id IS the
	// ephemeral server public key (RFC 8291 §3.1), so idlen = 65 — the
	// push service hands it to the browser for the ECDH derivation.
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("webpush: salt: %w", err)
	}
	header := concat(salt, u32BE(recordSize), []byte{byte(len(asPub))}, asPub)
	if len(header) != aes128gcmHeaderLen {
		return nil, fmt.Errorf("webpush: header length %d, want %d", len(header), aes128gcmHeaderLen)
	}
	return append(header, sealed...), nil
}

// concat joins byte slices in one allocation.
func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// hkdfExtract runs the RFC 5869 extract step: HMAC-SHA256(salt, ikm).
func hkdfExtract(salt, ikm []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	return mac.Sum(nil)
}

// hkdfExpand runs the RFC 5869 expand step to length n.
func hkdfExpand(prk, info []byte, n int) []byte {
	out := make([]byte, 0, n)
	var prev []byte
	for i := byte(1); len(out) < n; i++ {
		mac := hmac.New(sha256.New, prk)
		mac.Write(prev)
		mac.Write(info)
		mac.Write([]byte{i})
		prev = mac.Sum(nil)
		out = append(out, prev...)
	}
	return out[:n]
}

// hkdfKey is extract-then-expand in one call.
func hkdfKey(salt, ikm, info []byte, n int) []byte {
	return hkdfExpand(hkdfExtract(salt, ikm), info, n)
}

// u32BE encodes a uint32 big-endian (the record-size field).
func u32BE(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}
