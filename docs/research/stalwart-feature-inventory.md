# Stalwart Feature Inventory (source-evidence catalog)

> **Reference inspected:** Stalwart Mail & Collaboration Server, `main` @ `8b326865` = **v1.0.3** (fetched 2026-11-05; the 2026-11-02 security deep-dive used `23b69755` = v1.0.1 — Reports A–G in `docs/security-research/` cite that baseline; the delta is two patch releases).
> **Licenses** (`LICENSES/`): AGPL-3.0-only (core), Apache-2.0, MIT, and `LicenseRef-SEL.txt` — Stalwart Enterprise License for closed-source enterprise components (AI spam, tenants, traces/metrics stores, SCIM, sharded stores, archived items, live tracing/metrics, masked email).
> **Companion documents:** `stalwart-feature-gap-analysis.md` (main report), `reforwarder-capability-audit.md`, `reforwarder-stalwart-roadmap.md`.
>
> **Method note:** every entry was verified in source (not from README/marketing). The verification algorithms for SPF/DKIM/DMARC/ARC live in the external `mail-auth 0.15.1` crate (not vendored); this inventory covers the integration, policy, reporting and delivery wiring. Unit tests live in a sibling `stalwart-tests/` repository that is **not part of this clone** — test verification is therefore marked N/A. Coverage honesty is enforced by the checklist at the end.

Feature IDs (`SF-nn`) are referenced by the gap analysis and roadmap. Enterprise (SEL) features are marked **[ENT]**.

---

## A. Email protocols & mail delivery

| ID | Feature | Evidence (files / functions) | Notes |
|---|---|---|---|
| SF-01 | Full ESMTP/LMTP state machine (EHLO, MAIL, RCPT, DATA, RSET, NOOP, VRFY, EXPN, STARTTLS, AUTH) | `crates/smtp/src/inbound/session.rs` (`Session::ingest`, 736 ln), `inbound/{ehlo,mail,rcpt,spawn}.rs`, `crates/smtp-proto/` | BDAT chunking with per-session memory budgets; ETRN/ATRN/BURL refused (502) |
| SF-02 | ESMTP extension advertisement (PIPELINING, CHUNKING, 8BITMIME, BINARYMIME, SMTPUTF8, ENHANCEDSTATUSCODES, DSN, REQUIRETLS, SIZE, NO-SOLICITING, FUTURERELEASE, DELIVERBY, MT-PRIORITY) | `inbound/ehlo.rs:131-223`; `config/smtp/session.rs:71-83` | All gated by per-stage IfBlock expressions |
| SF-03 | Submission (MSA) with delivery-status writeback to the mail store | `queue/submission.rs` (`write_delivery_results`), `inbound/data.rs` (`MessageOrigin::Submission`) | `auth.require` gate |
| SF-04 | Relay control / open-relay prevention | `inbound/rcpt.rs:225-238`, `common/src/network/mta.rs::rcpt_resolve` | `550 5.7.1 Relay not allowed` |
| SF-05 | Routing strategies: Local (mailbox ingest), Mx (native MX), Relay (smart host) selected by expressions | `outbound/delivery.rs:250-365`, `config/smtp/queue.rs:53-208` | Per-recipient, per-(domain, route) grouping |
| SF-06 | MX handling: preference ordering, multi-exchange shuffle, null-MX failure, implicit MX, loopback rejection, multi-homed IP strategies, DNSSEC status | `outbound/lookup.rs:243-297`, `delivery.rs:607-688` | `max_mx` cap |
| SF-07 | Outbound relays: host/IP, port, SMTP or LMTP, SASL (PLAIN/LOGIN/bearer), implicit TLS vs STARTTLS, invalid-cert allowance, per-relay **source-IP pools**, outbound pipelining (100 RCPTs) | `outbound/mod.rs:259-409`, `outbound/client.rs` (817 ln) | |
| SF-08 | Persistent multi-queue manager with virtual queues, per-recipient retry/notify schedules (configurable interval lists), expiry by TTL or attempt count, event-locked delivery (cluster-safe) | `queue/manager.rs` (893 ln), `queue/spool.rs` (1329 ln), `queue/mod.rs` | Queues persisted in the data store as compressed archives |
| SF-09 | Future release (HOLDFOR/HOLDUNTIL), DELIVERBY, MT-PRIORITY (±9), DSN params (NOTIFY/ORCPT/RET/ENVID) | `inbound/mail.rs:252-422`, `inbound/data.rs:1005-1088` | |
| SF-10 | Full DSN generation (RFC 3464) incl. delayed/failed, double-bounce handling, signed DSNs, DKIM2-aware DSN forwarding | `queue/dsn.rs` (1306 ln) | |
| SF-11 | Forwarding chain: sub-addressing, directory recipients, mailing-list expansion (recursive, ORCPT propagation), per-domain catch-all, aliases | `mta.rs::rcpt_resolve:54-230`, `cache/principals.rs` | |
| SF-12 | Envelope rewriting (MAIL FROM / RCPT TO) via IfBlock + Sieve SetEnvelope | `inbound/mail.rs:190-213`, `inbound/rcpt.rs:150-179` | |
| SF-13 | Traceability: Received header (HELO, PTR, IP, ASN/country, TLS version/cipher, protocol token, queue id), full trc-logging of I/O and attempts | `inbound/data.rs:1132-1185`, trc throughout | |
| SF-14 | Local delivery engine running each recipient's Sieve script at ingest | `outbound/local.rs`, `email/src/message/delivery.rs:207-242` | |
| SF-15 | Milter client (Connect/Ehlo/Mail/Rcpt/Data/Eom stages; header/rcpt/body modifications) | `inbound/milter/` | |
| SF-16 | MTA hooks: first-party HTTP hooks per SMTP stage, HMAC-signed JSON, response `modifications` | `inbound/hooks/` | Replaces classic webhook integration |
| SF-17 | Queue quota reservations (sender/rcpt/rcpt-domain, count+size) enforced at ingest (452 4.3.1) | `queue/quota.rs` | |
| SF-18 | Queue blob encryption per recipient group (ECDH) **[ENT]**; at-rest encryption setting | `queue/encrypt.rs` | |
| SF-19 | Tenant-scoped queues **[ENT]** | `queue/tenant.rs` | |
| SF-20 | DKIM signing at queue time: DKIM1 (RSA-SHA256, Ed25519-SHA256) + **DKIM2 chained signatures** (envelope-bound, chain verification/restart) | `inbound/dkim.rs` (543 ln) | |
| SF-21 | MT-PRIORITY ingress (RFC 6710) + virtual-queue service ordering | `inbound/mail.rs:303-318`, `queue/manager.rs` | |

