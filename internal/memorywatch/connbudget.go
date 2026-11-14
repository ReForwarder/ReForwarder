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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Per-connection aggregate byte budgets (SF-43 bounded-spec item 1), riding
// the same memory-budget substrate as the process watchdog: every octet a
// listener connection reads — SMTP commands and message payloads alike,
// charged across the WHOLE session, not per message — counts against one
// budget. Past a soft threshold (80% of the budget) each read is delayed on
// a linear ramp (gradual throttling: the session slows down as it burns the
// budget); past the hard budget the connection is disconnected with a
// transport error. The listener backpressure (Watchdog, SF-108) protects the
// process; this wrapper protects each connection slot from a single abusive
// peer. The two compose: BudgetListener wraps the raw listener inside the
// security CountingListener, whose behavior is untouched.

package memorywatch

import (
	"errors"
	"net"
	"sync/atomic"
	"time"

	"github.com/goposta/posta/internal/metrics"
	"github.com/jkaninda/logger"
)

const (
	// connSoftRatioPct is the percentage of the hard budget at which the
	// gradual throttle begins.
	connSoftRatioPct = 80
	// connMaxThrottleDelay is the per-read delay ceiling at the hard edge.
	// It is deliberately small against the listeners' 60s read deadline: the
	// throttle must slow a session, not trip its timeouts.
	connMaxThrottleDelay = time.Second
)

// ErrConnBudgetExceeded is returned by a budgeted connection's Read once the
// hard budget is crossed. It is a transport-level error: the SMTP server
// closes the connection on any read error, which is the disconnect.
var ErrConnBudgetExceeded = errors.New("connection byte budget exceeded")

// ConnectionBudget is the per-connection aggregate byte budget. nil (budget
// unset) is the disabled case — every call site treats nil as "no budget",
// indistinguishable from the pre-SF-43 behavior.
type ConnectionBudget struct {
	softBytes int64
	hardBytes int64
	maxDelay  time.Duration
	// sleeper blocks for the throttle delay (tests record instead of sleeping).
	sleeper func(time.Duration)
}

// NewConnectionBudget arms a per-connection budget of megabytes. megabytes
// <= 0 returns nil — the budget is disabled and nothing is metered. The soft
// threshold sits at connSoftRatioPct of the hard budget.
func NewConnectionBudget(megabytes int) *ConnectionBudget {
	if megabytes <= 0 {
		return nil
	}
	hard := int64(megabytes) << 20
	return &ConnectionBudget{
		softBytes: hard * connSoftRatioPct / 100,
		hardBytes: hard,
		maxDelay:  connMaxThrottleDelay,
		sleeper:   time.Sleep,
	}
}

// throttleDelay maps bytes already consumed onto the ramp delay: 0 below the
// soft threshold, growing linearly toward maxDelay at the hard edge.
func (b *ConnectionBudget) throttleDelay(used int64) time.Duration {
	if b == nil || b.hardBytes <= 0 || used <= b.softBytes {
		return 0
	}
	span := b.hardBytes - b.softBytes
	if span <= 0 {
		return b.maxDelay
	}
	over := used - b.softBytes
	d := time.Duration(float64(b.maxDelay) * float64(over) / float64(span))
	if d > b.maxDelay {
		d = b.maxDelay
	}
	return d
}

// budgetConn meters one connection. Reads (the only client-controlled
// direction: commands and DATA payloads) are charged; writes (server
// replies) are not.
type budgetConn struct {
	net.Conn
	budget *ConnectionBudget
	used     atomic.Int64
	// disconnected makes the over-budget disconnect metric one-shot per conn.
	disconnected atomic.Bool
}

func (c *budgetConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n <= 0 || c.budget == nil {
		return n, err
	}
	used := c.used.Add(int64(n))
	if used > c.budget.hardBytes {
		if !c.disconnected.Swap(true) {
			metrics.IncrementConnBudgetDisconnect()
			logger.Warn("connbudget: connection byte budget exceeded; disconnecting",
				"remote", c.Conn.RemoteAddr().String(),
				"used_bytes", used, "budget_bytes", c.budget.hardBytes)
		}
		return n, ErrConnBudgetExceeded
	}
	if d := c.budget.throttleDelay(used); d > 0 {
		c.budget.sleeper(d)
	}
	return n, err
}

// budgetListener wraps a net.Listener so every accepted connection is
// metered through the budget. A nil budget (or nil listener) returns the
// listener unchanged — the disabled case is the previous behavior, untouched.
type budgetListener struct {
	net.Listener
	budget *ConnectionBudget
}

// BudgetListener arms the per-connection byte budget on a listener. The
// wrapper sits under the security CountingListener and under TLS: it meters
// whatever the socket delivers, so STARTTLS and implicit-TLS sessions are
// metered the same way (wire bytes).
func BudgetListener(l net.Listener, b *ConnectionBudget) net.Listener {
	if l == nil || b == nil {
		return l
	}
	return &budgetListener{Listener: l, budget: b}
}

func (l *budgetListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &budgetConn{Conn: c, budget: l.budget}, nil
}
