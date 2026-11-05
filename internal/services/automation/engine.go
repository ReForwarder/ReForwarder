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
	"strings"
	"time"

	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/robfig/cron/v3"
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
	segmentRepo    *repositories.SegmentRepository
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
		segmentRepo:    repositories.NewSegmentRepository(db),
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
		e.ProcessSchedules(ctx)
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
		// nor exited, exactly like the Notifuse reference engine.
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
			metrics.IncrementAutomationNodeFailure(node.Type)
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
			metrics.IncrementAutomationJourneyFailed()
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
		metrics.IncrementAutomationJourneyCompleted()
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
		metrics.IncrementAutomationJourneyExited()
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

// ---------- R-14: segment-membership conditions ----------

// evaluateJourneyConditions evaluates a filter node's condition set against the
// journey's subscriber. in_segment rules (R-14) resolve through the engine's
// segment checker; every other rule goes through the shared EvaluateConditions
// evaluator. The scope is the automation's tenant (the journey's workspace) —
// the author of the workflow, not the contact, picked the segment.
func (e *Engine) evaluateJourneyConditions(conditions []models.FilterRule, nc *nodeContext) bool {
	scope := repositories.ResourceScope{UserID: nc.subscriber.UserID, WorkspaceID: nc.journey.WorkspaceID}
	return evaluateConditionSet(conditions, nc.subscriber,
		e.listCheckerFor(nc.journey),
		e.segmentMemberChecker(scope, nc.subscriber.ID),
	)
}

// evaluateConditionSet AND-combines a condition set. in_segment rules (R-14)
// resolve through segmentChecker (nil checker ⇒ they never match, so a store
// without segment support degrades to closed); everything else delegates to
// the shared EvaluateConditions evaluator whose operator support
// filterRuleProblem mirrors for save-time validation. An empty set matches.
func evaluateConditionSet(conditions []models.FilterRule, sub *models.Subscriber, listChecker func(uint) bool, segmentChecker func(uint) bool) bool {
	pass := make([]models.FilterRule, 0, len(conditions))
	for _, rule := range conditions {
		if strings.TrimSpace(rule.Field) != FieldInSegment {
			pass = append(pass, rule)
			continue
		}
		id, ok := coerceUint(rule.Value)
		if !ok || segmentChecker == nil {
			return false
		}
		if !segmentChecker(id) {
			return false
		}
	}
	return EvaluateConditions(pass, sub, listChecker)
}

// segmentMemberChecker returns the in_segment predicate bound to one scope and
// subscriber (R-14).
func (e *Engine) segmentMemberChecker(scope repositories.ResourceScope, subscriberID uint) func(uint) bool {
	return func(segmentID uint) bool {
		return e.subscriberInSegment(scope, segmentID, subscriberID)
	}
}

// subscriberInSegment reports whether one subscriber matches a saved segment's
// condition, evaluated live with the same SQL the segment preview and the
// campaign audience resolution use. A segment outside the scope, an invalid
// condition, or a condition-less segment matches nothing.
func (e *Engine) subscriberInSegment(scope repositories.ResourceScope, segmentID, subscriberID uint) bool {
	if e.segmentRepo == nil {
		return false
	}
	seg, err := e.segmentRepo.FindByID(segmentID)
	if err != nil || !repositories.OwnsWorkspaceResource(scope, seg.WorkspaceID) {
		return false
	}
	if seg.Condition.IsEmpty() {
		return false
	}
	expr, args, err := repositories.BuildSegmentConditionSQL(seg.Condition)
	if err != nil {
		logger.Warn("automation: in_segment references a segment with an invalid condition", "segment_id", segmentID, "error", err)
		return false
	}
	var count int64
	q := repositories.ApplyScope(
		e.db.Model(&models.Subscriber{}).
			Where("id = ?", subscriberID).
			Where(expr, args...),
		scope,
	)
	if err := q.Count(&count).Error; err != nil {
		logger.Warn("automation: in_segment membership query failed", "segment_id", segmentID, "subscriber_id", subscriberID, "error", err)
		return false
	}
	return count > 0
}

// ---------- R-13: schedule (cron) triggers ----------

// maxScheduleCatchup bounds how many missed cron occurrences one fire chases.
// A process that was down for a long time (or an automation whose checkpoint
// predates a schedule change) must not iterate an unbounded backlog: past the
// bound the checkpoint jumps forward without firing, so a buried schedule
// drains cheaply and resumes on its cadence.
const maxScheduleCatchup = 1440

// liveScheduleAutomations loads every live automation whose trigger config
// mentions the schedule kind. The substring pre-filter keeps the per-tick JSON
// parses bounded; a false positive (e.g. a custom event named "…schedule…")
// costs one cheap parse and is skipped by the exact kind check. Soft-deleted
// rows are excluded by GORM's DeletedAt handling.
func (e *Engine) liveScheduleAutomations() []models.Automation {
	var automations []models.Automation
	if err := e.db.Where("status = ?", models.AutomationStatusLive).Find(&automations).Error; err != nil {
		logger.Error("automation: schedule lookup failed", "error", err)
		return nil
	}
	var out []models.Automation
	for i := range automations {
		if strings.Contains(automations[i].TriggerConfig, models.AutomationEventSchedule) {
			out = append(out, automations[i])
		}
	}
	return out
}

// ProcessSchedules fires every live schedule-trigger automation whose cron has
// an occurrence due (R-13). Called from the engine loop every tick — the cron
// granularity is a minute, the tick is 10s, and the persisted checkpoint makes
// the check idempotent across overlapping processes. Exported for tests.
func (e *Engine) ProcessSchedules(_ context.Context) {
	automations := e.liveScheduleAutomations()
	for i := range automations {
		e.processSchedule(&automations[i])
	}
}

