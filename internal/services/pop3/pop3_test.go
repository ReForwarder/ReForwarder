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
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailauth"
)

// refusedLine is the one generic wire answer every credential failure
// produces (identical across wrong/revoked/missing credentials — asserted
// by the login matrix subtests).
const refusedLine = "-ERR [AUTH] invalid credentials"

// saslPlain encodes an RFC 4616 PLAIN message as base64.
func saslPlain(authzid, authcid, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(authzid + "\x00" + authcid + "\x00" + secret))
}

// unstuffedLines splits raw message bytes into the lines a POP3 client sees
// after un-stuffing (line boundaries \n, terminators trimmed).
func unstuffedLines(raw []byte) []string {
	var out []string
	for pos := 0; pos < len(raw); {
		nl := -1
		for i := pos; i < len(raw); i++ {
			if raw[i] == '\n' {
				nl = i
				break
			}
		}
		var line []byte
		if nl < 0 {
			line = raw[pos:]
			pos = len(raw)
		} else {
			line = raw[pos : nl+1]
			pos = nl + 1
		}
		out = append(out, strings.TrimRight(string(line), "\r\n"))
	}
	return out
}

func containsFold(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// TestPOP3LoginMatrix: the credential matrix on the wire — a valid app
// password authenticates (USER/PASS and both AUTH PLAIN forms), the primary
// account password is REFUSED, a revoked app password is refused, a wrong
// secret is refused, an unknown user is refused — and every refusal carries
// the SAME generic message so the mail port cannot be used to enumerate
// identities or credential states (ADR-004 decision 3, mailauth contract).
func TestPOP3LoginMatrix(t *testing.T) {
	tf := newFixture(t, false)

	t.Run("valid app password via USER PASS", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("USER " + testUserEmail))
		expectOK(t, c.login(testUserEmail, testAppSecret))
		expectOK(t, c.cmd("STAT"))
	})

	t.Run("valid app password via AUTH PLAIN initial response", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		resp := c.cmd("AUTH PLAIN " + saslPlain("", testUserEmail, testAppSecret))
		expectOK(t, resp)
		if !strings.Contains(resp, "3 messages") {
			t.Fatalf("login response should carry the drop stats, got %q", resp)
		}
	})

	t.Run("valid app password via AUTH PLAIN challenge", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		resp := c.cmd("AUTH PLAIN")
		if resp != "+ " {
			t.Fatalf("SASL challenge = %q, want \"+ \"", resp)
		}
		expectOK(t, c.cmd(saslPlain("", testUserEmail, testAppSecret)))
	})

	t.Run("AUTH PLAIN cancelled with star", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		if resp := c.cmd("AUTH PLAIN"); resp != "+ " {
			t.Fatalf("SASL challenge = %q, want \"+ \"", resp)
		}
		expectERR(t, c.cmd("*"))
		// The session must still be usable: a later login succeeds.
		expectOK(t, c.login(testUserEmail, testAppSecret))
	})

	t.Run("primary account password REFUSED", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("USER " + testUserEmail))
		resp := c.cmd("PASS " + testPrimarySecret)
		expectERR(t, resp)
		if resp != refusedLine {
			t.Fatalf("primary-password refusal = %q, want the generic %q", resp, refusedLine)
		}
	})

	t.Run("revoked app password REFUSED while another stays valid", func(t *testing.T) {
		// A dedicated fixture whose active set no longer holds the laptop
		// credential — exactly what FindActiveByUserID returns post-revocation.
		// The presented secret is a REAL digest that authenticated before; it
		// is refused because the revoked row is excluded from the active set.
		tfR := newFixture(t, false)
		tfR.srv.auth = mailauth.NewAuthenticator(
			&fakeUserFinder{users: map[string]*models.User{
				testUserEmail: {ID: testUserID, Email: testUserEmail, Active: true},
			}},
			&fakeAppPasswords{active: map[uint][]models.UserAppPassword{
				testUserID: {
					// Only the phone credential remains active: the laptop
					// one was revoked.
					{ID: 102, UserID: testUserID, Name: "phone", PasswordHash: bcryptHash(t, testPhoneSecret)},
				},
			}},
		)
		c := dialPOP3(t, tfR.addr)
		defer c.close()
		expectOK(t, c.cmd("USER " + testUserEmail))
		if resp := c.cmd("PASS " + testAppSecret); resp != refusedLine {
			t.Fatalf("revoked-password refusal = %q, want %q", resp, refusedLine)
		}
		// The still-active phone credential works — revocation is
		// per-credential, not per-account.
		expectOK(t, c.login(testUserEmail, testPhoneSecret))
	})

	t.Run("wrong secret REFUSED", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("USER " + testUserEmail))
		if resp := c.cmd("PASS totally-wrong-secret"); resp != refusedLine {
			t.Fatalf("wrong-secret refusal = %q, want %q", resp, refusedLine)
		}
	})

	t.Run("unknown user REFUSED with the same generic line", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("USER mallory@example.test"))
		if resp := c.cmd("PASS whatever"); resp != refusedLine {
			t.Fatalf("unknown-user refusal = %q, want %q (no enumeration)", resp, refusedLine)
		}
	})

	t.Run("PASS without USER is refused", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectERR(t, c.cmd("PASS " + testAppSecret))
	})

	t.Run("second login attempt is refused", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.login(testUserEmail, testAppSecret))
		expectERR(t, c.cmd("USER " + testUserEmail))
		expectERR(t, c.cmd("PASS " + testAppSecret))
	})

	t.Run("AUTH after authentication is refused", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.login(testUserEmail, testAppSecret))
		expectERR(t, c.cmd("AUTH PLAIN " + saslPlain("", testUserEmail, testAppSecret)))
	})
}

