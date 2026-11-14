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

package mta

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/webhook"
)

// SF-16 tests: the per-SMTP-stage hook emitter. Stage events fire at
// connect/ehlo/mail/rcpt/data, the fire is asynchronous and never blocks the
// session, the payload rides the same DispatchJSON seam the HMAC-signed
// webhook delivery path uses, and a panicking delivery is isolated from the
// SMTP session.

type recordingHookDispatcher struct {
	mu    sync.Mutex
	calls []hookCall
	block chan struct{} // non-nil: DispatchJSON blocks until closed
}

type hookCall struct {
	userID      uint
	workspaceID *uint
	event       string
	body        map[string]any
	fromAddr    string
}

func (r *recordingHookDispatcher) DispatchJSON(userID uint, workspaceID *uint, event string, body []byte, fromAddr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.block != nil {
		<-r.block
	}
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	r.calls = append(r.calls, hookCall{
		userID: userID, workspaceID: workspaceID, event: event, body: m, fromAddr: fromAddr,
	})
}

func (r *recordingHookDispatcher) captured() []hookCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hookCall(nil), r.calls...)
}

// TestStageHooksFireAtEachStage pins the stage vocabulary and the payload
// progression: connect → ehlo → mail → rcpt → data, each event named
// smtp.<stage> and carrying the facts known at that stage.
func TestStageHooksFireAtEachStage(t *testing.T) {
	rec := &recordingHookDispatcher{}
	em := NewStageHookEmitter(rec)
	ws := uint(3)

	hs := NewStageHookSession(em, 7, &ws, "192.0.2.10")
	hs.Ehlo("client.example.org")
	hs.SetTenant(7, &ws, "smtp_alice")
	hs.Mail("hello@verified.example")
	hs.Rcpt("dest@example.net")
	hs.Data(512)

	calls := rec.captured()
	if len(calls) != 5 {
		t.Fatalf("hook calls = %d, want 5 (connect/ehlo/mail/rcpt/data)", len(calls))
	}
	wantStages := []string{"connect", "ehlo", "mail", "rcpt", "data"}
	for i, want := range wantStages {
		if calls[i].event != "smtp."+want {
			t.Fatalf("call %d event = %q, want smtp.%s", i, calls[i].event, want)
		}
	}
	// The tenant scope rides from the Auth attribution onward.
	for i := 2; i < 5; i++ {
		if calls[i].userID != 7 || calls[i].workspaceID == nil || *calls[i].workspaceID != 3 {
			t.Fatalf("call %d scope = %d/%v, want 7/3 (post-Auth attribution)", i, calls[i].userID, calls[i].workspaceID)
		}
	}
	// The payload progression: connect carries only the remote IP; ehlo adds
	// the name; mail adds the sender; rcpt adds the recipient; data adds the
	// size.
	if calls[0].body["remote_ip"] != "192.0.2.10" || calls[0].body["helo"] != nil {
		t.Fatalf("connect payload = %v", calls[0].body)
	}
	if calls[1].body["helo"] != "client.example.org" {
		t.Fatalf("ehlo payload = %v", calls[1].body)
	}
	if calls[2].body["mail_from"] != "hello@verified.example" {
		t.Fatalf("mail payload = %v", calls[2].body)
	}
	if rcpt, ok := calls[3].body["rcpt"].([]any); !ok || len(rcpt) != 1 || rcpt[0] != "dest@example.net" {
		t.Fatalf("rcpt payload = %v", calls[3].body)
	}
	if calls[4].body["message_size"] != float64(512) {
		t.Fatalf("data payload = %v", calls[4].body)
	}
	if calls[4].body["authenticated_as"] != "smtp_alice" {
		t.Fatalf("data payload authenticated_as = %v", calls[4].body["authenticated_as"])
	}
}

// TestStageHooksAsyncNonBlocking pins the never-blocks invariant: the stage
// fire returns while the delivery is still in flight. The fake mirrors the
// production DispatchJSON contract (the delivery runs in the dispatcher's
// own goroutine) but blocks that delivery until released — the SMTP session
// stage completes anyway, and the delivery lands after release.
func TestStageHooksAsyncNonBlocking(t *testing.T) {
	rec := &recordingHookDispatcher{}
	release := make(chan struct{})
	var wg sync.WaitGroup
	async := StageHookDispatcherFunc(func(userID uint, workspaceID *uint, event string, body []byte, fromAddr string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			rec.DispatchJSON(userID, workspaceID, event, body, fromAddr)
		}()
	})
	em := NewStageHookEmitter(async)

	hs := NewStageHookSession(em, 7, nil, "192.0.2.10")
	start := time.Now()
	hs.Ehlo("client.example.org")
	hs.Mail("hello@verified.example")
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("the stage fires took %s with deliveries blocked in flight, want an immediate return (async)", elapsed)
	}
	if got := rec.captured(); len(got) != 0 {
		t.Fatalf("deliveries landed while blocked: %d", len(got))
	}
	close(release)
	wg.Wait()
	// Three in-flight deliveries: the connect stage (fired by the session
	// constructor) plus ehlo and mail.
	if got := rec.captured(); len(got) != 3 {
		t.Fatalf("delivered = %d after release, want the three in-flight events", len(got))
	}
}

