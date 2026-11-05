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

package worker

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/hibiken/asynq"
)

// cwBatchTask builds a campaign:batch task payload.
func cwBatchTask(t *testing.T, campaignID uint) *asynq.Task {
	t.Helper()
	payload, err := json.Marshal(CampaignPayload{CampaignID: campaignID})
	if err != nil {
		t.Fatalf("marshal batch payload: %v", err)
	}
	return asynq.NewTask(TypeCampaignBatch, payload)
}

func newBackgroundContext() context.Context { return context.Background() }

// R-08 tests: A/B hold-back assignment, winner selection rules, and the
// exactly-once winner persistence + held-remainder unblocking. The fake
// database harness (cwState) comes from campaign_processor_segment_test.go in
// this package; the init below extends its campaigns table with the R-08
// columns so reads see zero values instead of NULL and the winner UPDATE is
// executable.

func init() {
	cwColumns["campaigns"] = append(cwColumns["campaigns"],
		"ab_test_metric", "ab_test_window_hours", "ab_test_min_sample",
		"ab_test_winner_sent", "ab_test_winner_stats", "local_send_hour")
	cwDefaults["campaigns"]["ab_test_metric"] = func() driver.Value { return "" }
	cwDefaults["campaigns"]["ab_test_window_hours"] = func() driver.Value { return int64(0) }
	cwDefaults["campaigns"]["ab_test_min_sample"] = func() driver.Value { return int64(0) }
	cwDefaults["campaigns"]["ab_test_winner_sent"] = func() driver.Value { return false }
	cwDefaults["campaigns"]["ab_test_winner_stats"] = func() driver.Value { return "{}" }
	cwDefaults["campaigns"]["local_send_hour"] = func() driver.Value { return nil }
}

func abStats(rows ...models.ABVariantStat) []models.ABVariantStat {
	return rows
}

// ---------- winner selection rules ----------

func TestSelectABWinnerByOpenRate(t *testing.T) {
	variants := models.ABTestVariants{{Name: "A", SplitPercentage: 20}, {Name: "B", SplitPercentage: 20}}
	stats := abStats(
		models.ABVariantStat{Variant: "A", Sent: 100, Opened: 10, Clicked: 2},
		models.ABVariantStat{Variant: "B", Sent: 100, Opened: 25, Clicked: 1},
	)
	res := selectABWinner(variants, stats, models.ABTestMetricOpenRate, 50)
	if res.Decision != abDecisionSelected || res.Winner != "B" {
		t.Fatalf("winner = %s (%d), want B selected", res.Winner, res.Decision)
	}
}

func TestSelectABWinnerByClickRateIgnoresOpenRate(t *testing.T) {
	variants := models.ABTestVariants{{Name: "A"}, {Name: "B"}}
	stats := abStats(
		// A: open 20%, click 1%; B: open 2%, click 10%
		models.ABVariantStat{Variant: "A", Sent: 100, Opened: 20, Clicked: 1},
		models.ABVariantStat{Variant: "B", Sent: 100, Opened: 2, Clicked: 10},
	)
	res := selectABWinner(variants, stats, models.ABTestMetricClickRate, 50)
	if res.Winner != "B" {
		t.Fatalf("click-rate winner = %s, want B (10%% clicks vs 1%%)", res.Winner)
	}
}

func TestSelectABWinnerTieBreakByOpenRate(t *testing.T) {
	variants := models.ABTestVariants{{Name: "A"}, {Name: "B"}}
	stats := abStats(
		// Equal click rates; A has the higher open rate → A wins the tie.
		models.ABVariantStat{Variant: "A", Sent: 100, Opened: 30, Clicked: 5},
		models.ABVariantStat{Variant: "B", Sent: 100, Opened: 10, Clicked: 5},
	)
	res := selectABWinner(variants, stats, models.ABTestMetricClickRate, 50)
	if res.Winner != "A" {
		t.Fatalf("tie-break winner = %s, want A (higher open rate)", res.Winner)
	}
}

