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

package email

import (
	"testing"
	"time"
)

// TestDeliverByDeadlineBounds pins the SF-09 deadline helper: non-positive
// by-times (BY=0 would expire the message at acceptance) and by-times above
// the 30-day cap are refused; a valid one computes acceptance + by-time.
// Lives in the email package because the helper is consumed by the SMTP
// receivers (services/mta, services/smtprelay) and email itself imports the
// queue service — the reverse import would cycle.
func TestDeliverByDeadlineBounds(t *testing.T) {
	acceptedAt := time.Now()
	if _, _, err := DeliverByDeadline(0, acceptedAt); err == nil {
		t.Fatal("BY=0 accepted, want an error")
	}
	if _, _, err := DeliverByDeadline(-5*time.Second, acceptedAt); err == nil {
		t.Fatal("negative by-time accepted, want an error")
	}
	if _, _, err := DeliverByDeadline(31*24*time.Hour, acceptedAt); err == nil {
		t.Fatal("31-day by-time accepted, want an error")
	}
	deadline, secs, err := DeliverByDeadline(90*time.Second, acceptedAt)
	if err != nil {
		t.Fatalf("90s by-time rejected: %v", err)
	}
	if secs != 90 || !deadline.Equal(acceptedAt.Add(90*time.Second)) {
		t.Fatalf("deadline = %v secs = %d, want acceptance+90s / 90", deadline, secs)
	}
}
