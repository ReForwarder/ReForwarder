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

package email

import (
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// restoreDialTimeout snapshots the process-wide dial timeout so a test can
// shrink it without leaking the change into other tests.
func restoreDialTimeout(t *testing.T) {
	t.Helper()
	prev := DialTimeout()
	t.Cleanup(func() { SetDialTimeout(prev) })
}

func TestDialTimeoutDefaultAndSetter(t *testing.T) {
	restoreDialTimeout(t)
	if got := DialTimeout(); got != DefaultDialTimeout {
		t.Fatalf("DialTimeout() = %v, want default %v", got, DefaultDialTimeout)
	}
	SetDialTimeout(3 * time.Second)
	if got := DialTimeout(); got != 3*time.Second {
		t.Fatalf("DialTimeout() = %v after SetDialTimeout(3s)", got)
	}
	// Zero and negative values keep the current setting: an operator typo
	// (POSTA_SMTP_DIAL_TIMEOUT_SECS=0) must not disable the bound entirely.
	SetDialTimeout(0)
	if got := DialTimeout(); got != 3*time.Second {
		t.Fatalf("SetDialTimeout(0) must be a no-op, got %v", got)
	}
}

// A timeout failure is transient: Permanent() stays false (no SMTP reply code),
// so the worker's permanentRejection predicate — which requires Permanent() &&
// Stage == "RCPT TO" && Recipient != "" — can never match a timeout, and the
// task keeps retrying via asynq.
func TestSendErrorTimeoutIsTransient(t *testing.T) {
	// The shape a deadline-exceeded read/write surfaces as: a *net.OpError
	// wrapping os.ErrDeadlineExceeded, a net.Error with Timeout() == true.
	deadline := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	wrapped := wrapSendError("DATA", "", fmt.Errorf("SMTP write failed: %w", deadline))

	var se *SendError
	if !errors.As(wrapped, &se) {
		t.Fatalf("wrapSendError result is not a *SendError")
	}
	if !se.TimedOut() {
		t.Fatalf("TimedOut() = false for a net.Error timeout")
	}
	if se.Permanent() {
		t.Fatalf("Permanent() = true for a timeout — a timeout must stay transient")
	}
	if se.Code != 0 {
		t.Fatalf("Code = %d for a timeout, want 0 (no SMTP reply)", se.Code)
	}

	// The exact predicate the worker applies before auto-suppression
	// (internal/worker/handler.go permanentRejection). A SendError carrying a
	// 550 at RCPT TO matches; a timeout never does.
	workerPredicate := func(err error) bool {
		var s *SendError
		return errors.As(err, &s) && s.Permanent() && s.Stage == "RCPT TO" && s.Recipient != ""
	}
	if workerPredicate(wrapped) {
		t.Fatal("timeout SendError must not satisfy the permanent-rejection predicate")
	}
	rejection := wrapSendError("RCPT TO", "user@example.com",
		fmt.Errorf("SMTP RCPT TO failed: %w", &textproto.Error{Code: 550, Msg: "5.1.1 rejected"}))
	if !workerPredicate(rejection) {
		t.Fatal("sanity: a 550 RCPT TO failure must satisfy the predicate")
	}
}

// A non-timeout network failure (connection refused) is not classified as a
// timeout — it must not claim TimedOut().
func TestSendErrorNonTimeoutNetworkError(t *testing.T) {
	wrapped := wrapSendError("MAIL FROM", "x@example.com",
		fmt.Errorf("SMTP MAIL FROM failed: %w", errors.New("dial tcp: connection refused")))
	var se *SendError
	if !errors.As(wrapped, &se) || se.TimedOut() {
		t.Fatalf("connection refused must not be classified as a timeout")
	}
}

// A black-holed relay accepts the TCP connection and never sends a byte. The
// send must fail within the overall session budget (dial timeout × factor),
// releasing the worker slot; asynq then retries the task as for any other
// transient failure. This pins the deadline mechanism end to end: before R-22
// this send blocked forever.
func TestSendFailsAgainstBlackHoledRelay(t *testing.T) {
	restoreDialTimeout(t)
	SetDialTimeout(400 * time.Millisecond)
	budget := 400 * time.Millisecond * commandBudgetFactor

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Accept connections, read nothing, reply nothing.
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			// Hold the socket well past the deadline before closing.
			time.Sleep(budget + 3*time.Second)
			_ = c.Close()
		}
	}()

	sender := NewSMTPSender()
	server := &models.SMTPServer{Host: "127.0.0.1", Port: portOf(t, ln), Encryption: models.EncryptionNone}

	start := time.Now()
	sendErr := sender.Send(server, "sender@example.com", []string{"rcpt@example.com"},
		"subject", "", "body", nil, nil, "", "", false)
	elapsed := time.Since(start)

	if sendErr == nil {
		t.Fatal("send against a black-holed relay must fail")
	}
	if elapsed > budget+1500*time.Millisecond {
		t.Fatalf("send took %v, want within the %v budget + slack", elapsed, budget)
	}

	var se *SendError
	var nerr net.Error
	if errors.As(sendErr, &se) {
		if se.Permanent() {
			t.Fatalf("timeout failure must not be permanent: %v", sendErr)
		}
	} else if !errors.As(sendErr, &nerr) || !nerr.Timeout() {
		t.Fatalf("send error %v is neither a timed-out net.Error nor a SendError", sendErr)
	}
}

