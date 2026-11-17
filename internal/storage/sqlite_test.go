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
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"path/filepath"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/migration"
	"github.com/lib/pq"
)

// SF-76 tests (the ADR-009 amendment): the SQLite dev-tier backend. The
// round-trip runs the SAME migration entry point the server runs
// (migration.Run over the full registered fleet) on a temp-file database
// through ConnectSQLite — the pure-Go driver + the DDL portability rewrite —
// and exercises the core models' create/read paths, including a model that
// carries the Postgres-idiomatic `DEFAULT gen_random_uuid()` (emails.uuid) so
// the rewrite is proven on a real insert, not by inspection.

func TestSQLiteDevTierMigrateAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.db")
	db, err := ConnectSQLite(path)
	if err != nil {
		t.Fatalf("connect sqlite: %v", err)
	}
	if err := migration.Run(db); err != nil {
		t.Fatalf("migration.Run on sqlite: %v", err)
	}

	// The core round-trip: user → workspace → domain → email (the email
	// exercises the uuid default rewrite + the text[] + jsonb columns).
	user := &models.User{Name: "dev", Email: "dev@example.test", PasswordHash: "hashed", Role: models.UserRoleAdmin, Active: true}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	ws := &models.Workspace{Name: "dev-ws", Slug: "dev-ws", OwnerID: user.ID}
	if err := db.Create(ws).Error; err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	dom := &models.Domain{
		UserID: user.ID, WorkspaceID: &ws.ID, Domain: "dev.example.test",
		OwnershipVerified: true, VerificationToken: "tok-dev",
	}
	if err := db.Create(dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	email := &models.Email{
		UserID: user.ID, WorkspaceID: &ws.ID,
		Sender:     "sender@dev.example.test",
		Recipients: pq.StringArray{"to@remote.test"},
		Subject:    "dev-tier round trip",
		HTMLBody:   "<p>hi</p>",
	}
	if err := db.Create(email).Error; err != nil {
		t.Fatalf("create email: %v", err)
	}

	// Read back through fresh queries.
	var gotUser models.User
	if err := db.First(&gotUser, user.ID).Error; err != nil {
		t.Fatalf("read user: %v", err)
	}
	if gotUser.Email != "dev@example.test" || !gotUser.Active {
		t.Fatalf("user round trip = %+v", gotUser)
	}
	var gotDom models.Domain
	if err := db.Where("domain = ?", "dev.example.test").First(&gotDom).Error; err != nil {
		t.Fatalf("read domain: %v", err)
	}
	if !gotDom.OwnershipVerified || gotDom.WorkspaceID == nil || *gotDom.WorkspaceID != ws.ID {
		t.Fatalf("domain round trip = %+v", gotDom)
	}
	var gotEmail models.Email
	if err := db.First(&gotEmail, email.ID).Error; err != nil {
		t.Fatalf("read email: %v", err)
	}
	if gotEmail.Subject != "dev-tier round trip" {
		t.Fatalf("email round trip = %+v", gotEmail)
	}
	// The Postgres-idiomatic uuid default was rewritten: the column carries a
	// generated identifier, not an empty string.
	if gotEmail.UUID == "" {
		t.Fatal("emails.uuid must carry the SQLite-native default value")
	}
	if len(gotEmail.Recipients) != 1 || gotEmail.Recipients[0] != "to@remote.test" {
		t.Fatalf("email recipients round trip = %v", gotEmail.Recipients)
	}
	if gotEmail.TemplateData != "{}" {
		t.Fatalf("template_data default = %q, want {}", gotEmail.TemplateData)
	}

	// Persistence across connections: a second handle on the same file sees
	// the migrated schema and the data.
	db2, err := ConnectSQLite(path)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	var count int64
	if err := db2.Model(&models.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count on the second connection: %v", err)
	}
	if count != 1 {
		t.Fatalf("user count = %d, want 1", count)
	}
}

func TestSQLiteConnectRequiresPath(t *testing.T) {
	if _, err := ConnectSQLite("  "); err == nil {
		t.Fatal("ConnectSQLite without a path must fail")
	}
}
