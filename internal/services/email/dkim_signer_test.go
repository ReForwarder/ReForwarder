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

package email

import (
	"bytes"
	"os"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/goposta/posta/internal/models"
	postacrypto "github.com/goposta/posta/internal/services/crypto"
	"gorm.io/gorm"
	"net"
	"time"
)

// The crypto service (SF-78 hardening) refuses to store secrets when no
// encryption key is configured; these tests exercise key storage, so they
// configure a fixed test key up front.
func TestMain(m *testing.M) {
	postacrypto.Init("dkim-signer-test-key")
	os.Exit(m.Run())
}

// Fixed, TEST-ONLY keys. Embedding them pins the golden-message contract: the
// same message signed with the same key must always verify. These keys are
// not used anywhere outside the tests.
const testRSAKeyPem = `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEAnvqA5Xb5LyYGhnSDZaXgWXD+HHAtoPaJa3FIqgQT0wiv8yAq
EHiHvlaklDPuFuhKoFX4oRSmwTJFgiBikAQDrE8YVyBK/bB7oR5Mtn/6o9I/0zqu
JgzWVgqtHwDBJT8KftjsUOBG7AV3OUMe+DFJfz1XMbe+wrhVwaiyDlJ8EeKwOWhw
1fS0CYzrGH21/GD5LgweNk7Xi+WUZTmy6xXX9Stk9R4P9BVKgEDgMatU/4u5Dsdq
KICOH7IbyqyvD/gz+b369GswXxdMiterEuvMOTp5/Ke4hXkFOHvyC5sZkBvFD7i4
i4deCobm1492E/5PR55CCib31hvJ568Wi1wM8QIDAQABAoIBAB6YDp4aGzIbmz3c
xjyVg2sy93P5ko5bY26tylINntMJ5PJgyjeJOnZCrkFnDPaTlWjton89PioHxqCY
It+cJijOv9xkdooDoIcNEa49SrqkB2xa6pCf/89nFpHUz1afnyFyFWgrAPsCwkLe
y+LveTlnUJRcy/UlDDvMfxeKgB3guE0+VLB12fA94B2iOa06YX0V7nhAYZ9biE2a
fjCKM66NVR6ysh2ElZcCU59hLf/yyBDH+6xcZ1so/1kCF7/J5dpz5nkEPr6rEd3s
MNpMtX4aOw1MdZ1FXOcRPZJnUDaDuvmXFSgtdHM3yj95daPlDLj0EkjLLgBpZ2L0
+ga/+5ECgYEA1S7lnjmfRrORyl9L9kJxmyR1HwXaUEWNmoEn9k07DRFAO/ToroeB
BGk5t5jZv84H1hi+r2WUO9bkmM1U34UL82pZv46B2u/ZjeNcNqgTowK9pCGlO9Cl
QFyxrUrhQfTvOKqu6zNWbD8j2bHw5GG6apu0Z38bInbhTZwmi34URScCgYEAvuiZ
rgRzvpEskRqT7LfSprcw8mXSbhxDMp8UgGQzNOmU2/LQxDsoywKmfG8mkDfASyPT
YHfmUaTJZrGkcOpKZvUCS4lrKlfm8Ge0UHrOvzMVSp6xj208dpSKcDKzx+YdqPKY
pStR5Vu+CdU+qHvkj4qG+gc9tcvgSOSS6nVo3CcCgYEApww6jYOllWc8hJGsGmny
T5l/1TFm4qO91CYExHG8Jf8wkErW0XwI0wYJXh598lhlt5RoR1SXlVRMiJfNO+Er
fjAF556pjn+HwDE6nvGRgZiyNZzPt1FFTGvWJm6YmHBvy72jl1YBEI1hcEKI+szB
oKwl7zugbZ8t6PzFD2VaSN0CgYAmOuYrlvCmxOOr2yqsSNOCV0USBZnyOu6qAHlW
jLpEalJJCYMNCNvOa6VzXb7XOpLjeTUnKFuTFfDPA9vuULy9iWsAPXCcdiXlFp9h
TnaAi3ZEDyt3kWR+Spi+hyjBay+dWZd/3AD2cTzJM07s5oKKzC7jeve8pCsx7bQO
B50MGQKBgQCsNOeRQ3nZyTRcEt9Gq2sdSQgvStCrdaS7zY6KcZzhcqOxcgTb7GpK
pZsgk1fSG8HwuVd6FR73iaWPFBCGPSIbpofUR+tz1szw4o4xYQEwepxRbkkE4vjL
xNoKUSDg9uZ7jvVqi5vj0RMx+Pz9Z2MnMaPrTr3Bce8qo3EJMPDY4g==
-----END RSA PRIVATE KEY-----
`

