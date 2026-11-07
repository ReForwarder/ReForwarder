# V-IJ — Verification of Category I and Category J records (SF-92..SF-100)

Auditor: V-IJ · Date: 2026-11-07 · Method: code reading only (no local Go toolchain —
no builds/tests/docker per the audit brief). Every claim below was verified against
this checkout by reading the cited files; greps are quoted as run. Docs were treated
as claims, not proof. Test functions are named `file:line`; no test was executed —
"covered" means the test exists in the tree and names the behavior, not that it
passes in this session.

---

### SF-92 — Event/metrics core

**Final status: COMPLETE — confirmed; one scope note: 29 Prometheus families vs Stalwart's ~40 event families (documented equivalence holds; per-object labels are deliberately withheld)**

Fields: Backend Verified · Frontend Verified (Jobs.vue metrics card) · DB — · API Verified · Security — · Tests Verified · Docs Verified

**Implementation:**
- `internal/metrics/metrics.go` — 29 metric families declared `:37-262`: HTTP counters/histogram (`:37-52`), email send/fail/queue/retry (`:54-80`), webhook deliveries + duration (`:82-96`), bounces/suppressions (`:98-111`), inbound received/forwarded/failed/rejected/bytes/ingest-duration (`:113-171`), active workers + per-queue archived (dead-letter) gauge (`:173-189`), R-12 campaign families (`:198-217`), R-12/R-13 automation journey/node-failure/schedule-fire families (`:219-262`). All 29 registered with the default registry in `init()` via 29 `prometheus.MustRegister` calls `:265-295`. Increment/observe helpers `:297-443`. `PrometheusMiddleware` `:446-463`, `HTTPHandler` (worker mux) `:467-469`, `MetricsHandler` (okapi) `:472-478`.
- Registration + increment call sites verified across the tree (grep as run, ~50 non-test hits): `internal/routes/routes.go:239-241,255-257,292,582-584,627` (webhook dispatcher, email service hooks, worker-count gauge, inbound hooks, form messages); `cmd/posta/server.go:308-310,438-441,564-575,583,643-644,666,814-816` and `cmd/posta/worker.go:113-124,131,198-199,237` (same hooks on both processes incl. retry-worker `OnSent/OnFailed/OnRetry` `server.go:814-816`); `internal/handlers/bounce_handler.go:87,105`; `internal/handlers/admin_queue_handler.go:149` (`SetQueueArchivedTasks` per queue on admin-metrics refresh); `internal/services/automation/triggers.go:179`, `engine.go:343,413,438,454,690`; `internal/services/provider_events/service.go:277,290,336`; `internal/worker/handler.go:515,528`; `internal/worker/campaign_processor.go:264-265,518`.
- R-18 per-key usage is NOT a Prometheus label by design: `metrics.go:191-196` documents that campaign/automation ids are user-controlled cardinality and the per-object breakdown lives in the analytics APIs instead.

**Evidence:**
- Tests named: `internal/metrics/metrics_test.go` — `TestR12R13FamiliesRegistered` :67 (asserts all 9 R-12/R-13 families gatherable from `prometheus.DefaultGatherer`, i.e. a family dropped from `init()` fails), `TestCampaignCountersIncrement` :95 (incl. no-op guard for `AddCampaignMessagesQueued(0/-2)` :109-112), `TestAutomationJourneyCountersIncrement` :115, `TestAutomationNodeFailureCounterByType` :133 (asserts the `node_type=email` label set), `TestAutomationScheduleFireCounterIncrements` :164. File comment `:28-32` states the intent: registration drops and mis-wired increments fail these tests.
- Frontend discoverability: `web/src/views/admin/Jobs.vue:354-369` — "Prometheus metrics" card naming `POSTA_METRICS_ENABLED=true` and the `GET /metrics` scrape path (R-12 default-off decision documented at `docs/implementation/ROADMAP_IMPLEMENTATION.md:119-121`).
- Not executed (no toolchain). All 29 `MustRegister` calls were counted by reading `metrics.go:265-295` directly; the test-existence check is a read, not a run.

