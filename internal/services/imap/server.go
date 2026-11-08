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

// Package imap is the in-house IMAP4rev1 server (OB-4 Option B Phase 4,
// ADR-004 decision 1). It speaks the ADR-004 command subset over the OB-4
// wave-W1 mailstore seam (internal/services/mailstore: FetchRange with BODY
// section extraction, the flag CAS on mod_seq, Expunge with UID lists,
// StatusCounts, SearchIMAP, the ChangeNotifier for IDLE) and authenticates
// exclusively with app passwords through mailauth.Authenticator (ADR-004
// decision 3 — the primary account password is structurally never consulted).
//
// Session policy (ADR-004 decisions 3/5/6):
//   - auth: AUTHENTICATE PLAIN (+ AUTHENTICATE LOGIN + the LOGIN command),
//     app passwords only; cleartext auth is refused before STARTTLS unless
//     the deployment opted in via config AND the peer is on loopback.
//   - limits: per-IP connection budget, per-user concurrent-session cap,
//     per-session command rate, and the security engine's ban snapshot at
//     accept (the callers wrap the listener in security.NewCountingListener)
//     plus per-session abuse signals through security.SessionGuard.
//   - audit: AUTH success/failure, SELECT, APPEND, STORE, EXPUNGE, COPY/MOVE
//     land in the events substrate through the wired AuditFunc.
//
// Protocol subset: CAPABILITY, NOOP, LOGOUT, STARTTLS, AUTHENTICATE (PLAIN,
// LOGIN), LOGIN, SELECT/EXAMINE, CREATE/DELETE/RENAME, SUBSCRIBE/UNSUBSCRIBE,
// LIST, STATUS, APPEND, CHECK, CLOSE, EXPUNGE, SEARCH (the ADR-004 key set),
// FETCH (macros + BODY[]/BODY.PEEK sections, partials), STORE (+FLAGS/−FLAGS,
// silent variants), COPY/MOVE, UID variants, IDLE. IMAP4rev2 extensions are
// NOT adopted (documented ADR-004 limitation). Custom IMAP keywords are not
// stored: only the system flags exist (same limitation as the mailstore's
// SEARCH KEYWORD).
//
// Documented limitation: STORE is CAS against the session's last-observed
// mod_seq (the W1 contract — "flag stores are CAS on the message mod_seq …
// giving OB-4 UNCHANGEDSINCE semantics"). A STORE racing another session's
// change of the same message surfaces as tagged NO instead of silently
// clobbering it; a session re-observing the message (FETCH/refresh) re-arms
// its STORE.
package imap

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goposta/posta/internal/services/mailauth"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/security"
	"github.com/jkaninda/logger"
)

// protocol constants.
const (
	// ServerID names the implementation in greeting/LOGOUT lines.
	ServerID = "Posta IMAP4rev1"
	// defaultLimits for unset Config fields.
	defaultMaxSessionsPerUser = 10
	defaultMaxConnsPerIP      = 30
	defaultCmdWindow          = 60 * time.Second
	defaultIdleTimeout        = 30 * time.Minute
	defaultMaxAppendBytes     = int64(64 * 1024 * 1024)
)

// AuditFunc receives one audit event per audited action (ADR-004 decision 6).
// Nil audits nothing. Implementations publish into the events substrate.
type AuditFunc func(action string, userID uint, userEmail, clientIP string, meta map[string]any)

// Config carries the server knobs the caller (cmd/posta wiring) resolves
// from POSTA_IMAP_* config (internal/config MailProtocolConfig). Zero fields
// fall back to the defaults above.
type Config struct {
	// Hostname is the server identity advertised in the greeting.
	Hostname string
	// MaxSessionsPerUser caps a user's concurrent IMAP sessions
	// (ADR-004 decision 5). <= 0 uses the default.
	MaxSessionsPerUser int
	// MaxConnsPerIP caps concurrent connections per client IP across both
	// listeners (ADR-004 decision 5). <= 0 uses the default.
	MaxConnsPerIP int
	// CommandRateLimit / CommandRateWindowSecs bound commands per session
	// per sliding window (ADR-004 decision 5). Limit <= 0 disables.
	CommandRateLimit      int
	CommandRateWindowSecs int
	// IdleTimeout reaps connections that send nothing for this long.
	IdleTimeout time.Duration
	// MaxMessageSize caps APPEND literal octet counts.
	MaxMessageSize int64
	// AllowCleartextAuth permits authentication before STARTTLS — and even
	// then only from loopback peers (ADR-004 decision 4: cleartext allowed
	// "only on loopback for tests"; validateMailProtocols ties cleartext
	// listeners to loopback binds).
	AllowCleartextAuth bool
}

// Server is the IMAP4rev1 server. One Server serves both the cleartext
// (STARTTLS) and the implicit-TLS listener; it is safe for concurrent use.
type Server struct {
	cfg    Config
	store  *mailstore.Service
	auth   *mailauth.Authenticator
	tlsCfg atomic.Pointer[tls.Config] // optional: STARTTLS + implicit TLS

	audit   AuditFunc
	secEng  *security.Engine
	limiter *connTracker

	mu       sync.Mutex
	listener net.Listener
	conns    map[*session]struct{}
	perUser  map[uint]int

	closed atomic.Bool
}

