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

package mta

import (
	"context"
	"errors"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/email"
)

// The happy path over a plain in-process server: EHLO, MAIL, RCPT, DATA all
// 2xx → delivered, with the conversation visible on the wire.
func TestSessionHappyPath(t *testing.T) {
	srv := newMXServer(t, withCaps("PIPELINING", "8BITMIME", "SMTPUTF8"))
	res, err := runSession(context.Background(),
		SessionOptions{Addr: srv.addr(), ServerName: "mx.test", HeloDomain: "ehlo.posta.test", Target: TargetMX},
		"sender@example.com", []string{"rcpt@example.com"}, []byte("Subject: hi\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("runSession: %v", err)
	}
	if !res.Delivered {
		t.Fatal("session must report Delivered on 2xx at end-of-DATA")
	}
	if !res.Caps.EightBitMIME || !res.Caps.PIPELINING {
		t.Fatalf("caps = %+v, want 8BITMIME+PIPELINING negotiated", res.Caps)
	}
	if res.Caps.UpgradedTLS {
		t.Fatal("no STARTTLS offered: session must stay plaintext")
	}

	cmds := srv.commandsSeen()
	joined := strings.Join(cmds, "|")
	// The recorder upper-cases commands; our EHLO name must appear. The
	// MAIL FROM may carry the negotiated BODY/SMTPUTF8 params — match the
	// envelope address itself.
	if !strings.Contains(joined, "EHLO EHLO.POSTA.TEST") {
		t.Fatalf("EHLO must identify us as ehlo.posta.test: %v", cmds)
	}
	if !strings.Contains(joined, "MAIL FROM:<SENDER@EXAMPLE.COM>") {
		t.Fatalf("envelope MAIL FROM missing: %v", cmds)
	}
	if !strings.Contains(joined, "RCPT TO:<RCPT@EXAMPLE.COM>") {
		t.Fatalf("envelope RCPT TO missing: %v", cmds)
	}
	if !containsCmd(cmds, "DATA") {
		t.Fatalf("no DATA in conversation: %v", cmds)
	}
	if len(srv.bodiesSeen()) != 1 || !strings.Contains(srv.bodiesSeen()[0], "body") {
		t.Fatalf("message body not delivered: %v", srv.bodiesSeen())
	}
}

// STARTTLS is mandatory when the remote offers it: the session upgrades in
// band, with certificate verification, and the conversation continues over
// TLS (the server records the upgrade).
func TestSessionSTARTTLSEnforcedWhenOffered(t *testing.T) {
	srv := newMXServer(t, withTLS(), withCaps("8BITMIME"))
	res, err := runSession(context.Background(),
		SessionOptions{
			Addr: srv.addr(), ServerName: "mx.test", Target: TargetMX,
			TLSConfig: clientTLSConfig(t, srv.cert),
		},
		"sender@example.com", []string{"rcpt@example.com"}, []byte("body"))
	if err != nil {
		t.Fatalf("runSession: %v", err)
	}
	if !res.Caps.UpgradedTLS {
		t.Fatal("session must upgrade to TLS when STARTTLS is offered")
	}
	if !srv.sawTLS() {
		t.Fatal("server completed no TLS handshake — STARTTLS was not enforced")
	}
	if !res.Delivered {
		t.Fatal("delivery over TLS must complete")
	}
}

// Plaintext continues only when the remote offers no STARTTLS at all.
func TestSessionPlainWhenSTARTTLSNotOffered(t *testing.T) {
	srv := newMXServer(t, withCaps("8BITMIME")) // no STARTTLS capability
	res, err := runSession(context.Background(),
		SessionOptions{Addr: srv.addr(), ServerName: "mx.test", Target: TargetMX},
		"sender@example.com", []string{"rcpt@example.com"}, []byte("body"))
	if err != nil {
		t.Fatalf("runSession: %v", err)
	}
	if res.Caps.UpgradedTLS {
		t.Fatal("no STARTTLS advertised: session must stay plaintext")
	}
	if srv.sawTLS() {
		t.Fatal("server saw a TLS handshake although it advertised none")
	}
	if !res.Delivered {
		t.Fatal("plaintext delivery must complete when STARTTLS is not offered")
	}
}

// A 4xx reply at MAIL FROM is transient: SendError with the reply code, not
// permanent, exactly the shape the worker's retry path consumes.
func TestSessionTransient450AtMailFrom(t *testing.T) {
	srv := newMXServer(t, withReply("MAIL FROM", "450 4.2.1 try again later"))
	_, err := runSession(context.Background(),
		SessionOptions{Addr: srv.addr(), ServerName: "mx.test", Target: TargetMX},
		"sender@example.com", []string{"rcpt@example.com"}, []byte("body"))
	var se *email.SendError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *email.SendError", err)
	}
	if se.Code != 450 || se.Permanent() {
		t.Fatalf("code = %d, permanent = %v; a 4xx must stay transient", se.Code, se.Permanent())
	}
	if se.Stage != "MAIL FROM" {
		t.Fatalf("stage = %q, want MAIL FROM", se.Stage)
	}
}

// A 421 (service not available / throttle) is transient too.
func TestSessionTransient421AtMailFrom(t *testing.T) {
	srv := newMXServer(t, withReply("MAIL FROM", "421 4.7.0 too busy"))
	_, err := runSession(context.Background(),
		SessionOptions{Addr: srv.addr(), ServerName: "mx.test", Target: TargetMX},
		"sender@example.com", []string{"rcpt@example.com"}, []byte("body"))
	var se *email.SendError
	if !errors.As(err, &se) || se.Code != 421 || se.Permanent() {
		t.Fatalf("421 must surface as a transient SendError, got %v", err)
	}
}

