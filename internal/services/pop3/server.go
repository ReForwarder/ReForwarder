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

package pop3

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

// Defaults for knobs the config does not carry (internal/config only carries
// the ADR-004 decision-5 limits). A 10-minute idle window matches typical
// POP3 client polling; the drop cap matches the mailstore's own per-request
// batch bound (mailstore.MaxFetchBatchMessages).
const (
	defaultIdleTimeout     = 10 * time.Minute
	defaultMaxDropMessages = 10000
)

// Timing bounds (the config can widen the idle window; these are hard
// ceilings so a session cannot stall the process indefinitely).
const (
	// writeTimeout bounds every response write; a wedged client cannot pin
	// the session's writer.
	writeTimeout = 60 * time.Second
	// tlsHandshakeTimeout bounds the in-band STLS upgrade.
	tlsHandshakeTimeout = 10 * time.Second
)

// Config carries the server knobs the caller (cmd/posta wiring) resolves
// from POSTA_POP3_* config (config.MailProtocolConfig). Zero fields fall
// back to the defaults above.
type Config struct {
	// Hostname is the server identity advertised in the greeting.
	Hostname string
	// MaxSessionsPerUser caps a user's concurrent POP3 sessions
	// (ADR-004 decision 5). <= 0 uses the default.
	MaxSessionsPerUser int
	// MaxConnsPerIP caps concurrent connections per client IP across both
	// listeners (ADR-004 decision 5). <= 0 uses the default.
	MaxConnsPerIP int
	// CommandRateLimit bounds commands per session per CommandRateWindow.
	// <= 0 uses the default.
	CommandRateLimit  int
	CommandRateWindow time.Duration
	// IdleTimeout reaps connections that send nothing for this long.
	IdleTimeout time.Duration
	// AllowCleartextAuth permits authentication without a security layer on
	// a cleartext listener — the loopback escape hatch (ADR-004 decision 4:
	// cleartext allowed "only on loopback for tests"). Production
	// configuration validation confines no-certificate binds to loopback;
	// this server additionally checks the PEER address at runtime, so the
	// flag is inert for non-loopback clients even if mis-deployed.
	AllowCleartextAuth bool
}

// Server is the POP3 server (OB-4 wave W3, ADR-004 decision 2). One Server
// serves both listeners — the plaintext/STLS port and the implicit-TLS port
// share the same session logic; it is safe for concurrent use.
type Server struct {
	cfg  Config
	store Store
	auth  *mailauth.Authenticator
	audit AuditFunc

	// security is the optional engine feeding the per-session guards
	// (auth-failure detection, logout signals); nil disables it.
	security *security.Engine

	// tlsConfig enables STLS (and marks the implicit-TLS listener usable);
	// nil means the plaintext listener serves no TLS at all (loopback dev).
	tlsConfig *tls.Config

	ipBudget    *ipConns
	sessionCaps *sessionCaps

	// maxDropMessages bounds the shadow view (POP3 message numbers span the
	// capped UID-ascending prefix).
	maxDropMessages int

	mu        sync.Mutex
	listeners []net.Listener
	conns     map[net.Conn]struct{}
	stopped   bool
}

// NewServer wires the server. store and auth are required; a nil auth makes
// construction fail (structurally, same stance as mailauth.NewAuthenticator —
// the mailauth seam is the only credential path, ADR-004 decision 3).
func NewServer(cfg Config, store Store, auth *mailauth.Authenticator) (*Server, error) {
	if store == nil {
		return nil, errors.New("pop3: nil store")
	}
	if auth == nil {
		return nil, errors.New("pop3: nil authenticator (app-password-only auth is the ADR-004 posture)")
	}
	if cfg.Hostname == "" {
		cfg.Hostname = "posta.local"
	}
	if cfg.MaxSessionsPerUser <= 0 {
		cfg.MaxSessionsPerUser = 10
	}
	if cfg.MaxConnsPerIP <= 0 {
		cfg.MaxConnsPerIP = 30
	}
	if cfg.CommandRateLimit <= 0 {
		cfg.CommandRateLimit = 600
	}
	if cfg.CommandRateWindow <= 0 {
		cfg.CommandRateWindow = 60 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	return &Server{
		cfg:             cfg,
		store:           store,
		auth:            auth,
		ipBudget:        newIPConns(cfg.MaxConnsPerIP),
		sessionCaps:     newSessionCaps(cfg.MaxSessionsPerUser),
		maxDropMessages: defaultMaxDropMessages,
		conns:           map[net.Conn]struct{}{},
	}, nil
}

// SetAudit installs the audit hook (nil audits nothing).
func (s *Server) SetAudit(fn AuditFunc) { s.audit = fn }

// SetSecurity wires the security engine for per-session abuse signals
// (auth-failure detection feeding the brute-force detector, logout signals).
// Nil keeps the guards as no-ops.
func (s *Server) SetSecurity(eng *security.Engine) { s.security = eng }

// SetTLSConfig installs the certificate used for STLS upgrades and the
// implicit-TLS listener. Nil keeps the plaintext listener cleartext-only
// (STLS answers -ERR; the implicit-TLS listener is then not served at all).
func (s *Server) SetTLSConfig(cfg *tls.Config) { s.tlsConfig = cfg }

// SetDropCap bounds how many messages a single session's shadow view may
// cover (the POP3 message numbers then span the capped prefix).
func (s *Server) SetDropCap(n int) {
	if n > 0 {
		s.maxDropMessages = n
	}
}

// Serve accepts connections on l until the server closes. The caller wraps l
// with security.NewCountingListener (ban enforcement + connect/abort
// signals) and, for implicit TLS, security.TLSServerListener — the same
// listener stack as the SMTP receivers.
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
			// Transient accept failure: back off like net/http and the SMTP
			// receivers do (5ms doubling to 1s).
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
// security signals on the port the client actually reached (143 vs 995).
func (s *Server) handleConn(conn net.Conn, listenerPort int) {
	ok, release := s.ipBudget.acquire(hostOf(conn.RemoteAddr().String()))
	if !ok {
		logger.Warn("pop3: IP connection cap exceeded", "ip", hostOf(conn.RemoteAddr().String()))
		writeGreetingOnly(conn, "-ERR too many connections from your address; try later")
		return
	}
	sess := newSession(s, conn, listenerPort)
	sess.ipRelease = release
	sess.serve()
}

// writeGreetingOnly sends one refusal line (the POP3 "error greeting"
// convention) before closing.
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
