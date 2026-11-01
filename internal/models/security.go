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

// SecurityBan is a ban placed on an IP address (or CIDR block) by the abuse
// detection engine or by an administrator. Expired rows are kept as offense
// history: the next ban for the same IP escalates by counting them.
type SecurityBan struct {
	ID                uint       `json:"id" gorm:"primaryKey"`
	IPAddress         string     `json:"ip_address" gorm:"size:45;not null;index"`
	IsCIDR            bool       `json:"is_cidr" gorm:"not null;default:false"`
	ReasonCode        string     `json:"reason_code" gorm:"size:40;not null;index"`
	ReasonDescription string     `json:"reason_description" gorm:"type:text;not null"`
	Evidence          string     `json:"evidence" gorm:"type:jsonb;not null;default:'{}'"` // JSON string (marshal map before save)
	RiskScore         int        `json:"risk_score" gorm:"not null;default:0"`
	BanType           string     `json:"ban_type" gorm:"size:12;not null;default:automatic"` // automatic|manual
	OffenseCount      int        `json:"offense_count" gorm:"not null;default:1"`
	CreatedBy         string     `json:"created_by" gorm:"size:120;not null;default:system"` // "system" or admin email
	CreatedAt         time.Time  `json:"created_at" gorm:"index;not null"`
	UpdatedAt         time.Time  `json:"updated_at" gorm:"not null"`
	ExpiresAt         *time.Time `json:"expires_at"` // nil = permanent
}

func (SecurityBan) TableName() string { return "security_bans" }

// SecurityEvent records one observation of the security engine: a detection
// signal crossing a threshold or a ban lifecycle event
// (ban.created | ban.lifted | ban.expired_notice).
type SecurityEvent struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	SourceIP    string    `json:"source_ip" gorm:"size:45;not null;index"`
	EventType   string    `json:"event_type" gorm:"size:40;not null;index"`      // signal name or ban.created|ban.lifted|ban.expired_notice
	Severity    string    `json:"severity" gorm:"size:10;not null;default:info"` // info|warning|critical
	DestPort    *int      `json:"destination_port,omitempty"`
	Protocol    string    `json:"protocol" gorm:"size:12;not null;default:''"` // smtp|http|relay|tls
	Evidence    string    `json:"evidence" gorm:"type:jsonb;not null;default:'{}'"`
	RiskScore   int       `json:"risk_score" gorm:"not null;default:0"`
	ActionTaken string    `json:"action_taken" gorm:"size:20;not null;default:recorded"` // recorded|banned|rejected
	CreatedAt   time.Time `json:"created_at" gorm:"index;not null"`
}

// SecurityAllowlist exempts an IP address or CIDR block from detection and
// banning: allowlist entries always win over bans.
type SecurityAllowlist struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	IPCidr      string    `json:"ip_cidr" gorm:"size:48;not null;uniqueIndex"` // "1.2.3.4" or "10.0.0.0/8"
	Description string    `json:"description" gorm:"size:200;not null;default:''"`
	CreatedBy   string    `json:"created_by" gorm:"size:120;not null;default:system"`
	CreatedAt   time.Time `json:"created_at" gorm:"not null"`
}
