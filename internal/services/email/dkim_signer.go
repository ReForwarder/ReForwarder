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
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/goposta/posta/internal/models"
	postacrypto "github.com/goposta/posta/internal/services/crypto"
	"gorm.io/gorm"
)

// Signing policy (roadmap R-02):
//
//   - No Active+published key for the sender domain: the message goes out
//     unsigned. That is a valid mode — an upstream signer (selector CNAME,
//     e.g. posta._domainkey) or the relay may sign instead.
//   - A lookup, key-parse or signing ERROR fails the send. Mis-signing is
//     worse than not signing, but silently dropping a configured signature is
//     not acceptable either: the send fails with a clear error and asynq
//     retries it — a transient DB/crypto problem heals, a misconfiguration
//     surfaces in the failure logs instead of shipping unmarked mail.
//
// The private key is stored encrypted (crypto.Encrypt) and is only ever
// decrypted here, in-process, for signing. It never appears in an API
// response (models.DomainSigningKey.PrivateKeyPem has no JSON tag).

// DkimKeyResolver looks up the signing key for a sending domain. Implemented
// by *repositories.DomainSigningKeyRepository; narrowed to an interface so
// tests can stub lookups.
type DkimKeyResolver interface {
	FindActiveByDomain(domain string) (*models.DomainSigningKey, error)
}

// TestLookupTXT, when non-nil, replaces the DNS TXT lookup used to confirm
// key publication. Test seam only; nil in production.
var TestLookupTXT func(host string) ([]string, error)

// lookupTXT resolves through TestLookupTXT when installed.
func lookupTXT(host string) ([]string, error) {
	if TestLookupTXT != nil {
		return TestLookupTXT(host)
	}
	return net.LookupTXT(host)
}

// DkimSigningService is the concrete signing service: it resolves the managed
// key for the sender domain and produces the DKIM-Signature header. It
// satisfies the DkimSigner interface declared by the SMTP sender hook.
type DkimSigningService struct {
	resolver DkimKeyResolver
}

func NewDkimSigningService(resolver DkimKeyResolver) *DkimSigningService {
	return &DkimSigningService{resolver: resolver}
}

// SignForDomain signs msg for the given sender domain. The message is returned
// unchanged when the domain has no Active + published signing key; an error is
// returned when a configured key cannot be applied (see the policy above).
func (s *DkimSigningService) SignForDomain(msg []byte, domain string) ([]byte, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if s == nil || s.resolver == nil || domain == "" {
		return msg, nil
	}

	key, err := s.resolver.FindActiveByDomain(domain)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return msg, nil // no managed key for this domain: unsigned is valid
		}
		return nil, fmt.Errorf("dkim: key lookup for %q failed: %w", domain, err)
	}
	if !key.CanSign() {
		// Defense in depth: the repository already filters for
		// Active + published, but a stale caller-provided key must not sign
		// before its DNS record is confirmed.
		return msg, nil
	}

	signer, err := ParseSigningKeyPem(key.PrivateKeyPem, key.Algorithm)
	if err != nil {
		return nil, fmt.Errorf("dkim: stored key %q (selector %s) is unusable: %w", domain, key.Selector, err)
	}

	opts := &dkim.SignOptions{
		Domain:                 domain,
		Selector:               key.Selector,
		Signer:                 signer,
		Hash:                   crypto.SHA256,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		// RFC 6376 §5.4.1 recommended set; From is mandatory in the list.
		HeaderKeys: []string{
			"From", "To", "Subject", "Date", "Message-ID",
			"MIME-Version", "Content-Type", "Content-Transfer-Encoding",
			"List-Unsubscribe", "List-Unsubscribe-Post",
		},
	}

	var signed bytes.Buffer
	if err := dkim.Sign(&signed, bytes.NewReader(msg), opts); err != nil {
		return nil, fmt.Errorf("dkim: signing for %q failed: %w", domain, err)
	}
	return signed.Bytes(), nil
}

// ParseSigningKeyPem decrypts and parses a stored private key.
func ParseSigningKeyPem(encryptedPem, algorithm string) (crypto.Signer, error) {
	pemStr, err := postacrypto.Decrypt(encryptedPem)
	if err != nil {
		return nil, fmt.Errorf("decrypt private key: %w", err)
	}
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("pem: no block found")
	}
	switch algorithm {
	case models.SigningAlgorithmRSA:
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			// Also accept PKCS#8 so a key pasted from OpenSSL -outform PEM
			// ("BEGIN PRIVATE KEY") works too.
			k8, err8 := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err8 != nil {
				return nil, fmt.Errorf("parse rsa private key: %w", err)
			}
			rsaKey, ok := k8.(*rsa.PrivateKey)
			if !ok {
				return nil, errors.New("pem: PKCS#8 key is not RSA")
			}
			return rsaKey, nil
		}
		return key, nil
	case models.SigningAlgorithmEd25519:
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse ed25519 private key: %w", err)
		}
		key, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("pem: key is not Ed25519")
		}
		return key, nil
	default:
		return nil, fmt.Errorf("unsupported signing algorithm %q", algorithm)
	}
}

