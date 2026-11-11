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

// Per-domain outbound delivery modes (ADR-001 §2). 'relay' (the default) sends
// through the configured trusted SMTP relay exactly as before; 'direct' hands
// mail from this domain to the outbound MTA executor for direct-to-MX
// delivery.
const (
	DeliveryModeRelay  = "relay"
	DeliveryModeDirect = "direct"
)

// Per-domain MTA-STS policy modes for hosted domains (SF-29, ADR-007 §5,
// RFC 8461 §4.2). The mode is published in the domain's HTTPS policy
// (mta-sts.<domain>/.well-known/mta-sts.txt): 'none' is the monitoring
// posture (a policy is served, violations are only reportable), 'testing'
// announces enforce semantics while failures are still delivered, 'enforce'
// asks sending MTAs to refuse unsecured delivery. The empty value (the column
// default until the operator opts in) means no policy is served at all.
const (
	MTASTSModeNone    = "none"
	MTASTSModeTesting = "testing"
	MTASTSModeEnforce = "enforce"
)

type Domain struct {
	ID                uint   `json:"id" gorm:"primaryKey"`
	UserID            uint   `json:"user_id" gorm:"index;not null"`
	WorkspaceID       *uint  `json:"workspace_id,omitempty" gorm:"index"`
	Domain            string `json:"domain" gorm:"not null"`
	OwnershipVerified bool   `json:"ownership_verified" gorm:"default:false"`
	SPFVerified       bool   `json:"spf_verified" gorm:"default:false"`
	DKIMVerified      bool   `json:"dkim_verified" gorm:"default:false"`
	DMARCVerified     bool   `json:"dmarc_verified" gorm:"default:false"`
	VerificationToken string `json:"verification_token" gorm:"not null"`

	// OB-1 outbound MTA (ADR-001): per-domain delivery configuration. The
	// columns are additive (default 'relay' = today's behavior); the PATCH
	// route is wired separately by the coordinator wave.
	//
	// DeliveryMode selects the executor ('relay' | 'direct').
	// OutboundBindIP pins the source address of direct deliveries (single IP
	// in this phase; PTR guidance for it is surfaced in domain health).
	// MaxMsgsPerConnection and HourlyBudget feed the per-domain sending
	// policy; 0 means the built-in default / unlimited respectively.
	DeliveryMode         string `json:"delivery_mode" gorm:"size:16;not null;default:'relay'"`
	OutboundBindIP       string `json:"outbound_bind_ip" gorm:"size:64"`
	MaxMsgsPerConnection int    `json:"max_msgs_per_connection" gorm:"default:0"`
	HourlyBudget         int    `json:"hourly_budget" gorm:"default:0"`
	// PoolID assigns the domain to an IP pool (OB-6 W3, ADR-007 decision 7):
	// direct deliveries rotate across the pool's enabled member IPs instead
	// of OutboundBindIP alone. nil = the domain uses OutboundBindIP alone.
	// The referenced pool must be visible to the domain's workspace —
	// enforced at the assignment endpoint, not here.
	PoolID *uint `json:"pool_id,omitempty" gorm:"index"`

	// MTASTSMode publishes the domain's MTA-STS policy mode (SF-29, OB-6 W4,
	// ADR-007 §5): '' (default; no policy served) | 'none' | 'testing' |
	// 'enforce'. The HTTPS policy path (mta-sts.<domain>/.well-known/
	// mta-sts.txt) renders it together with the domain's live MX hosts; the
	// companion _smtp._tls TXT record guidance (TLS-RPT rua) lives in the
	// domain DNS-records surface.
	MTASTSMode string `json:"mta_sts_mode" gorm:"column:mta_sts_mode;size:16;not null;default:''"`

	CreatedAt time.Time `json:"created_at"`

	User User `json:"-" gorm:"foreignKey:UserID"`
}

// IsOwnershipVerified returns true when domain ownership has been confirmed via TXT record.
func (d *Domain) IsOwnershipVerified() bool {
	return d.OwnershipVerified
}

// IsFullyVerified returns true when ownership is confirmed and all DNS checks pass.
func (d *Domain) IsFullyVerified() bool {
	return d.OwnershipVerified && d.SPFVerified && d.DKIMVerified && d.DMARCVerified
}
