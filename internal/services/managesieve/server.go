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

// Package managesieve is the RFC 5804 ManageSieve listener (SF-86, the
// ADR-004 amendment) over the SF-14 filter-script engine: the bounded
// command set (CAPABILITY / STARTTLS / AUTHENTICATE PLAIN / HAVESPACE /
// PUTSCRIPT / LISTSCRIPTS / SETACTIVE / GETSCRIPT / DELETESCRIPT /
// CHECKSCRIPT / NOOP / UNAUTHENTICATE / LOGOUT) managing the per-mailbox
// filter scripts. Script bodies are the bounded engine's serialized JSON
// documents ({"conditions": [...], "actions": [...]}) validated through the
// SAME strict evaluator the delivery engine runs — not RFC 5228 text; a body
// outside the bounded grammar is refused at PUTSCRIPT/CHECKSCRIPT. The
// server mirrors the POP3 listener's construction (the ADR-004 listener
// pattern): app-password-only auth via the mailauth seam, gated config,
// per-IP and per-user budgets, a per-session command limit, security hooks,
// audit events, and the STARTTLS / implicit-TLS posture.
package managesieve

import (
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/goposta/posta/internal/services/mailauth"
	"github.com/goposta/posta/internal/services/security"
	"github.com/jkaninda/logger"
)

// Defaults for knobs the config carries but a zero value must not disable:
// the ADR-004 non-negotiables make the budgets part of the feature.
const (
	defaultIdleTimeout = 10 * time.Minute
)

// Timing ceilings so a wedged client cannot pin the process.
const (
	writeTimeout        = 60 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
)

// maxRateRejections closes a session that keeps pushing commands while rate
// limited (the NO loop must not spin for the full idle timeout).
const maxRateRejections = 5

// Config carries the listener configuration (the ADR-004 listener pattern;
// cmd wiring resolves it from POSTA_MANAGESIEVE_* config).
type Config struct {
	// Hostname is the server identity advertised in the greeting.
	Hostname string
	// MaxSessionsPerUser caps a user's concurrent ManageSieve sessions.
	// <= 0 uses the default.
	MaxSessionsPerUser int
	// MaxConnsPerIP caps concurrent connections per client IP. <= 0 uses the
	// default.
	MaxConnsPerIP int
	// CommandRateLimit bounds commands per session per CommandRateWindow.
	CommandRateLimit  int
	CommandRateWindow time.Duration
	// IdleTimeout reaps connections that send nothing for this long.
	IdleTimeout time.Duration
	// AllowCleartextAuth permits authentication without a security layer on a
	// cleartext listener — the loopback escape hatch (ADR-004 decision 4,
	// exactly the POP3 posture). With a certificate present, credentials are
	// accepted only after STARTTLS; the server additionally checks the PEER
	// address at runtime, so the flag is inert for non-loopback clients even
	// if mis-deployed.
	AllowCleartextAuth bool
}

// ScriptEntry is one stored script as LISTSCRIPTS reports it.
type ScriptEntry struct {
	Name   string
	Body   string
	Active bool
}

// ScriptStore is the script-persistence seam over the SF-14 filter-script
// engine's storage. The FilterScriptStore adapter satisfies it. ManageSieve
// activation maps onto the filter engine's enabled flag (one active script
// per mailbox: SETACTIVE enables the named script and disables the others —
// the delivery evaluation runs enabled scripts, first match wins).
type ScriptStore interface {
	// ListScripts returns the authenticated user's scripts in evaluation
	// order, with the ACTIVE flag on the enabled one.
	ListScripts(userID uint) ([]ScriptEntry, error)
	// GetScript returns one script's stored document + its active flag.
	GetScript(userID uint, name string) (body string, active bool, err error)
	// PutScript stores (or replaces) a named script INACTIVE.
	PutScript(userID, mailboxID uint, name, body string) error
	// DeleteScript removes a named script; deleting the ACTIVE script is
	// refused by the server before this is reached.
	DeleteScript(userID uint, name string) error
	// SetActive activates one script and deactivates the others; an empty
	// name deactivates all.
	SetActive(userID uint, name string) error
	// PrimaryMailboxID returns the user's primary active mailbox (the maildrop
	// resolver's rule: the lowest-ID active mailbox).
	PrimaryMailboxID(userID uint) (uint, error)
}

// AuditFunc receives the per-session audit events (same shape as the POP3
// and IMAP servers' audit hooks). Production wiring installs the
// eventbus-backed adapter (NewEventAuditLogger); a nil hook audits nothing —
// audit failure never breaks a session.
type AuditFunc func(action string, userID uint, userEmail, clientIP string, meta map[string]any)

// Server is the ManageSieve listener (SF-86, the ADR-004 amendment). One
// Server serves both listeners — the plaintext/STARTTLS port and the
// implicit-TLS port share the same session logic; it is safe for concurrent
// use.
type Server struct {
	cfg     Config
	scripts ScriptStore
	auth    *mailauth.Authenticator
	audit   AuditFunc

	// security is the optional engine feeding the per-session guards
	// (auth-failure detection, logout signals); nil disables it.
	security *security.Engine

	// tlsConfig enables STARTTLS (and marks the implicit-TLS listener
	// usable); nil means the plaintext listener serves no TLS at all
	// (loopback dev).
	tlsConfig *tls.Config

	ipBudget    *ipConns
	sessionCaps *sessionCaps

	mu        sync.Mutex
	listeners []net.Listener
	conns     map[net.Conn]struct{}
	stopped   bool
}

