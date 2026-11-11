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
	"context"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ---- minimal protobuf wire reader (test side) ------------------------------

type pbField struct {
	num    int
	wire   int
	bytes  []byte // wire type 2
	varint uint64 // wire type 0
	fixed  uint64 // wire type 1 (64-bit)
}

func parsePB(t *testing.T, b []byte) []pbField {
	t.Helper()
	var fields []pbField
	i := 0
	for i < len(b) {
		key, n := binary.Uvarint(b[i:])
		if n <= 0 {
			t.Fatalf("bad protobuf: field key at offset %d", i)
		}
		i += n
		f := pbField{num: int(key >> 3), wire: int(key & 7)}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b[i:])
			if n <= 0 {
				t.Fatalf("bad protobuf: varint body for field %d", f.num)
			}
			f.varint = v
			i += n
		case 1:
			if i+8 > len(b) {
				t.Fatalf("bad protobuf: truncated 64-bit field %d", f.num)
			}
			f.fixed = binary.LittleEndian.Uint64(b[i:])
			i += 8
		case 5:
			if i+4 > len(b) {
				t.Fatalf("bad protobuf: truncated 32-bit field %d", f.num)
			}
			i += 4
		case 2:
			l, n := binary.Uvarint(b[i:])
			if n <= 0 || i+n+int(l) > len(b) {
				t.Fatalf("bad protobuf: truncated length-delimited field %d", f.num)
			}
			f.bytes = b[i+n : i+n+int(l)]
			i += n + int(l)
		default:
			t.Fatalf("bad protobuf: unsupported wire type %d", f.wire)
		}
		fields = append(fields, f)
	}
	return fields
}

func (f pbField) msg(t *testing.T) []pbField {
	if f.wire != 2 {
		t.Fatalf("field %d is not length-delimited", f.num)
	}
	return parsePB(t, f.bytes)
}

func (f pbField) str() string { return string(f.bytes) }

func (f pbField) double() float64 { return math.Float64frombits(f.fixed) }

func findField(fields []pbField, num int) *pbField {
	for i := range fields {
		if fields[i].num == num {
			return &fields[i]
		}
	}
	return nil
}

func allFields(fields []pbField, num int) []pbField {
	var out []pbField
	for _, f := range fields {
		if f.num == num {
			out = append(out, f)
		}
	}
	return out
}

// ---- fixtures --------------------------------------------------------------

// metricTestRegistry builds a registry with one counter, one labeled counter,
// one gauge, one histogram, and one summary — the four OTLP data shapes.
func metricTestRegistry(t *testing.T) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewRegistry()

	ctr := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "test_tasks_total",
		Help: "test counter",
	})
	ctr.Add(41)
	reg.MustRegister(ctr)

	labeled := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_labeled_total",
		Help: "test labeled counter",
	}, []string{"queue"})
	labeled.WithLabelValues("default").Add(7)
	reg.MustRegister(labeled)

	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "test_depth",
		Help: "test gauge",
	})
	gauge.Set(12.5)
	reg.MustRegister(gauge)

	hist := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "test_latency_seconds",
		Help:    "test histogram",
		Buckets: []float64{0.1, 1, 10},
	})
	hist.Observe(0.05)
	hist.Observe(0.5)
	hist.Observe(5)
	reg.MustRegister(hist)

	sum := prometheus.NewSummary(prometheus.SummaryOpts{
		Name:       "test_summary_seconds",
		Help:       "test summary",
		Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01},
	})
	sum.Observe(2)
	reg.MustRegister(sum)

	return reg
}

// ---- tests -----------------------------------------------------------------

