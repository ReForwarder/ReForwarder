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

package security

import (
	"reflect"
	"testing"
)

// mapSettings is a SettingsSource backed by plain maps, so LoadRules can be
// exercised with custom, missing and invalid values without the settings
// provider (and without a database behind it).
type mapSettings struct {
	ints  map[string]int
	bools map[string]bool
}

func (m mapSettings) GetInt(key string, def int) int {
	if v, ok := m.ints[key]; ok {
		return v
	}
	return def
}

func (m mapSettings) GetBool(key string, def bool) bool {
	if v, ok := m.bools[key]; ok {
		return v
	}
	return def
}

// TestDefaultRulesMatchDocumentedTable pins every documented default so a
// stray edit cannot silently change what the engine enforces out of the box:
// risk threshold 40, instant-ban ratio 2, per-signal threshold/window/weight,
// ban tiers 1/24/168/720 hours and a 30-day event retention.
func TestDefaultRulesMatchDocumentedTable(t *testing.T) {
	r := DefaultRules()

	if r.RiskThreshold != 40 {
		t.Errorf("RiskThreshold = %d, want 40", r.RiskThreshold)
	}
	if r.InstantBanRatio != 2 {
		t.Errorf("InstantBanRatio = %d, want 2", r.InstantBanRatio)
	}
	if !r.FailOpen {
		t.Errorf("FailOpen = false, want true")
	}

	want := map[Signal][3]int{ // threshold, window seconds, weight
		SigDistinctPorts:  {5, 60, 35},
		SigNewConnections: {30, 60, 20},
		SigIncompleteHS:   {10, 60, 20},
		SigConnectAborts:  {12, 60, 15},
		SigMalformed:      {15, 300, 15},
		SigSuspiciousPath: {5, 60, 25},
		SigAuthFailures:   {10, 300, 20},
	}
	for sig, wantVals := range want {
		got, ok := r.Signals[sig]
		if !ok {
			t.Fatalf("SignalRules missing entry for %q", sig)
			continue
		}
		if got.Threshold != wantVals[0] || got.WindowSeconds != wantVals[1] || got.Weight != wantVals[2] {
			t.Errorf("%s = {threshold %d, window %ds, weight %d}, want {%d, %d, %d}",
				sig, got.Threshold, got.WindowSeconds, got.Weight, wantVals[0], wantVals[1], wantVals[2])
		}
	}
	if len(r.Signals) != len(want) {
		t.Errorf("Rules.Signals has %d entries, want %d", len(r.Signals), len(want))
	}

	if r.BanTier1Hours != 1 || r.BanTier2Hours != 24 || r.BanTier3Hours != 168 || r.BanTier4Hours != 720 {
		t.Errorf("ban tiers = %d/%d/%d/%d hours, want 1/24/168/720",
			r.BanTier1Hours, r.BanTier2Hours, r.BanTier3Hours, r.BanTier4Hours)
	}
	if r.EventsRetentionDays != 30 {
		t.Errorf("EventsRetentionDays = %d, want 30", r.EventsRetentionDays)
	}
}

func TestLoadRulesWithoutSourceReturnsDefaults(t *testing.T) {
	if r := LoadRules(nil); !reflect.DeepEqual(r, DefaultRules()) {
		t.Fatalf("LoadRules(nil) = %+v, want the documented defaults", r)
	}

	// The loader itself must be nil-safe too (used bare in tests).
	var loader *RulesLoader
	if r := loader.Load(); !reflect.DeepEqual(r, DefaultRules()) {
		t.Fatalf("nil RulesLoader.Load() = %+v, want the documented defaults", r)
	}
	if r := NewRulesLoader(nil).Load(); !reflect.DeepEqual(r, DefaultRules()) {
		t.Fatalf("NewRulesLoader(nil).Load() = %+v, want the documented defaults", r)
	}
}

