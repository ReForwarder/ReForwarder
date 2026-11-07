# Documentation Structure Audit

**Audit date:** 2026-11-07 · **Baseline HEAD:** `482d10b` · **Programme:** Stalwart parity audit, Part 6 (documentation structure) · **Decision authority:** user directive of 2026-11-07 ("Keep `docs/docs/` unchanged because it is required by the existing Docusaurus configuration…").

## 1. Why `docs/docs/` exists — and why it is required

The documentation website's root **is** `docs/` itself: `docs/package.json` defines Docusaurus 3.10.2 (`@docusaurus/core`), and `docs/docusaurus.config.ts` loads the classic preset with **no `path` override** for the docs plugin:

```ts
// docs/docusaurus.config.ts:37-51
presets: [
  ['classic',
    {
      docs: {
        sidebarPath: './sidebars.ts',
        editUrl: 'https://github.com/ReForwarder/ReForwarder/tree/main/docs/',
        routeBasePath: 'docs',
      },
      blog: false,
      theme: { customCss: './src/css/custom.css' },
    } satisfies Preset.Options,
  ],
],
```

With no `path`, the Docusaurus docs plugin uses its default content root: **`<site root>/docs`** — i.e. `docs/docs/`. That directory holds the entire user documentation corpus: **78 Markdown/MDX files across 17 topic directories** (`admin`, `analytics`, `campaigns`, `contacts`, `email-sending`, `gdpr`, `getting-started`, `inbound`, `messages`, `sdks`, `security`, `smtp-domains`, `subscribers`, `templates`, `tracking`, `webhooks`, `workspaces`). The site chrome lives in `docs/src/` (landing page, CSS) and `docs/static/` (17 assets).

CI depends on this layout: `.github/workflows/deploy-docs.yml` runs `npm ci` + `npm run build` with `working-directory: docs` and uploads `docs/build` to GitHub Pages (`url: https://reforwarder.github.io`, `baseUrl: /ReForwarder/`, `onBrokenLinks: warn`).

**Verdict: framework-required, intentional, preserved.** Relocating the site root or renaming the content directory would touch the config, the CI workflow, `editUrl`, sidebar IDs, and every in-site link for zero functional benefit — rejected per the user's acceptance condition ("Never break existing documentation, links, builds, or deployment to satisfy an arbitrary folder-nesting preference").

## 2. Sweep for accidental or duplicate documentation directories elsewhere

Method: recursive markdown census across the repository (`find . -name "*.md"` excluding `node_modules`, `.git`, and `docs/build`), then classification of every directory that contains Markdown.

| Location | Files | Classification |
|---|---|---|
| `docs/docs/**` (17 topic dirs) | 78 | **Framework-required site content** — preserved (§1) |
| `docs/project/` | 10 | Repo-audit ledgers (RF-F registry, backlog, status history, overview docs) — intentional, referenced by `AGENTS.md` §1/§3 |
| `docs/security-research/` | 9 | Historical security research (Stalwart v1.0.1 deep-dive Reports A–G + commit analysis) — intentional |
| `docs/product-roadmap/` | 8 | 12-OSS research suite — intentional, indexed by its own README |
| `docs/research/` | 5 | Stalwart research (authoritative feature universe) + Plunk segments analysis — intentional |
| `docs/implementation/` | 2 | R-implementation tracker + route-contract golden file — intentional |
| `docs/audits/` | new (this programme) | Parity audit, source register, verification wave evidence — created by this audit |
| repo root `.md` | 7 | `README.md`, `AGENTS.md`, `todo.md`, `DEVELOPMENT_HISTORY.md`, `UPSTREAM_BASELINE.md`, `SECURITY.md`, `REFORWARDER_PRE_DEVELOPMENT_AUDIT.md` — each has a distinct root-level purpose (project readme, standing agent instructions, master tracker, history, provenance, security policy, pre-development audit) |
| `docs/` root | 2 | `README.md` (site readme — replaced this audit, see §3), `notifuse-logs-mapping.md` (historical clean-room method record) |
| `web/.agents/skills/**`, `.github/skills/**` | ~200 | Agent-skill definition files, **not documentation** — out of scope by classification |
| `.zcode/plans/**` | 7 | Machine-owned working files, git-ignored |

**Findings:**
1. **No accidentally duplicated documentation directories exist.** Every Markdown directory has a distinct, non-overlapping purpose; no document exists in two places (checked by name across directories; the only shared names are template boilerplate inside `docs/docs/` itself, which is site content).
2. `docs/README.md` was Docusaurus scaffold boilerplate (yarn instructions, no layout description) — **replaced** with a real structure guide (§3).
3. The repository documents are **excluded from the site build** by design: `docusaurus.config.ts` and `sidebars.ts` reference only `docs/docs/` and `docs/src/`; the research/project/product-roadmap/implementation/audits folders are plain repo folders. No cross-contamination, no build risk from them.
4. No CI/script/workflow references any path under `docs/` other than `docs/build` (deploy artifact), `docs/package-lock.json` (npm cache key), and `docs/**` (trigger filter) — verified against `.github/workflows/deploy-docs.yml`.

## 3. Changes made by this audit

| Change | Reason |
|---|---|
| Created `docs/audits/` | Home of the parity programme's ledgers: `STALWART_SOURCE_REGISTER.md`, `STALWART_FEATURE_PARITY_AUDIT.md`, `verification/` wave evidence, `FINAL_VERIFICATION_REPORT.md` |
| Replaced `docs/README.md` | Scaffold boilerplate → real documentation-structure guide (site vs repo-docs areas, commands, deployment) |
| Nothing moved, merged, or deleted | No duplication found; framework layout preserved per the user's decision |

## 4. Build verification

- Baseline build: run after `npm ci` in `docs/` — result recorded in `docs/audits/verification/V-DOCS.md` and in `FINAL_VERIFICATION_REPORT.md`.
- Post-change build: re-run after the Phase 6 reconciliation edits; both must succeed with `onBrokenLinks: warn` (a manual link sweep is still performed — `warn` does not fail the build on broken links).
- GitHub Pages workflow: `deploy-docs.yml` triggers on `docs/**` pushes — the parity-programme commits will exercise it; no workflow changes were needed or made.

## 5. Rules going forward

1. Site content (user documentation that should appear on the Pages site) goes in `docs/docs/<topic>/` and gets a sidebar entry in `sidebars.ts`.
2. Repository-internal documentation (research, audits, trackers, ledgers) goes in the plain folders (`docs/research/`, `docs/audits/`, `docs/implementation/`, `docs/project/`, `docs/product-roadmap/`) and is never added to the Docusaurus build.
3. Never relocate or rename `docs/docs/` without an ADR-level decision that also migrates `deploy-docs.yml`, `editUrl`, and sidebar IDs.
