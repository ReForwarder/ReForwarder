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
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ARC chain validation (SF-27, ADR-002 decision 6): parse the
// ARC-Authentication-Results / ARC-Message-Signature / ARC-Seal sets on a
// message and validate the seal chain per RFC 8617 §5.2, producing the
// chain-validation result cv=none|pass|fail. This is ANNOTATION ONLY —
// the verdict lands in Authentication-Results and the verdict row; nothing
// here rejects, bounces or quarantines mail. There is no sealing: ReForwarder
// never adds ARC sets to forwarded mail.
//
// Algorithm (RFC 8617 §5.2, in order):
//  1. no ARC set → none; more than 50 sets → fail.
//  2. cv=fail on the highest-instance set → fail.
//  3. structure: exactly one AAR+AMS+AS per set, instances contiguous
//     1..N, cv=none on instance 1 and cv=pass on every later instance.
//  4. verify the most recent AMS (DKIM verification) → failure = fail.
//  6. verify every AS from the highest instance down to 1 → any failure
//     = fail.
//  7. otherwise pass.
// Per §5.2.1 every validation failure is permanent — including DNS errors —
// so the result vocabulary is exactly none|pass|fail.
//
// The AS hash input (§5.1.1 with §4.1.3): the ARC set header fields of
// instances 1..k in instance order (AAR, AMS, AS within a set),
// relaxed-canonicalized, each terminated with CRLF except the seal being
// verified, whose b= value is deleted and whose trailing CRLF is omitted
// (the RFC 6376 §3.7 convention). The seal covers no body ("no body
// canonicalization is performed as the AS signature does not cover the
// body of a message").

const (
	// ARC result values (RFC 8617 §5.2; the arc= Authentication-Results
	// method registers exactly these three).
	ARCNone = "none"
	ARCPass = "pass"
	ARCFail = "fail"

	// arcMaxSets is the RFC 8617 instance ceiling: chains with more than 50
	// ARC sets fail (§5.2 step 1; instance values range 1-50, §3.9).
	arcMaxSets = 50
)

// ARCVerdict is the result of validating a message's ARC seal chain.
type ARCVerdict struct {
	// Result is none (no ARC set), pass or fail.
	Result string
	// Domain is the d= of the most recent seal — the forwarder that vouches
	// for the chain — empty when there is no usable seal.
	Domain string
	// Reason explains a fail verdict (or the absence of a chain).
	Reason string
}

// ARCEvaluator validates ARC chains on raw messages. Resolver serves the
// DKIM-style key lookups; nil uses the Go default resolver.
type ARCEvaluator struct {
	Resolver Resolver
}

// arcSet is one ARC set: the three header fields of one instance, exactly
// as they appear in the message (folding intact, no trailing CRLF).
type arcSet struct {
	instance int
	aar      string
	ams      string
	seal     string
	sealTags sigTags
	cv       string // lowercase cv= of the seal
}

// Verify runs the RFC 8617 §5.2 chain validation on raw.
func (e *ARCEvaluator) Verify(ctx context.Context, raw []byte) ARCVerdict {
	fail := func(domain, reason string) ARCVerdict {
		return ARCVerdict{Result: ARCFail, Domain: domain, Reason: reason}
	}
	if len(raw) == 0 {
		return ARCVerdict{Result: ARCNone, Reason: "no message content"}
	}
	msg := parseRawMessage(raw)

	sets, err := collectARCSets(msg.headers)
	if err != nil {
		return fail("", err.Error())
	}
	if len(sets) == 0 {
		return ARCVerdict{Result: ARCNone, Reason: "no ARC set"}
	}
	highest := sets[len(sets)-1]
	domain := highest.sealDomain()

	// §5.2 steps 2-3: chain status and structure. collectARCSets guarantees
	// one complete set per instance, ordered 1..N; the cv= claims remain.
	for _, s := range sets {
		want := ARCPass
		if s.instance == 1 {
			want = ARCNone
		}
		if s.cv != want {
			return fail(domain, fmt.Sprintf("i=%d cv=%s, want %s", s.instance, s.cv, want))
		}
	}

	res := e.resolver()

	// §5.2 step 4: the most recent AMS is verified as a DKIM signature over
	// the message.
	if err := verifyARCMS(ctx, res, msg, highest); err != nil {
		return fail(domain, fmt.Sprintf("i=%d AMS: %v", highest.instance, err))
	}

	// §5.2 step 6: every seal, from the highest instance down to 1.
	for i := len(sets) - 1; i >= 0; i-- {
		if err := verifyARCSeal(ctx, res, sets[:i+1]); err != nil {
			return fail(domain, fmt.Sprintf("i=%d AS: %v", sets[i].instance, err))
		}
	}
	return ARCVerdict{Result: ARCPass, Domain: domain}
}

