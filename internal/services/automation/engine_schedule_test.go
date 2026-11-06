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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// R-13 tests: the schedule (cron) trigger path. These run the real engine's
// ProcessSchedules over the fake database with the fake clock and pin: the
// first-sight no-replay checkpoint, firing exactly the due occurrences,
// pausing via automation status, invalid cron expressions, the 1440-occurrence
// catch-up bound, and the audience resolution (segment wins over list).

func statsJSON(t *testing.T, lastFired *time.Time) string {
	t.Helper()
	b, err := json.Marshal(models.WorkflowStats{LastFiredAt: lastFired})
	if err != nil {
		t.Fatalf("marshal stats: %v", err)
	}
	return string(b)
}

// scheduleAutomation seeds a live (or status-overridden) schedule automation
// and the list audience it targets. Returns the automation id.
func scheduleAutomation(t *testing.T, state *engState, status, triggerCfg, stats string) uint {
	engSeed(t, state, "subscriber_lists", map[string]any{
		"id": uint(4), "user_id": uint(7), "workspace_id": uint(3), "name": "list-a",
	})
	engSeed(t, state, "subscriber_list_members", map[string]any{
		"list_id": uint(4), "subscriber_id": uint(9),
	})
	engSeed(t, state, "subscribers", map[string]any{
		"id": uint(9), "user_id": uint(7), "workspace_id": uint(3),
		"email": "alice@example.com", "status": "subscribed",
	})
	return uint(engSeed(t, state, "automations", map[string]any{
		"user_id": uint(7), "workspace_id": uint(3), "name": "sched",
		"status": status, "trigger_config": triggerCfg, "nodes": "[]",
		"root_node_id": "t", "stats": stats, "version": 1,
	}))
}

func journeyCount(state *engState) int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.tables["automation_journeys"].rows)
}

func checkpointOf(t *testing.T, state *engState, id uint) *time.Time {
	t.Helper()
	row := state.rowMap("automations", int64(id))
	if row == nil {
		t.Fatalf("automation %d missing", id)
	}
	stats := models.ParseWorkflowStats(row["stats"].(string))
	return stats.LastFiredAt
}

const everyMinute = `{"event_kind":"schedule","cron_expression":"* * * * *","list_id":4}`

// The very first tick after a schedule automation goes live must checkpoint
// WITHOUT firing: a fresh automation never replays the past.
func TestScheduleFirstSightCheckpointsWithoutFiring(t *testing.T) {
	e, state, _, _, clock := newTestEngine(t)
	id := scheduleAutomation(t, state, models.AutomationStatusLive, everyMinute, models.DefaultWorkflowStatsJSON)

	e.ProcessSchedules(context.Background())

	if got := journeyCount(state); got != 0 {
		t.Fatalf("first sight enrolled %d journeys, want 0", got)
	}
	got := checkpointOf(t, state, id)
	if got == nil || !got.Equal(clock.Now()) {
		t.Fatalf("checkpoint = %v, want the first-sight instant %v", got, clock.Now())
	}

	// The checkpoint makes the next tick a no-op too.
	e.ProcessSchedules(context.Background())
	if got := journeyCount(state); got != 0 {
		t.Fatalf("second tick enrolled %d journeys, want 0", got)
	}
}

// A due occurrence fires once and advances the checkpoint to the fired
// occurrence; the immediately following tick does not re-fire.
func TestScheduleFiresDueOccurrenceOnce(t *testing.T) {
	e, state, _, _, clock := newTestEngine(t)
	prev := clock.Now().Add(-2 * time.Minute)
	id := scheduleAutomation(t, state, models.AutomationStatusLive, everyMinute, statsJSON(t, &prev))

	e.ProcessSchedules(context.Background())

	if got := journeyCount(state); got != 1 {
		t.Fatalf("due tick enrolled %d journeys, want 1", got)
	}
	got := checkpointOf(t, state, id)
	if got == nil || !got.Equal(clock.Now()) {
		t.Fatalf("checkpoint = %v, want the fired occurrence %v", got, clock.Now())
	}

	e.ProcessSchedules(context.Background())
	if got := journeyCount(state); got != 1 {
		t.Fatalf("next tick enrolled %d more journeys, want none", got)
	}
}

// Only live automations are scheduled: a paused/draft schedule trigger never
// fires.
func TestScheduleSkipsNonLiveAutomations(t *testing.T) {
	for _, status := range []string{models.AutomationStatusDraft, "paused"} {
		e, state, _, _, _ := newTestEngine(t)
		prev := time.Now().Add(-time.Hour)
		scheduleAutomation(t, state, status, everyMinute, statsJSON(t, &prev))

		e.ProcessSchedules(context.Background())

		if got := journeyCount(state); got != 0 {
			t.Fatalf("status %q enrolled %d journeys, want 0", status, got)
		}
	}
}

// An invalid cron expression is skipped (warned, never fired, never
// checkpointed).
func TestScheduleInvalidCronSkipped(t *testing.T) {
	e, state, _, _, _ := newTestEngine(t)
	prev := time.Now().Add(-time.Hour)
	id := scheduleAutomation(t, state, models.AutomationStatusLive,
		`{"event_kind":"schedule","cron_expression":"banana","list_id":4}`, statsJSON(t, &prev))

	e.ProcessSchedules(context.Background())

	if got := journeyCount(state); got != 0 {
		t.Fatalf("invalid cron enrolled %d journeys, want 0", got)
	}
	if got := checkpointOf(t, state, id); got == nil || !got.Equal(prev) {
		t.Fatalf("checkpoint = %v, want untouched %v", got, prev)
	}
}

