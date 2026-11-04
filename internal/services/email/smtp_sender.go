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

package email

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/goposta/posta/internal/models"
)

// SendError carries structured information about an SMTP failure so callers can
// distinguish a permanent recipient rejection (5xx at RCPT TO) from a transient
// error worth retrying. Error() preserves the original human-readable string.
type SendError struct {
	Stage     string // "RCPT TO", "MAIL FROM", "DATA", ...
	Recipient string // bare recipient address, set for RCPT TO failures
	Code      int    // SMTP reply code (e.g. 550); 0 when not an SMTP reply
	Msg       string // server reply text, incl. enhanced status code (5.1.1 ...)
	Err       error
}

// Permanent reports whether the failure is a permanent (5xx) SMTP rejection.
func (e *SendError) Permanent() bool { return e.Code >= 500 && e.Code < 600 }

// TimedOut reports whether the failure was a network timeout (dial or I/O
// deadline). Timeouts are transient by definition: the retry path re-enqueues
// them and they can never trigger auto-suppression — a timeout carries no SMTP
// reply code (Code stays 0), so Permanent() is false and the worker's
// permanentRejection predicate cannot match.
func (e *SendError) TimedOut() bool {
	var nerr net.Error
	return errors.As(e.Err, &nerr) && nerr.Timeout()
}

func (e *SendError) Error() string { return e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

// smtpReply extracts the SMTP reply code and message from an error returned by
// the net/smtp client. It returns (0, "") when err does not wrap a
// *textproto.Error (e.g. a connection-level failure).
func smtpReply(err error) (int, string) {
	var t *textproto.Error
	if errors.As(err, &t) {
		return t.Code, t.Msg
	}
	return 0, ""
}

// Timeout settings for outgoing SMTP connections.
//
// A stalled relay used to wedge a worker concurrency slot forever: the client
// dialed with no timeout and never set a deadline, so a server that accepted
// the connection and never replied blocked the send indefinitely. Every
// connection now gets (a) a dial timeout and (b) an overall session deadline —
// after the dial, the remaining commands (banner, EHLO, AUTH, MAIL/RCPT/DATA)
// must complete within dialTimeout × commandBudgetFactor. For simplicity the
// budget is a single flat deadline reset once, before the DATA write (the only
// long operation for large messages) gets a fresh budget of the same size —
// not a per-command accounting; documented in the roadmap notes.
const (
	// DefaultDialTimeout bounds one TCP/TLS dial to a relay.
	DefaultDialTimeout = 15 * time.Second
	// commandBudgetFactor scales the dial timeout into the overall session budget.
	commandBudgetFactor = 4
)

var (
	timeoutsMu  sync.RWMutex
	dialTimeout = DefaultDialTimeout
)

// SetDialTimeout sets the dial timeout used for every SMTP connection opened
// by this package (senders are constructed in many places, so the setting is
// process-wide and applied once at startup). Values <= 0 keep the default.
func SetDialTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	timeoutsMu.Lock()
	dialTimeout = d
	timeoutsMu.Unlock()
}

// DialTimeout returns the configured dial timeout.
func DialTimeout() time.Duration {
	timeoutsMu.RLock()
	defer timeoutsMu.RUnlock()
	return dialTimeout
}

// overallDeadline is the session budget applied right after dialing.
func overallDeadline() time.Duration {
	return DialTimeout() * commandBudgetFactor
}

// dialNet opens a plain TCP connection with the configured dial timeout.
func dialNet(addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: DialTimeout()}
	return d.Dial("tcp", addr)
}

// DkimSigner signs outgoing messages per sender domain (R-02). Interface so a
// stub can be injected in tests.
type DkimSigner interface {
	SignForDomain(msg []byte, domain string) ([]byte, error)
}

// defaultDkimSigner is the process-wide signer wired once at startup (see
// SetDkimSigner). Senders constructed before/without wiring read it as a
// fallback, so every delivery path (sync fallback, relay, retry worker,
// notifications) signs without each construction site needing the resolver.
var (
	dkimMu          sync.RWMutex
	defaultDkimSign DkimSigner
)

// SetDkimSigner sets the process-wide DKIM signer used by SMTPSenders that
// have no instance-level signer. nil disables managed signing globally
// (unsigned / CNAME mode).
func SetDkimSigner(s DkimSigner) {
	dkimMu.Lock()
	defaultDkimSign = s
	dkimMu.Unlock()
}

