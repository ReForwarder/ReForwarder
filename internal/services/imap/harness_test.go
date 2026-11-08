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
	"bufio"
	"context"
	"io"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailauth"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/migration"
	"github.com/goposta/posta/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The scripted-client harness (the R-22 SMTP-harness pattern, ADR-004
// consequences): in-process TCP servers driven by line-oriented fake clients,
// covering the ADR-004 command matrix end to end. The full stack needs a real
// Postgres (the mailstore service is concrete and the fake driver lives
// inside the mailstore package's own tests), so the harness tests skip when
// TEST_DATABASE_DSN is unset — the named battery runs them with
// TEST_DATABASE_DSN set against postgres:17-alpine, exactly like the other
// PG-gated suites.

const testDSNEnv = "TEST_DATABASE_DSN"

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(testDSNEnv)
	if dsn == "" {
		t.Skipf("skipping: %s not set", testDSNEnv)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available (%s): %v", dsn, err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.UserAppPassword{}, &models.Mailbox{}, &models.MailFolder{}, &models.MailMessage{}, &models.MailBlobRef{}); err != nil {
		t.Skipf("skipping: cannot migrate test schema: %v", err)
	}
	if err := migration.EnsureMailstoreSearch(db); err != nil {
		t.Skipf("skipping: cannot ensure search schema: %v", err)
	}
	return db
}

// testRunSuffix keeps reruns against a shared database collision-free.
var testRunSuffix = fmt.Sprintf("imap%d", time.Now().UnixNano())

// auditRecord collects the audit events a test env emitted.
type auditRecord struct {
	mu     sync.Mutex
	events []auditEntry
}

type auditEntry struct {
	action string
	userID uint
	email  string
	ip     string
}

func (a *auditRecord) record(action string, userID uint, email, ip string, _ map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, auditEntry{action: action, userID: userID, email: email, ip: ip})
}

func (a *auditRecord) count(action string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, e := range a.events {
		if e.action == action {
			n++
		}
	}
	return n
}

// imapEnv is one fully wired test deployment: mailstore + mailauth + IMAP
// server on an in-process listener.
type imapEnv struct {
	t        *testing.T
	db       *gorm.DB
	store    *mailstore.Service
	server   *Server
	addr     string
	user     *models.User
	appPass  string
	mailbox  *models.Mailbox
	inbox    *models.MailFolder
	audits   *auditRecord
	cleanup  func()
}

// newIMAPEnv builds the env with default (strict-TLS) config.
func newIMAPEnv(t *testing.T) *imapEnv {
	return newIMAPEnvWith(t, imapConfigDefaults())
}

func imapConfigDefaults() Config {
	return Config{
		Hostname:              "imap.test",
		MaxSessionsPerUser:    10,
		MaxConnsPerIP:         64,
		CommandRateLimit:      0, // unlimited unless a test asks
		CommandRateWindowSecs: 60,
		AllowCleartextAuth:    false, // TLS enforced by default; tests opt in explicitly
		IdleTimeout:           2 * time.Minute,
	}
}

