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

package mta

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/email"
)

// SF-02: CHUNKING (RFC 3030) client half. When the remote advertises
// CHUNKING the session runner ends the message with BDAT instead of DATA —
// the bytes cross the wire verbatim (dot-stuffing absent, so a message whose
// body starts with a dot needs no wire-level escaping and round-trips
// byte-exactly). Without the advertisement the runner keeps DATA.

// bdatServer is a minimal raw-wire ESMTP server for the BDAT tests. The
// shared mxServer harness is line-oriented (textproto), which cannot read a
// raw byte-counted BDAT payload; this server drives the whole conversation
// with one bufio.Reader of its own.
type bdatServer struct {
	t         *testing.T
	ln        net.Listener
	caps      []string
	bodies    chan string
	rejectBDAT bool
}

func newBdatServer(t *testing.T, caps ...string) *bdatServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &bdatServer{t: t, ln: ln, caps: caps, bodies: make(chan string, 4)}
	go s.serve()
	return s
}

func (s *bdatServer) addr() string { return s.ln.Addr().String() }

func (s *bdatServer) body() string {
	select {
	case b := <-s.bodies:
		return b
	case <-time.After(5 * time.Second):
		s.t.Fatal("no message body received")
		return ""
	}
}

func (s *bdatServer) serve() {
	c, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	reply := func(line string) {
		_, _ = fmt.Fprint(c, line+"\r\n")
	}
	reply("220 bdat.test ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO") || strings.HasPrefix(cmd, "HELO"):
			out := "250-bdat.test hello\r\n"
			for i, cap := range s.caps {
				sep := "-"
				if i == len(s.caps)-1 {
					sep = " "
				}
				out += "250" + sep + cap + "\r\n"
			}
			if len(s.caps) == 0 {
				out += "250 OK\r\n"
			}
			_, _ = fmt.Fprint(c, out)
		case strings.HasPrefix(cmd, "MAIL FROM"), strings.HasPrefix(cmd, "RCPT TO"), strings.HasPrefix(cmd, "QUIT"):
			if strings.HasPrefix(cmd, "QUIT") {
				reply("221 bye")
				return
			}
			reply("250 OK")
		case strings.HasPrefix(cmd, "DATA"):
			reply("354 go ahead")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimSpace(l) == "." {
					break
				}
				sb.WriteString(l)
			}
			s.bodies <- sb.String()
			reply("250 accepted")
		case strings.HasPrefix(cmd, "BDAT"):
			// `BDAT <n> [LAST]` — read exactly n raw bytes.
			var n int
			if _, err := fmt.Sscanf(cmd, "BDAT %d", &n); err != nil {
				reply("501 bad BDAT argument")
				return
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(r, buf); err != nil {
				return
			}
			if s.rejectBDAT {
				reply("550 5.7.1 relay access denied")
				continue
			}
			s.bodies <- string(buf)
			reply("250 accepted")
		default:
			reply("500 unrecognized")
		}
	}
}

func bdatSessionOpts(addr string) SessionOptions {
	return SessionOptions{
		Addr:       addr,
		ServerName: "bdat.test",
		HeloDomain: "ehlo.posta.test",
		Target:     TargetMX,
	}
}

func bdatEnvelope() []byte {
	// A body whose first line begins with a dot: the case where DATA's
	// dot-stuffing changes the wire bytes and BDAT's verbatim transfer shows.
	return []byte("Subject: bdat\r\n\r\n.leading dot line\r\nbinary\x01\x02tail\r\n")
}

func TestDirectDeliveryUsesBDATWhenRemoteAdvertisesChunking(t *testing.T) {
	srv := newBdatServer(t, "PIPELINING", "CHUNKING")

	res, err := runSession(context.Background(), bdatSessionOpts(srv.addr()), "sender@example.com", []string{"rcpt@example.com"}, bdatEnvelope())
	if err != nil {
		t.Fatalf("runSession: %v", err)
	}
	if !res.Delivered {
		t.Fatal("session must deliver via BDAT")
	}
	if !res.Caps.CHUNKING {
		t.Fatal("CHUNKING capability must be recorded")
	}
	if !strings.Contains(res.Msg, "BDAT") {
		t.Fatalf("result must describe the BDAT path: %q", res.Msg)
	}
	// Byte-exact: no dot-stuffing introduced, no escaping applied.
	if got := srv.body(); got != string(bdatEnvelope()) {
		t.Fatalf("BDAT payload not byte-exact:\n got %q\nwant %q", got, string(bdatEnvelope()))
	}
}

func TestDirectDeliveryFallsBackToDATAWithoutChunking(t *testing.T) {
	srv := newBdatServer(t, "PIPELINING")

	res, err := runSession(context.Background(), bdatSessionOpts(srv.addr()), "sender@example.com", []string{"rcpt@example.com"}, bdatEnvelope())
	if err != nil {
		t.Fatalf("runSession: %v", err)
	}
	if !res.Delivered {
		t.Fatal("session must deliver via DATA fallback")
	}
	if res.Caps.CHUNKING {
		t.Fatal("CHUNKING must not be recorded when not advertised")
	}
	// DATA path: the dot-led line arrives dot-stuffed on the wire and is
	// unstuffed by the server-side reader — the recorded body here is the
	// wire content BEFORE unstuffing by us (we record raw lines), so the
	// leading dot is doubled.
	got := srv.body()
	if !strings.Contains(got, "..leading dot line") {
		t.Fatalf("DATA path must dot-stuff the leading dot (wire content): %q", got)
	}
	if !strings.Contains(res.Msg, "DATA") {
		t.Fatalf("result must describe the DATA path: %q", res.Msg)
	}
}

func TestBDATPermanentRejectionClassifiesAsSendError(t *testing.T) {
	// A server that advertises CHUNKING but rejects the BDAT chunk with 550.
	srv := newBdatServer(t, "CHUNKING")
	srv.rejectBDAT = true

	_, err := runSession(context.Background(), bdatSessionOpts(srv.addr()), "sender@example.com", []string{"rcpt@example.com"}, bdatEnvelope())
	if err == nil {
		t.Fatal("BDAT rejection must surface an error")
	}
	var se *email.SendError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *email.SendError", err)
	}
	if !se.Permanent() {
		t.Fatalf("a 550 at BDAT is permanent; stage=%s code=%d", se.Stage, se.Code)
	}
	if se.Stage != "BDAT" {
		t.Fatalf("stage = %q, want BDAT", se.Stage)
	}
}
