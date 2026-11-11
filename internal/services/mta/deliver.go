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
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
)

// AttemptRecorder persists one durable attempt row per SMTP delivery attempt
// (ADR-001 §5: "delivery attempts are recorded"). Rows are written before the
// email's status changes, so the trace survives a crash between attempt and
// status update. Production: MTADeliveryAttemptRepository; tests: fakes.
type AttemptRecorder interface {
	RecordAttempt(models.MTADeliveryAttempt) error
}

// AttemptNumberer supplies the attempt ordinal stamped on each row. The
// repository implementation derives it from the stored rows; tests inject a
// counter.
type AttemptNumberer interface {
	NextAttemptNumber(emailID uint) int
}

// PolicyRefusal is a fail-closed policy rejection: no MX was contacted and no
// delivery may be attempted (unverified sender domain). It is permanent by
// construction — retrying cannot fix the configuration inside the task.
type PolicyRefusal struct {
	Reason string
	// UnverifiedSenderDomain marks the fail-closed sender-domain refusal
	// (non-negotiable #1 of ADR-001).
	UnverifiedSenderDomain bool
}

func (e *PolicyRefusal) Error() string { return e.Reason }

// DeliverySpec carries the per-domain delivery configuration resolved by the
// worker from the sender domain's row.
type DeliverySpec struct {
	SenderDomain   string
	DomainVerified bool
	BindIP         string
	Policy         Policy
	// PoolIPs lists the enabled member IPs of the domain's assigned pool
	// (OB-6 W3, ADR-007 decision 7); the executor rotates across them per
	// delivery. Empty = no pool — BindIP alone applies.
	PoolIPs []string
	// Warmup is the domain's active warm-up ramp; nil = none. While active,
	// its daily cap — spread evenly over the day's hours — caps the policy's
	// hourly budget (see EffectiveHourlyBudget).
	Warmup *WarmupSchedule
}

// Envelope is the delivery payload: the validated envelope plus the built
// (already DKIM-signed) message bytes. The recipient domain is re-derived
// from Recipients here — never trusted from stored input (ADR-001
// non-negotiable #2).
type Envelope struct {
	EmailID     uint
	WorkspaceID *uint
	From        string
	Recipients  []string
	Msg         []byte
}

// DeliveryResult reports what happened: Delivered true exactly when the
// remote MX answered 2xx at end-of-DATA for every recipient.
type DeliveryResult struct {
	Delivered bool
	MXHost    string
	Attempts  int
	Caps      SessionCaps
}

// Executor is the direct-to-MX delivery executor (ADR-001 §1/§6).
type Executor struct {
	resolver *Resolver
	budget   *BudgetKeeper
	attempts AttemptRecorder
	numberer AttemptNumberer
	// heloDomain is the EHLO identifier presented to remote MX hosts.
	heloDomain string
	// pickCounter round-robins pool member selection across concurrent
	// deliveries (atomic; wrapped only for determinism, not correctness).
	pickCounter atomic.Uint64
	// tlsConfigFor builds the STARTTLS config per remote host; certificate
	// verification is always on (tests inject a RootCAs pool for in-process
	// servers).
	tlsConfigFor func(host string) *tls.Config
}

// NewExecutor builds an executor. attempts may be nil (attempts not recorded —
// tests only; production always records). numberer falls back to a
// per-call counter starting at 1 when nil.
func NewExecutor(resolver *Resolver, budget *BudgetKeeper, attempts AttemptRecorder) *Executor {
	return &Executor{
		resolver:     resolver,
		budget:       budget,
		attempts:     attempts,
		numberer:     nil,
		heloDomain:   DefaultHeloDomain,
		tlsConfigFor: func(host string) *tls.Config { return &tls.Config{ServerName: host} },
	}
}

// SetHeloDomain overrides the EHLO identifier (process-wide EHLO name).
func (e *Executor) SetHeloDomain(d string) {
	if d != "" {
		e.heloDomain = d
	}
}

// SetAttemptNumberer supplies the attempt ordinal source.
func (e *Executor) SetAttemptNumberer(n AttemptNumberer) { e.numberer = n }