**Notes:** Pre-seed status stands (COMPLETE). Scope note for the ledger: Stalwart's trc core is ~40 event families with IPC aggregation; Posta has 29 families on one in-process registry — the audit's "documented equivalent" framing is fair, and per-key/per-campaign labels are deliberately excluded (cardinality, `metrics.go:191-196`), with the per-key usage surface living in the R-15/R-18 analytics APIs (verified under SF-88/V-H). Nothing to correct.

---

### SF-93 — Prometheus/OpenMetrics endpoint + OTLP push exporter

**Final status: PARTIAL — confirmed; one refinement: the dedicated worker's `/metrics` is gated by `POSTA_WORKER_HEALTH_ENABLED` (default true), not by `POSTA_METRICS_ENABLED` — so a default worker exposes metrics while a default server does not**

Fields: Backend Partial · Frontend Verified (docs card) · DB — · API Verified · Security — (env-gate only; no auth on the endpoint) · Tests Verified · Docs Verified

**Implementation:**
- Server route gate: `internal/routes/routes.go:750-753` — `if r.cfg.MetricsEnabled { r.app.Use(metrics.PrometheusMiddleware()); r.app.Get("/metrics", metrics.MetricsHandler(), okapi.DocHide()) }`. Flag source: `internal/config/config.go:362` — `MetricsEnabled: goutils.EnvBool("POSTA_METRICS_ENABLED", false)` (field `:75`). Registered at app root, not in the v1/auth groups — no auth middleware on it (DocHide only).
- Worker side: `internal/worker/health.go:70-72` — the worker mux serves `/healthz`, `/readyz`, and `/metrics` (`metrics.HTTPHandler()`); started at `cmd/posta/worker.go:267-279` under `if cfg.WorkerHealthEnabled` — `internal/config/config.go:375-376` defaults `POSTA_WORKER_HEALTH_ENABLED=true`. So the worker's scrape target is on by default and is not subject to `POSTA_METRICS_ENABLED`.
- OpenMetrics text: `promhttp.Handler()` (`metrics.go:467-478`) — Prometheus text exposition via `prometheus/client_golang`.
- OTLP exporter absent: `grep -in "otlp|opentelemetry|otel|go.opentelemetry" go.mod internal cmd -r` → the only hit is the substring "remotely" inside a comment (`internal/services/inbound/dmarc.go:312`). No `go.opentelemetry` module in `go.mod` (go.mod:19-20 carry `jkaninda/logger` and `jkaninda/okapi`). Verified absent.

**Evidence:**
- Docs: `docs/docs/getting-started/configuration.md:61` documents `POSTA_METRICS_ENABLED | false`; `docs/docs/analytics/prometheus-metrics.md:18` shows the enable line.
- Tests named: `internal/routes/contract_test.go:143` `TestAPIRouteContract` — the full-flag golden server (`:92-128`, header text `:63` names POSTA_METRICS_ENABLED) registers `/metrics`, so the golden contract `docs/implementation/api_routes.txt` pins the route; `internal/handlers/info_handler_test.go:77,89` pins the flag's exposure via `/info`; `internal/worker/health_test.go:115` `TestMetricsAreExposed` covers the worker mux. Not executed.
- Absence check quoted above (OTLP): 0 real hits.

