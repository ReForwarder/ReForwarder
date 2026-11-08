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

package imap

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/security"
	"github.com/jkaninda/logger"
)

// state names for the session state machine (RFC 9051 §2.2.2).
const (
	stateNotAuth   = "not-authenticated"
	stateAuth      = "authenticated"
	stateSelected  = "selected"
	stateLoggedOut = "logged-out"
)

// errSessionClosed signals dispatch that the connection is finished.
var errSessionClosed = errors.New("imap: session closed")

// session is one IMAP connection. One goroutine (run) reads and dispatches;
// the only other writer is the IDLE notification drainer — every socket
// write goes through writeMu.
type session struct {
	srv    *Server
	raw    net.Conn
	conn   net.Conn
	br     *bufio.Reader
	lr     *lineReader
	out    io.Writer
	writeM sync.Mutex
	wErr   error

	remote string
	ip     netip.Addr

	guard   *security.SessionGuard
	limiter *cmdLimiter

	state string
	user  *models.User
	ms    *mailstore.MailSession

	mailboxID uint
	folder    *models.MailFolder
	view      folderView
	subUnsub  func()
	notifs    <-chan mailstore.ChangeEvent
	lastExist int64

	// closeOnce serializes close(): Server.Close (graceful shutdown) and the
	// session goroutine's own run() defer can both reach it concurrently.
	// W4 cross-wave repair (2026-11-08): the unsynchronized double close was
	// a data race between shutdown and session teardown (-race failed on it).
	closeOnce sync.Once
}

// folderView is the session's sequence-number view of the selected folder:
// UIDs in seq order plus the per-UID metadata STORE's CAS consumes.
type folderView struct {
	uids    []uint32
	msgIDs  map[uint32]uint
	modSeqs map[uint32]uint64
}

func (v *folderView) count() int { return len(v.uids) }

// seqOf returns the 1-based sequence number of uid (0 when absent).
func (v *folderView) seqOf(uid uint32) int {
	for i, u := range v.uids {
		if u == uid {
			return i + 1
		}
	}
	return 0
}

// uidOf returns the UID at 1-based seq (0 when out of range).
func (v *folderView) uidOf(seq int) uint32 {
	if seq < 1 || seq > len(v.uids) {
		return 0
	}
	return v.uids[seq-1]
}

func newSession(srv *Server, c net.Conn, remote string) *session {
	st := &session{
		srv:    srv,
		raw:    c,
		conn:   c,
		remote: remote,
		state:  stateNotAuth,
	}
	st.ip = remoteIP(remote)
	st.resetWire()
	port := 143
	if la, err := net.ResolveTCPAddr("tcp", c.LocalAddr().String()); err == nil && la.Port != 0 {
		port = la.Port
	}
	st.guard = security.NewSessionGuard(srv.secEng, st.ip, port, "imap")
	st.limiter = newCmdLimiter(srv.cfg.CommandRateLimit, srv.cfg.CommandRateWindowSecs)
	return st
}

// resetWire (re)builds the buffered reader over the current conn (STARTTLS
// swaps the connection underneath).
//
// W4 cross-wave repair (2026-11-08): st.out was never initialized, so every
// session panicked with a nil-writer dereference on its first write (the
// greeting) — newSession and the post-STARTTLS swap both route through this
// one place, which is why the writer is re-pointed here.
func (st *session) resetWire() {
	st.br = bufio.NewReader(st.conn)
	st.out = st.conn
	st.lr = &lineReader{br: st.br, out: st.conn, maxLiteral: st.literalCap()}
}

func (st *session) literalCap() int64 {
	if st.srv.cfg.MaxMessageSize > 0 {
		return st.srv.cfg.MaxMessageSize
	}
	return 64 * 1024 * 1024
}

// close tears the session down once — the IDLE subscription is released, the
// per-user session slot freed. Idempotent via closeOnce: Server.Close
// (shutdown) and the session goroutine's run() defer race here legitimately
// and exactly one of them performs the work.
func (st *session) close() {
	st.closeOnce.Do(func() {
		_ = st.conn.Close()
		if st.subUnsub != nil {
			st.subUnsub()
			st.subUnsub = nil
		}
		st.notifs = nil
		if st.user != nil {
			st.srv.unregisterSession(st.user.ID)
			st.user = nil
		}
	})
}

// write sends raw response text; the first write error latches so the read
// loop stops early.
func (st *session) write(s string) {
	st.writeM.Lock()
	defer st.writeM.Unlock()
	if st.wErr != nil {
		return
	}
	if _, err := io.WriteString(st.out, s); err != nil {
		st.wErr = err
	}
}

