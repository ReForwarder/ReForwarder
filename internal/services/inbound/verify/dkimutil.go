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

package verify

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

// DKIM verification primitives (RFC 6376) shared by the ARC chain validator
// (RFC 8617 imports DKIM canonicalization, key handling and signature
// verification wholesale). The DKIM-Signature engine itself stays on
// go-msgauth (dkim.go); these helpers exist because go-msgauth's
// canonicalizers are unexported and its verifier only recognizes
// DKIM-Signature fields — ARC-Message-Signature and ARC-Seal need the same
// mechanics under different field names.
//
// Behaviors mirror go-msgauth v0.7.0 (the DKIM verifier this package already
// interoperates with) wherever RFC 6376 leaves an implementation choice:
// rsa-sha1 is rejected (RFC 8301 §3.1), RSA keys under 1024 bits are
// rejected (RFC 8301 §3.2), the l= body-length tag is rejected as insecure
// (RFC 6376 §8.2), key records are single TXT records parsed PKIX-first
// (erratum 3017), and bare CR/LF are treated as line breaks (RFC 6376 §5.3
// requires network-normal input; the receiver's DATA path already is).

// sigTags is one parsed RFC 6376 §3.2 tag=value list (signature or key).
type sigTags map[string]string

// parseSigTags parses a tag=value list. Tag names are compared
// case-insensitively (duplicate detection included — RFC 6376 §3.2 makes a
// duplicated tag invalidate the whole list); values keep their internal
// whitespace, with FWS trimmed at the edges. A trailing semicolon is legal.
func parseSigTags(s string) (sigTags, error) {
	out := sigTags{}
	for _, part := range strings.Split(s, ";") {
		if strings.TrimSpace(part) == "" {
			continue // empty spec: legal as the trailing semicolon
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("tag without '=' in %.32q", part)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("empty tag name in %.32q", part)
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
				return nil, fmt.Errorf("invalid tag name %q", name)
			}
		}
		lower := strings.ToLower(name)
		if _, dup := out[lower]; dup {
			return nil, fmt.Errorf("duplicate tag %q", name)
		}
		out[lower] = strings.TrimSpace(value)
	}
	return out, nil
}

// get returns the tag value (case-insensitive lookup) or "".
func (t sigTags) get(name string) string { return t[strings.ToLower(name)] }

// has reports whether the tag is present.
func (t sigTags) has(name string) bool {
	_, ok := t[strings.ToLower(name)]
	return ok
}

// stripSigFWS removes ALL whitespace from a base64 or list value (RFC 6376
// allows FWS inside b=/bh= values; it is not part of the data).
func stripSigFWS(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// ---------- raw message parsing ----------

// rawHeader is one header field exactly as it appears in the message:
// folding preserved, trailing CRLF excluded (callers add it when hashing).
type rawHeader struct {
	name string // lowercased field name (before the first colon)
	raw  string // the raw field bytes, internal folding intact
}

// rawMessage is a parsed RFC 5322 message: the raw header fields and the
// untouched body bytes.
type rawMessage struct {
	headers []rawHeader
	body    []byte
}

// parseRawMessage splits a raw message into header fields and body. Both
// CRLF and lone-LF separators are tolerated (canonicalization normalizes
// them). A message with no blank separator line is all headers with an
// empty body.
func parseRawMessage(raw []byte) *rawMessage {
	head, body := raw, []byte(nil)
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		head, body = raw[:i], raw[i+4:]
	} else if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		head, body = raw[:i], raw[i+2:]
	}

	msg := &rawMessage{body: body}
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		field := cur.String()
		cur.Reset()
		name, _, _ := strings.Cut(field, ":")
		msg.headers = append(msg.headers, rawHeader{
			name: strings.ToLower(strings.TrimSpace(name)),
			raw:  field,
		})
	}
	for _, line := range strings.Split(string(head), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			// Continuation: keep the folding (CRLF + leading WSP) verbatim —
			// simple canonicalization hashes it as-is.
			if cur.Len() > 0 {
				cur.WriteString("\r\n")
				cur.WriteString(line)
			}
			continue
		}
		flush()
		cur.WriteString(line)
	}
	flush()
	return msg
}

// picker selects header fields for signing per the RFC 6376 §5.4.2 rule:
// for each name in h= (left to right) the physically LAST still-unpicked
// field with that name is chosen, bottom up. Names of fields that do not
// exist contribute nothing.
type picker struct {
	h    []rawHeader
	used map[int]bool
	at   map[string]int
}

func newPicker(h []rawHeader) *picker {
	return &picker{h: h, used: map[int]bool{}, at: map[string]int{}}
}

