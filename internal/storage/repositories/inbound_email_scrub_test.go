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
	"gorm.io/gorm"
)

func reloadInbound(t *testing.T, tx *gorm.DB, id uint) *models.InboundEmail {
	t.Helper()
	var m models.InboundEmail
	if err := tx.First(&m, id).Error; err != nil {
		t.Fatalf("reload inbound %d: %v", id, err)
	}
	return &m
}

func TestInboundScrub_SeparatesBodyRawAndAttachments(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&models.Domain{}, &models.InboundEmail{}); err != nil {
		t.Skipf("skipping: cannot migrate inbound schema: %v", err)
	}

	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	userID := createUser(t, tx, "inbound-scrub@test.local")
	domain := &models.Domain{UserID: userID, Domain: "scrub.test.local", VerificationToken: "tok"}
	if err := tx.Create(domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	repo := NewInboundEmailRepository(tx)
	old := &models.InboundEmail{
		UserID:          userID,
		DomainID:        domain.ID,
		Sender:          "from@scrub.test.local",
		Recipients:      pq.StringArray{"to@scrub.test.local"},
		Subject:         "keep me",
		TextBody:        "secret",
		HTMLBody:        "<p>secret</p>",
		AttachmentsJSON: `[{"filename":"a.pdf"}]`,
		RawStorageKey:   "raw/old.eml",
		Source:          models.InboundSourceSMTP,
		ReceivedAt:      time.Now().AddDate(0, 0, -60),
		CreatedAt:       time.Now().AddDate(0, 0, -60),
	}
	if err := tx.Create(old).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	cutoff := time.Now().AddDate(0, 0, -30)

	// Body scrub must clear bodies but leave raw and attachments intact.
	if n, err := repo.ScrubBodiesOlderThan(cutoff); err != nil || n != 1 {
		t.Fatalf("ScrubBodiesOlderThan: n=%d err=%v (want 1, nil)", n, err)
	}
	got := reloadInbound(t, tx, old.ID)
	if got.HTMLBody != "" || got.TextBody != "" {
		t.Fatalf("bodies not scrubbed: html=%q text=%q", got.HTMLBody, got.TextBody)
	}
	if got.RawStorageKey == "" || got.AttachmentsJSON == "" {
		t.Fatalf("body scrub must not touch raw/attachments: raw=%q att=%q", got.RawStorageKey, got.AttachmentsJSON)
	}
	if got.Subject != "keep me" {
		t.Fatalf("metadata altered: subject=%q", got.Subject)
	}

	// Attachment scrub clears attachments only.
	if n, err := repo.ScrubAttachmentsOlderThan(cutoff); err != nil || n != 1 {
		t.Fatalf("ScrubAttachmentsOlderThan: n=%d err=%v (want 1, nil)", n, err)
	}
	got = reloadInbound(t, tx, old.ID)
	if got.AttachmentsJSON != "" || got.RawStorageKey == "" {
		t.Fatalf("attachment scrub wrong: att=%q raw=%q", got.AttachmentsJSON, got.RawStorageKey)
	}

	// Raw scrub clears the raw .eml reference.
	if n, err := repo.ScrubRawOlderThan(cutoff); err != nil || n != 1 {
		t.Fatalf("ScrubRawOlderThan: n=%d err=%v (want 1, nil)", n, err)
	}
	if got = reloadInbound(t, tx, old.ID); got.RawStorageKey != "" {
		t.Fatalf("raw not scrubbed: raw=%q", got.RawStorageKey)
	}
}
