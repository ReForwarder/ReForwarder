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

package jobs

import (
	"context"
	"fmt"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/migration"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SF-104: the daily send-outcome report job. Aggregation runs against REAL
// Postgres (TEST_DATABASE_DSN, the backup-restore suite's gate) — the grouped
// SUM(CASE) query is real SQL. The publish/notify seams are fakes.

// srDB opens the gated test database and runs the full migration once.
func srDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available: %v", err)
	}
	if err := migration.Run(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

type srEvent struct {
	wsID     uint
	typ      string
	metadata string
}

type srFixture struct {
	events   []srEvent
	notified map[uint][]string // workspace → template names
}

func (f *srFixture) publish(event *models.Event) error {
	if event.WorkspaceID == nil {
		return nil
	}
	f.events = append(f.events, srEvent{wsID: *event.WorkspaceID, typ: event.Type, metadata: event.Metadata})
	return nil
}

func (f *srFixture) notify(workspaceID uint, templateName string, _ map[string]any) error {
	if f.notified == nil {
		f.notified = map[uint][]string{}
	}
	f.notified[workspaceID] = append(f.notified[workspaceID], templateName)
	return nil
}

// srSeedEvent inserts one email_events row inside the aggregation window.
func srUniqueWS() uint {
	return uint(time.Now().UnixNano())%900000000 + 100000000
}

func srUUID(ws uint, n int) string {
	// Time-mixed so re-runs against the shared test database do not collide
	// with the (email_uuid, type) dedup index of a previous run.
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", uint64(time.Now().UnixNano())%uint64(time.Second) + uint64(ws)*100000 + uint64(n))
}

func srSeedEvent(t *testing.T, db *gorm.DB, wsID uint, emailUUID, typ string, at time.Time) {
	t.Helper()
	ev := &models.EmailEvent{
		EmailUUID:  emailUUID,
		WorkspaceID: &wsID,
		Type:       typ,
		OccurredAt: at,
	}
	if err := db.Create(ev).Error; err != nil {
		t.Fatalf("seed event %s/%s: %v", emailUUID, typ, err)
	}
}

func TestSendReportAggregatesOutcomeCounts(t *testing.T) {
	db := srDB(t)
	fx := &srFixture{}
	// Workspace ids are unique per run: the test database is shared, and
	// earlier runs leave their seed rows in this same UTC window.
	wsA, wsB := srUniqueWS(), srUniqueWS()

	now := time.Date(2026, 11, 12, 7, 30, 0, 0, time.UTC)
	windowStart := now.Add(-24 * time.Hour).Truncate(24 * time.Hour)
	windowEnd := windowStart.Add(24 * time.Hour)

	// The job aggregates the WHOLE window across all workspaces, and this is
	// a shared, disposable test database: clear the synthetic window's rows
	// from earlier runs so the counts are exactly what this run seeds.
	if err := db.Exec("DELETE FROM email_events WHERE occurred_at >= ? AND occurred_at < ?", windowStart, windowEnd).Error; err != nil {
		t.Fatalf("clear window: %v", err)
	}

	// Workspace 1: 3 sent, 1 failed, 2 bounced, 4 opened, 1 clicked.
	for i := 0; i < 3; i++ {
		srSeedEvent(t, db, wsA, srUUID(1, i), models.EmailEventSent, windowStart.Add(time.Duration(i)*time.Minute))
	}
	srSeedEvent(t, db, wsA, srUUID(1, 10), models.EmailEventFailed, windowStart.Add(5*time.Minute))
	srSeedEvent(t, db, wsA, srUUID(1, 11), models.EmailEventBounced, windowStart.Add(6*time.Minute))
	srSeedEvent(t, db, wsA, srUUID(1, 12), models.EmailEventBounced, windowStart.Add(7*time.Minute))
	for i := 0; i < 4; i++ {
		srSeedEvent(t, db, wsA, srUUID(1, 20+i), models.EmailEventOpened, windowStart.Add(time.Duration(8+i)*time.Minute))
	}
	srSeedEvent(t, db, wsA, srUUID(1, 30), models.EmailEventClicked, windowStart.Add(12*time.Minute))

	// Workspace 2: 1 sent.
	srSeedEvent(t, db, wsB, srUUID(2, 1), models.EmailEventSent, windowStart.Add(3*time.Minute))

	// Outside the window: must not count.
	srSeedEvent(t, db, wsA, srUUID(1, 40), models.EmailEventSent, windowEnd.Add(time.Minute))
	srSeedEvent(t, db, wsA, srUUID(1, 41), models.EmailEventSent, windowStart.Add(-time.Minute))

	job := NewSendReportJob(db, func() bool { return true })
	job.SetNow(func() time.Time { return now })
	job.SetPublisher(fx.publish)
	job.SetNotifier(fx.notify)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(fx.events) != 2 {
		t.Fatalf("platform events = %d, want 2 (one per non-quiet workspace)", len(fx.events))
	}
	byWs := map[uint]map[string]int64{}
	for _, ev := range fx.events {
		byWs[ev.wsID] = decodeJSON(t, ev.metadata)
	}
	ws1 := byWs[wsA]
	if ws1 == nil {
		t.Fatal("workspace A report missing")
	}
	for k, want := range map[string]int64{"sent": 3, "failed": 1, "bounced": 2, "opened": 4, "clicked": 1} {
		if ws1[k] != want {
			t.Errorf("workspace A %s = %d, want %d", k, ws1[k], want)
		}
	}
	ws2 := byWs[wsB]
	if ws2 == nil || ws2["sent"] != 1 {
		t.Errorf("workspace B report = %v, want sent=1", ws2)
	}
}

func decodeJSON(t *testing.T, s string) map[string]int64 {
	t.Helper()
	var m map[string]int64
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("metadata %q: %v", s, err)
	}
	return m
}

