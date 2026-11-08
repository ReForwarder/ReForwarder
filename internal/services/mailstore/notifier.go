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

package mailstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/redis/go-redis/v9"
)

// ChangeNotifier (OB-4 wave W1, ADR-004 decision 7): per-folder change
// notifications for IDLE. Every mutation the mailstore commits (append,
// expunge, flags, move, copy) fans out to the sessions watching that folder —
// in-process first, and through Redis pub/sub so a delivery executed by a
// remote worker wakes sessions connected to any replica. The propagation
// shape deliberately follows the ban-propagation pattern
// (internal/services/security/propagate.go): publish a JSON envelope on one
// namespaced channel, every replica subscribes and applies locally, nil Redis
// degrades to local-only (single-process deployments, tests).
//
// Delivery is best effort: a subscriber that is not listening when an event
// fires misses it, and a subscriber whose buffer is full misses that one
// event — the database stays the source of truth; IDLE is a hint to emit
// EXISTS/EXPUNGE responses, which the servers re-derive from the store.

// MailChannel is the Redis pub/sub channel folder changes fan out on.
const MailChannel = "posta:mailchg"

// ChangeKind identifies what changed in a folder.
type ChangeKind string

const (
	// KindAppend: one or more messages appeared (IMAP EXISTS).
	KindAppend ChangeKind = "append"
	// KindExpunge: the listed UIDs were permanently removed (IMAP EXPUNGE).
	KindExpunge ChangeKind = "expunge"
	// KindFlags: flags changed on one message (IMAP FETCH FLAGS update).
	KindFlags ChangeKind = "flags"
)

// ChangeEvent is one folder change. UID/MessageID/ModSeq/Flags are set
// depending on Kind; ExpungedUIDs carries the aggregate expunge victim list.
type ChangeEvent struct {
	FolderID     uint       `json:"folder_id"`
	Kind         ChangeKind `json:"kind"`
	MessageID    uint       `json:"message_id,omitempty"`
	UID          uint32     `json:"uid,omitempty"`
	ModSeq       uint64     `json:"mod_seq,omitempty"`
	Flags        uint32     `json:"flags,omitempty"`
	ExpungedUIDs []uint32   `json:"expunged_uids,omitempty"`
}

// subscriberBuffer is how many undelivered events one subscriber may hold
// before further events are dropped for it (same shape as the realtime bus).
const subscriberBuffer = 64

// changeHub is the in-process per-folder fan-out.
type changeHub struct {
	mu   sync.RWMutex
	subs map[uint]map[uint64]chan ChangeEvent
	next uint64
}

func newChangeHub() *changeHub {
	return &changeHub{subs: make(map[uint]map[uint64]chan ChangeEvent)}
}

func (h *changeHub) add(folderID uint) (uint64, <-chan ChangeEvent) {
	ch := make(chan ChangeEvent, subscriberBuffer)
	h.mu.Lock()
	defer h.mu.Unlock()
	subs, ok := h.subs[folderID]
	if !ok {
		subs = make(map[uint64]chan ChangeEvent)
		h.subs[folderID] = subs
	}
	id := h.next
	h.next++
	subs[id] = ch
	return id, ch
}

func (h *changeHub) remove(folderID uint, id uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	subs, ok := h.subs[folderID]
	if !ok {
		return false
	}
	if ch, ok := subs[id]; ok {
		delete(subs, id)
		close(ch)
	}
	if len(subs) == 0 {
		delete(h.subs, folderID)
		return true
	}
	return false
}

func (h *changeHub) deliver(ev ChangeEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.subs[ev.FolderID] {
		select {
		case ch <- ev:
		default:
			// Slow subscriber: drop rather than stall every session.
		}
	}
}

// ChangeNotifier fans out folder changes in-process and (optionally) over
// Redis. Safe for concurrent use.
type ChangeNotifier struct {
	// pub is the publish-side Redis handle (Cmdable, so a fake can stand in
	// for tests). nil = local-only.
	pub redis.Cmdable
	hub *changeHub

	// sub is the concrete client used for the subscription loop (Run);
	// go-redis exposes Subscribe only on the concrete client.
	sub *redis.Client

	mu      sync.Mutex
	ps      *redis.PubSub
	started bool
}

// NewChangeNotifier returns a local-only notifier (single-process
// deployments, tests).
func NewChangeNotifier() *ChangeNotifier {
	return &ChangeNotifier{hub: newChangeHub()}
}

