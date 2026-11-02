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

package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jkaninda/logger"
	"github.com/redis/go-redis/v9"
)

// Redis layout, one sorted set per sliding window:
//
//	sec:sig:<signal>:<ip>                       per-IP window
//	sec:sig:auth_failures:login:<sha256hex>     per-login window (brute force
//	                                            against one account from many IPs)
//
// Every member carries its event time (unix nanoseconds) as score; expired
// members are trimmed on each write, so a plain ZCARD is the window count and
// evaluation never needs a Lua script.

const (
	// redisOpTimeout bounds every Redis round trip so the detector never
	// blocks a hot path (listener accept loop, HTTP middleware, SMTP session)
	// for long.
	redisOpTimeout = 500 * time.Millisecond

	signalKeyPrefix  = "sec:sig:"
	loginScopePrefix = "login:"

	// memberCapFactor bounds each window at 10x its threshold so a flooding
	// source cannot grow a key without limit between evaluations.
	memberCapFactor = 10

	// notifyCacheCap bounds the last-notified tracker (per IP+signal).
	notifyCacheCap = 10000

	// windowExpiryBuffer keeps a window key alive a little longer than its
	// window so in-flight evaluations never race an expiry.
	windowExpiryBuffer = time.Minute
)

// Detector records abuse signals into Redis sliding windows and, after each
// recording, evaluates the source IP against the loaded Rules. It never
// touches the database on the hot path: counting is Redis-only, and bans and
// events are written only once a threshold is crossed.
type Detector struct {
	store    windowStore
	rules    *RulesLoader
	bans     *BanService
	ev       *EventRecorder
	now      func() time.Time
	failOpen bool
	notified *notifyCache
	limiter  *logThrottle
}

// NewDetector wires the detector. rdb is the Redis client backing the
// windows (nil disables detection entirely); rules is re-loaded on every
// evaluation; bans is the ban service consulted (allowlist / already-blocked
// short circuits) and called on a ban decision; ev persists security_events
// rows; now is the injectable clock; failOpen decides whether Redis failures
// are swallowed (log only) or returned to the caller.
func NewDetector(rdb *redis.Client, rules *RulesLoader, bans *BanService, ev *EventRecorder, now func() time.Time, failOpen bool) *Detector {
	if now == nil {
		now = time.Now
	}
	if rules == nil {
		rules = NewRulesLoader(nil)
	}
	d := &Detector{
		rules:    rules,
		bans:     bans,
		ev:       ev,
		now:      now,
		failOpen: failOpen,
		notified: newNotifyCache(notifyCacheCap),
		limiter:  newLogThrottle(),
	}
	if rdb != nil {
		d.store = redisWindowStore{rdb: rdb}
	}
	return d
}

// windowStore abstracts the Redis window operations so tests can substitute
// an in-memory implementation.
type windowStore interface {
	// ZAdd records one event in the window keyed by key with the given score.
	// When nx is true the member is only added if absent (used by
	// distinct_ports, where the member is the port number). The oldest
	// members beyond keep are trimmed in the same transaction, the key
	// expires after window. Returns the resulting member count.
	ZAdd(ctx context.Context, key, member string, score float64, nx bool, window time.Duration, keep int64) (int64, error)
	// Count returns the current member count of each key (0 for missing keys).
	Count(ctx context.Context, keys ...string) ([]int64, error)
	// Members returns every member of the key ordered oldest first.
	Members(ctx context.Context, key string) ([]string, error)
}

// redisWindowStore is the production windowStore over a go-redis client.
type redisWindowStore struct {
	rdb *redis.Client
}

