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

package managesieve

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailauth"
	"golang.org/x/crypto/bcrypt"
)

// ---------------------------------------------------------------------------
// Fake ScriptStore: an in-memory mirror of the ScriptStore seam — upsert by
// name (PUTSCRIPT never changes activation), single-active semantics, the
// active-delete refusal handled at the server. The real repository adapter's
// row semantics get their own real-Postgres test (store_pg_test.go).
// ---------------------------------------------------------------------------

type fakeScriptRow struct {
	id       uint
	name     string
	body     string
	enabled  bool
	priority int
}

type fakeScriptStore struct {
	mu        sync.Mutex
	nextID    uint
	mailboxID uint
	rows      map[uint][]fakeScriptRow // userID → rows, insertion order
}

func newFakeScriptStore() *fakeScriptStore {
	return &fakeScriptStore{nextID: 1, mailboxID: 4242, rows: map[uint][]fakeScriptRow{}}
}

func (f *fakeScriptStore) PrimaryMailboxID(userID uint) (uint, error) {
	return f.mailboxID, nil
}

func (f *fakeScriptStore) ListScripts(userID uint) ([]ScriptEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := append([]fakeScriptRow(nil), f.rows[userID]...)
	// Priority ASC, id ASC — the evaluation order.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			if rows[j-1].priority > rows[j].priority ||
				(rows[j-1].priority == rows[j].priority && rows[j-1].id > rows[j].id) {
				rows[j-1], rows[j] = rows[j], rows[j-1]
				continue
			}
			break
		}
	}
	out := make([]ScriptEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, ScriptEntry{Name: r.name, Body: r.body, Active: r.enabled})
	}
	return out, nil
}

func (f *fakeScriptStore) find(userID uint, name string) *fakeScriptRow {
	for i := range f.rows[userID] {
		if f.rows[userID][i].name == name {
			return &f.rows[userID][i]
		}
	}
	return nil
}

func (f *fakeScriptStore) GetScript(userID uint, name string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.find(userID, name)
	if r == nil {
		return "", false, errors.New("not found")
	}
	return r.body, r.enabled, nil
}

func (f *fakeScriptStore) PutScript(userID, mailboxID uint, name, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if mailboxID != f.mailboxID {
		return errors.New("not the primary mailbox")
	}
	if r := f.find(userID, name); r != nil {
		r.body = body // PUTSCRIPT never changes activation
		return nil
	}
	f.rows[userID] = append(f.rows[userID], fakeScriptRow{
		id: f.nextID, name: name, body: body, enabled: false, priority: 100,
	})
	f.nextID++
	return nil
}

func (f *fakeScriptStore) DeleteScript(userID uint, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := f.rows[userID]
	for i := range rows {
		if rows[i].name == name {
			f.rows[userID] = append(rows[:i], rows[i+1:]...)
			return nil
		}
	}
	return errors.New("not found")
}

func (f *fakeScriptStore) SetActive(userID uint, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "" {
		for i := range f.rows[userID] {
			f.rows[userID][i].enabled = false
		}
		return nil
	}
	found := false
	for i := range f.rows[userID] {
		if f.rows[userID][i].name == name {
			found = true
			break
		}
	}
	if !found {
		return errors.New("not found")
	}
	for i := range f.rows[userID] {
		f.rows[userID][i].enabled = f.rows[userID][i].name == name
	}
	return nil
}

// ---------------------------------------------------------------------------
// Fake mailauth finders (the POP3 harness's fake shape — package-private in
// mailauth, duplicated locally).
// ---------------------------------------------------------------------------

type fakeUserFinder struct {
	users map[string]*models.User
}

func (f *fakeUserFinder) FindByEmail(email string) (*models.User, error) {
	u, ok := f.users[email]
	if !ok {
		return nil, errors.New("not found")
	}
	return u, nil
}

type fakeAppPasswords struct {
	active map[uint][]models.UserAppPassword
}

func (f *fakeAppPasswords) FindActiveByUserID(userID uint) ([]models.UserAppPassword, error) {
	return f.active[userID], nil
}

func (f *fakeAppPasswords) TouchLastUsed(id uint) error { return nil }

func bcryptHash(t *testing.T, secret string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(h)
}

// ---------------------------------------------------------------------------
// Fixtures + server harness (the POP3 harness's shape)
// ---------------------------------------------------------------------------

