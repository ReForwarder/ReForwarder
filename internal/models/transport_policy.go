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

// Transport policy rules (SF-50, ADR-001 amendment): per-domain
// condition-driven overrides of the per-domain sending policy, evaluated at
// the MTA path's gate points — RCPT (the gated submission listener: a
// matching reject policy refuses the recipient) and delivery (the
// direct-to-MX executor: the matching policy's overrides merge into the
// resolved per-domain policy for that delivery). Conditions are envelope
// facts (mail_from, rcpt_to, helo, authenticated_as, sender_domain,
// message_size) evaluated through the unified condition evaluator's
// transport currency, which is strictly fail-closed: an unknown field or
// operator is an evaluator ERROR, and the callers answer the conservative
// outcome (a temporary 451 at RCPT; a deferred send at delivery) rather
// than slipping past a gate. Precedence: priority ASC then id ASC, the
// first matching enabled policy wins, later policies do not stack.
type TransportPolicy struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// WorkspaceID — the tenant the policy belongs to (NOT NULL: policies
	// are workspace-scoped; system mail carries no workspace and matches
	// nothing). The bound sending domain must belong to this workspace.
	WorkspaceID uint `json:"workspace_id" gorm:"not null;index"`
	// DomainName is the SENDING domain the policy binds to (the MSA's
	// verified sender domain / the executor's sender domain). Bound by name:
	// the MTA path carries the domain name, and the admin surface validates
	// the name belongs to the owning workspace at write time.
	DomainName string `json:"domain_name" gorm:"size:255;not null;index"`
	Name       string `json:"name" gorm:"size:255;not null"`
	// Priority orders evaluation (lower evaluated first; first match wins).
	Priority int `json:"priority" gorm:"not null;default:100"`
	Enabled  bool `json:"enabled" gorm:"not null;default:true"`
	// Conditions is the envelope-fact rule set (models.FilterRules JSON —
	// the FilterRule shape reused for its Field/Operator/Value triple; the
	// transport currency owns the field/operator whitelist and the strict
	// error semantics). An empty set matches every message.
	Conditions FilterRules `json:"conditions" gorm:"type:text"`

	// ── The route overrides (zero value = untouched) ──
	// Reject refuses the recipient at RCPT with RejectReason (550 5.7.1).
	Reject       bool   `json:"reject" gorm:"not null;default:false"`
	RejectReason string `json:"reject_reason" gorm:"size:255"`
	// BindIP pins the outbound source address for matching deliveries.
	BindIP string `json:"bind_ip" gorm:"size:64"`
	// MsgRatePerMin overrides the per-message send-rate pacing (CA-04).
	MsgRatePerMin int `json:"msg_rate_per_min" gorm:"not null;default:0"`
	// MaxMessageSize overrides the outbound size cap (octets; 0 = untouched).
	MaxMessageSize int64 `json:"max_message_size" gorm:"not null;default:0"`
	// HourlyBudget overrides the per-hour delivery budget (0 = untouched).
	HourlyBudget int `json:"hourly_budget" gorm:"not null;default:0"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (TransportPolicy) TableName() string { return "transport_policies" }
