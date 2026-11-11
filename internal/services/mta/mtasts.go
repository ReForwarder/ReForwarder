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

// MTA-STS outbound enforcement (SF-29, OB-6 W4; ADR-007 §5, RFC 8461).
//
// ADR-007 decision 5: "outbound MTA-STS enforcement for direct delivery
// (ADR-001) lands with it (mode: enforce/none per domain, cache with the MX
// resolver)". This file is the recipient-domain side of that decision: before
// a direct-to-MX session, the runner consults the recipient domain's
// published MTA-STS policy (DNS TXT at _smtp._tls.<domain> → HTTPS policy
// document at https://mta-sts.<domain>/.well-known/mta-sts.txt) and refuses —
// per host — delivery the policy forbids.
//
// Design constraints honored here:
//   - deliver.go's delivery loop is owned by another wave this round, so the
//     enforcement hook lives behind the small STSEnforcement interface and is
//     consumed by the session runner (session.go) through SessionOptions.STS,
//     with a process-wide default (SetDefaultSTS) wired in cmd/posta — the
//     executor's opts literal stays untouched.
//   - The relay path never calls runSession, so relay delivery is unaffected
//     by construction.
//   - Fetch failures fall open per RFC 8461 §6.1/§10: a domain without a
//     readable policy is treated as having no policy (opportunistic STARTTLS,
//     the pre-existing behavior). Enforcement only ever tightens behavior for
//     domains that publish mode: enforce.
//
// Integration note for the coordinator (single line, deliver.go is owned by
// the pool wave this round): none required — the process default covers the
// executor because runSession falls back to DefaultSTS() when
// SessionOptions.STS is nil (the zero value deliver.go builds today).

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

// netLookupTXT is the system resolver seam (tests inject fakes via
// PolicyClient.SetLookups; this indirection keeps the default wiring single).
var netLookupTXT = func(ctx context.Context, name string) ([]string, error) {
	return net.LookupTXT(name)
}

// MTA-STS policy modes (RFC 8461 §4.2). Mirrors models.MTASTSMode* without an
// import (mta is imported by worker/handler.go; keeping the constant set local
// avoids widening the dependency surface).
const (
	STSModeNone    = "none"
	STSModeTesting = "testing"
	STSModeEnforce = "enforce"
)

// Bounds for policy discovery.
const (
	// stsPolicyHost is the RFC 8461 §3.3 fixed prefix of the policy Web Host:
	// the policy is served at https://mta-sts.<domain>/.well-known/mta-sts.txt.
	stsPolicyHostPrefix = "mta-sts."

	// stsPolicyPath is the fixed policy HTTP path.
	stsPolicyPath = "/.well-known/mta-sts.txt"

	// STSTXTLabel is the DNS label prefix of the policy record:
	// _smtp._tls.<domain>.
	STSTXTLabel = "_smtp._tls"

	// stsMaxPolicyBytes caps the fetched policy document. Real policies are a
	// few hundred bytes; the cap bounds a hostile HTTPS endpoint's response.
	stsMaxPolicyBytes = 64 << 10

	// STSPolicyFetchTimeout bounds one policy fetch; delivery budget (R-22)
	// tolerates a few hundred ms of DNS + one HTTPS round trip.
	STSPolicyFetchTimeout = 5 * time.Second

	// stsNegativeTTL is how long a "no policy" verdict is cached. The DNS TXT
	// record carries no parseable TTL through the stdlib resolver, so a fixed
	// short window keeps a newly published policy discoverable quickly while
	// sparing the resolver a TXT lookup per session.
	stsNegativeTTL = 60 * time.Second

	// stsMaxPositiveTTL caps the cache lifetime of a fetched policy: RFC 8461
	// allows reuse until the policy's max_age elapses, but a one-day ceiling
	// bounds how stale a withdrawn policy can act.
	stsMaxPositiveTTL = 24 * time.Hour
)

// STSPolicy is a parsed RFC 8461 policy document.
type STSPolicy struct {
	Mode          string   // none | testing | enforce
	MX            []string // mx patterns, RFC 8461 §4.1 matching
	MaxAgeSeconds int
	ID            string   // the TXT record's id= (diagnostics only)
	Policy        []string // raw policy lines (TLS-RPT policy-string projection)
}