## B. Email security & authentication

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-22 | SPF verification (EHLO + MAIL FROM identities), Relaxed/Strict modes, Received-SPF header, strict rejection | `inbound/mail.rs:424-509`, `config/smtp/auth.rs:56-60` | Algorithm in `mail-auth` |
| SF-23 | DKIM verification incl. insecure-signature stripping, strict mode, ARF auth-failure reports | `inbound/data.rs:157-230`, `reporting/dkim.rs` | |
| SF-24 | DMARC verification with policy enforcement (Reject/Quarantine/None), multi-author-domain rejection | `inbound/data.rs:445-550, 1188-1216` | |
| SF-25 | DMARC aggregate reports — **send** (windowed, signed, per-domain) **and receive** (interception, XML parse, store/forward, tenant attribution) | `smtp/src/reporting/dmarc.rs` (681 ln), `reporting/{index,inbound,scheduler,send}.rs`, `smtp-report/src/dmarc/` | |
| SF-26 | SPF/DKIM auth-failure (ARF, RFC 5965/6591) reports with rate limiting + loop suppression | `reporting/{spf,dkim}.rs` | |
| SF-27 | ARC **verification** (no sealing) | `inbound/data.rs:232-274` | Sealing explicitly absent |
| SF-28 | DKIM2 verification (chained, envelope-bound) + forged-DSN protection | `inbound/data.rs:276-408` | Draft spec |
| SF-29 | MTA-STS client (policy fetch/cache/backoff, MX validation, Enforce blocking) + local MTA-STS policy server | `outbound/mta_sts/`, `delivery.rs:454-605` | |
| SF-30 | DANE (TLSA) with DNSSEC-validated lookups, end-entity/trust-anchor matching, union enforcement with REQUIRETLS/MTA-STS | `outbound/dane/`, `delivery.rs:838-1009` | |
| SF-31 | TLS-RPT: outbound report build/send + inbound parse | `reporting/tls.rs` (449 ln), `smtp-report/src/tlsrpt/` | |
| SF-32 | Reverse DNS (iprev) validation with strict modes | `inbound/mail.rs:57-111` | |
| SF-33 | TLS server stack: rustls, STARTTLS, SNI with wildcard fallback, validity filtering | `common/src/network/tls.rs`, `config/server/tls.rs` | |
| SF-34 | ACME certificate automation (Http01, Dns01, DnsPersist01, TlsAlpn01; scheduled renewal; per-domain manual opt-out) | `common/src/network/acme/` (2,277 ln) | |
| SF-35 | SASL: PLAIN, LOGIN, OAUTHBEARER, XOAUTH2 (TLS-gated), MFA-token and app-password flows, auth-error throttling | `inbound/auth.rs` (356 ln) | SCRAM/GSSAPI constants exist but are unimplemented |
| SF-36 | Password hashing: Argon2id (default), PBKDF2, scrypt, bcrypt, sha-crypt family | `directory/src/core/secret.rs` | No password-policy object |
| SF-37 | OAuth 2.0 / OIDC **provider** (auth code + refresh, device grant, introspection, revocation, dynamic client registration, CIMD) and directory backends (internal, LDAP, SQL, OIDC) | `common/src/auth/oauth/`, `directory/src/backend/` | |
| SF-38 | Abuse prevention suite (already analyzed in Reports A–G): fail2ban-style `BlockedIp` with four ban classes, greylisting, inbound/outbound/remote rate limiters, DNSBL, auth-error delays, HTTP banned paths | `network/security.rs`, `inbound/rcpt.rs:253-328`, `core/throttle.rs` | ReForwarder already ported the core of this (security engine) |
| SF-39 | Spam filter: ~23 analyzer modules / ~200 tags (headers, Received chain, Message-ID, dates, subject, From/Reply-To identity, URL analysis with redirect chasing + homographs, MIME, HTML, charset), tag→score glob mapping, Allow/Discard/Reject thresholds, X-Spam-* headers, per-recipient spam percentage | `crates/spam-filter/src/analysis/*` | |
| SF-40 | Statistical spam classifier: FTRL-Proximal online logistic regression over hashed features, reservoir-sampled training, auto-learn triggers, per-user or global models, scheduled retrain | `crates/nlp/src/classifier/`, `tasks/spam_classifier.rs` | |
| SF-41 | AI spam verdicts (LLM via Anthropic/OpenAI/Gemini/Ollama) with confidence gating **[ENT, SEL-licensed file]** | `common/src/enterprise/ai/`, `analysis/ai/` | |
| SF-42 | Pyzor client, spam traps, GTUBE test, attachment extension blocklists, external rule feed (`spam_rules_url`) | `analysis/pyzor.rs`, `modules/dnsbl.rs`, `tasks/spam_classifier.rs:336+` | |
| SF-43 | Connection/DoS budgets: global buffered-memory accounting, per-session reservation, processing multiplier, transfer limits, header-size limits | `inbound/session.rs:47-58`, `network/memory.rs` | |
| SF-44 | Security event taxonomy (~40 trc event families incl. Security/Auth/Spam/DANE/MtaSts/Dkim2) | `crates/trc/` | |
| SF-45 | Sieve-driven policy at Connect/EHLO/MAIL/RCPT/DATA stages (trusted scripts), Accept/Replace/Reject/Discard outcomes | `inbound/{spawn,ehlo,mail,rcpt,data}.rs`, `scripts/` | |

