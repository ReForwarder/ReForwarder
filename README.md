# ReForwarder

<p align="center">
  <img src="web/public/logo.png" alt="ReForwarder" width="150" />
</p>

<p align="center">
Self-hosted email platform for sending, receiving, and relaying email through a unified API and SMTP.
</p>
<p align="center">
  <a href="#overview">Overview</a> ·
  <a href="#quick-start">Quick Start</a> ·
  <a href="#core-features">Features</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#api-documentation">API</a> ·
  <a href="#dashboard">Dashboard</a> ·
  <a href="#sdks">SDKs</a> ·
  <a href="#license">License</a> ·
  <a href="#acknowledgments">Acknowledgments</a>
</p>

[![CI](https://github.com/ReForwarder/ReForwarder/actions/workflows/ci.yml/badge.svg)](https://github.com/ReForwarder/ReForwarder/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/ReForwarder/ReForwarder)](https://go.dev/)
[![GitHub Release](https://img.shields.io/github/v/release/ReForwarder/ReForwarder)](https://github.com/ReForwarder/ReForwarder/releases)

---

## Overview

**ReForwarder** is a self-hosted, developer-first email platform that handles **outbound**, **inbound**, and **relayed** email — driven by a single HTTP API, with SMTP available for the paths that need it.

- **Outbound** — send transactional and marketing email over your own SMTP, with templates, localization, campaigns, subscriber lists, bounce and complaint handling, and delivery analytics.
- **Inbound** — receive email at your domains, parse messages and attachments, and forward the structured payloads to your application via webhooks.
- **Relay** — point an existing SMTP client at ReForwarder and its mail flows through the same outbound pipeline as the API, so you can migrate to the HTTP API at your own pace.
- **Forms** — let a website contact form post straight to ReForwarder, scan each submission for spam, and reply to the sender from the dashboard through the same domains and suppression list as everything else.

It is designed as a fully self-hostable alternative to services like SendGrid, Mailgun, and Postmark — giving you complete ownership of your email infrastructure, data, and deliverability.

[![Website](https://img.shields.io/badge/Website-reforwarder.com-blue?style=flat-square)](https://reforwarder.com/)
[![Source Code](https://img.shields.io/badge/Source%20Code-GitHub-181717?style=flat-square&logo=github)](https://github.com/ReForwarder/ReForwarder)
[![License](https://img.shields.io/badge/License-AGPL--3.0--or--later-blue?style=flat-square)](#license)

---

## Quick Example

Send your first email:

```bash
curl -X POST http://localhost:9000/api/v1/emails/send \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "from": "Acme <hello@example.com>",
    "to": ["Jonas Kaninda <jonas@example.com>","bob@example.com"],
    "subject": "Hello from ReForwarder",
    "html": "<h1>Hello!</h1>"
  }'
```

> The `from`, `to`, and other address fields accept a plain address (`hello@example.com`)
> or RFC 5322 display-name format (`Acme <hello@example.com>`).

Response:

```json
{
  "id": "0ae4b04e-5c64-4b2f-bad6-460f8d5d98b3",
  "status": "queued"
}
```

Receive a website form submission — no JavaScript, no backend of your own:

```html
<form action="http://localhost:9000/api/v1/f/YOUR_FORM_KEY" method="POST">
  <input type="email" name="email" required>
  <textarea name="message" required></textarea>
  <button type="submit">Send</button>
</form>
```

The submission is scanned, stored, and shown in the dashboard, where a person
replies to the sender. There is no auto-responder.

---

## Core Features

### Email Delivery

* REST API for transactional, batch, and templated emails
* Attachments, custom headers, and unsubscribe support
* Web view ("view in browser") with signed, expiring links
* Scheduled sending and preview mode
* Email address verification (syntax, MX, disposable & role-account checks)
* Async processing with Redis and Asynq
* Automatic retries and priority queues

### Inbound Email

* Built-in SMTP receiver with TLS
* HTTP webhook ingest with HMAC verification
* Raw message, headers & attachment storage
* Forwarding with status tracking
* Spam scoring & retry on failure
* Real-time SSE stream for inbound notifications

### Web Form Messages

* Public ingest endpoint per form — `POST /api/v1/f/{key}`, no API key on the page
* Accepts plain HTML form posts, JSON, and multipart uploads; works without JavaScript
* Origin allowlist, honeypot field, single-use nonces, and per-IP, per-form, per-sender and per-workspace rate limits
* Spam scanning with a score, the reasons behind it, and an action ladder — allow, flag, quarantine, reject
* Workspace spam filters by keyword, phrase, regex, email, domain, or IP, with a dry-run before you commit
* Manual replies from the dashboard through the normal sending pipeline — no auto-responder
* Inbox states, assignment, attachments, and a live event stream
* Immediate, hourly, or daily digest notifications to the addresses each form nominates
* Off by default behind `POSTA_MESSAGES_ENABLED`

### Email Relay

* Point an existing SMTP client at ReForwarder — no code changes
* Relays into the same pipeline as the send API
* Domain verification, suppression & rate limits still apply
* Workspace-scoped credentials with IP allowlist
* Instant revocation, no redeploy
* Off by default — built for private networks

### Templates

* Versioned and multi-language templates
* Drag-and-drop visual email builder, or a code editor with syntax highlighting and a live preview
* Variable substitution and stylesheet inlining
* System variables for web view and one-click unsubscribe links
* Import/export, HTML file import, and desktop/mobile preview

### Campaigns

* Bulk email campaigns with scheduling and subscriber targeting
* Draft, scheduled, sending, paused, and cancelled lifecycle states
* A/B testing with multi-variant splits and per-variant performance metrics

### SMTP & Domains

* Multiple SMTP providers with TLS support
* Shared SMTP pools for teams
* Domain verification (SPF, DKIM, DMARC)
* Verified sender enforcement

### Security

* API keys with expiration, hashing, and IP allowlisting
* JWT authentication and RBAC
* Two-factor authentication (TOTP)
* OAuth / SSO login (Google, Keycloak, authentik, and more)
* Rate limiting and session management

### Contacts & Subscribers

* Auto-tracked contacts with per-recipient send/failure stats
* Subscriber management with static and dynamic (segmented) lists
* Bulk import via JSON or CSV with column mapping
* Subscriber lifecycle (active, bounced, unsubscribed)
* Hard/soft bounce and complaint handling with automatic suppression

### Unsubscribe Lists

* RFC 8058 one-click unsubscribe scoped per list
* ReForwarder-minted signed URLs and `List-Unsubscribe` header
* Scoped opt-outs so receipts and password resets keep flowing
* Management API and dashboard for CRUD and opt-out browsing
* `email.unsubscribed` webhook event
* Resubscribe individual addresses without lifting a global block

### Tracking

* Pixel-based open tracking
* Click tracking with link rewriting
* Per-email engagement metrics

### Workspaces

* Multi-tenant architecture with isolated workspaces
* Role-based access control (owner, admin, editor, viewer)
* Member invitations and scoped API keys
* Data export/import and GDPR contact/log deletion

### Webhooks & Events

* Event-driven architecture with webhook delivery
* Retry strategies and delivery tracking
* Audit logs and real-time event streaming

### Analytics & Monitoring

* Email delivery, open, and click metrics, compared against the preceding period
* Delivery and bounce trends, plus time-to-deliver percentiles
* Deliverability broken down by recipient mailbox provider — Gmail, Outlook, Yahoo, and the rest
* CSV export of any report
* Prometheus integration
* Health endpoints and daily reports

### Admin Platform

* User and API key management
* Platform dashboard, metrics, and logs
* Domains across every workspace, with a manual ownership override for the cases DNS cannot settle
* Announcements broadcast to every user's in-app inbox, retractable after the fact
* SMTP pool management
* Usage plans with quotas and per-workspace assignment
* OAuth/SSO provider configuration
* Platform configuration and retention policies

### Dashboard

* Vue-based UI for managing all resources
* Analytics, templates, SMTP, domains, contacts, campaigns, messages, and logs
* Per-user notification inbox with dismissible workspace health alerts
* Dark/light mode and user preferences

---

## Architecture

ReForwarder ships as a single binary that runs in two modes: `posta` serves the HTTP API,
dashboard, and SMTP listeners; `posta worker` consumes the job queue. Both modes are
stateless — all state lives in the shared PostgreSQL, Redis, and object storage — so you
can run as many workers as your send volume needs.

```mermaid
flowchart LR
    App["Your app<br/>REST API / SMTP client"]
    MX["Inbound mail<br/>MX delivery"]
    Form["Website form<br/>POST /api/v1/f/{key}"]

    subgraph app["ReForwarder"]
        Server["<b>posta</b><br/>HTTP API :9000 · Dashboard<br/>Inbound SMTP :2525 · Relay :2526"]
        W1["<b>worker 1</b><br/>asynq consumer"]
        W2["<b>worker 2</b><br/>asynq consumer"]
    end

    subgraph shared["Shared state"]
        Redis[("Redis<br/>Asynq queues<br/>+ scheduler")]
        PG[("PostgreSQL<br/>emails · templates<br/>contacts · logs")]
        Blob[("Object storage<br/>S3-compatible or filesystem<br/>attachments · raw inbound")]
    end

    Providers["SMTP providers<br/>outbound delivery"]
    Hooks["Your webhook<br/>endpoints"]

    App --> Server
    MX --> Server
    Form --> Server
    Server -- enqueue --> Redis
    Redis -- dequeue --> W1
    Redis -- dequeue --> W2
    Server <--> PG
    W1 <--> PG
    W2 <--> PG
    Server -.-> Blob
    W1 & W2 -.-> Blob
    W1 & W2 --> Providers
    W1 & W2 --> Hooks
```

Running the API without a dedicated worker works for evaluation, but production
deployments should split them so sending, retries, campaigns, and scheduled jobs scale
independently of request traffic — see
[`examples/docker-compose-full.yml`](examples/docker-compose-full.yml).

Object storage is optional (dashed above). With `POSTA_BLOB_PROVIDER` unset, attachments
and raw inbound messages stay in the database; point it at `s3` or `fs` to move them out.
Any multi-node deployment should use `s3` — a local `fs` path is not shared between the
server and its workers.

### Stack

* Backend: Go (Okapi framework)
* Frontend: Vue 3 + Vite
* Database: PostgreSQL
* Queue: Redis + Asynq
* Metrics: Prometheus

---

## Project Roadmap

Planning details live in `docs/project/`: [FEATURE_INVENTORY.md](docs/project/FEATURE_INVENTORY.md) is the detailed registry (123 entries as of 2026-11-12), [BACKLOG.md](docs/project/BACKLOG.md) the operational backlog, and the [ReForwarder Backlog](https://github.com/ReForwarder/ReForwarder/projects) board the task board. The Option B mail-infrastructure programme (Phases 0–6) **closed 2026-11-12 at HEAD `ee38ca8`** — the owner-mandated what-added / what-is-done / what-will-be-done / what-is-incomplete accounting lives in [todo.md](todo.md) Track A, and the delivered mail capabilities are registered as RF-F109–RF-F123 below.

### To-dos

- [ ] [RF-F029] Custom tracking domain
- [ ] [RF-F048] Transactional scheduled send (API send_at) — broken end-to-end (GAP-01 remainder)
- [ ] [RF-F056] Webhook test-fire endpoint
- [ ] [RF-F057] Bounce classification analytics surface (classification counts)
- [ ] [RF-F061] UTM auto-tagging of campaign links
- [ ] [RF-F071] Automation workflow dry-run / test tooling
- [ ] [RF-F073] Preference center (multi-list subscription management)
- [ ] [RF-F074] Subscriber signup forms (form-to-subscriber capture)
- [ ] [RF-F075] Double opt-in (DOI) confirmation flow
- [ ] [RF-F076] Subscriber tags
- [ ] [RF-F092] Admin-console IP allowlisting
- [ ] [RF-F099] Delivered-state analytics, email.delivered emission, and dead delivered UI paths (GAP-02 residual)
- [ ] [RF-F100] Tracking toggle symmetry (product-decision gate)
- [ ] [RF-F101] Contact Lists pages — dead end-to-end (handler never registered, views unrouted)
- [ ] [RF-F103] Backend endpoints with no UI consumer / dead client functions (segments contacts, message analytics & assign, automation custom-event ingest, admin provider breakdown)

### In Progress

- [ ] [RF-F104] Campaign duplication UI wired (2026-11-07); residual: campaign-level failed-recipient retry
- [ ] [RF-F005] 2FA recovery codes
- [ ] [RF-F014] Passkeys / WebAuthn
- [ ] [RF-F015] Admin impersonation
- [ ] [RF-F026] Domain deliverability health / diagnostics
- [ ] [RF-F027] Domain-sending policy (strict verified-domain mode, R-23)
- [ ] [RF-F028] DMARC aggregate report ingestion (rua)
- [ ] [RF-F053] Legacy bounce webhook (/webhooks/bounce)
- [ ] [RF-F081] System-info endpoint feature flags (Section-11 gate)
- [ ] [RF-F082] API route contract gate (golden file + frontend spec)
- [ ] [RF-F023] DNS record guidance depth — drift re-verification landed for provider-connected domains (RF-F120); SPF/DMARC parsing depth remains
- [ ] [RF-F040] Per-domain inbound routing rules — the routing engine + forwarding + rules frontend landed (OB-2); the `/inbound-routes` rules-CRUD REST handler registration is the remaining integration pass (KNOWN_DRIFT)
- [ ] [RF-F122] Sending IP pools, warm-up controls, and reputation monitoring (SF-07) — landed in OB-6 W3; remaining: blacklist alerts + abuse-response runbook (RF-B123 AC 3), metrics families (AC 4)
- [ ] [RF-B118] Outbound MTA residuals — landed in OB-1 (`cdfa066`); remaining: GAP-48 queue hold/pause/throttle + latency metric, outbound SPF/DMARC alignment checks
- [ ] [RF-B119] Inbound routing residuals — all seven waves landed (`b756f63`); remaining: the `/inbound-routes` rules-CRUD handler registration pass
- [ ] [RF-B120] Mailstore residuals — all five waves landed (`be9883f`); remaining: backup/restore/migration tooling (RF-B125 AC 2), orphan-object enumeration (GAP-45 remainder), size-based retention
- [ ] [RF-B124] DNS automation + certificate lifecycle residuals — W1 DNS automation + W2 ACME/SNI landed (`ee38ca8`); remaining: DNS-01 via the reserved `CertSolver` seam, expiry monitoring/alerting, 8-of-11 provider adapters
- [ ] [RF-B125] Deployment/DR residuals — deployment + Redis-HA runbooks and the failover drill landed (`ee38ca8`); remaining: tested rollback, blob/message + mailbox-store backup coverage and restore drills, full DR runbook with RPO/RTO

### In Review

_None._

### Done

- [x] [RF-F001] Email + password registration and login
- [x] [RF-F002] Per-IP login rate limiting
- [x] [RF-F003] Session lifecycle (JWT sessions, revocation, device list)
- [x] [RF-F004] Two-factor authentication (TOTP)
- [x] [RF-F006] two_factor_required policy enforcement (GAP-12) — marker + forced-enrollment redirect; admins not exempt (2026-11-07)
- [x] [RF-F007] Workspace RBAC (owner/admin/editor/viewer)
- [x] [RF-F008] Workspaces / tenants (CRUD, system workspace, personal provisioning, default workspace)
- [x] [RF-F009] Workspace invitations
- [x] [RF-F010] API keys (scopes, workspace binding, IP allowlist, expiry, quota)
- [x] [RF-F011] Per-API-key rate limits
- [x] [RF-F012] Per-API-key usage analytics
- [x] [RF-F013] API-key credential transport restriction
- [x] [RF-F016] Self-service password reset
- [x] [RF-F017] Email verification
- [x] [RF-F018] OAuth login, account linking, provider admin, workspace SSO
- [x] [RF-F019] Account lifecycle (self-service deletion scheduling)
- [x] [RF-F020] Admin user administration
- [x] [RF-F021] Domain CRUD (workspace + admin)
- [x] [RF-F022] Domain ownership + DNS verification (TXT token, SPF/DKIM/DMARC checks)
- [x] [RF-F024] Managed DKIM signing on the send path
- [x] [RF-F025] DKIM key lifecycle: generate / rotate / verify-dns / retire / delete
- [x] [RF-F030] SMTP relay listener (:2526)
- [x] [RF-F031] SMTP relay submission auth and relay-to-send pipeline
- [x] [RF-F032] STARTTLS/TLS modes for both SMTP listeners
- [x] [RF-F033] SMTP relay credentials (bcrypt, legacy-SHA-256 migration, IP allowlists, CRUD API+UI)
- [x] [RF-F034] Inbound SMTP receiver (:2525)
- [x] [RF-F035] Inbound webhook ingest (POST /api/v1/inbound/webhook)
- [x] [RF-F036] Inbound SSE stream (GET /workspaces/current/inbound-stream)
- [x] [RF-F037] Website form messages (public form ingest + inbox)
- [x] [RF-F038] Form thread replies via inbound email (msg+token@ domain)
- [x] [RF-F039] Messages SSE stream (GET /workspaces/current/message-stream)
- [x] [RF-F041] Email send pipeline (API send → asynq enqueue → worker SMTP delivery)
- [x] [RF-F042] Asynq queues + deterministic TaskIDs (transactional/bulk/low)
- [x] [RF-F043] Retry layer 1 — asynq task retries + exhausted-error handler
- [x] [RF-F044] Retry layer 2 — DB retry worker (bounded jittered sweep)
- [x] [RF-F045] Manual retry (POST /emails/{id}/retry + dashboard retry button)
- [x] [RF-F046] queue_reconcile — lost-task healing cron
- [x] [RF-F047] Campaign scheduled sends (scheduled_at + campaign-scheduler cron)
- [x] [RF-F049] Campaign pause / cancel / resume with send-time re-check
- [x] [RF-F050] Queue administration (admin API, dashboard Jobs view, dead-letter gauge)
- [x] [RF-F051] Bounce handling — 14-class failure classifier + SMTP-path hard-bounce chain
- [x] [RF-F052] Provider events ingestion (SES/SNS, SendGrid, Postmark, Mailgun)
- [x] [RF-F054] Suppression list (scoped CRUD, send-time filtering, auto-suppress chain)
- [x] [RF-F055] Webhook delivery history and operator replay
- [x] [RF-F058] Campaign lifecycle & fan-out sending engine
- [x] [RF-F059] Campaign A/B testing with hold-back and winner selection
- [x] [RF-F060] Timezone-aware sending
- [x] [RF-F062] Segments — SegmentCondition SQL builder, preview and campaign targeting
- [x] [RF-F063] Subscriber management (marketing audience CRUD, custom fields, bulk import)
- [x] [RF-F064] Dual audience models: Contact (transactional) vs Subscriber (marketing)
- [x] [RF-F065] Subscriber lists — static + dynamic filter-rule lists with subscribe/unsubscribe APIs
- [x] [RF-F066] Templates — CRUD, preview, send-test, HTML import, JSON export/import
- [x] [RF-F067] Template versions with activation
- [x] [RF-F068] Template localizations (per-language content)
- [x] [RF-F069] Automation execution engine (journeys, node executors, triggers, schedules)
- [x] [RF-F070] Automation visual builder (vue-flow canvas, palette, config panel, execution drawer)
- [x] [RF-F072] Unsubscribe — campaign list opt-out + RFC 8058 one-click transactional
- [x] [RF-F077] Workspace data export/import (audience + settings JSON)
- [x] [RF-F078] Admin platform core (users, workspaces, domains, plans, servers, events, metrics, announcements, updates)
- [x] [RF-F079] Scheduled-jobs admin endpoint (GET /admin/jobs)
- [x] [RF-F080] Platform settings engine (GET/PUT /admin/settings + seeded security rules)
- [x] [RF-F083] Webhooks engine (scoped dispatch, HMAC signatures, bounded exponential retries, per-attempt history)
- [x] [RF-F084] Webhook SSRF guard (static policy, resolve-and-reject, pinned dial, redirect block, sanitized errors)
- [x] [RF-F085] Webhook egress proxy with destination pinning (http/https/socks5, double opt-in)
- [x] [RF-F086] Open/click/unsubscribe tracking with HMAC-signed tokens
- [x] [RF-F087] Analytics APIs (admin + workspace daily counts, status breakdown, dashboard trends, provider breakdown)
- [x] [RF-F088] Security engine — ban service (tiered escalation, allowlist-wins, multi-replica propagation)
- [x] [RF-F089] Fail2ban-style detectors (7 sliding-window signals, weighted scoring, instant-ban thresholds)
- [x] [RF-F090] Security admin surface (overview, bans, events, tunable rules, allowlist, SSE streams)
- [x] [RF-F093] Encryption at rest for credentials and signing keys (AES-256-GCM)
- [x] [RF-F094] Retention cron and GDPR tooling (multi-store cleanup, blob purge, GDPR message erase, account cleanup, data export)
- [x] [RF-F095] Database backups (pg_dump cron with retention and restore runbook, R-11)
- [x] [RF-F096] Prometheus metrics and health probes
- [x] [RF-F098] REST API response envelope and OpenAPI documentation
- [x] [RF-F105] Core dashboard integration surface (verified end-to-end at contract level)
- [x] [RF-F102] Dead admin API client methods removed (listApiKeys / revokeApiKey / listEmails — CA-02, 2026-11-07)
- [x] [RF-F091] Security-events retention enforcement (GAP-13 — 2026-11-07)
- [x] [RF-F097] Error-monitoring hook (GAP-51 — OB-6 W5, 2026-11-11)
- [x] [RF-F106] Platform metric-alert engine (SF-96 — OB-6 W5, 2026-11-11)
- [x] [RF-F107] MTA-STS policy hosting + outbound enforcement (SF-29 — OB-6 W4, 2026-11-11)
- [x] [RF-F108] SMTP TLS reporting — TLS-RPT (SF-31 — OB-6 W4, 2026-11-11)
- [x] [RF-B121] Option B Phase 4 — IMAP4rev1 + POP3 servers over the mailstore with app-password auth, TLS, limits, audit (`183303a`; OB-4 close-out green)
- [x] [RF-B122] Option B Phase 5 — Webmail and mailbox administration: `/api/v1/mail/*` surface, compose with scanned attachments, the Vue mail area, admin provisioning (`3ef6291`; close-out gate + visual acceptance green 2026-11-10)
- [x] [RF-F109] Outbound MTA: direct-to-MX delivery + authenticated submission :587 (OB-1 — `cdfa066`, ADR-001)
- [x] [RF-F110] Inbound forwarding with loop protection and confirmed targets (OB-2 W2 — `b756f63`, ADR-002)
- [x] [RF-F111] Inbound quarantine and operator review (OB-2 W3 — `b756f63`, ADR-002)
- [x] [RF-F112] VERP return-path + RFC 3464 DSN bounce match-back (OB-2 W4 — `b756f63`; CA-05, GAP-02 residual)
- [x] [RF-F113] Inbound spam scoring + malware scanning (ClamAV) (OB-2 W5 — `b756f63`, ADR-006; GAP-47)
- [x] [RF-F114] ARC + iprev verification annotations (OB-2 W6 — `b756f63`, ADR-002)
- [x] [RF-F115] Hosted mailbox storage — the mailstore (OB-3 — `be9883f`, ADR-003)
- [x] [RF-F116] IMAP4rev1 server over the mailstore (OB-4 — `183303a`, ADR-004; SF-85)
- [x] [RF-F117] POP3 server over the mailstore (OB-4 — `183303a`, ADR-004; SF-86)
- [x] [RF-F118] Webmail — mail REST surface, compose with scanned attachments, the Vue mail area (OB-5 — `3ef6291`, ADR-005; CA-01)
- [x] [RF-F119] Mailbox administration — provisioning, quotas, suspension, cascade delete (OB-5 — `3ef6291`, ADR-005)
- [x] [RF-F120] DNS record automation: provider publish + drift re-verification (OB-6 W1 — `ee38ca8`, ADR-007; SF-61)
- [x] [RF-F121] ACME/SNI certificate lifecycle for the mail listeners (OB-6 W2 — `ee38ca8`, ADR-007; SF-33/34)
- [x] [RF-F123] Multi-replica cron leader election (OB-6 W6 — `ee38ca8`; GAP-03)

---

## Requirements

* Go 1.27+
* PostgreSQL
* Redis

---

## Quick Start

### Docker Compose

```bash
docker compose up -d
```

Access the dashboard:

```
http://localhost:9000
```

Default credentials:

```
Email: admin@example.com
Password: admin1234
```

---

### Local Development

```bash
git clone https://github.com/ReForwarder/ReForwarder.git
cd ReForwarder

make dev-deps
make dev
make dev-worker
```

---

## API Documentation

Every ReForwarder instance serves its own interactive API documentation:

* API Reference: `/docs` on your ReForwarder instance
* OpenAPI Spec: `/openapi.json` on your ReForwarder instance

Running locally with the default port, that is [http://localhost:9000/docs](http://localhost:9000/docs).
---

# Dashboard

ReForwarder includes a web dashboard for managing templates, SMTP servers, domains, contacts, API keys, and analytics.

<p align="center">
  <img src="docs/static/img/screenshots/dashboard.png" alt="ReForwarder Dashboard" width="900"/>
</p>

### Email Analytics

<p align="center">
  <img src="docs/static/img/screenshots/analytics.png" alt="Email Analytics" width="900"/>
</p>

### Emails

<p align="center">
  <img src="docs/static/img/screenshots/emails.png" alt="Emails" width="900"/>
</p>

### Admin Platform Metrics

<p align="center">
  <img src="docs/static/img/screenshots/admin-platform-metrics.png" alt="Admin Platform Metrics" width="900"/>
</p>

---

## SDKs

The official Posta SDKs are compatible with ReForwarder's API:

* Go: [https://github.com/goposta/posta-go](https://github.com/goposta/posta-go)
* PHP: [https://github.com/goposta/posta-php](https://github.com/goposta/posta-php)
* Java: [https://github.com/goposta/posta-java](https://github.com/goposta/posta-java)
* Node: [https://github.com/goposta/posta-node](https://github.com/goposta/posta-node)
* .NET: [https://github.com/goposta/posta-dotnet](https://github.com/goposta/posta-dotnet)

### Go Example

```go
package main

import (
    "fmt"
    "log"

    posta "github.com/goposta/posta-go"
)

func main() {
    client := posta.New("https://reforwarder.example.com", "your-api-key")

    resp, err := client.Emails.Send(&posta.SendEmailRequest{
        From:    "sender@example.com",
        To:      []string{"recipient@example.com"},
        Subject: "Hello from ReForwarder",
        HTML:    "<h1>Hello!</h1><p>This is a test email.</p>",
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Email sent: id=%s status=%s\n", resp.ID, resp.Status)
}
```

---

## Contributing

Contributions are welcome. Please open an issue before submitting a pull request.

---

## License

ReForwarder is free and open-source software licensed under the
GNU Affero General Public License v3.0 or later ([AGPL-3.0-or-later](LICENSE)).

## Copyright

Copyright (c) 2026 Jonas Kaninda and contributors — the original [Posta](https://github.com/goposta/posta) project.
ReForwarder changes and additions by the ReForwarder contributors.

---

## Acknowledgments

ReForwarder is a rebranded distribution built on top of the excellent work of the
Posta project and its community.

* Upstream project: [goposta/posta](https://github.com/goposta/posta)
* Author: [Jonas Kaninda](https://github.com/jkaninda)
* Support upstream: [Ko-fi](https://ko-fi.com/jkaninda)

If ReForwarder is useful to you, starring both the
[ReForwarder repository](https://github.com/ReForwarder/ReForwarder) and the
[upstream Posta repository](https://github.com/goposta/posta) is the best way to
say thanks.
