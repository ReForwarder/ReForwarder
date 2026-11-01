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

type Domain struct {
	ID                uint      `json:"id" gorm:"primaryKey"`
	UserID            uint      `json:"user_id" gorm:"index;not null"`
	WorkspaceID       *uint     `json:"workspace_id,omitempty" gorm:"index"`
	Domain            string    `json:"domain" gorm:"not null"`
	OwnershipVerified bool      `json:"ownership_verified" gorm:"default:false"`
	SPFVerified       bool      `json:"spf_verified" gorm:"default:false"`
	DKIMVerified      bool      `json:"dkim_verified" gorm:"default:false"`
	DMARCVerified     bool      `json:"dmarc_verified" gorm:"default:false"`
	VerificationToken string    `json:"verification_token" gorm:"not null"`
	CreatedAt         time.Time `json:"created_at"`

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
