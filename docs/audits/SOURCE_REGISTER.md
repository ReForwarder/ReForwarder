# the reference platform Source Register

**Audit:** the reference platform 115-feature parity programme (2026-11-07) · **Baseline HEAD:** `482d10b` ·
**Ledger of record:** [REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md](REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md) ·
**Status vocabulary:** COMPLETE / PARTIAL / MISSING / BROKEN / BLOCKED BY ADR / PLANNED / IN PROGRESS (no silent N/A or DEFERRED — every non-COMPLETE record carries a written technical justification and, where applicable, the governing ADR and Option B phase).

This register catalogs every document that contributes feature requirements or implementation evidence to the parity audit, classifies its authority, and records contradictions between documents. Original documents are preserved verbatim; the parity audit is the single source of truth for final implementation status.

## 1. Authoritative sources (the feature universe)

| Document | Location | Purpose | Records | Authority | Unique requirements not found elsewhere |
|---|---|---|---|---|---|
| the reference platform feature inventory | `docs/research/reference-platform-feature-inventory.md` | Source-evidence catalog of the reference platform v1.0.3 (clone `8b326865`): 115 features SF-01..SF-115 across categories A–M, with per-feature evidence, 15 `[ENT]` markers, 6 in-table negative findings, and a 13-item explicit non-features list | 115 | **Authoritative** (feature universe) | The definitive per-feature requirement statements; the non-features list; the `[ENT]` boundaries |
| the reference platform feature gap analysis | `docs/research/reference-platform-feature-gap-analysis.md` | Master comparison matrix (30 grouped rows), §4a **authoritative per-ID status ledger** (FULL 27 / PART 29 / N-I 18 / N-V 2 / N-A 39 = 115, superseding the original §4 counts), architectural improvement recommendations (§7), do-not-add list (§6) | 115 ledger rows | **Authoritative** (statuses + gap conclusions) | §7's four architecture requirements (API contract-test layer — delivered by the route contract test; `email_events` as the single delivery-timeline substrate; one condition→SQL currency; settings live-apply pattern); the "management in, mail-server out" governing distinction |
| ReForwarder the reference platform roadmap | `docs/research/reforwarder-reference-platform-roadmap.md` | 28 recommendations R-01..R-28 (R-16 tombstoned), priorities P0×2/P1×3/P2×12/P3×8/P4×2, per-item the reference platform refs, dependency map, "explicitly preserved (do not weaken)" list, 12-row do-not-implement table | 28 | **Authoritative** (recommendations + exclusions) | The do-not-implement table with per-row lightweight alternatives; the preservation list (SSRF guard, suppression/eligibility, webhook authz, security-engine enforcement) |
| ReForwarder capability audit | `docs/research/reforwarder-capability-audit.md` | 9-term ReForwarder-side status audit: Part 1 backend (12 subsections), Part 2 frontend/infra, Part 3 summary (~24 subsystem rows) | ~24 subsystem rows | **Authoritative** (ReForwarder-side baseline) | Findings unique to this doc: API-only sending by design (no compose UI); backend-only endpoints without frontend callers (`POST /campaigns/{id}/duplicate`, `GET /emails/{id}/status`, admin api-keys/emails); feature flags invisible to the UI (closed 2026-11-06); no refresh-token rotation; no per-message throttling/SMTPUTF8 negotiation; no VERP/bounce-mailbox; no fallback broker in dev; segment audiences skip per-list unsubscribes by design; single vitest spec at audit time (since grown to 10 files/36 tests) |

## 2. Supporting sources (cross-register reconciliation)

