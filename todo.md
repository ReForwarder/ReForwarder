# ReForwarder — Master Todo

> Generated **2026-11-06** from the `docs/product-roadmap/` suite (research dated **2026-11-05**) plus the **Option B** decision in `upcomming-convo.md`. This file is the working checklist; where a sibling doc disagrees with it, **the sibling governs** (README §11 governance). Registry IDs, GAP-01…53, D1–D9 and R1–R16 are permanent — never renumber.

**How to use this file**
- **Track A** = Option B mail-infrastructure programme (the chosen product direction) — architecture-first, Phases 0–6.
- **Track B** = the 52-item relay roadmap (P0–P3) kept as its own track; items marked `→ OB Phase n` fold into Track A phases when their phase arrives.
- Before starting **any** item, re-verify its current state at HEAD — the audit statuses are historical (see §0).

## Sources

| File | Role |
| --- | --- |
| `docs/product-roadmap/README.md` | Index & executive overview; count governance |
| `docs/product-roadmap/01-oss-feature-inventory.md` | 199-feature registry (permanent IDs), 12 project profiles |
| `docs/product-roadmap/02-competitor-comparison.md` | Capability matrices; architectural references (Postal, BillionMail, Listmonk, useSend) |
| `docs/product-roadmap/03-existing-feature-audit.md` | Current-state audit at baseline `a6f1681` → `6d2d7c0` |
| `docs/product-roadmap/04-feature-gap-analysis.md` | 53 gaps, D1–D9 duplicates, NV register, exclusions |
| `docs/product-roadmap/05-implementation-roadmap.md` | **Track B source** — 52 remaining items, phases, scores, sequence |
| `docs/product-roadmap/06-architecture-impact.md` | AS-IS/TO-BE architecture, risks R1–R16, diagrams |
| `docs/product-roadmap/upcomming-convo.md` | **Track A source** — Option B decision (note: filename misspelled on disk) |

## Status legend

- `[ ]` open · `[x]` done (with commit)
- `[P0–P3]` relay-roadmap priority · size `S/M/L/XL` · score in parentheses
- `◆ residual` — core landed in `6d2d7c0`, only the remainder is listed
- `⚠ verify` — possibly (partially) closed after the audit baseline; reconcile at HEAD before starting
- `→ OB Phase n` — folds into Option B Phase *n*

---

## 0. Context & reconciliation (read first)

### 0.1 Where the baseline stands

- Audit suite frozen at research baseline `a6f1681` (2026-11-05); `6d2d7c0` landed during research (provider-event ingestion, A/B winner, timezone sending, webhook replay — 36 new tests) and is the current audited HEAD-of-record.
- Registry v1.0.0: **199 features** → 109 IMPLEMENTED / 2 NV / 46 PARTIAL / 38 MISSING / 4 N/A (after SEC-016 reconciliation).
- 53 gaps (P0×5, P1×10, P2×22, P3×16) → **52 items remain** (GAP-39 closed outright; GAP-02/07/18/28 reduced to residuals). Sizes: S×15, M×32, L×4, XL×1 (GAP-04).
- Option B (in `upcomming-convo.md`) **supersedes** the relay-only exclusions for direct-to-MX, IP pools, PTR guidance, bounce-mailbox/return-path and full mailboxes/IMAP/webmail — they become Track A candidates, not dead items (see §Not-recommended for what stays dead).

### 0.2 Post-audit commit `7f445f2` — reconcile these before starting

| Landed | Tracker | Affects |
| --- | --- | --- |
| Atomic send/campaign claims: CAS transitions (`queued→processing`, `failed→queued`), TaskID dedup, `FOR UPDATE SKIP LOCKED` batch claims + concurrency test | R-26 | **GAP-01** (partially — cancel + reconcile coverage still to verify) |
| TOTP recovery codes: bcrypt-hashed single-use, atomic consume, regeneration gated on current TOTP | R-10 | **GAP-12** (enforcement landed 2026-11-07 — Wave P3-4; RF-F005 full completion remains the open half) |
| `pg_dump` backup CronJob (gzip + retention) + restore runbook (`POSTA_BACKUP_ENABLED`) | R-11 | **GAP-50** (⚠ verify acceptance criteria, likely closeable) |
| Domain deliverability health endpoint (GET + diagnose): SPF/DKIM/DMARC status, DKIM key state, per-domain bounce/complaint rates, live DNS re-verification persisted | R-07 | **GAP-08** (⚠ large chunk landed; verify remaining depth) |
| `require_domain_verification` platform setting actually enforced | R-23 | **GAP-10** (partial progress) |
| CR/LF/NUL header-injection hardening; SMTP relay credentials → bcrypt + rehash-on-success | R-24, R-25 | (new hardening, no GAP) |

### 0.3 In-flight (uncommitted working tree)

- New `internal/handlers/api_key_usage_handler.go` + `internal/storage/repositories/api_key_usage_repo.go`, edits to api-key handler/model/routes/middleware/migration, `retry/worker.go` + `internal/worker/retry_bound_test.go` → **API-key usage tracking + retry bounding — likely GAP-44 groundwork**. Do not discard; reconcile when committed.

---

## 1. Target product definition (Option B)

> Source: `upcomming-convo.md` §2. **ReForwarder — self-hosted email infrastructure and communication platform.**

| Pillar | Scope |
| --- | --- |
| **Outbound email** | SMTP, direct-to-MX, queues, delivery and IP pools |
| **Inbound email** | MX reception, routing, filtering and storage |
| **Hosted mailboxes** | Mailbox provisioning, IMAP, POP and webmail |
| **Email marketing** | Contacts, campaigns, automation and analytics |
| **Developer platform** | REST API, SMTP credentials, SDKs and webhooks |
| **Infrastructure security** | Abuse controls, reputation, DNS and TLS |

Two distinct use cases, **without mixing their policies**:

1. **Email service provider** — applications/businesses send transactional and marketing email through API or SMTP.
2. **Hosted email provider** — organisations create domains, mailboxes and users, then send and receive ordinary mailbox email.

They share domain management, authentication, delivery infrastructure and administration — but need **separate sending policies, storage rules, quotas and abuse controls**.

---

## 2. Target architecture — control plane / data plane

> Source: `upcomming-convo.md` §3. Preserve the Go modular monolith + Vue dashboard as the control plane; add dedicated mail-data-plane components for protocols and message transport. **Not** thirteen microservices — logical ownership boundaries.

```mermaid
flowchart TB
    CP["ReForwarder Control Plane<br/>(existing Go app + Vue dashboard)<br/>Tenants · Users · Domains · Mailboxes · Policies<br/>API · Billing/quotas · Monitoring"]
    OB["Outbound data plane<br/>SMTP submission · MTA · delivery queues<br/>IP pools · DKIM"]
    IB["Inbound data plane<br/>MX reception · SMTP filtering<br/>routing · mailbox delivery"]
    MB["Mailbox services<br/>Mailbox storage · IMAP · POP3<br/>optional JMAP · webmail"]
    SEC["Security services<br/>Spam filtering · malware scanning<br/>abuse detection"]
    SH["Shared infrastructure<br/>PostgreSQL · Redis · blob/message storage<br/>event bus · metrics"]
    CP <--> OB
    CP <--> IB
    CP <--> MB
    CP <--> SEC
    OB --> SH
    IB --> SH
    MB --> SH
    SEC --> SH
```

**Boundary rules**

- Control plane owns tenant configuration, identities, domains, mailbox metadata, credentials, policies, API and orchestration.
- Dedicated protocol services own SMTP transport, inbound reception, mailbox access and message transfer.
- PostgreSQL remains the **control-plane** system of record — not every message body belongs in the relational schema.
- Explicit service interfaces + durable events between components; protocol services never depend on private Go app internals.
- Tenant identity/ownership consistent across **all** data-plane services.
- Protocol services independently restartable and observable without taking down the dashboard/API.
- Reuse mature protocol software where safer than building from scratch; ReForwarder owns integration, configuration, lifecycle and operator experience.

### 2.1 New domain boundaries (13)

