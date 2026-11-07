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
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/gorm"
)

// OB-2 forwarding executor tests (ADR-002 decision 2). Everything runs on
// plain fakes of the executor's narrow seams — no database, no Redis, no
// network — so the battery can run them anywhere. The seams are exactly the
// production ones: *repositories.InboundForwardTaskRepository,
// *repositories.InboundEmailRepository, *Service (LoadRaw),
// *repositories.DomainRepository, *email.Service (SendSystemForward) and
// *repositories.InboundForwardTargetRepository satisfy them in wiring.

// ---------- fakes ----------

type fwTaskStore struct {
	mu       sync.Mutex
	rows     map[uint]*models.InboundForwardTask
	order    []uint
	listErr  error
	requeued int
}

func newFwTaskStore(tasks ...*models.InboundForwardTask) *fwTaskStore {
	s := &fwTaskStore{rows: map[uint]*models.InboundForwardTask{}}
	for _, t := range tasks {
		cp := *t
		s.rows[t.ID] = &cp
		s.order = append(s.order, t.ID)
	}
	return s
}

func (s *fwTaskStore) ListDue(now time.Time, limit int) ([]models.InboundForwardTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []models.InboundForwardTask
	for _, id := range s.order {
		t := s.rows[id]
		if t.Status == models.InboundForwardTaskPending && !t.NextAttemptAt.After(now) {
			out = append(out, *t)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *fwTaskStore) Claim(id uint) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.rows[id]
	if t == nil || t.Status != models.InboundForwardTaskPending {
		return false, nil
	}
	t.Status = models.InboundForwardTaskRunning
	t.Attempts++
	return true, nil
}

func (s *fwTaskStore) MarkDone(task *models.InboundForwardTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.rows[task.ID]
	if t.Status != models.InboundForwardTaskRunning {
		return errors.New("not running")
	}
	t.Status = models.InboundForwardTaskDone
	t.Hops = task.Hops
	t.LastError = ""
	return nil
}

func (s *fwTaskStore) MarkRetry(task *models.InboundForwardTask, next time.Time, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.rows[task.ID]
	if t.Status != models.InboundForwardTaskRunning {
		return errors.New("not running")
	}
	t.Status = models.InboundForwardTaskPending
	t.Hops = task.Hops
	t.NextAttemptAt = next
	t.LastError = lastErr
	return nil
}

func (s *fwTaskStore) MarkFailed(task *models.InboundForwardTask, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.rows[task.ID]
	if t.Status != models.InboundForwardTaskRunning {
		return errors.New("not running")
	}
	t.Status = models.InboundForwardTaskFailed
	t.Hops = task.Hops
	t.LastError = lastErr
	return nil
}

func (s *fwTaskStore) RequeueStaleRunning(staleFor time.Duration, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, id := range s.order {
		t := s.rows[id]
		if t.Status == models.InboundForwardTaskRunning {
			t.Status = models.InboundForwardTaskPending
			t.NextAttemptAt = now
			n++
		}
	}
	s.requeued = int(n)
	return n, nil
}

func (s *fwTaskStore) get(id uint) models.InboundForwardTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.rows[id]
}

type fwInbounds struct {
	mu      sync.Mutex
	rows    map[uint]*models.InboundEmail
	updates []uint
}

func newFwInbounds(recs ...*models.InboundEmail) *fwInbounds {
	s := &fwInbounds{rows: map[uint]*models.InboundEmail{}}
	for _, r := range recs {
		cp := *r
		s.rows[r.ID] = &cp
	}
	return s
}