// writeBytes sends raw bytes (FETCH literal payloads).
func (st *session) writeBytes(b []byte) {
	st.writeM.Lock()
	defer st.writeM.Unlock()
	if st.wErr != nil {
		return
	}
	if _, err := st.out.Write(b); err != nil {
		st.wErr = err
	}
}

func (st *session) writeErr() error {
	st.writeM.Lock()
	defer st.writeM.Unlock()
	return st.wErr
}

func (st *session) tagged(tag, status, text string) {
	st.write(tag + " " + status + " " + text + "\r\n")
}

func (st *session) untagged(text string) {
	st.write("* " + text + "\r\n")
}

// run is the connection's read-dispatch loop.
func (st *session) run() {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("imap: session panic", "remote", st.remote, "error", fmt.Sprint(r))
		}
		st.close()
	}()
	_ = st.conn.SetDeadline(time.Now().Add(st.srv.cfg.IdleTimeout))
	st.write("* OK [CAPABILITY " + st.capabilityString() + "] " + ServerID + " ready\r\n")
	for {
		cmd, err := st.lr.tokenize()
		if err != nil {
			var le *literalTooLargeError
			if errors.As(err, &le) {
				// The oversized literal was never sent (no continuation was
				// issued): answer tagged NO and keep reading the stream —
				// the client's next line is its next command.
				if le.tag == "" {
					st.write("* NO [TOOBIG] literal of " + strconv.FormatInt(le.size, 10) + " octets exceeds the cap\r\n")
				} else {
					st.tagged(le.tag, "NO", "[TOOBIG] literal of "+strconv.FormatInt(le.size, 10)+" octets exceeds the cap")
				}
				continue
			}
			if !errors.Is(err, errProtocol) {
				return // IO-level: connection is done
			}
			st.write("* BAD " + err.Error() + "\r\n")
			// A malformed command may have left unread literal bytes on the
			// wire; the session cannot resynchronize safely — terminate.
			st.write("* BYE protocol error\r\n")
			return
		}
		_ = st.conn.SetDeadline(time.Now().Add(st.srv.cfg.IdleTimeout))
		if !st.limiter.allow() {
			st.write("* BYE command rate limit exceeded\r\n")
			return
		}
		st.guard.MarkCommand()
		if err := st.dispatch(cmd); err != nil {
			if errors.Is(err, errSessionClosed) {
				return
			}
			logger.Error("imap: session error", "remote", st.remote, "error", err.Error())
			st.write("* BYE internal server error\r\n")
			return
		}
		if we := st.writeErr(); we != nil {
			return
		}
		if st.state == stateLoggedOut {
			return
		}
	}
}

// dispatch routes one command.
func (st *session) dispatch(cmd *commandTokens) error {
	switch cmd.Verb {
	case "CAPABILITY":
		st.untagged("CAPABILITY " + st.capabilityString())
		st.tagged(cmd.Tag, "OK", "CAPABILITY completed")
		return nil
	case "NOOP":
		st.applyNotifications(false)
		st.tagged(cmd.Tag, "OK", "NOOP completed")
		return nil
	case "CHECK":
		if st.state != stateSelected {
			st.stateRefusal(cmd)
			return nil
		}
		st.applyNotifications(false)
		st.tagged(cmd.Tag, "OK", "CHECK completed")
		return nil
	case "LOGOUT":
		st.untagged("BYE " + ServerID + " logging out")
		st.tagged(cmd.Tag, "OK", "LOGOUT completed")
		st.state = stateLoggedOut
		return errSessionClosed
	case "STARTTLS":
		return st.cmdStartTLS(cmd)
	case "AUTHENTICATE":
		return st.cmdAuthenticate(cmd)
	case "LOGIN":
		return st.cmdLogin(cmd)
	case "IDLE":
		return st.cmdIdle(cmd)
	case "SELECT":
		return st.cmdSelect(cmd, false)
	case "EXAMINE":
		return st.cmdSelect(cmd, true)
	case "CREATE":
		return st.cmdCreate(cmd)
	case "DELETE":
		return st.cmdDelete(cmd)
	case "RENAME":
		return st.cmdRename(cmd)
	case "SUBSCRIBE":
		return st.cmdSubscribe(cmd, true)
	case "UNSUBSCRIBE":
		return st.cmdSubscribe(cmd, false)
	case "LIST":
		return st.cmdList(cmd)
	case "STATUS":
		return st.cmdStatus(cmd)
	case "APPEND":
		return st.cmdAppend(cmd)
	case "EXPUNGE":
		return st.cmdExpunge(cmd)
	case "CLOSE":
		return st.cmdClose(cmd)
	case "FETCH":
		return st.cmdFetch(cmd, false)
	case "STORE":
		return st.cmdStore(cmd, false)
	case "COPY":
		return st.cmdCopyMove(cmd, false, false)
	case "MOVE":
		return st.cmdCopyMove(cmd, false, true)
	case "SEARCH":
		return st.cmdSearch(cmd, false)
	case "UID":
		return st.cmdUID(cmd)
	}
	// Unknown command: protocol-level error, regardless of state.
	st.tagged(cmd.Tag, "BAD", "Unknown command "+cmd.Verb)
	return nil
}

