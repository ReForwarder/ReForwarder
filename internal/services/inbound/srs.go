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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package inbound

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Sender rewrite scheme (SF-12, the ADR-001 amendment): the bounded SRS for
// forwarded mail. A forwarder copy's envelope sender is an encoded address on
// the forwarder's own domain carrying the ORIGINAL inbound message's identity,
// signed with the existing bounce secret:
//
//	srs0+<payload32hex>=<sig16hex>@<forwarder-domain>
//
// The payload is the inbound record's UUID without hyphens (32 lowercase hex
// chars) — the forwarded original the copy was made from; the signature is
// the first 16 hex chars (64 bits) of HMAC-SHA256(secret, "srs0+" + payload +
// "@" + host) — the full address is signed, so neither the payload nor the
// host can be swapped. The local part stays within RFC 5321's 64-octet limit
// (6 + 32 + 1 + 16 = 55) and is all lowercase. Bounces arriving at the
// address verify + decode to the inbound UUID for the RFC 3464 match-back.
// The codec rides the Verp instance (the same bounce secret, the same HMAC
// construction as verpSignature); a nil or disabled Verp disables SRS too.

const (
	// srsLocalPrefix starts every forwarded-mail envelope sender.
	srsLocalPrefix = "srs0+"
	// srsPayloadHexLen is the payload length: an inbound UUID without
	// hyphens (the VERP payload length — the same 32-hex shape).
	srsPayloadHexLen = verpUUIDHexLen
	// srsSigHexLen is the truncated-HMAC signature length in hex chars
	// (64 bits of HMAC-SHA256 with the bounce secret; the RCPT surface is
	// rate-limited).
	srsSigHexLen = 16
)

// EncodeForwarded builds the SRS envelope sender for one forwarded copy:
// the encoded address on the forwarder domain carrying the original inbound
// message's identity. Returns ("", false) when the feature is off or the
// inbound UUID is unusable.
func (v *Verp) EncodeForwarded(inboundUUID, forwarderDomain string) (string, bool) {
	if !v.Enabled() {
		return "", false
	}
	payload, ok := verpUUIDPayload(inboundUUID)
	if !ok {
		return "", false
	}
	host := verpDomain(forwarderDomain)
	if host == "" {
		return "", false
	}
	local := srsLocalPrefix + payload + "=" + srsSignature(v.secret, srsLocalPrefix+payload, host)
	return local + "@" + host, true
}

// DecodeForwarded validates an srs0+ bounce recipient and returns the
// original inbound message's UUID. Any tampering — payload, signature, or
// host — fails the HMAC and reports false.
func (v *Verp) DecodeForwarded(address string) (string, bool) {
	if !v.Enabled() {
		return "", false
	}
	local, host, ok := splitAddress(strings.ToLower(strings.TrimSpace(address)))
	if !ok || !strings.HasPrefix(local, srsLocalPrefix) {
		return "", false
	}
	rest := local[len(srsLocalPrefix):]
	if len(rest) != srsPayloadHexLen+1+srsSigHexLen || rest[srsPayloadHexLen] != '=' {
		return "", false
	}
	payload, sigHex := rest[:srsPayloadHexLen], rest[srsPayloadHexLen+1:]
	expected := srsSignature(v.secret, srsLocalPrefix+payload, host)
	if !hmac.Equal([]byte(expected), []byte(sigHex)) {
		return "", false
	}
	raw, err := hex.DecodeString(payload)
	if err != nil || len(raw) != 16 {
		return "", false
	}
	return strings.Join([]string{
		hex.EncodeToString(raw[0:4]),
		hex.EncodeToString(raw[4:6]),
		hex.EncodeToString(raw[6:8]),
		hex.EncodeToString(raw[8:10]),
		hex.EncodeToString(raw[10:16]),
	}, "-"), true
}

// LooksLikeForwardedBounce is the cheap syntactic pre-check for the srs0+
// shape (the local-part prefix only — the host is the forwarder's own
// domain, no subdomain label). It does not verify the signature — call
// DecodeForwarded for that.
func (v *Verp) LooksLikeForwardedBounce(address string) bool {
	if !v.Enabled() {
		return false
	}
	local, _, ok := splitAddress(strings.ToLower(strings.TrimSpace(address)))
	return ok && strings.HasPrefix(local, srsLocalPrefix)
}

// srsSignature is the truncated address HMAC: first 16 hex chars of
// HMAC-SHA256(secret, local + "@" + host) — the VERP construction with the
// bounce secret and the SRS local prefix.
func srsSignature(secret, local, host string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(local + "@" + host))
	return hex.EncodeToString(mac.Sum(nil))[:srsSigHexLen]
}