// SetTLSConfigFor overrides the per-host STARTTLS configuration (tests only;
// verification is never disabled here).
func (e *Executor) SetTLSConfigFor(fn func(host string) *tls.Config) {
	if fn != nil {
		e.tlsConfigFor = fn
	}
}

// Deliver performs one direct-to-MX delivery for a claimed email.
//
// Flow (ADR-001 §5/§6): fail-closed sender-domain check → source-IP selection
// (the domain's pool, rotating round-robin, else the static bind IP — OB-6
// W3, ADR-007 decision 7) → per-recipient-domain policy (hourly budget, with
// the active warm-up cap folded in; exhaustion defers) → MX resolution →
// attempt per MX host, bounded by policy → one durable attempt row per
// attempt → 2xx at end-of-DATA is the only delivered signal. Transient
// failures and permanent rejections surface as *email.SendError so the
// worker's existing R-05 classifier, bounce and suppression chains apply
// unchanged.
func (e *Executor) Deliver(ctx context.Context, spec DeliverySpec, env Envelope) (*DeliveryResult, error) {
	// Source-IP selection happens before any attempt is recorded so every
	// attempt row carries the address the delivery would have gone out from.
	bindIP := e.selectBindIP(spec)

	// Warm-up cap (ADR-007 decision 7): fold the active ramp's hourly cap
	// into the policy budget before the gate — the exhaustion/defer machinery
	// is shared, only the effective number changes.
	policy := spec.Policy
	policy.HourlyBudget = EffectiveHourlyBudget(spec.Policy, spec.Warmup, nowFunc())

	// Fail closed (non-negotiable #1): no direct attempt for an unverified
	// sender domain. Recorded durably like every other refusal.
	if !spec.DomainVerified {
		refusal := &PolicyRefusal{
			Reason:                 fmt.Sprintf("direct delivery refused: sender domain %q is not verified", spec.SenderDomain),
			UnverifiedSenderDomain: true,
		}
		e.record(env, attemptRow(env, e.nextAttempt(env), "", "", bindIP, 0, refusal.Reason,
			models.MTAAttemptRefusedUnverifiedSender, nowFunc(), nowFunc()))
		return nil, refusal
	}

	// Non-negotiable #2: re-derive recipient domains from the validated
	// recipients. An address without a usable domain is a permanent
	// recipient-level failure (550 5.1.3 at RCPT TO).
	groups, domains, err := groupByRecipientDomain(env.Recipients)
	if err != nil {
		return nil, err
	}

	result := &DeliveryResult{}
	attemptBase := e.nextAttempt(env)

	for _, domain := range domains {
		rcpts := groups[domain]

		// Per-domain policy: hourly budget (possibly capped by the active
		// warm-up ramp). Exhaustion defers the message without contacting
		// the MX (honor-throttle, ADR-001 §8).
		if e.budget != nil {
			if ok, delay := e.budget.Allow(domain, policy, nowFunc()); !ok {
				e.record(env, attemptRow(env, attemptBase, "", "", bindIP, 450,
					deferredBudgetText(domain, delay, spec.Warmup),
					models.MTAAttemptDeferredBudget, nowFunc(), nowFunc()))
				return result, &email.SendError{
					Stage: "POLICY", Code: 450,
					Msg: fmt.Sprintf("hourly budget exhausted for domain %s", domain),
					Err: fmt.Errorf("mta: deferred (budget), retry after %s", delay),
				}
			}
			e.budget.Record(domain, nowFunc())
		}

		hosts, rerr := e.resolver.Resolve(ctx, domain)
		if rerr != nil {
			e.record(env, attemptRow(env, attemptBase, "", "", bindIP, 0, rerr.Error(),
				models.MTAAttemptDNSFailure, nowFunc(), nowFunc()))
			// DNS failures are transient by classification (the error text
			// carries the resolver verdict); the retry path re-resolves.
			return result, email.WrapSendError("MX", "", fmt.Errorf("MX resolution failed for %s: %w", domain, rerr))
		}
		if len(hosts) == 0 {
			e.record(env, attemptRow(env, attemptBase, "", "", bindIP, 550,
				fmt.Sprintf("5.1.2 no MX or address records for %s", domain),
				models.MTAAttemptPermanentFailure, nowFunc(), nowFunc()))
			return result, &email.SendError{
				Stage: "MX", Code: 550,
				Msg: fmt.Sprintf("5.1.2 no MX or address records for %s", domain),
				Err: fmt.Errorf("mta: no delivery target for %s", domain),
			}
		}

		bound := boundAttempts(policy, len(hosts))
		var lastErr error
		for _, h := range hosts[:bound] {
			if err := ctx.Err(); err != nil {
				return result, email.WrapSendError("DIAL", "", err)
			}
			attemptNo := attemptBase + result.Attempts
			startedAt := nowFunc()

			opts := SessionOptions{
				Addr:       h.Addr(),
				ServerName: h.Host,
				HeloDomain: e.heloDomain,
				BindIP:     parseBindIP(bindIP),
				Target:     TargetMX,
				TLSConfig:  e.tlsConfigFor(h.Host),
			}
			sres, serr := runSession(ctx, opts, env.From, rcpts, env.Msg)
			finishedAt := nowFunc()
			result.Attempts++

			ip := ""
			if sres != nil {
				if host, _, splitErr := net.SplitHostPort(sres.RemoteIP); splitErr == nil {
					ip = host
				}
			}
			outcome, code, text := classifySession(sres, serr)
			e.record(env, attemptRow(env, attemptNo, h.Host, ip, bindIP, code, text, outcome, startedAt, finishedAt))

			if serr == nil {
				result.Delivered = true
				result.MXHost = h.Host
				if sres != nil {
					result.Caps = sres.Caps
				}
				// A later host delivered — clear any earlier host's failure so
				// the post-loop check below does not return a stale error.
				lastErr = nil
				break
			}

			// Record the failure and try the next MX host, bounded by the
			// domain policy. A permanent recipient-level rejection at RCPT TO
			// must not be retried on the next MX host: the address is dead
			// everywhere, so it feeds the bounce/suppression chain directly.
			lastErr = serr
			result.MXHost = h.Host
			if se, ok := serr.(*email.SendError); ok && se.Permanent() && se.Stage == "RCPT TO" {
				return result, serr
			}
		}

		if lastErr != nil {
			result.Delivered = false
			return result, lastErr
		}
	}

	return result, nil
}

