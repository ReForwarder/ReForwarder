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

package spamfilter

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ADR-006 tests: the clamd INSTREAM client against an in-process fake clamd
// listening on a real loopback port (no dialer seam needed), the fail-mode
// policy, and the response parser.

// eicarSample is the standard EICAR antivirus test string — the industry's
// designated benign malware-test payload.
const eicarSample = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

// fakeClamd is a minimal clamd: it reads zINSTREAM\0 + length-prefixed
// chunks, then answers with the configured response. PING gets PONG.
type fakeClamd struct {
	listener net.Listener
	mu       sync.Mutex
	scans    int
	// response is what a completed INSTREAM scan answers with.
	response string
	// failAccepts makes every connection error out (transport-level fault).
	failAccepts bool
}

func startFakeClamd(t *testing.T, response string) *fakeClamd {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeClamd{listener: l, response: response}
	go f.serve()
	t.Cleanup(func() { _ = l.Close() })
	return f
}

func (f *fakeClamd) addr() string { return f.listener.Addr().(*net.TCPAddr).IP.String() }
func (f *fakeClamd) port() int    { return f.listener.Addr().(*net.TCPAddr).Port }
func (f *fakeClamd) scanCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scans
}

func (f *fakeClamd) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		failing := f.failAccepts
		f.mu.Unlock()
		if failing {
			_ = conn.Close()
			continue
		}
		go f.handle(conn)
	}
}

func (f *fakeClamd) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Read the NUL-terminated command.
	var cmd []byte
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(conn, one); err != nil {
			return
		}
		if one[0] == 0 {
			break
		}
		cmd = append(cmd, one[0])
	}
	command := strings.TrimPrefix(string(cmd), "z")
	command = strings.TrimSuffix(command, "\n")
	switch command {
	case "PING":
		_, _ = conn.Write([]byte("PONG\x00"))
		return
	case "INSTREAM":
		// Drain length-prefixed chunks.
		var lenBuf [4]byte
		for {
			if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(lenBuf[:])
			if n == 0 {
				break
			}
			if _, err := io.CopyN(io.Discard, conn, int64(n)); err != nil {
				return
			}
		}
		f.mu.Lock()
		f.scans++
		resp := f.response
		f.mu.Unlock()
		_, _ = conn.Write([]byte(resp + "\x00"))
	default:
		_, _ = conn.Write([]byte("UNKNOWN COMMAND\x00"))
	}
}

func clientFor(f *fakeClamd, failMode ClamFailMode) *ClamdClient {
	return NewClamdClient(ClamdConfig{
		Host:     f.addr(),
		Port:     f.port(),
		Timeout:  5 * time.Second,
		FailMode: failMode,
	})
}

