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

package jobs

import (
	"context"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/meticalert"
	"github.com/prometheus/client_golang/prometheus"
)

// hookingEvents records what the engine persists through the job run.
type hookingEvents struct {
	mu   sync.Mutex
	n    int
	fail bool
}

func (h *hookingEvents) Create(_ *models.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail {
		return errStub
	}
	h.n++
	return nil
}

type stubErr struct{}

func (stubErr) Error() string { return "stub" }

var errStub = stubErr{}

type hookingSettings struct{ v string }

func (s *hookingSettings) GetString(_, _ string) string { return s.v }

// TestMetricAlertJobImplementsCronJob: name/schedule contract — every minute,
// so a duration_seconds alert never waits more than one tick past its hold.
func TestMetricAlertJobImplementsCronJob(t *testing.T) {
	job := NewMetricAlertJob(nil)
	if job.Name() == "" {
		t.Fatal("job must have a name")
	}
	if job.Schedule() != "* * * * *" {
		t.Fatalf("schedule = %q, want every minute (* * * * *)", job.Schedule())
	}
	// Nil engine (defensive wiring) is a no-op, not a panic.
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("nil engine Run: %v", err)
	}
}

// TestMetricAlertJobRunsEngine: one Run = one evaluation — a breaching rule
// with duration 0 fires exactly once through the job wrapper.
func TestMetricAlertJobRunsEngine(t *testing.T) {
	rec := &hookingEvents{}
	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_depth"})
	g.Set(99)
	reg.MustRegister(g)

	eng := meticalert.NewEngine(
		&hookingSettings{v: `[{"id":"j","name":"J","metric":"test_depth","condition":"gt","threshold":0,"duration_seconds":0,"enabled":true}]`},
		rec, nil)
	eng.SetGatherer(reg)

	job := NewMetricAlertJob(eng)
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.n != 1 {
		t.Fatalf("events recorded through job run = %d, want 1", rec.n)
	}
}
