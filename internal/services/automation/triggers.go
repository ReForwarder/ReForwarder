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
	"strings"
	"time"

	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
)

// enrollNudgeDelay is how long after enrollment the automation:run nudge fires.
// The 10s in-process scheduler is the backstop if the enqueue is lost.
const enrollNudgeDelay = time.Second

// TriggerEvent is one occurrence of a triggerable event, raised by the handler
// hooks (list subscription, contact creation, unsubscribe, opens/clicks,
// bounces, custom-event ingest).
type TriggerEvent struct {
	// Kind is one of the EventKind* constants.
	Kind string
	// ListID is the list the event happened on, for list.* events.
	ListID *uint
	// CustomEventName is the ingest event name for custom events.
	CustomEventName string
	// Subscriber is the contact the event is about. Enrollment resolves the
	// subscriber up front (unlike Notifuse, which resolves at execution time),
	// so a nil subscriber means nothing to enroll.
	Subscriber *models.Subscriber
	// Properties are the event's payload; stored on the journey context and
	// offered to email templates and webhook bodies.
	Properties map[string]any
}

// AutomationTrigger is the narrow interface handlers hold so they depend on
// enrollment without importing the full engine. The engine implements it; W3's
// SetAutomationTrigger-style setters take this type.
type AutomationTrigger interface {
	Enroll(ctx context.Context, ev TriggerEvent) error
}

var _ AutomationTrigger = (*Engine)(nil)

// Enroll matches the event against the workspace's live automations and starts
// a journey for every hit.
//
// The critical rule: engine problems NEVER propagate into the triggering
// action. Enroll recovers panics, logs every error with a bounded message and
// always returns nil — callers may ignore the return value entirely. The error
// return exists for interface symmetry only.
func (e *Engine) Enroll(ctx context.Context, ev TriggerEvent) error {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("automation: enroll panic recovered", "kind", ev.Kind, "panic", r)
		}
	}()
	e.enroll(ctx, ev)
	return nil
}

func (e *Engine) enroll(_ context.Context, ev TriggerEvent) {
	sub := ev.Subscriber
	if sub == nil || sub.ID == 0 {
		logger.Debug("automation: trigger event has no subscriber; skipping", "kind", ev.Kind)
		return
	}
	if strings.TrimSpace(ev.Kind) == "" {
		return
	}

	automations := e.liveAutomationsFor(sub)
	for i := range automations {
		e.enrollAutomation(&automations[i], sub, ev)
	}
}

// enrollAutomation runs the per-automation match pipeline: trigger config
// match, condition evaluation, once-frequency dedup, journey creation, stats
// and the execution nudge. Every failure is logged and contained.
func (e *Engine) enrollAutomation(automation *models.Automation, sub *models.Subscriber, ev TriggerEvent) {
	cfg, err := parseTriggerConfig(automation.TriggerConfig)
	if err != nil {
		logger.Warn("automation: malformed trigger config; skipping", "automation_id", automation.ID, "error", err)
		return
	}
	if !triggerMatches(cfg, automation, ev) {
		return
	}
	// Conditions are evaluated in the automation's tenant so in_segment rules
	// (R-14) resolve against segments the workflow's author can see.
	scope := repositories.ResourceScope{UserID: sub.UserID, WorkspaceID: automation.WorkspaceID}
	if !evaluateConditionSet(cfg.Conditions, sub,
		func(listID uint) bool { return e.lists.IsMember(listID, sub.ID) },
		e.segmentMemberChecker(scope, sub.ID),
	) {
		return
	}

	frequency := cfg.Frequency
	if frequency == "" {
		frequency = FrequencyOnce
		if cfg.EventKind == models.AutomationEventSchedule {
			// A schedule is inherently recurring: the once-per-contact-forever
			// default would silence every digest after its first fire (R-13).
			frequency = FrequencyEveryTime
		}
	}
	if frequency == FrequencyOnce {
		// The permanent dedup row decides atomically: false means this contact
		// already entered the automation. Inserted BEFORE the journey so two
		// racing events still enroll exactly once (a failure in between burns
		// the dedup for this contact — logged, and far preferable to a double
		// enrollment).
		inserted, err := e.triggerLogRepo.Insert(automation.ID, sub.ID)
		if err != nil {
			logger.Error("automation: trigger log insert failed", "automation_id", automation.ID, "error", err)
			return
		}
		if !inserted {
			return
		}
	}

	e.createJourneyFor(automation, sub, ev)
}