// NewServer wires the server. scripts and auth are required; a nil auth
// makes construction fail (the mailauth seam is the only credential path —
// the ADR-004 decision-3 posture).
func NewServer(cfg Config, scripts ScriptStore, auth *mailauth.Authenticator) (*Server, error) {
	if scripts == nil {
		return nil, errors.New("managesieve: nil script store")
	}
	if auth == nil {
		return nil, errors.New("managesieve: nil authenticator (app-password-only auth is the ADR-004 posture)")
	}
	if cfg.Hostname == "" {
		cfg.Hostname = "posta.local"
	}
	if cfg.MaxSessionsPerUser <= 0 {
		cfg.MaxSessionsPerUser = 3
	}
	if cfg.MaxConnsPerIP <= 0 {
		cfg.MaxConnsPerIP = 8
	}
	if cfg.CommandRateLimit <= 0 {
		cfg.CommandRateLimit = 30
	}
	if cfg.CommandRateWindow <= 0 {
		cfg.CommandRateWindow = 10 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	return &Server{
		cfg:         cfg,
		scripts:     scripts,
		auth:        auth,
		ipBudget:    newIPConns(cfg.MaxConnsPerIP),
		sessionCaps: newSessionCaps(cfg.MaxSessionsPerUser),
		conns:       map[net.Conn]struct{}{},
	}, nil
}

// SetAudit installs the audit hook (nil audits nothing).
func (s *Server) SetAudit(fn AuditFunc) { s.audit = fn }

// SetSecurity wires the security engine for per-session abuse signals
// (auth-failure detection feeding the brute-force detector, logout signals).
// Nil keeps the guards as no-ops.
func (s *Server) SetSecurity(eng *security.Engine) { s.security = eng }

// SetTLSConfig installs the certificate used for STARTTLS upgrades and the
// implicit-TLS listener. Nil keeps the plaintext listener cleartext-only
// (STARTTLS answers NO; the implicit-TLS listener is then not served at all).
func (s *Server) SetTLSConfig(cfg *tls.Config) { s.tlsConfig = cfg }

// Serve accepts connections on l until the server closes. The caller wraps l
// with security.NewCountingListener (ban enforcement + connect/abort
// signals) and, for implicit TLS, security.TLSServerListener — the same
// listener stack as the POP3 receivers.
func (s *Server) Serve(l net.Listener) error {
	listenerPort := portOf(l.Addr())
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return net.ErrClosed
	}
	s.listeners = append(s.listeners, l)
	s.mu.Unlock()

	for {
		conn, err := l.Accept()
		if err != nil {
			s.mu.Lock()
			stopped := s.stopped
			s.mu.Unlock()
			if stopped || errors.Is(err, net.ErrClosed) {
				return net.ErrClosed
			}
			// Transient accept failure: back off like the POP3 listener does
			// (5ms doubling floor; the timeout case waits a full second).
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				time.Sleep(time.Second)
				continue
			}
			time.Sleep(5 * time.Millisecond)
			continue
		}
		s.trackConn(conn)
		go s.handleConn(conn, listenerPort)
	}
}

// portOf extracts the numeric port from a listener address (0 when
// unparseable — the signals then key on "unknown port", still usable).
func portOf(addr net.Addr) int {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.Port
	}
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(port)
	return p
}

// trackConn registers a live connection so Close can cut sessions.
func (s *Server) trackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		_ = conn.Close()
		return
	}
	s.conns[conn] = struct{}{}
}

// forgetConn deregisters a finished connection.
func (s *Server) forgetConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

// handleConn gates one accepted connection through the per-IP concurrent
// connection cap, then runs the session. The CountingListener (caller-side)
// has already enforced bans and recorded the connect. listenerPort keys the
// security signals on the port the client actually reached.
func (s *Server) handleConn(conn net.Conn, listenerPort int) {
	ok, release := s.ipBudget.acquire(hostOf(conn.RemoteAddr().String()))
	if !ok {
		logger.Warn("managesieve: IP connection cap exceeded", "ip", hostOf(conn.RemoteAddr().String()))
		writeGreetingOnly(conn, "NO \"Too many connections from your address; try later\"")
		return
	}
	sess := newSession(s, conn, listenerPort)
	sess.ipRelease = release
	sess.serve()
	s.forgetConn(conn)
}

// writeGreetingOnly sends one refusal line (the "error greeting" convention)
// before closing.
func writeGreetingOnly(conn net.Conn, line string) {
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, _ = conn.Write([]byte(line + "\r\n"))
	_ = conn.Close()
}

// Close shuts the server down: closes every listener and live session
// (sessions release their per-user and per-IP slots on close).
func (s *Server) Close() {
	s.mu.Lock()
	s.stopped = true
	listeners := s.listeners
	s.listeners = nil
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.conns = map[net.Conn]struct{}{}
	s.mu.Unlock()

	for _, l := range listeners {
		_ = l.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
}
