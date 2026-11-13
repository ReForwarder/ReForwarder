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

package mta

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/goposta/posta/internal/services/security"
	"github.com/goposta/posta/internal/services/smtprelay"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
)

// Submission listener (SF-03, gated; ADR-001 §10): a go-smtp-based MSA on
// port 587 (default OFF, POSTA_SUBMISSION_ENABLED) for standards-compliant
// clients. Accepted messages enter the same pipeline as API sends — the email
// service creates the row, enqueues the worker task, which claims, signs,
// delivers and records.
//
// Non-negotiable (ADR-001 #1): never an open relay. MAIL FROM, RCPT TO and
// DATA all refuse unauthenticated sessions; the sender domain must be
// ownership-verified for the authenticated tenant; recipients must parse.

// CredentialVerifier resolves and verifies submission credentials. Production
// verifies SMTP relay credentials through smtprelay's verifier (bcrypt with
// the R-25 rehash-on-success migration).
type CredentialVerifier interface {
	Verify(username, password, remoteIP string) (*models.SMTPCredential, string, error)
}

// relayCredentialVerifier is the production verifier over the relay
// credential store.
type relayCredentialVerifier struct {
	credRepo *repositories.SMTPCredentialRepository
	userRepo *repositories.UserRepository
}

// NewRelayCredentialVerifier builds the production credential verifier.
func NewRelayCredentialVerifier(credRepo *repositories.SMTPCredentialRepository, userRepo *repositories.UserRepository) CredentialVerifier {
	return &relayCredentialVerifier{credRepo: credRepo, userRepo: userRepo}
}

func (v *relayCredentialVerifier) Verify(username, password, remoteIP string) (*models.SMTPCredential, string, error) {
	cred, err := v.credRepo.FindByUsername(username)
	if err != nil || cred == nil || !cred.IsValid() {
		return nil, "", fmt.Errorf("unknown credential")
	}
	if !smtprelay.VerifyPassword(v.credRepo, cred, password) {
		return nil, "", fmt.Errorf("bad password")
	}
	if !middlewares.IPAllowed(cred.AllowedIPs, remoteIP) {
		return nil, "", fmt.Errorf("ip not allowed")
	}
	owner, err := v.userRepo.FindByID(cred.UserID)
	if err != nil {
		return nil, "", fmt.Errorf("credential owner missing")
	}
	go func() { _ = v.credRepo.UpdateLastUsed(cred.ID) }()
	return cred, owner.Email, nil
}

// SenderDomainChecker enforces that the envelope sender domain is verified
// for the authenticated tenant (non-negotiable #1). Production checks the
// workspace scope first, then the user scope.
type SenderDomainChecker interface {
	VerifiedSenderDomain(userID uint, workspaceID *uint, domain string) bool
}

// domainSenderChecker is the production checker over the domain repository.
type domainSenderChecker struct {
	repo *repositories.DomainRepository
}

// NewDomainSenderChecker builds the production sender-domain checker.
func NewDomainSenderChecker(repo *repositories.DomainRepository) SenderDomainChecker {
	return &domainSenderChecker{repo: repo}
}

func (c *domainSenderChecker) VerifiedSenderDomain(userID uint, workspaceID *uint, domain string) bool {
	if domain == "" {
		return false
	}
	if workspaceID != nil && *workspaceID > 0 && c.repo.IsOwnershipVerifiedInWorkspace(*workspaceID, domain) {
		return true
	}
	return c.repo.IsOwnershipVerified(userID, domain)
}

// SubmissionSink is the pipeline accepted messages enter: the email service
// (claim/sign/deliver/record, identical to API sends).
type SubmissionSink interface {
	Submit(ctx context.Context, userID, apiKeyID uint, workspaceID *uint, userEmail string, req *email.SendRequest) error
}

// EmailServiceSink adapts the email service to SubmissionSink.
type EmailServiceSink struct {
	Service *email.Service
}

