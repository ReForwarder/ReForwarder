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

package imap

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// idleMaxDuration is the RFC 9051 §6.3.13 guidance: a server may break IDLE
// after 29 minutes so the client reconnects statefully.
const idleMaxDuration = 29 * time.Minute

// idleEventInterval is how often the IDLE loop re-drains notifications even
// without fresh wire input.
const idleEventInterval = 500 * time.Millisecond

// cmdIdle implements IDLE (RFC 9051 §6.3.13) over the W1 ChangeNotifier:
// while idling, folder changes surface as EXISTS/EXPUNGE/FETCH FLAGS untagged
// responses; the command ends on "DONE" (or the 29-minute safety bound).
//
// Concurrency: the notification drainer runs only while the main read loop
// is blocked on this connection's wire, and cmdIdle waits for the drainer to
// exit before returning control to dispatch — the two never touch the
// session state concurrently.
func (st *session) cmdIdle(cmd *commandTokens) error {
	if st.state != stateAuth && st.state != stateSelected {
		st.stateRefusal(cmd)
		return nil
	}
	st.write("+ idling\r\n")
	stop := make(chan struct{})
	done := make(chan struct{})
	// Drainer: re-apply notifications until stopped. All socket writes are
	// serialized through writeMu against the main loop.
	go func() {
		defer close(done)
		ticker := time.NewTicker(idleEventInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				st.applyNotifications(false)
			}
		}
	}()
	var once sync.Once
	endIdle := func() {
		once.Do(func() {
			close(stop)
			<-done // the drainer must be fully gone before the main loop resumes
		})
	}
	defer endIdle() // covers timeout / IO-error exits
	// Wait for DONE with the idle bound.
	if err := st.conn.SetDeadline(time.Now().Add(idleMaxDuration)); err != nil {
		return err
	}
	for {
		line, err := st.lr.readLine()
		if err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(line), "DONE") {
			break
		}
		if line == "" {
			continue
		}
		st.tagged(cmd.Tag, "BAD", "Expected DONE to end IDLE")
		// Keep idling; the next line may still be DONE.
	}
	endIdle()
	if err := st.conn.SetDeadline(time.Now().Add(st.srv.cfg.IdleTimeout)); err != nil {
		return err
	}
	st.tagged(cmd.Tag, "OK", "IDLE terminated")
	return nil
}

// errIdleBroken is reserved for the future server-initiated IDLE break.
var errIdleBroken = errors.New("imap: idle broken by server")