func TestSelectABWinnerExactTieKeepsLowerVariantID(t *testing.T) {
	variants := models.ABTestVariants{{Name: "A"}, {Name: "B"}}
	stats := abStats(
		models.ABVariantStat{Variant: "A", Sent: 100, Opened: 10, Clicked: 2},
		models.ABVariantStat{Variant: "B", Sent: 100, Opened: 10, Clicked: 2},
	)
	res := selectABWinner(variants, stats, models.ABTestMetricOpenRate, 50)
	if res.Winner != "A" {
		t.Fatalf("exact tie winner = %s, want A (lower variant id)", res.Winner)
	}
}

func TestSelectABWinnerNoEngagementDefaultsToVariantA(t *testing.T) {
	variants := models.ABTestVariants{{Name: "A"}, {Name: "B"}}
	stats := abStats(
		models.ABVariantStat{Variant: "A", Sent: 80, Opened: 0, Clicked: 0},
		models.ABVariantStat{Variant: "B", Sent: 80, Opened: 0, Clicked: 0},
	)
	res := selectABWinner(variants, stats, models.ABTestMetricOpenRate, 50)
	if res.Decision != abDecisionSelected || res.Winner != "A" {
		t.Fatalf("zero-engagement winner = %s (%d), want A selected", res.Winner, res.Decision)
	}
}

func TestSelectABWinnerWaitsBelowMinSample(t *testing.T) {
	variants := models.ABTestVariants{{Name: "A"}, {Name: "B"}}
	stats := abStats(
		models.ABVariantStat{Variant: "A", Sent: 50, Opened: 10},
		models.ABVariantStat{Variant: "B", Sent: 49, Opened: 20},
	)
	res := selectABWinner(variants, stats, models.ABTestMetricOpenRate, 50)
	if res.Decision != abDecisionWait {
		t.Fatalf("decision = %d, want wait (variant B under the 50 sample)", res.Decision)
	}

	// A zero-sent variant is also under the floor — even with engagement.
	stats[1].Sent = 0
	res = selectABWinner(variants, stats, models.ABTestMetricOpenRate, 50)
	if res.Decision != abDecisionWait {
		t.Fatalf("decision = %d, want wait (variant B never delivered)", res.Decision)
	}
}

func TestSelectABWinnerIgnoresUnknownVariantGroups(t *testing.T) {
	// The stats query groups ALL variants, including the hold-back remainder
	// (empty variant name); it must not influence the decision.
	variants := models.ABTestVariants{{Name: "A"}, {Name: "B"}}
	stats := abStats(
		models.ABVariantStat{Variant: "", Sent: 300, Opened: 300},
		models.ABVariantStat{Variant: "A", Sent: 50, Opened: 5},
		models.ABVariantStat{Variant: "B", Sent: 50, Opened: 20},
	)
	res := selectABWinner(variants, stats, models.ABTestMetricOpenRate, 50)
	if res.Winner != "B" {
		t.Fatalf("winner = %s, want B (the held group must be ignored)", res.Winner)
	}
}

// ---------- hold-back assignment ----------

func TestAssignABTestGroupsHoldsTheRemainder(t *testing.T) {
	variants := models.ABTestVariants{{Name: "A", SplitPercentage: 20}, {Name: "B", SplitPercentage: 20}}
	messages := make([]models.CampaignMessage, 10)
	assignABTestGroups(messages, variants)

	counts := map[string]int{"A": 0, "B": 0, "": 0}
	for _, m := range messages {
		counts[m.Variant]++
	}
	if counts["A"] != 2 || counts["B"] != 2 {
		t.Fatalf("test group = A:%d B:%d, want 2/2 of 10", counts["A"], counts["B"])
	}
	if counts[""] != 6 {
		t.Fatalf("held remainder = %d, want 6", counts[""])
	}
}

func TestAssignABTestGroupsFullSplitLeavesNothingHeld(t *testing.T) {
	variants := models.ABTestVariants{{Name: "A", SplitPercentage: 50}, {Name: "B", SplitPercentage: 50}}
	messages := make([]models.CampaignMessage, 10)
	assignABTestGroups(messages, variants)

	for i, m := range messages {
		if m.Variant == "" {
			t.Fatalf("message %d held with a 100%% split, want fully assigned", i)
		}
	}
}

