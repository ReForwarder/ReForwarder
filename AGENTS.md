# AGENTS.md

Standing instructions for every AI coding agent in this repository (ReForwarder, module
`github.com/goposta/posta`: Go backend under `internal/` + `cmd/posta`, Vue 3 dashboard in
`web/`, AGPL-3.0-or-later). These rules are binding; if a task conflicts with them, say so
instead of working around them.

## 1. Orientation — read before writing code

- Read the project ledgers first, all under `docs/project/`: `ARCHITECTURE_OVERVIEW.md`,
  `BACKLOG.md`, `FEATURE_GAP_ANALYSIS.md`, `FEATURE_INVENTORY.md`, `PRIORITIES.md`,
  `PROJECT_MAINTENANCE.md`, `FEATURE_AUDIT.md`.
  If one is missing or stale, say so rather than guessing.
- Then read `todo.md` (master todo; Track A = Option B mail-infrastructure programme,
  Track B = relay roadmap P0–P3). Read its §0 before starting any item and re-verify the
  item's current state at HEAD — audit statuses are historical snapshots.
- For upstream merges, rebases, dependency upgrades, or backend synchronization: read
  `UPSTREAM_BASELINE.md` first. It is the authoritative provenance record; its §7 policy
  is **never blindly merge upstream changes**.
- The whole tree is AGPL-3.0-or-later (`LICENSE`, `NOTICE`). Never weaken, relicense, or
  strip attribution.

## 2. Feature IDs — permanent; never renumber, never duplicate

- Check existing IDs before adding anything and **reuse** the existing ID for the same
  feature. Never renumber an ID; never give one feature two IDs.
- `RF-F###` IDs (RF-F001–RF-F105 as of 2026-11-06) come from the audited feature registry
  maintained by the repo-audit workflow — consult its current registry view (and
  `docs/project/FEATURE_INVENTORY.md`) before minting a new one; new
  features take the next free number only.
- `todo.md` line 3 declares Registry IDs, GAP-01…53, D1–D9 and R1–R16 permanent — never
  renumber. The same discipline applies to every other ID family (SEC-, ANA-, INF-,
  CAM-, CON-, TPL-, AUT-, API-, DOM-, OBS-).
- For the remainder of a partially landed item, keep the original ID and mark it
  `residual` (as `todo.md` does) instead of creating a new ID.

## 3. After you implement — update the ledgers

Bookkeeping is part of the task, not an afterthought:

- Update the item's status in the `README.md` roadmap section.
- Update `BACKLOG.md`, `FEATURE_INVENTORY.md`, and `STATUS_HISTORY.md` (the status
  ledgers under `docs/project/`): one entry per status change, with the date and evidence.
- Keep statuses truthful and use the existing legend (Done / In Progress / To-dos). Move
  to Done only when the Done bar in §4 is met.
- The backlog lives ONLY on the repository-level "ReForwarder Backlog" GitHub project
  (https://github.com/ReForwarder/ReForwarder/projects) as draft items (board notes);
  **never create GitHub issues** for backlog tracking. Sync the board **only when the
  task explicitly authorizes it** (`gh` CLI is available): update the draft item's
  **Status** field (Feature Available / Feature Done / Features Incoming / To-Dos /
  Incomplete / In Progress) to match the item's state.
  `BACKLOG.md` and the `docs/project/` ledgers are updated per the bullets above.
  Board sync (GraphQL / `gh project`) currently requires project scopes — run
  `gh auth refresh -s read:project,project` to enable it. Until then, document the manual
  procedure instead of claiming a sync happened.

## 4. The Done bar — never mark unverified work Done

Mark an item Done only when **all** of the following hold, each with recorded evidence
(`path:line`, or the exact command + output for checks you ran):

1. **Implementation** — the code exists at a real path; cite it.
2. **Wiring** — it is reachable: route registered, handler bound, UI wired, cron/worker
   scheduled. An unused file is not a feature.
3. **Not a stub** — no TODO/placeholder/"not implemented" left on the user path;
   dead-end flows count as not Done (e.g. the Contact Lists dead end RF-F101, dead admin
   client methods RF-F102).
4. **Tests** — the test gate passes: `go test ./... -count=1` (the programme runs it
   dockerized on `golang:1.27`); add tests for the new behavior and name them in the
   evidence.
5. **Documented limitations** — anything not covered is written down in the item entry /
   `STATUS_HISTORY.md`, not silently omitted.

If you cannot produce the evidence, leave the status unchanged and report the gap. Never
fake a passing check.

## 5. Evidence

- Back every claim — in commits, ledgers, and reports — with `path:line` or a command you
  actually ran (quote the command and relevant output).
- A check counts as passed only if you ran it in that session. Report anything you could
  not run, verify, or find as "not run / not found" — never as a plausible guess.

## 6. Git discipline

- **Preserve uncommitted work.** The tree routinely carries uncommitted changes (34
  dirty/untracked entries at the time of writing). Never discard, stash-drop, reset, or
  overwrite work you did not create; run `git status` before and after your changes and
  keep your diff limited to your own files.
- **Never push without explicit authorization** in the task — including force-pushes,
  tags, and opening PRs.
- Reference the feature IDs a commit closes or advances in its message.

## 7. Repo conventions to preserve

- **Backend boundary care.** Follow the layering: `internal/handlers` →
  `internal/services` → `internal/storage`/repositories; `cmd/posta` runs the server +
  worker. The Vue dashboard in `web/` talks to the backend only through the REST API.
  Respect upstream provenance per `UPSTREAM_BASELINE.md` §7 — never blindly merge.
- **Clean-room discipline for AGPL/BSL references.** Study competitors and prior art only
  through the written analyses (`docs/product-roadmap/`, `docs/research/`). Never copy
  source, assets, or docs from AGPL- or BSL-licensed projects into this tree; describe
  behavior in your own words and verify claims against our own checkout with `path:line`.
- **Additive migrations.** Schema changes go through `internal/storage/migration` (plus
  `internal/storage/migration/upgrade`). New migrations must be additive — add
  tables/columns, do not drop or rewrite existing ones — so deployed instances upgrade
  in place.
- **API envelope.** Every REST response uses the envelope from `internal/dto/types.go`:
  `dto.Response[T]` → `{"success": bool, "data": T}`; lists use `PageableResponse` (adds
  `pageable`); errors use the error envelope (`success`, `data`, `error{code, …}`). Reuse
  these — do not invent ad-hoc response shapes.

## 8. Verification commands

```bash
go test ./... -count=1        # test gate (programme runs it dockerized, golang:1.27)
make vet                      # go vet
make lint                     # golangci-lint (config: .golangci.yml)
make build-ui && make build   # dashboard (web/), then the embedded binary
```

Run each check at the scale the task names; report narrower runs as narrower runs, never
as the named check.
