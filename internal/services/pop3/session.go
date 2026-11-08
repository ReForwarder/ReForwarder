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
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/security"
)

// pop3State is the RFC 1939 state machine. UPDATE is entered by QUIT's
// expunge phase and immediately ends the session.
type pop3State int

const (
	stateAuthorization pop3State = iota
	stateTransaction
	stateClosed
)

func (s pop3State) String() string {
	switch s {
	case stateAuthorization:
		return "AUTHORIZATION"
	case stateTransaction:
		return "TRANSACTION"
	default:
		return "CLOSED"
	}
}

// shadowMessage is one entry of the session's shadow view: the login-time
// snapshot of the INBOX mapped onto POP3 message numbers. DELE only flips
// shadowDeleted — the mailstore row is untouched until QUIT's UPDATE phase.
type shadowMessage struct {
	seq           uint32 // 1-based POP3 message number (stable for the session)
	uid           uint32 // mailstore UID — the UIDL identity, stable across sessions
	messageID     uint   // mailstore row ID
	size          int64  // octets (stored plaintext size)
	shadowDeleted bool   // in-session DELE mark
}

// maxRateRejections closes a session that keeps pushing commands while rate
// limited (the -ERR loop must not spin for the full idle timeout).
const maxRateRejections = 5

// session is one POP3 connection: the wire, the state machine, the shadow
// view and the security hooks. Owned by a single goroutine (the accept loop
// hands it off); no internal locking.
type session struct {
	srv    *Server
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
	remote string // host part of RemoteAddr
	port   int

	state pop3State
	tls   bool // security layer active (implicit TLS or a completed STLS)

	// pendingUser is the USER command argument awaiting PASS.
	pendingUser string

	// identity + maildrop (valid once authenticated)
	user       *models.User
	msession   *mailstore.MailSession // W1 seam: shared per-connection protocol state
	mailbox    *models.Mailbox
	folder     *models.MailFolder
	drop       []shadowMessage
	dropBytes  int64
	deletedN   int  // shadow-deleted count (for STAT/LIST + audit)
	retrievedN int  // RETR count (audit)
	expungedN  int  // messages the UPDATE-phase sweep actually removed
	quitClean  bool // QUIT completed without a store failure

	// limits + security hooks
	guard     *security.SessionGuard
	cmdLim    *commandLimiter // per-session command budget
	release   func()          // per-user session slot release (nil-safe)
	ipRelease func()          // per-IP connection slot release (nil-safe)
	rateRej   int
}

// newSession builds the per-connection state. The implicit-TLS listener
// hands over an already-handshaked *tls.Conn, which marks the security layer
// active from the start.
func newSession(srv *Server, conn net.Conn, listenerPort int) *session {
	s := &session{
		srv:    srv,
		conn:   conn,
		reader: bufio.NewReaderSize(conn, maxCommandLine),
		writer: bufio.NewWriter(conn),
		remote: hostOf(conn.RemoteAddr().String()),
		port:   listenerPort,
		state:  stateAuthorization,
		guard:  security.NewSessionGuard(srv.security, security.ParseRemoteIP(conn.RemoteAddr().String()), listenerPort, "pop3"),
		cmdLim: newCommandLimiter(srv.cfg.CommandRateLimit, srv.cfg.CommandRateWindow),
	}
	if tc, ok := conn.(*tls.Conn); ok && tc.ConnectionState().HandshakeComplete {
		s.tls = true
	}
	return s
}

func hostOf(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
}

// serve drives one connection from greeting to close.
func (s *session) serve() {
	defer s.close()

	s.writeLine("+OK " + s.srv.cfg.Hostname + " POP3 server ready")

	for s.state != stateClosed {
		s.conn.SetReadDeadline(time.Now().Add(s.srv.cfg.IdleTimeout))
		line, err := readCommandLine(s.reader)
		if err != nil {
			return // client vanished, idle timeout, or unusable input
		}
		if !s.allowCommand() {
			continue
		}
		s.guard.MarkCommand()
		s.dispatch(line)
	}
}

