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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package automation

import (
	"context"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
)

// The trigger side: event-to-automation matching, AND-combined conditions
// (including in_list through the real list membership repo), and the
// once-frequency dedup decided by the trigger log's unique index.

func enrollEvent(listID *uint) TriggerEvent {
	return TriggerEvent{
		Kind:       EventKindListSubscribed,
		ListID:     listID,
		Subscriber: &models.Subscriber{ID: 9, UserID: 7, WorkspaceID: uintPtr(3), Email: "alice@example.com"},
	}
}

func TestTriggerMatchesEventKind(t *testing.T) {
	auto := &models.Automation{ID: 1, ListID: uintPtr(3)}
	ev := enrollEvent(uintPtr(3))

	cfg := models.AutomationTriggerConfig{EventKind: EventKindListSubscribed, Frequency: FrequencyOnce}
	if !triggerMatches(cfg, auto, ev) {
		t.Error("list.subscribed must match a list.subscribed event")
	}
	cfg.EventKind = EventKindContactCreated
	if triggerMatches(cfg, auto, ev) {
		t.Error("contact.created must not match a list.subscribed event")
	}
}

func TestTriggerMatchesListScope(t *testing.T) {
	evList3 := enrollEvent(uintPtr(3))
	evList9 := enrollEvent(uintPtr(9))
	evNoList := TriggerEvent{Kind: EventKindListSubscribed, Subscriber: evList3.Subscriber}

	// The trigger's own list_id wins.
	cfg := models.AutomationTriggerConfig{EventKind: EventKindListSubscribed, ListID: uintPtr(3)}
	if !triggerMatches(cfg, &models.Automation{ID: 1}, evList3) {
		t.Error("trigger list 3 must match event on list 3")
	}
	if triggerMatches(cfg, &models.Automation{ID: 1}, evList9) {
		t.Error("trigger list 3 must not match an event on list 9")
	}
	if triggerMatches(cfg, &models.Automation{ID: 1}, evNoList) {
		t.Error("a list-scoped trigger must not fire without an event list")
	}

	// The automation-level list backs the trigger up.
	cfg = models.AutomationTriggerConfig{EventKind: EventKindListSubscribed}
	if !triggerMatches(cfg, &models.Automation{ID: 1, ListID: uintPtr(9)}, evList9) {
		t.Error("the automation list must satisfy an unspecific trigger")
	}
	if triggerMatches(cfg, &models.Automation{ID: 1, ListID: uintPtr(9)}, evList3) {
		t.Error("the automation list scope must not match a foreign list event")
	}

	// No list anywhere: the trigger matches any list event (the documented
	// contract — an unscoped trigger is scoped to nothing, so nothing is
	// filtered; the event's own list id may even be absent).
	cfg = models.AutomationTriggerConfig{EventKind: EventKindListSubscribed}
	if !triggerMatches(cfg, &models.Automation{ID: 1}, evList3) || !triggerMatches(cfg, &models.Automation{ID: 1}, evList9) {
		t.Error("an unscoped trigger fires for every list event")
	}
	if !triggerMatches(cfg, &models.Automation{ID: 1}, evNoList) {
		t.Error("an unscoped trigger does not filter on the event list at all")
	}
}

func TestTriggerMatchesCustomEventName(t *testing.T) {
	auto := &models.Automation{ID: 1}
	cfg := models.AutomationTriggerConfig{EventKind: EventKindCustom, CustomEventName: "Order.Placed"}

	ev := TriggerEvent{Kind: EventKindCustom, CustomEventName: "order.PLACED",
		Subscriber: &models.Subscriber{ID: 9, Email: "alice@example.com"}}
	if !triggerMatches(cfg, auto, ev) {
		t.Error("custom event names must match case-insensitively")
	}
	ev.CustomEventName = "order.cancelled"
	if triggerMatches(cfg, auto, ev) {
		t.Error("a different custom event must not match")
	}
	ev.CustomEventName = "  "
	if triggerMatches(cfg, auto, ev) {
		t.Error("a blank event name must not match a named custom trigger")
	}
	// The trigger itself without a name never fires.
	blank := models.AutomationTriggerConfig{EventKind: EventKindCustom}
	if triggerMatches(blank, auto, ev) {
		t.Error("a custom trigger without a name must never fire")
	}
}

