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

package meticalert

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/prometheus/client_golang/prometheus"
)

// ---- stubs -----------------------------------------------------------------

type stubSettings struct {
	value string
}

func (s *stubSettings) GetString(_, _ string) string { return s.value }

type eventRecord struct {
	Category models.EventCategory
	Type     string
	Message  string
	Outcome  string
	TargetID string
}

type recordingEvents struct {
	mu     sync.Mutex
	events []eventRecord
}

func (r *recordingEvents) Create(e *models.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, eventRecord{
		Category: e.Category,
		Type:     e.Type,
		Message:  e.Message,
		Outcome:  e.Outcome,
		TargetID: e.TargetID,
	})
	return nil
}

func (r *recordingEvents) countByType(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

type recordingMailer struct {
	mu      sync.Mutex
	sentTo  []string
	subject string
	data    map[string]any
}

func (m *recordingMailer) Send(to, subject, _ string, data map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentTo = append(m.sentTo, to)
	m.subject = subject
	m.data = data
	return nil
}

func (m *recordingMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sentTo)
}

// ---- harness ---------------------------------------------------------------

type webhookRecorder struct {
	mu     sync.Mutex
	bodies []alertPayload
	sigs   []string
}

func (w *webhookRecorder) handler() http.HandlerFunc {
	return func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var p alertPayload
		_ = json.Unmarshal(body, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.bodies = append(w.bodies, p)
		w.sigs = append(w.sigs, req.Header.Get("X-Posta-Signature"))
		rw.WriteHeader(http.StatusOK)
	}
}

type testHarness struct {
	engine   *Engine
	events   *recordingEvents
	mailer   *recordingMailer
	registry *prometheus.Registry
	webhooks *webhookRecorder
	now      time.Time
}

// newTestHarness wires an engine against a custom registry, a stub settings
// holder, event recorder, and mailer, with a live loopback webhook receiver.
// The SSRF policy seam is relaxed exactly like the webhook dispatcher's own
// tests do (loopback is policy-blocked in production); the production default
// stays webhook.ValidateWebhookURL.
func newTestHarness(t *testing.T, rulesJSON string) *testHarness {
	t.Helper()
	reg := prometheus.NewRegistry()
	rec := &recordingEvents{}
	mailer := &recordingMailer{}
	wh := &webhookRecorder{}

	eng := NewEngine(&stubSettings{value: rulesJSON}, rec, mailer)
	eng.SetGatherer(reg)
	eng.SetURLPolicy(func(string) error { return nil })

	h := &testHarness{engine: eng, events: rec, mailer: mailer, registry: reg, webhooks: wh}
	h.now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	eng.SetNow(func() time.Time { return h.now })
	return h
}

// webhookServer exposes the harness's webhook receiver over httptest and
// points the engine's client at it.
func (h *testHarness) webhookServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(h.webhooks.handler())
	t.Cleanup(srv.Close)
	h.engine.SetHTTPClient(srv.Client())
	return srv.URL
}

// advance moves the harness clock and runs one evaluation tick.
func (h *testHarness) advance(t *testing.T, d time.Duration) {
	t.Helper()
	h.now = h.now.Add(d)
	if err := h.engine.Evaluate(context.Background()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
}

// ---- registry fixtures -----------------------------------------------------

func addCounter(t *testing.T, reg *prometheus.Registry, name string, v float64) {
	t.Helper()
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: name})
	c.Add(v)
	reg.MustRegister(c)
}

func addGauge(t *testing.T, reg *prometheus.Registry, name string, v float64) {
	t.Helper()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name})
	g.Set(v)
	reg.MustRegister(g)
}

func jsonFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// ---- tests -----------------------------------------------------------------

