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
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// dispatch routes one client line to its command handler. Command verbs are
// case-insensitive (RFC 1939 §4); unknown commands answer -ERR.
func (s *session) dispatch(line []byte) {
	cmd, arg := splitLine(line)
	switch cmd {
	case "CAPA":
		s.cmdCapa()
	case "USER":
		s.cmdUser(arg)
	case "PASS":
		s.cmdPass(arg)
	case "AUTH":
		s.cmdAuth(arg)
	case "STLS":
		s.cmdStls()
	case "QUIT":
		s.cmdQuit()
	case "STAT":
		s.cmdStat()
	case "LIST":
		s.cmdList(arg)
	case "UIDL":
		s.cmdUidl(arg)
	case "RETR":
		s.cmdRetr(arg)
	case "TOP":
		s.cmdTop(arg)
	case "DELE":
		s.cmdDele(arg)
	case "RSET":
		s.cmdRset()
	case "NOOP":
		s.cmdNoop()
	default:
		s.writeLine("-ERR unknown command")
	}
}

// requireAuthState reports whether the session is authenticated. Transaction
// commands in the AUTHORIZATION state answer -ERR (RFC 1939 clients are
// expected to follow the state machine; the message names the state).
func (s *session) requireAuthState() bool {
	if s.state != stateTransaction {
		s.writeLine("-ERR command invalid in the " + s.state.String() + " state")
		return false
	}
	return true
}

// ---- CAPA (RFC 2449) ----

// cmdCapa advertises the capabilities that hold RIGHT NOW: STLS only before
// the security layer is up (RFC 2595 §4 forbids advertising it afterwards),
// USER/AUTH only when credentials are acceptable on this connection — a
// TLS-enforced server never leaks "PLAIN works in cleartext".
func (s *session) cmdCapa() {
	var b strings.Builder
	b.WriteString("+OK capability list follows\r\n")
	b.WriteString("TOP\r\n")
	b.WriteString("UIDL\r\n")
	b.WriteString("PIPELINING\r\n")
	if s.authAllowed() {
		b.WriteString("USER\r\n")
		b.WriteString("AUTH PLAIN\r\n")
	}
	if !s.tls && s.srv.tlsConfig != nil {
		b.WriteString("STLS\r\n")
	}
	b.WriteString("IMPLEMENTATION Posta-POP3\r\n")
	b.WriteString(".")
	s.writeLine(b.String())
}

// ---- USER / PASS (RFC 1939) ----

func (s *session) cmdUser(arg string) {
	if !s.authAllowed() {
		s.writeLine("-ERR [AUTH] TLS required (STLS first)")
		return
	}
	if s.state != stateAuthorization {
		s.writeLine("-ERR already authenticated")
		return
	}
	s.pendingUser = arg
	s.writeLine("+OK send PASS")
}

func (s *session) cmdPass(arg string) {
	if !s.authAllowed() {
		s.writeLine("-ERR [AUTH] TLS required (STLS first)")
		return
	}
	if s.pendingUser == "" {
		s.writeLine("-ERR USER required first")
		return
	}
	s.finishLogin(s.pendingUser, arg)
}

// ---- AUTH PLAIN (RFC 5034) ----

// cmdAuth implements SASL PLAIN only. With an initial response the exchange
// is one round-trip; without one the server sends the "+" challenge and reads
// the client's base64 (or "*" to cancel).
func (s *session) cmdAuth(arg string) {
	if !s.authAllowed() {
		s.writeLine("-ERR [AUTH] TLS required (STLS first)")
		return
	}
	if s.state != stateAuthorization {
		s.writeLine("-ERR already authenticated")
		return
	}
	fields := splitArgs(arg)
	if len(fields) == 0 {
		s.writeLine("-ERR AUTH mechanism required")
		return
	}
	if !strings.EqualFold(fields[0], "PLAIN") {
		s.writeLine("-ERR authentication mechanism not supported")
		return
	}
	var payload string
	switch len(fields) {
	case 1:
		s.writeLine("+ ")
		s.conn.SetReadDeadline(time.Now().Add(s.srv.cfg.IdleTimeout))
		line, err := readCommandLine(s.reader)
		if err != nil {
			s.state = stateClosed
			return
		}
		resp := strings.TrimSpace(string(line))
		if resp == "*" {
			s.writeLine("-ERR authentication cancelled")
			return
		}
		payload = resp
	case 2:
		payload = fields[1]
	default:
		s.writeLine("-ERR invalid AUTH arguments")
		return
	}
	if payload == "" {
		s.writeLine("-ERR [AUTH] missing SASL response")
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		s.writeLine("-ERR [AUTH] invalid SASL response encoding")
		return
	}
	// RFC 4616 message: authzid NUL authcid NUL passwd. The authcid is the
	// identity we authenticate (authzid authorization impersonation is out of
	// scope for POP3 — it is refused unless empty or equal to the authcid).
	parts := strings.Split(string(decoded), "\x00")
	if len(parts) != 3 {
		s.writeLine("-ERR [AUTH] invalid SASL PLAIN message")
		return
	}
	authzid, authcid, secret := parts[0], parts[1], parts[2]
	if authzid != "" && authzid != authcid {
		s.guard.RecordAuthFailure(authcid)
		auditAuthFailure(s.srv.audit, authcid, s.remote, "authzid_mismatch")
		s.writeLine("-ERR [AUTH] invalid credentials")
		return
	}
	s.finishLogin(authcid, secret)
}

