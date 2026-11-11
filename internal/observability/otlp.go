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

// Package observability completes the Option B Phase 6 observability slice
// (W5 / OB-6): the OTLP push exporter (SF-93 / GAP-51 adjacent), the
// error-monitoring hook (GAP-51), and the sample Grafana dashboard (GAP-52).
// The metric-alert engine itself (SF-96) lives in internal/services/meticalert
// because it is a cron-driven service over settings + the event store.
//
// # OTLP push exporter (SF-93)
//
// OTLPEndpointFromEnv-style configuration arrives through internal/config:
// POSTA_OTLP_ENDPOINT (base URL of an OTLP/HTTP receiver, e.g.
// http://otel-collector:4318; EMPTY MEANS OFF — the exporter never ships
// anything by default), POSTA_OTLP_INTERVAL_SECS (push period, default 60)
// and POSTA_OTLP_HEADERS (comma-separated Name=Value pairs for collectors
// that require authentication).
//
// # Dependency choice (recorded for the ADR-009/OB-6 ledger)
//
// The exporter encodes the OTLP ExportMetricsServiceRequest protobuf wire
// format BY HAND with the Go standard library only (encoding/binary). The
// go.opentelemetry.io/otel/exporters/otlp module was deliberately NOT added:
// it pulls the OTel SDK plus the OpenTelemetry proto module tree for a job
// that amounts to re-encoding the metric families the Prometheus registry
// already exposes. The families are read through prometheus.Gatherer, whose
// dto.MetricFamily type comes from github.com/prometheus/client_model — an
// existing module-graph dependency of prometheus/client_golang (it becomes a
// direct import here; go.mod gains no new module). The wire encoding is
// pinned by TestOTLPExportPayloadShape, which decodes the emitted bytes back
// field by field against an httptest collector.
//
// Transport trust class: the OTLP endpoint is operator infrastructure set
// through the environment — the same trust class as POSTA_DB_HOST or
// POSTA_SYSTEM_SMTP_HOST — so it is NOT routed through the tenant-webhook
// SSRF guard (internal/services/webhook.ssrf). That guard exists for URLs
// less-privileged actors can store in the database; private-network
// collectors (the common self-hosted OTLP deployment) must keep working.
// The guard itself is untouched.
package observability

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const (
	// otlpTimeout bounds a single push; a hung collector must not stall
	// whatever goroutine runs the exporter.
	otlpTimeout = 10 * time.Second
	// OTLP/HTTP metrics path per the OpenTelemetry protocol specification.
	otlpMetricsPath = "/v1/metrics"
	// Protobuf is the required content type for the OTLP/HTTP protobuf
	// flavor (the JSON flavor is not implemented — protobuf is what every
	// collector accepts and the smaller encoding).
	otlpContentType = "application/x-protobuf"
	otlpUserAgent   = "Posta-OTLP/1.0"

	// OTLP enum AggregationTemporality: cumulative matches Prometheus
	// counters/histograms exactly (monotonic totals since process start).
	otlpTemporalityCumulative = 1

	defaultOTLPInterval = 60 * time.Second
	defaultServiceName  = "posta"
)

// OTLPOptions configures the push exporter. Endpoint empty = disabled.
type OTLPOptions struct {
	// Endpoint is the OTLP/HTTP receiver base URL, e.g.
	// "http://otel-collector:4318". Empty disables the exporter.
	Endpoint string
	// Interval is the push period. Zero selects 60s.
	Interval time.Duration
	// Headers are sent with every push (collector authentication).
	Headers map[string]string
	// ServiceName/ServiceVersion become the resource attributes
	// service.name / service.version on every payload.
	ServiceName    string
	ServiceVersion string
	// Gatherer defaults to prometheus.DefaultGatherer (the same registry
	// /metrics exposes), so OTLP ships exactly the families operators see
	// on the OpenMetrics endpoint. Tests inject a custom gatherer.
	Gatherer prometheus.Gatherer
	// Client defaults to a plain http.Client with otlpTimeout. Tests inject
	// one pointed at an httptest server.
	Client *http.Client
}

