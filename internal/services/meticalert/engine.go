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

// Package meticalert implements the SF-96 metric-alert engine (OB-6 W5):
// alert rules — metric name, label filter, condition, threshold, duration —
// stored as a JSON array in the platform setting `metric_alert_rules`,
// evaluated on a cron (internal/cron/jobs/metric_alert.go, every minute).
//
// When a rule's condition has held continuously for its duration, the engine
// FIRES it and executes the configured actions in this order:
//
//  1. event   — a models.Event row (category system, type metric_alert.fired)
//     on the platform activity timeline;
//  2. webhook — a JSON POST (metric_alert.fired payload) signed with the
//     SAME HMAC machinery as delivery webhooks (X-Posta-Signature:
//     sha256=<hex>, internal/services/webhook.SignBody) and sent
//     through the SAME SSRF gate (webhook.ValidateWebhookURL +
//     guarded transport). The guard is reused, never weakened:
//     rule webhook URLs live in the settings table, so they get
//     exactly the protection user-stored webhook URLs get;
//  3. email   — optional (rule.email) via the notification service's
//     metric_alert template.
//
// When the condition clears after a firing, the engine RESOLVES the rule
// (metric_alert.resolved event + webhook, no email). While the breach
// persists the rule stays firing — one fire per breach episode, not one per
// evaluation tick.
//
// Tenancy: platform metrics are deployment-wide, so rules and their events
// are platform-scoped. Rules are writable only through the admin-gated
// settings API (PUT /admin/settings, adminGroup) — a workspace user can
// neither read nor write them.
package meticalert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/jkaninda/logger"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// RulesSettingKey is the platform setting holding the JSON rule array.
// Admins manage it through the existing admin settings API; there is no
// dedicated route (the API surface stays minimal-additive).
const RulesSettingKey = "metric_alert_rules"

// Conditions.
const (
	CondGT  = "gt"
	CondGTE = "gte"
	CondLT  = "lt"
	CondLTE = "lte"
	CondEQ  = "eq"
	CondNEQ = "neq"
)

// Rule is one alert definition. JSON shape documented in
// docs/observability/README.md — this struct is the wire format of the
// `metric_alert_rules` setting value.
type Rule struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Metric        string            `json:"metric"`
	Labels        map[string]string `json:"labels,omitempty"`
	Condition     string            `json:"condition"`
	Threshold     float64           `json:"threshold"`
	DurationSecs  int               `json:"duration_seconds"`
	WebhookURL    string            `json:"webhook_url,omitempty"`
	WebhookSecret string            `json:"webhook_secret,omitempty"`
	Email         string            `json:"email,omitempty"`
	Enabled       bool              `json:"enabled"`
}

// validate rejects rules that cannot ever fire, so a typo fails visibly in
// the job log instead of sitting silently inert.
func (r Rule) validate() error {
	if r.ID == "" {
		return fmt.Errorf("rule id is required")
	}
	if r.Metric == "" {
		return fmt.Errorf("rule %s: metric is required", r.ID)
	}
	switch r.Condition {
	case CondGT, CondGTE, CondLT, CondLTE, CondEQ, CondNEQ:
	default:
		return fmt.Errorf("rule %s: unknown condition %q (want gt|gte|lt|lte|eq|neq)", r.ID, r.Condition)
	}
	if r.DurationSecs < 0 {
		return fmt.Errorf("rule %s: negative duration", r.ID)
	}
	return nil
}

// SettingsReader is the slice of the settings provider the engine needs
// (*settings.Provider satisfies it; tests stub it).
type SettingsReader interface {
	GetString(key, fallback string) string
}

// EventRecorder persists the platform event rows (*repositories.EventRepository
// satisfies it; tests stub it).
type EventRecorder interface {
	Create(event *models.Event) error
}

// AlertMailer sends the optional email action (*notification.Service
// satisfies it; tests stub it).
type AlertMailer interface {
	Send(to, subject, templateName string, data map[string]any) error
}