func TestSendReportDisabledByDefault(t *testing.T) {
	db := srDB(t)
	fx := &srFixture{}
	wsA := srUniqueWS()

	srSeedEvent(t, db, wsA, srUUID(5, 1), models.EmailEventSent, time.Now().Add(-time.Hour))

	// enabled=false — the platform setting's default.
	job := NewSendReportJob(db, func() bool { return false })
	job.SetPublisher(fx.publish)
	job.SetNotifier(fx.notify)
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fx.events) != 0 || len(fx.notified) != 0 {
		t.Fatalf("disabled pass must be inert; got %d events, %d notifies", len(fx.events), len(fx.notified))
	}
}

func TestSendReportDeliversToAdmins(t *testing.T) {
	db := srDB(t)
	fx := &srFixture{}
	// Unique-per-run workspace ids: the shared test database keeps earlier
	// runs' seed rows inside the same UTC window.
	wsA, wsB := srUniqueWS(), srUniqueWS()

	now := time.Date(2026, 11, 12, 7, 30, 0, 0, time.UTC)
	windowStart := now.Add(-24 * time.Hour).Truncate(24 * time.Hour)
	srSeedEvent(t, db, wsA, srUUID(42, 1), models.EmailEventSent, windowStart.Add(time.Minute))
	srSeedEvent(t, db, wsA, srUUID(42, 2), models.EmailEventBounced, windowStart.Add(2*time.Minute))
	// A quiet workspace: seeded events outside the window only.
	srSeedEvent(t, db, wsB, srUUID(43, 1), models.EmailEventSent, windowStart.Add(-2*time.Hour))

	job := NewSendReportJob(db, func() bool { return true })
	job.SetNow(func() time.Time { return now })
	job.SetPublisher(fx.publish)
	job.SetNotifier(fx.notify)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := fx.notified[wsA]; len(got) != 1 || got[0] != "send_report" {
		t.Fatalf("workspace A notifications = %v, want one send_report", got)
	}
	if got := fx.notified[wsB]; len(got) != 0 {
		t.Fatalf("quiet workspace B must not be mailed; got %v", got)
	}
}
