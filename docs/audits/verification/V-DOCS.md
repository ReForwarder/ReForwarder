# V-DOCS — Documentation structure verification evidence

**Auditor:** coordinator · **Date:** 2026-11-07 · **Baseline HEAD:** `482d10b`

## Docusaurus production build (baseline)

- Environment: Windows host, Node 24 (site requires >=20), `docs/` working directory.
- First attempt: `npm run build` failed — `'docusaurus' is not recognized` (no local `node_modules`; CI installs via `npm ci`).
- `npm ci --no-audit --no-fund` → 1399 packages installed.
- `npm run build` → **exit 0**: `[SUCCESS] Generated static files in "build"`.
- Note: two packages had install scripts blocked by the host's allowScripts policy (`@swc/core` postinstall, `core-js` postinstall) — the build succeeded regardless, matching CI behavior where no allowScripts gate exists.

## Structure sweep (summary; full analysis in docs/DOCUMENTATION_STRUCTURE_AUDIT.md)

- Markdown census across the repo (excluding node_modules/.git/docs/build): every Markdown directory has a distinct non-overlapping purpose; **no duplicated documentation directories**.
- `docs/docs/` = Docusaurus default content root (no `path` override in `docusaurus.config.ts:37-51`), 78 files / 17 topic dirs — framework-required, preserved per user decision 2026-11-07.
- Non-site folders (research, product-roadmap, project, implementation, audits, security-research) are excluded from the site build by design; `docusaurus.config.ts`/`sidebars.ts` reference only `docs/docs/` + `docs/src/`.
- CI references to `docs/` checked: `deploy-docs.yml` uses only `docs/**` (trigger), `docs/package-lock.json` (cache key), `docs/build` (artifact). No other workflow/script touches `docs/` paths.
- `docs/README.md` scaffold boilerplate replaced with a real structure guide (site vs repo-docs areas, commands, deployment, conventions).

## Post-change build

To be re-run after the Phase 6 reconciliation edits; result recorded in `FINAL_VERIFICATION_REPORT.md`. Baseline is green, so any post-change failure is attributable to the edits themselves.