// Enforced reports whether the policy must actually gate delivery: only
// mode: enforce refuses (RFC 8461 §10 — testing records but delivers).
func (p *STSPolicy) Enforced() bool {
	return p != nil && p.Mode == STSModeEnforce
}

// HostAllowed applies the policy's mx pattern match (RFC 8461 §4.1): exact
// match, or a leading "*." wildcard covering one or more subdomain labels.
func (p *STSPolicy) HostAllowed(host string) bool {
	if p == nil {
		return false
	}
	return MXAllowed(p.MX, host)
}

// MXAllowed reports whether host matches any pattern (RFC 8461 §4.1):
// case-insensitive exact match; a leading "*." wildcard matching at least one
// subdomain label; and the leading-dot suffix form ".<domain>" matching the
// apex and any subdomain (the publishing convention the RFC carries forward
// from long-standing MX practice).
func MXAllowed(patterns []string, host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		return false
	}
	for _, raw := range patterns {
		p := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		switch {
		case p == "":
			continue
		case p == h:
			return true
		case strings.HasPrefix(p, "*."):
			suffix := p[2:]
			if suffix != "" && strings.HasSuffix(h, "."+suffix) {
				return true
			}
		case strings.HasPrefix(p, "."):
			suffix := p[1:]
			if suffix != "" && (strings.HasSuffix(h, "."+suffix) || h == suffix) {
				return true
			}
		}
	}
	return false
}

// STSRecordID parses a _smtp._tls TXT record and returns its id= value
// (RFC 8461 §3.2: "v=STS1 id=<id>"). Unlike the TLS-RPT record, the MTA-STS
// record separates its tags with whitespace (the RFC's own examples), so both
// whitespace and semicolons are accepted as separators. Any string that is
// not a valid STS1 record reports no id.
func STSRecordID(txt string) (string, bool) {
	id := ""
	valid := false
	for _, tag := range splitTags(txt) {
		k, v, ok := cutTag(tag)
		if !ok {
			continue
		}
		switch k {
		case "v":
			if v != "STS1" {
				return "", false // a different (future) version: not ours
			}
			valid = true
		case "id":
			id = v
		}
	}
	if !valid || id == "" {
		return "", false
	}
	return id, true
}

// splitTags splits a DNS TXT record into its tag=value parts on semicolons
// and whitespace alike.
func splitTags(txt string) []string {
	return strings.FieldsFunc(txt, func(r rune) bool { return r == ';' || r == ' ' || r == '	' })
}

// cutTag splits one tag=value pair, lowercasing the key (tag names are
// case-insensitive) and trimming space.
func cutTag(tag string) (key, value string, ok bool) {
	i := strings.Index(tag, "=")
	if i <= 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(tag[:i]))
	value = strings.TrimSpace(tag[i+1:])
	return key, value, value != ""
}

// cutPolicyLine splits one policy document line ("key: value", RFC 8461
// §4.2), lowercasing the key. Unlike TXT tags, policy fields separate with a
// colon.
func cutPolicyLine(line string) (key, value string, ok bool) {
	i := strings.Index(line, ":")
	if i <= 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(line[:i]))
	value = strings.TrimSpace(line[i+1:])
	return key, value, value != ""
}

