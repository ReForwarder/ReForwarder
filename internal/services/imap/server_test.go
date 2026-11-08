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
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Command-matrix tests (the ADR-004 command list). Every test name is the
// matrix cell it pins. All run through the scripted-client harness
// (harness_test.go) over in-process TCP against real Postgres
// (TEST_DATABASE_DSN — skipped when unset).

func simpleRawMessage(messageID, subject, from, to, body string) string {
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%s>\r\nDate: Sat, 07 Nov 2026 10:00:00 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", from, to, subject, messageID, body)
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// TestHarnessSessionLifecycleGreetingCapabilityLogout — the state machine's
// happy path: greeting, CAPABILITY, NOOP, LOGOUT.
func TestHarnessSessionLifecycleGreetingCapabilityLogout(t *testing.T) {
	env := newIMAPEnv(t)
	c := dialEnv(t, env.addr)
	greet := c.greeting()
	if !strings.Contains(greet, "IMAP4rev1") {
		t.Fatalf("greeting must advertise IMAP4rev1: %q", greet)
	}
	untagged, tagged := c.cmd("a1", "CAPABILITY")
	if !strings.Contains(tagged, "OK") {
		t.Fatalf("CAPABILITY: %q", tagged)
	}
	caps := strings.Join(untagged, "\n")
	if !strings.Contains(caps, "* CAPABILITY IMAP4rev1") {
		t.Fatalf("capability line missing: %v", untagged)
	}
	if strings.Contains(caps, "AUTH=PLAIN") {
		t.Fatalf("AUTH=PLAIN must be absent with TLS enforcement on (pre-STARTTLS, non-opted cleartext): %v", untagged)
	}
	if !strings.Contains(caps, "LOGINDISABLED") {
		t.Fatalf("LOGINDISABLED must be advertised when cleartext auth is refused: %v", untagged)
	}
	u, tagged := c.cmd("a2", "NOOP")
	if !strings.HasPrefix(tagged, "a2 OK") {
		t.Fatalf("NOOP: %q %v", tagged, u)
	}
	u, tagged = c.cmd("a3", "LOGOUT")
	if !strings.HasPrefix(tagged, "a3 OK") {
		t.Fatalf("LOGOUT: %q %v", tagged, u)
	}
	bye := untaggedByPrefix(u, "* BYE")
	if len(bye) == 0 {
		t.Fatalf("LOGOUT must send * BYE first, got %v", u)
	}
}

// TestHarnessUnauthenticatedCommandRefusal — selected-state commands refuse
// before authentication.
func TestHarnessUnauthenticatedCommandRefusal(t *testing.T) {
	env := newIMAPEnv(t)
	c := dialEnv(t, env.addr)
	c.greeting()
	tagged := c.expectBAD("b1", "SELECT INBOX")
	if !strings.Contains(tagged, "not valid") {
		t.Fatalf("refusal must name the state: %q", tagged)
	}
	c.expectBAD("b2", "FETCH 1:* (UID)")
	c.expectBAD("b3", "STORE 1 +FLAGS (\\Seen)")
	c.expectBAD("b4", "EXPUNGE")
	c.expectBAD("b5", "LIST %q %q", "", "*")
	c.expectBAD("b6", "STATUS INBOX (MESSAGES)")
	c.expectBAD("b7", "APPEND INBOX")
}

// TestHarnessLoginAppPasswordOnly — the non-negotiable: app passwords
// authenticate; the account's primary password never does.
func TestHarnessLoginAppPasswordOnly(t *testing.T) {
	cfg := imapConfigDefaults()
	cfg.AllowCleartextAuth = true // loopback peer, explicit test opt-in
	env := newIMAPEnvWith(t, cfg)
	c := dialEnv(t, env.addr)
	c.greeting()
	_, tagged := c.cmd("l1", "LOGIN %q %q", env.user.Email, env.appPass)
	if !strings.HasPrefix(tagged, "l1 OK") {
		t.Fatalf("app-password login failed: %q", tagged)
	}
	if env.audits.count(AuditAuthSuccess) != 1 {
		t.Fatalf("auth success must be audited (ADR-004 decision 6), events: %+v", env.audits.events)
	}
	c.expectOK("l2", "LOGOUT")

	// Primary account password: refused (structurally — bcrypt runs only
	// against app-password digests, internal/services/mailauth/auth.go).
	c2 := dialEnv(t, env.addr)
	c2.greeting()
	_, tagged = c2.expectNO("l3", "LOGIN %q %q", env.user.Email, "primary-password-123")
	if !strings.Contains(tagged, "AUTHENTICATIONFAILED") {
		t.Fatalf("primary-password rejection must carry AUTHENTICATIONFAILED: %q", tagged)
	}
	if env.audits.count(AuditAuthFailure) < 1 {
		t.Fatalf("auth failure must be audited")
	}
	_, tagged = c2.expectNO("l4", "LOGIN %q %q", env.user.Email, "totally-wrong")
	if !strings.Contains(tagged, "AUTHENTICATIONFAILED") {
		t.Fatalf("wrong secret must carry AUTHENTICATIONFAILED: %q", tagged)
	}
	_, tagged = c2.expectNO("l5", "LOGIN %q %q", "nobody@imap.test", env.appPass)
	if !strings.Contains(tagged, "AUTHENTICATIONFAILED") {
		t.Fatalf("unknown user must read as a bad credential: %q", tagged)
	}
}

// TestHarnessAuthenticatePlainAndLoginMechanisms — AUTHENTICATE PLAIN (initial
// response + continuation) and AUTHENTICATE LOGIN, both via the app-password
// seam; "*" cancels; bad credentials answer NO.
func TestHarnessAuthenticatePlainAndLoginMechanisms(t *testing.T) {
	cfg := imapConfigDefaults()
	cfg.AllowCleartextAuth = true
	env := newIMAPEnvWith(t, cfg)

	c := dialEnv(t, env.addr)
	c.greeting()
	ir := b64("\x00" + env.user.Email + "\x00" + env.appPass)
	_, tagged := c.cmd("p1", "AUTHENTICATE PLAIN %s", ir)
	if !strings.HasPrefix(tagged, "p1 OK") {
		t.Fatalf("AUTHENTICATE PLAIN (initial response): %q", tagged)
	}
	if !strings.Contains(tagged, "CAPABILITY") {
		t.Fatalf("auth success must re-advertise CAPABILITY: %q", tagged)
	}

	// PLAIN with continuation.
	c2 := dialEnv(t, env.addr)
	c2.greeting()
	c2.send("p2 AUTHENTICATE PLAIN")
	if line := c2.line(); !strings.HasPrefix(line, "+") {
		t.Fatalf("expected continuation request, got %q", line)
	}
	c2.send("%s", b64("\x00"+env.user.Email+"\x00"+env.appPass))
	tagged = c2.waitForTagged("p2")
	if !strings.HasPrefix(tagged, "p2 OK") {
		t.Fatalf("AUTHENTICATE PLAIN (continuation): %q", tagged)
	}

	// LOGIN mechanism: two base64 continuations.
	c3 := dialEnv(t, env.addr)
	c3.greeting()
	c3.send("p3 AUTHENTICATE LOGIN")
	if l := c3.line(); !strings.HasPrefix(l, "+") {
		t.Fatalf("expected username prompt, got %q", l)
	}
	c3.send("%s", b64(env.user.Email))
	if l := c3.line(); !strings.HasPrefix(l, "+") {
		t.Fatalf("expected password prompt, got %q", l)
	}
	c3.send("%s", b64(env.appPass))
	tagged = c3.waitForTagged("p3")
	if !strings.HasPrefix(tagged, "p3 OK") {
		t.Fatalf("AUTHENTICATE LOGIN: %q", tagged)
	}

	// Cancel with *.
	c4 := dialEnv(t, env.addr)
	c4.greeting()
	c4.send("p4 AUTHENTICATE PLAIN")
	c4.line() // continuation
	c4.send("*")
	tagged = c4.waitForTagged("p4")
	if !strings.HasPrefix(tagged, "p4 BAD") {
		t.Fatalf("cancelled AUTHENTICATE must answer BAD: %q", tagged)
	}

	// Wrong secret via PLAIN: NO.
	c5 := dialEnv(t, env.addr)
	c5.greeting()
	c5.send("p5 AUTHENTICATE PLAIN")
	c5.line()
	c5.send("%s", b64("\x00"+env.user.Email+"\x00"+"wrong-secret"))
	tagged = c5.waitForTagged("p5")
	if !strings.HasPrefix(tagged, "p5 NO") {
		t.Fatalf("bad PLAIN credentials must answer NO: %q", tagged)
	}
}

// waitForTagged reads lines until the tag's tagged response appears.
func (c *imapClient) waitForTagged(tag string) string {
	c.t.Helper()
	for {
		line := c.readFullResponse()
		if strings.HasPrefix(line, tag+" ") {
			return line
		}
	}
}

// TestHarnessCleartextAuthRefusedWithoutOptIn — the STARTTLS-enforcement
// half that needs no certificate: with TLS enforcement on (the default),
// authentication commands answer NO before STARTTLS.
func TestHarnessCleartextAuthRefusedWithoutOptIn(t *testing.T) {
	env := newIMAPEnv(t)
	c := dialEnv(t, env.addr)
	c.greeting()
	_, tagged := c.expectNO("t1", "LOGIN %q %q", env.user.Email, env.appPass)
	if !strings.Contains(tagged, "PRIVACYREQUIRED") {
		t.Fatalf("cleartext refusal must carry PRIVACYREQUIRED: %q", tagged)
	}
	_, tagged = c.expectNO("t2", "AUTHENTICATE PLAIN %s", b64("\x00"+env.user.Email+"\x00"+env.appPass))
	if !strings.Contains(tagged, "PRIVACYREQUIRED") {
		t.Fatalf("AUTHENTICATE cleartext refusal must carry PRIVACYREQUIRED: %q", tagged)
	}
	if env.audits.count(AuditAuthSuccess) != 0 {
		t.Fatalf("refused cleartext auth must not authenticate")
	}
}

// TestHarnessStarttlsUpgradeFlow — full STARTTLS: refuse pre-auth without a
// certificate, upgrade with one, and verify capability/state after TLS.
func TestHarnessStarttlsUpgradeFlow(t *testing.T) {
	env := newIMAPEnv(t)
	c := dialEnv(t, env.addr)
	c.greeting()

	_, tagged := c.expectNO("s1", "STARTTLS")
	if !strings.Contains(tagged, "TLS not available") {
		t.Fatalf("STARTTLS without cert must be refused: %q", tagged)
	}

	cert := selfSignedCert(t)
	env.server.SetTLSConfig(&tls.Config{Certificates: []tls.Certificate{cert}})
	c2 := dialEnv(t, env.addr)
	c2.greeting()
	u, tagged := c2.cmd("s2", "CAPABILITY")
	if !strings.Contains(strings.Join(u, "\n"), "STARTTLS") {
		t.Fatalf("STARTTLS must be advertised with a certificate: %v", u)
	}
	_, tagged = c2.cmd("s3", "STARTTLS")
	if !strings.HasPrefix(tagged, "s3 OK") {
		t.Fatalf("STARTTLS: %q", tagged)
	}
	c2.starttls()

	u, tagged = c2.cmd("s4", "CAPABILITY")
	caps := strings.Join(u, "\n")
	if strings.Contains(caps, "STARTTLS") {
		t.Fatalf("STARTTLS must disappear after the upgrade: %v", u)
	}
	if strings.Contains(caps, "LOGINDISABLED") {
		t.Fatalf("LOGINDISABLED must disappear after the upgrade: %v", u)
	}
	if !strings.Contains(caps, "AUTH=PLAIN") {
		t.Fatalf("AUTH=PLAIN must be advertised after the upgrade: %v", u)
	}
	_, tagged = c2.cmd("s5", "LOGIN %q %q", env.user.Email, env.appPass)
	if !strings.HasPrefix(tagged, "s5 OK") {
		t.Fatalf("login after STARTTLS: %q", tagged)
	}
	_, tagged = c2.cmd("s6", "STARTTLS")
	if !strings.HasPrefix(tagged, "s6 BAD") {
		t.Fatalf("second STARTTLS must be BAD: %q", tagged)
	}
}

// TestHarnessSelectExamineSemantics — the READ-WRITE / READ-ONLY contract and
// the SELECT banner.
func TestHarnessSelectExamineSemantics(t *testing.T) {
	env := newIMAPEnv(t)
	env.appendMessage("one", "a@t.test", simpleRawMessage("m1@t.test", "one", "a@t.test", "b@t.test", "body one"))
	env.appendMessage("two", "a@t.test", simpleRawMessage("m2@t.test", "two", "a@t.test", "b@t.test", "body two"))

	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)

	_, tagged := c.expectNO("m1", "SELECT %q", "NoSuchFolder")
	if !strings.Contains(tagged, "No such mailbox") {
		t.Fatalf("missing SELECT: %q", tagged)
	}

	u, tagged := c.expectOK("m2", "SELECT INBOX")
	if !strings.Contains(tagged, "[READ-WRITE]") {
		t.Fatalf("SELECT must answer [READ-WRITE]: %q", tagged)
	}
	if got := untaggedByPrefix(u, "* 2 EXISTS"); len(got) != 1 {
		t.Fatalf("EXISTS must report 2, got %v", u)
	}
	if got := untaggedByPrefix(u, "* OK [UIDVALIDITY"); len(got) != 1 {
		t.Fatalf("UIDVALIDITY banner missing: %v", got)
	}
	if got := untaggedByPrefix(u, "* OK [UIDNEXT"); len(got) != 1 {
		t.Fatalf("UIDNEXT banner missing: %v", got)
	}
	if got := untaggedByPrefix(u, "* FLAGS"); len(got) != 1 || !strings.Contains(got[0], "\\Seen") {
		t.Fatalf("FLAGS banner missing: %v", got)
	}
	if got := untaggedByPrefix(u, "* OK [PERMANENTFLAGS"); len(got) != 1 {
		t.Fatalf("PERMANENTFLAGS banner missing: %v", got)
	}

	u, tagged = c.expectOK("m3", "EXAMINE INBOX")
	if !strings.Contains(tagged, "[READ-ONLY]") {
		t.Fatalf("EXAMINE must answer [READ-ONLY]: %q", tagged)
	}
	_, tagged = c.expectNO("m4", "STORE 1 +FLAGS (\\Seen)")
	if !strings.Contains(tagged, "READ-ONLY") {
		t.Fatalf("STORE on EXAMINE must refuse [READ-ONLY]: %q", tagged)
	}
	_, tagged = c.expectNO("m5", "EXPUNGE")
	if !strings.Contains(tagged, "READ-ONLY") {
		t.Fatalf("EXPUNGE on EXAMINE must refuse: %q", tagged)
	}
	_, tagged = c.expectNO("m6", "APPEND INBOX")
	if !strings.Contains(tagged, "READ-ONLY") {
		t.Fatalf("APPEND on EXAMINE session must refuse: %q", tagged)
	}
	c.expectOK("m7", "FETCH 1 (UID)")
	c.expectOK("m8", "CHECK")
	c.expectOK("m9", "CLOSE")
	c.expectBAD("m10", "CHECK")
}