// ParseOTLPHeaders parses the POSTA_OTLP_HEADERS value: comma-separated
// Name=Value pairs. Surrounding whitespace is trimmed. A pair without '=' is
// an error (fail-closed: a silently dropped auth header would turn into a
// 401 the operator has to hunt for).
func ParseOTLPHeaders(raw string) (map[string]string, error) {
	headers := make(map[string]string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return headers, nil
	}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, found := strings.Cut(pair, "=")
		if !found || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid OTLP header %q (want Name=Value)", pair)
		}
		headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return headers, nil
}

// OTLPExporter pushes the Prometheus registry to an OTLP/HTTP collector.
type OTLPExporter struct {
	opts OTLPOptions
	url  string
}

// NewOTLPExporter validates the options and returns an exporter. The endpoint
// must parse as an http(s) URL; an invalid endpoint is an error (config
// mistake — fail loud at startup, not silently in the push loop).
func NewOTLPExporter(opts OTLPOptions) (*OTLPExporter, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("otlp: endpoint is empty (exporter disabled)")
	}
	base, err := url.Parse(opts.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("otlp: invalid endpoint %q: %w", opts.Endpoint, err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("otlp: endpoint scheme must be http or https, got %q", base.Scheme)
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultOTLPInterval
	}
	if opts.Gatherer == nil {
		opts.Gatherer = prometheus.DefaultGatherer
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: otlpTimeout}
	}
	if opts.ServiceName == "" {
		opts.ServiceName = defaultServiceName
	}
	return &OTLPExporter{
		opts: opts,
		url:  strings.TrimRight(opts.Endpoint, "/") + otlpMetricsPath,
	}, nil
}

// ExportOnce gathers the registry and pushes one ExportMetricsServiceRequest.
func (e *OTLPExporter) ExportOnce(ctx context.Context) error {
	families, err := e.opts.Gatherer.Gather()
	if err != nil {
		return fmt.Errorf("otlp: gather: %w", err)
	}
	now := time.Now().UnixNano()
	payload := encodeExportMetricsServiceRequest(families, now, e.opts.ServiceName, e.opts.ServiceVersion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("otlp: build request: %w", err)
	}
	req.Header.Set("Content-Type", otlpContentType)
	req.Header.Set("User-Agent", otlpUserAgent)
	for k, v := range e.opts.Headers {
		req.Header.Set(k, v)
	}

	resp, err := e.opts.Client.Do(req)
	if err != nil {
		return fmt.Errorf("otlp: push to %s: %w", e.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain a bounded amount of the body so the connection is reusable.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("otlp: collector answered HTTP %d", resp.StatusCode)
	}
	return nil
}

// StartOTLPPusher runs the exporter on a ticker until the returned stop func
// is called (or ctx is cancelled). A disabled (empty-endpoint) configuration
// returns a no-op stop. The first push happens after one interval, not at
// startup — boot must never block on the collector. Push errors are logged
// and retried next tick: metrics shipping is best-effort and must not take
// the server or worker down.
func StartOTLPPusher(ctx context.Context, opts OTLPOptions) (stop func(), err error) {
	if opts.Endpoint == "" {
		return func() {}, nil
	}
	exp, err := NewOTLPExporter(opts)
	if err != nil {
		return func() {}, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(exp.opts.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				pushCtx, pushCancel := context.WithTimeout(runCtx, otlpTimeout+5*time.Second)
				if err := exp.ExportOnce(pushCtx); err != nil {
					logger.Warn("otlp push failed (retrying next interval)", "error", err)
				}
				pushCancel()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		})
	}, nil
}

// encodeExportMetricsServiceRequest builds the OTLP payload:
//
//	ExportMetricsServiceRequest { repeated ResourceMetrics resource_metrics = 1; }
//
// One ResourceMetrics with one ScopeMetrics carrying every gathered family.
// Field numbers below are the opentelemetry/proto/metrics/v1 and
// opentelemetry/proto/collector/metrics/v1 definitions this encoding follows.
func encodeExportMetricsServiceRequest(families []*dto.MetricFamily, nowNanos int64, serviceName, serviceVersion string) []byte {
	w := &pbWriter{}
	w.message(1, func(rm *pbWriter) { // resource_metrics
		rm.message(1, func(res *pbWriter) { // resource
			res.keyValueString(1, "service.name", serviceName)
			if serviceVersion != "" {
				res.keyValueString(1, "service.version", serviceVersion)
			}
		})
		rm.message(2, func(sm *pbWriter) { // scope_metrics
			sm.message(1, func(scope *pbWriter) { // scope
				scope.stringField(1, "github.com/goposta/posta/internal/observability")
			})
			for _, mf := range families {
				encodeMetric(sm, mf, nowNanos)
			}
		})
	})
	return w.bytes()
}

