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

package inbound

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Inbound forwarding executor (OB-2, ADR-002 decision 2). Consumes the durable
// inbound_forward_tasks rows the routing engine queues after a message is
// durably persisted, and re-sends the stored original through the outbound
// pipeline as a system_forward email row.
//
// Binding constraints (ADR-002):
//   - The original stays durably stored regardless of the forward outcome —
//     the executor only ever flips the inbound row to forwarded on success and
//     never deletes or rewrites its content.
//   - The copy leaves through the email service's system path, which never
//     touches the marketing/consent layers; signing happens on the standard
//     ADR-001 delivery path (domain_signing_keys at send time).
//   - Forward targets must be confirmed (single-use token) before a rule
//     using them forwards — the executor refuses an unconfirmed target.
//   - Loop protection: X-Forwarded-For-style hop accounting. Every copy this
//     executor produces carries an X-Posta-Forwarded-For header chain; when a
//     copy loops back through our own receiver and is forwarded again, the
//     next task's hop count grows with the chain. A forward deeper than
//     MaxForwardHops is rejected outright.
//
// Abuse protection: a per-target rate cap (hourly + daily windows, the
// limiter pattern from internal/services/ratelimit) bounds how much volume a
// single target address can absorb.

const (
	// MaxForwardHops is the deepest forwarding chain this system performs.
	// The first forward is hop 1; a task whose hop count would exceed this
	// value is failed permanently (never retried, never forwarded).
	MaxForwardHops = 3
	// MaxForwardTaskAttempts is the retry budget of one forwarding task,
	// matching the standard worker retry default (POSTA_WORKER_MAX_RETRIES).
	MaxForwardTaskAttempts = 5
	// forwardRetryBase / forwardRetryCap shape the standard backoff between
	// attempts: base·2^(attempt-1) with a hard cap and jitter (the R-06
	// backoff shape used by the webhook dispatcher, scaled for a durable
	// queue: minutes, not seconds).
	forwardRetryBase = time.Minute
	forwardRetryCap  = time.Hour
	// staleRunningFor re-arms tasks an executor died holding (crash between
	// claim and terminal write) — the lost-task reconcile pattern (R-03).
	staleRunningFor = 10 * time.Minute

	// HeaderForwardedFor is the X-Forwarded-For-style chain header the
	// executor stamps on every copy: the comma-separated list of forward
	// targets already applied. A client can spoof the header on a message it
	// SENDS us (which can only make hops higher — fail-closed); it cannot
	// remove the header from a copy this system produced on the receiving
	// side without controlling that mailbox.
	HeaderForwardedFor = "X-Posta-Forwarded-For"
	// HeaderForwardedFrom names the original sender on the copy.
	HeaderForwardedFrom = "X-Posta-Forwarded-From"

	// DefaultForwardTargetHourly / DefaultForwardTargetDaily are the
	// per-target forwarding caps applied by the executor (0 = unlimited).
	DefaultForwardTargetHourly = 60
	DefaultForwardTargetDaily  = 500

	// forwardBatchLimit bounds one executor sweep.
	forwardBatchLimit = 50
)

// forwardSubjectPrefix marks a re-send copy in the subject line.
const forwardSubjectPrefix = "Fwd: "

// ForwardTaskStore is the durable queue seam the executor consumes.
// *repositories.InboundForwardTaskRepository satisfies it.
type ForwardTaskStore interface {
	ListDue(now time.Time, limit int) ([]models.InboundForwardTask, error)
	Claim(id uint) (bool, error)
	MarkDone(task *models.InboundForwardTask) error
	MarkRetry(task *models.InboundForwardTask, next time.Time, lastErr string) error
	MarkFailed(task *models.InboundForwardTask, lastErr string) error
	RequeueStaleRunning(staleFor time.Duration, now time.Time) (int64, error)
}

// InboundRecordSource loads and updates the durable original.
// *repositories.InboundEmailRepository satisfies it.
type InboundRecordSource interface {
	FindByID(id uint) (*models.InboundEmail, error)
	Update(rec *models.InboundEmail) error
}

// ForwardRawSource loads the original's raw RFC 5322 bytes (inline column or
// blob fallback). *Service satisfies it via LoadRaw.
type ForwardRawSource interface {
	LoadRaw(ctx context.Context, rec *models.InboundEmail) ([]byte, error)
}

