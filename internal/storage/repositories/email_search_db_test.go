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

package repositories

import (
	"sort"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func seedSearchEmails(t *testing.T, tx *gorm.DB) ResourceScope {
	t.Helper()
	if err := tx.AutoMigrate(&models.APIKey{}, &models.Email{}); err != nil {
		t.Skipf("skipping: cannot migrate email schema: %v", err)
	}
	user := createUser(t, tx, "search-owner@test.local")
	ws := uintPtr(90101)
	key := &models.APIKey{UserID: user, WorkspaceID: ws, Name: "Billing service", KeyHash: "x", KeyPrefix: "pk_test"}
	if err := tx.Create(key).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}
	emails := []models.Email{
		{Sender: "noreply@acme.com", Subject: "Weekly report", Recipients: pq.StringArray{"solo@example.com"},
			Provider: "Google Workspace", SMTPHostname: "smtp.acme.com"},
		{Sender: "noreply@acme.com", Subject: "Invoice", Recipients: pq.StringArray{"first@example.com", "second@example.com"},
			Provider: "Other", SMTPHostname: "smtps.relay.net", APIKeyID: &key.ID},
		{Sender: "noreply@acme.com", Subject: "Welcome second@example.com", Recipients: pq.StringArray{"other@example.com"}},
	}
	for i := range emails {
		emails[i].UserID = user
		emails[i].WorkspaceID = ws
		if err := tx.Create(&emails[i]).Error; err != nil {
			t.Fatalf("create email: %v", err)
		}
	}
	return ResourceScope{UserID: user, WorkspaceID: ws}
}

func searchSubjects(t *testing.T, repo *EmailRepository, scope ResourceScope, q string) []string {
	t.Helper()
	items, total, err := repo.FindByScopeFiltered(scope, ParseSearchQuery(q), "", 50, 0)
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	if int(total) != len(items) {
		t.Errorf("search %q: total = %d, items = %d", q, total, len(items))
	}
	subjects := make([]string, 0, len(items))
	for _, e := range items {
		subjects = append(subjects, e.Subject)
	}
	sort.Strings(subjects)
	return subjects
}

func TestFindByScopeFiltered_Search(t *testing.T) {
	db := testDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	scope := seedSearchEmails(t, tx)
	repo := NewEmailRepository(tx)

	tests := []struct {
		name string
		q    string
		want []string
	}{
		{"bare text matches subject", "weekly", []string{"Weekly report"}},
		{"bare address matches single recipient", "solo@example.com", []string{"Weekly report"}},
		{"bare address matches any of several recipients", "first@example.com", []string{"Invoice"}},
		{"bare address matches recipient or subject", "second@example.com", []string{"Invoice", "Welcome second@example.com"}},
		{"to: matches any of several recipients", "to:second@example.com", []string{"Invoice"}},
		{"subject: stays subject-only", "subject:second@example.com", []string{"Welcome second@example.com"}},
		{"bare words merge into subject: as before", "subject:weekly report", []string{"Weekly report"}},
		{"provider: matches case-insensitively", "provider:google", []string{"Weekly report"}},
		{"provider: accepts a quoted name", `provider:"google workspace"`, []string{"Weekly report"}},
		{"server: matches the SMTP hostname", "server:relay", []string{"Invoice"}},
		{"key: matches the API key name", "key:billing", []string{"Invoice"}},
		{"key: with no matching key finds nothing", "key:nope", nil},
		{"provider: list matches any provider", "provider:gmail,google", []string{"Weekly report"}},
		{"provider: list matches several emails", "provider:google,other", []string{"Invoice", "Weekly report"}},
		{"server: list matches any server", "server:acme,relay", []string{"Invoice", "Weekly report"}},
		{"key: list matches any key", "key:nope,billing", []string{"Invoice"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := searchSubjects(t, repo, scope, tc.q)
			if len(got) != len(tc.want) {
				t.Fatalf("search %q = %v, want %v", tc.q, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("search %q = %v, want %v", tc.q, got, tc.want)
				}
			}
		})
	}
}
