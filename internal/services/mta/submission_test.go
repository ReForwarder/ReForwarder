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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
)

// ---------- fakes for the submission backend collaborators ----------

type fakeVerifier struct {
	creds map[string]fakeCred
}

type fakeCred struct {
	password   string
	cred       *models.SMTPCredential
	ownerEmail string
}

func (f *fakeVerifier) Verify(username, password, remoteIP string) (*models.SMTPCredential, string, error) {
	c, ok := f.creds[username]
	if !ok || c.password != password {
		return nil, "", errors.New("unknown credential")
	}
	return c.cred, c.ownerEmail, nil
}

type fakeDomains struct {
	// verified maps domain → verified-for-the-fixture-tenant.
	verified map[string]bool
}

func (f *fakeDomains) VerifiedSenderDomain(userID uint, workspaceID *uint, domain string) bool {
	return f.verified[domain]
}

type fakeSink struct {
	mu      sync.Mutex
	submits []recordedSubmit
}

type recordedSubmit struct {
	userID           uint
	ownerEmail       string
	from             string
	to               []string
	subject          string
	expiresAt        *time.Time
	deliverBySeconds int
	requireTLS       bool
}

func (f *fakeSink) Submit(_ context.Context, userID, _ uint, _ *uint, ownerEmail string, req *email.SendRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits = append(f.submits, recordedSubmit{
		userID:           userID,
		ownerEmail:       ownerEmail,
		from:             req.From,
		to:               req.To,
		subject:          req.Subject,
		expiresAt:        req.ExpiresAt,
		deliverBySeconds: req.DeliverBySeconds,
		requireTLS:       req.RequireTLS,
	})
	return nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.submits)
}

func (f *fakeSink) last() recordedSubmit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.submits[len(f.submits)-1]
}

// newSubmissionFixture starts an in-process MSA over the fakes and returns
// its address plus the shared collaborators.
func newSubmissionFixture(t *testing.T) (string, *fakeVerifier, *fakeDomains, *fakeSink) {
	t.Helper()
	verifier := &fakeVerifier{creds: map[string]fakeCred{
		"smtp_alice": {
			password:   "correct-horse",
			cred:       &models.SMTPCredential{ID: 1, UserID: 7, WorkspaceID: 3, Username: "smtp_alice"},
			ownerEmail: "alice@example.org",
		},
	}}
	domains := &fakeDomains{verified: map[string]bool{"verified.example": true}}
	sink := &fakeSink{}

	backend, err := NewSubmissionBackend(verifier, domains, sink, 1024*1024)
	if err != nil {
		t.Fatalf("NewSubmissionBackend: %v", err)
	}
	srv, err := NewSubmissionServer(backend, SubmissionConfig{
		Host: "127.0.0.1", Port: 0, Hostname: "posta.test", TLSMode: "none",
	})
	if err != nil {
		t.Fatalf("NewSubmissionServer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), verifier, domains, sink
}

// submissionClient dials the MSA and optionally authenticates.
func submissionClient(t *testing.T, addr string, auth bool) *gosmtp.Client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial MSA: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cl := gosmtp.NewClient(conn)
	if err := cl.Hello("client.example.org"); err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	if auth {
		if err := cl.Auth(sasl.NewPlainClient("", "smtp_alice", "correct-horse")); err != nil {
			t.Fatalf("AUTH: %v", err)
		}
	}
	return cl
}

func smtpCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var se *gosmtp.SMTPError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *gosmtp.SMTPError", err, err)
	}
	return se.Code
}

// An authenticated tenant submitting from a verified sender domain is
// accepted and lands in the API pipeline (the sink).
func TestSubmissionAuthenticatedSendAccepted(t *testing.T) {
	addr, _, _, sink := newSubmissionFixture(t)
	cl := submissionClient(t, addr, true)

	if err := cl.Mail("hello@verified.example", nil); err != nil {
		t.Fatalf("MAIL FROM: %v", err)
	}
	if err := cl.Rcpt("dest@example.net", nil); err != nil {
		t.Fatalf("RCPT TO: %v", err)
	}
	w, err := cl.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	if _, err := io.WriteString(w, "Subject: hi\r\n\r\nbody\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("end of DATA: %v", err)
	}
	_ = cl.Quit()

	if sink.count() != 1 {
		t.Fatalf("sink submissions = %d, want 1", sink.count())
	}
	sub := sink.last()
	if sub.from != "hello@verified.example" {
		t.Fatalf("from = %q, want the envelope sender", sub.from)
	}
	if len(sub.to) != 1 || sub.to[0] != "dest@example.net" {
		t.Fatalf("to = %v, want the envelope recipient", sub.to)
	}
	if sub.subject != "hi" || sub.userID != 7 || sub.ownerEmail != "alice@example.org" {
		t.Fatalf("submit record = %+v", sub)
	}
}