// TestAlertMatrixConditions is the condition table in both directions: each
// comparison must fire when a series satisfies it and must NOT fire (no
// event) when it does not.
func TestAlertMatrixConditions(t *testing.T) {
	cases := []struct {
		condition string
		threshold float64
		value     float64
		wantFire  bool
	}{
		{"gt", 100, 101, true},
		{"gt", 100, 100, false},
		{"gt", 100, 99, false},
		{"gte", 100, 100, true},
		{"gte", 100, 99.99, false},
		{"lt", 5, 4.9, true},
		{"lt", 5, 5, false},
		{"lte", 5, 5, true},
		{"lte", 5, 5.1, false},
		{"eq", 42, 42, true},
		{"eq", 42, 41.999, false},
		{"neq", 42, 43, true},
		{"neq", 42, 42, false},
	}
	for _, tc := range cases {
		t.Run(tc.condition, func(t *testing.T) {
			rules := `[{"id":"m","name":"matrix","metric":"test_value","condition":"` + tc.condition +
				`","threshold":` + jsonFloat(tc.threshold) + `,"duration_seconds":0,"enabled":true}]`
			h := newTestHarness(t, rules)
			addGauge(t, h.registry, "test_value", tc.value)
			h.advance(t, 0)

			got := h.events.countByType("metric_alert.fired")
			if tc.wantFire && got != 1 {
				t.Fatalf("condition %s %v with value %v: fired events = %d, want 1", tc.condition, tc.threshold, tc.value, got)
			}
			if !tc.wantFire && got != 0 {
				t.Fatalf("condition %s %v with value %v: fired events = %d, want 0", tc.condition, tc.threshold, tc.value, got)
			}
		})
	}
}

// TestAlertDurationGate: duration_seconds=300 — the breach alone must not
// fire; once the duration has held, the next tick fires; a still-breaching
// rule must not re-fire; a cleared rule resolves exactly once; a re-breach
// starts a fresh episode with a fresh duration.
func TestAlertDurationGate(t *testing.T) {
	rules := `[{"id":"backlog","name":"Backlog","metric":"test_depth","condition":"gt","threshold":100,"duration_seconds":300,"enabled":true}]`
	h := newTestHarness(t, rules)
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_depth"})
	g.Set(500)
	h.registry.MustRegister(g)

	// Tick 0: breach begins — held 0s, no fire.
	h.advance(t, 0)
	if got := h.events.countByType("metric_alert.fired"); got != 0 {
		t.Fatalf("fired before duration held: %d events, want 0", got)
	}
	// +299s: still under the duration.
	h.advance(t, 299*time.Second)
	if got := h.events.countByType("metric_alert.fired"); got != 0 {
		t.Fatalf("fired at held=299s: %d events, want 0", got)
	}
	// +360s total: duration held — exactly one firing.
	h.advance(t, 61*time.Second)
	if got := h.events.countByType("metric_alert.fired"); got != 1 {
		t.Fatalf("fired at held=360s: %d events, want 1", got)
	}
	// Still breaching: no re-fire per tick.
	h.advance(t, time.Minute)
	if got := h.events.countByType("metric_alert.fired"); got != 1 {
		t.Fatalf("re-fired while still breaching: %d events, want 1", got)
	}
	// Condition clears: exactly one resolution, no email.
	g.Set(1)
	h.advance(t, time.Minute)
	if got := h.events.countByType("metric_alert.resolved"); got != 1 {
		t.Fatalf("resolved events = %d, want 1", got)
	}
	if got := h.mailer.count(); got != 0 {
		t.Fatalf("resolve emails = %d, want 0 (email fires only on firing)", got)
	}
	// Breach again after resolution: fresh episode, duration restarts.
	g.Set(999)
	h.advance(t, 0)
	if got := h.events.countByType("metric_alert.fired"); got != 1 {
		t.Fatalf("re-breach must not insta-fire (duration resets): %d events", got)
	}
	h.advance(t, 5*time.Minute)
	if got := h.events.countByType("metric_alert.fired"); got != 2 {
		t.Fatalf("second episode fires = %d, want 2", got)
	}
}