func TestAssignABTestGroupsTinyAudienceKeepsOnePerVariant(t *testing.T) {
	variants := models.ABTestVariants{{Name: "A", SplitPercentage: 50}, {Name: "B", SplitPercentage: 50}}
	messages := make([]models.CampaignMessage, 3)
	assignABTestGroups(messages, variants)

	if messages[0].Variant != "A" || messages[1].Variant != "B" {
		t.Fatalf("variants = %q/%q, want one recipient per variant", messages[0].Variant, messages[1].Variant)
	}
	if messages[2].Variant != "" {
		t.Fatalf("last recipient = %q, want held", messages[2].Variant)
	}
}

func TestIsHeldForWinner(t *testing.T) {
	ab := &models.Campaign{ABTestEnabled: true, ABTestVariants: models.ABTestVariants{{Name: "A"}, {Name: "B"}}}
	if !isHeldForWinner(ab, "") {
		t.Error("variant-less message must be held while the winner is pending")
	}
	if isHeldForWinner(ab, "A") {
		t.Error("test-group message must not be held")
	}
	winnerChosen := &models.Campaign{ABTestEnabled: true, ABTestWinnerSent: true}
	if isHeldForWinner(winnerChosen, "") {
		t.Error("nothing is held once the winner is selected")
	}
	plain := &models.Campaign{}
	if isHeldForWinner(plain, "") {
		t.Error("non-A/B campaigns never hold messages")
	}
}

// ---------- persistence: winner CAS + held remainder unblocking ----------

func TestMarkABWinnerExactlyOnce(t *testing.T) {
	state := newCWState()
	gdb := cwGorm(t, state)
	campaignRepo := repositories.NewCampaignRepository(gdb)

	cwSeed(t, state, "campaigns", map[string]any{
		"id": uint(31), "user_id": uint(7), "workspace_id": uint(3),
		"name": "AB", "subject": "Hi", "from_email": "hello@example.com",
		"template_id": uint(5), "status": string(models.CampaignStatusSending),
		"created_at": cwTestTime, "started_at": cwTestTime,
		"ab_test_winner_sent": false,
	})

	stats := models.TemplateData{"reason": "highest open_rate"}
	won, err := campaignRepo.MarkABWinner(31, "B", stats)
	if err != nil {
		t.Fatalf("MarkABWinner: %v", err)
	}
	if !won {
		t.Fatal("first decision must win the CAS")
	}

	// A racing second decision is refused: exactly-once.
	won, err = campaignRepo.MarkABWinner(31, "A", stats)
	if err != nil {
		t.Fatalf("MarkABWinner race: %v", err)
	}
	if won {
		t.Fatal("second decision must lose the exactly-once guard")
	}
	if got := state.columnByID("campaigns", "ab_test_winner", 31); got != "B" {
		t.Fatalf("stored winner = %v, want B (unchanged by the loser)", got)
	}
	if got := state.columnByID("campaigns", "ab_test_winner_sent", 31); got != true {
		t.Fatalf("ab_test_winner_sent = %v, want true", got)
	}
}

func TestAssignVariantToHeldUnblocksOnlyHeldPending(t *testing.T) {
	state := newCWState()
	gdb := cwGorm(t, state)
	messageRepo := repositories.NewCampaignMessageRepository(gdb)

	const campaignID = uint(32)
	seedMessage := func(id uint, status models.CampaignMessageStatus, variant string) {
		cwSeed(t, state, "campaign_messages", map[string]any{
			"id": id, "campaign_id": campaignID, "subscriber_id": id + 100,
			"status": string(status), "variant": variant, "created_at": cwTestTime,
		})
	}
	seedMessage(1, models.CampaignMsgPending, "")
	seedMessage(2, models.CampaignMsgPending, "")
	seedMessage(3, models.CampaignMsgPending, "A") // test group: untouched
	seedMessage(4, models.CampaignMsgSent, "")     // already-sent held row: untouched
	seedMessage(5, models.CampaignMsgFailed, "")   // failed: untouched

	affected, err := messageRepo.AssignVariantToHeld(campaignID, "B")
	if err != nil {
		t.Fatalf("AssignVariantToHeld: %v", err)
	}
	if affected != 2 {
		t.Fatalf("rows affected = %d, want 2", affected)
	}
	for id, want := range map[uint]string{1: "B", 2: "B", 3: "A", 4: "", 5: ""} {
		if got := state.columnByID(cwTableCampaignMessages, "variant", int64(id)); got != want {
			t.Errorf("message %d variant = %v, want %q", id, got, want)
		}
	}
}