// TestPOP3AuthRequiresTLS: on a TLS-enforced server (certificate present),
// credentials are refused before the STLS upgrade and accepted after it
// (ADR-004 decision 4: STARTTLS required by default).
func TestPOP3AuthRequiresTLS(t *testing.T) {
	tf := newFixture(t, true)

	t.Run("USER refused before STLS", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectERR(t, c.cmd("USER " + testUserEmail))
	})

	t.Run("AUTH PLAIN refused before STLS", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectERR(t, c.cmd("AUTH PLAIN " + saslPlain("", testUserEmail, testAppSecret)))
	})

	t.Run("login succeeds after STLS", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("STLS"))
		c.upgradeTLS(tf.tlsConf)
		expectOK(t, c.login(testUserEmail, testAppSecret))
		expectOK(t, c.cmd("STAT"))
	})

	t.Run("CAPA before STLS lists STLS but no auth", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("CAPA"))
		caps := c.readMulti()
		if !containsFold(caps, "STLS") {
			t.Fatalf("pre-STLS CAPA must advertise STLS, got %v", caps)
		}
		if containsFold(caps, "USER") || containsFold(caps, "AUTH") {
			t.Fatalf("pre-STLS CAPA must not advertise credentials in the clear, got %v", caps)
		}
	})

	t.Run("data pipelined behind STLS is refused, then a clean STLS works", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		// Both lines in one TCP write: the CAPA bytes are already in the
		// server's plaintext buffer when STLS is processed, so the server
		// must refuse the upgrade rather than fold pre-handshake plaintext
		// into the TLS channel (RFC 2595 §4).
		_, _ = c.conn.Write([]byte("STLS\r\nCAPA\r\n"))
		first := c.readLine()
		expectERR(t, first)
		// The server still processed the buffered CAPA; drain its response.
		expectOK(t, c.readLine())
		c.readMulti()
		// A clean STLS alone is accepted.
		expectOK(t, c.cmd("STLS"))
		c.upgradeTLS(tf.tlsConf)
		expectOK(t, c.login(testUserEmail, testAppSecret))
	})
}