// ForwardDomainSource resolves the tenant-verified domain row.
// *repositories.DomainRepository satisfies it.
type ForwardDomainSource interface {
	FindByID(id uint) (*models.Domain, error)
}

// ForwardSendRequest describes one system_forward copy the executor hands to
// the outbound pipeline. It is deliberately inbound-local (no import of the
// email package — the email package's tests import this package, so a
// forward dependency would close a cycle): the wiring adapter in
// internal/routes maps it onto email.SystemForwardRequest.
type ForwardSendRequest struct {
	UserID      uint
	WorkspaceID *uint
	// From is the receiving address on the tenant-verified domain the copy is
	// sent from — DKIM signing resolves its domain's keys at delivery time
	// (ADR-001 path). Never a client-supplied authority: the executor derives
	// it from the durable inbound record.
	From string
	To   []string
	// Subject/Text/HTML carry the copy's content. Headers are system headers
	// (Message-ID threading, X-Posta-Forwarded-For loop evidence) — never
	// accepted from external input.
	Subject     string
	Text        string
	HTML        string
	Headers     map[string]string
	Attachments []models.Attachment
	// Metadata lands on the copy's "forwarded" email_events row: the inbound
	// message's identity, the route that matched and the hop count.
	Metadata map[string]any
}

// ForwardSendResponse is the enqueue result of a system_forward copy.
type ForwardSendResponse struct {
	EmailUUID string
	Status    models.EmailStatus
}

// SystemMailer creates the system_forward copy through the outbound pipeline.
// *email.Service (adapted at the wiring site) satisfies it.
type SystemMailer interface {
	SendSystemForward(ctx context.Context, req *ForwardSendRequest) (*ForwardSendResponse, error)
}

// SystemMailerFunc adapts a function to the SystemMailer seam.
type SystemMailerFunc func(ctx context.Context, req *ForwardSendRequest) (*ForwardSendResponse, error)

func (f SystemMailerFunc) SendSystemForward(ctx context.Context, req *ForwardSendRequest) (*ForwardSendResponse, error) {
	return f(ctx, req)
}

// TargetStore reports forward-target confirmation.
// *repositories.InboundForwardTargetRepository satisfies it via FindConfirmed.
type TargetStore interface {
	FindConfirmed(scope repositories.ResourceScope, target string) (*models.InboundForwardTarget, error)
}

// TargetRateLimiter gates per-target forwarding volume.
type TargetRateLimiter interface {
	AllowTarget(ctx context.Context, scope repositories.ResourceScope, target string) error
}

// Forwarder executes due inbound_forward_tasks rows.
type Forwarder struct {
	tasks      ForwardTaskStore
	inbounds   InboundRecordSource
	raws       ForwardRawSource
	domains    ForwardDomainSource
	mailer     SystemMailer
	targets    TargetStore
	limiter    TargetRateLimiter
	bus        *eventbus.EventBus
	batchLimit int
	// now overrides the clock (tests).
	now func() time.Time
	// jitter returns the random component added to a backoff slot (tests pin it).
	jitter func() time.Duration
}

func NewForwarder(
	tasks ForwardTaskStore,
	inbounds InboundRecordSource,
	raws ForwardRawSource,
	domains ForwardDomainSource,
	mailer SystemMailer,
) *Forwarder {
	return &Forwarder{
		tasks:      tasks,
		inbounds:   inbounds,
		raws:       raws,
		domains:    domains,
		mailer:     mailer,
		batchLimit: forwardBatchLimit,
		now:        time.Now,
		jitter: func() time.Duration {
			// Jitter over 1/4 of the base window, mirroring the R-06 shape.
			buf := make([]byte, 1)
			if _, err := rand.Read(buf); err != nil {
				return 0
			}
			return time.Duration(buf[0]%16) * time.Second
		},
	}
}

// SetTargetStore attaches the confirmed-forward-target check. When nil, every
// target is treated as unconfirmed (fail-closed: forwarding never runs).
func (f *Forwarder) SetTargetStore(t TargetStore) { f.targets = t }

// SetTargetLimiter attaches the per-target abuse cap. When nil, no cap is
// applied — production wiring always attaches one.
func (f *Forwarder) SetTargetLimiter(l TargetRateLimiter) { f.limiter = l }