func (p *picker) pick(name string) string {
	name = strings.ToLower(name)
	seen := p.at[name]
	for i := len(p.h) - 1; i >= 0; i-- {
		if p.h[i].name != name || p.used[i] {
			continue
		}
		if seen > 0 {
			seen--
			continue
		}
		p.used[i] = true
		p.at[name]++
		return p.h[i].raw
	}
	return ""
}

// ---------- canonicalization (RFC 6376 §3.4) ----------

// canonHeaderSimple is RFC 6376 §3.4.1: the field exactly as it appears,
// plus its CRLF terminator.
func canonHeaderSimple(raw string) string { return raw + "\r\n" }

// canonHeaderRelaxed is RFC 6376 §3.4.2: lowercase name, unfold, collapse
// WSP runs to one SP, delete WSP at the end of the value and around the
// colon; result ends with a single CRLF.
func canonHeaderRelaxed(raw string) string {
	name, value, ok := strings.Cut(raw, ":")
	if !ok {
		return strings.ToLower(strings.TrimSpace(raw)) + ":\r\n"
	}
	name = strings.ToLower(strings.TrimSpace(name))
	return name + ":" + collapseWSP(value) + "\r\n"
}

// collapseWSP unfolds (drops CR/LF), collapses WSP runs to one SP and trims
// the result — the RFC 6376 §3.4.2 value treatment.
func collapseWSP(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevWS := false
	for _, c := range s {
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			prevWS = true
			continue
		}
		if prevWS && b.Len() > 0 {
			b.WriteByte(' ')
		}
		prevWS = false
		b.WriteRune(c)
	}
	return strings.Trim(b.String(), " ")
}

// canonBodySimple is RFC 6376 §3.4.3: normalize line endings, convert every
// run of trailing empty lines to exactly one CRLF. A completely empty body
// canonicalizes to a single CRLF (SHA-256
// frcCV1k9oG9oKj3dpUqdJg1PxRT2RSN/XKdLCPjaYaY=).
func canonBodySimple(body []byte) []byte {
	lines := splitBodyLines(body)
	joined := strings.Join(lines, "\r\n")
	return []byte(strings.TrimRight(joined, "\r\n") + "\r\n")
}

// canonBodyRelaxed is RFC 6376 §3.4.4: per line, ignore WSP at the end and
// reduce WSP runs to one SP (a leading run becomes one leading SP); ignore
// trailing empty lines entirely. A completely empty body canonicalizes to
// the empty string (SHA-256 47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=).
func canonBodyRelaxed(body []byte) []byte {
	lines := splitBodyLines(body)
	canon := make([]string, len(lines))
	last := -1
	for i, ln := range lines {
		var b strings.Builder
		b.Grow(len(ln))
		prevWS := false
		for _, c := range ln {
			if c == ' ' || c == '\t' {
				prevWS = true
				continue
			}
			if prevWS {
				b.WriteByte(' ')
				prevWS = false
			}
			b.WriteRune(c)
		}
		canon[i] = b.String()
		if b.Len() > 0 {
			last = i
		}
	}
	var out bytes.Buffer
	out.Grow(len(body))
	for i := 0; i <= last; i++ {
		out.WriteString(canon[i])
		out.WriteString("\r\n")
	}
	return out.Bytes()
}

