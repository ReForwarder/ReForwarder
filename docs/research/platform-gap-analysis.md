# ReForwarder — the reference platform Feature Gap Analysis (main report)

> **the reference platform inspected:** `main` @ `8b326865` = **v1.0.3** (clone refreshed 2026-11-05; the 2026-11-02 security deep-dive used `23b69755` = v1.0.1 — two patch releases behind, no feature divergence). **ReForwarder inspected:** `main` @ `7a84e46` (2026-11-04). **Licenses:** the reference platform core AGPL-3.0-only (+ SEL enterprise components); ReForwarder AGPL-3.0-or-later.
> **Companion documents:** `reference-platform-feature-inventory.md` (115 catalogued the reference platform features `SF-01…SF-115`, source evidence + coverage checklist) · `reforwarder-capability-audit.md` (backend + frontend + infrastructure audit) · `reforwarder-reference-platform-roadmap.md` (28 recommendations `R-01…R-29`, R-16 tombstoned, dependencies, acceptance criteria).

---

## 1. Executive summary

the reference platform is a full mail & collaboration **server** (MTA + mailbox store + JMAP/IMAP/POP3/DAV + Sieve + groupware). ReForwarder is an **email-operations platform** — campaigns, automations, tracking, multi-tenant workspaces, and a sending API — that deliberately delivers through trusted relays instead of being an MTA. Of 115 catalogued the reference platform capabilities, **38 are not applicable** to that product model (mailbox/protocol/groupware server surface), **24 are already fully implemented** (several ahead of the reference platform: an in-repo admin UI, admin audit logging, RFC 8058 one-click unsubscribe, campaign/segment depth the reference platform has no equivalent for), **35 are partially implemented**, **16 are not implemented**, and **2 need verification**.

The genuinely valuable gaps cluster in four areas, all consistent with the governing distinction (**email-infrastructure management capabilities in, mail-server capabilities out**):

1. **Deliverability**: DKIM signing is missing entirely (the platform verifies DKIM presence but assumes an upstream signer) — the single largest gap (R-02).
2. **Delivery truth**: no true `delivered` status, no bounce classification, no provider-event ingestion (R-04/R-05) — with a relay-based sender, upstream provider webhooks are the right mechanism, not raw DSN parsing.
3. **Operations**: the asynq delivery queue lacks admin cancel/dead-letter/requeue tooling that the reference platform's queue API has (R-03); Prometheus is off by default (R-12); there is no backup story at all (R-11).
4. **Own-product bugs surfaced by the audit**: scheduled campaigns never fire (`FindScheduledReady` has zero callers — P0) and the subscriber-list "Preview segment" button is broken (wrong endpoint path + response shape — P0). Neither required the reference platform to discover, but the audit caught both.

Everything that makes the reference platform an MTA — MX delivery, routing strategies, milter, Sieve, IMAP/JMAP/POP3, DANE/MTA-STS, clustering — is classified **do not implement**, with the explicit note that the reference platform itself can serve as a ReForwarder customer's relay.

## 2. Scope and methodology

Five read-only investigation passes (no production code or configuration touched; the four documents are the only artifacts): three parallel source sweeps of the the reference platform workspace covering all 13 research categories (A–M) with a per-category coverage checklist; a backend capability audit and a frontend/infrastructure audit of ReForwarder at `7a84e46`, each claim verified in code with file paths (a visible button ≠ a working feature; a route ≠ a feature — handlers and worker paths were checked). The pre-existing `docs/security-research/` Reports A–G (the reference platform v1.0.1 abuse-prevention deep-dive) were cited rather than redone. Feature IDs `SF-nn` (the reference platform) and `R-nn` (roadmap) are stable across all four documents.

## 3. Master comparison matrix

Condensed to comparison rows; full per-feature detail in the inventory (the reference platform side) and audit (ReForwarder side). Statuses use the agreed taxonomy.

