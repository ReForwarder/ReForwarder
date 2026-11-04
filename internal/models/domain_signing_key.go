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

package models

import "time"

// Domain signing lifecycle (roadmap R-02, corrected the reference platform semantics):
//
//	Status is the rotation lifecycle — new keys are created directly Active;
//	a key becomes Retiring once a replacement Active exists, and Retired
//	(then deletable) after the operator retires it. There is no "Pending"
//	stage between Active and Retiring.
//
//	DNS publication is tracked separately in DNSState: "pending-retry" is the
//	transient state of a record that has not been confirmed in DNS yet (the
//	rotate-DNS-verify loop), "published" means the TXT record was found.
//	Only Active + published keys sign mail — a signature whose public key is
//	not yet in DNS verifies as fail, which is worse than not signing.
//
//	Rotation is API-driven (POST .../signing-keys/rotate); the retiring key is
//	kept until manually retired. There is no scheduled migration task — a
//	documented simplification versus the reference platform's scheduled rotation.
const (
	DomainSigningStatusActive   = "active"
	DomainSigningStatusRetiring = "retiring"
	DomainSigningStatusRetired  = "retired"

	DomainSigningDNSPending   = "pending-retry"
	DomainSigningDNSPublished = "published"

	SigningAlgorithmRSA     = "rsa-sha256"
	SigningAlgorithmEd25519 = "ed25519-sha256"
)

// DomainSigningKey is a DKIM signing key for one verified domain. The private
// key is stored encrypted (crypto.Encrypt, AES-256-GCM at rest) and never
// leaves the server: API responses only ever carry the public DNS record.
type DomainSigningKey struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	UserID        uint   `json:"user_id" gorm:"index;not null"`
	WorkspaceID   *uint  `json:"workspace_id,omitempty" gorm:"index"`
	DomainID      uint   `json:"domain_id" gorm:"index;not null"`
	Selector      string `json:"selector" gorm:"not null"`
	Algorithm     string `json:"algorithm" gorm:"not null;default:rsa-sha256"`
	PrivateKeyPem string `json:"-" gorm:"type:text;not null"`
	Status        string `json:"status" gorm:"not null;default:active;index"`
	DNSState      string `json:"dns_state" gorm:"not null;default:pending-retry"`
	// DNSPublishedAt is set the first time the TXT record was confirmed in DNS.
	DNSPublishedAt *time.Time `json:"dns_published_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	Domain Domain `json:"-" gorm:"foreignKey:DomainID"`
}

// CanSign reports whether the key should be used to sign outbound mail.
func (k *DomainSigningKey) CanSign() bool {
	return k.Status == DomainSigningStatusActive && k.DNSState == DomainSigningDNSPublished
}

// DNSHost is the fully-qualified name the TXT record must be published at.
func (k *DomainSigningKey) DNSHost(domainName string) string {
	return k.Selector + "._domainkey." + domainName
}