// encodeMetric maps one Prometheus family onto its OTLP data shape:
// counter→Sum (monotonic, cumulative), gauge/untyped→Gauge,
// histogram→Histogram, summary→Summary.
func encodeMetric(w *pbWriter, mf *dto.MetricFamily, nowNanos int64) {
	// Families materialize only once they have samples; skip empties so the
	// payload carries no zero-sample metrics.
	if len(mf.GetMetric()) == 0 {
		return
	}
	name := mf.GetName()
	help := mf.GetHelp()

	w.message(2, func(m *pbWriter) { // metrics
		m.stringField(1, name)
		if help != "" {
			m.stringField(2, help)
		}
		switch mf.GetType() {
		case dto.MetricType_COUNTER:
			m.message(7, func(sum *pbWriter) { // sum
				for _, metric := range mf.GetMetric() {
					sum.message(1, func(dp *pbWriter) {
						encodeNumberDataPoint(dp, metric.GetLabel(), nowNanos, metric.GetCounter().GetValue())
					})
				}
				sum.enumField(2, otlpTemporalityCumulative)
				sum.boolField(3, true) // counters are monotonic
			})
		case dto.MetricType_HISTOGRAM:
			m.message(9, func(h *pbWriter) { // histogram
				for _, metric := range mf.GetMetric() {
					hg := metric.GetHistogram()
					h.message(1, func(dp *pbWriter) {
						dp.fixed64Field(2, 0)                // start_time_unix_nano (unknown for Prometheus)
						dp.fixed64Field(3, uint64(nowNanos)) // time_unix_nano
						dp.fixed64Field(4, hg.GetSampleCount())
						dp.doubleField(5, hg.GetSampleSum())
						// Packed repeated fixed64 bucket counts. OTLP requires
						// len(bucket_counts) == len(explicit_bounds)+1 and
						// counts are CUMULATIVE, so the final entry (the +Inf
						// bucket) is the total sample count — exactly
						// HistogramDataPoint.count. client_golang's DTO omits
						// the +Inf bucket (SampleCount covers it); it is
						// appended here.
						var counts []uint64
						for _, b := range hg.GetBucket() {
							counts = append(counts, b.GetCumulativeCount())
						}
						counts = append(counts, hg.GetSampleCount())
						dp.packedFixed64(6, counts)
						// ...and packed repeated double explicit bounds (the
						// +Inf bound stays implicit in OTLP).
						var bounds []float64
						for _, b := range hg.GetBucket() {
							if b.UpperBound != nil && !math.IsInf(b.GetUpperBound(), +1) {
								bounds = append(bounds, b.GetUpperBound())
							}
						}
						dp.packedDouble(7, bounds)
						encodeLabels(dp, metric.GetLabel())
					})
				}
				h.enumField(2, otlpTemporalityCumulative)
			})
		case dto.MetricType_SUMMARY:
			m.message(11, func(s *pbWriter) { // summary
				for _, metric := range mf.GetMetric() {
					sm := metric.GetSummary()
					s.message(1, func(dp *pbWriter) {
						dp.fixed64Field(2, 0)
						dp.fixed64Field(3, uint64(nowNanos))
						dp.fixed64Field(4, sm.GetSampleCount())
						dp.doubleField(5, sm.GetSampleSum())
						for _, q := range sm.GetQuantile() {
							dp.message(6, func(qv *pbWriter) {
								qv.doubleField(1, q.GetQuantile())
								qv.doubleField(2, q.GetValue())
							})
						}
						encodeLabels(dp, metric.GetLabel())
					})
				}
			})
		default: // GAUGE and UNTYPED → Gauge
			m.message(5, func(g *pbWriter) { // gauge
				for _, metric := range mf.GetMetric() {
					g.message(1, func(dp *pbWriter) {
						encodeNumberDataPoint(dp, metric.GetLabel(), nowNanos, metric.GetGauge().GetValue())
					})
				}
			})
		}
	})
}

