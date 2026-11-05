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
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
)

// R-09 tests: timezone resolution with the documented fallbacks, the send
// window gating, window-open computation across DST transitions, the timezone
// grouping plan, and the per-message mapping.

func TestResolveSendLocationFallbacks(t *testing.T) {
	if got := resolveSendLocation(""); got != time.Local {
		t.Errorf("empty timezone = %v, want the server-local zone", got)
	}
	if got := resolveSendLocation("   "); got != time.Local {
		t.Errorf("blank timezone = %v, want the server-local zone", got)
	}
	if got := resolveSendLocation("America/New_York"); got.String() != "America/New_York" {
		t.Errorf("IANA name = %v, want America/New_York", got)
	}
	if got := resolveSendLocation("Mars/Olympus_Mons"); got != time.UTC {
		t.Errorf("unknown timezone = %v, want the documented UTC fallback", got)
	}
}

func TestLocalSendEligibleWindow(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY: %v", err)
	}
	now := time.Date(2026, 5, 1, 9, 15, 0, 0, ny)
	if !localSendEligible(ny, 9, now) {
		t.Error("09:15 local must be inside the 09:00 window")
	}
	if localSendEligible(ny, 10, now) {
		t.Error("09:15 local must be outside the 10:00 window")
	}
}

func TestNextSendWindowOpenSameDay(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, ny)
	if got := nextSendWindowOpen(ny, 9, now); got != time.Hour {
		t.Errorf("delay = %v, want 1h (today's 09:00)", got)
	}
}

func TestNextSendWindowOpenTomorrow(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 5, 1, 9, 30, 0, 0, ny)
	if got := nextSendWindowOpen(ny, 9, now); got != 23*time.Hour+30*time.Minute {
		t.Errorf("delay = %v, want 23h30m (tomorrow's 09:00)", got)
	}
}

// TestNextSendWindowOpenAcrossSpringForward proves DST handling by
// construction: on 2026-03-29 Europe/Berlin jumps 02:00→03:00 CET→CEST. At
// 01:30 CET a 03:00 send hour opens 30 real minutes later (03:00 CEST = 01:00
// UTC), not 1h30m of wall clock.
func TestNextSendWindowOpenAcrossSpringForward(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("load Berlin: %v", err)
	}
	now := time.Date(2026, 3, 29, 1, 30, 0, 0, berlin) // CET (UTC+1)
	got := nextSendWindowOpen(berlin, 3, now)
	if got != 30*time.Minute {
		t.Fatalf("delay across spring-forward = %v, want 30m (03:00 CEST is 01:00 UTC)", got)
	}
}

// TestNextSendWindowOpenAcrossFallBack: on 2026-10-25 Europe/Berlin repeats
// the 02:00-03:00 hour (CEST→CET). From 01:30 CEST (a non-ambiguous instant —
// Go's choice within a repeated hour is unspecified) the next 03:00 opening is
// 03:00 CET = 02:00 UTC, i.e. 2h30m of real time for a 1h30m wall-clock gap.
func TestNextSendWindowOpenAcrossFallBack(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	now := time.Date(2026, 10, 25, 1, 30, 0, 0, berlin)
	got := nextSendWindowOpen(berlin, 3, now)
	if got != 150*time.Minute {
		t.Fatalf("delay across fall-back = %v, want 2h30m", got)
	}
}

func TestPlanTimezoneGroupsDedupsAndFallsBack(t *testing.T) {
	tzs := []string{"Europe/Berlin", "Europe/Berlin", "", "Mars/Olympus_Mons", "America/New_York"}

	// No send hour: everything is immediately eligible (send-immediately
	// default), regardless of the timezone values.
	plans := planTimezoneGroups(tzs, nil, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC))
	if len(plans) != 4 {
		t.Fatalf("groups = %d, want 4 (duplicates collapsed)", len(plans))
	}
	for _, p := range plans {
		if !p.EligibleNow || p.NextOpenIn != 0 {
			t.Errorf("group %q = eligible %v next %v, want immediately eligible", p.Timezone, p.EligibleNow, p.NextOpenIn)
		}
	}

	// With a send hour, per-group eligibility is computed in each group's own
	// zone; unknown names follow the documented fallbacks. At 12:00 UTC on May 1
	// Berlin is already 14:00 CEST while New York is 08:00 EDT.
	hour := 14
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	plans = planTimezoneGroups(tzs, &hour, now)
	eligible := map[string]bool{}
	for _, p := range plans {
		eligible[p.Timezone] = p.EligibleNow
	}
	if !eligible["Europe/Berlin"] {
		t.Errorf("Berlin (14:00 local) must be eligible for the 14:00 window: %v", eligible)
	}
	if eligible["America/New_York"] {
		t.Error("New York (08:00 local) must be deferred at 12:00 UTC")
	}
	if eligible["Mars/Olympus_Mons"] {
		t.Error("the UTC fallback (12:00) must be deferred for the 14:00 window")
	}
	// The empty-timezone group follows the server clock by design — its
	// eligibility is environment-dependent and asserted nowhere.
}

func TestEarliestOpenDelay(t *testing.T) {
	if got := earliestOpenDelay([]tzGroupPlan{{Timezone: "A", EligibleNow: true}}); got != 0 {
		t.Errorf("eligible group must send now, got %v", got)
	}
	plans := []tzGroupPlan{
		{Timezone: "A", EligibleNow: false, NextOpenIn: 5 * time.Hour},
		{Timezone: "B", EligibleNow: false, NextOpenIn: 2 * time.Hour},
	}
	if got := earliestOpenDelay(plans); got != 2*time.Hour {
		t.Errorf("earliest open = %v, want 2h", got)
	}
}

func TestMessageTimezonesMapping(t *testing.T) {
	messages := []models.CampaignMessage{
		{ID: 1, SubscriberID: 10},
		{ID: 2, SubscriberID: 11},
		{ID: 3, SubscriberID: 99}, // no matching subscriber record
	}
	subscribers := []models.Subscriber{
		{ID: 10, Timezone: "Europe/Berlin"},
		{ID: 11, Timezone: ""},
	}
	tzs := messageTimezones(messages, subscribers)
	if len(tzs) != 2 || tzs[0] != "Europe/Berlin" || tzs[1] != "" {
		t.Fatalf("timezones = %v, want [Europe/Berlin ]", tzs)
	}
}

// TestLocalSendEligibleNowGatesOnlyWhenConfigured pins the batch-loop gate:
// without local-time sending (or without a configured hour) every message is
// eligible; with both, eligibility follows the subscriber's zone.
func TestLocalSendEligibleNowGatesOnlyWhenConfigured(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	nowNY := time.Date(2026, 5, 1, 8, 0, 0, 0, ny) // 12:00 UTC

	immediate := &models.Campaign{}
	if !localSendEligibleNow(immediate, "America/New_York", nowNY) {
		t.Error("without send_at_local_time everything is eligible")
	}

	hour := 9
	enabled := &models.Campaign{SendAtLocalTime: true, LocalSendHour: &hour}
	if localSendEligibleNow(enabled, "America/New_York", nowNY) {
		t.Error("08:59 in New York must wait for the 09:00 window")
	}
	// The empty timezone follows the server clock: at 08:00 server-local the
	// 09:00 window is not open, whatever zone the server runs on.
	nowLocal := time.Date(2026, 5, 1, 8, 0, 0, 0, time.Local)
	if localSendEligibleNow(enabled, "", nowLocal) {
		t.Error("server-local batch must follow the server clock (08:00 < 09:00)")
	}
}
