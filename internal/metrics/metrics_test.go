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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// The R-12/R-13 counters are wired into the default registry (the /metrics
// endpoint gathers it), and every increment helper must move the counter it
// documents. These tests read families back out of prometheus.DefaultGatherer,
// so a counter dropped from the init() registration — or an increment helper
// pointed at the wrong family — fails here.

// counterValue returns the current sum of one counter family (across all
// label sets for the CounterVec).
func counterValue(t *testing.T, name string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		sum := 0.0
		for _, m := range mf.GetMetric() {
			sum += m.GetCounter().GetValue()
		}
		return sum
	}
	t.Fatalf("metric family %q is not registered", name)
	return 0
}

func assertDelta(t *testing.T, name string, before, after float64, want float64) {
	t.Helper()
	if after-before != want {
		t.Fatalf("%s moved %v → %v (delta %v), want delta %v", name, before, after, after-before, want)
	}
}

// Every R-12/R-13 family must be registered with the default registry so the
// /metrics endpoint exposes it. The node-failure CounterVec materializes a
// family only once it has a labeled child (prometheus semantics), so the test
// seeds one child before gathering.
func TestR12R13FamiliesRegistered(t *testing.T) {
	IncrementAutomationNodeFailure("registration-probe")
	want := []string{
		"posta_campaigns_started_total",
		"posta_campaigns_completed_total",
		"posta_campaign_messages_queued_total",
		"posta_automation_journeys_enrolled_total",
		"posta_automation_journeys_completed_total",
		"posta_automation_journeys_exited_total",
		"posta_automation_journeys_failed_total",
		"posta_automation_node_failures_total",
		"posta_automation_schedule_fires_total",
	}
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	registered := map[string]bool{}
	for _, mf := range families {
		registered[mf.GetName()] = true
	}
	for _, name := range want {
		if !registered[name] {
			t.Errorf("metric family %q is not registered", name)
		}
	}
}

func TestCampaignCountersIncrement(t *testing.T) {
	before := counterValue(t, "posta_campaigns_started_total")
	IncrementCampaignStarted()
	assertDelta(t, "posta_campaigns_started_total", before, counterValue(t, "posta_campaigns_started_total"), 1)

	before = counterValue(t, "posta_campaigns_completed_total")
	IncrementCampaignCompleted()
	assertDelta(t, "posta_campaigns_completed_total", before, counterValue(t, "posta_campaigns_completed_total"), 1)

	before = counterValue(t, "posta_campaign_messages_queued_total")
	AddCampaignMessagesQueued(3)
	assertDelta(t, "posta_campaign_messages_queued_total", before, counterValue(t, "posta_campaign_messages_queued_total"), 3)

	// Non-positive batch adds must be no-ops.
	before = counterValue(t, "posta_campaign_messages_queued_total")
	AddCampaignMessagesQueued(0)
	AddCampaignMessagesQueued(-2)
	assertDelta(t, "posta_campaign_messages_queued_total", before, counterValue(t, "posta_campaign_messages_queued_total"), 0)
}

func TestAutomationJourneyCountersIncrement(t *testing.T) {
	cases := []struct {
		name string
		bump func()
	}{
		{"posta_automation_journeys_enrolled_total", IncrementAutomationJourneyEnrolled},
		{"posta_automation_journeys_completed_total", IncrementAutomationJourneyCompleted},
		{"posta_automation_journeys_exited_total", IncrementAutomationJourneyExited},
		{"posta_automation_journeys_failed_total", IncrementAutomationJourneyFailed},
	}
	for _, tc := range cases {
		before := counterValue(t, tc.name)
		tc.bump()
		assertDelta(t, tc.name, before, counterValue(t, tc.name), 1)
	}
}

// The node-failure CounterVec must break out by node type.
func TestAutomationNodeFailureCounterByType(t *testing.T) {
	before := counterValue(t, "posta_automation_node_failures_total")
	IncrementAutomationNodeFailure("email")
	after := counterValue(t, "posta_automation_node_failures_total")
	if after-before != 1 {
		t.Fatalf("node failure counter moved %v → %v, want +1", before, after)
	}

	// The per-type breakdown is exposed on the family.
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	found := false
	for _, mf := range families {
		if mf.GetName() != "posta_automation_node_failures_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "node_type" && l.GetValue() == "email" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("node failure family must expose a node_type=email label set")
	}
}

func TestAutomationScheduleFireCounterIncrements(t *testing.T) {
	before := counterValue(t, "posta_automation_schedule_fires_total")
	IncrementAutomationScheduleFire()
	assertDelta(t, "posta_automation_schedule_fires_total", before, counterValue(t, "posta_automation_schedule_fires_total"), 1)
}
