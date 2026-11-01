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

type BounceType string

const (
	BounceTypeHard      BounceType = "hard"
	BounceTypeSoft      BounceType = "soft"
	BounceTypeComplaint BounceType = "complaint"
)

type Bounce struct {
	ID          uint       `json:"id" gorm:"primaryKey"`
	UserID      uint       `json:"user_id" gorm:"index;not null"`
	WorkspaceID *uint      `json:"workspace_id,omitempty" gorm:"index"`
	EmailID     uint       `json:"email_id" gorm:"index;not null"`
	Recipient   string     `json:"recipient" gorm:"not null"`
	Type        BounceType `json:"type" gorm:"not null"`
	Reason      string     `json:"reason"`
	CreatedAt   time.Time  `json:"created_at"`

	User  User  `json:"-" gorm:"foreignKey:UserID"`
	Email Email `json:"-" gorm:"foreignKey:EmailID"`
}
