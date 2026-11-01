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

	"github.com/goposta/posta/internal/models"
	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDecodeEmailSenders(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "host=localhost user=posta password=posta dbname=posta port=5432 sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.APIKey{}, &models.Email{}); err != nil {
		t.Skipf("skipping: cannot migrate test schema: %v", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	user := &models.User{Name: "test", Email: "decode-senders@test.local", PasswordHash: "x"}
	if err := tx.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	cases := []struct{ stored, want string }{
		{"=?UTF-8?b?0KHQsNC90LDRgtC+0YDQuNC5LtGA0YM=?= <noreply@sanatory.ru>", `"Санаторий.ру" <noreply@sanatory.ru>`},
		{"=?UTF-8?b?0JDQutC80LU=?= =?UTF-8?b?INCz0YDRg9C/0L8=?= <a@example.com>", `"Акме групп" <a@example.com>`},
		{"=?UTF-8?q?Acme?= <a@example.com>", `"Acme" <a@example.com>`},
		{`"Санаторий.ру" <noreply@sanatory.ru>`, `"Санаторий.ру" <noreply@sanatory.ru>`},
		{"plain@example.com", "plain@example.com"},
		{"=?broken <not an address", "=?broken <not an address"},
	}
	ids := make([]uint, len(cases))
	for i, tc := range cases {
		em := &models.Email{UserID: user.ID, Sender: tc.stored, Subject: "s", Recipients: pq.StringArray{"r@example.com"}}
		if err := tx.Create(em).Error; err != nil {
			t.Fatalf("create email: %v", err)
		}
		ids[i] = em.ID
	}

	if err := applyDecodeEmailSenders(tx); err != nil {
		t.Fatalf("apply: %v", err)
	}

	for i, tc := range cases {
		var em models.Email
		if err := tx.First(&em, ids[i]).Error; err != nil {
			t.Fatalf("load email: %v", err)
		}
		if em.Sender != tc.want {
			t.Errorf("sender %q became %q, want %q", tc.stored, em.Sender, tc.want)
		}
	}
}

func TestDecodeEmailSendersStepIsRegistered(t *testing.T) {
	for _, s := range Registry() {
		if s.ID == "2026-10-13-decode-email-senders" {
			return
		}
	}
	t.Fatal(`upgrade step "2026-10-13-decode-email-senders" is not registered`)
}
