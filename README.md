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

Planning details live in `docs/project/`: [FEATURE_INVENTORY.md](docs/project/FEATURE_INVENTORY.md) is the detailed registry, [BACKLOG.md](docs/project/BACKLOG.md) the operational backlog, and the [ReForwarder Backlog](https://github.com/ReForwarder/ReForwarder/projects) board the task board.

### To-dos

- [ ] [RF-F023] DNS record guidance depth (SPF/DMARC parsing, MX records, drift re-verification)
- [ ] [RF-F029] Custom tracking domain
- [ ] [RF-F040] Per-domain inbound routing rules (alias/catch-all/forward targets)
- [ ] [RF-F048] Transactional scheduled send (API send_at) — broken end-to-end
- [ ] [RF-F056] Webhook test-fire endpoint
- [ ] [RF-F057] Bounce classification analytics surface (classification counts)
- [ ] [RF-F061] UTM auto-tagging of campaign links
- [ ] [RF-F071] Automation workflow dry-run / test tooling
- [ ] [RF-F073] Preference center (multi-list subscription management)
- [ ] [RF-F074] Subscriber signup forms (form-to-subscriber capture)
- [ ] [RF-F075] Double opt-in (DOI) confirmation flow
- [ ] [RF-F076] Subscriber tags
- [ ] [RF-F091] Security-events retention enforcement
- [ ] [RF-F092] Admin-console IP allowlisting
- [ ] [RF-F097] Error-monitoring hook
- [ ] [RF-F099] Delivered-state analytics, email.delivered emission, and dead delivered UI paths
- [ ] [RF-F100] Tracking toggle symmetry
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
- [ ] [RF-B120] Option B Phase 3 — mailbox & storage foundation (ADR-003): storage core landed in wave W1 (2026-11-07) — Postgres mailstore models + migration, append/fetch/copy/move/flags-CAS/expunge service, blob ref-counting seam, concurrent-access suites; quotas + retention landed in wave W4 (2026-11-07) — full ADR-003 §5 quota enforcement (per-mailbox bytes + message count + workspace aggregate ceiling, lock-and-check in the append tx, typed `*QuotaError`) and the §7 retention sweep in the unified cron (per-mailbox/per-folder age+count policies, counters synced, blob refs at ≤0 reclaimed with their objects); Postgres FTS search landed in wave W3 (2026-11-07) — `mailstore.PostgresSearcher` over the `search_tsv` generated column (websearch syntax, weighted subject/from/to/body ranking, per-user isolation + folder scoping, paged ranked ids), plus the admin-gated `POST /admin/mailstore/search/reindex` raw-SQL rebuild; the content-addressed encrypted blob layer landed in wave W2 (2026-11-07) — `mailstore.ZstdBlobCodec` (zstd >4 KiB + AES-256-GCM via the crypto service, SHA-256 content addressing, SF-78/SF-83), reserve-then-write append under a per-hash advisory lock, grace-perioded ref-count GC with the audited `mail_blob_gc` cron event, and dedup-aware per-mailbox blob-byte usage reporting; the ingestion seam (flow F13) landed in wave W5 (2026-11-08) — inbound messages whose matched route action is `deliver` file into the recipient's hosted INBOX via `mailstore.Append` (`internal/services/inbound/mailbox_delivery.go`, wired on both ingest paths), auto-provisioning the recipient mailbox on first delivery to a verified domain bound to the tenant user (`POSTA_MAILSTORE_AUTO_PROVISION`, default true; off → archive only), quota-enforced with over-quota deliveries quarantined under the typed `mailbox_quota_exceeded` reason (release re-runs routing and re-delivers); no mailbox → archive-only exactly as before; transactional `Email`/subscriber paths untouched; backup/restore tooling and orphan-object enumeration remain

- [ ] [RF-B121] Option B Phase 4 — IMAP and POP3 access (ADR-004): wave W1, the protocol-access seam, landed (2026-11-08) — `mailstore.FetchRange` (sequence/UID sets with the RFC 9051 `*` semantics + BODY section extraction from the content-addressed blobs: full body, HEADER, HEADER.FIELDS(+.NOT) with folded continuations, TEXT, arbitrary MIME part paths, `<start.count>` partials, non-peek body fetches set `\Seen`, per-request batch cap) with the MIME structure tree for BODY/BODYSTRUCTURE rendering (`internal/services/mailstore/fetchrange.go`, `seqset.go`, `mimesection.go`); `mailstore.SearchIMAP` translating the IMAP SEARCH keys onto SQL predicates (flags/dates/sizes/header columns pushed down for pure conjunctions) and the Postgres FTS searcher for BODY/TEXT, with bounded raw-header scans for BCC/HEADER (`search_imap.go`); `mailstore.StatusCounts` (messages/unseen/recent, one round-trip); `mailstore.ConcurrentSessionGuard` per-folder write locking wired into Expunge; `mailstore.ChangeNotifier` for IDLE (in-process per-folder pub/sub + Redis fan-out on `posta:mailchg` after the ban-propagation pattern, emitting from Append/Expunge/flags/Move/Copy); `mailauth.Authenticator.AuthenticateWithAppPassword` — bcrypt verification against app-password digests only (revoked/missing/wrong all read as one generic failure; the primary account password is structurally never accepted); `mailstore.MailSession` SELECT/EXAMINE state; ~45 named tests incl. the concurrent append+expunge `-race` suite and real-Postgres PG-gated siblings; the IMAP/POP3 server packages, listener/TLS/limit/audit wiring and the client-compatibility matrix are the remaining OB-4 waves

- [ ] [RF-B121] Option B Phase 4 — IMAP and POP3 access (ADR-004), continued: waves W2/W3/W4 landed (2026-11-08) — the IMAP4rev1 server (`internal/services/imap/`: the ADR-004 command subset over the W1 seam with STARTTLS, IDLE, mod_seq-CAS STORE, audit sink `imap.auth.success|failure` + `imap.select|append|store|expunge|...`) and the POP3 server (`internal/services/pop3/`: classic command set with STLS, session-scoped mark-then-expunge-on-QUIT deletion that leaves IMAP UIDs intact, audit logger `pop3.auth.*|pop3.session.end`), both app-password-only via the W1 mailauth seam and gated listener-wired on ALL FOUR ports with graceful shutdown registration in `cmd/posta/server.go` (`startIMAPServer` 143 + 993 via CountingListener→TLSServerListener, `startPOP3Server` 110 + 995 same stack); the ops layer (W4): `POSTA_IMAP_*`/`POSTA_POP3_*` config keys with validation enforcing the non-negotiables (TLS on any non-loopback bind, certificates mandatory on 993/995, mandatory per-user/per-IP/command-rate limits, port-collision matrix), security-engine limiter integration per SF-44 (CountingListener ban-drop + connect/abort signals, TLSServerListener TLS-probing, SessionGuard auth-failure/malformed feeds), audit events into the events substrate per SF-74, commented default-off `compose.override.yml` block, operator page `docs/docs/security/mail-protocols.md`; tests: the 19-case config validation matrix + env-parsing + loopback-variants (`internal/config/mail_protocols_test.go`), IMAP audit-emission and listener-lifecycle/limiter suites (`internal/services/imap/audit_test.go`, `lifecycle_test.go` — W2 shipped none), POP3 protocol matrix (W3's `pop3_test.go`); two cross-wave repairs recorded on `internal/services/imap/session.go` (uninitialized session writer — every session panicked on the greeting — and a shutdown-vs-teardown data race in `session.close`) with W2 self-repairing an invalid ternary in `commands.go:998` that broke the tree build; open OB-4 items: UIDVALIDITY-driven folder/message sync and the standard-client compatibility matrix (W5 brief) ; W2 evidence addendum (2026-11-08): the IMAP server's scripted-client command matrix landed (`internal/services/imap/harness_test.go` + `server_test.go` + `parser_test.go` — 22 harness tests + 15 units over an in-process real-TCP listener, real Postgres via TEST_DATABASE_DSN, real bcrypt app-password fixtures) covering the ADR-004 matrix end to end (lifecycle, unauthenticated refusal, app-password-only auth with the primary password refused, AUTHENTICATE PLAIN/LOGIN, STARTTLS enforcement + upgrade, SELECT/EXAMINE, APPEND + FETCH body/section/peek/partial byte-exactness, STORE ±FLAGS/.SILENT with mod_seq-CAS conflicts as tagged NO, COPY/MOVE UIDVALIDITY, the SEARCH key set incl. FTS BODY/TEXT, EXPUNGE/CLOSE, IDLE delivery, over-quota APPEND, session/command/IP limits, literal-cap rejection); checks: package suite `-race` with the DSN ok 33.326s, the named battery verbatim exit 0, and the battery re-run WITH TEST_DATABASE_DSN (postgres:17-alpine) exit 0 (imap ok 28.384s, mailstore ok 22.277s) — the harness drove eight real server fixes recorded in the STATUS_HISTORY W2 note

- [ ] [RF-B122] Option B Phase 5 — Webmail and mailbox administration (ADR-005): wave W2, the mailbox-administration slice, landed (2026-11-08) — the admin-gated REST surface over the mailstore (ADR-005 decision 3, admin slices of SF-65/72/80): `GET/POST /admin/mailboxes` (paged platform-wide table with owner identity + the mailstore's counters, filterable by name/owner/workspace/status; provisioning for a user with byte+message-count quotas, optional workspace binding validated against ADR-008's mailbox → user → workspace membership chain, 409 on the (owner,name) unique conflict), `PATCH /admin/mailboxes/{id}` (quota change and/or suspend/reactivate; closed deliberately not admin-settable — deletion is the removal path), `DELETE /admin/mailboxes/{id}` (cascade through the ADR-008 ownership chain — folders, messages and blob ref counts in one transaction, objects left for the GC sweep; the dashboard confirms before calling), `GET /admin/mailboxes/{id}/usage` (GAP-45 counters: quota basis raw bytes/message count, storage basis dedup-aware blob bytes + distinct blobs); every mutating action audited as `admin.mailbox.provisioned|updated|suspended|reactivated|deleted` (SF-74); routes answer 503 when the mailstore's blob store/encryption key are unconfigured (same preconditions as inbound mailbox delivery). Service layer: `internal/services/mailstore/admin.go` (`AdminListMailboxes` paged+filtered with escaped ILIKE, `AdminGetMailbox`, `AdminProvisionMailbox`, `AdminSetMailboxQuota`, `AdminSetMailboxStatus`, `AdminDeleteMailbox`, `AdminMailboxUsage` — every mutation reuses the owner-scoped machinery underneath so purge/ref-count/counter code cannot drift between admin and user paths); handler `internal/handlers/admin_mailbox_handler.go` behind a narrow `AdminMailboxStore` seam; wiring `internal/routes/routes.go` + the five route definitions in `internal/routes/admin_routes.go` (minimal-additive; golden API contract regenerated). Admin UI: `web/src/views/admin/Mailboxes.vue` (table with usage bars + provision modal + usage-informed quota-edit modal + suspend/reactivate + confirm-gated delete, Users.vue idiom), route `admin/mailboxes` in `web/src/router/index.ts`, nav entry in `DashboardLayout.vue`, client `web/src/api/mailboxes.ts`. Tests: `internal/services/mailstore/admin_test.go` (service battery over the fake driver: filters/paging/total, cross-owner reach while the owner-scoped paths still refuse, provision conflict/negative quotas, quota change honored by the append path, suspend blocks appends / reactivate restores, closed refused, admin cascade delete with blob ref decrements, usage report), `internal/handlers/admin_mailbox_handler_test.go` (real-middleware battery: 401/403 gating on all five routes, envelope/paging/filters/owner enrichment, provisioning validation 404/400/409, patch audit trio, delete audit with final counters, usage endpoint), `internal/routes/admin_mailbox_routes_test.go` (route registration + gate through the real `adminRoutes()`), `web/src/views/admin/Mailboxes.spec.ts` (7 vitest tests). Residuals: self-service quota/usage display is the W1 frontend's scope per the wave split; user-facing webmail surfaces are the other ADR-005 waves; visual acceptance screenshots remain the Phase 7 gate.

- [ ] [RF-B122] Option B Phase 5 — Webmail and mailbox administration (ADR-005), continued: wave W3, the compose/send/scan seam, landed (2026-11-08) — `POST /mail/attachments` (multipart upload staged into the content-addressed encrypted blob layer with a per-user staging row; ADR-006 malware scan at upload via the deployment's clamd config: a positive is refused 409 carrying the signature and nothing is stored, an unavailable scanner is refused under a closed fail mode and accepted unscanned under the open default), `POST /mail/send` (the Sent copy is filed through the quota-enforced `mailstore.Append` into the mailbox's Sent folder — auto-provisioned by the lowercased From address, inbound-seam convention — and the transport copy enters the standard outbound pipeline via `email.SendMailCompose` with provenance `mail-compose`: ADR-001 direct/relay per sending domain, DKIM-signed at delivery, attempts recorded by the delivering worker; verified sender domain enforced per ADR-001 non-negotiable 1; bcc is envelope-only and never appears on stored headers; the compose mints the shared `Message-ID` + `X-Posta-Email-Uuid` linkage and the `composed` email_events event opens the timeline), the draft lifecycle (`POST /mail/drafts` stores a `\Draft` message in the Drafts folder, `PUT /mail/drafts/{uid}` replaces it preserving the Message-ID via append + mark-`\Deleted` + expunge, `POST /mail/drafts/{uid}/send` sends the STORED content and deletes the draft only after an accepted send), `GET /mail/messages/{uid}/reply-frame` (reply/reply-all/forward prefill: Reply-To target, self-excluding reply-all Cc, Re:/Fwd: guards, In-Reply-To/References, bounded quoted text, original HTML for the frontend's sandboxed remote-content-blocked renderer), `GET /mail/messages/{uid}/delivery-status` (the Sent copy's linkage header keys the outbound row's email_events timeline — delivered only ever from provider/MX truth per ADR-001); delivery status is user-scoped on both legs (mailstore folder scoping + the Email row's owner); service `internal/services/mailcompose/` over narrow seams (`Mailstore`/`Sender`/`Scanner`/`StagingStore`/`DeliveryStore`), mailstore helpers `internal/services/mailstore/compose.go` (`GetMessageByUID`, `ReserveBlob`/`ReleaseBlob` with the Append path's per-hash-lock reservation, `ReadBlob`, `SweepStaleAttachmentUploads`), staging model `models.MailAttachmentUpload` (additive AutoMigrate in `internal/storage/migration/migration.go`), new event constant `models.EmailEventComposed` + `emailhistory.RecordComposed`, handler `internal/handlers/mail_compose_handler.go` (JWT-only; typed error mapping — infected/quota 409 with signature/scope detail, staging gate 400, ownership/existence 404, fail-closed scanner 503; unconfigured mailstore → 503 like the webauthn convention), routes `internal/routes/mail_compose_routes.go` registered minimal-additively in `routes.go`, typed client `web/src/api/mailCompose.ts`. Tests: `internal/services/mailcompose/service_test.go` (14 named tests — the happy path proving Sent storage + outbound enqueue + shared identity + bcc never leaking into the stored raw, unstaged-ref refusal, infected refusal with signature, fail-closed/fail-open scanner, quota-refusal ordering with nothing enqueued, the draft lifecycle with Message-ID preservation and deletion-on-acceptance, refused-send keeps the draft, the reply-frame matrix, delivery-status + unlinked→404, cross-user isolation, auto-provision on/off, upload dedup), `clamd_e2e_test.go` (the real ClamdClient against an in-process fake clamd over INSTREAM — EICAR refused with the signature), `service_pg_test.go` (real-Postgres: the real quota enforcer refusing a compose with nothing enqueued, the upload→draft→replace→send refcount chain ending at exactly the Sent copy's one reference, replace-expunge counter bookkeeping, suspended-mailbox refusal), `internal/services/email/compose_send_test.go` (bound-value proofs: provenance stamp, linkage headers, composed event, dev store-without-send, broken-input refusals), `internal/handlers/mail_compose_handler_test.go` (real-middleware HTTP battery). Residuals: threading headers (In-Reply-To/References) are returned by the reply frame but not accepted by the send schema (the ask's reply framing is prefill-only); the compose UI rides the webmail-frontend wave; staged uploads that never reach a message pin their blob until the 7-day TTL sweep; no marketing-rate-limit consumption on compose (ADR-005 decision 4; per-user caps ride the future submission listener per ADR-001 §10).

- [ ] [RF-B122] Option B Phase 5 — Webmail and mailbox administration (ADR-005), continued: wave W1, the webmail REST surface, landed (2026-11-08) — the session-user-scoped `/api/v1/mail/*` surface (ADR-005 decision 1) over the mailstore: `GET /mail/mailboxes` (the user's mailboxes with the GAP-45/ADR-003 §5 quota and usage counters — quota basis raw bytes/message count plus dedup-aware blob bytes), `GET /mail/folders?mailbox=` (folders with live total/unseen counters), `POST /mail/folders` (create under an optional parent IMAP path), `PATCH /mail/folders/{id}` (rename with descendant-path rewrite and/or subscribe toggle; INBOX refused), `DELETE /mail/folders/{id}` (subtree cascade), `POST /mail/folders/{id}/expunge` (purge `\Deleted`, survivors keep UIDs), `GET /mail/messages?folder=&page=&size=&unread=&flagged=&q=` (paged `PageableResponse` in UID order; unread/flagged pushed into the listing SQL; `q` via the ADR-003 FTS — flag filters combine truthfully through the `SearchIMAP` tree), `GET /mail/messages/{uid}?folder=` (metadata + attachment list + body: sanitized HTML when present else plain text; marks `\Seen` like an IMAP non-peek BODY fetch), `GET /mail/messages/{uid}/attachments/{index}` (stored bytes with the ingested content type, always `Content-Disposition: attachment` + `nosniff` — a text/html attachment is forced to `application/octet-stream`), `POST /mail/messages/{uid}/flags` (seen/flagged/answered/draft, optional `if_mod_seq` CAS → 409 on a lost race), `POST /mail/messages/{uid}/move|copy` (same-mailbox target folder), `DELETE /mail/messages/{uid}` (`\Deleted`; rows leave at folder expunge), `POST /mail/search` (mailbox-wide or folder-narrowed FTS, ranked paged). Scoping is strictly per session user — the mailstore enforces ownership below every call and every foreign resource reads as 404 (thirteen-operation isolation test); quota rejections surface as 422 carrying the typed `*QuotaError` scope detail; a deployment without the blob store or `POSTA_ENCRYPTION_KEY` answers 503 on every endpoint (the webauthn/webpush convention). Mail-view HTML is sanitized server-side (`internal/handlers/mail_html.go`, ADR-005 decision 5: scripts, event handlers, forms and dangerous elements dropped, remote content blocked by default — image sources only `data:image/*`, `@import`/remote `url()` in CSS neutralized) for rendering in the dashboard's `sandbox=""` iframe (the Logs content-iframe pattern — the iframe alone does not block network fetches, so the block happens before it). Mailstore session-level helpers (`internal/services/mailstore/webmail.go`, ADR-005-sanctioned): `GetFolderByID`, order-preserving ownership-re-verifying `GetMessagesByIDs`, plain FTS `Search` (503 when unwired). Routes `internal/routes/mail_routes.go` — fourteen definitions, `jwtOnly` + optional workspace like `/users/me`, minimal-additive registration; golden API contract regenerated. Tests: `internal/handlers/mail_handler_test.go` over the fake-driver harness (`mail_fake_driver_test.go`, the mailstore fakedb pattern + `IdentityBlobCodec` fake codec) — mailboxes usage, folder CRUD matrix, list pagination/filters, sanitized view + attachments, thirteen-operation cross-user isolation, flag CAS, quota-rejection surfacing, attachment authz/content-type, move/copy/delete/expunge, list+search matrix, nil-store 503, missing params; `internal/handlers/mail_html_test.go` (5 sanitizer tests); `internal/routes/mail_routes_test.go` (route set + gate). Named battery green (docker golang:1.27 `go build ./... && go test -count=1 -race ./internal/...` — 49 packages ok, zero FAIL); `npm --prefix web run type-check` exit 0 and `npm --prefix web run test` 89/89. Residuals: the user-facing webmail UI (mailbox/message/compose views) is the webmail-frontend wave's scope; list endpoint query names (`folder=`, `mailbox=`) differ from the W3 compose routes' (`folder_id=`, `mailbox_id=`) — both documented for the frontend client; search flag-combination paginates the searcher's UID-ascending hits in Go (bounded by folder size); FTS ranking itself is proven by the mailstore package's real-Postgres suite (OB-3 W3), this wave's battery runs the plumbing over a fake searcher; visual acceptance screenshots remain the Phase 7 gate.

- [ ] [RF-B110] Option B Phase 6 — convergence (ADR-008/009), wave W4 — MTA-STS + TLS-RPT (SF-29/31, ADR-007 §5) landed (2026-11-11): **MTA-STS policy hosting** — GET `/.well-known/mta-sts.txt` on the existing HTTP server (`internal/handlers/mta_sts_policy_handler.go`, Host-header-driven per RFC 8461 §3.3, gated by `POSTA_MTA_STS_ENABLED` default off), rendering the domain's opted-in mode (`domains.mta_sts_mode` additive column, pinned column name; `PATCH /domains/{id}/mta-sts` requireEdit/ownsResource/verified-fail-closed/audited) with the live MX list (60s cache) and 404 on any mismatch (wrong host, unknown/unverified domain, no opt-in, no resolvable MX); **outbound enforcement** — `internal/services/mta/mtasts.go` (TXT parse, strict policy parser, HTTPS fetch with 64 KiB cap + verification always on, `PolicyClient` TTL cache with negative caching, fall-open on discovery failure per RFC 8461 §6.1) behind the small `STSEnforcement` interface consumed by the session runner (`session.go`: non-policy MX host or missing STARTTLS under mode:enforce → 421 STS transients; testing/none never block; smarthost exempt; recipient domain re-derived from the validated recipients), armed process-wide from `newMTAExecutor` — deliver.go's core loop untouched (pool wave owns it; the process-default seam makes the integration line unnecessary); relay path untouched by construction; **TLS-RPT inbound** per the ADR-002 pattern — dedicated RCPT allowlist rua address `tlsrpt@<verified-domain>` (system address, no routes/suppression), durable-first system ingest, RFC 8460 JSON parser (gzip + 16 MiB caps), idempotent `tls_rpt_reports` keyed (user_id, report_id, policy_domain), read surface `GET /domains/{id}/tls-rpt` (workspace-scoped); **TLS-RPT outbound** (SF-104 slice) — daily cron aggregates the previous UTC day's direct sessions per policy domain, addresses reports ONLY to rua endpoints read live from each remote domain's `_smtp._tls` TXT, sends via the system pipeline as gzipped `application/tlsrpt+json`; inert without `POSTA_MTA_STS_ORG` and on quiet days. DNS guidance (`v=TLSRPTv1; rua=mailto:tlsrpt@<domain>` + the mta-sts A record) added to the domain records surface. Route contract regenerated (3 new lines). Tests: mtasts parse/render/match/cache/enforcer + session-runner enforce matrix + hosting/read handler suites + tlsrpt parse/idempotency/caps + aggregator attribution + cron-job policy matrix (named in the OB-6 W4 evidence section) — targeted `-race` runs green; frontend type-check exit 0, vitest 122/122. Residuals: per-attempt TLS outcome columns (counts are session-scoped, documented), STS-fetch-error reporting, dashboard toggle/view. Wave evidence: the OB-6 W4 section of `docs/audits/FEATURE_PARITY_AUDIT.md`.

- [ ] [RF-B110] Option B Phase 6 — convergence (ADR-008/009), wave W6 scope landed (2026-11-11) — **GAP-03 closed**: Redis `SET NX PX` cron leader lease (`internal/cron/lease.go` + `RunOnce` gate in `internal/cron/manager.go`; per-recipient/day dedup claims on `bounce_alert`/`daily_report` with clear-on-failure; `POSTA_CRON_LEADER_LEASE_ENABLED` default off + `POSTA_CRON_LEASE_TTL_SECONDS`; atomic Lua holder-scoped acquire-or-renew, fail-closed lease / fail-open claims), tests: 8 unit tests `internal/cron/lease_test.go` PASS `-race`, config off-by-default proof (`TestCronLeaderLeaseOffByDefault`), and the two-manager acceptance integration test executed against a real Redis (PASS; skips without `POSTA_TEST_REDIS_URL`). **GAP-49 closed**: `docs/docs/operations/redis-ha.md` (per-role fail-open/fail-closed matrix citing verified callsites; managed-Redis replication guidance; Postgres queue-durability backstop; drills; multi-replica checklist) + `compose.sentinel.yml` with the failover drill executed end-to-end (quorum consensus → primary stop → replica promoted → old primary rejoined as replica; hostname-monitoring caveat recorded). **Tenancy revisit decided** (`docs/architecture/TENANCY-DECISION.md`): SF-66/71/72 deferred with prerequisites per ADR-008 §6 — nothing ships without a reviewed migration plan + demand evidence; no half-tenantancy. **Evaluations produced** (`docs/architecture/EVALUATIONS.md`): JMAP RF-B126/SF-84 (demand absence recorded, not-now), groupware SF-111 + SF-112 FileNode (not-now; extraction-for-search first slice if demand appears) — evaluations, not commitments. **Deployment docs**: `compose.yml` commented default-off listener blocks (submission/IMAP/POP3/ACME + lease envs; `docker compose config` OK), `docs/docs/operations/deployment.md` (topology, additive migrations, listener matrix, scaling, upgrades), docs sidebars Operations category, docusaurus build green. Wave evidence: the OB-6 W6 section of `docs/audits/FEATURE_PARITY_AUDIT.md`.

- [ ] [RF-B110] Option B Phase 6 — convergence (ADR-008/009), wave W5 — observability completion (SF-93 OTLP slice, SF-96, GAP-51/52) landed (2026-11-11): **SF-93 OTLP push exporter** (`internal/observability/otlp.go`) ships the existing Prometheus families to `POSTA_OTLP_ENDPOINT` (default OFF — empty endpoint ships nothing; `POSTA_OTLP_INTERVAL_SECS` default 60; `POSTA_OTLP_HEADERS` for collector auth) as OTLP/HTTP protobuf (`/v1/metrics`, counters→Sum monotonic+cumulative, gauges→Gauge, histograms→Histogram with the derived +Inf bucket, summaries→Summary, resource `service.name=posta`+version) — encoded with the standard library only (`encoding/binary` wire writer; the OpenTelemetry exporter/SDK dependency was deliberately NOT added, choice recorded in the package doc + ADR-009 ledger), wired in both entrypoints (server OnStart + shutdown; worker defer); **SF-96 metric-alert engine** (`internal/services/meticalert/engine.go` + `internal/cron/jobs/metric_alert.go`, every minute): rules (metric, label filter, gt/gte/lt/lte/eq/neq, threshold, `duration_seconds`, enabled, optional webhook URL+secret, optional email) stored as JSON in the admin-gated setting `metric_alert_rules` (no new routes — contract untouched), fire-once-per-episode after the hold → `metric_alert.fired` platform event + HMAC-signed webhook (shared `webhook.SignBody` scheme through `webhook.ValidateWebhookURL` + guarded transport — the SSRF guard reused, not weakened) + optional email; clearing resolves (`metric_alert.resolved`); **GAP-51 error hook** (`internal/observability/errorhook.go` + `internal/middlewares/panic_capture.go`): `POSTA_ERROR_WEBHOOK_URL` (default off) + optional secret ships panics (HTTP middleware re-panics, behavior unchanged) and fatals (sync 3s cap before every `logger.Fatal` in `cmd/posta`) as `runtime.error` events + best-effort `runtime.panic/fatal` platform events; **GAP-52** `docs/observability/grafana-posta-overview.json` (17 panels over the real families, datasource variable) + `docs/observability/README.md` (inventory, OTLP, alert-rule format, hook, import). No schema change (rules live in the settings table). Tests: TestOTLPExportPayloadShape (httptest collector + wire-level decode), TestOTLPExporterDisabledWithoutEndpoint, TestOTLPExporterRejectsBadEndpoint, TestOTLPExportNon2xxIsError, TestParseOTLPHeaders, TestErrorHookEmitsPanicEvent/FatalEventSynchronously/StoreFailureDoesNotBlockDelivery, TestErrorHookDisabledWithoutURL, TestCaptureGlobalFatalWithoutHookIsNoOp, TestGrafanaDashboardJSONIsValid/CoversCoreFamilies, TestAlertMatrixConditions (13-case fire/no-fire table), TestAlertDurationGate, TestAlertLabelFilterMismatch, TestAlertCounterGaugeAndAbsentMetrics, TestAlertSkippedHistogramMetric, TestAlertDisabledAndRemovedRules, TestAlertFireActionsEventWebhookEmail (event+webhook+HMAC+email), TestAlertResolveWebhook, TestAlertInvalidRulesAreReported, TestMetricAlertJobImplementsCronJob/RunsEngine, TestObservabilityConfigDefaultsOff — all `-race` green; the named battery ran 63 packages ok with a single concurrent-wave artifact (`internal/handlers` vet failed on another wave's scratch file deleted mid-run — reported, not this wave's code). Frontend: type-check exit 0; vitest 121/122 with the single failure being another wave's uncommitted `domains.ts` ip-pool/warmup/reputation client calls (not this wave's files). Residuals: alert breach-duration state is in-memory (restart restarts a timer); histogram/summary families not alertable; OTLP `start_time_unix_nano=0`; traces not adopted (CA-07 residual); RF-F097's Sentry-DSN + `posta_errors_total` superseded by the webhook hook (recorded). Wave evidence: the OB-6 W5 section of `docs/audits/FEATURE_PARITY_AUDIT.md`.

- [ ] [RF-B110] Option B Phase 6 — convergence (ADR-008/009), wave W1 — DNS automation (ADR-007 decision 1, SF-61, GAP-08 drift residual) landed (2026-11-11): the `DNSProvider` interface (`PublishRecordSet`, `ReadRecords`, `DeleteRecord`) in `internal/services/dnsmanager/provider.go` with three adapters — Cloudflare REST v4 (`cloudflare.go`, bearer-token, per-record POST/PUT upsert), AWS Route53 (`route53.go`, local SigV4 signing with zero new dependencies, ONE atomic ChangeBatch per publish — values grouped by name+type and merged with unrelated existing records so an upsert never deletes the operator's own records) and DigitalOcean API v2 (`digitalocean.go`) — each validating access with a read BEFORE any write so a mis-scoped token fails with a typed error (`ErrUnauthorized`/`ErrZoneNotFound`/`ErrRateLimited`/`*PublishError` carrying the applied prefix) and produces no partial-zone writes. Publication planner (`planner.go`) computes the full set from the domain state: verification/SPF/DKIM/DMARC always (managed R-02 signing key's TXT via the `email.DnsTXTRecord` seam when an Active key exists, legacy upstream CNAME otherwise) and MX + return-path (bounce subdomain MX to the mail host) only on the explicit `POSTA_DNS_MAIL_ROUTING` opt-in and for ownership-verified domains — publishing an MX redirects mail RECEIPT, so it stays an operator choice (ADR-008 decision 2 fail-closed: publish refused outright for unverified domains). `Service.PublishDomainRecords` ALWAYS runs the existing R-07 verification flow after the publish and persists the fresh check state on the domain (the diagnose-endpoint semantics); the publication baseline lands on the new `dns_publication_state` table for drift comparison. Credentials: stored encrypted (crypto AES-256-GCM, SF-78), validated with a read-only zone check at connect, masked in every response (`MaskCredential` first4…last4), per-domain revocation (DELETE), audited (`domain.dns_provider.connected|revoked`, `domain.dns_records.published|publish_failed`). Drift re-verification cron `dns-drift-check` (hourly at :23, `internal/cron/jobs/dns_drift_check.go` + `Service.CheckDrift`/`driftFindings`) compares live provider records against the baseline (missing/changed findings; unrelated zone records are NOT drift), persists ok/drift/unknown, fires `domain.dns_drift_detected` / `domain.dns_drift_resolved` audit alerts on transitions, and a provider outage never masquerades as drift. API (minimal-additive, registered only when `POSTA_DNS_AUTOMATION_ENABLED` is set, default off per ADR-009 §8): `POST|GET|DELETE /domains/{id}/dns-provider`, `POST /domains/{id}/dns-records/publish`, `GET /domains/{id}/dns-drift` — workspace-scoped with the ownsResource/requireEdit gates and typed-error HTTP mapping (401/404/409/429/502); `DashboardFeatures.dns_automation` gates the panel. UI: Domains.vue DNS-automation panel (provider connect with provider-specific credential fields, masked connection + revoke, planned-record display with per-record copy, publish with confirm, drift badge + findings) + `web/src/api/domains.ts` client + types. DB: `dns_provider_credentials` + `dns_publication_state` (additive AutoMigrate, unique per domain, workspace ownership columns). Route contract regenerated (`docs/implementation/api_routes.txt`, 5 new routes). Tests: 44 dnsmanager tests (`provider`/`cloudflare`/`route53`/`digitalocean` fakes over httptest servers — upsert/idempotency/no-write-on-rejected-token/typed errors/atomic single batch/sibling-value preservation; planner completeness incl. all six kinds + gating; masking never leaking the raw token; connect/publish/drift flows with encrypted storage proofs; drift detection/resolution/outage), 8 handler tests over the real okapi stack (masking in responses, 404 tenant scoping, 409 unverified publish, 401/404 typed mapping, disconnect revocation), 3 drift-job tests (nil-inertness, multi-domain sweep with per-domain failure resilience, connected-scan set); `npm --prefix web run type-check` exit 0, `npm --prefix web run test` 122/122. Residuals: the other 8 of the reference platform's 11 DNS providers (adapters are additive behind the same interface); ACME/cert lifecycle (SF-34) and MTA-STS/TLS-RPT are separate OB-6 waves; drift checks run against the provider API (not direct DNS resolution) for connected domains only.

- [ ] [RF-B123] Option B Phase 6 — IP pools, warm-up controls, and reputation monitoring (ADR-007 decision 7), wave W3 landed (2026-11-11) — additive schema (`internal/models/ip_pool.go`: `ip_pools` workspace-or-shared scoped per ADR-008 §1, `ip_pool_members` (pool_id, ip) unique with enabled flag + PTR name, `ip_warmup_schedules` geometric ramp (start_volume, daily_multiplier, days) anchored at started_at, `ip_reputation_rollups` per (workspace, ip, domain, day) unique — transport counters on IP rows, sent/bounced/complained events on the domain-wide IP='' row; `domains.pool_id` + `mta_delivery_attempts.source_ip` additive columns), all registered additive in `internal/storage/migration/migration.go` (the OB-1 precedent: new tables/columns with defaults need no upgrade step). Pool selection: the executor round-robins the domain's pool member IPs per delivery (`internal/services/mta/deliver.go` `selectBindIP`, atomic counter, blank-entry skip, static-bind-IP fallback; every attempt row stamps `SourceIP` while `IP` stays the remote MX), resolved by the worker from the domain row (`internal/worker/handler.go` `deliverySpec`, `SetIPPoolRepo` seam; nil keeps the OB-1 behavior byte-for-byte) and wired in `cmd/posta/worker.go` + `cmd/posta/server.go`. Warm-up: shared leaf package `internal/services/warmup` (DayIndex/DailyCap/HourlyCap — pure, `now`-injected; multiplier<=0 flat, 1 msg/day floor, cap 0 after the ramp) consumed by both the MTA and the reputation surface; `internal/services/mta/policy_warmup.go` `EffectiveHourlyBudget` folds the ramp into the per-domain hourly budget (the cap binds even unlimited domains; after completion the policy budget stands), enforced through the existing budget gate in `Deliver` — exhaustion defers 450 with the attempt text naming `warm-up cap N/day`. Reputation: `internal/services/reputation/` aggregates `mta_delivery_attempts` + `email_events` per (ip, domain) into the idempotent per-day rollups (UTC buckets, unique-key upserts never double-count), reads them back as rates (bounce/complaint percent-of-sent, hard-fail/deferred percent-of-attempts), documented good/warning/poor/unknown grades, and per-day trend; shared sender-domain expression extracted to `repositories.SenderDomainExpr` so the R-07 stats and the rollups cannot drift. R-07 integration: `HealthReport.Reputation/Pool/Warmup` via the `domain.ReputationReader` seam (omitempty — absent reader preserves the report shape), today's rollup refreshed on read; the daily `ip_reputation_rollup` cron (`internal/cron/jobs/ip_reputation_rollup.go`, 02:15 UTC, previous two days) owns history. API (minimal-additive, 12 routes in `internal/routes/ip_pool_routes.go` with its own ops/admin groups; contract regenerated): workspace GET/POST/DELETE `/workspaces/current/ip-pools` + member add/remove, PUT `/domains/{id}/pool` (foreign pools 404 — assignment visibility validated), PUT `/domains/{id}/warmup`, GET `/domains/{id}/reputation`; admin GET/POST `/admin/ip-pools`, member add/remove, GET `/admin/ip-reputation?ip=`. `internal/handlers/ip_pool_handler.go` with ownsResource/requireEdit gates + `domain.ip_pool.*`/`domain.pool.assigned`/`domain.warmup.set`/`admin.ip_pool.*` audit events. Dashboard: Domains.vue "Sending IPs & warm-up" panel (pool assign incl. no-pool, member add/remove, pool create/delete, warm-up form with live resolved caps) + per-IP reputation rows (grade badges, rates, limitation note) + `web/src/api/domains.ts`/`types.ts`. Named tests: `policy_warmup_test.go` (TestWarmupDayIndexAcrossDays, TestWarmupDailyCapAcrossDays, TestWarmupHourlyCapSpreadsDaily, TestEffectiveHourlyBudgetFoldsWarmupCap — cap math across days), `deliver_pool_test.go` (TestExecutorSelectsPoolIPRoundRobin, TestExecutorFallsBackToStaticBindIP, TestDeliverDialsFromPoolMembersAndStampsSourceIP over a real in-process MX with bindable loopback pool IPs, TestPoolSelectionIsolatesDomains, TestDeliverWarmupCapDefersWithoutMX, TestDeliverWarmupCapExpiresAfterRampDays — pool selection + isolation + warm-up defer), `reputation_test.go` (TestAggregateDayRollupMath, TestAggregateDayBucketingAndNormalization, TestReputationReportRatesTrendAndGrade, TestReputationGradeThresholds, TestReputationIsolationAcrossWorkspaces, TestServiceRollupAndReportRoundTrip — aggregation correctness + workspace isolation), `ip_pool_repo_test.go` (DryRun scope-filter proofs + the delete guard). Battery: docker golang:1.27 `go build ./...` exit 0; `-race` package batteries for mta/reputation/domain/repositories/cron-jobs/worker all ok; `go test ./internal/routes/ -run TestAPIRouteContract -update` regenerated the golden (12 new lines); `npm --prefix web run type-check` exit 0, `npm --prefix web run test` 122/122. Residuals (open on RF-B123): blacklist alerts + abuse-response runbook (AC 3 remainder) and metrics-package signals (AC 4's "metrics" half); reputation transport counters cover direct-mode deliveries only (relay mode writes no attempt rows — stated on every report); warm-up enforces a spread hourly cap, not a per-message throttle; the budget keeper remains per-process (OB-1 documented limitation). Wave evidence: the OB-6 W3 section of `docs/audits/FEATURE_PARITY_AUDIT.md`.

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
