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

// WebhookDeliveryAttempt records one HTTP attempt of an outgoing webhook
// delivery — success or failure — so the delivery history shows every retry,
// not only the final result recorded on WebhookDelivery.
type WebhookDeliveryAttempt struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	DeliveryID   uint      `json:"delivery_id" gorm:"not null;index"`
	Attempt      int       `json:"attempt" gorm:"not null"`
	StatusCode   int       `json:"status_code"`
	ResponseBody string    `json:"response_body" gorm:"type:text"` // truncated to 2KB
	Error        string    `json:"error,omitempty" gorm:"type:text"`
	CreatedAt    time.Time `json:"created_at" gorm:"not null"`
}

// TableName overrides GORM's default table naming.
func (WebhookDeliveryAttempt) TableName() string { return "webhook_delivery_attempts" }
