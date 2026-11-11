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

package reputation

import (
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
)

// ws3 / ws4 are two distinct workspaces for the isolation tests.
func uintp(v uint) *uint { return &v }

var ws3 = uintp(3)
var ws4 = uintp(4)

func day(d int) time.Time { return time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Add(time.Duration(d) * 24 * time.Hour) }

func at(d int, h int) time.Time { return day(d).Add(time.Duration(h) * time.Hour) }

// The day aggregation folds transport outcomes per (workspace, source-ip,
// domain) and recipient events per (workspace, domain) — the transport side
// counts exactly one row per attempt, the event side exactly one per event,
// with bounced/complained/sent never mixed into the transport counters.
func TestAggregateDayRollupMath(t *testing.T) {
	attempts := []AttemptOutcomeRow{
		{WorkspaceID: ws3, SourceIP: "192.0.2.1", Domain: "Dest.Example", Outcome: models.MTAAttemptDelivered, At: at(0, 3)},
		{WorkspaceID: ws3, SourceIP: "192.0.2.1", Domain: "dest.example", Outcome: models.MTAAttemptDelivered, At: at(0, 4)},
		{WorkspaceID: ws3, SourceIP: "192.0.2.1", Domain: "dest.example", Outcome: models.MTAAttemptPermanentFailure, At: at(0, 5)},
		{WorkspaceID: ws3, SourceIP: "192.0.2.1", Domain: "dest.example", Outcome: models.MTAAttemptDeferredBudget, At: at(0, 6)},
		{WorkspaceID: ws3, SourceIP: "192.0.2.2", Domain: "dest.example", Outcome: models.MTAAttemptTransientFailure, At: at(0, 7)},
		{WorkspaceID: ws3, SourceIP: "192.0.2.1", Domain: "other.example", Outcome: models.MTAAttemptDelivered, At: at(0, 8)},
	}
	events := []RecipientEventRow{
		{WorkspaceID: ws3, Domain: "dest.example", Type: models.EmailEventSent, At: at(0, 3)},
		{WorkspaceID: ws3, Domain: "dest.example", Type: models.EmailEventSent, At: at(0, 4)},
		{WorkspaceID: ws3, Domain: "dest.example", Type: models.EmailEventBounced, At: at(0, 9)},
		{WorkspaceID: ws3, Domain: "dest.example", Type: models.EmailEventComplained, At: at(0, 10)},
	}

	rows := AggregateDay(day(0), attempts, events)

	find := func(ip, domain string) *models.IPReputationRollup {
		for i := range rows {
			if rows[i].IP == ip && rows[i].Domain == domain {
				return &rows[i]
			}
		}
		return nil
	}

	r1 := find("192.0.2.1", "dest.example")
	if r1 == nil {
		t.Fatal("missing rollup for 192.0.2.1/dest.example")
	}
	if r1.AttemptsTotal != 4 || r1.AttemptsDelivered != 2 || r1.AttemptsPermanent != 1 || r1.AttemptsDeferred != 1 {
		t.Fatalf("transport counters wrong: %+v", r1)
	}
	if r1.EventsSent != 0 || r1.EventsBounced != 0 {
		t.Fatalf("IP rows must not carry event counters: %+v", r1)
	}
	if r1.WorkspaceID == nil || *r1.WorkspaceID != 3 {
		t.Fatalf("workspace attribution wrong: %+v", r1.WorkspaceID)
	}
	if !r1.Day.Equal(day(0)) {
		t.Fatalf("day bucket wrong: %v", r1.Day)
	}

	r2 := find("192.0.2.2", "dest.example")
	if r2 == nil || r2.AttemptsTotal != 1 || r2.AttemptsTransient != 1 {
		t.Fatalf("second-IP rollup wrong: %+v", r2)
	}

	ro := find("192.0.2.1", "other.example")
	if ro == nil || ro.AttemptsDelivered != 1 {
		t.Fatalf("other-domain rollup wrong: %+v", ro)
	}

	// The domain-wide event row: IP "" carries the recipient-side truth.
	ev := find("", "dest.example")
	if ev == nil {
		t.Fatal("missing domain-wide event row")
	}
	if ev.EventsSent != 2 || ev.EventsBounced != 1 || ev.EventsComplained != 1 {
		t.Fatalf("event counters wrong: %+v", ev)
	}
	if ev.AttemptsTotal != 0 {
		t.Fatalf("domain-wide row must not carry transport counters: %+v", ev)
	}
}

