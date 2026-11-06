# ReForwarder — Architecture Overview (current system + Option B direction)

> Summary level only. Full diagrams and per-engine detail live in the source docs; this file links to them rather than reproducing them:
> - `README.md` §Architecture — deployment-level flow diagram (server/worker/shared state) and stack.
> - `docs/product-roadmap/06-architecture-impact.md` §2 — AS-IS system context, engine map, delivery-pipeline detail; §4 — recommended relay-plane target; §10 — security defense-in-depth.
> - `docs/product-roadmap/upcomming-convo.md` §3–§6 and `todo.md` §2 — Option B control-plane/data-plane target and the 13 domain boundaries.
> - `FEATURE_INVENTORY.md` — the feature-level evidence for everything summarized here.

## 1. Shape: a modular monolith

ReForwarder **is a modular monolith**: one Go codebase, one binary family (`cmd/posta`), one PostgreSQL system of record, one Redis, package-per-domain boundaries under `internal/` with strict `routes → handlers → services → repositories → models` layering (`06-architecture-impact.md` §1, §2.2). The Vue dashboard is compiled and **embedded into the Go binary** at build (3-stage Dockerfile). No microservices exist today, and the roadmap explicitly does not propose splitting the existing domains — every planned change is a new package, table, middleware, or interface extraction inside the same process (`06-architecture-impact.md` §1, §13).

Key internal/ package groups (engine roles map onto existing packages; full table in `06-architecture-impact.md` §2.2):

| Role | Packages |
|---|---|
| Core API + authZ | `internal/routes` (~13 route files, ~340 route defs), `internal/handlers`, `internal/middlewares` (JWT/API-key auth, workspace resolution, scopes, security gate) |
| Delivery engine (outbound) | `internal/services/email` (send gates, renderer, DKIM signer, SMTP sender), `internal/worker/handler.go` |
| Inbound / relay plane | `internal/services/{inbound,smtprelay,messagescan,inbox,messages}` |
| Queue engine | `internal/queues`, `internal/worker/producer.go` (asynq, deterministic TaskIDs) |
| Campaign engine | `internal/worker/campaign_processor.go`, `internal/cron/jobs/{campaign_scheduler,campaign_restart}.go`, `internal/handlers/campaign_handler.go` |
| Audience engine | `internal/models/subscriber*`, `contact*`, `internal/storage/repositories/segment_query.go` (guarded SQL builder) |
| Template engine | `internal/services/email/template_renderer.go`, `system_vars.go`, `internal/models/template*` |
| Automation engine | `internal/services/automation` (journey engine, nodes, triggers, SSRF-guarded webhook node) |
| Analytics engine | `internal/services/{tracking,emailhistory}`, `internal/storage/repositories/{tracking_repo,analytics_repo}.go` |
| Webhook engine | `internal/services/webhook` (11-event registry, backoff, per-attempt history, HMAC) |
| Security engine | `internal/services/security` (fail2ban-style: ZSET windows, ban tiers, CIDR pub/sub, fail-open) |
| Provider-signal plane | `internal/services/provider_events` (SES/SNS, SendGrid, Postmark, Mailgun adapters + bounce classifier) |
| Infrastructure | `internal/{config,health,metrics,workermon,cron,dto,error_handlers}`, `internal/services/{cache,ratelimit,realtime,plan,settings,seeder,eventbus,notification,crypto}`, `internal/storage/{migration,repositories,blob}` |
| UI | `internal/web` (embedded Vue dist) |

## 2. Entry points & process topology

Source: `README.md` §Architecture; `03-existing-feature-audit.md` §2.1–§2.2, §5; `cmd/posta/main.go:37-53`.

| Entrypoint | What it runs | Notes |
|---|---|---|
| `posta server` | HTTP API + dashboard (:9000), SMTP listeners, cron fleet, optional embedded worker (`POSTA_EMBEDDED_WORKER`) | Wiring order: config → crypto init (`POSTA_ENCRYPTION_KEY`) → Postgres (GORM AutoMigrate + versioned upgrade steps) → Redis → okapi routes with OpenAPI/Scalar (`/docs`) → cron registration |
| `posta worker` | Standalone asynq consumer with its own health/metrics server (`/healthz` `/readyz` `/metrics`) | Stateless; scale horizontally; compose runs it as a separate service |
| `posta doctor` | Workspace-integrity checker (NULL-`workspace_id` unreachable rows, system workspace, plan backfill) | Not a general CLI client |

