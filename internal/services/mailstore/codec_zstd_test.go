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

package mailstore

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/services/crypto"
)

// The codec suite covers the W2 BlobCodec implementation (ADR-003 decision 4):
// zstd compression for content above the 4 KiB threshold, AES-256-GCM
// encryption through internal/services/crypto, and SHA-256 content addressing
// with ErrBlobCorrupt on every tampering path. The crypto key is package
// state, so each test initializes it explicitly.

const codecTestKey = "mailstore-codec-test-key"

// TestZstdBlobCodecRoundTrip — plaintext round-trips byte-exact through
// compress + encrypt + decrypt, below and above the compression threshold.
func TestZstdBlobCodecRoundTrip(t *testing.T) {
	crypto.Init(codecTestKey)
	codec := ZstdBlobCodec{}

	cases := []struct {
		name string
		data []byte
	}{
		{"small-body-under-threshold", []byte(strings.Repeat("hello mailstore ", 32))}, // 512 B
		{"large-compressible-body", []byte(strings.Repeat("lorem ipsum dolor sit amet\n", 400))},
		{"exact-threshold-boundary", []byte(strings.Repeat("x", compressionThreshold+1))},
	}
	for _, tc := range cases {
		stored, ref, err := codec.Encode(tc.data)
		if err != nil {
			t.Fatalf("%s: encode: %v", tc.name, err)
		}
		if ref.Size != int64(len(tc.data)) {
			t.Fatalf("%s: ref.Size = %d, want %d", tc.name, ref.Size, len(tc.data))
		}
		if bytes.Equal(stored, tc.data) && len(tc.data) > compressionThreshold {
			t.Fatalf("%s: large compressible body stored as plaintext", tc.name)
		}
		decoded, err := codec.Decode(stored, ref)
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if !bytes.Equal(decoded, tc.data) {
			t.Fatalf("%s: round-trip mismatch (%d vs %d bytes)", tc.name, len(decoded), len(tc.data))
		}
	}
}

// TestZstdBlobCodecCompressesLargeBodiesOnly — the ADR-003 rule: zstd applies
// only above the 4 KiB threshold and only when it actually shrinks the
// payload; the frame flag records the choice either way.
func TestZstdBlobCodecCompressesLargeBodiesOnly(t *testing.T) {
	crypto.Init(codecTestKey)
	codec := ZstdBlobCodec{}

	small := []byte(strings.Repeat("tiny", 100)) // 400 B < 4 KiB
	stored, _, err := codec.Encode(small)
	if err != nil {
		t.Fatalf("encode small: %v", err)
	}
	if stored[1]&codecFlagCompressed != 0 {
		t.Fatalf("small body must not be compressed (flags=0x%02x)", stored[1])
	}

	big := []byte(strings.Repeat("compressible text over the threshold ", 300))
	stored, ref, err := codec.Encode(big)
	if err != nil {
		t.Fatalf("encode big: %v", err)
	}
	if stored[1]&codecFlagCompressed == 0 {
		t.Fatalf("large compressible body must be compressed (flags=0x%02x)", stored[1])
	}
	if int64(len(stored)) >= ref.Size {
		t.Fatalf("compressed frame (%d B) should be smaller than plaintext (%d B)", len(stored), ref.Size)
	}
	if decoded, err := codec.Decode(stored, ref); err != nil || !bytes.Equal(decoded, big) {
		t.Fatalf("compressed round trip: err=%v match=%v", err, bytes.Equal(decoded, big))
	}

	// Incompressible content (random bytes): compression is skipped rather
	// than inflating the payload, but encryption and addressing still apply.
	incompressible := make([]byte, compressionThreshold*4)
	if _, err := rand.Read(incompressible); err != nil {
		t.Fatalf("rand: %v", err)
	}
	stored, ref, err = codec.Encode(incompressible)
	if err != nil {
		t.Fatalf("encode random: %v", err)
	}
	if stored[1]&codecFlagCompressed != 0 {
		t.Fatalf("incompressible body must not be force-compressed (flags=0x%02x)", stored[1])
	}
	if bytes.Contains(stored, incompressible[:32]) {
		t.Fatalf("incompressible body stored unencrypted")
	}
	if decoded, err := codec.Decode(stored, ref); err != nil || !bytes.Equal(decoded, incompressible) {
		t.Fatalf("incompressible round trip: err=%v match=%v", err, bytes.Equal(decoded, incompressible))
	}
}

// TestZstdBlobCodecContentAddress — the dedup identity: same plaintext → same
// SHA-256 hash (and thus one blob.Store object), different plaintext →
// different hash. Ciphertexts differ per write (random GCM nonces) without
// affecting the address.
func TestZstdBlobCodecContentAddress(t *testing.T) {
	crypto.Init(codecTestKey)
	codec := ZstdBlobCodec{}

	payload := []byte(strings.Repeat("dedup me ", 600))
	_, ref1, err := codec.Encode(payload)
	if err != nil {
		t.Fatalf("encode 1: %v", err)
	}
	_, ref2, err := codec.Encode(payload)
	if err != nil {
		t.Fatalf("encode 2: %v", err)
	}
	if ref1.Hash != ref2.Hash {
		t.Fatalf("identical content addressed differently: %s vs %s", ref1.Hash, ref2.Hash)
	}
	if len(ref1.Hash) != 64 {
		t.Fatalf("hash = %q, want 64-char SHA-256 hex", ref1.Hash)
	}

	other := []byte(strings.Repeat("dedup ME ", 600))
	_, ref3, err := codec.Encode(other)
	if err != nil {
		t.Fatalf("encode 3: %v", err)
	}
	if ref3.Hash == ref1.Hash {
		t.Fatalf("different content shared a hash")
	}
}

