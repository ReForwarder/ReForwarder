# 01 — OSS Feature Inventory: 12 Self-Hostable Email Platforms

> **Doc 1 of 7** in the ReForwarder product-roadmap documentation suite. Feeds `02-competitor-comparison.md` and all later docs (gap analysis, roadmap prioritization). This document is the **normalized, registry-ID-keyed inventory**; cross-project matrices and ReForwarder verdicts live in later docs — see `02-competitor-comparison.md`.

- **Research date:** 2026-11-05 (all facts, releases, and repo states are as-of that date; see §5 caveats).
- **Scope:** 12 open/source-available OSS email platforms audited for feature completeness. ReForwarder itself is referenced only for context (its audit baseline is the working tree at HEAD `a6f1681`, parent `347ebb4` — spot-checked against the repo on 2026-11-05).
- **Method (evidence hierarchy, applied per project):** repo (GitHub API metadata, raw LICENSE/raw files, code tree) → official docs → official site → issues/releases → release notes → secondary sources (clearly flagged when used). No source-level clone verification was performed for most projects (see §5). Web research only; the ReForwarder repo was read for spot-checks, never modified.
- **Normalization:** every feature in §3 carries a permanent ID from the canonical registry (`registry.md` v1.0.0, frozen 2026-11-05 — IDs are permanent; later docs must reference, never renumber). Where a capability was found in a dossier without a registry ID, it is described **under the nearest registry entry**, not given a new ID. No new IDs were invented.

## ID scheme

| Prefix | Category | Entries |
|---|---|---|
| INF | A — Email infrastructure | 36 |
| DOM | B — Domains & DNS | 13 |
| CON | C — Contacts & audience | 20 |
| CAM | D — Campaigns | 18 |
| TPL | E — Templates & editors | 22 |
| ANA | F — Analytics & tracking | 16 |
| AUT | G — Automation | 18 |
| API | H — APIs & integrations | 19 |
| SEC | I — Security & administration | 18 |
| OBS | J — Infrastructure & observability | 19 |
| **Total** | | **199** |

## Project codes

| Code | Project | Code | Project | Code | Project | Code | Project |
|---|---|---|---|---|---|---|---|
| LM | Listmonk | NU | Notifuse | MC | MailCheap | WR | Wraps |
| BM | BillionMail | SP | SendPortal | SE | Sessy | HP | Hyvor Post |
| PO | Postal | KE | Keila | HR | Hyvor Relay | | |
| US | useSend | | | | | | |

## Status-flag legend (per-project flags used in §3 "Projects" column)

| Flag | Meaning |
|---|---|
| ✓ | implemented |
| ◐ | partial |
| ▲ | planned / unreleased |
| E | enterprise / paid / licence-gated |
| H | hosted-only |
| ✗ | verified absent / non-feature |

Unverified items are **omitted** from §3 rather than guessed. ReForwarder's own status column (IMPLEMENTED / PARTIAL / MISSING / NEEDS_REFACTOR / NEEDS_VERIFICATION / NOT_APPLICABLE) is reproduced from the registry for context; per-feature verdict analysis lives in `02-competitor-comparison.md`.

---

# §2 Project profiles

Profile order follows dossier order 01–12. Every license claim carries its verification source. "NV" = NEEDS_VERIFICATION (tallied per project in §5).

## 2.1 Listmonk (LM)

| Field | Value |
|---|---|
| Description | High-performance, self-hosted newsletter and mailing-list manager; single Go binary + PostgreSQL; positioned as Mailchimp/Sendy alternative. Developed since 2018 (Zerodha Tech backing). ~24.1k stars. |
| Repo (verified) | https://github.com/knadh/listmonk — GitHub API `full_name`, homepage `https://listmonk.app` |
| License | **AGPL-3.0** — verified: raw `LICENSE` (full FSF AGPLv3 text) + GitHub API `license.spdx_id`. No enterprise tier, no dual licensing, no hosted offering. AGPL §13 network copyleft applies. |
| Languages / frameworks | Go (Echo); Vue + Buefy (v7 beta ships a full admin-UI rewrite: Squire editor, CodeMirror, Playwright e2e) |
| Databases / infra | **PostgreSQL ≥12 only** — no Redis, no broker. S3-compatible or local media. Single binary (goreleaser), Docker + compose, community Helm. |
| Maintenance | **Active.** Pushed 2026-11-04; latest stable v6.2.0 (2026-06-26, includes security fixes); v7.0.0-beta.1 pre-release (2026-10-20); nightly builds; cadence ~2–4 releases/yr; 38 language packs. |
| Self-hosting | **FULLY SELF-HOSTABLE** — single binary + PostgreSQL; no hosted-service dependency for any core function (sending is operator SMTP / generic messengers; bounce ingestion via operator POP3 or provider webhooks; third-party one-click hosts are unaffiliated). |

Per-category summary: **A** strong tx API (single/bulk, `subscriber_mode=external`), multi-SMTP with per-campaign/per-domain selection, sliding-window throttling, one-shot scheduling. **B** none — delegated to SMTP provider (no SPF/DKIM/DMARC tooling). **C** lists, per-list subscription states, double opt-in, JSON attributes, raw-SQL segmentation, CSV/ZIP import, GDPR self-service export/removal, public signup pages. **D** campaigns w/ drafts, scheduling, cloning, lists+SQL targeting, per-campaign analytics, **public archive**, multiple newsletters. **E** raw HTML + WYSIWYG + drag-and-drop builder + Markdown; Go templates + Sprig (100+ functions); 38-lang UI i18n. **F** open/click tracking (anonymous or per-subscriber; global disable v6.1.0), POP3 + 6 provider bounce webhook receivers, heuristic hard/soft classification, campaign analytics. **G** no automation engine; automated bounce-threshold blocklisting; double-opt-in confirmation automation. **H** full REST API + Swagger; BasicAuth/token; role-enforced API scopes; generic HTTP messenger interface (SMS/WhatsApp/FCM community impls); community SDKs + community MCP; direct-DB integration documented. **I** granular RBAC + per-list roles, OIDC SSO (PKCE, claim-based role mapping), GDPR self-service; no 2FA/audit logs. **J** single-process internal queues, Postgres-only, slow-query cache + cron refresh, stdout + admin Logs page, S3 media, no Prometheus/health endpoint documented.

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| SQL-expression segmentation over JSONB attributes | CON-007 | docs/querying-and-segmentation |
| Public campaign archive + per-campaign publishing | CAM-012 | docs/archives/ |
| Multi-SMTP sender pool w/ per-campaign & per-domain routing | INF-001/INF-024 | docs/configuration/, v5.0.0/v6.2.0 notes |
| Transactional API incl. sending to non-subscribers (`subscriber_mode`) | INF-004 | docs/apis/transactional/ |
| Bounce webhooks from 6 providers (SES/Azure/SendGrid/Postmark/ForwardEmail/Lettermint) | INF-019/API-014 | docs/bounces/ |
| Generic messenger interface → SMS/WhatsApp/FCM | INF-036 | docs/messengers/ |
| Bounce POP3 mailbox ingestion (destructive read) | INF-020 | docs/bounces/ |
| OIDC SSO w/ role mapping + RP-initiated logout | SEC-006 | docs/oidc/, v7 beta notes |
| Per-list permission roles | SEC-002 | docs/roles-and-permissions/ |
| Drag-and-drop email builder + Markdown content type | TPL-002/TPL-003 | homepage, v5.0.0, tx API |
| Double opt-in per list w/ List-Unsubscribe on opt-in mails | CON-004 | docs/concepts/, v5.0.0 |
| Global tracking kill-switch + anonymous tracking | ANA-012 | v6.1.0, homepage |

Verified non-features: no marketing automation/workflows/drips; no A/B testing; no multi-tenancy (single tenant/instance); no inbound email (POP3 is bounces-only); no SPF/DKIM/DMARC management; no outbound event webhooks (API-pull or direct DB only); no official SDKs (community only); no priority queues; no per-message retention policies; no 2FA; no native audit logs; RSS-to-email community-only; not an SMTP server (client only); no hosted/SaaS version.

Limitations & security notes: AGPL §13 network copyleft with no commercial escape hatch; Sprig templating is Turing-complete (docs warn template-edit permission is a DoS vector — restrict `templates:manage`); `subscribers:sql_query` bypasses list/subscriber permission boundaries (docs recommend restricted Postgres role); bounce handling is heuristic and mailbox-destructive (POP3 download-and-delete; webhook hard/soft type "doesn't affect handling"); Postgres is the scaling ceiling (aggregate stats slow at millions of rows, mitigated by query cache); recurring permission-validation security patches (v6.1.0, v6.2.0); API 429s exist but limits undocumented.

NEEDS_VERIFICATION: (1) campaign-composer attachments; (2) admin bulk CSV subscriber export; (3) health-check endpoint; (4) CSS-inlining behavior; (5) saved subscriber filters; (6) multi-instance/worker deployment story; (7) RTL in i18n; (8) CAPTCHA/honeypot on forms; (9) campaign analytics CSV export.

## 2.2 BillionMail (BM)

