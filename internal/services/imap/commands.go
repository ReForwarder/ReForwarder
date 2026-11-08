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
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailauth"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/jkaninda/logger"
)

// ─── Authentication ──────────────────────────────────────────────────────────

// cmdLogin handles the classic LOGIN command (RFC 9051 §6.2.3): credentials
// are verified exclusively against app passwords via mailauth — the primary
// account password is structurally never consulted.
func (st *session) cmdLogin(cmd *commandTokens) error {
	if st.state != stateNotAuth {
		st.tagged(cmd.Tag, "BAD", "Already authenticated")
		return nil
	}
	userArg, i, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "LOGIN requires a user and password")
		return nil
	}
	passArg, _, err := wantString(cmd.Args, i)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "LOGIN requires a user and password")
		return nil
	}
	return st.authenticate(cmd.Tag, userArg, passArg)
}

// cmdAuthenticate handles AUTHENTICATE (RFC 9051 §6.2.2): PLAIN and LOGIN
// mechanisms, both backed by the app-password seam. SASL initial responses
// and the "*" cancel are honored.
func (st *session) cmdAuthenticate(cmd *commandTokens) error {
	if st.state != stateNotAuth {
		st.tagged(cmd.Tag, "BAD", "Already authenticated")
		return nil
	}
	mech, i, err := wantAtom(cmd.Args, 0, true)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "AUTHENTICATE requires a mechanism")
		return nil
	}
	if !st.cleartextAuthAllowed() {
		st.srv.auditf(AuditCleartextAuthDeny, 0, "", st.remote, map[string]any{"protocol": "imap", "mechanism": mech})
		st.tagged(cmd.Tag, "NO", "[PRIVACYREQUIRED] STARTTLS required before authentication")
		return nil
	}
	switch mech {
	case "PLAIN":
		// Optional SASL initial response: base64("authzid\0authcid\0passwd").
		var resp string
		if i < len(cmd.Args) {
			if cmd.Args[i].Kind != 'a' && cmd.Args[i].Kind != 's' {
				st.tagged(cmd.Tag, "BAD", "Invalid initial response")
				return nil
			}
			resp = cmd.Args[i].Val
		} else {
			st.write("+ \r\n")
			line, rerr := st.lr.readLine()
			if rerr != nil {
				return rerr
			}
			resp = strings.TrimSpace(line)
		}
		if resp == "*" {
			st.tagged(cmd.Tag, "BAD", "Authentication cancelled")
			return nil
		}
		raw, derr := base64.StdEncoding.DecodeString(resp)
		if derr != nil {
			st.tagged(cmd.Tag, "BAD", "Invalid base64 response")
			return nil
		}
		parts := strings.SplitN(string(raw), "\x00", 3)
		if len(parts) != 3 {
			st.tagged(cmd.Tag, "BAD", "Invalid PLAIN response")
			return nil
		}
		authzid, authcid, passwd := parts[0], parts[1], parts[2]
		if authzid != "" && authzid != authcid {
			st.tagged(cmd.Tag, "NO", "[AUTHORIZATIONFAILED] Proxy authentication not supported")
			return nil
		}
		return st.authenticate(cmd.Tag, authcid, passwd)

	case "LOGIN":
		st.write("+ VXNlcm5hbWU6\r\n") // "Username:"
		line, rerr := st.lr.readLine()
		if rerr != nil {
			return rerr
		}
		if strings.TrimSpace(line) == "*" {
			st.tagged(cmd.Tag, "BAD", "Authentication cancelled")
			return nil
		}
		userRaw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
		if derr != nil {
			st.tagged(cmd.Tag, "BAD", "Invalid base64 username")
			return nil
		}
		st.write("+ UGFzc3dvcmQ6\r\n") // "Password:"
		line2, rerr2 := st.lr.readLine()
		if rerr2 != nil {
			return rerr2
		}
		if strings.TrimSpace(line2) == "*" {
			st.tagged(cmd.Tag, "BAD", "Authentication cancelled")
			return nil
		}
		passRaw, derr2 := base64.StdEncoding.DecodeString(strings.TrimSpace(line2))
		if derr2 != nil {
			st.tagged(cmd.Tag, "BAD", "Invalid base64 password")
			return nil
		}
		return st.authenticate(cmd.Tag, string(userRaw), string(passRaw))
	}
	st.tagged(cmd.Tag, "NO", "Unsupported authentication mechanism")
	return nil
}