// Submit forwards to the email service's Send pipeline.
func (s *EmailServiceSink) Submit(ctx context.Context, userID, apiKeyID uint, workspaceID *uint, userEmail string, req *email.SendRequest) error {
	_, err := s.Service.Send(ctx, userID, apiKeyID, workspaceID, userEmail, req)
	return err
}

// SubmissionBackend implements go-smtp Backend for the MSA listener.
type SubmissionBackend struct {
	verifier       CredentialVerifier
	domains        SenderDomainChecker
	sink           SubmissionSink
	maxMessageSize int64
	limiter        *inbound.IPRateLimiter
	sec            *security.Engine
	securityPort   int
	// memGuard is the SF-108 memory-pressure watchdog (nil = no budget).
	memGuard MemoryGuard
	// domainPolicy resolves the SF-17 per-domain outbound size cap
	// (nil = no per-domain cap; the listener-wide cap still applies).
	domainPolicy DomainPolicy
}

// NewSubmissionBackend assembles the MSA backend. All three collaborators are
// required — a backend missing any of them would risk an open relay, so nil
// is refused at construction.
func NewSubmissionBackend(verifier CredentialVerifier, domains SenderDomainChecker, sink SubmissionSink, maxMessageSize int64) (*SubmissionBackend, error) {
	if verifier == nil || domains == nil || sink == nil {
		return nil, fmt.Errorf("submission backend requires a credential verifier, sender-domain checker and sink (no open relay)")
	}
	if maxMessageSize <= 0 {
		maxMessageSize = 26214400
	}
	return &SubmissionBackend{
		verifier:       verifier,
		domains:        domains,
		sink:           sink,
		maxMessageSize: maxMessageSize,
	}, nil
}

// SetRateLimiter enables per-IP rate limiting for new MSA sessions.
func (b *SubmissionBackend) SetRateLimiter(l *inbound.IPRateLimiter) { b.limiter = l }

// SetSecurity wires the security engine (setter-injection convention). A nil
// engine disables security enforcement for new sessions.
func (b *SubmissionBackend) SetSecurity(eng *security.Engine) { b.sec = eng }

// SetSecurityPort attaches the listener port to security signals.
func (b *SubmissionBackend) SetSecurityPort(port int) { b.securityPort = port }

// SubmissionConfig holds listener configuration.
type SubmissionConfig struct {
	Host           string
	Port           int
	Hostname       string
	MaxMessageSize int64
	TLSMode        string // "none" or "starttls"
	TLSCertFile    string
	TLSKeyFile     string
	// TLSGetCertificate is the dynamic-certificate seam (OB-6 wave W2,
	// SF-34/ADR-007 decision 3): with TLSMode=starttls and no static
	// certificate files, the STARTTLS handshake serves certificates through
	// this callback — the certmanager SNI map (per-domain ACME certs with
	// the static fallback). A configured TLSCertFile/TLSKeyFile pair always
	// wins; the callback is only consulted when no files are set.
	TLSGetCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
}

