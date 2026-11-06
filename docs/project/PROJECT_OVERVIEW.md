# ReForwarder — Project Overview

> Companion documents: [`FEATURE_INVENTORY.md`](FEATURE_INVENTORY.md) (the 105-entry feature registry — *what exists*) and [`ARCHITECTURE_OVERVIEW.md`](ARCHITECTURE_OVERVIEW.md) (system architecture — *how it fits together*).

## Title & description

**ReForwarder** — a self-hosted, developer-first email platform for **sending, receiving, and relaying email through a unified API and SMTP**, with a built-in web dashboard. It is designed as a fully self-hostable alternative to services like SendGrid, Mailgun, and Postmark — giving operators complete ownership of their email infrastructure, data, and deliverability (source: `README.md` §Overview, lines 28–37).

It is a rebranded distribution built on the upstream [Posta](https://github.com/goposta/posta) project (AGPL-3.0-or-later; see `README.md` §Copyright/§Acknowledgments).

**Four product surfaces today:**

- **Outbound** — transactional and marketing email over operator-configured upstream SMTP, with templates, localization, campaigns, subscriber lists, bounce/complaint handling, and delivery analytics.
- **Inbound** — receive email at your domains (built-in SMTP receiver + provider webhook ingest), parse messages and attachments, forward structured payloads via webhooks.
- **Relay** — point an existing SMTP client at ReForwarder; mail flows through the same outbound pipeline as the API (migration path to the HTTP API at your own pace).
- **Forms** — a website contact form posts straight to ReForwarder (`POST /api/v1/f/{key}`); submissions are spam-scanned, stored, and answered from the dashboard. No auto-responder.

## Purpose and the Option B vision

The product direction was set in `docs/product-roadmap/upcomming-convo.md` (**Option B: Complete Email Infrastructure**): evolve ReForwarder from a relay-based email delivery and marketing platform into a **self-hosted email infrastructure and communication platform**. Six pillars (source: `upcomming-convo.md` §2, mirrored in `todo.md` §1):

| Pillar | Scope |
|---|---|
| **Outbound email** | SMTP, direct-to-MX, queues, delivery and IP pools |
| **Inbound email** | MX reception, routing, filtering and storage |
| **Hosted mailboxes** | Mailbox provisioning, IMAP, POP and webmail |
| **Email marketing** | Contacts, campaigns, automation and analytics |
| **Developer platform** | REST API, SMTP credentials, SDKs and webhooks |
| **Infrastructure security** | Abuse controls, reputation, DNS and TLS |

The vision must support **two distinct use cases without mixing their policies**:

1. **Email service provider** — applications and businesses send transactional and marketing email through the API or SMTP.
2. **Hosted email provider** — organisations create domains, mailboxes and users, then send and receive ordinary mailbox email.

They share domain management, authentication, delivery infrastructure and administration — but need **separate sending policies, storage rules, quotas and abuse controls** (`upcomming-convo.md` §2).

**How the vision is executed** (source: `todo.md`):

- **Track A — Option B programme** (architecture-first, Phases 0–6): re-audit + ADRs → outbound MTA foundation → inbound mail & routing → mailbox storage → IMAP/POP3 → webmail & mailbox admin → platform convergence. Deliberately **control-plane / data-plane**: the existing Go modular monolith + Vue dashboard remains the control plane; dedicated mail-data-plane components own protocols and message transport (see `ARCHITECTURE_OVERVIEW.md`).
- **Track B — the 52-item relay roadmap** (P0–P3): trust & correctness hardening, delivery-signal integrity, sender seam, marketing parity — retained alongside Track A; items marked `→ OB Phase n` fold into Track A phases when they arrive.

## Current platform capabilities

Per the feature registry (`FEATURE_INVENTORY.md`, snapshot HEAD `0f52636`, 2026-11-06): **74 Done · 9 In Progress · 22 To-dos** across 105 entries. Summary of what the platform has today:

**Identity & tenancy (auth-org, 16 Done + 3 In Progress)** — email/password auth with JWT sessions (revocation, device list), TOTP 2FA with recovery codes (completion in progress), workspace RBAC (owner/admin/editor/viewer) with pinned privilege-boundary tests, invitations, scoped workspace-bound API keys (expiry, CIDR allowlists, per-key rate limits and usage analytics, credential-transport restriction), OAuth/OIDC + workspace SSO, account lifecycle, admin user administration; WebAuthn passkeys and admin impersonation landed and in completion. The known P0 auth gap: the `two_factor_required` setting is seeded but never enforced at login.

**Domains & DNS (4 Done + 3 In Progress)** — domain CRUD with TXT-token ownership verification plus SPF/DKIM/DMARC presence checks, managed DKIM signing (go-msgauth, RSA/Ed25519) with full key lifecycle (generate/rotate/verify-dns/retire/delete), domain deliverability health endpoint with live DNS re-verification, strict verified-domain sending policy, and DMARC aggregate-report (rua) ingestion — the latter three in completion.

**SMTP relay, inbound & forms (10 Done + 1 To-do)** — default-off SMTP relay listener (:2526) with bcrypt relay credentials (legacy-hash migration, IP allowlists, CRUD API+UI), inbound SMTP receiver (:2525) + secret-protected webhook ingest + SSE streams, and the website-form→inbox surface with an 11-signal spam scanner. Per-domain inbound routing rules are the open item.

**Delivery, queues & bounce ops (13 Done + 1 In Progress + 3 To-dos)** — the full send pipeline (API → asynq/Redis queues with deterministic TaskIDs → worker SMTP delivery with dial timeouts, DKIM, atomic claims), three bounded retry layers plus `queue_reconcile` lost-task healing, campaign scheduling with an every-minute atomic-claim cron, admin queue administration with dead-letter inspection, a 14-class bounce classifier, provider-event ingestion (SES/SNS, SendGrid, Postmark, Mailgun) recording true `delivered` events, suppression with send-time enforcement, and webhook delivery history with operator replay. Open items: transactional scheduled-send cancel/reconcile (P0 GAP-01), legacy bounce-webhook parity (P0 GAP-02 residual), webhook test-fire, and classification-count analytics.

**Marketing & automation (14 Done + 6 To-dos)** — campaign lifecycle with hold-back A/B testing and winner selection, timezone-aware sending, guarded SegmentCondition segments (incl. live `in_segment` automation conditions), subscriber/list management with CSV import, versioned/localized templates with GrapesJS builder, a durable journey automation engine with a vue-flow visual builder and cron schedule triggers, RFC 8058 one-click unsubscribe, and workspace export/import. Open marketing cluster: double opt-in, signup forms, preference center, subscriber tags, UTM tagging, automation dry-run.

**Platform admin, security & ops (16 Done + 2 In Progress + 5 To-dos)** — admin platform core (users, workspaces, domains, plans, shared servers, events, metrics, announcements, updates), settings engine, scheduled-jobs visibility, the webhook engine (11-event registry, HMAC signatures, bounded exponential backoff, per-attempt history) behind a layered SSRF guard with optional pinned egress proxy, open/click/unsubscribe tracking, analytics APIs (daily series, latency percentiles, provider breakdown), a clean-room fail2ban security engine with sliding-window detectors and admin surface, AES-256-GCM credential encryption, retention cron + GDPR tooling, `pg_dump` backup cron with restore runbook, Prometheus metrics + health probes, feature-flag system-info gate and a route contract gate (both in completion). Open: security-events retention caller, admin-console IP allowlist, error-monitoring hook, delivered-state emission parity, tracking-toggle symmetry.

**Dashboard integration (1 Done + 4 To-dos)** — the Vue dashboard is contract-gated against the backend route golden file; known dead surfaces (Contact Lists pages, three dead admin client methods, four backend-endpoint-without-UI cases, backend-only campaign duplication) are catalogued for cleanup.

## Technology stack

| Layer | Technology | Source |
|---|---|---|
| Backend | Go 1.27, okapi HTTP framework (typed request/response docs, generated OpenAPI + Scalar) | `go.mod`; Audit §2.2 |
| Database | PostgreSQL 17 (system of record), GORM AutoMigrate + versioned upgrade steps | `compose.yml`; Audit §2.4 |
| Queue & cache | Redis 7 + asynq (weighted queues), Redis for cache/limits/security-windows/sessions/nonces/heartbeats/SSE pub-sub | `compose.yml`; `06-architecture-impact.md` §6 |
| Scheduling | robfig/cron (10 registered jobs, visible at `/admin/crons`) | Audit §2.8 |
| SMTP / mail | emersion/go-smtp (inbound + relay), net/smtp relay client, emersion/go-msgauth (DKIM) | `go.mod`; Audit §2.6 |
| Frontend | Vue 3.5 + TypeScript + Vite + Pinia, embedded into the Go binary at build (3-stage Dockerfile); CodeMirror 6, GrapesJS, @vue-flow; hand-rolled SVG charts | Audit §2.3 |
| Observability | Prometheus client (20+ metric families, default-off), `/healthz` `/readyz`, worker Redis heartbeats | Audit §2.2, §3.J |
| Storage | Blob store abstraction: filesystem or S3-compatible (attachments, raw inbound, DMARC report attachments) | `README.md` §Architecture; Audit §5 |
| Security | AES-256-GCM crypto service, fail2ban-style security engine, layered SSRF guard, JWT + `psk_` API keys | Audit §2.9, §6.1 |
| Deployment | Docker Compose (server + worker + postgres + redis with healthchecks), CI with real PG/Redis service containers, multi-arch release images | Audit §4.3, §5 |

Process topology: a single binary family (`cmd/posta`) with `server` (HTTP API + dashboard + SMTP listeners + cron + optional embedded worker), `worker` (standalone asynq consumer with its own health/metrics ports), and `doctor` (workspace-integrity checker). Both modes are stateless; all state lives in PostgreSQL, Redis and object storage (source: `README.md` §Architecture).

## Documentation & board links

**In-repo docs**

- `README.md` — product overview, quick start, architecture diagram, stack.
- `FEATURE_INVENTORY.md` / `ARCHITECTURE_OVERVIEW.md` — this overview's companions (current-state registry and architecture summary).
- `docs/product-roadmap/README.md` — index of the 7-document research suite: `01-oss-feature-inventory.md` (199-ID registry, 12 competitors), `02-competitor-comparison.md`, `03-existing-feature-audit.md` (evidence audit), `04-feature-gap-analysis.md` (53 gaps, D1–D9), `05-implementation-roadmap.md` (Track B source), `06-architecture-impact.md` (AS-IS/TO-BE architecture, risks R1–R16), `upcomming-convo.md` (Track A source, Option B).
- `todo.md` — master todo consolidating Track A (Option B phases 0–6) and Track B (52 relay-roadmap items), with the already-delivered record and decision gates.
- `docs/implementation/ROADMAP_IMPLEMENTATION.md` — implementation tracker with per-item notes and tests for R-01…R-28.
- Hosted docs site: Docusaurus sources under `docs/docs/**` (deployed by `.github/workflows/deploy-docs.yml`); every instance serves its own API reference at `/docs` and OpenAPI spec at `/openapi.json` (`README.md` §API Documentation).

**Board & repo**

- Repository: `https://github.com/ReForwarder/ReForwarder` (issues CLI available; CI badge in `README.md`).
- Project board: **GitHub Project #1**. The GraphQL API for the project is **unavailable in this environment** — the token lacks `read:project`/`project` scopes. To enable scripted board access, run `gh auth refresh -s read:project,project` and re-authenticate; until then, board synchronization (e.g. moving registry rows to the board) is a **manual procedure**: open the project board in the browser and mirror the RF-F statuses from `FEATURE_INVENTORY.md` by hand.

## Sources

- `README.md` (product description, surfaces, stack, links).
- `docs/product-roadmap/upcomming-convo.md` §2–§3 and `todo.md` §1–§2 (Option B vision, use cases, Track A/B).
- `FEATURE_INVENTORY.md` (capability summary — statuses and counts from the RF-F registry view supplied with this task).
- `docs/product-roadmap/03-existing-feature-audit.md` §2 (stack detail) and `docs/implementation/ROADMAP_IMPLEMENTATION.md` (recent work).
- Board status: orchestrator-reported GitHub state in this task ("issues CLI available; Project #1 GraphQL UNAVAILABLE — token lacks read:project/project scopes").
