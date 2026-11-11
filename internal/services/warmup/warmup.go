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

// Package warmup holds the IP warm-up ramp math (OB-6 W3, ADR-007 decision
// 7). A fresh source IP (or a domain newly switched to direct delivery) has
// no reputation with receiving networks; blasting full volume on day one is
// the classic path to the spam folder. A warm-up schedule caps the domain's
// daily volume on a geometric ramp — day N allows round(StartVolume ×
// DailyMultiplier^N) messages for Days days — and the delivery path enforces
// the cap through the hourly-budget gate, while the reputation surface reads
// it back for the domain page.
//
// Everything here is pure and takes `now` so the ramp math is testable
// across days without sleeping. The package is a deliberate leaf: the MTA
// executor and the reputation service both consume it and neither may drag
// the other in.
package warmup

import (
	"math"
	"time"
)

// Schedule is the resolved ramp for one sending domain. Produced by the
// worker from the domain's active models.IPWarmupSchedule row; nil anywhere
// in the consumers means "no warm-up, the domain's own budget stands".
type Schedule struct {
	StartVolume     int       // allowed volume on ramp day 0
	DailyMultiplier float64   // per-day growth factor (1.5, 2.0, …)
	Days            int       // ramp length in days; 0 disables the cap
	StartedAt       time.Time // the day-0 anchor
}

// secondsPerDay is the ramp-day length the schedule math uses. Day boundaries
// are computed from StartedAt, so a 03:00 start shifts days to 03:00 — the
// operator's anchor is honored verbatim.
const secondsPerDay = 24 * 60 * 60

// DayIndex returns the 0-based ramp day of now relative to the schedule's
// start. Negative moments are clamped to day 0 — an early-starting fleet
// must not exceed day 0 volume — and the index is >= Days once the ramp has
// completed. A nil schedule or a zero-length ramp indexes at 0.
func DayIndex(s *Schedule, now time.Time) int {
	if s == nil || s.Days <= 0 {
		return 0
	}
	elapsed := now.Sub(s.StartedAt)
	if elapsed < 0 {
		return 0
	}
	return int(elapsed / (secondsPerDay * time.Second))
}

// DailyCap returns the allowed daily volume on the ramp day containing now:
// 0 when there is no schedule (or the ramp is over — no cap), the geometric
// ramp value otherwise. A multiplier <= 0 is treated as 1 (flat ramp at
// StartVolume), and the value never drops below 1 message per day once a
// schedule is active, so a misconfigured schedule cannot silently zero a
// domain.
func DailyCap(s *Schedule, now time.Time) int {
	if s == nil || s.Days <= 0 {
		return 0
	}
	day := DayIndex(s, now)
	if day >= s.Days {
		return 0 // ramp complete — the domain's own budget stands
	}
	mult := s.DailyMultiplier
	if mult <= 0 {
		mult = 1
	}
	cap := math.Round(float64(s.StartVolume) * math.Pow(mult, float64(day)))
	if cap < 1 {
		cap = 1
	}
	return int(cap)
}

// HourlyCap spreads the daily cap evenly across the 24 hours of the ramp
// day: the hourly budget gate spreads deferral across the hour anyway, so an
// even hourly allowance is the enforcement-friendly reading of the daily
// cap. Minimum 1 while the schedule is active.
func HourlyCap(s *Schedule, now time.Time) int {
	daily := DailyCap(s, now)
	if daily <= 0 {
		return 0
	}
	// Round up so the last hour of a ragged division is not shortchanged.
	hourly := int(math.Ceil(float64(daily) / 24.0))
	if hourly < 1 {
		hourly = 1
	}
	return hourly
}

// CapOf reports whether the schedule is actively capping right now (a cap
// above zero) — the deliver path uses it to annotate deferral texts.
func CapOf(s *Schedule, now time.Time) bool {
	return DailyCap(s, now) > 0
}