func encodeNumberDataPoint(dp *pbWriter, labels []*dto.LabelPair, nowNanos int64, value float64) {
	dp.fixed64Field(2, 0)                // start_time_unix_nano (unknown: Prometheus gather carries no series start)
	dp.fixed64Field(3, uint64(nowNanos)) // time_unix_nano
	dp.doubleField(4, value)             // as_double
	encodeLabels(dp, labels)
}

func encodeLabels(dp *pbWriter, labels []*dto.LabelPair) {
	// Deterministic order: protobuf maps have no order, and a stable payload
	// makes collector-side diffs and the shape test reproducible.
	sorted := make([]*dto.LabelPair, len(labels))
	copy(sorted, labels)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].GetName() < sorted[j].GetName() })
	for _, lp := range sorted {
		dp.keyValueString(7, lp.GetName(), lp.GetValue())
	}
}

// pbWriter is a minimal protobuf wire-format writer covering exactly the
// OTLP subset this package needs: varint (int/bool/enum), 64-bit (fixed64,
// sfixed64, double) and length-delimited (string/bytes/message) fields,
// including packed repeated scalars.
type pbWriter struct {
	buf []byte
}

func (w *pbWriter) tag(field int, wireType byte) {
	w.varint(uint64(field)<<3 | uint64(wireType))
}

func (w *pbWriter) varint(v uint64) {
	var scratch [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(scratch[:], v)
	w.buf = append(w.buf, scratch[:n]...)
}

func (w *pbWriter) stringField(field int, s string) {
	w.tag(field, 2)
	w.varint(uint64(len(s)))
	w.buf = append(w.buf, s...)
}

func (w *pbWriter) bytesField(field int, b []byte) {
	w.tag(field, 2)
	w.varint(uint64(len(b)))
	w.buf = append(w.buf, b...)
}

// message appends field = { nested }, emitting nothing when the callback
// writes no bytes (protobuf: an absent message field and an empty one are
// equivalent on the wire; skipping keeps payloads tight).
func (w *pbWriter) message(field int, fn func(*pbWriter)) {
	nested := &pbWriter{}
	fn(nested)
	if len(nested.buf) == 0 {
		return
	}
	w.tag(field, 2)
	w.varint(uint64(len(nested.buf)))
	w.buf = append(w.buf, nested.buf...)
}

func (w *pbWriter) doubleField(field int, v float64) {
	w.tag(field, 1) // 64-bit
	w.buf = binary.LittleEndian.AppendUint64(w.buf, math.Float64bits(v))
}

func (w *pbWriter) fixed64Field(field int, v uint64) {
	w.tag(field, 1)
	w.buf = binary.LittleEndian.AppendUint64(w.buf, v)
}

func (w *pbWriter) boolField(field int, v bool) {
	w.tag(field, 0)
	if v {
		w.varint(1)
	} else {
		w.varint(0)
	}
}

func (w *pbWriter) enumField(field int, v int32) {
	w.tag(field, 0)
	w.varint(uint64(v))
}

// packedFixed64 writes a packed repeated fixed64 (wire type 2 length-delimited
// scalar run), the proto3 default for numeric repeated fields.
func (w *pbWriter) packedFixed64(field int, values []uint64) {
	if len(values) == 0 {
		return
	}
	var run []byte
	for _, v := range values {
		run = binary.LittleEndian.AppendUint64(run, v)
	}
	w.bytesField(field, run)
}

func (w *pbWriter) packedDouble(field int, values []float64) {
	if len(values) == 0 {
		return
	}
	var run []byte
	for _, v := range values {
		run = binary.LittleEndian.AppendUint64(run, math.Float64bits(v))
	}
	w.bytesField(field, run)
}

// keyValueString writes KeyValue { key = 1; AnyValue { string_value = 1 } }.
func (w *pbWriter) keyValueString(field int, key, value string) {
	w.message(field, func(kv *pbWriter) {
		kv.stringField(1, key)
		kv.message(2, func(av *pbWriter) {
			av.stringField(1, value)
		})
	})
}

func (w *pbWriter) bytes() []byte { return w.buf }