// ---- STLS (RFC 2595) ----

func (s *session) cmdStls() {
	s.doSTLS()
}

// ---- QUIT (RFC 1939 §6 UPDATE phase) ----

// cmdQuit ends the session. In the TRANSACTION state it runs the UPDATE
// phase: every shadow-deleted message is flagged \Deleted through the
// mailstore and the expunge sweep removes them transactionally. Marks live
// only in the shadow, so a dropped connection (no QUIT) never expunges
// anything. RFC 1939 defines no error response for QUIT — failures are
// answered +OK (the connection closes either way) and recorded server-side.
func (s *session) cmdQuit() {
	switch s.state {
	case stateTransaction:
		s.updatePhase()
		s.writeLine("+OK Posta POP3 signing off")
	case stateAuthorization:
		s.writeLine("+OK Posta POP3 signing off")
	}
	s.state = stateClosed
}

// updatePhase maps the shadow's deletions onto the mailstore: \Deleted flags
// set through the CAS flag store (tolerating messages another session already
// expunged — ErrMessageNotFound — since the expunge sweep is the authority on
// what was actually removed), then one expunge sweep. The sweep removes every
// \Deleted-flagged row of the folder — ADR-004 decision 2 ("expunge maps to
// mailstore flag sweeps"); an IMAP client that set \Deleted itself consented
// to removal, and the mailstore keeps survivors' UIDs and uid_validity
// intact.
func (s *session) updatePhase() {
	s.quitClean = true
	if s.deletedN == 0 {
		return
	}
	for i := range s.drop {
		if !s.drop[i].shadowDeleted {
			continue
		}
		// A message already expunged by another session fails with
		// ErrMessageNotFound — that outcome is indistinguishable for this
		// session's tally and must not abort the sweep.
		_ = s.srv.store.MarkDeleted(s.user.ID, 0, s.drop[i].messageID)
	}
	res, err := s.srv.store.Expunge(s.folder.ID, s.user.ID, 0)
	if err != nil {
		// The shadow marks stay applied (the mailstore may or may not have
		// taken them); the tally reports what the sweep actually removed.
		s.quitClean = false
		return
	}
	if res != nil {
		s.expungedN = int(res.Expunged)
	}
}

// ---- TRANSACTION state ----

// cmdStat: +OK <visible count> <visible octets>.
func (s *session) cmdStat() {
	if !s.requireAuthState() {
		return
	}
	s.writeLinef("+OK %d %d", s.visibleCount(), s.visibleBytes())
}

// cmdList serves LIST [msg]: with a message number the single-entry form,
// otherwise the multiline enumeration of visible messages.
func (s *session) cmdList(arg string) {
	if !s.requireAuthState() {
		return
	}
	if arg == "" {
		var b strings.Builder
		b.WriteString("+OK ")
		b.WriteString(strconv.Itoa(s.visibleCount()))
		b.WriteString(" messages (")
		b.WriteString(strconv.FormatInt(s.visibleBytes(), 10))
		b.WriteString(" octets)\r\n")
		for i := range s.drop {
			if s.drop[i].shadowDeleted {
				continue
			}
			b.WriteString(strconv.FormatUint(uint64(s.drop[i].seq), 10))
			b.WriteByte(' ')
			b.WriteString(strconv.FormatInt(s.drop[i].size, 10))
			b.WriteString("\r\n")
		}
		b.WriteString(".")
		s.writeLine(b.String())
		return
	}
	m, ok := s.shadowBySeq(arg, false)
	if !ok {
		s.writeLine("-ERR no such message")
		return
	}
	s.writeLinef("+OK %d %d", m.seq, m.size)
}