func TestParseOTLPHeaders(t *testing.T) {
	if h, err := ParseOTLPHeaders(""); err != nil || len(h) != 0 {
		t.Fatalf("empty input: got (%v, %v), want empty map, nil", h, err)
	}
	h, err := ParseOTLPHeaders("Authorization=Basic abc, X-Tenant = posta ,")
	if err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if h["Authorization"] != "Basic abc" || h["X-Tenant"] != "posta" {
		t.Fatalf("parsed headers wrong: %v", h)
	}
	if _, err := ParseOTLPHeaders("NoEqualsSign"); err == nil {
		t.Fatal("header without '=' must be rejected (silent auth-header drop would 401 later)")
	}
	if _, err := ParseOTLPHeaders("=value"); err == nil {
		t.Fatal("header with empty name must be rejected")
	}
}

func TestOTLPExporterDisabledWithoutEndpoint(t *testing.T) {
	if _, err := NewOTLPExporter(OTLPOptions{}); err == nil {
		t.Fatal("empty endpoint must be rejected by NewOTLPExporter")
	}
	stop, err := StartOTLPPusher(context.Background(), OTLPOptions{})
	if err != nil {
		t.Fatalf("disabled pusher must not error: %v", err)
	}
	stop() // must be a safe no-op
}

func TestOTLPExporterRejectsBadEndpoint(t *testing.T) {
	if _, err := NewOTLPExporter(OTLPOptions{Endpoint: "ftp://collector:4318"}); err == nil {
		t.Fatal("non-http(s) scheme must be rejected")
	}
	if _, err := NewOTLPExporter(OTLPOptions{Endpoint: "http://[::1"}); err == nil {
		t.Fatal("unparseable endpoint must be rejected")
	}
}