// Non-negotiable: no open relay. Without AUTH, MAIL FROM / RCPT TO refuse —
// emersion go-smtp refuses pre-auth mail commands at the server layer (502
// "Please authenticate first"); the session itself answers 530. Either way,
// nothing reaches the pipeline.
func TestSubmissionUnauthenticatedCommandsRefused(t *testing.T) {
	addr, _, _, sink := newSubmissionFixture(t)
	cl := submissionClient(t, addr, false) // no AUTH

	err := cl.Mail("hello@verified.example", nil)
	if code := smtpCode(t, err); code != 502 && code != 530 {
		t.Fatalf("unauthenticated MAIL FROM code = %d (%v), want 502/530", code, err)
	}
	err = cl.Rcpt("dest@example.net", nil)
	if code := smtpCode(t, err); code != 502 && code != 530 {
		t.Fatalf("unauthenticated RCPT TO code = %d (%v), want 502/530", code, err)
	}
	if sink.count() != 0 {
		t.Fatalf("sink submissions = %d, want 0 (no relay without auth)", sink.count())
	}
}

// The sender domain must be tenant-verified: an authenticated session with an
// unverified MAIL FROM domain gets 550 5.7.1.
func TestSubmissionUnverifiedSenderDomainRefused(t *testing.T) {
	addr, _, _, sink := newSubmissionFixture(t)
	cl := submissionClient(t, addr, true)

	err := cl.Mail("evil@unverified.test", nil)
	if code := smtpCode(t, err); code != 550 {
		t.Fatalf("unverified sender domain code = %d (%v), want 550", code, err)
	}
	// The refused MAIL must leave no pipeline entry even if the client
	// proceeds to DATA.
	_ = cl.Rcpt("dest@example.net", nil)
	_, dataErr := cl.Data()
	if dataErr == nil {
		t.Fatal("DATA after a refused MAIL must not succeed")
	}
	if sink.count() != 0 {
		t.Fatalf("sink submissions = %d, want 0", sink.count())
	}
}

// Recipients are validated: a recipient the server accepts at the protocol
// layer but that fails address parsing is refused (553) by the session.
func TestSubmissionInvalidRecipientRefused(t *testing.T) {
	addr, _, _, sink := newSubmissionFixture(t)
	cl := submissionClient(t, addr, true)

	if err := cl.Mail("hello@verified.example", nil); err != nil {
		t.Fatalf("MAIL FROM: %v", err)
	}
	// "bad recipient@example.net" is not a parseable address: the MSA refuses
	// it with a 5xx — either the server layer (501, go-smtp's own parser) or
	// the session validation (553). Either way the recipient never reaches
	// the pipeline.
	err := cl.Rcpt("bad recipient@example.net", nil)
	if code := smtpCode(t, err); code < 500 || code > 599 {
		t.Fatalf("invalid recipient code = %d (%v), want a 5xx refusal", code, err)
	}
	if sink.count() != 0 {
		t.Fatalf("sink submissions = %d, want 0", sink.count())
	}
}