// SetEventBus publishes inbound.forwarded events on success (nil = no-op).
func (f *Forwarder) SetEventBus(b *eventbus.EventBus) { f.bus = b }

// SetBatchLimit overrides the per-sweep task cap (tests).
func (f *Forwarder) SetBatchLimit(n int) {
	if n > 0 {
		f.batchLimit = n
	}
}

// SetNow overrides the clock (tests).
func (f *Forwarder) SetNow(fn func() time.Time) {
	if fn != nil {
		f.now = fn
	}
}

// SetJitter overrides the backoff jitter (tests).
func (f *Forwarder) SetJitter(fn func() time.Duration) {
	if fn != nil {
		f.jitter = fn
	}
}

// forwardRetryDelay returns the standard backoff slot after the given 1-based
// attempt: base·2^(attempt-1), hard-capped, plus jitter.
func forwardRetryDelay(attempt int, jitter func() time.Duration) time.Duration {
	backoff := forwardRetryBase << (attempt - 1)
	if backoff <= 0 || backoff > forwardRetryCap {
		backoff = forwardRetryCap
	}
	if jitter != nil {
		backoff += jitter()
	}
	return backoff
}

// RunOnce re-queues stale running rows (a crashed executor's orphans), then
// claims and processes up to the batch limit of due tasks. Returns the number
// of tasks processed (terminal or retried). A per-task failure is contained;
// only a store-level read error aborts the sweep.
func (f *Forwarder) RunOnce(ctx context.Context) (int, error) {
	if _, err := f.tasks.RequeueStaleRunning(staleRunningFor, f.now()); err != nil {
		logger.Error("inbound forwarder: failed to requeue stale running tasks", "error", err)
	}
	due, err := f.tasks.ListDue(f.now(), f.batchLimit)
	if err != nil {
		return 0, fmt.Errorf("list due forward tasks: %w", err)
	}
	processed := 0
	for i := range due {
		select {
		case <-ctx.Done():
			return processed, ctx.Err()
		default:
		}
		f.process(ctx, &due[i])
		processed++
	}
	return processed, nil
}

// process runs one claimed task through the guard chain. Every branch is
// terminal for the task (done / failed / retried with backoff) — a claimed
// task never stays running past this call except on a panic.
func (f *Forwarder) process(ctx context.Context, task *models.InboundForwardTask) {
	claimed, err := f.tasks.Claim(task.ID)
	if err != nil {
		logger.Error("inbound forwarder: claim failed", "task_id", task.ID, "error", err)
		return
	}
	if !claimed {
		return // another sweep won the row
	}
	// Refresh the task's post-claim state (attempts incremented, status
	// running) for the Mark* calls below.
	task.Status = models.InboundForwardTaskRunning
	task.Attempts++

	if task.Attempts > MaxForwardTaskAttempts {
		f.fail(task, fmt.Sprintf("retry budget exhausted after %d attempts", task.Attempts-1))
		return
	}

	target := strings.ToLower(strings.TrimSpace(task.Target))
	if _, err := mail.ParseAddress(target); err != nil {
		f.fail(task, fmt.Sprintf("forward target %q is not a valid address", task.Target))
		return
	}

	rec, err := f.inbounds.FindByID(task.InboundEmailID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			f.fail(task, "original inbound message no longer exists")
			return
		}
		f.retry(task, "failed to load original message")
		return
	}

	// Loop protection: derive the hop count before anything leaves.
	hops, chain := f.resolveHops(ctx, task, rec)
	task.Hops = hops
	if hops > MaxForwardHops {
		f.fail(task, fmt.Sprintf("forward hop limit exceeded (%d > %d)", hops, MaxForwardHops))
		return
	}

	scope := repositories.ResourceScope{UserID: task.UserID, WorkspaceID: task.WorkspaceID}

	// Confirmation gate: the target must have completed its single-use
	// confirmation before this rule forwards to it. Unconfirmed waits (the
	// operator may still confirm) — bounded by the retry budget.
	if f.targets != nil {
		if _, err := f.targets.FindConfirmed(scope, target); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				f.retry(task, "forward target is not confirmed")
				return
			}
			f.retry(task, "failed to check forward target confirmation")
			return
		}
	} else {
		f.retry(task, "forward target confirmation store is not wired")
		return
	}

	// Per-target abuse cap (limiter pattern). A limiter error is transient —
	// the task comes back through the standard backoff.
	if f.limiter != nil {
		if err := f.limiter.AllowTarget(ctx, scope, target); err != nil {
			f.retry(task, "forward target rate limit: "+err.Error())
			return
		}
	}

	// Build and enqueue the system_forward copy. The mailer owns the row,
	// the forwarded email_event, and the enqueue; delivery + signing happen
	// on the standard outbound path with the standard backoff.
	resp, err := f.deliver(ctx, task, rec, chain)
	if err != nil {
		f.retry(task, "failed to create system forward: "+err.Error())
		return
	}

	if err := f.tasks.MarkDone(task); err != nil {
		logger.Error("inbound forwarder: copy enqueued but task not finalized",
			"task_id", task.ID, "email_id", resp.EmailUUID, "error", err)
		return
	}

	// Success: mark the ORIGINAL inbound row forwarded. The row itself is
	// never deleted or rewritten — durable regardless of any earlier failures.
	now := f.now().UTC()
	rec.Status = models.InboundStatusForwarded
	rec.ForwardedAt = &now
	if err := f.inbounds.Update(rec); err != nil {
		logger.Error("inbound forwarder: failed to mark original forwarded",
			"inbound_id", rec.UUID, "task_id", task.ID, "error", err)
	}

	f.publishForwarded(rec, task, resp.EmailUUID, hops)
	logger.Info("inbound forwarder: forwarded",
		"task_id", task.ID, "inbound_id", rec.UUID, "target", task.Target,
		"hops", hops, "email_id", resp.EmailUUID)
}