// ---------- the winner check itself ----------

func TestMaybeSelectABWinnerWaitsForWindow(t *testing.T) {
	state := newCWState()
	processor := newWinnerProcessor(t, state)

	started := time.Now().Add(-time.Hour)
	cwSeed(t, state, "campaigns", map[string]any{
		"id": uint(33), "user_id": uint(7), "workspace_id": uint(3),
		"name": "AB", "subject": "Hi", "from_email": "hello@example.com",
		"template_id": uint(5), "status": string(models.CampaignStatusSending),
		"created_at": started, "started_at": started,
		"ab_test_enabled":     true,
		"ab_test_variants":    `[{"name":"A","split_percentage":20},{"name":"B","split_percentage":20}]`,
		"ab_test_winner_sent": false,
	})

	campaign, err := processor.campaignRepo.FindByID(33)
	if err != nil {
		t.Fatalf("load campaign: %v", err)
	}
	recheckIn, err := processor.maybeSelectABWinner(campaign)
	if err != nil {
		t.Fatalf("maybeSelectABWinner: %v", err)
	}
	if recheckIn <= 0 || recheckIn > 24*time.Hour {
		t.Fatalf("recheckIn = %v, want a positive delay until the 24h window closes", recheckIn)
	}
	if campaign.ABTestWinnerSent {
		t.Error("no winner may be selected while the window is open")
	}
}

func TestMaybeSelectABWinnerNoopForPlainCampaigns(t *testing.T) {
	state := newCWState()
	processor := newWinnerProcessor(t, state)

	campaign := &models.Campaign{ID: 34}
	recheckIn, err := processor.maybeSelectABWinner(campaign)
	if err != nil {
		t.Fatalf("maybeSelectABWinner: %v", err)
	}
	if recheckIn != 0 {
		t.Fatalf("recheckIn = %v, want 0 (not an A/B campaign)", recheckIn)
	}
}

// TestHandleCampaignStart_ABHoldBack drives the campaign:start path end to end
// against the fake database: ten recipients, 20/20 split — two recipients per
// variant go out as the test group, six stay variant-less (held).
func TestHandleCampaignStart_ABHoldBack(t *testing.T) {
	state := newCWState()
	processor := newWinnerProcessor(t, state) // nil producer: enqueue guard fails after fan-out

	cwSeed(t, state, "campaigns", map[string]any{
		"id": uint(35), "user_id": uint(7), "workspace_id": uint(3),
		"name": "AB", "subject": "Hi", "from_email": "hello@example.com",
		"template_id": uint(5), "status": string(models.CampaignStatusSending),
		"segment_id": uint(9), "created_at": cwTestTime,
		"ab_test_enabled":  true,
		"ab_test_variants": `[{"name":"A","split_percentage":20},{"name":"B","split_percentage":20}]`,
	})
	cwSeed(t, state, "segments", map[string]any{
		"id": uint(9), "user_id": uint(7), "workspace_id": uint(3),
		"name": "Audience", "condition": cwSegmentCondition, "created_at": cwTestTime,
	})
	for i := 0; i < 10; i++ {
		cwSeed(t, state, "subscribers", map[string]any{
			"id": uint(101 + i), "user_id": uint(7), "workspace_id": uint(3),
			"email":  "alice@example.com", // the fake matches them all via the equality condition
			"status": "subscribed", "created_at": cwTestTime,
		})
	}

	// The nil producer stops the task after the messages exist (the documented
	// enqueue guard), which is exactly the point where the assignment is
	// observable.
	err := processor.HandleCampaignStart(newBackgroundContext(), cwStartTask(t, 35))
	if err == nil || !strings.Contains(err.Error(), "producer not configured") {
		t.Fatalf("HandleCampaignStart error = %v, want the nil-producer enqueue guard after messages were created", err)
	}

	if got := state.rowCount(); got != 10 {
		t.Fatalf("campaign messages created = %d, want 10", got)
	}
	counts := map[string]int{"A": 0, "B": 0, "": 0}
	for _, row := range state.rowsByColumn(cwTableCampaignMessages, "campaign_id", 35) {
		variant := row[state.colIndex(cwTableCampaignMessages, "variant")].(string)
		counts[variant]++
	}
	if counts["A"] != 2 || counts["B"] != 2 {
		t.Fatalf("test group = A:%d B:%d, want 2/2 (20%% of 10)", counts["A"], counts["B"])
	}
	if counts[""] != 6 {
		t.Fatalf("held remainder = %d, want 6", counts[""])
	}
}