// Rows outside the day bucket are ignored; the domain is normalized.
func TestAggregateDayBucketingAndNormalization(t *testing.T) {
	attempts := []AttemptOutcomeRow{
		{WorkspaceID: ws3, SourceIP: "192.0.2.1", Domain: "dest.example", Outcome: models.MTAAttemptDelivered, At: at(0, 2)},
		{WorkspaceID: ws3, SourceIP: "192.0.2.1", Domain: "dest.example", Outcome: models.MTAAttemptDelivered, At: at(1, 2)},  // next day
		{WorkspaceID: ws3, SourceIP: "192.0.2.1", Domain: "dest.example", Outcome: models.MTAAttemptDelivered, At: at(-1, 23)}, // previous day
		{WorkspaceID: ws3, SourceIP: " 192.0.2.1 ", Domain: "DEST.example", Outcome: models.MTAAttemptDelivered, At: at(0, 20)},
	}
	rows := AggregateDay(day(0), attempts, nil)
	total := int64(0)
	for i := range rows {
		if rows[i].Domain != "dest.example" {
			t.Fatalf("domain not normalized: %q", rows[i].Domain)
		}
		total += rows[i].AttemptsTotal
	}
	if total != 2 {
		t.Fatalf("day bucket leaked rows: total attempts = %d, want 2 (same-day only)", total)
	}
}

// BuildDomainReport computes rates per IP, joins the domain-wide events once,
// grades the aggregate, and orders the trend by day.
func TestReputationReportRatesTrendAndGrade(t *testing.T) {
	rows := []models.IPReputationRollup{
		// 192.0.2.1: 90 delivered of 100 attempts on day 0; day 1: 80/100.
		{WorkspaceID: ws3, IP: "192.0.2.1", Domain: "dest.example", Day: day(0),
			AttemptsTotal: 100, AttemptsDelivered: 90, AttemptsPermanent: 8, AttemptsDeferred: 2},
		{WorkspaceID: ws3, IP: "192.0.2.1", Domain: "dest.example", Day: day(1),
			AttemptsTotal: 100, AttemptsDelivered: 80, AttemptsTransient: 15, AttemptsPermanent: 5},
		// Domain-wide events over the window: 500 sent, 3 bounced, 1 complained.
		{WorkspaceID: ws3, IP: "", Domain: "dest.example", Day: day(0),
			EventsSent: 300, EventsBounced: 2, EventsComplained: 1},
		{WorkspaceID: ws3, IP: "", Domain: "dest.example", Day: day(1),
			EventsSent: 200, EventsBounced: 1, EventsComplained: 0},
	}
	rep := BuildDomainReport("dest.example", 14*24*time.Hour, day(0), day(2), rows)

	if rep.Domain != "dest.example" || rep.WindowDays != 14 {
		t.Fatalf("report header wrong: %+v", rep)
	}
	if len(rep.IPs) != 1 {
		t.Fatalf("IPs = %d, want 1", len(rep.IPs))
	}
	ip := rep.IPs[0]
	if ip.IP != "192.0.2.1" || ip.Attempts != 200 || ip.Delivered != 170 || ip.Permanent != 13 {
		t.Fatalf("aggregate counters wrong: %+v", ip)
	}
	if ip.Sent != 500 || ip.Bounced != 3 || ip.Complained != 1 {
		t.Fatalf("event join wrong: %+v", ip)
	}
	// bounce 3/500 = 0.6%, complaint 1/500 = 0.2%, hard-fail 13/200 = 6.5%,
	// deferred 2/200 = 1%.
	if ip.BounceRate < 0.599 || ip.BounceRate > 0.601 {
		t.Fatalf("bounce rate = %v, want 0.6", ip.BounceRate)
	}
	if ip.ComplaintRate < 0.199 || ip.ComplaintRate > 0.201 {
		t.Fatalf("complaint rate = %v, want 0.2", ip.ComplaintRate)
	}
	if ip.HardFailRate < 6.49 || ip.HardFailRate > 6.51 {
		t.Fatalf("hard-fail rate = %v, want 6.5", ip.HardFailRate)
	}
	if ip.DeferredRate < 0.99 || ip.DeferredRate > 1.01 {
		t.Fatalf("deferred rate = %v, want 1", ip.DeferredRate)
	}
	// bounce 0.6 >= 0.2 and < 0.5? 0.6 >= 0.2 warns; hard-fail 6.5 >= 5 warns.
	if ip.Grade != GradeWarning {
		t.Fatalf("grade = %q, want warning", ip.Grade)
	}
	if len(ip.Trend) != 2 {
		t.Fatalf("trend points = %d, want 2", len(ip.Trend))
	}
	if !ip.Trend[0].Day.Equal(day(0)) || ip.Trend[0].Attempts != 100 || ip.Trend[0].Bounced != 2 {
		t.Fatalf("trend point 0 wrong: %+v", ip.Trend[0])
	}
	if !ip.Trend[1].Day.Equal(day(1)) || ip.Trend[1].Permanent != 5 || ip.Trend[1].Complained != 0 {
		t.Fatalf("trend point 1 wrong: %+v", ip.Trend[1])
	}
	if rep.LimitationNote == "" {
		t.Fatal("the limitation note must be present (relay-mode invisibility)")
	}
}