## C. Message filtering & policy

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-46 | Sieve interpreter: full RFC 5228 + 47 extensions (vacation, editheader, imap4flags, mailbox, duplicate, convert, extracttext, extlists, fcc, mime/foreverypart, regex, spamtest, virustest, include, imapsieve, expressions, while loops) with CPU/memory limits | `crates/sieve/` (100 files), `compiler/grammar/mod.rs:29-79` | Vendored sieve-rs |
| SF-47 | ManageSieve (RFC 5804) complete command set | `crates/managesieve/` | |
| SF-48 | Per-account Sieve at final delivery: FileInto (Junk quarantine-equivalent via spam percentage), Vacation with dedup, Redirect re-queue, Notify | `email/src/sieve/ingest.rs` | |
| SF-49 | Header inspection/transformation: Sieve editheader, milter headers, MTA-hook modifications, built-in Authentication-Results/Received-SPF injection, header count/size limits | `runtime/message/edits.rs`, `inbound/milter/message.rs:305-395` | |
| SF-50 | Conditional routing/policy engine: the IfBlock expression language drives relay permission, rewriting, signer selection, queue/virtual-queue, TLS strategy, throttles, report addresses, spam actions | `common/src/expr/`, `queue/mod.rs::eval_strategy` | The single most reused Stalwart mechanism |
| SF-51 | Quarantine-equivalent: per-recipient spam percentage filed to Junk special-use at delivery | `email/src/sieve/ingest.rs:836` | No dedicated quarantine store |

