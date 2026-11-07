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

// InboundQuarantineStatus is the review state of one quarantined message
// (ADR-002 §3): held for review, put back through routing, or discarded.
type InboundQuarantineStatus string

const (
	// InboundQuarantinePending — the message is held; awaiting operator review.
	InboundQuarantinePending InboundQuarantineStatus = "pending"
	// InboundQuarantineReleased — an operator released the message; routing was
	// re-run at that moment (the row is a review record from then on).
	InboundQuarantineReleased InboundQuarantineStatus = "released"
	// InboundQuarantineDeleted — an operator permanently discarded the message.
	// The inbound email row and its blobs are gone; this row (plus the audit
	// event) is the surviving record that it happened.
	InboundQuarantineDeleted InboundQuarantineStatus = "deleted"
)

// InboundQuarantine is one quarantine review record (ADR-002 decision 3,
// SF-51). It references the durably stored inbound email — the message itself
// never moves; the row records the hold, the disposition, and who took it.
type InboundQuarantine struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// UserID is the owning tenant (denormalized from the domain for
	// attribution); WorkspaceID scopes the record like every other resource.
	UserID      uint  `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint `json:"workspace_id,omitempty" gorm:"index"`
	// DomainID references the tenant-verified domain the message arrived on.
	DomainID uint `json:"domain_id" gorm:"index;not null"`
	// InboundEmailID references the quarantined inbound_emails row. Multiple
	// records may reference the same message over its life (quarantined →
	// released → quarantined again by re-routing); only one is ever pending.
	InboundEmailID uint `json:"inbound_email_id" gorm:"index;not null"`
	// RouteID records the routing rule that quarantined the message (the
	// decision's provenance, mirroring the inbound row's route stamp). NULL
	// when the quarantine was not route-driven.
	RouteID *uint `json:"route_id,omitempty" gorm:"index"`
	// Reason is the human-readable quarantine cause (e.g. "matched quarantine
	// route").
	Reason string `json:"reason" gorm:"size:255;not null;default:''"`
	// Score carries the message's spam/verification score at quarantine time,
	// when one is known (NULL otherwise).
	Score *float64 `json:"score,omitempty"`
	// Status is one of the InboundQuarantineStatus values above.
	Status InboundQuarantineStatus `json:"status" gorm:"size:20;not null;default:pending;index"`
	// ReceivedAt mirrors the message's arrival (the retention window keys on
	// it, not on the row's creation time).
	ReceivedAt time.Time `json:"received_at" gorm:"not null"`
	// ReviewedBy / ReviewedAt record the release or deletion disposition and
	// who took it. NULL while pending.
	ReviewedBy *uint      `json:"reviewed_by,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Domain Domain `json:"-" gorm:"foreignKey:DomainID;constraint:OnDelete:CASCADE"`
}

// TableName overrides GORM's default table naming (ADR-002: additive table
// `inbound_quarantine`).
func (InboundQuarantine) TableName() string { return "inbound_quarantine" }
