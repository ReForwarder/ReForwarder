# Project Status Dashboard

**Snapshot date:** 2026-11-14 · **HEAD:** `7d7c848` · **Rule:** audit statuses are historical snapshots — before starting any item, re-verify its state at HEAD (`todo.md` §0).

> **🟡 In flight right now:** SF-25 (DMARC aggregate reports, send + receive parser fix) is **implemented in the working tree but not yet committed** — its programme owns that commit; do not start on the same files (`internal/services/dmarc/`, `internal/cron/jobs/dmarc_report.go`).

## How to read this

Status vocabulary across the repo, and which ledger is authoritative for each:

| ID family | Authoritative ledger | Vocabulary |
|---|---|---|
| `RF-Fnnn` (repo feature registry) | [`project/FEATURE_INVENTORY.md`](project/FEATURE_INVENTORY.md) | Done / In Progress / To-dos |
| `SF-nn` (reference-platform parity, 123 records) | [`audits/FEATURE_PARITY_AUDIT.md`](audits/FEATURE_PARITY_AUDIT.md) | COMPLETE / PARTIAL / PLANNED / EXCLUDED |
| `SF-nn` re-graded verification | [`audits/FINAL_STALWART_FEATURE_VERIFICATION.md`](audits/FINAL_STALWART_FEATURE_VERIFICATION.md) | COMPLETE / PARTIAL / MISSING / BROKEN / BLOCKED / EXCLUDED |
| Live completion queue (26 records) | [`implementation/PARTIAL_FEATURE_COMPLETION_TRACKER.md`](implementation/PARTIAL_FEATURE_COMPLETION_TRACKER.md) | PARTIAL → IN PROGRESS → IMPLEMENTED → TESTING → VERIFIED → COMPLETE |
| Operational tasks (91) | [`project/BACKLOG.md`](project/BACKLOG.md) | To-do / In Progress / In Review / Done |

Legend used below: ✅ done · 🟡 in progress / partial · ⬜ to-do · 🚧 in-flight (uncommitted) · 🚫 decision-gated — do not start without the gate · ⛔ excluded / deferred with recorded justification.

---

## 1. Headline counts (2026-11-14)