// stateRefusal answers commands that are valid verbs but not valid in the
// current session state (RFC 9051: tagged BAD for wrong-state commands).
func (st *session) stateRefusal(cmd *commandTokens) {
	st.tagged(cmd.Tag, "BAD", "Command "+cmd.Verb+" not valid in the "+st.state+" state")
}

// cmdUID handles the UID variants of FETCH/STORE/COPY/MOVE/SEARCH.
func (st *session) cmdUID(cmd *commandTokens) error {
	if len(cmd.Args) == 0 {
		st.tagged(cmd.Tag, "BAD", "UID requires a command")
		return nil
	}
	sub := strings.ToUpper(cmd.Args[0].Val)
	rest := &commandTokens{Tag: cmd.Tag, Verb: "UID " + sub, Args: cmd.Args[1:]}
	switch sub {
	case "FETCH", "STORE", "COPY", "MOVE", "SEARCH":
		if sub != "SEARCH" && st.state != stateSelected {
			st.stateRefusal(cmd)
			return nil
		}
		if sub == "SEARCH" && st.state != stateSelected {
			st.stateRefusal(cmd)
			return nil
		}
		switch sub {
		case "FETCH":
			return st.cmdFetch(rest, true)
		case "STORE":
			return st.cmdStore(rest, true)
		case "COPY":
			return st.cmdCopyMove(rest, true, false)
		case "MOVE":
			return st.cmdCopyMove(rest, true, true)
		case "SEARCH":
			return st.cmdSearch(rest, true)
		}
	case "EXPUNGE":
		st.tagged(cmd.Tag, "BAD", "UID EXPUNGE not supported (UIDPLUS not advertised)")
		return nil
	}
	st.tagged(cmd.Tag, "BAD", "Unknown UID command "+sub)
	return nil
}

// capabilityString renders the CAPABILITY value for the current state.
func (st *session) capabilityString() string {
	caps := []string{"IMAP4rev1"}
	if !st.isTLS() {
		if st.srv.tlsCfg.Load() != nil {
			caps = append(caps, "STARTTLS")
		}
		if !st.cleartextAuthAllowed() {
			caps = append(caps, "LOGINDISABLED")
		}
	}
	if st.cleartextAuthAllowed() {
		caps = append(caps, "AUTH=PLAIN", "AUTH=LOGIN")
	}
	if st.state == stateAuth || st.state == stateSelected {
		caps = append(caps, "IDLE")
	}
	return strings.Join(caps, " ")
}

// cleartextAuthAllowed applies ADR-004 decision 4: authentication before
// STARTTLS is refused unless the deployment opted in AND the peer is on
// loopback.
func (st *session) cleartextAuthAllowed() bool {
	if st.isTLS() {
		return true
	}
	return st.srv.cfg.AllowCleartextAuth && st.ip.IsLoopback()
}

func (st *session) isTLS() bool { return st.conn != st.raw }

// tlsVersionMin pins the STARTTLS floor (TLS 1.2, matching the SMTP
// listeners' TLS config).
func tlsVersionMin() uint16 { return tls.VersionTLS12 }

// tlsServer wraps conn in a server-side TLS connection with our config.
func tlsServer(c net.Conn, cfg *tls.Config) *tls.Conn { return tls.Server(c, cfg) }

