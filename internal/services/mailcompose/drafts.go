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
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
)

// Drafts (ADR-005 decision 2): a draft is a Drafts-folder message stored
// through the mailstore's Append with the \Draft flag — the same rows an IMAP
// client would see, quota-enforced like every append. Update is the IMAP
// replace dance: the edited draft is appended as a NEW message (fresh UID) and
// the old one is marked \Deleted and expunged; Message-ID is carried over so
// identity survives edits. Send-from-draft sends the STORED draft and deletes
// it only after the send was accepted — a refused send (quota, pipeline
// refusal) leaves the draft intact.

// DraftResult reports a stored draft's addressing.
type DraftResult struct {
	MailboxID uint   `json:"mailbox_id"`
	FolderID  uint   `json:"folder_id"`
	UID       uint32 `json:"uid"`
	MessageID string `json:"message_id"`
}

// SaveDraft stores a new draft in the mailbox's Drafts folder.
func (s *Service) SaveDraft(ctx context.Context, userID, workspaceID uint, in MessageInput) (*DraftResult, error) {
	to, cc, _, err := envelopeRecipients(in)
	if err != nil {
		return nil, err
	}
	if err := validateComposeSubject(in.Subject); err != nil {
		return nil, err
	}
	staged, err := s.gateAttachments(userID, in.Attachments)
	if err != nil {
		return nil, err
	}
	mb, err := s.resolveMailbox(ctx, userID, workspaceID, in)
	if err != nil {
		return nil, err
	}
	drafts, err := s.ensureFolder(mb, userID, workspaceID, draftsFolderPath)
	if err != nil {
		return nil, err
	}
	atts, err := s.loadAttachments(ctx, staged)
	if err != nil {
		return nil, err
	}
	messageID := uuid.NewString() + "@" + domainOf(in.From)
	raw := buildMessage(in, to, cc, atts, map[string]string{
		"Message-ID": "<" + messageID + ">",
	})
	appended, err := s.store.Append(ctx, userID, workspaceID, mailstore.AppendIn{
		MailboxID: mb.ID,
		FolderID:  drafts.ID,
		Raw:       raw,
		Flags:     models.FlagDraft,
	})
	if err != nil {
		return nil, err
	}
	// Staging rows are deliberately KEPT on draft save: the composer re-refs
	// the same uploads when the user re-opens and re-saves the draft, and the
	// entry gate above must keep admitting them. The opportunistic TTL sweep
	// reclaims rows whose drafts never went out.
	return &DraftResult{
		MailboxID: mb.ID,
		FolderID:  appended.FolderID,
		UID:       appended.UID,
		MessageID: messageID,
	}, nil
}

// UpdateDraft replaces a draft: the edited copy is appended with the ORIGINAL
// Message-ID (identity survives edits) and the old message is marked \Deleted
// and expunged — the folder-wide expunge also collects any message the user
// had already deleted in Drafts, which is exactly IMAP EXPUNGE semantics.
func (s *Service) UpdateDraft(ctx context.Context, userID, workspaceID, mailboxID uint, uid uint32, in MessageInput) (*DraftResult, error) {
	// Authorization first (a foreign mailbox reads as not-found before any
	// validation leaks), then validation, then the gate.
	if _, err := s.store.GetMailbox(mailboxID, userID, workspaceID); err != nil {
		if errors.Is(err, mailstore.ErrNotMailboxOwner) || errors.Is(err, mailstore.ErrMailboxNotFound) {
			return nil, ErrNotMailboxOwner
		}
		return nil, fmt.Errorf("resolve mailbox %d: %w", mailboxID, err)
	}
	to, cc, _, err := envelopeRecipients(in)
	if err != nil {
		return nil, err
	}
	if err := validateComposeSubject(in.Subject); err != nil {
		return nil, err
	}
	staged, err := s.gateAttachments(userID, in.Attachments)
	if err != nil {
		return nil, err
	}
	drafts, err := s.ensureFolder(&models.Mailbox{ID: mailboxID}, userID, workspaceID, draftsFolderPath)
	if err != nil {
		return nil, err
	}
	old, err := s.store.GetMessageByUID(drafts.ID, uid, userID, workspaceID)
	if err != nil {
		return nil, err // ErrMessageNotFound → 404, already owner-scoped
	}
	oldMessageID := messageIDOf(old)
	if oldMessageID == "" {
		oldMessageID = uuid.NewString() + "@" + domainOf(in.From)
	}
	atts, err := s.loadAttachments(ctx, staged)
	if err != nil {
		return nil, err
	}
	in.MailboxID = mailboxID
	raw := buildMessage(in, to, cc, atts, map[string]string{
		"Message-ID": "<" + oldMessageID + ">",
	})
	appended, err := s.store.Append(ctx, userID, workspaceID, mailstore.AppendIn{
		MailboxID: mailboxID,
		FolderID:  drafts.ID,
		Raw:       raw,
		Flags:     models.FlagDraft,
	})
	if err != nil {
		return nil, err
	}
	// Replace: mark the old revision deleted and purge it (counter and blob
	// bookkeeping run inside the mailstore's transactions).
	if err := s.store.MarkDeleted(userID, workspaceID, old.ID); err != nil {
		return nil, fmt.Errorf("mark old draft deleted: %w", err)
	}
	if _, err := s.store.Expunge(drafts.ID, userID, workspaceID); err != nil {
		return nil, fmt.Errorf("expunge replaced draft: %w", err)
	}
	// Staging rows survive updates (the entry gate must keep admitting the
	// refs the composer still holds); only an accepted send consumes them.
	return &DraftResult{
		MailboxID: mailboxID,
		FolderID:  appended.FolderID,
		UID:       appended.UID,
		MessageID: oldMessageID,
	}, nil
}