// authenticate verifies one credential pair through the app-password seam
// (mailauth.Authenticator — bcrypt over app-password digests only) and
// completes the login.
func (st *session) authenticate(tag, email, secret string) error {
	if !st.cleartextAuthAllowed() {
		st.tagged(tag, "NO", "[PRIVACYREQUIRED] STARTTLS required before authentication")
		return nil
	}
	user, err := st.srv.auth.AuthenticateWithAppPassword(context.Background(), email, secret)
	if err != nil {
		if !errors.Is(err, mailauth.ErrInvalidCredentials) {
			// An infrastructure error (repo failure) must not read as a bad
			// credential: log it distinctly; the wire answer stays generic
			// but tagged NO either way.
			logger.Error("imap: auth backend error", "remote", st.remote, "error", err.Error())
		}
		st.authFailed(tag, email)
		return nil
	}
	return st.finishAuth(tag, user, "Authenticated")
}

// ─── Folder selection ────────────────────────────────────────────────────────

// cmdSelect implements SELECT and EXAMINE (RFC 9051 §6.3.1/§6.3.2): the W1
// MailSession carries the read-write vs read-only mode; EXAMINE sessions
// refuse every mutating command.
func (st *session) cmdSelect(cmd *commandTokens, examine bool) error {
	if st.state == stateNotAuth {
		st.stateRefusal(cmd)
		return nil
	}
	path, _, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "SELECT requires a mailbox name")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mb, err := st.resolveMailbox(ctx)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No mailbox available for this account")
		return nil
	}
	f, err := st.srv.store.GetFolderByPath(mb.ID, st.user.ID, 0, path)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No such mailbox")
		return nil
	}
	if examine {
		st.ms.Examine(f)
	} else {
		st.ms.Select(f)
	}
	st.folder = f
	st.state = stateSelected
	st.subscribe()
	if err := st.refreshView(ctx); err != nil {
		st.folder = nil
		st.state = stateAuth
		st.tagged(cmd.Tag, "NO", "Folder listing failed")
		return nil
	}
	count := st.view.count()
	fst, _ := st.srv.store.StatusCounts(f.ID, st.user.ID, 0)
	recent := int64(0)
	if fst != nil {
		recent = fst.Recent
	}
	st.untagged(fmt.Sprintf("%d EXISTS", count))
	st.untagged(fmt.Sprintf("%d RECENT", recent))
	st.untagged("FLAGS (\\Answered \\Flagged \\Draft \\Deleted \\Seen \\Recent)")
	st.untagged("OK [PERMANENTFLAGS (\\Answered \\Flagged \\Draft \\Deleted \\Seen \\*)] Flags permitted")
	st.untagged(fmt.Sprintf("OK [UIDVALIDITY %d] UIDs valid", f.UIDValidity))
	st.untagged(fmt.Sprintf("OK [UIDNEXT %d] Predicted next UID", f.UIDNext))
	st.lastExist = int64(count)
	st.srv.auditf(AuditSelect, st.user.ID, st.user.Email, st.remote,
		map[string]any{"folder": f.IMAPPath, "read_only": examine})
	if examine {
		st.tagged(cmd.Tag, "OK", "[READ-ONLY] EXAMINE completed")
	} else {
		st.tagged(cmd.Tag, "OK", "[READ-WRITE] SELECT completed")
	}
	return nil
}

// deselect tears down the selection state.
func (st *session) deselect() {
	if st.subUnsub != nil {
		st.subUnsub()
		st.subUnsub = nil
	}
	st.notifs = nil
	st.folder = nil
	st.view = folderView{}
	st.lastExist = 0
	if st.ms != nil {
		st.ms.Deselect()
	}
	if st.state == stateSelected {
		st.state = stateAuth
	}
}

// ─── Folder management ───────────────────────────────────────────────────────