| Ledger | Counts | Evidence |
|---|---|---|
| RF-F feature registry (128 entries) | **100 Done · 13 In Progress · 15 To-dos** | [`project/FEATURE_INVENTORY.md:5`](project/FEATURE_INVENTORY.md) |
| SF parity register (123 records) | **COMPLETE 83 · PARTIAL 22 · PLANNED 17 · EXCLUDED 1** | [`audits/FEATURE_PARITY_AUDIT.md:1216`](audits/FEATURE_PARITY_AUDIT.md) |
| Live PARTIAL-completion tracker (26 rows) | **4 COMPLETE (SF-08, SF-09, SF-02, SF-25) · 1 IMPLEMENTED (SF-26, in flight) · 21 PARTIAL** | [`implementation/PARTIAL_FEATURE_COMPLETION_TRACKER.md`](implementation/PARTIAL_FEATURE_COMPLETION_TRACKER.md) |
| Operational backlog (91 tasks) | 47 To-do · 17 In Progress · 27 Done | [`project/BACKLOG.md`](project/BACKLOG.md) (header count line is stale; row statuses are current) |
| GitHub board ("ReForwarder Backlog" #3) | **102 Feature Done · 20 In Progress · 46 To-Dos · 22 Features Incoming** (190 cards) | synced + **full-coverage verified 2026-11-14**: every not-done item in every ledger (registry 13 IP + 15 To-dos, BACKLOG 91 tasks, tracker 22 open records, parity 22 PARTIAL) has exactly one card — 0 missing, 0 duplicates (the original double-seeded 31 duplicate cards were removed; richer card kept in each pair). 9 coverage gaps closed this pass (RF-F122 + SF-01/60/66/68/72/76/106/112). Card bodies lead with plain-language summaries; internal details collapsed. Mapping: registry Done → Feature Done, In Progress → In Progress, To-dos + hygiene/open-gap cards → To-Dos. The 17 PLANNED (ADR-gated) records stay off-board by design — tracked in §7. |

---

## 2. 🚧 In flight

- **SF-26 — ARF auth-failure reports** — implemented and verified by its programme (tracker row 5, IMPLEMENTED; commit pending per its flow). **SF-25 landed as `8e9a8b3` (2026-11-14)**. Owner: the PARTIAL-completion programme — do not start on its files.

## 3. ⬜ Next up — the live programme queue (in dependency order)

`docs/implementation/PARTIAL_FEATURE_COMPLETION_TRACKER.md` rows 5–26, verbatim order:

| # | ID | Feature | Gate / dependency |
|---|---|---|---|
| 5 | SF-26 | ARF auth-failure reports (RFC 5965/6591) | rides the landed SF-25 skeleton — **start here** |
| 6 | SF-57 | ARF report admin objects | needs SF-26 |
| 7 | SF-63 | per-domain rua / report-interval settings | needs SF-25 send side (now landed) |
| 8 | SF-16 | per-SMTP-stage HTTP hooks (async) | ADR-001/002-gated slice |
| 9 | SF-43 | per-connection byte budgets + MTA session reservations | bounded spec items 1+3 |
| 10 | SF-65 | mail-account aliases + group delivery | — |
| 11 | SF-99 | task-manager run history + manual trigger + role gating | bounded spec: 1 table + 2 routes + drawer |
| 12 | SF-103 | expression-language unification | refactor spec recorded |
| 13 | SF-50 | transport-policy expressions | needs SF-103 |
| 14 | SF-39 | additional spam analyzers | beyond the ADR-006 baseline |
| 15 | SF-42 | Pyzor / spam traps / rule feeds | ADR-006 amendment |
| 16 | SF-12 | SRS envelope rewriting | ADR amendment + bounded implementation |
| 17 | SF-14 | Sieve-subset filter scripts at ingest | ADR-006 amendment |
| 18 | SF-49 | in-band A-R header injection | archive-integrity-safe path decision |
| 19 | SF-86 | ManageSieve server | needs SF-14 + ADR-004 listener |
| 20 | SF-01 | VRFY/EXPN | 🚫 go-smtp fork decision (VRFY 252 hardcoded upstream) |
| 21 | SF-60 | domain CRUD ledger re-word | wording-only change |
| 22 | SF-68 | OAuth grant/session administration | 🚫 SF-37 OAuth-provider decision (EVALUATIONS.md) |
| 23 | SF-72 | tenants | 🚫 TENANCY-DECISION.md prerequisites |
| 24 | SF-76 | alternative data-store backends (SQLite) | 🚫 ADR-009 amendment |
| 25 | SF-106 | multi-node store failover | 🚫 documented deployment concern (ADR-009 §5) |
| 26 | SF-112 | FileNode + text extraction | 🚫 demand gate (EVALUATIONS.md) |

Rows 20/22/23/24/25/26 are decision- or infra-gated — they end as honest BLOCKED records with evidence unless their gate is moved first. **All 22 open rows have board cards (2026-11-14 coverage verification); SF-66 (parity PARTIAL, outside the tracker) also has a card.**

## 4. 🟡 Registry In Progress (13) — the named residual in each

| ID | Feature | Remaining |
|---|---|---|
| RF-F005 | 2FA recovery codes | admin reset / policy interplay |
| RF-F014 | Passkeys / WebAuthn | complete enrollment + login (tracker-vs-registry discrepancy on record) |
| RF-F015 | Admin impersonation | guardrails, audit depth, UI flag |
| RF-F023 | DNS record guidance depth | SPF/DMARC parsing depth, non-connected-domain drift |
| RF-F026 | Domain deliverability health | depth (parsing, DMARC trends) |
| RF-F027 | Strict verified-domain sending | campaign-path parity, per-domain caps |
| RF-F028 | DMARC rua ingestion | trend surfacing on the health page |
| RF-F040 | Per-domain inbound routing rules | REST handler registration (KNOWN_DRIFT) |
| RF-F053 | Legacy `/webhooks/bounce` parity | suppression, bounce rows, complaint semantics — **P0** |
| RF-F081 | System-info feature flags | completion remainder |
| RF-F082 | API route contract gate | frontend spec coverage |
| RF-F104 | Campaign-level failed-recipient retry | `POST /campaigns/{id}/retry-failed` missing |
| RF-F122 | IP pools / warm-up / reputation | RF-B123 AC 3 (blacklist alerts + abuse runbook) and AC 4 (metrics families) |

## 5. ⬜ Registry To-dos (15)

| ID | Feature | Gate |
|---|---|---|
| RF-F048 | scheduled transactional send cancel + reconcile (GAP-01) | **P0** |
| RF-F099 | true delivered state: emission + analytics + UI paths (GAP-02) | **P0** — blocks GAP-06/07 |
| RF-F075 | double opt-in (GAP-25) | unlocks RF-F073 + RF-F074 |
| RF-F073 | preference center (GAP-35) | blocked behind DOI |
| RF-F074 | subscriber signup forms (GAP-30) | blocked behind DOI |
| RF-F029 | custom tracking domain (GAP-09) | — |
| RF-F056 | webhook test-fire (GAP-18 residual) | — |
| RF-F057 | bounce classification analytics (GAP-07 residual) | — |
| RF-F061 | UTM auto-tagging (GAP-37) | — |
| RF-F071 | automation dry-run (GAP-41) | — |
| RF-F076 | subscriber tags (GAP-29) | — |
| RF-F092 | admin-console IP allowlisting (GAP-16) | — |
| RF-F100 | tracking toggle symmetry (GAP-15) | 🚫 product-decision gate |
| RF-F101 | Contact Lists finish-or-delete (D1) | 🚫 product decision |
| RF-F103 | backend endpoints without UI | list needs re-verification |

## 6. 🟡 Parity register PARTIAL (23) — summary

SF-01, SF-12, SF-14, SF-16, SF-26, SF-39, SF-42, SF-43, SF-49, SF-50, SF-57, SF-60, SF-63, SF-65, SF-66, SF-68, SF-72, SF-76, SF-86, SF-99, SF-103, SF-106, SF-112 — all detailed in §3's tracker table above; evidence per record in [`audits/FEATURE_PARITY_AUDIT.md`](audits/FEATURE_PARITY_AUDIT.md).

> Vocabulary note: `FINAL_STALWART_FEATURE_VERIFICATION.md` re-grades this set with its own vocabulary — 11 → BLOCKED, 2 → MISSING (SF-18, SF-55), SF-66 + 4 PLANNED → EXCLUDED. The parity register remains the ledger of record.

## 7. ⛔ PLANNED (17, ADR-anchored) and EXCLUDED (1)

| Records | Anchor |
|---|---|
| SF-18, SF-19, SF-28, SF-30, SF-40, SF-41, SF-46, SF-47, SF-55, SF-71, SF-84, SF-101, SF-102, SF-105, SF-113 | ADR-001…ADR-009 gates (queue encryption, tenant-scoped queues, DKIM2, DANE, FTRL classifier, LLM verdicts, Sieve interpreter/plugins, diagnosis SSE, SCIM, JMAP, cluster fabric, nlp crate) |
| SF-111 (groupware) | PLANNED per V-M; higher re-evaluation bar than JMAP |
| SF-21 (MT-PRIORITY) | PLANNED-approved-exclusion |
| SF-15 (milter) | EXCLUDED — ADR-006 lightweight alternative shipped (`webhook/filter_consult.go`) |

## 8. 🚫 Decision gates (do not start without moving the gate first)

| Gate | Blocks | Decision doc |
|---|---|---|
| Tenancy prerequisites (migration plan + demand) | SF-72 tenants, SCIM SF-71, granular roles SF-66 | [`architecture/TENANCY-DECISION.md`](architecture/TENANCY-DECISION.md) |
| OAuth-provider decision (SF-37) | SF-68 grant/session admin | [`architecture/EVALUATIONS.md`](architecture/EVALUATIONS.md) |
| ADR-009 amendment | SF-76 SQLite backend, SF-106 failover | [`architecture/ADR-009-deployment-topology.md`](architecture/ADR-009-deployment-topology.md) |
| go-smtp fork decision | SF-01 VRFY | tracker row 20 |
| Product decisions | RF-F100 tracking toggle, RF-F101 Contact Lists, RF-F073/074 (via DOI) | BACKLOG entries |

## 9. Suggested working order

1. **Land SF-25** (in flight — commit + ledger hash, its programme's flow).
2. **Tracker order** §3: SF-26 first (its dependency just landed), then SF-57, SF-63, SF-16, SF-43.
3. **P0 registry items**: RF-F048 (GAP-01), RF-F099 (GAP-02), RF-F053 (GAP-02 residual), GAP-11 editor gate.
4. **Registry In Progress completions** §4 — smallest residuals first (RF-F104 retry endpoint, RF-F040 handler registration, RF-F081).
5. **RF-F075 DOI** to unlock RF-F073/RF-F074.
6. Everything else by BACKLOG priority (P1 → P2 → P3).

## 10. Known ledger drift (fix opportunistically, don't trust blindly)

- `project/BACKLOG.md:12` header says "In Progress × 12"; the file carries 17 In-Progress rows.
- `project/PRIORITIES.md` §3 still lists ~9 items as top work that are Done (RF-F006, GAP-03, RF-F091, mailbox/IMAP/webmail, RF-F102, GAP-49, RF-F097, GAP-47, GAP-52, JMAP eval).
- `project/FEATURE_GAP_ANALYSIS.md` is frozen at 2026-11-06 — ~12 of its 76 gaps have since closed.
- `project/PROJECT_OVERVIEW.md` cites the 2026-11-06 registry generation (74/9/22 @105 — three generations old).
- `todo.md` Track B checkboxes lag reality in places (GAP-44 `:547`, GAP-51 `:551` unflipped though closed; the 14 lifecycle-design checkboxes at `:399` unflipped though MESSAGE-LIFECYCLE.md is Accepted).
- `audits/FINAL_STALWART_FEATURE_VERIFICATION.md` §3 tally line lags its own table rows for the four 2026-11-14 flips (the file's own concurrency note at `:18` records this).

## 11. Where the evidence lives

- Per-record implementation evidence (path:line): [`audits/FEATURE_PARITY_AUDIT.md`](audits/FEATURE_PARITY_AUDIT.md) (123 records, Categories A–M + CA appendix).
- Verification-wave evidence: [`audits/verification/`](audits/verification/) (V-A1…V-M, V-R, V-DOCS — 2026-11-07 snapshots).
- Status-change history: [`project/STATUS_HISTORY.md`](project/STATUS_HISTORY.md) (append-only, one entry per re-baseline).
- Programme narratives: [`implementation/ROADMAP_IMPLEMENTATION.md`](implementation/ROADMAP_IMPLEMENTATION.md) (R-01..R-28, complete) and [`audits/FINAL_VERIFICATION_REPORT.md`](audits/FINAL_VERIFICATION_REPORT.md) (Option B OB-0..OB-6).
- Screenshots (visual acceptance): [`screenshots/`](screenshots/) (24 shots + index).