## D. Queue & delivery management (admin surface)

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-52 | Queue inspection API: list/query/patch/destroy queued messages; per-recipient schedule/expiry/status patching; cancel; requeue | `jmap/src/registry/mapping/queued_message.rs` | Listing read-limit 4096 |
| SF-53 | SMTP response taxonomy surfaced to admins: `HostResponse{hostname, code, enhanced, text}`, typed errors (DnsError, UnexpectedResponse{command,…}, ConnectionError, TlsError, DaneError, MtaStsError, RateLimited, ConcurrencyLimited) | `queue/mod.rs`, `registry/src/schema/structs.rs` | |
| SF-54 | Global queue pause/resume (cluster-broadcast); **no per-domain suspension** | `mapping/action.rs:155-180`, `common/src/ipc.rs` | Negative finding |
| SF-55 | Live delivery diagnosis: SSE-streamed connect test (MX → MTA-STS → connect/EHLO/MAIL/RCPT/DATA with per-stage timing) **[ENT]** | `http/src/api/diagnose.rs` | |
| SF-56 | Delivery tracing: queryable trace store **[ENT]** + live tracing SSE **[ENT]** | `mapping/telemetry.rs` | |
| SF-57 | DMARC/TLS/ARF report admin objects (query/destroy internal+external) **[ENT for stores]** | `mapping/report.rs` | |
| SF-58 | Queue virtual queues, named queues, delivery-schedule objects, per-queue worker threads | `MtaVirtualQueue`, `MtaDeliverySchedule` objects | |
| SF-59 | Operational Action objects: ReloadSettings, ReloadTlsCertificates, ReloadLookupStores, ReloadBlockedIps, UpdateApps, InvalidateCaches, TroubleshootDmarc, ClassifySpam, Pause/ResumeMtaQueue | `mapping/action.rs` | |

## E. Domain management

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-60 | Domain CRUD with aliases, catch-all, sub-addressing, relay flag, report address, per-domain DKIM/DNS/certificate management switches | `mapping/domain.rs::validate_domain`, `structs.rs Domain` | |
| SF-61 | **Automatic DNS record publication** (DKIM, TLSA, SPF, MX, DMARC, SRV, MTA-STS, TLS-RPT, CAA, autoconfig) via 11 provider integrations (TSIG RFC 2136, Cloudflare, GCloud, PowerDNS, Infoblox, cPanel, Plesk, …) with propagation waiting | `common/src/network/dns/update.rs`, `services/tasks/dns.rs` | **No ownership-verification TXT challenge** — admin-configured trust |
| SF-62 | DKIM key lifecycle: signature objects with rotation stages (Active→Pending→Retiring→Retired) + scheduled rotation task generating keys, publishing DNS, retiring old selectors | `registry/src/utils/dkim.rs`, `tasks/dkim.rs` | |
| SF-63 | DMARC/SPF/DKIM report settings objects + `TroubleshootDmarc` diagnostic (full SPF/iprev/DKIM/ARC/DMARC re-check of a pasted message) | `mapping/action.rs:488-635` | |
| SF-64 | Domain health endpoint | **Negative** — none; closest: DNS propagation check + live delivery test | |

