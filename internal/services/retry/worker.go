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

package retry

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/queue"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
)

// Worker periodically retries failed emails that haven't exceeded their
// SMTP server's max_retries limit.
type Worker struct {
	emailRepo  *repositories.EmailRepository
	smtpRepo   *repositories.SMTPRepository
	sender     *email.SMTPSender
	dispatcher *webhook.Dispatcher
	enqueuer   email.EmailEnqueuer
	interval   time.Duration
	onSent     func()
	onFailed   func()
	onRetry    func()
	// batchLimit caps how many failed emails one sweep tick loads per SMTP
	// server (R-27): a large failed backlog re-enqueues in bounded batches
	// across ticks instead of all at once. SetBatchLimit overrides it (tests).
	batchLimit int
	// jitter returns the random pre-sweep delay that desynchronizes the retry
	// sweeps of multiple processes started at the same moment (R-27).
	jitter func() time.Duration
	// scheduler is the SF-08 per-recipient schedule seam (services/queue.
	// Manager): before claiming a failed email the sweep consults the
	// recipients' retry schedules — rows not yet due stay failed until their
	// interval elapses, and exhausted schedules never re-enter. Nil (the
	// default) keeps the pre-SF-08 behavior: every bounded-backlog candidate
	// re-enqueues immediately.
	scheduler RetryScheduler
	stopOnce  sync.Once
	stop      chan struct{}
}

// RetryScheduler is the SF-08 schedule consult (services/queue.Manager
// satisfies it). The decision constants mirror queue.DecideRetry's contract.
type RetryScheduler interface {
	DecideRetry(emailID uint, now time.Time) (queue.ScheduleDecision, time.Time)
}

// DefaultBatchLimit is the per-server, per-tick cap on re-enqueued retries.
const DefaultBatchLimit = 200

func NewWorker(
	emailRepo *repositories.EmailRepository,
	smtpRepo *repositories.SMTPRepository,
	dispatcher *webhook.Dispatcher,
	interval time.Duration,
) *Worker {
	return &Worker{
		emailRepo:  emailRepo,
		smtpRepo:   smtpRepo,
		sender:     email.NewSMTPSender(),
		dispatcher: dispatcher,
		interval:   interval,
		batchLimit: DefaultBatchLimit,
		jitter: func() time.Duration {
			return time.Duration(rand.Int63n(int64(interval / 10)))
		},
		stop: make(chan struct{}),
	}
}

// SetBatchLimit overrides the per-tick retry batch cap. 0 removes the cap
// (unbounded, the pre-R-27 behavior); negative values are ignored.
func (w *Worker) SetBatchLimit(limit int) {
	if limit >= 0 {
		w.batchLimit = limit
	}
}

// SetJitter overrides the pre-sweep jitter function (tests).
func (w *Worker) SetJitter(fn func() time.Duration) {
	if fn != nil {
		w.jitter = fn
	}
}

// SetScheduler wires the SF-08 per-recipient schedule consult. When set, the
// sweep skips emails whose retry schedules are not yet due or already
// exhausted; due and schedule-less emails behave exactly as before.
func (w *Worker) SetScheduler(s RetryScheduler) { w.scheduler = s }

// Jitter returns one sample of the configured pre-sweep jitter delay.
func (w *Worker) Jitter() time.Duration {
	if w.jitter == nil {
		return 0
	}
	return w.jitter()
}

// SetEnqueuer sets an enqueuer so retries are re-enqueued to the background worker
// instead of being sent synchronously.
func (w *Worker) SetEnqueuer(eq email.EmailEnqueuer) {
	w.enqueuer = eq
}

// OnSent sets a callback invoked after each successful retry.
func (w *Worker) OnSent(fn func()) { w.onSent = fn }

// OnFailed sets a callback invoked after each failed retry.
func (w *Worker) OnFailed(fn func()) { w.onFailed = fn }

// OnRetry sets a callback invoked for each retry attempt.
func (w *Worker) OnRetry(fn func()) { w.onRetry = fn }

// Start begins the retry loop in a background goroutine.
func (w *Worker) Start(ctx context.Context) {
	go w.run(ctx)
}

// Stop signals the worker to stop.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
}

func (w *Worker) run(ctx context.Context) {
	logger.Info("retry worker started", "interval", w.interval)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("retry worker stopped (context cancelled)")
			return
		case <-w.stop:
			logger.Info("retry worker stopped")
			return
		case <-ticker.C:
			// Jittered spread (R-27): stagger the sweep so processes started
			// together do not stampede the queue with identical re-enqueues.
			if d := w.jitter(); d > 0 {
				select {
				case <-ctx.Done():
					return
				case <-w.stop:
					return
				case <-time.After(d):
				}
			}
			w.ProcessRetries()
		}
	}
}