// ParseSTSPolicy parses the HTTPS policy document (RFC 8461 §4.2): lines of
// key: value separated by CRLF (LF tolerated), exactly one version (STS1),
// one mode (none|testing|enforce), one max_age (positive int), and at least
// one mx for testing/enforce policies. mode: none policies may omit mx.
func ParseSTSPolicy(body []byte, recordID string) (*STSPolicy, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("mtasts: empty policy document")
	}
	if len(body) > stsMaxPolicyBytes {
		return nil, fmt.Errorf("mtasts: policy document exceeds %d bytes", stsMaxPolicyBytes)
	}

	p := &STSPolicy{ID: recordID}
	version := ""
	seenMode, seenMaxAge := false, false
	textprotoReader := textproto.NewReader(bufio.NewReader(strings.NewReader(string(body))))
	for {
		line, err := textprotoReader.ReadLine()
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := cutPolicyLine(line)
		if !ok {
			return nil, fmt.Errorf("mtasts: malformed policy line %q", line)
		}
		p.Policy = append(p.Policy, key+": "+value)
		switch key {
		case "version":
			if version != "" {
				return nil, fmt.Errorf("mtasts: duplicate version field")
			}
			version = value
		case "mode":
			if seenMode {
				return nil, fmt.Errorf("mtasts: duplicate mode field")
			}
			seenMode = true
			switch value {
			case STSModeNone, STSModeTesting, STSModeEnforce:
				p.Mode = value
			default:
				return nil, fmt.Errorf("mtasts: unknown mode %q", value)
			}
		case "mx":
			if value == "" {
				return nil, fmt.Errorf("mtasts: empty mx value")
			}
			p.MX = append(p.MX, value)
		case "max_age":
			if seenMaxAge {
				return nil, fmt.Errorf("mtasts: duplicate max_age field")
			}
			seenMaxAge = true
			age, err := strconv.Atoi(value)
			if err != nil || age <= 0 || age > 31557600 {
				return nil, fmt.Errorf("mtasts: invalid max_age %q", value)
			}
			p.MaxAgeSeconds = age
		default:
			return nil, fmt.Errorf("mtasts: unknown policy field %q", key)
		}
	}
	if version != "STS1" {
		return nil, fmt.Errorf("mtasts: policy version must be STS1")
	}
	if !seenMode {
		return nil, fmt.Errorf("mtasts: policy has no mode field")
	}
	if !seenMaxAge {
		return nil, fmt.Errorf("mtasts: policy has no max_age field")
	}
	if p.Mode != STSModeNone && len(p.MX) == 0 {
		return nil, fmt.Errorf("mtasts: %s policy requires at least one mx field", p.Mode)
	}
	return p, nil
}

// RenderSTSPolicy renders the hosted policy document for one of our domains
// (SF-29 policy-hosting half): the exact field set the parser above accepts.
// mode is one of the models.MTASTSMode* values; mxHosts are the domain's live
// MX hostnames. An empty mx list or an unknown mode renders nothing (the
// handler answers 404 rather than serve an invalid policy).
func RenderSTSPolicy(mode string, mxHosts []string, maxAgeSeconds int) string {
	switch mode {
	case STSModeNone, STSModeTesting, STSModeEnforce:
	default:
		return ""
	}
	if len(mxHosts) == 0 {
		return ""
	}
	if maxAgeSeconds <= 0 {
		maxAgeSeconds = 86400
	}
	var b strings.Builder
	b.WriteString("version: STS1\r\n")
	b.WriteString("mode: " + mode + "\r\n")
	for _, h := range mxHosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		b.WriteString("mx: " + h + "\r\n")
	}
	b.WriteString("max_age: " + strconv.Itoa(maxAgeSeconds) + "\r\n")
	return b.String()
}

// ---------- policy discovery (TXT → HTTPS) with a TTL cache ----------

// STSPolicySource resolves a recipient domain to its active MTA-STS policy,
// caching per domain. Production: *PolicyClient; tests inject fakes.
type STSPolicySource interface {
	Policy(ctx context.Context, domain string) (*STSPolicy, bool)
}

// PolicyClient discovers and caches recipient-domain MTA-STS policies
// (RFC 8461 §3/§6). Cache discipline mirrors the MX resolver's TTL cache
// (ADR-007: "cache with the MX resolver"): positive entries live until the
// policy's max_age (bounded by stsMaxPositiveTTL), "no policy" verdicts are
// negatively cached for stsNegativeTTL. Lookup failures fall open — a domain
// whose policy cannot be fetched right now is treated as having none, never
// as an enforcement outage that blocks delivery.
type PolicyClient struct {
	lookupTXT func(ctx context.Context, domain string) ([]string, error)
	fetch     func(ctx context.Context, domain string) ([]byte, error)
	now       func() time.Time
	ttl       time.Duration // overrides the policy's max_age when > 0 (tests)

	mu    sync.Mutex
	cache map[string]stsCacheEntry
}