// ruleState is the engine's memory for one rule: when the current breach
// episode started and whether the firing actions already ran. In-memory only
// — a restart forgets breach history, so a duration timer restarts after a
// restart (documented; the alternative, persisting per-rule breach state,
// is not worth a new table for a minute-granularity evaluator).
type ruleState struct {
	breachSince time.Time
	firing      bool
}

// Engine evaluates the rules on every tick.
type Engine struct {
	settings SettingsReader
	events   EventRecorder
	notifier AlertMailer

	// gatherer defaults to prometheus.DefaultGatherer — the same registry
	// the /metrics endpoint and the OTLP exporter ship.
	gatherer prometheus.Gatherer

	// urlPolicy is the SSRF gate every rule webhook URL must pass. Production
	// uses webhook.ValidateWebhookURL (identical to user webhook delivery);
	// loopback httptest-based tests relax it — the same arrangement the
	// webhook dispatcher's own tests use.
	urlPolicy func(string) error
	// client posts rule webhooks. Production wraps webhook.NewGuardedTransport
	// (destination pinned to a validated public IP at dial time).
	client *http.Client

	// now is the clock seam; tests drive the duration logic through it.
	now func() time.Time

	mu    sync.Mutex
	state map[string]*ruleState
}

// NewEngine builds the engine. Any dependency may be nil: rules without the
// matching action simply skip it (nil events → no event rows, nil notifier →
// email actions are skipped, and the engine still evaluates and delivers
// webhooks).
func NewEngine(settings SettingsReader, events EventRecorder, notifier AlertMailer) *Engine {
	return &Engine{
		settings:  settings,
		events:    events,
		notifier:  notifier,
		gatherer:  prometheus.DefaultGatherer,
		urlPolicy: webhook.ValidateWebhookURL,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: webhook.NewGuardedTransport(nil),
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now:   time.Now,
		state: make(map[string]*ruleState),
	}
}

// SetGatherer overrides the metric source (tests).
func (e *Engine) SetGatherer(g prometheus.Gatherer) { e.gatherer = g }

// SetHTTPClient overrides the webhook poster (tests: an httptest server).
func (e *Engine) SetHTTPClient(c *http.Client) { e.client = c }

// SetURLPolicy overrides the SSRF gate (tests on loopback, mirroring the
// webhook dispatcher's test arrangement).
func (e *Engine) SetURLPolicy(p func(string) error) { e.urlPolicy = p }

// SetNow overrides the clock (tests: duration matrix).
func (e *Engine) SetNow(now func() time.Time) { e.now = now }

// LoadRules reads and parses the rule array from settings. A missing setting
// yields no rules (the engine idles); malformed JSON is an error so the cron
// job's LastError surfaces an admin typo instead of silently doing nothing.
// Rules that fail per-rule validation are dropped with a logged warning and
// reported here too, so ` Jobs()` shows the problem.
func (e *Engine) LoadRules() ([]Rule, error) {
	raw := ""
	if e.settings != nil {
		raw = e.settings.GetString(RulesSettingKey, "")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rules []Rule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %w", RulesSettingKey, err)
	}
	valid := make([]Rule, 0, len(rules))
	var errs []error
	for _, r := range rules {
		if err := r.validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		valid = append(valid, r)
	}
	if len(errs) > 0 {
		return valid, fmt.Errorf("%s: %d invalid rule(s): %v", RulesSettingKey, len(errs), errs[0])
	}
	return valid, nil
}

