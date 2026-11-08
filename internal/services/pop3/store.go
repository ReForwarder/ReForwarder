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

package pop3

import (
	"context"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
)

// Store is the slice of the mailstore API the POP3 server consumes
// (ADR-003). The narrow interface keeps the protocol server unit-testable
// with an in-memory contract mirror and pins the production wiring to the
// real *mailstore.Service at compile time (storeContract below).
//
// Every method is ownership-scoped by (userID, workspaceID) at the mailstore
// layer — the POP3 server never passes a user other than the authenticated
// one, and workspaceID is always 0 (a user's personal mailbox domain).
type Store interface {
	// ListMailboxes returns the user's mailboxes (the server picks the
	// primary one — lowest ID, status active).
	ListMailboxes(userID, workspaceID uint) ([]models.Mailbox, error)
	// GetFolderByPath resolves a folder inside a mailbox by IMAP path
	// ("INBOX" is matched case-insensitively on its first segment).
	GetFolderByPath(mailboxID, userID, workspaceID uint, path string) (*models.MailFolder, error)
	// ListMessages pages a folder's messages in UID-ascending order
	// (POP3 message numbers are positions in that order, stable per session).
	ListMessages(folderID, userID, workspaceID uint, filter mailstore.ListFilter, page, pageSize int) (*mailstore.ListPage, error)
	// GetMessageBody returns the stored raw RFC 5322 message bytes
	// (content-addressed blob, integrity-checked).
	GetMessageBody(ctx context.Context, messageID, userID, workspaceID uint) ([]byte, error)
	// MarkDeleted sets \Deleted on one message (POP3 QUIT's UPDATE phase).
	MarkDeleted(userID, workspaceID, messageID uint) error
	// Expunge purges the folder's \Deleted messages transactionally
	// (survivors keep their UIDs; uid_validity never changes).
	Expunge(folderID, userID, workspaceID uint) (*mailstore.ExpungeResult, error)
}

// storeContract pins the production wiring: *mailstore.Service must always
// satisfy the interface, so the IMAP/POP3 seam cannot drift.
var _ Store = (*mailstore.Service)(nil)

// resolveDrop resolves the user's POP3 maildrop: the first active mailbox
// (lowest ID — ListMailboxes returns them oldest first) and its INBOX.
// A suspended or closed mailbox is refused; a closed mailbox keeps its
// messages but no longer accepts protocol sessions (the mailstore keeps
// reads open only for the owner's own drain paths).
func resolveDrop(st Store, userID uint) (*models.Mailbox, *models.MailFolder, error) {
	boxes, err := st.ListMailboxes(userID, 0)
	if err != nil {
		return nil, nil, err
	}
	var primary *models.Mailbox
	for i := range boxes {
		if boxes[i].Status == models.MailboxStatusActive {
			primary = &boxes[i]
			break
		}
	}
	if primary == nil {
		return nil, nil, mailstore.ErrMailboxNotFound
	}
	inbox, err := st.GetFolderByPath(primary.ID, userID, 0, "INBOX")
	if err != nil {
		return nil, nil, err
	}
	return primary, inbox, nil
}