const testEd25519KeyPem = `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEIHaOWmn8/n3a8iBd8qmoG8Glz5r3rDx38gR2830wmJBc
-----END PRIVATE KEY-----
`

// goldenMessage is the fixed message every signature in this file signs. CRLF
// line endings per RFC 5322 — DKIM canonicalization is sensitive to them.
var goldenMessage = []byte("From: Sender <sender@example.com>\r\n" +
	"To: rcpt@example.com\r\n" +
	"Subject: Golden\r\n" +
	"Date: Thu, 01 Oct 2026 10:00:00 +0000\r\n" +
	"Message-ID: <golden-1@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=\"UTF-8\"\r\n" +
	"\r\n" +
	"Body line.\r\n")

// stubKeyResolver answers FindActiveByDomain from a map.
type stubKeyResolver struct {
	keys map[string]*models.DomainSigningKey
	err  error
}

func (s *stubKeyResolver) FindActiveByDomain(domain string) (*models.DomainSigningKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	k, ok := s.keys[strings.ToLower(domain)]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return k, nil
}

// storeKeyPem encrypts a test PEM the way the handler would store it.
func storeKeyPem(t *testing.T, pemStr, algorithm, selector string, domainID uint) *models.DomainSigningKey {
	t.Helper()
	enc, err := postacrypto.Encrypt(pemStr)
	if err != nil {
		t.Fatalf("encrypt test key: %v", err)
	}
	return &models.DomainSigningKey{
		ID:            domainID,
		DomainID:      domainID,
		Selector:      selector,
		Algorithm:     algorithm,
		PrivateKeyPem: enc,
		Status:        models.DomainSigningStatusActive,
		DNSState:      models.DomainSigningDNSPublished,
	}
}

// verifyWithStubDNS runs the library's own verifier against the signed
// message, feeding it the TXT record the service would have the operator
// publish. A nil Verification.Err means the signature cryptographically checks
// out against the published key.
func verifyWithStubDNS(t *testing.T, signed []byte, domain, selector string, txt string) *dkim.Verification {
	t.Helper()
	verifs, err := dkim.VerifyWithOptions(bytes.NewReader(signed), &dkim.VerifyOptions{
		LookupTXT: func(name string) ([]string, error) {
			want := selector + "._domainkey." + domain
			if name != want {
				return nil, fmt.Errorf("unexpected lookup %q (want %q)", name, want)
			}
			return []string{txt}, nil
		},
	})
	if err != nil {
		t.Fatalf("dkim.VerifyWithOptions: %v", err)
	}
	if len(verifs) != 1 {
		t.Fatalf("got %d verifications, want 1", len(verifs))
	}
	return verifs[0]
}

func TestDkimGoldenRSASignature(t *testing.T) {
	key := storeKeyPem(t, testRSAKeyPem, models.SigningAlgorithmRSA, "posta", 1)
	svc := NewDkimSigningService(&stubKeyResolver{keys: map[string]*models.DomainSigningKey{"example.com": key}})

	signed, err := svc.SignForDomain(goldenMessage, "Example.COM") // case-insensitive
	if err != nil {
		t.Fatalf("SignForDomain: %v", err)
	}
	if bytes.Equal(signed, goldenMessage) {
		t.Fatal("signed message must differ from the input")
	}
	if !bytes.Contains(signed, []byte("DKIM-Signature:")) {
		t.Fatalf("signed message has no DKIM-Signature header:\n%s", signed)
	}
	// The signature is prepended before From.
	if !bytes.HasPrefix(signed, []byte("DKIM-Signature:")) {
		t.Fatalf("DKIM-Signature must be prepended:\n%s", signed)
	}

	txt, err := DnsTXTRecord(key.PrivateKeyPem, models.SigningAlgorithmRSA)
	if err != nil {
		t.Fatalf("DnsTXTRecord: %v", err)
	}
	verif := verifyWithStubDNS(t, signed, "example.com", "posta", txt)
	if verif.Err != nil {
		t.Fatalf("golden RSA signature does not verify: %v", verif.Err)
	}
	if verif.Domain != "example.com" {
		t.Fatalf("verification identity wrong: domain=%q", verif.Domain)
	}
}