Listener/port surface: HTTP API + dashboard `:9000`; inbound SMTP receiver `:2525` (default-off); SMTP relay submission `:2526` (default-off); Prometheus `/metrics` (default-off, unauthenticated when enabled). Both server and worker are stateless — all durable state lives in PostgreSQL, Redis, and object storage (FS or S3), so workers scale independently of request traffic.

## 3. Delivery plane (outbound, inbound, relay)

**Outbound pipeline** (detail + AS-IS flow diagram: `06-architecture-impact.md` §2.3; feature evidence: `FEATURE_INVENTORY.md` §4):

1. `POST /emails/send` passes the service gates: address validation → maintenance gate → plan rate limit → attachment limits → domain check → suppression filter → tracking rewrite.
2. The email row persists in Postgres (`queued`, or `scheduled` when `send_at` is set) and is enqueued to asynq/Redis with a **deterministic TaskID** (`email:send:<id>`) that collapses duplicate enqueues.
3. The worker re-checks campaign pause/cancel, selects the sending server (first enabled workspace server, else admin shared server matched by verified sender domain with strict-mode ownership check), re-checks suppression, stamps headers, applies **managed DKIM signing**, and delivers over `net/smtp` to the upstream relay with dial timeouts + session budgets (timeout → transient, never suppresses).
4. Failure handling is layered: (1) asynq retries + exhausted-error handler → `email.failed`; (2) bounded, jittered DB retry sweep; (3) manual retry endpoint; (4) `campaign_restart` watchdog; (5) `queue_reconcile` cron that re-enqueues lost `queued` rows. 5xx at RCPT → classified (14 classes) → hard bounce + auto-suppress. Weighted queues `transactional=6 / bulk=3 / low=1` (probabilistic fair-share, not strict priority).

**Inbound** (default-off): go-smtp receiver (`:2525`, RCPT gated to verified recipient domains) or secret-protected webhook ingest (`POST /api/v1/inbound/webhook`) → store-first, async MIME parse → attachments to blob store → SSE stream + webhook fan-out; the same pipeline now detects and ingests DMARC aggregate reports (`R-17`). The relay path authenticates SASL PLAIN against `smtp_credentials` (bcrypt, per-credential IP allowlists, per-IP rate limit, security-engine guard) and injects into the same outbound pipeline. Campaign fan-out runs in batches of 100 with self-re-enqueue, per-campaign SendRate pacing, hold-back A/B winner selection, and per-timezone windows — all CAS/atomic-claim based (evidence: `FEATURE_INVENTORY.md` rows RF-F041…RF-F060).

**Known delivery-plane gaps (tracked, not hidden):** scheduled transactional sends have no cancel and are invisible to `queue_reconcile` (GAP-01, P0); the legacy `/webhooks/bounce` path lacks suppression/complaint parity (GAP-02 residual, P0); no cron leader election — all replicas run all 10 cron schedules (GAP-03, P0). The 10-job cron fleet, queue admin surface, and Redis role/failure-semantics matrix are documented in `06-architecture-impact.md` §2.8 (Audit §2.8) and §6.

## 4. Signal plane (tracking, events, webhooks, provider events)

Two deliberately separate event stores (do **not** merge — duplicate finding D7): `tracking_events` (campaign open/click time series, HMAC-signed pixel + rewritten links) and `email_events` (per-message timeline; 8 event types — `sent` on SMTP acceptance, true `delivered` since provider-event ingestion landed in `6d2d7c0` (timeline events only — `internal/services/provider_events/service.go:213`; the `email.delivered`/`email.complained` outbound webhook emission, delivered-state analytics, and dead delivered UI paths remain open per RF-F099), `complained` still API-only). Outbound webhooks: an 11-event registry (enum-pinned by test), HMAC-SHA256 signatures (timestamp/replay window pending — GAP-14), bounded exponential backoff with jitter, per-attempt delivery history, and operator replay of failed deliveries. Provider events: `POST /webhooks/provider/{provider}` with per-provider signature verification (SES/SNS, SendGrid, Postmark, Mailgun), replay windows, and the full permanent-bounce/complaint side-effect chains into suppression, bounce rows, and analytics (evidence: `FEATURE_INVENTORY.md` RF-F051…RF-F055, RF-F083…RF-F087; residual work: RF-F053, RF-F099).

Observability rides the same plane: 20+ Prometheus families (default-off), `/healthz` `/readyz`, worker Redis heartbeats with version mismatch detection, admin SSE streams, and the audit-log/`events` surface (three read-model views over one table — duplicate finding D2, consolidation pending).

