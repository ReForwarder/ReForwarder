# ReForwarder — Pre-Development Audit Report

**Date:** 2026-10-31
**Scope:** Read-only audit and preparation of the ReForwarder repository prior to any UI redesign.
**Upstream project:** Posta — https://github.com/goposta/posta (product name **"Posta"**; **"GoPosta"** is the GitHub organization name, not the product name).
**Distribution:** ReForwarder — https://github.com/ReForwarder/ReForwarder — https://reforwarder.com
**License:** AGPL-3.0-or-later (inherited, preserved)

**Verification methodology.** Findings marked **[Verified]** were confirmed by direct inspection of the cited file at the cited lines. Findings marked **[Inspected]** come from systematic file-by-file inspection during this audit and are cited to their source file. Items marked **[Assumption]** or **[Needs investigation]** could not be fully established from static inspection alone. No builds, tests, migrations, or network fetches were executed. No application code was modified.

**Changes made during this audit (complete list):**

1. `README.md` — replaced with the ReForwarder attribution README (Section 4).
2. `REFORWARDER_PRE_DEVELOPMENT_AUDIT.md` — this report (new file).

Nothing else was created, modified, or deleted. No commits were made; nothing was pushed.

---

## Severity legend

| Level | Meaning |
|---|---|
| **CRITICAL** | Blocks development or distribution; must be fixed immediately |
| **HIGH** | Must be fixed before first commit or before shipping; risks breakage or security exposure |
| **MEDIUM** | Should be fixed soon; concrete risk or legal uncertainty |
| **LOW** | Hygiene / quality issue; fix at convenience |
| **INFORMATIONAL** | Context worth knowing; no action required yet |

---

## 1. Project overview

**[Inspected]** ReForwarder is a modified-and-redesigned distribution of **Posta**, a self-hosted, developer-first email infrastructure platform: outbound transactional/marketing email, inbound email reception, SMTP relay, web-form messages, templates, campaigns, subscriber management, tracking/analytics, and webhooks — exposed through a single HTTP API plus a Vue 3 dashboard, embeddable and self-hostable.

- **Architecture:** single Go binary (`cmd/posta`) serving the HTTP API, an embedded Vue SPA, an optional inbound SMTP receiver, and an optional SMTP relay listener; a Redis-backed asynq worker for async delivery; PostgreSQL for storage.
- **Entry points:** `cmd/posta/main.go` — subcommands `server` (default), `worker`, `doctor`.
- **State of the tree:** this is an unmodified transfer of upstream. **Zero occurrences of "ReForwarder" exist anywhere in the codebase** — no rebranding has started. All project files are inherited from upstream; the only ReForwarder-authored files are the new `README.md` and this report. One local tool artifact (`.mimosa/`, Section 12) is not project content.

## 2. Repository structure

**[Verified]** Top-level layout (all entries inspected):

| Path | Contents |
|---|---|
| `.dockerignore` | Excludes `.git`, `.github`, web/node_modules, web/dist, internal/web/dist — intact |
| `.env.example` | 231-line documented reference for all `POSTA_*` env vars — all values placeholders |
| `.github/` | `FUNDING.yml` (ko-fi: jkaninda), `dependabot.yml` (gomod, weekly), `workflows/` (ci, lint, release, deploy-docs) |
| `.gitignore` | `.env`, `bin/`, `.history`, `.DS_Store`, `/internal/web/dist/*` (keeps `.gitkeep`) |
| `.golangci.yml` | golangci-lint v2 config; one stale exclusion (Section 12) |
| `.mimosa/` | **Local security-scan tool state (contains a ~795 KB source snapshot) — not upstream content; must not be committed** |
| `LICENSE` | Verbatim GNU AGPL-3.0, 661 lines |
| `Makefile` | Go + UI build targets (Section 8) |
| `NOTICE` | "Posta, Copyright 2026 Jonas Kaninda" + AGPL-3.0-or-later notice |
| `README.md` | Upstream "Posta" README — replaced with the ReForwarder attribution README (Section 4) |
| `SECURITY.md` | Upstream vulnerability-reporting policy (email to upstream maintainer) |
| `cmd/posta/` | `main.go`, `server.go`, `worker.go`, `doctor.go` |
| `compose.yml` | Dev compose: `posta` + `worker` services, `postgres:17-alpine`, `redis:7-alpine` |
| `docker/` | 3-stage `Dockerfile` (node UI builder → Go builder → alpine runtime, non-root `posta` user, OCI labels, healthcheck) + `healthcheck.sh` |
| `docs/` | Docusaurus site (~75 markdown pages in 17 sections) with its own `package.json` + `package-lock.json` |
| `examples/` | 3 deployment templates (`.env.example`, single-container `compose.yml`, `docker-compose-full.yml`) |
| `go.mod`, `go.sum` | Module `github.com/goposta/posta`, Go 1.27.1, 27 direct deps; go.sum consistent |
| `internal/` | All backend domain code (~410 files; map in Section 10) |
| `logo.png` | Posta logo, 184 KB — byte-identical copies at `web/public/logo.png` and `docs/static/img/logo.png` |
| `web/` | Vue 3 dashboard SPA (`package.json` + `package-lock.json`, no node_modules checked in) |

**Transfer integrity: INTACT. [Inspected]**

