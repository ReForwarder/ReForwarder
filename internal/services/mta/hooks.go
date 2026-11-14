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
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// StageHookDispatcher is the delivery seam for the per-SMTP-stage hooks
// (SF-16): the production implementation is the existing webhook Dispatcher
// (DispatchJSON), which delivers asynchronously, retries with the standard
// backoff, and HMAC-signs every delivery with the webhook's own secret
// (X-Posta-Signature: sha256=...) — the per-stage hooks inherit all of it.
// Tests inject recording/blocking fakes.
type StageHookDispatcher interface {
	DispatchJSON(userID uint, workspaceID *uint, event string, body []byte, fromAddr string)
}

// StageHookEmitter fires first-party HTTP hook events at the SMTP session
// stages (connect/ehlo/mail/rcpt/data — SF-16, the ADR-001/002-gated slice).
//
// Non-blocking by construction: every fire marshals a small payload and hands
// it to the dispatcher, which delivers asynchronously (DispatchJSON runs the
// delivery in its own goroutine and returns immediately) — the SMTP session
// is never delayed waiting for a hook. A panic inside the delivery seam is
// recovered at the fire boundary (failure isolation: the SMTP session always
// continues), and marshal failures are impossible for these plain-struct
// payloads — a marshal error is swallowed with the fire skipped rather than
// delaying the session.
//
// Gating: the emitter is wired only when POSTA_SMTP_STAGE_HOOKS_ENABLED is
// set (the ADR-gated slice posture); unwired, every fire is a nil-receiver
// no-op and the sessions behave byte-for-byte as before.
type StageHookEmitter struct {
	dispatcher StageHookDispatcher
}

// NewStageHookEmitter builds the emitter. nil dispatcher = inert.
func NewStageHookEmitter(d StageHookDispatcher) *StageHookEmitter {
	return &StageHookEmitter{dispatcher: d}
}

// StageFacts carries the session facts known at each stage. Fields stay
// zero until their stage completes; the payload includes only the set ones.
type StageFacts struct {
	RemoteIP        string
	Helo            string
	MailFrom        string
	AuthenticatedAs string
	Rcpt            []string
	MessageSize     int64
}

// hookPayload is the JSON shape of one stage event.
type hookPayload struct {
	Event           string   `json:"event"`
	Stage           string   `json:"stage"`
	RemoteIP        string   `json:"remote_ip,omitempty"`
	Helo            string   `json:"helo,omitempty"`
	MailFrom        string   `json:"mail_from,omitempty"`
	Rcpt            []string `json:"rcpt,omitempty"`
	AuthenticatedAs string   `json:"authenticated_as,omitempty"`
	MessageSize     int64    `json:"message_size,omitempty"`
	Timestamp       string   `json:"timestamp"`
}

// stageNames are the hook-event vocabulary stages.
const (
	stageConnect = "connect"
	stageEhlo    = "ehlo"
	stageMail    = "mail"
	stageRcpt    = "rcpt"
	stageData    = "data"
)

// StageHookSession carries the per-session facts the emitter fires with, plus
// a mutex for the session's own progression (single SMTP command at a time by
// protocol, so the mutex is formality).
type StageHookSession struct {
	mu      sync.Mutex
	emitter *StageHookEmitter
	userID  uint
	wsID    *uint
	facts   StageFacts
}

// NewStageHookSession starts the hook session (the connect stage fires here —
// the receiver's NewSession boundary, before any SMTP command).
func NewStageHookSession(emitter *StageHookEmitter, userID uint, wsID *uint, remoteIP string) *StageHookSession {
	s := &StageHookSession{emitter: emitter, userID: userID, wsID: wsID}
	s.facts.RemoteIP = remoteIP
	s.fire(stageConnect)
	return s
}

// Ehlo records the EHLO name and fires the ehlo stage.
func (s *StageHookSession) Ehlo(helo string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.facts.Helo = helo
	s.mu.Unlock()
	s.fire(stageEhlo)
}

// SetTenant attributes the hook session to the authenticated tenant (the
// stage fires after Auth carry the tenant scope; the pre-auth connect/ehlo
// stages fire without it) and records the authenticated identity.
func (s *StageHookSession) SetTenant(userID uint, wsID *uint, authenticatedAs string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.userID = userID
	s.wsID = wsID
	s.facts.AuthenticatedAs = authenticatedAs
	s.mu.Unlock()
}

// ResetTransaction clears the per-transaction facts (envelope sender,
// recipients, message size) at RSET — the connection-wide facts (remote IP,
// EHLO name) and the tenant attribution persist.
func (s *StageHookSession) ResetTransaction() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.facts.MailFrom = ""
	s.facts.Rcpt = nil
	s.facts.MessageSize = 0
	s.mu.Unlock()
}

// Mail records the envelope sender and fires the mail stage.
func (s *StageHookSession) Mail(from string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.facts.MailFrom = from
	s.mu.Unlock()
	s.fire(stageMail)
}

// Rcpt records one envelope recipient and fires the rcpt stage.
func (s *StageHookSession) Rcpt(to string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.facts.Rcpt = append(s.facts.Rcpt, to)
	s.mu.Unlock()
	s.fire(stageRcpt)
}

// Data records the message size and fires the data stage (end-of-data).
func (s *StageHookSession) Data(size int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.facts.MessageSize = size
	sizeLocal := size
	facts := s.facts
	s.mu.Unlock()
	s.fireWith(stageData, func(f StageFacts) StageFacts { f.MessageSize = sizeLocal; return facts })
}

// fire marshals the current facts and dispatches asynchronously. A panic in
// the delivery seam is recovered at this boundary — the SMTP session always
// continues (failure isolation).
func (s *StageHookSession) fire(stage string) {
	s.fireWith(stage, func(f StageFacts) StageFacts { return f })
}

func (s *StageHookSession) fireWith(stage string, mutate func(StageFacts) StageFacts) {
	if s == nil || s.emitter == nil {
		return
	}
	s.mu.Lock()
	userID, wsID := s.userID, s.wsID
	facts := mutate(s.facts)
	s.mu.Unlock()

	payload := hookPayload{
		Event:           "smtp." + stage,
		Stage:           stage,
		RemoteIP:        facts.RemoteIP,
		Helo:            facts.Helo,
		MailFrom:        facts.MailFrom,
		Rcpt:            facts.Rcpt,
		AuthenticatedAs: facts.AuthenticatedAs,
		MessageSize:     facts.MessageSize,
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return // plain-struct marshal cannot fail; a failure skips the fire
	}
	func() {
		defer func() {
			_ = recover() // failure isolation: the SMTP session always continues
		}()
		s.emitter.dispatcher.DispatchJSON(userID, wsID, payload.Event, body, facts.MailFrom)
	}()
}

// StageHookDispatcherFunc adapts a function onto the delivery seam.
type StageHookDispatcherFunc func(userID uint, workspaceID *uint, event string, body []byte, fromAddr string)

// DispatchJSON implements StageHookDispatcher.
func (f StageHookDispatcherFunc) DispatchJSON(userID uint, workspaceID *uint, event string, body []byte, fromAddr string) {
	f(userID, workspaceID, event, body, fromAddr)
}

// StageHookEventName builds the hook-event name for a stage ("smtp.mail" ...).
func StageHookEventName(stage string) string {
	return fmt.Sprintf("smtp.%s", stage)
}