// Evaluate runs one pass: load rules, gather, advance every rule's breach
// state machine, execute firing/resolution actions. Returns an error only
// for engine-level failures (gather error, malformed rules JSON) so the cron
// manager's LastError field reflects them; per-rule action failures are
// logged and do not fail the tick (one dead webhook target must not stop the
// other rules from being evaluated).
func (e *Engine) Evaluate(ctx context.Context) error {
	rules, err := e.LoadRules()
	if err != nil {
		// Still evaluate the rules that parsed — a typo in rule 3 must not
		// silence rules 1 and 2.
		logger.Error("metric-alert: rule load reported problems", "error", err)
	}

	families, err := e.gatherer.Gather()
	if err != nil {
		return fmt.Errorf("metric-alert: gather: %w", err)
	}

	now := e.now().UTC()

	e.mu.Lock()
	defer e.mu.Unlock()

	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			delete(e.state, rule.ID)
			continue
		}
		seen[rule.ID] = true
		e.evaluateRule(ctx, rule, families, now)
	}
	// Forget state for rules that were removed or disabled so re-adding a
	// rule later starts a fresh breach episode.
	for id := range e.state {
		if !seen[id] {
			delete(e.state, id)
		}
	}
	return nil
}

// evaluateRule advances one rule's state machine. Caller holds e.mu.
func (e *Engine) evaluateRule(ctx context.Context, rule Rule, families []*dto.MetricFamily, now time.Time) {
	metric, breached, matched := findBreachedSeries(rule, families)
	if !breached {
		st, existed := e.state[rule.ID]
		if existed && st.firing {
			// Condition cleared after a firing episode: resolve.
			delete(e.state, rule.ID)
			e.recordResolved(ctx, rule)
			return
		}
		delete(e.state, rule.ID)
		return
	}

	st, existed := e.state[rule.ID]
	if !existed {
		st = &ruleState{breachSince: now}
		e.state[rule.ID] = st
	}
	if st.firing {
		return // still breached, actions already ran this episode
	}
	held := now.Sub(st.breachSince)
	// duration_seconds=0 means fire on the FIRST observed breach; a positive
	// duration requires the breach to have been held that long.
	if rule.DurationSecs > 0 && held < time.Duration(rule.DurationSecs)*time.Second {
		return // breach not held long enough yet
	}
	st.firing = true
	e.fire(ctx, rule, metric, matched, held)
}

// seriesValue is one matching time series of the rule's metric.
type seriesValue struct {
	labels map[string]string
	value  float64
}

// findBreachedSeries scans the gathered families for the rule's metric and
// returns the first (sorted, deterministic) matching series that breaches the
// condition. Histogram and summary families have no single scalar value —
// rules referencing them are skipped with a warning (documented limitation:
// alert on gauges and counters, or export the aggregation as a gauge).
func findBreachedSeries(rule Rule, families []*dto.MetricFamily) (string, bool, *seriesValue) {
	var candidates []seriesValue
	found := false
	for _, mf := range families {
		if mf.GetName() != rule.Metric {
			continue
		}
		found = true
		switch mf.GetType() {
		case dto.MetricType_COUNTER:
			for _, m := range mf.GetMetric() {
				if v := m.GetCounter().GetValue(); breaches(rule, m.GetLabel(), v) {
					candidates = append(candidates, seriesOf(m.GetLabel(), v))
				}
			}
		case dto.MetricType_GAUGE, dto.MetricType_UNTYPED:
			for _, m := range mf.GetMetric() {
				if v := m.GetGauge().GetValue(); breaches(rule, m.GetLabel(), v) {
					candidates = append(candidates, seriesOf(m.GetLabel(), v))
				}
			}
		default:
			logger.Warn("metric-alert: rule references a histogram/summary family, which has no single alertable value; skipping",
				"rule", rule.ID, "metric", rule.Metric)
		}
	}
	if !found {
		// Debug, not warn: a family with no labeled children does not
		// materialize in the registry, so a rule on a dormant counter is
		// normal idle behavior — warning every minute would be spam.
		logger.Debug("metric-alert: rule metric not present in this tick's gather", "rule", rule.ID, "metric", rule.Metric)
		return rule.Metric, false, nil
	}
	if len(candidates) == 0 {
		return rule.Metric, false, nil
	}
	// Deterministic pick: lexicographically smallest label set.
	sort.Slice(candidates, func(i, j int) bool {
		return fmt.Sprint(candidates[i].labels) < fmt.Sprint(candidates[j].labels)
	})
	return rule.Metric, true, &candidates[0]
}

