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

package pop3

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailauth"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/security"
	"golang.org/x/crypto/bcrypt"
)

// ---------------------------------------------------------------------------
// Fake Store: an in-memory MIRROR OF THE MAILSTORE CONTRACT (ADR-003) —
// MarkDeleted sets \Deleted through the same flag shape, Expunge removes
// every \Deleted row of the folder transactionally, survivors keep their
// UIDs and the folder's UIDValidity never changes. The real
// *mailstore.Service's behavior is covered by its own tests; this mirror
// exists so the POP3 layer's use of the contract is testable without SQL.
// ---------------------------------------------------------------------------

type fakeMessage struct {
	id    uint
	uid   uint32
	size  int64
	flags uint32
}

type fakeBox struct {
	id       uint
	userID   uint
	name     string
	status   models.MailboxStatus
	inboxID  uint
	uidValid uint32
	uidNext  uint32
	messages []*fakeMessage // INBOX contents, UID-ascending
}

type fakeStore struct {
	mu       sync.Mutex
	boxes    []*fakeBox
	bodies   map[uint][]byte
	nextID   uint
	failNext bool // fail the next Expunge (QUIT-failure path)
}

func newFakeStore() *fakeStore {
	return &fakeStore{bodies: map[uint][]byte{}, nextID: 1}
}

func (f *fakeStore) addUserBox(userID uint, status models.MailboxStatus) *fakeBox {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := &fakeBox{
		id:       f.nextID,
		userID:   userID,
		name:     "primary",
		status:   status,
		inboxID:  f.nextID + 1,
		uidValid: 420001,
		uidNext:  1,
	}
	f.nextID += 2
	f.boxes = append(f.boxes, b)
	return b
}

// addMessage files one raw message into the box's INBOX with the next UID,
// mirroring mailstore.Append (UID claim + blob body + denormalized size).
func (f *fakeStore) addMessage(b *fakeBox, raw []byte) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies[f.nextID] = raw
	m := &fakeMessage{
		id:    f.nextID,
		uid:   b.uidNext,
		size:  int64(len(raw)),
		flags: 0,
	}
	f.nextID++
	b.uidNext++
	b.messages = append(b.messages, m)
	return m.uid
}

func (f *fakeStore) findBox(folderID uint) *fakeBox {
	for _, b := range f.boxes {
		if b.inboxID == folderID {
			return b
		}
	}
	return nil
}

// findMessage resolves a message row through the user's mailboxes (the
// ownership scoping the mailstore does in resolveMessage).
func (f *fakeStore) findMessage(userID, messageID uint) (*fakeBox, *fakeMessage) {
	for _, b := range f.boxes {
		if b.userID != userID {
			continue
		}
		for _, m := range b.messages {
			if m.id == messageID {
				return b, m
			}
		}
	}
	return nil, nil
}

func (f *fakeStore) ListMailboxes(userID, workspaceID uint) ([]models.Mailbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.Mailbox
	for _, b := range f.boxes {
		if b.userID == userID {
			out = append(out, models.Mailbox{
				ID: b.id, UserID: b.userID, Name: b.name, Status: b.status,
			})
		}
	}
	return out, nil
}

func (f *fakeStore) GetFolderByPath(mailboxID, userID, workspaceID uint, path string) (*models.MailFolder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.boxes {
		if b.id != mailboxID || b.userID != userID {
			continue
		}
		if strings.EqualFold(strings.Trim(path, "/"), "inbox") {
			return &models.MailFolder{
				ID:          b.inboxID,
				MailboxID:   b.id,
				Name:        "INBOX",
				IMAPPath:    "INBOX",
				UIDValidity: b.uidValid,
				UIDNext:     b.uidNext,
			}, nil
		}
	}
	return nil, mailstore.ErrFolderNotFound
}

func (f *fakeStore) ListMessages(folderID, userID, workspaceID uint, filter mailstore.ListFilter, page, pageSize int) (*mailstore.ListPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.findBox(folderID)
	if b == nil || b.userID != userID {
		return nil, mailstore.ErrFolderNotFound
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	if page < 1 {
		page = 1
	}
	total := int64(len(b.messages))
	start := (page - 1) * pageSize
	if start > len(b.messages) {
		start = len(b.messages)
	}
	end := start + pageSize
	if end > len(b.messages) {
		end = len(b.messages)
	}
	var msgs []models.MailMessage
	for _, m := range b.messages[start:end] {
		msgs = append(msgs, models.MailMessage{
			ID: m.id, FolderID: b.inboxID, UID: m.uid, Size: m.size, Flags: m.flags,
		})
	}
	return &mailstore.ListPage{Messages: msgs, Total: total}, nil
}

func (f *fakeStore) GetMessageBody(ctx context.Context, messageID, userID, workspaceID uint) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, m := f.findMessage(userID, messageID)
	if b == nil || m == nil {
		return nil, mailstore.ErrMessageNotFound
	}
	raw, ok := f.bodies[m.id]
	if !ok {
		return nil, mailstore.ErrBlobCorrupt
	}
	return append([]byte(nil), raw...), nil
}