// TestPOP3StatListUidl: STAT/LIST/UIDL correctness — counts and octets from
// the shadow snapshot, UIDL identities equal to the mailstore UIDs and
// stable across sessions, deleted messages excluded, bad message numbers
// rejected.
func TestPOP3StatListUidl(t *testing.T) {
	tf := newFixture(t, false)

	// Snapshot the fixture UIDs (three messages, UIDs 1..3).
	tf.store.mu.Lock()
	var uids []uint32
	for _, m := range tf.box.messages {
		uids = append(uids, m.uid)
	}
	tf.store.mu.Unlock()
	if len(uids) != 3 {
		t.Fatalf("fixture should have 3 messages, has %d", len(uids))
	}

	var wantOctets int64
	for _, uid := range uids {
		wantOctets += int64(len(tf.raws[uid]))
	}

	c := dialPOP3(t, tf.addr)
	defer c.close()
	expectOK(t, c.login(testUserEmail, testAppSecret))

	t.Run("STAT counts and octets", func(t *testing.T) {
		n, octets := parseStats(t, expectOK(t, c.cmd("STAT")))
		if n != 3 || octets != wantOctets {
			t.Fatalf("STAT = %d msgs / %d octets, want 3 / %d (advertised size = stored plaintext size)", n, octets, wantOctets)
		}
	})

	t.Run("LIST all enumerates seq + octets", func(t *testing.T) {
		expectOK(t, c.cmd("LIST"))
		lines := c.readMulti()
		if len(lines) != 3 {
			t.Fatalf("LIST lines = %v", lines)
		}
		for i, uid := range uids {
			want := fmt.Sprintf("%d %d", i+1, len(tf.raws[uid]))
			if lines[i] != want {
				t.Fatalf("LIST line %d = %q, want %q", i+1, lines[i], want)
			}
		}
	})

	t.Run("LIST with a message number", func(t *testing.T) {
		resp := expectOK(t, c.cmd("LIST 2"))
		if resp != fmt.Sprintf("+OK 2 %d", len(tf.raws[uids[1]])) {
			t.Fatalf("LIST 2 = %q", resp)
		}
		expectERR(t, c.cmd("LIST 99"))
		expectERR(t, c.cmd("LIST notanumber"))
	})

	t.Run("UIDL identities are the mailstore UIDs", func(t *testing.T) {
		expectOK(t, c.cmd("UIDL"))
		lines := c.readMulti()
		if len(lines) != 3 {
			t.Fatalf("UIDL lines = %v", lines)
		}
		for i, uid := range uids {
			want := fmt.Sprintf("%d %d", i+1, uid)
			if lines[i] != want {
				t.Fatalf("UIDL line %d = %q, want %q (unique-id = mailstore UID)", i+1, lines[i], want)
			}
		}
		resp := expectOK(t, c.cmd("UIDL 3"))
		if resp != fmt.Sprintf("+OK 3 %d", uids[2]) {
			t.Fatalf("UIDL 3 = %q", resp)
		}
		expectERR(t, c.cmd("UIDL 0"))
		expectERR(t, c.cmd("UIDL 99"))
	})

	t.Run("UIDL values stable across sessions", func(t *testing.T) {
		// A second session must observe the same unique-ids (the mailstore
		// UIDs are monotonic per folder and uid_validity never changes on
		// expunge — the UIDL contract).
		c2 := dialPOP3(t, tf.addr)
		defer c2.close()
		expectOK(t, c2.login(testUserEmail, testAppSecret))
		expectOK(t, c2.cmd("UIDL"))
		got := c2.readMulti()
		for i, uid := range uids {
			want := fmt.Sprintf("%d %d", i+1, uid)
			if got[i] != want {
				t.Fatalf("second-session UIDL line %d = %q, want %q", i+1, got[i], want)
			}
		}
	})

	t.Run("deleted messages vanish from STAT/LIST/UIDL", func(t *testing.T) {
		expectOK(t, c.cmd("DELE 1"))
		if n, _ := parseStats(t, expectOK(t, c.cmd("STAT"))); n != 2 {
			t.Fatalf("STAT after DELE = %d msgs, want 2 (shadow view)", n)
		}
		expectOK(t, c.cmd("LIST"))
		lines := c.readMulti()
		if len(lines) != 2 || strings.HasPrefix(lines[0], "1 ") {
			t.Fatalf("LIST after DELE = %v, want messages 2 and 3 only", lines)
		}
		expectERR(t, c.cmd("LIST 1"))
		expectERR(t, c.cmd("UIDL 1"))
		expectOK(t, c.cmd("RSET")) // restore the view for later subtests
	})
}

