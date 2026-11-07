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
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// IMAPDelimiter separates path segments. The IMAP layer (OB-4) may advertise
// a different wire delimiter and translate; the store standardizes on "/".
const IMAPDelimiter = "/"

// inboxPath is the canonical INBOX path. RFC 9051: the INBOX name is
// case-insensitive; every other name is case-sensitive. The store stores
// INBOX canonically uppercase and resolves lookups case-insensitively for the
// first segment only.
const inboxPath = "INBOX"

// maxFolderNameLen bounds a single path segment (IMAP mailbox names are
// protocol-limited; 255 bytes leaves room for deep paths).
const maxFolderNameLen = 255

// canonicalizeFirstSegment maps an INBOX-cased first segment to "INBOX".
func canonicalizeFirstSegment(path string) string {
	parts := strings.SplitN(path, IMAPDelimiter, 2)
	if strings.EqualFold(parts[0], inboxPath) {
		parts[0] = inboxPath
	}
	return strings.Join(parts, IMAPDelimiter)
}

// validateFolderName rejects empty names, delimiter characters and oversized
// segments before anything is built.
func validateFolderName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("mailstore: folder name required")
	}
	if strings.Contains(name, IMAPDelimiter) {
		return fmt.Errorf("mailstore: folder name %q must not contain the delimiter %q", name, IMAPDelimiter)
	}
	if len(name) > maxFolderNameLen {
		return fmt.Errorf("mailstore: folder name longer than %d bytes", maxFolderNameLen)
	}
	return nil
}

// mintUIDValidity draws a fresh UIDVALIDITY. RFC 9051 requires the value to
// increase between folder incarnations; random 32-bit values satisfy that in
// practice and avoid wall-clock skew issues.
func mintUIDValidity() (uint32, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("mailstore: mint uid_validity: %w", err)
	}
	v := binary.BigEndian.Uint32(b[:])
	if v == 0 { // 0 is reserved-looking and needlessly confusing
		v = 1
	}
	return v, nil
}

// createFolderRow inserts a folder under parent (nil = root). Caller must hold
// a transaction and have validated the name.
func createFolderRow(tx *gorm.DB, mailboxID uint, parent *models.MailFolder, name string) (*models.MailFolder, error) {
	if err := validateFolderName(name); err != nil {
		return nil, err
	}
	path := name
	if parent != nil {
		path = parent.IMAPPath + IMAPDelimiter + name
	}
	path = canonicalizeFirstSegment(path)
	// Explicit path-uniqueness check (the DB's unique index on
	// (mailbox_id, imap_path) remains the backstop).
	var n int64
	if err := tx.Model(&models.MailFolder{}).
		Where("mailbox_id = ? AND imap_path = ?", mailboxID, path).
		Count(&n).Error; err != nil {
		return nil, fmt.Errorf("check folder path %q: %w", path, err)
	}
	if n > 0 {
		return nil, ErrFolderExists
	}
	v, err := mintUIDValidity()
	if err != nil {
		return nil, err
	}
	f := &models.MailFolder{
		MailboxID:   mailboxID,
		Name:        name,
		IMAPPath:    path,
		UIDValidity: v,
		UIDNext:     1,
		ModSeqNext:  1,
		Subscribed:  true,
	}
	if parent != nil {
		pid := parent.ID
		f.ParentID = &pid
	}
	if err := tx.Create(f).Error; err != nil {
		return nil, fmt.Errorf("create folder %q: %w", path, err)
	}
	return f, nil
}

