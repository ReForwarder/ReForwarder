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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
)

// ---------- in-process MX / SMTP server harness ----------
// Pattern taken from internal/services/email/smtp_sender_timeout_test.go: a
// real TCP server the client talks to, with scripted replies and recorded
// commands, so the session runner is exercised end to end in-process.

// mxServer is a scriptable ESMTP server for one test.
type mxServer struct {
	t     *testing.T
	ln    net.Listener
	cert  *tls.Certificate // nil when TLS is not supported
	caps  []string         // EHLO capability lines (e.g. "STARTTLS", "8BITMIME")
	helo  string           // server name in the greeting/banners
	repls map[string]string
	// firstMailReply, when set, is answered once to the server's first MAIL
	// FROM (simulating a transient throttle), later sessions succeed.
	firstMailReply string

	mu       sync.Mutex
	commands []string
	bodies   []string
	tlsSeen  bool
	sessions int
	mailSeen int
}

func newMXServer(t *testing.T, opts ...func(*mxServer)) *mxServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mx server listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &mxServer{
		t:     t,
		ln:    ln,
		helo:  "mx.test",
		repls: map[string]string{},
	}
	for _, o := range opts {
		o(s)
	}
	go s.accept()
	return s
}

func withCaps(caps ...string) func(*mxServer) {
	return func(s *mxServer) { s.caps = append(s.caps, caps...) }
}

func withTLS() func(*mxServer) {
	return func(s *mxServer) {
		cert := testCertificate(s.t)
		s.cert = &cert
		s.caps = append(s.caps, "STARTTLS")
	}
}

func withReply(prefix, reply string) func(*mxServer) {
	return func(s *mxServer) { s.repls[strings.ToUpper(prefix)] = reply }
}

func withHeloName(name string) func(*mxServer) {
	return func(s *mxServer) { s.helo = name }
}

func (s *mxServer) addr() string { return s.ln.Addr().String() }

func (s *mxServer) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(c)
	}
}

func (s *mxServer) record(cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commands == nil {
		s.commands = []string{}
	}
	s.commands = append(s.commands, strings.ToUpper(strings.TrimSpace(cmd)))
}

func (s *mxServer) recordBody(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, body)
}

func (s *mxServer) sawTLS() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tlsSeen
}

func (s *mxServer) sessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

func (s *mxServer) commandsSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *mxServer) bodiesSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

func (s *mxServer) replyFor(cmd string) string {
	for prefix, reply := range s.repls {
		if strings.HasPrefix(cmd, prefix) {
			return reply
		}
	}
	switch {
	case strings.HasPrefix(cmd, "MAIL FROM"),
		strings.HasPrefix(cmd, "RCPT TO"),
		strings.HasPrefix(cmd, "RSET"),
		strings.HasPrefix(cmd, "NOOP"):
		return "250 ok"
	default:
		return "250 ok"
	}
}

// serve runs one session: banner, EHLO with capabilities, optional STARTTLS
// upgrade, then scripted replies. A per-connection deadline keeps a misbehaving
// client from hanging the test suite.
func (s *mxServer) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	s.mu.Lock()
	s.sessions++
	s.mu.Unlock()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))

	text := textproto.NewConn(c)
	defer func() { _ = text.Close() }()
	_ = text.PrintfLine("220 %s ESMTP", s.helo)

	for {
		line, err := text.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		s.record(cmd)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			_ = text.PrintfLine("250-%s hello", s.helo)
			for i, capability := range s.caps {
				if i == len(s.caps)-1 {
					_ = text.PrintfLine("250 %s", capability)
				} else {
					_ = text.PrintfLine("250-%s", capability)
				}
			}
			if len(s.caps) == 0 {
				_ = text.PrintfLine("250 OK")
			}
		case strings.HasPrefix(cmd, "STARTTLS"):
			if s.cert == nil {
				_ = text.PrintfLine("454 TLS not available")
				continue
			}
			_ = text.PrintfLine("220 go")
			tlsConn := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*s.cert}})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			s.mu.Lock()
			s.tlsSeen = true
			s.mu.Unlock()
			text = textproto.NewConn(tlsConn)
		case cmd == "DATA":
			_ = text.PrintfLine("354 go ahead")
			var body []string
			for {
				l, err := text.ReadLine()
				if err != nil {
					return
				}
				if l == "." {
					break
				}
				body = append(body, l)
			}
			s.recordBody(strings.Join(body, "\n"))
			_ = text.PrintfLine("%s", s.replyOr("DATA-END", "250 accepted"))
		case strings.HasPrefix(cmd, "MAIL FROM"):
			s.mu.Lock()
			s.mailSeen++
			first := s.mailSeen == 1
			s.mu.Unlock()
			if first && s.firstMailReply != "" {
				_ = text.PrintfLine("%s", s.firstMailReply)
				continue
			}
			_ = text.PrintfLine("%s", s.replyOr(cmd, s.replyFor(cmd)))
		case strings.HasPrefix(cmd, "QUIT"):
			_ = text.PrintfLine("221 bye")
			return
		default:
			_ = text.PrintfLine("%s", s.replyOr(cmd, s.replyFor(cmd)))
		}
	}
}

func (s *mxServer) replyOr(key, def string) string {
	if r, ok := s.repls[key]; ok {
		return r
	}
	return def
}

// testCertificate generates a self-signed certificate for mx.test; the client
// side trusts it through a RootCAs pool (verification stays on).
func testCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mx.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		DNSNames:              []string{"mx.test"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("tls key pair: %v", err)
	}
	return cert
}

// clientTLSConfig trusts the mx.test certificate (verification enabled).
func clientTLSConfig(t *testing.T, cert *tls.Certificate) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	pool.AddCert(leaf)
	return &tls.Config{ServerName: "mx.test", RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// ---------- fakes ----------

type fakeAttempts struct {
	mu   sync.Mutex
	rows []models.MTADeliveryAttempt
	next int
}

func newFakeAttempts() *fakeAttempts { return &fakeAttempts{next: 1} }

func (f *fakeAttempts) RecordAttempt(a models.MTADeliveryAttempt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, a)
	return nil
}

func (f *fakeAttempts) NextAttemptNumber(uint) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.next
	f.next++
	return n
}

func (f *fakeAttempts) all() []models.MTADeliveryAttempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]models.MTADeliveryAttempt(nil), f.rows...)
}

func (f *fakeAttempts) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

// restoreDialTimeout snapshots the process-wide dial timeout so a test can
// shrink it without leaking the change into other tests.
func restoreDialTimeout(t *testing.T) {
	t.Helper()
	prev := email.DialTimeout()
	t.Cleanup(func() { email.SetDialTimeout(prev) })
}