// TestPOP3RetrTopByteAccounting: RETR returns exactly the advertised octets
// (byte-exact body from the stored blob), byte-stuffing round-trips
// leading-dot lines, and TOP returns the header block plus the requested
// number of body lines.
func TestPOP3RetrTopByteAccounting(t *testing.T) {
	tf := newFixture(t, false)

	c := dialPOP3(t, tf.addr)
	defer c.close()
	expectOK(t, c.login(testUserEmail, testAppSecret))

	tf.store.mu.Lock()
	uid1 := tf.box.messages[0].uid
	uid2 := tf.box.messages[1].uid
	size2 := tf.box.messages[1].size
	tf.store.mu.Unlock()

	t.Run("RETR byte count equals advertised size", func(t *testing.T) {
		resp := expectOK(t, c.cmd("RETR 2"))
		if resp != fmt.Sprintf("+OK %d octets", size2) {
			t.Fatalf("RETR banner = %q, want the stored size %d", resp, size2)
		}
		got := c.readMulti()
		want := unstuffedLines(tf.raws[uid2])
		if len(got) != len(want) {
			t.Fatalf("RETR returned %d lines, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("RETR line %d = %q, want %q", i+1, got[i], want[i])
			}
		}
	})

	t.Run("RETR of a dot-prefixed body round-trips byte-stuffing", func(t *testing.T) {
		raw := rawMessage("dotted", []string{".leading dot", "normal line", "..double dot"})
		tf.store.addMessage(tf.box, raw)
		tf.store.mu.Lock()
		seq := len(tf.box.messages)
		tf.store.mu.Unlock()
		// The message arrived after the fixture session's shadow snapshot —
		// a fresh login is required to see it (RFC 1939 permits this; new
		// mail is invisible to an open session).
		c2 := dialPOP3(t, tf.addr)
		defer c2.close()
		expectOK(t, c2.login(testUserEmail, testAppSecret))
		resp := expectOK(t, c2.cmd(fmt.Sprintf("RETR %d", seq)))
		if !strings.HasSuffix(resp, fmt.Sprintf("%d octets", len(raw))) {
			t.Fatalf("RETR banner = %q, want %d octets", resp, len(raw))
		}
		got := c2.readMulti()
		want := unstuffedLines(raw)
		if len(got) != len(want) {
			t.Fatalf("stuffed RETR returned %d lines, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("stuffed line %d = %q, want %q (byte-stuffing must round-trip)", i+1, got[i], want[i])
			}
		}
	})

	t.Run("TOP returns headers plus N body lines", func(t *testing.T) {
		resp := expectOK(t, c.cmd("TOP 1 1"))
		if !strings.HasPrefix(resp, "+OK") {
			t.Fatalf("TOP banner = %q", resp)
		}
		lines := c.readMulti()
		headers, _ := splitHeaderBody(tf.raws[uid1])
		wantHeaders := unstuffedLines(headers)
		if len(lines) != len(wantHeaders)+1 {
			t.Fatalf("TOP 1 returned %d lines (headers %d + 1 body line), got %v", len(wantHeaders)+1, len(wantHeaders), lines)
		}
		for i := range wantHeaders {
			if lines[i] != wantHeaders[i] {
				t.Fatalf("TOP header line %d = %q, want %q", i+1, lines[i], wantHeaders[i])
			}
		}
		if lines[len(wantHeaders)] != "body of alpha" {
			t.Fatalf("first body line = %q, want %q", lines[len(wantHeaders)], "body of alpha")
		}
	})

	t.Run("TOP 0 returns headers only", func(t *testing.T) {
		expectOK(t, c.cmd("TOP 1 0"))
		lines := c.readMulti()
		headers, _ := splitHeaderBody(tf.raws[uid1])
		wantHeaders := unstuffedLines(headers)
		if len(lines) != len(wantHeaders) {
			t.Fatalf("TOP 0 returned %d lines, want %d (headers only)", len(lines), len(wantHeaders))
		}
	})

	t.Run("TOP and RETR reject bad requests", func(t *testing.T) {
		expectERR(t, c.cmd("TOP 99 5"))
		expectERR(t, c.cmd("TOP 1"))
		expectERR(t, c.cmd("TOP 1 -1"))
		expectERR(t, c.cmd("RETR 99"))
		expectERR(t, c.cmd("RETR abc"))
	})
}

// TestPOP3DeleQuitExpunges: DELE marks in the shadow, QUIT runs the UPDATE
// phase — the marked message is expunged from the mailstore, the others
// survive with their UIDs, and uid_validity never changes (the UIDL
// contract; ADR-004 decision 2).
func TestPOP3DeleQuitExpunges(t *testing.T) {
	tf := newFixture(t, false)

	c := dialPOP3(t, tf.addr)
	defer c.close()
	expectOK(t, c.login(testUserEmail, testAppSecret))

	tf.store.mu.Lock()
	uid1, uid2, uid3 := tf.box.messages[0].uid, tf.box.messages[1].uid, tf.box.messages[2].uid
	uidValid := tf.box.uidValid
	tf.store.mu.Unlock()

	// DELE 1: shadow only — the mailstore row still exists.
	expectOK(t, c.cmd("DELE 1"))
	if !tf.store.inBox(tf.box, uid1) {
		t.Fatalf("DELE must not touch the mailstore before QUIT (shadow view)")
	}

	// QUIT: UPDATE phase → expunge via the mailstore.
	expectOK(t, c.cmd("QUIT"))

	if tf.store.inBox(tf.box, uid1) {
		t.Fatalf("QUIT must expunge the shadow-marked message (UID %d still present)", uid1)
	}
	if !tf.store.inBox(tf.box, uid2) || !tf.store.inBox(tf.box, uid3) {
		t.Fatalf("survivors must keep their messages (UIDs %d, %d)", uid2, uid3)
	}
	tf.store.mu.Lock()
	validNow := tf.box.uidValid
	uidNextNow := tf.box.uidNext
	tf.store.mu.Unlock()
	if validNow != uidValid {
		t.Fatalf("uid_validity changed on expunge (%d → %d); UIDL identities would break", uidValid, validNow)
	}
	if uidNextNow != uid3+1 {
		t.Fatalf("uid_next rewound (%d, want %d) — UIDs are never reused", uidNextNow, uid3+1)
	}

	// The next session sees two messages and the same UIDL identities.
	c2 := dialPOP3(t, tf.addr)
	defer c2.close()
	expectOK(t, c2.login(testUserEmail, testAppSecret))
	if n, _ := parseStats(t, expectOK(t, c2.cmd("STAT"))); n != 2 {
		t.Fatalf("STAT after expunge = %d, want 2", n)
	}
	expectOK(t, c2.cmd("UIDL"))
	lines := c2.readMulti()
	want := []string{fmt.Sprintf("1 %d", uid2), fmt.Sprintf("2 %d", uid3)}
	if len(lines) != len(want) {
		t.Fatalf("UIDL after expunge = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("UIDL line %d = %q, want %q (survivors keep their UIDs)", i+1, lines[i], want[i])
		}
	}
}