// TestHandleCampaignBatch_DefersWhileWinnerPending pins the batch-loop side of
// the hold-back: with a pending A/B decision and only held messages left, the
// batch sends nothing (statuses unchanged) — the held recipients wait for the
// winner.
func TestHandleCampaignBatch_DefersWhileWinnerPending(t *testing.T) {
	state := newCWState()
	processor := newWinnerProcessor(t, state)

	started := time.Now().Add(-time.Hour)
	cwSeed(t, state, "campaigns", map[string]any{
		"id": uint(36), "user_id": uint(7), "workspace_id": uint(3),
		"name": "AB", "subject": "Hi", "from_email": "hello@example.com",
		"template_id": uint(5), "status": string(models.CampaignStatusSending),
		"created_at": started, "started_at": started,
		"ab_test_enabled":     true,
		"ab_test_variants":    `[{"name":"A","split_percentage":20},{"name":"B","split_percentage":20}]`,
		"ab_test_winner_sent": false,
	})
	for i := 0; i < 3; i++ {
		cwSeed(t, state, "campaign_messages", map[string]any{
			"id": uint(201 + i), "campaign_id": uint(36), "subscriber_id": uint(301 + i),
			"status": string(models.CampaignMsgPending), "variant": "", "created_at": cwTestTime,
		})
	}

	// The nil producer stops the task at the deferral enqueue (the documented
	// guard) — the point of this test is that NOTHING left the held pool on
	// the way there: no email rows, no status flips.
	_ = processor.HandleCampaignBatch(newBackgroundContext(), cwBatchTask(t, 36))
	for i := 0; i < 3; i++ {
		if got := state.columnByID(cwTableCampaignMessages, "status", int64(201+i)); got != string(models.CampaignMsgPending) {
			t.Errorf("held message %d status = %v, want pending (deferred until the winner)", 201+i, got)
		}
	}
}

// TestHandleCampaignBatch_RespectsPause pins that a deferred batch (timezone or
// winner window) re-checks campaign state at execution time: a paused campaign
// is a no-op tick.
func TestHandleCampaignBatch_RespectsPause(t *testing.T) {
	state := newCWState()
	processor := newWinnerProcessor(t, state)

	cwSeed(t, state, "campaigns", map[string]any{
		"id": uint(37), "user_id": uint(7), "workspace_id": uint(3),
		"name": "Paused", "subject": "Hi", "from_email": "hello@example.com",
		"template_id": uint(5), "status": string(models.CampaignStatusPaused),
		"created_at": cwTestTime,
	})
	cwSeed(t, state, "campaign_messages", map[string]any{
		"id": uint(401), "campaign_id": uint(37), "subscriber_id": uint(501),
		"status": string(models.CampaignMsgPending), "variant": "", "created_at": cwTestTime,
	})

	if err := processor.HandleCampaignBatch(newBackgroundContext(), cwBatchTask(t, 37)); err != nil {
		t.Fatalf("HandleCampaignBatch: %v", err)
	}
	if got := state.columnByID(cwTableCampaignMessages, "status", 401); got != string(models.CampaignMsgPending) {
		t.Errorf("message status = %v, want pending (paused campaign untouched)", got)
	}
}

// newWinnerProcessor builds a processor on the fake database with nil
// template/tracking dependencies (the deferred paths never render content).
func newWinnerProcessor(t *testing.T, state *cwState) *CampaignProcessor {
	t.Helper()
	gdb := cwGorm(t, state)
	return NewCampaignProcessor(
		repositories.NewCampaignRepository(gdb),
		repositories.NewCampaignMessageRepository(gdb),
		repositories.NewSubscriberListRepository(gdb),
		repositories.NewSegmentRepository(gdb),
		repositories.NewSubscriberRepository(gdb),
		nil, nil, nil, nil, nil, nil,
		nil, // producer: the enqueue guard fails the task after messages exist
		nil, // dispatcher
	)
}
