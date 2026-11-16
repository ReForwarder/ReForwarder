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

package managesieve

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/security"
)

// sieveState is the RFC 5804 session state machine: non-authenticated →
// authenticated (STARTTLS and AUTHENTICATE are the transitions); CLOSED ends
// the session.
type sieveState int

const (
	stateNonAuth sieveState = iota
	stateAuthed
	stateClosed
)

// session is one ManageSieve connection: the wire, the state machine and the
// security hooks. Owned by a single goroutine (the accept loop hands it off);
// no internal locking.
type session struct {
	srv    *Server
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
	remote string // host part of RemoteAddr
	port   int

	tls   bool // security layer active (implicit TLS or a completed STARTTLS)
	state sieveState

	// identity + script scope (valid once authenticated)
	user      *models.User
	mailboxID uint

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
		state:  stateNonAuth,
		guard:  security.NewSessionGuard(srv.security, security.ParseRemoteIP(conn.RemoteAddr().String()), listenerPort, "managesieve"),
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

	s.writeLine(`OK "Posta ManageSieve ready"`)

	for s.state != stateClosed {
		s.conn.SetReadDeadline(s.readDeadline())
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
// commands are rejected with NO; maxRateRejections consecutive rejections
// close the connection.
func (s *session) allowCommand() bool {
	if s.cmdLim.allow() {
		s.rateRej = 0
		return true
	}
	s.rateRej++
	if s.rateRej >= maxRateRejections {
		s.writeLine(`NO "too many commands; connection closing"`)
		s.state = stateClosed
		return false
	}
	s.writeLine(`NO "too many commands; slow down"`)
	return false
}

// close ends the session: release the per-user and per-IP slots, notify the
// security guard, then cut the wire.
func (s *session) close() {
	if s.release != nil {
		s.release()
		s.release = nil
	}
	if s.ipRelease != nil {
		s.ipRelease()
		s.ipRelease = nil
	}
	s.guard.OnLogout()
	_ = s.conn.Close()
}

// ---- wire helpers ----

func (s *session) writeLine(line string) {
	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, _ = s.writer.WriteString(line)
	_, _ = s.writer.WriteString("\r\n")
	_ = s.writer.Flush()
}

// writeScriptLiteral emits a script body as an RFC 5804 server literal:
// `{n}` CRLF followed by n octets (GETSCRIPT's answer shape).
func (s *session) writeScriptLiteral(body string) {
	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, _ = fmt.Fprintf(s.writer, "{%d}\r\n", len(body))
	_, _ = s.writer.WriteString(body)
	_, _ = s.writer.WriteString("\r\n")
	_ = s.writer.Flush()
}

func (s *session) ok(reason string) { s.writeLine("OK " + quoteSieveString(reason)) }
func (s *session) no(reason string) { s.writeLine("NO " + quoteSieveString(reason)) }

// ---- dispatch ----

// dispatch routes one client line. Command verbs are case-insensitive
// (RFC 5804 §1.1); the bounded command set answers NO on anything else.
func (s *session) dispatch(line []byte) {
	verb, arg := splitLine(line)
	switch verb {
	case "CAPABILITY":
		s.cmdCapability()
	case "STARTTLS":
		s.cmdStartTLS()
	case "AUTHENTICATE":
		s.cmdAuthenticate(arg)
	case "HAVESPACE":
		s.cmdHaveSpace(arg)
	case "PUTSCRIPT":
		s.cmdPutScript(arg)
	case "LISTSCRIPTS":
		s.cmdListScripts()
	case "SETACTIVE":
		s.cmdSetActive(arg)
	case "GETSCRIPT":
		s.cmdGetScript(arg)
	case "DELETESCRIPT":
		s.cmdDeleteScript(arg)
	case "CHECKSCRIPT":
		s.cmdCheckScript(arg)
	case "NOOP":
		s.ok("NOOP completed")
	case "UNAUTHENTICATE":
		s.cmdUnauthenticate()
	case "LOGOUT":
		s.ok("Logout completed")
		s.state = stateClosed
	default:
		s.no("Unknown command")
	}
}

// requireAuth guards the script-management commands.
func (s *session) requireAuth() bool {
	if s.state != stateAuthed || s.user == nil {
		s.no("Authenticate first")
		return false
	}
	return true
}

// cmdCapability answers the RFC 5804 capability listing: quoted name/value
// lines terminated by OK. SASL and STARTTLS appear only while the session is
// non-authenticated; STARTTLS only when the listener has a certificate.
func (s *session) cmdCapability() {
	s.writeLine(`"IMPLEMENTATION" "Posta ManageSieve"`)
	s.writeLine(`"SIEVE" "fileinto"`)
	s.writeLine(`"VERSION" "1.0"`)
	if s.state == stateNonAuth {
		s.writeLine(`"SASL" "PLAIN"`)
		if !s.tls && s.srv.tlsConfig != nil {
			s.writeLine(`"STARTTLS"`)
		}
	}
	s.ok("Capability completed")
}

// cmdStartTLS upgrades the connection in-session (RFC 5804 §2.1). The client
// must not have pipelined anything behind the STARTTLS line; after a
// successful handshake the session's TLS layer is marked active (the
// POP3 STLS posture — the buffered-input check is what makes a pipelined
// follow-up command unable to sneak past the upgrade).
func (s *session) cmdStartTLS() {
	if s.srv.tlsConfig == nil {
		s.no("TLS not available")
		return
	}
	if s.state != stateNonAuth {
		s.no("STARTTLS only allowed before authentication")
		return
	}
	if s.tls {
		s.no("Connection is already secure")
		return
	}
	if s.reader.Buffered() > 0 {
		s.no("Data pending before STARTTLS; reconnect")
		return
	}
	s.ok("Begin TLS negotiation now")
	s.conn.SetDeadline(time.Now().Add(tlsHandshakeTimeout))
	tlsConn := tls.Server(s.conn, s.srv.tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		s.no("TLS negotiation failed")
		_ = tlsConn.Close()
		s.state = stateClosed
		return
	}
	s.conn = tlsConn
	s.reader = bufio.NewReaderSize(tlsConn, maxCommandLine)
	s.writer = bufio.NewWriter(tlsConn)
	s.tls = true
	s.conn.SetDeadline(time.Time{})
}

// authAllowed reports whether credentials may be accepted on this connection
// right now: after a TLS upgrade / on an implicit-TLS connection, or when the
// operator explicitly allowed cleartext auth AND the peer is a loopback
// address (the ADR-004 decision-4 posture — identical to the POP3 listener).
func (s *session) authAllowed() bool {
	return s.tls || (s.srv.cfg.AllowCleartextAuth && isLoopbackRemote(s.remote))
}

// isLoopbackRemote reports whether the client address is a loopback address
// (127.0.0.0/8, ::1). Unparseable peers are never treated as loopback.
func isLoopbackRemote(host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

// cmdAuthenticate runs AUTHENTICATE "PLAIN" (RFC 5804 §2.2): the only SASL
// mechanism the listener advertises, carrying base64(authzid NUL authcid NUL
// password). The authcid is the account email; the secret is an app password
// exclusively — the primary account password is structurally never consulted
// (the mailauth seam's posture). Every failure collapses to the same generic
// NO; the reason is recorded in the audit sink.
func (s *session) cmdAuthenticate(arg string) {
	if s.state == stateAuthed {
		s.no("Already authenticated")
		return
	}
	if !s.authAllowed() {
		s.guard.RecordAuthFailure("")
		auditAuthFailure(s.srv.audit, "", s.remote, "tls_required")
		s.no("TLS required for authentication")
		return
	}
	// The mechanism name must be PLAIN (the only advertised one); RFC 5804
	// sends it quoted — `AUTHENTICATE "PLAIN"`.
	fields := splitSieveWords(arg)
	if len(fields) == 0 || !strings.EqualFold(strings.Trim(fields[0], `"`), "PLAIN") {
		s.no("Unsupported authentication mechanism")
		return
	}
	var decoded []byte
	if len(fields) >= 2 {
		// Initial response on the command line (quoted or bare base64).
		raw, err := base64.StdEncoding.DecodeString(strings.Trim(fields[1], `"`))
		if err != nil {
			s.no("Invalid base64 response")
			return
		}
		decoded = raw
	} else {
		// Continuation: one base64 line, or "*" to cancel (RFC 5804 §2.2).
		s.writeLine(`+ ""`)
		s.conn.SetReadDeadline(s.readDeadline())
		line, err := readCommandLine(s.reader)
		if err != nil {
			return
		}
		resp := strings.TrimSpace(string(line))
		if resp == "*" {
			s.no("Authentication cancelled")
			return
		}
		raw, derr := base64.StdEncoding.DecodeString(resp)
		if derr != nil {
			s.no("Invalid base64 response")
			return
		}
		decoded = raw
	}
	parts := strings.Split(string(decoded), "\x00")
	if len(parts) != 3 {
		s.no("Invalid PLAIN response")
		return
	}
	authzid, authcid, secret := parts[0], parts[1], parts[2]
	authcid = strings.TrimSpace(authcid)
	if authzid != "" && authzid != authcid {
		// PLAIN with a distinct authorization identity is refused: a
		// ManageSieve session manages only the authenticating account's own
		// scripts — no proxying.
		s.guard.RecordAuthFailure(authcid)
		auditAuthFailure(s.srv.audit, authcid, s.remote, "authzid_refused")
		s.no("Invalid credentials")
		return
	}
	user, err := s.srv.auth.AuthenticateWithAppPassword(context.Background(), authcid, secret)
	if err != nil || user == nil {
		s.guard.RecordAuthFailure(authcid)
		auditAuthFailure(s.srv.audit, authcid, s.remote, "invalid_credentials")
		s.no("Invalid credentials")
		return
	}
	if !user.Active {
		s.guard.RecordAuthFailure(authcid)
		auditAuthFailure(s.srv.audit, authcid, s.remote, "user_inactive")
		s.no("Invalid credentials")
		return
	}
	mailboxID, err := s.srv.scripts.PrimaryMailboxID(user.ID)
	if err != nil || mailboxID == 0 {
		auditAuthFailure(s.srv.audit, authcid, s.remote, "mailbox_unavailable")
		s.no("No mailbox to manage")
		return
	}
	ok, release := s.srv.sessionCaps.acquire(user.ID)
	if !ok {
		auditAuthFailure(s.srv.audit, authcid, s.remote, "session_cap")
		s.no("Too many sessions for this account; try later")
		return
	}
	s.user = user
	s.mailboxID = mailboxID
	s.release = release
	s.state = stateAuthed
	auditAuthSuccess(s.srv.audit, user, s.remote, mailboxID)
	s.ok("Authenticated")
}

// splitSieveWords splits an argument string into whitespace-separated words
// (for the AUTHENTICATE mechanism line only — never for script names, whose
// parsing goes through parseArgs).
func splitSieveWords(arg string) []string {
	fields := strings.Fields(arg)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f)
	}
	return out
}

// cmdUnauthenticate returns the session to the non-authenticated state
// (RFC 5804 §2.13). The TLS layer stays up.
func (s *session) cmdUnauthenticate() {
	if s.state != stateAuthed {
		s.no("Not authenticated")
		return
	}
	if s.release != nil {
		s.release()
		s.release = nil
	}
	s.user = nil
	s.mailboxID = 0
	s.state = stateNonAuth
	s.ok("Unauthentication completed")
}

// cmdHaveSpace answers the storage-capacity probe (RFC 5804 §2.3). The
// engine's per-script bound IS the quota — HAVESPACE reports it as the
// available-octet figure (the store imposes no aggregate script quota).
func (s *session) cmdHaveSpace(arg string) {
	if !s.requireAuth() {
		return
	}
	args, err := s.parseArgs(arg)
	if err != nil {
		s.no("Malformed arguments")
		return
	}
	if _, ok := argString(args, 0); !ok {
		s.no("HAVESPACE requires a script name and a size")
		return
	}
	if _, ok := argNumber(args, 1); !ok {
		s.no("HAVESPACE requires a script name and a size")
		return
	}
	s.ok(strconv.FormatInt(maxScriptBytes, 10))
}

// cmdPutScript stores (or replaces) a named script INACTIVE
// (RFC 5804 §2.5). The document is validated through the SAME strict
// evaluator the delivery engine runs BEFORE any storage write — a body
// outside the bounded grammar never reaches the table (the ADR-004
// amendment's refusal posture). PUTSCRIPT never disturbs the active flag:
// re-putting the ACTIVE script's name updates its body and keeps it active
// (RFC 5804: PUTSCRIPT does not change activation).
func (s *session) cmdPutScript(arg string) {
	if !s.requireAuth() {
		return
	}
	args, err := s.parseArgs(arg)
	if err != nil {
		s.no("Malformed arguments: " + err.Error())
		return
	}
	name, ok := argString(args, 0)
	if !ok || strings.TrimSpace(name) == "" {
		s.no("PUTSCRIPT requires a script name and a script document")
		return
	}
	if len(name) > 255 {
		s.no("Script name must be at most 255 characters")
		return
	}
	body, ok := argString(args, 1)
	if !ok {
		s.no("PUTSCRIPT requires a script name and a script document")
		return
	}
	if _, _, verr := parseScriptDocument(body); verr != nil {
		s.no("Invalid script document: " + verr.Error())
		return
	}
	if err := s.srv.scripts.PutScript(s.user.ID, s.mailboxID, name, body); err != nil {
		s.no("Failed to store the script")
		return
	}
	s.ok("Script stored")
}

// cmdCheckScript validates a script document without storing it
// (RFC 5804 §2.7) — the same strict evaluator PUTSCRIPT runs.
func (s *session) cmdCheckScript(arg string) {
	if !s.requireAuth() {
		return
	}
	args, err := s.parseArgs(arg)
	if err != nil {
		s.no("Malformed arguments: " + err.Error())
		return
	}
	body, ok := argString(args, 0)
	if !ok {
		s.no("CHECKSCRIPT requires a script document")
		return
	}
	if _, _, verr := parseScriptDocument(body); verr != nil {
		s.no("Invalid script document: " + verr.Error())
		return
	}
	s.ok("Script is valid")
}

// cmdListScripts lists the user's scripts (RFC 5804 §2.4): one quoted name
// per line, the active one prefixed with ACTIVE, terminated by OK.
func (s *session) cmdListScripts() {
	if !s.requireAuth() {
		return
	}
	entries, err := s.srv.scripts.ListScripts(s.user.ID)
	if err != nil {
		s.no("Failed to list the scripts")
		return
	}
	for _, e := range entries {
		if e.Active {
			s.writeLine("ACTIVE " + quoteSieveString(e.Name))
		} else {
			s.writeLine(quoteSieveString(e.Name))
		}
	}
	s.ok("Listscripts completed")
}

// cmdSetActive activates one script and deactivates the others
// (RFC 5804 §2.6): exactly one active script per mailbox — the activation
// maps onto the filter engine's enabled flag. `SETACTIVE ""` deactivates all.
func (s *session) cmdSetActive(arg string) {
	if !s.requireAuth() {
		return
	}
	args, err := s.parseArgs(arg)
	if err != nil {
		s.no("Malformed arguments")
		return
	}
	name, ok := argString(args, 0)
	if !ok {
		s.no("SETACTIVE requires a script name")
		return
	}
	if err := s.srv.scripts.SetActive(s.user.ID, name); err != nil {
		s.no("Script does not exist")
		return
	}
	s.ok("Script activated")
}

// cmdGetScript returns one stored script as a server literal
// (RFC 5804 §2.9).
func (s *session) cmdGetScript(arg string) {
	if !s.requireAuth() {
		return
	}
	args, err := s.parseArgs(arg)
	if err != nil {
		s.no("Malformed arguments")
		return
	}
	name, ok := argString(args, 0)
	if !ok || strings.TrimSpace(name) == "" {
		s.no("GETSCRIPT requires a script name")
		return
	}
	body, _, err := s.srv.scripts.GetScript(s.user.ID, name)
	if err != nil {
		s.no("Script does not exist")
		return
	}
	s.writeScriptLiteral(body)
	s.ok("Getscript completed")
}

// cmdDeleteScript removes one named script (RFC 5804 §2.10). Deleting the
// ACTIVE script is refused (RFC 5804: the active script cannot be deleted
// until another script is activated) — the same activation guard the
// ADR-004 amendment records.
func (s *session) cmdDeleteScript(arg string) {
	if !s.requireAuth() {
		return
	}
	args, err := s.parseArgs(arg)
	if err != nil {
		s.no("Malformed arguments")
		return
	}
	name, ok := argString(args, 0)
	if !ok || strings.TrimSpace(name) == "" {
		s.no("DELETESCRIPT requires a script name")
		return
	}
	_, active, err := s.srv.scripts.GetScript(s.user.ID, name)
	if err != nil {
		s.no("Script does not exist")
		return
	}
	if active {
		s.no("The active script cannot be deleted")
		return
	}
	if err := s.srv.scripts.DeleteScript(s.user.ID, name); err != nil {
		s.no("Failed to delete the script")
		return
	}
	s.ok("Script deleted")
}
