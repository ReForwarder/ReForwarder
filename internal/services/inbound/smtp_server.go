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
	"github.com/goposta/posta/internal/mailtrace"
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
	// router validates recipients against the inbound routing rules at RCPT
	// time (OB-2, ADR-002); nil until SetRouter, and RCPT then behaves
	// exactly as before (verified-domain check only).
	router *Router
	// verp accepts VERP bounce recipients through the dedicated return-path
	// allowlist (OB-2, ADR-002 decision 4); nil or disabled until SetVerp,
	// and RCPT then behaves exactly as before.
	verp *Verp
	// tlsrpt accepts TLS-RPT rua recipients through the dedicated report
	// allowlist (SF-31, OB-6 W4, ADR-007 §5); nil until SetTLSRPT, and RCPT
	// then behaves exactly as before.
	tlsrpt *TlsRptAllowlist
	// hostname is the receiving hostname stamped into the SF-13 Received:
	// trace header; set by NewSMTPServer from SMTPConfig.Hostname.
	hostname string
	// memGuard is the SF-108 process memory watchdog; while it reports
	// pressure, NEW sessions are refused with a temporary 451 (backpressure
	// at the front door — in-flight sessions are never aborted). nil = no
	// budget configured.
	memGuard MemoryGuard
}

func NewBackend(svc *Service, domainRepo *repositories.DomainRepository, maxMessageSize int64) *Backend {
	return &Backend{svc: svc, domainRepo: domainRepo, maxMessageSize: maxMessageSize}
}

// SetRouter wires the routing engine for RCPT-time recipient validation
// (setter-injection convention).
func (b *Backend) SetRouter(r *Router) { b.router = r }

// SetVerp wires the return-path allowlist for VERP bounce recipients
// (OB-2, ADR-002 decision 4; setter-injection convention).
func (b *Backend) SetVerp(v *Verp) { b.verp = v }

// SetTLSRPT wires the allowlist for TLS-RPT rua recipients (SF-31, OB-6 W4,
// ADR-007 §5; setter-injection convention).
func (b *Backend) SetTLSRPT(a *TlsRptAllowlist) { b.tlsrpt = a }

// SetRateLimiter enables per-IP rate limiting for new SMTP sessions.
func (b *Backend) SetRateLimiter(l *IPRateLimiter) { b.limiter = l }

// SetSecurity wires the security engine (setter-injection convention). A nil
// engine disables security enforcement for new sessions.
func (b *Backend) SetSecurity(eng *security.Engine) { b.sec = eng }

// MemoryGuard is the SF-108 pressure seam the SMTP listeners consult.
// *memorywatch.Watchdog satisfies it; nil guards report healthy.
type MemoryGuard interface {
	UnderPressure() bool
}

// SetMemoryGuard attaches the SF-108 memory-pressure watchdog (nil = none).
func (b *Backend) SetMemoryGuard(g MemoryGuard) { b.memGuard = g }

func (b *Backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	// SF-108: under a memory-budget breach, refuse new sessions with a
	// temporary 451 — the client retries later; nothing in flight is harmed.
	if b.memGuard != nil && b.memGuard.UnderPressure() {
		return nil, &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 1}, Message: "server under memory pressure, try again later"}
	}
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
	// SF-13: capture the EHLO argument here — go-smtp re-runs NewSession
	// after a STARTTLS upgrade, so both implicit-TLS and upgraded sessions
	// see the values of their own connection.
	return &session{
		backend: b,
		remote:  remote,
		guard:   guard,
		helo:    c.Hostname(),
		conn:    c,
	}, nil
}

