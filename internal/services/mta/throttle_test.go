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

package mta

import (
	"net"
	"testing"
	"time"
)

// SF-108: the per-connection transfer throttle. Writes cross at the token
// bucket's pace — the burst covers the first rate-worth of bytes, the rest
// accrue at rate bytes/second.

// sinkConn swallows writes (the wire stand-in; no blocking).
type sinkConn struct{ net.Conn }

func (sinkConn) Write(p []byte) (int, error) { return len(p), nil }
func (sinkConn) Close() error                { return nil }

func TestThrottledConnBoundsWriteRate(t *testing.T) {
	const rate = 8 << 10 // 8 KiB/s: burst 8 KiB, then paced
	c := newThrottledConn(sinkConn{}, rate)

	payload := make([]byte, 3*rate) // 3 bursts' worth = 24 KiB
	start := time.Now()
	if _, err := c.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	elapsed := time.Since(start)

	// The first burst is free; the remaining 2 bursts accrue at rate.
	min := 2 * time.Second // 16 KiB at 8 KiB/s
	max := 6 * time.Second // generous CI margin
	if elapsed < min {
		t.Fatalf("24 KiB at 8 KiB/s wrote in %s; the throttle is not pacing (want >= %s)", elapsed, min)
	}
	if elapsed > max {
		t.Fatalf("24 KiB at 8 KiB/s took %s; the throttle over-paces (want <= %s)", elapsed, max)
	}
}

func TestThrottledConnZeroRateFloored(t *testing.T) {
	// An absurdly small configured rate is floored at 64 B/s so a single
	// SMTP command never stalls forever.
	c := newThrottledConn(sinkConn{}, 1)
	if c.rate < 64 {
		t.Fatalf("rate = %v, want the 64 B/s floor", c.rate)
	}
}
