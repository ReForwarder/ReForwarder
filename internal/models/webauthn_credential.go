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

package models

import "time"

// WebAuthnCredential is one registered passkey (R-19). The primary key is the
// base64url credential ID the WebAuthn protocol itself assigns — the same
// identifier a client's navigator.credentials lookup reports — so registration,
// listing and removal all key on the authenticator's own value. The public key
// is stored base64-encoded and never serialized (json:"-"), and the user-facing
// Name column backs the dashboard's rename action.
type WebAuthnCredential struct {
	// ID is the base64url credential ID (the WebAuthn credential record's `id`).
	ID string `json:"id" gorm:"primaryKey;size:1024"`
	// UserID owns the credential; a credential is only ever usable by its owner.
	UserID uint `json:"user_id" gorm:"index;not null"`
	// Name is the user-facing label ("MacBook Touch ID", …).
	Name string `json:"name"`
	// PublicKey is the credential public key, base64 (std) encoded of the raw
	// COSE key bytes. Never exposed through the API.
	PublicKey string `json:"-" gorm:"not null"`
	// AttestationType is the attestation type conveyed at registration
	// (none, basic, …).
	AttestationType string `json:"attestation_type"`
	// Transport lists the authenticator transports reported at registration
	// (usb, nfc, ble, internal, hybrid).
	Transport []string `json:"transport" gorm:"serializer:json"`
	// SignCount is the authenticator's assertion counter, kept for clone
	// detection — the WebAuthn library compares it on every login.
	SignCount uint32 `json:"sign_count"`
	// LastUsedAt records the most recent successful assertion.
	LastUsedAt *time.Time `json:"last_used_at"`
	// CreatedAt is the registration time.
	CreatedAt time.Time `json:"created_at"`
}
