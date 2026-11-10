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
	"fmt"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// Session-level helpers for the webmail REST surface (OB-5 wave W1, ADR-005:
// "mailstore gains a small set of session-level helpers"). These are the only
// primitives the /api/v1/mail handlers need that the existing Service methods
// do not already expose: a folder-by-ID lookup (the REST surface addresses
// folders by ID where IMAP uses paths), an order-preserving scoped batch load
// for search-hit rows, and the plain Searcher entry point (SearchIMAP serves
// the IMAP SEARCH tree; the webmail list/search endpoints need the raw
// ADR-003 FTS tier). GetMessageByUID lives in compose.go (the compose wave
// needed it first); the webmail handlers share it.

// GetFolderByID resolves one folder by ID with the same ownership scoping as
// every other read: the folder's mailbox must belong to userID (and, when
// non-zero, the workspace must match). Returns the folder and its owning
// mailbox — the mailbox feeds the search scope of the webmail list endpoint.
func (s *Service) GetFolderByID(folderID, userID, workspaceID uint) (*models.MailFolder, *models.Mailbox, error) {
	var (
		outF *models.MailFolder
		outM *models.Mailbox
	)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		f, mb, err := s.resolveFolder(tx, folderID, userID, workspaceID)
		if err != nil {
			return err
		}
		outF, outM = f, mb
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return outF, outM, nil
}

// GetMessagesByIDs loads messages by ID, preserving the caller's order and
// keeping only rows the user can actually see (each message's folder must
// resolve for (userID, workspaceID)). The webmail search endpoints feed this
// with FTS hit IDs: the searcher already scopes its statements, and this
// re-derives the same ownership decision below the HTTP layer so a hit list
// can never surface a foreign row even if a searcher implementation drifted.
func (s *Service) GetMessagesByIDs(ids []uint, userID, workspaceID uint) ([]models.MailMessage, error) {
	if len(ids) == 0 {
		return []models.MailMessage{}, nil
	}
	var rows []models.MailMessage
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
			return fmt.Errorf("load %d messages: %w", len(ids), err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	byID := make(map[uint]models.MailMessage, len(rows))
	for _, m := range rows {
		byID[m.ID] = m
	}
	// Resolve each distinct folder once: folders are per-mailbox and few, so
	// this costs one lookup per folder rather than per message.
	folderOK := make(map[uint]bool)
	for _, m := range rows {
		if _, done := folderOK[m.FolderID]; done {
			continue
		}
		err := s.db.Transaction(func(tx *gorm.DB) error {
			_, _, err := s.resolveFolder(tx, m.FolderID, userID, workspaceID)
			return err
		})
		folderOK[m.FolderID] = err == nil
	}
	out := make([]models.MailMessage, 0, len(ids))
	for _, id := range ids {
		m, ok := byID[id]
		if !ok || !folderOK[m.FolderID] {
			continue // foreign or vanished row: filtered, never surfaced
		}
		out = append(out, m)
	}
	return out, nil
}

// Search runs the wired FTS searcher (PostgresSearcher over
// mail_messages.search_tsv, ADR-003 decision 3) for the webmail surface.
// Unwired, it reports ErrSearchUnavailable — the HTTP layer refuses search
// rather than silently returning no matches (the same posture SearchIMAP
// takes for BODY/TEXT keys).
func (s *Service) Search(ctx context.Context, scope SearchScope, q SearchQuery) ([]SearchHit, int64, error) {
	if s.searcher == nil {
		return nil, 0, ErrSearchUnavailable
	}
	return s.searcher.Search(ctx, scope, q)
}