| ID(s) | Category | Feature | the reference platform | ReForwarder | Missing in ReForwarder | Pri | Cplx | Roadmap |
|---|---|---|---|---|---|---|---|---|
| SF-01–07, 12, 14, 15, 17–19, 21 | A | Full MTA surface: ESMTP server, MX delivery, routing strategies, relays/source-IP pools, milter, envelope rewriting, queue quotas/encryption, MT-PRIORITY | Full | NOT APPLICABLE (relay-based sender; purpose-specific inbound/relay listeners exist, default-off) | Deliberately absent | DO-NOT | — | — |
| SF-08, 09 | A | Persistent delivery queue w/ per-recipient retry schedules, expiry, scheduling | Full | PARTIALLY (asynq queue + retries + visibility; **scheduled campaigns dead**, no cancel/DLQ) | Scheduler wiring; cancel/requeue/dead-letter | **P0/P1** | S/M | R-01, R-03 |
| SF-10 | A/B | DSN generation & parsing | Full | NOT IMPLEMENTED | True delivery signal | P2* | M | R-04 (via provider events) |
| SF-11 | A | Inbound forwarding, catch-alls, aliases | Full | NEEDS VERIFICATION (inbound ingest + webhook fan-out exist; auto-forwarding to user addresses not found) | Product decision required | P3 | M | INVESTIGATE FURTHER |
| SF-13 | A | Traceability (Received headers, queue ids, wire logs) | Full | PARTIALLY (email_events + message history; no wire-level trace) | Wire-level trace NA for relay model | P4 | — | — |
| SF-16 | A/K | MTA hooks (HTTP decision point per SMTP stage) | Full | PARTIALLY (automations + webhooks cover the concept at product level) | SMTP-stage hooks NA | — | — | — |
| SF-20, 62 | B/E | **DKIM signing + key lifecycle/rotation** | Full | **NOT IMPLEMENTED** (verifies presence only; assumes upstream signer) | Signing, key storage, rotation | **P1** | M | **R-02** |
| SF-22–26, 57 | B | SPF/DKIM/DMARC inbound verification + aggregate/ARF reporting | Full | NOT IMPLEMENTED (domain *presence* verification exists; no inbound verification, no report ingestion) | Inbound verification NA for sender; report ingestion optional | P3 | L | R-17 |
| SF-27–32 | B | ARC, DKIM2, MTA-STS, DANE, TLS-RPT, iprev | Full | NOT APPLICABLE (no MX delivery; TLS to trusted relays) | — | DO-NOT | — | — |
| SF-33–35 | B | TLS server stack, ACME, SASL (PLAIN/LOGIN/OAUTHBEARER) | Full | PARTIALLY (inbound receiver STARTTLS optional; relay AUTH PLAIN/LOGIN; TLS at ingress proxy) | ACME/SNI NA; OAUTHBEARER relay auth optional | P4 | S | OPTIONAL |
| SF-36–38, 44 | B/F | Password hashing, OAuth/OIDC federation, abuse prevention, security events | Full | **FULLY IMPLEMENTED** (bcrypt+TOTP+JWT+keys; security engine is a the reference platform clean-room port; audit + events) | WebAuthn, recovery codes, impersonation | P2/P4 | S/L | R-10, R-19, R-20 |
| SF-39–43 | B | Spam filter (23 modules, ML classifier, AI [ENT]), Pyzor, DNSBL, DoS budgets | Full | NOT APPLICABLE / PARTIALLY (rate limits + size caps + inbound domain verification; no untrusted-mail firehose) | ML filter only if inbound becomes hosted | DO-NOT | — | — |
| SF-45–48, 51 | C | Sieve (47 extensions), ManageSieve, delivery scripts, quarantine | Full | NOT APPLICABLE (automation engine + filter nodes cover workflow conditioning; Junk-filing NA) | — | DO-NOT | — | — |
| SF-49, 50 | C | Header transformation; expression/policy engine | Full | PARTIALLY (header stamping + user-header filtering; FilterRule/segment conditions + security rules — no unified expression language) | Unified expression engine | P4 | L | OPTIONAL |
| SF-52–56 | D | Queue admin: inspect/patch/cancel/requeue, response taxonomy, pause, live diagnostics, tracing | Full | PARTIALLY (asynq Inspector stats + retry endpoint + campaign pause; no cancel/DLQ/requeue/pause, no diagnostics SSE) | Queue admin UI + controls | **P1** | M | **R-03** |
| SF-57–58 | D | Report objects; virtual/named queues | Full | NOT IMPLEMENTED / FULLY (asynq named queues) | Report objects tied to R-17 | P3 | — | R-17 |
| SF-59 | D/J | Operational Actions (reload settings/certs, invalidate caches) | Full | PARTIALLY (DB settings live-apply in 30s; no explicit reload actions) | — | P4 | — | — |
| SF-60, 64 | E | Domain CRUD (aliases, catch-all, relay flag) | Full | **FULLY IMPLEMENTED** (CRUD + verify) | — | — | — | — |
| SF-61 | E | Automatic DNS publication via 11 providers | Full | PARTIALLY (record generation guidance only; no publishing) | Provider integrations | P4 | L | OPTIONAL |
| SF-62 | E | DKIM rotation | Full | NOT IMPLEMENTED | — | P1* | — | R-02 |
| SF-63, 64 | E | DMARC troubleshooter; domain health | Full | NOT IMPLEMENTED / PARTIALLY | Domain health page (auth status + bounce rates) | P2 | S–M | **R-07** (improves on the reference platform) |
| SF-65–68 | F | Accounts, ~660 permissions, API keys w/ narrowing + IP allowlists, session admin | Full | FULLY / PARTIALLY (roles+scopes adequate but coarser; revocation without user-visible session list) | Granular custom roles; session/device UI | P3 | M | R-15, R-16 |
| SF-69–73 | F | 2FA (TOTP), impersonation, SCIM [ENT], tenants [ENT], account destruction | Full | PARTIALLY / NOT IMPLEMENTED / **FULLY** (workspaces ≈ tenants for the core; destruction task exists) | Recovery codes; impersonation; SCIM | P2/P4 | S/M/L | R-10, R-19, R-20 |
| SF-74 | F | Admin audit history | **Negative** | **FULLY IMPLEMENTED** (ReForwarder ahead) | — | — | — | — |
| SF-75–79 | G | Storage planes, backends, blobs (dedup/encrypt), search (inverted index/ES/Meili) | Full | PARTIALLY (Postgres + fs/S3 blobs + retention; no FTS, no at-rest encryption) | FTS for message history | P3 | M | OPTIONAL |
| SF-80–83 | G | Quotas, retention, backup/restore, compression | Full | FULLY (retention) / PARTIALLY (plan quotas, default-off) / **NOT IMPLEMENTED** (backup) / NA | Backup tooling | P2 | S | **R-11** |
| SF-84–86 | H | JMAP / IMAP4rev2 / POP3 / ManageSieve servers | Full | NOT APPLICABLE | — | DO-NOT | — | — |
| SF-87–91 | H | Mgmt API (JMAP registry / REST), API auth, push, telemetry webhooks, lookup stores | Full | **FULLY IMPLEMENTED** (REST + generated OpenAPI; HMAC webhooks out; SSE; Redis lookup) | Web/mobile push (NA) | — | — | — |
| SF-92–96 | I | trc event core, Prometheus, log sinks, healthz, alerts | Full | FULLY / PARTIALLY (metrics default-off; single log sink; bounce-alert only) | Metrics default-on; metric alerts | P3 | XS | R-12 |
| SF-97–99 | J | Admin UI, config model, operational controls | Full | **FULLY IMPLEMENTED** (in-repo Vue admin — ahead of the reference platform's external UI), PARTIALLY (controls) | — | — | — | — |
| SF-100 | J | Config history/rollback | Negative | NOT IMPLEMENTED (parity — neither has it) | Settings change audit events | P4 | S | OPTIONAL |
| SF-101–104 | K | Sieve plugins, expression engine, scheduled tasks | Full | PARTIALLY (automation nodes; FilterRule conditions; 8 crons but user-facing scheduling dead) | Cron triggers; segment conditions | P3 | M | R-13, R-14 |
| SF-105–110 | L | Clustering, queue failover, caches, backpressure, shutdown, hot reload | Full | PARTIALLY (asynq persistence+retry, Redis caches, hot settings; single-node; shutdown unverified) | Distributed fabric (premature) | DO-NOT | — | — |
| SF-111–113 | M | Groupware, file storage, nlp | Full | NOT APPLICABLE | — | DO-NOT | — | — |
| SF-114–115 | M | Deployment tooling; migration guards | Full | **FULLY IMPLEMENTED** (multi-stage image, CI incl. pinned lint + channel-split releases, AutoMigrate + versioned upgrades) | — | — | — | — |

\* routed through provider-event ingestion (R-04) rather than raw DSN parsing, per the relay-based architecture.

## 4. Counts

| Status | Count (of 115) |
|---|---|
| FULLY IMPLEMENTED | 27 |
| PARTIALLY IMPLEMENTED | 29 |
| NOT IMPLEMENTED | 18 |
| NEEDS VERIFICATION | 2 |
| NOT APPLICABLE | 39 |

(Counts re-derived from the per-ID ledger in §4a after the verification pass; the original 24/35/16/2/38 could not be independently re-derived from the grouped matrix — three IDs were double-listed and SF-64 carried two conflicting statuses. The ledger is authoritative.)

Priorities across the **28 recommendations** (R-16 deleted — session management already exists — R-22–R-28 added by verification): **P0 × 2 · P1 × 3 · P2 × 12 · P3 × 8 · P4 × 3** (details and acceptance criteria in the roadmap).

### 4a. Appendix — per-ID status ledger (authoritative; supersedes the grouped matrix above)

| ID | Status | ID | Status | ID | Status | ID | Status | ID | Status |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| SF-01 | N/A | SF-24 | N/I | SF-47 | N/A | SF-70 | N/I | SF-93 | FULL |
| SF-02 | N/A | SF-25 | N/I | SF-48 | N/A | SF-71 | N/A | SF-94 | PART |
| SF-03 | N/A | SF-26 | N/I | SF-49 | PART | SF-72 | FULL | SF-95 | FULL |
| SF-04 | N/A | SF-27 | N/A | SF-50 | PART | SF-73 | FULL | SF-96 | N/I |
| SF-05 | N/A | SF-28 | N/A | SF-51 | N/A | SF-74 | FULL | SF-97 | FULL |
| SF-06 | N/A | SF-29 | N/A | SF-52 | PART | SF-75 | PART | SF-98 | FULL |
| SF-07 | N/A | SF-30 | N/A | SF-53 | PART | SF-76 | PART | SF-99 | PART |
| SF-08 | PART | SF-31 | N/A | SF-54 | PART | SF-77 | FULL | SF-100 | N/I |
| SF-09 | PART | SF-32 | N/A | SF-55 | N/I | SF-78 | N/I | SF-101 | N/A |
| SF-10 | N/I | SF-33 | PART | SF-56 | PART | SF-79 | PART | SF-102 | PART |
| SF-11 | N/V | SF-34 | N/A | SF-57 | N/I | SF-80 | PART | SF-103 | PART |
| SF-12 | N/I | SF-35 | PART | SF-58 | FULL | SF-81 | FULL | SF-104 | PART |
| SF-13 | PART | SF-36 | FULL | SF-59 | PART | SF-82 | N/I | SF-105 | N/A |
| SF-14 | N/A | SF-37 | FULL | SF-60 | FULL | SF-83 | N/A | SF-106 | PART |
| SF-15 | N/A | SF-38 | FULL | SF-61 | PART | SF-84 | N/A | SF-107 | FULL |
| SF-16 | PART | SF-39 | N/A | SF-62 | N/I | SF-85 | N/A | SF-108 | PART |
| SF-17 | N/A | SF-40 | N/A | SF-63 | N/I | SF-86 | N/A | SF-109 | N/V |
| SF-18 | N/A | SF-41 | N/A | SF-64 | N/I | SF-87 | FULL | SF-110 | FULL |
| SF-19 | N/A | SF-42 | N/A | SF-65 | FULL | SF-88 | FULL | SF-111 | N/A |
| SF-20 | N/I | SF-43 | PART | SF-66 | PART | SF-89 | PART | SF-112 | N/A |
| SF-21 | N/A | SF-44 | FULL | SF-67 | FULL | SF-90 | FULL | SF-113 | N/A |
| SF-22 | N/I | SF-45 | N/A | SF-68 | FULL | SF-91 | PART | SF-114 | FULL |
| SF-23 | N/I | SF-46 | N/A | SF-69 | FULL | SF-92 | FULL | SF-115 | FULL |

Legend: FULL = fully implemented · PART = partially implemented · N/I = not implemented · N/V = needs verification · N/A = not applicable.
Tally: FULL 27 · PART 29 · N/I 18 · N/V 2 · N/A 39 = 115.

**Corrections vs the original §4 (24/35/16/2/38):** the grouped matrix double-listed SF-57/SF-62/SF-64 and carried two conflicting SF-64 statuses; this ledger resolves SF-64 to N/I (neither platform has a domain-health page; R-07 proposes it), promotes SF-67/SF-68/SF-72 to FULL (verified: API-key scopes+allowlists+expiry; session list/revoke exists; workspaces cover the tenant core), reclassifies SF-71 (SCIM), SF-101 (Sieve) and SF-105 (clustering) to N/A (do-not-implement tier), moves SF-78 (at-rest encryption) and SF-96 (metric alerts) to N/I, and keeps SF-109 as N/V (shutdown behavior unverified). This ledger is the authoritative status source; the grouped matrix above is retained for narrative context.

## 5. Top recommended additions (highest value first, post-verification)

1. **R-01** Make scheduled campaigns send (P0, S) — broken feature, fix first
2. **R-21** Fix broken subscriber-list segment preview (P0, XS) — broken UI control
3. **R-02** DKIM signing + key lifecycle (P1, M) — biggest deliverability gap
4. **R-22** SMTP client timeouts & deadlines (P1, M) — added by verification; stalled relay wedges workers
5. **R-03** Delivery queue administration: cancel/requeue/dead-letter views (P1, M)
6. **R-04** Provider event ingestion → true `delivered` status (P2, M)
7. **R-05** Bounce/failure classification engine (P2, M)
8. **R-07** Domain deliverability health page (P2, S–M) — improves on the reference platform
9. **R-26** Atomic send/campaign claims — duplicate-send races (P2, M) — added by verification
10. **R-06** Webhook delivery replay (P2, S)
11. **R-08** Campaign A/B winner logic (P2, M)
12. **R-09** Timezone-aware campaign sending (P2, M)
13. **R-10** 2FA recovery codes (P2, S)
14. **R-11** Backup & restore automation + runbook (P2, S)
15. **R-23** Wire up or remove the dead `require_domain_verification` setting (P2, S) — added by verification
16. **R-24** Header/Subject CRLF injection hardening (P2, S) — added by verification
17. **R-25** Relay credential hashing (bcrypt + constant-time) (P2, S) — added by verification
18. **R-12** Prometheus metrics default-on + documented, incl. new campaign/automation families (P3, XS)
19. **R-27** Bound retry sweep query (P3, XS) — added by verification
20. **R-13** Scheduled (cron) automation triggers (P3, M)
21. **R-14** Segment-membership automation conditions (P3, M)

(R-15 per-API-key rate limits, R-17 DMARC ingestion, R-18 key analytics, R-19 passkeys, R-20 impersonation follow in P3/P4 — see the roadmap.)

## 6. Should NOT be added

IMAP/JMAP/POP3/ManageSieve servers · MX delivery engine/full MTA (routing strategies, milter, virtual-queue spool, source-IP pools) · Sieve/ManageSieve · CalDAV/CardDAV/groupware/file storage · mailbox-store clustering (FDB/sharded/replicas) · distributed coordinator fabric · ACME · inbound spam ML stack · masked email/SCIM/AI features [ENT] — each with why-the reference platform-needs-it / why-ReForwarder-doesn't / lightweight alternatives in `reforwarder-reference-platform-roadmap.md` §"Not recommended". The test of the analysis: **the reference platform itself is the ideal relay for ReForwarder** — the two products compose; they should not converge.

## 7. Architectural improvements identified (separate from features)

From source inspection only: (a) the dead scheduled-campaign path and the broken preview endpoint are symptoms of missing endpoint-contract tests between frontend and backend — an API contract test layer (okapi already generates OpenAPI) would prevent both classes; (b) `email_events` is the single delivery-timeline substrate and should be the ingestion target for all provider events (R-04) rather than adding parallel tables; (c) the FilterRule/segment-condition evaluator should remain the one condition→SQL currency (segments already reuse it); (d) settings live-apply (30s cache) is the pattern to extend to any new runtime knob.

## 7a. Verification pass (2026-11-05)

An adversarial verification pass re-checked both source trees and all four documents: every load-bearing the reference platform claim (scheduling, DKIM signing/rotation, queue-admin semantics, DMARC reports, backup CLI, Prometheus, and four negative claims) was **confirmed**; every ReForwarder P0/P1/P2 driver claim was **confirmed** with file:line evidence. Corrections applied: R-16 deleted (sessions already implemented), R-22–R-29 added (two P1-class robustness gaps — SMTP client timeouts and duplicate-send races — plus a dead security toggle, CRLF injection, unsalted relay-credential hashing, unbounded retry sweep, over-broad `?token=` auth), the SF-64/SF-57/SF-62 matrix double-listings resolved via the per-ID ledger (§4a), and citation fixes (R-06→SF-52, R-15→SF-88, do-not range scoped). One the reference platform design correction: DKIM rotation’s `Pending` stage is a DNS-publication retry state, not a pipeline stage.

## 8. Conclusions

ReForwarder is not a young the reference platform — it is a different product whose the reference platform-derived parts (abuse prevention, inbound/relay listeners, the new proxy-pinned webhook egress) are already ported where they belong. The remaining valuable transfers are about **observing and proving delivery** (queue admin, DKIM signing, provider truth, domain health) and **finishing its own broken edges** (scheduled campaigns, preview button). Both P0 items are small; the P1 pair is where the platform's credibility as sending infrastructure is won.

---

*Research-only deliverable. No production code, configuration, issues, or commits were touched. Analyzed 2026-11-05.*