// ProcessRetries runs one sweep: it loads the retry-enabled SMTP servers and,
// per server, re-enqueues up to batchLimit failed emails. Exported so the
// R-27 bounded-batch test (internal/worker/retry_bound_test.go) can drive a
// single sweep without waiting on the ticker.
func (w *Worker) ProcessRetries() {
	// Get all SMTP servers that have retries enabled
	smtpServers, err := w.smtpRepo.FindAllWithRetries()
	if err != nil {
		logger.Error("retry worker: failed to load SMTP servers", "error", err)
		return
	}

	for _, server := range smtpServers {
		var emails []models.Email
		// Bounded batch (R-27): each tick claims at most batchLimit failed
		// emails per server, oldest first. Claimed rows leave the failed set,
		// so the next tick naturally advances through the backlog.
		if server.WorkspaceID != nil {
			emails, err = w.emailRepo.FindFailedForRetryByWorkspace(*server.WorkspaceID, server.MaxRetries, w.batchLimit)
		} else {
			emails, err = w.emailRepo.FindFailedForRetry(server.UserID, server.MaxRetries, w.batchLimit)
		}
		if err != nil {
			logger.Error("retry worker: failed to load failed emails", "user_id", server.UserID, "error", err)
			continue
		}

		for i := range emails {
			w.retryEmail(&emails[i], &server)
		}
	}
}

func (w *Worker) retryEmail(em *models.Email, server *models.SMTPServer) {
	// SF-08: consult the per-recipient retry schedule before claiming. A
	// schedule holding the next attempt in the future leaves the row failed
	// until its interval elapses; an exhausted schedule (interval list run
	// out, or every recipient terminal) never re-enters the queue. No rows or
	// a due schedule fall through to the unchanged claim below.
	if w.scheduler != nil {
		switch d, due := w.scheduler.DecideRetry(em.ID, time.Now()); d {
		case queue.DecisionWait:
			logger.Debug("retry worker: schedule holds the next attempt, skipping",
				"id", em.ID, "due", due.UTC().Format(time.RFC3339))
			return
		case queue.DecisionExhausted:
			logger.Info("retry worker: per-recipient retry schedules exhausted, skipping", "id", em.ID)
			return
		}
	}

	if w.onRetry != nil {
		w.onRetry()
	}

	// If an enqueuer is available, re-enqueue instead of sending synchronously.
	// The claim is a CAS (R-26): failed→queued happens in one UPDATE, so this
	// poller and a user retry (or a second poller) can never both claim the
	// same email and enqueue two tasks.
	if w.enqueuer != nil {
		claimed, err := w.emailRepo.ClaimForRetry(em.ID)
		if err != nil {
			logger.Error("retry worker: failed to claim email", "id", em.ID, "error", err)
			return
		}
		if !claimed {
			logger.Info("retry worker: email no longer failed (claimed elsewhere), skipping", "id", em.ID)
			return
		}
		em.Status = models.EmailStatusQueued
		em.RetryCount++
		em.ErrorMessage = ""
		if err := w.enqueuer.EnqueueEmailSend(em.ID, ""); err != nil {
			em.Status = models.EmailStatusFailed
			em.ErrorMessage = err.Error()
			_ = w.emailRepo.Update(em)
			logger.Error("retry worker: failed to re-enqueue email", "id", em.ID, "error", err)
		} else {
			logger.Info("retry worker: email re-enqueued", "id", em.ID, "attempt", em.RetryCount)
		}
		return
	}

	// Synchronous fallback. Claim failed→processing first so two pollers (or a
	// poller and a user retry racing the send) cannot both deliver; the claim
	// also stops FindFailedForRetry from re-listing the row mid-send.
	claimed, err := w.emailRepo.ClaimFailedForProcessing(em.ID)
	if err != nil {
		logger.Error("retry worker: failed to claim email", "id", em.ID, "error", err)
		return
	}
	if !claimed {
		logger.Info("retry worker: email no longer failed (claimed elsewhere), skipping", "id", em.ID)
		return
	}
	em.Status = models.EmailStatusProcessing
	em.RetryCount++

	if err := w.sender.Send(server, em.Sender, em.Recipients, em.Subject, em.HTMLBody, em.TextBody, nil, nil, em.ListUnsubscribeURL, em.ListUnsubscribeMailto, em.ListUnsubscribePost); err != nil {
		em.Status = models.EmailStatusFailed
		em.ErrorMessage = err.Error()
		_ = w.emailRepo.Update(em)
		logger.Debug("retry worker: email retry failed", "id", em.ID, "attempt", em.RetryCount, "error", err)
		if w.onFailed != nil {
			w.onFailed()
		}
		return
	}

	now := time.Now()
	em.Status = models.EmailStatusSent
	em.SentAt = &now
	em.ErrorMessage = ""
	_ = w.emailRepo.Update(em)
	w.dispatcher.Dispatch(em.UserID, em.WorkspaceID, "email.sent", em.UUID, em.Sender)
	logger.Info("retry worker: email sent successfully", "id", em.ID, "attempt", em.RetryCount)
	if w.onSent != nil {
		w.onSent()
	}
}