// TestHarnessAppendFetchSectionsAndPeek — APPEND with a literal (octet
// counts), BODY[] byte-exactness, \Seen side effect vs BODY.PEEK,
// HEADER.FIELDS, partial windows.
func TestHarnessAppendFetchSectionsAndPeek(t *testing.T) {
	env := newIMAPEnv(t)
	raw := simpleRawMessage("append-1@t.test", "Quarterly Report", "sender@t.test", "rcpt@t.test", "Hello fetching world")
	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)
	c.expectOK("q1", "SELECT INBOX")

	// APPEND with synchronizing literal.
	tagged := c.appendLiteral("q2", "INBOX", " (\\Seen)", raw)
	if !strings.HasPrefix(tagged, "q2 OK") {
		t.Fatalf("APPEND: %q", tagged)
	}
	u, _ := c.cmd("q3", "NOOP")
	if got := untaggedByPrefix(u, "* 1 EXISTS"); len(got) == 0 {
		t.Fatalf("EXISTS after APPEND must be 1, got %v", u)
	}

	// Metadata fetch.
	u, _ = c.expectOK("q4", "FETCH 1 (UID FLAGS RFC822.SIZE INTERNALDATE)")
	fetch := strings.Join(u, " ")
	if !strings.Contains(fetch, "UID 1") {
		t.Fatalf("first message must be UID 1: %q", fetch)
	}
	if !strings.Contains(fetch, "\\Seen") {
		t.Fatalf("appended \\Seen must surface: %q", fetch)
	}
	if !strings.Contains(fetch, fmt.Sprintf("RFC822.SIZE %d", len(raw))) {
		t.Fatalf("RFC822.SIZE mismatch: %q", fetch)
	}
	if !strings.Contains(fetch, `INTERNALDATE "`) {
		t.Fatalf("INTERNALDATE missing: %q", fetch)
	}

	// BODY.PEEK[HEADER] — header block literal.
	u, _ = c.expectOK("q5", "FETCH 1 (BODY.PEEK[HEADER])")
	hdr := literalOf(strings.Join(u, ""))
	if !strings.Contains(hdr, "From: sender@t.test") || !strings.Contains(hdr, "Subject: Quarterly Report") {
		t.Fatalf("HEADER section must carry the header block: %q", hdr)
	}

	// BODY[] — byte-exact raw message.
	u, _ = c.expectOK("q6", "FETCH 1 (BODY.PEEK[])")
	lit := literalOf(strings.Join(u, ""))
	if lit != raw {
		t.Fatalf("BODY[] must be byte-exact:\n got %q\nwant %q", lit, raw)
	}

	// BODY[HEADER.FIELDS (From Subject)] — exactly those fields + blank line.
	u, _ = c.expectOK("q7", "FETCH 1 (BODY.PEEK[HEADER.FIELDS (From Subject)])")
	lit = literalOf(strings.Join(u, ""))
	if !strings.Contains(lit, "From: sender@t.test") || !strings.Contains(lit, "Subject: Quarterly Report") {
		t.Fatalf("HEADER.FIELDS must carry requested fields: %q", lit)
	}
	if strings.Contains(lit, "Message-ID") {
		t.Fatalf("HEADER.FIELDS must exclude other fields: %q", lit)
	}
	if !strings.HasSuffix(lit, "\r\n\r\n") {
		t.Fatalf("HEADER.FIELDS section must end with the blank line: %q", lit)
	}

	// BODY.PEEK[TEXT] on an unseen message does not set \Seen; a non-peek
	// section fetch does.
	env.appendMessage("peeked", "a@t.test", simpleRawMessage("peek-1@t.test", "peeked", "a@t.test", "b@t.test", "peek body"))
	c.expectOK("q8", "NOOP")
	u, _ = c.expectOK("q9", "FETCH 2 (BODY.PEEK[TEXT] FLAGS)")
	if !strings.Contains(literalOf(strings.Join(u, "")), "peek body") {
		t.Fatalf("TEXT section must carry the body: %v", u)
	}
	if strings.Contains(strings.Join(u, " "), "\\Seen") {
		t.Fatalf("BODY.PEEK must not set \\Seen: %v", u)
	}
	u, _ = c.expectOK("q10", "FETCH 2 (BODY[TEXT])")
	if !strings.Contains(literalOf(strings.Join(u, "")), "peek body") {
		t.Fatalf("non-peek TEXT section: %v", u)
	}
	u, _ = c.expectOK("q11", "FETCH 2 (FLAGS)")
	if !strings.Contains(strings.Join(u, " "), "\\Seen") {
		t.Fatalf("non-peek BODY[] must set \\Seen: %v", u)
	}

	// Partial window: BODY[]<5.4> returns 4 octets from offset 5.
	u, _ = c.expectOK("q12", "FETCH 1 (BODY.PEEK[]<5.4>)")
	line := strings.Join(u, "")
	if !strings.Contains(line, "BODY[]<5> {4}") {
		t.Fatalf("partial literal expected BODY[]<5> {4}, got %q", line)
	}
	if got := literalOf(line); got != raw[5:9] {
		t.Fatalf("partial window must be raw[5:9], got %q", got)
	}

	// A missing section yields an empty literal, not an error.
	u, _ = c.expectOK("q13", "FETCH 1 (BODY.PEEK[9.9])")
	if !strings.Contains(strings.Join(u, " "), "BODY[9.9] {0}") {
		t.Fatalf("missing section must answer an empty literal: %v", u)
	}
}

