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

package upgrade

import (
	"errors"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// readVersion returns the stored app version. The fresh flag is true only when
// the database has never been bootstrapped: no app.version row and no applied
// upgrade steps.
//
// The step rows matter because a dev build never writes app.version. Keying
// "fresh" off the setting alone makes every dev boot look like a first boot,
// so markAllApplied re-inserts the whole registry and the second boot dies on
// the upgrade_steps primary key. Applied steps are the durable evidence that
// this database has already been sealed.
func readVersion(db *gorm.DB) (version string, fresh bool, err error) {
	var s models.Setting
	err = db.Where("key = ?", KeyAppVersion).First(&s).Error
	if err == nil {
		return s.Value, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, err
	}

	var applied int64
	if err := db.Model(&models.UpgradeStep{}).Count(&applied).Error; err != nil {
		return "", false, err
	}
	return "", applied == 0, nil
}

// writeVersion updates the app.version and app.last_started_at settings rows
// in a single transaction. It also creates app.first_started_at on first
// successful boot.
func writeVersion(db *gorm.DB, version string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return db.Transaction(func(tx *gorm.DB) error {
		if err := upsertSetting(tx, KeyAppVersion, version); err != nil {
			return err
		}
		if err := upsertSetting(tx, KeyAppLastStartedAt, now); err != nil {
			return err
		}
		return ensureSetting(tx, KeyAppFirstStartedAt, now)
	})
}

// upsertSetting writes a string value, replacing any existing row for the key.
func upsertSetting(tx *gorm.DB, key, value string) error {
	var existing models.Setting
	err := tx.Where("key = ?", key).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&models.Setting{Key: key, Value: value, Type: models.SettingTypeString}).Error
	}
	if err != nil {
		return err
	}
	existing.Value = value
	existing.Type = "string"
	return tx.Save(&existing).Error
}

// ensureSetting inserts the row only if it does not already exist.
func ensureSetting(tx *gorm.DB, key, value string) error {
	var existing models.Setting
	err := tx.Where("key = ?", key).First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&models.Setting{Key: key, Value: value, Type: models.SettingTypeString}).Error
}