## F. Identity & account management

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-65 | Accounts (User/Group) with credential lists, aliases, quotas, locale/timezone, encryption-at-rest | `registry/src/schema/structs.rs` | |
| SF-66 | ~660 granular permissions; custom Role objects with inherit/merge/replace composition; tenant default roles **[ENT]** | `schema/enums.rs:1778-2547` | |
| SF-67 | API keys + app passwords as registry objects: description, expiry, **per-credential permission narrowing** (Inherit/Disable/Replace), IP allowlists | `structs.rs` CredentialPermissions | |
| SF-68 | OAuth grant/session administration (view/revoke), OAuth clients, Application objects bundling UI+scopes | `SysOAuthGrant*` | |
| SF-69 | 2FA: TOTP only; **no WebAuthn/passkeys** | `directory/src/core/secret.rs` | Negative finding |
| SF-70 | Admin **impersonation** (`Permission::Impersonate`) | `authentication.rs:323` | |
| SF-71 | SCIM 2.0 provisioning (users/groups/bulk RFC 7644, filter, cursor pagination, ETags) **[ENT]** | `crates/scim/` | |
| SF-72 | Tenants: object + quotas + tenant-scoped admin views + queue/report tagging **[ENT]** | `structs.rs Tenant`, `queue/tenant.rs` | |
| SF-73 | Account destruction task (staged, with grantee cleanup) | `tasks/destroy_account.rs` | No explicit "suspended" state |
| SF-74 | Admin audit history | **Negative** — no audit-log feature; only store-level changelogs | |

## G. Storage & message management

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-75 | Three-plane storage: data store + blob store + in-memory/lookup store + search store + registry store | `store/src/lib.rs:369-439`, `store/src/build/` | |
| SF-76 | Data-store backends: RocksDB, SQLite, PostgreSQL, MySQL, FoundationDB, ephemeral, HTTP KV **[ENT read replicas]** | `store/src/backend/` | |
| SF-77 | Blob stores: FS, S3 (self-contained SigV4 client), Azure, store-backed **[ENT sharded]**; content-addressed (hash) with link tables + ref counting; orphan GC; recode task | `store/src/write/blob.rs`, `backend/s3/`, `tasks/{maintenance,blob_recode}.rs` | |
| SF-78 | Blob encoding: zstd compression + AES-256-GCM/ChaCha20 encryption at rest with per-blob keys; per-account S/MIME encryption-on-append | `store/src/write/blob_codec/`, `common/src/storage/encryption.rs` | |
| SF-79 | Search: built-in inverted index (custom tokenizer, zstd term dictionaries, per-collection search fields) + Elasticsearch/Meilisearch backends; async index queue with reindexing | `store/src/search/`, `services/index_queue/` | |
| SF-80 | Quotas: per-account disk + ~16 object-count quotas; recalculation tasks; IMAP QUOTA; JMAP quota capability **[ENT tenant quotas]** | `common/src/storage/quota.rs`, `tasks/maintenance.rs:457-580` | No per-domain quota |
| SF-81 | Retention/expiration: DataRetention settings (trash/submissions/scheduling expunges), cron purge tasks (store, blobs, spans, metrics, reports), change-log truncation | `structs.rs:1098-1118`, `tasks/maintenance.rs` | |
| SF-82 | Backup/restore: offline stop-and-dump CLI (`--export/--import`), 8 data families, LZ4 frames; enterprise deleted-item archiving/restore **[ENT]** | `manager/backup.rs`, `restore.rs` | Not a hot backup |
| SF-83 | Compression: zstd with embedded per-type dictionaries | `store/src/write/compress.rs` | |

## H. APIs & integrations

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-84 | JMAP server: core/mail/submission/vacation/sieve/quota/blob/files/contacts/calendars/principals/websocket/webpush capabilities; download/upload/eventsource/websocket endpoints | `crates/jmap/`, `http/src/request.rs:141-330` | |
| SF-85 | IMAP4rev1+rev2 with ~35 extensions (IDLE cluster-wide, ACL, QRESYNC, OBJECTID, UIDONLY, JMAPACCESS, …) | `imap-proto/src/protocol/capability.rs:157-230` | |
| SF-86 | POP3, ManageSieve servers | `crates/pop3`, `crates/managesieve` | |
| SF-87 | Management API = JMAP registry methods over ~120 object types; REST is deliberately thin (13 helper paths incl. `/api/live/delivery/{target}` SSE diagnostics) | `jmap/src/registry/{get,query,set}.rs`, `api/v1/openapi.yml` | |
| SF-88 | API auth: Basic + Bearer, OAuth2/OIDC provider, API keys/app passwords with permission narrowing + IP allowlists, per-request rate/concurrency limiter | `http/src/auth/` | |
| SF-89 | Web Push (RFC 8030/8291/8292 VAPID) + SSE + WebSocket push; emailpush filtered payloads | `services/state_manager/{push,ece}.rs` | No APNs/FCM |
| SF-90 | Outbound telemetry webhooks: HMAC-SHA256-signed event batches with throttle/backoff/max-pending | `common/src/telemetry/webhooks/` | |
| SF-91 | Lookup stores (Redis/HTTP/memory/SQL/local) usable by Sieve plugins, rate limits, spam traps | `store/src/backend/{redis,http}/` | |