// TestHarnessFetchMacrosAndStructure — ALL/FAST/FULL, ENVELOPE,
// BODY/BODYSTRUCTURE, RFC822* aliases.
func TestHarnessFetchMacrosAndStructure(t *testing.T) {
	env := newIMAPEnv(t)
	raw := simpleRawMessage("macro-1@t.test", "Struct Test", "from@t.test", "to@t.test", "macro body")
	env.appendMessage("Struct Test", "from@t.test", raw)
	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)
	c.expectOK("f1", "SELECT INBOX")

	u, _ := c.expectOK("f2", "FETCH 1 ALL")
	j := strings.Join(u, " ")
	for _, item := range []string{"FLAGS", "INTERNALDATE", "RFC822.SIZE", "ENVELOPE"} {
		if !strings.Contains(j, item) {
			t.Fatalf("ALL macro must include %s: %v", item, u)
		}
	}
	if !strings.Contains(j, `"Struct Test"`) {
		t.Fatalf("ENVELOPE subject must be quoted: %v", u)
	}

	u, _ = c.expectOK("f3", "FETCH 1 FAST")
	j = strings.Join(u, " ")
	if strings.Contains(j, "ENVELOPE") || strings.Contains(j, "BODY") {
		t.Fatalf("FAST must not carry envelope/body: %v", u)
	}

	u, _ = c.expectOK("f4", "FETCH 1 FULL")
	j = strings.Join(u, " ")
	if !strings.Contains(j, "BODY (") {
		t.Fatalf("FULL must include BODY structure: %v", u)
	}

	u, _ = c.expectOK("f5", "FETCH 1 BODYSTRUCTURE")
	j = strings.Join(u, " ")
	if !strings.Contains(j, `BODYSTRUCTURE ("TEXT" "PLAIN"`) {
		t.Fatalf("BODYSTRUCTURE for a simple text message: %v", u)
	}

	u, _ = c.expectOK("f6", "FETCH 1 (RFC822.HEADER)")
	if !strings.Contains(strings.Join(u, " "), "RFC822.HEADER {") {
		t.Fatalf("RFC822.HEADER literal expected: %v", u)
	}
	u, _ = c.expectOK("f7", "FETCH 1 (RFC822.TEXT)")
	if !strings.Contains(strings.Join(u, " "), "RFC822.TEXT {") {
		t.Fatalf("RFC822.TEXT literal expected: %v", u)
	}
	u, _ = c.expectOK("f8", "FETCH 1 (RFC822)")
	if !strings.Contains(strings.Join(u, " "), fmt.Sprintf("RFC822 {%d}", len(raw))) {
		t.Fatalf("RFC822 literal must be the whole message (%d bytes): %v", len(raw), u)
	}
	if literalOf(strings.Join(u, "")) != raw {
		t.Fatalf("RFC822 must be byte-exact")
	}

	c.expectBAD("f9", "FETCH 1 (NOTANITEM)")
}

