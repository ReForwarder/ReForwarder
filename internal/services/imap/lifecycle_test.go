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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/mailauth"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/security"
	"github.com/goposta/posta/internal/storage/repositories"
)

// fakeBlobStore satisfies blob.Store in memory; the lifecycle and audit
// tests never touch a real object (sessions greet, LOGOUT, or fail auth —
// none of which reads or writes bodies).
type fakeBlobStore struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (f *fakeBlobStore) Put(_ context.Context, key string, r io.Reader, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[key] = data
	return nil
}

func (f *fakeBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	data, ok := f.objs[key]
	f.mu.Unlock()
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

func (f *fakeBlobStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objs, key)
	return nil
}

// newLifecycleServer builds an IMAP server over the DryRun mailstore fixture
// with the limits the tests exercise, mirrors the cmd/posta listener stack
// (security.NewCountingListener in front of Serve) and starts serving on an
// ephemeral loopback port. Returns the server and its dial address.
func newLifecycleServer(t *testing.T, audit AuditFunc) (*Server, string) {
	t.Helper()
	gdb := dryGorm(t)
	ms, err := mailstore.New(gdb, mailstore.ZstdBlobCodec{}, &fakeBlobStore{objs: map[string][]byte{}}, nil)
	if err != nil {
		t.Fatalf("mailstore.New: %v", err)
	}
	auth := mailauth.NewAuthenticator(
		repositories.NewUserRepository(gdb),
		repositories.NewUserAppPasswordRepository(gdb),
	)
	if auth == nil {
		t.Fatal("authenticator could not be built")
	}
	srv := NewServer(Config{
		Hostname:              "localhost",
		MaxSessionsPerUser:    1,
		MaxConnsPerIP:         2,
		CommandRateLimit:      3,
		CommandRateWindowSecs: 60,
		AllowCleartextAuth:    true,
	}, ms, auth)
	if audit != nil {
		srv.SetAudit(audit)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	// The wiring wraps the listener with the security CountingListener; a nil
	// engine makes it a pure passthrough here (the engine itself is the
	// security package's test territory).
	go func() {
		_ = srv.Serve(security.NewCountingListener(ln, nil, portOfAddr(ln), "imap"))
	}()
	return srv, ln.Addr().String()
}

func portOfAddr(ln net.Listener) int {
	if ta, ok := ln.Addr().(*net.TCPAddr); ok {
		return ta.Port
	}
	return 0
}

// readGreeting reads the server's "* OK [CAPABILITY ...]" greeting.
func readGreeting(t *testing.T, conn net.Conn) string {
	t.Helper()
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	line := string(buf[:n])
	if !strings.HasPrefix(line, "* OK [CAPABILITY ") {
		t.Fatalf("greeting = %q, want '* OK [CAPABILITY ...'", line)
	}
	return line
}

// readReply reads lines until one with the given tag arrives, returning it.
func readReply(t *testing.T, conn net.Conn, tag string) string {
	t.Helper()
	buf := make([]byte, 4096)
	var acc []byte
	deadline := time.Now().Add(3 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for tag %q; got %q", tag, string(acc))
		}
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			for _, line := range strings.Split(string(acc), "\r\n") {
				if strings.HasPrefix(line, tag+" ") {
					return line
				}
			}
		}
		if err != nil {
			t.Fatalf("read reply for %q: %v (got %q)", tag, err, string(acc))
		}
	}
}