func newIMAPEnvWith(t *testing.T, cfg Config) *imapEnv {
	t.Helper()
	db := testDB(t)
	st, err := os.MkdirTemp("", "imap-blob-*")
	if err != nil {
		t.Fatalf("temp blob dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(st) })
	bstore, err := blob.New(blob.Config{Provider: "fs", FSBasePath: st})
	if err != nil {
		t.Fatalf("fs blob store: %v", err)
	}
	svc, err := mailstore.New(db, mailstore.IdentityBlobCodec{}, bstore, nil)
	if err != nil {
		t.Fatalf("new mailstore: %v", err)
	}
	svc.SetChangeNotifier(mailstore.NewChangeNotifier())
	if sr, err := mailstore.NewPostgresSearcher(db); err == nil {
		svc.SetSearcher(sr)
	}

	// The authenticated identity: a user with a bcrypt PRIMARY password that
	// mail protocols must never accept, plus one app password that they must.
	email := fmt.Sprintf("imap-user-%s@imap.test", testRunSuffix)
	primaryHash, err := bcrypt.GenerateFromPassword([]byte("primary-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt primary: %v", err)
	}
	user := &models.User{Email: email, PasswordHash: string(primaryHash), Name: "IMAP Test"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _ = db.Unscoped().Delete(user) })
	appSecret := "app-password-secret-xyz"
	digest, err := bcrypt.GenerateFromPassword([]byte(appSecret), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt app password: %v", err)
	}
	if err := db.Create(&models.UserAppPassword{UserID: user.ID, Name: "imap-test", PasswordHash: string(digest)}).Error; err != nil {
		t.Fatalf("create app password: %v", err)
	}

	auth := mailauth.NewAuthenticator(
		repositories.NewUserRepository(db),
		repositories.NewUserAppPasswordRepository(db),
	)
	if auth == nil {
		t.Fatal("nil authenticator")
	}

	mb, err := svc.CreateMailbox(user.ID, nil, email, 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	t.Cleanup(func() {
		_ = svc.DeleteMailbox(mb.ID, user.ID, 0)
	})
	inbox, err := svc.GetFolderByPath(mb.ID, user.ID, 0, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}

	audits := &auditRecord{}
	server := NewServer(cfg, svc, auth)
	server.SetAudit(audits.record)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(server.Close)

	return &imapEnv{
		t:       t,
		db:      db,
		store:   svc,
		server:  server,
		addr:    ln.Addr().String(),
		user:    user,
		appPass: appSecret,
		mailbox: mb,
		inbox:   inbox,
		audits:  audits,
	}
}

// appendMessage files one message through the store directly (helper for
// seeding before a session connects).
func (env *imapEnv) appendMessage(subject, from string, raw string) *mailstore.AppendResult {
	env.t.Helper()
	res, err := env.store.Append(context.Background(), env.user.ID, 0, mailstore.AppendIn{
		MailboxID: env.mailbox.ID,
		FolderID:  env.inbox.ID,
		Raw:       []byte(raw),
	})
	if err != nil {
		env.t.Fatalf("seed append %q: %v", subject, err)
	}
	return res
}

// imapClient is the scripted fake client. Literal-aware: FETCH responses
// carry inline literals ({N}\r\n<octets>); the reader splices them into the
// line between LITMARK sentinels so assertions can match raw bytes.
const litMark = "\x01"

type imapClient struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

func dialEnv(t *testing.T, addr string) *imapClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &imapClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	return c
}

// greeting reads and returns the initial server greeting.
func (c *imapClient) greeting() string {
	c.t.Helper()
	line := c.line()
	if !strings.HasPrefix(line, "* OK") {
		c.t.Fatalf("bad greeting: %q", line)
	}
	return line
}

func (c *imapClient) send(format string, args ...any) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, format+"\r\n", args...); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

func (c *imapClient) sendRaw(s string) {
	c.t.Helper()
	if _, err := c.conn.Write([]byte(s)); err != nil {
		c.t.Fatalf("send raw: %v", err)
	}
}

// line reads one CRLF line (fatal on IO error).
func (c *imapClient) line() string {
	c.t.Helper()
	l, err := c.br.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read line: %v", err)
	}
	return strings.TrimRight(l, "\r\n")
}

