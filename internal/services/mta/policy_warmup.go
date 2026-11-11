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
	"time"

	"github.com/goposta/posta/internal/services/warmup"
)

// Warm-up ramp enforcement (OB-6 W3, ADR-007 decision 7). The ramp math
// lives in the shared leaf package internal/services/warmup — the MTA
// executor enforces the cap through the hourly-budget gate and the reputation
// surface reads it back, and neither may import the other. This file keeps
// the mta-facing names and folds the cap into the per-domain policy.

// WarmupSchedule is the resolved ramp for one sending domain (the shared
// warmup.Schedule). The worker builds it from the domain's active
// models.IPWarmupSchedule row; nil anywhere in the delivery path means "no
// warm-up, policy budget stands".
type WarmupSchedule = warmup.Schedule

// WarmupDayIndex returns the 0-based ramp day of now relative to the
// schedule's start (clamped at day 0 before the anchor; >= Days after the
// ramp ends).
func WarmupDayIndex(s *WarmupSchedule, now time.Time) int { return warmup.DayIndex(s, now) }

// WarmupDailyCap returns the allowed daily volume on the ramp day containing
// now; 0 when there is no schedule or the ramp is over.
func WarmupDailyCap(s *WarmupSchedule, now time.Time) int { return warmup.DailyCap(s, now) }

// WarmupHourlyCap spreads the daily cap evenly over the 24 hours of the ramp
// day (minimum 1 while active).
func WarmupHourlyCap(s *WarmupSchedule, now time.Time) int { return warmup.HourlyCap(s, now) }

// EffectiveHourlyBudget folds the warm-up cap into the policy's hourly
// budget. The cap binds even for otherwise-unlimited domains (budget 0):
// while a schedule is active the hourly allowance is the smaller of the two;
// once the ramp completes (cap 0) the policy budget stands unchanged.
func EffectiveHourlyBudget(p Policy, s *WarmupSchedule, now time.Time) int {
	cap := warmup.HourlyCap(s, now)
	if cap <= 0 {
		return p.HourlyBudget
	}
	if p.HourlyBudget <= 0 || cap < p.HourlyBudget {
		return cap
	}
	return p.HourlyBudget
}
