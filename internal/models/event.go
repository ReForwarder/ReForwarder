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

type EventCategory string

const (
	EventCategoryUser   EventCategory = "user"
	EventCategoryEmail  EventCategory = "email"
	EventCategorySystem EventCategory = "system"
	EventCategoryAudit  EventCategory = "audit"
)

type Event struct {
	ID          uint          `json:"id" gorm:"primaryKey"`
	Category    EventCategory `json:"category" gorm:"index;not null"`
	Type        string        `json:"type" gorm:"index;not null"`
	WorkspaceID *uint         `json:"workspace_id,omitempty" gorm:"index"`
	ActorID     *uint         `json:"actor_id" gorm:"index"`
	ActorName   string        `json:"actor_name"`
	ClientIP    string        `json:"client_ip,omitempty" gorm:"size:45"`
	Message     string        `json:"message" gorm:"not null"`
	Metadata    string        `json:"metadata" gorm:"type:text"`
	CreatedAt   time.Time     `json:"created_at" gorm:"index"`
}
