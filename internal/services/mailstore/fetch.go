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

package mailstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// GetMessage returns one message's metadata (no body read) with ownership
// scoping.
func (s *Service) GetMessage(messageID, userID, workspaceID uint) (*models.MailMessage, error) {
	var out *models.MailMessage
	err := s.db.Transaction(func(tx *gorm.DB) error {
		m, _, _, err := s.resolveMessage(tx, messageID, userID, workspaceID)
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetMessageBody reads the stored raw RFC 5322 message: blob → decode →
// integrity check (the codec verifies the content address). The raw message
// is the source of truth; no reassembly, no data loss.
func (s *Service) GetMessageBody(ctx context.Context, messageID, userID, workspaceID uint) ([]byte, error) {
	m, err := s.GetMessage(messageID, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	return s.readBlob(ctx, m.BodyBlobRef)
}

// GetAttachment returns one attachment's decoded content and metadata.
func (s *Service) GetAttachment(ctx context.Context, messageID, userID, workspaceID uint, blobHash string) (*models.MailAttachmentRef, []byte, error) {
	m, err := s.GetMessage(messageID, userID, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	var atts []models.MailAttachmentRef
	if err := json.Unmarshal([]byte(m.AttachmentsJSON), &atts); err != nil {
		return nil, nil, fmt.Errorf("parse attachment refs of message %d: %w", m.ID, err)
	}
	for _, a := range atts {
		if a.BlobHash == blobHash {
			data, err := s.readBlob(ctx, blobHash)
			if err != nil {
				return nil, nil, err
			}
			return &a, data, nil
		}
	}
	return nil, nil, ErrMessageNotFound
}

// readBlob fetches and decodes one content-addressed blob.
func (s *Service) readBlob(ctx context.Context, hash string) ([]byte, error) {
	if hash == "" {
		return nil, ErrBlobCorrupt
	}
	var ref models.MailBlobRef
	if err := s.db.Where("blob_hash = ?", hash).First(&ref).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("blob %s unreferenced: %w", hash, ErrBlobCorrupt)
		}
		return nil, fmt.Errorf("load blob ref %s: %w", hash, err)
	}
	rc, err := s.store.Get(ctx, BlobObjectKey(hash))
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", hash, err)
	}
	defer rc.Close()
	stored, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read blob %s body: %w", hash, err)
	}
	return s.codec.Decode(stored, BlobRef{Hash: ref.BlobHash, Size: ref.Size})
}

// ListFilter narrows a listing. Zero value lists everything.
type ListFilter struct {
	Unread  bool // only messages without \Seen
	Flagged bool // only messages with \Flagged
}

// ListPage is one page of a folder listing.
type ListPage struct {
	Messages []models.MailMessage
	Total    int64
}

// ListMessages returns a page of a folder's messages sorted by UID ascending
// (IMAP order), with the total count for pagination. PageSize is capped at
// 200; Page is 1-based.
func (s *Service) ListMessages(folderID, userID, workspaceID uint, filter ListFilter, page, pageSize int) (*ListPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	var out ListPage
	err := s.db.Transaction(func(tx *gorm.DB) error {
		f, _, err := s.resolveFolder(tx, folderID, userID, workspaceID)
		if err != nil {
			return err
		}
		q := tx.Model(&models.MailMessage{}).Where("folder_id = ?", f.ID)
		switch {
		case filter.Unread:
			q = q.Where("(flags & ?) = 0", models.FlagSeen)
		case filter.Flagged:
			q = q.Where("(flags & ?) > 0", models.FlagFlagged)
		}
		if err := q.Count(&out.Total).Error; err != nil {
			return fmt.Errorf("count messages of folder %d: %w", folderID, err)
		}
		if err := q.Order("uid ASC").
			Limit(pageSize).Offset((page - 1) * pageSize).
			Find(&out.Messages).Error; err != nil {
			return fmt.Errorf("list messages of folder %d: %w", folderID, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
