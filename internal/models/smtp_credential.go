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

import (
	"time"

	"github.com/lib/pq"
)

type SMTPCredential struct {
	ID           uint           `json:"id" gorm:"primaryKey"`
	WorkspaceID  uint           `json:"workspace_id" gorm:"index;not null"`
	UserID       uint           `json:"user_id" gorm:"index;not null"` // creator, for audit
	Name         string         `json:"name" gorm:"not null"`
	Username     string         `json:"username" gorm:"uniqueIndex;not null"`
	PasswordHash string         `json:"-" gorm:"not null"`
	AllowedIPs   pq.StringArray `json:"allowed_ips" gorm:"type:text[]"`
	Revoked      bool           `json:"revoked" gorm:"default:false"`
	CreatedAt    time.Time      `json:"created_at"`
	LastUsedAt   *time.Time     `json:"last_used_at"`

	User User `json:"-" gorm:"foreignKey:UserID"`
}

func (k *SMTPCredential) IsValid() bool {
	return !k.Revoked
}
