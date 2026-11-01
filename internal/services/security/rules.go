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
	"github.com/goposta/posta/internal/services/settings"
)

// SettingsSource is the read side of the settings system the security engine
// consumes. *settings.Provider satisfies it directly; reads are cached by the
// provider (30s TTL) so per-evaluation reloads are cheap and admin changes
// apply without a restart.
type SettingsSource interface {
	GetInt(key string, def int) int
	GetBool(key string, def bool) bool
}

// Compile-time guarantee that the live settings provider can be handed to
// the engine and detector as their SettingsSource.
var _ SettingsSource = (*settings.Provider)(nil)

// SignalRule is the per-signal configuration: how many events within the
// window count as "exceeded" and how much the signal contributes to the
// risk score.
type SignalRule struct {
	Threshold     int
	WindowSeconds int
	Weight        int
}

// Rules is a snapshot of the security_* settings, reloaded from the
// SettingsSource before every evaluation so admin edits take effect quickly.
type Rules struct {
	// RiskThreshold bans an IP once the sum of exceeded-signal weights
	// reaches it (security_risk_threshold, default 40).
	RiskThreshold int
	// InstantBanRatio bans an IP when any single signal reaches
	// threshold * InstantBanRatio (security_instant_ban_ratio, default 2).
	InstantBanRatio int
	// FailOpen mirrors security_fail_open (default true). Informational:
	// the Detector uses the failOpen flag given to NewDetector.
	FailOpen bool

	// Signals holds threshold / window / weight per signal.
	Signals map[Signal]SignalRule

	// Automatic ban TTL escalation by offense count
	// (security_ban_tier_N_hours, defaults 1 / 24 / 168 / 720).
	BanTier1Hours int
	BanTier2Hours int
	BanTier3Hours int
	BanTier4Hours int

	// EventsRetentionDays is how long security_events rows are kept
	// (security_events_retention_days, default 30).
	EventsRetentionDays int
}

// DefaultRules returns the documented defaults for every security_* setting.
// It is the fallback when no settings source is available and the baseline
// LoadRules starts from.
func DefaultRules() Rules {
	return Rules{
		RiskThreshold:   40,
		InstantBanRatio: 2,
		FailOpen:        true,
		Signals: map[Signal]SignalRule{
			SigDistinctPorts:  {Threshold: 5, WindowSeconds: 60, Weight: 35},
			SigNewConnections: {Threshold: 30, WindowSeconds: 60, Weight: 20},
			SigIncompleteHS:   {Threshold: 10, WindowSeconds: 60, Weight: 20},
			SigConnectAborts:  {Threshold: 12, WindowSeconds: 60, Weight: 15},
			SigMalformed:      {Threshold: 15, WindowSeconds: 300, Weight: 15},
			SigSuspiciousPath: {Threshold: 5, WindowSeconds: 60, Weight: 25},
			SigAuthFailures:   {Threshold: 10, WindowSeconds: 300, Weight: 20},
		},
		BanTier1Hours:       1,
		BanTier2Hours:       24,
		BanTier3Hours:       168,
		BanTier4Hours:       720,
		EventsRetentionDays: 30,
	}
}

// LoadRules reads every security_* key from the settings source, falling back
// to the documented default for missing or invalid values. Values are clamped
// to sane minimums so a bad admin edit cannot disable or wedge the engine
// (a 0 threshold would otherwise instantly ban, a 0 window would never
// expire, a 0 ban tier would become a permanent ban).
func LoadRules(s SettingsSource) Rules {
	r := DefaultRules()
	if s == nil {
		return r
	}

	r.RiskThreshold = max(s.GetInt("security_risk_threshold", r.RiskThreshold), 1)
	r.InstantBanRatio = max(s.GetInt("security_instant_ban_ratio", r.InstantBanRatio), 1)
	r.FailOpen = s.GetBool("security_fail_open", r.FailOpen)

	for _, sig := range AllSignals() {
		def := r.Signals[sig]
		r.Signals[sig] = SignalRule{
			Threshold:     max(s.GetInt("security_sig_"+string(sig)+"_threshold", def.Threshold), 1),
			WindowSeconds: max(s.GetInt("security_sig_"+string(sig)+"_window_seconds", def.WindowSeconds), 1),
			Weight:        max(s.GetInt("security_weight_"+string(sig), def.Weight), 0),
		}
	}

	r.BanTier1Hours = max(s.GetInt("security_ban_tier_1_hours", r.BanTier1Hours), 1)
	r.BanTier2Hours = max(s.GetInt("security_ban_tier_2_hours", r.BanTier2Hours), 1)
	r.BanTier3Hours = max(s.GetInt("security_ban_tier_3_hours", r.BanTier3Hours), 1)
	r.BanTier4Hours = max(s.GetInt("security_ban_tier_4_hours", r.BanTier4Hours), 1)
	r.EventsRetentionDays = max(s.GetInt("security_events_retention_days", r.EventsRetentionDays), 1)

	return r
}

// RulesLoader wraps a SettingsSource and re-reads Rules on every Load call.
// It is deliberately cheap: settings.Provider caches values in memory with a
// 30-second TTL, so loading per evaluation costs a few map lookups while
// keeping admin changes responsive.
type RulesLoader struct {
	src SettingsSource
}

// NewRulesLoader returns a loader over the given settings source. A nil
// source makes Load fall back to DefaultRules.
func NewRulesLoader(src SettingsSource) *RulesLoader {
	return &RulesLoader{src: src}
}

// Load returns a fresh Rules snapshot.
func (l *RulesLoader) Load() Rules {
	if l == nil || l.src == nil {
		return DefaultRules()
	}
	return LoadRules(l.src)
}
