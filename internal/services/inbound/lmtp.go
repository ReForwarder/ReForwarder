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

package inbound

import (
	"context"
	"io"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/goposta/posta/internal/mailtrace"
	"github.com/goposta/posta/internal/models"
)

// LMTP listener (SF-01, RFC 2033): a local delivery agent (a fronting MX or
// external MDA) hands messages for OUR recipients straight into the same
// ingest pipeline as the SMTP receiver. Differences from SMTP per the RFC:
// LHLO instead of EHLO, and one final status per recipient after DATA.
//
// The listener reuses the inbound Backend wholesale — RCPT-time route
// validation, the security engine, rate limiting and the SF-13 trace header
// all apply unchanged. Off by default (POSTA_LMTP_ENABLED).

// NewLMTPServer builds the RFC 2033 LMTP server over the same Backend as the
// inbound SMTP receiver.
func NewLMTPServer(backend *Backend, cfg SMTPConfig) (*smtp.Server, error) {
	srv, err := NewSMTPServer(backend, cfg)
	if err != nil {
		return nil, err
	}
	srv.LMTP = true
	return srv, nil
}

// LMTPData implements go-smtp's LMTPSession: one durable ingest per
// recipient, so each recipient gets its own final status — a routed
// recipient is 250 (queued) while an unrouted one is 550, inside the same
// DATA transaction (RFC 2033 §4.2). The message bytes are read once; each
// per-recipient ingest stores its own row through the ordinary pipeline
// (route → async parse → mailbox delivery), so a per-recipient status
// reflects exactly what was accepted for that recipient. SetStatus is called
// exactly once per RCPT occurrence (duplicates included), and the nil return
// leaves no recipient status unfilled.
func (s *session) LMTPData(r io.Reader, status smtp.StatusCollector) error {
	raw, err := s.readBounded(r)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	clientIP := clientHost(s.remote)
	for _, rcpt := range s.to {
		_, ierr := s.backend.svc.IngestRaw(ctx, raw, s.from, []string{rcpt}, models.InboundSourceLMTP, clientIP)
		status.SetStatus(rcpt, ingestErrorToStatus(ierr, s.remote))
	}
	return nil
}

// readBounded reads at most maxMessageSize+1 bytes (+1 detects overflow),
// mirroring the DATA handler's bounds, and prepends the SF-13 ingest trace
// header from the same live session state (HELO, peer IP, TLS state).
func (s *session) readBounded(r io.Reader) ([]byte, error) {
	limit := s.backend.maxMessageSize
	if limit <= 0 {
		limit = 26214400
	}
	lr := &io.LimitedReader{R: r, N: limit + 1}
	raw, err := io.ReadAll(lr)
	if err != nil {
		return nil, &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "read error"}
	}
	if int64(len(raw)) > limit {
		return nil, &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "message size exceeds limit"}
	}
	return mailtrace.Prepend(raw, s.withReceivedHeader(s.to)), nil
}
