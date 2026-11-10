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
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/mailstore"
)

// The stored-copy builder and the special-folder helpers. The Sent/Drafts
// copy is the message as the user composed it, built with the SAME RFC 5322
// builder the outbound pipeline uses (email.BuildMessage), so both copies
// share structure and the mailstore parser (mailstore.ParseMessage) round-trips
// them cleanly. Recipient headers carry to/cc only — bcc is envelope-only and
// must never appear on a stored header.

const (
	sentFolderPath   = "Sent"
	draftsFolderPath = "Drafts"
)

// ensureFolder returns the mailbox's folder at path, creating it at the root
// when missing. The mailstore's ownership scoping applies to both paths.
func (s *Service) ensureFolder(mb *models.Mailbox, userID, workspaceID uint, path string) (*models.MailFolder, error) {
	f, err := s.store.GetFolderByPath(mb.ID, userID, workspaceID, path)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, mailstore.ErrFolderNotFound) {
		return nil, fmt.Errorf("resolve %s folder of mailbox %d: %w", path, mb.ID, err)
	}
	f, err = s.store.CreateFolder(mb.ID, userID, workspaceID, "", path)
	if err != nil {
		// Lost a concurrent-create race: re-resolve once (unique
		// (mailbox_id, imap_path) index backstops the race).
		if errors.Is(err, mailstore.ErrFolderExists) {
			return s.store.GetFolderByPath(mb.ID, userID, workspaceID, path)
		}
		return nil, fmt.Errorf("create %s folder of mailbox %d: %w", path, mb.ID, err)
	}
	return f, nil
}

// buildMessage renders the stored copy of a composed message: recipient
// headers exclude bcc; the compose headers (Message-ID, the Posta UUID
// linkage) ride through like system headers. Attachment Content must be raw
// bytes (base64-encoded here, matching BuildMessage's wire expectation).
func buildMessage(in MessageInput, to, cc []string, atts []models.Attachment, composeHeaders map[string]string) []byte {
	headers := make(map[string]string, len(composeHeaders)+1)
	for k, v := range composeHeaders {
		headers[k] = v
	}
	if len(cc) > 0 {
		headers["Cc"] = joinAddresses(cc)
	}
	// Bcc is deliberately absent from the stored headers: it is envelope-only.
	encoded := make([]models.Attachment, len(atts))
	for i, a := range atts {
		encoded[i] = models.Attachment{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Content:     base64.StdEncoding.EncodeToString([]byte(a.Content)),
		}
	}
	// BuildMessage stamps Date and (absent a Message-ID header) Message-ID
	// itself, writes the Cc from headers, and produces the multipart structure
	// the transactional pipeline ships — one builder for both copies.
	return email.BuildMessage(in.From, to, in.Subject, in.HTML, in.Text, encoded, headers, "", "", false)
}

// joinAddresses renders a recipient header value.
func joinAddresses(list []string) string {
	out := ""
	for i, a := range list {
		if i > 0 {
			out += ", "
		}
		out += a
	}
	return out
}