func (e *ARCEvaluator) resolver() Resolver {
	if e.Resolver == nil {
		return NetResolver{}
	}
	return e.Resolver
}

// sealDomain returns the d= of a set's seal ("" when unparseable).
func (s *arcSet) sealDomain() string {
	if s.sealTags == nil {
		return ""
	}
	return s.sealTags.get("d")
}

// collectARCSets groups the ARC header fields into complete sets ordered by
// instance (contiguous 1..N). Incomplete, duplicated or gapped instances
// are chain failures (RFC 8617 §5.2 step 3).
func collectARCSets(headers []rawHeader) ([]*arcSet, error) {
	byInstance := map[int]*arcSet{}
	count := 0
	get := func(instance int) (*arcSet, error) {
		if instance < 1 || instance > arcMaxSets {
			return nil, fmt.Errorf("ARC instance %d outside 1..%d", instance, arcMaxSets)
		}
		set := byInstance[instance]
		if set == nil {
			set = &arcSet{instance: instance}
			byInstance[instance] = set
			count++
		}
		return set, nil
	}

	for _, h := range headers {
		switch h.name {
		case "arc-authentication-results":
			instance, err := aarInstance(h.raw)
			if err != nil {
				return nil, err
			}
			set, err := get(instance)
			if err != nil {
				return nil, err
			}
			if set.aar != "" {
				return nil, fmt.Errorf("duplicate ARC-Authentication-Results for i=%d", instance)
			}
			set.aar = h.raw
		case "arc-message-signature":
			instance, err := arcSigInstanceOnly(h.raw)
			if err != nil {
				return nil, fmt.Errorf("ARC-Message-Signature: %v", err)
			}
			set, err := get(instance)
			if err != nil {
				return nil, err
			}
			if set.ams != "" {
				return nil, fmt.Errorf("duplicate ARC-Message-Signature for i=%d", instance)
			}
			set.ams = h.raw
		case "arc-seal":
			tags, instance, err := arcSigParsed(h.raw)
			if err != nil {
				return nil, fmt.Errorf("ARC-Seal: %v", err)
			}
			set, err := get(instance)
			if err != nil {
				return nil, err
			}
			if set.seal != "" {
				return nil, fmt.Errorf("duplicate ARC-Seal for i=%d", instance)
			}
			set.seal = h.raw
			set.sealTags = tags
			set.cv = strings.ToLower(tags.get("cv"))
		}
	}

	if count == 0 {
		return nil, nil
	}
	sets := make([]*arcSet, 0, count)
	for i := 1; i <= count; i++ {
		set := byInstance[i]
		if set == nil {
			return nil, fmt.Errorf("ARC instance sequence has a gap at i=%d", i)
		}
		if set.aar == "" || set.ams == "" || set.seal == "" {
			return nil, fmt.Errorf("incomplete ARC set i=%d (need AAR, AMS and AS)", i)
		}
		sets = append(sets, set)
	}
	return sets, nil
}

// aarInstance extracts the instance from an ARC-Authentication-Results
// value: the instance tag leads the payload — "i" [CFWS] "=" [CFWS]
// 1*2DIGIT [CFWS] ";" (RFC 8617 §4.1.1).
func aarInstance(field string) (int, error) {
	_, value, _ := strings.Cut(field, ":")
	value = strings.TrimSpace(value)
	if value == "" || value[0] != 'i' && value[0] != 'I' {
		return 0, fmt.Errorf("ARC-Authentication-Results does not start with the instance tag")
	}
	value = strings.TrimLeft(value[1:], " \t")
	if value == "" || value[0] != '=' {
		return 0, fmt.Errorf("ARC-Authentication-Results instance tag malformed")
	}
	value = strings.TrimLeft(value[1:], " \t")
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 || end > 2 {
		return 0, fmt.Errorf("ARC-Authentication-Results instance is not 1-2 digits")
	}
	n, err := strconv.Atoi(value[:end])
	if err != nil || n < 1 {
		return 0, fmt.Errorf("ARC-Authentication-Results instance %q invalid", value[:end])
	}
	return n, nil
}