func (s *fwInbounds) FindByID(id uint) (*models.InboundEmail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rows[id]
	if r == nil {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *r
	return &cp, nil
}

func (s *fwInbounds) Update(rec *models.InboundEmail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows[rec.ID] == nil {
		return gorm.ErrRecordNotFound
	}
	*s.rows[rec.ID] = *rec
	s.updates = append(s.updates, rec.ID)
	return nil
}

type fwRaws struct{ byUUID map[string][]byte }

func (s fwRaws) LoadRaw(_ context.Context, rec *models.InboundEmail) ([]byte, error) {
	raw, ok := s.byUUID[rec.UUID]
	if !ok {
		return nil, errors.New("raw unavailable")
	}
	return raw, nil
}

type fwDomains struct{ byID map[uint]*models.Domain }

func (s fwDomains) FindByID(id uint) (*models.Domain, error) {
	d := s.byID[id]
	if d == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return d, nil
}

type fwMailer struct {
	mu       sync.Mutex
	requests []*ForwardSendRequest
	failFor  int // fail the first N calls with a transient error
	resp     *ForwardSendResponse
}

func (s *fwMailer) SendSystemForward(_ context.Context, req *ForwardSendRequest) (*ForwardSendResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	if s.failFor > 0 {
		s.failFor--
		return nil, errors.New("transient mailer outage")
	}
	if s.resp == nil {
		s.resp = &ForwardSendResponse{EmailUUID: "fwd-email-uuid", Status: models.EmailStatusQueued}
	}
	return s.resp, nil
}

func (s *fwMailer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

type fwTargetStore struct {
	mu    sync.Mutex
	sheet map[string]bool // "scope|target" -> confirmed
	err   error
}

func (s *fwTargetStore) FindConfirmed(_ repositories.ResourceScope, target string) (*models.InboundForwardTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	// The fake keys on target only; scope isolation is the real repo's
	// ApplyScope concern and is pinned by the repositories' own tests.
	if s.sheet[target] {
		return &models.InboundForwardTarget{Target: target}, nil
	}
	return nil, gorm.ErrRecordNotFound
}

type fwLimiter struct {
	mu      sync.Mutex
	refuse  bool
	allowed []string
}

func (s *fwLimiter) AllowTarget(_ context.Context, _ repositories.ResourceScope, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse {
		return errors.New("hourly rate limit exceeded (61/60)")
	}
	s.allowed = append(s.allowed, target)
	return nil
}

// fwTargetRepo is the map-based stand-in for the forward-target repository
// (single-use confirmation + revoke ownership live here).
type fwTargetRepo struct {
	mu   sync.Mutex
	rows map[uint]*models.InboundForwardTarget
	seq  uint
}

func (s *fwTargetRepo) Create(t *models.InboundForwardTarget) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	t.ID = s.seq
	cp := *t
	s.rows[s.seq] = &cp
	return nil
}

func (s *fwTargetRepo) Delete(id uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, id)
	return nil
}