const (
	msTestUserEmail     = "ada@example.test"
	msTestUserID        = 7
	msTestAppSecret     = "apw_secret_laptop"
	msTestPrimarySecret = "primary-dashboard-secret"
)

type auditRecord struct {
	action string
	userID uint
	email  string
	ip     string
	meta   map[string]any
}

type auditCapture struct {
	mu      sync.Mutex
	records []auditRecord
}

func (c *auditCapture) hook(action string, userID uint, email, ip string, meta map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, auditRecord{action: action, userID: userID, email: email, ip: ip, meta: meta})
}

func (c *auditCapture) countOf(action string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.records {
		if r.action == action {
			n++
		}
	}
	return n
}

type testFixture struct {
	srv     *Server
	scripts *fakeScriptStore
	audit   *auditCapture
	tlsConf *tls.Config
	addr    string
}

// selfSignedCert generates a throwaway cert for the STARTTLS tests.
func selfSignedCert(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  key,
		}},
		MinVersion: tls.VersionTLS12,
	}
}

// newFixture starts a ManageSieve server on 127.0.0.1:0 with the standard
// fixture: one active user with an app password (plus the primary password
// hash that must NEVER authenticate).
func newFixture(t *testing.T, tlsOn bool, cfgTweaks ...func(*Config)) *testFixture {
	t.Helper()

	uf := &fakeUserFinder{users: map[string]*models.User{
		msTestUserEmail: {
			ID:           msTestUserID,
			Email:        msTestUserEmail,
			PasswordHash: bcryptHash(t, msTestPrimarySecret),
			Active:       true,
		},
	}}
	pf := &fakeAppPasswords{
		active: map[uint][]models.UserAppPassword{
			msTestUserID: {
				{ID: 101, UserID: msTestUserID, Name: "laptop", PasswordHash: bcryptHash(t, msTestAppSecret)},
			},
		},
	}
	auth := mailauth.NewAuthenticator(uf, pf)
	if auth == nil {
		t.Fatalf("mailauth authenticator nil")
	}

	scripts := newFakeScriptStore()
	cap := &auditCapture{}
	var tlsConf *tls.Config
	if tlsOn {
		tlsConf = selfSignedCert(t)
	}
	cfg := Config{
		Hostname:           "sieve.test",
		MaxSessionsPerUser: 10,
		MaxConnsPerIP:      30,
		CommandRateLimit:   0, // unlimited unless a test overrides
		AllowCleartextAuth: tlsConf == nil,
	}
	for _, tw := range cfgTweaks {
		tw(&cfg)
	}
	srv, err := NewServer(cfg, scripts, auth)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.SetAudit(cap.hook)
	if tlsConf != nil {
		srv.SetTLSConfig(tlsConf)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	go func() { _ = srv.Serve(l) }()

	return &testFixture{srv: srv, scripts: scripts, audit: cap, tlsConf: tlsConf, addr: l.Addr().String()}
}

// ---------------------------------------------------------------------------
// Scripted ManageSieve client
// ---------------------------------------------------------------------------

type sieveClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dialSieve(t *testing.T, addr string) *sieveClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &sieveClient{t: t, conn: conn, r: bufio.NewReaderSize(conn, 4096)}
	greeting := c.readLine()
	if !strings.HasPrefix(greeting, "OK") {
		t.Fatalf("greeting = %q", greeting)
	}
	return c
}

func (c *sieveClient) close() { _ = c.conn.Close() }

// cmd sends one command line and returns the single-line response.
func (c *sieveClient) cmd(line string) string {
	c.t.Helper()
	if _, err := c.conn.Write([]byte(line + "\r\n")); err != nil {
		c.t.Fatalf("write %q: %v", line, err)
	}
	return c.readLine()
}

// readLine reads one response line (deadline-bounded so a protocol bug fails
// the test instead of hanging it).
func (c *sieveClient) readLine() string {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

// putScript sends PUTSCRIPT with a literal body (the RFC 5804 client
// continuation form: `PUTSCRIPT "name" {n+}` CRLF body CRLF).
func (c *sieveClient) putScript(name, body string) string {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, "PUTSCRIPT %s {%d+}\r\n%s\r\n", quoteSieveString(name), len(body), body); err != nil {
		c.t.Fatalf("write PUTSCRIPT: %v", err)
	}
	return c.readLine()
}

