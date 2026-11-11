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

// DNS provider kinds accepted by the DNS automation layer (ADR-007 decision 1:
// Phase 6 adapters for Cloudflare, Route53 and DigitalOcean; the DNSProvider
// interface is the contract, adapters are additive).
const (
	DNSProviderCloudflare   = "cloudflare"
	DNSProviderRoute53      = "route53"
	DNSProviderDigitalOcean = "digitalocean"
)

// DNSProviderCredential holds one workspace's API credentials for the DNS
// provider managing a domain's records (OB-6, ADR-007 decision 2). The
// credential material is stored encrypted (crypto.Encrypt, AES-256-GCM at
// rest — same discipline as SMTP relay credentials and DKIM private keys) and
// NEVER leaves the server: API responses carry only a masked fingerprint.
//
// Ownership follows the domain: UserID + WorkspaceID mirror models.Domain so
// every query path can enforce the workspace boundary (ADR-008 decision 1).
// One credential per domain — connecting again replaces the stored one.
type DNSProviderCredential struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	UserID      uint   `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint  `json:"workspace_id,omitempty" gorm:"index"`
	// DomainID is unique: one provider connection per domain (reconnecting
	// replaces the stored credential).
	DomainID    uint   `json:"domain_id" gorm:"uniqueIndex;not null"`
	Provider    string `json:"provider" gorm:"size:32;not null"`
	// Credential is the encrypted JSON envelope of the provider's secret
	// material ({"token":...} for bearer providers, access/secret key pair for
	// Route53). json:"-" so it can never be serialized to a response body.
	Credential string `json:"-" gorm:"type:text;not null"`
	// ZoneID caches the provider-side zone identifier resolved at connect
	// time (Cloudflare zone id, Route53 hosted zone id, DigitalOcean domain
	// name) so publish/drift calls skip a lookup round trip.
	ZoneID string `json:"zone_id" gorm:"size:255"`
	// LastVerifiedAt records the newest successful read-only access check.
	LastVerifiedAt *time.Time `json:"last_verified_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	Domain Domain `json:"-" gorm:"foreignKey:DomainID"`
}

// DNSPublicationState records what the DNS automation layer last wrote for a
// domain and how its live DNS compares to what was expected (OB-6 drift
// re-verification, ADR-007 decision 1 + GAP-08 residual). One row per domain.
type DNSPublicationState struct {
	ID       uint   `json:"id" gorm:"primaryKey"`
	UserID   uint   `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint `json:"workspace_id,omitempty" gorm:"index"`
	DomainID uint   `json:"domain_id" gorm:"uniqueIndex;not null"`
	// PublishedRecords is the JSON array of DNS record specs the last
	// successful publish applied (the drift baseline). Empty when nothing was
	// ever published through the automation layer.
	PublishedRecords string     `json:"published_records" gorm:"type:text"`
	PublishedAt      *time.Time `json:"published_at,omitempty"`
	// PublishError holds the typed failure message of the newest failed
	// publish attempt; cleared by the next success.
	PublishError string `json:"publish_error" gorm:"type:text"`
	// DriftStatus: "unknown" (never compared), "ok", or "drift".
	DriftStatus string `json:"drift_status" gorm:"size:16;not null;default:unknown"`
	// DriftDetail is the JSON array of drift findings behind DriftStatus.
	DriftDetail      string     `json:"drift_detail" gorm:"type:text"`
	LastDriftCheckAt *time.Time `json:"last_drift_check_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`

	Domain Domain `json:"-" gorm:"foreignKey:DomainID"`
}

// Drift status values stored on DNSPublicationState.DriftStatus.
const (
	DNSDriftUnknown = "unknown"
	DNSDriftOK      = "ok"
	DNSDriftFound   = "drift"
)