// NewSubmissionServer configures the go-smtp server for the MSA listener.
//
// SF-09: the server advertises and accepts DELIVERBY (RFC 2852) — the
// dependency parses the BY parameter as an RCPT TO option (go-smtp v0.25.0
// conn.go, a deviation from RFC 2852 §4, which defines BY on MAIL FROM) and
// the session carries the by-time into the send pipeline as the message's
// queue-expiry deadline, so a message past its deliver-by is expired with a
// DSN-style event by the SF-08 expiry machinery. MinimumDeliverByTime stays
// unset (no server-side minimum); the session refuses non-positive by-times
// itself. FUTURERELEASE (RFC 4865) is NOT supported: the dependency has no
// parameter or backend hook for it — unknown MAIL FROM arguments draw its
// hardcoded 500 5.5.4 "Unknown MAIL FROM argument" (conn.go's MAIL FROM
// switch default case), with no clean extension point in v0.25.0.
func NewSubmissionServer(backend *SubmissionBackend, cfg SubmissionConfig) (*gosmtp.Server, error) {
	srv := gosmtp.NewServer(backend)
	srv.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv.Domain = cfg.Hostname
	srv.ReadTimeout = 60 * time.Second
	srv.WriteTimeout = 60 * time.Second
	srv.MaxMessageBytes = cfg.MaxMessageSize
	srv.MaxRecipients = 50
	srv.EnableSMTPUTF8 = true
	srv.EnableDELIVERBY = true
	if backend != nil {
		backend.securityPort = cfg.Port
	}
	switch strings.ToLower(strings.TrimSpace(cfg.TLSMode)) {
	case "", "none":
		// Self-hosted private-network MSA: AUTH is allowed over plaintext
		// (go-smtp AllowInsecureAuth) so LAN clients can submit without a
		// certificate. Operators can require STARTTLS via config.
		srv.AllowInsecureAuth = true
	case "starttls":
		switch {
		case cfg.TLSCertFile != "" && cfg.TLSKeyFile != "":
			cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
			if err != nil {
				return nil, fmt.Errorf("load submission TLS cert: %w", err)
			}
			srv.TLSConfig = &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			}
		case cfg.TLSGetCertificate != nil:
			// ACME SNI map (SF-34): per-domain certificates resolved at the
			// STARTTLS handshake, cached in the blob store.
			srv.TLSConfig = &tls.Config{
				GetCertificate: cfg.TLSGetCertificate,
				MinVersion:     tls.VersionTLS12,
			}
		default:
			return nil, fmt.Errorf("submission TLS mode starttls requires POSTA_SUBMISSION_TLS_CERT_FILE/POSTA_SUBMISSION_TLS_KEY_FILE or an ACME certificate manager")
		}
	default:
		return nil, fmt.Errorf("unsupported submission TLS mode %q (use none or starttls)", cfg.TLSMode)
	}
	return srv, nil
}

// submissionSession is one MSA connection.
type submissionSession struct {
	backend    *SubmissionBackend
	remote     string
	guard      *security.SessionGuard
	credential *models.SMTPCredential
	ownerEmail string
	from       string
	to         []string
	// deliverBy is the strictest (shortest) DELIVERBY by-time carried by this
	// transaction's RCPT TO parameters (SF-09): the deadline is enforced
	// per-message, so multiple recipients with different by-times resolve to
	// the shortest one. Zero = no BY parameter was carried.
	deliverBy time.Duration
}

// MemoryGuard is the SF-108 pressure seam; *memorywatch.Watchdog satisfies
// it. nil guards report healthy.
type MemoryGuard interface {
	UnderPressure() bool
}

// SetMemoryGuard attaches the SF-108 memory-pressure watchdog (nil = none):
// new MSA sessions are refused with a temporary 451 while the process is
// over its memory budget; in-flight sessions are never aborted.
func (b *SubmissionBackend) SetMemoryGuard(g MemoryGuard) { b.memGuard = g }

// DomainPolicy is the SF-17 seam resolving a sender domain's outbound size
// cap. *repositories.DomainRepository satisfies it; nil disables the
// per-domain cap (the listener-wide MaxMessageBytes still applies).
type DomainPolicy interface {
	MaxMessageSizeFor(userID uint, workspaceID *uint, domain string) int64
}

// SetDomainPolicy attaches the per-domain size-cap resolver (SF-17).
func (b *SubmissionBackend) SetDomainPolicy(p DomainPolicy) { b.domainPolicy = p }

func (b *SubmissionBackend) NewSession(c *gosmtp.Conn) (gosmtp.Session, error) {
	// SF-108: backpressure at the front door under a memory-budget breach.
	if b.memGuard != nil && b.memGuard.UnderPressure() {
		return nil, &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 1}, Message: "server under memory pressure, try again later"}
	}
	remote := c.Conn().RemoteAddr().String()
	ip := remote
	if host, _, err := net.SplitHostPort(remote); err == nil {
		ip = host
	}
	if b.limiter != nil && !b.limiter.Allow(ip) {
		logger.Warn("submission: rate-limited remote", "ip", ip)
		return nil, &gosmtp.SMTPError{Code: 421, EnhancedCode: gosmtp.EnhancedCode{4, 7, 0}, Message: "rate limit exceeded"}
	}
	guard := security.NewSessionGuard(b.sec, security.ParseRemoteIP(remote), b.securityPort, "smtp-submission")
	guard.WatchConn(c)
	if err := guard.GuardNewSession(); err != nil {
		return nil, err
	}
	return &submissionSession{backend: b, remote: remote, guard: guard}, nil
}