- No `.gitmodules` and no submodule references — none were lost.
- Every file referenced by configs exists: Dockerfile `COPY` targets, `web/index.html` favicon/manifest/logo references, `go:embed` target directory — all resolve.
- No generated directories committed (no `node_modules`, `dist`, `vendor`, `.next`).
- `internal/web/dist/` intentionally contains only `.gitkeep`; the binary embeds `all:dist` (`internal/web/embed.go:39-40`), so a fresh build serves a placeholder until `make build-ui` runs. Expected behavior, not corruption.
- Only intentional zero-byte files: `docs/static/.nojekyll`, `internal/web/dist/.gitkeep`.
- No broken relative paths found; no unexpected generated files other than `.mimosa/` (local tool state).

## 3. Technology stack

**[Verified]** (go.mod, web/package.json, compose.yml read directly)

### Backend
- **Go 1.27.1**, module `github.com/goposta/posta` (go.mod:1)
- HTTP framework/router: `github.com/jkaninda/okapi` v1.1.0 (upstream author's own framework; CLI from `okapicli`)
- ORM/DB: GORM v1.31.2 + `gorm.io/driver/postgres` (pgx v5.11.0); `lib/pq` also present
- Database: **PostgreSQL only** (postgres:17-alpine pinned in compose/CI)
- Cache/queues: `redis/go-redis/v9` + `hibiken/asynq` (queues `transactional`, `bulk`, `low`); cron `robfig/cron/v3`
- SMTP: `emersion/go-smtp` + `go-sasl`; `vanng822/go-premailer`
- Auth: `golang-jwt/jwt/v5`, `coreos/go-oidc/v3`, `golang.org/x/oauth2`, `pquerna/otp` (TOTP 2FA)
- Storage: `aws-sdk-go-v2` (S3-compatible blobs) or filesystem
- Metrics: `prometheus/client_golang`; config: `joho/godotenv`

### Frontend
- **Vue 3.5** + vue-router 5.2 + Pinia 4 + axios 1.13 — SPA, no SSR
- Build: **Vite 8** (Rolldown), TypeScript 5.9; `advancedChunks` splits codemirror and grapesjs into separate chunks (web/vite.config.ts:18-23)
- Editors: grapesjs 0.23 + grapesjs-preset-newsletter (email template builder), CodeMirror 6
- Icons: `@mdi/font` 7.4; fonts: **Inter loaded from fonts.googleapis.com at runtime** (web/index.html:35-37)
- Package manager: **npm** (`web/package-lock.json` present, 90 KB; no yarn/pnpm lockfiles)
- Package name: `dashboard` v0.0.0 (neutral — not brand-locked)
- **No test framework** (no vitest/jest/playwright); only `npm run type-check` (vue-tsc)

### Docs & CI
- Docs: Docusaurus 3.10 (`docs/`), deployed to GitHub Pages via `deploy-docs.yml`
- CI: 4 workflows — `ci.yml` (backend tests with Postgres/Redis service containers, frontend type-check+build, docker build), `lint.yml` (golangci-lint v2.13.2), `release.yml` (v* tags → DockerHub multi-arch via `vars.BUILDKIT_IMAGE`), `deploy-docs.yml`

## 4. README changes

**[Verified]** The root `README.md` was the full upstream Posta README (title `# Posta`, upstream logo from raw.githubusercontent.com, goposta.dev/jkaninda badges, upstream demo/docs/funding links, License and Copyright sections).

It has been **replaced** with the ReForwarder attribution README: product tagline, Upstream Attribution section linking https://github.com/goposta/posta, the AGPL-3.0-or-later declaration, a statement that modifications exist and copyright/license obligations are preserved, and repository-policy notes (LICENSE file, copyright notices, third-party attribution, and nested READMEs must not be casually removed).

The prior upstream README content remains recoverable from the upstream repository (https://github.com/goposta/posta) since no git history exists locally. Nested `docs/README.md` (stock Docusaurus boilerplate) was **not** modified, per audit rules.

## 5. License and attribution findings

**[Verified]** Core license chain is intact and AGPL-compliant:

| Item | Status |
|---|---|
| `LICENSE` | Verbatim GNU Affero General Public License v3, 19 November 2007, 661 lines — unmodified. The license text itself does not state "or-later"; that grant lives in NOTICE, file headers, and the Dockerfile OCI label |
| `NOTICE` | "Posta / Copyright 2026 Jonas Kaninda … GNU Affero General Public License, version 3 or (at your option) any later version" — intact, must be preserved |
| Go file headers | **348/348** files in `cmd/` + `internal/` carry "Copyright (c) 2026 Jonas Kaninda" + AGPL-3 grant (+ `SPDX-License-Identifier: AGPL-3.0-or-later` in many, incl. `docker/healthcheck.sh`) |
| Frontend file headers | **0/160** files in `web/src` carry headers — permitted under AGPL, but new ReForwarder files should adopt a header policy (Section 14) |
| Dockerfile OCI labels | `licenses="AGPL-3.0-or-later"`, `source="https://github.com/goposta/posta"`, `authors="Jonas Kaninda"` — attribution intact |

**Dependency licenses [Inspected, static check only — no downloads]:**

- All identified Go direct dependencies are permissive (MIT / Apache-2.0 / BSD): gorm, pgx, go-redis, asynq, go-smtp, golang-jwt, go-oidc, oauth2, otp, aws-sdk-go-v2, prometheus, godotenv, uuid, robfig/cron, golang.org/x/*.
- **Unverified — needs license confirmation before public distribution:** `github.com/jkaninda/go-utils`, `jkaninda/logger`, `jkaninda/okapi`, `jkaninda/njia` (indirect; upstream author's own modules), and `vanng822/go-premailer`. Not recognized as copyleft, but licenses were not confirmed offline.
- Frontend: vue/vue-router/pinia/axios/codemirror (MIT); grapesjs + preset (BSD-3-Clause); **`@mdi/font` — Pictogrammers Free License (attribution-bearing; a THIRD-PARTY-NOTICES file is recommended)**; Inter font (SIL OFL, currently fetched from Google Fonts at runtime).
- **No vendored or bundled third-party source code** was found in `internal/` or `web/src`; no `vendor/` directory.

**Provenance caveat [Verified]:** the local git history is empty (Section 7). Upstream provenance currently rests entirely on NOTICE and file headers. The upstream commit/tag this transfer was made from is **not recorded anywhere in the repository** — [Needs investigation]: identify and record it (e.g., in this report's successor or a PROVENANCE note) while it is still known.

### Licensing questions requiring human/legal review

1. **NOTICE strategy for the fork** — AGPL requires preserving upstream notices. Decide whether ReForwarder will (a) keep `NOTICE` as-is and add a ReForwarder copyright line for its own modifications, and (b) add a pointer to the ReForwarder source repository (AGPL §13 corresponding-source obligation for network use).
2. **"or-later" inheritance** — upstream grants "version 3 or (at your option) any later version". Confirm ReForwarder distributes under AGPL-3.0-or-later and that this option cannot be stripped from inherited files.
3. **Unverified dependency licenses** — the four `jkaninda/*` Go modules and `go-premailer` (Section 5). Confirm before any public release.
4. **`@mdi/font` attribution** — add a third-party-notices file covering the Pictogrammers Free License; do the same for Inter/OFL if the font is ever self-hosted.
5. **Trademark/endorsement** — confirm "ReForwarder" name availability; decide how residual "Posta" strings (docs, seeded workspace name, update banner) are treated to avoid implying upstream endorsement.
6. **Upstream-pointing operational surfaces** — `SECURITY.md` (reports go to the upstream maintainer's email), `FUNDING.yml` (ko-fi: jkaninda), README "Support the Project" content, and the `POSTA_UPDATE_CHECK` feature phoning `api.github.com/repos/goposta/posta`. Decide: replace, redirect, or disable for ReForwarder.
7. **Data-affecting names** — seeded workspace `SystemName = "Posta System"` (`internal/services/workspace/system.go:33-34`) and the "Welcome to Posta!" notification (`internal/handlers/user_handler.go:376`) write brand strings into user databases; renaming them needs a data-migration plan, not a find/replace.

**No AGPL-redistribution blockers were found.** LICENSE, NOTICE, and headers are intact; no notices were removed; no incompatible bundled assets or vendored code exist.

## 6. Branding migration inventory

**Naming clarification [Verified]:** the upstream **product** is named **"Posta"**; **"GoPosta"** is the GitHub organization (`github.com/goposta`). Domains are `goposta.dev`, `app.goposta.dev`, `docs.goposta.dev`. Searching for "GoPosta" alone would miss most of the brand surface; the inventory below covers "posta"/"goposta"/"jkaninda".

Approximate occurrence volumes (excluding `.git`, `node_modules`, `.mimosa`): **642** `goposta` refs in 231 files (overwhelmingly Go import paths); ~30 "Posta" string sites in Go code; **38** "Posta" hits in `web/src`; **~350+** across `docs/` + old README; `POSTA_` env prefix ~66 vars in `.env.example` and 119 matches in `internal/config/config.go`; 10 distinct `X-Posta-*` HTTP headers. **Zero "ReForwarder" mentions anywhere.**

### Category 1 — Legal attribution (must remain)
- `LICENSE`, `NOTICE` (whole files).
- 348 Go file headers + `docker/healthcheck.sh` header ("This file is part of Posta", Jonas Kaninda copyright).
- Dockerfile OCI attribution labels (`authors`, `source`, `licenses`).

### Category 2 — Internal technical identifiers (do NOT rename without a full compatibility plan)
| Identifier | Location | Consequence of renaming |
|---|---|---|
| Go module path `github.com/goposta/posta` | go.mod:1 + every Go import (642 refs in 231 files), Makefile:10 ldflags, Dockerfile | Mechanical but repo-wide; touches every Go file |
| `POSTA_*` env-var prefix (~185 vars) | `.env.example`, `internal/config/config.go`, compose files, examples, CI | Breaks every existing deployment/env file on upgrade |
| `X-Posta-*` HTTP headers (10 distinct: Workspace-Id, Signature, Inbound-Secret, Internal, API-Key-ID, Campaign-ID, ID, Message-ID, Track-Clicks, Track-Opens) | `internal/middlewares/`, CORS allowlist config.go:467, web/src | Breaks API contract for SDK clients and MX-webhook senders |
| `{{ posta_web_view_url }}`, `{{ posta_mail_web_link }}`, `{{ posta_unsubscribe_url }}` template vars; sentinel paths `/__posta_web_view__`, `/__posta_unsubscribe__` | `internal/services/email/system_vars.go:29-49` | **Embedded in already-delivered emails and saved templates**; renaming orphans live links |
| `posta_version` JSON field | `internal/handlers/template_handler.go:314,367`, `workspace_data_handler.go:98,187` | API response contract |
| `Posta-Webhook/1.0` User-Agent | `internal/services/webhook/dispatcher.go:327` | Receiver-side contract |
| API key prefix `psk_` | `internal/services/auth/api_key.go:37` | Not brand-derived — leave alone |
| Binary name `posta` | Makefile:3,12, Dockerfile paths, ci.yml:50 | Cosmetic but touches Docker/CI |
| Compose service names `posta`, `posta-db`, `posta-redis`; DB user/pass/name `posta` | compose.yml, examples/, ci.yml:23-25 | Local identifiers; renameable at low value |
| Docker image `jkaninda/posta` in examples | `examples/compose.yml:10`, `examples/docker-compose-full.yml` (8 refs) | Examples must point at ReForwarder's own images eventually |

### Category 3 — User-visible branding (rebrand candidates, per-occurrence decisions)
- `web/index.html:8-10,14-15,17-28` — author meta "Jonas Kaninda", application-name/apple-title "Posta", `<title>Posta — Self-Hosted Email Delivery & Inbound Platform</title>`, og:/twitter: tags, og:image `/logo.png`.
- `web/public/site.webmanifest:2-3` — name/short_name "Posta".
- `web/src/router/index.ts:5-6` — `APP_NAME = 'Posta'` and default page title (suffixes every route).
- `web/src/layouts/DashboardLayout.vue:385-386,581,604,624-625` — sidebar/mobile "Posta." wordmark, `/logo-navy.png`, "Posta {{ version }} is available" banner, footer link to github.com/goposta/posta.
- `web/src/views/auth/AuthLayout.vue:33,58` — login/register "Posta." wordmarks; `web/src/views/about/About.vue:9-10,92-106` — "About Posta" + upstream links.
- View copy: `GettingStarted.vue:24,53`, `Dashboard.vue:170`, `Domains.vue:43,47`, `SmtpCredentials.vue:152,176`, `UnsubscribeLists.vue:138`, `Notifications.vue:126,160`, `Settings.vue:261`, `Webhooks.vue:267`, `StatusMix.vue:8`, `Announcements.vue:109`; export filenames `posta-analytics-*.csv` (Analytics.vue:219), `posta-workspace-export-*.json` (WorkspaceSettings.vue:333).
- Backend user-visible: OpenAPI `Title: "Posta API"` + `BearerFormat: "Posta API Key"` (config.go:487,490), contact `https://goposta.dev/` / `jonas@goposta.dev` (config.go:500-501), `/info` returns `Name: "Posta"` (`internal/handlers/info_handler.go:28,37`), "Welcome to Posta!" (user_handler.go:376), seeded "Posta System" workspace (workspace/system.go:33-34).
- Seeder content references upstream docs/help URLs: `internal/services/seeder/seeder.go:230-307`.

### Category 4 — Documentation references
- `docs/docusaurus.config.ts:6,14,17-18,41` — title "Posta", `url: https://docs.goposta.dev`, organizationName `goposta`, projectName `posta`, editUrl to upstream.
- ~75 content pages under `docs/docs/` (heaviest: configuration.md ~106 hits, installation.md ~66, smtp-relay.md ~27, sdks/*, getting-started/managed-posta.md — an upstream commercial offering).
- Old root README (now replaced); upstream badges/links were part of it.

### Category 5 — External links (distinct upstream domains found)
`github.com/goposta/posta` (+issues/blob/actions/raw), SDK repos `github.com/goposta/{posta-go,posta-php,posta-java,posta-node}`, `goposta.dev` / `www.` / `app.` / `docs.` (22 refs), `goposta.discourse.group`, `marketplace.miabi.io/templates/posta` + `miabi.io`, Docker Hub `jkaninda/posta`, `pkg.go.dev/github.com/goposta/posta`, `ko-fi.com/jkaninda`, `mailto:me@jkaninda.dev` (SECURITY.md), `api.github.com/repos/goposta/posta/releases` (update check). No `posta.dev`, no `goposta.com`.

### Category 6 — Branding assets (replacement candidates when visual identity is decided)
- `logo.png` (root, 184 KB) — byte-identical copies at `web/public/logo.png`, `docs/static/img/logo.png`.
- `web/public/logo-navy.png` (188 KB); favicon set: `favicon.ico`, `favicon.png`, `favicon.svg`, `favicon-96x96.png`, `apple-touch-icon.png`, `web-app-manifest-{192,512}x512.png`, `site.webmanifest`.
- `docs/static/img/`: logo.svg (SVG `<title>Posta</title>`, purple #A855F7→#7E22CE gradient), `posta-social-card.png` (651 KB), 7 screenshots (incl. `miabi-posta.png`), plus unmodified Docusaurus template leftovers (`docusaurus.png`, `docusaurus-social-card.jpg`, `undraw_docusaurus_*.svg`).
- Theme color purple `#9333ea` (index.html:6, site.webmanifest:18).
- Referenced in code at: index.html:22-28,32-35, About.vue:104, DashboardLayout.vue:385,624, README (removed with old README).

**Per the audit rules, no branding changes were made and no global replacements were performed.** This inventory is the input for the later rebranding pass.

## 7. Git repository status

**[Verified]** (all commands run read-only)

| Check | Result |
|---|---|
| Repository root | Valid git work tree |
| Branch | `main` — **unborn: zero commits** ("No commits yet on main… origin/main [gone]") |
| Tracked files | **0** — the entire tree is untracked working files |
| History | None — original upstream git history was removed in the transfer (deliberate); nothing to audit for leaked secrets in history |
| Remote | `origin → https://github.com/ReForwarder/ReForwarder.git` (fetch+push) — **matches the expected repository** |
| Submodules | None configured, none lost |
| `.gitignore` | Present and adequate (`.env`, `bin/`, `.history`, `.DS_Store`, `internal/web/dist/*` keeping `.gitkeep`) — but **does not exclude `.mimosa/`** |
| `.gitattributes` | **Does not exist — and is needed** (see HIGH finding below) |
| Git LFS | **Not needed.** Largest tracked-content file is `docs/static/img/posta-social-card.png` at 651 KB; a handful of PNGs exceed 100 KB. All far below any LFS threshold |
| Secrets in tracked files | **None found.** No `.env`, `*.pem`, `*.key`, `id_rsa*`, `.p12/.pfx` anywhere in the tree; nothing is committed yet, so there is also no history exposure |
| GitHub-side configuration (branch protection, repo settings,Actions variables like `BUILDKIT_IMAGE`) | **[Needs investigation]** — not verifiable from a local clone |

**Findings:**

- **HIGH — CRLF line endings repo-wide, no `.gitattributes`. [Verified]** Every inspected text file uses CRLF (README.md: 477 CR lines, go.mod: 91, `docker/healthcheck.sh`: 34/34 lines). If the tree is committed as-is, `docker/healthcheck.sh` (a `#!/bin/sh` script run inside the Alpine runtime container by the Dockerfile `HEALTHCHECK`) will fail with `/bin/sh\r: bad interpreter`. **Fix before the first commit:** add a `.gitattributes` (`* text=auto`, `*.sh text eol=lf`) and let git normalize on add. Nothing is committed yet, so this is still free to fix.
- **MEDIUM — `.mimosa/` must never be committed. [Verified]** It is local security-scan plugin state (hook-state JSON + a ~795 KB baseline snapshot of source files). Add `.mimosa/` to `.gitignore` before the first commit.
- **INFORMATIONAL — Zero-commit state.** The first commit should be made deliberately (after the hygiene fixes above) and should represent the pristine upstream transfer + attribution README. Consider recording the upstream commit/tag it corresponds to in the commit message.

Nothing was pushed, no remotes modified, no history rewritten.

## 8. Development environment instructions (native, no Docker)

**[Verified from code; no installs performed, per audit rules]**

Prerequisites: **Go ≥ 1.27.1**, **Node ≥ 24.11.0** (enforced by web/package.json engines), **PostgreSQL 17**, **Redis 7** — installed natively; Docker is not required for development.

### Backend

```bash
# 1. Prepare local Postgres (db/user/pass "posta"/"posta"/"posta" or your own)
#    and Redis listening on localhost:6379.

# 2. Configure
cp .env.example .env
#   Minimum edits for local dev:
#     POSTA_ENV=dev                 (dev allows the placeholder JWT secret; production refuses it)
#     POSTA_DB_HOST/USER/PASSWORD/NAME  (or POSTA_DB_URL)
#     POSTA_REDIS_ADDR=localhost:6379
#     POSTA_JWT_SECRET=<32+ random chars>   # openssl rand -hex 32
#     POSTA_ADMIN_EMAIL / POSTA_ADMIN_PASSWORD
#     POSTA_EMBEDDED_WORKER=true    (run the async worker inside the server process)

# 3. Run (auto-runs GORM AutoMigrate + upgrade steps + seeding on boot)
go run ./cmd/posta server          # HTTP API + dashboard on http://localhost:9000

# Optional separate worker (only if POSTA_EMBEDDED_WORKER=false and not POSTA_DEV_MODE):
go run ./cmd/posta worker
```

- **`POSTA_DEV_MODE=true`** skips queue/cron/SMTP-listener startup and stores emails without sending (cmd/posta/server.go:162-211) — useful for UI work, **but the server still initializes Postgres and Redis; you cannot boot without both** even in dev mode.
- Config loader: `internal/config/config.go` (loads `.env` via godotenv, else OS env; every var is `POSTA_`-prefixed).

### Make targets (Makefile)

`build`, `build-ui` (npm install+build, stages output to `internal/web/dist` for `go:embed`), `build-all`, `run`/`dev` (server), `run-worker`/`dev-worker`, `dev-ui`, `dev-deps`/`dev-deps-stop` (**Docker-based** dependency starters — not needed for native setups), `test` (`go test ./...`), `vet`, `fmt`, `lint` (golangci-lint), `tidy`, `clean`.

**Windows note [Verified]:** `make build` emits `bin/posta` without `.exe`; on Windows prefer `go run ./cmd/posta …` directly. `make build-ui` uses `rm -rf`/`cp -r` (Unix semantics — available in Git Bash, but untested on this machine [Assumption]).

### Port map [Verified]

| Port | Listener | Default state |
|---|---|---|
| 9000 | HTTP API + embedded dashboard (+ worker health endpoints) | always |
| 3000 | Vite dev server | dev only |
| 2525 | Inbound SMTP receiver | disabled by default (`POSTA_INBOUND_ENABLED=false`) |
| 2526 | SMTP relay listener | disabled by default (`POSTA_SMTP_RELAY_ENABLED=false`) |
| 5432 | PostgreSQL | required |
| 6379 | Redis | required |

## 9. Docker-free frontend development instructions

**Yes — the frontend runs fully independently of Docker and of the Go build. [Verified]** (web/vite.config.ts, web/package.json, web/src/api/client.ts)

```bash
cd web
npm install        # uses package-lock.json (npm ci also works)
npm run dev        # → http://localhost:3000 with HMR
```

- **Dev proxy:** `/api/v1` → `http://localhost:9000`, `changeOrigin: true` (web/vite.config.ts:34-39). The backend URL is hardcoded in the proxy (not env-parameterized); start the Go server on its default port 9000 and the dashboard works as-is.
- **Hot reload:** Vite HMR, enabled by default.
- **Env vars:** only `VITE_API_URL` exists (web/env.d.ts:10) — overrides the axios `baseURL` (client.ts:5). **Leave unset** to use the proxy path.
- **Type-check:** `npm run type-check` (vue-tsc). **Production build:** `npm run build` → `web/dist`.
- **Serve a built UI without rebuilding the Go binary:** set `POSTA_WEB_DIR=web/dist` (config.go:309, routes.go:599-603) — handy during redesign iterations.
- **To embed a rebuilt UI into the binary:** `make build-ui && make build` (stages `web/dist` → `internal/web/dist` for `go:embed`).

**Recommended full local loop (no Docker):** native Postgres + Redis → `go run ./cmd/posta server` (terminal 1) → `cd web && npm run dev` (terminal 2) → open http://localhost:3000.

## 10. Existing application functionality

**[Inspected]** Everything below must remain operational through the redesign. Backend package map (`internal/`):

| Package | Responsibility |
|---|---|
| `config` | env/.env config; production security validation (`security.go`); version |
| `cron` + `cron/jobs` | 8 scheduled jobs: retention, daily report, update check, account cleanup, API-key expiry, bounce alert, campaign restart, message digest |
| `handlers` (~60 files) | HTTP handlers per resource (admin_*, email, campaign, contact, domain, oauth, smtp, template, tracking, webhook, workspace, …) |
| `middlewares` | JWT/API-key auth, workspace scoping, scopes, login rate limit |
| `models` (~55) | User, Plan, Workspace(+Member/Invitation/SSO/Setting), OAuthProvider/Account, APIKey, SMTPCredential, Email, InboundEmail, Template(+Version/Localization), StyleSheet, Language, SMTPServer, Domain, Bounce, Suppression, UnsubscribeList, Contact(+List/Member), Event, Campaign(+Message), Subscriber(+List), Tracking, Message(+Filter), Form, Notification, Session, PasswordReset, Setting, UpgradeStep, … |
| `routes` | Router wiring + SPA mount; auth, admin, workspace, workspace-resource, user, tracking, inbound, message, oauth, webhook-docs route files |
| `services/` (30+ subpackages) | audit, auth (API keys/OAuth), cache, crypto (AES-256-GCM), domain verification, **email pipeline**, emailverify/verifier, eventbus, **inbound (SMTP receiver)**, inbox, **messages (web forms)**, messagescan, notification (15 .tmpl platform emails), passwordreset, plan, ratelimit, realtime (SSE pub/sub), retry, seeder (+ seed templates en/de/fr), session, settings, **smtprelay**, tracking, twofactor, updatecheck, webhook dispatcher, workermon, workspace(+provision) |
| `storage` (+migration, repositories) | Postgres connect, blob storage (S3/FS), **GORM AutoMigrate of 52 models + FK constraints**, versioned upgrade-step framework (5 dated steps, `steps_YYYY_MM_DD_*.go`), ~55 repository files |
| `web` | `//go:embed all:dist` SPA embedding |
| `worker` | asynq handlers: email send, campaign start/batch, inbound parse/process, message process/reply; health server |

**Feature inventory (all present):** authentication (email/password, JWT, TOTP 2FA, password reset, optional email verification, session revoke, account deletion); users + workspaces/teams (members, invitations, roles, SSO, system workspace, plans/quotas); outbound delivery (send/batch/send-template, multiple SMTP servers, retry worker, suppression, bounces); inbound email (built-in SMTP receiver, MX-provider webhook, attachments, retries); SMTP relay (workspace credentials, dedicated listener); custom domains (DNS verification); campaigns (CRUD, send/pause/resume/cancel/duplicate, subscriber lists/segments, imports, analytics); templates (versions, localizations, stylesheets, GrapesJS builder, `{{ }}` variables incl. reserved `posta_*`); analytics/tracking (open pixel, click redirect, unsubscribe pages, web view, dashboards); webhooks (CRUD, delivery logs, retries, proxy); forms/messages inbox (public ingest, spam scan, threading, replies); admin panel (`/api/v1/admin/*` + admin UI + SSE streams); OpenAPI docs at `/docs` + `/openapi.json`; background jobs (asynq + cron); no marketing site in-repo (docs are the separate Docusaurus site).

**API surface (router `internal/routes/routes.go`, all under `/api/v1` unless noted):** `/healthz`, `/readyz`, `/metrics`, `/auth`, `/auth/oauth`, `/emails`, `/emails/verify`, `/webhooks` (+`/bounce`), `/subscriber-lists/{id}/(un)subscribe` (public), `/users/me` (+oauth), `/workspaces` + `/workspaces/current/*` (emails, templates, campaigns, subscribers, contacts, smtp-servers, smtp-credentials, webhooks, api-keys, analytics, dashboard, domains, stylesheets, languages, localizations, events, audit-log, export/import, gdpr, members, invitations, forms, messages, message-filters, inbound-emails, sso), `/invitations`, `/inbound` (webhook), `/f/{key}` (public form ingest), `/admin/*` (+ SSE streams), `/t/*` (public tracking), `/info`, SPA fallback `/`.

### Frontend ↔ backend contracts the redesign must not break

1. **Auth transport:** JWT delivered as **HttpOnly, SameSite=Strict, Secure(HTTPS) cookie** (`internal/services/session/cookie.go:47-49`); axios client uses `withCredentials: true` (client.ts:11). CLI/SDKs use `Authorization: Bearer` with `psk_` API keys.
2. **401 handling contract:** response interceptor redirects to `/login?error=…` unless `requires_2fa` is set (client.ts:14-26). The 2FA handshake is part of this contract.
3. **Workspace scoping:** `X-Posta-Workspace-Id` header (CORS-allowed, config.go:467) + `posta_workspace_id` in localStorage (workspace store).
4. **Realtime = SSE, not WebSocket:** `EventSource({ withCredentials: true })` on `/api/v1/admin/events/stream`, `/api/v1/admin/metrics/stream`, `/api/v1/workspaces/current/inbound-stream`, `/api/v1/workspaces/current/message-stream`.
5. **Embedded-SPA contract:** Vite SPA built into `internal/web/dist` and embedded via `//go:embed all:dist`; Go serves `/` with SPA fallback, excluding `/api`, `/healthz`, `/metrics`, `/docs`, `/openapi.json` (routes.go:599-603). A redesign must keep producing a Vite SPA at the same output paths (or always set `POSTA_WEB_DIR`).
6. **Chunk splitting:** `advancedChunks` for codemirror and grapesjs (vite.config.ts:18-23) — preserve when touching the build config.
7. **Email HTML preview = sandboxed iframe `srcdoc`** (`sandbox=""` in InboundEmailDetail.vue, TemplatePreview.vue, EmailPreview.vue) — keep sandboxing in any redesign; the one un-sandboxed instance is a bug to fix (Section 11).
8. **Public endpoints must stay untouched:** tracking pixel `/t/o/{id}.gif`, click redirect, unsubscribe/web-view pages, form ingest `/api/v1/f/{key}`, inbound webhook.
9. **Dev proxy path** `/api/v1` → :9000 and the single `VITE_API_URL` override (Section 9).

## 11. Security findings

No actual secret values are printed in this report. **No committed secrets exist** — `.env.example` (root and examples/) contain only placeholders (`change-me-in-production`, `admin1234`, `example.com`, `secret`), and `internal/config/security.go:45-55` explicitly treats published placeholders as absent, refusing production boot on them.

| # | Severity | Finding |
|---|---|---|
| 1 | **HIGH** | **Un-sandboxed email-HTML iframe.** `web/src/views/emails/EmailDetail.vue:198-202` renders `email.html_body` in an iframe via `:srcdoc` with **no `sandbox` attribute**. [Verified directly] A same-origin unsandboxed iframe executes scripts from stored email content → stored-XSS vector into the dashboard. Sibling views (`InboundEmailDetail.vue:256-261`, `TemplatePreview.vue:299`, `EmailPreview.vue:173`) correctly use `sandbox=""`. Fix is a one-attribute change — deliberately not made during this audit. |
| 2 | **MEDIUM** | **2FA QR code generated by a third-party service.** `web/src/views/auth/Profile.vue` builds QR images via `https://api.qrserver.com` with the otpauth URL (containing the TOTP secret) in the query string. The 2FA secret leaks to an external service. Generate client-side instead. [Inspected] |
| 3 | **MEDIUM** | **SMTP credentials stored base64-only unless `POSTA_ENCRYPTION_KEY` is set** (AES-256-GCM when set); code warns only (`internal/config/security.go:130-136`). Config-dependent; document for operators. [Inspected] |
| 4 | **MEDIUM** | **`POSTA_TRUSTED_PROXIES` empty by default** → client IPs spoofable via X-Forwarded-For when deployed behind a proxy, undermining rate limits/audit; warned only (`security.go:157-163`). [Inspected] |
| 5 | **LOW** | Placeholder dev credentials in `compose.yml:15-17` (declared `POSTA_ENV=dev`, comments say so) and `Makefile:43-51` local containers; production refuses placeholders. [Verified] |
| 6 | **LOW** | External runtime requests: Google Fonts (web/index.html:35-37, EmailBuilder.vue:56), api.qrserver.com (finding 2), update check to api.github.com (config-gated via `POSTA_UPDATE_CHECK`). Privacy/self-hosting concern, not a vuln. [Verified for index.html] |
| 7 | **LOW** | `POSTA_OPENAPI_DOCS=true` by default (version disclosure; mitigated — `/info` requires auth, auth_routes.go:56-73). [Inspected] |
| 8 | **INFORMATIONAL** | Upstream brand/URLs hardcoded in backend responses and seeds (`config.go:500-501`, `seeder/seeder.go:230-307`, About.vue) — rebrand surface (Section 6), not a vulnerability. |
| 9 | **INFORMATIONAL** | Positive checks: no `os/exec` usage anywhere in `internal/`+`cmd/` (no command-injection surface); SQL is GORM-parameterized — the one dynamic-column site (`internal/storage/repositories/subscriber_repo.go:185-218`) validates keys against `isAlphanumericUnderscore` and binds values; no wildcard CORS by default (wildcard warns); no dangerous scripts (`make clean` uses fixed paths; no `curl | bash`). |

**Potentially dangerous scripts:** none found. **Debug settings:** `POSTA_DEV_MODE` and `POSTA_OPENAPI_DOCS` as above. **Hardcoded production URLs:** only upstream-brand URLs (finding 8) plus OAuth/Google endpoints (legitimate). **Unnecessary privileged permissions:** Dockerfile runs as non-root `posta` user — correct.

## 12. Missing files or configuration

| Item | Severity | Status |
|---|---|---|
| `.gitattributes` (LF normalization, `*.sh eol=lf`) | **HIGH** | Missing — must exist before the first commit (Section 7) |
| `.mimosa/` excluded from git | **MEDIUM** | Not in `.gitignore`; it is a local tool artifact, not project content |
| Third-party notices file (THIRD-PARTY-NOTICES) | MEDIUM | Missing — needed for `@mdi/font` (Pictogrammers) attribution; add Inter/OFL if self-hosted later |
| Frontend test framework | MEDIUM | None exists (no vitest/jest/playwright; CI only type-checks). The redesign has **no UI regression safety net** — adding one requires a new dependency (deliberately not added during this audit) |
| License confirmation for `jkaninda/*` Go modules + `go-premailer` | MEDIUM | Unverifiable offline [Needs investigation] |
| Upstream provenance record (commit/tag of the transfer) | MEDIUM | Not recorded anywhere [Needs investigation] |
| `.golangci.yml` stale exclusion | LOW | Line 50-52 excludes `internal/services/seeder/constants\.go`, which does not exist (harmless) [Verified] |
| `docs/README.md` package-manager mismatch | LOW | Stock Docusaurus boilerplate says yarn; repo tracks npm with `package-lock.json` [Inspected] |
| Makefile `.PHONY` omits `dev-deps-stop` | LOW | Line 1 lists `dev-deps` but not `dev-deps-stop` [Verified] |
| Makefile Windows ergonomics | LOW | `build` emits `bin/posta` without `.exe`; `build-ui` uses Unix commands — works via Git Bash, untested here [Assumption] |
| Dockerfile `ARG BUILD_DATE=2026-03-15` fixed default | LOW | Overridden in release flow; cosmetic [Inspected] |
| Issue/PR templates | INFORMATIONAL | None (upstream had none) |
| GitHub-side repo configuration | INFORMATIONAL | Branch protection, Actions variables/secrets (`BUILDKIT_IMAGE`, `DOCKERHUB_*`), Pages — not verifiable locally [Needs investigation] |

## 13. Potential blockers

**Nothing blocks starting the UI redesign.** The repository is intact, the toolchain is identifiable, and the frontend runs without Docker. Items that must be sequenced around it:

1. **[HIGH] CRLF + missing `.gitattributes`** — must be resolved **before the first commit**, or `docker/healthcheck.sh` breaks in containers and the repo bakes in CRLF. Zero-cost fix today; annoying migration later.
2. **[HIGH] `EmailDetail.vue` iframe sandbox fix** — one-line security fix; should land before the redesign ships (it touches a view the redesign will rebuild anyway — coordinate).
3. **[MEDIUM] License verification** (jkaninda/* modules, go-premailer) and the legal-review questions in Section 5 — gate **public distribution**, not local development.
4. **[MEDIUM] No frontend tests** — the redesign will modify ~160 Vue files with only `vue-tsc` as a net. Strongly recommend introducing a test runner as one of the first redesign-phase steps (requires a new dependency → needs your approval).
5. **[MEDIUM] Rebranding requires per-occurrence decisions** — Section 6 shows the rename blast radius. Technical identifiers (env prefix, headers, template vars, sentinels) should **stay**; user-visible strings and assets can move once the visual identity exists.
6. **[Assumption] Toolchain availability on this machine** — Go ≥ 1.27.1, Node ≥ 24.11, and native Postgres/Redis were not probed (no installs/execution per audit rules). Verify before the first `go run`/`npm install`.
7. **[Needs investigation] Docs scope** — `docs/` (~75 pages) was inventoried, not proofread; if ReForwarder ships its own docs, that is a large but separate workstream.

## 14. Recommended next steps

In order:

1. **Repository hygiene before the first commit:** add `.gitattributes` (`* text=auto`, `*.sh text eol=lf`); add `.mimosa/` to `.gitignore`; then make the initial commit capturing the pristine upstream transfer + new attribution README, with the upstream provenance noted in the message.
2. **Fix the HIGH security finding** — add `sandbox=""` to the `EmailDetail.vue` iframe (one attribute).
3. **Resolve the legal-review list** in Section 5 (NOTICE strategy, dependency licenses, trademark, upstream-pointing surfaces: SECURITY.md, FUNDING.yml, update check).
4. **Add a THIRD-PARTY-NOTICES file** (@mdi/font now; Inter/OFL if self-hosted).
5. **Verify the local toolchain** (Go, Node, Postgres, Redis) and do a first end-to-end native boot: `go run ./cmd/posta server` + `npm run dev`.
6. **Decide the frontend testing strategy** before redesign work begins.
7. **Establish the ReForwarder visual identity**, then execute the branding migration using the Section 6 inventory — user-visible strings and assets first; leave every Category-2 technical identifier alone.
8. **Then** redesign the dashboard/navigation and individual pages against the contract list in Section 10.

---

## Appendix A — Severity index

- **CRITICAL:** none found.
- **HIGH:** (1) CRLF repo-wide + missing `.gitattributes` — broken `docker/healthcheck.sh` if committed as-is (Section 7); (2) un-sandboxed email-HTML iframe, `EmailDetail.vue:198-202` (Section 11).
- **MEDIUM:** `.mimosa/` not excluded (§7/§12); 2FA QR via third-party service (§11); base64 SMTP credentials without encryption key (§11); empty `POSTA_TRUSTED_PROXIES` default (§11); unverified dependency licenses (§5); missing upstream provenance record (§5/§12); missing third-party notices file (§12); no frontend test framework (§12/§13).
- **LOW:** stale `.golangci.yml` exclusion; docs/README yarn mismatch; Makefile `.PHONY`/Windows ergonomics; fixed `BUILD_DATE` default; external Google Fonts requests; placeholder dev credentials; OpenAPI docs default-on.
- **INFORMATIONAL:** zero commits / removed history; zero ReForwarder mentions; product name is "Posta" (GoPosta = org); `internal/web/dist` placeholder-only by design; byte-identical logo triplicate; Docusaurus template leftovers in docs assets; no issue/PR templates; no git LFS needed.

## Appendix B — Explicitly not done (per audit rules)

No UI changes, no application-code changes (including the identified security fixes), no rebranding, no dependency changes, no commits, no pushes, no deletions, no installs, no migrations, no builds or test runs, no secrets printed. The two file changes listed at the top of this report are the complete set.