// TestIMAPServerGreetingAndLogout: the serving loop — a client connects,
// receives the IMAP4rev1 greeting, and LOGOUT answers with the tagged OK and
// a BYE before the connection closes (start → serve → clean session end).
func TestIMAPServerGreetingAndLogout(t *testing.T) {
	srv, addr := newLifecycleServer(t, nil)
	_ = srv
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	greeting := readGreeting(t, conn)
	if !strings.Contains(greeting, ServerID) {
		t.Fatalf("greeting %q does not advertise %q", greeting, ServerID)
	}
	if _, err := conn.Write([]byte("a1 LOGOUT\r\n")); err != nil {
		t.Fatalf("write LOGOUT: %v", err)
	}
	// Read until the tagged reply; the BYE may arrive before or with it.
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1024)
	var acc []byte
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			if strings.Contains(string(acc), "a1 OK") && strings.Contains(string(acc), "BYE") {
				break
			}
		}
		if err != nil {
			// The server closes the conn after LOGOUT; what we accumulated
			// must already contain both lines.
			break
		}
	}
	if !strings.Contains(string(acc), "a1 OK") || !strings.Contains(string(acc), "BYE") {
		t.Fatalf("LOGOUT exchange = %q, want tagged OK + BYE", string(acc))
	}
}

// TestIMAPServerCloseStopsAcceptAndDropsSessions: Close (the graceful
// shutdown registration in cmd/posta shutdownServer) closes the listener AND
// the live sessions — no half-open mail sessions outlive the process
// decision to stop.
func TestIMAPServerCloseStopsAcceptAndDropsSessions(t *testing.T) {
	srv, addr := newLifecycleServer(t, nil)

	live, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer live.Close()
	_ = live.SetDeadline(time.Now().Add(3 * time.Second))
	readGreeting(t, live)

	srv.Close()

	// The live session is dropped: reads fail promptly.
	_ = live.SetReadDeadline(time.Now().Add(1 * time.Second))
	if _, err := live.Read(make([]byte, 64)); err == nil {
		t.Fatal("live session survived Close; the server must drop live conns")
	}

	// The listener is closed: new dials are refused.
	if _, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		t.Fatal("dial after Close succeeded; the listener must be closed")
	}

	// Close is idempotent (shutdownServer calls it once, but the safety
	// contract holds under races with the accept loop).
	srv.Close()
}

// TestIMAPServerImplicitTLSListenerServesGreeting: the implicit-TLS wiring
// (CountingListener → TLSServerListener → Serve, exactly the stack cmd/posta
// composes for 993) serves the greeting over an established TLS conn, drops
// a plaintext probe without disturbing the accept loop, and keeps serving
// TLS clients afterwards.
func TestIMAPServerImplicitTLSListenerServesGreeting(t *testing.T) {
	gdb := dryGorm(t)
	ms, err := mailstore.New(gdb, mailstore.ZstdBlobCodec{}, &fakeBlobStore{objs: map[string][]byte{}}, nil)
	if err != nil {
		t.Fatalf("mailstore.New: %v", err)
	}
	auth := mailauth.NewAuthenticator(
		repositories.NewUserRepository(gdb),
		repositories.NewUserAppPasswordRepository(gdb),
	)
	srv := NewServer(Config{
		Hostname:           "localhost",
		MaxSessionsPerUser: 1,
		MaxConnsPerIP:      8,
		AllowCleartextAuth: true,
	}, ms, auth)
	t.Cleanup(func() { srv.Close() })

	certPath, keyPath := writeTestCert(t)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load test cert: %v", err)
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	counting := security.NewCountingListener(ln, nil, portOfAddr(ln), "imap")
	tlsListener := security.TLSServerListener(counting, tlsCfg, nil, portOfAddr(ln))
	go func() { _ = srv.Serve(tlsListener) }()

	// 1. TLS client: handshake + greeting.
	tlsConn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	defer tlsConn.Close()
	_ = tlsConn.SetDeadline(time.Now().Add(3 * time.Second))
	greeting := readGreeting(t, tlsConn)
	if !strings.Contains(greeting, ServerID) {
		t.Fatalf("greeting over TLS = %q", greeting)
	}

	// 2. Plaintext probe of the implicit-TLS port: dropped, no greeting.
	plain, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	defer plain.Close()
	_ = plain.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = plain.Write([]byte("a1 CAPABILITY\r\n"))
	if _, err := plain.Read(make([]byte, 64)); err == nil {
		t.Fatal("plaintext probe of the implicit-TLS port was answered; it must be dropped")
	}

	// 3. The accept loop survived: a fresh TLS client still gets a greeting.
	tlsConn2, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("TLS dial after plaintext probe: %v", err)
	}
	defer tlsConn2.Close()
	_ = tlsConn2.SetDeadline(time.Now().Add(3 * time.Second))
	readGreeting(t, tlsConn2)
}