// deliver builds the system_forward copy from the durable original and hands
// it to the outbound pipeline via the mailer seam.
func (f *Forwarder) deliver(ctx context.Context, task *models.InboundForwardTask, rec *models.InboundEmail, priorChain []string) (*ForwardSendResponse, error) {
	from, err := f.forwardFromAddress(task, rec)
	if err != nil {
		return nil, err
	}

	subject := rec.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	subject = forwardSubjectPrefix + subject

	// Deterministic Message-ID keyed on the task: a re-run after a crash
	// between enqueue and finalize produces the same Message-ID, so
	// downstream Message-ID dedup limits the duplicate to one.
	msgID := fmt.Sprintf("<forward-%d-%d@%s>", task.InboundEmailID, task.ID, domainOf(from))

	// X-Forwarded-For-style loop evidence: the prior chain plus this hop.
	target := strings.ToLower(strings.TrimSpace(task.Target))
	chain := append(append([]string{}, priorChain...), target)

	headers := map[string]string{
		"Message-ID":        msgID,
		HeaderForwardedFor:  strings.Join(chain, ", "),
		HeaderForwardedFrom: rec.Sender,
	}
	if rec.MessageID != "" {
		headers["In-Reply-To"] = "<" + rec.MessageID + ">"
		headers["References"] = "<" + rec.MessageID + ">"
	}
	if _, err := mail.ParseAddress(rec.Sender); err == nil {
		// Replies to a forwarded copy go to the original sender, not back
		// into this system (auto-reply loop safety).
		headers["Reply-To"] = rec.Sender
	}

	atts, err := forwardAttachments(rec)
	if err != nil {
		return nil, err
	}

	return f.mailer.SendSystemForward(ctx, &ForwardSendRequest{
		UserID:      task.UserID,
		WorkspaceID: task.WorkspaceID,
		From:        from,
		To:          []string{target},
		Subject:     subject,
		Text:        rec.TextBody,
		HTML:        rec.HTMLBody,
		Headers:     headers,
		Attachments: atts,
		Metadata: map[string]any{
			"inbound_id": rec.UUID,
			"task_id":    task.ID,
			"route_id":   task.RouteID,
			"hops":       len(chain),
		},
	})
}

// forwardFromAddress derives the copy's From: the original envelope recipient
// on the task's verified domain (never a client-supplied authority). Falls
// back to postmaster@<domain> when the stored recipients do not name one.
func (f *Forwarder) forwardFromAddress(task *models.InboundForwardTask, rec *models.InboundEmail) (string, error) {
	if f.domains == nil {
		return "", fmt.Errorf("domain source is not wired")
	}
	domain, err := f.domains.FindByID(task.DomainID)
	if err != nil {
		return "", fmt.Errorf("resolve forward domain %d: %w", task.DomainID, err)
	}
	for _, r := range rec.Recipients {
		addr := strings.ToLower(strings.TrimSpace(r))
		at := strings.LastIndex(addr, "@")
		if at > 0 && strings.EqualFold(addr[at+1:], domain.Domain) {
			return addr, nil
		}
	}
	return "postmaster@" + domain.Domain, nil
}

