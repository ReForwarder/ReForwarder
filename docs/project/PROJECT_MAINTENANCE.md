# PROJECT_MAINTENANCE — synchronization operating procedure

How the repo's tracking surfaces stay in sync. Produced by run `dwfrun-84c797c2-39b6-49df-9612-c3dc664a20e7` (2026-11-06). Every claim below marked **verified** was executed in that run's session; everything else is the documented procedure to follow.

> **Migration note (2026-11-06):** an audit run created 92 GitHub issues as the individual specs; the user directed their removal the same day — the issues were deleted, never closed, and must not reappear. The backlog moved to **Project #1 draft items** (the board notes described in §1); GitHub issues are no longer used for backlog tracking anywhere in this procedure. The deleted issues' full bodies are archived at `.zcode/tmp/issues-backup-2026-11-06.json` — an archive, not a tracking surface.

> **Board move (2026-11-07):** the operational task board moved from the organization project #1 (`github.com/orgs/ReForwarder/projects/1`, 92 draft items) to the repository-level **"ReForwarder Backlog"** project, visible at https://github.com/ReForwarder/ReForwarder/projects (linked to the repository). It is a Kanban board with six Status columns: Feature Available, Feature Done, Features Incoming, To-Dos, Incomplete, In Progress. Org project #1 is left **exactly as-is** per explicit user decision — never delete or archive it — and is no longer the board items are added to. The no-issues rule stands: backlog tracking uses draft items only, never GitHub issues. Every passage below that named org Project #1 as the board now targets the repository-level board.
>
> **Correction (2026-11-07, board-repair run):** org project #1 as recorded above (`PVT_kwDOFOOdac4BqhYJs`) **does not exist** — GraphQL `node` lookup returns NOT_FOUND, the org's `projectsV2` connection lists only project #3 (`totalCount` 1), and `gh project list --owner ReForwarder` (open and `--closed`) shows only project 3. Either it was deleted or the id/number recorded above was wrong; its claimed 92 draft items cannot be verified. It was not touched and not recreated (per the explicit do-not-touch instruction of the 2026-11-07 repair ask); the repository-level "ReForwarder Backlog" board (project 3) remains the only backlog surface (AGENTS.md §3).

## 1. Surfaces and their roles