func (s *submissionSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *submissionSession) Auth(mech string) (sasl.Server, error) {
	s.guard.MarkCommand()
	if mech != sasl.Plain {
		return nil, gosmtp.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		ip := s.remote
		if host, _, err := net.SplitHostPort(s.remote); err == nil {
			ip = host
		}
		cred, ownerEmail, err := s.backend.verifier.Verify(username, password, ip)
		if err != nil {
			s.guard.RecordAuthFailure(username)
			return gosmtp.ErrAuthFailed
		}
		s.credential = cred
		s.ownerEmail = ownerEmail
		return nil
	}), nil
}

func (s *submissionSession) Reset() {
	s.guard.MarkCommand()
	s.from = ""
	s.to = nil
	s.deliverBy = 0
}

func (s *submissionSession) Logout() error {
	s.guard.OnLogout()
	return nil
}

func (s *submissionSession) Mail(from string, opts *gosmtp.MailOptions) error {
	s.guard.MarkCommand()
	// Non-negotiable: authenticated tenant required — no open relay.
	if s.credential == nil {
		return gosmtp.ErrAuthRequired
	}
	if from == "" {
		return &gosmtp.SMTPError{Code: 553, EnhancedCode: gosmtp.EnhancedCode{5, 1, 8}, Message: "sender address required on the submission port"}
	}
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return &gosmtp.SMTPError{Code: 553, EnhancedCode: gosmtp.EnhancedCode{5, 1, 8}, Message: "invalid sender address"}
	}
	s.from = addr.Address

	// Sender domain must be tenant-verified (fail closed).
	domain := senderDomainOf(addr.Address)
	workspaceID := s.credential.WorkspaceID
	if !s.backend.domains.VerifiedSenderDomain(s.credential.UserID, &workspaceID, domain) {
		return &gosmtp.SMTPError{
			Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
			Message: "sender domain not verified for this tenant",
		}
	}

	// SF-17: the sender domain's outbound size cap — declared SIZE at MAIL
	// is checked before any body is read, saving the transfer entirely.
	if s.backend.domainPolicy != nil && opts != nil && opts.Size > 0 {
		if max := s.backend.domainPolicy.MaxMessageSizeFor(s.credential.UserID, &workspaceID, domain); max > 0 && opts.Size > max {
			return &gosmtp.SMTPError{
				Code: 552, EnhancedCode: gosmtp.EnhancedCode{5, 3, 4},
				Message: fmt.Sprintf("message size exceeds the %d-octet limit for domain %s", max, domain),
			}
		}
	}
	return nil
}

func (s *submissionSession) Rcpt(to string, opts *gosmtp.RcptOptions) error {
	s.guard.MarkCommand()
	if s.credential == nil {
		return gosmtp.ErrAuthRequired
	}
	addr, err := mail.ParseAddress(to)
	if err != nil || addr.Address == "" {
		return &gosmtp.SMTPError{Code: 553, EnhancedCode: gosmtp.EnhancedCode{5, 1, 3}, Message: "invalid recipient address"}
	}
	// SF-09: the DELIVERBY by-time rides RCPT TO in the receiver dependency.
	// A by-time the server cannot honor (non-positive — BY=0 would expire the
	// message at acceptance — or above the 30-day cap) is refused per the
	// RFC 2852 §4.2 posture: a 501, before DATA is accepted.
	if opts != nil && opts.DeliverBy != nil {
		if _, _, derr := email.DeliverByDeadline(opts.DeliverBy.Time, time.Now()); derr != nil {
			return &gosmtp.SMTPError{Code: 501, EnhancedCode: gosmtp.EnhancedCode{5, 5, 4}, Message: derr.Error()}
		}
		if s.deliverBy == 0 || opts.DeliverBy.Time < s.deliverBy {
			s.deliverBy = opts.DeliverBy.Time
		}
	}
	s.to = append(s.to, addr.Address)
	return nil
}