func TestClamdScanClean(t *testing.T) {
	f := startFakeClamd(t, "stream: OK")
	c := clientFor(f, ClamFailOpen)
	res, err := c.Scan(context.Background(), []byte("hello world"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Infected || res.Signature != "" || res.Skipped {
		t.Fatalf("clean result = %+v", res)
	}
	if f.scanCount() != 1 {
		t.Fatalf("scans = %d, want 1", f.scanCount())
	}
}

func TestClamdScanInfected(t *testing.T) {
	f := startFakeClamd(t, "stream: Eicar-Test-Signature FOUND")
	c := clientFor(f, ClamFailOpen)
	res, err := c.Scan(context.Background(), []byte(eicarSample))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.Infected {
		t.Fatalf("result = %+v, want infected", res)
	}
	if res.Signature != "Eicar-Test-Signature" {
		t.Fatalf("signature = %q, want Eicar-Test-Signature", res.Signature)
	}
}

func TestClamdPing(t *testing.T) {
	f := startFakeClamd(t, "stream: OK")
	if err := clientFor(f, ClamFailOpen).Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestClamdUnexpectedResponseIsError(t *testing.T) {
	f := startFakeClamd(t, "size limit exceeded")
	c := clientFor(f, ClamFailOpen)
	res, err := c.Scan(context.Background(), []byte("payload"))
	if err == nil {
		t.Fatal("expected error for unexpected clamd response")
	}
	if !strings.Contains(err.Error(), "size limit exceeded") {
		t.Fatalf("error = %v, want it to carry the response", err)
	}
	if res.Infected {
		t.Fatal("unexpected response must never look infected")
	}
}

func TestClamdUnreachableFails(t *testing.T) {
	// Bind then close a port to guarantee connection refusal.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	c := NewClamdClient(ClamdConfig{Host: "127.0.0.1", Port: closedPort, Timeout: 2 * time.Second})
	if _, err := c.Scan(context.Background(), []byte("x")); err == nil {
		t.Fatal("scan against a closed clamd must fail")
	}
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("ping against a closed clamd must fail")
	}
}

func TestClamdDisabledWithoutHost(t *testing.T) {
	c := NewClamdClient(ClamdConfig{})
	if c.Enabled() {
		t.Fatal("client without host must be disabled")
	}
	if _, err := c.Scan(context.Background(), []byte("x")); err == nil {
		t.Fatal("scan on disabled client must fail")
	}
	if c.FailMode() != ClamFailOpen {
		t.Fatalf("default fail mode = %v, want open", c.FailMode())
	}
}

func TestClamdOversizedSampleSkipped(t *testing.T) {
	c := NewClamdClient(ClamdConfig{
		Host:           "127.0.0.1",
		Port:           1, // nothing there — must not be dialed
		MaxStreamBytes: 8,
		Timeout:        time.Second,
	})
	res, err := c.Scan(context.Background(), []byte("0123456789"))
	if err != nil {
		t.Fatalf("oversized sample must skip, got error: %v", err)
	}
	if !res.Skipped || res.Infected {
		t.Fatalf("result = %+v, want skipped", res)
	}
}

func TestClamdFailModeFromEnv(t *testing.T) {
	cases := []struct {
		in   string
		want ClamFailMode
	}{
		{"open", ClamFailOpen},
		{"CLOSED", ClamFailClosed},
		{" closed ", ClamFailClosed},
		{"", ClamFailOpen},
		{"nonsense", ClamFailOpen},
	}
	for _, tc := range cases {
		if got := ClamFailModeFromEnv(tc.in); got != tc.want {
			t.Fatalf("ClamFailModeFromEnv(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestClamdDeadlineTerminatesStuckServer(t *testing.T) {
	// A clamd that accepts but never answers must not hang past the timeout.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_ = conn // hold it open, say nothing
		}
	}()

	c := NewClamdClient(ClamdConfig{
		Host:     l.Addr().(*net.TCPAddr).IP.String(),
		Port:     l.Addr().(*net.TCPAddr).Port,
		Timeout:  300 * time.Millisecond,
		FailMode: ClamFailOpen,
	})
	start := time.Now()
	_, err = c.Scan(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("stuck clamd must produce a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("scan took %v, deadline not enforced", elapsed)
	}
}

func TestParseClamdResponse(t *testing.T) {
	cases := []struct {
		resp      string
		infected  bool
		signature string
		wantErr   bool
	}{
		{"stream: OK", false, "", false},
		{"stream: Eicar-Test-Signature FOUND", true, "Eicar-Test-Signature", false},
		{"stream: Win.Test.EICAR_HDB-1 FOUND", true, "Win.Test.EICAR_HDB-1", false},
		{"size limit exceeded", false, "", true},
		{"", false, "", true},
	}
	for _, tc := range cases {
		res, err := parseClamdResponse(tc.resp)
		if tc.wantErr && err == nil {
			t.Fatalf("parse(%q) = %+v, want error", tc.resp, res)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("parse(%q) error: %v", tc.resp, err)
		}
		if res.Infected != tc.infected || res.Signature != tc.signature {
			t.Fatalf("parse(%q) = %+v, want infected=%v sig=%q", tc.resp, res, tc.infected, tc.signature)
		}
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
