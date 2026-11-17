---
sidebar_position: 2
title: Features
description: Everything ReForwarder can do today — shipped, in progress, and planned
---

# Features

ReForwarder is a self-hosted email platform: it sends, receives, and relays email through one API, and it also runs the mail infrastructure underneath — the mail server, mailboxes, IMAP/POP3 access, and the deliverability automation around your domains.

This page lists what the product does in plain language. Every item is marked:

- ✅ **Shipped** — live in the product today
- 🚧 **In progress** — the core is shipped, a named piece is being finished
- 🗺️ **Planned** — designed and scheduled, not yet built

For step-by-step usage of any feature, follow the links into the topic guides.

## Sending & delivery

- ✅ **Send API** — send single, templated, batch, and scheduled emails over REST; attachments, custom headers, and preview mode included. See [Email Sending](email-sending/single-email).
- ✅ **Direct-to-MX mail agent (MTA)** — deliver straight to recipient mail servers over SMTP, or route through your own upstream relay per domain; STARTTLS is always used when the other side offers it.
- ✅ **Durable queue with real retries** — every send rides a persistent queue with layered retries, manual retry from the dashboard, and a self-healing reconciliation job.
- ✅ **Per-recipient retry schedules** — give any message its own retry interval list, notification addresses, and an expiry deadline; recipients that keep failing get notified and then expire cleanly.
- ✅ **DKIM signing & key lifecycle** — automatic DKIM signing on everything outbound, with key generation, rotation, DNS verification, and retirement from the dashboard.
- ✅ **Bounce & complaint handling** — a 14-class failure classifier feeds automatic suppression, and provider events (SES, SendGrid, Postmark, Mailgun) flow into the same pipeline.
- ✅ **Delivery status notifications (DSN)** — standards-compliant bounce/delay reports with enhanced status codes.
- ✅ **SMTP relay listener** — point any existing SMTP client at ReForwarder; submissions enter the same pipeline as the API, with workspace-scoped credentials, IP allowlists, and instant revocation. See [SMTP Relay](security/smtp-relay).
- ✅ **ESMTP extras** — `DELIVERBY` deadlines and `REQUIRETLS` (refuse to send over a plaintext hop) are honored end to end.
- ✅ **TLS everywhere** — STARTTLS/TLS modes for every SMTP listener, with certificate automation (see below).
- 🗺️ **ESP provider integrations** — send through external email APIs with failover, selected per send.
- 🗺️ **Idempotent sending** — supply your own idempotency key so retries never double-send.
- 🗺️ **Auto-pause circuit breaker** — campaigns pause themselves when failure or complaint rates cross a threshold.
- 🚧 **Scheduled transactional sends** — scheduling works; cancel/expire handling for far-future sends is being completed.

## Domains & deliverability

- ✅ **Domain verification** — prove ownership with a TXT token, then check SPF, DKIM, and DMARC records, all visible in the dashboard. See [Domain Verification](smtp-domains/domain-verification).
- ✅ **DNS automation** — publish and manage your DNS records through Cloudflare, Route53, or DigitalOcean without leaving the dashboard.
- ✅ **Automatic HTTPS certificates** — ACME certificate issuance and renewal with per-domain SNI for every SMTP listener.
- ✅ **DMARC aggregate reports** — daily RFC 7489 aggregate reports built and mailed to the `rua` addresses published in your DNS, plus inbound report parsing.
- ✅ **MTA-STS & TLS-RPT** — publish an MTA-STS policy for your domains and send TLS-RPT reports; inbound TLS reports are parsed too.
- ✅ **Sending IP pools, warm-up & reputation** — group sending IPs into pools, warm them up on a schedule, and watch per-IP reputation.
- 🚧 **Deliverability health & diagnostics** — per-domain health scoring with the deeper SPF/DMARC parsing being expanded.
- 🚧 **Strict verified-domain policy** — campaign-path parity and per-domain caps are being finished.
- 🚧 **DMARC report trends** — ingest trends surfaced on the domain health page.
- 🗺️ **Custom tracking domain** — open/click tracking on your own subdomain.