// NewRedisChangeNotifier returns a notifier that fans out through Redis
// pub/sub (plus the local hub, which carries every event — local subscribers
// must not depend on the Redis round-trip). A nil client degrades to
// NewChangeNotifier, mirroring the ban Propagator's nil-Redis stance.
func NewRedisChangeNotifier(rdb *redis.Client) *ChangeNotifier {
	if rdb == nil {
		return NewChangeNotifier()
	}
	return &ChangeNotifier{pub: rdb, sub: rdb, hub: newChangeHub()}
}

// Notify fans one event out to local subscribers and, when Redis is wired,
// to every replica subscribed to MailChannel. Publish errors are returned
// (the caller logs them); the local hub is always updated first, so a Redis
// outage never delays same-process sessions.
func (n *ChangeNotifier) Notify(ctx context.Context, ev ChangeEvent) error {
	n.hub.deliver(ev)
	if n == nil || n.pub == nil {
		return nil
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("mailstore: marshal change event: %w", err)
	}
	return n.pub.Publish(ctx, MailChannel, payload).Err()
}

// Subscribe registers a local subscriber for one folder's changes and returns
// the event channel plus the unsubscribe function (safe to call once).
func (n *ChangeNotifier) Subscribe(ctx context.Context, folderID uint) (<-chan ChangeEvent, func()) {
	if n == nil {
		ch := make(chan ChangeEvent)
		close(ch)
		return ch, func() {}
	}
	id, ch := n.hub.add(folderID)
	if n.pub != nil {
		n.redisSubscribe(ctx, folderID)
	}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			if n.hub.remove(folderID, id) && n.pub != nil {
				n.redisUnsubscribe(folderID)
			}
		})
	}
}

// redisSubscribe adds the folder to the process-wide pub/sub connection,
// opening it and starting the reader on first use (same lazy shape as
// realtime.redisBus).
func (n *ChangeNotifier) redisSubscribe(ctx context.Context, folderID uint) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ps == nil {
		n.ps = n.sub.Subscribe(context.Background())
	}
	if err := n.ps.Subscribe(ctx, MailChannel); err != nil {
		logger.Warn("mailstore: redis change subscribe failed", "error", err.Error())
	}
	if !n.started {
		n.started = true
		go n.read(n.ps)
	}
}

func (n *ChangeNotifier) redisUnsubscribe(folderID uint) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ps == nil {
		return
	}
	if err := n.ps.Unsubscribe(context.Background(), MailChannel); err != nil {
		logger.Warn("mailstore: redis change unsubscribe failed", "error", err.Error())
	}
}

// read relays every Redis message to the local hub. go-redis reconnects and
// resubscribes on its own; this loop only ends if the PubSub closes. Remote
// events are delivered LOCALLY ONLY — never re-published — because every
// replica publishes its own mutations and the channel would otherwise echo
// forever.
func (n *ChangeNotifier) read(ps *redis.PubSub) {
	for msg := range ps.Channel() {
		n.applyRemote([]byte(msg.Payload))
	}
}

// applyRemote decodes a wire payload and delivers it to local subscribers.
// It is the shared core of read(); exposing it keeps the wire contract
// (encode via Notify, decode here) testable without a live Redis.
func (n *ChangeNotifier) applyRemote(payload []byte) {
	var ev ChangeEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		logger.Warn("mailstore: dropping malformed change event", "error", err.Error())
		return
	}
	n.hub.deliver(ev)
}

// Run keeps the subscription alive until ctx is cancelled. With no Redis
// client wired it exits immediately. go-redis transparently reconnects; the
// loop simply blocks on the message channel.
func (n *ChangeNotifier) Run(ctx context.Context) {
	if n == nil || n.sub == nil {
		return
	}
	<-ctx.Done()
	n.mu.Lock()
	if n.ps != nil {
		_ = n.ps.Close()
	}
	n.mu.Unlock()
}

// SetChangeNotifier wires the optional change notifier into the service.
// nil disables notifications (all emit paths no-op). It must be called before
// the service is used by sessions; emits are best-effort and never fail the
// mutation that triggered them.
func (s *Service) SetChangeNotifier(n *ChangeNotifier) {
	s.notifier = n
}

// ChangeNotifier returns the wired notifier (nil when unset). The mail
// protocol servers (OB-4) subscribe IDLE sessions through this same
// instance — every store emit lands on it, so subscribers must share it
// rather than build their own.
func (s *Service) ChangeNotifier() *ChangeNotifier {
	return s.notifier
}

// notifyChange emits one event; best-effort by contract — the mutation is
// already committed when this runs, so a fan-out failure must never surface
// as an append/expunge error.
func (s *Service) notifyChange(ev ChangeEvent) {
	if s.notifier == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.notifier.Notify(ctx, ev); err != nil {
		logger.Warn("mailstore: change notification failed",
			"folder_id", ev.FolderID, "kind", string(ev.Kind), "error", err.Error())
	}
}
