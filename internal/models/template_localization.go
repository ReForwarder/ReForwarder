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

type TemplateLocalization struct {
	ID              uint       `json:"id" gorm:"primaryKey"`
	VersionID       uint       `json:"version_id" gorm:"uniqueIndex:idx_version_language;not null"`
	Language        string     `json:"language" gorm:"uniqueIndex:idx_version_language;not null;size:10"`
	SubjectTemplate string     `json:"subject_template" gorm:"not null"`
	HTMLTemplate    string     `json:"html_template"`
	TextTemplate    string     `json:"text_template"`
	BuilderJSON     string     `json:"builder_json,omitempty" gorm:"type:text"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`

	Version TemplateVersion `json:"-" gorm:"foreignKey:VersionID;constraint:OnDelete:CASCADE"`
}