// cmdCreate implements CREATE (RFC 9051 §6.3.3), creating intermediate
// hierarchy levels when missing.
func (st *session) cmdCreate(cmd *commandTokens) error {
	if st.state == stateNotAuth {
		st.stateRefusal(cmd)
		return nil
	}
	path, _, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "CREATE requires a mailbox name")
		return nil
	}
	path = strings.TrimSuffix(path, mailstore.IMAPDelimiter)
	if strings.EqualFold(path, "INBOX") || path == "" {
		st.tagged(cmd.Tag, "NO", "INBOX exists and cannot be created")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mb, err := st.resolveMailbox(ctx)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No mailbox available for this account")
		return nil
	}
	parent := ""
	seg := strings.Split(path, mailstore.IMAPDelimiter)
	for i := 0; i < len(seg)-1; i++ {
		if _, err := st.srv.store.GetFolderByPath(mb.ID, st.user.ID, 0, strings.Join(seg[:i+1], mailstore.IMAPDelimiter)); errors.Is(err, mailstore.ErrFolderNotFound) {
			if _, err := st.srv.store.CreateFolder(mb.ID, st.user.ID, 0, parent, seg[i]); err != nil {
				st.tagged(cmd.Tag, "NO", "Create failed: "+err.Error())
				return nil
			}
		}
		parent = strings.Join(seg[:i+1], mailstore.IMAPDelimiter)
	}
	if _, err := st.srv.store.CreateFolder(mb.ID, st.user.ID, 0, parent, seg[len(seg)-1]); err != nil {
		if errors.Is(err, mailstore.ErrFolderExists) {
			st.tagged(cmd.Tag, "NO", "Mailbox already exists")
			return nil
		}
		st.tagged(cmd.Tag, "NO", "Create failed: "+err.Error())
		return nil
	}
	st.tagged(cmd.Tag, "OK", "CREATE completed")
	return nil
}

// cmdDelete implements DELETE (RFC 9051 §6.3.4). INBOX is immutable
// (mailstore.ErrFolderImmutable).
func (st *session) cmdDelete(cmd *commandTokens) error {
	if st.state == stateNotAuth {
		st.stateRefusal(cmd)
		return nil
	}
	path, _, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "DELETE requires a mailbox name")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mb, err := st.resolveMailbox(ctx)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No mailbox available for this account")
		return nil
	}
	f, err := st.srv.store.GetFolderByPath(mb.ID, st.user.ID, 0, path)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No such mailbox")
		return nil
	}
	if err := st.srv.store.DeleteFolder(f.ID, st.user.ID, 0); err != nil {
		if errors.Is(err, mailstore.ErrFolderImmutable) {
			st.tagged(cmd.Tag, "NO", "INBOX cannot be deleted")
			return nil
		}
		st.tagged(cmd.Tag, "NO", "Delete failed: "+err.Error())
		return nil
	}
	st.tagged(cmd.Tag, "OK", "DELETE completed")
	return nil
}

// cmdRename implements RENAME (RFC 9051 §6.3.5). Renaming INBOX is refused
// (the mailstore's documented limitation).
func (st *session) cmdRename(cmd *commandTokens) error {
	if st.state == stateNotAuth {
		st.stateRefusal(cmd)
		return nil
	}
	path, i, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "RENAME requires source and destination")
		return nil
	}
	newName, _, err := wantString(cmd.Args, i)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "RENAME requires source and destination")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mb, err := st.resolveMailbox(ctx)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No mailbox available for this account")
		return nil
	}
	f, err := st.srv.store.GetFolderByPath(mb.ID, st.user.ID, 0, path)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No such mailbox")
		return nil
	}
	if strings.EqualFold(f.IMAPPath, "INBOX") {
		st.tagged(cmd.Tag, "NO", "INBOX cannot be renamed")
		return nil
	}
	// Only the leaf may change (the mailstore rewrites descendants).
	leaf := newName
	if idx := strings.LastIndex(newName, mailstore.IMAPDelimiter); idx >= 0 {
		leaf = newName[idx+1:]
	}
	if _, err := st.srv.store.RenameFolder(f.ID, st.user.ID, 0, leaf); err != nil {
		if errors.Is(err, mailstore.ErrFolderExists) {
			st.tagged(cmd.Tag, "NO", "Destination mailbox already exists")
			return nil
		}
		st.tagged(cmd.Tag, "NO", "Rename failed: "+err.Error())
		return nil
	}
	st.tagged(cmd.Tag, "OK", "RENAME completed")
	return nil
}

