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

// InboundRouteAction is what the routing engine does with a recipient that
// matches a route (ADR-002 §1). Routing rules are data: they are evaluated at
// RCPT time (before the receiver accepts responsibility for the message) and
// their action is executed only AFTER the message is durably persisted.
type InboundRouteAction string

const (
	// InboundRouteActionDeliver keeps today's archive/thread behavior: the
	// message is stored for the domain's inbound view and processed by the
	// regular parse/notify pipeline. No extra side effect.
	InboundRouteActionDeliver InboundRouteAction = "deliver"
	// InboundRouteActionForward queues a durable forwarding task row
	// (inbound_forward_tasks) consumed by the forwarding executor. Forwarding
	// never touches the marketing/consent layers (ADR-002 non-negotiable).
	InboundRouteActionForward InboundRouteAction = "forward"
	// InboundRouteActionWebhook POSTs the message to the workspace's
	// registered webhooks (subscribed to the route's target event) through
	// the existing HMAC webhook machinery.
	InboundRouteActionWebhook InboundRouteAction = "webhook"
	// InboundRouteActionQuarantine holds the message for review: the inbound
	// row moves to the quarantined status and a quarantine row is created via
	// the quarantine seam.
	InboundRouteActionQuarantine InboundRouteAction = "quarantine"
)

// InboundRoute is one routing rule (ADR-002 decision 1): a pattern evaluated
// against an envelope recipient of a tenant-verified domain, and the action to
// take. Rules are per-tenant data — they resolve only through domains the
// workspace has verified ownership of.
type InboundRoute struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// UserID is the owning tenant (denormalized from the domain for
	// attribution); WorkspaceID scopes the rule like every other resource.
	UserID      uint  `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint `json:"workspace_id,omitempty" gorm:"index"`
	// DomainID references the tenant-verified domain the rule belongs to.
	DomainID uint `json:"domain_id" gorm:"index;not null"`
	// Pattern is the recipient spec the rule matches:
	//   - "local@domain"  exact address (also the sub-address spec form:
	//     "user+tag@domain" matches exactly that tagged address);
	//   - "@domain"       catch-all for the domain (fallback for recipients
	//     that matched no specific rule — the explicit unknown-recipient
	//     policy: without it, unknown recipients are rejected).
	Pattern string `json:"pattern" gorm:"size:320;not null"`
	// Action is one of the InboundRouteAction values above.
	Action InboundRouteAction `json:"action" gorm:"size:20;not null"`
	// Target is action-specific: the destination address for forward, the
	// webhook event name the route triggers for webhook (empty selects the
	// established "email.inbound" event), unused for deliver/quarantine.
	Target string `json:"target,omitempty" gorm:"size:320"`
	// Priority orders evaluation: lower value = evaluated first. Exact and
	// sub-address patterns always beat the catch-all regardless of priority
	// (the catch-all is the fallback for unmatched recipients).
	Priority int  `json:"priority" gorm:"not null;default:100"`
	Enabled  bool `json:"enabled" gorm:"not null;default:true"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Domain Domain `json:"-" gorm:"foreignKey:DomainID;constraint:OnDelete:CASCADE"`
}

// TableName overrides GORM's default table naming.
func (InboundRoute) TableName() string { return "inbound_routes" }

// IsCatchAll reports whether the pattern is the domain's catch-all spec.
func (r InboundRoute) IsCatchAll() bool {
	return len(r.Pattern) > 0 && r.Pattern[0] == '@'
}
