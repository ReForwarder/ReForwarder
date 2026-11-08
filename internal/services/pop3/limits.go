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

package pop3

import (
	"sync"
	"time"
)

// ipConns enforces the per-IP concurrent-connection cap (ADR-004 decision 5,
// config.MailProtocolConfig.MaxConnsPerIP): a live-connection counter per
// client IP shared by both listeners. max <= 0 disables the cap.
type ipConns struct {
	mu   sync.Mutex
	max  int
	live map[string]int
}

func newIPConns(max int) *ipConns {
	return &ipConns{max: max, live: map[string]int{}}
}

// acquire registers one live connection for ip; the returned release func is
// idempotent and always frees the slot.
func (c *ipConns) acquire(ip string) (bool, func()) {
	if c == nil {
		return true, func() {}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.max > 0 && c.live[ip] >= c.max {
		return false, func() {}
	}
	c.live[ip]++
	return true, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if n := c.live[ip]; n > 1 {
			c.live[ip] = n - 1
		} else {
			delete(c.live, ip)
		}
	}
}

// sessionCaps enforces the per-user concurrent-session cap (ADR-004 decision
// 5). maxPerUser <= 0 means unlimited.
type sessionCaps struct {
	mu         sync.Mutex
	maxPerUser int
	active     map[uint]int
}

func newSessionCaps(maxPerUser int) *sessionCaps {
	return &sessionCaps{maxPerUser: maxPerUser, active: map[uint]int{}}
}

// acquire registers one session for userID, reporting whether the cap allows
// it. The returned release func is idempotent and always frees the slot.
func (c *sessionCaps) acquire(userID uint) (bool, func()) {
	if c == nil {
		return true, func() {}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxPerUser > 0 && c.active[userID] >= c.maxPerUser {
		return false, func() {}
	}
	c.active[userID]++
	user := userID
	return true, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if n := c.active[user]; n > 1 {
			c.active[user] = n - 1
		} else {
			delete(c.active, user)
		}
	}
}

// commandLimiter bounds one session's command rate (ADR-004 decision 5):
// a fixed window per session; when the budget is exhausted every command is
// rejected until the window resets. maxCommands <= 0 disables the limit.
type commandLimiter struct {
	window      time.Duration
	maxCommands int
	count       int
	reset       time.Time
}

func newCommandLimiter(maxCommands int, window time.Duration) *commandLimiter {
	return &commandLimiter{maxCommands: maxCommands, window: window}
}

// allow reports whether one more command fits this window.
func (l *commandLimiter) allow() bool {
	if l == nil || l.maxCommands <= 0 {
		return true
	}
	now := time.Now()
	if now.After(l.reset) {
		l.count = 0
		l.reset = now.Add(l.window)
	}
	if l.count >= l.maxCommands {
		return false
	}
	l.count++
	return true
}
