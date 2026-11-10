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
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
)

// Read-side of the compose seam: the reply/forward frame the composer
// prefills from (ADR-005 W3 item 4 — headers prefill only; no server-side
// send changes), and the delivery-status view that ties a Sent-folder copy
// back to its outbound email_events timeline.

// Frame modes of the reply-frame endpoint.
const (
	FrameReply    = "reply"
	FrameReplyAll = "reply_all"
	FrameForward  = "forward"
)

// maxQuoteLines bounds the quoted text the frame returns — a prefill for the
// composer, not a full-message transfer (the message view fetches the body
// itself).
const maxQuoteLines = 200

// ReplyFrame is the prefill payload for the frontend composer.
type ReplyFrame struct {
	Mode      string `json:"mode"`
	MailboxID uint   `json:"mailbox_id"`
	FolderID  uint   `json:"folder_id"`
	UID       uint32 `json:"uid"`
	// Threading headers of the message being answered. The send schema does
	// not carry threading headers in this wave (the ask's reply framing is
	// prefill-only), so these are informational for the frontend.
	InReplyTo  string   `json:"in_reply_to,omitempty"`
	References string   `json:"references,omitempty"`
	To         []string `json:"to"`
	Cc         []string `json:"cc"`
	Subject    string   `json:"subject"`
	// QuoteText is the bounded, "> "-prefixed plain-text quote.
	QuoteText string `json:"quote_text"`
	// OriginalHTML is the original message's HTML body for the sandboxed
	// iframe quote view — remote content stays BLOCKED and sanitization is
	// the frontend renderer's job (ADR-005 decision 5); the backend only
	// hands over stored bytes it itself parsed.
	OriginalHTML string        `json:"original_html,omitempty"`
	Original     FrameOriginal `json:"original"`
}

// FrameOriginal summarizes the source message above the quote.
type FrameOriginal struct {
	From    string    `json:"from"`
	Date    time.Time `json:"date"`
	Subject string    `json:"subject"`
}

// ReplyFrame builds the composer prefill for one stored message. mode is
// reply (default), reply_all, or forward. Ownership follows the mailstore's
// folder scoping: a foreign folder/UID reads as not-found.
func (s *Service) ReplyFrame(ctx context.Context, userID, workspaceID, mailboxID, folderID uint, uid uint32, mode string) (*ReplyFrame, error) {
	if mode == "" {
		mode = FrameReply
	}
	switch mode {
	case FrameReply, FrameReplyAll, FrameForward:
	default:
		return nil, fmt.Errorf("mailcompose: unknown reply-frame mode %q", mode)
	}
	m, err := s.store.GetMessageByUID(folderID, uid, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	raw, err := s.store.GetMessageBody(ctx, m.ID, userID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load message body: %w", err)
	}
	parsed, err := mailstore.ParseMessage(raw)
	if err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	// Reply-To and References are outside ParsedMessage; read them straight
	// from the raw headers.
	replyTo, references := threadingHeaders(raw)

	frame := &ReplyFrame{
		Mode:      mode,
		MailboxID: mailboxID,
		FolderID:  folderID,
		UID:       uid,
		Subject:   parsed.Subject,
		Original: FrameOriginal{
			From:    parsed.From,
			Date:    parsed.Date,
			Subject: parsed.Subject,
		},
		OriginalHTML: parsed.HTMLBody,
		QuoteText:    quoteText(parsed.TextBody, parsed.From, parsed.Date),
	}
	switch mode {
	case FrameForward:
		frame.Subject = forwardSubject(parsed.Subject)
		frame.To = []string{}
		frame.Cc = []string{}
	default:
		frame.Subject = replySubject(parsed.Subject)
		// The reply target: Reply-To when present, the original sender
		// otherwise (RFC 5322 §3.6.2 semantics).
		target := parsed.From
		if replyTo != "" {
			target = replyTo
		}
		frame.InReplyTo = parsed.MessageID
		if references != "" {
			frame.References = strings.TrimSpace(references + " <" + parsed.MessageID + ">")
		} else if parsed.MessageID != "" {
			frame.References = "<" + parsed.MessageID + ">"
		}
		if mode == FrameReplyAll {
			// Everyone who could see the original, minus the reply target and
			// minus the mailbox's own address.
			var self []string
			if mb, mbErr := s.store.GetMailbox(mailboxID, userID, workspaceID); mbErr == nil {
				self = []string{mb.Name}
			}
			frame.To = dedupAddresses([]string{target})
			frame.Cc = dedupAddresses(
				excludeAddresses(parsed.To, []string{target}, self),
				excludeAddresses(parsed.Cc, []string{target}, self),
			)
		} else {
			frame.To = dedupAddresses([]string{target})
			frame.Cc = []string{}
		}
	}
	if frame.To == nil {
		frame.To = []string{}
	}
	if frame.Cc == nil {
		frame.Cc = []string{}
	}
	return frame, nil
}

// threadingHeaders extracts Reply-To and References from the raw message.
func threadingHeaders(raw []byte) (replyTo, references string) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return "", ""
	}
	replyTo = strings.TrimSpace(msg.Header.Get("Reply-To"))
	references = strings.TrimSpace(msg.Header.Get("References"))
	return replyTo, references
}

