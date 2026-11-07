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

package inbound

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/security"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
)

// SMTPConfig holds configuration for the built-in SMTP receiver.
type SMTPConfig struct {
	Host           string
	Port           int
	Hostname       string
	MaxMessageSize int64
	TLSMode        string // "none" or "starttls"
	TLSCertFile    string
	TLSKeyFile     string
}

// Backend implements smtp.Backend for go-smtp.
type Backend struct {
	svc            *Service
	domainRepo     *repositories.DomainRepository
	maxMessageSize int64
	limiter        *IPRateLimiter
	sec            *security.Engine
	securityPort   int
}

func NewBackend(svc *Service, domainRepo *repositories.DomainRepository, maxMessageSize int64) *Backend {
	return &Backend{svc: svc, domainRepo: domainRepo, maxMessageSize: maxMessageSize}
}

// SetRateLimiter enables per-IP rate limiting for new SMTP sessions.
func (b *Backend) SetRateLimiter(l *IPRateLimiter) { b.limiter = l }

// SetSecurity wires the security engine (setter-injection convention). A nil
// engine disables security enforcement for new sessions.
func (b *Backend) SetSecurity(eng *security.Engine) { b.sec = eng }

func (b *Backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	remote := c.Conn().RemoteAddr().String()
	if b.limiter != nil {
		ip := remote
		if host, _, err := net.SplitHostPort(remote); err == nil {
			ip = host
		}
		if !b.limiter.Allow(ip) {
			logger.Warn("smtp: rate-limited remote", "ip", ip)
			return nil, &smtp.SMTPError{Code: 421, EnhancedCode: smtp.EnhancedCode{4, 7, 0}, Message: "rate limit exceeded"}
		}
	}
	guard := security.NewSessionGuard(b.sec, security.ParseRemoteIP(remote), b.securityPort, "smtp-inbound")
	guard.WatchConn(c)
	if err := guard.GuardNewSession(); err != nil {
		return nil, err
	}
	return &session{
		backend: b,
		remote:  remote,
		guard:   guard,
	}, nil
}

type session struct {
	backend *Backend
	from    string
	to      []string
	remote  string
	guard   *security.SessionGuard
}

func (s *session) Reset() {
	s.guard.MarkCommand()
	s.from = ""
	s.to = nil
}

func (s *session) Logout() error {
	s.guard.OnLogout()
	return nil
}

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	s.guard.MarkCommand()
	if from != "" {
		if addr, err := mail.ParseAddress(from); err == nil {
			s.from = addr.Address
		} else {
			s.from = from
		}
	}
	return nil
}

// Rcpt rejects recipients whose domain is not ownership-verified, saving bandwidth
// by failing before DATA.
func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.guard.MarkCommand()
	addr := to
	if parsed, err := mail.ParseAddress(to); err == nil {
		addr = parsed.Address
	}
	domain := extractDomain(addr)
	if domain == "" {
		s.guard.RecordMalformed("rcpt_rejected")
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 3}, Message: "malformed recipient"}
	}
	if _, err := s.backend.domainRepo.FindVerifiedByName(domain); err != nil {
		s.guard.RecordMalformed("rcpt_rejected")
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "relay not permitted"}
	}
	s.to = append(s.to, addr)
	return nil
}

// Data reads the raw message (bounded by MaxMessageSize) and hands the bytes
// to the store-first ingest path. Parsing is deferred to the async worker,
// so a malformed MIME body or a Postgres encoding error during structured
// persistence no longer maps to 451 — once IngestRaw succeeds, the message
// is durably stored and the MTA gets 250.
func (s *session) Data(r io.Reader) error {
	s.guard.MarkCommand()
	limit := s.backend.maxMessageSize
	if limit <= 0 {
		limit = 26214400
	}
	// +1 to detect overflow.
	lr := &io.LimitedReader{R: r, N: limit + 1}
	raw, err := io.ReadAll(lr)
	if err != nil {
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "read error"}
	}
	if int64(len(raw)) > limit {
		return &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "message size exceeds limit"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	clientIP := s.remote
	if host, _, err := net.SplitHostPort(s.remote); err == nil {
		clientIP = host
	}

	recipients := append([]string(nil), s.to...)
	_, ierr := s.backend.svc.IngestRaw(ctx, raw, s.from, recipients, models.InboundSourceSMTP, clientIP)
	switch {
	case ierr == nil:
		return nil
	case errors.Is(ierr, ErrUnverifiedDomain):
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "relay not permitted"}
	case errors.Is(ierr, ErrSizeExceeded):
		return &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "message too large"}
	case errors.Is(ierr, ErrSenderSuppressed):
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "sender suppressed"}
	default:
		logger.Error("inbound ingest failed", "remote", s.remote, "error", ierr)
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "temporary failure"}
	}
}

// NewSMTPServer builds a configured *smtp.Server. When cfg.TLSMode == "starttls",
// STARTTLS is automatically advertised by go-smtp once TLSConfig is set.
func NewSMTPServer(backend *Backend, cfg SMTPConfig) (*smtp.Server, error) {
	srv := smtp.NewServer(backend)
	srv.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv.Domain = cfg.Hostname
	srv.ReadTimeout = 30 * time.Second
	srv.WriteTimeout = 30 * time.Second
	srv.MaxMessageBytes = cfg.MaxMessageSize
	srv.MaxRecipients = 50
	srv.AllowInsecureAuth = true
	srv.EnableSMTPUTF8 = true
	if backend != nil {
		backend.securityPort = cfg.Port // port attached to security signals
	}

	switch strings.ToLower(strings.TrimSpace(cfg.TLSMode)) {
	case "", "none":
		// plain SMTP; STARTTLS not advertised
	case "starttls":
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load inbound TLS cert: %w", err)
		}
		srv.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			ServerName:   cfg.Hostname,
			MinVersion:   tls.VersionTLS12,
		}
	default:
		return nil, fmt.Errorf("unsupported inbound TLS mode %q (use none or starttls)", cfg.TLSMode)
	}
	return srv, nil
}

func extractDomain(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at < 0 || at == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[at+1:])
}