// cmdSubscribe implements SUBSCRIBE/UNSUBSCRIBE (RFC 9051 §6.3.6).
func (st *session) cmdSubscribe(cmd *commandTokens, subscribe bool) error {
	if st.state == stateNotAuth {
		st.stateRefusal(cmd)
		return nil
	}
	path, _, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "A mailbox name is required")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mb, err := st.resolveMailbox(ctx)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No mailbox available for this account")
		return nil
	}
	f, err := st.srv.store.GetFolderByPath(mb.ID, st.user.ID, 0, path)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No such mailbox")
		return nil
	}
	if err := st.srv.store.SetSubscribed(f.ID, st.user.ID, 0, subscribe); err != nil {
		st.tagged(cmd.Tag, "NO", "Subscription update failed")
		return nil
	}
	st.tagged(cmd.Tag, "OK", "Command completed")
	return nil
}

// ─── LIST / STATUS ───────────────────────────────────────────────────────────

// cmdList implements LIST (RFC 9051 §6.3.9). Only mailboxes that themselves
// match the pattern are returned (no \NonExistent parents — documented).
func (st *session) cmdList(cmd *commandTokens) error {
	if st.state == stateNotAuth {
		st.stateRefusal(cmd)
		return nil
	}
	ref, i, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "LIST requires reference and pattern")
		return nil
	}
	var patterns []string
	for i < len(cmd.Args) {
		p, ni, perr := wantString(cmd.Args, i)
		if perr != nil {
			break
		}
		patterns = append(patterns, p)
		i = ni
	}
	if len(patterns) == 0 {
		st.tagged(cmd.Tag, "BAD", "LIST requires a pattern")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mb, err := st.resolveMailbox(ctx)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No mailbox available for this account")
		return nil
	}
	folders, err := st.srv.store.ListFolders(mb.ID, st.user.ID, 0)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "Folder listing failed")
		return nil
	}
	// Empty pattern: return the hierarchy delimiter only.
	if len(patterns) == 1 && patterns[0] == "" {
		st.untagged(`LIST (\Noselect) "/" ""`)
		st.tagged(cmd.Tag, "OK", "LIST completed")
		return nil
	}
	childCounts := make(map[string]int, len(folders))
	for _, f := range folders {
		if p := parentPath(f.IMAPPath); p != "" {
			childCounts[p]++
		}
	}
	for _, f := range folders {
		for _, pat := range patterns {
			full := pat
			if !strings.HasPrefix(full, mailstore.IMAPDelimiter) {
				full = ref + full
			}
			if matchListPattern(f.IMAPPath, full) {
				attr := "\\HasNoChildren"
				if childCounts[f.IMAPPath] > 0 {
					attr = "\\HasChildren"
				}
				st.untagged(fmt.Sprintf("LIST (%s) \"/\" %s", attr, quoteString(f.IMAPPath)))
				break
			}
		}
	}
	st.tagged(cmd.Tag, "OK", "LIST completed")
	return nil
}

// parentPath returns the path's parent ("" at the root).
func parentPath(p string) string {
	if i := strings.LastIndex(p, mailstore.IMAPDelimiter); i > 0 {
		return p[:i]
	}
	return ""
}

// matchListPattern matches an IMAP LIST pattern against a path: '*' spans
// any characters including the delimiter, '%' any characters except it.
// INBOX is matched case-insensitively (RFC 9051 §6.3.9): the pattern's first
// segment is canonicalized before matching (stored paths use canonical INBOX).
func matchListPattern(path, pattern string) bool {
	return listMatch(path, canonicalizeListPattern(pattern))
}

// canonicalizeListPattern uppercases the pattern's INBOX first segment.
func canonicalizeListPattern(pattern string) string {
	seg := strings.SplitN(pattern, mailstore.IMAPDelimiter, 2)
	if strings.EqualFold(seg[0], "INBOX") {
		seg[0] = "INBOX"
	}
	return strings.Join(seg, mailstore.IMAPDelimiter)
}

func listMatch(path, pattern string) bool {
	if pattern == "" {
		return path == ""
	}
	switch pattern[0] {
	case '*':
		for i := 0; i <= len(path); i++ {
			if listMatch(path[i:], pattern[1:]) {
				return true
			}
		}
		return false
	case '%':
		for i := 0; i <= len(path); i++ {
			if strings.Contains(path[:i], mailstore.IMAPDelimiter) {
				break // '%' must not span the delimiter
			}
			if listMatch(path[i:], pattern[1:]) {
				return true
			}
		}
		return false
	default:
		if path == "" || path[0] != pattern[0] {
			return false
		}
		return listMatch(path[1:], pattern[1:])
	}
}