// excludeAddresses drops every address matching any exclusion (case-insensitive).
func excludeAddresses(list []string, excludes ...[]string) []string {
	blocked := map[string]bool{}
	for _, ex := range excludes {
		for _, e := range ex {
			blocked[strings.ToLower(addressOnly(e))] = true
		}
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		if blocked[strings.ToLower(addressOnly(a))] {
			continue
		}
		out = append(out, a)
	}
	return out
}

// quoteText renders the "> "-prefixed quote block with an attribution line.
func quoteText(body, from string, date time.Time) string {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(body), "\r\n", "\n"), "\n")
	if len(lines) > maxQuoteLines {
		lines = append(lines[:maxQuoteLines], "…")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "On %s, %s wrote:\n", date.Format("2006-01-02 15:04"), from)
	for _, l := range lines {
		b.WriteString("> " + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// DeliveryStatus ties a Sent-folder copy back to its outbound timeline: the
// X-Posta-Email-Uuid header the compose seam baked into the raw message names
// the Email row, whose email_events carry the delivery truth (ADR-001: never
// reported from queue acceptance, only from provider/MX truth).
type DeliveryStatus struct {
	EmailUUID   string              `json:"email_uuid"`
	Status      string              `json:"status"`
	Subject     string              `json:"subject"`
	From        string              `json:"from"`
	Recipients  []string            `json:"recipients"`
	SentAt      *time.Time          `json:"sent_at,omitempty"`
	DeliveredAt *time.Time          `json:"delivered_at,omitempty"`
	Error       string              `json:"error,omitempty"`
	Events      []models.EmailEvent `json:"events"`
}

// DeliveryStatus resolves one stored message's outbound timeline. Both legs
// are ownership-scoped: the message through the mailstore, the Email row
// through DeliveryStore.FindEmailByUUIDForUser (a foreign UUID that somehow
// ended up in a stored message still reads as not-found for this user).
func (s *Service) DeliveryStatus(ctx context.Context, userID, workspaceID, folderID uint, uid uint32) (*DeliveryStatus, error) {
	if s.delivery == nil {
		return nil, errors.New("mailcompose: delivery history is not wired")
	}
	m, err := s.store.GetMessageByUID(folderID, uid, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	raw, err := s.store.GetMessageBody(ctx, m.ID, userID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load message body: %w", err)
	}
	composeUUID := composeUUIDOf(raw)
	if composeUUID == "" {
		// Not a compose-produced copy (e.g. an inbound message): no outbound
		// timeline exists for it.
		return nil, mailstore.ErrMessageNotFound
	}
	em, err := s.delivery.FindEmailByUUIDForUser(composeUUID, userID)
	if err != nil || em == nil {
		// The row may have been scrubbed by retention; without it there is no
		// timeline to show.
		return nil, mailstore.ErrMessageNotFound
	}
	events, err := s.delivery.ListEventsByEmailUUID(composeUUID)
	if err != nil {
		return nil, fmt.Errorf("load delivery events: %w", err)
	}
	if events == nil {
		events = []models.EmailEvent{}
	}
	out := &DeliveryStatus{
		EmailUUID:   composeUUID,
		Status:      string(em.Status),
		Subject:     em.Subject,
		From:        em.Sender,
		Recipients:  []string(em.Recipients),
		SentAt:      em.SentAt,
		DeliveredAt: em.DeliveredAt,
		Error:       em.ErrorMessage,
		Events:      events,
	}
	if out.Recipients == nil {
		out.Recipients = []string{}
	}
	return out, nil
}

// composeUUIDOf reads the X-Posta-Email-Uuid linkage header off a stored raw
// message.
func composeUUIDOf(raw []byte) string {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(msg.Header.Get("X-Posta-Email-Uuid"))
}