// boundAttempts caps the number of MX hosts tried by the domain policy.
func boundAttempts(p Policy, n int) int {
	max := p.MaxHostAttempts
	if max <= 0 {
		max = DefaultMaxHostAttempts
	}
	if n > max {
		return max
	}
	return n
}

// selectBindIP picks the outbound source address for one delivery: a
// round-robin member of the sender domain's pool when one is configured
// (OB-6 W3, ADR-007 decision 7), otherwise the domain's static bind IP,
// otherwise the OS default (empty string). Blank pool entries are skipped so
// a hand-edited member row cannot blank the source; an all-blank pool falls
// back to the static bind IP.
func (e *Executor) selectBindIP(spec DeliverySpec) string {
	if len(spec.PoolIPs) > 0 {
		ips := make([]string, 0, len(spec.PoolIPs))
		for _, ip := range spec.PoolIPs {
			if t := strings.TrimSpace(ip); t != "" {
				ips = append(ips, t)
			}
		}
		if len(ips) > 0 {
			n := e.pickCounter.Add(1) - 1
			return ips[int(n)%len(ips)]
		}
	}
	return spec.BindIP
}

// deferredBudgetText is the response text recorded on a budget-deferred
// attempt. When a warm-up ramp produced the effective cap, the text says so —
// an operator reading the attempt log must be able to tell a warm-up deferral
// from a plain per-domain budget exhaustion.
func deferredBudgetText(domain string, delay time.Duration, warmup *WarmupSchedule) string {
	if warmup != nil && warmup.Days > 0 && WarmupDailyCap(warmup, nowFunc()) > 0 {
		return fmt.Sprintf("4.7.1 hourly budget exhausted for %s (warm-up cap %d/day); defer %s",
			domain, WarmupDailyCap(warmup, nowFunc()), delay)
	}
	return fmt.Sprintf("4.7.1 hourly budget exhausted for %s; defer %s", domain, delay)
}

