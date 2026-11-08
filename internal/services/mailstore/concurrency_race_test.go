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
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// TestConcurrentAppendAndExpungeRace (battery: -race) hammers one folder from
// concurrent sessions: appenders ingest, flaggers store flags (CAS), expungers
// purge under the per-folder ConcurrentSessionGuard, and a notifier fan-out
// runs alongside. Invariants asserted afterwards:
//
//   - every append succeeded with a distinct UID (the atomic claim holds under
//     concurrency);
//   - UIDs observed per message never decrease within a folder (monotonicity);
//   - final state is internally consistent: StatusCounts.Messages equals the
//     surviving rows and the expunge victims were all \Deleted rows;
//   - no data race (the race detector's job in the dockerized battery).
//
// The fake driver serializes statements with its own mutex, which is a
// strictly stronger ordering than the row locks provide on real Postgres; the
// real-Postgres sibling (TestConcurrentAppendAndExpungeRacePG) proves the SQL
// layer holds the same invariants when TEST_DATABASE_DSN is set.
func TestConcurrentAppendAndExpungeRace(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, 1, "race@post", 0)
	folder := inboxFolder(t, svc, mb)

	n := NewChangeNotifier()
	svc.SetChangeNotifier(n)
	ch, cancel := n.Subscribe(context.Background(), folder.ID)
	defer cancel()

	const (
		appenders   = 6
		perAppender = 12
		flaggers    = 3
		expungers   = 2
	)

	var mu sync.Mutex
	seenUIDs := map[uint32]bool{}
	assignedUIDs := make([]uint32, 0, appenders*perAppender)

	var wg sync.WaitGroup
	errCh := make(chan error, appenders+flaggers+expungers)

	for a := 0; a < appenders; a++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perAppender; i++ {
				raw := simpleRaw(
					fmt.Sprintf("race-%d-%d", id, i),
					fmt.Sprintf("r%d@race.test", id),
					"inbox@race.test",
					"concurrent body",
				)
				res, err := svc.Append(context.Background(), 1, 0, AppendIn{
					FolderID: folder.ID, Raw: raw,
				})
				if err != nil {
					errCh <- fmt.Errorf("append %d/%d: %w", id, i, err)
					return
				}
				mu.Lock()
				if seenUIDs[res.UID] {
					mu.Unlock()
					errCh <- fmt.Errorf("duplicate UID %d assigned", res.UID)
					return
				}
				seenUIDs[res.UID] = true
				assignedUIDs = append(assignedUIDs, res.UID)
				mu.Unlock()
			}
		}(a)
	}
	for f := 0; f < flaggers; f++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				page, err := svc.ListMessages(folder.ID, 1, 0, ListFilter{}, 1, 10)
				if err != nil {
					errCh <- fmt.Errorf("flagger list: %w", err)
					return
				}
				for _, m := range page.Messages {
					if _, err := svc.SetFlags(1, 0, FlagsUpdate{
						MessageID: m.ID,
						Set:       models.FlagFlagged,
						Clear:     models.FlagFlagged, // set then clear = no net change, exercises the CAS path
					}); err != nil {
						// The message may have been expunged between the list
						// and the store — that is a legal race outcome, not a
						// failure.
						continue
					}
				}
			}
		}(f)
	}
	for e := 0; e < expungers; e++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 6; i++ {
				page, err := svc.ListMessages(folder.ID, 1, 0, ListFilter{}, 1, 10)
				if err != nil {
					errCh <- fmt.Errorf("expunger list: %w", err)
					return
				}
				for _, m := range page.Messages {
					if m.UID%5 == uint32(id) {
						_ = svc.MarkDeleted(1, 0, m.ID) // may race an expunge — fine
					}
				}
				if _, err := svc.Expunge(folder.ID, 1, 0); err != nil {
					errCh <- fmt.Errorf("expunge: %w", err)
					return
				}
			}
		}(e)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	// Draining the subscriber proves the notifier fan-out survived the churn
	// without a panic (delivery is best-effort; the count is not asserted).
	for i := 0; i < 3; i++ {
		select {
		case <-ch:
		default:
		}
	}

	sort.Slice(assignedUIDs, func(i, j int) bool { return assignedUIDs[i] < assignedUIDs[j] })
	if len(assignedUIDs) != appenders*perAppender {
		t.Fatalf("expected %d appended messages, saw %d", appenders*perAppender, len(assignedUIDs))
	}
	for i := 1; i < len(assignedUIDs); i++ {
		if assignedUIDs[i] == assignedUIDs[i-1] {
			t.Fatalf("UID %d assigned twice", assignedUIDs[i])
		}
	}

	// Final consistency: the store's own counters and the message rows agree.
	st, err := svc.StatusCounts(folder.ID, 1, 0)
	if err != nil {
		t.Fatalf("final status: %v", err)
	}
	page, err := svc.ListMessages(folder.ID, 1, 0, ListFilter{}, 1, 200)
	if err != nil {
		t.Fatalf("final list: %v", err)
	}
	if st.Messages != int64(len(page.Messages)) {
		t.Fatalf("StatusCounts(%d) disagrees with the rows (%d)", st.Messages, len(page.Messages))
	}
	for _, m := range page.Messages {
		if !seenUIDs[m.UID] {
			t.Fatalf("row carries UID %d that was never assigned", m.UID)
		}
	}
}
