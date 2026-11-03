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

package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// Engine tuning constants.
const (
	// DefaultInterval is the polling cadence of the in-process scheduler; it is
	// also the effective granularity of delay nodes (delays are poll-based, not
	// timer-based, so a paused automation's parked journeys survive restarts
	// for free).
	DefaultInterval = 10 * time.Second
	// DefaultBatchSize is how many due journeys one tick claims.
	DefaultBatchSize = 50
	// maxNodesPerTick bounds one journey's walk per execution: a crash-looping
	// or pathological workflow cannot run away inside a single task. The walk
	// re-parks the journey when it runs out (see ExecuteJourney).
	maxNodesPerTick = 10
	// maxRetries is the number of attempts a failing node gets before the
	// journey fails permanently. Backoff between attempts is 2^retryCount
	// minutes (2m, 4m).
	maxRetries = 3
	// sweepLimit caps one sweep's re-enqueues.
	sweepLimit = 500
	// DefaultSweepGrace is how overdue a due journey must be before the sweep
	// re-enqueues it, keeping the sweep out of the way of live executors
	// (a mid-execution journey has scheduled_at NULL and is excluded anyway).
	DefaultSweepGrace = 5 * time.Minute
	// sweepEveryTicks schedules a Sweep every N scheduler ticks (N × 10s = 5m).
	sweepEveryTicks = 30
)

// AutomationRunProducer enqueues journey executions. *worker.Producer
// implements it; the interface keeps the automation package from importing the
// worker (and keeps the dependency one-way).
type AutomationRunProducer interface {
	EnqueueAutomationRun(journeyID uint, delay time.Duration) error
}

// Engine drives automation journeys: it polls for due journeys, executes them
// node by node under compare-and-swap, parks them on delay nodes, retries
// failed nodes with exponential backoff and sweeps up journeys whose execution
// nudge was lost. It also owns enrollment (triggers.go) and the node
// executors (nodes.go).
type Engine struct {
	db *gorm.DB
	// producer nudges executions; never nil after NewEngine.
	producer AutomationRunProducer
	// emailSender sends email-node mail; may be nil, in which case email nodes
	// fail with a clear error (dev setups without the send pipeline).
	emailSender EmailSender

	lists          *repositories.SubscriberListRepository
	subscriberRepo *repositories.SubscriberRepository
	automationRepo *repositories.AutomationRepository
	journeyRepo    *repositories.AutomationJourneyRepository
	runRepo        *repositories.AutomationRunRepository
	triggerLogRepo *repositories.AutomationTriggerLogRepository

	webhook *WebhookClient

	now func() time.Time
	// interval is the scheduler tick; Start uses it.
	interval   time.Duration
	sweepGrace time.Duration
}

// NewEngine constructs the engine over the shared database handle. The
// repositories are built from db so call sites (worker.go, server.go, tests)
// stay small; the clock defaults to time.Now and the tick to DefaultInterval —
// SetClock / SetInterval override both for tests.
func NewEngine(db *gorm.DB, producer AutomationRunProducer, emailSender EmailSender, lists *repositories.SubscriberListRepository, subscriberRepo *repositories.SubscriberRepository) *Engine {
	return &Engine{
		db:             db,
		producer:       producer,
		emailSender:    emailSender,
		lists:          lists,
		subscriberRepo: subscriberRepo,
		automationRepo: repositories.NewAutomationRepository(db),
		journeyRepo:    repositories.NewAutomationJourneyRepository(db),
		runRepo:        repositories.NewAutomationRunRepository(db),
		triggerLogRepo: repositories.NewAutomationTriggerLogRepository(db),
		webhook:        NewWebhookClient(),
		now:            time.Now,
		interval:       DefaultInterval,
		sweepGrace:     DefaultSweepGrace,
	}
}

// SetClock overrides the engine clock (tests).
func (e *Engine) SetClock(now func() time.Time) {
	if now != nil {
		e.now = now
	}
}

// SetInterval overrides the scheduler tick duration (tests).
func (e *Engine) SetInterval(d time.Duration) {
	if d > 0 {
		e.interval = d
	}
}

// SetSweepGrace overrides how overdue a journey must be before the sweep
// re-enqueues it.
func (e *Engine) SetSweepGrace(d time.Duration) {
	if d > 0 {
		e.sweepGrace = d
	}
}

// SetWebhookClient overrides the webhook node HTTP client (tests).
func (e *Engine) SetWebhookClient(c *WebhookClient) {
	if c != nil {
		e.webhook = c
	}
}