func TestDkimGoldenEd25519Signature(t *testing.T) {
	key := storeKeyPem(t, testEd25519KeyPem, models.SigningAlgorithmEd25519, "s2026", 2)
	svc := NewDkimSigningService(&stubKeyResolver{keys: map[string]*models.DomainSigningKey{"example.org": key}})

	signed, err := svc.SignForDomain(goldenMessage, "example.org")
	if err != nil {
		t.Fatalf("SignForDomain: %v", err)
	}

	txt, err := DnsTXTRecord(key.PrivateKeyPem, models.SigningAlgorithmEd25519)
	if err != nil {
		t.Fatalf("DnsTXTRecord: %v", err)
	}
	if !strings.Contains(txt, "k=ed25519") {
		t.Fatalf("ed25519 record must declare k=ed25519: %s", txt)
	}
	verif := verifyWithStubDNS(t, signed, "example.org", "s2026", txt)
	if verif.Err != nil {
		t.Fatalf("golden ed25519 signature does not verify: %v", verif.Err)
	}
}

// A message tampered after signing must fail verification — the point of DKIM.
func TestDkimSignatureCatchesTampering(t *testing.T) {
	key := storeKeyPem(t, testRSAKeyPem, models.SigningAlgorithmRSA, "posta", 1)
	svc := NewDkimSigningService(&stubKeyResolver{keys: map[string]*models.DomainSigningKey{"example.com": key}})
	signed, err := svc.SignForDomain(goldenMessage, "example.com")
	if err != nil {
		t.Fatalf("SignForDomain: %v", err)
	}

	tampered := bytes.Replace(signed, []byte("Body line."), []byte("Body EDITED."), 1)
	txt, _ := DnsTXTRecord(key.PrivateKeyPem, models.SigningAlgorithmRSA)
	verif := verifyWithStubDNS(t, tampered, "example.com", "posta", txt)
	if verif.Err == nil {
		t.Fatal("tampered body must fail verification")
	}
}

func TestDkimNoKeyUnsignedPassthrough(t *testing.T) {
	svc := NewDkimSigningService(&stubKeyResolver{keys: map[string]*models.DomainSigningKey{}})
	out, err := svc.SignForDomain(goldenMessage, "unsigned.example")
	if err != nil {
		t.Fatalf("unsigned domain must not error: %v", err)
	}
	if !bytes.Equal(out, goldenMessage) {
		t.Fatal("unsigned domain must pass the message through unchanged")
	}
}

// An Active key whose DNS record is not confirmed yet must not sign: a
// signature the receiver cannot resolve verifies as fail, which is worse than
// not signing.
func TestDkimUnpublishedKeyDoesNotSign(t *testing.T) {
	key := storeKeyPem(t, testRSAKeyPem, models.SigningAlgorithmRSA, "posta", 1)
	key.DNSState = models.DomainSigningDNSPending
	svc := NewDkimSigningService(&stubKeyResolver{keys: map[string]*models.DomainSigningKey{"example.com": key}})
	out, err := svc.SignForDomain(goldenMessage, "example.com")
	if err != nil {
		t.Fatalf("unpublished key must pass through, not error: %v", err)
	}
	if !bytes.Equal(out, goldenMessage) {
		t.Fatal("unpublished key must leave the message unsigned")
	}
}

// A resolver failure is an error, not silent unsigned mail — the send fails
// and asynq retries (documented R-02 policy).
func TestDkimResolverErrorFails(t *testing.T) {
	svc := NewDkimSigningService(&stubKeyResolver{err: errors.New("db unavailable")})
	_, err := svc.SignForDomain(goldenMessage, "example.com")
	if err == nil {
		t.Fatal("resolver error must fail the sign")
	}
}

func TestDkimUnusableStoredKeyFails(t *testing.T) {
	enc, _ := postacrypto.Encrypt("this is not a pem block")
	key := &models.DomainSigningKey{
		Selector: "posta", Algorithm: models.SigningAlgorithmRSA,
		PrivateKeyPem: enc, Status: models.DomainSigningStatusActive,
		DNSState: models.DomainSigningDNSPublished,
	}
	svc := NewDkimSigningService(&stubKeyResolver{keys: map[string]*models.DomainSigningKey{"example.com": key}})
	if _, err := svc.SignForDomain(goldenMessage, "example.com"); err == nil {
		t.Fatal("an unusable stored key must fail the sign, not ship unsigned mail")
	}
}

