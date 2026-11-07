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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// SettingHistory is one per-key audit record of a platform settings write
// (SF-100): every settings update that changes a key's value appends a row
// capturing old value → new value and the actor that made the change. Rollback
// (POST /admin/settings/history/rollback) restores OldValue through the normal
// settings setter, which in turn appends a new row — history is never rewritten.
//
// WorkspaceID is nil for platform keys (the only keys that flow through the
// platform settings setter today). It is nullable so workspace-scoped
// key/value settings, if introduced later, share this table without a
// migration; the repository filters on it when set.
type SettingHistory struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	WorkspaceID *uint  `json:"workspace_id,omitempty" gorm:"index"`
	Key         string `json:"key" gorm:"index;not null"`
	// OldValue is nil when the write created the key (there was no prior
	// value). A nil OldValue cannot be rolled back — there is nothing to
	// restore — and the rollback endpoint refuses it.
	OldValue       *string   `json:"old_value,omitempty" gorm:"type:text"`
	NewValue       string    `json:"new_value" gorm:"type:text"`
	ChangedBy      uint      `json:"changed_by" gorm:"not null"`
	ChangedByEmail string    `json:"changed_by_email" gorm:"type:text"`
	CreatedAt      time.Time `json:"created_at" gorm:"index"`
}

// TableName pins the table to `setting_history` (the SF-100 spec name) —
// gorm's pluralizer would otherwise call it setting_histories.
func (SettingHistory) TableName() string { return "setting_history" }
