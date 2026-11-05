# 03 — ReForwarder Existing-Feature Audit: Current State of the Product

> **Doc 3 of 7** in the ReForwarder product-roadmap documentation suite. This document records the **actual current state of ReForwarder**: every registry feature mapped to its implementation (or absence) with exact file paths, endpoints, models, and tests. IDs are permanent registry IDs — see **`01-oss-feature-inventory.md`** (§ "ID scheme") for the ID scheme and project codes; gap analysis consumes this document's PARTIAL/MISSING verdicts in **`04-feature-gap-analysis.md`**; deep architecture prose lives in **`06-architecture-impact.md`**.

- **Audit date:** 2026-11-05.
- **Baseline:** working tree at HEAD `a6f1681` ("Add SMTP timeouts, DKIM signing, and delivery-queue administration", 2026-11-05); parent `347ebb4` ("Implement scheduled-campaign scheduler and fix list segment preview"). Stack: Go 1.27 + okapi + GORM/PostgreSQL + Redis/asynq + robfig/cron + emersion/go-smtp + Prometheus; Vue 3.5 + TypeScript dashboard.
- **Scale:** ~340 API route definitions; 33 backend service packages; ~55 models; 10 cron jobs; 106 committed Go test files; 2 frontend spec files.
- **Method:** five category audits (A–B, C–D, E–F, G–H, I–J) each traced **route → handler → service → model/repo → test** for every non-MISSING verdict; the seed capability audit (`docs/research/reforwarder-capability-audit.md`, drafted at `7a84e46`) was re-verified claim-by-claim and its corrections are folded in (audit files' correction §§ govern). Verdicts were normalized into the canonical registry (`registry.md` v1.0.0, frozen 2026-11-05); this document reproduces the registry's per-entry evidence. Where audit files disagreed, the later audit's verified evidence wins — conflicts resolved are listed in §8.2.
- **Commit cutoff:** statuses reflect **committed HEAD `a6f1681` only**. Capabilities that existed as uncommitted WIP when the early audits ran (SMTP dial timeouts, DKIM signing, delivery-queue admin, TaskID dedup, scheduled transactional sends) are **committed at `a6f1681` and recorded IMPLEMENTED** with the commit note. Post-`a6f1681` uncommitted work is **excluded** and described separately in §2.10 — it is **in progress, pending commit**, never counted as IMPLEMENTED.

## Status legend

| Status | Meaning |
|---|---|
| IMPLEMENTED | Verified in code at the baseline; end-to-end evidence (route → handler → service → model → test where they exist) |
| PARTIAL | Core present with a named, verified functional gap |
| MISSING | Verified absent (grep + route/handler/model sweep) |
| NEEDS_REFACTOR | Present but the implementation approach needs rework (0 in registry) |
| NEEDS_VERIFICATION | No audit-file verdict; requires follow-up |
| NOT_APPLICABLE | Out of product scope (verified per-entry) |

**Registry totals (authoritative, frozen):** 108 IMPLEMENTED · 46 PARTIAL · 38 MISSING · 0 NEEDS_REFACTOR · 3 NEEDS_VERIFICATION · 4 NOT_APPLICABLE across 199 entries. Reconciliation applied at synthesis: SEC-016 moved NEEDS_VERIFICATION → IMPLEMENTED (see §3.I and §8).

---

# 2. Repository & Architecture

## 2.1 Repository structure

```
ReForwarder/
├── cmd/posta/            # Entrypoints: server.go, worker.go, doctor.go, main.go (subcommand router)
├── internal/             # All backend code (routes, handlers, services, models, storage, worker, cron, …)
├── web/                  # Vue 3.5 + TypeScript dashboard (Vite + Pinia), embedded into the Go binary at build
├── docker/               # 3-stage Dockerfile (ui-builder → builder → runtime)
├── docs/                 # Docusaurus site (docs/docs/**, incl. docs/docs/sdks/*), implementation notes,
│                         #   research/ (seed capability audit), product-roadmap/ (this suite)
├── examples/             # Usage examples
├── site/                 # Marketing/site assets
├── compose.yml           # server + worker + postgres:17 + redis:7 with healthchecks
├── Makefile              # build / build-ui / build-all / test / vet / lint / fmt / tidy / dev targets
├── .github/workflows/    # ci.yml, lint.yml, release.yml, deploy-docs.yml
├── .golangci.yml         # Pinned golangci-lint config
├── go.mod / go.sum       # Go 1.27; okapi, GORM, asynq, robfig/cron, emersion/go-smtp, go-msgauth
├── REFORWARDER_PRE_DEVELOPMENT_AUDIT.md, DEVELOPMENT_HISTORY.md, UPSTREAM_BASELINE.md, SECURITY.md, NOTICE, README.md
```

### internal/ package map

| Package | Contents |
|---|---|
| `config` | All `POSTA_*` env settings + defaults (`config.go`), `config/security.go` (trusted-proxy warning, ban-engine enforcement mode) |
| `routes` | ~13 route files registering okapi `RouteDefinition`s (§2.5); route-order + RBAC integration tests live here |
| `handlers` | HTTP handlers per resource (email, template, campaign, subscriber, segment, automation, admin, queue, domain-signing, provider-event (in-flight)…) |
| `services` | 33 domain packages (list in §2.2) |
| `models` | ~55 GORM models (§2.4) |
| `storage` | `repositories/` (one repo per aggregate + guarded `segment_query.go`) and `migration/` (AutoMigrate, constraints, `upgrade/` versioned steps) |
| `worker` | asynq producer (`producer.go`), task handlers (`handler.go`), campaign batch processor (`campaign_processor.go`) |
| `cron` | manager (`cron/manager.go`) + 10 jobs (`cron/jobs/`, §2.8) |
| `middlewares` | auth (JWT/API-key), workspace resolution + RBAC, scopes, CORS, security-engine HTTP gate |
| `dto` | Typed request/response docs incl. the 11-event `webhook_registry.go` |
| `metrics` | 20 Prometheus metric families (`metrics.go`), `POSTA_METRICS_ENABLED`-gated |
| `queues` | asynq queue/TaskID constants (`queues.go`: transactional/bulk/low, `EmailSendTaskID`) |
| `health` | `/healthz` `/readyz` probes (DB+Redis ping, `health.go:62-87`) |
| `error_handlers` | Centralized error mapping to the `{success, data}` envelope |
| `web` | Embedded dashboard assets (`dist/` staged by `make build-ui`) |

## 2.2 Backend architecture

- **Entrypoints** (`cmd/posta/main.go:37-53`): `server` (API + optional embedded worker + cron), `worker` (standalone asynq consumer with its own health/metrics server), `doctor` (workspace-integrity checker, §5).
- **Wiring order (server):** config load → `crypto.Init` from `POSTA_ENCRYPTION_KEY` (`cmd/posta/server.go:108`) → Postgres via GORM + AutoMigrate + versioned upgrade steps → Redis → okapi route registration with OpenAPI/Scalar (`server.go:496-529`) → cron manager registration (`server.go:690-768`). Worker mirrors crypto init at `cmd/posta/worker.go:62` and registers asynq task handlers + `ExhaustedErrorHandler` (`worker.go` + `handler.go:529-565`).
- **HTTP layer:** okapi declarative handlers with typed request/response docs; consistent `{success, data}` envelope; RequestID middleware (`routes.go:655`); route groups per resource file (§2.5).
- **Service packages (33):** `auth`, `audit`, `automation`, `cache`, `clientinfo`, `crypto`, `domain`, `email`, `emailhistory`, `emailverify`, `eventbus`, `inbound`, `inbox`, `messages`, `messagescan`, `notification`, `passwordreset`, `plan`, `ratelimit`, `realtime`, `retry`, `security`, `seeder`, `session`, `settings`, `smtprelay`, `tracking`, `twofactor`, `updatecheck`, `verifier`, `webhook`, `workermon`, `workspace`, `workspaceprovision` (+ in-flight `provider_events`, §2.10). Notables: `security/` is a 16-file fail2ban-style engine (5,298 LOC incl. tests); `email/` holds the send pipeline, DKIM signer, renderer, verifier-adjacent provider classification.
- **Repositories:** `internal/storage/repositories/*` — one per aggregate (email, campaign, subscriber, segment + guarded SQL builder `segment_query.go`, tracking, suppression, webhook, automation journeys, smtp servers, signing keys…), plus `migration/upgrade/` versioned steps.

## 2.3 Frontend architecture

- **Stack:** Vue 3.5 + TypeScript + Vite + Pinia (`web/src`); no component-UI library, no i18n framework, no chart library — charts are hand-rolled SVG (`web/src/views/dashboard/VolumeChart.vue`, `Sparkline.vue`; `web/package.json` has no chart dep).
- **Router:** `web/src/router/index.ts` — ~90 route paths / 60+ named routes covering auth (login/register/oauth-callback/invitation-accept/verify-email/forgot+reset-password), dashboard, api-keys, smtp-relay, emails + inbound-emails, messages + message-filters + forms, templates (+ editor, builder, preview, languages, stylesheets), smtp-servers, domains, webhooks + webhook-deliveries, bounces, contacts, subscribers + subscriber-lists + segments, automations (+ builder/detail), unsubscribe-lists, campaigns, analytics, audit/logs views, settings, admin/* (incl. Jobs.vue, Events.vue, Workspaces, security/SecurityEvents).
- **Views by feature area** (view directory → primary API module):

  | Feature area | Views (`web/src/views/…`) | API client |
  |---|---|---|
  | Dashboard/analytics | `dashboard/` (VolumeChart, Sparkline), `analytics/` | `dashboard.ts`, `analytics.ts` |
  | Campaigns | `campaigns/` (Campaigns, CampaignDetail) | `campaigns.ts` |
  | Templates | `templates/` (Templates, TemplateEditor, EmailBuilder, TemplatePreview, detail/* incl. VersionList, LocalizationList, SendTestModal, PreviewModal) + `languages/`, `stylesheets/` | `templates.ts`, `languages.ts`, `stylesheets.ts` |
  | Audience | `subscribers/`, `subscriber-lists/` (incl. SegmentDrawer), `segments/`, `contacts/` | `subscribers.ts`, `subscriberLists.ts`, `segments.ts`, `contacts.ts` |
  | Automation | `automations/` (AutomationBuilder, AutomationDetail, NodePalette, ConfigPanel, ExecutionDrawer, StatCanvas) | `automations.ts` |
  | Delivery config | `domains/`, `smtp/` (servers), `smtp-relay/` | `domains.ts`, `servers.ts`/`smtp.ts`, `smtpRelay.ts` |
  | Sending/logs | `emails/` (Emails, EmailDetail, EmailPreview), `logs/` (MessageHistory, MessageDetail, IncomingWebhooks, OutgoingWebhooks, AuditLogs) | `emails.ts`, `logs.ts` |
  | Inbound & forms | `inbound/`, `messages/`, `forms/` | `inbound.ts`, `inbox.ts`, `messages.ts` |
  | Bounces/webhooks | `bounces/` (incl. Suppressions tab), `webhooks/` (WebhookDeliveries) | `bounces.ts`, `webhooks.ts` |
  | Consent | `unsubscribe-lists/` | `unsubscribeLists.ts` |
  | Account/admin | `apikeys/`, `audit/`, `invitations/`, `settings/`, `workspaces/`, `admin/` (Workspaces, WorkspaceDetail, Settings, Events, EventDetail, Jobs, Dashboard, Inventory, security/SecurityEvents) | `apikeys.ts`, `audit.ts`, `settings.ts`, `workspaces.ts`, `admin.ts`, `oauth.ts`, `sessions.ts`, `plans.ts`, `info.ts` |

  Note: `web/src/views/contact-lists/` exists but is an unrouted dead surface (§7).
- **API clients:** 35 typed modules in `web/src/api/` (admin.ts, analytics.ts, apikeys.ts, auth.ts, automations.ts, campaigns.ts, contacts.ts, domains.ts, emails.ts, inbound.ts, inbox.ts, logs.ts, messages.ts, oauth.ts, segments.ts, servers.ts, sessions.ts, settings.ts, smtp.ts, smtpRelay.ts, subscriberLists.ts, subscribers.ts, templates.ts, webhooks.ts, workspaces.ts, …) over a shared `client.ts`.
- **Specialized editors:** CodeMirror 6 HTML editor (`TemplateEditor.vue`), GrapesJS + grapesjs-preset-newsletter builder (`EmailBuilder.vue:41-49`) with device previews (`:60-66`), @vue-flow automation builder with 9-node palette and undo/redo (`AutomationBuilder.vue:47,375-377`).
- **Tests:** 2 spec files — `web/src/views/subscriber-lists/SubscriberListPreview.spec.ts`, `web/src/views/admin/security/SecurityEvents.spec.ts` (§4).

## 2.4 Database architecture

- **PostgreSQL** as the single durable store (compose `postgres:17`; no sqlite/mysql/mongo driver in `go.mod`). **GORM AutoMigrate** (`internal/storage/migration/migration.go`) + FK/index constraints (`migration/constraints.go`) + **6 versioned upgrade steps** in `internal/storage/migration/upgrade/` (personal-workspaces backfill, contact dedupe `steps_2026_06_04_dedupe_contacts.go`, api-key scopes, workspace types, email-sender decode) with a migrate smoke test (`migrate_smoke_test.go`).
- **~55 models** (`internal/models/`) grouped by domain:

  | Domain | Models (representative) |
  |---|---|
  | Tenancy | `workspace.go` (Workspace + WorkspaceMember, system-workspace flag), `workspace_sso.go`, `plan.go` |
  | Users & auth | `user.go`, `session.go` (JTI uniqueIndex), `api_key.go` (scopes, CIDR, expiry), OAuth identities |
  | Messaging | `email.go` (status machine, `ScheduledAt`, `TemplateData`, `ErrorMessage`, `SMTPHostname`), `email_event.go` (8 event types, 6 written), `template.go` + `template_version.go` + `template_localization.go` (BuilderJSON), `stylesheet.go`, language |
  | Audience | `subscriber.go` (status, timezone, language, CustomFields), `subscriber_list.go` (static/dynamic, FilterRules), `subscriber_list_unsubscribe.go`, `contact.go` (send ledger), `suppression.go` (4 kinds), `unsubscribe_list.go`, `bounce.go` (hard/soft/complaint) |
  | Campaigns | `campaign.go` (CAS state machine, SendRate, ABTestWinner — in-flight), `abtest.go` (variants), `campaign_message.go` (per-recipient ledger) |
  | Automation | `automation.go` (graph, trigger kinds, frequency), `automation_journey.go`, `automation_node_run.go`, `automation_trigger_log.go` |
  | Delivery ops | `smtp_server.go` (encrypted creds, AllowedEmails), `smtp_credential.go` (PasswordHash), `server.go` (shared, allowed_domains, SecurityMode), `domain.go` (verification tokens), `domain_signing_key.go` (rotation states) |
  | Webhooks & inbound | webhook endpoint/delivery/attempt, `form.go` (nonce, thresholds, NotifyEmails), inbound messages |
  | Security | ban/allowlist/detector state, `security_events`; `event.go` (audit events) |

- **jsonb-heavy:** `emails.template_data` (`models/email.go:55-58`), subscriber `CustomFields` (`subscriber.go:37-70`), `SegmentCondition` trees on `segments` (`segment.go:53-102` — `{logic, groups[]{logic, rules}}`), automation graph definitions (`automation.go`), template `BuilderJSON` (GrapesJS project, `template_localization.go:31`), event/audit metadata and `security_events` evidence payloads (`models/security.go`, `models/event.go:33-49`).
- Versioned steps + doctor (`cmd/posta/doctor.go:52-149`) guard tenancy invariants (NULL-workspace unreachable rows, system workspace, plan backfill).

## 2.5 API architecture

- **~340 route definitions** under `/api/v1` across ~13 route files (audit-H verified count: 158 GET / 126 POST / 33 PUT / 41 DELETE / 1 PATCH / 1 OPTIONS). Per-file breakdown (route-definition structs; working-tree grep, committed non-test total = 340):

  | Route file | Definitions | Scope |
  |---|---|---|
  | `workspace_resource_routes.go` | 128 | Templates/versions/localizations, stylesheets, languages, domains + signing keys, subscribers + import, subscriber-lists, segments, campaigns, suppressions, unsubscribe-lists, contacts, smtp-servers, webhook-deliveries, logs/messages, analytics, GDPR |
  | `admin_routes.go` | 67 | Platform admin: users, workspaces, plans, shared servers, OAuth providers, events + SSE, bans + security events, crons, queues (a6f1681), admin analytics |
  | `auth_routes.go` | 26 | Public auth + health/info + API-key-scoped send/read/webhook groups (emails send/batch/verify/preview, suppressions, bounces read, relay credentials, unsubscribe opt-outs) |
  | `workspace_routes.go` | 26 | Workspace lifecycle, members, invitations, settings, plan, sso, data export/import, gdpr, audit-log |
  | `message_routes.go` | 26 | Website-form inbox: forms, messages, spam triage, filters |
  | `user_routes.go` | 25 | Profile, password, 2FA setup/verify/disable, sessions |
  | `oauth_routes.go` | 13 | OAuth login/callback + admin provider CRUD + workspace SSO |
  | `automation_routes.go` | 12 | Automation CRUD/lifecycle/executions/stats/event-ingest |
  | `tracking_routes.go` | 10 | Pixel/click/web-view/unsubscribe pages + bounce webhook |
  | `inbound_routes.go` | 9 | Inbound ingest, attachments, SSE stream |

  Scope-map notes not captured above: the API-key `send` group also carries `/emails/send-template` (`auth_routes.go:225`), `/emails/verify` (`:206`), and `POST /subscriber-lists/subscribe` (`:323-336` — not public); the `webhooks` group holds suppression CRUD and the bounce-ingest webhook (`tracking_routes.go:93`, api-key auth); per-message retry/status live under `workspace_resource_routes.go:103,116`; `webhook_docs.go` renders per-event payload docs from the registry; **admin routes are session-JWT only — API keys structurally cannot reach `/admin/*`** (`middlewares/middleware.go:157-158`).
- **OpenAPI/Scalar:** okapi-generated OpenAPI + Scalar UI at `/docs`, gated by `POSTA_OPENAPI_DOCS` (default true, `config.go:323`, `config.go:496-529`); surfaced via `GET /info` (`info_handler.go:31-40`).
- **API-key scopes:** `send/read/webhooks/write/admin/*` (`models/api_key.go:32-48`; empty set = send-only legacy) enforced structurally per route group (`RequireScope`, `middlewares/middleware.go:346`; scope map `:312-324` — GET→read, mutations→write, tenant-admin paths→admin).
- **CORS & proxies:** `POSTA_CORS_ORIGINS` allowlist (`config.go:334,489-490`); unset `TrustedProxies` triggers a startup warning that X-Forwarded-For is spoofable (`config/security.go:157-160`).

## 2.6 SMTP & delivery architecture

Outbound path end-to-end (committed):

1. **API:** `POST /emails/send` (`internal/routes/auth_routes.go:194`) → `internal/handlers/email_handler.go:130` → `internal/services/email/service.go:645` `Send` (address validation :647, maintenance gate :654, plan rate limit :659, attachment limits :671, domain check :688, suppression filter :705, persist row :821, tracking rewrite :851, enqueue :887; `send_at` branch :869-881 → `EnqueueEmailSendAt`).
2. **Queue → worker:** `internal/worker/producer.go` (deterministic TaskID `email:send:<id>` + `ErrTaskIDConflict` resolution, `producer.go:47-101`, committed a6f1681) → `internal/worker/handler.go:151` `ProcessTask` (campaign pause/cancel re-check :165-185, server selection :197-250, suppression re-check :283-302, header stamping :315-332, `sender.Send` :334, events + webhooks :360-382).
3. **Delivery:** `internal/services/email/smtp_sender.go:295` `Send` — `net/smtp` relay client (SSL/STARTTLS/plain `dialSMTP` :210-236), RFC 5322 builder (:528; quoted-printable :412-423, RFC 2047 :437-485), **DKIM signing hook :308-324** (committed a6f1681), **dial timeouts :94-134** (committed a6f1681: 15 s default dial, `POSTA_SMTP_DIAL_TIMEOUT_SECS`, session budget = dial × 4, fresh budget before DATA; timeout→transient classification :64-67 so timeouts never suppress).
4. **Server selection:** first enabled workspace/user server (`internal/storage/repositories/smtp_repo.go:119-125`), else admin shared server matched by verified sender domain with strict-mode ownership check (`worker/handler.go:227-243`).

**Retry layers:** (1) asynq `MaxRetry` + `ExhaustedErrorHandler` → permanently failed + `email.failed` webhook (`producer.go:60`, `worker/handler.go:529-565`); (2) 5-min DB-retry worker honoring per-server `max_retries` (`internal/services/retry/worker.go:110-155`); (3) per-message manual retry (`workspace_resource_routes.go:116`); (4) campaign-restart watchdog cron (`cron/jobs/campaign_restart.go:57`); (5) `queue_reconcile` cron re-enqueueing lost `queued` emails (a6f1681, `cron/jobs/queue_reconcile.go:64-146`). 5xx RCPT rejection skips retry and auto-suppresses (`handler.go:343-347` → `handlePermanentRejection` :442-510).

**DKIM signing (committed a6f1681):** go-msgauth relaxed/relaxed SHA-256, RSA-2048 or Ed25519, per-envelope-domain key resolution, two-phase `retiring→active` rotation + `verify-dns` publication check (`internal/services/email/dkim_signer.go:80-136`; key lifecycle routes `workspace_resource_routes.go:813-872`; encrypted key at rest, `models/domain_signing_key.go:73-79`).

**Relay (submission):** `internal/services/smtprelay/backend.go:68-139` — SASL PLAIN against `smtp_credentials` with per-credential IP allowlist + per-IP rate limit + security-engine session guard; default-off (`POSTA_SMTP_RELAY_ENABLED`), relays into the same email pipeline.

**Inbound receiver:** `internal/services/inbound/smtp_server.go:61-230` (go-smtp backend; RCPT gated to verified recipient domains) + webhook ingest `POST /webhooks/inbound` (`inbound_routes.go:43`, shared secret + dedup `inbound/service.go:176,184`); 317-line MIME parser (`inbound/parser.go`); attachments to blob + signed URLs; SSE stream `/inbound-stream` (`inbound_routes.go:125`). Default-off.

**Bounce ingestion (3 sources):** sync SMTP 5xx at RCPT (auto-suppress default-on), API-key webhook `POST /webhooks/bounce` (`tracking_routes.go:93`; marks subscriber/message only — suppression/complaint semantics live in the authenticated `POST /bounces`, audit-G correction 4), authenticated `POST /bounces` (`bounce_handler.go:58-116`).

## 2.7 Queue & worker architecture

- **asynq + Redis** queues with weights: `transactional=6 / bulk=3 / low=1` (`cmd/posta/worker.go:140-143`, `server.go:581-584`) — weighted fair-share, not strict priority (registry INF-011).
- **TaskID dedup:** deterministic `email:send:<id>` / `campaign:start:<id>` / message TaskIDs with conflict resolution (live task = dedupe success; archived/dead-lettered = delete + re-enqueue) — `internal/queues/queues.go:44-52`, `producer.go:54-105` (committed a6f1681).
- **Campaign batching:** batches of 100 with self re-enqueue (`worker/campaign_processor.go:91,254`; completes at :276-285); per-campaign `SendRate` sets inter-batch pacing (`campaign_processor.go:419-426`).
- **Journeys:** `FOR UPDATE ... SKIP LOCKED` claims + CAS cursors make the automation engine multi-replica safe (`internal/storage/repositories/automation_journey_repo.go:87,107`; `services/automation/engine.go:149-153`).
- **Embedded vs standalone:** `POSTA_EMBEDDED_WORKER` (default false, `config.go:102,348`; `server.go:182-183,520-523`) or standalone `worker` with its own health server serving `/healthz /readyz /metrics` (`cmd/posta/worker.go:47+,264-288`); compose runs a separate worker service (`compose.yml:31-37`, validates the same JWT secret :37).
- **Worker observability:** Redis heartbeats 15 s/45 s TTL with version+commit (`services/workermon/heartbeat.go:34-54`), Inspector server visibility + connected/disconnected events (`workermon/monitor.go:91-140`), SSE `/admin/metrics/stream`, Prometheus gauge `posta_queue_archived_tasks` (`metrics/metrics.go:180-183`).

## 2.8 Cron jobs (10)

Registered in `cmd/posta/server.go:690-768` via the robfig/cron manager; listed at `GET /admin/crons` (`cron_handler.go:36`; no last-run/duration columns).

| # | Job | Schedule | Purpose | Evidence |
|---|---|---|---|---|
| 1 | `campaign_scheduler` | `* * * * *` (every minute) | Fires scheduled campaigns: atomic claim scheduled→sending, rollback on enqueue failure | `cron/jobs/campaign_scheduler.go:60-117` — committed 347ebb4; 616-line test |
| 2 | `queue_reconcile` | `*/5 * * * *` | Re-enqueues emails stuck `queued` whose asynq task vanished (CAS claim; campaign rows skipped) | `cron/jobs/queue_reconcile.go:64-146` — committed a6f1681 |
| 3 | `campaign_restart` | `*/5 * * * *` | Re-enqueues batches for campaigns stuck `sending` >10 min with pending messages | `cron/jobs/campaign_restart.go:57-82` |
| 4 | `retention` | `0 3 * * *` (03:00 UTC) | Purges 10+ surfaces (emails, bodies/attachment blobs, audit events, webhook deliveries+attempts, tracking, inbound, messages incl. spam window, per-form, notifications, email events) | `cron/jobs/retention.go:195+` |
| 5 | `update_check` | `37 4 * * *` | Opt-out-able new-version check | `cron/jobs/update_check.go:44` |
| 6 | `daily_report` | `0 7 * * *` | Per-workspace daily report | `cron/jobs/daily_report.go:59` |
| 7 | `account_cleanup` | `0 2 * * *` | Deletes accounts past 7-day grace | `cron/jobs/account_cleanup.go:41` |
| 8 | `api_key_expiry` | `0 8 * * *` | Notifies owners 7 days before key expiry | `cron/jobs/api_key_expiry.go:46` |
| 9 | `bounce_alert` | `0 9 * * *` | Per-workspace bounce-rate threshold alerts | `cron/jobs/bounce_alert.go:61` |
| 10 | `message_digest` | `5 * * * *` (hourly) | Inbound message digests | `cron/jobs/message_digest.go:66` |

**No leader lock:** every server replica runs all 10 schedules (OBS-019); alert-type jobs (`bounce_alert`, `daily_report`) risk double-send across replicas.

## 2.9 Authentication & authorization

- **Sessions:** JWT + `session` rows (JTI uniqueIndex, IP, UA) + Redis JTI blacklist checked per request (`middlewares/middleware.go:132-139`); self-service list/revoke (`session_handler.go:59-143`); admin force-revoke (`admin_routes.go:148-159`).
- **2FA (TOTP):** enrollment/verify/disable (`services/twofactor/totp.go:27-42`, `user_routes.go:91-118`), login challenge (`user_handler.go:486-496`), admin reset (`admin_handler.go:601-606`). **Dead finding:** the `two_factor_required` setting is seeded (`services/seeder/settings.go:52`) with getter (`settings/provider.go:124`) but **zero consumers — login never enforces it**; no recovery codes. SEC-005 PARTIAL.
- **OAuth/SSO:** Google special-case + generic OIDC with issuer auto-discovery and account linking (`services/auth/oauth.go:66-126`, admin provider CRUD `oauth_routes.go:96-162`); **workspace SSO** = per-workspace OIDC enforcement with `EnforceSSO`/`AllowedDomains`/AutoProvision (`models/workspace_sso.go:25-37`, `oauth_routes.go:174+`). No SAML, no SCIM.
- **API keys:** `psk_` prefix, 32 random bytes, SHA-256 hashed at rest, 12-char prefix lookup (`services/auth/api_key.go`); scopes + revocation + expiry enforced in `ValidateKey` (`api_key.go:120-126`) + expiry notification cron; per-key workspace binding via `X-Posta-Workspace-Id` (`middlewares/workspace.go:33`); per-key CIDR allowlists (`middleware.go:232-279`); also on relay credentials (`models/smtp_credential.go:35`).
- **RBAC:** 4 roles owner/admin/editor/viewer (`models/workspace.go:24-31`); `RequireWorkspaceRole` (`middlewares/workspace.go:149-176`); structural scope enforcement inside workspace resolution (`middleware.go:312-341`); admin is a separate credential class (`JWTAdminAuth`, `middleware.go:159-161`). Integration tests: `internal/routes/workspace_rbac_test.go`, `middlewares/workspace_test.go`.
- **Automation editor-gate finding:** commit `c0408dc` gated automation UI only (3 Vue files); `internal/routes/automation_routes.go:35` carries `auth + workspace` middleware only — **no server-side editor role gate**; a viewer JWT can still POST `/automations` server-side (SEC-002 note; synthesis §6.3: event ingest is authenticated and tenant-scoped but not editor-gated).

## 2.10 In-flight work, pending commit (NOT counted as IMPLEMENTED)

Present in the working tree after `a6f1681`; excluded from all status verdicts per the registry rule:

- **Provider-event (ESP webhook) ingestion** — untracked `internal/services/provider_events/` (classifier already records `EmailEventDelivered` at `provider_events/service.go:213`) + `internal/handlers/provider_event_handler.go` + test; would produce the first true `delivered` signal (ANA-003/INF gap).
- **Campaign A/B winner logic** — modified `internal/models/campaign.go:109-118` (`ABTestWinner`, `ABTestWinnerSent`, `ABTestWinnerStats`), winner hold/selection in `internal/worker/campaign_processor.go` (`isHeldForWinner`), untracked `internal/storage/repositories/campaign_ab_repo.go` + `internal/worker/campaign_winner_test.go`, winner UI in `web/src/views/campaigns/CampaignDetail.vue`. Registry CAM-008 PARTIAL stands for HEAD (zero winner writers at HEAD, verified via `git show HEAD`).
- **Timezone-aware sends** — untracked `internal/worker/campaign_timezone_test.go` (+ modified processor); HEAD still logs intent only (`campaign_processor.go:230-235` stub). CAM-016 PARTIAL stands.
- **Webhook replay/redrive** — untracked `internal/handlers/webhook_replay_handler.go`, `internal/storage/repositories/webhook_replay_repo.go`, `internal/services/webhook/dispatcher_replay_test.go`; HEAD has inspectable but not re-driveable dead-lettered deliveries. API-013 MISSING stands.
- **Supporting edits:** modified `campaign_handler.go`, `auth_routes.go`, `tracking_routes.go`, `workspace_resource_routes.go`, `webhook/dispatcher.go`, `web/src/api/{campaigns,types,webhooks}.ts`, `web/src/views/{campaigns,webhooks}/*` (campaign UI + webhook replay additions); **minor migration edits** `internal/storage/migration/{constraints.go,migration.go}` modified-uncommitted (line numbers in those files may drift).

---

# 3. Feature-Status Tables (Categories A–J)

Evidence format: `file:line` within the repo root, endpoint, model, or test. Commit notes: `committed 347ebb4` / `committed a6f1681` where the audit files had marked the item WIP.

## 3.A — Email infrastructure (INF, 36 entries) — 21 IMP / 9 PARTIAL / 5 MISSING / 1 N/A

| ID | Feature | Status | Evidence | Gap note |
|---|---|---|---|---|
| INF-001 | External SMTP sending | IMPLEMENTED | Relay-only client `internal/services/email/smtp_sender.go:295` (SSL/STARTTLS/plain `dialSMTP` :210-236); `smtp_sender_test.go` | Relay-only by design: RF cannot be its own MTA (no direct-to-MX) |
| INF-002 | Built-in SMTP server (submission) | IMPLEMENTED | `internal/services/smtprelay/backend.go:68-139` (SASL PLAIN, per-cred IP allowlist :124, per-IP rate limit :70-79, security guard :80-83); default-off `config.go:399` | No STARTTLS enforcement; AUTH PLAIN only |
| INF-003 | Full mail server (IMAP/POP/webmail) | NOT_APPLICABLE | — | BillionMail-only; out of RF product scope |
| INF-004 | Transactional send API | IMPLEMENTED | `POST /emails/send` `auth_routes.go:194` → `email_handler.go:130` → `email/service.go:645` → `worker/handler.go:151` → `models.Email`; tests `smtp_sender_test.go`, `worker/handler_test.go`, `headers_test.go`; also `/emails/send-template` (`auth_routes.go:225`) | — |
| INF-005 | Batch send API (per-recipient variables) | IMPLEMENTED | `POST /emails/batch` (`auth_routes.go:239`) → `email_handler.go:240-281`; per-item results; `dry_run` validation | — |
| INF-006 | Campaign/newsletter bulk sending | IMPLEMENTED | Batch=100 self re-enqueue `worker/campaign_processor.go:91,254`; tx batch `service.go:1052`; bulk queue w=3 | Bulk = many `email:send` tasks; no cross-batch ceiling |
| INF-007 | Outbound attachments | IMPLEMENTED | Blob-backed, 10-25MB plan limits (`email/service.go:671`); MIME build `smtp_sender.go:528+`; tests `attachment_pipeline_test.go`, `attachment_mime_test.go` | — |
| INF-008 | Custom headers control | IMPLEMENTED | `x-posta-`/`dkim-`/`arc-` prefixes blocked (`service.go:1478`); setting-gated `CustomHeadersEnabled` (:773); test `headers_test.go` | Allowlist mode absent (free-form otherwise) |
| INF-009 | Background queue pipeline | IMPLEMENTED | asynq+Redis `internal/queues/queues.go:31-42`; producer `worker/producer.go` | — |
| INF-010 | Queue administration & dead-letter mgmt | IMPLEMENTED (committed a6f1681) | `/admin/queues`, `/admin/queues/{q}/tasks` (incl. archived=dead-letter), cancel/run/archive (`admin_queue_handler.go:157,221,317-339`, `admin_routes.go:641-705`); `queue_reconcile.go:64-146`; UI `web/src/views/admin/Jobs.vue`; test `admin_queue_test.go` (599 lines) | Closes seed R-03; per-queue pause/throttle not exposed |
| INF-011 | Priority/weighted queues | PARTIAL | Weights tx=6/bulk=3/low=1 (`cmd/posta/worker.go:140-143`) | Weighted = probabilistic, not strict ordering; no per-tenant priority |
| INF-012 | Send rate limiting & quotas | IMPLEMENTED | Redis fixed-window hourly/daily (`services/ratelimit/quota.go:44`); plan-overridable (`plan/service.go:213`); login + relay + verifier limiters | No per-API-key limits |
| INF-013 | Retry policies | IMPLEMENTED (committed a6f1681) | 3 layers: asynq MaxRetry + ExhaustedErrorHandler (`worker/handler.go:529-565`), DB retry worker (`retry/worker.go:110-155`), manual retry (`workspace_resource_routes.go:116`); timeout→transient (`smtp_sender.go:64-67`, test `smtp_sender_timeout_test.go:64`); 5xx RCPT skips retry + suppresses (`handler.go:343-347`) | No per-server backoff tuning |
| INF-014 | Scheduled (future) sends | IMPLEMENTED (committed a6f1681) | tx `send_at` → `status=scheduled` (`service.go:869-881`), `EnqueueEmailSendAt` (`producer.go:108-132`), `models.Email.ScheduledAt` (`email.go:69`); campaign scheduler committed 347ebb4 (`campaign_scheduler.go:60-117`) | Scheduled tx rows cannot be cancelled; not covered by queue_reconcile (scans `queued` only) |
| INF-015 | Gradual/throttled campaign sending | IMPLEMENTED | Per-campaign `SendRate` → inter-batch delay (`campaign_processor.go:419-426`) | Batch-granular pacing, not token bucket |
| INF-016 | Circuit breaker / auto-pause | MISSING | Alerts only (`bounce_alert.go`) | No auto-pause on failure/bounce thresholds |
| INF-017 | Message & attachment retention/purge | IMPLEMENTED | `cron/jobs/retention.go:35-138` (10+ surfaces incl. blob scrubbing); test `retention_test.go` | No per-workspace legal hold |
| INF-018 | Suppression lists (send-time enforcement) | IMPLEMENTED | Kinds hard/bounce/complaint/list_unsubscribe/manual (`models/suppression.go:29-50`); enforced in worker (`worker/handler.go:283-297`) | Suppression UI cannot filter by kind; no CSV import |
| INF-019 | Bounce ingestion & auto-suppression | IMPLEMENTED | 3 sources (sync 5xx `worker/handler.go:442-510`, webhook, authenticated API) | `/webhooks/bounce` marks subscriber only — suppression/complaint only via authenticated API or sync 5xx (audit-G correction 4) |
| INF-020 | Bounce mailbox / VERP ingestion | MISSING | — | No VERP/mailbox path under relay-only design |
| INF-021 | Complaint/FBL ingestion | PARTIAL | Authenticated `POST /bounces` type=complaint → suppression + `email.complained` (`bounce_handler.go:89-138`) | No FBL/ARF source; webhook rejects complaint type (`bounce_webhook_handler.go:89`) |
| INF-022 | Multiple ESP integrations (native APIs) | PARTIAL | Generic SMTP only; `go.mod` has s3 only (audit-H) | No SES/Postmark/Mailgun/SendGrid clients |
| INF-023 | ESP/provider failover | MISSING | Failure re-queues against same server (`worker/handler.go:334-356`) | No alternate-server rotation/circuit breaker |
| INF-024 | Per-message/per-campaign server selection | PARTIAL | Only first-enabled workspace server picked (`smtp_repo.go:119-125`) | Multiple servers storable, not selectable per message |
| INF-025 | IP pools / multiple sending IPs | MISSING | No IP concept in code (grep clean) | Structurally absent under relay-only design |
| INF-026 | Development/sandbox hold mode | IMPLEMENTED | `POSTA_DEV_MODE` stores as `sent` without delivery (`service.go:855`); dev-only listing (`email_handler.go:381,392`) | — |
| INF-027 | Dry-run send validation | IMPLEMENTED | `dry_run=true` on send/batch/template endpoints → `DryRunResponse` (`email_handler.go:44`); render-only `POST /emails/preview` (`auth_routes.go:254`) | — |
| INF-028 | Recipient address verification | IMPLEMENTED | `POST /emails/verify` (`auth_routes.go:206`) → `verifier/verifier.go:116` (syntax/disposable/role/MX, Redis cache) | Advisory only — not enforced at send |
| INF-029 | Inbound email receiving & parsing | IMPLEMENTED | Inbound SMTP server (`inbound/smtp_server.go:61-230`) + webhook ingest (`inbound_routes.go:43`) + MIME parser (`inbound/parser.go`) + SSE (`inbound_routes.go:125`); tests `parser_test.go` | Default-off |
| INF-030 | Inbound routing rules | PARTIAL | Recipient-domain-verified gating + webhook fan-out (`inbound_routes.go`) | No per-domain route table (HTTP/SMTP/forward targets) |
| INF-031 | Idempotent sends | PARTIAL (committed a6f1681) | Internal deterministic TaskID dedup (`producer.go:47-101`) | No client `Idempotency-Key` header (audit-H gap) |
| INF-032 | Mail/message tagging | PARTIAL | `ExternalID` caller correlation (`service.go:343`, `models/email.go:43`) | No tag model |
| INF-033 | SMTP connection diagnostics | PARTIAL | `POST /smtp-servers/{id}/test` + `validated_at` (`smtp_handler.go:300-321`) | No EHLO/TLS transcript |
| INF-034 | Greylisting & transient-failure handling | PARTIAL (committed a6f1681) | Timeout→transient classification (`smtp_sender.go:64-67`) | No explicit greylist detection |
| INF-035 | Maintenance-mode send gate | IMPLEMENTED | Settings-driven gate in `Send` (`service.go:654-656`) | — |
| INF-036 | Multi-channel messengers (SMS/push) | MISSING | — | Email-only product |

## 3.B — Domains & DNS (DOM, 13 entries) — 4 IMP / 4 PARTIAL / 5 MISSING

| ID | Feature | Status | Evidence | Gap note |
|---|---|---|---|---|
| DOM-001 | Custom sending domains (multi-domain CRUD) | IMPLEMENTED | Domain CRUD + per-workspace scoping (`workspace_resource_routes.go:750-795`, `models/domain.go:24-37`); admin conflict detection (`admin_domain_handler.go:91,123,229`) | No domain grouping/pooling |
| DOM-002 | Domain ownership verification | IMPLEMENTED | TXT `posta-verification=<token>` (`services/domain/verifier.go:88-107`); `POST /domains/{id}/verify` (`workspace_resource_routes.go:797-808`) | No DKIM/MX alternative; no re-verification |
| DOM-003 | Managed DKIM signing | IMPLEMENTED (committed a6f1681) | go-msgauth relaxed/relaxed SHA-256, RSA/Ed25519, per-envelope-domain (`email/dkim_signer.go:80-136`; hook `smtp_sender.go:308-324`); tests `dkim_signer_test.go` (475 lines) | Signing failure fails the send (misconfiguration surfaces, not unmarked mail) |
| DOM-004 | DKIM key rotation & DNS publication verification | IMPLEMENTED (committed a6f1681) | Two-phase `retiring→active` rotation + per-key `verify-dns` (`workspace_resource_routes.go:813-872`); encrypted keys (`models/domain_signing_key.go:73-79`) | verify-dns is manual; no scheduled re-check |
| DOM-005 | SPF checking & guidance | PARTIAL | Presence check only (`verifier.go:97-101`) | No include validation, no 10-DNS-lookup check |
| DOM-006 | DMARC checking & guidance | PARTIAL | Presence check only (`verifier.go:126-136`) | No policy parsing; no rua ingestion (absent industry-wide) |
| DOM-007 | Automated DNS provisioning | MISSING | Copy-paste `RequiredRecords` only (`verifier.go:58-81`) | No provider-API writes, no built-in DNS |
| DOM-008 | DNS re-verification & drift monitoring | MISSING | Verification is on-demand only (cron list §2.8 has no DNS job) | Records can silently rot; DKIM verify-dns manual |
| DOM-009 | Custom tracking domain | MISSING | Tracking runs on the instance domain only | No per-workspace branded tracking host |
| DOM-010 | TLS certificate management (ACME/SNI) | MISSING | Server-level inbound TLS files only (`config.go:163-165`) | No ACME/SNI |
| DOM-011 | Domain reputation/blacklist monitoring | PARTIAL | Proxies only: bounce-rate alert cron (`bounce_alert.go`) + provider-classified analytics (`email/provider.go:148-167`) | No blacklist scans/Postmaster integration |
| DOM-012 | Domain-sending policy gates | PARTIAL | Strict-mode opt-in + shared-server `allowed_domains` (`worker/handler.go:227-243`); campaigns only warn on unverified sender (`campaign_handler.go:566` `warnSenderDeliverable`) | Binary verification, not policy management |
| DOM-013 | rDNS/PTR guidance | MISSING | Zero code/UI (grep clean) | Relevant only if direct-to-MX is ever added |

## 3.C — Contacts & audience (CON, 20 entries) — 9 IMP / 5 PARTIAL / 5 MISSING / 1 N/A

| ID | Feature | Status | Evidence | Gap note |
|---|---|---|---|---|
| CON-001 | Subscriber/contact records | IMPLEMENTED | Subscribers CRUD (`workspace_resource_routes.go:1017-1062` → `subscriber_handler.go:130-244`); read-only send-ledger Contacts (`contact_handler.go:48-108`); dedupe migration `migration/upgrade/steps_2026_06_04_dedupe_contacts.go` | Dual surface (contacts ledger vs subscribers, §7) |
| CON-002 | Mailing lists | IMPLEMENTED | Static/dynamic `SubscriberList` (`models/subscriber_list.go:66-87`) + members routes (`workspace_resource_routes.go:1087-1167`) | Dead ContactLists orphan surface (§7) |
| CON-003 | Subscription status management | IMPLEMENTED | 4 statuses + `UnsubscribedAt` (`subscriber_handler.go:199-207`); list-scoped opt-out table (`subscriber_list.go:90-99`, `subscriber_list_repo.go:146-160`) | API-set status doesn't auto-create suppression rows |
| CON-004 | Double opt-in | MISSING | grep clean (`double.opt\|confirm.token\|optin`); subscribe is immediate (`subscriber_list_handler.go:373-460`) | EU-marketing compliance blocker |
| CON-005 | Custom attributes/fields | IMPLEMENTED | JSONB `CustomFields` + segment custom-key rules (`segment_query.go:136-152,563`) | No typed schema registry |
| CON-006 | Subscriber tags | MISSING | No Tag model anywhere | Competitor-parity gap |
| CON-007 | Dynamic segmentation (condition builder) | IMPLEMENTED | Guarded SQL builder, 14 operators, injection tests (`segment_query.go:408`, tests `segment_query_test.go:447,609`); SegmentDrawer UI w/ debounced preview | Rules can't reference list membership |
| CON-008 | Engagement-based segmentation | IMPLEMENTED | Engagement subqueries over campaign_messages with legacy-parity tests (`segment_query.go:109-131`, `segment_query_test.go:660,697`) | — |
| CON-009 | CSV import (column mapping) | IMPLEMENTED | CSV+JSON endpoints, `custom_fields.*` mapping, IANA-tz validation (`subscriber_handler.go:246-398`) | No streaming (memory profile NV for 100k+ rows) |
| CON-010 | CSV export | MISSING | Only audit-log CSV + GDPR JSON bundle exist (`logs_handler.go:853`, `data_export.go`) | Asymmetric portability vs import |
| CON-011 | Bulk contact operations | PARTIAL | Import-only bulk; single-item delete (`subscriber_handler.go:232`) | GDPR purge is whole-workspace (`workspace_data_handler.go:271`) |
| CON-012 | Contact upsert/sync API | PARTIAL | Import dedupe + CRUD exist | No explicit upsert endpoint |
| CON-013 | Suppression management (UI/API) | IMPLEMENTED | Suppression CRUD + Bounces-view tab (`suppression_handler.go:56-115`) | No CSV import, no kind filter |
| CON-014 | Unsubscribe management (RFC 8058, hosted pages) | IMPLEMENTED | Campaign + tx one-click, HMAC-separated tokens, hosted pages (`tracking_handler.go:211-424`, `tracking_pages.go`) | — |
| CON-015 | Unsubscribe reasons | PARTIAL | Reason column exists (`subscriber_list.go:94`) | No analytics UI |
| CON-016 | Preference center | MISSING | Forward-reference comment only (`suppression.go:26`) | Hosted unsubscribe page is the foundation |
| CON-017 | Signup forms (hosted/embedded) | MISSING | RF "forms" = website-form→inbox (`models/form.go`), not audience capture | Do not market as audience capture |
| CON-018 | Form bot protection (captcha/honeypot) | NOT_APPLICABLE | No signup forms exist to protect | RF nonce/time-trap applies to the inbound website-form inbox |
| CON-019 | Contact activity history | PARTIAL | Events live in Logs/messages | No per-subscriber timeline in detail view |
| CON-020 | Import cleaning & validation | PARTIAL | Dedupe + skip counts on import (`subscriber_repo.go:106`) | No cleaning heuristics (role/spam-trap detection) |

## 3.D — Campaigns (CAM, 18 entries) — 8 IMP / 5 PARTIAL / 5 MISSING

| ID | Feature | Status | Evidence | Gap note |
|---|---|---|---|---|
| CAM-001 | Campaign creation & drafts | IMPLEMENTED | Draft-only-send CAS state machine (`models/campaign.go:32-39`, `campaign_handler.go:345-347`) | Content from templates by design (no inline editor) |
| CAM-002 | Campaign scheduling | IMPLEMENTED (committed 347ebb4) | Every-minute sweep, atomic claim + rollback, 616-line test (`cron/jobs/campaign_scheduler.go:60-117`, `campaign_scheduler_test.go:497-590`); UI schedule field (`Campaigns.vue:366-370`) | No reschedule-endpoint semantics beyond draft update |
| CAM-003 | Recurring campaigns | MISSING | grep clean | Absent in all 12 audited platforms |
| CAM-004 | Campaign duplication | PARTIAL | Backend `POST /campaigns/{id}/duplicate` deep-copies variants/template data/audience into a fresh draft (`workspace_resource_routes.go:1389-1394`, `campaign_handler.go:466-500`) | Zero UI and zero API-client call sites (`web/src/api/campaigns.ts`, `web/src/views/campaigns/` grep clean) — backend-only |
| CAM-005 | Reusable campaign templates | IMPLEMENTED | Templates pin template+version with per-language resolution (`campaign_processor.go:447`) | — |
| CAM-006 | Template content slots / campaign overrides | MISSING | Template object is atomic | No block-level override |
| CAM-007 | Campaign preview & test send | PARTIAL | Template send-test exists (`template_handler.go:273`) | Ignores campaign data; no campaign-scoped preview endpoint/UI — top usability gap |
| CAM-008 | A/B testing | PARTIAL | Subject-split API + per-variant analytics (`campaign_processor.go:194-221`, `tracking_repo.go:122+`) | No winner logic at HEAD (zero `ABTestWinner` writers — winner impl is in-flight, §2.10); variant `TemplateID` dead; no UI |
| CAM-009 | Audience targeting (list/segment) | IMPLEMENTED | XOR enforced + segment branch tests (`campaign_handler.go:533-570`, `campaign_processor_segment_test.go:646-760`) | Segments can't honor per-list unsubscribes (documented) |
| CAM-010 | Pause/resume/cancel lifecycle | IMPLEMENTED | Atomic CAS transitions, resume re-enqueue with rollback, cancel skips pending (`campaign_handler.go:391-464`) | — |
| CAM-011 | Campaign reports | IMPLEMENTED | Single-aggregation analytics + per-variant breakdown + hourly series (`tracking_repo.go:122`); snapshot survives delete (`campaign.go:95-97`) | No delivered-vs-sent distinction; no report export |
| CAM-012 | Public newsletter archive | MISSING | Web-view tokens are per-recipient + expiring | No hosted archive |
| CAM-013 | Multiple newsletters/publications | MISSING | Flat `campaigns` entity | No publication grouping |
| CAM-014 | Web view of message | IMPLEMENTED | `{{ posta_web_view_url }}` + HMAC 90-day token + CSP (`tracking_handler.go:345`) | — |
| CAM-015 | UTM auto-tagging | MISSING | No UTM rewriting in tracking pipeline | — |
| CAM-016 | Timezone-aware sending | PARTIAL | Stub: `SendAtLocalTime` field exists, logs intent, sends one batch (`campaign_processor.go:230-235`) | Absent industry-wide; timezone impl in-flight (§2.10) |
| CAM-017 | Stuck-campaign watchdog recovery | IMPLEMENTED | `campaign_restart` */5 cron for `sending`>10min (`campaign_restart.go:57`) | No dedicated test file for the job itself |
| CAM-018 | Failed-recipient retry | PARTIAL | Per-message manual retry API only (`workspace_resource_routes.go:116`) | No campaign-level retryFailed |

## 3.E — Templates & editors (TPL, 22 entries) — 12 IMP / 2 PARTIAL / 6 MISSING / 2 NEEDS_VERIFICATION

| ID | Feature | Status | Evidence | Gap note |
|---|---|---|---|---|
| TPL-001 | Raw HTML editor | IMPLEMENTED | CodeMirror 6 editor + auto-escaping html/template (`web/src/views/templates/TemplateEditor.vue`; renderer `email/template_renderer.go:181-213`) | — |
| TPL-002 | WYSIWYG / drag-and-drop builder | PARTIAL | Stock GrapesJS newsletter preset only (`EmailBuilder.vue:41-49`; BuilderJSON persisted `template_localization.go:31`) | No product block library or saved sections |
| TPL-003 | Markdown authoring | MISSING | grep clean | — |
| TPL-004 | Plain-text part & auto-generation | IMPLEMENTED | `email/html2text.go` applied in tx (`service.go:768`) + campaign (`worker/handler.go:257`) paths | No Markdown→HTML step |
| TPL-005 | Template variables / merge fields | IMPLEMENTED | Data map + reserved `{{ posta_* }}` namespace with sentinel substitution (`services/email/system_vars.go:28-118`) | No variable-insertion UI helper |
| TPL-006 | Advanced templating engines | PARTIAL | Go templates + Mustache-style normalization layer (`template_renderer.go:85-149`) | No Liquid filters/pipeline |
| TPL-007 | MJML support | MISSING | Zero mjml references | — |
| TPL-008 | React Email | MISSING | Vue product; feature absent | — |
| TPL-009 | Template versioning & activation | IMPLEMENTED | Versions + `activate` pointer swap + uniqueIndex (`template_version_handler.go:162-193`) | No diff view; active-version delete guard unverified |
| TPL-010 | Template localization (multi-language) | IMPLEMENTED | Localizations + exact→base→default→en fallback + per-subscriber language at send (`service.go:1377-1419`) | No L10n completeness report |
| TPL-011 | CSS inlining | IMPLEMENTED | go-premailer on render when version has stylesheet (`template_renderer.go:226-263`) — corrects seed uncertainty | Only when a stylesheet is attached |
| TPL-012 | Reusable blocks/sections library | MISSING | No block model/route | GrapesJS preset blocks are in-memory only |
| TPL-013 | Brand kit / shared stylesheets | IMPLEMENTED | Stylesheet entity CRUD + attach-to-version + render application; UI included | — |
| TPL-014 | Template preview with sample data | IMPLEMENTED | Ad-hoc + localized preview endpoints + 300ms-debounced live editor preview (`template_handler.go:235-271`, `TemplateEditor.vue:121-152`) | — |
| TPL-015 | Editor device previews | IMPLEMENTED | GrapesJS device previews (`EmailBuilder.vue:60-66`) | — |
| TPL-016 | Preview text (preheader) | NEEDS_VERIFICATION | No evidence in audits; subject templating exists | Preheader unverified |
| TPL-017 | Send-test to inbox | IMPLEMENTED | `POST /templates/{id}/send-test` with language + sample data (`template_handler.go:273-291`) | Sends to the authenticated user's own address |
| TPL-018 | Template import/export (JSON bundle) | IMPLEMENTED | Export/import incl. `BuilderJSON` so GrapesJS projects survive (`template_handler.go:327-441`) | No stylesheet dedup on import beyond name reuse |
| TPL-019 | HTML import (style extraction) | IMPLEMENTED | `POST /templates/import-html` ≤2MB with style/link resolution (`template_handler.go:446-534`); tests `html_parser_test.go` | No URL/zip import |
| TPL-020 | AI-assisted creation | MISSING | Zero LLM references | RF stance: self-host, no cloud |
| TPL-021 | Spintax variation | MISSING | Absent in RF (BillionMail niche) | — |
| TPL-022 | Media/image uploads & storage | NEEDS_VERIFICATION | Blob store exists for attachments | Template media-library surface not audited |

## 3.F — Analytics & tracking (ANA, 16 entries) — 8 IMP / 6 PARTIAL / 1 MISSING / 1 N/A

| ID | Feature | Status | Evidence | Gap note |
|---|---|---|---|---|
| ANA-001 | Open tracking | IMPLEMENTED | HMAC-signed pixel injection, campaign+UUID variants, day-bucket dedup (`services/tracking/tracking.go:161-227`, `emailhistory/service.go:146-153`) | No bot/UA filtering |
| ANA-002 | Click tracking | IMPLEMENTED | href rewrite skipping own/mailto/tel + TrackedLink table (`tracking.go:65-155`) | No attribution window |
| ANA-003 | Delivery status tracking | PARTIAL | `delivered` event defined but never produced — SMTP-accept only (`models/email_event.go:27`; `webhook_registry.go:32` still documents `email.sent` as "accepted by the destination MTA") | Needs ESP webhook ingestion (in-flight, §2.10) |
| ANA-004 | Bounce categorization (hard/soft) | PARTIAL | Hard auto-classified from 5xx (`worker/handler.go:428-467`) | Soft never auto-detected — exhaustion records only `failed` (`handler.go:538-574`) |
| ANA-005 | Complaint tracking | PARTIAL | API-only ingest (`bounce_handler.go:111-138`) | No `complained` email_events row ever written; no FBL source |
| ANA-006 | Campaign analytics (rates, links, series) | IMPLEMENTED | Rates + clicked-links + hourly open/click series + A/B variants (`tracking_handler.go:425-457`, `tracking_repo.go:122-224`) | No cohort/geo/device breakdowns |
| ANA-007 | Historical trend analytics (date ranges) | IMPLEMENTED | Gap-filled UTC daily series, p50-p99 latency, provider breakdown (`analytics_repo.go:49-330`; range tests `analytics_range_test.go`) | No aggregation table (scans base tables) |
| ANA-008 | Per-message event timeline & searchable logs | IMPLEMENTED | `/logs/messages/{uuid}/events` + content endpoint; effective timeline is 6 written event types (audit-F) | `delivered`/`complained` are dead constants |
| ANA-009 | Provider/inbox breakdown analytics | IMPLEMENTED | `ClassifyProvider` stamped on rows + provider-breakdown analytics (`email/provider.go:148-167`) | — |
| ANA-010 | Bounce-rate alerting | IMPLEMENTED | `bounce_alert` cron on workspace thresholds | Alert-only (no auto-pause) |
| ANA-011 | Exportable reports | PARTIAL | Audit-log CSV only (`logs_handler.go:821-892`, 100k cap) | No analytics/message export |
| ANA-012 | Tracking privacy controls (opt-out scoping) | PARTIAL | Global toggle disables rewriting+pixel for tx (`service.go:913-929`) | Campaign open-pixel injection continues (`campaign_processor.go:392-396`); no per-campaign scope — toggle asymmetry, §6 |
| ANA-013 | SMTP response/conversation inspection | PARTIAL | Last-error text + `smtp_code`/`smtp_stage` metadata (`worker/handler.go:409-488`) | No conversation transcript |
| ANA-014 | Dashboard KPIs | IMPLEMENTED | Redis-cached `/dashboard/stats` + hand-rolled SVG charts (`dashboard_handler.go:133-257`) | — |
| ANA-015 | Device/geo breakdowns | MISSING | Explicit audit-F gap | — |
| ANA-016 | Web analytics (site traffic) | NOT_APPLICABLE | NU-only adjacent product | Out of RF scope |

## 3.G — Automation (AUT, 18 entries) — 14 IMP / 2 PARTIAL / 2 MISSING

| ID | Feature | Status | Evidence | Gap note |
|---|---|---|---|---|
| AUT-001 | Visual workflow builder | IMPLEMENTED | @vue-flow builder, 9 node types, undo/redo (`web/src/views/automations/AutomationBuilder.vue:47,375-377`) | — |
| AUT-002 | Event triggers | IMPLEMENTED | 7 trigger kinds + hooks wired across handlers (`models/automation.go:78-84`, `services/automation/triggers.go:81-136`) | `email.sent/delivered` deliberately not triggerable (loop guard, `domain.go:74`) |
| AUT-003 | Custom event ingest API | IMPLEMENTED | `/automations/events` always-202 + properties ride journey context (`automation_handler.go:699-743`) | Authenticated + tenant-scoped but **not editor-gated** (§2.9 finding) |
| AUT-004 | Email action node | IMPLEMENTED | `SendAutomationEmail` (`services/automation/nodes.go:143-167`) | — |
| AUT-005 | Delay/wait steps | IMPLEMENTED | `next_run_at` parking + poll resume surviving restarts (`engine.go:38-42`, `nodes.go:106-137`) | No wait-until-event/time-of-day |
| AUT-006 | Conditional branching | IMPLEMENTED | filter + 3-way `list_status` branches; operators validated at save (`nodes.go:200-274`, `domain.go:632-661`) | No segment-membership conditions |
| AUT-007 | List-management action nodes | IMPLEMENTED | List nodes in graph model (`nodes.go:73-99`) | — |
| AUT-008 | A/B split node | PARTIAL | FNV-1a hash split with weight validation (`nodes.go:219-248`, `domain.go:580-625`) | No winner selection or persisted outcome |
| AUT-009 | Webhook action node (SSRF-guarded) | IMPLEMENTED | Static policy + pinned-dial transport + save-time URL validation + scope gate (`automation/webhook_client.go:59-149`) | Bearer not HMAC; response bodies capped 2 KB |
| AUT-010 | Durable journey engine | IMPLEMENTED | SKIP LOCKED claims, 10-node tick budget, 2^n backoff, lost-nudge sweep (~40 tests) (`engine.go:284-478`, `automation_journey_repo.go:87`) | No repeat/loop node (by design) |
| AUT-011 | Re-enrollment policy (once/every-time) | IMPLEMENTED | Atomic dedup insert before journey creation (`triggers.go:119-133`) | Failed dedup-insert burns the slot (documented) |
| AUT-012 | Execution stats & timeline observability | IMPLEMENTED | Executions + node-stats endpoints + UI (`automation_handler.go:613-688`) | — |
| AUT-013 | Lifecycle (activate/pause/duplicate/delete-safe) | IMPLEMENTED | CAS transitions + live-edit pickup + transactional journey exits (`automation_handler.go:441-606`) | No manual journey-cancel endpoint |
| AUT-014 | Welcome/onboarding sequences | IMPLEMENTED | End-to-end verified by engine tests (`engine_test.go:1304,1383`) | — |
| AUT-015 | Re-engagement / scheduled enrollment | PARTIAL | Triggers purely event-reactive (`domain.go:115-123`) | No inactivity conditions, no cron enrollment |
| AUT-016 | Stop-on-reply | MISSING | `ExitOnReply` reserved stub the engine never reads; no reply detection | — |
| AUT-017 | Automated suppression on bounce/complaint | IMPLEMENTED | 5xx auto-suppress (default on) + kind-tagged suppression upserts (`worker/handler.go:343,463-472`) | — |
| AUT-018 | Workflow testing / dry-run | MISSING | Static validation only (reachability/cycles/weights) | No test execution against a sample contact |

## 3.H — APIs & integrations (API, 19 entries) — 9 IMP / 5 PARTIAL / 4 MISSING / 1 N/A

| ID | Feature | Status | Evidence | Gap note |
|---|---|---|---|---|
| API-001 | REST API (full product coverage) | IMPLEMENTED | 340 route definitions across ~13 route files (audit-H; supersedes seed "~200") | — |
| API-002 | OpenAPI/Swagger docs | IMPLEMENTED | okapi OpenAPI + Scalar UI at `/docs` + webhook payload docs endpoint (`webhook_docs.go:29`) | — |
| API-003 | API keys (hashed, workspace-bound) | IMPLEMENTED | `psk_` keys SHA-256 hashed + per-key workspace + `X-Posta-Workspace-Id` (`services/auth/api_key.go`) | — |
| API-004 | Scoped API keys / permissions | IMPLEMENTED | send/read/webhooks/write/admin scopes; structural route-group enforcement (`middlewares/middleware.go:346`, scope map :312-324) | — |
| API-005 | Per-key IP/CIDR allowlists | IMPLEMENTED | `AllowedIPs` + `IPAllowed` at auth (`middleware.go:232-279`); also on relay credentials | — |
| API-006 | API key expiry & rotation | IMPLEMENTED | Expiry enforced in `ValidateKey` + 7-day notification cron (`api_key_expiry.go:46`) | — |
| API-007 | API rate limiting | PARTIAL | Hourly/daily send quotas only (`config.go:311-312`); fails closed on store error (`quota.go:24-27`) | No per-API-key or per-endpoint limits |
| API-008 | Official SDKs | PARTIAL | In-repo SDK docs for 6 languages (`docs/docs/sdks/`); Go/Node/PHP/Java repos confirmed, Python/.NET unverified | Registry parity unverified against the 340-endpoint API |
| API-009 | Outbound webhooks (event push) | IMPLEMENTED | 11-event registry (enum-pinning test) + scope/from/domain filtering (`dto/webhook_registry.go:29-48`, `webhook/dispatcher.go:310-387`) | — |
| API-010 | Webhook signatures & replay protection | PARTIAL | HMAC-SHA256 `X-Posta-Signature` (`dispatcher.go:485`) | No timestamp/replay window |
| API-011 | Webhook retries with backoff | IMPLEMENTED | 2s×2^n, 60s cap, jitter, default 3 retries (`dispatcher.go:56-71`) — fixes seed R-06 "linear" | All non-2xx retried (no 4xx-only policy) |
| API-012 | Webhook delivery history & inspection | IMPLEMENTED | `webhook_delivery_attempts` + deliveries API + incoming-webhook store (`dispatcher.go:367+,543`) | — |
| API-013 | Webhook test-fire / replay | MISSING | No test endpoint or replay at HEAD | Replay impl in-flight, uncommitted (§2.10) |
| API-014 | Inbound ESP event webhooks (adapters) | PARTIAL | Generic Posta schema only (`bounce_webhook_handler.go:83-165`) | No ESP-format adapters; SES-class payload compat untested |
| API-015 | Native ESP sending integrations | PARTIAL | Generic SMTP only (shared entry with INF-022) | — |
| API-016 | MCP server | MISSING | Zero MCP references (grep clean) | — |
| API-017 | CLI client | MISSING | Only server/worker/doctor subcommands (`main.go:37-53`) | `doctor` = workspace-integrity checker, not a client |
| API-018 | Zapier/platform integrations | MISSING | None | — |
| API-019 | Embeddable core package | NOT_APPLICABLE | SendPortal-only architecture | Not an RF goal |

## 3.I — Security & administration (SEC, 18 entries) — 13 IMP / 2 PARTIAL / 2 MISSING / 0 N/A (after SEC-016 reconciliation)

| ID | Feature | Status | Evidence | Gap note |
|---|---|---|---|---|
| SEC-001 | Multi-tenancy (workspaces) | IMPLEMENTED | Workspace resolution enforced by construction + RBAC integration tests (`models/workspace.go:41-70`, `middlewares/workspace.go:58-117`; `workspace_rbac_test.go`) | No per-tenant encryption |
| SEC-002 | RBAC | IMPLEMENTED | 4 roles + structural scope checks (`workspace.go:24-31`, `middleware.go:312-341`) | Automation write gating is UI-only (`c0408dc`; `automation_routes.go:35`) — §2.9 finding |
| SEC-003 | Team management & invitations | IMPLEMENTED | Admin-gated member routes + pending/accepted/declined invites + self-service acceptance (`workspace_routes.go:121-198`) | No bulk invite |
| SEC-004 | Audit logs | IMPLEMENTED | 3 overlapping surfaces (workspace/deployment/admin) + CSV export ≤100k (`audit/audit.go:38-52`, `logs_handler.go:821-892`) | No WORM, no read auditing (§7) |
| SEC-005 | Two-factor auth (TOTP) | PARTIAL | TOTP real (`twofactor/totp.go:27-42`) | `two_factor_required` enforcement is dead code (login never reads it, `user_handler.go:459-510`); no recovery codes — P0 trust issue |
| SEC-006 | SSO/OIDC (+ workspace SSO enforcement) | IMPLEMENTED | Generic OIDC auto-discovery + account linking + `EnforceSSO`/`AllowedDomains` (`auth/oauth.go:66-126`, `models/workspace_sso.go:25-37`) | No SAML (absent industry-wide), no SCIM |
| SEC-007 | Session management & revocation | IMPLEMENTED | JTI uniqueIndex + Redis blacklist + self-service + admin revocation (`session_handler.go:59-143`, `middleware.go:132-139`) | — |
| SEC-008 | Configurable retention framework | IMPLEMENTED | 10+ independent windows incl. per-form + spam-specific (`retention.go:195+`) | `security_events` window is dead — table grows unbounded (§6) |
| SEC-009 | GDPR deletion & export | IMPLEMENTED | GDPR endpoints + export/import with safety rails (domains re-verify on import) (`workspace_data_handler.go:118-305`) | No DSR workflow tracking |
| SEC-010 | Inbound spam scanning & filtering | IMPLEMENTED | 11 weighted signals + admin filters + form nonce/time-trap (`messagescan/scanner.go:108-341`, `form_ingest_handler.go:69-145`) | No CAPTCHA, no external reputation (Akismet) |
| SEC-011 | Antivirus integration | MISSING | grep clean | No AV hook in the inbound pipeline |
| SEC-012 | Ban engine / brute-force protection | IMPLEMENTED | 16-file security engine, CIDR pub/sub propagation, fail-open, admin bans+SSE (`internal/services/security/`) — RF differentiator | No geo-IP signals; no per-account velocity rules |
| SEC-013 | Admin-console IP allowlisting | PARTIAL | Ban-engine allowlist (detector exemption, `models/security.go:61-63`) ≠ admin-access control | Reverse proxy is the current answer (`config/security.go:61`) |
| SEC-014 | Plans & quota enforcement | IMPLEMENTED | Plan→default→settings resolution + admin plans CRUD (`plan/service.go:54-97`, `admin_routes.go:424-527`) | Billing out of scope (self-host) |
| SEC-015 | Admin impersonation | MISSING | Confirmed absent (audit-I) | — |
| SEC-016 | Credential encryption at rest | IMPLEMENTED (reconciled at synthesis) | AES-256-GCM via `internal/services/crypto/crypto.go` (`enc:` prefix, `Init(secret)` from `POSTA_ENCRYPTION_KEY` `config.go:375`; inited `server.go:108`, `worker.go:62`); `SMTPServer.BeforeSave/AfterFind` encrypt/decrypt (`models/smtp_server.go:76-100`); DKIM keys use the same service (`dkim_signer.go:140,207`); relay credentials store one-way hash (`smtp_credential.go:34`) | **Caveat:** with no `POSTA_ENCRYPTION_KEY` set, `crypto.Encrypt` falls back to reversible base64 — production docs must require the key |
| SEC-017 | Registration control / invite-only mode | PARTIAL | Verified-email gate on workspace create/invites/keys (`middleware.go:360-378`) | Global registration toggle not documented in audits |
| SEC-018 | SSRF hardening on outbound HTTP | IMPLEMENTED | Shared SSRF guard: pre-flight resolution, pinned-dial, redirect block (`automation/webhook_client.go:59`, `automation_handler.go:275`) | — |

## 3.J — Infrastructure & observability (OBS, 19 entries) — 11 IMP / 5 PARTIAL / 3 MISSING

| ID | Feature | Status | Evidence | Gap note |
|---|---|---|---|---|
| OBS-001 | Horizontal scaling (server/worker split) | IMPLEMENTED | `POSTA_EMBEDDED_WORKER` split + standalone worker with own health server (`config.go:102,348`; `cmd/posta/worker.go:47,264-288`) | — |
| OBS-002 | Multiple worker processes | IMPLEMENTED | SKIP LOCKED claims + deterministic TaskIDs + Inspector server visibility (`workermon/monitor.go:91-140`) | — |
| OBS-003 | Multiple sending servers / shared upstream pool | IMPLEMENTED | Shared-server fallback by sender domain, strict mode requires ownership (`worker/handler.go:227-243`) | One server per domain match — no LB pool |
| OBS-004 | Redis-backed queues/cache | IMPLEMENTED | 9 verified Redis roles (asynq broker, cache, rate limits, ZSET security windows, session blacklist, SSE pub/sub, nonces, heartbeats, verifier cache) | Single Redis is a SPOF (no sentinel docs) |
| OBS-005 | PostgreSQL system of record | IMPLEMENTED | GORM AutoMigrate + 6 versioned upgrade steps + migrate smoke test (`storage/migration/upgrade/`) | No pool-tuning docs, no read replicas |
| OBS-006 | Prometheus metrics | IMPLEMENTED | 20 families incl. `posta_queue_archived_tasks`; default-off, not UI-surfaced (`internal/metrics/metrics.go`) | No open/click rate metrics |
| OBS-007 | Grafana dashboards | MISSING | `/metrics` is the integration surface | A sample dashboard JSON would close it cheaply |
| OBS-008 | Health check endpoints | IMPLEMENTED | `/healthz` `/readyz` (DB+Redis) + worker health server + compose/Dockerfile HEALTHCHECKs (`internal/health/health.go:62-87`) | Worker `/readyz` semantics NV |
| OBS-009 | Queue metrics & latency observability | PARTIAL | Per-queue size/state via Inspector + SSE + gauges (`admin_queue_handler.go:37-306`) | No per-queue latency metric |
| OBS-010 | Runtime log viewer | PARTIAL | Rich business-event surfaces (events, webhook attempts); views `web/src/views/logs/*` | No stdout/app-log viewer |
| OBS-011 | Error monitoring (Sentry) | MISSING | grep clean; errors slog'ed | — |
| OBS-012 | Runtime resource monitoring | PARTIAL | SSE `system.status` with NumGoroutine+MemStats (`admin_handler.go:666-701`) | No CPU/disk/history |
| OBS-013 | Blob storage abstraction (FS/S3) | PARTIAL | FS/S3 blob store + retention deletes blobs (`config.go:144-152`) | No usage reporting or orphan GC |
| OBS-014 | Cron job visibility | IMPLEMENTED | `/admin/crons` lists 10 jobs incl. scheduler + reconcile (`cron_handler.go:36`) | No last-run/duration columns |
| OBS-015 | Worker heartbeats & version detection | IMPLEMENTED | Redis heartbeat 15s/45s TTL + connected/disconnected events + admin SSE mismatch detection (`workermon/heartbeat.go:34-54`) | — |
| OBS-016 | Deployment tooling (Docker/compose/CI) | IMPLEMENTED | 3-stage image embedding UI + compose (PG17/Redis7 healthchecks) + CI with real PG/Redis + multi-arch releases (§4-5) | — |
| OBS-017 | Backup/restore tooling | MISSING | Absent (seed R-11 still true) | No Helm/k8s manifests either |
| OBS-018 | Update check & version banner | IMPLEMENTED | Update-check cron + admin surface; phone-home disable-able (`update_check.go:44`) | — |
| OBS-019 | Multi-replica cron leader election | PARTIAL | No leader lock: all replicas run all 10 crons (§2.8) | Alert jobs (`bounce_alert`, `daily_report`) risk double-send |

---

# 4. Testing & CI/CD

## 4.1 Go tests

- **106 committed Go test files** per the audit baseline (git ls-files enumerates 109 including 3 helper/spec-adjacent files) across handler, service, repository, worker, cron, middleware, and route-integration layers.

**Per-package inventory (committed `*_test.go`):**

| Package | Files | Package | Files |
|---|---|---|---|
| `storage/repositories` | 14 | `services/workspace` | 2 |
| `services/email` | 10 | `services/tracking` | 2 |
| `services/automation` | 6 | `services/notification` | 2 |
| `services/webhook` | 5 | `services/inbound` | 2 |
| `services/messages` | 5 | `worker` | 6 |
| `storage/migration` | 4 | `cron/jobs` | 3 |
| `services/security` | 4 | `services/smtprelay` | 3 |
| `routes` (integration) | 4 | `middlewares` | 5 |
| `handlers` | 17 | `models` | 1 |
| `config` | 2 | other `services/*` (1 each) | 14 |

- Notable suites: `internal/cron/jobs/campaign_scheduler_test.go` (616 lines; conflict/rollback/ignore-paused scenarios at :497,528,555,590), `internal/handlers/admin_queue_test.go` (599 lines), `internal/handlers/domain_signing_handler_test.go` (942 lines), `internal/services/email/dkim_signer_test.go` (475 lines), `internal/services/email/smtp_sender_timeout_test.go` (black-holed-relay + mid-conversation stall at :117,212), `internal/services/automation/engine_test.go` (~17 engine tests incl. `TestExecuteJourneyAbortsWhenCASLost` :1623), `internal/storage/repositories/segment_query_test.go` (7 tests incl. SQL-injection :447 and legacy parity :660,697), `internal/routes/workspace_rbac_test.go` + `middlewares/workspace_test.go` (privilege-boundary integration tests), `internal/services/security/{detector,snapshot,bansvc,rules}_test.go` (fail-open design pinned), `internal/handlers/webhook_registry_test.go` (enum-pinning), `internal/handlers/ssrf_audit_test.go`, `migrate_smoke_test.go`.
- Test seams worth noting: `QueueInspector` interface for queue-admin tests (`admin_queue_handler.go:37+`), `backoffDelay` seam for webhook retry tests (`dispatcher.go:56-71`).

## 4.2 Frontend tests

- **2 spec files:** `web/src/views/subscriber-lists/SubscriberListPreview.spec.ts` (R-21 preview-fix regression), `web/src/views/admin/security/SecurityEvents.spec.ts`. Coverage is thin relative to ~35 API modules and ~90 routes (§6 risk spot).

## 4.3 CI workflows (`.github/workflows/`)

| Workflow | Contents |
|---|---|
| `ci.yml` | 3 jobs — `backend` (Go test/vet with **real PostgreSQL 17 + Redis 7 service containers**, `ci.yml:19-36`), `frontend` (type-check + build), `docker` (image build); concurrency-cancellation on PRs |
| `lint.yml` | golangci-lint (pinned, per `.golangci.yml`) |
| `release.yml` | Multi-arch buildx release images |
| `deploy-docs.yml` | Deploys the Docusaurus docs site |

## 4.4 Makefile

`build` · `build-ui` (builds dashboard and stages it into `internal/web/dist` for embedding) · `build-all` · `run` · `run-worker` · `dev`/`dev-worker`/`dev-ui` · `dev-deps`/`dev-deps-stop` · `test` · `vet` · `fmt` · `lint` · `tidy` · `clean`.

---

# 5. Deployment & Configuration

- **compose services** (`compose.yml`): `posta` (server, HEALTHCHECK `wget /healthz` :18-19), `worker` (separate asynq consumer, validates the same JWT secret :37), `posta-db` (postgres:17, `pg_isready` healthcheck :59-60), `posta-redis` (redis:7, `redis-cli ping` :69-70).
- **Dockerfile** (`docker/Dockerfile`, 3 stages): `node:24-bookworm-slim` ui-builder → `golang:1.27-alpine` CGO-free builder **embedding the built UI** → `alpine:3.21` runtime, non-root `USER posta` (:63), `HEALTHCHECK` (:66).
- **Entrypoints:** `server` (API + cron + optional embedded worker), `worker` (standalone, own health/metrics ports), `doctor` — **a workspace-integrity checker** (finds rows with NULL `workspace_id` that would become unreachable, verifies system-workspace presence and plan backfill; `cmd/posta/doctor.go:52-149`), not a general deployment preflight.
- **Env-var surface (~77 `POSTA_*` settings, `internal/config/config.go`), grouped:**
  - **DB:** `DB_HOST/PORT/USER/PASSWORD/NAME/URL/SSL_MODE`
  - **Redis:** `REDIS_ADDR/URL/USERNAME/PASSWORD/DB`
  - **JWT/auth:** `JWT_SECRET`, `AUTH_RATE_LIMIT_ENABLED`, `EMAIL_VERIFICATION_REQUIRED`
  - **Rate limits:** `RATE_LIMIT_HOURLY/DAILY`, `EMAIL_VERIFY_RATE_HOURLY`, `MESSAGES_*_RATE_LIMIT*` (per-IP/form/workspace windows), `SMTP_RELAY_RATE_LIMIT/WINDOW`, `INBOUND_SMTP_RATE_LIMIT/WINDOW`
  - **Security:** `SECURITY_ENABLED`, `SECURITY_FAIL_OPEN`, `ENCRYPTION_KEY`, `AUTO_SUPPRESS_ON_REJECT`, `CORS_ORIGINS`
  - **Webhooks:** `WEBHOOK_MAX_RETRIES`, webhook timeout (`config.go:344-345`)
  - **OAuth:** `GOOGLE_OAUTH_CLIENT_ID/SECRET`, `OAUTH_CALLBACK_URL`
  - **Blob storage:** `BLOB_PROVIDER`, `BLOB_FS_PATH`, `BLOB_S3_*` (endpoint/region/bucket/keys/path-style/use-ssl)
  - **Inbound:** `INBOUND_ENABLED/HOSTNAME/SMTP_HOST/SMTP_PORT/TLS_MODE/TLS_CERT_FILE/TLS_KEY_FILE/MAX_MESSAGE_SIZE/MAX_ATTACH_SIZE/WEBHOOK_SECRET`
  - **Relay:** `SMTP_RELAY_ENABLED/HOST/PORT/HOSTNAME/TLS_MODE/TLS_CERT_FILE/TLS_KEY_FILE/MAX_MESSAGE_SIZE`
  - **Messages/forms:** `MESSAGES_ENABLED`, `MESSAGES_INBOUND_DOMAIN`, `MESSAGES_MAX_BODY_BYTES`, `MESSAGES_MAX_ATTACH_SIZE`, `MESSAGES_PER_*` quotas
  - **System SMTP:** `SYSTEM_SMTP_HOST/FROM/PASSWORD/ENCRYPTION` (+ `PROVIDER_*` keys present in config for future ESP adapters — no clients wired, see INF-022)
  - **Runtime:** `PORT`, `ENV`, `EMBEDDED_WORKER`, `METRICS_ENABLED`, `OPENAPI_DOCS`, `DEV_MODE`, `PLAN_ENFORCEMENT`, `ALLOW_DOWNGRADE`, `SMTP_DIAL_TIMEOUT_SECS` (a6f1681)

---

# 6. Strengths & Risk-Spots Summary

Full analysis lives in `04-feature-gap-analysis.md`; this is the evidence-first digest.

## 6.1 Verified strengths

| Strength | Evidence |
|---|---|
| SSRF guard + proxy pinning on all outbound webhook/feed HTTP | `automation/webhook_client.go:59-149` (static policy → pre-flight public resolution → pinned-dial transport, redirects blocked, no ambient proxy); save-time URL validation (`domain.go:497-509`) |
| Security engine with bans + ZSET sliding-window detector | `internal/services/security/` (16 files, 5,298 LOC incl. tests): ZREMRANGEBYSCORE+ZADD NX signals `detector.go:117-151`, weighted risk tiers, CIDR snapshot + pub/sub propagation, fail-open, enforced at TCP accept / EHLO 421 / HTTP 403 |
| Webhook per-attempt history | Every attempt persisted to `webhook_delivery_attempts` (`dispatcher.go:367+,543`), body capped 2 KB, deliveries API + incoming-webhook store |
| Segment query builder guardrails | Whitelisted columns, 14 operators, bindvar-only values, custom-key regex (`segment_query.go:75-152,408`) with injection + workspace-scoping + legacy-parity tests (`segment_query_test.go:447,609,660`) |
| Atomic scheduler claim | `scheduled→sending` CAS claim with enqueue-failure rollback + deterministic TaskID collapse of double-fires (`campaign_scheduler.go:90-113`, committed 347ebb4) |
| Delivery-queue administration incl. dead-letter + reconcile | cancel/run/archive over pending/active/scheduled/retry/archived states + `queue_reconcile` self-healing (`admin_queue_handler.go`, `queue_reconcile.go:64-146`, committed a6f1681) |
| DKIM signing + rotation with encrypted keys | RSA-2048/Ed25519, two-phase selector rotation, DNS publication verification, AES-encrypted key material never serialized (`dkim_signer.go:80-136`, `models/domain_signing_key.go:73-79`, committed a6f1681) |
| Crypto at rest | AES-256-GCM `internal/services/crypto` for SMTP-server passwords and DKIM keys (SEC-016; base64 fallback caveat when `POSTA_ENCRYPTION_KEY` unset) |

## 6.2 Top risk spots (from the audit files)

| Risk | Evidence (1-2 lines) |
|---|---|
| Relay-only delivered/complained signals — dead constants | `delivered` and `complained` email-event types are defined but never produced (`models/email_event.go:27,32`) — delivery-rate analytics measure SMTP acceptance only; the provider_events ingestion that would fix it is in-flight (§2.10) |
| Bounce webhook inconsistency | `POST /webhooks/bounce` only marks the subscriber/message bounced (`bounce_webhook_handler.go:83-165`) — suppression rows and `email.complained` come only from the authenticated `POST /bounces` (`bounce_handler.go:91-138`); a stock deployment has no automatic provider-webhook→suppression path |
| No cron leader lock | All replicas run all 10 robfig/cron schedules (`cron/manager.go`; OBS-019) — `bounce_alert`/`daily_report` can double-send across replicas |
| Scheduled-email cancel gap | `send_at` transactional emails have no cancel endpoint and `status='scheduled'` rows are outside queue_reconcile's `queued`-only scan (`queue_reconcile.go:93`) — a lost Redis task strands the row |
| `security_events` unbounded | `security_events_retention_days` is declared (`security_handler.go:489`) and read (`security/rules.go:129`) but `SecurityEventRepository.DeleteOlderThan` (`security_event_repo.go:157-160`) has no caller — the highest-volume security table grows forever |
| Tracking toggle asymmetry | Global `email_tracking_enabled` disables link rewriting + pixel for transactional, but campaign open-pixel injection continues via the else-branch (`campaign_processor.go:392-396`), contradicting the setting's doc comment (`settings/provider.go:174-178`) — deliberate per comments, but an open product question (synthesis §6.5) |
| Frontend test coverage thin | 2 spec files against ~90 routes and ~35 API modules (`web/src/views/subscriber-lists/SubscriberListPreview.spec.ts`, `web/src/views/admin/security/SecurityEvents.spec.ts`) |
| Dead `two_factor_required` control | Setting seeded + getter exists but zero consumers; login never enforces it (`user_handler.go:459-510`) — a security control that silently does nothing (SEC-005) |
| Automation write endpoints lack server-side editor gate | `automation_routes.go:35` = `auth + workspace` only; `c0408dc` gated the UI only (SEC-002 finding) |

---

# 7. Duplication & Debt Notes (brief; details in `04-feature-gap-analysis.md`)

1. **Contacts vs subscribers dual model** — `models/contact.go:24-34` is a read-only per-workspace send ledger auto-populated by sends; `models/subscriber.go:63-78` is the real audience record. Two mental models, two API surfaces, two view sets (`web/src/views/contacts/` vs `subscribers/`).
2. **Orphaned contact-lists surface** — `internal/handlers/contact_list_handler.go` (7 methods) has **zero route registrations**; `web/src/views/contact-lists/*` + `web/src/api/contactLists.ts` are unrouted; dead in-app link `ContactListMembers.vue:33` pushes the nonexistent route name `contact-lists`. Finish or delete (it still round-trips in GDPR export/import, so removal has a data-migration cost).
3. **Three audit-log surfaces** — `/audit-log` (workspace, `workspace_routes.go:242-259`), `/logs/audit` (deployment scope + CSV, `workspace_resource_routes.go:1562-1576`), `/admin/events` (admin SSE, `admin_routes.go:205-226`) all read the same `events` table with different filters; UI triplication (`AuditLog.vue`, `AuditLogs.vue`, `Events.vue`).
4. **FilterRules vs SegmentCondition** — two segmentation engines coexist deliberately: legacy dynamic-list `FilterRules` (`subscriber_list.go:35-64`, `subscriber_repo.go:145 applyFilterRules`) vs the guarded `SegmentCondition` SQL builder (`segment_query.go`); the campaign worker resolves both (`campaign_processor.go:116-152`) with parity tests.
5. **ChangePassword/Profile duplication** — `UpdateProfile` and `ChangePassword` are separate handlers with parallel request types and validation plumbing (`internal/handlers/user_handler.go:170-177,406-425`) rather than one profile surface.

---

# 8. Status Summary

## 8.1 Per-category × per-status counts (registry v1.0.0 frozen totals)

| Category | IMPLEMENTED | PARTIAL | MISSING | NEEDS_REFACTOR | NEEDS_VERIFICATION | NOT_APPLICABLE |
|---|---|---|---|---|---|---|
| A (INF) | 21 | 9 | 5 | 0 | 0 | 1 |
| B (DOM) | 4 | 4 | 5 | 0 | 0 | 0 |
| C (CON) | 9 | 5 | 5 | 0 | 0 | 1 |
| D (CAM) | 8 | 5 | 5 | 0 | 0 | 0 |
| E (TPL) | 12 | 2 | 6 | 0 | 2 | 0 |
| F (ANA) | 8 | 6 | 1 | 0 | 0 | 1 |
| G (AUT) | 14 | 2 | 2 | 0 | 0 | 0 |
| H (API) | 9 | 5 | 4 | 0 | 0 | 1 |
| I (SEC) | 12 | 3 | 2 | 0 | 1 | 0 |
| J (OBS) | 11 | 5 | 3 | 0 | 0 | 0 |
| **Total** | **108** | **46** | **38** | **0** | **3** | **4** |

**Reconciliation delta (applied at synthesis, registry remains the frozen reference):** SEC-016 credential encryption moved NEEDS_VERIFICATION → IMPLEMENTED on repo verification (`internal/services/crypto/crypto.go`, `models/smtp_server.go:76-100`) — the live SEC row is 13 IMP / 2 PARTIAL / 2 MISSING / 0 NV, making the effective total **109 IMPLEMENTED / 2 NEEDS_VERIFICATION**, all other cells unchanged.

**NEEDS_VERIFICATION items named:** **TPL-016** (preview text/preheader — no evidence either way), **TPL-022** (template media/image uploads — blob store exists, media-library surface unaudited), and **SEC-016** (resolved — now IMPLEMENTED per above). In-flight work pending re-audit after commit: provider_events ingestion, A/B winner, timezone sends, webhook replay (§2.10).

## 8.2 Conflicts resolved while compiling this document

1. **CAM-004 duplication:** audit-D verified both the complete backend endpoint (`campaign_handler.go:466-500`) and the absence of any UI/API-client call site — the registry's PARTIAL (backend-only) verdict is retained as the honest record; no status change.
2. **WIP → committed:** audit-A-B and audit-C-D (drafted at `347ebb4`) marked dial timeouts, DKIM signing, queue admin/TaskID dedup, and tx scheduling "WIP, uncommitted"; these are committed at `a6f1681` (later audits E-F/G-H/I-J confirm) and recorded IMPLEMENTED with commit notes. Audit A-B's open NV items 2/4 (queue-action scoping, Jobs.vue/Domains.vue wiring) are considered resolved by the later audits' evidence (`admin_queue_test.go` exists; `web/src/api/admin.ts:120-136` wired).
3. **Route count:** seed "~200+" superseded by audit-H's verified 340 across ~13 route files.
4. **Bounce webhook semantics:** audit-A row credited `/webhooks/bounce` with complaint→suppression; audit-G correction 4 proved suppression/complaint only via the authenticated API — the stricter verified evidence governs (INF-019/ANA-005 notes).
5. **Cron count:** "8 crons" (seed) → **10** with `campaign_scheduler` (347ebb4) and `queue_reconcile` (a6f1681), confirmed against `cmd/posta/server.go:690-768` and the `Schedule()` declarations in `internal/cron/jobs/`.