// TestHarnessStoreFlagVariantsAndCASConflict — +FLAGS/-FLAGS/silent variants;
// the W1 CAS contract: a concurrent change by another session surfaces as
// tagged NO; re-observing re-arms the store.
func TestHarnessStoreFlagVariantsAndCASConflict(t *testing.T) {
	env := newIMAPEnv(t)
	env.appendMessage("s-one", "a@t.test", simpleRawMessage("s1@t.test", "s-one", "a@t.test", "b@t.test", "body"))
	env.appendMessage("s-two", "a@t.test", simpleRawMessage("s2@t.test", "s-two", "a@t.test", "b@t.test", "body"))

	a := dialEnv(t, env.addr)
	a.greeting()
	env.server.cfg.AllowCleartextAuth = true
	a.login(env)
	a.expectOK("k1", "SELECT INBOX")

	u, _ := a.expectOK("k2", "STORE 1 +FLAGS (\\Flagged)")
	if got := untaggedByPrefix(u, "* 1 FETCH"); len(got) != 1 || !strings.Contains(got[0], "\\Flagged") {
		t.Fatalf("+FLAGS must echo FLAGS: %v", u)
	}
	u, _ = a.expectOK("k3", "STORE 1 -FLAGS (\\Flagged)")
	if got := untaggedByPrefix(u, "* 1 FETCH"); len(got) != 1 || strings.Contains(got[0], "\\Flagged") {
		t.Fatalf("-FLAGS must clear the flag: %v", u)
	}
	u, _ = a.expectOK("k4", "STORE 2 +FLAGS.SILENT (\\Seen)")
	if got := untaggedByPrefix(u, "* 2 FETCH"); len(got) != 0 {
		t.Fatalf(".SILENT must not echo FLAGS: %v", u)
	}
	u, _ = a.expectOK("k5", "STORE 1 FLAGS (\\Answered)")
	if got := untaggedByPrefix(u, "* 1 FETCH"); len(got) != 1 || strings.Contains(got[0], "\\Seen") || !strings.Contains(got[0], "\\Answered") {
		t.Fatalf("FLAGS replace must swap the set: %v", u)
	}

	// CAS conflict: session B changes the message A last observed → A's
	// STORE answers tagged NO.
	b := dialEnv(t, env.addr)
	b.greeting()
	env.server.cfg.AllowCleartextAuth = true
	b.login(env)
	b.expectOK("k6", "SELECT INBOX")
	a.expectOK("k7", "FETCH 2 (UID FLAGS)")
	b.expectOK("k8", "STORE 2 +FLAGS (\\Flagged)")
	_, tagged := a.expectNO("k9", "STORE 2 -FLAGS (\\Flagged)")
	if !strings.Contains(tagged, "conflict") {
		t.Fatalf("CAS conflict must surface as tagged NO: %q", tagged)
	}
	a.expectOK("k10", "FETCH 2 (UID FLAGS)")
	a.expectOK("k11", "STORE 2 -FLAGS (\\Flagged)")
}

