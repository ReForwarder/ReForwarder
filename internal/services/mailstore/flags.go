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

// FlagsUpdate is one CAS flag store (IMAP STORE / CONDSTORE UNCHANGEDSINCE,
// consumed by OB-4).
type FlagsUpdate struct {
	MessageID uint
	// ExpectedModSeq is the mod_seq the caller last observed. 0 means
	// "unconditional store"; any other value must match the row's current
	// mod_seq or the store is rejected with ErrFlagConflict (the caller must
	// re-fetch and retry — another session changed the message).
	ExpectedModSeq uint64
	// Set / Clear are flag bitmasks; Set wins on overlap.
	Set   uint32
	Clear uint32
}

// FlagsResult reports the post-store state.
type FlagsResult struct {
	Flags  uint32
	ModSeq uint64
}

// SetFlags applies one CAS flag update for a user:
//
//	UPDATE mail_messages SET flags = ?, mod_seq = ?, updated_at = now
//	WHERE id = ? AND mod_seq = ?      (expected != 0)
//
// RowsAffected == 0 ⇒ either the message moved on (ErrFlagConflict) or it does
// not exist for this user (ErrMessageNotFound) — both are resolved by a scoped
// re-read before the CAS, so the distinction is exact (R-26 row-level CAS
// shape: one statement, decided by RowsAffected).
func (s *Service) SetFlags(userID, workspaceID uint, up FlagsUpdate) (*FlagsResult, error) {
	var result FlagsResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		m, _, _, err := s.resolveMessage(tx, up.MessageID, userID, workspaceID)
		if err != nil {
			return err
		}
		newFlags := (m.Flags | up.Set) &^ up.Clear
		modSeq := m.ModSeq
		if up.ExpectedModSeq != 0 {
			// Claim a fresh mod-sequence first; monotonicity holds even when
			// the CAS below loses (an allocated value may go unused).
			claimed, err := claimModSeq(tx, m.FolderID)
			if err != nil {
				return err
			}
			modSeq = claimed
			res := tx.Model(&models.MailMessage{}).
				Where("id = ? AND mod_seq = ?", m.ID, up.ExpectedModSeq).
				Updates(map[string]interface{}{
					"flags":   newFlags,
					"mod_seq": modSeq,
					// GORM auto-touches updated_at for map Updates.
				})
			if res.Error != nil {
				return fmt.Errorf("cas flags of message %d: %w", m.ID, res.Error)
			}
			if res.RowsAffected == 0 {
				return ErrFlagConflict
			}
		} else {
			claimed, err := claimModSeq(tx, m.FolderID)
			if err != nil {
				return err
			}
			modSeq = claimed
			if err := tx.Model(&models.MailMessage{}).Where("id = ?", m.ID).
				Updates(map[string]interface{}{
					"flags":   newFlags,
					"mod_seq": modSeq,
				}).Error; err != nil {
				return fmt.Errorf("store flags of message %d: %w", m.ID, err)
			}
		}
		result = FlagsResult{Flags: newFlags, ModSeq: modSeq}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// MarkDeleted sets the \Deleted flag unconditionally (convenience for webmail;
// IMAP clients will use SetFlags with UNCHANGEDSINCE semantics).
func (s *Service) MarkDeleted(userID, workspaceID uint, messageID uint) error {
	_, err := s.SetFlags(userID, workspaceID, FlagsUpdate{MessageID: messageID, Set: models.FlagDeleted})
	return err
}
