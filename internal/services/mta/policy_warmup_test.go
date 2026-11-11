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
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mta

import (
	"testing"
	"time"
)

// rampDay anchors a schedule at midnight UTC of day 0.
func rampDay0() time.Time {
	return time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
}

func testRamp() *WarmupSchedule {
	return &WarmupSchedule{StartVolume: 100, DailyMultiplier: 2.0, Days: 5, StartedAt: rampDay0()}
}

// The ramp index walks one day per anchor day and completes after Days.
func TestWarmupDayIndexAcrossDays(t *testing.T) {
	s := testRamp()
	base := rampDay0()
	cases := []struct {
		offset time.Duration
		want   int
	}{
		{0, 0},
		{1 * time.Hour, 0},
		{23 * time.Hour, 0},
		{24 * time.Hour, 1},
		{47 * time.Hour, 1},
		{72 * time.Hour, 3},
		{4 * 24 * time.Hour, 4},
		{5 * 24 * time.Hour, 5},  // ramp completed exactly
		{9 * 24 * time.Hour, 9},  // long past the ramp
	}
	for _, c := range cases {
		if got := WarmupDayIndex(s, base.Add(c.offset)); got != c.want {
			t.Fatalf("day index at +%s = %d, want %d", c.offset, got, c.want)
		}
	}
	// Before the anchor clamps to day 0 (never a negative overshoot).
	if got := WarmupDayIndex(s, base.Add(-3*time.Hour)); got != 0 {
		t.Fatalf("day index before anchor = %d, want 0", got)
	}
	// A nil schedule indexes at 0.
	if got := WarmupDayIndex(nil, time.Now()); got != 0 {
		t.Fatalf("nil schedule day index = %d, want 0", got)
	}
}

// The daily cap follows the geometric ramp across the days and expires after
// the ramp completes.
func TestWarmupDailyCapAcrossDays(t *testing.T) {
	s := testRamp() // 100/day, ×2, 5 days
	base := rampDay0()
	cases := []struct {
		offset time.Duration
		want   int
	}{
		{0, 100},
		{24 * time.Hour, 200},
		{48 * time.Hour, 400},
		{72 * time.Hour, 800},
		{96 * time.Hour, 1600},
		{5 * 24 * time.Hour, 0},   // ramp complete — uncapped
		{10 * 24 * time.Hour, 0},  // still uncapped later
	}
	for _, c := range cases {
		if got := WarmupDailyCap(s, base.Add(c.offset)); got != c.want {
			t.Fatalf("daily cap at +%s = %d, want %d", c.offset, got, c.want)
		}
	}
	// No schedule → no cap.
	if got := WarmupDailyCap(nil, time.Now()); got != 0 {
		t.Fatalf("nil schedule cap = %d, want 0", got)
	}
	// A zero-length ramp is uncapped.
	zero := &WarmupSchedule{StartVolume: 100, Days: 0, StartedAt: base}
	if got := WarmupDailyCap(zero, base); got != 0 {
		t.Fatalf("zero-day ramp cap = %d, want 0", got)
	}
	// A multiplier <= 0 is treated as flat; the floor is 1 message/day.
	flat := &WarmupSchedule{StartVolume: 50, DailyMultiplier: -1, Days: 3, StartedAt: base}
	if got := WarmupDailyCap(flat, base.Add(48*time.Hour)); got != 50 {
		t.Fatalf("flat ramp cap = %d, want 50", got)
	}
	tiny := &WarmupSchedule{StartVolume: 0, DailyMultiplier: 2, Days: 3, StartedAt: base}
	if got := WarmupDailyCap(tiny, base); got != 1 {
		t.Fatalf("degenerate ramp cap = %d, want 1 (cannot silently zero a domain)", got)
	}
}

// The hourly cap spreads the daily cap over 24 hours, rounding up, minimum 1.
func TestWarmupHourlyCapSpreadsDaily(t *testing.T) {
	s := testRamp()
	base := rampDay0()
	if got := WarmupHourlyCap(s, base); got != 5 { // ceil(100/24)=5
		t.Fatalf("day-0 hourly cap = %d, want 5", got)
	}
	if got := WarmupHourlyCap(s, base.Add(24*time.Hour)); got != 9 { // ceil(200/24)=9
		t.Fatalf("day-1 hourly cap = %d, want 9", got)
	}
	// Ramp over → no cap.
	if got := WarmupHourlyCap(s, base.Add(6*24*time.Hour)); got != 0 {
		t.Fatalf("post-ramp hourly cap = %d, want 0", got)
	}
	// A small daily cap still yields an hourly allowance of 1.
	tiny := &WarmupSchedule{StartVolume: 1, DailyMultiplier: 1, Days: 3, StartedAt: base}
	if got := WarmupHourlyCap(tiny, base); got != 1 {
		t.Fatalf("tiny ramp hourly cap = %d, want 1", got)
	}
}

// The effective budget: the cap binds even for otherwise-unlimited domains,
// the policy budget stands when it is tighter, and after the ramp completes
// the policy budget is untouched.
func TestEffectiveHourlyBudgetFoldsWarmupCap(t *testing.T) {
	base := rampDay0()
	s := testRamp()

	// Unlimited policy (budget 0) is capped by the ramp while active.
	if got := EffectiveHourlyBudget(Policy{}, s, base); got != 5 {
		t.Fatalf("unlimited policy with active ramp = %d, want 5", got)
	}
	// A tighter policy budget wins over the cap.
	if got := EffectiveHourlyBudget(Policy{HourlyBudget: 3}, s, base); got != 3 {
		t.Fatalf("tight policy budget = %d, want 3", got)
	}
	// A looser policy budget loses to the cap.
	if got := EffectiveHourlyBudget(Policy{HourlyBudget: 1000}, s, base.Add(24*time.Hour)); got != 9 {
		t.Fatalf("loose policy budget = %d, want 9", got)
	}
	// After the ramp completes the policy budget stands unchanged —
	// including unlimited.
	if got := EffectiveHourlyBudget(Policy{HourlyBudget: 1000}, s, base.Add(6*24*time.Hour)); got != 1000 {
		t.Fatalf("post-ramp policy budget = %d, want 1000", got)
	}
	if got := EffectiveHourlyBudget(Policy{}, s, base.Add(6*24*time.Hour)); got != 0 {
		t.Fatalf("post-ramp unlimited budget = %d, want 0 (unlimited)", got)
	}
	// No schedule at all → the policy budget verbatim.
	if got := EffectiveHourlyBudget(Policy{HourlyBudget: 42}, nil, base); got != 42 {
		t.Fatalf("no schedule budget = %d, want 42", got)
	}
}