// cmdStartTLS upgrades the connection in place (RFC 9051 §6.2.1; pre-auth
// only, once).
func (st *session) cmdStartTLS(cmd *commandTokens) error {
	if st.isTLS() {
		st.tagged(cmd.Tag, "BAD", "STARTTLS already active")
		return nil
	}
	tc := st.srv.tlsCfg.Load()
	if tc == nil {
		st.tagged(cmd.Tag, "NO", "TLS not available")
		return nil
	}
	st.tagged(cmd.Tag, "OK", "Begin TLS negotiation now")
	// Derive the per-connection TLS config without copying the stored
	// *tls.Config value (it carries a mutex): build a fresh config sharing
	// the certificates.
	up := &tls.Config{
		Certificates: tc.Certificates,
		ServerName:   st.srv.cfg.Hostname,
		MinVersion:   tlsVersionMin(),
	}
	if up.ServerName == "" {
		up.ServerName = "localhost"
	}
	st.writeM.Lock()
	tlsConn := tlsServer(st.conn, up)
	st.writeM.Unlock()
	if err := tlsConn.Handshake(); err != nil {
		logger.Warn("imap: STARTTLS handshake failed", "remote", st.remote, "error", err.Error())
		return errSessionClosed
	}
	st.conn = tlsConn
	st.resetWire()
	return nil
}

// finishAuth completes a successful authentication: session registry, state
// transition, audit, and the tagged OK with a fresh CAPABILITY.
func (st *session) finishAuth(tag string, user *models.User, detail string) error {
	if !st.srv.registerSession(user.ID) {
		st.tagged(tag, "NO", "[UNAVAILABLE] Too many concurrent sessions for this user")
		return nil
	}
	st.user = user
	st.ms = mailstore.NewMailSession(user.ID, user.Email)
	st.state = stateAuth
	st.srv.auditf(AuditAuthSuccess, user.ID, user.Email, st.remote, map[string]any{"protocol": "imap"})
	st.tagged(tag, "OK", "[CAPABILITY "+st.capabilityString()+"] "+detail)
	return nil
}

// authFailed records the failure with the security engine and audit substrate.
func (st *session) authFailed(tag, login string) {
	st.guard.RecordAuthFailure(login)
	st.srv.auditf(AuditAuthFailure, 0, login, st.remote, map[string]any{"protocol": "imap"})
	st.tagged(tag, "NO", "[AUTHENTICATIONFAILED] Invalid credentials")
}

// resolveMailbox finds the mailbox the session operates on: the hosted
// mailbox named by the authenticated user's email, falling back to the
// user's single mailbox. Cached per session.
func (st *session) resolveMailbox(ctx context.Context) (*models.Mailbox, error) {
	if st.mailboxID != 0 {
		return st.srv.store.GetMailbox(st.mailboxID, st.user.ID, 0)
	}
	mb, err := st.srv.store.FindMailboxByName(st.user.ID, st.user.Email)
	if errors.Is(err, mailstore.ErrMailboxNotFound) {
		mbs, lerr := st.srv.store.ListMailboxes(st.user.ID, 0)
		if lerr == nil && len(mbs) == 1 {
			mb = &mbs[0]
		} else {
			return nil, mailstore.ErrMailboxNotFound
		}
	} else if err != nil {
		return nil, err
	}
	st.mailboxID = mb.ID
	return mb, nil
}

// requireSelection returns the selected folder or answers a tagged BAD for
// commands that need one.
func (st *session) requireSelection(tag string) (*models.MailFolder, bool) {
	if st.state != stateSelected || st.folder == nil || st.ms == nil || !st.ms.Selected() {
		st.tagged(tag, "BAD", "No folder selected")
		return nil, false
	}
	return st.folder, true
}

// refreshView re-derives the sequence view from the store (UID order,
// per-UID message IDs and mod-seqs — the snapshot STORE's CAS consumes).
func (st *session) refreshView(ctx context.Context) error {
	if st.state != stateSelected || st.folder == nil {
		return nil
	}
	set, err := mailstore.ParseSeqSet("1:*")
	if err != nil {
		return err
	}
	res, err := st.srv.store.FetchRange(ctx, mailstore.FetchRangeSpec{
		FolderID:    st.folder.ID,
		UserID:      st.user.ID,
		WorkspaceID: 0,
		UIDMode:     false,
		Set:         set,
	})
	if err != nil {
		return err
	}
	v := folderView{
		uids:    make([]uint32, 0, len(res)),
		msgIDs:  make(map[uint32]uint, len(res)),
		modSeqs: make(map[uint32]uint64, len(res)),
	}
	for _, r := range res {
		v.uids = append(v.uids, r.UID)
		v.msgIDs[r.UID] = r.MessageID
		v.modSeqs[r.UID] = r.Message.ModSeq
	}
	st.view = v
	return nil
}