func seriesOf(labels []*dto.LabelPair, value float64) seriesValue {
	m := make(map[string]string, len(labels))
	for _, lp := range labels {
		m[lp.GetName()] = lp.GetValue()
	}
	return seriesValue{labels: m, value: value}
}

// breaches reports whether any label-filtered series of the family breaches
// the condition. The label filter (rule.Labels) is an exact-match conjunction:
// every filter key must equal the series label for the series to count.
func breaches(rule Rule, labels []*dto.LabelPair, value float64) bool {
	if len(rule.Labels) > 0 {
		series := make(map[string]string, len(labels))
		for _, lp := range labels {
			series[lp.GetName()] = lp.GetValue()
		}
		for k, want := range rule.Labels {
			if series[k] != want {
				return false
			}
		}
	}
	return checkCondition(value, rule.Threshold, rule.Condition)
}

// checkCondition evaluates one comparison.
func checkCondition(value, threshold float64, condition string) bool {
	switch condition {
	case CondGT:
		return value > threshold
	case CondGTE:
		return value >= threshold
	case CondLT:
		return value < threshold
	case CondLTE:
		return value <= threshold
	case CondEQ:
		return value == threshold
	case CondNEQ:
		return value != threshold
	default:
		return false
	}
}

// alertPayload is the webhook JSON body for both firing and resolution.
type alertPayload struct {
	Event           string            `json:"event"`
	RuleID          string            `json:"rule_id"`
	RuleName        string            `json:"rule_name"`
	Metric          string            `json:"metric"`
	Labels          map[string]string `json:"labels,omitempty"`
	Value           float64           `json:"value"`
	Threshold       float64           `json:"threshold"`
	Condition       string            `json:"condition"`
	DurationSeconds int               `json:"duration_seconds"`
	HeldSeconds     float64           `json:"held_seconds"`
	FiredAt         string            `json:"fired_at"`
	Resolved        bool              `json:"resolved"`
}

// fire executes the three firing actions. Caller holds e.mu and has marked
// the rule firing, so this runs once per breach episode.
func (e *Engine) fire(ctx context.Context, rule Rule, metric string, matched *seriesValue, held time.Duration) {
	payload := alertPayload{
		Event:           "metric_alert.fired",
		RuleID:          rule.ID,
		RuleName:        rule.Name,
		Metric:          metric,
		Value:           0,
		Threshold:       rule.Threshold,
		Condition:       rule.Condition,
		DurationSeconds: rule.DurationSecs,
		HeldSeconds:     held.Seconds(),
		FiredAt:         e.now().UTC().Format(time.RFC3339),
		Resolved:        false,
	}
	if matched != nil {
		payload.Labels = matched.labels
		payload.Value = matched.value
	}

	// 1. Platform event (ADR-008 event substrate: the platform `events`
	// table, category system — the admin activity timeline).
	meta, _ := json.Marshal(map[string]any{
		"rule_id":   rule.ID,
		"metric":    metric,
		"value":     payload.Value,
		"threshold": rule.Threshold,
		"condition": rule.Condition,
		"held_secs": payload.HeldSeconds,
	})
	e.createEvent(ctx, &models.Event{
		Category:   models.EventCategorySystem,
		Type:       "metric_alert.fired",
		Message:    fmt.Sprintf("Metric alert %q fired: %s %s %v (value %v held %ds)", rule.Name, metric, rule.Condition, rule.Threshold, payload.Value, int(payload.HeldSeconds)),
		Metadata:   string(meta),
		Outcome:    "failure",
		TargetType: "metric_alert",
		TargetID:   rule.ID,
		CreatedAt:  e.now().UTC(),
	})

	// 2. Webhook (existing machinery: guarded transport + HMAC signature).
	e.deliverWebhook(rule, payload)

	// 3. Optional email.
	if rule.Email != "" && e.notifier != nil {
		data := map[string]any{
			"AlertName":  rule.Name,
			"RuleID":     rule.ID,
			"Metric":     metric,
			"Value":      fmt.Sprintf("%v", payload.Value),
			"Threshold":  fmt.Sprintf("%v", rule.Threshold),
			"Condition":  rule.Condition,
			"Duration":   rule.DurationSecs,
			"LabelsJSON": labelSummary(matched),
		}
		if err := e.notifier.Send(rule.Email, "Metric alert fired: "+rule.Name, "metric_alert", data); err != nil {
			logger.Error("metric-alert: alert email failed", "rule", rule.ID, "error", err)
		}
	}
}