// TestPOP3RsetRollback: DELE then RSET clears every in-session mark — QUIT
// expunges nothing and the maildrop is unchanged.
func TestPOP3RsetRollback(t *testing.T) {
	tf := newFixture(t, false)

	tf.store.mu.Lock()
	uids := []uint32{tf.box.messages[0].uid, tf.box.messages[1].uid, tf.box.messages[2].uid}
	tf.store.mu.Unlock()

	c := dialPOP3(t, tf.addr)
	defer c.close()
	expectOK(t, c.login(testUserEmail, testAppSecret))

	expectOK(t, c.cmd("DELE 1"))
	expectOK(t, c.cmd("DELE 3"))
	if n, _ := parseStats(t, expectOK(t, c.cmd("STAT"))); n != 1 {
		t.Fatalf("STAT after two DELEs = %d, want 1", n)
	}
	resp := expectOK(t, c.cmd("RSET"))
	if !strings.Contains(resp, "3 messages") {
		t.Fatalf("RSET response = %q, want the full maildrop restored", resp)
	}
	if n, _ := parseStats(t, expectOK(t, c.cmd("STAT"))); n != 3 {
		t.Fatalf("STAT after RSET = %d, want 3", n)
	}
	expectOK(t, c.cmd("QUIT"))

	for _, uid := range uids {
		if !tf.store.inBox(tf.box, uid) {
			t.Fatalf("RSET rollback failed: UID %d was expunged", uid)
		}
	}
}

// TestPOP3SessionScopedDeletion: deletion is scoped to the session's shadow
// view — a concurrent session sees the untouched maildrop and can retrieve
// the shadow-marked message; the store is never mutated mid-session; and a
// session that drops WITHOUT QUIT expunges nothing (mark-then-expunge, RFC
// 1939 §6).
func TestPOP3SessionScopedDeletion(t *testing.T) {
	tf := newFixture(t, false)

	tf.store.mu.Lock()
	uid1, uid2, uid3 := tf.box.messages[0].uid, tf.box.messages[1].uid, tf.box.messages[2].uid
	tf.store.mu.Unlock()

	// Session A marks message 1 and stays open.
	a := dialPOP3(t, tf.addr)
	defer a.close()
	expectOK(t, a.login(testUserEmail, testAppSecret))
	expectOK(t, a.cmd("DELE 1"))

	// Session B, concurrent, still sees the FULL maildrop — message 1 is
	// retrievable and no counter moved (the mailstore was never touched).
	b := dialPOP3(t, tf.addr)
	defer b.close()
	expectOK(t, b.login(testUserEmail, testAppSecret))
	if n, _ := parseStats(t, expectOK(t, b.cmd("STAT"))); n != 3 {
		t.Fatalf("concurrent session STAT = %d msgs, want 3 — a shadow DELE must not leak", n)
	}
	expectOK(t, b.cmd("UIDL"))
	if uidl := b.readMulti(); len(uidl) != 3 {
		t.Fatalf("concurrent UIDL = %v, want 3 entries", uidl)
	}
	expectOK(t, b.cmd("RETR 1"))
	body := b.readMulti()
	if len(body) == 0 || body[len(body)-1] != "body of alpha" {
		t.Fatalf("concurrent RETR body = %v", body)
	}

	// Session A quits: the sweep removes ONLY what A marked.
	expectOK(t, a.cmd("QUIT"))
	if tf.store.inBox(tf.box, uid1) {
		t.Fatal("A's marked message should be expunged after A's QUIT")
	}
	if !tf.store.inBox(tf.box, uid2) || !tf.store.inBox(tf.box, uid3) {
		t.Fatal("B's messages must survive A's expunge")
	}

	// Session B (still open) keeps its own shadow view: numbers never shift
	// under an open session.
	if n, _ := parseStats(t, expectOK(t, b.cmd("STAT"))); n != 3 {
		t.Fatalf("open session STAT = %d, want 3 (the shadow view is stable for the session)", n)
	}

	// Abandonment without QUIT: session C marks a message and the connection
	// is cut — nothing may be expunged.
	c3 := dialPOP3(t, tf.addr)
	defer c3.close()
	expectOK(t, c3.login(testUserEmail, testAppSecret))
	expectOK(t, c3.cmd("DELE 1")) // C's shadow seq 1 = uid2 now
	c3.conn.Close()
	waitFor(t, 2*time.Second, func() bool {
		return tf.audit.countOf(auditEventSessionEnd) >= 1
	})
	if !tf.store.inBox(tf.box, uid2) {
		t.Fatal("a session that drops without QUIT must expunge nothing")
	}
	if !tf.store.inBox(tf.box, uid3) {
		t.Fatal("unrelated message must survive the abandoned session")
	}
}

