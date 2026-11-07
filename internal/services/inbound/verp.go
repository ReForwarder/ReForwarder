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

package inbound

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"
)

// VERP return-path (OB-2, ADR-002 decision 4; parity CA-05, SF-10).
//
// When the feature is configured (POSTA_RETURN_PATH_SUBDOMAIN +
// POSTA_RETURN_PATH_SECRET), outbound direct-to-MX mail carries a VERP-encoded
// envelope sender:
//
//	bounce+<uuid-hex>=<sig>@<subdomain>.<sender-domain>
//
// <uuid-hex> is the outbound Email row's UUID with the hyphens stripped
// (32 lowercase hex chars) and <sig> is the first 20 hex chars (80 bits) of
// HMAC-SHA256(secret, "bounce+" + payload + "@" + host) — the full address is
// signed, so neither the payload nor the host can be swapped. The bounce
// address for one message is unique and unforgeable, so a DSN arriving at it
// can be matched back to exactly one sent message.
//
// Wire-format constraints:
//   - The local part stays within RFC 5321's 64-octet limit:
//     7 ("bounce+") + 32 + 1 ("=") + 20 = 60 octets.
//   - Everything is lowercase hex, so case-folding intermediaries cannot
//     corrupt the address.
//
// A nil *Verp or a config missing either value is the feature-off state: every
// method reports false / "" and callers keep today's behavior unchanged.

// VerpConfig carries the return-path configuration from the environment.
type VerpConfig struct {
	// Subdomain is the label prepended to sending domains ("bounce" →
	// bounce.mail.example.com). Empty disables the feature.
	Subdomain string
	// Secret keys the bounce-address HMAC. Required for the feature.
	Secret string
}

// Verp encodes and decodes VERP bounce addresses. Safe for concurrent use; a
// nil *Verp is a valid, disabled instance.
type Verp struct {
	subdomain string
	secret    string
}

const (
	// verpLocalPrefix starts every bounce address's local part.
	verpLocalPrefix = "bounce+"
	// verpUUIDHexLen is the payload length: a UUID without hyphens.
	verpUUIDHexLen = 32
	// verpSigHexLen is the truncated-HMAC signature length in hex chars
	// (80 bits of HMAC-SHA256 — unforgeable without the secret, and the
	// RCPT surface is rate-limited).
	verpSigHexLen = 20
)

// NewVerp builds the VERP codec from configuration. The returned value is
// disabled unless both the subdomain and the secret are set.
func NewVerp(cfg VerpConfig) *Verp {
	return &Verp{
		subdomain: strings.ToLower(strings.TrimSpace(cfg.Subdomain)),
		secret:    cfg.Secret,
	}
}

// Enabled reports whether the return-path feature is active.
func (v *Verp) Enabled() bool {
	return v != nil && v.subdomain != "" && v.secret != ""
}

// BounceDomain returns the bounce host for a sending domain:
// <subdomain>.<senderDomain>. Empty when the feature is off or the sender
// domain is unusable.
func (v *Verp) BounceDomain(senderDomain string) string {
	if !v.Enabled() {
		return ""
	}
	sd := verpDomain(strings.TrimSpace(senderDomain))
	if sd == "" {
		return ""
	}
	return v.subdomain + "." + sd
}

// EnvelopeFrom builds the VERP MAIL FROM for one outbound message.
// emailUUID is the Email row's UUID; senderAddress is the RFC 5322 From
// (bare or display form — the bare address is extracted here). Returns
// (plain envelope semantics unchanged, false) when the feature is off or the
// inputs cannot form a bounce address.
func (v *Verp) EnvelopeFrom(emailUUID, senderAddress string) (string, bool) {
	if !v.Enabled() {
		return "", false
	}
	payload, ok := verpUUIDPayload(emailUUID)
	if !ok {
		return "", false
	}
	bare := bareAddress(senderAddress)
	at := strings.LastIndex(bare, "@")
	if at < 0 || at == len(bare)-1 {
		return "", false
	}
	host := v.BounceDomain(bare[at+1:])
	if host == "" {
		return "", false
	}
	local := verpLocalPrefix + payload + "=" + verpSignature(v.secret, verpLocalPrefix+payload, host)
	return local + "@" + host, true
}

// Decode validates a bounce recipient address and returns the original
// message's UUID. Any tampering — payload, signature, or host — fails the
// HMAC and reports false.
func (v *Verp) Decode(address string) (string, bool) {
	if !v.Enabled() {
		return "", false
	}
	local, host, ok := splitAddress(strings.ToLower(strings.TrimSpace(address)))
	if !ok || !strings.HasPrefix(local, verpLocalPrefix) {
		return "", false
	}
	rest := local[len(verpLocalPrefix):]
	// The payload is exactly the UUID hex, so the separator position is fixed.
	if len(rest) != verpUUIDHexLen+1+verpSigHexLen || rest[verpUUIDHexLen] != '=' {
		return "", false
	}
	payload, sigHex := rest[:verpUUIDHexLen], rest[verpUUIDHexLen+1:]
	expected := verpSignature(v.secret, verpLocalPrefix+payload, host)
	if !hmac.Equal([]byte(expected), []byte(sigHex)) {
		return "", false
	}
	raw, err := hex.DecodeString(payload)
	if err != nil || len(raw) != 16 {
		return "", false
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), true
}

// BounceParentDomain strips the configured subdomain label from a bounce
// host, returning the sending domain it belongs to ("bounce.mail.example.com"
// → "mail.example.com"). Reports false for hosts that are not bounce hosts.
func (v *Verp) BounceParentDomain(host string) (string, bool) {
	if !v.Enabled() {
		return "", false
	}
	h := verpDomain(host)
	// The bounce host is "<subdomain>.<sending-domain>": the label is the
	// first dot-separated label, not the last.
	if !strings.HasPrefix(h, v.subdomain+".") {
		return "", false
	}
	parent := strings.TrimPrefix(h, v.subdomain+".")
	if parent == "" {
		return "", false
	}
	return parent, true
}

// LooksLikeBounce is the cheap syntactic pre-check used to decide whether a
// recipient must be offered to the return-path allowlist. It does not verify
// the signature — call Decode for that.
func (v *Verp) LooksLikeBounce(address string) bool {
	if !v.Enabled() {
		return false
	}
	local, host, ok := splitAddress(strings.ToLower(strings.TrimSpace(address)))
	return ok && strings.HasPrefix(local, verpLocalPrefix) && strings.HasPrefix(host, v.subdomain+".")
}

// verpUUIDPayload strips the hyphens from a UUID and validates the hex.
func verpUUIDPayload(emailUUID string) (string, bool) {
	p := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(emailUUID), "-", ""))
	if len(p) != verpUUIDHexLen {
		return "", false
	}
	if _, err := hex.DecodeString(p); err != nil {
		return "", false
	}
	return p, true
}

// verpSignature is the truncated address HMAC: first 20 hex chars of
// HMAC-SHA256(secret, local + "@" + host).
func verpSignature(secret, local, host string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(local + "@" + host))
	return hex.EncodeToString(mac.Sum(nil))[:verpSigHexLen]
}

// verpDomain normalizes a domain for comparison: lowercased, trimmed, and
// without a trailing dot.
func verpDomain(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	return strings.TrimSuffix(d, ".")
}

// bareAddress extracts the bare address of an RFC 5322 value for callers
// outside the email package (kept local to avoid an import cycle).
func bareAddress(value string) string {
	if addr, err := mail.ParseAddress(strings.TrimSpace(value)); err == nil {
		return addr.Address
	}
	return strings.TrimSpace(value)
}
