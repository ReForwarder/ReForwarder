# ReForwarder Documentation

This directory serves two distinct purposes — read this before adding anything.

## 1. The documentation website (Docusaurus)

The site root is this directory (`docs/`). The Docusaurus docs plugin uses its **default content root**, which is why the user documentation lives in the nested **`docs/docs/`** directory — that is framework-required, not accidental. Details and the full reasoning: [`DOCUMENTATION_STRUCTURE_AUDIT.md`](DOCUMENTATION_STRUCTURE_AUDIT.md).

- **Site content:** `docs/docs/<topic>/` — 78 Markdown/MDX files in 17 topic directories (getting-started, email-sending, campaigns, subscribers, templates, contacts, analytics, tracking, messages, inbound, webhooks, sdks, security, smtp-domains, workspaces, admin, gdpr). New site pages go here **and** need a sidebar entry in `sidebars.ts`.
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

These folders are plain repository documentation, deliberately excluded from the Docusaurus build. Put internal documents here — never in `docs/docs/` unless they are user-facing site content.

| Folder | Contents |
|---|---|
| `docs/audits/` | Feature-parity ledgers: [`REFERENCE-PLATFORM_SOURCE_REGISTER.md`](audits/REFERENCE-PLATFORM_SOURCE_REGISTER.md), [`REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md`](audits/REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md) (the definitive 115-feature status ledger), verification wave evidence, final verification report |
| `docs/research/` | the reference platform research suite (feature inventory SF-01..115, gap analysis + §4a ledger, roadmap R-01..28, capability audit) and the the reference segmentation product segments analysis |
| `docs/product-roadmap/` | 12-OSS research suite (199-ID registry, competitor comparison, gap analysis GAP-01..53, implementation roadmap, architecture impact, Option B decision record) |
| `docs/project/` | Audited repo ledgers: RF-F feature registry, backlog, status history, architecture/overview docs, maintenance |
| `docs/implementation/` | R-01..R-28 implementation tracker and the API route-contract golden file |
| `docs/security-research/` | Historical security research (the reference platform deep-dive reports) |

Single-file exceptions at the `docs/` root: `reference-workflow-logs-mapping.md` (historical method record) and `DOCUMENTATION_STRUCTURE_AUDIT.md` (this audit).

## Conventions

1. Feature IDs (SF-nn, R-nn, GAP-nn, RF-Fnnn) are permanent across all documents — never renumber, never duplicate (see `/AGENTS.md` §2).
2. The [`docs/audits/REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md`](audits/REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md) ledger is the single source of truth for feature implementation status; other documents reference it rather than carrying competing statuses.
3. Research on competitors/prior art follows the clean-room discipline in `/AGENTS.md` §7 and lives in `docs/product-roadmap/` or `docs/research/`.
