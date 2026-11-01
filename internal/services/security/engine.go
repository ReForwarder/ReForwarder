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
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Engine is the security engine's assembly point. It wires the in-memory ban
// list, the Redis propagator, the ban service, the detector and (when an
// event bus is available) the realtime login-failure feed, and owns their
// background loops.
//
// Enforcement (W3e) consumes it through the exported fields: Engine.Bans for
// IsAllowed/IsBlocked/ManualBan/Unban/ExtendBan and Engine.Detect for the
// Record* signal methods. Engine.Enabled gates the whole engine
// (POSTA_SECURITY_ENABLED).
type Engine struct {
	Bans   *BanService
	Detect *Detector

	cfg      *config.Config
	settings SettingsSource

	list *BanList
	prop *Propagator
	feed *Feed

	mu     sync.Mutex
	cancel context.CancelFunc
}

// NewEngine wires the security engine. settings is the settings provider
// (rules and ban-tier durations are read from it); bus is the realtime event
// bus — when nil, no login-failure feed is started. Loopback is seeded into
// the allowlist here (the allowlist-wins seed for 127.0.0.0/8 and ::1).
func NewEngine(db *gorm.DB, rdb *redis.Client, cfg *config.Config, settings SettingsSource, bus EventPublisher) *Engine {
	if settings == nil {
		settings = nullSettingsSource{}
	}

	list := NewBanList(true)
	prop := NewPropagator(rdb, list, db)

	bans := NewBanService(db, list, prop, time.Now)
	loader := NewRulesLoader(settings)
	bans.TierHours = tierHoursFunc(loader)

	failOpen := true
	if cfg != nil {
		failOpen = cfg.SecurityFailOpen
	}
	eventRepo := repositories.NewSecurityEventRepository(db)
	detector := NewDetector(rdb, loader, bans, NewEventRecorder(eventRepo), time.Now, failOpen)

	e := &Engine{
		Bans:     bans,
		Detect:   detector,
		cfg:      cfg,
		settings: settings,
		list:     list,
		prop:     prop,
	}
	if bus != nil {
		e.feed = NewFeed(bus, detector)
	}
	return e
}

// tierHoursFunc resolves the automatic ban TTL through the rules loader:
// tier = min(offense, 4), with the security_ban_tier_* settings read fresh on
// every placement so admin changes apply immediately. A zero tier value lets
// BanService treat the ban as permanent.
func tierHoursFunc(loader *RulesLoader) func(offense int) int {
	return func(offense int) int {
		tier := offense
		if tier < 1 {
			tier = 1
		}
		if tier > 4 {
			tier = 4
		}
		r := loader.Load()
		switch tier {
		case 1:
			return r.BanTier1Hours
		case 2:
			return r.BanTier2Hours
		case 3:
			return r.BanTier3Hours
		default:
			return r.BanTier4Hours
		}
	}
}

// nullSettingsSource answers every settings lookup with the caller's
// default. It backs NewEngine when no settings provider is wired (tests,
// minimal embeds).
type nullSettingsSource struct{}

func (nullSettingsSource) GetInt(_ string, def int) int { return def }
func (nullSettingsSource) GetBool(_ string, def bool) bool { return def }

// Enabled reports whether the security engine is switched on
// (POSTA_SECURITY_ENABLED, default true). Enforcement and Start both check
// it.
func (e *Engine) Enabled() bool {
	return e.cfg == nil || e.cfg.SecurityEnabled
}

// FailOpen reports what the detector does when Redis is unreachable: true
// (the default) lets the connection through, false rejects it.
func (e *Engine) FailOpen() bool {
	return e.cfg == nil || e.cfg.SecurityFailOpen
}

// Start brings the engine up: a full ban-list resync from the database
// (failing startup when the source of truth is unreachable — the caller runs
// Start in a goroutine and its error surfaces in the logs), then the
// propagation and login-failure feed loops in the background. Everything
// stops when ctx is cancelled or Stop is called. Starting twice is a no-op.
func (e *Engine) Start(ctx context.Context) error {
	if !e.Enabled() {
		logger.Info("security: engine disabled; not starting")
		return nil
	}

	e.mu.Lock()
	if e.cancel != nil {
		e.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.mu.Unlock()

	if err := e.prop.ResyncFromDB(runCtx); err != nil {
		e.mu.Lock()
		e.cancel = nil
		e.mu.Unlock()
		cancel()
		return fmt.Errorf("security: initial ban list resync: %w", err)
	}

	go e.prop.Run(runCtx)
	if e.feed != nil {
		go func() {
			if err := e.feed.Start(runCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("security: login-failure feed stopped", "error", err)
			}
		}()
	}
	return nil
}

// Stop cancels the engine's background loops (propagation, feed). The ban
// list stays valid: enforcement keeps working from the last synced state
// until the process exits.
func (e *Engine) Stop() {
	e.mu.Lock()
	cancel := e.cancel
	e.cancel = nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