func parseBindIP(s string) net.IP {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return net.ParseIP(s)
}

func (e *Executor) nextAttempt(env Envelope) int {
	if e.numberer != nil {
		return e.numberer.NextAttemptNumber(env.EmailID)
	}
	return 1
}

// groupByRecipientDomain re-derives recipient domains from the validated
// recipients and groups them, with domains sorted for deterministic attempt
// numbering. A recipient without a parseable domain is a permanent recipient
// failure (550 5.1.3).
func groupByRecipientDomain(recipients []string) (map[string][]string, []string, error) {
	groups := make(map[string][]string, 1)
	seen := make(map[string]bool, 1)
	var domains []string
	for _, r := range recipients {
		addr := email.EnvelopeAddress(r)
		domain := domainOf(addr)
		if domain == "" {
			return nil, nil, &email.SendError{
				Stage: "RCPT TO", Recipient: addr, Code: 550,
				Msg: "5.1.3 recipient address has no domain",
				Err: fmt.Errorf("mta: invalid recipient %q", addr),
			}
		}
		if !seen[domain] {
			seen[domain] = true
			domains = append(domains, domain)
		}
		groups[domain] = append(groups[domain], addr)
	}
	sort.Strings(domains)
	return groups, domains, nil
}

// domainOf returns the lowercased domain of a bare address, or "".
func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i+1 < len(addr) {
		return strings.ToLower(strings.TrimSpace(addr[i+1:]))
	}
	return ""
}

// classifySession maps a session outcome onto an attempt outcome + reply
// data. Delivered requires a 2xx at end-of-DATA (serr == nil implies that —
// runSession only returns nil after DATA 2xx).
func classifySession(sres *SessionResult, serr error) (outcome string, code int, text string) {
	if serr == nil && sres != nil && sres.Delivered {
		return models.MTAAttemptDelivered, sres.Code, sres.Msg
	}
	var se *email.SendError
	if errors.As(serr, &se) {
		text = se.Msg
		if text == "" && se.Err != nil {
			text = se.Err.Error()
		}
		if se.Permanent() {
			return models.MTAAttemptPermanentFailure, se.Code, text
		}
		// 4xx replies and connection-level failures (no SMTP reply code at
		// all) are transient: the message is re-enqueued with the existing
		// backoff.
		return models.MTAAttemptTransientFailure, se.Code, text
	}
	if serr != nil {
		return models.MTAAttemptTransientFailure, 0, serr.Error()
	}
	return models.MTAAttemptTransientFailure, 0, "unknown session failure"
}

// attemptRow builds one durable attempt record. sourceIP is the outbound
// source address the attempt went out from (pool member or static bind IP;
// blank when neither is configured) — the attribution key for per-IP
// reputation rollups (OB-6 W3).
func attemptRow(env Envelope, attempt int, mxHost, ip, sourceIP string, code int, text, outcome string, startedAt, finishedAt time.Time) models.MTADeliveryAttempt {
	return models.MTADeliveryAttempt{
		WorkspaceID:  env.WorkspaceID,
		EmailID:      env.EmailID,
		Attempt:      attempt,
		MXHost:       mxHost,
		IP:           ip,
		SourceIP:     sourceIP,
		ResponseCode: code,
		ResponseText: truncateResponse(text),
		Outcome:      outcome,
		StartedAt:    startedAt,
		FinishedAt:   finishedAt,
	}
}

// record writes one attempt row; a recording failure must never block the
// delivery report (the attempt already happened on the wire).
func (e *Executor) record(env Envelope, row models.MTADeliveryAttempt) {
	if e.attempts == nil {
		return
	}
	_ = e.attempts.RecordAttempt(row)
}

func truncateResponse(s string) string {
	if len(s) > 2048 {
		return s[:2048]
	}
	return s
}