// cmdStatus implements STATUS (RFC 9051 §6.3.11).
func (st *session) cmdStatus(cmd *commandTokens) error {
	if st.state == stateNotAuth {
		st.stateRefusal(cmd)
		return nil
	}
	path, i, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "STATUS requires a mailbox and items")
		return nil
	}
	var items []string
	for i < len(cmd.Args) {
		switch cmd.Args[i].Kind {
		case '(':
		case ')':
		case 'a', 's':
			items = append(items, strings.ToUpper(cmd.Args[i].Val))
		}
		i++
	}
	if len(items) == 0 {
		st.tagged(cmd.Tag, "BAD", "STATUS requires at least one item")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mb, err := st.resolveMailbox(ctx)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No mailbox available for this account")
		return nil
	}
	f, err := st.srv.store.GetFolderByPath(mb.ID, st.user.ID, 0, path)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No such mailbox")
		return nil
	}
	fst, err := st.srv.store.StatusCounts(f.ID, st.user.ID, 0)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "STATUS failed")
		return nil
	}
	var b strings.Builder
	b.WriteByte('(')
	for _, it := range items {
		switch it {
		case "MESSAGES":
			fmt.Fprintf(&b, "MESSAGES %d ", fst.Messages)
		case "RECENT":
			fmt.Fprintf(&b, "RECENT %d ", fst.Recent)
		case "UIDNEXT":
			fmt.Fprintf(&b, "UIDNEXT %d ", f.UIDNext)
		case "UIDVALIDITY":
			fmt.Fprintf(&b, "UIDVALIDITY %d ", f.UIDValidity)
		case "UNSEEN":
			fmt.Fprintf(&b, "UNSEEN %d ", fst.Unseen)
		default:
			st.tagged(cmd.Tag, "BAD", "Unknown STATUS item "+it)
			return nil
		}
	}
	out := strings.TrimRight(b.String(), " ") + ")"
	st.untagged("STATUS " + quoteString(f.IMAPPath) + " " + out)
	st.tagged(cmd.Tag, "OK", "STATUS completed")
	return nil
}

// ─── APPEND ──────────────────────────────────────────────────────────────────

// cmdAppend implements APPEND (RFC 9051 §6.3.12): an optional flag list and
// internal date precede the message literal. Over-quota appends surface as
// `NO [OVERQUOTA]`; a missing target mailbox as `NO [TRYCREATE]`.
func (st *session) cmdAppend(cmd *commandTokens) error {
	if st.state == stateNotAuth {
		st.stateRefusal(cmd)
		return nil
	}
	if st.ms != nil {
		// The W1 session contract: EXAMINE-selected sessions refuse writes.
		if err := st.ms.CanWrite(); err != nil {
			st.tagged(cmd.Tag, "NO", "[READ-ONLY] Session is read-only (EXAMINE)")
			return nil
		}
	}
	path, i, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "APPEND requires a mailbox and a message literal")
		return nil
	}
	var flagAtoms []string
	var dateArg string
	for i < len(cmd.Args)-1 {
		t := cmd.Args[i]
		if t.Kind == '(' {
			i++
			for i < len(cmd.Args) && cmd.Args[i].Kind != ')' {
				if cmd.Args[i].Kind == 'a' {
					flagAtoms = append(flagAtoms, cmd.Args[i].Val)
				}
				i++
			}
			if i < len(cmd.Args) {
				i++ // consume ')'
			}
			continue
		}
		if t.Kind == 's' {
			dateArg = t.Val
		}
		i++
	}
	// The message literal is the last token.
	if i >= len(cmd.Args) || cmd.Args[i].Kind != 's' {
		st.tagged(cmd.Tag, "BAD", "APPEND requires a message literal")
		return nil
	}
	msg := []byte(cmd.Args[i].Val)
	_ = dateArg // client-supplied INTERNALDATE is accepted and ignored (server time is used — documented limitation)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mb, err := st.resolveMailbox(ctx)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No mailbox available for this account")
		return nil
	}
	f, err := st.srv.store.GetFolderByPath(mb.ID, st.user.ID, 0, path)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "[TRYCREATE] No such mailbox")
		return nil
	}
	flags := parseFlagList(flagAtoms)
	res, err := st.srv.store.Append(ctx, st.user.ID, 0, mailstore.AppendIn{
		MailboxID: mb.ID,
		FolderID:  f.ID,
		Raw:       msg,
		Flags:     flags,
	})
	if err != nil {
		switch {
		case errors.Is(err, mailstore.ErrQuotaExceeded):
			st.srv.auditf(AuditAppendQuotaDenied, st.user.ID, st.user.Email, st.remote,
				map[string]any{"folder": f.IMAPPath, "bytes": len(msg)})
			st.tagged(cmd.Tag, "NO", "[OVERQUOTA] Mailbox quota exceeded")
		case errors.Is(err, mailstore.ErrEmptyMessage):
			st.tagged(cmd.Tag, "NO", "Empty message refused")
		default:
			st.tagged(cmd.Tag, "NO", "APPEND failed: "+err.Error())
		}
		return nil
	}
	st.srv.auditf(AuditAppend, st.user.ID, st.user.Email, st.remote,
		map[string]any{"folder": f.IMAPPath, "bytes": len(msg), "uid": res.UID})
	st.tagged(cmd.Tag, "OK", "APPEND completed")
	st.applyNotifications(false)
	return nil
}