// TestAlertLabelFilterMismatch: a rule with a label filter counts only
// matching series — a breach on another label value must not fire it.
func TestAlertLabelFilterMismatch(t *testing.T) {
	rules := `[{"id":"dlq","name":"Dead letter","metric":"test_failed_total","labels":{"queue":"critical"},"condition":"gt","threshold":0,"duration_seconds":0,"enabled":true}]`
	h := newTestHarness(t, rules)
	// One family, two series: the breach exists on queue=bulk, not on the
	// filter's queue=critical.
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_failed_total"}, []string{"queue"})
	h.registry.MustRegister(vec)
	vec.WithLabelValues("bulk").Add(10)
	h.advance(t, 0)
	if got := h.events.countByType("metric_alert.fired"); got != 0 {
		t.Fatalf("label mismatch fired: %d events, want 0", got)
	}
	// A matching breaching series appears: fires.
	vec.WithLabelValues("critical").Add(2)
	h.advance(t, 0)
	if got := h.events.countByType("metric_alert.fired"); got != 1 {
		t.Fatalf("matching breach did not fire: %d events, want 1", got)
	}
}

// TestAlertCounterGaugeAndAbsentMetrics: counters alert on their cumulative
// value; a rule on a family absent from the gather never fires.
func TestAlertCounterGaugeAndAbsentMetrics(t *testing.T) {
	rules := `[{"id":"c","name":"Counter rule","metric":"test_done_total","condition":"gt","threshold":10,"duration_seconds":0,"enabled":true},
	           {"id":"a","name":"Absent rule","metric":"test_never_total","condition":"gt","threshold":0,"duration_seconds":0,"enabled":true}]`
	h := newTestHarness(t, rules)
	addCounter(t, h.registry, "test_done_total", 11)
	h.advance(t, 0)
	if got := h.events.countByType("metric_alert.fired"); got != 1 {
		t.Fatalf("counter rule fired events = %d, want 1", got)
	}
}

// TestAlertSkippedHistogramMetric: a rule on a histogram family must not fire
// and must not panic (documented limitation: histograms have no single
// alertable value).
func TestAlertSkippedHistogramMetric(t *testing.T) {
	rules := `[{"id":"hist","name":"Hist","metric":"test_latency","condition":"gt","threshold":0,"duration_seconds":0,"enabled":true}]`
	h := newTestHarness(t, rules)
	hist := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "test_latency"})
	hist.Observe(1)
	h.registry.MustRegister(hist)

	h.advance(t, 0)
	if got := h.events.countByType("metric_alert.fired"); got != 0 {
		t.Fatalf("histogram rule fired: %d events, want 0", got)
	}
}

// TestAlertDisabledAndRemovedRules: disabled rules never fire; removing a
// rule clears its episode silently (no resolve actions for a rule that no
// longer exists).
func TestAlertDisabledAndRemovedRules(t *testing.T) {
	set := &stubSettings{}
	h := newTestHarness(t, "")
	h.engine.settings = set
	addGauge(t, h.registry, "test_depth", 900)

	set.value = `[{"id":"x","name":"X","metric":"test_depth","condition":"gt","threshold":0,"duration_seconds":0,"enabled":false}]`
	h.advance(t, 0)
	if got := h.events.countByType("metric_alert.fired"); got != 0 {
		t.Fatalf("disabled rule fired: %d events, want 0", got)
	}

	set.value = `[{"id":"x","name":"X","metric":"test_depth","condition":"gt","threshold":0,"duration_seconds":0,"enabled":true}]`
	h.advance(t, 0)
	if got := h.events.countByType("metric_alert.fired"); got != 1 {
		t.Fatalf("enabled rule did not fire: %d events, want 1", got)
	}

	set.value = `[]`
	h.advance(t, 0)
	if got := h.events.countByType("metric_alert.resolved"); got != 0 {
		t.Fatalf("removed rule produced resolve actions: %d events, want 0", got)
	}
}