// TestHarnessCopyMoveUIDValidity — COPY/MOVE land messages with fresh UIDs
// in the target while the target's UIDVALIDITY never changes; MOVE expunges
// from the source.
func TestHarnessCopyMoveUIDValidity(t *testing.T) {
	env := newIMAPEnv(t)
	env.appendMessage("cm-one", "a@t.test", simpleRawMessage("cm1@t.test", "cm-one", "a@t.test", "b@t.test", "body"))
	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)
	c.expectOK("v0", "SELECT INBOX")
	c.expectOK("v1", "CREATE Archive")

	u, _ := c.expectOK("v2", "STATUS %q (UIDVALIDITY UIDNEXT)", "Archive")
	before := strings.Join(u, " ")

	_, _ = c.expectOK("v3", "COPY 1 Archive")
	u, _ = c.expectOK("v4", "STATUS %q (UIDVALIDITY UIDNEXT MESSAGES)", "Archive")
	after := strings.Join(u, " ")
	if fieldOf(before, "UIDVALIDITY") == "" || fieldOf(before, "UIDVALIDITY") != fieldOf(after, "UIDVALIDITY") {
		t.Fatalf("COPY must not change the target UIDVALIDITY: before=%s after=%s", before, after)
	}
	if fieldOf(after, "MESSAGES") != "1" {
		t.Fatalf("target must hold the copy: %q", after)
	}
	if n, _ := strconv.Atoi(fieldOf(after, "UIDNEXT")); n < 2 {
		t.Fatalf("target UIDNEXT must advance: %q", after)
	}

	_, tagged := c.expectNO("v5", "COPY 1 Missing")
	if !strings.Contains(tagged, "TRYCREATE") {
		t.Fatalf("COPY to missing mailbox must answer [TRYCREATE]: %q", tagged)
	}

	u, tagged = c.expectOK("v6", "MOVE 1 Archive")
	if !strings.Contains(tagged, "OK") {
		t.Fatalf("MOVE: %q", tagged)
	}
	if got := expungeLines(u); len(got) == 0 {
		t.Fatalf("MOVE must emit EXPUNGE (RFC 9051 §6.4.9): %v", u)
	}
	u, _ = c.expectOK("v7", "STATUS %q (MESSAGES)", "INBOX")
	if fieldOf(strings.Join(u, " "), "MESSAGES") != "0" {
		t.Fatalf("source folder must be empty after MOVE: %v", u)
	}
	u, _ = c.expectOK("v8", "STATUS %q (MESSAGES)", "Archive")
	if fieldOf(strings.Join(u, " "), "MESSAGES") != "2" {
		t.Fatalf("target must hold both messages after MOVE: %v", u)
	}
}

// TestHarnessUidVariants — UID FETCH/STORE/SEARCH/COPY/MOVE semantics: sets
// resolve in UID space, responses carry UID, out-of-range UIDs succeed
// silently.
func TestHarnessUidVariants(t *testing.T) {
	env := newIMAPEnv(t)
	env.appendMessage("u-one", "a@t.test", simpleRawMessage("u1@t.test", "u-one", "a@t.test", "b@t.test", "body one"))
	env.appendMessage("u-two", "a@t.test", simpleRawMessage("u2@t.test", "u-two", "a@t.test", "b@t.test", "body two"))
	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)
	c.expectOK("w1", "SELECT INBOX")

	u, _ := c.expectOK("w2", "UID FETCH 2 (FLAGS)")
	if !strings.Contains(strings.Join(u, " "), "UID 2") {
		t.Fatalf("UID FETCH must carry UID: %v", u)
	}
	u, _ = c.expectOK("w3", "UID FETCH 999 (FLAGS)")
	if len(u) != 0 {
		t.Fatalf("UID FETCH 999 must return no data: %v", u)
	}
	u, _ = c.expectOK("w4", "UID STORE 1 +FLAGS (\\Seen)")
	if got := fetchEchoLines(u); len(got) == 0 || !strings.Contains(got[0], "UID 1") {
		t.Fatalf("UID STORE echo must carry UID: %v", u)
	}
	u, _ = c.expectOK("w5", "UID SEARCH SEEN")
	if !strings.Contains(strings.Join(u, " "), "* SEARCH 1") {
		t.Fatalf("UID SEARCH must return UIDs: %v", u)
	}
	c.expectOK("w6", "CREATE UArch")
	c.expectOK("w7", "UID COPY 1 UArch")
	u, _ = c.expectOK("w8", "STATUS %q (MESSAGES)", "UArch")
	if fieldOf(strings.Join(u, " "), "MESSAGES") != "1" {
		t.Fatalf("UID COPY must land the message: %v", u)
	}
	u, _ = c.expectOK("w9", "UID MOVE 2 UArch")
	if got := expungeLines(u); len(got) == 0 {
		t.Fatalf("UID MOVE must emit EXPUNGE: %v", u)
	}
}