// GenerateSigningKeyPem creates a new signing key pair and returns the private
// key PEM-encrypted via crypto (the caller stores the ciphertext).
func GenerateSigningKeyPem(algorithm string) (encryptedPem string, err error) {
	var der []byte
	var blockType string
	switch algorithm {
	case models.SigningAlgorithmRSA:
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return "", fmt.Errorf("generate rsa key: %w", err)
		}
		der = x509.MarshalPKCS1PrivateKey(key)
		blockType = "RSA PRIVATE KEY"
	case models.SigningAlgorithmEd25519:
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return "", fmt.Errorf("generate ed25519 key: %w", err)
		}
		der, err = x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return "", fmt.Errorf("marshal ed25519 key: %w", err)
		}
		blockType = "PRIVATE KEY"
	default:
		return "", fmt.Errorf("unsupported signing algorithm %q (use rsa-sha256 or ed25519-sha256)", algorithm)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
	return postacrypto.Encrypt(pemStr)
}

// DnsTXTRecord builds the public DNS TXT record for a stored private key:
// "<selector>._domainkey.<domain> TXT v=DKIM1; h=sha256; k=<alg>; p=<base64>".
// The value is what the operator publishes; it contains public material only.
func DnsTXTRecord(encryptedPem, algorithm string) (string, error) {
	pemStr, err := postacrypto.Decrypt(encryptedPem)
	if err != nil {
		return "", fmt.Errorf("decrypt private key: %w", err)
	}
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return "", errors.New("pem: no block found")
	}

	var pubBase64 string
	var k string
	switch algorithm {
	case models.SigningAlgorithmRSA:
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("parse rsa private key: %w", err)
		}
		pubBase64 = base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PublicKey(&key.PublicKey))
		k = "rsa"
	case models.SigningAlgorithmEd25519:
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("parse ed25519 private key: %w", err)
		}
		key, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return "", errors.New("pem: key is not Ed25519")
		}
		// RFC 8463: p= is the Base64 of the raw 32-octet Ed25519 public key
		// (not the SPKI wrapper) — what verifiers, go-msgauth included,
		// expect to decode.
		pub, ok := key.Public().(ed25519.PublicKey)
		if !ok {
			return "", errors.New("pem: key is not Ed25519")
		}
		pubBase64 = base64.StdEncoding.EncodeToString(pub)
		k = "ed25519"
	default:
		return "", fmt.Errorf("unsupported signing algorithm %q", algorithm)
	}

	return fmt.Sprintf("v=DKIM1; h=sha256; k=%s; p=%s", k, pubBase64), nil
}

// selectorRe matches a legal DKIM selector: RFC 6376 allows alphanumerics,
// dots, hyphens and underscores (underscores are conventional in DNS key
// record names, e.g. selectors under _domainkey), labels must not start or
// end with a hyphen, and the DNS label cap of 63 applies per label.
var selectorRe = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?(\.[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?)*$`)

// ValidSelector reports whether s is a usable DKIM selector.
func ValidSelector(s string) bool {
	if s == "" || len(s) > 63*4 {
		return false
	}
	if !selectorRe.MatchString(s) {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
	}
	return true
}

// ValidSigningAlgorithm reports whether algorithm is one of the supported
// DKIM algorithms.
func ValidSigningAlgorithm(algorithm string) bool {
	return algorithm == models.SigningAlgorithmRSA || algorithm == models.SigningAlgorithmEd25519
}

// VerifyDNSPublication checks that the TXT record for selector._domainkey.domain
// is present in DNS and looks like a DKIM key for the expected algorithm. It
// returns the record found, or an error when DNS has nothing usable yet (the
// caller records a pending-retry state).
func VerifyDNSPublication(domain, selector, algorithm string) (string, error) {
	host := selector + "._domainkey." + strings.TrimSuffix(strings.ToLower(domain), ".")
	records, err := lookupTXT(host)
	if err != nil {
		return "", fmt.Errorf("dns lookup %s failed: %w", host, err)
	}
	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if !strings.Contains(strings.ToLower(rec), "p=") {
			continue
		}
		if v := tagValue(rec, "v"); v != "" && !strings.EqualFold(strings.TrimSpace(v), "DKIM1") {
			continue
		}
		k := tagValue(rec, "k")
		switch algorithm {
		case models.SigningAlgorithmRSA:
			if k != "" && !strings.EqualFold(k, "rsa") {
				continue
			}
		case models.SigningAlgorithmEd25519:
			if !strings.EqualFold(k, "ed25519") {
				// RFC 8463 requires k=ed25519 to be explicit.
				continue
			}
		}
		return rec, nil
	}
	return "", fmt.Errorf("no usable DKIM TXT record found at %s", host)
}

// tagValue extracts one tag from a DKIM key record ("v=DKIM1; k=rsa; p=...").
func tagValue(record, tag string) string {
	for _, part := range strings.Split(record, ";") {
		part = strings.TrimSpace(part)
		if eq := strings.Index(part, "="); eq > 0 && strings.EqualFold(strings.TrimSpace(part[:eq]), tag) {
			return strings.TrimSpace(part[eq+1:])
		}
	}
	return ""
}