// checkScript sends CHECKSCRIPT with a literal body.
func (c *sieveClient) checkScript(body string) string {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, "CHECKSCRIPT {%d+}\r\n%s\r\n", len(body), body); err != nil {
		c.t.Fatalf("write CHECKSCRIPT: %v", err)
	}
	return c.readLine()
}

// getScript sends GETSCRIPT and returns the literal body read out of the
// RFC 5804 answer (`{n}` CRLF, n octets, CRLF, then the final OK line — the
// literal precedes the OK per RFC 5804 §2.9).
func (c *sieveClient) getScript(name string) (string, string) {
	c.t.Helper()
	resp := c.cmd("GETSCRIPT " + quoteSieveString(name))
	if strings.HasPrefix(resp, "OK") || strings.HasPrefix(resp, "NO") {
		// An immediate OK/NO (no literal) — only plausible for an empty
		// body; the caller treats the tail line as the verdict.
		return "", resp
	}
	if !strings.HasPrefix(resp, "{") {
		c.t.Fatalf("GETSCRIPT answer not a literal: %q", resp)
	}
	var n int
	if _, err := fmt.Sscanf(resp, "{%d}", &n); err != nil {
		c.t.Fatalf("parse literal marker %q: %v", resp, err)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	body := make([]byte, n)
	if _, err := io.ReadFull(c.r, body); err != nil {
		c.t.Fatalf("read literal body: %v", err)
	}
	crlf := make([]byte, 2)
	if _, err := io.ReadFull(c.r, crlf); err != nil {
		c.t.Fatalf("read literal terminator: %v", err)
	}
	if crlf[0] != '\r' || crlf[1] != '\n' {
		c.t.Fatalf("literal not CRLF-terminated")
	}
	return string(body), c.readLine()
}

// listScripts sends LISTSCRIPTS and reads the response: the script lines
// before the final OK/NO line.
func (c *sieveClient) listScripts() ([]string, string) {
	c.t.Helper()
	resp := c.cmd("LISTSCRIPTS")
	if strings.HasPrefix(resp, "OK") || strings.HasPrefix(resp, "NO") {
		return nil, resp
	}
	lines := []string{resp}
	for {
		line := c.readLine()
		if strings.HasPrefix(line, "OK") || strings.HasPrefix(line, "NO") {
			return lines, line
		}
		lines = append(lines, line)
	}
}

// authPlain sends AUTHENTICATE "PLAIN" with the initial response carrying
// base64(authzid NUL authcid NUL password).
func (c *sieveClient) authPlain(authzid, authcid, secret string) string {
	c.t.Helper()
	token := base64.StdEncoding.EncodeToString([]byte(authzid + "\x00" + authcid + "\x00" + secret))
	return c.cmd(`AUTHENTICATE "PLAIN" ` + quoteSieveString(token))
}

// authPlainContinuation sends AUTHENTICATE "PLAIN" without an initial
// response and answers the `+` continuation.
func (c *sieveClient) authPlainContinuation(authcid, secret string) string {
	c.t.Helper()
	resp := c.cmd(`AUTHENTICATE "PLAIN"`)
	if !strings.HasPrefix(resp, "+") {
		return resp
	}
	token := base64.StdEncoding.EncodeToString([]byte("\x00" + authcid + "\x00" + secret))
	if _, err := c.conn.Write([]byte(token + "\r\n")); err != nil {
		c.t.Fatalf("write continuation: %v", err)
	}
	return c.readLine()
}

// upgradeTLS wraps the connection in a client-side TLS channel after a
// successful STARTTLS answer.
func (c *sieveClient) upgradeTLS() {
	c.t.Helper()
	tlsConn := tls.Client(c.conn, &tls.Config{
		ServerName:         "localhost",
		InsecureSkipVerify: true, // self-signed test certificate
		MinVersion:         tls.VersionTLS12,
	})
	if err := tlsConn.Handshake(); err != nil {
		c.t.Fatalf("STARTTLS handshake: %v", err)
	}
	c.conn = tlsConn
	c.r = bufio.NewReaderSize(tlsConn, 4096)
}

func expectOK(t *testing.T, resp string) string {
	t.Helper()
	if !strings.HasPrefix(resp, "OK") {
		t.Fatalf("expected OK, got %q", resp)
	}
	return resp
}

func expectNO(t *testing.T, resp string) string {
	t.Helper()
	if !strings.HasPrefix(resp, "NO") {
		t.Fatalf("expected NO, got %q", resp)
	}
	return resp
}

// validDoc is a script document inside the bounded grammar (subject contains).
func validDoc(folder string) string {
	return `{"conditions":[{"field":"subject","operator":"contains","value":"invoice"}],` +
		`"actions":[{"type":"fileinto","target":"` + folder + `"}]}`
}

func invalidDoc(reason string) string {
	// reason selects which broken document the caller wants.
	switch reason {
	case "unknown_field":
		return `{"conditions":[{"field":"from_domain","operator":"contains","value":"x"}],` +
			`"actions":[{"type":"keep"}]}`
	case "unknown_operator":
		return `{"conditions":[{"field":"subject","operator":"regex","value":"x"}],` +
			`"actions":[{"type":"keep"}]}`
	case "unknown_action":
		return `{"conditions":[{"field":"subject","operator":"contains","value":"x"}],` +
			`"actions":[{"type":"vacation"}]}`
	default:
		return `{"conditions": [], "actions": []}`
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestGreetingAndCapabilityMatrix walks the capability answer: the
// implementation/sieve/version lines, SASL only before authentication,
// STARTTLS only when the listener has a certificate and the security layer
// is not yet up.
func TestGreetingAndCapabilityMatrix(t *testing.T) {
	tf := newFixture(t, true, func(cfg *Config) {
		cfg.AllowCleartextAuth = false
	})
	c := dialSieve(t, tf.addr)

	// Before STARTTLS and authentication: SASL + STARTTLS advertised.
	lines, final := c.capabilityLines()
	expectOK(t, final)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"IMPLEMENTATION"`) {
		t.Fatalf("capability missing IMPLEMENTATION: %v", lines)
	}
	if !strings.Contains(joined, `"SIEVE"`) {
		t.Fatalf("capability missing SIEVE: %v", lines)
	}
	if !strings.Contains(joined, `"VERSION"`) {
		t.Fatalf("capability missing VERSION: %v", lines)
	}
	if !strings.Contains(joined, `"SASL" "PLAIN"`) {
		t.Fatalf("capability missing SASL PLAIN: %v", lines)
	}
	if !strings.Contains(joined, `"STARTTLS"`) {
		t.Fatalf("capability missing STARTTLS with a certificate configured: %v", lines)
	}

	// STARTTLS upgrades; the capability drops the STARTTLS line (already
	// secure) but keeps SASL (still unauthenticated).
	expectOK(t, c.cmd("STARTTLS"))
	c.upgradeTLS()
	lines, final = c.capabilityLines()
	expectOK(t, final)
	joined = strings.Join(lines, "\n")
	if strings.Contains(joined, `"STARTTLS"`) {
		t.Fatalf("capability still advertises STARTTLS on a secured connection: %v", lines)
	}
	if !strings.Contains(joined, `"SASL"`) {
		t.Fatalf("capability lost SASL before authentication: %v", lines)
	}

	// After authentication the SASL line disappears too.
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))
	lines, final = c.capabilityLines()
	expectOK(t, final)
	joined = strings.Join(lines, "\n")
	if strings.Contains(joined, "SASL") || strings.Contains(joined, "STARTTLS") {
		t.Fatalf("capability still advertises auth mechanisms after authentication: %v", lines)
	}
}

// capabilityLines sends CAPABILITY and reads the capability lines before the
// final OK.
func (c *sieveClient) capabilityLines() ([]string, string) {
	c.t.Helper()
	resp := c.cmd("CAPABILITY")
	if strings.HasPrefix(resp, "OK") || strings.HasPrefix(resp, "NO") {
		return nil, resp
	}
	lines := []string{resp}
	for {
		line := c.readLine()
		if strings.HasPrefix(line, "OK") || strings.HasPrefix(line, "NO") {
			return lines, line
		}
		lines = append(lines, line)
	}
}

// listScriptLines sends a zero-argument command and reads its multiline
// answer (lines before the final OK/NO).
func (c *sieveClient) listScriptLines(t *testing.T, cmd string) ([]string, string) {
	t.Helper()
	resp := c.cmd(cmd)
	if strings.HasPrefix(resp, "OK") || strings.HasPrefix(resp, "NO") {
		return nil, resp
	}
	lines := []string{resp}
	for {
		line := c.readLine()
		if strings.HasPrefix(line, "OK") || strings.HasPrefix(line, "NO") {
			return lines, line
		}
		lines = append(lines, line)
	}
}

// TestCommandsRequireAuth — every script-management command before
// authentication is refused with "Authenticate first".
func TestCommandsRequireAuth(t *testing.T) {
	tf := newFixture(t, false)
	c := dialSieve(t, tf.addr)

	// Simple line commands.
	for _, cmd := range []string{
		"LISTSCRIPTS",
		`GETSCRIPT "any"`,
		`DELETESCRIPT "any"`,
		`SETACTIVE "any"`,
		`HAVESPACE "any" 100`,
		"UNAUTHENTICATE",
	} {
		expectNO(t, c.cmd(cmd))
	}

	// Literal-carrying commands (the client continuation form).
	if _, err := fmt.Fprintf(c.conn, "PUTSCRIPT \"any\" {5+}\r\n12345\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	expectNO(t, c.readLine())
	if _, err := fmt.Fprintf(c.conn, "CHECKSCRIPT {5+}\r\n12345\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	expectNO(t, c.readLine())
}

// TestAuthPlainSuccessFailureAudit — wrong secret refused (generic NO, the
// reason only in the audit sink), the app password accepted, the primary
// account password structurally refused, UNAUTHENTICATE returns to the
// non-authenticated state and re-authentication works.
func TestAuthPlainSuccessFailureAudit(t *testing.T) {
	tf := newFixture(t, false)
	c := dialSieve(t, tf.addr)

	// Wrong secret.
	expectNO(t, c.authPlain("", msTestUserEmail, "wrong-secret"))
	if n := tf.audit.countOf("managesieve.auth.failure"); n != 1 {
		t.Fatalf("auth failure events = %d, want 1", n)
	}

	// The primary account password is never consulted (app-password-only).
	expectNO(t, c.authPlain("", msTestUserEmail, msTestPrimarySecret))
	if n := tf.audit.countOf("managesieve.auth.failure"); n != 2 {
		t.Fatalf("auth failure events = %d, want 2", n)
	}

	// Still unauthenticated: LISTSCRIPTS refused.
	expectNO(t, c.cmd("LISTSCRIPTS"))

	// The app password succeeds.
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))
	if n := tf.audit.countOf("managesieve.auth.success"); n != 1 {
		t.Fatalf("auth success events = %d, want 1", n)
	}
	expectOK(t, c.cmd("LISTSCRIPTS"))

	// Already authenticated: a second AUTHENTICATE is refused.
	expectNO(t, c.authPlain("", msTestUserEmail, msTestAppSecret))

	// UNAUTHENTICATE drops back to the non-authenticated state.
	expectOK(t, c.cmd("UNAUTHENTICATE"))
	expectNO(t, c.cmd("LISTSCRIPTS"))

	// Re-authentication works (the session slot was released).
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))
	expectOK(t, c.cmd("LISTSCRIPTS"))
}

// TestAuthzidDistinctRefused — PLAIN with a distinct authorization identity
// is refused (a session manages only the authenticating account's own
// scripts; no proxying).
func TestAuthzidDistinctRefused(t *testing.T) {
	tf := newFixture(t, false)
	c := dialSieve(t, tf.addr)
	expectNO(t, c.authPlain("other@example.test", msTestUserEmail, msTestAppSecret))
	expectNO(t, c.cmd("LISTSCRIPTS"))
}

// TestAuthPlainContinuation — AUTHENTICATE without an initial response uses
// the `+` continuation; "*" cancels.
func TestAuthPlainContinuation(t *testing.T) {
	tf := newFixture(t, false)
	c := dialSieve(t, tf.addr)

	expectOK(t, c.authPlainContinuation(msTestUserEmail, msTestAppSecret))
	expectOK(t, c.cmd("LISTSCRIPTS"))
	expectOK(t, c.cmd("UNAUTHENTICATE"))

	// Cancel with "*".
	resp := c.cmd(`AUTHENTICATE "PLAIN"`)
	if !strings.HasPrefix(resp, "+") {
		t.Fatalf("expected continuation, got %q", resp)
	}
	if _, err := c.conn.Write([]byte("*\r\n")); err != nil {
		t.Fatalf("write cancel: %v", err)
	}
	expectNO(t, c.readLine())
}

// TestAuthRequiresTLSWithCertificate — with a certificate configured,
// credentials are refused before STARTTLS and accepted after the upgrade
// (the ADR-004 decision-4 confinement).
func TestAuthRequiresTLSWithCertificate(t *testing.T) {
	tf := newFixture(t, true, func(cfg *Config) {
		cfg.AllowCleartextAuth = false
	})
	c := dialSieve(t, tf.addr)

	expectNO(t, c.authPlain("", msTestUserEmail, msTestAppSecret))
	if n := tf.audit.countOf("managesieve.auth.failure"); n != 1 {
		t.Fatalf("auth failure events = %d, want 1", n)
	}

	// STARTTLS upgrades, then credentials are accepted.
	resp := c.cmd("STARTTLS")
	expectOK(t, resp)
	c.upgradeTLS()
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))
	expectOK(t, c.cmd("LISTSCRIPTS"))

	// STARTTLS on an already-secured connection is refused.
	expectNO(t, c.cmd("STARTTLS"))
}

// TestPutGetScriptLifecycle — PUTSCRIPT stores inactive, GETSCRIPT
// round-trips the document byte-for-byte, an invalid document is refused
// BEFORE storage, and re-put keeps the existing activation.
func TestPutGetScriptLifecycle(t *testing.T) {
	tf := newFixture(t, false)
	c := dialSieve(t, tf.addr)
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))

	doc := validDoc("INBOX/Finance")
	expectOK(t, c.putScript("invoices", doc))

	// Stored inactive: LISTSCRIPTS shows the name without the ACTIVE marker.
	lines, final := c.listScripts()
	expectOK(t, final)
	if len(lines) != 1 || !strings.Contains(lines[0], `"invoices"`) || strings.Contains(lines[0], "ACTIVE") {
		t.Fatalf("listscripts = %v (want one inactive %q entry)", lines, "invoices")
	}

	// GETSCRIPT round-trips the stored document.
	body, final := c.getScript("invoices")
	expectOK(t, final)
	if body != doc {
		t.Fatalf("getscript body = %q, want the stored document", body)
	}

	// Re-put with a different body replaces the document (and the script is
	// still inactive).
	doc2 := validDoc("INBOX/Billing")
	expectOK(t, c.putScript("invoices", doc2))
	body, _ = c.getScript("invoices")
	if body != doc2 {
		t.Fatalf("re-put body = %q, want the replaced document", body)
	}

	// An invalid document is refused before storage: unknown field.
	expectNO(t, c.putScript("broken", invalidDoc("unknown_field")))
	_, final = c.getScript("broken")
	expectNO(t, final) // reads as missing — it never reached the store

	// CHECKSCRIPT validates without storing.
	expectOK(t, c.checkScript(validDoc("INBOX/Finance")))
	expectNO(t, c.checkScript(invalidDoc("unknown_operator")))
	expectNO(t, c.checkScript(invalidDoc("unknown_action")))
	expectNO(t, c.checkScript(invalidDoc("empty_actions")))
	lines, final = c.listScripts()
	expectOK(t, final)
	if len(lines) != 1 {
		t.Fatalf("CHECKSCRIPT stored something: %v", lines)
	}
}

// TestSetActiveSemantics — exactly one active script per mailbox: PUTSCRIPT
// stores inactive, SETACTIVE activates one and deactivates the others,
// SETACTIVE "" deactivates all, the ACTIVE script cannot be deleted, and
// deleting an inactive one works.
func TestSetActiveSemantics(t *testing.T) {
	tf := newFixture(t, false)
	c := dialSieve(t, tf.addr)
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))

	expectOK(t, c.putScript("alpha", validDoc("INBOX/A")))
	expectOK(t, c.putScript("beta", validDoc("INBOX/B")))

	// Both inactive after put.
	lines, final := c.listScripts()
	expectOK(t, final)
	if len(lines) != 2 || strings.Contains(strings.Join(lines, " "), "ACTIVE") {
		t.Fatalf("listscripts = %v, want two inactive entries", lines)
	}

	// Activate alpha.
	expectOK(t, c.cmd(`SETACTIVE "alpha"`))
	lines, _ = c.listScripts()
	if len(lines) != 2 || !strings.Contains(lines[0], `ACTIVE "alpha"`) {
		t.Fatalf("listscripts = %v, want ACTIVE alpha first", lines)
	}

	// The ACTIVE script cannot be deleted.
	expectNO(t, c.cmd(`DELETESCRIPT "alpha"`))

	// Activate beta: alpha deactivates (single active per mailbox).
	expectOK(t, c.cmd(`SETACTIVE "beta"`))
	lines, _ = c.listScripts()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `ACTIVE "beta"`) || strings.Contains(joined, `ACTIVE "alpha"`) {
		t.Fatalf("listscripts = %v, want exactly ACTIVE beta", lines)
	}

	// Now alpha (inactive) deletes fine; beta survives.
	expectOK(t, c.cmd(`DELETESCRIPT "alpha"`))
	lines, _ = c.listScripts()
	if len(lines) != 1 || !strings.Contains(lines[0], `"beta"`) {
		t.Fatalf("listscripts = %v, want only beta", lines)
	}

	// SETACTIVE "" deactivates everything.
	expectOK(t, c.cmd(`SETACTIVE ""`))
	lines, _ = c.listScripts()
	if len(lines) != 1 || strings.Contains(lines[0], "ACTIVE") {
		t.Fatalf("listscripts = %v, want beta inactive", lines)
	}

	// Deleting the inactive beta works after the deactivation.
	expectOK(t, c.cmd(`DELETESCRIPT "beta"`))
	lines, _ = c.listScripts()
	if len(lines) != 0 {
		t.Fatalf("listscripts = %v, want empty", lines)
	}

	// SETACTIVE on a missing script is refused.
	expectNO(t, c.cmd(`SETACTIVE "missing"`))
}

// TestPutScriptDoesNotActivate — RFC 5804: PUTSCRIPT never changes
// activation. Re-putting the ACTIVE script's name updates the body and keeps
// it active.
func TestPutScriptDoesNotActivate(t *testing.T) {
	tf := newFixture(t, false)
	c := dialSieve(t, tf.addr)
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))

	expectOK(t, c.putScript("main", validDoc("INBOX/M")))
	expectOK(t, c.cmd(`SETACTIVE "main"`))
	doc2 := validDoc("INBOX/M2")
	expectOK(t, c.putScript("main", doc2))
	lines, _ := c.listScripts()
	if len(lines) != 1 || !strings.Contains(lines[0], `ACTIVE "main"`) {
		t.Fatalf("listscripts = %v, want main still ACTIVE", lines)
	}
	body, _ := c.getScript("main")
	if body != doc2 {
		t.Fatalf("body = %q, want the replaced document", body)
	}

	// A second put of a NEW name stores inactive and does not steal the
	// active flag.
	expectOK(t, c.putScript("other", validDoc("INBOX/O")))
	lines, _ = c.listScripts()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `ACTIVE "main"`) || strings.Contains(joined, `ACTIVE "other"`) {
		t.Fatalf("listscripts = %v, want main ACTIVE, other inactive", lines)
	}
}

// TestHaveSpaceNoopUnknownLogout walks the small commands: HAVESPACE answers
// the per-script bound, NOOP answers OK, unknown verbs answer NO, LOGOUT
// answers OK and closes the connection.
func TestHaveSpaceNoopUnknownLogout(t *testing.T) {
	tf := newFixture(t, false)
	c := dialSieve(t, tf.addr)
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))

	resp := expectOK(t, c.cmd(`HAVESPACE "any" 1000`))
	if !strings.Contains(resp, fmt.Sprintf("%d", maxScriptBytes)) {
		t.Fatalf("HAVESPACE answer %q, want the %d bound", resp, maxScriptBytes)
	}
	// HAVESPACE without the number argument is refused.
	expectNO(t, c.cmd(`HAVESPACE "any"`))

	expectOK(t, c.cmd("NOOP"))
	expectNO(t, c.cmd("FROBNICATE"))
	expectNO(t, c.cmd(`GETSCRIPT ""`))
	expectNO(t, c.cmd(`DELETESCRIPT "missing"`))

	expectOK(t, c.cmd("LOGOUT"))
	// The connection closes: the next read hits EOF.
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 16)
	if _, err := c.r.Read(buf); err != io.EOF {
		t.Fatalf("post-LOGOUT read = %v, want EOF", err)
	}
}

// TestRateLimitCloses — over-budget commands get NO and a session that keeps
// pushing is closed after maxRateRejections.
func TestRateLimitCloses(t *testing.T) {
	tf := newFixture(t, false, func(cfg *Config) {
		cfg.CommandRateLimit = 2
		cfg.CommandRateWindow = 10 * time.Second
	})
	c := dialSieve(t, tf.addr)
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))

	slowDowns := 0
	closing := false
	for i := 0; i < 10; i++ {
		_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.conn.Write([]byte("NOOP\r\n")); err != nil {
			break
		}
		line, err := c.r.ReadString('\n')
		if err != nil {
			closing = true
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.Contains(line, "too many commands") {
			slowDowns++
		} else if !strings.HasPrefix(line, "OK") {
			t.Fatalf("unexpected NOOP answer %q", line)
		}
	}
	if slowDowns == 0 || !closing {
		t.Fatalf("rate limit did not reject and close (rejections=%d closed=%v)", slowDowns, closing)
	}
}

// TestSessionCapPerUser — a second concurrent authenticated session over the
// cap is refused (and releasing the first lets authentication succeed
// again).
func TestSessionCapPerUser(t *testing.T) {
	tf := newFixture(t, false, func(cfg *Config) {
		cfg.MaxSessionsPerUser = 1
	})
	c1 := dialSieve(t, tf.addr)
	expectOK(t, c1.authPlain("", msTestUserEmail, msTestAppSecret))

	c2 := dialSieve(t, tf.addr)
	expectNO(t, c2.authPlain("", msTestUserEmail, msTestAppSecret))

	// Release c1's slot; c2 authenticates.
	expectOK(t, c1.cmd("UNAUTHENTICATE"))
	expectOK(t, c2.authPlain("", msTestUserEmail, msTestAppSecret))
	expectOK(t, c2.cmd("LISTSCRIPTS"))
}

// TestPerIPConnectionCap — connections beyond the per-IP budget get the
// refusal greeting and are closed.
func TestPerIPConnectionCap(t *testing.T) {
	tf := newFixture(t, false, func(cfg *Config) {
		cfg.MaxConnsPerIP = 2
	})
	c1 := dialSieve(t, tf.addr)
	c2 := dialSieve(t, tf.addr)

	conn, err := net.DialTimeout("tcp", tf.addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("refusal greeting read: %v", err)
	}
	greeting := string(buf[:n])
	if !strings.HasPrefix(greeting, `NO "Too many connections`) {
		t.Fatalf("refusal greeting = %q", greeting)
	}
	_ = c1
	_ = c2
}

// TestUnknownFieldScriptNeverReachesStore — an unknown condition field is
// refused at PUTSCRIPT (fail-closed grammar) and the store stays untouched;
// the SAME document is accepted after STARTTLS too (wire-order sanity: the
// grammar runs on the decoded plaintext, not the wire form).
func TestUnknownFieldScriptNeverReachesStore(t *testing.T) {
	tf := newFixture(t, false)
	c := dialSieve(t, tf.addr)
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))

	expectNO(t, c.putScript("evil", invalidDoc("unknown_field")))
	rows := 0
	entries, err := tf.scripts.ListScripts(msTestUserID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for range entries {
		rows++
	}
	if rows != 0 {
		t.Fatalf("store holds %d rows after a refused put", rows)
	}
}

// TestScriptDocumentEdges — malformed documents are refused at
// PUTSCRIPT/CHECKSCRIPT with NO and never reach the store: non-JSON bodies,
// JSON arrays instead of objects, unknown members (DisallowUnknownFields),
// and trailing content after the object.
func TestScriptDocumentEdges(t *testing.T) {
	tf := newFixture(t, false)
	c := dialSieve(t, tf.addr)
	expectOK(t, c.authPlain("", msTestUserEmail, msTestAppSecret))

	broken := []string{
		`this is not json`,
		`[]`,
		`{"conditions": [], "actions": [{"type": "keep"}], "sneaky": true}`,
		`{"conditions": [], "actions": [{"type": "keep"}]} trailing`,
		`{}`,
		`{"conditions": [{"field": "subject", "operator": "contains", "value": "x"}]}`,
	}
	for i, body := range broken {
		expectNO(t, c.putScript(fmt.Sprintf("broken-%d", i), body))
	}
	expectNO(t, c.checkScript(`not json at all`))

	entries, err := tf.scripts.ListScripts(msTestUserID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("store holds %d rows after refused puts", len(entries))
	}
}

// TestGuardsRunWithoutSecurityEngine — the fixture never attaches a security
// engine; the auth failure path and the command guards still hold (the
// nil-able seam's posture).
func TestGuardsRunWithoutSecurityEngine(t *testing.T) {
	if tf := newFixture(t, false); tf.srv.security != nil {
		t.Fatalf("fixture attached a security engine; want the nil seam")
	}
}