// ─── EXPUNGE / CLOSE ─────────────────────────────────────────────────────────

// cmdExpunge implements EXPUNGE (RFC 9051 §6.4.3): permanently removes
// \Deleted messages and announces each by descending sequence number.
func (st *session) cmdExpunge(cmd *commandTokens) error {
	f, ok := st.requireSelection(cmd.Tag)
	if !ok {
		return nil
	}
	if err := st.ms.CanWrite(); err != nil {
		st.tagged(cmd.Tag, "NO", "[READ-ONLY] Session is read-only (EXAMINE)")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := st.srv.store.Expunge(f.ID, st.user.ID, 0)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "EXPUNGE failed: "+err.Error())
		return nil
	}
	// Refresh the view, then announce the vanished UIDs newest-first.
	old := st.view.uids
	if err := st.refreshView(ctx); err != nil {
		st.tagged(cmd.Tag, "NO", "EXPUNGE failed")
		return nil
	}
	newSet := make(map[uint32]bool, len(st.view.uids))
	for _, u := range st.view.uids {
		newSet[u] = true
	}
	gone := make(map[uint32]bool, len(res.UIDs))
	for _, u := range res.UIDs {
		gone[u] = true
	}
	// Newest-first (RFC 9051 §7.5.1: each successive EXPUNGE number
	// decreases), so every announced seq is valid in the shrinking view.
	for i := len(old) - 1; i >= 0; i-- {
		if gone[old[i]] && !newSet[old[i]] {
			st.untagged(fmt.Sprintf("%d EXPUNGE", i+1))
		}
	}
	st.lastExist = int64(st.view.count())
	st.srv.auditf(AuditExpunge, st.user.ID, st.user.Email, st.remote,
		map[string]any{"folder": f.IMAPPath, "count": res.Expunged})
	st.tagged(cmd.Tag, "OK", "EXPUNGE completed")
	return nil
}

// cmdClose implements CLOSE (RFC 9051 §6.4.2): silently expunges \Deleted
// messages (no untagged EXPUNGE responses) and returns to authenticated.
func (st *session) cmdClose(cmd *commandTokens) error {
	f, ok := st.requireSelection(cmd.Tag)
	if !ok {
		return nil
	}
	if err := st.ms.CanWrite(); err == nil {
		if _, expErr := st.srv.store.Expunge(f.ID, st.user.ID, 0); expErr != nil {
			st.deselect()
			st.tagged(cmd.Tag, "NO", "CLOSE failed: "+expErr.Error())
			return nil
		}
	}
	st.deselect()
	st.tagged(cmd.Tag, "OK", "CLOSE completed")
	return nil
}

// ─── STORE / COPY / MOVE ─────────────────────────────────────────────────────

