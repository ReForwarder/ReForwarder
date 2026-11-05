# ReForwarder Capability Audit (source-verified)

> **Audited:** ReForwarder `main` @ `7a84e46` ("ui: update automations sidebar icon", 2026-11-04). Go backend (`cmd/posta` server + worker, okapi HTTP framework, GORM/Postgres, Redis, asynq) and Vue 3 web app. This document is the ReForwarder side of the comparison in `stalwart-feature-gap-analysis.md`; Stalwart-side findings live in `stalwart-feature-inventory.md`; priorities live in `reforwarder-stalwart-roadmap.md`.
> **Method:** every claim verified in the current checkout with file paths; statuses use the comparison taxonomy (FULLY IMPLEMENTED / PARTIALLY IMPLEMENTED / BACKEND ONLY / CONFIGURATION ONLY / PLACEHOLDER / BROKEN / NOT IMPLEMENTED / NOT APPLICABLE / NEEDS VERIFICATION). Untracked working-tree files at audit time: `DEVELOPMENT_HISTORY.md`, `compose.override.yml`, and the four research documents of this analysis (deliberately not committed per the task's research-only rule).

## Part 1 — Backend inventory

### 1.1 Email sending pipeline — **PARTIALLY IMPLEMENTED** (relay-based; no MX delivery, no DKIM signing, no DSN)

**Evidence:** `internal/services/email/smtp_sender.go` (540 lines), `internal/services/email/service.go`, `internal/services/smtprelay/{backend,smtp_server}.go`, `internal/services/retry/worker.go`, `internal/worker/handler.go:197-250,343-347`, `cmd/posta/server.go:215-229,388-508,755-771`.

- **Outbound delivery is relay-only.** `SMTPSender.Send` is a `net/smtp` client delivering to a per-workspace/per-user configured SMTP server (`models.SMTPServer`: SSL/STARTTLS/plain, AUTH PLAIN+LOGIN) or a platform "shared server" matched by verified sender domain (`worker/handler.go:197-250`). There is **no direct-to-MX delivery** (no recipient-domain MX lookup), no connection pooling, no per-message throttling, no SMTPUTF8/8BITMIME client negotiation.
- **RFC 5322 construction is solid:** Date/Message-ID generation, RFC 2047 folding, quoted-printable, `List-Unsubscribe` + `List-Unsubscribe-Post` (RFC 8058) headers, `X-Mailer`/`X-Posta-*` stamping + HMAC (`stamper.go`).
- **Bounces/DSN:** no bounce-mailbox, no VERP, no DSN parsing. Bounces arrive via (a) synchronous RCPT-5xx detection at send time (`permanentRejection` → auto-suppress + bounce record when `POSTA_AUTO_SUPPRESS_ON_REJECT`, default on), (b) a provider webhook (`POST /api/v1/webhooks/bounce`, API-key auth), (c) an authenticated API. `delivered` email-event type is **defined but never produced** — no true delivery-confirmation source exists.
- **Retries:** asynq task retries (attempt counting → failed + `email.failed` webhook on exhaustion) plus a 5-minute polling `retry.Worker` honoring per-SMTP-server `max_retries`.
- **Inbound submission relay** (`POSTA_SMTP_RELAY_ENABLED`, default off): SASL PLAIN against DB credentials, per-credential IP allowlists, rate limits, security-engine session guard, relays into the email pipeline. **Inbound mail receiver** (`POSTA_INBOUND_ENABLED`, default off): accepts for verified recipient domains only, store-first ingest → async parse → attachments (S3/fs, signed URLs) → webhook fan-out + SSE.

### 1.2 Delivery queue — **FULLY IMPLEMENTED**

**Evidence:** `internal/worker/{producer,tasks,handler}.go`, `cmd/posta/server.go:567-678`, `internal/handlers/admin_handler.go:703-745`, `internal/handlers/email_handler.go`.

asynq/Redis queues (`transactional` w=6, `bulk` w=3, `low` w=1); every send persists an `emails` row (draft→queued→processing→sent/failed/suppressed); **post-audit correction:** the enqueue is *not* TaskID-deduped for single-email sends (only campaign:start and message tasks get TaskIDs) — see the non-atomic-claims finding (roadmap R-26); worker re-checks suppression and live campaign state (pause/cancel enforced lazily); `email_events` timeline per message; webhooks on sent/failed. Admin queue visibility: asynq `Inspector` server stats, Redis worker heartbeats, version-mismatch detection, SSE `/admin/metrics/stream` + `/admin/jobs`. Per-message: status view + retry endpoint. **Gaps:** no direct queued-task cancel/delete endpoint; no dead-letter inspection UI; no time-based deferral of individual sends (FUTURERELEASE-equivalent).

### 1.3 AuthN/AuthZ & security — **FULLY IMPLEMENTED**

**Evidence:** `internal/middlewares/{middleware,workspace}.go`, `internal/services/{session,twofactor,ratelimit,audit,security}`, `internal/handlers/{user_handler,oauth_handler,oauth_admin_handler}.go`.

JWT sessions (HttpOnly cookie/Bearer) with Redis jti revocation; API keys `psk_…` with scopes `read/write/send/admin/*` + per-key CIDR allowlists; structural scope enforcement inside workspace middleware; API keys can never reach `/admin/*`. Workspace RBAC (owner/admin/editor/viewer via `RequireWorkspaceRole`). **TOTP 2FA** (setup/verify/disable, optional platform-wide enforcement, email alerts; no recovery codes). bcrypt passwords + self-service reset. **Security engine** (`internal/services/security`, Stalwart-clean-room): sliding-window Redis signals (port scan, connect abuse, TLS failures, pre-auth garbage, suspicious paths, dual-keyed brute force), weighted risk, escalation tiers 1h→24h→7d→30d, CIDR snapshot + pub/sub propagation, allowlist, fail-open; enforcement at TCP accept (`CountingListener`), EHLO (421), and HTTP 403 middleware; admin API + SSE. **Audit logging** via event bus with workspace + admin views + CSV export. **Rate limiting:** Redis hourly/daily sender limits (plan-overridable), per-IP login limiter, SMTP per-IP session limits, per-form/per-workspace message limits. **OAuth/OIDC:** Google/GitHub/custom OIDC providers, account linking, per-workspace SSO with email-domain discovery, admin CRUD.

**Gaps vs Stalwart:** no WebAuthn/passkeys, no recovery codes, no refresh-token rotation, no per-API-key rate limits, no admin impersonation.

### 1.4 Domains & deliverability — **PARTIALLY IMPLEMENTED** (verification yes; signing no)

**Evidence:** `internal/services/domain/verifier.go`, `internal/models/domain.go`, `internal/handlers/{domain_handler,admin_domain_handler}.go`, `internal/services/email/service.go:1531-1571`, `internal/services/verifier/`.

Real DNS verification: ownership TXT (`posta-verification=<token>`), SPF presence (`v=spf1`), DKIM presence (TXT **or CNAME** at `posta._domainkey.<domain>` — i.e. assumes an **external/upstream DKIM signer**), DMARC presence. `IsFullyVerified` gates conditional enforcement (send path check is setting-gated; campaign send only **warns**). **DKIM signing of outbound mail: NOT IMPLEMENTED** (no DKIM-Signature anywhere; user-supplied `dkim-*` headers are even stripped). No SPF/DMARC evaluation of inbound senders; no DMARC aggregate report consumption; no MTA-STS/DANE/TLS-RPT. **Recipient email verification** is a real, wired feature (syntax, disposable/role detection, MX, Redis cache, suppression overlay). Bounce alert cron on workspace thresholds.

### 1.5 Campaigns — **PARTIALLY IMPLEMENTED**

**Evidence:** `internal/handlers/campaign_handler.go`, `internal/worker/campaign_processor.go`, `internal/cron/jobs/campaign_restart.go`, `internal/models/campaign.go`.

Lifecycle draft→sending (atomic claim + dedup enqueue), pause (worker releases queued mail), cancel, duplicate, per-campaign stats. Audience: subscriber list (static members or **dynamic filter rules**) XOR **segment** (saved condition tree, shipped in `fd45e18`); eligibility = `status==subscribed` + per-list unsubscribes (+ global suppressions at send time). Batch 100 with self-re-enqueue; stuck-campaign watchdog (5 min). Templates: full versioning, per-version localizations with fallback chain, stylesheets, languages, import/export, send-test, reserved variables. Unsubscribe: RFC 8058 one-click (list-managed + transactional), hosted pages, per-list resubscribe, webhook + automation trigger.

**Gaps (verified dead code / stubs):**
1. **Scheduled campaigns never fire** — `CampaignRepository.FindScheduledReady()` (`campaign_repo.go:152`) has **zero callers**; the restart cron only revives already-`sending` campaigns. `scheduled` is a dead-end status.
2. **Timezone-aware sending is a stub** (`campaign_processor.go:230-235` logs intent, sends one batch).
3. **A/B tests have no winner logic** — variant assignment + subject override only; no statistics-driven winner/auto-send.
4. Segment audiences skip per-list unsubscribes by design (list-keyed); global suppressions still apply.

### 1.6 Audience — **FULLY IMPLEMENTED**

Subscribers CRUD + JSON/CSV import + custom fields; subscriber lists static + dynamic (`applyFilterRules` engine); **Segments** (saved condition groups over subscribers, preview + contacts endpoints); suppression lists (workspace + per-unsubscribe-list); contacts send-ledger; GDPR delete endpoints; public subscribe endpoint.

### 1.7 Automations — **FULLY IMPLEMENTED**

Triggers: `list.subscribed`, `contact.created`, `contact.unsubscribed`, `email.opened/clicked/bounced`, custom ingest events. Nodes: trigger, delay (journey parks with `next_run_at`), email (dedicated email-service clone with tracking), filter (custom-field aware), ab_test (variant selection only), list_status, add_to_list/remove_from_list, webhook (SSRF-guarded via the shared guard). Worker-claimed journeys (`FOR UPDATE SKIP LOCKED`), per-node retry/backoff, journey/node-run persistence, stale sweep. **Gaps:** no scheduled/scheduled-time triggers, no segment-membership conditions (documented deferral), ab_test node has no winner logic.

### 1.8 Tracking & analytics — **FULLY IMPLEMENTED** (with the `delivered` caveat)

Open pixel + HMAC-signed click redirects, per-day dedup, global toggle, send-time link rewriting. Analytics: daily counts, status breakdown, delivery/bounce-rate trends, latency percentiles, provider breakdown — SQL over `emails`/`bounces`, Redis-cached, user + admin scopes. 8-type email-event timeline backing the Logs UI. Caveat: `sent` and `delivered` are both recorded at SMTP-accept; there is no true delivery signal.

### 1.9 Logs & observability — **FULLY IMPLEMENTED** (Prometheus default-off)

Message history (content-privacy gating), incoming-webhook store, outgoing-webhook deliveries + attempts, audit log + CSV. Prometheus text endpoint `/metrics` (`POSTA_METRICS_ENABLED` default **false**) with email/webhook/bounce/suppression/HTTP metrics. `/healthz` + `/readyz` (DB+Redis ping). SSE: admin metrics/events/security streams + inbound live stream. Request logging with RequestID. No OpenTelemetry.

### 1.10 Webhooks — **FULLY IMPLEMENTED**

Outbound dispatcher (HMAC `X-Posta-Signature: sha256=…`, filters, retries, sanitized persisted deliveries + attempts, metrics) behind the shared SSRF guard (validate → resolve → pin validated IP, Control re-check, redirects blocked, optional ack-gated proxy pinning — commits `5c2678a`, `fa383b3`, `7fd30eb`). Incoming: bounce webhook (API key) + inbound-email webhook (shared secret, constant-time compare, dedup), payloads persisted + viewable. Automation webhook nodes share the guard. Event catalog: email sent/failed, campaign started/completed, message/inbound events, unsubscribed, complained.

### 1.11 Storage & jobs — **FULLY IMPLEMENTED**

Blob storage behind one interface (S3 with static creds + fs) for outbound/inbound attachments + workspace export; retention cron; Postgres + GORM code migrations + versioned upgrade steps; Redis multi-purpose (cache, rate limits, session blacklist, security windows, pub/sub, SSE fanout, asynq broker; **no fallback broker in dev** — HTTP send falls back to synchronous); 8 cron jobs (retention, update check, daily report, account cleanup, API-key expiry, bounce alert, campaign restart, message digest); GDPR workspace export/import bundle.

### 1.12 Config & API surface — **FULLY IMPLEMENTED**

~90 `POSTA_*` env vars with cross-validation (placeholder-secret refusal, inbound/relay flag checks); DB-backed settings provider layered over env defaults; okapi-generated OpenAPI from declarative tags (~200+ routes, 15 groups); compose + full-example compose; CI (build/test), lint, release, docs (Docusaurus) workflows; `Makefile` embeds the UI via `go:embed`.

## Part 2 — Frontend & infrastructure

### 2.1 Route inventory — complete and consistent

`web/src/router/index.ts` (187 lines): 60+ routes in 10 groups (Overview, Sending, Inbound, Audience, Deliverability, Logs, Developers, Infrastructure, Workspace, Admin) — every nav item in `DashboardLayout.vue navSections` resolves to a real view; no orphan nav entries; catch-all NotFound. The API Reference nav item is an external link to backend-served Scalar UI at `/docs` (okapi-generated OpenAPI), shown when `appInfo.openapi_docs`.

### 2.2 View→backend wiring — **very high** (verified per view against `internal/routes/*.go`)

37 api modules; every audited view calls real, existing endpoints and renders real data. Highlights: Dashboard (60s refresh + health from stats), Analytics (3 endpoints, dual ops/admin scope), Emails (list/retry/preview), Campaigns (CRUD + send/pause/resume/cancel + messages + analytics), Templates (versions/localizations/stylesheets/languages/import/export/send-test), Inbound (SSE `inbound-stream`), Messages/Forms/Filters (SSE `message-stream`, rotate-key, snippet, filter test), Subscribers (JSON/CSV import), Subscriber Lists (members + dynamic rules), Segments (CRUD + preview + contacts — correctly wired), Automations (@vue-flow builder + executions + stats + test events), Logs (4 surfaces + CSV export), API Keys (revoke), SMTP Relay/Servers (test), Domains (verify), Security admin (4 pages + SSE), Admin (users incl. force-delete/cancel-deletion, workspaces, plans, settings, announcements, jobs, OAuth providers).

### 2.3 Gaps found

1. **BROKEN — Subscriber List "Preview segment" button** (`web/src/api/subscriberLists.ts:30`): calls `POST /workspaces/current/subscriber-lists/preview`, but the backend registers `POST /subscriber-lists/preview-segment` (`workspace_resource_routes.go:1092`); the view also expects a pageable shape (`res.data.data ?? []`, `pageable.total_elements` — `SubscriberListDetail.vue:144-147`) while the handler returns `okapi.M{"count": N}` (`subscriber_list_handler.go:351`). The button always toasts "Failed to preview segment". **The new Segments feature's preview is wired correctly — this is the older dynamic-list preview that broke when the endpoint was renamed.** → roadmap **R-21** (P0, XS).
2. **Sending is API-only by design**: `POST /emails/send|batch|send-template|verify` have no compose UI anywhere (UnsubscribeLists.vue even embeds a curl snippet as documentation); Emails.vue lists/filters/retries. Fine for a forwarding platform; a compose screen would be a product decision.
3. **Backend-only endpoints**: `POST /campaigns/{id}/duplicate` (backend has it, `campaigns.ts` doesn't), `GET /emails/{id}/status`, `GET /admin/api-keys`, `GET /admin/emails` (api functions exist in `admin.ts` but no view calls them).
4. **Feature flags invisible to the UI**: `AppInfo` exposes only `{name, version, commit_id, openapi_docs}`; `POSTA_MESSAGES_ENABLED=false` still shows Messages/Forms/Spam Filters in nav — those pages error when the feature is off.
5. Frontend tests: vitest configured but one spec file exists. Zero TODO/stub markers anywhere (grep clean).
6. Design system: coherent Resend-derived token system (light/dark via `[data-theme]`), only 9 shared components (page-local trees elsewhere), hand-rolled SVG charts (no chart lib).

### 2.4 Infrastructure — **FULLY IMPLEMENTED, production-grade posture**

- **Compose**: `compose.yml` (app + worker + postgres:17 + redis:7, healthchecks, named volumes; explicitly dev-mode credentials) + untracked local `compose.override.yml` (inbound/relay ports); prod examples in `examples/` (`${POSTA_JWT_SECRET:?required}`).
- **Image**: 3-stage `docker/Dockerfile` — node:24 builds `web/dist` (glibc over alpine, documented Rolldown musl bug), golang:1.27 CGO-free build embedding the UI via `go:embed`, alpine:3.21 non-root runtime with HEALTHCHECK. One self-contained binary.
- **CI**: ci.yml (backend tests against real PG/Redis services, frontend type-check + build, docker build on main), lint.yml (golangci-lint **pinned to v2.13.2** so a new linter release can't turn a green branch red), release.yml (multi-arch buildx; `-rc` tags never become `latest`), deploy-docs.yml (Docusaurus → Pages).
- **Config**: ~100 `POSTA_*` env vars with production validation (placeholder secrets rejected); **three-layer settings** — env → DB platform settings (30s cache, edited live in `/admin/settings`, consumed by the rate limiter at runtime) → user/workspace settings.
- **Data stores**: Postgres 17 (all business data); Redis in four distinct roles (asynq broker, generic cache, rate limiting, SSE fanout); blob storage `fs` (default) or `s3` (static creds, path-style/MinIO-compatible). **No backup tooling in-repo** — named volumes are the whole persistence story (→ roadmap R-11).
- **Observability**: `/healthz` (liveness) + `/readyz` (DB+Redis 503), Prometheus `/metrics` default-off, admin SSE dashboards. No OTel.
- **Docs**: substantive Docusaurus site (getting-started + 15 feature docs), `DEVELOPMENT_HISTORY.md`, `UPSTREAM_BASELINE.md` (fork provenance + sync policy), `SECURITY.md`, `docs/research/` + `docs/security-research/`.

### 2.5 Frontend/infra status table

| Area | Status |
|---|---|
| Route inventory / nav consistency | FULLY IMPLEMENTED |
| View→backend wiring | FULLY IMPLEMENTED (one broken control: R-21) |
| Compose + Dockerfile + CI/CD | FULLY IMPLEMENTED |
| Env/config layering + validation | FULLY IMPLEMENTED |
| Prometheus metrics | PARTIALLY IMPLEMENTED (off by default, undocumented in UI) |
| Backup/restore tooling | NOT IMPLEMENTED |
| Frontend test coverage | PARTIALLY IMPLEMENTED (1 spec file) |
| Feature-flag surfacing in UI | NOT IMPLEMENTED |

## Part 3 — Summary table

| Subsystem | Status |
|---|---|
| Outbound SMTP (relay-based) | PARTIALLY IMPLEMENTED |
| Inbound SMTP receiver / submission relay | FULLY IMPLEMENTED (default-off) |
| Delivery queue (asynq) + retries | PARTIALLY IMPLEMENTED (queue + retries + visibility complete; no cancel/dead-letter/deferral controls — roadmap R-03) |
| AuthN/AuthZ, RBAC, sessions | FULLY IMPLEMENTED |
| 2FA (TOTP) | FULLY IMPLEMENTED at Stalwart parity (both TOTP-only; recovery codes are a ReForwarder self-improvement — R-10) |
| Security engine (fail2ban-style) | FULLY IMPLEMENTED |
| Audit logging / rate limiting | FULLY IMPLEMENTED |
| OAuth/OIDC + workspace SSO | FULLY IMPLEMENTED |
| Domain verification (SPF/DKIM/DMARC presence) | PARTIALLY IMPLEMENTED (verification complete; DKIM signing absent — R-02) |
| DKIM signing | NOT IMPLEMENTED |
| MX delivery / DSN parsing / true delivery status | NOT IMPLEMENTED |
| Bounce processing | PARTIALLY IMPLEMENTED |
| Campaigns (scheduling, A/B winner) | PARTIALLY IMPLEMENTED |
| Templates (versions/i18n) / Unsubscribe (RFC 8058) | FULLY IMPLEMENTED |
| Audience (subscribers/lists/segments/suppressions) | FULLY IMPLEMENTED |
| Automations | FULLY IMPLEMENTED |
| Forms/messages inbox | FULLY IMPLEMENTED (default-off) |
| Tracking / Analytics | FULLY IMPLEMENTED |
| Logs / health | FULLY IMPLEMENTED; Prometheus metrics | PARTIALLY IMPLEMENTED (endpoint exists, default-off, undocumented in UI — R-12) |
| Webhooks (out+in, SSRF guard) | FULLY IMPLEMENTED |
| Inbound email pipeline | FULLY IMPLEMENTED (default-off) |
| Blob storage / retention / GDPR | FULLY IMPLEMENTED |
| Config validation / CI / OpenAPI | FULLY IMPLEMENTED |