// currentDkimSigner returns the package-wide signer, or nil.
func currentDkimSigner() DkimSigner {
	dkimMu.RLock()
	defer dkimMu.RUnlock()
	return defaultDkimSign
}

type SMTPSender struct {
	// dkim, when set, signs every message this sender emits for the envelope
	// sender's domain. When nil, the process-wide signer (SetDkimSigner)
	// applies; when that is unset too, mail goes out unsigned (the upstream
	// CNAME signer mode).
	dkim DkimSigner
}

func NewSMTPSender() *SMTPSender {
	return &SMTPSender{}
}

// SetDkimSigner pins a signer on this sender, overriding the process-wide one.
func (s *SMTPSender) SetDkimSigner(signer DkimSigner) {
	s.dkim = signer
}

// TestConnection verifies that a connection to the SMTP server can be
// established and, when credentials are provided, that authentication succeeds.
func (s *SMTPSender) TestConnection(server *models.SMTPServer) error {
	addr := fmt.Sprintf("%s:%d", server.Host, server.Port)
	tlsConfig := &tls.Config{ServerName: server.Host}

	conn, client, err := dialSMTP(addr, server.Host, server.Encryption, tlsConfig)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = client.Close() }()

	if server.Username != "" {
		if err := client.Auth(newAuth(server.Host, server.Username, server.Password)); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}
	return client.Quit()
}

// dialSMTP establishes the SMTP connection for one session: dial with the
// configured timeout (implicit TLS wraps the dialer, STARTTLS/plain dial raw
// TCP and hand the conn to net/smtp), then set the overall session deadline.
// STARTTLS is upgraded in-band by the caller afterwards; the deadline set here
// keeps applying — tls.Conn wraps the same socket.
func dialSMTP(addr, host, encryption string, tlsConfig *tls.Config) (net.Conn, *smtp.Client, error) {
	var conn net.Conn
	var err error
	switch encryption {
	case models.EncryptionSSL:
		// tls.DialWithDialer applies the dialer's Timeout to both the TCP dial
		// and the TLS handshake.
		d := net.Dialer{Timeout: DialTimeout()}
		conn, err = tls.DialWithDialer(&d, "tcp", addr, tlsConfig)
		if err != nil {
			return nil, nil, fmt.Errorf("SSL dial failed: %w", err)
		}
	default: // none, and STARTTLS before the upgrade
		conn, err = dialNet(addr)
		if err != nil {
			return nil, nil, fmt.Errorf("SMTP dial failed: %w", err)
		}
	}

	// Overall session budget: everything after the dial must finish within it,
	// so a silent relay can no longer wedge a worker slot indefinitely.
	if err := conn.SetDeadline(time.Now().Add(overallDeadline())); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("failed to set connection deadline: %w", err)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("SMTP client creation failed: %w", err)
	}
	if encryption == models.EncryptionSTARTTLS {
		if err := client.StartTLS(tlsConfig); err != nil {
			_ = client.Close()
			return nil, nil, fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	return conn, client, nil
}

// autoAuth uses PLAIN when the server offers it, otherwise LOGIN (Exchange Online).
type autoAuth struct {
	plain            smtp.Auth
	host, user, pass string
	login            bool
}

func newAuth(host, username, password string) smtp.Auth {
	return &autoAuth{plain: smtp.PlainAuth("", username, password, host), host: host, user: username, pass: password}
}

func (a *autoAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !offers(server.Auth, "LOGIN") || offers(server.Auth, "PLAIN") {
		return a.plain.Start(server)
	}
	if !server.TLS && server.Name != "localhost" && server.Name != "127.0.0.1" && server.Name != "::1" {
		return "", nil, errors.New("unencrypted connection")
	}
	if server.Name != a.host {
		return "", nil, errors.New("wrong host name")
	}
	a.login = true
	return "LOGIN", nil, nil
}

func (a *autoAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !a.login {
		return a.plain.Next(fromServer, more)
	}
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.pass), nil
	}
	return nil, fmt.Errorf("unexpected LOGIN challenge %q", fromServer)
}

func offers(mechanisms []string, name string) bool {
	return slices.ContainsFunc(mechanisms, func(m string) bool { return strings.EqualFold(m, name) })
}