// applyNotifications drains pending folder-change events into untagged
// responses. RFC 9051: EXPUNGE responses must not be sent while a FETCH is
// streaming (duringFetch suppresses them; EXISTS/FLAGS remain allowed).
func (st *session) applyNotifications(duringFetch bool) {
	if st.notifs == nil {
		return
	}
	var events []mailstore.ChangeEvent
	drain:
	for {
		select {
		case ev, ok := <-st.notifs:
			if !ok {
				st.notifs = nil
				if st.subUnsub != nil {
					st.subUnsub()
					st.subUnsub = nil
				}
				break drain
			}
			events = append(events, ev)
			if len(events) >= 256 {
				break drain
			}
		default:
			break drain
		}
	}
	if len(events) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	old := st.view.uids
	if err := st.refreshView(ctx); err != nil {
		logger.Warn("imap: view refresh failed", "error", err.Error())
		return
	}
	newSet := make(map[uint32]bool, len(st.view.uids))
	for _, u := range st.view.uids {
		newSet[u] = true
	}
	// Expunges: UIDs present in the old view, absent now — announced
	// newest-first (RFC 9051 §7.5.1: each successive EXPUNGE number
	// decreases, so every announced seq stays valid in the shrinking view).
	if !duringFetch && len(old) > 0 {
		for i := len(old) - 1; i >= 0; i-- {
			if !newSet[old[i]] {
				st.untagged(fmt.Sprintf("%d EXPUNGE", i+1))
			}
		}
	}
	// EXISTS when the folder grew.
	if st.lastExist != int64(len(st.view.uids)) {
		st.lastExist = int64(len(st.view.uids))
		st.untagged(fmt.Sprintf("%d EXISTS", len(st.view.uids)))
	}
	// Flag changes announced for messages still present.
	for _, ev := range events {
		if ev.Kind != mailstore.KindFlags {
			continue
		}
		if seq := st.view.seqOf(ev.UID); seq > 0 {
			st.untagged(fmt.Sprintf("%d FETCH (FLAGS (%s))", seq, flagNames(ev.Flags)))
		}
	}
}

// subscribe wires the session to the selected folder's change notifications
// (the W1 ChangeNotifier — ADR-004 decision 7).
func (st *session) subscribe() {
	if st.subUnsub != nil {
		st.subUnsub()
		st.subUnsub = nil
	}
	if st.folder == nil {
		st.notifs = nil
		return
	}
	n := st.srv.store.ChangeNotifier()
	if n == nil {
		st.notifs = nil
		return
	}
	ch, unsub := n.Subscribe(context.Background(), st.folder.ID)
	st.notifs = ch
	st.subUnsub = unsub
}

// flagNames renders a flag bitmask as IMAP system flags (the mailstore's
// bitmask is IMAP's system-flag set; custom keywords are not stored — the
// documented mailstore limitation).
func flagNames(f uint32) string {
	var parts []string
	if f&models.FlagSeen != 0 {
		parts = append(parts, "\\Seen")
	}
	if f&models.FlagAnswered != 0 {
		parts = append(parts, "\\Answered")
	}
	if f&models.FlagFlagged != 0 {
		parts = append(parts, "\\Flagged")
	}
	if f&models.FlagDraft != 0 {
		parts = append(parts, "\\Draft")
	}
	if f&models.FlagDeleted != 0 {
		parts = append(parts, "\\Deleted")
	}
	if f&models.FlagRecent != 0 {
		parts = append(parts, "\\Recent")
	}
	return strings.Join(parts, " ")
}

// parseFlagList converts IMAP flag atoms into a Set bitmask. Unknown
// keywords are ignored (custom keywords are not stored — documented
// limitation).
func parseFlagList(atoms []string) uint32 {
	var set uint32
	for _, a := range atoms {
		switch strings.ToUpper(strings.TrimPrefix(a, "\\")) {
		case "SEEN":
			set |= models.FlagSeen
		case "ANSWERED":
			set |= models.FlagAnswered
		case "FLAGGED":
			set |= models.FlagFlagged
		case "DRAFT":
			set |= models.FlagDraft
		case "DELETED":
			set |= models.FlagDeleted
		case "RECENT":
			// \Recent is server-managed (mailstore semantics); client stores
			// of \Recent are ignored per RFC 9051 §2.3.2.
		}
	}
	return set
}

// quoteString renders an IMAP quoted string with the two mandatory escapes.
func quoteString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	b.WriteByte('"')
	return b.String()
}

// internalDate renders the IMAP date-time for INTERNALDATE responses.
func internalDate(t time.Time) string {
	return t.Format("02-Jan-2006 15:04:05 -0700")
}