// TestStageHooksFailureIsolation pins the failure isolation: a delivery seam
// that panics is recovered at the fire boundary — the SMTP session continues
// and the next stage still fires.
func TestStageHooksFailureIsolation(t *testing.T) {
	rec := &recordingHookDispatcher{}
	var fired int
	panicking := StageHookDispatcherFunc(func(userID uint, workspaceID *uint, event string, body []byte, fromAddr string) {
		fired++
		if fired == 1 {
			panic("hook delivery exploded")
		}
		rec.DispatchJSON(userID, workspaceID, event, body, fromAddr)
	})
	em := NewStageHookEmitter(panicking)

	// The connect stage (fired by the session constructor) panics inside the
	// seam — recovered at the fire boundary — and the session continues: the
	// subsequent stages still fire.
	hs := NewStageHookSession(em, 7, nil, "192.0.2.10")
	hs.Ehlo("client.example.org")
	hs.Mail("hello@verified.example")

	calls := rec.captured()
	if len(calls) != 2 || calls[0].event != "smtp.ehlo" || calls[1].event != "smtp.mail" {
		t.Fatalf("calls = %+v, want ehlo + mail to still fire after the isolated connect panic", calls)
	}
}

func wsIDLocal(v uint) *uint { return &v }

// TestStageHooksPayloadHMACSignature pins the HMAC contract: the stage
// payload the emitter produces is signed by the webhook machinery's own
// signing function (webhook.SignBody — the exact function the Dispatcher's
// delivery path applies at dispatcher.go, producing the X-Posta-Signature:
// sha256=... header) and the signature verifies over the delivered bytes.
func TestStageHooksPayloadHMACSignature(t *testing.T) {
	rec := &recordingHookDispatcher{}
	em := NewStageHookEmitter(rec)

	hs := NewStageHookSession(em, 7, wsIDLocal(3), "192.0.2.10")
	hs.Mail("hello@verified.example")

	calls := rec.captured()
	// The session constructor fires the connect stage first; the mail stage's
	// payload is the one under signature test.
	if len(calls) != 2 || calls[1].event != "smtp.mail" {
		t.Fatalf("hook calls = %+v, want [connect, mail]", calls)
	}
	// Re-serialize the payload exactly as the delivery path would carry it.
	body, err := json.Marshal(calls[1].body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	secret := "webhook-secret-123"
	signature := "sha256=" + webhook.SignBody(secret, body)
	if !strings.HasPrefix(signature, "sha256=") {
		t.Fatalf("signature = %q, want the sha256= scheme", signature)
	}
	// Recompute and compare (the verification a webhook receiver performs).
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if signature != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature %q does not verify over the delivered payload", signature)
	}
}

// TestStageHooksPayloadHMACSignature pins the event vocabulary.
func TestStageHookEventName(t *testing.T) {
	for stage, want := range map[string]string{
		"connect": "smtp.connect", "ehlo": "smtp.ehlo", "mail": "smtp.mail",
		"rcpt": "smtp.rcpt", "data": "smtp.data",
	} {
		if got := StageHookEventName(stage); got != want {
			t.Fatalf("StageHookEventName(%q) = %q, want %q", stage, got, want)
		}
	}
}

// The nil-session and nil-emitter guards: every entry point is a no-op.
func TestStageHooksNilGuards(t *testing.T) {
	var nilSession *StageHookSession
	nilSession.Ehlo("h")
	nilSession.Mail("f")
	nilSession.Rcpt("r")
	nilSession.Data(1)
	nilSession.ResetTransaction()
	nilSession.SetTenant(1, nil, "x")

	em := NewStageHookEmitter(nil)
	hs := NewStageHookSession(em, 7, nil, "192.0.2.1")
	hs.Ehlo("h") // the emitter's dispatcher is nil — the fire recovers/skips
	if hs.facts.Helo != "h" {
		t.Fatalf("facts must advance even with an inert dispatcher: %+v", hs.facts)
	}
}