// resolveHops reports the forward depth of this task and the prior forward
// chain carried by the stored original's X-Posta-Forwarded-For header.
//
// An explicit task.Hops (> 0 — e.g. a task re-armed after a crashed run that
// already derived its depth) wins over re-derivation. Otherwise the original's
// chain is counted: this forward is one deeper than the chain. A readable
// original with no chain is hop 1; raw bytes that are unavailable (scrubbed by
// retention, or the webhook path storing none) are ALSO treated as hop 1 —
// documented under-count that can only shorten a chain retention has already
// aged out, never bypass the limit on live mail.
func (f *Forwarder) resolveHops(ctx context.Context, task *models.InboundForwardTask, rec *models.InboundEmail) (int, []string) {
	chain := f.priorChain(ctx, rec)
	if task.Hops > 0 {
		return task.Hops, chain
	}
	return len(chain) + 1, chain
}

// priorChain loads the original's X-Posta-Forwarded-For entries.
func (f *Forwarder) priorChain(ctx context.Context, rec *models.InboundEmail) []string {
	if f.raws == nil {
		return nil
	}
	raw, err := f.raws.LoadRaw(ctx, rec)
	if err != nil || len(raw) == 0 {
		return nil
	}
	return parseForwardChain(raw)
}

// parseForwardChain extracts the X-Posta-Forwarded-For entries from the raw
// message's header block (a header-only parse — the body is not touched).
func parseForwardChain(raw []byte) []string {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return nil
	}
	value := msg.Header.Get(HeaderForwardedFor)
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// forwardAttachments converts the stored inbound attachment metadata into
// sendable attachments, resolving blob-backed content when configured.
func forwardAttachments(rec *models.InboundEmail) ([]models.Attachment, error) {
	if rec.AttachmentsJSON == "" {
		return nil, nil
	}
	var metas []models.InboundAttachmentMeta
	if err := json.Unmarshal([]byte(rec.AttachmentsJSON), &metas); err != nil {
		return nil, fmt.Errorf("decode stored attachments: %w", err)
	}
	if len(metas) == 0 {
		return nil, nil
	}
	atts := make([]models.Attachment, 0, len(metas))
	for _, m := range metas {
		atts = append(atts, models.Attachment{
			Filename:    m.Filename,
			ContentType: m.ContentType,
			Content:     m.Content,
			StorageKey:  m.StorageKey,
		})
	}
	return atts, nil
}

// fail terminates a task permanently (never retried).
func (f *Forwarder) fail(task *models.InboundForwardTask, reason string) {
	if err := f.tasks.MarkFailed(task, reason); err != nil {
		logger.Error("inbound forwarder: failed to mark task failed", "task_id", task.ID, "error", err)
		return
	}
	logger.Warn("inbound forwarder: task failed permanently", "task_id", task.ID, "reason", reason)
}

// retry returns a task to the queue with the standard backoff.
func (f *Forwarder) retry(task *models.InboundForwardTask, reason string) {
	next := f.now().Add(forwardRetryDelay(task.Attempts, f.jitter))
	if err := f.tasks.MarkRetry(task, next, reason); err != nil {
		logger.Error("inbound forwarder: failed to requeue task", "task_id", task.ID, "error", err)
		return
	}
	logger.Info("inbound forwarder: task requeued",
		"task_id", task.ID, "attempt", task.Attempts, "next", next.UTC().Format(time.RFC3339), "reason", reason)
}

// publishForwarded emits the success event into the events substrate.
func (f *Forwarder) publishForwarded(rec *models.InboundEmail, task *models.InboundForwardTask, emailUUID string, hops int) {
	if f.bus == nil {
		return
	}
	actor := task.UserID
	f.bus.PublishScoped(
		task.WorkspaceID,
		models.EventCategoryEmail,
		"inbound.forwarded",
		&actor,
		"",
		"",
		"Inbound email forwarded to "+task.Target,
		map[string]any{
			"inbound_id":   rec.UUID,
			"task_id":      task.ID,
			"route_id":     task.RouteID,
			"target":       task.Target,
			"hops":         hops,
			"email_uuid":   emailUUID,
			"workspace_id": task.WorkspaceID,
		},
	)
}