| Domain | Core responsibilities |
| --- | --- |
| `mailserver` | Mail-server lifecycle, server configuration and orchestration |
| `mta` | Outbound transport adapter, delivery status and queue integration |
| `inbound` | Inbound SMTP acceptance, validation and routing |
| `mailbox` | Mailbox, folder, quota and message metadata |
| `mailstore` | Message body and MIME storage abstraction |
| `imap` | IMAP service integration and mailbox synchronization |
| `pop3` | POP3 service integration |
| `mailrouting` | Local delivery, aliases, forwarding and routing rules |
| `reputation` | IP reputation, warm-up, throttling and abuse signals |
| `dnsmanager` | Domain verification, MX, SPF, DKIM, DMARC and PTR guidance |
| `spamfilter` | Spam scoring, quarantine and filtering integration |
| `malware` | Attachment scanning and scanning policy |
| `webmail` | User-facing mailbox application integration |

**Data ownership:** control plane = tenant/domain/mailbox config, credential metadata, policy · MTA = transport queue, remote attempts, transport status · mailbox store = message content, membership, folder state, IMAP sync metadata · filtering = verdicts, quarantine decisions, scan records · event layer = durable lifecycle events for analytics/webhooks/dashboard. Every cross-component operation has an explicit correlation identity.

### 2.2 Relay-plane target (current programme, unchanged shape)

> Source: `06-architecture-impact.md` §4.1 — what Track B builds inside the existing monolith before/alongside Track A.

```mermaid
flowchart TD
    subgraph mono["Modular monolith (unchanged shape)"]
        API["HTTP API + middleware chain"] --> ENG["Service engines<br>delivery, audience, campaign, template,<br>automation, webhook, security"]
        ENG --> DB[("Postgres (system of record)")]
        ENG --> RD[("Redis")]
        CRON["Cron fleet (10 jobs)"] --> ENG
        CRON -.->|"NEW: Redis SET NX PX leader lease (GAP-03)"| RD
    end
    subgraph send["Sending plane"]
        SEL["NEW: server selection = ordered candidates<br>+ health-aware rotation (GAP-04)"]
        SMTP["SMTPSender relay mode (AS-IS)"]
        ESP["NEW: ESP API drivers SES/Postmark/Mailgun<br>behind Sender interface (GAP-04)"]
        SEL --> SMTP
        SEL --> ESP
    end
    subgraph signals["Delivery-signal plane (NEW, GAP-02)"]
        INW["/webhooks/bounce + per-ESP adapters<br>(SES SNS, Postmark, Mailgun, SendGrid)"]
        PE["provider_events ingestion<br>(commit in-flight code first)"]
        EE["email_events: delivered + complained<br>become real events"]
        SUP["suppression + subscriber-status parity<br>on the webhook path"]
    end
    subgraph obs["Observability"]
        PROMX["Prometheus families + NEW<br>provider-event, queue-latency, error metrics"]
        SENTRY["NEW: optional error-monitoring hook (GAP-51)"]
    end
    ENG --> SEL
    INW --> PE
    PE --> EE
    PE --> SUP
    EE --> ANA["Analytics engine<br>accept vs delivered distinguished"]
    ENG --> ANA
    EE --> WHX["Webhook engine + NEW replay/test-fire (GAP-18)"]
    WHX --> CWH(("customer endpoints"))
    ANA --> PROMX
    ENG --> SENTRY
```

---

# TRACK A — Option B: Complete Email Infrastructure (Phases 0–6)

> Source: `upcomming-convo.md` §7 + master prompt §12. Architecture-first: **do not start by implementing IMAP or building a webmail UI.** The original relay roadmap's P0 trust/correctness work (Track B Phase 0) is retained, not discarded.

## Phase 0 — Repository re-audit and architecture decisions (must happen first)

> **DONE 2026-11-07** (Option B programme authorized by the user; ADRs Accepted). ADRs live flat at `docs/architecture/ADR-001-outbound-mta.md` … `ADR-009-deployment-topology.md` (no `ADRs/` subfolder — noted deviation from the original path sketch). Repository re-audit evidence: `docs/audits/REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md` (115-record ledger, verification waves in flight) + `docs/audits/REFERENCE-PLATFORM_SOURCE_REGISTER.md`.

- [x] Inspect current branch, HEAD, working tree; reconcile the 7 audit docs with actual code (evidence-backed current-state matrix; include `7f445f2` + in-flight work per §0) — pre-flight `482d10b` recorded; reconciliation live in the parity register
- [x] Verify existing SMTP relay, inbound receiver, queue, DKIM, provider-event ingestion, webhook behavior at HEAD — 2026-11-07 acceptance audit (commit `27c418a`) + verification waves V-B1/V-B2 at `482d10b`
- [x] Record current migrations, message models, event contracts, tenant boundaries — `docs/architecture/DATA-OWNERSHIP.md` + source register
- [x] Identify what is reusable vs must be isolated — `docs/architecture/COMPONENT-MATRIX.md` §2
- [x] ADR-001 — Outbound MTA (direct-to-MX + upstream relay modes, transport queues, retries, rate limits, IP pools, DKIM, TLS, deferrals, delivery events, ops admin)
- [x] ADR-002 — Inbound SMTP (acceptance, recipient validation, domain routing, aliases, forwarding, catch-all, spam/malware, quarantine, durable local delivery)
- [x] ADR-003 — Mailbox storage (message identity, MIME storage, folders, flags, quotas, concurrent access, expunge, search, attachments, retention, backup — **not** the transactional `Email` model)
- [x] ADR-004 — IMAP and POP3 (implementations, auth, TLS, connection limits, sync, client compatibility)
- [x] ADR-005 — Webmail (frontend boundaries, auth integration, compose/send, attachments, folders, search, sessions)
- [x] ADR-006 — Spam and malware (content-filtering boundary, quarantine, abuse detection — **distinct from the existing network security engine**)
- [x] ADR-007 — DNS and deliverability (MX, SPF, DKIM, DMARC, PTR, FCrDNS, TLS certificates, verification, warm-up, reputation)
- [x] ADR-008 — Data and event ownership (source of truth per entity/store)
- [x] ADR-009 — Deployment topology (single-node + future multi-node, health, restarts, upgrades, backups, DR)
- [x] Create `docs/architecture/OPTION-B-ARCHITECTURE.md` — target architecture and system boundaries
- [x] Create the nine ADR files (see path note above)
- [x] Create `docs/architecture/COMPONENT-MATRIX.md` — candidates, licenses, maturity, maintenance, integration costs, trade-offs
- [x] Create `docs/architecture/DATA-OWNERSHIP.md` — source of truth for every entity and store
- [x] Create `docs/architecture/MESSAGE-LIFECYCLE.md` — outbound, inbound, bounce, quarantine, mailbox-delivery flows (14 designs below)
- [x] Create `docs/architecture/MIGRATION-PLAN.md` — compatibility, migrations, rollback
- [x] Create `docs/architecture/IMPLEMENTATION-ROADMAP.md` — dependency-aware tickets + acceptance criteria
- [x] Create `docs/architecture/RISK-REGISTER.md` — reliability, security, deliverability, operational, licensing risks

**Exit criteria:** approved architecture, component-selection criteria, protocol boundaries, data ownership, migration plan. **No large feature implementation yet.**
**Architecture-only rules:** no feature implementation, no production-code changes, no schema changes, no new dependencies, no destructive commands; cite exact files/symbols for current-state claims; label facts vs recommendations vs assumptions.

## Phase 1 — Outbound MTA foundation

Target flow (source: `upcomming-convo.md` §6 / screenshots):

```mermaid
flowchart TD
    A["REST API / SMTP Submission / Campaign"] --> B["ReForwarder validation and policy"]
    B --> C["Tenant, domain, quota and suppression checks"]
    C --> D["Durable outbound message record"]
    D --> E["MTA adapter"]
    E --> F["Outbound MTA queue"]
    F --> G["IP pool and routing policy"]
    G --> H["Remote MX via SMTP"]
    H --> I["Delivery / deferral / bounce"]
    I --> J["Transport event ingestion"]
    J --> K["ReForwarder event pipeline"]
    K --> L["Analytics, webhooks and suppression"]
```

