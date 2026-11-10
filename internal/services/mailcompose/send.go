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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/mailstore"
)

// Send paths of the compose seam: the accepted-send flow that files the Sent
// copy through the mailstore and hands the transport copy to the outbound
// pipeline, plus the shared address/header helpers.

// SendResult reports what an accepted compose send produced.
type SendResult struct {
	// EmailUUID keys the outbound pipeline row — the delivery-status timeline
	// (email_events) is read back under this id.
	EmailUUID string `json:"email_uuid"`
	Status    string `json:"status"`
	// MessageID is the RFC 5322 Message-ID shared by the stored Sent copy and
	// the transported message.
	MessageID string            `json:"message_id"`
	MailboxID uint              `json:"mailbox_id"`
	FolderID  uint              `json:"folder_id"`
	UID       uint32            `json:"uid"`
	Extra     map[string]string `json:"-"`
}

// Send accepts a composed message (ADR-005 decision 2). Order matters and is
// deliberate:
//
//  1. validate + gate attachments on the ADR-006 scan verdicts (a refusal
//     here must never leave state behind);
//  2. file the Sent copy through the mailstore — a quota refusal aborts the
//     send BEFORE anything enters the outbound pipeline;
//  3. push the transport copy into the outbound pipeline (SendMailCompose,
//     provenance mail-compose) and consume the staging reservations.
//
// If step 3 fails after step 2, the Sent copy truthfully remains (the user
// did compose and submit it) and its delivery-status timeline shows the
// failure — the message is not lost, not silently dropped, and not marked
// delivered.
func (s *Service) Send(ctx context.Context, userID, workspaceID uint, in MessageInput) (*SendResult, error) {
	// Entry gate: every referenced attachment must have been staged (and
	// scanned) by THIS user. Drafts re-gate the same way on save/update; a
	// send-from-draft re-reads content from its already-gated stored copy.
	staged, err := s.gateAttachments(userID, in.Attachments)
	if err != nil {
		return nil, err
	}
	atts, err := s.loadAttachments(ctx, staged)
	if err != nil {
		return nil, err
	}
	return s.sendPrepared(ctx, userID, workspaceID, in, atts, staged)
}