func (s *SMTPSender) Send(server *models.SMTPServer, from string, to []string, subject, htmlBody, textBody string, attachments []models.Attachment, headers map[string]string, listUnsubscribeURL, listUnsubscribeMailto string, listUnsubscribePost bool) error {
	addr := fmt.Sprintf("%s:%d", server.Host, server.Port)

	var auth smtp.Auth
	if server.Username != "" {
		auth = newAuth(server.Host, server.Username, server.Password)
	}

	msg := buildMessage(from, to, subject, htmlBody, textBody, attachments, headers, listUnsubscribeURL, listUnsubscribeMailto, listUnsubscribePost)

	// Extract bare email for SMTP envelope (MAIL FROM), keep full format for headers
	envelopeFrom := envelopeAddress(from)

	// DKIM signing (R-02): sign the final message for the envelope sender's
	// domain when a managed signing key exists. A signing error fails the send
	// (asynq retries); a missing key keeps the message unsigned — the CNAME /
	// upstream-signer mode.
	signer := s.dkim
	if signer == nil {
		signer = currentDkimSigner()
	}
	if signer != nil {
		if domain := senderDomainOf(envelopeFrom); domain != "" {
			signed, err := signer.SignForDomain(msg, domain)
			if err != nil {
				return fmt.Errorf("DKIM signing failed: %w", err)
			}
			msg = signed
		}
	}

	tlsConfig := &tls.Config{ServerName: server.Host}
	conn, client, err := dialSMTP(addr, server.Host, server.Encryption, tlsConfig)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = client.Close() }()

	return sendViaClient(client, conn, auth, envelopeFrom, to, msg)
}

// envelopeAddress extracts the bare email from an RFC 5322 address like
// "Display Name <user@example.com>", returning just "user@example.com".
// If parsing fails, it returns the input unchanged.
func envelopeAddress(from string) string {
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return from
	}
	return addr.Address
}

// senderDomainOf returns the lowercased domain of a bare email address, or ""
// when the address has no domain.
func senderDomainOf(bare string) string {
	if i := strings.LastIndex(bare, "@"); i >= 0 && i+1 < len(bare) {
		return strings.ToLower(bare[i+1:])
	}
	return ""
}

// sendViaClient runs the SMTP conversation over an already-established
// client. conn is the underlying socket, used to give the DATA phase a fresh
// slice of the overall budget (it is the only long operation: large messages
// and slow links) before quitting.
func sendViaClient(client *smtp.Client, conn net.Conn, auth smtp.Auth, from string, to []string, msg []byte) error {
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return wrapSendError("MAIL FROM", from, fmt.Errorf("SMTP MAIL FROM failed: %w", err))
	}
	for _, addr := range to {
		rcpt := envelopeAddress(addr)
		if err := client.Rcpt(rcpt); err != nil {
			return wrapSendError("RCPT TO", rcpt, fmt.Errorf("SMTP RCPT TO failed: %w", err))
		}
	}

	// Fresh budget for the DATA phase: the message body write is the longest
	// single operation and would otherwise share the budget with everything
	// before it.
	_ = conn.SetDeadline(time.Now().Add(overallDeadline()))

	w, err := client.Data()
	if err != nil {
		return wrapSendError("DATA", "", fmt.Errorf("SMTP DATA failed: %w", err))
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("SMTP write failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return wrapSendError("DATA", "", fmt.Errorf("SMTP close failed: %w", err))
	}

	return client.Quit()
}

// wrapSendError turns a stage failure into a *SendError carrying the SMTP reply
// code and recipient, while preserving the original error string.
func wrapSendError(stage, recipient string, err error) error {
	code, msg := smtpReply(err)
	return &SendError{Stage: stage, Recipient: recipient, Code: code, Msg: msg, Err: err}
}

const (
	contentTypeTextPlain = "text/plain"
	contentTypeTextHTML  = "text/html"
)