Current AS-IS pipeline for comparison (source: `06-architecture-impact.md` §2.3):

```mermaid
flowchart TD
    A["POST /emails/send<br>(auth_routes.go:194)"] --> B["email_handler.go:130"]
    B --> C["email.Service.Send gates<br>validation, maintenance, plan quota,<br>domain check, suppression, tracking rewrite"]
    C -->|"send_at future"| D["status=scheduled<br>EnqueueEmailSendAt (asynq ProcessIn,<br>producer.go:108-133)"]
    C -->|"immediate"| E["status=queued<br>deterministic TaskID email:send:id<br>(producer.go:47-101, a6f1681)"]
    D --> Q(("asynq / Redis"))
    E --> Q
    Q --> F["worker ProcessTask<br>(worker/handler.go:151)"]
    F --> G["pause/cancel re-check, server selection<br>(handler.go:197-250), suppression re-check,<br>DKIM sign, X-Posta stamping"]
    G --> H["smtp_sender.Send<br>dial timeout 15s + session budget,<br>SSL/STARTTLS/plain (a6f1681)"]
    H -->|"2xx"| I["status=sent + email.sent<br>+ webhook + campaign ledger"]
    H -->|"5xx at RCPT"| J["handlePermanentRejection:<br>failed + hard bounce + auto-suppress<br>(handler.go:442-510)"]
    H -->|"transient 4xx/timeout"| K["Layer 1: asynq retry"]
    K -->|"retries exhausted"| L["ExhaustedErrorHandler:<br>failed + email.failed<br>(handler.go:529-565)"]
    K -->|"lost task"| M["queue_reconcile cron re-enqueues<br>queued rows (a6f1681)<br>- scans queued only, :93"]
    K -->|"stuck in DB"| N["Layer 2: retry.Worker DB poll<br>(retry/worker.go:110-155)"]
    O["Layer 3: manual retry<br>POST /emails/{id}/retry"] --> F
```

- [ ] MTA adapter and lifecycle manager
- [ ] Direct-to-MX delivery and upstream-relay modes
- [ ] Outbound queue visibility, retry, hold and release
- [ ] Delivery attempt records + durable transport events
- [ ] IP binding, per-domain sending policy, rate limits
- [ ] TLS, DKIM signing, SPF/DMARC alignment checks, PTR guidance
- [ ] Test harnesses: SMTP responses, transient failures, permanent failures, remote throttling
- [ ] Flow requirements: stable message identifier across systems · distinguish API acceptance / MTA queue acceptance / remote SMTP acceptance / final delivery evidence · idempotency + duplicate protection + durable retry · per-domain and per-IP rate limits · held queues + manual release · campaign traffic must not starve transactional traffic · record the MTA's **actual** response (queue acceptance ≠ delivery)

**Exit criteria:** controlled outbound delivery with traceable, recoverable message state. No production direct-to-MX launch until operational readiness gates pass.
**Folds in from Track B:** GAP-04 (Sender seam), GAP-05 (idempotency), GAP-48 (queue ops), GAP-01 (durable record).

## Phase 2 — Inbound mail and routing

Target flow (source: `upcomming-convo.md` §6 / screenshots):

```mermaid
flowchart TD
    S["Remote sender"] --> MX["Public MX / Inbound SMTP"]
    MX --> V["SMTP validation and recipient lookup"]
    V --> SC["Spam and malware scanning"]
    SC --> P["Recipient policy"]
    P -->|"Local mailbox"| DL["Mailbox delivery"]
    P -->|"Alias / forward"| RT["Routing engine"]
    P -->|"Inbound webhook"| WH["Webhook / application route"]
    P -->|"Rejected"| RJ["SMTP rejection / quarantine"]
    DL --> ST["Mailbox message store"]
    ST --> IM["IMAP / POP3"]
    ST --> WM["Webmail"]
    ST --> EV["Mailbox events"]
```

- [ ] Inbound SMTP service integration
- [ ] Recipient and domain validation
- [ ] Aliases, forwarding, routing rules, catch-all policy
- [ ] Spam and malware filtering interfaces
- [ ] Bounce and return-path processing
- [ ] Quarantine and manual review
- [ ] Inbound event and audit trails
- [ ] Flow requirements: validate recipients **before** accepting responsibility · handle unknown users/aliases/catch-all/forwarding explicitly · inbound delivery idempotent · never accept then silently lose a message · quarantine + operator review · per-mailbox and per-domain quotas · forwarding loop + abuse protection · mailbox delivery kept separate from marketing subscriber state

**Exit criteria:** accepted inbound messages durably routed, rejected or quarantined with observable outcomes.
**Folds in from Track B:** GAP-02 residual (bounce/return-path), GAP-47 (antivirus hook).

## Phase 3 — Mailbox and storage foundation

> Progress 2026-11-07 (OB-3 wave W1, RF-B120): storage core landed — Postgres mailstore models + additive migration (incl. the `search_tsv` generated column + GIN), the `internal/services/mailstore` service (mailbox/folder CRUD, Append with atomic per-folder UID claim, Copy/Move, flag CAS on mod_seq, Expunge, Fetch/List), the `mail_blob_refs` content-address ref-count seam (GAP-45 storage half), and concurrency suites (UID monotonicity + flag CAS against real Postgres). Remaining: retention sweep + blob GC, real compress/encrypt BlobCodec, FTS Searcher, quota ceilings, backup/restore/migration tooling — see `docs/project/BACKLOG.md` RF-B120 + `docs/project/STATUS_HISTORY.md`.

> Progress 2026-11-07 (OB-3 wave W4, RF-B120): quotas + retention landed — `DefaultQuotaEnforcer` now enforces the full ADR-003 §5 policy (per-mailbox byte quota + new `mailboxes.quota_messages` count quota + workspace aggregate ceiling via `workspaces.mail_quota_bytes`, decided under a mailbox-row `FOR UPDATE` lock inside the append/copy transaction, typed `*QuotaError` matching `ErrQuotaExceeded` for the ingestion seam); per-mailbox/per-folder retention policies (`retention_days`, `retention_max_count` — a folder value overrides the mailbox value; age read from the server-side ingest timestamp) executed by the unified retention cron via `mailstore.RetentionSweeper` (family wired in `cmd/posta/server.go` initCronManager), which purges only in-policy messages mailbox-by-mailbox inside single transactions, keeps `used_bytes`/`message_count` in sync, and reclaims `mail_blob_refs` rows at ref_count ≤ 0 together with their stored objects (GAP-45 ref-count half — orphan-object enumeration remains open). Size-based retention is a documented residual. Remaining for RF-B120: backup/restore/migration tooling; dashboard quota-usage surfacing lands with OB-4/OB-5.

