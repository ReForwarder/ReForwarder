# ReForwarder Product-Roadmap Suite — Index & Executive Overview

> **Doc 1 of 7** in the ReForwarder product-roadmap documentation suite. This README is the central index and executive overview; it adds no new feature IDs, scores, or claims beyond the six sibling documents — where any sibling disagrees with this summary, the sibling governs.

## 1. Purpose

This suite establishes an **evidence-backed baseline of ReForwarder** — what the product has, lacks, and duplicates, measured against 12 open-source / source-available email platforms — **before implementation of OSS feature gaps begins**. Every claim in the suite traces to a frozen feature registry, five category audits of the repo, or twelve competitor dossiers, all dated 2026-11-05. Research into commercial SaaS ESPs (Resend and peers) is **explicitly deferred to a later phase**; this suite covers only the self-hostable OSS field. The suite documents; it does not modify code (see §11).

**The suite at a glance:**

| Artifact | Count | Where |
|---|---|---|
| Competitor projects researched | 12 | `01-oss-feature-inventory.md`, `02-competitor-comparison.md` |
| Normalized feature registry IDs | 199 (10 categories) | frozen v1.0.0; scheme in `01-oss-feature-inventory.md` |
| ReForwarder registry verdicts | 108–109 IMPLEMENTED / 46 PARTIAL / 38 MISSING / 2–3 NV / 4 N/A | `03-existing-feature-audit.md` §8 |
| Gap register | 53 gaps (P0×5, P1×10, P2×22, P3×16) | `04-feature-gap-analysis.md` |
| Duplicate/redundancy findings | 9 (D1–D9) | `04-feature-gap-analysis.md` §4 |
| Architecture risks / verified strengths | 16 risks (4 HIGH) / 10 strengths | `06-architecture-impact.md` §2.5, §3 |
| Remaining roadmap items | 52 (S 15 / M 32 / L 4 / XL 1), 4 phases | `05-implementation-roadmap.md` §8, §10 |

## 2. Research scope — the 12 projects

All 12 researched 2026-11-05, web-first (GitHub API metadata, raw LICENSE/code files, official docs; no clone verification for most). Codes are used throughout the suite. Licenses verified per project from LICENSE files / APIs.

