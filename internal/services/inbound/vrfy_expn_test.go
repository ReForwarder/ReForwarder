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

package inbound

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// SF-01: the receivers' VRFY/EXPN wire posture — the decision recorded in the
// ADR-001 amendment (2026-11-17, SF-01). The SMTP parser dependency answers
// VRFY with the RFC 5321 §3.5.3/§7.3-mandated anti-harvest 252 and EXPN with
// an unconditional 502 refusal; no session hook exists in any upstream release
// (v0.25.0, v0.25.1 and the latest v0.26.0 all verified at their conn.go
// command dispatcher — a fork is the only path to a different answer, and the
// amendment records why the fork is rejected). These tests pin the posture ON
// THE WIRE through the production receiver so a dependency upgrade that ever
// changes it breaks loudly instead of silently. The LMTP and MSA listeners run
// the same dispatcher over the same module version, so the posture is shared.
//
// The exchanges are raw-wire on purpose: the receiver's answers are asserted
// at the protocol level (status code + enhanced code + text), independent of
// any client library's own VRFY convenience.

type rawSMTPClient struct {
	t  *testing.T
	c  net.Conn
	br *bufio.Reader
}

func dialRawSMTP(t *testing.T, addr string) *rawSMTPClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	c := &rawSMTPClient{t: t, c: conn, br: bufio.NewReader(conn)}
	greeting := c.readLine()
	if !strings.HasPrefix(greeting, "220") {
		t.Fatalf("greeting = %q, want 220", greeting)
	}
	return c
}

func (c *rawSMTPClient) write(line string) {
	c.t.Helper()
	_ = c.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.c.Write([]byte(line + "\r\n")); err != nil {
		c.t.Fatalf("write %q: %v", line, err)
	}
}

// readLine reads one response line.
func (c *rawSMTPClient) readLine() string {
	c.t.Helper()
	_ = c.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := c.br.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

// readMultiline drains a multiline reply: lines ending with "-" continue until
// the terminating line (same code + space).
func (c *rawSMTPClient) readMultiline(code string) []string {
	c.t.Helper()
	lines := []string{c.readLine()}
	for {
		last := lines[len(lines)-1]
		if !strings.HasPrefix(last, code+"-") {
			return lines
		}
		lines = append(lines, c.readLine())
	}
}

// ehlo completes the greeting handshake and returns the capability lines.
func (c *rawSMTPClient) ehlo() []string {
	c.t.Helper()
	c.write("EHLO client.example.org")
	lines := c.readMultiline("250")
	if !strings.HasPrefix(lines[len(lines)-1], "250 ") {
		c.t.Fatalf("EHLO failed: %v", lines)
	}
	return lines
}

// cmd sends one command and reads its single-line reply.
func (c *rawSMTPClient) cmd(line string) string {
	c.t.Helper()
	c.write(line)
	return c.readLine()
}

// expectCode fails the test unless the reply carries the exact status code
// and enhanced code.
func expectCode(t *testing.T, reply, code, enhanced string) string {
	t.Helper()
	if !strings.HasPrefix(reply, code+" ") {
		t.Fatalf("reply = %q, want %s", reply, code)
	}
	if !strings.HasPrefix(reply, code+" "+enhanced+" ") {
		t.Fatalf("reply = %q, want enhanced code %s %s", reply, code, enhanced)
	}
	return reply
}

// vrfyExpnAddr starts the production receiver (the memory-guard harness's
// server shape) with an un-wired backend: VRFY/EXPN must never reach backend
// state, so no stores are needed to assert the wire posture.
func vrfyExpnAddr(t *testing.T) string {
	t.Helper()
	b := &Backend{maxMessageSize: 1 << 20}
	return guardServer(t, b)
}

// TestReceiverVRFYAnswersRFC252 — VRFY answers 252 2.5.0 in both the
// greeting (pre-EHLO) and the command (post-EHLO) states: the RFC 5321
// §7.3 "disabled for security → MUST return 252" posture. The answer must
// never be 250 (§3.5.3 forbids an unverified success) nor a plain 550
// always-refusal form (§7.3: "equally not in conformance").
func TestReceiverVRFYAnswersRFC252(t *testing.T) {
	addr := vrfyExpnAddr(t)
	c := dialRawSMTP(t, addr)

	// Pre-EHLO: the dispatcher answers VRFY in every state.
	pre := c.cmd("VRFY ada@example.test")
	expectCode(t, pre, "252", "2.5.0")
	if !strings.Contains(pre, "Cannot VRFY") {
		t.Fatalf("VRFY pre-EHLO reply text = %q", pre)
	}

	// Post-EHLO: the normal probing state.
	c.ehlo()
	post := c.cmd("VRFY postmaster@example.test")
	expectCode(t, post, "252", "2.5.0")

	// Mid-transaction: same answer (state-independent).
	if resp := c.cmd("MAIL FROM:<sender@example.remote>"); !strings.HasPrefix(resp, "250") {
		t.Fatalf("MAIL FROM = %q", resp)
	}
	mid := c.cmd("VRFY ada@example.test")
	expectCode(t, mid, "252", "2.5.0")
}

// TestReceiverRefusesEXPNExplicitly — EXPN is refused 502 5.5.1 in every
// state: the disabled-by-default explicit refusal (RFC 5321 §3.5.3 directs
// 502 or 500 for a VRFY/EXPN the server does not actually verify; the
// dispatcher refuses before any session or backend involvement, so no
// directory information can leak regardless of configuration).
func TestReceiverRefusesEXPNExplicitly(t *testing.T) {
	addr := vrfyExpnAddr(t)
	c := dialRawSMTP(t, addr)

	// Pre-EHLO.
	pre := c.cmd("EXPN team@example.test")
	expectCode(t, pre, "502", "5.5.1")

	// Post-EHLO.
	c.ehlo()
	post := c.cmd("EXPN team@example.test")
	expectCode(t, post, "502", "5.5.1")

	// Mid-transaction.
	if resp := c.cmd("MAIL FROM:<sender@example.remote>"); !strings.HasPrefix(resp, "250") {
		t.Fatalf("MAIL FROM = %q", resp)
	}
	mid := c.cmd("EXPN team@example.test")
	expectCode(t, mid, "502", "5.5.1")

	// The refusal is the command's own name echoed by the parser's
	// not-implemented family — not a backend error shape.
	if !strings.Contains(strings.ToLower(mid), "expn") {
		t.Fatalf("EXPN reply lost the command name: %q", mid)
	}
}