// TestHarnessSearchKeys — the ADR-004 SEARCH key set against real messages.
func TestHarnessSearchKeys(t *testing.T) {
	env := newIMAPEnv(t)
	oldDate := "Date: Tue, 01 Jan 2025 10:00:00 +0000\r\n"
	recentDate := "Date: Sat, 07 Nov 2026 09:00:00 +0000\r\n"
	base := "From: %s\r\nTo: rcpt@t.test\r\nCc: cc@t.test\r\nSubject: %s\r\nMessage-ID: <%s>\r\n%sContent-Type: text/plain\r\n\r\n%s\r\n"
	env.appendMessage("big", "big@t.test", fmt.Sprintf(base, "alpha@t.test", "Quarterly numbers", "srch1@t.test", oldDate, strings.Repeat("x", 5000)))
	env.appendMessage("small", "small@t.test", fmt.Sprintf(base, "beta@t.test", "Lunch plans", "srch2@t.test", recentDate, "short body"))
	env.appendMessage("flagged", "flag@t.test", fmt.Sprintf(base, "gamma@t.test", "Lunch numbers", "srch3@t.test", recentDate, "flag me"))

	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)
	c.expectOK("z0", "SELECT INBOX")
	c.expectOK("z0b", "STORE 3 +FLAGS (\\Flagged \\Seen)")

	assertSearch := func(tag, keys string, want string) {
		t.Helper()
		u, tagged := c.cmd(tag, "SEARCH %s", keys)
		if !strings.HasPrefix(tagged, tag+" OK") {
			t.Fatalf("SEARCH %s: %q", keys, tagged)
		}
		got := ""
		if len(u) > 0 {
			got = strings.TrimSpace(strings.TrimPrefix(u[0], "* SEARCH"))
		}
		if got != want {
			t.Fatalf("SEARCH %s: got %q want %q (untagged %v)", keys, got, want, u)
		}
	}
	assertSearch("z1", "ALL", "1 2 3")
	assertSearch("z2", "FLAGGED", "3")
	assertSearch("z3", "UNFLAGGED", "1 2")
	assertSearch("z4", "SEEN", "3")
	assertSearch("z5", "UNSEEN", "1 2")
	assertSearch("z6", "FROM alpha", "1")
	assertSearch("z7", "SUBJECT lunch", "2 3")
	assertSearch("z8", "HEADER Message-ID srch2", "2")
	assertSearch("z9", "LARGER 1000", "1")
	assertSearch("z10", "SMALLER 1000", "2 3")
	assertSearch("z11", "SINCE 1-Jan-2025", "1 2 3")
	assertSearch("z12", "BEFORE 1-Jan-2025", "")
	assertSearch("z13", "SENTSINCE 1-Jan-2026", "2 3")
	assertSearch("z14", "UID 2:*", "2 3")
	assertSearch("z15", "2:3", "2 3")
	assertSearch("z16", "NOT FLAGGED", "1 2")
	assertSearch("z17", "OR FROM beta FROM gamma", "2 3")
	// \Recent is ingest-time (never cleared), so everything is NEW; OLD is
	// empty (documented mailstore semantics).
	assertSearch("z18", "NEW", "1 2")
	assertSearch("z19", "OLD", "")
	assertSearch("z20", "KEYWORD \\Flagged", "3")
	assertSearch("z21", "UNKEYWORD \\Flagged", "1 2")
	// FTS-backed keys (PostgresSearcher wired in the harness env).
	assertSearch("z22", "BODY quarterly", "1")
	assertSearch("z23", "TEXT lunch", "2 3")
	assertSearch("z24", "CHARSET UTF-8 SUBJECT lunch", "2 3")
	_, tagged := c.cmd("z25", "SEARCH CHARSET KOI8-R ALL")
	if !strings.HasPrefix(tagged, "z25 NO") || !strings.Contains(tagged, "BADCHARSET") {
		t.Fatalf("unsupported charset must answer NO [BADCHARSET]: %q", tagged)
	}
}

// TestHarnessExpungeAndCloseSemantics — EXPUNGE announces victims newest-first;
// CLOSE expunges silently; EXAMINE CLOSE does not expunge.
func TestHarnessExpungeAndCloseSemantics(t *testing.T) {
	env := newIMAPEnv(t)
	for i := 1; i <= 3; i++ {
		env.appendMessage(fmt.Sprintf("e%d", i), "a@t.test", simpleRawMessage(fmt.Sprintf("e%d@t.test", i), fmt.Sprintf("e%d", i), "a@t.test", "b@t.test", "body"))
	}
	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)
	c.expectOK("x1", "SELECT INBOX")
	c.expectOK("x2", "STORE 1,3 +FLAGS.SILENT (\\Deleted)")
	u, _ := c.expectOK("x3", "EXPUNGE")
	if got := expungeLines(u); len(got) != 2 || got[0] != "* 3 EXPUNGE" || got[1] != "* 1 EXPUNGE" {
		t.Fatalf("EXPUNGE must announce victims newest-first: %v", u)
	}
	// Only message 2 remains, now at seq 1.
	c.expectOK("x4", "STORE 1 +FLAGS.SILENT (\\Deleted)")
	u, _ = c.expectOK("x5", "CLOSE")
	if got := expungeLines(u); len(got) != 0 {
		t.Fatalf("CLOSE must not announce EXPUNGE responses: %v", u)
	}
	c.expectBAD("x6", "CHECK")

	// EXAMINE CLOSE must NOT expunge \Deleted messages: the \Deleted flag is
	// set by a second read-write session, the read-only session closes, and
	// the message survives until a read-write CLOSE.
	env.appendMessage("e4", "a@t.test", simpleRawMessage("e4@t.test", "e4", "a@t.test", "b@t.test", "body"))
	c.expectOK("x7", "EXAMINE INBOX")
	_, tagged := c.expectNO("x8", "STORE 1 +FLAGS (\\Deleted)")
	if !strings.Contains(tagged, "READ-ONLY") {
		t.Fatalf("STORE on EXAMINE must refuse: %q", tagged)
	}
	b := dialEnv(t, env.addr)
	b.greeting()
	env.server.cfg.AllowCleartextAuth = true
	b.login(env)
	b.expectOK("x8b", "SELECT INBOX")
	b.expectOK("x8c", "STORE 1 +FLAGS.SILENT (\\Deleted)")
	c.expectOK("x9", "CLOSE")
	u, _ = c.expectOK("x10", "STATUS INBOX (MESSAGES)")
	if fieldOf(strings.Join(u, " "), "MESSAGES") != "1" {
		t.Fatalf("EXAMINE CLOSE must not expunge: %v", u)
	}
	// The read-write CLOSE does expunge.
	b.expectOK("x11", "CLOSE")
	u, _ = c.expectOK("x12", "STATUS INBOX (MESSAGES)")
	if fieldOf(strings.Join(u, " "), "MESSAGES") != "0" {
		t.Fatalf("read-write CLOSE must expunge: %v", u)
	}
}