**Notes:** Pre-seed status stands (PARTIAL; OTLP push exporter PLANNED (OB-6) with GAP-51/52). Two ledger refinements: (1) gate asymmetry — worker `/metrics` default-on via the health server, server `/metrics` default-off; if the default-off posture is a security decision (unauthenticated endpoint, per `ROADMAP_IMPLEMENTATION.md:121`), the worker exposure should be recorded next to it; (2) no Basic-auth option on either `/metrics` (Stalwart's is optional-Basic) — env-gate only. Neither changes the verdict.

---

### SF-94 — Structured log sinks (console/files/journald/OTLP/webhooks/store)

**Final status: PARTIAL — confirmed: single sink (stdout console; JSON in production), structured key-value fields widely used, request-ID middleware present; file/journald/OTLP/webhook sinks and per-type level filtering absent**

Fields: Backend Partial · Frontend — · DB — · API — · Security — · Tests — · Docs Partial

**Implementation:**
- Library + init: `github.com/jkaninda/logger v0.0.5` (`go.mod:19`). `internal/config/config.go:589-594` `initLogger()` — dev: `logger.New(logger.WithDebugLevel())`; otherwise `logger.New(logger.WithJSONFormat(), logger.WithInfoLevel())`. Wired from `InitWorker` (`config.go:584-586`) and config load (`:326` debug on missing .env).
- Structured call sites: `grep -rn "logger.(Info|Error|Warn|Debug|With)" internal cmd --include="*.go"` (non-test) → **390** hits, with key-value args, e.g. `internal/routes/routes.go:213` (`logger.Error("security feed stopped", "error", err)`), `internal/config/security.go:246,282` (`"env", p.envVar`), `internal/cron/manager.go:99-108` (`"name"`, `"schedule"`, `"error"` fields per job run), `internal/handlers/dashboard_handler.go:129` (`"workspace_id"`).
- Request scoping: `okapi.RequestID()` middleware installed first in `registerRoutes` (`internal/routes/routes.go:740-741`); ban engine sits after it (`:746-748`). Per-request access logs are okapi-framework behavior (external dependency, not verifiable in-tree).
- Sinks absent: `grep -rn -i "journald|LOG_FILE|LogOutput|log_output|POSTA_LOG" internal cmd --include="*.go"` (non-test) → **0 hits**. No rotating-file writer, no journald writer, no OTLP trace export (see SF-93), no webhook log sink, no store tracer. One sink only: stdout.
- Per-event-type level filtering (`config/telemetry.rs:58-75` in Stalwart): no equivalent — grep shows no per-source log-level configuration surface.
- Live tracing stream [ENT]: absent — the five SSE streams are business/event streams (V-H SF-89), not log tracing.

**Evidence:**
- No logging package of the codebase has a test file: `ls internal/config` (initLogger lives there), and no `*_test.go` in the repo references `initLogger` or a log sink; the logging surface is untested. Stated as "not found", not guessed.
- Not runnable in this session (no toolchain).

**Notes:** Pre-seed status stands (PARTIAL, PLANNED Phase 3 for configurable sinks). No corrections. The only sink is stdout — consistent with the audit's "structured logging to console with request-scoped fields" wording, with the mild correction that "request-scoped fields" is realized as (a) the RequestID middleware and (b) ad-hoc structured fields at call sites, not automatic per-request enrichment.

---

### SF-95 — Health endpoints (/healthz/live + /healthz/ready)

**Final status: COMPLETE — confirmed for the endpoint machinery (server /healthz + /readyz, worker /healthz + /readyz + /metrics, shared check semantics, worker processing gate); Docker image healthcheck covers both roles, but the compose `worker` service itself declares no healthcheck block, and no systemd sd-notify equivalent exists**

Fields: Backend Verified · Frontend — · DB — · API Verified · Security Verified (probes stay public by documented decision) · Tests Verified · Docs Verified

**Implementation:**
- Server: `internal/routes/auth_routes.go:35-54` `healthRoutes()` — `GET /healthz` → `r.h.health.Healthz`, `GET /readyz` → `r.h.health.Readyz`; registered app-wide at `internal/routes/routes.go:757` (public; the `/info` doc comment `auth_routes.go:60` records that probes stay public deliberately). Handlers: `internal/handlers/health_handler.go:48-51` (liveness = static ok) and `:53-60` (readiness = `health.Check(ctx, db, redis)`, 503 + payload when `!status.Ready()`).
- Shared check: `internal/health/health.go` — `probeTimeout = 2s` `:36`, `Status{status, database, redis}` `:46-50` with `Ready()` `:55`, `Check` pings each dependency (nil reported as unconfigured, not skipped, `:60` comment).
- Worker: `internal/worker/health.go:66-80` — dedicated `HealthServer` mux serving `/healthz`, `/readyz`, `/metrics`; `healthz` `:108-113` touches nothing (documented rationale `:105-107`); `readyz` `:115-134` = `health.Check` + `processing` atomic bool (`:52`, `SetProcessing` `:83`) — a process that stopped consuming reports not-ready (503), which is stronger than the server contract. Started under `cfg.WorkerHealthEnabled` (default true, `internal/config/config.go:375-376`) at `cmd/posta/worker.go:267-279`, with readiness flipping around the consumer run loop (`worker.go:284+` comments).
- Container wiring: `docker/Dockerfile:60-67` `HEALTHCMD ["/usr/local/bin/posta-healthcheck"]`; `docker/healthcheck.sh:20-26` probes `http://127.0.0.1:${POSTA_WORKER_HEALTH_PORT:-${POSTA_PORT:-9000}}${POSTA_HEALTHCHECK_PATH:-/healthz}` — usable by both roles. `compose.yml:18-23` wires the `wget /healthz` healthcheck on the **server** service only; `compose.yml:31-49` and `examples/docker-compose-full.yml` (worker block) declare **no** healthcheck for the `worker` service.

**Evidence:**
- Tests named: `internal/worker/health_test.go` — `TestHealthzIsIndependentOfDependencies` :49, `TestReadyzFailsWithoutDependencies` :72, `TestReadyzRequiresProcessing` :90, `TestMetricsAreExposed` :115, `TestReadyzPayloadShape` :130, `TestStartAndStop` :152. `internal/health/` has **no** test file (`ls internal/health/` → `health.go` only) — the shared `Check` is covered only through the worker tests. Not executed.
- sd-notify (Stalwart's systemd state machine): `grep -rn -i "sd_notify|sdnotify|systemd" internal cmd --include="*.go"` → 0 relevant hits. Absent; the Docker/compose healthchecks are the documented equivalent.

**Notes:** Pre-seed status stands (COMPLETE) for what the record claims — the endpoints and their use as probes exist and are tested. Two ledger notes: (1) "compose healthchecks use them" is true for the server service only; the worker service relies on the image-level `HEALTHCMD` default and would gain an explicit healthcheck if `POSTA_WORKER_HEALTH_PORT` is set; (2) no sd-notify equivalent — consistent with the record's Docker/compose framing, worth one sentence in the ledger.

---

### SF-96 — Metric alerts (condition→action) [ENT] + metrics/trace history

**Final status: PLANNED (OB-6) — confirmed: no metric-alert engine, no metrics/trace history stores; the nearest analog (BounceAlertJob) is a DB-threshold email alert, not a metric-condition engine**

Fields: Backend Missing · Frontend Missing · DB Missing · API Missing · Security — · Tests Missing · Docs — (ledger only)

**Implementation:** none — verified absent.

**Evidence:**
- `grep -rn "AlertRule|MetricAlert|alert_rule|alertRule" internal web/src --include="*.go" --include="*.ts" --include="*.vue"` → **0 hits**. No condition-expression evaluator, no scheduled metric evaluation (the only 5-min-style scheduled evaluations are cron jobs over SQL state, e.g. `internal/cron/jobs/bounce_alert.go:60` `BounceAlertJob`), no alert action executor.
- Metrics/trace history stores: no retention/query surface over Prometheus families (they are in-process registry state only, SF-92); no trace store (no OTel at all, SF-93 evidence).
- The "alert" grep hits that DO exist are the user-notification bounce alerts (`internal/models/user_setting.go` `NotifyBounceAlerts`, `internal/cron/jobs/bounce_alert.go`, workspace dashboard health alerts) — DB-backed notification features, out of scope for this record. The gap-analysis ledger (§4 line 59: "bounce-alert only") already records this distinction correctly.

**Notes:** Pre-seed status stands (PLANNED (OB-6), with GAP-51/52 — error-monitoring hook, Grafana dashboard). No corrections.

---

### SF-97 — Admin UI

**Final status: COMPLETE — confirmed; one factual update: the admin backend surface is 51 route definitions (33 GET / 20 POST / 6 PUT / 8 DELETE / 1 PATCH) across users, events, analytics, settings, update-check, announcements, domains, workspaces, plans, servers, jobs, queues, security**

Fields: Backend Verified (admin surface) · Frontend Verified (20 admin views + 5 security views) · DB — · API Verified · Security Verified (admin-role middleware on every admin group) · Tests Partial (vitest: 1 admin view spec + API contract) · Docs Verified

**Implementation:**
- Frontend: `web/src/views/admin/` — 20 views (`AdminHealth`, `AdminInventory`, `AdminQuickLinks`, `Announcements`, `Dashboard`, `DomainDetail`, `Domains`, `EventDetail`, `Events`, `Jobs`, `OAuthProviders`, `PlanDetail`, `Plans`, `ServerDetail`, `Servers`, `Settings`, `UserDetail`, `Users`, `WorkspaceDetail`, `Workspaces`) + `security/` subdirectory with 5 views (`SecurityOverview`, `SecurityEvents` + `SecurityEvents.spec.ts`, `SecurityRules`, `SecurityAllowlist`, `SecurityBlocked`). Covers the audit's claimed surface: users, workspaces, queue admin (`Jobs.vue` — queues + tasks + cron jobs), security, settings, announcements, provider events (`Events.vue`), system info (`AdminHealth`/`AdminInventory`).
- Backend group: `internal/routes/admin_routes.go:34` `adminGroup := r.v1.Group("/admin", r.mw.jwtAdminAuth.Middleware)` — 51 route definitions grouped on it (`grep -c "Group: adminGroup"` = 51; method census by reading `Method:` lines: 33 GET, 20 POST, 6 PUT, 8 DELETE, 1 PATCH). Additional admin-gated groups: SSE `:734`, security `:764`, security SSE `:917`, OAuth providers `internal/routes/oauth_routes.go:116`. Paths enumerated at `admin_routes.go:44-925`: `/users` (+metrics/force/cancel-deletion/2fa/revoke-sessions/impersonate/workspaces/plan), `/events` (+`/{id}`, `/stream`), `/metrics`, `/analytics(/dashboard|/providers)`, settings GET/PUT (`:284-292`), `/update(+\/dismiss)`, announcements, domains (+verify), workspaces (+plan), plans (+default), servers (+enable/disable/test), `/jobs`, `/queues` (+task list/cancel/run/archive), `/security/*` (overview, bans CRUD + extend, events + stream, rules, allowlist CRUD).
- Admin middleware: `internal/middlewares/middleware.go:105` `baseJWTAuth(..., requireAdmin)` with the role check at `:151-156` (`role != string(models.UserRoleAdmin)` → 403 `insufficient permissions`, rejection marker `:98-103` distinguishing forbidden from unauthenticated); `JWTAdminAuth` (`:212` per V-H SF-88) wired at `internal/routes/routes.go:296`.

**Evidence:**
- Tests named: `web/src/views/admin/security/SecurityEvents.spec.ts` (vitest, admin-surface component test); frontend API contract guard `web/src/api/admin.spec.ts:9-23` (fails when an api module calls an unregistered route — R-21). Backend admin-role gating tests live in the per-feature suites, e.g. `internal/handlers/admin_queue_test.go:595` `TestAdminQueuesRequireAdminRole`, `internal/middlewares/authenticate_test.go:196` `TestJWTAdminAuth_RejectsNonAdmin`. "Partial" vitest coverage is accurate: only 1 spec targets an admin view directly. Not executed.
- The view list and the 51-route count are direct reads of the tree, as quoted above.

**Notes:** Pre-seed status stands (COMPLETE). No corrections. Stalwart's admin UI is an external repository; ReForwarder embedding it remains the documented ahead-of-parity position.

---

### SF-98 — Configuration = typed registry objects

**Final status: COMPLETE — confirmed (documented equivalent: settings table + typed accessors + seeder + 30s live-apply TTL cache + admin Settings UI); one scope note: the "typed registry" is a closed 3-type vocabulary (string/bool/int), not Stalwart's struct-typed registry objects**

Fields: Backend Verified · Frontend Verified · DB Verified · API Verified · Security Verified (admin group) · Tests Verified · Docs Partial

**Implementation:**
- Model: `internal/models/setting.go:41-48` `Setting{ID, Key (uniqueIndex, not null), Value (text), Type (default string)}`; closed type vocabulary `:26-30` (`string|bool|int`) with serialized bools `:34-37`.
- DB: migrated in `internal/storage/migration/migration.go:67` (`&models.Setting{}` in the AutoMigrate list). Repository: `internal/storage/repositories/setting_repo.go` — `NewSettingRepository` :31, `FindAll` :35, `FindByKey` :43, `Upsert` :51, `BulkUpsert` :62.
- Live-apply cache: `internal/services/settings/provider.go` — `NewProvider` `:42-51` (30s `cacheTTL`, eager refresh), `refresh` `:53-66` (full reload from `FindAll`), `ensureFresh` `:68-75` (TTL staleness check per read). Typed accessors: `GetString` :78, `GetInt` :89 (invalid → fallback), `GetBool` :102, plus 25 named convenience accessors `:116-190` (`MaintenanceMode`, `RegistrationEnabled`, `TwoFactorRequired`, rate limits, batch size, attachment size, bounce threshold, SMTP timeout, 9 retention windows, login rate-limit knobs, `EmailTrackingEnabled` :179-181 with the deprecated alias :188-190).
- Seeder: `internal/services/seeder/settings.go` — `SeedDefaultSettings` :28 with **53** default keys (`grep -c "Key:"` on the defaults slice = 53), including the `campaign_click_tracking_enabled` → `email_tracking_enabled` migration `:106-118`; wired asynchronously at server start `internal/routes/routes.go:288` (`go seeder.SeedDefaultSettings(settingRepo)`).
- API + UI: `internal/routes/admin_routes.go:284-292` GET/PUT on `pathSettings` → `internal/handlers/setting_handler.go` (`GetSettings` :69 with reserved-key filtering :31-45, `UpdateSettings` :77 → `BulkUpsert` :97 + audit event :101). Frontend: `web/src/api/settings.ts:7,10` (`getAdminSettings`/`updateAdminSettings`), `web/src/views/admin/Settings.vue:106,141` (load; per-setting PUT with `key/value/type`).

**Evidence:**
- Tests named: `internal/services/settings/provider_test.go` — `TestEmailTrackingDefaultsToTrue` :33, `TestEmailTrackingReadsStoredValue` :41, `TestEmailTrackingInvalidValueFallsBackToDefault` :53, `TestCampaignClickTrackingIsAliasForEmailTracking` :60; `internal/handlers/setting_timezone_test.go` — `TestSettingsTimezoneValidation` :33, `TestSubscriberTimezoneValidation` :66, `TestSubscriberTimezoneNormalisation` :95. Not executed.
- Live-apply claim: the TTL cache is the mechanism (settings take effect without restart within 30s) — verified by reading `provider.go`; there is no push-invalidation path (admin PUT does not call the provider), so the effective worst-case apply delay is the 30s TTL. Recorded as the mechanism's documented behavior.

**Notes:** Pre-seed status stands (COMPLETE). Scope note only: "typed registry objects" is realized as a key-value table with a 3-value type hint driving frontend control rendering (`setting.go:24-30` — the closed-vocabulary comment is explicit); struct-typed registry objects were not built and are not needed for the parity claim as the audit frames it.

---

### SF-99 — Operational controls + task manager with scheduler-role gating

**Final status: PARTIAL — confirmed, with one significant update: task-manager visibility EXISTS at HEAD (GET /admin/jobs + Jobs.vue render name/schedule/running/last-run/last-error/next-run) — the pre-seed's "task-manager visibility UI (next runs, history) absent" is half-stale; what remains missing is run history beyond the last run, manual trigger, and scheduler-role gating**

Fields: Backend Verified (controls) · Frontend Verified (Jobs.vue: queues/tasks/cron) · DB Verified · API Partial (controls Verified; no history/trigger endpoints) · Security Verified (admin group + `TestAdminQueuesRequireAdminRole`) · Tests Verified · Docs Partial

**Implementation:**
- Cron machinery: `internal/cron/cronjob.go:29-34` `CronJob` interface (Name/Schedule/Run); `internal/cron/manager.go` — `Register` :69, `Start` :76-110 (per-job `cron.AddFunc`, running flag + last-run/error recording :111-124), `Stop` :118, `Jobs()` :123-149 returning `JobStatus{Name, Schedule, Running, LastRunAt, LastError, NextRunAt}` (`:33-40`; next-run read from `m.cron.Entries()`).
- Job inventory at HEAD — **11 distinct job types** in `internal/cron/jobs/` (`account_cleanup.go`, `api_key_expiry.go`, `backup.go`, `bounce_alert.go`, `campaign_restart.go`, `campaign_scheduler.go`, `daily_report.go`, `message_digest.go`, `queue_reconcile.go`, `retention.go`, `update_check.go`), registered in `cmd/posta/server.go` `initCronManager` :702-790: retention (always), update-check (if `updateSvc.Enabled()`), daily-report, account-cleanup, api-key-expiry, bounce-alert (always), campaign-restart + campaign-scheduler + queue-reconcile (if `producer != nil`), backup (if `cfg.BackupEnabled`), message-digest (if `cfg.MessagesEnabled`). Manager started at `server.go:233-234`, stopped `:838-839`. The pre-seed's "8 registered cron jobs" is stale — 11 types exist (3 added with R-03/R-11/R-13 since).
- Admin queue operations: `internal/routes/admin_routes.go:657-713` — `GET /admin/queues`, `GET /admin/queues/{queue}/tasks`, and `POST .../tasks/{task_id}/cancel|run|archive`; handler `internal/handlers/admin_queue_handler.go` (`AsynqQueueInspector` :60-93: queue info + list pending/active/scheduled/retry/archived + delete/run/archive; per-queue dead-letter gauge feed `:149`; audit events on task actions `:363-366`).
- Task-manager visibility: `GET /admin/jobs` (`admin_routes.go:640-652`, registered when `r.h.cron != nil`, wired `routes.go:445-446`) → `internal/handlers/cron_handler.go:36-39` → `manager.Jobs()`. Frontend: `web/src/views/admin/Jobs.vue:305-340` renders each job's schedule, running badge, last-run time/status (`:322,334`), last error, and next run with countdown (`:339-340`, `timeUntil` :159).
- Automation schedules (R-13): `internal/services/automation/triggers.go:124` schedule-event triggers, fire metric at `engine.go:690` (SF-92); campaign_scheduler job claims due scheduled campaigns (`internal/cron/jobs/campaign_scheduler.go:75`).
- Scheduler-role gating (Stalwart: only some nodes run jobs): **absent** — `manager.Start` runs every registered job in whichever process starts it (`manager.go:76-110`); there is no node-role election or cluster broadcast. Single-process assumption; a second server replica would double-run jobs (not guardable in-tree).

**Evidence:**
- Tests named: `internal/handlers/admin_queue_test.go` — `TestAdminQueuesOverview` :406, `TestAdminQueueTasksListByState` :450, `TestAdminQueueTasksOnQueueThatNeverExisted` :516, `TestAdminQueueTaskActions` :541, `TestAdminQueuesRequireAdminRole` :595, `TestAdminQueuesUnavailableWithoutInspector` :608. Cron-job behavior: `internal/cron/jobs/campaign_scheduler_test.go` — `TestSchedulerClaimsOnlyDueScheduledCampaigns` :497, `TestSchedulerClaimConflictSkipsWithoutEnqueue` :528, `TestSchedulerEnqueueFailureRollsBackToScheduled` :555, `TestSchedulerIgnoresPausedAndCancelledCampaigns` :590; `internal/cron/jobs/backup_test.go` :57/:97/:114; `internal/cron/jobs/retention_test.go` :26/:46/:65; `internal/cron/jobs/queue_reconcile_test.go` (present). Not executed.
- The `/admin/jobs` route, `JobStatus` fields, and the Jobs.vue next-run rendering are direct reads quoted above — this is the refutation of the "visibility absent" half of the pre-seed note.

**Notes:** Pre-seed PARTIAL stands, but the record text should be corrected on the next docs pass: (1) "task-manager visibility UI (next runs, history) absent" → next-run/status visibility **exists** (GET /admin/jobs + Jobs.vue); the missing half is per-run history beyond last-run, manual "run now" trigger, and scheduler-role gating; (2) "8 registered cron jobs" → 11 job types (up to 11 concurrently registered depending on flags). The Phase-3 scope for the remaining gap narrows accordingly.

---

### SF-100 — Configuration history/rollback

**Final status: MISSING → PLANNED (Phase 3) — confirmed: exactly one audit event is written on settings change, with no key-level old/new values; no history table, no rollback/revert surface anywhere**

Fields: Backend Missing (audit event only) · Frontend Missing · DB Partial (audit-event substrate exists) · API Missing · Security — · Tests Missing · Docs Missing

**Implementation:**
- Settings-change audit: `internal/handlers/setting_handler.go:101` — on every admin PUT, `h.audit.Log(userID, email, ip, "settings.updated", "Platform settings updated", nil)` — **one** bulk event, `metadata nil`: the changed keys and their previous values are NOT recorded. The audit logger (`internal/services/audit/audit.go:38-51`) publishes onto the event bus (`models.EventCategoryAudit`), so the event is visible in the admin events stream, but carries no diff.
- History/rollback absence: `grep -rn -i "rollback|history|revision|previous value|Restore(" internal/handlers/setting_handler.go internal/storage/repositories/setting_repo.go internal/services/settings/ web/src/views/admin/Settings.vue` → **0 hits**. No `settings_history`/`SettingHistory` model (`grep -rn "setting_history|settings_history|SettingHistory" internal web/src` → 0). The repo has no previous-value retention (`setting_repo.go:51-62` is Upsert/BulkUpsert overwrite only). No revert endpoint in `admin_routes.go` (the settings surface is exactly GET + PUT, `:284-292`).

**Evidence:**
- Stalwart-negative parity confirmed: inventory §J SF-100 marks Stalwart itself as lacking history/rollback (`docs/research/stalwart-feature-inventory.md:161` — "**Negative** — none"), and the gap-analysis ledger records SF-100 as N/I (`stalwart-feature-gap-analysis.md` §4a, `:94`) with the settings-change audit events as the local substrate (`:61`).
- The one write site is `setting_handler.go:101` — no other handler writes `settings.*` audit actions (`grep -rn "audit.Log(" internal/handlers` non-test: settings.updated is the only settings-family action; plan/server/queue/impersonation actions are separate features).
- Tests: none cover the audit event (the settings handler tests — `setting_timezone_test.go` :33/:66/:95 — exercise validation only). Stated as "not found".

**Notes:** Pre-seed status stands (MISSING → PLANNED (Phase 3)). One scoping input for the Phase-3 item: the "existing events substrate" is thinner than the record implies — the audit event exists but stores neither the changed keys nor prior values (metadata is nil), so key-level history + rollback needs new event metadata (old/new per key) or a history table; the rollback itself is a new write path, not a replay of existing data. This is consistent with the Phase-3 pool listing "settings history SF-100" (audit line 1168).

---

## Summary of corrections to feed back into the parity audit (next docs pass)

1. **SF-93:** record the gate asymmetry — server `/metrics` default-off via `POSTA_METRICS_ENABLED`, worker `/metrics` default-on via `POSTA_WORKER_HEALTH_ENABLED=true` (`config.go:375-376`, `worker/health.go:72`); and note neither endpoint offers auth (env-gate only, no Basic-auth option).
2. **SF-95:** "compose healthchecks use them" holds for the server service only; the `worker` compose service (compose.yml:31-49, examples/docker-compose-full.yml) declares no healthcheck block (image `HEALTHCMD` covers it). No systemd sd-notify equivalent exists.
3. **SF-99:** two stale claims — (a) task-manager visibility **exists** at HEAD (GET /admin/jobs, admin_routes.go:640-652 + Jobs.vue:305-340 next-run/status); missing piece is run history/manual trigger/scheduler-role gating; (b) "8 registered cron jobs" → **11** job types (initCronManager, cmd/posta/server.go:702-790).
4. **SF-100:** the "existing events substrate" records one bulk `settings.updated` event with nil metadata (setting_handler.go:101) — no per-key old/new values; Phase 3 must add diff capture or a history table before rollback is possible.
5. No status changes vs the pre-seed: SF-92 COMPLETE, SF-93 PARTIAL (OB-6), SF-94 PARTIAL (Phase 3), SF-95 COMPLETE, SF-96 PLANNED (OB-6), SF-97 COMPLETE, SF-98 COMPLETE, SF-99 PARTIAL (Phase 3), SF-100 MISSING→PLANNED (Phase 3) — all confirmed against code.
