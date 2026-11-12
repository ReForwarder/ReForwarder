# PARTIAL Feature Completion Tracker

**Programme:** complete all 26 PARTIAL records from `docs/audits/FINAL_STALWART_FEATURE_VERIFICATION.md` ·
**Started:** 2026-11-13 · **Baseline:** `919fadb` · **Vocabulary:** `PARTIAL → IN PROGRESS → IMPLEMENTED → TESTING → VERIFIED → COMPLETE` (a record moves to COMPLETE only after independent verification + commit + push)

Source evidence per record: `docs/audits/FINAL_STALWART_FEATURE_VERIFICATION.md` (the mandated table) and `docs/audits/FEATURE_PARITY_AUDIT.md`. Original IDs and source references are preserved verbatim.

## Execution order (dependency-driven)

| # | Feature ID | Feature | Remaining scope (from the verification document) | Depends on | Status | Commit |
|---|---|---|---|---|---|---|
| 1 | SF-08 | Persistent multi-queue manager | per-recipient retry/notify schedules + message expiry (bounded spec on record) | — | PARTIAL | — |
| 2 | SF-09 | Future release / DELIVERBY / DSN params | DELIVERBY honoring via the SF-08 expiry machinery; FUTURERELEASE protocol param (go-smtp limitation documented) | SF-08 | PARTIAL | — |
| 3 | SF-02 | ESMTP extension advertisement | REQUIRETLS honoring (submission parse + outbound enforcement) | — | PARTIAL | — |
| 4 | SF-25 | DMARC aggregate reports: send side | daily cron → aggregate XML → DNS-discovered rua dispatch (mirrors tlsrpt_report.go) | — | PARTIAL | — |
| 5 | SF-26 | ARF auth-failure reports (RFC 5965/6591) | ARF generation riding the SF-25 skeleton | SF-25 | PARTIAL | — |
| 6 | SF-57 | DMARC/TLS/ARF report admin objects | ARF report admin objects (TLS-RPT objects already landed) | SF-26 | PARTIAL | — |
| 7 | SF-63 | DMARC/SPF/DKIM report settings | per-domain rua/report-interval settings surface (needs SF-25 send side) | SF-25 | PARTIAL | — |
| 8 | SF-16 | MTA hooks: per-SMTP-stage HTTP hooks | async per-stage dispatch (must not delay the SMTP response; ADR-001/002-gated slice) | — | PARTIAL | — |
| 9 | SF-43 | Connection/DoS budgets | per-connection aggregate byte budgets + MTA session reservations (bounded spec items 1+3) | — | PARTIAL | — |
| 10 | SF-65 | Accounts: mail-account aliases/groups | mail-account aliases + group delivery within the mailstore | — | PARTIAL | — |
| 11 | SF-99 | Operational controls + task manager | run history table + manual trigger + scheduler-role gating (bounded spec: additive table + 2 routes + drawer) | — | PARTIAL | — |
| 12 | SF-103 | Expression language unification | one condition-evaluator package unifying the three currencies (refactor spec recorded) | — | PARTIAL | — |
| 13 | SF-50 | Conditional routing/policy engine | transport-policy expressions via the unified evaluator (needs SF-103) | SF-103 | PARTIAL | — |
| 14 | SF-39 | Spam filter analyzer suite | additional analyzer slices beyond the ADR-006 baseline (SF-40/41/42-gated items tracked there) | — | PARTIAL | — |
| 15 | SF-42 | Pyzor, spam traps, rule feeds | ADR-006 amendment + bounded implementations (external-dependency surfaces) | ADR-006 amendment | PARTIAL | — |
| 16 | SF-12 | Envelope rewriting: SRS | SRS scheme for the forward-as-own-domain model (ADR amendment + bounded implementation) | — | PARTIAL | — |
| 17 | SF-14 | Local delivery: per-recipient filter scripts at ingest | ADR-006 amendment + bounded filter-script engine (Sieve-subset per the documented requirement) | ADR-006 amendment | PARTIAL | — |
| 18 | SF-49 | Header transformation: in-band A-R injection | archive-integrity-safe injection path (content-addressed blob rewrite or verdict-view canonicalization decision) | — | PARTIAL | — |
| 19 | SF-86 | ManageSieve server | needs the SF-14 script engine + ADR-004 listener | SF-14 | PARTIAL | — |
| 20 | SF-01 | VRFY/EXPN | VRFY 252 is go-smtp-v0.25.0-hardcoded (owner-level fork decision); EXPN deferred with SF-11 | dependency | PARTIAL | — |
| 21 | SF-60 | Domain CRUD ledger re-word | the alias/catch-all/relay capabilities were delivered by the OB-2 route model — ledger re-word only (per §6 of the verification document) | — | PARTIAL | — |
| 22 | SF-68 | OAuth grant/session administration | gated by the SF-37 OAuth-provider decision (excluded per EVALUATIONS.md) | SF-37 decision | PARTIAL | — |
| 23 | SF-72 | Tenants | gated by TENANCY-DECISION.md prerequisites (migration plan + demand) | ADR-008 | PARTIAL | — |
| 24 | SF-76 | Data-store backends | alternative backends under an ADR-009 decision (bounded: SQLite) | ADR-009 amendment | PARTIAL | — |
| 25 | SF-106 | Multi-node store failover | application-level failover is a documented deployment concern (ADR-009 §5); single-node posture tested | infra | PARTIAL | — |
| 26 | SF-112 | File storage: FileNode + text extraction | EVALUATIONS.md demand-triggered slice (extraction-for-search behind FTS + ClamAV seams) | demand gate | PARTIAL | — |

## Status log (append-only; one line per status change with evidence)

- 2026-11-13 — tracker created from `FINAL_STALWART_FEATURE_VERIFICATION.md`; all 26 rows seeded PARTIAL with their remaining scopes, dependencies, and evidence references. Baseline `919fadb`.