// TestAlertFireActionsEventWebhookEmail is the actions gate: one firing
// episode must produce (1) a metric_alert.fired platform event with the right
// shape, (2) a webhook whose payload carries the rule, value and resolution
// flag and whose X-Posta-Signature verifies under the rule's secret (the
// shared webhook.SignBody scheme), (3) the optional email.
func TestAlertFireActionsEventWebhookEmail(t *testing.T) {
	h := newTestHarness(t, "")
	whURL := h.webhookServer(t)
	rules := `[{"id":"q1","name":"Queue fire","metric":"test_depth","condition":"gte","threshold":10,"duration_seconds":0,"enabled":true,
	            "webhook_url":"` + whURL + `","webhook_secret":"s3cret","email":"ops@example.com"}]`
	h.engine.settings = &stubSettings{value: rules}

	addGauge(t, h.registry, "test_depth", 55)
	h.advance(t, 0)

	if got := h.events.countByType("metric_alert.fired"); got != 1 {
		t.Fatalf("fired events = %d, want 1", got)
	}
	h.events.mu.Lock()
	fired := h.events.events[0]
	h.events.mu.Unlock()
	if fired.Category != models.EventCategorySystem || fired.Outcome != "failure" || fired.TargetID != "q1" {
		t.Fatalf("fired event shape wrong: %+v", fired)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(h.webhookBodies()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	bodies := h.webhookBodies()
	if len(bodies) != 1 {
		t.Fatalf("webhook deliveries = %d, want 1", len(bodies))
	}
	p := bodies[0]
	if p.Event != "metric_alert.fired" || p.RuleID != "q1" || p.Value != 55 || p.Resolved {
		t.Fatalf("webhook payload wrong: %+v", p)
	}
	body, _ := json.Marshal(p)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	wantSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := h.webhookSigs()[0]; got != wantSig {
		t.Fatalf("webhook signature = %q, want %q (webhook.SignBody scheme)", got, wantSig)
	}

	if got := h.mailer.count(); got != 1 {
		t.Fatalf("alert emails = %d, want 1", got)
	}
	if h.mailer.subject != "Metric alert fired: Queue fire" {
		t.Fatalf("email subject = %q", h.mailer.subject)
	}
}

// TestAlertResolveWebhook: the resolution episode also POSTs a webhook, with
// resolved=true.
func TestAlertResolveWebhook(t *testing.T) {
	h := newTestHarness(t, "")
	whURL := h.webhookServer(t)
	rules := `[{"id":"r","name":"Resolve me","metric":"test_depth","condition":"gt","threshold":10,"duration_seconds":0,"enabled":true,
	            "webhook_url":"` + whURL + `"}]`
	h.engine.settings = &stubSettings{value: rules}

	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_depth"})
	h.registry.MustRegister(g)

	g.Set(50)
	h.advance(t, 0) // fires
	g.Set(1)
	h.advance(t, time.Minute) // resolves

	if got := h.events.countByType("metric_alert.resolved"); got != 1 {
		t.Fatalf("resolved events = %d, want 1", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(h.webhookBodies()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	bodies := h.webhookBodies()
	if len(bodies) != 2 {
		t.Fatalf("webhook deliveries = %d, want 2 (fire + resolve)", len(bodies))
	}
	if !bodies[1].Resolved || bodies[1].Event != "metric_alert.resolved" {
		t.Fatalf("resolve payload wrong: %+v", bodies[1])
	}
}

// TestAlertInvalidRulesAreReported: malformed JSON and per-rule validation
// failures must surface as LoadRules errors (they land in the cron job's
// LastError), not silently vanish. An empty setting is legitimately rule-less.
func TestAlertInvalidRulesAreReported(t *testing.T) {
	h := newTestHarness(t, `{not json`)
	if _, err := h.engine.LoadRules(); err == nil {
		t.Fatal("malformed JSON must be an error")
	}
	h2 := newTestHarness(t, `[{"id":"bad","name":"B","metric":"m","condition":"bogus","threshold":1,"enabled":true}]`)
	rules, err := h2.engine.LoadRules()
	if err == nil {
		t.Fatal("unknown condition must be an error")
	}
	if len(rules) != 0 {
		t.Fatalf("invalid rules must be dropped, got %d", len(rules))
	}
	h3 := newTestHarness(t, "")
	if rules, err := h3.engine.LoadRules(); err != nil || len(rules) != 0 {
		t.Fatalf("empty setting = no rules, no error; got (%v, %v)", rules, err)
	}
}

// ---- harness helpers -------------------------------------------------------

func (h *testHarness) webhookBodies() []alertPayload {
	h.webhooks.mu.Lock()
	defer h.webhooks.mu.Unlock()
	return append([]alertPayload(nil), h.webhooks.bodies...)
}

func (h *testHarness) webhookSigs() []string {
	h.webhooks.mu.Lock()
	defer h.webhooks.mu.Unlock()
	return append([]string(nil), h.webhooks.sigs...)
}