## I. Monitoring & observability

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-92 | Zero-copy event/metrics core: ~40 event families, hundreds of counters/histograms, IPC aggregation | `crates/trc/` | |
| SF-93 | Prometheus/OpenMetrics text endpoint (optional Basic auth) + OTLP push exporter (scheduled) | `telemetry/src/prometheus/`, `config/telemetry.rs` | |
| SF-94 | Structured log sinks: console, rotating files, journald, OTLP traces, webhooks, store tracer **[ENT]**; per-event-type level filtering; live tracing stream **[ENT]** | `config/telemetry.rs:58-75` | |
| SF-95 | Health: `/healthz/live` + `/healthz/ready` (503 until store configured); systemd sd-notify state machine | `http/src/request.rs:699-717` | |
| SF-96 | Metric alerts (condition expression → email/event action, 5-min evaluation) **[ENT]**; metrics/trace history stores **[ENT]** | `enterprise/alerts.rs` | |

## J. Administration

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-97 | Admin UI: **external repository**; this repo ships only a login/theme shell + the settings JSON schema served at `/api/schema/{hash}` (UI forms generated from it) | `resources/html-templates/`, `resources/schema/` | |
| SF-98 | Configuration = typed registry objects in the data store (no TOML at runtime); JSON bootstrap file; env overrides for recovery/bootstrap modes | `store/src/registry/local.rs`, `boot.rs` | |
| SF-99 | Operational controls (Action objects, cluster-broadcast); task manager with scheduler-role gating so only some nodes run jobs | `mapping/action.rs`, `services/task_manager/` | |
| SF-100 | Configuration history/rollback | **Negative** — none; only optimistic concurrency + change log | |

## K. Automation & extensibility

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-101 | Sieve system + user scripts at every SMTP stage and delivery (see SF-45/48) | `smtp/src/scripts/` | |
| SF-102 | Sieve plugin functions: 13 registered external functions (query, exec external program, lookup KV, dns, http fetch, headers, text, **llm_prompt [ENT]**) with trusted/untrusted sets | `common/src/scripts/plugins/` | No dynamic .so plugins |
| SF-103 | Expression language with regex capture, SIMD kernels, async functions — reused across spam rules, throttles, hooks, alerts, firewall | `common/src/expr/` | |
| SF-104 | Scheduled task system: ACME, DKIM rotation, DNS management, report sending, purges, spam training, OTLP push, alarms, iMIP, account destruction, blob recode; distributed role gating + reconciliation | `services/task_manager/` | |
| SF-105 | Cluster broadcast fabric: Redis/NATS/Zenoh/Kafka pub/sub for registry changes, cache invalidations, queue status/rescans | `crates/coordinator/`, `services/broadcast/` | |

## L. Performance & reliability

| ID | Feature | Evidence | Notes |
|---|---|---|---|
| SF-106 | Store-level queue failover: shared store + distributed event locks (no double-delivery); no bespoke queue replication | `spool.rs`, `coordinator/` | |
| SF-107 | Cache layers with TTL + negative caching + targeted cluster invalidation + large-object swap cache with persisted snapshots | `common/src/cache/` | |
| SF-108 | Backpressure: virtual-queue in-flight caps, session memory budgets, transfer limits, throttles, quotas | `queue/manager.rs`, `network/memory.rs` | |
| SF-109 | Graceful shutdown with systemd notify, cache-snapshot flush, telemetry shutdown | `main/src/main.rs` | |
| SF-110 | Hot reload of settings/certificates/lookup stores/blocked IPs with cluster broadcast | `cache/reload.rs` | |

## M. Other capabilities (classified for a marketing platform)