// cmdStore implements STORE and UID STORE (RFC 9051 §6.4.6): +FLAGS/-FLAGS
// (with .SILENT variants). Every store is CAS against the session's
// last-observed mod_seq (the W1 contract); a concurrent change by another
// session surfaces as tagged NO.
func (st *session) cmdStore(cmd *commandTokens, uidMode bool) error {
	f, ok := st.requireSelection(cmd.Tag)
	if !ok {
		return nil
	}
	if err := st.ms.CanWrite(); err != nil {
		st.tagged(cmd.Tag, "NO", "[READ-ONLY] Session is read-only (EXAMINE)")
		return nil
	}
	setStr, i, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "STORE requires a sequence set and a flag operation")
		return nil
	}
	opAtom, i, err := wantAtom(cmd.Args, i, true)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "STORE requires a flag operation")
		return nil
	}
	silent := false
	op := opAtom
	if base, ok := strings.CutSuffix(op, ".SILENT"); ok {
		silent = true
		op = base
	}
	var setFlag, clearFlag uint32
	switch op {
	case "+FLAGS":
		setFlag = parseFlagList(storeFlagAtoms(cmd.Args, i))
	case "-FLAGS":
		clearFlag = parseFlagList(storeFlagAtoms(cmd.Args, i))
	case "FLAGS":
		// Replace: the final set is exactly the listed flags (plus the
		// server-managed \Recent, which survives — RFC 9051 §2.3.2). The
		// mailstore computes (flags | Set) &^ Clear, so Clear must spare
		// the flags being set.
		setFlag = parseFlagList(storeFlagAtoms(cmd.Args, i))
		clearFlag = (models.FlagSeen | models.FlagAnswered | models.FlagFlagged |
			models.FlagDraft | models.FlagDeleted) &^ setFlag
	default:
		st.tagged(cmd.Tag, "BAD", "Unknown STORE operation "+opAtom)
		return nil
	}
	set, err := mailstore.ParseSeqSet(setStr)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "Invalid sequence set")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Deliberately NOT refreshed here: the STORE's CAS compares against the
	// mod-seq the session last OBSERVED (SELECT/FETCH/event drains). A
	// fresh read would erase the window another session's concurrent store
	// must surface through (the UNCHANGEDSINCE contract).
	if st.view.count() == 0 {
		if err := st.refreshView(ctx); err != nil {
			st.tagged(cmd.Tag, "NO", "STORE failed: view refresh")
			return nil
		}
	}
	targets := st.resolveSet(set, uidMode)
	if len(targets) == 0 {
		if uidMode {
			// RFC 9051: UID commands succeed silently when nothing matches.
			st.tagged(cmd.Tag, "OK", "STORE completed (no matching messages)")
			return nil
		}
		st.tagged(cmd.Tag, "NO", "No matching messages")
		return nil
	}
	for _, t := range targets {
		expected := st.view.modSeqs[t.uid]
		res, err := st.srv.store.SetFlags(st.user.ID, 0, mailstore.FlagsUpdate{
			MessageID:      t.msgID,
			ExpectedModSeq: expected,
			Set:            setFlag,
			Clear:          clearFlag,
		})
		if err != nil {
			if errors.Is(err, mailstore.ErrFlagConflict) {
				// Another session changed the message since it was last
				// observed: surface as tagged NO (UNCHANGEDSINCE semantics).
				st.srv.auditf(AuditStoreConflict, st.user.ID, st.user.Email, st.remote,
					map[string]any{"folder": f.IMAPPath, "uid": t.uid})
				st.tagged(cmd.Tag, "NO", "Flag store conflict: message changed since last seen")
				return nil
			}
			if errors.Is(err, mailstore.ErrMessageNotFound) {
				continue // vanished mid-command
			}
			st.tagged(cmd.Tag, "NO", "STORE failed: "+err.Error())
			return nil
		}
		st.view.modSeqs[t.uid] = res.ModSeq
		if !silent {
			line := fmt.Sprintf("FLAGS (%s)", flagNames(res.Flags))
			if uidMode {
				line = fmt.Sprintf("UID %d %s", t.uid, line)
			}
			st.untagged(fmt.Sprintf("%d FETCH (%s)", st.view.seqOf(t.uid), line))
		}
	}
	st.srv.auditf(AuditStore, st.user.ID, st.user.Email, st.remote,
		map[string]any{"folder": f.IMAPPath, "count": len(targets)})
	st.tagged(cmd.Tag, "OK", "STORE completed")
	return nil
}

// storeFlagAtoms collects flag atoms after the operation token: either the
// parenthesized flag list or bare atoms to end of line.
func storeFlagAtoms(toks []token, i int) []string {
	var atoms []string
	for ; i < len(toks); i++ {
		switch toks[i].Kind {
		case '(', ')':
		case 'a', 's':
			atoms = append(atoms, toks[i].Val)
		}
	}
	return atoms
}