// Grade thresholds sit exactly on the documented boundaries.
func TestReputationGradeThresholds(t *testing.T) {
	grade := func(attempts, sent int64, bounce, complaint, hard, deferred float64) string {
		return GradeOf(IPReputation{
			Attempts: attempts, Sent: sent,
			BounceRate: bounce, ComplaintRate: complaint,
			HardFailRate: hard, DeferredRate: deferred,
		})
	}
	if g := grade(0, 0, 0, 0, 0, 0); g != GradeUnknown {
		t.Fatalf("no data = %q, want unknown", g)
	}
	if g := grade(100, 500, 1.0, 0.1, 2.0, 5.0); g != GradeGood {
		t.Fatalf("healthy metrics = %q, want good", g)
	}
	if g := grade(100, 500, 3.0, 0.1, 2.0, 5.0); g != GradeWarning {
		t.Fatalf("bounce at warning threshold = %q, want warning", g)
	}
	if g := grade(100, 500, 2.9, 0.2, 2.0, 5.0); g != GradeWarning {
		t.Fatalf("complaint at warning threshold = %q, want warning", g)
	}
	if g := grade(100, 500, 1.0, 0.1, 5.0, 5.0); g != GradeWarning {
		t.Fatalf("hard-fail at warning threshold = %q, want warning", g)
	}
	if g := grade(100, 500, 1.0, 0.1, 1.0, 20.0); g != GradeWarning {
		t.Fatalf("deferred at warning threshold = %q, want warning", g)
	}
	if g := grade(100, 500, 8.0, 0.1, 1.0, 0.0); g != GradePoor {
		t.Fatalf("bounce at poor threshold = %q, want poor", g)
	}
	if g := grade(100, 500, 1.0, 0.5, 1.0, 0.0); g != GradePoor {
		t.Fatalf("complaint at poor threshold = %q, want poor", g)
	}
	if g := grade(100, 500, 1.0, 0.1, 10.0, 0.0); g != GradePoor {
		t.Fatalf("hard-fail at poor threshold = %q, want poor", g)
	}
	// Only event data (no attempts) still grades on the recipient rates.
	if g := grade(0, 500, 9.0, 0.0, 0.0, 0.0); g != GradePoor {
		t.Fatalf("event-only poor bounce rate = %q, want poor", g)
	}
}

// The read surface never leaks another workspace's rollups: the fake source
// feeds two workspaces into one day, the report for ws3 reads only ws3 rows.
func TestReputationIsolationAcrossWorkspaces(t *testing.T) {
	attempts := []AttemptOutcomeRow{
		{WorkspaceID: ws3, SourceIP: "192.0.2.1", Domain: "dest.example", Outcome: models.MTAAttemptDelivered, At: at(0, 3)},
		{WorkspaceID: ws4, SourceIP: "192.0.2.1", Domain: "dest.example", Outcome: models.MTAAttemptPermanentFailure, At: at(0, 4)},
	}
	rows := AggregateDay(day(0), attempts, nil)
	ws3Rows := int64(0)
	ws4Rows := int64(0)
	for i := range rows {
		switch {
		case rows[i].WorkspaceID != nil && *rows[i].WorkspaceID == 3:
			ws3Rows++
		case rows[i].WorkspaceID != nil && *rows[i].WorkspaceID == 4:
			ws4Rows++
		}
	}
	if ws3Rows != 1 || ws4Rows != 1 {
		t.Fatalf("workspace attribution broken: ws3 rows %d, ws4 rows %d (want 1/1)", ws3Rows, ws4Rows)
	}
	// The report builder groups by workspace: build ws3's report from its
	// rows only (what RollupsForDomain's scope filter would return) and the
	// ws4 permanent failure must not move the grade.
	var filtered []models.IPReputationRollup
	for i := range rows {
		if rows[i].WorkspaceID != nil && *rows[i].WorkspaceID == 3 {
			filtered = append(filtered, rows[i])
		}
	}
	rep := BuildDomainReport("dest.example", 14*24*time.Hour, day(0), day(1), filtered)
	// ws3's own delivered attempt is present, ws4's permanent failure is not:
	// no leak, and the grade reflects only ws3's (clean) data.
	if len(rep.IPs) != 1 || rep.IPs[0].Permanent != 0 || rep.IPs[0].Grade != GradeGood {
		t.Fatalf("ws3 report leaked ws4 transport failure: %+v", rep.IPs)
	}
}