// recordResolved emits the resolution event + webhook (no email: resolution
// notices ride the same event/webhook channels; documented in the package
// doc).
func (e *Engine) recordResolved(ctx context.Context, rule Rule) {
	payload := alertPayload{
		Event:           "metric_alert.resolved",
		RuleID:          rule.ID,
		RuleName:        rule.Name,
		Metric:          rule.Metric,
		Threshold:       rule.Threshold,
		Condition:       rule.Condition,
		DurationSeconds: rule.DurationSecs,
		FiredAt:         e.now().UTC().Format(time.RFC3339),
		Resolved:        true,
	}
	meta, _ := json.Marshal(map[string]any{"rule_id": rule.ID, "metric": rule.Metric})
	e.createEvent(ctx, &models.Event{
		Category:   models.EventCategorySystem,
		Type:       "metric_alert.resolved",
		Message:    fmt.Sprintf("Metric alert %q resolved: %s back within %s %v", rule.Name, rule.Metric, rule.Condition, rule.Threshold),
		Metadata:   string(meta),
		Outcome:    "success",
		TargetType: "metric_alert",
		TargetID:   rule.ID,
		CreatedAt:  e.now().UTC(),
	})
	e.deliverWebhook(rule, payload)
}

func (e *Engine) createEvent(_ context.Context, event *models.Event) {
	if e.events == nil {
		return
	}
	if err := e.events.Create(event); err != nil {
		logger.Error("metric-alert: failed to record event", "type", event.Type, "rule", event.TargetID, "error", err)
	}
}

// deliverWebhook POSTs the payload through the SAME path as delivery
// webhooks: static SSRF validation, guarded transport (destination pinned to
// a validated public IP at dial time), HMAC signature when a secret is set.
// Failures are logged and dropped — rule webhooks are fire-and-forget
// notifications, not a delivery ledger (the user-webhook delivery history
// machinery stays scoped to user webhooks).
func (e *Engine) deliverWebhook(rule Rule, payload alertPayload) {
	if rule.WebhookURL == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("metric-alert: failed to marshal webhook payload", "rule", rule.ID, "error", err)
		return
	}
	if e.urlPolicy != nil {
		if err := e.urlPolicy(rule.WebhookURL); err != nil {
			logger.Warn("metric-alert: webhook URL blocked by security policy", "rule", rule.ID, "error", err)
			return
		}
	}
	req, err := http.NewRequest(http.MethodPost, rule.WebhookURL, bytes.NewReader(body))
	if err != nil {
		logger.Error("metric-alert: failed to build webhook request", "rule", rule.ID, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Posta-MetricAlert/1.0")
	if rule.WebhookSecret != "" {
		req.Header.Set("X-Posta-Signature", "sha256="+webhook.SignBody(rule.WebhookSecret, body))
	}
	if e.client == nil {
		return
	}
	resp, err := e.client.Do(req)
	if err != nil {
		logger.Warn("metric-alert: webhook delivery failed", "rule", rule.ID, "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 300 {
		logger.Warn("metric-alert: webhook answered with error status", "rule", rule.ID, "status", resp.StatusCode)
	}
}

// labelSummary renders the matched series labels for the email body.
func labelSummary(matched *seriesValue) string {
	if matched == nil || len(matched.labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(matched.labels))
	for k := range matched.labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s=%s", k, matched.labels[k])
	}
	return out
}
