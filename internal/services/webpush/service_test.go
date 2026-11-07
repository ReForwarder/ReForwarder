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
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// testSubscription generates the browser side of a subscription: the keys a
// real PushSubscriptionJSON would carry, plus the private key the test uses
// to DECRYPT what the server sends (proving the derivation from both roles).
func testSubscription(t *testing.T) (uaPriv *ecdh.PrivateKey, p256dh, auth string) {
	t.Helper()
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate UA key: %v", err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatalf("generate auth secret: %v", err)
	}
	return uaPriv,
		base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(authSecret)
}

// mustB64 decodes unpadded base64url.
func mustB64(s string) []byte {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		panic(fmt.Sprintf("test base64url decode: %v", err))
	}
	return raw
}

// gcmOpen decrypts one record: AES-128-GCM with the derived CEK and nonce,
// empty AAD (the aes128gcm coding defines none).
func gcmOpen(t *testing.T, cek, nonce, body []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("GCM: %v", err)
	}
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return plain
}

// decryptAES128GCM is the browser-side decryption (RFC 8291 §5): the mirror
// of encodeAES128GCM. It derives the same CEK/nonce from the UA private key
// and the server's ephemeral public key carried in the header.
func decryptAES128GCM(t *testing.T, uaPriv *ecdh.PrivateKey, auth, payload []byte) []byte {
	t.Helper()
	if len(payload) < aes128gcmHeaderLen {
		t.Fatalf("payload shorter than the aes128gcm header: %d", len(payload))
	}
	rs := big.NewInt(int64(uint32(payload[16])<<24 | uint32(payload[17])<<16 | uint32(payload[18])<<8 | uint32(payload[19])))
	if rs.Int64() != recordSize {
		t.Logf("record size header = %d (want %d)", rs.Int64(), recordSize)
	}
	idLen := int(payload[20])
	asPubRaw := payload[21 : 21+idLen]
	body := payload[21+idLen:]

	asPub, err := ecdh.P256().NewPublicKey(asPubRaw)
	if err != nil {
		t.Fatalf("server ephemeral key: %v", err)
	}
	shared, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatalf("UA-side ECDH: %v", err)
	}

	ikm := hkdfKey(auth,
		concat(shared, uaPriv.PublicKey().Bytes(), asPubRaw),
		concat([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes(), asPubRaw),
		32)
	cek := hkdfKey(make([]byte, 16), ikm, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfKey(make([]byte, 12), ikm, []byte("Content-Encoding: nonce\x00"), 12)

	plaintext := gcmOpen(t, cek, nonce, body)
	if len(plaintext) == 0 || plaintext[len(plaintext)-1] != 0x02 {
		t.Fatalf("missing 0x02 padding delimiter: %v", plaintext)
	}
	return plaintext[:len(plaintext)-1]
}

func TestEncodeAES128GCMRoundTripDecryptsOnSubscriptionSide(t *testing.T) {
	uaPriv, p256dh, auth := testSubscription(t)
	uaPub, err := ecdh.P256().NewPublicKey(mustB64(p256dh))
	if err != nil {
		t.Fatalf("decode p256dh: %v", err)
	}
	authSecret := mustB64(auth)

	plain := []byte(`{"type":"message.replied","message":"ws:1"}`)
	payload, err := encodeAES128GCM(uaPub, authSecret, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	got := decryptAES128GCM(t, uaPriv, authSecret, payload)
	if string(got) != string(plain) {
		t.Fatalf("round trip plaintext = %q, want %q", got, plain)
	}
}

func TestEncodeAES128GCMUniqueEphemeralPerMessage(t *testing.T) {
	uaPub, err := ecdh.P256().NewPublicKey(mustB64(func() string {
		_, p, _ := testSubscription(t)
		return p
	}()))
	if err != nil {
		t.Fatal(err)
	}
	a1, err := encodeAES128GCM(uaPub, make([]byte, 16), []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	a2, err := encodeAES128GCM(uaPub, make([]byte, 16), []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a1) == string(a2) {
		t.Fatal("two encryptions of the same plaintext must differ (fresh ephemeral + salt)")
	}
}

func TestVAPIDAuthorizationSignsAndVerifies(t *testing.T) {
	pubB64, privB64, err := GenerateKeys()
	if err != nil {
		t.Fatalf("generate keys: %v", err)
	}
	k, err := loadVAPIDKey(pubB64, privB64)
	if err != nil {
		t.Fatalf("load keys: %v", err)
	}

	endpoint := "https://fcm.googleapis.com/fcm/send/abc"
	authz, err := k.vapidAuthorization(endpoint, "mailto:ops@posta.dev")
	if err != nil {
		t.Fatalf("vapidAuthorization: %v", err)
	}
	if !strings.HasPrefix(authz, "vapid t=") || !strings.Contains(authz, ", k=") {
		t.Fatalf("authorization header shape wrong: %q", authz)
	}

	// Parse `vapid t=<JWT>, k=<key>` and verify the JWT by hand.
	parts := strings.SplitN(authz, "t=", 2)
	jwtPart := strings.SplitN(parts[1], ",", 2)[0]
	segments := strings.Split(jwtPart, ".")
	if len(segments) != 3 {
		t.Fatalf("JWT must have 3 segments, got %d", len(segments))
	}

	headerRaw, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		t.Fatalf("header b64: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		t.Fatalf("header JSON: %v", err)
	}
	if header["alg"] != "ES256" || header["typ"] != "JWT" {
		t.Fatalf("header = %v, want alg ES256 typ JWT", header)
	}

	claimsRaw, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		t.Fatalf("claims b64: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatalf("claims JSON: %v", err)
	}
	if claims["aud"] != "https://fcm.googleapis.com" {
		t.Fatalf("aud = %v, want the endpoint origin", claims["aud"])
	}
	if claims["sub"] != "mailto:ops@posta.dev" {
		t.Fatalf("sub = %v", claims["sub"])
	}
	exp, ok := claims["exp"].(float64)
	if !ok || time.Unix(int64(exp), 0).Before(time.Now()) {
		t.Fatalf("exp = %v, want a future timestamp", claims["exp"])
	}

	// Verify the ES256 signature with the public key (raw r||s → verify).
	sig, err := base64.RawURLEncoding.DecodeString(segments[2])
	if err != nil {
		t.Fatalf("sig b64: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("ES256 signature must be raw 64 bytes, got %d", len(sig))
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), mustB64(pubB64))
	if x == nil {
		t.Fatal("public key is not on P-256")
	}
	digest := sha256.Sum256([]byte(segments[0] + "." + segments[1]))
	if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:],
		new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("VAPID JWT signature does not verify against the public key")
	}
}

func TestLoadVAPIDKeyRejectsMismatchedPair(t *testing.T) {
	_, privB64, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	otherPub, _, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadVAPIDKey(otherPub, privB64); err == nil {
		t.Fatal("a public key that does not match the private scalar must be rejected")
	}
	if _, err := loadVAPIDKey("", ""); err == nil {
		t.Fatal("an empty private key must be rejected")
	}
}

func TestLoadVAPIDKeyRejectsRubbish(t *testing.T) {
	// Not base64url at all.
	if _, err := loadVAPIDKey("", "!!!not base64!!!"); err == nil {
		t.Fatal("non-base64url private key must be rejected")
	}
	// Valid base64url but not 32 bytes.
	if _, err := loadVAPIDKey("", base64.RawURLEncoding.EncodeToString(make([]byte, 8))); err == nil {
		t.Fatal("short private key must be rejected")
	}
}

// TestSendEndToEnd posts to a local push service that acts as the browser:
// it verifies the RFC 8030 headers, the VAPID authorization, decrypts the
// RFC 8291 body, and answers the status codes the protocol defines.
func TestSendEndToEnd(t *testing.T) {
	uaPriv, p256dh, auth := testSubscription(t)
	authSecret := mustB64(auth)

	var gotTTL, gotEncoding, gotAuthz string
	var gotPayload []byte
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTTL = r.Header.Get("TTL")
		gotEncoding = r.Header.Get("Content-Encoding")
		gotAuthz = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotPayload = buf
		w.WriteHeader(status)
	}))
	defer srv.Close()

	pubB64, privB64, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{PublicKey: pubB64, PrivateKey: privB64, Subject: "mailto:test@posta.dev"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sub := &models.UserPushSubscription{
		Endpoint: srv.URL,
		P256DH:   p256dh,
		Auth:     auth,
	}

	status = http.StatusOK
	err = svc.Send(sub, Notification{Type: "message.replied", WorkspaceID: 3, Message: "ws:3"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotTTL != "300" {
		t.Fatalf("TTL = %q, want 300", gotTTL)
	}
	if gotEncoding != "aes128gcm" {
		t.Fatalf("Content-Encoding = %q", gotEncoding)
	}
	if !strings.HasPrefix(gotAuthz, "vapid t=") {
		t.Fatalf("Authorization = %q", gotAuthz)
	}
	plain := decryptAES128GCM(t, uaPriv, authSecret, gotPayload)
	var n Notification
	if err := json.Unmarshal(plain, &n); err != nil {
		t.Fatalf("decrypted body: %v", err)
	}
	if n.Type != "message.replied" || n.WorkspaceID != 3 {
		t.Fatalf("decrypted notification = %+v", n)
	}

	// 410 tells the dispatcher the subscription is dead.
	status = http.StatusGone
	err = svc.Send(sub, Notification{Type: "message.replied"})
	if err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("410 must map to a gone error, got %v", err)
	}

	// Other errors stay ordinary errors.
	status = http.StatusTeapot
	err = svc.Send(sub, Notification{Type: "message.replied"})
	if err == nil || strings.Contains(err.Error(), "gone") {
		t.Fatalf("418 must not map to gone, got %v", err)
	}
}
