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

package inbound

import (
	"testing"
	"time"
)

func TestRateLimiterDisabled(t *testing.T) {
	l := NewIPRateLimiter(0, time.Second)
	for i := 0; i < 100; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("disabled limiter denied request #%d", i)
		}
	}
}

func TestRateLimiterAllowsThenDenies(t *testing.T) {
	l := NewIPRateLimiter(3, time.Second)
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("denied request #%d within budget", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatalf("expected deny after exhausting budget")
	}
	// different IP still allowed
	if !l.Allow("5.6.7.8") {
		t.Fatalf("unrelated IP denied")
	}
}

func TestRateLimiterResetsAfterWindow(t *testing.T) {
	l := NewIPRateLimiter(1, 30*time.Millisecond)
	if !l.Allow("x") {
		t.Fatal("first should succeed")
	}
	if l.Allow("x") {
		t.Fatal("second should deny")
	}
	time.Sleep(60 * time.Millisecond)
	if !l.Allow("x") {
		t.Fatal("should reset after window")
	}
}