> Progress 2026-11-07 (OB-3 wave W3, RF-B120): Postgres FTS search landed — `mailstore.PostgresSearcher` (`internal/services/mailstore/search.go`) implements the `Searcher` W1 defined: websearch-syntax queries (`websearch_to_tsquery('english', ?)`, bound parameter, never concatenated) over the W1 `mail_messages.search_tsv` generated column (A=subject, B=from+to+cc, C=body weights), per-user isolation (mailbox+user predicates in the statement; a foreign mailbox yields an empty page, never another user's rows), optional folder narrowing, and relevance-ranked paged hits with the true total (`ts_rank` ordering, `COUNT(*) OVER ()` plus a count fallback past the end, page sizes defaulted/clamped, 512-rune query guard with typed `ErrSearchQueryTooLong`). Index maintenance: `migration.RebuildMailstoreSearch` (`internal/storage/migration/mailstore_fts_reindex.go`) — idempotent, single-transaction raw-SQL drop+re-add of the canonical generated column + GIN rebuild — exposed as the admin-gated, audited `POST /admin/mailstore/search/reindex` (registered in `internal/routes/admin_routes.go`, handler `internal/handlers/mailstore_search_admin_handler.go`; the only new HTTP surface this phase, golden route file regenerated accordingly). Tests named and run against real Postgres via the `TEST_DATABASE_DSN` gate: `TestPGSearchHitsSubjectFromAndBody`, `TestPGSearchIsolationBetweenUsers`, `TestPGSearchPaginationAndTotals`, `TestPGSearchRankingPrefersSubjectOverBody`, `TestPGSearchFolderScoping`, `TestPGRebuildSearchIndexIdempotent` (+ no-DB guard units in `search_test.go`); the fake driver cannot express tsvector, so the gate skips cleanly rather than faking. User-facing search HTTP surfaces are intentionally OB-4 (IMAP SEARCH) / OB-5 (webmail).

> Progress 2026-11-07 (OB-3 wave W2, RF-B120): the content-addressed encrypted blob layer (GAP-45 storage half) landed — (1) the real `BlobCodec` W1 stubbed is now `mailstore.ZstdBlobCodec` (`internal/services/mailstore/codec_zstd.go`): zstd compression (`github.com/klauspost/compress/zstd`, new direct require in `go.mod`) applied only above the 4 KiB threshold and only when it actually shrinks the payload (ADR-003 decision 4, SF-83), AES-256-GCM through `internal/services/crypto` via new `EncryptBytes`/`DecryptBytes` (the credential-encryption pattern extended to binary stored content; no key → refuse, SF-78), self-describing frame (version byte + flags byte + nonce‖ciphertext), SHA-256-of-plaintext content addressing so identical content dedups regardless of per-write GCM nonces, every tamper path (`codec_zstd_test.go`: bit flips, truncation, unknown version/flags, wrong key, disabled key) collapsing to `ErrBlobCorrupt`. (2) `mail_blob_refs` gained the additive `last_referenced` grace anchor (`internal/models/mailbox.go`; stamped on reserve, increment and release) and Append was restructured to **reserve-then-write**: one transaction takes a per-content-address advisory lock (`pg_advisory_xact_lock`, namespaced, parameterized), upserts the ref returning the post-increment count, and Puts the blob inside that same lock (dedup: count 1 → must Put; count > 1 → probe-read the object, skip only when readable — self-healing crashed reservations), so `ref_count ≥ 1` is durable before any message row exists. (3) The retention wave's `RetentionSweeper.reclaimBlobs` now enforces the GAP-45 grace period (dead refs only after `COALESCE(last_referenced, created_at)` ages past the window, default 24 h, `ReclaimGrace` overridable), re-checks `ref_count <= 0` inside the same per-hash lock (total order between reservation and reclamation — a live reference can never be collected), rolls the row delete back when the object delete fails (retried next sweep, never an untracked orphan), and reports reclaimed bytes; the unified cron (`internal/cron/jobs/retention.go` `auditMailBlobGC`, reporter wired additively in `cmd/posta/server.go` next to the W4 family) writes the audited `mail_blob_gc` summary event including per-mailbox blob-byte usage. (4) GAP-45 usage reporting: `mailstore.MailboxUsage`/`MailboxBlobUsageAll` (`usage.go`) — dedup-aware per-mailbox blob bytes summed from `mail_blob_refs` over the distinct hashes the mailbox's messages reference (distinct from the dedup-blind `used_bytes` quota basis; documented in both), ownership-scoped like every mailstore call. Battery: the named dockerized `go build ./... && go test -count=1 -race ./internal/...` run passed with 45 packages ok and zero FAIL (run with `TEST_DATABASE_DSN` set against postgres:17-alpine, so the PG-gated suite ran inside it); named W2 tests: `codec_zstd_test.go` (round-trip, threshold, incompressible, content address, corruption, wrong key, disabled key, concurrency), `TestExpungeDecrementsBlobRefs`, `TestMailboxUsageBlobBytesFromBlobRefs`, `TestMailboxUsageOwnershipScoped` (fake driver) and `TestPGAppendDedupRefCountsTwo`, `TestPGMailBlobGCDropsOnlyAgedOrphans`, `TestPGConcurrentAppendSameContent`, `TestPGMailboxUsageBlobBytesFromBlobRefs`, `TestPGReserveSelfHealsMissingBlob` (real Postgres). Residual: per-workspace data keys (the codec interface carries no workspace context — the deployment-wide `POSTA_ENCRYPTION_KEY` data key is used; see STATUS_HISTORY), orphan-object enumeration for blobs without ref rows, AdminInventory usage surfacing (OB-5 dashboard).

> Progress 2026-11-08 (OB-3 wave W5, RF-B120): the ingestion seam (flow F13) landed — inbound messages whose matched route action is `deliver` file into the recipient's hosted INBOX via `mailstore.Append` (`internal/services/inbound/mailbox_delivery.go`: `MailboxDeliverer` seam + `MailboxDelivery`, executed by the route-action executor on both ingest paths — SMTP `IngestRaw` and webhook `Ingest` — and on quarantine release). Provisioning: the recipient mailbox (named by the full lowercased address, unique (user_id, name), via new `mailstore.FindMailboxByName`) is auto-provisioned for the tenant user on first delivery to a verified domain bound to that user, gated by new config `POSTA_MAILSTORE_AUTO_PROVISION` (default true); off → archive-only exactly as today. Quota-enforced: an append refused with the typed `*mailstore.QuotaError` quarantines the message per ADR-002 §3 with the stable typed reason `mailbox_quota_exceeded` + structured quota fields; release re-runs routing and files the message (never lost — the durable archive copy exists throughout); any other append failure logs and falls back to archive-only. Wiring sits behind blob store + `POSTA_ENCRYPTION_KEY` presence (production `ZstdBlobCodec` refuses unencrypted storage, SF-78) in `cmd/posta/server.go` and `internal/routes/routes.go`; no new HTTP routes; no schema change. Success publishes `inbound.mail_delivered` with the delivered mailbox names. Marketing-subscriber separation (Phase 2 flow requirement) asserted directly: `TestPGMailboxDeliveryLeavesTransactionalEmailAndSubscriberStateUntouched` (delta counts on emails/subscribers/subscriber_lists unchanged). Named tests, all passing: `TestPGMailboxDeliveryRoundTrip`, `TestPGMailboxDeliveryAutoProvisionOff`, `TestPGMailboxDeliveryOverQuotaQuarantinesTyped`, `TestPGMailboxDeliveryLeavesTransactionalEmailAndSubscriberStateUntouched` (real Postgres), `TestDeliverActionWithoutSeamKeepsArchiveOnly`, `TestMailboxDeliveryNilGuards`, `TestDeliverActionUnknownErrorFallsBackToArchive`, `TestDeliverActionOverQuotaQuarantinesWithoutStore` (units). Documented residuals: auto-provisioned quota 0 (workspace ceiling still applies); multi-recipient over-quota holds the record as a whole (release may duplicate into already-served mailboxes); suspended/closed mailboxes → archive-only fallback; per-workspace data keys remain W2's residual — see `docs/project/STATUS_HISTORY.md` W5 entry.

- [ ] Choose and prototype the mailbox storage backend (backend chosen per ADR-003 — Postgres mailstore + blob.Store; IMAP-side prototype pending Phase 4)
- [ ] Mailbox, folder, quota and message metadata
- [x] MIME storage, attachment storage, retention (storage W1; retention sweep + blob-ref reclamation W4 2026-11-07 — per-mailbox/per-folder age+count policies via the unified cron family `mailstore.RetentionSweeper`; size-based policy residual)
- [ ] Append, fetch, copy, move, flags, expunge, search
- [ ] Concurrent-access and synchronization behavior
- [ ] Backup, restore, mailbox migration tooling

**Exit criteria:** mailbox data survives restarts, supports concurrent clients, backs up/restores without corruption. Do **not** reuse the transactional `Email` model; do not auto-migrate transactional records into mailbox storage.

## Phase 4 — IMAP and POP3

> Progress 2026-11-08 (OB-4 wave W1, RF-B121): the protocol-access seam landed in `internal/services/mailstore/` + new `internal/services/mailauth/` — `FetchRange` (RFC 9051 sequence/UID sets incl. `*` semantics; BODY section extraction from the stored raw blob: full body, HEADER(+blank line), HEADER.FIELDS/.NOT with folded continuations, TEXT, RFC part-numbered paths incl. message/rfc822 re-entry, `.MIME` terminals, `<start.count>` partials, non-peek fetches set `\Seen` via the CAS flag store, per-request batch cap) plus the `BuildMimeStructure` tree for BODY/BODYSTRUCTURE rendering; `SearchIMAP` (IMAP SEARCH keys → parameterized SQL predicates for pure conjunctions + the Postgres FTS `Searcher` for BODY/TEXT; bounded raw-header scans for BCC/HEADER; unwired FTS → typed error, never silent no-match); `StatusCounts` (messages/unseen/recent, one `count(*) FILTER` round-trip); `ConcurrentSessionGuard` (per-folder write locking wired into Expunge; in-process by design, cross-replica ordering on the DB); `ChangeNotifier` (per-folder IDLE pub/sub + Redis fan-out on `posta:mailchg` per the ban-propagation pattern; best-effort emits from Append/Expunge/flags/Move/Copy after commit); `mailauth.AuthenticateWithAppPassword` (bcrypt vs app-password digests only — revoked/missing/wrong collapse to one generic failure, primary password structurally never accepted, last_used stamped); `MailSession` (SELECT/EXAMINE snapshot + read-only enforcement seam). ~45 named tests incl. `TestConcurrentAppendAndExpungeRace` under `-race` and real-Postgres PG-gated siblings (`w1_pg_test.go`); named battery green; frontend type-check + vitest 82/82 green. The IMAP/POP3 server packages, listener/TLS/limit/audit wiring, UIDVALIDITY-driven sync and the compatibility matrix remain — SF-85/SF-86 stay PLANNED until those land. See `docs/project/STATUS_HISTORY.md` OB-4 W1 entry for limitations.

> Progress 2026-11-08 (OB-4 waves W2/W3/W4, RF-B121): the protocol servers and their ops wiring landed. **W2 — IMAP server** (`internal/services/imap/`): the ADR-004 command subset (CAPABILITY/NOOP/LOGOUT/AUTHENTICATE PLAIN+LOGIN/LOGIN/STARTTLS/SELECT/EXAMINE/CREATE/DELETE/RENAME/SUBSCRIBE/UNSUBSCRIBE/LIST/STATUS/APPEND/CHECK/CLOSE/EXPUNGE/SEARCH/FETCH with BODY[] sections + partials/STORE ±FLAGS with mod_seq CAS/COPY/MOVE/UID variants/IDLE) over the W1 seam, app-password-only auth, per-IP conn budget + per-user session cap + per-session command budget, audit sink (`audit.go` `NewEventAuditLogger` → `imap.auth.success|failure`, `imap.select|append|store|expunge|fetch|search|copy|move` …). **W3 — POP3 server** (`internal/services/pop3/`): the classic command set with STLS, session-scoped deletion (mark → expunge on QUIT, IMAP UIDs untouched), its own audit logger (`pop3.auth.success|failure`, `pop3.session.end`), limits from the same config keys, and its protocol test matrix (`pop3_test.go`: login matrix, auth-requires-TLS, connection+command limits, audit events, implicit TLS). **W4 — ops wiring (this wave's scope)**: config keys + validation (`internal/config`: `MailProtocolConfig` for IMAP+POP3 — enabled/host/ports/TLS cert files/max-sessions-per-user/max-conns-per-IP/command rate limit+window; `validateMailProtocols` enforces the ADR-004 posture — TLS on any non-loopback bind, implicit-TLS ports require certificates, mandatory limits ≥ 1, port-collision matrix vs each other + the dashboard/SMTP listeners), gated listener wiring for ALL FOUR ports with graceful shutdown registration (`cmd/posta/server.go`: `startIMAPServer` 143 CountingListener + 993 CountingListener→TLSServerListener, `startPOP3Server` 110/995 same stack, both consuming `cfg.IMAP`/`cfg.POP3` and registered in `shutdownServer`), limiter integration on the security-engine substrate (SF-44: CountingListener ban-drop + connect/abort signals, TLSServerListener TLS-probing signal, `security.SessionGuard` auth-failure + malformed feeds in both servers), audit events into the events substrate (SF-74 continuation: AUTH success/failure + SELECT/EXPUNGE/APPEND/STORE with user + IP), `compose.override.yml` commented default-off block, operator page `docs/docs/security/mail-protocols.md` (ports/TLS/app-password setup/client settings). W4 tests: config validation matrix (`internal/config/mail_protocols_test.go` — TestMailProtocolsDisabledByDefault, TestMailProtocolsValidationMatrix 19 cases, TestMailProtocolEnvParsing, TestMailProtocolsLoopbackHostVariants), IMAP audit-event emission (`internal/services/imap/audit_test.go` — TestIMAPEventAuditLoggerEmitsAuditEvents, TestIMAPEventAuditLoggerNilBusYieldsNil, TestIMAPServerAuthFailureEmitsAuditEvent), IMAP limiter + listener lifecycle (`internal/services/imap/lifecycle_test.go` — TestIMAPServerGreetingAndLogout, TestIMAPServerCloseStopsAcceptAndDropsSessions, TestIMAPServerImplicitTLSListenerServesGreeting, TestIMAPServerPerIPConnectionCap, TestIMAPServerPerUserSessionCap, TestIMAPServerCommandRateLimit) — POP3 equivalents pre-existed (`TestPOP3ConnectionAndCommandLimits`, `TestPOP3AuditEvents`, `TestPOP3ImplicitTLS`, `TestPOP3AuthRequiresTLS`). **Cross-wave repairs (W4, orchestrator-authorized, recorded per AGENTS.md honesty rules):** `internal/services/imap/session.go` — `st.out` was never initialized (every session panicked with a nil-writer dereference on the greeting; re-pointed in `resetWire` which both newSession and the STARTTLS swap route through) and `session.close` had a data race between `Server.Close` (shutdown) and the session goroutine's run-defer (now `closeOnce`-idempotent); `internal/services/imap/commands.go:998` carried an invalid Go ternary that broke `go build ./...` — W2 self-repaired it mid-flight. SF-85/SF-86 move PLANNED → IN PROGRESS (servers + wiring + tests landed; folder/UIDVALIDITY-driven sync and the standard-client compatibility matrix remain open items). See `docs/project/STATUS_HISTORY.md` OB-4 W4 entry.
> Progress 2026-11-08 (OB-4 wave W2 evidence addendum, RF-B121): the IMAP server's scripted-client command matrix landed (`internal/services/imap/harness_test.go` + `server_test.go` + `parser_test.go`) — the R-22-shape harness (in-process TCP, real mailstore over real Postgres via the TEST_DATABASE_DSN gate, real bcrypt app-password fixtures) covering: session lifecycle/capability gating, unauthenticated command refusal, app-password-only auth with the primary password refused, AUTHENTICATE PLAIN/LOGIN flows, STARTTLS enforcement + upgrade, SELECT/EXAMINE semantics, APPEND literals + FETCH BODY[]/PEEK/sections/partials byte-exactness, STORE ±FLAGS/.SILENT with mod_seq-CAS conflicts as tagged NO, COPY/MOVE UIDVALIDITY behavior, the ADR-004 SEARCH key set (incl. FTS BODY/TEXT and BADCHARSET), EXPUNGE/CLOSE semantics, IDLE notification delivery, APPEND over-quota `NO [OVERQUOTA]`, per-user session cap, command-rate budget, literal-cap rejection. Checks: package suite under `-race` with the DSN → ok 33.326s; the named battery verbatim → exit 0; the battery re-run WITH TEST_DATABASE_DSN (postgres:17-alpine) → exit 0 with imap `ok 28.384s` (the harness runs against real Postgres inside the battery). The harness drove eight real server fixes (Server.Close registry deadlock; RFC 9051 §7.5.1 newest-first EXPUNGE; STORE .SILENT suffix; FLAGS-replace keeping server-managed \Recent; UID FETCH auto-UID; tagged NO [TOOBIG] literal recovery; BADCHARSET on the parse path; BODY field-list case preservation) — full detail in the STATUS_HISTORY W2 note. Real-client interop + UIDVALIDITY-driven sync remain W5.

- [x] IMAP integration against the selected mailbox backend
- [x] Authentication/authorization tied to ReForwarder identities
- [ ] Folder and message synchronization
- [x] POP3 as a separate access mode with correct deletion semantics
- [ ] Standard-client + protocol compatibility testing
- [x] Connection limits, TLS enforcement, rate limits, audit logs

**Exit criteria:** standard mail clients securely use hosted mailboxes with consistent state.

## Phase 5 — Webmail and mailbox administration

- [x] Mailbox provisioning and lifecycle management (2026-11-08, OB-5 wave W2 — the admin control-plane slice: `/admin/mailboxes` provision/quota/suspend/reactivate/cascade-delete + usage, audited `admin.mailbox.*`; user-facing webmail surfaces are the other ADR-005 waves)
- [ ] Inbox, folders, compose, reply, forwarding, attachments
- [ ] Search, read/unread state, mailbox settings
- [ ] Password changes, sessions, device management
- [ ] Mailbox quotas and usage reporting (2026-11-08, OB-5 wave W2: admin-side quota management + usage reporting landed with `/admin/mailboxes` + `GET /admin/mailboxes/{id}/usage`; the self-service quota/usage display is the W1 frontend's scope)
- [x] Admin tools for mailbox suspension and recovery (2026-11-08, OB-5 wave W2: `PATCH /admin/mailboxes/{id}` suspend/reactivate + confirm-gated cascade delete in `web/src/views/admin/Mailboxes.vue`, backed by `internal/services/mailstore/admin.go`)

**Exit criteria:** users manage hosted email without a third-party webmail deployment.

## Phase 6 — Full platform convergence

- [ ] Integrate mailboxes, campaigns, transactional sending, inbound routing into one control plane
- [ ] DNS automation + certificate lifecycle management (progress 2026-11-10, OB-6 wave W2: the certificate-lifecycle half landed — `internal/services/certmanager`, autocert behind `POSTA_ACME_ENABLED` default false, per-domain SNI map on IMAP 993/POP3 995/submission 587 STARTTLS/webmail HTTPS, blob-store cert cache + SF-110 hot reload, HTTP-01 through the existing HTTP server, TLS-ALPN-01 on the webmail HTTPS listener; DNS-01 is the recorded residual via the `CertSolver` seam awaiting the W1 dnsmanager adapter — see SF-34, now PARTIAL, and the OB-6 wave evidence; the DNS-automation half is the other OB-6 waves' scope)
- [ ] IP pools, warm-up controls, reputation monitoring
- [ ] Event, analytics, webhook coverage completion
- [ ] Deployment, upgrade, backup, disaster-recovery tooling
- [ ] Revisit JMAP + advanced integrations on validated demand

**Exit criteria:** one supported self-hosted platform; coherent administration; documented deployment; end-to-end operational testing.
**Folds in from Track B:** GAP-08 (DNS health), GAP-49 (Redis HA), GAP-50 (backups/DR).

### Non-negotiable design rules (all phases)

1. **Never expose an open relay.** Authenticated submission and local inbound delivery are separate trust paths.
2. **Never trust client-supplied tenant ID or recipient domain** as authorization — resolve ownership server-side.
3. **Never report successful delivery** just because a message entered a queue.
4. **Never accept inbound mail** without a durable path to deliver, route, quarantine or reject it.

### Critical operational requirements

| Area | Required protection |
| --- | --- |
| IP reputation | Warm-up, throttling, reputation monitoring, abuse response, blacklist alerts |
| DNS | MX, SPF, DKIM, DMARC, PTR + FCrDNS guidance |
| SMTP security | STARTTLS, auth policy, rate limits, connection limits, relay restrictions |
| Mailbox security | Secure credential storage, session controls, per-mailbox authorization, quotas |
| Spam | Scoring, greylisting policy, quarantine, suppression |
| Malware | Attachment scanning, safe file handling, quarantine |
| Tenant isolation | Workspace isolation across SMTP, inbound routing, mailbox access, storage, events |
| Data durability | Persistence, backup, restore, replication, recovery testing |
| Abuse prevention | Account limits, suspicious-sending detection, compromised-account response, suspension |
| Operations | Queue visibility, delivery tracing, service health, logs, alerts, upgrade safety |

Preserve existing strengths: SSRF guard, security engine, tenant isolation, encrypted credentials, queue reconciliation, webhook attempt history, retention/health tooling. New integrations build on them, never bypass them.

### Message-lifecycle designs to document (→ `MESSAGE-LIFECYCLE.md`)

- [ ] API transactional send · [ ] Authenticated SMTP submission · [ ] Direct-to-MX outbound delivery · [ ] Upstream relay delivery · [ ] Remote delivery deferral and retry · [ ] Hard bounce and soft bounce · [ ] Complaint ingestion and suppression · [ ] Inbound SMTP acceptance · [ ] Inbound mailbox delivery · [ ] Alias and forwarding · [ ] Spam quarantine · [ ] IMAP mailbox access · [ ] POP3 mailbox access · [ ] Mailbox deletion and retention

---

# TRACK B — Relay roadmap: 52 remaining items (P0–P3)

> Source: `05-implementation-roadmap.md` §3–§8. Sequence below; sizes and scores verbatim from that doc.

```mermaid
graph TD
    subgraph P0["Phase 0 — trust & correctness"]
        G11["GAP-11 editor gate (S)"]
        G12["GAP-12 2FA enforce (M)"]
        G03["GAP-03 cron leader (M)"]
        G01["GAP-01 scheduled cancel (M)"]
        G02r["GAP-02 residual parity (M)"]
    end
    subgraph P1["Phase 1 — signals & sender value"]
        G06["GAP-06 auto-pause (M)"]
        G25["GAP-25 DOI (L)"]
        G04["GAP-04 provider seam (XL)"]
    end
    subgraph P2["Phase 2 — parity batches"]
        G08["GAP-08 DNS health (M)"]
        G10["GAP-10 policy hardening (M)"]
        G26["GAP-26 test-send/preview (M)"]
        G34["GAP-34 blocks/slots (L)"]
        G30["GAP-30 signup forms (L)"]
    end
    subgraph P3["Phase 3 — long tail"]
        G35["GAP-35 preference center (M)"]
    end
    P0 --> P1 --> P2 --> P3
    G02r --> G06
    G08 --> G10
    G26 --> G34
    G25 --> G30
    G25 --> G35
    G04 --> G44x["GAP-44 per-key limits (P2)"]
```

## Phase 0 — P0: Trust & correctness hardening (5 items, fully parallel)

Security layers these items plug into (source: `06-architecture-impact.md` §10):

```mermaid
flowchart TD
    REQ["Inbound request / SMTP connection"] --> BAN["1. Security-engine bans<br>TCP accept, EHLO 421, HTTP 403<br>fail-open by design (AS-IS)"]
    BAN --> AUTH["2. Auth: session JWT w/ JTI blacklist,<br>or psk_ API key (SHA-256 hashed,<br>expiry, CIDR allowlist) (AS-IS)"]
    AUTH --> SCOPE["3. RequireScope: send/read/webhooks/write/admin<br>+ tenant-admin paths force admin (AS-IS)"]
    SCOPE --> WS["4. resolveWorkspace: membership or<br>workspace-bound key = tenant boundary (AS-IS)"]
    WS --> ROLE["5. RequireWorkspaceRole per route group<br>NEW: editor gate on automation writes (GAP-11)"]
    ROLE --> APP["6. Handler/service logic: plan quotas,<br>verified-domain gates, suppression filter (AS-IS)"]
    APP --> EGR["7. Egress: shared SSRF guard<br>static policy, pinned dial, redirect block (AS-IS)"]
    APP --> OUT["8. Outbound trust: HMAC webhooks<br>NEW: timestamp + replay window (GAP-14)"]
```

- [ ] **GAP-11** — Server-side editor gate on automation write & event-ingest routes · S (2.85) — smallest, highest certainty
- [x] **GAP-12** — Enforce or remove `two_factor_required`; recovery codes · M (2.85) — **enforcement landed 2026-11-07 (Wave P3-4)**: policy read at login (`internal/handlers/user_handler.go:129-134`), forced-enrollment marker + dashboard redirect to 2FA setup, admins not exempt, 8-case test matrix (`internal/handlers/login_enforcement_test.go`); recovery codes themselves (R-10) landed `7f445f2`; residual: middleware-layer refusal for un-enrolled sessions (documented `user_handler.go:566-571`)
- [x] **GAP-03** — Multi-replica cron leader election · M (3.15) — **landed 2026-11-11 (OB-6 W6)**: Redis `SET NX PX` leader lease around the cron manager tick (`internal/cron/lease.go` — atomic Lua acquire-or-renew scoped to the holder id, fail-closed on Redis errors; `internal/cron/manager.go` `RunOnce` gates every scheduled tick; `POSTA_CRON_LEADER_LEASE_ENABLED` off by default — single-replica behavior unchanged), per-recipient dedup claims on `bounce_alert`/`daily_report` (fail-open, cleared on send failure), two-manager integration test vs real Redis PASS (`TestLeaderLeaseTwoManagersRedis`, `internal/cron/lease_test.go`) + config off-by-default proof (`TestCronLeaderLeaseOffByDefault`); runbook: `docs/docs/operations/redis-ha.md`
- [ ] **GAP-01** — Scheduled transactional email: cancel + reconcile coverage · M (3.40) — top of register · ⚠ atomic claims landed `7f445f2` (R-26); verify cancel/reconcile remainder · → OB Phase 1
- [ ] **GAP-02** ◆ residual — Delivery & complaint signal ingestion · M (2.85) — core landed `6d2d7c0`:
  - [ ] Legacy `/webhooks/bounce` parity
  - [ ] Complaint → suppression with complaint kind
  - [ ] Outbound `email.delivered` / `email.complained` webhook emission
  - [ ] Analytics + automation integration
  - [ ] Retention wiring
  - [ ] Optional per-workspace provider-config UI
  - [ ] (stretch) ARF/FBL
  
  → OB Phase 2 (bounce/return-path). **Blocks GAP-06, GAP-07.**

## Phase 1 — P1: Signals & sender value (10 items, 6 parallel tracks)

- **Security track:** GAP-13 → GAP-14 → GAP-15
- **Deliverability track:** GAP-06 (after GAP-02 residual)
- **API track:** GAP-05
- **Marketing track:** GAP-25, GAP-26, GAP-27
- **Ops track:** GAP-50
- **Early start:** GAP-04 (XL — the register's biggest build)

- [ ] **GAP-06** — Circuit breaker / auto-pause on failure & complaint thresholds · M (3.15) — blocked by GAP-02 · → OB Phase 1 (reputation)
- [ ] **GAP-50** — Backup/restore tooling & guidance · S (3.00) — ⚠ `pg_dump` CronJob + restore runbook landed `7f445f2` (R-11); verify acceptance criteria, likely closeable · → OB Phase 6 (DR)
- [ ] **GAP-25** — Double opt-in (DOI) · L (2.95) — blocks GAP-30, GAP-35
- [ ] **GAP-05** — Caller-supplied idempotency keys · M (2.85) — → OB Phase 1
- [ ] **GAP-14** — Webhook signature replay protection · S (2.75) — signed timestamp + 5-minute tolerance
- [ ] **GAP-04** — Native ESP sending integrations, failover, per-send server selection · XL (2.55) — `Sender` interface + drivers; seam reused by OB MTA adapter · → OB Phase 1. **Blocks GAP-44 + ESP value.**
- [x] **GAP-13** — `security_events` retention caller · S (2.50) — landed 2026-11-07 (uncommitted working tree, P3-3 ops/settings wave, dwfrun-65ae252f): `RetentionCleanupJob` purges via `SecurityEventRepository.DeleteOlderThan` under `security_events_retention_days` (default 90, 0 = keep forever); wiring `internal/cron/jobs/retention.go:424-433` + `cmd/posta/server.go:829-832`; tests `internal/cron/jobs/retention_security_test.go` (4 named tests); battery PASS
- [ ] **GAP-27** — Data export completeness · M (2.00)
- [ ] **GAP-15** — Tracking toggle symmetry (privacy) · S (2.00) — ⚠ **product-decision gate: decide intent before code**
- [ ] **GAP-26** — Campaign test-send + campaign-scoped preview · M (1.95) — top usability gap; blocks GAP-34

## Phase 2 — P2: Parity batches (22 items, 5 groups)

Email status lifecycle (source: `docs/docs/email-sending/single-email.md`):

```
pending → queued → processing → sent
                        → failed (with error_message)
```

### Batch A — Deliverability & DNS

- [ ] **GAP-07** ◆ residual — Soft-bounce classification & greylist/transient refinement · S (2.30) — core landed `6d2d7c0`:
  - [ ] Classification-counts endpoint
  - [ ] Hard/soft bounce analytics surface
  
  → OB Phase 2
- [ ] **GAP-08** — DNS record health: drift re-verification + SPF/DMARC depth · M (2.30) — ⚠ domain-health endpoint landed `7f445f2` (R-07); verify remaining depth · blocks GAP-10 · → OB Phase 6
- [ ] **GAP-10** — Domain-sending policy enforcement hardening · M (2.20) — after GAP-08 · ⚠ `require_domain_verification` enforcement landed `7f445f2` (R-23) · → OB Phase 1
- [ ] **GAP-09** — Custom tracking domain · M (1.40)

### Batch B — Audience

- [ ] **GAP-29** — Subscriber tags · M (1.50)
- [ ] **GAP-31** — Bulk contact operations · M (1.80)
- [ ] **GAP-19** — Contact upsert/sync API · S (1.30)
- [ ] **GAP-32** — Contact activity timeline (+ unsubscribe-reason reporting) · M (1.50)

### Batch C — Campaign operability

Campaign state machine (source: `docs/docs/campaigns/sending.md`):

```
draft → sending (immediate send)
draft → scheduled (future send)
scheduled → sending (when scheduled time arrives)
sending → paused
paused → sending (resume)
sending → cancelled
paused → cancelled
scheduled → cancelled
sending → sent (all messages processed)
```

- [ ] **GAP-28** ◆ residual — A/B winner selection, end-to-end · S (2.05) — campaign scope landed `6d2d7c0`:
  - [ ] AUT-008 automation `ab_test` node outcome persistence
- [ ] **GAP-33** — Campaign operability: duplication UI + failed-recipient retry · M (1.75)
- [ ] **GAP-34** — Reusable blocks/sections library + campaign content slots · L (1.15) — after GAP-26

### Batch D — Automation

- [ ] **GAP-41** — Workflow testing / dry-run · M (1.80)
- [ ] **GAP-42** — Re-engagement & scheduled enrollment · M (1.65)
- [ ] **GAP-43** — Stop-on-reply · M (1.15)

### Batch E — Platform & ops

- [ ] **GAP-44** — Per-API-key rate limits · M (2.15) — ⚠ in-flight uncommitted (api-key-usage tracking, §0.3); verify when committed
- [ ] **GAP-45** — Blob usage reporting & orphan GC · M (1.45) — adjacent to OB Phase 3/6 (message storage); ⚠ storage half landed in OB-3 wave W2 (2026-11-07): mailstore content-addressed blob GC (per-hash advisory lock + grace period + audited `mail_blob_gc` event) and dedup-aware per-mailbox blob-byte reporting (`mailstore.MailboxUsage`); remaining: bytes-per-workspace reporting for the transactional blob store in AdminInventory and orphan-object enumeration (blobs without ref rows) with a dry-run report
- [ ] **GAP-48** — Queue operations: per-queue latency metric + pause/throttle · M (2.40) — → OB Phase 1
- [x] **GAP-49** — Redis HA guidance & SPOF mitigation docs · M (2.40) — **landed 2026-11-11 (OB-6 W6)**: runbook at `docs/docs/operations/redis-ha.md` (per-role failure matrix citing verified fail-open/fail-closed callsites; managed-Redis replication guidance; queue durability on Postgres via `queue_reconcile`; failure drills; multi-replica checklist) + `compose.sentinel.yml` drill stack **verified end-to-end** (boot clean → 3-sentinel quorum consensus → primary stop → replica promoted ~10s → old primary rejoins as replica); sentinel-monitor-by-hostname caveat recorded (no quorum forms — monitor an IP/VIP)
- [ ] **GAP-51** — Error-monitoring hook · S (2.05)
- [ ] **GAP-16** — Admin-console IP allowlisting · S (1.80) — fail-open CIDR middleware on `/admin/*` via `IPAllowed`
- [ ] **GAP-18** ◆ residual — Webhook test-fire endpoint · S (1.40) — replay landed `6d2d7c0`:
  - [ ] `POST /webhook-endpoints/{id}/test`

## Phase 3 — P3: Long tail (15 items, 4 groups)

### Developer surface

- [ ] **GAP-20** — SMTP diagnostics deepening · M (1.30) — → OB Phase 1 ops
- [ ] **GAP-21** — Message tagging/correlation · M (0.85)
- [ ] **GAP-22** — CLI client · M (0.65)
- [ ] **GAP-23** — SDK completeness verification · S (0.85)
- [ ] **GAP-24** — MCP server · M (0.85)

### Marketing tail

- [ ] **GAP-36** — Template authoring completeness: preview text, Markdown part, media library · M (0.95) — ⚠ **verify-first gate: verify TPL-016/TPL-022 before build**
- [ ] **GAP-37** — UTM auto-tagging · S (1.05)
- [ ] **GAP-38** — Newsletter archive & publications · L (0.75)
- [ ] **GAP-40** — Device/geo breakdowns (`ANA-015`) · M (0.75) — privacy trade-off needs explicit decisions
- [ ] **GAP-35** — Preference center · M (0.80) — builds on GAP-25

### Admin tail

- [ ] **GAP-46** — Admin impersonation · M (1.35)
- [ ] **GAP-47** — Antivirus hook · M (1.25) — → OB Phase 2 (malware scanning)
- [ ] **GAP-17** — Global registration control · S (1.50)

### Docs & observability tail

- [ ] **GAP-52** — Grafana sample dashboard · S (1.20)
- [ ] **GAP-53** — Runtime log viewer & resource history · M (0.90)

## Already delivered (record — do not re-implement)

- **GAP-39** — Timezone-aware sending — **done**, committed `6d2d7c0`
- `347ebb4` — R-01 scheduled-campaign scheduler · R-21 list segment preview fix
- `a6f1681` — R-22 SMTP dial timeouts · R-02 DKIM signing + key lifecycle · R-03 delivery-queue admin + `queue_reconcile` + deterministic TaskIDs
- `6d2d7c0` — R-04 provider-event ingestion · R-05 bounce/failure classification (14 classes) · R-06 webhook delivery replay · R-08 campaign A/B winner · R-09 timezone-aware sending
- `7f445f2` — R-24 CRLF/NUL header-injection hardening · R-25 bcrypt relay credentials · R-26 atomic claims · R-10 TOTP recovery codes · R-11 pg_dump backups + restore runbook · R-23 `require_domain_verification` enforcement · R-07 domain deliverability health

## Hard blockers & decision gates

| Edge | Meaning |
| --- | --- |
| GAP-02 residual → GAP-06 / GAP-07 | Signal integrity precedes auto-pause and soft-bounce analytics |
| GAP-04 seam → GAP-44 + ESP value | `Sender` interface is the precondition |
| GAP-08 → GAP-10 | DNS health before policy hardening |
| GAP-26 → GAP-34 | Test-send/preview before blocks/slots |
| GAP-25 → GAP-30 / GAP-35 | DOI before signup forms and preference center |
| GAP-15 | **Product decision before code** |
| GAP-36 | **Verify before build** |

### D1–D9 duplicates (consolidation work, fold into adjacent items)

D1 keep Subscribers canonical, delete ContactLists (versioned step) · D2 one audit UI w/ scope switcher · D3 segments canonical + FilterRules converter, deprecate new dynamic lists · D4 keep both analytics (document) · D5 merge retry/status into MessageDetail, retire Emails views · D6 delete `ChangePassword.vue`, keep redirect · D7 do **not** merge `tracking_events`/`email_events` (document) · D8 promote Suppressions to own nav entry · D9 three SSE channels acceptable (note only).

---

## Not recommended — keep dead

> Source: `04-feature-gap-analysis.md` §6 + `05-implementation-roadmap.md` §9. **Option B supersedes** the first block only; the rest stays dead.

**Reinstated as Track A candidates by Option B** (were excluded under relay-only strategy): direct-to-MX/MTA (INF exclusion) · IP pools/multiple sending IPs (INF-025) · bounce mailbox/VERP ingestion (INF-020) · PTR/rDNS guidance (DOM-013) · full mail server / IMAP / JMAP / webmail (INF-003) · automated DNS provisioning (DOM-007) · per-domain TLS/ACME (DOM-010).

**Still dead (all 12 competitors verified absent or rejected):** strict priority send queues (INF-011) · multi-channel messengers (INF-036) · import cleaning heuristics (CON-020) · recurring campaigns (CAM-003) · Liquid/Handlebars (TPL-006) · MJML (TPL-007) · React Email (TPL-008) · AI-assisted creation (TPL-020) · spintax (TPL-021) · Zapier/platform connectors (API-018) · SAML SSO · SCIM · DMARC rua ingestion · per-subscriber ML send-time optimization · SPF flattening · MTA-STS/DANE · retention legal-hold · web2-adjacent products (ANA-016, API-019) · license-gated source copies (AGPL/BSL — clean-room behavioral research only).

---

## Appendix — diagram index

| # | Diagram | Source | In this file |
| --- | --- | --- | --- |
| D1 | Control plane / data planes (target) | `upcomming-convo.md` §3 | ✅ §2 |
| D2 | Relay-plane TO-BE (GAP-02/03/04/18/51 marked) | `06-architecture-impact.md` §4.1 | ✅ §2.2 |
| D3 | Outbound delivery flow (target) | `upcomming-convo.md` §6 | ✅ Track A Phase 1 |
| D4 | Outbound delivery pipeline (AS-IS detail) | `06-architecture-impact.md` §2.3 | ✅ Track A Phase 1 |
| D5 | Inbound mail & mailbox delivery (target) | `upcomming-convo.md` §6 | ✅ Track A Phase 2 |
| D6 | Roadmap implementation sequence | `05-implementation-roadmap.md` §8 | ✅ Track B header |
| D7 | Security defense-in-depth layers | `06-architecture-impact.md` §10 | ✅ Track B Phase 0 |
| D8 | Campaign state machine | `docs/docs/campaigns/sending.md` | ✅ Track B Phase 2 |
| D9 | Email status lifecycle | `docs/docs/email-sending/single-email.md` | ✅ Track B Phase 2 |
| D10 | Overall architecture (README) | `README.md` §Architecture | path only |
| D11 | Same architecture, docs-site variant | `docs/docs/getting-started/architecture.md` | path only |
| D12 | AS-IS system context | `06-architecture-impact.md` §2.1 | path only |
| D13 | SMTP relay session → send pipeline | `docs/docs/security/smtp-relay.md` | path only |
| D14 | Inbound webhook flow (AS-IS) | `docs/docs/inbound/overview.md` | path only |
| D15 | Website form → scan → store | `docs/docs/messages/overview.md` | path only |
| D16 | the reference platform detection-to-ban flow (security reference) | `docs/security-research/reference-platform-issue-820-commit-581533b-analysis.md` §5 | path only |