// splitBodyLines splits the body into logical lines WITHOUT terminators.
// Lone LF and lone CR are treated as line breaks (RFC 6376 §5.3: bare
// CR/LF must be normalized before hashing).
func splitBodyLines(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	s := strings.ReplaceAll(string(body), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// ---------- signatures and keys ----------

var bTagRE = regexp.MustCompile(`(b[ \t\r\n]*=)[^;]+`)

// removeSigValue empties the b= tag value of a signature header field so the
// field can be hashed (RFC 6376 §3.7: the b= value "including all
// surrounding whitespace" is deleted).
func removeSigValue(field string) string {
	return bTagRE.ReplaceAllString(field, "$1")
}

// sigAlgoSupported reports whether the a= algorithm is one we verify. Only
// sha256-based algorithms are accepted: RFC 8301 §3.1 retired rsa-sha1 and
// ARC (RFC 8617) never had it.
func sigAlgoSupported(algo string) bool {
	return algo == "rsa-sha256" || algo == "ed25519-sha256"
}

// dkimKey is a parsed RFC 6376 §3.6.1 public-key record.
type dkimKey struct {
	algo   string // "rsa" | "ed25519"
	rsa    *rsa.PublicKey
	ed     ed25519.PublicKey
	hashes []string // nil = unrestricted (key record without h=)
	raw    string   // the record text (s= service checks)
}

// fetchDKIMKey retrieves and parses the key record at
// <selector>._domainkey.<domain> through the resolver seam.
func fetchDKIMKey(ctx context.Context, res Resolver, selector, domain string) (*dkimKey, error) {
	txts, err := res.LookupTXT(ctx, selector+"._domainkey."+domain)
	if err != nil {
		if ErrNotFound(err) {
			return nil, fmt.Errorf("no key for signature %s._domainkey.%s", selector, domain)
		}
		return nil, fmt.Errorf("key unavailable for %s._domainkey.%s: %v", selector, domain, err)
	}
	switch len(txts) {
	case 0:
		return nil, fmt.Errorf("no key for signature %s._domainkey.%s", selector, domain)
	case 1:
		return parseDKIMKey(txts[0])
	default:
		return nil, fmt.Errorf("multiple TXT records for %s._domainkey.%s", selector, domain)
	}
}

// parseDKIMKey validates one TXT record per RFC 6376 §6.1.2 steps 5-8.
func parseDKIMKey(rec string) (*dkimKey, error) {
	tags, err := parseSigTags(rec)
	if err != nil {
		return nil, fmt.Errorf("key record error: %v", err)
	}
	if v := tags.get("v"); v != "" && v != "DKIM1" {
		return nil, fmt.Errorf("incompatible key version %q", v)
	}
	p := stripSigFWS(tags.get("p"))
	if p == "" {
		if tags.has("p") {
			return nil, fmt.Errorf("key revoked")
		}
		return nil, fmt.Errorf("key syntax error: missing p=")
	}
	der, err := base64.StdEncoding.DecodeString(p)
	if err != nil {
		return nil, fmt.Errorf("key syntax error: %v", err)
	}
	key := &dkimKey{algo: tags.get("k"), raw: rec}
	if key.algo == "" {
		key.algo = "rsa"
	}
	switch key.algo {
	case "rsa":
		pub, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			// RFC 6376 is inconsistent about RSAPublicKey vs
			// SubjectPublicKeyInfo encoding (erratum 3017) — try both.
			pub, err = x509.ParsePKCS1PublicKey(der)
			if err != nil {
				return nil, fmt.Errorf("key syntax error: %v", err)
			}
		}
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("key syntax error: not an RSA public key")
		}
		if rsaPub.Size()*8 < 1024 {
			return nil, fmt.Errorf("RSA key too short (%d bits, want >= 1024)", rsaPub.Size()*8)
		}
		key.rsa = rsaPub
	case "ed25519":
		if len(der) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid Ed25519 public key size %d", len(der))
		}
		key.ed = ed25519.PublicKey(der)
	default:
		return nil, fmt.Errorf("unsupported key algorithm %q", key.algo)
	}
	if h := tags.get("h"); h != "" {
		for _, alg := range strings.Split(h, ":") {
			key.hashes = append(key.hashes, strings.ToLower(stripSigFWS(alg)))
		}
	}
	return key, nil
}

// verifySig checks the b= signature over the SHA-256 data digest for the
// given a= algorithm.
func verifySig(algo string, key *dkimKey, digest, sig []byte) error {
	switch algo {
	case "rsa-sha256":
		if key.algo != "rsa" {
			return fmt.Errorf("key algorithm %q does not match %s", key.algo, algo)
		}
		return rsa.VerifyPKCS1v15(key.rsa, crypto.SHA256, digest, sig)
	case "ed25519-sha256":
		if key.algo != "ed25519" {
			return fmt.Errorf("key algorithm %q does not match %s", key.algo, algo)
		}
		if !ed25519.Verify(key.ed, digest, sig) {
			return fmt.Errorf("ed25519 signature did not verify")
		}
		return nil
	default:
		return fmt.Errorf("unsupported signature algorithm %q", algo)
	}
}

// decodeSigB decodes a b= value, ignoring embedded FWS.
func decodeSigB(v string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(stripSigFWS(v))
}

// keyHashAllowed applies the key record's h= restriction (RFC 6376 §6.1.2
// step 6): a nil list is unrestricted. Every accepted a= is sha256-based.
func keyHashAllowed(key *dkimKey, algo string) bool {
	if key.hashes == nil {
		return true
	}
	want := "sha256" // both supported algorithms hash with SHA-256
	for _, h := range key.hashes {
		if h == want {
			return true
		}
	}
	return false
}

// keyServiceAllowed applies the key record's s= service restriction
// (RFC 6376 §3.6.1): absent or "*" means any service.
func keyServiceAllowed(rec string) bool {
	tags, err := parseSigTags(rec)
	if err != nil {
		return true
	}
	s := tags.get("s")
	if s == "" {
		return true
	}
	for _, svc := range strings.Split(s, ":") {
		svc = stripSigFWS(svc)
		if svc == "*" || svc == "email" {
			return true
		}
	}
	return false
}