| Surface | Role | Content contract |
| --- | --- | --- |
| `README.md` | **Human overview** | What the product is and what it does, feature-level only, plus the Project Roadmap section (per-feature status checkboxes keyed by registry ID, maintained per `AGENTS.md` §3). No task-level detail. |
| `BACKLOG.md` | **Detailed tasks** | The working checklist: one entry per work item, keyed by its permanent ID, with current state and residual scope. On disk at `docs/project/` since 2026-11-06 (1,409 lines); maintained per this contract. |
| Board draft items ("ReForwarder Backlog") | **Individual specs** | One draft item (board note) per work item on the repository-level "ReForwarder Backlog" board (https://github.com/ReForwarder/ReForwarder/projects), titled `[RF-Fxxx] Title` (the item's permanent ID): the spec and acceptance criteria, content sourced from `BACKLOG.md`; its **Status** field is set from the item's `BACKLOG.md` priority/state. |
| "ReForwarder Backlog" (repository-level) | **Operational board** | GitHub Projects v2, linked to the repository and visible at https://github.com/ReForwarder/ReForwarder/projects (board move 2026-11-07). Its single-select **Status** field carries the six Kanban columns: Feature Available / Feature Done / Features Incoming / To-Dos / Incomplete / In Progress. Org project #1 (92 draft items) was recorded as preserved untouched per user decision (2026-11-07), but it **does not exist** as of the 2026-11-07 verification (GraphQL NOT_FOUND for `PVT_kwDOFOOdac4BqhYJs`; the org's only Projects v2 is this board, project 3) — its claimed 92 draft items cannot be verified; see the Board move note in the preamble. |

Supporting files (not surfaces): `STATUS_HISTORY.md` — append-only ledger of status decisions (date, producing run, HEAD, counts); `PRIORITIES.md` — tier definitions and ordering; `todo.md` — master working checklist (Track A/B) and the source of permanent IDs.

**Frozen records:** `docs/product-roadmap/*` and `todo.md`'s research sections are historical research records — they are **not** edited for status sync (`todo.md:3`: registry IDs, GAP-01…53, D1–D9, R1–R16 are permanent, never renumbered). New truth goes to the ledger, the backlog, and the board.

## 2. Update flow after every feature / bug / PR

Ordered — work top to bottom. Steps 0–3 always; 4–6 conditionally.

| Order | Touch | What to do |
| --- | --- | --- |
| 0 | Code + tests | Land the change with tests. Run the test gate `go test ./... -count=1` — it must **PASS** before any status moves. A changed file alone never marks a feature Done (§5, rule 3). |
| 1 | `BACKLOG.md` | Update the item's entry: mark done with the commit sha, or narrow the residual scope. Search the file by ID first; edit in place, never append a duplicate entry. |
| 2 | "ReForwarder Backlog" draft item | Search for an existing draft item first (`gh project item-list <NUMBER> --owner <owner> -L 100` — the repository-level "ReForwarder Backlog" project; see §4 for resolving `<NUMBER>`). Update the draft item's body with an evidence note: commit sha + test-gate result. Never open a GitHub issue; never create a duplicate draft item. |
| 3 | "ReForwarder Backlog" board | Ensure the draft item is on the board and set its **Status** field — exact procedure in §4. |
| 4 | `README.md` | Only if the change alters the human overview (a new user-visible capability or a removed one). Do not mirror task detail here. |
| 5 | `STATUS_HISTORY.md` | If any registry status changed: append a ledger entry — date, producing run id, HEAD, test-gate result, new counts. Never rewrite prior entries. |
| 6 | `PRIORITIES.md` | If a priority assignment changed: re-issue §3 (current ordering) and note the producing run. |

Rationale: bottom-up. Ground truth (code + tests) first, then the detailed tracker, then the board (its draft item is the individual spec), then the human overview, then history/priorities — which record the outcome of the sync, not drive it.

## 3. Idempotency rules

1. **Search before create — everywhere.**
   - "ReForwarder Backlog" board (repository-level, https://github.com/ReForwarder/ReForwarder/projects): `gh project item-list <NUMBER> --owner <owner> -L 100` and check for the draft-item title before `gh project item-create` (default page size is 30 — pass `-L`).
   - `BACKLOG.md` / `PRIORITIES.md` / `STATUS_HISTORY.md`: grep by ID (or date + run id for ledger entries) before adding anything.
2. **Never duplicate.** One draft item per work item; one backlog entry per ID; one ledger entry per date + run id.
3. **Titles carry the permanent ID** (e.g. `GAP-01: Fix transactional scheduled sends end-to-end`, `RF-F048: …`) so searches are deterministic. Permanent IDs (`RF-*`, `GAP-01…53`, `D1–D9`, `R1–R16`) are never renumbered (`todo.md:3`).
4. **Update in place, keyed by ID.** No "take 2" copies, no duplicate sections.
5. **After a failed or interrupted create, re-run the search before retrying** — the failure may have been partial.
6. **No API safety net for draft items.** `gh project item-create` / `addProjectV2DraftIssue` always create a new item — a draft item is created from title/body, not linked to existing content, so there is no duplicate detection (the existing-item net documented for `addProjectV2ItemById` applies only to issue/PR content, which this model no longer uses). Search-first (rule 1) is the only guard.

## 4. Manual procedure — add an item to the "ReForwarder Backlog" board and set Status

**Current access state (verified by execution, 2026-11-07):** project access **works**. `gh auth status` shows token scopes `'gist', 'project', 'read:org', 'repo', 'workflow'` (the `project` scope is present), `gh project list --owner ReForwarder` succeeds both open and with `--closed` (listing project 3 only), and GraphQL board reads and item edits (`updateProjectV2DraftIssue`) succeed with this token. Historical (2026-11-06): the token then held only `'gist', 'read:org', 'repo', 'workflow'` and `gh project list` failed with `error: your authentication token is missing required scopes [read:project]`; `gh auth refresh -s read:project,project` (§4.1) resolved it. The success path has since been executed end-to-end (board reads and draft-item body writes on 2026-11-07).

### 4.1 One-time prerequisite: grant the project scopes

```bash
gh auth refresh -s read:project,project
```

- `read:project` — read-only access to user and organization projects; `project` — read/write access (docs.github.com, "Scopes for OAuth apps"). Granting both, as above, covers viewing *and* item-add/item-edit.
- This is an interactive browser/device flow (`gh auth refresh` help: `--scopes` accepts a comma-separated scope list). Never script token exchange (§5, rule 1).
- Verify the grant: `gh project list --owner ReForwarder` lists the repository-level "ReForwarder Backlog" project (https://github.com/ReForwarder/ReForwarder/projects) — project **number 3**, node id `PVT_kwDOFOOdac4BqhY4` (verified 2026-11-07). These are the `<NUMBER>`/`<owner>` used in §4.2 and §4.3, and `<PROJECT_ID>` there is `PVT_kwDOFOOdac4BqhY4`. Owner `ReForwarder` derives from `git remote -v` → `https://github.com/ReForwarder/ReForwarder.git` (verified). If the project is user-owned instead, use `--owner @me`.

### 4.2 Create a draft item and set Status (CLI)

```bash
# 1. Confirm the project (<NUMBER> = the "ReForwarder Backlog" project number, read
#    from `gh project list --owner ReForwarder` — see §4.1)
gh project view <NUMBER> --owner ReForwarder

# 2. SEARCH BEFORE CREATE — is a draft item for this ID already on the board?
gh project item-list <NUMBER> --owner ReForwarder -L 100

# 3. Create the draft item (board note) — content sourced from BACKLOG.md
gh project item-create <NUMBER> --owner ReForwarder --title "[RF-Fxxx] Title" --body "<spec + acceptance criteria from BACKLOG.md>"

# 4. Set the Status field on the new item (draft items have no issue URL, so the by-name
#    --field path is unavailable — item-edit rejects --field combined with --id; use the machine variant)
gh project item-edit --id <ITEM_ID> --project-id <PROJECT_ID> --field-id <FIELD_ID> --single-select-option-id <OPTION_ID>

# 5. VERIFY — the item is present with the expected Status
gh project item-list <NUMBER> --owner ReForwarder -L 100 --field Status
```

Flags verified against the installed `gh` v2.100.0 help: `item-create [<number>] --owner --title --body` (creates a draft item — takes a title/body, not repo content); `item-list [<number>] --owner -L/--limit (default 30) --field --query`; `item-edit --id <ITEM_ID> --project-id <PROJECT_ID> --field-id <FIELD_ID> --single-select-option-id <OPTION_ID>` — the CLI enforces that the by-name `--field`/`--value` path requires `--url` (issue/PR URL, absent for draft items): `gh project item-edit` help states "`--field` cannot be used with `--id`; use `--url` to address the item when editing by name". The field/option IDs come from §4.3. The GraphQL write path has been executed end-to-end in this environment (2026-11-07: 21 `updateProjectV2DraftIssue` body edits, read back verified); the `gh project item-edit` CLI path itself has not been executed here.

**Status options (verified 2026-11-07):** the "ReForwarder Backlog" Status field (`PVTSSF_lADOFOOdac4BqhY4zho4LJU`) has exactly six options, read via the §4.3 field lookup (`options { id name }`): Feature Available (`c0c61470`), Feature Done (`a9cfd3a4`), Features Incoming (`db447872`), To-Dos (`59b3d0a6`), Incomplete (`0e649187`), In Progress (`26f37449`). Use these names/ids verbatim. How an item's `BACKLOG.md` priority/state maps onto these six columns is not yet documented — confirm the intended mapping before scripting.

### 4.3 GraphQL equivalent

Verified against docs.github.com, "Using the API to manage projects", and against the live GitHub GraphQL schema by introspection (2026-11-06): the draft-item mutation is **`addProjectV2DraftIssue`** (input `AddProjectV2DraftIssueInput`: `projectId`, `title`, `body`; `AddProjectV2DraftItemInput` does not exist in the schema; payload `AddProjectV2DraftIssuePayload` → `projectItem { id }`). Same scopes as §4.1 apply. Run via `gh api graphql -f query='…'`.

```graphql
# Project node id (organization-owned; use user(login:) for user-owned; <NUMBER> = the
# "ReForwarder Backlog" project number from §4.1)
query { organization(login: "ReForwarder") { projectV2(number: <NUMBER>) { id } } }

# Status field id + option ids (single-select fields come back as ProjectV2SingleSelectField)
query { organization(login: "ReForwarder") { projectV2(number: <NUMBER>) {
  field(name: "Status") { ... on ProjectV2SingleSelectField { id name options { id name } } }
} } }

# Create the draft item (board note) — content sourced from BACKLOG.md
mutation { addProjectV2DraftIssue(input: { projectId: "<PROJECT_ID>", title: "[RF-Fxxx] Title", body: "<spec + acceptance criteria>" }) { projectItem { id } } }

# Set Status — a SEPARATE call; you cannot add and update in one call
mutation { updateProjectV2ItemFieldValue(input: {
  projectId: "<PROJECT_ID>", itemId: "<ITEM_ID>", fieldId: "<FIELD_ID>",
  value: { singleSelectOptionId: "<OPTION_ID>" }
}) { projectV2Item { id } } }
```

## 5. Automation-safety rules

1. **No tokens in source.** Never write a token value, a `GH_TOKEN=` assignment, or keyring contents into any repo file, command output committed to disk, or CI config. Authentication comes only from the local `gh auth` session; scopes are granted interactively via `gh auth refresh` (browser/device flow). If a token ever lands in a file, treat it as compromised: rotate it before anything else.
2. **No destructive operations.** Never delete project items — archive instead (`gh project item-archive --id <ITEM_ID>`, verified against the installed `gh` v2.100.0 help) or change Status. Never run `git reset --hard`, force-push, or discard dirty working-tree entries without recording their content first (the 34 audit-time dirty entries are the standing example: preserved, uncommitted, disposition still open). Never run `gh auth refresh --remove-scopes/--reset-scopes` from automation.
3. **A changed file alone never marks a feature Done.** Done requires (a) the test gate passing — `go test ./... -count=1` — and (b) recorded evidence (commit sha, and where applicable a draft-item evidence note with that evidence). A doc edit, a file change, or a board move without that evidence must not set Done in `BACKLOG.md`, the "ReForwarder Backlog" board, or `STATUS_HISTORY.md`.
4. **The ledger is append-only.** `STATUS_HISTORY.md` is never rewritten; corrections are new entries citing what they supersede.
5. **Research records stay frozen.** `docs/product-roadmap/*` and `todo.md`'s research sections are never edited to reflect new statuses; the sync surfaces in §1 carry the live state.