func (f *fakeStore) MarkDeleted(userID, workspaceID, messageID uint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, m := f.findMessage(userID, messageID)
	if m == nil {
		return mailstore.ErrMessageNotFound
	}
	m.flags |= models.FlagDeleted
	return nil
}

func (f *fakeStore) Expunge(folderID, userID, workspaceID uint) (*mailstore.ExpungeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return nil, errors.New("fake: expunge failure injected")
	}
	b := f.findBox(folderID)
	if b == nil || b.userID != userID {
		return nil, mailstore.ErrFolderNotFound
	}
	var uids []uint32
	var kept []*fakeMessage
	for _, m := range b.messages {
		if m.flags&models.FlagDeleted != 0 {
			uids = append(uids, m.uid)
			continue
		}
		kept = append(kept, m)
	}
	if len(uids) > 0 {
		b.messages = kept
	}
	return &mailstore.ExpungeResult{Expunged: int64(len(uids)), UIDs: uids}, nil
}

// inBox reports whether a message with the given UID is still present (the
// tests' view of "the mailstore still holds it").
func (f *fakeStore) inBox(b *fakeBox, uid uint32) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range b.messages {
		if m.uid == uid {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Fake mailauth finders (the mailauth test's fake shape, duplicated locally
// because those are package-private).
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
	// active mirrors FindActiveByUserID: only still-active digests (a
	// revoked password is excluded from this set by the repository query).
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
// Fixtures + server harness
// ---------------------------------------------------------------------------

const (
	testUserEmail     = "ada@example.test"
	testUserID        = 7
	testAppSecret     = "apw_secret_laptop"
	testPhoneSecret   = "apw_secret_phone"
	testPrimarySecret = "primary-dashboard-secret"
)

// auditRecord is one captured audit event.
type auditRecord struct {
	action   string
	userID   uint
	email    string
	ip       string
	meta     map[string]any
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

func (c *auditCapture) of(action string) []auditRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []auditRecord
	for _, r := range c.records {
		if r.action == action {
			out = append(out, r)
		}
	}
	return out
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
	store   *fakeStore
	box     *fakeBox
	raws    map[uint32][]byte // UID → raw message bytes
	audit   *auditCapture
	tlsConf *tls.Config
	addr    string // plaintext listener
	tlsAddr string // implicit-TLS listener ("" when none)
}

// selfSignedCert generates a throwaway cert for the STLS/implicit-TLS tests.
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
	cert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

// rawMessage builds an RFC 5322 message; extraBodyLines lets tests include
// leading-dot lines (byte-stuffing round-trips).
func rawMessage(subject string, bodyLines []string) []byte {
	body := ""
	for _, l := range bodyLines {
		body += l + "\r\n"
	}
	return []byte("Message-ID: <" + subject + "@test>\r\n" +
		"From: sender@example.test\r\n" +
		"To: " + testUserEmail + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Mon, 02 Nov 2026 10:00:00 +0000\r\n" +
		"\r\n" +
		body)
}

// newFixture starts a POP3 server on 127.0.0.1:0 with the standard fixture:
// one active user with an app password (plus the primary password hash that
// must NEVER authenticate) and three INBOX messages.
func newFixture(t *testing.T, tlsOn bool, cfgTweaks ...func(*Config)) *testFixture {
	t.Helper()

	uf := &fakeUserFinder{users: map[string]*models.User{
		testUserEmail: {
			ID:           testUserID,
			Email:        testUserEmail,
			PasswordHash: bcryptHash(t, testPrimarySecret),
			Active:       true,
		},
	}}
	pf := &fakeAppPasswords{
		active: map[uint][]models.UserAppPassword{
			testUserID: {
				// Two live credentials; the revoked-credential test swaps in
				// a finder whose active set no longer holds the laptop one —
				// exactly the shape FindActiveByUserID returns after a
				// revocation (the row is excluded from the active set).
				{ID: 101, UserID: testUserID, Name: "laptop", PasswordHash: bcryptHash(t, testAppSecret)},
				{ID: 102, UserID: testUserID, Name: "phone", PasswordHash: bcryptHash(t, testPhoneSecret)},
			},
		},
	}
	auth := mailauth.NewAuthenticator(uf, pf)
	if auth == nil {
		t.Fatalf("mailauth authenticator nil")
	}

	store := newFakeStore()
	box := store.addUserBox(testUserID, models.MailboxStatusActive)
	raws := map[uint32][]byte{}
	subjects := []string{"alpha", "beta", "gamma"}
	for _, subj := range subjects {
		raw := rawMessage("subject-"+subj, []string{"body of " + subj})
		uid := store.addMessage(box, raw)
		raws[uid] = raw
	}

	cap := &auditCapture{}
	var tlsConf *tls.Config
	if tlsOn {
		tlsConf = selfSignedCert(t)
	}
	cfg := Config{
		Hostname:           "pop3.test",
		MaxSessionsPerUser: 10,
		MaxConnsPerIP:      30,
		CommandRateLimit:   0, // unlimited unless a test overrides
		AllowCleartextAuth: tlsConf == nil,
	}
	for _, tw := range cfgTweaks {
		tw(&cfg)
	}
	srv, err := NewServer(cfg, store, auth)
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

	tf := &testFixture{
		srv:     srv,
		store:   store,
		box:     box,
		raws:    raws,
		audit:   cap,
		tlsConf: tlsConf,
		addr:    l.Addr().String(),
	}
	return tf
}

// withImplicitTLS adds a second listener wrapped with the same eager-TLS
// stack the production wiring uses (CountingListener + TLSServerListener).
func (tf *testFixture) withImplicitTLS(t *testing.T) {
	t.Helper()
	if tf.tlsConf == nil {
		t.Fatalf("withImplicitTLS requires tlsOn")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tl := security.TLSServerListener(l, tf.tlsConf, nil, 0)
	go func() { _ = tf.srv.Serve(tl) }()
	tf.tlsAddr = l.Addr().String()
}

// ---------------------------------------------------------------------------
// Scripted POP3 client
// ---------------------------------------------------------------------------

type pop3Client struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dialPOP3(t *testing.T, addr string) *pop3Client {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &pop3Client{t: t, conn: conn, r: bufio.NewReaderSize(conn, 4096)}
	greeting := c.readLine()
	if !strings.HasPrefix(greeting, "+OK") {
		t.Fatalf("greeting = %q", greeting)
	}
	return c
}

func (c *pop3Client) close() { _ = c.conn.Close() }

// cmd sends one command line and returns the single-line response.
func (c *pop3Client) cmd(line string) string {
	c.t.Helper()
	if _, err := c.conn.Write([]byte(line + "\r\n")); err != nil {
		c.t.Fatalf("write %q: %v", line, err)
	}
	return c.readLine()
}

// readLine reads one response line (deadline-bounded so a protocol bug
// fails the test instead of hanging it).
func (c *pop3Client) readLine() string {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

// readMulti reads a multiline response terminated by the "." marker and
// un-stuffs it (RFC 1939 §2.3.1.2).
func (c *pop3Client) readMulti() []string {
	c.t.Helper()
	var out []string
	for {
		line := c.readLine()
		if line == "." {
			return out
		}
		if strings.HasPrefix(line, ".") {
			line = line[1:]
		}
		out = append(out, line)
	}
}

// login authenticates with USER/PASS and fails the test on refusal.
func (c *pop3Client) login(user, secret string) string {
	c.t.Helper()
	c.cmd("USER " + user)
	return c.cmd("PASS " + secret)
}

// upgradeTLS wraps the connection in a client-side TLS channel after a
// "+OK begin TLS negotiation" answer (the STLS client procedure).
func (c *pop3Client) upgradeTLS(conf *tls.Config) {
	c.t.Helper()
	tlsConn := tls.Client(c.conn, &tls.Config{
		ServerName:         "localhost",
		InsecureSkipVerify: true, // self-signed test certificate
		MinVersion:         tls.VersionTLS12,
	})
	if err := tlsConn.Handshake(); err != nil {
		c.t.Fatalf("TLS upgrade handshake: %v", err)
	}
	c.conn = tlsConn
	c.r = bufio.NewReaderSize(tlsConn, 4096)
}

// expectOK fails the test unless the response is a +OK.
func expectOK(t *testing.T, resp string) string {
	t.Helper()
	if !strings.HasPrefix(resp, "+OK") {
		t.Fatalf("expected +OK, got %q", resp)
	}
	return resp
}

func expectERR(t *testing.T, resp string) string {
	t.Helper()
	if !strings.HasPrefix(resp, "-ERR") {
		t.Fatalf("expected -ERR, got %q", resp)
	}
	return resp
}

// stats parses a "+OK <n> <octets>" response.
func parseStats(t *testing.T, resp string) (int, int64) {
	t.Helper()
	var n int
	var octets int64
	if _, err := fmt.Sscanf(resp, "+OK %d %d", &n, &octets); err != nil {
		t.Fatalf("parse STAT response %q: %v", resp, err)
	}
	return n, octets
}