// TestHarnessIdleNotificationDelivery — IDLE wakes on another session's
// append (EXISTS) and on flag changes (FETCH FLAGS); DONE ends cleanly.
func TestHarnessIdleNotificationDelivery(t *testing.T) {
	env := newIMAPEnv(t)
	env.appendMessage("idle-seed", "a@t.test", simpleRawMessage("idle1@t.test", "idle-seed", "a@t.test", "b@t.test", "body"))
	a := dialEnv(t, env.addr)
	a.greeting()
	env.server.cfg.AllowCleartextAuth = true
	a.login(env)
	a.expectOK("i1", "SELECT INBOX")

	a.send("i2 IDLE")
	if l := a.line(); !strings.HasPrefix(l, "+") {
		t.Fatalf("expected + idling, got %q", l)
	}

	// Another session appends and flags.
	b := dialEnv(t, env.addr)
	b.greeting()
	env.server.cfg.AllowCleartextAuth = true
	b.login(env)
	b.expectOK("i3", "SELECT INBOX")
	tagged := b.appendLiteral("i4", "INBOX", "", "From: x@t.test\r\n\r\nidle body\r\n")
	if !strings.HasPrefix(tagged, "i4 OK") {
		t.Fatalf("b APPEND: %q", tagged)
	}

	// A must see the EXISTS announcement while idling.
	if got := a.idleWaitFor("* 2 EXISTS", 5*time.Second); got == "" {
		t.Fatal("IDLE session did not receive the EXISTS notification")
	}

	// A flag change also announces FETCH FLAGS.
	b.expectOK("i6", "STORE 2 +FLAGS (\\Seen)")
	if got := a.idleWaitFor("FETCH (FLAGS", 5*time.Second); got == "" || !strings.Contains(got, "\\Seen") {
		t.Fatal("IDLE session did not receive the flag-change notification")
	}

	// DONE ends IDLE with tagged OK.
	a.send("DONE")
	tagged = a.waitForTagged("i2")
	if !strings.HasPrefix(tagged, "i2 OK") {
		t.Fatalf("DONE must end IDLE with OK: %q", tagged)
	}
}

// idleWaitFor reads idle announcements until match or deadline.
func (c *imapClient) idleWaitFor(needle string, d time.Duration) string {
	c.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		_ = c.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		line, err := c.br.ReadString('\n')
		if err != nil {
			continue
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.Contains(line, needle) {
			_ = c.conn.SetReadDeadline(time.Time{})
			return line
		}
	}
	_ = c.conn.SetReadDeadline(time.Time{})
	return ""
}

// TestHarnessNoopDeliversUntaggedUpdates — NOOP drains pending EXISTS and
// EXPUNGE announcements caused by other sessions.
func TestHarnessNoopDeliversUntaggedUpdates(t *testing.T) {
	env := newIMAPEnv(t)
	env.appendMessage("noop-seed", "a@t.test", simpleRawMessage("n1@t.test", "noop-seed", "a@t.test", "b@t.test", "body"))
	a := dialEnv(t, env.addr)
	a.greeting()
	env.server.cfg.AllowCleartextAuth = true
	a.login(env)
	a.expectOK("o1", "SELECT INBOX")

	b := dialEnv(t, env.addr)
	b.greeting()
	env.server.cfg.AllowCleartextAuth = true
	b.login(env)
	b.expectOK("o2", "SELECT INBOX")
	tagged := b.appendLiteral("o3", "INBOX", "", "From: y@t.test\r\n\r\nnoop body\r\n")
	if !strings.HasPrefix(tagged, "o3 OK") {
		t.Fatalf("b APPEND: %q", tagged)
	}

	u, _ := a.expectOK("o4", "NOOP")
	if got := untaggedByPrefix(u, "* 2 EXISTS"); len(got) == 0 {
		t.Fatalf("NOOP must report EXISTS 2: %v", u)
	}

	b.expectOK("o5", "STORE 1:2 +FLAGS.SILENT (\\Deleted)")
	b.expectOK("o6", "EXPUNGE")
	u, _ = a.expectOK("o7", "NOOP")
	if got := expungeLines(u); len(got) != 2 || got[0] != "* 2 EXPUNGE" || got[1] != "* 1 EXPUNGE" {
		t.Fatalf("NOOP must report expunges newest-first: %v", u)
	}
}

// TestHarnessAppendQuotaRejection — an over-quota APPEND answers
// NO [OVERQUOTA] (the mailstore QuotaError mapping, ADR-003 decision 5).
func TestHarnessAppendQuotaRejection(t *testing.T) {
	env := newIMAPEnv(t)
	if err := env.store.SetMailboxQuota(env.mailbox.ID, env.user.ID, 0, 10); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)
	c.expectOK("g1", "SELECT INBOX")
	raw := simpleRawMessage("quota@t.test", "too big", "a@t.test", "b@t.test", strings.Repeat("b", 500))
	tagged := c.appendLiteral("g2", "INBOX", "", raw)
	if !strings.HasPrefix(tagged, "g2 NO") || !strings.Contains(tagged, "OVERQUOTA") {
		t.Fatalf("over-quota APPEND must answer NO [OVERQUOTA]: %q", tagged)
	}
	c.expectOK("g3", "NOOP")
}

// TestHarnessCreateDeleteRenameSubscribeList — folder management + LIST +
// STATUS, including INBOX immutability and hierarchy.
func TestHarnessCreateDeleteRenameSubscribeList(t *testing.T) {
	env := newIMAPEnv(t)
	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)

	c.expectOK("h1", "CREATE %q", "Arch/2026/Nov")
	_, tagged := c.expectNO("h2", "CREATE %q", "Arch/2026/Nov")
	if !strings.Contains(tagged, "exists") {
		t.Fatalf("duplicate CREATE must say exists: %q", tagged)
	}
	c.expectNO("h3", "CREATE INBOX")

	u, _ := c.expectOK("h4", `LIST "" "*"`)
	listed := strings.Join(u, "\n")
	for _, want := range []string{`"INBOX"`, `"Arch"`, `"Arch/2026"`, `"Arch/2026/Nov"`} {
		if !strings.Contains(listed, want) {
			t.Fatalf("LIST * must include %s: %v", want, u)
		}
	}
	u, _ = c.expectOK("h5", `LIST "" "Arch/%%"`)
	listed = strings.Join(u, "\n")
	if !strings.Contains(listed, `"Arch/2026"`) || strings.Contains(listed, `"Arch/2026/Nov"`) {
		t.Fatalf("LIST %% must not cross the delimiter: %v", u)
	}
	u, _ = c.expectOK("h6", `LIST "" "inbox"`)
	if len(untaggedByPrefix(u, "* LIST")) != 1 {
		t.Fatalf("LIST inbox (lowercase) must still match INBOX: %v", u)
	}
	u, _ = c.expectOK("h7", `LIST "" "Arch"`)
	if !strings.Contains(strings.Join(u, " "), "\\HasChildren") {
		t.Fatalf("Arch must be \\HasChildren: %v", u)
	}

	c.expectOK("h8", "SUBSCRIBE %q", "Arch/2026")
	c.expectOK("h9", "UNSUBSCRIBE %q", "Arch/2026")
	c.expectNO("h10", "SUBSCRIBE %q", "Missing")

	c.expectOK("h11", "RENAME %q %q", "Arch/2026/Nov", "Dec")
	u, _ = c.expectOK("h12", `LIST "" "*"`)
	if !strings.Contains(strings.Join(u, "\n"), `"Arch/2026/Dec"`) {
		t.Fatalf("renamed subtree path missing: %v", u)
	}
	c.expectNO("h13", "RENAME INBOX %q", "NotInbox")

	c.expectOK("h14", "DELETE %q", "Arch")
	u, _ = c.expectOK("h15", `LIST "" "*"`)
	if strings.Contains(strings.Join(u, "\n"), `"Arch"`) {
		t.Fatalf("deleted subtree must disappear: %v", u)
	}
	_, tagged = c.expectNO("h16", "DELETE INBOX")
	if !strings.Contains(tagged, "cannot be deleted") {
		t.Fatalf("INBOX delete refusal: %q", tagged)
	}
	c.expectNO("h17", "DELETE %q", "Missing")
}