// arcSigParsed parses an ARC-Message-Signature / ARC-Seal header into its
// tag list and i= instance (RFC 8617 §4.2.1: 1*2DIGIT).
func arcSigParsed(field string) (sigTags, int, error) {
	_, value, _ := strings.Cut(field, ":")
	tags, err := parseSigTags(value)
	if err != nil {
		return nil, 0, err
	}
	i := stripSigFWS(tags.get("i"))
	if len(i) == 0 || len(i) > 2 {
		return nil, 0, fmt.Errorf("missing or malformed i= instance tag")
	}
	n, err := strconv.Atoi(i)
	if err != nil || n < 1 {
		return nil, 0, fmt.Errorf("malformed i= instance tag %q", i)
	}
	return tags, n, nil
}

// arcSigInstanceOnly parses only the instance of an
// ARC-Message-Signature header.
func arcSigInstanceOnly(field string) (int, error) {
	_, instance, err := arcSigParsed(field)
	return instance, err
}

// verifyARCSeal validates the seal of the LAST set in the given prefix
// (sets 1..k): the signature covers the relaxed-canonicalized ARC set
// headers of instances 1..k in order, with the seal's own b= emptied and no
// trailing CRLF (RFC 8617 §5.1.1 with §4.1.3; RFC 6376 §3.7 convention).
func verifyARCSeal(ctx context.Context, res Resolver, sets []*arcSet) error {
	seal := sets[len(sets)-1]
	tags := seal.sealTags

	// Required tags (RFC 8617 §4.1.3): i, a, b, cv, d, s (i= was required by
	// the instance parse). The DKIM h= tag is prohibited and MUST fail the
	// chain.
	for _, tag := range []string{"a", "b", "cv", "d", "s"} {
		if !tags.has(tag) {
			return fmt.Errorf("missing required tag %s=", tag)
		}
	}
	if tags.has("h") {
		return fmt.Errorf("h= tag is not allowed in ARC-Seal")
	}
	algo := strings.ToLower(stripSigFWS(tags.get("a")))
	if !sigAlgoSupported(algo) {
		return fmt.Errorf("unsupported seal algorithm %q", algo)
	}
	domain := tags.get("d")
	selector := stripSigFWS(tags.get("s"))

	var input strings.Builder
	for i, s := range sets {
		input.WriteString(canonHeaderRelaxed(s.aar))
		input.WriteString(canonHeaderRelaxed(s.ams))
		if i == len(sets)-1 {
			input.WriteString(strings.TrimRight(canonHeaderRelaxed(removeSigValue(s.seal)), "\r\n"))
		} else {
			input.WriteString(canonHeaderRelaxed(s.seal))
		}
	}
	digest := sha256.Sum256([]byte(input.String()))

	sig, err := decodeSigB(tags.get("b"))
	if err != nil {
		return fmt.Errorf("malformed b= tag: %v", err)
	}
	key, err := fetchDKIMKey(ctx, res, selector, domain)
	if err != nil {
		return err
	}
	if !keyHashAllowed(key, algo) {
		return fmt.Errorf("inappropriate hash algorithm for the key record")
	}
	if !keyServiceAllowed(key.raw) {
		return fmt.Errorf("inappropriate key service")
	}
	return verifySig(algo, key, digest[:], sig)
}