// sendPrepared is the accepted-send flow over prepared attachment content.
// Order matters and is deliberate:
//
//  1. validate (a refusal here must never leave state behind);
//  2. file the Sent copy through the mailstore — a quota refusal aborts the
//     send BEFORE anything enters the outbound pipeline;
//  3. push the transport copy into the outbound pipeline (SendMailCompose,
//     provenance mail-compose) and consume the staging reservations.
//
// If step 3 fails after step 2, the Sent copy truthfully remains (the user
// did compose and submit it) and its delivery-status timeline shows the
// failure — the message is not lost, not silently dropped, and not marked
// delivered.
func (s *Service) sendPrepared(ctx context.Context, userID, workspaceID uint, in MessageInput, atts []models.Attachment, staged []*models.MailAttachmentUpload) (*SendResult, error) {
	to, cc, bcc, err := envelopeRecipients(in)
	if err != nil {
		return nil, err
	}
	if len(to)+len(cc)+len(bcc) == 0 {
		return nil, ErrNothingToSend
	}
	if err := validateComposeSubject(in.Subject); err != nil {
		return nil, err
	}
	mb, err := s.resolveMailbox(ctx, userID, workspaceID, in)
	if err != nil {
		return nil, err
	}
	sentFolder, err := s.ensureFolder(mb, userID, workspaceID, sentFolderPath)
	if err != nil {
		return nil, err
	}

	// One identity for both copies: the compose mints the UUID and Message-ID
	// so the Sent copy can carry the linkage header before the outbound row
	// exists.
	composeUUID := uuid.NewString()
	fromDomain := domainOf(in.From)
	messageID := composeUUID + "@" + fromDomain

	raw := buildMessage(in, to, cc, atts, map[string]string{
		"Message-ID":         "<" + messageID + ">",
		"X-Posta-Email-Uuid": composeUUID,
	})

	// Sent copy first: quota and mailbox-status refusals abort here.
	appended, err := s.store.Append(ctx, userID, workspaceID, mailstore.AppendIn{
		MailboxID: mb.ID,
		FolderID:  sentFolder.ID,
		Raw:       raw,
		Flags:     models.FlagSeen, // a filed sent message is read
	})
	if err != nil {
		return nil, err // *mailstore.QuotaError and friends pass through typed
	}

	// The message is filed; push the transport copy into the outbound
	// pipeline. Attachments ride base64 like every transactional send.
	resp, err := s.sender.SendMailCompose(ctx, &email.MailComposeRequest{
		UUID:        composeUUID,
		UserID:      userID,
		WorkspaceID: workspacePtr(workspaceID),
		From:        in.From,
		To:          append(append(to, cc...), bcc...),
		Subject:     in.Subject,
		Text:        in.Text,
		HTML:        in.HTML,
		Attachments: base64Attachments(atts),
		MessageID:   messageID,
		MailboxID:   mb.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("outbound pipeline refused the compose (sent copy filed as uid %d): %w", appended.UID, err)
	}

	// The stored message now covers this content: drop the staging rows and
	// release their reservations (the Append reserved its own).
	s.consumeStaging(ctx, userID, staged)

	return &SendResult{
		EmailUUID: resp.ID,
		Status:    string(resp.Status),
		MessageID: messageID,
		MailboxID: mb.ID,
		FolderID:  appended.FolderID,
		UID:       appended.UID,
	}, nil
}

// loadAttachments fetches the staged attachment bytes in request order.
func (s *Service) loadAttachments(ctx context.Context, staged []*models.MailAttachmentUpload) ([]models.Attachment, error) {
	if len(staged) == 0 {
		return nil, nil
	}
	out := make([]models.Attachment, 0, len(staged))
	for _, row := range staged {
		content, err := s.store.ReadBlob(ctx, row.BlobHash)
		if err != nil {
			return nil, fmt.Errorf("load staged attachment %q: %w", row.Name, err)
		}
		out = append(out, models.Attachment{
			Filename:    row.Name,
			ContentType: row.ContentType,
			Content:     string(content), // raw bytes; encoded at the boundaries
		})
	}
	return out, nil
}

// base64Attachments renders attachments for the outbound request, whose
// contract is base64 Content (decoded by persistAttachments).
func base64Attachments(atts []models.Attachment) []models.Attachment {
	if len(atts) == 0 {
		return nil
	}
	out := make([]models.Attachment, len(atts))
	for i, a := range atts {
		out[i] = models.Attachment{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Content:     base64.StdEncoding.EncodeToString([]byte(a.Content)),
		}
	}
	return out
}

// Shared helpers.

// validateAddress checks one address (plain or RFC 5322 display-name form).
func validateAddress(a string) error {
	a = strings.TrimSpace(a)
	if a == "" {
		return errors.New("address is empty")
	}
	if strings.ContainsAny(a, "\r\n\x00") {
		return errors.New("address contains forbidden control characters")
	}
	if _, err := mail.ParseAddress(a); err != nil {
		return err
	}
	return nil
}

// parseAddressList validates a recipient list.
func parseAddressList(list []string) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, a := range list {
		if err := validateAddress(a); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSpace(a))
	}
	return out, nil
}

// addressOnly reduces one address to its bare addr-spec (best-effort: an
// unparseable value passes through trimmed).
func addressOnly(a string) string {
	a = strings.TrimSpace(a)
	if addr, err := mail.ParseAddress(a); err == nil {
		return addr.Address
	}
	return a
}

// domainOf extracts the sending domain from an address (the Message-ID
// right-hand side, mirroring the transactional builder's rule).
func domainOf(from string) string {
	addr := addressOnly(from)
	if i := strings.LastIndex(addr, "@"); i >= 0 && i+1 < len(addr) {
		return addr[i+1:]
	}
	return "posta.local"
}

// validateComposeSubject rejects header-bound control characters (R-24 shape;
// the outbound pipeline re-validates, this fails fast with a compose-typed
// error).
func validateComposeSubject(subject string) error {
	if strings.ContainsAny(subject, "\r\n\x00") {
		return errors.New("subject contains forbidden control characters (CR/LF/NUL)")
	}
	return nil
}

func hashOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func workspacePtr(workspaceID uint) *uint {
	if workspaceID == 0 {
		return nil
	}
	ws := workspaceID
	return &ws
}