// trailingLiteralN reports N when the fragment ends with a "{N}" literal
// marker (responses place literals at end-of-fragment).
func trailingLiteralN(fragment string) (int64, bool) {
	if !strings.HasSuffix(fragment, "}") {
		return 0, false
	}
	open := strings.LastIndexByte(fragment, '{')
	if open < 0 {
		return 0, false
	}
	body := fragment[open+1 : len(fragment)-1]
	var n int64
	if _, err := fmt.Sscanf(body, "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

// readFullResponse reads one complete response line, splicing any inline
// literals between LITMARK sentinels (recursively, for multiple literals).
func (c *imapClient) readFullResponse() string {
	fragment := c.line()
	out := fragment
	for {
		n, ok := trailingLiteralN(fragment)
		if !ok {
			return out
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.br, buf); err != nil {
			c.t.Fatalf("read literal of %d octets: %v", n, err)
		}
		out += litMark + string(buf) + litMark
		fragment = c.line()
		out += fragment
	}
}

// cmd sends a command and collects every response until the tagged reply.
// Literal payloads appear inside the collected lines between litMark
// sentinels.
func (c *imapClient) cmd(tag string, format string, args ...any) (untagged []string, tagged string) {
	c.t.Helper()
	c.send("%s %s", tag, fmt.Sprintf(format, args...))
	for {
		line := c.readFullResponse()
		if strings.HasPrefix(line, tag+" ") {
			return untagged, line
		}
		untagged = append(untagged, line)
	}
}

// literalOf returns the FIRST literal payload embedded between sentinels.
func literalOf(s string) string {
	i := strings.Index(s, litMark)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i+len(litMark):], litMark)
	if j < 0 {
		return ""
	}
	return s[i+len(litMark) : i+len(litMark)+j]
}

// expectOK asserts the tagged reply is OK.
func (c *imapClient) expectOK(tag string, format string, args ...any) ([]string, string) {
	c.t.Helper()
	untagged, tagged := c.cmd(tag, format, args...)
	if !strings.HasPrefix(tagged, tag+" OK") {
		c.t.Fatalf("expected %s OK, got %q (untagged: %v)", tag, tagged, untagged)
	}
	return untagged, tagged
}

// expectNO asserts the tagged reply is NO.
func (c *imapClient) expectNO(tag string, format string, args ...any) (untagged []string, tagged string) {
	c.t.Helper()
	untagged, tagged = c.cmd(tag, format, args...)
	if !strings.HasPrefix(tagged, tag+" NO") {
		c.t.Fatalf("expected %s NO, got %q (untagged: %v)", tag, tagged, untagged)
	}
	return untagged, tagged
}

// expectBAD asserts the tagged reply is BAD.
func (c *imapClient) expectBAD(tag string, format string, args ...any) string {
	c.t.Helper()
	_, tagged := c.cmd(tag, format, args...)
	if !strings.HasPrefix(tagged, tag+" BAD") {
		c.t.Fatalf("expected %s BAD, got %q", tag, tagged)
	}
	return tagged
}

// login authenticates with the env's app password (the standard flow).
func (c *imapClient) login(env *imapEnv) {
	c.t.Helper()
	// LOGIN with quoted strings.
	_, tagged := c.cmd("t1", "LOGIN %q %q", env.user.Email, env.appPass)
	if !strings.HasPrefix(tagged, "t1 OK") {
		c.t.Fatalf("login failed: %q", tagged)
	}
}

// appendLiteral performs an APPEND with a synchronizing literal.
func (c *imapClient) appendLiteral(tag, mailbox, flags, msg string) string {
	c.t.Helper()
	c.send("%s APPEND %s%s {%d}", tag, mailbox, flags, len(msg))
	if l := c.line(); !strings.HasPrefix(l, "+") {
		c.t.Fatalf("expected literal continuation, got %q", l)
	}
	c.sendRaw(msg + "\r\n")
	return c.waitForTagged(tag)
}

// untaggedByPrefix filters collected untagged lines.
func untaggedByPrefix(lines []string, prefix string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// selfSignedCert generates a throwaway certificate for the STARTTLS tests.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("tls key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "imap.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"imap.test", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("self-signed cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TLSUpgrade dials with tls.Config (for the implicit-TLS listener tests).
func dialTLS(t *testing.T, addr string) *imapClient {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) // test-only: self-signed trust
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &imapClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	return c
}

// starttls performs the client side of the STARTTLS upgrade over the plain
// connection (after the server answered STARTTLS with tagged OK).
func (c *imapClient) starttls() {
	c.t.Helper()
	tlsConn := tls.Client(c.conn, &tls.Config{InsecureSkipVerify: true, ServerName: "imap.test"}) // test-only: self-signed trust
	if err := tlsConn.Handshake(); err != nil {
		c.t.Fatalf("client TLS handshake: %v", err)
	}
	c.conn = tlsConn
	c.br = bufio.NewReader(tlsConn)
}