// Bad credentials are refused (535) and reach no sink.
func TestSubmissionBadCredentialsRefused(t *testing.T) {
	addr, _, _, sink := newSubmissionFixture(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	cl := gosmtp.NewClient(conn)
	if err := cl.Hello("client.example.org"); err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	if err := cl.Auth(sasl.NewPlainClient("", "smtp_alice", "wrong-password")); err == nil {
		t.Fatal("wrong password must fail")
	}
	if sink.count() != 0 {
		t.Fatalf("sink submissions = %d, want 0", sink.count())
	}
}

// A backend without all three collaborators must refuse to start — the
// constructor is the structural no-open-relay guard.
func TestSubmissionBackendRefusesIncompleteWiring(t *testing.T) {
	if _, err := NewSubmissionBackend(nil, &fakeDomains{}, &fakeSink{}, 0); err == nil {
		t.Fatal("nil verifier must refuse construction")
	}
	if _, err := NewSubmissionBackend(&fakeVerifier{}, nil, &fakeSink{}, 0); err == nil {
		t.Fatal("nil sender-domain checker must refuse construction")
	}
	if _, err := NewSubmissionBackend(&fakeVerifier{}, &fakeDomains{}, nil, 0); err == nil {
		t.Fatal("nil sink must refuse construction")
	}
}

// ---------- SF-09: DELIVERBY honoring through the MSA ----------

// The server advertises DELIVERBY, and a client-supplied BY parameter rides
// the send pipeline as the message's queue-expiry deadline (acceptance time +
// by-time) with the by-time recorded as provenance — the SF-08 expiry
// machinery takes it from there.
func TestSubmissionDeliverByAdvertisedAndHonored(t *testing.T) {
	addr, _, _, sink := newSubmissionFixture(t)
	cl := submissionClient(t, addr, true)

	if ok, _ := cl.Extension("DELIVERBY"); !ok {
		t.Fatal("DELIVERBY not advertised on the MSA")
	}
	if err := cl.Mail("hello@verified.example", nil); err != nil {
		t.Fatalf("MAIL FROM: %v", err)
	}
	if err := cl.Rcpt("dest@example.net", &gosmtp.RcptOptions{
		DeliverBy: &gosmtp.DeliverByOptions{Time: 90 * time.Second, Mode: gosmtp.DeliverByReturn},
	}); err != nil {
		t.Fatalf("RCPT TO with BY: %v", err)
	}
	w, err := cl.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	if _, err := io.WriteString(w, "Subject: deliverby\r\n\r\nbody\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("end of DATA: %v", err)
	}
	_ = cl.Quit()

	if sink.count() != 1 {
		t.Fatalf("sink submissions = %d, want 1", sink.count())
	}
	sub := sink.last()
	if sub.expiresAt == nil {
		t.Fatal("pipeline request carries no expires_at, want the DELIVERBY deadline")
	}
	remaining := time.Until(*sub.expiresAt)
	if remaining < 88*time.Second || remaining > 92*time.Second {
		t.Fatalf("deadline %s away, want ~90s (acceptance + by-time)", remaining)
	}
	if sub.deliverBySeconds != 90 {
		t.Fatalf("deliver_by_seconds = %d, want 90", sub.deliverBySeconds)
	}
}

// The deadline is per-message: multiple recipients with different by-times
// resolve to the strictest (shortest) one.
func TestSubmissionDeliverByStrictestByTimeWins(t *testing.T) {
	addr, _, _, sink := newSubmissionFixture(t)
	cl := submissionClient(t, addr, true)

	if err := cl.Mail("hello@verified.example", nil); err != nil {
		t.Fatalf("MAIL FROM: %v", err)
	}
	if err := cl.Rcpt("a@example.net", &gosmtp.RcptOptions{
		DeliverBy: &gosmtp.DeliverByOptions{Time: 120 * time.Second, Mode: gosmtp.DeliverByReturn},
	}); err != nil {
		t.Fatalf("RCPT TO (120s): %v", err)
	}
	if err := cl.Rcpt("b@example.net", &gosmtp.RcptOptions{
		DeliverBy: &gosmtp.DeliverByOptions{Time: 60 * time.Second, Mode: gosmtp.DeliverByNotify},
	}); err != nil {
		t.Fatalf("RCPT TO (60s): %v", err)
	}
	w, err := cl.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	if _, err := io.WriteString(w, "Subject: strictest\r\n\r\nbody\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("end of DATA: %v", err)
	}
	_ = cl.Quit()

	if sink.count() != 1 {
		t.Fatalf("sink submissions = %d, want 1", sink.count())
	}
	sub := sink.last()
	if sub.deliverBySeconds != 60 {
		t.Fatalf("deliver_by_seconds = %d, want 60 (the strictest by-time)", sub.deliverBySeconds)
	}
	if sub.expiresAt == nil {
		t.Fatal("pipeline request carries no expires_at")
	}
	if remaining := time.Until(*sub.expiresAt); remaining < 58*time.Second || remaining > 62*time.Second {
		t.Fatalf("deadline %s away, want ~60s", remaining)
	}
}

// A by-time the server cannot honor — non-positive (BY=0 would expire the
// message at acceptance) or above the 30-day cap — is refused with 501
// before DATA, per the RFC 2852 §4.2 posture, and nothing reaches the
// pipeline.
func TestSubmissionDeliverByRefusesNonHonorableByTime(t *testing.T) {
	addr, _, _, sink := newSubmissionFixture(t)

	// BY=0;N — the dependency accepts the zero by-time for notify mode, the
	// session refuses it (the server cannot honor an immediate deadline).
	cl := submissionClient(t, addr, true)
	if err := cl.Mail("hello@verified.example", nil); err != nil {
		t.Fatalf("MAIL FROM: %v", err)
	}
	err := cl.Rcpt("dest@example.net", &gosmtp.RcptOptions{
		DeliverBy: &gosmtp.DeliverByOptions{Time: 0, Mode: gosmtp.DeliverByNotify},
	})
	if code := smtpCode(t, err); code != 501 {
		t.Fatalf("BY=0 code = %d (%v), want 501", code, err)
	}

	// BY above the 30-day cap.
	err = cl.Rcpt("dest@example.net", &gosmtp.RcptOptions{
		DeliverBy: &gosmtp.DeliverByOptions{Time: 31 * 24 * time.Hour, Mode: gosmtp.DeliverByNotify},
	})
	if code := smtpCode(t, err); code != 501 {
		t.Fatalf("oversize BY code = %d (%v), want 501", code, err)
	}

	// Nothing reached the pipeline, and a by-less transaction still works.
	if sink.count() != 0 {
		t.Fatalf("sink submissions = %d, want 0", sink.count())
	}
	if err := cl.Rcpt("dest@example.net", nil); err != nil {
		t.Fatalf("plain RCPT TO after refusals: %v", err)
	}
}

// ---------- SF-02: the RFC 8689 REQUIRETLS parameter recorded ----------

// requireTLSClient dials the fixture MSA, authenticates, and returns the
// connection plus the client: the REQUIRETLS parameter is then written raw
// (the go-smtp client refuses to send it when the server has not advertised
// it — RFC 8689 §3 has the server advertise only on TLS, and the fixture MSA
// runs plaintext; the MAIL parameter itself is flag-gated, not TLS-gated, so
// the parse is exercised honestly either way).
func requireTLSClient(t *testing.T, addr string) (net.Conn, *gosmtp.Client) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial MSA: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cl := gosmtp.NewClient(conn)
	if err := cl.Hello("client.example.org"); err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	if err := cl.Auth(sasl.NewPlainClient("", "smtp_alice", "correct-horse")); err != nil {
		t.Fatalf("AUTH: %v", err)
	}
	return conn, cl
}