// TestPOP3CapaBeforeAfterStls: CAPA reflects the security state — STLS is
// advertised only before the upgrade, credentials only when a security layer
// is in place (RFC 2595 §4; RFC 5034 §7.2).
func TestPOP3CapaBeforeAfterStls(t *testing.T) {
	tf := newFixture(t, true)

	t.Run("before STLS", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("CAPA"))
		caps := c.readMulti()
		if !containsFold(caps, "STLS") {
			t.Fatalf("CAPA before STLS must advertise STLS: %v", caps)
		}
		if containsFold(caps, "AUTH") || containsFold(caps, "USER") {
			t.Fatalf("CAPA before STLS must not advertise auth: %v", caps)
		}
		if !containsFold(caps, "UIDL") || !containsFold(caps, "TOP") || !containsFold(caps, "PIPELINING") {
			t.Fatalf("CAPA must advertise the command set: %v", caps)
		}
	})

	t.Run("after STLS", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("STLS"))
		c.upgradeTLS(tf.tlsConf)
		expectOK(t, c.cmd("CAPA"))
		caps := c.readMulti()
		if containsFold(caps, "STLS") {
			t.Fatalf("CAPA after STLS must NOT advertise STLS (RFC 2595 §4): %v", caps)
		}
		if !containsFold(caps, "AUTH PLAIN") || !containsFold(caps, "USER") {
			t.Fatalf("CAPA after STLS must advertise credentials: %v", caps)
		}
	})

	t.Run("second STLS refused", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("STLS"))
		c.upgradeTLS(tf.tlsConf)
		expectERR(t, c.cmd("STLS"))
	})

	t.Run("STLS after authentication refused", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("STLS"))
		c.upgradeTLS(tf.tlsConf)
		expectOK(t, c.login(testUserEmail, testAppSecret))
		expectERR(t, c.cmd("STLS"))
	})

	t.Run("STLS without TLS configured refused", func(t *testing.T) {
		tf2 := newFixture(t, false) // cleartext-only (loopback) server
		c := dialPOP3(t, tf2.addr)
		defer c.close()
		expectERR(t, c.cmd("STLS"))
		// Auth still works (loopback cleartext posture).
		expectOK(t, c.login(testUserEmail, testAppSecret))
	})
}