// TestZstdBlobCodecCorruptionDetected — bit flips, truncation, unknown frame
// versions and unknown flags all collapse to ErrBlobCorrupt.
func TestZstdBlobCodecCorruptionDetected(t *testing.T) {
	crypto.Init(codecTestKey)
	codec := ZstdBlobCodec{}

	data := []byte(strings.Repeat("tamper target ", 500))
	stored, ref, err := codec.Encode(data)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	tampered := append([]byte(nil), stored...)
	tampered[len(tampered)/2] ^= 0xFF // flip a bit inside nonce/ciphertext
	if _, err := codec.Decode(tampered, ref); !errors.Is(err, ErrBlobCorrupt) {
		t.Fatalf("tampered ciphertext err = %v, want ErrBlobCorrupt", err)
	}

	if _, err := codec.Decode(stored[:len(stored)-1], ref); !errors.Is(err, ErrBlobCorrupt) {
		t.Fatalf("truncated frame err = %v, want ErrBlobCorrupt", err)
	}

	badVersion := append([]byte(nil), stored...)
	badVersion[0] = 0x7F
	if _, err := codec.Decode(badVersion, ref); !errors.Is(err, ErrBlobCorrupt) {
		t.Fatalf("unknown version err = %v, want ErrBlobCorrupt", err)
	}

	badFlags := append([]byte(nil), stored...)
	badFlags[1] |= 0x80
	if _, err := codec.Decode(badFlags, ref); !errors.Is(err, ErrBlobCorrupt) {
		t.Fatalf("unknown flags err = %v, want ErrBlobCorrupt", err)
	}

	if _, err := codec.Decode([]byte{1}, ref); !errors.Is(err, ErrBlobCorrupt) {
		t.Fatalf("short frame err = %v, want ErrBlobCorrupt", err)
	}
}

// TestZstdBlobCodecWrongKeyReportsCorrupt — a blob written under one data key
// is unreadable garbage under another; the codec surfaces that as
// ErrBlobCorrupt (never a panic, never partial plaintext).
func TestZstdBlobCodecWrongKeyReportsCorrupt(t *testing.T) {
	crypto.Init(codecTestKey)
	codec := ZstdBlobCodec{}

	data := []byte(strings.Repeat("secret body ", 500))
	stored, ref, err := codec.Encode(data)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	crypto.Init("a-different-deployment-key")
	defer crypto.Init(codecTestKey)
	if _, err := codec.Decode(stored, ref); !errors.Is(err, ErrBlobCorrupt) {
		t.Fatalf("wrong-key decode err = %v, want ErrBlobCorrupt", err)
	}
}

// TestZstdBlobCodecEncryptionDisabledRefused — SF-78: without a configured
// key, Encode refuses instead of storing plaintext.
func TestZstdBlobCodecEncryptionDisabledRefused(t *testing.T) {
	crypto.Init("") // no POSTA_ENCRYPTION_KEY
	defer crypto.Init(codecTestKey)
	codec := ZstdBlobCodec{}
	if _, _, err := codec.Encode([]byte("payload")); !errors.Is(err, crypto.ErrEncryptionDisabled) {
		t.Fatalf("encode without key err = %v, want ErrEncryptionDisabled", err)
	}
}

// TestZstdBlobCodecConcurrentUse — the codec is safe for concurrent use
// (shared zstd encoders); run under -race in the battery.
func TestZstdBlobCodecConcurrentUse(t *testing.T) {
	crypto.Init(codecTestKey)
	codec := ZstdBlobCodec{}

	payloads := make([][]byte, 8)
	for i := range payloads {
		payloads[i] = []byte(strings.Repeat("concurrent "+strings.Repeat("z", i), 400))
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(payloads)*2)
	for _, p := range payloads {
		wg.Add(1)
		go func(p []byte) {
			defer wg.Done()
			stored, ref, err := codec.Encode(p)
			if err != nil {
				errs <- err
				return
			}
			decoded, err := codec.Decode(stored, ref)
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(decoded, p) {
				errs <- errors.New("concurrent round-trip mismatch")
			}
		}(p)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent use: %v", err)
	}
}

// TestZstdBlobCodecEmptyRejected — nothing is stored for empty content.
func TestZstdBlobCodecEmptyRejected(t *testing.T) {
	crypto.Init(codecTestKey)
	codec := ZstdBlobCodec{}
	if _, _, err := codec.Encode(nil); !errors.Is(err, ErrEmptyMessage) {
		t.Fatalf("encode nil err = %v, want ErrEmptyMessage", err)
	}
}