| ID | Feature | Evidence | Classification for ReForwarder |
|---|---|---|---|
| SF-111 | CalDAV/CardDAV/WebDAV groupware: calendars (iTIP/iMIP scheduling, free/busy, alarms), address books, WebDAV files, JSCalendar/JSContact | `crates/dav`, `groupware`, `calcard` | Out of scope (mail-server product surface) |
| SF-112 | File storage with JMAP FileNode, text extraction for search (PDF/OOXML/EPUB/RTF) | `crates/jmap/src/file/`, `text-extract` | Out of scope; text-extract ideas marginally relevant to attachments |
| SF-113 | nlp crate = ML spam classifier (see SF-40), language detection, CJK tokenizers | `crates/nlp/` | Concept relevant to bounce/spam text classification |
| SF-114 | Deployment tooling: installer script, multi-target Docker bake, systemd units, AppArmor profile, ops scripts (migrate/repair/ossify) | `install.sh`, `resources/` | Practices worth mirroring, not features |
| SF-115 | Schema-version migration guard at boot; per-release upgrade notes | `main.rs`, `UPGRADING/` | ReForwarder has AutoMigrate + constraints; parity exists |

---

## Coverage checklist (honesty statement)

| Category | Examined fully | Examined partially | Not examined |
|---|---|---|---|
| A. Protocols & delivery | SMTP inbound session/data (all 1294 ln of data.rs), rcpt resolution, outbound delivery (1780 ln), MX/DANE/MTA-STS verify, DSN (grep-verified every fn), local delivery, milter/hook surfaces | outbound client.rs tail, smtp-proto parse branches, spool/manager every-branch | — |
| B. Security & auth | SPF/DKIM/DMARC/ARC/DKIM2/iprev integration points, SASL, ACME module map, spam pipeline + tag inventory, DNSBL | reporting codecs (dmarc/tls aggregation internals), OAuth grant lifecycle | `mail-auth`/`email` crate algorithms (external), enterprise AI adapters (SEL) |
| C. Filtering & policy | Sieve capability enum + runtime actions, stage scripts, expression engine role, ManageSieve command set | sieve compiler internals per-op | sieve bytecode opcode-level details |
| D. Queue admin | QueuedMessage/Action registry mappings, response taxonomy, pause/resume, expiry/DSN | live-delivery SSE stage listing, trace store write path | — |
| E. Domains | Domain/DkimSignature objects, DNS publication types + provider list, rotation task entry points, TroubleshootDmarc | per-provider updater implementations (1000+ ln), alias-resolution full path | — |
| F. Identity | Account/Credential/Role/ApiKey models, permission catalog scope, TOTP, SCIM entry points, tenants | OAuth grant internals, permission-resolution mechanics | — |
| G. Storage | Store/blob/search/lookup planes, backends file map, blob codec, quotas, retention/purge, backup/restore signatures | individual backend read/write paths, elastic/meili modules | FDB internals |
| H. APIs | JMAP capability set, IMAP capability list verbatim, admin REST (13 paths), push stack, webhooks/MTA hooks, lookup stores | per-operation JMAP/IMAP handlers | dav-proto XML parsing |
| I. Observability | trc event/metric catalogs, exporters, tracers, healthz, alerts | metric aggregation internals | — |
| J. Admin | Confirmed external UI + schema-serving + Action/task/registry models | — | resources/schema JSON contents (binary-gz) |
| K. Extensibility | Sieve plugin registry, expression engine role, task list | — | — |
| L. Reliability | Coordinator event list, store failover model, caching, shutdown, hot reload, backup | per-backend pubsub internals | — |
| M. Other | Groupware/file/nlp/text-extract/reports/deployment module inventories | iTIP state machine, PDF/OOXML extractors | — |
| Tests | **N/A** — all tests live in the external `stalwart-tests/` repository, absent from this clone | | |

**Explicit non-features (verified, to avoid false claims):** ARC sealing; SCRAM/GSSAPI/EXTERNAL SASL; ETRN/ATRN/BURL; RRVS/CONPERM enforcement (parser-only); SMTP-side quarantine store (Junk filing instead); per-domain queue suspension; domain ownership TXT-verification flow; admin audit log; config rollback; WebAuthn/passkeys; APNs/FCM push; per-domain quotas; rspamd/spamassassin proxy clients.
