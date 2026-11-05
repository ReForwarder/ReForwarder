# ReForwarder — Development & Implementation History

> **Status:** Development record · **Last updated:** 2026-11-04 · **HEAD:** `7a84e46` (branch `main`)
>
> This document records what this repository implements and how it evolved during the active development window (2026-11-01 → 2026-11-04). Every claim is grounded in source code inspected on 2026-11-04; where a statement relies on a verification performed during development rather than in this pass, it is dated explicitly. Items that are uncommitted, partially implemented, or planned are labeled as such and are never presented as finished work. **Note:** HEAD moved during documentation (`7fd30eb` → `7a84e46`): the segments workstream was committed as `fd45e18` while this document was being written; §4.4, §6, §8, and §12 reflect the committed state.

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Repository Architecture](#2-repository-architecture)
3. [Security Fixes & Improvements](#3-security-fixes--improvements)
4. [Newly Implemented Features](#4-newly-implemented-features)
5. [Existing Feature Improvements](#5-existing-feature-improvements)
6. [Backend & API Changes](#6-backend--api-changes)
7. [Frontend & UI/UX Changes](#7-frontend--uiux-changes)
8. [Database & Data Management Changes](#8-database--data-management-changes)
9. [Infrastructure & Integrations](#9-infrastructure--integrations)
10. [Testing & Reliability](#10-testing--reliability)
11. [Important Files & Modules](#11-important-files--modules)
12. [Implementation Status](#12-implementation-status)
13. [Overall Development Summary](#13-overall-development-summary)

---

## 1. Executive Summary

ReForwarder is a self-hosted email delivery platform: transactional and campaign email sending, inbound email reception, SMTP relay, web form messaging, templates with versioning and localization, audience management, analytics, webhooks, multi-tenant workspaces, and a full administration surface. The stack is Go 1.27 on PostgreSQL 17 and Redis 7, with background processing through `asynq` and a Vue 3.5 / Vite 8 single-page dashboard embedded into the server binary.

**Initial state (initial commit `73ee10f`, 2026-11-01).** The repository began with the complete platform already in place: the sending API with API-key scopes, template versions/localizations with a GrapesJS visual builder and CodeMirror editor, campaigns with A/B variants and rate pacing, subscriber lists (static and rule-based dynamic), contacts, suppressions, RFC 8058 unsubscribe handling, domain verification (SPF/DKIM/DMARC), personal and shared SMTP servers, SMTP relay, inbound email, web form messages with a spam-filter engine, signed open/click tracking with a web-view token, workspace-scoped analytics, workspaces with role-based access, API keys, sessions, 2FA, plans and quotas, an audit log, GDPR tooling, an in-app notification inbox, Prometheus metrics, OpenAPI documentation with Scalar UI, and S3 or filesystem blob storage.

**Development since then (37 commits to `7a84e46`).** The major work items, in order of completion:

| # | Workstream | State |
| --- | --- | --- |
| 1 | Brand and interface completion (README, logo/favicon set, dark/light design-token system, layout passes) | Committed |
| 2 | Dependency maintenance (4 automated dependency-bump merges; one framework upgrade intentionally deferred — see §12) | Committed |
| 3 | Tracking controls: a global tracking toggle extending signed link rewriting and the open pixel from campaign-only to all outbound email | Committed |
| 4 | Abuse-prevention engine: signal-based detection, weighted scoring, escalating IP bans, 4 enforcement points, full admin surface | Committed |
| 5 | Logs & Message History: per-message event timeline, delivery attempts, raw webhook captures, audit export | Committed |
| 6 | Webhook egress hardening (SSRF): layered destination policy, IP-pinned dialing, error sanitization, independent audit, proxy-mode pinning | Committed |
| 7 | Automation engine: 9 node types, journey scheduler, triggers, custom-event ingest, Vue Flow visual builder | Committed |
| 8 | Documentation site (Docusaurus) and marketing site | Committed |
| 9 | CI/CD: test/lint/build pipelines, multi-arch release workflow, docs deployment | Committed |
| 10 | Segments: reusable audience filters with a condition compiler, campaign integration, drawer UI | Committed (`fd45e18`) |

Scale of the added work: roughly 40,000 added lines across the largest committed feature commits alone (`43ab560` security engine +11,759; `87cf0d2` logs +5,429; `e9ac335` automation +9,425; `1565242` automation UI +5,379; `5c2678a`/`7fd30eb` SSRF +2,466; `fd45e18` segments +6,093), 11 new database tables, and a test suite that grew to 103 backend test files.

---

## 2. Repository Architecture

### 2.1 Stack

| Layer | Technology |
| --- | --- |
| Backend | Go 1.27 (`go.mod`), `okapi` HTTP framework, `gorm.io/gorm` + `pgx/v5` |
| Data | PostgreSQL 17, Redis 7 (cache, rate limits, pub/sub, SSE broker, session blacklist, security windows) |
| Background jobs | `asynq` (Redis-backed), `robfig/cron/v3` |
| Email protocol | `emersion/go-smtp` / `go-sasl` (inbound receiver, SMTP relay) |
| Auth | JWT sessions (HttpOnly cookie) + API keys with scopes, TOTP 2FA (`pquerna/otp`), OAuth/OIDC |
| Frontend | Vue 3.5, Pinia 4, Vue Router 5, Vite 8 (Rolldown), axios; `@vue-flow/*`, CodeMirror 6, GrapesJS |
| Storage | S3 or filesystem blob store for attachments/imports |
| Observability | Prometheus `/metrics` (opt-in), `/healthz` + `/readyz`, structured logging |

### 2.2 Backend layout (`cmd/`, `internal/` — 423 Go files)

| Path | Responsibility |
| --- | --- |
| `cmd/posta/main.go` | CLI: `server` (default), `worker`, `doctor` (upgrade-readiness check) |
| `cmd/posta/server.go` | HTTP boot: config validation → storage → migrations → crypto init → admin seed → system workspace → blob store → notification service → security engine → inbound SMTP → cron → routes → relay → retry worker |
| `cmd/posta/worker.go` | Standalone `asynq` worker with the same handler wiring, plus an optional health server (`/healthz`, `/readyz`, `/metrics`) with processing-state readiness |
| `internal/routes/` | 18 route-definition files assembling every endpoint group (§6) |
| `internal/handlers/` | 73 files — one HTTP handler set per resource |
| `internal/services/` | 39 packages (business logic, §2.4) |
| `internal/worker/` | `asynq` task types, producer, email-send pipeline, campaign processor, inbound/message processors, health |
| `internal/models/` | 56 model files; 60 entries in the AutoMigrate registry (§8) |
| `internal/storage/repositories/` | 70 repository files; `scope.go` centralizes workspace-scoped resource ownership (`OwnsWorkspaceResource`, `ApplyScope`) |
| `internal/storage/migration/` | `migration.go` (AutoMigrate), `constraints.go` (index/FK repair pass), `upgrade/` (13 files — versioned data migrations) |
| `internal/middlewares/` | JWT/API-key authentication, API-key scopes, workspace scoping and role enforcement |
| `internal/config/` | Environment parsing, cross-field validation, production security refusal (`security.go`) |
| `internal/web/` | Embedded dashboard (`//go:embed` of the built SPA) — the binary is self-contained |
| `internal/metrics/`, `internal/health/` | Prometheus counters/histograms + middleware; liveness/readiness |

### 2.3 HTTP middleware chain (order verified in `internal/routes/routes.go`)

1. `okapi.RequestID()`
2. Security-engine ban enforcement (403 before any routing/auth, when enabled)
3. `metrics.PrometheusMiddleware()` + `GET /metrics` (only when `POSTA_METRICS_ENABLED=true`)
4. All API route groups (per-group auth: session JWT, admin JWT, or API key + scope + workspace)
5. Embedded SPA last (API paths excluded from the fallback)

CORS allow-lists are derived from `POSTA_CORS_ORIGINS` / `POSTA_WEB_URL` / `POSTA_API_URL`; trusted proxies come from `POSTA_TRUSTED_PROXIES` (invalid entries refuse to boot).

### 2.4 Service packages (`internal/services/`)

`security` (abuse prevention), `webhook` (dispatch + SSRF guard), `tracking` (link rewriting/pixels/tokens), `automation` (journey engine), `emailhistory` (event recording), `email` (SMTP sending, rendering, stamping), `inbound` (SMTP receiver), `smtprelay`, `messages` + `messagescan` (form inbox + spam scanner), `auth` (API keys, OAuth), `session` (Redis JWT blacklist), `twofactor`, `crypto` (credential encryption), `domain` (DNS verification), `emailverify`/`verifier` (address verification), `settings` (cached settings provider), `ratelimit` (Redis limiter), `plan`, `audit`, `eventbus`, `realtime` (SSE), `inbox`/`notification` (notification inbox + system email with 16 templates), `seeder` (+ seeded templates de/en/fr), `workspace`/`workspaceprovision`, `retry`, `workermon`, `updatecheck`, `cache`, `clientinfo`, `passwordreset`.

### 2.5 Frontend layout (`web/src/`)

| Path | Contents |
| --- | --- |
| `views/` | 24 directories, ~130 view components (dashboard, campaigns, contacts, subscribers, subscriber lists, segments, automations, templates, emails, messages, inbound, logs, analytics, admin incl. security, settings, smtp, smtp-relay, domains, webhooks, workspaces, apikeys, audit, bounces, notifications, auth, about) |
| `components/` | 9 shared components (SearchableSelect, MultiSelect, Pagination, ConfirmDialog, Sparkline, NotificationBell, DomainStatusBadge, EmailVerificationBanner, TemplateModal) |
| `composables/` | 8 (incl. `useAutomationBuilder.ts`, 915 lines; `useConfirm`, `usePagination`, `useUnsavedChanges`, `useModalSafeClose`, timezones, search tokens, API error handling) |
| `api/` | 36 Axios client modules over a shared `client.ts` (cookie session, 401 interceptor) |
| `stores/` | 5 Pinia stores (auth, workspace, theme, notification toasts, inbox) |
| `layouts/DashboardLayout.vue` | Authenticated shell: 10 collapsible nav sections, workspace switcher, theme toggle, notification bell, release banner |
| `assets/styles.css` | 1,060-line design-token system (§7.3) |

The router guards (`meta.auth`, `meta.guest`, `meta.admin`) plus a workspace pre-fetch ensure the `X-Posta-Workspace-Id` header is set before the first workspace-scoped request after a fresh login.

---

## 3. Security Fixes & Improvements

This section distinguishes **confirmed fixes** (a weakness existed and was closed), **hardening** (protective capability added), **preventive improvements** (structural safeguards), and **remaining limitations** (documented, not fixed).

### 3.1 Confirmed fix — webhook egress SSRF (`internal/services/webhook/ssrf.go`, `proxydial.go`, `dispatcher.go`)

| Aspect | Detail |
| --- | --- |
| **Issue** | Outbound webhook delivery followed administrator-supplied URLs. Before the fix, a URL could address loopback, private, link-local, or otherwise non-public infrastructure, DNS could resolve a public-looking hostname to an internal address (DNS rebinding), redirects could carry a request elsewhere, and raw dial/TLS errors stored with delivery attempts exposed internal addressing detail to dashboard readers. |
| **Risk** | Server-side request forgery: pivoting into the host network, probing internal services, exfiltrating request/response content (up to 2 KB of response body per attempt) to an attacker-chosen listener, and information leakage from stored error strings. |
| **Implementation** | A three-layer, fail-closed destination policy enforced inside the HTTP transport itself — not just at input validation: (1) **static URL validation** (`ValidateWebhookURL`): only `http`/`https`, no embedded credentials, decimal ports, rejection of `localhost` and IP-literal/private/special ranges (loopback, RFC 1918, link-local `169.254/16`, multicast, unspecified, plus IANA special-use ranges: `0/8`, `100.64/10`, `192.0.0/24`, TEST-NET-1/2/3, `192.88.99/24`, `198.18/15`, `240/4`, `2001:db8::/32`, 6to4 `2002::/16`, NAT64 `64:ff9b::/96`); (2) **resolve-time policy** (`ResolvePublicHost`): the *entire* DNS answer is rejected if any address is non-public (defeats mixed-answer and rebinding tricks); (3) **connection-time pinning** (`NewGuardedTransport`): each connection re-resolves, validates, and dials the validated IP literal directly, with a `Control` callback re-checking the raw socket address immediately before connect; ambient proxy environment variables are ignored (`Proxy: nil`). Redirects are never followed product-wide (`http.ErrUseLastResponse`). |
| **Technical details** | `SanitizeDeliveryError` collapses any raw transport error to one of six fixed categories (blocked / resolve / connection / timeout / TLS / generic) and is applied **at write time** (attempt records) and again **on read** (`webhook_delivery_handler.go`); `LegacyAttemptBodyUnsafe` flags response bodies captured under pre-hardening rows so their stored bodies are redacted fail-closed. Create/update-time validation runs in `internal/handlers/webhook_handler.go` (`ValidateWebhookURL` + `ResolvePublicURL`). The automation engine's webhook nodes were routed through the same guard (commit `fa383b3`): node URLs are validated against the static policy at automation save time (`internal/services/automation/domain.go`) and every delivery runs through the guarded pinned transport plus a pre-flight resolve (`internal/services/automation/webhook_client.go`). Proxy mode (`POSTA_WEBHOOK_PROXY_URL`) activates only when the operator sets `POSTA_WEBHOOK_PROXY_ACK_POLICY=enforced`, and pins destinations through the proxy too (`proxydial.go`): SOCKS5 dials carry the validated IP as an ATYP=1/ATYP=4 address (the proxy never resolves DNS) and HTTPS through HTTP proxies uses a CONNECT tunnel to the pinned IP with the original hostname preserved for SNI and the `Host` header via a per-hostname transport pool. HMAC request signing (`X-Posta-Signature: sha256=…`) and response-body truncation (2,048 bytes) remain in force. |
| **Result** | Webhook and automation-webhook traffic can only reach public, globally-routable unicast destinations; DNS rebinding and redirect-based escape are closed; stored delivery data no longer discloses internal infrastructure; policy violations fail fast (no retry sleeps) with a deterministic `ErrPolicyBlocked`. |

**Verification.** `internal/services/webhook/ssrf_test.go` (+510 lines) and `proxy`/`dispatcher` tests cover the policy matrix; `internal/routes/security_routes_test.go` and `internal/handlers/ssrf_audit_test.go` verify route auth, no stream shadowing, and a redaction matrix asserting audit/delivery rows never leak infrastructure detail. An independent cold audit (three reviewers, no implementation input) is recorded in `docs/security-research/webhook-ssrf-audit.md` with verdict **pass with limitations**; its two follow-ups were subsequently addressed in-tree (automation-node routing `fa383b3`, proxy-mode pinning `7fd30eb`). The fix was additionally verified live against the running stack on 2026-11-04 (development record).

### 3.2 Hardening — abuse-prevention engine (`internal/services/security/`, 17 files + `README.md`)

A signal-based detection and IP-ban engine protecting the SMTP, relay, and HTTP surfaces. Not a fix for a specific vulnerability — a protective subsystem added to the platform.

**Detection.** Seven signals recorded into Redis sorted-set sliding windows (key `sec:sig:<signal>:<ip>`, event timestamps as scores; every write is a trimmed, capped, expiring `TxPipeline`; every Redis operation bounded by a 500 ms timeout): `distinct_ports`, `new_connections`, `incomplete_handshakes`, `connect_aborts`, `malformed_preauth`, `suspicious_paths`, `auth_failures`. Authentication failures are recorded **dual-keyed** — per source IP and per login name (`sec:sig:auth_failures:login:<sha256>`), with evaluation taking the max of the two — and are fed from the `user.login_failed` domain event through the in-process event bus (`feed.go`). Scoring sums the weights of exceeded signals against a configurable risk threshold (default **40**); any single signal at ≥ 2× its threshold triggers an **instant ban**. Suspicious-path matching is case-insensitive, query-stripped, percent-decoded glob matching (`*.php`, `/.env*`, `/.git*`, `/wp-login*`, `/phpmyadmin*`, `/actuator*`, `/.aws*`, `/id_rsa*`, …). Signal exceedances are persisted as `security_events` rows (deduplicated per window in memory).

**Bans.** `BanService` is the single writer for `security_bans` / `security_allowlist` / `security_events`. Automatic bans carry JSON evidence (score, trigger, counts, windows, thresholds, port list, login). Duration escalates with repeat offenses through four tiers — **1 h → 24 h → 7 d → 30 d** — overridable per tier via platform settings (≤ 0 = permanent). Manual bans support CIDR ranges, fixed durations, or permanence, with a ten-code reason whitelist (e.g. `MANUAL_ADMIN_BLOCK`, `PORT_SCAN_DETECTED`, `AUTH_BRUTE_FORCE`). Allowlisted addresses suppress bans (an informational event is recorded instead); conflicting active bans extend to the longer expiry while preserving offense history; unbans expire in place so offense counts survive.

**Propagation.** The ban list is a **copy-on-write snapshot** (`snapshot.go`): readers load an immutable snapshot through an atomic pointer — lock-free, with the hot path touching neither Redis nor PostgreSQL — while writers replace a clone under a mutex. Mutations propagate to every process (server, worker, relay, inbound listener) via Redis pub/sub (`posta:sec:bans`), with reconnection + full resync and a 5-minute periodic reconciliation. IPv4-mapped IPv6 is canonicalized; overlapping CIDRs are held by the longest-covering ban.

**Enforcement — four points, all server-side:**

1. **SMTP accept** (`listener.go`): allowlist pass → banned connections are **silently closed** (no bytes, no SMTP banner) → otherwise the connection is counted; zero-byte closes record `connect_aborts` (plaintext probes of TLS ports).
2. **First SMTP command** (`smtp.go` session guard): a fresh snapshot re-check answers **`421 4.7.1`** to banned peers; AUTH failures and rejected RCPTs feed the respective signals.
3. **HTTP middleware** (`middleware.go`): registered after request-ID and before all route groups — banned IPs get **403**, every request records `new_connections`, suspicious paths are recorded pre-routing and on 404s.
4. **Dashboard authentication**: login failures feed the dual-keyed brute-force signal via the event bus.

**Administration.** Admin API (`/admin/security/*`: overview, bans with extend/lift, events, rules, allowlist) plus a live SSE stream (`GET /admin/security/events/stream` emitting `security.event`, `ban.created`, `ban.lifted`). Five admin views (§7.1) expose stat tiles and 14-day sparkline trends, ban management with evidence inspection, a live event stream with filter pills, inline-editable detection rules (grouped `general/signals/weights/escalation/retention`, value-clamped), and CIDR allowlisting. All rule values are clamped to sane minima on load; the engine is toggled by `POSTA_SECURITY_ENABLED` (default on) and `POSTA_SECURITY_FAIL_OPEN` (default on — availability over strictness, explicitly documented).

### 3.3 Preventive improvements

- **Route anti-shadowing tests** (`internal/routes/admin_sse_route_order_test.go`, `security_routes_test.go`): SSE stream routes are registered before parameterized siblings so a `{id}` route can never capture `/stream`; tests pin this ordering as a contract.
- **Write-and-read error sanitization** for webhook/automation delivery errors (§3.1) — sanitizing at both layers protects against future code paths that forget the write-time pass.
- **Production configuration refusal** (`internal/config/security.go`): boot fails in production when `POSTA_JWT_SECRET` is empty or a published placeholder (placeholder set includes `change-me-in-production`, `changeme`, `admin1234`, …); admin seed passwords must be ≥ 12 characters and non-placeholder (`ValidateAdminSeedPassword`); worker validation always refuses an empty Redis address, non-positive concurrency, or an invalid health port. Advisory checks (short secrets, wildcard CORS, unset trusted proxies, disabled form-ingest IP limits) are surfaced as boot warnings.
- **Compose-level secret guards** in `examples/compose.yml` (`${POSTA_JWT_SECRET:?required, min 32 chars…}`) so misconfigured deployments fail before starting.
- **Admin security actions are audit-logged** (`auditSecurity` in `internal/handlers/security_handler.go`).
- **Per-workspace uniqueness re-scoping** and FK/cascade repairs across the schema (§8) remove cross-tenant collision and accidental-deletion classes of bugs.

### 3.4 Remaining security limitations (documented, not fixed)

| Limitation | Detail | Location |
| --- | --- | --- |
| 2FA QR code rendered by an external service | 2FA enrollment renders the `otpauth://` URI (which contains the TOTP secret) through `https://api.qrserver.com` — the secret-bearing URI leaves the host. A local renderer (e.g. an npm `qrcode` dependency) is the identified remedy; not implemented. | `web/src/views/auth/Profile.vue:462` (verified 2026-11-04) |
| Reversible credential fallback | When `POSTA_ENCRYPTION_KEY` is unset, stored SMTP credentials fall back to reversible base64 encoding instead of AES-256-GCM. Surfaced as an advisory boot warning; production hard-fail is the identified remedy. | `internal/services/crypto`, `internal/config/security.go` |
| Security engine fails open | With `POSTA_SECURITY_FAIL_OPEN=true` (the default), Redis unavailability suspends new detection/banning rather than blocking traffic. Availability-over-strictness trade-off, documented and toggleable. | `internal/services/security/engine.go` |
| No CSP/HSTS middleware observed | Beyond request-ID and CORS, no dedicated Content-Security-Policy or HSTS header middleware exists in `internal/middlewares/` or boot code. | Observation from code inspection, 2026-11-04 |
| No CSRF token middleware | Browser sessions ride an HttpOnly cookie; state-changing endpoints rely on that plus same-origin policy and CORS allow-listing rather than per-request CSRF tokens. | Observation from code inspection, 2026-11-04 |
| Plain-HTTP proxy pinning rewrites the Host header | In proxy mode against plain-HTTP origins, the request line and `Host` both derive from the pinned IP (documented consequence of absolute-form forwarding). HTTPS origins are unaffected. | `internal/services/webhook/proxydial.go`, documented in `SECURITY.md` |

---

## 4. Newly Implemented Features

### 4.1 Abuse prevention & IP banning

Covered in depth in §3.2. User-facing summary: administrators get a Security section (Overview, Blocked IPs, Detection Events, Rules, Allowlist) with live streaming events, manual/CIDR bans with ten reason codes, evidence inspection, extend/lift actions, editable thresholds/weights/escalation, and allowlisting; abusive SMTP/HTTP clients are banned automatically with escalating durations.

### 4.2 Logs & Message History (`internal/services/emailhistory/`, `internal/handlers/logs_handler.go`, `web/src/views/logs/`, commits `87cf0d2` + `32dc574` + `2f9fddf`)

- **Problem solved.** Message outcomes were previously visible only as final statuses; there was no unified per-message lifecycle view, no record of inbound webhook payloads as they arrived, no per-attempt outbound webhook delivery history, and no exportable audit view.
- **Backend.** Three new tables — `email_events` (one row per lifecycle event across a closed set: `sent, delivered, failed, opened, clicked, bounced, complained, unsubscribed`; deduplicated by a unique `(email_uuid, type, provider_event_id)` index, with synthesized interaction IDs for pixel/redirect events), `incoming_webhook_events` (raw provider payloads captured before processing), and `webhook_delivery_attempts` (every HTTP attempt with status code, truncated response body, sanitized error). Lifecycle timestamps (`sent_at` … `unsubscribed_at`) are written once onto the email row via a whitelisted `UpdateStatusTimestamp`. The recorder is deliberately forgiving: panic recovery, rate-limited logging, nil-safe no-ops — history can never break sending.
- **API.** Nine endpoints under `/workspaces/current/logs/*`: message list (repeatable status-flag filters), message detail, content (`html | mjml | data | meta`, answering `{redacted:true}` when the `email_content_visibility` setting demands it), events, incoming-webhook list/detail, outgoing-webhook list + per-delivery attempts, audit list, and audit CSV export (≤ 100,000 rows; deployment-wide export requires an admin session).
- **Frontend.** Five views: `MessageHistory.vue` (status chips + eight text-filter chips, URL-synced, per-row event badges), `MessageDetail.vue` (three parallel loads so a content failure never hides metadata; tabbed content viewer; copy buttons), `IncomingWebhooks.vue`, `OutgoingWebhooks.vue` (per-delivery attempt panels), `AuditLogs.vue` (filterable, CSV export).
- **Retention.** `EmailEventsRetentionDays` (180), `WebhookDeliveryRetentionDays` (30), plus existing retention windows, enforced by the nightly retention cron job.
- **Status:** fully implemented, committed.

### 4.3 Automation engine & visual builder (`internal/services/automation/`, `internal/handlers/automation_handler.go`, `web/src/views/automations/`, commits `e9ac335`, `1565242`, and six follow-up fixes)

- **Problem solved.** Repetitive contact lifecycle actions (welcome sequences, re-engagement, winback, sync-out via webhooks) previously required manual campaigns; there was no event-driven workflow capability.
- **Engine.** Nine node types: `trigger, delay, email, filter, ab_test, list_status, add_to_list, remove_from_list, webhook`. Workflows are a flat JSONB node array with branch pointers; validation (`domain.go` + `graph.go`) enforces unique IDs, exactly one trigger at the root, per-node config correctness (delay duration/unit, email requires list + template, filter conditions, A/B ≥ 2 variants with weights summing to exactly 100, webhook URL passing the SSRF static policy), reachability, and acyclicity. Execution: a polling scheduler (10 s tick) claims due journeys with `FOR UPDATE SKIP LOCKED`, walks up to 10 nodes per tick with CAS cursor advancement, parks on delay nodes, retries failures up to 3 times with exponential backoff (2 min, 4 min) before marking the journey failed, and sweeps overdue journeys every 5 minutes. A/B assignment is deterministic (FNV-1a hash of contact + node IDs against cumulative variant weights). Node errors are sanitized through the webhook error sanitizer before persistence.
- **Triggers.** Seven event kinds — `list.subscribed, contact.created, contact.unsubscribed, email.opened, email.clicked, email.bounced, custom` (send/delivered events are deliberately excluded as loop protection) — with `once` / `every_time` frequency, per-automation condition filters, and an atomic trigger-dedup log for `once` semantics. An authenticated custom-event ingest endpoint (`POST /automations/events`, always 202 for well-formed payloads) accepts custom events; `email.bounced` enrollment is wired from the send pipeline.
- **API.** Ten endpoints under `/workspaces/current/automations/*` (CRUD, activate/pause/duplicate, executions list + per-journey detail with node-run timeline, aggregate + per-node stats, custom-event ingest). Deleting a live automation returns 409.
- **Frontend.** `AutomationsDashboard.vue` (status tabs, activate/pause/duplicate/delete), `AutomationBuilder.vue` + `useAutomationBuilder.ts` (915 lines: Vue Flow canvas with background/controls/minimap, draggable node palette, per-node config panel, branch handles per node type incl. one handle per A/B variant, undo/redo history, unsaved-changes guard, client-side validation mirroring the server), `AutomationDetail.vue` (read-only node canvas with per-node entered/completed/failed counts, executions table, per-journey execution drawer). Write affordances render only for workspace editors (owner/admin/editor) — read-only members see a read-only canvas (commit `c0408dc`).
- **Status:** fully implemented, committed (eight commits including lint and call-site fixes).

### 4.4 Segments — reusable audience filters (committed `fd45e18`, 2026-11-04)

- **Problem solved.** Dynamic subscriber lists already supported rule filters, but rules could not be reused across campaigns or previewed standalone; campaign creation required picking a concrete list.
- **Backend.** `internal/models/segment.go` (`segments` table; JSONB `SegmentCondition{Logic, Groups[]{Logic, Rules[]{Field,Operator,Value}}}` with `driver.Valuer`/`Scanner`), `internal/storage/repositories/segment_query.go` (713 lines): a condition→SQL compiler with 14 operators (`equals, not_equals, contains, not_contains, starts_with, ends_with, is_empty, is_not_empty, greater_than, less_than, one_of, not_one_of, exists, not_exists`), a whitelisted direct-field set (email, name, status, timezone, language, created_at, subscribed_at, unsubscribed_at), strictly-validated `custom_fields.<key>` JSONB extraction (`[A-Za-z0-9_]+`), escaped-LIKE wildcard handling, numeric casts via `NULLIF(...)::numeric`, and **engagement fields** (`engagement.opened_campaign / clicked_campaign / not_opened_campaign / received_campaign / inactive_days / active_days / total_opens_gte / total_clicks_gte`) compiled to the same subquery shapes the dynamic-list engine uses, so a segment and a dynamic list with identical rules resolve identically. `ValidateSegmentCondition` returns address-free, rule-attributed error messages; `BuildSegmentConditionSQL` emits only `?` bindvars — user input never reaches SQL text. `segment_repo.go` provides live count and paged subscriber resolution (limit −1 = all, used at send time). `internal/handlers/segment_handler.go` (300 lines) exposes CRUD plus preview of unsaved conditions and member listing, all edit-permission gated.
- **Campaign integration.** `internal/handlers/campaign_handler.go` resolves the audience XOR — exactly one of `list_id` or `segment_id` (enforced in `resolveAudience`, with ownership checks and mutual clearing on update); `internal/worker/campaign_processor.go` branches at send time: segment audiences resolve the full matching subscriber set (global suppressions still apply later in the send path), then proceed through the unchanged batch pipeline. Schema support: `idx_segments_workspace` (committed with the feature).
- **Frontend.** `web/src/api/segments.ts` (client + field/operator catalogs), `web/src/views/segments/Segments.vue` (list with human-phrased condition summaries, live matching counts, duplicate/delete with warnings), `SegmentDrawer.vue` (973 lines: grouped condition builder with AND/OR at group and rule level, typed value editors, custom-field support, debounced 400 ms live preview with count + 10-row sample, sequence-guarded stale-response handling, server validation errors rendered verbatim). Campaign creation (`Campaigns.vue`) gained an audience-mode radio (list XOR segment) with a live "≈ N recipients match" estimate. Sidebar nav and router entries are wired.
- **Tests present:** `segment_handler_test.go`, `segment_query_test.go`, `campaign_processor_segment_test.go`. Verification on record from development (2026-11-04): tests executed green with `-race`, `vue-tsc` type-check, lint, a live end-to-end send against a segment audience, and a visual UI gate (development record — not re-executed in this documentation pass; the commit `fd45e18` also landed after the code inspection on which this section is based).
- **Status:** fully implemented, committed (`fd45e18`); verified during development, not re-verified in this pass.

### 4.5 Tracking controls (`internal/services/tracking/`, commit `53200a7` + later generalization)

- **Problem solved.** Marketers need engagement tracking; some senders need to disable URL rewriting (deliverability/privacy/compliance), and tracking was limited to campaign HTML.
- **Implementation.** A platform setting, `email_tracking_enabled` (default `true`), governs whether outgoing email gets its links rewritten into signed `/t/c/` click redirects and whether the open pixel is injected (`internal/services/settings/provider.go:174-190`; the earlier campaign-scoped key remains as a deprecated alias). Rewriting persists `hash→URL` mappings (SHA-256 of campaign+URL, first 16 hex), signs redirects with HMAC-SHA256, and injects a `<img>` open pixel before `</body>` — open tracking is independent of click rewriting, so disabling tracking preserves pixel-only behavior when appropriate. The tracking service exposes transactional-email variants (`ProcessHTMLEmail`, `ClickURLEmail`, …) so non-campaign email carries the same signed, verifiable URLs. Unsubscribe handling includes RFC 8058 one-click (`/t/u/tx/`) and 90-day web-view tokens.
- **Status:** fully implemented, committed.

### 4.6 Documentation site (`docs/`)

A Docusaurus site (84 content pages across 18 sidebar categories — Getting Started, Email Sending, Templates, Subscribers, Campaigns, SMTP & Domains, Security, Contacts & Suppression, Tracking & Engagement, Webhooks & Events, Inbound Email, Forms & Messages, Analytics & Monitoring, Workspaces, Admin Panel, GDPR & Data, SDKs, Messages) with dark-by-default color mode, Mermaid support, project-page GitHub Pages deployment (`deploy-docs.yml`), and ReForwarder branding. Committed, including a deployment baseUrl fix and a brand-asset cache-bust pass.

### 4.7 Marketing site (`site/`)

A self-contained static site (`site/index.html`, 755 lines, plus `site/hosted.html`) using relative links and data-URI favicons — deployable to static hosting without a build step. Committed.

### 4.8 Release pipeline (`docker/Dockerfile`, `.github/workflows/release.yml`)

Tag-push releases build the three-stage image (Node 24 UI builder → Go 1.27 builder → Alpine runtime, non-root user, embedded dashboard, healthcheck) for `linux/amd64` and `linux/arm64`, stamping `VERSION` / `COMMIT` / `BUILD_DATE` through ldflags into `internal/config` so the binary reports an honest build identity; `-rc` tags publish to the `:rc` channel, stable tags to `:latest`. Committed.

---

## 5. Existing Feature Improvements

| Area | Previous behavior | Change introduced | Result |
| --- | --- | --- | --- |
| Security navigation (commit `ae830a4`) | Security admin entries rendered flat in the Admin section | Collapsible "Security" group with exact-match child highlighting | Five security pages read as one coherent area; no false highlighting on adjacent admin pages |
| Detection-event filters (commit `7d20128`) | Event-type filter pills derived dynamically from arbitrary data | Static canonical filter list | Filters are stable and match the backend's closed event-type set |
| Automation permissions (commit `c0408dc`) | Write affordances rendered for any workspace member | Editor-only write affordances; read-only canvas otherwise | UI mirrors server-side RBAC instead of revealing dead controls |
| Campaign audience selection (`fd45e18`) | Campaigns targeted a subscriber list only | Audience-mode radio (list XOR segment) with live recipient estimates | Segment audiences selectable with immediate feedback; invalid "both/neither" states rejected client- and server-side |
| Schema constraints (`internal/storage/migration/constraints.go`) | Several unique indexes spanned user+workspace scopes; some FKs lacked cascades; tracked links referenced campaigns only | Per-workspace uniqueness re-scoped (templates, stylesheets, contacts, domains, languages, suppressions, unsubscribe lists, subscribers), `fk_tracked_links_campaign` dropped for email-keyed tracking, template-localization and message-reply cascades repaired, API-key FK set to `ON DELETE SET NULL` | No cross-tenant unique collisions; deleting parents no longer orphaned or blocked children |
| Index additions | Missing composites for common dashboard/analytics and queue queries | `idx_emails_workspace_created`, `idx_bounces_workspace_created`, `idx_email_events_workspace_created`, partial unique `idx_notification_open`, case-insensitive `idx_verified_domain`, `idx_inbound_workspace_message_id`, messages/replies/filters indexes, partial unique `uq_security_bans_active_ip`, automation claim index `idx_automation_journeys_queue` | Faster scoped lists/analytics; DB-level dedup guarantees for notifications, verified domains, and one active ban per IP; the automation scheduler's claim query is index-backed |
| Frontend bundling | CodeMirror and GrapesJS pulled into the initial bundle | Rolldown `advancedChunks` splitting for `codemirror` and `grapesjs` | Smaller initial download for the dashboard |
| Dev-server stability | Vite watcher crashed (EBUSY) when tool-state directories changed under `web/` | `watch.ignored` covers dot-directories and tool folders | Reliable long-running dev sessions on Windows |
| Update banner | Version announcement via the release-check job | Server-side dismissal with optimistic rollback | Dismissals persist across sessions |
| Worker observability | Worker state opaque | Health server with processing-state readiness gating + asynq heartbeat | Orchestrators can gate traffic on actual processing readiness |

---

## 6. Backend & API Changes

### 6.1 New endpoint groups

| Group | Endpoints (all under `/api/v1`) | Auth |
| --- | --- | --- |
| **Security admin** | `GET /admin/security/overview` · `GET+POST /admin/security/bans` · `DELETE /admin/security/bans/{id}` · `POST /admin/security/bans/{id}/extend` · `GET /admin/security/events` · `GET+PUT /admin/security/rules` · `GET+POST /admin/security/allowlist` · `DELETE /admin/security/allowlist/{id}` | Admin session |
| **Security SSE** | `GET /admin/security/events/stream` (emits `security.event`, `ban.created`, `ban.lifted`) | Admin session (cookie) |
| **Automations** | `GET+POST /automations` · `GET/PUT/DELETE /automations/{id}` · `POST …/activate` · `POST …/pause` · `POST …/duplicate` · `GET …/executions` · `GET …/executions/{journeyId}` · `GET …/stats` · `POST /automations/events` | Session/API key + workspace (write: editors) |
| **Logs** | `GET /logs/messages` · `GET /logs/messages/{uuid}` · `GET /logs/messages/{uuid}/content` · `GET /logs/messages/{uuid}/events` · `GET /logs/incoming-webhooks(+/{id})` · `GET /logs/outgoing-webhooks(+/{id}/attempts)` · `GET /logs/audit` · `GET /logs/audit/export` | Session/API key + workspace (deployment-scope export: admin) |
| **Segments** *(committed `fd45e18`)* | `POST /segments/preview` · `POST+GET /segments` · `GET/PUT/DELETE /segments/{id}` · `GET /segments/{id}/contacts` | Session/API key + workspace (mutations: editors) |

### 6.2 Modified endpoints

- `POST/PUT /workspaces/current/campaigns` accept an audience XOR: `list_id` or `segment_id` (never both; one required). (commit `fd45e18`)
- Webhook create/update now validate destination URLs at input time; webhook-delivery reads return sanitized errors and redacted legacy bodies. (§3.1)

### 6.3 Background tasks

`asynq` task types: `email:send`, `campaign:start`, `campaign:batch`, `inbound:parse`, `inbound:process`, `message:process`, `message:reply`, `automation:run` across `Transactional`/`Bulk`/`Low` queues (served 6/3/1). Notable producer decisions: `campaign:start` uses a dedup TaskID; `automation:run` deliberately does **not** (journey-level CAS makes duplicate deliveries safe, and a dedup ID would swallow crash-recovery re-runs); `inbound:parse` is separated from `inbound:process` so a permanently unparseable message can never block the SMTP 250.

---

## 7. Frontend & UI/UX Changes

### 7.1 New screens

| Area | Views | Notes |
| --- | --- | --- |
| Segments *(committed `fd45e18`)* | `views/segments/Segments.vue`, `SegmentDrawer.vue` | List + condition-builder drawer with live preview |
| Security admin | `views/admin/security/` — `SecurityOverview`, `SecurityBlocked`, `SecurityEvents` (+ `SecurityEvents.spec.ts`), `SecurityRules`, `SecurityAllowlist` | Stat tiles + sparklines, ban management, live SSE stream, rule editing, allowlist |
| Automations | `views/automations/` — `AutomationsDashboard`, `AutomationBuilder`, `AutomationDetail` + `components/` (`NodePalette`, `ConfigPanel`, `StatCanvas`, `ExecutionDrawer`) | Vue Flow canvas, 9 node types, undo/redo, execution timelines |
| Logs | `views/logs/` — `MessageHistory`, `MessageDetail`, `IncomingWebhooks`, `OutgoingWebhooks`, `AuditLogs` | Filter chips, event timelines, CSV export |
| Notifications | `views/Notifications.vue` | In-app inbox with cursor pagination, category filters, banner-alert mode |

### 7.2 Navigation & shell

`layouts/DashboardLayout.vue` grew a 10-section collapsible sidebar (Overview, Sending, Inbound, **Audience** — now containing Segments and Automations — Deliverability, **Logs**, Developers, Infrastructure, Workspace, Admin-with-Security-group); collapse/group state persists in `localStorage` (`posta_nav_sections`, `posta_nav_groups`), with active-child force-open behavior; a three-way theme switcher (light/dark/system) lives in the topbar and user menu.

### 7.3 Design system

`web/src/assets/styles.css` defines the token system: near-black canvases with hairline borders and a single orange accent (`--primary-600 #e66a00`, signature `#ff801f`), per-theme semantic ramps tuned for WCAG AA contrast, inverted monochrome primary buttons, a constant white email canvas, Inter body / Instrument Serif display / Geist Mono technical type, a full component library (buttons, badges, tables, modals, toasts, skeletons, DNS panels), single `:focus-visible` ring, and a `prefers-reduced-motion` kill-switch. Dark theme is token-complete (`data-theme` attribute), defaulting to system preference.

### 7.4 Shared components

`SearchableSelect` (accessible combobox), `MultiSelect`, windowed `Pagination`, global `ConfirmDialog` via `useConfirm()`, dependency-free SVG `Sparkline` (accepts CSS-variable strokes), `NotificationBell` (60 s polling inbox with optimistic updates and a "99+" badge cap), `DomainStatusBadge`, `EmailVerificationBanner`, `TemplateModal`.

---

## 8. Database & Data Management Changes

**New tables (11), added across the window (all in the AutoMigrate registry, `internal/storage/migration/migration.go`):**

| Table | Purpose |
| --- | --- |
| `security_bans` | IP/CIDR bans with evidence JSONB, risk score, type (`automatic`/`manual`), offense count, expiry (`NULL` = permanent); partial unique index enforces one active ban per IP |
| `security_events` | Detection events (type, severity, protocol, port, evidence, action taken) |
| `security_allowlist` | Exempt addresses/CIDRs (unique index) |
| `email_events` | Per-message lifecycle events with a dedup unique index |
| `incoming_webhook_events` | Raw provider webhook payloads as captured |
| `webhook_delivery_attempts` | Per-attempt webhook delivery records |
| `automations` | Workflow definitions (flat node JSONB), status, stats counters |
| `automation_journeys` | Per-contact journey executions (cursor, scheduled-at, status; partial index backs the SKIP LOCKED claim query) |
| `automation_node_runs` | Per-node execution records (entered/completed/failed, output JSONB, sanitized errors) |
| `automation_trigger_log` | Atomic `once`-trigger dedup |
| `segments` | Named reusable audience conditions (JSONB) with workspace index |

**Constraint & index program** (`constraints.go`): per-workspace unique re-scoping, FK cascade/SET-NULL repairs, partial unique indexes (open notifications, verified domains, active bans, queued journeys), composite analytics indexes, and message/inbound index additions (§5).

**Data lifecycle:** settings-driven retention windows (`EmailBodyRetentionDays`, `EmailAttachmentRetentionDays`, `AuditLogRetentionDays` 90, `MessageRetentionDays` 365, `WebhookDeliveryRetentionDays` 30, `EmailEventsRetentionDays` 180, `TrackingEventRetentionDays` 90) enforced by the nightly `retention-cleanup` cron; body/attachment scrubbing queries exist in `email_repo.go` for privacy purges.

---

## 9. Infrastructure & Integrations

- **CI** (`.github/workflows/ci.yml`): backend job runs `go vet ./...`, `go test ./...` (Go version read from `go.mod`) against live `postgres:17-alpine` + `redis:7-alpine` services, then builds `./cmd/posta`; frontend job runs `npm run type-check` + `npm run build` on Node 24; a Docker build job (no push) runs on main. **Lint** (`lint.yml`): golangci-lint action pinned to v2.13.2 with a strict linter set (`staticcheck`, `revive`, `errcheck`, `gocyclo`, `dupl`, …). **Release** and **docs deployment** workflows per §4.6/§4.8.
- **Containerization** (`docker/Dockerfile`): glibc UI builder chosen deliberately over musl (Vite 8/Rolldown native bindings + the npm optional-deps musl bug), UI staged into `internal/web/dist` for embedding, CGO-disabled static binary, non-root runtime user, container healthcheck probing `/healthz`. `.dockerignore` prevents locally staged UI builds from leaking into images.
- **Local development** (`compose.yml` + untracked `compose.override.yml`): the override enables the inbound receiver (`POSTA_INBOUND_ENABLED`) and SMTP relay (`POSTA_SMTP_RELAY_ENABLED`) and exposes ports 2525/2526 — both off by default in the base compose file. Intentionally uncommitted as a local-only convenience.
- **Configuration surface** (`.env.example`, 239 lines, 20 groups): server/env, database, Redis, secrets (`POSTA_JWT_SECRET`, `POSTA_ENCRYPTION_KEY`), rate limits, **security-engine toggles (`POSTA_SECURITY_ENABLED`, `POSTA_SECURITY_FAIL_OPEN`)**, admin seeding, OpenAPI/metrics/update-check, web URLs/CORS/trusted proxies, worker (embedded toggle, health endpoints, auto-suppression on 5xx rejects), webhooks (retries/timeout/`POSTA_WEBHOOK_PROXY_URL`/`POSTA_WEBHOOK_PROXY_ACK_POLICY`), OAuth, blob storage, system SMTP, inbound email (TLS modes, size caps, per-IP limits), web form messages (multi-dimension rate limits, `msg+token@` inbound domain), SMTP relay, email hygiene/verification, and plan enforcement.
- **`SECURITY.md`** gained a "Webhook egress security" section documenting direct-mode IP pinning, the proxy-mode operator acknowledgement, and error sanitization — matching the implementation.
- **`cmd/posta/doctor.go`**: the `doctor` CLI subcommand checking upgrade readiness against the current data/state.

---

## 10. Testing & Reliability

- **Backend tests:** 103 `_test.go` files across config, cron, handlers (13), middlewares (5), models, routes (4), and 25+ service/worker/storage packages. The security package alone carries 34 test functions (`detector_test` 12, `snapshot_test` 9, `bansvc_test` 8, `rules_test` 5) covering window math, escalation, CIDR/allowlist precedence, snapshot concurrency, and rule clamping. SSRF coverage spans `ssrf_test.go`, `proxy` tests, dispatcher retry/attempt tests, and the `ssrf_audit` redaction-matrix tests.
- **Route-contract tests:** `internal/routes/security_routes_test.go` (admin auth required, stream-not-shadowed, surface matches contract), `workspace_rbac`, `message_routes`, `admin_sse_route_order`.
- **Executed verifications on record:** security-engine test suite run green with `-race` during development (2026-11-02); SSRF fix verified live end-to-end against the running stack (2026-11-04, development record and audit report). CI runs vet + test + build per push, and golangci-lint gates merges (lint-fix commits `78da301`, `b9a3a5f`, `2f9fddf`, `bb6968c`, `f03e719` document the gate working).
- **Frontend tests:** Vitest is configured (`vitest`, `@vue/test-utils`, `happy-dom`) with one spec (`SecurityEvents.spec.ts`, 5 test blocks); `npm run type-check` (vue-tsc) gates the frontend CI job. Broader component/view test coverage is a known gap.
- **Not re-executed in this pass:** the segments verification (tests with `-race`, type-check, lint, live E2E) is on record from development (2026-11-04) but was not re-run here; the feature's commit (`fd45e18`) postdates the code inspection for this document, and its CI status at push time was not re-verified.
- **No performance benchmarks have been measured**; index work in §5/§8 is motivated by query shape, not by recorded timing evidence.

---

## 11. Important Files & Modules

| File / module | Responsibility |
| --- | --- |
| `cmd/posta/server.go` | Boot orchestration: storage, migrations, seeding, security engine, SMTP listeners, cron, routes |
| `internal/routes/routes.go` | Assembles all endpoint groups and the middleware order |
| `internal/services/security/` | Detection engine, ban service, snapshot/propagation, listeners, HTTP middleware (17 files + README) |
| `internal/services/webhook/ssrf.go` + `proxydial.go` + `dispatcher.go` | Egress policy, pinned transports, retrying dispatcher with sanitized attempts |
| `internal/services/automation/` | Workflow domain/validation/graph, journey engine, node executors, triggers, webhook client (13 files) |
| `internal/services/emailhistory/` | Event recorder + pipeline hooks (write-once timestamps, dedup, redaction gating) |
| `internal/services/tracking/tracking.go` | Signed link rewriting, open pixel, unsubscribe/web-view tokens |
| `internal/services/settings/provider.go` | 30 s-cached settings provider (tracking, retention, visibility, rules) |
| `internal/storage/repositories/segment_query.go` | Condition→SQL compiler (operators, engagement subqueries, bindvars-only) |
| `internal/storage/migration/constraints.go` | Index/FK repair program |
| `internal/worker/campaign_processor.go` | Campaign start/batch pipeline incl. segment audiences (`fd45e18`) |
| `internal/handlers/logs_handler.go` | The nine log endpoints incl. redaction and CSV export |
| `internal/config/security.go` | Production security validation (placeholder refusal, advisories) |
| `web/src/composables/useAutomationBuilder.ts` | Builder state machine: node metadata, defaults, validation, undo/redo |
| `web/src/views/segments/SegmentDrawer.vue` | Condition builder with debounced live preview |
| `web/src/assets/styles.css` | Design-token system and component library |
| `docs/security-research/` + `docs/research/` | Nine design-research/audit reports (abuse-prevention architecture research, an implementation blueprint, feature-gap analysis, webhook egress audit) and segment-model design research — referenced by topic; the webhook audit is `docs/security-research/webhook-ssrf-audit.md` |

---

## 12. Implementation Status

**Fully implemented and verified (committed; exercised by tests and/or recorded live verification):**

- Webhook egress SSRF guard (direct + proxy modes), including automation-node routing and proxy pinning
- Abuse-prevention engine (detection, bans, propagation, 4 enforcement points, admin API + UI) — tests + `-race` green (2026-11-02)
- Logs & Message History (tables, recorder, API, UI)
- Automation engine + builder (engine tests: `domain`, `engine`, `graph`, `nodes`, `triggers`, `webhook_guard`)
- Global email tracking toggle; signed click/open/unsubscribe/web-view URLs
- Segments feature (committed `fd45e18`; verification on record from development, 2026-11-04 — tests with `-race`, type-check, lint, live E2E, visual gate — not re-executed in this documentation pass)
- Docs site, marketing site, CI/CD and release pipeline

**Implemented and committed, verification narrower than the feature surface:**

- Frontend breadth (only one Vitest spec; type-check + build are the automated gates)
- Schema constraint program (covered by a migration smoke test; not exercised against every migration path)

**Deliberately untracked local artifacts:**

- `compose.override.yml` — local enablement of the inbound/relay SMTP listeners; intentionally not committed

**Planned / not implemented:**

- A deletion guard that rejects removing a segment currently referenced by campaigns (noted as absent during development review)
- Local 2FA QR rendering (removes the external QR-service dependency — §3.4)
- Hard-fail on unset `POSTA_ENCRYPTION_KEY` in production (currently advisory)
- CSP/HSTS header middleware; CSRF token middleware (currently documented observations, §3.4)
- A framework dependency upgrade (`okapi` 1.1.0 → 1.2.0) is intentionally deferred: it removes an API the admin routes use (`okapi.DocErrorResponse` in `internal/routes/admin_routes.go`) and requires call-site adaptation; the upgrade branch is maintained open but unmerged

---

## 13. Overall Development Summary

Within the documented window the repository grew from a complete but static email platform into a considerably more defensive, observable, and automated one. The security posture changed most: outbound HTTP egress is now policy-bound at the transport layer with independently audited guarantees, and a self-contained abuse-prevention subsystem detects, scores, bans, and propagates enforcement across every network surface the product exposes. Operationally, the platform gained a full message-history plane (events, attempts, raw captures, exportable audit), an event-driven automation engine with a visual builder, and honest build/version identity in release artifacts. Audience tooling gained reusable segments with a bindvars-only condition compiler (committed as `fd45e18` at window close). Throughout, the work kept the platform self-contained (embedded UI, single binary), workspace-scoped (RBAC enforced server-side, mirrored in the UI), and verifiable (103 backend test files, CI with live service dependencies, pinned lint, route-contract tests) — with every remaining gap explicitly recorded in §3.4 and §12 rather than left implicit.