// verifyARCMS verifies the most recent ARC-Message-Signature as a DKIM
// signature over the message (RFC 8617 §4.1.2 imports RFC 6376 verification
// wholesale; differences: the field name, no v= tag, and the i= tag carries
// the instance).
func verifyARCMS(ctx context.Context, res Resolver, msg *rawMessage, set *arcSet) error {
	_, value, _ := strings.Cut(set.ams, ":")
	tags, err := parseSigTags(value)
	if err != nil {
		return err
	}
	for _, tag := range []string{"a", "b", "bh", "d", "h", "s"} {
		if !tags.has(tag) {
			return fmt.Errorf("missing required tag %s=", tag)
		}
	}
	algo := strings.ToLower(stripSigFWS(tags.get("a")))
	if !sigAlgoSupported(algo) {
		return fmt.Errorf("unsupported signature algorithm %q", algo)
	}
	if tags.has("l") {
		// Mirror the package's DKIM engine (go-msgauth): the body-length tag
		// leaves part of the body unsigned (RFC 6376 §8.2) and fails.
		return fmt.Errorf("insecure body length tag")
	}
	if x := stripSigFWS(tags.get("x")); x != "" {
		exp, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return fmt.Errorf("malformed x= tag")
		}
		if time.Now().Unix() > exp {
			return fmt.Errorf("signature has expired")
		}
	}
	headerCanon, bodyCanon := canonNames(tags.get("c"))

	// The signed header list: From MUST be covered, and
	// Authentication-Results / ARC headers MUST NOT be (RFC 8617 §4.1.2).
	var names []string
	hasFrom := false
	for _, n := range strings.Split(tags.get("h"), ":") {
		n = strings.ToLower(stripSigFWS(n))
		if n == "" {
			continue
		}
		if n == "authentication-results" || strings.HasPrefix(n, "arc-") {
			return fmt.Errorf("h= must not cover %s", n)
		}
		if n == "from" {
			hasFrom = true
		}
		names = append(names, n)
	}
	if !hasFrom {
		return fmt.Errorf("From field not signed")
	}

	// RFC 6376 §3.7 hash step 1: the body hash.
	wantBH, err := decodeSigB(tags.get("bh"))
	if err != nil {
		return fmt.Errorf("malformed bh= tag: %v", err)
	}
	gotBH := bodyCanonHash(bodyCanon, msg.body)
	if !compareBodyHashes(wantBH, gotBH) {
		return fmt.Errorf("body hash did not verify")
	}

	// RFC 6376 §3.7 hash step 2: the selected headers (each + CRLF), then
	// the AMS itself with b= emptied and no trailing CRLF.
	p := newPicker(msg.headers)
	var headerInput strings.Builder
	for _, n := range names {
		if raw := p.pick(n); raw != "" {
			headerInput.WriteString(canonicalHeader(headerCanon, raw))
		}
	}
	headerInput.WriteString(strings.TrimRight(canonicalHeader(headerCanon, removeSigValue(set.ams)), "\r\n"))
	digest := sha256.Sum256([]byte(headerInput.String()))

	sig, err := decodeSigB(tags.get("b"))
	if err != nil {
		return fmt.Errorf("malformed b= tag: %v", err)
	}
	key, err := fetchDKIMKey(ctx, res, stripSigFWS(tags.get("s")), tags.get("d"))
	if err != nil {
		return err
	}
	if !keyHashAllowed(key, algo) {
		return fmt.Errorf("inappropriate hash algorithm for the key record")
	}
	if !keyServiceAllowed(key.raw) {
		return fmt.Errorf("inappropriate key service")
	}
	return verifySig(algo, key, digest[:], sig)
}

// canonNames parses the c= tag ("relaxed/relaxed" etc.); defaults follow
// RFC 6376 §3.5 (simple/simple; a bare value names the header algorithm and
// simple applies to the body).
func canonNames(c string) (header, body string) {
	header, body = "simple", "simple"
	parts := strings.SplitN(stripSigFWS(c), "/", 2)
	if parts[0] != "" {
		header = strings.ToLower(parts[0])
	}
	if len(parts) == 2 && parts[1] != "" {
		body = strings.ToLower(parts[1])
	}
	return header, body
}

// canonicalHeader dispatches to the requested RFC 6376 header algorithm.
func canonicalHeader(algo, raw string) string {
	if algo == "relaxed" {
		return canonHeaderRelaxed(raw)
	}
	return canonHeaderSimple(raw)
}

// bodyCanonHash computes the RFC 6376 §3.7 step-1 body hash with the named
// body canonicalization.
func bodyCanonHash(algo string, body []byte) []byte {
	var canonical []byte
	if algo == "relaxed" {
		canonical = canonBodyRelaxed(body)
	} else {
		canonical = canonBodySimple(body)
	}
	h := sha256.Sum256(canonical)
	return h[:]
}

// compareBodyHashes is the constant-time bh= comparison (RFC 6376 §6.1.3
// step 3).
func compareBodyHashes(want, got []byte) bool {
	return len(want) == len(got) && subtle.ConstantTimeCompare(want, got) == 1
}
