# ReForwarder — Upstream Baseline & Provenance

> **Status:** Permanent source of truth · **Last verified:** 2026-11-02
>
> **Notice for maintainers and agents:** This document is ReForwarder's authoritative record of its upstream origin. Read it **before** attempting any upstream merge, rebase, dependency upgrade, or backend synchronization, and follow the synchronization policy in [§7](#7-future-upstream-synchronization-policy). Where a fact below cannot be independently re-verified, it is explicitly flagged as a historical finding.

---

## Table of Contents

1. [Official Upstream Baseline](#1-official-upstream-baseline)
2. [How the Baseline Was Verified](#2-how-the-baseline-was-verified)
3. [ReForwarder Git History](#3-reforwarder-git-history)
4. [Changes Made Since the Upstream Baseline](#4-changes-made-since-the-upstream-baseline)
   - [4.1 Committed ReForwarder changes](#41-committed-reforwarder-changes)
   - [4.2 Changes already present in the initial ReForwarder commit](#42-changes-already-present-in-the-initial-reforwarder-commit)
   - [4.3 Current uncommitted changes](#43-current-uncommitted-changes)
5. [Upstream Changes Not Yet Integrated](#5-upstream-changes-not-yet-integrated)
6. [Why the Running Application Reports version "dev"](#6-why-the-running-application-reports-version-dev)
7. [Future Upstream Synchronization Policy](#7-future-upstream-synchronization-policy)
8. [Recommended Comparison Commands](#8-recommended-comparison-commands)
9. [Future Build Version Recommendation](#9-future-build-version-recommendation)
10. [Baseline Integrity Statement](#10-baseline-integrity-statement)
- [Appendix A — Verification Evidence](#appendix-a--verification-evidence)

---

## 1. Official Upstream Baseline

| Detail | Value |
| --- | --- |
| Project name | ReForwarder |
| Original project | GoPosta / Posta |
| Original repository | [https://github.com/goposta/posta](https://github.com/goposta/posta) |
| ReForwarder repository | [https://github.com/ReForwarder/ReForwarder](https://github.com/ReForwarder/ReForwarder) |
| Upstream release tag | `v0.18.0-rc.1` |
| Exact upstream commit | `e423b8ad02168767baa76a5998657452853a38a6` |
| Short commit SHA | `e423b8ad` |
| Tag date | October 21, 2026 (tag commit dated 2026-10-21 08:07:49 +0200) |
| Backend differences at original fork baseline | 0 |
| Current binary version | `dev` |
| Stable v0.18.0 | Not established (no `v0.18.0` tag existed as of 2026-11-02; `v0.18.0-rc.1` was the newest upstream tag) |
| Upstream license | AGPL-3.0-or-later |
| Upstream author | Jonas Kaninda |
| Baseline verification date | 2026-11-02 |

**ReForwarder uses GoPosta/Posta commit `e423b8ad02168767baa76a5998657452853a38a6`, corresponding to upstream tag `v0.18.0-rc.1`, as its verified upstream baseline.**

> **Warning:** Do not confuse the upstream baseline commit with ReForwarder's current Git HEAD. `e423b8ad02168767baa76a5998657452853a38a6` is a commit in the **upstream** repository (`goposta/posta`) and does **not** exist in ReForwarder's reconstructed local history. ReForwarder's own history starts at its initial commit `73ee10f` (see [§3](#3-reforwarder-git-history)).

---

## 2. How the Baseline Was Verified

ReForwarder was created by copying the upstream source and **deleting the original `.git` history**. The upstream commit could therefore not be read from local history and was established forensically:

1. The initial ReForwarder commit is `73ee10f` (`73ee10f78cb8e211e743039f8d495425b01fcca2`).
2. A pristine copy of the initial commit tree was exported (`git archive 73ee10f`) into a temporary Git repository outside the project.
3. All upstream tags were fetched from `https://github.com/goposta/posta` into that repository (47 tags at verification time).
4. The pristine backend was compared against the upstream release tags, comparing `go.mod`, `go.sum`, `cmd/`, and `internal/`.
5. (2026-11-02 re-check) The comparison was re-run and confirmed with `UPSTREAM_BASELINE.md` being authored; result unchanged.

Verification results:

| Upstream reference | Backend files differing |
| --- | --- |
| `v0.18.0-rc.1` | **0** |
| `v0.17.0` | 53 |
| `v0.16.0` | 68 |
| Upstream `main` (November 1, 2026) | 21 |

The zero-difference comparison establishes that the ReForwarder backend (`go.mod`, `go.sum`, `cmd/`, `internal/`) was **byte-identical** to upstream tag `v0.18.0-rc.1` at the original fork baseline. Neighboring tags differ by dozens of files, so the match is unique and unambiguous.

Timeline context (all dates verified from upstream Git metadata):

- **2026-10-21** — Upstream tagged `v0.18.0-rc.1` (`e423b8ad`).
- **2026-10-30 19:04 UTC** — Upstream's first backend change after the tag landed on `main` (`df2de9552435a8e7cef2da29d322f2cc54e4fbc9`, a `golang.org/x/net` dependency bump).
- **2026-10-30/31** — The ReForwarder source snapshot was acquired (local file timestamps 2026-10-31 ~02:27 IST). This falls **inside the window** in which upstream `main`'s backend still matched the tag tree, which is consistent with — and explains — the byte-identical result.

**Known limits of this verification (flagged, not resolvable from local evidence):**

- Whether the received snapshot was the `v0.18.0-rc.1` release archive itself, or an upstream `main` checkout from the Oct 21–30 window whose tree equaled the tag, cannot be distinguished after the fact. Either way, the code in hand is byte-identical to the tag on all backend paths, so the tag is the correct baseline.
- The received **frontend** at acquisition time was compared against the tag only indirectly: by the time of the first ReForwarder commit, the frontend had already been rebranded (see [§4.2](#42-changes-already-present-in-the-initial-reforwarder-commit)), so a direct frontend byte-comparison against the tag is no longer possible. Only the backend identity is proven; the frontend baseline is recorded here as a historical finding, not a verified fact.

---

## 3. ReForwarder Git History

| Detail | Value |
| --- | --- |
| Initial commit | `73ee10f` (`73ee10f78cb8e211e743039f8d495425b01fcca2`) — "ReForwarder — initial commit", 2026-11-01 22:40:57 +0530 |
| Current branch | `main` |
| Remote | `origin` → `https://github.com/ReForwarder/ReForwarder.git` |
| Repository visibility | Private |
| HEAD at last verification | `191d961` (`191d961d9d6f5337b2a06022e97e1f990940397a`), 2026-11-02 00:04:55 +0530 |
| Upstream tags in local history | None |
| Upstream remote configured locally | None (only `origin`; no `upstream` remote had been added as of 2026-11-02) |

Commit sequence at last verification (oldest first):

| Commit | Subject |
| --- | --- |
| `73ee10f` | ReForwarder — initial commit (727 files) |
| `6c588d2` (`6c588d297852a8f1b5aa67b9851d0d2764528ef2`) | README: full ReForwarder README adapted from upstream Posta |
| `60c682d` (`60c682dae0bd5c8de05e43e14d915ef703a43b4f`) | Replace logo and favicon set with the ReForwarder R. mark |
| `abe7635` (`abe7635fb6644b5a355a12fe870719a0999407a8`) | Dependabot #1: `golang.org/x/net` 0.61.0 → 0.62.0 |
| `bc531a1` (`bc531a103ba71ac162abeaceba3c51208d2b611b`) | Dependabot #2: `aws-sdk-go-v2` 1.47.3 → 1.47.5 |
| `63646b6` (`63646b6420189545c60f475e895693c65958f036`) | Dependabot #4: `aws-sdk-go-v2/service/s3` 1.114.3 → 1.114.6 |
| `191d961` (`191d961d9d6f5337b2a06022e97e1f990940397a`) | Dependabot #5: `aws-sdk-go-v2/credentials` 1.20.9 → 1.20.12 |

Important facts about this history:

- The original upstream Git history was **not retained**. Do not assume any upstream commits, tags, or merge-base exist locally.
- ReForwarder's history was rebuilt from scratch; the initial commit is the fork snapshot **plus** the rebrand-era changes described in [§4.2](#42-changes-already-present-in-the-initial-reforwarder-commit) — it is *not* a pristine upstream tree.
- Dependabot PR #3 (`jkaninda/okapi` 1.1.0 → 1.2.0) was deliberately **held open**; see [§5](#5-upstream-changes-not-yet-integrated).

---

## 4. Changes Made Since the Upstream Baseline

The inventory below was verified on **2026-11-02**. It is a point-in-time record: re-verify against the actual Git diff before relying on it for any synchronization work.

### 4.1 Committed ReForwarder changes

Diff `73ee10f..HEAD` contained exactly **18 changed files** (verified via `git diff --name-only 73ee10f..HEAD` on 2026-11-02):

| Area | Files | Description |
| --- | --- | --- |
| README | `README.md` | Full ReForwarder README adapted from upstream (commit `6c588d2`), including Acknowledgments/Copyright/License attribution |
| Root branding | `logo.png` | ReForwarder "R." mark (commit `60c682d`) |
| Documentation screenshots | `docs/static/img/screenshots/admin-platform-metrics.png`, `analytics.png`, `dashboard.png`, `emails.png` | Replaced alongside the README rebrand |
| Favicon / logo / manifest assets | `web/public/`: `apple-touch-icon.png`, `favicon-16x16.png`, `favicon-32x32.png`, `favicon-96x96.png`, `favicon.ico`, `favicon.svg`, `logo-navy.png`, `logo.png`, `web-app-manifest-192x192.png`, `web-app-manifest-512x512.png` | ReForwarder favicon/logo set (commit `60c682d`) |
| Dependencies | `go.mod`, `go.sum` | Four Dependabot updates (see below) |

Dependency updates (all merged as squash commits with passing CI):

| PR | Dependency | Version change | Commit |
| --- | --- | --- | --- |
| #1 | `golang.org/x/net` | 0.61.0 → 0.62.0 | `abe7635fb6644b5a355a12fe870719a0999407a8` |
| #2 | `github.com/aws/aws-sdk-go-v2` | 1.47.3 → 1.47.5 | `bc531a103ba71ac162abeaceba3c51208d2b611b` |
| #4 | `github.com/aws/aws-sdk-go-v2/service/s3` | 1.114.3 → 1.114.6 | `63646b6420189545c60f475e895693c65958f036` |
| #5 | `github.com/aws/aws-sdk-go-v2/credentials` | 1.20.9 → 1.20.12 | `191d961d9d6f5337b2a06022e97e1f990940397a` |

The 18-file list above was verified against the actual Git diff on 2026-11-02 and was complete as of that date; it will stop being complete the moment new work is committed.

### 4.2 Changes already present in the initial ReForwarder commit

The initial commit `73ee10f` contained **130 files** differing from upstream tag `v0.18.0-rc.1` (+2,957 / −1,474 lines, verified via full-tree diff on 2026-11-02), primarily:

- Frontend and UI (`web/src/**`): ReForwarder branding, design system, layout and component changes.
- Branding assets: logo/favicon set, `web/index.html` metadata.
- Documentation: rebranded `docs/` (Docusaurus config, getting-started introduction, screenshots).
- Docker Compose configuration: `compose.yml` (dev-oriented environment values).
- Development environment configuration: `.gitignore`, `.gitattributes`, `docker/healthcheck.sh`.
- Project documentation: `REFORWARDER_PRE_DEVELOPMENT_AUDIT.md` (ReForwarder-authored audit report; not upstream).

**These changes did not introduce any backend differences against the verified upstream baseline** — `go.mod`, `go.sum`, `cmd/`, and `internal/` were untouched by the rebrand-era work.

### 4.3 Current uncommitted changes

The following working-tree state was re-verified on **2026-11-02**. It was previously reported during the 2026-11-01/02 investigation; **maintainers must re-verify the live working tree (`git status`, `git diff`) before relying on this inventory** — it changes with active development.

Modified (8 files, +76 / −17 lines):

| File | Change |
| --- | --- |
| `cmd/posta/server.go`, `cmd/posta/worker.go` | Wiring for the new settings provider usage |
| `internal/services/seeder/settings.go` | New seeded setting row |
| `internal/services/settings/provider.go` | New `CampaignClickTrackingEnabled()` accessor (+30s cache TTL semantics) |
| `internal/services/tracking/tracking.go` | `tracking.Service` split into `RewriteLinksForClicks` + `InjectOpenPixel` |
| `internal/worker/campaign_processor.go` | Branches on the setting: disabled → open pixel only (no URL rewriting) |
| `internal/worker/handler.go` | Honest `X-Posta-Track-Clicks` header stamping |
| `web/src/views/admin/Settings.vue` | Admin Settings UI entry for the toggle |

Untracked (3 items):

- `internal/services/tracking/tracking_test.go`
- `internal/services/settings/provider_test.go`
- `docs/security-research/`

Purpose: a global campaign click-tracking toggle (`campaign_click_tracking_enabled`, default `true`) that allows disabling automatic URL rewriting for campaign emails while keeping open-pixel tracking.

---

## 5. Upstream Changes Not Yet Integrated

The following is the **historical comparison of upstream `main` against the baseline, recorded November 1, 2026** (21 backend files differing, per [§2](#2-how-the-baseline-was-verified)). These are point-in-time findings — **verify upstream's current state before planning any synchronization**, as upstream may have released or changed further since.

Known upstream `main` differences at that date:

- Form-ingest and message-route changes (`internal/handlers/form_handler.go`, `form_ingest_handler.go`, `internal/routes/message_routes.go` and tests).
- Webhook DTO expansion (`internal/dto/webhook_events.go`, `webhook_registry.go`).
- Admin/auth route changes (`internal/routes/admin_routes.go`, `auth_routes.go`).
- Dependency updates (`go.mod`/`go.sum`), including an `okapi` upgrade from 1.1.0 to 1.2.0 (upstream commit `120ee56b835c8f055b111a25e57209481cbf1872`, 2026-11-01).

`okapi` compatibility concern (historical finding): the `okapi` 1.1.0 → 1.2.0 upgrade was deliberately **held** in ReForwarder (Dependabot PR #3 kept open) because v1.2.0 removes `okapi.DocErrorResponse`, which ReForwarder's `internal/routes/admin_routes.go` uses; integrating it requires backend call-site adaptations.

Notable convergence: upstream landed the same three dependency bumps ReForwarder merged via Dependabot (`golang.org/x/net` 0.62.0, AWS S3 SDK, AWS credentials) between Oct 30 – Oct 31, 2026 — the two dependency sets had converged on identical versions, while upstream additionally moved to `okapi` 1.2.0.

---

## 6. Why the Running Application Reports version "dev"

The running application reports `version: dev` in its server log and OpenAPI information. This is a **build-time identifier, not an upstream release identifier** — it does not indicate which upstream source the application was built from (that is documented in [§1](#1-official-upstream-baseline)).

Mechanism, verified in the source on 2026-11-02:

- `internal/config/version.go` defines compile-time defaults: `Version = "dev"` and `CommitID = "unknown"`.
- The `Makefile` and `docker/Dockerfile` inject real values through linker flags (`-X github.com/goposta/posta/internal/config.Version=…`, `CommitID=…`, `BuildDate=…`), derived from `VERSION` / `COMMIT` build arguments (Makefile defaults: `git describe --tags --always --dirty` and `git rev-parse --short HEAD`).
- The local Docker image was built **without** explicit `VERSION`/`COMMIT` build arguments, so the compile-time defaults were baked in.

Verified Docker details (2026-11-02):

| Detail | Value |
| --- | --- |
| Image | `reforwarder-posta:latest` (ID `sha256:5d323c4cfbed…`), plus `reforwarder-worker:latest` from the same Dockerfile |
| Built | Locally, from `docker/Dockerfile`, 2026-11-02 00:58 IST (`2026-11-01T19:28:32Z`) |
| Image version label | `dev` (`org.opencontainers.image.version`) |
| Image revision label | `unknown` (`org.opencontainers.image.revision`) |
| Entrypoint | `/bin/posta` (worker container appends command `worker`) |
| Server log | `"Posta Server started","version":"dev"` |

There is no `--version` flag or `version` subcommand on the `posta` binary; the version surfaces only via the server log and OpenAPI information.

---

## 7. Future Upstream Synchronization Policy

The following rules are binding for all future ReForwarder development:

1. Treat `e423b8ad02168767baa76a5998657452853a38a6` as the permanent historical origin baseline.
2. Treat GoPosta/Posta as the upstream project.
3. Track upstream `main` and future release tags separately.
4. Never blindly merge upstream changes into ReForwarder.
5. Review backend changes for compatibility before integration.
6. Preserve ReForwarder branding, frontend design, product identity, and custom features.
7. Review database migrations and schema changes carefully.
8. Review API changes, authentication, authorization, and webhook compatibility.
9. Review dependency changes for breaking changes and security fixes.
10. Run relevant tests before and after every upstream synchronization.
11. Maintain attribution to the original GoPosta/Posta project.
12. Preserve the AGPL-3.0-or-later license obligations.
13. Record every upstream synchronization in this document (or a successor section) with the source commit, imported changes, conflicts, custom modifications preserved, and verification results.

---

## 8. Recommended Comparison Commands

> **Note:** These commands are examples for a future, properly configured `upstream` remote. **They have not been run in the ReForwarder repository itself** — the 2026-11-02 verification was performed in an out-of-tree temporary Git repository (see [Appendix A](#appendix-a--verification-evidence)), because no `upstream` remote existed locally.

Fetch upstream (skip `remote add` if the remote already exists):

```bash
git remote add upstream https://github.com/goposta/posta.git
git fetch upstream --tags
```

Compare backend changes between the original baseline and upstream `main`:

```bash
git diff --name-status e423b8ad02168767baa76a5998657452853a38a6..upstream/main -- go.mod go.sum cmd internal
```

List upstream commits since the baseline:

```bash
git log --oneline e423b8ad02168767baa76a5998657452853a38a6..upstream/main
```

Compare the baseline tag with upstream `main`:

```bash
git diff --stat v0.18.0-rc.1..upstream/main
```

Check the current working tree:

```bash
git status --short
```

Method actually used for the 2026-11-02 verification (reproducible without touching this repository's remotes): export the pristine initial-commit tree to a temporary directory, `git init` there, add `https://github.com/goposta/posta` as `origin`, fetch tags (`--filter=blob:none`), commit the tree, then `git diff --name-only v0.18.0-rc.1 HEAD -- go.mod go.sum cmd internal`.

---

## 9. Future Build Version Recommendation

**Recommendation (not yet applied):** stamp future Docker builds with explicit version and commit identifiers so the running application reports an honest build identity, for example:

```bash
docker compose build \
  --build-arg VERSION=v0.18.0-rc.1+reforwarder \
  --build-arg COMMIT=<reforwarder-commit-sha>
```

Future builds should report the **ReForwarder build version and ReForwarder's own commit SHA**, while this document permanently retains the original upstream baseline. The version string convention (e.g. `v0.18.0-rc.1+reforwarder`, optionally suffixed with the ReForwarder commit) should be settled at the first such build. Applying this requires a change to build/compose invocation only — no application code changes — and had **not** been executed as of 2026-11-02.

---

## 10. Baseline Integrity Statement

> ReForwarder is a downstream project derived from GoPosta/Posta. Its verified original backend baseline is upstream commit `e423b8ad02168767baa76a5998657452853a38a6`, tagged `v0.18.0-rc.1`. Future upstream updates must be evaluated against this historical baseline while preserving ReForwarder's independent development, branding, and custom functionality.

---

## Appendix A — Verification Evidence

- **Verification date:** 2026-11-02 (initial forensics 2026-11-01/02; document authored and facts re-confirmed 2026-11-02).
- **Environment:** Windows / Git Bash; Docker containers `reforwarder-posta-1`, `reforwarder-worker-1` (locally built images); temporary verification repository outside the project tree.
- **Commands and checks actually run:**
  - `git archive 73ee10f` → pristine tree export → temp repo with upstream tags fetched (`git fetch origin 'refs/tags/*:refs/tags/*' --filter=blob:none`, 47 tags).
  - `git diff --name-only v0.18.0-rc.1 HEAD -- go.mod go.sum cmd internal` → **0 files** (re-confirmed twice on 2026-11-02); `v0.17.0` → 53; `v0.16.0` → 68; upstream `main` (`1eca7f05d441feb193334ca43bdae368b1eb1fc9`) → 21.
  - `git cat-file -t e423b8ad02168767baa76a5998657452853a38a6` → `commit` (i.e., `v0.18.0-rc.1` is a lightweight tag pointing directly at the commit).
  - `git rev-parse` for all local SHAs quoted in this document; `git diff --name-only 73ee10f..HEAD` → 18 files; `git diff --stat` for the uncommitted working tree (8 files, +76/−17).
  - `docker inspect` / `docker exec` on the running containers for the build/version facts in [§6](#6-why-the-running-application-reports-version-dev).
  - Read-only source inspection of `go.mod`, `Makefile`, `docker/Dockerfile`, `compose.yml`, `internal/config/version.go`.
- **Ephemeral evidence:** the temporary verification repository (containing the pristine baseline tree and fetched upstream tags) lived outside the project and may not persist. The verification method in [§8](#8-recommended-comparison-commands) reproduces it.
- **Historical facts retained from the original investigation (not independently re-derived for this document):** the `okapi.DocErrorResponse` breaking-change diagnosis behind the held PR #3, and the acquisition-time frontend baseline assumption noted in [§2](#2-how-the-baseline-was-verified).
