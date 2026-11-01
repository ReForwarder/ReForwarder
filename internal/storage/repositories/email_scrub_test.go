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
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/lib/pq"
)

func TestScrubOlderThan_ClearsContentKeepsMetadata(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&models.Email{}); err != nil {
		t.Skipf("skipping: cannot migrate email schema: %v", err)
	}

	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	userID := createUser(t, tx, "scrub@test.local")
	repo := NewEmailRepository(tx)

	old := &models.Email{
		UserID:          userID,
		Sender:          "from@test.local",
		Recipients:      pq.StringArray{"to@test.local"},
		Subject:         "keep me",
		HTMLBody:        "<p>secret</p>",
		TextBody:        "secret",
		AttachmentsJSON: `[{"filename":"a.pdf"}]`,
		Status:          models.EmailStatusSent,
		CreatedAt:       time.Now().AddDate(0, 0, -60),
	}
	recent := &models.Email{
		UserID:     userID,
		Sender:     "from@test.local",
		Recipients: pq.StringArray{"to@test.local"},
		Subject:    "recent",
		HTMLBody:   "<p>fresh</p>",
		TextBody:   "fresh",
		Status:     models.EmailStatusSent,
		CreatedAt:  time.Now(),
	}
	if err := repo.Create(old); err != nil {
		t.Fatalf("create old: %v", err)
	}
	if err := repo.Create(recent); err != nil {
		t.Fatalf("create recent: %v", err)
	}

	cutoff := time.Now().AddDate(0, 0, -30)
	if n, err := repo.ScrubBodiesOlderThan(cutoff); err != nil || n != 1 {
		t.Fatalf("ScrubBodiesOlderThan: n=%d err=%v (want 1, nil)", n, err)
	}
	if n, err := repo.ScrubAttachmentsOlderThan(cutoff); err != nil || n != 1 {
		t.Fatalf("ScrubAttachmentsOlderThan: n=%d err=%v (want 1, nil)", n, err)
	}

	gotOld, err := repo.FindByID(old.ID)
	if err != nil {
		t.Fatalf("reload old: %v", err)
	}
	if gotOld.HTMLBody != "" || gotOld.TextBody != "" || gotOld.AttachmentsJSON != "" {
		t.Fatalf("old content not scrubbed: html=%q text=%q att=%q", gotOld.HTMLBody, gotOld.TextBody, gotOld.AttachmentsJSON)
	}
	if gotOld.Subject != "keep me" || gotOld.Sender != "from@test.local" {
		t.Fatalf("old metadata altered: subject=%q sender=%q", gotOld.Subject, gotOld.Sender)
	}

	gotRecent, err := repo.FindByID(recent.ID)
	if err != nil {
		t.Fatalf("reload recent: %v", err)
	}
	if gotRecent.HTMLBody == "" || gotRecent.TextBody == "" {
		t.Fatalf("recent content should be untouched, got html=%q text=%q", gotRecent.HTMLBody, gotRecent.TextBody)
	}
}