// TestHarnessStatusItems — STATUS renders every ADR-004 item.
func TestHarnessStatusItems(t *testing.T) {
	env := newIMAPEnv(t)
	env.appendMessage("st", "a@t.test", simpleRawMessage("st1@t.test", "st", "a@t.test", "b@t.test", "body"))
	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)
	u, _ := c.expectOK("y1", "STATUS INBOX (MESSAGES RECENT UIDNEXT UIDVALIDITY UNSEEN)")
	j := strings.Join(u, " ")
	for _, item := range []string{"MESSAGES 1", "UIDNEXT 2", "UIDVALIDITY", "UNSEEN 1", "RECENT"} {
		if !strings.Contains(j, item) {
			t.Fatalf("STATUS output missing %s: %v", item, u)
		}
	}
	c.expectBAD("y2", "STATUS INBOX (BOGUS)")
	_, tagged := c.expectNO("y3", "STATUS %q (MESSAGES)", "Missing")
	if !strings.Contains(tagged, "No such mailbox") {
		t.Fatalf("STATUS of missing mailbox: %q", tagged)
	}
}

// TestHarnessPerUserSessionCap — the connection-limit non-negotiable.
func TestHarnessPerUserSessionCap(t *testing.T) {
	cfg := imapConfigDefaults()
	cfg.MaxSessionsPerUser = 2
	cfg.AllowCleartextAuth = true
	env := newIMAPEnvWith(t, cfg)
	c1 := dialEnv(t, env.addr)
	c1.greeting()
	c1.login(env)
	c2 := dialEnv(t, env.addr)
	c2.greeting()
	c2.login(env)
	c3 := dialEnv(t, env.addr)
	c3.greeting()
	_, tagged := c3.expectNO("c1", "LOGIN %q %q", env.user.Email, env.appPass)
	if !strings.Contains(tagged, "UNAVAILABLE") {
		t.Fatalf("session cap must answer NO [UNAVAILABLE]: %q", tagged)
	}
	c1.expectOK("c2", "LOGOUT")
	deadline := time.Now().Add(2 * time.Second)
	for env.server.sessionsOfUser(env.user.ID) != 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	c4 := dialEnv(t, env.addr)
	c4.greeting()
	c4.expectOK("c3", "LOGIN %q %q", env.user.Email, env.appPass)
}

// TestHarnessCommandRateLimit — the per-session command budget answers BYE.
func TestHarnessCommandRateLimit(t *testing.T) {
	cfg := imapConfigDefaults()
	cfg.CommandRateLimit = 5
	cfg.CommandRateWindowSecs = 60
	cfg.AllowCleartextAuth = true
	env := newIMAPEnvWith(t, cfg)
	c := dialEnv(t, env.addr)
	c.greeting()
	c.login(env)
	// login = 1 command; 5 more NOOPs push the session over the budget.
	for i := 0; i < 5; i++ {
		c.send("q%d NOOP", 10+i)
	}
	if got := c.idleWaitFor("* BYE", 3*time.Second); got == "" || !strings.Contains(got, "rate limit") {
		t.Fatalf("command rate limit must terminate the session with BYE, got %q", got)
	}
}

// TestHarnessUnknownCommandAndMalformed — BAD for unknown verbs; the
// connection terminates on a malformed literal size (protocol resync).
func TestHarnessUnknownCommandAndMalformed(t *testing.T) {
	env := newIMAPEnv(t)
	c := dialEnv(t, env.addr)
	c.greeting()
	env.server.cfg.AllowCleartextAuth = true
	c.login(env)
	tagged := c.expectBAD("u1", "BOGUSCOMMAND")
	if !strings.Contains(tagged, "Unknown command") {
		t.Fatalf("unknown command: %q", tagged)
	}
	c.send("u2 SELECT {%s}", "abc")
	line := c.line()
	if !strings.HasPrefix(line, "* BAD") {
		t.Fatalf("malformed literal must produce * BAD, got %q", line)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		l, err := c.br.ReadString('\n')
		if err != nil {
			break // closed
		}
		if strings.Contains(l, "BYE") {
			break
		}
	}
	_ = c.conn.SetReadDeadline(time.Time{})
}

// TestHarnessLiteralCapRefusal — an APPEND literal above the cap is refused
// with a tagged NO instead of a continuation request (no octets are read).
func TestHarnessLiteralCapRefusal(t *testing.T) {
	cfg := imapConfigDefaults()
	cfg.MaxMessageSize = 64
	cfg.AllowCleartextAuth = true
	env := newIMAPEnvWith(t, cfg)
	c := dialEnv(t, env.addr)
	c.greeting()
	c.login(env)
	c.expectOK("m1", "SELECT INBOX")
	c.send("m2 APPEND INBOX {%d}", 100)
	tagged := c.waitForTagged("m2")
	if !strings.HasPrefix(tagged, "m2 NO") {
		t.Fatalf("oversized literal must be refused with NO before the continuation: %q", tagged)
	}
	// No continuation was sent, so the client proceeds with the next command.
	c.expectOK("m3", "NOOP")
}

// fetchEchoLines collects untagged FETCH response lines.
func fetchEchoLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "* ") && strings.Contains(l, " FETCH (") {
			out = append(out, l)
		}
	}
	return out
}

// expungeLines collects the untagged EXPUNGE announcements.
func expungeLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasSuffix(l, " EXPUNGE") {
			out = append(out, l)
		}
	}
	return out
}

// fieldOf extracts "KEY value" from a parenthesized STATUS-style string.
func fieldOf(s, key string) string {
	fields := strings.Fields(s)
	for i := 0; i < len(fields)-1; i++ {
		if strings.EqualFold(strings.Trim(fields[i], "()"), key) {
			return strings.Trim(fields[i+1], "()")
		}
	}
	return ""
}
