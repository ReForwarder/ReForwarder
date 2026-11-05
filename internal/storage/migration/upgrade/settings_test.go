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
	"os"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func upgradeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=posta password=posta dbname=posta port=5432 sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available: %v", err)
	}
	return db
}

// A dev build never writes app.version, so a database bootstrapped by one has
// applied upgrade steps but no version row. That must not read as "fresh":
// marking the registry applied a second time collides on upgrade_steps_pkey
// and the server refuses to start.
func TestReadVersionFreshOnlyBeforeRegistryIsSealed(t *testing.T) {
	db := upgradeTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	if err := tx.AutoMigrate(&models.Setting{}, &models.UpgradeStep{}); err != nil {
		t.Skipf("skipping: cannot migrate test schema: %v", err)
	}
	if err := tx.Where("key = ?", KeyAppVersion).Delete(&models.Setting{}).Error; err != nil {
		t.Fatalf("clear app.version: %v", err)
	}
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.UpgradeStep{}).Error; err != nil {
		t.Fatalf("clear upgrade steps: %v", err)
	}

	// Truly empty database: a first boot.
	version, fresh, err := readVersion(tx)
	if err != nil {
		t.Fatalf("readVersion on empty database: %v", err)
	}
	if !fresh || version != "" {
		t.Fatalf("empty database = (%q, fresh=%v), want (\"\", fresh=true)", version, fresh)
	}

	// Sealed registry, no version pin — the shape a dev build leaves behind.
	if err := tx.Create(&models.UpgradeStep{
		ID:         "test-read-version",
		AppVersion: "dev",
		AppliedAt:  time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("create upgrade step: %v", err)
	}
	version, fresh, err = readVersion(tx)
	if err != nil {
		t.Fatalf("readVersion with applied steps: %v", err)
	}
	if fresh || version != "" {
		t.Fatalf("sealed database = (%q, fresh=%v), want (\"\", fresh=false)", version, fresh)
	}

	// Normal versioned install: the stored version wins.
	if err := tx.Create(&models.Setting{
		Key:   KeyAppVersion,
		Value: "v1.2.3",
		Type:  models.SettingTypeString,
	}).Error; err != nil {
		t.Fatalf("create app.version: %v", err)
	}
	version, fresh, err = readVersion(tx)
	if err != nil {
		t.Fatalf("readVersion with version: %v", err)
	}
	if fresh || version != "v1.2.3" {
		t.Fatalf("versioned database = (%q, fresh=%v), want (\"v1.2.3\", fresh=false)", version, fresh)
	}
}