| Code | Project | Repo | License (SPDX) | Self-hosting verdict | Maintenance note (2026-11-05) |
|---|---|---|---|---|---|
| LM | Listmonk | github.com/knadh/listmonk | AGPL-3.0 | Fully self-hostable (single binary + PostgreSQL) | Active; v6.2.0 stable + v7.0.0-beta |
| BM | BillionMail | github.com/Billionmail/BillionMail | AGPL-3.0 | Fully self-hostable (compose; full mail server) | CAUTION — apparent stall (~11 months, no releases) |
| PO | Postal | github.com/postalserver/postal | MIT | Fully self-hostable (Docker + MariaDB; operator runs SMTP) | Maintained again after ~20-month dormancy; security-focused releases |
| US | useSend | github.com/usesend/useSend | AGPL-3.0 | Verified self-hostable; hard AWS SES + SNS dependency | Active beta, ~monthly |
| NU | the reference workflow platform | github.com/the reference workflow platform/reference-workflow | BUSL-1.1 (v40+; ≤v39 permanently AGPL-3.0) | Verified self-hostable; free tier runs without license key | Very active, single-vendor; PRs not accepted |
| SP | SendPortal | github.com/mettle/sendportal | MIT | Fully self-hostable (PHP; manual ops) | Active but slow; one major line per 12–18 months |
| KE | Keila | github.com/pentacent/keila | AGPL-3.0 (proprietary `extra/` carve-out) | Fully self-hostable, production-documented | Resumed after 2025 gap; bus factor 1 |
| MC | MailCheap | github.com/marclou/mailcheap | AGPL-3.0 | Self-hostable but mandatory third-party SaaS (Upstash QStash, MongoDB, SES); no air-gapped path | Very active but unconventional: no releases, issues disabled |
| SE | Sessy | github.com/marckohlbrugge/sessy | Source-available, non-OSI (O'Saasy, `NOASSERTION`) | Fully self-hostable by design; anti-SaaS clause | No releases (rolling `:main`); single maintainer, early stage |
| HR | Hyvor Relay | github.com/hyvor/relay | AGPL-3.0 (€10k/yr enterprise escape) | Fully self-hostable — self-hosting is the primary mode (native MTA + built-in DNS) | Active, pre-1.0, ~monthly |
| WR | Wraps | github.com/wraps-team/wraps | AGPL-3.0 core + proprietary EE directories | Self-host control plane documented but **licence-gated** (`wraps_lic_` key required) | Weekly CLI releases; low adoption |
| HP | Hyvor Post | github.com/hyvor/post | AGPL-3.0 | **NOT self-hostable in a supported way** (vendor README/FAQ; contradictory docs) | Active, young; rapid 0.0.x patch trains |

Self-hosting verified for 10 of 12; the four special cases (SE, WR, MC, HP) are detailed in `01-oss-feature-inventory.md` §5. License families group as: MIT (PO, SP); clean AGPL-3.0 (LM, BM, US, MC, HR, HP — HR sells an AGPL escape from €10k/yr); AGPL + proprietary carve-outs (KE `extra/`, WR EE directories); BUSL-1.1 open-core (NU v40+, 6 licence-gated features); source-available non-OSI (SE, O'Saasy). Full per-family implications: `01-oss-feature-inventory.md` §4.4 and `02-competitor-comparison.md` §2.11.

## 3. Methodology

- **Evidence hierarchy (competitors):** repo (GitHub API metadata, raw LICENSE/raw files, code tree) → official docs → official site → issues/releases → release notes → secondary sources (flagged when used). Applied per project in the 12 dossiers feeding `01-oss-feature-inventory.md`.
- **Verification rules:** every competitor capability carries a status flag (✓ ◐ ▲ E H ✗); **unverified items are omitted rather than guessed**, so a blank cell never implies absence. On the ReForwarder side, no verdict of IMPLEMENTED rests on UI-only evidence — five category audits (A–B, C–D, E–F, G–H, I–J) traced **route → handler → service → model/repo → test** for every non-MISSING entry, with file:line citations in `03-existing-feature-audit.md`; the repo's own seed capability audit was re-verified claim-by-claim and its corrections folded in. Capabilities without a verdict carry NEEDS_VERIFICATION rather than a guess, tallied in the `04-feature-gap-analysis.md` §5 register.
- **Commit-cutoff rule:** ReForwarder statuses reflect **committed HEAD only** at the pinned baseline; uncommitted working-tree code is excluded from every status verdict and described separately (03 §2.10), so the suite never counts unbuilt work as shipped.
- **Conflict resolution:** where audit files disagreed, the later audit's verified evidence governs, and every resolution is recorded (03 §8.2 — e.g., route count corrected to 340, cron count corrected to 10, bounce-webhook semantics corrected to the stricter evidence).
- **Registry as single source of truth:** 199 normalized feature IDs across 10 categories (registry v1.0.0, frozen 2026-11-05; INF 36, DOM 13, CON 20, CAM 18, TPL 22, ANA 16, AUT 18, API 19, SEC 18, OBS 19). IDs are permanent — later docs reference, never renumber; micro-features found in dossiers are described under the nearest registry entry, and no new IDs were invented.

## 4. Current ReForwarder architecture (baseline summary)

Per `03-existing-feature-audit.md` §2 and `06-architecture-impact.md` §2, at HEAD `a6f1681`:

- **Modular monolith** — one Go codebase, one binary family (`server`, `worker`, `doctor` under `cmd/posta`); package-per-domain boundaries under `internal/`; route → handler → service → repository layering. No microservices are proposed anywhere in the suite.
- **Stack:** Go 1.27 + okapi + GORM/PostgreSQL 17 + Redis 7/asynq + robfig/cron + emersion/go-smtp + Prometheus; Vue 3.5 + TypeScript + Vite + Pinia dashboard embedded into the Go binary at build.
- **Scale:** ~340 API route definitions under `/api/v1` (~13 route files); 33 backend service packages; ~55 GORM models; 10 cron jobs; 106 committed Go test files; 2 frontend spec files.
- **Delivery:** relay-only outbound over upstream SMTP relays (`net/smtp` client) with DKIM signing + two-phase key rotation (go-msgauth, committed `a6f1681`), dial timeouts with timeout→transient classification, five retry layers, 5xx-RCPT auto-suppression. Default-off SMTP relay submission (SASL) and inbound go-smtp receiver with webhook ingest.
- **Queues:** asynq weighted queues (`transactional=6 / bulk=3 / low=1`); deterministic TaskID dedup with conflict resolution; campaign batches of 100 with per-campaign SendRate pacing; `queue_reconcile` self-healing cron (committed `a6f1681`).
- **Multi-tenancy:** workspace scoping enforced by construction in middleware; 4-role RBAC (owner/admin/editor/viewer); `psk_` API keys with scopes, CIDR allowlists, expiry; JWT sessions with Redis JTI blacklist; `/admin/*` reachable only via session JWT, structurally not by API keys.
- **Security:** clean-room fail2ban-style engine (16 files, 5,298 LOC; ZSET windows, ban tiers, enforced at TCP/EHLO/HTTP); layered SSRF guard on all outbound HTTP; AES-256-GCM encryption at rest for SMTP and DKIM credentials.
- **Cron:** 10 registered jobs visible at `/admin/crons` — **no leader lock**; every server replica runs all schedules (risk R3).
- **Observability:** 20 Prometheus metric families (gated, default-off), `/healthz` `/readyz`, worker Redis heartbeats with version+commit, per-attempt webhook delivery history.
- **Frontend:** ~90 route paths over 60+ named routes, 35 typed API client modules; specialized editors are CodeMirror 6 (HTML), GrapesJS newsletter builder, and @vue-flow automation builder; charts are hand-rolled SVG (no chart library); 2 spec files only.
- **Deployment & ops:** compose runs `server` + `worker` + `postgres:17` + `redis:7` with healthchecks; 3-stage Dockerfile embeds the dashboard into the Go binary; CI covers build, lint, release, and docs deploy; `doctor` checks workspace-integrity invariants; versioned migration steps in `internal/storage/migration/upgrade/` guard schema evolution.
- **Architecture stance (`06-architecture-impact.md` §1):** modular monolith preserved; the roadmap adds a provider-event ingestion plane, a leader-locked cron, a `Sender` interface seam, and an instrumented analytics spine — all inside the same process. Engine-by-engine recommendations in 06 §4.2; tenant isolation (R16) is verified strong and is the pattern every new route group must keep.

## 5. Feature coverage summary

Per-category × per-status counts from `03-existing-feature-audit.md` §8.1 (registry v1.0.0 frozen totals):

| Category | IMPLEMENTED | PARTIAL | MISSING | NEEDS_REFACTOR | NEEDS_VERIFICATION | NOT_APPLICABLE |
|---|---|---|---|---|---|---|
| A (INF) — infrastructure | 21 | 9 | 5 | 0 | 0 | 1 |
| B (DOM) — domains & DNS | 4 | 4 | 5 | 0 | 0 | 0 |
| C (CON) — contacts & audience | 9 | 5 | 5 | 0 | 0 | 1 |
| D (CAM) — campaigns | 8 | 5 | 5 | 0 | 0 | 0 |
| E (TPL) — templates & editors | 12 | 2 | 6 | 0 | 2 | 0 |
| F (ANA) — analytics & tracking | 8 | 6 | 1 | 0 | 0 | 1 |
| G (AUT) — automation | 14 | 2 | 2 | 0 | 0 | 0 |
| H (API) — APIs & integrations | 9 | 5 | 4 | 0 | 0 | 1 |
| I (SEC) — security & admin | 12 | 3 | 2 | 0 | 1 | 0 |
| J (OBS) — infrastructure & observability | 11 | 5 | 3 | 0 | 0 | 0 |
| **Total (199)** | **108** | **46** | **38** | **0** | **3** | **4** |

**Reconciliation:** SEC-016 (credential encryption) moved NEEDS_VERIFICATION → IMPLEMENTED at synthesis, making the **effective total 109 IMPLEMENTED / 2 NEEDS_VERIFICATION**; the registry remains the frozen reference and the table above the frozen record.

**Interpretation.** ReForwarder is strong where delivery platforms must be boring and correct: email infrastructure, APIs, security, automation, and observability carry the highest implemented ratios. Campaigns, audience, and templates are mid-tier — functional cores with named gaps (DOI, signup forms, test-send, preference center). The thin spots are DNS automation and depth (DOM: 4 implemented), analytics depth (delivery-rate measured SMTP-accept, not delivered — provider-event ingestion landed mid-research as `6d2d7c0`), preference center/signup forms (CON-016/017 missing), and ESP provider breadth (INF-022). Several of these thin spots were being closed by parallel commits during the research window itself (§6, finding 7).

## 6. Important findings

1. **Five P0 gaps** (`04-feature-gap-analysis.md` §1.2): GAP-01 scheduled-email cancel + reconcile coverage (un-cancellable future mail; scheduled rows invisible to `queue_reconcile` → silent lost/double sends in the core money path); GAP-12 enforce-or-remove the dead `two_factor_required` setting (+ recovery codes); GAP-03 multi-replica cron leader lock (all 10 jobs run on every replica; alert/report jobs double-send); GAP-02 delivery/complaint signal integration remainder (core landed in `6d2d7c0`); GAP-11 server-side editor gate on automation write/event-ingest routes (a viewer JWT can create/activate automations).
2. **Four HIGH architecture risks** (of 16 total, `06-architecture-impact.md` §3): R1 automation routes lack a server-side editor gate (`automation_routes.go:35` carries `auth + workspace` only; commit `c0408dc` gated the UI only); R2 `two_factor_required` is enforced nowhere — "a control that silently does nothing"; R3 no cron leader lock — duplicate alerts/reports across replicas; R4 `delivered`/`complained` are dead constants — delivery-rate analytics measure SMTP-accept only and complaint analytics are empty in stock deployments.
3. **Delivery-signal integrity** is the suite's central technical finding: stock deployments had no path from provider webhook to suppression or complaint analytics. The core landed (provider-event ingestion with SES/SendGrid/Postmark/Mailgun adapters, 14-class bounce classifier — `6d2d7c0`); the residual (webhook-path suppression parity, analytics/automation integration) remains GAP-02/GAP-07 work in doc 05.
4. **Duplication:** 9 verified findings D1–D9 (`04-feature-gap-analysis.md` §4). Highlights: D1 dual audience model (`contacts` send-ledger vs `subscribers`) plus a fully unrouted ContactLists surface; D2 three audit-log surfaces over one `events` table; D3 two segmentation engines (legacy `FilterRules` vs guarded `SegmentCondition` — canonical is segments).
5. **License families shape the engineering rules** (`01-oss-feature-inventory.md` §4.4, `02-competitor-comparison.md` §2.11): MIT (PO, SP) is freely referenceable; clean AGPL-3.0 (LM, BM, US, MC, HR, HP) demands **clean-room discipline** — document behaviors, never port code; AGPL + carve-outs (KE `extra/`, WR EE) make those directories off-limits beyond documented behavior; NU's BUSL-1.1 code (v40+) is reference-only documentation, with the ≤v39 AGPL history as the safe open reference; SE is source-available non-OSI.
6. **Verified strengths worth preserving** (10, `06-architecture-impact.md` §2.5): layered SSRF guard; clean-room security engine; guarded segment SQL builder with injection tests; atomic scheduler CAS claim; queue administration + `queue_reconcile`; crypto at rest; webhook per-attempt history; multi-tenancy by construction; retention breadth + GDPR tooling; operational hygiene (heartbeats, doctor, health checks).
7. **In-flight work was committed during the research** and is accounted for per document: research pinned `a6f1681`, but three commits landed on 2026-11-05 (`347ebb4`, `a6f1681`, `6d2d7c0`). 03 §2.10 excludes the in-flight work from statuses; 04 §3 keeps the register frozen at the synthesis baseline; 05 §1.1 + §7 reconcile per item — GAP-39 closed outright, GAP-02/07/18/28 reduced to residuals, 52 items remaining.
8. **Not-recommended strategies are recorded so they stay dead** (15 registry entries in 04 §6 plus `05-implementation-roadmap.md` §9): MTA/direct-to-MX, full mail server/IMAP/webmail, license-gated copies (the reference workflow platform forks/feature mimicry, Listmonk code), strict priority queues, SAML/SCIM, recurring campaigns, MJML/React Email/spintax/AI authoring, and more.
9. **The audience/consent layer is the biggest marketing gap cluster:** 15 marketing-class gaps (GAP-25…39) including double opt-in, signup forms, subscriber tags, bulk contact ops, preference center — mostly P2/P3, concentrated in Phases 2–3.
10. **Testing asymmetry:** 106 Go test files against 2 frontend spec files (risk R12); regressions in complex UI reach production undetected.

## 7. Documentation navigation

| File | Purpose | When to read |
|---|---|---|
| `01-oss-feature-inventory.md` | 12 project profiles + the full 199-entry normalized inventory with per-project flags | Needing per-project feature evidence, the ID scheme, or project codes; license/self-hosting detail (§4.4, §5) |
| `02-competitor-comparison.md` | Cross-project matrices over all 10 categories; licensing, maintenance, architecture comparison; relevance verdicts for ReForwarder | Asking "who has X and how"; competitor evidence for any gap |
| `03-existing-feature-audit.md` | ReForwarder's actual current state: every registry entry with file:line evidence, architecture prose, status tables | Verifying what ReForwarder has today and where; the architecture summary (§2) |
| `04-feature-gap-analysis.md` | The gap register: 53 gaps with competitor evidence and file-level proof; 9 duplicates D1–D9; NEEDS_VERIFICATION register; not-proposed lists | Understanding any gap's evidence, approach, and acceptance criteria |
| `05-implementation-roadmap.md` | **The execution document**: scored, phase-ordered plan (52 remaining items), already-delivered record, not-recommended list | Before and during any implementation work — start here, respect its sequence |
| `06-architecture-impact.md` | AS-IS architecture, 16 risks, target architecture, engine-by-engine recommendations, migration consequences | Before touching architecture-sensitive surfaces; understanding what must not break |

Reading order for a full pass: 01 → 02 → 03 → 04 → 05 → 06.

**Suite conventions** (enforced in every sibling's header):

- Every capability reference uses the permanent registry ID (`INF-001`, `CAM-008`, …); gaps use stable `GAP-nn`, duplicates `D1–D9`, architecture risks `R1–R16` — none are ever renumbered. (Note: architecture risks `R1–R16` are a different series from the repo's own implementation-tracker IDs `R-01…R-22` — 05 §"Gap-ID note".)
- Competitor flags are always the six-value legend from `02-competitor-comparison.md` §0; project codes are the two-letter codes from §2 above.
- All ReForwarder claims are dated to the pinned baseline and cite file:line evidence from the audit files; where this README and a sibling disagree, the sibling governs.

## 8. Implementation roadmap overview

Per `05-implementation-roadmap.md`:

- **Scoring formula** (six 1–5 dimensions, verbatim): `score = 0.30*SR + 0.25*II + 0.20*UV + 0.10*CA − 0.10*CX − 0.05*AD`, with a **P0 override**: an item drafted P0 stays P0 regardless of numeric rank (GAP-11 at score 2.85 sits in Phase 0 beside the 3.40 GAP-01).
- **Shape:** four phases — **Phase 0** trust & correctness hardening (5 items, fully parallel, no intra-phase edges), **Phase 1** signals & sender value (10 items, 6 parallel tracks), **Phase 2** parity batches (22 items, 5 batches), **Phase 3** long tail (15 items, 4 groups) = **52 remaining items** (15 S, 32 M, 4 L, 1 XL — the XL is GAP-04, the ESP provider seam, started early in Phase 1).
- **Top-5 P0s:** GAP-01, GAP-12, GAP-03, GAP-02 (residual), GAP-11 — see finding 1 above; full records in 05 §3.
- **Structural hard blockers called out in 05 §8:** GAP-02 → GAP-06/GAP-07 (signal source gates complaint-rate gates and analytics); GAP-04's `Sender` seam → GAP-44 and all ESP-specific value; GAP-08 → GAP-10 (DNS drift re-verification before strict policy enforcement); GAP-26 → GAP-34 (preview before blocks/slots); GAP-25 → GAP-30/GAP-35 (DOI before forms and preference center); product-decision gates on GAP-15 (tracking-toggle intent) and GAP-36 (verify-first).
- **Already-delivered record:** 05 §7 tabulates the ten research-window tracker items (R-01, R-02, R-03, R-04, R-05, R-06, R-08, R-09, R-21, R-22) with their commits and test evidence; GAP-39 is the one fully closed gap.
- **Not-recommended list:** 05 §9 — do not implement MTA/direct-to-MX, IMAP/webmail, or license-gated copies; the full exclusion table governs scope so later work cannot resurrect it.
- **Architecture consequences:** 06 §4 — monolith preserved; provider-event plane, leader-locked cron, `Sender` seam, analytics instrumentation; migration and compatibility analysis per change.

## 9. Research limitations

- **Date-of-research + active parallel development:** all facts are as of 2026-11-05, and the repo moved under the research — three commits landed mid-window (`347ebb4`, `a6f1681`, `6d2d7c0`). Each document accounts for this separately (03 §2.10, 04 §3/§7, 05 §1.1/§7/§11); the `6d2d7c0` DONE/residual markings rest on commit-scope reconciliation, not a full re-audit at the new HEAD, which 04 §5 (I-J NV-6) requests before residual scopes are trusted.
- **Web-first competitor research:** no source-level clone verification for most projects; behavior-level claims should be re-verified against a clone before engineering decisions rely on them (01 §5). Competitor NEEDS_VERIFICATION tally: **115 items across the 12 dossiers** (01 §5 item 2).
- **NEEDS_VERIFICATION carryover (ReForwarder):** the **31-row register in 04 §5** (open / folded-into-gaps / resolved / landed-pending-re-audit); folded items resolve when their gap's acceptance criteria are met.
- **Registry granularity:** 199 IDs normalize a larger feature surface; micro-features are folded under the nearest registry entry and unverified items are omitted rather than guessed — absence of a flag never implies absence of a feature.
- **License/self-hosting nuances:** SE (source-available, anti-SaaS), WR (licence-gated self-host), MC (mandatory third-party SaaS), HP (not supported for self-hosting) need the special handling in 01 §5; HP's own docs contradict themselves on self-hosting.
- **Dossier-scope asymmetry:** flag semantics differed slightly across the 12 competitor dossiers and were normalized to the `02-competitor-comparison.md` §0 legend; in some dossiers "absent" means "not documented" rather than "confirmed absent from code" (01 §5 item 5).
- **Fast-moving projects:** WR ships ~weekly, HP runs 0.0.x patch trains with empty release notes, MC has no releases (rolling git), SE ships rolling `:main` images, BM may have stalled entirely — where the suite and a live primary source disagree at a later date, re-check the source (01 §5 item 4).
- **Research-material provenance:** the raw material (12 competitor dossiers, 5 category audits, `registry.md`, `synthesis.md`) is preserved **outside the repo** in workspace scratch (`.zcode` tmp). This suite is self-contained for decisions; the raw material is working notes, not part of the repository.

## 10. Last research date

**2026-11-05** — all competitor facts, repo evidence, scores, and reconciliation calls are as of that date. Baseline commits enumerated: research pinned **`a6f1681`** ("Add SMTP timeouts, DKIM signing, and delivery-queue administration", parent `347ebb4` "Implement scheduled-campaign scheduler and fix list segment preview"); **`6d2d7c0`** ("Add provider event ingestion, A/B winner logic, timezone sending, and webhook replay", 36 new tests) landed during the suite's production and is current HEAD. Anything merged after 2026-11-05 is not accounted for; repeat the per-item reconciliation before each implementation phase starts.

## 11. Governance note

This suite **documents only** — no file outside `docs/product-roadmap/` was created or modified in its production, and no git state was changed. Implementation phases must start from **`05-implementation-roadmap.md`**, respecting its phase sequence, dependency edges, the P0 override, the not-recommended list (05 §9), and the per-gap acceptance criteria inherited from `04-feature-gap-analysis.md`. Registry IDs are permanent: reference, never renumber.
