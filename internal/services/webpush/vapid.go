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

package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"time"
)

// vapidTokenTTL is how long one VAPID JWT stays valid. RFC 8292 allows fresh
// tokens per request; a 12h window keeps the signing cost negligible while
// staying well inside what push services accept.
const vapidTokenTTL = 12 * time.Hour

// vapidKeyPair is the application server's long-lived RFC 8292 identity.
type vapidKeyPair struct {
	priv      *ecdsa.PrivateKey
	publicRaw []byte // 65-byte uncompressed P-256 point, base64url in the header
}

// GenerateKeys returns a fresh (publicB64, privateB64) VAPID key pair for
// POSTA_WEB_PUSH_VAPID_PUBLIC_KEY / POSTA_WEB_PUSH_VAPID_PRIVATE_KEY: both
// base64url without padding, the private key the 32-byte scalar, the public
// key the 65-byte uncompressed P-256 point.
func GenerateKeys() (publicB64, privateB64 string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("webpush: generate VAPID key: %w", err)
	}
	privRaw := key.D.FillBytes(make([]byte, 32))
	pubRaw := elliptic.Marshal(elliptic.P256(), key.PublicKey.X, key.PublicKey.Y)
	return base64.RawURLEncoding.EncodeToString(pubRaw),
		base64.RawURLEncoding.EncodeToString(privRaw),
		nil
}

// loadVAPIDKey builds the key pair from the configured public and private
// base64url values. The public key is re-derived from the private scalar and
// must match the configured one when both are provided — a mismatch means the
// push service will reject every push with a 403.
func loadVAPIDKey(publicB64, privateB64 string) (*vapidKeyPair, error) {
	privRaw, err := base64.RawURLEncoding.DecodeString(privateB64)
	if err != nil {
		return nil, fmt.Errorf("webpush: VAPID private key must be base64url: %w", err)
	}
	if len(privRaw) != 32 {
		return nil, fmt.Errorf("webpush: VAPID private key must be 32 bytes, got %d", len(privRaw))
	}

	curve := elliptic.P256()
	x, y := curve.ScalarBaseMult(privRaw)
	priv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y},
		D:         new(big.Int).SetBytes(privRaw),
	}

	derived := elliptic.Marshal(curve, x, y)
	if publicB64 != "" {
		pubRaw, err := base64.RawURLEncoding.DecodeString(publicB64)
		if err != nil {
			return nil, fmt.Errorf("webpush: VAPID public key must be base64url: %w", err)
		}
		if len(pubRaw) != 65 {
			return nil, fmt.Errorf("webpush: VAPID public key must be the 65-byte uncompressed point, got %d bytes", len(pubRaw))
		}
		if !equalBytes(pubRaw, derived) {
			return nil, fmt.Errorf("webpush: VAPID public key does not match the private key")
		}
	}
	return &vapidKeyPair{priv: priv, publicRaw: derived}, nil
}

// vapidAuthorization builds the RFC 8292 Authorization header value for one
// push endpoint: `vapid t=<JWT>, k=<base64url public key>`.
func (k *vapidKeyPair) vapidAuthorization(endpoint, subject string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("webpush: endpoint URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("webpush: endpoint URL has no scheme/host")
	}

	header := base64urlJSON(map[string]string{"typ": "JWT", "alg": "ES256"})
	claims := base64urlJSON(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": time.Now().Add(vapidTokenTTL).Unix(),
		"sub": subject,
	})
	signingInput := header + "." + claims

	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, digest[:])
	if err != nil {
		return "", fmt.Errorf("webpush: VAPID sign: %w", err)
	}
	// JWS ES256 wants raw r||s (32 bytes each), not ASN.1 DER.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	return fmt.Sprintf("vapid t=%s.%s, k=%s",
		signingInput,
		base64.RawURLEncoding.EncodeToString(sig),
		base64.RawURLEncoding.EncodeToString(k.publicRaw)), nil
}

// base64urlJSON marshals v and encodes it unpadded base64url.
func base64urlJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Both payload shapes are maps of strings and ints; marshaling cannot
		// fail. A panic here would be a programming error.
		panic(fmt.Sprintf("webpush: marshal VAPID JSON: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