// TestOTLPExportPayloadShape is the OTLP slice's core gate: the exporter must
// POST a parseable ExportMetricsServiceRequest to <endpoint>/v1/metrics with
// the right content type, carrying every family in the expected OTLP data
// shape (counter→Sum, gauge→Gauge, histogram→Histogram, summary→Summary).
// The payload is decoded with a test-side wire reader, so a field-number or
// encoding mistake fails here rather than at some collector.
func TestOTLPExportPayloadShape(t *testing.T) {
	reg := metricTestRegistry(t)

	var (
		gotMethod      string
		gotPath        string
		gotContentType string
		gotUserAgent   string
		gotAuth        string
		gotBody        []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotUserAgent = r.Header.Get("User-Agent")
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp, err := NewOTLPExporter(OTLPOptions{
		Endpoint:       srv.URL,
		Headers:        map[string]string{"Authorization": "Basic test"},
		ServiceName:    "posta",
		ServiceVersion: "test-version",
		Gatherer:       reg,
		Client:         srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewOTLPExporter: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exp.ExportOnce(ctx); err != nil {
		t.Fatalf("ExportOnce: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/v1/metrics" {
		t.Fatalf("path = %s, want /v1/metrics (OTLP/HTTP metrics convention)", gotPath)
	}
	if gotContentType != "application/x-protobuf" {
		t.Fatalf("content type = %q, want application/x-protobuf", gotContentType)
	}
	if gotUserAgent == "" || !strings.HasPrefix(gotUserAgent, "Posta-OTLP/") {
		t.Fatalf("user agent = %q, want Posta-OTLP/*", gotUserAgent)
	}
	if gotAuth != "Basic test" {
		t.Fatalf("configured header not sent: Authorization=%q", gotAuth)
	}

	// ExportMetricsServiceRequest { repeated ResourceMetrics resource_metrics = 1; }
	req := parsePB(t, gotBody)
	rmFields := allFields(req, 1)
	if len(rmFields) != 1 {
		t.Fatalf("resource_metrics count = %d, want 1", len(rmFields))
	}
	rm := rmFields[0].msg(t)

	// ResourceMetrics { Resource resource = 1; repeated ScopeMetrics scope_metrics = 2; }
	res := findField(rm, 1)
	if res == nil {
		t.Fatal("resource field missing")
	}
	resAttrs := findField(res.msg(t), 1) // resource.attributes
	if resAttrs == nil {
		t.Fatal("resource.attributes missing")
	}
	kv := resAttrs.msg(t)
	key := findField(kv, 1)
	val := findField(kv, 2)
	if key == nil || val == nil || key.str() != "service.name" {
		t.Fatalf("resource attribute key wrong: %+v", kv)
	}
	if v := findField(val.msg(t), 1); v == nil || v.str() != "posta" {
		t.Fatalf("service.name = %v, want posta", val.msg(t))
	}

	smFields := allFields(rm, 2)
	if len(smFields) != 1 {
		t.Fatalf("scope_metrics count = %d, want 1", len(smFields))
	}
	sm := smFields[0].msg(t)
	scope := findField(sm, 1)
	if scope == nil {
		t.Fatal("scope missing")
	}
	scopeName := findField(scope.msg(t), 1)
	if scopeName == nil || !strings.Contains(scopeName.str(), "observability") {
		t.Fatalf("scope name = %v", scopeName)
	}

	// Metrics: decode into a name→metric map.
	type otelpMetric struct {
		fields []pbField
	}
	metrics := map[string]otelpMetric{}
	for _, mf := range allFields(sm, 2) { // ScopeMetrics.metrics = 2
		m := mf.msg(t)
		nameF := findField(m, 1)
		if nameF == nil {
			t.Fatal("metric without name")
		}
		metrics[nameF.str()] = otelpMetric{fields: m}
	}

	for _, want := range []string{"test_tasks_total", "test_labeled_total", "test_depth", "test_latency_seconds", "test_summary_seconds"} {
		if _, ok := metrics[want]; !ok {
			t.Fatalf("metric %q missing from payload; got %v", want, mapKeys(metrics))
		}
	}

	// Counter → Sum (field 7), monotonic, cumulative, value 41.
	sumFields := findField(metrics["test_tasks_total"].fields, 7)
	if sumFields == nil {
		t.Fatal("test_tasks_total not encoded as Sum (field 7)")
	}
	sumMsg := sumFields.msg(t)
	if tmp := findField(sumMsg, 2); tmp == nil || tmp.varint != 1 { // AggregationTemporality AGGREGATION_TEMPORALITY_CUMULATIVE
		t.Fatalf("Sum.aggregation_temporality = %v, want cumulative (1)", tmp)
	}
	if mono := findField(sumMsg, 3); mono == nil || mono.varint != 1 {
		t.Fatalf("Sum.is_monotonic must be true for counters")
	}
	dps := allFields(sumMsg, 1)
	if len(dps) != 1 {
		t.Fatalf("Sum datapoints = %d, want 1", len(dps))
	}
	dp := dps[0].msg(t)
	if v := findField(dp, 4); v == nil || v.double() != 41 {
		t.Fatalf("counter value = %v, want 41", v)
	}
	if ts := findField(dp, 3); ts == nil || ts.fixed == 0 {
		t.Fatal("datapoint time_unix_nano must be set")
	}

	// Labeled counter: label pair queue=default rides on the datapoint.
	labeledSum := findField(metrics["test_labeled_total"].fields, 7).msg(t)
	ldps := allFields(labeledSum, 1)
	if len(ldps) != 1 {
		t.Fatalf("labeled counter datapoints = %d, want 1", len(ldps))
	}
	attrs := allFields(ldps[0].msg(t), 7) // NumberDataPoint.attributes = 7
	foundQueue := false
	for _, a := range attrs {
		kvMsg := a.msg(t)
		if k := findField(kvMsg, 1); k != nil && k.str() == "queue" {
			if vMsg := findField(kvMsg, 2); vMsg != nil {
				if sv := findField(vMsg.msg(t), 1); sv != nil && sv.str() == "default" {
					foundQueue = true
				}
			}
		}
	}
	if !foundQueue {
		t.Fatal("datapoint label queue=default not found")
	}

	// Gauge → Gauge (field 5), value 12.5.
	gaugeMsg := findField(metrics["test_depth"].fields, 5)
	if gaugeMsg == nil {
		t.Fatal("test_depth not encoded as Gauge (field 5)")
	}
	gdps := allFields(gaugeMsg.msg(t), 1)
	if len(gdps) != 1 {
		t.Fatalf("gauge datapoints = %d, want 1", len(gdps))
	}
	if v := findField(gdps[0].msg(t), 4); v == nil || v.double() != 12.5 {
		t.Fatalf("gauge value = %v, want 12.5", v)
	}

	// Histogram → Histogram (field 9): count 3, sum 5.55, cumulative bucket
	// counts [1,2,3,+Inf bucket =3], explicit bounds [0.1,1,10] (the +Inf
	// bound stays implicit in OTLP).
	histMsg := findField(metrics["test_latency_seconds"].fields, 9)
	if histMsg == nil {
		t.Fatal("test_latency_seconds not encoded as Histogram (field 9)")
	}
	hmsg := histMsg.msg(t)
	hdps := allFields(hmsg, 1)
	if len(hdps) != 1 {
		t.Fatalf("histogram datapoints = %d, want 1", len(hdps))
	}
	hdp := hdps[0].msg(t)
	if c := findField(hdp, 4); c == nil || c.fixed != 3 {
		t.Fatalf("histogram count = %v, want 3", c)
	}
	if s := findField(hdp, 5); s == nil || math.Abs(s.double()-5.55) > 1e-9 {
		t.Fatalf("histogram sum = %v, want 5.55", s)
	}
	// explicit_bounds (field 7): 3 finite bounds (0.1, 1, 10), packed doubles.
	if bounds := findField(hdp, 7); bounds == nil || len(bounds.bytes) != 24 {
		t.Fatalf("explicit bounds = %v bytes, want packed 3×float64 (24 bytes)", bounds)
	}
	// bucket_counts (field 6): OTLP requires len(bounds)+1 CUMULATIVE entries —
	// the last one is the +Inf bucket = SampleCount (client_golang's DTO omits
	// it). Packed fixed64: [1, 2, 3, 3] = 4×8 = 32 bytes.
	if counts := findField(hdp, 6); counts == nil || len(counts.bytes) != 32 {
		t.Fatalf("bucket counts = %v bytes, want packed 4×fixed64 (32 bytes)", counts)
	} else {
		var got []uint64
		for i := 0; i+8 <= len(counts.bytes); i += 8 {
			got = append(got, binary.LittleEndian.Uint64(counts.bytes[i:]))
		}
		wantCounts := []uint64{1, 2, 3, 3}
		for i, w := range wantCounts {
			if got[i] != w {
				t.Fatalf("bucket_counts = %v, want %v", got, wantCounts)
			}
		}
	}

	// Summary → Summary (field 11): count 1, sum 2, two quantiles.
	sumryMsg := findField(metrics["test_summary_seconds"].fields, 11)
	if sumryMsg == nil {
		t.Fatal("test_summary_seconds not encoded as Summary (field 11)")
	}
	smsg := sumryMsg.msg(t)
	sdps := allFields(smsg, 1)
	if len(sdps) != 1 {
		t.Fatalf("summary datapoints = %d, want 1", len(sdps))
	}
	sdp := sdps[0].msg(t)
	if c := findField(sdp, 4); c == nil || c.fixed != 1 {
		t.Fatalf("summary count = %v, want 1", c)
	}
	if s := findField(sdp, 5); s == nil || s.double() != 2 {
		t.Fatalf("summary sum = %v, want 2", s)
	}
	if q := allFields(sdp, 6); len(q) != 2 { // QuantileValue quantiles = 6
		t.Fatalf("summary quantiles = %d, want 2", len(q))
	}
}

// TestOTLPExportNon2xxIsError pins that a collector answering 5xx surfaces as
// an error (the push loop logs and retries — it must not swallow failures).
func TestOTLPExportNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	exp, err := NewOTLPExporter(OTLPOptions{Endpoint: srv.URL, Gatherer: metricTestRegistry(t), Client: srv.Client()})
	if err != nil {
		t.Fatalf("NewOTLPExporter: %v", err)
	}
	if err := exp.ExportOnce(context.Background()); err == nil {
		t.Fatal("503 response must surface as an error")
	}
}

func mapKeys[M ~map[string]V, V any](m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