// domainOf returns the host part of an address, or a placeholder.
func domainOf(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at < 0 || at == len(addr)-1 {
		return "invalid"
	}
	return addr[at+1:]
}

// ---------- forward-target confirmation service ----------

// ErrInvalidForwardTarget rejects a malformed target address.
var ErrInvalidForwardTarget = errors.New("forward target is not a valid email address")

// ForwardTargetRepo is the store seam of the confirmation flow.
// *repositories.InboundForwardTargetRepository satisfies it.
type ForwardTargetRepo interface {
	Create(t *models.InboundForwardTarget) error
	Delete(id uint) error
	FindByID(id uint) (*models.InboundForwardTarget, error)
	FindByTokenHash(tokenHash string) (*models.InboundForwardTarget, error)
	FindConfirmed(scope repositories.ResourceScope, target string) (*models.InboundForwardTarget, error)
	FindByScope(scope repositories.ResourceScope, limit, offset int) ([]models.InboundForwardTarget, int64, error)
	Confirm(id uint, tokenHash string, at time.Time) (bool, error)
}

// ForwardTargetService manages the confirmed forwarding destinations:
// creation (issuing the single-use token), confirmation, listing, revocation.
// The raw token exists exactly once — in the return value of Create and in
// the plain-text confirm URL the operator sends manually; only its SHA-256
// hash is persisted.
type ForwardTargetService struct {
	repo ForwardTargetRepo
	now  func() time.Time
	// random supplies the raw token bytes (tests pin it).
	random func() (raw string, err error)
}

func NewForwardTargetService(repo ForwardTargetRepo) *ForwardTargetService {
	return &ForwardTargetService{repo: repo, now: time.Now}
}

// SetNow overrides the clock (tests).
func (s *ForwardTargetService) SetNow(fn func() time.Time) {
	if fn != nil {
		s.now = fn
	}
}

// SetRandom overrides the token source (tests).
func (s *ForwardTargetService) SetRandom(fn func() (string, error)) {
	if fn != nil {
		s.random = fn
	}
}

// CreatedTarget is the create result: the stored row plus the one-time
// plain-text confirmation URL (rendered once — the raw token is never
// persisted nor shown again).
type CreatedTarget struct {
	Target     models.InboundForwardTarget `json:"target"`
	ConfirmURL string                      `json:"confirm_url"`
}

// ErrForwardTargetNeedsWorkspace refuses target creation without an active
// workspace: every resource in this system is workspace-scoped (ApplyScope
// matches nothing without one), so a personal-mode target could never be
// confirmed or matched by the executor — refusing up front is the honest
// behavior.
var ErrForwardTargetNeedsWorkspace = errors.New("forward targets require an active workspace")

// Create registers a new forward target and issues its single-use
// confirmation token. The address is stored lowercased; the confirm URL is
// the caller-provided public base + /api/v1/inbound/forward-confirm/<token>.
func (s *ForwardTargetService) Create(scope repositories.ResourceScope, target, confirmBaseURL string) (*CreatedTarget, error) {
	if scope.WorkspaceID == nil {
		return nil, ErrForwardTargetNeedsWorkspace
	}
	addr := strings.ToLower(strings.TrimSpace(target))
	if _, err := mail.ParseAddress(addr); err != nil {
		return nil, ErrInvalidForwardTarget
	}
	raw, hashHex, err := newForwardConfirmToken(s.random)
	if err != nil {
		return nil, fmt.Errorf("generate confirm token: %w", err)
	}
	row := &models.InboundForwardTarget{
		UserID:      scope.UserID,
		WorkspaceID: scope.WorkspaceID,
		Target:      addr,
		TokenHash:   hashHex,
	}
	if err := s.repo.Create(row); err != nil {
		return nil, err
	}
	return &CreatedTarget{
		Target:     *row,
		ConfirmURL: strings.TrimRight(confirmBaseURL, "/") + "/api/v1/inbound/forward-confirm/" + raw,
	}, nil
}