// TestPOP3ConnectionAndCommandLimits: the ADR-004 decision-5 non-negotiables
// — per-user concurrent-session cap (with slot release), per-IP concurrent
// connection cap, and the per-session command-rate budget.
func TestPOP3ConnectionAndCommandLimits(t *testing.T) {
	t.Run("per-user concurrent session cap", func(t *testing.T) {
		tf := newFixture(t, false, func(cfg *Config) { cfg.MaxSessionsPerUser = 2 })

		c1 := dialPOP3(t, tf.addr)
		defer c1.close()
		expectOK(t, c1.login(testUserEmail, testAppSecret))

		c2 := dialPOP3(t, tf.addr)
		defer c2.close()
		expectOK(t, c2.login(testUserEmail, testAppSecret))

		c3 := dialPOP3(t, tf.addr)
		defer c3.close()
		expectOK(t, c3.cmd("USER " + testUserEmail))
		resp := c3.cmd("PASS " + testAppSecret)
		expectERR(t, resp)
		if !strings.Contains(resp, "too many sessions") {
			t.Fatalf("session-cap refusal = %q", resp)
		}

		// The cap frees on disconnect: close c1, wait for the release, then
		// a new login succeeds.
		c1.close()
		waitFor(t, 2*time.Second, func() bool {
			tf.srv.sessionCaps.mu.Lock()
			defer tf.srv.sessionCaps.mu.Unlock()
			return tf.srv.sessionCaps.active[testUserID] == 1
		})
		c4 := dialPOP3(t, tf.addr)
		defer c4.close()
		expectOK(t, c4.login(testUserEmail, testAppSecret))
	})

	t.Run("per-IP concurrent connection cap", func(t *testing.T) {
		tf := newFixture(t, false, func(cfg *Config) { cfg.MaxConnsPerIP = 2 })

		// Two connections are admitted (each reads its greeting — proof the
		// accept loop counted them).
		c1 := dialPOP3(t, tf.addr)
		defer c1.close()
		c2 := dialPOP3(t, tf.addr)
		defer c2.close()

		// The third is refused with the POP3 error greeting.
		conn3, err := net.DialTimeout("tcp", tf.addr, 3*time.Second)
		if err != nil {
			t.Fatalf("conn3 dial: %v", err)
		}
		defer conn3.Close()
		_ = conn3.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 256)
		n, _ := conn3.Read(buf)
		refusal := strings.TrimSpace(string(buf[:n]))
		if !strings.HasPrefix(refusal, "-ERR") || !strings.Contains(refusal, "too many connections") {
			t.Fatalf("IP-cap refusal greeting = %q", refusal)
		}
	})

	t.Run("per-session command-rate budget", func(t *testing.T) {
		tf := newFixture(t, false, func(cfg *Config) {
			cfg.CommandRateLimit = 5
			cfg.CommandRateWindow = 60 * time.Second
		})
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.login(testUserEmail, testAppSecret))

		// The greeting and login consumed budget; NOOPs beyond the budget
		// are rejected with -ERR, and the session closes after the
		// consecutive-rejection cap.
		sawRejection := false
		for i := 0; i < 12; i++ {
			resp := c.cmd("NOOP")
			if strings.HasPrefix(resp, "-ERR") {
				sawRejection = true
				if !strings.Contains(resp, "too many commands") {
					t.Fatalf("rate-limit rejection = %q", resp)
				}
				break
			}
		}
		if !sawRejection {
			t.Fatal("command rate limit never engaged within 12 commands")
		}
	})
}

// TestPOP3AuditEvents: every session publishes audit events — auth success
// and failure (with the server-side reason), session end with the UPDATE
// tally (ADR-004 decision 6).
func TestPOP3AuditEvents(t *testing.T) {
	tf := newFixture(t, false)

	t.Run("auth success + session end on a clean quit", func(t *testing.T) {
		base := tf.audit.countOf(auditEventSessionEnd)
		c := dialPOP3(t, tf.addr)
		expectOK(t, c.login(testUserEmail, testAppSecret))
		expectOK(t, c.cmd("DELE 1"))
		expectOK(t, c.cmd("QUIT"))
		c.close()

		succ := tf.audit.of(auditEventAuthSuccess)
		if len(succ) == 0 {
			t.Fatalf("no auth.success event recorded")
		}
		last := succ[len(succ)-1]
		if last.userID != testUserID || last.email != testUserEmail {
			t.Fatalf("auth.success record = %+v", last)
		}
		waitFor(t, 2*time.Second, func() bool {
			return tf.audit.countOf(auditEventSessionEnd) == base+1
		})
		end := tf.audit.of(auditEventSessionEnd)[base]
		if end.meta["deleted"] != 1 || end.meta["expunged"] != 1 {
			t.Fatalf("session.end tally = %v, want deleted=1 expunged=1", end.meta)
		}
		if end.meta["quit_clean"] != true {
			t.Fatalf("session.end quit_clean = %v", end.meta)
		}
	})

	t.Run("auth failure records the server-side reason", func(t *testing.T) {
		base := tf.audit.countOf(auditEventAuthFailure)
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("USER " + testUserEmail))
		expectERR(t, c.cmd("PASS wrong"))

		waitFor(t, 2*time.Second, func() bool {
			return tf.audit.countOf(auditEventAuthFailure) == base+1
		})
		f := tf.audit.of(auditEventAuthFailure)[base]
		if f.meta["reason"] != "invalid_credentials" {
			t.Fatalf("failure reason = %v", f.meta)
		}
		if f.meta["email"] != testUserEmail {
			t.Fatalf("failure record = %+v", f)
		}
	})

	t.Run("session end on an aborted session (no QUIT)", func(t *testing.T) {
		base := tf.audit.countOf(auditEventSessionEnd)
		c := dialPOP3(t, tf.addr)
		expectOK(t, c.login(testUserEmail, testAppSecret))
		c.conn.Close()
		waitFor(t, 2*time.Second, func() bool {
			return tf.audit.countOf(auditEventSessionEnd) == base+1
		})
	})

	t.Run("QUIT still answers +OK when the store fails; audit reports not clean", func(t *testing.T) {
		base := tf.audit.countOf(auditEventSessionEnd)
		tf.store.mu.Lock()
		tf.store.failNext = true
		tf.store.mu.Unlock()
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.login(testUserEmail, testAppSecret))
		expectOK(t, c.cmd("DELE 2"))
		// RFC 1939 §6 defines no error response for QUIT — the server
		// answers +OK and records the failure server-side.
		expectOK(t, c.cmd("QUIT"))
		waitFor(t, 2*time.Second, func() bool {
			return tf.audit.countOf(auditEventSessionEnd) == base+1
		})
		end := tf.audit.of(auditEventSessionEnd)[base]
		if end.meta["quit_clean"] != false {
			t.Fatalf("quit_clean = %v, want false after a store failure", end.meta)
		}
	})
}

