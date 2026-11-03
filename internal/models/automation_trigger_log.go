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

// AutomationTriggerLog dedups once-frequency enrollments: the composite unique
// index on (automation_id, subscriber_id) lets a fire-and-forget insert decide
// atomically whether this contact has already entered this automation, so two
// racing trigger events enroll the contact exactly once.
type AutomationTriggerLog struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	AutomationID uint      `json:"automation_id" gorm:"uniqueIndex:idx_automation_trigger_dedup,priority:1;not null"`
	SubscriberID uint      `json:"subscriber_id" gorm:"uniqueIndex:idx_automation_trigger_dedup,priority:2;not null"`
	TriggeredAt  time.Time `json:"triggered_at" gorm:"not null"`
}

// TableName overrides GORM's default table naming.
func (AutomationTriggerLog) TableName() string { return "automation_trigger_log" }
