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

type TemplateVersion struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	TemplateID   uint      `json:"template_id" gorm:"index;not null"`
	Version      int       `json:"version" gorm:"not null"`
	StyleSheetID *uint     `json:"stylesheet_id,omitempty" gorm:"index"`
	SampleData   string    `json:"sample_data"`
	CreatedAt    time.Time `json:"created_at"`

	StyleSheet    *StyleSheet            `json:"stylesheet,omitempty" gorm:"foreignKey:StyleSheetID;constraint:OnDelete:SET NULL"`
	Localizations []TemplateLocalization `json:"localizations,omitempty" gorm:"foreignKey:VersionID"`
}