## Receiving email (inbound)

- ✅ **Inbound SMTP receiver** — accept mail for your domains on port 2525 with TLS, durable storage, and full parsing.
- ✅ **Routing rules** — per-domain rules deliver, forward, or hand mail to a webhook; forwarding re-enters the outbound pipeline with confirmed targets. (The rules engine is shipped; the last REST/UI registration step is in progress.)
- ✅ **Quarantine** — suspicious mail is held with a full audit trail instead of being bounced.
- ✅ **Spam & malware filtering** — heuristics, DNS blocklists, and sender reputation decide the action ladder (allow → flag → quarantine → reject); ClamAV scans attachments.
- ✅ **Sender verification** — SPF, DKIM, DMARC, ARC (annotated), and reverse-DNS checks on everything that arrives.
- ✅ **Inbound webhooks & live stream** — structured payloads pushed to your app, plus an SSE stream for real-time UIs.
- ✅ **Website form messages** — a public form endpoint with spam scanning and human replies from the dashboard. See [Forms & Messages](messages/overview).

## Mailboxes, IMAP & webmail

- ✅ **Mailboxes & folders** — real mailboxes with folders, flags, quotas, full-text search, and bodies encrypted at rest.
- ✅ **IMAP4rev1 & POP3 servers** — read your mail with any standard client; authentication is app-password-only so your primary password is never exposed. See [Mail Protocols](security/mail-protocols).
- ✅ **Webmail** — read, compose, and reply from the built-in `/mail` area of the dashboard.
- ✅ **Retention & expiration** — unified retention policies clean up mail, blobs, and events on one schedule.
- 🗺️ **Server-side filter scripts** — a Sieve-subset filter engine at delivery, with remote management (ManageSieve).

## Audience: contacts & subscribers

- ✅ **Dual audience models** — transactional contacts and marketing subscribers stay separate by design. See [Subscribers](subscribers/subscriber-management) and [Contacts](contacts/contact-management).
- ✅ **Subscriber lists** — static and dynamic lists with subscribe/unsubscribe APIs and bulk import. See [Subscriber Lists](subscribers/subscriber-lists).
- ✅ **Segments** — dynamic segments with a guarded SQL condition builder, live preview, and campaign targeting. See [Contact Lists](contacts/contact-lists).
- ✅ **Suppression list** — scoped suppression with send-time filtering and automatic suppression from hard bounces. See [Suppression List](contacts/suppression-list).
- 🗺️ **Double opt-in, signup forms & preference center** — the consent layer (planned in sequence: DOI first, then forms and preference center).
- 🗺️ **Subscriber tags, bulk operations & activity timelines**.

## Campaigns

- ✅ **Campaign engine** — draft → scheduled → sending lifecycle with fan-out, pause/cancel/resume, and send-time re-checks. See [Campaigns](campaigns/overview).
- ✅ **A/B testing** — multi-variant splits with automatic winner selection.
- ✅ **Timezone-aware sending** — deliver at each recipient's local send time.
- ✅ **Open/click tracking** — pixel opens and rewritten links, all HMAC-signed. See [Tracking](tracking/overview).
- ✅ **One-click unsubscribe** — RFC 8058 one-click, scoped per list so password resets keep flowing. See [Unsubscribe](tracking/unsubscribe).
- 🚧 **Campaign retry for failed recipients** — duplication is shipped; re-sending to just the failed recipients is being finished.
- 🗺️ **Campaign test sends, CSV exports & UTM auto-tagging**.

## Templates

- ✅ **Versioned, localized templates** — versions with activation, per-language content, variable substitution, and stylesheet inlining. See [Templates](templates/overview).
- ✅ **Visual & code editors** — drag-and-drop newsletter builder plus a code editor with live preview and desktop/mobile rendering.
- ✅ **Import/export** — HTML import and JSON export/import.

