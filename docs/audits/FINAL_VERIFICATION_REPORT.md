# Final Verification Report — Option B Mail-Infrastructure Programme

**Programme:** Option B (Track A) — the reference-platform mail-server parity surface, ReForwarder (`github.com/goposta/posta`)
**Report date:** 2026-11-12 · **Produced by:** final report builder (final reconciliation stage)
**Baseline:** HEAD `ee38ca8` ("OB-6 convergence: DNS automation, ACME/SNI, IP pools and warm-up with reputation, MTA-STS/TLS-RPT, observability completion, deployment runbooks") — **pushed**: `origin/main` tip is `ee38ca8` and `git log origin/main..main` is empty (verified this session).
**Definitive ledger:** [FEATURE_PARITY_AUDIT.md](FEATURE_PARITY_AUDIT.md) — 123 records (115 SF + 8 CA), each with per-field statuses, Final status, and evidence.

## 0. Provenance and method (read this first)

Everything in this report is grounded in one of exactly two sources, labeled per section:

- **[RAN]** — executed or read in this report session at the working tree (HEAD `ee38ca8` + the uncommitted working-tree changes listed below). Examples: `git log`/`git status`/`git cat-file -t`, the zero-hit absence greps for the four investigated feature groups, the independent tally recount, the ADR/decision-record reads, the mechanical extraction of the §4 matrix from the ledger.
- **[GATE]** / **[AUDIT]** — material supplied to this stage by the workflow: the **gate battery results** (11 named checks with exit codes and output, run by the gate stage at this HEAD) and the **final reconciliation audit** (123 per-record verdicts with `path:line` evidence, distilled into §5). These are incorporated as supplied material and are **not re-run by this session** — see the explicit not-run list below.

**Explicitly NOT run in this session** (reported, not inferred):

- The Go gate battery (`go build`, `go test -count=1 -race ./internal/...`, `go vet`, `golangci-lint`) was **not re-run here**: this environment has no host Go toolchain (`go version` → `go: command not found`, run this session). The [GATE] results in §6 are the gate stage's runs at this HEAD and stand as supplied.
- Docker engine state was not re-checked; the DB-tier battery result in §6 is the gate stage's run.
- No commit and no push was made by this stage (not authorized by the ask; AGENTS.md §6). The tree carries uncommitted work that predates this report and was preserved untouched.

**Working-tree state at report time [RAN]** — `git status --porcelain` (8 modified files, all pre-existing work preserved):

```
 M README.md                                  (33/29)
 M docs/audits/FEATURE_PARITY_AUDIT.md        (36/32 — the 2026-11-12 final-reconciliation ledger repair + tracker finalization)
 M docs/project/BACKLOG.md                    (49/49)
 M docs/project/FEATURE_INVENTORY.md          (36/10)
 M docs/project/STATUS_HISTORY.md             (26/0)
 M todo.md                                    (57/39)
 M web/src/assets/styles.css                  (3/1)
 M web/src/views/inbound/InboundRoutes.vue    (13/2)
```

(`git diff --numstat` output above [RAN].) The §6 [GATE] results correspond to the gate stage's runs; the two `web/src` edits above exist in the working tree outside those runs — recorded, not silently ignored. The ledger repair is uncommitted; its content is verified in §4/§5 and its history is in the ledger's movement bullets ([AUDIT], independently confirmed by the ledger text reads below).

**Route rule honored:** no conflict was created in `internal/routes/*` by this stage (docs-only output file). The programme's route diffs were verified additive by the reconciliation audit ([AUDIT]: OB-2 quarantine file, OB-6 per-domain GETs — "additive, no conflicts").

---

## 1. Complete Option B implementation report [RAN unless noted]

Option B delivered the reference platform's mail-server surface as six implementation units behind a nine-ADR decision gate, executed 2026-11-07 → 2026-11-11, closed by the final reconciliation on 2026-11-12:

1. **OB-0 — ADR gate** (commit `92378f6`, 2026-11-07): nine ADRs Accepted + seven programme documents. All nine read "Status: Accepted (2026-11-07)" at HEAD [RAN].
2. **Phase-3 relay gaps P3-1..P3-5 + OB-1 MTA integration** (commit `cdfa066`, 2026-11-07): inbound verification engines (SPF/DKIM/DMARC), queue operations, settings history, identity hardening (refresh-token rotation, direct IP-allowlist tests), webhook batching, web push, backup/restore, secret encryption — plus the first-party outbound MTA.
3. **OB-1 — Outbound MTA (ADR-001)**: first-party ESMTP client with direct-to-MX delivery, gated 587 MSA, per-domain `relay|direct` delivery mode, per-domain sending budgets (connections / msgs-per-connection / hourly / host attempts), DKIM1 signing, MX resolution behaviors. Committed with the Phase-3 completion commit `cdfa066` (the programme's separate OB-1 hash `1552059` does not resolve in this repository — see §11).
4. **OB-2 — Inbound mail & routing (ADR-002)** (commit `b756f63`, 2026-11-07): seven waves — rules engine + RCPT-time validation, forwarding with hop limits, quarantine with retention, VERP return-path + RFC 3464 DSN match-back, spam/ClamAV baseline chain, ARC/iprev annotate, dashboard UI.
5. **OB-3 — Mailbox storage (ADR-003)** (commit `be9883f`, 2026-11-08): five waves — mailstore service, content-addressed encrypted blobs (zstd + AES-256-GCM) with dedup/ref-count/orphan GC, Postgres FTS search, byte+count quotas with workspace ceiling, inbound delivery seam.
6. **OB-4 — IMAP4rev1 + POP3 (ADR-004)** (commit `183303a`, 2026-11-08): four waves — protocol-access seam (app-password-only `mailauth`), IMAP server, POP3 server, ops wiring; gated listeners on 143/993/110/995 with graceful shutdown.
7. **OB-5 — Webmail & mailbox administration (ADR-005)** (commit `3ef6291`, 2026-11-10): fourteen session-user `/api/v1/mail/*` endpoints, admin mailbox provisioning, compose with clamd-scanned attachments + drafts, webmail UI (folder tree, sandboxed message view, composer, settings).
8. **OB-6 — Convergence (ADR-007/008/009)** (commit `ee38ca8`, 2026-11-11): six waves — DNS automation + drift, ACME/SNI certificate lifecycle, IP pools/warm-up/reputation, MTA-STS + TLS-RPT, observability completion (OTLP, metric alerts, Grafana dashboard, error hook), tenancy decisions + deployment runbooks + JMAP/groupware evaluations.
9. **Final reconciliation** (2026-11-12, ledger-only): 123/123 register records re-verified read-only — 111 verified as recorded, 6 gap-found and repaired in-ledger (SF-05 → COMPLETE; SF-14, SF-39 → PARTIAL; SF-12, SF-38, SF-57 corrected in place), 6 excluded with written justifications ([AUDIT]; the repaired records and the movement bullets were read at the ledger this session [RAN]).

**Final state of the 123-record register** (independently recounted this session [RAN] — `awk` prefix count over the 123 `| Final status | **` lines → `records=123 COMPLETE=69 PARTIAL=35 MISSING=1 PLANNED=18 IN_PROGRESS=0 other=0`, matching the ledger's live-tally line at `FEATURE_PARITY_AUDIT.md:1210`):

| Final status | Records |
|---|---|
| COMPLETE | 69 |
| PARTIAL | 35 |
| MISSING → PLANNED (Phase 3) | 1 (SF-10, RFC 3464 DSN generation) |
| PLANNED (ADR-anchored, OB phases) | 18 |
| IN PROGRESS / BROKEN | 0 |

The PARTIAL and PLANNED records each carry a named residual on the record itself; the deliberate exclusions are justified in §12.

---

## 2. All nine ADR outcomes [RAN]

All nine ADRs are on file under `docs/architecture/` with "**Status:** Accepted (2026-11-07 — Option B programme authorization)" read at HEAD this session (ADR-001's status line additionally carries the programme-authorization pointer). Outcomes as executed:

| ADR | Title | Outcome |
|---|---|---|
| ADR-001 | Outbound MTA Foundation (Direct-to-MX Delivery) | **Landed.** First-party ESMTP client + direct-to-MX executor + gated 587 MSA; per-domain `relay|direct` mode (Decision 2, `ADR-001-outbound-mta.md:19`); flat per-domain sending policy (Decision 8, `:25`); bespoke-queue alternative rejected (`:33`). One recorded non-adoption: SF-21 MT-PRIORITY (§12.1). Residuals: DNSSEC/DANE (SF-30), per-message throttle (CA-04), queue size reservations (SF-17). |
| ADR-002 | Inbound Mail & Routing (Aliases, Forwarding, Quarantine, Return-Path) | **Landed** (OB-2, seven waves). Rules engine, forwarding, quarantine, VERP+DSN match-back. Sieve deliberately not adopted — documented alternative: routing evaluator + ADR-006 filter consult (SF-45/48; SF-46 keeps the interpreter PLANNED). |
| ADR-003 | Mailbox & Storage Foundation | **Landed** (OB-3, five waves). Mailstore service; content-addressed encrypted blobs (compress-then-encrypt, `codec_zstd.go`); dedup + ref-count + orphan GC; Postgres FTS; quotas. External blob stores (FS/S3 swap) remain a documented GAP-45 residual. |
| ADR-004 | IMAP and POP3 Access | **Landed** (OB-4). IMAP4rev1 core + POP3 over the mailstore; app-password-only auth via the `mailauth` seam. IMAP4rev2 extensions and ManageSieve recorded out of scope (ManageSieve stays with ADR-006; SF-46/47). |
| ADR-005 | Webmail & Mailbox Administration | **Landed** (OB-5). Session-user mail REST surface + webmail UI + admin provisioning; mail-view HTML sanitizer; compose with scanned attachments. Decision 4's transactional-compose product decision remains open on the CA-01 record (supersession recorded 2026-11-10). |
| ADR-006 | Spam & Malware Filtering | **Landed** as the baseline chain (OB-2 W5 + GAP-47): auth verdicts → DNSBL → header heuristics → URLs → GTUBE → reputation, ClamAV INSTREAM, dispositions, fail-open HTTP filter consult. The reference ~23-analyzer/~200-tag suite, FTRL classifier (SF-40), LLM verdicts (SF-41), Pyzor/traps/rule-feeds (SF-42 remainder) remain the post-programme parity remainder (SF-39 PARTIAL). |
| ADR-007 | DNS Automation & Deliverability (Providers, Certificates, MTA-STS/TLS-RPT/DANE) | **Landed** (OB-6 W1/W2/W4): 3-of-11 DNS providers + drift detection, ACME (DNS-01 not wired — CertSolver seam reserved), per-domain SNI, MTA-STS hosting+enforcement, TLS-RPT both halves. DANE/TLSA not adopted (SF-30). |
| ADR-008 | Data & Event Ownership (Tenancy, Quotas, Event Substrate) | **Landed** for the event substrate + workspace-as-tenant boundary; §6's Phase-6 revisit produced the tenancy decision record (below). Tenants/SCIM/granular roles not adopted (§12.3). |
| ADR-009 | Deployment Topology & Scaling | **Landed as single-node**: single Postgres + single Redis at Phases 1–5; external stores and multi-node failover rejected per do-not-implement; SF-76 alternative backends remain "an ADR-level decision, not a config option" (`ADR-009-deployment-topology.md:14`, per the SF-76 record). Redis-HA runbook + sentinel failover drill landed (GAP-49). |

**Two sub-decision records produced under ADR-008 §6 / OB-6 W6** (both read at HEAD this session [RAN]):

- [`TENANCY-DECISION.md`](../architecture/TENANCY-DECISION.md) — "**Status:** Decided (2026-11-11 …per ADR-008 §6's Phase 6 revisit)"; "**Decision in one line: Nothing ships now.** No tenant objects, no SCIM, no granular-permission registry…"; "no `tenants` table, no `tenant_id` column anywhere; no SCIM endpoints (RFC 7644)…; no permission registry, no custom-role composition UI/API; no changes to `ownsResource` or the admin middleware chain."
- [`EVALUATIONS.md`](../architecture/EVALUATIONS.md) — "**Status:** Evaluations only (2026-11-11 — OB-6 convergence wave). **These are not commitments.**" §1 = JMAP (RF-B126 / SF-84), §2 = groupware (SF-111) + file storage (SF-112).

---

## 3. OB-1..OB-6 implementation summaries with commits [RAN — tracker read at `FEATURE_PARITY_AUDIT.md:1214-1228`]

Commit hashes below were verified in this session with `git log --oneline -12` and `git cat-file -t`; push state in §11. The Programme Tracker vocabulary is `PLANNED → IN PROGRESS → IMPLEMENTED → TESTING → VERIFIED → COMPLETE`; all rows below are COMPLETE with a recorded commit (ledger `:1219-1227`).

### OB-0 — ADR gate — `92378f6` (2026-11-07)
9 ADRs + 7 programme docs accepted. (The tracker cell's previous hash `50e7f86` does not resolve in this repository — re-recorded at the 2026-11-12 reconciliation, ledger `:1220`.)

### Phase-3 relay gaps (P3-1..P3-5) + OB-1 MTA integration — `cdfa066` (2026-11-07)
Verification engines SPF/DKIM/DMARC (SF-22/23/24 COMPLETE); queue ops (inspect/cancel/replay, pause/resume, actions — SF-52/54/59); logs taxonomy + per-message trace SSE (SF-53/56); settings history + rollback (SF-100); identity hardening — refresh-token rotation (CA-03: 5 named tests), direct IP-allowlist/auth-gate tests (SF-04: 4 named test functions), Redis limiter batteries (SF-91: 9+5 tests), 2FA login enforcement; webhook batching (SF-90: 6 tests), web push (SF-89), backup/restore round-trip (SF-82), secret encryption (SF-78), DKIM key lifecycle + retire (SF-62/104), wire-or-retire CA-02, dev fallback CA-06. Tracker row VERIFIED; remaining work: "SF-50/103 unification deferred" (§12.5). (The tracker cell's previous `02666cc`/`1552059` do not resolve — ledger `:1221`.)

### OB-1 — Outbound MTA (ADR-001) — `cdfa066` (2026-11-07)
First-party outbound ESMTP state machine (`internal/services/mta/session.go` — EHLO/STARTTLS-when-offered/MAIL/RCPT/DATA, AUTH forbidden on MX sessions); direct-to-MX executor with preference-sort + equal-preference shuffle, implicit MX, null-MX-with-recorded-deviation, failure-never-cached TTL resolver (`internal/services/mta/resolver.go`); gated 587 MSA (`submission.go`, `POSTA_SUBMISSION_ENABLED` default false) entering the same send pipeline with event writeback; per-domain `delivery_mode` `relay|direct` with fail-closed unverified-domain refusal; per-domain sending budgets with sliding-window BudgetKeeper and 421/450-style deferral (`mta/policy.go`, `deliver.go`); DKIM1 signing at queue time (`email/dkim_signer.go`). Committed inside `cdfa066` ("…misc waves + OB-1 MTA integration" per the commit subject). Pools/warm-up completed later in OB-6 W3; VERP in OB-2 W4; MT-PRIORITY = approved exclusion (ledger `:1222`).

### OB-2 — Inbound routing (ADR-002) — `b756f63` (2026-11-07)
Seven waves (evidence section read at ledger `:1232-1242` [RAN]): **W1** routing foundation — `inbound_route`/`inbound_forward_task` models, `Router.ResolveRecipient` (first-match-wins, exact beats catch-all, sub-addressing, verified-domain fail-closed), RCPT-time 550 5.1.1 / 451 4.4.3, post-ingest actions (deliver/forward/webhook/quarantine), 21 named tests. **W2** forwarding executor — CAS claim, hop limit 3 with `X-Posta-Forwarded-For`, per-target rate caps, confirmation gate, 17 tests. **W3** quarantine — idempotent holds, release re-runs routing fail-closed, retention purge, dashboard. **W4** VERP/DSN — HMAC-signed `bounce+<uuid>=<sig>@` return-path, RFC 3464 parser, bounce match-back → events + suppression, 31 tests. **W5** spam/malware — `RulesScorer` (six analyzer groups), DNSBL (allowlisted zones, fail-open), GTUBE, ClamAV INSTREAM, dispositions, external filter consult, 53 tests. **W6** ARC/iprev — RFC 8617 §5.2 chain validation annotate-only, iprev forward-confirmation, A-R composer, 43+4 tests. **W7** frontend — `InboundRoutes.vue` + client, 10 vitest cases. Recorded integration item: the `/inbound-routes` rules-CRUD handler is not registered server-side (KNOWN_DRIFT on SF-11/SF-48 — re-verified at HEAD, ledger `:99-102` [RAN]).

### OB-3 — Mailstore (ADR-003) — `be9883f` (2026-11-08)
Five waves: **W1** storage core (mailbox/folder/message models over Postgres); **W2** blob codec + dedup/ref-count/orphan GC (GAP-45 storage half) — content-addressed keys, zstd compress-then-encrypt with AES-256-GCM data keys, self-healing blob reservation; **W3** Postgres FTS — `search_tsv` weighted generated column + GIN, bound-parameterized `websearch_to_tsquery`, 6 named PG search tests; **W4** quotas/retention — per-mailbox byte+count quotas, workspace aggregate ceiling, typed `QuotaError`, concurrent-append no-overshoot test; **W5** inbound ingestion seam (`MailboxDelivery` over `mailstore.Append`, quota-enforced, over-quota → typed quarantine). GAP-45 closed as scoped; `blob.Store` listing-API residual recorded (ledger `:1258`).

### OB-4 — IMAP/POP3 (ADR-004) — `183303a` (2026-11-08)
**W1** protocol-access seam — `internal/services/mailauth` app-password-only authentication (primary password structurally never consulted; byte-identical failure lines asserted by test). **W2** IMAP4rev1 server over the mailstore (22 harness tests + 15 parser + 3 audit + 6 lifecycle); untagged-update engine over UIDVALIDITY/UIDNEXT/mod-seq. **W3** POP3 server (12 named tests, real-TCP harness; TLS enforced by default; session-scoped deletion shadow view; one real race found by review and fixed). **W4** ops wiring — gated listeners 143/993/110/995, graceful shutdown, `POSTA_IMAP_*`/`POSTA_POP3_*` validation, security-engine limiter integration, audit events. Final named battery exit 0, 48 packages ok under `-race`, zero FAIL / zero DATA RACE (ledger `:1225`). SF-85 → COMPLETE, SF-86 → PARTIAL (ManageSieve stays ADR-006).

### OB-5 — Webmail & admin (ADR-005) — `3ef6291` (2026-11-10)
**W1** fourteen session-user `/api/v1/mail/*` endpoints over `mailstore/webmail.go` (mailboxes with quota/usage, folder CRUD+expunge, paged list with filters + FTS, message view with sanitized body + auto-`\Seen`, attachment download, `if_mod_seq` CAS flags, move/copy/delete) + the ADR-005-decision-5 HTML sanitizer. **W2** admin `/admin/mailboxes` (provision/patch/delete/usage, ownership-chain cascade, `admin.mailbox.*` audit). **W3** compose/send/scan — `POST /mail/attachments` (clamd; positives refused 409), `POST /mail/send` (Sent folder via quota-enforced Append + outbound pipeline, delivery status via `X-Posta-Email-Uuid`), draft lifecycle, reply-frame, delivery-status. **W4** webmail UI (`Mailbox.vue`, `MessageView.vue` sandboxed iframe + remote-content gate, `Compose.vue`, `MailSettings.vue`). Close-out gates: full-tree battery exit 0 under `-race`; `type-check` exit 0; vitest 122/122 (23 files); visual acceptance green over `compose.acceptance.yml` (ledger `:1226`). CA-01 → COMPLETE (supersession recorded); SF-65 stays PARTIAL (account aliases/groups absent).

### OB-6 — Convergence (ADR-007/008/009) — `ee38ca8` (2026-11-11)
**W1** DNS automation + drift (SF-61 → COMPLETE): `internal/services/dnsmanager/` (Cloudflare/Route53/DigitalOcean + planner), drift events, cron, five golden routes, Domains.vue panel. **W2** ACME/SNI (SF-34/33 → COMPLETE): certmanager with blob cache, `POSTA_ACME_ENABLED` default false, per-domain SNI selection + named tests; DNS-01 honestly not wired (CertSolver seam reserved). **W3** IP pools/warm-up/reputation (SF-07 completion): `ip_pools`/`ip_pool_members` + `domains.pool_id`, executor `selectBindIP` with attempt-row SourceIP stamping, warm-up ramp folded into the hourly budget, per-(ip,domain) reputation rollups. **W4** MTA-STS + TLS-RPT (SF-29/31 → COMPLETE): policy hosting, per-domain mode API, outbound enforcement hook; TLS-RPT ingest + aggregate/send + daily cron. **W5** observability completion (SF-93/96, GAP-51/52): OTLP push exporter (default-off), metric-alert engine + cron, Grafana dashboard, error-monitoring hook. **W6** tenancy decisions / runbooks / evaluations (SF-66/71/72/84/111/112, GAP-03/49): `TENANCY-DECISION.md`, `EVALUATIONS.md`, Redis leader lease, `redis-ha.md` + sentinel drill, `deployment.md`. Recorded phase-close battery: W4's authoritative final run exit 0, 56 packages ok, 0 FAIL under `-race` (ledger `:1227`).

---

## 4. The full 123-record parity matrix [RAN — mechanically extracted from the ledger this session]

Extraction: `awk` over `docs/audits/FEATURE_PARITY_AUDIT.md` collecting each `### SF-*/CA-*` heading and its `| Final status | **…**` row (123 rows, no manual transcription). The "Ledger line" column points at the record heading; open the cited line for the record's full Implementation/Status/Evidence fields. Live tally: **COMPLETE 69 · PARTIAL 35 · MISSING→PLANNED 1 · PLANNED 18 · IN PROGRESS 0** (recounted this session; matches ledger `:1210`).

| Record | Feature | Final status | Ledger line |
|---|---|---|---|

| SF-01 | Full ESMTP/LMTP state machine | PARTIAL | FEATURE_PARITY_AUDIT.md:54 |
| SF-02 | ESMTP extension advertisement | PARTIAL | FEATURE_PARITY_AUDIT.md:64 |
| SF-03 | Submission (MSA) with delivery-status writeback | COMPLETE | FEATURE_PARITY_AUDIT.md:74 |
| SF-04 | Relay control / open-relay prevention | COMPLETE | FEATURE_PARITY_AUDIT.md:84 |
| SF-05 | Routing strategies (Local / Mx / Relay via expressions) | COMPLETE | FEATURE_PARITY_AUDIT.md:94 |
| SF-06 | MX handling (preference, shuffle, null-MX, implicit MX, DNSSEC) | COMPLETE | FEATURE_PARITY_AUDIT.md:104 |
| SF-07 | Outbound relays (SASL, TLS modes, source-IP pools, pipelining) | COMPLETE | FEATURE_PARITY_AUDIT.md:114 |
| SF-08 | Persistent multi-queue manager (per-recipient retry/notify schedules, expiry, event-locked delivery) | PARTIAL | FEATURE_PARITY_AUDIT.md:124 |
| SF-09 | Future release (HOLDFOR/HOLDUNTIL), DELIVERBY, MT-PRIORITY, DSN params | PARTIAL | FEATURE_PARITY_AUDIT.md:134 |
| SF-10 | Full DSN generation (RFC 3464), double-bounce, signed DSNs | MISSING → PLANNED (Phase 3) | FEATURE_PARITY_AUDIT.md:144 |
| SF-11 | Forwarding chain (sub-addressing, catch-all, aliases, list expansion) | COMPLETE | FEATURE_PARITY_AUDIT.md:154 |
| SF-12 | Envelope rewriting (MAIL FROM / RCPT TO) | PARTIAL | FEATURE_PARITY_AUDIT.md:163 |
| SF-13 | Traceability (Received headers, queue ids, wire logging) | PARTIAL — PLANNED (Phase 3) | FEATURE_PARITY_AUDIT.md:172 |
| SF-14 | Local delivery engine (per-recipient Sieve at ingest) | PARTIAL | FEATURE_PARITY_AUDIT.md:181 |
| SF-15 | Milter client | PLANNED (ADR-001 accepted · OB-1) | FEATURE_PARITY_AUDIT.md:190 |
| SF-16 | MTA hooks (per-SMTP-stage first-party HTTP hooks, HMAC-signed) | PARTIAL | FEATURE_PARITY_AUDIT.md:198 |
| SF-17 | Queue quota reservations | PARTIAL | FEATURE_PARITY_AUDIT.md:207 |
| SF-18 | Queue blob encryption [ENT] | PLANNED (ADR-003 accepted · OB-3) | FEATURE_PARITY_AUDIT.md:216 |
| SF-19 | Tenant-scoped queues [ENT] | PLANNED (ADR-008 accepted · OB-6) | FEATURE_PARITY_AUDIT.md:224 |
| SF-20 | DKIM signing at queue time (DKIM1 RSA/Ed25519 + DKIM2 chained) | COMPLETE | FEATURE_PARITY_AUDIT.md:232 |
| SF-21 | MT-PRIORITY ingress + virtual-queue ordering | PLANNED — approved exclusion (OB-6 close-out, 2026-11-11) | FEATURE_PARITY_AUDIT.md:241 |
| SF-22 | SPF verification (EHLO + MAIL FROM, strict modes, Received-SPF) | COMPLETE | FEATURE_PARITY_AUDIT.md:252 |
| SF-23 | DKIM verification (insecure-sig stripping, strict, ARF) | COMPLETE | FEATURE_PARITY_AUDIT.md:262 |
| SF-24 | DMARC verification with policy enforcement | COMPLETE | FEATURE_PARITY_AUDIT.md:271 |
| SF-25 | DMARC aggregate reports: send AND receive/ingest | PARTIAL | FEATURE_PARITY_AUDIT.md:280 |
| SF-26 | SPF/DKIM auth-failure (ARF RFC 5965/6591) reports | PARTIAL | FEATURE_PARITY_AUDIT.md:289 |
| SF-27 | ARC verification (no sealing) | COMPLETE | FEATURE_PARITY_AUDIT.md:298 |
| SF-28 | DKIM2 verification (chained) + forged-DSN protection | PLANNED (ADR-007 accepted · OB-6) | FEATURE_PARITY_AUDIT.md:307 |
| SF-29 | MTA-STS client + local policy server | COMPLETE (2026-11-11, OB-6 close-out) | FEATURE_PARITY_AUDIT.md:315 |
| SF-30 | DANE (TLSA) with DNSSEC | PLANNED (ADR-001 accepted · OB-1/6) | FEATURE_PARITY_AUDIT.md:324 |
| SF-31 | TLS-RPT (outbound build/send + inbound parse) | COMPLETE (2026-11-11, OB-6 close-out) | FEATURE_PARITY_AUDIT.md:332 |
| SF-32 | Reverse DNS (iprev) validation | PARTIAL | FEATURE_PARITY_AUDIT.md:341 |
| SF-33 | TLS server stack (STARTTLS, SNI wildcard fallback) | COMPLETE (2026-11-11, OB-6 close-out) | FEATURE_PARITY_AUDIT.md:350 |
| SF-34 | ACME certificate automation | COMPLETE (2026-11-11, OB-6 close-out) | FEATURE_PARITY_AUDIT.md:359 |
| SF-35 | SASL mechanisms (PLAIN, LOGIN, OAUTHBEARER, XOAUTH2, MFA, app passwords) | COMPLETE | FEATURE_PARITY_AUDIT.md:368 |
| SF-36 | Password hashing (Argon2id/PBKDF2/scrypt/bcrypt/sha-crypt) | COMPLETE | FEATURE_PARITY_AUDIT.md:377 |
| SF-37 | OAuth 2.0/OIDC provider + directory backends (LDAP/SQL/OIDC) | COMPLETE | FEATURE_PARITY_AUDIT.md:386 |
| SF-38 | Abuse prevention suite (fail2ban, greylisting, rate limiters, DNSBL) | COMPLETE | FEATURE_PARITY_AUDIT.md:395 |
| SF-39 | Spam filter (~23 analyzers / ~200 tags) | PARTIAL | FEATURE_PARITY_AUDIT.md:404 |
| SF-40 | Statistical spam classifier (FTRL-Proximal) | PLANNED (ADR-006 accepted · OB-2/6) | FEATURE_PARITY_AUDIT.md:413 |
| SF-41 | AI spam verdicts via LLM [ENT] | PLANNED (ADR-006 accepted · OB-6) | FEATURE_PARITY_AUDIT.md:421 |
| SF-42 | Pyzor, spam traps, GTUBE, attachment blocklists, external rule feed | PARTIAL | FEATURE_PARITY_AUDIT.md:429 |
| SF-43 | Connection/DoS budgets (memory accounting, session reservations) | PARTIAL | FEATURE_PARITY_AUDIT.md:438 |
| SF-44 | Security event taxonomy (~40 families) | COMPLETE | FEATURE_PARITY_AUDIT.md:447 |
| SF-45 | Sieve-driven policy at SMTP stages | COMPLETE | FEATURE_PARITY_AUDIT.md:456 |
| SF-46 | Sieve interpreter (RFC 5228 + extensions) | PLANNED (ADR-006 accepted · OB-2) | FEATURE_PARITY_AUDIT.md:467 |
| SF-47 | ManageSieve (RFC 5804) | PLANNED (ADR-004 accepted · OB-4) | FEATURE_PARITY_AUDIT.md:475 |
| SF-48 | Per-account Sieve at final delivery | COMPLETE | FEATURE_PARITY_AUDIT.md:483 |
| SF-49 | Header inspection/transformation (editheader, milter, hooks, A-R injection) | PARTIAL | FEATURE_PARITY_AUDIT.md:492 |
| SF-50 | Conditional routing/policy engine (IfBlock expression language) | PARTIAL | FEATURE_PARITY_AUDIT.md:501 |
| SF-51 | Quarantine (spam-percentage Junk filing) | COMPLETE | FEATURE_PARITY_AUDIT.md:510 |
| SF-52 | Queue inspection API (list/query/patch/destroy/cancel/requeue) | COMPLETE | FEATURE_PARITY_AUDIT.md:521 |
| SF-53 | SMTP response taxonomy surfaced to admins | COMPLETE | FEATURE_PARITY_AUDIT.md:530 |
| SF-54 | Global queue pause/resume | COMPLETE | FEATURE_PARITY_AUDIT.md:539 |
| SF-55 | Live delivery diagnosis SSE [ENT] | PLANNED (ADR-001 accepted · OB-1) | FEATURE_PARITY_AUDIT.md:548 |
| SF-56 | Delivery tracing (trace store + live tracing SSE) | COMPLETE | FEATURE_PARITY_AUDIT.md:556 |
| SF-57 | DMARC/TLS/ARF report admin objects | PARTIAL | FEATURE_PARITY_AUDIT.md:565 |
| SF-58 | Virtual queues, named queues, delivery-schedule objects | COMPLETE | FEATURE_PARITY_AUDIT.md:574 |
| SF-59 | Operational Action objects (Reload*, InvalidateCaches, Pause/Resume) | COMPLETE | FEATURE_PARITY_AUDIT.md:583 |
| SF-60 | Domain CRUD (aliases, catch-all, relay flag, DKIM/DNS switches) | PARTIAL | FEATURE_PARITY_AUDIT.md:594 |
| SF-61 | Automatic DNS record publication (11 provider integrations) | COMPLETE (2026-11-11, OB-6 close-out) | FEATURE_PARITY_AUDIT.md:603 |
| SF-62 | DKIM key lifecycle (rotation stages + scheduled rotation task) | COMPLETE | FEATURE_PARITY_AUDIT.md:612 |
| SF-63 | DMARC/SPF/DKIM report settings + TroubleshootDmarc diagnostic | PARTIAL — PLANNED (Phase 3) | FEATURE_PARITY_AUDIT.md:621 |
| SF-64 | Domain health endpoint | COMPLETE | FEATURE_PARITY_AUDIT.md:630 |
| SF-65 | Accounts (User/Group) with credentials, aliases, quotas, locale | PARTIAL | FEATURE_PARITY_AUDIT.md:641 |
| SF-66 | Granular permissions (~660) + custom Roles | PARTIAL — PLANNED (OB-6, with ADR-008 tenancy decisions) | FEATURE_PARITY_AUDIT.md:650 |
| SF-67 | API keys + app passwords (permission narrowing, IP allowlists) | COMPLETE | FEATURE_PARITY_AUDIT.md:659 |
| SF-68 | OAuth grant/session administration | PARTIAL | FEATURE_PARITY_AUDIT.md:668 |
| SF-69 | 2FA: TOTP (+ recovery codes, + WebAuthn beyond the reference platform) | COMPLETE | FEATURE_PARITY_AUDIT.md:677 |
| SF-70 | Admin impersonation | COMPLETE | FEATURE_PARITY_AUDIT.md:686 |
| SF-71 | SCIM 2.0 provisioning [ENT] | PLANNED under ADR-008 (OB-6 revisit) | FEATURE_PARITY_AUDIT.md:695 |
| SF-72 | Tenants (object + quotas + scoped admin views) [ENT] | PARTIAL | FEATURE_PARITY_AUDIT.md:703 |
| SF-73 | Account destruction task (staged, grantee cleanup) | PARTIAL | FEATURE_PARITY_AUDIT.md:712 |
| SF-74 | Admin audit history | COMPLETE | FEATURE_PARITY_AUDIT.md:721 |
| SF-75 | Three-plane storage (data + blob + lookup + search + registry) | COMPLETE | FEATURE_PARITY_AUDIT.md:732 |
| SF-76 | Data-store backends (RocksDB/SQLite/Postgres/MySQL/FDB/HTTP KV) | PARTIAL → PLANNED (ADR-009 accepted · OB-6) | FEATURE_PARITY_AUDIT.md:741 |
| SF-77 | Blob stores (FS/S3; dedup, ref-count, orphan GC) | COMPLETE | FEATURE_PARITY_AUDIT.md:750 |
| SF-78 | Blob at-rest encryption (zstd + AES-256-GCM/ChaCha20) | COMPLETE | FEATURE_PARITY_AUDIT.md:759 |
| SF-79 | Search (inverted index + Elasticsearch/Meilisearch; async reindex) | COMPLETE | FEATURE_PARITY_AUDIT.md:768 |
| SF-80 | Quotas (per-account disk + count quotas) | COMPLETE | FEATURE_PARITY_AUDIT.md:777 |
| SF-81 | Retention/expiration (DataRetention settings, cron purges) | COMPLETE | FEATURE_PARITY_AUDIT.md:786 |
| SF-82 | Backup/restore (stop-and-dump CLI, LZ4) | COMPLETE | FEATURE_PARITY_AUDIT.md:795 |
| SF-83 | Compression (zstd with per-type dictionaries) | COMPLETE | FEATURE_PARITY_AUDIT.md:804 |
| SF-84 | JMAP server (core/mail/submission/…) | PLANNED under ADR-004/005 (OB-4; revisit OB-6) | FEATURE_PARITY_AUDIT.md:815 |
| SF-85 | IMAP4rev1/rev2 server | COMPLETE (ADR-004 · OB-4; IMAP4rev1 core — rev2 extensions + some extensions documented absent, out of scope) | FEATURE_PARITY_AUDIT.md:823 |
| SF-86 | POP3 + ManageSieve servers | PARTIAL (ADR-004 · OB-4; POP3 COMPLETE — ManageSieve remains PLANNED under ADR-006) | FEATURE_PARITY_AUDIT.md:831 |
| SF-87 | Management API (REST/OpenAPI) | COMPLETE | FEATURE_PARITY_AUDIT.md:839 |
| SF-88 | API auth (Basic/Bearer, keys w/ narrowing + IP allowlists, rate limiter) | COMPLETE | FEATURE_PARITY_AUDIT.md:848 |
| SF-89 | Web Push (RFC 8030/8291/8292 VAPID) + SSE + WebSocket push | COMPLETE | FEATURE_PARITY_AUDIT.md:857 |
| SF-90 | Outbound telemetry webhooks (HMAC-signed event batches) | COMPLETE | FEATURE_PARITY_AUDIT.md:866 |
| SF-91 | Lookup stores (Redis/HTTP/memory/SQL) for Sieve/rate-limits/traps | COMPLETE | FEATURE_PARITY_AUDIT.md:875 |
| SF-92 | Event/metrics core | COMPLETE | FEATURE_PARITY_AUDIT.md:886 |
| SF-93 | Prometheus/OpenMetrics endpoint + OTLP push exporter | COMPLETE (2026-11-11, OB-6 W5) | FEATURE_PARITY_AUDIT.md:895 |
| SF-94 | Structured log sinks (console/files/journald/OTLP/webhooks/store) | COMPLETE | FEATURE_PARITY_AUDIT.md:904 |
| SF-95 | Health endpoints (/healthz/live + /healthz/ready) | COMPLETE | FEATURE_PARITY_AUDIT.md:913 |
| SF-96 | Metric alerts (condition→action) [ENT] + metrics/trace history | COMPLETE (2026-11-11, OB-6 W5) | FEATURE_PARITY_AUDIT.md:922 |
| SF-97 | Admin UI | COMPLETE | FEATURE_PARITY_AUDIT.md:932 |
| SF-98 | Configuration = typed registry objects | COMPLETE | FEATURE_PARITY_AUDIT.md:941 |
| SF-99 | Operational controls + task manager with scheduler-role gating | PARTIAL — PLANNED (Phase 3) | FEATURE_PARITY_AUDIT.md:950 |
| SF-100 | Configuration history/rollback | COMPLETE (Wave P3-3) | FEATURE_PARITY_AUDIT.md:959 |
| SF-101 | Sieve system + user scripts at every SMTP stage and delivery | PLANNED (ADR-006 accepted · OB-2) | FEATURE_PARITY_AUDIT.md:970 |
| SF-102 | Sieve plugin functions [ENT] | PLANNED (ADR-006 accepted · OB-6) | FEATURE_PARITY_AUDIT.md:978 |
| SF-103 | Expression language reused everywhere | PARTIAL — PLANNED (Phase 3) | FEATURE_PARITY_AUDIT.md:986 |
| SF-104 | Scheduled task system (ACME, DKIM rotation, reports, purges) | PARTIAL — DKIM auto-retire task COMPLETE (Wave P3-3) | FEATURE_PARITY_AUDIT.md:995 |
| SF-105 | Cluster broadcast fabric (Redis/NATS/Zenoh/Kafka) | PLANNED (ADR-009 accepted · OB-6) | FEATURE_PARITY_AUDIT.md:1004 |
| SF-106 | Store-level queue failover with distributed event locks | PARTIAL | FEATURE_PARITY_AUDIT.md:1015 |
| SF-107 | Cache layers (TTL, negative caching, invalidation, snapshots) | COMPLETE | FEATURE_PARITY_AUDIT.md:1024 |
| SF-108 | Backpressure (queue caps, memory budgets, transfer limits, throttles) | PARTIAL | FEATURE_PARITY_AUDIT.md:1033 |
| SF-109 | Graceful shutdown (systemd notify, snapshot flush) | PARTIAL | FEATURE_PARITY_AUDIT.md:1042 |
| SF-110 | Hot reload of settings/certs/lookup stores/blocked IPs | COMPLETE | FEATURE_PARITY_AUDIT.md:1050 |
| SF-111 | CalDAV/CardDAV/WebDAV groupware | PLANNED (ADR-009 accepted · OB-6 convergence revisit) | FEATURE_PARITY_AUDIT.md:1061 |
| SF-112 | File storage (JMAP FileNode + text extraction) | PARTIAL | FEATURE_PARITY_AUDIT.md:1069 |
| SF-113 | nlp crate (ML classifier, language detection, CJK tokenizers) | PLANNED (ADR-006 accepted · OB-2/6) | FEATURE_PARITY_AUDIT.md:1078 |
| SF-114 | Deployment tooling (installer, Docker bake, systemd, AppArmor) | COMPLETE | FEATURE_PARITY_AUDIT.md:1086 |
| SF-115 | Schema-version migration guard at boot | COMPLETE | FEATURE_PARITY_AUDIT.md:1095 |
| CA-01 | Compose UI for transactional sending | COMPLETE | FEATURE_PARITY_AUDIT.md:1108 |
| CA-02 | Backend endpoints without frontend consumers | COMPLETE | FEATURE_PARITY_AUDIT.md:1116 |
| CA-03 | Refresh-token rotation | COMPLETE | FEATURE_PARITY_AUDIT.md:1125 |
| CA-04 | SMTP client negotiation (SMTPUTF8/8BITMIME) + per-message throttling | PARTIAL | FEATURE_PARITY_AUDIT.md:1134 |
| CA-05 | VERP / bounce mailbox | COMPLETE | FEATURE_PARITY_AUDIT.md:1143 |
| CA-06 | Fallback broker in dev | COMPLETE | FEATURE_PARITY_AUDIT.md:1152 |
| CA-07 | OpenTelemetry export | COMPLETE for metrics (2026-11-11, OB-6 W5) | FEATURE_PARITY_AUDIT.md:1161 |
| CA-08 | Segment audiences skip per-list unsubscribes (by design) | COMPLETE (as designed) | FEATURE_PARITY_AUDIT.md:1169 |

---

## 5. Per-feature implementation evidence (path:line)

Provenance: the cites below are **[AUDIT]** — distilled from the final reconciliation audit supplied to this stage (123 records, each verdict read against HEAD `ee38ca8` by that stage) — except where marked **[RAN]**, which this session executed or read directly. Where a record's status is PARTIAL/PLANNED the absent half is stated, not omitted.

### Category A — Email protocols & delivery (SF-01..SF-21)

| Record | Evidence |
|---|---|
| SF-01 | `internal/services/mta/session.go:111` (runSession drives the outbound conversation), `:128` (AUTH forbidden on MX sessions); gated MSA `internal/services/mta/submission.go:45-53`; attempt rows `internal/models/mta_delivery_attempt.go:47-52`. Absence: LHLO/VRFY/EXPN/LMTP zero hits over `internal/services/mta/` + `internal/services/inbound/`. [AUDIT] |
| SF-02 | Caps negotiation on `SessionCaps` `internal/services/mta/session.go:193-195` (struct `:51-53`); SMTPUTF8 refusal without remote support `:235-238`; caps on `DeliveryResult` `deliver.go:99,269`; both receivers advertise SMTPUTF8 (`submission.go:203`, `inbound/smtp_server.go:286`). Absence: REQUIRETLS/CHUNKING/BDAT zero hits. [AUDIT] |
| SF-03 | `internal/services/mta/submission.go:45-53` (gated 587 MSA, same Send pipeline); `POSTA_SUBMISSION_ENABLED` default false `internal/config/config.go:770`; PLAIN AUTH `:94`, fail-closed tenant gate `:305`; event substrate `internal/models/email_event.go:55`. [AUDIT] |
| SF-04 | Named tests `TestIPAllowed` `internal/middlewares/ipallowed_test.go:36`, `TestAPIKeyAuth_MissingKeyPaths` `:110`, `TestQueryTokenAllowedMatrix` `:166`, `TestAuthenticate_MissingCredentialMessage` `:189`; auth gate `internal/middlewares/middleware.go:254`; smtp-credentials routes `internal/routes/workspace_resource_routes.go:219-254`. [AUDIT] |
| SF-05 | Local delivery: `internal/services/inbound/mailbox_delivery.go:103` (DeliverToMailbox over mailstore.Append), seam `internal/services/inbound/route_action.go:212`, wiring `cmd/posta/server.go:731`; relay\|direct `internal/models/domain.go:29-30,66`, direct-requires-verified refusal `internal/handlers/domain_handler.go:92-93`. [AUDIT; ledger repair 2026-11-12 — record read at `FEATURE_PARITY_AUDIT.md:94-102` this session] |
| SF-06 | `internal/services/mta/resolver.go:210-216` (null MX → A/AAAA fallback with recorded deviation), `:218-222` (implicit MX), `:258-277` (preference sort + shuffle), `:191-196` (TTL cache, failures never cached). DNSSEC absent (SF-30). [AUDIT] |
| SF-07 | `internal/models/ip_pool.go:45,73`; executor `selectBindIP` per delivery `internal/services/mta/deliver.go:165` (impl `:309-315`), SourceIP stamped `:432`; worker resolution `internal/worker/handler.go:167-172`; warm-up cap `internal/services/mta/policy_warmup.go:57`; dial-through test `internal/services/mta/deliver_pool_test.go:66`. [AUDIT] |
| SF-08 | asynq persistent queues + atomic claims `internal/worker/atomic_claim_test.go`; `FOR UPDATE SKIP LOCKED` `internal/storage/repositories/campaign_message_repo.go:83`. Retry/notify schedules + message expiry remain post-programme. [AUDIT] |
| SF-09 | Application-level `ScheduledAt` `internal/models/email.go:87`; send/send-template/batch routes `internal/routes/auth_routes.go:194,224,239`. SMTP HOLDFOR/HOLDUNTIL/DELIVERBY/DSN params absent. [AUDIT] |
| SF-10 | RFC 3464 **parsing** only: `internal/services/inbound/dsn.go:115` (ParseDSN, `:165/:171/:179`); no DSN **builder** — `message/delivery-status` zero-hit as a generator. Status MISSING→PLANNED stands. [AUDIT] |
| SF-11 | `internal/models/inbound_route.go:53`, `internal/models/inbound_forward_task.go:45`; `Router.ResolveRecipient` `internal/services/inbound/router.go:90`; RCPT 550/451 `internal/services/inbound/smtp_server.go:197-217`; hop limit + header `internal/services/inbound/forwarder.go:67-70,90`; UI `web/src/views/inbound/InboundRoutes.vue`. Residual TRUE at HEAD: no `/inbound-routes` CRUD registration (`internal/routes/routes.go:790` wires the engine only). [AUDIT] |
| SF-12 | VERP landed: `internal/services/inbound/verp.go:115` (HMAC-SHA256-signed return path, format doc `:37-42`), stamped via `internal/worker/handler.go:179,185-187` and `cmd/posta/worker.go:153`, `cmd/posta/server.go:745,749,754,1308` [RAN — grep-verified line numbers; ledger repair read at `:163-170`]; SRS not adopted (zero-hit word grep [AUDIT]); MAIL FROM validation `submission.go:303,312`. [AUDIT] |
| SF-13 | Substrate: `internal/models/email_event.go:55`, `internal/models/mta_delivery_attempt.go:47`. RFC 5322 Received-header injection absent (zero hits). [AUDIT] |
| SF-14 | Landed half: `mailbox_delivery.go:103,121` + `route_action.go:212` + `server.go:731`. Absent half: per-recipient Sieve at ingest — `grep -ri sieve internal/` → 0 [RAN this session]. [AUDIT; ledger repair read at `:181-188`] |
| SF-15 | No milter client anywhere; documented alternative is the ADR-006 HTTP filter consult `internal/services/webhook/filter_consult.go:62`. [AUDIT] |
| SF-16 | HMAC-signed consult with timeout scoping `internal/services/webhook/filter_consult.go:62-67`; only MTA hook is STS enforcement (`mta/mtasts.go:509-542`); per-SMTP-stage EHLO/MAIL/RCPT HTTP hooks absent. [AUDIT] |
| SF-17 | `internal/services/mta/policy.go:33-58` (MaxConnections/MaxMsgsPerConnection/MaxHostAttempts/HourlyBudget), BudgetKeeper `:88-100`; hourly budget per delivery `deliver.go:171`, deferral on exhaustion `:207-212`, MaxHostAttempts `:299-301`. Queue size reservations absent (`submission.go:181,201` is the MSA SIZE limit). [AUDIT] |
| SF-18 | Queue payloads are JSON identifiers (`internal/worker/message_handler.go:136-137`); no queue-blob encryption; the landed blob encryption belongs to SF-78/83. [AUDIT] |
| SF-19 | No tenant-scoped queue code (zero hits over `internal/worker` + config); asynq queues global. [AUDIT] |
| SF-20 | DKIM1 signing `internal/services/email/dkim_signer.go:116,132`; AES-256-GCM keys `internal/services/crypto/crypto.go:83`; DKIM2 chained absent per ADR-007 note. [AUDIT] |
| SF-21 | **Excluded** — see §12.1. Absence grep zero hits [RAN]; governing anchors `docs/architecture/ADR-001-outbound-mta.md:5,:19,:25,:33` [RAN]. [AUDIT + RAN] |

### Category B — Email security & authentication (SF-22..SF-45)

| Record | Evidence |
|---|---|
| SF-22 | `internal/services/inbound/verify/spf.go` — include/redirect/a/mx/ip4/ip6/exists/ptr, 4 qualifiers (evalRecord `:136/:167-178`; documented permerror posture `:55`); persisted on `inbound_auth_result` (`internal/models/inbound_auth_result.go:57` area); 10 test functions in `spf_test.go`. [AUDIT] |
| SF-23 | `internal/services/inbound/verify/dkim.go:85` `dkim.VerifyWithOptions` (go-msgauth); d=/s= captured; 8 test functions in `dkim_test.go`. [AUDIT] |
| SF-24 | `internal/services/inbound/verify/dmarc.go` — pct tag + deterministic FNV sampling `:59-61,:144`, relaxed/strict alignment `:61`; recommendation-only posture; read route `internal/routes/workspace_resource_routes.go:1074-1075` wired `internal/routes/routes.go:385`; 15 tests. [AUDIT] |
| SF-25 | Ingest side: `internal/storage/repositories/dmarc_repo.go` + read route above. Send side (aggregate generation/dispatch) absent. [AUDIT] |
| SF-26 | Dependencies (SF-22/23/24) COMPLETE; no RFC 5965/6591 ARF builder exists. [AUDIT] |
| SF-27 | `internal/services/inbound/verify/arc.go:35-44` (cv= matrix, `:97,:101`); RFC 6376 primitives `verify/dkimutil.go`; A-R composer `verify/authres.go`; columns `internal/models/inbound_auth_result.go:67-70`; gates `verify_hook.go:64-65`; 27 tests in `arc_test.go`. [AUDIT] |
| SF-28 | DKIM2 verification zero hits (consistent with SF-20's ADR-007 limitation note). [AUDIT] |
| SF-29 | Policy hosting `internal/handlers/mta_sts_policy_handler.go` (route `internal/routes/mta_sts_routes.go:36`); per-domain mode API `workspace_resource_routes.go:854` → `internal/handlers/domain_handler.go:126`; modes `internal/models/domain.go:37-44`; outbound client `internal/services/mta/mtasts.go:373-408`; enforcement seam in the session runner (`session.go:146-158`). [AUDIT] |
| SF-30 | TLSA/DANE zero hits; DNSSEC absent in the resolver. [AUDIT] |
| SF-31 | Inbound parse `internal/services/tlsrpt/tlsrpt.go:184`; idempotent store `internal/models/tls_rpt.go:36,75`; read handler `internal/handlers/tls_rpt_handler.go` (`Get` at `:78`); route `workspace_resource_routes.go:1086-1088`, golden `docs/implementation/api_routes.txt:182`; aggregator+sender (`tlsrpt/aggregate.go`, `sender.go`); daily cron `internal/cron/jobs/tlsrpt_report.go:69,96`. [AUDIT] |
| SF-32 | Annotate slice: `internal/services/inbound/verify/iprev.go:65`; gate `verify_hook.go:65`; additive columns `inbound_auth_result.go:72+`; 12 tests in `iprev_test.go`. Full validation absent. [AUDIT] |
| SF-33 | Per-domain SNI `internal/services/certmanager/certmanager.go:387`; named tests `certmanager_test.go:129,:177,:198`; wired on IMAP/POP3 `cmd/posta/server.go:385,398`. [AUDIT] |
| SF-34 | `POSTA_ACME_ENABLED` default false `internal/config/config.go:405-411`; CertSolver seam `internal/services/certmanager/certmanager.go:90-100` (DNS-01 not wired — honest residual); listeners wired on IMAP/POP3/submission/webmail. [AUDIT] |
| SF-35 | Constant-time compare `internal/handlers/app_password_handler.go:180-191`; additive migration `internal/storage/migration/migration.go:91`; app-password routes `internal/routes/user_routes.go:500`; relay PLAIN `internal/services/smtprelay/backend.go:103-109`; MSA rehash (`submission.go:57`); Profile card `web/src/views/auth/Profile.vue`. [AUDIT] |
| SF-36 | bcrypt across auth/imap/mailauth/passwordreset/mta-submission/smtprelay (6 files incl. `internal/services/mailauth/auth.go`). [AUDIT] |
| SF-37 | `internal/handlers/oauth_handler_test.go` (831 lines) with the 9 named fake-driver tests at `:592-:825`; provider-issuance non-adoption + ADR-004 LDAP note stand. [AUDIT] |
| SF-38 | DNSBL landed: `internal/services/spamfilter/dnsbl.go` (5-zone allowlist `:47-53`, fail-open `:40,:127`), wiring `cmd/posta/server.go:642`, chain position `internal/worker/inbound_filter.go:44-47`. [AUDIT; ledger repair read at `:394-401`] |
| SF-39 | Baseline chain: `internal/services/spamfilter/scorer.go:139-151` (six analyzer groups), `:163-171` (threshold dispositions); `internal/worker/inbound_filter.go:44-47,:60,:202`; `internal/services/inbound/filter.go:68`; wiring `cmd/posta/server.go:626-645`; ClamAV `internal/services/spamfilter/clamav.go:36,88,144`. Not adopted: ~23-analyzer suite / FTRL / LLM / Pyzor / traps / rule feeds (zero-hit greps). [AUDIT; ledger repair read at `:403-411`] |
| SF-40 | FTRL zero hits; classifier referenced only as the Phase-6 plan comment `internal/services/spamfilter/filter.go:57`. [AUDIT] |
| SF-41 | No LLM verdict code (zero hits in `internal/services/spamfilter` beyond the plan comment). [AUDIT] |
| SF-42 | GTUBE slice: `internal/services/spamfilter/gtube.go:40` (score 1000), `TestScoreGTUBE` `scorer_test.go:189`, `TestInboundFilterRunGTUBEQuarantinedByThreshold` `internal/worker/inbound_filter_test.go:600`. Pyzor/traps/blocklists/rule feeds absent. [AUDIT] |
| SF-43 | Connection budgets verified (as SF-17); memory accounting/session reservations absent. [AUDIT] |
| SF-44 | `internal/models/event.go:33`, `internal/models/security.go:48`; security SSE stream with route-order guard `internal/routes/routes.go:1126-1130`; handlers `internal/handlers/event_handler.go`, `security_handler.go`. [AUDIT] |
| SF-45 | No-Sieve policy hooks: RCPT validation + post-ingest actions + dispositions + fail-open consult (`smtp_server.go:197-217`, `route_action.go:96,212`, `filter.go:68`, `filter_consult.go:62`). Sieve interpreter absent (SF-46). [AUDIT] |

### Category C..M — Product, observability, platform (SF-46..SF-115)

| Record | Evidence |
|---|---|
| SF-46 | Sieve zero hits [RAN grep this session]; alternative real: `in_segment` nodes `internal/services/automation/engine.go:498-533`; ADR-006 on file. [AUDIT + RAN] |
| SF-47 | No ManageSieve (RFC 5804) code; ADR-004 scopes it out to ADR-006. [AUDIT] |
| SF-48 | `router.go:90` + action executor + fail-closed release (`internal/services/inbound/quarantine.go`); rules UI + 11-test spec. KNOWN_DRIFT holds: rules CRUD unregistered (`routes.go:790`; frontend client refs exist in `web/src/api/admin.spec.ts:74,78`, `web/src/api/inboundRoutes.ts`). [AUDIT] |
| SF-49 | A-R composer `internal/services/inbound/verify/authres.go:175` (`sanitizeToken :248`, `quoteARValue :264`); 11 tests in `authres_test.go`; in-band injection deferred. [AUDIT] |
| SF-50 | `in_segment` conditioning `internal/services/automation/engine.go:498,510-533`; `SegmentCondition` `internal/models/segment.go`; `FilterRule` `internal/models/subscriber_list.go`. Transport-policy expressions absent. Unification deferred (§12.5). [AUDIT] |
| SF-51 | Idempotent quarantine `internal/services/inbound/quarantine.go:96-100`; routes `internal/routes/inbound_quarantine_routes.go:35` (golden `api_routes.txt:48,193,373`); handler + `web/src/views/inbound/Quarantine.vue`; retention `internal/cron/jobs/retention.go:93,479`. [AUDIT] |
| SF-52 | `DestroyQueueTask` + audited event `internal/handlers/admin_queue_handler.go:393-409`; Jobs.vue queue surfaces `:23-36`. [AUDIT] |
| SF-53 | `TestLogsClassifications` `internal/handlers/logs_handler_test.go:944,:1009`; route `internal/routes/workspace_resource_routes.go:1794`. [AUDIT] |
| SF-54 | Pause/resume `internal/routes/admin_routes.go:786-812`; `TestAdminQueuePauseResume` `internal/handlers/admin_queue_test.go:723`. [AUDIT] |
| SF-55 | No delivery-diagnosis SSE (zero hits); distinct trace SSE (SF-56) does not satisfy this record. [AUDIT] |
| SF-56 | `GET /logs/{uuid}/events/stream` `workspace_resource_routes.go:1814`; tests `logs_handler_test.go:1026,:1108`. [AUDIT] |
| SF-57 | TLS-RPT objects landed: `internal/models/tls_rpt.go:36-78` (dedup uniqueIndex), `migration.go:55`, `tls_rpt_handler.go:41,78`, route `workspace_resource_routes.go:1086-1088`, golden `:182`, service tests 9+3 [RAN — counts re-verified by the repair verifier]. Residual narrowed to ARF only. [AUDIT; ledger repair read at `:563-572`] |
| SF-58 | Named queues `internal/queues/queues.go:34-40`; per-queue stats `admin_queue_handler.go:182,488`. [AUDIT] |
| SF-59 | `POST /admin/queue/actions/{action}` `admin_routes.go:817-820`; tests `admin_queue_test.go:841,:891`; `ReconcileOnce` `internal/cron/jobs/queue_reconcile.go:96`. [AUDIT] |
| SF-60 | Domain CRUD + verification `internal/handlers/domain_handler.go` + `internal/services/domain/{verifier,health}.go`. Alias/catch-all routing fields deferred to inbound_routes (SF-11/48). [AUDIT] |
| SF-61 | `internal/services/dnsmanager/` (provider + cloudflare/route53/digitalocean + planner + service, 47 tests); drift events `service.go:591,601`; cron `internal/cron/jobs/dns_drift_check.go`; golden `api_routes.txt:44,177,178,357,358`; panel `web/src/views/domains/Domains.vue:381+`. [AUDIT] |
| SF-62 | Lifecycle `internal/models/domain_signing_key.go:27-42`; `DKIMRetireJob` `internal/cron/jobs/dkim_retire.go:36-53` wired `cmd/posta/server.go:1581-1585`. [AUDIT] |
| SF-63 | `internal/handlers/domain_health_handler.go:72` (Get), `:87` (Diagnose). DMARC report-delivery settings absent. [AUDIT] |
| SF-64 | Health routes `workspace_resource_routes.go:961-992`; Domains.vue panel + reputation feed. [AUDIT] |
| SF-65 | `internal/handlers/mail_handler.go`, `internal/routes/mail_routes.go`, `web/src/api/mail.ts`, `web/src/views/mail/`. Account aliases/groups absent (ADR-004 residual). [AUDIT] |
| SF-66 | **Excluded** — see §12.3. Existing roles: `internal/models/user.go:27-28`, `internal/models/workspace.go:27-30`, deployment-admin scope `internal/middlewares/middleware.go:152`. [AUDIT + RAN grep] |
| SF-67 | `internal/models/api_key.go:41` (ValidScopes), `:61-62` (AllowedIPs/Scopes), `:66-67` (RateLimitHourly/Daily); allowlist tests. App-password MSA consumption residual recorded. [AUDIT] |
| SF-68 | `internal/handlers/session_handler.go:77,107,130`; new-device alerts `internal/handlers/user_handler.go:636,839,969`; impersonation field `internal/models/session.go:38`. [AUDIT] |
| SF-69 | 2FA enforcement `internal/handlers/user_handler.go:127-134` (no-admin-exemption comment); 8-case matrix `login_enforcement_test.go`; app-password acceptance `internal/services/mailauth/auth.go` + `auth_test.go:102,193`. [AUDIT] |
| SF-70 | `internal/models/session.go:38` (ImpersonatedBy); reauth/revoke/blacklist/audit chain + banner per the R-20 record (V-EF.md). [AUDIT] |
| SF-71 | **Excluded** — see §12.3. No scim package or routes (zero-hit grep [RAN]). [AUDIT + RAN] |
| SF-72 | Workspace-is-the-tenant half verified (`internal/models/mailbox.go:51` WorkspaceID indexed; `internal/handlers/admin_mailbox_handler.go:40-42` fail-closed binding); distinct tenants/quotas excluded (§12.3; zero `tenant_id` hits [RAN]). [AUDIT + RAN] |
| SF-73 | Staged-destruction chain `internal/cron/jobs/account_cleanup.go` real; recorded Tests: Missing re-verified (no cleanup test functions). [AUDIT] |
| SF-74 | CSV export `internal/handlers/logs_handler.go:1032-1034` + `TestLogsAuditExport` `logs_handler_test.go:873`; SSE streams; deployment-admin-only scope consistent with `middleware.go:152`. [AUDIT] |
| SF-75 | Data plane `internal/storage/migration/migration.go:110-117`; blob plane `internal/services/mailstore/service.go:126-129` (BlobObjectKey); search plane `internal/storage/migration/mailstore_fts.go:34-39`. [AUDIT] |
| SF-76 | ADR-009 `:14` — alternative backends are an ADR-level decision, not a config option. [AUDIT] |
| SF-77 | Named tests `mailstore_pg_test.go:347,:405,:522,:638`, `mailstore_test.go:970`; GC audit event `internal/cron/jobs/retention.go:546,676,725`; sweeper wiring `cmd/posta/server.go:1501-1504`. [AUDIT] |
| SF-78 | `internal/services/crypto/crypto.go:38-42` (ErrEncryptionDisabled), refuse-at-Encrypt `:94`, `EncryptBytes/DecryptBytes :144-151,166`; `ZstdBlobCodec` `internal/services/mailstore/codec_zstd.go:59-72`. [AUDIT] |
| SF-79 | FTS column + GIN `mailstore_fts.go:34-39`; bound-parameterized searcher `internal/services/mailstore/search.go:71,85-86` (512-rune guard `:60`); 6 named PG tests (`search_pg_test.go:77,:121,:156,:202,:236,:274`); reindex route `internal/routes/admin_routes.go:840` (golden `:264`). [AUDIT] |
| SF-80 | `internal/models/mailbox.go:53,56` (QuotaBytes/QuotaMessages), `internal/models/workspace.go:59`; `internal/services/mailstore/quota.go:42-52`; named tests `quota_test.go:134,:83,:167,:230`, `mailstore_pg_quota_test.go:45`, `admin_test.go:165`; UI `Mailboxes.vue:54-68`, `MailSettings.vue:56-62`. [AUDIT] |
| SF-81 | `SetSecurityEventRepo` `internal/cron/jobs/retention.go:133-136`, prune `:466`, wiring `cmd/posta/server.go:1487`; 4 named tests in `retention_security_test.go`. [AUDIT] |
| SF-82 | Backup env trio `internal/cron/jobs/backup.go:31`; restore round-trip `TestBackupRestoreRunbookRoundTrip` `backup_restore_test.go:89` (DB-tier battery skips on golang:1.27 — documented; the gate's DB-tier run executed it — §6). [AUDIT + GATE] |
| SF-83 | `codec_zstd.go:59-61` (klauspost/compress/zstd), 4 KiB threshold `:70-72`, self-describing frame `:65-69`; tests `codec_zstd_test.go:43,:79`. [AUDIT] |
| SF-84 | **Excluded** — see §12.2. Zero-hit jmap grep [RAN]; governing evaluation `docs/architecture/EVALUATIONS.md` §1. [AUDIT + RAN] |
| SF-85 | `internal/services/imap/` (server/session/commands/fetch/parser/search/idle/mime/audit); gated wiring `cmd/posta/server.go:1137` wired `:398`; `AuthenticateWithAppPassword` `internal/services/mailauth/auth.go:94`; 22+15+3+6 named tests across four test files. [AUDIT] |
| SF-86 | `internal/services/pop3/` (server/session/commands/wire/limits/store/audit/doc); wiring `cmd/posta/server.go:997` wired `:385`; exactly the 12 named tests (`pop3_test.go:86-:949`); ManageSieve stays PLANNED (sieve zero hits [RAN]). [AUDIT + RAN] |
| SF-87 | Contract test `TestAPIRouteContract` `internal/routes/contract_test.go:184` pinning the golden `docs/implementation/api_routes.txt`; runtime OpenAPI via okapi (`internal/config/config.go:37`). Golden file = 449 lines total (`wc -l` [RAN]; the record's "431 route lines" counts route entries within it). [AUDIT + RAN] |
| SF-88 | `TestAPIKeyAuth_IPAllowlistGate` `ipallowed_test.go:85`; per-key limits `internal/middlewares/rate_limit_test.go:143,:215`. [AUDIT] |
| SF-89 | `internal/services/webpush/` (encrypt/vapid/service + tests); user push routes `internal/routes/user_routes.go` (~201-230); `web/public/push-sw.js`; Profile card `:1207`; `POSTA_WEB_PUSH_ENABLED` `config.go:655`. [AUDIT] |
| SF-90 | `internal/services/webhook/batch.go`; 6 named tests `dispatcher_batch_test.go:86-:222`; config keys `config.go:645,:648`; wiring `cmd/posta/server.go:1737-1741`. [AUDIT] |
| SF-91 | Exactly 9 test functions in `internal/services/ratelimit/redis_limiter_test.go` and 5 in `internal/services/webauthn/redis_store_test.go` (`grep -c '^func Test'`). [AUDIT] |
| SF-92 | `TestR12R13FamiliesRegistered` `internal/metrics/metrics_test.go:67`; increment tests `:95,:115,:164`. [AUDIT] |
| SF-93 | `internal/observability/otlp.go` (env doc `:29-32`, `ParseOTLPHeaders :119`); `OTLPEndpoint` `config.go:618`; 5 named tests `otlp_test.go`; wired both entrypoints (`server.go:112-114,:409-414`; `worker.go:333-353`). [AUDIT] |
| SF-94 | `internal/logging/` — 7 named tests (`logging_test.go:32-:213`); wired both entrypoints (`server.go:125-129`; `worker.go:50`); sinks stdout\|file only. [AUDIT] |
| SF-95 | `/healthz` + `/readyz` public probes `internal/routes/auth_routes.go:38-53`; worker probes + metrics `cmd/posta/worker.go:330`. [AUDIT] |
| SF-96 | `internal/services/meticalert/engine.go` + 9 named tests (`engine_test.go:204-:464`); cron `internal/cron/jobs/metric_alert.go:43-44` + 2 tests; wiring `cmd/posta/server.go:1592`. [AUDIT] |
| SF-97 | `web/src/views/admin/Mailboxes.vue` + spec + `web/src/api/mailboxes.ts`; router `web/src/router/index.ts:127`; nav `web/src/layouts/DashboardLayout.vue:273`; backend `internal/handlers/admin_mailbox_handler.go` over `internal/services/mailstore/admin.go`; routes `internal/routes/admin_routes.go:854+`. [AUDIT] |
| SF-98 | `internal/services/settings/provider.go:43-48` (30s cache TTL), `:78` (Invalidate). [AUDIT] |
| SF-99 | `GET /admin/jobs` is the only jobs route (`internal/routes/admin_routes.go:679`); no run-history model, no manual-trigger route (recorded PLANNED remainder); ~15 cron jobs registered (`cmd/posta/server.go:1462+`). [AUDIT] |
| SF-100 | `internal/models/setting_history.go:34,:50`; routes `admin_routes.go:309,:322` (golden `:102,:280`); 8 named tests in `setting_history_handler_test.go`. [AUDIT] |
| SF-101 | Sieve zero hits [RAN]; automation alternative `internal/services/automation/engine.go` (+ `engine_segment_test.go:29`); ADR-006 on file. [AUDIT + RAN] |
| SF-102 | Same zero-hit sieve grep [RAN]; ADR-006 anchor. [AUDIT + RAN] |
| SF-103 | Three condition currencies exist independently (`FilterRule`, `SegmentCondition`, automation conditions `engine_segment_test.go:29-30`); no unified evaluator. Deferred (§12.5). [AUDIT] |
| SF-104 | DKIM auto-retire: 7 named tests `dkim_retire_test.go` + registration `server.go:1585`; report-generation task absent; TLS-RPT cron landed outside this record's scope. [AUDIT] |
| SF-105 | Single-fabric Redis pub/sub `internal/services/security/propagate.go`; no nats/zenoh/kafka in `go.mod`. [AUDIT] |
| SF-106 | `FOR UPDATE SKIP LOCKED` claims `internal/storage/repositories/automation_journey_repo.go:69,:87`; constraint note `internal/storage/migration/constraints.go:212`; failover PLANNED per ADR-009. [AUDIT] |
| SF-107 | Snapshot layer `internal/services/security/bansvc.go:64`; settings TTL live-apply; cert host-allowlist hot-reload `internal/services/certmanager/certmanager.go:304-308`. [AUDIT] |
| SF-108 | Delivery budgets enforced (`policy.go:36,:58`; `deliver.go:171`); memory budgets zero hits. [AUDIT] |
| SF-109 | `shutdownServer` `cmd/posta/server.go:1656+` drains protocols/cron/Redis/DB; the embedded worker is still not drained (`server.go:301` discards the handle) — recorded remainder. [AUDIT] |
| SF-110 | Settings TTL + Invalidate (`provider.go:78`), ban propagation (`propagate.go`), ACME host-allowlist hot-reload (`certmanager.go:304-308`). [AUDIT] |
| SF-111 | **Excluded** — see §12.2. Zero-hit DAV grep [RAN]; governing evaluation `docs/architecture/EVALUATIONS.md` §2. [AUDIT + RAN] |
| SF-112 | Implemented half (mail attachment storage): `internal/services/mailstore/parse.go:94-101` (attachment walk), `internal/models/mailbox.go:155` (AttachmentsJSON), `internal/services/mailstore/fetch.go:63` (GetAttachment), tests `mailstore_test.go:90`, `mailbox_delivery_pg_test.go:231`. Excluded half: FileNode zero hits [RAN]; §12.2. [AUDIT + RAN] |
| SF-113 | ftrl/language-detect/cjk zero hits. [AUDIT] |
| SF-114 | Multi-stage `docker/Dockerfile` (node:24 `:9` → golang:1.27-alpine `:18` → alpine:3.21 `:41`); compose.yml/.override/.sentinel/.acceptance; multi-arch release `.github/workflows/release.yml:57` (linux/amd64,linux/arm64); runbooks `docs/docs/operations/deployment.md` + `redis-ha.md` [RAN — directory listed]. [AUDIT + RAN] |
| SF-115 | `internal/storage/migration/upgrade/upgrade.go:67` (Run), `:85` (sealed-registry fresh-install handling); `upgrade/settings.go:38`; registry tests `step_test.go:24,:55`; DSN-gated smoke `migrate_smoke_test.go:33`. [AUDIT] |

### Appendix CA — ReForwarder-side requirements (CA-01..CA-08)

| Record | Evidence |
|---|---|
| CA-01 | `internal/services/mailcompose/` (`send.go:71,:99`; `drafts.go:53,:106,:177`); routes `internal/routes/mail_compose_routes.go:131,:147` (golden `:310-312,:319,:422`); migration `migration.go:123`; `web/src/views/mail/Compose.vue` + 7 vitest tests. [AUDIT] |
| CA-02 | Wired: `web/src/api/campaigns.ts:94-97` (duplicate), `CampaignDetail.vue:289`, `MessageDetail.vue:62-71` (delivery-status poller). Retired: listApiKeys/revokeApiKey/listEmails zero hits in `web/src/api/admin.ts`. [AUDIT] |
| CA-03 | `internal/services/auth/refresh.go` + `internal/storage/repositories/refresh_token_repo.go`; `/auth/refresh` `internal/routes/user_routes.go:544-557`; gate `config.go:54-58`; 5 named tests (`login_enforcement_test.go:989-:1103`). [AUDIT] |
| CA-04 | SMTPUTF8/8BITMIME negotiation `internal/services/mta/session.go:194-195,:232-238`; per-message throttle absent. [AUDIT] |
| CA-05 | `verp.go:115` (EnvelopeFrom), `:139` (Decode); `dsn.go:115` (ParseDSN); receiver allowlist `smtp_server.go:63-83`; envelope seam `internal/worker/handler.go:181-185` (used `:582`); gates `config.go:766-767`. [AUDIT] |
| CA-06 | Dev fallback `internal/services/email/service.go:923` (comment), `sendSync :1111`; tests `dev_fallback_test.go:234,:264`. [AUDIT] |
| CA-07 | OTLP metrics export `internal/observability/otlp.go` + tests (`:192,:203,:218,:459`); default-off `config.go:618`; wired both entrypoints. Trace export honestly not adopted. [AUDIT] |
| CA-08 | By-design skip `internal/worker/campaign_processor.go:131-147`; unsubscribe resolution `internal/services/email/service.go:710-722`; segment tests `campaign_processor_segment_test.go:687,:751,:778`. [AUDIT] |

---

## 6. Backend / frontend / DB / protocol test results [GATE — run by the gate stage at this HEAD; not re-run in this session (no host Go toolchain — §0)]

| # | Check | Exit code | Result |
|---|---|---|---|
| 1 | `go build ./...` | 0 | clean |
| 2 | `go test -count=1 -race ./internal/...` | 0 | **56 packages ok, zero failures, zero DATA RACE.** Slowest: `internal/handlers` 102.407s, `internal/services/inbound` 21.234s, `internal/services/smtprelay` 18.027s, `internal/services/webhook` 12.742s, `internal/services/email` 5.066s, `internal/services/mailstore` 5.067s, `internal/services/mta` 3.093s, `internal/services/pop3` 3.798s. 16 further packages carry `[no test files]` (dto, error_handlers, health, queues, services/audit, cache, emailverify, eventbus, plan, retry, twofactor, storage, storage/blob, web, services/warmup, services/workermon). |
| 3 | `go vet ./...` | 0 | clean |
| 4 | `golangci-lint v2.13.2` (LF-normalized tree) | 0 | `0 issues.` One recorded context line, quoted verbatim: `level=error msg="[linters_context] typechecking error: pattern ./...: directory prefix . does not contain main module or its selected dependencies"` — a linters-context message, not a lint finding (exit 0, zero issues). |
| 5 | `vue-tsc --noEmit` | 0 | clean |
| 6 | `vitest run` (full suite) | 0 | **23 test files passed, 122 tests passed** (21.68s). Includes the OB-5 suites: MessageView 11, Mailboxes (admin) 7, Mailbox 9, Compose 7, MailSettings 6, InboundRoutes 10, Quarantine 7, plus auth/store/api suites. |
| 7 | Production frontend build (`vite build`) | 0 | built in 5.42s; recorded warnings only (deprecated `advancedChunks` option; chunks > 500 kB — codemirror 563.58 kB, grapesjs 1,488.28 kB). |
| 8 | Docs build (Docusaurus) | 0 | `[SUCCESS] Generated static files in "build".` |
| 9 | DB-tier + migration smoke (postgres+redis up) | 0 | `migration` 8.225s ok · `migration/upgrade` 2.971s ok · `repositories` 39.531s ok · `services/domain` 1.775s ok · `services/passwordreset` 2.461s ok. This run also executed the SF-82 restore round-trip path against a live DB tier. |

**Protocol test results** [AUDIT — named batteries per the wave close-outs; counts re-verified by the reconciliation stage]:

- **POP3 (SF-86):** exactly 12 named tests — TestPOP3LoginMatrix, TestPOP3AuthRequiresTLS, TestPOP3StatListUidl, TestPOP3RetrTopByteAccounting, TestPOP3DeleQuitExpunges, TestPOP3RsetRollback, TestPOP3SessionScopedDeletion, TestPOP3CapaBeforeAfterStls, TestPOP3ConnectionAndCommandLimits, TestPOP3AuditEvents, TestPOP3ProtocolBasics, TestPOP3ImplicitTLS — green under `-race`.
- **IMAP (SF-85):** 22 harness tests + 15 parser units + 3 audit + 6 lifecycle = 46 named tests, names matching the ledger record.
- **Inbound verification:** SPF 10 + DKIM 8 + DMARC 15 + ARC 27 + iprev 12 + authres 11 named test functions in the `verify` package family.
- **Mailstore:** dedup/GC/concurrency/quota batteries incl. TestPGConcurrentAppendSameContent, TestPGConcurrentAppendQuotaNoOvershoot, TestPGReserveSelfHealsMissingBlob; PG search battery (6 named tests).
- **OB-4 phase-close battery:** 48 packages ok under `-race`; **OB-6 phase-close battery:** 56 packages ok, 0 FAIL (consistent with gate #2's 56 "ok" lines).

---

## 7. Security results [AUDIT unless noted]

**Pre-existing suites preserved (never weakened — ledger honesty rule 5):**

- SSRF guard `internal/services/webhook/ssrf.go` + `proxydial.go` intact.
- Suppression/consent/eligibility layers, webhook authorization gates, security engine intact (SF-44: shared Event + SecurityEvent models `internal/models/event.go:33`, `internal/models/security.go:48`; SSE with route-order guard `routes.go:1126-1130`).
- Direct gate tests (SF-04): `TestIPAllowed` 19-case matrix, `TestAPIKeyAuth_MissingKeyPaths`, `TestQueryTokenAllowedMatrix`, `TestAuthenticate_MissingCredentialMessage` (`ipallowed_test.go:36-:189`); per-key rate limits (SF-88: `rate_limit_test.go:143,:215`); Redis limiter batteries (SF-91: 9 ratelimit + 5 webauthn tests).
- Identity: bcrypt everywhere (SF-36, 6 files); constant-time app-password compare (`app_password_handler.go:180-191`); 2FA login enforcement with no admin exemption + 8-case matrix (SF-69); refresh-token rotation single-use/chain-rotation tests (CA-03: 5); session/device management + new-device alerts (SF-68); impersonation model + audit (SF-70); WebAuthn store tests (SF-91).
- Secret encryption refuse-closed: `ErrEncryptionDisabled` at `crypto/crypto.go:38-42`, refusal at `:94`.

**Programme additions (verified by the reconciliation audit at HEAD):**

- **Open-relay prevention:** AUTH forbidden on MX sessions (`mta/session.go:128`); MSA PLAIN + fail-closed verified-tenant sender check (`submission.go:94,:305`); construction refuses a missing guard (`submission.go:151-164`).
- **Inbound auth:** SPF/DKIM/DMARC/ARC/iprev engines persisted to `inbound_auth_results` with additive columns; A-R composer with CR/LF/NUL token sanitization (`verify/authres.go:248`).
- **VERP return path:** HMAC-SHA256-signed bounce addresses (`verp.go:115`; secrets `config.go:766-767`).
- **Mail-protocol auth:** `mailauth` seam accepts app passwords ONLY — the primary password is structurally never consulted, and all four credential-failure cases answer byte-identical `-ERR [AUTH] invalid credentials` (asserted by test — no user enumeration) (OB-4 W3 evidence, ledger `:1272` [RAN read]).
- **Spam/malware chain:** allowlisted DNSBL zones, fail-open on lookup errors (`dnsbl.go:40,:127`); ClamAV INSTREAM with 25 MiB cap and configurable fail mode; GTUBE instant-reject; dispositions via `ApplyFilterDisposition` (`inbound/filter.go:68`); fail-open HMAC filter consult (`filter_consult.go:62-67`).
- **Webmail hardening:** server-side HTML sanitizer + remote-content blocking for the sandboxed-iframe render (`internal/handlers/mail_html.go`, ADR-005 decision 5); attachment upload refused 409 with the signature on clamd positives (`mail_compose_routes.go` W3).
- **Quarantine:** fail-closed release re-running the routing evaluator (`inbound/quarantine.go`); audited permanent delete.
- **TLS:** STARTTLS-when-offered with certificate verification always on (outbound); SNI per-domain selection failing closed without the base cert (`certmanager_test.go:198` TestSNISelectionFailsClosedWithoutBaseCert); POP3/IMAP refuse credentials before STLS on non-loopback peers (OB-4 W3).
- **Audit:** `admin.mailbox.*`, `mail_blob_gc`, `queue.task_destroyed`, `domain.dns_drift_detected|resolved`, `pop3.auth.*` etc. over the shared events substrate (SF-74), CSV export tested.

**Security-relevant gate coverage:** the `-race` tree-wide battery (§6 #2) includes every security package (`services/auth`, `security`, `mailauth`, `ratelimit`, `webauthn`, `crypto`, `middlewares`, `session`) — all ok.

---

## 8. Performance / reliability results [AUDIT unless noted]

**Structural reliability evidence (what exists in code, verified):**

- **Queue durability & concurrency:** asynq persistent queues with atomic claims (`internal/worker/atomic_claim_test.go`); `FOR UPDATE SKIP LOCKED` batch claims for campaigns and automation journeys (`campaign_message_repo.go:83`, `automation_journey_repo.go:69,:87`); admin pause/resume + purge/requeue actions; queue reconcile cron (`ReconcileOnce`, `queue_reconcile.go:96`).
- **Sending budgets:** per-domain max connections / msgs-per-connection / hourly budget with sliding-window enforcement and 450-style deferral (`mta/policy.go:33-58,:88-100`; `deliver.go:171,:207-212`); warm-up geometric ramp folded into the hourly budget (`policy_warmup.go:57`); host-attempt caps (`:299-301`).
- **Protocol limits:** POP3 per-IP concurrent-connection cap, per-user session cap, per-session command-rate budget, bounded line reads with drain-on-overflow (`internal/services/pop3/limits.go`, `wire.go`); IMAP equivalents per its record.
- **Mailstore concurrency:** dedup ref-counting under concurrent identical appends (`TestPGConcurrentAppendSameContent`); quota enforcement with no overshoot under concurrency (`TestPGConcurrentAppendQuotaNoOvershoot`, `mailstore_pg_quota_test.go:45`); self-healing blob reservation (`TestPGReserveSelfHealsMissingBlob`).
- **Retention & GC:** quarantine/security-event/mail retention crons; mailstore sweeper + blob usage reporter wiring (`server.go:1501-1504`); orphan GC with audited events (`retention.go:546,676,725`).
- **Failover/HA:** Redis `SET NX PX` cron leader lease (GAP-03, `internal/cron/lease.go`/`manager.go`, two-manager integration test against real Redis PASS); Redis-HA runbook with per-role failure matrix + sentinel failover drill executed end-to-end (GAP-49, `docs/docs/operations/redis-ha.md` + `compose.sentinel.yml`) [RAN — file listed].
- **Backup/restore:** cron backup (`backup.go:31`) with the pg_dump→psql restore round-trip test (SF-82); executed in the gate's DB-tier run against postgres+redis [GATE #9].
- **Observability:** OTLP push exporter default-off, both entrypoints (SF-93/CA-07); metric-alert engine with absent-metric handling (SF-96); R-12/R-13 metric families registered (SF-92).
- **Graceful shutdown:** `shutdownServer` (`cmd/posta/server.go:1656+`) drains SMTP inbound/relay/submission, POP3, IMAP, cert-sync, cron, producer, Redis, log sinks, DB. **Honest remainder:** the embedded worker is not drained (`server.go:301` discards the handle) — recorded on SF-109.

**What was NOT produced:** the programme generated **no load-test or benchmark numbers** (no k6/vegeta/benchmark suite exists in the tree; none was run by any wave). Performance evidence is structural (budgets/limits/claims under `-race`) — reported as such, not as measured throughput.

---

## 9. Documentation structure summary [RAN unless noted]

- **`docs/architecture/`** — 9 ADRs (all Accepted; §2) + `OPTION-B-ARCHITECTURE.md`, `IMPLEMENTATION-ROADMAP.md`, `MIGRATION-PLAN.md`, `RISK-REGISTER.md`, `MESSAGE-LIFECYCLE.md`, `COMPONENT-MATRIX.md`, `DATA-OWNERSHIP.md` + the two decision records `TENANCY-DECISION.md` and `EVALUATIONS.md` (directory listed this session; 18 files).
- **`docs/audits/`** — `FEATURE_PARITY_AUDIT.md` (the definitive 123-record ledger with Programme Tracker, movement bullets, wave-evidence sections), `SOURCE_REGISTER.md`, and `verification/` with 13 verdict documents (V-A1, V-A2, V-B1, V-B2, V-CD, V-DOCS, V-EF, V-G, V-H, V-IJ, V-KL, V-M, V-R — listed this session) + this report.
- **`docs/project/`** — the standing ledgers: `ARCHITECTURE_OVERVIEW.md`, `BACKLOG.md`, `FEATURE_GAP_ANALYSIS.md`, `FEATURE_INVENTORY.md`, `PRIORITIES.md`, `PROJECT_MAINTENANCE.md`, `FEATURE_AUDIT.md`, `STATUS_HISTORY.md` (per AGENTS.md §1; BACKLOG/FEATURE_INVENTORY/STATUS_HISTORY carry the programme's ledger entries).
- **`docs/implementation/`** — `api_routes.txt`, the contract golden pinned by `TestAPIRouteContract` (449 lines total, `wc -l` [RAN]; regenerated as OB waves added routes).
- **`docs/docs/`** — the Docusaurus site (build exit 0 [GATE #8]) including the OB-6 operator runbooks `operations/deployment.md` and `operations/redis-ha.md` [RAN — listed], plus `docs/observability/grafana-posta-overview.json` (GAP-52) [AUDIT].
- **`docs/research/`, `docs/product-roadmap/`** — the clean-room written analyses referenced by the evaluations (AGENTS.md §7 discipline; `infrastructure-feature-roadmap.md:166` and `platform-gap-analysis.md:144` are cited by SF-111's evaluation [AUDIT]).
- Ledger bookkeeping state: the 2026-11-12 final-reconciliation repair + tracker finalization sit as an uncommitted working-tree change to `FEATURE_PARITY_AUDIT.md` (see §0); `README.md` roadmap, `BACKLOG.md`, `FEATURE_INVENTORY.md`, `STATUS_HISTORY.md`, `todo.md` updates are likewise in the working tree.

---

## 10. Migration / deployment instructions [AUDIT unless noted]

**Migrations (additive-only, AGENTS.md §7):**

1. All schema changes ride `internal/storage/migration` (+ `internal/storage/migration/upgrade`): AutoMigrate list additions per unit — OB-2: `inbound_routes`, `inbound_forward_tasks`, `inbound_forward_targets`, `inbound_quarantine`; OB-3: mailstore models (`Mailbox`/`MailFolder`/`MailMessage` + more, `migration.go:110-117`) + FTS (`mailstore_fts.go:34-39`, idempotent ensure); OB-5: `MailAttachmentUpload` (`migration.go:123`); OB-6: `ip_pools`/`ip_pool_members`/`ip_warmup_schedules`/`ip_reputation_rollups`, `TLSRPTReport` (`migration.go:55`), `SettingHistory`, signing-key lifecycle tables.
2. No drops/rewrites; deployed instances upgrade in place. The upgrade framework has fresh-install sealed-registry handling (`upgrade/upgrade.go:85`) and a DSN-gated smoke test (`migrate_smoke_test.go:33`).
3. FTS is idempotent (`ensure`-guarded), safe on re-run.

**Deployment:**

1. Image: multi-stage `docker/Dockerfile` (node:24 build UI → golang:1.27-alpine build → alpine:3.21 runtime); release workflow builds linux/amd64 + linux/arm64 (`.github/workflows/release.yml:57`).
2. Stacks: `compose.yml` (+ `compose.override.yml` dev; `compose.sentinel.yml` Redis-HA; `compose.acceptance.yml` visual acceptance). Single Postgres + single Redis per ADR-009.
3. Required configuration to enable the landed surface (all verified in `internal/config/config.go`):
   - `POSTA_ENCRYPTION_KEY` — required for the mailstore blob plane (the OB-3 delivery seam is gated on it, `server.go:731`).
   - `POSTA_RETURN_PATH_SUBDOMAIN` + `POSTA_RETURN_PATH_SECRET` — VERP bounce ingestion (`config.go:766-767`).
   - `POSTA_IMAP_*` / `POSTA_POP3_*` — protocol listeners; validation enforces TLS on non-loopback and mandatory limits (OB-4 W4).
   - `POSTA_SUBMISSION_ENABLED` (default **false**), `POSTA_ACME_ENABLED` (default **false**), `POSTA_INBOUND_ARC_ENABLED` / `POSTA_INBOUND_IPREV_ENABLED` (default off), `POSTA_SPAM_DNSBL_ZONES` (empty = off; allowlisted zones only), `POSTA_OTLP_ENDPOINT` (empty ships nothing), `POSTA_WEB_PUSH_ENABLED`.
   - DNS automation: `POSTA_DNS_AUTOMATION_ENABLED` posture per the OB-6 W1 evidence [AUDIT].
4. Runbooks: `docs/docs/operations/deployment.md` and `docs/docs/operations/redis-ha.md` (failover drill documented) [RAN — present].
5. Backups: `POSTA_BACKUP_DIR` / `POSTA_BACKUP_RETENTION_DAYS` / `POSTA_BACKUP_PG_DUMP_BIN` (SF-82); restore procedure verified by the round-trip test and the gate's DB-tier run.
6. Contract discipline: after any route change, regenerate `docs/implementation/api_routes.txt` — `TestAPIRouteContract` (`internal/routes/contract_test.go:184`) fails on drift.

---

## 11. Commit hashes and push confirmations [RAN — all verified this session]

| Unit | Commit (verified) | Subject | Pushed |
|---|---|---|---|
| OB-0 ADR gate | `92378f6` | "Option B Phase 0: ADR gate (9 ADRs + 7 programme docs) and feature-parity parity register" | yes (ancestor of origin/main) |
| Phase-3 (P3-1..P3-5) + OB-1 | `cdfa066` | "Phase 3 completion: verification engines, queue ops, ops/settings, identity hardening, misc waves + OB-1 MTA integration" | yes |
| OB-2 | `b756f63` | "OB-2 inbound mail and routing: rules engine, forwarding, quarantine, VERP/DSN, spam+ClamAV, ARC/iprev, dashboard" | yes |
| OB-3 | `be9883f` | "OB-3 mailbox storage foundation: mailstore service, content-addressed encrypted blobs with GC, FTS, quotas, retention, inbound delivery seam" | yes |
| OB-4 | `183303a` | "OB-4 mail protocols: IMAP4rev1 and POP3 servers over the mailstore with app-password auth, TLS, limits, and audit" | yes |
| OB-5 | `3ef6291` | "OB-5 webmail and mailbox administration: mail REST surface, Vue mail area, compose with attachment scanning, admin provisioning" | yes |
| OB-6 (HEAD) | `ee38ca8` | "OB-6 convergence: DNS automation, ACME/SNI, IP pools and warm-up with reputation, MTA-STS/TLS-RPT, observability completion, deployment runbooks" | yes — `origin/main` tip == `ee38ca8`; `git log origin/main..main` → 0 commits; `git status -sb` shows `## main...origin/main` with no ahead/behind marker |

**Hash-provenance deviation (recorded, not papered over).** The reconciliation directive supplied OB-1 = `1552059`, OB-2 = `30a967c`, OB-3 = `8627972`. **None of the three resolves in this repository** — `git cat-file -t` fails for all three ("Not a valid object name", run this session), and none appears in the reflog. The in-repo subjects were matched instead: `git log --all` carries exactly one commit per "OB-n" subject, and the tracker records the same deviation for its earlier cells (`50e7f86`, `02666cc`, `1552059` — ledger `:1209`, `:1220-:1222`), consistent with pre-freeze history rewrite. Every hash in the table above is an in-repo, subject-verified commit.

**Uncommitted and therefore NOT pushed:** the 2026-11-12 ledger repair/tracker finalization (`docs/audits/FEATURE_PARITY_AUDIT.md`) and the other 7 working-tree files (§0). No commit or push was made by this stage (not authorized). The tracker's Final Reconciliation row anticipated the report landing "with the final-reconciliation ledger commit" — that commit remains to be made by an authorized committer.

---

## 12. Remaining exclusions and deferrals, with justifications

The four programme-owner investigations each terminate **excluded, with a written architectural justification citing the governing ADR**, re-verified at HEAD in this session (zero-hit greps [RAN]) and by the reconciliation audit ([AUDIT]):

### 12.1 SF-21 — MT-PRIORITY (RFC 6710) — **EXCLUDED (architectural non-adoption)**

- **Governing ADR:** ADR-001 (SF-21 is in its Covers list, `docs/architecture/ADR-001-outbound-mta.md:5` [RAN]).
- **Absence:** `grep -rniE "mt-priority|mt_priority|mtpriority" internal/ cmd/ --include="*.go"` → **0 hits** [RAN this session].
- **Justification:** ADR-001 Decision 8 fixes the sending policy as flat per-domain controls — max connections, msgs/connection, hourly budget, throttle handling — with no priority-class dimension (`:25` [RAN]). Decision 1 pins delivery to asynq per-delivery worker tasks and the Alternatives section explicitly rejects "Replacing asynq with a bespoke MTA queue" (`:33` [RAN]). The default per-domain mode is `relay` (`:19` [RAN]); in relay mode the message leaves our control at the smarthost, and RFC 6710 §4.3 rule 1 requires dropping the MT-PRIORITY parameter toward any next hop that does not advertise the extension — so no ordering benefit is deliverable on the dominant path. In direct mode, RFC 6710 §5.1's ordering duty requires a shared store-and-forward virtual queue; ADR-001's Decision 1 + rejected alternative leave no such surface — deliveries are independent asynq tasks gated by the flat budget keeper, and faithful priority lanes would reopen exactly the queue-manager alternative ADR-001 rejected.
- **Honesty note (from the audit):** RFC 6710 does *not* forbid MSA use (§3/§10/§11 permit authenticated submission priority) — the exclusion is architectural to this codebase per ADR-001, not standards-mandated. Any future adoption requires a new decision record defining an authenticated MSA priority policy (§11), priority lanes coexisting with the Decision-8 budget keeper with documented fairness semantics, and upstream-relay behavior at non-advertising hops.

### 12.2 SF-84 (JMAP) and SF-111/112 (groupware / file storage) — **EXCLUDED per EVALUATIONS.md**

- **Governing records:** `docs/architecture/EVALUATIONS.md` (OB-6 W6, 2026-11-11; "Evaluations only… These are not commitments" [RAN — header read]), anchored to ADR-004/ADR-005 (JMAP revisit) and ADR-009 (gated-listener/topology model).
- **SF-84 JMAP:** `grep -rni jmap internal/ cmd/ --include="*.go"` → **0 hits** [RAN]. Evaluation §1 maps dependencies against the landed mailstore/IMAP substrate, records the absence of demand with sources, sizes the effort as phase-sized (larger than OB-4's IMAP), records license verification as a prerequisite of any adoption, and sets a standing **NOT NOW** with named re-evaluation triggers. RF-B126 stays open as the demand collector. Ledger record (`:815`) states PLANNED with the evaluation pointer — accurate.
- **SF-111 groupware (CalDAV/CardDAV/WebDAV):** `grep -rniE "caldav|carddav|webdav" internal/ cmd/ --include="*.go"` → **0 hits** [RAN]. Evaluation §2 records demand absence ("different product category" per the written analyses), the dependency analysis (groupware needs its own data domain — calendar/contact stores, recurrence, DAV ACLs, new HTTP/XML listeners — and would not reuse the Option B investment), effort assessed as the largest item on the page, and a standing **NOT NOW** with a higher re-evaluation bar than JMAP.
- **SF-112 file storage — SPLIT verdict:** the **mail-attachment storage half is implemented and verified** (`internal/services/mailstore/parse.go:94-101` attachment walk, `internal/models/mailbox.go:155` AttachmentsJSON, `internal/services/mailstore/fetch.go:63` GetAttachment, tests `mailstore_test.go:90`, `mailbox_delivery_pg_test.go:231` [AUDIT]); the **JMAP FileNode document-style storage + text-extraction half is excluded** (`grep -rni filenode` → **0 hits** [RAN]; evaluation §2 sets the narrow trigger: extraction-for-search behind the existing FTS + ClamAV seams if demand appears). Ledger PARTIAL is accurate.

### 12.3 SF-66/71/72 (granular roles / SCIM / tenants) — **EXCLUDED per TENANCY-DECISION.md**

- **Governing chain:** ADR-008 §6 (Phase-6 revisit gate, `ADR-008-data-event-ownership.md:17` [AUDIT]) → `docs/architecture/TENANCY-DECISION.md` — **Status: Decided 2026-11-11**; "Nothing ships now — no tenant objects, no SCIM, no granular-permission registry… no `tenants` table, no `tenant_id` column anywhere; no SCIM endpoints (RFC 7644); no permission registry, no custom-role composition UI/API; no changes to `ownsResource` or the admin middleware chain" [RAN — read this session].
- **Absence re-verified:** `scim` → 0 hits; `tenant_id|"tenants"` over `internal/` → 0 hits [RAN].
- **Rationale (per the decision record):** the ~660-permission registry is negative value while 4 workspace roles + scoped API keys cover every current surface (status-theater bar per AGENTS.md §4); SCIM without tenant objects is a stub (with only workspaces it would map IdP groups onto workspace memberships with no requesting enterprise); the tenant migration has no consumer and unbounded additive-migration cost without a plan. Deferral-with-prerequisites, not rejection: prerequisites 1–7 (demand evidence, route-derived registry inventory, RFC 7644 design, additive-only schema plan, query-path re-audit, migration plan, operator surface) and named revisit triggers are recorded on the document.
- **What exists today** (verified): platform admin/user roles (`internal/models/user.go:27-28`), workspace owner/admin/editor/viewer (`internal/models/workspace.go:27-30`), deployment-admin scope (`internal/middlewares/middleware.go:152`), per-row workspace isolation with fail-closed admin binding (`admin_mailbox_handler.go:40-42`) [AUDIT].

### 12.4 SF-10 (RFC 3464 DSN generation) — the register's sole MISSING→PLANNED record

Bounce **ingest** and match-back landed (CA-05 COMPLETE); DSN **generation** (builder), double-bounce, signed DSNs remain unbuilt and unscheduled (`dsn.go` is parser-only [AUDIT]). Pointer re-pointed to post-programme backlog.

### 12.5 SF-50/103 — expression-language unification — **DEFERRED** (tracker row, ledger `:1221`)

Three condition currencies operate independently — `FilterRule` (`internal/models/subscriber_list.go`), `SegmentCondition` (`internal/models/segment.go`), automation conditions incl. `in_segment` (`internal/services/automation/engine.go`) — and no unified evaluator exists (no single evaluator type spans the three [AUDIT]). Unification is recorded as the deferred remainder on both records (SF-50 PARTIAL, SF-103 PARTIAL-PLANNED); no behavioral gap results — each currency serves its own surface.

### 12.6 The standing PLANNED surface (18 records)

All 18 PLANNED records are ADR-anchored with named residuals on their records — the largest families: Sieve interpreter + ManageSieve (SF-46/47/101/102, ADR-006 alternative documented), DKIM2 (SF-20/28, ADR-007 note), DANE/DNSSEC (SF-30), tenant-scoped queues (SF-19), spam-suite depth (SF-40/41 + SF-42 remainder), multi-node failover (SF-105/106, ADR-009), FTRL/language detect (SF-113), delivery-diagnosis SSE (SF-55), per-message throttle (CA-04). Each is honest post-programme backlog, not a claimed delivery.

---

## Closing note for the tracker

The Programme Tracker's Final Reconciliation row (ledger `:1228`) named two open items: the **battery+visual acceptance** (delivered by the gate stage at this HEAD — §6 [GATE], incl. the frontend build and visual-acceptance stack usage recorded at the OB-5 close-out) and **this report** — now produced at `docs/audits/FINAL_VERIFICATION_REPORT.md`. The row's remaining step is the authorized commit + push of the reconciliation ledger change and this report; that action is outside this stage's mandate (AGENTS.md §6).