func (s *fwTargetRepo) FindByID(id uint) (*models.InboundForwardTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.rows[id]
	if t == nil {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *t
	return &cp, nil
}

func (s *fwTargetRepo) FindByTokenHash(hash string) (*models.InboundForwardTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.rows {
		if t.TokenHash == hash {
			cp := *t
			return &cp, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (s *fwTargetRepo) FindConfirmed(scope repositories.ResourceScope, target string) (*models.InboundForwardTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Mirrors the real store: ApplyScope matches nothing without a workspace.
	if scope.WorkspaceID == nil {
		return nil, gorm.ErrRecordNotFound
	}
	for _, t := range s.rows {
		if t.Target != target || t.ConfirmedAt == nil || t.WorkspaceID == nil {
			continue
		}
		if *t.WorkspaceID == *scope.WorkspaceID {
			cp := *t
			return &cp, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (s *fwTargetRepo) FindByScope(scope repositories.ResourceScope, limit, offset int) ([]models.InboundForwardTarget, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []models.InboundForwardTarget
	for _, t := range s.rows {
		if t.UserID == scope.UserID {
			out = append(out, *t)
		}
	}
	return out, int64(len(out)), nil
}

func (s *fwTargetRepo) Confirm(id uint, tokenHash string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.rows[id]
	if t == nil || t.TokenHash != tokenHash {
		return false, nil
	}
	t.ConfirmedAt = &at
	t.TokenHash = ""
	return true, nil
}

// ---------- fixtures ----------

func fwDomain() *models.Domain {
	return &models.Domain{ID: 7, UserID: 11, Domain: "lists.example"}
}

func fwInbound() *models.InboundEmail {
	return &models.InboundEmail{
		ID:          42,
		UUID:        "inb-fw-test",
		UserID:      11,
		WorkspaceID: nil,
		DomainID:    7,
		MessageID:   "orig-1@example.com",
		Sender:      "someone@example.org",
		Recipients:  []string{"inbox@lists.example"},
		Subject:     "Hello there",
		TextBody:    "plain body",
		HTMLBody:    "<p>html body</p>",
		Status:      models.InboundStatusReceived,
		ReceivedAt:  time.Now().UTC().Add(-time.Hour),
	}
}

func fwTask(inboundID uint, target string) *models.InboundForwardTask {
	ws := uint(3)
	return &models.InboundForwardTask{
		ID:             5,
		UserID:         11,
		WorkspaceID:    &ws,
		DomainID:       7,
		InboundEmailID: inboundID,
		RouteID:        3,
		Target:         target,
		Status:         models.InboundForwardTaskPending,
		NextAttemptAt:  time.Now().UTC().Add(-time.Minute),
	}
}

// fwScope is the workspace scope every fixture shares (matches fwTask's).
func fwScope() repositories.ResourceScope {
	ws := uint(3)
	return repositories.ResourceScope{UserID: 11, WorkspaceID: &ws}
}

type fwDeps struct {
	store    *fwTaskStore
	inbounds *fwInbounds
	raws     fwRaws
	domains  fwDomains
	mailer   *fwMailer
	targets  *fwTargetStore
	limiter  *fwLimiter
	fwd      *Forwarder
}

func newFwDeps(t *testing.T, rec *models.InboundEmail, task *models.InboundForwardTask) *fwDeps {
	t.Helper()
	d := &fwDeps{
		store:    newFwTaskStore(task),
		inbounds: newFwInbounds(rec),
		raws:     fwRaws{byUUID: map[string][]byte{}},
		domains:  fwDomains{byID: map[uint]*models.Domain{7: fwDomain()}},
		mailer:   &fwMailer{},
		targets:  &fwTargetStore{sheet: map[string]bool{}},
		limiter:  &fwLimiter{},
	}
	d.fwd = NewForwarder(d.store, d.inbounds, d.raws, d.domains, d.mailer)
	d.fwd.SetTargetStore(d.targets)
	d.fwd.SetTargetLimiter(d.limiter)
	d.fwd.SetJitter(func() time.Duration { return 0 })
	return d
}

// ---------- the named behaviors ----------

// TestForwarderHappyPathCreatesSystemForward: a claimed due task re-sends the
// durable original as a system_forward copy — From is the receiving address
// on the verified domain, the target is the only recipient, the subject is
// prefixed, the loop-evidence and threading headers are stamped, and the
// forward metadata travels to the email_events recorder.
func TestForwarderHappyPathCreatesSystemForward(t *testing.T) {
	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true

	n, err := d.fwd.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("processed %d tasks, want 1", n)
	}
	if d.mailer.calls() != 1 {
		t.Fatalf("mailer called %d times, want 1", d.mailer.calls())
	}
	req := d.mailer.requests[0]
	if req.From != "inbox@lists.example" {
		t.Errorf("From = %q, want the receiving address on the verified domain", req.From)
	}
	if len(req.To) != 1 || req.To[0] != "friend@external.test" {
		t.Errorf("To = %v, want [friend@external.test]", req.To)
	}
	if req.Subject != "Fwd: Hello there" {
		t.Errorf("Subject = %q, want prefixed forward subject", req.Subject)
	}
	if req.Text != rec.TextBody || req.HTML != rec.HTMLBody {
		t.Errorf("copy bodies do not match the original (text %q, html %q)", req.Text, req.HTML)
	}
	if got := req.Headers["X-Posta-Forwarded-For"]; got != "friend@external.test" {
		t.Errorf("X-Posta-Forwarded-For = %q, want the first forward target", got)
	}
	if got := req.Headers["X-Posta-Forwarded-From"]; got != rec.Sender {
		t.Errorf("X-Posta-Forwarded-From = %q, want %q", got, rec.Sender)
	}
	if got := req.Headers["Reply-To"]; got != rec.Sender {
		t.Errorf("Reply-To = %q, want the original sender", got)
	}
	if got := req.Headers["In-Reply-To"]; got != "<orig-1@example.com>" {
		t.Errorf("In-Reply-To = %q, want the original Message-ID", got)
	}
	if !strings.Contains(req.Headers["Message-ID"], "<forward-42-5@lists.example>") {
		t.Errorf("Message-ID = %q, want the deterministic forward id", req.Headers["Message-ID"])
	}
	if req.Metadata["hops"] != 1 {
		t.Errorf("metadata hops = %v, want 1", req.Metadata["hops"])
	}
	if req.Metadata["inbound_id"] != rec.UUID {
		t.Errorf("metadata inbound_id = %v, want %q", req.Metadata["inbound_id"], rec.UUID)
	}

	done := d.store.get(task.ID)
	if done.Status != models.InboundForwardTaskDone {
		t.Errorf("task status = %q, want done", done.Status)
	}
	if done.Hops != 1 {
		t.Errorf("task hops = %d, want 1", done.Hops)
	}
	if done.LastError != "" {
		t.Errorf("task last_error = %q, want empty", done.LastError)
	}
	if len(d.limiter.allowed) != 1 || d.limiter.allowed[0] != "friend@external.test" {
		t.Errorf("rate limiter saw %v, want one forward to the target", d.limiter.allowed)
	}
}

// TestForwarderMarksOriginalForwardedAndDurable: on success the ORIGINAL
// inbound row flips to forwarded with ForwardedAt, and its content is
// untouched; the row itself is never deleted.
func TestForwarderMarksOriginalForwardedAndDurable(t *testing.T) {
	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got := d.inbounds.rows[rec.ID]
	if got.Status != models.InboundStatusForwarded {
		t.Errorf("original status = %q, want forwarded", got.Status)
	}
	if got.ForwardedAt == nil {
		t.Error("original ForwardedAt not set")
	}
	// Durability: content intact.
	if got.Subject != "Hello there" || got.TextBody != "plain body" || got.HTMLBody != "<p>html body</p>" {
		t.Errorf("original content was rewritten: %+v", got)
	}
	if got.Sender != "someone@example.org" {
		t.Errorf("original sender rewritten: %q", got.Sender)
	}
}

// TestForwarderOriginalStaysDurableOnFailure: a task that cannot forward
// (mailer outage) leaves the original exactly as ingested — status received,
// no ForwardedAt, content intact — while the task goes back to the queue
// with the standard backoff.
func TestForwarderOriginalStaysDurableOnFailure(t *testing.T) {
	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true
	d.mailer.failFor = 1

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got := d.inbounds.rows[rec.ID]
	if got.Status != models.InboundStatusReceived {
		t.Errorf("original status = %q, want received (untouched on failure)", got.Status)
	}
	if got.ForwardedAt != nil {
		t.Error("original ForwardedAt set despite failure")
	}
	if got.Subject != "Hello there" || got.TextBody != "plain body" {
		t.Errorf("original content changed on failure: %+v", got)
	}
	state := d.store.get(task.ID)
	if state.Status != models.InboundForwardTaskPending {
		t.Errorf("task status = %q, want pending (requeued)", state.Status)
	}
	if state.Attempts != 1 {
		t.Errorf("task attempts = %d, want 1", state.Attempts)
	}
	if state.LastError == "" || !strings.Contains(state.LastError, "transient mailer outage") {
		t.Errorf("task last_error = %q, want the transient failure", state.LastError)
	}
	if !state.NextAttemptAt.After(time.Now()) {
		t.Errorf("next_attempt_at = %v, want a future backoff slot", state.NextAttemptAt)
	}

	// Second sweep: the mailer recovers, the task completes (the fake store
	// has no clock — make the backed-off task due again).
	d.store.mu.Lock()
	d.store.rows[task.ID].NextAttemptAt = time.Now().UTC().Add(-time.Minute)
	d.store.mu.Unlock()
	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	state = d.store.get(task.ID)
	if state.Status != models.InboundForwardTaskDone {
		t.Errorf("task status after recovery = %q, want done", state.Status)
	}
	if d.inbounds.rows[rec.ID].Status != models.InboundStatusForwarded {
		t.Errorf("original status after recovery = %q, want forwarded", d.inbounds.rows[rec.ID].Status)
	}
}

// TestForwarderRejectsBeyondMaxForwardHops: a message that already carries
// MaxForwardHops forwards is refused — the task fails permanently, nothing
// is re-sent, and the original stays durable. Also pins the boundary: a
// chain of exactly MaxForwardHops-1 still forwards (hop == MaxForwardHops).
func TestForwarderRejectsBeyondMaxForwardHops(t *testing.T) {
	// A chain of MaxForwardHops entries means this forward would be hop
	// MaxForwardHops+1 — one beyond the ceiling.
	chain := strings.Repeat("prior@hop.test, ", MaxForwardHops-1) + "last@hop.test"
	raw := []byte("From: someone@example.org\r\nTo: inbox@lists.example\r\nSubject: loop\r\nMessage-ID: <loop-1@example.org>\r\n" +
		HeaderForwardedFor + ": " + chain + "\r\n\r\nbody\r\n")
	rec := fwInbound()
	rec.RawContent = raw
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true
	d.raws.byUUID[rec.UUID] = raw

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if calls := d.mailer.calls(); calls != 0 {
		t.Fatalf("mailer called %d times, want 0 (hop limit)", calls)
	}
	state := d.store.get(task.ID)
	if state.Status != models.InboundForwardTaskFailed {
		t.Errorf("task status = %q, want failed", state.Status)
	}
	if state.Hops != MaxForwardHops+1 {
		t.Errorf("task hops = %d, want %d", state.Hops, MaxForwardHops+1)
	}
	if !strings.Contains(state.LastError, "forward hop limit exceeded") {
		t.Errorf("task last_error = %q, want the hop-limit reason", state.LastError)
	}
	if got := d.inbounds.rows[rec.ID]; got.Status != models.InboundStatusReceived || got.ForwardedAt != nil {
		t.Errorf("original disturbed on hop-limit rejection: status %q forwardedAt %v", got.Status, got.ForwardedAt)
	}
}

// TestForwarderAllowsForwardAtMaxHopsBoundary pins the boundary of the hop
// limit: a chain of MaxForwardHops-1 entries is one less than the ceiling,
// so the forward runs (Repeat(n) adds a trailing separator, so the entry
// count is MaxForwardHops-1 via Repeat(MaxForwardHops-2) + the edge target).
func TestForwarderAllowsForwardAtMaxHopsBoundary(t *testing.T) {
	chain := strings.Repeat("prior@hop.test, ", MaxForwardHops-2) + "edge@hop.test"
	raw := []byte("From: someone@example.org\r\nTo: inbox@lists.example\r\nSubject: edge\r\n" +
		HeaderForwardedFor + ": " + chain + "\r\n\r\nbody\r\n")
	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true
	d.raws.byUUID[rec.UUID] = raw

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	state := d.store.get(task.ID)
	if state.Status != models.InboundForwardTaskDone {
		t.Fatalf("task status = %q (last_error %q), want done at the boundary", state.Status, state.LastError)
	}
	if state.Hops != MaxForwardHops {
		t.Errorf("task hops = %d, want %d", state.Hops, MaxForwardHops)
	}
	if d.mailer.calls() != 1 {
		t.Fatalf("mailer called %d times, want 1", d.mailer.calls())
	}
	wantChain := chain + ", friend@external.test"
	if got := d.mailer.requests[0].Headers["X-Posta-Forwarded-For"]; got != wantChain {
		t.Errorf("X-Posta-Forwarded-For = %q, want %q", got, wantChain)
	}
}

// TestForwarderRefusesUnconfirmedTarget: a rule's forward action never runs
// against an unconfirmed destination — the task is requeued (the operator may
// still confirm), nothing is re-sent, the original stays untouched.
func TestForwarderRefusesUnconfirmedTarget(t *testing.T) {
	rec := fwInbound()
	task := fwTask(rec.ID, "stranger@external.test")
	d := newFwDeps(t, rec, task)
	// d.targets.sheet deliberately empty.

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if calls := d.mailer.calls(); calls != 0 {
		t.Fatalf("mailer called %d times, want 0 (unconfirmed target)", calls)
	}
	state := d.store.get(task.ID)
	if state.Status != models.InboundForwardTaskPending {
		t.Errorf("task status = %q, want pending (waiting for confirmation)", state.Status)
	}
	if state.LastError != "forward target is not confirmed" {
		t.Errorf("task last_error = %q, want the unconfirmed refusal", state.LastError)
	}
	if got := d.inbounds.rows[rec.ID]; got.Status != models.InboundStatusReceived {
		t.Errorf("original status = %q, want received", got.Status)
	}

	// Once the confirmation lands, the same task forwards (the fake store has
	// no clock — make the backed-off task due again).
	d.store.mu.Lock()
	d.store.rows[task.ID].NextAttemptAt = time.Now().UTC().Add(-time.Minute)
	d.store.mu.Unlock()
	d.targets.sheet["stranger@external.test"] = true
	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if state := d.store.get(task.ID); state.Status != models.InboundForwardTaskDone {
		t.Errorf("task status after confirmation = %q, want done", state.Status)
	}
}

// TestForwarderPerTargetRateCapRetries: over the per-target cap the forward
// waits — no copy is created, the task is requeued with the cap reason.
func TestForwarderPerTargetRateCapRetries(t *testing.T) {
	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true
	d.limiter.refuse = true

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if calls := d.mailer.calls(); calls != 0 {
		t.Fatalf("mailer called %d times, want 0 (rate capped)", calls)
	}
	state := d.store.get(task.ID)
	if state.Status != models.InboundForwardTaskPending {
		t.Errorf("task status = %q, want pending", state.Status)
	}
	if !strings.Contains(state.LastError, "rate limit") {
		t.Errorf("task last_error = %q, want the rate-cap reason", state.LastError)
	}
	if got := d.inbounds.rows[rec.ID]; got.Status != models.InboundStatusReceived {
		t.Errorf("original status = %q, want received", got.Status)
	}
}

// TestForwarderRetryBudgetExhaustedMarksFailed: past the attempt budget the
// executor finalizes the task as failed instead of retrying forever.
func TestForwarderRetryBudgetExhaustedMarksFailed(t *testing.T) {
	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true
	d.mailer.failFor = 100 // every attempt fails transiently

	for i := 0; i < MaxForwardTaskAttempts+1; i++ {
		// The fake store has no real clock; force the task due each sweep.
		d.store.mu.Lock()
		d.store.rows[task.ID].NextAttemptAt = time.Now().UTC().Add(-time.Minute)
		d.store.mu.Unlock()
		if _, err := d.fwd.RunOnce(context.Background()); err != nil {
			t.Fatalf("sweep %d: %v", i+1, err)
		}
	}
	state := d.store.get(task.ID)
	if state.Status != models.InboundForwardTaskFailed {
		t.Errorf("task status = %q after %d sweeps, want failed", state.Status, MaxForwardTaskAttempts+1)
	}
	if !strings.Contains(state.LastError, "retry budget exhausted") {
		t.Errorf("task last_error = %q, want the budget reason", state.LastError)
	}
	// The budget is attempts, not mailer calls beyond it.
	if calls := d.mailer.calls(); calls > MaxForwardTaskAttempts {
		t.Errorf("mailer called %d times, want <= %d", calls, MaxForwardTaskAttempts)
	}
}

// TestForwarderHopLimitNeverRetries: the hop-limit rejection is permanent —
// a second sweep does not resurrect the task.
func TestForwarderHopLimitNeverRetries(t *testing.T) {
	raw := []byte("From: a@b.c\r\nTo: inbox@lists.example\r\n" +
		HeaderForwardedFor + ": one@hop, two@hop, three@hop, four@hop\r\n\r\nbody\r\n")
	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true
	d.raws.byUUID[rec.UUID] = raw

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	// The stale-requeue sweep inside RunOnce must NOT resurrect a failed task.
	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if state := d.store.get(task.ID); state.Status != models.InboundForwardTaskFailed {
		t.Errorf("task status = %q, want failed to stick", state.Status)
	}
	if calls := d.mailer.calls(); calls != 0 {
		t.Errorf("mailer called %d times across sweeps, want 0", calls)
	}
}

// TestForwardTargetConfirmTokenSingleUse: the confirmation token works
// exactly once — the first click confirms the target, a replay (or any other
// token) finds nothing; the hash is cleared so the store holds no live token.
func TestForwardTargetConfirmTokenSingleUse(t *testing.T) {
	repo := &fwTargetRepo{rows: map[uint]*models.InboundForwardTarget{}}
	svc := NewForwardTargetService(repo)
	svc.SetRandom(func() (string, error) { return "fixed-test-token", nil })

	created, err := svc.Create(fwScope(), "Friend@External.test", "https://api.example.test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.Contains(created.ConfirmURL, "/api/v1/inbound/forward-confirm/fixed-test-token") {
		t.Fatalf("ConfirmURL = %q, want the plain-text public URL with the raw token", created.ConfirmURL)
	}
	if created.Target.Target != "friend@external.test" {
		t.Errorf("stored target = %q, want lowercased", created.Target.Target)
	}
	if created.Target.TokenHash == "" {
		t.Error("TokenHash not persisted at creation")
	}
	if created.Target.IsConfirmed() {
		t.Error("target created confirmed")
	}

	confirmed, target, err := svc.Confirm("fixed-test-token")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !confirmed || target != "friend@external.test" {
		t.Fatalf("first confirm = (%v, %q), want confirmed with the address", confirmed, target)
	}

	// Single use: the replay finds no outstanding token.
	confirmed, _, err = svc.Confirm("fixed-test-token")
	if err != nil {
		t.Fatalf("second Confirm: %v", err)
	}
	if confirmed {
		t.Error("token confirmed twice — single-use violated")
	}
	// Unknown token: refused, not an error.
	if confirmed, _, err := svc.Confirm("never-issued"); err != nil || confirmed {
		t.Errorf("unknown token confirm = (%v, %v), want (false, nil)", confirmed, err)
	}

	// The store keeps no live token after confirmation.
	row, err := repo.FindByID(created.Target.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.TokenHash != "" {
		t.Errorf("TokenHash after confirmation = %q, want cleared", row.TokenHash)
	}
	if !row.IsConfirmed() {
		t.Error("target not confirmed in store")
	}
}

// TestForwarderExecutorSeesConfirmedTarget: end-to-end across the two
// services — an executor with the real confirmation flow wired refuses an
// unconfirmed target, forwards after Create+Confirm, and still refuses after
// Revoke (the confirmation is revocable).
func TestForwarderExecutorSeesConfirmedTarget(t *testing.T) {
	repo := &fwTargetRepo{rows: map[uint]*models.InboundForwardTarget{}}
	svc := NewForwardTargetService(repo)
	svc.SetRandom(func() (string, error) { return "tok-1", nil })
	scope := fwScope()

	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	// Wire the REAL confirmation store over the fake repo.
	d.fwd.SetTargetStore(repo)

	// Unconfirmed: refused.
	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if d.mailer.calls() != 0 {
		t.Fatalf("mailer called %d times before confirmation, want 0", d.mailer.calls())
	}

	created, err := svc.Create(scope, "friend@external.test", "https://api.example.test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if confirmed, _, err := svc.Confirm(created.ConfirmURL[strings.LastIndex(created.ConfirmURL, "/")+1:]); err != nil || !confirmed {
		t.Fatalf("Confirm: (%v, %v)", confirmed, err)
	}

	// Make the task due again (the fake store has no clock).
	d.store.mu.Lock()
	d.store.rows[task.ID].NextAttemptAt = time.Now().UTC().Add(-time.Minute)
	d.store.mu.Unlock()
	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce after confirm: %v", err)
	}
	if state := d.store.get(task.ID); state.Status != models.InboundForwardTaskDone {
		t.Fatalf("task status after confirmation = %q (%q), want done", state.Status, state.LastError)
	}

	// Revoke: a NEW task for the same target is refused again.
	if _, err := svc.RevokeOwned(scope, created.Target.ID); err != nil {
		t.Fatalf("RevokeOwned: %v", err)
	}
	task2 := fwTask(rec.ID, "friend@external.test")
	task2.ID = 6
	d.store.mu.Lock()
	d.store.rows[task2.ID] = task2
	d.store.order = append(d.store.order, task2.ID)
	d.store.mu.Unlock()
	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce after revoke: %v", err)
	}
	if state := d.store.get(task2.ID); state.Status != models.InboundForwardTaskPending {
		t.Errorf("task status after revoke = %q (%q), want pending (refused)", state.Status, state.LastError)
	}
}

// TestForwardTargetRevokeRequiresOwnership: a foreign scope cannot revoke a
// target — it sees a not-found error, never the row.
func TestForwardTargetRevokeRequiresOwnership(t *testing.T) {
	repo := &fwTargetRepo{rows: map[uint]*models.InboundForwardTarget{}}
	svc := NewForwardTargetService(repo)
	svc.SetRandom(func() (string, error) { return "tok-2", nil })

	created, err := svc.Create(fwScope(), "friend@external.test", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Personal mode (no workspace) cannot even create.
	if _, err := svc.Create(repositories.ResourceScope{UserID: 11}, "other@external.test", ""); !errors.Is(err, ErrForwardTargetNeedsWorkspace) {
		t.Errorf("nil-workspace create err = %v, want ErrForwardTargetNeedsWorkspace", err)
	}

	// A different workspace is a foreign scope for revoke.
	foreignWS := uint(9)
	foreign := repositories.ResourceScope{UserID: 11, WorkspaceID: &foreignWS}
	if _, err := svc.RevokeOwned(foreign, created.Target.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("foreign revoke err = %v, want ErrRecordNotFound", err)
	}
	if _, err := repo.FindByID(created.Target.ID); err != nil {
		t.Fatalf("target deleted by foreign scope: %v", err)
	}

	if _, err := svc.RevokeOwned(fwScope(), created.Target.ID); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	if _, err := repo.FindByID(created.Target.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("target still present after owner revoke: %v", err)
	}
}

// TestForwarderMalformedTargetFailsPermanently: a target address that cannot
// parse never reaches the mailer and is never retried.
func TestForwarderMalformedTargetFailsPermanently(t *testing.T) {
	rec := fwInbound()
	task := fwTask(rec.ID, "not-an-address")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["not-an-address"] = true

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if d.mailer.calls() != 0 {
		t.Errorf("mailer called %d times, want 0", d.mailer.calls())
	}
	if state := d.store.get(task.ID); state.Status != models.InboundForwardTaskFailed {
		t.Errorf("task status = %q, want failed", state.Status)
	}
}

// TestForwardRetryDelayStandardBackoff pins the standard backoff shape:
// base·2^(attempt-1) with a hard cap (R-06 shape, minute scale for a
// durable queue).
func TestForwardRetryDelayStandardBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
	}
	for _, tc := range cases {
		if got := forwardRetryDelay(tc.attempt, nil); got != tc.want {
			t.Errorf("forwardRetryDelay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
	if got := forwardRetryDelay(30, nil); got != forwardRetryCap {
		t.Errorf("forwardRetryDelay(30) = %v, want the %v cap", got, forwardRetryCap)
	}
}

// TestForwardHopsSpoofedHeaderFailsClosed: a client-supplied chain can only
// RAISE the hop count (fail-closed) — a spoofed deep chain refuses the
// forward even though this message was never forwarded by us.
func TestForwardHopsSpoofedHeaderFailsClosed(t *testing.T) {
	raw := []byte("From: evil@attacker.test\r\nTo: inbox@lists.example\r\n" +
		HeaderForwardedFor + ": a@x, b@x, c@x\r\n\r\nbody\r\n")
	if got := parseForwardChain(raw); len(got) != 3 {
		t.Fatalf("parseForwardChain = %v, want 3 entries", got)
	}
	rec := fwInbound()
	task := fwTask(rec.ID, "friend@external.test")
	d := newFwDeps(t, rec, task)
	d.targets.sheet["friend@external.test"] = true
	d.raws.byUUID[rec.UUID] = raw

	if _, err := d.fwd.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if state := d.store.get(task.ID); state.Status != models.InboundForwardTaskFailed {
		t.Errorf("task status = %q, want failed (spoofed deep chain)", state.Status)
	}
	if d.mailer.calls() != 0 {
		t.Errorf("mailer called %d times, want 0", d.mailer.calls())
	}
}