type stsCacheEntry struct {
	policy  *STSPolicy // nil = negatively cached (no usable policy)
	id      string
	expires time.Time
}

// NewPolicyClient builds a client over the system DNS resolver and an HTTPS
// fetcher with certificate verification always on.
func NewPolicyClient() *PolicyClient {
	c := &PolicyClient{
		now:   time.Now,
		cache: map[string]stsCacheEntry{},
	}
	c.lookupTXT = defaultLookupTXT
	c.fetch = func(ctx context.Context, domain string) ([]byte, error) {
		return fetchSTSPolicy(ctx, http.DefaultClient, domain)
	}
	return c
}

// SetTTL overrides the positive cache lifetime (tests). Values <= 0 keep the
// policy's max_age semantics.
func (c *PolicyClient) SetTTL(d time.Duration) {
	if d > 0 {
		c.ttl = d
	}
}

// SetLookups replaces the TXT lookup and HTTPS fetch (tests inject fakes;
// production never calls this).
func (c *PolicyClient) SetLookups(
	txt func(ctx context.Context, domain string) ([]string, error),
	fetch func(ctx context.Context, domain string) ([]byte, error),
) {
	if txt != nil {
		c.lookupTXT = txt
	}
	if fetch != nil {
		c.fetch = fetch
	}
}

// Invalidate drops the cached verdict for one domain.
func (c *PolicyClient) Invalidate(domain string) {
	c.mu.Lock()
	delete(c.cache, normalizeDomain(domain))
	c.mu.Unlock()
}

// Policy returns the domain's active policy, or (nil, false) when none is
// discoverable. Never returns an error: to the session runner a discovery
// failure is indistinguishable from "no policy" (fall open, RFC 8461 §6.1).
func (c *PolicyClient) Policy(ctx context.Context, domain string) (*STSPolicy, bool) {
	d := normalizeDomain(domain)
	if d == "" {
		return nil, false
	}

	c.mu.Lock()
	if e, ok := c.cache[d]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.policy, e.policy != nil
	}
	c.mu.Unlock()

	policy, id := c.discover(ctx, d)

	ttl := stsNegativeTTL
	if policy != nil {
		ttl = time.Duration(policy.MaxAgeSeconds) * time.Second
		if c.ttl > 0 {
			ttl = c.ttl
		}
		if ttl > stsMaxPositiveTTL {
			ttl = stsMaxPositiveTTL
		}
	}
	c.mu.Lock()
	c.cache[d] = stsCacheEntry{policy: policy, id: id, expires: c.now().Add(ttl)}
	c.mu.Unlock()
	if policy == nil {
		return nil, false
	}
	return policy, true
}

// discover performs one TXT lookup + (when a live STS1 id is published) one
// HTTPS policy fetch. A lookup or fetch failure yields (nil, "").
func (c *PolicyClient) discover(ctx context.Context, domain string) (*STSPolicy, string) {
	if c.lookupTXT == nil || c.fetch == nil {
		return nil, ""
	}
	txts, err := c.lookupTXT(ctx, STSTXTLabel+"."+domain)
	if err != nil || len(txts) == 0 {
		return nil, ""
	}
	// RFC 8461 §3.2.1: exactly one TXT record is published; when several
	// exist, use the first valid STS1 record and ignore the rest.
	id := ""
	for _, txt := range txts {
		if v, ok := STSRecordID(txt); ok {
			id = v
			break
		}
	}
	if id == "" {
		return nil, ""
	}
	body, err := c.fetch(ctx, domain)
	if err != nil {
		return nil, ""
	}
	policy, err := ParseSTSPolicy(body, id)
	if err != nil {
		return nil, ""
	}
	return policy, id
}

func defaultLookupTXT(ctx context.Context, name string) ([]string, error) {
	return netLookupTXT(ctx, name)
}

// ---------- enforcement hook (the small interface the runner consumes) ----------

