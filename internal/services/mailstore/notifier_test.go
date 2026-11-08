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
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/redis/go-redis/v9"
)

// waitForEvent drains one event from a subscriber channel with a deadline.
func waitForEvent(t *testing.T, ch <-chan ChangeEvent) ChangeEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for a change event")
		return ChangeEvent{}
	}
}

func assertEmpty(t *testing.T, ch <-chan ChangeEvent) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(30 * time.Millisecond):
	}
}

// TestChangeNotifierLocalFanOut: two subscribers on one folder both receive
// every event; a subscriber on another folder receives none; unsubscribe
// stops delivery.
func TestChangeNotifierLocalFanOut(t *testing.T) {
	n := NewChangeNotifier()
	ctx := context.Background()
	chA, cancelA := n.Subscribe(ctx, 10)
	chB, cancelB := n.Subscribe(ctx, 10)
	chOther, cancelOther := n.Subscribe(ctx, 11)

	if err := n.Notify(ctx, ChangeEvent{FolderID: 10, Kind: KindAppend, UID: 5}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	evA := waitForEvent(t, chA)
	evB := waitForEvent(t, chB)
	if evA.UID != 5 || evB.UID != 5 || evA.Kind != KindAppend {
		t.Fatalf("fan-out events wrong: %+v / %+v", evA, evB)
	}
	assertEmpty(t, chOther)

	cancelA()
	// Unsubscribe closes the channel: the receive returns the zero value
	// with ok == false.
	if ev, ok := <-chA; ok {
		t.Fatalf("channel still open after unsubscribe (event %+v)", ev)
	}
	n.Notify(ctx, ChangeEvent{FolderID: 10, Kind: KindExpunge})
	if ev := waitForEvent(t, chB); ev.Kind != KindExpunge {
		t.Fatalf("post-cancel event on B wrong: %+v", ev)
	}
	cancelB()
	cancelOther()
}

// notifierFakeRedis captures publishes for the wire-contract test (the
// limiterFakeRedis pattern: embed redis.Cmdable, override what the code
// calls — no Redis test dependency).
type notifierFakeRedis struct {
	redis.Cmdable
	mu       sync.Mutex
	channel  string
	payloads [][]byte
}

func (f *notifierFakeRedis) Publish(_ context.Context, channel string, payload any) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	cmd.SetVal(1)
	raw, _ := payload.([]byte)
	f.mu.Lock()
	f.channel = channel
	f.payloads = append(f.payloads, raw)
	f.mu.Unlock()
	return cmd
}