## 5. Security architecture (defense-in-depth summary)

Layered order (diagram: `06-architecture-impact.md` §10): security-engine bans (TCP accept / EHLO 421 / HTTP 403, fail-open by design) → session JWT with Redis JTI blacklist or `psk_` API key (SHA-256 hashed, expiry, CIDR allowlist, per-key rate limits) → structural `RequireScope` derivation from route+method → `resolveWorkspace` tenancy (workspace-bound keys are owner-equivalent only inside their workspace) → per-route-group role floors → handler/service logic (plan quotas, verified-domain gates, suppression filter) → shared layered SSRF guard on all outbound HTTP → HMAC-signed outbound webhooks. Credentials and DKIM keys are AES-256-GCM encrypted at rest (caveat: `POSTA_ENCRYPTION_KEY` must be set or encryption degrades to reversible base64). `/admin/*` is session-JWT only — API keys structurally cannot reach it. Tenant isolation is verified strong and is the pattern every new route group must keep (`06-architecture-impact.md` §2.4, R16).

## 6. Option B direction: control plane / data plane

Source: `docs/product-roadmap/upcomming-convo.md` §3–§7; `todo.md` §2 (Track A). Decision: keep the existing Go application + Vue dashboard as the **control plane**; introduce dedicated **mail-data-plane components** for protocols and message transport. This is not thirteen microservices — the 13 listed domains (`mailserver`, `mta`, `inbound`, `mailbox`, `mailstore`, `imap`, `pop3`, `mailrouting`, `reputation`, `dnsmanager`, `spamfilter`, `malware`, `webmail`) are logical ownership boundaries, starting from a small number of independently deployable protocol components. Boundary rules (abridged; full list in `todo.md` §2):

- The control plane owns tenants, identities, domains, mailbox metadata, credentials, policies, API and orchestration; PostgreSQL remains the **control-plane** system of record (message bodies and mailbox operations are not assumed into the relational schema).
- Dedicated protocol services own SMTP transport, inbound reception, mailbox access and message transfer — via explicit service interfaces and durable events, never private Go-app internals; independently restartable and observable without taking down the dashboard/API.
- Tenant identity stays consistent across all data-plane services; mature protocol software is integrated where safer than building from scratch ( Postal as infrastructure reference, not ported code; license discipline — no AGPL/BSL source copying).
- Four non-negotiable rules: never expose an open relay; never trust client-supplied tenant IDs; never report queue acceptance as delivery; never accept inbound mail without a durable path to deliver/route/quarantine/reject it.

**Phased programme** (source: `upcomming-convo.md` §7, `todo.md` Track A): Phase 0 re-audit + ADR-001…009 (architecture-only, no production code) → Phase 1 outbound MTA foundation (direct-to-MX + upstream relay modes, transport queues, delivery-attempt records) → Phase 2 inbound mail & routing → Phase 3 mailbox storage foundation (not the transactional `Email` model) → Phase 4 IMAP/POP3 → Phase 5 webmail & mailbox administration → Phase 6 platform convergence (IP pools, warm-up, reputation, DNS/certificate automation, backup/DR). The relay roadmap's P0 trust/correctness work (Track B Phase 0: GAP-01/02/03/11/12) is retained and folds into the corresponding Track A phases; the Track B `Sender` seam (GAP-04) is the precondition the MTA adapter reuses.

**What changes inside the existing monolith first** (relay-plane target, `06-architecture-impact.md` §4): a Redis-lease cron leader lock, a `Sender` interface seam with ESP API drivers and health-aware server selection, the delivery-signal plane completion (real `delivered`/`complained` end-to-end), and observability extensions — all in-process. Target diagrams: `06-architecture-impact.md` §4.1 (relay plane), `upcomming-convo.md` §3 (control/data planes), `todo.md` §2 and §2.2 (both, with NEW markers).

## 7. What must not break

From `06-architecture-impact.md` §2.5 (verified strengths) and §11 (byte-compatibility guarantees): tenant isolation by construction; the guarded segment SQL builder and its injection tests; the atomic scheduler claim/rollback pattern; suppression re-check in the worker and the 5xx auto-suppress path; the shared SSRF guard for any new outbound HTTP; the `{success, data}` envelope; the test-pinned webhook event registry; existing HMAC tracking-token formats; `applyFilterRules` legacy behavior until consolidation parity is proven; and Postgres as the sole system of record (Redis is ephemeral — reconcile crons depend on that).