// The same black-hole contract for the implicit-TLS path: the TLS handshake
// never completes because the server never replies, so the dial (handshake
// included, via tls.DialWithDialer) fails within the dial timeout.
func TestSendImplicitTLSFailsAgainstBlackHoledRelay(t *testing.T) {
	restoreDialTimeout(t)
	SetDialTimeout(400 * time.Millisecond)

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
			time.Sleep(3 * time.Second)
			_ = c.Close()
		}
	}()

	sender := NewSMTPSender()
	server := &models.SMTPServer{Host: "127.0.0.1", Port: portOf(t, ln), Encryption: models.EncryptionSSL}

	start := time.Now()
	sendErr := sender.Send(server, "sender@example.com", []string{"rcpt@example.com"},
		"subject", "", "body", nil, nil, "", "", false)
	elapsed := time.Since(start)

	if sendErr == nil {
		t.Fatal("implicit-TLS send against a black-holed relay must fail")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("send took %v, want within the dial timeout + slack", elapsed)
	}
	if !strings.Contains(sendErr.Error(), "SSL dial failed") {
		t.Fatalf("unexpected error: %v", sendErr)
	}
}

// A relay that answers the banner and EHLO and then goes silent before MAIL
// FROM also fails within the overall budget: the deadline covers the whole
// session, not just the dial.
func TestSendFailsWhenRelayStallsMidConversation(t *testing.T) {
	restoreDialTimeout(t)
	SetDialTimeout(400 * time.Millisecond)

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
			go serveGreetingThenStall(c)
		}
	}()

	sender := NewSMTPSender()
	server := &models.SMTPServer{Host: "127.0.0.1", Port: portOf(t, ln), Encryption: models.EncryptionNone}

	start := time.Now()
	sendErr := sender.Send(server, "sender@example.com", []string{"rcpt@example.com"},
		"subject", "", "body", nil, nil, "", "", false)
	elapsed := time.Since(start)

	if sendErr == nil {
		t.Fatal("send against a mid-conversation stall must fail")
	}
	// Dial is immediate; the stall happens after EHLO, so the remaining
	// session budget (~1.6s) must fire.
	if elapsed > 3*time.Second {
		t.Fatalf("send took %v, want within the session budget + slack", elapsed)
	}
}

// serveGreetingThenStall writes a 220 banner, answers EHLO, then never replies
// again — the client hangs inside MAIL FROM until its deadline fires.
func serveGreetingThenStall(c net.Conn) {
	defer func() { _ = c.Close() }()
	buf := make([]byte, 4096)
	writeAll(c, "220 blackhole ESMTP\r\n")
	if _, err := c.Read(buf); err != nil { // EHLO
		return
	}
	writeAll(c, "250-blackhole\r\n250 8BITMIME\r\n")
	for {
		if _, err := c.Read(buf); err != nil { // MAIL FROM … then silence
			return
		}
	}
}

func writeAll(c net.Conn, s string) {
	_, _ = c.Write([]byte(s))
}

func portOf(t *testing.T, ln net.Listener) int {
	t.Helper()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener address %T", ln.Addr())
	}
	return addr.Port
}

// The client still works end to end against a real (in-process) relay: a plain
// SMTP conversation completes when the server behaves. This guards the
// refactor from bare smtp.Dial to dialer + NewClient.
func TestSendAgainstWorkingRelay(t *testing.T) {
	restoreDialTimeout(t)
	SetDialTimeout(2 * time.Second)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	sessions := make(chan []string, 4)
	go serveHappyRelay(ln, sessions)

	sender := NewSMTPSender()
	server := &models.SMTPServer{Host: "127.0.0.1", Port: portOf(t, ln), Encryption: models.EncryptionNone}
	if err := sender.Send(server, "Sender <sender@example.com>", []string{"Rcpt <rcpt@example.com>"},
		"subject", "<p>hi</p>", "hi", nil, nil, "", "", false); err != nil {
		t.Fatalf("send against a working relay failed: %v", err)
	}

	select {
	case convo := <-sessions:
		// The relay echoes commands upper-cased; compare accordingly.
		joined := strings.ToUpper(strings.Join(convo, "|"))
		if !strings.Contains(joined, "MAIL FROM:<SENDER@EXAMPLE.COM>") {
			t.Fatalf("envelope MAIL FROM missing: %v", convo)
		}
		if !strings.Contains(joined, "RCPT TO:<RCPT@EXAMPLE.COM>") {
			t.Fatalf("envelope RCPT TO must use the bare address: %v", convo)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay saw no complete session")
	}
}

// serveHappyRelay is a minimal but complete SMTP server: banner, EHLO, 250 to
// MAIL/RCPT, accepts DATA, replies 250 after the final dot, 221 on QUIT.
func serveHappyRelay(ln net.Listener, sessions chan<- []string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()
			var convo []string
			text := textproto.NewConn(c)
			defer func() { _ = text.Close() }()
			_ = text.PrintfLine("220 relay ESMTP")
			for {
				line, err := text.ReadLine()
				if err != nil {
					return
				}
				cmd := strings.ToUpper(strings.TrimSpace(line))
				switch {
				case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
					_ = text.PrintfLine("250-relay")
					_ = text.PrintfLine("250 8BITMIME")
				case strings.HasPrefix(cmd, "MAIL FROM:"):
					convo = append(convo, cmd)
					_ = text.PrintfLine("250 ok")
				case strings.HasPrefix(cmd, "RCPT TO:"):
					convo = append(convo, cmd)
					_ = text.PrintfLine("250 ok")
				case cmd == "DATA":
					_ = text.PrintfLine("354 go ahead")
					for {
						l, err := text.ReadLine()
						if err != nil {
							return
						}
						if l == "." {
							break
						}
					}
					_ = text.PrintfLine("250 accepted")
				case cmd == "QUIT":
					_ = text.PrintfLine("221 bye")
					sessions <- convo
					return
				default:
					_ = text.PrintfLine("250 ok")
				}
			}
		}(c)
	}
}