// A moderate backlog drains by firing once at the newest due occurrence.
func TestScheduleCatchupFiresNewestDueOccurrence(t *testing.T) {
	e, state, _, _, clock := newTestEngine(t)
	prev := clock.Now().Add(-2 * time.Hour)
	id := scheduleAutomation(t, state, models.AutomationStatusLive, everyMinute, statsJSON(t, &prev))

	e.ProcessSchedules(context.Background())

	if got := journeyCount(state); got != 1 {
		t.Fatalf("catch-up enrolled %d journeys, want 1 (one fire, not 120)", got)
	}
	got := checkpointOf(t, state, id)
	if got == nil || !got.Equal(clock.Now()) {
		t.Fatalf("checkpoint = %v, want the newest due occurrence %v", got, clock.Now())
	}
}

// A backlog deeper than maxScheduleCatchup (1440) occurrences does not fire:
// the checkpoint jumps forward past the bound so the backlog drains in bounded
// steps and the automation resumes on its cadence.
func TestScheduleBacklogBeyondBoundAdvancesWithoutFiring(t *testing.T) {
	e, state, _, _, clock := newTestEngine(t)
	// 2000 due minute-occurrences > the 1440 bound.
	prev := clock.Now().Add(-2000 * time.Minute)
	id := scheduleAutomation(t, state, models.AutomationStatusLive, everyMinute, statsJSON(t, &prev))

	e.ProcessSchedules(context.Background())

	if got := journeyCount(state); got != 0 {
		t.Fatalf("deep backlog enrolled %d journeys, want 0", got)
	}
	got := checkpointOf(t, state, id)
	want := clock.Now().Add(-560 * time.Minute) // prev + 1440 minutes
	if got == nil || !got.Equal(want) {
		t.Fatalf("checkpoint = %v, want advanced past the bound to %v", got, want)
	}
}

const segmentAndListCfg = `{"event_kind":"schedule","cron_expression":"* * * * *","list_id":4,"segment_id":12}`

// The audience resolver: a segment wins over the list, and only members the
// segment's condition matches enroll.
func TestScheduleSegmentAudienceWins(t *testing.T) {
	e, state, _, _, clock := newTestEngine(t)
	engSeed(t, state, "segments", map[string]any{
		"id": int64(12), "user_id": uint(7), "workspace_id": uint(3),
		"name": "alice only", "condition": aliceSegmentCondition,
	})
	// Two list members, only one in the segment.
	engSeed(t, state, "subscriber_lists", map[string]any{
		"id": uint(4), "user_id": uint(7), "workspace_id": uint(3), "name": "list-a",
	})
	engSeed(t, state, "subscriber_list_members", map[string]any{
		"list_id": uint(4), "subscriber_id": uint(9),
	})
	engSeed(t, state, "subscriber_list_members", map[string]any{
		"list_id": uint(4), "subscriber_id": uint(10),
	})
	engSeed(t, state, "subscribers", map[string]any{
		"id": uint(9), "user_id": uint(7), "workspace_id": uint(3),
		"email": "alice@example.com", "status": "subscribed",
	})
	engSeed(t, state, "subscribers", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": uint(3),
		"email": "bob@example.com", "status": "subscribed",
	})
	prev := clock.Now().Add(-time.Hour)
	scheduleAutomationCfgID(t, state, segmentAndListCfg, prev)

	e.ProcessSchedules(context.Background())

	if got := journeyCount(state); got != 1 {
		t.Fatalf("segment audience enrolled %d journeys, want 1 (alice only)", got)
	}
}

// A schedule trigger whose segment belongs to another workspace resolves no
// audience and fires nothing; the same for a trigger with neither list nor
// segment.
func TestScheduleUnresolvableAudienceFiresNothing(t *testing.T) {
	// Foreign segment.
	e, state, _, _, _ := newTestEngine(t)
	engSeed(t, state, "segments", map[string]any{
		"id": int64(20), "user_id": uint(42), "workspace_id": uint(99),
		"name": "foreign", "condition": aliceSegmentCondition,
	})
	prev := time.Now().Add(-time.Hour)
	scheduleAutomationCfgID(t, state, `{"event_kind":"schedule","cron_expression":"* * * * *","segment_id":20}`, prev)
	e.ProcessSchedules(context.Background())
	if got := journeyCount(state); got != 0 {
		t.Fatalf("foreign segment enrolled %d journeys, want 0", got)
	}

	// No audience at all.
	e2, state2, _, _, _ := newTestEngine(t)
	scheduleAutomationCfgID(t, state2, `{"event_kind":"schedule","cron_expression":"* * * * *"}`, prev)
	e2.ProcessSchedules(context.Background())
	if got := journeyCount(state2); got != 0 {
		t.Fatalf("audience-less trigger enrolled %d journeys, want 0", got)
	}
}

// scheduleAutomationCfgID seeds just the automation (audience seeded by the
// caller).
func scheduleAutomationCfgID(t *testing.T, state *engState, triggerCfg string, lastFired time.Time) {
	engSeed(t, state, "automations", map[string]any{
		"user_id": uint(7), "workspace_id": uint(3), "name": "sched",
		"status": models.AutomationStatusLive, "trigger_config": triggerCfg, "nodes": "[]",
		"root_node_id": "t", "stats": statsJSON(t, &lastFired), "version": 1,
	})
}
