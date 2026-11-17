# ReForwarder Documentation

**Start here.** This directory has two distinct purposes — the documentation website and the repository-internal ledgers. Every internal document now carries a status banner at the top saying whether it is **current**, **historical**, **delivered**, or a **decision record**, so you can tell at a glance what still describes reality.

## 0. Status at a glance (2026-11-14)

| What | State |
|---|---|
| **Feature registry** (RF-F001…RF-F145) | ✅ 118 Done · 🟡 13 In Progress · ⬜ 14 To-dos — [`PROJECT_STATUS_DASHBOARD.md`](PROJECT_STATUS_DASHBOARD.md) |
| **Reference-platform parity** (SF-01…SF-115 + CA, 123 records) | ✅ 103 COMPLETE · 🟡 1 PARTIAL · 🅿️ 17 PLANNED (ADR-anchored) · ⛔ 1 EXCLUDED · 🚧 1 BLOCKED (SF-72 — the amendment proposal produced, gated on demand) |
| **Live programme** (PARTIAL-completion, 26 records) | ✅ 8 COMPLETE (SF-08, SF-09, SF-02, SF-25, SF-26, SF-57, SF-63, SF-16) · 🟡 17 implemented (SF-43, SF-65, SF-99, SF-103, SF-50, SF-39, SF-42, SF-12, SF-14, SF-49, SF-86, SF-01, SF-60, SF-68, SF-76, SF-106, SF-112) · 🚧 1 blocked with a decision proposal (SF-72) · ⬜ 1 queued (next: CA-02, the sweep's last record) |
| **Relay roadmap R-01…R-28** | ✅ All complete or tombstoned (2026-11-07) |
| **Option B mail-infrastructure programme** (OB-1…OB-6, ADR-001…009) | ✅ All landed; named residuals tracked on the dashboard |
| **GitHub board** ("ReForwarder Backlog") | ✅ Synced + full-coverage-verified 2026-11-14: 102 Feature Done · 20 In Progress · 46 To-Dos · 22 Features Incoming (190 cards, 0 duplicates) — every not-done item in the ledgers has exactly one card; every card opens with a plain-language summary, a status line, and a link to its documentation page (internal planning details collapsed inside the card) |

**👉 The one file to check before working on anything: [`PROJECT_STATUS_DASHBOARD.md`](PROJECT_STATUS_DASHBOARD.md)** — what's done, what's partial, what's untouched, what's decision-gated, and the suggested working order.

**Public-facing documentation** (for the open-source audience): the documentation site carries a plain-language [Features page](https://reforwarder.github.io/ReForwarder/docs/getting-started/features) listing every shipped, in-progress, and planned feature with links into the topic guides, and the GitHub board presents the same picture card-by-card. The root `README.md` carries the feature summary for the repo landing page.

## 1. The documentation website (Docusaurus)

The site root is this directory (`docs/`). The Docusaurus docs plugin uses its **default content root**, which is why the user documentation lives in the nested **`docs/docs/`** directory — that is framework-required, not accidental. Details and the full reasoning: [`DOCUMENTATION_STRUCTURE_AUDIT.md`](DOCUMENTATION_STRUCTURE_AUDIT.md).

- **Site content:** `docs/docs/<topic>/` — 81 Markdown/MDX files in 17 topic directories (getting-started, email-sending, campaigns, subscribers, templates, contacts, analytics, tracking, messages, inbound, webhooks, sdks, security, smtp-domains, workspaces, admin, gdpr). New site pages go here **and** need a sidebar entry in `sidebars.ts`.
- **Site chrome:** `docs/src/` (landing page, CSS) and `docs/static/` (assets).
- **Config:** `docs/docusaurus.config.ts` (site `url: https://reforwarder.github.io`, `baseUrl: /ReForwarder/`, `onBrokenLinks: warn`) and `docs/sidebars.ts`.

### Commands (run from `docs/`)

```bash
npm ci          # install (node >= 20; CI uses node 24)
npm start       # dev server with live reload
npm run build   # production build into docs/build
npm run serve   # serve the production build locally
npm run typecheck
```

### Deployment

GitHub Actions builds and deploys the site: `.github/workflows/deploy-docs.yml` runs `npm ci` + `npm run build` on every push to `main` that touches `docs/**`, then deploys `docs/build` to GitHub Pages. There is no manual deploy step.

## 2. Repository-internal documentation (NOT part of the website)

Plain repository documentation, deliberately excluded from the Docusaurus build. Every file in these folders has a status banner (📌 current · 🗄️ historical · ✅ delivered · 📌🚫 decision-gated):

| Folder | Contents | Currency |
|---|---|---|
| `docs/audits/` | The parity ledgers — [`FEATURE_PARITY_AUDIT.md`](audits/FEATURE_PARITY_AUDIT.md) (**live ledger of record**, 123 SF records), [`FINAL_STALWART_FEATURE_VERIFICATION.md`](audits/FINAL_STALWART_FEATURE_VERIFICATION.md) (mandated verification table), source register, final verification report, provenance audit, `verification/` wave evidence | 📌 live + 🗄️ historical evidence |
| `docs/project/` | Audited repo ledgers: [`FEATURE_INVENTORY.md`](project/FEATURE_INVENTORY.md) (**RF-F registry of record**), [`BACKLOG.md`](project/BACKLOG.md) (operational tasks), [`STATUS_HISTORY.md`](project/STATUS_HISTORY.md) (append-only), gap analysis, priorities, roadmap, overview, maintenance procedure | 📌 live ledgers + 🗄️ stale snapshots (marked) |
| `docs/implementation/` | [`PARTIAL_FEATURE_COMPLETION_TRACKER.md`](implementation/PARTIAL_FEATURE_COMPLETION_TRACKER.md) (**live programme queue**), `ROADMAP_IMPLEMENTATION.md` (R-01…R-28, ✅ complete), `api_routes.txt` (route-contract golden file) | 📌 live + 🗄️ complete |
| `docs/architecture/` | ADR-001…ADR-009 + programme docs (option architecture, component matrix, data ownership, message lifecycle, migration plan, risk register, implementation roadmap, tenancy decision, evaluations) | 📌 current decision records |
| `docs/product-roadmap/` | 12-OSS research suite (199-ID registry, competitor comparison, GAP-01…53 register, implementation roadmap, architecture impact, Option B decision transcript) | 🗄️ historical (2026-11-05 baseline) — superseded by the ledgers above |
| `docs/research/` | Reference-platform research suite (SF-01…115 inventory, gap analysis, R-01…28 roadmap, capability audit) + the segments clean-room analysis | 🗄️ historical — largely delivered |
| `docs/security-research/` | Security deep-dive series (Reports A–G, issue/commit analysis, SSRF audit) | 🗄️ historical — the blueprint it produced **shipped** as the 2026-11-02 security engine |
| `docs/observability/` | Operator guide for metrics/alerts/OTLP/error hook/Grafana (OB-6 W5 artifacts) | 📌 current |
| `docs/screenshots/` | 24 visual-acceptance screenshots + index | 📌 evidence |

Single-file exceptions at the `docs/` root: [`logs-mapping.md`](logs-mapping.md) (historical clean-room method record) and [`DOCUMENTATION_STRUCTURE_AUDIT.md`](DOCUMENTATION_STRUCTURE_AUDIT.md) (structure audit — still current).

## 3. How to find what to work on

| You want to… | Open |
|---|---|
| See everything done / partial / untouched at once | [`PROJECT_STATUS_DASHBOARD.md`](PROJECT_STATUS_DASHBOARD.md) |
| Pick the next live-programme feature | [`implementation/PARTIAL_FEATURE_COMPLETION_TRACKER.md`](implementation/PARTIAL_FEATURE_COMPLETION_TRACKER.md) (dependency-ordered) |
| Check whether a specific RF-F feature is done | [`project/FEATURE_INVENTORY.md`](project/FEATURE_INVENTORY.md) |
| Check a reference-platform SF record | [`audits/FEATURE_PARITY_AUDIT.md`](audits/FEATURE_PARITY_AUDIT.md) |
| See the operational task list | [`project/BACKLOG.md`](project/BACKLOG.md) · GitHub board "ReForwarder Backlog" |
| Understand why something is built the way it is | `docs/architecture/ADR-*.md` |
| Understand the direction (not statuses) | [`project/ROADMAP.md`](project/ROADMAP.md) + root `todo.md` |

## 4. Conventions

1. Feature IDs (SF-nn, R-nn, GAP-nn, RF-Fnnn, RF-Bnnn, D1–D9, NV-*) are permanent across all documents — never renumber, never duplicate (see `/AGENTS.md` §2).
2. The [`docs/audits/FEATURE_PARITY_AUDIT.md`](audits/FEATURE_PARITY_AUDIT.md) ledger is the single source of truth for feature implementation status; other documents reference it rather than carrying competing statuses.
3. Research on competitors/prior art follows the clean-room discipline in `/AGENTS.md` §7 and lives in `docs/product-roadmap/` or `docs/research/`; use neutral terms ("the reference platform"), never competitor names.
4. Ledgers are updated as part of implementing anything (see `/AGENTS.md` §3); statuses move to Done only with recorded evidence per the Done bar in `/AGENTS.md` §4.
5. When you add or re-baseline a document, give it a status banner like the ones introduced on 2026-11-14 and add it to the folder's `README.md` and the tables above.