// NewServer wires the server. store and auth are required; a nil auth makes
// every login fail (structurally, same stance as mailauth.NewAuthenticator).
func NewServer(cfg Config, store *mailstore.Service, auth *mailauth.Authenticator) *Server {
	if cfg.MaxSessionsPerUser <= 0 {
		cfg.MaxSessionsPerUser = defaultMaxSessionsPerUser
	}
	if cfg.MaxConnsPerIP <= 0 {
		cfg.MaxConnsPerIP = defaultMaxConnsPerIP
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	if cfg.MaxMessageSize <= 0 {
		cfg.MaxMessageSize = defaultMaxAppendBytes
	}
	return &Server{
		cfg:     cfg,
		store:   store,
		auth:    auth,
		limiter: newConnTracker(cfg.MaxConnsPerIP),
		conns:   make(map[*session]struct{}),
		perUser: make(map[uint]int),
	}
}

// SetTLSConfig installs the TLS configuration used for STARTTLS upgrades and
// implicit-TLS listeners. Optional: without it STARTTLS is refused.
func (s *Server) SetTLSConfig(c *tls.Config) { s.tlsCfg.Store(c) }

// SetAudit wires the audit sink (nil disables).
func (s *Server) SetAudit(fn AuditFunc) { s.audit = fn }

// SetSecurity wires the security engine for per-session abuse signals
// (auth-failure and malformed-command detection). Nil disables.
func (s *Server) SetSecurity(eng *security.Engine) { s.secEng = eng }

// Serve accepts connections on ln until Shutdown/Close. The caller chooses
// the listener wrapping: cmd/posta wraps with security.NewCountingListener
// (ban enforcement at accept + connect signals) and, for implicit TLS,
// wraps with tls.NewListener — exactly the inbound SMTP listener pattern.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			if s.closed.Load() {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		if s.closed.Load() {
			_ = c.Close()
			return nil
		}
		s.accept(c)
	}
}

// Close stops the accept loop and terminates every live session. The registry
// lock must NOT be held while closing: session.close() unregisters itself
// (unregisterSession takes the same mutex).
func (s *Server) Close() {
	s.closed.Store(true)
	s.mu.Lock()
	ln := s.listener
	conns := make([]*session, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.conns = make(map[*session]struct{})
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		c.close()
	}
}

func (s *Server) accept(c net.Conn) {
	remote := c.RemoteAddr().String()
	ip := remoteIP(remote)
	key := "unknown"
	if ip.IsValid() {
		key = ip.String()
	}
	if !s.limiter.acquire(key) {
		logger.Warn("imap: per-IP connection cap exceeded", "ip", key)
		_, _ = fmt.Fprintf(c, "* BYE too many connections\r\n")
		_ = c.Close()
		return
	}
	sess := newSession(s, c, remote)
	s.mu.Lock()
	s.conns[sess] = struct{}{}
	s.mu.Unlock()
	go func() {
		defer s.limiter.release(key)
		defer func() {
			s.mu.Lock()
			delete(s.conns, sess)
			s.mu.Unlock()
		}()
		sess.run()
	}()
}

// registerSession enforces the per-user concurrent-session cap. It returns
// false when the user is already at the cap (ADR-004 decision 5).
func (s *Server) registerSession(userID uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perUser[userID] >= s.cfg.MaxSessionsPerUser {
		return false
	}
	s.perUser[userID]++
	return true
}

func (s *Server) unregisterSession(userID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.perUser[userID]; n > 1 {
		s.perUser[userID] = n - 1
	} else {
		delete(s.perUser, userID)
	}
}

func (s *Server) auditf(action string, userID uint, email, ip string, meta map[string]any) {
	if s == nil || s.audit == nil {
		return
	}
	s.audit(action, userID, email, ip, meta)
}

// remoteIP extracts the peer IP from an addr string.
func remoteIP(addr string) netip.Addr {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		h = addr
	}
	a, err := netip.ParseAddr(h)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// connTracker enforces the concurrent per-IP connection cap (ADR-004
// decision 5; the POSTA_IMAP_MAX_CONNS_PER_IP semantics — "concurrent
// connections per client IP"). Local to this package so the IMAP server does
// not import the SMTP receiver.
type connTracker struct {
	mu  sync.Mutex
	ips map[string]int
	max int
}

func newConnTracker(max int) *connTracker {
	return &connTracker{ips: make(map[string]int), max: max}
}

// acquire registers one connection for the IP; false when at the cap.
func (t *connTracker) acquire(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ips[ip] >= t.max {
		return false
	}
	t.ips[ip]++
	return true
}

func (t *connTracker) release(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := t.ips[ip]; n > 1 {
		t.ips[ip] = n - 1
	} else {
		delete(t.ips, ip)
	}
}

// cmdLimiter is the per-session command-rate sliding window
// (POSTA_IMAP_COMMAND_RATE_LIMIT / _WINDOW semantics).
type cmdLimiter struct {
	mu     sync.Mutex
	stamps []time.Time
	max    int
	window time.Duration
}

func newCmdLimiter(max int, windowSecs int) *cmdLimiter {
	if windowSecs <= 0 {
		windowSecs = 60
	}
	return &cmdLimiter{max: max, window: time.Duration(windowSecs) * time.Second}
}

func (l *cmdLimiter) allow() bool {
	if l.max <= 0 {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	keep := l.stamps[:0]
	for _, t := range l.stamps {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.max {
		l.stamps = keep
		return false
	}
	l.stamps = append(keep, now)
	return true
}

// sessionsOfUser reports a user's live session count (test/ops helper).
func (s *Server) sessionsOfUser(userID uint) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.perUser[userID]
}
