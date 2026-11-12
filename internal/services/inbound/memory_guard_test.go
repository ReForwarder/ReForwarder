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
	"net"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
)

// SF-108: under a memory-budget breach the SMTP receiver refuses NEW
// sessions with a temporary 451 — backpressure at the front door, in-flight
// sessions never aborted. The same seam backs the MSA, LMTP and relay
// backends (one shared interface, one setter convention).

type fakeGuard struct{ pressure bool }

func (f *fakeGuard) UnderPressure() bool { return f.pressure }

func guardServer(t *testing.T, b *Backend) string {
	t.Helper()
	srv, err := NewSMTPServer(b, SMTPConfig{
		Host: "127.0.0.1", Port: 0, Hostname: "mx.test", MaxMessageSize: 1 << 20,
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func TestBackendRefusesNewSessionsUnderMemoryPressure(t *testing.T) {
	guard := &fakeGuard{pressure: true}
	b := &Backend{maxMessageSize: 1 << 20}
	b.SetMemoryGuard(guard)
	addr := guardServer(t, b)

	// A fresh connection is rejected before any session state is built.
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := smtp.NewClient(conn)
	defer func() { _ = c.Close() }()
	if err := c.Hello("client.example.org"); err == nil {
		t.Fatal("a session under memory pressure must be refused")
	} else if smtpErr, ok := err.(*smtp.SMTPError); !ok || smtpErr.Code != 451 {
		t.Fatalf("err = %v, want a 451 temporary refusal", err)
	}
}

func TestBackendAcceptsSessionsWhenGuardHealthy(t *testing.T) {
	guard := &fakeGuard{pressure: false}
	b := &Backend{maxMessageSize: 1 << 20}
	b.SetMemoryGuard(guard)
	addr := guardServer(t, b)

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := smtp.NewClient(conn)
	defer func() { _ = c.Close() }()
	if err := c.Hello("client.example.org"); err != nil {
		t.Fatalf("a healthy guard must admit sessions: %v", err)
	}
}

func TestBackendNilGuardAdmitsSessions(t *testing.T) {
	// No watchdog configured (POSTA_MEMORY_BUDGET_MB unset): identical to the
	// pre-SF-108 behavior.
	b := &Backend{maxMessageSize: 1 << 20}
	addr := guardServer(t, b)

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := smtp.NewClient(conn)
	defer func() { _ = c.Close() }()
	if err := c.Hello("client.example.org"); err != nil {
		t.Fatalf("no guard must admit sessions: %v", err)
	}
}
