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

package mailcompose

import (
	"errors"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/gorm"
)

// Production adapters over GORM/repositories for the compose seam's stores.
// They are deliberately thin — every decision lives in the service, and tests
// substitute in-memory fakes for both.

// GormStagingStore implements StagingStore over mail_attachment_uploads.
type GormStagingStore struct {
	db *gorm.DB
}

func NewGormStagingStore(db *gorm.DB) *GormStagingStore {
	return &GormStagingStore{db: db}
}

// FindByUserAndHash implements StagingStore. A missing row is (nil, nil) —
// "not staged" is an expected outcome the gate refuses, not an error.
func (s *GormStagingStore) FindByUserAndHash(userID uint, hash string) (*models.MailAttachmentUpload, error) {
	var row models.MailAttachmentUpload
	err := s.db.Where("user_id = ? AND blob_hash = ?", userID, hash).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Save implements StagingStore (create or update by primary key).
func (s *GormStagingStore) Save(u *models.MailAttachmentUpload) error {
	return s.db.Save(u).Error
}

// DeleteByUserAndHashes implements StagingStore.
func (s *GormStagingStore) DeleteByUserAndHashes(userID uint, hashes []string) (int64, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	res := s.db.Where("user_id = ? AND blob_hash IN ?", userID, hashes).
		Delete(&models.MailAttachmentUpload{})
	return res.RowsAffected, res.Error
}

// GormDeliveryStore implements DeliveryStore over the email + email_events
// repositories. The user scoping rides FindEmailByUUIDForUser: the compose
// surface reads a timeline only for the row's own owner.
type GormDeliveryStore struct {
	emails *repositories.EmailRepository
	events *repositories.EmailEventRepository
}

func NewGormDeliveryStore(emails *repositories.EmailRepository, events *repositories.EmailEventRepository) *GormDeliveryStore {
	return &GormDeliveryStore{emails: emails, events: events}
}

// FindEmailByUUIDForUser implements DeliveryStore.
func (s *GormDeliveryStore) FindEmailByUUIDForUser(uuid string, userID uint) (*models.Email, error) {
	em, err := s.emails.FindByUUID(uuid)
	if err != nil {
		return nil, err
	}
	if em == nil || em.UserID != userID {
		// A foreign row reads exactly like a missing one.
		return nil, nil
	}
	return em, nil
}

// ListEventsByEmailUUID implements DeliveryStore.
func (s *GormDeliveryStore) ListEventsByEmailUUID(uuid string) ([]models.EmailEvent, error) {
	return s.events.ListByEmailUUID(uuid)
}