// TestIMAPServerPerIPConnectionCap: the third concurrent connection from one
// IP is refused at accept with "* BYE too many connections" (ADR-004
// decision 5; the POSTA_IMAP_MAX_CONNS_PER_IP wiring).
func TestIMAPServerPerIPConnectionCap(t *testing.T) {
	srv, addr := newLifecycleServer(t, nil)
	_ = srv

	c1, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer c1.Close()
	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer c2.Close()
	_ = c1.SetDeadline(time.Now().Add(3 * time.Second))
	_ = c2.SetDeadline(time.Now().Add(3 * time.Second))
	readGreeting(t, c1)
	readGreeting(t, c2)

	c3, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial 3: %v", err)
	}
	defer c3.Close()
	_ = c3.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 256)
	var acc []byte
	for {
		n, err := c3.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			if strings.Contains(string(acc), "BYE too many connections") {
				break
			}
		}
		if err != nil {
			break
		}
	}
	if !strings.Contains(string(acc), "BYE too many connections") {
		t.Fatalf("over-budget dial read %q, want '* BYE too many connections'", string(acc))
	}
}

// TestIMAPServerPerUserSessionCap: registerSession/unregisterSession enforce
// the per-user concurrent cap (ADR-004 decision 5; the POSTA_IMAP_MAX_SESSIONS_PER_USER
// wiring) — the second concurrent session of one user is refused and the
// slot frees when the session ends.
func TestIMAPServerPerUserSessionCap(t *testing.T) {
	srv, _ := newLifecycleServer(t, nil)
	if !srv.registerSession(7) {
		t.Fatal("first registerSession refused")
	}
	if srv.registerSession(7) {
		t.Fatal("second registerSession must be refused at cap 1")
	}
	if got := srv.sessionsOfUser(7); got != 1 {
		t.Fatalf("sessionsOfUser = %d, want 1", got)
	}
	srv.unregisterSession(7)
	if !srv.registerSession(7) {
		t.Fatal("registerSession after unregister refused")
	}
	srv.unregisterSession(7)
	if got := srv.sessionsOfUser(7); got != 0 {
		t.Fatalf("sessionsOfUser after unregister = %d, want 0", got)
	}
	// Unregistering below zero must not happen.
	srv.unregisterSession(7)
	if got := srv.sessionsOfUser(7); got != 0 {
		t.Fatalf("sessionsOfUser after extra unregister = %d, want 0", got)
	}
}

// TestIMAPServerCommandRateLimit: the per-session command budget kills the
// session after the configured number of commands ("* BYE command rate limit
// exceeded"; the POSTA_IMAP_COMMAND_RATE_LIMIT/_WINDOW wiring).
func TestIMAPServerCommandRateLimit(t *testing.T) {
	srv, addr := newLifecycleServer(t, nil)
	_ = srv

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	readGreeting(t, conn)

	// Budget is 3: three NOOPs get tagged replies, the fourth command is
	// answered with the BYE and the session ends.
	var acc []byte
	buf := make([]byte, 1024)
	for i := 0; i < 4; i++ {
		if _, err := conn.Write([]byte("n" + string(rune('1'+i)) + " NOOP\r\n")); err != nil {
			t.Fatalf("write NOOP %d: %v", i, err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out; got %q", string(acc))
		}
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
		}
		if strings.Contains(string(acc), "BYE command rate limit exceeded") {
			break
		}
		if err != nil {
			break
		}
	}
	if !strings.Contains(string(acc), "BYE command rate limit exceeded") {
		t.Fatalf("rate-limited session read %q, want the BYE", string(acc))
	}
}

// writeTestCert generates a self-signed certificate for 127.0.0.1 into the
// test temp dir and returns the PEM file paths.
func writeTestCert(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}
