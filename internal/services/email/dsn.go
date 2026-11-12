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
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package email

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/google/uuid"
)

// RFC 3464 delivery-status-notification builder (SF-10). A locally-originated
// message that permanently failed produces a multipart/report;
// report-type=delivery-status bounce back to the original envelope sender:
// per-message fields (Reporting-MTA, Arrival-Date) plus one per-recipient
// field block, reusing the same vocabulary the inbound receiver's DSN
// match-back parser (internal/services/inbound/dsn.go) reads — the two sides
// round-trip.
//
// The DSN row travels the outbound pipeline stamped
// models.EmailProvenanceSystemDSN with the complete message in TextBody; the
// worker sends it verbatim with a NULL reverse path (RFC 3464 §4.3), which
// also stops double bounces: an undeliverable DSN cannot itself be bounced.
// DKIM signing rides the existing signer — the MAILER-DAEMON address sits on
// the sender's own domain, so a domain with an active key signs its DSNs.

// maxDSNReturnedBytes bounds the message/rfc822 part (the returned original).
// Real returned messages are small; the cap keeps the part inside the
// receiver's own parse bounds (maxDSNPartBytes on the parse side).
const maxDSNReturnedBytes = 1 << 20

// DSNRecipient is one per-recipient field block of the delivery-status part.
type DSNRecipient struct {
	// Recipient is the envelope address the report is about.
	Recipient string
	// Action is the RFC 3464 §2.3.3 disposition: failed, delayed or
	// delivered. Permanent failures report "failed".
	Action string
	// Status is the enhanced status code token, e.g. "5.1.1"; empty allowed.
	Status string
	// Diagnostic is the remote MTA's diagnostic text (may be empty).
	Diagnostic string
	// RemoteMTA is the remote host that issued the failure (may be empty).
	RemoteMTA string
}

// DSNRequest describes one delivery-status notification to build.
type DSNRequest struct {
	// UserID and WorkspaceID own the stored DSN row — the tenant whose
	// message failed (the original email row's owner).
	UserID      uint
	WorkspaceID uint
	// ReportingMTA is this system's identity for the per-message block
	// (POSTA_MTA_EHLO_NAME or the process hostname).
	ReportingMTA string
	// Sender is the original envelope sender — the DSN goes TO this address.
	Sender string
	// OriginalSubject is the failed message's Subject (human-readable part).
	OriginalSubject string
	// OriginalMessageID threads References/In-Reply-To when present.
	OriginalMessageID string
	// ArrivalDate is when the message first arrived at this MTA.
	ArrivalDate time.Time
	// Recipients carries one entry per affected recipient.
	Recipients []DSNRecipient
	// OriginalRaw is the failed message's RFC 5322 bytes, returned in the
	// third part. Internally bounded at maxDSNReturnedBytes.
	OriginalRaw []byte
}

// dsnBoundary produces a MIME boundary that cannot collide with report text.
func dsnBoundary() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("dsn-%d", time.Now().UnixNano())
	}
	return "dsn-" + hex.EncodeToString(buf)
}

// dsnAddrValue renders an RFC 3464 address field value: the addr-type prefix
// plus the address, CRLF-sanitized.
func dsnAddrValue(addr string) string {
	addr = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, addr)
	return "rfc822; " + addr
}