// cmdUidl serves UIDL [msg]: the unique-id is the mailstore's per-folder
// monotonic UID in decimal — stable across sessions (the folder's
// uid_validity never changes on expunge, per the mailstore contract).
func (s *session) cmdUidl(arg string) {
	if !s.requireAuthState() {
		return
	}
	if arg == "" {
		var b strings.Builder
		b.WriteString("+OK ")
		b.WriteString(strconv.Itoa(s.visibleCount()))
		b.WriteString(" messages\r\n")
		for i := range s.drop {
			if s.drop[i].shadowDeleted {
				continue
			}
			b.WriteString(strconv.FormatUint(uint64(s.drop[i].seq), 10))
			b.WriteByte(' ')
			b.WriteString(strconv.FormatUint(uint64(s.drop[i].uid), 10))
			b.WriteString("\r\n")
		}
		b.WriteString(".")
		s.writeLine(b.String())
		return
	}
	m, ok := s.shadowBySeq(arg, false)
	if !ok {
		s.writeLine("-ERR no such message")
		return
	}
	s.writeLinef("+OK %d %d", m.seq, m.uid)
}

// cmdRetr serves RETR msg: the stored raw message, byte-stuffed, terminated
// by the CRLF.CRLF marker. No side effects on the store (POP3 never mutates
// flags mid-session — see package doc).
func (s *session) cmdRetr(arg string) {
	if !s.requireAuthState() {
		return
	}
	m, ok := s.shadowBySeq(arg, false)
	if !ok {
		s.writeLine("-ERR no such message")
		return
	}
	raw, err := s.srv.store.GetMessageBody(context.Background(), m.messageID, s.user.ID, 0)
	if err != nil {
		s.writeLine("-ERR message unavailable")
		return
	}
	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	s.writer.WriteString("+OK ")
	s.writer.WriteString(strconv.FormatInt(int64(len(raw)), 10))
	s.writer.WriteString(" octets\r\n")
	if err := writeStuffed(s.writer, raw); err != nil {
		s.state = stateClosed
		return
	}
	s.writer.WriteString(".\r\n")
	if err := s.writer.Flush(); err != nil {
		s.state = stateClosed
		return
	}
	s.retrievedN++
}

// cmdTop serves TOP msg n: the header block (with its separating blank line)
// plus the first n body lines, byte-stuffed like RETR. TOP is designed for
// clients that preview mail without downloading it; per RFC 1939 it may be
// refused, but we implement it fully.
func (s *session) cmdTop(arg string) {
	if !s.requireAuthState() {
		return
	}
	fields := splitArgs(arg)
	if len(fields) != 2 {
		s.writeLine("-ERR TOP requires a message number and a line count")
		return
	}
	m, ok := s.shadowBySeq(fields[0], false)
	if !ok {
		s.writeLine("-ERR no such message")
		return
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil || n < 0 {
		s.writeLine("-ERR invalid line count")
		return
	}
	raw, err := s.srv.store.GetMessageBody(context.Background(), m.messageID, s.user.ID, 0)
	if err != nil {
		s.writeLine("-ERR message unavailable")
		return
	}
	headers, body := splitHeaderBody(raw)
	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	s.writer.WriteString("+OK\r\n")
	if err := writeStuffed(s.writer, headers); err != nil {
		s.state = stateClosed
		return
	}
	if err := writeStuffed(s.writer, firstBodyLines(body, n)); err != nil {
		s.state = stateClosed
		return
	}
	s.writer.WriteString(".\r\n")
	if err := s.writer.Flush(); err != nil {
		s.state = stateClosed
		return
	}
}

// cmdDele marks a message deleted IN THE SHADOW ONLY (RFC 1939 §6: the
// mailstore is untouched until QUIT's UPDATE phase). Re-deleting the same
// message is refused; the message stays visible to every other session.
func (s *session) cmdDele(arg string) {
	if !s.requireAuthState() {
		return
	}
	m, ok := s.shadowBySeq(arg, true)
	if !ok {
		s.writeLine("-ERR no such message")
		return
	}
	if m.shadowDeleted {
		s.writeLine("-ERR message already deleted")
		return
	}
	m.shadowDeleted = true
	s.deletedN++
	s.writeLinef("+OK message %d deleted", m.seq)
}

// cmdRset unmarks every shadow deletion (pure in-session rollback — the
// store never saw the marks, so there is nothing to undo server-side).
func (s *session) cmdRset() {
	if !s.requireAuthState() {
		return
	}
	for i := range s.drop {
		s.drop[i].shadowDeleted = false
	}
	s.deletedN = 0
	s.writeLinef("+OK maildrop has %d messages (%d octets)", s.visibleCount(), s.visibleBytes())
}

// cmdNoop: +OK.
func (s *session) cmdNoop() {
	s.writeLine("+OK")
}