## Automation

- ✅ **Automation engine** — journeys with node executors, triggers, and schedules. See [Workflows overview in Automation guides](#).
- ✅ **Visual builder** — canvas-based workflow editor with a node palette and per-node configuration.
- ✅ **Segment-aware conditions** — automations branch on segment membership and subscriber engagement.
- 🗺️ **Dry-run tooling, stop-on-reply, re-engagement triggers**.

## Analytics & monitoring

- ✅ **Analytics APIs & dashboard** — daily counts, status breakdowns, trends, and provider breakdowns for admins and workspaces. See [Analytics](analytics/dashboard).
- ✅ **Email analytics** — delivery, open, and click metrics with trend views. See [Email Analytics](analytics/email-analytics).
- ✅ **Prometheus metrics & health probes** — 30+ metric families behind `/metrics`, plus `/healthz` and `/readyz`. See [Prometheus Metrics](analytics/prometheus-metrics).
- ✅ **Metric alerts** — condition → action alert rules that fire and resolve as webhook events.
- ✅ **OpenTelemetry export & error hook** — push metrics to any OTLP endpoint and forward panics/errors to your monitor.
- ✅ **Grafana dashboard** — an importable sample dashboard. See [Health Checks](analytics/health-checks).
- 🗺️ **Delivered-state analytics, bounce classification surfaces & device/geo breakdowns**.

## Webhooks & events

- ✅ **Webhooks engine** — workspace-scoped dispatch, HMAC signatures, bounded exponential retries, and per-attempt history. See [Webhooks](webhooks/overview).
- ✅ **SSRF-hardened egress** — destination validation, IP pinning, redirect blocking, and an optional egress proxy.
- ✅ **Delivery history & replay** — inspect every attempt and replay failed deliveries from the dashboard.
- ✅ **Audit log & event streams** — audit trails for admin actions and live event streams. See [Audit Log](webhooks/audit-log).

## Administration & operations

- ✅ **Admin console** — users, workspaces, domains, plans, servers, events, metrics, announcements, and updates. See [Admin Panel](admin/platform-settings).
- ✅ **Security administration** — bans, security events, tunable rules, allowlists, and live streams.
- ✅ **Admin impersonation** — support staff can act as a user, visibly and auditable.
- ✅ **Retention & GDPR tooling** — multi-store cleanup, blob purge, message erasure, account cleanup, and data export. See [GDPR](gdpr/data-export-import).
- ✅ **Database backups** — scheduled `pg_dump` with retention and a restore runbook.
- ✅ **Operational resilience** — hot settings reload, graceful shutdown with drain, memory backpressure, and Redis HA guidance. See [Redis HA](operations/redis-ha).

## Developer API

- ✅ **One REST API** — every feature above is available under `/api/v1` with a consistent response envelope and OpenAPI documentation. See [SDKs](sdks/overview).
- ✅ **API keys & app passwords** — scoped keys with IP allowlists, expiry, quotas, rate limits, and usage analytics.
- 🚧 **Route contract gate** — a CI gate keeps the documented route set and the real routes perfectly in sync (frontend coverage being expanded).

## Coming next

The next slice of delivery-engine work, in order: authenticated failure (ARF) reports → admin objects for the report family → per-domain report settings → HTTP hooks at each SMTP stage → per-connection budgets → mail-account aliases and groups → task-manager run history → a unified expression language for rules → more spam analyzers (Pyzor, spam traps, rule feeds) → SRS envelope rewriting → per-recipient filter scripts → ManageSieve. Larger pieces (tenants, alternative storage backends, multi-node failover) are designed but gated behind explicit architecture decisions.

:::note
Internal planning, per-feature status ledgers, and the development backlog live in the repository under [`docs/`](https://github.com/ReForwarder/ReForwarder/tree/main/docs) — see `docs/PROJECT_STATUS_DASHBOARD.md` for the live status of every feature.
:::