// allowCommand applies the per-session command-rate limit. Over-budget
// commands are rejected with -ERR; maxRateRejections consecutive rejections
// close the connection.
func (s *session) allowCommand() bool {
	if s.cmdLim.allow() {
		s.rateRej = 0
		return true
	}
	s.rateRej++
	if s.rateRej >= maxRateRejections {
		s.writeLine("-ERR too many commands; connection closing")
		return false
	}
	s.writeLine("-ERR too many commands; slow down")
	return false
}

// close ends the session: UPDATE phase on QUIT already ran; here we release
// the per-user and per-IP slots, notify the security guard and the audit
// hook.
func (s *session) close() {
	if s.release != nil {
		s.release()
	}
	if s.ipRelease != nil {
		s.ipRelease()
	}
	s.guard.OnLogout()
	if s.user != nil {
		auditSessionEnd(s.srv.audit, s.user, s.remote, s.deletedN, s.expungedN, s.retrievedN, s.quitClean)
	}
	_ = s.conn.Close()
}

// ---- wire helpers ----

func (s *session) writeLine(line string) {
	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, _ = s.writer.WriteString(line)
	_, _ = s.writer.WriteString("\r\n")
	_ = s.writer.Flush()
}

func (s *session) writeLinef(format string, args ...any) {
	s.writeLine(fmt.Sprintf(format, args...))
}

// ---- authentication ----

// authAllowed reports whether credentials may be accepted on this connection
// right now: after a TLS upgrade / on an implicit-TLS connection, or when the
// operator explicitly allowed cleartext auth AND the peer is a loopback
// address (ADR-004 decision 4 — the runtime check makes the flag inert for
// non-loopback peers even on a mis-deployed loopback-bound listener).
func (s *session) authAllowed() bool {
	return s.tls || (s.srv.cfg.AllowCleartextAuth && isLoopbackRemote(s.remote))
}

