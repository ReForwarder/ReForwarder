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
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/goposta/posta/internal/services/crypto"
	"github.com/klauspost/compress/zstd"
)

// ZstdBlobCodec is the production BlobCodec (OB-3 wave W2, ADR-003 decision 4,
// SF-78 + SF-83): content is zstd-compressed when large enough to benefit
// (> 4 KiB, and only when compression actually shrinks it), then encrypted
// with AES-256-GCM through internal/services/crypto (the credential-encryption
// pattern extended to stored content), then content-addressed by the SHA-256
// of the PLAINTEXT — so identical content dedups to one stored object no
// matter what the ciphertext bytes are (GCM nonces are random per write).
//
// Stored frame (self-describing; blob.Store holds it under the content key):
//
//	byte 0:    magic/version (codecFrameVersion)
//	byte 1:    flags — bit 0 set = payload is zstd-compressed
//	bytes 2..: crypto.EncryptBytes output — nonce || AES-256-GCM ciphertext
//
// Decode reverses the frame and verifies SHA-256(plaintext) == ref.Hash,
// reporting ErrBlobCorrupt otherwise. All failure paths surface as
// ErrBlobCorrupt so callers (OB-4/OB-5) can map them uniformly.
//
// Concurrency: the package-level zstd encoders are safe for concurrent use
// (klauspost/compress pools their internal state), so the zero value is ready
// and shared instances are safe.
//
// Data-key scope (documented limitation, SF-78 mailstore extension): the
// crypto service derives one AES-256 key from POSTA_ENCRYPTION_KEY per
// deployment; the BlobCodec interface carries no workspace context, so
// per-workspace data keys (ADR-003 wording) are NOT implemented in this wave —
// every workspace's blobs share the deployment key. Rotating the deployment
// key makes existing blobs undecryptable (they are not re-encrypted in place);
// that is the same key-rotation posture the credential store already has.
type ZstdBlobCodec struct{}

const (
	// codecFrameVersion is the stored-frame magic. Bump on any format change;
	// Decode rejects unknown versions instead of misparsing them.
	codecFrameVersion byte = 1
	// codecFlagCompressed marks a zstd-compressed payload.
	codecFlagCompressed byte = 1 << 0
	// compressionThreshold: bodies/attachments at or below this size are
	// stored uncompressed (ADR-003: zstd for bodies > 4 KiB only).
	compressionThreshold = 4 * 1024
)

var (
	zstdEncoder *zstd.Encoder
	zstdDecoder *zstd.Decoder
)

func init() {
	// EncoderConcurrency(1) keeps memory per encoder bounded; mailstore
	// appends are not throughput-critical enough to justify one goroutine per
	// core inside the encoder pool.
	zstdEncoder, _ = zstd.NewWriter(nil,
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderLevel(zstd.SpeedDefault))
	zstdDecoder, _ = zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1))
}

// Encode implements BlobCodec: compress (when it pays) → encrypt → address.
func (ZstdBlobCodec) Encode(plaintext []byte) ([]byte, BlobRef, error) {
	if len(plaintext) == 0 {
		return nil, BlobRef{}, ErrEmptyMessage
	}
	payload := plaintext
	flags := byte(0)
	if len(plaintext) > compressionThreshold {
		if compressed := zstdEncoder.EncodeAll(plaintext, nil); len(compressed) < len(plaintext) {
			payload = compressed
			flags |= codecFlagCompressed
		}
	}
	sealed, err := crypto.EncryptBytes(payload)
	if err != nil {
		return nil, BlobRef{}, fmt.Errorf("mailstore: encrypt blob payload: %w", err)
	}
	// 2-byte frame header + nonce||ciphertext.
	frame := make([]byte, 2, 2+len(sealed))
	frame[0] = codecFrameVersion
	frame[1] = flags
	frame = append(frame, sealed...)
	sum := sha256.Sum256(plaintext)
	return frame, BlobRef{Hash: hex.EncodeToString(sum[:]), Size: int64(len(plaintext))}, nil
}

// Decode implements BlobCodec: decrypt → decompress (per frame flag) → verify
// the content address. Any tampering, wrong key, or truncation collapses to
// ErrBlobCorrupt (GCM authenticates; the hash double-checks the plaintext).
func (ZstdBlobCodec) Decode(stored []byte, ref BlobRef) ([]byte, error) {
	if len(stored) < 2 {
		return nil, fmt.Errorf("%w: frame too short (%d bytes)", ErrBlobCorrupt, len(stored))
	}
	if stored[0] != codecFrameVersion {
		return nil, fmt.Errorf("%w: unknown frame version %d", ErrBlobCorrupt, stored[0])
	}
	flags := stored[1]
	if flags&^codecFlagCompressed != 0 {
		return nil, fmt.Errorf("%w: unknown frame flags 0x%02x", ErrBlobCorrupt, flags)
	}
	payload, err := crypto.DecryptBytes(stored[2:])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBlobCorrupt, err)
	}
	if flags&codecFlagCompressed != 0 {
		payload, err = zstdDecoder.DecodeAll(payload, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: zstd decompress: %v", ErrBlobCorrupt, err)
		}
	}
	if ref.Size > 0 && int64(len(payload)) != ref.Size {
		return nil, fmt.Errorf("%w: plaintext size %d != recorded %d", ErrBlobCorrupt, len(payload), ref.Size)
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != ref.Hash {
		return nil, ErrBlobCorrupt
	}
	return payload, nil
}