// ZAdd runs ZREMRANGEBYSCORE (drop expired) + ZADD (+NX) + ZREMRANGEBYRANK
// (enforce the member cap) + EXPIRE in a single transaction, then returns
// the resulting cardinality. Trimming unconditionally to keep members is
// equivalent to the "trim when over cap" rule but needs no extra round trip.
func (s redisWindowStore) ZAdd(ctx context.Context, key, member string, score float64, nx bool, window time.Duration, keep int64) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()

	pipe := s.rdb.TxPipeline()
	cutoff := score - float64(window.Nanoseconds())
	// Exclusive upper bound: entries exactly at the window edge survive one
	// more pass.
	pipe.ZRemRangeByScore(ctx, key, "-inf", "("+strconv.FormatFloat(cutoff, 'f', -1, 64))
	if nx {
		pipe.ZAddArgs(ctx, key, redis.ZAddArgs{NX: true, Members: []redis.Z{{Score: score, Member: member}}})
	} else {
		pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: member})
	}
	// Drop ranks 0..(size-keep-1), i.e. keep the newest `keep` members. When
	// the set is smaller than keep the range is empty and this is a no-op.
	pipe.ZRemRangeByRank(ctx, key, 0, -keep-1)
	pipe.Expire(ctx, key, window+windowExpiryBuffer)

	card := pipe.ZCard(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return card.Val(), nil
}