// copyTarget is one resolved message for set-based commands.
type copyTarget struct {
	uid   uint32
	seq   int
	msgID uint
}

// resolveSet maps a parsed sequence set (seq or UID mode) onto live messages
// through the session view.
func (st *session) resolveSet(set mailstore.SeqSet, uidMode bool) []copyTarget {
	out := make([]copyTarget, 0, len(set))
	if uidMode {
		maxUID := uint32(0)
		for _, u := range st.view.uids {
			if u > maxUID {
				maxUID = u
			}
		}
		resolved := set.Resolve(maxUID)
		for _, u := range st.view.uids {
			if resolved.Contains(u) {
				out = append(out, copyTarget{uid: u, seq: st.view.seqOf(u), msgID: st.view.msgIDs[u]})
			}
		}
		return out
	}
	resolved := set.Resolve(uint32(st.view.count()))
	for seq := 1; seq <= st.view.count(); seq++ {
		if resolved.Contains(uint32(seq)) {
			uid := st.view.uids[seq-1]
			out = append(out, copyTarget{uid: uid, seq: seq, msgID: st.view.msgIDs[uid]})
		}
	}
	return out
}

// cmdCopyMove implements COPY and MOVE (RFC 9051 §6.4.7/§6.4.9) plus their
// UID variants: each message lands in the target folder with a fresh UID;
// the target's UIDVALIDITY never changes. MOVE is copy + source expunge.
func (st *session) cmdCopyMove(cmd *commandTokens, uidMode, move bool) error {
	f, ok := st.requireSelection(cmd.Tag)
	if !ok {
		return nil
	}
	if move {
		// MOVE mutates the source folder.
		if err := st.ms.CanWrite(); err != nil {
			st.tagged(cmd.Tag, "NO", "[READ-ONLY] Session is read-only (EXAMINE)")
			return nil
		}
	}
	setStr, i, err := wantString(cmd.Args, 0)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "A sequence set is required")
		return nil
	}
	target, _, err := wantString(cmd.Args, i)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "A target mailbox is required")
		return nil
	}
	set, err := mailstore.ParseSeqSet(setStr)
	if err != nil {
		st.tagged(cmd.Tag, "BAD", "Invalid sequence set")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := st.refreshView(ctx); err != nil {
		st.tagged(cmd.Tag, "NO", "Command failed: view refresh")
		return nil
	}
	mb, err := st.resolveMailbox(ctx)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "No mailbox available for this account")
		return nil
	}
	dst, err := st.srv.store.GetFolderByPath(mb.ID, st.user.ID, 0, target)
	if err != nil {
		st.tagged(cmd.Tag, "NO", "[TRYCREATE] No such mailbox")
		return nil
	}
	targets := st.resolveSet(set, uidMode)
	if len(targets) == 0 {
		if uidMode {
			st.tagged(cmd.Tag, "OK", "Command completed (no matching messages)")
			return nil
		}
		st.tagged(cmd.Tag, "NO", "No matching messages")
		return nil
	}
	verb := "COPY"
	if move {
		verb = "MOVE"
	}
	for _, t := range targets {
		if move {
			if _, err := st.srv.store.MoveMessage(t.msgID, dst.ID, st.user.ID, 0); err != nil {
				st.tagged(cmd.Tag, "NO", verb+" failed: "+err.Error())
				return nil
			}
		} else {
			if _, err := st.srv.store.CopyMessage(t.msgID, dst.ID, st.user.ID, 0); err != nil {
				st.tagged(cmd.Tag, "NO", verb+" failed: "+err.Error())
				return nil
			}
		}
	}
	auditAction := AuditCopy
	if move {
		auditAction = AuditMove
	}
	st.srv.auditf(auditAction, st.user.ID, st.user.Email, st.remote,
		map[string]any{"folder": f.IMAPPath, "target": dst.IMAPPath, "count": len(targets)})
	if move {
		// The source expunges are announced through the pending change
		// events (RFC 9051 §6.4.9: MOVE MUST send EXPUNGE responses).
		st.applyNotifications(false)
	}
	st.tagged(cmd.Tag, "OK", verb+" completed")
	return nil
}