// A client carrying the REQUIRETLS MAIL FROM parameter has the requirement
// recorded on the message and persisted into the pipeline: the parameter is
// written raw and the reply consumed before the client continues (the socket
// is quiescent after AUTH, so the exchange leaves the client's pipeline
// consistent).
func TestSubmissionRequireTLSRecordedInPipeline(t *testing.T) {
	addr, _, _, sink := newSubmissionFixture(t)
	conn, cl := requireTLSClient(t, addr)

	if _, err := fmt.Fprintf(conn, "MAIL FROM:<hello@verified.example> REQUIRETLS\r\n"); err != nil {
		t.Fatalf("write MAIL FROM: %v", err)
	}
	tr := textproto.NewReader(bufio.NewReader(conn))
	code, _, err := tr.ReadResponse(250)
	if err != nil || code != 250 {
		t.Fatalf("MAIL FROM with REQUIRETLS reply = %d (%v), want 250", code, err)
	}
	if err := cl.Rcpt("dest@example.net", nil); err != nil {
		t.Fatalf("RCPT TO after the raw MAIL FROM: %v", err)
	}
	w, err := cl.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	if _, err := io.WriteString(w, "Subject: requiretls\r\n\r\nbody\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("end of DATA: %v", err)
	}
	_ = cl.Quit()

	if sink.count() != 1 {
		t.Fatalf("sink submissions = %d, want 1", sink.count())
	}
	if sub := sink.last(); !sub.requireTLS {
		t.Fatal("pipeline request RequireTLS = false, want true (the parameter was recorded)")
	}
}

// The control: a plain MAIL FROM records no requirement.
func TestSubmissionPlainMailRecordsNoRequirement(t *testing.T) {
	addr, _, _, sink := newSubmissionFixture(t)
	cl := submissionClient(t, addr, true)

	if err := cl.Mail("hello@verified.example", nil); err != nil {
		t.Fatalf("MAIL FROM: %v", err)
	}
	if err := cl.Rcpt("dest@example.net", nil); err != nil {
		t.Fatalf("RCPT TO: %v", err)
	}
	w, err := cl.Data()
	if err != nil {
		t.Fatalf("DATA: %v", err)
	}
	if _, err := io.WriteString(w, "Subject: plain\r\n\r\nbody\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("end of DATA: %v", err)
	}
	_ = cl.Quit()

	if sink.count() != 1 {
		t.Fatalf("sink submissions = %d, want 1", sink.count())
	}
	if sub := sink.last(); sub.requireTLS {
		t.Fatal("pipeline request RequireTLS = true, want false (no parameter carried)")
	}
}