func TestDnsTXTRecordShapes(t *testing.T) {
	encRSA, _ := postacrypto.Encrypt(testRSAKeyPem)
	txt, err := DnsTXTRecord(encRSA, models.SigningAlgorithmRSA)
	if err != nil {
		t.Fatalf("rsa record: %v", err)
	}
	if !strings.HasPrefix(txt, "v=DKIM1; h=sha256; k=rsa; p=") {
		t.Fatalf("rsa record shape wrong: %s", txt)
	}
	p := tagValue(txt, "p")
	der, err := base64.StdEncoding.DecodeString(p)
	if err != nil {
		t.Fatalf("p= is not base64: %v", err)
	}
	pub, err := x509.ParsePKCS1PublicKey(der)
	if err != nil {
		t.Fatalf("p= is not a PKCS#1 RSA public key: %v", err)
	}
	if pub.N.BitLen() != 2048 {
		t.Fatalf("unexpected key size %d", pub.N.BitLen())
	}

	encEd, _ := postacrypto.Encrypt(testEd25519KeyPem)
	txt, err = DnsTXTRecord(encEd, models.SigningAlgorithmEd25519)
	if err != nil {
		t.Fatalf("ed25519 record: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(tagValue(txt, "p"))
	if err != nil {
		t.Fatalf("ed25519 p= is not base64: %v", err)
	}
	// RFC 8463: p= is the raw 32-octet public key.
	if len(raw) != ed25519.PublicKeySize {
		t.Fatalf("ed25519 p= decodes to %d bytes, want 32", len(raw))
	}
}

func TestGenerateSigningKeyPemRoundTrip(t *testing.T) {
	for _, alg := range []string{models.SigningAlgorithmRSA, models.SigningAlgorithmEd25519} {
		enc, err := GenerateSigningKeyPem(alg)
		if err != nil {
			t.Fatalf("generate %s: %v", alg, err)
		}
		// The stored key parses back, and its DNS record matches the algorithm.
		signer, err := ParseSigningKeyPem(enc, alg)
		if err != nil {
			t.Fatalf("parse back %s: %v", alg, err)
		}
		switch alg {
		case models.SigningAlgorithmRSA:
			if _, ok := signer.(*rsa.PrivateKey); !ok {
				t.Fatalf("rsa round-trip produced %T", signer)
			}
		case models.SigningAlgorithmEd25519:
			if _, ok := signer.(ed25519.PrivateKey); !ok {
				t.Fatalf("ed25519 round-trip produced %T", signer)
			}
		}
		if _, err := DnsTXTRecord(enc, alg); err != nil {
			t.Fatalf("dns record for generated %s key: %v", alg, err)
		}
	}
	if _, err := GenerateSigningKeyPem("dsa-sha1"); err == nil {
		t.Fatal("unsupported algorithm must be rejected")
	}
}

func TestValidSelector(t *testing.T) {
	good := []string{"posta", "mail2026", "s1._domainkey-test", "a.b.c", "Selector-1"}
	bad := []string{"", "post a", "-leading", "trailing.", "double..dot", "ümlaut", strings.Repeat("x", 64)}
	for _, s := range good {
		if !ValidSelector(s) {
			t.Errorf("selector %q must be valid", s)
		}
	}
	for _, s := range bad {
		if ValidSelector(s) {
			t.Errorf("selector %q must be invalid", s)
		}
	}
}

func TestVerifyDNSPublication(t *testing.T) {
	prev := TestLookupTXT
	t.Cleanup(func() { TestLookupTXT = prev })

	goodRecord := "v=DKIM1; h=sha256; k=rsa; p=MIIBIjANBg"
	TestLookupTXT = func(host string) ([]string, error) {
		if host == "posta._domainkey.example.com" {
			return []string{goodRecord}, nil
		}
		return nil, errors.New("NXDOMAIN")
	}
	rec, err := VerifyDNSPublication("example.com", "posta", models.SigningAlgorithmRSA)
	if err != nil || rec != goodRecord {
		t.Fatalf("published record not found: %q, %v", rec, err)
	}

	// ed25519 requires k=ed25519 explicitly.
	TestLookupTXT = func(string) ([]string, error) { return []string{"v=DKIM1; k=rsa; p=x"}, nil }
	if _, err := VerifyDNSPublication("example.com", "posta", models.SigningAlgorithmEd25519); err == nil {
		t.Fatal("an rsa record must not verify for an ed25519 key")
	}

	// Nothing in DNS.
	TestLookupTXT = func(string) ([]string, error) { return nil, errors.New("NXDOMAIN") }
	if _, err := VerifyDNSPublication("example.com", "posta", models.SigningAlgorithmRSA); err == nil {
		t.Fatal("missing record must be an error")
	}

	// A record without p= is not a DKIM key record.
	TestLookupTXT = func(string) ([]string, error) { return []string{"some other txt"}, nil }
	if _, err := VerifyDNSPublication("example.com", "posta", models.SigningAlgorithmRSA); err == nil {
		t.Fatal("a TXT record without p= must not verify")
	}
}

// ---------- the SMTPSender signing hook ----------

type stubHookSigner struct {
	called int
	domain string
	fail   bool
}

func (s *stubHookSigner) SignForDomain(msg []byte, domain string) ([]byte, error) {
	s.called++
	s.domain = domain
	if s.fail {
		return nil, errors.New("simulated signing failure")
	}
	signed := append([]byte("DKIM-Signature: v=1; a=rsa-sha256; s=stub\r\n"), msg...)
	return signed, nil
}

// A sender with a pinned signer signs before the SMTP conversation: the relay
// receives the message with the DKIM-Signature header, computed for the
// envelope sender's domain.
func TestSMTPSenderSignsWhenSignerPinned(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	sessions := make(chan []string, 4)
	go serveHappyRelay(ln, sessions)

	hook := &stubHookSigner{}
	sender := NewSMTPSender()
	sender.SetDkimSigner(hook)
	server := &models.SMTPServer{Host: "127.0.0.1", Port: portOf(t, ln), Encryption: models.EncryptionNone}
	if err := sender.Send(server, "Sender <user@example.com>", []string{"rcpt@example.com"},
		"subject", "", "body", nil, nil, "", "", false); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case <-sessions:
	case <-time.After(5 * time.Second):
		t.Fatal("relay saw no complete session")
	}
	if hook.called != 1 || hook.domain != "example.com" {
		t.Fatalf("signer hook called=%d domain=%q, want 1/example.com", hook.called, hook.domain)
	}
}

// The process-wide signer (SetDkimSigner) applies to senders constructed
// without a pinned one — that is how server.go/worker.go wire it once.
func TestSMTPSenderPackageLevelSigner(t *testing.T) {
	hook := &stubHookSigner{}
	SetDkimSigner(hook)
	t.Cleanup(func() { SetDkimSigner(nil) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	sessions := make(chan []string, 4)
	go serveHappyRelay(ln, sessions)

	sender := NewSMTPSender()
	server := &models.SMTPServer{Host: "127.0.0.1", Port: portOf(t, ln), Encryption: models.EncryptionNone}
	if err := sender.Send(server, "user@campaigns.example", []string{"rcpt@example.com"},
		"subject", "", "body", nil, nil, "", "", false); err != nil {
		t.Fatalf("send: %v", err)
	}
	<-sessions
	if hook.called != 1 || hook.domain != "campaigns.example" {
		t.Fatalf("package-level signer not applied: called=%d domain=%q", hook.called, hook.domain)
	}
}

// A signing failure fails the send (never silently unsigned) — the error the
// asynq task retries on.
func TestSMTPSenderSigningErrorFailsSend(t *testing.T) {
	hook := &stubHookSigner{fail: true}
	sender := NewSMTPSender()
	sender.SetDkimSigner(hook)
	server := &models.SMTPServer{Host: "127.0.0.1", Port: 1, Encryption: models.EncryptionNone}
	err := sender.Send(server, "user@example.com", []string{"rcpt@example.com"},
		"subject", "", "body", nil, nil, "", "", false)
	if err == nil || !strings.Contains(err.Error(), "DKIM signing failed") {
		t.Fatalf("signing failure must fail the send with a DKIM error, got: %v", err)
	}
}
