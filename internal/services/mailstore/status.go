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
	"fmt"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// FolderStatus (OB-4 wave W1): the per-folder counters the IMAP STATUS
// command and the SELECT banner are built from (ADR-004 decision 1).
// UIDValidity/UIDNext stay on the folder row (GetFolderByPath); these are the
// message-derived counts.
type FolderStatus struct {
	FolderID uint
	// Messages is the folder's message count (STATUS MESSAGES / EXISTS).
	Messages int64
	// Unseen counts messages without \Seen (STATUS UNSEEN).
	Unseen int64
	// Recent counts messages still carrying \Recent (STATUS RECENT). \Recent
	// is best-effort per the mailstore model: set at append/copy, never
	// cleared by the store itself (session-selection semantics are the
	// protocol servers').
	Recent int64
}

// statusCountsSQL is one round-trip for all three counters. FILTER (WHERE …)
// is standard PostgreSQL; the fake test driver routes this exact statement to
// a dedicated handler (fakedb_test.go). Values travel as bound parameters.
const statusCountsSQL = `SELECT count(*) AS total_count,
       count(*) FILTER (WHERE (flags & ?) = 0) AS unseen_count,
       count(*) FILTER (WHERE (flags & ?) > 0) AS recent_count
FROM mail_messages WHERE folder_id = ?`

// statusCountRow mirrors one statusCountsSQL result row.
type statusCountRow struct {
	Total  int64 `gorm:"column:total_count"`
	Unseen int64 `gorm:"column:unseen_count"`
	Recent int64 `gorm:"column:recent_count"`
}

// StatusCounts returns the message/unseen/recent counters of one folder with
// ownership scoping.
func (s *Service) StatusCounts(folderID, userID, workspaceID uint) (*FolderStatus, error) {
	var out FolderStatus
	err := s.db.Transaction(func(tx *gorm.DB) error {
		f, _, err := s.resolveFolder(tx, folderID, userID, workspaceID)
		if err != nil {
			return err
		}
		var row statusCountRow
		if err := tx.Raw(statusCountsSQL,
			models.FlagSeen, models.FlagRecent, f.ID,
		).Scan(&row).Error; err != nil {
			return fmt.Errorf("status counts of folder %d: %w", f.ID, err)
		}
		out = FolderStatus{
			FolderID: f.ID,
			Messages: row.Total,
			Unseen:   row.Unseen,
			Recent:   row.Recent,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