// writeTextPart writes a text body part. Non-ASCII bodies are quoted-printable
// encoded: with no Content-Transfer-Encoding the part defaults to 7bit, so raw
// UTF-8 would be a lie the next hop is free to mangle.
func writeTextPart(b *strings.Builder, mediaType, body string) {
	fmt.Fprintf(b, "Content-Type: %s; charset=\"UTF-8\"\r\n", mediaType)
	if isASCII(body) {
		b.WriteString("\r\n")
		b.WriteString(body)
		return
	}
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	w := quotedprintable.NewWriter(b)
	_, _ = w.Write([]byte(body))
	_ = w.Close()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// encodeHeader encodes s for an unstructured header such as Subject, folding
// its encoded-words onto separate lines. Plain text containing "=?" is encoded
// too, or a reader would decode what merely looks like an encoded-word.
func encodeHeader(s string) string {
	if mime.BEncoding.Encode("UTF-8", s) == s && !strings.Contains(s, "=?") {
		return foldPlain(s)
	}
	return strings.Join(encodeWords(s), "\r\n ")
}

// encodeWords encodes s as RFC 2047 encoded-words. RFC 2047 ignores the
// whitespace between encoded-words, but some relays keep it when they
// re-encode the header, so encoded-words break only at spaces; a break inside
// a word would reach the reader as a stray space.
func encodeWords(s string) []string {
	// 39 bytes encode to 52 base64 characters, so even the first line, after
	// "Subject: ", stays within the 76 characters RFC 2047 allows a line that
	// carries encoded-words.
	const limit = 39
	chunks := []string{""}
	for i, w := range strings.Split(s, " ") {
		if i > 0 {
			w = " " + w
		}
		if n := len(chunks) - 1; chunks[n] != "" && len(chunks[n])+len(w) > limit {
			chunks = append(chunks, "")
		}
		for _, r := range w {
			if n := len(chunks) - 1; len(chunks[n])+utf8.RuneLen(r) > limit {
				chunks = append(chunks, "")
			}
			chunks[len(chunks)-1] += string(r)
		}
	}
	for i, c := range chunks {
		chunks[i] = "=?UTF-8?b?" + base64.StdEncoding.EncodeToString([]byte(c)) + "?="
	}
	return chunks
}

// encodeFrom encodes a non-ASCII sender name, folded between encoded-words.
func encodeFrom(from string) string {
	addr, err := mail.ParseAddress(from)
	if err != nil || isASCII(addr.Name) {
		return from
	}
	return strings.Join(encodeWords(addr.Name), "\r\n ") + " <" + addr.Address + ">"
}

// foldPlain folds plain text at spaces so that, after "Subject: ", each line
// stays within the 78 characters RFC 5322 §2.1.1 recommends.
func foldPlain(s string) string {
	var b strings.Builder
	n := len("Subject: ")
	for i, w := range strings.Split(s, " ") {
		if i > 0 {
			if w != "" && n+1+len(w) > 78 {
				b.WriteString("\r\n")
				n = 0
			}
			b.WriteByte(' ')
			n++
		}
		b.WriteString(w)
		n += len(w)
	}
	return b.String()
}

// hasHeader reports whether the caller-supplied headers already include key
// (header names are case-insensitive per RFC 5322).
func hasHeader(headers map[string]string, key string) bool {
	for k := range headers {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// newBoundary returns a random MIME boundary. A fixed boundary shared by every
// message would collide with quoted or nested content that happens to contain
// the same string, and makes all Posta mail trivially fingerprintable. The
// token stays short enough that the Content-Type header line fits in 76
// characters even with the boundary quoted.
func newBoundary() string {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		// crypto/rand failing is extraordinary; a UUID keeps the boundary unique.
		return "posta-" + uuid.NewString()
	}
	return "posta-" + hex.EncodeToString(token[:])
}

func buildMessage(from string, to []string, subject, htmlBody, textBody string, attachments []models.Attachment, headers map[string]string, listUnsubscribeURL, listUnsubscribeMailto string, listUnsubscribePost bool) []byte {
	var b strings.Builder

	fmt.Fprintf(&b, "From: %s\r\n", encodeFrom(from))
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeHeader(subject))

	// RFC 5322 requires Date and recommends Message-ID; relays are free to
	// forward a message without adding them, so emit both unless the caller
	// already supplied one through custom headers.
	if !hasHeader(headers, "Date") {
		fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	}
	if !hasHeader(headers, "Message-ID") {
		domain := "posta.local"
		if addr, err := mail.ParseAddress(from); err == nil {
			if i := strings.LastIndex(addr.Address, "@"); i >= 0 && i+1 < len(addr.Address) {
				domain = addr.Address[i+1:]
			}
		}
		fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", uuid.NewString(), domain)
	}

	b.WriteString("MIME-Version: 1.0\r\n")

	// RFC 2369 / 8058 List-Unsubscribe. Emit mailto first, then the https URL.
	var luParts []string
	if listUnsubscribeMailto != "" {
		luParts = append(luParts, "<"+listUnsubscribeMailto+">")
	}
	if listUnsubscribeURL != "" {
		luParts = append(luParts, "<"+listUnsubscribeURL+">")
	}
	if len(luParts) > 0 {
		fmt.Fprintf(&b, "List-Unsubscribe: %s\r\n", strings.Join(luParts, ", "))
		// RFC 8058 one-click applies to the https POST target only.
		if listUnsubscribePost && listUnsubscribeURL != "" {
			b.WriteString("List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n")
		}
	}

	// Write custom headers (already filtered for safety)
	for key, value := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", key, value)
	}

	hasAttachments := len(attachments) > 0

	if hasAttachments {
		mixedBoundary := newBoundary()
		altBoundary := newBoundary()

		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", mixedBoundary)

		// Body part
		fmt.Fprintf(&b, "--%s\r\n", mixedBoundary)

		if htmlBody != "" && textBody != "" {
			fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", altBoundary)
			fmt.Fprintf(&b, "--%s\r\n", altBoundary)
			writeTextPart(&b, contentTypeTextPlain, textBody)
			fmt.Fprintf(&b, "\r\n--%s\r\n", altBoundary)
			writeTextPart(&b, contentTypeTextHTML, htmlBody)
			fmt.Fprintf(&b, "\r\n--%s--\r\n", altBoundary)
		} else if htmlBody != "" {
			writeTextPart(&b, contentTypeTextHTML, htmlBody)
		} else {
			writeTextPart(&b, contentTypeTextPlain, textBody)
		}

		// Attachment parts
		for _, att := range attachments {
			fmt.Fprintf(&b, "\r\n--%s\r\n", mixedBoundary)
			contentType := att.ContentType
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			fmt.Fprintf(&b, "Content-Type: %s\r\n", mime.FormatMediaType(contentType, map[string]string{"name": att.Filename}))
			b.WriteString("Content-Transfer-Encoding: base64\r\n")
			fmt.Fprintf(&b, "Content-Disposition: %s\r\n\r\n", mime.FormatMediaType("attachment", map[string]string{"filename": att.Filename}))

			// Content is already base64-encoded from the API request
			content := att.Content
			for len(content) > 76 {
				b.WriteString(content[:76])
				b.WriteString("\r\n")
				content = content[76:]
			}
			if len(content) > 0 {
				b.WriteString(content)
			}
		}
		fmt.Fprintf(&b, "\r\n--%s--\r\n", mixedBoundary)
	} else {
		altBoundary := newBoundary()
		if htmlBody != "" && textBody != "" {
			fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", altBoundary)
			fmt.Fprintf(&b, "--%s\r\n", altBoundary)
			writeTextPart(&b, contentTypeTextPlain, textBody)
			fmt.Fprintf(&b, "\r\n--%s\r\n", altBoundary)
			writeTextPart(&b, contentTypeTextHTML, htmlBody)
			fmt.Fprintf(&b, "\r\n--%s--\r\n", altBoundary)
		} else if htmlBody != "" {
			writeTextPart(&b, contentTypeTextHTML, htmlBody)
		} else {
			writeTextPart(&b, contentTypeTextPlain, textBody)
		}
	}

	return []byte(b.String())
}

// ValidateAttachments checks that attachments are within size limits.
func ValidateAttachments(attachments []models.Attachment, maxAttachmentSize, maxTotalSize int64) error {
	var totalSize int64
	for _, att := range attachments {
		if att.Filename == "" {
			return fmt.Errorf("attachment filename is required")
		}
		decoded, err := base64.StdEncoding.DecodeString(att.Content)
		if err != nil {
			return fmt.Errorf("attachment %q has invalid base64 content", att.Filename)
		}
		size := int64(len(decoded))
		if size > maxAttachmentSize {
			return fmt.Errorf("attachment %q exceeds maximum size of %d bytes", att.Filename, maxAttachmentSize)
		}
		totalSize += size
	}
	if totalSize > maxTotalSize {
		return fmt.Errorf("total attachment size exceeds maximum of %d bytes", maxTotalSize)
	}
	return nil
}
