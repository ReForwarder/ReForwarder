# Observability (OB-6, W5)

This directory carries the Phase 6 observability completion artifacts:
the sample Grafana dashboard (GAP-52) and the operator guide for the OTLP
push exporter (SF-93), the metric-alert engine (SF-96), and the
error-monitoring hook (GAP-51).

Everything here is **default-off**: with no environment configured, the
process ships nothing anywhere and the alert engine idles.

## 1. Metrics inventory

`/metrics` (server, gated by `POSTA_METRICS_ENABLED`, default off) and the
worker health listener (`POSTA_WORKER_HEALTH_ENABLED`) expose the Prometheus
families registered in `internal/metrics/metrics.go` (29 `posta_*` families)
plus the Go-runtime and process families the default registry collects. The
OTLP exporter ships exactly these families — it gathers the same registry.

Key families (labels in parentheses):

| Family | Type | Purpose |
| --- | --- | --- |
| `posta_http_requests_total` (method, path, status) | counter | API traffic |
| `posta_http_request_duration_seconds` | histogram | API latency |
| `posta_emails_sent_total` / `posta_emails_failed_total` | counter | delivery outcome |
| `posta_emails_queue_size` | gauge | in-flight outbound |
| `posta_email_retries_total` | counter | retry pressure |
| `posta_bounces_total` (type) | counter | hard/soft/complaint |
| `posta_suppressions_total` | counter | list hygiene |
| `posta_webhook_deliveries_total` (status) | counter | user webhook outcome |
| `posta_webhook_delivery_duration_seconds` | histogram | webhook latency |
| `posta_webhook_events_dropped_total` | counter | SF-90 pending cap drops |
| `posta_inbound_messages_received_total` (source) | counter | inbound volume |
| `posta_inbound_messages_forwarded_total` / `..._failed_total` | counter | forwarding outcome |
| `posta_inbound_messages_rejected_total` (reason) | counter | ingestion rejects |
| `posta_inbound_bytes_total`, `posta_inbound_ingest_duration_seconds` | counter/histogram | inbound load |
| `posta_active_workers` | gauge | asynq workers |
| `posta_queue_archived_tasks` (queue) | gauge | dead-letter depth |
| `posta_campaigns_started_total` / `..._completed_total`, `posta_campaign_messages_queued_total` | counter | campaign throughput |
| `posta_automation_journeys_*` | counter | automation throughput |
| `posta_automation_node_failures_total` (node_type) | counter | node failures |
| `posta_automation_schedule_fires_total` | counter | schedule triggers |

## 2. OTLP push exporter (SF-93)

| Env | Default | Meaning |
| --- | --- | --- |
| `POSTA_OTLP_ENDPOINT` | *(empty = off)* | OTLP/HTTP receiver base URL, e.g. `http://otel-collector:4318`. The exporter POSTs protobuf `ExportMetricsServiceRequest` to `<endpoint>/v1/metrics`. |
| `POSTA_OTLP_INTERVAL_SECS` | `60` | Push period. |
| `POSTA_OTLP_HEADERS` | *(empty)* | Extra headers, comma-separated `Name=Value` (collector auth, e.g. `Authorization=Basic ...`). |

Implementation: standard-library-only protobuf wire encoding
(`internal/observability/otlp.go`) — no OpenTelemetry SDK dependency was
added (rationale recorded in the package doc and the ADR-009/OB-6 ledger
entry). Counters map to OTLP `Sum` (monotonic, cumulative), gauges to
`Gauge`, histograms to `Histogram` (cumulative, explicit bounds), summaries
to `Summary`. Resource: `service.name=posta`, `service.version=<build>`.

Compose sketch:

```yaml
environment:
  POSTA_OTLP_ENDPOINT: http://otel-collector:4318
  POSTA_OTLP_INTERVAL_SECS: "30"
```

## 3. Metric alerts (SF-96)

Rules live in the platform setting **`metric_alert_rules`** — a JSON array —
managed through the admin-gated settings API (`PUT /admin/settings`). The
evaluation cron (`metric-alert`, every minute,
`internal/cron/jobs/metric_alert.go`) applies each rule to the live
registry:

```json
[
  {
    "id": "outbound-queue-backlog",
    "name": "Outbound queue backlog",
    "metric": "posta_emails_queue_size",
    "labels": {},
    "condition": "gt",
    "threshold": 1000,
    "duration_seconds": 300,
    "webhook_url": "https://alerts.example.com/posta",
    "webhook_secret": "shared-secret",
    "email": "ops@example.com",
    "enabled": true
  }
]
```

Fields: `condition` ∈ `gt|gte|lt|lte|eq|neq`; `labels` is an optional exact
match conjunction narrowing which series count; `duration_seconds` is how
long the condition must hold continuously before firing (0 fires on the
first breach). Actions on fire: **event** (`metric_alert.fired` on the
platform activity timeline) + **webhook** (JSON POST, `X-Posta-Signature:
sha256=<hex>` HMAC — same scheme as delivery webhooks; sent through the same
SSRF guard, so loopback/private targets are refused like every user-stored
webhook URL) + optional **email**. When the condition clears the rule
resolves: `metric_alert.resolved` event + webhook (no email). A rule fires
once per breach episode, not once per tick.

Example payload:

```json
{
  "event": "metric_alert.fired",
  "rule_id": "outbound-queue-backlog",
  "rule_name": "Outbound queue backlog",
  "metric": "posta_emails_queue_size",
  "labels": {},
  "value": 1240,
  "threshold": 1000,
  "condition": "gt",
  "duration_seconds": 300,
  "held_seconds": 301.4,
  "fired_at": "2026-11-10T12:00:00Z",
  "resolved": false
}
```

Limitations (documented, tested): histogram/summary families have no single
alertable value — rules referencing them are skipped with a warning. Breach
duration state is in-memory, so a process restart restarts a rule's
duration timer.

## 4. Error-monitoring hook (GAP-51)

| Env | Default | Meaning |
| --- | --- | --- |
| `POSTA_ERROR_WEBHOOK_URL` | *(empty = off)* | Panics and fatal exits are shipped here as JSON events. |
| `POSTA_ERROR_WEBHOOK_SECRET` | *(empty)* | When set, the body is HMAC-signed (`X-Posta-Signature: sha256=<hex>`, same scheme as delivery webhooks). |

Payload shape (`internal/observability.ErrorEvent`):

```json
{
  "event": "runtime.error",
  "kind": "panic",
  "message": "runtime error: index out of range [5]",
  "stack": "goroutine ...",
  "timestamp": "2026-11-10T12:00:00Z",
  "service": "posta",
  "version": "1.x.y"
}
```

Each incident also lands as a best-effort platform event row
(`runtime.panic` / `runtime.fatal`, category `system`) on the admin
activity timeline. Coverage: the HTTP API panic path
(`middlewares.CapturePanicsMiddleware`, re-panics — request behavior
unchanged) and the `logger.Fatal` sites in `cmd/posta`. SMTP/IMAP/POP3
session-level `recover()` sites keep their local logging — hooking them is
a documented residual.

## 5. Grafana dashboard (GAP-52)

Import `grafana-posta-overview.json` (Dashboards → New → Import): HTTP
traffic, email delivery, bounces/suppressions, webhooks, inbound mail,
queue health, campaigns/automations. Point the `DS_PROMETHEUS` datasource
variable at the Prometheus scraping `/metrics` — or at the store the OTLP
exporter feeds (both carry the same families).