// SendDraft sends the STORED draft (content read back from the Drafts copy —
// the staging gate ran when the draft was saved or last edited, and the raw
// message is immutable, so no re-gating is needed) and deletes the draft only
// after the send was accepted.
func (s *Service) SendDraft(ctx context.Context, userID, workspaceID, mailboxID uint, uid uint32) (*SendResult, error) {
	if _, err := s.store.GetMailbox(mailboxID, userID, workspaceID); err != nil {
		if errors.Is(err, mailstore.ErrNotMailboxOwner) || errors.Is(err, mailstore.ErrMailboxNotFound) {
			return nil, ErrNotMailboxOwner
		}
		return nil, fmt.Errorf("resolve mailbox %d: %w", mailboxID, err)
	}
	drafts, err := s.ensureFolder(&models.Mailbox{ID: mailboxID}, userID, workspaceID, draftsFolderPath)
	if err != nil {
		return nil, err
	}
	old, err := s.store.GetMessageByUID(drafts.ID, uid, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	raw, err := s.store.GetMessageBody(ctx, old.ID, userID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load draft body: %w", err)
	}
	parsed, err := mailstore.ParseMessage(raw)
	if err != nil {
		return nil, fmt.Errorf("parse draft: %w", err)
	}
	if parsed.From == "" {
		return nil, errors.New("draft has no sender address")
	}
	in := MessageInput{
		MailboxID: mailboxID,
		From:      parsed.From,
		To:        parsed.To,
		Cc:        parsed.Cc,
		Subject:   parsed.Subject,
		Text:      parsed.TextBody,
		HTML:      parsed.HTMLBody,
	}
	// Parsed attachments carry the exact bytes the draft stored.
	draftAtts := make([]models.Attachment, 0, len(parsed.Attachments))
	for _, a := range parsed.Attachments {
		draftAtts = append(draftAtts, models.Attachment{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Content:     string(a.Content),
		})
	}

	// Send the draft's content through the standard accepted-send flow. The
	// draft's recipient header set (to/cc) is the envelope here — a stored
	// draft never carries bcc (it was envelope-only at save time and the
	// draft-send path re-derives everything from the stored copy).
	in.Attachments = nil
	res, err := s.sendPrepared(ctx, userID, workspaceID, in, draftAtts, nil)
	if err != nil {
		// Refused send: the draft stays. The caller maps typed errors.
		return nil, err
	}
	// Accepted: release any staging reservations these attachments still hold
	// (only rows that exist — content the TTL sweep already released is
	// carried by the draft/sent ref counts alone), then delete the draft.
	for _, a := range draftAtts {
		if row, err := s.staging.FindByUserAndHash(userID, hashOf([]byte(a.Content))); err == nil && row != nil {
			s.consumeStaging(ctx, userID, []*models.MailAttachmentUpload{row})
		}
	}
	if err := s.store.MarkDeleted(userID, workspaceID, old.ID); err != nil {
		return res, nil // sent; the draft lingers as \Deleted — logged upstream
	}
	if _, err := s.store.Expunge(drafts.ID, userID, workspaceID); err != nil {
		return res, nil // sent; expunge is housekeeping
	}
	return res, nil
}

// messageIDOf extracts a stored message's bare Message-ID.
func messageIDOf(m *models.MailMessage) string {
	v := strings.TrimSpace(m.MessageID)
	v = strings.Trim(v, "<> \t")
	return v
}

// replyPrefixRE guards against stacking Re: on an already-prefixed subject.
var replyPrefixRE = regexp.MustCompile(`(?i)^\s*(re|aw|sv)\s*:\s*`)
var forwardPrefixRE = regexp.MustCompile(`(?i)^\s*(fwd|fw)\s*:\s*`)

// replySubject prefixes "Re:" unless the subject already carries it.
func replySubject(subject string) string {
	if replyPrefixRE.MatchString(subject) {
		return subject
	}
	return "Re: " + subject
}

// forwardSubject prefixes "Fwd:" unless already present.
func forwardSubject(subject string) string {
	if forwardPrefixRE.MatchString(subject) {
		return subject
	}
	return "Fwd: " + subject
}

// dedupAddresses merges address lists case-insensitively, dropping the
// excluded set (the reply target, the caller's own address).
func dedupAddresses(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, a := range list {
			key := strings.ToLower(addressOnly(a))
			if key == "" || seen[key] {
				continue
			}
			if _, err := mail.ParseAddress(a); err != nil {
				continue
			}
			seen[key] = true
			out = append(out, a)
		}
	}
	return out
}