// Start runs the scheduler loop in a background goroutine: every interval it
// claims and executes a batch of due journeys, and every sweepEveryTicks ticks
// it sweeps for overdue journeys. It returns immediately; the loop stops when
// ctx is done. Running the scheduler in multiple processes is safe — the claim
// query uses FOR UPDATE SKIP LOCKED and every cursor write is a CAS.
func (e *Engine) Start(ctx context.Context) {
	go e.loop(ctx)
}

func (e *Engine) loop(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := e.ProcessBatch(ctx, DefaultBatchSize); err != nil {
			logger.Error("automation: batch processing failed", "error", err)
		}
		ticks++
		if ticks >= sweepEveryTicks {
			ticks = 0
			if err := e.Sweep(ctx); err != nil {
				logger.Error("automation: sweep failed", "error", err)
			}
		}
	}
}

// ProcessBatch claims up to limit due journeys (live automations only) and
// executes each. A journey's execution error is logged and the batch carries
// on; the joined error is returned for the caller's logging.
func (e *Engine) ProcessBatch(ctx context.Context, limit int) error {
	journeys, err := e.journeyRepo.ClaimDueBatchAt(limit, e.now())
	if err != nil {
		return fmt.Errorf("automation: claim due batch: %w", err)
	}
	var errs []error
	for i := range journeys {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.ExecuteJourney(ctx, journeys[i].ID); err != nil {
			logger.Error("automation: journey execution failed", "journey_id", journeys[i].ID, "error", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ExecuteJourney advances one journey by up to maxNodesPerTick nodes.
//
// Per node: a run row is written (entered), the node executes, the cursor is
// advanced under CAS (only while the journey is still active — a concurrent
// terminal transition aborts the walk), and the run row is completed with the
// node's output. A delay node CASes the cursor past itself together with the
// future scheduled_at and stops the tick (parked — the scheduler picks the
// journey up when the delay elapses). An empty successor completes the journey.
// A node error fails the run row and schedules a retry with exponential
// backoff, or fails the journey once the retry budget is spent.
//
// Returns nil for every per-journey condition (frozen automation, completed,
// exited, retried); only infrastructure failures (load errors, CAS write
// errors) return an error, which asynq may retry.
func (e *Engine) ExecuteJourney(ctx context.Context, journeyID uint) error {
	journey, err := e.journeyRepo.GetByID(journeyID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // deleted between enqueue and execution
		}
		return fmt.Errorf("automation: load journey %d: %w", journeyID, err)
	}
	if journey.Status != models.AutomationJourneyStatusActive {
		return nil // finished elsewhere (concurrent executor, manual exit)
	}

	automation, err := e.automationRepo.GetByID(journey.AutomationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The automation was soft-deleted; its journeys should already
			// have been exited, but never leave a zombie behind.
			e.exitJourney(journey.ID, journey.AutomationID, models.AutomationExitReasonAutomationDeleted)
			return nil
		}
		return fmt.Errorf("automation: load automation %d: %w", journey.AutomationID, err)
	}
	if automation.Status != models.AutomationStatusLive {
		// Paused or drafted: frozen in place, journeys are neither advanced
		// nor exited, exactly like the the reference workflow platform reference engine.
		return nil
	}

	workflow, err := ParseWorkflow(automation.RootNodeID, automation.Nodes)
	if err != nil {
		// Corrupt workflow document: a node-level failure on the current
		// cursor, retried and eventually failed like any node error.
		logger.Error("automation: workflow parse failed", "automation_id", automation.ID, "error", err)
		e.handleNodeError(journey, err.Error())
		return nil
	}

	// The subscriber is resolved up front (it was resolved at enrollment too):
	// a contact deleted mid-journey exits the journey instead of retry-failing.
	subscriber, err := e.subscriberRepo.FindByID(journey.SubscriberID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			e.exitJourney(journey.ID, journey.AutomationID, models.AutomationExitReasonContactMissing)
			return nil
		}
		return fmt.Errorf("automation: load subscriber %d: %w", journey.SubscriberID, err)
	}

	properties := map[string]any{}
	if journey.ContextJSON != "" {
		if err := json.Unmarshal([]byte(journey.ContextJSON), &properties); err != nil {
			logger.Warn("automation: journey context is not valid JSON; continuing with empty context", "journey_id", journey.ID, "error", err)
			properties = map[string]any{}
		}
	}
	nc := &nodeContext{
		automation: automation,
		journey:    journey,
		subscriber: subscriber,
		workflow:   workflow,
		properties: properties,
	}

	currentNodeID := journey.CurrentNodeID
	if currentNodeID == "" {
		currentNodeID = automation.RootNodeID
	}

	for i := 0; i < maxNodesPerTick; i++ {
		node := workflow.Node(currentNodeID)
		if node == nil {
			e.exitJourney(journey.ID, journey.AutomationID, models.AutomationExitReasonNodeDeleted)
			return nil
		}

		// Fresh-cursor guard: the claim snapshot can be stale (an execution
		// nudge and a scheduler tick, or two schedulers, may hold the same
		// journey). Re-read the cursor before executing; if another executor
		// moved it, adopt its position instead of double-executing the node.
		fresh, err := e.journeyRepo.GetByID(journey.ID)
		if err != nil {
			return fmt.Errorf("automation: re-read journey %d: %w", journey.ID, err)
		}
		if fresh.Status != models.AutomationJourneyStatusActive {
			return nil
		}
		if fresh.CurrentNodeID != currentNodeID {
			if fresh.ScheduledAt != nil && fresh.ScheduledAt.After(e.now()) {
				return nil // the other executor parked it on a delay
			}
			journey = fresh
			currentNodeID = fresh.CurrentNodeID
			if currentNodeID == "" {
				currentNodeID = automation.RootNodeID
			}
			continue // adopt and re-resolve without executing
		}

		isTrigger := node.Type == NodeTypeTrigger
		var run *models.AutomationNodeRun
		if !isTrigger {
			// The trigger node's run row is written at enrollment; every other
			// node records its own entered row before executing.
			run = &models.AutomationNodeRun{
				JourneyID:    journey.ID,
				AutomationID: journey.AutomationID,
				NodeID:       node.ID,
				NodeType:     node.Type,
				Action:       models.AutomationRunActionEntered,
				EnteredAt:    e.now(),
			}
			if err := e.runRepo.Create(run); err != nil {
				return fmt.Errorf("automation: write run row (journey %d, node %q): %w", journey.ID, node.ID, err)
			}
		}

		started := e.now()
		result, execErr := e.executeNode(ctx, nc, node)
		durationMs := int(e.now().Sub(started).Milliseconds())

		if execErr != nil {
			// The run row and the journey's last_error surface through the
			// execution inspector and the API, so they are written as the
			// guard's canonical text: raw delivery detail (resolved addresses,
			// ports, resolver internals) never reaches a client. The server
			// log keeps the raw error — that is where the detail belongs.
			errMsg := webhook.SanitizeDeliveryError(execErr.Error())
			if run != nil {
				if failErr := e.runRepo.Fail(run.ID, errMsg); failErr != nil {
					logger.Error("automation: failed to record run failure", "run_id", run.ID, "error", failErr)
				}
			}
			logger.Warn("automation: node execution failed", "journey_id", journey.ID, "node_id", node.ID, "error", execErr.Error())
			e.handleNodeError(journey, errMsg)
			return nil
		}

		next := workflow.NextNodeFrom(node, result.Branch)
		if ok, err := e.journeyRepo.UpdateCursorIfActive(journey.ID, next, result.ScheduledAt); err != nil {
			return fmt.Errorf("automation: advance cursor (journey %d): %w", journey.ID, err)
		} else if !ok {
			// The journey left the active state while the node executed; the
			// CAS lost, so the walk must not continue.
			return nil
		}

		if run != nil {
			output := "{}"
			if result.Output != nil {
				if b, err := json.Marshal(result.Output); err == nil {
					output = string(b)
				}
			}
			if err := e.runRepo.Complete(run.ID, output, durationMs, e.now()); err != nil {
				logger.Error("automation: failed to record run completion", "run_id", run.ID, "error", err)
			}
		}

		if result.ScheduledAt != nil && result.ScheduledAt.After(e.now()) {
			return nil // parked on a delay; the scheduler resumes the journey
		}
		if next == "" {
			e.completeJourney(journey.ID, journey.AutomationID)
			return nil
		}
		currentNodeID = next
	}

	// Tick budget exhausted mid-walk (a long side-effect-free chain): re-park
	// due-now so the next tick resumes instead of stranding the journey with a
	// NULL scheduled_at the claim query would never match.
	if _, err := e.journeyRepo.UpdateCursorIfActive(journey.ID, currentNodeID, timePtr(e.now())); err != nil {
		logger.Error("automation: failed to re-park journey after tick budget", "journey_id", journey.ID, "error", err)
	}
	logger.Warn("automation: journey tick budget exhausted; re-parked for the next tick", "journey_id", journey.ID, "node_id", currentNodeID)
	return nil
}

// handleNodeError schedules a journey retry: retryCount grows by one and the
// journey re-parks at now + 2^retryCount minutes, until the budget (maxRetries)
// is spent and the journey fails permanently.
func (e *Engine) handleNodeError(journey *models.AutomationJourney, errMsg string) {
	retryCount := journey.RetryCount + 1
	if retryCount >= maxRetries {
		ok, err := e.journeyRepo.MarkFailedIfActive(journey.ID, errMsg)
		if err != nil {
			logger.Error("automation: failed to mark journey failed", "journey_id", journey.ID, "error", err)
			return
		}
		if ok {
			if err := e.automationRepo.IncrementStat(journey.AutomationID, "failed"); err != nil {
				logger.Warn("automation: failed stat increment failed", "automation_id", journey.AutomationID, "error", err)
			}
			logger.Error("automation: journey failed permanently", "journey_id", journey.ID, "automation_id", journey.AutomationID, "error", errMsg)
		}
		return
	}
	backoff := time.Duration(1<<uint(retryCount)) * time.Minute
	retryAt := e.now().Add(backoff)
	if ok, err := e.journeyRepo.BumpRetry(journey.ID, retryAt, errMsg); err != nil {
		logger.Error("automation: failed to schedule journey retry", "journey_id", journey.ID, "error", err)
	} else if ok {
		logger.Warn("automation: journey re-parked for retry", "journey_id", journey.ID, "retry_count", retryCount, "retry_at", retryAt.Format(time.RFC3339))
	}
}

// completeJourney finishes a journey under CAS and bumps the completed stat.
func (e *Engine) completeJourney(journeyID, automationID uint) {
	ok, err := e.journeyRepo.MarkCompletedIfActive(journeyID, e.now())
	if err != nil {
		logger.Error("automation: failed to mark journey completed", "journey_id", journeyID, "error", err)
		return
	}
	if ok {
		if err := e.automationRepo.IncrementStat(automationID, "completed"); err != nil {
			logger.Warn("automation: completed stat increment failed", "automation_id", automationID, "error", err)
		}
	}
}

// exitJourney exits a journey with a reason under CAS and bumps the exited stat.
func (e *Engine) exitJourney(journeyID, automationID uint, reason string) {
	ok, err := e.journeyRepo.MarkExitedIfActive(journeyID, reason)
	if err != nil {
		logger.Error("automation: failed to mark journey exited", "journey_id", journeyID, "error", err)
		return
	}
	if ok {
		logger.Info("automation: journey exited", "journey_id", journeyID, "reason", reason)
		if err := e.automationRepo.IncrementStat(automationID, "exited"); err != nil {
			logger.Warn("automation: exited stat increment failed", "automation_id", automationID, "error", err)
		}
	}
}

// Sweep re-enqueues journeys the driving loop lost: active, due journeys whose
// scheduled_at has been in the past for longer than the sweep grace — a lost
// enqueue, a worker crash between claim and execution, or a scheduler outage.
// Journeys a live executor already holds (scheduled_at NULL) are excluded by
// the repository query. Capped at sweepLimit per run.
func (e *Engine) Sweep(ctx context.Context) error {
	cutoff := e.now().Add(-e.sweepGrace)
	journeys, err := e.journeyRepo.ListOverdue(cutoff, sweepLimit)
	if err != nil {
		return fmt.Errorf("automation: list overdue journeys: %w", err)
	}
	if len(journeys) == 0 {
		return nil
	}
	enqueued := 0
	for i := range journeys {
		if ctx.Err() != nil {
			break
		}
		if err := e.producer.EnqueueAutomationRun(journeys[i].ID, 0); err != nil {
			logger.Error("automation: sweep enqueue failed", "journey_id", journeys[i].ID, "error", err)
			continue
		}
		enqueued++
	}
	logger.Info("automation: sweep re-enqueued overdue journeys", "found", len(journeys), "enqueued", enqueued)
	return nil
}

// timePtr returns a pointer to t (the CAS APIs take *time.Time).
func timePtr(t time.Time) *time.Time {
	return &t
}
