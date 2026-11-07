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

// UserPushSubscription (SF-89) stores one browser Web Push subscription
// (RFC 8030) registered by a dashboard user. Endpoint is the push-service URL
// the browser handed out; P256DH/Auth are the RFC 8291 encryption keys
// (base64url, exactly as the PushSubscriptionJSON reported them). Rows are
// user-scoped: every read and delete filters by user_id.
type UserPushSubscription struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	UserID    uint      `json:"user_id" gorm:"index;not null"`
	Endpoint  string    `json:"endpoint" gorm:"uniqueIndex;not null"`
	P256DH    string    `json:"p256dh" gorm:"column:p256dh;not null"`
	Auth      string    `json:"auth" gorm:"not null"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