// CreateFolder creates a folder under the given parent path (empty = root),
// resolving the parent through IMAP-path semantics.
func (s *Service) CreateFolder(mailboxID, userID, workspaceID uint, parentPath, name string) (*models.MailFolder, error) {
	var created *models.MailFolder
	err := s.db.Transaction(func(tx *gorm.DB) error {
		mb, err := s.resolveMailbox(tx, mailboxID, userID, workspaceID)
		if err != nil {
			return err
		}
		if mb.Status != models.MailboxStatusActive {
			return ErrMailboxInactive
		}
		var parent *models.MailFolder
		if parentPath != "" {
			parent, err = findFolderByPath(tx, mailboxID, parentPath)
			if err != nil {
				return err
			}
		}
		created, err = createFolderRow(tx, mailboxID, parent, name)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// findFolderByPath resolves a folder by IMAP path within a mailbox, with
// INBOX case-insensitivity on the first segment.
func findFolderByPath(tx *gorm.DB, mailboxID uint, path string) (*models.MailFolder, error) {
	path = canonicalizeFirstSegment(strings.Trim(path, IMAPDelimiter))
	var f models.MailFolder
	err := tx.Where("mailbox_id = ? AND imap_path = ?", mailboxID, path).First(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrFolderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find folder %q: %w", path, err)
	}
	return &f, nil
}

// GetFolderByPath resolves a folder by IMAP path with scoping.
func (s *Service) GetFolderByPath(mailboxID, userID, workspaceID uint, path string) (*models.MailFolder, error) {
	var out *models.MailFolder
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.resolveMailbox(tx, mailboxID, userID, workspaceID); err != nil {
			return err
		}
		f, err := findFolderByPath(tx, mailboxID, path)
		if err != nil {
			return err
		}
		out = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListFolders returns a mailbox's folders ordered by path.
func (s *Service) ListFolders(mailboxID, userID, workspaceID uint) ([]models.MailFolder, error) {
	var out []models.MailFolder
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.resolveMailbox(tx, mailboxID, userID, workspaceID); err != nil {
			return err
		}
		if err := tx.Where("mailbox_id = ?", mailboxID).Order("imap_path ASC").Find(&out).Error; err != nil {
			return fmt.Errorf("list folders of mailbox %d: %w", mailboxID, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetSubscribed toggles the IMAP subscription flag.
func (s *Service) SetSubscribed(folderID, userID, workspaceID uint, subscribed bool) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, _, err := s.resolveFolder(tx, folderID, userID, workspaceID); err != nil {
			return err
		}
		return tx.Model(&models.MailFolder{}).Where("id = ?", folderID).
			Update("subscribed", subscribed).Error
	})
}

// RenameFolder renames a folder and rewrites every descendant's IMAP path
// prefix in the same transaction. Renaming INBOX is refused: RFC 9051 gives
// INBOX move-to-subfolder semantics the store does not model yet (documented
// limitation, refined by OB-4 if needed).
func (s *Service) RenameFolder(folderID, userID, workspaceID uint, newName string) (*models.MailFolder, error) {
	var out *models.MailFolder
	err := s.db.Transaction(func(tx *gorm.DB) error {
		f, mb, err := s.resolveFolder(tx, folderID, userID, workspaceID)
		if err != nil {
			return err
		}
		if strings.EqualFold(f.IMAPPath, inboxPath) {
			return ErrFolderImmutable
		}
		if err := validateFolderName(newName); err != nil {
			return err
		}
		newPath := f.IMAPPath
		if i := strings.LastIndex(f.IMAPPath, IMAPDelimiter); i >= 0 {
			newPath = f.IMAPPath[:i+1] + newName
		} else {
			newPath = newName
		}
		newPath = canonicalizeFirstSegment(newPath)
		// Collision check against the target path (a different folder holding
		// it already) — resolved in Go so the rename of a folder onto itself
		// (same path) is a no-op, not an error.
		existing, err := findFolderByPath(tx, mb.ID, newPath)
		if err == nil && existing.ID != f.ID {
			return ErrFolderExists
		}
		if err != nil && !errors.Is(err, ErrFolderNotFound) {
			return err
		}
		if err := tx.Model(&models.MailFolder{}).Where("id = ?", f.ID).
			Updates(map[string]interface{}{"name": newName, "imap_path": newPath}).Error; err != nil {
			return fmt.Errorf("rename folder %d: %w", f.ID, err)
		}
		// Rewrite descendant paths in Go: load the mailbox's folders, prefix-
		// match the old subtree, update each affected row. Folder counts are
		// small and this stays portable across drivers.
		var all []models.MailFolder
		if err := tx.Where("mailbox_id = ?", mb.ID).Find(&all).Error; err != nil {
			return fmt.Errorf("load folders of mailbox %d: %w", mb.ID, err)
		}
		oldPrefix := f.IMAPPath + IMAPDelimiter
		for _, d := range all {
			if d.ID == f.ID || !strings.HasPrefix(d.IMAPPath, oldPrefix) {
				continue
			}
			np := newPath + IMAPDelimiter + strings.TrimPrefix(d.IMAPPath, oldPrefix)
			if err := tx.Model(&models.MailFolder{}).Where("id = ?", d.ID).
				Update("imap_path", np).Error; err != nil {
				return fmt.Errorf("rewrite path of folder %d: %w", d.ID, err)
			}
		}
		out = &models.MailFolder{ID: f.ID, MailboxID: mb.ID, Name: newName, IMAPPath: newPath}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteFolder deletes a folder and its whole subtree with all messages,
// decrementing blob ref counts. INBOX is refused (same rationale as rename).
func (s *Service) DeleteFolder(folderID, userID, workspaceID uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		f, mb, err := s.resolveFolder(tx, folderID, userID, workspaceID)
		if err != nil {
			return err
		}
		if strings.EqualFold(f.IMAPPath, inboxPath) {
			return ErrFolderImmutable
		}
		// Collect the subtree in Go (prefix match on the loaded folder set),
		// purge its messages, then delete the folders.
		var all []models.MailFolder
		if err := tx.Where("mailbox_id = ?", mb.ID).Find(&all).Error; err != nil {
			return fmt.Errorf("load folders of mailbox %d: %w", mb.ID, err)
		}
		oldPrefix := f.IMAPPath + IMAPDelimiter
		ids := make([]uint, 0, 8)
		for _, d := range all {
			if d.ID == f.ID || strings.HasPrefix(d.IMAPPath, oldPrefix) {
				ids = append(ids, d.ID)
			}
		}
		for _, id := range ids {
			if err := purgeFolderMessages(tx, id, mb.ID); err != nil {
				return err
			}
		}
		if err := tx.Where("id IN ?", ids).Delete(&models.MailFolder{}).Error; err != nil {
			return fmt.Errorf("delete folder subtree %d: %w", f.ID, err)
		}
		return nil
	})
}