// Count returns the ZCARD of every key in one pipeline.
func (s redisWindowStore) Count(ctx context.Context, keys ...string) ([]int64, error) {
	if len(keys) == 0 {
		return []int64{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()

	pipe := s.rdb.TxPipeline()
	cmds := make([]*redis.IntCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.ZCard(ctx, key)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	out := make([]int64, len(keys))
	for i, cmd := range cmds {
		out[i] = cmd.Val()
	}
	return out, nil
}

// Members returns the full member list of the key, oldest first.
func (s redisWindowStore) Members(ctx context.Context, key string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	return s.rdb.ZRange(ctx, key, 0, -1).Result()
}

// RecordConnect records a new connection (new_connections) and a distinct
// port hit (distinct_ports, deduplicated per port within the window), then
// evaluates the source IP.
func (d *Detector) RecordConnect(ctx context.Context, ip netip.Addr, port int, proto string) error {
	if d == nil || d.store == nil {
		return nil
	}
	ip = canonicalAddr(ip)
	if !ip.IsValid() {
		return nil
	}
	r := d.rules.Load()
	now := d.now()

	var errs []error
	if _, err := d.record(ctx, SigNewConnections, ip, nanoMember(now), now, false, r); err != nil {
		errs = append(errs, err)
	}
	if port > 0 {
		// Member = port, ZADD NX: repeated connections to the same port do
		// not grow the set.
		if _, err := d.record(ctx, SigDistinctPorts, ip, strconv.Itoa(port), now, true, r); err != nil {
			errs = append(errs, err)
		}
	}
	return d.evaluateAndFinish(ctx, ip, "", SigNewConnections, proto, extraWithPort(port), r, errs)
}

// RecordAbort records a connection that was closed before the client sent
// any command (pre-auth disconnect scanning), then evaluates the IP.
func (d *Detector) RecordAbort(ctx context.Context, ip netip.Addr, port int, proto string) error {
	return d.recordAndEvaluate(ctx, ip, SigConnectAborts, port, proto, nil)
}

// RecordHandshakeFail records an incomplete protocol handshake. Protocol
// "tls" is TLS probing, anything else an incomplete SMTP/HTTP handshake.
func (d *Detector) RecordHandshakeFail(ctx context.Context, ip netip.Addr, port int, proto string) error {
	return d.recordAndEvaluate(ctx, ip, SigIncompleteHS, port, proto, nil)
}

// RecordMalformed records a malformed pre-auth protocol message. detail is a
// short human-readable sample kept in the event/ban evidence.
func (d *Detector) RecordMalformed(ctx context.Context, ip netip.Addr, port int, proto, detail string) error {
	extra := extraWithPort(port)
	if detail = strings.TrimSpace(detail); detail != "" {
		if extra == nil {
			extra = map[string]any{}
		}
		extra["detail"] = clipString(detail, 256)
	}
	return d.recordAndEvaluate(ctx, ip, SigMalformed, port, proto, extra)
}

// RecordSuspiciousPath records a suspicious URL path hit. Ordinary hits
// (404 noise) accumulate in the window only; high-risk probe paths — the
// same patterns IsSuspiciousPath matches, which the HTTP middleware checks
// before routing — trigger an immediate evaluation.
func (d *Detector) RecordSuspiciousPath(ctx context.Context, ip netip.Addr, path string) error {
	if d == nil || d.store == nil {
		return nil
	}
	ip = canonicalAddr(ip)
	if !ip.IsValid() {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	r := d.rules.Load()
	now := d.now()

	var errs []error
	if _, err := d.record(ctx, SigSuspiciousPath, ip, nanoMember(now), now, false, r); err != nil {
		errs = append(errs, err)
	}
	if !IsSuspiciousPath(path) {
		return d.finish(errs)
	}
	return d.evaluateAndFinish(ctx, ip, "", SigSuspiciousPath, "http", map[string]any{"path": clipString(path, 256)}, r, errs)
}

// RecordAuthFailure records a failed authentication attempt under two keys:
// the source IP window (one attacker, many accounts) and a per-login window
// keyed by sha256(login) (one account, many IPs). The evaluation takes the
// larger of the two counts for auth_failures.
func (d *Detector) RecordAuthFailure(ctx context.Context, ip netip.Addr, login string) error {
	if d == nil || d.store == nil {
		return nil
	}
	ip = canonicalAddr(ip)
	if !ip.IsValid() {
		return nil
	}
	login = strings.ToLower(strings.TrimSpace(login))
	r := d.rules.Load()
	now := d.now()

	var errs []error
	if _, err := d.record(ctx, SigAuthFailures, ip, nanoMember(now), now, false, r); err != nil {
		errs = append(errs, err)
	}
	extra := map[string]any{}
	if login != "" {
		if _, err := d.recordKey(ctx, SigAuthFailures, loginScopeKey(login), nanoMember(now), now, false, r); err != nil {
			errs = append(errs, err)
		}
		extra["login"] = clipString(login, 128)
	}
	return d.evaluateAndFinish(ctx, ip, login, SigAuthFailures, "", extra, r, errs)
}

// recordAndEvaluate is the shared body of the single-signal recorders.
func (d *Detector) recordAndEvaluate(ctx context.Context, ip netip.Addr, sig Signal, port int, proto string, extra map[string]any) error {
	if d == nil || d.store == nil {
		return nil
	}
	ip = canonicalAddr(ip)
	if !ip.IsValid() {
		return nil
	}
	r := d.rules.Load()
	now := d.now()

	var errs []error
	if _, err := d.record(ctx, sig, ip, nanoMember(now), now, false, r); err != nil {
		errs = append(errs, err)
	}
	if extra == nil {
		extra = extraWithPort(port)
	}
	return d.evaluateAndFinish(ctx, ip, "", sig, proto, extra, r, errs)
}

// record writes one event into the signal's per-IP window.
func (d *Detector) record(ctx context.Context, sig Signal, ip netip.Addr, member string, now time.Time, nx bool, r Rules) (int64, error) {
	return d.recordKey(ctx, sig, scopeKey(sig, ip), member, now, nx, r)
}

// recordKey writes one event into an explicit window key belonging to sig.
func (d *Detector) recordKey(ctx context.Context, sig Signal, key, member string, now time.Time, nx bool, r Rules) (int64, error) {
	rule := r.Signals[sig]
	window := time.Duration(rule.WindowSeconds) * time.Second
	if window <= 0 {
		window = time.Minute
	}
	keep := int64(rule.Threshold * memberCapFactor)
	if keep < memberCapFactor {
		keep = memberCapFactor
	}
	return d.store.ZAdd(ctx, key, member, float64(now.UnixNano()), nx, window, keep)
}

// evaluateAndFinish runs the post-recording evaluation and applies the
// fail-open policy to every error collected along the way.
func (d *Detector) evaluateAndFinish(ctx context.Context, ip netip.Addr, login string, trigger Signal, proto string, extra map[string]any, r Rules, errs []error) error {
	if err := d.evaluate(ctx, ip, login, trigger, proto, extra, r); err != nil {
		errs = append(errs, err)
	}
	return d.finish(errs)
}

// finish applies the fail-open policy: with failOpen set, Redis failures are
// logged (rate-limited) and swallowed; otherwise they are returned to the
// caller (HTTP paths may reject instead).
func (d *Detector) finish(errs []error) error {
	err := errors.Join(errs...)
	if err == nil {
		return nil
	}
	if d.failOpen {
		if d.limiter.allow("security.detector.redis", d.now()) {
			logger.Error("security: detector unavailable, failing open", "error", err)
		}
		return nil
	}
	return err
}

// evaluate scores the source IP against every signal window and, when the
// risk threshold or an instant-ban ratio is crossed, asks the BanService for
// a ban. Exactly one Count pipeline is spent per evaluation.
func (d *Detector) evaluate(ctx context.Context, ip netip.Addr, login string, trigger Signal, proto string, extra map[string]any, r Rules) error {
	if d.bans != nil {
		if d.bans.IsAllowed(ip) {
			return nil // allowlist wins: never score, never ban
		}
		if blocked, _ := d.bans.IsBlocked(ip); blocked {
			return nil // already banned: Ban would only re-extend; keep the hot path cheap
		}
	}

	sigs := AllSignals()
	keys := make([]string, 0, len(sigs)+1)
	for _, sig := range sigs {
		keys = append(keys, scopeKey(sig, ip))
	}
	loginIdx := -1
	if login != "" {
		loginIdx = len(keys)
		keys = append(keys, loginScopeKey(login))
	}

	raw, err := d.store.Count(ctx, keys...)
	if err != nil {
		return err
	}

	counts := make(map[Signal]int64, len(sigs))
	for i, sig := range sigs {
		counts[sig] = raw[i]
	}
	if loginIdx >= 0 && raw[loginIdx] > counts[SigAuthFailures] {
		counts[SigAuthFailures] = raw[loginIdx]
	}

	score := 0
	var exceeded []Signal
	instant := Signal("")
	for _, sig := range sigs {
		rule := r.Signals[sig]
		if rule.Threshold < 1 {
			continue
		}
		c := counts[sig]
		if c >= int64(rule.Threshold) {
			exceeded = append(exceeded, sig)
			score += rule.Weight
		}
		if instant == "" && rule.Threshold*r.InstantBanRatio > 0 && c >= int64(rule.Threshold*r.InstantBanRatio) {
			instant = sig
		}
	}

	notifyErr := d.notifyExceeded(ctx, ip, trigger, proto, extra, r, counts, exceeded)

	if score < r.RiskThreshold && instant == "" {
		return notifyErr
	}

	banSignal := trigger
	if !containsSignal(exceeded, trigger) {
		// The trigger itself did not cross: the decision came from another
		// signal, prefer the instant one, else the heaviest exceeded.
		if instant != "" {
			banSignal = instant
		} else if len(exceeded) > 0 {
			banSignal = heaviestSignal(exceeded, r)
		}
	}
	rule := r.Signals[banSignal]

	evidence := map[string]any{
		"score":      score,
		"trigger":    string(trigger),
		"counts":     counts,
		"windows":    windowsOf(r),
		"thresholds": thresholdsOf(r),
	}
	if login != "" {
		evidence["login"] = clipString(login, 128)
	}
	for k, v := range extra {
		if _, dup := evidence[k]; !dup {
			evidence[k] = v
		}
	}
	// Port list for port-scan evidence, best effort — the ban path is cold,
	// one extra read is fine here.
	if ports, err := d.store.Members(ctx, scopeKey(SigDistinctPorts, ip)); err == nil && len(ports) > 0 {
		evidence["ports"] = ports
	}

	description := banSignal.BanDescription(int(counts[banSignal]), rule.WindowSeconds, proto)
	if sample := sampleOf(extra); sample != "" {
		description += fmt.Sprintf(" Last sample: %q.", clipString(sample, 120))
	}

	if d.bans == nil {
		return notifyErr
	}
	decision := "risk_score"
	if instant != "" {
		decision = "instant:" + string(banSignal)
	}
	if _, err := d.bans.Ban(ctx, BanInput{
		IP:          ip,
		Reason:      ReasonForSignal(banSignal, proto),
		Description: description,
		Evidence:    evidence,
		RiskScore:   score,
		Rule:        decision,
		BanType:     "automatic",
		CreatedBy:   CreatedBySystem,
	}); err != nil {
		return err
	}
	// The BanService writes the ban.created security_events row itself.
	return notifyErr
}

// notifyExceeded emits one signal_exceeded security_events row (action
// recorded, severity warning) the first time each crossed signal is seen
// within its window. Repeat crossings inside the window are suppressed via a
// bounded in-memory tracker.
func (d *Detector) notifyExceeded(ctx context.Context, ip netip.Addr, trigger Signal, proto string, extra map[string]any, r Rules, counts map[Signal]int64, exceeded []Signal) error {
	if d.ev == nil || len(exceeded) == 0 {
		return nil
	}
	now := d.now()
	var errs []error
	for _, sig := range exceeded {
		rule := r.Signals[sig]
		if rule.Threshold < 1 {
			continue
		}
		until := now.Add(time.Duration(rule.WindowSeconds) * time.Second)
		if !d.notified.Mark(string(sig)+"|"+ip.String(), until, now) {
			continue
		}
		ev := map[string]any{
			"count":          counts[sig],
			"threshold":      rule.Threshold,
			"window_seconds": rule.WindowSeconds,
			"trigger":        string(trigger),
		}
		for k, v := range extra {
			if _, dup := ev[k]; !dup {
				ev[k] = v
			}
		}
		if err := d.ev.SignalExceeded(ctx, ip.String(), sig, proto, portFromExtra(extra), ev, rule.Weight); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// notifyCache suppresses duplicate signal_exceeded events per IP+signal
// until the window closes. Bounded: when full it evicts expired entries,
// then arbitrary ones — losing an entry only causes a duplicate warning
// event, never lost security state.
type notifyCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
	max     int
}

func newNotifyCache(max int) *notifyCache {
	return &notifyCache{entries: make(map[string]time.Time), max: max}
}

// Mark records a notification for key valid until the given instant and
// reports whether this is the first notification in that period.
func (n *notifyCache) Mark(key string, until, now time.Time) bool {
	if n == nil {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if last, ok := n.entries[key]; ok && now.Before(last) {
		return false
	}
	if len(n.entries) >= n.max {
		for k, last := range n.entries {
			if !now.Before(last) {
				delete(n.entries, k)
			}
		}
		for k := range n.entries {
			if len(n.entries) < n.max {
				break
			}
			delete(n.entries, k)
		}
	}
	n.entries[key] = until
	return true
}

// scopeKey is the Redis key for a signal's per-IP window:
// sec:sig:<signal>:<ip> with the canonical (Unmap'd) address.
func scopeKey(sig Signal, ip netip.Addr) string {
	return signalKeyPrefix + string(sig) + ":" + ip.String()
}

// loginScopeKey is the per-login auth-failure window key:
// sec:sig:auth_failures:login:<sha256hex(login)>. The login is hashed so
// arbitrary attacker input never becomes a Redis key.
func loginScopeKey(login string) string {
	sum := sha256.Sum256([]byte(login))
	return signalKeyPrefix + string(SigAuthFailures) + ":" + loginScopePrefix + hex.EncodeToString(sum[:])
}

// canonicalAddr normalizes an address for keying: IPv4-mapped IPv6
// (::ffff:1.2.3.4) collapses to plain IPv4.
func canonicalAddr(ip netip.Addr) netip.Addr {
	return ip.Unmap()
}

// nanoMember is the unique window member for a point-in-time event.
func nanoMember(now time.Time) string {
	return strconv.FormatInt(now.UnixNano(), 10)
}

func extraWithPort(port int) map[string]any {
	if port <= 0 {
		return nil
	}
	return map[string]any{"port": port}
}

func portFromExtra(extra map[string]any) *int {
	if v, ok := extra["port"].(int); ok && v > 0 {
		p := v
		return &p
	}
	return nil
}

func sampleOf(extra map[string]any) string {
	if v, ok := extra["path"].(string); ok {
		return v
	}
	if v, ok := extra["detail"].(string); ok {
		return v
	}
	return ""
}

func windowsOf(r Rules) map[Signal]int {
	out := make(map[Signal]int, len(r.Signals))
	for sig, rule := range r.Signals {
		out[sig] = rule.WindowSeconds
	}
	return out
}

func thresholdsOf(r Rules) map[Signal]int {
	out := make(map[Signal]int, len(r.Signals))
	for sig, rule := range r.Signals {
		out[sig] = rule.Threshold
	}
	return out
}

func containsSignal(list []Signal, sig Signal) bool {
	for _, s := range list {
		if s == sig {
			return true
		}
	}
	return false
}

// heaviestSignal returns the first signal with the highest weight.
func heaviestSignal(list []Signal, r Rules) Signal {
	best := Signal("")
	weight := -1
	for _, s := range list {
		if w := r.Signals[s].Weight; w > weight {
			best, weight = s, w
		}
	}
	return best
}

// clipString shortens s to at most max bytes without splitting a multi-byte
// rune, for evidence and description fields fed from network input.
func clipString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// suspiciousPathPatterns are the high-risk probe paths the HTTP middleware
// checks before routing (and the detector treats as instant-evaluate
// triggers). Patterns are matched case-insensitively against the
// query-stripped, unescaped path; "*" matches any run of characters
// (including "/"), a pattern without "*" matches exactly.
var suspiciousPathPatterns = []string{
	"*.php", "*.asp", "*.aspx", "*.jsp", "*.cgi", "*.pl", "*.sql", "*.bak",
	"/.env*", "/.git*", "/.svn*", "/.hg*", "/.htaccess", "/.htpasswd", "/.DS_Store",
	"/.aws*", "/.ssh*", "/id_rsa*", "/id_dsa*",
	"/wp-login*", "/wp-admin*", "/wp-content*", "/wp-includes*", "/xmlrpc.php",
	"/phpmyadmin*", "/pma*", "/adminer*", "/myadmin*",
	"/cgi-bin/*",
	"/vendor/phpunit*", "/eval-stdin.php",
	"/actuator*", "/manager/*", "/jmx-console*",
	"/owa/*", "/autodiscover*", "/ecp/*",
	"/backup*", "/database.sql", "/db.sql", "/dump.sql",
}

// IsSuspiciousPath reports whether the request path matches a high-risk
// probe pattern. The HTTP middleware uses it for the pre-routing check and
// the detector uses it to decide whether a path hit is an
// instant-evaluate trigger.
func IsSuspiciousPath(path string) bool {
	p := normalizeProbePath(path)
	if p == "" || p == "/" {
		return false
	}
	for _, pattern := range suspiciousPathPatterns {
		if matchGlob(p, pattern) {
			return true
		}
	}
	return false
}

// normalizeProbePath lowercases the path and strips query, fragment and
// percent-encoding so "/.ENV?x=1" and "/%2Eenv" are both caught.
func normalizeProbePath(path string) string {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	return strings.ToLower(path)
}

// matchGlob matches a single "*" pattern: prefix + anything + suffix. A
// pattern without "*" is an exact match.
func matchGlob(path, pattern string) bool {
	i := strings.IndexByte(pattern, '*')
	if i < 0 {
		return path == pattern
	}
	prefix, suffix := pattern[:i], pattern[i+1:]
	return len(path) >= len(prefix)+len(suffix) &&
		strings.HasPrefix(path, prefix) &&
		strings.HasSuffix(path, suffix)
}