// dsnTextValue renders a free-text field value (Diagnostic-Code body),
// sanitized and folded onto continuation lines when long.
func dsnTextValue(text string) string {
	text = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || (r < 0x20 && r != '\t') {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	if len(text) > 500 {
		text = text[:500]
	}
	return text
}

// BuildDSN renders the complete RFC 5322 multipart/report message for req.
// The result parses back through the receiver's ParseDSN with the same
// action/status/recipient values (round-trip contract).
func BuildDSN(req *DSNRequest) []byte {
	if req == nil {
		return nil
	}
	boundary := dsnBoundary()
	at := req.ArrivalDate
	if at.IsZero() {
		at = time.Now()
	}
	senderDomain := "posta.local"
	if at := strings.LastIndex(req.Sender, "@"); at >= 0 && at < len(req.Sender)-1 {
		senderDomain = req.Sender[at+1:]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "From: MAILER-DAEMON@%s (Mail Delivery System)\r\n", senderDomain)
	fmt.Fprintf(&b, "To: <%s>\r\n", req.Sender)
	b.WriteString("Subject: Undelivered Mail Returned to Sender\r\n")
	if req.OriginalMessageID != "" {
		fmt.Fprintf(&b, "References: %s\r\nIn-Reply-To: %s\r\n", req.OriginalMessageID, req.OriginalMessageID)
	}
	// RFC 3834 Auto-Submitted: auto-replied — autoresponders must not reply
	// to a DSN (the loop guard at the content level, on top of the NULL
	// reverse path at the envelope level).
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Auto-Submitted: auto-replied\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/report; report-type=delivery-status; boundary=\"%s\"\r\n\r\n", boundary)

	// Part 1: human-readable notification.
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n", boundary)
	subject := req.OriginalSubject
	if subject == "" {
		subject = "(no subject)"
	}
	fmt.Fprintf(&b, "This is an automatically generated Delivery Status Notification.\r\n\r\n"+
		"Delivery to the following recipient(s) failed permanently:\r\n\r\n")
	for _, rcpt := range req.Recipients {
		// Sanitized: a hostile recipient address must not inject headers into
		// the notification (the human-readable part carries it too).
		fmt.Fprintf(&b, "  %s\r\n", dsnAddrValue(rcpt.Recipient))
	}
	fmt.Fprintf(&b, "\r\nOriginal message subject: %s\r\n", dsnTextValue(subject))

	// Part 2: message/delivery-status.
	fmt.Fprintf(&b, "--%s\r\nContent-Type: message/delivery-status\r\n\r\n", boundary)
	fmt.Fprintf(&b, "Reporting-MTA: dns; %s\r\n", dsnTextValue(req.ReportingMTA))
	fmt.Fprintf(&b, "Arrival-Date: %s\r\n\r\n", at.Format(time.RFC1123Z))
	for _, rcpt := range req.Recipients {
		fmt.Fprintf(&b, "Original-Recipient: %s\r\n", dsnAddrValue(rcpt.Recipient))
		fmt.Fprintf(&b, "Final-Recipient: %s\r\n", dsnAddrValue(rcpt.Recipient))
		fmt.Fprintf(&b, "Action: %s\r\n", rcpt.Action)
		if rcpt.Status != "" {
			fmt.Fprintf(&b, "Status: %s\r\n", rcpt.Status)
		}
		if rcpt.Diagnostic != "" {
			fmt.Fprintf(&b, "Diagnostic-Code: smtp; %s\r\n", dsnTextValue(rcpt.Diagnostic))
		}
		if rcpt.RemoteMTA != "" {
			fmt.Fprintf(&b, "Remote-MTA: dns; %s\r\n", dsnTextValue(rcpt.RemoteMTA))
		}
		b.WriteString("\r\n")
	}

	// Part 3: the returned original, bounded.
	if len(req.OriginalRaw) > 0 {
		raw := req.OriginalRaw
		if len(raw) > maxDSNReturnedBytes {
			raw = raw[:maxDSNReturnedBytes]
		}
		fmt.Fprintf(&b, "--%s\r\nContent-Type: message/rfc822\r\n\r\n", boundary)
		b.Write(raw)
		if !strings.HasSuffix(b.String(), "\r\n") {
			b.WriteString("\r\n")
		}
	}

	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}

// DSNMailer is the SF-10 seam the worker uses to emit delivery-status
// notifications for permanently failed mail. *Service (wired at the cmd
// sites) satisfies it.
type DSNMailer interface {
	SendDSN(ctx context.Context, req *DSNRequest) (*SendResponse, error)
}

// SendDSN stores the built notification as a system_dsn row and enqueues it
// through the ordinary pipeline. The row's TextBody IS the complete RFC 5322
// message — the worker sends it verbatim (null reverse path) instead of
// re-building it. Suppression/unsubscribe layers are never consulted: the
// bounce notice is system traffic, like system_forward copies.
func (s *Service) SendDSN(ctx context.Context, req *DSNRequest) (*SendResponse, error) {
	if req == nil || req.Sender == "" || len(req.Recipients) == 0 {
		return nil, fmt.Errorf("dsn: sender and affected recipients are required")
	}
	raw := BuildDSN(req)

	em := &models.Email{
		UUID:        uuid.NewString(),
		UserID:      req.UserID,
		WorkspaceID: wsPtr(req.WorkspaceID),
		Sender:      dsnFromAddress(req.Sender),
		Recipients:  []string{req.Sender},
		Subject:     "Undelivered Mail Returned to Sender",
		TextBody:    string(raw),
		Provenance:  models.EmailProvenanceSystemDSN,
		Status:      models.EmailStatusPending,
		Provider:    ClassifyRecipients([]string{req.Sender}),
	}
	if err := s.emailRepo.Create(em); err != nil {
		return nil, fmt.Errorf("failed to store DSN: %w", err)
	}

	if s.devMode {
		em.Status = models.EmailStatusSent
		now := time.Now()
		em.SentAt = &now
		_ = s.emailRepo.Update(em)
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}
	if s.enqueuer == nil {
		return nil, fmt.Errorf("dsn: no producer wired; notification stays unsent")
	}
	em.Status = models.EmailStatusQueued
	_ = s.emailRepo.Update(em)
	if err := s.enqueuer.EnqueueEmailSend(em.ID, ""); err != nil {
		em.Status = models.EmailStatusFailed
		em.ErrorMessage = fmt.Sprintf("failed to enqueue DSN: %v", err)
		_ = s.emailRepo.Update(em)
		return nil, fmt.Errorf("failed to enqueue DSN: %w", err)
	}
	return &SendResponse{ID: em.UUID, Status: em.Status}, nil
}

// dsnFromAddress is the header From of the notification: MAILER-DAEMON on the
// original sender's domain, so the tenant's own signing key covers it.
func wsPtr(v uint) *uint { return &v }

func dsnFromAddress(sender string) string {
	if at := strings.LastIndex(sender, "@"); at >= 0 && at < len(sender)-1 {
		return "MAILER-DAEMON@" + sender[at+1:]
	}
	return "MAILER-DAEMON@posta.local"
}