// isLoopbackRemote reports whether the client address is a loopback address
// (127.0.0.0/8, ::1). Unparseable peers are never treated as loopback.
func isLoopbackRemote(host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

// finishLogin validates credentials and, on success, builds the maildrop:
// app-password-only verification through the W1 mailauth seam (the primary
// password is structurally never consulted), then mailbox + INBOX resolution
// and the shadow snapshot. Every failure collapses to the same generic wire
// answer; the reason is recorded in the audit sink.
func (s *session) finishLogin(email, secret string) {
	email = strings.TrimSpace(email)
	auth := s.srv.auth
	user, err := auth.AuthenticateWithAppPassword(context.Background(), email, secret)
	if err != nil {
		s.guard.RecordAuthFailure(email)
		auditAuthFailure(s.srv.audit, email, s.remote, "invalid_credentials")
		s.writeLine("-ERR [AUTH] invalid credentials")
		return
	}
	if !user.Active {
		s.guard.RecordAuthFailure(email)
		auditAuthFailure(s.srv.audit, email, s.remote, "user_inactive")
		s.writeLine("-ERR [AUTH] invalid credentials")
		return
	}
	mailbox, folder, err := resolveDrop(s.srv.store, user.ID)
	if err != nil {
		auditAuthFailure(s.srv.audit, email, s.remote, "maildrop_unavailable")
		s.writeLine("-ERR [SYS/PERM] maildrop unavailable")
		return
	}

	ok, release := s.srv.sessionCaps.acquire(user.ID)
	if !ok {
		auditAuthFailure(s.srv.audit, email, s.remote, "session_cap")
		s.writeLine("-ERR [SYS/TEMP] too many sessions for this account; try later")
		return
	}

	s.user = user
	s.release = release
	s.mailbox = mailbox
	s.folder = folder
	// W1 seam: record the shared per-connection protocol state (INBOX
	// selected read-write — POP3 sessions are never EXAMINE-style).
	s.msession = mailstore.NewMailSession(user.ID, user.Email)
	s.msession.Select(folder)
	if err := s.buildShadow(); err != nil {
		s.release()
		s.release = nil
		auditAuthFailure(s.srv.audit, email, s.remote, "maildrop_unreadable")
		s.writeLine("-ERR [SYS/PERM] maildrop unavailable")
		return
	}
	s.pendingUser = ""
	s.state = stateTransaction
	auditAuthSuccess(s.srv.audit, user, s.remote)
	s.writeLinef("+OK maildrop has %d messages (%d octets)", len(s.drop), s.dropBytes)
}

// buildShadow snapshots the INBOX into the session's shadow view: a single
// UID-ascending pass over ListMessages pages, capped at maxDropMessages
// (POP3 message numbers then cover the capped prefix — a consistent view).
func (s *session) buildShadow() error {
	const pageSize = 200
	s.drop = nil
	s.dropBytes = 0
	collected := 0
	for page := 1; ; page++ {
		lp, err := s.srv.store.ListMessages(s.folder.ID, s.user.ID, 0, mailstore.ListFilter{}, page, pageSize)
		if err != nil {
			return err
		}
		for i := range lp.Messages {
			m := &lp.Messages[i]
			s.drop = append(s.drop, shadowMessage{
				seq:       uint32(collected + 1),
				uid:       m.UID,
				messageID: m.ID,
				size:      m.Size,
			})
			s.dropBytes += m.Size
			collected++
		}
		if collected >= s.srv.maxDropMessages {
			break
		}
		if len(lp.Messages) < pageSize {
			break
		}
	}
	s.deletedN = 0
	return nil
}

// shadowBySeq resolves a 1-based POP3 message number to its shadow entry.
// Deleted messages still resolve (so DELE can answer "already deleted")
// only when includeDeleted is set; RETR/LIST/UIDL/RETR reject them.
func (s *session) shadowBySeq(arg string, includeDeleted bool) (*shadowMessage, bool) {
	seq, err := strconv.ParseUint(strings.TrimSpace(arg), 10, 32)
	if err != nil || seq == 0 {
		return nil, false
	}
	for i := range s.drop {
		if s.drop[i].seq == uint32(seq) {
			if s.drop[i].shadowDeleted && !includeDeleted {
				return nil, false
			}
			return &s.drop[i], true
		}
	}
	return nil, false
}

// visibleCount / visibleBytes compute the STAT/LIST view of the shadow.
func (s *session) visibleCount() int {
	n := 0
	for i := range s.drop {
		if !s.drop[i].shadowDeleted {
			n++
		}
	}
	return n
}

func (s *session) visibleBytes() int64 {
	var total int64
	for i := range s.drop {
		if !s.drop[i].shadowDeleted {
			total += s.drop[i].size
		}
	}
	return total
}

// ---- STLS (RFC 2595 §4) ----

// doSTLS upgrades the connection to TLS in-session. The client must not have
// pipelined anything behind the STLS line (RFC 2595 §4); after a successful
// handshake the server-side session knowledge is reset (the client must
// discard its own — RFC 2595 §4) and authentication starts over.
func (s *session) doSTLS() {
	if s.srv.tlsConfig == nil {
		s.writeLine("-ERR TLS not available")
		return
	}
	if s.state != stateAuthorization {
		s.writeLine("-ERR STLS only allowed in the AUTHORIZATION state")
		return
	}
	if s.tls {
		s.writeLine("-ERR connection already secure")
		return
	}
	if s.reader.Buffered() > 0 {
		s.writeLine("-ERR data pending before STLS; reconnect")
		return
	}
	s.writeLine("+OK begin TLS negotiation")
	_ = s.writer.Flush()

	s.conn.SetDeadline(time.Now().Add(tlsHandshakeTimeout))
	tlsConn := tls.Server(s.conn, s.srv.tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		s.writeLine("-ERR TLS negotiation failed")
		_ = tlsConn.Close()
		s.state = stateClosed
		return
	}
	// Handshake succeeded: swap the wire to the TLS layer. Buffered plaintext
	// cannot exist here (checked above); the fresh reader reads the TLS conn.
	s.conn = tlsConn
	s.reader = bufio.NewReaderSize(tlsConn, maxCommandLine)
	s.writer = bufio.NewWriter(tlsConn)
	s.tls = true
	s.pendingUser = ""
	s.conn.SetDeadline(time.Time{})
}