type session struct {
	backend *Backend
	from    string
	to      []string
	remote  string
	guard   *security.SessionGuard
	// helo is the client's EHLO argument (trace-header `from` clause, SF-13).
	helo string
	// conn is the session's SMTP connection; read at DATA time so the TLS
	// state reflects any STARTTLS upgrade that happened mid-session.
	conn *smtp.Conn
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
// by failing before DATA. With routing enabled (OB-2, ADR-002) it additionally
// requires an enabled route — exact, sub-address base, or catch-all — for the
// recipient BEFORE the receiver accepts responsibility for the message: a
// verified-domain recipient with neither gets an explicit 550. A route-store
// failure is a temporary error (451), never a policy reject.
func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.guard.MarkCommand()
	addr := to
	if parsed, err := mail.ParseAddress(to); err == nil {
		addr = parsed.Address
	}
	// OB-2 (ADR-002 decision 4): VERP bounce recipients are system addresses,
	// not user mailboxes — accepted through the dedicated return-path
	// allowlist before ordinary recipient validation (the bounce host is not
	// a tenant mailbox domain, so the verified-domain check below would
	// reject them). Invalid or forged bounce addresses fall through to that
	// ordinary chain and are rejected there like any unknown recipient.
	if vp := s.backend.verp; vp != nil && vp.LooksLikeBounce(addr) {
		if uuid, ok := vp.Decode(addr); ok {
			if _, host, hok := splitAddress(addr); hok {
				if parent, pok := vp.BounceParentDomain(host); pok {
					if _, derr := s.backend.domainRepo.FindVerifiedByName(parent); derr == nil {
						_ = uuid
						s.to = append(s.to, addr)
						return nil
					}
				}
			}
		}
	}
	// SF-31 (OB-6 W4): the TLS-RPT rua address is a system address exactly
	// like the VERP bounce host — accepted through its dedicated allowlist
	// before ordinary recipient validation. Unverified domains fall through
	// to the ordinary chain and are rejected there.
	if tr := s.backend.tlsrpt; tr != nil && tr.LooksLikeReport(addr) {
		if rptDomain, ok := tr.ReportDomain(addr); ok {
			if _, derr := s.backend.domainRepo.FindVerifiedByName(rptDomain); derr == nil {
				s.to = append(s.to, addr)
				return nil
			}
		}
	}
	domain := extractDomain(addr)
	if domain == "" {
		s.guard.RecordMalformed("rcpt_rejected")
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 3}, Message: "malformed recipient"}
	}
	d, err := s.backend.domainRepo.FindVerifiedByName(domain)
	if err != nil {
		s.guard.RecordMalformed("rcpt_rejected")
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "relay not permitted"}
	}
	if rt := s.backend.router; rt != nil {
		_, _, rerr := rt.ResolveRecipient(d, addr)
		switch {
		case rerr == nil:
			// matched: the chosen action is carried into ingest from the
			// recipient list (re-resolved there against the same rules).
		case errors.Is(rerr, ErrNoRoute):
			s.guard.RecordMalformed("rcpt_rejected")
			return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "recipient not accepted"}
		default:
			// Route store unavailable — do not turn an infra failure into a
			// permanent rejection.
			logger.Error("inbound route resolution failed", "rcpt_domain", domain, "error", rerr)
			return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 4, 3}, Message: "temporary routing failure"}
		}
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
	raw, err := s.readBounded(r)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	clientIP := clientHost(s.remote)

	recipients := append([]string(nil), s.to...)
	_, ierr := s.backend.svc.IngestRaw(ctx, raw, s.from, recipients, models.InboundSourceSMTP, clientIP)
	return ingestErrorToStatus(ierr, s.remote)
}

// ingestErrorToStatus maps an IngestRaw error to the final SMTP/LMTP reply:
// nil (accepted → 250) or the typed failure. Shared by the DATA handler and
// the LMTP per-recipient status (SF-01) so both listeners reject identically.
func ingestErrorToStatus(ierr error, remote string) error {
	switch {
	case ierr == nil:
		return nil
	case errors.Is(ierr, ErrUnverifiedDomain):
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "relay not permitted"}
	case errors.Is(ierr, ErrNoRoute):
		// OB-2: routing was re-evaluated at ingest (rules changed between
		// RCPT and DATA, or the route store recovered differently) — the
		// explicit reject, persisted by IngestRaw.
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "recipient not accepted"}
	case errors.Is(ierr, ErrSizeExceeded):
		return &smtp.SMTPError{Code: 552, EnhancedCode: smtp.EnhancedCode{5, 3, 4}, Message: "message too large"}
	case errors.Is(ierr, ErrSenderSuppressed):
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "sender suppressed"}
	default:
		logger.Error("inbound ingest failed", "remote", remote, "error", ierr)
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "temporary failure"}
	}
}

// withReceivedHeader builds the SF-13 trace header from the live session
// state (HELO, peer IP, negotiated TLS) and the receiving hostname. Isolated
// as a method so tests can pin its inputs directly.
func (s *session) withReceivedHeader(recipients []string) string {
	var tlsVersion uint16
	if s.conn != nil {
		if tc, ok := s.conn.Conn().(*tls.Conn); ok {
			tlsVersion = tc.ConnectionState().Version
		}
	}
	return mailtrace.IngestReceived(s.helo, clientHost(s.remote), s.backend.hostname, tlsVersion, recipients, time.Now())
}

// clientHost reduces a host:port remote to its host part.
func clientHost(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
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
		backend.securityPort = cfg.Port  // port attached to security signals
		backend.hostname = cfg.Hostname // trace-header `by` clause (SF-13)
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
