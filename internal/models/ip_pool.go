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

// IP pool member selection strategies (OB-6 W3, ADR-007 decision 7). The
// executor rotates across the pool's enabled members; additional strategies
// are additive later values of this column.
const (
	PoolStrategyRoundRobin = "round_robin"
)

// Pool scoping: a pool with a workspace_id belongs to that workspace; a pool
// with a nil workspace_id is a deployment-wide pool the admin manages and
// every workspace may send from (visible alongside workspace-private pools).
// The nil-workspace case mirrors the existing nullable workspace_id columns
// (ADR-008 §1: every table carries workspace_id or resolves through a strict
// parent chain — here the column is carried directly).
const (
	PoolScopeShared     = "shared"
	PoolScopeWorkspace  = "workspace"
)

// IPPool is a named set of outbound source IPs a sending domain may rotate
// across for direct-to-MX delivery (ADR-001 §7's per-domain bind extended to
// a pool; ADR-007 decision 7). Workspace-scoped per ADR-008 §1.
type IPPool struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// WorkspaceID nil = deployment-wide pool (admin-managed, usable by every
	// workspace); non-nil = the workspace's private pool.
	WorkspaceID *uint   `json:"workspace_id,omitempty" gorm:"index"`
	UserID      uint    `json:"user_id" gorm:"default:0"` // owning user when workspace-scoped; 0 for deployment pools
	Name        string  `json:"name" gorm:"size:120;not null"`
	Description string  `json:"description" gorm:"size:500"`
	Strategy    string  `json:"strategy" gorm:"size:24;not null;default:'round_robin'"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName overrides GORM's default table naming.
func (IPPool) TableName() string { return "ip_pools" }

// Scope reports whether the pool is deployment-wide ("shared") or
// workspace-private.
func (p *IPPool) Scope() string {
	if p.WorkspaceID == nil {
		return PoolScopeShared
	}
	return PoolScopeWorkspace
}

// IPPoolMember is one source IP inside a pool. Additive membership changes
// never touch the pool row; disabling a member stops selection without
// deleting the history that attributes deliveries to it.
type IPPoolMember struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// WorkspaceID is denormalized from the pool so scoped sweeps and scoped
	// reads never need the join (ADR-008 migration checklist).
	WorkspaceID *uint     `json:"workspace_id,omitempty" gorm:"index"`
	PoolID      uint      `json:"pool_id" gorm:"not null;index:idx_ip_pool_members_pool_ip,unique"`
	IP          string    `json:"ip" gorm:"size:64;not null;index:idx_ip_pool_members_pool_ip,unique"`
	PTRName     string    `json:"ptr_name" gorm:"size:255"`
	Enabled     bool      `json:"enabled" gorm:"not null;default:true"`
	CreatedAt   time.Time `json:"created_at"`
}

// TableName overrides GORM's default table naming.
func (IPPoolMember) TableName() string { return "ip_pool_members" }

// IPWarmupSchedule is a per-domain volume ramp (OB-6 W3, ADR-007 decision 7):
// day N of the ramp allows roughly StartVolume × DailyMultiplier^N messages
// for Days days; afterwards the domain's own policy budget stands. One active
// schedule per domain — starting a new one deactivates the previous (the
// repository enforces it; the ramp anchor is StartedAt, day 0).
type IPWarmupSchedule struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// WorkspaceID mirrors the owning domain's workspace (ADR-008 §1).
	WorkspaceID     *uint     `json:"workspace_id,omitempty" gorm:"index"`
	UserID          uint      `json:"user_id" gorm:"default:0"`
	DomainID        uint      `json:"domain_id" gorm:"not null;index"`
	Domain          string    `json:"domain" gorm:"size:255;not null"`
	StartVolume     int       `json:"start_volume" gorm:"not null;default:0"`
	DailyMultiplier float64   `json:"daily_multiplier" gorm:"not null;default:1"`
	Days            int       `json:"days" gorm:"not null;default:0"`
	StartedAt       time.Time `json:"started_at" gorm:"not null"`
	Active          bool      `json:"active" gorm:"not null;default:true;index"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// TableName overrides GORM's default table naming.
func (IPWarmupSchedule) TableName() string { return "ip_warmup_schedules" }

// IPReputationRollup is the pre-aggregated per-(workspace, ip, domain, day)
// delivery reputation row (ADR-007 decision 7). Two row shapes share the
// table, keyed by IP:
//
//   - IP set: transport outcomes of that source IP against that recipient
//     domain that day, from mta_delivery_attempts.
//   - IP '' (the domain-wide row): recipient-side events (sent/bounced/
//     complained) for the domain that day, from email_events. Rates that
//     need both sides are computed by joining on (domain, day).
//
// The unique key (workspace_id, ip, domain, day) makes every refresh an
// idempotent upsert: re-running a day never double-counts.
type IPReputationRollup struct {
	ID uint `json:"id" gorm:"primaryKey"`
	// WorkspaceID nil in the shared view of the admin surface (rows roll up
	// per workspace; the admin aggregate groups across them).
	WorkspaceID *uint     `json:"workspace_id,omitempty" gorm:"uniqueIndex:idx_ip_reputation_rollup_key,priority:1"`
	IP          string    `json:"ip" gorm:"size:64;not null;default:'';uniqueIndex:idx_ip_reputation_rollup_key,priority:2"`
	Domain      string    `json:"domain" gorm:"size:255;not null;uniqueIndex:idx_ip_reputation_rollup_key,priority:3"`
	Day         time.Time `json:"day" gorm:"not null;uniqueIndex:idx_ip_reputation_rollup_key,priority:4"`

	// Transport outcomes (IP rows only; zero on domain-wide rows).
	AttemptsTotal     int64 `json:"attempts_total" gorm:"not null;default:0"`
	AttemptsDelivered int64 `json:"attempts_delivered" gorm:"not null;default:0"`
	AttemptsTransient int64 `json:"attempts_transient" gorm:"not null;default:0"`
	AttemptsPermanent int64 `json:"attempts_permanent" gorm:"not null;default:0"`
	AttemptsDeferred  int64 `json:"attempts_deferred" gorm:"not null;default:0"`
	AttemptsDNSFail   int64 `json:"attempts_dns_failure" gorm:"not null;default:0"`
	AttemptsRefused   int64 `json:"attempts_refused" gorm:"not null;default:0"`

	// Recipient-side events (domain-wide rows only; zero on IP rows).
	EventsSent       int64 `json:"events_sent" gorm:"not null;default:0"`
	EventsBounced    int64 `json:"events_bounced" gorm:"not null;default:0"`
	EventsComplained int64 `json:"events_complained" gorm:"not null;default:0"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName overrides GORM's default table naming.
func (IPReputationRollup) TableName() string { return "ip_reputation_rollups" }