// Data reads the raw message and hands it to the API send pipeline: the email
// service validates, stores, enqueues; the worker claims, signs, delivers,
// records — identical to an API send.
func (s *submissionSession) Data(r io.Reader) error {
	s.guard.MarkCommand()
	if s.credential == nil {
		return gosmtp.ErrAuthRequired
	}
	if s.from == "" || len(s.to) == 0 {
		return &gosmtp.SMTPError{Code: 503, EnhancedCode: gosmtp.EnhancedCode{5, 5, 1}, Message: "need MAIL and RCPT before DATA"}
	}

	limit := s.backend.maxMessageSize
	// +1 to detect overflow.
	lr := &io.LimitedReader{R: r, N: limit + 1}
	raw, err := io.ReadAll(lr)
	if err != nil {
		return &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "read error"}
	}
	if int64(len(raw)) > limit {
		return &gosmtp.SMTPError{Code: 552, EnhancedCode: gosmtp.EnhancedCode{5, 3, 4}, Message: "message size exceeds limit"}
	}

	parsed, perr := inbound.ParseRawEmail(raw)
	if perr != nil {
		return &gosmtp.SMTPError{Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 6, 0}, Message: "malformed message"}
	}

	attachments := make([]models.Attachment, 0, len(parsed.Attachments))
	for _, a := range parsed.Attachments {
		attachments = append(attachments, models.Attachment{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Content:     base64.StdEncoding.EncodeToString(a.Content),
		})
	}

	req := &email.SendRequest{
		From:        s.from,
		To:          s.to,
		Subject:     parsed.Subject,
		HTML:        parsed.HTMLBody,
		Text:        parsed.TextBody,
		Attachments: attachments,
	}
	// SF-09: the DELIVERBY deadline is computed at message acceptance —
	// acceptance time + the strictest by-time — and rides the SF-08 expiry
	// machinery: the deadline lands on the row's expires_at, the claim-time
	// check refuses delivery past it, and the retention sweep expires it
	// with the DSN-style event.
	if s.deliverBy > 0 {
		acceptedAt := time.Now()
		deadline, secs, derr := email.DeliverByDeadline(s.deliverBy, acceptedAt)
		if derr != nil {
			return &gosmtp.SMTPError{Code: 501, EnhancedCode: gosmtp.EnhancedCode{5, 5, 4}, Message: derr.Error()}
		}
		req.ExpiresAt = &deadline
		req.DeliverBySeconds = secs
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	workspaceID := s.credential.WorkspaceID
	if err := s.backend.sink.Submit(ctx, s.credential.UserID, 0, &workspaceID, s.ownerEmail, req); err != nil {
		return mapSubmissionError(err, s.remote)
	}
	return nil
}

// mapSubmissionError translates a pipeline error into an SMTP status for the
// submitting client (same mapping conventions as the relay listener).
func mapSubmissionError(err error, remote string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "rate_limit:"):
		return &gosmtp.SMTPError{Code: 452, EnhancedCode: gosmtp.EnhancedCode{4, 7, 0}, Message: "rate limit exceeded, try again later"}
	case strings.HasPrefix(msg, "domain_verification:"):
		return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 7, 1}, Message: "sender domain not verified for this workspace"}
	default:
		logger.Error("submission: send failed", "remote", remote, "error", err)
		return &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "temporary failure"}
	}
}

// senderDomainOf returns the lowercased domain of a bare address, or "".
func senderDomainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i+1 < len(addr) {
		return strings.ToLower(strings.TrimSpace(addr[i+1:]))
	}
	return ""
}