// A 550 at RCPT TO is permanent and carries the recipient — the exact shape
// the worker's permanentRejection predicate and R-05 classifier consume.
func TestSessionPermanent550AtRcptTo(t *testing.T) {
	srv := newMXServer(t, withReply("RCPT TO", "550 5.1.1 user unknown"))
	_, err := runSession(context.Background(),
		SessionOptions{Addr: srv.addr(), ServerName: "mx.test", Target: TargetMX},
		"sender@example.com", []string{"dead@example.com"}, []byte("body"))
	var se *email.SendError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *email.SendError", err)
	}
	if se.Code != 550 || !se.Permanent() || se.Stage != "RCPT TO" || se.Recipient != "dead@example.com" {
		t.Fatalf("SendError = {stage %q, code %d, rcpt %q}", se.Stage, se.Code, se.Recipient)
	}
	workerPredicate := func(err error) bool {
		var s *email.SendError
		return errors.As(err, &s) && s.Permanent() && s.Stage == "RCPT TO" && s.Recipient != ""
	}
	if !workerPredicate(err) {
		t.Fatal("550 at RCPT TO must satisfy the permanent-rejection predicate")
	}
}

// AUTH is refused outright on an MX session — non-negotiable (ADR-001 §4) —
// even before any dial happens.
func TestSessionRefusesAUTHOnMXSession(t *testing.T) {
	_, err := runSession(context.Background(), SessionOptions{
		Addr: "127.0.0.1:1", ServerName: "mx.test", Target: TargetMX,
		Auth: smtp.PlainAuth("", "user", "pass", "mx.test"),
	}, "sender@example.com", []string{"rcpt@example.com"}, []byte("body"))
	if err == nil || !strings.Contains(err.Error(), "AUTH is not permitted on MX sessions") {
		t.Fatalf("AUTH on an MX session must be refused, got %v", err)
	}
}

// A non-ASCII envelope requires SMTPUTF8; without the capability the session
// fails closed with a permanent policy rejection.
func TestSessionSMTPUTF8RequiredForUTF8Envelope(t *testing.T) {
	srv := newMXServer(t, withCaps("8BITMIME")) // no SMTPUTF8 advertised
	_, err := runSession(context.Background(),
		SessionOptions{Addr: srv.addr(), ServerName: "mx.test", Target: TargetMX},
		"üser@example.com", []string{"rcpt@example.com"}, []byte("body"))
	var se *email.SendError
	if !errors.As(err, &se) || !se.Permanent() {
		t.Fatalf("UTF-8 envelope without SMTPUTF8 must fail permanently, got %v", err)
	}
	// The refusal happens before MAIL FROM: nothing was sent.
	if containsCmd(srv.commandsSeen(), "MAIL FROM") {
		t.Fatal("no MAIL FROM may be sent when SMTPUTF8 is required but unadvertised")
	}
}

// A UTF-8 envelope over a server that advertises SMTPUTF8 goes through.
func TestSessionSMTPUTF8Negotiated(t *testing.T) {
	srv := newMXServer(t, withCaps("SMTPUTF8"))
	res, err := runSession(context.Background(),
		SessionOptions{Addr: srv.addr(), ServerName: "mx.test", Target: TargetMX},
		"üser@example.com", []string{"rcpt@example.com"}, []byte("body"))
	if err != nil {
		t.Fatalf("runSession: %v", err)
	}
	if !res.Caps.SMTPUTF8 || !res.Delivered {
		t.Fatalf("caps = %+v; UTF-8 envelope must deliver with SMTPUTF8", res.Caps)
	}
}

// A server that answers the banner and EHLO and then goes silent must fail
// within the per-command budget (R-22 discipline extended to direct MX).
func TestSessionStallMidConversationFailsWithinBudget(t *testing.T) {
	restoreDialTimeout(t)
	email.SetDialTimeout(400 * time.Millisecond)
	budget := email.SessionBudget() // 1.6s

	// Black-hole: accepts, greets, answers EHLO, then never replies.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go serveGreetingThenStall(c, budget)
		}
	}()

	start := time.Now()
	_, serr := runSession(context.Background(),
		SessionOptions{Addr: ln.Addr().String(), ServerName: "127.0.0.1", Target: TargetMX},
		"sender@example.com", []string{"rcpt@example.com"}, []byte("body"))
	elapsed := time.Since(start)

	if serr == nil {
		t.Fatal("stalled session must fail")
	}
	if elapsed > budget+1500*time.Millisecond {
		t.Fatalf("session took %v, want within the %v budget + slack", elapsed, budget)
	}
	var se *email.SendError
	if errors.As(serr, &se) && se.Permanent() {
		t.Fatal("a stall/timeout failure must not be permanent")
	}
}

func serveGreetingThenStall(c net.Conn, hold time.Duration) {
	defer func() { _ = c.Close() }()
	buf := make([]byte, 4096)
	_, _ = c.Write([]byte("220 blackhole ESMTP\r\n"))
	if _, err := c.Read(buf); err != nil {
		return
	}
	_, _ = c.Write([]byte("250-blackhole\r\n250 8BITMIME\r\n"))
	for {
		if _, err := c.Read(buf); err != nil {
			return
		}
	}
}

func containsCmd(cmds []string, prefix string) bool {
	for _, c := range cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