// createJourneyFor inserts the journey (cursor at the root, due now), bumps the
// enrolled stat, writes the trigger node's run row and nudges the executor.
func (e *Engine) createJourneyFor(automation *models.Automation, sub *models.Subscriber, ev TriggerEvent) {
	now := e.now()

	properties := ev.Properties
	if properties == nil {
		properties = map[string]any{}
	}
	contextJSON, err := json.Marshal(properties)
	if err != nil {
		logger.Warn("automation: failed to marshal event properties; storing empty context", "automation_id", automation.ID, "error", err)
		contextJSON = []byte("{}")
	}

	journey := &models.AutomationJourney{
		AutomationID:  automation.ID,
		WorkspaceID:   automation.WorkspaceID,
		SubscriberID:  sub.ID,
		Email:         sub.Email,
		CurrentNodeID: automation.RootNodeID,
		Status:        models.AutomationJourneyStatusActive,
		ScheduledAt:   &now,
		ContextJSON:   string(contextJSON),
		EnteredAt:     now,
	}
	if err := e.journeyRepo.Create(journey); err != nil {
		logger.Error("automation: journey creation failed", "automation_id", automation.ID, "subscriber_id", sub.ID, "error", err)
		return
	}
	metrics.IncrementAutomationJourneyEnrolled()
	if err := e.automationRepo.IncrementStat(automation.ID, "enrolled"); err != nil {
		logger.Warn("automation: enrolled stat increment failed", "automation_id", automation.ID, "error", err)
	}

	// The trigger node's run row is recorded here (and completed immediately —
	// its execution IS the enrollment, a pass-through); the executor skips
	// trigger-type nodes when walking so the timeline shows exactly one row.
	run := &models.AutomationNodeRun{
		JourneyID:    journey.ID,
		AutomationID: automation.ID,
		NodeID:       automation.RootNodeID,
		NodeType:     models.AutomationNodeTrigger,
		Action:       models.AutomationRunActionEntered,
		EnteredAt:    now,
	}
	if err := e.runRepo.Create(run); err != nil {
		logger.Warn("automation: trigger run row creation failed", "journey_id", journey.ID, "error", err)
	} else if err := e.runRepo.Complete(run.ID, "{}", 0, now); err != nil {
		logger.Warn("automation: trigger run row completion failed", "journey_id", journey.ID, "error", err)
	}

	if err := e.producer.EnqueueAutomationRun(journey.ID, enrollNudgeDelay); err != nil {
		logger.Error("automation: execution nudge enqueue failed (the scheduler will pick the journey up)", "journey_id", journey.ID, "error", err)
	}
}

// liveAutomationsFor returns every live automation in the subscriber's scope.
// The repository is paginated, so this walks the pages (live automations are
// few; the common case is a single page).
func (e *Engine) liveAutomationsFor(sub *models.Subscriber) []models.Automation {
	const pageSize = 100
	var out []models.Automation
	for page := 1; ; page++ {
		automations, total, err := e.automationRepo.ListByScope(page, pageSize, sub.UserID, sub.WorkspaceID, models.AutomationStatusLive)
		if err != nil {
			logger.Error("automation: live automation lookup failed", "error", err)
			return out
		}
		out = append(out, automations...)
		if len(automations) < pageSize || len(out) >= int(total) {
			return out
		}
	}
}

// parseTriggerConfig decodes an Automation.TriggerConfig JSONB value.
func parseTriggerConfig(raw string) (models.AutomationTriggerConfig, error) {
	var cfg models.AutomationTriggerConfig
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// triggerMatches reports whether the event satisfies the trigger's kind, list
// scope and (for custom events) name.
func triggerMatches(cfg models.AutomationTriggerConfig, automation *models.Automation, ev TriggerEvent) bool {
	if cfg.EventKind != ev.Kind {
		return false
	}
	if cfg.EventKind == EventKindCustom {
		name := strings.TrimSpace(cfg.CustomEventName)
		if name == "" {
			return false
		}
		return strings.EqualFold(strings.TrimSpace(ev.CustomEventName), name)
	}
	if strings.HasPrefix(cfg.EventKind, "list.") {
		// A list-scoped trigger fires only for its list: the trigger's own
		// list_id, or the automation-level list when the trigger leaves it
		// empty (the builder accepts either). A trigger scoped to no list at
		// all matches any list event.
		triggerList := uint(0)
		if cfg.ListID != nil {
			triggerList = *cfg.ListID
		}
		automationList := uint(0)
		if automation.ListID != nil {
			automationList = *automation.ListID
		}
		if triggerList == 0 && automationList == 0 {
			return true
		}
		if ev.ListID == nil {
			return false
		}
		return *ev.ListID == triggerList || *ev.ListID == automationList
	}
	return true
}

// RecordBouncedTrigger is the worker-side hook for the email.bounced trigger
// kind: it builds the event from the bounced email's subscriber and hands it
// to the engine. Safe to call with a nil trigger (feature not wired) or a nil
// subscriber (the bounce could not be resolved to one).
func RecordBouncedTrigger(t AutomationTrigger, sub *models.Subscriber) {
	if t == nil || sub == nil {
		return
	}
	// Enroll recovers its own panics and swallows its own errors — a bounce
	// must never fail because automation enrollment did.
	_ = t.Enroll(context.Background(), TriggerEvent{
		Kind:       models.AutomationEventEmailBounced,
		Subscriber: sub,
	})
}