func TestEnrollConditionsWithInListChecker(t *testing.T) {
	e, state, producer, _, _ := newTestEngine(t)
	listID := uint(3)

	// The automation's trigger: subscribed to list 3 AND a member of list 4.
	cfg := `{"event_kind":"list.subscribed","frequency":"once","conditions":[` +
		`{"field":"email","operator":"equals","value":"alice@example.com"},` +
		`{"field":"in_list","operator":"equals","value":4}]}`
	auto := seedAutomation(t, state, "MembersOnly", models.AutomationStatusLive, &listID, cfg,
		marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)

	// Not a member of list 4: no enrollment.
	_ = e.Enroll(context.Background(), enrollEvent(&listID))
	if got := state.rowCount("automation_journeys"); got != 0 {
		t.Fatalf("AND with a failing in_list must not enroll, got %d journeys", got)
	}

	// Make the contact a member of list 4: both conditions hold.
	engSeed(t, state, "subscriber_list_members", map[string]any{"list_id": uint(4), "subscriber_id": uint(9)})
	_ = e.Enroll(context.Background(), enrollEvent(&listID))

	if got := state.rowCount("automation_journeys"); got != 1 {
		t.Fatalf("AND with both conditions true must enroll once, got %d journeys", got)
	}
	journey := state.rowMap("automation_journeys", int64(producer.journeys[0]))
	if journey == nil || journey["automation_id"].(int64) != int64(auto) {
		t.Errorf("the enrolled journey must belong to automation %d", auto)
	}
}

// TestTriggerLogInsertSemantics pins the dedup insert contract against the
// fake database's ON CONFLICT DO NOTHING: the first insert wins and reports
// true, the second hits the unique key and reports false without an error.
func TestTriggerLogInsertSemantics(t *testing.T) {
	state := newEngState()
	gdb := engGorm(t, state)
	repo := repositories.NewAutomationTriggerLogRepository(gdb)

	inserted, err := repo.Insert(5, 9)
	if err != nil || !inserted {
		t.Fatalf("first insert = (%v, %v), want (true, nil)", inserted, err)
	}
	inserted, err = repo.Insert(5, 9)
	if err != nil {
		t.Fatalf("conflicting insert must not error, got %v", err)
	}
	if inserted {
		t.Fatal("second insert must report false (already enrolled)")
	}

	// A different subscriber is unaffected by the pair's uniqueness.
	inserted, err = repo.Insert(5, 10)
	if err != nil || !inserted {
		t.Fatalf("insert for a fresh subscriber = (%v, %v), want (true, nil)", inserted, err)
	}

	exists, err := repo.Exists(5, 9)
	if err != nil || !exists {
		t.Errorf("Exists(5,9) = (%v, %v), want (true, nil)", exists, err)
	}
	exists, err = repo.Exists(6, 9)
	if err != nil || exists {
		t.Errorf("Exists(6,9) = (%v, %v), want (false, nil)", exists, err)
	}

	if got := state.rowCount("automation_trigger_log"); got != 2 {
		t.Errorf("trigger log rows = %d, want 2", got)
	}
}

// TestEnrollDefaultsFrequencyToOnce pins the fallback: a trigger config
// without an explicit frequency dedups like once.
func TestEnrollDefaultsFrequencyToOnce(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	listID := uint(3)
	seedAutomation(t, state, "Welcome", models.AutomationStatusLive, &listID,
		`{"event_kind":"list.subscribed"}`, marshalNodes(t, linearChain(1)))
	seedSubscriber(t, state)

	ev := enrollEvent(&listID)
	_ = e.Enroll(context.Background(), ev)
	_ = e.Enroll(context.Background(), ev)

	if got := state.rowCount("automation_journeys"); got != 1 {
		t.Fatalf("journeys = %d, want 1 (default frequency is once)", got)
	}
	if got := state.rowCount("automation_trigger_log"); got != 1 {
		t.Fatalf("trigger log rows = %d, want 1", got)
	}
}