// TestPOP3ProtocolBasics: state-machine edges — unknown commands, commands
// in the wrong state, QUIT in the AUTHORIZATION state (no expunge), and the
// overlong-line guard.
func TestPOP3ProtocolBasics(t *testing.T) {
	tf := newFixture(t, false)

	t.Run("unknown command", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectERR(t, c.cmd("FROB 1"))
	})

	t.Run("transaction commands before login", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		for _, cmd := range []string{"STAT", "LIST", "UIDL", "DELE 1", "RSET"} {
			resp := c.cmd(cmd)
			expectERR(t, resp)
			if !strings.Contains(resp, "AUTHORIZATION") {
				t.Fatalf("%s pre-auth response = %q, want a state error", cmd, resp)
			}
		}
	})

	t.Run("NOOP and case-insensitive verbs", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("NOOP"))
		expectOK(t, c.cmd("noop"))
		expectOK(t, c.cmd("UsEr " + testUserEmail))
		expectOK(t, c.cmd("PASS " + testAppSecret))
		expectOK(t, c.cmd("stat"))
	})

	t.Run("QUIT in AUTHORIZATION state expunges nothing", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		expectOK(t, c.cmd("QUIT"))
		tf.store.mu.Lock()
		n := len(tf.box.messages)
		tf.store.mu.Unlock()
		if n != 3 {
			t.Fatalf("QUIT pre-auth expunged something (%d messages left, want 3)", n)
		}
	})

	t.Run("overlong command line closes the session", func(t *testing.T) {
		c := dialPOP3(t, tf.addr)
		defer c.close()
		_, _ = c.conn.Write([]byte("NOOP " + strings.Repeat("A", 8192) + "\r\n"))
		_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 128)
		n, err := c.conn.Read(buf)
		// The server closes the socket (or answers -ERR); it must not
		// accept the junk line as a command.
		if err == nil && strings.HasPrefix(strings.TrimSpace(string(buf[:n])), "+OK") {
			t.Fatal("overlong command line was accepted")
		}
	})
}

// TestPOP3ImplicitTLS: the port-995 shape — an implicit-TLS listener whose
// accepted connections arrive already handshaked; sessions on it start
// secured (no STLS advertised, auth allowed immediately).
func TestPOP3ImplicitTLS(t *testing.T) {
	tf := newFixture(t, true)
	tf.withImplicitTLS(t)

	conn, err := tls.Dial("tcp", tf.tlsAddr, &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer conn.Close()
	c := &pop3Client{t: t, conn: conn, r: bufio.NewReaderSize(conn, 4096)}
	if greeting := c.readLine(); !strings.HasPrefix(greeting, "+OK") {
		t.Fatalf("implicit-TLS greeting = %q", greeting)
	}

	expectOK(t, c.cmd("CAPA"))
	caps := c.readMulti()
	if containsFold(caps, "STLS") {
		t.Fatalf("implicit-TLS session must not advertise STLS: %v", caps)
	}
	if !containsFold(caps, "AUTH PLAIN") {
		t.Fatalf("implicit-TLS session must advertise credentials: %v", caps)
	}
	expectOK(t, c.login(testUserEmail, testAppSecret))
	expectOK(t, c.cmd("STAT"))

	// STLS on an already-secured connection is refused.
	expectERR(t, c.cmd("STLS"))
}

// waitFor polls cond until it holds or the deadline passes (synchronization
// for events the server processes on its own goroutines).
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within the deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