func (e *Engine) processSchedule(automation *models.Automation) {
	cfg, err := parseTriggerConfig(automation.TriggerConfig)
	if err != nil || cfg.EventKind != models.AutomationEventSchedule {
		return
	}
	if strings.TrimSpace(cfg.CronExpression) == "" {
		logger.Warn("automation: schedule trigger has no cron expression", "automation_id", automation.ID)
		return
	}
	sched, err := cron.ParseStandard(cfg.CronExpression)
	if err != nil {
		logger.Warn("automation: schedule trigger has an invalid cron expression", "automation_id", automation.ID, "cron", cfg.CronExpression, "error", err)
		return
	}

	now := e.now()
	stats := models.ParseWorkflowStats(automation.Stats)
	if stats.LastFiredAt == nil {
		// First sight (creation or the first tick after the trigger became a
		// schedule): checkpoint at now WITHOUT firing, so a fresh automation
		// never replays the past.
		e.persistScheduleCheckpoint(automation.ID, now)
		return
	}

	next := sched.Next(*stats.LastFiredAt)
	if next.After(now) {
		return // nothing due
	}
	fireAt := next
	behind := 1
	for t := sched.Next(fireAt); !t.After(now) && behind < maxScheduleCatchup; t = sched.Next(t) {
		fireAt = t
		behind++
	}
	if !sched.Next(fireAt).After(now) {
		// Still buried after the catch-up bound: advance the checkpoint
		// without firing so the backlog drains in bounded steps.
		logger.Warn("automation: schedule backlog too deep; advancing checkpoint without firing", "automation_id", automation.ID, "checkpoint", fireAt.Format(time.RFC3339))
		e.persistScheduleCheckpoint(automation.ID, fireAt)
		return
	}

	e.fireSchedule(automation, cfg, fireAt)
	e.persistScheduleCheckpoint(automation.ID, fireAt)
}

// fireSchedule enrolls every eligible audience member into the automation for
// one schedule occurrence. The audience is the trigger's segment or list
// (segment wins when both are set); non-subscribed members are skipped and
// the per-contact frequency dedup (once / every_time) applies as for any
// trigger.
func (e *Engine) fireSchedule(automation *models.Automation, cfg models.AutomationTriggerConfig, fireAt time.Time) {
	scope := repositories.ResourceScope{UserID: automation.UserID, WorkspaceID: automation.WorkspaceID}
	audience, err := e.scheduleAudience(scope, cfg)
	if err != nil {
		logger.Error("automation: schedule audience resolution failed", "automation_id", automation.ID, "error", err)
		return
	}

	enrolled := 0
	for i := range audience {
		sub := audience[i]
		if sub.Status != models.SubscriberStatusSubscribed {
			continue
		}
		member := sub
		e.enrollAutomation(automation, &member, TriggerEvent{
			Kind: models.AutomationEventSchedule,
			Properties: map[string]any{
				"scheduled_at": fireAt.UTC().Format(time.RFC3339),
			},
		})
		enrolled++
	}
	metrics.IncrementAutomationScheduleFire()
	logger.Info("automation: schedule fired", "automation_id", automation.ID, "audience", len(audience), "enrolled", enrolled, "fire_at", fireAt.Format(time.RFC3339))
}

// scheduleAudience resolves a schedule trigger's audience. The segment branch
// mirrors the campaign processor's segment path (ownership-checked, live
// condition); the list branch resolves static members of an owned list.
func (e *Engine) scheduleAudience(scope repositories.ResourceScope, cfg models.AutomationTriggerConfig) ([]models.Subscriber, error) {
	if cfg.SegmentID != nil && *cfg.SegmentID > 0 {
		seg, err := e.segmentRepo.FindByID(*cfg.SegmentID)
		if err != nil {
			return nil, fmt.Errorf("schedule segment %d not found", *cfg.SegmentID)
		}
		if !repositories.OwnsWorkspaceResource(scope, seg.WorkspaceID) {
			return nil, fmt.Errorf("schedule segment %d does not belong to the automation's workspace", seg.ID)
		}
		subs, _, err := e.segmentRepo.FindSubscribersByCondition(scope, seg.Condition, -1, 0)
		return subs, err
	}

	listID := uint(0)
	if cfg.ListID != nil {
		listID = *cfg.ListID
	}
	if listID == 0 {
		return nil, fmt.Errorf("schedule trigger has no audience: set list_id or segment_id")
	}
	list, err := e.lists.FindByID(listID)
	if err != nil {
		return nil, fmt.Errorf("schedule list %d not found", listID)
	}
	if !repositories.OwnsWorkspaceResource(scope, list.WorkspaceID) {
		return nil, fmt.Errorf("schedule list %d does not belong to the automation's workspace", list.ID)
	}
	subs, _, err := e.lists.ListMembers(listID, -1, 0)
	return subs, err
}

// persistScheduleCheckpoint writes the schedule trigger's fire checkpoint into
// the Automation.Stats JSONB (R-13). It survives restarts, so a restarted
// scheduler never refires an occurrence.
func (e *Engine) persistScheduleCheckpoint(automationID uint, t time.Time) {
	err := e.db.Exec(
		`UPDATE automations SET stats = jsonb_set(COALESCE(stats, '{}'::jsonb), '{last_fired_at}', to_jsonb(?::text), true) WHERE id = ?`,
		t.UTC().Format(time.RFC3339Nano),
		automationID,
	).Error
	if err != nil {
		logger.Error("automation: failed to persist schedule checkpoint", "automation_id", automationID, "error", err)
	}
}