// The service end-to-end over fakes: RollupDay writes exactly the aggregated
// rows through the repository seam, and the report reads them back scoped.
func TestServiceRollupAndReportRoundTrip(t *testing.T) {
	src := &fakeSource{
		attempts: []AttemptOutcomeRow{
			{WorkspaceID: ws3, SourceIP: "192.0.2.7", Domain: "dest.example", Outcome: models.MTAAttemptDelivered, At: at(0, 5)},
			{WorkspaceID: ws3, SourceIP: "192.0.2.7", Domain: "dest.example", Outcome: models.MTAAttemptPermanentFailure, At: at(0, 6)},
			{WorkspaceID: ws4, SourceIP: "192.0.2.7", Domain: "dest.example", Outcome: models.MTAAttemptDelivered, At: at(0, 7)},
		},
		events: []RecipientEventRow{
			{WorkspaceID: ws3, Domain: "dest.example", Type: models.EmailEventSent, At: at(0, 5)},
			{WorkspaceID: ws3, Domain: "dest.example", Type: models.EmailEventBounced, At: at(0, 6)},
		},
	}
	store := &fakeRollupStore{}
	svc := &Service{
		raw:  src,
		now:  func() time.Time { return day(1) },
	}
	svc.store = store

	n, err := svc.RollupDay(day(0))
	if err != nil {
		t.Fatalf("RollupDay: %v", err)
	}
	if n != 3 { // ws3 IP row + ws4 IP row + ws3 domain-wide event row
		t.Fatalf("rollup rows = %d, want 3", n)
	}
	if store.upserts != 1 {
		t.Fatalf("upsert batches = %d, want 1", store.upserts)
	}

	// The read path: inject the stored rows as the repository would return
	// them for ws3 (scope filter applied upstream) and check the report.
	stored := store.rows
	var scoped []models.IPReputationRollup
	for i := range stored {
		if stored[i].WorkspaceID != nil && *stored[i].WorkspaceID == 3 {
			scoped = append(scoped, stored[i])
		}
	}
	rep := BuildDomainReport("dest.example", 14*24*time.Hour, day(0), day(1), scoped)
	if len(rep.IPs) != 1 {
		t.Fatalf("report IPs = %d, want 1", len(rep.IPs))
	}
	ip := rep.IPs[0]
	if ip.Attempts != 2 || ip.Permanent != 1 || ip.Sent != 1 || ip.Bounced != 1 {
		t.Fatalf("round-trip counters wrong: %+v", ip)
	}
	if ip.HardFailRate != 50.0 {
		t.Fatalf("hard-fail rate = %v, want 50", ip.HardFailRate)
	}
	if ip.Grade != GradePoor {
		t.Fatalf("grade = %q, want poor (50%% transport hard-fail)", ip.Grade)
	}
}

// ---------- fakes ----------

type fakeSource struct {
	attempts []AttemptOutcomeRow
	events   []RecipientEventRow
	lastFrom time.Time
	lastTo   time.Time
	lastF    DomainFilter
}

func (f *fakeSource) AttemptOutcomes(from, to time.Time, fl DomainFilter) ([]AttemptOutcomeRow, error) {
	f.lastFrom, f.lastTo, f.lastF = from, to, fl
	var out []AttemptOutcomeRow
	for _, a := range f.attempts {
		if a.At.Before(from) || !a.At.Before(to) {
			continue
		}
		if fl.WorkspaceID != nil && (a.WorkspaceID == nil || *a.WorkspaceID != *fl.WorkspaceID) {
			continue
		}
		if fl.Domain != "" && a.Domain != fl.Domain {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeSource) RecipientEvents(from, to time.Time, fl DomainFilter) ([]RecipientEventRow, error) {
	var out []RecipientEventRow
	for _, e := range f.events {
		if e.At.Before(from) || !e.At.Before(to) {
			continue
		}
		if fl.WorkspaceID != nil && (e.WorkspaceID == nil || *e.WorkspaceID != *fl.WorkspaceID) {
			continue
		}
		if fl.Domain != "" && e.Domain != fl.Domain {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

type fakeRollupStore struct {
	rows    []models.IPReputationRollup
	upserts int
}

func (f *fakeRollupStore) UpsertRollups(rows []models.IPReputationRollup) error {
	f.upserts++
	f.rows = append(f.rows, rows...)
	return nil
}

func (f *fakeRollupStore) RollupsForDomain(scope repositories.ResourceScope, domain string, from, to time.Time) ([]models.IPReputationRollup, error) {
	return f.rows, nil
}
