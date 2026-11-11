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

package observability

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// GAP-52: the sample Grafana dashboard must stay importable and must only
// reference metric families that actually exist on the registry it will be
// pointed at. A renamed family without a dashboard update fails here.
//
// The metrics package import is deliberate: its init() registers the posta_*
// families on the default registry — the universe the check validates panel
// expressions against (and the same registry /metrics and the OTLP exporter
// ship).

type dashTarget struct {
	Expr string `json:"expr"`
}

type dashPanel struct {
	Type    string       `json:"type"`
	Title   string       `json:"title"`
	Targets []dashTarget `json:"targets"`
	Panels  []dashPanel  `json:"panels"`
}

type dashboardDoc struct {
	Title  string      `json:"title"`
	UID    string      `json:"uid"`
	Panels []dashPanel `json:"panels"`
}

var metricToken = regexp.MustCompile(`\b(posta_[a-z0-9_]+)`)

// histogramSuffixes are the per-bucket series names a histogram family
// materializes in exposition (PromQL queries hit these, not the family name).
var histogramSuffixes = []string{"_bucket", "_count", "_sum"}

func readSampleDashboard(t *testing.T) dashboardDoc {
	t.Helper()
	raw, err := os.ReadFile("../../docs/observability/grafana-posta-overview.json")
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	var doc dashboardDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("dashboard JSON does not parse: %v", err)
	}
	return doc
}

// knownFamilies gathers the default registry after seeding every family with
// at least one sample (Vec families with no children and histograms with no
// observations materialize no family).
func knownFamilies(t *testing.T) map[string]bool {
	t.Helper()
	metrics.IncrementWebhookDelivery("seed")
	metrics.IncrementBounce("seed")
	metrics.IncrementInboundReceived("seed")
	metrics.IncrementInboundRejected("seed")
	metrics.IncrementMessageReceived("seed")
	metrics.IncrementAutomationNodeFailure("seed")
	metrics.SetQueueArchivedTasks("seed", 0)
	metrics.IncrementHTTPRequests("GET", "/seed", "200")
	metrics.ObserveHTTPDuration(0.01)
	metrics.ObserveWebhookDeliveryDuration(0.01)
	metrics.ObserveInboundIngestDuration(0.01)

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	known := map[string]bool{}
	for _, f := range families {
		known[f.GetName()] = true
	}
	return known
}

func TestGrafanaDashboardJSONIsValid(t *testing.T) {
	doc := readSampleDashboard(t)
	if doc.UID == "" || doc.Title == "" {
		t.Fatalf("dashboard missing uid/title: %q/%q", doc.UID, doc.Title)
	}
	if len(doc.Panels) == 0 {
		t.Fatal("dashboard has no panels")
	}

	known := knownFamilies(t)

	panelCount := 0
	exprCount := 0
	var checkExprs func(panelTitle string, targets []dashTarget)
	checkExprs = func(panelTitle string, targets []dashTarget) {
		for _, tgt := range targets {
			if tgt.Expr == "" {
				continue
			}
			exprCount++
			for _, m := range metricToken.FindAllStringSubmatch(tgt.Expr, -1) {
				name := m[1]
				if known[name] {
					continue
				}
				// Histogram panels query _bucket/_sum/_count series.
				matched := false
				for _, suf := range histogramSuffixes {
					base := name[:len(name)-len(suf)]
					if known[base] {
						matched = true
						break
					}
				}
				if !matched {
					t.Errorf("panel %q references unknown metric %q (expr: %s)", panelTitle, name, tgt.Expr)
				}
			}
		}
	}

	for _, panel := range doc.Panels {
		if panel.Type == "row" {
			continue
		}
		panelCount++
		checkExprs(panel.Title, panel.Targets)
		for _, sub := range panel.Panels {
			checkExprs(panel.Title, sub.Targets)
		}
	}

	if panelCount < 10 {
		t.Fatalf("dashboard has %d visual panels, want >= 10 (it must cover the family groups)", panelCount)
	}
	if exprCount == 0 {
		t.Fatal("dashboard has no PromQL expressions")
	}
}

// TestGrafanaDashboardCoversCoreFamilies: the sample dashboard must touch at
// least the traffic, delivery, queue-health, and inbound groups — GAP-52 is a
// usable starting point, not a stub with one panel.
func TestGrafanaDashboardCoversCoreFamilies(t *testing.T) {
	raw, err := os.ReadFile("../../docs/observability/grafana-posta-overview.json")
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	text := string(raw)
	for _, required := range []string{
		"posta_http_requests_total",
		"posta_emails_sent_total",
		"posta_emails_queue_size",
		"posta_bounces_total",
		"posta_webhook_deliveries_total",
		"posta_inbound_messages_received_total",
		"posta_queue_archived_tasks",
		"posta_active_workers",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("dashboard does not reference required family %q", required)
		}
	}
}
