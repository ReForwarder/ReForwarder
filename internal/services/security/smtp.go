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

package security

import (
	"context"
	"net/netip"
	"sync/atomic"

	"github.com/emersion/go-smtp"
)

// SessionGuard is the per-connection enforcement helper the SMTP backends
// (inbound receiver, relay) create in Backend.NewSession and drive through
// the session lifecycle. It covers the two hooks go-smtp offers a backend:
//
//   - NewSession runs on the first HELO/EHLO (after the 220 greeting), where
//     GuardNewSession re-checks the ban snapshot for IPs banned between TCP
//     accept and EHLO and answers EHLO with 421 4.7.1 when blocked;
//   - Logout fires on every connection end (QUIT, idle timeout, protocol
//     errors, client disconnect), where OnLogout reports a connect abort when
//     the session never saw a command.
//
// Ban-rejects answer the client's EHLO with a protocol-visible 421 and rely
// on the server's ReadTimeout to reap the socket (go-smtp keeps the
// connection open after a failed NewSession).
//
// A nil engine produces a no-op guard, and every method is safe on a nil
// receiver, so backends can call the guard unconditionally.
type SessionGuard struct {
	eng   *Engine
	ip    netip.Addr
	port  int
	proto string

	sawCommand atomic.Bool

	// conn backs the STARTTLS distinction in OnLogout; tlsAtStart records
	// whether TLS was already in place when the session was created.
	conn       *smtp.Conn
	tlsAtStart bool
}

// NewSessionGuard builds a guard for one SMTP session. eng may be nil (the
// security feature is disabled), in which case the guard no-ops.
func NewSessionGuard(eng *Engine, ip netip.Addr, port int, proto string) *SessionGuard {
	return &SessionGuard{eng: eng, ip: ip.Unmap(), port: port, proto: proto}
}

// WatchConn attaches the go-smtp connection so OnLogout can tell a STARTTLS
// upgrade apart from a pre-transaction disconnect. go-smtp logs out the
// pre-TLS session after swapping the connection to TLS — without this, every
// legitimate STARTTLS client would be counted as a connect abort.
func (g *SessionGuard) WatchConn(c *smtp.Conn) {
	if g == nil || c == nil {
		return
	}
	g.conn = c
	_, g.tlsAtStart = c.TLSConnectionState()
}

// GuardNewSession re-checks the ban snapshot at session creation (first
// EHLO). The snapshot is in-memory, so the check is cheap enough for the
// repeated-EHLO patterns a rejected client produces. Blocked peers get
// 421 4.7.1 as their EHLO reply.
func (g *SessionGuard) GuardNewSession() error {
	if g == nil || g.eng == nil || g.eng.Bans == nil {
		return nil
	}
	if g.eng.Bans.IsAllowed(g.ip) {
		return nil
	}
	if blocked, _ := g.eng.Bans.IsBlocked(g.ip); blocked {
		return &smtp.SMTPError{
			Code:         421,
			EnhancedCode: smtp.EnhancedCode{4, 7, 1},
			Message:      "Your IP address has been blocked",
		}
	}
	return nil
}

// MarkCommand records that the client sent at least one command. Backends
// call it at the top of every session command method (Mail, Rcpt, Reset,
// Data, Auth). A session that ends without any of these firing is a
// connect: connect → EHLO → vanish, counted as an abort by OnLogout.
func (g *SessionGuard) MarkCommand() {
	if g == nil {
		return
	}
	g.sawCommand.Store(true)
}

// OnLogout ends the guard. When the session processed no command the peer is
// recorded as a connect abort (zero-transaction disconnect). A STARTTLS
// upgrade is excluded: go-smtp logs the pre-TLS session out after the
// connection has switched to TLS, which is a mid-connection upgrade, not an
// abort — sessions created after the upgrade still carry tlsAtStart and are
// judged normally.
func (g *SessionGuard) OnLogout() {
	if g == nil || g.eng == nil || g.eng.Detect == nil {
		return
	}
	if g.sawCommand.Load() {
		return
	}
	if g.conn != nil {
		if _, isTLS := g.conn.TLSConnectionState(); isTLS && !g.tlsAtStart {
			return
		}
	}
	_ = g.eng.Detect.RecordAbort(context.Background(), g.ip, g.port, g.proto)
}

// RecordAuthFailure records a failed AUTH attempt for brute-force detection.
// The detector keys it by source IP and by sha256(login), so attacker input
// never becomes a Redis key.
func (g *SessionGuard) RecordAuthFailure(login string) {
	if g == nil || g.eng == nil || g.eng.Detect == nil {
		return
	}
	_ = g.eng.Detect.RecordAuthFailure(context.Background(), g.ip, login)
}

// RecordMalformed records a malformed or rejected pre-auth message — for the
// backends this is the rejected-recipient path (550 on RCPT), a proxy signal
// for SMTP protocol abuse: go-smtp v0.25.0 exposes no per-command hook for
// unrecognized commands or protocol errors.
func (g *SessionGuard) RecordMalformed(detail string) {
	if g == nil || g.eng == nil || g.eng.Detect == nil {
		return
	}
	_ = g.eng.Detect.RecordMalformed(context.Background(), g.ip, g.port, g.proto, detail)
}