func TestLoadRulesUsesCustomValues(t *testing.T) {
	s := mapSettings{
		ints: map[string]int{
			"security_risk_threshold":                     100,
			"security_instant_ban_ratio":                  3,
			"security_sig_auth_failures_threshold":        3,
			"security_sig_auth_failures_window_seconds":   600,
			"security_weight_distinct_ports":              50,
			"security_sig_new_connections_window_seconds": 120,
			"security_ban_tier_2_hours":                   48,
			"security_events_retention_days":              7,
		},
		bools: map[string]bool{"security_fail_open": false},
	}
	r := LoadRules(s)

	if r.RiskThreshold != 100 {
		t.Errorf("RiskThreshold = %d, want 100", r.RiskThreshold)
	}
	if r.InstantBanRatio != 3 {
		t.Errorf("InstantBanRatio = %d, want 3", r.InstantBanRatio)
	}
	if r.FailOpen {
		t.Errorf("FailOpen = true, want false")
	}
	if got := r.Signals[SigAuthFailures]; got.Threshold != 3 || got.WindowSeconds != 600 {
		t.Errorf("auth_failures = {threshold %d, window %ds}, want {3, 600}", got.Threshold, got.WindowSeconds)
	}
	if got := r.Signals[SigDistinctPorts]; got.Weight != 50 {
		t.Errorf("distinct_ports weight = %d, want 50", got.Weight)
	}
	if got := r.Signals[SigNewConnections]; got.WindowSeconds != 120 {
		t.Errorf("new_connections window = %ds, want 120", got.WindowSeconds)
	}
	if r.BanTier2Hours != 48 {
		t.Errorf("BanTier2Hours = %d, want 48", r.BanTier2Hours)
	}
	if r.EventsRetentionDays != 7 {
		t.Errorf("EventsRetentionDays = %d, want 7", r.EventsRetentionDays)
	}

	// Keys that were not overridden keep the documented defaults.
	if got := r.Signals[SigSuspiciousPath]; got.Threshold != 5 || got.Weight != 25 {
		t.Errorf("suspicious_paths = {threshold %d, weight %d}, want the defaults {5, 25}", got.Threshold, got.Weight)
	}
	if r.BanTier1Hours != 1 {
		t.Errorf("BanTier1Hours = %d, want the default 1", r.BanTier1Hours)
	}
}

// TestLoadRulesClampsZeroAndNegativeValues pins the clamps that keep a bad
// admin edit from wedging the engine: a 0 threshold would instantly ban on the
// first event, a 0 window would never expire, and a 0 ban tier would read as
// an accidental permanent ban. Weights are special: they clamp to 0 (not 1),
// so a signal can be turned off but never made negative.
func TestLoadRulesClampsZeroAndNegativeValues(t *testing.T) {
	s := mapSettings{ints: map[string]int{
		"security_risk_threshold":                       -5,
		"security_instant_ban_ratio":                    0,
		"security_sig_distinct_ports_threshold":         0,
		"security_sig_new_connections_threshold":        -1,
		"security_sig_new_connections_window_seconds":   0,
		"security_sig_malformed_preauth_window_seconds": -60,
		"security_weight_suspicious_paths":              -3,
		"security_ban_tier_1_hours":                     0,
		"security_ban_tier_3_hours":                     -10,
		"security_events_retention_days":                0,
	}}
	r := LoadRules(s)

	if r.RiskThreshold != 1 {
		t.Errorf("RiskThreshold = %d, want clamped to 1", r.RiskThreshold)
	}
	if r.InstantBanRatio != 1 {
		t.Errorf("InstantBanRatio = %d, want clamped to 1", r.InstantBanRatio)
	}
	if got := r.Signals[SigDistinctPorts]; got.Threshold != 1 {
		t.Errorf("distinct_ports threshold = %d, want clamped to 1", got.Threshold)
	}
	if got := r.Signals[SigNewConnections]; got.Threshold != 1 || got.WindowSeconds != 1 {
		t.Errorf("new_connections = {threshold %d, window %d}, want both clamped to 1", got.Threshold, got.WindowSeconds)
	}
	if got := r.Signals[SigMalformed]; got.WindowSeconds != 1 {
		t.Errorf("malformed_preauth window = %d, want clamped to 1", got.WindowSeconds)
	}
	if got := r.Signals[SigSuspiciousPath]; got.Weight != 0 {
		t.Errorf("suspicious_paths weight = %d, want clamped to 0", got.Weight)
	}
	if r.BanTier1Hours != 1 || r.BanTier3Hours != 1 {
		t.Errorf("ban tiers 1/3 = %d/%d, want both clamped to 1", r.BanTier1Hours, r.BanTier3Hours)
	}
	if r.EventsRetentionDays != 1 {
		t.Errorf("EventsRetentionDays = %d, want clamped to 1", r.EventsRetentionDays)
	}
}

// TestRulesLoaderReloadsOnEveryLoad pins the fresh-per-evaluation contract:
// admin edits take effect on the next Load without a restart.
func TestRulesLoaderReloadsOnEveryLoad(t *testing.T) {
	m := mapSettings{ints: map[string]int{}}
	l := NewRulesLoader(m)

	if got := l.Load().RiskThreshold; got != 40 {
		t.Fatalf("initial RiskThreshold = %d, want the default 40", got)
	}
	m.ints["security_risk_threshold"] = 90
	if got := l.Load().RiskThreshold; got != 90 {
		t.Fatalf("RiskThreshold after edit = %d, want 90 (settings must be re-read per Load)", got)
	}
	m.ints["security_risk_threshold"] = 15
	if got := l.Load().RiskThreshold; got != 15 {
		t.Fatalf("RiskThreshold after second edit = %d, want 15", got)
	}
}