// STSRule is the per-session enforcement verdict for one recipient domain.
// nil means "no policy applies" — the runner behaves exactly as before.
type STSRule struct {
	Mode string
	MX   []string
}

// Enforced reports whether this rule gates delivery (mode: enforce only).
func (r *STSRule) Enforced() bool { return r != nil && r.Mode == STSModeEnforce }

// HostAllowed applies the policy's mx pattern match to one candidate MX host.
// A nil rule allows every host (no policy applies).
func (r *STSRule) HostAllowed(host string) bool {
	if r == nil {
		return true
	}
	return MXAllowed(r.MX, host)
}

// STSEnforcement is the enforcement hook behind which all MTA-STS gating
// lives (the small interface the ask pins): the session runner consumes it
// through SessionOptions.STS (or the process default), nothing else in the
// delivery path changes.
type STSEnforcement interface {
	// SessionRule resolves the recipient domain's active policy. A nil rule
	// means no policy applies; an error never surfaces (discovery failures
	// fall open — see PolicyClient.Policy).
	SessionRule(ctx context.Context, domain string) *STSRule
}

// Enforcer adapts a PolicyClient onto STSEnforcement.
type Enforcer struct {
	policies STSPolicySource
}

// NewEnforcer builds the enforcement hook over a policy source.
func NewEnforcer(src STSPolicySource) *Enforcer { return &Enforcer{policies: src} }

// SessionRule implements STSEnforcement.
func (e *Enforcer) SessionRule(ctx context.Context, domain string) *STSRule {
	if e == nil || e.policies == nil {
		return nil
	}
	p, ok := e.policies.Policy(ctx, domain)
	if !ok || p == nil {
		return nil
	}
	return &STSRule{Mode: p.Mode, MX: p.MX}
}

// ---------- process-wide default (wiring seam) ----------

// defaultSTS holds the process-wide enforcement hook. It exists so the
// executor wiring (cmd/posta newMTAExecutor) can arm MTA-STS enforcement for
// every direct-MX session without editing deliver.go (owned by another wave
// this round) — the same pattern as the process-wide DKIM signer
// (email.CurrentDkimSigner). nil = enforcement off (the zero-value default).
var defaultSTS atomicSTSValue

// atomicSTSValue is a sync-free holder for the process-wide hook.
type atomicSTSValue struct {
	mu    sync.RWMutex
	value STSEnforcement
}

func (a *atomicSTSValue) Store(s STSEnforcement) {
	a.mu.Lock()
	a.value = s
	a.mu.Unlock()
}

func (a *atomicSTSValue) Load() STSEnforcement {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.value
}

// SetDefaultSTS arms the process-wide MTA-STS enforcement hook. Pass nil to
// disable. Safe for concurrent use.
func SetDefaultSTS(s STSEnforcement) { defaultSTS.Store(s) }

// DefaultSTS returns the process-wide hook (nil when unset).
func DefaultSTS() STSEnforcement { return defaultSTS.Load() }

// fetchSTSPolicy performs the HTTPS policy fetch (RFC 8461 §4): GET
// https://mta-sts.<domain>/.well-known/mta-sts.txt with certificate
// verification (the default transport), a bounded response size, and a
// text/* content type. Any deviation — non-2xx, oversized body, wrong path —
// is a fetch failure (fall open).
func fetchSTSPolicy(ctx context.Context, client *http.Client, domain string) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, STSPolicyFetchTimeout)
	defer cancel()
	url := "https://" + stsPolicyHostPrefix + domain + stsPolicyPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mtasts: policy fetch returned %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(strings.ToLower(ct), "text/") {
		return nil, fmt.Errorf("mtasts: policy content type %q is not text/*", ct)
	}
	return readBoundedBytes(resp.Body, stsMaxPolicyBytes)
}

// readBoundedBytes reads r up to max bytes, refusing longer streams (a
// hostile policy host must not be able to balloon worker memory).
func readBoundedBytes(r io.Reader, max int) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(out) > max {
		return nil, fmt.Errorf("mtasts: policy document exceeds %d bytes", max)
	}
	return out, nil
}