| Field | Value |
|---|---|
| Description | All-in-one self-hosted **mail server + email-marketing platform** from the aaPanel team (international edition of China's BaoTa). One compose stack: Postfix + Dovecot + Rspamd + Roundcube mailboxes, plus marketing layer (contacts/tags/templates/campaign "tasks"/tracking). Positioning explicitly includes cold-email outreach ("unlimited sending, no built-in speed limits"). ~16.2k stars. |
| Repo (verified) | https://github.com/Billionmail/BillionMail — expected `aaPanel/BillionMail` redirects here (GitHub API owner login `Billionmail`, repo ID 932580333); default branch `dev` |
| License | **AGPL-3.0** — verified: GitHub API `license.spdx_id` + raw `LICENSE` on `dev` (verbatim unmodified FSF text, no exceptions). No enterprise/software edition; only monetization found is a paid human "Deployment Package" service ($98.9/instance, 7-day support). |
| Languages / frameworks | Go core (web UI/API/Swagger); Postfix/Dovecot/Rspamd/Roundcube containers; Fail2ban in core |
| Databases / infra | PostgreSQL 17.4 + Redis 7.4.2 (loopback, password-protected); Docker Compose only; `bm` management CLI; core container mounts **Docker socket read-only** |
| Maintenance | **CAUTION — apparent stall.** Latest release v4.9 (2025-12-11); `pushed_at` 2026-06-11; as of 2026-11-05 no releases in ~11 months and no pushes in ~5 months despite 252 open issues. Where development continues is NV (§2.2 NV-1). 2025 cadence was monthly. |
| Self-hosting | **Fully self-hostable** — compose defines every component locally; no SaaS dependency for core functions. Optional externals: outbound SES/Mailgun relays (port-25-blocked hosts), BYO LLM keys for AI templates. Domain DNS user-configured; Let's Encrypt HTTP-01 (port 80) or pasted certs. |

Per-category summary: **A** built-in SMTP (Postfix 25/465/587), full mailboxes (IMAP/POP + Roundcube webmail), tx send API (`/api/batch_mail/api/send`, `X-API-Key`), visual Postfix queue manager (retry/delete/clear), relay-out to SES/Mailgun (one relay per domain), retention policies, **no rate limits by positioning**. **B** DNS record guidance + `bm show-record`, DKIM 1024/2048 generation, Let's Encrypt + SNI, domain blacklist scanning w/ alerts, per-domain quotas. **C** groups + tags (batch ops, NOT-condition filters), JSON attributes, CSV import/export, paste-in import, email validation on import, double opt-in subscription lists, per-contact activity history, Smart Suspend List. **D** 5-step campaign tasks, group+tag targeting, duplication, scheduling, campaign/global tracking toggles, sent-record archives w/ retention, spintax, **no A/B**. **E** HTML + drag-and-drop editors, reusable templates, template variables, spintax, **AI-assisted creation + website profiling** (v4.0). **F** open/click tracking, real-time dashboard (deliverability/open/CTR/bounce reasons), operation + error logs, log compression, blacklist alerts, no webhooks (open feature requests #386/#150). **G** auto-suppression of invalid addresses, double-opt-in automation, blacklist/quota alerts; **no workflow builder**. **H** send API w/ error-code contract, per-key IP whitelists, Swagger (per-instance), `bm` CLI, SES/Mailgun/custom relays, aaPanel one-click + aaPanel login. **I** Fail2ban, Rspamd, SNI TLS, IP whitelists, operation logs; **single-admin model** (no RBAC/2FA evidence). **J** single-node compose only, no Prometheus, queue manager UI, self-update via `bm update`, warmup scoring + per-domain IP binding (v4.4/v3.1).

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| Full mail server: mailboxes + IMAP/POP + Roundcube webmail | INF-003 | compose, docs /start/mailbox.html |
| Built-in SMTP server (Postfix submission) | INF-002 | compose, docs /start/smtp.html |
| Visual Postfix queue manager w/ one-click retry/delete/clear | INF-010 | v4.1 release notes |
| AI template generation + website profiling | TPL-020 | v4.0, website /ai |
| Spintax content variation | TPL-021 | v4.3, docs /start/spintax.html |
| Domain blacklist monitoring w/ alert notifications | DOM-011 | v4.8 |
| IP warmup scoring + per-domain outbound IP binding | INF-025 | v4.4/v3.1, FAQ |
| Let's Encrypt auto-apply/renewal + SNI | DOM-010 | v4.4/v4.0, docs /start/domain.html |
| DKIM 1024/2048 key-length options | DOM-003 | v4.9 |
| Smart Suspend List (auto-pause invalid addresses) | INF-018 | v4.1 |
| Per-key API IP whitelists (error 1002) | API-005 | api_mail_guide, v3.2 |
| Retention policies + log rotation/compression | INF-017 | v4.9/v4.3 |

Verified non-features: webhooks of any kind (open requests #386/#150); automation/workflow builder; A/B testing; gradual/ramped sending (deliberately no speed limits); preference centers; signup form builder; dynamic segments; official SDKs; MCP server; Prometheus/Grafana; health-check endpoints; multi-node/HA deployment; multi-user RBAC/teams/2FA; ClamAV in default stack; DMARC report processing; automated DNS provisioning; hosted SaaS (only a paid deployment service).

Limitations & security notes: maintenance stall is the top risk (see NV-1); repo moved `aaPanel`→`Billionmail` owner (circumstances NV); Docker socket mounted read-only into core (container-escape surface); single-tenant single-admin; "unlimited sending" positioning is a deliverability trap on fresh IPs (mitigation: warmup scoring, relay-out); Roundcube password changes do not sync back; v4.4 removed an arbitrary-file-read vulnerability; default `bm` credentials should be rotated; Postgres is current DB (MySQL references are stale); cold-email use cases carry legal/reputation risk the tool does not gate.

NEEDS_VERIFICATION: (1) where development continues (other branches/org/announcements); (2) repo-move details; (3) AI provider list (from third-party review only; /ai page JS-rendered); (4) attachments/custom headers in Send API; (5) campaign test sends, template previews, CSS inlining, plain-text parts; (6) exportable analytics reports; (7) scope of v3.2 IP-whitelist settings (login vs API); (8) optional antivirus beyond Rspamd; (9) multi-user accounts/2FA absence; (10) exact management-API surface (Swagger per-instance); (11) campaign scheduling depth (recurring? timezones?); (12) greylisting handling.

## 2.3 Postal (PO)

| Field | Value |
|---|---|
| Description | Fully featured open-source mail **delivery platform** for incoming and outgoing email — self-hosted Sendgrid/Mailgun/Postmark alternative. SMTP submission + HTTP send API, MX receiving with routing, per-message logging/retention, DKIM, IP pools, delivery-lifecycle webhooks, open/click tracking, multi-tenant admin (organizations → mail servers). **Pure infrastructure — deliberately no marketing stack.** Originally aTech Media, now under Krystal Hosting (UK); repo in `postalserver` org. ~16.9k stars. |
| Repo (verified) | https://github.com/postalserver/postal — GitHub API; legacy `atech/postal` redirects. Website `postalserver.io` currently 301s to the install repo; docs at https://docs.postalserver.io |
| License | **MIT** — verified: GitHub license API (`spdx_id: MIT`, file `MIT-LICENCE`) + raw file (copyright Krystal Hosting Ltd 2017-2024, Adam Cook 2024, Charlie Smurthwaite 2024). No dual licensing, no enterprise tier, no hosted service of its own. |
| Languages / frameworks | Ruby on **Rails 7.1.6** (Puma); custom SMTP server in the same codebase; authie/jwt/omniauth_openid_connect; prometheus-client; sentry-rails |
| Databases / infra | **MariaDB 10.6+ only** (MySQL explicitly unsupported; per-mail-server message DB). **No external broker in v3** (DB-backed queue inferred — NV). One official image, three processes (`web-server`, `smtp-server`, `worker`) + tools runner. Optional self-hosted SpamAssassin/ClamAV/rspamd. |
| Maintenance | **Maintained again after a ~20-month gap.** v3.3.7 (2026-06-03) — security release (SSRF in outbound webhooks + SQLi GHSA-x2hq-rfpg-3xr5); 2026 releases are security/hardening-focused; v2 management API "planned, no ETA"; low but real bus factor (two named individual maintainers + Krystal). |
| Self-hosting | **Fully self-hostable** — Docker + MariaDB (≥4 GB RAM, outbound port 25); all integrations point at user-controlled endpoints; no telemetry/licence server/SaaS component found. Docs caveat: operator bears full deliverability responsibility. |

Per-category summary: **A** own SMTP server (PLAIN/LOGIN/CRAM-MD5 + IP-based `SMTP-IP` creds), HTTP send API (v1), inbound MX + routing (HTTP/SMTP/email) w/ wildcards + address tags, queues incl. **held queue w/ manual release**, per-server rolling 60-min send limits, soft-fail retries (default max 18), auto-suppression (24h hard-fail window / max soft-fail; 30-day expiry), per-server Live/Development hold mode, message tags, SpamAssassin/ClamAV integration, bounce handling via return-path. **B** TXT-token domain verification (+ emailed code), org/server-level domains, SPF include guidance, per-domain DKIM (1024-bit) + custom return-path CNAME, **hourly DNS re-checks + `DomainDNSError` webhook**, manual TLS certs only (no ACME/SNI), CNAME tracking domains. **C** none by design (suppression list + per-recipient logs only). **D** none by design (tags + server graphs closest). **E** none (messages submitted pre-rendered). **F** open/click tracking w/ per-link `+notrack` opt-outs, delivery statuses (`MessageSent/Delayed/DeliveryFailed/Held`), bounce webhook w/ original message, historical message logs + server-wide search, volume graphs, spam status per message, admin SMTP-data logging, queue visibility + latency metric (3.3.0), **7-day webhook request history**, per-server Privacy mode. **G** delivery-lifecycle event webhooks; automatic suppression + bounce processing; DNS-monitoring alerts; no workflows. **H** API v1 (send/read; management = UI-only, v2 planned), official client libs (Ruby/PHP/Node + community), per-server SMTP credentials, OIDC SSO, `postal` management CLI. **I** orgs→mail-servers multi-tenancy, **minimal RBAC** (global admins vs org users), server suspension, per-server retention, privacy mode. **J** independently scalable web/SMTP/worker, Prometheus `/metrics` (worker :9090 / SMTP :9091), `/health`, Sentry optional, message-DB management, Helm exporter in code (official chart NV).

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| IP pools (org→server allocation, host networking) + IP routing rules | INF-025 | docs 3.features/ip-pools.md |
| Held queue with manual release + hold expiry | INF-010/OBS-009 | mail-server-settings |
| Rolling 60-minute send limits w/ SendLimit webhooks | INF-012 | mail-server-settings |
| Inbound routing: HTTP endpoint / SMTP server / email + wildcards, address tags | INF-029/INF-030 | feature-list, routing docs |
| Automatic suppression w/ 30-day expiry + manual release | INF-018 | mail-server-settings |
| Hourly DNS re-checks + `DomainDNSError` webhook | DOM-008 | sending-domains, webhooks |
| Custom return-path CNAME for DMARC alignment | DOM-005 | sending-domains |
| Per-message SMTP data logging (admin) | ANA-013 | mail-server-settings |
| 7-day webhook request history for debugging | API-012 | feature-list |
| Delivery-lifecycle webhooks (Sent/Delayed/Failed/Held/Bounced) | API-009 | webhooks docs |
| Development mode (hold all mail, viewable) | INF-026 | mail-server-settings |
| Prometheus metrics + health endpoints per process | OBS-006/OBS-008 | health-metrics docs |

Verified non-features: no email-marketing suite (no contacts/lists/segments/campaigns/A-B/forms/templates/automation); no mailboxes/webmail/IMAP (receiving is routing-only); no DMARC record management or report ingestion; no domain-reputation scoring; no 2FA; no granular/scoped API keys; no webhook signing or documented retry policy (as documented today); no ACME/SNI; no management API (v1 is send/read only; v2 planned no ETA); no AI features; no priority queues or pacing beyond rolling limits; no IP warm-up tooling; no upstream-relay mode (Postal *is* the relay); v2-era "senders"/"snapshots" absent from v3 docs (legacy/removed — NV).

Limitations & security notes: MariaDB only (`innodb_log_file_size` ≥ 10× largest message); **DKIM keys are 1024-bit** (below current 2048-bit expectations); minimal RBAC (org users control all servers in org; `postal make-user` always creates admins); webhook SSRF + message-db SQLi patched in 3.3.7 — run ≥3.3.7 and consider worker egress controls; IP pools require host networking and physically bound IPs (misconfiguration silently breaks dequeuing); 2024–2026 dormancy gap — check issue-tracker responsiveness; port 25 must be unblocked; RSA-2048 `signing.key` is load-bearing for message tokens.

NEEDS_VERIFICATION: (1) exact v3 worker job mechanism (no broker gems; DB-backed inferred); (2) API v1 custom headers / scheduled sending; (3) webhook payload signing + retry/backoff; (4) 2FA availability; (5) audit-log/event-feed scope in UI; (6) global tracking kill-switch; (7) formal removal of "senders"/"snapshots"; (8) governance-handover announcement; (9) `helm_config_exporter.rb` maturity; (10) historical RabbitMQ dependency (v1/v2).

## 2.4 useSend (US)

| Field | Value |
|---|---|
| Description | Open-source email platform ("open source alternative to Resend, Sendgrid, Postmark") covering transactional + marketing email **on top of AWS SES**. Developer-first: REST API, SDKs, SMTP proxy. Monorepo (`apps/`, `packages/`). Status: **beta**, email channel only (SMS/push stated as future). Formerly "Unsend" (cosmetic NV). 4.8k stars. |
| Repo (verified) | https://github.com/usesend/useSend — GitHub API; default branch `main`. Docker: `usesend/usesend`, `ghcr.io/usesend/usesend`, optional `usesend/smtp-proxy`. Website https://usesend.com, docs https://docs.usesend.com |
| License | **AGPL-3.0** — verified: raw `LICENSE` (standard unmodified text) + GitHub API. No dual licensing/open-core split found; the `Plan` enum (FREE/BASIC) is hosted-cloud billing, not a self-host gate; self-hosting needs no license key. |
| Languages / frameworks | TypeScript; Next.js 15; public API Hono (`/api/v1`) + published OpenAPI; internal dashboard tRPC; NextAuth (**GitHub OAuth only**) |
| Databases / infra | PostgreSQL via Prisma; Redis + BullMQ; **AWS SES (only sending provider) + SNS status callbacks**; S3-compatible storage (RustFS replaced MinIO in v1.10.0); optional SMTP proxy app |
| Maintenance | **Active, beta.** v1.10.0 (2026-10-26) — security-focused: CVE batch (0 critical/2 high), Node 24, Next.js 15.5.28, container halts on failed migrations; pushed 2026-10-31; steady monthly cadence. |
| Self-hosting | **GENUINELY SELF-HOSTABLE — VERIFIED**, with one hard external dependency: **AWS SES + SNS** for sending and status events (no relay-only sending mode; optional inbound SMTP proxy fronting the API). Callback URL must be publicly reachable; SES account must leave sandbox. No hosted control plane, no license key. |

Per-category summary: **A** tx REST (`POST /v1/emails`), batch endpoint, campaigns w/ `batchSize` (≤100k) + `batchWindowMinutes`, SMTP proxy (password = API key), attachments (≤10), custom headers, **idempotent sends** (`Idempotency-Key`, 24h, 409 on conflict), `EMAIL_CLEANUP_DAYS` retention, natural-language scheduling ("tomorrow 9am"). **B** SES identity verification, DKIM CNAME (selector default `usesend`) w/ DkimAttributes polling, generated SPF/DMARC w/ DNS checks, per-domain tracking toggles, domain reputation API, domain CRUD API. **C** contact books (CRUD), contact upsert, bulk ops, CSV import, per-book custom properties, statuses subscribed/pending/unsubscribed, **double opt-in** (HMAC-SHA256 links, 7-day expiry), one-click unsubscribe + reasons, **suppression dashboard** (HARD_BOUNCE/COMPLAINT/MANUAL auto-listing; add/bulk-add/remove/stats); **no segmentation, tags, preference center, signup forms**. **D** campaigns (API + dashboard), scheduling, pause/resume/delete, send-now, per-campaign counters, unsubscribe enforcement (`{{usesend_unsubscribe_url}}` mandatory), personalization w/ fallback syntax; **no A/B, no segment targeting**; dashboard-only drafts/previews/test-sends (NV). **E** jsx-email + Tiptap drag-and-drop editor (maily.to-inspired, unsubscribe-footer node), template variables for tx, React Email integration, HTML+text; no template REST API/MJML/Markdown/localization. **F** open/click tracking (webhook payloads w/ ua/ip/platform), SES/SNS delivery/bounce/complaint status, dashboard analytics, **time-series API** (7/30-day buckets), **reputation metrics API** (bounceRate/complaintRate per domain), internal per-event `EmailEvent` rows (no public read API). **G** scheduled API sends only; **no workflows/event triggers**. **H** Hono REST + OpenAPI, API keys w/ FULL/SENDING scopes, **official TS/Python/Go SDKs** (+community Rust), **webhooks: 21 event types, HMAC-SHA256 `v1=` + timestamp + 5-min replay rejection, backoff attempts 1–6, auto-disable after 30 consecutive failures, payload dedup**; internal WebhookCall history (NV); per-team rate limit in schema (NV). **I** teams w/ invites (self-host signup invite-gated after first user), ADMIN/MEMBER roles, GitHub-only auth (no 2FA/SSO/email-password), team-scoped row isolation, retention controls; no audit logs. **J** Docker compose (PG+Redis+RustFS), Railway one-click, migration-failure halt, MinIO→RustFS migration tool; health checks/BullMQ observability NV.

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| Idempotent send API (header, 24h window, 409) | INF-031 | send-email.md |
| Natural-language scheduling ("tomorrow 9am") | INF-014 | update-schedule.md, schedule-campaign.md |
| Hard AWS SES+SNS integration as the platform core | INF-022 | self-hosting/overview |
| Double opt-in w/ HMAC links + 7-day expiry | CON-004 | guides/double-opt-in.md |
| Suppression management UI w/ stats + auto-listing | CON-013 | suppression-service.ts, dashboard |
| Webhook security stack (HMAC, timestamp, replay, backoff, auto-disable) | API-010/API-011 | guides/webhooks.md |
| Official TypeScript / Python / Go SDKs | API-008 | packages/, get-started/go.md |
| Domain reputation metrics API (bounce/complaint rates) | DOM-011 | analytics/reputation-metrics.md |
| Campaign pacing (batchSize ≤100k + batchWindowMinutes) | INF-015 | campaigns/create-campaign.md |
| Unsubscribe reasons captured | CON-015 | `unsubscribe_reason` migration |
| React Email authoring integration | TPL-008 | guides/use-with-react-email.md |

Verified non-features: inbound email (README "Planned"); BYO AWS credentials ("Planned"); multiple sending providers (SES only); campaign A/B testing; segmentation/audience filters; workflow/automation builder; template REST API; template localization; MJML; Markdown; signup forms; preference center; tags; audit logs; 2FA; SSO beyond GitHub OAuth; email/password auth; fine-grained API scopes (FULL/SENDING only); Zapier/MCP/CLI; SMS/push/WhatsApp ("future plans").

Limitations & security notes: hard AWS dependency — IAM docs require `AmazonSESFullAccess` + `AmazonSNSFullAccess` (broad permissions); GitHub-only auth blocks air-gapped/org-restricted environments; S3 objects publicly readable by design for editor images (`S3_COMPATIBLE_PUBLIC_URL` must be public); per-message event history internal-only (`latestStatus` single enum publicly); webhook security solid (timing-safe comparison recommended, SDK `constructEvent()` helper); double-opt-in links HMAC-signed 7-day; v1.10.0 batch shows responsive security posture; SMTP proxy Bcc/envelope fix in v1.9.8 (earlier mismatch).

NEEDS_VERIFICATION: (1) dashboard campaign preview/test-send/duplication end-to-end; (2) public API rate-limit behavior; (3) WebhookCall history exposure; (4) per-domain tracking-toggle granularity; (5) send-path retry semantics on SES throttling; (6) health checks + BullMQ observability tooling; (7) former "Unsend" naming history.

## 2.5 Notifuse (NU)

| Field | Value |
|---|---|
| Description | Self-hosted newsletter / email-marketing / transactional platform positioned as Mailchimp/Brevo/Listmonk/Klaviyo alternative with a sovereignty pitch. Go backend + React console + PostgreSQL; MJML builder, Liquid templating, canvas automations, A/B testing, real-time segmentation, built-in cookieless web analytics (Staminads), blog CMS, AI assistant. EU-hosted managed cloud from $16/mo. 2.3k stars. |
| Repo (verified) | https://github.com/Notifuse/notifuse — default branch `main`. Website https://www.notifuse.com, docs https://docs.notifuse.com, Docker `notifuse/notifuse` |
| License | **BUSL-1.1 for v40.0+** — verified from raw `LICENSE` + `LICENSING.md` at HEAD (GitHub API reports NOASSERTION). Change License: AGPL-3.0-or-later; Change Date = 4 years from each version's publication. **v39.x and earlier are permanently AGPL-3.0-or-later** (not retroactive); `web_analytics_sdk/` is AGPL in every version. Licensor: Pierre Bazoge (sole trader, France, SIREN 507 888 568). **Open-core: 6 "Licensed Features" require a paid key** — (1) >3 workspaces per deployment, (2) granular permission sets, (3) Amazon SES tenant provisioning, (4) OIDC SSO, (5) multilingual template variants, (6) audit-log recording. Tiers: Studio $290/yr, Agency $890/yr, Enterprise $1,990/yr. The entire send path, contacts, broadcasts, automations, tx API, web analytics and all 7 provider integrations are never gated; licence verification is offline; expired key → 30-day grace → free-tier fallback; nothing created is removed. **PRs are not accepted.** |
| Languages / frameworks | Go (clean architecture, Squirrel); React + TypeScript + Ant Design; **gomjml fork** (server-side MJML); BYO LLM (Anthropic default, OpenAI, Google Gemini; optional Firecrawl) |
| Databases / infra | **PostgreSQL 17+, two-tier: one system DB + one database per workspace** (root DB credentials needed for standalone installs). S3-compatible storage; OTLP tracing (Jaeger/Zipkin/Datadog); MaxMind GeoLite2. 7 sending providers: SES, Mailgun, Postmark, Mailjet, SparkPost, SendGrid, generic SMTP. |
| Maintenance | **Very active.** v42.1 (2026-10-28, SSO DoS fix go-jose 4.1.6); pushed 2026-11-03; cadence v39.1→v42.1 Aug–Oct 2026; "~95 releases/12 months" is a marketing claim (NV). |
| Self-hosting | **GENUINELY SELF-HOSTABLE — VERIFIED.** Docker Compose (embedded PG) or standalone `docker run` + own PG; interactive setup wizard; `NOTIFUSE_LICENSE_KEY` optional ("not needed to run Notifuse" — free tier without it); no cloud account required. Third-party one-click hosts exist (unofficial). |

Per-category summary: **A** tx API (`transactional.send`, idempotent via `notification.external_id`), broadcasts (~200/round, ~6,000/min practical), 7 providers, inbound SMTP bridge (587/465; refuses TLS-less startup in production), attachments (20 files/3 MB each/10 MB total, inline cid:), broadcast scheduling, rounds + per-integration concurrency, **circuit breaker (pause after 5 consecutive failures)**, reply detection for automation exit (Mailgun/SES inbound). **B** domain config per provider integration, per-workspace branded endpoint URL (tracking/unsubscribe/notification center), TLS modes + Let's Encrypt via certbot DNS challenge. **C** contact profiles (fixed attribute slots: 5×string/number/datetime/JSON), public/private lists, 5 statuses w/ permanent bounce/complaint suppression, double opt-in, CSV import (auto mapping, resumable), **upsert API**, **real-time segmentation** (properties, RFM goals, activity incl. web pageviews w/ path/scroll/device/country), **preference center (11 languages, RFC 8058)**. **D** 8-step broadcast workflow, one list + segment filters evaluated at send time, **A/B w/ automatic winner** (open/click over window), **UTM auto-tagging**, **data feeds** (global + per-recipient, SSRF-protected), pause/resume/cancel, **failed-recipient retry** (`broadcasts.retryFailed`, 7-day hold). **E** visual block builder + **MJML code editor w/ Liquid**, multilingual variants (licence-gated editing), template versioning, preview + test send w/ language selector, **AI block-by-block drafting**. **F** real-time message history (8 denormalized timestamps, encrypted `message_data`, cursor pagination), provider error column, A/B results API, **web analytics (Staminads: cookieless, engaged-time, goals, geo, PG17 partitions, AGPL SDK)**, web-analytics AI queries, blog CMS. **G** canvas automations (draft/live/paused, recovery), 20+ trigger kinds + custom API events (once/every_time), nodes: delay 1min–365d / email / add+remove list / webhook (SSRF-protected), branches: filter / **A/B test (FNV-32a weighted)** / list-status, per-contact journeys w/ exit reasons incl. **`replied` (stop-on-reply)**, 10s poll ≤10 nodes/tick, node-level stats. **H** ~70-endpoint REST + OpenAPI, programmatic root signin (HMAC `email:timestamp`), permission-scoped API keys, **webhooks per Standard Webhooks spec (30+ events, 10 retries over ~34h, 410 retires, auto-disable after 20 failures >2h, delivery history + test-fire + secret regen)**, Zapier, Supabase. **I** **DB-per-workspace isolation**, 14-permission-area RBAC (non-full sets licence-gated), owner role, audit logs (16 categories, append-only DB trigger, before/after diff w/ secret redaction, CSV/NDJSON export; recording licence-gated), OIDC SSO (licence-gated), public-endpoint enumeration protection. **J** Docker + compose, multiple API containers, tracing, telemetry + update checks (both default ON, disablable), message-log observability.

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| Canvas automation engine w/ durable per-contact journeys | AUT-001/AUT-010 | features/automations.md |
| Stop-on-reply (In-Reply-To/References, Mailgun/SES inbound) | AUT-016 | features/automations.md |
| A/B testing w/ automatic winner selection | CAM-008 | broadcast-campaigns.md, get-ab-test-results.md |
| Broadcast data feeds (global + per-recipient POST, SSRF-guarded) | CAM-001 (nearest) | broadcast-campaigns.md |
| UTM auto-tagging of external links | CAM-015 | broadcast-campaigns.md |
| MJML + Liquid templating (server-side gomjml) | TPL-007/TPL-006 | features/templates.md |
| Real-time segmentation incl. web-pageview activity conditions | CON-007/CON-008 | features/contacts.md |
| Preference center, 11 languages, RFC 8058 | CON-016 | features/notification-center.md |
| Standard Webhooks-spec outbound webhooks (30+ events, history, test) | API-009/API-012/API-013 | features/webhooks.md |
| 7 native ESP provider integrations | INF-022 | integrations docs |
| Circuit breaker (5 consecutive failures → auto-pause) | INF-016 | broadcast-campaigns.md |
| Append-only audit logs w/ diff redaction (licence-gated recording) | SEC-004 | features/audit-logs.md |
| Per-workspace database isolation | SEC-001 | features/workspaces.md |
| Built-in cookieless web analytics + AI queries | ANA-016 | docs/web-analytics/* |

Verified non-features: multi-channel messaging (email only — no SMS/push/WhatsApp); scheduled transactional sends (real-time only); contact tags; free-form custom-attribute schema (fixed slots); native contact-email change (manual migration only); official SDKs; 2FA; password policies (login-code + OIDC only); MCP; CLI; inbound webhook subscriptions beyond the 7 documented providers; external PRs; Notifuse-managed sending infra in self-host mode (always BYO provider).

Limitations & security notes: license boundary precision — treat as open-core, not fully open source (RBAC/SSO/audit for compliance are paid; the ≤v39 AGPL history is the safe open-source reference); **telemetry discrepancy** — docs document `TELEMETRY`/`CHECK_FOR_UPDATES` default true while the marketing site claims "no telemetry, nothing ever leaves" (NV); SSRF hardening verified (webhooks + data feeds block private/loopback/link-local, re-checked on redirects; v42.0 added webhook URL blocking + provider-webhook message-ID verification); `SECRET_KEY` encrypts workspace integration secrets (rotation documented; loss unrecoverable); documented permission-coupling warnings (Lists write ⇒ Contacts write; Broadcasts write can send to whole audience; Automations read exposes webhook secrets); append-only audit enforceable but bypassable by DB superuser (docs acknowledge); SMTP bridge refuses TLS-less production startup; broadcast ceiling ~6,000/min (per-recipient feeds are a bottleneck); CORS default `*` on self-host; root DB credentials expose all workspace data.

NEEDS_VERIFICATION: (1) telemetry payload contents/destination; (2) per-provider DNS guidance depth beyond SES page; (3) analytics/report export beyond audit logs; (4) email tracking opt-out granularity; (5) send-time A/B (marketing-site claim only); (6) custom headers on tx sends + per-message GET; (7) message-history retention defaults; (8) coverage/release-count marketing claims; (9) health checks + backup guidance; (10) embeddable notification widget status (README vs current docs).

## 2.6 SendPortal (SP)

| Field | Value |
|---|---|
| Description | "Open-source self-hosted email marketing. Manage your own newsletters at a fraction of the cost." Laravel campaign/newsletter manager sending through external ESP APIs (SES, Postmark, SendGrid, Mailgun, Mailjet, Postal) or SMTP, with workspaces, tags, templates and ESP-webhook-based tracking. Built by Mettle (UK agency). **Unique architecture: installable as an embeddable Laravel package** (`mettle/sendportal-core`). 2.2k stars. |
| Repo (verified) | https://github.com/mettle/sendportal (+ `mettle/sendportal-core`) — GitHub API; homepage https://sendportal.io |
| License | **MIT** — verified at three independent points: raw `LICENSE` ("Copyright (c) 2020 Mettle"), `composer.json` `license: MIT`, GitHub API for both repos. No enterprise tier. No official hosted/SaaS today (a 2020–2021 hosted-beta reference is NV). |
| Languages / frameworks | PHP ≥8.3 + Laravel 13 + Livewire 3.8 + Horizon 5.50; Guzzle 7 |
| Databases / infra | MySQL ≥5.7 or PostgreSQL ≥10; queues `sync`/`database`/`redis` (Horizon recommended); **no official Docker image** (third-party Railway template exists) |
| Maintenance | **Active but slow.** v4.0.0.1 (2026-10-22, install fixes); v4.0.0 = Laravel 13 upgrade (2026-10-12); roughly one major line per 12–18 months; small-team signal — recent commits AI-co-authored ("Claude Opus 5.5"); core repo pushed 2026-11-03. |
| Self-hosting | **FULLY SELF-HOSTABLE** — clone + `composer install` on your own PHP server; requires per-minute cron (`schedule:run` — "essential") + two queue workers or Horizon; all sending via operator-configured ESP credentials; tracking webhooks land on the self-hosted instance; no Mettle-operated dependency. More manual ops than most peers. |

Per-category summary: **A** external SMTP (docs warn SMTP sends "will not be able to track your campaigns") + 6 ESP APIs, campaign fan-out ("Send Automatically" vs "Queue Draft"), Laravel queues w/ dedicated dispatch + webhook queues, scheduling via cron; **no tx send API, no inbound, no rate limiting, no retention**; attachments/headers NV. **B** none in-app — domain verification/DKIM handled in ESP consoles. **C** subscribers (email unique per workspace), **no list entity** (workspace-wide audience), **tags as the primary segmentation**, custom fields **explicitly "Not supported"**, CSV import/export, API import, search/filter, subscribed/unsubscribed statuses, unsubscribe page + resubscribe; double opt-in NV (likely absent). **D** campaigns w/ drafts, single datetime scheduling, templates, preview + test email, per-campaign tracking toggles, per-campaign message table w/ status filters, **web view placeholder**, subject/body placeholders; no A/B, no gradual sending, no duplication (NV). **E** raw HTML templates (must include `{{content}}`), **automatic CSS inlining on dispatch**, simple `{{placeholder}}` substitution (no logic), URL-placeholder caveat; no WYSIWYG/MJML/Markdown/localization/versioning/blocks. **F** open/click/delivery/bounce tracking **entirely via ESP webhooks** (per-provider endpoints `/api/v1/webhooks/{aws,postmark,sendgrid,...}`), unsubscribes per campaign, complaint events ingested (downstream handling NV); no trends dashboard, no exportable reports, no webhook signatures/retry policy. **G** none beyond scheduled dispatch + manual "Send Now" on queued drafts. **H** REST `/api/v1/` (templates, subscribers, tags, campaigns; `GET /ping`), per-workspace Bearer tokens (scopes NV), **60 req/min default throttle (configurable)**, Laravel pagination; **no SDKs — the embeddable package is the integration path**; no outbound webhooks; artisan CLI (`sp:install`, `sp:upgrade`). **I** workspaces (isolated data + switcher), owner-only member invites w/ retract, workspace-admin role only, `SENDPORTAL_REGISTER` toggle, **encrypted ESP credentials** (APP_KEY-bound), `roave/security-advisories` pinned; 2FA/SSO/audit logs NV/absent. **J** Horizon queue dashboard (Redis), Laravel-standard logging, stateless app scaling, minute-cron mandatory, no Prometheus, no media library (host images externally).

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| Embeddable Laravel core package (engine inside another app) | API-019 | docs package-installation, sendportal-core |
| 7 ESP integrations usable simultaneously | INF-022 | email-services intro |
| Automatic CSS inlining at dispatch | TPL-011 | features/templates |
| Subscriber tags as primary segmentation | CON-006 | features/tags |
| Per-campaign open/click toggles (ESP-webhook based) | ANA-012 | features/campaigns |
| Web view URL placeholder per message | CAM-014 | features/templates |
| Configurable API throttle (60 req/min default) | API-007 | api/introduction |
| Encrypted ESP credentials in DB (APP_KEY-bound) | SEC-016 | email-services intro |
| Registration toggle + password-reset toggle | SEC-017 | configuration-and-setup |
| Horizon queue dashboard bundled | OBS-009 | configuration-and-setup |

Verified non-features: custom subscriber fields (explicit docs non-feature); subscriber lists (no list entity — workspace-wide audience + tags); double opt-in (no documented flow); signup forms/widgets; marketing automation (no workflows/drips/triggers); transactional send API; A/B testing; outbound webhooks (inbound ESP webhooks only); WYSIWYG/visual editor; official Docker image and SDKs; in-app DNS/SPF/DKIM/DMARC tooling; open tracking over plain SMTP (impossible by design); MCP/official AI surfaces; fine-grained API scopes.

Limitations & security notes: tracking depends entirely on ESP webhooks and a publicly reachable instance — SMTP sends untracked; high ops burden (cron + two workers/Horizon mandatory; docs troubleshooting exists for silently-broken scheduled sends); `APP_KEY` is load-bearing (rotation ⇒ re-enter all ESP credentials); SES 1 req/s throttling trap (file cache OK small-scale, Redis recommended); slow cadence + AI-co-authored commits suggest reduced human review; coarse authorization (workspace admin is the documented role); no in-app bounce intelligence (deliverability hygiene lives in the ESP).

NEEDS_VERIFICATION: (1) historical hosted beta; (2) campaign duplication; (3) attachments + custom headers; (4) double opt-in; (5) bounce categorization + complaint suppression; (6) failed-message retry behavior; (7) health endpoint beyond `/api/v1/ping`; (8) signup forms; (9) Mailgun/Mailjet/Postal webhook details; (10) media/image hosting; (11) API token scopes.

## 2.7 Keila (KE)

| Field | Value |
|---|---|
| Description | Open-source newsletter tool positioned as a free, privacy-respecting Mailchimp/Brevo alternative. Campaigns, visual block editor + Markdown/MJML/plain-text authoring, contacts w/ JSON data + segments, signup forms w/ captcha + double opt-in, per-campaign analytics w/ tracking opt-out, JSON:API, transactional email via API. Managed EU cloud ("Keila Cloud" by Pentacent, Germany) alongside self-hosting. 2.3k stars. |
| Repo (verified) | https://github.com/pentacent/keila — GitHub API (language Elixir, pushed 2026-11-04); website https://keila.io (source `pentacent/keila-web`) |
| License | **AGPL-3.0** — verified: raw `LICENSE.md` (FSF AGPLv3) + GitHub API. **Carve-out:** the `extra/` directory (Keila Cloud glue: `keila_cloud/`, Paddle billing webhook, `license.public.pem`) is **explicitly excluded from the AGPLv3 license** — `extra/README.md`: "You may not use, modify, or distribute modified versions of this code" (© 2025 Philipp Schmieder). Core claims "100% Open Source with no proprietary premium features"; cloud-only capabilities (managed sending, managed SPF/DKIM/DMARC, shared domains, billing) live in `extra/`. No paid tier of the core. |
| Languages / frameworks | **Elixir + Phoenix 1.7.24 + LiveView** (server-rendered admin); Solid (Liquid) 1.3; MJML 6.0 (NIF); earmark (GFM Markdown); Swoosh + gen_smtp + ex_aws_ses; open_api_spex; ex_rated; argon2_elixir |
| Databases / infra | **PostgreSQL only** (ecto); **Oban 2.24** PG-backed jobs (render, delivery, scheduled dispatch, system mailer, message pruner, update check); telemetry + Phoenix LiveDashboard; Docker Hub `pentacent/keila` (sample compose explicitly not production-ready); reverse proxy must forward WebSockets |
| Maintenance | **Actively maintained, single-maintainer risk.** 0.40.2 (2026-11-04) — CVE dependency fixes + token revocation + API page caps; 0.40.0/0.40.1 security releases; steady through 2024, **~9.5-month gap in 2025**, resumed Jan 2026; effectively one maintainer (Philipp/wmnnd) — bus factor 1. |
| Self-hosting | **Fully self-hostable, production-documented; no hosted service required for core.** Sending is bring-your-own (SMTP, SES, Sendgrid, Mailgun, Postmark adapters). Phone-home limited + opt-out (update checks, tzdata). Cloud-only: "Send with Keila" managed delivery, managed DMARC, shared domains. |

Per-category summary: **A** BYO SMTP/SES/Sendgrid/Mailgun/Postmark adapters, tx API (`POST /messages`, `/messages/actions/render`, v0.30.0), campaign scheduling (`deliver_scheduled_campaigns_worker`), **per-sender rate limiting**, Oban queues, message pruner, `Precedence: Bulk` toggle, image uploads; attachments NV; no inbound; SES bounce webhook controller verified (other ESPs NV). **B** managed SPF/DKIM/DMARC **cloud-only** (CNAMEs to keila.io mx/dkim hosts); self-host DNS delegated to chosen ESP; outbound TLS certificate verification (`tls_certificate_check`); no DMARC reports/reputation. **C** contacts w/ nested JSON custom data, CSV import (replace-duplicates) + export, filtering/sorting + external IDs, **visual segment editor + Keila Query Language** (JSON, Mongo-like `$and/$or/$not/$like/$lt…`), **interaction-based segments (opens/clicks)**, signup forms w/ custom fields + drag-and-drop sorting + embedded HTML + landing pages, **hCaptcha/Friendly Captcha bot protection**, double opt-in + welcome emails (editable, 0.40.0), **signed one-click unsubscribe** (unsigned links removed 0.40.2), pre-filled "manage preferences" form links, suppression partial. **D** campaigns w/ editor choice (block/Markdown/plain text), scheduling, **reusable templates w/ content slots** (`<keila-content>`/`<keila-code>`), **public newsletter archive links** (`{{ campaign.public_link }}`), review of sent campaigns, previews (test-send NV), segment targeting, projects as multiple newsletters, **A/B testing is roadmap-only** (marketing-site mention — not implemented). **E** visual Block Editor (editor.js fork), GFM Markdown + WYSIWYG variant, first-class plain-text, MJML layouts, **Liquid templating incl. subjects**, preview text, UI in 9 languages; CSS inlining NV; no versioning/blocks library. **F** per-campaign open/click tracking, **per-campaign "do not track" + global single-click disable**, contact activity view, delivery/bounce/complaint tracking via `message_action` modules (SES verified), public-link share analytics; campaign report export NV. **G** **no automation engine (roadmap)**; automated bounce/complaint handling, double opt-in + welcome, caller-orchestrated tx triggers, automated suppression partial. **H** JSON:API + Bearer keys + OpenAPI spec (campaigns, contacts, forms, segments, senders, templates, tx messages), API rate limiting (ex_rated) + page-size cap (0.40.2), Zapier "private beta" (NV), **no user-facing outgoing webhooks**, no CLI/SDKs/MCP. **I** RBAC schema (roles/groups/permissions; catalog NV), root bootstrap user + `DISABLE_REGISTRATION`, **shared senders** (`DISABLE_SENDER_CREATION`), **sending quotas/credits** (transactional consumption), Argon2 + session revocation on password change, constant-time HMAC, **admin impersonation endpoint** (scope NV), no 2FA, no SSO/OIDC/LDAP, audit logs NV, opt-out update checks. **J** Oban/LiveDashboard job UI, message pruner, user-content storage (dir or external), official Docker image, health checks NV, no HA guidance, `LOG_LEVEL`.

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| Keila Query Language segments (JSON, Mongo-like operators) | CON-007 | docs/segments |
| Interaction-based segmentation (opens/clicks) | CON-008 | update 2025-11-04 |
| Signup forms w/ hCaptcha + Friendly Captcha, drag-and-drop fields | CON-017/CON-018 | keila-web forms.mdx, 0.40.0 |
| Double opt-in + editable welcome emails | CON-004 | double_opt_in_message.ex, 0.40.0 |
| Signed one-click unsubscribe (unsigned removed) | CON-014 | 0.40.2 changelog |
| Public newsletter archive links per campaign | CAM-012 | v0.18.0/v0.19.1, public-links.mdx |
| Template content slots (`<keila-content>`) | CAM-006 | v0.30.0, update 2026-06-24 |
| MJML + Liquid + Markdown + plain-text authoring | TPL-007/TPL-006/TPL-003 | mix.exs, docs/campaigns |
| Per-campaign + global tracking disable | ANA-012 | update 2022-09-17, homepage |
| Per-sender rate limiting | INF-012 | rate_limiter.ex, update 2022-09-05 |
| Sending quotas/credits (transactional consumption) | SEC-014 | ENABLE_QUOTAS, credit.ex |
| Shared senders for multi-tenant self-hosts | SEC-002 | DISABLE_SENDER_CREATION |
| Admin impersonation endpoint | SEC-015 | 0.40.1 changelog |
| Transactional send + render API | INF-004 | v0.30.0 CHANGELOG |

Verified non-features: no marketing-automation engine (roadmap item only); no A/B testing (roadmap item only — despite marketing "features & roadmap" listing); no user-facing outgoing webhooks; no 2FA; no SSO/OIDC/LDAP; no built-in MTA/relay; no inbound email processing; no native preference center (pre-filled forms only); cloud-only managed sending/DMARC/shared domains/billing; no template versioning/blocks library.

Limitations & security notes: single-maintainer risk + demonstrated 2025 stall; PostgreSQL-only; sample compose not production-ready; 0.40.1/0.40.2 were security releases (stored XSS in forms/campaigns, DOMPurify hardening, sandboxed preview iframes, constant-time HMAC, session revocation, API page caps, CVE-2026-116078); `extra/` carve-out means cloud code cannot be reused/modified (relevant to forking cloud features); outbound update/tzdata checks exist by default (opt-out flags) — privacy-relevant; 9-language UI but no per-subscriber campaign localization.

NEEDS_VERIFICATION: (1) file attachments on campaigns/tx; (2) campaign test-send-to-email; (3) CSS inlining approach; (4) exportable campaign/recipient reports; (5) suppression UI + automation semantics; (6) delivery-webhook coverage beyond SES; (7) API rate-limit values; (8) health endpoint + multi-node/HA guidance; (9) impersonation scope (core vs `extra/`); (10) Zapier availability (cloud only?); (11) role/permission catalog.

## 2.8 MailCheap (MC)

| Field | Value |
|---|---|
| Description | Newsletter app — write an issue, send to your list, publish on the web, **from the dashboard or from an AI agent (MCP)**. Pay-per-email alternative to beehiiv/Substack/Mailchimp; hosted cloud (mailcheap.ai, JustShipIt Pte. Ltd., Singapore) runs the same codebase with `MAILCHEAP_CLOUD=true` (adds open sign-up, plans, Stripe billing); marketing site is not open source. Author: Marc Lou. 1.6k stars. |
| Repo (verified) | https://github.com/marclou/mailcheap — GitHub API (`homepage: mailcheap.ai`, topics incl. `mcp-server`, `mongodb`); default branch `main` |
| License | **AGPL-3.0** — verified: raw `LICENSE` (FSF text) + `package.json` `"license": "AGPL-3.0-only"` + GitHub API. One earlier commit (`67d13b0`) reportedly MIT (historical footnote, NV). Self-hosted: no billing, no plan limits. |
| Languages / frameworks | **JavaScript (no TypeScript)**; Next.js 16.4.1 + React 19 + Tailwind 4; TipTap 3 editor; Better Auth (6-digit email-code + Google OAuth; **no passwords**); **MCP server** (`@modelcontextprotocol/server`, endpoint `/mcp`); Stripe (cloud-only path); `botid` bot protection |
| Databases / infra | **MongoDB via Mongoose 9.10** (28 models incl. Segment, Suppression, GlobalSuppression, PostmasterConnection); **Upstash QStash required** for background sending/scheduling (signed job calls); sending via **Amazon SES** (SESv2, per-account tenants + SNS) **or PostShiba** (per-tenant SMTP); optional Upstash Redis, S3+CloudFront/Vercel Blob, Resend, Telegram alerts; Vercel-oriented (`vercel.json` cron); **no Docker app image** |
| Maintenance | **Very active but unconventional.** Created 2026-10-10; 221 commits on `main`, last 2026-11-04; **no GitHub releases** (rolling git + Vercel); **issues disabled** ("PRs may not be reviewed"); single primary author. |
| Self-hosting | **Self-hostable (documented, no billing) — but required third-party SaaS in the critical path:** MongoDB, Upstash QStash (background sending; app must be internet-reachable), SES/PostShiba. One-click Deploy-to-Vercel or any Node 22+ host. `SENDING_ENABLED` gate; allowlist-only sign-in. Cloud-only: marketing site + DNS-helper service for shared `<slug>` domains (`DNS_BROKER_URL`, not in repo). A fully air-gapped install is not what the docs describe. |

Per-category summary: **A** SES tenants (SESv2) or PostShiba sending, campaign scheduling, **gradual sending** (2% random first, +5%, rest 30 min apart; engaged-first + "Send to more"), QStash batch jobs (signed), throttled-batch requeue + hourly-cap backoff, **rate limiting 300 req/min per key** + hourly action limits + optional shared Redis, welcome/confirmation emails, Feedback-ID header + RFC 8058 + signed unsubscribe links, Gmail 100 KB clip pre-send check + postal address check; **no inbound receiving**; attachments NV. **B** domain verification w/ DKIM/SPF/DMARC per-record checks, **hourly DNS re-verification cron**, from-address restriction to verified domains (re-checked per send), per-newsletter reply-to validation, Gmail Postmaster Tools (spam rates), SES tenant pause/reputation events via EventBridge→SNS, shared domains via external DNS helper (partial). **C** up to 3 newsletters/account w/ isolated contacts, contact attributes + sections, provider-event status tracking, **confirmed opt-in default + manual-add attestation**, one-click unsubscribe, **CSV import from 9 platforms** (beehiiv, Substack, Mailchimp, Kit, MailerLite, Buttondown, Ghost, EmailOctopus, listmonk) w/ status preserved, **import cleaning** (invalid/misspelled/spam traps/role/throwaway/duplicates/dead domains), expiring signed-link CSV exports, **server-wide hashed GlobalSuppression** (180d hard bounce, permanent spam trap), per-account suppression, **list-cleanup re-permission email**, signup forms + signup API; Segment model exists but undocumented (NV). **D** campaign editor + drafts (6-status enum), scheduling, **pre-send review checks** (subject, preview text, unsubscribe link, 100 KB clip, postal address, sender), test sends (≤5 addresses, caps), gradual sending + "Send to more", **auto-pause/block on thresholds (pause 2.5% bounce / 0.08% complaint; block 5% / 0.3%; spam-trap block)**, campaign reports incl. **per-inbox-provider** + link-level, **public newsletter website** (issue pages, signup, author page, sitemap, custom domain); no A/B; duplication NV. **E** TipTap rich-text (slash commands, images, boxes), **Markdown + raw HTML via API/MCP**, newsletter sections/blocks, **AI-assisted creation via external MCP agents**; themes NV; no drag-and-drop/MJML/React Email. **F** open/click tracking **excluding link scanners**, provider-webhook delivery/bounce/complaint tracking, 30-day overview dashboard, per-inbox-provider reports, **Gmail spam-rate monitoring (pause 0.08% / block 0.3% / domain alert 0.1%)**, link-level tracking; no no-tracking mode; report exports NV. **G** welcome emails, confirmation/double-opt-in, list-cleanup re-permission, admin alerting (dashboard + email + Telegram, deduped 6h); **no sequences/workflows/branches**. **H** REST v1 + OpenAPI served per instance (`/api/v1/openapi.json`, `/docs`) with a **parity test suite that fails if a dashboard action is missing from REST/MCP/OpenAPI**, **MCP server exposing every dashboard action** (send annotated destructive), **OAuth for ChatGPT/Claude** (DPoP, PKCE S256, RFC 9207 issuer, consent, RFC 7591 dynamic registration), API keys (32-byte, SHA-256 hashed, shown once, 2 scopes: full/signups-only, ≤20 active, instant revocation), SES/PostShiba/Stripe webhooks, optional integrations (Resend, Google, Postmaster, Telegram, Redis); no outbound user webhooks; no SDKs. **I** multi-account w/ strict tenant isolation (Mongoose accountId guard plugin + isolation/API/MCP tests), teams ≤5 members (admin/member, allowlist-gated invites), admin panel (404 to non-admins) + alert taxonomy, passwordless sign-in (hashed 6-digit codes 5 min/3 tries + Google), **credential encryption** (Postmaster refresh token + PostShiba creds via `CREDENTIALS_ENCRYPTION_KEY`), owner-only account deletion (code-confirmed, sessions/keys revoked, 30-day purge + restore, HMAC'd deletion record), allowlist-only sign-in (checked every route), bot protection + CORS open only for signup + SSRF guards on image-by-URL + signed tracking links; no audit logs; no RBAC beyond admin/member. **J** `/api/health` (503 names the fix), Vercel Cron + external-scheduler pattern with `CRON_SECRET`, stateless scaling, admin alert surface, server-side logging (`internal_error` without detail to clients), import resumption/abort detection; no Docker image/self-contained queue runtime.

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| MCP server with full dashboard parity (enforced by tests) | API-016 | README, app/mcp, tests/mcp.test.mjs |
| OAuth for AI agents (DPoP, PKCE S256, RFC 9207/7591) | API-003 (nearest) | SECURITY.md |
| Auto-pause/block on bounce/complaint thresholds | INF-016 | README "spam and bounce limits" |
| Gradual sending ramp (2% → +5% → rest, engaged-first) | INF-015 | README Features |
| Import cleaning (spam traps, misspellings, role, throwaway) | CON-020 | README "contact cleaning" |
| Server-wide hashed global suppression (180d / permanent spam trap) | INF-018 | README, SECURITY.md, GlobalSuppression |
| CSV imports from 9 platforms w/ status preserved | CON-009 | README Imports |
| Gmail Postmaster Tools spam-rate integration | DOM-011 | README, PostmasterConnection.js |
| Per-inbox-provider deliverability reports | ANA-009 | README |
| Pre-send review checks (incl. Gmail 100 KB clip, postal address) | CAM-007 (nearest) | README "Review shows what to fix" |
| List-cleanup re-permission email (dormancy) | CON-011 (nearest) | README "list cleanup" |
| Tenant-isolation guard plugin + test suites | SEC-001 | SECURITY.md, tests/isolation.test.mjs |
| Hourly DNS re-verification of verified domains | DOM-008 | README Optional |
| Health endpoint naming what to fix | OBS-008 | README §2, SECURITY.md |

Verified non-features: inbound email receiving; A/B testing; drag-and-drop template builder; MJML/React Email templating; official SDKs; outbound user webhooks; password auth/TOTP 2FA; Docker app image; plan limits/billing on self-hosted (by design); marketing site not open source; issues/PR support effectively off.

Limitations & security notes: third-party SaaS in the critical path (QStash mandatory for background sending/scheduling; MongoDB + provider mandatory) — not air-gappable; sending gated behind `SENDING_ENABLED=true`; server refuses weak `BETTER_AUTH_SECRET`/`SUPPRESSION_HASH_KEY`; suppression list stores only keyed hashes (rotating `SUPPRESSION_HASH_KEY` empties it); 180-day hard-bounce suppression, permanent spam-trap entries, deletion record keeps only HMAC'd email; SES path uses a broad `Resource: "*"` IAM policy; Postmaster Tools needs a published Google consent screen (else connection expires in 7 days); Gmail 100 KB clip + postal address enforced pre-send; issues disabled + single author = bus-factor/support risk.

NEEDS_VERIFICATION: (1) contact search/filter UI specifics; (2) Segment model usage (segment targeting); (3) campaign duplication; (4) campaign/analytics report exports; (5) email attachment support; (6) themes + phone previews in code; (7) event-data retention configuration; (8) exact MCP tool list (served at per-instance `/docs`); (9) details of the early MIT-licensed commit.

## 2.9 Sessy (SE)

| Field | Value |
|---|---|
| Description | **Email observability dashboard for teams sending with raw Amazon SES.** Sessy sends, relays, and manages nothing: you point an SES configuration set's event destination at an SNS topic that pushes to a per-source webhook on your Sessy instance; it stores, categorizes, and visualizes the full event stream. Positioning: against "overpriced email services that are often just glorified SES wrappers." By Marc Köhlbrugge (BetaList/WIP). 945 stars. |
| Repo (verified) | https://github.com/marckohlbrugge/sessy — GitHub API (language Ruby, homepage https://sessy.do, created 2025-12-30) |
| License | **NONE (source-available, not OSI open source)** — GitHub API `NOASSERTION`. `LICENSE.md` = **"O'Saasy License Agreement"** (© 2026 Killbridge Ventures Pte. Ltd.): use/modify/distribute/sell permitted **with attribution retained**, except an **anti-SaaS clause** (no offering the software's functionality as a hosted/managed product where that is the primary value) — violates OSD #6. README's "open-source" branding overstates it. Sub-carve-outs: `.claude-plugin`/`.cursor-plugin` packages MIT; the `saas/` hosted-edition engine ships its own O'Saasy copy and "is not meant to be used by third parties". No enterprise tier — hosted Sessy (beta, free during beta) is the monetization. |
| Languages / frameworks | Ruby on **Rails**; ERB + Tailwind + Stimulus; **Solid Queue + MissionControl::Jobs** (dashboard at `/jobs`); built-in **MCP server** at `/mcp` (streamable HTTP); Rails health `GET /up` |
| Databases / infra | **SQLite by default** (mounted `/rails/storage`); **PostgreSQL auto-detected** via `DATABASE_URL`; Docker `ghcr.io/marckohlbrugge/sessy:main`; Kamal/Dokku/compose docs; **SNS push ingestion — no AWS credentials stored**; CloudFormation "Launch Stack" creates config set + SNS topic + webhook subscription in **your** AWS account (no IAM resources) |
| Maintenance | **No releases** (zero tags; rolling `:main` image, git revision baked in + update-check banner vs GitHub `main`). 407 commits; last push 2026-11-03 — very active, single maintainer, "still in a very early stage". |
| Self-hosting | **Fully self-hostable, explicitly designed for it; no dependency on Sessy's own hosted services.** One-command `docker run`; HTTP Basic auth for dashboard (webhooks stay open for SNS); the only external service is **your own AWS account**; update checks phone GitHub only; `saas/` engine excluded from the default bundle (test suite verifies identical behavior without it). |

Per-category summary: **A** none (no sending/SMTP/tx API — by design); event ingestion pipeline (SNS → tokened webhook → raw `webhooks` table); partial message storage (ses_message_id/subject/source/timeline; full MIME not evident). **B** none (docs guide AWS-side domain/DKIM/SPF/DMARC only); one-click SES wiring (Launch Stack) + per-source setup wizard. **C** none; SES `subscription` events ingested/viewable (observation only); per-recipient bounce/complaint records searchable. **D** none (per-source + per-message analytics instead). **E** none. **F** (core strength) full SES event coverage — **10 types** (send, delivery, bounce, complaint, reject, delivery_delay, rendering_failure, subscription, open, click); real-time dashboard + activity feed (search by recipient/subject, filter type/date); **bounce categorization** (permanent/transient/undetermined + subtype + backfill job); **per-message bounce diagnostics**; complaint-spike monitoring; open/click unique counts; 30-day trend charts; per-source analytics + retention; **exportable logs**; account + per-source `retention_days` w/ scheduled purge. **G** automatic retention enforcement; SNS subscription confirmation handling; **no suppression/bounce actions** (observation only). **H** SES-via-SNS integration ("no AWS credentials — just a webhook URL"); multi-environment sources (own webhook URL/region/color/retention); **MCP server** (search events, `get_message` w/ delivery timeline, `email_stats`, `create_source`, SES wiring; read-only email data); hashed API keys (digest + prefix + `last_used_at`); **no generic REST/SDKs/CLI/outbound webhooks**. **I** multi-user accounts (accounts/memberships/users/sessions; role column defaults "owner"); passwordless **magic-link** auth; HTTP Basic for dashboard; API keys independent of Basic auth (documented caveat); webhook security thin (token-in-URL; SNS signature validation NV); 2FA/audit logs absent; hosted-only approval gate/admin quarantined in `saas/`. **J** MissionControl::Jobs dashboard; `GET /up`; update-check banner (GitHub only); SQLite→PostgreSQL scaling path; Kamal/Dokku guides; no Prometheus/Grafana (native dashboard is the surface).

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| Full SES event-stream ingestion (10 event types) | API-014 (nearest) | app/models/event/types.rb |
| Bounce categorization (permanent/transient/undetermined + subtype) | ANA-004 | Event::Types, bounce_subtype_backfill.rb |
| Per-message delivery timeline w/ bounce diagnostics | ANA-008 | event/diagnostics.rb, MCP get_message |
| Complaint-spike monitoring | ANA-010 | sessy.do |
| Per-source analytics + per-source retention policies | ANA-007 | source_scoped.rb, schema |
| Exportable logs (search/filter/export) | ANA-011 | sessy.do |
| MCP server for AI agents (read-only email data) | API-016 | /mcp, mcp_server/* |
| Account + per-source retention w/ scheduled purge | INF-017/SEC-008 | retention_days, delete_expired_data_job.rb |
| One-click AWS wiring via CloudFormation Launch Stack | API-014 (nearest) | docs/aws-ses-setup.md |
| Multi-environment source isolation | SEC-001 (nearest) | sources schema, sessy.do |
| Jobs dashboard (MissionControl) + Rails `/up` health | OBS-009/OBS-008 | README, routes.rb |

Verified non-features: no sending, relaying, SMTP, or transactional email API; no domain/DKIM/SPF/DMARC management (AWS-side, docs guide only); no suppression management or bounce-triggered actions; no contacts/campaigns/templates/unsubscribe flows; no outbound webhooks/SDKs/CLI/generic REST query API (MCP + UI only); no Prometheus/Grafana endpoints; hosted-only onboarding controls quarantined in `saas/`.

Limitations & security notes: SES-only (any other provider out of scope); license is source-available with anti-SaaS clause — fine for internal use/self-hosting, cannot be offered as a hosted product; no versioned releases (rolling images make pinning/auditing harder); webhook security model thin by design (per-source token URL; SNS message-signature validation not evidenced — check `docs/ses-security-best-practices.md` before exposing `/webhooks/*`); MCP caveats (Cloudflare bot protection must exempt `/mcp`; Basic auth and API keys are independent — locking Basic auth doesn't revoke API keys); open/click data depends on SES config-set event destinations; single maintainer; hosted-beta pricing unknown.

NEEDS_VERIFICATION: (1) SNS message-signature validation in ingest; (2) whether Launch Stack enables open/click destinations by default; (3) roles beyond "owner"; (4) default retention values + per-source UI configurability; (5) future hosted pricing; (6) raw email content/MIME viewing (no body column evident); (7) O'Saasy template provenance.

## 2.10 Hyvor Relay (HR)

| Field | Value |
|---|---|
| Description | Open-source, self-hosted **email API for developers** — "Cloud & Self-hosted Alternative to SES, Mailgun, SendGrid". Deploy your own instance (your servers, public IPv4s), connect domains with **enforced DKIM**, send via idempotent REST API or SMTP backed by Relay's own Go SMTP server. Pure infrastructure: no newsletter/campaign UI. Delivery engine of the Hyvor ecosystem — **Hyvor Post delegates delivery to Relay**. 920 stars. |
| Repo (verified) | https://github.com/hyvor/relay — GitHub API (homepage https://relay.hyvor.com, created 2025-05-29); docs `/docs`, self-hosting `/hosting` |
| License | **AGPL-3.0** — verified: raw `LICENSE` (canonical FSF text) + GitHub API. **Enterprise tier: yes** — licenses "from €10,000/year" for orgs wanting a commercial license / AGPL exemption (docs: same features; open-source features "will remain available"). Cloud Standard (~€30/mo, 300k emails/mo) announced Q1 2026 — **not open for signups** as of 2026-11-05 (waitlist); treat cloud claims as planned. No CLA found. |
| Languages / frameworks | **PHP + Symfony** (API); **Go single binary** (email workers + SMTP server + DNS server + webhook handlers); **SvelteKit** console (Hyvor Design System); **OIDC-only auth** (no built-in login system, explicitly never planned) |
| Databases / infra | **PostgreSQL** ("database as well as the queue"); Docker Compose or Swarm; single image containing API + workers + bounce SMTP + webhooks + DNS (host network); `hyvor/smtp-simulator` for dev; Prometheus/Grafana/Alertmanager integrations |
| Maintenance | **Active, pre-1.0.** 0.0.48 (2026-10-21); pushed 2026-11-03; ~monthly-to-quarterly 0.0.x cadence; roadmap maintained in-repo (`ROADMAP.md`); Hyvor forum + Discord. |
| Self-hosting | **Fully self-hostable; self-hosting is the primary supported mode.** Easy Deploy (single server) + Prod Deploy (multi-server HA via Swarm); PostgreSQL is the only external dependency; DNS delegation of your instance domain to Relay's **built-in DNS server** via NS records; host network. **OIDC provider is a mandatory operational dependency** (your own Keycloak/Authentik/etc.). L3/L4 DDoS protection explicitly the operator's responsibility. |

Per-category summary: **A** tx REST (`POST /api/console/sends`, Bearer), **idempotent sends** (`X-Idempotency-Key`, 24h storage, short-circuit header), own Go SMTP server (587/25; password = API key; converts SMTP→API), attachments (≤10, 10 MB total), restricted custom-header list, ≤20 recipients, **two queues isolating transactional vs distributional (IP reputation)**, rate limits (10 req/s send endpoint; 100 req/min console) w/ `X-RateLimit-*` + `Retry-After`, automatic greylisting/retries, **SMTP conversation logs (30 days)**; scheduling NV; inbound routing planned; dedicated IPs planned; "distributional" queue but no campaign UI (bulk ◐). **B** TXT domain verification (checked every 5 minutes), **domain lifecycle states** (pending 14-day auto-removal / verified / warning 24h / suspended), **enforced DKIM signing** (unique selector per project), custom DKIM selector (API docs say yes; ROADMAP says planned — discrepancy), **built-in DNS server** managing forward/MX/SPF/DKIM records via NS delegation, rDNS/multiple IPs listed, DMARC NV, domain CRUD API w/ async verify + `domain.verified` webhook, multi-project domain reuse, irreversible domain deletion. **C** **no contact management** (recipient-agnostic); project-scoped suppression lists (hard bounces auto-added; list/delete API; `suppression.created/deleted` webhooks); complaint-based suppression + feedback loops; no unsubscribe machinery (app-level concern). **D** none (explicit product boundary; Hyvor Post is the newsletter layer). **E** none — **templates explicitly "will not implement"** per ROADMAP. **F** send/delivery analytics (`analytics.read`: sends, bounce rate, complaint rate, chart data); per-recipient events (`send.recipient.accepted/deferred/bounced/complained/suppressed/failed`); searchable send logs w/ raw content + SMTP conversations (30d); **open/click tracking explicitly never planned** (app-level concern); exportable reports NV. **G** auto-suppression on hard bounce; automatic retries/greylisting; feedback-loop processing; automated domain hygiene (5-min checks, 14-day removal, 24h warning). **H** send API + console management API (sends list/get/retry, domains, webhooks CRUD + deliveries, API keys ≤10/project, suppressions, analytics); **scoped keys** (`sends.send`, `domains.*`, `webhooks.*`, `api_keys.*`, `suppressions.*`, `analytics.read`); 429 semantics w/ headers; **HMAC-SHA256 signed webhooks (`X-Signature`) — 11 event types — 6-retry ladder (1m→24h) w/ delivery status visible**; no event filtering; no SDKs/CLI/MCP; no upstream-provider bridging (Relay *is* the infrastructure). **I** multi-tenancy w/ isolated projects; OIDC-only auth (mandatory); scoped project-bound keys (shown once); layer-7 rate limiting built in; L3/L4 DDoS = operator duty; audit logs/2FA NV (2FA delegated to OIDC). **J** horizontal scaling (add servers/workers; Swarm path); Prometheus/Grafana/Alertmanager; health checks + alerts (server issues, DNS errors, **IP reputation drops**); PG-backed queue (per-queue stats UI NV — Prometheus covers metrics); single-image packaging + simulator; benchmark tooling in repo.

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| Built-in DNS server w/ NS-delegation automation (MX/SPF/DKIM) | DOM-007 | README, /hosting |
| Enforced DKIM signing (mandatory for all sending) | DOM-003 | /docs/domains |
| Domain lifecycle states + 5-min re-verification | DOM-008 | /docs/domains |
| Transactional vs distributional queue isolation | INF-011 | README |
| Idempotent send API (24h keys) | INF-031 | /docs/send-emails |
| Own Go SMTP server + inbound submission | INF-002 | README, /docs/send-emails-smtp |
| HMAC-signed webhooks w/ 6-step retry ladder | API-010/API-011 | /docs/webhooks |
| Scoped API keys (10 scopes, ≤10/project) | API-004 | /docs/api-console |
| SMTP conversation logs (30 days) | ANA-013 | README, Console → Sends |
| Suppression API + suppression webhooks | CON-013 | /docs/api-console, /docs/webhooks |
| Prometheus + Grafana + Alertmanager integrations | OBS-006/OBS-007 | /hosting |
| Health checks incl. IP-reputation-drop alerts | OBS-008 | homepage, /hosting |
| Multi-tenancy w/ isolated projects | SEC-001 | README, /docs |

Verified non-features (ROADMAP "Features we will not implement" + docs): in-built authentication (OIDC only, forever); in-built open/click tracking (deliberate privacy decision); templates; contact lists / campaigns / unsubscribe flows (Hyvor Post's domain); SDKs/CLI/MCP; incoming email routing (planned, not yet); dedicated IPs (planned, not yet); upstream ESP bridging.

Limitations & security notes: pre-1.0 — API surface may change; instance runs **public SMTP + DNS + HTTP** on host network (L3/L4 DDoS protection is yours); OIDC mandatory (cannot run Console without an external IdP); DKIM mandatory (unverified domains can't send, auto-removed after 14 days; TXT removal triggers 24h "Warning"); **ROADMAP vs API discrepancy on custom DKIM selectors** — verify against code before relying; fixed 30-day log retention (configurability NV); AGPL §13 applies (€10k/yr enterprise buy-out exists); cloud pricing/launch unshipped — treat as planned. Positive: idempotency, signed webhooks with sensible ladder, queue isolation, in-instance DNS automation are unusually strong for v0.x.

NEEDS_VERIFICATION: (1) DKIM key type/rotation details; (2) custom DKIM selector implementation status; (3) log-retention configurability; (4) webhook management UI beyond delivery status; (5) Cloud Standard availability/pricing; (6) SMTP rate limits.

## 2.11 Wraps (WR)

| Field | Value |
|---|---|
| Description | CLI + TypeScript SDK that **deploys production-ready AWS email infrastructure (SES, Lambda, DynamoDB, EventBridge, SQS, S3/CloudFront) into the customer's own AWS account**, plus an optional hosted platform (dashboard, API, contacts/templates/workflows/broadcasts, billing). "Modern DX. AWS economics. Full ownership." Email + SMS + CDN product lines. "Wraps is never in the sending path." Author: Jarod Stewart. 66 stars, ~weekly CLI releases. |
| Repo (verified) | https://github.com/wraps-team/wraps — GitHub API (TypeScript, homepage https://wraps.dev). Secondary: `wraps-team/wraps-js` (TS SDKs + MCP, MIT), `wraps-team/skills` (agent skills) |
| License | **AGPL-3.0 core + proprietary Enterprise Edition carve-out** — verified: root `LICENSE` ("Content outside of the above mentioned directories… under the 'AGPLv3' license") + `ee/LICENSE.md`: all `apps/web/src/.../(ee)`, `apps/api/src/(ee)` directories (dashboard org pages, parts of API, AI features) are proprietary — production use requires a valid commercial license; evaluation-only otherwise (contact support@wraps.dev). GitHub API `NOASSERTION` (consistent); wraps-js is MIT. SPDX summary: AGPL-3.0 (core) + LicenseRef-Proprietary (EE dirs) + MIT (wraps-js). |
| Languages / frameworks | TypeScript (strict); **SST** (`sst.config.ts`) for the self-host control plane; **Pulumi** (CLI deploys customer resources) + `@wraps.dev/cdk` / `@wraps.dev/pulumi` packages + CloudFormation dir; **React Email (TSX)** templates; pluggable AI (Vercel AI Gateway/OpenAI/Anthropic/**AWS Bedrock — self-hosted-only**) |
| Databases / infra | Customer AWS: SES, EventBridge, SQS+DLQ, Lambda, DynamoDB (TTL), S3 (inbound bucket; CDN origin), CloudFront, IAM (least-privilege, OIDC); namespaced `wraps-email-*`/`wraps-sms-*`, tagged `ManagedBy: wraps-cli`. Control plane: apps/web + apps/api on **Postgres via Drizzle** (BYO Neon/Supabase/Railway/self-hosted; SaaS = Wraps' own Neon US); Better Auth. Distribution: npm `@wraps.dev/{cli,email,sms,client,mcp,cdk,pulumi}`, PyPI `wraps-email`. Node 22+, pnpm 10+. |
| Maintenance | **Very high release velocity.** Created 2025-11-07; last push 2026-11-04; CLI v3.20.1 (2026-11-04), v3.20.0 (11-01), v3.19.0 (10-31), v3.18.0 (10-29) — roughly weekly; changelog at wraps.dev/changelog; GitHub Actions CI + Vitest; low star count but active. |
| Self-hosting | **Self-hostable control plane DOCUMENTED but licence-gated — NOT fully FOSS.** `pnpm selfhost:deploy` deploys the full control plane into your AWS account via SST ("no Wraps SaaS servers are in the critical path") — **but a vendor-issued enterprise license key (`wraps_lic_…`) is a required prerequisite**, and the EE directories it runs are precisely the dashboard/API/AI code; production use without a commercial agreement is prohibited for that code. Offline license verification; one plane per AWS account; upgrades fork-based (weekly sync PR); without a Sentry DSN the stack "reports errors nowhere". SaaS mode centralizes contacts/templates/workflows/audit on Wraps' hosted Neon Postgres (US). |

Per-category summary: **A** tx + bulk sending via SES (`send`/`sendBatch`; SDK/CLI/REST/MCP — **no SMTP**), broadcasts (draft/send/cancel/resume `--from-chunk-index`/duplicate), broadcast scheduling (`--scheduled-for`), SQS+DLQ queues, retention (DynamoDB TTL 90/365d presets; plan history windows 30d/90d/1y), **inbound email** (SES receipt rules → S3; inbox SDK list/get/forward/reply/delete; "Wraps stores nothing"; attachments metadata-only; >200 KB HTML truncated); custom headers incl. RFC 8058 one-click for marketing sends; gradual sending NV; **SMS + CDN adjacent lines**. **B** domain verification + DKIM/SPF/DMARC via CLI, domain status via MCP/CLI, **suppression management (SES account-level; complaint removal needs `--acknowledge-complaint`)**, **account-health hourly sweep** (`wraps email health`; Account Health API guide), sandbox handling (`wraps doctor`). **C** contact CRUD + rich attributes (names, company, jobTitle, externalId, preferredChannel, free-form `properties` JSON, engagement counters), **topics** (subscriptions, double opt-in, public flag), **segments (filter-tree; Pro plan `PLAN_REQUIRED`)**, status engine (active/soft_bounced/unsubscribed/bounced/complained/suppressed; **3-soft-bounce→bounced rule; auto-reactivate on delivery**), CSV/JSON import ≤10k rows (dedupe that "never overwrites consent"; optional paid SES address validation), export ≤50k rows, unsubscribe + **preference centre** (`{{preferencesUrl}}`), bulk template sends; no tags. **D** broadcast drafts (API cannot edit drafts), immediate + scheduled sends, cancel/resume, duplication (keeps content/audience/sender, not schedule), targeting all|topic|segment (topic/segment = Pro), per-broadcast stats; test sends NV; no A/B; no public archives; gradual sending NV. **E** **React Email templates as code** (TSX; metadata exports for subject/email type/preview text), CLI lifecycle (init → `preview` localhost:3333 hot-reload → `push` to SES + dashboard; `--dry-run`/`--force`), **brand kit** (`brand.ts` + shared `_components/`), `{{var}}` variables incl. subject, unsubscribe/preference signed URLs, push to SES native templates, **AI-assisted generation (metered per plan; partially EE)**; no drag-and-drop/MJML ("just code"); dashboard editor/versioning NV. **F** event pipeline (EventBridge → SQS → Lambda → DynamoDB; sends/deliveries/opens/clicks/bounces w/ subtypes/complaints), event history w/ TTL retention, `wraps email metrics` (hourly→monthly; dimensions domain/broadcast/template), zero-filled daily series, account health sweeps, dashboard analytics w/ plan-gated windows; exports NV; no-tracking mode NV. **G** **workflows as TypeScript** (`defineWorkflow`; steps `sendEmail`/`delay`/`condition`/`waitForEvent`/`exit`; triggers `contact_created`, event/API `POST /v1/workflows/{id}/trigger`, `schedule`), executions tracked on platform, **agent email w/ approvals/policies + guarded MCP `send_email`**, AI workflow generation; **no visual builder** ("No drag-and-drop builders, no YAML — just code"); workflow dry-run/test NV. **H** REST v1 (OpenAPI 3.0.3 at api.wraps.dev/swagger/json; error-code reference), hashed API keys, **OAuth Device Authorization Grant** (`wraps auth login`), TS SDKs (`@wraps.dev/email|sms|client`) + Python `wraps-email`, full CLI, **MCP ×2 (hosted docs server + local stdio: list_recent_sends, get_email_event_log, verify_domain_status, list_suppressions, guarded send_email)**, CDK constructs + Pulumi components, event webhooks guide, custom events (`POST /v1/events` — the only metered resource), **agent ecosystem (llms.txt, agent card, 8 Claude Code skills)**; no non-AWS providers. **I** multi-org model (orgSlug-scoped; org features partially EE; RBAC NV), **cross-account IAM role w/ `sts:ExternalId`** (deleting the role revokes platform access), **OIDC "zero credentials stored"**, platform audit log (plan-gated retention), hashed keys, suppression complaint-ack safety, `wraps_lic_` offline verification; 2FA NV; key/IP restrictions NV. **J** namespaced+tagged resources, config presets (Starter/Production/Enterprise), CloudWatch Logs (customer-owned), SQS DLQ, `wraps doctor`, `wraps selfhost status/connect`, CI (Vitest/oxlint/biome), Sentry optional.

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| Deploy-into-your-AWS-account ownership model (IaC: SST/Pulumi/CDK) | API-019 (nearest) | README, llms.txt, sst.config.ts |
| Workflows as TypeScript (steps incl. waitForEvent) | AUT-001/AUT-005/AUT-006 | docs quickstart/email/workflows |
| React Email templates as code w/ CLI push to SES | TPL-008 | quickstart/email/templates |
| Brand kit (brand.ts + shared components) | TPL-013 | quickstart/email/templates |
| Topics w/ double opt-in; plan-gated filter-tree segments | CON-002/CON-007 | llms-full.txt |
| Status engine (3-soft-bounce rule, auto-reactivation) | CON-003 | llms-full.txt |
| Account-health hourly sweep + SES reputation | DOM-011 | guides/account-health |
| Dual MCP servers (docs + operational w/ guarded send) | API-016 | docs/mcp-reference |
| CDK constructs + Pulumi components as IaC alternatives | API-019 (nearest) | README, docs/cdk-reference |
| OAuth Device Authorization Grant for headless CLI | API-003 (nearest) | docs/cli-reference/auth |
| Cross-account IAM w/ ExternalId; OIDC zero-credentials | SEC-006 (nearest) | llms.txt Platform page |
| Inbound email (S3 receipt + inbox SDK) | INF-029 | quickstart/email/inbound |
| Agent ecosystem (llms.txt, agent card, Claude skills) | API-016 (nearest) | README, .well-known |

Verified non-features: no SMTP relay/credentials provisioned (SES-direct only); no drag-and-drop builders or YAML (templates/workflows); no A/B testing; no public newsletter/archive pages; no tags (topics/segments instead); API cannot edit broadcast drafts; no non-AWS email providers; no visual workflow builder.

Limitations & security notes: **license asymmetry is the headline** — the dashboard org/API/AI EE code is proprietary; production self-hosting requires a commercial agreement even though the repo is public under AGPLv3 (do not classify Wraps as fully self-hostable open source on repo-publicness alone); SaaS mode centralizes customer data on Wraps' hosted Neon Postgres (US) — data-residency consideration; plan-gated history windows (30d/90d/1y) + DynamoDB TTLs bound event history; no SOC 2/ISO 27001 (vendor positions the AGPL codebase as the auditability story); documented self-host gotchas (DB connection limits across six Lambdas — pooled endpoints; OIDC trust-policy mismatch pitfall; `NEXT_PUBLIC_APP_URL` change strands org logos; `selfhost:destroy` leaves email infrastructure behind; one plane per AWS account); AI features fail without a configured provider (metered); vendor-issued license key is a vendor interaction for obtaining/renewing.

NEEDS_VERIFICATION: (1) broadcast test sends; (2) gradual sending for broadcasts; (3) outbound attachments; (4) dashboard template-editor capabilities; (5) template versioning; (6) localization; (7) exportable analytics reports; (8) 2FA; (9) API-key IP restrictions; (10) org RBAC specifics (EE code, undocumented); (11) rate-limits page details + wraps-js npm relation (repo shows 1 star vs 1.1k-star npm listing per search).

## 2.12 Hyvor Post (HP)

| Field | Value |
|---|---|
| Description | **Privacy-first newsletter platform by HYVOR (SARL, France)** — "For People, Not Funnels." Defining constraint: **open and click tracking do not exist** ("no hidden switch to turn on tracking"). Multiple newsletters per account, list-based subscribers w/ metadata, embeddable signup form web component (double opt-in default), Markdown-friendly rich-text editor, customizable Twig templates, auto-generated archive website, team collaboration, Console API. **All delivery delegated to Hyvor Relay** (separate repo/dossier). 34 stars, pre-1.0. |
| Repo (verified) | https://github.com/hyvor/post — GitHub API (description "Newsletters without surveillance", homepage https://hyvor.com/post, created 2025-01-07, pushed 2026-10-26) |
| License | **AGPL-3.0** — verified: raw `LICENSE` (FSF text) + GitHub API + official open-source page FAQ. Enterprise tier: none yet; README notes enterprise licenses will be offered once self-hosting ships. **Inconsistency flag:** `backend/composer.json` declares `"license": "proprietary"` while repo LICENSE/README say AGPL-3.0 (likely scaffold default; unresolved metadata conflict). Companion Hyvor Relay is AGPL-3.0 with paid enterprise licenses. |
| Languages / frameworks | **PHP ≥8.4 + Symfony 8.0**: Doctrine (+ JSON-ODM metadata), Messenger (worker scaling via `WORKERS`), Scheduler, Mailer, RateLimiter, Lock, **Twig + `twig/cssinliner-extra`**, `hyvor/phrosemirror` (editor data model), nelmio api-doc (OpenAPI), monolog, Sentry (optional), zenstruck messenger-monitor, aws-sns-message-validator (Relay webhook validation); **FrankenPHP** runtime; SvelteKit ×3 (console / embed form / archive) |
| Databases / infra | **PostgreSQL** (`DATABASE_URL` single source of truth); delivery 100% via **Hyvor Relay API** (`RELAY_URL`/`RELAY_API_KEY` — Post never talks SMTP); storage adapters s3/file/memory; dev compose (Traefik) **dev-only**; `Dockerfile` production `final` target; `DEPLOYMENT=cloud|on-prem` env surface |
| Maintenance | **Actively maintained, young, pre-1.0.** 0.0.63 (2026-10-21); rapid 0.0.x trains (10 releases Aug–Oct 2026; bodies carry no notes); ≥4 recurring contributors, PGP-verified commits; **CVE-2026-109618/109619 fixed within days** (Oct 2026); API rate limiting added 2026-10-26; 37 open issues (incl. unsubscribes-not-recorded bug #478). |
| Self-hosting | **NOT self-hostable in a supported way as of 2026-11-05 — transitional, with internally contradictory official docs.** README: "not yet ready for self-hosting" (OIDC sign-in + docs pending); open-source FAQ: "Not in a supported way yet"; **privacy-first page contradicts: "Yes… run it on your own server with Hyvor Relay"** and a Hosting guide is published (Relay setup, API-key scopes, `RELAY_*`/`SYSTEM_MAIL_DOMAIN` env). Code has `DEPLOYMENT=on-prem` + production Docker target but only a dev compose; no production compose/Helm/install guide. Dependencies for a would-be self-host: Hyvor Relay, OIDC/Hyvor Core, S3/local storage. Treat any self-host attempt as unsupported (§5). |

Per-category summary: **A** sending via Relay API; newsletter sending w/ Symfony Messenger queues + `WORKERS` scaling; throttling (`MAX_EMAILS_PER_SECOND`, default 8); retries/greylisting/bounce-handling/suppressions all **[relay]**; **no tx email in Post** (Relay's job); **no attachments** (images only, ≤10 MB); soft-delete newsletters (permanent after 30 days); issue scheduling (draft/scheduled/sending/sent). **B** custom sending domains per newsletter via Relay (`domains.read/write` scopes); DNS automation/DKIM **[relay]**; DMARC NV; `SYSTEM_MAIL_DOMAIN` reserved; TLS **[relay]**. **C** multiple mailing lists per newsletter; subscriber metadata (text-only definitions, immutable after creation, lowercase keys); CSV import w/ column mapping (email required; lists/subscribed-at/IP/metadata; async) + export; statuses incl. pending + `send_pending_confirmation_email`; **double opt-in default** (disable-ability NV); unsubscribe w/ per-send recording **open bug #478**; **embeddable signup web component** (`<hyvor-post-form>`, list chooser, colors, custom CSS via shadow DOM); captcha NV; preference center partial (form-side chooser only); bulk ops (delete/status/metadata). **D** multiple newsletters per account (per-newsletter subdomain); issues w/ 4 statuses; send w/ preview (HTML + sendable count) + progress + per-send records; **issue report counts (total/pending/sent/failed/unsubscribed/bounced/complained — no open/click fields, consistent with no-tracking)**; **auto-generated archive website** (SvelteKit, subdomains, free); per-newsletter templates; sending profiles; duplication NV; test sends NV (admin `sudo` only). **E** rich-text editor w/ Markdown (ProseMirror); full HTML control via **Twig** templates (`{{ content | raw }}`, all else escaped); **conditional content per list** (`has_list('Physics')`); **CSS inlining** (cssinliner-extra); palette/branding styling vars; no drag-and-drop/MJML; plain-text part NV; no blocks/versioning. **F** **no open/click tracking, ever (by policy — not a setting)**; no subscriber profiling ("a subscriber is an email address and the lists they joined" + optional name/metadata); delivery/bounce/complaint/unsubscribe counts per issue; per-send records via API; report export NV. **G** no workflow automations/welcome sequences; DOI confirmation email (customization = open issue #477); automated bounce/complaint/unsubscribe recording via **signed Relay webhooks** (`RELAY_WEBHOOK_SECRET`); **resubscribe blocking per reason** (`list_skip_resubscribe_on`); scheduled sending. **H** Console REST API (the console's own API; OpenAPI via nelmio): newsletters, issues (+send/preview/progress/report), lists, subscribers (upsert w/ `lists_strategy`/`metadata_strategy`, resend opt-in, bulk), metadata, sending profiles, template (+render), users, API keys, media, exports; **scoped API keys** (12 scopes; regenerable, shown once); **API rate limiting (Oct 2026)**; **no outgoing user webhooks**; no SDKs/CLI/MCP/Zapier; ESP integration = Relay only. **I** multi-tenancy via Hyvor accounts/orgs (collaborators must belong to owning org); team invitations (role model NV); key scoping + regeneration; soft delete w/ 30-day recovery; **admin impersonation ("sudo")** for preview/sends (internal); 2FA/SSO [platform, NV]; audit logs NV; GDPR partial (EU storage, DPA offered — cloud context). **J** on-prem deployment mode in code (partial); production Docker image; `WORKERS` scaling; storage adapters; messenger-monitor queue observability (UI exposure NV); Sentry optional; health checks **[relay]** / NV for Post; `LOG_LEVEL`/monolog; Relay-layer Prometheus/Grafana covers deliverability monitoring Post lacks.

Distinctive capabilities:

| Capability | Registry ID | Evidence |
|---|---|---|
| No open/click tracking by policy (product core non-feature) | ANA-001/ANA-002 (absence) | privacy-first page, open-source page |
| No subscriber profiling (email + lists only) | CON-001 (design stance) | privacy-first page |
| Double opt-in default | CON-004 | form docs, privacy-first page |
| Embeddable signup web component w/ custom CSS | CON-017 | SignupForm.md |
| Conditional content per list in Twig templates | TPL-005 (nearest) | EmailDesign.md |
| CSS inlining (Twig cssinliner-extra) | TPL-011 | composer.json |
| Auto-generated public archive website | CAM-012 | README, archive/ app |
| Subscriber metadata definitions (typed-ish, immutable) | CON-005 | SubscriberMetadataDefinition |
| Console API w/ 12 fine-grained key scopes + rate limiting | API-004/API-007 | ConsoleApi.md, commit #348 |
| Resubscribe blocking per reason | CON-003 | `list_skip_resubscribe_on` |
| Soft delete w/ 30-day recovery | INF-017 (nearest) | Console API semantics |
| Signed Relay webhooks for bounce/complaint recording | INF-019 | `RELAY_WEBHOOK_SECRET`, SNS validator |
| Admin impersonation (sudo preview/sends) | SEC-015 | commit #450, Api/Sudo |
| CSV import w/ column mapping + export | CON-009/CON-010 | Import.md, Tools → Export |

Verified non-features: no open/click tracking ever (no opt-in); no marketing-automation workflows/welcome sequences/A-B; no transactional email inside Post; no direct SMTP (Relay API only); no file attachments (images only 10 MB); no Excel import; no drag-and-drop builder/MJML; no user-facing outgoing webhooks; **no supported self-hosting yet** (vendor's own README + FAQ); no reusable content blocks/template versioning/per-subscriber localization.

Limitations & security notes: **self-hosting story contradicts itself across three official pages** (README "not yet ready"; open-source FAQ "not in a supported way yet"; privacy-first FAQ "yes" + published Hosting guide) — re-verify before relying on any path; pre-1.0 with tiny community surface (34 stars); hard dependency on Hyvor Relay couples availability/deliverability to a second deployment (Relay Cloud or self-hosted Relay, itself an ops burden: DNS server, IPs, scaling); `composer.json` "proprietary" license-field conflict worth flagging upstream; signup-form embed hardcodes the cloud script URL (self-host form serving NV); per-send unsubscribe recording has open bug #478 (analytics data-consistency edge); metadata limited to text fields; security responsiveness demonstrably real (CVE fixes within days, PGP-verified commits).

NEEDS_VERIFICATION: (1) whether DOI can be disabled per newsletter/list; (2) form spam protection; (3) collaborator role model; (4) email dark mode + plain-text part; (5) issue duplication + non-admin test sends; (6) report export; (7) DMARC/SPF monitoring at Relay layer; (8) whether the Hosting guide is GA (official pages contradict) + OIDC timeline; (9) self-hosted Post + fully self-hosted Relay end-to-end; (10) form-embed URL handling on self-host; (11) release-note content (0.0.x bodies empty).

---

# §3 Normalized feature inventory (registry v1.0.0, 199 entries)

Reproduced from `registry.md` (frozen 2026-11-05) **in full and in registry order** — same IDs, same per-project status flags, same ReForwarder statuses; IDs must never be renumbered. The Notes column is lightly compressed; evidence file paths are ReForwarder repo paths from the audits (baseline `a6f1681`). "Projects (codes)" = the 12 OSS projects; ReForwarder = "RF".

## A — Email infrastructure (INF, 36)

| ID | Feature | Description | Projects | RF status | Notes |
|---|---|---|---|---|---|
| INF-001 | External SMTP sending | Send via operator-configured external SMTP relays/servers (SSL/STARTTLS/plain). | LM✓ SP✓ KE✓ NU✓ BM✓ | IMPLEMENTED | Relay-only client `smtp_sender.go`; RF cannot be its own MTA (audit-A top gap). |
| INF-002 | Built-in SMTP server (submission) | Product runs its own SMTP listener accepting submissions (SASL auth). | BM✓ PO✓ HR✓ US◐ NU✓ | IMPLEMENTED | RF relay `smtprelay/backend.go:68-139` (PLAIN auth, per-cred IP allowlist), default-off. |
| INF-003 | Full mail server (mailboxes, IMAP/POP, webmail) | Host end-user mailboxes with IMAP/POP access and webmail UI. | BM✓ | NOT_APPLICABLE | BillionMail-only (Postfix+Dovecot+Roundcube); out of RF scope. |
| INF-004 | Transactional send API | HTTP endpoint to send a single templated or raw transactional email. | LM✓ BM✓ PO✓ US✓ NU✓ KE✓ MC✓ HR✓ WR✓ | IMPLEMENTED | `POST /emails/send` + send-template; full pipeline trace audit-A row 3. |
| INF-005 | Batch send API (per-recipient variables) | One API call fans out a batch with per-recipient variables and itemized results. | LM✓ US✓ WR✓ | IMPLEMENTED | `POST /emails/batch` (`auth_routes.go:239`) incl. dry-run validation. |
| INF-006 | Campaign/newsletter bulk sending | Fan a campaign out to a full audience asynchronously. | LM✓ BM✓ US✓ NU✓ SP✓ KE✓ MC✓ WR✓ HP✓ HR◐ | IMPLEMENTED | Batch=100 self-re-enqueue (`campaign_processor.go:91`); bulk queue weight 3. |
| INF-007 | Outbound attachments | Attach files to API/campaign mail with size limits; MIME multipart build. | US✓ NU✓ HR✓ LM◐ PO◐ | IMPLEMENTED | Blob-backed, 10-25MB limits (`service.go:671`); tests `attachment_pipeline_test.go`. |
| INF-008 | Custom headers control | Caller-supplied headers with reserved-prefix spoofing protection. | LM✓ US✓ HR✓ MC✓ WR◐ | IMPLEMENTED | `x-posta-`/`dkim-`/`arc-` blocked (`service.go:1478`); setting-gated. |
| INF-009 | Background queue pipeline | Durable async sending queues decoupled from the API process. | LM✓ BM✓ PO✓ US✓ NU✓ SP✓ KE✓ MC✓ WR✓ HP✓ HR✓ | IMPLEMENTED | asynq+Redis; deterministic TaskIDs; producer `producer.go`. |
| INF-010 | Queue administration & dead-letter management | Inspect queues per state (incl. dead-letter), cancel/run/archive from admin. | BM✓ PO◐ WR◐ | IMPLEMENTED | committed a6f1681: `/admin/queues*` + Jobs.vue + `queue_reconcile` cron. |
| INF-011 | Priority/weighted queues (tx vs bulk isolation) | Transactional traffic protected from bulk backlogs via weighting or separation. | LM✗ PO✗ HR✓ | PARTIAL | Weighted (tx=6/bulk=3/low=1) is probabilistic, not strict (`cmd/posta/worker.go:140`); HR isolates queues. |
| INF-012 | Send rate limiting & quotas | Hourly/daily or throughput limits per sender/workspace, plan-overridable. | LM✓ PO✓ NU✓ KE✓ MC✓ HR✓ WR✓ HP✓ US◐ BM✗ | IMPLEMENTED | Redis fixed-window hourly/daily (`ratelimit/quota.go:44`); no per-API-key limits. |
| INF-013 | Retry policies | Layered retries for transient failures with exhaustion handling. | LM✓ PO✓ NU✓ MC✓ HR✓ | IMPLEMENTED | 3 layers (asynq, DB retry worker, manual); 5xx RCPT skips retry + suppresses. |
| INF-014 | Scheduled (future) sends | One-shot future-dated sends for transactional messages and campaigns. | LM✓ BM✓ US✓ NU✓ SP✓ KE✓ MC✓ WR✓ HP✓ PO◐ | IMPLEMENTED | tx `send_at` + campaign scheduler committed 347ebb4; tx scheduled rows can't be cancelled. |
| INF-015 | Gradual/throttled campaign sending | Pace a campaign (send-rate, batch windows, warm-up ramps) instead of blasting. | US✓ NU✓ MC✓ KE✓ HP✓ LM◐ BM✗ SP✗ | IMPLEMENTED | Per-campaign `SendRate` → inter-batch delay (`campaign_processor.go:419`); batch-granular. |
| INF-016 | Circuit breaker / auto-pause on failure | Auto-pause sending after consecutive-failure or bounce/complaint thresholds. | NU✓ MC✓ | MISSING | NU pauses after 5 consecutive failures; MC at 2.5%/0.08%. RF has alerts only. |
| INF-017 | Message & attachment retention/purge | Time-based purge of sent mail, bodies, attachments and raw logs. | BM✓ PO✓ US✓ KE✓ SE✓ HR✓ WR✓ HP✓ NU◐ MC◐ | IMPLEMENTED | `retention.go` purges 10+ surfaces incl. blob scrubbing; no per-workspace legal hold. |
| INF-018 | Suppression lists (send-time enforcement) | Global/list suppression blocking delivery (hard bounce, complaint, manual). | LM✓ BM✓ PO✓ US✓ NU✓ MC✓ HR✓ WR✓ HP✓ | IMPLEMENTED | Kinds hard/bounce/complaint/list_unsubscribe/manual; enforced in worker (`handler.go:283`). |
| INF-019 | Bounce ingestion & auto-suppression | Ingest bounces from sync SMTP 5xx, webhooks, and API; auto-suppress on hard bounce. | LM✓ BM✓ PO✓ US✓ NU✓ KE◐ MC✓ SE✓ HR✓ WR✓ HP✓ | IMPLEMENTED | 3 sources (audit-A); `/webhooks/bounce` marks subscriber only — suppression via authenticated API or sync 5xx. |
| INF-020 | Bounce mailbox / VERP ingestion | Dedicated return-path mailbox or VERP parsing as bounce source. | LM✓ | MISSING | LM POP3 mailbox (destructive read); RF relay-only architecture has no VERP/mailbox path. |
| INF-021 | Complaint/FBL ingestion | Consume spam-complaint feedback loops (ARF/FBL or provider complaints) into suppression. | LM◐ US✓ NU✓ SP◐ KE◐ MC✓ SE✓ HR✓ WR✓ HP✓ | PARTIAL | Authenticated API only; no FBL source, no ARF parse, webhook rejects complaint type. |
| INF-022 | Multiple ESP integrations (native provider APIs) | Ship first-class SES/Postmark/Mailgun/SendGrid connectors. | NU✓ SP✓ KE✓ BM✓ US✓ MC✓ WR✓ | PARTIAL | Generic SMTP only; no native ESP API clients (go.mod: s3 only). |
| INF-023 | ESP/provider failover | On send failure, retry via an alternate configured provider/server. | LM◐ SP◐ | MISSING | Retry re-queues against the same server (`handler.go:334`); no rotation/circuit breaker. |
| INF-024 | Per-message/per-campaign server selection | Choose which sending server a message or campaign uses. | LM✓ | PARTIAL | Only first-enabled workspace server picked (`smtp_repo.go:119-125`); multiple servers storable, not selectable. |
| INF-025 | IP pools / multiple sending IPs | Manage dedicated IP pools, per-domain IP binding, warm-up. | PO✓ BM◐ HR▲ | MISSING | No IP concept in code; structurally absent under relay-only design (audit-A). |
| INF-026 | Development/sandbox hold mode | Mode that holds/stubs all mail instead of delivering. | PO✓ MC◐ | IMPLEMENTED | `POSTA_DEV_MODE` stores as `sent` without delivery (`service.go:855`). |
| INF-027 | Dry-run send validation | Validate a send payload (recipients, limits, render) without sending. | — | IMPLEMENTED | `dry_run=true` returns `DryRunResponse` (`email_handler.go:44`). |
| INF-028 | Recipient address verification | Syntax/disposable/role/MX checks pre-send, cached. | MC✓ BM✓ WR◐ | IMPLEMENTED | `POST /emails/verify` (`verifier.go:116`); advisory only, not enforced at send. |
| INF-029 | Inbound email receiving & parsing | Accept mail (MX/SMTP or webhook), parse MIME, store attachments, expose stream. | PO✓ BM◐ US▲ HR▲ | IMPLEMENTED | Inbound SMTP + webhook ingest + MIME parser + SSE (`inbound_routes.go`). |
| INF-030 | Inbound routing rules | Route inbound mail by address/domain/wildcard to HTTP/SMTP/forward targets. | PO✓ BM◐ | PARTIAL | Recipient-domain-verified gating + webhook fan-out only; no per-domain route table. |
| INF-031 | Idempotent sends | Client idempotency keys prevent duplicate sends on retry. | US✓ HR✓ NU◐ | PARTIAL | Internal deterministic TaskID dedup committed a6f1681 (`producer.go:47-101`); no `Idempotency-Key` API. |
| INF-032 | Mail/message tagging | Free-form tags on messages for grouping/analytics. | PO✓ BM◐ | PARTIAL | `ExternalID` caller correlation only (`service.go:343`); no tag model. |
| INF-033 | SMTP connection diagnostics | Test connection/auth to a configured server; persist validation state. | BM◐ | PARTIAL | `POST /smtp-servers/{id}/test` + `validated_at` (`smtp_handler.go:300`); no EHLO/TLS transcript. |
| INF-034 | Greylisting & transient-failure handling | Detect greylist deferrals and classify timeouts as transient (never suppress). | HR✓ | PARTIAL | Timeout→transient classification committed a6f1681 (`smtp_sender.go:64-67`); no explicit greylist detection. |
| INF-035 | Maintenance-mode send gate | Instance-wide gate blocking sends during maintenance windows. | — | IMPLEMENTED | Settings-driven gate in `Send` (`service.go:654-656`); RF-specific. |
| INF-036 | Multi-channel messengers (SMS/push adapters) | Generic HTTP messenger adapters to send via SMS/push providers. | LM✓ | MISSING | LM messenger interface (SMS/WhatsApp/FCM community impls); RF email-only. |

## B — Domains & DNS (DOM, 13)

| ID | Feature | Description | Projects | RF status | Notes |
|---|---|---|---|---|---|
| DOM-001 | Custom sending domains (multi-domain CRUD) | Register and manage multiple sending domains per workspace. | BM✓ PO✓ US✓ NU✓ MC✓ HR✓ WR✓ HP✓ LM✗ | IMPLEMENTED | Domain CRUD + per-workspace scoping + admin conflict detection (`workspace_resource_routes.go:750`). |
| DOM-002 | Domain ownership verification | TXT-token verification gating sending. | PO✓ US✓ HR✓ MC✓ WR✓ BM✓ | IMPLEMENTED | TXT `posta-verification=<token>` (`verifier.go:88`); no DKIM/MX alternative, no re-verification. |
| DOM-003 | Managed DKIM signing | Product generates keys and DKIM-signs outbound mail. | PO◐ BM✓ HR✓ US✓ WR✓ | IMPLEMENTED | committed a6f1681: go-msgauth relaxed/relaxed SHA-256, RSA/Ed25519, per-envelope-domain (`dkim_signer.go:80`). |
| DOM-004 | DKIM key rotation & DNS publication verification | Rotate selector keys with DNS publication re-check. | HR◐ | IMPLEMENTED | committed a6f1681: two-phase `retiring→active` rotation + `verify-dns` endpoint (`workspace_resource_routes.go:813-872`). |
| DOM-005 | SPF checking & guidance | Check/generate SPF records for sending domains. | PO✓ US✓ BM✓ MC✓ WR✓ | PARTIAL | Presence check only (`verifier.go:97-101`); no include validation or 10-lookup check. |
| DOM-006 | DMARC checking & guidance | Check/generate DMARC records. | US✓ MC✓ WR✓ BM◐ | PARTIAL | Presence check only (`verifier.go:126`); no policy parsing, no rua ingestion (absent industry-wide). |
| DOM-007 | Automated DNS provisioning | Write DNS records via provider APIs or a built-in DNS server. | HR✓ MC◐ | MISSING | HR delegates NS to built-in DNS server; RF offers copy-paste `RequiredRecords` only. |
| DOM-008 | DNS re-verification & drift monitoring | Periodic re-checks of published records with drift alerts. | PO✓ MC✓ HR✓ | MISSING | Verification on-demand only; no cron re-check (audit-B top gap; DKIM verify-dns is manual). |
| DOM-009 | Custom tracking domain | Branded CNAME domain for click/open tracking links. | PO✓ NU✓ | MISSING | Tracking runs on instance domain only; no per-workspace branded tracking host. |
| DOM-010 | TLS certificate management (ACME/SNI) | Per-domain cert provisioning/renewal (Let's Encrypt), SNI. | BM✓ PO◐ | MISSING | Server-level inbound TLS files only (`config.go:163-165`); no ACME/SNI. |
| DOM-011 | Domain reputation/blacklist monitoring | Blacklist scans, Postmaster integration, account-health sweeps. | BM✓ MC✓ WR✓ | PARTIAL | Proxies only: bounce-rate alert cron + provider-classified analytics (`bounce_alert.go`); no blacklist/Postmaster. |
| DOM-012 | Domain-sending policy gates | Enforce sending only from verified domains; per-server allowed domains/senders. | MC✓ HR✓ PO◐ | PARTIAL | Strict-mode opt-in + shared-server `allowed_domains`; campaigns only warn on unverified. |
| DOM-013 | rDNS/PTR guidance | Guidance/checks for reverse DNS on sending IPs. | HR◐ | MISSING | Zero code/UI; relevant only if direct-to-MX ever added (audit-B). |

## C — Contacts & audience (CON, 20)

| ID | Feature | Description | Projects | RF status | Notes |
|---|---|---|---|---|---|
| CON-001 | Subscriber/contact records | Audience records with identity fields, per-workspace unique emails. | LM✓ BM✓ US✓ NU✓ SP✓ KE✓ MC✓ WR✓ HP✓ PO✗ | IMPLEMENTED | Subscribers CRUD + read-only send-ledger Contacts (dual surface); dead ContactLists orphan noted for cleanup. |
| CON-002 | Mailing lists | Multiple named lists per workspace; membership management. | LM✓ BM✓ US✓ NU✓ MC✓ WR✓ HP✓ SP✗ | IMPLEMENTED | Static/dynamic `SubscriberList` + members routes; SP deliberately list-less (tags only). |
| CON-003 | Subscription status management | subscribed/unsubscribed/bounced/complained transitions with timestamps. | LM✓ US✓ NU✓ MC✓ WR✓ HP✓ SP◐ | IMPLEMENTED | 4 statuses + list-scoped opt-out table; API status set doesn't auto-create suppression rows. |
| CON-004 | Double opt-in | Confirmation-email flow with pending state before subscription activates. | LM✓ BM✓ US✓ NU✓ KE✓ MC✓ WR✓ HP✓ SP✗ | MISSING | Entirely absent (grep clean); EU-marketing compliance blocker (audit-C top gap). |
| CON-005 | Custom attributes/fields | Free-form or typed custom fields on contacts usable in content/segments. | LM✓ BM✓ US✓ KE✓ MC✓ WR✓ HP◐ NU◐ SP✗ | IMPLEMENTED | JSONB `CustomFields` + segment custom-key rules; no typed schema registry. |
| CON-006 | Subscriber tags | Label taxonomy on subscribers for targeting. | BM✓ SP✓ LM✗ US✗ WR✗ | MISSING | No Tag model anywhere; SP uses tags as primary segmentation (competitor-parity gap). |
| CON-007 | Dynamic segmentation (condition builder) | Saved condition trees (AND/OR groups) evaluated against contacts. | LM✓ NU✓ KE✓ WR(E) SP◐ BM◐ | IMPLEMENTED | Guarded SQL builder, 14 operators, injection tests (`segment_query.go:408`); rules can't reference list membership. |
| CON-008 | Engagement-based segmentation | Segment on opens/clicks/campaign history/inactivity. | NU✓ KE✓ | IMPLEMENTED | Engagement subqueries over campaign_messages with legacy-parity tests (`segment_query.go:109-131`). |
| CON-009 | CSV import (column mapping) | Import contacts from CSV with column mapping and dedupe. | LM✓ BM✓ US✓ NU✓ SP✓ KE✓ MC✓ WR✓ HP✓ | IMPLEMENTED | CSV+JSON endpoints, `custom_fields.*` mapping, IANA-tz validation (`subscriber_handler.go:246`); no streaming. |
| CON-010 | CSV export | Export contacts/subscribers/segments to CSV. | BM✓ SP✓ KE✓ MC✓ WR✓ HP✓ LM◐ | MISSING | Only audit-log CSV + GDPR JSON bundle exist (`logs_handler.go:853`); asymmetric portability. |
| CON-011 | Bulk contact operations | Bulk delete/status-change/list add-remove beyond import. | LM✓ US✓ NU✓ HP✓ | PARTIAL | Import-only bulk; single-item delete; GDPR purge is whole-workspace. |
| CON-012 | Contact upsert/sync API | Create-or-update semantics keyed on email for external sync. | US✓ NU✓ HP✓ | PARTIAL | Import dedupe + CRUD exist; no explicit upsert endpoint. |
| CON-013 | Suppression management (UI/API) | Add/remove/browse suppression entries with scoping. | US✓ MC✓ HR✓ WR✓ | IMPLEMENTED | Suppression CRUD + Bounces-view tab; no CSV import, no kind filter (`suppression_handler.go:40`). |
| CON-014 | Unsubscribe management (RFC 8058, hosted pages) | One-click unsubscribe headers, hosted confirm pages, resubscribe. | LM✓ BM✓ US✓ NU✓ SP✓ KE✓ MC✓ WR✓ HP✓ | IMPLEMENTED | Campaign + tx one-click, HMAC-separated tokens, hosted pages (`tracking_handler.go:211-424`). |
| CON-015 | Unsubscribe reasons | Capture/report why contacts unsubscribed. | US✓ | PARTIAL | Reason column exists (`subscriber_list.go:94`); no analytics UI. |
| CON-016 | Preference center | Self-service per-recipient list/group/language preferences page. | NU✓ WR✓ LM◐ KE◐ HP◐ | MISSING | Forward-reference comment only (`suppression.go:26`); hosted unsubscribe page is the foundation. |
| CON-017 | Signup forms (hosted/embedded) | Public forms/embeds that capture subscribers into lists. | LM✓ NU✓ KE✓ MC✓ HP✓ BM✗ US✗ SP✗ | MISSING | RF "forms" = website-form→inbox, not audience capture (audit-C classification). |
| CON-018 | Form bot protection (captcha/honeypot) | CAPTCHA, honeypot, or bot detection on public forms. | KE✓ MC✓ | NOT_APPLICABLE | No signup forms exist to protect; RF nonce/time-trap applies to inbound website-form inbox. |
| CON-019 | Contact activity history | Per-contact timeline of sends/opens/clicks/bounces. | BM✓ KE✓ NU✓ US◐ | PARTIAL | Events live in Logs/messages; no per-subscriber timeline in detail view. |
| CON-020 | Import cleaning & validation | Clean invalid/role/disposable/duplicate addresses at import. | MC✓ BM✓ WR◐ | PARTIAL | Dedupe + skip counts on import; no cleaning heuristics (MC exemplar: spam-trap/misspell detection). |

## D — Campaigns (CAM, 18)

| ID | Feature | Description | Projects | RF status | Notes |
|---|---|---|---|---|---|
| CAM-001 | Campaign creation & drafts | Compose campaigns with draft state, editable until send. | LM✓ BM✓ US✓ NU✓ SP✓ KE✓ MC✓ WR✓ HP✓ | IMPLEMENTED | Draft-only-send CAS state machine (`campaign.go:32-39`); content from templates by design. |
| CAM-002 | Campaign scheduling | Future-dated sends fired by a scheduler. | LM✓ BM✓ US✓ NU✓ SP✓ KE✓ MC✓ WR✓ HP✓ | IMPLEMENTED | committed 347ebb4: every-minute sweep, atomic claim+rollback, 616-line test (`campaign_scheduler.go:60`). |
| CAM-003 | Recurring campaigns | Repeat sends on a schedule. | — | MISSING | Absent in all 12 projects; could layer on scheduler cron. |
| CAM-004 | Campaign duplication | Clone a campaign (content, audience, settings) into a new draft. | LM✓ BM✓ WR✓ | PARTIAL | Backend endpoint only — zero UI/API-client call sites (`campaign_handler.go:466`). |
| CAM-005 | Reusable campaign templates | First-class reusable templates selectable per campaign. | LM✓ BM✓ SP✓ KE✓ HP✓ US◐ | IMPLEMENTED | Templates pin template+version with per-language resolution (`campaign_processor.go:447`). |
| CAM-006 | Template content slots / campaign overrides | Campaign-level override of template blocks/slots. | KE✓ | MISSING | No block-level override; template object is atomic. |
| CAM-007 | Campaign preview & test send | Render the assembled campaign (subject+data) to preview or an inbox. | LM✓ SP✓ MC✓ NU✓ KE◐ | PARTIAL | Template send-test ignores campaign data; no campaign-scoped preview endpoint/UI — top usability gap. |
| CAM-008 | A/B testing | Split audience across subject/content variants with winner selection. | NU✓ KE▲ | PARTIAL | Subject-split API only: no winner logic, variant `TemplateID` dead, no UI (`campaign_processor.go:194`). |
| CAM-009 | Audience targeting (list/segment) | Target one list or one segment (XOR) with eligibility filtering. | LM✓ NU✓ KE✓ SP◐ WR✓ HP✓ | IMPLEMENTED | XOR enforced + segment branch tests; segments can't honor per-list unsubscribes (documented). |
| CAM-010 | Pause/resume/cancel lifecycle | Control running campaigns; cancel drains pending messages. | LM✓ US✓ NU✓ WR✓ | IMPLEMENTED | Atomic CAS transitions, resume re-enqueue with rollback, cancel skips pending. |
| CAM-011 | Campaign reports | Per-campaign delivery/open/click/bounce/unsub rates, links, series. | LM✓ BM✓ US✓ NU✓ MC✓ WR✓ HP✓ SP◐ | IMPLEMENTED | Single-aggregation analytics + per-variant breakdown + hourly series; snapshot survives delete. |
| CAM-012 | Public newsletter archive | Hosted public archive of sent campaign pages. | LM✓ KE✓ MC✓ HP✓ | MISSING | Web-view tokens are per-recipient + expiring, not an archive. |
| CAM-013 | Multiple newsletters/publications | Group campaigns under separate newsletter/publication entities. | LM✓ KE✓ MC✓ HP✓ | MISSING | Flat `campaigns` entity; no publication grouping. |
| CAM-014 | Web view of message | Hosted browser version of each message via variable. | SP✓ | IMPLEMENTED | `{{ posta_web_view_url }}` + HMAC 90-day token + CSP (`tracking_handler.go:345`). |
| CAM-015 | UTM auto-tagging | Auto-append utm_source/medium/campaign to external links. | NU✓ | MISSING | No UTM rewriting in tracking pipeline. |
| CAM-016 | Timezone-aware sending | Send each recipient at their local time. | NU✓ | PARTIAL | Stub: `SendAtLocalTime` field exists, logs intent, sends one batch (`campaign_processor.go:230`); absent industry-wide. |
| CAM-017 | Stuck-campaign watchdog recovery | Cron re-enqueues stalled campaigns automatically. | — | IMPLEMENTED | `campaign_restart` */5 cron for `sending`>10min (`campaign_restart.go:57`); RF-specific. |
| CAM-018 | Failed-recipient retry | Retry failed recipients after fixing the cause. | NU✓ | PARTIAL | Per-message manual retry API only (`workspace_resource_routes.go:116`); no campaign-level retryFailed. |

## E — Templates & editors (TPL, 22)

| ID | Feature | Description | Projects | RF status | Notes |
|---|---|---|---|---|---|
| TPL-001 | Raw HTML editor | Code editor for full HTML templates. | LM✓ BM✓ SP✓ NU✓ KE✓ MC✓ HP✓ | IMPLEMENTED | CodeMirror 6 editor + auto-escaping html/template (`TemplateEditor.vue`). |
| TPL-002 | WYSIWYG / drag-and-drop builder | Visual block/canvas email builder. | LM✓ BM✓ US✓ NU✓ KE✓ MC◐ SP✗ WR✗ HP✗ | PARTIAL | Stock GrapesJS newsletter preset only — no product block library or saved sections (`EmailBuilder.vue:41`). |
| TPL-003 | Markdown authoring | Write/send Markdown-rendered campaigns. | LM✓ KE✓ MC✓ | MISSING | No Markdown in pipeline (grep clean). |
| TPL-004 | Plain-text part & auto-generation | Send text/plain alternative; auto-generate from HTML. | LM✓ KE✓ US✓ | IMPLEMENTED | `html2text.go` applied in tx + campaign paths; no Markdown→HTML step. |
| TPL-005 | Template variables / merge fields | Merge fields with fallbacks in subject and body. | LM✓ BM✓ US✓ NU✓ KE✓ WR✓ HP✓ SP◐ | IMPLEMENTED | Data map + reserved `{{ posta_* }}` namespace with sentinel substitution (`system_vars.go`). |
| TPL-006 | Advanced templating engines (Liquid/Handlebars-style) | Logic-capable template language (filters, loops, conditionals). | NU✓ KE✓ LM◐ | PARTIAL | Go templates + Mustache-style normalization; no Liquid filters/pipeline (`template_renderer.go:85-149`). |
| TPL-007 | MJML support | Author/compile MJML responsive templates. | NU✓ KE✓ | MISSING | Zero mjml references (audit-E). |
| TPL-008 | React Email | Component-based (TSX) email authoring. | US✓ WR✓ | MISSING | Vue product; feature absent. |
| TPL-009 | Template versioning & activation | Version history with single-active-version activation. | NU✓ | IMPLEMENTED | Versions + `activate` pointer swap + uniqueIndex; no diff view; delete guard unverified. |
| TPL-010 | Template localization (multi-language) | Per-language template variants with fallback chain. | NU(E) | IMPLEMENTED | Localizations + exact→base→default→en fallback + per-subscriber language at send. |
| TPL-011 | CSS inlining | Server-side inlining of stylesheets at render. | SP✓ HP✓ | IMPLEMENTED | go-premailer on render when version has stylesheet; corrects seed uncertainty. |
| TPL-012 | Reusable blocks/sections library | Saved shared content blocks across templates. | — | MISSING | No block model/route; GrapesJS preset blocks are in-memory only. |
| TPL-013 | Brand kit / shared stylesheets | Central brand styling (colors/fonts/logos) applied to templates. | WR✓ | IMPLEMENTED | Stylesheet entity CRUD + attach-to-version + render application; UI included. |
| TPL-014 | Template preview with sample data | Render templates with arbitrary/sample data without sending. | LM✓ NU✓ KE◐ | IMPLEMENTED | Ad-hoc + localized preview endpoints + 300ms-debounced live editor preview. |
| TPL-015 | Editor device previews | Desktop/tablet/mobile preview inside the builder. | MC◐ | IMPLEMENTED | GrapesJS device previews (`EmailBuilder.vue:60-66`). |
| TPL-016 | Preview text (preheader) | Dedicated preheader/preview-text field. | KE✓ US✓ | NEEDS_VERIFICATION | No evidence in audits; subject templating exists, preheader unverified. |
| TPL-017 | Send-test to inbox | Email the rendered template to a real address. | LM✓ SP✓ NU✓ | IMPLEMENTED | `POST /templates/{id}/send-test` with language + sample data (`template_handler.go:273`). |
| TPL-018 | Template import/export (JSON bundle) | Round-trip full templates (versions+localizations+builder JSON) as files. | NU◐ | IMPLEMENTED | Export/import incl. `BuilderJSON` so GrapesJS projects survive. |
| TPL-019 | HTML import (style extraction) | Upload HTML files; extract CSS into stylesheets, title→subject. | — | IMPLEMENTED | `POST /templates/import-html` ≤2MB with style/link resolution; RF-specific. |
| TPL-020 | AI-assisted creation | AI generates/edits template content or builds from a website. | BM✓ NU✓ WR(E) MC◐ | MISSING | Zero LLM references; RF-specific stance (self-host, no cloud). |
| TPL-021 | Spintax variation | Spintax content spinning for per-recipient variation. | BM✓ | MISSING | Cold-email niche (BM); absent in RF. |
| TPL-022 | Media/image uploads & storage | Upload/host images for template use (S3/local). | LM✓ KE✓ MC✓ HP◐ | NEEDS_VERIFICATION | Blob store exists for attachments; template media-library surface not audited. |

## F — Analytics & tracking (ANA, 16)

| ID | Feature | Description | Projects | RF status | Notes |
|---|---|---|---|---|---|
| ANA-001 | Open tracking | Tracking pixel with dedup; per-message keying. | LM✓ BM✓ PO✓ US✓ NU✓ SP◐ KE✓ MC✓ SE✓ WR✓ HP✗ HR✗ | IMPLEMENTED | HMAC-signed pixel injection, campaign+UUID variants, day-bucket dedup (`tracking.go:161`). |
| ANA-002 | Click tracking | Link rewriting + signed redirects with per-link counts. | LM✓ BM✓ PO✓ US✓ NU✓ SP◐ KE✓ MC✓ SE✓ WR✓ HP✗ HR✗ | IMPLEMENTED | href rewrite skipping own/mailto/tel + TrackedLink table (`tracking.go:65`); no attribution window. |
| ANA-003 | Delivery status tracking | True delivery confirmation events (delivered/deferred) beyond SMTP accept. | PO✓ US✓ NU✓ MC✓ SE✓ WR✓ HR◐ | PARTIAL | `delivered` event defined but never produced — SMTP-accept only (`email_event.go:27`); needs ESP webhook ingestion. |
| ANA-004 | Bounce categorization (hard/soft) | Classify bounces hard/soft/complaint with trend analytics. | LM◐ PO◐ US✓ MC✓ SE✓ | PARTIAL | Hard auto-classified from 5xx; soft never auto-detected — exhaustion records only `failed`. |
| ANA-005 | Complaint tracking | Record complaints, suppress, emit complained events. | LM◐ US✓ NU✓ SP◐ MC✓ HR✓ WR✓ | PARTIAL | API-only ingest; no `complained` email_events row ever written; no FBL source. |
| ANA-006 | Campaign analytics (rates, links, series) | Per-campaign engagement analytics incl. per-link and hourly series. | LM✓ BM✓ US✓ NU✓ SP◐ KE◐ MC✓ WR✓ HP✓ | IMPLEMENTED | Rates + clicked-links + hourly series + A/B variants (`tracking_handler.go:425`). |
| ANA-007 | Historical trend analytics (date ranges) | Arbitrary-range daily series: status mix, delivery/bounce trends, latency percentiles. | LM✓ BM✓ US✓ NU✓ SE✓ WR✓ | IMPLEMENTED | Gap-filled UTC daily series, p50-p99 latency, provider breakdown (`analytics_repo.go:49-330`). |
| ANA-008 | Per-message event timeline & searchable logs | Per-message status timeline, content, headers, events. | PO✓ BM✓ NU✓ SE✓ HR✓ MC✓ WR✓ | IMPLEMENTED | `/logs/messages/{uuid}/events` + content endpoint; effective timeline is 6 written event types. |
| ANA-009 | Provider/inbox breakdown analytics | Delivery stats split by receiving provider/inbox. | MC✓ | IMPLEMENTED | `ClassifyProvider` stamped on rows + provider-breakdown analytics (`provider.go:148`). |
| ANA-010 | Bounce-rate alerting | Threshold-based bounce alerts per workspace. | LM◐ MC✓ | IMPLEMENTED | `bounce_alert` cron on workspace thresholds; alert-only (no auto-pause). |
| ANA-011 | Exportable reports | CSV/JSON export of analytics or message history. | SE✓ LM◐ NU◐ | PARTIAL | Audit-log CSV only (`logs_handler.go:821`); no analytics/message export. |
| ANA-012 | Tracking privacy controls (opt-out scoping) | Global/per-campaign/per-workspace tracking kill-switches. | LM✓ KE✓ PO◐ US◐ SP◐ | PARTIAL | Global toggle disables rewriting+pixel for tx, but campaign open-pixel injection continues; no per-campaign scope. |
| ANA-013 | SMTP response/conversation inspection | Persist SMTP dialogue/transcripts for diagnosis. | PO◐ HR✓ | PARTIAL | Last-error text + `smtp_code`/`smtp_stage` metadata; no conversation transcript. |
| ANA-014 | Dashboard KPIs | Cached overview: totals, failure/bounce rates, 14-day volume, feature flags. | BM✓ MC✓ | IMPLEMENTED | Redis-cached `/dashboard/stats` + hand-rolled SVG charts (`dashboard_handler.go:133`). |
| ANA-015 | Device/geo breakdowns | Opens/clicks by device, client, or geography. | NU✓ | MISSING | Explicit audit-F gap: no cohort/geo/device breakdowns. |
| ANA-016 | Web analytics (site traffic) | Cookieless website/pageview analytics bundled with email. | NU✓ | NOT_APPLICABLE | NU-only adjacent product (Staminads SDK); out of RF scope. |

## G — Automation (AUT, 18)

| ID | Feature | Description | Projects | RF status | Notes |
|---|---|---|---|---|---|
| AUT-001 | Visual workflow builder | Canvas builder for automation graphs with node palette. | NU✓ | IMPLEMENTED | @vue-flow builder, 9 node types, undo/redo (`AutomationBuilder.vue`). |
| AUT-002 | Event triggers | Enroll journeys on contact/list/email events. | NU✓ WR✓ | IMPLEMENTED | 7 trigger kinds + hooks wired across handlers; `email.sent/delivered` deliberately not triggerable (`domain.go:74`). |
| AUT-003 | Custom event ingest API | POST arbitrary events to trigger automations from external apps. | NU✓ WR✓ | IMPLEMENTED | `/automations/events` always-202 + properties ride journey context; no editor-role gate (audit-G flag). |
| AUT-004 | Email action node | Send a templated email as a workflow step. | NU✓ WR✓ | IMPLEMENTED | `SendAutomationEmail` (`nodes.go:143`). |
| AUT-005 | Delay/wait steps | Park journeys for minutes→weeks with restart-safe resume. | NU✓ WR✓ | IMPLEMENTED | `next_run_at` parking + poll resume surviving restarts (`engine.go:38`); no wait-until-event. |
| AUT-006 | Conditional branching | Filter/list-status branch nodes with exit semantics. | NU✓ WR✓ | IMPLEMENTED | filter + 3-way `list_status` branches; operators validated at save (`domain.go:632`). |
| AUT-007 | List-management action nodes | Add/remove contacts from lists as workflow steps. | NU✓ | IMPLEMENTED | List nodes in graph model (`nodes.go:73-99`). |
| AUT-008 | A/B split node | Weighted deterministic variant split inside workflows. | NU✓ | PARTIAL | FNV-1a hash split with weight validation; no winner selection or persisted outcome (`nodes.go:219-248`). |
| AUT-009 | Webhook action node (SSRF-guarded) | HTTP POST step with templated body and egress protection. | NU✓ | IMPLEMENTED | Static policy + pinned-dial transport + save-time URL validation + scope gate (`webhook_client.go:59`); Bearer not HMAC. |
| AUT-010 | Durable journey engine | Crash-safe multi-step execution (locks, CAS cursors, retry backoff). | NU✓ WR✓ | IMPLEMENTED | SKIP LOCKED claims, 10-node tick budget, 2^n backoff, lost-nudge sweep (~40 tests). |
| AUT-011 | Re-enrollment policy (once/every-time) | Control whether contacts can re-enter an automation. | NU✓ | IMPLEMENTED | Atomic dedup insert before journey creation (`triggers.go:119`). |
| AUT-012 | Execution stats & timeline observability | Per-journey/per-node stats, timelines, error sanitization. | NU✓ | IMPLEMENTED | Executions + node-stats endpoints + UI (`automation_handler.go:613`). |
| AUT-013 | Lifecycle (activate/pause/duplicate/delete-safe) | Draft/live states, duplication, safe deletes exiting journeys. | NU✓ | IMPLEMENTED | CAS transitions + live-edit pickup + transactional journey exits. |
| AUT-014 | Welcome/onboarding sequences | list.subscribed / contact.created trigger→delay→email chains. | NU✓ | IMPLEMENTED | End-to-end verified by engine tests (`engine_test.go:1304,1383`). |
| AUT-015 | Re-engagement / scheduled enrollment | Inactivity/date-based triggers or scheduled enrollment sweeps. | WR✓ | PARTIAL | Triggers purely event-reactive; no inactivity conditions, no cron enrollment. |
| AUT-016 | Stop-on-reply | Exit journeys when the contact replies. | NU✓ | MISSING | `ExitOnReply` reserved stub the engine never reads; no reply detection. |
| AUT-017 | Automated suppression on bounce/complaint | Suppress contacts automatically from bounce/complaint signals. | LM✓ BM✓ PO✓ US✓ NU✓ KE◐ MC✓ HR✓ WR✓ | IMPLEMENTED | 5xx auto-suppress (default on) + kind-tagged suppression upserts (`worker/handler.go:343,463`). |
| AUT-018 | Workflow testing / dry-run | Test-run a workflow against a sample contact before activation. | WR◐ | MISSING | Static validation only (reachability/cycles/weights); no test execution. |

## H — APIs & integrations (API, 19)

| ID | Feature | Description | Projects | RF status | Notes |
|---|---|---|---|---|---|
| API-001 | REST API (full product coverage) | Dashboard-equivalent HTTP API for all operations. | LM✓ US✓ NU✓ SP✓ KE✓ MC✓ HR✓ WR✓ HP✓ PO◐ SE◐ | IMPLEMENTED | 340 route definitions across ~13 route files (audit-H; supersedes seed "~200"). |
| API-002 | OpenAPI/Swagger docs | Generated spec + interactive docs UI. | LM✓ US✓ NU✓ KE✓ MC✓ WR✓ HP✓ | IMPLEMENTED | okapi OpenAPI + Scalar UI at `/docs` + webhook payload docs endpoint (`webhook_docs.go:29`). |
| API-003 | API keys (hashed, workspace-bound) | Hashed at rest, prefix lookup, optional workspace binding. | LM✓ BM✓ US✓ NU✓ SP✓ KE✓ MC✓ HR✓ WR✓ HP✓ | IMPLEMENTED | `psk_` keys SHA-256 hashed + per-key workspace + `X-Posta-Workspace-Id` (`api_key.go`). |
| API-004 | Scoped API keys / permissions | Scope model restricting keys to capability areas. | LM✓ NU✓ HR✓ HP✓ US◐ MC◐ | IMPLEMENTED | send/read/webhooks/write/admin scopes; structural route-group enforcement (`middleware.go:346`). |
| API-005 | Per-key IP/CIDR allowlists | Restrict API keys to source IP ranges. | BM✓ | IMPLEMENTED | `AllowedIPs` + `IPAllowed` at auth (`middleware.go:232`); also on relay credentials. |
| API-006 | API key expiry & rotation | Expiring keys with owner notifications ahead of expiry. | HP✓ | IMPLEMENTED | Expiry enforced in `ValidateKey` + 7-day notification cron (`api_key_expiry.go:26`). |
| API-007 | API rate limiting | Per-key/endpoint request throttling with 429 semantics. | SP✓ KE✓ MC✓ HR✓ WR✓ HP✓ LM◐ US◐ | PARTIAL | Hourly/daily send quotas only; no per-API-key or per-endpoint limits; fails closed on store error. |
| API-008 | Official SDKs | First-party client libraries. | PO✓ US✓ WR✓ | PARTIAL | In-repo SDK docs for 6 languages; Go/Node/PHP/Java repos confirmed, Python/.NET unverified. |
| API-009 | Outbound webhooks (event push) | Push product events to customer endpoints. | PO✓ US✓ NU✓ HR✓ WR✓ BM✗ LM✗ SP✗ KE✗ | IMPLEMENTED | 11-event registry (enum-pinning test) + scope/from/domain filtering (`dispatcher.go:310`). |
| API-010 | Webhook signatures & replay protection | Signed payloads with timestamp/replay defenses. | US✓ NU✓ HR✓ | PARTIAL | HMAC-SHA256 `X-Posta-Signature` (`dispatcher.go:485`); no timestamp/replay window. |
| API-011 | Webhook retries with backoff | Exponential backoff retry ladder for failed deliveries. | US✓ NU✓ HR✓ | IMPLEMENTED | 2s×2^n, 60s cap, jitter, default 3 retries (`dispatcher.go:56-71`); fixes seed R-06 "linear". |
| API-012 | Webhook delivery history & inspection | Persist per-attempt deliveries viewable via API/UI. | NU✓ HR✓ US◐ | IMPLEMENTED | `webhook_delivery_attempts` + deliveries API + incoming-webhook store. |
| API-013 | Webhook test-fire / replay | Send a test event or replay a failed delivery on demand. | NU✓ | MISSING | No test endpoint or replay (replay exists only as post-a6f1681 untracked code — excluded). |
| API-014 | Inbound ESP event webhooks (adapters) | Provider-shaped bounce/delivery event receivers (SES SNS etc.). | LM✓ SP✓ US✓ NU✓ MC✓ SE✓ WR✓ KE◐ HP◐ BM✗ | PARTIAL | Generic Posta schema only; no ESP-format adapters; compat with real ESP payloads untested. |
| API-015 | Native ESP sending integrations | Send through SES/Postmark/SendGrid-class APIs (not just SMTP). | NU✓ SP✓ KE✓ BM✓ US✓ MC✓ WR✓ | PARTIAL | Generic SMTP only — see INF-022 for the shared sending-side entry. |
| API-016 | MCP server | Model Context Protocol server exposing product actions to AI agents. | SE✓ MC✓ WR✓ LM◐ | MISSING | Zero MCP references (grep clean). |
| API-017 | CLI client | Command-line client for sending/management. | PO✓ BM✓ WR✓ | MISSING | Only server/worker/doctor subcommands; `doctor` = workspace-integrity checker (`main.go:37`). |
| API-018 | Zapier/platform integrations | No-code automation platform connectors. | NU✓ KE◐ | MISSING | None; community paths only in competitors. |
| API-019 | Embeddable core package | Install the product engine as a library inside another app. | SP✓ | NOT_APPLICABLE | SP-only architecture (`sendportal-core` Laravel package); not an RF goal. |

## I — Security & administration (SEC, 18)

| ID | Feature | Description | Projects | RF status | Notes |
|---|---|---|---|---|---|
| SEC-001 | Multi-tenancy (workspaces) | Isolated tenant containers with scoped data access. | PO✓ US✓ NU(E) SP✓ MC✓ HR✓ HP✓ KE◐ WR◐ SE◐ LM✗ | IMPLEMENTED | Workspace resolution enforced by construction + RBAC integration tests (`workspace.go:41`); no per-tenant encryption. |
| SEC-002 | RBAC | Roles/permissions enforced server-side. | LM✓ KE✓ PO◐ US◐ NU(E) SP◐ MC◐ | IMPLEMENTED | 4 roles + structural scope checks; automation routes lack editor-role gate (UI hidden only, c0408dc — audit-I top gap). |
| SEC-003 | Team management & invitations | Member management with expiring invitations. | LM◐ PO◐ US✓ NU✓ SP✓ MC✓ HP✓ | IMPLEMENTED | Admin-gated member routes + pending/accepted/declined invites + self-service acceptance. |
| SEC-004 | Audit logs | Actor/IP/outcome audit trail with export. | NU(E) WR◐ BM◐ LM✗ US✗ | IMPLEMENTED | 3 overlapping surfaces (workspace/deployment/admin) + CSV export ≤100k; no WORM, no read auditing. |
| SEC-005 | Two-factor auth (TOTP) | TOTP enrollment + login challenge + admin reset. | LM✗ | PARTIAL | TOTP real; `two_factor_required` enforcement is dead code (login never reads it), no recovery codes — P0 trust issue. |
| SEC-006 | SSO/OIDC (+ workspace SSO enforcement) | OIDC login with provider admin + per-workspace enforced SSO/domains. | LM✓ PO✓ NU(E) US◐ | IMPLEMENTED | Generic OIDC auto-discovery + account linking + `EnforceSSO`/`AllowedDomains` (`workspace_sso.go:25`); no SAML (absent industry-wide). |
| SEC-007 | Session management & revocation | Session list/revoke, JTI blacklist, admin force-revoke. | KE◐ | IMPLEMENTED | JTI uniqueIndex + Redis blacklist + self-service + admin revocation (`session_handler.go:59`). |
| SEC-008 | Configurable retention framework | Admin-configurable retention windows per data class. | PO✓ NU✓ SE✓ WR✓ | IMPLEMENTED | 10+ independent windows incl. per-form + spam-specific (`retention.go:195`); `security_events` window is dead — table grows unbounded. |
| SEC-009 | GDPR deletion & export | Contact/email-log erasure + workspace data export/import bundles. | LM✓ PO◐ US◐ NU◐ | IMPLEMENTED | GDPR endpoints + export/import with safety rails (domains re-verify on import). |
| SEC-010 | Inbound spam scanning & filtering | Weighted spam scanner with custom filters + anti-replay nonces. | BM✓ PO✓ | IMPLEMENTED | 11 weighted signals + admin filters + form nonce/time-trap (`messagescan/scanner.go:108`); exceeds peer "rate-limit only". |
| SEC-011 | Antivirus integration | ClamAV-style virus scanning of mail. | PO✓ | MISSING | Grep clean; inbound pipeline has no AV hook. |
| SEC-012 | Ban engine / brute-force protection | Sliding-window detectors, risk tiers, IP/CIDR bans at TCP/SMTP/HTTP. | BM✓ | IMPLEMENTED | 16-file security engine, CIDR pub/sub propagation, fail-open, admin bans+SSE (`internal/services/security/`) — RF differentiator. |
| SEC-013 | Admin-console IP allowlisting | Restrict admin UI/API access by source IP. | BM◐ | PARTIAL | Ban-engine allowlist (detector exemption) ≠ admin-access control; reverse proxy is the current answer. |
| SEC-014 | Plans & quota enforcement | Plan-based limits with per-workspace assignment. | KE✓ | IMPLEMENTED | Plan→default→settings resolution + admin plans CRUD (`plan/service.go:54`); billing out of scope (self-host). |
| SEC-015 | Admin impersonation | Admin "login as user" for support/debugging. | KE◐ HP✓ | MISSING | No impersonation (audit-I confirmed-unchanged list). |
| SEC-016 | Credential encryption at rest | Encrypt stored secrets (SMTP creds, provider tokens, signing keys). | SP✓ MC✓ | NEEDS_VERIFICATION | DKIM private keys AES-encrypted, never serialized (`domain_signing_key.go:73`); SMTP credential storage unaudited. |
| SEC-017 | Registration control / invite-only mode | Toggle open registration / gate signup. | SP✓ KE✓ MC✓ | PARTIAL | Verified-email gate on workspace create/invites/keys; global registration toggle not documented in audits. |
| SEC-018 | SSRF hardening on outbound HTTP | Block private/loopback targets on webhook and feed calls. | NU✓ | IMPLEMENTED | Shared SSRF guard: pre-flight resolution, pinned-dial, redirect block (`webhook_client.go:59`, `automation_handler.go:275`). |

## J — Infrastructure & observability (OBS, 19)

| ID | Feature | Description | Projects | RF status | Notes |
|---|---|---|---|---|---|
| OBS-001 | Horizontal scaling (server/worker split) | Stateless API server scaled independently from send workers. | PO✓ HR✓ NU◐ SP◐ | IMPLEMENTED | `POSTA_EMBEDDED_WORKER` split + standalone worker with own health server (`worker.go:47`). |
| OBS-002 | Multiple worker processes | Scale N workers with multi-replica safety. | PO✓ | IMPLEMENTED | SKIP LOCKED claims + deterministic TaskIDs + Inspector server visibility (`workermon/monitor.go:91`). |
| OBS-003 | Multiple sending servers / shared upstream pool | Per-user servers + admin-managed shared servers matched by domain. | BM◐ | IMPLEMENTED | Shared-server fallback by sender domain, strict mode requires ownership (`handler.go:227`); one server per domain match, no LB pool. |
| OBS-004 | Redis-backed queues/cache | Redis for queue broker, cache, rate limits, sessions. | US✓ SP◐ HR✓ MC✓ | IMPLEMENTED | 9 verified Redis roles (audit-I-J); single Redis is a SPOF (no sentinel docs). |
| OBS-005 | PostgreSQL system of record | Single durable relational store with versioned migrations. | LM✓ BM✓ US✓ NU✓ KE✓ HR✓ WR✓ HP✓ SP◐ SE◐ PO✗ MC✗ | IMPLEMENTED | GORM AutoMigrate + 6 versioned upgrade steps + migrate smoke test (`migration/upgrade/`); PO=MariaDB, MC=Mongo. |
| OBS-006 | Prometheus metrics | `/metrics` endpoint with named metric families. | PO✓ HR✓ | IMPLEMENTED | 20 families incl. `posta_queue_archived_tasks`; default-off, not UI-surfaced (`metrics.go`). |
| OBS-007 | Grafana dashboards | Bundled/sample dashboards for metrics. | HR✓ PO✗ | MISSING | `/metrics` is the integration surface; a sample dashboard JSON would close it cheaply. |
| OBS-008 | Health check endpoints | Process + dependency health/readiness probes. | PO✓ SE✓ MC✓ HR◐ | IMPLEMENTED | `/healthz` `/readyz` (DB+Redis) + worker health server + compose/Dockerfile HEALTHCHECKs (`health.go:62`). |
| OBS-009 | Queue metrics & latency observability | Queue depth/latency metrics and admin views. | PO✓ SP✓ KE✓ SE✓ | PARTIAL | Per-queue size/state via Inspector + SSE + gauges; no per-queue latency metric (PO added one in 3.3.0). |
| OBS-010 | Runtime log viewer | In-product application/runtime log viewing. | LM◐ BM◐ | PARTIAL | Rich business-event surfaces (events, webhook attempts); no stdout/app-log viewer. |
| OBS-011 | Error monitoring (Sentry) | Third-party error tracking integration. | PO◐ HP◐ NU◐ | MISSING | Grep clean; errors slog'ed only. |
| OBS-012 | Runtime resource monitoring | In-process runtime stats (goroutines, memory) streamed to admin. | — | PARTIAL | SSE `system.status` with NumGoroutine+MemStats (`admin_handler.go:666`); no CPU/disk/history. |
| OBS-013 | Blob storage abstraction (FS/S3) | File storage behind one interface (local FS or S3-compatible). | LM✓ US✓ NU✓ | PARTIAL | FS/S3 blob store + retention deletes blobs; no usage reporting or orphan GC. |
| OBS-014 | Cron job visibility | Admin listing of scheduled jobs. | SP◐ MC◐ | IMPLEMENTED | `/admin/crons` lists 10 jobs incl. scheduler + reconcile (`cron_handler.go:36`); no last-run/duration columns. |
| OBS-015 | Worker heartbeats & version detection | Liveness heartbeats with version/commit mismatch alerts. | — | IMPLEMENTED | Redis heartbeat 15s/45s TTL + connected/disconnected events + admin SSE mismatch detection (`workermon/heartbeat.go:34`). |
| OBS-016 | Deployment tooling (Docker/compose/CI) | Production image, compose, CI with real service containers. | LM✓ PO✓ US✓ NU✓ BM✓ SE✓ HR✓ | IMPLEMENTED | 3-stage image embedding UI + compose (PG17/Redis7 healthchecks) + CI with real PG/Redis + multi-arch releases. |
| OBS-017 | Backup/restore tooling | In-repo backup/restore guidance or tooling. | — | MISSING | Absent (seed R-11 still true); no Helm/k8s manifests either. |
| OBS-018 | Update check & version banner | Opt-out update check / new-version notice. | KE✓ SE✓ BM✓ | IMPLEMENTED | Update-check cron + admin surface; phone-home disable-able pattern matches KE/SE. |
| OBS-019 | Multi-replica cron leader election | Leader lock so cron jobs run once across replicas. | — | PARTIAL | No leader lock: all replicas run all 10 crons; alert jobs risk double-send (audit-J top gap). |

---

# §4 Cross-project observations

Brief by design — the full comparison matrices and per-feature ReForwarder verdicts belong to `02-competitor-comparison.md`.

## 4.1 Near-universal capabilities (present in ≥8 of the 12)

Campaign/bulk fan-out with drafts and scheduling (INF-006, CAM-001/002); subscriber records + CSV import (CON-001/009); RFC 8058 one-click unsubscribe with hosted pages (CON-014 — every marketing-capable project); background queue pipelines (INF-009); full-coverage REST APIs with hashed API keys (API-001/003); OpenAPI/Swagger docs among API-first projects (API-002); bounce ingestion with auto-suppression (INF-019, AUT-017); custom sending domains with ownership verification (DOM-001/002); Docker/compose deployment tooling (OBS-016).

## 4.2 Rare / differentiating capabilities

- **MCP servers:** native in SE, MC, WR (×2); community for LM. Absent elsewhere — an emerging 2026 pattern aimed at AI-agent-driven sending.
- **Full mail server (mailboxes + webmail):** BM only (INF-003) — structurally out of scope for every other product.
- **Embeddable engine package:** SP only (API-019) — Laravel-package architecture.
- **IP pools / multi-IP deliverability:** PO (full), BM (per-domain binding + warmup), HR (planned) — infra-first products only.
- **Built-in DNS automation:** HR (NS-delegated DNS server), MC (external DNS-helper service) — DOM-007 has two implementors total.
- **Pure observability layer:** SE is the only product that *only* observes (SES event stream) — a category of one, and evidence that "delivery telemetry without sending" is a viable product wedge.
- **Automation engines:** only NU (full canvas) and WR (code-first workflows) ship one; KE lists it as roadmap. This is the single most polarized category.
- **No-tracking-by-design:** HP (and HR at the infrastructure layer) are the only products where tracking absence is policy, not a gap.
- **AI-assisted creation:** BM (template gen + website profiling), NU (block drafting), WR (metered generation, EE-gated), MC (via external MCP agents), LM/KE (none, LM via community MCP).

## 4.3 Verified absent industry-wide (do not invent as competitor features)

Checked across all 12 dossiers and found in none (registry §"Capabilities verified ABSENT"; ReForwarder status noted): DMARC aggregate (rua) report ingestion; recurring campaigns; per-subscriber local-time send optimization (NU workspace-timezone only); user-facing manual webhook replay/redrive (history and auto-retry exist in US/NU/HR); retention legal-hold/compliance freeze; ML send-time optimization; SAML SSO (every SSO is OIDC/OAuth); SCIM provisioning; SPF flattening / 10-DNS-lookup validation; MTA-STS / TLS-RPT / DANE management; strict priority send queues (RF's weighted fair-share is the closest implementation). These are legitimate whitespace, not table-stakes.

## 4.4 License families and practical implications

| Family | Projects | Practical implication for ReForwarder research |
|---|---|---|
| **AGPL-3.0, clean single license** | LM, US, HP | Network copyleft (§13). Read/inspect freely for research; **clean-room discipline**: document behaviors from docs/dossiers, never port code, keep an evidence trail. |
| **AGPL-3.0 + commercial escape hatch** | HR (€10k/yr enterprise), MC (cloud = same codebase, `MAILCHEAP_CLOUD=true`) | Same AGPL discipline; the paid tier is a licensing shortcut, not extra code. MC's cloud/self-host split is one codebase — no separate EE tree to avoid. |
| **AGPL-3.0 + proprietary carve-out** | KE (`extra/` excluded from license), WR (`ee/` directories proprietary) | Core is AGPL (clean-room applies); **carve-out directories are off-limits for any reference beyond documented behavior** — WR's EE covers dashboard org/API/AI code and cannot legally run in production without a commercial agreement. |
| **MIT** | PO, SP | Permissive — behaviors and even code structure may be freely referenced; only attribution applies. |
| **BUSL-1.1 open-core** | NU (v40+; ≤v39 permanently AGPL-3.0; 6 licence-gated features) | BSL code is **not open source until its Change Date (4 yrs/version)** — treat v40+ as reference-only documentation; the ≤v39 AGPL history is the permissible open-source reference for the same features. Licence-gated features (SSO, granular RBAC, audit recording, SES tenants, translations, >3 workspaces) are still researchable as documented behaviors. |
| **Source-available (non-OSI)** | SE (O'Saasy: attribution + anti-SaaS clause) | Reading is permitted with attribution retained; the anti-SaaS clause means its *functionality* may not be offered hosted — irrelevant to ReForwarder's research use, but note it when citing SE as a "competitor" (it does not compete for sending). |

Cross-cutting license notes: the **registry-verified-absent list (§4.3) doubles as a freedom check** — since no project implements SAML/SCIM/recurring sends, ReForwarder implementing them infringes nothing. Notifuse's audit-log recording being a paid Licensed Feature makes independent implementation of similar audit trails legally prudent (dossier-05 §8). AGPL §13 applies to *modified versions offered over a network* — unmodified running is fine; any ReForwarder integration that embeds AGPL code (not merely interoperates via API) would trigger it.

---

# §5 Research limitations

1. **Web-first method.** Evidence was gathered via GitHub API metadata, raw file fetches (LICENSE, README, compose files, key source files), official docs and sites. **No full source-level clone verification was performed for most projects.** Exceptions/cross-checks: Notifuse was cross-checked against a prior local-clone audit (`docs/notifuse-logs-mapping.md`, pinned f635aa09, 2026-10-28); ReForwarder's own baseline (`a6f1681`) was spot-checked against the local repo. Behavior-level claims (exact code paths, undocumented handlers) for other projects should be re-verified against a clone before engineering decisions rely on them.
2. **NEEDS_VERIFICATION tally: 115 items across 12 projects.** LM 9 · BM 12 · PO 10 · US 7 · NU 10 · SP 11 · KE 11 · MC 9 · SE 7 · HR 6 · WR 12 · HP 11. Highest-risk clusters: BM (development-stall unknowns), WR (EE-undocumented org/RBAC), KE/SP/HP (feature-absence claims that are "absent from docs" rather than "confirmed absent from code").
3. **Self-hosting nuances — the four cases to handle specially in later docs:**
   - **Sessy** — fully self-hostable *by design*, but source-available (O'Saasy) with an anti-SaaS clause; "open-source" marketing overstates it.
   - **Wraps** — self-host control plane is documented and real, but **licence-gated** (`wraps_lic_` key) and the EE directories it runs are proprietary; not fully FOSS despite public AGPL core.
   - **MailCheap** — self-hostable and un-gated, but **Upstash QStash, MongoDB, and SES/PostShiba are mandatory** third-party services; no air-gapped path; no Docker image; no releases.
   - **Hyvor Post** — **not self-hostable in a supported way** per the vendor's own README/open-source FAQ, while the privacy-first page and a published Hosting guide say otherwise; OIDC sign-in is the named blocker. Re-verify against the moving docs before any comparative claim about HP self-hosting.
4. **Date-of-research caveat.** All facts are as of **2026-11-05**. High-velocity projects move materially between drafting and reading: WR ships ~weekly CLI releases, HP runs 0.0.x patch trains with empty release notes, MC has no releases at all (rolling git), SE ships rolling `:main` images, BM may have stalled entirely. Where this doc and later docs disagree on a fast-moving fact, re-check the primary source at the later date.
5. **Dossier-scope asymmetry.** Flag semantics differ slightly across dossiers (e.g., LM uses ❓ for unverified; SE/HR use [EV]); everything was normalized to the §-header legend, but "absent" sometimes means "not documented" — the §3 Projects column only carries flags backed by dossier evidence, and unverified items were omitted rather than guessed.
6. **Registry exclusion rule.** Post-`a6f1681` untracked ReForwarder work (ESP `provider_events` ingestion, webhook replay) is **excluded** from all RF status columns; statuses reflect `a6f1681` only.

---

# §6 Sources (per project, from dossiers 01–12)

All sources consulted 2026-11-05. Full URL-level detail is in each dossier (`C:\Users\Ibrahim\.zcode\tmp\oss-audit-2026-11\dossier-XX-*.md`); the lists below name every source class consulted.

- **LM (Listmonk)** — GitHub API `repos/knadh/listmonk`; raw `LICENSE`, `README.md`, `cmd/init.go`, `Dockerfile`, `i18n/`; releases index + v4.1.0/v5.0.0/v6.1.0/v6.2.0/v7.0.0-beta.1 notes; listmonk.app docs (concepts, templating, bounces, messengers, configuration, installation, querying-and-segmentation, external-integration, apis/*, roles-and-permissions, oidc, archives, i18n, maintenance/performance); homepage; live demo.
- **BM (BillionMail)** — GitHub API `repos/aaPanel/BillionMail` (redirect → `Billionmail/BillionMail`), releases/commits/languages, raw README/LICENSE/docker-compose.yml (dev branch); billionmail.com docs (/start/what, quickstart, marketing, contacts, api_mail_guide, api_usage_guide, domain, smtp, mailbox, support); website + /ai attempt; third-party context (faun.dev, martechsignal.com); GitHub issues #386, #150.
- **PO (Postal)** — GitHub API `repos/postalserver/postal` (+ /license, /releases, /contents, `atech/postal` redirect check); raw Gemfile, Gemfile.lock, MIT-LICENCE, docker-compose.yml; docs.postalserver.io (getting-started, prerequisites) and raw docs repo: feature-list, mail-server-settings, sending-domains, smtp-authentication, smtp-tls, ip-pools, click-and-open-tracking, oidc, health-metrics, containers, wildcards-and-address-tags, api, client-libraries, webhooks, auto-responders-and-bounces; github.com/postalserver/install (+ compose template, install script); api.github.com/repos/postalserver/docs tree; third-party: powermtaproviders.com, postalserver discussion #1513.
- **US (useSend)** — GitHub API `repos/usesend/usesend`; raw LICENSE, README, prisma/schema.prisma, domain-service.ts, queue services, suppressions, smtp-server, packages/; releases v1.9.5–v1.10.0; docs.usesend.com (introduction, self-hosting/overview + railway, get-started/{smtp,nodejs,go}, guides/{webhooks,double-opt-in,campaign-personalization,use-with-react-email}, api-reference/{introduction, emails/*, campaigns/*, analytics/*}, llms.txt); usesend.com; community SDK usesend-rs.
- **NU (Notifuse)** — GitHub API `repos/Notifuse/notifuse` (license NOASSERTION) + releases v39.1–v42.1; raw LICENSE (BSL 1.1 + Licensed Features), LICENSING.md, README; docs.notifuse.com via llms.txt (features/{workspaces, contacts, lists, templates, broadcast-campaigns, transactional-api, custom-events, automations, ai-assistant, file-manager, webhooks, logs, audit-logs, admins-api-keys, notification-center}, self-hosting/{installation, licence}, integrations, web-analytics/*, blog/*); www.notifuse.com; prior internal clone audit `docs/notifuse-logs-mapping.md` (f635aa09, 2026-10-28).
- **SP (SendPortal)** — GitHub API `repos/mettle/sendportal` + `sendportal-core`; raw LICENSE, README, composer.json; releases v1.0.3–v4.0.0.1; commits API (AI co-author trail); sendportal.io/docs/v4 (installation, configuration-and-setup, features/{subscribers,tags,templates,campaigns,messages,workspaces}, email-services/{introduction,smtp,aws,postmark,sendgrid}, api/introduction); homepage; web search for hosted status/third-party hosts (365i, Railway, Capterra).
- **KE (Keila)** — GitHub API `repos/pentacent/keila` (+ releases ×30, commits, contents/lib trees); raw README, LICENSE.md, CHANGELOG.md, mix.exs, ops/docker-compose.yml, extra/README.md; keila.io (homepage features/roadmap/updates, docs/installation, docs/configuration) via keila-web source `src/content/docs/en/*.mdx` (forms, contacts, segments, senders, campaigns, public-links, managed-dmarc, shared-domains, api); GitHub code search (webhook/oidc/two_factor).
- **MC (MailCheap)** — GitHub API `repos/marclou/mailcheap`; raw LICENSE, README, SECURITY.md, package.json, .env.example, models/Campaign.js, lib/newsletter-settings.js, contents trees, code search (replyTo); mailcheap.ai; opensourcealternatives.to (discovery only).
- **SE (Sessy)** — GitHub API `repos/marckohlbrugge/sessy` (authenticated gh after rate limit; license NOASSERTION; empty releases); raw LICENSE.md (O'Saasy), saas/LICENSE.md + saas/README.md, README, config/routes.rb, db/schema.rb, app/models/event/types.rb, docs/docker-deployment.md, docs/aws-ses-setup.md; trees (docs/, saas/, app/) + languages; sessy.do; third-party corroboration: hatchbox.io article, PikaPods/Railway listings.
- **HR (Hyvor Relay)** — GitHub API `repos/hyvor/relay` (+ releases 0.0.44–0.0.48, languages); raw LICENSE, README, ROADMAP.md, compose.yaml; relay.hyvor.com (homepage, /docs incl. domains, send-emails, send-emails-smtp, api-console, webhooks, /hosting); WebSearch discovery (r/selfhosted, codeline.co — not fetched).
- **WR (Wraps)** — GitHub API `repos/wraps-team/wraps` + wraps-js (license/stars/pushed_at/releases); raw LICENSE, ee/LICENSE.md, README; wraps.dev (homepage, llms.txt, llms-full.txt, /changelog, docs/guides/{self-hosted,account-health,webhooks}, docs/infrastructure (+sms, cdn, events), docs/quickstart/{platform, email/templates, email/workflows, email/inbound, email/agents}, docs/reference/{api,rate-limits}, docs/mcp-reference, docs/cdk-reference, docs/pulumi-reference, docs/cli-reference); glama.ai MCP listing (secondary).
- **HP (Hyvor Post)** — GitHub API `repos/hyvor/post` (+ releases 0.0.54–0.0.63, commits, README history, issues); raw README, LICENSE, Dockerfile, compose.yaml, backend/.env, backend/composer.json, Entity/Api/Schedule trees, docs/docs/en/{Introduction,SignupForm,EmailDesign,Import,ConsoleApi}.md, docs/hosting/en/Introduction.md; hyvor.com/post (docs index, /hosting, /open-source, /privacy-first); companion `repos/hyvor/relay` + Relay README.

**Registry cross-reference:** `C:\Users\Ibrahim\.zcode\tmp\oss-audit-2026-11\registry.md` (v1.0.0, frozen 2026-11-05) — the ID authority for this doc and all later docs in the suite. ReForwarder-side evidence lives in `audit-A-B.md` … `audit-I-J.md` in the same directory; their reconciliation into the RF status columns is documented in the registry header.

**Feeds:** `02-competitor-comparison.md` (normalized matrices + ReForwarder gap verdicts) consumes §2 and §3 of this document directly.