// TestChangeNotifierRedisWireContract: the exact wire path a remote worker's
// event takes — Notify encodes and publishes on MailChannel; a replica's
// applyRemote decodes the payload and delivers it to local subscribers. No
// live Redis needed: the fake captures the publish, the second "process"
// replays the same bytes through the same decode/deliver core the Run loop
// uses.
func TestChangeNotifierRedisWireContract(t *testing.T) {
	fake := &notifierFakeRedis{}
	pub := &ChangeNotifier{pub: fake, hub: newChangeHub()}
	ctx := context.Background()

	if err := pub.Notify(ctx, ChangeEvent{FolderID: 77, Kind: KindFlags, UID: 3, MessageID: 9, ModSeq: 12, Flags: models.FlagSeen}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	fake.mu.Lock()
	channel := fake.channel
	payload := fake.payloads[0]
	fake.mu.Unlock()
	if channel != MailChannel {
		t.Fatalf("published on %q, want %q", channel, MailChannel)
	}

	// The replica ("process B"): local subscriber + the shared decode/deliver core.
	b := &ChangeNotifier{hub: newChangeHub()}
	chB, cancelB := b.Subscribe(ctx, 77)
	defer cancelB()
	b.applyRemote(payload)
	ev := waitForEvent(t, chB)
	if ev.Kind != KindFlags || ev.UID != 3 || ev.ModSeq != 12 || ev.Flags != models.FlagSeen || ev.FolderID != 77 {
		t.Fatalf("remote event decoded wrong: %+v", ev)
	}

	// Malformed payloads are dropped, never panic.
	b.applyRemote([]byte("not json"))
	assertEmpty(t, chB)

	// Loop prevention: applying a remote event must not re-publish (there is
	// no publish handle on B at all — the reader loop delivers locally only).
	fake.mu.Lock()
	count := len(fake.payloads)
	fake.mu.Unlock()
	b.applyRemote(payload)
	fake.mu.Lock()
	after := len(fake.payloads)
	fake.mu.Unlock()
	if after != count {
		t.Fatalf("applyRemote re-published (%d → %d events): echo loop", count, after)
	}
}

// TestServiceEmitsChangeEvents proves the mutation paths fan out through the
// wired notifier: append → KindAppend, flags → KindFlags, expunge →
// KindExpunge with the victim UIDs, move → append(into)+expunge(from).
func TestServiceEmitsChangeEvents(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "notify-svc@post", 0)
	inbox := inboxFolder(t, svc, mb)
	archive, err := svc.CreateFolder(mb.ID, 1, 0, "", "Archive")
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	n := NewChangeNotifier()
	svc.SetChangeNotifier(n)
	ch, cancel := n.Subscribe(context.Background(), inbox.ID)
	defer cancel()
	chTarget, cancelTarget := n.Subscribe(context.Background(), archive.ID)
	defer cancelTarget()

	// Append → KindAppend with the claimed UID.
	res, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("notify-one", "a@t.test", "b@t.test", "hello")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	ev := waitForEvent(t, ch)
	if ev.Kind != KindAppend || ev.UID != res.UID || ev.MessageID != res.MessageID || ev.FolderID != inbox.ID {
		t.Fatalf("append event = %+v, want append for uid %d", ev, res.UID)
	}

	// Flags → KindFlags.
	if _, err := svc.SetFlags(1, 0, FlagsUpdate{MessageID: res.MessageID, Set: models.FlagSeen}); err != nil {
		t.Fatalf("flags: %v", err)
	}
	ev = waitForEvent(t, ch)
	if ev.Kind != KindFlags || ev.UID != res.UID || ev.Flags&models.FlagSeen == 0 {
		t.Fatalf("flags event = %+v", ev)
	}

	// Move → append into the target folder, expunge out of the source.
	moved, err := svc.MoveMessage(res.MessageID, archive.ID, 1, 0)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	evTarget := waitForEvent(t, chTarget)
	if evTarget.Kind != KindAppend || evTarget.UID != moved.UID || evTarget.FolderID != archive.ID {
		t.Fatalf("move target event = %+v", evTarget)
	}
	ev = waitForEvent(t, ch)
	if ev.Kind != KindExpunge || ev.UID != res.UID || ev.FolderID != inbox.ID {
		t.Fatalf("move source event = %+v, want expunge of uid %d", ev, res.UID)
	}

	// Expunge → KindExpunge listing the victim UIDs ascending.
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("notify-two", "a@t.test", "b@t.test", "hello two")}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	waitForEvent(t, ch) // drain the append event
	page, err := svc.ListMessages(inbox.ID, 1, 0, ListFilter{}, 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var deletedUIDs []uint32
	for _, m := range page.Messages {
		if _, err := svc.SetFlags(1, 0, FlagsUpdate{MessageID: m.ID, Set: models.FlagDeleted}); err != nil {
			t.Fatalf("delete %d: %v", m.ID, err)
		}
		waitForEvent(t, ch) // drain each flags event
		deletedUIDs = append(deletedUIDs, m.UID)
	}
	exp, err := svc.Expunge(inbox.ID, 1, 0)
	if err != nil {
		t.Fatalf("expunge: %v", err)
	}
	if exp.Expunged != int64(len(deletedUIDs)) || len(exp.UIDs) != len(deletedUIDs) {
		t.Fatalf("expunge = %+v, want %d victims", exp, len(deletedUIDs))
	}
	ev = waitForEvent(t, ch)
	if ev.Kind != KindExpunge || len(ev.ExpungedUIDs) != len(deletedUIDs) {
		t.Fatalf("expunge event = %+v, want %d UIDs", ev, len(deletedUIDs))
	}
	for i, uid := range ev.ExpungedUIDs {
		if uid != deletedUIDs[i] {
			t.Fatalf("expunge UID[%d] = %d, want %d", i, uid, deletedUIDs[i])
		}
	}
}

// TestServiceNotificationsOptional: an unwired notifier (nil) never fails a
// mutation — notification is best-effort by contract.
func TestServiceNotificationsOptional(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "notify-nil@post", 0)
	inbox := inboxFolder(t, svc, mb)
	svc.SetChangeNotifier(nil) // explicit: disabled
	if _, err := svc.Append(context.Background(), 1, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("notify-nil", "a@t.test", "b@t.test", "x")}); err != nil {
		t.Fatalf("append with nil notifier: %v", err)
	}
	if _, err := svc.Expunge(inbox.ID, 1, 0); err != nil {
		t.Fatalf("expunge with nil notifier: %v", err)
	}
}