| Document | Location | Purpose | Records | Authority | Notes |
|---|---|---|---|---|---|
| OSS feature inventory | `docs/product-roadmap/01-oss-feature-inventory.md` | 12-OSS normalized registry: 199 IDs in 10 families (INF 36, DOM 13, CON 20, CAM 18, TPL 22, ANA 16, AUT 18, API 19, SEC 18, OBS 19); counts 108 IMP / 46 PARTIAL / 38 MISSING / 3 NV / 4 N-A (reconciled live: 109/2/46/38/4) | 199 | Supporting (frozen 2026-11-05 baseline `a6f1681`) | Statuses stale relative to the 2026-11-07 reality; the parity audit supersedes them per-feature |
| Competitor comparison | `docs/product-roadmap/02-competitor-comparison.md` | 12-OSS platform comparison (LM/BM/PO/US/NU/SP/KE/MC/SE/HR/WR/HP) with verified licenses | — | Supporting | Context only; no per-feature requirements beyond the registry |
| Existing feature audit | `docs/product-roadmap/03-existing-feature-audit.md` | ReForwarder audit against the 199-ID registry at baseline | — | Supporting | Baseline drift accounted per §2.10 |
| Feature gap analysis (OSS) | `docs/product-roadmap/04-feature-gap-analysis.md` | GAP-01..GAP-53 register (P0×5, P1×10, P2×22, P3×16), duplicates D1–D9 with canonicals, risks R1–R16 | 53 | Supporting (cross-register) | Several GAPs delivered since freeze (GAP-02 core via R-04, GAP-08 via R-07, GAP-10 via R-23, GAP-12 via R-10, GAP-39 via R-09, GAP-44 via R-15, GAP-46 via R-20, GAP-50 via R-11); GAP-02/07/18/28 residual sub-checklists tracked in todo.md |
| Implementation roadmap (OSS) | `docs/product-roadmap/05-implementation-roadmap.md` | 52-item relay roadmap with scoring formula and phases | 52 | Supporting (superseded in part by R-01..R-28 delivery) | Track B of todo.md |
| Architecture impact | `docs/product-roadmap/06-architecture-impact.md` | Track B relay-plane target, risks R1–R16, "the monolith stands" verdict, DB/Redis/API policies | — | Supporting | Does NOT define Option B phases — see upcomming-convo §7 |
| Option B decision record | `docs/product-roadmap/upcomming-convo.md` | User's Option B decision transcript: complete email infrastructure; control-plane/data-plane split; 13 logical domains; 7-phase programme (§7 lines 314–377); 4 non-negotiables; 10-row critical operational requirements | 7 phases | **Authoritative for the Option B programme** | Phase→capability assignments used by the parity register's BLOCKED BY ADR mappings |
| Master todo | `todo.md` | Two-track consolidation: Track A = Option B phases 0–6 (ADRs + deliverables as checkboxes); Track B = 52-item relay roadmap with residuals; §0 registry declarations; 13 domain boundaries; "reinstated by Option B" vs "still dead" lists | — | Supporting (programme tracker) | Carries the §0.1 supersession rule for the relay-only exclusions |
| R implementation tracker | `docs/implementation/ROADMAP_IMPLEMENTATION.md` | R-01..R-28 delivery tracker + per-phase verification log + 2026-11-07 acceptance-audit entry (14 verified clean, 13 gap-items fixed) | 28 | Supporting (delivery evidence) | Evidence source for the parity audit's R-delivered records |
| RF-F registry | `docs/project/FEATURE_INVENTORY.md` | Repo-audit feature registry RF-F001..RF-F105 (74 Done / 9 In Progress / 22 To-dos) with RF-F→R cross-refs | 105 | Supporting (AGPL-compliant registry per AGENTS.md §2) | Zero SF- references; RF-F014/015 In Progress vs R-19/R-20 COMPLETE discrepancy to reconcile in Phase 6 |
| Operational backlog | `docs/project/BACKLOG.md` | P0–P3 backlog incl. RF-B1xx Option B umbrella items | — | Supporting | Programme-level; Phase 6 reconciles |
| Status history | `docs/project/STATUS_HISTORY.md` | Append-only decision ledger with test-gate evidence rules | — | Supporting | Phase 6 appends the parity-audit reconciliation entry |
| Other project ledgers | `docs/project/ARCHITECTURE_OVERVIEW.md`, `PROJECT_OVERVIEW.md`, `ROADMAP.md`, `PRIORITIES.md`, `PROJECT_MAINTENANCE.md`, `FEATURE_AUDIT.md`, `FEATURE_GAP_ANALYSIS.md` | Repo-audit outputs | — | Supporting | Phase 6 refresh where stale |