// Confirm consumes a confirmation token: the matching target becomes
// confirmed and the token is cleared (single-use). A second click — or a
// click on a token that was revoked, or never issued — reports false. The
// confirmed address rides along for the public response.
func (s *ForwardTargetService) Confirm(rawToken string) (confirmed bool, target string, err error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return false, "", nil
	}
	row, err := s.repo.FindByTokenHash(hashForwardConfirmToken(rawToken))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, "", nil
		}
		return false, "", err
	}
	ok, err := s.repo.Confirm(row.ID, row.TokenHash, s.now().UTC())
	if err != nil || !ok {
		return false, "", err
	}
	logger.Info("inbound forward target confirmed", "target_id", row.ID, "target", row.Target)
	return true, row.Target, nil
}

// List returns the scope's forward targets, paged, newest first.
func (s *ForwardTargetService) List(scope repositories.ResourceScope, limit, offset int) ([]models.InboundForwardTarget, int64, error) {
	return s.repo.FindByScope(scope, limit, offset)
}

// RevokeOwned removes a target after verifying it belongs to the scope (the
// handler's ownsResource pattern). A foreign or missing ID reports
// gorm.ErrRecordNotFound — never confirming cross-scope existence.
func (s *ForwardTargetService) RevokeOwned(scope repositories.ResourceScope, id uint) (*models.InboundForwardTarget, error) {
	row, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if !repositories.OwnsResource(scope, row.UserID, row.WorkspaceID) {
		return nil, gorm.ErrRecordNotFound
	}
	if err := s.repo.Delete(id); err != nil {
		return nil, err
	}
	return row, nil
}

// newForwardConfirmToken mints the raw URL-safe token and its hash.
func newForwardConfirmToken(random func() (string, error)) (raw, hashHex string, err error) {
	if random != nil {
		raw, err = random()
		if err != nil {
			return "", "", err
		}
	} else {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return "", "", err
		}
		raw = hex.EncodeToString(buf)
	}
	return raw, hashForwardConfirmToken(raw), nil
}

// hashForwardConfirmToken is the store-side token representation (SHA-256 hex).
func hashForwardConfirmToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// redisTargetLimiter applies the per-target forwarding caps through Redis
// using the same windowed-counter shape as the ratelimit limiter (hourly and
// daily INCR keys with TTLs). A Redis error FAILS CLOSED: the task retries
// with the standard backoff instead of forwarding unaccounted.
type redisTargetLimiter struct {
	client redis.Cmdable
	hourly int
	daily  int
	now    func() time.Time
}

// NewRedisTargetLimiter builds the per-target limiter; hourly/daily of 0 mean
// unlimited for that window.
func NewRedisTargetLimiter(client redis.Cmdable, hourly, daily int) TargetRateLimiter {
	return &redisTargetLimiter{client: client, hourly: hourly, daily: daily, now: time.Now}
}

func (l *redisTargetLimiter) AllowTarget(ctx context.Context, scope repositories.ResourceScope, target string) error {
	subject := fmt.Sprintf("forward-target:%s:%s", scopeString(scope), strings.ToLower(target))
	hourKey := fmt.Sprintf("ratelimit:hour:%s:%s", subject, l.now().Format("2006010215"))
	dayKey := fmt.Sprintf("ratelimit:day:%s:%s", subject, l.now().Format("20060102"))

	if l.hourly > 0 {
		count, err := l.client.Incr(ctx, hourKey).Result()
		if err != nil {
			return fmt.Errorf("rate limit check failed: %w", err)
		}
		if count == 1 {
			l.client.Expire(ctx, hourKey, time.Hour)
		}
		if count > int64(l.hourly) {
			return fmt.Errorf("hourly rate limit exceeded (%d/%d)", count, l.hourly)
		}
	}
	if l.daily > 0 {
		count, err := l.client.Incr(ctx, dayKey).Result()
		if err != nil {
			return fmt.Errorf("rate limit check failed: %w", err)
		}
		if count == 1 {
			l.client.Expire(ctx, dayKey, 24*time.Hour)
		}
		if count > int64(l.daily) {
			return fmt.Errorf("daily rate limit exceeded (%d/%d)", count, l.daily)
		}
	}
	return nil
}

// scopeString renders a scope for limiter keys.
func scopeString(scope repositories.ResourceScope) string {
	if scope.WorkspaceID != nil {
		return fmt.Sprintf("ws-%d", *scope.WorkspaceID)
	}
	return fmt.Sprintf("u-%d", scope.UserID)
}
