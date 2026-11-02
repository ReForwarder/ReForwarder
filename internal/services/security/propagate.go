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
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// BansChannel is the Redis pub/sub channel ban changes fan out on. Every
// replica publishes ban placements/lifts and subscribes to keep its in-memory
// BanList in step.
const BansChannel = "posta:sec:bans"

// Ban propagation actions carried in the message envelope.
const (
	ActionBan      = "ban"
	ActionUnban    = "unban"
	ActionAllow    = "allow"
	ActionDisallow = "disallow"
	ActionReload   = "reload"
)

// Propagator keeps the in-memory BanList in step across replicas: it publishes
// ban changes on Redis pub/sub and applies the same messages from other
// replicas. It is also the bridge back to the source of truth — ResyncFromDB
// rebuilds the whole list from the database on startup and after every Redis
// hiccup.
type Propagator struct {
	rdb  *redis.Client
	list *BanList
	db   *gorm.DB

	banRepo   *repositories.SecurityBanRepository
	allowRepo *repositories.SecurityAllowlistRepository

	// resyncMu serializes resyncs triggered concurrently by the subscribe
	// loop (reconnect, allowlist change) and engine startup.
	resyncMu sync.Mutex
}

// NewPropagator returns a propagator over the given Redis client, ban list
// and database. A nil Redis client is tolerated: Publish becomes a no-op and
// Run exits immediately, leaving the local BanService calls as the only
// source of ban changes (single-replica deployments, tests).
func NewPropagator(rdb *redis.Client, list *BanList, db *gorm.DB) *Propagator {
	return &Propagator{
		rdb:       rdb,
		list:      list,
		db:        db,
		banRepo:   repositories.NewSecurityBanRepository(db),
		allowRepo: repositories.NewSecurityAllowlistRepository(db),
	}
}

// banMessage is the wire format on BansChannel: {"action": "...", "payload":
// ...}. The payload is the affected ban row for ban/unban; allow/disallow/
// reload carry no data the subscriber uses (see apply).
type banMessage struct {
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload"`
}

// Publish sends a ban change on the bans channel. Errors are returned to the
// caller (BanService logs them and carries on: the change is already applied
// locally and to the database).
func (p *Propagator) Publish(ctx context.Context, action string, payload any) error {
	if p == nil || p.rdb == nil {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("security: marshal %q ban payload: %w", action, err)
	}
	env, err := json.Marshal(banMessage{Action: action, Payload: raw})
	if err != nil {
		return fmt.Errorf("security: marshal %q ban message: %w", action, err)
	}
	return p.rdb.Publish(ctx, BansChannel, env).Err()
}

// Run subscribes to the bans channel until ctx is cancelled, applying every
// message to the BanList. Any Redis error (failed subscribe, closed channel)
// is logged and retried after a 5s backoff, with a full ResyncFromDB on each
// reconnect so bans published while disconnected are not lost.
//
// go-redis also reconnects and resubscribes transparently on its own; messages
// published during such a silent gap would be missed without any error
// surfacing here. A periodic full resync runs as a reconciliation safety net
// so those gaps self-heal within minutes.
func (p *Propagator) Run(ctx context.Context) {
	if p == nil || p.rdb == nil {
		logger.Warn("security: no redis client; ban propagation disabled")
		return
	}

	const (
		backoff        = 5 * time.Second
		reconcileEvery = 5 * time.Minute
	)
	reconcile := time.NewTicker(reconcileEvery)
	defer reconcile.Stop()

	for {
		if ctx.Err() != nil {
			return
		}
		err := p.subscribeLoop(ctx, reconcile.C)
		if ctx.Err() != nil {
			return
		}
		logger.Error("security: ban propagation interrupted; retrying in 5s", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if err := p.ResyncFromDB(ctx); err != nil {
			logger.Warn("security: resync after reconnect failed", "error", err)
		}
	}
}

// subscribeLoop consumes messages until the context is cancelled or the
// pub/sub channel closes.
func (p *Propagator) subscribeLoop(ctx context.Context, reconcile <-chan time.Time) error {
	ps := p.rdb.Subscribe(ctx, BansChannel)
	defer func() { _ = ps.Close() }()
	msgs := ps.Channel()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reconcile:
			if err := p.ResyncFromDB(ctx); err != nil {
				logger.Warn("security: periodic resync failed", "error", err)
			}
		case msg, ok := <-msgs:
			if !ok {
				return errors.New("redis pub/sub channel closed")
			}
			p.apply(ctx, msg.Payload)
		}
	}
}

// apply applies one published message to the BanList.
//
// "ban" upserts the carried row; "unban" removes it by ID. "allow",
// "disallow" and "reload" trigger a full resync from the database: the
// BanList has no incremental allowlist mutation (Replace is the only
// allowlist entry point), so a resync is the only way to apply allowlist
// changes, and the payload is ignored.
func (p *Propagator) apply(ctx context.Context, raw string) {
	var msg banMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		logger.Warn("security: dropping malformed ban message", "error", err)
		return
	}

	switch msg.Action {
	case ActionBan:
		var ban models.SecurityBan
		if err := json.Unmarshal(msg.Payload, &ban); err != nil {
			logger.Warn("security: dropping malformed ban payload", "error", err)
			return
		}
		p.list.UpsertBan(ban)
	case ActionUnban:
		var ban models.SecurityBan
		if err := json.Unmarshal(msg.Payload, &ban); err == nil && ban.ID != 0 {
			p.list.RemoveBan(ban.ID)
			return
		}
		var ref struct {
			ID uint `json:"id"`
		}
		if err := json.Unmarshal(msg.Payload, &ref); err != nil || ref.ID == 0 {
			logger.Warn("security: dropping unban message without a ban id")
			return
		}
		p.list.RemoveBan(ref.ID)
	case ActionAllow, ActionDisallow, ActionReload:
		if err := p.ResyncFromDB(ctx); err != nil {
			logger.Warn("security: resync after allowlist change failed", "error", err)
		}
	default:
		logger.Warn("security: ignoring unknown ban message action", "action", msg.Action)
	}
}

// ResyncFromDB rebuilds the entire BanList from the source of truth: every
// active ban plus the whole allowlist. It runs on startup, after every
// reconnection and after allowlist changes.
func (p *Propagator) ResyncFromDB(ctx context.Context) error {
	if p == nil || p.db == nil {
		return errors.New("security: propagator has no database handle")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	p.resyncMu.Lock()
	defer p.resyncMu.Unlock()

	bans, err := p.banRepo.FindAllActive()
	if err != nil {
		return fmt.Errorf("security: load active bans: %w", err)
	}
	allows, err := p.allowRepo.FindAll()
	if err != nil {
		return fmt.Errorf("security: load allowlist: %w", err)
	}
	p.list.Replace(bans, allows)
	return nil
}