## 3. Historical / contextual sources (no direct requirements)

| Document | Location | Purpose |
|---|---|---|
| the reference platform security deep-dive (7 reports + commit analysis) | `docs/security-research/` | the reference platform v1.0.1 security architecture, IP-blocking verdicts, Report F blueprint (delivered as the 2026-11-02 security engine) |
| Pre-development audit | `REFORWARDER_PRE_DEVELOPMENT_AUDIT.md` | Historical pre-development repository audit (root-level; superseded by `docs/project/` ledgers) |
| Logs & message history mapping | `docs/reference-workflow-logs-mapping.md` | Clean-room method record for the email_events substrate (87cf0d2) |
| the reference segmentation product segments analysis | `docs/research/reference-segmentation-segments-analysis.md` | Clean-room record for the segments feature (fd45e18) |
| Development history | `DEVELOPMENT_HISTORY.md` | Origin-free implementation narrative |
| Product-roadmap README | `docs/product-roadmap/README.md` | Index of the 7-doc suite |

## 4. Recorded contradictions (to resolve in Phase 6)

1. **R-29 phantom** — the gap-analysis header says "28 recommendations R-01…R-29" and §7a says "R-22–R-29 added", but no R-29 exists (highest is R-28). Resolution: header/§7a wording corrected in Phase 6; IDs unchanged.
2. **P4 count drift** — gap-analysis §4 says "P4 × 3" while the roadmap has 2 P4 items (R-19, R-20). Resolution: §4a ledger is authoritative; wording fixed in Phase 6.
3. **"DMARC rua ingestion still dead"** — todo.md's "still dead" list includes DMARC rua ingestion although R-17 delivered it (2026-11-06). Resolution: todo.md corrected in Phase 6.
4. **Stale OSS baseline** — docs/product-roadmap 01/04/05 are frozen at `a6f1681`/`6d2d7c0` (2026-11-05); the R-epic (`347ebb4`…`27c418a`) and the 2026-11-07 acceptance audit have since delivered many GAP items. Resolution: parity audit is the live ledger; doc 04's per-GAP statuses refreshed in Phase 6 with pointers.
5. **RF-F014/RF-F015 In Progress vs R-19/R-20 COMPLETE** — the RF-F registry lags the 2026-11-07 acceptance audit. Resolution: Phase 6 registry refresh with evidence.
6. **SF-64 dual status (historical)** — originally carried two conflicting statuses; resolved to N/I (domain health did not exist) and then to COMPLETE by R-07. No action; recorded for provenance.
7. **Original §4 counts (24/35/16/2/38)** — could not be independently re-derived (three double-listed IDs); superseded by §4a's 27/29/18/2/39. Resolution: §4a authoritative; noted.
8. **Option B vs do-not-implement** — the roadmap's do-not-implement table excluded the mail-server surface under the relay-only strategy; the Option B decision reinstates those capabilities as Track A phases 1–6 (per todo.md §0.1) while keeping the "still dead" list. The parity register encodes the union: mail-server surface → BLOCKED BY ADR with the governing ADR + Option B phase; "still dead" items → the lightweight-alternative record with the do-not-implement citation, classified per the parity audit's evidence rules.

## 5. Register maintenance rules

- Original feature IDs (SF-nn, R-nn, GAP-nn, RF-Fnnn) are permanent — never renumbered, never duplicated (AGENTS.md §2).
- The parity audit is updated after every verification wave and implementation wave; each record's Evidence field cites `path:line` or a command + output.
- No document may carry a feature status that contradicts the parity audit; downstream docs reference it.
